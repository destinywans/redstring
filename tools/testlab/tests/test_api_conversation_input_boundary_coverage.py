"""Offline real compiled-call validator/wire and unchanged-store counterexamples."""
import contextlib,copy,hashlib,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_conversation_input_boundary_coverage as c
class InputBoundaryTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name).resolve()
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.users={'reader':'own-reader'};x.cookies={'reader':'memory-cookie-never-store'};x.calls=0;x.first_red=Mock();x.frozen=Mock();x.contract=c.Contract(c.ROOT);x.marker_case=Mock();return x
 def dto(self,code):return {'error':{'code':code,'message':c.MESSAGES[code]}}
 def wire(self,x,code,value=None,status=422,headers=None,body=None):
  fake=Mock(return_value=(status,json.dumps(self.dto(code) if value is None else value).encode(),{'Content-Type':'application/json'} if headers is None else headers))
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
   result=x.call('reader','POST','/api/v1/conversations/'+'b'*32+'/messages',body or {'text':'owned','model':c.MODEL},want=422,code=code,scenario='boundary',index=1);self.assertIs(c.base.transport,fake);self.assertIs(c.h._namespace['transport'],fake)
  return result,fake,[json.loads(v) for v in out.getvalue().splitlines()]
 def test_actual_compiled_call_has_wire422_false_registration_and_no_cookie(self):
  x=self.chain();_,fake,events=self.wire(x,'invalid_text');fake.assert_called_once();raw=(self.work/'input-wire-1.json').read_text();receipt=json.loads(raw);self.assertEqual(receipt['API_index'],1);self.assertEqual(receipt['request_bytes'],len(fake.call_args.args[3]));self.assertEqual(receipt['request_sha256'],hashlib.sha256(fake.call_args.args[3]).hexdigest());self.assertFalse(receipt['request_body_stored']);self.assertNotIn('memory-cookie-never-store',raw);self.assertNotIn('Cookie',raw);self.assertTrue(all(v['interface']['registration_evidence'] is False for v in events));self.assertFalse(json.loads((self.work/'interfaces.jsonl').read_text())['registration_evidence']);x.first_red.assert_not_called()
 def test_each_expected_error_validates_before_pass_not_only_openapi(self):
  for code in c.MESSAGES:
   with self.subTest(code=code):self.wire(self.chain(),code)
 def test_wrong_code_reflection_extra_metadata_and_nonjson_are_firstred(self):
  samples=[(self.dto('invalid_reasoning'),{}),({'error':{'code':'invalid_text','message':'reflected-own-sensitive-input'}},{}),(dict(self.dto('invalid_text'),owner='other'),{}),(self.dto('invalid_text'),{'Content-Type':'text/event-stream'})]
  for value,headers in samples:
   x=self.chain();fake=Mock(return_value=(422,json.dumps(value).encode(),headers or {'Content-Type':'application/json'}))
   with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out,self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/conversations/'+'b'*32+'/messages',{'text':'owned'},want=422,code='invalid_text',scenario='boundary')
   x.first_red.assert_called_once();self.assertTrue(all(json.loads(v)['status']=='failed' for v in out.getvalue().splitlines()));fake.assert_called_once()
 def test_wrong_status401403202500_cannot_be_input_boundary_pass(self):
  for status in (401,403,202,500):
   x=self.chain()
   with self.assertRaises(AssertionError):self.wire(x,'invalid_text',status=status)
   x.first_red.assert_called_once()
 def test_exact_max_body_wire_limit_plus_one_without_stored_request_body(self):
  name,body,code=c.fixtures('a'*32)[-1];payload=json.dumps(body).encode();self.assertEqual(len(payload),(2<<20)+1);self.assertLess(len(body['text']),2<<20);self.assertEqual(code,'invalid_body');x=self.chain();self.wire(x,code,body=body);raw=(self.work/'input-wire-1.json').read_text();self.assertLess(len(raw),2048);self.assertNotIn('Owned max-body sentinel',raw);self.assertEqual(json.loads(raw)['request_bytes'],(2<<20)+1)
 def test_missing_attachment_is_complete_paste_reference_not_fake_link(self):
  samples=c.fixtures('a'*32);self.assertEqual(tuple(v[0] for v in samples),c.CASES);item=samples[4][1]['attachments'][0];self.assertEqual(set(item),{'id','kind','name','size','mime'});self.assertEqual(item['kind'],'paste');self.assertEqual(item['mime'],'text/plain');self.assertEqual(len(item['id']),32);self.assertNotIn('url',item);self.assertEqual(len(samples[1][1]['reasoning']),129);self.assertEqual(samples[3][1]['reasoning'],'high')
 def test_model_control_requires_advertised_exact_model_and_no_reasoning(self):
  good={'items':[{'id':c.MODEL,'reasoning':{'kind':'none','levels':[]}}]};c.verify_models(good)
  for bad in ({'items':[]},{'items':[{'id':'other','reasoning':{'kind':'none','levels':[]}}]},{'items':[{'id':c.MODEL,'reasoning':{'kind':'levels','levels':[{'id':'high'}]}}]}):
   with self.assertRaises(AssertionError):c.verify_models(bad)
 def test_refusal_exercise_full_ledger_and_final_dto_same(self):
  x=self.chain();baseline={t:[] for t in c.TABLES};baseline['business']={'uploads':'emptyhash','other':'stable'};x.ledger=Mock(return_value=baseline);x.sql=Mock(return_value='f');detail={'owned':'detail'};x.call=Mock(return_value=(detail,{}));x.exercise('b'*32,'title',baseline,detail);self.assertEqual(x.call.call_count,8);self.assertTrue(x.sql.call_args.kwargs['read']);self.assertTrue(all(v.kwargs.get('want')==422 for v in x.call.call_args_list[:-1]));self.assertEqual(x.marker_case.call_count,7)
 def test_any_business_audit_source_or_timestamp_residue_stops_case(self):
  for key in (*c.TABLES,'business'):
   x=self.chain();baseline={t:[] for t in c.TABLES};baseline['business']={'unchanged':'hash'};bad=copy.deepcopy(baseline);bad[key]=[{'changed':'timestamp'}] if key!='business' else {'changed':'hash'};x.ledger=Mock(side_effect=[baseline,bad]);x.sql=Mock(return_value='f');x.call=Mock()
   with self.assertRaises(AssertionError):x.exercise('b'*32,'title',baseline,{})
   self.assertEqual(x.call.call_count,1);x.marker_case.assert_not_called()
 def test_existing_missing_upload_fixture_fails_before_any_http(self):
  x=self.chain();x.sql=Mock(return_value='t');x.call=Mock()
  with self.assertRaises(AssertionError):x.exercise('b'*32,'title',{}, {})
  x.call.assert_not_called()
 def test_safe_response_write_failure_restores_both_transports_without_retry(self):
  x=self.chain();fake=Mock(return_value=(422,json.dumps(self.dto('invalid_text')).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),patch.object(c.base,'_private',side_effect=OSError('offline evidence failure')),contextlib.redirect_stdout(io.StringIO()):
   with self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/conversations/'+'b'*32+'/messages',{'text':'owned'},want=422,code='invalid_text',scenario='boundary')
   self.assertIs(c.base.transport,fake);self.assertIs(c.h._namespace['transport'],fake)
  fake.assert_called_once();x.first_red.assert_called_once()
 def test_private_scope_excludes_old_logout_sse_and_metrics(self):
  contract=c.Contract(c.ROOT)
  for method,path in (('POST','/api/v1/auth/logout'),('GET','/api/v1/conversations/'+'b'*32+'/events'),('POST','/api/v1/metrics/ask')):
   with self.assertRaises((AssertionError,c.Blocked)):contract.canonical(method,path)
 def test_inventory_is_pure_registration_false_and_cleanup_budget_original(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  value=json.loads(out.getvalue());doctor.assert_not_called();self.assertFalse(value['registration_evidence']);self.assertEqual(value['normal_added'],0);self.assertEqual(value['estimated_HTTP_calls'],12);self.assertEqual(len(value['required_cases']),len(set(value['required_cases'])))
  x=self.chain()
  with patch.object(c.base.Chain,'cleanup') as cleanup,patch.object(c.h.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x)
if __name__=='__main__':unittest.main()
