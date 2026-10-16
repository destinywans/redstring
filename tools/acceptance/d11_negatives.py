#!/usr/bin/env python3
"""D11 counter-examples (design §9.1 step 5 and §9.2 step 4).

  python3 tools/acceptance/d11_negatives.py changes   # prod: GitLab paused, bad Vault reference, non-GitLab change
  python3 tools/acceptance/d11_negatives.py rules     # nonprod: approvals, stale base, rules sync stopped, GitLab paused mid-write

changes:
  * GitLab paused (docker pause of the lab GitLab): the changes view answers 503 source_unavailable,
    the incident's changes keep the cluster revisions with gitlab coverage unavailable, and the AI's
    read_service_changes reports the source unavailable instead of "no deployment"; recovers on unpause.
  * The GitLab binding points at a missing Vault key: a credential error, not an empty list; restored.
  * A cluster change outside GitLab (aiops-test lab-faults bad-release): the revision is listed with no
    GitLab pipeline.
rules:
  * proposals X and Y share one base revision. X: self-approval and an API token are refused, one vote
    leaves it pending, the second vote enables it. Y, approved after X moved main: the executor stops
    without writing main and without a merge request.
  * rules sync stopped: an enable reaches load_timeout after the readback window; with the sync back,
    POST /executions/{id}/recheck records it enabled.
  * GitLab paused right after approval: the execution converges to enabled with exactly one MR.
  * every rule enabled here is disabled again.
Writes .local/acceptance/e2e/drills/d11-negatives-<part>.json. Lab containers are only paused or
stopped and started again; nothing else in the lab changes.
"""
import argparse
import base64
import json
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, ORIGINS, call  # noqa: E402
from drill import LAB, OUT  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
GITLAB_CONTAINER = 'aiops-delivery-gitlab-1'
RULES_SYNC = 'aiops-test-rules-sync-1'
GITLAB_API = 'http://127.0.0.1:28080/api/v4'
PROM = 'http://127.0.0.1:19090'
results = []


def check(label, ok, detail=''):
    results.append({'check': label, 'ok': bool(ok), 'detail': str(detail)[:400]})
    print(f"{'PASS' if ok else 'FAIL'}  {label}  {str(detail)[:240]}", flush=True)
    return ok


def docker(*args):
    r = subprocess.run(['docker', *args], capture_output=True, text=True, timeout=120)
    return r.returncode == 0, (r.stdout + r.stderr).strip()[-200:]


def gitlab_up(timeout=600):
    started = time.time()
    while time.time() - started < timeout:
        try:
            with urllib.request.urlopen('http://127.0.0.1:28080/users/sign_in', timeout=5) as r:
                if r.status == 200:
                    return True
        except OSError:
            pass
        time.sleep(5)
    return False


def bearer(instance, env, method, path, token, body=None):
    """Status of one API-token request (golden.bearer, for either instance)."""
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    headers = {'Authorization': f'Bearer {token}', 'X-AIOps-Environment': env, 'Origin': ORIGINS[instance],
               'Content-Type': 'application/json', 'Idempotency-Key': secrets.token_hex(16)}
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1' + path, data=json.dumps(body or {}).encode(), method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
            return r.status
    except urllib.error.HTTPError as e:
        return e.code


def approve(instance, env, pid, approvers='approver1,approver2'):
    r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-approval.spec.ts'], cwd=ROOT / 'web',
                       env={**__import__('os').environ, 'GOLDEN_PROPOSAL': pid, 'GOLDEN_INSTANCE': instance, 'GOLDEN_ENV': env,
                            'GOLDEN_APPROVERS': approvers}, capture_output=True, text=True, timeout=900)
    return r.returncode == 0 and ' passed' in r.stdout, ' | '.join((r.stdout + r.stderr).strip().splitlines()[-3:])


# ---------------------------------------------------------------- changes (prod)

