"""Prepared-only source60/min and first knowledge attempted-audit boundaries.

Real local Prometheus Pull reads; synthetic identities/exposition. Upload probe
runtime evidence proves503/no residue, not an instrumented UploadReader count.
No live acquisition before fresh root admission and the sole matching Runner.
"""
from __future__ import annotations
import argparse,collections,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-api-source-budget-probe-attempt'
PORTS=(18892,18893);PROM_PORT,EXPORT_PORT,RELAY_PORT=18894,18895,18896
ALL_PORTS=(*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT)
BUDGET_ACTORS=('admin','reader','peer','hidden','denied','budget_a');REJECT_ACTOR='budget_b'
WINDOW_GUARD_SECONDS=45
COOLDOWN_GUARD_SECONDS=70
HTTP_CALLS=75
TARGETS=(('GET','/ingest/pull/{id}'),('POST','/knowledge-sources/test'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/connectors'),('POST','/services/{id}/external-targets'),('POST','/uploads'))
REQUIRED_CASES=tuple('source_budget_probe.'+name for name in ('source','fixture','source60','source61','no_rejected_io','source_identity','source_recovery','probe_input','attempted_audit_failed','probe_no_residue','probe_fault_removed','probe_recovery','probe_no_persistence','cleanup'))

def load(name,file):
 spec=importlib.util.spec_from_file_location(name,ROOT/'tools/testlab'/file);value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value);return value
cap=load('source_budget_private_capacity','api_pull_pending_capacity_coverage.py')
probe=load('source_budget_private_probe','api_knowledge_probe_result_audit_coverage.py')
ro,check,Blocked,q=cap.ro,cap.check,cap.Blocked,cap.q
SOURCE,BINARY,RUNTIME,PG_CID,PROM_IMAGE=cap.SOURCE,cap.BINARY,cap.RUNTIME,cap.PG_CID,cap.PROM_IMAGE
cap.PORTS,cap.PROM_PORT,cap.EXPORT_PORT,cap.RELAY_PORT,cap.ALL_PORTS=PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT,ALL_PORTS
ro.PORTS,ro.BASES=PORTS,tuple('http://127.0.0.1:'+str(port) for port in PORTS)

def hashes():
 names=('api_source_budget_probe_attempt_coverage.py','tests/test_api_source_budget_probe_attempt_coverage.py','api_pull_pending_capacity_coverage.py','api_knowledge_probe_result_audit_coverage.py','api_knowledge_sources_interfaces_coverage.py','api_readonly_chain.py','runner.py','store.py','env.py','backend.py','faults.py','identity.py','inventory.py','components.py')
 return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest() for name in names}
cap.hashes=hashes

_private_json=cap.private_json
def private_json(path,value):
 # Owned container guards consume full inspect results in memory; persisted
 # receipts retain status/identity only, never arbitrary Docker error text.
 if Path(path).name.startswith('container-inspect-'):value={key:value[key] for key in ('target','phase','returncode') if key in value}
 return _private_json(path,value)
cap.private_json=private_json

def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=dict(fields['interface'],registration_evidence=False,test_name='FormalSourceBudgetProbeAttempt')
 # Error text and credentials are never copied into evidence/stdout.
 actual={key:value for key,value in actual.items() if key not in ('error','reason','assertion','stderr','stdout','token','cookie','headers')}
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
cap.emit=ro.emit=emit

def admission(path,run_id):
 path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonical UUID/private admission required')
 info=path.lstat();check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid() and info.st_size<=65536,'own bounded regular0600 admission required')
 data=ro.private_json(path)
 expected=dict(status='root-admitted-source-budget-probe-attempt',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,prometheus_image_id=PROM_IMAGE,ports=list(ALL_PORTS),helper_sha256=hashes())
 check(all(data.get(key)==value for key,value in expected.items()),'exact root source-budget/probe admission differs')
 for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','owned_container_cleanup_authorized','source60_real_window_authorized','first_attempted_audit_fault_authorized'):check(data.get(key) is True,'root prerequisite missing '+key)
 conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').as_uri()+'?mode=ro',uri=True,timeout=5)
 try:active=[json.loads(row[0]) for row in conn.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
 finally:conn.close()
 check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE and active[0].get('pid')==os.getppid() and re.fullmatch('[a-f0-9]{64}',str(data.get('manifest_sha256',''))) and active[0].get('manifest_sha')==data['manifest_sha256'],'sole matching actual Runner required')
 return data

