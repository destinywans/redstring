#!/usr/bin/env python3
"""Probe the V3 role matrix route by route on one acceptance instance.

  python3 tools/acceptance/permissions.py prod [--json out.json]

Routes and their (module, action) come from the server's own registry calls
(httpapi MarkV3Route / MarkV3ObjectRoute) in internal/ and cmd/, plus the few
registered through variables (DYNAMIC below). Each role account's effective
grants come from GET /members/{id}/access (all roles, legacy origin included).

  * GET routes: every role. With the grant the answer must not be 403; without
    it, a route-level check must answer 403 (object-scoped: 403 or 404).
  * Write routes: only roles WITHOUT the grant, with an empty JSON body, so a
    working check has no side effect. Route-level checks must answer 403.
    Object-scoped handlers validate the body before resolving the target, so
    their 4xx/5xx answers are listed as "inconclusive" for a valid-body recheck.

Path parameters use a real id from the matching list route when one exists,
otherwise a dummy id (writes) or the route is skipped (reads).
"""
import argparse
import json
import re
import sys
import urllib.error
import urllib.request
import ssl
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
MARK = re.compile(r'MarkV3(Object)?Route\(\s*"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]+)",\s*"(\w*)",\s*"(\w+)"\)')
TABLE = re.compile(r'\{\s*"(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^"]+)",\s*"(\w+)",\s*"(\w+)"')
# Routes marked through a variable; (object_scoped, module, action).
DYNAMIC = {
    'POST /api/v1/knowledge/search': (False, 'knowledge', 'view'),
    'POST /api/v1/uploads': (False, 'conversation', 'change'),
    'POST /api/v1/uploads/{id}/revoke': (False, 'conversation', 'change'),
    'GET /api/v1/services/{id}/resilience': (True, 'services', 'view'),
    'POST /api/v1/services/{id}/resilience/proposals': (True, 'proposals', 'change'),
    'POST /api/v1/proposals/{id}/approve/options': (True, 'proposals', 'approve'),
    'POST /api/v1/proposals/{id}/approve': (True, 'proposals', 'approve'),
    'GET /api/v1/audit': (False, 'audit', 'view'),
    'GET /api/v1/audit/{id}': (False, 'audit', 'view'),
    'POST /api/v1/audit/export': (False, 'audit', 'view'),
    'GET /api/v1/knowledge-sources': (True, 'knowledge', 'view'),
    'POST /api/v1/knowledge-sources': (True, 'knowledge', 'manage'),
    'GET /api/v1/connector-types/{type}': (False, 'connectors', 'view'),
    'GET /api/v1/connectors': (False, 'connectors', 'view'),
    'GET /api/v1/connectors/{id}': (False, 'connectors', 'view'),
    'POST /api/v1/connectors': (False, 'connectors', 'manage'),
    'POST /api/v1/connectors/{id}/activate': (False, 'connectors', 'manage'),
    'PATCH /api/v1/connectors/{id}': (False, 'connectors', 'manage'),
    'POST /api/v1/connectors/{id}/test': (False, 'connectors', 'manage'),
    'PUT /api/v1/connectors/{id}/settings': (False, 'connectors', 'manage'),
    'POST /api/v1/connectors/{id}/disable': (False, 'connectors', 'manage'),
    'POST /api/v1/webhooks': (False, 'connectors', 'manage'),
    'GET /api/v1/conversations': (True, 'conversation', 'view'),
    'POST /api/v1/conversations': (False, 'conversation', 'change'),
    'POST /api/v1/conversations/{id}/messages': (False, 'conversation', 'change'),
    'POST /api/v1/alert-rules': (True, 'alert_rules', 'change'),
    'PUT /api/v1/alert-rules/{id}': (True, 'alert_rules', 'change'),
    'POST /api/v1/alert-rules/import': (True, 'alert_rules', 'change'),
    'PUT /api/v1/investigation-templates/{id}': (True, 'canvas', 'change'),
}
ROLES = ['admin', 'operator', 'approver1', 'viewer']
DENIED = {403, 404}


def routes():
    out = {}
    for f in list((ROOT / 'internal').rglob('*.go')) + list((ROOT / 'cmd').rglob('*.go')):
        if f.name.endswith('_test.go'):
            continue
        text = f.read_text()
        for m in MARK.finditer(text):
            if m[4]:
                out[f'{m[2]} {m[3]}'] = (bool(m[1]), m[4], m[5])
        for m in TABLE.finditer(text):
            if 'v3admin' in str(f):
                out.setdefault(f'{m[1]} {m[2]}', (False, m[3], m[4]))
    for key, value in DYNAMIC.items():
        out.setdefault(key, value)
    return out


