#!/usr/bin/env python3
"""API-level golden paths on the prod acceptance instance (operator account, real model).

  python3 tools/acceptance/golden.py canvas|conversation [--keep]

knowledge     bad-release drill → declare an incident from the firing alert (operator) →
              timeline note → drill off → mitigated → resolved → postmortem draft and
              save (admin) → runbook from the postmortem, dry run, submit → upload a
              Markdown document, register it as a knowledge source, search.
settings      admin: API token issue → Bearer read in scope / refused out of scope → rotate
              (old and new valid) → revoke; workspace PUT with current/stale/no ETag;
              notification and AI preferences round trip; audit export and detail.
canvas        new investigation → question on the root node (model with read-only
              tools) → derive a child branch → question on the child → rename the
              child (layout version) → writeback preview → write the child's
              conclusion back to the root → a stale writeback must answer 409.
conversation  @ mentions and / commands lists → upload a text attachment → message
              with the attachment and a context ref → SSE event stream, then a
              reconnect with Last-Event-ID resumes after the last event.

Each step records status, timing and the ids it produced in
.local/acceptance/e2e/golden/<path>.json; the run stops at the first failed step.
"""
import argparse
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
from permissions import login  # noqa: E402

OUT = LOCAL / 'e2e/golden'


class Run:
    def __init__(self, name):
        self.name, self.steps, self.cookie = name, [], None

    def step(self, title, method, path, body=None, ok=(200, 201, 202), **kw):
        started = time.time()
        status, data = call('prod', method, path, body, cookie=self.cookie, **kw)
        row = {'step': title, 'call': f'{method} {path}', 'status': status, 'seconds': round(time.time() - started, 1)}
        if isinstance(data, dict) and isinstance(data.get('error'), dict):
            row['code'] = data['error'].get('code')
            row['message'] = data['error'].get('message')
        row['ok'] = status in ok
        self.steps.append(row)
        print(f"{'ok ' if row['ok'] else 'FAIL'} {status} {row['seconds']:>5}s  {title}  {row.get('code') or ''}", flush=True)
        if not row['ok']:
            raise SystemExit(self.save())
        return data

    def note(self, **kw):
        self.steps[-1].update(kw)

    def save(self):
        OUT.mkdir(parents=True, exist_ok=True)
        path = OUT / f'{self.name}.json'
        path.write_text(json.dumps(self.steps, ensure_ascii=False, indent=1))
        return f'report: {path}'


def first_id(node_list, **match):
    for n in node_list:
        if all(n.get(k) == v for k, v in match.items()):
            return n
    return None


