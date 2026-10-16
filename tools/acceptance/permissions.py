#!/usr/bin/env python3
"""Probe the V3 role matrix route by route on one acceptance instance.

  python3 tools/acceptance/permissions.py prod [--json out.json] [--routes routes.json]

Routes and their (module, action, object scope) come from routescan, which reads
every MarkV3Route / MarkV3ObjectRoute call in internal/ and cmd/ with go/ast,
including the route tables registered in loops (`go run ./tools/acceptance/routescan`;
--routes reads a saved scan instead). Routes without a module (own account,
composite discovery) are not in the matrix. Each role account's effective grants
come from GET /members/{id}/access (all roles, legacy origin included).

  * GET routes: every role. With the grant the answer must not be 403; without
    it, a route-level check must answer 403 (object-scoped: 403 or 404).
  * Write routes: only roles WITHOUT the grant, with an empty JSON body, so a
    working check has no side effect. Route-level checks must answer 403.
    Object-scoped handlers validate the body before resolving the target, so
    their 4xx/5xx answers are listed as "inconclusive" for a valid-body recheck.

  * --valid-bodies: object-scoped writes again with a valid body per route from
    permission_bodies.py, as the same roles without the grant (target visible to
    them), and as the environment-scoped account on an object of another
    environment when it exists (--setup-scoped ENV creates it; nonprod only).
    Each entry's guard keeps a missed check from changing real objects; see that
    module. A 2xx answer is a mismatch (verdict ACCEPTED) with what to clean up.

Every request carries X-Acceptance-Probe: permissions, so coverage counts can
leave this traffic out.

Path parameters use a real id from the matching list route when one exists,
otherwise a dummy id (writes) or the route is skipped (reads).
"""
import argparse
import json
import subprocess
import sys
import urllib.error
import urllib.request
import ssl
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
PROBE = 'permissions'
ROLES = ['admin', 'operator', 'approver1', 'viewer']
# Readable only by the account that created the object (the sample ids come from admin's records).
OWNER_ONLY = {'GET /api/v1/agent-runs/{id}/result'}
DENIED = {403, 404}
# List routes read for sample ids, and the path prefix their ids fill in.
SAMPLES = {
    '/services': '/services', '/alert-groups': '/alert-groups', '/incidents': '/incidents', '/alert-rules': '/alert-rules',
    '/runbooks': '/runbooks', '/connectors': '/connectors', '/agents/lifecycle': '/agents', '/prompts': '/prompts',
    '/audit': '/audit', '/members': '/members', '/roles': '/roles', '/silences': '/silences', '/canvases': '/canvases',
    '/evals': '/evals', '/postmortems': '/postmortems', '/mcp-servers': '/mcp-servers', '/api-tokens': '/api-tokens',
    '/agent-runs': '/agent-runs', '/eval-datasets': '/eval-datasets', '/trace-views': '/trace-views',
    '/maintenance-windows': '/maintenance-windows', '/knowledge-sources': '/knowledge-sources',
    '/knowledge-sources/registrations': '/knowledge-sources/registrations', '/investigation-templates': '/investigation-templates',
    '/canvas-templates': '/canvas-templates', '/guardrails': '/guardrails', '/webhooks': '/webhooks', '/ingest/pull': '/ingest/pull',
    '/notification-routes': '/notification-routes', '/memory/facts': '/memory/facts', '/config-sync': '/config-sync',
}


def scan(path=None):
    """routescan output (see tools/acceptance/routescan). A fatal scan result
    (an unresolved or conflicting mark) stops the run: the matrix would be wrong."""
    if path:
        return json.loads(Path(path).read_text())
    proc = subprocess.run(['go', 'run', './tools/acceptance/routescan'], cwd=ROOT, capture_output=True, text=True)
    if proc.returncode != 0:
        raise SystemExit(f'routescan failed ({proc.returncode}):\n{proc.stderr}')
    return json.loads(proc.stdout)


def routes(scanned=None):
    """{'METHOD /api/v1/path': (object_scoped, module, action)} for every V3 route in the matrix."""
    scanned = scanned or scan()
    return {r['pattern']: (r['object_scoped'], r['module'], r['action']) for r in scanned['routes'] if r['module']}


