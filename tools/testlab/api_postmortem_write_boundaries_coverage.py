"""Prepared-only formal postmortem draft/create boundary batch, no publish or full chain.
HTTP-owned manual case and CAS resolve only; SQL is limited to owned identity/ACL.
"""
from __future__ import annotations
import argparse,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-api-postmortem-write-boundaries';PORTS=ALL_PORTS=(18880,18881)
spec=importlib.util.spec_from_file_location('pm_boundaries_private_audit',ROOT/'tools/testlab/api_postmortem_audit_rollback_coverage.py')
pm=importlib.util.module_from_spec(spec);spec.loader.exec_module(pm)
ro=pm.ro;incident=pm.incident;check=pm.check;Blocked=pm.Blocked;q=pm.q
SOURCE_SHA,BINARY_SHA,RUNTIME_SHA,PG_CID=pm.SOURCE_SHA,pm.BINARY_SHA,pm.RUNTIME_SHA,pm.PG_CID
TARGETS=(('GET','/postmortems/draft'),('POST','/postmortems'))
SUPPORT=tuple(x for x in pm.SUPPORT if x not in (('GET','/postmortems/records'),('GET','/postmortems')))
INVALID_DRAFT_PATH='/api/v1/postmortems/draft?incident='
HTTP_PLAN=(dict(marker='postmortem_boundaries.draft_invalid_incident',method='GET',path='/postmortems/draft',query='incident=',scenario='boundary',expected_status=422,expected_error_code='invalid_incident',expected_error_message='Select a resolved incident',registration_evidence=False),)
CASES=('http_inputs','draft_invalid_incident','open_source','resolved_source','hidden_source','no_view','no_manage','acl_restored','invalid_status','duplicate_sections','empty_review','unknown_field','missing_revision','stale_revision','case_seq','create_control','duplicate','draft_excludes_existing','cleanup')
REQUIRED_CASES=tuple('postmortem_boundaries.'+s for s in CASES)
ro.PORTS,ro.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS)
pm.PORTS=PORTS

def hashes():
 return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in ('api_postmortem_write_boundaries_coverage.py','tests/test_api_postmortem_write_boundaries_coverage.py','api_postmortem_audit_rollback_coverage.py','api_incident_fault_interfaces_coverage.py','api_readonly_chain.py','runner.py','store.py','faults.py')}
pm.hashes=hashes

