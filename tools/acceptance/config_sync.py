#!/usr/bin/env python3
"""Configuration promotion through the lab GitLab repository (docs/api/v3-config-sync.md, D35).

  python3 tools/acceptance/config_sync.py prepare|export|merge|import|all

prepare  nonprod: four objects to promote, each through its real path —
         a plan-canvas template (owner admin); a runbook from a postmortem of a manual incident,
         reviewed and published by approver2 with the knowledge reviewer role (D9, D40); an agent with
         a stable v1; the checkout-api rule group placeholder from the trusted Prometheus source.
export   nonprod: POST /promotions per kind (admin), GET /config-sync shows them exported; each MR in
         aiops-lab/redstring-config is open with one manifest file; prod refuses to import an
         unmerged MR.
merge    the lab approves (lab-maintainer, a person) and merges each MR (aiops-test d11.py config-mr).
import   prod: the operator may not list or import; admin lists them merged, imports each, a replay
         converges, the list says imported; targets: the template, the runbook as a reviewable draft
         without a body, the alert rule as a not-applied record (Prometheus unchanged), the agent paused
         at L0.
Object IDs and results go to .local/acceptance/e2e/config-sync.json.
"""
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
from golden import RESOURCES, login  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
LAB = Path.home() / 'Home/code/codex/aiops-test'
PROM = 'http://127.0.0.1:19090'
STATE = LOCAL / 'e2e/config-sync.json'
results = []


def check(label, ok, detail=''):
    results.append({'check': label, 'ok': bool(ok), 'detail': str(detail)[:400]})
    print(f"{'PASS' if ok else 'FAIL'}  {label}  {str(detail)[:220]}", flush=True)
    return ok


def np(method, path, body=None, role='admin', cookie=None):
    return call('nonprod', method, path, body, env='test', role=role, cookie=cookie, idempotency_key=secrets.token_hex(12))


def pr(method, path, body=None, role='admin', cookie=None):
    return call('prod', method, path, body, env='prod', role=role, cookie=cookie, idempotency_key=secrets.token_hex(12))


def load():
    return json.loads(STATE.read_text()) if STATE.exists() else {}


def save(state):
    state['results'] = state.get('results', []) + results
    STATE.write_text(json.dumps(state, ensure_ascii=False, indent=1))


def reviewer(instance):
    """D9 on this instance: approver2 gets a global role that manages knowledge and runbooks."""
    matrix = [{'resource': r, 'grants': [1, 0, 0, 0, 1 if r in ('knowledge', 'runbooks') else 0]} for r in RESOURCES]
    env = 'test' if instance == 'nonprod' else 'prod'
    status, body = call(instance, 'POST', '/roles', {'id': 'custom-knowledge-reviewer', 'name': '知识审核',
                                                    'description': '审核并发布复盘与 Runbook（验收 D9）',
                                                    'scopes': [{'kind': 'global', 'id': '*'}], 'matrix': matrix},
                        env=env, idempotency_key=secrets.token_hex(12))
    if status not in (200, 201, 409):
        raise SystemExit(f'create role: {status} {body}')
    accounts = json.loads((LOCAL / f'{instance}/secrets.json').read_text())['accounts']
    _, members = call(instance, 'GET', '/members', env=env)
    mid = next(m['id'] for m in members['items'] if m.get('email') == accounts['approver2']['email'])
    _, access = call(instance, 'GET', f'/members/{mid}/access', env=env)
    current = {r['id']: str(r['matrix_revision']) for r in access['roles'] if r.get('origin') == 'direct'}
    if 'custom-knowledge-reviewer' not in current:
        headers = {}
        call(instance, 'GET', '/roles/custom-knowledge-reviewer', env=env, headers_out=headers)
        current['custom-knowledge-reviewer'] = headers.get('X-Role-Matrix-Revision', '1')
        status, body = call(instance, 'PUT', f'/members/{mid}/access', {'expected_revision': access['revision'],
                            'role_ids': sorted(current), 'role_revisions': current, 'enabled': True}, env=env)
        if status != 200:
            raise SystemExit(f'assign role: {status} {body}')
    return login(instance, accounts['approver2'])


