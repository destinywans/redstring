"""Offline generator lifecycle regressions; subprocesses are local Python, Docker is stubbed."""
import json,sys,tempfile,time,unittest,subprocess,socket,threading,signal,os
from pathlib import Path
from unittest.mock import patch,Mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import perf

class GeneratorLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.previous_signals={sig:signal.getsignal(sig) for sig in (signal.SIGINT,signal.SIGTERM)}
        perf.STOP.clear()
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        self.work=self.root/'.local/testlab/environments/testlab-0123456789ab/perf'
        self.work.mkdir(parents=True)
        self.data={'owner':'testlab-0123456789ab','supervisor_pid':12,'source_tree_sha':'same','perf_work':str(self.work),'base_urls':['http://127.0.0.1:18561','http://127.0.0.1:18562'],'environment':'test','users':[{'session_expires_at':9999999999}],'perf_password':'private-test-secret'}
        self.name=None;self.cid='a'*64;self.image='sha256:'+'b'*64;self.commands=[];self.rows=[];self.children=[]
        self.lost_create_response=False;self.lookup_after_start=False;self.create_error=None;self.inspect_count=0;self.replace_image=False;self.replace_id=False;self.replace_name=False;self.wrong_configured_image=False;self.start_error=False;self.mode='oom';self.lookup_unavailable=False;self.foreign=False;self.remove_failure=False
        self.real_popen=subprocess.Popen
        self.patches=[patch.object(perf,'ROOT',self.root),patch.object(perf.subprocess,'run',side_effect=self.docker),patch.object(perf.subprocess,'Popen',side_effect=self.child),patch.object(perf,'frozen'),patch.object(perf,'metrics',return_value={'at':1,'postgres':{}}),patch.object(perf,'identity',side_effect=RuntimeError('unavailable')),patch.object(perf,'result',side_effect=lambda *args,**kw:self.rows.append(args)),patch.object(perf.STOP,'wait',side_effect=lambda timeout:time.sleep(.01))]
        for p in self.patches:p.start()
    def tearDown(self):
        for p in reversed(self.patches):p.stop()
        for child in self.children:
            if child.poll() is None:child.kill()
            child.wait(timeout=2)
        self.tmp.cleanup();perf.STOP.clear()
        for sig,handler in self.previous_signals.items():signal.signal(sig,handler)
        if hasattr(perf,'_CANCEL_DEADLINE'):perf._CANCEL_DEADLINE=None
        if hasattr(perf,'_STOP_REQUESTED'):perf._STOP_REQUESTED=False
    def docker(self,cmd,**kwargs):
        self.commands.append(cmd)
        if cmd[1]=='create':
            self.name=cmd[cmd.index('--name')+1]
            if self.create_error is not None:raise self.create_error
            return subprocess.CompletedProcess(cmd,0,'' if self.lost_create_response else self.cid+'\n','')
        if cmd[1]=='inspect':
            self.inspect_count+=1
            if self.lookup_unavailable or self.lookup_after_start and self.inspect_count>1:return subprocess.CompletedProcess(cmd,1,'','private daemon error')
            if cmd[-1]=='{{json .Config.Labels}}':return subprocess.CompletedProcess(cmd,0,json.dumps({'aiops.testlab.owner':'foreign' if self.foreign else self.data['owner']}),'')
            value={'id':self.cid,'name':'/'+self.name,'owner':'foreign' if self.foreign else self.data['owner'],'configured_image':perf.K6_IMAGE,'image_id':self.image,'state':{'Running':False,'OOMKilled':self.mode=='oom' and self.inspect_count>1,'ExitCode':137 if self.mode=='oom' and self.inspect_count>1 else 0,'FinishedAt':'2026-10-01T01:15:00Z'}}
            if self.inspect_count>1 and self.mode=='threshold':value['state']['ExitCode']=99
            if self.inspect_count>1 and self.replace_image:value['image_id']='sha256:'+'c'*64
            if self.inspect_count>1 and self.replace_id:value['id']='d'*64
            if self.inspect_count>1 and self.replace_name:value['name']='/foreign'
            if self.wrong_configured_image:value['configured_image']='other/image:latest'
            return subprocess.CompletedProcess(cmd,0,json.dumps(value),'')
        if cmd[1]=='rm':return subprocess.CompletedProcess(cmd,1 if self.remove_failure else 0,'','private cleanup secret')
        raise AssertionError('unexpected subprocess.run '+str(cmd))
    def child(self,cmd,**kwargs):
        self.commands.append(cmd)
        if self.start_error:raise OSError('private start failure')
        code='import sys,time;time.sleep(.025);sys.exit('+('137' if self.mode=='oom' else '0')+')'
        child=self.real_popen([sys.executable,'-c',code],**kwargs);self.children.append(child);return child
    def execute(self):return perf.k6(self.data,'run','rps50',50,.01,strict_source=True)
    def failure(self):
        with self.assertRaisesRegex(RuntimeError,'k6 exited without real summary') as caught:self.execute()
        path=self.work/'run/rps50/error-evidence.json'
        stored=json.loads(path.read_text());self.assertEqual(path.stat().st_mode&0o777,0o600)
        self.assertNotIn('private-test-secret',path.read_text());return caught.exception,stored
    def test_actual_child_137_missing_summary_retains_owned_state_before_cleanup(self):
        error,stored=self.failure()
        self.assertEqual(stored['reason_code'],'k6_missing_summary')
        self.assertEqual(stored['reason'],str(error))
        self.assertEqual(stored['generator']['proc_returncode'],137)
        container=stored['generator']['container'];self.assertEqual(container['id'],self.cid)
        self.assertEqual(container['image_id'],self.image);self.assertTrue(container['state']['OOMKilled'])
        self.assertEqual(container['state']['ExitCode'],137);self.assertTrue(container['state']['FinishedAt'])
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))

    def test_readiness_observation_never_relaxes_admission_gate_soak_or_other_windows(self):
        for stage,rate,duration,gate,hooks in [('rps50',50,300,False,None),('rps25',25,300,True,None),
                ('soak24h',10,86400,True,{}),('rps50',50,300,True,None),('rps50',50,299,False,None),
                ('rps50',49,300,False,None),('burst100',100,60,False,{}),('warmup',10,120,False,None)]:
            with self.subTest(stage=stage,rate=rate,duration=duration,gate=gate,hooks=hooks):
                def unready(*args,**kwargs):
                    self.assertNotIn('measurement_api_pids',kwargs)
                    raise RuntimeError('two ready registered child PIDs required')
                with patch.object(perf,'frozen',side_effect=unready),self.assertRaisesRegex(RuntimeError,'two ready'):
                    perf.k6(self.data,'run',stage,rate,duration,gate,hooks=hooks)
                self.assertFalse(any(row[0]=='perf.'+stage and row[1]=='passed' for row in self.rows))
        self.assertFalse(any('--rm' in c for c in self.commands));self.assertTrue(all(p.poll() is not None for p in self.children))
    def test_normal_complete_stage_also_removes_captured_id(self):
        self.mode='normal';stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        good,actual=self.execute();self.assertTrue(good);self.assertEqual(actual['generator']['proc_returncode'],0)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
    def test_cancel_removes_owned_child_and_container(self):
        with patch.object(perf,'frozen',side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):self.execute()
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertTrue(all(p.poll() is not None for p in self.children))
    def test_foreign_owner_is_never_removed_or_started(self):
        self.foreign=True
        with self.assertRaises(RuntimeError):self.execute()
        self.assertFalse(any(c[1] in ('rm','start') for c in self.commands))
    def test_missing_lookup_is_explicit_and_never_adopted(self):
        self.lookup_unavailable=True
        with self.assertRaises(RuntimeError):self.execute()
        self.assertFalse(any(c[1] in ('rm','start') for c in self.commands))
        self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))
    def test_cleanup_failure_keeps_original_137_error_and_is_independent_failed(self):
        self.remove_failure=True;error,stored=self.failure()
        self.assertEqual(stored['generator']['proc_returncode'],137)
        self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))
        self.assertFalse(any(row[1]=='passed' for row in self.rows))

    def test_create_response_timeout_recovers_exact_nonce_identity_and_removes(self):
        self.create_error=subprocess.TimeoutExpired(['docker','create'],20)
        with self.assertRaises(subprocess.TimeoutExpired) as caught:self.execute()
        self.assertIs(caught.exception,self.create_error)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertEqual(sum(c[1]=='create' for c in self.commands),1)
        self.assertFalse(any(c[1]=='start' for c in self.commands))
    def test_create_cancel_still_recovers_and_removes_owned_partial_acquisition(self):
        self.create_error=KeyboardInterrupt()
        with self.assertRaises(KeyboardInterrupt):self.execute()
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
    def test_start_attach_failure_keeps_primary_and_cleans_created_container(self):
        self.start_error=True
        with self.assertRaisesRegex(OSError,'private start failure'):self.execute()
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        raw=(self.work/'run/rps50/error-evidence.json').read_text();self.assertNotIn('private start failure',raw)
    def test_replaced_identity_or_image_never_removed(self):
        for attr in ('replace_image','replace_id','replace_name'):
            with self.subTest(attr=attr):
                setattr(self,attr,True);self.inspect_count=0;self.commands.clear();self.rows.clear()
                error,stored=self.failure()
                self.assertIn('container_state',stored['generator']['missing'])
                self.assertFalse(any(c[1]=='rm' for c in self.commands))
                self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))
                setattr(self,attr,False)
    def test_wrong_pinned_configured_image_is_not_started_or_removed(self):
        self.wrong_configured_image=True
        with self.assertRaises(RuntimeError):self.execute()
        self.assertFalse(any(c[1] in ('rm','start') for c in self.commands))
    def test_cleanup_failure_after_normal_stage_cannot_emit_passed_stage(self):
        self.mode='normal';self.remove_failure=True;stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        with self.assertRaisesRegex(RuntimeError,'owned k6 cleanup failed'):self.execute()
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
        self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))
    def test_resource_limits_and_original_generator_contract_unchanged(self):
        self.failure();create=next(c for c in self.commands if c[1]=='create')
        for flag,value in (('--cpus','2'),('--memory','4g'),('--user','0:0')):self.assertEqual(create[create.index(flag)+1],value)
        self.assertIn('RATE=50',create);self.assertIn('GATE=0',create);self.assertIn(perf.K6_IMAGE,create)
        self.assertEqual(create[-3:],['run','--quiet','/load.js'])
        self.assertTrue(all('private-test-secret' not in str(cmd) for cmd in self.commands))

    def assert_declared_resources(self,value):
        self.assertEqual(value,{'memory_limit':'4g','cpus':2,'image':perf.K6_IMAGE,'owner':self.data['owner'],'evidence_mode':'declared_config'})
    def test_oom_with_new_declared_memory_remains_failed_and_never_retries(self):
        error,stored=self.failure()
        self.assert_declared_resources(stored['generator']['declared_resources'])
        self.assertTrue(stored['generator']['container']['state']['OOMKilled'])
        self.assertEqual(stored['generator']['container']['state']['ExitCode'],137)
        self.assertEqual(sum(c[1]=='create' for c in self.commands),1)
        self.assertEqual(sum(c[1]=='start' for c in self.commands),1)
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
        failed=next(row for row in self.rows if row[0]=='perf.rps50' and row[1]=='failed')
        self.assert_declared_resources(failed[2]['generator']['declared_resources'])
    def test_declared_resources_visible_in_running_and_completed_result(self):
        self.mode='normal';stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        good,actual=self.execute();self.assertTrue(good)
        self.assert_declared_resources(actual['generator']['declared_resources'])
        self.assert_declared_resources(perf.dashboard_metrics(actual)['declared_generator_resources'])
        running=[row for row in self.rows if row[0]=='perf.rps50' and row[1]=='running']
        self.assertTrue(running)
        for row in running:self.assert_declared_resources(row[2]['declared_generator_resources'])
    def test_cancelled_generator_still_declares_scoped_resources_without_pass(self):
        with patch.object(perf,'frozen',side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):self.execute()
        interrupted=next(row for row in self.rows if row[0]=='perf.rps50' and row[1]=='interrupted')
        self.assert_declared_resources(interrupted[2]['generator']['declared_resources'])
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))

    def test_explicit_high_load_unready_completes_measurement_but_still_fails(self):
        self.run_high_load_observation('rps50',50,300)
    def test_explicit_burst100_unready_then_ready_still_latches_failure(self):
        self.run_high_load_observation('burst100',100,60,recover=True)
    def test_explicit_high_load_healthy_remains_accepted_measurement(self):
        self.run_high_load_observation('rps50',50,300,healthy=True)
    def run_high_load_observation(self,stage_name,rate,duration,recover=False,healthy=False):
        self.mode='normal';stage=self.work/'run'/stage_name;stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':duration*1000},'metrics':{'iterations':{'values':{'count':rate*duration}}}}))
        calls=[]
        current={'owner':self.data['owner'],'pid':12,'api_pids':[13,14],'source_tree_sha':'same','ready':False,'failed':False}
        def sample(data,*,measurement_api_pids=None,health_observer=None):
            calls.append(measurement_api_pids)
            if len(calls)==1:
                self.data['_freeze_baseline']=(12,(13,14),'hash');return 'same'
            if measurement_api_pids is None:raise RuntimeError('two ready registered child PIDs required')
            self.assertEqual(measurement_api_pids,(13,14));health_observer({**current,'ready':healthy or recover and len(calls)>2});return 'same'
        def child(cmd,**kwargs):
            self.commands.append(cmd)
            process=self.real_popen([sys.executable,'-c','import time;time.sleep(.07)'],**kwargs);self.children.append(process);return process
        def clock():return 500 if self.children and self.children[-1].poll() is not None else 0
        with patch.object(perf,'frozen',side_effect=sample),patch.object(perf.time,'monotonic',side_effect=clock),patch.object(perf.subprocess,'Popen',side_effect=child):
            good,actual=perf.k6(self.data,'run',stage_name,rate,duration,False)
        self.assertEqual(good,healthy);self.assertTrue(actual['measurement_completed'])
        self.assertEqual(actual['health_degraded'],not healthy);self.assertEqual(actual['saturated'],not healthy)
        self.assertEqual(perf.dashboard_metrics(actual)['health_degraded'],not healthy)
        if not healthy:
            self.assertGreater(actual['health_observation']['unready_samples'],0)
            self.assertEqual(actual['health_observation']['snapshots'][0]['identity'],current)
            path=self.root/actual['health_evidence'];self.assertEqual(path.stat().st_mode&0o777,0o600)
            rows=[json.loads(line) for line in path.read_text().splitlines()]
            self.assertEqual(len(rows),actual['health_observation']['unready_samples'])
            self.assertNotIn('private-test-secret',path.read_text())
            self.assertEqual(actual['generator']['health_degraded'],True)
            self.assertIn('not exact downtime',actual['health_observation']['span_interpretation'])
            if recover:self.assertGreater(actual['health_observation']['samples'],actual['health_observation']['unready_samples'])
        self.assertTrue(any(row[0]=='perf.'+stage_name and row[1]==('passed' if healthy else 'failed') for row in self.rows))
        self.assertFalse(any(row[0]=='perf.'+stage_name and row[1]==('failed' if healthy else 'passed') for row in self.rows))
        self.assertEqual(sum(c[1]=='create' for c in self.commands),1)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
    def test_main_returns_nonzero_after_degraded_measurement(self):
        self.data['counts']={'exact_profile':True};streams=Mock();streams.snapshot.return_value={'peak_normal':50,'peak_slow':10}
        def stage(data,run,name,*args):return name!='rps50',{'health_degraded':name=='rps50'}
        with patch.object(perf.sys,'argv',['perf.py','load','--profile','base']),patch.object(perf,'fixture',return_value=self.data),patch.object(perf,'prepare',return_value=self.data),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'k6 v1.5.0','')),patch.object(perf,'Streams',return_value=streams),patch.object(perf,'k6',side_effect=stage),patch.object(perf,'sse_accepted',return_value=True),patch.object(perf,'resource_recovery',return_value=(True,{})),patch.object(perf,'consistency',return_value=True):
            self.assertEqual(perf.main(),1)
        streams.close.assert_called_once()
    def test_actual_metrics_unready_between_healthy_freezes_is_latched(self):
        good,actual,calls=self.run_actual_metrics_interleaving()
        self.assertFalse(good)
        self.assertTrue(actual['measurement_completed']);self.assertTrue(actual['health_degraded'])
        self.assertGreaterEqual(calls,2)
        self.assertEqual(actual['health_observation']['unready_samples'],1)
        self.assertEqual(actual['resources_last']['backend_processes'][0]['pid'],13)
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
    def test_actual_metrics_invalid_hmac_aborts_instead_of_missing_resource(self):
        with self.assertRaisesRegex(RuntimeError,'attestation mismatch'):self.run_actual_metrics_interleaving(forged=True)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
    def test_actual_metrics_changed_generation_aborts_instead_of_missing_resource(self):
        changed={'owner':self.data['owner'],'pid':12,'api_pids':[15,16],'source_tree_sha':'same','ready':False,'failed':False}
        with self.assertRaisesRegex(RuntimeError,'identity changed'):self.run_actual_metrics_interleaving(altered=changed)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
    def test_actual_metrics_fd_second_identity_drift_aborts_after_first_sample(self):
        changed={'owner':self.data['owner'],'pid':12,'api_pids':[13,14],'source_tree_sha':'changed','ready':True,'failed':False}
        with self.assertRaisesRegex(RuntimeError,'identity changed'):self.run_actual_metrics_interleaving(altered=changed,altered_at=1)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        stored=json.loads((self.work/'run/rps50/error-evidence.json').read_text())
        self.assertTrue(stored['generator']['health_degraded'])
        self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
    def test_actual_gate25_strict_metrics_unready_preserves_primary_and_cleans(self):
        with self.assertRaisesRegex(RuntimeError,'two ready'):self.run_actual_metrics_interleaving(stage_name='rps25',rate=25,gate=True)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertTrue(all(p.poll() is not None for p in self.children))
        self.assertFalse(any(row[0]=='perf.rps25' and row[1]=='passed' for row in self.rows))
    def test_actual_soak_strict_metrics_bad_hmac_preserves_primary_and_cleans(self):
        with self.assertRaisesRegex(RuntimeError,'attestation mismatch'):self.run_actual_metrics_interleaving(stage_name='soak24h',rate=10,duration=86400,gate=True,hooks={},forged=True)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertTrue(all(p.poll() is not None for p in self.children))
        self.assertFalse(any(row[0]=='perf.soak24h' and row[1]=='passed' for row in self.rows))
    def run_actual_metrics_interleaving(self,altered=None,forged=False,altered_at=0,stage_name='rps50',rate=50,duration=300,gate=False,hooks=None):
        self.mode='normal';stage=self.work/'run'/stage_name;stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':duration*1000},'metrics':{'iterations':{'values':{'count':rate*duration}}}}))
        self.data['control_secret']='synthetic-control-secret';calls=[]
        healthy={'owner':self.data['owner'],'pid':12,'api_pids':[13,14],'source_tree_sha':'same','ready':True,'failed':False}
        real_metrics=self.patches[4].temp_original;real_identity=self.patches[5].temp_original;docker=self.docker
        primary=[]
        def checked_identity(*args,**kwargs):
            try:return real_identity(*args,**kwargs)
            except BaseException as error:
                primary.append(error);raise
        def connection(*args,**kwargs):
            conn=Mock();nonce=[]
            def request(method,path):nonce.append(path.rsplit('=',1)[1])
            def read(limit):
                current={**healthy,'ready':False} if not calls else healthy
                if altered is not None and len(calls)==altered_at:current=altered
                calls.append(current)
                return json.dumps({'nonce':nonce[0],'identity':current,'proof':'forged' if forged else perf.control_proof(self.data['control_secret'],nonce[0],current)}).encode()
            conn.request.side_effect=request;conn.getresponse.return_value.status=200;conn.getresponse.return_value.read.side_effect=read;return conn
        def frozen(data,*,measurement_api_pids=None,health_observer=None):
            if measurement_api_pids is None:self.data['_freeze_baseline']=(12,(13,14),'hash')
            else:health_observer(healthy)
            return 'same'
        def command(cmd,**kwargs):
            if cmd[0]=='ps':return subprocess.CompletedProcess(cmd,0,'100 1.0','')
            if cmd[0]=='lsof':return subprocess.CompletedProcess(cmd,0,'p13\nf3\n','')
            return docker(cmd,**kwargs)
        def child(cmd,**kwargs):
            self.commands.append(cmd);process=self.real_popen([sys.executable,'-c','import time;time.sleep(.07)'],**kwargs);self.children.append(process);return process
        def clock():return 500 if self.children and self.children[-1].poll() is not None else 0
        with patch.object(perf,'metrics',side_effect=real_metrics),patch.object(perf,'identity',side_effect=checked_identity),patch.object(perf,'sql',return_value='{}'),patch.object(perf.http.client,'HTTPConnection',side_effect=connection),patch.object(perf,'frozen',side_effect=frozen),patch.object(perf.subprocess,'run',side_effect=command),patch.object(perf.subprocess,'Popen',side_effect=child),patch.object(perf.time,'monotonic',side_effect=clock):
            try:good,actual=perf.k6(self.data,'run',stage_name,rate,duration,gate,hooks=hooks)
            except BaseException as error:
                self.assertIs(error,primary[0]);raise
        return good,actual,len(calls)

    def test_daemon_lookup_loss_after_child_exit_retains_returncode_and_explicit_missing(self):
        self.lookup_after_start=True;error,stored=self.failure()
        self.assertEqual(stored['generator']['proc_returncode'],137)
        self.assertIn('container_state',stored['generator']['missing'])
        self.assertFalse(any(c[1]=='rm' for c in self.commands))
        self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))
    def test_partial_create_timeout_never_adopts_foreign_resource(self):
        self.create_error=subprocess.TimeoutExpired(['docker','create'],20);self.foreign=True
        with self.assertRaises(subprocess.TimeoutExpired):self.execute()
        self.assertFalse(any(c[1] in ('rm','start') for c in self.commands))
        self.assertTrue(any(row[0]=='perf.rps50.cleanup' and row[1]=='failed' for row in self.rows))

    def test_attach_cli_zero_cannot_hide_real_container_threshold_exit(self):
        self.mode='threshold';stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        good,actual=self.execute();self.assertFalse(good)
        self.assertEqual(actual['generator']['proc_returncode'],0)
        self.assertEqual(actual['generator']['container']['state']['ExitCode'],99)
        self.assertTrue(any(row[0]=='perf.rps50' and row[1]=='failed' for row in self.rows))
    def test_create_lost_stdout_recovers_owned_identity_without_create_retry(self):
        self.lost_create_response=True
        with self.assertRaisesRegex(RuntimeError,'creation failed'):self.execute()
        self.assertEqual(sum(c[1]=='create' for c in self.commands),1)
        self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
        self.assertFalse(any(c[1]=='start' for c in self.commands))

    def test_cleanup_broken_pipe_preserves_exact_missing_summary_primary(self):
        def broken(*args,**kw):
            if args[0].endswith('.cleanup'):raise BrokenPipeError('private reporting secret')
            self.rows.append(args)
        with patch.object(perf,'result',side_effect=broken):error,stored=self.failure()
        self.assertEqual(stored['generator']['proc_returncode'],137)
        report=json.loads((self.work/'run/rps50/cleanup-reporting-error.json').read_text())
        self.assertEqual(report['errors'][0]['error_type'],'BrokenPipeError')
        self.assertNotIn('private reporting secret',json.dumps(report))
    def test_cleanup_broken_pipe_preserves_same_keyboard_interrupt(self):
        primary=KeyboardInterrupt()
        def broken(*args,**kw):
            if args[0].endswith('.cleanup'):raise BrokenPipeError('private reporting secret')
        with patch.object(perf,'frozen',side_effect=primary),patch.object(perf,'result',side_effect=broken):
            with self.assertRaises(KeyboardInterrupt) as caught:self.execute()
        self.assertIs(caught.exception,primary)
        self.assertTrue(all(child.poll() is not None for child in self.children))
        self.assertTrue((self.work/'run/rps50/cleanup-reporting-error.json').exists())
    def test_registered_sigterm_aborts_real_body_read_and_shares_cleanup_budget(self):
        listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen();listener.settimeout(2)
        finished=threading.Event();ready=threading.Event();transport=[]
        def server():
            conn=None
            try:
                conn,_=listener.accept();transport.append(conn);conn.recv(8192)
                conn.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 1000\r\n\r\n{')
                ready.set();finished.wait(1)
            finally:
                if conn:conn.close()
                listener.close()
        thread=threading.Thread(target=server);thread.start();previous=signal.getsignal(signal.SIGTERM)
        signal.signal(signal.SIGTERM,getattr(perf,'cancel_signal',lambda *args:perf.STOP.set()))
        def cancel():
            if ready.wait(2):os.kill(os.getpid(),signal.SIGTERM)
        canceler=threading.Thread(target=cancel);canceler.start()
        real_connection=perf.http.client.HTTPConnection
        def connection(host,port,timeout):return real_connection('127.0.0.1',listener.getsockname()[1],timeout=timeout)
        docker=self.docker
        def delayed(cmd,**kw):
            if cmd[1] in ('inspect','rm') and self.inspect_count>=1:
                duration=.06;timeout=kw.get('timeout',999)
                time.sleep(min(duration,timeout))
                if duration>timeout:raise subprocess.TimeoutExpired(cmd,timeout)
            return docker(cmd,**kw)
        started=time.monotonic();caught=None
        try:
            with patch.object(perf,'CANCEL_BUDGET',.3,create=True),patch.object(perf.http.client,'HTTPConnection',side_effect=connection),patch.object(perf.subprocess,'run',side_effect=delayed),patch.object(perf,'frozen',side_effect=lambda *args:perf.request(self.data,self.data['base_urls'][0],'GET','/api/v1/testlab',timeout=30)):
                try:self.execute()
                except BaseException as error:caught=error
            elapsed=time.monotonic()-started
            print('offline_cancellation_evidence='+json.dumps({'elapsed_seconds':elapsed,'shared_budget_seconds':.3,'registered_signal':'SIGTERM','body_socket_timeout_seconds':30,'docker_stub_delay_seconds':.06,'child_reaped':all(child.poll() is not None for child in self.children)}))
            self.assertLess(elapsed,.6)
            self.assertIsInstance(caught,KeyboardInterrupt)
            self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
            self.assertTrue(all(child.poll() is not None for child in self.children))
            self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
        finally:
            finished.set();thread.join(2);canceler.join(2);signal.signal(signal.SIGTERM,previous)
            self.assertFalse(thread.is_alive());self.assertFalse(canceler.is_alive())

    def test_first_sigterm_during_cleanup_finishes_remove_and_is_not_stage_pass(self):
        self.mode='normal';stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        old=signal.getsignal(signal.SIGTERM);signal.signal(signal.SIGTERM,perf.cancel_signal);docker=self.docker
        def term_in_remove(cmd,**kw):
            if cmd[1]=='rm':os.kill(os.getpid(),signal.SIGTERM)
            return docker(cmd,**kw)
        try:
            with patch.object(perf.subprocess,'run',side_effect=term_in_remove):
                with self.assertRaises(KeyboardInterrupt):self.execute()
            self.assertTrue(any(c[1]=='rm' and c[-1]==self.cid for c in self.commands))
            self.assertTrue(all(child.poll() is not None for child in self.children))
            self.assertFalse(any(row[0]=='perf.rps50' and row[1]=='passed' for row in self.rows))
        finally:signal.signal(signal.SIGTERM,old)
    def test_main_reporting_broken_pipe_does_not_replace_failure_exit(self):
        for primary,expected in ((RuntimeError('k6 exited without real summary'),2),(KeyboardInterrupt(),130)):
            with self.subTest(primary=type(primary).__name__),patch.object(sys,'argv',['perf.py','prepare']),patch.object(perf,'prepare',side_effect=primary),patch.object(perf,'result',side_effect=BrokenPipeError()):
                self.assertEqual(perf.main(),expected)
    def test_main_no_primary_stream_cleanup_failure_is_nonzero(self):
        stats={'stable_samples':1,'window_violations':0,'protocol_errors':0,'producer_errors':0,'normal_frames':1,'slow_bytes':1,'reconnects':1,'peak_normal':50,'peak_slow':10}
        from unittest.mock import Mock
        streams=Mock();streams.close.side_effect=RuntimeError('controlled stream cleanup failure')
        data={**self.data,'counts':{'profile':'check'}}
        with patch.object(sys,'argv',['perf.py','self-check']),patch.object(perf,'prepare',return_value=data),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'k6 v1.5.0','')),patch.object(perf,'Streams',return_value=streams),patch.object(perf,'k6',return_value=(True,{'sse':stats})):
            with self.assertRaisesRegex(RuntimeError,'cleanup incomplete'):perf.main()
        streams.close.assert_called_once()

    def test_first_registered_term_inside_cleanup_sql_cannot_return_success(self):
        from unittest.mock import Mock
        stats={'stable_samples':1,'window_violations':0,'protocol_errors':0,'producer_errors':0,'normal_frames':1,'slow_bytes':1,'reconnects':1,'peak_normal':50,'peak_slow':10}
        for phase in ('stream','trial'):
            with self.subTest(phase=phase):
                perf._STOP_REQUESTED=False;perf._CANCEL_DEADLINE=None;perf.STOP.clear();self.rows.clear()
                data={**self.data,'counts':{'profile':'check'}}
                if phase=='trial':data['_soak_trial']={'id':'own-id','key':'own-key'}
                streams=Mock()
                def sql_term(*args,**kw):
                    signal.getsignal(signal.SIGTERM)(signal.SIGTERM,None)
                    return ''
                if phase=='stream':
                    def close():
                        if hasattr(perf,'cleanup_sql'):perf.cleanup_sql(data,'owned statement')
                        else:perf.stream_finish(data,'owned-prefix')
                    streams.close.side_effect=close
                with patch.object(sys,'argv',['perf.py','self-check']),patch.object(perf,'prepare',return_value=data),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'k6 v1.5.0','')),patch.object(perf,'Streams',return_value=streams),patch.object(perf,'k6',return_value=(True,{'sse':stats})),patch.object(perf,'sql',side_effect=sql_term),patch.object(perf,'stream_finish',side_effect=lambda *args:perf.sql(data,'owned statement')),patch.object(perf,'cleanup_sql',side_effect=sql_term,create=True):
                    outcome=perf.main()
                self.assertEqual(outcome,130)
                self.assertTrue(any(row[0]=='perf.interrupted' and row[1]=='interrupted' for row in self.rows))
                streams.close.assert_called_once()

    def test_registered_term_in_real_blocked_cleanup_children_is_bounded_and_pending(self):
        from unittest.mock import Mock
        stats={'stable_samples':1,'window_violations':0,'protocol_errors':0,'producer_errors':0,'normal_frames':1,'slow_bytes':1,'reconnects':1,'peak_normal':50,'peak_slow':10}
        for phase in ('stream','trial'):
            with self.subTest(phase=phase):
                perf._STOP_REQUESTED=False;perf._CANCEL_DEADLINE=None;perf.STOP.clear();self.rows.clear()
                data={**self.data,'counts':{'profile':'check'},'postgres_container':'owned-stub-pg'};children=[];timers=[];commands=[]
                if phase=='trial':data['_soak_trial']={'id':'own-id','key':'own-key'}
                streams=Mock()
                if phase=='stream':
                    streams=object.__new__(perf.Streams);streams.done=threading.Event();streams.lock=threading.Lock()
                    streams.connections=[];streams.data=data;streams.prefix='owned-prefix';streams.threads=[]
                    streams.start=Mock()
                def blocked_child(cmd,**kw):
                    commands.append(cmd)
                    child=self.real_popen([sys.executable,'-c','import time;time.sleep(30)'],**kw);children.append(child)
                    handler=signal.getsignal(signal.SIGTERM)
                    timer=threading.Timer(.04,lambda:handler(signal.SIGTERM,None));timer.start();timers.append(timer)
                    return child
                started=time.monotonic()
                try:
                    with patch.object(sys,'argv',['perf.py','self-check']),patch.object(perf,'prepare',return_value=data),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'k6 v1.5.0','')),patch.object(perf,'Streams',return_value=streams),patch.object(perf,'k6',return_value=(True,{'sse':stats})),patch.object(perf.subprocess,'Popen',side_effect=blocked_child),patch.object(perf,'CANCEL_BUDGET',.2):
                        outcome=perf.main()
                    elapsed=time.monotonic()-started
                    self.assertEqual(outcome,130);self.assertLess(elapsed,.6)
                    self.assertEqual(len(children),1);self.assertTrue(all(child.poll() is not None for child in children))
                    self.assertTrue(all(pipe.closed for child in children for pipe in (child.stdin,child.stdout,child.stderr)))
                    self.assertTrue(any(row[0]=='perf.'+('sse_cleanup' if phase=='stream' else 'trial_cleanup') and row[1]=='failed' for row in self.rows))
                    self.assertTrue(any(row[0]=='perf.interrupted' and row[1]=='interrupted' for row in self.rows))
                    self.assertIn('PGOPTIONS=-c lock_timeout=1000 -c statement_timeout=4000',commands[0])
                    print('offline_cleanup_sql_evidence='+json.dumps({'phase':phase,'elapsed_seconds':elapsed,'registered_signal':'SIGTERM','signal_delivery':'Timer invokes registered callback; not OS delivery','child_reaped':True,'pending_record':True,'server_completion':'unconfirmed'}))
                finally:
                    for timer in timers:timer.cancel();timer.join(1);self.assertFalse(timer.is_alive())
                    for child in children:
                        if child.poll() is None:child.kill()
                        child.wait(timeout=1)

    def test_cleanup_sql_cancel_keeps_original_failure_exits_even_if_reporting_fails(self):
        from unittest.mock import Mock
        for primary,expected in ((RuntimeError('original failure'),2),(KeyboardInterrupt(),130)):
            with self.subTest(primary=type(primary).__name__):
                perf._STOP_REQUESTED=False;perf._CANCEL_DEADLINE=None;perf.STOP.clear()
                data={**self.data,'counts':{'profile':'check'}};streams=Mock()
                def close():
                    signal.getsignal(signal.SIGTERM)(signal.SIGTERM,None)
                    raise RuntimeError('cleanup pending')
                streams.close.side_effect=close
                def report(*args,**kw):
                    if args[0]!='perf.seed':raise BrokenPipeError()
                with patch.object(sys,'argv',['perf.py','self-check']),patch.object(perf,'prepare',return_value=data),patch.object(perf.subprocess,'run',return_value=subprocess.CompletedProcess([],0,'k6 v1.5.0','')),patch.object(perf,'Streams',return_value=streams),patch.object(perf,'k6',side_effect=primary),patch.object(perf,'result',side_effect=report):
                    self.assertEqual(perf.main(),expected)
                streams.close.assert_called_once()

    def test_cleanup_sql_has_independent_timeout_and_preserves_stream_transaction(self):
        import load.seed as seed
        statements=[]
        with patch.object(seed,'sql',side_effect=lambda data,statement:statements.append(statement)):
            seed.stream_finish(self.data,"owned-prefix'escaped")
        with patch.object(perf,'cleanup_sql') as cleanup:
            perf.cleanup_stream_finish(self.data,"owned-prefix'escaped")
        self.assertEqual(cleanup.call_args.args,(self.data,statements[0]))
        children=[]
        def child(cmd,**kw):
            proc=self.real_popen([sys.executable,'-c','import time;time.sleep(30)'],**kw);children.append(proc);return proc
        started=time.monotonic()
        with patch.object(perf.subprocess,'Popen',side_effect=child),patch.object(perf,'CLEANUP_SQL_BUDGET',.08):
            with self.assertRaisesRegex(TimeoutError,'completion unconfirmed'):
                perf.cleanup_sql({**self.data,'postgres_container':'owned-stub-pg'},statements[0])
        self.assertLess(time.monotonic()-started,.5)
        self.assertTrue(all(proc.poll() is not None for proc in children))
        self.assertTrue(all(pipe.closed for proc in children for pipe in (proc.stdin,proc.stdout,proc.stderr)))

    def test_os_sigterm_at_cleanup_popen_return_boundary_reaps_acquired_child(self):
        children=[];previous_cleaning=perf._CLEANING;perf._CLEANING=False
        signal.signal(signal.SIGTERM,perf.cancel_signal)
        def child(cmd,**kw):
            proc=self.real_popen([sys.executable,'-c','import time;time.sleep(30)'],**kw);children.append(proc)
            # Actual OS delivery after acquisition, before Popen returns to caller.
            os.kill(os.getpid(),signal.SIGTERM)
            return proc
        caught=None;started=time.monotonic()
        try:
            with patch.object(perf.subprocess,'Popen',side_effect=child):
                try:perf.cleanup_sql({**self.data,'postgres_container':'owned-stub-pg'},'owned statement')
                except BaseException as error:caught=error
            elapsed=time.monotonic()-started
            self.assertEqual(len(children),1)
            self.assertIsInstance(caught,RuntimeError)
            self.assertIn('cleanup pending',str(caught))
            self.assertLess(elapsed,.5)
            self.assertTrue(all(proc.poll() is not None for proc in children))
            self.assertTrue(all(pipe.closed for proc in children for pipe in (proc.stdin,proc.stdout,proc.stderr)))
            self.assertFalse(perf._CLEANING)
            print('offline_cleanup_acquisition_evidence='+json.dumps({'elapsed_seconds':elapsed,'signal_delivery':'os.kill own Python SIGTERM','child_reaped':True,'pipes_closed':True,'cleaning_scope_restored':True}))
        finally:
            perf._CLEANING=previous_cleaning
            for proc in children:
                if proc.poll() is None:proc.kill()
                proc.wait(timeout=1)
                for pipe in (proc.stdin,proc.stdout,proc.stderr):
                    if pipe is not None:pipe.close()
    def test_stream_cleanup_uses_one_shared_deadline_and_skips_unbounded_db_finish(self):
        from unittest.mock import Mock
        perf.arm_cancel();perf._CANCEL_DEADLINE=time.monotonic()+.04
        streams=object.__new__(perf.Streams);streams.done=threading.Event();streams.lock=threading.Lock();streams.connections=[];streams.data=self.data;streams.prefix='owned-prefix';streams.threads=[]
        for i in range(60):
            worker=Mock();worker.is_alive.return_value=True;worker.join.side_effect=lambda timeout:time.sleep(timeout);streams.threads.append(worker)
        started=time.monotonic()
        with patch.object(perf,'stream_finish') as finish:
            with self.assertRaisesRegex(RuntimeError,'DB finish pending'):streams.close()
        self.assertLess(time.monotonic()-started,.15);finish.assert_not_called()
        self.assertLess(sum(worker.join.call_count for worker in streams.threads),3)
    def test_no_primary_reporting_failure_is_nonzero_and_retains_private_error(self):
        self.mode='normal';stage=self.work/'run/rps50';stage.mkdir(parents=True)
        (stage/'summary.json').write_text(json.dumps({'state':{'testRunDurationMs':10},'metrics':{'iterations':{'values':{'count':1}}}}))
        def broken(*args,**kw):
            if args[0].endswith('.cleanup'):raise BrokenPipeError('private secret')
        with patch.object(perf,'result',side_effect=broken):
            with self.assertRaises(BrokenPipeError):self.execute()
        self.assertTrue((stage/'cleanup-reporting-error.json').exists());self.assertFalse(perf._CLEANING)

if __name__=='__main__':unittest.main()
