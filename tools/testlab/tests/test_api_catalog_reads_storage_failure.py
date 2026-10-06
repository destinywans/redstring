"""Two-catalog offline bounds only; no native resource acquisition."""
import contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_catalog_reads_storage_failure as c
class CatalogReadTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.frozen=Mock();x.owner=Mock();x.calls=0;x.cookies={'admin':'never-store-cookie'};x.users={};x.first_red=Mock();x.contract=c.Contract(c.ROOT);x.sid=str(uuid.uuid4());x.app='b'*32;x.marker_case=Mock();x.acl_fault=False;x.acl_before={'test':'owned'};return x
 def mcp(self):return dict(items=[],complete_v3_dto=False,kpis=[dict(value='0') for _ in range(3)],unavailable_fields=['unbound_legacy_registrations','connection_health','agent_authorized_apps','call_metrics','pending_change'])
 def test_two_exact_tables_main_knowledge_never_legacy_documents(self):
  self.assertEqual(c.FAULTS['/mcp-servers'][0],'ai_extensions');self.assertEqual(c.FAULTS['/knowledge-sources'][0],'v3_knowledge_source_registrations')
  con=c.Contract(c.ROOT)
  for method,path in c.TARGETS+c.SUPPORT:con.canonical(method,'/api/v1'+path)
  for method,path in (('GET','/api/v1/knowledge-sources/observed-documents'),('POST','/api/v1/knowledge-sources/registrations/id/sync'),('GET','/api/v1/mcp-servers/id'),('GET','/api/v1/agent-tools')):
   with self.assertRaises((AssertionError,c.Blocked)):con.canonical(method,path)
 def test_empty_MCP_honest_counts_not_nonempty_discovery_or_complete_claim(self):
  c.verify_read('/mcp-servers',self.mcp(),'source','app')
  for bad in (dict(self.mcp(),items=[{'id':'fake'}]),dict(self.mcp(),complete_v3_dto=True),dict(self.mcp(),kpis=[{'value':'1'}]*3),dict(self.mcp(),unavailable_fields=[])):
   with self.assertRaises(AssertionError):c.verify_read('/mcp-servers',bad,'source','app')
 def test_registered_read_reuses_exact_upload_evidence_validator(self):
  value={'owned':'dto'}
  with patch.object(c.k,'evidence') as validator:c.verify_read('/knowledge-sources',value,'source','app')
  validator.assert_called_once_with(value,['source'],'app')
 def test_actual_original_compiled_call_preserves_specific_error_newscope_and_cookie_private(self):
  for path,(_,code,message) in c.FAULTS.items():
   x=self.chain();c.core.ERROR={'error':{'code':code,'message':message}}
   fake=Mock(return_value=(503,json.dumps(c.core.ERROR).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:
    x.call('admin','GET','/api/v1'+path,want=503,code=code,index=1)
    self.assertIs(c.ro.transport,fake)
   fake.assert_called_once();x.first_red.assert_not_called();row=json.loads((self.work/'interfaces.jsonl').read_text().splitlines()[-1]);self.assertEqual(row['test_name'],'FormalCatalogReadsStorageFailure');self.assertFalse(row['registration_evidence']);self.assertEqual(row['scenario'],'failure');self.assertTrue(all(json.loads(v)['interface']['test_name']=='FormalCatalogReadsStorageFailure' for v in out.getvalue().splitlines()));self.assertNotIn('never-store-cookie',(self.work/'core-read-wire-1.json').read_text())
 def test_wrong_table_error_message_cannot_pass(self):
  c.core.ERROR={'error':{'code':'mcp_unavailable','message':'MCP registrations are unavailable'}}
  for value in ({'items':[]},{'error':{'code':'mcp_unavailable','message':'MCP observation is inconsistent'}},{'error':{'code':'knowledge_sources_unavailable','message':'Knowledge source evidence is unavailable'}}):
   with self.assertRaises(AssertionError):c.verify_error(value,{'Content-Type':'application/json'})
 def test_each_fault_route_has_same_four_requests_and_exact_table_with_fullledger(self):
  for path,(table,code,message) in c.FAULTS.items():
   x=self.chain();x.call=Mock(return_value=({},{}));baseline={'sessions':'a'*64,'management_audit':'a'*64,'all':'a'*64};x.ledger=Mock(return_value=baseline);x.files=Mock(return_value={});x.revoke=Mock();x.restore=Mock()
   with patch.object(c.ro,'private_json',return_value={'revoked':'owned'}):x.exercise(0,path,baseline)
   self.assertEqual(c.core.TABLE,table);self.assertEqual(c.core.ERROR,{'error':{'code':code,'message':message}});self.assertEqual(x.call.call_count,4);self.assertEqual([v.kwargs['want'] for v in x.call.call_args_list],[200,200,503,200]);self.assertEqual([v.kwargs['index'] for v in x.call.call_args_list],[0,1,0,1]);self.assertTrue(all(v.args[0]=='admin' for v in x.call.call_args_list));self.assertEqual(x.ledger.call_count,4);x.restore.assert_called_once()
 def test_any_session_or_tmp_residue_stops_before_fault(self):
  for kind in ('ledger','TMPDIR'):
   x=self.chain();x.call=Mock(return_value=({},{}));x.ledger=Mock(return_value={'changed':'x'} if kind=='ledger' else {});x.files=Mock(return_value={'tmp':'hash'} if kind=='TMPDIR' else {});x.revoke=Mock()
   with self.assertRaises(AssertionError):x.exercise(0,'/mcp-servers',{})
   self.assertEqual(x.call.call_count,1);x.revoke.assert_not_called()
 def test_offline_inventory_no_resources_and_cleanup_original40_only(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();value=json.loads(out.getvalue());self.assertEqual(value['http_calls'],12);self.assertEqual(value['cleanup_seconds'],40);self.assertEqual(value['grace_seconds'],45);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
  x=self.chain();x.restore=Mock()
  with patch.object(c.ro.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x);x.restore.assert_not_called()
if __name__=='__main__':unittest.main()
