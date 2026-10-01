"""Read-only, source-bound acceptance overview; plans and inventory are not proof."""
from __future__ import annotations

import collections
import datetime as dt
import html
import json
from pathlib import Path
import re

from runner import git_value, source_fingerprint

SCENARIOS = ('normal', 'authorization', 'boundary', 'idempotency', 'failure')
AVAILABLE = ('local_ready', 'local_environment', 'external_pending', 'not_implemented', 'user_paused', 'needs_confirmation')


def _read(path, default):
    if not path.is_file() or path.is_symlink():
        return default
    return json.loads(path.read_text(encoding='utf-8'))


def _path(value):
    return value if value.startswith('/api/v1/') else '/api/v1' + value


def _key(row):
    return row.get('method', '').upper(), _path(row.get('path', ''))


def _actual(case):
    # The display field is deliberately truncated to 8 KiB by Store. Never infer
    # acceptance from its text; only the registered original JSON item is usable.
    return case.get('_verified_actual', {})


def _original_actual(store, case):
    for ident in reversed(case.get('_evidence_ids', [])):
        try:
            raw, _ = store.read_evidence(ident)
            item = json.loads(raw.decode('utf-8'))
            if (isinstance(item, dict) and item.get('name') == case['name']
                    and item.get('status') == case['status'] and isinstance(item.get('actual'), dict)):
                return item['actual'], ident
        except (OSError, KeyError, UnicodeError, ValueError, RecursionError):
            continue
    return {}, None


def _binding(run, sha):
    if not re.fullmatch('[0-9a-f]{64}', sha or ''):
        return 'unknown'
    if run.get('source_tree_sha') != sha:
        return 'stale' if run.get('source_tree_sha') else 'unknown'
    if run.get('status') == 'interrupted':
        return 'interrupted'
    if run.get('source_tree_sha_end') != sha:
        return 'unknown_end'
    if run.get('status') in ('queued', 'running'):
        return 'in_progress'
    return 'current'


def _proof(run, record, environment, evidence_ids=()):
    return {'run_id': run['id'], 'record_id': record.get('id'),
            'source_tree_sha': run.get('source_tree_sha'), 'source_tree_sha_end': run.get('source_tree_sha_end'),
            'git_sha': run.get('sha'), 'environment': environment,
            'evidence_ids': list(evidence_ids), 'evidence_mode': record.get('evidence_mode'),
            'proof_scope': record.get('proof_scope') or record.get('note', ''),
            'upstream_mode': record.get('upstream_mode'), 'run_status': run['status']}


def _package_acceptance(package, runs, cases, source):
    required = package.get('required_cases')
    if not isinstance(required, list) or not required or any(not isinstance(r, dict) or not r.get('suite') or not r.get('name') for r in required):
        return {'status': 'needs_scenario_plan', 'reason': '尚未登记此包完整验收所需的稳定 suite/name 场景列表。', 'evidence': []}
    keys = {(r['suite'], r['name']) for r in required}
    candidates = [r for r in runs if r.get('source_tree_sha') == source and any((c.get('_suite_key'), c['name']) in keys for c in cases.get(r['id'], []))]
    if not candidates:
        return {'status': 'not_started', 'reason': '当前源码尚无对应验收场景运行。', 'evidence': []}
    latest = max(candidates, key=lambda r: r.get('started_at', ''))
    rows = [c for c in cases.get(latest['id'], []) if c.get('_suite_key') in {k[0] for k in keys}]
    matching = {(c.get('_suite_key'), c['name']): c for c in rows}
    allowed = set(package.get('allowed_case_statuses', ['passed'])) & {'passed', 'external_todo'}
    complete = (_binding(latest, source) == 'current' and all(k in matching and matching[k]['status'] == 'passed' and matching[k].get('_evidence_ids') for k in keys)
                and all(c['status'] in allowed and c.get('_evidence_ids') for c in rows))
    evidence = [{'run_id': latest['id'], 'source_tree_sha': latest.get('source_tree_sha'), 'source_tree_sha_end': latest.get('source_tree_sha_end'),
                 'source_binding': _binding(latest, source), 'suite': k[0], 'name': k[1],
                 'status': matching[k]['status'] if k in matching else 'missing',
                 'evidence_ids': matching[k].get('_evidence_ids', []) if k in matching else []} for k in sorted(keys)]
    blockers = [{'run_id': latest['id'], 'case_id': c['id'], 'name': c['name'], 'status': c['status'],
                 'reason': _actual(c).get('reason', ''), 'evidence_ids': c.get('_evidence_ids', [])}
                for c in rows if c['status'] not in allowed or not c.get('_evidence_ids')]
    return {'status': 'passed' if complete else 'incomplete', 'reason': '仅此包显式必需场景和同组结果；外部待办不计通过，也不推广为全部功能空间。',
            'evidence': evidence, 'blocking_cases': blockers,
            'external_todos': [{'run_id': latest['id'], 'case_id': c['id'], 'name': c['name'], 'evidence_ids': c.get('_evidence_ids', [])} for c in rows if c['status'] == 'external_todo']}