def canvas(run):
    _, services = call('prod', 'GET', '/services', cookie=run.cookie)
    app_ids = {}
    import base64
    for i in services['items']:
        raw = base64.urlsafe_b64decode(i['id'] + '=' * (-len(i['id']) % 4)).decode()
        app_ids[i['name']] = raw.split('\x00')[0]
    created = run.step('new investigation canvas', 'POST', '/canvases', {
        'title': '验收：画布分支与回写', 'kind': 'investigation', 'template_id': None,
        'goal': '确认 checkout-api 与 order-api 当前是否健康', 'services': [app_ids['checkout-api']], 'ai_draft': False})
    inv = created['id']
    run.note(investigation=inv)
    graph = run.step('read canvas graph', 'GET', f'/investigations/{inv}/canvas')
    nodes = graph.get('nodes') or []
    root = next((n for n in nodes if not n.get('parent_id') and not n.get('parents')), nodes[0] if nodes else None)
    run.note(nodes=len(nodes), root=root and root.get('id'))
    root_id = root['id']
    reply = run.step('question on the root node (model + tools)', 'POST', f'/investigations/{inv}/nodes/{root_id}/messages',
                     {'text': 'checkout-api 现在的错误率和 p95 是多少？用两句话回答并注明证据来源。'})
    run.note(summary=str(reply.get('summary'))[:300])
    child = run.step('derive a child branch', 'POST', f'/investigations/{inv}/branches',
                     {'parent_id': root_id, 'title': '排查错误日志', 'expected_parent_revision': root.get('revision')})
    child_id = child['id']
    run.note(child=child_id, context_revision=child.get('context_revision') or child.get('revision'))
    reply = run.step('question on the child branch', 'POST', f'/investigations/{inv}/nodes/{child_id}/messages',
                     {'text': 'checkout-api 最近 15 分钟有没有 error 级别日志？一句话，注明证据来源。'})
    run.note(summary=str(reply.get('summary'))[:300])
    graph = run.step('re-read canvas graph', 'GET', f'/investigations/{inv}/canvas')
    node = first_id(graph.get('nodes') or [], id=child_id) or {}
    renamed = run.step('rename the child (layout version)', 'PATCH', f'/investigations/{inv}/nodes/{child_id}',
                       {'title': '排查错误日志（已确认）', 'expected_title': node.get('title'),
                        'expected_layout_version': node.get('layout_version')})
    run.step('stale layout version is refused', 'PATCH', f'/investigations/{inv}/nodes/{child_id}',
             {'x': 400, 'y': 300, 'expected_layout_version': node.get('layout_version')}, ok=(409,))
    preview = run.step('writeback preview child → root', 'GET', f'/investigations/{inv}/writeback-preview?node={child_id}&target={root_id}')
    items = preview.get('conclusions') or []
    run.note(conclusions=len(items), checks=[c.get('text') for c in preview.get('checks') or []])
    chosen = [i.get('id') for i in items if i.get('id')][:1]
    body = {'node': child_id, 'target': root_id, 'conclusions': chosen, 'mode': 'summary',
            'expected_source_revision': preview.get('source_revision'), 'expected_target_revision': preview.get('target_revision')}
    written = run.step('write the conclusion back to the root', 'POST', f'/investigations/{inv}/writeback', body)
    run.note(result=written)
    run.step('stale writeback is refused', 'POST', f'/investigations/{inv}/writeback', body, ok=(409,))
    graph = run.step('root shows the written-back conclusion', 'GET', f'/investigations/{inv}/canvas')
    run.note(graph_keys=sorted(graph.keys()))
    return inv


def sse(cid, cookie, last_event_id=None, seconds=60):
    """Read the conversation event stream; returns [(id, event)] until it ends or times out."""
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Origin': ORIGINS['prod'], 'Cookie': cookie, 'X-AIOps-Environment': 'prod', 'Accept': 'text/event-stream'}
    if last_event_id:
        headers['Last-Event-ID'] = last_event_id
    req = urllib.request.Request(ORIGINS['prod'] + f'/api/v1/conversations/{cid}/events', headers=headers)
    events, current = [], {}
    started = time.time()
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=seconds) as r:
            for raw in r:
                line = raw.decode().rstrip('\n')
                if line.startswith('id:'):
                    current['id'] = line[3:].strip()
                elif line.startswith('event:'):
                    current['event'] = line[6:].strip()
                elif line == '' and current:
                    events.append((current.get('id'), current.get('event')))
                    if current.get('event') in ('message.completed', 'message.failed', 'done'):
                        break
                    current = {}
                if time.time() - started > seconds:
                    break
    except (TimeoutError, urllib.error.URLError, OSError):
        pass
    return events


