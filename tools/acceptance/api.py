#!/usr/bin/env python3
"""Call an acceptance instance as a saved Playwright session (admin by default).

  python3 tools/acceptance/api.py prod GET /home
  python3 tools/acceptance/api.py nonprod POST /conversations '{"title":"x"}' --env test

Resolves *.redstring.test to 127.0.0.1 for this process only and trusts only the
acceptance certificate. Prints status and JSON; never prints cookies.
"""
import argparse
import json
import socket
import ssl
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
ORIGINS = {'prod': 'https://prod.redstring.test:18443', 'nonprod': 'https://nonprod.redstring.test:18444'}
DEFAULT_ENV = {'prod': 'prod', 'nonprod': 'test'}

_orig = socket.getaddrinfo
socket.getaddrinfo = lambda host, *a, **k: _orig('127.0.0.1' if str(host).endswith('.redstring.test') else host, *a, **k)


def session_cookie(instance, role):
    state = json.loads((LOCAL / 'e2e' / f'state-{instance}-{role}.json').read_text())
    return '; '.join(f"{c['name']}={c['value']}" for c in state['cookies'])


def call(instance, method, path, body=None, env=None, role='admin', cookie=None, locale='zh-CN'):
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Accept-Language': locale, 'Origin': ORIGINS[instance], 'Cookie': cookie or session_cookie(instance, role)}
    if env != '':
        headers['X-AIOps-Environment'] = env or DEFAULT_ENV[instance]
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1' + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=60) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode(errors='replace')[:500]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('instance', choices=ORIGINS)
    ap.add_argument('method')
    ap.add_argument('path')
    ap.add_argument('body', nargs='?')
    ap.add_argument('--env')
    ap.add_argument('--role', default='admin')
    a = ap.parse_args()
    status, body = call(a.instance, a.method.upper(), a.path, json.loads(a.body) if a.body else None, a.env, a.role)
    print(status)
    print(json.dumps(body, ensure_ascii=False, indent=2) if not isinstance(body, str) else body)
    sys.exit(0 if status < 400 else 1)


if __name__ == '__main__':
    main()
