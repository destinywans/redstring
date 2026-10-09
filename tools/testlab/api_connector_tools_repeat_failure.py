"""Six HTTP: native connector create, duplicate tools PATCH and audit rollback.
No SQL business seed, Prober, provider HTTP, executor or complete chain.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-connector-tools-repeat-failure';PORTS=(18954,18955)
def load(name,path):
 spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
h=load('query_boundary_private_upload',ROOT/'tools/testlab/api_upload_audit_rollback_coverage.py');gate=load('query_boundary_private_gate',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
ro=h.ro;check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=h.SOURCE_SHA,h.BINARY_SHA,h.RUNTIME_SHA,h.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('PATCH','/connectors/{id}'),);SUPPORT=(('POST','/auth/login'),('POST','/connectors'))
CASES=('first_patch','same_patch_repeat','audit_rollback','opposite_API_recovery')
REQUIRED_CASES=tuple('connector_tools.'+n for n in ('identity','created','first_patch','same_patch_repeat','fault_installed','audit_rollback','fault_removed','opposite_API_recovery','ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_connector_tools_repeat_failure.py','tests/test_api_connector_tools_repeat_failure.py','api_upload_audit_rollback_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalConnectorToolsRepeatFailure',proof_scope='native connector tools duplicate PATCH per-invocation audit and exact audit-failure transaction rollback; no Prober/execution/whole acceptance')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(ro.Contract):
 def canonical(self,method,path):
  parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.fragment)),'relative exact boundary path required')
  check(not parsed.query,'exact connector tools path only')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside connector tools/create/login');return canonical,kind
ro.Contract=Contract
_src=inspect.getsource(gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-connector-tools-repeat-failure'),('conversation_boundaries_authorized','connector_tools_repeat_failure_authorized')):
 check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
def doctor(root):
 data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift')
 info=ro.inspect_pg(data['postgres_container']);check(info['Id']==PG_CID and info['HostConfig'].get('NanoCpus')==4000000000 and info['HostConfig'].get('Memory')==2147483648,'exact PG4CPU2GiB required')
 data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());h.frozen_identity(data,info['Id']);return data
def body(enabled):return dict(tools=[dict(name='query_metrics',enabled=enabled)])
def verify_patch(value,enabled):check(value==dict(ok=True,tools=[dict(name='query_metrics',desc='Query metrics',enabled=enabled)]),'exact English tools PATCH DTO')
def verify_change(before,after,cid,actor,enabled):
 check(len(before['connectors'])==len(after['connectors'])==1 and before['connectors'][0]['id']==cid,'exact own connector before')
 old,new=before['connectors'][0],after['connectors'][0];check(new['id']==cid and new['environment_id']=='test' and new['provider']=='prometheus' and new['enabled'] is True,'native own unauthenticated connector identity')
 expected=[dict(name='query_metrics',desc='Read-only',enabled=enabled)];check(new['tools']==expected,'exact stored registered tools')
 check({k:v for k,v in old.items() if k not in ('tools','updated_at')}=={k:v for k,v in new.items() if k not in ('tools','updated_at')},'PATCH changed other connector fields')
 check(len(after['audit'])==len(before['audit'])+1 and all(x in after['audit'] for x in before['audit']),'old audits immutable and one new audit')
 added=[x for x in after['audit'] if x not in before['audit']];check(len(added)==1,'one distinct audit per invocation');a=added[0]
 check(a['actor_id']==actor and a['action']=='connector.tools.updated' and a['target']==cid and a['outcome']=='success' and a['changes']==dict(environment='test',tools=expected),'exact patch transactional audit')
 check(set(before['all_public'])==set(after['all_public']) and {t for t in before['all_public'] if before['all_public'][t]!=after['all_public'][t]}<= {'v3_connectors','management_audit'} and before['all_public']['management_audit']!=after['all_public']['management_audit'],'no other public rows/session mutation')
def fault_sql(nonce,actor,cid):
 check(re.fullmatch('[a-f0-9]{32}',nonce) and re.fullmatch('[a-f0-9]{32}',actor) and re.fullmatch('[a-f0-9]{32}',cid),'canonical own fault identities')
 name='ctf_'+nonce
 guard='NEW.actor_id='+q(actor)+" AND NEW.action='connector.tools.updated' AND NEW.target="+q(cid)+" AND NEW.changes->>'environment'='test'"
 install='CREATE FUNCTION '+ro.identifier(name)+"() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF "+guard+" THEN RAISE EXCEPTION 'owned connector tools audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+ro.identifier(name)+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION '+ro.identifier(name)+'();'
 drop='DROP TRIGGER IF EXISTS '+ro.identifier(name)+' ON public.management_audit; DROP FUNCTION IF EXISTS '+ro.identifier(name)+'();';return name,install,drop
class Chain(ro.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.fault_attempted=False;self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,SQL_business_writes=0);ro._private(self.work/'state.json',self.state)
 def frozen(self):
  admission(self.permit,self.run_id);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift');super().frozen();h.frozen_identity(self.data,PG_CID);check(hashes()==self.data['helper_sha256'],'transitive lifecycle drift')
 def owner(self):return h.Chain.owner(self)
 def marker_case(self,name,value):
  check('connector_tools.'+name in REQUIRED_CASES,'unknown marker');self.frozen();emit('connector_tools.'+name,'passed',dict(run_id=self.run_id,**value))
 def ledger(self):
  self.frozen();self.owner()
  tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'",read=True));check(tables and len(tables)<=512 and {'sessions','management_audit','v3_connectors'}.issubset(tables) and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded public inventory')
  parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+ro.identifier(t)+' t' for t in tables]
  rows=json.loads(self.sql("SELECT json_build_object('connectors',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id),'[]'::jsonb) FROM public.v3_connectors t),'audit',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY id),'[]'::jsonb) FROM public.management_audit t),'all_public',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(parts)+') h))',read=True));check(set(rows['all_public'])==set(tables) and all(re.fullmatch('[a-f0-9]{64}',v) for v in rows['all_public'].values()),'full public row digests');return rows
 def call(self,*args,**kwargs):
  self.frozen();original=ro.transport
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 budget drift');status,raw,hs=original(origin,method,path,payload,headers,timeout)
   if method=='PATCH':
    check(len(raw)<=4096,'bounded connector response');v=json.loads(raw)
    if self.want_fault:check(status==503 and v==dict(error=dict(code='audit_unavailable',message='Could not audit connector change')),'exact audit503')
    else:check(status==200,'exact PATCH200');verify_patch(v,self.want_enabled)
    check(next((v for k,v in hs.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','JSON patch response')
    ro._private(self.work/('connector-tools-wire-'+str(self.calls)+'.json'),dict(method=method,path=self.contract.canonical(method,path)[0],API_index=ro.BASES.index(origin),status=status,response=v,response_sha256=hashlib.sha256(raw).hexdigest(),request_sha256=hashlib.sha256(payload or b'').hexdigest(),request_body_stored=False,credentials_stored=False))
   return status,raw,hs
  ro.transport=wire
  try:return super().call(*args,**kwargs)
  finally:ro.transport=original
 def patch(self,enabled,index=0,fault=False):
  self.want_enabled,self.want_fault=enabled,fault;return self.call('admin','PATCH','/api/v1/connectors/'+self.cid,body(enabled),want=503 if fault else 200,code='audit_unavailable' if fault else None,index=index,scenario='failure' if fault else 'idempotency')
 def install_fault(self):
  self.frozen();self.owner();name,install,_=fault_sql(self.nonce,self.actor,self.cid);check(self.sql('SELECT to_regprocedure('+q(name+'()')+') IS NULL',read=True)=='t','fault name preexisting');self.fault_attempted=True;self.sql(install);self.marker_case('fault_installed',dict(scoped_actor_action_target_environment=True,UUID_function=name))
 def remove_fault(self):
  self.owner();name,_,drop=fault_sql(self.nonce,self.actor,self.cid);self.sql(drop)
  result=json.loads(self.sql("SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q(name+'()')+') IS NOT NULL)',read=True));check(result==dict(trigger=False,function=False),'own fault removed');self.fault_attempted=False;self.marker_case('fault_removed',result)
 def run(self):
  self.provision();self.start();self.login('admin');self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True);check(re.fullmatch('[a-f0-9]{32}',self.actor),'own bootstrapped admin identity');self.marker_case('identity',dict(login=1,SQL_business_writes=0,provider_calls='uncollected',source_guard='create/PATCH do not invoke Prober'))
  empty=self.ledger();check(empty['connectors']==[] and empty['audit']==[],'fresh own connector and audit rows')
  value,_=self.call('admin','POST','/api/v1/connectors',dict(type='prometheus',url='http://127.0.0.1:1',auth='none',tools=['query_metrics']),want=201,scenario='fixture');check(isinstance(value,dict) and set(value)=={'id'} and re.fullmatch('[a-f0-9]{32}',value['id']),'formal create ID');self.cid=value['id'];created=self.ledger();check(len(created['connectors'])==len(created['audit'])==1 and created['connectors'][0]['id']==self.cid and created['connectors'][0]['tools']==[dict(name='query_metrics',desc='Read-only',enabled=True)] and created['audit'][0]['action']=='connector.created' and created['audit'][0]['target']==self.cid and created['audit'][0]['actor_id']==self.actor,'exact native create rows/audit');check({t for t in empty['all_public'] if empty['all_public'][t]!=created['all_public'][t]}=={'v3_connectors','management_audit'},'formal create only connector/audit changes');ro._private(self.work/'connector-created-ledger.json',created);self.marker_case('created',dict(HTTP201=True,one_connector_audit=True,unauthenticated_readonly_registration=True,provider_calls='uncollected',source_guard='native registration creates configuration only'))
  previous=created
  for index,phase in enumerate(('first_patch','same_patch_repeat')):
   self.patch(False,index);after=self.ledger();verify_change(previous,after,self.cid,self.actor,False);ro._private(self.work/('connector-'+phase+'-ledger.json'),after);self.marker_case(phase,dict(HTTP200=True,tools_false=True,one_new_audit=True,same_body=True,no_dedup_promise=True,API_index=index));previous=after
  self.install_fault();self.patch(True,0,True);failed=self.ledger();check(failed==previous,'audit failure changed whole connector/audit/all-public ledger');ro._private(self.work/'connector-failed-ledger.json',failed);self.marker_case('audit_rollback',dict(HTTP503=True,error_code='audit_unavailable',full_public_exact=True,transaction_atomic=True))
  self.remove_fault();self.patch(True,1);final=self.ledger();verify_change(previous,final,self.cid,self.actor,True);ro._private(self.work/'connector-recovered-ledger.json',final);self.marker_case('opposite_API_recovery',dict(HTTP200=True,original_failed_body=True,API_index=1,one_new_audit=True))
  check(self.calls==6 and len(final['audit'])==4,'exact six HTTP, create1+three successfulPATCH audits');self.marker_case('ledger',dict(HTTP=6,successful_PATCHes=3,patch_audits=3,failed_PATCH_audits=0,all_other_public_unchanged=True,provider_calls='uncollected',provider_guard='source create/PATCH never invokes Prober'))
  result=dict(run_id=self.run_id,http_calls=6,target_PATCHes=4,normal_added=0,registration_evidence=False,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'connector-tools-result.json',result);return result
 def cleanup(self):
  # Sole original40 deadline destroys only this UUID DB (including its trigger).
  # Never mutate a shared audit table or adopt a saved PID to restore a fault.
  ro.Chain.cleanup(self)
  for port in PORTS:
   with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own connector tools listener remains')
  emit('connector_tools.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,fault_destroyed_with_owned_database=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=6,target_PATCHes=4,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('connector_tools.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('connector_tools.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
