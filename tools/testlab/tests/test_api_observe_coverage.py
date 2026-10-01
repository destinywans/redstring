"""Offline source/DTO/cleanup guards, no Docker/HTTP/API acquisition."""
import base64,contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_observe_coverage as observe


class ObserveCoverageTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        for file in ('internal/apicontract/openapi.json','docs/api/v3-contract.md'):
            p=self.root/file;p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes((observe.ROOT/file).read_bytes())
        self.data={'owner':observe.OWNER,'source_tree_sha':'offline','binary_sha256':'offline','postgres_container':'owned-pg',
                   'component_identities':{},'helper_sha256':observe.helper_hashes()}
        self.chain=observe.Chain(self.root,self.data,str(uuid.uuid4()))
    def info(self,provider):
        spec=observe.COMPONENTS[provider]
        return {'Id':'a'*64,'Name':'/aiops-'+observe.OWNER+'-'+provider,'Image':'sha256:'+'b'*64,
                'State':{'Running':True},'Config':{'Image':spec['image'],'Labels':{'aiops.testlab.owner':observe.OWNER}},
                'NetworkSettings':{'Ports':{spec['internal']+'/tcp':[{'HostIp':'127.0.0.1','HostPort':str(spec['port'])}]}},
                'Mounts':[{'Source':str(observe.ROOT/'tools/testlab/components'/spec['config']),
                           'Destination':'/etc/testlab-config.yml','RW':False}]}
    def test_offline_inventory_never_adopts_or_runs_resources(self):
        with patch.object(observe,'doctor') as doctor,patch.object(observe.readonly,'execute') as execute,contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(observe.main(['--inventory']),0)
        data=json.loads(out.getvalue());self.assertTrue(data['offline_only']);self.assertEqual(len(data['normal_targets']),2)
        self.assertIn('GET /api/v1/traces',data['blocked_normal_targets']);doctor.assert_not_called();execute.assert_not_called()
    def test_live_is_explicit(self):
        with contextlib.redirect_stderr(io.StringIO()),self.assertRaises(SystemExit):observe.main([])
    def test_each_real_component_requires_exact_identity_image_and_readonly_config_mount(self):
        for provider in observe.COMPONENTS:
            info=self.info(provider);pinned=observe.guard_component(provider,info)
            changed=copy.deepcopy(info);changed['Id']='c'*64
            with self.assertRaises(observe.Blocked):observe.guard_component(provider,changed,pinned)
            for section,key,value in [('Config','Labels',{}),('State','Running',False),('Config','Image','latest')]:
                changed=copy.deepcopy(info);changed[section][key]=value
                with self.assertRaises(observe.Blocked):observe.guard_component(provider,changed)
            changed=copy.deepcopy(info);changed['Mounts'][0]['RW']=True
            with self.assertRaises(observe.Blocked):observe.guard_component(provider,changed)
            changed=copy.deepcopy(info);changed['NetworkSettings']['Ports'][observe.COMPONENTS[provider]['internal']+'/tcp'][0]['HostIp']='0.0.0.0'
            with self.assertRaises(observe.Blocked):observe.guard_component(provider,changed)
    def test_configuration_keeps_search_completeness_false_and_unique_scope(self):
        config=self.chain.telemetry_config()
        self.assertIs(config['tempo_search_fail_closed'],False)
        self.assertEqual(set(config['applications']),{self.chain.resource,self.chain.hidden_resource})
        visible=config['applications'][self.chain.resource];hidden=config['applications'][self.chain.hidden_resource]
        self.assertNotEqual(visible['loki_labels'],hidden['loki_labels']);self.assertNotEqual(visible['tempo_service'],hidden['tempo_service'])
        self.assertEqual(visible['loki_pattern_field'],'message_template')
        self.assertEqual(visible['loki_complete_lookback_seconds'],86400)
        self.assertEqual(config['loki']['url'],observe.RELAY_URL+'/loki')
    def test_bound_normal_targets_and_formal_mapping_support_only(self):
        contract=self.chain.contract
        for method,path in [('GET','/api/v1/logs?levels=error'),('GET','/api/v1/traces/'+self.chain.trace_id),('PUT','/api/v1/service-mappings'),('POST','/api/v1/services')]:
            self.assertEqual(contract.canonical(method,path)[1],'main')
        for method,path in [('DELETE','/api/v1/services/other'),('POST','/api/v1/proposals/other/approve'),('GET','//external')]:
            with self.assertRaises(observe.Blocked):contract.canonical(method,path)
    def test_seed_payloads_are_unique_current_telemetry_and_closed_two_span_graph(self):
        fixture=self.chain.make_source_data(1800000000000000000)
        self.assertEqual(len(fixture['loki']['streams']),2)
        self.assertEqual(sum(len(s['values']) for s in fixture['loki']['streams']),4)
        self.assertEqual(fixture['loki']['streams'][0]['stream']['testlab_run'],self.chain.nonce)
        resources=fixture['otel']['resourceSpans'];self.assertEqual(len(resources),2)
        for resource in resources:
            spans=resource['scopeSpans'][0]['spans'];self.assertEqual(len(spans),2)
            self.assertEqual(spans[1]['parentSpanId'],spans[0]['spanId'])
            self.assertEqual(spans[1]['traceId'],spans[0]['traceId'])
            self.assertLess(int(spans[1]['endTimeUnixNano']),int(spans[0]['endTimeUnixNano']))
        self.assertNotEqual(resources[0]['scopeSpans'][0]['spans'][0]['traceId'],resources[1]['scopeSpans'][0]['spans'][0]['traceId'])
    def test_component_transport_rejects_arbitrary_method_path_and_scope(self):
        for provider,method,path in [('loki','DELETE','/loki/api/v1/push'),('otel','POST','/admin/clear'),('tempo','GET','/api/search'),('other','GET','/ready')]:
            with patch.object(observe.http.client,'HTTPConnection') as conn,self.assertRaises(observe.Blocked):
                observe.component_request(provider,method,path,{},None)
            conn.assert_not_called()
    def test_pg_cid_mismatch_prevents_even_inherited_sql_cleanup(self):
        with patch.object(observe.readonly,'inspect_pg',return_value={'Id':'foreign'}),patch.object(observe.readonly.Chain,'pg') as parent:
            with self.assertRaises(observe.Blocked):self.chain.pg('DROP DATABASE own')
        parent.assert_not_called()
    def test_frozen_helper_drift_fails_before_component_or_parent_access(self):
        with patch.object(observe,'helper_hashes',return_value={}),patch.object(observe,'guard_live_components') as guard,patch.object(observe.readonly.Chain,'frozen') as parent:
            with self.assertRaises(observe.Blocked):self.chain.frozen()
        guard.assert_not_called();parent.assert_not_called()
    def test_exact_logs_complete_counts_patterns_and_scope_validators(self):
        value={'total':'3','errors':'1','histogram':[[1,1,1]],
               'patterns':[{'template':'controlled '+level,'level':level.upper(),'count':'1','trend':[1]} for level in ('error','warn','info')],
               'items':[{'level':level.upper(),'service':'own-app','message':json.dumps({'level':level,'marker':self.chain.nonce})} for level in ('error','warn','info')]}
        observe.validate_logs(value,self.chain.nonce,'own-app')
        for field,wrong in [('total','2'),('errors','0'),('patterns',[]),('items',[])]:
            with self.assertRaises(AssertionError):observe.validate_logs(dict(value,**{field:wrong}),self.chain.nonce,'own-app')
        value['items'][0]['service']='foreign-app'
        with self.assertRaises(AssertionError):observe.validate_logs(value,self.chain.nonce,'own-app')
    def test_exact_trace_detail_rejects_wrong_graph_operation_and_data_leak(self):
        value={'id':self.chain.trace_id,'trace_id':self.chain.trace_id,'op':'GET /testlab/observe','duration_ms':20,'version':'testlab-v1',
               'spans':[{'id':self.chain.root_span_id,'depth':0,'op':'GET /testlab/observe','service':self.chain.service_name,'status':'ok'},
                        {'id':self.chain.child_span_id,'depth':1,'op':'operation withheld','service':self.chain.service_name,'status':'error'}]}
        observe.validate_trace(value,self.chain)
        for field,wrong in [('id','foreign'),('duration_ms',10),('spans',value['spans'][:1]),('op','unsafe raw operation')]:
            with self.assertRaises(AssertionError):observe.validate_trace(dict(value,**{field:wrong}),self.chain)
    def test_parent_cleanup_failure_still_closes_only_own_relay_and_preserves_first(self):
        self.chain.relay=Mock();self.chain.relay.close.side_effect=RuntimeError('relay close')
        first=observe.Blocked('database retained')
        with patch.object(observe.readonly.Chain,'cleanup',side_effect=first),self.assertRaises(observe.Blocked) as caught:self.chain.cleanup()
        self.assertIs(caught.exception,first);self.chain.relay.close.assert_called_once()
    def test_relay_router_never_accepts_arbitrary_urls_search_or_write(self):
        for path in ('/loki/loki/api/v1/query?query=own','/tempo/api/traces/'+self.chain.trace_id):
            self.assertIn(observe.relay_target(path)[0],('loki','tempo'))
        for path in ('https://external/','//external','/tempo/api/search','/loki/admin/clear','/tempo/api/traces/nothex'):
            with self.assertRaises(observe.Blocked):observe.relay_target(path)
    def test_startup_environment_is_only_explicit_private_invocation_config(self):
        self.chain.frozen=Mock()
        with self.assertRaises(observe.Blocked):self.chain.startup_environment()
        config=self.chain.work/'telemetry.json';observe.readonly.private_file(config,json.dumps(self.chain.telemetry_config()))
        self.chain.config_digest=observe.hash_file(config)
        extra=self.chain.startup_environment();self.assertEqual(set(extra),{'AIOPS_TELEMETRY_CONFIG'})
        self.assertEqual(Path(extra['AIOPS_TELEMETRY_CONFIG']),config)
    def test_normal_target_requires_validator_and_honest_list_never_counts_as_normal(self):
        self.chain.frozen=Mock()
        with patch.object(observe.readonly.Chain,'call') as call:
            with self.assertRaises(observe.Blocked):self.chain.call('reader','GET','/api/v1/logs')
            self.chain.call('reader','GET','/api/v1/traces',want=503,code='not_configured',scenario='failure')
        call.assert_called_once()
    def test_sql_seed_adds_only_synthetic_authorization_inputs(self):
        self.chain.sql=Mock()
        with patch.object(observe.readonly.Chain,'seed') as parent:self.chain.seed()
        parent.assert_called_once();statement=self.chain.sql.call_args.args[0]
        self.assertIn('v3_role_grants',statement)
        for name in ('applications','management_audit','ops_incidents','v3_connectors'):self.assertNotIn(name,statement)
    def test_tempo_hex_and_base64_decoding_does_not_promote_partial_graph(self):
        fixture=self.chain.make_source_data(1800000000000000000)['otel']
        self.assertEqual(observe.stored_trace_span_ids(fixture),{self.chain.root_span_id,self.chain.child_span_id})
        stored={'batches':[{'instrumentationLibrarySpans':[{'spans':[
            {'spanId':base64.b64encode(bytes.fromhex(self.chain.root_span_id)).decode()},
            {'spanId':base64.b64encode(bytes.fromhex(self.chain.child_span_id)).decode()}]}]}]}
        self.assertEqual(observe.stored_trace_span_ids(stored),{self.chain.root_span_id,self.chain.child_span_id})
        stored['batches'][0]['instrumentationLibrarySpans'][0]['spans'][1]['spanId']='not-an-id'
        self.assertEqual(observe.stored_trace_span_ids(stored),{self.chain.root_span_id})
    def test_parent_transport_is_restored_when_actual_http_assertion_fails(self):
        self.chain.frozen=Mock();original=observe.readonly.transport
        with patch.object(observe.readonly.Chain,'call',side_effect=AssertionError('first HTTP red')):
            with self.assertRaises(AssertionError):self.chain.call('reader','GET','/api/v1/logs',validate=Mock())
        self.assertIs(observe.readonly.transport,original)
    def test_source_mapping_uses_only_http_current_revision_and_scoped_resources(self):
        self.chain.case_marker=Mock();calls=[]
        def call(user,method,path,body=None,**kw):
            calls.append((user,method,path,body,kw));env=kw['environment']
            if method=='GET':
                resource=self.chain.resource if env=='test' else self.chain.hidden_resource
                return {'rows':[{'service':resource,'save':{'service_id':'app-'+env,'service_environment_id':'env-'+env,'binding_revision':3,'editable':True}}]},{}
            value={'created':1} if method=='POST' else {'environment_id':env,'items':[dict(body['items'][0],binding_revision=4)]}
            kw['validate'](value);return value,{}
        self.chain.call=call;self.chain.sql=Mock();self.chain.source_scope()
        self.assertEqual(len(calls),6);self.chain.sql.assert_not_called()
        for row in [row for row in calls if row[1]=='PUT']:
            self.assertEqual(row[3]['items'][0]['binding_revision'],3)
            self.assertEqual(row[4]['index'],1);self.assertEqual(row[4]['scenario'],'fixture')
            expected=self.chain.resource if row[4]['environment']=='test' else self.chain.hidden_resource
            self.assertTrue(all(binding['resource']==expected for binding in row[3]['items'][0]['bindings']))
        self.assertEqual(self.chain.app,'app-test')
    def test_real_ingestion_submits_once_then_reads_both_complete_traces(self):
        self.chain.data['component_identities']={provider:{} for provider in observe.COMPONENTS};self.chain.case_marker=Mock()
        payload=self.chain.make_source_data(1800000000000000000);calls=[]
        def request(provider,method,path,pinned,body=None):
            calls.append((provider,method,path))
            if method=='POST':return (204,b'') if provider=='loki' else (200,b'{}')
            if provider=='tempo':return 200,json.dumps(payload['otel']).encode()
            return 200,json.dumps({'data':{'result':[{'values':payload['loki']['streams'][0]['values']}]}}).encode()
        with patch.object(observe,'component_request',side_effect=request),patch.object(observe.time,'time_ns',return_value=1800000000000000000):self.chain.real_ingest()
        self.assertEqual([(p,m) for p,m,_ in calls if m=='POST'],[('loki','POST'),('otel','POST')])
        self.assertEqual(len([c for c in calls if c[:2]==('tempo','GET')]),2)
        proof=json.loads((self.chain.work/'source-readback.json').read_text());self.assertEqual(proof['visible_log_rows'],3)
        self.chain.case_marker.assert_called_once()
    def test_tempo_readiness_timeout_never_retries_writes_or_marks_pass(self):
        self.chain.data['component_identities']={provider:{} for provider in observe.COMPONENTS};self.chain.case_marker=Mock();calls=[]
        def request(provider,method,path,pinned,body=None):
            calls.append((provider,method))
            return ((204,b'') if provider=='loki' else (200,b'{}')) if method=='POST' else (404,b'{}')
        with patch.object(observe,'component_request',side_effect=request),patch.object(observe.time,'monotonic',side_effect=[0,0,46]),self.assertRaises(observe.Blocked):self.chain.real_ingest()
        self.assertEqual([c for c in calls if c[1]=='POST'],[('loki','POST'),('otel','POST')])
        self.chain.case_marker.assert_not_called();self.assertFalse((self.chain.work/'source-readback.json').exists())
        self.assertTrue((self.chain.work/'source-readiness.json').exists())
    def test_rejected_real_ingest_keeps_first_response_and_never_replays(self):
        self.chain.data['component_identities']={provider:{} for provider in observe.COMPONENTS};self.chain.case_marker=Mock()
        with patch.object(observe,'component_request',return_value=(429,b'limited')) as request,self.assertRaises(AssertionError):self.chain.real_ingest()
        request.assert_called_once();self.chain.case_marker.assert_not_called()
        receipt=json.loads((self.chain.work/'loki-ingest-receipt.json').read_text());self.assertEqual(receipt,{'status':429,'response_body':'limited','writes':1})
    def test_audit_matches_exact_actor_target_application_environment_revision_and_bindings(self):
        self.chain.case_marker=Mock();self.chain.services={env:dict(service_id='app-'+env,service_environment_id='env-'+env,binding_revision=3) for env in ('test','staging')}
        rows=[];proof=[]
        for env,resource in (('test',self.chain.resource),('staging',self.chain.hidden_resource)):
            bindings=[dict(kind=provider,connector_id=provider,resource=resource) for provider in ('loki','tempo')]
            proof.append(dict(id='env-'+env,revision=4,bindings=bindings))
            rows.extend([dict(action='v3.service.created',application='app-'+env,target='app-'+env,actor='admin',outcome='success',changes={'environment':env}),
                         dict(action='v3.service_mapping.saved',application='app-'+env,target='env-'+env,actor='admin',outcome='success',changes=dict(environment=env,binding_revision_before=3,binding_revision_after=4,bindings_after=bindings))])
        def sql(statement,read=False):
            return json.dumps(rows) if 'FROM management_audit' in statement else 'admin' if 'FROM users' in statement else json.dumps(proof)
        self.chain.sql=sql;self.chain.audit_consistency();self.chain.case_marker.assert_called_once();self.chain.case_marker.reset_mock()
        for key,wrong in (('application','foreign'),('actor','foreign'),('target','foreign')):
            changed=rows[1][key];rows[1][key]=wrong
            with self.assertRaises(AssertionError):self.chain.audit_consistency()
            rows[1][key]=changed
        rows[1]['changes']['binding_revision_after']=99
        with self.assertRaises(AssertionError):self.chain.audit_consistency()
        rows[1]['changes']['binding_revision_after']=4
        proof[0]['bindings'][0]['resource']='foreign'
        with self.assertRaises(AssertionError):self.chain.audit_consistency()
        self.chain.case_marker.assert_not_called()
    def test_relay_only_cleanup_failure_never_emits_pass(self):
        self.chain.relay=Mock();self.chain.relay.close.side_effect=RuntimeError('close failed')
        with patch.object(observe.readonly.Chain,'cleanup'),patch.object(observe,'emit') as emit,self.assertRaises(RuntimeError):self.chain.cleanup()
        emit.assert_not_called();self.assertFalse((self.chain.work/'relay-cleanup.json').exists())


if __name__=='__main__':unittest.main()
