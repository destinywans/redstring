#!/usr/bin/env python3
"""D11 full release drill on the prod acceptance instance (design §9.1 step 4).

  python3 tools/acceptance/delivery_drill.py [--skip-fix] [--fire-timeout 900]

Stages, each timed:
  1. the lab merges the bad-version MR in GitLab, the pipeline passes and the manual deploy job
     rolls checkout-api out (aiops-test delivery/d11.py release-bad);
  2. GET /changes?sources=gitlab lists that deployment with its pipeline and cluster revision;
  3. LabHigh5xxRate fires in Prometheus, reaches Alertmanager and the product;
  4. GET /incidents/{id}/changes lists the deployment before the alert start, time-adjacent only,
     with the matching Kubernetes revision and coverage;
  5. an investigation is opened from the incident; the AI (real model, read-only tools) reads the
     changes and the workload, cites the deployment and suggests the revision before it;
  6. the operator proposes the rollback, two approvers approve with passkeys, the executor rolls
     back and verifies; the alert clears and the product group resolves;
  7. the lab merges the fix and redeploys (delivery/d11.py release-fix), so GitLab main and the
     cluster agree again, and the product lists that deployment too.
Writes .local/acceptance/e2e/drills/delivery-prod.json. Lab writes stay in aiops-test's scripts,
which use only the explicit lab kubeconfig.
"""
import argparse
import json
import secrets
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402
from drill import LAB, OUT, am_alerts, app_ids, prom_alerts, product_groups, wait  # noqa: E402
from rollback import kube_json  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
APP = 'checkout-api'
ALERTS = ['LabHigh5xxRate']
TERMINAL = {'succeeded', 'failed', 'needs_attention'}
REPORT = OUT / 'delivery-prod.json'


def op(method, path, body=None, key=None, role='operator'):
    return call('prod', method, path, body, env='prod', role=role, idempotency_key=key)


def d11(*args, timeout=1800):
    """Run a lab D11 command; its last stdout line is a JSON result."""
    r = subprocess.run(['python3', 'delivery/d11.py', *args], cwd=LAB, capture_output=True, text=True, timeout=timeout)
    lines = r.stdout.strip().splitlines()
    try:
        return r.returncode, json.loads(lines[-1]) if lines else {}
    except ValueError:
        return r.returncode, {'error': (r.stdout + r.stderr).strip()[-400:]}


def revision():
    return kube_json('get', 'deployment', APP).get('metadata', {}).get('annotations', {}).get('deployment.kubernetes.io/revision')


def changes(sid):
    q = urllib.parse.urlencode({'service_id': sid, 'range': '24h', 'sources': 'gitlab'})
    return op('GET', f'/changes?{q}')


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--skip-fix', action='store_true', help='leave the fix release to a later run')
    ap.add_argument('--resume', action='store_true', help='keep stages 1-4 of the last report and continue from the investigation')
    ap.add_argument('--fire-timeout', type=int, default=900)
    ap.add_argument('--exec-timeout', type=int, default=1200)
    a = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    app_id = app_ids()[APP]
    sid = json.loads((LOCAL / 'rollback-services.json').read_text())['prod'][APP]
    log = {'drill': 'delivery', 'app': APP, 't0': time.time(), 'started_at': time.strftime('%Y-%m-%d %H:%M:%S'), 'checks': {}}
    if a.resume:
        log = json.loads(REPORT.read_text())
        log['resumed_at'] = round(time.time() - log['t0'])
        keep = list(log['checks'])[:list(log['checks']).index('investigation opened from the incident')]
        log['checks'] = {k: log['checks'][k] for k in keep}
    checks = log['checks']

    def check(name, ok, detail=''):
        checks[name] = {'ok': bool(ok), 'detail': str(detail)[:400]}
        print(f"  {'PASS' if ok else 'FAIL'}  {name}  {str(detail)[:240]}", flush=True)
        return ok

    def save():
        REPORT.write_text(json.dumps(log, ensure_ascii=False, indent=2))

    if a.resume:
        bad = log['release_bad']
        dep_ids = {str(bad['deployment'].get('id')), str(bad['deployment'].get('iid'))}
        deployment, pipeline, sha = str(bad['deployment'].get('iid')), bad.get('pipeline'), bad.get('merge_sha') or ''
        incident = log['incident']
        print(f"resuming at +{log['resumed_at']}s: incident {incident}, deployment #{deployment}, revision {log['revision_bad']}", flush=True)
    else:
        incident = deployment = pipeline = sha = dep_ids = None
    if not a.resume:
        ready = subprocess.run(['python3', 'cluster/clock-watchdog.py', 'check'], cwd=LAB, capture_output=True, text=True, timeout=180)
        log['lab_check'] = ready.stdout.strip().splitlines()[-3:]
        if 'READY for drills' not in ready.stdout or 'NOT READY' in ready.stdout:
            print(f"lab not ready, drill not started: {log['lab_check']}", flush=True)
            save()
            sys.exit(2)
        log['revision_before'] = revision()
        incident, deployment, pipeline, sha, dep_ids = early_stages(a, log, check, save, app_id, sid)
    stage5(a, log, check, save, app_id, sid, incident, deployment, pipeline, sha, dep_ids)