def prepare(state):
    app = json.loads((LOCAL / 'nonprod/alert-rules.json').read_text())['bindings'][0]['application_id']
    stamp = state.get('stamp') or time.strftime('%m%d%H%M')
    state.update(stamp=stamp, app=app)
    if state.get('template') and state.get('agent') and state.get('alert_rule'):
        if not state.get('runbook'):
            state['runbook'] = runbook_chain(state, app)
        return

    status, canvas = np('POST', '/canvases', {'title': f'配置提升：发布前检查 {stamp}', 'kind': 'plan', 'template_id': None,
                                              'goal': '发布前检查清单', 'services': [], 'ai_draft': False})
    status, tpl = np('POST', '/canvas-templates', {'from_canvas': (canvas or {}).get('id'), 'name': f'发布前检查模板 {stamp}',
                                                   'keep': {'nodes': True, 'steps': True, 'runbooks': False, 'content': False}})
    check('template from a plan canvas (admin owns it)', status == 201 and (tpl or {}).get('id'), f'{status} {tpl}')
    state['template'] = (tpl or {}).get('id')

    if not state.get('runbook'):
        state['runbook'] = runbook_chain(state, app)

    agent_id = f'promote-triage-{stamp}'
    np('POST', '/agents', {'id': agent_id, 'application_id': app, 'name': f'提升验收分诊 {stamp}', 'summary': '只读分诊'})
    status, _ = np('POST', f'/agents/{agent_id}/versions', {'version': 'v1', 'artifact_ref': f'git:acceptance/promote-triage@v1'})
    _, detail = np('GET', f'/agents/{agent_id}')
    status, rolled = np('POST', f'/agents/{agent_id}/rollout', {'to': 'v1', 'percent': 100, 'expected_revision': detail.get('revision'),
                                                               'override': True, 'override_reason': '验收：首次稳定发布，人工确认'})
    if status >= 300:
        # A stable rollout may need a pinned execution config first.
        _, models = np('GET', '/models')
        model_id = next((m.get('id') for m in models.get('items') or models.get('models') or [] if m.get('id')), None)
        _, prompt = np('POST', '/prompts', {'agent_id': agent_id, 'name': f'promote-{stamp}', 'text': '只根据给出的信息作答，一句话。'})
        np('PUT', f'/agents/{agent_id}/versions/v1/execution', {'model_id': model_id, 'prompt_id': (prompt or {}).get('id'), 'prompt_version': 1,
                                                                'tool_names': [], 'max_model_calls': 2, 'max_tool_calls': 0, 'max_input_bytes': 8192,
                                                                'max_output_bytes': 16384, 'max_output_tokens': 512, 'timeout_seconds': 60})
        _, detail = np('GET', f'/agents/{agent_id}')
        status, rolled = np('POST', f'/agents/{agent_id}/rollout', {'to': 'v1', 'percent': 100, 'expected_revision': detail.get('revision'),
                                                                   'override': True, 'override_reason': '验收：首次稳定发布，人工确认'})
    _, detail = np('GET', f'/agents/{agent_id}')
    check('agent with a stable v1', (detail or {}).get('status') == 'stable' and (detail or {}).get('version') == 'v1',
          f"{status} {(rolled or {}).get('error')} {(detail or {}).get('status')} {(detail or {}).get('version')}")
    state['agent'] = agent_id

    _, rules = np('GET', '/alert-rules')
    rule = next((x for x in (rules or {}).get('items') or [] if x.get('name') == 'RedstringRuleGroupPlaceholder'), {})
    check('alert rule from the trusted Prometheus group', rule.get('id'), rule.get('id'))
    state['alert_rule'] = rule.get('id')