def _gates(runs, cases, source):
    gates = [{'id': 'functional', 'title': '功能、逻辑与权限覆盖', 'status': 'incomplete',
              'reason': '正常 HTTP 成功证据仅证明相应场景；边界、权限和故障闭环仍须逐项核验。', 'evidence': []}]
    full = []
    for run in runs:
        selected = set(run.get('selected_suite', '').split(','))
        if selected == {'full'} or {'baseline', 'mfa', 'passkey', 'configsync', 'regressions'} <= selected:
            rows = cases.get(run['id'], [])
            full.append({'run_id': run['id'], 'source_tree_sha': run.get('source_tree_sha'),
                         'binding': _binding(run, source), 'counts': dict(collections.Counter(c['status'] for c in rows)),
                         'evidence_complete': bool(rows) and all(c.get('_evidence_ids') for c in rows),
                         'ended_at': run.get('ended_at'), 'status': run['status']})
    current = [r for r in full if r['binding'] == 'current']
    regression_passed = any(r['status'] == 'passed' and r['evidence_complete'] and set(r['counts']) <= {'passed'} and r['counts'].get('passed', 0) > 0 for r in current)
    gates.append({'id': 'regression', 'title': '当前源码完整回归',
                  'status': 'passed' if regression_passed else 'incomplete' if current else 'stale' if full else 'not_started',
                  'reason': '完整回归的跳过项还需专项开关与跳过原因核对；历史回归不能替代当前源码验收。', 'evidence': current, 'history': full})
    expected = {'warmup': (10, 120), 'rps10': (10, 300), 'rps25': (25, 300), 'rps50': (50, 300), 'burst100': (100, 60)}
    profiles = {'base': [], 'scale': []}
    soak = []
    for run in runs:
        if _binding(run, source) != 'current':
            continue
        rows = cases.get(run['id'], [])
        if 'diagnos' not in run.get('selected_suite', '') and run['status'] == 'passed':
            for profile in profiles:
                stages = {}
                scoped = []
                active_profile = None
                for case in rows:
                    actual = _actual(case)
                    if case['name'] == 'perf.seed':
                        active_profile = actual.get('profile')
                    if actual.get('profile', active_profile) != profile:
                        continue
                    scoped.append(case)
                    stage = actual.get('stage')
                    if stage in expected and case['status'] == 'passed' and case.get('_evidence_ids'):
                        rate, seconds = expected[stage]
                        if (actual.get('rate_rps') == rate and actual.get('planned_seconds') == seconds
                                and actual.get('generator_seconds', 0) >= seconds
                                and actual.get('full_aggregate_scope_accepted') is True
                                and actual.get('k6_exit') == 0):
                            stages[stage] = {'case_id': case['id'], 'evidence_id': case.get('_actual_evidence_id')}
                checks = {c['name'] for c in scoped if c['status'] == 'passed' and c.get('_evidence_ids')}
                if set(stages) == set(expected) and {'perf.sse', 'perf.resource_recovery', 'perf.data_consistency'} <= checks:
                    profiles[profile].append({'run_id': run['id'], 'source_tree_sha': source, 'stage_cases': stages})
        for case in rows:
            if case['name'] != 'perf.soak_acceptance' or case['status'] != 'passed' or run['status'] != 'passed' or not case.get('_evidence_ids'):
                continue
            actual = _actual(case)
            hooks = actual.get('hooks', {})
            trial = actual.get('trial_final', {})
            sse = actual.get('sse', {})
            if (actual.get('generator_seconds', 0) >= 86400 and actual.get('planned_seconds') == 86400
                    and actual.get('rate_rps') == 10 and actual.get('full_aggregate_scope_accepted') is True
                    and trial.get('accepted') is True and trial.get('evaluated_points') == 1440 and trial.get('missing_points') == 0
                    and sse.get('stable_samples', 0) > 0 and sse.get('normal_frames', 0) > 0 and sse.get('slow_bytes', 0) > 0
                    and sse.get('peak_normal') == 2 and sse.get('peak_slow') == 1 and sse.get('reconnects', 0) > 0
                    and not any(sse.get(k, 0) for k in ('window_violations', 'protocol_errors', 'producer_errors'))
                    and not hooks.get('failures')
                    and all(hooks.get(k, 0) > 0 for k in ('dryrun_calls', 'webhook_calls', 'chat_turns', 'relogins'))):
                soak.append({'run_id': run['id'], 'source_tree_sha': source, 'case_id': case['id'],
                             'evidence_id': case.get('_actual_evidence_id'), 'seconds': actual['generator_seconds']})
    for profile, title in [('base', '完整基础档性能'), ('scale', '完整规模档性能')]:
        relevant = [r for r in runs if 'performance' in r.get('selected_suite', '')]
        gates.append({'id': 'performance_' + profile, 'title': title, 'status': 'passed' if profiles[profile] else 'incomplete' if relevant else 'not_started',
                      'reason': '同源码、同档位五阶段、原阈值、SSE、恢复和一致性均须完成；诊断或部分阶段不计完整验收。', 'evidence': profiles[profile]})
    started = any('soak' in r.get('selected_suite', '') for r in runs)
    gates.append({'id': 'soak', 'title': '连续真实 24 小时验收', 'status': 'passed' if soak else 'incomplete' if started else 'not_started',
                  'reason': '必须同一冻结窗口连续 86400 秒，并完成规则、Webhook、对话重连和真实续登；中断不累计。', 'evidence': soak})
    return gates


