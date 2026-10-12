#!/usr/bin/env python3
"""Enable an alert rule through the product and disable it again (D11 closed loop, design §9.2).

  python3 tools/acceptance/alert_rule_loop.py <instance> [--keep] [--timeout 600]

1. opens an investigation for checkout-api and proposes a rule with mode=enabled (422
   idempotency_key_required without an Idempotency-Key, the same proposal on a replay);
2. two approvers approve with passkeys (web/e2e/golden-approval.spec.ts);
3. follows the execution to enabled (committing → merging → merged → loading → enabled);
4. reads the rule back from the lab Prometheus (/api/v1/rules) and from GET /alert-rules;
   on prod also waits for the rule's alert to reach the product (it fires at any traffic);
5. proposes disabling it, approves, follows the execution to disabled, and checks the rule is gone.
--keep stops after step 4; --disable NAME runs only step 5 for a rule left enabled. The rule groups by cluster so the lab relabelling routes prod alerts to
the prod instance (D33); a test rule's alert is discarded by Alertmanager by design.
Writes .local/acceptance/e2e/drills/alert-rule-<instance>.json.
"""
import argparse
import json
import secrets
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
ENV = {'prod': 'prod', 'nonprod': 'test'}
PROM = 'http://127.0.0.1:19090'
GITLAB = 'http://127.0.0.1:28080/api/v4/projects/6'
EXPR = 'sum by (cluster) (rate(lab_http_requests_total{application="checkout-api"}[2m])) > 0'
TERMINAL = ('failed', 'needs_attention', 'stale_remote', 'pending_review', 'rejected')
results = []


def check(label, ok, detail=''):
    results.append({'check': label, 'ok': bool(ok), 'detail': str(detail)[:300]})
    print(f"{'PASS' if ok else 'FAIL'}  {label}  {str(detail)[:200]}", flush=True)
    return ok


def prom_rules(group):
    with urllib.request.urlopen(f'{PROM}/api/v1/rules', timeout=10) as r:
        groups = json.load(r)['data']['groups']
    return next((g for g in groups if g['name'] == group), None)


def approve(instance, env, pid):
    r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-approval.spec.ts'], cwd=ROOT / 'web',
                       env={**__import__('os').environ, 'GOLDEN_PROPOSAL': pid, 'GOLDEN_INSTANCE': instance, 'GOLDEN_ENV': env},
                       capture_output=True, text=True, timeout=900)
    return r.returncode == 0, ' | '.join((r.stdout + r.stderr).strip().splitlines()[-3:])


