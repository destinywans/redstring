"""Small offline fault guards; no native resources or HTTP."""
import contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_core_reads_storage_failure as c

class CoreReadTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.database='aiops_testlab_readonly_'+'a'*32;x.db_user='readonly_'+'a'*32;x.frozen=Mock();x.owner=Mock();x.marker_case=Mock();x.acl_fault=False;x.acl_before=None;return x
 def acl(self):
  x=self.chain();return dict(database=x.database,table=c.TABLE,owner=x.db_user,select=True,columns=0,role_oid=9,entries=[dict(grantor=x.db_user,grantee=9,privilege=p,grantable=False) for p in ('INSERT','SELECT','UPDATE')])
 def revoked(self,before):return dict(before,select=False,entries=[e for e in before['entries'] if e['privilege']!='SELECT'])
 def test_only_three_reads_and_formal_fixture_contract(self):
  con=c.Contract(c.ROOT)
  for method,path in c.TARGETS:self.assertEqual(con.canonical(method,'/api/v1'+path)[1],'main')
  for method,path in (('POST','/api/v1/incidents'),('GET','/api/v1/inbox?filter=mine'),('GET','/api/v1/models')):
   with self.assertRaises((AssertionError,c.Blocked)):con.canonical(method,path)
 def test_exact_storage_error_not403_empty200_or_pagination(self):
  c.verify_error(c.ERROR,{'Content-Type':'application/json'})
  for value,hs in (({'items':[]},{'Content-Type':'application/json'}),({'error':{'code':'pagination_required','message':'Event storage unavailable'}},{'Content-Type':'application/json'}),(c.ERROR,{'Content-Type':'text/plain'})):
   with self.assertRaises(AssertionError):c.verify_error(value,hs)
 def test_baseline_requires_real_ingress_identity_not_empty_count(self):
  examples=(('/inbox',{'items':[{'alert_id':'alert','id':'alert:alert'}],'counts':{'all':1},'partial':True,'window':'today_and_yesterday'}),('/alert-groups',{'items':[{'service':'app','source_id':'source','fingerprint':'source','count':1}],'counts':{'all':1},'partial':True,'aggregation':'local_persisted_incidents'}),('/incidents',{'items':[{'id':'alert'}],'counts':{'all':1}}))
  for path,value in examples:
   c.verify_read(path,value,'alert','app','source')
   bad=copy.deepcopy(value);bad['items']=[]
   with self.assertRaises(AssertionError):c.verify_read(path,bad,'alert','app','source')
 def test_revoke_and_restore_exact_effective_acl_no_other_mutation(self):
  x=self.chain();before=self.acl();after=self.revoked(before);x.acl=Mock(side_effect=[before,after,after,before]);x.pg=Mock(return_value='')
  x.revoke();self.assertTrue(x.acl_fault);x.restore();self.assertFalse(x.acl_fault)
  self.assertEqual(x.pg.call_count,2)
  self.assertEqual(x.pg.call_args_list[0].args[0],'REVOKE SELECT ON TABLE public."ops_incidents" FROM "'+x.db_user+'";')
  self.assertEqual(x.pg.call_args_list[1].args[0],'GRANT SELECT ON TABLE public."ops_incidents" TO "'+x.db_user+'";')
  self.assertTrue(all(v.args[1:]==(x.database,'testlab') for v in x.pg.call_args_list))
 def test_acl_numeric_identity_keeps_strict_raw_oid_rejection(self):
  x=self.chain();expected=self.acl();x.pg=Mock(return_value=json.dumps(expected))
  self.assertEqual(x.acl(),expected)
  query=x.pg.call_args.args[0]
  self.assertIn('SELECT oid::bigint FROM pg_roles',query)
  self.assertIn("'grantee',a.grantee::bigint",query)
  raw=copy.deepcopy(expected);raw['role_oid']='9';x.pg=Mock(return_value=json.dumps(raw))
  with self.assertRaises(AssertionError):x.acl()
 def test_nonselect_acl_drift_or_ineffective_revoke_cannot_pass(self):
  for change in ('ineffective','extra'):
   x=self.chain();before=self.acl();after=self.revoked(before)
   if change=='ineffective':after=before
   else:after['entries'].append(dict(grantor=x.db_user,grantee=10,privilege='SELECT',grantable=False))
   x.acl=Mock(side_effect=[before,after]);x.pg=Mock()
   with self.assertRaises(AssertionError):x.revoke()
   self.assertTrue(x.acl_fault)
 def test_grant_option_or_ambiguous_select_refused_before_revoke(self):
  for field in ('grantoption','duplicate'):
   x=self.chain();before=self.acl()
   if field=='grantoption':before['entries'][1]['grantable']=True
   else:before['entries'].append(dict(before['entries'][1]))
   x.acl=Mock(return_value=before);x.pg=Mock()
   with self.assertRaises(AssertionError):x.revoke()
   x.pg.assert_not_called()
 def test_foreign_acl_restore_fails_closed_without_blind_grant(self):
  x=self.chain();x.acl_before=self.acl();x.acl_fault=True;bad=self.revoked(x.acl_before);bad['owner']='other';x.acl=Mock(return_value=bad);x.pg=Mock()
  with self.assertRaises(AssertionError):x.restore()
  x.pg.assert_not_called();self.assertTrue(x.acl_fault)
 def test_each_route_has_four_calls_crossinstance_and_full_ledger_at_each_phase(self):
  x=self.chain();x.alert='alert';x.app='app';x.source_id='source';x.call=Mock(return_value=({},{}));baseline={'sessions':'a'*64,'management_audit':'b'*64,'ops_incidents':'c'*64};x.ledger=Mock(return_value=baseline);x.revoke=Mock();x.restore=Mock()
  x.exercise(0,'/inbox',baseline)
  self.assertEqual(x.call.call_count,4);self.assertEqual(x.ledger.call_count,4)
  self.assertEqual([v.kwargs['want'] for v in x.call.call_args_list],[200,200,503,200])
  self.assertEqual([v.kwargs['index'] for v in x.call.call_args_list],[0,1,0,1])
  self.assertEqual(x.call.call_args_list[2].kwargs['code'],'events_unavailable');x.restore.assert_called_once();self.assertEqual(x.marker_case.call_count,5)
 def test_primary_failed503_preserved_even_if_restore_also_fails(self):
  x=self.chain();x.alert='alert';x.app='app';x.source_id='source';primary=AssertionError('primary-only');x.call=Mock(side_effect=[({},{}),({},{}),primary]);x.ledger=Mock(return_value={});x.revoke=Mock();x.restore=Mock(side_effect=RuntimeError('restore'))
  with self.assertRaises(AssertionError) as caught:x.exercise(0,'/inbox',{})
  self.assertIs(caught.exception,primary);self.assertEqual(x.call.call_count,3);self.assertTrue((self.work/'restore-secondary.json').exists())
 def test_session_or_audit_digest_changed_stops_without_exclusions(self):
  for key in ('sessions','management_audit','ops_incidents'):
   x=self.chain();x.alert='alert';x.app='app';x.source_id='source';baseline={k:'a'*64 for k in ('sessions','management_audit','ops_incidents')};after=dict(baseline);after[key]='b'*64;x.call=Mock(return_value=({},{}));x.ledger=Mock(return_value=after);x.revoke=Mock()
   with self.assertRaises(AssertionError):x.exercise(0,'/inbox',baseline)
   self.assertEqual(x.call.call_count,1);x.revoke.assert_not_called()
 def test_actual_compiled_http_call_keeps_new_scope_and_never_cookie_in_evidence(self):
  x=self.chain();x.nonce='a'*32;x.calls=0;x.users={'reader':'own-reader'};x.cookies={'reader':'secret-cookie-never-store'};x.first_red=Mock();x.contract=c.Contract(c.ROOT)
  fake=Mock(return_value=(503,json.dumps(c.ERROR).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:
   x.call('reader','GET','/api/v1/inbox',want=503,code='events_unavailable',index=1)
   self.assertIs(c.ro.transport,fake)
  fake.assert_called_once();x.first_red.assert_not_called()
  raw=(self.work/'interfaces.jsonl').read_text();v=json.loads(raw)
  self.assertEqual(v['test_name'],'FormalCoreReadsStorageFailure');self.assertEqual(v['scenario'],'failure');self.assertFalse(v['registration_evidence']);self.assertNotIn('secret-cookie-never-store',raw)
  self.assertTrue(all(json.loads(line)['interface']['test_name']=='FormalCoreReadsStorageFailure' for line in out.getvalue().splitlines()))
  self.assertEqual(json.loads((self.work/'core-read-wire-1.json').read_text())['response'],c.ERROR)

 def test_inventory_pure_and_cleanup_sole40_no_second_restore_clock(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();value=json.loads(out.getvalue());self.assertEqual(value['target_HTTP'],12);self.assertEqual(value['http_calls'],16);self.assertEqual(value['cleanup_seconds'],40);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
  x=self.chain();x.restore=Mock();x.acl_fault=True
  with patch.object(c.ro.Chain,'cleanup') as clean,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  clean.assert_called_once_with(x);x.restore.assert_not_called()
if __name__=='__main__':unittest.main()
