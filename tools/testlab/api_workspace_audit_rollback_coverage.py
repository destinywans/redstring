"""Formal workspace audit rollback and same-ETag explicit cross-instance recovery.

Only --live with root admission acquires resources. SQL writes are synthetic
identity/ACL and UUID DDL faults; workspace business writes use formal HTTP.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-workspace-audit-rollback';PORTS=(18832,18833)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
spec=importlib.util.spec_from_file_location('workspace_audit_private_management',ROOT/'tools/testlab/api_management_chain.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);check,Blocked,q=base.check,base.Blocked,base.quote
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
base.TARGETS=(('GET','/workspace'),('PUT','/workspace'));base.SUPPORT=(('POST','/auth/login'),)
REQUIRED_CASES=tuple('workspace_audit.'+x for x in ('http_baseline','rollback','failed_readback','fault_removed','recovery','exact_audit','read_consistency','cleanup'))+('management.cleanup','management.complete')
PREFIELDS=('name','slug','timezone','locale','week_start','retention_days')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_workspace_audit_rollback_coverage.py','tests/test_api_workspace_audit_rollback_coverage.py',
        'api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,
    binary_platform='darwin/arm64 Mach-O',test_name='FormalWorkspaceAuditRollback',registration_evidence=False,
    proof_scope='workspace audit fault and same-ETag recovery only; synthetic identity/config; no new normal count or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=base.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=emit;base.append_json=append_json
_inspect=base.inspect_pg
def inspect_pg(*args,**kwargs):
    value=_inspect(*args,**kwargs);check(value.get('Id')==PG_CID,'exact ownedPG differs');return value
base.inspect_pg=inspect_pg
def frozen_identity(data):
    check(data.get('source_tree_sha')==SOURCE and data.get('binary_sha256')==BINARY and data.get('runtime_sha')==RUNTIME
        and data.get('helper_sha256')==hashes(),'source/binary/runtime/transitive helpers differ')
def doctor(root):
    check(Path(root).resolve()==ROOT,'reviewed checkout required');data=base.doctor(root)
    check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());frozen_identity(data);return data
def admission(path,run_id):
    path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonicalUUID/private admission required')
    st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 admission required')
    permit=base.private_json(path)
    expected=dict(status='root-admitted-workspace-audit-rollback',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'workspace root admission identity differs')
    for k in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','workspace_audit_fault_authorized'):check(permit.get(k) is True,'missing root prerequisite '+k)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative workspace path required')
        return super().canonical(method,path)
def fault_statements(nonce,actor):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)) and bool(re.fullmatch('[a-f0-9]{32}',actor)),'exactUUID actor required')
    name='workspace_audit_fail_'+nonce
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+
        " AND NEW.application_id='' AND NEW.action='v3.workspace.updated' AND NEW.target='default' AND NEW.outcome='success' THEN RAISE EXCEPTION 'owned workspace audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+
        name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def verify_rollback(before,after):check(before==after,'audit failure changed complete workspace/business/audit ledger')
def verify_update(before,after,actor,expected):
    old,new=before['workspace'],after['workspace'];check(old and new,'exact default workspace required')
    columns=dict(name='name',slug='v3_slug',timezone='timezone',locale='language',week_start='v3_week_start',retention_days='v3_retention_days')
    allowed=set(columns.values())|{'revision','updated_at'}
    check({k:v for k,v in old.items() if k not in allowed}=={k:v for k,v in new.items() if k not in allowed},'immutable/other workspace fields changed')
    check(all(new[col]==expected[key] for key,col in columns.items()) and new['revision']==old['revision']+1,'saved editable fields/revision differ')
    stamp=lambda x:datetime.datetime.fromisoformat(x.replace('Z','+00:00'))
    check(stamp(new['updated_at'])>stamp(old['updated_at']),'workspace successful updated_at did not advance')
    check(before['business']==after['business'],'unrelated complete public tables changed')
    check(after['audits'][:-1]==before['audits'] and len(after['audits'])==len(before['audits'])+1,'complete audit prefix/exact one differs')
    audit=after['audits'][-1];changes=dict(revision_before=old['revision'],revision_after=new['revision'],**{k:expected[k] for k in PREFIELDS})
    check(all(audit.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id='',action='v3.workspace.updated',target='default',outcome='success',changes=changes).items()),'exact workspace audit actor/changes differ')
    return audit

class Chain(base.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.fault_attempted=False;self.actor=None;self.raw_number=0
        self.state.update(suite=MODULE,targets=base.TARGETS,normal_count_increase=0,counts_as_whole_acceptance=False,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME)
        base._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen();frozen_identity(self.data)
        check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    def marker_case(self,suffix,actual):
        name='workspace_audit.'+suffix;check(name in REQUIRED_CASES,'unknown workspace marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def call(self,*args,**kwargs):
        self.frozen();original=base.transport
        def wire(origin,method,path,payload=None,headers=None,**options):
            result=original(origin,method,path,payload,headers,**options)
            if path=='/api/v1/workspace':
                self.raw_number+=1;status,raw,response_headers=result
                base._private(self.work/('workspace-wire-'+str(self.raw_number)+'.json'),dict(method=method,path=path,API_index=base.BASES.index(origin),
                    request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),status=status,
                    request_body=None if payload is None else payload.decode('utf-8'),response_body=raw.decode('utf-8','replace'),
                    If_Match=base.header(headers or {},'If-Match'),ETag=base.header(response_headers,'ETag'),Cache_Control=base.header(response_headers,'Cache-Control'),
                    binary_sha256=BINARY,source_tree_sha=SOURCE,synthetic_owned_only=True))
            return result
        base.transport=wire
        try:return super().call(*args,**kwargs)
        finally:base.transport=original
    def owner(self,deadline=None):
        query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        v=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(v==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'ownUUID DB/role tags differ')
    def ledger(self):
        self.frozen();self.owner()
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('management_audit','workspace_settings')",read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        fields=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        fields.append("SELECT 'workspace_settings_other' AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public.workspace_settings t WHERE id<>'default'")
        return json.loads(self.sql("SELECT json_build_object('business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(fields)+") hashes),'workspace',(SELECT to_jsonb(t) FROM workspace_settings t WHERE id='default'),'audits',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))",read=True))
    def save_ledger(self,name,value):
        base._private(self.work/(name+'-ledger.json'),dict(value,source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
    def install_fault(self):
        self.frozen();self.owner();statement=fault_statements(self.nonce,self.actor)[1];self.fault_attempted=True
        base._private(self.work/'workspace-fault-intent.json',dict(database=self.database,actor=self.actor,action='v3.workspace.updated',target='default'))
        self.sql(statement)
    def remove_fault(self,deadline=None):
        if not self.fault_attempted:return
        deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4
        self.owner(deadline=deadline);name,_,drop=fault_statements(self.nonce,self.actor);self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        absent=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(absent==dict(trigger=False,function=False),'own workspace fault remains');self.fault_attempted=False
    def secondary(self,path,error,primary):
        try:self.diagnostic(path,dict(error_type=type(error).__name__,first_failure_preserved=True))
        except BaseException as error:primary.add_note('secondary diagnostic unavailable: '+type(error).__name__)
    def read(self,expected,etag,index):
        value,headers=self.call('reader','GET','/api/v1/workspace',index=index,scenario='failure',validate=lambda v:check(v==expected,'crossAPI full workspace DTO differs'))
        check(base.header(headers,'ETag')==etag and base.header(headers,'Cache-Control')=='private, no-store','workspace ETag/cache contract differs')
        return value
    def baseline(self):
        self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True)
        check(bool(re.fullmatch('[a-f0-9]{32}',self.actor)),'actual bootstrap adminID missing')
        initial,headers=self.call('reader','GET','/api/v1/workspace',scenario='failure');etag=base.header(headers,'ETag');base.revision(headers,'ETag',quoted=True)
        initial_ledger=self.ledger();self.expected=copy.deepcopy(initial);self.expected['name']='Workspace baseline '+self.nonce
        _,headers=self.call('admin','PUT','/api/v1/workspace',self.expected,headers={'If-Match':etag},scenario='failure',validate=lambda v:check(v==self.expected,'formal baseline DTO differs'))
        baseline=self.ledger();verify_update(initial_ledger,baseline,self.actor,self.expected)
        self.etag=base.header(headers,'ETag');check(base.revision(headers,'ETag',quoted=True)==baseline['workspace']['revision'],'baseline ETag/row mismatch')
        self.read(self.expected,self.etag,1);verify_rollback(baseline,self.ledger());self.save_ledger('baseline',baseline)
        self.payload=copy.deepcopy(self.expected);self.payload['name']='Workspace recovery '+self.nonce
        self.marker_case('http_baseline',dict(actual_admin_global_manage=True,formal_baseline_update=True,revision=baseline['workspace']['revision'],business_SQL_writes=0))
        return baseline
    def exercise(self,before):
        primary=None
        try:
            self.install_fault();self.call('admin','PUT','/api/v1/workspace',self.payload,headers={'If-Match':self.etag},want=503,code='settings_unavailable',scenario='failure',index=0)
            failed=self.ledger();self.save_ledger('failed',failed);verify_rollback(before,failed)
            self.marker_case('rollback',dict(status=503,error_code='settings_unavailable',complete_row_revision_updated_at_audit_unchanged=True))
            for index in (0,1):self.read(self.expected,self.etag,index)
            verify_rollback(before,self.ledger());self.marker_case('failed_readback',dict(two_APIs=True,DTO_and_ETag_unchanged=True,read_side_effects=0))
        except BaseException as error:primary=error
        finally:
            try:self.remove_fault()
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'restore-secondary-error.json',error,primary)
        if primary is not None:raise primary
        self.marker_case('fault_removed',dict(trigger=False,function=False))
        _,headers=self.call('admin','PUT','/api/v1/workspace',self.payload,headers={'If-Match':self.etag},index=1,scenario='failure',validate=lambda v:check(v==self.payload,'samebody recovery DTO differs'))
        recovered=self.ledger();self.save_ledger('recovered',recovered);audit=verify_update(before,recovered,self.actor,self.payload)
        newtag=base.header(headers,'ETag');check(base.revision(headers,'ETag',quoted=True)==before['workspace']['revision']+1,'recovery ETag did not advance exactlyonce')
        self.marker_case('recovery',dict(status=200,API_index=1,same_body_and_prior_ETag=True,explicit_recovery=True,revision_delta=1,no_auto_retry=True))
        self.marker_case('exact_audit',dict(audit_id=audit['id'],audit_delta=1,exact_revision_changes=True,complete_prefix_preserved=True))
        for index in (0,1):self.read(self.payload,newtag,index)
        verify_rollback(recovered,self.ledger());self.marker_case('read_consistency',dict(two_APIs=True,fullDTO_ETag_matches=True,read_side_effects=0,other_public_tables_unchanged=True))
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader'):self.login(actor)
        self.exercise(self.baseline());self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],
            required_cases=REQUIRED_CASES,http_calls=self.calls,normal_count_increase=0,counts_as_whole_acceptance=False,
            unverified=['real organizational admin/deployment residency','inflight revocation','legacy writer interleaving','crash/corrupt row','performance/full chain'])
        base._private(self.work/'workspace-audit-result.json',result);return result
    def cleanup(self):
        deadline=time.monotonic()+40;primary=None
        try:self.remove_fault(deadline=min(deadline,time.monotonic()+4))
        except BaseException as error:primary=error
        finally:
            self.cleanup_deadline=deadline
            try:bounded_cleanup(self)
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'cleanup-secondary-error.json',error,primary)
        if primary is not None:raise primary
        for port in PORTS:
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'workspace owned API port occupied')
        emit('workspace_audit.cleanup','passed',dict(single_cleanup_seconds=40,restore_within4=True,owned_UUID_only=True,ports_free=list(PORTS)))
_source=textwrap.dedent(inspect.getsource(base.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_ns=dict(base.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared fault/PG deadline]','exec'),_ns);bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=11,normal_count_increase=0)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private management lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('workspace_audit.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('workspace_audit.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
