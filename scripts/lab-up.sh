#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
lab="$root/.local/aiops-canvas-demo"
mkdir -p "$lab/build"
chmod 700 "$lab"
if ! kind get clusters 2>/dev/null | rg -qx aiops-canvas-demo; then
  kind create cluster --name aiops-canvas-demo --image kindest/node:v1.35.8 --config "$root/demo/kind.yaml" --kubeconfig "$lab/kubeconfig" --wait 120s
else
  kind get kubeconfig --name aiops-canvas-demo > "$lab/kubeconfig"
fi
chmod 600 "$lab/kubeconfig"
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo "unsupported Docker architecture" >&2; exit 1;; esac
(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$lab/build/app" ./demo/service)
cp "$root/demo/Dockerfile" "$lab/build/Dockerfile"
BUILDX_CONFIG="$lab/buildx" docker build -t aiops-canvas-demo:local "$lab/build"
kind load docker-image aiops-canvas-demo:local --name aiops-canvas-demo
k=(kubectl --kubeconfig "$lab/kubeconfig" --context kind-aiops-canvas-demo)
"${k[@]}" apply -f "$root/demo/resources.yaml"
"${k[@]}" -n aiops-demo rollout status deployment/checkout --timeout=120s
"${k[@]}" -n aiops-demo rollout status deployment/traffic --timeout=120s
"${k[@]}" -n aiops-demo create token canvas-connector --duration=24h > "$lab/token"
chmod 600 "$lab/token"
"${k[@]}" config view --raw -o json | python3 -c 'import json,sys,base64,pathlib; c=json.load(sys.stdin)["clusters"][0]["cluster"]; p=pathlib.Path(sys.argv[1]); (p/"ca.crt").write_bytes(base64.b64decode(c["certificate-authority-data"])); (p/"connector.json").write_text(json.dumps({"api_url":c["server"],"ca_file":str(p/"ca.crt"),"token_file":str(p/"token"),"namespace":"aiops-demo","deployment":"checkout","service":"checkout","port":8080},indent=2))' "$lab"
echo "Dedicated lab ready. Connector config: $lab/connector.json (token valid up to 24 hours)."
