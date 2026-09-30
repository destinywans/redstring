"""Small offline tests for continuous-window invalidation, never performance proof."""
import importlib.util,json,sys,tempfile,unittest
import threading
from pathlib import Path
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import perf

class FrozenInputTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.work=self.root/'.local/testlab/environments/testlab-0123456789ab';(self.work/'perf').mkdir(parents=True);(self.work/'trial').mkdir()
        for name in ('perf.py','load/seed.py','load/traffic.js','fixtures.py'):
            p=self.root/'tools/testlab'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_text('fixed test helper')
        self.token=self.work/'trial/source-token';self.token.write_text('synthetic token')
        self.rules=self.work/'trial/rules.json';self.rules.write_text(json.dumps({'bearer_token_file':str(self.token)}))
        self.instance=self.work/'instance.json';self.instance.write_text('{}')
        (self.root/'.local/testlab/component-environment.json').write_text('{}')
        (self.root/'.local/testlab/runtime-config.json').write_text(json.dumps({'owner':'testlab-0123456789ab','work':str(self.work/'trial'),'env':{'AIOPS_V3_ALERT_RULES_CONFIG':str(self.rules)}}))
        self.data={'owner':'testlab-0123456789ab','perf_work':str(self.work/'perf'),'instance_config':str(self.instance),'source_tree_sha':'same'}
        self.patches=[patch.object(perf,'ROOT',self.root),patch.object(perf,'source_fingerprint',return_value='same'),patch.object(perf,'identity',return_value={'pid':1,'api_pids':[2,3],'source_tree_sha':'same'})]
        for p in self.patches:p.start()
    def tearDown(self):
        for p in reversed(self.patches):p.stop()
        self.tmp.cleanup()
    def test_token_rotation_invalidates_continuous_window(self):
        perf.frozen(self.data);self.token.write_text('changed synthetic token')
        with self.assertRaisesRegex(RuntimeError,'configuration changed'):perf.frozen(self.data)
    def test_uppercase_env_file_is_frozen(self):
        ca=self.work/'combined-ca.pem';ca.write_text('synthetic CA')
        path=self.root/'.local/testlab/runtime-config.json';runtime=json.loads(path.read_text());runtime['env']['SSL_CERT_FILE']=str(ca);path.write_text(json.dumps(runtime))
        perf.frozen(self.data);ca.write_text('rotated synthetic CA')
        with self.assertRaisesRegex(RuntimeError,'configuration changed'):perf.frozen(self.data)

    def test_rule_config_change_invalidates_continuous_window(self):
        perf.frozen(self.data);self.rules.write_text('{}')
        with self.assertRaisesRegex(RuntimeError,'configuration changed'):perf.frozen(self.data)
    def test_process_restart_invalidates_continuous_window(self):
        perf.frozen(self.data)
        with patch.object(perf,'identity',return_value={'pid':4,'api_pids':[5,6],'source_tree_sha':'same'}):
            with self.assertRaisesRegex(RuntimeError,'restarted'):perf.frozen(self.data)
    def test_reference_outside_owned_environment_is_rejected(self):
        self.rules.write_text(json.dumps({'bearer_token_file':'/etc/passwd'}))
        with self.assertRaisesRegex(RuntimeError,'escapes'):perf.frozen(self.data)


class TrialFinishTest(unittest.TestCase):
    def trial(self,**changes):
        row={'trial_id':'a'*32,'status':'completed','started_at':'2026-09-30T00:00:00+00:00','expires_at':'2026-10-01T00:00:00+00:00','evaluated_points':1440,'missing_points':0}
        row.update(changes);return row
    def finish(self,row):
        data={'base_urls':['http://127.0.0.1:18561'],'hook_user':{'cookie':'synthetic'}}
        start=perf.datetime.datetime.fromisoformat(row['started_at']).timestamp()
        with patch.object(perf,'frozen'),patch.object(perf,'request',return_value=(200,row,[])):
            return perf.finish_trial(data,{'trial_status_path':'/api/v1/alert-rules/dry-runs/'+row['trial_id']},start+1,start+86401)
    def test_completed_requires_full_points_and_zero_missing(self):
        for row in (self.trial(evaluated_points=1439),self.trial(missing_points=1),self.trial(status='inconclusive')):
            good,_=self.finish(row);self.assertFalse(good)
    def test_real_overlap_is_reported_without_rounding_to_24_hours(self):
        good,evidence=self.finish(self.trial());self.assertTrue(good)
        self.assertEqual(evidence['overlap_seconds'],86399)
        self.assertEqual(evidence['trial_window_seconds'],86400)
    def test_running_completion_has_bounded_poll_with_frozen_checks(self):
        row=self.trial();data={'base_urls':['http://127.0.0.1:18561'],'hook_user':{'cookie':'synthetic'}};start=perf.datetime.datetime.fromisoformat(row['started_at']).timestamp()
        with patch.object(perf,'frozen') as frozen,patch.object(perf,'request',side_effect=[(200,self.trial(status='running'),[]),(200,row,[])]),patch.object(perf.STOP,'wait',return_value=False),patch.object(perf,'result'):
            good,_=perf.finish_trial(data,{'trial_status_path':'/api/v1/alert-rules/dry-runs/'+row['trial_id']},start+1,start+86401)
        self.assertTrue(good);self.assertEqual(frozen.call_count,2)


