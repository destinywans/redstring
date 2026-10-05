import contextlib
import copy
import io
import json
import sys
import types
import unittest
import tempfile
from pathlib import Path
from unittest import mock

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as canonical_ro
import api_metrics_real_model_coverage as metrics


class RealMetricsTests(unittest.TestCase):
    def model_fixture(self):
        c=types.SimpleNamespace(model_name='own-uuid-model',model_volume='own-uuid-volume',model_cid='a'*64,
            model_labels={'aiops.testlab.owner':metrics.ro.OWNER,'aiops.testlab.run':'uuid','aiops.testlab.purpose':'metrics-real-model'},
            model_environment=dict(OLLAMA_HOST='0.0.0.0:11434',OLLAMA_MODELS='/root/.ollama/models',OLLAMA_NUM_PARALLEL='1'))
        ports={'11434/tcp':[dict(HostIp='127.0.0.1',HostPort=str(metrics.MODEL_PORT))]}
        info=dict(Name='/'+c.model_name,Id=c.model_cid,Image=metrics.OLLAMA_IMAGE,State={'Running':True},
            Config=dict(Labels=copy.deepcopy(c.model_labels),Env=[k+'='+v for k,v in c.model_environment.items()]),
            HostConfig=dict(NanoCpus=2_000_000_000,Memory=2<<30,MemorySwap=2<<30,Privileged=False,ReadonlyRootfs=True,
                CapDrop=['ALL'],SecurityOpt=['no-new-privileges'],Tmpfs={'/tmp':'rw,noexec,nosuid,size=67108864'},PortBindings=copy.deepcopy(ports)),
            NetworkSettings={'Ports':copy.deepcopy(ports)},Mounts=[dict(Type='volume',Name=c.model_volume,Destination='/root/.ollama',RW=True)],
            Path='/bin/ollama',Args=['serve'])
        return c,info

    def ledger_fixture(self):
        c=types.SimpleNamespace(apps={'test':dict(app='own-app',target='own-target',connector='own-connector')},users={'reader':'owner'})
        body={'request_id':'request-uuid','view':'request_rate','range':'15m'};conv='conversation'
        question=dict(owner_id='owner',request_id=body['request_id'],conversation_id=conv,environment_id='test',application_id='own-app',
            binding_id='own-target',connector_id='own-connector',resource_id='owned-metrics',query_id='request_rate',view_id='request_rate',range_id='15m',
            evidence_sha256='b'*64,source_fingerprint='c'*64,initial_reply_id='reply')
        rows=dict(v3_metrics_questions=[question],v3_conversations=[dict(id=conv,owner_id='owner',metrics_source_required=True,default_model_id=metrics.MODEL_ID,active_reply_id=None)],
            v3_conversation_replies=[dict(id='reply',conversation_id=conv,model_id=metrics.MODEL_ID,state='completed',user_message_id='u',assistant_message_id='a')],
            v3_conversation_messages=[dict(id='u',conversation_id=conv,role='user',parts=[]),dict(id='a',conversation_id=conv,role='assistant',state='completed',parts=[dict(type='text',markdown='Actual provider response')])],
            management_audit=[dict(action='v3.metrics.ask',actor_id='owner',actor_kind='user',target=conv,outcome='accepted',changes=dict(question,view=body['view'],range=body['range'])),
                dict(action='v3.conversation.reply',actor_id='owner',actor_kind='system',target='reply',outcome='completed',changes={'conversation_id':conv})],
            v3_conversation_events=[dict(conversation_id=conv,payload={'type':'message.started'}),dict(conversation_id=conv,payload={'type':'message.completed'})])
        return rows,c,conv,body

    def tags(self):
        return dict(models=[dict(name=metrics.MODEL,model=metrics.MODEL,digest=metrics.MODEL_MANIFEST_SHA,size=metrics.MODEL_LAYER_BYTES)])

    def test_exact_downloaded_model_manifest(self):
        self.assertEqual(metrics.verify_tags(self.tags()),metrics.MODEL_MANIFEST_SHA)
        value=self.tags();value['models'][0]['digest']='sha256:'+metrics.MODEL_MANIFEST_SHA
        self.assertEqual(metrics.verify_tags(value),metrics.MODEL_MANIFEST_SHA)

    def test_model_cache_rejects_wrong_tag_digest_and_budget(self):
        for key,bad in [('name','qwen2.5:3b'),('model','different'),('digest',metrics.MODEL_MANIFEST_SHA[:12]),('size',0),('size',True),('size',metrics.MODEL_LAYER_BYTES+4097)]:
            value=self.tags();value['models'][0][key]=bad
            with self.subTest(key=key,bad=bad),self.assertRaises(AssertionError):metrics.verify_tags(value)
        value=self.tags();value['models']*=2
        with self.assertRaises(AssertionError):metrics.verify_tags(value)

    def test_owned_model_guard_and_baseline_image_labels(self):
        c,info=self.model_fixture();info['Config']['Labels']['org.opencontainers.image.title']='immutable image label'
        self.assertEqual(metrics.guard_model(info,c),c.model_cid)

    def test_model_guard_rejects_identity_or_resource_drift(self):
        c,info=self.model_fixture()
        changes=[(('Id',),'b'*64),(('Image',),'wrong'),(('Name',),'/other'),(('HostConfig','NanoCpus'),3_000_000_000),
            (('HostConfig','Memory'),3<<30),(('HostConfig','MemorySwap'),-1),(('HostConfig','Privileged'),True),
            (('HostConfig','ReadonlyRootfs'),False),(('HostConfig','SecurityOpt'),[]),(('Config','Labels','aiops.testlab.run'),'other'),
            (('Config','Env'),[]),(('Args',),['serve','--other'])]
        for path,bad in changes:
            value=copy.deepcopy(info);node=value
            for key in path[:-1]:node=node[key]
            node[path[-1]]=bad
            with self.subTest(path=path),self.assertRaises(AssertionError):metrics.guard_model(value,c)

    def test_public_listener_and_shared_models_fail_closed(self):
        c,info=self.model_fixture()
        for mutation in ('public','port','volume','bind'):
            value=copy.deepcopy(info)
            if mutation=='public':value['NetworkSettings']['Ports']['11434/tcp'][0]['HostIp']='0.0.0.0'
            elif mutation=='port':value['NetworkSettings']['Ports']['11434/tcp'][0]['HostPort']='11434'
            elif mutation=='volume':value['Mounts'][0]['Name']='shared'
            else:value['Mounts'].append(dict(Type='bind',Source='/shared',Destination='/extra'))
            with self.subTest(mutation=mutation),self.assertRaises(AssertionError):metrics.guard_model(value,c)

    def test_stopped_model_guard_uses_configured_port_tuple(self):
        c,info=self.model_fixture();info['State']['Running']=False;info['NetworkSettings']['Ports']={}
        self.assertEqual(metrics.guard_model(info,c,running=False),c.model_cid)
        with self.assertRaises(AssertionError):metrics.guard_model(info,c)

    def test_volume_owner_must_match_before_removal(self):
        c,_=self.model_fixture();info=dict(Name=c.model_volume,Driver='local',Scope='local',Labels=c.model_labels)
        self.assertEqual(metrics.guard_volume(info,c),c.model_volume)
        for key,bad in [('Name','shared'),('Driver','external'),('Scope','global'),('Labels',{})]:
            value=copy.deepcopy(info);value[key]=bad
            with self.subTest(key=key),self.assertRaises(AssertionError):metrics.guard_volume(value,c)

    def test_real_model_counter_counts_only_actual_inference_route(self):
        text='[GIN] 200 | 1s | 127.0.0.1 | POST "/api/pull"\n[GIN] 200 | 1s | 127.0.0.1 | POST "/v1/chat/completions"\n'
        self.assertEqual(metrics.model_request_count(text),1)
        self.assertEqual(metrics.model_request_count('\x1b[32m'+text+'\x1b[0m'),1)
        self.assertEqual(metrics.model_request_count('POST "/v1/chat/completions/other"'),0)
        self.assertEqual(metrics.model_request_count('unsupported log format'),0)

    def test_private_helper_ancestry_preserves_canonical_ports(self):
        original=(canonical_ro.PORTS,canonical_ro.BASES,canonical_ro.transport)
        self.assertIsNot(metrics.ro,canonical_ro)
        self.assertIs(metrics.gen.Chain.__mro__[1],metrics.ro.Chain)
        with metrics.gen.context():self.assertEqual(metrics.ro.PORTS,metrics.PORTS)
        self.assertEqual((canonical_ro.PORTS,canonical_ro.BASES,canonical_ro.transport),original)

    def test_model_endpoint_bypasses_source_relay(self):
        c=metrics.Chain.__new__(metrics.Chain);c.configured=True;c.work=Path('/private/uuid');c.model_token='synthetic';c.rule_fixture=None
        value=c.apply_startup_environment({})
        self.assertEqual(value['OPENAI_BASE_URL'],'http://127.0.0.1:18773/v1')
        self.assertEqual(value['OPENAI_MODEL'],metrics.MODEL)
        self.assertEqual(value['OPENAI_API'],'chat_completions')
        self.assertEqual(value['OPENAI_MAX_OUTPUT_TOKENS'],'256')

    def test_source_relay_has_no_model_answer_endpoint(self):
        handlers=[]
        def server(port,handler):handlers.append((port,handler));return mock.Mock()
        with mock.patch.object(metrics.gen,'Server',side_effect=server):
            fixture=metrics.SourceOnlyFixture(types.SimpleNamespace())
        relay=handlers[1][1].__new__(handlers[1][1]);relay.path='/v1/chat/completions';relay.send_error=mock.Mock()
        relay.do_POST()
        relay.send_error.assert_called_once_with(405)
        self.assertEqual(fixture.snapshot()['model_reads'],[{'method':'POST','path':'/v1/chat/completions'}])
        self.assertFalse(hasattr(fixture,'validate_model'))

    def test_source_relay_preserves_actual_prometheus_response_bytes(self):
        handlers=[];raw=b'{"status":"success","data":{"resultType":"matrix","result":[]}}'
        with mock.patch.object(metrics.gen,'Server',side_effect=lambda port,handler:handlers.append(handler) or mock.Mock()):
            fixture=metrics.SourceOnlyFixture(types.SimpleNamespace(guard_prom=mock.Mock()))
        fixture.validate_source=mock.Mock(return_value=('test','/api/v1/query_range?original=query'))
        relay=handlers[1].__new__(handlers[1]);relay.path='/test/api/v1/query_range?original=query';relay.headers={};relay.wfile=io.BytesIO()
        relay.send_response=mock.Mock();relay.send_header=mock.Mock();relay.end_headers=mock.Mock()
        with mock.patch.object(metrics.gen,'prom_get',return_value=(200,raw)) as upstream:relay.do_GET()
        upstream.assert_called_once_with('/api/v1/query_range?original=query')
        self.assertEqual(relay.wfile.getvalue(),raw)
        self.assertEqual(fixture.snapshot()['source_reads'][0]['response'],json.loads(raw))
        self.assertEqual(fixture.snapshot()['model_reads'],[])

    def test_management_transport_cannot_generate_or_change_model_tag(self):
        c=metrics.Chain.__new__(metrics.Chain);c.inspect_model=mock.Mock(side_effect=AssertionError('must not inspect before rejecting'))
        with self.assertRaises(AssertionError):c.model_http('POST','/v1/chat/completions',{'model':metrics.MODEL})
        with self.assertRaises(AssertionError):c.model_http('POST','/api/pull',{'model':'different','stream':False})
        c.inspect_model.assert_not_called()

    def test_exporter_advertises_native_prometheus_text_format(self):
        handlers=[]
        c=types.SimpleNamespace(apps={'test':{'app':'own'}},nonce='uuid')
        with mock.patch.object(metrics.gen,'Server',side_effect=lambda port,handler:handlers.append(handler) or mock.Mock()):
            fixture=metrics.SourceOnlyFixture(c)
        export=handlers[0].__new__(handlers[0]);export.path='/metrics';export.wfile=io.BytesIO()
        export.send_response=mock.Mock();export.send_header=mock.Mock();export.end_headers=mock.Mock()
        export.do_GET()
        export.send_header.assert_any_call('Content-Type','text/plain; version=0.0.4')
        export.send_header.assert_any_call('Content-Length',str(len(export.wfile.getvalue())))
        self.assertIn(b'testlab_generation_rate{application="own",environment="test"',export.wfile.getvalue())
        self.assertEqual(fixture.snapshot()['scrapes'],1)

    def test_only_exact_explicit_uuid_absence_is_accepted(self):
        name='own-uuid'
        for kind,message in [('container','Error: No such object: '+name),('container','Error: No such container: '+name),
                ('container','Error response from daemon: No such container: '+name),('volume','Error response from daemon: get '+name+': no such volume')]:
            result=types.SimpleNamespace(returncode=1,stdout='[]\n',stderr=message+'\n')
            with self.subTest(kind=kind,message=message):self.assertTrue(metrics.explicit_absence(result,kind,name))
            with self.subTest(wrong_name=kind),self.assertRaises(metrics.Blocked):metrics.explicit_absence(result,kind,'other-uuid')

    def test_daemon_permission_timeout_and_ambiguous_nonzero_are_not_absence(self):
        for code,stdout,stderr in [(1,'','Cannot connect to the Docker daemon'),(1,'','permission denied'),
                (124,'','timeout'),(1,'',''),(2,'[]','Error: No such object: own-uuid'),
                (1,'[]','Error: No such object: own-uuid\nCannot connect to daemon'),(1,'[{}]','Error: No such object: own-uuid')]:
            result=types.SimpleNamespace(returncode=code,stdout=stdout,stderr=stderr)
            with self.subTest(stderr=stderr),self.assertRaises(metrics.Blocked):metrics.explicit_absence(result,'container','own-uuid')

    def test_continuous_scraping_is_not_replay_production(self):
        fixture=mock.Mock();fixture.snapshot.side_effect=[dict(source_reads=[{'real':True}],model_reads=[],scrapes=2),dict(source_reads=[{'real':True}],model_reads=[],scrapes=9)]
        self.assertEqual(metrics.request_snapshot(fixture),metrics.request_snapshot(fixture))
        fixture.snapshot.return_value=dict(source_reads=[{'real':True},{'replay':True}],model_reads=[],scrapes=10)
        fixture.snapshot.side_effect=None
        self.assertEqual(len(metrics.request_snapshot(fixture)['source_reads']),2)

    def test_arbitrary_real_nonempty_response_not_fixed_answer(self):
        rows,_,_,_=self.ledger_fixture()
        for text in ('The current sample is 2.5.','任意实际模型文字'):
            value=dict(messages=copy.deepcopy(rows['v3_conversation_messages']));value['messages'][1]['parts'][0]['markdown']=text
            self.assertEqual(metrics.assistant_text(value),text)

    def test_empty_failed_and_nontext_reply_not_success(self):
        rows,_,_,_=self.ledger_fixture()
        for parts,state in [([dict(type='text',markdown=' ')],'completed'),([dict(type='status',text='Model reply failed')],'failed'),([], 'streaming')]:
            value=dict(messages=copy.deepcopy(rows['v3_conversation_messages']));value['messages'][1].update(parts=parts,state=state)
            with self.subTest(parts=parts,state=state),self.assertRaises(AssertionError):metrics.assistant_text(value)

    def test_exact_native_ledger_consistency(self):
        metrics.verify_persistence(*self.ledger_fixture())

    def test_native_ledger_rejects_partial_model_or_source_state(self):
        rows,c,conv,body=self.ledger_fixture()
        changes=[('v3_metrics_questions','evidence_sha256','short'),('v3_metrics_questions','binding_id','foreign'),('v3_metrics_questions','application_id','foreign'),
            ('v3_conversations','metrics_source_required',False),('v3_conversations','active_reply_id','reply'),('v3_conversations','default_model_id','mock'),
            ('v3_conversation_replies','model_id','mock'),('v3_conversation_replies','state','failed'),('v3_conversation_replies','assistant_message_id','other')]
        for table,key,bad in changes:
            value=copy.deepcopy(rows);value[table][0][key]=bad
            with self.subTest(table=table,key=key),self.assertRaises(AssertionError):metrics.verify_persistence(value,c,conv,body)

    def test_audit_and_terminal_sse_corruption_is_detected(self):
        rows,c,conv,body=self.ledger_fixture()
        for case in ('ask_digest','ask_owner','terminal_outcome','terminal_actor','terminal_target','missing_event','duplicate_event','empty_parts'):
            value=copy.deepcopy(rows)
            if case=='ask_digest':value['management_audit'][0]['changes']['evidence_sha256']='d'*64
            elif case=='ask_owner':value['management_audit'][0]['actor_id']='other'
            elif case=='terminal_outcome':value['management_audit'][1]['outcome']='failed'
            elif case=='terminal_actor':value['management_audit'][1]['actor_kind']='user'
            elif case=='terminal_target':value['management_audit'][1]['target']='other'
            elif case=='missing_event':value['v3_conversation_events'].pop()
            elif case=='duplicate_event':value['v3_conversation_events'].append(value['v3_conversation_events'][-1])
            else:value['v3_conversation_messages'][1]['parts']=[]
            with self.subTest(case=case),self.assertRaises(AssertionError):metrics.verify_persistence(value,c,conv,body)

    def test_formal_detail_scope_includes_inferred_false(self):
        c=metrics.Chain.__new__(metrics.Chain);c.apps={'test':dict(app='own-app',expressions={})};c.model_token='secret'
        rows,_,conv,_=self.ledger_fixture();value=dict(id=conv,scope=dict(env='test',service='own-app',inferred=False),messages=rows['v3_conversation_messages'])
        def call(*args,**kwargs):kwargs['validate'](value);return value,{}
        c.call=call
        self.assertEqual(c.detail('reader',conv),value)
        value['scope']['env']='staging'
        with self.assertRaises(AssertionError):c.detail('reader',conv)

    def test_inventory_never_acquires_resources(self):
        with mock.patch.object(metrics,'doctor',side_effect=AssertionError('live forbidden')),mock.patch.object(metrics,'Chain',side_effect=AssertionError('live forbidden')),contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(metrics.main([]),0)
        value=json.loads(output.getvalue());self.assertEqual(value['status'],'prepare_only');self.assertFalse(value['old_large_model_gate_closed'])

    def test_admission_requires_current_root_and_sole_matching_runner(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory).resolve();private=root/'.local/testlab';private.mkdir(parents=True)
            path=private/'admission.json';run='test-resource-uuid';helper={'owned':'exact'}
            data=dict(status='root-admitted-metrics-real-model',owner=metrics.ro.OWNER,resource_run_id=run,
                source_tree_sha=metrics.SOURCE,binary_sha256=metrics.BINARY,runtime_sha256=metrics.RUNTIME,postgres_cid=metrics.gen.PG_CID,
                ollama_image_id=metrics.OLLAMA_IMAGE,model_manifest_sha256=metrics.MODEL_MANIFEST_SHA,ports=list(metrics.ALL_PORTS),helper_sha256=helper,
                manifest_sha256='b'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,public_model_download_authorized=True,private_model_volume_cleanup_authorized=True)
            active=dict(status='running',selected_suite=metrics.MODULE,manifest_sha='b'*64,source_tree_sha=metrics.SOURCE,pid=metrics.os.getppid())
            conn=mock.Mock();conn.execute.return_value=[(json.dumps(active),)]
            path.write_text(json.dumps(data));path.chmod(0o600)
            with mock.patch.object(metrics,'ROOT',root),mock.patch.object(metrics,'hashes',return_value=helper),mock.patch.object(metrics.sqlite3,'connect',return_value=conn) as connect:
                self.assertEqual(metrics.admission(path,run),data)
                self.assertIn('?mode=ro',connect.call_args.args[0]);conn.close.assert_called_once()
                for change in ('source','manifest','pid','parallel'):
                    altered=dict(active)
                    if change=='source':altered['source_tree_sha']='old'
                    elif change=='manifest':altered['manifest_sha']='old'
                    elif change=='pid':altered['pid']=0
                    conn.execute.return_value=[(json.dumps(altered),)]*(2 if change=='parallel' else 1)
                    with self.subTest(change=change),self.assertRaises(AssertionError):metrics.admission(path,run)
                data['public_model_download_authorized']=False;path.write_text(json.dumps(data))
                with self.assertRaises(AssertionError):metrics.admission(path,run)

    def test_formal_routes_and_existing_source_contract(self):
        contract=metrics.Contract(metrics.ROOT)
        self.assertEqual(contract.canonical('POST','/api/v1/metrics/ask')[0],'/api/v1/metrics/ask')
        for method,path in [('POST','/api/v1/alert-rules/generate'),('POST','/api/v1/conversations'),('GET','http://foreign/api/v1/metrics/ask')]:
            with self.subTest(path=path),self.assertRaises((metrics.Blocked,AssertionError)):contract.canonical(method,path)
        detail=(metrics.ROOT/'internal/v3conversation/detail.go').read_text();source=(metrics.ROOT/'internal/v3conversation/metrics_question.go').read_text()
        self.assertIn('"env": env, "inferred": false',detail)
        self.assertIn("'v3.metrics.ask'",source);self.assertIn('selectedModel.ID',source)
        self.assertEqual(metrics.manifest_pin(),metrics.MODEL_MANIFEST_SHA)

    def test_cleanup_continues_owned_resources_after_parent_failure(self):
        c=metrics.Chain.__new__(metrics.Chain);c.fixture=mock.Mock();c.prom_cid=None;c.prom_attempted=False
        c.model_cid='a'*64;c.model_attempted=True;c.volume_acquired=True;c.volume_attempted=True;c.model_volume='own';c.work=Path('/private/owned')
        c.inspect_model=mock.Mock(return_value={'State':{'Running':True}});c.inspect_volume=mock.Mock(return_value={})
        c.docker=mock.Mock(return_value=types.SimpleNamespace(returncode=0,stdout=''))
        parent='def cleanup(self):\n    deadline = time.monotonic() + 40\n    raise Blocked("parent DB cleanup failed")\n'
        sock=mock.Mock();sock.__enter__=mock.Mock(return_value=sock);sock.__exit__=mock.Mock(return_value=False);sock.connect_ex.return_value=1
        with mock.patch.object(metrics.inspect,'getsource',return_value=parent),mock.patch.object(metrics,'guard_model'),mock.patch.object(metrics,'guard_volume'),mock.patch.object(metrics.socket,'socket',return_value=sock),mock.patch.object(metrics.ro,'_private'),self.assertRaises(metrics.Blocked):
            c.cleanup()
        self.assertIsNone(c.model_cid);self.assertFalse(c.volume_acquired);c.fixture.close.assert_called_once()
        for call in c.docker.call_args_list:self.assertEqual(call.kwargs['deadline'],c.cleanup_deadline)

    def test_unconfirmed_create_is_not_adopted_or_deleted(self):
        c=metrics.Chain.__new__(metrics.Chain);c.fixture=None;c.prom_cid=None;c.prom_attempted=False
        c.model_cid=None;c.model_attempted=True;c.model_name='unknown-owned-name';c.volume_acquired=False;c.volume_attempted=False;c.work=Path('/private/owned')
        parent='def cleanup(self):\n    deadline = time.monotonic() + 40\n'
        sock=mock.Mock();sock.__enter__=mock.Mock(return_value=sock);sock.__exit__=mock.Mock(return_value=False);sock.connect_ex.return_value=1
        for result in (types.SimpleNamespace(returncode=0,stdout='unknown',stderr=''),types.SimpleNamespace(returncode=1,stdout='',stderr='Cannot connect to the Docker daemon')):
            c.docker=mock.Mock(return_value=result)
            with self.subTest(error=result.stderr),mock.patch.object(metrics.inspect,'getsource',return_value=parent),mock.patch.object(metrics.socket,'socket',return_value=sock),mock.patch.object(metrics.ro,'_private') as save,self.assertRaises(metrics.Blocked):c.cleanup()
            self.assertEqual(c.docker.call_args_list,[mock.call('inspect',c.model_name,deadline=c.cleanup_deadline)])
            self.assertIs(save.call_args.args[1]['model_removed'],False)


if __name__=='__main__':unittest.main()
