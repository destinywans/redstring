#!/usr/bin/env bash
# NOT EXECUTED: awaiting explicit user permission for test deployment.
set -euo pipefail
umask 077
root="$(cd "$(dirname "$0")/.." && pwd)"
lab="$root/.local/aiops-canvas-demo"
build="$lab/telemetry-build"
# Never invoke lab-up here: it would reset the existing checkout Deployment.
if [[ ! -f "$lab/kubeconfig" ]]; then
  echo "Create the dedicated aiops-canvas-demo lab first; no default context is used." >&2
  exit 1
fi
k=(kubectl --kubeconfig "$lab/kubeconfig" --context kind-aiops-canvas-demo -n aiops-demo)
"${k[@]}" get deployment checkout >/dev/null
mkdir -p "$build"
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo "Unsupported Docker architecture" >&2; exit 1;; esac
(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$build/collector" ./demo/telemetry/collector)
cp "$root/demo/telemetry/Dockerfile" "$build/Dockerfile"
BUILDX_CONFIG="$build/buildx" docker build -t aiops-canvas-telemetry-collector:local "$build"
docker pull prom/prometheus:v3.13.3
docker pull grafana/loki:3.7.0
kind load docker-image aiops-canvas-telemetry-collector:local prom/prometheus:v3.13.3 grafana/loki:3.7.0 --name aiops-canvas-demo
"${k[@]}" apply -f "$root/demo/telemetry/resources.yaml"
"${k[@]}" rollout status deployment/canvas-prometheus --timeout=180s
"${k[@]}" rollout status deployment/canvas-loki --timeout=180s
"${k[@]}" rollout status deployment/canvas-log-collector --timeout=180s
"${k[@]}" create token canvas-telemetry-reader --duration=24h > "$lab/telemetry-token"
python3 - "$lab" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]); base=json.loads((p/'connector.json').read_text())
def endpoint(service,port):
    return {'url':base['api_url']+'/api/v1/namespaces/aiops-demo/services/http:'+service+':'+str(port)+'/proxy', 'ca_file':base['ca_file'],'bearer_token_file':str(p/'telemetry-token')}
queries={
  'requests':'sum(rate(checkout_requests_total{application="demo",namespace="aiops-demo"}[1m]))',
  'errors':'sum(rate(checkout_errors_total{application="demo",namespace="aiops-demo"}[1m]))',
  'up':'up{application="demo",namespace="aiops-demo",job="canvas-checkout"}'
}
config={'prometheus':endpoint('canvas-prometheus',9090),'loki':endpoint('canvas-loki',3100),'applications':{'demo':{'prometheus_queries':queries,'loki_labels':{'application':'demo','namespace':'aiops-demo','app':'checkout'}}},'timeout_seconds':15,'max_window_seconds':3600,'max_lookback_seconds':7200,'min_step_seconds':5,'max_samples_per_series':1000,'max_log_entries':1000}
(p/'telemetry.json').write_text(json.dumps(config,indent=2)+'\n')
PY
echo "Telemetry deployed with private ClusterIP services. Wait for sufficient real samples, then explicitly run the verification command."
echo "AIOPS_TELEMETRY_CONFIG=$lab/telemetry.json"
