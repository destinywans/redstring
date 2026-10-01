"""Small offline tests for continuous-window invalidation, never performance proof."""
import importlib.util,json,sys,tempfile,unittest
import threading,shutil,subprocess,socket,time
from pathlib import Path
from unittest.mock import patch,Mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import perf

class FrozenInputTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.work=self.root/'.local/testlab/environments/testlab-0123456789ab';(self.work/'perf').mkdir(parents=True);(self.work/'trial').mkdir()
        for name in ('perf.py','backend.py','faults.py','load/seed.py','load/traffic.js','fixtures.py'):
            p=self.root/'tools/testlab'/name;p.parent.mkdir(parents=True,exist_ok=True);p.write_text('fixed test helper')
        self.token=self.work/'trial/source-token';self.token.write_text('synthetic token')
        self.rules=self.work/'trial/rules.json';self.rules.write_text(json.dumps({'bearer_token_file':str(self.token)}))
        self.instance=self.work/'instance.json';self.instance.write_text('{}')
        (self.root/'.local/testlab/component-environment.json').write_text('{}')
        (self.root/'.local/testlab/runtime-config.json').write_text(json.dumps({'owner':'testlab-0123456789ab','work':str(self.work/'trial'),'env':{'AIOPS_V3_ALERT_RULES_CONFIG':str(self.rules)}}))
        self.binary=self.work/'aiops';self.binary.write_bytes(b'synthetic binary')
        self.data={'owner':'testlab-0123456789ab','perf_work':str(self.work/'perf'),'instance_config':str(self.instance),'binary':str(self.binary),'source_tree_sha':'same'}
        self.patches=[patch.object(perf,'ROOT',self.root),patch.object(perf,'source_fingerprint',return_value='same'),patch.object(perf,'identity',return_value={'pid':1,'api_pids':[2,3],'source_tree_sha':'same'})]
        for p in self.patches:p.start()
    def tearDown(self):
        for p in reversed(self.patches):p.stop()
        self.tmp.cleanup()
    def test_token_rotation_invalidates_continuous_window(self):
        perf.frozen(self.data);self.token.write_text('changed synthetic token')
        with self.assertRaisesRegex(RuntimeError,'configuration changed'):perf.frozen(self.data)
    def test_binary_replacement_invalidates_continuous_window(self):
        perf.frozen(self.data);self.binary.write_bytes(b'changed binary')
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

    def test_observation_requires_strict_baseline_and_preserves_all_freeze_inputs(self):
        observe=Mock()
        with self.assertRaisesRegex(RuntimeError,'strict ready baseline'):perf.frozen(self.data,measurement_api_pids=(2,3),health_observer=observe)
        perf.frozen(self.data)
        current={'pid':1,'api_pids':[2,3],'source_tree_sha':'same','ready':False,'failed':False}
        with patch.object(perf,'identity',return_value=current) as identity:
            perf.frozen(self.data,measurement_api_pids=(2,3),health_observer=observe)
            identity.assert_called_once_with(self.data,measurement_api_pids=(2,3))
        observe.assert_called_once_with(current)
        for path in (self.token,self.rules,self.binary):
            previous=path.read_bytes();path.write_bytes(b'{"changed":true}' if path.suffix=='.json' else b'changed');observe.reset_mock()
            with self.assertRaisesRegex(RuntimeError,'configuration changed'):perf.frozen(self.data,measurement_api_pids=(2,3),health_observer=observe)
            observe.assert_not_called();path.write_bytes(previous)
        with patch.object(perf,'source_fingerprint',return_value='changed'),self.assertRaisesRegex(RuntimeError,'source changed'):
            perf.frozen(self.data,measurement_api_pids=(2,3),health_observer=observe)
        with self.assertRaisesRegex(RuntimeError,'strict ready baseline'):perf.frozen(self.data,measurement_api_pids=(4,5))


