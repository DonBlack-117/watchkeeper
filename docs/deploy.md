# Deploying to AWS

Watchkeeper runs on one EC2 instance with Docker Compose: the app, PostgreSQL and Caddy, which gets the HTTPS certificate from Let's Encrypt. Every push to `main` that passes CI publishes a multi-arch image to GitHub Container Registry, and the `Deploy` workflow rolls it out over SSH.

```text
GitHub Actions ──push image──▶ ghcr.io/donblack-117/watchkeeper
       │
       └──ssh: docker compose pull && up -d──▶ EC2
                                                ├─ caddy :80/:443  (Let's Encrypt)
                                                ├─ app   :8090     (Go, embedded React UI)
                                                └─ postgres        (volume pgdata)
```

## 1. Make the image public

After the first successful `Deploy` run, open the package at `github.com/DonBlack-117?tab=packages`, choose **Package settings** and set the visibility to **Public**, so the server can pull it without credentials.

## 2. Create the instance

In the EC2 console (region of your choice):

1. **Launch instance** with Amazon Linux 2023. `t4g.small` (ARM, 2 GiB) or `t3.micro` (x86, 1 GiB) are enough; the image is built for both.
2. Key pair: create one (`ed25519`) and keep the `.pem` file.
3. Security group: allow **HTTP 80** and **HTTPS 443** from anywhere, and **SSH 22**. GitHub-hosted runners have no fixed addresses, so automatic deploys need port 22 open to anywhere; that is acceptable only because password logins are off (the Amazon Linux default) and the deploy key can run nothing but the roll-out script (step 4). If you prefer manual deploys, keep 22 limited to your IP and skip step 4.
4. Storage: 20 GiB gp3.
5. **Advanced details → User data:** paste [`deploy/ec2-user-data.sh`](../deploy/ec2-user-data.sh). It installs Docker and Compose (checking the release checksum), adds swap, creates `/opt/watchkeeper`, and creates a `deploy` user that is not in the `docker` group and may only run [`deploy/roll.sh`](../deploy/roll.sh) through `sudo`.
6. Allocate an **Elastic IP** and associate it with the instance, so the address survives restarts.

## 3. Configure the server

Without a domain, use the Elastic IP through [sslip.io](https://sslip.io): `3.91.10.20` becomes `3-91-10-20.sslip.io`, which resolves to that IP and gets a real certificate.

```bash
scp -i key.pem deploy/compose.yaml deploy/Caddyfile deploy/.env.example ec2-user@3.91.10.20:/opt/watchkeeper/
ssh -i key.pem ec2-user@3.91.10.20
cd /opt/watchkeeper
cp .env.example .env && chmod 600 .env
openssl rand -hex 24   # POSTGRES_PASSWORD
nano .env              # WK_DOMAIN, POSTGRES_PASSWORD, WK_ADMIN_USER, WK_ADMIN_PASSWORD
docker compose up -d
docker compose logs -f app
```

Open `https://<WK_DOMAIN>` and sign in. The public page is `https://<WK_DOMAIN>/status`.

To use a real domain later, point an `A` record at the Elastic IP, change `WK_DOMAIN` and run `docker compose up -d`.

## 4. Turn on automatic deploys

First give GitHub its own key, limited to the roll-out script. On your laptop:

```bash
ssh-keygen -t ed25519 -N "" -C watchkeeper-deploy -f wk-deploy
ssh -i key.pem ec2-user@3.91.10.20 \
  "echo 'restrict,command=\"sudo /usr/local/sbin/watchkeeper-roll\" $(cat wk-deploy.pub)' | sudo tee /home/deploy/.ssh/authorized_keys && sudo chown deploy: /home/deploy/.ssh/authorized_keys && sudo chmod 600 /home/deploy/.ssh/authorized_keys"
```

`restrict` turns off forwarding and terminals, and `command=` replaces whatever the client asks to run. The script reads the requested image from `SSH_ORIGINAL_COMMAND` and refuses anything that is not `ghcr.io/donblack-117/watchkeeper:sha-<12 hex>`, so a leaked deploy key can only roll the app to another published commit.

Then, in the repository:

1. **Settings → Secrets and variables → Actions → Variables:** add `DEPLOY_HOST` (the Elastic IP) and `DEPLOY_DOMAIN` (the value of `WK_DOMAIN`). They are repository variables because the job checks `DEPLOY_HOST` before it enters the environment; until it is set, the deploy job is skipped and only the image is published.
2. **Settings → Environments → New environment** `production`, with two secrets:

| Secret               | Value                                           |
| -------------------- | ----------------------------------------------- |
| `DEPLOY_SSH_KEY`     | the private key (contents of `wk-deploy`)       |
| `DEPLOY_KNOWN_HOSTS` | output of `ssh-keyscan -t ed25519 <Elastic IP>` |

In the environment, set **Deployment branches** to `main` only; you can also add a required reviewer before each deploy.

From then on each push to `main` builds, tests, publishes `ghcr.io/donblack-117/watchkeeper:sha-<commit>`, updates `WK_IMAGE` in the server's `.env`, restarts the app and waits for `/api/v1/health`.

## Operations

```bash
docker compose ps
docker compose logs --tail 100 app
# Backup
docker compose exec -T postgres pg_dump -U watchkeeper watchkeeper | gzip > watchkeeper-$(date +%F).sql.gz
# Roll back to an earlier image
sed -i 's#^WK_IMAGE=.*#WK_IMAGE=ghcr.io/donblack-117/watchkeeper:sha-<old>#' .env && docker compose up -d app
```

Cost: a `t4g.small` or `t3.micro` with 20 GiB of gp3 and an Elastic IP in use stays around 8 to 15 USD a month after the free tier credits.