class DashboardSchemaTest(unittest.TestCase):
    def test_stage_profile_and_actual_scope_preserved(self):
        actual={'stage':'rps25','profile':'base','measured_routes':['/api/v1/conversations'],'full_aggregate_scope_accepted':False,'excluded_aggregate_paths':['/api/v1/home']}
        m=perf.dashboard_metrics(actual,'perf.rps25')
        self.assertEqual((m['name'],m['stage'],m['scope']['profile']),('perf.rps25','rps25','base'))
        self.assertEqual(m['scope']['measured_routes'],actual['measured_routes']);self.assertFalse(m['full_aggregate_scope_accepted'])
        self.assertEqual(m['excluded_aggregate_paths'],['/api/v1/home'])
    def test_terminal_metrics_retain_missing_and_real_window(self):
        actual={'summary':{'iterations':{'values':{'rate':10}},'http_req_duration':{'values':{'med':2,'p(95)':8,'p(99)':11}}},'unexpected_error_rate':0.01,'resource_samples':[{'missing':['backend_fd']}],'trial_final':{'overlap_seconds':86399}}
        m=perf.dashboard_metrics(actual)
        self.assertEqual((m['throughput_rps'],m['p50_ms'],m['p95_ms'],m['p99_ms']),(10,2,8,11));self.assertEqual(m['missing'],['backend_fd']);self.assertEqual(m['trial_window']['overlap_seconds'],86399)


class BoundedResourceTest(unittest.TestCase):
    def test_full_evidence_not_mislabelled_as_stdout_tail(self):
        samples=[{'at':i*30,'postgres':{'connections':20+i},'missing':['backend_rss'] if i==0 else []} for i in range(2880)]
        summary=perf.resource_summary(samples)
        self.assertEqual(summary['full_sample_count'],2880);self.assertEqual(len(summary['resource_samples']),20)
        self.assertEqual(summary['resource_trends']['postgres.connections']['sample_count'],2880)
        self.assertEqual(summary['resource_missing'],['backend_rss'])
        self.assertLess(len(json.dumps(summary)),12000)

class SSEAcceptanceTest(unittest.TestCase):
    def good(self):return {'stable_samples':5,'window_violations':0,'protocol_errors':0,'producer_errors':0,'normal_frames':30,'slow_bytes':100,'expected_disconnects':15}
    def test_protocol_replay_error_fails_despite_historical_peak_and_progress(self):
        stats=self.good();stats.update(peak_normal=50,peak_slow=10,protocol_errors=1)
        self.assertFalse(perf.sse_accepted(stats))
        self.assertTrue(perf.sse_accepted(self.good()))
    def test_duplicate_regression_and_malformed_protocol_rejected(self):
        for lines in ([b'id: 7\n',b'data: {"type":"text.delta"}\n',b'\n'],[b'id: bad\n'],[b'id: 8\n',b'data: nope\n',b'\n']):
            parser=perf.SSEParser(7)
            with self.assertRaises(perf.SSEProtocolError):
                for line in lines:parser.feed(line)
    def test_dead_clients_fail_window_but_bounded_reconnect_allowed(self):
        stream=perf.Streams.__new__(perf.Streams);stream.lock=threading.Lock();stream.renew_until=0;stream.stats=self.good()
        stream.clients={0:{'active':False,'last_progress':99,'disconnected_at':99}}
        with patch.object(perf.time,'monotonic',return_value=100):stream.sample_health(60,300)
        self.assertTrue(perf.sse_accepted(stream.stats))
        with patch.object(perf.time,'monotonic',return_value=116):stream.sample_health(76,300)
        self.assertFalse(perf.sse_accepted(stream.stats));self.assertEqual(stream.stats['last_window']['unhealthy_clients'],[0])
    def test_startup_and_renewal_grace_do_not_claim_stable_samples(self):
        stream=perf.Streams.__new__(perf.Streams);stream.lock=threading.Lock();stream.renew_until=130;stream.stats=self.good();stream.stats['stable_samples']=0;stream.clients={}
        with patch.object(perf.time,'monotonic',return_value=100):stream.sample_health(5,300);stream.sample_health(60,300)
        self.assertFalse(perf.sse_accepted(stream.stats))

if __name__=='__main__':unittest.main()
