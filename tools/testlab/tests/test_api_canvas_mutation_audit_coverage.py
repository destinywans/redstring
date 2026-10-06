"""Offline actual transport/validator, full ledger and bounded recovery seams."""
import contextlib,copy,io,json,sys,tempfile,threading,unittest
from pathlib import Path
from unittest.mock import Mock,patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import api_canvas_mutation_audit_coverage as c
INV='INV-'+'c'*32;ROOT='d'*32;CHILD='e'*32;ACTOR='b'*32
T0='2026-10-06T00:00:00+00:00';T1='2026-10-06T00:00:01+00:00';T2='2026-10-06T00:00:02+00:00'
def before():
 v={t:[] for t in c.TABLES};v['business']={'sessions':'stable','evidence':'empty','runs':'empty'};v['investigations']=[dict(id=INV,application_id='app-own',environment_id='env-own',title='Owned',status='open',updated_at=T0)];v['branches']=[dict(id=ROOT,investigation_id=INV,parent_id=None,title='主调查',model_id='',revision=0,inherited_parent_revision=None,synced_parent_revision=None)];v['context_revisions']=[dict(branch_id=ROOT,revision=0,messages=[],provenance={'kind':'initial'},created_at=T0)];v['events']=[dict(seq=8,type='investigation.created',payload={'id':INV})];v['management_audit']=[dict(id=9,action='v3.investigation_canvas.created',chain_entry_hash='kept')];return v
def fork_body():return dict(parent_id=ROOT,title='Owned child',x=360,y=120,expected_parent_revision=0)
def layout_body():return dict(x=500,y=240,collapsed=True,expected_layout_version=0)
def node(id,parent,title,x,y,collapsed=False,version=0,at=T0):
 v=dict(id=id,kind='root' if parent is None else 'branch',title=title,subtitle='Main investigation' if parent is None else 'Branch',status='active',x=x,y=y,w=260,collapsed=collapsed,layout_version=version,context=dict(version='v0',tokens=None,messages=0,excludes=[]),versions=[dict(v='v0',text='Context revision 0',at=at)])
 if parent is not None:v['parent']=parent;v['context']['inherits']='v0'
 return v
def recovered(op,old):
 v=copy.deepcopy(old);v['investigations'][0]['updated_at']=T1 if op=='fork' else T2;body=fork_body() if op=='fork' else layout_body();target=ROOT if op=='fork' else CHILD;response=node(CHILD,ROOT,'Owned child',body['x'],body['y'],op=='layout',0 if op=='fork' else 1,T1)
 v['management_audit'].append(dict(id=12 if op=='fork' else 15,actor_id=ACTOR,actor_kind='user',application_id='app-own',action=c.ACTIONS[op],target=CHILD,outcome='success',changes=c.audit_changes(op,INV,target)));v['events'].append(dict(seq=14 if op=='fork' else 19,investigation_id=INV,branch_id=CHILD,type='branch.forked' if op=='fork' else 'branch.updated',payload=dict(parent_id=ROOT,parent_revision=0) if op=='fork' else dict(id=CHILD,layout_version=1)))
 if op=='fork':
  v['branches'].append(dict(id=CHILD,investigation_id=INV,parent_id=ROOT,title=body['title'],model_id='',revision=0,inherited_parent_revision=0,synced_parent_revision=0));v['context_revisions'].append(dict(branch_id=CHILD,revision=0,messages=[],provenance=dict(kind='fork',source_branch_id=ROOT,source_revision=0),created_at=T1));v['v3_investigation_node_layouts'].append(dict(branch_id=CHILD,x=360,y=120,w=260,collapsed=False,version=0,updated_by=ACTOR,updated_at=T1))
 else:v['v3_investigation_node_layouts'][0].update(x=500,y=240,collapsed=True,version=1,updated_at=T2)
 return v,response,body,target
