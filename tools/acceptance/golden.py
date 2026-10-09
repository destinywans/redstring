#!/usr/bin/env python3
"""API-level golden paths on the prod acceptance instance (operator account, real model).

  python3 tools/acceptance/golden.py canvas|conversation [--keep]

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
from api import LOCAL, ORIGINS, call  # noqa: E402
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


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('path', choices=['canvas', 'conversation'])
    a = ap.parse_args()
    run = Run(a.path)
    accounts = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']
    run.cookie = login('prod', accounts['operator'])
    {'canvas': canvas, 'conversation': conversation}[a.path](run)
    print(run.save())


if __name__ == '__main__':
    main()
