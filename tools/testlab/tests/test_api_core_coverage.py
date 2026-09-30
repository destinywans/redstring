"""Offline mocked core-coverage checks. Never launch APIs, PG or producers."""
import ast,contextlib,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_core_coverage as core


class CoreCoverageTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        for file in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            target=self.root/file;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes((core.ROOT/file).read_bytes())
        self.data={'owner':core.management.OWNER,'binary_sha256':'synthetic','source_tree_sha':'synthetic'}
        self.chain=core.Chain(self.root,self.data,str(uuid.uuid4()))
    def test_inventory_never_calls_doctor_or_starts_environment(self):
        with patch.object(core.management,'doctor') as doctor,patch.object(core.management,'execute') as execute,contextlib.redirect_stdout(io.StringIO()) as stdout:
            self.assertEqual(core.main(['--inventory']),0)
        self.assertTrue(json.loads(stdout.getvalue())['offline_only']);doctor.assert_not_called();execute.assert_not_called()
    def test_static_contract_main_aux_and_outside_rejected(self):
        for method,path in (('GET','/api/v1/inbox'),('GET','/api/v1/incidents/draft?alerts=synthetic'),('GET','/api/v1/roles/own'),('PUT','/api/v1/roles/own')):
            self.assertEqual(self.chain.contract.canonical(method,path)[1],'main')
        self.assertEqual(self.chain.contract.canonical('POST','/api/v1/webhooks/alertmanager/own')[1],'auxiliary_or_historical')
        for method,path in (('POST','/api/v1/proposals/other/approve'),('GET','/etc/passwd'),('DELETE','/api/v1/services/other')):
            with self.assertRaises(core.Blocked):self.chain.contract.canonical(method,path)
    def test_all_own_and_inherited_actual_route_patterns_use_real_canonical(self):
        routes={('GET','/api/v1/roles'),('POST','/api/v1/audit/export'),
                ('GET','/api/v1/audit/own'),('GET','/api/v1/audit/exports/audit-own'),
                ('GET','/api/v1/incidents/draft?alerts=one,two'),
                ('POST','/api/v1/webhooks/alertmanager/core-own'),
                ('GET','/api/v1/roles/own'),('PUT','/api/v1/roles/own'),
                ('DELETE','/api/v1/me/sessions/own')}
        # Capture every literal call from this packet and exact reused methods.
        for source in (Path(core.__file__).read_text(),Path(core.management.__file__).read_text()):
            tree=ast.parse(source)
            for function in ast.walk(tree):
                if not isinstance(function,ast.FunctionDef) or function.name not in ('services_and_ingress','events','audits','run','role_matrix','sessions'):continue
                for node in ast.walk(function):
                    if isinstance(node,ast.Call) and isinstance(node.func,ast.Attribute) and node.func.attr=='call' and len(node.args)>=3 and all(isinstance(node.args[index],ast.Constant) and isinstance(node.args[index].value,str) for index in (1,2)):
                        routes.add((node.args[1].value,node.args[2].value))
        for path in ('/inbox','/alert-groups','/incidents'):routes.add(('GET','/api/v1'+path))
        for method,path in sorted(routes):
            with self.subTest(method=method,path=path):
                canonical,route_class=self.chain.contract.canonical(method,path)
                self.assertIn(method.lower(),self.chain.contract.api['paths'][canonical[len('/api/v1'):]])
                self.assertIn(route_class,('main','auxiliary_or_historical'))
    def test_sensitive_headers_only_exact_scope_and_restored_on_failure(self):
        self.chain.source_token='synthetic-private-token'
        transport=Mock(return_value=(202,b'{}',{}))
        def call(chain,user,method,path,*args,**kwargs):
            return core.management.transport(core.management.BASES[0],method,path,b'{}',{'Cookie':'own-private-cookie'})
        with patch.object(core.management,'transport',transport),patch.object(core.management.Chain,'call',call):
            self.chain.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.chain.source_id,source_token=self.chain.source_token)
            self.assertEqual(transport.call_args.args[4]['Authorization'],'Bearer synthetic-private-token')
            self.assertIs(core.management.transport,transport)
            transport.side_effect=RuntimeError('offline transport failure')
            with self.assertRaises(RuntimeError):self.chain.call('admin','POST','/api/v1/incidents',incident_key='core-'+self.chain.nonce+'-case')
            self.assertIs(core.management.transport,transport)
            with self.assertRaises(core.Blocked):self.chain.call(None,'POST','/api/v1/webhooks/alertmanager/foreign',source_token=self.chain.source_token)
    def test_native_ingress_restarts_only_acquired_handles_and_has_no_sql_business_writes(self):
        for index in (0,1):(self.chain.work/('api-'+str(index)+'.log')).write_text('synthetic setup log')
        processes=[Mock(),Mock()]
        for process in processes:process.poll.return_value=None
        self.chain.processes=processes;self.chain.call=Mock(return_value=({},{}))
        rows=[{'application_id':'app-test','environment_id':'env-test'},{'application_id':'app-staging','environment_id':'env-staging'},[{'id':'one','application_id':'app-test','environment_id':'env-test'},{'id':'two','application_id':'app-test','environment_id':'env-test'},{'id':'hidden','application_id':'app-staging','environment_id':'env-staging'}]]
        self.chain.sql=Mock(side_effect=[json.dumps(row) for row in rows]);self.chain.start=Mock()
        with patch.object(core.management.Chain,'frozen'),patch.object(core.management,'command_env',Mock()) as environment:
            self.chain.services_and_ingress();self.assertIs(core.management.command_env,environment)
        self.chain.start.assert_called_once()
        for process in processes:process.terminate.assert_called_once();process.wait.assert_called_once_with(timeout=8)
        for call in self.chain.sql.call_args_list:self.assertTrue(call.args[0].startswith('SELECT '));self.assertTrue(call.kwargs['read'])
        config=json.loads((self.chain.work/'operations.json').read_text());self.assertEqual(config['feishu_channels'],[])
        self.assertEqual({row['application_id'] for row in config['sources'][0]['mappings']},{'app-test','app-staging'})
        webhook=self.chain.call.call_args
        self.assertEqual(webhook.args[0:3],(None,'POST','/api/v1/webhooks/alertmanager/'+self.chain.source_id))
        self.assertEqual(len(webhook.args[3]['alerts']),3);self.assertEqual(webhook.kwargs['want'],202)
        self.assertEqual(self.chain.alert_ids,['one','two']);self.assertEqual(self.chain.hidden_alert,'hidden')
        self.assertEqual((self.chain.work/'source-token').stat().st_mode&0o777,0o600)
    def test_original_executor_preserves_primary_and_attempts_only_invocation_cleanup(self):
        original=AssertionError('first native failure');self.chain.run=Mock(side_effect=original);self.chain.cleanup=Mock(side_effect=RuntimeError('cleanup failure'));self.chain.first_red=Mock()
        with patch.object(core.management,'emit'):
            with self.assertRaises(AssertionError) as caught:core.management.execute(self.chain)
        self.assertIs(caught.exception,original);self.chain.cleanup.assert_called_once();self.assertTrue((self.chain.work/'cleanup-error.json').is_file())
    def test_run_reuses_role_session_flows_and_records_only_existing_positives(self):
        methods=('provision','start','seed','login','services_and_ingress','events','role_matrix','sessions','audits','frozen')
        order=[]
        for name in methods:setattr(self.chain,name,Mock(side_effect=lambda *args,name=name,**kwargs:order.append(name)))
        self.chain.call=Mock(return_value=({},{}))
        core.management.append_json(self.chain.work/'interfaces.jsonl',{'method':'GET','path':'/api/v1/inbox','route_class':'main','registration_evidence':True})
        core.management.append_json(self.chain.work/'interfaces.jsonl',{'method':'GET','path':'/api/v1/me/profile','route_class':'main','registration_evidence':False})
        result=self.chain.run()
        self.assertLess(order.index('services_and_ingress'),order.index('events'));self.assertLess(order.index('role_matrix'),order.index('sessions'))
        self.assertEqual(result['positive_routes'],[['GET','/api/v1/inbox','main']]);self.assertFalse(result['formal_performance_acceptance']);self.assertFalse(result['formal_24h_acceptance'])
    def test_audit_export_scope_uses_test_and_all_rejection_is_separate(self):
        selected=[{'id':str(i),'action':action} for i,action in enumerate(('v3.service.created','v3.service.created','v3.role.matrix.updated','v3.role.matrix.updated','v3.session.revoked','incident.create'))]
        calls=[]
        def call(user,method,path,body=None,**kwargs):
            self.chain.contract.canonical(method,path);calls.append((user,method,path,body,kwargs))
            if method=='GET' and path=='/api/v1/audit':return {'items':selected},{}
            if method=='POST' and path=='/api/v1/audit/export':
                scope=kwargs.get('environment','test');want=kwargs.get('want',200)
                if scope=='all':self.assertEqual((want,kwargs.get('code'),kwargs.get('scenario')),(409,'env_view_only','boundary'))
                elif body=={'filter':'invalid'}:self.assertEqual((scope,want,kwargs.get('code')),('test',422,'invalid_filter'))
                elif user=='admin':self.assertEqual((scope,want),('test',200))
                return {'url':'/api/v1/audit/exports/audit-'+'a'*36},{}
            return {},{}
        self.chain.call=call;self.chain.audits()
        exports=[call for call in calls if call[1:3]==('POST','/api/v1/audit/export')]
        self.assertEqual(sum(call[4].get('environment','test')=='all' for call in exports),1)

    def test_two_real_base_starts_have_phase_unique_readiness_ids(self):
        self.data['binary']='offline-not-executed'
        self.chain.frozen=Mock()
        process=Mock(pid=12345);process.poll.return_value=None
        sock=Mock();sock.connect_ex.return_value=1
        emitted=[]
        def transport(*args,**kwargs):
            core.management.emit('management.other','passed',{'kept':True},type='environment')
            return 200,b'{}',{}
        with patch.object(core.management.Chain,'frozen'),patch.object(core.management,'_private'),patch.object(core.management.socket,'socket') as socket_factory,patch.object(core.management.os,'open',return_value=100),patch.object(core.management.os,'close'),patch.object(core.management.subprocess,'Popen',return_value=process),patch.object(core.management,'command_env',return_value={}),patch.object(core.management,'_process_port',return_value=True),patch.object(core.management,'transport',side_effect=transport),patch.object(core.management,'emit',side_effect=lambda *args,**kwargs:emitted.append((args,kwargs))) as emit:
            socket_factory.return_value.__enter__.return_value=sock
            self.chain.start();self.assertIs(core.management.emit,emit)
            self.chain.config_digest='offline-source-phase'
            self.chain.start();self.assertIs(core.management.emit,emit)
        readiness=[args[0] for args,kwargs in emitted if 'api_ready' in args[0]]
        self.assertEqual(readiness,['core.setup.api_ready.0','core.setup.api_ready.1','core.source.api_ready.0','core.source.api_ready.1'])
        other=[(args,kwargs) for args,kwargs in emitted if args[0]=='management.other']
        self.assertEqual(other,[(('management.other','passed',{'kept':True}),{'type':'environment'})]*4)

    def test_readiness_emit_restores_after_original_start_failure(self):
        failure=RuntimeError('offline original launch failure')
        with patch.object(core.management.Chain,'start',side_effect=failure),patch.object(core.management,'emit') as emit:
            with self.assertRaises(RuntimeError) as caught:self.chain.start()
            self.assertIs(caught.exception,failure);self.assertIs(core.management.emit,emit)


if __name__=='__main__':unittest.main()
