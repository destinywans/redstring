#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
k=(kubectl --kubeconfig "$root/.local/aiops-canvas-demo/kubeconfig" --context kind-aiops-canvas-demo -n aiops-demo)
"${k[@]}" set env deployment/checkout MODE=regression
"${k[@]}" rollout status deployment/checkout --timeout=120s
echo "Regression injected. Pods are ready but real checkout requests return HTTP 500."
