"""Offline scoped target/ACL/resource first-red counterexamples, never live IO."""
import contextlib,copy,io,json,os,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_conversation_metrics_acl_coverage as c
class MetricsACLTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.nonce='a'*32;x.users={'reader':'own-reader'};x.roles={'reader':'own-role'};x.apps={'test':{'app':'own-app'}};x.database='own-db';x.db_user='own-role';x.owner_tag='own-uuid-tag';x.frozen=Mock();x.marker_case=Mock();x.calls=0;x.cookies={'reader':'only-memory-cookie'};x.contract=c.gen.Contract(c.ROOT);x.first_red=Mock();x.servers=[];x.prom_cid=None;x.prom_attempted=False;x.prom_name='own-prom';x.grant_intent=False;x.saved_grant=dict(role_id='own-role',module='services',action='view',scope_kind='application',scope_id='own-app',created_at='2026-10-06T00:00:00+00:00');x.original_grants=[x.saved_grant];return x
 def result(self,rc=1,name='own-prom',stderr=None,stdout=''):return Mock(returncode=rc,stdout=stdout,stderr='error: no such object: '+name if stderr is None else stderr)
 def test_exact_absence_rejects_daemon_wrong_name_stdout_and_rc(self):
  c.absence(self.result(),'own-prom')
  for r in (self.result(rc=2),self.result(name='other'),self.result(stderr='permission denied'),self.result(stderr='Cannot connect to Docker daemon'),self.result(stdout='unknown')):
   with self.assertRaises(AssertionError):c.absence(r,'own-prom')
 def test_actual_call_wire_false_cookie_free_and_exact403(self):
  x=self.chain();fake=Mock(return_value=(403,b'{"error":{"code":"forbidden","message":"Forbidden"}}',{'Content-Type':'application/json'}))
  with c.gen.context(),patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:x.call('reader','POST','/api/v1/conversations/'+'a'*32+'/messages',{'text':'valid','model':'openai:readonly-synthetic'},want=403,code='forbidden',scenario='authorization',index=1);self.assertIs(c.ro.transport,fake)
  raw=(self.work/'metrics-acl-wire-1.json').read_text();self.assertNotIn('only-memory-cookie',raw);self.assertNotIn('Cookie',raw);self.assertTrue(all(json.loads(s)['interface']['registration_evidence'] is False for s in out.getvalue().splitlines()));fake.assert_called_once()
 def test_wrong401404409202_are_first_failures_not_acl_pass(self):
  for status in (401,404,409,202):
   x=self.chain();fake=Mock(return_value=(status,b'{"error":{"code":"forbidden","message":"Forbidden"}}',{'Content-Type':'application/json'}))
   with c.gen.context(),patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/conversations/'+'a'*32+'/messages',{'text':'valid','model':'openai:readonly-synthetic'},want=403,code='forbidden',scenario='authorization')
   x.first_red.assert_called_once();fake.assert_called_once()
 def test_target_complete_business_and_counts_zero_delta(self):
  x=self.chain();ledger={'rows':[],'source':'same','audit':[]};x.ledger=Mock(return_value=ledger);x.counters=Mock(return_value={'source':1,'model':1});x.revoke=Mock();x.restore=Mock();x.call=Mock();x.detail=Mock(return_value={'id':'native'});x.target('native',{'id':'native'},ledger);self.assertEqual(x.call.call_args.kwargs['want'],403);x.restore.assert_called_once();x.detail.assert_called_once();self.assertEqual(x.call.call_count,1)
 def test_target_residue_or_extra_provider_blocks_before_recovery(self):
  for kind in ('business','source','model'):
   x=self.chain();x.revoke=Mock();x.restore=Mock();x.call=Mock();x.detail=Mock();x.ledger=Mock(return_value={'rows':[]});x.counters=Mock(return_value={'source':1,'model':1})
   if kind=='business':x.ledger.return_value={'rows':[{'residue':'owned'}]}
   else:x.counters.side_effect=[{'source':1,'model':1},{'source':2 if kind=='source' else 1,'model':2 if kind=='model' else 1}]
   with self.assertRaises(AssertionError):x.target('native',{}, {'rows':[]})
   x.restore.assert_not_called();x.detail.assert_not_called()
 def test_revoke_intent_precedes_exact_delete_and_no_other_grants(self):
  x=self.chain();x.sql=Mock();x.role_grants=Mock(return_value=[]);x.revoke();sql=x.sql.call_args.args[0];self.assertTrue((self.work/'grant-restore-intent.json').exists());self.assertTrue(x.grant_intent)
  for k in ('role_id','module','action','scope_kind','scope_id'):self.assertIn(k+'=',sql)
  self.assertNotIn('v3_conversations',sql)
 def test_unknown_grant_state_never_overwrites_or_restores_wrong_timestamp(self):
  x=self.chain();x.grant_intent=True;x.role_grants=Mock(return_value=[dict(x.saved_grant,created_at='wrong')]);x.pg=Mock()
  with self.assertRaises(AssertionError):x.restore(123)
  x.pg.assert_not_called();self.assertTrue(x.grant_intent)
 def test_restore_exact_original_row_timestamp_and_same_deadline(self):
  x=self.chain();x.grant_intent=True;x.role_grants=Mock(side_effect=[[],x.original_grants]);x.pg=Mock();x.restore(123);self.assertFalse(x.grant_intent);self.assertEqual(x.pg.call_args.kwargs['deadline'],123);self.assertIn(x.saved_grant['created_at'],x.pg.call_args.args[0]);self.assertTrue(all(v.kwargs['deadline']==123 if 'deadline' in v.kwargs else v.args==(123,) for v in x.role_grants.call_args_list))
 def test_role_grant_tag_mismatch_prevents_acl_read_or_mutation(self):
  x=self.chain();x.pg=Mock(return_value='{"db":"other","role":"other"}')
  with self.assertRaises(AssertionError):x.role_grants(123)
  x.pg.assert_called_once();self.assertEqual(x.pg.call_args.kwargs['deadline'],123)
 def test_sole_authority_and_unrelated_entry_grants_are_required(self):
  for bad in ('extra','wrong-app','missing-change','system-role'):
   x=self.chain();rows=[dict(x.saved_grant)]+[dict(role_id='own-role',module=m,action=a,scope_kind='environment',scope_id='test',created_at='own') for m,a in (('environment','view'),('conversation','view'),('conversation','change'),('connectors','view'),('models','view'))];assigned=['own-role']
   if bad=='extra':rows.append(dict(x.saved_grant,scope_kind='global',scope_id='*'))
   if bad=='wrong-app':rows[0]['scope_id']='other'
   if bad=='missing-change':rows=[v for v in rows if v['action']!='change']
   if bad=='system-role':assigned.append('admin')
   x.sql=Mock(side_effect=['',json.dumps(assigned)]);x.role_grants=Mock(return_value=rows)
   with self.assertRaises(AssertionError):x.acl_setup()
   x.marker_case.assert_not_called()
 def test_cleanup_restore_failure_still_exact_pg_cleanup_same40(self):
  x=self.chain();x.restore=Mock(side_effect=AssertionError('offline restore failed'));x.servers=[Mock()]
  with patch.object(c,'bounded_pg_cleanup') as pg,patch.object(c.time,'monotonic',return_value=10),patch.object(c.socket,'socket') as sock:
   sock.return_value.__enter__.return_value.connect_ex.return_value=1
   with self.assertRaises(AssertionError):x.cleanup()
  self.assertEqual(x.cleanup_deadline,50);x.restore.assert_called_once_with(14);pg.assert_called_once_with(x);x.servers[0].close.assert_called_once_with(50)
 def test_known_cid_retained_before_prom_guard_or_evidence_failure(self):
  x=self.chain();x.prom_args=[];x.tmpfs='owned';x.docker=Mock();x.inspect_prom=Mock();cid='b'*64
  def docker(args,**kw):
   if args[0]=='inspect':return self.result(name=x.prom_name)
   (self.work/'prom.cid').write_text(cid);return Mock(returncode=0,stdout=cid,stderr='')
  x.docker.side_effect=docker
  with patch.object(c.gen,'guard_prom',side_effect=AssertionError('offline inspect guard failed')):
   with self.assertRaises(AssertionError):x.start_prom()
  self.assertEqual(x.prom_cid,cid);self.assertTrue(x.prom_attempted)
 def test_unknown_create_without_cidfile_never_adopts_name(self):
  x=self.chain();x.prom_args=[];x.tmpfs='owned';x.docker=Mock(side_effect=[self.result(name=x.prom_name),Mock(returncode=1,stdout='unknown',stderr='')]);x.inspect_prom=Mock()
  with self.assertRaises(AssertionError):x.start_prom()
  self.assertIsNone(x.prom_cid);x.inspect_prom.assert_not_called();self.assertTrue(x.prom_attempted)
 def test_inventory_is_pure_has_fresh_unique_generation_markers(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  value=json.loads(out.getvalue());self.assertEqual(len(value['required_cases']),len(set(value['required_cases'])));self.assertEqual(value['normal_added'],0);doctor.assert_not_called()
 def test_server_close_bounded_join_and_remaining_worker_blocks(self):
  server=c.Server.__new__(c.Server);server.closed=False;server.lock=c.threading.Lock();server.server=Mock();server.thread=Mock();server.thread.is_alive.return_value=False;worker=Mock();worker.is_alive.return_value=True;connection=Mock();server.workers=[worker];server.sockets=[connection];stopper=Mock();stopper.is_alive.return_value=False
  with patch.object(c.threading,'Thread',return_value=stopper),patch.object(c.time,'monotonic',return_value=10),self.assertRaises(AssertionError):server.close(50)
  self.assertFalse(server.closed);stopper.join.assert_called_once_with(4);worker.join.assert_called_once_with(4);connection.close.assert_called_once();server.server.server_close.assert_called_once()
 def test_cleanup_unknown_acquisition_never_adopts_live_named_container(self):
  x=self.chain();x.prom_attempted=True;x.restore=Mock();x.docker=Mock(return_value=self.result(rc=0,stdout='[{"Id":"unowned"}]'))
  with patch.object(c,'bounded_pg_cleanup') as pg,patch.object(c.socket,'socket') as sock:
   sock.return_value.__enter__.return_value.connect_ex.return_value=1
   with self.assertRaises(AssertionError):x.cleanup()
  self.assertEqual(x.docker.call_args.args[0],['inspect','own-prom']);self.assertEqual(x.docker.call_count,1);pg.assert_called_once_with(x)
 def test_private_normal_fixture_source_still_exact_original_protocol(self):
  x=self.chain();x.model_token='synthetic-private';x.model_answer='Original fixed protocol answer';x.apps['test']['expressions']={'request_rate':'owned_private_expression'};handlers={}
  def server(port,handler):handlers[port]=handler;return Mock()
  with patch.dict(c.Fixture.__init__.__globals__,Server=server):fixture=c.Fixture(x)
  handler=handlers[c.FIXTURE_PORT].__new__(handlers[c.FIXTURE_PORT]);handler.path='/wrong';handler.headers={};handler.send_error=Mock();handler.do_GET();self.assertEqual(fixture.source_entries,1);handler.send_error.assert_called_once_with(502)
  handler.send_error.reset_mock();handler.do_POST();self.assertEqual(fixture.model_entries,1);handler.send_error.assert_called_once_with(400)
  body=json.dumps(dict(model='readonly-synthetic',stream=True,messages=[{'role':'user','content':'Observed metric'}])).encode();handler.path='/v1/chat/completions';handler.headers={'Content-Length':str(len(body)),'Authorization':'Bearer '+x.model_token};handler.rfile=io.BytesIO(body);handler.wfile=io.BytesIO();handler.send_response=Mock();handler.send_header=Mock();handler.end_headers=Mock();handler.do_POST()
  handler.send_response.assert_called_once_with(200);self.assertEqual(fixture.model_entries,2);self.assertIn(x.model_answer.encode(),handler.wfile.getvalue());self.assertTrue(handler.wfile.getvalue().endswith(b'data: [DONE]\n\n'));self.assertFalse(c.evidence({'path':'/api/v1/conversations/{id}/messages'})['registration_evidence']);self.assertEqual(c.evidence({'path':'/api/v1/conversations/{id}/messages'})['evidence_mode'],'protocol_mock')
if __name__=='__main__':unittest.main()
