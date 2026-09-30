"""Run the two original live telemetry gates only against verified owned targets."""
from __future__ import annotations
import json, secrets, signal, subprocess, time, urllib.parse, urllib.request, uuid
from contextlib import contextmanager
from pathlib import Path
import components
from env import _private
from runner import command_env


def emit(name, status, actual):
    print(json.dumps({'id': name, 'name': name, 'module': 'local-live-gates', 'type': 'integration', 'evidence_mode': 'real_local', 'status': status, 'actual': actual}, ensure_ascii=False), flush=True)


def targets(root):
    root, state, work = components.owned(root)
    ports = {'prometheus': 18570, 'loki': 18573, 'tempo': 18574}
    for provider, port in ports.items():
        info = components.inspect(components.name(state['owner'], provider))
        if not info or not info.get('State', {}).get('Running'):
            raise RuntimeError('owned ' + provider + ' is unavailable')
        components.verify_owner(info, state['owner'])
        mappings = [m for group in info.get('NetworkSettings', {}).get('Ports', {}).values() for m in group or []]
        if not any(m.get('HostIp') == '127.0.0.1' and m.get('HostPort') == str(port) for m in mappings):
            raise RuntimeError('owned ' + provider + ' loopback binding mismatch')
    return root, state, work


def seed(root, state, work):
    marker = 'local-live-' + uuid.uuid4().hex
    labels = {'application': 'testlab', 'environment': 'test', 'testlab_owner': state['owner']}
    line = json.dumps({'level': 'error', 'message_template': 'controlled local telemetry gate', 'message': marker})
    payload = {'streams': [{'stream': labels, 'values': [[str(time.time_ns()), line]]}]}
    req = urllib.request.Request('http://127.0.0.1:18573/loki/api/v1/push', json.dumps(payload).encode(), {'Content-Type': 'application/json'})
    with urllib.request.urlopen(req, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError('owned Loki refused exact synthetic log')
    selector = '{' + ','.join(k + '=' + json.dumps(v) for k, v in sorted(labels.items())) + '} |= ' + json.dumps(marker)
    deadline = time.monotonic() + 45
    while True:
        now = time.time_ns()
        query = urllib.parse.urlencode({'query': selector, 'start': now - 300_000_000_000, 'end': now, 'limit': 10})
        with urllib.request.urlopen('http://127.0.0.1:18573/loki/api/v1/query_range?' + query, timeout=10) as response:
            raw = response.read(1024 * 1024 + 1)
            if len(raw) > 1024 * 1024:
                raise RuntimeError('owned Loki response exceeded budget')
            data = json.loads(raw)
        if any(value[1] == line for stream in data.get('data', {}).get('result', []) for value in stream.get('values', [])):
            break
        if time.monotonic() >= deadline:
            raise RuntimeError('owned Loki did not return exact log within 45 seconds')
        time.sleep(1)
    path = work / 'telemetry-local-readcheck.json'
    _private(path, {'loki': {'url': 'http://127.0.0.1:18573', 'allow_http': True}, 'applications': {'lab-order-api': {'loki_labels': labels, 'loki_pattern_field': 'message_template', 'loki_complete_lookback_seconds': 86400}}, 'timeout_seconds': 10, 'max_window_seconds': 3600, 'max_lookback_seconds': 86400, 'max_log_entries': 100})
    emit('original_live_gate_seed', 'passed', {'owner': state['owner'], 'synthetic_log_exact_readback': True, 'application_key': 'lab-order-api', 'historical_window_proof': False})
    return path


def run_go(root, explicit, package, name):
    environment = command_env(explicit, {})
    environment['GOCACHE'] = str(root / '.local/go-cache')
    result = subprocess.run(['go', 'test', '-mod=readonly', '-race', '-json', '-count=1', '-run', '^' + name + '$', '-timeout', '90s', package], cwd=root, env=environment, text=True, capture_output=True, timeout=120)
    logs = {}
    terminal = 0
    for line in result.stdout.splitlines():
        frame = json.loads(line)
        case = frame.get('Test', frame.get('Package', package))
        if frame.get('Output'):
            logs[case] = (logs.get(case, '') + frame['Output'])[-8192:]
        if frame.get('Action') in ('pass', 'fail', 'skip'):
            terminal += 1
            emit(case, {'pass': 'passed', 'fail': 'failed', 'skip': 'skipped'}[frame['Action']], logs.get(case, ''))
    if not terminal or result.returncode:
        raise RuntimeError('original live gate failed or produced no terminal results: ' + name)


@contextmanager
def grafana(state, work):
    # This transient health-probe component has no product data or upstream writes.
    # Official fixed image: https://grafana.com/grafana/download/12.1.1?edition=oss&platform=docker
    image = 'grafana/grafana:12.1.1'
    port = 18600
    components.port_free(port)
    ident = uuid.uuid4().hex
    name = 'aiops-' + state['owner'] + '-grafana-' + ident[:12]
    if components.inspect(name) is not None:
        raise RuntimeError('transient Grafana name is already allocated')
    environment = work / ('grafana-' + ident + '.env')
    environment.write_text('GF_SECURITY_ADMIN_USER=testlab\nGF_SECURITY_ADMIN_PASSWORD=' + secrets.token_hex(24) + '\nGF_USERS_ALLOW_SIGN_UP=false\nGF_ANALYTICS_REPORTING_ENABLED=false\nGF_ANALYTICS_CHECK_FOR_UPDATES=false\nGF_ANALYTICS_CHECK_FOR_PLUGIN_UPDATES=false\nGF_PLUGINS_PREINSTALL_DISABLED=true\n')
    environment.chmod(0o600)
    try:
        components.cmd(['docker', 'pull', image], timeout=240)
        components.cmd(['docker', 'run', '-d', '--name', name, '--label', components.LABEL + '=' + state['owner'], '--label', 'aiops.testlab.run=' + ident, '--memory', '256m', '--cpus', '0.5', '--env-file', str(environment), '-p', '127.0.0.1:18600:3000', image], timeout=60)
        info = components.inspect(name)
        if not info or info['Config']['Image'] != image:
            raise RuntimeError('Grafana allocation/image mismatch')
        components.verify_owner(info, state['owner'])
        health = components.wait_http('http://127.0.0.1:18600/api/health', seconds=90)
        if not isinstance(health, dict) or health.get('database') != 'ok' or health.get('version') != '12.1.1':
            raise RuntimeError('owned Grafana health/version mismatch')
        emit('original_live_gate_grafana_ready', 'passed', {'image': image, 'image_id': info.get('Image'), 'port': port, 'database': 'ok', 'scope': 'read-only provider health probe, not dashboard/query acceptance'})
        yield
    finally:
        info = components.inspect(name)
        if info is not None:
            components.verify_owner(info, state['owner'])
            if info.get('Config', {}).get('Labels', {}).get('aiops.testlab.run') != ident:
                raise RuntimeError('refusing mismatched transient Grafana cleanup')
            components.cmd(['docker', 'rm', '-f', name])


def main():
    root = Path(__file__).resolve().parents[2]
    try:
        root, state, work = targets(root)
        config = seed(root, state, work)
        run_go(root, {'AIOPS_TESTLAB_TELEMETRY_CONFIG': str(config)}, './internal/connectors', 'TestLocalReadcheck')
        with grafana(state, work):
            run_go(root, {'AIOPS_TEST_PROMETHEUS_URL': 'http://127.0.0.1:18570', 'AIOPS_TEST_LOKI_URL': 'http://127.0.0.1:18573', 'AIOPS_TEST_TEMPO_URL': 'http://127.0.0.1:18574', 'AIOPS_TEST_GRAFANA_URL': 'http://127.0.0.1:18600'}, './internal/v3integrations', 'TestRealLocalConnectorProbes')
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        emit('original_live_gates', 'failed', {'error': type(error).__name__ + ': ' + str(error)})
        return 1
    return 0


if __name__ == '__main__':
    def cancelled(signum, frame):
        raise KeyboardInterrupt('owned telemetry gate cancelled')
    signal.signal(signal.SIGTERM, cancelled)
    raise SystemExit(main())
