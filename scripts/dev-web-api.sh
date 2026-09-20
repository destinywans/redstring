#!/bin/sh
# Companion API for Vite's fixed 127.0.0.1:5173 development origin.
set -eu
cd "$(dirname "$0")/.."
if [ ! -f .env ]; then
  printf '%s\n' 'Run make init db first; local credentials belong in .env.' >&2
  exit 1
fi
set -a
. ./.env
set +a
# Apply after loading .env so its ordinary :8080 setting cannot break the proxy.
export AIOPS_ADDR=127.0.0.1:18080
export AIOPS_PUBLIC_ORIGIN=http://127.0.0.1:5173
export GOCACHE="$PWD/.local/go-cache"
mkdir -p bin
go build -o bin/aiops ./cmd/aiops
exec ./bin/aiops
