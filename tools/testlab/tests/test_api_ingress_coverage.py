"""Offline ingress admission, fixture, transaction and lifecycle guards only."""
import base64,contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_ingress_coverage as ingress


class IngressCoverageTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        for name in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            path=self.root/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes((ingress.ROOT/name).read_bytes())
        self.data=dict(owner=ingress.OWNER,binary_sha256='offline',source_tree_sha='offline',postgres_container='owned-pg',
                       helper_sha256=ingress.helper_hashes(),components={},fixture={})
        self.chain=ingress.Chain(self.root,self.data,str(uuid.uuid4()))
    def test_explicit_offline_inventory_does_not_acquire_resources(self):
        with patch.object(ingress,'doctor') as doctor,patch.object(ingress.readonly,'execute') as execute,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(ingress.main(['--inventory']),0)
        self.assertEqual(len(json.loads(out.getvalue())['normal_targets']),3);doctor.assert_not_called();execute.assert_not_called()
    def test_no_implicit_live(self):
        with contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):ingress.main([])
    def test_contract_rejects_unrelated_mutations_and_nonlocal_namespace(self):
        for method,path in [('GET','/api/v1/ingest/pull/source'),('POST','/api/v1/ingest/pull/source/test'),('POST','/api/v1/ingest/push/test')]:
            self.assertEqual(self.chain.contract.canonical(method,path)[1],'main')
        for method,path in [('POST','/api/v1/proposals/other/approve'),('GET','https://outside/api/v1/ingest/push'),('DELETE','/api/v1/services/other')]:
            with self.assertRaises(ingress.Blocked):self.chain.contract.canonical(method,path)
    def component(self,provider):
        port,internal,image,files=ingress.COMPONENTS[provider];folder=ingress.ROOT/'tools/testlab/components'
        target='/etc/prometheus/' if provider=='prometheus' else '/etc/alertmanager/'
        return dict(Id='a'*64,Image='sha256:'+'b'*64,Name='/aiops-'+ingress.OWNER+'-'+provider,
                    State={'Running':True},Config=dict(Image=image,Labels={'aiops.testlab.owner':ingress.OWNER}),
                    NetworkSettings={'Ports':{internal+'/tcp':[dict(HostIp='127.0.0.1',HostPort=str(port))]}},
                    Mounts=[dict(Source=str(folder/f),Destination=target+f,RW=False) for f in files])
    def test_components_require_immutable_cid_image_owned_readonly_mount_and_exact_port(self):
        for provider in ingress.COMPONENTS:
            info=self.component(provider);pinned=ingress.component_guard(provider,info)
            changed=copy.deepcopy(info);changed['Id']='c'*64
            with self.assertRaises(ingress.Blocked):ingress.component_guard(provider,changed,pinned)
            changed=copy.deepcopy(info);changed['Mounts'][0]['RW']=True
            with self.assertRaises(ingress.Blocked):ingress.component_guard(provider,changed)
            changed=copy.deepcopy(info);changed['Config']['Labels']={}
            with self.assertRaises(ingress.Blocked):ingress.component_guard(provider,changed)
    def test_source_transport_never_contacts_arbitrary_ports_paths_or_urls(self):
        for port,method,path in [(80,'GET','/api/v1/query'),(18578,'POST','/admin/shutdown'),(18570,'POST','/-/reload'),(18571,'GET','https://outside/api/v2/status')]:
            with patch.object(ingress.http.client,'HTTPConnection') as conn,self.assertRaises(ingress.Blocked):ingress.wire(port,method,path)
            conn.assert_not_called()
    def runtime(self):
        prom='scrape_configs:\n- job_name: testlab-business\n  static_configs:\n  - targets:\n    - host.docker.internal:18578\nalerting:\n  alertmanagers:\n  - static_configs:\n    - targets:\n      - alertmanager:9093\n'
        am='route:\n  receiver: testlab-capture\nreceivers:\n- name: testlab-capture\n  labels:\n    name: testlab-capture\n  webhook_configs:\n  - send_resolved: true\n    http_config:\n      follow_redirects: true\n      enable_http2: true\n    url: http://host.docker.internal:18578/capture/alertmanager\n    url_file: ""\n    max_alerts: 0\n    timeout: 0s\ntemplates: []\n'
        return prom,am
    def test_all_actual_runtime_receivers_must_be_exact_local_capture(self):
        prom,am=self.runtime();proof=ingress.validate_local_runtime(prom,am)
        self.assertTrue(proof['local_capture_possible']);self.assertFalse(proof['real_external_notifications'])
        for bad in (am.replace('host.docker.internal:18578','outside:443'),am.replace('url: http://host.docker.internal:18578/capture/alertmanager','url: <secret>'),am.replace('webhook_configs:','email_configs:'),am.replace('webhook_configs:','webhook_configs: &alias'),am.replace('url_file: ""','url_file: /tmp/external'),am.replace('enable_http2: true','proxy_from_environment: true'),am.replace('enable_http2: true','proxy_url: http://outside:80')):
            with self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(prom,bad)
        for bad in (prom.replace('testlab-business','foreign'),prom.replace('host.docker.internal:18578','outside:80'),prom+'  consul_sd_configs: []\n',prom+'remote_write:\n- url: http://outside\n',prom+'remote_read: []\n',prom+'tracing: {}\n',prom+'relabel_configs: []\n',prom+'unknown_discovery: {}\n',prom+'proxy_url: http://outside\n'):
            with self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(bad,am)
    def test_normal_http_success_requires_explicit_validator(self):
        self.chain.frozen=Mock()
        with patch.object(ingress.readonly.Chain,'call') as call,self.assertRaises(ingress.Blocked):self.chain.call('reader','GET','/api/v1/ingest/pull/source')
        call.assert_not_called()
    def test_only_private_source_env_can_be_added_after_http_identity_production(self):
        self.assertEqual(self.chain.startup_environment(),{})
        self.chain.config_hashes={'pull.json':'test','push.json':'test'}
        self.assertEqual(self.chain.startup_environment(),{'AIOPS_V3_PULL_SOURCES_CONFIG':str(self.chain.evidence_work/'pull.json'),'AIOPS_V3_PUSH_SOURCES_CONFIG':str(self.chain.evidence_work/'push.json')})
    def test_generation_start_reuses_parent_but_never_overwrites_previous_logs(self):
        for file in ('instance.json','models.json','commands.json'):ingress.readonly.private_file(self.chain.work/file,'{}')
        paths=[]
        def start(chain):paths.append(chain.work);self.assertEqual(chain.work,self.chain.evidence_work/('startup-'+str(len(paths)-1)))
        old_emit=ingress.readonly.emit
        with patch.object(ingress.readonly.Chain,'start',autospec=True,side_effect=start):self.chain.start();self.chain.start()
        self.assertEqual(self.chain.generation,2);self.assertNotEqual(*paths);self.assertIs(self.chain.work,self.chain.evidence_work);self.assertIs(ingress.readonly.emit,old_emit)
    def test_start_failure_restores_work_and_global_emitter_for_cleanup(self):
        for file in ('instance.json','models.json','commands.json'):ingress.readonly.private_file(self.chain.work/file,'{}')
        old_emit=ingress.readonly.emit
        with patch.object(ingress.readonly.Chain,'start',side_effect=ingress.Blocked('partial acquired startup')),self.assertRaises(ingress.Blocked):self.chain.start()
        self.assertEqual(self.chain.generation,1);self.assertIs(self.chain.work,self.chain.evidence_work);self.assertIs(ingress.readonly.emit,old_emit)
    def test_stop_uses_only_held_popen_and_fresh_listener_identity_before_any_signal(self):
        procs=[Mock(pid=11),Mock(pid=12)]
        for proc in procs:proc.poll.return_value=None
        self.chain.processes=procs
        with patch.object(ingress.readonly,'_process_port',side_effect=[True,False]),self.assertRaises(ingress.Blocked):self.chain.stop_owned_generation()
        for proc in procs:proc.terminate.assert_not_called();proc.kill.assert_not_called()
    def test_pg_mismatch_prevents_even_parent_cleanup_sql(self):
        with patch.object(ingress.readonly,'inspect_pg',return_value={'Id':'foreign'}),patch.object(ingress.readonly.Chain,'pg') as parent,self.assertRaises(ingress.Blocked):self.chain.pg('DROP DATABASE own')
        parent.assert_not_called()
    def test_cleanup_closes_own_relay_even_when_database_cleanup_fails(self):
        first=ingress.Blocked('retain database');self.chain.relay=Mock();self.chain.relay.close.side_effect=RuntimeError('close')
        with patch.object(ingress.readonly.Chain,'cleanup',side_effect=first),self.assertRaises(ingress.Blocked) as caught:self.chain.cleanup()
        self.assertIs(caught.exception,first);self.chain.relay.close.assert_called_once()
    def test_identity_grant_seed_never_inserts_business_attempt_receipt_or_audit(self):
        self.chain.sql=Mock()
        with patch.object(ingress.readonly.Chain,'seed'):self.chain.seed()
        statement=self.chain.sql.call_args.args[0];self.assertIn('v3_role_grants',statement)
        for forbidden in ('v3_push_events','v3_push_receipts','management_audit','v3_pull_attempts','applications'):self.assertNotIn(forbidden,statement)
    def test_rejected_budget_does_not_add_expectations_and_checks_state_or_source_io(self):
        self.chain.case_marker=Mock();self.chain.pull_call=Mock();self.chain.call=Mock();self.chain.relay=Mock(receipts=[]);self.chain.pull_path='/api/v1/ingest/pull/own'
        self.chain.ledger_counts=Mock(side_effect=[{'attempts':10},{'attempts':11}])
        with self.assertRaises(AssertionError):self.chain.budget()
        self.assertEqual(self.chain.pull_call.call_count,10);self.chain.case_marker.assert_not_called()
    def test_actual_runtime_redaction_cannot_count_as_exact_destination_proof(self):
        directory=ingress.ROOT/'.local/testlab/ingress-offline'
        if not (directory/'alertmanager-runtime-config.json').exists():self.skipTest('parent private offline sample not available')
        prom=json.loads((directory/'prometheus-runtime-config.json').read_text())['data']['yaml']
        am=json.loads((directory/'alertmanager-runtime-config.json').read_text())['config']['original']
        with self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(prom,am)
    def test_push_count_validator_rejects_synthetic_in_real_counts(self):
        value={'types':[dict(id=typ,today='0',rejected='0%') for typ in ingress.TYPES]};ingress.validate_push_counts(value)
        value['types'][0]['today']='1'
        with self.assertRaises(AssertionError):ingress.validate_push_counts(value)
    def ledger(self):
        chain=self.chain;chain.app='http-produced-app';chain.receipts={typ:index+1 for index,typ in enumerate(ingress.TYPES)}
        chain.pull_expected=[dict(actor=chain.users['reader'],kind='preview',status='success',rows=4,error=''),dict(actor='admin',kind='test',status='success',rows=6,error=''),dict(actor=chain.users['reader'],kind='preview',status='failure',rows=0,error='upstream_failed')]
        attempts=[];audits=[]
        for expected,rows in zip(chain.pull_expected,[4,6,0]):
            attempts.append(dict(expected,rows=rows,source=chain.source_id,completed='2026-10-01T00:00:00Z',dropped=0))
            audits.append(dict(actor=expected['actor'],actor_kind='user',application=chain.app,action='v3.ingest_pull.'+expected['kind'],target=chain.source_id,outcome=expected['status'],changes=dict(source_id=chain.source_id,environment_id='test',kind=expected['kind'],rows=rows,dropped=0,error_code=expected['error'])))
        push=dict(events=[],receipts=[],fences=[],bindings=[dict(source_id=chain.source_id,provider='generic',application_id=chain.app,environment_id='test')])
        for typ,rid in chain.receipts.items():
            scope=dict(source_id=chain.source_id,provider='generic',application_id=chain.app,environment_id='test',event_type=typ)
            push['events'].append(dict(scope,id='event-'+typ,synthetic=True,subject='push-test',state=dict(alert='firing',change='succeeded',incident='open',resource='updated',oncall='started')[typ]))
            push['receipts'].append(dict(scope,id=rid,synthetic=True,code=202,result='test_accepted',key_hash='key-'+typ))
            push['fences'].append(dict(source_id=chain.source_id,event_type=typ,event_id='event-'+typ,key_hash='key-'+typ))
            audits.append(dict(actor=chain.users['peer'],actor_kind='user',application=chain.app,action='v3.push.test_accepted',target=chain.source_id,outcome='test_accepted',changes=dict(source_id=chain.source_id,environment_id='test',event_type=typ,receipt_id=rid)))
        return attempts,audits,push
    def test_ledger_proves_actual_attempts_and_exact_receipt_event_fence_audit_correspondence(self):
        attempts,audits,push=self.ledger();ingress.validate_ledger(self.chain,attempts,audits,push)
        for family,key,wrong in [('attempt','source','foreign'),('attempt','actor','foreign'),('attempt','rows',0),('attempt','completed',None),('push_receipt','synthetic',False),('push_receipt','application_id','foreign'),('push_event','event_type','foreign'),('push_fence','key_hash','foreign'),('push_audit','actor','foreign'),('push_audit','actor_kind','integration')]:
            a,u,p=copy.deepcopy((attempts,audits,push))
            row=a[0] if family=='attempt' else p['receipts'][0] if family=='push_receipt' else p['events'][0] if family=='push_event' else p['fences'][0] if family=='push_fence' else u[-1]
            row[key]=wrong
            with self.subTest(family=family,key=key),self.assertRaises(AssertionError):ingress.validate_ledger(self.chain,a,u,p)
    def test_wrong_audit_receipt_or_pull_outcome_counts_can_never_be_green(self):
        attempts,audits,push=self.ledger();audits[0]['changes']['rows']=999
        with self.assertRaises(AssertionError):ingress.validate_ledger(self.chain,attempts,audits,push)
        attempts,audits,push=self.ledger();audits[-1]['changes']['receipt_id']=999
        with self.assertRaises(AssertionError):ingress.validate_ledger(self.chain,attempts,audits,push)
    def test_redacted_or_foreign_runtime_destination_blocks_before_signal_write(self):
        with patch.object(self.chain,'receiver_proof',side_effect=ingress.Blocked('cannot prove destination')),patch.object(ingress,'wire') as wire,self.assertRaises(ingress.Blocked):self.chain.real_signal()
        wire.assert_not_called();self.assertFalse((self.chain.work/'signal-receipt.json').exists())
    def test_single_failed_signal_submission_keeps_receipt_never_retries(self):
        prom,am=self.runtime();self.chain.frozen=Mock();self.chain.case_marker=Mock()
        with patch.object(self.chain,'receiver_proof'),patch.object(ingress,'wire',return_value=(503,b'{"error":"not accepted"}')) as wire,self.assertRaises(AssertionError):self.chain.real_signal()
        self.assertEqual(wire.call_count,1);self.assertEqual(wire.call_args.args[:2],(18578,'POST'));self.chain.case_marker.assert_not_called()
        self.assertEqual(json.loads((self.chain.work/'signal-receipt.json').read_text())['writes'],1)
    def test_preview_is_nonempty_bound_to_exact_controlled_signal_and_values(self):
        chain=self.chain;chain.app='app';chain.signal_id='a'*24;chain.signal=dict(signals=7);chain.expression='private-expression'
        rows=[dict(service='app',name='testlab-api',time='2026-10-01T00:00:00Z',value='107',labels=dict(signal_id=chain.signal_id,status='200'))]
        value=dict(id=chain.source_id,contract='metric',contract_version='MetricSeries v1',step=3,request=dict(host=ingress.RELAY_URL,path='/api/v1/query_range'),sample=dict(status=200,body=json.dumps(rows)),preview=dict(rows=rows,checks=[dict(label='0 dropped'),dict(label='Complete bounded response')]))
        ingress.validate_preview(value,chain)
        for key,wrong in [('service','foreign'),('value','108'),('name','foreign')]:
            broken=copy.deepcopy(value);broken['preview']['rows'][0][key]=wrong
            with self.assertRaises(AssertionError):ingress.validate_preview(broken,chain)
    def test_pull_test_rows_and_capture_time_are_real_fresh_nonempty(self):
        now=ingress.datetime.datetime.now(ingress.datetime.timezone.utc).isoformat()
        ingress.validate_test(dict(ok=True,rows=2,dropped=0,captured_at=now))
        for data in [dict(ok=True,rows=0,dropped=0,captured_at=now),dict(ok=True,rows=2,dropped=1,captured_at=now),dict(ok=True,rows=2,dropped=0,captured_at='2000-01-01T00:00:00Z')]:
            with self.assertRaises(AssertionError):ingress.validate_test(data)
    def test_pull_http_rows_must_equal_one_actual_source_response(self):
        chain=self.chain;chain.signal_id='a'*24;chain.pull_path='/api/v1/ingest/pull/'+chain.source_id;chain.relay=Mock(receipts=[])
        raw=json.dumps({'status':'success','data':{'resultType':'matrix','result':[{'metric':dict(signal_id=chain.signal_id),'values':[[1,'1']]},{'metric':dict(signal_id=chain.signal_id),'values':[[1,'2']]}]}})
        def call(*args,**kwargs):
            chain.relay.receipts.append(dict(status=200,injected=False,body=raw));return dict(ok=True,rows=3,dropped=0,captured_at='unused'),{}
        chain.call=call
        with self.assertRaises(AssertionError):chain.pull_call('peer',test=True)
        self.assertEqual(chain.pull_expected,[])
    def test_fault_cannot_claim_an_unrelated_environment_failure_as_injected_source(self):
        chain=self.chain;chain.pull_path='/api/v1/ingest/pull/'+chain.source_id;chain.relay=Mock(receipts=[])
        def call(*args,**kwargs):chain.relay.receipts.append(dict(status=502,injected=False,body='{}'));return {},{}
        chain.call=call
        with self.assertRaises(AssertionError):chain.pull_call('peer',want=502,code='upstream_failed',scenario='failure')
        self.assertEqual(chain.pull_expected,[])

    def receiver_fixture(self):
        prom,am=self.runtime();raw=am.encode();sha=ingress.hashlib.sha256(raw).hexdigest()
        info=self.component('alertmanager');info.update(Path='/bin/alertmanager',Args=['--config.file=/etc/alertmanager/alertmanager.yml','--storage.path=/alertmanager'])
        info['State']['StartedAt']='2026-10-01T00:00:00Z'
        started=ingress.datetime.datetime.fromisoformat('2026-10-01T00:00:00+00:00').timestamp();loaded=started+10
        source=dict(path=str(ingress.ROOT/'tools/testlab/components/alertmanager.yml'),device=1,inode=2,size=len(raw),ctime_ns=int((started-1)*1e9),mtime_ns=int((started-1)*1e9),sha256=sha,body=am)
        config_hash=float(int.from_bytes(ingress.hashlib.md5(raw).digest()[:6],'little'))
        metrics=f'alertmanager_config_last_reload_successful 1\nalertmanager_config_last_reload_success_timestamp_seconds {loaded}\nalertmanager_config_hash {config_hash}\n'
        masked=am.replace('url: http://host.docker.internal:18578/capture/alertmanager','url: <secret>')
        return info,source,raw,metrics,prom,masked,sha,loaded+1
    def receiver_validate(self,fixture,expected=None):
        info,source,raw,metrics,prom,masked,sha,now=fixture
        return ingress.validate_receiver_load(info,source,raw,metrics,prom,masked,sha,expected=expected,now=now)
    def test_receiver_loaded_bytes_and_official_hash_are_required_with_masked_status(self):
        data=self.receiver_fixture();proof=self.receiver_validate(data)
        self.assertFalse(proof['local_delivery']['real_external_notifications'])
        self.receiver_validate(data,expected=proof['binding'])
        for index,value in ((2,b'old container bind bytes'),(3,data[3].replace('alertmanager_config_hash ','alertmanager_config_hash 1 #'))):
            bad=list(data);bad[index]=value
            with self.assertRaises(ingress.Blocked):self.receiver_validate(bad)
    def test_receiver_metrics_fail_closed_for_missing_failed_nonfinite_hash_or_future_load(self):
        original=self.receiver_fixture()
        for name in ('alertmanager_config_last_reload_successful','alertmanager_config_last_reload_success_timestamp_seconds','alertmanager_config_hash'):
            for value in ('NaN','Inf','0'):
                data=list(original);data[3]=ingress.re.sub(r'(?m)^'+name+r' .+$',name+' '+value,data[3])
                with self.subTest(name=name,value=value),self.assertRaises(ingress.Blocked):self.receiver_validate(data)
            data=list(original);data[3]=ingress.re.sub(r'(?m)^'+name+r' .+\n','',data[3])
            with self.assertRaises(ingress.Blocked):self.receiver_validate(data)
        bad=list(original);bad[7]=original[7]-2
        with self.assertRaises(ingress.Blocked):self.receiver_validate(bad)
    def test_receiver_loaded_source_rejects_ctime_mtime_argv_cid_start_and_unknown_masked_shape(self):
        original=self.receiver_fixture();proof=self.receiver_validate(original)['binding']
        for key in ('ctime_ns','mtime_ns'):
            bad=copy.deepcopy(original);bad[1][key]=int(bad[7]*1e9)
            with self.assertRaises(ingress.Blocked):self.receiver_validate(bad)
        for args in (['--config.file=/other'],original[0]['Args']+['--config.file=/etc/alertmanager/alertmanager.yml']):
            bad=copy.deepcopy(original);bad[0]['Args']=args
            with self.assertRaises(ingress.Blocked):self.receiver_validate(bad)
        for mutate in (lambda x:x[0].update(Id='c'*64),lambda x:x[0]['State'].update(StartedAt='2026-09-30T23:59:00Z')):
            bad=copy.deepcopy(original);mutate(bad)
            with self.assertRaises(ingress.Blocked):self.receiver_validate(bad,expected=proof)
        bad=copy.deepcopy(original);bad[1]['inode']+=1
        with self.assertRaises(ingress.Blocked):self.receiver_validate(bad,expected=proof)
        for masked in (original[5].replace('http_config:','email_configs:'),original[5].replace('templates: []','- name: another\n  webhook_configs:\n  - url: <secret>\ntemplates: []')):
            bad=list(original);bad[5]=masked
            with self.assertRaises(ingress.Blocked):self.receiver_validate(bad)
    def test_source_snapshot_requires_same_fd_and_path_inode_and_no_symlink(self):
        path=self.root.resolve()/'receiver';path.write_text('known bytes')
        snap=ingress.source_fd_snapshot(path);self.assertEqual(snap['body'],'known bytes')
        actual=path.stat();changed=Mock(st_dev=actual.st_dev,st_ino=actual.st_ino+1,st_size=actual.st_size,st_ctime_ns=actual.st_ctime_ns,st_mtime_ns=actual.st_mtime_ns)
        with patch.object(ingress.os,'fstat',side_effect=[actual,changed]),self.assertRaises(ingress.Blocked):ingress.source_fd_snapshot(path)
        link=self.root.resolve()/'alias';link.symlink_to(path)
        with self.assertRaises(ingress.Blocked):ingress.source_fd_snapshot(link)
    def test_container_read_has_fixed_argv_and_bounded_output_without_shell(self):
        # Fake only the Docker client process; never execute a real command.
        proc=Mock();proc.stdout=Mock();proc.stdout.fileno.return_value=99;proc.wait.return_value=0;proc.poll.return_value=0
        with patch.object(ingress.subprocess,'Popen',return_value=proc) as popen,patch.object(ingress.select,'select',return_value=([proc.stdout],[],[])),patch.object(ingress.os,'read',side_effect=[b'config',b'']):
            self.assertEqual(ingress.container_receiver_bytes('a'*64),b'config')
        self.assertEqual(popen.call_args.args[0],['docker','exec','a'*64,'/bin/cat','/etc/alertmanager/alertmanager.yml']);self.assertNotIn('shell',popen.call_args.kwargs)
        with patch.object(ingress.subprocess,'Popen',return_value=proc),patch.object(ingress.select,'select',return_value=([proc.stdout],[],[])),patch.object(ingress.os,'read',return_value=b'x'*65537),self.assertRaises(ingress.Blocked):ingress.container_receiver_bytes('a'*64)
        with patch.object(ingress.subprocess,'Popen') as popen,self.assertRaises(ingress.Blocked):ingress.container_receiver_bytes('not-exact')
        popen.assert_not_called()
    def test_parent_private_real_shape_and_official_hash_fixture(self):
        folder=ingress.ROOT/'.local/testlab/ingress-offline'
        if not all((folder/name).is_file() for name in ('receiver-load-readonly.json','prometheus-runtime-config.json','alertmanager-runtime-config.json')):
            self.skipTest('optional parent offline runtime snapshots unavailable; synthetic load guard cases still execute')
        capture=json.loads((folder/'receiver-load-readonly.json').read_text());info=dict(capture['container'],Name='/aiops-'+ingress.OWNER+'-alertmanager',Config=capture['config'],NetworkSettings={'Ports':capture['ports']})
        source=ingress.source_fd_snapshot(ingress.ROOT/'tools/testlab/components/alertmanager.yml')
        prom=json.loads((folder/'prometheus-runtime-config.json').read_text())['data']['yaml'];masked=json.loads((folder/'alertmanager-runtime-config.json').read_text())['config']['original']
        metrics='\n'.join(capture['metrics']) if isinstance(capture['metrics'],list) else capture['metrics']
        proof=ingress.validate_receiver_load(info,source,source['body'].encode(),metrics,prom,masked,source['sha256'])
        self.assertEqual(proof['binding']['config_hash_metric'],40530629311435.0)
    def test_load_proof_fd_drift_prevents_admission_without_business_signal(self):
        info,source,raw,metrics,prom,masked,sha,_=self.receiver_fixture()
        self.chain.frozen=Mock();self.chain.data['components']={'alertmanager':ingress.component_guard('alertmanager',info)}
        self.chain.data['helper_sha256']['tools/testlab/components/alertmanager.yml']=sha
        changed=dict(source,inode=source['inode']+1)
        replies=[(200,json.dumps({'data':{'yaml':prom}}).encode()),(200,json.dumps({'config':{'original':masked}}).encode()),(200,metrics.encode())]
        with patch.object(ingress.readonly,'inspect_pg',return_value=info),patch.object(ingress,'source_fd_snapshot',side_effect=[source,changed]),patch.object(ingress,'container_receiver_bytes',return_value=raw),patch.object(ingress,'wire',side_effect=replies) as wire,self.assertRaises(ingress.Blocked):self.chain.real_signal()
        self.assertTrue(all(call.args[1]=='GET' for call in wire.call_args_list));self.assertIsNone(self.chain.receiver_load)
        self.assertFalse((self.chain.work/'signal-receipt.json').exists())
    def test_pull_audit_actor_kind_cannot_be_an_unrelated_producer(self):
        attempts,audits,push=self.ledger();audits[0]['actor_kind']='system'
        with self.assertRaises(AssertionError):ingress.validate_ledger(self.chain,attempts,audits,push)
    def test_unknown_top_level_notification_channels_fail_before_and_after_receivers(self):
        prom,source=self.runtime();masked=source.replace('url: http://host.docker.internal:18578/capture/alertmanager','url: <secret>')
        extras=('event_recorder:\n  otlp:\n    endpoint: outside:4317\n','tracing:\n  endpoint: outside:4317\n  sampling_fraction: 1\n','unknown_channel:\n  endpoint: http://outside\n')
        for config,is_masked in ((source,False),(masked,True)):
            for extra in extras:
                for value in (extra+config,config+extra):
                    with self.subTest(extra=extra,masked=is_masked),self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(prom,value,masked=is_masked)
    def test_runtime_global_proxy_unknown_transport_and_template_files_fail_closed(self):
        prom,source=self.runtime();masked=source.replace('url: http://host.docker.internal:18578/capture/alertmanager','url: <secret>')
        for block in ('global:\n  http_config:\n    proxy_url: http://outside:80\n','global:\n  http_config:\n    proxy_from_environment: true\n','global:\n  http_config:\n    unknown_transport: enabled\n','global:\n  smtp_smarthost: outside:25\n'):
            with self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(prom,block+masked,masked=True)
        with self.assertRaises(ingress.Blocked):ingress.validate_local_runtime(prom,masked.replace('templates: []','templates:\n- /tmp/unknown'),masked=True)
    def test_case_marker_accepts_matching_explicit_scope_once(self):
        self.chain.frozen=Mock()
        value=dict(run_id=self.chain.run_id,source_tree_sha=self.data['source_tree_sha'],observed=True)
        with patch.object(ingress,'emit') as emit:self.chain.case_marker('ingress.real_signal',value)
        self.assertEqual(emit.call_args.args[2],value);self.chain.frozen.assert_called_once()
    def test_case_marker_rejects_foreign_run_or_source_scope_before_emitting(self):
        self.chain.frozen=Mock()
        for value in (dict(run_id='other'),dict(source_tree_sha='other')):
            with patch.object(ingress,'emit') as emit,self.assertRaises(AssertionError):self.chain.case_marker('ingress.real_signal',value)
            emit.assert_not_called()
    def test_real_signal_can_emit_stable_case_with_its_explicit_matching_run(self):
        self.chain.frozen=Mock();signal_id='a'*24;signal={'signal_id':signal_id,'signals':5}
        metric=lambda status,value:dict(metric=dict(application='testlab',environment='test',signal_id=signal_id,status=status),value=[1,str(value)])
        response={'data':{'result':[metric('200',105),metric('500',5)]}}
        with patch.object(self.chain,'receiver_proof'),patch.object(ingress,'wire',side_effect=[(200,json.dumps(signal).encode()),(200,json.dumps(response).encode())]) as wire,patch.object(ingress,'emit') as emit:
            self.chain.real_signal()
        self.assertEqual(wire.call_count,2);self.assertEqual(sum(c.args[1]=='POST' for c in wire.call_args_list),1)
        self.assertEqual(emit.call_args.args[:2],('ingress.real_signal','passed'));self.assertEqual(emit.call_args.args[2]['run_id'],self.chain.run_id)


if __name__=='__main__':unittest.main()
