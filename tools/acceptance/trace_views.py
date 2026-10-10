#!/usr/bin/env python3
"""Check the trace list across views and ranges on both instances (PA-78..80, PA-87).

  python3 tools/acceptance/trace_views.py [prod|nonprod ...]

The trace page opens on the Errors view, so the page crawl never loads the All view; this reads
the list directly. At lab traffic every view must answer 200: All and Slow stop at the search
budget (partial_reason search_budget), Errors and a narrow TraceQL cover the range or stop at the
budget, a range beyond the lookback is 422 range_exceeds_lookback. Exits 1 on any mismatch.
"""
import json
import sys
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import call  # noqa: E402

ENV = {'prod': 'prod', 'nonprod': 'test'}
REASONS = ('search_budget', 'search_unattested')
CASES = [
    ('all 1h', {'range': '1h'}, 200),
    ('all 24h', {'range': '24h'}, 200),
    ('all 7d', {'range': '7d'}, 200),
    ('slow 1h', {'view': 'slow', 'range': '1h'}, 200),
    ('errors 1h', {'view': 'errors', 'range': '1h'}, 200),
    ('service 1h', {'service': 'checkout-api', 'range': '1h'}, 200),
    ('errors + q 1h', {'view': 'errors', 'range': '1h', 'q': '{ duration > 2s }'}, 200),
    ('all 30d', {'range': '30d'}, 422),
]


def check(instance):
    failed = 0
    for label, params, want in CASES:
        status, body = call(instance, 'GET', '/traces?' + urllib.parse.urlencode({'scope': 'app', **params}), env=ENV[instance])
        body = body if isinstance(body, dict) else {}
        if want == 200:
            ok = status == 200 and body.get('partial_reason') in REASONS and body.get('window_start_at')
            detail = f"{len(body.get('items') or [])} items, {body.get('partial_reason')}" if status == 200 else body.get('error')
        else:
            ok = status == 422 and (body.get('error') or {}).get('code') == 'range_exceeds_lookback'
            detail = body.get('error')
        failed += not ok
        print(f"{'PASS' if ok else 'FAIL'}  {instance:<8} {label:<14} {status}  {json.dumps(detail, ensure_ascii=False)[:120]}")
    return failed


def main():
    instances = sys.argv[1:] or ['prod', 'nonprod']
    failed = sum(check(i) for i in instances)
    total = len(CASES) * len(instances)
    print(f'{total - failed}/{total} passed')
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