def runbook_chain(state, app):
    """A runbook belongs to an application only when drafted from a published postmortem, which needs an
    incident, which needs an alert: an acceptance rule fires in the test environment and reaches nonprod
    through the narrow lab route (D41); it is disabled again as soon as the incident exists."""
    r = subprocess.run(['python3', 'tools/acceptance/alert_rule_loop.py', 'nonprod', '--keep'], cwd=ROOT, capture_output=True, text=True, timeout=1200)
    name = next((l.split('"name": "')[1].split('"')[0] for l in r.stdout.splitlines() if '"name": "RedstringAcceptance' in l), None)
    name = name or json.loads((LOCAL / 'e2e/drills/alert-rule-nonprod.json').read_text()).get('name')
    check('acceptance rule enabled in the test environment', 'execution reaches enabled' in r.stdout and 'PASS  execution reaches enabled' in r.stdout, name)
    alert, started = None, time.time()
    while time.time() - started < 420 and not alert:
        _, inbox = np('GET', '/inbox', role='operator')
        alert = next((x.get('alert_id') for x in (inbox or {}).get('items') or [] if x.get('title') == name), None)
        if not alert:
            time.sleep(15)
    check('its alert reaches nonprod (D41)', alert, f'{alert} after {round(time.time() - started)}s')
    inc_id = None
    try:
        if alert:
            _, draft = np('GET', f'/incidents/draft?alerts={alert}', role='operator')
            status, inc = np('POST', '/incidents', {'title': (draft or {}).get('title') or f'验收：{name}', 'severity': (draft or {}).get('severity') or 'P3',
                                                    'impact': '验收用规则告警，无真实影响', 'env': 'test', 'services': [app], 'alert_ids': [alert],
                                                    'war_room': False, 'notify': False, 'status_page': False, 'open_canvas': False,
                                                    'participants': ['Operator']}, role='operator')
            inc_id = (inc or {}).get('id')
            check('incident declared from the alert (operator)', status in (200, 201) and inc_id, f'{status} {inc_id} {(inc or {}).get("error")}')
    finally:
        d = subprocess.run(['python3', 'tools/acceptance/alert_rule_loop.py', 'nonprod', '--disable', name], cwd=ROOT, capture_output=True, text=True, timeout=1200)
        check('acceptance rule disabled again', 'PASS  disable execution reaches disabled' in d.stdout, name)
    if not inc_id:
        return None
    _, detail = np('GET', f'/incidents/{inc_id}', role='operator')
    revision = detail.get('status_revision', detail.get('revision'))
    for target in ('mitigated', 'resolved'):
        status, changed = np('POST', f'/incidents/{inc_id}/status', {'status': target, 'expected_revision': revision,
                             'request_id': secrets.token_hex(16), 'reason': '验收：规则已停用'}, role='operator')
        revision = (changed or {}).get('revision', revision + 1 if isinstance(revision, int) else revision)
    check('incident resolved', status in (200, 201), f'{status} {(changed or {}).get("error")}')
    _, draft = np('GET', f'/postmortems/draft?incident={inc_id}')
    sections = [dict(sec, text=sec.get('text') or f"（作者补充）{sec.get('title')}：验收规则告警，已停用。")
                for sec in (draft or {}).get('sections') or []]
    status, pm = np('POST', '/postmortems', {'incident_id': inc_id, 'source_revision': (draft or {}).get('source_revision'),
                                             'sections': sections, 'actions': (draft or {}).get('actions') or [], 'status': 'review'})
    pm_id = (pm or {}).get('id')
    check('postmortem submitted for review (admin)', status in (200, 201) and pm_id, f'{status} {(pm or {}).get("error")}')
    rev = reviewer('nonprod')
    status, published = np('POST', f'/postmortems/{pm_id}/publish', {}, cookie=rev)
    check('postmortem published by the reviewer (approver2)', status == 200, f'{status} {(published or {}).get("error")}')
    status, rb_draft = np('GET', f'/runbooks/draft?source=postmortem&from={pm_id}')
    check('runbook draft from the postmortem', status == 200, f'{status} {(rb_draft or {}).get("error")}')
    steps = (rb_draft or {}).get('steps') or [{'n': 1, 'kind': 'check', 'title': '确认 5xx 比例与开始时间', 'readonly': True}]
    status, rb = np('POST', '/runbooks', {'title': f"checkout-api 告警处置 {state['stamp']}", 'source': 'postmortem', 'from': pm_id,
                                          'expected_source_sha256': (rb_draft or {}).get('source_sha256'), 'steps': steps, 'status': 'draft'})
    rb_id = (rb or {}).get('id')
    _, rb_detail = np('GET', f'/runbooks/{rb_id}')
    check('runbook belongs to checkout-api', status == 201 and (rb_detail or {}).get('application_id') == app,
          f"{status} application={(rb_detail or {}).get('application_id')!r}")
    np('POST', f'/runbooks/{rb_id}/submit', {})
    status, own = np('POST', f'/runbooks/{rb_id}/reviews', {'decision': 'approve', 'note': '自审'})
    check('the maintainer cannot approve their own runbook', status in (403, 409, 422), f'{status} {(own or {}).get("error")}')
    status, review = np('POST', f'/runbooks/{rb_id}/reviews', {'decision': 'approve', 'note': '验收：步骤只读，可以发布'}, cookie=rev)
    _, rb_detail = np('GET', f'/runbooks/{rb_id}')
    check('runbook published by the reviewer', status in (200, 201) and (rb_detail or {}).get('status') == 'published',
          f"{status} {(review or {}).get('error')} status={(rb_detail or {}).get('status')}")
    return rb_id


