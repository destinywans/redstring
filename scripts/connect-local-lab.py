#!/usr/bin/env python3
"""Register the isolated aiops-test data sources in a local review workspace.

Requires an existing private runtime JSON and the explicitly selected lab
kubeconfig. Credentials, account data and generated bindings stay in .local/.
Never switches kubectl context or enables infrastructure writes/notifications.
"""
import argparse
import base64
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import subprocess
import urllib.error
import urllib.request
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent.parent
LOCAL = ROOT / '.local/product-lab'
ROLES = ['order-api', 'checkout-api', 'message-api']


def save(name, value):
    LOCAL.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = LOCAL / name
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    path.chmod(0o600)


def kube(lab, *args, data=None):
    return subprocess.check_output(['kubectl', '--kubeconfig', str(lab / '.local/kubeconfig'),
                                    '--context', 'kind-aiops-test', *args], input=data, text=True)


def vm_queries():
    scope = 'job="aiops-test-vm",application="aiops-test-vm",environment="lab"'
    return {
        'vm_up': 'up{' + scope + '}',
        'vm_cpu_used_percent': '100 * (1 - avg by (instance) (rate(node_cpu_seconds_total{' + scope + ',mode="idle"}[2m])))',
        'vm_memory_used_percent': '100 * (1 - node_memory_MemAvailable_bytes{' + scope + '} / node_memory_MemTotal_bytes{' + scope + '})',
        'vm_memory_available_bytes': 'node_memory_MemAvailable_bytes{' + scope + '}',
        'vm_rootfs_used_percent': '100 * (1 - node_filesystem_avail_bytes{' + scope + ',mountpoint="/"} / node_filesystem_size_bytes{' + scope + ',mountpoint="/"})',
        'vm_network_receive_bytes_per_second': 'sum by (instance) (rate(node_network_receive_bytes_total{' + scope + ',device!="lo"}[2m]))',
        'vm_network_transmit_bytes_per_second': 'sum by (instance) (rate(node_network_transmit_bytes_total{' + scope + ',device!="lo"}[2m]))',
        'vm_systemd_unit_state': 'node_systemd_unit_state{' + scope + ',name="aiops-test-node-exporter.service",state="active"}',
    }