def conversation(run):
    mentions = run.step('@ mention candidates', 'GET', '/mentions?q=checkout')
    items = mentions.get('items') or mentions.get('groups') or []
    run.note(count=len(items), sample=json.dumps(items[:1], ensure_ascii=False)[:200])
    commands = run.step('/ command list', 'GET', '/commands')
    run.note(count=len(commands.get('items') or commands.get('commands') or []))
    boundary = secrets.token_hex(8)
    content = 'order-api 2026-10-09 17:00 起偶发 504，网关日志片段：upstream timeout 1.5s\n'.encode()
    body = (f'--{boundary}\r\nContent-Disposition: form-data; name="kind"\r\n\r\nfile\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="gateway.txt"\r\n'
            'Content-Type: text/plain\r\n\r\n').encode() + content + f'\r\n--{boundary}--\r\n'.encode()
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    req = urllib.request.Request(ORIGINS['prod'] + '/api/v1/uploads', body, method='POST', headers={
        'Origin': ORIGINS['prod'], 'Cookie': run.cookie, 'X-AIOps-Environment': 'prod',
        'Content-Type': f'multipart/form-data; boundary={boundary}', 'Idempotency-Key': secrets.token_hex(16)})
    started = time.time()
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=60) as r:
            upload, status = json.loads(r.read()), r.status
    except urllib.error.HTTPError as e:
        upload, status = json.loads(e.read() or b'{}'), e.code
    run.steps.append({'step': 'upload a text attachment', 'call': 'POST /uploads', 'status': status,
                      'seconds': round(time.time() - started, 1), 'ok': status in (200, 201), 'upload': upload})
    print(f"{'ok ' if status in (200, 201) else 'FAIL'} {status}  upload a text attachment", flush=True)
    if status not in (200, 201):
        raise SystemExit(run.save())
    mention = items[0] if items and isinstance(items[0], dict) else None
    ctx_ref = (mention or {}).get('ref') or (mention or {}).get('id')
    created = run.step('new conversation with an @ context', 'POST', '/conversations',
                       {'text': '附件里的网关日志说明什么？', **({'context': ctx_ref} if ctx_ref else {})})
    cid = created['id']
    posted = run.step('message with the attachment', 'POST', f'/conversations/{cid}/messages',
                      {'text': '结合附件判断 order-api 的 504 和 checkout-api 有没有关系，三句话以内。',
                       'attachments': [upload]})  # the upload object exactly as returned
    run.note(reply_id=posted.get('reply_id'))
    first = sse(cid, run.cookie, seconds=20)
    run.steps.append({'step': 'SSE stream (first 20 s)', 'events': len(first), 'last': first[-1] if first else None,
                      'ok': bool(first)})
    print(f"{'ok ' if first else 'FAIL'}  SSE stream: {len(first)} events, last {first[-1] if first else None}", flush=True)
    resume_from = next((e[0] for e in reversed(first) if e[0]), None)
    rest = sse(cid, run.cookie, last_event_id=resume_from, seconds=120)
    replayed = [e for e in rest if e[0] and resume_from and e[0] == resume_from]
    ok = bool(resume_from) and not replayed and (rest or (first and first[-1][1] in ('message.completed', 'message.failed')))
    run.steps.append({'step': 'reconnect with Last-Event-ID resumes after the last event', 'resume_from': resume_from,
                      'events': len(rest), 'last': rest[-1] if rest else None, 'replayed_last': bool(replayed), 'ok': bool(ok)})
    print(f"{'ok ' if ok else 'FAIL'}  resume from {resume_from}: {len(rest)} events, last {rest[-1] if rest else None}", flush=True)
    detail = run.step('final conversation', 'GET', f'/conversations/{cid}')
    last = (detail.get('messages') or [{}])[-1]
    run.note(state=last.get('state'), answer=''.join(p.get('markdown', '') for p in last.get('parts', []) if p.get('type') == 'text')[:400])
    return cid


LAB = Path.home() / 'Home/code/codex/aiops-test'


def lab(*args):
    import subprocess
    r = subprocess.run(['python3', 'scripts/lab-faults.py', *args], cwd=LAB, capture_output=True, text=True, timeout=600)
    return (r.stdout + r.stderr).strip().splitlines()[-2:]


