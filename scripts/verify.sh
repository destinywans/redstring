#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
export GOCACHE="$PWD/.local/go-cache"
if [ "${1:-}" = --postgres ]; then
  if [ ! -f .env ]; then
    printf '%s\n' 'Missing .env for dedicated local PostgreSQL. Run make init db.' >&2
    exit 1
  fi
  set -a
  . ./.env
  set +a
  export TEST_DATABASE_URL="$DATABASE_URL"
  # Each suite creates and drops only its own randomly named schema.
  # Model/Kubernetes actions are excluded unless a separate smoke is invoked.
fi
go test -race ./...
go vet ./...
python3 scripts/check-contract.py
printf '%s\n' 'Verification passed. Real model and live Kubernetes smoke are separate evidence.'