class IdentityAttestationTest(unittest.TestCase):
    def test_unready_observation_is_fresh_authenticated_exact_live_generation_only(self):
        data={'owner':'testlab-0123456789ab','control_secret':'synthetic-secret','supervisor_pid':12,'source_tree_sha':'same'}
        current={'owner':data['owner'],'pid':12,'api_pids':[13,14],'ready':False,'failed':False,'source_tree_sha':'same'}
        nonces=['a'*32,'b'*32];calls=[]
        def transport(*args,**kwargs):
            nonce=nonces[len(calls)];conn=Mock();calls.append(conn)
            conn.getresponse.return_value.status=200
            conn.getresponse.return_value.read.return_value=json.dumps({'nonce':nonce,'identity':current,'proof':perf.control_proof(data['control_secret'],nonce,current)}).encode();return conn
        with patch.object(perf.secrets,'token_hex',side_effect=nonces),patch.object(perf.http.client,'HTTPConnection',side_effect=transport):
            for _ in range(2):self.assertEqual(perf.identity(data,measurement_api_pids=(13,14)),current)
        for index,conn in enumerate(calls):conn.request.assert_called_once_with('GET','/attest?nonce='+nonces[index]);conn.close.assert_called_once()
        nonce='a'*32
        variants=[{**current,'owner':'other'},{**current,'pid':99},{**current,'api_pids':[15,16]},
                  {**current,'api_pids':[13.0,14]},{**current,'source_tree_sha':'changed'},
                  {**current,'failed':True},{key:value for key,value in current.items() if key!='failed'},
                  {**current,'ready':0}]
        for altered in variants:
            with self.subTest(altered=altered):
                conn=Mock();conn.getresponse.return_value.status=200
                conn.getresponse.return_value.read.return_value=json.dumps({'nonce':nonce,'identity':altered,'proof':perf.control_proof(data['control_secret'],nonce,altered)}).encode()
                with patch.object(perf.secrets,'token_hex',return_value=nonce),patch.object(perf.http.client,'HTTPConnection',return_value=conn):
                    with self.assertRaises(RuntimeError):perf.identity(data,measurement_api_pids=(13,14))
                    with self.assertRaises(RuntimeError):perf.identity(data)
                self.assertEqual(conn.request.call_count,2)
        conn=Mock();conn.getresponse.return_value.status=200;conn.getresponse.return_value.read.return_value=json.dumps({'nonce':nonce,'identity':current,'proof':'forged'}).encode()
        with patch.object(perf.secrets,'token_hex',return_value=nonce),patch.object(perf.http.client,'HTTPConnection',return_value=conn):
            with self.assertRaisesRegex(RuntimeError,'attestation mismatch'):perf.identity(data,measurement_api_pids=(13,14))

    def test_signed_receipt_and_invalid_replay_or_forged_proof(self):
        data={'owner':'testlab-0123456789ab','control_secret':'synthetic-secret','supervisor_pid':12,'source_tree_sha':'same'}
        nonce='a'*32
        identity={'owner':data['owner'],'pid':12,'api_pids':[13,14],'ready':True,'source_tree_sha':'same'}
        valid={'nonce':nonce,'identity':identity,'proof':perf.control_proof(data['control_secret'],nonce,identity)}
        for receipt,ok in ((valid,True),({**valid,'nonce':'b'*32},False),({**valid,'identity':{**identity,'pid':99}},False)):
            conn=Mock();response=conn.getresponse.return_value;response.status=200;response.read.return_value=json.dumps(receipt).encode()
            with patch.object(perf.secrets,'token_hex',return_value=nonce),patch.object(perf.http.client,'HTTPConnection',return_value=conn):
                if ok:self.assertEqual(perf.identity(data),identity)
                else:
                    with self.assertRaisesRegex(RuntimeError,'attestation mismatch'):perf.identity(data)
            conn.request.assert_called_once_with('GET','/attest?nonce='+nonce);conn.close.assert_called_once()
    def test_oversized_or_non_success_receipt_rejected(self):
        for status,raw in ((503,b'{}'),(200,b'x'*((1<<16)+1))):
            conn=Mock();conn.getresponse.return_value.status=status;conn.getresponse.return_value.read.return_value=raw
            with patch.object(perf.http.client,'HTTPConnection',return_value=conn),self.assertRaisesRegex(RuntimeError,'unavailable'):perf.identity({})
            conn.close.assert_called_once()


class StageFailureEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        self.work=self.root/'.local/testlab/environments/testlab-0123456789ab/perf/run/rps50';self.work.mkdir(parents=True)
        self.data={'owner':'testlab-0123456789ab','supervisor_pid':12,'control_secret':'synthetic-secret','source_tree_sha':'same','perf_work':str(self.work.parent.parent),'_freeze_baseline':(12,(13,14),'hash')}
        self.current={'owner':self.data['owner'],'pid':12,'api_pids':[13,14],'source_tree_sha':'same','ready':False,'failed':False}
        self.root_patch=patch.object(perf,'ROOT',self.root);self.root_patch.start()
    def tearDown(self):self.root_patch.stop();self.tmp.cleanup()
    def receipt_connection(self,current=None,proof=None):
        current=current or self.current;nonce='a'*32
        conn=Mock();conn.getresponse.return_value.status=200
        conn.getresponse.return_value.read.return_value=json.dumps({'nonce':nonce,'identity':current,'proof':proof or perf.control_proof(self.data['control_secret'],nonce,current)}).encode()
        return conn
    def test_authenticated_unready_exception_keeps_guard_and_failure_snapshot(self):
        with patch.object(perf.secrets,'token_hex',return_value='a'*32),patch.object(perf.http.client,'HTTPConnection',return_value=self.receipt_connection()):
            with self.assertRaisesRegex(Exception,'two ready registered child PIDs required') as caught:perf.identity(self.data)
        self.assertEqual(caught.exception.testlab_authenticated_identity,self.current)
    def test_metrics_strict_and_measurement_identity_failures_both_abort(self):
        original=RuntimeError('authenticated generation rejected');observer=Mock()
        with patch.object(perf,'sql',return_value='{}'),patch.object(perf,'identity',side_effect=original):
            with self.assertRaises(RuntimeError) as caught:perf.metrics(self.data)
            self.assertIs(caught.exception,original)
            with self.assertRaises(RuntimeError) as caught:perf.metrics(self.data,measurement_api_pids=(13,14),health_observer=observer)
            self.assertIs(caught.exception,original);observer.assert_not_called()
        with patch.object(perf,'identity',return_value=self.current),patch.object(perf,'sql',side_effect=RuntimeError('unavailable')),patch.object(perf.subprocess,'run',side_effect=OSError('resource unavailable')):
            actual=perf.metrics(self.data,measurement_api_pids=(13,14),health_observer=observer)
        self.assertEqual(observer.call_count,2)
        for key in ('postgres_connections_locks_queue','backend_cpu','backend_rss','backend_fd'):self.assertIn(key,actual['missing'])
        with self.assertRaisesRegex(RuntimeError,'strict ready baseline'):perf.metrics(self.data,measurement_api_pids=(15,16),health_observer=observer)
    def test_actual_default_metrics_rejects_signed_unready_and_bad_hmac(self):
        for proof in (None,'forged'):
            with self.subTest(proof=proof),patch.object(perf,'sql',return_value='{}'),patch.object(perf.secrets,'token_hex',return_value='a'*32),patch.object(perf.http.client,'HTTPConnection',return_value=self.receipt_connection(proof=proof)):
                with self.assertRaises(RuntimeError) as caught:perf.metrics(self.data)
                if proof is None:self.assertEqual(caught.exception.testlab_authenticated_identity,self.current)
                else:self.assertIn('attestation mismatch',str(caught.exception))
    def test_valid_failure_and_fresh_snapshot_health_are_private_and_bounded(self):
        error=RuntimeError('secret must not be stored');error.testlab_authenticated_identity=dict(self.current)
        conn=Mock();conn.getresponse.return_value.status=503
        conn.getresponse.return_value.read.return_value=b'{"error":{"code":"unavailable","message":"private-secret"}}'
        fresh_error=RuntimeError('still unready');fresh_error.testlab_authenticated_identity=dict(self.current)
        with patch.object(perf,'identity',side_effect=fresh_error) as identity,patch.object(perf.http.client,'HTTPConnection',return_value=conn) as constructor:
            actual=perf.stage_error_evidence(self.data,self.work,error,{'at':1,'postgres':{'waiting':3}})
        self.assertEqual(identity.call_args.args,(self.data,));self.assertEqual(identity.call_args.kwargs['timeout'],1)
        self.assertIsInstance(identity.call_args.kwargs['diagnostic_deadline'],float)
        self.assertEqual(constructor.call_args_list,[unittest.mock.call('127.0.0.1',18561,timeout=1),unittest.mock.call('127.0.0.1',18562,timeout=1)])
        self.assertEqual(conn.request.call_args_list,[unittest.mock.call('GET','/healthz')]*2)
        self.assertEqual(conn.getresponse.return_value.read.call_args_list,[unittest.mock.call(4097)]*2)
        path=self.root/actual['error_evidence'];raw=path.read_text();stored=json.loads(raw)
        self.assertEqual(path.stat().st_mode&0o777,0o600)
        self.assertNotIn('private-secret',raw);self.assertNotIn('secret must not',raw);self.assertNotIn('synthetic-secret',raw)
        self.assertEqual(stored['failure_authenticated_identity'],self.current)
        self.assertEqual(stored['fresh_authenticated_identity'],self.current)
        self.assertEqual(stored['health'][0]['http_status'],503);self.assertEqual(stored['health'][0]['error_code'],'unavailable')
        self.assertEqual(stored['resources_last']['postgres']['waiting'],3)
        self.assertIn('postgres_pool_internal_wait',stored['missing'])
    def test_invalid_hmac_owner_source_or_child_generation_never_probes_health(self):
        altered=[('bad-proof',self.current),('different-owner',{**self.current,'owner':'other'}),('different-source',{**self.current,'source_tree_sha':'other'}),('different-child',{**self.current,'api_pids':[15,16]}),('missing-child',{**self.current,'api_pids':[]})]
        for label,current in altered:
            with self.subTest(label=label),patch.object(perf.secrets,'token_hex',return_value='a'*32),patch.object(perf.http.client,'HTTPConnection',return_value=self.receipt_connection(current,proof='forged' if label=='bad-proof' else None)) as constructor:
                actual=perf.stage_error_evidence(self.data,self.work,RuntimeError('failure'))
            self.assertEqual(constructor.call_count,1) # only fixed controller attestation
            stored=json.loads((self.root/actual['error_evidence']).read_text())
            self.assertEqual(stored['health'],[]);self.assertIn('owned_api_health_mapping',stored['missing'])
            if label in ('bad-proof','different-owner'):self.assertIsNone(stored['fresh_authenticated_identity'])
    def test_stage_original_exception_and_failed_acceptance_are_preserved(self):
        data={**self.data,'base_urls':['http://127.0.0.1:18561','http://127.0.0.1:18562'],'environment':'test','users':[{'session_expires_at':9999999999}],'perf_password':'synthetic'}
        original=RuntimeError('original guard failure');proc=Mock();proc.poll.return_value=None
        diagnostic={'error_evidence':'.local/testlab/fixed/error-evidence.json','reason':'retained'}
        with patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'a'*64,'')),patch.object(perf,'k6_container_state',return_value={'id':'a'*64,'image_id':'sha256:'+'b'*64}),patch.object(perf.subprocess,'Popen',return_value=proc),patch.object(perf,'frozen',side_effect=original),patch.object(perf,'stage_error_evidence',return_value=diagnostic) as capture,patch.object(perf,'own_remove'),patch.object(perf,'result') as result:
            with self.assertRaises(RuntimeError) as caught:perf.k6(data,'run','rps50',50,300)
        self.assertIs(caught.exception,original)
        self.assertIs(capture.call_args.args[2],original)
        stage=[call.args for call in result.call_args_list if call.args[0]=='perf.rps50'][-1]
        self.assertEqual(stage[1],'failed');self.assertFalse(stage[2]['continuous_acceptance'])
        self.assertEqual(stage[2]['error_evidence'],diagnostic['error_evidence'])
    def test_health_body_size_and_timeout_errors_do_not_expose_secrets(self):
        conn=Mock();conn.getresponse.return_value.status=200
        conn.getresponse.return_value.read.side_effect=[b'x'*4097,TimeoutError('private-secret')]
        fresh_error=RuntimeError('unready');fresh_error.testlab_authenticated_identity=dict(self.current)
        with patch.object(perf,'identity',side_effect=fresh_error),patch.object(perf.http.client,'HTTPConnection',return_value=conn):
            actual=perf.stage_error_evidence(self.data,self.work,RuntimeError('original-private-secret'))
        raw=(self.root/actual['error_evidence']).read_text();stored=json.loads(raw)
        self.assertLess(len(raw),8192);self.assertNotIn('private-secret',raw)
        self.assertTrue(stored['health'][0]['body_truncated']);self.assertEqual(stored['health'][1]['error_type'],'TimeoutError')
        self.assertIn('last_resource_sample',stored['missing'])


