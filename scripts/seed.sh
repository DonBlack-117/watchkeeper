#!/usr/bin/env bash
# Creates the example checks through the API. Usage: scripts/seed.sh [base-url]
set -euo pipefail

BASE="${1:-http://127.0.0.1:8090}/api/v1/checks"

create() {
	curl -fsS -X POST "$BASE" -H 'Content-Type: application/json' -d "$1" >/dev/null
	echo "created: $1"
}

create '{"name":"bitcoin-trading-bot · API","kind":"http","target":"http://localhost:8080/api/analysis/cryptos"}'
create '{"name":"Bitso API (externa)","kind":"http","target":"https://api.bitso.com/v3/ticker/?book=btc_mxn"}'
create '{"name":"GitHub · battery-samsung","kind":"http","target":"https://github.com/DonBlack-117/battery-samsung"}'
create '{"name":"GitHub · DNS","kind":"dns","target":"github.com"}'
create '{"name":"GitHub · certificado TLS","kind":"tls","target":"github.com","intervalS":3600}'