def follow(op, pid, done, timeout, t0):
    eid, seen, ex, started = None, [], {}, time.time()
    while time.time() - started < timeout:
        _, detail = op('GET', f'/proposals/{pid}')
        eid = ((detail or {}).get('execution') or {}).get('execution_id') or eid
        if eid:
            _, ex = op('GET', f'/executions/{eid}')
            rule = (ex or {}).get('alert_rule') or {}
            st = rule.get('state')
            if st and (not seen or seen[-1][0] != st):
                seen.append((st, round(time.time() - t0)))
                print(f"  +{seen[-1][1]:>4}s execution {st} {rule.get('failure') or ''}", flush=True)
            if st in done or st in TERMINAL:
                break
        time.sleep(5)
    # The executor's own step history (with the retries it took) is the record; polling only samples it.
    steps = [(x.get('state'), x.get('at')) for x in ((ex or {}).get('alert_rule') or {}).get('steps') or []]
    return eid, steps or seen, (ex or {}).get('alert_rule') or {}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('instance', choices=list(ENV))
    ap.add_argument('--keep', action='store_true')
    ap.add_argument('--disable', metavar='NAME', help='only propose and approve disabling this enabled rule')
    ap.add_argument('--timeout', type=int, default=600)
    a = ap.parse_args()
    inst, env = a.instance, ENV[a.instance]
    op = lambda m, p, b=None, key=None, role='operator': call(inst, m, p, b, env=env, role=role, idempotency_key=key)  # noqa: E731
    binding = json.loads((LOCAL / inst / 'alert-rules.json').read_text())['bindings'][0]
    app, group = binding['application_id'], binding['group']
    name = a.disable or f'RedstringAcceptance{time.strftime("%m%d%H%M")}'
    log = {'instance': inst, 'name': name, 'group': group, 't0': time.time()}
    t0 = log['t0']

    verb = '停用' if a.disable else '启用'
    status, created = op('POST', '/canvases', {'title': f'验收：{verb}告警规则 {name}', 'kind': 'investigation', 'template_id': None,
                                               'goal': '验收 D11 告警规则启用与停用闭环', 'services': [app], 'ai_draft': False})
    inv = (created or {}).get('id')
    check('investigation opened', status < 300 and inv, f'{status} {inv}')
    if a.disable:
        _, listing = op('GET', '/alert-rules')
        row = next((x for x in (listing or {}).get('items') or [] if x.get('name') == name), None)
        check('the rule is in the product rule list', row, json.dumps(row, ensure_ascii=False)[:200])
        if row:
            disable(op, inst, env, row, inv, group, a.timeout, t0, log)
        finish(inst, log)
        return
    body = {'application_id': app, 'environment_id': env, 'group': group, 'name': name, 'expr': EXPR, 'for': '1m',
            'labels': {'severity': 'P3'}, 'annotations': {'summary': 'D11 验收：checkout-api 有流量即触发，验收后停用'},
            'mode': 'enabled', 'investigation_id': inv}
    status, r = op('POST', '/alert-rules', body, key=False)
    check('enable without Idempotency-Key is refused (422 idempotency_key_required)',
          status == 422 and ((r or {}).get('error') or {}).get('code') == 'idempotency_key_required', f'{status} {(r or {}).get("error")}')
    key = secrets.token_hex(16)
    status, r = op('POST', '/alert-rules', body, key=key)
    pid = (r or {}).get('proposal_id') or (r or {}).get('id')
    check('enable proposal accepted (202 pending_approval)', status == 202 and pid, f'{status} {r}')
    status, again = op('POST', '/alert-rules', body, key=key)
    check('replay with the same key returns the same proposal', (again or {}).get('proposal_id', (again or {}).get('id')) == pid, f'{status}')
    _, prop = op('GET', f'/proposals/{pid}')
    check('review content carries the scope labels (D32)', 'Scope labels: environment=' in json.dumps(prop, ensure_ascii=False),
          json.dumps(prop, ensure_ascii=False)[:200])
    _, listing = op('GET', '/alert-rules')
    pending = [x for x in (listing or {}).get('pending') or [] if pid in json.dumps(x)]
    check('the rule list shows the enable as pending (§9.2 3.6)', pending, json.dumps(pending[:1], ensure_ascii=False)[:200])
    ok, tail = approve(inst, env, pid)
    check('two approvers approve with passkeys', ok, tail)
    approved = time.time()
    eid, seen, ex = follow(op, pid, ('enabled',), a.timeout, t0)
    log.update(enable_execution=eid, enable_states=seen, enable=ex)
    check('execution reaches enabled', ex.get('state') == 'enabled', f"{ex.get('state')} {ex.get('failure')}")
    states = [str(s) for s, _ in seen]
    check('states pass committing → merging → merged → loading → enabled',
          all(s in states for s in ('committing', 'merging', 'merged', 'loading', 'enabled')), ' → '.join(states))
    check('no uncertain detour on the happy path', 'uncertain' not in states, ' → '.join(states))
    log['second_vote_to_enabled_s'] = round(time.time() - approved)
    rule = next((x for x in (prom_rules(group) or {}).get('rules', []) if x.get('name') == name), None)
    check('Prometheus has the rule with health ok', rule and rule.get('health') == 'ok',
          json.dumps({k: (rule or {}).get(k) for k in ('health', 'labels', 'lastEvaluation')}, ensure_ascii=False))
    check('rule labels are application=checkout-api and environment (D32)',
          rule and rule.get('labels', {}).get('application') == 'checkout-api' and rule.get('labels', {}).get('environment') == env,
          (rule or {}).get('labels'))
    _, listing = op('GET', '/alert-rules')
    row = next((x for x in (listing or {}).get('items') or [] if x.get('name') == name), None)
    check('the rule is in the product rule list', row, json.dumps(row, ensure_ascii=False)[:200])
    gitlab_and_audit(op, prop, ex, eid, pid)
    alert = None
    if inst == 'prod':
        # Groups carry the application and local fingerprint, not the provider's alert name, so the
        # alert is found by title in the inbox and its group by instance ID.
        started, hit = time.time(), None
        while time.time() - started < 300 and not hit:
            _, inbox = op('GET', '/inbox')
            alert = next((x.get('alert_id') for x in (inbox or {}).get('items') or [] if x.get('title') == name), None)
            if alert:
                _, groups = op('GET', '/alert-groups')
                hit = next((g for g in (groups or {}).get('items') or []
                            if any(i.get('id') == alert for i in g.get('instances') or [])), None)
            if not hit:
                time.sleep(15)
        log.update(alert_id=alert, alert_seen_s=round(time.time() - t0))
        checkout = json.loads((LOCAL / inst / 'alertmanager.json').read_text())['environments'][env]['application_bindings']['checkout-api']
        check('its alert reaches the prod instance and maps to checkout-api',
              hit and hit.get('service') == checkout and hit.get('environment_key') == env,
              json.dumps({k: (hit or {}).get(k) for k in ('id', 'service', 'environment_key', 'state', 'since')}) if hit else 'not within 5 min')
    if not (a.keep or not row):
        disable(op, inst, env, row, inv, group, a.timeout, t0, log)
        if alert:
            # Prometheus sends no resolve for a removed rule; Alertmanager lets the alert expire.
            started, state = time.time(), None
            while time.time() - started < 600:
                _, detail = op('GET', f'/alerts/{alert}')
                state = (detail or {}).get('state')
                if state == 'resolved':
                    break
                time.sleep(20)
            log['alert_resolved_s'] = round(time.time() - t0)
            check('its alert resolves after the rule is disabled', state == 'resolved', f'{state} after {round(time.time() - started)}s')
    finish(inst, log)


