"""One migration-produced role GET: repeat, narrow SELECT503, exact recovery.
Six HTTP; authorized synthetic identities only; no role creation/write route.
Dedicated column ACL seam, not a shared ACL platform. Prepared native proof only.
"""
from __future__ import annotations
import argparse, copy, hashlib, importlib.util, inspect, json, re, socket, textwrap
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-role-read-storage-failure';PORTS=(18952,18953)
_spec=importlib.util.spec_from_file_location('role_read_private_core',ROOT/'tools/testlab/api_core_reads_storage_failure.py')
core=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(core)
ro,inc=core.ro,core.inc
check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=core.SOURCE,core.BINARY,core.RUNTIME,core.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TABLE='rbac_roles';AUTH_COLUMNS=('id','v3_kind');DETAIL_COLUMNS=('name','description','v3_matrix_revision')
TARGETS=(('GET','/roles/{id}'),);SUPPORT=(('POST','/auth/login'),('GET','/roles'))
ERROR={'error':{'code':'admin_unavailable','message':'Administration storage unavailable'}}
REQUIRED_CASES=tuple('role_read.'+n for n in ('target','baseline','repeat','fault','restore','recovery','ledger','cleanup'))+tuple('role_read.api_ready.'+str(i) for i in (0,1))+('readonly.fresh_database','readonly.synthetic_identities','readonly.cleanup','readonly.complete')
def hashes():
    return {**core.hashes(),**{n:core.digest(ROOT/'tools/testlab'/n) for n in ('api_role_read_storage_failure.py','tests/test_api_role_read_storage_failure.py')}}
inc.hashes=hashes
def evidence(row):
    return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalRoleReadStorageFailure',proof_scope='migration builtin role read/repeat/narrow column SELECT503/recovery; full public no-write; authorized synthetic identities; no HTTP role creation, write-fault replacement, normal gain or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
def append_json(path,row):return core._append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(core.Contract.__bases__[0]):
    def canonical(self,method,path):
        p=urlsplit(path);check(not any((p.scheme,p.netloc,p.query,p.fragment)),'exact role read path required')
        canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside one role GET and login/list');return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
