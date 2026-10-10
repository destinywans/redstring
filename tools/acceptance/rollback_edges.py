#!/usr/bin/env python3
"""Rollback failure and boundary drills on the nonprod acceptance instance (PA-08, design §6 step 5).

  python3 tools/acceptance/rollback_edges.py faulty-target|restart|drift|revoked [--app checkout-api]

  faulty-target  roll a healthy Deployment back to a revision that still injects 5xx: verification fails
                 after the settle period, status failed/verification_failed, and the executor writes once only;
  restart        bad release, rollback approved, the nonprod process restarted while verifying: the executor
                 takes the lease over again and still reaches succeeded;
  drift          the Deployment changes after the proposal and before approval: nothing is written,
                 needs_attention (drift) or a refused approval;
  revoked        the first approver loses the approver role between the two votes: nothing is written;
  write-forbidden  (--app message-api) the write binding names a Deployment the lab account may not patch:
                 Kubernetes refuses the write, needs_attention/write_forbidden, the Deployment is unchanged.
Approvals go through web/e2e/golden-approval.spec.ts on nonprod (GOLDEN_INSTANCE=nonprod). The lab knob is
turned off at the end of every drill. Writes .local/acceptance/e2e/drills/rollback-edge-<name>.json.
"""
import argparse
import base64
import json
import os
import secrets
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402
from accounts import member_id  # noqa: E402
from drill import LAB, OUT, lab  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
KUBECONFIG = Path.home() / 'Home/code/codex/aiops-test/.local/vm-cluster/kubeconfig'
INSTANCE, ENV = 'nonprod', 'test'
TERMINAL = {'succeeded', 'failed', 'needs_attention'}


def op(method, path, body=None, role='operator', key=None):
    return call(INSTANCE, method, path, body, env=ENV, role=role, idempotency_key=key)


def deployment(app):
    r = subprocess.run(['kubectl', '--kubeconfig', str(KUBECONFIG), '--context', 'aiops-vm-cluster', '-n', 'aiops-lab',
                        'get', 'deployment', app, '-o', 'json'], capture_output=True, text=True, timeout=60)
    d = json.loads(r.stdout) if r.returncode == 0 else {}
    notes = d.get('metadata', {}).get('annotations', {})
    env = {e['name']: e.get('value', '') for e in d.get('spec', {}).get('template', {}).get('spec', {}).get('containers', [{}])[0].get('env', [])}
    return {'revision': notes.get('deployment.kubernetes.io/revision'), 'operation': notes.get('aiops-canvas.io/operation'),
            'LAB_FAULTS': env.get('LAB_FAULTS')}


def candidates(sid):
    status, body = op('GET', f'/services/{sid}/rollback-candidates')
    return status, body


def fault_of(candidate):
    return next((c.get('to') for c in candidate.get('changes', []) if c.get('field') == 'env' and c.get('name') == 'LAB_FAULTS'), None)


def investigation(app_id, title):
    status, created = op('POST', '/canvases', {'title': title, 'kind': 'investigation', 'template_id': None,
                                               'goal': title, 'services': [app_id], 'ai_draft': False})
    return created.get('id') if status < 300 else None


def propose(sid, inv, cand, target, reason):
    body = {'investigation_id': inv, 'service_id': sid, 'target_revision': target,
            'expected_template_digest': (cand.get('workload') or {}).get('current_template_digest'), 'reason': reason}
    status, created = op('POST', '/rollback-proposals', body, key=secrets.token_hex(16))
    return status, created


def approve(pid, approvers='approver1,approver2'):
    r = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'golden-approval.spec.ts'], cwd=ROOT / 'web',
                       env={**os.environ, 'GOLDEN_PROPOSAL': pid, 'GOLDEN_INSTANCE': INSTANCE, 'GOLDEN_ENV': ENV,
                            'GOLDEN_APPROVERS': approvers}, capture_output=True, text=True, timeout=900)
    return r.returncode, (r.stdout + r.stderr).strip().splitlines()[-4:]


def execution(pid):
    _, detail = op('GET', f'/proposals/{pid}')
    eid = ((detail or {}).get('execution') or {}).get('execution_id')
    if not eid:
        return detail, None
    _, ex = op('GET', f'/executions/{eid}')
    return detail, ex


