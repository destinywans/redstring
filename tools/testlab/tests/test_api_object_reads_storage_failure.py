"""Offline bounded object-read seams; no native resources or HTTP."""
import contextlib,copy,io,json,re,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_object_reads_storage_failure as o
class ObjectReadTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=o.Chain.__new__(o.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.database='aiops_testlab_readonly_'+'a'*32;x.db_user='readonly_'+'a'*32;x.frozen=Mock();x.owner=Mock();x.marker_case=Mock();x.active_table=None;x.acl_before=None;x.acl_fault=False;x.paths=['/api/v1/canvases/CV-'+'a'*32,'/api/v1/investigations/INV-'+'a'*32+'/canvas','/api/v1/connectors/'+'a'*32];x.expected={};return x
 def acl(self,x,table):return dict(database=x.database,table=table,owner=x.db_user,select=True,columns=0,role_oid=9,entries=[dict(grantor=x.db_user,grantee=9,privilege=p,grantable=False) for p in ('INSERT','SELECT','UPDATE')])
 def revoked(self,b):return dict(b,select=False,entries=[e for e in b['entries'] if e['privilege']!='SELECT'])
 def fixture(self,x):
  x.nonce='a'*32;x.marker='owned-fixture';x.users={'reader':'reader-id'};x.payloads={};x.rows={}
  root=o._read_ns['root_node']('owned-fixture plan');rows={'plan':{'owner_id':'reader-id','environment_id':'test','services':['app-id'],'nodes':[root],'edges':[],'artifacts':[]},'branches':[{'id':'b'*32,'parent_id':None,'model_id':'','revision':0}],'context_revisions':[{'created_at':'2026-10-06T00:00:00Z','messages':[]}]}
  x.call=Mock(side_effect=[({},{}),({'id':'CV-'+'a'*32,'href':'/canvas/CV-'+'a'*32},{}),({'id':'INV-'+'a'*32,'href':'/canvas/INV-'+'a'*32},{}),({'id':'c'*32},{})]);x.sql=Mock(side_effect=[json.dumps({'app':'app-id','env':'env-id'}),json.dumps(rows),json.dumps({'enabled':False,'status':'unverified','environment':'test'})]);return rows
 def test_contract_three_gets_and_only_six_fixture_operations_no_role_or_probe(self):
  con=o.Contract(o.ROOT)
  for method,path in o.TARGETS+o.SUPPORT:con.canonical(method,'/api/v1'+path.replace('{id}','owned'))
  for method,path in [('GET','/api/v1/roles/own'),('POST','/api/v1/connectors/own/test'),('GET','/api/v1/canvases/own?include_threads=false'),('GET','/api/v1/canvases')]:
   with self.assertRaises((AssertionError,o.Blocked)):con.canonical(method,path)
 def test_formal_fixture_has_four_http_writes_no_success_sql_insert(self):
  x=self.chain();self.fixture(x);x.inputs()
  self.assertEqual([(c.args[0],c.args[1],c.args[2],c.kwargs['want']) for c in x.call.call_args_list],[('admin','POST','/api/v1/services',201),('reader','POST','/api/v1/canvases',201),('reader','POST','/api/v1/canvases',201),('admin','POST','/api/v1/connectors',201)])
  self.assertTrue(all(c.kwargs=={'read':True} and c.args[0].startswith('SELECT ') for c in x.sql.call_args_list));self.assertEqual(x.call.call_args_list[3].args[3],{'type':'prometheus','url':'http://127.0.0.1:9','auth':'none','webhook':True,'tools':['query_metrics']});self.assertEqual(len(x.paths),3)
 def test_fixture_rejects_foreign_owner_inferred_context_and_activated_connector(self):
  for mode in ('owner','context','activation'):
   x=self.chain();rows=self.fixture(x)
   if mode=='owner':rows['plan']['owner_id']='foreign'
   elif mode=='context':rows['context_revisions'][0]['messages']=[{'text':'inference'}]
   x.sql=Mock(side_effect=[json.dumps({'app':'app-id','env':'env-id'}),json.dumps(rows),json.dumps({'enabled':mode=='activation','status':'unverified','environment':'test'})])
   with self.assertRaises(AssertionError):x.inputs()
 def test_original_pure_canvas_readbacks_have_native_root_and_empty_threads(self):
  x=self.chain();self.fixture(x);x.inputs()
  plan={'id':x.plan,'title':'owned-fixture plan','kind':'plan','status':'draft','participants':['owned-fixture reader'],'note':'Owned object read plan','nodes':[o._read_ns['root_node']('owned-fixture plan')],'edges':[],'artifacts':[],'threads':{'root':{'context':'主线','turns':[]}},'env':'test'};x.verify_read(0,plan)
  version=[{'v':'v0','text':'Context revision 0','at':'2026-10-06T00:00:00Z'}]
  graph={'investigation':{'id':x.inv,'title':'owned-fixture investigation','status':'active','env':'test','participants':['owned-fixture reader']},'loop':[],'tray':[],'nodes':[{'id':'b'*32,'kind':'root','title':'主调查','subtitle':'Main investigation','status':'active','x':16,'y':40,'w':260,'collapsed':False,'layout_version':0,'context':{'version':'v0','tokens':None,'messages':0,'excludes':[]},'versions':version}]};x.verify_read(1,graph)
  for i,v in [(0,dict(plan,threads={})),(1,dict(graph,nodes=[]))]:
   with self.assertRaises(AssertionError):x.verify_read(i,v)
 def test_connector_original_assertion_not_probe_available_or_fabricated_services(self):
  x=self.chain();x.connector='c'*32;value={'id':'c'*32,'brand':'prometheus','scope':'test','address':'http://127.0.0.1:9','status':'unverified','services':[],'tools':[],'queries':[],'tags':[]};x.verify_read(2,value)
  for v in (dict(value,status='available'),dict(value,services=['fake']),dict(value,address='http://elsewhere.invalid')):
   with self.assertRaises(AssertionError):x.verify_read(2,v)
 def test_three_exact_mapper_literals_not_shared_auth_or_node503(self):
  vals=[{'error':{'code':'canvas_unavailable','message':'Canvas unavailable'}},{'error':{'code':'investigation_canvas_unavailable','message':'Investigation canvas is unavailable'}},{'error':{'code':'storage_unavailable','message':'Connector storage is unavailable'}}]
  for i,v in enumerate(vals):
   o.verify_error(i,v,{'Content-Type':'application/json'})
   for bad in ({'error':{'code':'environment_unavailable','message':'Environment authorization unavailable'}},{'error':{'code':'investigation_node_unavailable','message':'Investigation node unavailable'}},vals[(i+1)%3]):
    with self.assertRaises(AssertionError):o.verify_error(i,bad,{'Content-Type':'application/json'})
 def test_actual_compiled_acl_roundtrip_valid_sql_keyword_and_only_business_tables(self):
  x=self.chain();state={t:self.acl(x,t) for t in o.TABLES};captured=[]
  def pg(sql,database,user):
   m=re.fullmatch(r'(REVOKE|GRANT) SELECT ON TABLE public\."(v3_canvases|branches|v3_connectors)" (FROM|TO) "readonly_([a]{32})";',sql);self.assertIsNotNone(m,'compiled ACL method emitted invalid or auth-table SQL');verb,table,direction,_=m.groups();self.assertEqual(direction,'FROM' if verb=='REVOKE' else 'TO');self.assertEqual((database,user),(x.database,'testlab'));captured.append(sql);state[table]=self.revoked(self.acl(x,table)) if verb=='REVOKE' else self.acl(x,table)
  x.pg=Mock(side_effect=pg);x.acl=Mock(side_effect=lambda:copy.deepcopy(state[x.active_table]))
  for table in o.TABLES:
   x.active_table=table;x.revoke_table();self.assertFalse(state[table]['select']);x.restore_table();self.assertEqual(state[table],self.acl(x,table))
  self.assertEqual(len(captured),6);self.assertFalse(x.acl_fault)
 def test_oid_bigint_guard_and_revoke_ineffectiveness_or_foreign_restore_cannot_pass(self):
  x=self.chain();x.active_table='branches';b=self.acl(x,'branches');x.pg=Mock(return_value=json.dumps(b));self.assertEqual(x.acl(),b);self.assertIn('SELECT oid::bigint FROM pg_roles',x.pg.call_args.args[0]);self.assertIn("'grantee',a.grantee::bigint",x.pg.call_args.args[0])
  bad=dict(b,role_oid='9');x.pg=Mock(return_value=json.dumps(bad))
  with self.assertRaises(AssertionError):x.acl()
  x.acl=Mock(side_effect=[b,b]);x.pg=Mock()
  with self.assertRaises(AssertionError):x.revoke_table()
  self.assertTrue(x.acl_fault);foreign=dict(self.revoked(b),owner='foreign');x.acl=Mock(return_value=foreign);x.pg.reset_mock()
  with self.assertRaises(AssertionError):x.restore_table()
  x.pg.assert_not_called()
 def test_each_object_four_calls_and_per_table_fault_restored_before_next(self):
  x=self.chain();x.verify_read=Mock();x.call=Mock(return_value=({'native':'same'},{}));baseline={'sessions':'a'*64,'management_audit':'b'*64};x.ledger=Mock(return_value=baseline);x.revoke_table=Mock();x.restore_table=Mock()
  for i in range(3):x.exercise(i,baseline)
  self.assertEqual(x.call.call_count,12);self.assertEqual(x.ledger.call_count,12);self.assertEqual(x.revoke_table.call_count,3);self.assertEqual(x.restore_table.call_count,3);self.assertEqual([c.kwargs['want'] for c in x.call.call_args_list],[200,200,503,200]*3);self.assertEqual([c.kwargs['index'] for c in x.call.call_args_list],[0,1,0,1]*3);self.assertIsNone(x.active_table)
 def test_first_wrong_mapper_stops_restores_once_and_preserves_primary_type_only_note(self):
  x=self.chain();primary=AssertionError('private fault');x.call=Mock(side_effect=[({},{}),({},{}),primary]);x.ledger=Mock(return_value={});x.revoke_table=Mock();x.restore_table=Mock(side_effect=RuntimeError('private restore'))
  with self.assertRaises(AssertionError) as caught:x.exercise(1,{})
  self.assertIs(caught.exception,primary);self.assertEqual(x.call.call_count,3);x.restore_table.assert_called_once();self.assertEqual(json.loads((self.work/'restore-secondary-1.json').read_text()),{'primary_type':'AssertionError','restore_type':'RuntimeError'})
 def test_execute_preserves_first_failure_and_cleans_up_when_restore_diagnostics_fail(self):
  # Real exercise/execute/first_red path; only HTTP, SQL and cleanup resources
  # are replaced. The primary failure occurs after HTTP, before any first_red.
  for ledger_changed,restore_failed,note_failed in ((True,True,True),(True,True,False),(True,False,False),(False,True,False)):
   with self.subTest(ledger_changed=ledger_changed,restore_failed=restore_failed,note_failed=note_failed):
    x=self.chain();x.work=self.work/str((ledger_changed,restore_failed,note_failed));x.work.mkdir();x.root=x.work/'root'
    baseline={'sessions':'a'*64};changed={'sessions':'b'*64};x.call=Mock(return_value=({},{}));x.ledger=Mock(side_effect=[baseline,baseline,changed if ledger_changed else baseline]);x.revoke_table=Mock()
    restore_error=RuntimeError('private restore');x.restore_table=Mock(side_effect=restore_error if restore_failed else None);x.cleanup=Mock();x.run=lambda:x.exercise(1,baseline)
    original_private=o.ro._private;note=x.work/'restore-secondary-1.json'
    def write(path,value):
     if path==note and note_failed:raise OSError('diagnostic disk full')
     return original_private(path,value)
    with patch.object(o.ro,'_private',side_effect=write),contextlib.redirect_stdout(io.StringIO()) as output:
     with self.assertRaises(AssertionError if ledger_changed else RuntimeError) as caught:o.ro.execute(x)
    expected=dict(stage='business_contract',error_type='AssertionError' if ledger_changed else 'RuntimeError',error='failed object read changed full public ledger' if ledger_changed else 'private restore')
    self.assertEqual(str(caught.exception),expected['error'])
    if not ledger_changed:self.assertIs(caught.exception,restore_error)
    self.assertEqual(json.loads((x.work/'first-failure.json').read_text()),expected)
    self.assertEqual(json.loads((x.root/'.local/testlab/first-failures'/('api-readonly-'+x.run_id+'.json')).read_text()),expected)
    events=[json.loads(line) for line in output.getvalue().splitlines()];self.assertEqual([(v['name'],v['status'],v['actual']) for v in events],[('readonly.business','failed',expected)])
    x.cleanup.assert_called_once_with();x.restore_table.assert_called_once_with();self.assertEqual(x.call.call_count,3)
    self.assertEqual(note.exists(),ledger_changed and restore_failed and not note_failed)
    if note.exists():self.assertEqual(json.loads(note.read_text()),dict(primary_type='AssertionError',restore_type='RuntimeError'))
 def test_full_public_digest_includes_session_audit_and_no_write_exclusions(self):
  x=self.chain();tables=['sessions','management_audit','ops_incidents',*o.TABLES];ledger={t:'a'*64 for t in tables};x.pg=Mock(side_effect=[json.dumps(tables),json.dumps(ledger)]);self.assertEqual(x.ledger(),ledger)
  self.assertTrue(all(c.args[0].startswith('BEGIN READ ONLY; ') and c.args[1:]==(x.database,'testlab') for c in x.pg.call_args_list));self.assertTrue(all(t in x.pg.call_args_list[1].args[0] for t in tables))
 def test_session_or_audit_change_stops_before_fault(self):
  for key in ('sessions','management_audit'):
   x=self.chain();baseline={'sessions':'a'*64,'management_audit':'b'*64};bad=dict(baseline);bad[key]='c'*64;x.call=Mock(return_value=({},{}));x.ledger=Mock(return_value=bad);x.revoke_table=Mock()
   with self.assertRaises(AssertionError):x.exercise(0,baseline)
   self.assertEqual(x.call.call_count,1);x.revoke_table.assert_not_called()
 def test_actual_http_call_saved_before_wrong_mapper_check_and_no_cookie(self):
  x=self.chain();x.calls=0;x.users={'reader':'own-reader'};x.cookies={'reader':'secret-cookie'};x.first_red=Mock();x.contract=o.Contract(o.ROOT);error={'error':{'code':'storage_unavailable','message':'Connector storage is unavailable'}};fake=Mock(return_value=(503,json.dumps(error).encode(),{'Content-Type':'application/json','Set-Cookie':'secret-header'}))
  with patch.object(o.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()):x.call('reader','GET',x.paths[2],want=503,code='storage_unavailable')
  raw=(self.work/'object-read-wire-1.json').read_text();self.assertNotIn('secret',raw);self.assertEqual(json.loads((self.work/'interfaces.jsonl').read_text())['test_name'],'FormalObjectReadsStorageFailure');self.assertEqual(json.loads(raw)['response'],error)
  fake=Mock(return_value=(503,json.dumps({'error':{'code':'environment_unavailable','message':'Environment authorization unavailable'}}).encode(),{'Content-Type':'application/json'}))
  with patch.object(o.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()):
   with self.assertRaises(AssertionError):x.call('reader','GET',x.paths[2],want=503,code='storage_unavailable')
  self.assertTrue((self.work/'object-read-wire-2.json').exists());x.first_red.assert_called_once()
 def test_inventory_exact18_and_sole_cleanup40_no_second_restore(self):
  with patch.object(o,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(o.main(['--inventory']),0)
  doctor.assert_not_called();v=json.loads(out.getvalue());self.assertEqual((v['http_calls'],v['target_HTTP'],v['fixture_HTTP']),(18,12,6));self.assertEqual((v['SQL_seconds'],v['API_seconds'],v['outer_seconds'],v['cleanup_seconds'],v['grace_seconds']),(8,12,420,40,45));self.assertTrue(v['role_fault_unverified']);self.assertEqual(len(o.REQUIRED_CASES),len(set(o.REQUIRED_CASES)))
  x=self.chain();x.acl_fault=True;x.restore_table=Mock()
  with patch.object(o.ro.Chain,'cleanup') as clean,patch.object(o.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  clean.assert_called_once_with(x);x.restore_table.assert_not_called()
if __name__=='__main__':unittest.main()
