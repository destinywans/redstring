"""Bounded offline input/repeat/evidence counterexamples, no native execution."""
import contextlib,copy,hashlib,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_upload_input_repeat_boundaries as c
class UploadInputTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.nonce='a'*32;x.calls=0;x.users={};x.cookies={'admin':'cookie-never-store'};x.first_red=Mock();x.frozen=Mock();x.contract=c.Contract(c.ROOT);return x
 def test_eight_exact_fixture_variants_small_UTF8_byte_size(self):
  body,samples=c.samples('a'*32,'b'*32)
  self.assertEqual(tuple(s[0] for s in samples),c.CASES);self.assertEqual(len(samples),8)
  self.assertEqual(body['size'],len(body['text'].encode()));self.assertGreater(body['size'],len(body['text']))
  self.assertTrue(all(len(json.dumps(s[2]).encode())<4096 for s in samples))
  self.assertEqual(samples[0][2]['kind'],'file');self.assertNotIn('text',samples[0][2]);self.assertEqual(samples[1][2]['size'],body['size']+1)
  self.assertEqual([s[3] for s in samples],['invalid_upload']*3+['invalid_body']*2+['invalid_rule_preview']*3)
  self.assertEqual(samples[5][2]['op'],'==');self.assertEqual(samples[6][2]['for_minutes'],61);self.assertEqual(samples[7][2]['range'],'8d')
 def test_contract_rejects_paste_download_and_extra_routes(self):
  con=c.Contract(c.ROOT)
  for method,path in c.TARGETS+c.SUPPORT:con.canonical(method,'/api/v1'+path)
  for method,path in (('GET','/api/v1/uploads/'+'a'*32+'/content'),('POST','/api/v1/conversations/'+'a'*32+'/messages'),('POST','/api/v1/alert-rules/preview?range=1h')):
   with self.assertRaises((AssertionError,c.Blocked)):con.canonical(method,path)
 def test_exact_paste_response_omits_body_URL_and_random_id_not_required_stable(self):
  body,_=c.samples('a'*32)
  for uid in ('b'*32,'c'*32):c.verify_paste(dict(id=uid,kind='paste',name=body['name'],size=body['size'],mime='text/plain'),body)
  for field,value in (('url','/api/v1/uploads/id/content'),('text',body['text']),('owner_id','other')):
   dto=dict(id='b'*32,kind='paste',name=body['name'],size=body['size'],mime='text/plain',**{field:value})
   with self.assertRaises(AssertionError):c.verify_paste(dto,body)
 def rows(self,body,ids):
  before=dict(uploads=[],revocations=[],audit=[],all_public={'v3_uploads':'a'*64,'management_audit':'a'*64,'sessions':'a'*64,'users':'a'*64})
  after=copy.deepcopy(before)
  after['uploads']=[dict(id=uid,owner_id='actor',environment_id='test',kind='paste',name=body['name'],size=body['size'],mime='text/plain',url='',bytes=body['size'],sha256=hashlib.sha256(body['text'].encode()).hexdigest()) for uid in sorted(ids)]
  after['audit']=[dict(actor_id='actor',target=uid,outcome='success',kind='paste',size=body['size'],mime='text/plain',environment_id='test') for uid in ids]
  after['all_public'].update(v3_uploads='b'*64,management_audit='b'*64);return before,after
 def test_two_distinct_ids_exact_bytes_and_two_transactional_audits(self):
  body,_=c.samples('a'*32)
  for ids in (['b'*32],['b'*32,'c'*32]):
   before,after=self.rows(body,ids);c.verify_rows(before,after,ids,'actor',body)
  before,after=self.rows(body,['b'*32,'c'*32])
  for change in ('duplicate_id','byte_hash','audit_actor','audit_missing','session_changed'):
   bad=copy.deepcopy(after);ids=['b'*32,'c'*32]
   if change=='duplicate_id':ids[1]=ids[0]
   elif change=='byte_hash':bad['uploads'][1]['sha256']='f'*64
   elif change=='audit_actor':bad['audit'][1]['actor_id']='other'
   elif change=='audit_missing':bad['audit'].pop()
   else:bad['all_public']['sessions']='f'*64
   with self.assertRaises(AssertionError):c.verify_rows(before,bad,ids,'actor',body)
 def test_real_compiled_422_call_all_codes_safe_false_registration(self):
  _,samples=c.samples('a'*32,'b'*32)
  for _,path,body,code,message in samples:
   x=self.chain();x.expected_error={'error':{'code':code,'message':message}}
   fake=Mock(return_value=(422,json.dumps(x.expected_error).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:
    x.call('admin','POST','/api/v1'+path,body,want=422,code=code,index=1)
    self.assertIs(c.ro.transport,fake)
   row=json.loads((self.work/'interfaces.jsonl').read_text().splitlines()[-1]);self.assertFalse(row['registration_evidence']);self.assertEqual(row['test_name'],'FormalUploadInputRepeatBoundaries');self.assertEqual(row['scenario'],'boundary');x.first_red.assert_not_called()
   wire=(self.work/'upload-input-wire-1.json').read_text();self.assertNotIn('cookie-never-store',wire);
   if len(body.get('text',''))>2:self.assertNotIn(body['text'],wire)
   self.assertFalse(json.loads(wire)['request_body_stored'])
   self.assertTrue(all(json.loads(s)['interface']['test_name']=='FormalUploadInputRepeatBoundaries' for s in out.getvalue().splitlines()))
 def test_wrong_refusal_error_not_passed_or_sensitive_raw_stored(self):
  x=self.chain();x.expected_error=c.ERROR;fake=Mock(return_value=(422,json.dumps({'error':{'code':'invalid_upload','message':'reflected-secret'}}).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('admin','POST','/api/v1/uploads',{'kind':'file'},want=422,code='invalid_upload')
  x.first_red.assert_called_once();self.assertFalse((self.work/'upload-input-wire-1.json').exists());self.assertNotIn('reflected-secret',(self.work/'interfaces.jsonl').read_text())
 def test_new201_repeat_scenario_never_gains_normal_registration(self):
  x=self.chain();body,_=c.samples(x.nonce);dto=dict(id='b'*32,kind='paste',name=body['name'],size=body['size'],mime='text/plain');fake=Mock(return_value=(201,json.dumps(dto).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()):x.call('admin','POST','/api/v1/uploads',body,want=201,scenario='idempotency',validate=lambda v:c.verify_paste(v,body))
  row=json.loads((self.work/'interfaces.jsonl').read_text());self.assertEqual(row['scenario'],'idempotency');self.assertFalse(row['registration_evidence'])
 def test_inventory_pure_and_original40_cleanup_has_no_fault_DDL(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();value=json.loads(out.getvalue());self.assertEqual(value['http_calls'],12);self.assertEqual(value['target_POSTs'],10);self.assertTrue(value['paste_download_not_supported']);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
  x=self.chain();x.remove_fault=Mock()
  with patch.object(c.ro.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x);x.remove_fault.assert_not_called()
if __name__=='__main__':unittest.main()
