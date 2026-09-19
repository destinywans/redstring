#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ ! -f .env ]; then
  printf '%s\n' 'Run ./scripts/dev-init.sh and docker compose up -d --wait db first.' >&2
  exit 1
fi
set -a
. ./.env
set +a
export GOCACHE="$PWD/.local/go-cache"
go build -o bin/aiops ./cmd/aiops
exec ./bin/aiops