def app_id(name):
    import base64
    _, services = call('prod', 'GET', '/services')
    for i in services['items']:
        if i['name'] == name:
            return i['id'], base64.urlsafe_b64decode(i['id'] + '=' * (-len(i['id']) % 4)).decode().split('\x00')[0]
    raise SystemExit(f'no service {name}')


def upload(cookie, filename, content, kind='file'):
    boundary = secrets.token_hex(8)
    body = (f'--{boundary}\r\nContent-Disposition: form-data; name="kind"\r\n\r\n{kind}\r\n'
            f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{filename}"\r\n'
            'Content-Type: text/markdown\r\n\r\n').encode() + content + f'\r\n--{boundary}--\r\n'.encode()
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    req = urllib.request.Request(ORIGINS['prod'] + '/api/v1/uploads', body, method='POST', headers={
        'Origin': ORIGINS['prod'], 'Cookie': cookie, 'X-AIOps-Environment': 'prod',
        'Content-Type': f'multipart/form-data; boundary={boundary}', 'Idempotency-Key': secrets.token_hex(16)})
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=60) as r:
            return r.status, json.loads(r.read())
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b'{}')


RESOURCES = ['conversation', 'events', 'alert_rules', 'silences', 'proposals', 'runbooks', 'knowledge', 'services',
             'connectors', 'agents', 'identity', 'audit', 'resources']


def ensure_knowledge_reviewer():
    """D9: a second person must publish a postmortem (self review is refused) and only admin holds
    knowledge.manage built in, so a custom global role gives approver2 knowledge/runbook manage."""
    matrix = [{'resource': r, 'grants': [1, 0, 0, 0, 1 if r in ('knowledge', 'runbooks') else 0]} for r in RESOURCES]
    status, body = call('prod', 'POST', '/roles', {'id': 'custom-knowledge-reviewer', 'name': '知识审核',
                                                   'description': '审核并发布复盘与 Runbook（验收 D9）',
                                                   'scopes': [{'kind': 'global', 'id': '*'}], 'matrix': matrix})
    if status not in (200, 201):
        raise SystemExit(f'create role: {status} {body}')
    accounts = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']
    _, members = call('prod', 'GET', '/members')
    mid = next(m['id'] for m in members['items'] if m.get('email') == accounts['approver2']['email'])
    _, access = call('prod', 'GET', f'/members/{mid}/access')
    current = {r['id']: str(r['matrix_revision']) for r in access['roles'] if r.get('origin') == 'direct'}
    if 'custom-knowledge-reviewer' not in current:
        headers = {}
        call('prod', 'GET', '/roles/custom-knowledge-reviewer', headers_out=headers)
        current['custom-knowledge-reviewer'] = headers.get('X-Role-Matrix-Revision', '1')
        status, body = call('prod', 'PUT', f'/members/{mid}/access', {'expected_revision': access['revision'],
                            'role_ids': sorted(current), 'role_revisions': current, 'enabled': True})
        if status != 200:
            raise SystemExit(f'assign role: {status} {body}')
    return login('prod', accounts['approver2'])