def login(instance, account):
    """Session cookie for a role account (no MFA on these accounts). A cached
    cookie that still answers GET /me is reused, so repeated runs stay under
    the login rate limit."""
    cache_path = LOCAL / 'e2e' / f'cookies-{instance}.json'
    try:
        cache = json.loads(cache_path.read_text())
    except (OSError, ValueError):
        cache = {}
    cached = cache.get(account['email'])
    if cached and call(instance, 'GET', '/me', cookie=cached)[0] == 200:
        return cached
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    body = json.dumps({'email': account['email'], 'password': account['password']}).encode()
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1/auth/login', body, method='POST',
                                 headers={'Content-Type': 'application/json', 'Origin': ORIGINS[instance]})
    with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
        cookies = [h.split(';', 1)[0] for k, h in r.headers.items() if k.lower() == 'set-cookie']
    cache[account['email']] = '; '.join(cookies)
    cache_path.write_text(json.dumps(cache))
    cache_path.chmod(0o600)
    return cache[account['email']]


def grants(instance, email):
    _, members = call(instance, 'GET', '/members')
    mid = next(m['id'] for m in members['items'] if m.get('email') == email)
    _, access = call(instance, 'GET', f'/members/{mid}/access')
    return {(g['module'], g['action']) for r in access['roles'] for g in r.get('grants', [])}


def sample_ids(instance):
    """A real id per list route, read as admin; keys are the list path."""
    ids = {}
    for path in ['/services', '/alert-groups', '/incidents', '/alert-rules', '/runbooks', '/connectors', '/agents/lifecycle',
                 '/prompts', '/audit', '/members', '/roles', '/silences', '/canvases', '/evals', '/postmortems', '/mcp-servers',
                 '/api-tokens', '/agent-runs', '/eval-datasets', '/trace-views', '/maintenance-windows', '/knowledge-sources',
                 '/investigation-templates', '/canvas-templates', '/guardrails', '/webhooks', '/ingest/pull', '/notification-routes']:
        status, body = call(instance, 'GET', path)
        if status != 200 or not isinstance(body, dict):
            continue
        for key in ('items', 'rules', 'agents', 'members', 'roles', 'silences', 'runs', 'tokens', 'servers'):
            items = body.get(key)
            if isinstance(items, list) and items and isinstance(items[0], dict) and items[0].get('id'):
                ids[path] = str(items[0]['id'])
                break
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
    a = ap.parse_args()
    secrets_ = json.loads((LOCAL / a.instance / 'secrets.json').read_text())
    accounts = secrets_['accounts']
    cookies = {'admin': None}
    effective = {'admin': grants(a.instance, secrets_['admin_email'])}
    for role in ROLES[1:]:
        cookies[role] = login(a.instance, accounts[role])
        effective[role] = grants(a.instance, accounts[role]['email'])
    table = routes()
    ids = {k.replace('/api/v1', ''): v for k, v in sample_ids(a.instance).items()}
    ids = {('/api/v1' + k): v for k, v in ids.items()}
    results, problems, skipped = [], [], []
    for key in sorted(table):
        method, path = key.split(' ', 1)
        scoped, module, action = table[key]
        for role in ROLES:
            allowed = (module, action) in effective[role]
            if method != 'GET' and allowed:
                continue  # positive writes are covered by the golden paths
            target = resolve(path, ids, dummy=method != 'GET')
            if target is None:
                skipped.append((key, role))
                continue
            status, body = call(a.instance, method, target.replace('/api/v1', '', 1), None if method == 'GET' else {},
                                cookie=cookies[role] if role != 'admin' else None, role='admin')
            code = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
            if allowed:
                ok = status not in (401, 403) and not (scoped and status == 404 and '{' in path)
            elif scoped:
                ok = status in DENIED or (method == 'GET' and status == 200 and '{' not in path)
            else:
                ok = status == 403
            inconclusive = not ok and not allowed and scoped and method != 'GET' and status in (400, 409, 422, 501, 503)
            row = {'route': key, 'role': role, 'module': module, 'action': action, 'object_scoped': scoped,
                   'allowed': allowed, 'status': status, 'code': code, 'ok': ok, 'inconclusive': inconclusive}
            results.append(row)
            if not ok and not inconclusive:
                problems.append(row)
    pending = [r for r in results if r['inconclusive']]
    print(f"{a.instance}: {len(table)} routes, {len(results)} probes, {len(problems)} mismatches, "
          f"{len(pending)} inconclusive object-scoped writes, {len(skipped)} skipped (no real id)")
    for p in problems:
        print(f"  {p['route']:<60} {p['role']:<10} {'granted' if p['allowed'] else 'denied ':<8} {p['module']}.{p['action']:<7} -> {p['status']} {p['code'] or ''}")
    if a.json:
        Path(a.json).write_text(json.dumps({'results': results, 'skipped': skipped, 'grants': {r: sorted(map(list, g)) for r, g in effective.items()}}, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