def changes_part():
    op = lambda m, p, b=None, key=None, role='operator': call('prod', m, p, b, env='prod', role=role, idempotency_key=key)  # noqa: E731
    sid = json.loads((LOCAL / 'rollback-services.json').read_text())['prod']['checkout-api']
    q = urllib.parse.urlencode({'service_id': sid, 'range': '24h', 'sources': 'gitlab'})
    status, base = op('GET', f'/changes?{q}')
    check('baseline: changes view 200 with gitlab complete', status == 200 and (base.get('coverage') or {}).get('gitlab') == 'complete', base.get('coverage'))
    _, incidents = op('GET', '/incidents')
    # Only checkout-api has a GitLab project and a workload binding; order-api raises the same alert.
    app_id = base64.urlsafe_b64decode(sid + '=' * (-len(sid) % 4)).decode().split('\x00')[0]
    # List items carry no application, so read each LabHigh5xxRate incident until checkout-api's.
    incident = next((i['id'] for i in incidents.get('items') or [] if i.get('title') == 'LabHigh5xxRate'
                     and (op('GET', f"/incidents/{i['id']}")[1] or {}).get('application_id') == app_id), None)

    # GitLab paused.
    ok, out = docker('pause', GITLAB_CONTAINER)
    check('GitLab paused', ok, out)
    try:
        started = time.time()
        status, body = op('GET', f'/changes?{q}')
        code = ((body or {}).get('error') or {}).get('code')
        check('changes view: 503 source_unavailable while GitLab is down', status == 503 and code == 'source_unavailable',
              f'{status} {code} in {round(time.time() - started)}s')
        if incident:
            status, around = op('GET', f'/incidents/{incident}/changes?before=23h&after=60m')
            cov = (around or {}).get('coverage') or {}
            kinds = {i.get('kind') for i in (around or {}).get('items') or []}
            check('incident changes: gitlab unavailable, cluster revisions still listed',
                  status == 200 and cov.get('gitlab') == 'unavailable' and 'kubernetes_revision' in kinds and 'gitlab_deployment' not in kinds,
                  f'{status} {cov} {sorted(k for k in kinds if k)}')
        question = 'checkout-api 最近 2 小时有哪些 GitLab 部署？只用只读工具回答。'
        status, conv = op('POST', '/conversations', {'text': question})
        cid = (conv or {}).get('id')
        if cid:
            op('POST', f'/conversations/{cid}/messages', {'text': question})
        calls, answer, last = [], '', {}
        started = time.time()
        while cid and time.time() - started < 300:
            _, detail = op('GET', f'/conversations/{cid}')
            last = (detail.get('messages') or [{}])[-1]
            if last.get('role') == 'assistant' and last.get('state') in ('completed', 'failed'):
                break
            time.sleep(5)
        for part in last.get('parts', []):
            if part.get('type') == 'tool_calls':
                calls += [{'tool': c.get('tool'), 'status': c.get('status')} for c in part.get('calls', [])]
        answer = ''.join(p.get('markdown', '') for p in last.get('parts', []) if p.get('type') == 'text')
        # The tool answers with partial evidence (coverage.gitlab unavailable, cluster revisions kept)
        # rather than failing, so the call is ok; the answer must say the source is unavailable.
        changes_calls = [c for c in calls if c['tool'] == 'read_service_changes']
        check('AI read the changes while GitLab was down', changes_calls, f'{changes_calls} conversation {cid}')
        check('AI says the GitLab source is unavailable, not that nothing was deployed',
              answer and ('不可用' in answer or 'unavailable' in answer.lower()), answer[:300])
    finally:
        ok, out = docker('unpause', GITLAB_CONTAINER)
        check('GitLab unpaused', ok, out)
    started = time.time()
    while time.time() - started < 300:
        status, body = op('GET', f'/changes?{q}')
        if status == 200 and (body.get('coverage') or {}).get('gitlab') == 'complete':
            break
        time.sleep(10)
    check('changes view recovers after GitLab is back', status == 200 and (body.get('coverage') or {}).get('gitlab') == 'complete',
          f'{status} after {round(time.time() - started)}s')

    # Missing Vault key for the GitLab binding.
    path = LOCAL / 'prod/extra.env'
    original = path.read_text()
    key = 'AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS='
    lines = original.splitlines()
    i = next(n for n, line in enumerate(lines) if line.startswith(key))
    bindings = json.loads(lines[i][len(key):])
    changed = 0
    for b in bindings:
        if b.get('provider') == 'gitlab':
            b['credential_ref'] = b['credential_ref'].replace('#token', '#token_missing_d11')
            changed += 1
    lines[i] = key + json.dumps(bindings, separators=(',', ':'))
    try:
        path.write_text('\n'.join(lines) + '\n')
        subprocess.run(['python3', 'tools/acceptance/acceptance.py', 'restart', 'prod'], cwd=ROOT, capture_output=True, timeout=300)
        status, body = op('GET', f'/changes?{q}')
        err = (body or {}).get('error') or {}
        check('bad Vault reference: a credential error, not an empty list', changed and status >= 400 and 'credential' in json.dumps(err),
              f'{status} {err} (bindings changed {changed})')
    finally:
        path.write_text(original)
        path.chmod(0o600)
        subprocess.run(['python3', 'tools/acceptance/acceptance.py', 'restart', 'prod'], cwd=ROOT, capture_output=True, timeout=300)
    status, body = op('GET', f'/changes?{q}')
    check('restored binding: changes view 200 again', status == 200 and (body.get('coverage') or {}).get('gitlab') == 'complete', status)

    # A cluster change outside GitLab.
    before = {r['revision'] for r in body.get('kubernetes_revisions') or []}
    on = subprocess.run(['python3', 'scripts/lab-faults.py', 'drill', 'bad-release', 'on', '--app', 'checkout-api', '--ratio', '0.05'],
                        cwd=LAB, capture_output=True, text=True, timeout=600)
    try:
        status, body = op('GET', f'/changes?{q}')
        new = [r for r in body.get('kubernetes_revisions') or [] if r['revision'] not in before]
        check('a change outside GitLab is listed with no GitLab pipeline', new and all(r.get('gitlab_pipeline_id') is None for r in new),
              json.dumps(new, ensure_ascii=False)[:300] + ' | ' + ' '.join((on.stdout + on.stderr).strip().splitlines()[-1:]))
    finally:
        off = subprocess.run(['python3', 'scripts/lab-faults.py', 'drill', 'bad-release', 'off', '--app', 'checkout-api'],
                             cwd=LAB, capture_output=True, text=True, timeout=600)
        print('  bad-release off:', ' '.join((off.stdout + off.stderr).strip().splitlines()[-1:]), flush=True)


