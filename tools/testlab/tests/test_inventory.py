import json,sys,tempfile,threading,unittest
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
from inventory import actual_interface,interfaces
from runner import Runner
from store import Store

# Exact non-secret normal row from native f31da62c, management.http.2.
# The original row and normalized SQLite record remain immutable private evidence.
TRACE_NORMAL_ROW = {
 'name':'management.http.2','method':'GET','path':'/api/v1/traces',
 'route_class':'main','scenario':'normal','scenario_detail':'target',
 'expected':{'status':[200],'error_code':None},
 'run_id':'8f428757-501e-4bf7-b2bb-7cfd671dcf09',
 'call_id':'8f428757-501e-4bf7-b2bb-7cfd671dcf09-2',
 'test_name':'FormalTraceAgentEmpty','actual':{'status':200,'error_code':None},
 'status':'passed','registration_evidence':False,
 'evidence_mode':'real_local','upstream_mode':'real_local',
 'source_tree_sha':'93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb',
 'binary_sha256':'6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70',
 'binary_platform':'darwin/arm64 Mach-O',
 'proof_scope':'scope=agent honest empty/partial persisted consumer only; no authoritative execution/spans/cost or Tempo completeness; no whole acceptance',
}

class InventoryTests(unittest.TestCase):
 def test_native_trace_explicit_false_survives_normalization(self):
  result=actual_interface(TRACE_NORMAL_ROW)
  self.assertFalse(result['registration_evidence'])
  self.assertEqual(result['scenarios']['normal']['actual_status'],200)
  self.assertEqual(result['scenarios']['normal']['status'],'passed')
  self.assertEqual(result['proof_scope'],TRACE_NORMAL_ROW['proof_scope'])
 def test_explicit_false_is_preserved_for_successful_http_modes(self):
  for mode in ('real_local','protocol_mock'):
   for code in (200,201,204,299):
    with self.subTest(mode=mode,code=code):
     row=dict(TRACE_NORMAL_ROW,actual=code,expected=code,evidence_mode=mode,upstream_mode=mode)
     self.assertFalse(actual_interface(row)['registration_evidence'])
 def test_missing_registration_keeps_legacy_success_fallback(self):
  for status,code,registered in [('passed',200,True),('passed',204,True),('passed',422,False),('failed',200,False)]:
   with self.subTest(status=status,code=code):
    row=dict(TRACE_NORMAL_ROW,status=status,actual=code,expected=code)
    row.pop('registration_evidence')
    self.assertEqual(actual_interface(row)['registration_evidence'],registered)
 def test_explicit_true_keeps_existing_status_exclusions(self):
  for status,code,registered in [('passed',200,True),('passed',422,True),('failed',500,True),('passed',401,False),('passed',403,False),('passed',404,False)]:
   with self.subTest(status=status,code=code):
    row=dict(TRACE_NORMAL_ROW,registration_evidence=True,status=status,actual=code,expected=code)
    self.assertEqual(actual_interface(row)['registration_evidence'],registered)
 def test_runner_parser_persists_native_false_and_legacy_fallback(self):
  with tempfile.TemporaryDirectory() as directory:
   root=Path(directory).resolve()
   runner=object.__new__(Runner)
   runner.root=root;runner.store=Store(root);runner.prepared={}
   runner.stop=threading.Event();runner.process=None
   runner.run_id=runner.store.create_run('offline-source','offline-branch')['id']
   spec={'id':'offline-inventory'}
   suite=runner.store.add_suite(runner.run_id,spec)
   legacy=dict(TRACE_NORMAL_ROW,name='legacy',path='/api/v1/legacy')
   legacy.pop('registration_evidence')
   rows=[dict(name=row['name'],status='passed',actual=row['actual'],interface=row) for row in (TRACE_NORMAL_ROW,legacy)]
   outputs=[('stdout',json.dumps(row)+'\n') for row in rows]
   process=Mock();process.wait.return_value=0
   with patch('runner.command_env',return_value={}),patch('runner.subprocess.Popen',return_value=process),patch('runner.command_output',return_value=outputs):
    runner.execute_command(spec,suite,{'argv':['offline-no-exec'],'parser':'testlab_json','timeout_seconds':5})
   detail=runner.store.detail(runner.run_id)
   actual={row['path']:row for row in detail['interfaces']}
   self.assertFalse(actual['/api/v1/traces']['registration_evidence'])
   self.assertTrue(actual['/api/v1/legacy']['registration_evidence'])
   self.assertEqual(actual['/api/v1/traces']['scenarios']['normal']['actual_status'],200)
   self.assertEqual(actual['/api/v1/traces']['proof_scope'],TRACE_NORMAL_ROW['proof_scope'])
   self.assertEqual([case['status'] for case in detail['cases']],['passed','passed'])
   case=next(case for case in detail['cases'] if case['name']==TRACE_NORMAL_ROW['name'])
   raw,_=runner.store.read_evidence(case['attempts'][0]['evidence_ids'][0])
   self.assertFalse(json.loads(raw)['interface']['registration_evidence'])
 def test_auth_response_is_not_registration_evidence(self):
  row=dict(method='GET',path='/api/v1/test',scenario='authorization',status='passed',expected=401,actual=401,registration_evidence=True)
  result=actual_interface(row)
  self.assertFalse(result['registration_evidence'])
  self.assertEqual(set(result['scenarios']),{'authorization'})
 def test_inventory_is_unverified_source(self):
  rows=list(interfaces(Path(__file__).resolve().parents[3]))
  self.assertEqual(len({(r['method'],r['path']) for r in rows}),len(rows))
  self.assertGreater(len(rows),400)
  self.assertTrue(all(not r['registration_evidence'] and {v['status'] for v in r['scenarios'].values()}=={'queued'} for r in rows))
  self.assertTrue(all(r['path'].startswith('/api/v1/') for r in rows))
