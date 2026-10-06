"""Offline settings read guards; mocks never count as native HTTP evidence."""
import contextlib,copy,io,json,re,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_settings_reads_storage_failure as s

class SettingsReadTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=s.Chain.__new__(s.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.database='aiops_testlab_readonly_'+'a'*32;x.db_user='readonly_'+'a'*32;x.frozen=Mock();x.owner=Mock();x.marker_case=Mock();x.acl_fault=False;x.acl_before=None;x.active_table=None;x.acl_states={};x.expected_reads={};return x
 def acl(self,x,table):return dict(database=x.database,table=table,owner=x.db_user,select=True,columns=0,role_oid=9,entries=[dict(grantor=x.db_user,grantee=9,privilege=p,grantable=False) for p in ('INSERT','SELECT','UPDATE')])
 def revoked(self,b):return dict(b,select=False,entries=[e for e in b['entries'] if e['privilege']!='SELECT'])
 def test_exact_four_gets_and_only_formal_put_login_fixtures(self):
  con=s.Contract(s.ROOT)
  for method,path in s.TARGETS+s.SUPPORT:con.canonical(method,'/api/v1'+path)
  for method,path in [('GET','/api/v1/me'),('POST','/api/v1/models/probe'),('GET','/api/v1/me/watched-services?x=1'),('POST','/api/v1/services')]:
   with self.assertRaises((AssertionError,s.Blocked)):con.canonical(method,path)
 def test_source_literal_error_payloads_and_nonstorage_refusals_rejected(self):
  fixtures=[('/models',{'error':{'code':'settings_unavailable','message':'Settings storage unavailable'}}),('/me/watched-services',{'error':{'code':'watched_services_unavailable','message':'Watched services or policy are unavailable'}})]
  for path,value in fixtures:
   s.verify_error(path,value,{'Content-Type':'application/json; charset=utf-8'})
   for bad in ({'items':[]},{'error':{'code':'unauthorized','message':'Login required'}},{'error':{'code':'authorization_unavailable','message':'Service policy is unavailable'}}):
    with self.assertRaises(AssertionError):s.verify_error(path,bad,{'Content-Type':'application/json'})
   with self.assertRaises(AssertionError):s.verify_error(path,value,{'Content-Type':'text/plain'})
 def test_put_established_values_and_honest_empty_watch_not_defaults_or_fake_available(self):
  s.verify_read('/me/ai-preferences',s.PREFERENCES);s.verify_read('/me/notification-settings',s.NOTIFICATIONS);s.verify_read('/me/watched-services',{'items':[],'complete_v3_dto':True})
  for path,value in [('/me/ai-preferences',dict(s.PREFERENCES,budget_warn_tokens=8000)),('/me/notification-settings',dict(s.NOTIFICATIONS,digest='weekly')),('/me/watched-services',{'items':[{'id':'fake'}],'complete_v3_dto':True})]:
   with self.assertRaises(AssertionError):s.verify_read(path,value)
 def test_catalog_requires_actual_local_protocol_and_persisted_default(self):
  value={'items':[{'id':'openai:readonly-synthetic','api':'chat_completions','available':True,'reasoning':{'kind':'none','levels':[]}}],'default':{'model':'openai:readonly-synthetic'}};s.verify_read('/models',value)
  for field,bad in [('api','responses'),('id','another'),('available',False)]:
   v=copy.deepcopy(value);v['items'][0][field]=bad
   with self.assertRaises(AssertionError):s.verify_read('/models',v)
 def test_acl_bigint_identity_and_exact_two_table_statements(self):
  x=self.chain()
  for table in s.TABLES:
   x.active_table=table;before=self.acl(x,table);x.pg=Mock(return_value=json.dumps(before));self.assertEqual(x.acl(),before)
   statement=x.pg.call_args.args[0];self.assertIn('SELECT oid::bigint FROM pg_roles',statement);self.assertIn("'grantee',a.grantee::bigint",statement);self.assertIn("c.relname='"+table+"'",statement)
   before['role_oid']='9';x.pg=Mock(return_value=json.dumps(before))
   with self.assertRaises(AssertionError):x.acl()
 def test_both_acl_preimages_restored_exactly_without_shared_session_revoke(self):
  x=self.chain();seq=[]
  for table in s.TABLES:
   b=self.acl(x,table);seq.extend([b,self.revoked(b)])
  for table in reversed(s.TABLES):
   b=self.acl(x,table);seq.extend([self.revoked(b),b])
  x.acl=Mock(side_effect=seq);x.pg=Mock()
  for table in s.TABLES:x.table_operation(table,'revoke')
  self.assertTrue(x.acl_fault);self.assertEqual(len(x.acl_states),2);x.restore_all();self.assertFalse(x.acl_fault)
  statements=[c.args[0] for c in x.pg.call_args_list];self.assertEqual(len(statements),4)
  self.assertTrue(all(('sessions' not in v and 'users' not in v and any('public."'+t+'"' in v for t in s.TABLES)) for v in statements))
  self.assertEqual(set(p.name for p in self.work.glob('acl-intent-*.json')),{'acl-intent-'+t+'.json' for t in s.TABLES})
 def test_actual_compiled_revoke_restore_sql_keeps_table_keyword_and_roundtrip(self):
  x=self.chain();state={t:self.acl(x,t) for t in s.TABLES};captured=[]
  def pg(statement,database,user):
   # Independent narrow SQL seam: invalid keyword cannot mutate ACL state.
   match=re.fullmatch(r'(REVOKE|GRANT) SELECT ON TABLE public\."(v3_user_settings|v3_user_watched_services)" (FROM|TO) "readonly_([a]{32})";',statement)
   self.assertIsNotNone(match,'compiled fault method emitted invalid SQL')
   verb,table,direction,_=match.groups();self.assertEqual(direction,'FROM' if verb=='REVOKE' else 'TO');self.assertEqual((database,user),(x.database,'testlab'))
   captured.append(statement);state[table]=self.revoked(self.acl(x,table)) if verb=='REVOKE' else self.acl(x,table)
  x.pg=Mock(side_effect=pg);x.acl=Mock(side_effect=lambda:copy.deepcopy(state[x.active_table]))
  for table in s.TABLES:x.table_operation(table,'revoke')
  self.assertTrue(all(not v['select'] for v in state.values()));x.restore_all()
  self.assertEqual(state,{t:self.acl(x,t) for t in s.TABLES});self.assertEqual(len(captured),4);self.assertFalse(x.acl_fault)
 def test_unlisted_table_ambiguous_acl_and_foreign_restore_fail_closed(self):
  x=self.chain();x.pg=Mock()
  with self.assertRaises(AssertionError):x.table_operation('sessions','revoke')
  x.pg.assert_not_called()
  b=self.acl(x,s.TABLES[0]);b['entries'][1]['grantable']=True;x.acl=Mock(return_value=b)
  with self.assertRaises(AssertionError):x.table_operation(s.TABLES[0],'revoke')
  x.pg.assert_not_called()
  b=self.acl(x,s.TABLES[0]);x.acl_states={s.TABLES[0]:(b,True)};bad=self.revoked(b);bad['owner']='other';x.acl=Mock(return_value=bad)
  with self.assertRaises(AssertionError):x.restore_all()
  x.pg.assert_not_called();self.assertTrue(x.acl_fault)
 def test_failed_second_revoke_still_restores_first_attempt_no_retry(self):
  x=self.chain();b=self.acl(x,s.TABLES[0]);x.acl=Mock(side_effect=[b,self.revoked(b),RuntimeError('second-read'),self.revoked(b),b]);x.pg=Mock();x.phase_reads=Mock()
  with self.assertRaises(RuntimeError):x.exercise({})
  self.assertEqual(x.pg.call_count,2);self.assertEqual(x.phase_reads.call_count,2);self.assertFalse(x.acl_fault)
 def test_one_two_table_fault_batch_all_four_routes_each_phase(self):
  x=self.chain();x.calls=0;baseline={'sessions':'a'*64,'management_audit':'b'*64,**{t:'c'*64 for t in s.TABLES}};x.ledger=Mock(return_value=baseline)
  def call(actor,method,path,**kw):
   x.calls+=1;p=path.removeprefix('/api/v1');return ({'p':p} if kw['want']==200 else s.error_for(p)),{}
  x.call=Mock(side_effect=call);x.table_operation=Mock();x.restore_all=Mock();x.exercise(baseline)
  self.assertEqual(x.calls,16);self.assertEqual([c.kwargs['want'] for c in x.call.call_args_list],[200]*8+[503]*4+[200]*4);self.assertEqual([c.kwargs['index'] for c in x.call.call_args_list],[0]*4+[1]*4+[0]*4+[1]*4)
  self.assertEqual(x.table_operation.call_count,2);x.restore_all.assert_called_once();self.assertEqual(x.ledger.call_count,16)
 def test_first_route_fault_stops_no_recovery_preserves_primary_and_restore_note(self):
  x=self.chain();primary=AssertionError('private-primary');x.phase_reads=Mock(side_effect=[None,None,primary]);x.table_operation=Mock();x.restore_all=Mock(side_effect=RuntimeError('private-restore'))
  with self.assertRaises(AssertionError) as caught:x.exercise({})
  self.assertIs(caught.exception,primary);self.assertEqual(x.phase_reads.call_count,3)
  note=json.loads((self.work/'restore-secondary.json').read_text());self.assertEqual(note,{'primary_type':'AssertionError','restore_type':'RuntimeError'});self.assertNotIn('private-',json.dumps(note))
 def test_sessions_or_audit_changes_fail_first_read_without_fault_install(self):
  for key in ('sessions','management_audit'):
   x=self.chain();baseline={'sessions':'a'*64,'management_audit':'b'*64};changed=dict(baseline);changed[key]='c'*64;x.call=Mock(return_value=({},{}));x.ledger=Mock(return_value=changed);x.table_operation=Mock()
   with self.assertRaises(AssertionError):x.exercise(baseline)
   self.assertEqual(x.call.call_count,1);x.table_operation.assert_not_called()
 def test_full_public_ledger_keeps_sessions_and_both_settings_tables_admin_readonly(self):
  x=self.chain();tables=['sessions','management_audit','ops_incidents',*s.TABLES];ledger={k:'a'*64 for k in tables};x.pg=Mock(side_effect=[json.dumps(tables),json.dumps(ledger)])
  self.assertEqual(x.ledger(),ledger)
  for c in x.pg.call_args_list:self.assertTrue(c.args[0].startswith('BEGIN READ ONLY; '));self.assertEqual(c.args[1:],(x.database,'testlab'))
  self.assertTrue(all(t in x.pg.call_args_list[1].args[0] for t in tables))
 def test_actual_call_namespace_response_whitelist_and_cookie_never_stored(self):
  x=self.chain();x.calls=0;x.users={'reader':'own-reader'};x.cookies={'reader':'secret-cookie'};x.first_red=Mock();x.contract=s.Contract(s.ROOT)
  value={'error':{'code':'settings_unavailable','message':'Settings storage unavailable'}};fake=Mock(return_value=(503,json.dumps(value).encode(),{'Content-Type':'application/json','Set-Cookie':'secret-header'}))
  with patch.object(s.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()):x.call('reader','GET','/api/v1/models',want=503,code='settings_unavailable')
  self.assertEqual(json.loads((self.work/'interfaces.jsonl').read_text())['test_name'],'FormalSettingsReadsStorageFailure')
  raw=(self.work/'settings-read-wire-1.json').read_text();self.assertNotIn('secret',raw);self.assertNotIn('Set-Cookie',raw);self.assertEqual(json.loads(raw)['response'],value)
 def test_inventory_19_http_and_cleanup_one40_no_second_grant(self):
  with patch.object(s,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(s.main(['--inventory']),0)
  doctor.assert_not_called();v=json.loads(out.getvalue());self.assertEqual((v['http_calls'],v['target_HTTP'],v['fixture_HTTP']),(19,16,3));self.assertEqual((v['SQL_seconds'],v['API_seconds'],v['outer_seconds'],v['cleanup_seconds'],v['grace_seconds']),(8,12,420,40,45));self.assertEqual(len(s.REQUIRED_CASES),len(set(s.REQUIRED_CASES)))
  x=self.chain();x.acl_fault=True;x.restore_all=Mock()
  with patch.object(s.ro.Chain,'cleanup') as clean,patch.object(s.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  clean.assert_called_once_with(x);x.restore_all.assert_not_called()
if __name__=='__main__':unittest.main()