def early_stages(a, log, check, save, app_id, sid):

    # 1. Bad version through GitLab.
    print(f"{log['started_at']} releasing the bad version through GitLab (revision before {log['revision_before']})…", flush=True)
    code, bad = d11('release-bad')
    log['release_bad'] = bad
    log['released'] = round(time.time() - log['t0'])
    print(f"  +{log['released']:>4}s release-bad exit {code}: {json.dumps(bad, ensure_ascii=False)[:300]}", flush=True)
    if not check('bad version merged, pipeline passed and deployed', code == 0 and bad.get('deployment') and
                 bad.get('revision_after') and bad.get('revision_after') != bad.get('revision_before'), json.dumps(bad)[:300]):
        save()
        sys.exit(2)
    # GitLab deployments have a global id and a per-project iid; the product may show either.
    dep_ids = {str(bad['deployment'].get('id')), str(bad['deployment'].get('iid'))}
    deployment, pipeline, sha = str(bad['deployment'].get('iid')), bad.get('pipeline'), bad.get('merge_sha') or ''
    log['revision_bad'] = str(bad['revision_after'])

    # 2. The changes view.
    status, ch = changes(sid)
    dep = next((d for d in (ch or {}).get('deployments') or [] if {str(d.get('id')), str(d.get('iid'))} & dep_ids), None)
    log['changes_deployment'] = dep
    check('changes view lists the deployment', status == 200 and dep, f'{status} {json.dumps(dep, ensure_ascii=False)[:240]}')
    check('deployment maps to the new cluster revision and pipeline', dep and str(dep.get('kubernetes_revision')) == log['revision_bad']
          and (pipeline is None or dep.get('pipeline_id') == pipeline), f"revision {(dep or {}).get('kubernetes_revision')} pipeline {(dep or {}).get('pipeline_id')}")
    check('gitlab coverage complete', ((ch or {}).get('coverage') or {}).get('gitlab') == 'complete', (ch or {}).get('coverage'))

    # 3. Alert.
    wait('prometheus_firing', lambda: {k: v for k, v in prom_alerts(ALERTS, [APP]).items() if v == 'firing'}, a.fire_timeout, log)
    wait('alertmanager_active', lambda: am_alerts(ALERTS, [APP]), 180, log)
    firing = wait('product_firing', lambda: {g: s for g, s in product_groups({app_id}).items() if s == 'firing'}, 300, log)
    log['product_groups'] = sorted(firing or {})
    if not check('LabHigh5xxRate reaches the product', firing, log['product_groups']):
        save()
        sys.exit(1)
    _, groups = op('GET', '/alert-groups')
    group = next(g for g in groups['items'] if g['id'] in firing)
    incident = (group.get('instances') or [{}])[0].get('id')
    log['incident'] = incident

    # 4. Changes around the alert.
    status, around = op('GET', f'/incidents/{incident}/changes')
    log['incident_changes'] = around
    items = (around or {}).get('items') or []
    gdep = next((i for i in items if i.get('kind') == 'gitlab_deployment' and str(i.get('id')).split(':')[-1] in dep_ids), None)
    rev = next((i for i in items if i.get('revision') == log['revision_bad']), None)
    check('incident changes list the deployment before the alert start', status == 200 and gdep and (gdep.get('offset_seconds') or 0) < 0,
          json.dumps(gdep, ensure_ascii=False)[:240])
    check('the deployment is marked time-adjacent only', gdep and gdep.get('basis') == 'time_adjacent'
          and (around or {}).get('note') == 'time_adjacent_not_causal', f"{(gdep or {}).get('basis')} {(around or {}).get('note')}")
    check('incident changes carry the matching Kubernetes revision', rev and str(rev.get('gitlab_deployment_id')) in dep_ids,
          json.dumps(rev, ensure_ascii=False)[:200])
    check('incident changes coverage', (around or {}).get('coverage', {}).get('gitlab') == 'complete', (around or {}).get('coverage'))

    return incident, deployment, pipeline, sha, dep_ids


