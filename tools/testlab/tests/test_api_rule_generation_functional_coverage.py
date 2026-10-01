import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rule_generation_functional_coverage as generation
from inventory import actual_interface

class GenerationTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);root=Path(self.temp.name).resolve()
        for n in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            f=root/n;f.parent.mkdir(parents=True,exist_ok=True);f.write_bytes((generation.ROOT/n).read_bytes())
        self.c=generation.Chain(root,{'owner':generation.OWNER,'source_tree_sha':generation.SOURCE_SHA,'binary_sha256':generation.BINARY_SHA,'helper_sha256':generation.hashes(),'postgres_container':'owned'},str(uuid.uuid4()))
        self.c.apps={env:{'app':env+'-app','expressions':{'request_rate':'owned_metric{application="'+env+'-app",environment="'+env+'"}'}} for env in ('test','staging')}
    def fixture(self):
        f=generation.Fixture.__new__(generation.Fixture);f.c=self.c;return f
    def info(self):
        return {'Name':'/'+self.c.prom_name,'Id':'c'*64,'Image':generation.PROM_IMAGE_ID,'Path':'/bin/prometheus','Args':self.c.prom_args,'State':{'Running':True},'Config':{'User':str(os.getuid())+':'+str(os.getgid()),'Labels':{'aiops.testlab.owner':generation.OWNER,'aiops.testlab.run':self.c.run_id,'aiops.testlab.purpose':'rule-generation'}},'HostConfig':{'NanoCpus':500000000,'Memory':268435456,'Privileged':False,'ReadonlyRootfs':True,'CapDrop':['ALL'],'SecurityOpt':['no-new-privileges'],'Tmpfs':{'/prometheus':self.c.tmpfs}},'NetworkSettings':{'Ports':{'9090/tcp':[{'HostIp':'127.0.0.1','HostPort':'18656'}]}},'Mounts':[{'Type':'bind','Source':str(self.c.work/'prometheus.json'),'Destination':'/etc/prometheus/owned.json','RW':False}]}
    def test_inventory_cannot_start_or_do_admission(self):
        with patch.object(generation,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(generation.main(['--inventory']),0)
        self.assertEqual(len(json.loads(out.getvalue())['targets']),3);doctor.assert_not_called()
    def test_context_restores_all_shared_names_on_error(self):
        old=generation.ro.PORTS,generation.ro.BASES,generation.ro.emit,generation.ro.transport,generation.ro.append_json
        with self.assertRaises(RuntimeError),generation.context():raise RuntimeError('controlled')
        self.assertEqual((generation.ro.PORTS,generation.ro.BASES,generation.ro.emit,generation.ro.transport,generation.ro.append_json),old)
    def test_mock_case_nested_interface_cannot_default_to_real(self):
        for path in generation.MODEL_MOCK_PATHS:
            row={'method':'GET' if '/conversations/' in path else 'POST','path':path,'scenario':'normal','status':'passed','expected':{'status':[200]},'actual':{'status':200},'registration_evidence':True}
            # This is the exact old counterexample: top-level Mock alone is lost.
            self.assertEqual(actual_interface(row)['evidence_mode'],'real_local')
            with contextlib.redirect_stdout(io.StringIO()) as out:generation.emit('owned.http','passed',row['actual'],evidence_mode='protocol_mock',interface=row)
            case=json.loads(out.getvalue());nested=case['interface'];record=actual_interface(nested)
            self.assertEqual(case['evidence_mode'],'protocol_mock');self.assertEqual(nested['evidence_mode'],case['evidence_mode']);self.assertEqual(record['evidence_mode'],'protocol_mock');self.assertEqual(record['upstream_mode'],'protocol_mock')
    def test_support_interfaces_keep_real_local_mode(self):
        row={'method':'POST','path':'/api/v1/auth/login','scenario':'normal','status':'passed','expected':{'status':[200]},'actual':{'status':200}}
        with contextlib.redirect_stdout(io.StringIO()) as out:generation.emit('owned.http','passed',row['actual'],interface=row)
        case=json.loads(out.getvalue());self.assertEqual(case['evidence_mode'],'real_local');self.assertEqual(actual_interface(case['interface'])['evidence_mode'],'real_local')
    def test_raw_sse_receipt_has_same_explicit_mock_mode(self):
        path=self.c.work/'interfaces.jsonl';row={'method':'GET','path':'/api/v1/conversations/{id}/events','scenario':'normal','status':'passed','expected':{'status':[200]},'actual':{'status':200}}
        with generation.context():generation.ro.append_json(path,row)
        record=json.loads(path.read_text());self.assertEqual(record['evidence_mode'],'protocol_mock');self.assertEqual(actual_interface(record)['evidence_mode'],'protocol_mock');self.assertNotIn('evidence_mode',row)
    def test_contract_rejects_outbound_and_unrelated_write(self):
        for m,p in generation.TARGETS:self.assertEqual(self.c.contract.canonical(m,'/api/v1'+p)[1],'main')
        for m,p in (('POST','/api/v1/alert-rules'),('POST','https://foreign/api/v1/metrics/ask'),('POST','/api/v1/config-sync/export'),('GET','/api/v1/models')):
            with self.assertRaises(generation.Blocked):self.c.contract.canonical(m,p)
    def test_native_source_revocation_uses_exact_machine_contract_parameter(self):
        canonical,kind=self.c.contract.canonical('DELETE','/api/v1/services/owned-service/external-targets/owned-target')
        self.assertEqual(canonical,'/api/v1/services/{id}/external-targets/{bindingId}');self.assertEqual(kind,'auxiliary_or_historical')
    def test_private_uuid_identifiers_credentials(self):
        self.assertTrue(self.c.database.endswith(self.c.nonce));self.assertIn('/rule-generation-parallel/',str(self.c.work));self.assertEqual((self.c.work/'credentials.json').stat().st_mode&0o777,0o600)
    def test_config_has_only_owned_exporter_and_no_notification(self):
        config=generation.prom_config();self.assertEqual(config['scrape_configs'][0]['static_configs'][0]['targets'],['host.docker.internal:18657']);self.assertNotIn('alerting',config);self.assertNotIn('remote_write',config)
    def test_exporter_is_valid_prometheus_text_and_exact_scoped_metrics(self):
        handlers=[]
        with patch.object(generation,'Server',side_effect=lambda port,handler:handlers.append((port,handler)) or Mock()):generation.Fixture(self.c)
        handler=handlers[0][1].__new__(handlers[0][1]);handler.path='/metrics';handler.send_response=Mock();handler.send_header=Mock();handler.end_headers=Mock();handler.wfile=io.BytesIO();handler.do_GET()
        handler.send_header.assert_any_call('Content-Type','text/plain; version=0.0.4')
        body=handler.wfile.getvalue().decode();self.assertEqual(len(body.splitlines()),4);self.assertIn('application="test-app",environment="test"',body);self.assertIn('application="staging-app",environment="staging"',body)
    def test_guard_rejects_image_owner_resources_readwrite_and_cid(self):
        info=self.info();self.assertEqual(generation.guard_prom(info,self.c),'c'*64)
        for mutation in (lambda d:d.update(Image='sha256:'+'f'*64),lambda d:d['Config']['Labels'].update({'aiops.testlab.run':str(uuid.uuid4())}),lambda d:d['HostConfig'].update(Memory=1073741824),lambda d:d['HostConfig'].update(Privileged=True),lambda d:d['Config'].update(User='0:0'),lambda d:d['Mounts'][0].update(RW=True),lambda d:d['NetworkSettings']['Ports']['9090/tcp'][0].update(HostIp='0.0.0.0')):
            d=copy.deepcopy(info);mutation(d)
            with self.assertRaises(generation.Blocked):generation.guard_prom(d,self.c)
        self.c.prom_cid='b'*64
        with self.assertRaises(generation.Blocked):generation.guard_prom(info,self.c)
    def test_guard_stopped_requires_exact_binding(self):
        info=self.info();info['State']['Running']=False;info['HostConfig']['PortBindings']=info['NetworkSettings']['Ports'];info['NetworkSettings']['Ports']={};self.c.prom_cid='c'*64
        self.assertEqual(generation.guard_prom(info,self.c,live=False),'c'*64)
        with self.assertRaises(generation.Blocked):generation.guard_prom(info,self.c)
    def test_prometheus_transport_is_readonly_allowlist(self):
        with patch.object(generation.http.client,'HTTPConnection') as conn,self.assertRaises(generation.Blocked):generation.prom_get('/api/v1/admin/tsdb/delete_series')
        conn.assert_not_called()
    def test_fixed_model_rejects_tools_labels_and_wrong_model(self):
        f=self.fixture();body={'model':'readonly-synthetic','stream':True,'messages':[{'role':'user','content':'Observed metric 2.5'}]};f.validate_model(body)
        for changes in ({'model':'paid-model'},{'stream':False},{'tools':[{'function':{}}]},{'messages':[{'role':'user','content':'Observed metric private_label'}]},{'messages':[{'role':'user','content':self.c.apps['test']['expressions']['request_rate']}]}):
            with self.assertRaises(AssertionError):f.validate_model(dict(body,**changes))
    def test_exact_catalog_source_window_before_transport(self):
        from urllib.parse import urlencode
        import datetime
        end=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0);args={'query':self.c.apps['test']['expressions']['request_rate'],'start':(end-datetime.timedelta(minutes=15)).isoformat(),'end':end.isoformat(),'step':'30','timeout':'5s','limit':'100'};f=self.fixture();path='/test/api/v1/query_range?'+urlencode(args);self.assertEqual(f.validate_source(path)[0],'test')
        for altered in ('/staging/api/v1/query_range?'+urlencode(args),'/test/api/v1/query_range?'+urlencode(dict(args,query='other_app_metric')),'/test/api/v1/query_range?'+urlencode(dict(args,limit='999'))):
            with self.assertRaises(generation.Blocked):f.validate_source(altered)
    def test_pg_exact_cid_before_sql(self):
        with patch.object(generation.ro,'inspect_pg',return_value={'Id':'foreign'}),patch.object(generation.ro.Chain,'pg') as pg,self.assertRaises(generation.Blocked):self.c.pg('SELECT 1')
        pg.assert_not_called()
    def test_normal_ask_requires_validator_before_http(self):
        with patch.object(self.c,'frozen'),patch.object(generation.ro.Chain,'call') as call,self.assertRaises(generation.Blocked):self.c.call('reader','POST','/api/v1/metrics/ask',{})
        call.assert_not_called()
    def test_model_fixture_override_preserves_database_model_and_addresses(self):
        self.c.configured=True;original={'DATABASE_URL':'owned-pg','AIOPS_ADDR':'127.0.0.1:18654','OPENAI_MODEL':'readonly-synthetic','OPENAI_BASE_URL':'http://127.0.0.1:9/v1','OPENAI_API_KEY':'inert'};changed=self.c.apply_startup_environment(original)
        for n in ('DATABASE_URL','AIOPS_ADDR','OPENAI_MODEL'):self.assertEqual(changed[n],original[n])
        self.assertEqual(changed['OPENAI_BASE_URL'],'http://127.0.0.1:18658/v1');self.assertEqual(changed['OPENAI_API_KEY'],self.c.model_token)
    def test_tls_container_identity_guard_does_not_adopt_other443(self):
        info={'Name':'/'+self.c.tls_name,'Id':'d'*64,'Image':generation.TLS_IMAGE,'Path':'python','Args':['-B','/tls-bridge.py'],'State':{'Running':True},'Config':{'User':str(os.getuid())+':'+str(os.getgid()),'Labels':{'aiops.testlab.owner':generation.OWNER,'aiops.testlab.run':self.c.run_id,'aiops.testlab.purpose':'rule-generation-tls'}},'HostConfig':{'NanoCpus':250000000,'Memory':134217728,'Privileged':False,'ReadonlyRootfs':True,'CapDrop':['ALL'],'SecurityOpt':['no-new-privileges']},'NetworkSettings':{'Ports':{'8443/tcp':[{'HostIp':'127.0.0.1','HostPort':'443'}]}},'Mounts':[{'Type':'bind','Source':str(self.c.work/'tls-bridge.py'),'Destination':'/tls-bridge.py','RW':False}]}
        self.assertEqual(generation.guard_tls(info,self.c),'d'*64)
        for mutation in (lambda d:d.update(Name='/docker-nginx-1'),lambda d:d.update(Image='unowned'),lambda d:d['NetworkSettings']['Ports']['8443/tcp'][0].update(HostIp='0.0.0.0'),lambda d:d['Mounts'][0].update(RW=True)):
            altered=copy.deepcopy(info);mutation(altered)
            with self.assertRaises(generation.Blocked):generation.guard_tls(altered,self.c)
    def test_synthetic_rule_source_requires_exact_reviewed_scope_token_and_grid(self):
        f=generation.RuleFixture.__new__(generation.RuleFixture);f.c=self.c;f.tokens={(kind,env):'token-'+kind+'-'+env for kind in ('preview','model') for env in self.c.apps}
        for row in self.c.apps.values():row['rule_query']=row['expressions']['request_rate']
        from urllib.parse import urlencode
        args={'query':self.c.apps['test']['rule_query'],'start':'1000','end':'605800','step':'300','limit':'2','timeout':'15s'};path='/preview/test/api/v1/query_range?'+urlencode(args);self.assertEqual(f.validate('GET',path,{'Authorization':'Bearer token-preview-test'},io.BytesIO())[0:2],('preview','test'))
        for altered,headers in ((path,{'Authorization':'Bearer token-preview-staging'}),('/preview/test/api/v1/query_range?'+urlencode(dict(args,query='unreviewed')),{'Authorization':'Bearer token-preview-test'})):
            with self.assertRaises(AssertionError):f.validate('GET',altered,headers,io.BytesIO())
    def test_cleanup_closes_sources_after_pg_failure(self):
        self.c.fixture=Mock()
        with patch.object(generation.ro.Chain,'cleanup',side_effect=generation.Blocked('PG fault')),patch.object(generation.socket,'socket') as sock,self.assertRaises(generation.Blocked):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;self.c.cleanup()
        self.c.fixture.close.assert_called_once()

if __name__=='__main__':unittest.main()