def gitlab(path):
    """The config repository is private: read it as the nonprod exporter does (token, proxy, CA)."""
    import ssl
    token = (LOCAL / 'config-sync/gitlab-nonprod.token').read_text().strip()
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/alertmanager.pem'))
    req = urllib.request.Request(f'https://127.0.0.1:18446/api/v4/projects/7{path}', headers={'PRIVATE-TOKEN': token})
    with urllib.request.urlopen(req, context=ctx, timeout=15) as r:
        return json.load(r)


def pin_agent(state):
    """PA-99: an agent whose stable artifact is not pinned to a digest is refused with a reason; a pinned
    v2 made stable is then exported."""
    status, out = np('POST', '/promotions', {'kind': 'agent_version', 'id': state['agent']})
    if status == 200:
        return
    check('an unpinned agent artifact is refused with artifact_ref_not_pinned (PA-99)',
          status == 422 and ((out or {}).get('error') or {}).get('code') == 'artifact_ref_not_pinned', f'{status} {(out or {}).get("error")}')
    import hashlib
    digest = hashlib.sha256(f"acceptance agent artifact {state['agent']} v2".encode()).hexdigest()
    np('POST', f"/agents/{state['agent']}/versions", {'version': 'v2', 'artifact_ref': f'aiops-lab/agents/promote-triage@sha256:{digest}'})
    _, detail = np('GET', f"/agents/{state['agent']}")
    np('POST', f"/agents/{state['agent']}/rollout", {'to': 'v2', 'percent': 100, 'expected_revision': detail.get('revision'),
                                                     'override': True, 'override_reason': '验收：固定摘要的版本设为稳定'})


def export(state):
    pin_agent(state)
    state['exports'] = {}
    for kind, key in (('template', 'template'), ('runbook', 'runbook'), ('alert_rule', 'alert_rule'), ('agent_version', 'agent')):
        status, out = np('POST', '/promotions', {'kind': kind, 'id': state[key]})
        check(f'export {kind}', status == 200 and (out or {}).get('mr'), f'{status} {out}')
        state['exports'][kind] = out or {}
    status, again = np('POST', '/promotions', {'kind': 'template', 'id': state['template']})
    check('exporting the same template again reuses its MR', (again or {}).get('mr') == state['exports']['template'].get('mr'), f'{status} {again}')
    status, listing = np('GET', '/config-sync')
    exported = {i.get('kind'): i for i in (listing or {}).get('items') or []}
    check('nonprod lists the four as exported', listing.get('direction') == 'export' and
          all(exported.get(k, {}).get('status') in ('exported', 'pending') for k in state['exports']),
          {k: v.get('status') for k, v in exported.items()})
    for kind, out in state['exports'].items():
        iid = str(out.get('mr', '')).split('-')[-1].lstrip('!')
        try:
            mr = gitlab(f'/merge_requests/{iid}')
            changes = gitlab(f'/merge_requests/{iid}/changes')
            files = [c['new_path'] for c in changes.get('changes') or []]
            check(f'{kind}: MR !{iid} open on main with one manifest file', mr.get('state') == 'opened' and mr.get('target_branch') == 'main'
                  and mr.get('source_branch', '').startswith('aiops/promotions/') and files == [out.get('path')], f"{mr.get('state')} {files}")
            out['iid'] = int(iid)
        except (OSError, ValueError) as e:
            check(f'{kind}: MR readable', False, e)
    first = next(iter(state['exports'].values()))
    status, body = pr('POST', f"/config-sync/mr-{first.get('iid')}/import", {})
    check('prod refuses to import an unmerged MR', status in (404, 409, 422, 503), f"{status} {(body or {}).get('error')}")


