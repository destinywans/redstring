#!/usr/bin/env python3
"""Explicit local review integration. No business requests or workload changes.

prepare provisions read-only discovery credentials and private registrations;
activate updates this review database after the matching API build is ready.
"""
import argparse
import base64
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent.parent
PRIVATE = ROOT / '.local/product-lab'
LIVE = ROOT / '.local/live-integration'
LAB = ROOT.parent / 'aiops-test'
ROLES = ('order-api', 'checkout-api', 'message-api')
PERMISSION = 'live-infrastructure-view'


def run(args, data=None, env=None, timeout=60):
    result = subprocess.run(args, input=data, text=True, capture_output=True, env=env, timeout=timeout)
    if result.returncode:
        # Provider output may contain credentials or private configuration.
        raise RuntimeError(f'{args[0]} failed; no success is claimed')
    return result.stdout.strip()


def save(path, value):
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + '.tmp')
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as handle:
        handle.write(json.dumps(value, ensure_ascii=False, indent=2) + '\n')
    os.replace(temporary, path)


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def sql(config, statement):
    db = urlsplit(config['DATABASE_URL'])
    if (config.get('AIOPS_ADDR') != '127.0.0.1:18081' or db.hostname not in ('localhost', '127.0.0.1')
            or not db.path.startswith('/aiops_') or '_review_' not in db.path):
        raise RuntimeError('Only the explicitly configured isolated review database is permitted')
    container = 'aiops-canvas-db-1'
    port = run(['docker', 'port', container, '5432/tcp'])
    if not any(line.rsplit(':', 1)[-1] == str(db.port or 5432) for line in port.splitlines()):
        raise RuntimeError('Review database container does not match configured port')
    env = dict(os.environ, PGPASSWORD=unquote(db.password or ''))
    return run(['docker', 'exec', '-i', '-e', 'PGPASSWORD', container, 'psql', '-X', '-qAt',
                '-v', 'ON_ERROR_STOP=1', '-U', unquote(db.username or 'postgres'), '-d', db.path[1:]], statement, env)


def applications(config):
    rows = json.loads(sql(config, "SELECT COALESCE(json_agg(x),'[]') FROM (SELECT id,key,name FROM applications WHERE key IN ('lab-order-api','lab-checkout-api','lab-message-api')) x;"))
    mapping = {row['key']: row['id'] for row in rows}
    if set(mapping) != {'lab-' + role for role in ROLES}:
        raise RuntimeError('Expected existing registered lab applications; refusing to invent replacements')
    return mapping


