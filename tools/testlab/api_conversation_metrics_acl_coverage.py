"""One existing metrics conversation application-ACL entry rejection.
Real current Prom; unchanged fixed model protocol Mock, no real inference.
Root alone may --live after fresh UUID/owned-resource admission.
"""
from __future__ import annotations
import argparse,base64,copy,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,subprocess,sys,textwrap,threading,time,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-conversation-metrics-application-acl'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb';BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70';RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7';PORTS=(18860,18861);PROM_PORT,EXPORT_PORT,FIXTURE_PORT=18862,18863,18864;ALL_PORTS=(*PORTS,PROM_PORT,EXPORT_PORT,FIXTURE_PORT)
def load(name,path):
 s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
ro=load('metrics_acl_private_readonly',ROOT/'tools/testlab/api_readonly_chain.py');old=sys.modules.get('api_readonly_chain')
try:sys.modules['api_readonly_chain']=ro;gen=load('metrics_acl_private_generation',ROOT/'tools/testlab/api_rule_generation_functional_coverage.py')
finally:
 if old is None:sys.modules.pop('api_readonly_chain',None)
 else:sys.modules['api_readonly_chain']=old
check,Blocked,q=ro.check,ro.Blocked,ro.quote
gen.SOURCE_SHA,gen.BINARY_SHA,gen.RUNTIME_SHA=SOURCE,BINARY,RUNTIME;gen.PORTS,gen.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS);gen.PROM_PORT,gen.EXPORT_PORT,gen.FIXTURE_PORT=PROM_PORT,EXPORT_PORT,FIXTURE_PORT
TARGETS=(('POST','/conversations/{id}/messages'),);SUPPORT=(('POST','/metrics/ask'),('POST','/auth/login'),('POST','/services'),('POST','/connectors'),('POST','/services/{id}/external-targets'),('GET','/conversations/{id}'))
gen.TARGETS,gen.SUPPORT=TARGETS,SUPPORT
TABLES=('v3_metrics_questions','v3_conversations','v3_conversation_messages','v3_conversation_replies','v3_conversation_events','management_audit')
REQUIRED_CASES=tuple('metrics_acl.'+n for n in ('identity','real_source','bindings','completed','revoked','forbidden','restore','recovered','ledger','cleanup'))+('readonly.fresh_database','generation.api_ready.1.0','generation.api_ready.1.1','generation.api_ready.2.0','generation.api_ready.2.1','readonly.cleanup','readonly.complete')
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_conversation_metrics_acl_coverage.py','tests/test_api_conversation_metrics_acl_coverage.py','api_rule_generation_functional_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):
 mode='protocol_mock' if row.get('path','').startswith('/api/v1/conversations') or row.get('path')=='/api/v1/metrics/ask' else 'real_local'
 return dict(row,registration_evidence=False,evidence_mode=mode,upstream_mode=mode,source_tree_sha=SOURCE,binary_sha256=BINARY,test_name='FormalMetricsConversationApplicationACL',proof_scope='real formal stored source/application ACL; actual current Prom with synthetic current samples; unchanged fixed model protocol Mock; no whole acceptance')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='protocol_mock',**fields)),flush=True)
gen.emit,gen.interface_evidence,gen.hashes=emit,evidence,hashes
# Only exact private helper adaptations, never mutate canonical modules.
def adapt(fn,replacements,namespace):
 source=inspect.getsource(fn)
 for old,new in replacements:check(source.count(old)==1,'private source anchor changed');source=source.replace(old,new)
 scope=dict(namespace);exec(compile(textwrap.dedent(source),str(__file__)+'[private adapter]','exec'),scope);return scope[fn.__name__]
gen.context=adapt(gen.context,[('timeout=20','timeout=12')],gen.__dict__)
def remaining(deadline,cap):
 budget=min(cap,deadline-time.monotonic());check(budget>0,'shared cleanup deadline exhausted');return budget
def absence(result,name):
 check(result.returncode==1 and result.stdout.strip() in ('','[]') and result.stderr.strip() in ('Error: No such object: '+name,'error: no such object: '+name,'Error: No such container: '+name,'Error response from daemon: No such container: '+name),'exact own absence unconfirmed; no name adoption');return True
