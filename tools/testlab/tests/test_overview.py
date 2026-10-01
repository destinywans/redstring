"""Aggregation must never promote inventory, stale runs or partial windows to acceptance."""
import json
import ast
from pathlib import Path
import sys
import tempfile
import unittest
import shutil
import subprocess

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from overview import get_overview, render_overview_report
from store import Store

SOURCE = 'a' * 64
OLD = 'b' * 64


class OverviewTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'docs/api').mkdir(parents=True)
        (self.root / 'docs/api/v3-contract.md').write_text('| GET | `/things` | things |\n| POST | `/things` | things |\n')
        (self.root / 'internal/apicontract').mkdir(parents=True)
        (self.root / 'internal/apicontract/openapi.json').write_text(json.dumps({'paths': {'/things': {'get': {'tags': ['things']}, 'post': {'tags': ['things']}}}}))
        (self.root / 'tools/testlab').mkdir(parents=True)
        (self.root / 'tools/testlab/gaps.json').write_text(json.dumps([{'id': 1, 'name': 'external', 'status': 'passed', 'completed_subscenarios': 'historical mock', 'remaining': 'real tenant'}]))
        (self.root / 'tools/testlab/acceptance-plan.json').write_text(json.dumps({'version': 1, 'source_tree_sha': SOURCE,
            'route_plans': [{'method': m, 'path': '/api/v1/things', 'availability': 'local_ready', 'package_id': 'p', 'priority': 'P0'} for m in ('GET', 'POST')],
            'packages': [{'id': 'p', 'title': 'planned', 'status': 'planned'}], 'external_todos': [{'title': 'real tenant'}]}))
        self.store = Store(self.root)

    def run_record(self, status='passed', sha=SOURCE, end=SOURCE, suite='api'):
        run = self.store.create_run(source_tree_sha=sha, selected_suite=suite)
        self.store.update('runs', run['id'], status=status, source_tree_sha_end=end)
        suite = self.store.add_suite(run['id'], {'id': suite, 'name': suite})
        return run, suite

    def case(self, run, suite, name='http', actual='', status='passed', raw_evidence=None, evidence_mode='source'):
        case = self.store.add_case(run['id'], suite['id'], name=name, gap_ids=[1], evidence_mode=evidence_mode)
        attempt = self.store.start_attempt(case)
        try:
            structured = ast.literal_eval(actual) if isinstance(actual, str) and actual else actual or {}
        except (ValueError, SyntaxError):
            structured = {}
        original = json.dumps({'name': name, 'status': status, 'actual': structured})
        evidence = self.store.write_evidence(run['id'], original if raw_evidence is None else raw_evidence)
        self.store.finish_attempt(case, attempt, status, actual, evidence_ids=[evidence])
        return case

    def http(self, run, suite, method='GET', code=200, scenario='normal', mode='real_local', registered=True, status='passed', path='/api/v1/things'):
        self.case(run, suite, status=status, evidence_mode=mode)
        return self.store.add_record(run['id'], 'interfaces', {'method': method, 'path': path, 'evidence_mode': mode,
            'registration_evidence': registered, 'scenarios': {scenario: {'status': status, 'actual_status': code, 'expected_status': code}}})

    def overview(self):
        return get_overview(self.root, self.store, source_sha=SOURCE)

    def test_union_deduplicates_partial_failed_and_blocked_runs_without_hiding_failure(self):
        r, s = self.run_record(status='blocked'); self.http(r, s)
        r2, s2 = self.run_record(status='failed'); self.http(r2, s2); self.http(r2, s2, status='failed')
        v = self.overview()
        self.assertEqual(v['coverage']['main']['normal_success'], 1)
        route = next(x for x in v['coverage']['routes'] if x['method'] == 'GET')
        self.assertEqual(len(route['failed_evidence']), 1)
        self.assertEqual(len(route['evidence']), 2)
        self.assertTrue(all(x['evidence_ids'] for x in route['evidence']))
        self.assertEqual(v['work_packages'][0]['acceptance_status'], 'needs_scenario_plan')
        self.assertEqual(v['gaps'][0]['acceptance_status'], 'unassessed')
        self.assertEqual(v['coverage']['main']['missing'], 1)

    def test_stale_unknown_end_interrupted_and_unknown_source_are_excluded(self):
        for status, sha, end in [('passed', OLD, OLD), ('passed', SOURCE, None), ('interrupted', SOURCE, SOURCE), ('passed', None, None)]:
            r, s = self.run_record(status, sha, end); self.http(r, s)
        v = self.overview()
        self.assertEqual(v['coverage']['main']['normal_success'], 0)
        self.assertEqual(set(v['coverage']['excluded_record_counts']), {'stale', 'unknown_end', 'interrupted', 'unknown'})
        self.assertEqual(get_overview(self.root, self.store, source_sha='unknown')['coverage']['main']['normal_success'], 0)

    def test_inventory_mock_rejection_and_unregistered_cannot_claim_normal_success(self):
        r, s = self.run_record()
        self.http(r, s, mode='source')
        self.http(r, s, mode='protocol_mock')
        self.http(r, s, code=403, registered=False)
        self.http(r, s, code=200, registered=False)
        self.http(r, s, code=401, scenario='authorization', registered=False)
        route = next(x for x in self.overview()['coverage']['routes'] if x['method'] == 'GET')
        self.assertEqual(route['normal_status'], 'missing')
        self.assertEqual(route['scenario_statuses']['authorization'], 'verified')

    def test_unlinked_or_missing_registered_original_evidence_is_not_proof(self):
        r, _ = self.run_record()
        self.store.add_record(r['id'], 'interfaces', {'method': 'GET', 'path': '/api/v1/things', 'evidence_mode': 'real_local',
            'registration_evidence': True, 'scenarios': {'normal': {'status': 'passed', 'actual_status': 200, 'expected_status': 200}}})
        self.assertEqual(self.overview()['coverage']['main']['normal_success'], 0)

    def test_protocol_evidence_is_visible_without_promoting_real_normal_coverage(self):
        r,s=self.run_record(); self.http(r,s,mode='protocol_mock')
        v=self.overview(); route=next(x for x in v['coverage']['routes'] if x['method']=='GET')
        self.assertEqual(v['coverage']['main']['normal_success'],0)
        self.assertEqual(v['coverage']['protocol_mock']['normal_success'],1)
        self.assertEqual(route['protocol_normal_status'],'verified')
        self.assertTrue(route['protocol_evidence'][0]['evidence_ids'])
        self.assertEqual(route['scenario_statuses']['normal'],'missing')
        self.assertIn('协议 Mock 正常子场景：1',render_overview_report(v,'md'))
        self.store.update('runs',r['id'],status='interrupted')
        self.assertEqual(self.overview()['coverage']['protocol_mock']['normal_success'],0)

    def test_conflicting_case_and_interface_modes_cannot_claim_acceptance(self):
        for case_mode, interface_mode in [('protocol_mock', 'real_local'), ('real_local', 'protocol_mock')]:
            with self.subTest(case_mode=case_mode):
                r, s = self.run_record()
                self.case(r, s, evidence_mode=case_mode)
                self.store.add_record(r['id'], 'interfaces', {'method': 'GET', 'path': '/api/v1/things',
                    'evidence_mode': interface_mode, 'registration_evidence': True,
                    'scenarios': {'normal': {'status': 'passed', 'actual_status': 200, 'expected_status': 200}}})
        v = self.overview()
        self.assertEqual(v['coverage']['main']['normal_success'], 0)
        self.assertEqual(v['coverage']['protocol_mock']['normal_success'], 0)
        self.assertEqual(v['coverage']['excluded_record_counts']['evidence_mode_conflict'], 2)

    def test_focused_subset_cannot_close_parent_package_and_latest_failure_is_visible(self):
        p=self.root/'tools/testlab/acceptance-plan.json'; plan=json.loads(p.read_text())
        plan['packages'][0]['focused_checks']=[dict(id='subset',title='protocol subset',evidence_mode='protocol_mock',
            proof_scope='protocol only',route_keys=['GET /api/v1/things'],
            required_cases=[dict(suite='protocol',name='subset.complete')])]
        p.write_text(json.dumps(plan))
        r,s=self.run_record(suite='protocol'); self.http(r,s,mode='protocol_mock'); self.case(r,s,name='subset.complete')
        v=self.overview(); check=v['work_packages'][0]['focused_checks'][0]
        self.assertEqual(check['scenario_acceptance']['status'],'passed')
        self.assertEqual(check['acceptance_status'],'passed')
        self.assertEqual(check['normal_covered'],1)
        self.assertEqual(v['work_packages'][0]['acceptance_status'],'needs_scenario_plan')
        r2,s2=self.run_record(status='failed',suite='protocol'); self.case(r2,s2,name='subset.complete',status='failed')
        check=self.overview()['work_packages'][0]['focused_checks'][0]
        self.assertEqual(check['scenario_acceptance']['status'],'incomplete')
        self.assertEqual(check['scenario_acceptance']['evidence'][0]['run_id'],r2['id'])

    def test_focused_marker_without_route_proof_cannot_pass_subset(self):
        p=self.root/'tools/testlab/acceptance-plan.json';plan=json.loads(p.read_text())
        plan['packages'][0]['focused_checks']=[dict(id='subset',title='subset',evidence_mode='real_local',
            route_keys=['GET /api/v1/things'],required_cases=[dict(suite='api',name='subset.complete')])]
        p.write_text(json.dumps(plan))
        r,s=self.run_record();self.case(r,s,name='subset.complete')
        check=self.overview()['work_packages'][0]['focused_checks'][0]
        self.assertEqual(check['scenario_acceptance']['status'],'passed')
        self.assertEqual(check['normal_covered'],0)
        self.assertEqual(check['acceptance_status'],'incomplete')

    def test_focused_normal_and_markers_cannot_hide_same_suite_environment_blocker(self):
        p=self.root/'tools/testlab/acceptance-plan.json';plan=json.loads(p.read_text())
        plan['packages'][0]['focused_checks']=[dict(id='subset',title='subset',evidence_mode='real_local',
            route_keys=['GET /api/v1/things'],required_cases=[dict(suite='api',name='subset.complete')])]
        p.write_text(json.dumps(plan))
        r,s=self.run_record(status='blocked');self.http(r,s);self.case(r,s,name='subset.complete')
        blocked=self.case(r,s,name='unproven.source',status='blocked',actual=str({'reason':'Source completeness unverified'}))
        v=self.overview();check=v['work_packages'][0]['focused_checks'][0]
        self.assertEqual(check['normal_covered'],1)
        self.assertEqual(check['acceptance_status'],'incomplete')
        blockers=check['scenario_acceptance']['blocking_cases']
        self.assertEqual(len(blockers),1)
        self.assertEqual(blockers[0]['case_id'],blocked['id'])
        self.assertEqual(blockers[0]['status'],'blocked')
        self.assertEqual(blockers[0]['reason'],'Source completeness unverified')
        self.assertTrue(blockers[0]['evidence_ids'])
        self.assertIn('unproven.source',render_overview_report(v,'md'))

    def test_mock_verified_normal_remains_missing_real_but_leaves_first_local_execution_queue(self):
        r,s=self.run_record();self.http(r,s,mode='protocol_mock')
        v=self.overview()
        self.assertEqual(v['coverage']['main']['normal_success'],0)
        self.assertEqual(v['coverage']['main']['missing'],2)
        self.assertEqual(len(v['local_queue']),2)
        self.assertEqual(len(v['execution_queue']),1)
        pending=v['protocol_verified_pending_real']
        self.assertEqual(len(pending),1)
        self.assertEqual(pending[0]['method'],'GET')
        self.assertEqual(pending[0]['normal_status'],'missing')
        self.assertTrue(pending[0]['protocol_evidence'])

    def test_blocker_needed_and_error_code_from_original_are_preserved(self):
        p = self.root / 'tools/testlab/acceptance-plan.json'
        plan = json.loads(p.read_text())
        plan['packages'][0]['required_cases'] = [dict(suite='api', name='complete')]
        p.write_text(json.dumps(plan))
        r, s = self.run_record(status='blocked')
        self.case(r, s, name='complete')
        needed = 'trusted complete environment-bound seven-day history producer; not present'
        self.case(r, s, name='history', status='blocked', actual={'needed': needed, 'error_code': 'not_connected'})
        self.case(r, s, name='route', status='blocked', actual={'error_code': 'route_source_unavailable'})
        self.case(r, s, name='unknown', status='blocked', actual={'normal_verified': False})
        blockers = {b['name']: b for b in self.overview()['work_packages'][0]['scenario_acceptance']['blocking_cases']}
        self.assertIn(needed, blockers['history']['reason'])
        self.assertIn('not_connected', blockers['history']['reason'])
        self.assertEqual(blockers['route']['reason'], 'route_source_unavailable')
        self.assertEqual(blockers['unknown']['reason'], '')
        for b in blockers.values():
            self.assertEqual(b['status'], 'blocked')
            self.assertTrue(b['evidence_ids'])

    def test_regression_components_are_rendered_in_reports_and_dashboard(self):
        old, old_suite = self.run_record(sha=OLD, end=OLD, suite='full')
        self.case(old, old_suite, name='history')
        runs = []
        for suite in ('baseline', 'regressions', 'passkey,configsync', 'mfa'):
            r, s = self.run_record(status='failed' if suite == 'baseline' else 'blocked' if suite == 'regressions' else 'passed', suite=suite)
            runs.append(r)
            self.case(r, s, name=suite)
            if suite == 'baseline':
                self.case(r, s, name='leaf', status='failed')
                self.case(r, s, name='package wrapper', status='failed')
                self.case(r, s, name='opt-in', status='skipped')
            if suite == 'regressions':
                self.case(r, s, name='unavailable', status='blocked')
        data = self.overview()
        gate = next(g for g in data['gates'] if g['id'] == 'regression')
        self.assertEqual(gate['status'], 'incomplete')
        for fmt in ('md', 'html'):
            report = render_overview_report(data, fmt).split('优先行动')[0]
            for r in runs:
                self.assertIn(r['id'], report)
            for suite in ('baseline', 'regressions', 'passkey,configsync', 'mfa'):
                self.assertIn(suite, report)
            self.assertIn('2 失败记录', report)
            self.assertIn('1 跳过', report)
            self.assertIn('1 受阻', report)
            self.assertIn('不等于独立产品缺陷数', report)
            self.assertIn('不计当前源码通过', report)
            self.assertIn('/api/runs/' + runs[0]['id'] + '/report.html', report)
        self.assertIn('href="/api/runs/', render_overview_report(data, 'html'))
        unsafe = json.loads(json.dumps(data))
        unsafe_component = next(g for g in unsafe['gates'] if g['id'] == 'regression')['component_progress'][0]
        unsafe_component.update(selected_suite='<script>component</script>', run_id='"/><script>record</script>')
        escaped = render_overview_report(unsafe, 'html')
        self.assertNotIn('<script>', escaped)
        self.assertIn('&lt;script&gt;component&lt;/script&gt;', escaped)
        self.assertIn('%22%2F%3E%3Cscript%3Erecord%3C%2Fscript%3E', escaped)
        if not shutil.which('node'):
            self.skipTest('Node is required for the isolated DOM rendering check')
        app = Path(__file__).resolve().parents[1] / 'static/app.js'
        harness = r'''
const fs = require('fs'), vm = require('vm');
class Node {
  constructor(tag) {this.tag=tag;this.children=[];this.style={};this.attrs={};this.textContent='';}
  append(...nodes) {this.children.push(...nodes);}
  replaceChildren(...nodes) {this.children=nodes;}
  setAttribute(k,v) {this.attrs[k]=String(v);}
  set innerHTML(value) {throw new Error('Unsafe HTML rendering');}
  get lastChild() {return this.children[this.children.length-1];}
}
const root=new Node('root');
const document={createElement:tag=>new Node(tag),getElementById:()=>root};
const source=fs.readFileSync(process.argv[1],'utf8');
const startup=source.lastIndexOf('  document.querySelectorAll("[data-view]")');
const data=JSON.parse(fs.readFileSync(0,'utf8'));
const code=source.slice(0,startup)+'state.overall='+JSON.stringify(data)+';renderOverview();})();';
vm.runInNewContext(code,{document,Node});
console.log(JSON.stringify(root));
'''
        result = subprocess.run(['node', '-e', harness, str(app)], input=json.dumps(data), text=True, capture_output=True, check=True)
        tree = json.loads(result.stdout)
        def nodes(n):
            yield n
            for child in n.get('children', []):
                if isinstance(child, dict):
                    yield from nodes(child)
        rendered = '\n'.join(str(n.get('textContent', '')) for n in nodes(tree))
        for suite in ('baseline', 'regressions', 'passkey,configsync', 'mfa'):
            self.assertIn(suite, rendered)
        self.assertIn('2 失败记录', rendered)
        self.assertIn('1 跳过', rendered)
        self.assertIn('1 受阻', rendered)
        self.assertIn('不等于独立产品缺陷数', rendered)
        self.assertIn('不计当前源码通过', rendered)
        progress = next(n for n in nodes(tree) if n.get('className') == 'regression-progress')
        self.assertEqual(len(progress['children']), len(runs))
        self.assertFalse(any(n.get('tag') == 'table' for n in nodes(progress)))
        for card in progress['children']:
            self.assertEqual(card['className'], 'regression-component')
            self.assertTrue(any(n.get('tag') == 'button' and '查看运行' in n.get('textContent', '') for n in nodes(card)))
        gates = next(n for n in nodes(tree) if n.get('className') == 'acceptance-gates')
        self.assertFalse(any(n.get('className') == 'regression-component' for n in nodes(gates)))

    def test_registered_but_missing_evidence_file_is_excluded(self):
        r, s = self.run_record(); self.http(r, s)
        self.assertEqual(self.overview()['coverage']['main']['normal_success'], 1)
        with self.store.connect() as conn:
            filename = conn.execute('SELECT filename FROM evidence WHERE run_id=?', (r['id'],)).fetchone()[0]
        (self.store.evidence_dir / filename).unlink()
        v = self.overview()
        self.assertEqual(v['coverage']['main']['normal_success'], 0)
        self.assertEqual(v['coverage']['excluded_record_counts']['unlinked_or_missing_original_evidence'], 1)

    def test_current_full_regression_can_pass_but_unexplained_skip_cannot(self):
        r, s = self.run_record(suite='baseline,mfa,passkey,configsync,regressions')
        self.case(r, s)
        def gate(): return next(g for g in self.overview()['gates'] if g['id']=='regression')
        self.assertEqual(gate()['status'], 'passed')
        self.case(r, s, status='skipped')
        self.assertEqual(gate()['status'], 'incomplete')

    def test_auxiliary_success_is_separate_from_primary_denominator(self):
        r, s = self.run_record(); self.http(r, s, path='/api/v1/aux')
        v = self.overview()
        self.assertEqual(v['coverage']['main']['total'], 2)
        self.assertEqual(v['coverage']['main']['normal_success'], 0)
        self.assertEqual(v['coverage']['auxiliary']['normal_success'], 1)

    def test_current_partial_regression_is_visible_but_does_not_pass_full_gate(self):
        old, osuite = self.run_record(sha=OLD, end=OLD, suite='baseline,mfa,passkey,configsync,regressions')
        self.case(old, osuite, 'historical')
        r, s = self.run_record(suite='baseline')
        self.case(r, s, 'current')
        self.case(r, s, 'special opt-in required', status='skipped')
        gate = next(g for g in self.overview()['gates'] if g['id'] == 'regression')
        self.assertEqual(gate['status'], 'incomplete')
        self.assertEqual(gate['component_progress'][0]['run_id'], r['id'])
        self.assertEqual(gate['component_progress'][0]['counts'], {'passed': 1, 'skipped': 1})
        self.assertIn('专项跳过', gate['reason'])

    def test_running_regression_with_pending_source_end_is_progress_only(self):
        r, s = self.run_record(status='running', end=None, suite='baseline')
        self.case(r, s, 'observed')
        gate = next(g for g in self.overview()['gates'] if g['id'] == 'regression')
        self.assertEqual(gate['status'], 'incomplete')
        self.assertTrue(gate['component_progress'][0]['source_end_pending'])
        self.assertIn('进行中', gate['reason'])

    def test_separate_specialty_runs_cannot_implicitly_close_whole_regression(self):
        for suite in ('baseline', 'mfa', 'passkey', 'configsync', 'regressions'):
            r, s = self.run_record(suite=suite)
            self.case(r, s, suite)
        gate = next(g for g in self.overview()['gates'] if g['id'] == 'regression')
        self.assertEqual(gate['status'], 'incomplete')
        self.assertEqual(len(gate['component_progress']), 5)

    def test_historical_full_regression_does_not_become_current(self):
        r, s = self.run_record(sha=OLD, end=OLD, suite='baseline,mfa,passkey,configsync,regressions')
        self.case(r, s)
        gate = next(g for g in self.overview()['gates'] if g['id'] == 'regression')
        self.assertEqual(gate['status'], 'stale')
        self.assertEqual(gate['history'][0]['counts']['passed'], 1)

    def test_five_complete_original_performance_stages_required_with_scope_and_recovery(self):
        r, s = self.run_record(suite='performance')
        self.case(r, s, name='perf.seed', actual=str({'profile': 'base'}))
        for stage, rate, seconds in [('warmup',10,120), ('rps10',10,300), ('rps25',25,300), ('rps50',50,300)]:
            self.case(r, s, name='perf.'+stage, actual=str({'stage':stage, 'profile':'base', 'rate_rps':rate, 'planned_seconds':seconds,
                'generator_seconds':seconds, 'full_aggregate_scope_accepted':True, 'k6_exit':0}))
        def gate(): return next(g for g in self.overview()['gates'] if g['id']=='performance_base')
        self.assertEqual(gate()['status'], 'incomplete')
        self.case(r, s, name='perf.burst100', actual=str({'stage':'burst100', 'profile':'base', 'rate_rps':100, 'planned_seconds':60,
            'generator_seconds':60, 'full_aggregate_scope_accepted':True, 'k6_exit':0}))
        for name in ('perf.sse', 'perf.resource_recovery', 'perf.data_consistency'):
            self.case(r, s, name=name)
        self.assertEqual(gate()['status'], 'passed')
        self.store.update('runs', r['id'], status='blocked')
        self.assertEqual(gate()['status'], 'incomplete')

    def test_short_soak_is_not_continuous_24h_and_report_is_readable_and_escaped(self):
        r, s = self.run_record(suite='soak'); self.case(r, s, name='perf.soak_acceptance', actual=str({'generator_seconds':120, 'planned_seconds':86400}))
        v = self.overview()
        self.assertEqual(next(g for g in v['gates'] if g['id']=='soak')['status'], 'incomplete')
        self.assertIn('正常 HTTP 子场景', render_overview_report(v, 'md'))
        self.assertEqual(json.loads(render_overview_report(v, 'json'))['coverage']['main']['total'], 2)
        v['limitations'].append('<script>danger</script>')
        self.assertNotIn('<script>danger</script>', render_overview_report(v, 'html'))
        with self.assertRaises(ValueError): render_overview_report(v, 'exe')

    def test_complete_performance_uses_original_structured_evidence_after_display_truncation(self):
        r, s = self.run_record(suite='performance')
        self.case(r, s, name='perf.seed', actual=str({'profile': 'base'}))
        for stage, rate, seconds in [('warmup',10,120), ('rps10',10,300), ('rps25',25,300), ('rps50',50,300), ('burst100',100,60)]:
            case = self.case(r, s, name='perf.'+stage, actual=str({'resource_padding':'x'*9000, 'stage':stage,
                'profile':'base', 'rate_rps':rate, 'planned_seconds':seconds, 'generator_seconds':seconds,
                'full_aggregate_scope_accepted':True, 'k6_exit':0}))
            self.assertGreater(len(str({'resource_padding':'x'*9000})), 8192)
            self.assertIn('日志达到大小上限', self.store.detail(r['id'])['cases'][-1]['actual'])
        for name in ('perf.sse', 'perf.resource_recovery', 'perf.data_consistency'):
            self.case(r, s, name=name)
        gate = next(g for g in self.overview()['gates'] if g['id']=='performance_base')
        self.assertEqual(gate['status'], 'passed')
        with self.store.connect() as conn:
            filename = conn.execute('SELECT filename FROM evidence WHERE run_id=? ORDER BY rowid LIMIT 1 OFFSET 1', (r['id'],)).fetchone()[0]
        original = self.store.evidence_dir / filename
        content = original.read_text()
        original.write_text('{"name": "perf.warmup", "actual":')
        self.assertEqual(next(g for g in self.overview()['gates'] if g['id']=='performance_base')['status'], 'incomplete')
        original.write_text(content)
        self.assertEqual(next(g for g in self.overview()['gates'] if g['id']=='performance_base')['status'], 'passed')
        original.unlink()
        self.assertEqual(next(g for g in self.overview()['gates'] if g['id']=='performance_base')['status'], 'incomplete')

    def test_complete_soak_reads_large_original_json_not_display_text(self):
        r, s = self.run_record(suite='soak')
        actual = {'resource_padding':'x'*9000, 'generator_seconds':86401, 'planned_seconds':86400,
                  'rate_rps':10, 'full_aggregate_scope_accepted':True,
                  'trial_final':{'accepted':True, 'evaluated_points':1440, 'missing_points':0},
                  'hooks':{'dryrun_calls':1, 'webhook_calls':1, 'chat_turns':1, 'relogins':1, 'failures':0},
                  'sse':{'stable_samples':1, 'normal_frames':1, 'slow_bytes':1, 'peak_normal':2, 'peak_slow':1, 'reconnects':1}}
        self.case(r, s, name='perf.soak_acceptance', actual=str(actual))
        self.assertEqual(next(g for g in self.overview()['gates'] if g['id']=='soak')['status'], 'passed')
        self.store.update('runs', r['id'], status='interrupted')
        self.assertEqual(next(g for g in self.overview()['gates'] if g['id']=='soak')['status'], 'incomplete')

    def test_explicit_scenario_package_requires_latest_group_and_all_normal_routes(self):
        plan_path = self.root / 'tools/testlab/acceptance-plan.json'
        plan = json.loads(plan_path.read_text())
        plan['packages'][0]['required_cases'] = [{'suite': 'api', 'name': 'complete'}, {'suite': 'api', 'name': 'cleanup'}]
        plan_path.write_text(json.dumps(plan))
        r, s = self.run_record(); self.http(r, s); self.http(r, s, method='POST')
        self.case(r, s, name='complete'); self.case(r, s, name='cleanup')
        package = self.overview()['work_packages'][0]
        self.assertEqual(package['normal_samples']['status'], 'passed')
        self.assertEqual(package['scenario_acceptance']['status'], 'passed')
        self.assertEqual(package['acceptance_status'], 'passed')
        self.assertEqual(self.overview()['gates'][0]['status'], 'passed')
        later, later_suite = self.run_record(status='interrupted', end=None)
        self.case(later, later_suite, name='complete')
        self.assertEqual(self.overview()['work_packages'][0]['scenario_acceptance']['status'], 'incomplete')
        self.assertEqual(self.overview()['work_packages'][0]['normal_samples']['status'], 'passed')

    def test_defect_original_evidence_history_survives_aggregation_without_fake_current_fix(self):
        source = [{'id': 'WAN-X', 'title': 'historic fix', 'status': 'in_review'}]
        (self.root / 'tools/testlab/defects.json').write_text(json.dumps(source))
        r, _ = self.run_record(sha=OLD, end=OLD)
        eid = self.store.write_evidence(r['id'], 'first failure original')
        self.store.add_record(r['id'], 'defects', {'id': 'WAN-X', 'evidence_ids': [eid], 'first_failure_source': OLD})
        defect = self.overview()['defects'][0]
        self.assertEqual(defect['evidence_history'][0]['evidence_ids'], [eid])
        self.assertEqual(defect['evidence_history'][0]['source_binding'], 'stale')
        self.assertEqual(defect['status'], 'in_review')
        report = render_overview_report(self.overview(), 'md')
        self.assertIn(eid, report)
        self.assertIn('原始证据历史', report)
        self.assertIn('51 项缺口', report)
        self.assertIn('real tenant', report)


if __name__ == '__main__':
    unittest.main()
