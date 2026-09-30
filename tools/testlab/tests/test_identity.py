import json
import http.cookiejar
import os
from pathlib import Path
import subprocess
import signal
import sys
import tempfile
import unittest
import urllib.request
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import identity
from env import _private


class IdentityProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        owner = 'testlab-a123456789ab'
        binary = self.root / '.local/testlab/environments' / owner / 'backend/aiops'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'owned formal binary')
        self.backend = dict(owner=owner, binary=str(binary), source_tree_sha='expected', environment='test',
                            postgres_container='aiops-' + owner + '-pg')
        self.chain = identity.Chain(self.root, self.backend, 'abc123')

    def test_container_cleanup_requires_exact_owner_run_name_image(self):
        name = self.chain.container
        info = dict(Name='/' + name, Config=dict(Image=identity.IMAGE, Labels={identity.OWNER_LABEL:self.backend['owner'],identity.RUN_LABEL:'abc123'}))
        self.assertEqual(identity.guard_run_container(info,self.backend['owner'],'abc123',name),info)
        for changed in [None,dict(info,Name='/foreign'),dict(info,Config=dict(Image='foreign',Labels=info['Config']['Labels'])),
                        dict(info,Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:'other',identity.RUN_LABEL:'abc123'})),
                        dict(info,Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:self.backend['owner'],identity.RUN_LABEL:'older-run'}))]:
            with self.subTest(info=changed),self.assertRaises(identity.Blocked):
                identity.guard_run_container(changed,self.backend['owner'],'abc123',name)

    def test_component_exact_owner_and_expected_existing_keycloak(self):
        component=dict(owner=self.backend['owner'],work=str(self.root/'.local/testlab/environments'/self.backend['owner']),
                       services=[dict(name='keycloak',container='aiops-'+self.backend['owner']+'-keycloak',url='http://127.0.0.1:18576')])
        identity.validate_component(self.root,self.backend,component)
        for changed in [dict(component,owner='other'),dict(component,work=str(self.root)),dict(component,services=[]),
                        dict(component,services=component['services']*2),dict(component,services=[dict(component['services'][0],url='http://example.com')])]:
            with self.assertRaises(identity.Blocked):identity.validate_component(self.root,self.backend,changed)

    def test_live_port_must_belong_to_exact_labelled_image_on_loopback(self):
        info=dict(Config=dict(Image=identity.IMAGE),NetworkSettings=dict(Ports={'8080/tcp':[{'HostIp':'127.0.0.1','HostPort':'18590'}]}))
        identity.guard_component_port(info,18590)
        for host,port in [('0.0.0.0','18590'),('127.0.0.1','18576')]:
            bad=dict(info,NetworkSettings=dict(Ports={'8080/tcp':[{'HostIp':host,'HostPort':port}]}))
            with self.assertRaises(identity.Blocked):identity.guard_component_port(bad,18590)
        with self.assertRaises(identity.Blocked):identity.guard_component_port(dict(info,Config=dict(Image='unknown')),18590)

    def test_request_cannot_follow_remote_redirect_or_business_host(self):
        for url in ['https://example.com/callback','http://127.0.0.1:18561/api/v1/auth/me',identity.ORIGIN+'#bad',
                    'http://secret@127.0.0.1:18586/api/v1/auth/me']:
            with self.assertRaises(identity.Blocked):identity.validate_url(url)
        self.assertIsNone(identity.NoRedirect().redirect_request(None,None,302,'',None,'https://example.com'))

    def test_source_and_binary_freeze_block_unknown_or_changed_before_launch(self):
        with patch.object(identity,'source_fingerprint',return_value='expected'):self.chain.frozen()
        for value in ['unknown','changed']:
            with patch.object(identity,'source_fingerprint',return_value=value),self.assertRaises(identity.Blocked):self.chain.frozen()
        Path(self.backend['binary']).write_bytes(b'replaced')
        with patch.object(identity,'source_fingerprint',return_value='expected'),self.assertRaises(identity.Blocked):self.chain.frozen()

    def test_fresh_chain_state_private_before_any_resource_creation(self):
        state=self.chain.work/'state.json'
        self.assertEqual(state.stat().st_mode&0o777,0o600)
        data=json.loads(state.read_text())
        self.assertIsNone(data['pid'])
        self.assertEqual(data['owner'],self.backend['owner'])
        self.assertNotIn('password',data)
        self.assertNotIn(self.chain.password,state.read_text())

    def test_bad_http_retains_first_red_without_secrets_or_queries(self):
        raw=json.dumps({'error':{'code':'identity_rejected','message':'secret token hidden'}}).encode()
        with patch.object(identity,'transport',return_value=(401,raw,{})),patch.object(identity,'emit'):
            for want in [302,200]:
                with self.assertRaises(AssertionError):
                    self.chain.call(Mock(),'GET',identity.CALLBACK+'?code=private-code&state=private-state',want=want)
        text=(self.chain.work/'first-failure.json').read_text()
        row=json.loads(text)
        self.assertEqual(row['expected']['status'],[302])
        self.assertEqual(row['path'],identity.CALLBACK)
        for value in ['private-code','private-state','secret token hidden']:
            self.assertNotIn(value,text)
        self.assertEqual(len((self.chain.work/'interfaces.jsonl').read_text().splitlines()),2)

    def test_redirect_html_is_normal_but_200_non_json_is_tool_failure(self):
        with patch.object(identity,'transport',return_value=(302,b'<a href="/">Found</a>',{'Location':'/'})),patch.object(identity,'emit'):
            self.chain.call(Mock(),'GET',identity.CALLBACK,want=302)
        with patch.object(identity,'transport',return_value=(200,b'not json',{})),patch.object(identity,'emit'),self.assertRaises(AssertionError):
            self.chain.call(Mock(),'GET','/api/v1/auth/me')

    def test_direct_child_cleanup_term_then_bounded_kill_never_uses_saved_pid(self):
        proc=Mock();proc.poll.return_value=None;proc.returncode=-9
        proc.wait.side_effect=[subprocess.TimeoutExpired('owned',20),-9]
        receipt=identity.cleanup_process(proc)
        proc.terminate.assert_called_once();proc.kill.assert_called_once()
        self.assertEqual(proc.wait.call_args_list[0].kwargs['timeout'],20)
        self.assertEqual(proc.wait.call_args_list[1].kwargs['timeout'],5)
        self.assertTrue(receipt['forced_kill'])
        dead=Mock();dead.poll.return_value=0
        identity.cleanup_process(dead);dead.terminate.assert_not_called();dead.kill.assert_not_called()
        self.chain.state['pid']=123456
        with patch.object(identity,'cleanup_process',return_value={'already_exited':True,'forced_kill':False}),patch.object(identity,'budgeted_inspect',return_value=None),patch.object(identity,'emit'):
            self.chain.cleanup()

    def test_owner_mismatch_refuses_container_delete(self):
        foreign=dict(Name='/'+self.chain.container,Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:'foreign',identity.RUN_LABEL:'abc123'}))
        with patch.object(identity,'budgeted_inspect',return_value=foreign),patch.object(identity,'docker') as delete,self.assertRaises(identity.Blocked):
            self.chain.cleanup()
        delete.assert_not_called()

    def test_own_lock_rejects_overlap_and_symlink(self):
        with identity.exclusive(self.root):
            with self.assertRaises(identity.Blocked),identity.exclusive(self.root):pass
        lock=self.root/'.local/testlab/identity.lock';lock.unlink();target=self.root/'secret';target.write_text('private')
        lock.symlink_to(target)
        with self.assertRaises(OSError),identity.exclusive(self.root):pass
        self.assertEqual(target.read_text(),'private')

    def test_doctor_port_checks_only_after_attestation(self):
        with patch.object(identity,'attest',side_effect=identity.Blocked('source mismatch')),patch.object(identity,'docker') as docker,patch.object(identity,'free_port') as ports,self.assertRaises(identity.Blocked):
            identity.doctor(self.root)
        docker.assert_not_called();ports.assert_not_called()

    def test_sigterm_raises_into_finally_and_restores_previous_handler(self):
        cleaned=[]
        def previous(signum,frame):pass
        with patch.object(identity.signal,'signal',return_value=previous) as register:
            with self.assertRaises(identity.Blocked),identity.cooperative_signals():
                handler=register.call_args.args[1]
                try:handler(identity.signal.SIGTERM,None)
                finally:cleaned.append(True)
        self.assertEqual(cleaned,[True])
        self.assertEqual(register.call_args.args,(identity.signal.SIGTERM,previous))

    def test_cleanup_failure_never_overwrites_business_failure_or_first_red(self):
        primary=AssertionError('business state mismatch');cleanup=identity.Blocked('owned cleanup unavailable')
        with patch.object(self.chain,'run',side_effect=primary),patch.object(self.chain,'cleanup',side_effect=cleanup),patch.object(identity,'emit'):
            with self.assertRaises(AssertionError) as result:identity.execute(self.chain)
        self.assertIs(result.exception,primary)
        self.assertEqual(json.loads((self.chain.work/'first-failure.json').read_text())['stage'],'business_contract')
        self.assertEqual(json.loads((self.chain.work/'cleanup-error.json').read_text())['error'],'owned cleanup unavailable')
        with patch.object(self.chain,'run',side_effect=AssertionError('later different red')),patch.object(self.chain,'cleanup'),patch.object(identity,'emit'),self.assertRaises(AssertionError):identity.execute(self.chain)
        self.assertEqual(json.loads((self.chain.work/'first-failure.json').read_text())['error'],'business state mismatch')

    def test_successful_business_with_failed_cleanup_is_not_green(self):
        with patch.object(self.chain,'run',return_value={'positive':True}),patch.object(self.chain,'cleanup',side_effect=AssertionError('cleanup failed')),patch.object(identity,'emit'),self.assertRaises(AssertionError):identity.execute(self.chain)

    def test_primary_evidence_io_failure_still_cleans_and_keeps_original_business_error(self):
        original=AssertionError('original business failure')
        with patch.object(self.chain,'run',side_effect=original),patch.object(self.chain,'cleanup') as cleanup, \
             patch.object(identity,'_private',side_effect=OSError('private storage unavailable')), \
             patch.object(identity,'preserve_first_failure') as first,patch.object(identity,'emit') as output:
            with self.assertRaises(AssertionError) as result:identity.execute(self.chain)
        cleanup.assert_called_once();self.assertIs(result.exception,original)
        first.assert_called_once()
        output.assert_called_once()
        self.assertEqual(output.call_args.args[0],self.chain.module+'.evidence_failure')
        self.assertEqual(output.call_args.args[2]['failures'],[{'operation':'primary-error.json','error_type':'OSError'}])
        self.assertNotIn('private storage unavailable',' '.join(result.exception.__notes__))

    def test_first_red_preservation_io_failure_still_cleans_and_keeps_original_business_error(self):
        original=AssertionError('original first red')
        with patch.object(self.chain,'run',side_effect=original),patch.object(self.chain,'cleanup') as cleanup, \
             patch.object(identity,'preserve_first_failure',side_effect=OSError('exclusive creation failed')),patch.object(identity,'emit'):
            with self.assertRaises(AssertionError) as result:identity.execute(self.chain)
        cleanup.assert_called_once();self.assertIs(result.exception,original)
        self.assertEqual(json.loads((self.chain.work/'primary-error.json').read_text())['error'],'original first red')

    def test_cleanup_error_reporting_failure_preserves_original_error_and_attempts_every_report(self):
        for business_failed in [False,True]:
            for failing in ['private','first','emit']:
                with self.subTest(business_failed=business_failed,failing=failing):
                    primary=AssertionError('original business red');failure=identity.Blocked('original cleanup red')
                    original=primary if business_failed else failure
                    storage=Mock(side_effect=OSError('storage unavailable')) if failing=='private' else Mock()
                    first=Mock(side_effect=OSError('first red unavailable')) if failing=='first' else Mock()
                    output=Mock(side_effect=OSError('output pipe closed')) if failing=='emit' else Mock()
                    with patch.object(self.chain,'run',side_effect=primary if business_failed else None,return_value={'ok':True}), \
                         patch.object(self.chain,'cleanup',side_effect=failure) as cleanup,patch.object(identity,'_private',storage), \
                         patch.object(identity,'preserve_first_failure',first),patch.object(identity,'emit',output):
                        with self.assertRaises(type(original)) as result:identity.execute(self.chain)
                    self.assertIs(result.exception,original);cleanup.assert_called_once()
                    self.assertTrue(any(call.args[0].name=='cleanup-error.json' for call in storage.call_args_list))
                    self.assertTrue(any(call.args[1]['stage']=='cleanup' for call in first.call_args_list))
                    self.assertTrue(any(call.args[0].endswith('.cleanup_failure') for call in output.call_args_list))

    def test_cleanup_interrupted_reporting_io_failure_cannot_turn_cancellation_into_success(self):
        def cleanup():os.kill(os.getpid(),signal.SIGTERM)
        with identity.cooperative_signals(),patch.object(self.chain,'run',return_value={'ok':True}), \
             patch.object(self.chain,'cleanup',side_effect=cleanup) as cleaned, \
             patch.object(identity,'_private',side_effect=OSError('storage failed')), \
             patch.object(identity,'preserve_first_failure',side_effect=OSError('first failed')), \
             patch.object(identity,'emit',side_effect=OSError('stdout failed')):
            with self.assertRaisesRegex(identity.Blocked,'interrupted during cleanup') as result:identity.execute(self.chain)
        cleaned.assert_called_once()
        notes=' '.join(result.exception.__notes__)
        for operation in ['cleanup-interrupted.json','cleanup_interrupted.first_failure','cleanup_interrupted.emit','evidence_failure.emit']:
            self.assertIn(operation,notes)
        for secret in ['storage failed','first failed','stdout failed']:
            self.assertNotIn(secret,notes)

    def test_execute_success_preserves_result_and_emits_no_failure_diagnostics(self):
        actual={'contract':'unchanged'}
        with patch.object(self.chain,'run',return_value=actual),patch.object(self.chain,'cleanup') as cleanup, \
             patch.object(identity,'_private') as storage,patch.object(identity,'preserve_first_failure') as first, \
             patch.object(identity,'emit') as output:
            self.assertIs(identity.execute(self.chain),actual)
        cleanup.assert_called_once();storage.assert_not_called();first.assert_not_called();output.assert_not_called()

    def test_keycloak_transport_failure_records_stage_and_oom_before_cleanup(self):
        info=dict(Name='/'+self.chain.container,Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:self.backend['owner'],identity.RUN_LABEL:'abc123'}),
                  State=dict(Running=False,OOMKilled=True,ExitCode=137,FinishedAt='known-test-time'))
        with patch.object(identity,'transport',side_effect=ConnectionResetError()),patch.object(identity,'_inspect',return_value=info),patch.object(identity,'emit'),self.assertRaises(identity.Blocked):
            self.chain.keycloak_call(Mock(),'GET',identity.KEYCLOAK+'/realms/private/auth?code=hidden&state=hidden')
        raw=(self.chain.work/'keycloak-transport-failure.json').read_text();data=json.loads(raw)
        self.assertEqual(data['path'],'/realms/private/auth');self.assertTrue(data['container_state']['OOMKilled'])
        self.assertNotIn('code=',raw);self.assertNotIn('state=',raw)

    def test_cleanup_deadline_exhaustion_prevents_another_resource_operation(self):
        with patch.object(identity.time,'monotonic',return_value=100),patch.object(identity.subprocess,'run') as docker,self.assertRaises(identity.Blocked):
            identity.budgeted_inspect('exact-owned',deadline=99)
        docker.assert_not_called()

    def test_headroom_blocks_without_one_gib_and_reserve(self):
        with patch.object(identity, 'docker', side_effect=['2147483648\n', '1GiB / 2GiB\n']), self.assertRaises(identity.Blocked):
            identity.resource_headroom()

    def test_headroom_records_exact_image_and_limits(self):
        with patch.object(identity, 'docker', side_effect=['8589934592\n', '512MiB / 1GiB\n1GiB / 2GiB\n', 'sha256:owned\n']):
            result = identity.resource_headroom()
        self.assertEqual(result['container_usage_bytes'], 1610612736)
        self.assertEqual(result['memory_limit_bytes'], 1073741824)
        self.assertEqual(result['reserve_bytes'], 268435456)
        self.assertEqual(result['cpu_limit'], 1)
        self.assertEqual(result['image_id'], 'sha256:owned')

    def test_blocked_main_exit_code_distinguishes_environment(self):
        with patch.object(sys, 'argv', ['identity.py', 'chain', '--root', str(self.root)]), patch.object(identity, 'doctor', side_effect=identity.Blocked('resource gate')), patch.object(identity, 'emit'):
            self.assertEqual(identity.main(), 2)

    def test_owned_keycloak_origin_is_https_and_plain_old_origin_rejected(self):
        self.assertEqual(identity.KEYCLOAK, 'https://localhost:18590')
        with self.assertRaises(identity.Blocked):identity.validate_url('http://127.0.0.1:18590/realms/owned')

    def test_secure_cookie_needs_https_transport_without_policy_override(self):
        jar=http.cookiejar.CookieJar()
        jar.set_cookie(http.cookiejar.Cookie(0,'AUTH_SESSION_ID','synthetic-value',None,False,'localhost.local',False,False,'/realms/owned/',True,True,None,True,None,None,{},False))
        plain=urllib.request.Request('http://localhost:18590/realms/owned/login-actions/authenticate');jar.add_cookie_header(plain)
        secure=urllib.request.Request('https://localhost:18590/realms/owned/login-actions/authenticate');jar.add_cookie_header(secure)
        self.assertFalse(plain.has_header('Cookie'));self.assertTrue(secure.has_header('Cookie'))

    def test_browser_trusts_only_supplied_owned_ca_with_normal_cookie_policy(self):
        with patch.object(identity.ssl,'create_default_context',return_value=Mock()) as context:
            opener,jar=identity.browser(cafile='owned-ca.pem')
        context.assert_called_once_with(cafile='owned-ca.pem')
        self.assertIs(type(jar),http.cookiejar.CookieJar)
        self.assertTrue(any(isinstance(h,urllib.request.HTTPSHandler) for h in opener.handlers))

    def test_auth_phase_evidence_excludes_query_codes_and_cookie_values(self):
        headers={'Location':identity.ORIGIN+identity.CALLBACK+'?code=hidden-code&state=hidden-state'}
        jar=http.cookiejar.CookieJar();jar.set_cookie(http.cookiejar.Cookie(0,'AUTH_SESSION_ID','hidden-cookie',None,False,'localhost.local',False,False,'/realms/owned/',True,True,None,True,None,None,{},False))
        self.chain.record_auth_phase('credentials_submitted',302,headers,jar)
        raw=(self.chain.work/'keycloak-auth-phases.json').read_text();data=json.loads(raw)[0]
        self.assertEqual(data['location_path'],identity.CALLBACK);self.assertTrue(data['cookies'][0]['secure'])
        for secret in ('hidden-code','hidden-state','hidden-cookie','?code='):self.assertNotIn(secret,raw)

    def test_temporary_keycloak_requires_exact_https_container_port(self):
        info=dict(Config=dict(Image=identity.IMAGE),NetworkSettings=dict(Ports={'8443/tcp':[{'HostIp':'127.0.0.1','HostPort':'18590'}]}))
        identity.guard_component_port(info,18590,internal_port=8443)
        with self.assertRaises(identity.Blocked):identity.guard_component_port(info,18590)

    def test_actual_start_configuration_has_verified_https_at_both_ends(self):
        self.backend.update(database_url='postgres://owned-fixture',instance_config='owned-instance.json',admin={'email':'owned@example.invalid','password':'synthetic'})
        self.chain.ca_file.write_bytes(b'owned CA fixture')
        info=dict(Name='/'+self.chain.container,Image='sha256:owned',Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:self.backend['owner'],identity.RUN_LABEL:'abc123'}),
                  NetworkSettings={'Ports':{'8443/tcp':[{'HostIp':'127.0.0.1','HostPort':'18590'}]}},HostConfig={'Memory':1073741824,'NanoCpus':1000000000})
        process=Mock();process.poll.return_value=None;process.pid=4242
        with patch.object(identity,'source_fingerprint',return_value='expected'),patch.object(identity,'_inspect',side_effect=[None,info]),patch.object(identity,'resource_headroom',return_value={'image_id':'sha256:owned'}),patch.object(identity,'docker') as docker,patch.object(identity,'browser',return_value=(Mock(),Mock())),patch.object(identity,'transport',side_effect=[(200,json.dumps({'issuer':self.chain.issuer}).encode(),{}),(200,b'{}',{})]),patch.object(identity.subprocess,'Popen',return_value=process) as popen,patch.object(identity,'TLSProxy') as proxy,patch.object(identity,'emit'):
            self.chain.start()
        variables=popen.call_args.kwargs['env']
        self.assertEqual(variables['AIOPS_PUBLIC_ORIGIN'],'https://localhost:18586')
        self.assertEqual(variables['AIOPS_V3_OIDC_ISSUER'],'https://localhost:18590/realms/'+self.chain.realm)
        self.assertEqual(variables['AIOPS_V3_OIDC_REDIRECT_URL'],'https://localhost:18586/api/v1/auth/oidc/callback')
        self.assertEqual(variables['AIOPS_V3_OIDC_ALLOW_LOOPBACK_HTTP'],'false')
        self.assertEqual(variables['AIOPS_SECURE_COOKIES'],'true');self.assertEqual(variables['SSL_CERT_FILE'],str(self.chain.ca_file))
        self.assertEqual(variables['AIOPS_ADDR'],'127.0.0.1:18587');proxy.assert_called_once_with(self.chain)
        argv=docker.call_args.args[0];self.assertIn('127.0.0.1:18590:8443',argv)

    def test_state_cookie_exact_host_only_localhost_normalization(self):
        jar=http.cookiejar.CookieJar();cookie=http.cookiejar.Cookie(0,'aiops_oidc_state','synthetic-state',None,False,'localhost.local',False,False,identity.CALLBACK,True,True,None,True,None,None,{},False);jar.set_cookie(cookie)
        self.assertEqual(identity.cookie_value(jar,'aiops_oidc_state'),'synthetic-state')

    def test_state_cookie_still_rejects_invalid_scope_secure_and_duplicate(self):
        def cookie(domain='localhost.local',path=identity.CALLBACK,secure=True,specified=False):
            return http.cookiejar.Cookie(0,'aiops_oidc_state','synthetic-state',None,False,domain,specified,False,path,True,secure,None,True,None,None,{},False)
        for value in [cookie(domain='127.0.0.1'),cookie(path='/'),cookie(secure=False),cookie(specified=True)]:
            jar=http.cookiejar.CookieJar();jar.set_cookie(value)
            with self.assertRaises(AssertionError):identity.cookie_value(jar,'aiops_oidc_state')
        jar=http.cookiejar.CookieJar();jar.set_cookie(cookie());jar.set_cookie(cookie(path='/'))
        with self.assertRaises(AssertionError):identity.cookie_value(jar,'aiops_oidc_state')

    def test_first_term_during_normal_cleanup_finishes_steps_but_cannot_pass(self):
        steps=[]
        def cleanup():
            steps.append('before');os.kill(os.getpid(),signal.SIGTERM);steps.append('after')
        with identity.cooperative_signals(),patch.object(self.chain,'run',return_value={'business':'passed'}),patch.object(self.chain,'cleanup',side_effect=cleanup),patch.object(identity,'emit'),self.assertRaises(identity.Blocked):
            identity.execute(self.chain)
        self.assertEqual(steps,['before','after'])
        self.assertTrue(json.loads((self.chain.work/'cleanup-interrupted.json').read_text())['interrupted'])

    def test_term_during_cleanup_keeps_original_business_failure(self):
        original=AssertionError('original business red');steps=[]
        def cleanup():
            os.kill(os.getpid(),signal.SIGTERM);steps.append('finished')
        with identity.cooperative_signals(),patch.object(self.chain,'run',side_effect=original),patch.object(self.chain,'cleanup',side_effect=cleanup),patch.object(identity,'emit'),self.assertRaises(AssertionError) as result:
            identity.execute(self.chain)
        self.assertIs(result.exception,original);self.assertEqual(steps,['finished'])

    def test_process_cleanup_error_still_closes_proxy_and_retires_owned_user(self):
        self.chain.tls_proxy=Mock();self.chain.user_created=True
        with patch.object(identity,'cleanup_process',side_effect=RuntimeError('controlled process failure')),patch.object(identity,'budgeted_inspect',return_value=None),patch.object(self.chain,'sql',return_value='') as sql,self.assertRaises(AssertionError):
            self.chain.cleanup()
        self.chain.tls_proxy.close.assert_called_once();sql.assert_called_once()
        self.assertIn(self.chain.user_id,sql.call_args.args[0]);self.assertNotIn('management_audit',sql.call_args.args[0])

    def test_final_log_failure_does_not_skip_owned_container_removal(self):
        info=dict(Name='/'+self.chain.container,Config=dict(Image=identity.IMAGE,Labels={identity.OWNER_LABEL:self.backend['owner'],identity.RUN_LABEL:'abc123'}),State=dict(Running=True))
        with patch.object(identity,'budgeted_inspect',return_value=info),patch.object(identity,'docker',side_effect=[identity.Blocked('controlled log failure'),'']) as docker,self.assertRaises(AssertionError):
            self.chain.cleanup()
        self.assertEqual(docker.call_args_list[1].args[0],['rm','-f',self.chain.container])


if __name__=='__main__':unittest.main()
