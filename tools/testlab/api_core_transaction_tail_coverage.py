"""Prepared-only three existing core transaction tails; no upstream or whole chain."""
from __future__ import annotations
import argparse,copy,fcntl,hashlib,importlib.util,inspect,json,os,re,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-core-transaction-tail';PORTS=(18886,18887)
spec=importlib.util.spec_from_file_location('core_tail_private_role',ROOT/'tools/testlab/api_role_audit_rollback_coverage.py')
ro=importlib.util.module_from_spec(spec);spec.loader.exec_module(ro);base=ro.base;check,Blocked,q=ro.check,ro.Blocked,ro.q
SOURCE,BINARY,RUNTIME,PG_CID=ro.SOURCE,ro.BINARY,ro.RUNTIME,ro.PG_CID
ADDENDUM=ROOT/'.local/testlab/cp306-native-core-scene-plan/de4b47e4-2df7-40e7-b615-c2d288ddbb4c/transaction-tail-addendum-b03972cc-c6b5-45c2-a370-a617724b8230.json'
ADDENDUM_SHA='cb10390ae4a13bdd5081c05970cb067619e64bc51a8fc3689d2d97ac50b83ffd'
TARGETS=(('POST','/services'),('PUT','/service-mappings'),('DELETE','/me/sessions/{id}'))
SUPPORT=(('POST','/auth/login'),('GET','/service-mappings'),('GET','/me/notification-settings'))
TABLES=('applications','application_environments','v3_application_environments','v3_service_environment_profiles','sessions','management_audit')
REQUIRED_CASES=tuple('core_tail.'+k+'.'+s for k in ('service','mapping','session') for s in ('rollback','fault_removed','recovery'))+('core_tail.cleanup','management.cleanup','management.complete')
HTTP_PLAN=('login.admin','login.reader.old','login.reader.current','login.peer','service.failed','service.recovery','mapping.before','mapping.failed','mapping.recovery','mapping.after','session.failed','session.old_alive','session.recovery','session.old_invalid','session.current_alive')
base.PORTS=ro.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);base.TARGETS=TARGETS;base.SUPPORT=SUPPORT

