#!/usr/bin/env python3
"""Environment and instance isolation checks for the two acceptance instances.

  python3 tools/acceptance/environments.py

Covers the plan's environment layer: the production instance only serves
`prod`; the non-production `all` view is read-only (writes answer 409
env_view_only before any validation); unknown or foreign environments are
refused; sessions and data do not cross instances; a write with another
instance's Origin is refused. Every check uses the admin account, so a
failure is an isolation problem, not a missing grant.
"""
import json
import secrets
import ssl
import sys
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call, session_cookie  # noqa: E402

WRITES = [('POST', '/conversations', {'text': 'acceptance env probe'}), ('POST', '/silences', {}),
          ('POST', '/alert-rules', {}), ('POST', '/incidents', {}), ('POST', '/runbooks', {}),
          ('PUT', '/service-mappings', {}), ('POST', '/canvases', {})]


def raw(instance, method, path, cookie, origin, body=None, env=None):
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Origin': origin, 'Cookie': cookie, 'Accept-Language': 'zh-CN'}
    if env:
        headers['X-AIOps-Environment'] = env
    data = None
    if body is not None:
        data, headers['Content-Type'] = json.dumps(body).encode(), 'application/json'
    if method != 'GET':
        headers['Idempotency-Key'] = secrets.token_hex(16)
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1' + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
            return r.status, None
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read()).get('error', {}).get('code')
        except Exception:
            return e.code, None


def code(body):
    return body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None


def main():
    rows = []

    def check(name, status, expected, detail=''):
        ok = status in expected if isinstance(expected, (set, tuple)) else status == expected
        rows.append((ok, name, status, detail))

    # Non-production: the all view reads, never writes.
    s, b = call('nonprod', 'GET', '/services', env='all')
    check('nonprod all: GET /services', s, 200, f"{len((b or {}).get('items') or [])} services")
    for method, path, body in WRITES:
        s, b = call('nonprod', method, path, body, env='all')
        check(f'nonprod all: {method} {path}', s, 409, code(b) or '')
    per_env = {}
    for env in ('dev', 'test', 'staging'):
        s, b = call('nonprod', 'GET', '/services', env=env)
        per_env[env] = sorted(i['name'] for i in (b or {}).get('items') or [])
        check(f'nonprod {env}: GET /services', s, 200, ','.join(per_env[env]) or 'none')
    s, b = call('nonprod', 'GET', '/services', env='prod')
    check('nonprod: foreign env prod refused', s, {400, 403, 404, 409, 422}, code(b) or '')
    s, b = call('nonprod', 'GET', '/services', env='nope')
    check('nonprod: unknown env refused', s, {400, 403, 404, 409, 422}, code(b) or '')

    # Production: only prod.
    s, b = call('prod', 'GET', '/services', env='prod')
    check('prod prod: GET /services', s, 200, f"{len((b or {}).get('items') or [])} services")
    for env in ('all', 'test', 'dev'):
        s, b = call('prod', 'GET', '/services', env=env)
        check(f'prod: env {env} refused', s, {400, 403, 404, 409, 422}, code(b) or '')
    s, b = call('prod', 'POST', '/conversations', {'text': 'acceptance env probe'}, env='all')
    check('prod: write with env all refused', s, {400, 403, 404, 409, 422}, code(b) or '')

    # Sessions and origins do not cross instances.
    for this, other in (('prod', 'nonprod'), ('nonprod', 'prod')):
        s, c = raw(other, 'GET', '/me', session_cookie(this, 'admin'), ORIGINS[other])
        check(f'{this} session on {other}: GET /me', s, 401, c or '')
        s, c = raw(this, 'POST', '/conversations', session_cookie(this, 'admin'), ORIGINS[other],
                   {'text': 'cross-origin probe'}, env='prod' if this == 'prod' else 'test')
        check(f'{this}: write with {other} Origin', s, 403, c or '')
        s, c = raw(this, 'POST', '/conversations', session_cookie(this, 'admin'), 'https://evil.example',
                   {'text': 'cross-origin probe'}, env='prod' if this == 'prod' else 'test')
        check(f'{this}: write with foreign Origin', s, 403, c or '')

    # Data: alert groups and conversations are per instance.
    _, pg = call('prod', 'GET', '/alert-groups', env='prod')
    _, ng = call('nonprod', 'GET', '/alert-groups', env='all')
    shared = {g['id'] for g in (pg or {}).get('items') or []} & {g['id'] for g in (ng or {}).get('items') or []}
    check('alert groups not shared across instances', len(shared), 0, f'{len(shared)} shared ids')

    failed = [r for r in rows if not r[0]]
    for ok, name, status, detail in rows:
        print(f"{'PASS' if ok else 'FAIL'}  {name:<48} {status}  {detail}")
    print(f"{len(rows) - len(failed)}/{len(rows)} passed")
    (LOCAL / 'e2e' / 'environments.json').write_text(json.dumps(
        [{'ok': ok, 'check': n, 'status': s, 'detail': d} for ok, n, s, d in rows], ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