def login(instance, account, probe=None):
    """Session cookie for a role account (no MFA on these accounts). A cached
    cookie that still answers GET /me is reused, so repeated runs stay under
    the login rate limit. probe tags the requests (X-Acceptance-Probe)."""
    cache_path = LOCAL / 'e2e' / f'cookies-{instance}.json'
    try:
        cache = json.loads(cache_path.read_text())
    except (OSError, ValueError):
        cache = {}
    cached = cache.get(account['email'])
    if cached and call(instance, 'GET', '/me', cookie=cached, probe=probe)[0] == 200:
        return cached
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    body = json.dumps({'email': account['email'], 'password': account['password']}).encode()
    headers = {'Content-Type': 'application/json', 'Origin': ORIGINS[instance]}
    if probe:
        headers['X-Acceptance-Probe'] = probe
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1/auth/login', body, method='POST', headers=headers)
    with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
        cookies = [h.split(';', 1)[0] for k, h in r.headers.items() if k.lower() == 'set-cookie']
    cache[account['email']] = '; '.join(cookies)
    cache_path.write_text(json.dumps(cache))
    cache_path.chmod(0o600)
    return cache[account['email']]


def grants(instance, email):
    _, members = call(instance, 'GET', '/members', probe=PROBE)
    mid = next(m['id'] for m in members['items'] if m.get('email') == email)
    _, access = call(instance, 'GET', f'/members/{mid}/access', probe=PROBE)
    return {(g['module'], g['action']) for r in access['roles'] for g in r.get('grants', [])}


def first_id(body, depth=0):
    """The id of the first object in the first non-empty list of a list response
    (items, rules, agents, policies ... the key differs per module)."""
    if depth > 3:
        return None
    if isinstance(body, list):
        for item in body:
            if isinstance(item, dict) and isinstance(item.get('id'), (str, int)) and str(item['id']):
                return str(item['id'])
        for item in body:
            found = first_id(item, depth + 1)
            if found:
                return found
    elif isinstance(body, dict):
        for key in ('items', 'rules', 'agents', 'members', 'roles', 'silences', 'runs', 'tokens', 'servers', 'policies'):
            found = first_id(body.get(key), depth + 1)
            if found:
                return found
        for value in body.values():
            if isinstance(value, (list, dict)):
                found = first_id(value, depth + 1)
                if found:
                    return found
    return None


def sample_ids(instance):
    """A real id per id prefix, read as admin; keys are the full /api/v1 prefix."""
    ids = {}
    for path, prefix in SAMPLES.items():
        status, body = call(instance, 'GET', path, probe=PROBE)
        found = first_id(body) if status == 200 else None
        if found:
            ids.setdefault('/api/v1' + prefix, found)
    return ids


