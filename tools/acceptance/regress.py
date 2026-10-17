#!/usr/bin/env python3
"""One regression pass over the acceptance instances; prints and saves a summary.

  python3 tools/acceptance/regress.py [--with-golden-api] [--with-roles] [--with-details] [--tag NAME]

Runs, in order: the full page crawl (both instances × zh/en × light/dark), every Playwright
golden spec, and the API checks (permissions on both instances, environments, inputs, auth).
--with-golden-api adds tools/acceptance/golden.py canvas/conversation/settings/agents (real
model calls, a few cents). --with-roles adds the viewer and operator crawls (zh dark, T4) after the
permission matrix they are judged against; --with-details adds the detail and tab crawl (T5).
Drills, fault injection and the knowledge path are separate (they touch the lab and take minutes
each). Results: .local/acceptance/e2e/regress-<tag>.json, which records the deployed binary's
vcs.revision and vcs.modified (`go version -m .local/acceptance/bin/aiops`), not git HEAD.

The crawl passes only when no API failure group is left after the exemptions in
.local/acceptance/e2e/crawl-exemptions.json (kept by the main session; see crawl-summary.py for the
entry format, e.g. D45's GET /services 503), no page throws or renders blank, and no permission
gate is wrong. GOLDEN_INVESTIGATION is looked up through the API when the environment does not set it:
an open investigation of checkout-api visible to the operator, created when there is none.
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
sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import call  # noqa: E402
from round import artifact  # noqa: E402

BIN = LOCAL / 'bin/aiops'
TARGET_SERVICE = 'checkout-api'  # the silence drawer and the alert it silences belong to this service
# golden-approval needs a pending proposal id (GOLDEN_PROPOSAL); golden-silence-ui proposes, approves
# with passkeys, executes and expires a silence end to end, so it covers approval in the regression.
# phase1-fixes: route-pinned checks for the WAN-434 phase 1 fixes (no writes). account-security: TOTP, passkey,
# photo and preferences on a throwaway account (tools/acceptance/accounts.py prod mfa:viewer); it skips without one.
SPECS = {'golden-conversation.spec.ts': {}, 'golden-invite.spec.ts': {}, 'phase1-fixes.spec.ts': {}, 'account-security.spec.ts': {},
         'golden-silence-ui.spec.ts': None}
# Phase 2 rounds add their fix checks here (docs/plans/2026-10-17-convergence-round.md §7); run once the file exists.
if (ROOT / 'web/e2e/phase2-fixes.spec.ts').exists():
    SPECS = {**{k: v for k, v in SPECS.items() if k != 'golden-silence-ui.spec.ts'}, 'phase2-fixes.spec.ts': {}, 'golden-silence-ui.spec.ts': None}


def golden_investigation():
    """An open investigation of the target service that the operator can see, found or created through the API."""
    import base64
    from permissions import login
    if os.environ.get('GOLDEN_INVESTIGATION'):
        return os.environ['GOLDEN_INVESTIGATION'], 'environment'
    accounts = json.loads((LOCAL / 'prod/secrets.json').read_text())['accounts']
    cookie = login('prod', accounts['operator'])
    status, services = call('prod', 'GET', '/services', cookie=cookie)
    if status != 200:
        raise RuntimeError(f'GET /services answered {status}')
    ids = [s['id'] for s in services.get('items', []) if s.get('name') == TARGET_SERVICE]
    if not ids:
        raise RuntimeError(f'{TARGET_SERVICE} is not in the service catalog')
    app = base64.urlsafe_b64decode(ids[0] + '=' * (-len(ids[0]) % 4)).decode().split('\x00')[0]
    status, listed = call('prod', 'GET', '/canvases?kind=investigation&status=active&page_size=100', cookie=cookie)
    if status != 200:
        raise RuntimeError(f'GET /canvases answered {status}')
    open_ones = [c for c in listed.get('items', []) if c.get('investigation_id') and c.get('services', [None])[:1] == [app]
                 and c.get('status') not in ('done', 'resolved', 'closed')]
    if open_ones:
        return max(open_ones, key=lambda c: c.get('updated_at', ''))['investigation_id'], 'found'
    status, created = call('prod', 'POST', '/canvases', {'title': '验收：静默演练目标', 'kind': 'investigation', 'template_id': None,
                                                         'goal': f'{TARGET_SERVICE} 的静默演练（regress.py 创建）', 'services': [app], 'ai_draft': False},
                           cookie=cookie)
    if status not in (200, 201) or not isinstance(created, dict):
        raise RuntimeError(f'POST /canvases answered {status}')
    return created.get('investigation_id') or created['id'], 'created'


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
    m = re.search(r'(\d+)/(\d+) (?:checks )?passed', out)
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
    """crawl-summary.py counts only the groups left after exemptions and the role's expected denials."""
    m = re.search(r'(\d+) page loads, (\d+) clean, (\d+) failing API groups, (\d+) exception kinds, (\d+) blank'
                  r'(?:, (\d+) exempt, (\d+) expected-denied, (\d+) gate problems)?(?:, (\d+) sample markers)?', out)
    if not m:
        return {}
    loads, clean, groups, exc, blank = map(int, m.groups()[:5])
    exempt, expected, gates, samples = (int(x or 0) for x in m.groups()[5:])
    return {'loads': loads, 'clean': clean, 'failing_api_groups': groups, 'exceptions': exc, 'blank': blank,
            'exempt_groups': exempt, 'expected_denied_groups': expected, 'gate_problems': gates, 'sample_markers': samples,
            'ok': exc == 0 and blank == 0 and groups == 0 and gates == 0 and samples == 0}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--with-golden-api', action='store_true')
    ap.add_argument('--with-roles', action='store_true', help='viewer and operator crawls (zh dark), judged against the permission matrix')
    ap.add_argument('--with-details', action='store_true', help='detail pages and their tabs (web/e2e/detail-pages.spec.ts)')
    ap.add_argument('--tag', default=time.strftime('%m%d-%H%M'))
    a = ap.parse_args()
    art = artifact(BIN)
    if art.get('modified') is not False:
        print(f"WARNING: deployed binary revision {art.get('revision')} modified={art.get('modified')}: this run is not on a clean build.", file=sys.stderr)
    results = []
    results.append(run('crawl (216 page loads)', ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'crawl.spec.ts'],
                       cwd=ROOT / 'web', env={'E2E_WORKERS': '3'}))
    results.append(run('crawl summary', ['python3', 'tools/acceptance/crawl-summary.py'], parse=crawl_summary))
    for spec, env in SPECS.items():
        if env is None:  # golden-silence-ui: look the investigation up now, after the crawl
            try:
                investigation, how = golden_investigation()
            except Exception as e:  # noqa: BLE001
                results.append({'name': spec, 'exit': -1, 'seconds': 0, 'ok': False, 'tail': [f'no investigation for the silence spec: {e}']})
                print(f"FAIL  {spec:<34}    0s  {results[-1]['tail'][0]}", flush=True)
                continue
            print(f'      {spec}: GOLDEN_INVESTIGATION {investigation} ({how})', flush=True)
            env = {'GOLDEN_INVESTIGATION': investigation}
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
    if a.with_roles:
        for role in ('viewer', 'operator'):
            crawl_env = {'E2E_WORKERS': '3', 'CRAWL_ROLE': role}
            results.append(run(f'crawl {role} (108 page loads)', ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'crawl.spec.ts'],
                               cwd=ROOT / 'web', env=crawl_env))
            results.append(run(f'crawl summary {role}', ['python3', 'tools/acceptance/crawl-summary.py'], env={'CRAWL_ROLE': role}, parse=crawl_summary))
    if a.with_details:
        # Detail page IDs change between runs, so a page left over from an earlier run is never overwritten and
        # the summary would judge it again: start from an empty output directory.
        for old in (LOCAL / 'e2e/detail-crawl').glob('*.json'):
            old.unlink()
        results.append(run('detail pages and tabs', ['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'detail-pages.spec.ts'],
                           cwd=ROOT / 'web', env={'E2E_WORKERS': '3'}, timeout=2400))
        results.append(run('detail summary', ['python3', 'tools/acceptance/crawl-summary.py'], env={'CRAWL_OUT': 'e2e/detail-crawl'}, parse=crawl_summary))
    # The trace page opens on the Errors view, so the crawl never loads All; read every view directly (PA-87).
    results.append(run('trace views', ['python3', 'tools/acceptance/trace_views.py'], parse=passed_line))
    # Lab credentials expire on their own (the K8s reader did on 2026-10-11 and nothing noticed for hours).
    results.append(run('credentials', ['python3', 'tools/acceptance/credentials.py'], parse=passed_line))
    for script in ('environments', 'inputs', 'auth'):
        results.append(run(script, ['python3', f'tools/acceptance/{script}.py'], parse=passed_line))
    # D11: enable and disable one rule through the GitOps executor on nonprod (its alert is dropped by
    # design), so every regression exercises the executor, GitLab and the rule loading path.
    results.append(run('alert rule loop nonprod', ['python3', 'tools/acceptance/alert_rule_loop.py', 'nonprod'], parse=passed_line))
    # D11 §9.1 step 3: the product's GitLab deployments, pipelines and MRs equal GitLab's, item by item.
    results.append(run('gitlab changes compare', ['python3', 'tools/acceptance/gitlab_compare.py', 'all'], parse=passed_line))
    if a.with_golden_api:
        for path in ('canvas', 'conversation', 'settings', 'agents'):
            results.append(run(f'golden {path}', ['python3', 'tools/acceptance/golden.py', path]))
    ok = sum(r['ok'] for r in results)
    print(f'{ok}/{len(results)} checks passed')
    out = LOCAL / f'e2e/regress-{a.tag}.json'
    out.write_text(json.dumps({'tag': a.tag, 'at': time.strftime('%Y-%m-%d %H:%M:%S'),
                               'commit': (art.get('revision') or '')[:8], 'artifact': art,
                               'results': results}, ensure_ascii=False, indent=1))
    print(f'report: {out}')


if __name__ == '__main__':
    main()