class Contract(ro.Contract):
 def canonical(self,method,path):
  parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative package path required')
  canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside source-budget/probe inventory');return canonical,kind

def fault_statements(nonce,actor,app):
 check(all(re.fullmatch('[a-f0-9]{32}',value) for value in (nonce,actor)) and re.fullmatch('[A-Za-z0-9_-]+',app),'exact UUID actor/application required')
 name='source_probe_attempt_fail_'+nonce
 create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+' AND NEW.application_id='+q(app)+" AND NEW.action='v3.knowledge_source.connection.test' AND NEW.target='upload' AND NEW.outcome='attempted' AND NEW.changes=jsonb_build_object('environment_id','test','team_id','') THEN RAISE EXCEPTION 'owned first probe audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
 return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'

def budget_window(value):
 check(value.get('total')==60 and value.get('window')==60 and value.get('new_actor')==0 and value.get('pending')==0,'source61 must see60 current completed attempts and a new actor')
 age=value.get('oldest_age_seconds');check(type(age) in (int,float) and 0<=age<WINDOW_GUARD_SECONDS,'real minute window margin exhausted; no retry')

def source_identity(rows,chain):
 wanted=dict(source_id=chain.source_id,application_id=chain.app,environment_id='test',connector_id=chain.connector,target_id=chain.target,resource_id=chain.resource_id,query_id='capacity_signal')
 expected_digest=hashlib.sha256(json.dumps(dict(expression=chain.expression,name_labels=['service'],label_keys=['signal_id']),separators=(',',':')).encode()).hexdigest()
 check(len(rows)==1 and all(rows[0].get(key)==value for key,value in wanted.items()) and rows[0].get('config_digest')==expected_digest,'exact producer-bound source identity differs')

_cleanup_source=textwrap.dedent(inspect.getsource(cap.Chain.cleanup))
_needle='self.cleanup_deadline = time.monotonic()+40; errors=[]'
check(_cleanup_source.count(_needle)==1,'capacity cleanup40 seam drift')
_scope=dict(cap.__dict__)
exec(compile(_cleanup_source.replace(_needle,'self.cleanup_deadline = self.cleanup_deadline or time.monotonic()+40; errors=[]'),str(__file__)+'[one cleanup40]', 'exec'),_scope)
bounded_cleanup=_scope['cleanup']

