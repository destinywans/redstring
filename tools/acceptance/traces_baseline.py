#!/usr/bin/env python3
"""Snapshot the trace pages before and after a reader switch (PA-19 stage 2 step 4).

  python3 tools/acceptance/traces_baseline.py <instance> <before|after>
  python3 tools/acceptance/traces_baseline.py <instance> compare

Per service: the default list (at lab traffic it exceeds the 40-trace budget, 413 trace_scope_limit,
see PA-78), two selective TraceQL filters that stay under the budget, and one trace read by ID. The
IDs come straight from the lab Tempo (the same IDs are reused for "after", so the span counts must be
equal), plus the saved trace views. Writes .local/acceptance/e2e/traces-<instance>-<phase>.json.
"""
import json
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENV = {'prod': 'prod', 'nonprod': 'test'}
SERVICES = ['checkout-api', 'order-api', 'message-api']
TEMPO = 'http://127.0.0.1:13200'
FILTERS = ['{ status = error }', '{ duration > 2s }']


def tempo_ids(service, n=2):
    now = int(time.time())
    q = urllib.parse.urlencode({'q': f'{{resource.service.name="{service}"}}', 'start': now - 1800, 'end': now - 120, 'limit': n})
    with urllib.request.urlopen(f'{TEMPO}/api/search?{q}', timeout=15) as r:
        # Tempo search drops leading zeros; the product takes the 32-character form.
        return [t['traceID'].rjust(32, '0') for t in json.load(r).get('traces', [])][:n]


def listing(instance, params):
    status, body = call(instance, 'GET', '/traces?' + urllib.parse.urlencode({'scope': 'app', **params}), env=ENV[instance])
    if status != 200:
        return {'status': status, 'error': ((body or {}).get('error') or {}).get('code')}
    return {'status': status, 'items': len(body.get('items') or []), 'partial': body.get('partial'),
            'unavailable': sorted((body.get('unavailable') or {}).keys())}


def detail(instance, trace_id):
    status, body = call(instance, 'GET', f'/traces/{trace_id}', env=ENV[instance])
    if status != 200:
        return {'status': status, 'error': ((body or {}).get('error') or {}).get('code')}
    spans = body.get('spans') or body.get('tree') or []
    return {'status': status, 'spans': len(spans) if isinstance(spans, list) else None,
            'services': sorted({s.get('service') for s in spans if isinstance(s, dict) and s.get('service')})}


def snapshot(instance, ids):
    out = {}
    for svc in SERVICES:
        out[f'{svc} default'] = listing(instance, {'service': svc})
        for f in FILTERS:
            out[f'{svc} {f}'] = listing(instance, {'service': svc, 'q': f})
        for tid in ids.get(svc, []):
            out[f'{svc} trace {tid[:12]}'] = detail(instance, tid)
    status, views = call(instance, 'GET', '/trace-views?scope=app', env=ENV[instance])
    out['views'] = {'status': status, 'items': len((views or {}).get('items') or [])}
    return out


def main():
    instance, phase = sys.argv[1], sys.argv[2]
    path = lambda p: LOCAL / f'e2e/traces-{instance}-{p}.json'  # noqa: E731
    if phase == 'before':
        ids = {svc: tempo_ids(svc) for svc in SERVICES}
        data = {'ids': ids, 'results': snapshot(instance, ids)}
    elif phase == 'after':
        ids = json.loads(path('before').read_text())['ids']
        data = {'ids': ids, 'results': snapshot(instance, ids)}
    else:
        before, after = (json.loads(path(p).read_text())['results'] for p in ('before', 'after'))
        diffs = [k for k in before if before[k] != after.get(k)]
        for k in diffs:
            print(f'DIFF {k}: {before[k]} -> {after.get(k)}')
        print(f'{len(before) - len(diffs)}/{len(before)} checks match')
        return
    path(phase).write_text(json.dumps(data, ensure_ascii=False, indent=1))
    for k, v in data['results'].items():
        print(f'{k:<40} {json.dumps(v, ensure_ascii=False)[:150]}')


if __name__ == '__main__':
    main()
