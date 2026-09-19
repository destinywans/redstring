#!/usr/bin/env bash
# NOT EXECUTED: run only after the user explicitly authorizes testing.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
go run ./demo/telemetry/verify -config "$root/.local/aiops-canvas-demo/telemetry.json"
