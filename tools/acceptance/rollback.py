#!/usr/bin/env python3
"""End-to-end Kubernetes rollback drill on the prod acceptance instance (PA-08, design §6 steps 2–4).

  python3 tools/acceptance/rollback.py [--app checkout-api] [--ratio 0.5] [--skip-ai]

Stages, each timed:
  1. bad release on (aiops-test lab-faults: a new ReplicaSet revision answering 5xx);
  2. LabHigh5xxRate firing in Prometheus, then a firing alert group in the product;
  3. the operator opens an investigation and asks the AI (real model, read-only tools);
  4. rollback candidates list the previous revision; negative checks (no Idempotency-Key 428,
     current revision 422, empty reason 422); propose (201) and replay the same key (200, same id);
  5. self-approval by the proposer and approval by an API token are refused;
  6. two approvers approve with passkeys (web/e2e/golden-approval.spec.ts);
  7. the execution is polled to a terminal state (status timeline, rollout, verification samples);
  8. the Deployment is read back (no LAB_FAULTS, operation annotation = execution id);
  9. the alert clears in Prometheus and the product group resolves.
The lab knob is turned off at the end in any case; once the rollback restored the old template that
only touches the Deployment's change-cause annotation, not the Pod template.
--ui proposes through the browser from the AI suggestion on the canvas (golden-rollback-canvas.spec.ts) instead of
the API. Writes .local/acceptance/e2e/drills/rollback-prod.json (rollback-prod-ui.json with --ui).
"""
import argparse
import json
import secrets
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402
from drill import OUT, am_alerts, app_ids, lab, product_groups, prom_alerts, wait  # noqa: E402
from golden import bearer  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
KUBECONFIG = Path.home() / 'Home/code/codex/aiops-test/.local/vm-cluster/kubeconfig'
ALERTS = ['LabHigh5xxRate']
TERMINAL = {'succeeded', 'failed', 'needs_attention'}


def op(method, path, body=None, key=None, role='operator'):
    return call('prod', method, path, body, env='prod', role=role, idempotency_key=key)


def kube_json(*args):
    r = subprocess.run(['kubectl', '--kubeconfig', str(KUBECONFIG), '--context', 'aiops-vm-cluster', '-n', 'aiops-lab',
                        *args, '-o', 'json'], capture_output=True, text=True, timeout=60)
    return json.loads(r.stdout) if r.returncode == 0 else {'error': r.stderr.strip()[-300:]}


