# Watchkeeper

[![CI](https://github.com/DonBlack-117/watchkeeper/actions/workflows/ci.yml/badge.svg)](https://github.com/DonBlack-117/watchkeeper/actions/workflows/ci.yml)

Uptime monitor and status page with AI incident triage, written in Go.

Watchkeeper checks your HTTP endpoints, DNS records and TLS certificates on a schedule, stores every run in PostgreSQL and serves the history through a REST API. Later phases add a React dashboard, Prometheus metrics and an agent that uses Claude to investigate incidents.

> **Status:** phase 1 (the Go core) is done. See the [roadmap](#roadmap).

## Features

- **HTTP checks** with the time spent in each phase (DNS, connect, TLS, time to first byte), expected status codes and certificate expiry.
- **DNS checks** that resolve a hostname.
- **TLS checks** that fail 7 days before a certificate expires.
- **Scheduler** with one goroutine per check, random start jitter, a concurrency limit and graceful shutdown. Checks created or edited through the API are picked up without a restart.
- **REST API** with cursor pagination, 90-day daily uptime and [RFC 9457 problem details](docs/problems.md).
- **Public status endpoint** that exposes names and states, never targets or errors.

## Quick start

Requirements: Go 1.26+, and Podman or Docker.

```bash
make db          # PostgreSQL 16 on 127.0.0.1:5432
make run         # applies migrations and listens on 127.0.0.1:8090
make seed        # creates a few example checks (in another terminal)
```

```bash
curl -s localhost:8090/api/v1/status | jq
```

Settings come from environment variables; see [`.env.example`](.env.example).

## API

| Method and path                                            | Description                                                      |
| ---------------------------------------------------------- | ---------------------------------------------------------------- |
| `GET /api/v1/health`                                       | Service and database health                                      |
| `GET /api/v1/checks`                                       | All checks with their current status and last result             |
| `POST /api/v1/checks`                                      | Create a check (`201` with `Location`)                           |
| `GET /api/v1/checks/{id}`                                  | One check                                                        |
| `PATCH /api/v1/checks/{id}`                                | Change any field, for example `{"enabled": false}`               |
| `DELETE /api/v1/checks/{id}`                               | Delete a check and its results                                   |
| `GET /api/v1/checks/{id}/results?limit=&cursor=&from=&to=` | Run history, newest first                                        |
| `GET /api/v1/checks/{id}/uptime?days=90`                   | Availability per UTC day; days without runs have `percent: null` |
| `GET /api/v1/status`                                       | Public summary for a status page                                 |

Create a check:

```bash
curl -s -X POST localhost:8090/api/v1/checks \
  -H 'Content-Type: application/json' \
  -d '{"name":"Bitso API","kind":"http","target":"https://api.bitso.com/v3/ticker/?book=btc_mxn","intervalS":60}'
```

| Field            | Default        | Rules                                                       |
| ---------------- | -------------- | ----------------------------------------------------------- |
| `name`           |                | 1 to 100 characters                                         |
| `kind`           |                | `http`, `dns` or `tls`                                      |
| `target`         |                | URL for `http`, hostname for `dns`, `host[:port]` for `tls` |
| `intervalS`      | 60             | 30 to 86400                                                 |
| `timeoutMs`      | 10000          | 1000 to 30000, shorter than the interval                    |
| `expectedStatus` | any 2xx or 3xx | `http` only                                                 |
| `enabled`        | `true`         |                                                             |

A check is **operational** when its last three runs passed, **degraded** when some failed or the last one took more than 2 s, and **down** when all three failed.

## Security

- **Private targets are blocked by default.** Every connection is checked at dial time, so loopback, private ranges, link-local addresses (including cloud metadata) and redirects or DNS answers that point to them fail with `blocked`. `make run` sets `WK_ALLOW_PRIVATE_TARGETS=true` for local development; keep it off on a public server.
- **Admin API behind a token.** With `WK_API_TOKEN` set, every endpoint except `/status` and `/health` needs `Authorization: Bearer <token>`. Watchkeeper refuses to start on a non-loopback address without a token.
- **Public status is cached and rate limited:** 15 s cache and 60 requests per minute per client IP.
- Errors stored with results are short categories (`timeout`, `connection refused`, `dns: no such host`), never raw network errors with internal addresses.
- Results older than `WK_RETENTION_DAYS` (90) are deleted every hour in small batches.

## Development

```bash
make test        # unit and integration tests with the race detector
make test-short  # skips the PostgreSQL integration tests
make lint        # go vet and golangci-lint
```

Integration tests start PostgreSQL with [Testcontainers](https://golang.testcontainers.org/). With rootless Podman, start the socket once (`systemctl --user start podman.socket`); the Makefile points Testcontainers at it.

```text
cmd/watchkeeper/     entry point, configuration and graceful shutdown
internal/check/      check types, validation, status rules and runners
internal/scheduler/  one goroutine per check, reloads the list every 30 s
internal/store/      PostgreSQL with pgx; embedded goose migrations
internal/api/        REST handlers, problem details and request logging
migrations/          SQL migrations
```

## Roadmap

1. **Go core** — checks, scheduler, storage, REST API. ✅
2. **Dashboard** — React and TypeScript UI, public status page, deployment on AWS with CI/CD.
3. **Observability** — Prometheus metrics, Grafana dashboards, alerts to Telegram or Discord.
4. **AI triage** — an agent that uses Claude tool use (`dig`, `curl`, history queries) to explain incidents, plus an MCP server.

Security reports: see [SECURITY.md](SECURITY.md).

## License

MIT. See [LICENSE](LICENSE).
