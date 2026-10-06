"""Offline actual callback counterexamples; no HTTP, PostgreSQL or listeners."""
import ast,contextlib,copy,io,json,sys,tempfile,unittest
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_core_transaction_tail_coverage as c
class CoreTailTests(unittest.TestCase):
 def chain(self):
  tmp=tempfile.TemporaryDirectory();self.addCleanup(tmp.cleanup);x=object.__new__(c.Chain);x.work=Path(tmp.name);x.nonce='a'*32;x.key='management-'+x.nonce[:20];x.database='own';x.db_user='own';x.owner_tag='own';x.frozen=Mock();x.owner=Mock();x.sql=Mock();x.fault_attempted=False;x.calls=0;return x
 def ledger(self):return dict(other_public={'cmdb_resource_applications':'same','users':'same','rbac_roles':'same'},**{t:[] for t in c.TABLES})
 def recovery(self,kind):
  b=self.ledger();a=copy.deepcopy(b);actor='b'*32;app='c'*32;env='d'*32;key='management-'+'a'*20;binding=dict(kind='prometheus',connector_id='prometheus',resource=key);target=app;changes={}
  if kind=='service':
   a['applications']=[dict(id=app,name=key,key=key,environment='test')];a['application_environments']=[dict(id=env,application_id=app,key='test',bindings=[],binding_revision=1)];a['v3_application_environments']=[dict(application_id=app,environment_id='test')];a['v3_service_environment_profiles']=[dict(application_id=app,environment_id='test',tier='T1',owner_label='Synthetic owner',namespace='')];changes=dict(environment='test',namespace='',resource_id='',resource_linked=False,tier='T1',existing_service=False)
  elif kind=='mapping':
   target=env;b['application_environments']=[dict(id=env,application_id=app,key='test',bindings=[],binding_revision=1,updated_at='preserved')];a=copy.deepcopy(b);a['application_environments'][0].update(bindings=[binding],binding_revision=2);changes=dict(environment='test',binding_revision_before=1,binding_revision_after=2,bindings_before=[],bindings_after=[binding])
  else:
   app='';target='e'*64;b['sessions']=[dict(token_hash=target,user_id=actor,expires_at='same'),dict(token_hash='f'*64,user_id=actor,expires_at='same'),dict(token_hash='1'*64,user_id='peer',expires_at='same')];a=copy.deepcopy(b);a['sessions']=a['sessions'][1:]
  a['management_audit']=[dict(id=1,actor_id=actor,actor_kind='user',application_id=app,action={'service':'v3.service.created','mapping':'v3.service_mapping.saved','session':'v3.session.revoked'}[kind],target=target,outcome='success',changes=changes)]
  return b,a,(kind,actor,key,target,app,binding)
 def test_all_recoveries_exact_audit_and_unrelated_ledger_counterexamples(self):
  for kind in ('service','mapping','session'):
   b,a,args=self.recovery(kind);c.verify_recovery(b,a,*args)
   for mutate in (lambda d:d['other_public'].update(users='changed'),lambda d:d['management_audit'][0].update(actor_id='foreign'),lambda d:d['management_audit'][0].update(changes={}),lambda d:d['management_audit'].append(d['management_audit'][0]),lambda d:d['sessions'].append({'foreign':'row'})):
    bad=copy.deepcopy(a);mutate(bad)
    if bad==a:continue
    with self.assertRaises(AssertionError):c.verify_recovery(b,bad,*args)
 def test_rollback_full_rows_timestamp_and_hash(self):
  b,a,args=self.recovery('session');c.same(b,copy.deepcopy(b));bad=copy.deepcopy(b);bad['sessions'][0]['expires_at']='lost'
  with self.assertRaises(AssertionError):c.same(b,bad)
 def test_mapping_preserves_other_columns_and_exact_revision(self):
  b,a,args=self.recovery('mapping')
  for key,value in (('updated_at','changed'),('binding_revision',3),('bindings',[])):
   bad=copy.deepcopy(a);bad['application_environments'][0][key]=value
   with self.assertRaises(AssertionError):c.verify_recovery(b,bad,*args)
 def test_install_unknown_commit_and_intent_IO_are_restorable(self):
  x=self.chain();x.sql.side_effect=RuntimeError('unknown commit')
  with self.assertRaisesRegex(RuntimeError,'unknown'):x.install_fault('service','b'*32,x.key)
  self.assertTrue(x.fault_attempted);self.assertEqual((x.work/'core-service-fault-intent.json').stat().st_mode&0o777,0o600)
  x=self.chain()
  with patch.object(c.base,'_private',side_effect=OSError('full')):
   with self.assertRaises(OSError):x.install_fault('service','b'*32,x.key)
  x.sql.assert_not_called();self.assertTrue(x.fault_attempted)
 def test_restore_bound_uses_no_frozen_and_absence_required(self):
  x=self.chain();x.fault_attempted=True;x.fault=c.fault_statements(x.nonce,'b'*32,'service',x.key);x.pg=Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
  with patch.object(c.time,'monotonic',return_value=100):x.remove_fault(102)
  x.owner.assert_called_once_with(deadline=102);self.assertEqual([r.kwargs['deadline'] for r in x.pg.call_args_list],[102,102]);x.frozen.assert_not_called();self.assertFalse(x.fault_attempted)
 def test_exercise_same_body_oppositeAPI_and_first_error(self):
  x=self.chain();b,a,args=self.recovery('session');x.ledger=Mock(side_effect=[b,b,a]);x.save_ledger=Mock();x.marker_case=Mock();x.install_fault=Mock();x.remove_fault=Mock();x.call=Mock(return_value=(None,{}));body={};x.exercise('session','reader','DELETE','/api/v1/me/sessions/'+args[3],body,'settings_unavailable',204,args[1],args[3])
  calls=x.call.call_args_list;self.assertIs(calls[0].args[3],calls[1].args[3]);self.assertEqual([r.kwargs['index'] for r in calls],[0,1]);x.remove_fault.assert_called_once()
  x.ledger=Mock(return_value=b);x.call=Mock(side_effect=AssertionError('HTTP first'));x.remove_fault=Mock(side_effect=RuntimeError('restore second'));x.diagnostic=Mock(side_effect=OSError('full'))
  with self.assertRaisesRegex(AssertionError,'HTTP first'):x.exercise('session','reader','DELETE','/api/v1/me/sessions/'+args[3],body,'settings_unavailable',204,args[1],args[3])
  x.call.assert_called_once();x.remove_fault.assert_called_once()
 def test_restore_failure_still_runs_owned_UUID_cleanup(self):
  x=self.chain();x.remove_fault=Mock(side_effect=RuntimeError('restore first'))
  with patch.object(c.ro,'bounded_cleanup') as cleanup:
   with self.assertRaisesRegex(RuntimeError,'restore first'):x.cleanup()
  cleanup.assert_called_once_with(x)
 def test_exact_fault_scopes_and_normal_SQLSTATE(self):
  for kind,target,app in (('service','management-'+'a'*20,''),('mapping','d'*32,'c'*32),('session','e'*64,'')):
   _,create,_=c.fault_statements('a'*32,'b'*32,kind,target,app)
   for text in ('NEW.actor_id=',"NEW.outcome='success'",'RAISE EXCEPTION','BEGIN;','COMMIT;'):self.assertIn(text,create)
   self.assertNotIn('23505',create)
  self.assertIn('EXISTS(SELECT 1 FROM public.applications',c.fault_statements('a'*32,'b'*32,'service','management-'+'a'*20)[1])
 def test_ledger_all_public_full_rows_and_original_timeout(self):
  x=self.chain();x.sql.side_effect=[json.dumps(list(c.TABLES)+['users']),'{}'];x.ledger();query=x.sql.call_args.args[0]
  for table in c.TABLES:self.assertIn('public.'+table,query)
  self.assertIn('public.users',query);self.assertTrue(x.sql.call_args.kwargs['read']);self.assertIn('statement_timeout=8000',__import__('inspect').getsource(c.base.Chain.pg))
 def test_inventory_is_prepared_and_no_registration(self):
  with patch.object(c.ro,'digest',side_effect=lambda p: c.ADDENDUM_SHA if Path(p)==c.ADDENDUM else __import__('hashlib').sha256(Path(p).read_bytes()).hexdigest()),patch.object(c.ro,'doctor') as doctor,patch.object(c.os,'open') as opened,patch.object(c.sqlite3,'connect') as db,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();opened.assert_not_called();db.assert_not_called();self.assertEqual(json.loads(out.getvalue())['exact_HTTP_calls'],15);self.assertFalse(c.evidence(dict(registration_evidence=True))['registration_evidence']);ast.parse(Path(c.__file__).read_text())
 def test_admission_original_fields_and_sole_runner_fail_closed(self):
  run=str(c.uuid.uuid4());path=c.ROOT/'.local/testlab'/('offline-'+run+'.json');permit=dict(status='root-admitted-core-transaction-tail',owner=c.base.OWNER,resource_run_id=run,source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha256=c.RUNTIME,postgres_cid=c.PG_CID,ports=list(c.PORTS),helper_sha256={'offline':'hash'},transaction_tail_addendum_sha256=c.ADDENDUM_SHA,manifest_sha256='f'*64,fresh_nonce_hmac_process_tuple_verified=True,headroom_verified=True,core_transaction_tail_faults_authorized=True);row=dict(status='running',selected_suite=c.MODULE,source_tree_sha=c.SOURCE,pid=c.os.getppid(),manifest_sha='f'*64)
  with patch.object(Path,'lstat',return_value=type('Stat',(),dict(st_mode=0o100600,st_uid=c.os.getuid()))()),patch.object(c.base,'private_json',return_value=permit),patch.object(c.ro,'digest',side_effect=lambda p: c.ADDENDUM_SHA if Path(p)==c.ADDENDUM else __import__('hashlib').sha256(Path(p).read_bytes()).hexdigest()),patch.object(c,'hashes',return_value={'offline':'hash'}),patch.object(c.sqlite3,'connect') as db:
   db.return_value.execute.return_value=[(json.dumps(row),)];c.admission(path,run)
   for field in ('source_tree_sha','binary_sha256','runtime_sha256','postgres_cid','ports','helper_sha256','transaction_tail_addendum_sha256','core_transaction_tail_faults_authorized','headroom_verified','fresh_nonce_hmac_process_tuple_verified'):
    saved=permit[field];permit[field]=False
    with self.assertRaises(AssertionError):c.admission(path,run)
    permit[field]=saved
   db.return_value.execute.return_value=[(json.dumps(row),),(json.dumps(row),)]
   with self.assertRaises(AssertionError):c.admission(path,run)
if __name__=='__main__':unittest.main()