class DiagnosticWallClockTest(unittest.TestCase):
    """Only ephemeral self-owned loopback sockets, never the formal API ports."""
    setUp=StageFailureEvidenceTest.setUp
    tearDown=StageFailureEvidenceTest.tearDown
    evidence=[]
    def slow_server(self,mode):
        listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen(4);listener.settimeout(.1)
        stopped=threading.Event();clients=[];errors=[]
        def serve():
            try:
                while not stopped.is_set():
                    try:conn,_=listener.accept()
                    except socket.timeout:continue
                    clients.append(conn);conn.settimeout(.2)
                    try:
                        conn.recv(8192)
                        if mode=='headers':chunks=[b'HTTP/1.1 200 OK\r\nX-Slow: ']+[b'a'*8]*32+[b'\r\nConnection: close\r\n\r\n{}']
                        else:chunks=[b'HTTP/1.1 200 OK\r\nConnection: close\r\n\r\n']+[b' '*8]*32+[b'{}']
                        for chunk in chunks:
                            if stopped.is_set():break
                            conn.sendall(chunk)
                            if stopped.wait(.05):break
                    except (OSError,TimeoutError):pass
                    finally:conn.close();clients.remove(conn)
            except Exception as error:errors.append(error)
        thread=threading.Thread(target=serve,name='owned-diagnostic-fixture');thread.start()
        original=perf.http.client.HTTPConnection
        def connection(host,port,timeout):
            self.assertEqual(host,'127.0.0.1');self.assertIn(port,(18560,18561,18562))
            return original('127.0.0.1',listener.getsockname()[1],timeout=timeout)
        def cleanup():
            stopped.set()
            for client in list(clients):
                try:client.shutdown(socket.SHUT_RDWR)
                except OSError:pass
            thread.join(2);listener.close()
            self.assertFalse(thread.is_alive());self.assertEqual(clients,[]);self.assertEqual(errors,[])
            self.assertEqual(listener.fileno(),-1)
        self.addCleanup(cleanup)
        return connection
    def run_slow_diagnostic(self,mode,authenticated):
        connection=self.slow_server(mode);original=RuntimeError('original failure')
        patches=[patch.object(perf.http.client,'HTTPConnection',side_effect=connection)]
        if authenticated:
            original.testlab_authenticated_identity=dict(self.current)
            fresh=RuntimeError('not ready');fresh.testlab_authenticated_identity=dict(self.current)
            patches.append(patch.object(perf,'identity',side_effect=fresh))
        for item in patches:item.start();self.addCleanup(item.stop)
        started=time.monotonic();actual=perf.stage_error_evidence(self.data,self.work,original)
        elapsed=time.monotonic()-started
        self.evidence.append({'scenario':mode,'fresh_mock_authenticated':authenticated,'elapsed_seconds':elapsed})
        self.assertLess(elapsed,1.5,f'diagnostic wall time {elapsed:.3f}s')
        self.assertIsNotNone(actual['error_evidence'])
        self.assertFalse(any(thread.name=='testlab-diagnostic-deadline' for thread in threading.enumerate()))
    def test_slow_header_trickle_has_total_deadline(self):
        with patch.object(perf,'DIAGNOSTIC_BUDGET',.6,create=True):self.run_slow_diagnostic('headers',False)
    def test_connection_close_slow_body_has_total_deadline(self):
        with patch.object(perf,'DIAGNOSTIC_BUDGET',.6,create=True):self.run_slow_diagnostic('body',True)
    def test_diagnostic_deadline_does_not_delay_original_stage_cleanup(self):
        connection=self.slow_server('body');original=RuntimeError('original guard failure')
        fresh=RuntimeError('unready');fresh.testlab_authenticated_identity=dict(self.current)
        data={**self.data,'base_urls':['http://127.0.0.1:18561','http://127.0.0.1:18562'],'environment':'test','users':[{'session_expires_at':9999999999}],'perf_password':'synthetic'}
        proc=Mock();proc.poll.return_value=None;cleanup_times=[];started=time.monotonic()
        with patch.object(perf,'DIAGNOSTIC_BUDGET',.6),patch.object(perf.http.client,'HTTPConnection',side_effect=connection),patch.object(perf,'identity',side_effect=fresh),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'a'*64,'')),patch.object(perf,'k6_container_state',return_value={'id':'a'*64,'image_id':'sha256:'+'b'*64}),patch.object(perf.subprocess,'Popen',return_value=proc),patch.object(perf,'frozen',side_effect=[None,original]),patch.object(perf,'own_remove',side_effect=lambda *args:cleanup_times.append(time.monotonic()-started)),patch.object(perf,'result') as result:
            with self.assertRaises(RuntimeError) as caught:perf.k6(data,'run','rps50',50,300)
        self.assertIs(caught.exception,original);self.assertEqual(len(cleanup_times),1);self.assertLess(cleanup_times[0],1.5)
        self.evidence.append({'scenario':'stage_cleanup','elapsed_to_cleanup_seconds':cleanup_times[0],'original_exception_preserved':True})
        stage=[call.args for call in result.call_args_list if call.args[0]=='perf.rps50'][-1]
        self.assertEqual(stage[1],'failed');self.assertFalse(stage[2]['continuous_acceptance'])
        self.assertFalse(any(thread.name=='testlab-diagnostic-deadline' for thread in threading.enumerate()))

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


