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

Path parameters use a real id from the matching list route. A role that holds
the grant reads with an id from its OWN list (drafts and pending objects are
visible to their maintainer only); roles without it and writes use admin's. A
read with no real id is skipped; a write falls back to a dummy id.

Some answers are reported apart from the mismatches (field "excluded"), so they
do not fail regress:
  * runtime_unregistered: a plain-text 404/405 from the mux, i.e. the route is
    not registered on this instance (OIDC bindings without OIDC configured);
  * known_defect:<id>: an answer that matches a registered defect (KNOWN);
  * no_object (list "no_object"): a granted read answered 404 for an object
    admin cannot read either (an evaluation never executed, no review candidate).
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
from permission_bodies import list_items  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
PROBE = 'permissions'
ROLES = ['admin', 'operator', 'approver1', 'viewer']
# Readable only by the account that created the object (the sample ids come from admin's records).
OWNER_ONLY = {'GET /api/v1/agent-runs/{id}/result'}
DENIED = {403, 404}
# Registered defects: a failing answer that matches is reported as known_defect:<defect>,
# not as a new mismatch. Remove an entry when the defect is fixed.
KNOWN = [
    {'route': 'GET /api/v1/config-sync', 'instances': {'prod'}, 'status': {403}, 'defect': 'PA-110',
     'why': 'production lists config-sync only for human platform administrators; the route mark says workspace.view'},
    {'route': 'POST /api/v1/silences', 'instances': {'nonprod'}, 'status': {501}, 'code': 'not_connected', 'defect': 'PA-108',
     'why': 'checks the Alertmanager write binding before the permission'},
]
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


def first_id(body):
    """The id of the first object of a list response (items, agents, policies ...
    the key differs per module); never an id from kpis, trend or other lists."""
    rows = list_items(body)
    return str(rows[0]['id']) if rows else None


def sample_ids(instance, cookie=None):
    """A real id per id prefix from the lists this account can read (admin when
    cookie is None); keys are the full /api/v1 prefix."""
    ids = {}
    for path, prefix in SAMPLES.items():
        status, body = call(instance, 'GET', path, cookie=cookie, role='admin', probe=PROBE)
        found = first_id(body) if status == 200 else None
        if found:
            ids.setdefault('/api/v1' + prefix, found)
    return ids


def exclusion(row, instance, body):
    """Why a row stays out of the mismatches, or None (see the module docstring)."""
    if row['status'] in (404, 405) and isinstance(body, str):
        return 'runtime_unregistered'
    if row['ok']:
        return None
    for k in KNOWN:
        if (k['route'] == row['route'] and instance in k['instances'] and row['status'] in k['status']
                and k.get('code', row['code']) == row['code']):
            return f"known_defect:{k['defect']}"
    return None


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
    ids = {'admin': sample_ids(a.instance)}
    for role in ROLES[1:]:
        ids[role] = sample_ids(a.instance, cookies[role])

    def allowed_for(role, key, module, action):
        return (module, action) in effective[role] and not (key in OWNER_ONLY and role != 'admin')

    results, problems, skipped, no_object = [], [], [], []

    def record(row, body):
        why = exclusion(row, a.instance, body)
        if why:
            row['excluded'] = why
            row['ok'], row['inconclusive'] = False, False
        results.append(row)
        if not row['ok'] and not row['inconclusive'] and not why:
            problems.append(row)

    for key in sorted(table):
        method, path = key.split(' ', 1)
        scoped, module, action = table[key]
        for role in ROLES:
            allowed = allowed_for(role, key, module, action)
            if method != 'GET' and allowed:
                continue  # positive writes are covered by the golden paths
            # A granted read targets an object from the role's own list; the rest use admin's.
            own = allowed and method == 'GET'
            target = resolve(path, ids[role] if own else ids['admin'], dummy=method != 'GET')
            if target is None:
                skipped.append((key, role))
                continue
            target = target.replace('/api/v1', '', 1)
            cookie = cookies[role] if role != 'admin' else None
            status, body = call(a.instance, method, target, None if method == 'GET' else {}, cookie=cookie, role='admin', probe=PROBE)
            code = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
            if own and status == 404 and '{' in path and isinstance(body, dict) and (
                    role == 'admin' or call(a.instance, 'GET', target, probe=PROBE)[0] == 404):
                no_object.append({'route': key, 'role': role, 'target': target, 'code': code})
                continue  # nobody can read it: no such object (or sub-resource) to probe
            if allowed:
                ok = status not in (401, 403) and not (scoped and status == 404 and '{' in path)
            elif scoped:
                ok = status in DENIED or (method == 'GET' and status == 200 and '{' not in path)
            else:
                ok = status == 403
            inconclusive = not ok and not allowed and scoped and method != 'GET' and status in (400, 409, 412, 422, 428, 501, 503)
            record({'route': key, 'role': role, 'module': module, 'action': action, 'object_scoped': scoped,
                    'allowed': allowed, 'status': status, 'code': code, 'ok': ok, 'inconclusive': inconclusive,
                    'probe': 'empty_body' if method != 'GET' else 'read',
                    'registration': info[key]['registration'], 'source': info[key]['source']}, body)
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
            record(row, row.pop('body_text', None))
    pending = [r for r in results if r['inconclusive']]
    excluded = [r for r in results if r.get('excluded')]
    print(f"{a.instance}: {len(table)} routes, {len(results)} probes, {len(problems)} mismatches, "
          f"{len(pending)} inconclusive object-scoped writes, {len(skipped)} skipped (no real id)")
    groups = {}
    for r in excluded:
        groups.setdefault(r['excluded'], set()).add(r['route'])
    for why, keys in sorted(groups.items()):
        print(f"  {why}: " + ', '.join(sorted(keys)))
    if no_object:
        print(f"  no_object (granted read, admin gets 404 too): " + ', '.join(sorted({x['route'] for x in no_object})))
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
            'excluded': {why: sorted(keys) for why, keys in sorted(groups.items())}, 'no_object': no_object,
            'known_defects': [{k: (sorted(v) if isinstance(v, set) else v) for k, v in d.items()} for d in KNOWN],
            'outside_matrix': [{'route': r['pattern'], 'reason': 'own account or composite'} for r in scanned['routes'] if not r['module']]
            + [{'route': r['pattern'], 'reason': r['exempt']} for r in scanned['exempt']],
        }, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