def emit(name,status,actual,**fields):
 if 'interface' in fields:fields['interface']=dict(fields['interface'],registration_evidence=False,test_name='FormalPostmortemWriteBoundaries')
 fields.pop('module',None);fields.pop('evidence_mode',None)
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
ro.emit=emit
# Persist unqualified records before emit as well as in the Runner protocol.
source=inspect.getsource(ro.Chain.call)
needle="row['registration_evidence'] = 200 <= status < 300 and scenario == 'normal'"
check(source.count(needle)==1,'private registration seam drift')
scope=dict(ro.__dict__)
# Preserve incident transport substitution (ingress bearer/idempotency), via the
# private module's current transport rather than a captured pre-substitution.
scope['transport']=lambda *a,**kw:ro.transport(*a,**kw)
exec(compile(textwrap.dedent(source.replace(needle,"row['registration_evidence'] = False\n            row['test_name'] = 'FormalPostmortemWriteBoundaries'")),str(__file__)+'[private call]','exec'),scope)
ro.Chain.call=scope['call']
cleanup_source=inspect.getsource(ro.Chain.cleanup)
check(cleanup_source.count('deadline = time.monotonic() + 40')==1,'private cleanup deadline seam drift')
cleanup_scope=dict(ro.__dict__)
exec(compile(textwrap.dedent(cleanup_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')),str(__file__)+'[private cleanup]','exec'),cleanup_scope)
bounded_cleanup=cleanup_scope['cleanup']

class Contract(pm.Contract):
 def canonical(self,method,path):
  # Permit only this exact malformed fixture; keep every released inventory
  # restriction for other draft queries and all other routes.
  value=ro.Contract.canonical(self,method,path) if method=='GET' and path==INVALID_DRAFT_PATH else super().canonical(method,path)
  check((method,value[0].removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside PM draft/create boundaries')
  return value

def doctor(root):return pm.doctor(root)

def admission(path,run_id):
 path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonical private admission required')
 st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 permit required')
 permit=ro.private_json(path)
 expected=dict(status='root-admitted-postmortem-write-boundaries',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha256=RUNTIME_SHA,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
 check(all(permit.get(k)==v for k,v in expected.items()),'root PM admission identity differs')
 for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','postmortem_boundaries_authorized'):check(permit.get(key) is True,'missing root prerequisite '+key)
 conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').as_uri()+'?mode=ro',uri=True,timeout=5)
 try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
 finally:conn.close()
 check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE_SHA and active[0].get('pid')==os.getppid() and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256'],'sole matching actual Runner required')
 return permit

def invalid_inputs(body):
 return (
  ('invalid_status',dict(body,status='published'),'invalid_postmortem'),
  ('duplicate_sections',dict(body,sections=body['sections']*2),'invalid_postmortem'),
  ('empty_review',dict(body,sections=[dict(key='root',title='Root',text=' ')]),'invalid_postmortem'),
  ('unknown_field',dict(body,unknown_owned_field=True),'invalid_body'),
  ('missing_revision',dict(body,source_revision=''),'source_revision_required'),
  ('stale_revision',dict(body,source_revision='case:1'),'postmortem_source_changed'),
  ('case_seq',dict(body,source_close_event_seq=1),'invalid_postmortem'))

def same(before,after):check(before==after,'refusal/read changed complete PM/source/audit ledger')

def verify_invalid_draft(status,value):
 check(status==422 and value==dict(error=dict(code='invalid_incident',message='Select a resolved incident')),'empty incident draft exact sanitized422 DTO differs')

def other_public_query(names):
 # 保留原完整排序、空表语义及摘要；无收益的 VALUES 实验不进入运行入口。
 digest="encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex')"
 original=['SELECT '+q(t)+' AS name,'+digest+' AS digest FROM public.'+t+' t' for t in names]
 return 'SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(original)+') hashes'

class Chain(pm.Chain):
 def __init__(self,root,data,run_id,permit):
  self.permit=Path(permit);super().__init__(root,data,run_id);self.contract=Contract(root)
  self.acl_original=None;self.cleanup_deadline=None
  self.ledger_observation_index=0;self.ledger_sql_index=0;self.ledger_active=None
  self.other_public_status='unread'
  self.state.update(suite=MODULE,targets=TARGETS,normal_added=0,counts_as_whole_acceptance=False);ro._private(self.work/'state.json',self.state)
 def frozen(self):admission(self.permit,self.run_id);super().frozen()
 def marker_case(self,suffix,actual):
  check('postmortem_boundaries.'+suffix in REQUIRED_CASES,'unknown PM boundary marker');self.frozen();emit('postmortem_boundaries.'+suffix,'passed',dict(run_id=self.run_id,**actual))
 def call(self,*a,**kw):
  original=ro.transport
  def wire(origin,method,path,body=None,headers=None,**opts):
   result=original(origin,method,path,body,headers,**opts)
   if '/postmortems' in path:
    status,raw,_=result;check(len(raw)<=16384,'bounded PM response exceeded')
    if method=='GET' and path==INVALID_DRAFT_PATH:verify_invalid_draft(status,json.loads(raw))
    ro._private(self.work/('postmortem-boundary-wire-'+str(self.calls)+'.json'),dict(method=method,path=path,API_index=ro.BASES.index(origin),status=status,request_bytes=len(body or b''),request_sha256=hashlib.sha256(body or b'').hexdigest(),response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),response_body=raw.decode('utf-8','replace'),source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,request_body_stored=False))
   return result
  ro.transport=wire
  try:return super().call(*a,**kw)
  finally:ro.transport=original
 def refuse(self,name,user,method,path,body,want,code,index=1,environment='test'):
  before=self.ledger();self.call(user,method,path,body,want=want,code=code,scenario='authorization' if want in (403,404) else 'boundary',index=index,environment=environment);after=self.ledger();same(before,after)
  ro.append_json(self.work/'refusal-ledger-proofs.jsonl',dict(name=name,before_sha256=hashlib.sha256(json.dumps(before,sort_keys=True).encode()).hexdigest(),after_sha256=hashlib.sha256(json.dumps(after,sort_keys=True).encode()).hexdigest(),other_public_tables=len(after['other_public']),sessions_excluded=True))
  self.marker_case(name,dict(status=want,error_code=code,complete_ledger_unchanged=True))
 def ledger(self):
  self.ledger_observation_index+=1;self.ledger_sql_index=0;self.ledger_active=self.ledger_observation_index
  try:return self.read_ledger()
  finally:self.ledger_active=None
 def pg(self,statement,*args,**kwargs):
  if self.ledger_active is None:return super().pg(statement,*args,**kwargs)
  self.ledger_sql_index+=1;started=time.monotonic();outcome='passed';error_type=None;primary=None
  stage='ownership' if 'shobj_description' in statement else 'table_inventory' if 'FROM pg_tables WHERE' in statement else 'other_public_digests' if 'json_object_agg(name,digest)' in statement else 'primary_rows'
  try:return super().pg(statement,*args,**kwargs)
  except BaseException as error:outcome='failed';error_type=type(error).__name__;primary=error;raise
  finally:
   try:ro.append_json(self.work/'ledger-sql-observations.jsonl',dict(ledger_index=self.ledger_active,sql_index=self.ledger_sql_index,stage=stage,statement_sha256=hashlib.sha256(statement.encode()).hexdigest(),started_monotonic=started,ended_monotonic=time.monotonic(),duration_seconds=time.monotonic()-started,outcome=outcome,error_type=error_type,SQLSTATE=None,statement_timeout_ms=8000,transport_timeout_seconds=kwargs.get('timeout',12),SQL_text_or_parameters_stored=False))
   except BaseException as error:
    if primary is None:raise
    primary.add_note('ledger diagnostic secondary: '+type(error).__name__)
 def read_ledger(self):
  check(self.other_public_status in ('unread','verified'),'failed other-public proof cannot be retried')
  value=super().ledger()
  excluded=('v3_postmortems','v3_incident_cases','v3_incident_case_alerts','v3_incident_case_status_events','v3_incident_timeline','ops_incidents','management_audit','investigations','events','sessions')
  names=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+','.join(q(t) for t in excluded)+')',read=True))
  check(names and len(names)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',n) for n in names),'bounded exact other-public table inventory required')
  value['other_public']=self.read_other_public(names);return value
 def read_other_public(self,names):
  check(self.other_public_status in ('unread','verified'),'failed other-public proof cannot be retried')
  try:
   result=json.loads(self.sql(other_public_query(names),read=True))
   check(isinstance(result,dict) and set(result)==set(names),'complete other-public digest table set differs')
   self.other_public_status='verified';return result
  except BaseException:
   self.other_public_status='failed';raise
 def grants(self,deadline=None):
  query="SELECT COALESCE(json_agg(to_jsonb(g) ORDER BY to_jsonb(g)::text),'[]'::json) FROM v3_role_grants g WHERE role_id="+q(self.roles['reader'])+" AND module='knowledge'"
  return json.loads(self.sql(query,read=True) if deadline is None else self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
 def restore_grants(self,rows,deadline):
  identity=json.loads(self.pg("BEGIN READ ONLY; SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user)); COMMIT;",self.database,self.db_user,deadline=deadline))
  check(identity==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'exact owned ACL recovery DB/role tags differ')
  self.pg('BEGIN; DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='knowledge'; INSERT INTO v3_role_grants SELECT * FROM json_populate_recordset(NULL::v3_role_grants,"+q(json.dumps(rows))+"::json); COMMIT;",self.database,self.db_user,deadline=deadline);check(self.grants(deadline)==rows,'original owned knowledge grants not exactly restored')
 def acl_boundaries(self,path,original):
  self.acl_original=original;primary=None
  try:
   self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='knowledge';")
   self.refuse('no_view','reader','GET',path,None,404,'not_found')
   before=self.ledger();self.call('reader','POST','/api/v1/postmortems',self.body,want=403,code='forbidden',scenario='authorization');same(before,self.ledger())
   self.restore_grants([g for g in original if g['action']=='view'],time.monotonic()+4)
   before=self.ledger();self.call('reader','GET',path,scenario='fixture');same(before,self.ledger())
   self.refuse('no_manage','reader','POST','/api/v1/postmortems',self.body,403,'forbidden')
  except BaseException as error:primary=error;self.cleanup_deadline=time.monotonic()+40
  finally:
   restore_started=time.monotonic()
   try:
    self.restore_grants(original,min(self.cleanup_deadline or float('inf'),restore_started+4));self.acl_original=None
   except BaseException as error:
    if primary is None:primary=error;self.cleanup_deadline=restore_started+40
    else:primary.add_note('owned ACL recovery secondary: '+type(error).__name__)
  if primary is not None:raise primary
 def run(self):
  self.provision();self.start();self.seed()
  for actor in ('admin','reader','hidden'):self.login(actor)
  self.inputs();created,_=self.call('reader','POST','/api/v1/incidents',incident.payload(self.app,self.alert,self.marker),want=201,scenario='fixture');self.case=created['id'];check(re.fullmatch('INC-[a-f0-9]{32}',self.case),'formal case ID differs')
  self.body=dict(incident_id=self.case,source_revision='case:1',status='review',sections=[dict(key='root',title='Root cause',text='Owned synthetic root '+self.nonce)],actions=[dict(text='Owned follow up '+self.nonce,owner=self.users['reader'],due='2099-01-01')])
  path='/api/v1/postmortems/draft?incident='+self.case
  self.refuse('draft_invalid_incident','reader','GET',INVALID_DRAFT_PATH,None,422,'invalid_incident')
  self.refuse('open_source','reader','GET',path,None,404,'not_found')
  before=self.ledger();self.call('reader','POST','/api/v1/postmortems',self.body,want=409,code='postmortem_source_changed',scenario='boundary');same(before,self.ledger())
  closed,_=self.call('reader','POST','/api/v1/incidents/'+self.case+'/status',dict(status='resolved',expected_revision=1,request_id='pm-boundary-'+self.nonce,reason='Owned PM source prerequisite'),want=201,index=1,scenario='fixture')
  self.event=closed['event']['id'];check(type(self.event) is int and self.event>0 and closed['current_status']=='resolved' and closed['current_revision']==2 and closed['current_resolved_status_event_id']==self.event,'formal resolved CAS differs');self.revision='case:'+str(self.event);self.body['source_revision']=self.revision
  baseline=self.ledger();ro._private(self.work/'resolved-baseline-ledger.json',baseline);draft,_=self.call('reader','GET',path,index=1,scenario='fixture');check(draft.get('source_revision')==self.revision and draft.get('incident',{}).get('id')==self.case and draft.get('draft_mode')=='evidence_only' and draft.get('partial') is True,'formal partial manual draft lineage differs');same(baseline,self.ledger());self.marker_case('resolved_source',dict(actual_HTTP_CAS=True,manual_source=True,partial_draft=True))
  self.refuse('hidden_source','hidden','GET',path,None,404,'not_found',environment='staging')
  before=self.ledger();self.call('hidden','POST','/api/v1/postmortems',self.body,want=404,code='not_found',scenario='authorization',index=1,environment='staging');same(before,self.ledger())
  original=self.grants();check(len(original)==2,'exact owned knowledge view/manage fixture required')
  self.acl_boundaries(path,original)
  before=self.ledger();restored,_=self.call('reader','GET',path,scenario='fixture',index=1);check(restored==draft,'restored current draft DTO changed');same(before,self.ledger());self.marker_case('acl_restored',dict(exact_original_grants=True,same_session_draft200=True))
  for index,(name,body,code) in enumerate(invalid_inputs(self.body)):
   # A real first close can receive event1; select a valid, certainly stale revision.
   if name=='stale_revision':body=dict(body,source_revision='case:'+str(self.event+1))
   self.refuse(name,'reader','POST','/api/v1/postmortems',body,409 if name=='stale_revision' else 422,code,index%2)
  before=self.ledger();made,_=self.call('reader','POST','/api/v1/postmortems',self.body,want=201,scenario='fixture',index=1);after=self.ledger();pm.verify_create(before,after,made,self.body,self.users['reader'],self.app,self.event);self.marker_case('create_control',dict(one_review=True,one_created_audit=True,HTTP201=True))
  self.refuse('duplicate','reader','POST','/api/v1/postmortems',self.body,409,'postmortem_exists')
  self.refuse('draft_excludes_existing','reader','GET',path,None,404,'not_found')
  ro._private(self.work/'final-ledger.json',self.ledger());self.frozen();check(self.calls==28,'reviewed exact28 HTTP scope differs')
  result=dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,helper_sha256=hashes(),targets=TARGETS,required_cases=REQUIRED_CASES,http_plan=HTTP_PLAN,http_calls=self.calls,normal_added=0,registration_evidence=False,full_business_chain_verified=False,unverified=['publish','inflight revocation','natural expiry','Runbook/full chain','real external identity','performance/24h'])
  ro._private(self.work/'postmortem-boundaries-result.json',result);return result
 def cleanup(self):
  self.cleanup_deadline=self.cleanup_deadline or time.monotonic()+40;primary=None
  try:
   if self.acl_original is not None:self.restore_grants(self.acl_original,min(self.cleanup_deadline,time.monotonic()+4));self.acl_original=None
  except BaseException as error:primary=error
  finally:
   try:bounded_cleanup(self)
   except BaseException as error:
    if primary is None:primary=error
    else:primary.add_note('owned UUID cleanup secondary: '+type(error).__name__)
  if primary is not None:raise primary
  for port in PORTS:
   check(time.monotonic()<self.cleanup_deadline,'owned cleanup total40 exceeded')
   with socket.socket() as s:s.settimeout(.3);check(s.connect_ex(('127.0.0.1',port))!=0,'owned PM boundary port remains')
  emit('postmortem_boundaries.cleanup','passed',dict(single_seconds=40,grace_seconds=45,owned_UUID_only=True))

def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_plan=HTTP_PLAN,estimated_HTTP_calls=28,normal_added=0,registration_evidence=False,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
 if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
  with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('postmortem_boundaries.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('postmortem_boundaries.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
