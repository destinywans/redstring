"""Offline actual compiled-call, write ledger and restoration counterexamples only."""
import contextlib,copy,hashlib,io,json,sys,tempfile,unittest,uuid
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_runbook_write_boundaries_coverage as c
RID='b'*32;ACTOR='c'*32
def ledger():
 v={t:[] for t in c.TABLES};v['business']={'other':'stable','source':'unchanged'};v['knowledge_documents']=[dict(id=RID,version=1,lifecycle='draft',body='unchanged',metadata={'maintainer_id':ACTOR})];v['v3_runbooks']=[{'document_id':RID,'steps':'stable'}];v['knowledge_revisions']=[{'document_id':RID,'version':1}];v['knowledge_history']=[dict(id=1,operation='v3.runbook.created')];v['management_audit']=[dict(id=1,action='runbook.created')];return v
def recovery(op,before):
 after=copy.deepcopy(before);v=dict(id='d'*32,steps=1,ok=False,mode='validation_only',checks=c.CHECKS) if op=='dry-run' else dict(ok=True,status='review');after['management_audit'].append(dict(id=2,actor_id=ACTOR,application_id='',action=c.ACTIONS[op],target=RID,outcome='succeeded',changes=c.changes(op)))
 if op=='dry-run':after['v3_runbook_validation_previews'].append(dict(id=v['id'],document_id=RID,document_version=1,actor_id=ACTOR,step_count=1,readonly_valid=False,checks=c.CHECKS))
 else:after['knowledge_documents'][0]['lifecycle']='review';after['knowledge_history'].append(dict(id=2,document_id=RID,version=1,actor_id=ACTOR,operation='lifecycle.review'))
 return after,v
class RunbookWriteTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.run_id=str(uuid.uuid4());x.users={'reader':ACTOR,'peer':'e'*32,'viewonly':'f'*32};x.cookies={'reader':'memory-cookie-secret','viewonly':'memory-cookie-secret'};x.calls=0;x.first_red=Mock();x.frozen=Mock();x.marker_case=Mock();x.contract=c.Contract(c.ROOT);x.database='own-db';x.db_user='own-role';x.owner_tag='own-tag';x.fault_intent=None;return x
 def actual_call(self,x,status,code,msg,suffix='dry-run',v=None,validate=None):
  value={'error':dict(code=code,message=msg)} if v is None else v;fake=Mock(return_value=(status,json.dumps(value).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:
   result=x.call('reader','POST','/api/v1/runbooks/'+RID+'/'+suffix,{},want=status,code=code,validate=validate,index=1);self.assertIs(c.base.transport,fake);self.assertIs(c.h._namespace['transport'],fake)
  return result,fake,[json.loads(v) for v in out.getvalue().splitlines()]
 def test_actual_compiled_refusal_callback_raw_wire_false_and_no_cookie(self):
  x=self.chain();_,fake,events=self.actual_call(x,404,'not_found','Runbook not found');fake.assert_called_once();x.first_red.assert_not_called();self.assertTrue(all(r['interface']['registration_evidence'] is False for r in events));raw=(self.work/'runbook-wire-1.json').read_text();self.assertNotIn('memory-cookie',raw);self.assertNotIn('Cookie',raw);self.assertEqual(json.loads(raw)['API_index'],1)
 def test_wrong_refusal_message_or_reflection_is_firstred_before_pass(self):
  for v in ({'error':dict(code='not_found',message='other actor detail')},{'error':dict(code='not_found',message='Runbook not found'),'owner':'leaked'}):
   x=self.chain()
   with self.assertRaises(AssertionError):self.actual_call(x,404,'not_found','',v=v)
   x.first_red.assert_called_once()
 def test_distinct_handler_errors_403_409_503_exact_and_not_assumed_same_status(self):
  for (status,code),messages in c.ERRORS.items():
   for message in messages:self.actual_call(self.chain(),status,code,message)
  fake=Mock(return_value=(403,json.dumps({'error':dict(code='forbidden',message='Runbook management denied')}).encode(),{'Content-Type':'application/json'}));x=self.chain()
  with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('viewonly','POST','/api/v1/runbooks/'+RID+'/dry-run',{},want=404,code='not_found')
  x.first_red.assert_called_once()
 def test_real_success_validator_seam_rejects_execution_or_wrong_lifecycle(self):
  for op in ('dry-run','submit'):
   before=ledger();after,value=recovery(op,before);self.actual_call(self.chain(),200,None,'',op,v=value,validate=lambda v:c.verify_response(op,v));bad=copy.deepcopy(value)
   if op=='submit':bad['status']='published'
   else:bad['checks'][0]['executed']=True
   x=self.chain()
   with self.assertRaises(AssertionError):self.actual_call(x,200,None,'',op,v=bad,validate=lambda v:c.verify_response(op,v))
   x.first_red.assert_called_once()
 def test_recovery_full_rows_and_exact_audit_both_routes(self):
  for op in ('dry-run','submit'):
   before=ledger();after,value=recovery(op,before);c.verify_recovery(op,before,after,RID,ACTOR,value)
   for table in c.TABLES:
    bad=copy.deepcopy(after);bad[table].append({'unexpected':'residue'})
    with self.assertRaises(AssertionError):c.verify_recovery(op,before,bad,RID,ACTOR,value)
   bad=copy.deepcopy(after);bad['business']['source']='changed'
   with self.assertRaises(AssertionError):c.verify_recovery(op,before,bad,RID,ACTOR,value)
 def test_formal_blank_create_prerequisite_has_no_source_or_execution_rows(self):
  before={t:[] for t in c.TABLES};before['business']={'other':'stable'};after=copy.deepcopy(before);body=c.payload('a'*32);render='# '+body['title']+'\n\n## 1. '+body['steps'][0]['title']+'\n'+body['steps'][0]['body'];meta=dict(maintainer_id=ACTOR,origin='human',actor_id=ACTOR,visibility='workspace',environment_ids=['test'])
  after['knowledge_documents']=[dict(id=RID,application_id=None,title=body['title'],body=render,source='runbook://v3/'+RID,version=1,content_hash=hashlib.sha256(render.encode()).hexdigest(),created_by=ACTOR,lifecycle='draft',metadata=meta,active_generation=None)];after['v3_runbooks']=[dict(document_id=RID,source_kind='blank',source_ref='',conversation_id=None,steps=body['steps'],source_snapshot=dict(source_id='',source_kind='blank',content_sha256=''),reviewers=[],when_text='',last_verified_at=None)];after['knowledge_revisions']=[dict(document_id=RID,version=1,created_by=ACTOR,snapshot=dict(body=render,metadata=meta,lifecycle='draft'))];after['knowledge_history']=[dict(document_id=RID,version=1,actor_id=ACTOR,operation='v3.runbook.created')];after['management_audit']=[dict(actor_id=ACTOR,application_id='',action='runbook.created',target=RID,outcome='succeeded',changes=dict(status='draft',source='blank',environment_id='test'))];c.verify_created(before,after,RID,ACTOR,body)
  for table,key,value in (('knowledge_documents','body','different bytes'),('v3_runbooks','conversation_id','fake historical source'),('knowledge_revisions','version',2),('knowledge_history','operation','execution.finished'),('management_audit','outcome','success')):
   bad=copy.deepcopy(after);bad[table][0][key]=value
   with self.assertRaises(AssertionError):c.verify_created(before,bad,RID,ACTOR,body)
 def test_wrong_success_audit_outcome_actor_target_or_changes_cannot_pass(self):
  for op in ('dry-run','submit'):
   before=ledger();after,value=recovery(op,before)
   for key,val in (('outcome','success'),('actor_id','other'),('target','other'),('changes',{'version':2,'environment_id':'test'})):
    bad=copy.deepcopy(after);bad['management_audit'][-1][key]=val
    with self.assertRaises(AssertionError):c.verify_recovery(op,before,bad,RID,ACTOR,value)
 def test_refused_or_failed_any_full_table_or_other_public_change_fails(self):
  before=ledger()
  for key in (*c.TABLES,'business'):
   bad=copy.deepcopy(before);bad[key]=[dict(changed='bytes/revision/audit')] if key!='business' else {'changed':'hash'}
   with self.assertRaises(AssertionError):c.h.same(before,bad)
 def test_exact_fault_scope_action_outcome_target_actor_and_changes(self):
  for op in c.ACTIONS:
   name,sql,drop=c.fault_statements('a'*32,ACTOR,RID,op);self.assertIn("NEW.outcome='succeeded'",sql);self.assertIn('NEW.actor_id='+c.q(ACTOR),sql);self.assertIn('NEW.target='+c.q(RID),sql);self.assertIn('NEW.action='+c.q(c.ACTIONS[op]),sql);self.assertIn(c.q(json.dumps(c.changes(op)))+'::jsonb',sql);self.assertIn('DROP TRIGGER IF EXISTS '+name,drop)
  with self.assertRaises(AssertionError):c.fault_statements('a'*32,ACTOR,RID,'execute')
 def test_unknown_ddl_install_keeps_intent_until_exact_owner_recovery(self):
  x=self.chain();x.owner=Mock();x.sql=Mock(side_effect=OSError('ownedDDLunknown'))
  with self.assertRaises(OSError):x.install_fault(RID,'submit')
  self.assertEqual(x.fault_intent,(RID,'submit'));self.assertTrue((self.work/'fault-intent.json').exists());x.owner_deadline=Mock();x.pg=Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))]);deadline=c.time.monotonic()+4;x.remove_fault(deadline);self.assertIsNone(x.fault_intent);x.owner_deadline.assert_called_once_with(deadline);self.assertTrue(all(v.kwargs['deadline']==deadline for v in x.pg.call_args_list))
 def test_recovery_failure_retains_intent_and_cleanup_still_runs_with_same40(self):
  x=self.chain();x.fault_intent=(RID,'dry-run');x.owner_deadline=Mock(side_effect=AssertionError('own tags unavailable'))
  with patch.object(c,'bounded_cleanup') as cleanup,self.assertRaises(AssertionError):x.cleanup()
  cleanup.assert_called_once_with(x);self.assertEqual(x.fault_intent,(RID,'dry-run'));self.assertLessEqual(x.owner_deadline.call_args.args[0],x.cleanup_deadline);self.assertLessEqual(x.owner_deadline.call_args.args[0]-c.time.monotonic(),4)
 def test_detail_readback_is_two_API_full_DTO_and_no_mutation(self):
  x=self.chain();before=ledger();body=c.payload('a'*32);dto=dict(id=RID,title=body['title'],version='v1',status='draft',base='blank',source_kind='blank',source_ref='',owner='own reader',scope='workspace · env:test',environment_ids=['test'],application_id='',team_id='',tags=[],when='',steps=body['steps'],reviewers=[],usage=dict(refs=None,resolve_rate=None,weekly=None,highlight=None),incidents=None,last_verified=None,complete_v3_dto=False,partial=True,unknown_fields=['usage.refs','usage.resolve_rate','usage.weekly','usage.highlight','incidents','pending_reviewers','execution_verification','pending_reviewers'],versions=[dict(v='v1',who='own reader',when='time')]);c.verify_detail(dto,RID,body,'draft','own reader');bad=copy.deepcopy(dto);bad['last_verified']='invented'
  with self.assertRaises(AssertionError):c.verify_detail(bad,RID,body,'draft','own reader')
  x.marker='own';x.ledger=Mock(return_value=before);x.call=Mock(return_value=(dto,{}));self.assertEqual(x.readback(RID,body,'draft',before,dto),dto);self.assertEqual([v.kwargs['index'] for v in x.call.call_args_list],[0,1])
 def test_object_boundary_actual_call_has_nonce_missing_ID_and_authorized_staging_wire(self):
  grants=[dict(module=m,action=a,scope_kind='environment',scope_id='staging') for m,a in (('environment','view'),('knowledge','view'),('knowledge','manage'))]
  for op in ('dry-run','submit'):
   x=self.chain();before=ledger();x.sql=Mock(side_effect=[json.dumps(grants),'f']);x.ledger=Mock(return_value=before);fake=Mock(return_value=(404,json.dumps({'error':dict(code='not_found',message='Runbook not found')}).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()) as out:x.object_boundaries(op,RID,before)
   unknown=uuid.uuid5(uuid.UUID(hex=x.nonce),'missing-write-runbook').hex;self.assertNotEqual(unknown,RID);self.assertEqual([v.args[2] for v in fake.call_args_list],['/api/v1/runbooks/'+unknown+'/'+op,'/api/v1/runbooks/'+RID+'/'+op]);self.assertEqual([v.args[4]['X-AIOps-Environment'] for v in fake.call_args_list],['test','staging']);self.assertTrue(all(v.args[4]['Cookie']=='memory-cookie-secret' for v in fake.call_args_list));self.assertEqual(x.calls,2);self.assertTrue(all(json.loads(v)['interface']['registration_evidence'] is False for v in out.getvalue().splitlines()));self.assertEqual([v.args[0] for v in x.marker_case.call_args_list],[op+'.unknown_object',op+'.wrong_environment']);self.assertEqual(json.loads((self.work/'runbook-wire-2.json').read_text())['environment_id'],'staging')
 def test_object_boundary_missing_scope_existing_nonce_wrong_status_or_ledger_change_fail(self):
  grants=[dict(module=m,action=a,scope_kind='environment',scope_id='staging') for m,a in (('environment','view'),('knowledge','view'),('knowledge','manage'))]
  for rows,exists in ((grants[:-1],'f'),(grants,'t')):
   x=self.chain();x.sql=Mock(side_effect=[json.dumps(rows),exists]);x.call=Mock()
   with self.assertRaises(AssertionError):x.object_boundaries('submit',RID,ledger())
   x.call.assert_not_called();x.marker_case.assert_not_called()
  for status in (403,422):
   x=self.chain();x.sql=Mock(side_effect=[json.dumps(grants),'f']);fake=Mock(return_value=(status,json.dumps({'error':dict(code='forbidden',message='Runbook management denied')}).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.base,'transport',fake),patch.dict(c.h._namespace,transport=fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.object_boundaries('dry-run',RID,ledger())
   x.first_red.assert_called_once();x.marker_case.assert_not_called()
  x=self.chain();x.sql=Mock(side_effect=[json.dumps(grants),'f']);x.call=Mock();bad=ledger();bad['management_audit'].append({'unexpected':'write'});x.ledger=Mock(return_value=bad)
  with self.assertRaises(AssertionError):x.object_boundaries('submit',RID,ledger())
  x.marker_case.assert_not_called()
 def test_inventory_no_resources_unique_required_and_27HTTP(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  v=json.loads(out.getvalue());doctor.assert_not_called();self.assertFalse(v['registration_evidence']);self.assertEqual(v['normal_added'],0);self.assertEqual(v['estimated_HTTP_calls'],27);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)));self.assertEqual(v['cleanup_seconds'],40)
if __name__=='__main__':unittest.main()