def resolve(path, ids, dummy):
    """Replace path parameters; returns None when a read has no real id."""
    parts, out, prefix = path.split('/'), [], ''
    for part in parts:
        if part.startswith('{'):
            real = ids.get(prefix)
            if real is None and not dummy:
                return None
            out.append(real or 'acceptance-probe')
        else:
            out.append(part)
        prefix = '/'.join(out)
    return '/'.join(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('instance', choices=ORIGINS)
    ap.add_argument('--json')
    ap.add_argument('--routes', help='saved routescan JSON instead of running go run ./tools/acceptance/routescan')
    ap.add_argument('--valid-bodies', action='store_true', help='also probe object-scoped writes with valid bodies')
    ap.add_argument('--setup-scoped', metavar='ENV', help='first create the probe account scoped to ENV (an instance write)')
    a = ap.parse_args()
    scanned = scan(a.routes)
    info = {r['pattern']: r for r in scanned['routes']}
    secrets_ = json.loads((LOCAL / a.instance / 'secrets.json').read_text())
    accounts = secrets_['accounts']
    cookies = {'admin': None}
    effective = {'admin': grants(a.instance, secrets_['admin_email'])}
    for role in ROLES[1:]:
        cookies[role] = login(a.instance, accounts[role], probe=PROBE)
        effective[role] = grants(a.instance, accounts[role]['email'])
    table = routes(scanned)
    ids = sample_ids(a.instance)

    def allowed_for(role, key, module, action):
        return (module, action) in effective[role] and not (key in OWNER_ONLY and role != 'admin')

    results, problems, skipped = [], [], []
    for key in sorted(table):
        method, path = key.split(' ', 1)
        scoped, module, action = table[key]
        for role in ROLES:
            allowed = allowed_for(role, key, module, action)
            if method != 'GET' and allowed:
                continue  # positive writes are covered by the golden paths
            target = resolve(path, ids, dummy=method != 'GET')
            if target is None:
                skipped.append((key, role))
                continue
            status, body = call(a.instance, method, target.replace('/api/v1', '', 1), None if method == 'GET' else {},
                                cookie=cookies[role] if role != 'admin' else None, role='admin', probe=PROBE)
            code = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
            if allowed:
                ok = status not in (401, 403) and not (scoped and status == 404 and '{' in path)
            elif scoped:
                ok = status in DENIED or (method == 'GET' and status == 200 and '{' not in path)
            else:
                ok = status == 403
            inconclusive = not ok and not allowed and scoped and method != 'GET' and status in (400, 409, 412, 422, 428, 501, 503)
            row = {'route': key, 'role': role, 'module': module, 'action': action, 'object_scoped': scoped,
                   'allowed': allowed, 'status': status, 'code': code, 'ok': ok, 'inconclusive': inconclusive,
                   'probe': 'empty_body' if method != 'GET' else 'read',
                   'registration': info[key]['registration'], 'source': info[key]['source']}
            results.append(row)
            if not ok and not inconclusive:
                problems.append(row)
    valid_skipped = []
    if a.valid_bodies or a.setup_scoped:
        import permission_bodies
        if a.setup_scoped:
            permission_bodies.ensure_scoped_account(a.instance, a.setup_scoped)
            accounts = json.loads((LOCAL / a.instance / 'secrets.json').read_text())['accounts']
        scoped = None
        if 'scoped' in accounts:
            scoped = {**accounts['scoped'], 'cookie': login(a.instance, accounts['scoped'], probe=PROBE)}
        valid, valid_skipped = permission_bodies.run(a.instance, table, effective, cookies, ROLES, scoped)
        for row in valid:
            row.update(registration=info[row['route']]['registration'], source=info[row['route']]['source'])
            results.append(row)
            if not row['ok'] and not row['inconclusive']:
                problems.append(row)
    pending = [r for r in results if r['inconclusive']]
    print(f"{a.instance}: {len(table)} routes, {len(results)} probes, {len(problems)} mismatches, "
          f"{len(pending)} inconclusive object-scoped writes, {len(skipped)} skipped (no real id)")
    if a.valid_bodies or a.setup_scoped:
        verdicts = {}
        for r in results:
            if r['probe'].startswith('valid_body'):
                verdicts[r['verdict']] = verdicts.get(r['verdict'], 0) + 1
        print(f"  valid bodies: {sum(verdicts.values())} probes " + ', '.join(f'{k} {v}' for k, v in sorted(verdicts.items()))
              + f"; {len(valid_skipped)} not probed (see valid_body_skipped)")
    for p in problems:
        print(f"  {p['route']:<60} {p['role']:<10} {'granted' if p['allowed'] else 'denied ':<8} {p['module']}.{p['action']:<7} -> {p['status']} {p['code'] or ''}"
              + (f" {p['verdict']} ({p['probe']})" if p.get('verdict') else ''))
    if a.json:
        matrix = []
        for key in sorted(table):
            r = info[key]
            granted = [role for role in ROLES if allowed_for(role, key, r['module'], r['action'])]
            matrix.append({'route': key, 'method': r['method'], 'path': r['path'], 'module': r['module'], 'action': r['action'],
                           'object_scoped': r['object_scoped'], 'write': r['write'], 'registration': r['registration'],
                           'source': r['source'], 'allowed_roles': granted, 'denied_roles': [x for x in ROLES if x not in granted]})
        Path(a.json).write_text(json.dumps({
            'results': results, 'skipped': skipped, 'grants': {r: sorted(map(list, g)) for r, g in effective.items()},
            'routes': matrix, 'scan': scanned['summary'],
            'valid_body_skipped': [{'route': k, 'role': r, 'reason': why} for k, r, why in valid_skipped],
            'outside_matrix': [{'route': r['pattern'], 'reason': 'own account or composite'} for r in scanned['routes'] if not r['module']]
            + [{'route': r['pattern'], 'reason': r['exempt']} for r in scanned['exempt']],
        }, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
