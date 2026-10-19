# Security policy

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's [private vulnerability reporting](https://github.com/DonBlack-117/watchkeeper/security/advisories/new) instead, with steps to reproduce and the affected version or commit. You will get an answer within 7 days.

## Scope

Watchkeeper makes outbound requests to user-configured targets, so these areas matter most:

- Bypasses of the private-address guard (SSRF), for example through redirects, DNS rebinding or IPv6 forms.
- Access to the admin API without `WK_API_TOKEN`.
- Information exposed through `/api/v1/status` beyond check names and states.

## Deployment notes

- Keep `WK_ALLOW_PRIVATE_TARGETS=false` on any server reachable by other people.
- Set a random `WK_API_TOKEN` of at least 32 characters (`openssl rand -hex 32`) and a dedicated `WK_DATABASE_URL` with `sslmode=require`.