def stage5(a, log, check, save, app_id, sid, incident, deployment, pipeline, sha, dep_ids):
    # 5. Investigation from the incident (the incident page's button); a plain investigation for the
    # service if that is refused, so the rest of the chain is still exercised.
    status, inc = op('POST', f'/incidents/{incident}/investigation', {})
    inv = (inc or {}).get('investigation_id')
    check('investigation opened from the incident', status == 200 and inv, f'{status} {inv} {(inc or {}).get("error")}')
    if not inv:
        status, created = op('POST', '/canvases', {'title': 'D11 演练：checkout-api 5xx 升高', 'kind': 'investigation', 'template_id': None,
                                                   'goal': 'checkout-api 5xx 升高，LabHigh5xxRate 触发', 'services': [app_id], 'ai_draft': False})
        inv = (created or {}).get('id')
        log['investigation_fallback'] = {'status': status, 'id': inv}
        print(f'  fallback investigation {inv} ({status})', flush=True)
    _, graph = op('GET', f'/investigations/{inv}/canvas')
    root = next((n['id'] for n in graph.get('nodes', []) if n.get('kind') == 'root'), (graph.get('nodes') or [{}])[0].get('id'))
    question = (f'{APP} 的 5xx 升高并触发了 LabHigh5xxRate。请用只读工具查看错误率、日志、Kubernetes 修订和告警前后的变更'
                '（GitLab 部署、流水线、合并请求），判断是否与最近一次发布相关；把集群修订和流水线对上；'
                '如果需要回滚，说明回到哪个修订以及依据。')
    print('  investigating…', flush=True)
    started = time.time()
    status, reply = op('POST', f'/investigations/{inv}/nodes/{root}/messages', {'text': question})
    log['ai_seconds'] = round(time.time() - started)
    answer = (reply or {}).get('summary', '') or ''
    _, msgs = op('GET', f'/investigations/{inv}/nodes/{root}/messages')
    last = next((m for m in reversed((msgs or {}).get('messages') or []) if m.get('role') == 'assistant'), {})
    answer = answer or last.get('content', '')
    _, graph = op('GET', f'/investigations/{inv}/canvas')
    tray = {e['id']: e for e in graph.get('tray') or []}
    cited = [tray[i] for i in last.get('evidence_ids') or [] if i in tray]
    bodies = [json.dumps(e.get('body'), ensure_ascii=False) for e in cited]
    log['investigation'] = {'id': inv, 'root': root, 'status': status, 'seconds': log['ai_seconds'], 'answer': answer[:6000],
                            'evidence': [{'title': e.get('title'), 'source': e.get('source')} for e in cited],
                            'rollback_suggestion': (reply or {}).get('rollback_suggestion')}
    check('AI answered', status in (200, 201) and answer, f"{status} {log['ai_seconds']}s")
    check('AI read the service changes (read_service_changes)', any('Changes only, not health' in b for b in bodies),
          [e.get('title') for e in cited])
    check('the changes evidence holds this deployment', any('Changes only, not health' in b and sha[:8] in b for b in bodies), sha[:8])
    check('AI read the workload revisions (read_service_workload)', any('current_revision' in b for b in bodies), '')
    check('answer cites the deployment or its pipeline', any(t in answer for t in (*(f'#{i}' for i in dep_ids), str(pipeline), '!7', sha[:8]))
          if answer else False, answer[:200])
    sug = (reply or {}).get('rollback_suggestion') or {}
    check('AI suggests the revision before the deployment', str(sug.get('target_revision')) == str(log['revision_before']),
          f"suggested {sug.get('target_revision')} from {sug.get('from_revision')}")
    log['ai_done'] = round(time.time() - log['t0'])
    save()

    # 6. Rollback through the executor.
    status, cand = op('GET', f'/services/{sid}/rollback-candidates')
    current = next((c for c in cand.get('candidates', []) if c.get('current')), {})
    target = next((c for c in cand.get('candidates', []) if c.get('revision') == str(log['revision_before'])), {})
    check('rollback candidates: current is the bad revision, target available', status == 200 and current.get('revision') == log['revision_bad']
          and target and cand.get('blocked_reason') is None, f"{status} current {current.get('revision')} target {target.get('revision')}")
    body = {'investigation_id': inv, 'service_id': sid, 'target_revision': target.get('revision'),
            'expected_template_digest': (cand.get('workload') or {}).get('current_template_digest'),
            'reason': f"GitLab 部署 #{deployment}（流水线 {pipeline}）上线的修订 {log['revision_bad']} 使 /checkout 返回 5xx，回到修订 {target.get('revision')}"}
    status, created = op('POST', '/rollback-proposals', body, key=secrets.token_hex(16))
    pid = (created or {}).get('proposal_id')
    check('rollback proposed (201)', status == 201 and pid, f'{status} {created}')
    log['proposal'] = pid
    print('  approving with two passkeys…', flush=True)
    r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-approval.spec.ts'], cwd=ROOT / 'web',
                       env={**__import__('os').environ, 'GOLDEN_PROPOSAL': pid or '', 'GOLDEN_INSTANCE': 'prod', 'GOLDEN_ENV': 'prod'},
                       capture_output=True, text=True, timeout=900)
    check('two approvers approve with passkeys', r.returncode == 0 and ' passed' in r.stdout and pid,
          ' | '.join((r.stdout + r.stderr).strip().splitlines()[-3:]))
    log['approved_at'] = round(time.time() - log['t0'])
    eid, ex, started = None, {}, time.time()
    while pid and time.time() - started < a.exec_timeout:
        _, detail = op('GET', f'/proposals/{pid}')
        eid = ((detail or {}).get('execution') or {}).get('execution_id') or eid
        if eid:
            _, ex = op('GET', f'/executions/{eid}')
            if ex.get('status') in TERMINAL:
                break
        time.sleep(3)
    log['execution_id'], log['execution'] = eid, ex
    log['executed_at'] = round(time.time() - log['t0'])
    check('rollback execution succeeded and verified', ex.get('status') == 'succeeded' and (ex.get('verification') or {}).get('result') == 'passed',
          f"{ex.get('status')} {ex.get('status_reason')} {(ex.get('verification') or {}).get('result')}")
    log['revision_rolled_back'] = revision()
    wait('prometheus_cleared', lambda: not prom_alerts(ALERTS, [APP]) or None, 900, log)
    wait('product_resolved', lambda: (all(s != 'firing' for g, s in product_groups({app_id}).items() if g in set(log['product_groups'])) or None), 300, log)
    check('alert cleared and the product group resolved', log.get('product_resolved'), log.get('product_resolved'))
    save()

    # 7. Fix through GitLab so main and the cluster agree.
    if not a.skip_fix:
        print('  releasing the fix through GitLab…', flush=True)
        code, fix = d11('release-fix')
        log['release_fix'] = fix
        log['fixed_at'] = round(time.time() - log['t0'])
        check('fix merged and deployed', code == 0 and fix.get('deployment') and fix.get('revision_after'), json.dumps(fix)[:300])
        fix_ids = {str((fix.get('deployment') or {}).get(k)) for k in ('id', 'iid')}
        status, ch = changes(sid)
        fdep = next((d for d in (ch or {}).get('deployments') or [] if {str(d.get('id')), str(d.get('iid'))} & fix_ids), None)
        check('changes view lists the fix deployment on the new revision', fdep and str(fdep.get('kubernetes_revision')) == str(fix.get('revision_after')),
              json.dumps(fdep, ensure_ascii=False)[:240])
        time.sleep(180)
        check('no LabHigh5xxRate after the fix', not prom_alerts(ALERTS, [APP]), prom_alerts(ALERTS, [APP]))
    log['revision_final'] = revision()
    checks = log['checks']
    passed = sum(c['ok'] for c in checks.values())
    print(f'{passed}/{len(checks)} checks passed')
    save()
    print(f'report: {REPORT}')
    sys.exit(0 if passed == len(checks) else 1)


if __name__ == '__main__':
    main()
