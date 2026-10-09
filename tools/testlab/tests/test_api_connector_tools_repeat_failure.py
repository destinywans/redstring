"""Offline contract and atomic-ledger counterexamples; no provider or DB."""
import contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_connector_tools_repeat_failure as c
class ConnectorToolsTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.nonce='a'*32;x.calls=0;x.users={};x.cookies={'admin':'cookie-never-store'};x.first_red=Mock();x.frozen=Mock();x.contract=c.Contract(c.ROOT);return x
 def ledger(self,enabled=True):
  return dict(connectors=[dict(id='b'*32,environment_id='test',provider='prometheus',enabled=True,tools=[dict(name='query_metrics',desc='Read-only',enabled=enabled)],updated_at='old',endpoint='http://127.0.0.1:1',credential_ref='')],audit=[dict(id='1',action='connector.created',actor_id='c'*32,target='b'*32,outcome='success',changes={})],all_public=dict(v3_connectors='a'*64,management_audit='a'*64,sessions='a'*64,users='a'*64))
 def changed(self,before,enabled):
  after=copy.deepcopy(before);after['connectors'][0].update(tools=[dict(name='query_metrics',desc='Read-only',enabled=enabled)],updated_at='new');after['audit'].append(dict(id=str(len(before['audit'])+1),action='connector.tools.updated',actor_id='c'*32,target='b'*32,outcome='success',changes=dict(environment='test',tools=after['connectors'][0]['tools'])));after['all_public'].update(v3_connectors='b'*64,management_audit='b'*64);return after
 def test_first_and_identical_repeat_add_audit_not_dedup(self):
  old=self.ledger();first=self.changed(old,False);c.verify_change(old,first,'b'*32,'c'*32,False)
  second=self.changed(first,False);second['all_public']['management_audit']='c'*64;c.verify_change(first,second,'b'*32,'c'*32,False)
  self.assertEqual(first['connectors'][0]['tools'],second['connectors'][0]['tools']);self.assertEqual(len(second['audit']),3)
 def test_changed_credential_endpoint_missing_audit_or_session_fail(self):
  before=self.ledger();after=self.changed(before,False)
  for bad in ('endpoint','actor','old_audit','audit_missing','session','tools'):
   altered=copy.deepcopy(after)
   if bad=='endpoint':altered['connectors'][0]['endpoint']='http://different'
   elif bad=='actor':altered['audit'][-1]['actor_id']='wrong'
   elif bad=='old_audit':altered['audit'][0]['outcome']='failure'
   elif bad=='audit_missing':altered['audit'].pop()
   elif bad=='session':altered['all_public']['sessions']='d'*64
   else:altered['connectors'][0]['tools'][0]['enabled']=True
   with self.assertRaises(AssertionError):c.verify_change(before,altered,'b'*32,'c'*32,False)
 def test_exact_tools_response_excludes_agent_execution(self):
  c.verify_patch(dict(ok=True,tools=[dict(name='query_metrics',desc='Query metrics',enabled=False)]),False)
  for value in (dict(ok=True,tools=[]),dict(ok=True,tools=[dict(name='query_metrics',desc='Read-only',enabled=False)]),dict(ok=True,tools=[dict(name='query_metrics',desc='Query metrics',enabled=False)],executed=True)):
   with self.assertRaises(AssertionError):c.verify_patch(value,False)
 def test_fault_exact_actor_action_target_environment_and_uuid(self):
  name,install,drop=c.fault_sql('a'*32,'b'*32,'c'*32)
  self.assertIn("NEW.action='connector.tools.updated'",install);self.assertIn("NEW.actor_id='"+'b'*32+"'",install);self.assertIn("NEW.target='"+'c'*32+"'",install);self.assertIn("NEW.changes->>'environment'='test'",install);self.assertIn(name,drop)
  with self.assertRaises(AssertionError):c.fault_sql('a'*31,'b'*32,'c'*32)
 def test_only_native_create_patch_login_no_test_activate(self):
  con=c.Contract(c.ROOT)
  for method,path in [('POST','/api/v1/connectors'),('PATCH','/api/v1/connectors/'+'b'*32),('POST','/api/v1/auth/login')]:con.canonical(method,path)
  for method,path in [('POST','/api/v1/connectors/'+'b'*32+'/test'),('POST','/api/v1/connectors/'+'b'*32+'/activate'),('GET','/api/v1/connectors'),('PATCH','/api/v1/connectors/'+'b'*32+'?secret=x')]:
   with self.assertRaises((AssertionError,c.Blocked)):con.canonical(method,path)
 def test_actual_call_success_and503_records_no_credentials_false_registration(self):
  for fault in (False,True):
   x=self.chain();x.want_enabled=False;x.want_fault=fault;value=dict(error=dict(code='audit_unavailable',message='Could not audit connector change')) if fault else dict(ok=True,tools=[dict(name='query_metrics',desc='Query metrics',enabled=False)]);status=503 if fault else 200;fake=Mock(return_value=(status,json.dumps(value).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()):
    x.call('admin','PATCH','/api/v1/connectors/'+'b'*32,c.body(False),want=status,code='audit_unavailable' if fault else None,index=1,scenario='failure' if fault else 'idempotency');self.assertIs(c.ro.transport,fake)
   x.first_red.assert_not_called();row=json.loads((self.work/'interfaces.jsonl').read_text().splitlines()[-1]);self.assertFalse(row['registration_evidence']);self.assertEqual(row['test_name'],'FormalConnectorToolsRepeatFailure');wire=(self.work/'connector-tools-wire-1.json').read_text();self.assertNotIn('cookie-never-store',wire);self.assertFalse(json.loads(wire)['request_body_stored'])
 def test_wrong_audit_error_preserves_first_red_without_storing_reflection(self):
  x=self.chain();x.want_fault=True;x.want_enabled=True;fake=Mock(return_value=(503,json.dumps(dict(error=dict(code='audit_unavailable',message='reflected-secret'))).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('admin','PATCH','/api/v1/connectors/'+'b'*32,c.body(True),want=503,code='audit_unavailable',scenario='failure')
  x.first_red.assert_called_once();self.assertNotIn('reflected-secret',(self.work/'interfaces.jsonl').read_text());self.assertFalse((self.work/'connector-tools-wire-1.json').exists())
 def test_inventory_pure_and_ownedDB_cleanup_single40(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();value=json.loads(out.getvalue());self.assertEqual((value['http_calls'],value['target_PATCHes']),(6,4));self.assertEqual((value['API_seconds'],value['SQL_seconds'],value['outer_seconds'],value['cleanup_seconds'],value['grace_seconds']),(12,8,420,40,45));self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
  x=self.chain()
  with patch.object(c.ro.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x)
if __name__=='__main__':unittest.main()
