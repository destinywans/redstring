#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
go run ./demo/labcli -config "$root/.local/aiops-canvas-demo/connector.json" observe