def knowledge(run):
    """Incident during a real bad-release drill → resolve → postmortem → runbook → knowledge source and search."""
    operator = run.cookie
    _, app = app_id('checkout-api')

    def firing_ids():
        _, groups = call('prod', 'GET', '/alert-groups', cookie=operator)
        return [i['id'] for g in groups.get('items') or [] if g.get('service') == app and g.get('state') == 'firing'
                for i in g.get('instances') or [] if i.get('state') == 'firing']

    # Start clean: earlier checkout-api alerts must have resolved first.
    started = time.time()
    while firing_ids() and time.time() - started < 420:
        time.sleep(15)
    print('drill on:', lab('drill', 'bad-release', 'on'), flush=True)
    alert, tried = None, set()
    try:
        started = time.time()
        while time.time() - started < 360 and not alert:
            for candidate in firing_ids():
                if candidate in tried:
                    continue
                tried.add(candidate)
                if call('prod', 'GET', f'/incidents/draft?alerts={candidate}', cookie=operator)[0] == 200:
                    alert = candidate
                    break
            if not alert:
                time.sleep(10)
        run.steps.append({'step': 'firing checkout-api alert from the drill', 'alert': alert, 'seconds': round(time.time() - started), 'ok': bool(alert)})
        print(f"{'ok ' if alert else 'FAIL'}  firing alert {alert} after {round(time.time() - started)} s", flush=True)
        if not alert:
            raise SystemExit(run.save())
        draft = run.step('incident draft from the alert', 'GET', f'/incidents/draft?alerts={alert}')
        run.note(draft={k: draft.get(k) for k in ('title', 'severity', 'services', 'env')})
        created = run.step('declare the incident (operator)', 'POST', '/incidents', {
            'title': draft.get('title') or 'checkout-api 5xx', 'severity': draft.get('severity') or 'P1',
            'impact': '下单接口约一半请求返回 500', 'env': 'prod', 'services': [app], 'alert_ids': [alert],
            'war_room': False, 'notify': False, 'status_page': False, 'open_canvas': False, 'participants': ['Operator']})
        inc = created['id']
        run.note(incident=inc)
        run.step('timeline note', 'POST', f'/incidents/{inc}/timeline', {'text': '确认为坏发布导致 5xx，执行回滚（关闭演练）'})
    finally:
        print('drill off:', lab('drill', 'bad-release', 'off'), flush=True)
    detail = run.step('incident detail', 'GET', f'/incidents/{inc}')
    revision = detail.get('status_revision', detail.get('revision'))
    run.note(status=detail.get('status'), revision=revision)
    for target in ('mitigated', 'resolved'):
        changed = run.step(f'incident → {target}', 'POST', f'/incidents/{inc}/status',
                           {'status': target, 'expected_revision': revision, 'request_id': secrets.token_hex(16),
                            'reason': '回滚完成，5xx 归零' if target == 'resolved' else '已回滚，观察中'})
        revision = changed.get('revision', revision + 1 if isinstance(revision, int) else revision)
    run.cookie = None  # admin from here on: the stored browser state (the admin signs in with MFA)
    candidates = run.step('postmortem candidates (admin)', 'GET', '/postmortems/candidates')
    found = [c for c in candidates.get('items') or [] if c.get('incident_id') == inc or c.get('id') == inc]
    run.note(candidates=len(candidates.get('items') or []), includes_incident=bool(found))
    pm_draft = run.step('postmortem draft', 'GET', f'/postmortems/draft?incident={inc}')
    run.note(draft_keys=sorted(pm_draft.keys()))
    # PA-65: a draft postmortem can never move to review, so the author submits for review directly.
    pm = run.step('submit the postmortem for review', 'POST', '/postmortems', {
        'incident_id': inc, 'source_revision': pm_draft.get('source_revision'),
        'sections': pm_draft.get('sections') or [], 'actions': pm_draft.get('actions') or [], 'status': 'review'})
    pm_id = pm['id']
    run.note(postmortem=pm_id)
    # The author may not publish their own postmortem (403 self_review_forbidden, checked below).
    run.step('author cannot publish own postmortem', 'POST', f'/postmortems/{pm_id}/publish', {}, ok=(403,))
    run.cookie = ensure_knowledge_reviewer()
    run.step('reviewer (approver2, custom role) publishes the postmortem', 'POST', f'/postmortems/{pm_id}/publish', {})
    run.cookie = None
    # PA-63: only postmortems of incidents closed through an investigation can seed a runbook.
    run.step('runbook draft from a manual-incident postmortem (PA-63)', 'GET',
             f'/runbooks/draft?source=postmortem&from={pm_id}', ok=(200, 422))
    doc = ('# checkout-api 坏发布回滚手册\n\n## 现象\n下单接口 5xx 比例超过 30%，order-api 同时报 5xx。\n\n'
           '## 处置\n1. 确认最近一次发布的 ReplicaSet。\n2. 回滚到上一版本。\n3. 观察 5xx 归零后再关闭事件。\n').encode()
    status, up = upload(session_cookie('prod', 'admin'), 'checkout-rollback.md', doc)
    run.steps.append({'step': 'upload a markdown document (admin)', 'status': status, 'ok': status in (200, 201), 'upload': up})
    print(f"{'ok ' if status in (200, 201) else 'FAIL'} {status}  upload a markdown document", flush=True)
    if status not in (200, 201):
        raise SystemExit(run.save())
    rb_draft = run.step('runbook draft imported from the document', 'GET', f"/runbooks/draft?source=import&from={up['id']}")
    run.note(steps=len(rb_draft.get('steps') or []), sha=bool(rb_draft.get('source_sha256')), title=rb_draft.get('title'))
    steps = rb_draft.get('steps') or [{'title': '确认最近一次发布', 'kind': 'manual'}, {'title': '回滚到上一版本', 'kind': 'manual'}]
    rb = run.step('save the runbook as draft', 'POST', '/runbooks', {
        'title': 'checkout-api 坏发布回滚', 'source': 'import', 'from': up['id'],
        'expected_source_sha256': rb_draft.get('source_sha256'), 'steps': steps, 'status': 'draft'})
    rb_id = rb['id']
    run.note(runbook=rb_id)
    run.step('runbook dry run', 'POST', f'/runbooks/{rb_id}/dry-run', {})
    run.step('submit the runbook for review', 'POST', f'/runbooks/{rb_id}/submit')
    source = {'type': 'upload', 'url': up['url'], 'application_id': app}
    run.step('probe the knowledge source', 'POST', '/knowledge-sources/test', source)
    reg = run.step('register the knowledge source', 'POST', '/knowledge-sources', {
        **source, 'name': '验收：checkout-api 回滚手册', 'spaces': [], 'schedule': 'manual', 'chunk': 'heading', 'inherit_acl': True})
    run.note(source=reg)
    hits = run.step('search the knowledge base', 'POST', '/knowledge/search', {'q': 'checkout-api 坏发布 回滚', 'top_k': 5})
    run.note(hits=[(h.get('title'), h.get('score')) for h in (hits.get('hits') or [])][:5])
    return inc


