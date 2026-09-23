#!/usr/bin/env python3
"""Install read-only metric sidecars for the three dedicated local middleware services.

Does not recreate middleware, generate traffic, or restart Prometheus. Secrets and
generated Compose stay private. Redis ACL is runtime-only; rerun after a Redis
restart. --apply also changes aiops-test's Prometheus configuration and requires
filesystem authorization for that repository when running in the Codex sandbox.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import secrets
import subprocess

ROOT = Path(__file__).resolve().parent.parent
LAB = ROOT.parent / 'aiops-test'
STATE = ROOT / '.local/observability-exporters'
BEGIN = '  # BEGIN AIOPS MIDDLEWARE EXPORTERS\n'
END = '  # END AIOPS MIDDLEWARE EXPORTERS\n'


def run(args, data=None, env=None):
    result = subprocess.run(args, input=data, capture_output=True, text=True, env=env, timeout=120)
    if result.returncode:
        raise RuntimeError('Command failed: ' + args[0] + ' (provider output withheld)')
    return result.stdout


def private(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write(data)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--apply', action='store_true')
    args = p.parse_args()
    if not args.apply:
        print('Creates only aiops-test middleware metric sidecars, monitoring accounts, and three scrape jobs. Use --apply.')
        return
    for service in ('mysql', 'redis', 'kafka', 'prometheus'):
        item = json.loads(run(['docker', 'inspect', 'aiops-test-' + service + '-1']))[0]
        labels = item['Config'].get('Labels', {})
        if labels.get('com.docker.compose.project') != 'aiops-test' or labels.get('com.docker.compose.service') != service or not item['State']['Running']:
            raise RuntimeError('Dedicated running middleware ownership is required')
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    credentials = dict(line.split('=', 1) for line in (LAB / '.local/credentials.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
    secret_path = STATE / 'monitor-credentials.json'
    monitor = json.loads(secret_path.read_text()) if secret_path.exists() else {x: secrets.token_hex(24) for x in ('mysql', 'redis')}
    private(secret_path, json.dumps(monitor))
    sql = ("CREATE USER IF NOT EXISTS 'aiops_metrics'@'%' IDENTIFIED BY '" + monitor['mysql'] + "' WITH MAX_USER_CONNECTIONS 3;\n"
           "GRANT PROCESS, REPLICATION CLIENT ON *.* TO 'aiops_metrics'@'%';\n")
    run(['docker', 'exec', '-i', '-e', 'MYSQL_PWD', 'aiops-test-mysql-1', 'mysql', '-uroot'], sql,
        dict(os.environ, MYSQL_PWD=credentials['MYSQL_ROOT_PASSWORD']))
    # No data commands, key access patterns, writes, or broad ACL categories.
    acl = ['ACL', 'SETUSER', 'aiops_metrics', 'reset', 'on', '>' + monitor['redis'],
           '+ping', '+info', '+select', '+config|get', '+client|list', '+slowlog|len',
           '+slowlog|get', '+latency|latest', '+memory|stats']
    # RESP over stdin avoids placing the credential in process arguments.
    resp = '*' + str(len(acl)) + '\r\n' + ''.join('$' + str(len(x)) + '\r\n' + x + '\r\n' for x in acl)
    out = run(['docker', 'exec', '-i', '-e', 'REDISCLI_AUTH', 'aiops-test-redis-1', 'redis-cli', '--pipe'], resp,
              dict(os.environ, REDISCLI_AUTH=credentials['REDIS_PASSWORD']))
    if 'errors: 0' not in out:
        raise RuntimeError('Redis monitoring ACL was not accepted')
    private(STATE / 'mysql.cnf', '[client]\nuser=aiops_metrics\npassword=' + monitor['mysql'] + '\nhost=mysql\nport=3306\n')
    common = {'restart': 'unless-stopped', 'networks': ['lab'], 'mem_limit': '128m', 'cpus': 0.25,
              'read_only': True, 'cap_drop': ['ALL'], 'security_opt': ['no-new-privileges:true']}
    services = {
        'mysql-exporter': {**common, 'image': 'prom/mysqld-exporter:v0.17.2', 'user': str(os.getuid()),
            'volumes': [str(STATE / 'mysql.cnf') + ':/etc/mysql-exporter.cnf:ro'],
            'command': ['--config.my-cnf=/etc/mysql-exporter.cnf', '--mysqld.address=mysql:3306', '--no-collect.info_schema.tables']},
        'redis-exporter': {**common, 'image': 'oliver006/redis_exporter:v1.67.0',
            'environment': {'REDIS_ADDR': 'redis://redis:6379', 'REDIS_USER': 'aiops_metrics', 'REDIS_PASSWORD': monitor['redis']}},
        'kafka-exporter': {**common, 'image': 'danielqsj/kafka-exporter:v1.9.0',
            'command': ['--kafka.server=kafka:9092', '--kafka.version=3.7.0', '--topic.filter=^orders\\.vm\\.completed$', '--group.filter=^aiops-vm-message-api$', '--refresh.metadata=30s']},
    }
    compose = {'name': 'aiops-test-observability', 'services': services,
               'networks': {'lab': {'external': True, 'name': 'aiops-test'}}}
    private(STATE / 'compose.json', json.dumps(compose, indent=2))
    run(['docker', 'compose', '-f', str(STATE / 'compose.json'), 'up', '-d', '--no-deps'])
    path = LAB / 'observability/prometheus/prometheus.yml'
    original = path.read_text()
    if original.count(BEGIN) != original.count(END) or original.count(BEGIN) > 1:
        raise RuntimeError('Malformed existing exporter block')
    existing = re.sub(re.escape(BEGIN) + '.*?' + re.escape(END), '', original, flags=re.S)
    block = BEGIN
    for service, port in [('mysql', 9104), ('redis', 9121), ('kafka', 9308)]:
        block += (f'  - job_name: aiops-middleware-{service}\n    scrape_interval: 15s\n    scrape_timeout: 10s\n'
                  f'    static_configs:\n      - targets: [\'{service}-exporter:{port}\']\n'
                  f'        labels:\n          cluster: local-docker\n          environment: lab\n          resource: aiops-test-{service}-1\n')
    candidate = existing.rstrip() + '\n' + block + END
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    private(STATE / ('prometheus-before-' + stamp + '.yml'), original)
    candidate_path = path.with_name('observability-candidate.yml')
    try:
        candidate_path.write_text(candidate)
        run(['docker', 'exec', 'aiops-test-prometheus-1', 'promtool', 'check', 'config', '/etc/prometheus/observability-candidate.yml'])
        path.write_text(candidate)
        run(['docker', 'kill', '--signal=HUP', 'aiops-test-prometheus-1'])
    finally:
        candidate_path.unlink(missing_ok=True)
    print('Installed three dedicated metric sidecars and monitoring accounts; Prometheus configuration validated and hot-reloaded.')


if __name__ == '__main__':
    main()