def admission(path,run_id):
 p=Path(path);check(str(uuid.UUID(run_id))==run_id and p.is_absolute() and p.resolve()==p and p.is_relative_to(ROOT/'.local/testlab'),'canonical UUID/private admission required');s=p.lstat();check(stat.S_ISREG(s.st_mode) and stat.S_IMODE(s.st_mode)==0o600 and s.st_uid==os.getuid(),'own0600 permit required');d=ro.private_json(p)
 expected=dict(status='root-admitted-conversation-metrics-acl',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,prom_image_id=gen.PROM_IMAGE_ID,ports=list(ALL_PORTS),helper_sha256=hashes())
 check(all(d.get(k)==v for k,v in expected.items()),'frozen admission identity differs')
 for n in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','conversation_metrics_acl_authorized'):check(d.get(n) is True,'root prerequisite missing '+n)
 c=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
 try:rows=[json.loads(r[0]) for r in c.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
 finally:c.close()
 check(len(rows)==1 and rows[0].get('selected_suite')==MODULE and rows[0].get('source_tree_sha')==SOURCE and rows[0].get('pid')==os.getppid() and re.fullmatch('[a-f0-9]{64}',d.get('manifest_sha256','')) and rows[0].get('manifest_sha')==d['manifest_sha256'],'sole matching fresh Runner required');return d
def doctor(root):
 gen.free_ports();d=gen.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes drift');info=ro.inspect_pg(d['postgres_container']);check(info['Id']==PG_CID and info['HostConfig']['Memory']==2<<30 and info['HostConfig']['NanoCpus']==4_000_000_000,'exactPG4CPU2GiB required');return dict(d,runtime_sha=RUNTIME,helper_sha256=hashes())
_owner=None
class Server:
 """Owned bounded loopback workers; preserves original Fixture protocol bytes."""
 def __init__(self,port,handler):
  owner=_owner;check(owner is not None,'fixture server lacks invocation owner');self.lock=threading.Lock();self.workers=[];self.sockets=[];self.closed=False;server=self
  class HTTP(gen.ThreadingHTTPServer):
   block_on_close=False
   def process_request(self,request,address):
    request.settimeout(6)
    def work():
     try:self.finish_request(request,address)
     finally:self.shutdown_request(request)
    t=threading.Thread(target=work,daemon=True)
    with server.lock:server.sockets.append(request);server.workers.append(t)
    t.start()
  self.server=HTTP(('127.0.0.1',port),handler);owner.servers.append(self)
  self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
 def close(self,deadline=None):
  if self.closed:return
  deadline=deadline if deadline is not None else time.monotonic()+4
  stopper=threading.Thread(target=self.server.shutdown,daemon=True);stopper.start();stopper.join(remaining(deadline,4));self.server.server_close()
  with self.lock:sockets=list(self.sockets);workers=list(self.workers)
  for s in sockets:
   try:s.shutdown(socket.SHUT_RDWR)
   except OSError:pass
   s.close()
  self.thread.join(remaining(deadline,4))
  for t in workers:t.join(remaining(deadline,4))
  check(not stopper.is_alive() and not self.thread.is_alive() and not any(t.is_alive() for t in workers),'owned fixture worker/listener incomplete');self.closed=True
Fixture=type('Fixture',(gen.Fixture,),{})
Fixture.__init__=adapt(gen.Fixture.__init__,[
 ("                    env,path=f.validate_source(self.path)","                    with f.lock:f.source_entries+=1\n                    env,path=f.validate_source(self.path)"),
 ("                    check(self.path=='/v1/chat/completions'", "                    with f.lock:f.model_entries+=1\n                    check(self.path=='/v1/chat/completions'"),
 ('self.prom_mode=\'normal\'','self.source_entries=0;self.model_entries=0;self.prom_mode=\'normal\'')],dict(gen.__dict__,Server=Server))
gen.Fixture=Fixture
# One shared cleanup deadline, including ACL restore/fixture/Prom/UUID DB.
bounded_pg_cleanup=adapt(ro.Chain.cleanup,[('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')],ro.__dict__)
def unchanged(before,after):check(before==after,'target rejection/read changed complete business/source/audit ledger or request counters')
def grant_key(row):return (row.get('role_id'),row.get('module'),row.get('action'),row.get('scope_kind'),row.get('scope_id'))
class Chain(gen.Chain):
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id);self.permit=Path(permit);self.servers=[];self.saved_grant=None;self.grant_intent=False;self.cleanup_deadline=None;self.state.update(suite=MODULE,normal_added=0,ports=list(ALL_PORTS));ro._private(self.work/'state.json',self.state)
 def frozen(self):
  admission(self.permit,self.run_id);check(self.data['source_tree_sha']==SOURCE and self.data['binary_sha256']==BINARY and self.data['runtime_sha']==RUNTIME,'source/binary/runtime drift');super().frozen()
 def marker_case(self,name,value,status='passed'):
  if name=='generation.real_source':name='metrics_acl.real_source'
  if name=='generation.native_bindings':name='metrics_acl.bindings';value=dict(applications=1,connectors=1,targets=1,queries=2,business_SQL_seed=False)
  self.frozen();emit(name,status,dict(run_id=self.run_id,**value))
 def docker(self,args,timeout=8,deadline=None):return subprocess.run(['docker',*args],capture_output=True,text=True,timeout=remaining(deadline,timeout) if deadline else timeout)
 def inspect_prom(self,deadline=None):
  r=self.docker(['inspect',self.prom_cid],deadline=deadline);check(r.returncode==0,'own held Prom inspect failed');rows=json.loads(r.stdout);check(len(rows)==1,'one held Prom required');return rows[0]
 def start_prom(self):
  absence(self.docker(['inspect',self.prom_name]),self.prom_name);self.prom_attempted=True;cidfile=self.work/'prom.cid';ro._private(self.work/'prom-intent.json',dict(name=self.prom_name,image=gen.PROM_IMAGE_ID,run_id=self.run_id))
  args=['create','--pull','never','--name',self.prom_name,'--cidfile',str(cidfile),'--label','aiops.testlab.owner='+ro.OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-generation','--cpus','.5','--memory','256m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),'--tmpfs','/prometheus:'+self.tmpfs,'-p','127.0.0.1:'+str(PROM_PORT)+':9090','--mount','type=bind,source='+str(self.work/'prometheus.json')+',target=/etc/prometheus/owned.json,readonly',gen.PROM_IMAGE_ID,*self.prom_args]
  with ro.acquisition_signals():
   try:r=self.docker(args,timeout=15)
   finally:
    if cidfile.exists() and not cidfile.is_symlink():
     cid=cidfile.read_text().strip()
     if re.fullmatch('[a-f0-9]{64}',cid):self.prom_cid=cid;cidfile.chmod(0o600)
  check(self.prom_cid and r.returncode==0 and r.stdout.strip()==self.prom_cid,'own Prom create unknown; no name adoption');gen.guard_prom(self.inspect_prom(),self,live=False)
  check(self.docker(['start',self.prom_cid],timeout=15).returncode==0,'own Prom start failed');deadline=time.monotonic()+20
  while time.monotonic()<deadline:
   self.guard_prom()
   try:
    if gen.prom_get('/-/ready')[0]==200 and self.fixture.scrapes>=2:break
   except OSError:pass
   time.sleep(.2)
  else:raise Blocked('real current Prom readiness deadline')
  actual=self.docker(['exec',self.prom_cid,'/bin/cat','/etc/prometheus/owned.json']);check(actual.returncode==0 and actual.stdout.encode()==(self.work/'prometheus.json').read_bytes(),'actual source mount differs');status,raw=gen.prom_get('/api/v1/status/config');check(status==200 and 'host.docker.internal:'+str(EXPORT_PORT) in json.loads(raw)['data']['yaml'],'loaded source exporter differs');self.source_started=time.monotonic();ro._private(self.work/'prom-proof.json',dict(cid=self.prom_cid,image=gen.PROM_IMAGE_ID,config_sha256=digest(self.work/'prometheus.json'),loaded=json.loads(raw),scrapes=self.fixture.scrapes));self.marker_case('metrics_acl.real_source',dict(cpu=.5,memory=268435456,current_only=True))
 def role_grants(self,deadline=None):
  query="SELECT json_build_object('db',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
  tags=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline));check(tags==dict(db=self.owner_tag,role=self.owner_tag),'exact UUID database/role tags differ')
  statement="SELECT COALESCE(jsonb_agg(to_jsonb(g) ORDER BY to_jsonb(g)::text),'[]'::jsonb) FROM v3_role_grants g WHERE role_id="+q(self.roles['reader'])
  return json.loads(self.pg('BEGIN READ ONLY; '+statement+'; COMMIT;',self.database,self.db_user,deadline=deadline))
 def acl_setup(self):
  role=self.roles['reader'];app=self.apps['test']['app'];self.sql('BEGIN; DELETE FROM v3_role_grants WHERE role_id='+q(role)+" AND module='services'; INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES("+','.join(q(v) for v in (role,'services','view','application',app))+'); COMMIT;')
  assigned=json.loads(self.sql("SELECT COALESCE(json_agg(role_id ORDER BY role_id),'[]'::json) FROM rbac_user_roles WHERE user_id="+q(self.users['reader']),read=True));check(assigned==[role],'actor has another authority role');rows=self.role_grants();services=[v for v in rows if v['module']=='services'];check(len(services)==1 and grant_key(services[0])==(role,'services','view','application',app),'sole exact app authority required');self.saved_grant=services[0];self.original_grants=rows
  required={(role,m,a,'environment','test') for m,a in (('environment','view'),('conversation','view'),('conversation','change'),('connectors','view'),('models','view'))};check(required<={grant_key(v) for v in rows},'entry environment/conversation/connector/model grants must remain valid');self.marker_case('metrics_acl.identity',dict(exact_app_witness=True,alternate_service_grants=0))
 def revoke(self):
  self.frozen();ro._private(self.work/'grant-restore-intent.json',self.saved_grant);self.grant_intent=True;key=self.saved_grant
  where=' AND '.join(n+'='+q(key[n]) for n in ('role_id','module','action','scope_kind','scope_id'));self.sql('DELETE FROM v3_role_grants WHERE '+where);check(self.role_grants()==[v for v in self.original_grants if v!=key],'exact sole grant removal/readback differs');self.marker_case('metrics_acl.revoked',dict(sole_application_grant_removed=True,environment_conversation_connectors_retained=True))
 def restore(self,deadline):
  if not self.grant_intent:return
  rows=self.role_grants(deadline);expected=[v for v in self.original_grants if v!=self.saved_grant];check(rows in (expected,self.original_grants),'unknown grant state; refusing overwrite')
  if rows==expected:
   names=('role_id','module','action','scope_kind','scope_id','created_at');row=self.saved_grant;sql='INSERT INTO v3_role_grants('+','.join(names)+') VALUES('+','.join(q(row[n]) for n in names)+');';self.pg(sql,self.database,self.db_user,deadline=deadline)
  check(self.role_grants(deadline)==self.original_grants,'exact grant/timestamp restore differs');self.grant_intent=False
 def ledger(self):
  value=super().ledger();excluded=','.join(q(n) for n in (*TABLES,'v3_role_grants'));names=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+excluded+')',read=True));check(names and len(names)<512 and all(re.fullmatch('[a-z][a-z0-9_]*',n) for n in names),'bounded public table names required')
  parts=['SELECT '+q(n)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+n+' t' for n in names];value['other_public']=json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h',read=True))
  where=' AND '.join(n+'='+q(self.saved_grant[n]) for n in ('role_id','module','action','scope_kind','scope_id'));value['other_grants']=self.sql("SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(g) ORDER BY to_jsonb(g)::text),'[]'::jsonb)::text,'UTF8')),'hex') FROM v3_role_grants g WHERE NOT ("+where+')',read=True);return value
 def counters(self):
  with self.fixture.lock:return dict(source=self.fixture.source_entries,model=self.fixture.model_entries)
 def call(self,*a,**kw):
  old=ro.transport
  def wire(origin,method,path,payload=None,headers=None,timeout=12):
   check(timeout==12,'API12 contract changed');result=old(origin,method,path,payload,headers,timeout)
   if '/conversations/' in path or path=='/api/v1/metrics/ask':
    status,raw,hs=result;ro._private(self.work/('metrics-acl-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,status=status,response_body=raw.decode('utf-8','replace'),response_sha256=hashlib.sha256(raw).hexdigest(),request_sha256=hashlib.sha256(payload or b'').hexdigest(),binary_sha256=BINARY,source_tree_sha=SOURCE))
    if kw.get('code')=='forbidden':
     value=json.loads(raw);error=value.get('error') if isinstance(value,dict) else None;check(next((v for k,v in hs.items() if k.lower()=='content-type'),'').split(';')[0]=='application/json' and set(value)=={'error'} and isinstance(error,dict) and set(error)=={'code','message'} and error['code']=='forbidden' and isinstance(error['message'],str) and 0<len(error['message'])<=256,'sanitized refusal JSON differs')
   return result
  ro.transport=wire
  try:return super().call(*a,**kw)
  finally:ro.transport=old
 def target(self,conv,detail,baseline):
  before=self.counters();self.revoke();self.call('reader','POST','/api/v1/conversations/'+conv+'/messages',dict(text='Owned application ACL boundary turn',model='openai:readonly-synthetic'),want=403,code='forbidden',scenario='authorization',index=1);unchanged(baseline,self.ledger());unchanged(before,self.counters());ro._private(self.work/'rejected-ledger.json',self.ledger());self.marker_case('metrics_acl.forbidden',dict(status=403,business_audit_delta=0,source_model_delta=0))
  self.restore(time.monotonic()+4);self.marker_case('metrics_acl.restore',dict(exact_original_grant_timestamp=True));value=self.detail('reader',conv,index=1);unchanged(detail,value);unchanged(baseline,self.ledger());unchanged(before,self.counters());self.marker_case('metrics_acl.recovered',dict(detail200=True,no_new_POST202=True));self.marker_case('metrics_acl.ledger',dict(all_business_and_audit_unchanged=True,source_binding_unchanged=True,source_model_requests_unchanged=True))
 def run(self):
  global _owner
  self.provision();self.start();self.seed();self.login('admin');self.login('reader');self.prepare_apps();self.acl_setup();_owner=self
  try:self.prepare_source()
  finally:_owner=None
  # Only the existing producer's current nonempty grid preflight, not normal().
  deadline=time.monotonic()+35
  while time.monotonic()<deadline:
   now=gen.datetime.datetime.now(gen.datetime.timezone.utc).replace(microsecond=0);query=gen.urlencode(dict(query=self.apps['test']['expressions']['request_rate'],start=(now-gen.datetime.timedelta(minutes=15)).isoformat(),end=now.isoformat(),step='30',limit='100',timeout='5s'));status,raw=gen.prom_get('/api/v1/query_range?'+query)
   if status==200 and json.loads(raw)['data']['result'] and json.loads(raw)['data']['result'][0]['values']:break
   time.sleep(.25)
  else:raise Blocked('real nonempty current grid missing')
  conv=self.ask(self.body('application-acl'),wait=False);self.wait_reply(conv);check(self.sql('SELECT active_reply_id IS NULL FROM v3_conversations WHERE id='+q(conv),read=True)=='t','terminal producer still active');detail=self.detail('reader',conv);baseline=self.ledger();check(self.counters()==dict(source=1,model=1),'exact one actual source/model protocol request required');check(baseline['v3_conversations'][0]['metrics_source_required'] is True and baseline['v3_metrics_questions'][0]['application_id']==self.apps['test']['app'],'formal source binding missing');ro._private(self.work/'completed-ledger.json',baseline);ro._private(self.work/'protocol-source-receipts.json',self.fixture.snapshot());self.marker_case('metrics_acl.completed',dict(metrics_source_required=True,terminal_completed=True,source_requests=1,model_protocol_requests=1,real_model=False));self.target(conv,detail,baseline)
  result=dict(run_id=self.run_id,http_calls=self.calls,normal_added=0,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,model_protocol_mock=True,binary_sha256=BINARY,source_tree_sha=SOURCE);ro._private(self.work/'result.json',result);return result
 def cleanup(self):
  self.cleanup_deadline=time.monotonic()+40;errors=[]
  try:self.restore(min(self.cleanup_deadline,time.monotonic()+4))
  except BaseException as e:errors.append(type(e).__name__)
  for s in self.servers:
   try:s.close(self.cleanup_deadline)
   except BaseException as e:errors.append(type(e).__name__)
  try:
   if self.prom_cid:
    cid=self.prom_cid;gen.guard_prom(self.inspect_prom(self.cleanup_deadline),self,live=False)
    check(self.docker(['stop','--time','1',cid],deadline=self.cleanup_deadline).returncode==0,'Prom stop failed');gen.guard_prom(self.inspect_prom(self.cleanup_deadline),self,live=False);check(self.docker(['rm',cid],deadline=self.cleanup_deadline).returncode==0,'Prom remove failed');absence(self.docker(['inspect',cid],deadline=self.cleanup_deadline),cid);self.prom_cid=None
   elif self.prom_attempted:absence(self.docker(['inspect',self.prom_name],deadline=self.cleanup_deadline),self.prom_name)
  except BaseException as e:errors.append(type(e).__name__)
  try:bounded_pg_cleanup(self)
  except BaseException as e:errors.append(type(e).__name__)
  absent={}
  for port in ALL_PORTS:
   with socket.socket() as s:s.settimeout(.3);absent[str(port)]=s.connect_ex(('127.0.0.1',port))!=0
  ro._private(self.work/'metrics-acl-cleanup.json',dict(errors=errors,ports_absent=absent,total_seconds=40,restore_subset_seconds=4));check(not errors and all(absent.values()),'shared cleanup incomplete; first failure retained');emit('metrics_acl.cleanup','passed',dict(single40=True,grace45=True,ports_absent=True))
Chain.prepare_apps=adapt(gen.Chain.prepare_apps,[("for env in ('test','staging'):","for env in ('test',):")],gen.__dict__)
def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:gen.Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,targets=TARGETS,ports=ALL_PORTS,required_cases=REQUIRED_CASES,normal_added=0,estimated_HTTP_calls=9,API_seconds=12,outer_seconds=420,cleanup_seconds=40,restore_subset_seconds=4,grace_seconds=45,upstream_model='protocol_mock')));return 0
 if not(a.live and a.run_id and a.admission):p.error('root --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with gen.lease(ROOT),gen.context(),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('metrics_acl.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('metrics_acl.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