def bearer(method, path, token, body=None):
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Authorization': f'Bearer {token}', 'X-AIOps-Environment': 'prod', 'Origin': ORIGINS['prod']}
    data = None
    if body is not None:
        data, headers['Content-Type'] = json.dumps(body).encode(), 'application/json'
        headers['Idempotency-Key'] = secrets.token_hex(16)
    req = urllib.request.Request(ORIGINS['prod'] + '/api/v1' + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


def settings(run):
    """Admin: API token issue/use/rotate/revoke, workspace ETag writes, personal preferences, audit export."""
    run.cookie = None  # admin (stored browser state)
    issued = run.step('issue an API token (1 day, connectors:view)', 'POST', '/api-tokens',
                      {'name': 'acceptance-probe', 'scopes': ['connectors:view'], 'ttl': 86400})
    token, tid = issued.get('secret_shown_once'), issued.get('id')
    run.note(id=tid, secret_returned=bool(token))
    listed = run.step('token list hides the secret', 'GET', '/api-tokens')
    raw = json.dumps(listed)
    run.note(secret_in_list=bool(token) and token in raw)
    if token and token in raw:
        run.steps[-1]['ok'] = False
    checks = {'bearer GET /connectors': (bearer('GET', '/connectors', token), {200}),
              'bearer GET /services (out of scope)': (bearer('GET', '/services', token), {401, 403}),
              'bearer POST /connectors (write)': (bearer('POST', '/connectors', token, {}), {401, 403, 405})}
    rotated = run.step('rotate the token', 'POST', f'/api-tokens/{tid}/rotate', {})
    new = rotated.get('secret_shown_once')
    checks['old secret still valid after rotate'] = (bearer('GET', '/connectors', token), {200})
    checks['new secret valid'] = (bearer('GET', '/connectors', new), {200})
    new_id = rotated.get('id') or tid
    run.step('revoke the rotated token', 'DELETE', f'/api-tokens/{new_id}', ok=(200, 204))
    checks['new secret refused after revoke'] = (bearer('GET', '/connectors', new), {401, 403})
    for name, (status, expected) in checks.items():
        run.steps.append({'step': name, 'status': status, 'ok': status in expected})
        print(f"{'ok ' if status in expected else 'FAIL'} {status}  {name}", flush=True)
    headers = {}
    status, workspace = call('prod', 'GET', '/workspace', headers_out=headers)
    etag = headers.get('ETag') or headers.get('Etag')
    run.steps.append({'step': 'workspace read with ETag', 'status': status, 'etag': bool(etag), 'ok': status == 200 and bool(etag)})
    body = {k: workspace.get(k) for k in ('name', 'slug', 'timezone', 'locale', 'week_start', 'retention_days', 'data_region', 'brand')}
    def put_workspace(if_match):
        extra = {} if if_match is None else {'If-Match': if_match}
        ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
        hdr = {'Origin': ORIGINS['prod'], 'Cookie': session_cookie('prod', 'admin'), 'X-AIOps-Environment': 'prod',
               'Content-Type': 'application/json', 'Idempotency-Key': secrets.token_hex(16), **extra}
        req = urllib.request.Request(ORIGINS['prod'] + '/api/v1/workspace', json.dumps(body).encode(), method='PUT', headers=hdr)
        try:
            with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
                return r.status, r.headers.get('ETag')
        except urllib.error.HTTPError as e:
            return e.code, None
    s1, new_etag = put_workspace(etag)
    s2, _ = put_workspace(etag)
    s3, _ = put_workspace(None)
    for name, status, expected in (('workspace PUT with current ETag', s1, {200}), ('workspace PUT with stale ETag', s2, {412}),
                                   ('workspace PUT without If-Match', s3, {428})):
        run.steps.append({'step': name, 'status': status, 'ok': status in expected})
        print(f"{'ok ' if status in expected else 'FAIL'} {status}  {name}", flush=True)
    before = run.step('notification settings', 'GET', '/me/notification-settings')
    digest = 'weekly' if before.get('digest') != 'weekly' else 'daily'
    after = run.step('change the digest', 'PUT', '/me/notification-settings', {'digest': digest})
    run.note(saved=after.get('digest') == digest)
    run.step('restore the digest', 'PUT', '/me/notification-settings', {'digest': before.get('digest') or 'off'})
    ai = run.step('AI preferences', 'GET', '/me/ai-preferences')
    run.step('save AI preferences unchanged', 'PUT', '/me/ai-preferences', ai)
    exported = run.step('audit export', 'POST', '/audit/export', {'filter': 'all'})
    run.note(url=exported.get('url'))
    audit = run.step('audit list', 'GET', '/audit?filter=user')
    first = (audit.get('items') or [{}])[0]
    detail = run.step('audit detail with hash-chain check', 'GET', f"/audit/{first.get('id')}")
    run.note(chain={k: detail.get(k) for k in ('chain', 'hash_chain', 'verified', 'integrity') if k in detail})
    sec = run.step('security policy view (no authority configured)', 'GET', '/security')
    run.note(unavailable=sec.get('unavailable'))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('path', choices=['canvas', 'conversation', 'knowledge', 'settings'])
    a = ap.parse_args()
    run = Run(a.path)
    accounts = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']
    run.cookie = login('prod', accounts['operator'])
    {'canvas': canvas, 'conversation': conversation, 'knowledge': knowledge, 'settings': settings}[a.path](run)
    print(run.save())


if __name__ == '__main__':
    main()
