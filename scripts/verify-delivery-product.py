#!/usr/bin/env python3
"""Verify approved release flow through the isolated review product API.

Opt-in: --execute. Uses only Delivery Demo and its three dedicated lab targets.
Credentials stay in the existing private runtime file. Evidence is resumable;
HTTP request retries never create a second Run with a different idempotency key.
"""
import argparse
import copy
import http.cookiejar
import json
import os
from pathlib import Path
import time
import urllib.error
import urllib.request
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parent.parent
LOCAL = ROOT / '.local/product-lab'
APP = '2e85edf1e3e8148d43832ea46ba424fb'
TARGETS = {'vm-compose': 'vm-compose', 'k8s-gitops': 'kubernetes-gitops', 'vm-systemd': 'vm-systemd'}
REVISION = '1a1d4d1796dcd76cce315f234fc8c79f90fb18c9'
EVIDENCE = LOCAL / 'delivery-product-verification.json'


class APIError(Exception):
    def __init__(self, status, message):
        self.status = status
        super().__init__(message)


class Client:
    def __init__(self):
        config = json.loads((LOCAL / 'runtime.json').read_text())
        db = urlsplit(config['DATABASE_URL'])
        if config['AIOPS_PUBLIC_ORIGIN'] != 'http://127.0.0.1:18081' or db.hostname not in ('localhost', '127.0.0.1') or '_review_' not in db.path:
            raise RuntimeError('Only the isolated loopback review workspace is allowed')
        self.base = config['AIOPS_PUBLIC_ORIGIN']
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.call('POST', '/auth/login', {'email': config['AIOPS_ADMIN_EMAIL'], 'password': config['AIOPS_ADMIN_PASSWORD']})

    def call(self, method, path, body=None, key=None):
        headers = {'Content-Type': 'application/json', 'Origin': self.base}
        if key:
            headers['Idempotency-Key'] = key
        request = urllib.request.Request(self.base + '/api/v1' + path, method=method, headers=headers,
                                         data=None if body is None else json.dumps(body).encode())
        try:
            with self.opener.open(request, timeout=90) as response:
                raw = response.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            raise APIError(error.code, f'{method} {path}: HTTP {error.code} {error.read(300).decode()}') from None


def save(state):
    temp = EVIDENCE.with_suffix('.tmp')
    temp.write_text(json.dumps(state, indent=2, ensure_ascii=False) + '\n')
    temp.chmod(0o600)
    temp.replace(EVIDENCE)


def definition(template, target, connections):
    value = copy.deepcopy(template['definition'])
    inputs = {'repository': connections['gitops_repo_url'], 'revision': REVISION,
              'image': connections['registry_cluster'].rstrip('/') + '/aiops-demo',
              'buildkit': connections['buildkit_cluster_addr'], 'insecure-registry': 'true',
              'go-package': '.', 'goarch': 'arm64'}
    for node in value['nodes']:
        if node['kind'] == 'approval':
            node['target_id'] = target
        for item in node.get('inputs', []):
            if not item.get('from_node') and item['name'] in inputs:
                item['value'] = inputs[item['name']]
    if not any(node['kind'] == 'health' for node in value['nodes']):
        raise RuntimeError('The current product binary lacks the approved health-node template')
    return value