def prepare(args):
    base = json.loads(args.runtime_config.read_text())
    database = urlsplit(base.get('DATABASE_URL', ''))
    if (base.get('AIOPS_ADDR') != '127.0.0.1:18081'
            or base.get('AIOPS_PUBLIC_ORIGIN') != 'http://127.0.0.1:18081'
            or database.hostname not in ('localhost', '127.0.0.1')
            or not database.path.startswith('/aiops_') or '_review_' not in database.path):
        raise RuntimeError('This helper only supports the loopback review workspace and an isolated aiops_*_review_* database')
    lab = args.lab.resolve()
    LOCAL.mkdir(mode=0o700, parents=True, exist_ok=True)
    # This service account can only read the registered Deployments and their revisions.
    namespace = 'aiops-lab'
    objects = [
        {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': 'aiops-reader', 'namespace': namespace}},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'Role', 'metadata': {'name': 'aiops-reader', 'namespace': namespace},
         'rules': [{'apiGroups': ['apps'], 'resources': ['deployments'], 'resourceNames': ROLES, 'verbs': ['get']},
                   {'apiGroups': ['apps'], 'resources': ['replicasets'], 'verbs': ['list']}]},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'RoleBinding', 'metadata': {'name': 'aiops-reader', 'namespace': namespace},
         'subjects': [{'kind': 'ServiceAccount', 'name': 'aiops-reader', 'namespace': namespace}],
         'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Role', 'name': 'aiops-reader'}},
    ]
    kube(lab, 'apply', '-f', '-', data=json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': objects}))
    config = json.loads(kube(lab, 'config', 'view', '--raw', '--minify', '-o', 'json'))
    cluster = config['clusters'][0]['cluster']
    ca = LOCAL / 'cluster-ca.crt'
    ca.write_bytes(base64.b64decode(cluster['certificate-authority-data']))
    ca.chmod(0o600)
    token = kube(lab, '-n', namespace, 'create', 'token', 'aiops-reader', '--duration=168h').strip()
    token_file = LOCAL / 'cluster-token'
    token_file.write_text(token + '\n')
    token_file.chmod(0o600)
    claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '==='))
    save('credential-metadata.json', {'kubernetes_token_expires_at_unix': claims['exp'], 'namespace': namespace, 'role': 'read-only'})
    registry = [{'id': 'lab-' + role, 'config': {'api_url': cluster['server'], 'ca_file': str(ca),
                 'token_file': str(token_file), 'namespace': namespace, 'deployment': role}} for role in ROLES]
    save('kubernetes.json', registry)
    applications = {}
    for role in ROLES:
        scope = f'job="aiops-lab",application="{role}",environment="lab",namespace="aiops-lab"'
        http = 'lab_http_requests_total{' + scope + '}'
        queries = {
            'target_up': 'up{' + scope + '}',
            'request_rate': 'sum(rate(' + http + '[2m]))',
            'http_p95_seconds': 'histogram_quantile(0.95, sum by (le) (rate(lab_http_request_duration_seconds_bucket{' + scope + '}[2m])))',
            'dependency_operations_per_second': 'sum by (dependency,result) (rate(lab_dependency_requests_total{' + scope + '}[2m]))',
            **vm_queries(),
        }
        if role == 'order-api':
            queries['completed_orders_per_second'] = 'sum(rate(lab_orders_total{' + scope + ',result="completed"}[2m]))'
        elif role == 'checkout-api':
            queries['published_messages_per_second'] = 'sum(rate(lab_outbox_published_total{' + scope + '}[2m]))'
        else:
            queries['consumed_messages_per_second'] = 'sum by (result) (rate(lab_messages_consumed_total{' + scope + '}[2m]))'
        applications['lab-' + role] = {'prometheus_queries': queries,
                                      'loki_labels': {'application': role, 'environment': 'lab', 'namespace': namespace}}
    # VM is separately selectable; shared VM queries also support application-scoped infrastructure panels.
    applications['lab-vm'] = {'prometheus_queries': vm_queries()}
    save('telemetry.json', {'prometheus': {'url': 'http://127.0.0.1:19090', 'allow_http': True},
         'loki': {'url': 'http://127.0.0.1:13100', 'allow_http': True}, 'applications': applications,
         'timeout_seconds': 15, 'max_window_seconds': 3600, 'max_lookback_seconds': 86400,
         'min_step_seconds': 15, 'max_samples_per_series': 1000, 'max_log_entries': 100})
    old_ops = base.get('AIOPS_OPERATIONS_CONFIG')
    if not (LOCAL / 'operations.json').exists():
        save('operations.json', json.loads(Path(old_ops).read_text()) if old_ops else {'sources': [], 'feishu_channels': []})
    ops = json.loads((LOCAL / 'operations.json').read_text())
    if ops.get('feishu_channels'):
        raise RuntimeError('Review workspace must not contain external notification channels')
    base.update({'AIOPS_TELEMETRY_CONFIG': str(LOCAL / 'telemetry.json'),
                 'AIOPS_KUBERNETES_REGISTRY': str(LOCAL / 'kubernetes.json'),
                 'AIOPS_OPERATIONS_CONFIG': str(LOCAL / 'operations.json'), 'AIOPS_CONNECTOR_CONFIG': '',
                 'OLLAMA_MODEL': 'qwen3.6:latest', 'OLLAMA_BASE_URL': 'http://127.0.0.1:11434/v1',
                 'EMBEDDING_PROVIDER': 'ollama', 'EMBEDDING_MODEL': 'bge-m3:567m',
                 'EMBEDDING_BASE_URL': 'http://127.0.0.1:21434', 'AIOPS_WEB_DIR': str(ROOT / 'web/dist')})
    save('runtime.json', base)
    print('Prepared private runtime, bounded queries and read-only cluster credentials. No infrastructure write permission.')


class Client:
    def __init__(self):
        self.config = json.loads((LOCAL / 'runtime.json').read_text())
        self.base = self.config['AIOPS_PUBLIC_ORIGIN']
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.call('POST', '/auth/login', {'email': self.config['AIOPS_ADMIN_EMAIL'], 'password': self.config['AIOPS_ADMIN_PASSWORD']})

    def call(self, method, path, body=None):
        req = urllib.request.Request(self.base + '/api/v1' + path, method=method,
              data=None if body is None else json.dumps(body).encode(),
              headers={'Content-Type': 'application/json', 'Origin': self.base})
        try:
            with self.opener.open(req, timeout=180) as response:
                data = response.read()
                return json.loads(data) if data else None
        except urllib.error.HTTPError as exc:
            raise RuntimeError(f'{method} {path}: HTTP {exc.code}: {exc.read(400).decode()}') from None