_source=inspect.getsource(core.cap.gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-role-read-storage-failure'),('conversation_boundaries_authorized','role_read_storage_failure_authorized')):
    check(_source.count(old)==1,'released admission seam changed');_source=_source.replace(old,new)
_ns=dict(core.cap.gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_source),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission'];core.admission=admission
def doctor(root):return dict(core.doctor(root),helper_sha256=hashes())
def header(headers,name):return next((v for k,v in headers.items() if k.lower()==name.lower()),'')
def verify_error(value,headers):
    check(value==ERROR and header(headers,'Content-Type').split(';')[0].strip()=='application/json','exact role detail storage503 required, not earlier session/environment/module failure')
def semantic_acl(value):
    # Ordinary GRANT can materialize an original NULL relacl. The complete
    # effective table ACL is the passed core contract; column raw ACLs restore
    # exactly. Raw table ACL is retained separately and never claimed identical.
    v=copy.deepcopy(value);v.pop('table_acl_raw')
    for col in v['column_acls']:col.pop('raw')
    return v
def fault_acl(before):
    v=semantic_acl(before);v['select']=False;v['entries']=[e for e in v['entries'] if e['privilege']!='SELECT']
    entry=dict(grantor=before['owner'],grantee=before['role_oid'],privilege='SELECT',grantable=False)
    for col in v['column_acls']:
        col['select']=col['name'] in AUTH_COLUMNS;col['entries']=[entry] if col['select'] else []
    return v
def guard_before(v):
    check(v['select'] is True and all(c['raw'] is None and c['entries']==[] and c['select'] is True for c in v['column_acls']),'original table SELECT and no column ACL required')
    check(len([e for e in v['entries'] if e['privilege']=='SELECT'])==1 and all(e['grantor']==v['owner'] and e['grantee']==v['role_oid'] and not e['grantable'] for e in v['entries']),'only unambiguous original owner ACL without grantoption permitted')
def guard_restored(v,before):
    check(semantic_acl(v)==semantic_acl(before) and v['column_acls']==before['column_acls'],'complete original effective table and raw column ACLs must restore exactly')
class Chain(core.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id,permit);self.contract=Contract(root);self.path=None;self.target=None;self.acl_before=None;self.acl_fault=False;self.fault_snapshot=None
        self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,target_source='migration_builtin',HTTP_role_creation_claimed=False,SQL_business_writes=0);ro._private(self.work/'state.json',self.state)
    def marker_case(self,name,actual):
        check('role_read.'+name in REQUIRED_CASES,'unknown role marker');self.frozen();emit('role_read.'+name,'passed',dict(run_id=self.run_id,**actual))
    def start(self):
        original=ro.emit
        def ready(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='role_read.api_ready.'+name[-1]
            return original(name,status,actual,**fields)
        ro.emit=ready
        try:ro.Chain.start(self)
        finally:ro.emit=original
    def seed(self):ro.Chain.seed(self)
    def ledger(self):
        v=super().ledger();check(TABLE in v,'role table must remain in complete public ledger');return v
    def auth_read(self,phase):
        # Same runtime DB user and global view predicates used by environment,
        # module and getRole transaction rechecks. No session token is queried
        # or persisted; successful baseline/login and full ledger cover it.
        statement="SELECT json_build_object('environment',EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id="+q(self.actor)+" AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*'),'roles',EXISTS(SELECT 1 FROM v3_effective_role_grants WHERE user_id="+q(self.actor)+" AND module='roles' AND action='view' AND scope_kind='global' AND scope_id='*'),'role_auth_columns',EXISTS(SELECT 1 FROM rbac_roles WHERE id='admin' AND v3_kind='system'),'actor_current',EXISTS(SELECT 1 FROM users WHERE id="+q(self.actor)+" AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo'))"
        value=json.loads(self.sql(statement,read=True));check(value==dict(environment=True,roles=True,role_auth_columns=True,actor_current=True),'current runtime role/environment/module authorization read must survive only-id/kind fault')
        ro._private(self.work/('role-auth-'+phase+'.json'),dict(runtime_DB_user=self.db_user,view='v3_effective_role_grants',role_columns=list(AUTH_COLUMNS),checks=value,session_token_stored=False));return value
    def acl(self):
        self.owner()
        statement="""SELECT json_build_object('database',current_database(),'table',c.relname,'oid',c.oid::bigint,
 'owner',pg_get_userbyid(c.relowner),'rls',c.relrowsecurity,
 'select',has_table_privilege(%s,c.oid,'SELECT'),'table_acl_raw',c.relacl::text,
 'role_oid',(SELECT oid::bigint FROM pg_roles WHERE rolname=%s),
 'superuser',(SELECT rolsuper FROM pg_roles WHERE rolname=%s),
 'bypass_rls',(SELECT rolbypassrls FROM pg_roles WHERE rolname=%s),
 'memberships',(SELECT count(*) FROM pg_auth_members WHERE member=(SELECT oid FROM pg_roles WHERE rolname=%s)),
 'view',(SELECT json_build_object('owner',pg_get_userbyid(v.relowner),'kind',v.relkind,'options',v.reloptions,
 'digest',encode(sha256(convert_to(pg_get_viewdef(v.oid),'UTF8')),'hex')) FROM pg_class v JOIN pg_namespace vn ON vn.oid=v.relnamespace WHERE vn.nspname='public' AND v.relname='v3_effective_role_grants'),
 'entries',(SELECT COALESCE(jsonb_agg(jsonb_build_object('grantor',pg_get_userbyid(a.grantor),'grantee',a.grantee::bigint,'privilege',a.privilege_type,'grantable',a.is_grantable) ORDER BY a.grantor,a.grantee,a.privilege_type),'[]'::jsonb) FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) a),
 'column_acls',(SELECT jsonb_agg(jsonb_build_object('number',x.attnum,'name',x.attname,'raw',x.attacl::text,
 'select',has_column_privilege(%s,c.oid,x.attnum,'SELECT'),
 'entries',(SELECT COALESCE(jsonb_agg(jsonb_build_object('grantor',pg_get_userbyid(a.grantor),'grantee',a.grantee::bigint,'privilege',a.privilege_type,'grantable',a.is_grantable) ORDER BY a.grantor,a.grantee,a.privilege_type),'[]'::jsonb) FROM aclexplode(COALESCE(x.attacl,'{}'::aclitem[])) a)) ORDER BY x.attnum) FROM pg_attribute x WHERE x.attrelid=c.oid AND x.attnum>0 AND NOT x.attisdropped))
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='rbac_roles' AND c.relkind='r'""" % ((q(self.db_user),)*6)
        v=json.loads(self.pg('BEGIN READ ONLY; '+statement+'; COMMIT;',self.database,'testlab'))
        check(v.get('database')==self.database and v.get('table')==TABLE and v.get('owner')==self.db_user and type(v.get('role_oid')) is int and type(v.get('oid')) is int and v.get('superuser') is False and v.get('bypass_rls') is False and v.get('memberships')==0 and v.get('rls') is False,'exact isolated role/table owner and no permission bypass required')
        view=v.get('view',{});check(view.get('owner')==self.db_user and view.get('kind')=='v' and view.get('options') is None and bool(re.fullmatch('[a-f0-9]{64}',view.get('digest',''))),'default own view owner/definition/options required')
        cols=v.get('column_acls');check(isinstance(cols,list) and len(cols)<=32 and len({c['name'] for c in cols})==len(cols) and set(AUTH_COLUMNS+DETAIL_COLUMNS).issubset({c['name'] for c in cols}) and isinstance(v.get('entries'),list),'complete bounded column ACL inventory required');return v
    def revoke(self):
        self.frozen();self.owner();before=self.acl();guard_before(before);self.acl_before=before
        ro._private(self.work/'role-acl-intent.json',before);self.acl_fault=True
        self.pg('BEGIN; REVOKE SELECT ON TABLE public."rbac_roles" FROM '+ro.identifier(self.db_user)+'; GRANT SELECT (id, v3_kind) ON TABLE public."rbac_roles" TO '+ro.identifier(self.db_user)+'; COMMIT;',self.database,'testlab')
        after=self.acl();check(semantic_acl(after)==fault_acl(before),'only id/v3_kind SELECT may remain; detail SELECT and table SELECT must be false');self.fault_snapshot=after;ro._private(self.work/'role-acl-revoked.json',after);self.auth_read('fault')
    def restore(self):
        if not self.acl_fault:return
        current=self.acl();before=self.acl_before
        if semantic_acl(current)!=semantic_acl(before) or current['column_acls']!=before['column_acls']:
            check(semantic_acl(current)==fault_acl(before) and (self.fault_snapshot is None or current==self.fault_snapshot),'foreign ACL/schema/view drift; refuse restoration')
            self.pg('BEGIN; REVOKE SELECT (id, v3_kind) ON TABLE public."rbac_roles" FROM '+ro.identifier(self.db_user)+'; GRANT SELECT ON TABLE public."rbac_roles" TO '+ro.identifier(self.db_user)+'; COMMIT;',self.database,'testlab')
        restored=self.acl();guard_restored(restored,before);self.acl_fault=False;ro._private(self.work/'role-acl-restored.json',dict(original=before,restored=restored,complete_effective_table_ACL_equal=True,raw_column_ACLs_equal=True,raw_table_relacl_equal=before['table_acl_raw']==restored['table_acl_raw']))
    def inputs(self):
        self.actor=self.sql('SELECT id FROM users WHERE email='+q(self.admin_email),read=True);check(bool(re.fullmatch('[a-f0-9]{32}',self.actor)),'actual bootstrap admin ID required')
        self.target=json.loads(self.sql("SELECT json_build_object('id',id,'name',name,'description',description,'kind',v3_kind,'builtin',builtin,'revision',v3_matrix_revision,'grants',(SELECT COALESCE(json_agg(json_build_object('module',g.module,'action',g.action,'scope_kind',g.scope_kind,'scope_id',g.scope_id,'assurance',g.assurance) ORDER BY g.module,g.action,g.scope_kind,g.scope_id,g.assurance),'[]'::json) FROM v3_role_grants g WHERE g.role_id=r.id)) FROM rbac_roles r WHERE id='viewer'",read=True))
        ro._private(self.work/'role-target-observed.json',dict(target=self.target,source='migration_builtin',before_guard=True,credentials_stored=False))
        # 029 seeds workspace/view/global/*; 031 adds standard assurance and
        # fresh matrix revision 1. Workspace is outside the 13-row catalog.
        check(isinstance(self.target,dict) and self.target.get('id')=='viewer' and self.target.get('builtin') is True and self.target.get('kind')=='system' and type(self.target.get('revision')) is int and self.target['revision']==1 and self.target.get('grants')==[{'module':'workspace','action':'view','scope_kind':'global','scope_id':'*','assurance':'standard'}],'migration builtin viewer exact workspace grant and fresh revision required')
        value,_=self.call('admin','GET','/api/v1/roles',want=200,scenario='fixture')
        check(isinstance(value.get('items'),list) and len([x for x in value['items'] if x.get('id')==self.target['id'] and x.get('kind')=='system'])==1,'formal role catalog must expose exactly the selected actual migration role')
        self.path='/api/v1/roles/'+self.target['id'];self.auth_read('before');ro._private(self.work/'role-target.json',dict(target=self.target,source='migration_builtin',root_decision='authorized owned UUID migration-produced role target; no formal HTTP role creation claimed',synthetic_identity_fixture=True,SQL_business_writes=0))
        self.marker_case('target',dict(id=self.target['id'],source='migration_builtin',builtin=True,HTTP_catalog=True,HTTP_role_creation_claimed=False))
    def verify_read(self,value,headers):
        t=self.target;check(value.get('id')==t['id'] and value.get('name')==t['name'] and value.get('desc')==t['description'] and value.get('kind')=='system' and value.get('scope')==['global'] and len(value.get('actions',[]))==5 and len(value.get('matrix',[]))==13 and all(row.get('grants')==[0,0,0,0,0] for row in value['matrix']) and value.get('notes')=='Machine identities cannot approve. Approval remains unavailable until passkey step-up verification is implemented, and the author cannot approve their own proposal. Other platform grants are managed outside this matrix. Only grants in the selected authorized environments are shown.','builtin workspace grant must show global scope, matrix-external note and zero matrix')
        check(header(headers,'X-Role-Matrix-Revision')==str(t['revision']) and header(headers,'Cache-Control')=='private, no-store','exact revision/cache headers required')
    def call(self,user,method,path,*args,**kwargs):
        self.frozen();kwargs.setdefault('scenario','failure' if kwargs.get('want')==503 else 'boundary');original=ro.transport
        received_headers={}
        if method=='GET' and path==self.path:
            caller_validate=kwargs.get('validate')
            def validate_detail(value):
                # Validate before the shared call publishes success, retaining
                # the received HTTP status if the DTO or headers are invalid.
                self.verify_read(value,received_headers)
                if caller_validate is not None:caller_validate(value)
            kwargs['validate']=validate_detail
        def wire(base,verb,target,payload=None,headers=None,**options):
            result=original(base,verb,target,payload,headers,**options)
            received_headers.update(result[2])
            if verb=='GET' and target in ('/api/v1/roles',self.path):
                status,raw,hs=result;check(len(raw)<=65536,'bounded role evidence required');value=json.loads(raw)
                ro._private(self.work/('role-read-wire-'+str(self.calls)+'.json'),dict(method=verb,path=target,API_index=ro.BASES.index(base),status=status,response=value,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),headers={k:v for k,v in hs.items() if k.lower() in ('content-type','cache-control','x-role-matrix-revision')},credentials_stored=False))
                if status==503:verify_error(value,hs)
            return result
        ro.transport=wire
        try:return ro.Chain.call(self,user,method,path,*args,**kwargs)
        finally:ro.transport=original
    def exercise(self,baseline):
        expected=None
        def same_projection(value):
            check(expected is None or value==expected,'role DTO differs from baseline')
        for phase,index in (('baseline',0),('repeat',1)):
            value,_=self.call('admin','GET',self.path,index=index,want=200,validate=same_projection)
            if expected is None:expected=value
            check(self.ledger()==baseline,'role read changed full public ledger');self.marker_case(phase,dict(HTTP200=True,API_index=index,all_public_unchanged=True))
        primary=None
        try:
            self.revoke();self.call('admin','GET',self.path,index=0,want=503,code='admin_unavailable')
            check(self.ledger()==baseline,'failed role read changed full public ledger');self.marker_case('fault',dict(HTTP503=True,exact_detail_mapper=True,table_SELECT=False,auth_columns_SELECT=True,detail_columns_SELECT=False,all_public_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.restore()
            except BaseException as error:
                if primary is None:primary=error
                else:
                    try:ro._private(self.work/'restore-secondary.json',dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
                    except BaseException:self.restore_secondary_note_failed=True
        if primary is not None:raise primary
        self.marker_case('restore',dict(complete_effective_table_ACL_equal=True,raw_column_ACLs_equal=True,no_retry=True))
        self.call('admin','GET',self.path,index=1,want=200,validate=same_projection)
        check(self.ledger()==baseline,'recovered full public ledger differs');self.marker_case('recovery',dict(HTTP200=True,API_index=1,same_cookie=True,all_public_unchanged=True))
    def run(self):
        self.provision();self.start();self.seed();self.login('admin');self.inputs();baseline=self.ledger();ro._private(self.work/'ledger-baseline.json',baseline);self.exercise(baseline)
        check(self.ledger()==baseline and not self.acl_fault and self.calls==6,'exact six HTTP/final ACL/ledger boundary differs');ro._private(self.work/'ledger-final.json',baseline);self.marker_case('ledger',dict(target_HTTP=4,fixture_HTTP=2,all_public_tables=len(baseline),sessions_and_audit_unchanged=True))
        result=dict(run_id=self.run_id,http_calls=6,target_HTTP=4,fixture_HTTP=2,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,target_source='migration_builtin',source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'role-read-result.json',result);return result
    def cleanup(self):
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own role read listener remains')
        emit('role_read.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,unrestored_fault_resource_retired=self.acl_fault))
def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
    if a.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=6,target_HTTP=4,fixture_HTTP=2,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45,target_source='migration_builtin')));return 0
    if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
    try:
        check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
        with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
        return 0
    except Blocked:emit('role_read.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as error:emit('role_read.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
