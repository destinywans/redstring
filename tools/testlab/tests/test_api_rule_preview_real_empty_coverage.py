import copy
import contextlib
import io
import json
import subprocess
import sys
import types
import unittest
from pathlib import Path
from unittest import mock
from urllib.parse import urlencode

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as canonical_ro
import api_rule_assistant_container_coverage as canonical_linux
import api_rule_preview_real_empty_coverage as preview

class EmptyPreviewTests(unittest.TestCase):
    def fixture(self):
        c=types.SimpleNamespace(apps={'test':{'app':'a'*32,'rule_query':'owned_reviewed{application="'+('a'*32)+'"}'}},users={'reader':'actor'})
        return c,{'test':'own-token'}
    def request(self,**changes):
        c,tokens=self.fixture();args=dict(query=c.apps['test']['rule_query'],start='0',end='604800',step='300',limit='2',timeout='15s');args.update(changes)
        return c,tokens,'/preview/test/api/v1/query_range?'+urlencode(args)
    def ledger(self):
        c,_=self.fixture();digest=preview.hashlib.sha256(c.apps['test']['rule_query'].encode()).hexdigest()
        before={name:[] for name in ('management_audit','v3_rule_generate_attempts','v3_rule_generate_budget','v3_rule_preview_budget')}
        after=copy.deepcopy(before)
        for index,action in enumerate(('requested','observed')):
            changes=dict(environment_id='test',query_sha256=digest)
            if action=='observed':changes.update(series_count=0,sampled_intervals=0,coverage='no_observed_series')
            after['management_audit'].append(dict(id=str(index),actor_id='actor',actor_kind='user',application_id='a'*32,
                target='owned_rate',action='v3.alert_rule.preview.'+action,outcome='recorded',changes=changes))
        after['v3_rule_preview_budget']=[dict(actor_id='actor',application_id='a'*32,environment_id='test',next_allowed_at='timestamp')]
        return before,after,c
    def test_private_modules_do_not_mutate_canonical(self):
        self.assertIsNot(preview.ro,canonical_ro);self.assertIsNot(preview.linux,canonical_linux)
        self.assertEqual(canonical_linux.PORTS,(18690,18691));self.assertIs(sys.modules['api_readonly_chain'],canonical_ro)
        self.assertIsNot(preview.ro.Chain.cleanup,canonical_ro.Chain.cleanup)
    def test_interface_records_actual_linux_not_darwin_admission(self):
        row=preview.evidence(dict(binary_sha256=preview.BINARY))
        self.assertEqual(row['container_platform'],'linux/arm64');self.assertEqual(row['binary_sha256'],preview.LINUX_SHA)
        self.assertEqual(row['actual_linux_binary_sha256'],preview.LINUX_SHA)
        self.assertEqual(row['main_admission_binary_sha256'],preview.BINARY)
        self.assertEqual(row['source_tree_sha_start'],preview.SOURCE);self.assertEqual(row['source_tree_sha_end'],preview.SOURCE)
        self.assertEqual(row['linux_build_proof_sha256'],preview.PROOF_SHA)
    def test_complete_result_retains_actual_linux_binding(self):
        # Read the final method return without acquiring its real resources.
        import ast,inspect,textwrap
        function=ast.parse(textwrap.dedent(inspect.getsource(preview.Chain.run))).body[0]
        result=next(node for node in function.body if isinstance(node,ast.Return))
        expression=ast.fix_missing_locations(ast.Expression(result.value))
        value=eval(compile(expression,'<offline complete return>','eval'),dict(preview.__dict__,self=types.SimpleNamespace(run_id='uuid',calls=11)))
        self.assertEqual(value['binary_sha256'],preview.LINUX_SHA);self.assertEqual(value['container_platform'],'linux/arm64')
        self.assertEqual(value['main_admission_binary_sha256'],preview.BINARY);self.assertEqual(value['required_cases'],list(preview.REQUIRED_CASES))
    def test_contract_blocks_generate_and_other_routes(self):
        c=preview.linux.Contract(preview.ROOT)
        self.assertEqual(c.canonical('POST','/api/v1/alert-rules/preview')[0],'/api/v1/alert-rules/preview')
        for route in ('generate','enable'):
            with self.assertRaises(preview.Blocked):c.canonical('POST','/api/v1/alert-rules/'+route)
    def test_configured_api_creation_keeps_preview_without_draft_adapter(self):
        import tempfile
        class StopBeforeCreate(RuntimeError):pass
        with tempfile.TemporaryDirectory() as temporary:
            chain=preview.Chain.__new__(preview.Chain)
            chain.nonce='a'*32;chain.run_id=str(preview.uuid.UUID(hex=chain.nonce));chain.generation=2;chain.work=Path(temporary);chain.configured=True
            chain.build={'binary':str(Path(temporary)/'owned-linux')};chain.image_env=['PATH=/usr/bin']
            chain.db_user='own_role';chain.db_password='test-only-db';chain.database='own_database'
            chain.admin_email='synthetic@testlab.invalid';chain.password='test-only-password'
            chain.hosts=('owned-query.testlab.invalid','unused-model.testlab.invalid');chain.network_name='own-network';chain.containers=[]
            chain.rule_fixture=types.SimpleNamespace(env_tokens=lambda:{'TESTLAB_RULE_PREVIEW_TEST':'test-only-preview-token'})
            captured={}
            def private_file(path,raw):captured['env']=dict(line.split('=',1) for line in raw.splitlines())
            def docker(argv,timeout=12):
                if argv[0]=='inspect':return subprocess.CompletedProcess([],1,'','Error: No such object: '+argv[-1])
                self.assertEqual(argv[0],'create');captured['argv']=argv;raise StopBeforeCreate()
            chain.docker=docker
            with mock.patch.dict(preview.Chain.create_container.__globals__,resources=mock.Mock()),mock.patch.object(preview.ro,'private_file',side_effect=private_file),mock.patch.object(preview.ro,'acquisition_signals',return_value=contextlib.nullcontext()),self.assertRaises(StopBeforeCreate):
                chain.create_container('api',1)
            env=captured['env']
            self.assertNotIn('AIOPS_V3_RULE_DRAFT_MODEL_CONFIG',env);self.assertNotIn('AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS',env)
            self.assertEqual(env['AIOPS_V3_RULE_PREVIEW_CONFIG'],'/owned/preview-config.json')
            self.assertEqual(env['AIOPS_V3_RULE_PREVIEW_ALLOWED_HOSTS'],chain.hosts[0])
            self.assertEqual(env['TESTLAB_RULE_PREVIEW_TEST'],'test-only-preview-token')
            self.assertEqual(env['SSL_CERT_FILE'],'/owned/tls-cert.pem')
            self.assertIn('127.0.0.1:'+str(preview.PORTS[1])+':8080',captured['argv'])
            # The private original path actually injects the unusable draft
            # adapter config; its named model tokens are absent in this fixture.
            # The same behavior assertion fails against that original path.
            with mock.patch.dict(preview.linux.Chain.create_container.__globals__,resources=mock.Mock()),mock.patch.object(preview.ro,'private_file',side_effect=private_file),mock.patch.object(preview.ro,'acquisition_signals',return_value=contextlib.nullcontext()),self.assertRaises(StopBeforeCreate):
                preview.linux.Chain.create_container(chain,'api',1)
            with self.assertRaises(AssertionError):self.assertNotIn('AIOPS_V3_RULE_DRAFT_MODEL_CONFIG',captured['env'])
            self.assertIn('AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS',captured['env'])
            self.assertFalse(any(name.startswith('TESTLAB_RULE_MODEL_') for name in captured['env']))
    def test_current_binary_proof_reads_actual_bytes(self):
        with mock.patch.object(preview.subprocess,'run',side_effect=AssertionError('no native command')),mock.patch.object(preview.subprocess,'check_output',side_effect=AssertionError('no native command')):
            build=preview.prepared_build()
        self.assertEqual(build['binary_sha256'],preview.LINUX_SHA)
        self.assertEqual(build['source_tree_sha_end'],preview.SOURCE)
    def test_pinned_actual_omitempty_config_and_empty_targets(self):
        folder=preview.ROOT/'.local/testlab/cp300-rule-preview-prom-config-diagnostic/6bc74731-efae-4e67-ba23-4aecdf662c6e'
        raw=(folder/'config-actual-response.raw').read_bytes()
        self.assertEqual(preview.hashlib.sha256(raw).hexdigest(),'e58c9d9451c43ee534ee0992f3f3c10f907f0bcf05dbf6e979028a3e33520790')
        self.assertTrue(preview.empty_loaded_config(200,raw)['omitted'])
        targets=(folder/'targets-actual-response.raw').read_bytes()
        self.assertEqual(preview.hashlib.sha256(targets).hexdigest(),'8ab1deb4dd2b9469a15098a7281d2e141c4e35fac2f4279723c84a27ae406615')
        self.assertEqual(preview.empty_targets(200,targets)['activeTargets'],[])
        explicit=json.loads(raw);explicit['data']['yaml']+='scrape_configs: []\n'
        self.assertFalse(preview.empty_loaded_config(200,json.dumps(explicit).encode())['omitted'])
    def test_loaded_nonempty_config_rejected_even_with_empty_targets(self):
        folder=preview.ROOT/'.local/testlab/cp300-rule-preview-prom-config-diagnostic/6bc74731-efae-4e67-ba23-4aecdf662c6e'
        value=json.loads((folder/'config-actual-response.raw').read_bytes())
        for suffix in ('scrape_configs:\n  - job_name: hidden\n','scrape_configs: null\n','scrape_configs: {}\n','unknown_field: []\n','global: []\n'):
            changed=copy.deepcopy(value);changed['data']['yaml']+=suffix
            with self.assertRaises(AssertionError):preview.empty_loaded_config(200,json.dumps(changed).encode())
        self.assertEqual(preview.empty_targets(200,b'{"status":"success","data":{"activeTargets":[],"droppedTargets":[],"droppedTargetCounts":{}}}')['activeTargets'],[])
    def test_loaded_source_envelopes_and_target_shapes_fail_closed(self):
        normal=dict(status='success',data=dict(activeTargets=[],droppedTargets=[],droppedTargetCounts={}))
        for mutation in (dict(status='error'),dict(warnings=['partial']),dict(data={}),dict(data=dict(activeTargets=None,droppedTargets=[],droppedTargetCounts={})),
            dict(data=dict(activeTargets=[{'unknown':'target'}],droppedTargets=[],droppedTargetCounts={})),dict(data=dict(activeTargets=[],droppedTargets=[{}],droppedTargetCounts={})),
            dict(data=dict(activeTargets=[],droppedTargets=[],droppedTargetCounts={'hidden':1})),dict(data=dict(activeTargets=[],droppedTargets=[],droppedTargetCounts={},unknown=[]))):
            with self.assertRaises(AssertionError):preview.empty_targets(200,json.dumps(dict(normal,**mutation)).encode())
        with self.assertRaises(AssertionError):preview.empty_targets(503,json.dumps(normal).encode())
        for value in (dict(status='success',data=dict(yaml=None)),dict(status='success',data=dict(yaml='global: []\n')),dict(status='success',data=dict(yaml='',unknown=True))):
            with self.assertRaises(AssertionError):preview.empty_loaded_config(200,json.dumps(value).encode())
        with self.assertRaises(json.JSONDecodeError):preview.empty_loaded_config(200,b'not json')
    def test_private_targets_transport_exact_path_only_without_listener(self):
        raw=b'{"status":"success","data":{}}';connection=mock.Mock()
        response=mock.Mock(status=200);response.read.return_value=raw;connection.getresponse.return_value=response
        with mock.patch.object(preview.gen.http.client,'HTTPConnection',return_value=connection):
            self.assertEqual(preview.gen.prom_get('/api/v1/targets'),(200,raw))
            with self.assertRaises(preview.Blocked):preview.gen.prom_get('http://foreign/api/v1/targets')
            with self.assertRaises(preview.Blocked):preview.gen.prom_get('/api/v1/targets/other')
        with self.assertRaises(preview.Blocked):canonical_linux.generation.prom_get('/api/v1/targets')
    def test_source_raw_receipts_saved_before_semantic_failure(self):
        import tempfile
        with tempfile.TemporaryDirectory() as temporary:
            c=types.SimpleNamespace(work=Path(temporary));raw=b'{"status":"error","data":{}}'
            preview.save_source_result(c,'config',503,raw)
            with self.assertRaises(AssertionError):preview.empty_loaded_config(503,raw)
            self.assertEqual((c.work/'prom-config-actual.raw').read_bytes(),raw)
            meta=json.loads((c.work/'prom-config-actual.json').read_text())
            self.assertEqual(meta['sha256'],preview.hashlib.sha256(raw).hexdigest());self.assertEqual(meta['status'],503)
            self.assertEqual((c.work/'prom-config-actual.raw').stat().st_mode&0o777,0o600)
    def test_no_old_current_pointer_fallback(self):
        with mock.patch.object(preview,'PROOF_SHA','0'*64),self.assertRaises(preview.Blocked):preview.prepared_build()
    def test_real_empty_matrix(self):
        self.assertEqual(preview.empty_matrix(b'{"status":"success","data":{"resultType":"matrix","result":[]}}')['data']['result'],[])
    def test_nonempty_and_warning_fail_not_fabricate_empty(self):
        for raw in (dict(status='success',data=dict(resultType='matrix',result=[dict(values=[])])),
            dict(status='success',data=dict(resultType='matrix',result=[]),warnings=['partial']),dict(status='error',data=dict(resultType='matrix',result=[]))):
            with self.assertRaises(AssertionError):preview.empty_matrix(json.dumps(raw).encode())
    def test_scoped_real_range(self):
        c,tokens,path=self.request()
        with mock.patch.object(preview.time,'time',return_value=604810):
            env,upstream=preview.validate_range(c,tokens,path,{'Authorization':'Bearer own-token'})
        self.assertEqual(env,'test');self.assertEqual(upstream,'/api/v1/query_range?'+path.split('?',1)[1])
    def test_query_window_and_token_rejected(self):
        for changes in ({'query':'up'},{'end':'604799'},{'step':'60'},{'limit':'0'},{'timeout':'30s'}):
            c,tokens,path=self.request(**changes)
            with mock.patch.object(preview.time,'time',return_value=604810),self.assertRaises(AssertionError):preview.validate_range(c,tokens,path,{'Authorization':'Bearer own-token'})
        c,tokens,path=self.request()
        with self.assertRaises(AssertionError):preview.validate_range(c,tokens,path,{'Authorization':'Bearer other-token'})
    def test_cross_endpoint_and_duplicate_args_rejected(self):
        c,tokens,path=self.request()
        for changed in (path.replace('/preview/test/','/preview/staging/'),path+'&query=up'):
            with self.assertRaises((preview.Blocked,AssertionError)):preview.validate_range(c,tokens,changed,{'Authorization':'Bearer own-token'})
    def test_absence_daemon_timeout_permission_never_success(self):
        for rc,err in ((1,'Cannot connect to the Docker daemon'),(1,'permission denied'),(124,'Error: No such object: own'),(1,'Error: No such object: foreign')):
            with self.assertRaises(preview.Blocked):preview.explicit_absence(subprocess.CompletedProcess([],rc,'',err),'container','own')
    def test_exact_absence_container_and_network(self):
        self.assertTrue(preview.explicit_absence(subprocess.CompletedProcess([],1,'[]','Error: No such object: own'),'container','own'))
        self.assertTrue(preview.explicit_absence(subprocess.CompletedProcess([],1,'','Error response from daemon: network own not found'),'network','own'))
    def test_exact_lowercase_absence_and_other_errors(self):
        name='aiops-'+preview.ro.OWNER+'-rule-'+('a'*32)+'-api-1-0'
        exact='error: no such object: '+name+'\n'
        self.assertTrue(preview.explicit_absence(subprocess.CompletedProcess([],1,'[]\n',exact),'container',name))
        for rc,out,err in ((1,'[]',exact.replace(name,'foreign')),(2,'[]',exact),(124,'[]',exact),
            (1,'unexpected stdout',exact),(1,'[]','error: cannot connect to the Docker daemon'),
            (1,'[]','ERROR: NO SUCH OBJECT: '+name),(1,'[]','error: no such object: '+name+' extra')):
            with self.assertRaises(preview.Blocked):preview.explicit_absence(subprocess.CompletedProcess([],rc,out,err),'container',name)
    def test_unknown_create_cleanup_daemon_error_is_blocked(self):
        chain=preview.Chain.__new__(preview.Chain);chain.cleanup_deadline=None;chain.network_name='network';chain.network_id=None
        chain.prom_name='prom';chain.prom_cid=None;chain.nonce='a'*32;chain.containers=[];chain.work=Path('/own')
        with mock.patch.object(preview.linux.Chain,'docker',return_value=subprocess.CompletedProcess([],1,'','Cannot connect to the Docker daemon')),mock.patch.object(preview.ro,'append_json'):
            with self.assertRaises(preview.Blocked):chain.docker(['inspect','prom'])
    def test_docker_failed_inspect_records_bounded_result_before_guard(self):
        chain=preview.Chain.__new__(preview.Chain);chain.cleanup_deadline=123;chain.network_name='network';chain.network_id=None
        chain.prom_name='prom';chain.prom_cid=None;chain.nonce='a'*32;chain.containers=[];chain.work=Path('/own')
        result=subprocess.CompletedProcess([],1,'x'*5000,'Cannot connect to the Docker daemon')
        order=[]
        def record(path,value):order.append('record');self.assertEqual(path,chain.work/'docker-absence-checks.jsonl');self.assertEqual(value['returncode'],1);self.assertEqual(value['target'],'prom');self.assertEqual(value['phase'],'cleanup');self.assertEqual(len(value['stdout']),4096);self.assertTrue(value['stdout_truncated']);self.assertEqual(value['stderr'],result.stderr)
        def guard(*args):order.append('guard');raise preview.Blocked('daemon remains unconfirmed')
        with mock.patch.object(preview.linux.Chain,'docker',return_value=result),mock.patch.object(preview.ro,'append_json',side_effect=record),mock.patch.object(preview,'explicit_absence',side_effect=guard):
            with self.assertRaises(preview.Blocked):chain.docker(['inspect','prom'])
        self.assertEqual(order,['record','guard'])
    def test_relay_preserves_actual_bytes_without_listener(self):
        c,_=self.fixture();c.work=Path('/own');c.prom_cid='p'*64;c.nonce='nonce'
        held={}
        def server(port,handler,*args):held['handler']=handler;return types.SimpleNamespace(close=lambda:None)
        raw=b'{ "status": "success", "data": {"resultType":"matrix","result":[]} }'
        with mock.patch.object(preview.gen,'Server',side_effect=server):fixture=preview.EmptyFixture(c)
        handler=held['handler'].__new__(held['handler']);handler.path='range';handler.headers={};handler.wfile=io.BytesIO();headers=[]
        handler.send_response=mock.Mock();handler.send_header=lambda *args:headers.append(args);handler.end_headers=mock.Mock()
        with mock.patch.object(preview,'validate_range',return_value=('test','/api/v1/query_range?owned')),mock.patch.object(preview.gen,'prom_get',return_value=(200,raw)) as upstream:
            handler.do_GET()
        self.assertEqual(handler.wfile.getvalue(),raw);self.assertIn(('Content-Type','application/json'),headers)
        self.assertEqual(fixture.snapshot()[0]['raw_sha256'],preview.hashlib.sha256(raw).hexdigest());upstream.assert_called_once()
    def test_empty_dto_never_zero_history(self):
        c,_=self.fixture();dto=preview.expected_dto(c.apps['test'],'test','nonce')
        self.assertEqual(dto['coverage'],'no_observed_series');self.assertIsNone(dto['current']);self.assertIsNone(dto['backtest']);self.assertIsNone(dto['fires'])
        self.assertEqual(dto['points'],[]);self.assertEqual(dto['values'],[])
    def test_exact_audit_contract_recorded(self):
        before,after,c=self.ledger();preview.verify_preview_audits(before,after,c,'test')
        source=(preview.ROOT/'internal/v3ruleassistant/service.go').read_text()
        self.assertIn("VALUES($1,'user',$2,$3,$4,'recorded',$5)",source)
    def test_partial_or_wrong_outcome_audit_rejected(self):
        for variant in ('missing','outcome','foreign_actor','invented_windows','extra','generate','budget'):
            before,after,c=self.ledger()
            if variant=='missing':after['management_audit'].pop()
            elif variant=='outcome':after['management_audit'][0]['outcome']='succeeded'
            elif variant=='foreign_actor':after['management_audit'][0]['actor_id']='other'
            elif variant=='invented_windows':after['management_audit'][1]['changes']['sampled_intervals']=1
            elif variant=='extra':after['management_audit'].append(dict(id='extra',action='unexpected'))
            elif variant=='generate':after['v3_rule_generate_attempts'].append({'status':'succeeded'})
            else:after['v3_rule_preview_budget'][0]['environment_id']='staging'
            with self.assertRaises(AssertionError):preview.verify_preview_audits(before,after,c,'test')
    def test_source_empty_contract_remains_explicit(self):
        source=(preview.ROOT/'internal/v3ruleassistant/preview.go').read_text()
        self.assertIn('Coverage: "no_observed_series"',source);self.assertIn('if len(series) == 1',source)
        reader=(preview.ROOT/'internal/v3ruleassistant/preview_reader.go').read_text()
        self.assertIn('u.Port() != ""',reader)
    def test_private_adapter_anchor_drift_blocked(self):
        with self.assertRaises(preview.Blocked):preview.replace_function(preview.expected_dto,[('no such anchor','replacement')],preview.__dict__)
    def test_cleanup_shared_clock_no_extended_grace(self):
        self.assertEqual(preview.REQUIRED_CASES[-1],'preview_empty.cleanup')
        chain=types.SimpleNamespace(cleanup_deadline=123,processes=[],db_user='own_role',database='own_database',
            owner_tag='own_tag',work=Path('/own'),db_attempted=True,role_attempted=True,diagnostic=mock.Mock(),pg=mock.Mock(side_effect=RuntimeError('stop before native')))
        with mock.patch.object(preview.time,'monotonic',return_value=100),self.assertRaises(RuntimeError):preview.ro.Chain.cleanup(chain)
        self.assertEqual(chain.pg.call_args.kwargs['deadline'],123)
    def test_resource_budget_includes_additional_prom(self):
        self.assertEqual(preview.linux.NEW_MEMORY,(512*2+128+256)<<20)
        with self.assertRaises(preview.Blocked):preview.linux.resource_admission(16<<30,10<<30,5.5,0)

if __name__=='__main__':unittest.main()
