#!/usr/bin/env python3
"""Connect the delivery lab GitLab to an acceptance instance for D11 (changes and releases).

  python3 tools/acceptance/gitlab_setup.py <instance> register|bind|target|activate|confirm|catalog|verify|all

Steps, in order (design docs/plans/2026-10-11-d11-delivery-chain.md):
  register  POST /connectors (type gitlab, https://127.0.0.1:18446, Vault read token); the id is kept
            in .local/acceptance/gitlab-connectors.json. Reuses an existing registration.
  bind      adds the connector to AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS in extra.env and restarts.
  target    registers GitLab project 4 as checkout-api's project target (before activation).
  activate  POST /connectors/{id}/activate, then PATCH the six read tools on.
  confirm   POST /connectors/{id}/test, POST /service-mappings/discover, accept checkout-api's code
            project candidate (D20).
  catalog   adds the "gitlab" scope (deploy environment aiops-lab, branch main) to the source catalog
            and restarts.
  verify    reads GET /changes?sources=gitlab for 7 days and prints the deployments, pipelines and MRs.
The Vault entry aiops/lab/gitlab/read must hold `token` and `ca` (the proxy's 127.0.0.1 certificate).
Only .local/acceptance is changed; nothing is written to GitLab.
"""
import json
import subprocess
import sys
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
ENV = {'prod': 'prod', 'nonprod': 'test'}
TIER = {'prod': 'production', 'nonprod': 'nonprod'}
URL = 'https://127.0.0.1:18446'
CRED = 'vault://aiops/lab/gitlab/read#token'
PROJECT = '4'
SERVICE = 'checkout-api'
TOOLS = ['list_pipelines', 'get_pipeline', 'get_commit', 'get_commit_diff', 'list_merge_requests', 'list_deployments']
IDS = LOCAL / 'gitlab-connectors.json'


def ids():
    return json.loads(IDS.read_text()) if IDS.exists() else {}


def connector(instance):
    cid = ids().get(instance)
    if not cid:
        sys.exit(f'{instance}: no GitLab connector yet, run register first')
    return cid


def service(instance):
    return json.loads((LOCAL / 'rollback-services.json').read_text())[instance][SERVICE]


def application(instance):
    catalog = json.loads((LOCAL / instance / 'source-catalog.json').read_text())
    return next(w['application_id'] for w in catalog['workloads'] if w['resource_id'] == SERVICE)


def checked(label, status, body, ok=(200, 201, 202)):
    print(f"{'ok ' if status in ok else 'FAIL'} {status}  {label}  {json.dumps(body, ensure_ascii=False)[:200]}")
    if status not in ok:
        sys.exit(1)
    return body


def restart(instance):
    r = subprocess.run(['python3', 'tools/acceptance/acceptance.py', 'restart', instance], cwd=ROOT, capture_output=True, text=True)
    print((r.stdout.strip().splitlines() or [''])[-1])


def register(instance):
    env = ENV[instance]
    _, existing = call(instance, 'GET', '/connectors', env=env)
    for item in existing.get('items') or []:
        if item.get('id') == ids().get(instance):
            print(f'reuse {item["id"]}')
            return
    body = {'type': 'gitlab', 'url': URL, 'auth': 'pat', 'credential_ref': CRED, 'tools': TOOLS}
    created = checked('register GitLab connector', *call(instance, 'POST', '/connectors', body, env=env))
    saved = ids()
    saved[instance] = created['id']
    IDS.write_text(json.dumps(saved, indent=1) + '\n')


def bind(instance):
    path = LOCAL / instance / 'extra.env'
    lines = path.read_text().splitlines()
    key = 'AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS='
    i = next(n for n, line in enumerate(lines) if line.startswith(key))
    bindings = json.loads(lines[i][len(key):])
    cid = connector(instance)
    if not any(b.get('connector_id') == cid for b in bindings):
        bindings.append({'tier': TIER[instance], 'environment_id': ENV[instance], 'connector_id': cid,
                         'provider': 'gitlab', 'endpoint': URL, 'credential_ref': CRED})
        lines[i] = key + json.dumps(bindings, separators=(',', ':'))
        path.write_text('\n'.join(lines) + '\n')
        path.chmod(0o600)
        print(f'binding added for {cid}')
    restart(instance)


def target(instance):
    body = {'connector_id': connector(instance), 'target_kind': 'project', 'external_id': PROJECT}
    status, resp = call(instance, 'POST', f'/services/{service(instance)}/external-targets', body, env=ENV[instance])
    if status == 409:
        print(f'ok  409  project target already registered')
        return
    checked('register project target', status, resp)


def activate(instance):
    cid, env = connector(instance), ENV[instance]
    status, resp = call(instance, 'POST', f'/connectors/{cid}/activate', {}, env=env)
    if status != 409:
        checked('activate', status, resp)
    checked('enable read tools', *call(instance, 'PATCH', f'/connectors/{cid}',
                                       {'tools': [{'name': t, 'enabled': True} for t in TOOLS]}, env=env))


def confirm(instance):
    cid, env = connector(instance), ENV[instance]
    checked('test connection', *call(instance, 'POST', f'/connectors/{cid}/test', {}, env=env))
    found = checked('discover', *call(instance, 'POST', '/service-mappings/discover', {}, env=env))
    sid = service(instance)
    picks = [c for c in found.get('candidates') or [] if c.get('service_id') == sid]
    if not picks:
        sys.exit(f'no candidate for {SERVICE}; source_state={found.get("source_state")}')
    keys = ('id', 'version', 'service_id', 'environment_id', 'system_id', 'binding_revision')
    checked('accept code project', *call(instance, 'POST', '/service-mappings/accept',
                                         {'candidates': [{k: c[k] for k in keys if k in c} for c in picks]}, env=env))


def catalog(instance):
    path = LOCAL / instance / 'source-catalog.json'
    data = json.loads(path.read_text())
    scope = {'connector_id': connector(instance), 'application_id': application(instance), 'environment_id': ENV[instance],
             'project_id': PROJECT, 'deploy_environment': 'aiops-lab', 'refs': ['main']}
    data['gitlab'] = [s for s in data.get('gitlab', []) if s.get('connector_id') != scope['connector_id']] + [scope]
    path.write_text(json.dumps(data, ensure_ascii=False, indent=1))
    path.chmod(0o600)
    print('catalog gitlab scope set')
    restart(instance)


def verify(instance):
    q = urllib.parse.urlencode({'service_id': service(instance), 'range': '7d', 'sources': 'gitlab'})
    status, body = call(instance, 'GET', f'/changes?{q}', env=ENV[instance])
    print(status, json.dumps({k: body.get(k) for k in ('coverage', 'unavailable')}, ensure_ascii=False)[:300])
    for kind in ('deployments', 'pipelines', 'merge_requests'):
        rows = body.get(kind)
        print(f'  {kind}: {None if rows is None else len(rows)}')
        for r in (rows or [])[:8]:
            print('    ', json.dumps({k: r.get(k) for k in ('id', 'iid', 'status', 'state', 'ref', 'sha', 'finished_at', 'merged_at',
                                                            'cluster_revision', 'pipeline_id')}, ensure_ascii=False)[:200])


STEPS = {'register': register, 'bind': bind, 'target': target, 'activate': activate, 'confirm': confirm,
         'catalog': catalog, 'verify': verify}

if __name__ == '__main__':
    inst, step = sys.argv[1], sys.argv[2]
    for name in (STEPS if step == 'all' else [step]):
        print(f'== {inst} {name}')
        STEPS[name](inst)
