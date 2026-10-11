#!/usr/bin/env python3
"""One regression pass over the acceptance instances; prints and saves a summary.

  python3 tools/acceptance/regress.py [--with-golden-api] [--tag NAME]

Runs, in order: the full page crawl (both instances × zh/en × light/dark), every Playwright
golden spec, and the API checks (permissions on both instances, environments, inputs, auth).
--with-golden-api adds tools/acceptance/golden.py canvas/conversation/settings/agents (real
model calls, a few cents). Drills, fault injection and the knowledge path are separate
(they touch the lab and take minutes each). Results: .local/acceptance/e2e/regress-<tag>.json.
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
# golden-approval needs a pending proposal id (GOLDEN_PROPOSAL); golden-silence-ui proposes, approves
# with passkeys, executes and expires a silence end to end, so it covers approval in the regression.
SPECS = {'golden-conversation.spec.ts': {}, 'golden-invite.spec.ts': {},
         'golden-silence-ui.spec.ts': {'GOLDEN_INVESTIGATION': os.environ.get('GOLDEN_INVESTIGATION', 'INV-df39c2dd6350ef10e845afbf03f21a41')}}


def run(name, cmd, cwd=ROOT, env=None, timeout=1800, parse=None):
    started = time.time()
    r = subprocess.run(cmd, cwd=cwd, env=dict(os.environ, **(env or {})), capture_output=True, text=True, timeout=timeout)
    out = (r.stdout + r.stderr).strip()
    result = {'name': name, 'exit': r.returncode, 'seconds': round(time.time() - started), 'tail': out.splitlines()[-3:]}
    if parse:
        result.update(parse(out))
    result['ok'] = result.get('ok', r.returncode == 0)
    print(f"{'PASS' if result['ok'] else 'FAIL'}  {name:<34} {result['seconds']:>4}s  {result['tail'][-1] if result['tail'] else ''}", flush=True)
    return result


def passed_line(out):
    m = re.search(r'(\d+)/(\d+) passed', out)
    return {'passed': int(m[1]), 'total': int(m[2]), 'ok': m[1] == m[2]} if m else {}


def playwright_counts(out):
    passed = sum(int(n) for n in re.findall(r'(\d+) passed', out))
    failed = sum(int(n) for n in re.findall(r'(\d+) failed', out))
    skipped = sum(int(n) for n in re.findall(r'(\d+) skipped', out))
    return {'passed': passed, 'failed': failed, 'skipped': skipped, 'ok': failed == 0 and skipped == 0 and passed > 0}


def mismatches(out):
    m = re.search(r'(\d+) mismatches', out)
    return {'mismatches': int(m[1]), 'ok': m[1] == '0'} if m else {}


def crawl_summary(out):
    m = re.search(r'(\d+) page loads, (\d+) clean, (\d+) failing API groups, (\d+) exception kinds, (\d+) blank', out)
    if not m:
        return {}
    loads, clean, groups, exc, blank = map(int, m.groups())
    return {'loads': loads, 'clean': clean, 'failing_api_groups': groups, 'exceptions': exc, 'blank': blank,
            'ok': exc == 0 and blank == 0}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--with-golden-api', action='store_true')
    ap.add_argument('--tag', default=time.strftime('%m%d-%H%M'))
    a = ap.parse_args()
    results = []
    results.append(run('crawl (216 page loads)', ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'crawl.spec.ts'],
                       cwd=ROOT / 'web', env={'E2E_WORKERS': '6'}))
    results.append(run('crawl summary', ['python3', 'tools/acceptance/crawl-summary.py'], parse=crawl_summary))
    for spec, env in SPECS.items():
        results.append(run(spec, ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', spec], cwd=ROOT / 'web',
                           env=env, timeout=900, parse=playwright_counts))
        proposal = LOCAL / 'e2e/golden/silence-proposal.txt'
        if spec == 'golden-silence-ui.spec.ts' and results[-1]['ok'] and proposal.exists():
            # The proposal just made in the drawer: two approvers approve it with passkeys, the executor writes it.
            results.append(run('golden-approval.spec.ts', ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts',
                                                          'golden-approval.spec.ts'], cwd=ROOT / 'web', timeout=900,
                               env={'GOLDEN_PROPOSAL': proposal.read_text().strip()}, parse=playwright_counts))
    for instance in ('prod', 'nonprod'):
        results.append(run(f'permissions {instance}', ['python3', 'tools/acceptance/permissions.py', instance,
                                                       '--json', str(LOCAL / f'e2e/permissions-{instance}.json')], parse=mismatches))
    # The trace page opens on the Errors view, so the crawl never loads All; read every view directly (PA-87).
    results.append(run('trace views', ['python3', 'tools/acceptance/trace_views.py'], parse=passed_line))
    # Lab credentials expire on their own (the K8s reader did on 2026-10-11 and nothing noticed for hours).
    results.append(run('credentials', ['python3', 'tools/acceptance/credentials.py'], parse=passed_line))
    for script in ('environments', 'inputs', 'auth'):
        results.append(run(script, ['python3', f'tools/acceptance/{script}.py'], parse=passed_line))
    if a.with_golden_api:
        for path in ('canvas', 'conversation', 'settings', 'agents'):
            results.append(run(f'golden {path}', ['python3', 'tools/acceptance/golden.py', path]))
    ok = sum(r['ok'] for r in results)
    print(f'{ok}/{len(results)} checks passed')
    out = LOCAL / f'e2e/regress-{a.tag}.json'
    out.write_text(json.dumps({'tag': a.tag, 'at': time.strftime('%Y-%m-%d %H:%M:%S'),
                               'commit': subprocess.run(['git', 'rev-parse', '--short', 'HEAD'], cwd=ROOT, capture_output=True, text=True).stdout.strip(),
                               'results': results}, ensure_ascii=False, indent=1))
    print(f'report: {out}')


if __name__ == '__main__':
    main()
