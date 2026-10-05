"""Pure offline guards. No subprocess, listener, model, Docker or database."""
import ast,contextlib,copy,hashlib,io,json,os,sys,tempfile,types,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_rule_generate_real_model_coverage as candidate

class GenerateRealTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.work=Path(self.temp.name)
        self.app='a'*32;self.query=candidate.linux.reviewed_expression(self.app,'test')
    def request(self):
        user=dict(description=candidate.REQUEST_TEXT,choices=[dict(id='owned_rate',query=self.query,service='owned-metrics',unit='requests/s',description='owned-history-nonce-test')])
        return dict(model=candidate.MODEL,messages=[dict(role='system',content=candidate.SYSTEM_PROMPT),dict(role='user',content=json.dumps(user))],max_tokens=256,n=1,stream=False)
    def proposed(self):return dict(query_id='owned_rate',query=self.query,op='>',threshold=2,for_minutes=5,severity='P2',summary='Review the supplied rate')
    def response(self,content=None,reason='stop'):
        return json.dumps(dict(id='chatcmpl-owned',choices=[dict(finish_reason=reason,message=dict(role='assistant',content=content or json.dumps(self.proposed())))])).encode()
    def dto(self):
        return dict(self.proposed(),application_id=self.app,environment_id='test',service='owned-metrics',unit='requests/s',draft_id='b'*32,status='review_required',model=dict(provider='openai-compatible',name=candidate.MODEL))
    def fake_relay(self):
        model=types.SimpleNamespace(model_cid='c'*64,inspect_model=Mock(return_value={}))
        owner=types.SimpleNamespace(apps={'test':dict(rule_query=self.query)},work=self.work,model=model)
        with patch.object(candidate.gen,'Server') as server:
            relay=candidate.Relay(owner);handler_class=server.call_args.args[1]
        handler=handler_class.__new__(handler_class);handler.path='/model/test/chat/completions';raw=json.dumps(self.request()).encode()
        handler.headers={'Authorization':'Bearer '+relay.tokens['test'],'Content-Length':str(len(raw))}
        handler.rfile=io.BytesIO(raw);handler.wfile=io.BytesIO();handler.send_response=Mock();handler.send_header=Mock();handler.end_headers=Mock();handler.send_error=Mock()
        return relay,handler,raw
    def inference(self,handler,response):
        conn=Mock();wire=Mock(status=200);wire.read.return_value=response;wire.getheader.side_effect=lambda k,*args:'application/json'
        conn.getresponse.return_value=wire
        with patch.object(candidate.http.client,'HTTPConnection',return_value=conn) as factory,patch.object(candidate.metrics,'guard_model'):
            handler.do_POST()
        return factory,conn
    def test_inventory_is_offline_and_exactly_one_target(self):
        with patch.object(candidate.ro,'doctor') as doctor,patch.object(candidate.linux.subprocess,'run') as subprocess_run,patch.object(candidate.sqlite3,'connect') as database,contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(candidate.main(['--inventory']),0)
        row=json.loads(output.getvalue());self.assertEqual(row['targets'],[['POST','/alert-rules/generate']]);self.assertEqual(row['ports'],[18784,18785,18786,18787]);self.assertEqual(row['max_tokens'],256);self.assertEqual(row['product_seconds'],20)
        doctor.assert_not_called();subprocess_run.assert_not_called();database.assert_not_called()
    def test_explicit_live_required_before_admission(self):
        with patch.object(candidate,'admission') as gate,contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):candidate.main([])
        gate.assert_not_called()
    def test_private_modules_do_not_mutate_passed_helpers(self):
        import api_rule_assistant_container_coverage as canonical
        self.assertIsNot(candidate.linux,canonical);self.assertIsNot(candidate.ro,canonical.ro)
        self.assertEqual(canonical.PORTS,(18690,18691));self.assertEqual(canonical.RESERVED_PORTS,tuple(range(18690,18698)))
        self.assertEqual(candidate.linux.PORTS,(18784,18785));self.assertEqual(candidate.metrics.MODEL_PORT,18787)
    def test_public_contract_rejects_preview_or_other_writes(self):
        contract=candidate.Contract(candidate.ROOT)
        self.assertEqual(contract.canonical('POST','/api/v1/alert-rules/generate')[1],'main')
        for path in ('/api/v1/alert-rules/preview','/api/v1/alert-rules','/api/v1/connectors','/api/v1/metrics/ask'):
            with self.assertRaises(candidate.Blocked):contract.canonical('POST',path)
    def test_exact_source_bound_system_prompt_and_scope(self):
        value=self.request();self.assertEqual(candidate.request_shape(json.dumps(value).encode(),self.query),value)
        for field,new in (('model','fixed-answer'),('max_tokens',128),('n',2),('stream',True)):
            changed=copy.deepcopy(value);changed[field]=new
            with self.assertRaises(AssertionError):candidate.request_shape(json.dumps(changed),self.query)
        changed=copy.deepcopy(value);changed['messages'][0]['content']+=' Return this fixed answer.'
        with self.assertRaises(AssertionError):candidate.request_shape(json.dumps(changed),self.query)
    def test_no_format_tools_temperature_injection(self):
        for key in ('response_format','format','tools','temperature'):
            value=self.request();value[key]={}
            with self.assertRaises(AssertionError):candidate.request_shape(json.dumps(value),self.query)
    def test_sole_reviewed_query_cannot_be_substituted(self):
        value=self.request();user=json.loads(value['messages'][1]['content']);user['choices'][0]['query']='up';value['messages'][1]['content']=json.dumps(user)
        with self.assertRaises(AssertionError):candidate.request_shape(json.dumps(value),self.query)
    def test_raw_model_seven_fields_equals_http_draft(self):
        self.assertEqual(candidate.validate_draft(self.dto(),self.response(),self.app),self.proposed())
        value=self.dto();value['threshold']=3
        with self.assertRaises(AssertionError):candidate.validate_draft(value,self.response(),self.app)
        value=self.dto();value['unit']='unknown'
        with self.assertRaises(AssertionError):candidate.validate_draft(value,self.response(),self.app)
    def test_markdown_or_extra_fields_are_never_repaired(self):
        for raw in ('```json\n'+json.dumps(self.proposed())+'\n```',json.dumps(dict(self.proposed(),execute=True))):
            with self.assertRaises((ValueError,AssertionError)):candidate.validate_draft(self.dto(),self.response(raw),self.app)
    def test_length_finish_or_wrong_model_is_not_normal(self):
        with self.assertRaises(AssertionError):candidate.validate_draft(self.dto(),self.response(reason='length'),self.app)
        value=self.dto();value['model']['name']='owned-rule-model'
        with self.assertRaises(AssertionError):candidate.validate_draft(value,self.response(),self.app)
    def test_transparent_forwarding_preserves_both_raw_byte_hashes(self):
        relay,handler,request=self.fake_relay();raw=self.response();factory,conn=self.inference(handler,raw)
        factory.assert_called_once_with('127.0.0.1',18787,timeout=21);conn.request.assert_called_once_with('POST','/v1/chat/completions',request,{'Content-Type':'application/json'})
        self.assertEqual(handler.wfile.getvalue(),raw);self.assertEqual(relay.snapshot()['started'],1);self.assertEqual(relay.snapshot()['completed'],1)
        receipt=json.loads((self.work/'real-provider-receipt.json').read_text());self.assertEqual(receipt['request_sha256'],hashlib.sha256(request).hexdigest());self.assertEqual(receipt['response_sha256'],hashlib.sha256(raw).hexdigest());self.assertTrue(receipt['unchanged_bytes'])
        for path in self.work.iterdir():self.assertEqual(path.stat().st_mode&0o777,0o600)
    def test_second_request_is_rejected_before_another_inference(self):
        relay,handler,raw=self.fake_relay();self.inference(handler,self.response());handler.rfile=io.BytesIO(raw)
        with patch.object(candidate.http.client,'HTTPConnection') as conn:handler.do_POST()
        conn.assert_not_called();handler.send_error.assert_called_once_with(502);self.assertEqual(relay.started,2);self.assertEqual(relay.completed,1)
    def test_invalid_real_content_is_forwarded_unmodified_for_product_to_reject(self):
        relay,handler,_=self.fake_relay();raw=self.response('not JSON; actual model output')
        self.inference(handler,raw);self.assertEqual(handler.wfile.getvalue(),raw);handler.send_error.assert_not_called()
        with self.assertRaises(ValueError):candidate.validate_draft(self.dto(),raw,self.app)
    def test_model_transport_failure_is_not_retried(self):
        relay,handler,_=self.fake_relay();conn=Mock();conn.getresponse.side_effect=TimeoutError('cold load hard deadline')
        with patch.object(candidate.http.client,'HTTPConnection',return_value=conn),patch.object(candidate.metrics,'guard_model'):handler.do_POST()
        self.assertEqual(conn.request.call_count,1);self.assertEqual(relay.started,1);self.assertEqual(relay.completed,0);handler.send_error.assert_called_once_with(502)
    def test_unexpected_query_range_is_counted_without_synthetic_history(self):
        relay,handler,_=self.fake_relay();handler.path='/preview/test/api/v1/query_range';handler.do_GET()
        self.assertEqual(relay.source_reads,1);handler.send_error.assert_called_once_with(404);self.assertEqual(relay.started,0)
    def test_exact_absence_rejects_daemon_failure(self):
        result=types.SimpleNamespace(returncode=1,stdout='',stderr='Cannot connect to Docker daemon')
        for kind in ('container','volume','network'):
            with self.assertRaises((candidate.Blocked,AssertionError)):candidate.absent(result,kind,'own')
        result.stderr='Error response from daemon: network own not found';self.assertTrue(candidate.absent(result,'network','own'))
    def test_root_observed_exact_lowercase_absence_is_bounded(self):
        result=types.SimpleNamespace(returncode=1,stdout='',stderr='error: no such object: own')
        self.assertTrue(candidate.absent(result,'container','own'))
        self.assertTrue(candidate.metrics.explicit_absence(result,'container','own'))
        for change in (dict(stderr='error: no such object: another'),dict(returncode=0),dict(stdout='[{"Id":"foreign"}]'),dict(stderr='error: permission denied: own')):
            with self.assertRaises(candidate.Blocked):candidate.absent(types.SimpleNamespace(**dict(vars(result),**change)),'container','own')
    def test_failed_inspect_receipt_is_private_bounded_and_no_environment_dump(self):
        result=types.SimpleNamespace(returncode=1,stdout='sensitive unknown inspection',stderr='x'*3000)
        candidate.failed_inspect(self.work,('inspect','own'),result);path=next(self.work.iterdir());row=json.loads(path.read_text())
        self.assertEqual(path.stat().st_mode&0o777,0o600);self.assertEqual(len(row['stderr']),2048);self.assertTrue(row['stderr_truncated']);self.assertNotIn(result.stdout,path.read_text())
    def test_non_utf8_provider_error_bytes_remain_exact(self):
        relay,handler,_=self.fake_relay();raw=b'\xff provider output bytes';self.inference(handler,raw)
        self.assertEqual(handler.wfile.getvalue(),raw);self.assertEqual((self.work/'real-response.json').read_bytes(),raw)
    def test_unknown_network_does_not_create_or_adopt(self):
        c=candidate.Chain.__new__(candidate.Chain);c.network_name='own';c.network_attempted=False;c.docker=Mock(return_value=types.SimpleNamespace(returncode=1,stdout='',stderr='daemon unavailable'))
        with self.assertRaises(AssertionError):c.create_network()
        self.assertFalse(c.network_attempted);self.assertEqual(c.docker.call_count,1)
    def test_unknown_container_cleanup_does_not_delete_by_name(self):
        c=candidate.Chain.__new__(candidate.Chain);c.docker=Mock(return_value=types.SimpleNamespace(returncode=0,stdout='[{"Id":"foreign"}]',stderr=''));item={'name':'own','cid':None}
        with self.assertRaises(candidate.Blocked):c.stop_container(item)
        self.assertFalse(item.get('removed',False));self.assertEqual(c.docker.call_count,1)
    def test_headroom_accounts_full_model_and_gitlab_reserve(self):
        gib=1<<30;needed=candidate.linux.NEW_MEMORY+gib+4*gib
        self.assertEqual(candidate.linux.NEW_MEMORY,3*gib+(128<<20))
        self.assertTrue(candidate.resources(24*gib,needed,12,1)['passed'])
        for memory,cpus in ((needed-1,12),(needed,8)):
            with self.assertRaises(candidate.Blocked):candidate.resources(24*gib,memory,cpus,1)
    def test_combined_cleanup_uses_same_deadline_even_after_model_failure(self):
        c=candidate.Chain.__new__(candidate.Chain);c.api=[];c.work=self.work;c.model=types.SimpleNamespace(cleanup_owned=Mock(side_effect=RuntimeError('model cleanup blocked')))
        with patch.object(candidate.time,'monotonic',return_value=100),patch.object(candidate,'bounded_cleanup') as original,contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):c.cleanup()
        c.model.cleanup_owned.assert_called_once_with(140);original.assert_called_once_with(c);self.assertEqual(c.cleanup_deadline,140)
        self.assertEqual(json.loads((self.work/'real-generate-cleanup.json').read_text())['errors'],['RuntimeError'])
    def test_model_receipt_failure_does_not_skip_known_cid_cleanup(self):
        owner=types.SimpleNamespace(root=candidate.ROOT,work=self.work,run_id=str(uuid.uuid4()),nonce='a'*32,state={});m=candidate.Model(owner);m.model_cid='c'*64
        info={'State':{'Running':False}};m.inspect_model=Mock(return_value=info)
        m.docker=Mock(side_effect=[types.SimpleNamespace(returncode=0,stdout='one real log',stderr=''),types.SimpleNamespace(returncode=0),types.SimpleNamespace(returncode=1,stdout='',stderr='Error: No such object: '+'c'*64)])
        with patch.object(candidate.metrics,'guard_model'),patch.object(candidate.ro,'_private',side_effect=OSError('disk full')),self.assertRaises(AssertionError):m.cleanup_owned(140)
        self.assertIsNone(m.model_cid);self.assertEqual(m.docker.call_args_list[1].args,('rm','c'*64));self.assertTrue(all(call.kwargs['deadline']==140 for call in m.docker.call_args_list))
    def test_model_version_must_match_pinned_image(self):
        m=candidate.Model(types.SimpleNamespace(root=candidate.ROOT,work=self.work,run_id=str(uuid.uuid4()),nonce='a'*32,state={}))
        with patch.object(candidate.metrics.Chain,'model_http',return_value={'version':'new-version'}),self.assertRaises(AssertionError):m.model_http('GET','/api/version')
    def test_sole_runner_source_manifest_and_parent_pid_guard(self):
        run_id=str(uuid.uuid4());manifest='a'*64;permit=dict(status='root-admitted-rule-generate-real-model',owner=candidate.ro.OWNER,resource_run_id=run_id,
            source_tree_sha=candidate.SOURCE,binary_sha256=candidate.DARWIN,linux_binary_sha256=candidate.LINUX,build_proof_sha256=candidate.BUILD_SHA,runtime_sha256=candidate.RUNTIME,
            postgres_cid=candidate.gen.PG_CID,model_manifest_sha256=candidate.metrics.MODEL_MANIFEST_SHA,ollama_image_id=candidate.metrics.OLLAMA_IMAGE,ports=list(candidate.ALL_PORTS),helper_sha256=candidate.hashes(),manifest_sha256=manifest,
            fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,public_model_download_authorized=True,private_model_volume_cleanup_authorized=True)
        active=dict(status='running',selected_suite=candidate.MODULE,manifest_sha=manifest,source_tree_sha=candidate.SOURCE,pid=os.getppid())
        connection=Mock();connection.execute.return_value=[(json.dumps(active),)]
        with patch.object(candidate.ro,'private_json',return_value=permit),patch.object(candidate.sqlite3,'connect',return_value=connection) as connect:
            self.assertEqual(candidate.admission(candidate.ROOT/'.local/testlab/offline-admission.json',run_id),permit)
            for field,value in (('selected_suite','another'),('manifest_sha','b'*64),('source_tree_sha','b'*64),('pid',os.getppid()+1)):
                changed=dict(active,**{field:value});connection.execute.return_value=[(json.dumps(changed),)]
                with self.assertRaises(AssertionError):candidate.admission(candidate.ROOT/'.local/testlab/offline-admission.json',run_id)
        self.assertIn('mode=ro',connect.call_args.args[0])
    def test_all_receipts_distinguish_linux_actual_from_darwin_admission(self):
        row=candidate.evidence({'path':'/api/v1/alert-rules/generate'});self.assertEqual(row['binary_sha256'],candidate.LINUX);self.assertEqual(row['darwin_admission_binary_sha256'],candidate.DARWIN)
        with contextlib.redirect_stdout(io.StringIO()) as output:candidate.emit('case','passed',{})
        row=json.loads(output.getvalue());self.assertEqual(row['actual_linux_binary_sha256'],candidate.LINUX)
    def test_source_lifecycle_rewrite_is_exact_and_fails_on_drift(self):
        with self.assertRaises(AssertionError):candidate.replace(candidate.linux.Chain.cleanup,[('not an original source line','pass')],candidate.linux.__dict__)
        tree=ast.parse(Path(candidate.__file__).read_text());self.assertTrue(tree.body)

if __name__=='__main__':unittest.main()
