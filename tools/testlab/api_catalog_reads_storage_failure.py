"""Two local catalog read contracts: empty MCP and HTTP upload registration.
12 HTTP requests maximum: four fixture requests plus four reads per catalog.
Registration remains unverified; no sync/index/knowledge_document production.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,re,socket,textwrap,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-catalog-reads-storage-failure';PORTS=(18934,18935)
def load(name,path):
 s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
core=load('catalog_reads_private_core',ROOT/'tools/testlab/api_core_reads_storage_failure.py');u=load('catalog_reads_private_upload',ROOT/'tools/testlab/api_upload_audit_rollback_coverage.py');k=load('catalog_reads_private_knowledge',ROOT/'tools/testlab/api_knowledge_sources_interfaces_coverage.py')
ro=core.ro;check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=core.SOURCE,core.BINARY,core.RUNTIME,core.PG_CID
TARGETS=(('GET','/mcp-servers'),('GET','/knowledge-sources'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/uploads'),('POST','/knowledge-sources'))
FAULTS={'/mcp-servers':('ai_extensions','mcp_unavailable','MCP registrations are unavailable'),'/knowledge-sources':('v3_knowledge_source_registrations','knowledge_sources_unavailable','Knowledge source evidence is unavailable')}
REQUIRED_CASES=tuple('catalog_reads.'+n for n in ('http_inputs',*[str(i)+'.'+phase for i in range(2) for phase in ('baseline','repeat','fault','restore','recovery')],'ledger','cleanup'))+('readonly.fresh_database','readonly.api_ready.0','readonly.api_ready.1','readonly.cleanup','readonly.complete')
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);core.TARGETS=TARGETS;core.TABLE='ai_extensions'
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():
 return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_catalog_reads_storage_failure.py','tests/test_api_catalog_reads_storage_failure.py','api_core_reads_storage_failure.py','api_inbox_groups_capacity_boundary.py','api_incident_fault_interfaces_coverage.py','api_upload_audit_rollback_coverage.py','api_knowledge_sources_interfaces_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
core.hashes=hashes;core.inc.hashes=hashes

def evidence(row):return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalCatalogReadsStorageFailure',proof_scope='empty MCP + formally registered upload source read/repeat/no-write/exact table SELECT503/recovery; not nonempty MCP or document/index production')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=core._append
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(core.Contract.__bases__[0]):
 def canonical(self,method,path):
  p=core.urlsplit(path);check(not any((p.scheme,p.netloc,p.query,p.fragment)),'exact local catalog path required')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside two catalogs and bounded formal fixture');return canonical,kind
core.inc.Contract=Contract;ro.Contract=Contract
_src=inspect.getsource(core.cap.gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-catalog-reads-storage-failure'),('conversation_boundaries_authorized','catalog_reads_storage_failure_authorized')):
 check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(core.cap.gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission'];core.admission=admission

def doctor(root):return core.doctor(root)
def verify_error(value,headers):
 check(value==core.ERROR and next((v for key,v in headers.items() if key.lower()=='content-type'),'').split(';')[0]=='application/json','exact catalog SELECT503 error required')
core.verify_error=verify_error

def verify_read(path,value,sid,app):
 if path=='/mcp-servers':
  check(value.get('items')==[] and value.get('complete_v3_dto') is False and isinstance(value.get('kpis'),list) and len(value['kpis'])==3 and all(v.get('value')=='0' for v in value['kpis']),'empty registration MCP catalog differs or fabricates observation')
  check(value.get('unavailable_fields')==['unbound_legacy_registrations','connection_health','agent_authorized_apps','call_metrics','pending_change'],'honest MCP unavailable fields differ')
 else:check(path=='/knowledge-sources','only two catalog read paths');k.evidence(value,[sid],app)

class Chain(core.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id,permit);self.contract=Contract(root);self.temp=self.work/'upload-tmp';self.temp.mkdir(mode=0o700);self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,no_sync_index=True);ro._private(self.work/'state.json',self.state)
 def startup_environment(self):return {'TMPDIR':str(self.temp)}
 def marker_case(self,name,value):
  check('catalog_reads.'+name in REQUIRED_CASES,'unknown catalog marker');self.frozen();emit('catalog_reads.'+name,'passed',dict(run_id=self.run_id,**value))
 def start(self):ro.Chain.start(self)
 def files(self):return u.Chain.files(self)
 def call(self,*args,**kwargs):
  kwargs.setdefault('scenario','failure' if kwargs.get('want')==503 else 'boundary');return core.Chain.call(self,*args,**kwargs)
 def inputs(self):
  key='catalog-read-'+self.nonce;self.call('admin','POST','/api/v1/services',dict(items=[dict(id=key,tier='T1')]),want=201,scenario='fixture');self.app=self.sql('SELECT id FROM applications WHERE key='+q(key),read=True);check(re.fullmatch('[a-f0-9]{32}',self.app),'actual own HTTP application required')
  body=('# Local catalog source\n\n'+self.nonce+' inert content.\n').encode();raw,media,name=u.multipart(self.nonce,body);original=ro.transport
  def wire(base,method,path,payload=None,headers=None,**options):
   check(base==ro.BASES[0] and method=='POST' and path=='/api/v1/uploads' and payload==b'{}','exact own multipart fixture substitution required');hs=dict(headers or {});check(hs.get('Cookie')==self.cookies['admin'] and hs.get('X-AIOps-Environment')=='test','own admin/environment required');hs['Content-Type']=media;return original(base,method,path,raw,hs,**options)
  ro.transport=wire
  try:value,_=self.call('admin','POST','/api/v1/uploads',{},want=201,scenario='fixture',validate=lambda v:u.verify_upload(v,name,body))
  finally:ro.transport=original
  self.upload_id=value['id'];self.payload=k.registration(self.app,value['url'],'Owned upload registration '+self.nonce)
  saved,_=self.call('admin','POST','/api/v1/knowledge-sources',self.payload,want=201,scenario='fixture');check(set(saved)=={'id','registration_status','sync_started'} and str(uuid.UUID(saved['id']))==saved['id'] and saved['registration_status']=='unverified' and saved['sync_started'] is False,'formal registration status differs');self.sid=saved['id']
  observed=json.loads(self.sql("SELECT json_build_object('registration',count(*),'bound',count(*) FILTER(WHERE created_by="+q(self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True))+" AND application_id="+q(self.app)+" AND environment_id='test' AND source_type='upload' AND source_url="+q(value['url'])+")) FROM v3_knowledge_source_registrations WHERE id="+q(self.sid),read=True));check(observed==dict(registration=1,bound=1),'actual registration owner/app/env/upload binding differs')
  counts=json.loads(self.sql("SELECT json_build_object('docs',(SELECT count(*) FROM knowledge_documents),'reads',(SELECT count(*) FROM v3_knowledge_source_reads),'index',(SELECT count(*) FROM knowledge_generations),'mcp',(SELECT count(*) FROM ai_extensions WHERE kind='mcp'))",read=True));check(counts==dict(docs=0,reads=0,index=0,mcp=0),'registration fabricated documents/sync/index/MCP')
  check(self.files()=={},'formal bytea upload left TMPDIR residue');self.marker_case('http_inputs',dict(formal_service=True,formal_upload=True,formal_registration=True,registration_status='unverified',sync_started=False,SQL_business_writes=0,no_sync_index=True))
 def exercise(self,i,path,baseline):
  table,code,message=FAULTS[path];core.TABLE=table;core.ERROR=dict(error=dict(code=code,message=message))
  for phase,index in (('baseline',0),('repeat',1)):
   self.call('admin','GET','/api/v1'+path,want=200,index=index,validate=lambda v:verify_read(path,v,self.sid,self.app));check(self.ledger()==baseline and self.files()=={},'catalog read/repeat changed complete public ledger or TMPDIR');self.marker_case(str(i)+'.'+phase,dict(HTTP200=True,API_index=index,all_public_unchanged=True))
  primary=None
  try:
   self.revoke();ro._private(self.work/('catalog-acl-'+str(i)+'-before.json'),self.acl_before);ro._private(self.work/('catalog-acl-'+str(i)+'-revoked.json'),ro.private_json(self.work/'acl-revoked.json'));self.call('admin','GET','/api/v1'+path,want=503,code=code,index=0);check(self.ledger()==baseline and self.files()=={},'failed catalog read changed full public ledger/TMPDIR');self.marker_case(str(i)+'.fault',dict(HTTP503=True,error_code=code,table=table,actual_SELECT_denied=True,all_public_unchanged=True))
  except BaseException as error:primary=error
  finally:
   try:self.restore()
   except BaseException as error:
    if primary is None:primary=error
    else:ro._private(self.work/'restore-secondary.json',dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
  if primary is not None:raise primary
  ro._private(self.work/('catalog-acl-'+str(i)+'-restored.json'),self.acl_before);self.marker_case(str(i)+'.restore',dict(original_effective_ACL_restored=True,no_retry=True))
  self.call('admin','GET','/api/v1'+path,want=200,index=1,validate=lambda v:verify_read(path,v,self.sid,self.app));check(self.ledger()==baseline and self.files()=={},'recovered catalog read changed full ledger/TMPDIR');self.marker_case(str(i)+'.recovery',dict(HTTP200=True,opposite_API=True,same_cookie=True,all_public_unchanged=True))
 def run(self):
  self.provision();self.start();self.login('admin');self.inputs();baseline=self.ledger();ro._private(self.work/'catalog-ledger-baseline.json',baseline)
  for i,(_,path) in enumerate(TARGETS):self.exercise(i,path,baseline)
  check(self.calls==12 and self.ledger()==baseline and not self.acl_fault,'exact12HTTP/final ACL/complete public ledger differs');ro._private(self.work/'catalog-ledger-final.json',baseline);self.marker_case('ledger',dict(target_GET=8,fixture_HTTP=4,all_public_tables=len(baseline),sessions_audit_unchanged=True,TMPDIR_unchanged=True))
  result=dict(run_id=self.run_id,http_calls=12,target_GET=8,fixture_HTTP=4,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY,no_nonempty_MCP=True,no_document_index_production=True);ro._private(self.work/'catalog-read-result.json',result);return result
 def cleanup(self):
  ro.Chain.cleanup(self)
  for port in PORTS:
   with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own catalog listener remains')
  emit('catalog_reads.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,unrestored_fault_resource_retired=self.acl_fault))
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,faults=FAULTS,required_cases=REQUIRED_CASES,http_calls=12,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('catalog_reads.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('catalog_reads.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