def hashes():
 return {n:ro.digest(ROOT/'tools/testlab'/n) for n in ('api_core_transaction_tail_coverage.py','tests/test_api_core_transaction_tail_coverage.py','api_role_audit_rollback_coverage.py','api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):
 number=int(row['name'].rsplit('.',1)[-1]) if row.get('name','').startswith('management.http.') else None
 if number is not None:check(1<=number<=len(HTTP_PLAN),'HTTP plan exceeded')
 return dict(row,attempt_name=HTTP_PLAN[number-1] if number else None,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha256=RUNTIME,evidence_mode='real_local',upstream_mode='real_local',registration_evidence=False,test_name='FormalCoreTransactionTail',proof_scope='three existing HTTP transaction tails only; synthetic inputs; configured binding only; normal_added=0')
def emit(name,status,actual,**fields):
 fields.pop('module',None);fields.pop('evidence_mode',None)
 if 'interface' in fields:fields['interface']=evidence(fields['interface'])
 print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
ro.hashes=hashes;ro.emit=base.emit=emit;ro.evidence=evidence

def admission(path,run_id):
 path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonical private permit required')
 st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 permit required')
 permit=base.private_json(path)
 expected=dict(status='root-admitted-core-transaction-tail',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes(),transaction_tail_addendum_sha256=ADDENDUM_SHA)
 check(all(permit.get(k)==v for k,v in expected.items()),'core tail root identity differs');check(ro.digest(ADDENDUM)==ADDENDUM_SHA,'reviewed transaction addendum changed')
 for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','core_transaction_tail_faults_authorized'):check(permit.get(key) is True,'missing root prerequisite '+key)
 conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').as_uri()+'?mode=ro',uri=True,timeout=5)
 try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
 finally:conn.close()
 check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE and active[0].get('pid')==os.getppid() and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256'],'sole matching actual Runner required');return permit
ro.admission=admission
class Contract(ro.Contract):pass
# Reuse the reviewed launch loop, with one private config path and no upstream process.
_src=textwrap.dedent(inspect.getsource(base.Chain.start));_needle='log = os.open(self.work /'
check(_src.count(_needle)==1,'private startup config seam drift');_ns=dict(base.__dict__)
exec(compile(_src.replace(_needle,"variables['AIOPS_TELEMETRY_CONFIG'] = str(self.work / 'telemetry.json')\n        "+_needle),str(__file__)+'[private start]','exec'),_ns);start=_ns['start']

def fault_statements(nonce,actor,kind,target='',app=''):
 check(re.fullmatch('[a-f0-9]{32}',nonce) and re.fullmatch('[a-f0-9]{32}',actor) and kind in ('service','mapping','session'),'exact own actor/nonce/kind required')
 action={'service':'v3.service.created','mapping':'v3.service_mapping.saved','session':'v3.session.revoked'}[kind];name='core_tail_'+kind+'_'+nonce
 match='NEW.actor_id='+q(actor)+' AND NEW.action='+q(action)+" AND NEW.outcome='success'"
 if kind=='service':
  check(target=='management-'+nonce[:20],'exact own fresh key required')
  match+=" AND NEW.target=NEW.application_id AND NEW.changes->>'environment'='test' AND EXISTS(SELECT 1 FROM public.applications a WHERE a.id=NEW.application_id AND a.key="+q(target)+')'
 else:
  check(bool(re.fullmatch('[a-f0-9]{64}' if kind=='session' else '[a-f0-9]{32}',target)) and (app=='' if kind=='session' else bool(re.fullmatch('[a-f0-9]{32}',app))),'exact produced target/app required')
  match+=' AND NEW.target='+q(target)+' AND NEW.application_id='+q(app)
 create='BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF '+match+" THEN RAISE EXCEPTION 'owned core transaction tail fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;'
 return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def same(before,after):check(before==after,'complete public business/session/audit ledger changed')
def verify_recovery(before,after,kind,actor,key,target='',app='',binding=None):
 expected=copy.deepcopy(before);check(after['management_audit'][:-1]==before['management_audit'] and len(after['management_audit'])==len(before['management_audit'])+1,'complete audit prefix/exact1 differs')
 changes={};action={'service':'v3.service.created','mapping':'v3.service_mapping.saved','session':'v3.session.revoked'}[kind]
 if kind=='service':
  for table in TABLES[:4]:
   additions=[r for r in after[table] if r not in before[table]];check(len(additions)==1 and len(after[table])==len(before[table])+1,'one exact service object per table required');expected[table]=before[table]+additions
  a,e,reg,p=(expected[t][-1] for t in TABLES[:4]);app=target=a['id'];check(re.fullmatch('[a-f0-9]{32}',app) and a['name']==a['key']==key and a['environment']=='test','new own application differs')
  check(e['application_id']==reg['application_id']==p['application_id']==app and e['key']==reg['environment_id']==p['environment_id']=='test','new environment/registration/profile lineage differs')
  check(e['bindings']==[] and p['tier']=='T1' and p['owner_label']=='Synthetic owner' and p['namespace']=='','manual profile differs')
  changes=dict(environment='test',namespace='',resource_id='',resource_linked=False,tier='T1',existing_service=False)
 elif kind=='mapping':
  rows=[r for r in before['application_environments'] if r['id']==target];check(len(rows)==1 and rows[0]['application_id']==app and rows[0]['bindings']==[],'exact empty mapping target required');old=rows[0];new=dict(old,bindings=[binding],binding_revision=old['binding_revision']+1)
  expected['application_environments']=[new if r['id']==target else r for r in before['application_environments']]
  changes=dict(environment='test',binding_revision_before=old['binding_revision'],binding_revision_after=new['binding_revision'],bindings_before=[],bindings_after=[binding])
 else:
  rows=[r for r in before['sessions'] if r['token_hash']==target];check(len(rows)==1 and rows[0]['user_id']==actor,'exact older own session required');expected['sessions']=[r for r in before['sessions'] if r['token_hash']!=target]
 audit=after['management_audit'][-1];check(all(audit.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id=app,action=action,target=target,outcome='success',changes=changes).items()),'exact audit actor/action/target/outcome/changes differs')
 expected['management_audit']=after['management_audit'];same(expected,after);return audit

class Chain(ro.Chain):
 start=start
 def __init__(self,root,data,run_id,permit):
  super().__init__(root,data,run_id,permit);self.contract=Contract(root);self.key='management-'+self.nonce[:20];self.fault=None
  self.state.update(suite=MODULE,targets=TARGETS,normal_added=0,registration_evidence=False);base._private(self.work/'state.json',self.state)
 def provision(self):
  super().provision();base._private(self.work/'telemetry.json',dict(prometheus=dict(url='http://127.0.0.1:9',allow_http=True),applications={self.key:dict(prometheus_queries={'owned_up':'up'})},timeout_seconds=3));self.telemetry_sha=ro.digest(self.work/'telemetry.json')
 def frozen(self):
  super().frozen()
  if hasattr(self,'telemetry_sha'):check(ro.digest(self.work/'telemetry.json')==self.telemetry_sha,'private registration config changed')
 def marker_case(self,suffix,actual):
  name='core_tail.'+suffix;check(name in REQUIRED_CASES,'unknown core marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
 def call(self,*a,**kw):
  self.frozen();check(self.calls<len(HTTP_PLAN),'exact15 HTTP plan exceeded');kw.setdefault('scenario','failure');return base.Chain.call(self,*a,**kw)
 def ledger(self):
  self.frozen();self.owner();names=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'",read=True))
  check(names and len(names)<=512 and set(TABLES)<=set(names) and all(re.fullmatch('[a-z][a-z0-9_]*',n) for n in names),'complete bounded public inventory required')
  hashes_sql=['SELECT '+q(n)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+n+' t' for n in names if n not in TABLES]
  fields=[q(t)+",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY "+('id' if t=='management_audit' else 'to_jsonb(t)::text')+"),'[]'::json) FROM public."+t+' t)' for t in TABLES]
  fields.append("'other_public',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(hashes_sql)+') hashes)');return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')',read=True))
 def install_fault(self,kind,actor,target,app=''):
  self.frozen();self.owner();self.fault=fault_statements(self.nonce,actor,kind,target,app);self.fault_attempted=True
  base._private(self.work/('core-'+kind+'-fault-intent.json'),dict(database=self.database,actor=actor,target=target,app=app,kind=kind));self.sql(self.fault[1])
 def remove_fault(self,deadline=None):
  if not self.fault_attempted:return
  deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4;self.owner(deadline=deadline);name,_,drop=self.fault;self.pg(drop,self.database,self.db_user,deadline=deadline)
  value=json.loads(self.pg('BEGIN READ ONLY; SELECT json_build_object(\'trigger\',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid=\'public.management_audit\'::regclass AND tgname='+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+') IS NOT NULL); COMMIT;',self.database,self.db_user,deadline=deadline));check(value==dict(trigger=False,function=False),'own core fault remains');self.fault_attempted=False
 def exercise(self,kind,user,method,path,body,code,want,actor,target,app='',binding=None,failed_control=None):
  before=self.ledger();self.save_ledger(kind+'-before',before);primary=None
  try:
   self.install_fault(kind,actor,target,app);self.call(user,method,path,body,want=503,code=code,index=0);failed=self.ledger();same(before,failed);self.save_ledger(kind+'-failed',failed)
   if failed_control:failed_control();same(before,self.ledger())
   self.marker_case(kind+'.rollback',dict(status=503,error_code=code,complete_public_ledger_unchanged=True))
  except BaseException as error:primary=error
  finally:
   try:self.remove_fault()
   except BaseException as error:
    if primary is None:primary=error
    else:self.secondary(self.work/'core-restore-secondary.json',error,primary)
  if primary is not None:raise primary
  self.marker_case(kind+'.fault_removed',dict(trigger=False,function=False,restore_seconds=4));value,_=self.call(user,method,path,body,want=want,index=1)
  after=self.ledger();audit=verify_recovery(before,after,kind,actor,self.key,target,app,binding);self.save_ledger(kind+'-recovered',after);self.marker_case(kind+'.recovery',dict(status=want,opposite_API=True,same_request=True,audit_id=audit['id'],audit_delta=1,no_auto_retry=True));return value,after
 def mapping(self,value):
  rows=[r for r in value['rows'] if r['service']==self.key];check(len(rows)==1 and rows[0]['save']['editable'] is True,'exact editable own mapping required');return rows[0]['save']
 def run(self):
  self.provision();self.start();self.seed();self.login('admin');self.login('reader');self.cookies['reader_old']=self.cookies['reader'];self.login('reader',1);self.login('peer',1)
  actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True);check(re.fullmatch('[a-f0-9]{32}',actor),'HTTP admin actor missing')
  value,ledger=self.exercise('service','admin','POST','/api/v1/services',{'items':[dict(id=self.key,tier='T1',owner='Synthetic owner')]},'catalog_unavailable',201,actor,self.key);check(value=={'created':1},'one service recovery acknowledgement required')
  app=ledger['applications'][0]['id'];env=ledger['application_environments'][0]['id'];rows,_=self.call('admin','GET','/api/v1/service-mappings');initial=self.mapping(rows)
  check(initial['service_id']==app and initial['service_environment_id']==env and initial['binding_revision']==ledger['application_environments'][0]['binding_revision'] and initial['bindings']==[],'HTTP mapping identity/revision/empty baseline differs')
  binding=dict(kind='prometheus',connector_id='prometheus',resource=self.key);wire=dict(binding,system_id='metrics');item=dict(service_id=app,service_environment_id=env,binding_revision=initial['binding_revision'],bindings=[wire]);body=dict(environment_id='test',items=[item])
  value,ledger=self.exercise('mapping','admin','PUT','/api/v1/service-mappings',body,'audit_unavailable',200,actor,env,app,binding);expected=dict(environment_id='test',items=[dict(item,binding_revision=initial['binding_revision']+1)]);check(value==expected,'exact mapping recovery DTO differs')
  rows,_=self.call('admin','GET','/api/v1/service-mappings',index=0);saved=self.mapping(rows);check(saved['service_id']==app and saved['service_environment_id']==env and saved['binding_revision']==initial['binding_revision']+1 and saved['bindings']==[wire],'oppositeAPI mapping readback differs');same(ledger,self.ledger())
  old=hashlib.sha256(self.cookies['reader_old'].split('=',1)[1].encode()).hexdigest();current=hashlib.sha256(self.cookies['reader'].split('=',1)[1].encode()).hexdigest();check(old!=current,'old and current sessions must differ')
  notify=lambda user,want=200:self.call(user,'GET','/api/v1/me/notification-settings',index=1,want=want,code='unauthorized' if want==401 else None)
  _,ledger=self.exercise('session','reader','DELETE','/api/v1/me/sessions/'+old,None,'settings_unavailable',204,self.users['reader'],old,failed_control=lambda:notify('reader_old'))
  notify('reader_old',401);notify('reader');same(ledger,self.ledger());check(self.calls==len(HTTP_PLAN),'exact15 HTTP budget differs');self.frozen()
  result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,helper_sha256=hashes(),http_calls=self.calls,HTTP_PLAN=HTTP_PLAN,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,full_business_chain_verified=False,external_health_verified=False);base._private(self.work/'core-tail-result.json',result);return result
 def cleanup(self):
  ro.Chain.cleanup(self);emit('core_tail.cleanup','passed',dict(single_seconds=40,grace_seconds=45,restore_seconds=4,owned_UUID_only=True))

def main(argv=None):
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
 if a.inventory:Contract(ROOT);check(ro.digest(ADDENDUM)==ADDENDUM_SHA,'addendum drift');print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,HTTP_PLAN=HTTP_PLAN,exact_HTTP_calls=len(HTTP_PLAN),normal_added=0,registration_evidence=False,API_seconds=12,outer_seconds=420,cleanup_seconds=40,grace_seconds=45,restore_seconds=4)));return 0
 if not(a.live and a.run_id and a.admission):p.error('explicit root --live/run-id/admission required')
 fd=None
 try:
  check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600);st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
  with base.cooperative_signals():base.execute(Chain(ROOT,ro.doctor(ROOT),a.run_id,a.admission))
  return 0
 except Blocked:emit('core_tail.environment','blocked',dict(first_failure_preserved=True));return 2
 except Exception as e:emit('core_tail.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
 finally:
  if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
