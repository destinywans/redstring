#!/usr/bin/env python3
"""Snapshot the logs page answers before and after a reader switch (PA-19 stage 2 step 3).

  python3 tools/acceptance/logs_baseline.py <instance> <before|after>
  python3 tools/acceptance/logs_baseline.py <instance> compare

For each service: 1 h totals, a 15 min ERROR search, an Elasticsearch provider filter (should stay
not_configured for Loki services) and an empty levels filter; plus one query across all services.
Writes .local/acceptance/e2e/logs-<instance>-<phase>.json; compare prints the differences, ignoring
counts that move with time by less than 25 %.
"""
import json
import sys
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENV = {'prod': 'prod', 'nonprod': 'test'}
SERVICES = ['checkout-api', 'order-api', 'message-api']


def query(instance, params):
    status, body = call(instance, 'GET', '/logs?' + urllib.parse.urlencode(params), env=ENV[instance])
    if status != 200:
        return {'status': status, 'error': (body or {}).get('error')}
    items = body.get('items') or []
    return {'status': status, 'total': body.get('total'), 'errors': body.get('errors'), 'items': len(items),
            'unavailable': body.get('unavailable'), 'sources': sorted({i.get('source') or i.get('provider') or '' for i in items})}


def snapshot(instance):
    out = {}
    for svc in SERVICES:
        out[f'{svc} 1h'] = query(instance, {'service': svc, 'range': '1h'})
        out[f'{svc} errors q'] = query(instance, {'service': svc, 'levels': 'ERROR', 'range': '15m', 'q': 'checkout'})
        out[f'{svc} provider=elasticsearch'] = query(instance, {'service': svc, 'range': '1h', 'provider': 'elasticsearch'})
        out[f'{svc} levels empty'] = query(instance, {'service': svc, 'levels': '', 'range': '1h'})
    out['all ERROR 1h'] = query(instance, {'levels': 'ERROR', 'range': '1h'})
    return out


def close(a, b):
    if a == b:
        return True
    if isinstance(a, int) and isinstance(b, int):
        return abs(a - b) <= max(3, 0.25 * max(a, b))
    return False


def main():
    instance, phase = sys.argv[1], sys.argv[2]
    path = lambda p: LOCAL / f'e2e/logs-{instance}-{p}.json'  # noqa: E731
    if phase in ('before', 'after'):
        data = snapshot(instance)
        path(phase).write_text(json.dumps(data, ensure_ascii=False, indent=1))
        for k, v in data.items():
            print(f'{k:<36} {json.dumps(v, ensure_ascii=False)[:160]}')
        return
    before, after = json.loads(path('before').read_text()), json.loads(path('after').read_text())
    diffs = 0
    for k in before:
        b, a = before[k], after.get(k, {})
        bad = [f for f in ('status', 'total', 'errors', 'items', 'unavailable', 'error') if not close(b.get(f), a.get(f))]
        if bad:
            diffs += 1
            print(f'DIFF {k}: ' + '; '.join(f'{f} {b.get(f)!r} -> {a.get(f)!r}' for f in bad))
    print(f'{len(before) - diffs}/{len(before)} queries match')


if __name__ == '__main__':
    main()