def gitlab_and_audit(op, prop, ex, eid, pid):
    """§9.2 3.4 and 3.6: the merged MR names the execution, main holds exactly the approved file, and
    the audit log shows the request and the executor's steps in this environment."""
    repo = ex.get('repository') or {}
    try:
        with urllib.request.urlopen(f"{GITLAB}/merge_requests/{repo.get('mr_iid')}", timeout=10) as r:
            mr = json.load(r)
        check('the GitLab MR is merged, names the execution and matches the merge', mr.get('state') == 'merged'
              and eid in (mr.get('description') or '') and repo.get('merged_sha') in (mr.get('merge_commit_sha'), mr.get('sha')),
              f"!{mr.get('iid')} {mr.get('state')} {(mr.get('merge_commit_sha') or mr.get('sha') or '')[:8]}")
        path = urllib.parse.quote(repo.get('path') or '', safe='')
        with urllib.request.urlopen(f"{GITLAB}/repository/files/{path}/raw?ref={repo.get('merged_sha')}", timeout=10) as r:
            merged = r.read().decode()
        approved = '\n'.join(x['text'] for x in ((prop.get('alert_rule') or {}).get('change') or {}).get('lines') or [] if x.get('op') in (' ', '+'))
        check('main holds exactly the approved file', merged.rstrip('\n') == approved.rstrip('\n'),
              f'{len(merged)} bytes merged, {len(approved)} approved')
    except OSError as e:
        check('the GitLab MR is merged, names the execution and matches the merge', False, e)
    started, actions = time.time(), set()
    while time.time() - started < 30:
        _, audit = op('GET', f'/audit?range=24h&q={pid}', role='admin')
        actions = {i.get('action') for i in (audit or {}).get('items') or []}
        _, audit = op('GET', f'/audit?range=24h&q={eid}', role='admin')
        actions |= {i.get('action') for i in (audit or {}).get('items') or []}
        if {'v3.alert_rule.enable_requested', 'v3.alert_rule.enable.prepare', 'v3.alert_rule.enable.observed'} <= actions:
            break
        time.sleep(5)
    check('the audit log shows the request and the executor steps (PA-93)',
          {'v3.alert_rule.enable_requested', 'v3.alert_rule.enable.prepare', 'v3.alert_rule.enable.observed'} <= actions, sorted(a for a in actions if a))


def finish(inst, log):
    log['results'] = results
    (LOCAL / f'e2e/drills/alert-rule-{inst}.json').write_text(json.dumps(log, ensure_ascii=False, indent=1))
    print(f"{sum(r['ok'] for r in results)}/{len(results)} checks passed")
    if not all(r['ok'] for r in results):
        sys.exit(1)


def disable(op, inst, env, row, inv, group, timeout, t0, log):
    name = row['name']
    status, r = op('POST', f"/alert-rules/{row['id']}/disable", {'investigation_id': inv}, key=secrets.token_hex(16))
    dpid = (r or {}).get('proposal_id')
    if not check('disable proposal accepted (202)', status == 202 and dpid, f'{status} {r}'):
        return
    ok, tail = approve(inst, env, dpid)
    check('two approvers approve the disable with passkeys', ok, tail)
    deid, dseen, dex = follow(op, dpid, ('disabled',), timeout, t0)
    log.update(disable_execution=deid, disable_states=dseen, disable=dex)
    check('disable execution reaches disabled', dex.get('state') == 'disabled', f"{dex.get('state')} {dex.get('failure')}")
    dstates = [str(s) for s, _ in dseen]
    check('no uncertain detour on the disable path', 'uncertain' not in dstates, ' → '.join(dstates))
    gone = not any(x.get('name') == name for x in (prom_rules(group) or {}).get('rules', []))
    check('the rule is gone from Prometheus', gone, '')


if __name__ == '__main__':
    main()
