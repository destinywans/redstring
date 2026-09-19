#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ -e .env ]; then
  printf '%s\n' '.env already exists; preserved.'
  exit 0
fi
umask 077
db_password=$(openssl rand -hex 24)
admin_password=$(openssl rand -hex 24)
cat > .env <<EOF
POSTGRES_PASSWORD=$db_password
POSTGRES_PORT=15432
DATABASE_URL=postgres://aiops:$db_password@127.0.0.1:15432/aiops?sslmode=disable
AIOPS_ADDR=127.0.0.1:8080
AIOPS_ADMIN_EMAIL=admin@localhost
AIOPS_ADMIN_PASSWORD=$admin_password
OLLAMA_MODEL=qwen3.6:latest
OLLAMA_BASE_URL=http://127.0.0.1:11434/v1
EMBEDDING_PROVIDER=ollama
EMBEDDING_MODEL=bge-m3
EMBEDDING_BASE_URL=http://127.0.0.1:11434
EOF
printf '%s\n' 'Created private .env; credentials are stored there and are not printed.'
