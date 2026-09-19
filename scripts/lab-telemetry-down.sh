#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
# Delete only named telemetry resources; checkout, traffic and the cluster survive.
kubectl --kubeconfig "$root/.local/aiops-canvas-demo/kubeconfig" --context kind-aiops-canvas-demo -n aiops-demo delete -f "$root/demo/telemetry/resources.yaml" --ignore-not-found
