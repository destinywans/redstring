import contextlib,copy,io,json,sys,unittest
from pathlib import Path
from unittest import mock
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_postmortem_write_boundaries_coverage as h
import api_postmortem_audit_rollback_coverage as original

class BoundariesTests(unittest.TestCase):
 def test_private_module_does_not_mutate_released_lifecycle(self):
  self.assertIsNot(h.pm,original);self.assertEqual(original.PORTS,(18744,18745));self.assertEqual(h.ro.PORTS,(18880,18881));self.assertEqual(h.ALL_PORTS,h.PORTS)
 def test_inventory_is_read_only_and_explicitly_unqualified(self):
  with mock.patch.object(h,'doctor') as doctor,mock.patch.object(h,'admission') as admission,mock.patch.object(h.Chain,'provision') as acquire,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(h.main(['--inventory']),0)
  value=json.loads(out.getvalue());self.assertEqual(value['normal_added'],0);self.assertFalse(value['registration_evidence']);self.assertEqual(value['outer_seconds'],420);self.assertEqual(value['cleanup_seconds'],40)
  doctor.assert_not_called();admission.assert_not_called();acquire.assert_not_called()
 def test_no_live_without_three_explicit_inputs(self):
  for args in ([],['--live'],['--live','--run-id','a'*32]):
   with mock.patch.object(h,'doctor') as doctor,mock.patch.object(h,'admission') as admission,contextlib.redirect_stderr(io.StringIO()):
    with self.assertRaises(SystemExit):h.main(args)
   doctor.assert_not_called();admission.assert_not_called()
 def test_no_publish_runbook_or_unbounded_query_allowed(self):
  contract=h.Contract(h.ROOT);cid='INC-'+'a'*32
  for method,path in [('GET','/postmortems/draft?incident='+cid),('GET','/postmortems/draft?incident='),('POST','/postmortems')]:contract.canonical(method,'/api/v1'+path)
  for method,path in [('POST','/postmortems/'+'a'*32+'/publish'),('POST','/runbooks'),('GET','/postmortems'),('GET','/postmortems/draft?incident='+cid+'&incident='+cid),('GET','/postmortems/draft')]:
   with self.assertRaises((AssertionError,h.Blocked)):contract.canonical(method,'/api/v1'+path)
 def test_invalid_draft_error_is_exact_and_plan_is_explicit(self):
  correct=dict(error=dict(code='invalid_incident',message='Select a resolved incident'));h.verify_invalid_draft(422,correct)
  for status,value in [(404,correct),(422,dict(error=dict(code='invalid_incident',message='other'))),(422,dict(error=dict(code='invalid_incident',message='Select a resolved incident'),detail='leak'))]:
   with self.assertRaises(AssertionError):h.verify_invalid_draft(status,value)
  self.assertEqual(h.HTTP_PLAN,(dict(marker='postmortem_boundaries.draft_invalid_incident',method='GET',path='/postmortems/draft',query='incident=',scenario='boundary',expected_status=422,expected_error_code='invalid_incident',expected_error_message='Select a resolved incident',registration_evidence=False),))
  source=Path(h.__file__).read_text();self.assertIn('check(self.calls==28',source);self.assertIn("self.refuse('draft_invalid_incident','reader','GET',INVALID_DRAFT_PATH,None,422,'invalid_incident')",source)
 def test_invalid_draft_fixture_does_not_broaden_other_queries(self):
  contract=h.Contract(h.ROOT)
  for path in ('/api/v1/postmortems/draft?incident=&other=1','/api/v1/postmortems/draft?incident=&incident=','/api/v1/postmortems/draft?incident='+('x'*129)):
   with self.assertRaises((AssertionError,h.Blocked)):contract.canonical('GET',path)
 def test_body_boundaries_do_not_mutate_control(self):
  body=dict(incident_id='INC-'+'a'*32,source_revision='case:9',status='review',sections=[dict(key='root',title='Root',text='Observed')],actions=[]);before=copy.deepcopy(body)
  cases=h.invalid_inputs(body);self.assertEqual(body,before);self.assertEqual(len(cases),7);self.assertEqual(len({n for n,_,_ in cases}),7)
  expected={'invalid_status':'invalid_postmortem','duplicate_sections':'invalid_postmortem','empty_review':'invalid_postmortem','unknown_field':'invalid_body','missing_revision':'source_revision_required','stale_revision':'postmortem_source_changed','case_seq':'invalid_postmortem'}
  self.assertEqual({n:code for n,_,code in cases},expected)
 def test_exact_ledger_rejects_audit_source_or_other_public_delta(self):
  before=dict(postmortems=[],cases=[dict(id='owned')],audit=[],other_public={'some_table':'a'*64});h.same(before,copy.deepcopy(before))
  for key,value in [('postmortems',[{}]),('cases',[]),('audit',[{}]),('other_public',{'some_table':'b'*64})]:
   with self.assertRaises(AssertionError):h.same(before,dict(before,**{key:value}))
 def test_emitted_http_never_qualifies_normal(self):
  with contextlib.redirect_stdout(io.StringIO()) as out:h.emit('wire','passed',{},interface=dict(registration_evidence=True))
  value=json.loads(out.getvalue());self.assertFalse(value['interface']['registration_evidence']);self.assertEqual(value['interface']['test_name'],'FormalPostmortemWriteBoundaries')
 def test_admission_rejects_noncanonical_or_outside_path_before_io(self):
  with self.assertRaises(AssertionError):h.admission(Path('/tmp/permit'),'a'*32)
 def test_private_call_keeps_actual_current_transport_seam(self):
  with mock.patch.object(h.ro,'transport',return_value=('sentinel',{},{})) as wire:
   self.assertEqual(h.scope['transport']('origin','POST','path',b'body',{'Authorization':'fixture'}),('sentinel',{},{}))
  wire.assert_called_once_with('origin','POST','path',b'body',{'Authorization':'fixture'})
 def test_marker_inventory_complete_and_unique(self):
  self.assertEqual(len(h.REQUIRED_CASES),19);self.assertEqual(len(set(h.REQUIRED_CASES)),19);self.assertTrue(all(n.startswith('postmortem_boundaries.') for n in h.REQUIRED_CASES))
 def test_final_result_is_bounded_and_pause_scope_preserved(self):
  source=Path(h.__file__).read_text();self.assertNotIn('install_fault(',source);self.assertIn('full_business_chain_verified=False',source);self.assertIn("'performance/24h'",source);self.assertIn('json_populate_recordset',source)
 def test_actual_acl_branch_preserves_first_error_on_restore_failure(self):
  x=object.__new__(h.Chain);x.roles={'reader':'owned'};x.body={};x.sql=mock.Mock();first=AssertionError('full ledger changed');x.refuse=mock.Mock(side_effect=first);x.restore_grants=mock.Mock(side_effect=OSError('owned PG unavailable'));x.acl_original=None;x.cleanup_deadline=None
  with self.assertRaises(AssertionError) as caught:x.acl_boundaries('/owned',[])
  self.assertIs(caught.exception,first);self.assertIn('owned ACL recovery secondary: OSError',first.__notes__);self.assertEqual(x.acl_original,[]);self.assertLessEqual(x.restore_grants.call_args.args[1],x.cleanup_deadline)
 def test_actual_restore_uses_one_deadline_for_owner_write_and_readback(self):
  x=object.__new__(h.Chain);x.roles={'reader':'owned'};x.database='owned_db';x.db_user='owned_role';x.owner_tag='owner/run';rows=[{'action':'view'}];x.pg=mock.Mock(side_effect=[json.dumps(dict(database=x.database,role=x.db_user,db_tag=x.owner_tag,role_tag=x.owner_tag)),'',json.dumps(rows)])
  deadline=h.time.monotonic()+4;x.restore_grants(rows,deadline)
  self.assertEqual(len(x.pg.call_args_list),3);self.assertTrue(all(c.kwargs['deadline']==deadline for c in x.pg.call_args_list))
 def test_actual_cleanup_still_deletes_uuid_after_restore_error_with_same40(self):
  x=object.__new__(h.Chain);x.acl_original=[];x.cleanup_deadline=h.time.monotonic()+40;x.restore_grants=mock.Mock(side_effect=OSError('owned restore failed'));deadline=x.cleanup_deadline
  with mock.patch.object(h,'bounded_cleanup') as cleanup,self.assertRaises(OSError):x.cleanup()
  cleanup.assert_called_once_with(x);self.assertEqual(x.cleanup_deadline,deadline);self.assertLessEqual(x.restore_grants.call_args.args[1],deadline)
 def test_actual_ledger_pg_observation_preserves_result_limits_and_no_query_text(self):
  x=object.__new__(h.Chain);x.ledger_active=3;x.ledger_sql_index=0;x.work=Path('/owned')
  with mock.patch.object(h.ro.Chain,'pg',return_value='same bytes') as pg,mock.patch.object(h.ro,'append_json') as save:self.assertEqual(x.pg('SELECT json_object_agg(name,digest) FROM owned',timeout=12),'same bytes')
  pg.assert_called_once_with('SELECT json_object_agg(name,digest) FROM owned',timeout=12);row=save.call_args.args[1];self.assertEqual(row['stage'],'other_public_digests');self.assertEqual(row['statement_timeout_ms'],8000);self.assertFalse(row['SQL_text_or_parameters_stored']);self.assertNotIn('SELECT',json.dumps(row));self.assertEqual(row['outcome'],'passed')
 def test_actual_observed_pg_failure_retains_same_exception(self):
  x=object.__new__(h.Chain);x.ledger_active=2;x.ledger_sql_index=0;x.work=Path('/owned');first=h.Blocked('owned SQL timeout')
  with mock.patch.object(h.ro.Chain,'pg',side_effect=first),mock.patch.object(h.ro,'append_json') as save,self.assertRaises(h.Blocked) as caught:x.pg('SELECT owned')
  self.assertIs(caught.exception,first);self.assertEqual(save.call_args.args[1]['outcome'],'failed');self.assertEqual(save.call_args.args[1]['error_type'],'Blocked')
 def test_outside_ledger_pg_has_no_observer_or_behavior_change(self):
  x=object.__new__(h.Chain);x.ledger_active=None
  with mock.patch.object(h.ro.Chain,'pg',return_value='unchanged') as pg,mock.patch.object(h.ro,'append_json') as save:self.assertEqual(x.pg('unchanged',deadline=123),'unchanged')
  pg.assert_called_once_with('unchanged',deadline=123);save.assert_not_called()
 def test_observer_write_failure_cannot_replace_first_sql_failure(self):
  x=object.__new__(h.Chain);x.ledger_active=1;x.ledger_sql_index=0;x.work=Path('/owned');first=h.Blocked('owned SQL timeout')
  with mock.patch.object(h.ro.Chain,'pg',side_effect=first),mock.patch.object(h.ro,'append_json',side_effect=OSError('owned disk')),self.assertRaises(h.Blocked) as caught:x.pg('SELECT owned')
  self.assertIs(caught.exception,first);self.assertIn('ledger diagnostic secondary: OSError',first.__notes__)
 def test_original_query_keeps_all172_full_table_sorted_aggregates(self):
  names=['owned_table_'+str(i).zfill(3) for i in range(172)];query=h.other_public_query(names)
  digest="encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex')"
  self.assertEqual(query,'SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join('SELECT '+h.q(t)+' AS name,'+digest+' AS digest FROM public.'+t+' t' for t in names)+') hashes')
  self.assertEqual(query.count(' FROM public.'),172);self.assertNotIn('WHERE',query);self.assertNotIn('LIMIT',query);self.assertNotIn('GROUP BY',query)
  for name in names:self.assertEqual(query.count(' FROM public.'+name+' t'),1)
 def test_each_ledger_uses_one_original_query_without_extra_oracle(self):
  names=['owned_table_'+str(i).zfill(3) for i in range(172)];before={name:'a'*64 for name in names};changed=dict(before,owned_table_171='b'*64)
  x=object.__new__(h.Chain);x.other_public_status='unread';x.sql=mock.Mock(side_effect=[json.dumps(before),json.dumps(changed)])
  self.assertEqual(x.read_other_public(names),before);self.assertEqual(x.read_other_public(names),changed)
  self.assertEqual(x.sql.call_args_list,[mock.call(h.other_public_query(names),read=True)]*2);self.assertEqual(x.other_public_status,'verified')
 def test_missing_or_extra_table_fails_and_cannot_retry(self):
  before={'owned':'a'*64}
  for wrong in ({},dict(before,extra_table='a'*64)):
   with self.subTest(kind=len(wrong)):
    x=object.__new__(h.Chain);x.other_public_status='unread';x.sql=mock.Mock(return_value=json.dumps(wrong))
    with self.assertRaises(AssertionError):x.read_other_public(['owned'])
    with self.assertRaises(AssertionError):x.read_other_public(['owned'])
    self.assertEqual(x.sql.call_count,1);self.assertEqual(x.other_public_status,'failed')
 def test_original_query_failure_is_retained_without_retry(self):
  for state in ('unread','verified'):
   with self.subTest(state=state):
    x=object.__new__(h.Chain);x.other_public_status=state;first=h.Blocked('original 8s timeout');x.sql=mock.Mock(side_effect=first)
    with self.assertRaises(h.Blocked) as caught:x.read_other_public(['owned'])
    with self.assertRaises(AssertionError):x.read_other_public(['owned'])
    self.assertIs(caught.exception,first);x.sql.assert_called_once_with(h.other_public_query(['owned']),read=True);self.assertEqual(x.other_public_status,'failed')
 def test_failed_oracle_blocks_whole_ledger_before_any_parent_sql(self):
  x=object.__new__(h.Chain);x.other_public_status='failed'
  with mock.patch.object(h.pm.Chain,'ledger') as parent,self.assertRaises(AssertionError):x.read_ledger()
  parent.assert_not_called()
if __name__=='__main__':unittest.main()