class Chain(cap.Chain):
 def __init__(self,data,run_id,permit):
  super().__init__(data,run_id);self.permit=Path(permit);self.contract=Contract(ROOT);self.probe_fault_attempted=False
  self.prom_name='aiops-'+ro.OWNER+'-source-budget-'+self.nonce;self.labels['aiops.testlab.purpose']='source-budget-probe'
  self.state.update(suite=MODULE,ports=list(ALL_PORTS),targets=TARGETS,normal_added=0,counts_as_whole_acceptance=False)
  cap.private_json(self.work/'state.json',self.state)
 def frozen(self):
  admission(self.permit,self.run_id);super().frozen()
 def marker_case(self,name,actual):
  marker='source_budget_probe.'+name;check(marker in REQUIRED_CASES,'unknown source-budget/probe marker');self.frozen();emit(marker,'passed',dict(run_id=self.run_id,**actual))
 def first_red(self,row):
  actual=row.get('actual',{});safe={key:row[key] for key in ('name','stage','error_type','run_id') if key in row}
  safe['actual']={key:actual[key] for key in ('status','error_code','error_type') if key in actual};super().first_red(safe)
 def diagnostic(self,path,value,exclusive=False):
  safe={key:value[key] for key in ('stage','database','user','timeout_seconds','mutation_outcome','error_type','first_failure_preserved') if key in value}
  if 'stderr' in value:safe['sql_error_class']='statement_timeout' if 'statement timeout' in value['stderr'] else 'lock_timeout' if 'lock timeout' in value['stderr'] else 'sql_error'
  return super().diagnostic(path,safe,exclusive=exclusive)
 def extra_identities(self):
  self.owner()
  for name in ('budget_a','budget_b'):
   uid=uuid.uuid4().hex;role='readonly-'+self.nonce+'-'+name
   self.sql('BEGIN; INSERT INTO users(id,email,name,role,password_hash) SELECT '+q(uid)+','+q('member-'+self.nonce+'-'+name+'@testlab.invalid')+','+q(self.marker+' '+name)+",'viewer',password_hash FROM users WHERE email="+q(self.admin_email)+'; DELETE FROM rbac_user_roles WHERE user_id='+q(uid)+'; INSERT INTO rbac_roles(id,name,v3_kind) VALUES('+q(role)+','+q(role)+",'business'); INSERT INTO rbac_user_roles(user_id,role_id) VALUES("+q(uid)+','+q(role)+'); COMMIT;')
   self.users[name]=uid;self.roles[name]=role
  people=json.loads(self.sql("SELECT json_build_object('people',count(*),'human',bool_and(v3_identity_kind='human')) FROM users WHERE email="+q(self.admin_email)+' OR id IN ('+','.join(q(value) for value in self.users.values())+')',read=True))
  check(people==dict(people=7,human=True),'seven independent synthetic human actors required')
 def owner(self,deadline=None):
  query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
  value=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
  check(value==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'exact UUID DB/role tags differ')
 def ledger(self):
  fields=[q(key)+",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+table+' t'+clause+')' for key,table,clause in (('attempts','v3_pull_attempts',''),('identities','v3_pull_source_identities',''),('audit','management_audit'," WHERE action LIKE 'v3.ingest_pull.%'"))]
  return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')',read=True))
 def source_budget(self):
  self.frozen();before=self.ledger();check(before==dict(attempts=[],identities=[],audit=[]),'fresh source budget ledger not empty')
  self.gate.release.set();started=time.monotonic();actors=[];counts=collections.defaultdict(list)
  for number in range(60):
   check(time.monotonic()-started<WINDOW_GUARD_SECONDS,'source60 real window margin exhausted; no retry')
   actor=BUDGET_ACTORS[number%6];key='budget-'+str(number+1)
   self.request(key,actor,number%2,timeout=12)
   check(len(self.gate.arrivals)==number+1 and len(self.gate.receipts)==number+1,'successful request did not produce exactly one real source read')
   self.record(key,200,self.gate.receipts[number]);actors.append(self.actor_id(actor));counts[self.actor_id(actor)].append(self.responses[key]['rows'])
  complete=self.ledger();cap.check_completed(complete,actors,self.source_id,self.app,counts);source_identity(complete['identities'],self)
  check(collections.Counter(actors)==collections.Counter({self.actor_id(actor):10 for actor in BUDGET_ACTORS}),'six distinct actors must each consume exactly10')
  window=json.loads(self.sql("SELECT json_build_object('total',count(*),'window',count(*) FILTER (WHERE started_at>clock_timestamp()-interval '1 minute'),'new_actor',count(*) FILTER (WHERE actor_id="+q(self.users[REJECT_ACTOR])+"),'pending',count(*) FILTER (WHERE status='pending'),'oldest_age_seconds',EXTRACT(EPOCH FROM clock_timestamp()-min(started_at))) FROM v3_pull_attempts WHERE source_id="+q(self.source_id),read=True))
  budget_window(window);check(time.monotonic()-started<WINDOW_GUARD_SECONDS,'source61 real window margin exhausted; no retry')
  self.request('budget-61',REJECT_ACTOR,1,timeout=12);self.record('budget-61',429)
  after=self.ledger();check(after==complete and len(self.gate.arrivals)==len(self.gate.receipts)==60,'rejected source61 changed attempts/audits/identity or performed upstream I/O')
  for name,value in (('before',before),('completed60',complete),('rejected61',after)):cap.private_json(self.work/('source-budget-'+name+'.json'),value)
  self.marker_case('source60',dict(success_HTTP=60,distinct_actors=6,per_actor_attempts=10,real_source_reads=60,minute_window_unchanged=True))
  self.marker_case('source61',dict(status=429,error_code='rate_limited',retry_after=60,new_actor_prior_attempts=0))
  self.marker_case('no_rejected_io',dict(attempt_delta=0,audit_delta=0,upstream_delta=0))
  self.marker_case('source_identity',dict(exact_producer_bound_source=True,source_identity_preserved=True))
  wait_started=time.monotonic();age=None
  while True:
   check(time.monotonic()-wait_started<COOLDOWN_GUARD_SECONDS,'natural source cooldown guard exhausted; no retry')
   age=json.loads(self.sql("SELECT EXTRACT(EPOCH FROM clock_timestamp()-min(started_at)) FROM v3_pull_attempts WHERE source_id="+q(self.source_id),read=True))
   check(type(age) in (int,float) and age>=window['oldest_age_seconds'],'real reservation clock age regressed')
   cap.private_json(self.work/'source-budget-cooldown-progress.json',dict(oldest_age_seconds=age,wait_elapsed_seconds=time.monotonic()-wait_started,recovery_requested=False))
   if age>60:break
   time.sleep(min(5,max(.1,60-age+.1)))
  check(time.monotonic()-wait_started<COOLDOWN_GUARD_SECONDS,'natural source cooldown guard exhausted; no retry')
  self.request('budget-62',REJECT_ACTOR,0,timeout=12);check(len(self.gate.arrivals)==len(self.gate.receipts)==61,'natural recovery did not perform exactly one source read')
  self.record('budget-62',200,self.gate.receipts[60]);actors.append(self.actor_id(REJECT_ACTOR));counts[self.actor_id(REJECT_ACTOR)].append(self.responses['budget-62']['rows'])
  recovered=self.ledger();cap.check_completed(recovered,actors,self.source_id,self.app,counts);check(recovered['identities']==complete['identities'],'natural recovery changed source identity')
  cap.private_json(self.work/'source-budget-recovered62.json',recovered)
  cap.private_json(self.work/'source-budget-window.json',dict(real_window=window,oldest_age_before_recovery=age,wait_elapsed_seconds=time.monotonic()-wait_started,elapsed_seconds=time.monotonic()-started,successful_attempts=61,source_reads=61,target_requests=62))
  self.marker_case('source_recovery',dict(status=200,oldest_age_seconds=age,natural_clock=True,attempts=61,audits=61,real_source_reads=61,failed_request_retried=False))
 def probe_input(self):
  for action in ('view','manage'):self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(value) for value in (self.roles['reader'],'knowledge',action,'application',self.app))+');')
  body=('# Owned first attempted audit fixture\n\n'+self.marker+' inert text.\n').encode();raw,media,filename=probe.base.multipart(self.nonce,body);original=ro.transport
  def multipart(origin,method,path,payload=None,headers=None,**options):
   check(origin==ro.BASES[0] and method=='POST' and path=='/api/v1/uploads' and headers.get('Cookie')==self.cookies['reader'],'exact owned upload substitution required')
   return original(origin,method,path,raw,dict(headers,**{'Content-Type':media}),**options)
  ro.transport=multipart
  try:value,_=self.call('reader','POST','/api/v1/uploads',{},want=201,scenario='fixture')
  finally:ro.transport=original
  check(re.fullmatch('[a-f0-9]{32}',value.get('id','')) and value.get('kind')=='file' and value.get('mime')=='text/markdown' and value.get('name')==filename and value.get('size')==len(body) and value.get('url')=='/api/v1/uploads/'+value['id']+'/content','exact owned upload DTO differs')
  self.probe_payload={key:val for key,val in probe.base.registration(self.app,value['url'],self.marker).items() if key in probe.base.CONNECTION_FIELDS}
  self.marker_case('probe_input',dict(actual_owned_upload=True,upload_sha256=hashlib.sha256(body).hexdigest(),source_type='upload',external_credential_resolution_applicable=False))
 def probe_ledger(self):
  # Only three complete snapshots, outside the real minute budget batch.
  self.frozen();self.owner()
  names=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename<>'management_audit'",read=True))
  check(names and len(names)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',name) for name in names),'bounded exact public inventory required')
  parts=['SELECT '+q(name)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+name+' t' for name in names]
  query="SELECT json_build_object('business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(parts)+") hashes),'audits',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))"
  return json.loads(self.sql(query,read=True))
 def install_probe_fault(self):
  self.frozen();self.owner();self.probe_fault_attempted=True
  cap.private_json(self.work/'probe-attempted-fault-intent.json',dict(database=self.database,actor=self.users['reader'],application=self.app,first_attempted_only=True))
  self.sql(fault_statements(self.nonce,self.users['reader'],self.app)[1])
 def remove_probe_fault(self,deadline=None):
  if not self.probe_fault_attempted:return
  deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4;self.owner(deadline)
  name,_,drop=fault_statements(self.nonce,self.users['reader'],self.app);self.pg(drop,self.database,self.db_user,deadline=deadline)
  absent=json.loads(self.pg("BEGIN READ ONLY; SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL); COMMIT;",self.database,self.db_user,deadline=deadline))
  check(absent==dict(trigger=False,function=False),'owned attempted-audit trigger/function remains');self.probe_fault_attempted=False
 def probe_attempt(self):
  before=self.probe_ledger();primary=None
  try:
   self.install_probe_fault();self.call('reader','POST','/api/v1/knowledge-sources/test',self.probe_payload,want=503,code='audit_unavailable',scenario='failure',index=0)
   failed=self.probe_ledger();probe.assert_audits(before,failed,[],self.users['reader'],self.app)
   self.marker_case('attempted_audit_failed',dict(status=503,error_code='audit_unavailable',attempted_audit_delta=0,result_audit_delta=0))
   self.marker_case('probe_no_residue',dict(all_business_unchanged=True,upload_read_invocations_observed=False,external_credential_resolution_applicable=False,control_flow_audit_before_reader=True))
  except BaseException as error:primary=error
  finally:
   try:self.remove_probe_fault()
   except BaseException as error:
    if primary is None:primary=error
    else:primary.add_note('owned attempted-audit removal secondary: '+type(error).__name__)
  if primary is not None:raise primary
  self.marker_case('probe_fault_removed',dict(trigger=False,function=False))
  self.call('reader','POST','/api/v1/knowledge-sources/test',self.probe_payload,index=1,scenario='failure',validate=probe.base.probe_result)
  after=self.probe_ledger();rows=probe.assert_audits(failed,after,['attempted','success'],self.users['reader'],self.app);probe.assert_audits(before,after,['attempted','success'],self.users['reader'],self.app)
  for name,value in (('before',before),('first-failed',failed),('recovered',after)):cap.private_json(self.work/('probe-attempted-'+name+'.json'),value)
  cap.private_json(self.work/'probe-attempted-summary.json',dict(payload_sha256=hashlib.sha256(json.dumps(self.probe_payload,sort_keys=True).encode()).hexdigest(),upload_read_invocations_observed=False,external_credential_resolution_applicable=False))
  self.marker_case('probe_recovery',dict(status=200,API_index=1,identical_body=True,attempted_audit_id=rows[0]['id'],result_audit_id=rows[1]['id'],explicit_recovery=True))
  self.marker_case('probe_no_persistence',dict(all_business_unchanged=True,source_identity_preserved=True,persistent=False,scope_verified=False,normal_added=0))
 def run(self):
  self.provision();self.start();self.seed();self.extra_identities()
  for actor in ('admin',*self.users):self.login(actor)
  self.start_source();self.setup_http();self.marker_case('fixture',dict(independent_humans=7,source_configured=True,normal_added=0))
  self.source_budget();self.probe_input();self.probe_attempt();self.frozen();check(self.calls==HTTP_CALLS,'exact75 formal HTTP scope differs')
  result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,helper_sha256=hashes(),required_cases=REQUIRED_CASES,http_calls=self.calls,normal_added=0,counts_as_whole_acceptance=False,upload_read_invocations_observed=False,external_credential_resolution_applicable=False,unverified=['upload reader runtime invocation count','external credential resolution/token consumption','expiry/crash/inflight revocation','performance/24h/full chain'])
  cap.private_json(self.work/'source-budget-probe-result.json',result);return result
 def cleanup(self):
  self.cleanup_deadline=self.cleanup_deadline or time.monotonic()+40;primary=None
  try:self.remove_probe_fault(deadline=self.cleanup_deadline)
  except BaseException as error:primary=error
  finally:
   try:bounded_cleanup(self)
   except BaseException as error:
    if primary is None:primary=error
    else:primary.add_note('owned cleanup secondary: '+type(error).__name__)
  if primary is not None:raise primary
  self.marker_case('cleanup',dict(single_cleanup_seconds=40,grace_seconds=45,owned_UUID_only=True,ports_free=list(ALL_PORTS)))

def describe():
 return dict(status='prepared-only',module=MODULE,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,prometheus_image_id=PROM_IMAGE,ports=list(ALL_PORTS),helper_sha256=hashes(),targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=HTTP_CALLS,pull_calls=62,probe_calls=2,login_calls=7,fixture_producer_calls=4,per_source_per_minute=60,per_actor_per_minute=10,real_window_guard_seconds=WINDOW_GUARD_SECONDS,natural_cooldown_guard_seconds=COOLDOWN_GUARD_SECONDS,API_seconds=12,provider_seconds=5,SQL_milliseconds=8000,outer_seconds=300,cleanup_seconds=40,grace_seconds=45,normal_added=0,upload_read_invocations_observed=False,external_credential_resolution_applicable=False)

def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);args=p.parse_args(argv)
 if args.inventory:Contract(ROOT);print(json.dumps(describe()));return 0
 if not(args.live and args.run_id and args.admission):p.error('fresh root --live/run-id/admission required')
 try:
  admission(args.admission,args.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(cap.doctor(),args.run_id,args.admission))
  return 0
 except Blocked:emit('source_budget_probe.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as error:emit('source_budget_probe.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
