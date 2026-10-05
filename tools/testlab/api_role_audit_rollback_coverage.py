"""Formal role audit rollback and same-revision explicit cross-instance recovery.

Only --live with root admission acquires resources. Target business role and
users/ACL are synthetic identities: no public role-creation producer is claimed.
Matrix changes use formal HTTP; exact audit DDL affects only own UUID database.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-role-audit-rollback';PORTS=(18852,18853)
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
spec=importlib.util.spec_from_file_location('role_audit_private_management',ROOT/'tools/testlab/api_management_chain.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);check,Blocked,q=base.check,base.Blocked,base.quote
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
base.TARGETS=(('GET','/roles/{id}'),('PUT','/roles/{id}'));base.SUPPORT=(('POST','/auth/login'),)
REQUIRED_CASES=tuple('role_audit.'+x for x in ('http_baseline','rollback','failed_readback','fault_removed','recovery','exact_audit','read_consistency','permission_effect','cleanup'))+('management.cleanup','management.complete')
CODES=('conversation','events','alert_rules','silences','proposals','runbooks','knowledge','services','connectors','agents','identity','audit','resources')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_role_audit_rollback_coverage.py','tests/test_api_role_audit_rollback_coverage.py',
        'api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,
    binary_platform='darwin/arm64 Mach-O',test_name='FormalRoleAuditRollback',registration_evidence=False,
    proof_scope='role audit fault and same-revision recovery only; synthetic identity/target business role; no new normal count or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=base.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=emit;base.append_json=append_json
_inspect=base.inspect_pg
def inspect_pg(*args,**kwargs):
    value=_inspect(*args,**kwargs);check(value.get('Id')==PG_CID,'exact ownedPG differs')
    limits=value.get('HostConfig',{});check(limits.get('NanoCpus')==4_000_000_000 and limits.get('Memory')==2_147_483_648,'ownedPG exact4CPU/2GiB limits differ');return value
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
    expected=dict(status='root-admitted-role-audit-rollback',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'role root admission identity differs')
    for k in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','role_audit_fault_authorized'):check(permit.get(k) is True,'missing root prerequisite '+k)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative role path required')
        return super().canonical(method,path)
def fault_statements(nonce,actor):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)) and bool(re.fullmatch('[a-f0-9]{32}',actor)),'exactUUID actor required')
    target='management-'+nonce+'-target';name='role_audit_fail_'+nonce
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+
        " AND NEW.application_id='' AND NEW.action='v3.role.matrix.updated' AND NEW.target="+q(target)+" AND NEW.outcome='success' THEN RAISE EXCEPTION 'owned role audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+
        name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def verify_rollback(before,after):check(before==after,'audit failure changed complete roles/grants/scope/revision/other-public/audit ledger')
def codes(matrix):
    check(len(matrix)==len(CODES) and all(len(row['grants'])==5 for row in matrix),'complete source matrix required')
    return {code:list(row['grants']) for code,row in zip(CODES,matrix)}
def verify_update(before,after,actor,target,old_matrix,matrix):
    old,new=before['role'],after['role'];check(old and new and old['id']==new['id']==target,'exact target role required')
    check(old['v3_kind']==new['v3_kind']=='business','business target only')
    check({k:v for k,v in old.items() if k!='v3_matrix_revision'}=={k:v for k,v in new.items() if k!='v3_matrix_revision'},'immutable/other target role fields changed')
    check(type(new['v3_matrix_revision']) is int and new['v3_matrix_revision']==old['v3_matrix_revision']+1,'revision did not advance exactly once')
    check(after['roles']==[new if row['id']==target else row for row in before['roles']],'another role changed or target row missing')
    check(before['business']==after['business'],'unrelated complete public tables changed')
    old_grants=before['grants'];new_grants=after['grants']
    check(all(row in new_grants for row in old_grants),'old grant row/assurance/scope/timestamp changed')
    additions=[row for row in new_grants if row not in old_grants]
    expected={(target,module,'view','global','*','standard') for module in ('members','roles')}
    check(len(additions)==2 and len(new_grants)==len(old_grants)+2,'exactly two grouped identity grants required')
    check({(r['role_id'],r['module'],r['action'],r['scope_kind'],r['scope_id'],r['assurance']) for r in additions}==expected,'new permission/assurance/scope differs')
    for row in additions:
        check(set(row)=={'role_id','module','action','scope_kind','scope_id','assurance','created_at'},'unknown grant columns require review')
        stamp=datetime.datetime.fromisoformat(row['created_at'].replace('Z','+00:00'));check(stamp.tzinfo is not None,'grant timestamp invalid')
    old_codes,new_codes=codes(old_matrix),codes(matrix)
    wanted=copy.deepcopy(old_codes);check(wanted['identity'][0]==0,'target already has identity view');wanted['identity'][0]=1
    check(new_codes==wanted,'only grouped identity view may change')
    check(after['audits'][:-1]==before['audits'] and len(after['audits'])==len(before['audits'])+1,'complete audit prefix/exact one differs')
    audit=after['audits'][-1]
    check(all(audit.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id='',action='v3.role.matrix.updated',target=target,outcome='success',changes={'before':old_codes,'after':new_codes}).items()),'exact role audit actor/stable codes differ')
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
        name='role_audit.'+suffix;check(name in REQUIRED_CASES,'unknown role marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def call(self,*args,**kwargs):
        self.frozen();original=base.transport
        def wire(origin,method,path,payload=None,headers=None,**options):
            result=original(origin,method,path,payload,headers,**options)
            if path=='/api/v1/roles/'+self.roles['target']:
                self.raw_number+=1;status,raw,response_headers=result
                base._private(self.work/('role-wire-'+str(self.raw_number)+'.json'),dict(method=method,path=path,API_index=base.BASES.index(origin),
                    request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),status=status,
                    request_body=None if payload is None else payload.decode('utf-8'),response_body=raw.decode('utf-8','replace'),
                    If_Role_Matrix_Revision=base.header(headers or {},'If-Role-Matrix-Revision'),X_Role_Matrix_Revision=base.header(response_headers,'X-Role-Matrix-Revision'),Cache_Control=base.header(response_headers,'Cache-Control'),
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
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('management_audit','rbac_roles','v3_role_grants')",read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        fields=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        return json.loads(self.sql("SELECT json_build_object('business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(fields)+") hashes),'role',(SELECT to_jsonb(t) FROM rbac_roles t WHERE id="+q(self.roles['target'])+"),'roles',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM rbac_roles t),'grants',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY role_id,module,action,scope_kind,scope_id),'[]'::json) FROM v3_role_grants t),'audits',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))",read=True))
    def save_ledger(self,name,value):
        base._private(self.work/(name+'-ledger.json'),dict(value,source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
    def install_fault(self):
        self.frozen();self.owner();statement=fault_statements(self.nonce,self.actor)[1];self.fault_attempted=True
        base._private(self.work/'role-fault-intent.json',dict(database=self.database,actor=self.actor,action='v3.role.matrix.updated',target=self.roles['target']))
        self.sql(statement)
    def remove_fault(self,deadline=None):
        if not self.fault_attempted:return
        deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4
        self.owner(deadline=deadline);name,_,drop=fault_statements(self.nonce,self.actor);self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        absent=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(absent==dict(trigger=False,function=False),'own role fault remains');self.fault_attempted=False
    def secondary(self,path,error,primary):
        try:self.diagnostic(path,dict(error_type=type(error).__name__,first_failure_preserved=True))
        except BaseException as error:primary.add_note('secondary diagnostic unavailable: '+type(error).__name__)
    def read(self,expected,revision,index,user='reader'):
        value,headers=self.call(user,'GET',self.path,index=index,scenario='failure',validate=lambda v:check(v==expected,'crossAPI full role DTO differs'))
        check(base.header(headers,'X-Role-Matrix-Revision')==revision and base.header(headers,'Cache-Control')=='private, no-store','role revision/cache contract differs')
        return value
    def baseline(self):
        self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True)
        check(bool(re.fullmatch('[a-f0-9]{32}',self.actor)),'actual bootstrap adminID missing')
        target=self.roles['target'];check(target=='management-'+self.nonce+'-target','own synthetic target differs');self.path='/api/v1/roles/'+target
        assigned=json.loads(self.sql("SELECT COALESCE(json_agg(user_id ORDER BY user_id),'[]'::json) FROM rbac_user_roles WHERE role_id="+q(target),read=True))
        check(assigned==[self.users['target']] and self.actor!=self.users['target'],'target must not grant requester access')
        self.expected,headers=self.call('admin','GET',self.path,scenario='failure');self.revision=base.header(headers,'X-Role-Matrix-Revision')
        old=base.revision(headers,'X-Role-Matrix-Revision')
        check(self.expected['id']==target and self.expected['kind']=='business' and len(self.expected['matrix'])==13,'target DTO/catalog differs')
        check(self.expected['matrix'][10]['resource']=='Members & roles' and self.expected['matrix'][10]['grants'][0]==0,'identity view must initially be absent')
        self.read(self.expected,self.revision,1)
        self.call('target','GET',self.path,want=403,code='forbidden',scenario='authorization')
        before=self.ledger();check(before['role']['v3_matrix_revision']==old,'header/persisted revision differs')
        self.payload={'matrix':copy.deepcopy(self.expected['matrix'])};self.payload['matrix'][10]['grants'][0]=1
        self.save_ledger('baseline',before);self.marker_case('http_baseline',dict(revision=old,target=target,target_never_grants_admin=True,synthetic_identity_fixture=True,public_role_creation_not_claimed=True,business_SQL_writes=0))
        return before
    def exercise(self,before):
        primary=None;precondition={'If-Role-Matrix-Revision':self.revision}
        try:
            self.install_fault();self.call('admin','PUT',self.path,self.payload,headers=precondition,want=503,code='admin_unavailable',scenario='failure',index=0)
            failed=self.ledger();self.save_ledger('failed',failed);verify_rollback(before,failed)
            self.marker_case('rollback',dict(status=503,error_code='admin_unavailable',all_roles_grants_scope_revision_audit_and_public_unchanged=True))
            for index in (0,1):self.read(self.expected,self.revision,index)
            self.call('target','GET',self.path,want=403,code='forbidden',scenario='authorization',index=1)
            verify_rollback(before,self.ledger());self.marker_case('failed_readback',dict(two_APIs=True,DTO_revision_and_authorization_unchanged=True,read_side_effects=0))
        except BaseException as error:primary=error
        finally:
            try:self.remove_fault()
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'restore-secondary-error.json',error,primary)
        if primary is not None:raise primary
        self.marker_case('fault_removed',dict(trigger=False,function=False))
        _,headers=self.call('admin','PUT',self.path,self.payload,headers=precondition,index=1,scenario='failure',validate=lambda v:check(v=={'ok':True},'samebody recovery acknowledgement differs'))
        recovered=self.ledger();self.save_ledger('recovered',recovered);audit=verify_update(before,recovered,self.actor,self.roles['target'],self.expected['matrix'],self.payload['matrix'])
        new_revision=base.header(headers,'X-Role-Matrix-Revision');check(base.revision(headers,'X-Role-Matrix-Revision')==before['role']['v3_matrix_revision']+1,'recovery revision did not advance exactlyonce')
        check(base.header(headers,'Cache-Control')=='private, no-store','recovery cache contract differs')
        expected=copy.deepcopy(self.expected);expected['matrix']=self.payload['matrix']
        self.marker_case('recovery',dict(status=200,API_index=1,same_body_and_prior_revision=True,explicit_recovery=True,revision_delta=1,no_auto_retry=True))
        self.marker_case('exact_audit',dict(audit_id=audit['id'],audit_delta=1,exact_stable_codes=True,complete_prefix_preserved=True))
        for index in (0,1):self.read(expected,new_revision,index)
        verify_rollback(recovered,self.ledger());self.marker_case('read_consistency',dict(two_APIs=True,fullDTO_revision_matches=True,other_public_tables_unchanged=True))
        self.read(expected,new_revision,0,user='target');verify_rollback(recovered,self.ledger())
        self.marker_case('permission_effect',dict(target_reader_before_and_after_fault=403,after_recovery=200,fresh_authorization_from_same_session=True))
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader','target'):self.login(actor)
        self.exercise(self.baseline());self.frozen()
        check(self.calls==14,'reviewed exact14 HTTP scope differs')
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],
            required_cases=REQUIRED_CASES,http_calls=self.calls,normal_count_increase=0,counts_as_whole_acceptance=False,
            unverified=['public role creation producer','real organization identities','inflight revocation','legacy writer interleaving','crash/corrupt row','performance/full chain'],existing_normal_CAS_noop_proof='c63c2fdb-702e-4e93-8295-192f1fe36f02')
        base._private(self.work/'role-audit-result.json',result);return result
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
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'role owned API port occupied')
        emit('role_audit.cleanup','passed',dict(single_cleanup_seconds=40,restore_within4=True,owned_UUID_only=True,ports_free=list(PORTS)))
_source=textwrap.dedent(inspect.getsource(base.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_ns=dict(base.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared fault/PG deadline]','exec'),_ns);bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=14,normal_count_increase=0,run_timeout_seconds=420,cleanup_grace_seconds=45,cleanup_seconds=40,restore_seconds=4,API_timeout_seconds=12)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private management lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('role_audit.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('role_audit.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
