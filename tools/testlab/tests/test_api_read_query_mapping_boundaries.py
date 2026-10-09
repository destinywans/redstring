"""Offline exact-contract and evidence counterexamples; no native resources."""
import contextlib,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_read_query_mapping_boundaries as c
class QueryMappingTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.nonce='a'*32;x.calls=0;x.users={};x.cookies={'admin':'cookie-never-store'};x.first_red=Mock();x.frozen=Mock();x.contract=c.Contract(c.ROOT);return x
 def test_three_exact_samples_no_business_producer(self):
  s=c.samples();self.assertEqual(tuple(x[0] for x in s),c.CASES);self.assertEqual(s[0][2],'/api/v1/silences?state=invalid');self.assertEqual(len(s[1][2].split('/')[4]),1025);self.assertEqual(s[2][3],dict(environment_id='test',items=[]));self.assertEqual([x[4] for x in s],['invalid_state','invalid_reference','invalid_mappings'])
 def test_contract_only_reviewed_query_and_three_targets(self):
  con=c.Contract(c.ROOT)
  for _,method,path,_,_,_ in c.samples():con.canonical(method,path)
  con.canonical('POST','/api/v1/auth/login')
  for method,path in [('GET','/api/v1/silences?state=invalid&state=active'),('GET','/api/v1/silences?state=active'),('GET','https://evil/silences?state=invalid'),('POST','/api/v1/services'),('GET','/api/v1/workspace')]:
   with self.assertRaises((AssertionError,c.Blocked)):con.canonical(method,path)
 def test_six_wire422_safe_exact_record_false_registration(self):
  for name,method,path,body,code,message in c.samples():
   for index in range(2):
    x=self.chain();x.expected_error=(code,message);fake=Mock(return_value=(422,json.dumps(dict(error=dict(code=code,message=message))).encode(),{'Content-Type':'application/json'}))
    with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:
     x.call('admin',method,path,body,want=422,code=code,index=index,scenario='boundary');self.assertIs(c.ro.transport,fake)
    x.first_red.assert_not_called();row=json.loads((self.work/'interfaces.jsonl').read_text().splitlines()[-1]);self.assertFalse(row['registration_evidence']);self.assertEqual(row['test_name'],'FormalReadQueryMappingBoundaries');self.assertEqual(row['scenario'],'boundary');wire=(self.work/'query-input-wire-1.json').read_text();self.assertNotIn('cookie-never-store',wire);self.assertFalse(json.loads(wire)['request_body_stored']);self.assertTrue(all(json.loads(s)['interface']['test_name']=='FormalReadQueryMappingBoundaries' for s in out.getvalue().splitlines()))
 def test_wrong_message_status_and_nonjson_do_not_store_raw_secret(self):
  for status,value,headers in [(422,dict(error=dict(code='invalid_state',message='secret-reflected')),{'Content-Type':'application/json'}),(503,dict(error=dict(code='alertmanager_unconfigured',message='secret-reflected')),{'Content-Type':'application/json'}),(422,dict(error=dict(code='invalid_state',message='Select one known silence state')),{'Content-Type':'text/plain'})]:
   x=self.chain();x.expected_error=('invalid_state','Select one known silence state');fake=Mock(return_value=(status,json.dumps(value).encode(),headers))
   with patch.object(c.ro,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('admin','GET','/api/v1/silences?state=invalid',want=422,code='invalid_state',scenario='boundary')
   x.first_red.assert_called_once();self.assertNotIn('secret-reflected',(self.work/'interfaces.jsonl').read_text())
 def test_inventory_pure_limits_and_no_extra_test_scenarios(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();v=json.loads(out.getvalue());self.assertEqual(v['http_calls'],7);self.assertEqual(v['target_HTTP'],6);self.assertEqual((v['API_seconds'],v['SQL_seconds'],v['outer_seconds'],v['cleanup_seconds'],v['grace_seconds']),(12,8,420,40,45));self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)))
 def test_cleanup_exact_original40_no_fault_DDL(self):
  x=self.chain()
  with patch.object(c.ro.Chain,'cleanup') as cleanup,patch.object(c.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  cleanup.assert_called_once_with(x)
if __name__=='__main__':unittest.main()
