"""Offline actual-call refusal/wire and full-ledger counterexamples; no live IO."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_conversation_object_session_coverage as c
class ConversationBoundariesTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve();self.cid='c'*32;self.title='Owned conversation '+('a'*32)
    def chain(self):
        x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.users={'reader':'b'*32,'peer':'d'*32};x.roles={'reader':'role-reader','peer':'role-peer'};x.cookies={'reader':'memory-only-old','peer':'memory-only-peer'};x.calls=0;x.frozen=Mock();x.first_red=Mock();x.contract=c.Contract(c.ROOT);x.marker_case=Mock();return x
    def ledger(self):return dict({t:[] for t in c.TABLES},business={'users':'hash','models':'hash','other_table':'hash'})
    def detail(self):return dict(id=self.cid,title=self.title,mode='chat',status='active',scope={'env':'test','inferred':False},messages=[],artifacts=[],side={'connectors':[],'context':{'used':0,'budget':None,'parts':[]},'recalled':[]})
    def error(self,code='not_found'):return {'error':{'code':code,'message':'Conversation not found' if code=='not_found' else 'Authentication required'}}
    def response(self,status=404,value=None,headers=None):return status,json.dumps(self.error() if value is None else value).encode(),{'Content-Type':'application/json'} if headers is None else headers
    def actual_call(self,x,result,**kw):
        # Both globals patched only as an offline transport. Old code skips its
        # wrapper, so the actual self.call counterexample detects missing wire.
        fake=Mock(return_value=result)
        with patch.object(c.base,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
            value=x.call('peer','GET','/api/v1/conversations/'+self.cid+'/events',want=404,code='not_found',scenario='authorization',index=1,**kw)
            self.assertIs(c.base.transport,fake);self.assertIs(c._namespace['transport'],fake)
        return value,fake,[json.loads(v) for v in out.getvalue().splitlines()]
    def test_actual_call_saves_wire_false_registration_and_no_cookie(self):
        x=self.chain();value,fake,events=self.actual_call(x,self.response());self.assertEqual(value[0],self.error());fake.assert_called_once()
        wire=json.loads((self.work/'conversation-wire-1.json').read_text());self.assertEqual(wire['API_index'],1);self.assertEqual(wire['status'],404);self.assertEqual(len(wire['response_sha256']),64)
        self.assertNotIn('memory-only',json.dumps(wire));self.assertNotIn('Cookie',json.dumps(wire));self.assertEqual(wire['response_body'],json.dumps(self.error()))
        row=json.loads((self.work/'interfaces.jsonl').read_text());self.assertFalse(row['registration_evidence']);self.assertEqual(row['status'],'passed');self.assertTrue(all(v['interface']['registration_evidence'] is False for v in events));x.first_red.assert_not_called()
    def test_actual_success_detail_never_registers_normal(self):
        x=self.chain();fake=Mock(return_value=self.response(200,self.detail()))
        with patch.object(c.base,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()):x.call('reader','GET','/api/v1/conversations/'+self.cid,validate=lambda v:c.verify_detail(v,self.cid,self.title))
        self.assertFalse(json.loads((self.work/'interfaces.jsonl').read_text())['registration_evidence'])
    def test_actual_sse_refusal_eventstream_or_metadata_leak_fails_before_pass(self):
        for result in (self.response(headers={'Content-Type':'text/event-stream'}),self.response(value=dict(self.error(),conversation={'owner':'other'})),self.response(value={'error':dict(self.error()['error'],owner='other')})):
            x=self.chain();fake=Mock(return_value=result)
            with patch.object(c.base,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
                with self.assertRaises(AssertionError):x.call('peer','GET','/api/v1/conversations/'+self.cid+'/events',want=404,code='not_found',scenario='authorization')
            x.first_red.assert_called_once();self.assertTrue(all(json.loads(v)['status']=='failed' for v in out.getvalue().splitlines()));self.assertFalse(x.first_red.call_args.args[0]['registration_evidence'])
    def test_actual_wrong404_or401_status_and_code_retains_first_red(self):
        for want,code,result in ((404,'not_found',self.response(200,self.detail())),(401,'unauthorized',self.response()),(401,'unauthorized',self.response(401,self.error()))):
            x=self.chain();fake=Mock(return_value=result)
            with patch.object(c.base,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(AssertionError):x.call('reader','GET','/api/v1/conversations/'+self.cid,want=want,code=code,scenario='authorization')
            x.first_red.assert_called_once();fake.assert_called_once();self.assertTrue((self.work/'failure-response-1.json').is_file())
    def test_actual_wire_write_failure_restores_transport_and_no_retry(self):
        x=self.chain();fake=Mock(return_value=self.response());original=c._namespace['transport']
        with patch.object(c.base,'transport',fake),patch.dict(c._namespace,transport=fake),patch.object(c.base,'_private',side_effect=OSError('offline write failure')),contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaises(AssertionError):x.call('peer','GET','/api/v1/conversations/'+self.cid+'/events',want=404,code='not_found')
            self.assertIs(c.base.transport,fake);self.assertIs(c._namespace['transport'],fake)
        self.assertIs(c._namespace['transport'],original);fake.assert_called_once();x.first_red.assert_called_once()
    def test_refusal_detects_every_full_ledger_residue(self):
        for table in self.ledger():
            x=self.chain();before=self.ledger();after=copy.deepcopy(before);after[table]={'changed':'hash'} if table=='business' else [{'residue':'owned'}];x.ledger=Mock(side_effect=[before,after]);x.call=Mock()
            with self.assertRaises(AssertionError):x.refusal('peer','POST','/api/v1/conversations/'+self.cid+'/messages',{'text':'valid','model':c.MODEL})
            self.assertEqual(x.call.call_args.kwargs['want'],404)
    def test_exact_empty_detail_rejects_state_env_message_or_context_change(self):
        c.verify_detail(self.detail(),self.cid,self.title);c.verify_detail(dict(self.detail(),side=dict(self.detail()['side'],context={'used':0,'budget':2048,'parts':[]})),self.cid,self.title)
        for changes in ({'status':'running'},{'messages':[{}]},{'scope':{'env':'staging','inferred':False}},{'mode':'canvas'},{'artifacts':[{}]},{'extra':'secret'}):
            with self.assertRaises(AssertionError):c.verify_detail(dict(self.detail(),**changes),self.cid,self.title)
        bad=self.detail();bad['side']['context']['used']=False
        with self.assertRaises(AssertionError):c.verify_detail(bad,self.cid,self.title)
    def exercise_chain(self):
        x=self.chain();baseline=self.ledger();x.ledger=Mock(return_value=baseline);x.sessions=Mock(side_effect=[{'reader':1,'peer':1},{'reader':0,'peer':1},{'reader':1,'peer':1}]);x.other_sessions=Mock(return_value='e'*64);calls=[]
        def call(user,method,path,body=None,**kwargs):
            calls.append((user,method,path,body,kwargs));value=self.detail() if kwargs.get('want',200)==200 else {}
            if kwargs.get('validate'):kwargs['validate'](value)
            return value,{}
        x.call=Mock(side_effect=call);x.login=Mock(side_effect=lambda name:x.cookies.update({name:'memory-only-new'}));return x,baseline,calls
    def test_exact_targets_real_logout_cookie_and_relogin_without_turn(self):
        x,baseline,calls=self.exercise_chain();x.exercise(self.cid,self.title,baseline)
        self.assertEqual(len(calls),9);old=[v for v in calls if v[4].get('want')==401];self.assertEqual({(v[1],v[2].removeprefix('/api/v1/conversations/'+self.cid)) for v in old},{('GET',''),('POST','/messages'),('GET','/events')});self.assertTrue(all(v[0]=='reader' and v[4]['index']==1 for v in old))
        peer=[v for v in calls if v[0]=='peer'];self.assertEqual(len(peer),2);self.assertTrue(all(v[4]['environment']=='test' for v in peer));cross=[v for v in calls if v[4].get('environment')=='staging'];self.assertEqual(len(cross),1);self.assertTrue(cross[0][2].endswith('/messages'));self.assertEqual(cross[0][0],'reader')
        x.login.assert_called_once_with('reader');self.assertEqual(x.marker_case.call_count,8)
    def test_logout_on_API0_and_old_cookie_targets_on_API1(self):
        x,baseline,calls=self.exercise_chain();x.exercise(self.cid,self.title,baseline)
        logout=[v for v in calls if v[2]=='/api/v1/auth/logout'];refused=[v for v in calls if v[4].get('want')==401]
        self.assertEqual(len(logout),1);self.assertEqual(logout[0][4]['index'],0)
        self.assertEqual(len(refused),3);self.assertTrue(all(v[4]['index']==1 for v in refused))
    def test_actual_logout_wire_saves_safe_origin_status_without_cookie(self):
        x=self.chain();fake=Mock(return_value=(204,b'',{}))
        with patch.object(c.base,'transport',fake),patch.dict(c._namespace,{'transport':fake}),contextlib.redirect_stdout(io.StringIO()):
            x.call('reader','POST','/api/v1/auth/logout',index=0,want=204,scenario='fixture')
        row=json.loads((self.work/'conversation-wire-1.json').read_text())
        self.assertEqual(row['API_index'],0);self.assertEqual(row['status'],204);self.assertEqual(row['path'],'/api/v1/auth/logout')
        self.assertNotIn('memory-only',json.dumps(row));self.assertNotIn('Cookie',json.dumps(row))
    def test_logout_cannot_replace_old_cookie_with_anonymous(self):
        x,baseline,_=self.exercise_chain();old=x.call.side_effect
        def call(*args,**kw):
            result=old(*args,**kw)
            if args[2].endswith('/auth/logout'):x.cookies.pop('reader')
            return result
        x.call.side_effect=call
        with self.assertRaises((AssertionError,KeyError)):x.exercise(self.cid,self.title,baseline)
        x.login.assert_not_called()
    def test_logout_wrong_session_change_and_relogin_same_cookie_fail(self):
        x,baseline,_=self.exercise_chain();x.sessions.side_effect=[{'reader':1,'peer':1},{'reader':1,'peer':1}]
        with self.assertRaises(AssertionError):x.exercise(self.cid,self.title,baseline)
        x,baseline,_=self.exercise_chain();x.login.side_effect=None
        with self.assertRaises(AssertionError):x.exercise(self.cid,self.title,baseline)
    def test_logout_other_session_mutation_is_not_exempted(self):
        x,baseline,_=self.exercise_chain();x.other_sessions.side_effect=['e'*64,'f'*64]
        with self.assertRaises(AssertionError):x.exercise(self.cid,self.title,baseline)
        x.login.assert_not_called()
    def test_other_sessions_readonly_digest_never_saves_token_hash(self):
        x=self.chain();x.sql=Mock(return_value='e'*64);self.assertEqual(x.other_sessions(),'e'*64);self.assertTrue(x.sql.call_args.kwargs['read']);self.assertIn('WHERE user_id<>',x.sql.call_args.args[0])
        x.sql.return_value='raw-secret'
        with self.assertRaises(AssertionError):x.other_sessions()
    def created(self):
        before=self.ledger();after=copy.deepcopy(before);after['v3_conversations']=[dict(id=self.cid,owner_id='b'*32,environment_id='test',title=self.title,context_ref='',default_model_id=c.MODEL,status='active',active_reply_id=None,metrics_source_required=False)];after['management_audit']=[dict(actor_id='b'*32,application_id='',action='v3.conversation.create',target=self.cid,outcome='completed',changes={'environment':'test'})];return before,after
    def test_formal_create_exact_one_audit_no_turn_or_unrelated_row(self):
        before,after=self.created();c.verify_created(before,after,self.cid,'b'*32,self.title)
        for table in ('v3_conversation_messages','v3_conversation_replies','v3_conversation_events','business'):
            bad=copy.deepcopy(after);bad[table]=[{}] if table!='business' else {'changed':'hash'}
            with self.assertRaises(AssertionError):c.verify_created(before,bad,self.cid,'b'*32,self.title)
        for field,value in (('outcome','success'),('action','v3.conversation.message'),('target','other'),('application_id','other')):
            bad=copy.deepcopy(after);bad['management_audit'][0][field]=value
            with self.assertRaises(AssertionError):c.verify_created(before,bad,self.cid,'b'*32,self.title)
    def test_ledger_is_readonly_all_public_except_separately_checked_sessions(self):
        x=self.chain();x.owner=Mock();x.sql=Mock(side_effect=['["users","models"]','{}']);x.ledger();query=x.sql.call_args.args[0]
        self.assertIn('json_object_agg',query);self.assertTrue(x.sql.call_args.kwargs['read']);self.assertIn("'sessions'",x.sql.call_args_list[0].args[0]);self.assertIn('FROM public.management_audit t',query)
    def test_contract_scope_disallows_unrelated_or_sse_env_query(self):
        contract=c.Contract(c.ROOT)
        for method,path in c.TARGETS+c.SUPPORT:contract.canonical(method,'/api/v1'+path.replace('{id}',self.cid))
        for method,path in (('GET','/api/v1/home'),('GET','/api/v1/conversations/'+self.cid+'/events?env=staging'),('GET','http://other/api/v1/models')):
            with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
    def test_inventory_no_live_and_all_registration_false(self):
        with patch.object(c,'doctor') as doctor,patch.object(c.Chain,'provision') as provision,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
        value=json.loads(out.getvalue());self.assertEqual(value['command_timeout_seconds'],420);self.assertEqual(value['estimated_HTTP_calls'],14);self.assertFalse(value['registration_evidence']);self.assertEqual(value['normal_added'],0);doctor.assert_not_called();provision.assert_not_called();self.assertEqual(len(value['required_cases']),len(set(value['required_cases'])))
    def test_cleanup_calls_original_budget_once_even_source_drift(self):
        x=self.chain()
        with patch.object(c.base.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):
            sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
        cleanup.assert_called_once_with();x.frozen.assert_not_called()
    def test_explicit_live_required_before_any_admission_or_lifecycle(self):
        with patch.object(c,'admission') as admission,contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit):c.main([])
        admission.assert_not_called()
    def test_fresh_admission_rejects_wrong_flag_manifest_or_parent(self):
        run_id=str(uuid.uuid4());path=c.ROOT/'.local/testlab/cp303-conversation-boundaries-preparation'/run_id/'offline-permit.json'
        permit=dict(status='root-admitted-conversation-object-session',owner=c.base.OWNER,resource_run_id=run_id,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},manifest_sha256='f'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,conversation_boundaries_authorized=True)
        active=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,manifest_sha='f'*64,pid=os.getppid());conn=Mock();conn.execute.return_value=[(json.dumps(active),)];info=Mock(st_mode=0o100600,st_uid=os.getuid())
        with patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(Path,'lstat',return_value=info),patch.object(c.base,'private_json',return_value=permit),patch.object(c.sqlite3,'connect',return_value=conn):
            self.assertEqual(c.admission(path,run_id),permit)
            for key,value in (('conversation_boundaries_authorized',False),('manifest_sha256','0'*64),('source_tree_sha','0'*64)):
                bad=dict(permit,**{key:value})
                with patch.object(c.base,'private_json',return_value=bad),self.assertRaises(AssertionError):c.admission(path,run_id)
            conn.execute.return_value=[(json.dumps(dict(active,pid=os.getppid()+1)),)]
            with self.assertRaises(AssertionError):c.admission(path,run_id)
if __name__=='__main__':unittest.main()
