"""Offline checks only; never open API/model sockets, PG, Docker or Go."""
import ast,contextlib,io,json,sys,tempfile,time,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_canvas_coverage as canvas
import api_management_chain as original

class CanvasCoverageTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        for file in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            target=self.root/file;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes((canvas.ROOT/file).read_bytes())
        self.chain=canvas.Chain(self.root,{'owner':canvas.management.OWNER,'source_tree_sha':'offline','binary_sha256':'offline'},str(uuid.uuid4()))
    def test_inventory_and_module_globals_are_isolated(self):
        with patch.object(canvas.management,'doctor') as doctor,patch.object(canvas.management,'execute') as execute,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(canvas.main(['--inventory']),0)
        self.assertEqual(json.loads(out.getvalue())['ports'],[18608,18609]);doctor.assert_not_called();execute.assert_not_called()
        self.assertEqual(original.PORTS,(18606,18607));self.assertIsNot(original,canvas.management)
    def test_ten_targets_are_fixed_main_operations_and_outside_rejected(self):
        self.assertEqual(len(canvas.TARGETS),10)
        for method,path in canvas.TARGETS:
            actual=path.replace('{id}','own').replace('{node}','child')
            self.assertEqual(self.chain.contract.canonical(method,'/api/v1'+actual),('/api/v1'+path,'main'))
        for method,path in (('POST','/api/v1/proposals/own/approve'),('POST','/api/v1/action-plans/own/execute'),('GET','http://else.invalid/api/v1/canvases')):
            with self.assertRaises(canvas.Blocked):self.chain.contract.canonical(method,path)
    def test_literal_requests_all_reach_real_canonical(self):
        tree=ast.parse(Path(canvas.__file__).read_text())
        for node in ast.walk(tree):
            if isinstance(node,ast.Call) and isinstance(node.func,ast.Attribute) and node.func.attr=='call' and len(node.args)>=3 and all(isinstance(node.args[i],ast.Constant) for i in (1,2)):
                self.chain.contract.canonical(node.args[1].value,node.args[2].value)
    def test_template_seed_is_configuration_only_private_and_manual(self):
        self.chain.app='own-app';self.chain.sql=Mock();self.chain.configure_template()
        sql=self.chain.sql.call_args.args[0]
        self.assertTrue(sql.startswith('INSERT INTO v3_investigation_templates('));self.assertNotIn('INSERT INTO investigations',sql);self.assertNotIn('INSERT INTO branches',sql)
        definition=json.loads((self.chain.work/'template-definition.json').read_text())
        self.assertEqual(definition['origin'],'synthetic_test_configuration');self.assertEqual(definition['definition']['triggers'],{'manual':True,'auto_triage':False,'matchers':[]})
        self.assertEqual(definition['definition']['budget']['tools'],0);self.assertEqual(definition['definition']['tools'],[]);self.assertEqual(definition['definition']['runbooks'],[])
        self.assertIn('High TODO',definition['author_import_producer']);self.assertEqual((self.chain.work/'template-definition.json').stat().st_mode&0o777,0o600)
    def test_template_start_and_replay_use_native_http_not_business_sql(self):
        self.chain.app='own-app';self.chain.sql=Mock(return_value='1');calls=[]
        def call(user,method,path,body=None,**kw):
            self.chain.contract.canonical(method,path);calls.append((method,path,body,kw))
            value={'items':[{'id':self.chain.template}]} if method=='GET' else {'investigation_id':'INV-native'}
            if kw.get('validate'):kw['validate'](value)
            return value,{}
        self.chain.call=call;self.chain.templates()
        self.assertEqual(self.chain.investigation,'INV-native');self.assertEqual(calls[1][3]['want'],201);self.assertEqual(calls[2][3]['scenario'],'idempotency')
        self.assertEqual(calls[1][2],calls[2][2]);self.assertTrue(self.chain.sql.call_args.kwargs['read'])
    def test_canvas_revision_writeback_and_mock_scope(self):
        self.chain.investigation='INV-own';self.chain.app='own-app';self.chain.sql=Mock(side_effect=AssertionError('no business SQL permitted'))
        root={'id':'root','context':{'version':'v0'}};child={'id':'child','parent':'root','layout_version':0,'context':{'version':'v0'}};calls=[];graphs=[{'nodes':[root,{'id':'template-child','parent':'root'}]}, {'nodes':[{'id':'root','context':{'version':'v1'}},{'id':'child','parent':'root','context':{'version':'v1'}}]}]
        self.chain.graph=Mock(side_effect=graphs)
        def call(user,method,path,body=None,**kw):
            self.chain.contract.canonical(method,path);calls.append((method,path,body,kw))
            value={}
            if kw.get('want',200)>=400:return {},{}
            if path.endswith('/branches'):value=child
            elif method=='PATCH':value={**child,'x':123,'y':234,'layout_version':1}
            elif 'writeback-preview?' in path:value={'target':'root','conclusions':[{'id':'message-native'}],'source_revision':1,'target_revision':0}
            elif path.endswith('/writeback'):value={'target_revision':1,'source_revision':1}
            elif path=='/api/v1/canvases':value={'id':'CV-own'}
            elif path.startswith('/api/v1/canvases/'):value={'reply':self.chain.answer}
            elif path.endswith('/messages'):value={'summary':self.chain.answer}
            if kw.get('validate'):kw['validate'](value)
            return value,{}
        self.chain.call=call;self.chain.canvas();self.chain.sql.assert_not_called()
        writes=[c for c in calls if c[1].endswith('/writeback')]
        self.assertEqual(writes[0][2]['expected_source_revision'],1);self.assertEqual(writes[0][2]['expected_target_revision'],0)
        self.assertEqual(writes[1][3]['code'],'writeback_revision_conflict')
    def test_start_config_restored_after_original_failure_and_only_owned_dsn(self):
        server=Mock(server_port=12345);thread=Mock();failure=RuntimeError('offline launch')
        self.chain.data['binary']='offline-not-run'
        with patch.object(canvas.http.server,'ThreadingHTTPServer',return_value=server),patch.object(canvas.threading,'Thread',return_value=thread),patch.object(canvas.management,'command_env') as env,patch.object(canvas.management.Chain,'start',side_effect=failure):
            with self.assertRaises(RuntimeError) as caught:self.chain.start()
            self.assertIs(caught.exception,failure);self.assertIs(canvas.management.command_env,env)
        thread.start.assert_called_once()
    def test_cleanup_closes_mock_even_on_parent_failure(self):
        self.chain.mock=Mock();self.chain.mock_thread=Mock();self.chain.mock_thread.is_alive.return_value=False
        failure=RuntimeError('offline parent cleanup failure')
        with patch.object(canvas.management.Chain,'cleanup',side_effect=failure):
            with self.assertRaises(RuntimeError) as caught:self.chain.cleanup()
        self.assertIs(caught.exception,failure);self.chain.mock.shutdown.assert_not_called();self.chain.mock.server_close.assert_called_once();self.chain.mock_thread.join.assert_called_once_with(timeout=3)
    def test_sse_uses_own_fixed_base_and_actual_terminal_payload(self):
        self.chain.cookies['reader']='synthetic';self.chain.call=Mock(side_effect=[({'id':'conversation'},{}),({'reply_id':'reply'}, {})])
        raw=('data: '+json.dumps({'type':'text.delta','delta':self.chain.answer})+'\n\ndata: '+json.dumps({'type':'message.completed'})+'\n\n').encode()
        with patch.object(self.chain,'sse_response',return_value=(200,raw,{'Content-Type':'text/event-stream'})),patch.object(canvas.management,'emit'):
            self.chain.events()
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text());self.assertTrue(row['registration_evidence']);self.assertEqual(row['path'],'/api/v1/conversations/{id}/events');self.assertEqual(row['actual']['model_source'],'protocol_mock')
    def test_sse_original_failure_kept_without_false_positive(self):
        self.chain.cookies['reader']='synthetic';self.chain.call=Mock(side_effect=[({'id':'conversation'},{}),({'reply_id':'reply'}, {})]);self.chain.first_red=Mock()
        with patch.object(self.chain,'sse_response',return_value=(200,b'data: {}\n\n',{'Content-Type':'text/event-stream'})),patch.object(canvas.management,'emit'):
            with self.assertRaises(AssertionError):self.chain.events()
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text());self.assertFalse(row['registration_evidence']);self.assertEqual(row['status'],'failed');self.chain.first_red.assert_called_once()
    def test_sse_deadline_transport_closes_exact_fd_and_timer(self):
        self.chain.cookies['reader']='synthetic';connection=Mock();response=Mock(status=200);response.read.return_value=b'data: {}\n\n';response.getheaders.return_value=[('Content-Type','text/event-stream')];connection.getresponse.return_value=response
        timer=Mock()
        with patch.object(canvas.http.client,'HTTPConnection',return_value=connection),patch.object(canvas.threading,'Timer',return_value=timer):
            self.chain.sse_response('/api/v1/conversations/own/events?reply_id=own')
        timer.start.assert_called_once();timer.cancel.assert_called_once();timer.join.assert_called_once();response.close.assert_called_once();connection.close.assert_called_once();connection.sock.close.assert_called_once()
    def test_mock_cleanup_after_thread_start_failure_never_calls_shutdown(self):
        for failure in (RuntimeError('thread acquisition failed'),KeyboardInterrupt()):
            with self.subTest(failure=type(failure).__name__):
                server=Mock();thread=Mock();thread.start.side_effect=failure;thread.is_alive.return_value=False
                server.shutdown.side_effect=AssertionError('shutdown would wait forever without serve_forever')
                with patch.object(canvas.http.server,'ThreadingHTTPServer',return_value=server),patch.object(canvas.threading,'Thread',return_value=thread):
                    with self.assertRaises(type(failure)) as caught:self.chain.start()
                    self.assertIs(caught.exception,failure)
                with patch.object(canvas.management.Chain,'cleanup'):
                    self.chain.cleanup()
                server.shutdown.assert_not_called();server.server_close.assert_called_once()
    def test_sse_cancel_preserves_original_and_nonpass_evidence_even_if_reporting_fails(self):
        failure=KeyboardInterrupt();self.chain.cookies['reader']='synthetic';self.chain.call=Mock(side_effect=[({'id':'conversation'},{}),({'reply_id':'reply'}, {})]);self.chain.first_red=Mock()
        with patch.object(self.chain,'sse_response',side_effect=failure),patch.object(canvas.management,'emit',side_effect=RuntimeError('offline report failed')):
            with self.assertRaises(KeyboardInterrupt) as caught:self.chain.events()
        self.assertIs(caught.exception,failure);self.chain.first_red.assert_called_once()
        row=json.loads((self.chain.work/'interfaces.jsonl').read_text());self.assertFalse(row['registration_evidence']);self.assertEqual(row['status'],'interrupted');self.assertIsNone(row['actual']['status'])
    def test_own_loopback_server_closes_after_unstarted_thread_and_parent_failure(self):
        # Only an ephemeral stdlib socket; no requests or product environment.
        with patch.object(canvas.threading.Thread,'start',side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):self.chain.start()
        server=self.chain.mock;started=time.monotonic()
        with patch.object(canvas.management.Chain,'cleanup',side_effect=RuntimeError('parent cleanup failure')):
            with self.assertRaises(RuntimeError):self.chain.cleanup()
        self.assertLess(time.monotonic()-started,.5);self.assertEqual(server.socket.fileno(),-1);self.assertFalse(self.chain.mock_thread.is_alive())
    def test_own_started_polling_thread_stops_and_socket_closes(self):
        with patch.object(canvas.management.Chain,'start',side_effect=RuntimeError('offline API launch')):
            with self.assertRaises(RuntimeError):self.chain.start()
        server=self.chain.mock;thread=self.chain.mock_thread;started=time.monotonic()
        with patch.object(canvas.management.Chain,'cleanup'):self.chain.cleanup()
        self.assertLess(time.monotonic()-started,1);self.assertFalse(thread.is_alive());self.assertEqual(server.socket.fileno(),-1);self.assertEqual(self.chain.mock_cleanup_errors,[])

if __name__=='__main__':unittest.main()