def graph(v):
 nodes=[node(ROOT,None,'主调查',16,40)]
 if len(v['branches'])==2:
  l=v['v3_investigation_node_layouts'][0];nodes.append(node(CHILD,ROOT,'Owned child',l['x'],l['y'],l['collapsed'],l['version'],T1))
 return dict(investigation=dict(id=INV,title='Owned',status='active',env='test',participants=['owned reader']),loop=[],nodes=nodes,tray=[])
class CanvasMutationTests(unittest.TestCase):
 def setUp(self):self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.work=Path(self.tmp.name)
 def chain(self):
  x=c.Chain.__new__(c.Chain);x.work=self.work;x.nonce='a'*32;x.actor=ACTOR;x.app='app-own';x.envrow='env-own';x.inv=INV;x.users={'reader':ACTOR};x.cookies={'reader':'memory-cookie-never-save'};x.calls=0;x.raw_number=0;x.run_id='f'*32;x.lock=threading.Lock();x.contract=c.Contract(c.ROOT);x.frozen=Mock();x.owner=Mock();x.first_red=Mock();x.marker_case=Mock();x.database='own-db';x.db_user='own-role';x.mutation_fault=None;x.body={'title':'Owned'};x.owner_name='owned reader';x.diagnostic=Mock();return x
 def test_both_mutations_exact_full_rows_and_sequence_gaps_are_legal(self):
  b=before()
  for op in ('fork','layout'):
   a,value,body,target=recovered(op,b);c.verify_recovery(op,b,a,value,body,ACTOR,'app-own',INV,target);c.verify_graph(graph(a),a,INV,'Owned','owned reader');b=a
 def test_each_full_table_business_or_prior_audit_mutation_rejected(self):
  b=before()
  for op in ('fork','layout'):
   a,value,body,target=recovered(op,b)
   for table in c.TABLES:
    bad=copy.deepcopy(a);bad[table].append({'extra':'residue'})
    with self.assertRaises(AssertionError):c.verify_recovery(op,b,bad,value,body,ACTOR,'app-own',INV,target)
   for audit_key,val in (('outcome','succeeded'),('actor_id','foreign'),('application_id','foreign'),('target',ROOT),('changes',{})):
    bad=copy.deepcopy(a);bad['management_audit'][-1][audit_key]=val
    with self.assertRaises(AssertionError):c.verify_recovery(op,b,bad,value,body,ACTOR,'app-own',INV,target)
   for bad in (dict(a,business={'runs':'one'}),dict(a,management_audit=[dict(a['management_audit'][0],chain_entry_hash='changed'),*a['management_audit'][1:]])):
    with self.assertRaises(AssertionError):c.verify_recovery(op,b,bad,value,body,ACTOR,'app-own',INV,target)
   b=a
 def test_fork_parent_snapshot_and_layout_cas_context_invariants(self):
  b=before();a,value,body,target=recovered('fork',b)
  for table,key,val in (('branches','inherited_parent_revision',1),('context_revisions','messages',[{'role':'assistant'}]),('v3_investigation_node_layouts','version',1)):
   bad=copy.deepcopy(a);bad[table][-1][key]=val
   with self.assertRaises(AssertionError):c.verify_recovery('fork',b,bad,value,body,ACTOR,'app-own',INV,ROOT)
  b=a;a,value,body,target=recovered('layout',b)
  for table,key,val in (('branches','revision',1),('context_revisions','provenance',{'kind':'invented'}),('v3_investigation_node_layouts','version',2),('v3_investigation_node_layouts','updated_at',T0)):
   bad=copy.deepcopy(a);bad[table][-1][key]=val
   with self.assertRaises(AssertionError):c.verify_recovery('layout',b,bad,value,body,ACTOR,'app-own',INV,CHILD)
 def test_actual_call_error_validator_before_emit_false_registration_no_sensitive_wire(self):
  for op,method in (('fork','POST'),('layout','PATCH')):
   x=self.chain();path='/api/v1/investigations/'+INV+('/branches' if op=='fork' else '/nodes/'+CHILD);fake=Mock(return_value=(503,json.dumps({'error':dict(code='audit_unavailable',message='Branch audit unavailable' if op=='fork' else 'Node audit unavailable')}).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out:x.call('reader',method,path,{},want=503,code='audit_unavailable',scenario='failure',index=1);self.assertIs(c.base.transport,fake)
   self.assertTrue(all(json.loads(r)['interface']['registration_evidence'] is False for r in out.getvalue().splitlines()));raw=(self.work/'mutation-wire-1.json').read_text();self.assertNotIn('memory-cookie',raw);self.assertNotIn('response_body',raw);self.assertNotIn('Cookie',raw)
 def test_actual_call_frozen_failure_is_zero_transport_zero_emit(self):
  x=self.chain();x.frozen=Mock(side_effect=AssertionError('frozen source drift'));fake=Mock(return_value=(503,b'{}',{}))
  with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()) as out,self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/investigations/'+INV+'/branches',fork_body(),want=503,code='audit_unavailable',scenario='failure')
  x.frozen.assert_called_once();fake.assert_not_called();x.first_red.assert_not_called();self.assertEqual(x.calls,0);self.assertEqual(x.raw_number,0);self.assertEqual(out.getvalue(),'');self.assertEqual(list(self.work.iterdir()),[]);self.assertIsNot(c.base.transport,fake)
 def test_actual_call_wrong_error_body_fails_without_saving_arbitrary_body(self):
  x=self.chain();fake=Mock(return_value=(503,json.dumps({'error':dict(code='audit_unavailable',message='arbitrary-sensitive-provider-text')}).encode(),{'Content-Type':'application/json'}))
  with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('reader','POST','/api/v1/investigations/'+INV+'/branches',fork_body(),want=503,code='audit_unavailable',scenario='failure')
  x.first_red.assert_called_once();self.assertNotIn('arbitrary-sensitive', ''.join(p.read_text() for p in self.work.iterdir()));self.assertFalse((self.work/'failure-response-1.json').exists())
 def test_actual_success_callback_rejects_wrong_layout_context_before_pass(self):
  b,_response,_body,_target=recovered('fork',before());a,value,body,target=recovered('layout',b)
  for wrong in ('layout_version','context'):
   bad=copy.deepcopy(value);bad[wrong]=2 if wrong=='layout_version' else dict(version='v1');x=self.chain();fake=Mock(return_value=(200,json.dumps(bad).encode(),{'Content-Type':'application/json'}))
   with patch.object(c.base,'transport',fake),contextlib.redirect_stdout(io.StringIO()),self.assertRaises(AssertionError):x.call('reader','PATCH','/api/v1/investigations/'+INV+'/nodes/'+CHILD,body,index=1,validate=lambda v:c.verify_response('layout',v,body))
   x.first_red.assert_called_once()
 def test_graph_omitted_inherits_foreign_parent_wrong_persisted_time_rejected(self):
  a,_,_,_=recovered('fork',before());v=graph(a);c.verify_graph(v,a,INV,'Owned','owned reader')
  for key,val in (('parent',ROOT+'foreign'),('layout_version',9),('context',dict(version='v0',tokens=None,messages=0,excludes=[])),('versions',[dict(v='v0',text='Context revision 0',at=T0)])):
   bad=copy.deepcopy(v);bad['nodes'][1][key]=val
   with self.assertRaises(AssertionError):c.verify_graph(bad,a,INV,'Owned','owned reader')
 def test_exact_scoped_trigger_and_unknown_ddl_intent(self):
  for op,target in (('fork',ROOT),('layout',CHILD)):
   name,sql,drop=c.fault_statements('a'*32,ACTOR,'app-own',INV,target,op)
   for s in (ACTOR,'app-own',INV,target,c.ACTIONS[op],"NEW.outcome='success'","NEW.actor_kind='user'",'NEW.changes='):self.assertIn(s,sql)
   self.assertIn(name,drop)
  x=self.chain();x.sql=Mock(side_effect=OSError('unknownDDL'))
  with self.assertRaises(OSError):x.install_fault('fork',ROOT)
  self.assertEqual(x.mutation_fault,('fork',ROOT));self.assertEqual((self.work/'mutation-fault-intent.json').stat().st_mode&0o777,0o600)
  x=self.chain();x.sql=Mock()
  with patch.object(c.base,'_private',side_effect=OSError('privatewrite')),self.assertRaises(OSError):x.install_fault('layout',CHILD)
  self.assertEqual(x.mutation_fault,('layout',CHILD));x.sql.assert_not_called()
 def test_restore_three_steps_share_deadline_and_remaining_fault_keeps_intent(self):
  x=self.chain();x.mutation_fault=('fork',ROOT);x.pg=Mock(side_effect=['',json.dumps(dict(trigger=False,function=False))])
  with patch.object(c.time,'monotonic',return_value=100):x.remove_fault(deadline=102)
  x.owner.assert_called_once_with(deadline=102);self.assertEqual([r.kwargs['deadline'] for r in x.pg.call_args_list],[102,102]);self.assertIsNone(x.mutation_fault);x.frozen.assert_not_called()
  x=self.chain();x.mutation_fault=('layout',CHILD);x.pg=Mock(side_effect=['',json.dumps(dict(trigger=True,function=False))])
  with self.assertRaises(AssertionError):x.remove_fault()
  self.assertEqual(x.mutation_fault,('layout',CHILD))
 def test_primary_failure_not_overwritten_restore_failure_cleanup_still_same40(self):
  x=self.chain();primary=AssertionError('first-ledger-failure');x.ledger=Mock(return_value=before());x.readback_graph=graph(before());x.install_fault=Mock();x.call=Mock(side_effect=primary);x.remove_fault=Mock(side_effect=OSError('restore'))
  with self.assertRaises(AssertionError) as result:x.exercise('fork',ROOT,fork_body())
  self.assertIs(result.exception,primary);x.diagnostic.assert_called_once();x.marker_case.assert_not_called()
  x=self.chain();x.remove_fault=Mock(side_effect=primary)
  with patch.object(c.h,'bounded_cleanup') as cleanup,patch.object(c.time,'monotonic',return_value=100),self.assertRaises(AssertionError) as result:x.cleanup()
  cleanup.assert_called_once_with(x);self.assertEqual(x.cleanup_deadline,140);x.remove_fault.assert_called_once_with(deadline=104);self.assertIs(result.exception,primary)
 def test_readback_two_APIs_and_any_refusal_ledger_delta_rejected(self):
  x=self.chain();v=before();x.call=Mock(return_value=(graph(v),{}));x.ledger=Mock(return_value=v);self.assertEqual(x.readback(v),graph(v));self.assertEqual([r.kwargs['index'] for r in x.call.call_args_list],[0,1])
  for key in (*c.TABLES,'business'):
   bad=copy.deepcopy(v);bad[key]=[] if v[key] else [{'residue':True}]
   if key=='business':bad[key]={'runs':'one'}
   with self.assertRaises(AssertionError):c.h.same(v,bad)
 def test_inventory_and_transitive_identity_no_resources_normal0(self):
  with patch.object(c,'doctor') as doctor,contextlib.redirect_stdout(io.StringIO()) as out:self.assertEqual(c.main(['--inventory']),0)
  doctor.assert_not_called();v=json.loads(out.getvalue());self.assertEqual(v['estimated_HTTP_calls'],18);self.assertEqual(v['normal_added'],0);self.assertFalse(v['registration_evidence']);self.assertEqual(v['cleanup_seconds'],40);self.assertEqual(len(c.REQUIRED_CASES),len(set(c.REQUIRED_CASES)));self.assertIn('components.py',c.hashes());self.assertEqual(c._ns['MODULE'],c.MODULE);self.assertEqual(c._ns['PORTS'],c.PORTS)
  good=dict(source_tree_sha=c.SOURCE,binary_sha256=c.BINARY,runtime_sha=c.RUNTIME,helper_sha256=c.hashes());c.h.frozen_identity(good)
  with self.assertRaises(AssertionError):c.h.frozen_identity(dict(good,runtime_sha='drift'))
if __name__=='__main__':unittest.main()
