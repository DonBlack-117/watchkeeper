#!/bin/bash
# EC2 user data for Amazon Linux 2023: installs Docker and the Compose plugin,
# adds 1 GiB of swap for small instances, prepares /opt/watchkeeper and creates
# the "deploy" user that GitHub Actions logs in as.
set -euo pipefail

COMPOSE_VERSION=v5.6.0
ROLL_URL=https://raw.githubusercontent.com/DonBlack-117/watchkeeper/main/deploy/roll.sh

dnf install -y docker
systemctl enable --now docker
usermod -aG docker ec2-user

# Compose plugin, checked against the checksum published with the release.
arch=$(uname -m) # x86_64 or aarch64
name="docker-compose-linux-${arch}"
tmp=$(mktemp -d)
curl -fsSL -o "$tmp/$name" "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/${name}"
curl -fsSL -o "$tmp/$name.sha256" "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/${name}.sha256"
(cd "$tmp" && sha256sum -c "$name.sha256")
install -D -m 755 "$tmp/$name" /usr/local/lib/docker/cli-plugins/docker-compose
rm -rf "$tmp"

if [ ! -f /swapfile ]; then
  dd if=/dev/zero of=/swapfile bs=1M count=1024
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap defaults 0 0' >> /etc/fstab
fi

install -d -o ec2-user -g ec2-user -m 750 /opt/watchkeeper

# Deploy user: not in the docker group. Its key may only run roll.sh through
# sudo (forced command in authorized_keys, added by hand; see docs/deploy.md).
useradd --create-home --shell /bin/bash deploy || true
install -d -o deploy -g deploy -m 700 /home/deploy/.ssh
curl -fsSL -o /usr/local/sbin/watchkeeper-roll "$ROLL_URL"
chown root:root /usr/local/sbin/watchkeeper-roll
chmod 755 /usr/local/sbin/watchkeeper-roll
cat > /etc/sudoers.d/watchkeeper-deploy <<'SUDO'
Defaults:deploy env_keep += "SSH_ORIGINAL_COMMAND"
deploy ALL=(root) NOPASSWD: /usr/local/sbin/watchkeeper-roll
SUDO
chmod 440 /etc/sudoers.d/watchkeeper-deploy
visudo -cf /etc/sudoers.d/watchkeeper-deploy
