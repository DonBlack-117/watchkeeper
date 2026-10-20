#!/bin/bash
# Forced command of the deploy key (see docs/deploy.md): the only thing GitHub
# Actions can run on the server. It takes the image tag from the SSH command,
# accepts only this repository's commit tags and restarts the app with it.
set -euo pipefail

image="${SSH_ORIGINAL_COMMAND:-}"
if [[ ! "$image" =~ ^ghcr\.io/donblack-117/watchkeeper:sha-[0-9a-f]{12}$ ]]; then
  echo "refused: not a watchkeeper commit image: ${image:0:100}" >&2
  exit 2
fi

cd /opt/watchkeeper
if ! grep -q '^WK_IMAGE=' .env; then
  echo "WK_IMAGE is missing from /opt/watchkeeper/.env" >&2
  exit 1
fi
sed -i "s#^WK_IMAGE=.*#WK_IMAGE=${image}#" .env
docker compose pull app
docker compose up -d --remove-orphans
docker image prune -f