def get_overview(root, store, source_sha=None):
    root = Path(root).resolve()
    source = source_fingerprint(root) if source_sha is None else source_sha
    # One SQLite snapshot keeps event sequence and the evidence counts coherent.
    with store.connect() as conn:
        conn.execute('BEGIN')
        runs = [json.loads(r[0]) for r in conn.execute('SELECT data FROM runs ORDER BY rowid DESC')]
        records = [(r[0], r[1], json.loads(r[2])) for r in conn.execute("SELECT run_id,kind,data FROM records WHERE (kind='interfaces' AND COALESCE(json_extract(data,'$.evidence_mode'),'')!='source') OR kind='defects' OR (kind='environment' AND json_extract(data,'$.kind')='testlab') ORDER BY rowid")]
        suites = {r[0]: json.loads(r[1]).get('suite_key') for r in conn.execute('SELECT id,data FROM suites')}
        cases = collections.defaultdict(list)
        for run_id, raw in conn.execute('SELECT run_id,data FROM cases ORDER BY rowid'):
            cases[run_id].append(json.loads(raw))
        attempts = collections.defaultdict(list)
        for case_id, raw in conn.execute('SELECT case_id,data FROM attempts ORDER BY rowid'):
            attempts[case_id].extend(json.loads(raw).get('evidence_ids', []))
        registered = {r[0]: (r[1], r[2]) for r in conn.execute('SELECT id,run_id,filename FROM evidence')}
        case_for_record = {}
        latest_case = {}
        for run_id, kind, raw in conn.execute("SELECT run_id,type,data FROM events WHERE type='case' OR (type='interfaces' AND COALESCE(json_extract(data,'$.evidence_mode'),'')!='source') ORDER BY id"):
            row = json.loads(raw)
            if kind == 'case':
                latest_case[run_id] = row.get('id')
            elif row.get('evidence_mode') != 'source':
                case_for_record[row.get('id')] = latest_case.get(run_id)
        sequence = conn.execute('SELECT COALESCE(MAX(id),0) FROM events').fetchone()[0]
    run_map = {r['id']: r for r in runs}
    valid_evidence = {}
    def evidence_exists(ident, rid):
        key = (ident, rid)
        if key not in valid_evidence:
            row = registered.get(ident)
            path = store.evidence_dir / (ident + '.txt')
            valid_evidence[key] = bool(row and row == (rid, ident + '.txt') and re.fullmatch('[0-9a-f-]{36}', ident)
                                       and not path.is_symlink() and path.is_file())
        return valid_evidence[key]
    for group in cases.values():
        for case in group:
            case['_evidence_ids'] = [e for e in attempts.get(case['id'], []) if evidence_exists(e, case['run_id'])]
            case['_suite_key'] = suites.get(case['suite_id'])
            if case['name'].startswith('perf.') or case['status'] not in ('passed', 'queued', 'running'):
                case['_verified_actual'], case['_actual_evidence_id'] = _original_actual(store, case)
    contract = _read(root / 'internal/apicontract/openapi.json', {'paths': {}})
    modules = {}
    for path, methods in contract.get('paths', {}).items():
        for method, operation in methods.items():
            if isinstance(operation, dict):
                modules[(method.upper(), _path(path))] = operation.get('tags', ['未分类'])[0]
    main_file = root / 'docs/api/v3-contract.md'
    main = set((m, _path(p)) for m, p in re.findall(r'^\| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \|', main_file.read_text() if main_file.is_file() else '', re.M))
    plans = _read(root / 'tools/testlab/acceptance-plan.json', {})
    plan_routes = {_key(p): p for p in plans.get('route_plans', [])}
    environments = {}
    for rid, kind, row in records:
        if kind == 'environment' and row.get('kind') == 'testlab':
            environments[rid] = row
    routes = {key: {'method': key[0], 'path': key[1], 'module': modules.get(key, '未分类'), 'normal_status': 'missing',
                    'scenario_statuses': {s: 'missing' for s in SCENARIOS}, 'evidence': [], 'failed_evidence': [],
                    'protocol_normal_status': 'missing', 'protocol_scenario_statuses': {s: 'missing' for s in SCENARIOS},
                    'protocol_evidence': [], 'protocol_failed_evidence': [],
                    'availability': plan_routes.get(key, {}).get('availability', 'needs_confirmation'),
                    'priority': plan_routes.get(key, {}).get('priority', 'P1'), 'package_id': plan_routes.get(key, {}).get('package_id'),
                    'reason': plan_routes.get(key, {}).get('reason', '尚未分类；需要核对本地测试条件。'),
                    'proof_scope': plan_routes.get(key, {}).get('proof_scope', '正式后端 HTTP 子场景；上游Mock不证明真实外部能力。'),
                    'prerequisites': plan_routes.get(key, {}).get('prerequisites', [])} for key in main}
    auxiliary = collections.defaultdict(list)
    rejected = collections.Counter()
    case_map = {c['id']: c for group in cases.values() for c in group}
    for rid, kind, row in records:
        if kind != 'interfaces' or row.get('evidence_mode') == 'source':
            continue
        run = run_map.get(rid, {})
        binding = _binding(run, source)
        if binding != 'current':
            rejected[binding] += 1
            continue
        protocol = row.get('evidence_mode') == 'protocol_mock' or row.get('upstream_mode') == 'protocol_mock'
        if row.get('evidence_mode') != 'real_local' and not protocol:
            rejected['mock_or_non_http'] += 1
            continue
        if protocol:
            rejected['protocol_not_real_acceptance'] += 1
        key = _key(row)
        case_id = case_for_record.get(row.get('id'))
        evidence_ids = case_map.get(case_id, {}).get('_evidence_ids', [])
        if not evidence_ids:
            rejected['unlinked_or_missing_original_evidence'] += 1
        proof = _proof(run, row, environments.get(rid, {'status': 'unknown'}), evidence_ids)
        for scenario, outcome in row.get('scenarios', {}).items():
            if scenario not in SCENARIOS or not isinstance(outcome, dict):
                continue
            code = outcome.get('actual_status')
            wanted = outcome.get('expected_status')
            expected = wanted if isinstance(wanted, list) else [wanted]
            actual_case = case_map.get(case_id, {})
            passed = (outcome.get('status') == 'passed' and type(code) is int and code in expected
                      and actual_case.get('status') == 'passed' and bool(evidence_ids))
            if scenario == 'normal':
                passed = passed and 200 <= code < 300 and row.get('registration_evidence') is True
            if key in routes:
                target = routes[key]
                entry = {**proof, 'scenario': scenario, 'actual_status': code, 'status': outcome.get('status')}
                if passed:
                    target['protocol_scenario_statuses' if protocol else 'scenario_statuses'][scenario] = 'verified'
                    target['protocol_evidence' if protocol else 'evidence'].append(entry)
                    if scenario == 'normal':
                        target['protocol_normal_status' if protocol else 'normal_status'] = 'verified'
                elif outcome.get('status') == 'failed':
                    target['protocol_failed_evidence' if protocol else 'failed_evidence'].append(entry)
            elif scenario == 'normal' and passed and not protocol:
                auxiliary[key].append({**proof, 'scenario': scenario, 'actual_status': code, 'status': outcome.get('status')})
    ordered = sorted(routes.values(), key=lambda r: (r['priority'], r['module'], r['path'], r['method']))
    missing = [r for r in ordered if r['normal_status'] == 'missing']
    classified = dict(collections.Counter(r['availability'] for r in missing))
    packages = []
    for package in plans.get('packages', []):
        assigned = [r for r in ordered if r['package_id'] == package['id']]
        covered = sum(r['normal_status'] == 'verified' for r in assigned)
        normal_status = 'none' if covered == 0 else 'passed' if covered == len(assigned) else 'partial'
        scenario = _package_acceptance(package, runs, cases, source)
        package_status = 'passed' if normal_status == 'passed' and scenario['status'] == 'passed' else scenario['status'] if scenario['status'] == 'needs_scenario_plan' else 'incomplete'
        focused = []
        for check in package.get('focused_checks', []):
            keys = set(check.get('route_keys', []))
            selected = [r for r in assigned if r['method'] + ' ' + r['path'] in keys]
            mode = 'protocol_normal_status' if check.get('evidence_mode') == 'protocol_mock' else 'normal_status'
            check_scenarios = _package_acceptance(check, runs, cases, source)
            check_covered = sum(r[mode] == 'verified' for r in selected)
            check_complete = bool(keys) and check_covered == len(selected) == len(keys) and check_scenarios['status'] == 'passed'
            focused.append({**check, 'scenario_acceptance': check_scenarios,
                            'acceptance_status': 'passed' if check_complete else 'incomplete',
                            'normal_covered': check_covered, 'normal_total': len(keys),
                            'scope': '有界子包证明，不能替代父工作包或真实外部验收。'})
        packages.append({**package, 'normal_total': len(assigned), 'normal_covered': covered,
                         'normal_samples': {'status': normal_status, 'covered': covered, 'total': len(assigned)},
                         'scenario_acceptance': scenario, 'acceptance_status': package_status,
                         'focused_checks': focused,
                         'acceptance_reason': scenario['reason'],
                         'scope': '正常成功路径子场景；计划状态不代表完整测试包通过。'})
    gaps = _read(root / 'tools/testlab/gaps.json', [])
    for gap in gaps:
        # Narrative closure in the source ledger remains narrative, never promoted to a passing test.
        linked = []
        for rid, group in cases.items():
            if _binding(run_map[rid], source) != 'current':
                continue
            matching = [c for c in group if gap['id'] in c.get('gap_ids', [])]
            if matching:
                linked.append({'run_id': rid, 'source_tree_sha': source, 'counts': dict(collections.Counter(c['status'] for c in matching)),
                               'scope': '用例关联包含公共夹具，不能据数量认定缺口关闭。'})
        gap.update(acceptance_status='unassessed', acceptance_reason='缺少逐子场景的明确收口判定规则；台账叙述与宽泛用例关联不能自动关闭。', current_case_groups=linked,
                   completed_evidence_binding='historical_narrative_unverified')
    history = [{**r, 'source_binding': _binding(r, source), 'counts': dict(collections.Counter(c['status'] for c in cases.get(r['id'], [])))} for r in runs]
    defects = _read(root / 'tools/testlab/defects.json', [])
    defect_map = {d['id']: d for d in defects}
    for rid, kind, record in records:
        if kind != 'defects':
            continue
        defect = defect_map.setdefault(record.get('id'), dict(record))
        evidence_ids = [e for e in record.get('evidence_ids', []) if evidence_exists(e, rid)]
        if evidence_ids:
            run = run_map[rid]
            defect.setdefault('evidence_history', []).append({'run_id': rid, 'source_tree_sha': run.get('source_tree_sha'),
                'source_binding': _binding(run, source), 'evidence_ids': evidence_ids,
                'original_claim': record.get('first_failure_source', record.get('description', '')),
                'scope': '本运行附载的原证据；附载源码指纹不是首次失败或修复复验的源码证明。'})
    defects = list(defect_map.values())
    for defect in defects:
        defect['record_scope'] = '修复台账；是否通过当前源码回归须查看对应运行证据。'
    gates = _gates(runs, cases, source)
    functional_status = 'incomplete' if missing else 'passed' if packages and all(p['acceptance_status'] == 'passed' for p in packages) else 'needs_scenario_plan'
    gates[0].update(status=functional_status,
                    reason='当前正常样本与所有已列工作包的明确必需场景均满足；仅对应所列范围，仍需其他验收门槛。' if functional_status == 'passed'
                    else f'仍缺 {len(missing)} 个正式 HTTP 正常成功子场景。样本覆盖不能替代权限、边界和故障闭环。' if missing
                    else '正常 HTTP 样本已覆盖；功能、逻辑、权限和故障的综合判定规则尚未登记，需要补齐场景验收计划。')
    return {'schema_version': 1, 'generated_at': dt.datetime.now(dt.timezone.utc).isoformat(), 'event_sequence': sequence,
            'overall_status': 'ready_for_review' if gates and all(g['status'] == 'passed' for g in gates) else 'open', 'source': {'fingerprint': source, 'known': bool(re.fullmatch('[0-9a-f]{64}', source or '')),
            'git_sha': git_value(root, 'rev-parse', 'HEAD'), 'branch': git_value(root, 'branch', '--show-current')},
            'coverage': {'main': {'total': len(main), 'normal_success': len(main) - len(missing), 'missing': len(missing),
                                 'scope': '当前源码、正式 HTTP 正常成功子场景；不等于完整接口验收或项目完成率。'},
                         'auxiliary': {'normal_success': len(auxiliary), 'routes': [{'method': m, 'path': p, 'evidence': auxiliary[(m, p)]} for m, p in sorted(auxiliary)]},
                         'protocol_mock': {'normal_success': sum(r['protocol_normal_status'] == 'verified' for r in ordered),
                                           'scope': '当前正式 HTTP 与 PG 的协议 Mock 子场景；不计真实主接口正常覆盖或真实策略强制验收。'},
                         'routes': ordered, 'excluded_record_counts': dict(rejected)},
            'runs': {'active': [r for r in history if r['status'] in ('running', 'queued')], 'history': history,
                     'source_binding_counts': dict(collections.Counter(r['source_binding'] for r in history))},
            'gates': gates, 'work_packages': packages,
            'local_queue': [r for r in missing if r['availability'] in ('local_ready', 'local_environment')],
            'execution_queue': [r for r in missing if r['availability'] in ('local_ready', 'local_environment') and r['protocol_normal_status'] != 'verified'],
            'protocol_verified_pending_real': [r for r in missing if r['protocol_normal_status'] == 'verified'],
            'blockers': {'counts': classified, 'routes': [r for r in missing if r['availability'] not in ('local_ready', 'local_environment')]},
            'next_actions': plans.get('next_actions', []), 'gaps': gaps, 'defects': defects,
            'external_todos': plans.get('external_todos', []), 'paused_todos': plans.get('paused_todos', []),
            'plan': {'schema_version': plans.get('schema_version', plans.get('version')), 'source_tree_sha': plans.get('source_tree_sha'),
                     'binding': 'current' if plans.get('source_tree_sha') == source else 'stale_or_unknown'},
            'limitations': ['没有统一整体完成率：不同验收门槛不能用库存格数或单次运行通过率相加。',
                            '单元、拒绝路径、源码库存、协议 Mock 和历史版本证据分开保留。',
                            '51 项台账描述保留；缺少源码绑定的叙述未自动升级为当前通过。']}


