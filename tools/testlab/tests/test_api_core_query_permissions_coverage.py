"""Offline exact rejection callback, wire header seam and full-ledger counterexamples."""
import contextlib,copy,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_core_query_permissions_coverage as c
RID='INC-'+'b'*32
class CoreQueryTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.source_id='owned-source';x.source_token='memory-source-token';x.users={'reader':'owner','denied':'own-denied','hidden':'own-staging'};x.cookies={'reader':'memory-cookie-private','denied':'memory-cookie-private','hidden':'memory-cookie-private'};x.calls=0;x.frozen=Mock();x.first_red=Mock();x.marker_case=Mock();x.contract=c.Contract(c.ROOT);return x
 def actual_call(self,x,status,code,msg=None,value=None,path='/api/v1/inbox?filter=mine&tab=mentions',method='GET',**kw):
  v=dict(error=dict(code=code,message=c.ERRORS[code] if msg is None else msg)) if value is None else value;fake=Mock(return_value=(status,json.dumps(v).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
   result=x.call('reader',method,path,{} if method=='POST' else None,want=status,code=code,index=1,**kw);self.assertIs(c.ro.transport,fake);self.assertIs(c._namespace['transport'],fake)
  return result,fake,[json.loads(v) for v in out.getvalue().splitlines()]
 def test_actual_compiled_query_callback_wire_false_and_no_cookie(self):
  x=self.chain();_,fake,rows=self.actual_call(x,422,'invalid_filter');fake.assert_called_once();x.first_red.assert_not_called();self.assertTrue(all(r['interface']['registration_evidence'] is False for r in rows));raw=(self.work/'core-wire-1.json').read_text();self.assertNotIn('memory-cookie',raw);self.assertNotIn('Cookie',raw);self.assertEqual(json.loads(raw)['path'],'/api/v1/inbox?filter=mine&tab=mentions')
 def test_all_three_source_defined_422_not_success_or_generic_bad_request(self):
  for path,code in (('/api/v1/inbox?filter=mine&tab=mentions','invalid_filter'),('/api/v1/alert-groups?severity=P5','invalid_severity'),('/api/v1/incidents?status=active&status=resolved','invalid_status')):self.actual_call(self.chain(),422,code,path=path)
  for status in (200,400,403):
   x=self.chain();fake=Mock(return_value=(status,json.dumps(dict(error=dict(code='invalid_filter',message=c.ERRORS['invalid_filter']))).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.ro,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('reader','GET','/api/v1/inbox?filter=mine&tab=mentions',want=422,code='invalid_filter')
   x.first_red.assert_called_once()
 def test_schema_valid_error_wrong_message_or_metadata_leak_is_firstred(self):
  for value in (dict(error=dict(code='invalid_filter',message='reflected other object')),dict(error=dict(code='invalid_filter',message=c.ERRORS['invalid_filter']),owner='leaked')):
   x=self.chain()
   with self.assertRaises(AssertionError):self.actual_call(x,422,'invalid_filter',value=value)
   x.first_red.assert_called_once()
 def test_case404_and_create_runbook403_are_distinct_source_contracts(self):
  self.actual_call(self.chain(),404,'not_found',path='/api/v1/incidents/'+RID);self.actual_call(self.chain(),403,'forbidden',path='/api/v1/runbooks',method='POST')
  x=self.chain();fake=Mock(return_value=(403,json.dumps(dict(error=dict(code='forbidden',message='Insufficient permission'))).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('denied','GET','/api/v1/incidents/'+RID,want=404,code='not_found')
  x.first_red.assert_called_once()
 def test_actual_case_key_and_webhook_bearer_seam_never_saved(self):
  x=self.chain();key='incident-'+x.nonce;_,fake,rows=self.actual_call(x,201,None,value=dict(id=RID),method='POST',path='/api/v1/incidents',request_key=key);self.assertEqual(fake.call_args.args[4]['Idempotency-Key'],key);self.assertFalse(rows[0]['interface']['registration_evidence'])
  fake=Mock(return_value=(202,json.dumps(dict(accepted=1,notification_delivery='not_configured')).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),patch.dict(c._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()):x.call(None,'POST','/api/v1/webhooks/alertmanager/owned-source',{'alerts':[]},want=202)
  self.assertEqual(fake.call_args.args[4]['Authorization'],'Bearer memory-source-token');self.assertNotIn('memory-source-token',''.join(p.read_text() for p in self.work.iterdir()))
 def test_refusal_rejects_every_full_ledger_and_other_public_change(self):
  baseline={k:[] for k in ('cases','links','alerts','audit','status_events','timeline')};baseline.update(investigations=0,events=0,other_public={'runbooks':'same','source':'same','sessions':'same'})
  for key in baseline:
   x=self.chain();bad=copy.deepcopy(baseline);bad[key]=['unexpected changed bytes/audit'];x.ledger=Mock(side_effect=[baseline,bad]);x.call=Mock()
   with self.assertRaises(AssertionError):x.reject(baseline,'reader','GET','/api/v1/incidents?status=active&status=resolved',422,'invalid_status')
   x.call.assert_called_once()
 def test_case_owner_control_binds_real_case_not_occurrence_or_wrong_env(self):
  body=dict(title='owned case',severity='P2');v=dict(id=RID,title='owned case',severity='P2',status='active',record_kind='case',environment_key='test',investigation_id=None);c.verify_case_detail(v,RID,body)
  for key,value in (('record_kind','alert'),('environment_key','staging'),('id','other'),('investigation_id','fabricated')):
   bad=dict(v,**{key:value})
   with self.assertRaises(AssertionError):c.verify_case_detail(bad,RID,body)
 def test_actual_case_fixture_fulfills_inherited_exact_assertion_inputs(self):
  body=c.case_payload('owned_app','owned_alert','confirmed Case')
  self.assertIsInstance(body.get('impact'),str);self.assertTrue(body['impact']);self.assertIsInstance(body.get('commander'),str);self.assertTrue(body['commander']);self.assertFalse(body['notify']);self.assertFalse(body['open_canvas'])
 def test_only_fixed_query_scope_and_immutable_readiness_names(self):
  contract=c.Contract(c.ROOT)
  for path in ('/api/v1/incidents?status=all','/api/v1/inbox?filter=mine','/api/v1/alert-groups?severity=P4'):
   with self.assertRaises(AssertionError):contract.canonical('GET',path)
  x=self.chain()
  def ready(obj):
   for i in (0,1):c.ro.emit('readonly.api_ready.'+str(i),'passed',dict(status=200))
  with patch.object(c.ro.Chain,'start',ready),contextlib.redirect_stdout(io.StringIO()) as out:
   x.phase=0;x.start();x.phase=1;x.start()
  names=[json.loads(v)['name'] for v in out.getvalue().splitlines()];self.assertEqual(len(names),len(set(names)));self.assertEqual(names,['core_queries.api_ready.'+stage+'.'+str(i) for stage in ('setup','source') for i in (0,1)])
 def test_inventory_no_actual_and_original_single_cleanup(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  v=json.loads(out.getvalue());doctor.assert_not_called();self.assertFalse(v['registration_evidence']);self.assertEqual(v['estimated_HTTP_calls'],15);self.assertEqual(v['normal_added'],0);self.assertEqual(v['cleanup_seconds'],40)
  x=self.chain()
  with patch.object(c.ro.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x)
if __name__=='__main__':unittest.main()