def follow(pid, log, timeout=900, on_status=None):
    seen, started, ex = [], time.time(), None
    while time.time() - started < timeout:
        _, ex = execution(pid)
        st = (ex or {}).get('status')
        if st and (not seen or seen[-1][0] != st):
            seen.append((st, round(time.time() - log['t0'])))
            print(f"  +{seen[-1][1]:>4}s execution {st} {ex.get('status_reason') or ''}", flush=True)
            if on_status:
                on_status(st)
        if st in TERMINAL:
            break
        time.sleep(3)
    log['status_changes'] = seen
    log['execution'] = ex
    return ex or {}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('drill', choices=['faulty-target', 'restart', 'drift', 'revoked', 'write-forbidden'])
    ap.add_argument('--app', default='checkout-api')
    a = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    sid = json.loads((LOCAL / 'rollback-services.json').read_text())[INSTANCE][a.app]
    app_id = base64.urlsafe_b64decode(sid + '=' * (-len(sid) % 4)).decode().split('\x00')[0]
    log = {'drill': a.drill, 'app': a.app, 't0': time.time(), 'started_at': time.strftime('%Y-%m-%d %H:%M:%S'), 'checks': {},
           'before': deployment(a.app)}
    checks = log['checks']

    def check(name, ok, detail=''):
        checks[name] = {'ok': bool(ok), 'detail': detail}
        print(f"  {'PASS' if ok else 'FAIL'}  {name}  {detail}", flush=True)

    print(f"{log['started_at']} {a.drill} on {INSTANCE}/{ENV} {a.app}, before {log['before']}", flush=True)
    ready = subprocess.run(['python3', 'cluster/clock-watchdog.py', 'check'], cwd=LAB, capture_output=True, text=True, timeout=180)
    if 'READY for drills' not in ready.stdout or 'NOT READY' in ready.stdout:
        print(f"lab not ready, drill not started: {ready.stdout.strip().splitlines()[-3:]}", flush=True)
        sys.exit(2)
    inv = investigation(app_id, f'回滚边界演练：{a.drill}')
    log['investigation'] = inv
    try:
        if a.drill not in ('faulty-target', 'write-forbidden'):
            on = lab('drill', 'bad-release', 'on', '--app', a.app)
            print(f"  bad release on: {on}", flush=True)
            if not any('rollout complete' in line for line in on):
                check('bad release took effect', False, str(on))
                raise SystemExit(2)
        status, cand = candidates(sid)
        cur = next((c for c in cand.get('candidates', []) if c.get('current')), {})
        others = [c for c in cand.get('candidates', []) if not c.get('current')]
        if a.drill == 'faulty-target':
            target = next((c for c in others if fault_of(c)), {})
        else:
            target = next((c for c in others if fault_of(c) in ('', None)), {})
        log['current'], log['target'] = cur.get('revision'), target.get('revision')
        check('candidates readable', status == 200 and cur and target, f"{status} current={cur.get('revision')} target={target.get('revision')} fault={fault_of(target)!r}")
        status, created = propose(sid, inv, cand, target.get('revision'), f'边界演练 {a.drill}：回到修订 {target.get("revision")}')
        pid = (created or {}).get('proposal_id')
        log['proposal'] = pid
        check('propose (201)', status == 201 and pid, f'{status} {created}')

        if a.drill == 'faulty-target':
            code, tail = approve(pid)
            check('two approvals', code == 0, ' | '.join(tail))
            ex = follow(pid, log)
            check('status failed / verification_failed', ex.get('status') == 'failed' and ex.get('status_reason') == 'verification_failed',
                  f"{ex.get('status')} {ex.get('status_reason')}")
            samples = (ex.get('verification') or {}).get('samples') or []
            check('failing samples recorded', samples and not samples[-1].get('ok'), json.dumps(samples[-1] if samples else None))
            first = deployment(a.app)
            time.sleep(60)
            later = deployment(a.app)
            log['after'], log['later'] = first, later
            check('written once, no automatic retry', first['operation'] == ex.get('id') and later['revision'] == first['revision'],
                  f"revision {first['revision']} → {later['revision']} after 60 s, operation {first['operation']}")

        elif a.drill == 'restart':
            code, tail = approve(pid)
            check('two approvals', code == 0, ' | '.join(tail))
            restarted = {}

            def on_status(st):
                if st == 'verifying' and not restarted:
                    time.sleep(20)
                    r = subprocess.run(['python3', 'tools/acceptance/acceptance.py', 'restart', INSTANCE], cwd=ROOT, capture_output=True, text=True, timeout=300)
                    restarted['at'] = round(time.time() - log['t0'])
                    restarted['out'] = r.stdout.strip().splitlines()[-1:]
                    print(f"  +{restarted['at']:>4}s restarted {INSTANCE}: {restarted['out']}", flush=True)
            ex = follow(pid, log, on_status=on_status)
            log['restart'] = restarted
            check('process restarted while verifying', bool(restarted), json.dumps(restarted, ensure_ascii=False))
            check('execution still succeeded after the restart', ex.get('status') == 'succeeded', f"{ex.get('status')} {ex.get('status_reason')}")
            after = deployment(a.app)
            log['after'] = after
            check('written once (operation annotation = execution)', after['operation'] == ex.get('id') and not after['LAB_FAULTS'], json.dumps(after))

        elif a.drill == 'drift':
            print(f"  someone changes the Deployment: {lab('drill', 'bad-release', 'on', '--app', a.app, '--ratio', '0.3')}", flush=True)
            drifted = deployment(a.app)
            log['drifted'] = drifted
            code, tail = approve(pid)
            log['approval'] = {'code': code, 'tail': tail}
            detail, ex = execution(pid)
            if ex:
                ex = follow(pid, log)
            after = deployment(a.app)
            log['after'] = after
            refused = ex is None or ex.get('status') == 'needs_attention'
            check('drift stops the rollback (needs_attention or approval refused)', refused,
                  f"approval exit {code}; execution {(ex or {}).get('status')} {(ex or {}).get('status_reason')}; proposal {(detail or {}).get('status')}")
            check('nothing written', after['revision'] == drifted['revision'] and after['operation'] != (ex or {}).get('id'),
                  f"revision {drifted['revision']} → {after['revision']}, operation {after['operation']}")

        elif a.drill == 'write-forbidden':
            code, tail = approve(pid)
            check('two approvals', code == 0, ' | '.join(tail))
            ex = follow(pid, log)
            after = deployment(a.app)
            log['after'] = after
            check('Kubernetes refuses the write: needs_attention/write_forbidden', ex.get('status') == 'needs_attention' and ex.get('status_reason') == 'write_forbidden',
                  f"{ex.get('status')} {ex.get('status_reason')}")
            check('Deployment unchanged', after['revision'] == log['before']['revision'] and after['operation'] != ex.get('id'),
                  f"revision {log['before']['revision']} → {after['revision']}")

        elif a.drill == 'revoked':
            code, tail = approve(pid, 'approver1')
            check('first approval', code == 0, ' | '.join(tail))
            account = json.loads((LOCAL / INSTANCE / 'secrets.json').read_text())['accounts']['approver1']['email']
            mid = member_id(INSTANCE, account)
            _, access = call(INSTANCE, 'GET', f'/members/{mid}/access', env=ENV)
            keep = {r['id']: str(r['matrix_revision']) for r in access.get('roles', []) if r.get('origin') != 'legacy' and r['id'] != 'change-approver'}
            status, result = call(INSTANCE, 'PUT', f'/members/{mid}/access', {'expected_revision': access['revision'], 'role_ids': sorted(keep),
                                                                             'role_revisions': keep, 'enabled': True}, env=ENV)
            check('approver1 loses change-approver', status == 200, f'{status} {str(result)[:160]}')
            before_vote = deployment(a.app)
            code, tail = approve(pid, 'approver2')
            log['second_vote'] = {'code': code, 'tail': tail}
            detail, ex = execution(pid)
            if ex:
                ex = follow(pid, log)
            after = deployment(a.app)
            log['after'] = after
            stopped = ex is None or (ex.get('status') == 'needs_attention' and ex.get('status_reason') in ('approval_invalid', 'authorization_changed'))
            check('revoked approval stops the rollback', stopped,
                  f"second vote exit {code} {tail[-1:]}; execution {(ex or {}).get('status')} {(ex or {}).get('status_reason')}; proposal {(detail or {}).get('status')}")
            check('nothing written', after['revision'] == before_vote['revision'] and after['operation'] != (ex or {}).get('id'),
                  f"revision {before_vote['revision']} → {after['revision']}")
            r = subprocess.run(['python3', 'tools/acceptance/accounts.py', INSTANCE], cwd=ROOT, capture_output=True, text=True, timeout=300)
            check('approver1 role restored', 'approver1 ready' in r.stdout, r.stdout.strip().splitlines()[-1:] if r.stdout else r.stderr[-200:])
    finally:
        if a.drill != 'write-forbidden':
            log['off'] = lab('drill', 'bad-release', 'off', '--app', a.app)
            print(f"  bad release off: {log['off']}", flush=True)
        log['final'] = deployment(a.app)
    passed = sum(c['ok'] for c in checks.values())
    print(f'{passed}/{len(checks)} checks passed')
    out = OUT / f'rollback-edge-{a.drill}.json'
    out.write_text(json.dumps(log, ensure_ascii=False, indent=2))
    print(f'report: {out}')


if __name__ == '__main__':
    main()