def investigate(app_id, question, ask=True):
    status, created = op('POST', '/canvases', {'title': '回滚演练：checkout-api 5xx 升高', 'kind': 'investigation',
                                               'template_id': None, 'goal': question, 'services': [app_id], 'ai_draft': False})
    if status >= 300:
        return {'error': f'create {status} {created}'}
    inv = created['id']
    _, graph = op('GET', f'/investigations/{inv}/canvas')
    root = next((n['id'] for n in graph.get('nodes', []) if n.get('kind') == 'root'), (graph.get('nodes') or [{}])[0].get('id'))
    if not ask:
        return {'investigation': inv, 'root': root, 'status': None, 'seconds': 0, 'summary': ''}
    started = time.time()
    status, reply = op('POST', f'/investigations/{inv}/nodes/{root}/messages', {'text': question})
    _, after = op('GET', f'/investigations/{inv}/canvas')
    projected = next((n.get('rollback_suggestion') for n in after.get('nodes', []) if n.get('id') == root), None)
    return {'investigation': inv, 'root': root, 'status': status, 'seconds': round(time.time() - started),
            'summary': (reply or {}).get('summary', '')[:4000], 'rollback_suggestion': (reply or {}).get('rollback_suggestion'),
            'projected_suggestion': projected}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--app', default='checkout-api')
    ap.add_argument('--ratio', default='0.5')
    ap.add_argument('--skip-ai', action='store_true', help='open the investigation but do not ask the model')
    ap.add_argument('--ui', action='store_true', help='propose from the AI suggestion on the canvas (golden-rollback-canvas.spec.ts)')
    ap.add_argument('--fire-timeout', type=int, default=600)
    ap.add_argument('--exec-timeout', type=int, default=1200)
    a = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    ids = app_ids()
    app_id = ids[a.app]
    services = json.loads((LOCAL / 'rollback-services.json').read_text())['prod']
    sid = services[a.app]
    log = {'drill': 'rollback', 'app': a.app, 't0': time.time(), 'started_at': time.strftime('%Y-%m-%d %H:%M:%S'), 'checks': {}}
    checks = log['checks']

    def check(name, ok, detail=''):
        checks[name] = {'ok': bool(ok), 'detail': detail}
        print(f"  {'PASS' if ok else 'FAIL'}  {name}  {detail}", flush=True)

    before = kube_json('get', 'deployment', a.app)
    log['revision_before'] = before.get('metadata', {}).get('annotations', {}).get('deployment.kubernetes.io/revision')
    print(f"{log['started_at']} bad release on ({a.app}, revision before {log['revision_before']}): "
          f"{lab('drill', 'bad-release', 'on', '--app', a.app, '--ratio', a.ratio)}", flush=True)
    try:
        bad = kube_json('get', 'deployment', a.app)
        log['revision_bad'] = bad.get('metadata', {}).get('annotations', {}).get('deployment.kubernetes.io/revision')
        wait('prometheus_firing', lambda: {k: v for k, v in prom_alerts(ALERTS, [a.app]).items() if v == 'firing'}, a.fire_timeout, log)
        wait('alertmanager_active', lambda: am_alerts(ALERTS, [a.app]), 180, log)
        firing = wait('product_firing', lambda: {g: s for g, s in product_groups({app_id}).items() if s == 'firing'}, 240, log)
        log['product_groups'] = sorted(firing or {})

        question = (f'{a.app} 的 5xx 刚刚升高并触发了 LabHigh5xxRate。请用只读工具查看错误率、日志和 Kubernetes 修订，'
                    '判断是否由最近一次发布引起；如果是，说明应该回滚到哪个修订以及依据。')
        print('  investigating…', flush=True)
        log['investigation'] = investigate(app_id, question, ask=not a.skip_ai)
        inv = log['investigation'].get('investigation')
        log['ai_done'] = round(time.time() - log['t0'])
        print(f"  +{log['ai_done']:>4}s investigation {inv} ({log['investigation'].get('status')}, {log['investigation'].get('seconds')}s)", flush=True)

        status, cand = op('GET', f'/services/{sid}/rollback-candidates')
        log['candidates'] = cand
        current = next((c for c in cand.get('candidates', []) if c.get('current')), {})
        target = next((c for c in cand.get('candidates', []) if not c.get('current')), {})
        check('candidates readable, not blocked', status == 200 and cand.get('blocked_reason') is None and target,
              f"{status} blocked={cand.get('blocked_reason')} current={current.get('revision')} target={target.get('revision')}")
        if not a.skip_ai:
            sug = log['investigation'].get('rollback_suggestion') or {}
            check('AI suggests rolling back (PA-72)', bool(sug), json.dumps(sug, ensure_ascii=False)[:300])
            check('AI suggests the previous healthy revision', sug.get('target_revision') == target.get('revision'),
                  f"suggested {sug.get('target_revision')} from {sug.get('from_revision')}, candidate {target.get('revision')}")
            check('suggestion carries the encoded service id', sug.get('service_id') == sid, str(sug.get('service_id')))
            check('suggestion is on the canvas node after a re-read', log['investigation'].get('projected_suggestion') == sug and bool(sug), '')
        env_changes = [c for c in target.get('changes', []) if c.get('field') == 'env' and c.get('name') == 'LAB_FAULTS']
        check('target revision removes the injected fault', current.get('revision') == log['revision_bad'] and env_changes,
              json.dumps(env_changes, ensure_ascii=False)[:200])
        body = {'investigation_id': inv, 'service_id': sid, 'target_revision': target.get('revision'),
                'expected_template_digest': (cand.get('workload') or {}).get('current_template_digest'),
                'reason': f"坏发布修订 {current.get('revision')} 使 /checkout 返回 5xx，回到修订 {target.get('revision')}"}
        status, r = op('POST', '/rollback-proposals', body, key=False)
        check('propose without Idempotency-Key is refused (428)', status == 428, f'{status} {(r or {}).get("error", r)}')
        status, r = op('POST', '/rollback-proposals', {**body, 'target_revision': current.get('revision')}, key=secrets.token_hex(12))
        check('propose the current revision is refused (422)', status == 422, f'{status} {(r or {}).get("error", r)}')
        status, r = op('POST', '/rollback-proposals', {**body, 'reason': ''}, key=secrets.token_hex(12))
        check('propose without a reason is refused (422)', status == 422, f'{status} {(r or {}).get("error", r)}')
        if a.ui:
            # A person presses the suggestion on the canvas, checks the preselected revision and submits.
            out = LOCAL / 'e2e/golden/rollback-canvas-proposal.txt'
            out.unlink(missing_ok=True)
            r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-rollback-canvas.spec.ts'], cwd=ROOT / 'web',
                               env={**__import__('os').environ, 'GOLDEN_INVESTIGATION': inv}, capture_output=True, text=True, timeout=600)
            pid = out.read_text().strip() if out.exists() else None
            check('proposed from the canvas suggestion (golden-rollback-canvas.spec.ts)', r.returncode == 0 and pid,
                  ' | '.join((r.stdout + r.stderr).strip().splitlines()[-3:]))
        else:
            key = secrets.token_hex(16)
            status, created = op('POST', '/rollback-proposals', body, key=key)
            pid = (created or {}).get('proposal_id')
            check('propose (201)', status == 201 and pid, f'{status} {created}')
            status, again = op('POST', '/rollback-proposals', body, key=key)
            check('replay with the same key returns the same proposal (200)', status == 200 and (again or {}).get('proposal_id') == pid, f'{status} {again}')
        log['proposal'] = pid
        log['proposed_at'] = round(time.time() - log['t0'])
        (LOCAL / 'e2e/golden/rollback-proposal.txt').write_text(pid or '')

        status, r = op('POST', f'/proposals/{pid}/approve/options', {'confirm': True}, key=secrets.token_hex(12))
        check('proposer cannot approve their own proposal', status in (403, 409), f'{status} {(r or {}).get("error", r)}')
        status, issued = call('prod', 'POST', '/api-tokens', {'name': 'rollback-drill', 'scopes': ['connectors:view'], 'ttl': 3600},
                              env='prod', idempotency_key=secrets.token_hex(12))
        token = (issued or {}).get('secret_shown_once')
        code = bearer('POST', f'/proposals/{pid}/approve/options', token, {'confirm': True}) if token else None
        check('an API token cannot approve', code in (401, 403), str(code))
        if (issued or {}).get('id'):
            call('prod', 'DELETE', f"/api-tokens/{issued['id']}", env='prod')
        _, detail = op('GET', f'/proposals/{pid}')
        log['proposal_detail'] = {k: detail.get(k) for k in ('title', 'status', 'action_kind', 'rollback', 'risk', 'verification')}
        check('proposal is a pending rollback', detail.get('action_kind') == 'kubernetes.deployment.rollback', f"{detail.get('status')} {detail.get('title')}")
        if a.ui:
            sug = log['investigation'].get('rollback_suggestion') or {}
            check('the proposal targets the suggested revision', (detail.get('rollback') or {}).get('target_revision') == sug.get('target_revision'),
                  f"{(detail.get('rollback') or {}).get('target_revision')} vs suggested {sug.get('target_revision')}")
        check('proposer display name is returned (PA-74)', bool(detail.get('proposed_by_name')), str(detail.get('proposed_by_name')))

        print('  approving with two passkeys…', flush=True)
        r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-approval.spec.ts'], cwd=ROOT / 'web',
                           env={**__import__('os').environ, 'GOLDEN_PROPOSAL': pid}, capture_output=True, text=True, timeout=900)
        tail = (r.stdout + r.stderr).strip().splitlines()[-4:]
        check('two approvers approve with passkeys (golden-approval.spec.ts)', r.returncode == 0, ' | '.join(tail))
        log['approved_at'] = round(time.time() - log['t0'])

        eid, seen, started = None, [], time.time()
        while time.time() - started < a.exec_timeout:
            _, detail = op('GET', f'/proposals/{pid}')
            eid = ((detail or {}).get('execution') or {}).get('execution_id') or eid
            if eid:
                _, ex = op('GET', f'/executions/{eid}')
                st = ex.get('status')
                if not seen or seen[-1][0] != st:
                    seen.append((st, round(time.time() - log['t0'])))
                    print(f"  +{seen[-1][1]:>4}s execution {st} {ex.get('status_reason') or ''}", flush=True)
                log['execution'] = ex
                if st in TERMINAL:
                    break
            time.sleep(3)
        log['execution_id'] = eid
        log['status_changes'] = seen
        ex = log.get('execution') or {}
        states = [t.get('state') for t in ex.get('timeline', [])]
        check('execution succeeded', ex.get('status') == 'succeeded', f"{ex.get('status')} {ex.get('status_reason')}")
        check('timeline applied → rolling_out → verifying → succeeded',
              [s for s in states if s in ('applied', 'rolling_out', 'verifying', 'succeeded')] == ['applied', 'rolling_out', 'verifying', 'succeeded'],
              ' → '.join(states))
        samples = (ex.get('verification') or {}).get('samples') or []
        check('verification passed with samples', (ex.get('verification') or {}).get('result') == 'passed' and samples,
              f"{len(samples)} samples, last {samples[-1] if samples else None}")

        after = kube_json('get', 'deployment', a.app)
        env = {e['name']: e.get('value', '') for e in after.get('spec', {}).get('template', {}).get('spec', {}).get('containers', [{}])[0].get('env', [])}
        notes = {k: v for k, v in after.get('metadata', {}).get('annotations', {}).items() if 'aiops' in k or 'revision' in k}
        log['readback'] = {'LAB_FAULTS': env.get('LAB_FAULTS'), 'annotations': notes}
        check('Deployment template no longer injects faults', env.get('LAB_FAULTS', '') == '', repr(env.get('LAB_FAULTS')))
        check('operation annotation names the execution', eid and eid in json.dumps(notes), json.dumps(notes)[:200])
    finally:
        log['off_at'] = round(time.time() - log['t0'])
        print(f"  +{log['off_at']:>4}s bad release off: {lab('drill', 'bad-release', 'off', '--app', a.app)}", flush=True)
    wait('prometheus_cleared', lambda: not prom_alerts(ALERTS, [a.app]) or None, 900, log)
    wait('product_resolved', lambda: (all(s != 'firing' for g, s in product_groups({app_id}).items() if g in set(log.get('product_groups') or [])) or None), 300, log)
    final = kube_json('get', 'deployment', a.app)
    log['revision_final'] = final.get('metadata', {}).get('annotations', {}).get('deployment.kubernetes.io/revision')
    log['lab_status'] = lab('status')
    passed = sum(c['ok'] for c in checks.values())
    print(f'{passed}/{len(checks)} checks passed')
    (OUT / ('rollback-prod-ui.json' if a.ui else 'rollback-prod.json')).write_text(json.dumps(log, ensure_ascii=False, indent=2))
    print(f"report: {OUT / ('rollback-prod-ui.json' if a.ui else 'rollback-prod.json')}")


if __name__ == '__main__':
    main()