def seed(_args):
    api = Client()
    me = api.call('GET', '/auth/me')
    user = me.get('user', me)
    teams = api.call('GET', '/teams')['items'] or []
    team = next((x for x in teams if x['name'] == '本地实验'), None)
    if team is None:
        team = api.call('POST', '/teams', {'name': '本地实验', 'description': 'AIOps 隔离实验应用；无生产系统权限'})
    apps = api.call('GET', '/applications')['items']
    bindings = {}
    for role in ROLES + ['vm']:
        key = 'lab-' + role
        is_vm = role == 'vm'
        app = next((a for a in apps if a.get('key') == key), None)
        created = app is None
        if app is None:
            app = api.call('POST', '/applications', {
                'key': key, 'name': 'Lima 实验主机' if is_vm else role.replace('-api', '').title() + ' API · 实验',
                'description': '本机实验模拟生产；真实 Linux guest 指标；独立 aiops-test-vm' if is_vm else '本机实验模拟生产；真实订单/结算/消息链路，业务输入为合成数据。依赖 MySQL、Redis、Kafka；非生产。',
                'environment': 'lab', 'owner_team_id': team['id'], 'maintainer_id': user['id'],
                'tags': ['local-lab', 'virtual-machine' if is_vm else 'synthetic-business-data'],
                'profile': {'business_unit': 'AIOps 实验', 'service_type': 'virtual-machine' if is_vm else 'API',
                            'lifecycle': '实验', 'criticality': '非生产', 'language': '' if is_vm else 'Go',
                            'framework': '' if is_vm else 'net/http', 'default_branch': 'codex/wan-85-local-lab'}})
        envs = api.call('GET', '/applications/' + app['id'] + '/environments')['items']
        env = next(e for e in envs if e['key'] == 'lab')
        resource_bindings = [{'kind': 'prometheus', 'connector_id': 'prometheus', 'resource': key}]
        if not is_vm:
            resource_bindings += [{'kind': 'loki', 'connector_id': 'loki', 'resource': key},
                                  {'kind': 'kubernetes', 'connector_id': key, 'resource': 'deployment'}]
        if not created:
            # A rerun must preserve choices the user made on the product pages.
            def normalized(items):
                return sorted((x['kind'], x['connector_id'], x['resource']) for x in items or [])
            if (app.get('archived') or env.get('archived')
                    or 'local-lab' not in (app.get('tags') or [])
                    or normalized(env.get('bindings')) != normalized(resource_bindings)):
                raise RuntimeError(f'{key} already exists with changed state/bindings; review it in the UI instead of overwriting it')
            bindings[role] = {'application_id': app['id'], 'environment_id': env['id'], 'name': app['name']}
            continue
        expected = {'key': env['key'], 'name': '生产环境', 'archived': False,
                    'binding_revision': env['binding_revision'], 'configuration_revision': env['configuration_revision'],
                    'bindings': resource_bindings, 'configuration': env.get('configuration') or {}}
        if not is_vm:
            expected['configuration'].update({'replicas': 1, 'cpu_request_millicores': 50, 'cpu_limit_millicores': 400,
                                             'memory_request_mib': 64, 'memory_limit_mib': 192, 'port': 8080})
        env = api.call('PATCH', '/environments/' + env['id'], expected)
        bindings[role] = {'application_id': app['id'], 'environment_id': env['id'], 'name': app['name']}
    save('bindings.json', bindings)
    token_file = LOCAL / 'alert-token'
    if not token_file.exists():
        token_file.write_text(secrets.token_urlsafe(36))
        token_file.chmod(0o600)
    ops = json.loads((LOCAL / 'operations.json').read_text())
    ops['sources'] = [s for s in ops.get('sources', []) if s['id'] != 'aiops-test-alertmanager']
    ops['sources'].append({'id': 'aiops-test-alertmanager', 'token_file': str(token_file), 'mappings': [
        {'labels': {'application': role if role != 'vm' else 'aiops-test-vm', 'environment': 'lab'},
         'application_id': val['application_id'], 'environment_id': val['environment_id']} for role, val in bindings.items()]})
    ops['public_url'] = api.base
    save('operations.json', ops)
    print('Registered three applications and the Lima resource, environment bindings and a local alert source. Restart API to load alert mappings.')


def alerts(args):
    """Enable only the lab Alertmanager -> local product webhook, never ChatOps."""
    lab = args.lab.resolve()
    token = (LOCAL / 'alert-token').read_text().strip()
    # The directory is private (0700). Only this copy is readable by the
    # non-root Alertmanager container through its individual read-only mount.
    mount_token = LOCAL / 'alertmanager-token'
    mount_token.write_text(token + '\n')
    mount_token.chmod(0o644)
    config = LOCAL / 'alertmanager.yml'
    config.write_text('''global:
  resolve_timeout: 1m
route:
  receiver: aiops-local
  group_by: [alertname, application, environment]
  group_wait: 5s
  group_interval: 15s
  repeat_interval: 4h
receivers:
  - name: aiops-local
    webhook_configs:
      - url: http://host.docker.internal:18081/api/v1/webhooks/alertmanager/aiops-test-alertmanager
        send_resolved: true
        http_config:
          authorization:
            type: Bearer
            credentials_file: /etc/alertmanager/aiops-token
''')
    save('alertmanager-compose.json', {'services': {'alertmanager': {'volumes': [
        str(config) + ':/etc/alertmanager/alertmanager.yml:ro',
        str(mount_token) + ':/etc/alertmanager/aiops-token:ro']}}})
    subprocess.run(['docker', 'compose', '--project-directory', str(lab), '--env-file', str(lab / '.local/credentials.env'),
                    '-f', str(lab / 'compose.yaml'), '-f', str(LOCAL / 'alertmanager-compose.json'),
                    'up', '-d', '--no-deps', 'alertmanager'], check=True)
    print('Lab Alertmanager forwards only to the local AIOps webhook. No external notification channel.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'seed', 'alerts'])
    parser.add_argument('--runtime-config', type=Path, default=ROOT / '.local/workspace-review-config.json')
    parser.add_argument('--lab', type=Path, default=ROOT.parent / 'aiops-test')
    args = parser.parse_args()
    {'prepare': prepare, 'seed': seed, 'alerts': alerts}[args.command](args)
