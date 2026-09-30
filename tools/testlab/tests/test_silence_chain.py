import copy, http.client, json, os, sqlite3, ssl, subprocess, sys, tempfile, threading, unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import Mock, patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import silence_chain as silence
import identity


class SilenceProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name).resolve()
        owner='testlab-abcdef123456';self.work=self.root/'.local/testlab/environments'/owner;binary=self.work/'backend/aiops';binary.parent.mkdir(parents=True);binary.write_bytes(b'owned binary')
        self.backend={'owner':owner,'binary':str(binary),'source_tree_sha':'frozen','postgres_container':'aiops-'+owner+'-pg'}
        password_path=self.work/'perf/users-password.json';password_path.parent.mkdir();identity._private(password_path,{'password':'generated-private-password-'+'x'*24})
        self.component={'owner':owner,'work':str(self.work)}
        self.chain=silence.Chain(self.root,self.backend,self.component,'a'*32)
        self.operation='b'*32;self.uuid='12345678-1234-1234-1234-123456789abc'
        self.payload={'annotations':{'aiops_operation_id':self.operation},'matchers':[{'name':'application','value':'testlab','isRegex':False,'isEqual':True},{'name':'environment','value':'test','isRegex':False,'isEqual':True}],'comment':'Owned test','createdBy':'user','startsAt':'2026-09-30T12:00:00Z','endsAt':'2026-09-30T12:30:00Z'}
        self.observed=dict(copy.deepcopy(self.payload),id=self.uuid,status={'state':'active'})

    def test_exact_real_readback_accepts_am_start_shift_and_order_only(self):
        self.observed['matchers'].reverse();self.observed['startsAt']='2026-09-30T12:00:00.350Z'
        silence.exact_create(self.observed,self.payload,self.operation,self.uuid)

    def test_exact_readback_rejects_marker_scope_payload_and_time_drift(self):
        variants=[]
        for key,value in [('id','87654321-1234-1234-1234-123456789abc'),('annotations',{'aiops_operation_id':'c'*32}),('createdBy','other'),('comment','other'),('endsAt','2026-09-30T12:30:00.001Z'),('startsAt','2026-09-30T12:00:30.001Z'),('status',{'state':'expired'})]:
            item=copy.deepcopy(self.observed);item[key]=value;variants.append(item)
        item=copy.deepcopy(self.observed);item['matchers'][0]['value']='foreign-app';variants.append(item)
        for item in variants:
            with self.subTest(item=item),self.assertRaises(AssertionError):silence.exact_create(item,self.payload,self.operation,self.uuid)

    def test_component_label_and_live_loopback_are_checked_before_http(self):
        info={'State':{'Running':True},'Config':{'Labels':{'aiops.testlab.owner':self.backend['owner']}},'NetworkSettings':{'Ports':{'9093/tcp':[{'HostIp':'127.0.0.1','HostPort':'18571'}]}}}
        with patch.object(identity,'budgeted_inspect',return_value=info):silence.guard_component(self.backend,'alertmanager',18571)
        for changed in [dict(info,State={'Running':False}),dict(info,Config={'Labels':{'aiops.testlab.owner':'foreign'}}),dict(info,NetworkSettings={'Ports':{'9093/tcp':[{'HostIp':'0.0.0.0','HostPort':'18571'}]}})]:
            with patch.object(identity,'budgeted_inspect',return_value=changed),self.assertRaises(identity.Blocked):silence.guard_component(self.backend,'alertmanager',18571)

    def test_doctor_stops_before_component_or_ports_when_base_not_attested(self):
        with patch.object(silence,'attest',side_effect=identity.Blocked('source unknown')),patch.object(silence,'guard_component') as guard,patch.object(identity,'free_port') as ports,self.assertRaises(identity.Blocked):silence.doctor(self.root)
        guard.assert_not_called();ports.assert_not_called()

    def test_new_run_state_private_and_never_includes_login_secret(self):
        path=self.chain.work/'state.json';self.assertEqual(path.stat().st_mode&0o777,0o600);raw=path.read_text()
        self.assertNotIn(self.chain.password,raw);self.assertEqual(json.loads(raw)['bulk_history_acceptance'],False)

    def test_constructor_uses_actual_backend_schema_and_owned_private_password(self):
        backend=dict(self.backend)
        path=self.work/'perf/users-password.json'
        chain=silence.Chain(self.root,backend,self.component,'c'*32)
        self.assertEqual(chain.password,json.loads(path.read_text())['password'])
        self.assertNotIn(chain.password,(chain.work/'state.json').read_text())

    def test_constructor_failure_retains_private_stack_without_public_exception_value(self):
        with patch.object(sys,'argv',['silence_chain.py','chain','--root',str(self.root)]),patch.object(silence,'doctor',return_value=(self.backend,self.component)),patch.object(silence,'Chain',side_effect=KeyError('missing_fixture_field')),patch.object(identity,'emit') as emit:
            self.assertEqual(silence.main(),1)
        paths=list((self.work/'silence-chain').glob('*/diagnostic-error.json'))
        self.assertEqual(len(paths),1);self.assertEqual(paths[0].stat().st_mode&0o777,0o600)
        detail=json.loads(paths[0].read_text());self.assertEqual(detail['error_type'],'KeyError');self.assertTrue(detail['stack']);self.assertEqual(detail['stage'],'construct')
        self.assertNotIn('missing_fixture_field',str(emit.call_args))

    def test_generated_password_refuses_missing_public_symlink_and_foreign_owner(self):
        path=self.work/'perf/users-password.json'
        with self.assertRaises(identity.Blocked):silence.generated_password(self.root,dict(self.backend,owner='foreign-owner'))
        os.chmod(path,0o644)
        with self.assertRaises(identity.Blocked):silence.generated_password(self.root,self.backend)
        path.unlink()
        with self.assertRaises(identity.Blocked):silence.generated_password(self.root,self.backend)
        target=self.root/'unowned-password.json';identity._private(target,{'password':'synthetic-secret-'+'x'*24});path.symlink_to(target)
        with self.assertRaises(identity.Blocked):silence.generated_password(self.root,self.backend)

    def test_prepare_seed_obeys_real_role_name_unique_constraint(self):
        database=sqlite3.connect(':memory:');self.addCleanup(database.close)
        database.executescript('CREATE TABLE rbac_roles(id text PRIMARY KEY,name text NOT NULL UNIQUE); CREATE TABLE users(id text PRIMARY KEY,email text,name text,role text,password_hash text); CREATE TABLE rbac_user_roles(user_id text,role_id text); CREATE TABLE v3_role_grants(role_id text,module text,action text,scope_kind text,scope_id text,assurance text);')
        database.execute('INSERT INTO users(id,password_hash) VALUES(?,?)',('perf-'+self.backend['owner']+'-user-5','synthetic-hash'));database.commit()
        self.chain.backend.update(database_url='private-fixture',instance_config='private-config',admin={'email':'owned@testlab.invalid','password':'private-password'})
        def openssl(*args,**kwargs):
            for name in ('tls.crt','tls.key'):(self.chain.work/name).write_text('offline certificate fixture')
            return subprocess.CompletedProcess(args[0],0,'','')
        with patch.object(silence.subprocess,'run',side_effect=openssl),patch.object(silence.ssl,'create_default_context'),patch.object(self.chain,'sql',side_effect=database.executescript),patch.object(self.chain,'observe',return_value=3),patch.object(silence,'guard_component',side_effect=RuntimeError('stop before live component')),self.assertRaisesRegex(RuntimeError,'stop before live component'):
            self.chain.prepare()
        rows=database.execute('SELECT id,name FROM rbac_roles').fetchall();self.assertEqual(len(rows),2);self.assertEqual(len({name for _,name in rows}),2)

    def test_sql_failure_keeps_bounded_private_stderr_without_public_values(self):
        info={'State':{'Running':True},'Config':{'Labels':{'aiops.testlab.owner':self.backend['owner']}}}
        result=subprocess.CompletedProcess([],3,'','ERROR: duplicate fixture role; private-canary '+('x'*(70<<10)))
        with patch.object(identity,'budgeted_inspect',return_value=info),patch.object(silence.subprocess,'run',return_value=result) as run,self.assertRaises(AssertionError) as error:
            self.chain.sql('private statement with private-canary')
        self.assertNotIn('private-canary',str(error.exception))
        path=self.chain.work/'sql-failure-1.json';self.assertEqual(path.stat().st_mode&0o777,0o600);data=json.loads(path.read_text())
        self.assertTrue(data['stderr_truncated']);self.assertLessEqual(len(data['stderr']),64<<10);self.assertEqual(data['exit_code'],3)
        self.assertNotIn('private statement',path.read_text());self.assertEqual(run.call_args.kwargs['input'],'private statement with private-canary')

    def test_sql_owner_refusal_precedes_any_command(self):
        info={'State':{'Running':True},'Config':{'Labels':{'aiops.testlab.owner':'foreign-owner'}}}
        with patch.object(identity,'budgeted_inspect',return_value=info),patch.object(silence.subprocess,'run') as run,self.assertRaises(ValueError):self.chain.sql('BEGIN READ ONLY; SELECT 1; COMMIT;')
        run.assert_not_called()

    def test_source_or_binary_change_blocks_before_process_launch(self):
        with patch.object(identity,'source_fingerprint',return_value='frozen'):self.chain.frozen()
        with patch.object(identity,'source_fingerprint',return_value='changed'),self.assertRaises(identity.Blocked):self.chain.frozen()

    def test_fixed_transport_refuses_other_ports_and_network_path(self):
        for port,path in [(18561,'/api/v1/silences'),(443,'/'),(18571,'//remote')]:
            with self.assertRaises(identity.Blocked):silence.request_local(port,'GET',path)

    def test_cleanup_never_targets_existing_users_or_roles(self):
        self.chain.seeded=True;self.chain.proc=None
        with patch.object(self.chain,'sql',return_value='') as sql,patch.object(identity,'emit'):
            self.chain.cleanup()
        query=sql.call_args.args[0]
        for value in self.chain.uids+self.chain.roles:self.assertIn(value,query)
        self.assertNotIn('perf-'+self.backend['owner']+'-user-',query);self.assertNotIn('DELETE FROM management_audit',query)

    def test_blocked_main_returns_environment_exit_code(self):
        with patch.object(sys,'argv',['silence_chain.py','chain','--root',str(self.root)]),patch.object(silence,'doctor',side_effect=identity.Blocked('no real alert')),patch.object(identity,'emit'):
            self.assertEqual(silence.main(),2)

    def test_first_vote_rejects_any_early_dispatch_authority(self):
        self.chain.approvers=[(self.chain.uids[1],'synthetic-cookie',Mock(spec=['assertion','save']))]
        options={'challenge_id':'own-challenge','options':{'challenge':'own-challenge'}}
        vote={'status':'pending_review','received_approvals':1}
        with patch.object(self.chain,'call',side_effect=[(options,{}),(vote,{})]),patch.object(self.chain,'observe',return_value={'decisions':0,'claims':1}),patch.object(identity,'emit'),self.assertRaisesRegex(AssertionError,'first vote'):
            self.chain.approve('a'*32)

    def test_manual_review_is_failure_and_keeps_exact_private_evidence(self):
        pid='b'*32;claim={'state':'manual_review','operation':'own-op','payload':self.payload}
        with patch.object(self.chain,'claim',return_value=claim),self.assertRaisesRegex(AssertionError,'manual_review'):
            self.chain.confirmed(pid)
        path=self.chain.work/('claim-red-'+pid+'.json');self.assertEqual(path.stat().st_mode&0o777,0o600);self.assertEqual(json.loads(path.read_text()),claim)

    def test_teardown_cannot_expire_unrelated_marker_even_when_app_matches(self):
        self.chain.am=Mock();self.chain.am.payloads={};self.chain.proc=None
        unrelated=dict(self.observed,annotations={'aiops_operation_id':'unrelated'})
        with patch.object(silence,'guard_component'),patch.object(silence,'request_local',return_value=(200,[unrelated],{})) as request,patch.object(identity,'emit'):
            self.chain.cleanup()
        self.assertEqual(request.call_count,1);self.assertEqual(request.call_args.args[1],'GET')

    def test_process_cleanup_error_still_closes_entry_and_retires_only_owned_ids(self):
        self.chain.seeded=True;self.chain.entry=Mock()
        with patch.object(identity,'cleanup_process',side_effect=RuntimeError('controlled failure')),patch.object(self.chain,'sql',return_value='') as sql,self.assertRaises(AssertionError):
            self.chain.cleanup()
        self.chain.entry.close.assert_called_once();sql.assert_called_once()
        for uid in self.chain.uids:self.assertIn(uid,sql.call_args.args[0])

    def test_http_matrix_emits_complete_outcome_and_positive_registration(self):
        self.chain.context=Mock()
        for path,expected_path,status in [('/api/v1/canvases/INV-'+'a'*32,'/api/v1/canvases/{id}',200),('/api/v1/silences/'+self.uuid,'/api/v1/silences/{id}',202),('/api/v1/proposals/'+'b'*32+'/approve?private=query','/api/v1/proposals/{id}/approve',201)]:
            value={'id':'owned'} if status!=202 else {'status':'pending_review','proposal_id':'b'*32}
            with patch.object(silence,'request_local',return_value=(status,value,{})),patch.object(identity,'emit') as emit:
                self.chain.call('POST',path,want=status)
            row=emit.call_args.kwargs['interface']
            self.assertEqual(row['path'],expected_path);self.assertEqual(row['status'],'passed');self.assertEqual(row['actual'],status);self.assertEqual(row['expected'],[status]);self.assertTrue(row['registration_evidence'])
            self.assertEqual(row['scenario'],'normal');self.assertEqual(row['method'],'POST')

    def test_expected_http_rejections_have_scenario_but_never_register_route(self):
        self.chain.context=Mock()
        for status,scenario in [(401,'authorization'),(403,'authorization'),(404,'boundary'),(422,'boundary'),(409,'idempotency'),(503,'failure')]:
            with patch.object(silence,'request_local',return_value=(status,{'error':{'code':'owned-negative'}},{})),patch.object(identity,'emit') as emit:
                self.chain.call('GET','/api/v1/silences',want=status)
            row=emit.call_args.kwargs['interface']
            self.assertEqual(row['status'],'passed');self.assertEqual(row['scenario'],scenario);self.assertFalse(row['registration_evidence'])

    def test_failed_positive_http_attempt_is_emitted_without_registration(self):
        self.chain.context=Mock()
        with patch.object(silence,'request_local',return_value=(403,{'error':{'code':'forbidden'}},{})),patch.object(identity,'emit') as emit,self.assertRaises(AssertionError):
            self.chain.call('GET','/api/v1/silences',want=200)
        row=emit.call_args.kwargs['interface'];self.assertEqual(row['status'],'failed');self.assertEqual(row['expected'],[200]);self.assertFalse(row['registration_evidence'])


