#!/usr/bin/env python3
"""Check service mapping writes after the registry switch (PA-19 stage 2 step 6, `mapping_writes`).

  python3 tools/acceptance/mapping_writes.py <instance>

Uses the message-api trace binding, the one with the smallest blast radius: three writes that must be
rejected without changing the revision (stale revision, resource outside the catalog, a logs connector
on the traces row), then removing the binding and adding it back. After each accepted write it runs
`aiops connectors compare` with the instance environment and reads the service's trace page. The
binding ends where it started; audits are left for a manual look (`GET /audit?q=v3.service_mapping`).
"""
import copy
import json
import os
import subprocess
import sys
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENV = {'prod': 'prod', 'nonprod': 'test'}
SERVICE = 'message-api'
ROOT = Path(__file__).resolve().parents[2]


def instance_env(instance):
    # Parsed literally: JSON values in the env files do not survive a shell `source`.
    env = {}
    for name in ('server.env', 'extra.env'):
        for line in (LOCAL / instance / name).read_text().splitlines():
            if line and not line.startswith('#') and '=' in line:
                key, value = line.split('=', 1)
                env[key] = value
    return env


def compare(instance):
    out = LOCAL / instance / 'registry-compare.json'
    run = subprocess.run([str(LOCAL / 'bin/aiops'), 'connectors', 'compare', '-out', str(out)], cwd=ROOT,
                         env={**os.environ, **instance_env(instance)}, capture_output=True, text=True)
    if run.returncode not in (0, 3):  # 3: differences found, registry_only included
        sys.exit(f'compare failed: {run.stderr.strip()}')
    data = json.loads(out.read_text())
    return data['counts'], data.get('diverged_services')


def main():
    instance = sys.argv[1]
    env = ENV[instance]

    def snapshot():
        _, body = call(instance, 'GET', '/service-mappings', env=env)
        return next(r for r in body['rows'] if r['service'] == SERVICE)['save']

    def put(item):
        status, body = call(instance, 'PUT', '/service-mappings', {'environment_id': env, 'items': [item]}, env=env)
        return status, ((body or {}).get('error') or {}).get('code') if status != 200 else 'ok'

    def traces_page():
        q = urllib.parse.urlencode({'scope': 'app', 'service': SERVICE, 'q': '{ status = error }'})
        status, body = call(instance, 'GET', f'/traces?{q}', env=env)
        return status, ((body or {}).get('error') or {}).get('code') if status != 200 else 'ok'

    start = snapshot()
    rev = start['binding_revision']
    traces = next(b for b in start['bindings'] if b['system_id'] == 'traces')
    logs_connector = next(b['connector_id'] for b in start['bindings'] if b['system_id'] == 'logs')
    print('start revision', rev, 'compare', compare(instance))

    stale = {**copy.deepcopy(start), 'binding_revision': rev - 1}
    outside = copy.deepcopy(start)
    wrong = copy.deepcopy(start)
    for b in outside['bindings']:
        if b['system_id'] == 'traces':
            b['resource'] = 'not-in-catalog'
    for b in wrong['bindings']:
        if b['system_id'] == 'traces':
            b['connector_id'] = logs_connector
    print('stale revision (409 stale_mapping):', put(stale))
    print('outside catalog (422 resource_not_in_catalog):', put(outside))
    print('logs connector on traces (422 connector_unavailable):', put(wrong))
    print('revision after rejects', snapshot()['binding_revision'], '(expected', rev, ')')

    removed = copy.deepcopy(snapshot())
    removed['bindings'] = [b for b in removed['bindings'] if b['system_id'] != 'traces']
    print('remove traces:', put(removed), 'revision', snapshot()['binding_revision'],
          'compare', compare(instance), 'trace page', traces_page())

    added = copy.deepcopy(snapshot())
    added['bindings'].append({k: traces[k] for k in ('system_id', 'kind', 'connector_id', 'resource')})
    print('add traces back:', put(added), 'revision', snapshot()['binding_revision'],
          'compare', compare(instance), 'trace page', traces_page())


if __name__ == '__main__':
    main()