def run_target(api, target, template, env, connections, state):
    record = state['targets'].setdefault(target, {'idempotency_key': 'wan91-http-' + target + '-' + os.urandom(12).hex()})
    save(state)
    if not record.get('pipeline_id'):
        pipeline = api.call('POST', '/delivery/pipelines', {'application_id': APP, 'environment_id': env,
                  'name': 'Delivery Demo · ' + target + ' · HTTP 验收', 'definition': definition(template, target, connections)})
        record['pipeline_id'], record['version'] = pipeline['id'], pipeline['version']
        save(state)
    if not record.get('run_id'):
        run = api.call('POST', '/delivery/runs', {'pipeline_id': record['pipeline_id'], 'version': record['version']}, record['idempotency_key'])
        record['run_id'] = run['id']
        save(state)
    deadline = time.monotonic() + 1200
    previous = None
    while time.monotonic() < deadline:
        run = api.call('GET', '/delivery/runs/' + record['run_id'])
        if run['application_id'] != APP or run['environment_id'] != env:
            raise RuntimeError('Unexpected application or environment')
        record['run'] = run
        gates = api.call('GET', '/delivery/runs/' + run['id'] + '/gates')['items'] or []
        record['gates'] = gates
        for gate in gates:
            if gate['node_id'] != 'approval':
                continue
            plan = api.call('GET', '/deployments/plans/' + gate['plan_id'])
            if (plan['target_id'] != target or plan['source']['run_id'] != run['id']
                    or plan['source']['application_id'] != APP or plan['source']['environment_id'] != env
                    or plan['source']['commit_sha'] != REVISION):
                raise RuntimeError('Approval is not bound to the expected lab source and target')
            record['plan'] = plan
            if plan['status'] == 'pending':
                try:
                    api.call('POST', '/deployments/plans/' + plan['id'] + '/execute', {})
                except APIError as error:
                    if error.status != 409:
                        raise
                    record['unapproved_execute_status'] = error.status
                else:
                    raise RuntimeError('Unapproved execution was accepted')
                # The user explicitly authorized these dedicated experimental releases.
                # Exercise the same human-approval API used by the UI, not direct SQL.
                record['plan'] = api.call('POST', '/deployments/plans/' + plan['id'] + '/approve', {'digest': plan['digest']})
        ids = {gate['execution_id'] for gate in gates if gate.get('execution_id')}
        executions = [api.call('GET', '/deployments/executions/' + identity) for identity in ids]
        record['executions'] = executions
        snapshot = (run['status'], tuple((node['name'], node['status']) for node in (run['projection'].get('nodes') or [])), tuple((x['id'], x['status']) for x in executions))
        if snapshot != previous:
            print(json.dumps({'target': target, 'run_id': run['id'], 'status': run['status'], 'nodes': dict(snapshot[1]), 'executions': list(snapshot[2])}, ensure_ascii=False), flush=True)
            previous = snapshot
        save(state)
        if run['status'] == 'succeeded':
            if not executions or any(x['status'] != 'succeeded' or x['result'].get('health_samples', 0) < 30 for x in executions):
                raise RuntimeError('Pipeline success did not include continuous release verification')
            observed = {node['name']: node['status'] for node in (run['projection'].get('nodes') or [])}
            required = {node['id'] for node in run['snapshot']['definition']['nodes']}
            if not required.issubset(observed) or any(observed[node] != 'succeeded' for node in required):
                raise RuntimeError('Not every required node succeeded')
            record['verified_at'] = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
            record['events'] = api.call('GET', '/delivery/runs/' + run['id'] + '/events')['items']
            save(state)
            return
        if run['status'] in ('failed', 'error', 'cancelled', 'needs_attention'):
            raise RuntimeError('Recorded product run needs inspection; no blind retry: ' + run['id'])
        time.sleep(5)
    raise RuntimeError('Timed out; state retained for read-only inspection and resume')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--execute', action='store_true')
    parser.add_argument('--target', choices=list(TARGETS), action='append')
    parser.add_argument('--lab', type=Path, default=ROOT.parent / 'aiops-test')
    args = parser.parse_args()
    api = Client()
    binding = json.loads((LOCAL / 'delivery-binding.json').read_text())
    if binding['application_id'] != APP:
        raise RuntimeError('Unexpected Delivery Demo application')
    config = json.loads((LOCAL / 'deployment-product-config.json').read_text())
    for target in config['targets']:
        if target['id'] not in TARGETS or target['application_id'] != APP or target['environment_id'] != binding['environment_id'] or target['health']['window_seconds'] != 180:
            raise RuntimeError('The product targets do not match the approved experiment')
    catalog = api.call('GET', '/delivery/catalog')
    if not all(catalog.get(k) for k in ('native_enabled', 'artifact_store_configured', 'native_deployment_enabled')):
        raise RuntimeError('Product native delivery configuration is not active')
    templates = {entry['id']: entry for entry in catalog['templates']}
    connections = json.loads((args.lab / '.local/delivery/connections.json').read_text())
    requested = args.target or list(TARGETS)
    for target in requested:
        definition(templates[TARGETS[target]], target, connections)
    if not args.execute:
        print('Preflight passed; --execute is required to create pipelines, runs and approve lab releases.')
        return
    state = json.loads(EVIDENCE.read_text()) if EVIDENCE.exists() else {'application_id': APP, 'environment_id': binding['environment_id'], 'targets': {}}
    if state['application_id'] != APP or state['environment_id'] != binding['environment_id']:
        raise RuntimeError('Evidence scope mismatch')
    # The three registered targets share one product environment. Preserve its
    # release serialization instead of making concurrent conflicting approvals.
    for target in requested:
        run_target(api, target, templates[TARGETS[target]], binding['environment_id'], connections, state)


if __name__ == '__main__':
    main()
