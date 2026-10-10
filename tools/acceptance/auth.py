#!/usr/bin/env python3
"""Sign-in, sign-out and session checks on the prod acceptance instance (viewer account).

  python3 tools/acceptance/auth.py

  * unknown e-mail and wrong password give the same 401 (no account enumeration);
  * the session cookie is Secure, HttpOnly and SameSite;
  * sign-out ends the session (the old cookie answers 401);
  * a member can revoke another of their own sessions from the profile;
  * repeated failures are rate limited (429), then sign-in works again after the window.
Writes .local/acceptance/e2e/auth.json. Sends at most ~12 sign-in attempts.
"""
import json
import ssl
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call  # noqa: E402


def login(email, password):
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    req = urllib.request.Request(ORIGINS['prod'] + '/api/v1/auth/login', json.dumps({'email': email, 'password': password}).encode(),
                                 method='POST', headers={'Content-Type': 'application/json', 'Origin': ORIGINS['prod']})
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
            cookies = [v for k, v in r.headers.items() if k.lower() == 'set-cookie']
            return r.status, None, cookies
    except urllib.error.HTTPError as e:
        try:
            body = json.loads(e.read())
        except ValueError:
            body = {}
        return e.code, (body.get('error') or {}), []


def main():
    rows = []

    def check(name, ok, detail=''):
        rows.append({'ok': bool(ok), 'check': name, 'detail': detail})
        print(f"{'PASS' if ok else 'FAIL'}  {name}  {detail}", flush=True)

    account = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']['viewer']
    s1, e1, _ = login('nobody@prod.redstring.test', 'wrong-password-123')
    s2, e2, _ = login(account['email'], 'wrong-password-123')
    check('unknown e-mail and wrong password look the same', s1 == s2 == 401 and e1 == e2, f'{s1} {e1} / {s2} {e2}')
    status, _, cookies = login(account['email'], account['password'])
    raw = next((c for c in cookies if c.startswith('aiops_session=')), '')
    attrs = raw.lower()
    check('sign-in succeeds', status == 200, str(status))
    check('session cookie is Secure, HttpOnly, SameSite', all(a in attrs for a in ('secure', 'httponly', 'samesite')), raw.split(';', 1)[-1].strip())
    cookie = raw.split(';', 1)[0]
    status2, _, cookies2 = login(account['email'], account['password'])
    other = next((c for c in cookies2 if c.startswith('aiops_session=')), '').split(';', 1)[0]
    if status2 != 200:
        # A 429 here means earlier runs in the same window used up the sign-in budget, not a session defect.
        check('second sign-in for the session list', False, f'{status2} (rate limited if 429; rerun after a minute)')
    check('signed-in session reads /me', call('prod', 'GET', '/me', cookie=cookie)[0] == 200)
    _, profile = call('prod', 'GET', '/me/profile', cookie=cookie)
    sessions = ((profile or {}).get('security') or {}).get('sessions') or []
    # Session ids are the SHA-256 of the cookie token: revoke exactly the second session.
    import hashlib
    other_id = hashlib.sha256(other.split('=', 1)[1].encode()).hexdigest() if other else ''
    target = next((x for x in sessions if x.get('id') == other_id), None)
    if target:
        status, _ = call('prod', 'DELETE', f"/me/sessions/{target['id']}", cookie=cookie)
        check('revoke my other session', status in (200, 204), str(status))
        check('revoked session answers 401', call('prod', 'GET', '/me', cookie=other)[0] == 401)
    else:
        check('profile lists the second session', False, f'{len(sessions)} sessions, none matches')
    status, _ = call('prod', 'POST', '/auth/logout', {}, cookie=cookie)
    check('sign-out', status in (200, 204), str(status))
    check('signed-out cookie answers 401', call('prod', 'GET', '/me', cookie=cookie)[0] == 401)
    statuses = [login(account['email'], 'wrong-password-123')[0] for _ in range(8)]
    check('repeated failures are rate limited', 429 in statuses, str(statuses))
    time.sleep(65)
    status, _, _ = login(account['email'], account['password'])
    check('sign-in works again after the window', status == 200, str(status))
    print(f"{sum(r['ok'] for r in rows)}/{len(rows)} passed")
    (LOCAL / 'e2e/auth.json').write_text(json.dumps(rows, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
