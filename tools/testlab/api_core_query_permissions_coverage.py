"""Five existing core routes: three query errors, manual-case object ACL, Runbook create ACL.
Formal service/webhook/manual-case prerequisites; no SQL business seeds or execution.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap,time
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-core-query-permissions';PORTS=(18884,18885)
def load(name,path):
 s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
inc=load('core_queries_private_incident',ROOT/'tools/testlab/api_incident_fault_interfaces_coverage.py');ro=inc.ro;h=load('core_queries_private_admission',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
check,Blocked,q=ro.check,ro.Blocked,ro.quote;SOURCE,BINARY,RUNTIME,PG_CID=inc.SOURCE_SHA,inc.BINARY_SHA,inc.RUNTIME_SHA,inc.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);GenericContract=inc.Contract.__bases__[0]
TARGETS=(('GET','/inbox'),('GET','/alert-groups'),('GET','/incidents'),('GET','/incidents/{id}'),('POST','/runbooks'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'),('POST','/incidents'))
QUERIES={'/api/v1/inbox':'filter=mine&tab=mentions','/api/v1/alert-groups':'severity=P5','/api/v1/incidents':'status=active&status=resolved'}
ERRORS={'invalid_filter':'Unknown or conflicting inbox filter','invalid_severity':'Choose P1, P2, P3, or P4','invalid_status':'Use active, resolved, or all','not_found':'Event not found','forbidden':'Insufficient permission'}
REQUIRED_CASES=tuple('core_queries.'+n for n in ('identity','http_inputs','case_created','case_owner','inbox_filter','groups_severity','incidents_status','case_no_authority','case_wrong_environment','runbook_no_manage','readback','ledger','cleanup'))+tuple('core_queries.api_ready.'+stage+'.'+str(i) for stage in ('setup','source') for i in (0,1))+('readonly.fresh_database','readonly.cleanup','readonly.complete')
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_core_query_permissions_coverage.py','tests/test_api_core_query_permissions_coverage.py','api_incident_fault_interfaces_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
inc.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalCoreQueryPermissions',proof_scope='exact three invalid filters, HTTP manual-case object authority, knowledge:view without manage403; no new normal or whole acceptance')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit=emit;ro.append_json=append_json
class Contract(GenericContract):
 def canonical(self,method,path):
  u=urlsplit(path);check(not any((u.scheme,u.netloc,u.fragment)),'relative exact core request required')
  if u.query:check(method=='GET' and QUERIES.get(u.path)==u.query,'only three exact reviewed invalid filters')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside five core routes');return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
def adapt(fn,replacements,ns):
 source=inspect.getsource(fn)
 for old,new in replacements:check(source.count(old)==1,'private lifecycle anchor changed');source=source.replace(old,new)
 scope=dict(ns);exec(compile(textwrap.dedent(source),str(__file__)+'[private seam]','exec'),scope);return scope[fn.__name__]
admission=adapt(h.admission,[('root-admitted-conversation-object-session','root-admitted-core-query-permissions'),('conversation_boundaries_authorized','core_query_permissions_authorized')],dict(h.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro))
def doctor(root):
 data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes drift');data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());inc.frozen_identity(data,ro.inspect_pg(data['postgres_container'])['Id']);return data
def verify_error(v,hs,status,code):
 expected=422 if code.startswith('invalid_') else (404 if code=='not_found' else 403);check(status==expected and code in ERRORS and next((v for k,v in hs.items() if k.lower()=='content-type'),'').split(';')[0]=='application/json','exact error status/JSON required');check(v==dict(error=dict(code=code,message=ERRORS[code])),'exact sanitized error differs/reflected input')
_source=inspect.getsource(ro.Chain.call);needle='            if 200 <= status < 300:';check(_source.count(needle)==1,'private actual validation seam drift');_namespace=dict(ro.__dict__,verify_error=verify_error);exec(compile(textwrap.dedent(_source.replace(needle,'            if code is not None:verify_error(value,response_headers,want,code)\n'+needle)),str(__file__)+'[private call]','exec'),_namespace);validated_call=_namespace['call']
def unchanged(before,after):check(before==after,'rejected/read request changed full Case/alert/audit/Runbook or other-public ledger')
def blank_payload(nonce):return dict(title='Owned refused Runbook '+nonce,source='blank',**{'from':''},status='draft',steps=[dict(n=1,kind='check',title='Readonly inert check',body='No execution.',readonly=True)])
def case_payload(app,alert,title):return dict(inc.payload(app,alert,title),impact='Owned synthetic impact',commander='Owned synthetic commander')
def verify_case_detail(v,rid,body):
 check(v.get('id')==rid and v.get('title')==body['title'] and v.get('severity')==body['severity'] and v.get('status')=='active' and v.get('record_kind')=='case' and v.get('environment_key')=='test' and v.get('investigation_id') is None,'formal case owner detail binding differs')
class Chain(inc.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.state.update(suite=MODULE,normal_added=0,ports=list(PORTS));ro._private(self.work/'state.json',self.state)
 def frozen(self):admission(self.permit,self.run_id);super().frozen()
 def marker_case(self,name,actual):self.frozen();emit('core_queries.'+name,'passed',dict(run_id=self.run_id,**actual))
 def start(self):
  stage=('setup','source')[self.phase];original=ro.emit
  def ready(name,status,actual,**fields):
   if name.startswith('readonly.api_ready.'):name='core_queries.api_ready.'+stage+'.'+name[-1]
   return original(name,status,actual,**fields)
  ro.emit=ready
  try:ro.Chain.start(self)
  finally:ro.emit=original
 def seed(self):
  super().seed();role=self.roles['reader'];self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) SELECT '+','.join(q(v) for v in (role,'knowledge','view','environment','test'))+' WHERE NOT EXISTS(SELECT 1 FROM v3_role_grants WHERE role_id='+q(role)+" AND module='knowledge' AND action='view' AND scope_kind='environment' AND scope_id='test');")
  rows=json.loads(self.sql("SELECT COALESCE(json_agg(to_jsonb(g)),'[]'::json) FROM v3_effective_role_grants g WHERE user_id=ANY(ARRAY["+q(self.users['reader'])+','+q(self.users['denied'])+'])',read=True));check(any(r['user_id']==self.users['reader'] and r['module']=='knowledge' and r['action']=='view' and r['scope_id']=='test' for r in rows) and not any(r['user_id']==self.users['reader'] and r['module']=='knowledge' and r['action']=='manage' for r in rows) and not any(r['user_id']==self.users['denied'] and r['module']=='events' for r in rows),'exact reader knowledge-view/no-manage and denied no-events required');ro._private(self.work/'acl-witness.json',rows);self.marker_case('identity',dict(knowledge_view_without_manage=True,denied_environment_view_without_events=True,SQL_business_writes=0))
 def call(self,user,method,path,body=None,request_key=None,**kw):
  self.frozen();original=ro.transport;cloned=_namespace['transport']
  def wire(origin,verb,target,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 contract changed');hs=dict(headers or {})
   if verb=='POST' and target=='/api/v1/webhooks/alertmanager/'+self.source_id:check(user is None,'own ingress only');hs['Authorization']='Bearer '+self.source_token
   if verb=='POST' and target=='/api/v1/incidents':check(request_key=='incident-'+self.nonce,'exact own case key');hs['Idempotency-Key']=request_key
   result=original(origin,verb,target,payload,hs,timeout)
   if target!='/api/v1/auth/login':
    status,raw,_=result;check(len(raw)<=65536,'bounded own core response required');ro._private(self.work/('core-wire-'+str(self.calls)+'.json'),dict(method=verb,path=target,API_index=ro.BASES.index(origin),status=status,request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),response_body=raw.decode('utf-8','replace'),source_tree_sha=SOURCE,binary_sha256=BINARY))
   return result
  ro.transport=wire;_namespace['transport']=wire
  try:return validated_call(self,user,method,path,body,**kw)
  finally:ro.transport=original;_namespace['transport']=cloned
 def ledger(self):
  value=super().ledger();excluded=','.join(q(t) for t in ('v3_incident_cases','v3_incident_case_alerts','v3_incident_case_status_events','v3_incident_timeline','ops_incidents','management_audit'));tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+excluded+')',read=True));check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded public table names');parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+t+' t' for t in tables];value['other_public']=json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h',read=True));return value
 def reject(self,baseline,actor,method,path,status,code,body=None,environment='test'):
  unchanged(baseline,self.ledger());self.call(actor,method,path,body,want=status,code=code,index=1,scenario='boundary' if status==422 else 'authorization',environment=environment);unchanged(baseline,self.ledger())
 def run(self):
  self.provision();self.start();self.seed()
  for actor in ('admin','reader','denied','hidden'):self.login(actor)
  self.inputs();before=self.ledger();body=case_payload(self.app,self.alert,self.marker+' confirmed Case');key='incident-'+self.nonce;v,_=self.call('reader','POST','/api/v1/incidents',body,want=201,request_key=key,scenario='fixture');after=self.ledger();ro._private(self.work/'case-create-before-ledger.json',before);ro._private(self.work/'case-create-after-ledger.json',after);inc.assert_recovery(before,after,v,body,self.users['reader'],self.app,self.alert,key);rid=v['id'];ro._private(self.work/'created-case-ledger.json',after);self.marker_case('case_created',dict(HTTP201=True,SQL_business_writes=0,no_investigation=True));old,_=self.call('reader','GET','/api/v1/incidents/'+rid,validate=lambda v:verify_case_detail(v,rid,body));unchanged(after,self.ledger());self.marker_case('case_owner',dict(status=200,real_http_case=True))
  for name,path,code in (('inbox_filter','/api/v1/inbox?filter=mine&tab=mentions','invalid_filter'),('groups_severity','/api/v1/alert-groups?severity=P5','invalid_severity'),('incidents_status','/api/v1/incidents?status=active&status=resolved','invalid_status')):
   self.reject(after,'reader','GET',path,422,code);self.marker_case(name,dict(status=422,error_code=code,ledger_delta=0))
  self.reject(after,'denied','GET','/api/v1/incidents/'+rid,404,'not_found');self.marker_case('case_no_authority',dict(status=404,authenticated_same_environment=True));self.reject(after,'hidden','GET','/api/v1/incidents/'+rid,404,'not_found',environment='staging');self.marker_case('case_wrong_environment',dict(status=404,authenticated_environment_grant=True));self.reject(after,'reader','POST','/api/v1/runbooks',403,'forbidden',blank_payload(self.nonce));self.marker_case('runbook_no_manage',dict(status=403,knowledge_view_preserved=True,all_business_audit_delta=0));value,_=self.call('reader','GET','/api/v1/incidents/'+rid,index=1,validate=lambda v:check(v==old,'secondAPI original Case DTO differs'));unchanged(after,self.ledger());self.marker_case('readback',dict(two_API_same=True));ro._private(self.work/'final-ledger.json',self.ledger());self.marker_case('ledger',dict(complete_Case_alert_audit_and_all_other_public_unchanged=True,no_runbook_or_model_or_notifications=True));check(self.calls==15,'reviewed15HTTP scope differs');result=dict(run_id=self.run_id,http_calls=self.calls,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'core-query-result.json',result);return result
 def cleanup(self):
  # No DDL/ACL restore fault in this subgroup. Original sole40 UUID/Popen cleanup.
  ro.Chain.cleanup(self)
  for p in PORTS:
   with socket.socket() as sock:sock.settimeout(.2);check(sock.connect_ex(('127.0.0.1',p))!=0,'own core API port remains')
  emit('core_queries.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=15,normal_added=0,registration_evidence=False,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('core_queries.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('core_queries.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
