#!/usr/bin/env python3
"""Extend existing local telemetry registrations without changing apps or RBAC.

Only explicitly registered local infrastructure is included. Provider credentials,
non-metric registrations, and existing custom queries are preserved. Run again
after infrastructure discovery changes to refresh exact Docker identities.
"""
import argparse
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location('live_integration', ROOT / 'scripts/connect-live-platform.py')
live = importlib.util.module_from_spec(spec)
spec.loader.exec_module(live)


def selector(labels):
    return '{' + ','.join(k + '=' + json.dumps(v) for k, v in labels.items()) + '}'


def machine_queries(scope, prefix):
    s = selector(scope)
    cpu = selector({**scope, 'mode': 'idle'})
    disk = selector({**scope, 'mountpoint': '/'})
    network = s[:-1] + ',device!="lo"}'
    return {
        prefix + 'CPU使用率': '100 * (1 - avg by(node) (rate(node_cpu_seconds_total' + cpu + '[2m])))',
        prefix + '内存使用率': '100 * (1 - node_memory_MemAvailable_bytes' + s + ' / node_memory_MemTotal_bytes' + s + ')',
        prefix + '磁盘使用率': '100 * (1 - node_filesystem_avail_bytes' + disk + ' / node_filesystem_size_bytes' + disk + ')',
        prefix + '网络接收速率': 'sum by(node)(rate(node_network_receive_bytes_total' + network + '[2m]))',
        prefix + '网络发送速率': 'sum by(node)(rate(node_network_transmit_bytes_total' + network + '[2m]))',
    }


def bridge_query(metric, scope, name=None):
    s = selector(scope)
    # The bridge collection timestamp can be slightly ahead of the scrape start.
    expression = ('(100 * aiops_node_' + metric + '_usage_ratio' + s
                  + ' and on(cluster,node) (aiops_node_metrics_up' + s + ' == 1)'
                  + ' and on(cluster,node) (time()-aiops_node_metrics_timestamp_seconds' + s + ' >= -5)'
                  + ' and on(cluster,node) (time()-aiops_node_metrics_timestamp_seconds' + s + ' <= 90))')
    if name:
        expression = 'label_replace(' + expression + ',"resource",' + json.dumps(name.replace('$', '$$')) + ',"node",".*")'
    return expression