def reader(kubeconfig, context, source_id):
    command = ['kubectl', '--kubeconfig', str(kubeconfig), '--context', context]
    raw = json.loads(run(command + ['config', 'view', '--raw', '--minify', '-o', 'json']))
    cluster = raw['clusters'][0]['cluster']
    endpoint = urlsplit(cluster['server'])
    if endpoint.scheme != 'https' or endpoint.hostname not in ('localhost', '127.0.0.1'):
        raise RuntimeError('Discovery registration only supports explicitly owned loopback lab APIs')
    namespace, name = 'aiops-system', 'aiops-platform-reader'
    objects = [
        {'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace}},
        {'apiVersion': 'v1', 'kind': 'ServiceAccount', 'metadata': {'name': name, 'namespace': namespace}},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRole', 'metadata': {'name': name}, 'rules': [
            {'apiGroups': [''], 'resources': ['nodes', 'namespaces', 'pods', 'events', 'services', 'persistentvolumes', 'persistentvolumeclaims'], 'verbs': ['get', 'list', 'watch']},
            {'apiGroups': ['apps'], 'resources': ['deployments', 'replicasets', 'statefulsets', 'daemonsets'], 'verbs': ['get', 'list', 'watch']},
            {'apiGroups': ['batch'], 'resources': ['jobs', 'cronjobs'], 'verbs': ['get', 'list', 'watch']},
            {'apiGroups': ['networking.k8s.io'], 'resources': ['ingresses'], 'verbs': ['get', 'list', 'watch']},
            {'apiGroups': ['storage.k8s.io'], 'resources': ['storageclasses'], 'verbs': ['get', 'list', 'watch']},
        ]},
        {'apiVersion': 'rbac.authorization.k8s.io/v1', 'kind': 'ClusterRoleBinding', 'metadata': {'name': name},
         'subjects': [{'kind': 'ServiceAccount', 'name': name, 'namespace': namespace}],
         'roleRef': {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'ClusterRole', 'name': name}},
    ]
    run(command + ['apply', '-f', '-'], json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': objects}))
    directory = LIVE / source_id
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    ca = directory / 'ca.crt'
    ca.write_bytes(base64.b64decode(cluster['certificate-authority-data']))
    ca.chmod(0o600)
    token = run(command + ['-n', namespace, 'create', 'token', name, '--duration=168h'])
    token_file = directory / 'token'
    fd = os.open(token_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as handle:
        handle.write(token + '\n')
    claims = json.loads(base64.urlsafe_b64decode(token.split('.')[1] + '==='))
    save(directory / 'credential-metadata.json', {'expires_at': claims['exp'], 'source_id': source_id, 'mode': 'read-only'})
    return {'api_url': cluster['server'], 'ca_file': str(ca), 'token_file': str(token_file)}


def metric_queries(role=None):
    node = 'cluster="aiops-vm-cluster",job="aiops-vm-nodes"'
    queries = {
        '节点在线状态': 'up{' + node + '}',
        '节点CPU使用率': '100 * (1 - avg by (node) (rate(node_cpu_seconds_total{' + node + ',mode="idle"}[2m])))',
        '节点内存使用率': '100 * (1 - node_memory_MemAvailable_bytes{' + node + '} / node_memory_MemTotal_bytes{' + node + '})',
        '节点磁盘使用率': '100 * (1 - node_filesystem_avail_bytes{' + node + ',mountpoint="/",fstype!="rootfs"} / node_filesystem_size_bytes{' + node + ',mountpoint="/",fstype!="rootfs"})',
        '节点网络接收速率': 'sum by (node) (rate(node_network_receive_bytes_total{' + node + ',device!="lo"}[2m]))',
    }
    if role:
        scope = 'cluster="aiops-vm-cluster",job="aiops-vm-lab",application="' + role + '"'
        queries = {
            '实例在线状态': 'up{' + scope + '}',
            '请求速率': 'sum(rate(lab_http_requests_total{' + scope + '}[2m]))',
            '请求P95耗时': 'histogram_quantile(0.95,sum by(le)(rate(lab_http_request_duration_seconds_bucket{' + scope + '}[2m])))',
            '依赖操作速率': 'sum by(dependency,result)(rate(lab_dependency_requests_total{' + scope + '}[2m]))',
        }
    return queries


def prepare(config):
    mapping = applications(config)
    old_runtime = dict(config)
    if not (LIVE / 'runtime-before.json').exists():
        save(LIVE / 'runtime-before.json', old_runtime)
        for name in ('kubernetes', 'telemetry'):
            path = Path(config['AIOPS_' + ('KUBERNETES_REGISTRY' if name == 'kubernetes' else 'TELEMETRY_CONFIG')])
            save(LIVE / (name + '-before.json'), json.loads(path.read_text()))
    access = reader(LAB / '.local/vm-cluster/kubeconfig', 'aiops-vm-cluster', 'aiops-vm-cluster')
    previous = json.loads((LIVE / 'kubernetes-before.json').read_text())
    entries = [item for item in previous if item['id'] not in {'lab-' + role for role in ROLES}]
    for role in ROLES:
        entries.append({'id': 'lab-' + role, 'config': {**access, 'namespace': 'aiops-lab', 'deployment': role}})
    save(LIVE / 'kubernetes.json', entries)
    telemetry = json.loads((LIVE / 'telemetry-before.json').read_text())
    credentials = dict(line.split('=', 1) for line in (LAB / '.local/enterprise/credentials.env').read_text().splitlines() if line and not line.startswith('#'))
    password_file = LIVE / 'elasticsearch-password'
    fd = os.open(password_file, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as handle:
        handle.write(credentials['ELASTIC_PASSWORD'])
    telemetry.update(elasticsearch={'url': 'http://127.0.0.1:19200', 'allow_http': True, 'username': 'elastic', 'password_file': str(password_file)},
                     tempo={'url': 'http://127.0.0.1:13200', 'allow_http': True}, max_window_seconds=86400, max_lookback_seconds=86400)
    telemetry['applications'].pop('lab-vm', None)
    telemetry['applications']['infra-local'] = {'prometheus_queries': metric_queries()}
    for role in ROLES:
        telemetry['applications']['lab-' + role] = {
            'prometheus_queries': metric_queries(role),
            'loki_labels': {'application': role, 'environment': 'lab', 'namespace': 'aiops-lab'},
            'elasticsearch_index': 'aiops-lab-logs',
            'elasticsearch_filters': {'orchestrator.cluster.name': 'aiops-vm-cluster', 'kubernetes.namespace': 'aiops-lab'},
            'elasticsearch_prefixes': {'kubernetes.pod.name': role + '-'},
            'tempo_service': role,
            'tempo_attributes': {'k8s.cluster.name': 'aiops-vm-cluster', 'k8s.namespace.name': 'aiops-lab', 'deployment.environment.name': 'lab'},
        }
    save(LIVE / 'telemetry.json', telemetry)
    source = {'id': 'aiops-vm-cluster', 'name': '本地 Kubernetes 集群', **access, 'environment_key': 'lab',
              'application_bindings': [{'application_id': 'infra-local', 'environment_id': 'infra-local-lab'},
                  *[{'namespace': 'aiops-lab', 'label_selector': {'app': role}, 'application_id': mapping['lab-' + role],
                     'environment_id': mapping['lab-' + role] + '-default'} for role in ROLES]]}
    sources = [source]
    for source_id, name, kubeconfig, context in [
        ('aiops-canvas-demo', '恢复演示集群', ROOT / '.local/aiops-canvas-demo/kubeconfig', 'kind-aiops-canvas-demo'),
        ('aiops-delivery', '交付实验集群', LAB / '.local/delivery/kubeconfig', 'kind-aiops-delivery'),
    ]:
        sources.append({'id': source_id, 'name': name, **reader(kubeconfig, context, source_id), 'environment_key': 'lab',
                        'application_bindings': [{'application_id': 'infra-local', 'environment_id': 'infra-local-lab'}]})
    inventory = {'sources': sources, 'external_snapshot_file': str(LAB / '.local/inventory/snapshot.json'),
                 'prometheus_url': 'http://127.0.0.1:19090',
                 'grant_permission_ids': [PERMISSION]}
    save(LIVE / 'inventory.json', inventory)
    config.update(AIOPS_KUBERNETES_REGISTRY=str(LIVE / 'kubernetes.json'), AIOPS_TELEMETRY_CONFIG=str(LIVE / 'telemetry.json'),
                  AIOPS_INVENTORY_CONFIG=str(LIVE / 'inventory.json'))
    save(LIVE / 'runtime-next.json', config)
    print('Prepared read-only cluster registration, bounded telemetry queries and private runtime configuration.')


def activate(config):
    mapping = applications(config)
    backup = sql(config, "SELECT json_build_object('environments',(SELECT json_agg(e) FROM application_environments e),'applications',(SELECT json_agg(a) FROM applications a));")
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    save(LIVE / ('catalog-before-' + stamp + '.json'), json.loads(backup))
    statements = ["BEGIN;",
        "DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM rbac_roles WHERE id='admin') THEN RAISE EXCEPTION 'Configured administrator role missing'; END IF; END $$;",
        "INSERT INTO applications(id,key,name,description,environment,tags) VALUES('infra-local','infra-local','本地基础设施','本机真实集群、主机和基础服务。','lab','[\"local\"]') ON CONFLICT(id) DO NOTHING;",
        "INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES('infra-local-lab','infra-local','lab','本地实验','[{\"kind\":\"prometheus\",\"connector_id\":\"prometheus\",\"resource\":\"infra-local\"}]') ON CONFLICT(id) DO NOTHING;",
        "INSERT INTO rbac_permissions(id,name,description) VALUES('live-infrastructure-view','本地基础设施查看','查看已接入的本地真实资源；不授予变更、审批或删除。') ON CONFLICT(id) DO NOTHING;",
        "INSERT INTO rbac_role_permissions(role_id,permission_id) SELECT 'admin','live-infrastructure-view' WHERE EXISTS(SELECT 1 FROM rbac_roles WHERE id='admin') ON CONFLICT DO NOTHING;",
    ]
    for role in ROLES:
        app = mapping['lab-' + role]
        bindings = [{'kind': kind, 'connector_id': kind, 'resource': 'lab-' + role} for kind in ('prometheus', 'loki', 'elasticsearch', 'tempo')]
        bindings.append({'kind': 'kubernetes', 'connector_id': 'lab-' + role, 'resource': 'deployment'})
        statements.append('UPDATE application_environments SET key=\'lab\', name=\'本地实验\', bindings=' + literal(json.dumps(bindings)) + "::jsonb, binding_revision=binding_revision+1, configuration=jsonb_set(configuration,'{replicas}','2'::jsonb),configuration_revision=configuration_revision+1 WHERE id=" + literal(app + '-default') + ';')
        statements.append('UPDATE applications SET environment=\'lab\' WHERE id=' + literal(app) + ';')
    statements.extend([
        "UPDATE applications SET archived=true WHERE key='lab-vm';",
        "UPDATE application_environments SET archived=true,binding_revision=binding_revision+1 WHERE application_id IN (SELECT id FROM applications WHERE key='lab-vm') AND NOT archived;",
        "INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes) VALUES('','system','local_integration.updated','aiops-vm-cluster','success','{\"read_only\":true,\"permission\":\"live-infrastructure-view\"}');",
        'COMMIT;',
    ])
    sql(config, '\n'.join(statements))
    save(PRIVATE / 'runtime.json', json.loads((LIVE / 'runtime-next.json').read_text()))
    print('Updated local catalog/bindings, archived the replaced VM entry and activated the prepared configuration. Restart the matching review binary separately.')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=['prepare', 'activate'])
    args = parser.parse_args()
    runtime = json.loads((PRIVATE / 'runtime.json').read_text())
    (prepare if args.command == 'prepare' else activate)(runtime)
