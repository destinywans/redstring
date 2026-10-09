#!/usr/bin/env python3
"""Summarise web/e2e crawl results (.local/acceptance/e2e/crawl/*.json).

Groups failing API calls by method + normalised path + status + error code and lists
the routes each one breaks, so one root cause is triaged once. Writes
.local/acceptance/e2e/crawl-summary.md and prints the headline counts.
"""
import collections
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CRAWL = ROOT / '.local/acceptance/e2e/crawl'
OUT = ROOT / '.local/acceptance/e2e/crawl-summary.md'

ID = re.compile(r'/(?:[0-9a-f]{16,}|[A-Z]+-\d+|\d+)(?=/|$)')


def norm(path):
    path = path.split('?', 1)[0]
    return ID.sub('/{id}', path)


def main():
    reports = [json.loads(p.read_text()) for p in sorted(CRAWL.glob('*.json'))]
    if not reports:
        sys.exit('no crawl results')
    failures = collections.defaultdict(lambda: {'routes': set(), 'instances': set(), 'message': '', 'count': 0})
    page_errors = collections.defaultdict(set)
    console = collections.Counter()
    blank = set()
    pages_ok = 0
    for r in reports:
        tag = f"{r['instance']}:{r['route']}"
        bad = False
        for c in r['api']:
            if c['status'] >= 400:
                key = (c['method'], norm(c['path']), c['status'], c.get('code') or '')
                f = failures[key]
                f['routes'].add(r['route'])
                f['instances'].add(r['instance'])
                f['message'] = f['message'] or (c.get('message') or '')
                f['count'] += 1
                bad = True
        for e in r['pageErrors']:
            page_errors[e].add(tag)
            bad = True
        if r['blank']:
            blank.add(tag)
            bad = True
        for e in r['consoleErrors']:
            if not e.startswith('Failed to load resource'):
                console[e[:160]] += 1
        pages_ok += not bad
    lines = ['# Crawl summary', '',
             f'- page loads: {len(reports)}; clean (no API error, exception or blank): {pages_ok}',
             f'- distinct failing API groups: {len(failures)}; uncaught exception kinds: {len(page_errors)}; blank pages: {len(blank)}', '',
             '## Failing API calls (grouped)', '', '| # | call | status | code | instances | routes | message |', '| --- | --- | --- | --- | --- | --- | --- |']
    ranked = sorted(failures.items(), key=lambda kv: (-len(kv[1]['routes']), kv[0]))
    for i, ((method, path, status, code), f) in enumerate(ranked, 1):
        routes = sorted(f['routes'])
        shown = ', '.join(routes[:6]) + (f' …(+{len(routes) - 6})' if len(routes) > 6 else '')
        lines.append(f"| {i} | `{method} {path}` | {status} | {code} | {','.join(sorted(f['instances']))} | {len(routes)}: {shown} | {f['message'][:120]} |")
    lines += ['', '## Uncaught exceptions', '']
    for e, tags in sorted(page_errors.items(), key=lambda kv: -len(kv[1])):
        lines.append(f'- **{e}** — {len(tags)} page(s): ' + ', '.join(sorted(tags)[:8]))
    lines += ['', '## Blank pages', ''] + [f'- {t}' for t in sorted(blank)]
    lines += ['', '## Console errors (excluding failed resource loads)', ''] + [f'- ({n}) {e}' for e, n in console.most_common(30)]
    OUT.write_text('\n'.join(lines) + '\n')
    print(f'{len(reports)} page loads, {pages_ok} clean, {len(failures)} failing API groups, {len(page_errors)} exception kinds, {len(blank)} blank -> {OUT.relative_to(ROOT)}')


if __name__ == '__main__':
    main()
