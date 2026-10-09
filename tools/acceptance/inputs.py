#!/usr/bin/env python3
"""Input-boundary checks against the prod acceptance instance (admin unless noted).

  python3 tools/acceptance/inputs.py

Every case must end in an explicit, documented answer (2xx, or 4xx with an error
code), never 500, a hang or a silent truncation:
  * oversized and malformed JSON bodies, unknown fields;
  * a conversation title/text far beyond any limit;
  * the same Idempotency-Key twice (same body → same result; other body → refused);
  * a stale revision on a revisioned write (409);
  * an upload over the 20 MiB limit (413) and an empty upload.
Creates at most one conversation (the idempotency case). Writes .local/acceptance/e2e/inputs.json.
"""
import json
import secrets
import ssl
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call, session_cookie  # noqa: E402


def send(method, path, data, content_type, key=None, timeout=60):
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Origin': ORIGINS['prod'], 'Cookie': session_cookie('prod', 'admin'), 'X-AIOps-Environment': 'prod',
               'Content-Type': content_type, 'Idempotency-Key': key or secrets.token_hex(16)}
    if key == '':
        del headers['Idempotency-Key']
    req = urllib.request.Request(ORIGINS['prod'] + '/api/v1' + path, data=data, method=method, headers=headers)
    started = time.time()
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=timeout) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None, round(time.time() - started, 1)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw), round(time.time() - started, 1)
        except Exception:
            return e.code, raw[:200].decode(errors='replace'), round(time.time() - started, 1)


def code(body):
    return body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None


def multipart(filename, content):
    boundary = secrets.token_hex(12)
    head = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
            'Content-Type: text/plain\r\n\r\n').encode()
    return head + content + f'\r\n--{boundary}--\r\n'.encode(), f'multipart/form-data; boundary={boundary}'


def main():
    rows = []

    def check(name, status, expected, body=None, seconds=None, note=''):
        ok = status in expected and status != 500
        rows.append({'ok': ok, 'check': name, 'status': status, 'code': code(body), 'seconds': seconds, 'note': note})

    big = json.dumps({'text': 'x' * (3 << 20)}).encode()
    s, b, t = send('POST', '/conversations', big, 'application/json')
    check('JSON body over 2 MiB', s, {413, 422}, b, t)
    s, b, t = send('POST', '/conversations', b'{"text": "unterminated', 'application/json')
    check('malformed JSON', s, {400, 422}, b, t)
    s, b, t = send('POST', '/conversations', b'{"text": "hi"} {"text": "again"}', 'application/json')
    check('two JSON objects in one body', s, {400, 422}, b, t)
    s, b, t = send('POST', '/conversations', json.dumps({'text': 'hi', 'unexpected_field': 1}).encode(), 'application/json')
    check('unknown field', s, {400, 422}, b, t)
    # POST /conversations only seeds the title from `text` (no message, no model call).
    s, b, t = send('POST', '/conversations', json.dumps({'text': '长' * 200000}).encode(), 'application/json')
    _, created = call('prod', 'GET', f"/conversations/{b.get('id')}") if isinstance(b, dict) and b.get('id') else (0, {})
    title = (created or {}).get('title') or ''
    check('conversation seed text 200k chars', s, {201, 400, 413, 422}, b, t, f'stored title {len(title)} chars')
    if s == 201 and len(title) > 200:
        rows[-1]['ok'] = False
    s, b, t = send('POST', '/conversations', b'text=hi', 'application/x-www-form-urlencoded')
    check('wrong content type', s, {400, 415, 422}, b, t)

    # Idempotency on a route that promises it (POST /silences, operator): the same key and body
    # must return the same proposal; the same key with another body must be refused. The probe
    # proposal is rejected afterwards. POST /conversations does not promise idempotency (contract).
    from permissions import login
    accounts = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']
    op = login('prod', accounts['operator'])
    key = secrets.token_hex(16)
    silence = {'investigation_id': 'INV-df39c2dd6350ef10e845afbf03f21a41', 'duration_seconds': 1800,
               'matchers': [{'name': 'alertname', 'op': '=', 'value': 'AcceptanceIdempotencyProbe'},
                            {'name': 'application', 'op': '=', 'value': 'checkout-api'}],
               'comment': 'acceptance idempotency probe; will be rejected'}
    s1, b1 = call('prod', 'POST', '/silences', silence, cookie=op, idempotency_key=key)
    s2, b2 = call('prod', 'POST', '/silences', silence, cookie=op, idempotency_key=key)
    p1, p2 = (b1 or {}).get('proposal_id'), (b2 or {}).get('proposal_id')
    check('silence: same Idempotency-Key, same body', s2, {200, 202}, b2, None, f'first {s1}, same proposal: {bool(p1) and p1 == p2}')
    if not (p1 and p1 == p2):
        rows[-1]['ok'] = False
    other = dict(silence, comment='a different body under the same key')
    s3, b3 = call('prod', 'POST', '/silences', other, cookie=op, idempotency_key=key)
    check('silence: same Idempotency-Key, other body', s3, {409, 422}, b3)
    for pid in {p for p in (p1, p2, (b3 or {}).get('proposal_id')) if p}:
        call('prod', 'POST', f'/proposals/{pid}/reject', {'comment': 'acceptance idempotency probe'})
    s4, b4 = call('prod', 'POST', '/conversations', {'text': 'acceptance double submit'})
    s5, b5 = call('prod', 'POST', '/conversations', {'text': 'acceptance double submit'})
    check('conversation create twice (no idempotency promised)', s5, {201}, b5, None,
          f"distinct ids: {(b4 or {}).get('id') != (b5 or {}).get('id')}")

    # Stale revision → 409: resend the operator's current access with a wrong expected revision.
    _, members = call('prod', 'GET', '/members')
    operator = next(m for m in members['items'] if str(m.get('email', '')).startswith('operator@'))
    _, access = call('prod', 'GET', f"/members/{operator['id']}/access")
    direct = {r['id']: str(r['matrix_revision']) for r in access['roles'] if r.get('origin') == 'direct'}
    revision = str(access['revision'])
    stale = {'expected_revision': revision[:-1] + ('0' if revision[-1] != '0' else '1'),
             'role_ids': sorted(direct), 'role_revisions': direct, 'enabled': True}
    s, b = call('prod', 'PUT', f"/members/{operator['id']}/access", stale)
    check('stale revision on member access', s, {409, 412}, b)

    # Uploads.
    data, ctype = multipart('big.txt', b'a' * (21 << 20))
    s, b, t = send('POST', '/uploads', data, ctype, timeout=120)
    check('upload over 20 MiB', s, {413}, b, t)
    data, ctype = multipart('empty.txt', b'')
    s, b, t = send('POST', '/uploads', data, ctype)
    check('empty upload', s, {201, 400, 422}, b, t, 'empty file: accepted or refused explicitly')

    for r in rows:
        print(f"{'PASS' if r['ok'] else 'FAIL'}  {r['check']:<40} {r['status']} {r['code'] or ''} {r['note']}")
    print(f"{sum(r['ok'] for r in rows)}/{len(rows)} passed")
    (LOCAL / 'e2e' / 'inputs.json').write_text(json.dumps(rows, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
