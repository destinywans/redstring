"""Runbook validation-preview/submit entry boundaries and exact audit rollback.
Blank human HTTP Runbook; no connector/tool/model/executor or notification.
"""
from __future__ import annotations
import argparse,copy,hashlib,importlib.util,inspect,json,re,socket,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-runbook-write-boundaries';PORTS=(18876,18877)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb';BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70';RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d';PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
BASE_HELPER_SHA='6501cb3c5e9603d8071731b963eb92e19c1a65d286326ecff8639e3bfd829d11'
spec=importlib.util.spec_from_file_location('runbook_write_private_lifecycle',ROOT/'tools/testlab/api_conversation_object_session_coverage.py');h=importlib.util.module_from_spec(spec);spec.loader.exec_module(h)
base=h.base;check,Blocked,q=h.check,h.Blocked,h.q;GenericContract=h.Contract.__bases__[0]
TABLES=('knowledge_documents','v3_runbooks','knowledge_revisions','knowledge_history','v3_runbook_validation_previews','management_audit');h.TABLES=TABLES;h.MODULE=MODULE;h.PORTS=PORTS;base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);h._namespace['BASES']=base.BASES
TARGETS=(('POST','/runbooks/{id}/dry-run'),('POST','/runbooks/{id}/submit'));SUPPORT=(('POST','/auth/login'),('POST','/runbooks'),('GET','/runbooks/{id}'))
HTTP_PLAN=27
REQUIRED_CASES=('runbook_writes.identity','runbook_writes.created','runbook_writes.nonmaintainer')+tuple('runbook_writes.'+op+'.'+stage for op in ('dry-run','submit') for stage in ('unknown_object','wrong_environment','view_only','all_env','rollback','fault_removed','recovery','readback'))+('runbook_writes.ledger','runbook_writes.cleanup','readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
CHECKS=[dict(step=1,validated=False,executed=False,requires_approval=False)]
ACTIONS={'dry-run':'runbook.validation_preview','submit':'runbook.submitted'}
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_runbook_write_boundaries_coverage.py','tests/test_api_runbook_write_boundaries_coverage.py','api_conversation_object_session_coverage.py','tests/test_api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalRunbookWriteBoundaries',proof_scope='blank human Runbook entry refusals, preview/submit exact audit rollback and explicit recovery; no execution, verification producer, notification or whole acceptance')
h.evidence=evidence
class Contract(GenericContract):
 def canonical(self,method,path):
  u=urlsplit(path);check(not any((u.scheme,u.netloc,u.query,u.fragment)),'exact relative Runbook route required');canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside two Runbook writes');return canonical,kind
base.Contract=Contract
def adapt(fn,replacements,namespace):
 s=inspect.getsource(fn)
 for old,new in replacements:check(s.count(old)==1,'private lifecycle anchor drift');s=s.replace(old,new)
 ns=dict(namespace);exec(compile(textwrap.dedent(s),str(__file__)+'[private lifecycle]','exec'),ns);return ns[fn.__name__]
admission=adapt(h.admission,[('root-admitted-conversation-object-session','root-admitted-runbook-write-boundaries'),('conversation_boundaries_authorized','runbook_write_faults_authorized')],h.__dict__);h.admission=admission
bounded_cleanup=adapt(base.Chain.cleanup,[('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')],base.__dict__)
def doctor(root):
 check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released atomic lifecycle drift');return h.doctor(root)
ERRORS={(404,'not_found'):('Runbook not found',),(403,'forbidden'):('Runbook management denied','Only the maintainer can submit this draft'),(409,'env_view_only'):('All non-production environments are read-only',),(503,'knowledge_unavailable'):('Knowledge operation failed',)}
def verify_error(v,headers,status,code):
 check((status,code) in ERRORS and h.header(headers,'Content-Type').split(';')[0]=='application/json','declared JSON Runbook refusal required');check(isinstance(v,dict) and set(v)=={'error'} and isinstance(v['error'],dict) and set(v['error'])=={'code','message'} and v['error']['code']==code and v['error']['message'] in ERRORS[(status,code)],'exact sanitized Runbook refusal differs')
h._namespace['verify_error']=verify_error
def payload(nonce):return dict(title='Owned write boundary '+nonce,source='blank',**{'from':''},status='draft',steps=[dict(n=1,kind='check',title='Read-only manual checklist',body='Owned inert human instruction.',readonly=True)])
def changes(op):return dict(environment_id='test',version=1,readonly_valid=False,steps=1) if op=='dry-run' else dict(version=1,environment_id='test')
def fault_statements(nonce,actor,rid,op):
 check(op in ACTIONS and all(re.fullmatch('[a-f0-9]{32}',v) for v in (nonce,actor,rid)),'own exact UUID actor/target/action required');name='runbook_write_fail_'+nonce
 create='BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+" AND NEW.application_id='' AND NEW.action="+q(ACTIONS[op])+' AND NEW.target='+q(rid)+" AND NEW.outcome='succeeded' AND NEW.changes="+q(json.dumps(changes(op)))+"::jsonb THEN RAISE EXCEPTION 'owned Runbook write audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;'
 return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def addition(old,new,count):
 check(all(r in new for r in old) and len(new)==len(old)+count,'old complete rows or exact delta differs');delta=[r for r in new if r not in old];check(len(delta)==count,'exact unique additions differ');return delta
def verify_created(before,after,rid,actor,body):
 check(after['business']==before['business'],'create changed unrelated public tables');rows={t:addition(before[t],after[t],0 if t=='v3_runbook_validation_previews' else 1)[0] if t!='v3_runbook_validation_previews' else addition(before[t],after[t],0) for t in TABLES};doc=rows['knowledge_documents'];render='# '+body['title']+'\n\n## 1. '+body['steps'][0]['title']+'\n'+body['steps'][0]['body'];metadata=dict(maintainer_id=actor,origin='human',actor_id=actor,visibility='workspace',environment_ids=['test'])
 check(all(doc.get(k)==v for k,v in dict(id=rid,application_id=None,title=body['title'],body=render,source='runbook://v3/'+rid,version=1,content_hash=hashlib.sha256(render.encode()).hexdigest(),created_by=actor,lifecycle='draft',metadata=metadata,active_generation=None).items()),'formal blank document lineage differs');rb=rows['v3_runbooks'];check(all(rb.get(k)==v for k,v in dict(document_id=rid,source_kind='blank',source_ref='',conversation_id=None,steps=body['steps'],source_snapshot=dict(source_id='',source_kind='blank',content_sha256=''),reviewers=[],when_text='',last_verified_at=None).items()),'formal inert structured steps differ');rev=rows['knowledge_revisions'];check(rev['document_id']==rid and rev['version']==1 and rev['created_by']==actor and rev['snapshot']['body']==render and rev['snapshot']['metadata']==metadata and rev['snapshot']['lifecycle']=='draft','initial snapshot lineage differs');history=rows['knowledge_history'];check(all(history.get(k)==v for k,v in dict(document_id=rid,version=1,actor_id=actor,operation='v3.runbook.created').items()),'create history differs');a=rows['management_audit'];check(all(a.get(k)==v for k,v in dict(actor_id=actor,application_id='',action='runbook.created',target=rid,outcome='succeeded',changes=dict(status='draft',source='blank',environment_id='test')).items()),'create audit differs')
def verify_response(op,v):
 if op=='submit':check(v==dict(ok=True,status='review'),'submit response differs');return
 check(isinstance(v,dict) and set(v)=={'id','steps','ok','mode','checks'} and re.fullmatch('[a-f0-9]{32}',v['id']) and v['steps']==1 and v['ok'] is False and v['mode']=='validation_only' and v['checks']==CHECKS,'inert validation preview response differs or claims execution')
def verify_recovery(op,before,after,rid,actor,value):
 verify_response(op,value);check(before['business']==after['business'],'recovery changed unrelated public ledger');audit=addition(before['management_audit'],after['management_audit'],1)[0];check(all(audit.get(k)==v for k,v in dict(actor_id=actor,application_id='',action=ACTIONS[op],target=rid,outcome='succeeded',changes=changes(op)).items()),'exact recovered write audit differs')
 for t in ('v3_runbooks','knowledge_revisions'):check(before[t]==after[t],'write changed structured steps/revision/source')
 if op=='dry-run':
  check(before['knowledge_documents']==after['knowledge_documents'] and before['knowledge_history']==after['knowledge_history'],'preview changed lifecycle/history');row=addition(before['v3_runbook_validation_previews'],after['v3_runbook_validation_previews'],1)[0];check(all(row.get(k)==v for k,v in dict(id=value['id'],document_id=rid,document_version=1,actor_id=actor,step_count=1,readonly_valid=False,checks=CHECKS).items()),'exact persisted preview differs')
 else:
  check(before['v3_runbook_validation_previews']==after['v3_runbook_validation_previews'],'submit changed previews');old=before['knowledge_documents'];new=after['knowledge_documents'];check(len(old)==len(new)==1 and dict(old[0],lifecycle='review')==new[0],'submit changed document fields beyond lifecycle');row=addition(before['knowledge_history'],after['knowledge_history'],1)[0];check(all(row.get(k)==v for k,v in dict(document_id=rid,version=1,actor_id=actor,operation='lifecycle.review').items()),'exact submission history differs')
def verify_detail(v,rid,body,state,owner):
 wanted=dict(id=rid,title=body['title'],version='v1',status=state,base='blank',source_kind='blank',source_ref='',owner=owner,scope='workspace · env:test',environment_ids=['test'],application_id='',team_id='',tags=[],when='',steps=body['steps'],reviewers=[],usage=dict(refs=None,resolve_rate=None,weekly=None,highlight=None),incidents=None,last_verified=None,complete_v3_dto=False,partial=True)
 check(isinstance(v,dict) and all(v.get(k)==w for k,w in wanted.items()),'exact inert Runbook detail differs/claims verification');check(v.get('unknown_fields')==['usage.refs','usage.resolve_rate','usage.weekly','usage.highlight','incidents','pending_reviewers','execution_verification','pending_reviewers'],'unknown verification/reviewer scope differs');check(len(v.get('versions',[]))==1 and v['versions'][0]['v']=='v1' and v['versions'][0]['who']==owner,'v1 revision lineage differs')
class Chain(h.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id,permit);self.users['viewonly']=uuid.uuid4().hex;self.roles['viewonly']='readonly-'+self.nonce+'-viewonly';self.fault_intent=None;self.cleanup_deadline=None;self.contract=Contract(root);self.state.update(suite=MODULE,normal_added=0,model_scope='no inference or executor');base._private(self.work/'state.json',self.state)
 def frozen(self):check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py')==BASE_HELPER_SHA,'released atomic lifecycle drift');super().frozen()
 def marker_case(self,name,value):
  if name=='identities':return
  self.frozen();h.emit('runbook_writes.'+name,'passed',dict(run_id=self.run_id,**value))
 def seed(self):
  super().seed();statements=[]
  for name,role in self.roles.items():
   for env in (('test','staging') if name=='reader' else ('test',)):
    for action in (('view',) if name=='viewonly' else ('view','manage')):statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (role,'knowledge',action,'environment',env))+');')
  self.sql('BEGIN; '+' '.join(statements)+' COMMIT;');roles=','.join(q(v) for v in self.roles.values());actual=json.loads(self.sql("SELECT COALESCE(json_agg(to_jsonb(g) ORDER BY role_id,action,scope_id),'[]'::json) FROM v3_role_grants g WHERE module='knowledge' AND role_id=ANY(ARRAY["+roles+'])',read=True));expected={(role,a,e) for n,role in self.roles.items() for e in (('test','staging') if n=='reader' else ('test',)) for a in (('view',) if n=='viewonly' else ('view','manage'))};check(len(actual)==len(expected) and {(r['role_id'],r['action'],r['scope_id']) for r in actual}==expected and all(r['scope_kind']=='environment' for r in actual),'exact synthetic knowledge grants differ');base._private(self.work/'knowledge-grants.json',actual);self.marker_case('identity',dict(owner_manage=True,nonmaintainer_manage=True,view_only=True,SQL_business_writes=0))
 def call(self,*a,**kw):
  original=base.transport;cloned=h._namespace['transport']
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 changed');result=original(origin,method,path,payload,headers,timeout)
   if path.startswith('/api/v1/runbooks'):
    status,raw,hs=result;check(len(raw)<=65536,'bounded synthetic Runbook response required');base._private(self.work/('runbook-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,environment_id=(headers or {}).get('X-AIOps-Environment'),API_index=base.BASES.index(origin),status=status,request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),response_body=raw.decode('utf-8','replace'),source_tree_sha=SOURCE,binary_sha256=BINARY))
   return result
  base.transport=wire;h._namespace['transport']=wire
  try:return super().call(*a,**kw)
  finally:base.transport=original;h._namespace['transport']=cloned
 def install_fault(self,rid,op):
  self.frozen();self.owner();check(self.fault_intent is None,'one owned audit fault only');self.fault_intent=(rid,op);name,create,_=fault_statements(self.nonce,self.users['reader'],rid,op);base._private(self.work/'fault-intent.json',dict(name=name,runbook=rid,action=ACTIONS[op]));self.sql(create)
 def remove_fault(self,deadline):
  if self.fault_intent is None:return
  rid,op=self.fault_intent;self.owner_deadline(deadline);name,_,drop=fault_statements(self.nonce,self.users['reader'],rid,op);self.pg(drop,self.database,self.db_user,deadline=deadline);query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)";check(json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))==dict(trigger=False,function=False),'owned fault remains');self.fault_intent=None
 def owner_deadline(self,deadline):
  value=json.loads(self.pg("BEGIN READ ONLY; SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user)); COMMIT;",self.database,self.db_user,deadline=deadline));check(value==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'exact UUID DB/role tags differ')
 def readback(self,rid,body,state,ledger,old=None):
  values=[]
  for index in (0,1):
   value,_=self.call('reader','GET','/api/v1/runbooks/'+rid,index=index,validate=lambda v:verify_detail(v,rid,body,state,self.marker+' reader'));values.append(value);h.same(ledger,self.ledger())
  check(values[0]==values[1] and (old is None or values[0]==old),'twoAPI/same baseline full detail differs');return values[0]
 def object_boundaries(self,op,rid,baseline):
  grants=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('module',module,'action',action,'scope_kind',scope_kind,'scope_id',scope_id)),'[]'::json) FROM v3_effective_role_grants WHERE user_id="+q(self.users['reader'])+" AND scope_kind='environment' AND scope_id='staging' AND module IN ('environment','knowledge')",read=True));expected={('environment','view'),('knowledge','view'),('knowledge','manage')};check(len(grants)==len(expected) and {(r['module'],r['action']) for r in grants}==expected and all(r['scope_kind']=='environment' and r['scope_id']=='staging' for r in grants),'same maintainer effective staging authority required');unknown=uuid.uuid5(uuid.UUID(hex=self.nonce),'missing-write-runbook').hex;check(unknown!=rid and self.sql('SELECT EXISTS(SELECT 1 FROM knowledge_documents WHERE id='+q(unknown)+')',read=True)=='f','nonce Runbook ID must actually be absent');base._private(self.work/(op+'-object-boundary-proof.json'),dict(actor_id=self.users['reader'],unknown_id=unknown,existing_id=rid,staging_grants=grants))
  for stage,target,env in (('unknown_object',unknown,'test'),('wrong_environment',rid,'staging')):
   self.call('reader','POST','/api/v1/runbooks/'+target+'/'+op,{},want=404,code='not_found',scenario='authorization',environment=env,index=1);h.same(baseline,self.ledger());self.marker_case(op+'.'+stage,dict(status=404,code='not_found',environment_id=env,same_owner=True,staging_authorized=True,full_ledger_unchanged=True))
 def run(self):
  self.provision();self.start();self.seed()
  for actor in self.users:self.login(actor)
  before=self.ledger();check(all(before[t]==[] for t in TABLES),'fresh Runbook six tables not empty');body=payload(self.nonce);v,_=self.call('reader','POST','/api/v1/runbooks',body,want=201,scenario='fixture');check(set(v)=={'id'} and re.fullmatch('[a-f0-9]{32}',v['id']),'HTTP Runbook ID differs');rid=v['id'];created=self.ledger();verify_created(before,created,rid,self.users['reader'],body);base._private(self.work/'created-ledger.json',created);old=self.readback(rid,body,'draft',created);self.marker_case('created',dict(HTTP201=True,source='blank',steps=1,readonly_only=True,SQL_business_writes=0))
  self.call('peer','POST','/api/v1/runbooks/'+rid+'/submit',{},want=403,code='forbidden',scenario='authorization',index=1);h.same(created,self.ledger());self.marker_case('nonmaintainer',dict(route='submit',status=403,knowledge_manage_preserved=True))
  for op in ('dry-run','submit'):
   path='/api/v1/runbooks/'+rid+'/'+op;baseline=self.ledger();self.object_boundaries(op,rid,baseline);self.call('viewonly','POST',path,{},want=404 if op=='dry-run' else 403,code='not_found' if op=='dry-run' else 'forbidden',scenario='authorization',index=1);h.same(baseline,self.ledger());self.marker_case(op+'.view_only',dict(status=404 if op=='dry-run' else 403,write_delta=0));self.call('reader','POST',path,{},want=409,code='env_view_only',scenario='authorization',environment='all');h.same(baseline,self.ledger());self.marker_case(op+'.all_env',dict(status=409,write_delta=0));primary=None
   try:
    self.install_fault(rid,op);self.call('reader','POST',path,{},want=503,code='knowledge_unavailable',scenario='failure');failed=self.ledger();h.same(baseline,failed);base._private(self.work/(op+'-failed-ledger.json'),failed);self.marker_case(op+'.rollback',dict(status=503,full_six_tables_and_other_public_unchanged=True));self.readback(rid,body,'draft',baseline,old)
   except BaseException as error:primary=error
   finally:
    try:self.remove_fault(time.monotonic()+4)
    except BaseException as error:
     if primary is None:primary=error
     else:primary.add_note('fault restore secondary: '+type(error).__name__)
   if primary is not None:raise primary
   self.marker_case(op+'.fault_removed',dict(trigger=False,function=False));v,_=self.call('reader','POST',path,{},index=1,scenario='failure',validate=lambda v:verify_response(op,v));after=self.ledger();verify_recovery(op,baseline,after,rid,self.users['reader'],v);base._private(self.work/(op+'-recovered-ledger.json'),after);self.marker_case(op+'.recovery',dict(status=200,API_index=1,explicit=True,audit_delta=1,no_auto_retry=True));old=self.readback(rid,body,'draft' if op=='dry-run' else 'review',after,old if op=='dry-run' else None);self.marker_case(op+'.readback',dict(two_APIs=True,no_read_delta=True))
  self.marker_case('ledger',dict(no_execution_or_model_or_notification=True,normal_added=0));check(self.calls==HTTP_PLAN,'reviewed27 HTTP scope differs');result=dict(run_id=self.run_id,http_calls=self.calls,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY);base._private(self.work/'runbook-writes-result.json',result);return result
 def cleanup(self):
  self.cleanup_deadline=time.monotonic()+40;primary=None
  try:self.remove_fault(min(self.cleanup_deadline,time.monotonic()+4))
  except BaseException as e:primary=e
  finally:
   try:bounded_cleanup(self)
   except BaseException as e:
    if primary is None:primary=e
    else:primary.add_note('UUID cleanup secondary: '+type(e).__name__)
  if primary is not None:raise primary
  for p in PORTS:
   check(time.monotonic()<self.cleanup_deadline,'cleanup budget exhausted')
   with socket.socket() as sock:sock.settimeout(.2);check(sock.connect_ex(('127.0.0.1',p))!=0,'own Runbook API port remains')
  h.emit('runbook_writes.cleanup','passed',dict(single40=True,restore_subset4=True,grace45=True,owned_UUID_only=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=HTTP_PLAN,normal_added=0,registration_evidence=False,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45,containers=0,models=0)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with base.exclusive(ROOT),base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:h.emit('runbook_writes.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:h.emit('runbook_writes.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