def render_overview_report(data, fmt='json'):
    if fmt == 'json':
        return json.dumps(data, ensure_ascii=False, indent=2)
    if fmt not in ('md', 'html'):
        raise ValueError('unsupported overview report format')
    source = data['source']
    coverage = data['coverage']['main']
    lines = ['# AIOps 后端整体验收报告', '', '整体状态：' + data['overall_status'],
             '生成时间：' + data['generated_at'], '源码指纹：' + str(source['fingerprint']),
             f"主接口正常 HTTP 子场景：{coverage['normal_success']} / {coverage['total']}；缺 {coverage['missing']} 项。",
             f"协议 Mock 正常子场景：{data['coverage'].get('protocol_mock', {}).get('normal_success', 0)} 个主接口；不计上述真实覆盖。",
             f"尚需本地正常路径补测：{len(data.get('execution_queue', []))} 项；Mock正常已验、真实来源待验：{len(data.get('protocol_verified_pending_real', []))} 项。两者均未视为完整接口验收。",
             coverage['scope'], '', '## 验收门槛']
    lines.extend(f"- {g['title']}：{g['status']}。{g['reason']}" for g in data['gates'])
    lines.extend(['', '## 优先行动'])
    lines.extend('- ' + str(a.get('action', a.get('title', a))) for a in data['next_actions'])
    lines.extend(['', '## 未覆盖主接口', '', '| 接口 | 分类 | 原因 |', '| --- | --- | --- |'])
    lines.extend('| ' + ' | '.join(str(v).replace('|', '\\|').replace('\n', ' ') for v in
                 (r['method'] + ' ' + r['path'], r['availability'], r['reason'])) + ' |'
                 for r in data['coverage']['routes'] if r['normal_status'] == 'missing')
    lines.extend(['', '## 当前版本有效接口证据', '', '以下仅列已核验子场景；来源绑定不等于整个接口或外部租户通过。', '',
                  '| 接口与场景 | HTTP | 运行 | 源码 | 环境与数据来源 | 原证据 ID |', '| --- | --- | --- | --- | --- | --- |'])
    def cell(value):
        return str(value).replace('|', '\\|').replace('\n', ' ')
    for route in data['coverage']['routes'] + data['coverage']['auxiliary']['routes']:
        for proof in route['evidence'] + route.get('protocol_evidence', []):
            environment = proof.get('environment', {})
            scope = {'owner': environment.get('owner', 'unknown'), 'data_source': environment.get('data_source', 'unknown'),
                     'runtime_sha': environment.get('backend', {}).get('runtime_sha', 'unknown'),
                     'postgres_image_id': environment.get('postgres', {}).get('image_id', 'unknown')}
            lines.append('| ' + ' | '.join(cell(v) for v in (route['method'] + ' ' + route['path'] + ' · ' + proof['scenario'],
                proof.get('actual_status'), proof['run_id'], proof['source_tree_sha'], json.dumps({**scope, 'evidence_mode': proof.get('evidence_mode'), 'proof_scope': proof.get('proof_scope')}, ensure_ascii=False), ', '.join(proof['evidence_ids']))) + ' |')
    lines.extend(['', '## 工作包里程碑'])
    for package in data['work_packages']:
        lines.append('- ' + cell(package.get('title', package['id'])) + '：正常样本 ' + cell(package.get('normal_samples', {}))
                     + '；场景验收 ' + package['scenario_acceptance']['status'] + '；整体 ' + package['acceptance_status'] + '。' + package['acceptance_reason'])
        for proof in package['scenario_acceptance'].get('evidence', []):
            lines.append('  - 明确场景证据：' + json.dumps(proof, ensure_ascii=False))
        for check in package.get('focused_checks', []):
            lines.append('  - 有界子包：' + check['title'] + ' · ' + check['acceptance_status']
                         + ' · ' + check['evidence_mode'] + '。' + check.get('proof_scope', ''))
            for proof in check['scenario_acceptance'].get('evidence', []):
                lines.append('    - 子包场景证据：' + json.dumps(proof, ensure_ascii=False))
            for blocker in check['scenario_acceptance'].get('blocking_cases', []):
                lines.append('    - 未通过或缺证据场景：' + json.dumps(blocker, ensure_ascii=False))
    lines.extend(['', '## 51 项缺口及关联范围'])
    for gap in data['gaps']:
        lines.extend(['', '### ' + str(gap['id']) + ' · ' + gap.get('name', ''),
                      '判定：' + gap['acceptance_status'] + '。' + gap['acceptance_reason'],
                      '台账已做子项（历史叙述）：' + str(gap.get('completed_subscenarios', '未记录')),
                      '剩余：' + str(gap.get('remaining', '未记录')), '证明范围：' + str(gap.get('proof_scope', '未记录'))])
        for group in gap['current_case_groups']:
            lines.append('- 当前源码关联用例：' + json.dumps(group, ensure_ascii=False))
    lines.extend(['', '## 外部待办与明确暂停'])
    for item in data['external_todos'] + data['paused_todos']:
        lines.append('- ' + json.dumps(item, ensure_ascii=False))
    lines.extend(['', '## 缺陷原始证据历史'])
    for defect in data['defects']:
        lines.append('- ' + str(defect.get('id')) + ' · ' + defect.get('title', '') + '。' + defect['record_scope'])
        for proof in defect.get('evidence_history', []):
            lines.append('  - ' + json.dumps(proof, ensure_ascii=False))
    lines.extend(['', '## 证据边界'] + ['- ' + s for s in data['limitations']])
    text = '\n'.join(lines) + '\n'
    if fmt == 'md':
        return text
    return '<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>AIOps 后端整体验收报告</title><style>body{font:16px/1.7 system-ui;margin:32px;max-width:1100px}pre{white-space:pre-wrap;overflow-wrap:anywhere}</style><body><pre>' + html.escape(text) + '</pre></body></html>'