def extend(config, inventory, snapshot):
    apps = config['applications']
    for role in live.ROLES:
        key = 'lab-' + role
        if key not in apps:
            raise ValueError('Existing application registration is required: ' + key)
        q = apps[key].setdefault('prometheus_queries', {})
        q.update(live.metric_queries(role))
        scope = selector({'cluster': 'aiops-vm-cluster', 'job': 'aiops-vm-lab', 'application': role})
        requests = 'sum(rate(lab_http_requests_total' + scope + '[2m]))'
        errors = 'sum(rate(lab_http_requests_total' + scope[:-1] + ',status=~"5.."}[2m]))'
        # Zero only when a real request series exists; no traffic remains undefined.
        q['请求错误率'] = '100 * (' + errors + ' or (' + requests + ' * 0)) / (' + requests + ' > 0)'
        q['在线实例数'] = 'sum(up' + scope + ')'
    if 'infra-local' not in apps:
        raise ValueError('Existing infra-local registration is required')
    q = apps['infra-local'].setdefault('prometheus_queries', {})
    sources = {s['id']: s for s in inventory['sources'] if any(
        b.get('application_id') == 'infra-local' and b.get('environment_id') == 'infra-local-lab'
        for b in s.get('application_bindings', []))}
    if 'aiops-vm-cluster' not in sources:
        raise ValueError('VM cluster must already belong to infra-local')
    q.update(live.metric_queries())
    q.update(machine_queries({'cluster': 'aiops-vm-cluster', 'job': 'aiops-vm-nodes'}, '节点'))
    kinds = {'aiops-canvas-demo': 'aiops-kind-demo', 'aiops-delivery': 'aiops-kind-delivery'}
    for metric, label in [('cpu', 'CPU'), ('memory', '内存')]:
        parts = [bridge_query(metric, {'cluster': cluster, 'job': job})
                 for cluster, job in kinds.items() if cluster in sources]
        if parts:
            q['Kind节点' + label + '使用率'] = ' or '.join(parts)
    docker = []
    middleware = set()
    for source in snapshot['sources']:
        for resource in source.get('resources', []):
            if not any(a.get('application_id') == 'infra-local' and a.get('environment_id') == 'infra-local-lab'
                       for a in resource.get('applications', [])):
                continue
            attrs = resource.get('attributes', {})
            if attrs.get('status', '').lower() != 'running':
                continue
            if attrs.get('metric_cluster') == 'local-docker' and attrs.get('metric_node'):
                docker.append((attrs['metric_node'], resource['name']))
                if attrs.get('compose_project') == 'aiops-test':
                    middleware.add(attrs.get('compose_service'))
            if attrs.get('metric_cluster') == 'local-lima' and attrs.get('metric_node') == 'aiops-deploy-vm':
                q.update(machine_queries({'cluster': 'local-lima', 'job': 'aiops-deploy-vm', 'node': 'aiops-deploy-vm'}, '部署VM'))
    for metric, label in [('cpu', 'CPU'), ('memory', '内存')]:
        if docker:
            q['Docker服务容器' + label + '使用率'] = ' or '.join(
                bridge_query(metric, {'cluster': 'local-docker', 'job': 'aiops-docker-services', 'node': node}, name)
                for node, name in sorted(set(docker)))
    # These jobs are installed separately by install-observability-exporters.py.
    # Never expose a middleware instance absent from the current infra inventory.
    if (ROOT / '.local/observability-exporters/compose.json').exists():
        if 'mysql' in middleware:
            s = selector({'cluster': 'local-docker', 'job': 'aiops-middleware-mysql', 'resource': 'aiops-test-mysql-1'})
            q.update({'MySQL在线状态': 'mysql_up' + s,
                      'MySQL连接数': 'mysql_global_status_threads_connected' + s,
                      'MySQL查询速率': 'rate(mysql_global_status_queries' + s + '[2m])',
                      'MySQL慢查询速率': 'rate(mysql_global_status_slow_queries' + s + '[2m])'})
        if 'redis' in middleware:
            s = selector({'cluster': 'local-docker', 'job': 'aiops-middleware-redis', 'resource': 'aiops-test-redis-1'})
            hits = 'rate(redis_keyspace_hits_total' + s + '[2m])'
            misses = 'rate(redis_keyspace_misses_total' + s + '[2m])'
            q.update({'Redis在线状态': 'redis_up' + s,
                      'Redis连接数': 'redis_connected_clients' + s,
                      'Redis内存使用率': '100 * redis_memory_used_bytes' + s + ' / (redis_memory_max_bytes' + s + ' > 0)',
                      'Redis命令速率': 'rate(redis_commands_processed_total' + s + '[2m])',
                      'Redis缓存命中率': '100 * ' + hits + ' / ((' + hits + ' + ' + misses + ') > 0)'})
        if 'kafka' in middleware:
            s = selector({'cluster': 'local-docker', 'job': 'aiops-middleware-kafka', 'resource': 'aiops-test-kafka-1'})
            q.update({'Kafka Broker数量': 'kafka_brokers' + s,
                      'Kafka分区数量': 'kafka_topic_partitions' + s[:-1] + ',topic="orders.vm.completed"}',
                      'Kafka消费积压': 'kafka_consumergroup_lag_sum' + s[:-1] + ',topic="orders.vm.completed",consumergroup="aiops-vm-message-api"}'})
    return {key: len(value.get('prometheus_queries', {})) for key, value in apps.items()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true', help='Back up and replace only the current telemetry JSON')
    args = parser.parse_args()
    runtime = json.loads((ROOT / '.local/product-lab/runtime.json').read_text())
    if runtime.get('AIOPS_PUBLIC_ORIGIN') != 'http://127.0.0.1:18081':
        raise SystemExit('Only the dedicated local review platform is supported')
    path = Path(runtime['AIOPS_TELEMETRY_CONFIG']).resolve()
    if path.parent != (ROOT / '.local/live-integration').resolve():
        raise SystemExit('Unexpected private telemetry location')
    config = json.loads(path.read_text())
    inventory = json.loads(Path(runtime['AIOPS_INVENTORY_CONFIG']).read_text())
    snapshot = json.loads(Path(inventory['external_snapshot_file']).read_text())
    counts = extend(config, inventory, snapshot)
    if args.apply:
        stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
        live.save(path.with_name('telemetry-before-observability-' + stamp + '.json'), json.loads(path.read_text()))
        live.save(path, config)
    print(json.dumps({'applied': args.apply, 'registered_queries': counts, 'restart_required': args.apply}, ensure_ascii=False))


if __name__ == '__main__':
    main()
