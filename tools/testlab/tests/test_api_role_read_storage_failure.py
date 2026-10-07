"""Offline execution of the dedicated role ACL/HTTP seams; no resources."""
import contextlib,copy,hashlib,io,json,re,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_role_read_storage_failure as r
from inventory import actual_interface
from overview import get_overview
from store import Store
VIEWER_MIGRATION_JSON='{"id":"viewer","name":"只读成员","description":"查看已关联的基础设施资源。","kind":"system","builtin":true,"revision":1,"grants":[{"module":"workspace","action":"view","scope_kind":"global","scope_id":"*","assurance":"standard"}]}'
class RoleReadTests(unittest.TestCase):
 def test_source_migration_fixture_fixed_UTF8_digest_not_generated_from_checker(self):
  # Fresh 018 viewer fields, 023 kind, 029 workspace grant, 031 revision/assurance.
  self.assertEqual(hashlib.sha256(VIEWER_MIGRATION_JSON.encode('utf-8')).hexdigest(),'1381d28cff6a3e7ff97848e2d87840c7c3654a8addb94e942ec2d474c1c21142')
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=r.Chain.__new__(r.Chain);x.work=self.work;x.run_id=str(uuid.uuid4());x.database='aiops_testlab_readonly_'+'a'*32;x.db_user='readonly_'+'a'*32;x.frozen=Mock();x.owner=Mock();x.marker_case=Mock();x.acl_before=None;x.acl_fault=False;x.fault_snapshot=None;x.path='/api/v1/roles/viewer';x.admin_email='owned-admin@testlab.invalid';x.actor='b'*32;x.auth_read=Mock();x.target=json.loads(VIEWER_MIGRATION_JSON);return x
 def before(self,x):
  return dict(database=x.database,table='rbac_roles',oid=17,owner=x.db_user,rls=False,select=True,table_acl_raw=None,role_oid=9,superuser=False,bypass_rls=False,memberships=0,view=dict(owner=x.db_user,kind='v',options=None,digest='b'*64),entries=[dict(grantor=x.db_user,grantee=9,privilege=p,grantable=False) for p in ('INSERT','SELECT','UPDATE')],column_acls=[dict(number=i+1,name=n,raw=None,select=True,entries=[]) for i,n in enumerate(('id','name','description','v3_kind','v3_matrix_revision','builtin'))])
 def fault(self,b):
  v=r.fault_acl(b);v['table_acl_raw']='revoked-table-acl'
  for col in v['column_acls']:col['raw']='owner=r/owner' if col['name'] in ('id','v3_kind') else None
  return v
 def dto(self,x):return dict(id='viewer',name=x.target['name'],desc=x.target['description'],kind='system',scope=['global'],actions=['View','Create / edit','Delete','Approve','Manage'],matrix=[dict(resource=str(i),grants=[0,0,0,0,0]) for i in range(13)],notes='Machine identities cannot approve. Approval remains unavailable until passkey step-up verification is implemented, and the author cannot approve their own proposal. Other platform grants are managed outside this matrix. Only grants in the selected authorized environments are shown.')
 def headers(self):return {'Content-Type':'application/json','Cache-Control':'private, no-store','X-Role-Matrix-Revision':'1'}
 def http_chain(self):
  x=self.chain();x.calls=0;x.users={};x.cookies={'admin':'synthetic-cookie'};x.first_red=Mock();x.contract=r.Contract(r.ROOT)
  x.ledger=Mock(return_value={});x.revoke=Mock();x.restore=Mock()
  return x
 def response(self,x):return dict(self.dto(x),members=0,member_names=[])
 def overview_route(self,row):
  root=self.work/'overview';(root/'docs/api').mkdir(parents=True);(root/'internal/apicontract').mkdir(parents=True)
  (root/'docs/api/v3-contract.md').write_text('| GET | `/roles/{id}` | roles |\n')
  (root/'internal/apicontract/openapi.json').write_text(json.dumps({'paths':{'/roles/{id}':{'get':{'tags':['roles']}}}}))
  store=Store(root);run=store.create_run(source_tree_sha=r.SOURCE,selected_suite=r.MODULE)
  store.update('runs',run['id'],status='failed',source_tree_sha_end=r.SOURCE)
  suite=store.add_suite(run['id'],{'id':r.MODULE,'name':r.MODULE})
  for name,status,actual in ((row['name'],row['status'],row['actual']),('readonly.business','failed',{'error':'role contract failed'})):
   case=store.add_case(run['id'],suite['id'],name=name,evidence_mode='real_local');attempt=store.start_attempt(case)
   evidence=store.write_evidence(run['id'],json.dumps({'name':name,'status':status,'actual':actual}))
   store.finish_attempt(case,attempt,status,actual,evidence_ids=[evidence])
   if name==row['name']:store.add_record(run['id'],'interfaces',actual_interface(row))
  return get_overview(root,store,source_sha=r.SOURCE)['coverage']['routes'][0]
 def test_invalid_role_projection_and_headers_fail_before_interface_success(self):
  for kind in ('notes','matrix','revision','cache'):
   with self.subTest(kind=kind):
    x=self.http_chain();x.work=self.work/kind;x.work.mkdir();value=self.response(x);headers=self.headers()
    if kind=='notes':value['notes']='incorrect role projection'
    elif kind=='matrix':value['matrix'][0]['grants'][0]=1
    elif kind=='revision':headers['X-Role-Matrix-Revision']='2'
    else:headers['Cache-Control']='public, max-age=3600'
    with patch.object(r.ro,'transport',return_value=(200,json.dumps(value).encode(),headers)),contextlib.redirect_stdout(io.StringIO()) as out:
     with self.assertRaises(AssertionError):x.call('admin','GET',x.path,want=200)
    row=json.loads((x.work/'interfaces.jsonl').read_text());self.assertEqual(row['status'],'failed');self.assertEqual(row['actual']['status'],200);self.assertFalse(row['registration_evidence'])
    x.first_red.assert_called_once();self.assertEqual(x.first_red.call_args.args[0]['status'],'failed')
    self.assertFalse(any(v['status']=='passed' for v in map(json.loads,out.getvalue().splitlines())))
 def test_failed_role_contract_cannot_verify_overview_boundary(self):
  x=self.http_chain();headers=dict(self.headers(),**{'Cache-Control':'public, max-age=3600'})
  with patch.object(r.ro,'transport',return_value=(200,json.dumps(self.response(x)).encode(),headers)),contextlib.redirect_stdout(io.StringIO()):
   with self.assertRaises(AssertionError):x.exercise({})
  row=json.loads((x.work/'interfaces.jsonl').read_text());route=self.overview_route(row)
  self.assertNotEqual(route['scenario_statuses']['boundary'],'verified');self.assertEqual(route['normal_status'],'missing');self.assertEqual(len(route['failed_evidence']),1);x.marker_case.assert_not_called()
 def test_successful_detail_retains_caller_validator_and_actual_response(self):
  x=self.http_chain();value=self.response(x);validator=Mock()
  with patch.object(r.ro,'transport',return_value=(200,json.dumps(value).encode(),self.headers())),contextlib.redirect_stdout(io.StringIO()):
   received,headers=x.call('admin','GET',x.path,want=200,validate=validator)
  self.assertEqual(received,value);self.assertEqual(headers,self.headers());validator.assert_called_once_with(value);x.first_red.assert_not_called()
  self.assertEqual(json.loads((x.work/'interfaces.jsonl').read_text())['status'],'passed')
 def test_recovery_header_failure_is_a_failed_http_record(self):
  x=self.http_chain();value=self.response(x);valid=(200,json.dumps(value).encode(),self.headers())
  fault=(503,json.dumps(r.ERROR).encode(),{'Content-Type':'application/json'})
  invalid=(200,json.dumps(value).encode(),dict(self.headers(),**{'X-Role-Matrix-Revision':'2'}))
  with patch.object(r.ro,'transport',side_effect=[valid,valid,fault,invalid]),contextlib.redirect_stdout(io.StringIO()):
   with self.assertRaises(AssertionError):x.exercise({})
  rows=list(map(json.loads,(x.work/'interfaces.jsonl').read_text().splitlines()))
  self.assertEqual([v['status'] for v in rows],['passed','passed','passed','failed']);self.assertEqual(rows[-1]['actual']['status'],200);x.first_red.assert_called_once();x.restore.assert_called_once()
  self.assertNotIn('recovery',[c.args[0] for c in x.marker_case.call_args_list])
 def test_contract_only_login_role_list_and_detail_no_legacy_or_write(self):
  c=r.Contract(r.ROOT)
  for method,path in r.TARGETS+r.SUPPORT:c.canonical(method,'/api/v1'+path.replace('{id}','viewer'))
  for method,path in [('POST','/api/v1/roles'),('POST','/api/v1/rbac/roles'),('PUT','/api/v1/roles/viewer'),('GET','/api/v1/roles/viewer?foo=1')]:
   with self.assertRaises((AssertionError,r.Blocked)):c.canonical(method,path)
 def test_actual_compiled_revoke_restore_transaction_grammar_and_exact_target(self):
  x=self.chain();before=self.before(x);state=copy.deepcopy(before);captured=[]
  def pg(sql,database,user):
   nonlocal state
   self.assertEqual((database,user),(x.database,'testlab'))
   grant=r'"readonly_[a]{32}"'
   fault=re.fullmatch(r'BEGIN; REVOKE SELECT ON TABLE public\."rbac_roles" FROM ('+grant+r'); GRANT SELECT \(id, v3_kind\) ON TABLE public\."rbac_roles" TO \1; COMMIT;',sql)
   restore=re.fullmatch(r'BEGIN; REVOKE SELECT \(id, v3_kind\) ON TABLE public\."rbac_roles" FROM ('+grant+r'); GRANT SELECT ON TABLE public\."rbac_roles" TO \1; COMMIT;',sql)
   self.assertTrue(bool(fault)^bool(restore),'compiled ACL SQL must have two correct statements and atomic COMMIT');captured.append(sql)
   state=self.fault(before) if fault else dict(copy.deepcopy(before),table_acl_raw='materialized-original-effective-acl')
  x.pg=Mock(side_effect=pg);x.acl=Mock(side_effect=lambda:copy.deepcopy(state));x.revoke();self.assertFalse(state['select']);self.assertEqual({c['name'] for c in state['column_acls'] if c['select']},{'id','v3_kind'});x.restore()
  self.assertEqual(len(captured),2);self.assertFalse(x.acl_fault);proof=json.loads((self.work/'role-acl-restored.json').read_text());self.assertFalse(proof['raw_table_relacl_equal']);self.assertTrue(proof['raw_column_ACLs_equal'])
 def test_actual_acl_query_captures_all_columns_raw_acl_view_owner_and_oid_bigint(self):
  x=self.chain();b=self.before(x);x.pg=Mock(return_value=json.dumps(b));self.assertEqual(x.acl(),b);sql=x.pg.call_args.args[0]
  for literal in ('oid::bigint',"'grantee',a.grantee::bigint",'x.attacl::text',"COALESCE(x.attacl,'{}'::aclitem[])",'has_column_privilege','pg_get_viewdef','pg_auth_members',"v.relname='v3_effective_role_grants'","c.relname='rbac_roles'"):self.assertIn(literal,sql)
  self.assertEqual(x.pg.call_args.args[1:],(x.database,'testlab'));self.assertTrue(sql.startswith('BEGIN READ ONLY; '));self.assertTrue(sql.endswith('; COMMIT;'))
 def test_baseline_foreign_permissions_or_column_acls_stop_before_mutation(self):
  for kind in ('public','column','no_table_select','grantoption'):
   x=self.chain();b=self.before(x)
   if kind=='public':b['entries'][1]['grantee']=0
   elif kind=='column':b['column_acls'][0]['raw']='pre-existing-column-grant'
   elif kind=='no_table_select':b['select']=False
   else:b['entries'][1]['grantable']=True
   x.acl=Mock(return_value=b);x.pg=Mock()
   with self.assertRaises(AssertionError):x.revoke()
   x.pg.assert_not_called();self.assertFalse(x.acl_fault)
 def test_actual_acl_identity_view_options_and_membership_drift_failclosed(self):
  for kind in ('view_owner','view_option','membership','superuser','bypass','string_oid','missing_column'):
   x=self.chain();b=self.before(x)
   if kind=='view_owner':b['view']['owner']='foreign'
   elif kind=='view_option':b['view']['options']=['security_invoker=true']
   elif kind=='membership':b['memberships']=1
   elif kind=='superuser':b['superuser']=True
   elif kind=='bypass':b['bypass_rls']=True
   elif kind=='string_oid':b['role_oid']='9'
   else:b['column_acls']=[c for c in b['column_acls'] if c['name']!='name']
   x.pg=Mock(return_value=json.dumps(b))
   with self.assertRaises(AssertionError):x.acl()
 def test_ineffective_revoke_or_extra_detail_grant_cannot_pass(self):
  for mode in ('table','name','id'):
   x=self.chain();b=self.before(x);f=self.fault(b)
   if mode=='table':f['select']=True
   else:next(c for c in f['column_acls'] if c['name']==mode)['select']=mode=='name'
   x.acl=Mock(side_effect=[b,f]);x.pg=Mock()
   with self.assertRaises(AssertionError):x.revoke()
   self.assertTrue(x.acl_fault);x.pg.assert_called_once()
 def test_foreign_fault_raw_or_effective_drift_refuses_restore(self):
  for mode in ('raw','view','column','entry'):
   x=self.chain();b=self.before(x);f=self.fault(b);x.acl_before=b;x.acl_fault=True;x.fault_snapshot=copy.deepcopy(f)
   if mode=='raw':f['table_acl_raw']='foreign mutation'
   elif mode=='view':f['view']['digest']='c'*64
   elif mode=='column':f['column_acls'][1]['entries']=[{'grantor':'foreign'}]
   else:f['entries'].append(dict(grantor=x.db_user,grantee=0,privilege='INSERT',grantable=False))
   x.acl=Mock(return_value=f);x.pg=Mock()
   with self.assertRaises(AssertionError):x.restore()
   x.pg.assert_not_called();self.assertTrue(x.acl_fault)
 def test_restore_rejects_empty_instead_of_original_null_column_acl(self):
  x=self.chain();b=self.before(x);f=self.fault(b);after=copy.deepcopy(b);after['column_acls'][0]['raw']='{}';x.acl_before=b;x.acl_fault=True;x.fault_snapshot=f;x.acl=Mock(side_effect=[f,after]);x.pg=Mock()
  with self.assertRaises(AssertionError):x.restore()
  x.pg.assert_called_once();self.assertTrue(x.acl_fault)
 def test_atomic_mutation_failure_accepts_only_original_or_expected_fault_restore(self):
  x=self.chain();b=self.before(x);x.acl=Mock(side_effect=[b,b,b]);x.pg=Mock(side_effect=RuntimeError('private SQL fault'))
  with self.assertRaises(RuntimeError):x.revoke()
  self.assertTrue(x.acl_fault);x.restore();self.assertFalse(x.acl_fault);x.pg.assert_called_once()
 def test_migration_target_readonly_selection_and_http_catalog_no_creation(self):
  x=self.chain();x.sql=Mock(side_effect=['b'*32,json.dumps(x.target)]);x.call=Mock(return_value=({'items':[{'id':'viewer','kind':'system'}]},{}));x.inputs()
  self.assertTrue(x.sql.call_args.args[0].startswith('SELECT '));self.assertEqual(x.sql.call_args.kwargs,{'read':True});x.call.assert_called_once_with('admin','GET','/api/v1/roles',want=200,scenario='fixture');v=json.loads((self.work/'role-target.json').read_text());self.assertEqual(v['source'],'migration_builtin');self.assertFalse(v.get('HTTP_role_creation_claimed',False))
  for bad in (dict(x.target,builtin=False),dict(x.target,grants=[]),dict(x.target,id='synthetic-business'),dict(x.target,revision=2),dict(x.target,grants=[dict(x.target['grants'][0],action='manage')])):
   x.sql=Mock(side_effect=['b'*32,json.dumps(bad)]);x.call.reset_mock()
   with self.assertRaises(AssertionError):x.inputs()
   x.call.assert_not_called()
   self.assertEqual(json.loads((self.work/'role-target-observed.json').read_text())['target'],bad)
  query=x.sql.call_args.args[0]
  self.assertIn("json_build_object('module',g.module,'action',g.action,'scope_kind',g.scope_kind,'scope_id',g.scope_id,'assurance',g.assurance)",query);self.assertNotIn('SELECT count(*)',query)
 def test_actual_authorization_probe_reads_runtime_view_and_idkind_not_detail_columns(self):
  x=self.chain();expected={'environment':True,'roles':True,'role_auth_columns':True,'actor_current':True};x.sql=Mock(return_value=json.dumps(expected));self.assertEqual(r.Chain.auth_read(x,'fault'),expected);sql=x.sql.call_args.args[0]
  self.assertEqual(x.sql.call_args.kwargs,{'read':True});self.assertEqual(sql.count('FROM v3_effective_role_grants'),2);self.assertIn("WHERE id='admin' AND v3_kind='system'",sql)
  for column in ('name','description','v3_matrix_revision'):self.assertNotIn(column,sql)
  x.sql=Mock(return_value=json.dumps(dict(expected,roles=False)))
  with self.assertRaises(AssertionError):r.Chain.auth_read(x,'fault')
 def test_read_projection_revision_and_zero_matrix_are_required(self):
  x=self.chain();value=self.dto(x);x.verify_read(value,self.headers())
  for bad in (dict(value,kind='business'),dict(value,scope=[]),dict(value,desc='wrong'),dict(value,matrix=[]),dict(value,notes='No extra platform grant')):
   with self.assertRaises(AssertionError):x.verify_read(bad,self.headers())
  with self.assertRaises(AssertionError):x.verify_read(value,dict(self.headers(),**{'X-Role-Matrix-Revision':'2'}))
 def test_exact_detail503_rejects_shared_auth_and_invalid_grant_mapper(self):
  r.verify_error({'error':{'code':'admin_unavailable','message':'Administration storage unavailable'}},{'Content-Type':'application/json'})
  for value in ({'error':{'code':'environment_unavailable','message':'Environment authorization unavailable'}},{'error':{'code':'authorization_unavailable','message':'Authorization storage unavailable'}},{'error':{'code':'role_grants_invalid','message':'Stored role grants invalid'}}):
   with self.assertRaises(AssertionError):r.verify_error(value,{'Content-Type':'application/json'})
 def test_four_phase_http_then_no_write_ledger_and_no_retry(self):
  x=self.chain();x.call=Mock(return_value=(self.dto(x),self.headers()));x.verify_read=Mock();x.ledger=Mock(return_value={});x.revoke=Mock();x.restore=Mock();x.exercise({})
  self.assertEqual([c.kwargs['want'] for c in x.call.call_args_list],[200,200,503,200]);self.assertEqual([c.kwargs['index'] for c in x.call.call_args_list],[0,1,0,1]);self.assertEqual(x.ledger.call_count,4);x.revoke.assert_called_once();x.restore.assert_called_once()
 def test_primary_wrong_mapper_and_restore_error_preserved_stops_recovery(self):
  x=self.chain();primary=AssertionError('private assertion');x.call=Mock(side_effect=[({},{}),({},{}),primary]);x.verify_read=Mock();x.ledger=Mock(return_value={});x.revoke=Mock();x.restore=Mock(side_effect=RuntimeError('private restore'))
  with self.assertRaises(AssertionError) as error:x.exercise({})
  self.assertIs(error.exception,primary);self.assertEqual(x.call.call_count,3);x.restore.assert_called_once();self.assertEqual(json.loads((self.work/'restore-secondary.json').read_text()),{'primary_type':'AssertionError','restore_type':'RuntimeError'})
 def test_full_ledger_includes_role_session_audit_and_read_change_stops_before_acl(self):
  x=self.chain();tables=['sessions','management_audit','ops_incidents','rbac_roles'];ledger={t:'a'*64 for t in tables};x.pg=Mock(side_effect=[json.dumps(tables),json.dumps(ledger)]);self.assertEqual(x.ledger(),ledger);self.assertIn('public."rbac_roles"',x.pg.call_args.args[0]);self.assertIn('public."sessions"',x.pg.call_args.args[0])
  x.call=Mock(return_value=({},{}));x.verify_read=Mock();x.ledger=Mock(return_value=dict(ledger,sessions='b'*64));x.revoke=Mock()
  with self.assertRaises(AssertionError):x.exercise(ledger)
  x.revoke.assert_not_called();self.assertEqual(x.call.call_count,1)
 def test_secondary_note_write_failure_never_masks_primary(self):
  x=self.chain();primary=AssertionError('first');x.call=Mock(side_effect=[({},{}),({},{}),primary]);x.verify_read=Mock();x.ledger=Mock(return_value={});x.revoke=Mock();x.restore=Mock(side_effect=RuntimeError('restore'))
  with patch.object(r.ro,'_private',side_effect=OSError('note')):
   with self.assertRaises(AssertionError) as error:x.exercise({})
  self.assertIs(error.exception,primary);self.assertTrue(x.restore_secondary_note_failed);self.assertEqual(x.call.call_count,3)
 def test_actual_http_seam_saves_sanitized_received_error_before_mapper_rejection(self):
  x=self.chain();x.calls=0;x.users={};x.cookies={'admin':'secret-cookie'};x.first_red=Mock();x.contract=r.Contract(r.ROOT);error={'error':{'code':'admin_unavailable','message':'Administration storage unavailable'}}
  with patch.object(r.ro,'transport',Mock(return_value=(503,json.dumps(error).encode(),{'Content-Type':'application/json','Set-Cookie':'secret'}))),contextlib.redirect_stdout(io.StringIO()):x.call('admin','GET',x.path,want=503,code='admin_unavailable')
  raw=(self.work/'role-read-wire-1.json').read_text();self.assertNotIn('secret',raw);self.assertEqual(json.loads(raw)['response'],error)
  bad={'error':{'code':'environment_unavailable','message':'Environment authorization unavailable'}}
  with patch.object(r.ro,'transport',Mock(return_value=(503,json.dumps(bad).encode(),{'Content-Type':'application/json'}))),contextlib.redirect_stdout(io.StringIO()):
   with self.assertRaises(AssertionError):x.call('admin','GET',x.path,want=503,code='admin_unavailable')
  self.assertTrue((self.work/'role-read-wire-2.json').exists());x.first_red.assert_called_once()
 def test_inventory_six_http_unchanged_budgets_and_cleanup_no_second_restore(self):
  with patch.object(r,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(r.main(['--inventory']),0)
  doctor.assert_not_called();v=json.loads(out.getvalue());self.assertEqual((v['http_calls'],v['target_HTTP'],v['fixture_HTTP']),(6,4,2));self.assertEqual((v['SQL_seconds'],v['API_seconds'],v['outer_seconds'],v['cleanup_seconds'],v['grace_seconds']),(8,12,420,40,45));self.assertEqual(len(r.REQUIRED_CASES),14)
  x=self.chain();x.acl_fault=True;x.restore=Mock()
  with patch.object(r.ro.Chain,'cleanup') as clean,patch.object(r.socket,'socket') as sock,contextlib.redirect_stdout(io.StringIO()):sock.return_value.__enter__.return_value.connect_ex.return_value=1;x.cleanup()
  clean.assert_called_once_with(x);x.restore.assert_not_called()
if __name__=='__main__':unittest.main()