class SilenceRelayProtocolTests(unittest.TestCase):
    """Protocol Mock on ephemeral local sockets; no product/AM evidence."""
    @classmethod
    def setUpClass(cls):
        cls.tmp=tempfile.TemporaryDirectory();cls.work=Path(cls.tmp.name);cls.cert=cls.work/'cert.pem';cls.key=cls.work/'key.pem'
        result=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(cls.key),'-out',str(cls.cert),'-days','1','-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,IP:127.0.0.1'],capture_output=True,timeout=15)
        if result.returncode:raise RuntimeError('unit TLS fixture generation failed')
        for path in (cls.cert,cls.key):os.chmod(path,0o600)
    @classmethod
    def tearDownClass(cls):cls.tmp.cleanup()
    def setUp(self):
        self.calls=[];calls=self.calls
        class Upstream(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def relay(self):
                calls.append(self.command);body=self.rfile.read(int(self.headers.get('Content-Length','0')))
                raw=json.dumps({'silenceID':'12345678-1234-1234-1234-123456789abc'}).encode() if self.command=='POST' else b'{}'
                self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
            do_POST=do_DELETE=relay
        self.upstream=ThreadingHTTPServer(('127.0.0.1',0),Upstream);threading.Thread(target=self.upstream.serve_forever,daemon=True).start()
        self.addCleanup(self.upstream.server_close);self.addCleanup(self.upstream.shutdown)
        self.guard=patch.object(silence,'guard_component');self.guard.start();self.addCleanup(self.guard.stop)
        self.relay=silence.Relay(0,str(self.cert),str(self.key),self.upstream.server_port,'synthetic-token',{'owner':'testlab-unit'})
        self.relay.run_id='a'*32;self.relay.labels={'application':'testlab','environment':'test','alertname':'OwnedAlert','signal_id':'owned-signal'};self.relay.start();self.addCleanup(self.relay.close)
        self.context=ssl.create_default_context(cafile=str(self.cert))
        self.payload={'annotations':{'aiops_operation_id':'b'*32},'comment':'Owned silence '+'a'*32+' unit','createdBy':'owned-user','matchers':[{'name':k,'value':v,'isRegex':False,'isEqual':True} for k,v in self.relay.labels.items()]}
    def request(self,method,path,body=None,token='synthetic-token'):
        conn=http.client.HTTPSConnection('localhost',self.relay.server.server_port,context=self.context,timeout=3)
        try:
            conn.request(method,path,None if body is None else json.dumps(body).encode(),{'Authorization':'Bearer '+token,'Content-Type':'application/json'})
            response=conn.getresponse();response.read();return response.status
        finally:conn.close()
    def test_exact_post_success_then_loss_retains_one_authoritative_write_receipt(self):
        self.relay.fault('POST',self.payload['comment'],'drop')
        with self.assertRaises(http.client.RemoteDisconnected):self.request('POST','/api/v2/silences',self.payload)
        self.assertEqual(self.calls,['POST']);self.assertTrue(self.relay.accepted.is_set())
        self.assertEqual(self.relay.count('POST',operation='b'*32),1);self.assertEqual(self.relay.records[0]['status'],200)
    def test_auth_scope_and_foreign_delete_never_reach_upstream(self):
        self.assertEqual(self.request('POST','/api/v2/silences',self.payload,token='wrong-token'),403)
        bad=copy.deepcopy(self.payload);bad['matchers'][0]['value']='foreign'
        self.assertEqual(self.request('POST','/api/v2/silences',bad),403)
        self.assertEqual(self.request('DELETE','/api/v2/silence/87654321-1234-1234-1234-123456789abc'),403)
        self.assertEqual(self.calls,[])
    def test_exact_delete_success_then_loss_has_single_expiration_dispatch(self):
        self.assertEqual(self.request('POST','/api/v2/silences',self.payload),200)
        uid='12345678-1234-1234-1234-123456789abc';self.relay.fault('DELETE',uid,'drop')
        with self.assertRaises(http.client.RemoteDisconnected):self.request('DELETE','/api/v2/silence/'+uid)
        self.assertEqual(self.calls,['POST','DELETE']);self.assertEqual(self.relay.count('DELETE',uid),1)
    def test_crash_gate_holds_only_after_upstream_success_until_owned_release(self):
        self.relay.fault('POST',self.payload['comment'],'crash');errors=[]
        def invoke():
            try:self.request('POST','/api/v2/silences',self.payload)
            except Exception as error:errors.append(error)
        request_thread=threading.Thread(target=invoke,daemon=True);request_thread.start()
        self.assertTrue(self.relay.accepted.wait(2));self.assertTrue(request_thread.is_alive());self.assertEqual(self.calls,['POST'])
        self.relay.release.set();request_thread.join(timeout=2)
        self.assertFalse(request_thread.is_alive());self.assertEqual(len(errors),1);self.assertIsInstance(errors[0],http.client.RemoteDisconnected)
        self.assertEqual(self.relay.count('POST',operation='b'*32),1)

    def test_cancellation_before_proxy_thread_start_closes_without_shutdown_deadlock(self):
        relay=silence.Relay(0,str(self.cert),str(self.key),self.upstream.server_port,'synthetic-token',{'owner':'testlab-unit'})
        relay.close()
        self.assertEqual(relay.server.socket.fileno(),-1);self.assertFalse(relay.thread.is_alive())


if __name__=='__main__':unittest.main()
