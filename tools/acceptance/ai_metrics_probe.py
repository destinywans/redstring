#!/usr/bin/env python3
"""Ask the AI to read reviewed metrics and compare the tool calls before and after a reader switch
(PA-19 stage 2 step 5: read_service_metrics and list_services).

  python3 tools/acceptance/ai_metrics_probe.py <instance> <before|after>
  python3 tools/acceptance/ai_metrics_probe.py <instance> compare

The question names the services and query IDs, so the model's calls are predictable; one ID that is
not configured on the instance is included on purpose (it must stay not_configured). Recorded per run:
each tool call's tool, status and connector, and the evidence titles shown in the reply. Evidence
payloads go to the model only; field-by-field equality is covered by the backend's isolated tests.
Writes .local/acceptance/e2e/ai-metrics-<instance>-<phase>.json.
"""
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENV = {'prod': 'prod', 'nonprod': 'test'}
QUERIES = {
    'nonprod': ('checkout-api', ['request_rate', 'error_rate', 'http_p95_seconds', 'instances_up'], 'pod_restarts'),
    'prod': ('checkout-api', ['request_rate', 'error_rate', 'http_p95_seconds', 'instances_up', 'node_cpu_busy_ratio', 'pod_restarts'], 'dashboard'),
}


def run(instance):
    svc, ids, missing = QUERIES[instance]
    question = (f'先用 list_services 列出可读取的服务；然后只用 read_service_metrics 读取 {svc} 的这些查询 ID：'
                f'{", ".join(ids)}，再试读一次 {missing}。每个查询只读一次，不要读日志。最后用一两句话说明读到了什么、哪个读不到。')
    env = ENV[instance]
    _, conv = call(instance, 'POST', '/conversations', {'text': question}, env=env, role='operator')
    cid = conv['id']
    call(instance, 'POST', f'/conversations/{cid}/messages', {'text': question}, env=env, role='operator')
    started, last = time.time(), {}
    while time.time() - started < 300:
        _, detail = call(instance, 'GET', f'/conversations/{cid}', env=env, role='operator')
        last = (detail.get('messages') or [{}])[-1]
        if last.get('role') == 'assistant' and last.get('state') in ('completed', 'failed'):
            break
        time.sleep(5)
    calls = []
    for part in last.get('parts', []):
        if part.get('type') == 'tool_calls':
            calls += [{'tool': c.get('tool'), 'status': c.get('status'), 'connector': c.get('connector')} for c in part.get('calls', [])]
    titles = sorted(p.get('text') for p in last.get('parts', []) if p.get('type') == 'status')
    answer = ''.join(p.get('markdown', '') for p in last.get('parts', []) if p.get('type') == 'text')
    return {'conversation': cid, 'state': last.get('state'), 'seconds': round(time.time() - started),
            'calls': sorted(calls, key=lambda c: (c['tool'], c['status'], c['connector'] or '')), 'titles': titles, 'answer': answer[:1500]}


def main():
    instance, phase = sys.argv[1], sys.argv[2]
    path = lambda p: LOCAL / f'e2e/ai-metrics-{instance}-{p}.json'  # noqa: E731
    if phase in ('before', 'after'):
        out = run(instance)
        path(phase).write_text(json.dumps(out, ensure_ascii=False, indent=1))
        print(out['state'], f"{out['seconds']}s", json.dumps(out['calls'], ensure_ascii=False))
        print('titles:', out['titles'])
        return
    before, after = (json.loads(path(p).read_text()) for p in ('before', 'after'))
    ok = True
    for key in ('state', 'calls', 'titles'):
        same = before[key] == after[key]
        ok &= same
        print(f"{'same' if same else 'DIFF'} {key}: {before[key]}{'' if same else ' -> ' + json.dumps(after[key], ensure_ascii=False)}")
    print('match' if ok else 'differs (model calls can vary; read the answers in the two files)')


if __name__ == '__main__':
    main()