# ---------------------------------------------------------------- rules (nonprod)

def rules_part():
    inst, env = 'nonprod', 'test'
    op = lambda m, p, b=None, key=None, role='operator': call(inst, m, p, b, env=env, role=role, idempotency_key=key)  # noqa: E731
    binding = json.loads((LOCAL / inst / 'alert-rules.json').read_text())['bindings'][0]
    app, group = binding['application_id'], binding['group']
    stamp = time.strftime('%m%d%H%M')
    _, created = op('POST', '/canvases', {'title': f'验收：D11 告警规则反例 {stamp}', 'kind': 'investigation', 'template_id': None,
                                          'goal': 'D11 告警规则反例与对账', 'services': [app], 'ai_draft': False})
    inv = created['id']
    enabled = []

    def propose(suffix):
        name = f'RedstringNegative{stamp}{suffix}'
        body = {'application_id': app, 'environment_id': env, 'group': group, 'name': name, 'for': '1m',
                'expr': 'sum by (cluster) (rate(lab_http_requests_total{application="checkout-api"}[2m])) > 0',
                'labels': {'severity': 'P3'}, 'annotations': {'summary': f'D11 反例 {suffix}，验收后停用'}, 'mode': 'enabled', 'investigation_id': inv}
        status, r = op('POST', '/alert-rules', body, key=secrets.token_hex(16))
        return name, (r or {}).get('proposal_id'), status

    def execution(pid, done, timeout):
        started, eid, rule = time.time(), None, {}
        while time.time() - started < timeout:
            _, detail = op('GET', f'/proposals/{pid}')
            eid = ((detail or {}).get('execution') or {}).get('execution_id') or eid
            if eid:
                _, ex = op('GET', f'/executions/{eid}')
                rule = (ex or {}).get('alert_rule') or {}
                if done(rule):
                    break
            time.sleep(5)
        return eid, rule

    def steps(rule):
        return ' → '.join(s.get('state') for s in rule.get('steps') or [])

    def mrs_for(eid):
        q = urllib.parse.urlencode({'source_branch': f'aiops-alert-rule-enable/{eid}', 'state': 'all'})
        with urllib.request.urlopen(f'{GITLAB_API}/projects/6/merge_requests?{q}', timeout=10) as r:
            return json.load(r)

    def main_head():
        with urllib.request.urlopen(f'{GITLAB_API}/projects/6/repository/branches/main', timeout=10) as r:
            return json.load(r)['commit']['id']

    # Approvals and a stale base.
    x, px, sx = propose('X')
    y, py, sy = propose('Y')
    check('two enable proposals on one base (202)', sx == 202 and sy == 202 and px and py, f'{sx} {sy}')
    status, r = op('POST', f'/proposals/{px}/approve/options', {'confirm': True}, key=secrets.token_hex(12))
    check('the proposer cannot approve', status in (403, 409), f'{status} {(r or {}).get("error")}')
    status, issued = call(inst, 'POST', '/api-tokens', {'name': 'd11-negatives', 'scopes': ['connectors:view'], 'ttl': 3600},
                          env=env, idempotency_key=secrets.token_hex(12))
    token = (issued or {}).get('secret_shown_once')
    code = bearer(inst, env, 'POST', f'/proposals/{px}/approve/options', token, {'confirm': True}) if token else None
    check('an API token cannot approve', code in (401, 403), str(code))
    if (issued or {}).get('id'):
        call(inst, 'DELETE', f"/api-tokens/{issued['id']}", env=env)
    ok, tail = approve(inst, env, px, 'approver1')
    _, detail = op('GET', f'/proposals/{px}')
    check('one vote leaves the proposal pending, nothing executes', ok and detail.get('status') == 'pending' and not (detail.get('execution') or {}).get('execution_id'),
          f"{detail.get('status')} {tail}")
    ok, tail = approve(inst, env, px, 'approver2')
    ex_x, rule = execution(px, lambda r: r.get('state') in ('enabled', 'failed', 'needs_attention'), 600)
    if check('the second vote enables X', ok and rule.get('state') == 'enabled', f"{rule.get('state')} {steps(rule)}"):
        enabled.append(x)
    head = main_head()
    ok, tail = approve(inst, env, py)
    ex_y, rule = execution(py, lambda r: r.get('state') not in (None, '', 'reserved', 'committing', 'exporting'), 300)
    check('Y after main moved: the executor stops for review', rule.get('state') in ('needs_attention', 'stale_remote', 'pending_review', 'failed'),
          f"{rule.get('state')} {rule.get('failure')} {steps(rule)}")
    check('Y wrote nothing: main unchanged and no merge request', main_head() == head and ex_y and not mrs_for(ex_y), f'main {head[:8]} eid {ex_y}')

    # Rules sync stopped: load timeout, then recheck.
    ok, out = docker('stop', RULES_SYNC)
    check('rules sync stopped', ok, out)
    try:
        w, pw, sw = propose('W')
        ok, tail = approve(inst, env, pw)
        ex_w, rule = execution(pw, lambda r: r.get('state') in ('enabled', 'failed', 'needs_attention'), 600)
        check('without the sync the load times out (needs_attention load_timeout)',
              (rule.get('failure') or {}).get('code') == 'load_timeout' and rule.get('state') == 'needs_attention',
              f"{rule.get('state')} {rule.get('failure')} readback={json.dumps(rule.get('readback'))[:160]}")
    finally:
        ok, out = docker('start', RULES_SYNC)
        check('rules sync started again', ok, out)
    time.sleep(45)
    reload = (rule.get('readback') or {}).get('reload')
    check('the load timeout names Prometheus reloads (PA-90)', reload and 'reload' in json.dumps(rule.get('failure') or {}),
          f"reload={json.dumps(reload)[:160]} failure={json.dumps(rule.get('failure'), ensure_ascii=False)[:160]}")
    status, r = op('POST', f'/executions/{ex_w}/recheck', {}, key=secrets.token_hex(12))
    _, rule = execution(pw, lambda r: r.get('state') == 'enabled', 120)
    if check('recheck records W enabled once loaded', status == 200 and rule.get('state') == 'enabled', f"{status} {rule.get('state')} {steps(rule)}"):
        enabled.append(w)
    names = [x.get('state') for x in rule.get('steps') or []]
    check('the timeline keeps the timeout and the recheck (PA-90)', 'needs_attention' in names and 'recheck' in names,
          ' → '.join(f"{x.get('state')}({x.get('reason')})" if x.get('reason') else str(x.get('state')) for x in rule.get('steps') or []))

    # GitLab paused right after approval.
    v, pv, _ = propose('V')
    ok, tail = approve(inst, env, pv)
    paused, out = docker('pause', GITLAB_CONTAINER)
    time.sleep(45)
    docker('unpause', GITLAB_CONTAINER)
    gitlab_up()
    ex_v, rule = execution(pv, lambda r: r.get('state') in ('enabled', 'failed', 'needs_attention'), 900)
    check('GitLab paused mid-write: the execution converges to enabled', paused and rule.get('state') == 'enabled', f"{rule.get('state')} {rule.get('failure')} {steps(rule)}")
    check('exactly one merge request for that execution', ex_v and len(mrs_for(ex_v)) == 1, f'{len(mrs_for(ex_v)) if ex_v else None} MR')
    if rule.get('state') == 'enabled':
        enabled.append(v)

    # Clean up.
    _, listing = op('GET', '/alert-rules')
    for name in enabled:
        row = next((x for x in (listing or {}).get('items') or [] if x.get('name') == name), None)
        if not row:
            check(f'cleanup: {name} listed', False, '')
            continue
        status, r = op('POST', f"/alert-rules/{row['id']}/disable", {'investigation_id': inv}, key=secrets.token_hex(16))
        dp = (r or {}).get('proposal_id')
        approve(inst, env, dp)
        _, rule = execution(dp, lambda r: r.get('state') in ('disabled', 'failed', 'needs_attention'), 600)
        check(f'cleanup: {name} disabled', rule.get('state') == 'disabled', f"{rule.get('state')} {steps(rule)}")
    with urllib.request.urlopen(f'{PROM}/api/v1/rules', timeout=10) as r:
        left = [x['name'] for g in json.load(r)['data']['groups'] if g['name'] == group for x in g['rules'] if stamp in x['name']]
    check('no negative-test rule left in Prometheus', not left, left)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('part', choices=['changes', 'rules'])
    a = ap.parse_args()
    t0 = time.time()
    try:
        changes_part() if a.part == 'changes' else rules_part()
    finally:
        out = OUT / f'd11-negatives-{a.part}.json'
        out.write_text(json.dumps({'part': a.part, 'seconds': round(time.time() - t0), 'results': results}, ensure_ascii=False, indent=1))
        passed = sum(r['ok'] for r in results)
        print(f'{passed}/{len(results)} checks passed  report: {out}')
    sys.exit(0 if all(r['ok'] for r in results) else 1)


if __name__ == '__main__':
    main()