class TrafficRouteEvidenceTest(unittest.TestCase):
    """Execute the load script with offline HTTP/metric stubs; no k6 or API load."""
    def execute(self,excluded=(),status=200,body='{}',gate='1',aggregate_supported=False):
        node=shutil.which('node')
        if not node:self.skipTest('Node required for offline JavaScript execution')
        source=(Path(__file__).parent/'traffic.js').read_text()
        source='\n'.join(line for line in source.splitlines() if not line.startswith('import '))
        source=source.replace('export const options=','const options=').replace('export default function(){','function iteration(){').replace('export function handleSummary','function handleSummary')
        harness=r'''
const vm=require('vm');const input=JSON.parse(require('fs').readFileSync(0,'utf8'));
const metrics={},calls=[];
class Metric {constructor(name){if(metrics[name])throw Error('duplicate metric');this.name=name;metrics[name]=[];}add(value){metrics[this.name].push(value);}}
const config={users:[{cookie:'synthetic',session_expires_at:Date.now()/1000+86400,conversation_id:'conversation-id',claim_alert_id:'alert-id',claim_revision:3}],base_urls:['http://127.0.0.1:18561'],environment:'synthetic',excluded_aggregate_paths:input.excluded,aggregate_measurement_supported:input.aggregate_supported};
const execution={vu:{idInTest:1},scenario:{iterationInTest:0}};
const respond=(method,url,body,options)=>{calls.push({method,url,body,options});return {status:input.status,body:input.body,timings:{duration:7}};};
const context={Counter:Metric,Rate:Metric,Trend:Metric,open:()=>JSON.stringify(config),__ENV:{GATE:input.gate},execution,http:{get:(url,options)=>respond('GET',url,null,options),post:(url,body,options)=>respond('POST',url,body,options)}};
vm.createContext(context);vm.runInContext(input.source+`;for(let i=0;i<50;i++){execution.scenario.iterationInTest=i;iteration();}globalThis.testOptions=options;globalThis.summary=handleSummary({metrics:{aggregate_latency:{values:{'p(95)':0}},database_latency:{values:{'p(95)':7}}},state:{testRunDurationMs:300000}});`,context);
process.stdout.write(JSON.stringify({metrics,calls,options:context.testOptions,summary:context.summary}));
'''
        result=subprocess.run([node,'-e',harness],input=json.dumps({'source':source,'excluded':list(excluded),'status':status,'body':body,'gate':gate,'aggregate_supported':aggregate_supported}),text=True,capture_output=True,check=True,timeout=10)
        return json.loads(result.stdout)
    def test_local_db_route_classification_and_individual_gate(self):
        result=self.execute();metrics=result['metrics'];thresholds=result['options']['thresholds']
        with self.subTest(classification='database'):self.assertEqual(len(metrics['database_latency']),50)
        with self.subTest(classification='aggregate'):self.assertEqual(len(metrics['aggregate_latency']),0)
        for route in ('inbox','alert_groups'):
            with self.subTest(route=route,metric='latency'):self.assertEqual(thresholds.get('route_'+route+'_latency'),['p(95)<=500','p(99)<=2000'])
            with self.subTest(route=route,metric='errors'):self.assertEqual(thresholds.get('route_'+route+'_errors'),['rate<0.001'])
        self.assertEqual(self.execute(gate='0')['options']['thresholds'],{})
        excluded=self.execute(['/api/v1/inbox','/api/v1/alert-groups'])['options']['thresholds']
        for route in ('inbox','alert_groups'):
            self.assertNotIn('route_'+route+'_latency',excluded);self.assertNotIn('route_'+route+'_errors',excluded)
    def test_preflight_groups_are_db_and_cannot_claim_aggregate_support(self):
        data={'owner':'testlab-offline','base_urls':['http://offline.invalid','http://offline-peer.invalid']}
        def request(data,base,method,path,*args,**kwargs):
            if path=='/api/v1/home':return 503,{'error':{'code':'home_result_limit'}},{}
            return 200,{},{}
        def login(data,index):return {'conversation_id':'own-'+str(index)}
        with patch.object(perf,'fixture',return_value=data),patch.object(perf,'seed',return_value={'prefix':'offline-'}),patch.object(perf,'login',side_effect=login),patch.object(perf,'sql',return_value='1'),patch.object(perf,'request',side_effect=request),patch.object(perf,'result'):
            prepared=perf.prepare('base')
        self.assertFalse(prepared['aggregate_measurement_supported'])
        routes={row['path']:row for row in prepared['route_preflight']}
        self.assertEqual(routes['/api/v1/alert-groups'].get('kind'),'db')
        self.assertEqual(routes['/api/v1/inbox'].get('kind'),'db')
    def test_home_is_db_with_eligible_gate_and_no_aggregate_samples(self):
        result=self.execute();metrics=result['metrics'];thresholds=result['options']['thresholds']
        with self.subTest(classification='database'):self.assertEqual(len(metrics['database_latency']),50)
        with self.subTest(classification='aggregate'):self.assertEqual(metrics['aggregate_latency'],[])
        with self.subTest(metric='latency'):self.assertEqual(thresholds.get('route_home_latency'),['p(95)<=500','p(99)<=2000'])
        with self.subTest(metric='errors'):self.assertEqual(thresholds.get('route_home_errors'),['rate<0.001'])
        excluded=self.execute(['/api/v1/home'])['options']['thresholds']
        self.assertNotIn('route_home_latency',excluded);self.assertNotIn('route_home_errors',excluded)
        data={'owner':'testlab-offline','base_urls':['http://offline.invalid','http://offline-peer.invalid']}
        with patch.object(perf,'fixture',return_value=data),patch.object(perf,'seed',return_value={'prefix':'offline-'}),patch.object(perf,'login',side_effect=lambda data,index:{'conversation_id':'own-'+str(index)}),patch.object(perf,'sql',return_value='1'),patch.object(perf,'request',return_value=(200,{},{})),patch.object(perf,'result'):
            prepared=perf.prepare('base')
        with self.subTest(preflight='aggregate_support'):self.assertFalse(prepared['aggregate_measurement_supported'])
        self.assertEqual({row['kind'] for row in prepared['route_preflight']},{'db'})
        summary=json.loads(result['summary']['/private/summary.json'])
        self.assertNotIn('aggregate_latency',summary['metrics']);self.assertEqual(summary['metrics']['database_latency']['values']['p(95)'],7)
        self.assertEqual(summary['state']['testRunDurationMs'],300000)
        legacy=self.execute(aggregate_supported=True)
        self.assertEqual(legacy['options']['thresholds']['aggregate_latency'],['p(95)<=2000','p(99)<=5000'])
        self.assertIn('aggregate_latency',json.loads(legacy['summary']['/private/summary.json'])['metrics'])
    def test_fixed_routes_cover_actual_requests_and_preserve_load_gates(self):
        result=self.execute();metrics=result['metrics'];calls=result['calls']
        expected={'conversation_list':15,'conversation_detail':14,'inbox':10,'alert_detail':0,'alert_claim':2,'alert_groups':5,'home':4}
        self.assertEqual(len(calls),50)
        for route,count in expected.items():
            self.assertEqual(len(metrics['route_'+route+'_latency']),count)
            self.assertEqual(metrics['route_'+route+'_latency'],[7]*count)
            self.assertEqual(metrics['route_'+route+'_errors'],[False]*count)
            self.assertEqual(metrics['route_'+route+'_requests'],[1]*count)
        expected_paths=['/api/v1/conversations','/api/v1/conversations/conversation-id','/api/v1/inbox','/api/v1/conversations','/api/v1/conversations/conversation-id','/api/v1/inbox','/api/v1/conversations','/api/v1/conversations/conversation-id','/api/v1/alert-groups','/api/v1/home']
        for i,call in enumerate(calls):
            write=i%25==24
            self.assertEqual(call['method'],'POST' if write else 'GET')
            self.assertEqual(call['url'],'http://host.docker.internal:18561'+('/api/v1/alerts/alert-id/claim' if write else expected_paths[i%10]))
            self.assertEqual(call['options']['timeout'],'15s')
            self.assertEqual(call['options']['headers']['Cookie'],'aiops_session=synthetic')
            if write:self.assertEqual(json.loads(call['body']),{'expected_revision':3})
        self.assertEqual(len(metrics['database_latency']),50)
        self.assertEqual(len(metrics['aggregate_latency']),0)
        self.assertEqual(metrics['bounded_write_latency'],[7,7])
        self.assertEqual(result['options']['thresholds'],{'dropped_iterations':['count==0'],'unexpected_errors':['rate<0.001'],'bounded_write_errors':['rate<0.001'],'database_latency':['p(95)<=500','p(99)<=2000'],'route_inbox_latency':['p(95)<=500','p(99)<=2000'],'route_inbox_errors':['rate<0.001'],'route_alert_groups_latency':['p(95)<=500','p(99)<=2000'],'route_alert_groups_errors':['rate<0.001'],'route_home_latency':['p(95)<=500','p(99)<=2000'],'route_home_errors':['rate<0.001']})
        self.assertEqual(result['options']['summaryTrendStats'],['avg','min','med','max','p(95)','p(99)'])
        self.assertEqual(result['options']['scenarios']['traffic'],{'executor':'constant-arrival-rate','rate':10,'timeUnit':'1s','duration':'300s','preAllocatedVUs':40,'maxVUs':200,'gracefulStop':'15s'})
        self.assertIn('/private/summary.json',result['summary'])
        self.assertEqual(len([name for name in metrics if name.startswith('route_')]),21)
    def test_excluded_routes_record_actual_fallback_and_existing_classification(self):
        result=self.execute(['/api/v1/home','/api/v1/alert-groups','/api/v1/inbox'])
        metrics=result['metrics']
        self.assertEqual(len(metrics['route_alert_detail_requests']),19)
        for route in ('home','alert_groups','inbox'):self.assertEqual(metrics['route_'+route+'_requests'],[])
        self.assertEqual(len(metrics['database_latency']),50)
        self.assertEqual(metrics['aggregate_latency'],[])
        self.assertEqual(len(metrics['route_alert_claim_requests']),2)
    def test_status_error_envelope_and_malformed_body_remain_failures(self):
        for status,body in ((503,'{}'),(200,'{"error":"synthetic"}'),(200,'malformed')):
            with self.subTest(status=status,body=body):
                metrics=self.execute(status=status,body=body)['metrics']
                self.assertEqual(metrics['unexpected_errors'],[True]*50)
                self.assertEqual(metrics['bounded_write_errors'],[True]*2)
                for name,values in metrics.items():
                    if name.startswith('route_') and name.endswith('_errors'):self.assertTrue(all(values))

if __name__=='__main__':unittest.main()
