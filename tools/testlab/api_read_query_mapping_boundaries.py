"""Seven HTTP: one admin login, three exact input refusals on each owned API.
No business seed, source request, inference, command, fault DDL or complete chain.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-read-query-mapping-boundaries';PORTS=(18950,18951)
def load(name,path):
 spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
h=load('query_boundary_private_upload',ROOT/'tools/testlab/api_upload_audit_rollback_coverage.py');gate=load('query_boundary_private_gate',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
ro=h.ro;check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=h.SOURCE_SHA,h.BINARY_SHA,h.RUNTIME_SHA,h.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/silences'),('GET','/services/{id}/peek'),('PUT','/service-mappings'));SUPPORT=(('POST','/auth/login'),)
CASES=('silence_state','oversized_reference','empty_mappings')
REQUIRED_CASES=tuple('query_mapping.'+n for n in ('identity','baseline',*(name+'.'+str(i) for name in CASES for i in range(2)),'ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_read_query_mapping_boundaries.py','tests/test_api_read_query_mapping_boundaries.py','api_upload_audit_rollback_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
h.hashes=hashes
def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalReadQueryMappingBoundaries',proof_scope='six exact authenticated input422 on two UUID APIs, full public ledger unchanged; no successful source read, producer, replay, fault or whole acceptance')
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
  check(not parsed.query or (method,path)==('GET','/api/v1/silences?state=invalid'),'only single reviewed silence query')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside three input routes/login');return canonical,kind
ro.Contract=Contract
_src=inspect.getsource(gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-read-query-mapping-boundaries'),('conversation_boundaries_authorized','read_query_mapping_authorized')):
 check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
def doctor(root):
 data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift')
 info=ro.inspect_pg(data['postgres_container']);check(info['Id']==PG_CID and info['HostConfig'].get('NanoCpus')==4000000000 and info['HostConfig'].get('Memory')==2147483648,'exact PG4CPU2GiB required')
 data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());h.frozen_identity(data,info['Id']);return data
def samples():return (
 ('silence_state','GET','/api/v1/silences?state=invalid',None,'invalid_state','Select one known silence state'),
 ('oversized_reference','GET','/api/v1/services/'+'a'*1025+'/peek',None,'invalid_reference','Invalid service ID'),
 ('empty_mappings','PUT','/api/v1/service-mappings',dict(environment_id='test',items=[]),'invalid_mappings','choose one environment and 1 to 50 service mappings'))
def verify_error(value,headers,code,message):check(value==dict(error=dict(code=code,message=message)) and next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','exact sanitized input422 required')
class Chain(ro.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,no_business_seed=True,no_fault_DDL=True);ro._private(self.work/'state.json',self.state)
 def frozen(self):
  admission(self.permit,self.run_id);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift');super().frozen();h.frozen_identity(self.data,PG_CID);check(hashes()==self.data['helper_sha256'],'transitive lifecycle drift')
 def marker_case(self,name,value):
  check('query_mapping.'+name in REQUIRED_CASES,'unknown marker');self.frozen();emit('query_mapping.'+name,'passed',dict(run_id=self.run_id,**value))
 def ledger(self):
  self.frozen();h.Chain.owner(self)
  tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'",read=True));check(tables and len(tables)<=512 and {'sessions','management_audit','applications'}.issubset(tables) and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded public inventory')
  parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+ro.identifier(t)+' t' for t in tables]
  value=json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h',read=True));check(set(value)==set(tables) and all(re.fullmatch('[a-f0-9]{64}',v) for v in value.values()),'full public row digests');return value
 def call(self,*args,**kwargs):
  self.frozen();original=ro.transport
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 budget drift');status,raw,hs=original(origin,method,path,payload,headers,timeout)
   if method!='POST' or path!='/api/v1/auth/login':
    check(len(raw)<=4096 and status==422,'bounded exact input422 required');value=json.loads(raw);verify_error(value,hs,*self.expected_error)
    ro._private(self.work/('query-input-wire-'+str(self.calls)+'.json'),dict(method=method,canonical_path=self.contract.canonical(method,path)[0],API_index=ro.BASES.index(origin),status=status,response=value,response_sha256=hashlib.sha256(raw).hexdigest(),request_bytes=len(payload or b''),request_sha256=hashlib.sha256(payload or b'').hexdigest(),request_body_stored=False,credentials_stored=False))
   return status,raw,hs
  ro.transport=wire
  try:return super().call(*args,**kwargs)
  finally:ro.transport=original
 def run(self):
  self.provision();self.start();self.login('admin');self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True);check(re.fullmatch('[a-f0-9]{32}',self.actor),'own bootstrapped admin identity');self.marker_case('identity',dict(login=1,SQL_business_writes=0,no_extra_component=True))
  before=self.ledger();ro._private(self.work/'query-ledger-before.json',before);self.marker_case('baseline',dict(all_public_tables=len(before),after_login=True))
  for name,method,path,body,code,message in samples():
   self.expected_error=(code,message)
   for index in range(2):
    self.call('admin',method,path,body,want=422,code=code,index=index,scenario='boundary');self.marker_case(name+'.'+str(index),dict(HTTP422=True,error_code=code,API_index=index,exact_safe_error=True))
  after=self.ledger();check(after==before,'input refusals mutated public state/session/audit');ro._private(self.work/'query-ledger-after.json',after);check(self.calls==7,'exact login1+six422 HTTP scope');self.marker_case('ledger',dict(all_public_exact=True,HTTP=7,SQL_business_writes=0,source_calls='uncollected',source_call_guard='source inspection: invalid input returns before source/read operation'))
  result=dict(run_id=self.run_id,http_calls=7,target_HTTP=6,normal_added=0,registration_evidence=False,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'query-input-result.json',result);return result
 def cleanup(self):
  ro.Chain.cleanup(self)
  for port in PORTS:
   with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own query input listener remains')
  emit('query_mapping.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,no_fault_DDL=True))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=7,target_HTTP=6,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('query_mapping.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('query_mapping.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
