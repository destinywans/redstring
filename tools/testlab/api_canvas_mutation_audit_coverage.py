"""Prepared formal investigation fork/layout audit rollback; no model or chain.

Only root --live admission acquires an owned UUID database and two API children.
SQL creates synthetic ACL and exact audit faults, never canvas business rows.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,stat,textwrap,time
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-canvas-mutation-audit';PORTS=(18888,18889);HTTP_PLAN=18
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
CREATION_SHA='9f64c2f581eebd723b1bbc5711bcb3f4b833955a453bc518f8c744deb26f239b'
spec=importlib.util.spec_from_file_location('canvas_mutation_private_creation',ROOT/'tools/testlab/api_canvas_creation_audit_coverage.py');h=importlib.util.module_from_spec(spec);spec.loader.exec_module(h)
base=h.base;check,Blocked,q=h.check,h.Blocked,h.q
TABLES=(*h.TABLES,'v3_investigation_node_layouts');h.TABLES=TABLES;h.MODULE=MODULE;h.PORTS=PORTS;base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('POST','/investigations/{id}/branches'),('PATCH','/investigations/{id}/nodes/{node}'))
base.TARGETS=TARGETS;base.SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/canvases'),('GET','/investigations/{id}/canvas'))
REQUIRED_CASES=('canvas_mutations.http_service','canvas_mutations.created')+tuple('canvas_mutations.'+op+'.'+stage for op in ('fork','layout') for stage in ('rollback','fault_removed','recovery','readback'))+('canvas_mutations.ledger','canvas_mutations.cleanup','management.fresh_database','management.synthetic_identities','management.api_ready.0','management.api_ready.1','management.cleanup','management.complete')
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_canvas_mutation_audit_coverage.py','tests/test_api_canvas_mutation_audit_coverage.py','api_canvas_creation_audit_coverage.py','tests/test_api_canvas_creation_audit_coverage.py','api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalCanvasMutationAudit',proof_scope='human blank investigation fork/layout audit rollback and samebody CAS recovery only; normal0; no inference/template/whole acceptance')
h.evidence=evidence;emit=h.emit;Contract=h.Contract
_source=textwrap.dedent(inspect.getsource(h.admission))
for old,new in (('root-admitted-canvas-creation-audit','root-admitted-canvas-mutation-audit'),('canvas_creation_audit_fault_authorized','canvas_mutation_audit_fault_authorized')):
 check(_source.count(old)==1,'admission anchor drift');_source=_source.replace(old,new)
_ns=dict(h.__dict__);exec(compile(_source,str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission'];h.admission=admission
def doctor(root):check(digest(ROOT/'tools/testlab/api_canvas_creation_audit_coverage.py')==CREATION_SHA,'passed lifecycle drift');return h.doctor(root)
ACTIONS={'fork':'v3.investigation_canvas.branch_created','layout':'v3.investigation_canvas.node_updated'}
def audit_changes(op,inv,parent):
 return dict(investigation_id=inv,environment_id='test',parent_id=parent,parent_revision=0) if op=='fork' else dict(investigation_id=inv,environment_id='test',old_layout_version=0,new_layout_version=1,title_changed=False,position_changed=True,collapse_changed=True)
def fault_statements(nonce,actor,app,inv,target,op):
 check(op in ACTIONS and all(re.fullmatch('[a-f0-9]{32}',v) for v in (nonce,actor,target)) and re.fullmatch('INV-[a-f0-9]{32}',inv) and re.fullmatch('[A-Za-z0-9_-]+',app),'exact own actor/app/investigation/node/action required');name='canvas_mutation_fail_'+nonce
 lineage=('EXISTS(SELECT 1 FROM branches b WHERE b.id=NEW.target AND b.investigation_id='+q(inv)+' AND b.parent_id='+q(target)+' AND b.revision=0 AND b.inherited_parent_revision=0 AND b.synced_parent_revision=0)') if op=='fork' else 'NEW.target='+q(target)
 create='BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+" AND NEW.actor_kind='user' AND NEW.application_id="+q(app)+' AND NEW.action='+q(ACTIONS[op])+" AND NEW.outcome='success' AND NEW.changes="+q(json.dumps(audit_changes(op,inv,target)))+'::jsonb AND '+lineage+" THEN RAISE EXCEPTION 'owned canvas mutation audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;'
 return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def stamp(v):return datetime.datetime.fromisoformat(v.replace('Z','+00:00'))
def verify_graph(value,ledger,inv,title,owner):
 check(set(value)=={'investigation','loop','nodes','tray'} and value['investigation']==dict(id=inv,title=title,status='active',env='test',participants=[owner]) and value['loop']==[] and value['tray']==[],'full graph scope/evidence differs');branches=ledger['branches'];check(len(value['nodes'])==len(branches),'graph lost/added node');by={n['id']:n for n in value['nodes']};check(len(by)==len(branches),'duplicate graph node')
 for b in branches:
  check(b['investigation_id']==inv and b['revision']==0 and b['model_id']=='','unexpected lineage/context/model');layouts=[r for r in ledger['v3_investigation_node_layouts'] if r['branch_id']==b['id']];check(len(layouts)<=1,'duplicate layout');l=layouts[0] if layouts else dict(x=16,y=40,w=260,collapsed=False,version=0);parent=b['parent_id'];context=dict(version='v0',tokens=None,messages=0,excludes=[])
  if parent is not None:context['inherits']='v0'
  revisions=[r for r in ledger['context_revisions'] if r['branch_id']==b['id']];check(len(revisions)==1 and revisions[0]['revision']==0 and revisions[0]['messages']==[],'exact persisted empty context required');node=by.get(b['id']);check(isinstance(node,dict),'actual branch absent from graph');versions=node.get('versions');check(isinstance(versions,list) and len(versions)==1 and set(versions[0])=={'v','text','at'} and versions[0]['v']=='v0' and versions[0]['text']=='Context revision 0' and stamp(versions[0]['at'])==stamp(revisions[0]['created_at']),'persisted v0 version/time differs');wanted=dict(id=b['id'],kind='root' if parent is None else 'branch',title=b['title'],subtitle='Main investigation' if parent is None else 'Branch',status='active',x=l['x'],y=l['y'],w=l['w'],collapsed=l['collapsed'],layout_version=l['version'],context=context,versions=versions)
  if parent is not None:wanted['parent']=parent
  check(node==wanted,'exact node/layout/context projection differs')
def verify_response(op,value,body):
 check(isinstance(value,dict) and re.fullmatch('[a-f0-9]{32}',value.get('id','')) and value.get('kind')=='branch' and value.get('status')=='active' and value.get('x')==body['x'] and value.get('y')==body['y'] and value.get('layout_version')==(0 if op=='fork' else 1) and value.get('context')==dict(version='v0',inherits='v0',tokens=None,messages=0,excludes=[]),'mutation response did not preserve empty inherited context/CAS')
 check(value.get('collapsed')==(False if op=='fork' else True),'collapse differs')
 if op=='fork':check(value.get('parent')==body['parent_id'] and value.get('title')==body['title'],'fork response lineage differs')
def verify_recovery(op,before,after,value,body,actor,app,inv,target):
 verify_response(op,value,body);check(before['business']==after['business'] and before['v3_canvases']==after['v3_canvases'],'unrelated complete public ledger changed');check(len(before['investigations'])==len(after['investigations'])==1,'investigation count changed');old,new=before['investigations'][0],after['investigations'][0];check(dict(old,updated_at=new['updated_at'])==new and stamp(new['updated_at'])>stamp(old['updated_at']),'mutation changed investigation beyond advancing update time')
 audit=h.additions(before['management_audit'],after['management_audit'],1)[0];check(all(audit.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id=app,action=ACTIONS[op],target=value['id'],outcome='success',changes=audit_changes(op,inv,target)).items()),'exact successful audit differs');event=h.additions(before['events'],after['events'],1)[0];check(all(event.get(k)==v for k,v in dict(investigation_id=inv,branch_id=value['id'],type='branch.forked' if op=='fork' else 'branch.updated',payload=dict(parent_id=target,parent_revision=0) if op=='fork' else dict(id=target,layout_version=1)).items()),'exact event differs')
 if op=='fork':
  b=h.additions(before['branches'],after['branches'],1)[0];check(all(b.get(k)==v for k,v in dict(id=value['id'],investigation_id=inv,parent_id=target,title=body['title'],model_id='',revision=0,inherited_parent_revision=0,synced_parent_revision=0).items()),'fork durable branch differs');ctx=h.additions(before['context_revisions'],after['context_revisions'],1)[0];check(all(ctx.get(k)==v for k,v in dict(branch_id=value['id'],revision=0,messages=[],provenance=dict(kind='fork',source_branch_id=target,source_revision=0)).items()),'fork context/provenance differs');l=h.additions(before['v3_investigation_node_layouts'],after['v3_investigation_node_layouts'],1)[0];check(all(l.get(k)==v for k,v in dict(branch_id=value['id'],x=body['x'],y=body['y'],w=260,collapsed=False,version=0,updated_by=actor).items()),'fork saved layout differs')
 else:
  check(value['id']==target and before['branches']==after['branches'] and before['context_revisions']==after['context_revisions'],'layout changed branch/context');old=before['v3_investigation_node_layouts'];new=after['v3_investigation_node_layouts'];check(len(old)==len(new)==1 and dict(old[0],x=body['x'],y=body['y'],collapsed=True,version=1,updated_by=actor,updated_at=new[0]['updated_at'])==new[0] and stamp(new[0]['updated_at'])>stamp(old[0]['updated_at']),'exact full saved layout CAS differs')
class Chain(h.Chain):
 def __init__(self,*args):
  super().__init__(*args);self.mutation_fault=None;self.state.update(suite=MODULE,targets=TARGETS,normal_added=0);base._private(self.work/'state.json',self.state)
 def frozen(self):check(digest(ROOT/'tools/testlab/api_canvas_creation_audit_coverage.py')==CREATION_SHA,'passed lifecycle drift');super().frozen()
 def marker_case(self,name,value):
  check('canvas_mutations.'+name in REQUIRED_CASES,'unknown mutation marker');self.frozen();emit('canvas_mutations.'+name,'passed',dict(run_id=self.run_id,**value))
 def call(self,*args,**kwargs):
  self.frozen()
  original=base.transport
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 changed');status,raw,hs=result=original(origin,method,path,payload,headers,timeout)
   if path.startswith('/api/v1/investigations/') or path=='/api/v1/canvases':
    check(len(raw)<=65536,'bounded own canvas response required');self.raw_number+=1;record=dict(method=method,path=path,API_index=base.BASES.index(origin),status=status,request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),source_tree_sha=SOURCE,binary_sha256=BINARY)
    if status<400:record['response_body']=json.loads(raw);record['request_body']=None if payload is None else json.loads(payload)
    base._private(self.work/('mutation-wire-'+str(self.raw_number)+'.json'),record)
    if status>=400:check(status==503 and kwargs.get('want')==503 and kwargs.get('code')=='audit_unavailable' and base.header(hs,'Content-Type').split(';')[0]=='application/json' and json.loads(raw)=={'error':dict(code='audit_unavailable',message='Branch audit unavailable' if method=='POST' else 'Node audit unavailable')},'exact sanitized mutation audit failure differs')
   return result
  base.transport=wire
  try:return base.Chain.call(self,*args,**kwargs)
  finally:base.transport=original
 def install_fault(self,op,target):
  self.frozen();self.owner();check(self.mutation_fault is None,'one exact mutation fault only');self.mutation_fault=(op,target);base._private(self.work/'mutation-fault-intent.json',dict(actor=self.actor,app=self.app,investigation=self.inv,op=op,target=target));self.sql(fault_statements(self.nonce,self.actor,self.app,self.inv,target,op)[1])
 def remove_fault(self,deadline=None):
  if self.mutation_fault is None:return
  deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4;self.owner(deadline=deadline);op,target=self.mutation_fault;name,_,drop=fault_statements(self.nonce,self.actor,self.app,self.inv,target,op);self.pg(drop,self.database,self.db_user,deadline=deadline);query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)";check(json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))==dict(trigger=False,function=False),'owned mutation fault remains');self.mutation_fault=None
 def readback(self,ledger,old=None):
  values=[]
  for index in (0,1):
   value,_=self.call('reader','GET','/api/v1/investigations/'+self.inv+'/canvas',index=index,validate=lambda v:verify_graph(v,ledger,self.inv,self.body['title'],self.owner_name));values.append(value);h.same(ledger,self.ledger())
  check(values[0]==values[1] and (old is None or values[0]==old),'twoAPI/full old graph differs');return values[0]
 def exercise(self,op,target,body):
  before=self.ledger();old=self.readback_graph;path='/api/v1/investigations/'+self.inv+('/branches' if op=='fork' else '/nodes/'+target);method='POST' if op=='fork' else 'PATCH';primary=None
  try:
   self.install_fault(op,target);self.call('reader',method,path,body,want=503,code='audit_unavailable',scenario='failure');failed=self.ledger();self.save_ledger(op+'-failed',failed);h.same(before,failed);self.readback(before,old);self.marker_case(op+'.rollback',dict(status=503,full_seven_tables_and_other_public_unchanged=True,same_CAS=True))
  except BaseException as error:primary=error
  finally:
   try:self.remove_fault()
   except BaseException as error:
    if primary is None:primary=error
    else:self.secondary(self.work/(op+'-restore-secondary.json'),error,primary)
  if primary is not None:raise primary
  self.marker_case(op+'.fault_removed',dict(trigger=False,function=False));value,_=self.call('reader',method,path,body,want=201 if op=='fork' else 200,index=1,scenario='failure',validate=lambda v:verify_response(op,v,body));after=self.ledger();verify_recovery(op,before,after,value,body,self.actor,self.app,self.inv,target);self.save_ledger(op+'-recovered',after);self.marker_case(op+'.recovery',dict(same_body=True,same_CAS=True,API_index=1,exact_audit_delta=1,explicit_no_retry=True));self.readback_graph=self.readback(after);check(next(n for n in self.readback_graph['nodes'] if n['id']==value['id'])==value,'write response differs from exact persistent graph');self.marker_case(op+'.readback',dict(two_APIs=True,full_DTO=True,no_read_delta=True));return value['id']
 def run(self):
  self.provision();self.start();self.seed()
  for actor in ('admin','reader'):self.login(actor)
  self.inputs();before=self.ledger();self.body=self.payload('investigation');value,_=self.call('reader','POST','/api/v1/canvases',self.body,want=201,purpose='fixture');check(set(value)=={'id','href'} and value['href']=='/canvas/'+value['id'],'formal create response differs');self.inv=value['id'];created=self.ledger();rows=h.verify_creation(before,created,self.body,self.actor,self.app,self.envrow,self.inv);self.save_ledger('created',created);self.readback_graph=self.readback(created);root=rows['branches'][0]['id'];check(self.readback_graph['nodes'][0]['id']==root,'actual blank root differs');self.marker_case('created',dict(ai_draft=False,HTTP201=True,root_revision=0,SQL_business_writes=0))
  child=self.exercise('fork',root,dict(parent_id=root,title=self.marker+' child',x=360,y=120,expected_parent_revision=0));self.exercise('layout',child,dict(x=500,y=240,collapsed=True,expected_layout_version=0));check(self.calls==HTTP_PLAN,'reviewed18 HTTP scope differs');self.marker_case('ledger',dict(model_execution_notification=False,normal_added=0));result=dict(run_id=self.run_id,http_calls=self.calls,required_cases=REQUIRED_CASES,helper_sha256=hashes(),source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False);base._private(self.work/'canvas-mutation-result.json',result);return result
 def cleanup(self):
  deadline=time.monotonic()+40;primary=None
  try:self.remove_fault(deadline=min(deadline,time.monotonic()+4))
  except BaseException as error:primary=error
  finally:
   self.cleanup_deadline=deadline
   try:h.bounded_cleanup(self)
   except BaseException as error:
    if primary is None:primary=error
    else:self.secondary(self.work/'cleanup-secondary.json',error,primary)
  if primary is not None:raise primary
  for port in PORTS:
   check(time.monotonic()<deadline,'shared40 cleanup exhausted')
   with socket.socket() as sock:sock.settimeout(.2);check(sock.connect_ex(('127.0.0.1',port))!=0,'own mutation port remains')
  emit('canvas_mutations.cleanup','passed',dict(single40=True,restore_subset4=True,grace45=True,owned_UUID_only=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=HTTP_PLAN,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45,containers=0,models=0,normal_added=0,registration_evidence=False)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root --live/run-id/admission required')
 fd=None
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600);st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'own regular0600 lifecycle lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
  with base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('canvas_mutations.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as error:emit('canvas_mutations.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
 finally:
  if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