def merge(state):
    for kind, out in state['exports'].items():
        r = subprocess.run(['python3', 'delivery/d11.py', 'config-mr', str(out['iid'])], cwd=LAB, capture_output=True, text=True, timeout=300)
        last = (r.stdout.strip().splitlines() or [''])[-1]
        check(f'{kind}: lab-maintainer approves and merges !{out["iid"]}', r.returncode == 0, last[:200])


def import_(state):
    status, _ = pr('GET', '/config-sync', role='operator')
    check('the operator may not list imports', status in (403, 404), status)
    before = prom_group('redstring-prod-checkout-api')
    status, listing = pr('GET', '/config-sync')
    merged = {i.get('id'): i for i in (listing or {}).get('items') or []}
    check('prod lists the four as merged', listing.get('direction') == 'import' and
          all(merged.get(f"mr-{o['iid']}", {}).get('status') == 'merged' for o in state['exports'].values()),
          {k: v.get('status') for k, v in merged.items()})
    first = next(iter(state['exports'].values()))
    status, body = pr('POST', f"/config-sync/mr-{first['iid']}/import", {}, role='operator')
    check('the operator may not import', status in (403, 404), status)
    for kind, out in state['exports'].items():
        status, item = pr('POST', f"/config-sync/mr-{out['iid']}/import", {})
        check(f'import {kind}', status == 200 and (item or {}).get('status') == 'imported', f"{status} {item}")
        status, again = pr('POST', f"/config-sync/mr-{out['iid']}/import", {})
        check(f'{kind}: a replay converges', status == 200 and (again or {}).get('status') == 'imported', f"{status} {(again or {}).get('error')}")
    status, listing = pr('GET', '/config-sync')
    check('prod lists the four as imported', all(i.get('status') == 'imported' for i in (listing or {}).get('items') or []
                                                 if i.get('id') in {f"mr-{o['iid']}" for o in state['exports'].values()}),
          [(i.get('kind'), i.get('status')) for i in (listing or {}).get('items') or []])
    _, agent = pr('GET', f"/agents/{state['agent']}")
    check('the agent arrives paused at L0', agent and agent.get('level') == 'L0' and agent.get('status') == 'paused',
          {k: (agent or {}).get(k) for k in ('status', 'level', 'version')})
    _, rbs = pr('GET', '/runbooks')
    rb = next((x for x in (rbs or {}).get('items') or [] if state['stamp'] in (x.get('title') or '')), None)
    check('the runbook arrives as a reviewable draft', rb and rb.get('status') in ('draft', 'review'), rb)
    after = prom_group('redstring-prod-checkout-api')
    check('the alert rule is recorded, not applied: prod Prometheus unchanged', before == after, f'{len(before)} rules before and after')


def prom_group(name):
    with urllib.request.urlopen(f'{PROM}/api/v1/rules', timeout=10) as r:
        groups = json.load(r)['data']['groups']
    return sorted(x['name'] for g in groups if g['name'] == name for x in g['rules'])


def main():
    step = sys.argv[1] if len(sys.argv) > 1 else 'all'
    state = load() if step != 'all' else {}
    try:
        for name, fn in (('prepare', prepare), ('export', export), ('merge', merge), ('import', import_)):
            if step in (name, 'all'):
                print(f'== {name}', flush=True)
                fn(state)
    finally:
        save(state)
        print(f"{sum(r['ok'] for r in results)}/{len(results)} checks passed  state: {STATE}")
    sys.exit(0 if all(r['ok'] for r in results) else 1)


if __name__ == '__main__':
    main()
