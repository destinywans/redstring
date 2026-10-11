"""Formal empty Agent trace consumer with honest partial metadata and live DB window.

Only --live with root admission acquires resources. SQL writes are synthetic
identity/ACL only; no business rows, source configuration, inference or DDL fault.
"""
from __future__ import annotations
import argparse,calendar,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlencode,urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-trace-agent-empty';PORTS=(18848,18849);COMMAND_BUDGET=420
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
spec=importlib.util.spec_from_file_location('trace_agent_empty_private_management',ROOT/'tools/testlab/api_management_chain.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);check,Blocked,q=base.check,base.Blocked,base.quote
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
base.TARGETS=(('GET','/traces'),);base.SUPPORT=(('POST','/auth/login'),)
REQUIRED_CASES=tuple('trace_agent_empty.'+n for n in ('identities','empty_source','API0','API1','static_consistency','anonymous','invalid_queries','ledger','cleanup'))+('management.api_ready.0','management.api_ready.1','management.cleanup','management.complete')
CLOCK_TOLERANCE_SECONDS=2
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_trace_agent_empty_coverage.py','tests/test_api_trace_agent_empty_coverage.py',
        'api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,
    binary_platform='darwin/arm64 Mach-O',test_name='FormalTraceAgentEmpty',registration_evidence=False,
    proof_scope='scope=agent honest empty/partial persisted consumer only; no authoritative execution/spans/cost or Tempo completeness; no whole acceptance')
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
    expected=dict(status='root-admitted-trace-agent-empty',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes(),clock_tolerance_seconds=CLOCK_TOLERANCE_SECONDS)
    check(all(permit.get(k)==v for k,v in expected.items()),'trace agent root admission identity differs')
    for k in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','trace_agent_empty_authorized'):check(permit.get(k) is True,'missing root prerequisite '+k)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit
EXACT_QUERY='/api/v1/traces?scope=agent&view=all'
INVALID_QUERIES=((EXACT_QUERY.replace('view=all','view=unknown'),'invalid_view'),(EXACT_QUERY+'&scope=agent','invalid_query'),(EXACT_QUERY+'&view=all','invalid_query'),(EXACT_QUERY+'&'+urlencode({'q':'status = "running"'}),'invalid_query'))
TABLES=('v3_agents','v3_agent_runs','v3_agent_run_spans','v3_agent_run_feedback','management_audit','applications')
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.fragment)),'relative own trace/auth path required')
        if parsed.path=='/api/v1/traces':check(method=='GET' and path in (EXACT_QUERY,*(p for p,_ in INVALID_QUERIES)),'only exact Agent query and declared illegal fixtures permitted')
        else:check(not parsed.query,'no auth query permitted')
        return super().canonical(method,path)
VIEW_OPTIONS=[{'id':id,'label':label,'count':0,'query':query} for id,label,query in [('all','All',''),('errors','Failed, blocked or reported errors',''),('slow','Slow','duration >= 1s'),('blocked','Blocked','status = "blocked"')]]
FACETS=[{'key':k,'label':k.capitalize(),'items':[]} for k in ('agent','model','status','version','service')]
STATIC=dict(scope='agent',total=0,colors={},items=[],scatter=[],source='v3_agent_runs_and_reported_spans',aggregation_scope='authorized_persisted_finished_runs',excluded_running=0,partial=True,missing=['producer_coverage','complete_span_coverage','cost'],histogram_non_success_states=['failed','blocked','reported_span_error'],views=VIEW_OPTIONS,facets=FACETS)
def utc_now():return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
def timestamp_ns(value):
    match=re.fullmatch(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.([0-9]{1,9}))?Z',value) if isinstance(value,str) else None
    check(match is not None,'actual UTC RFC3339Nano required')
    seconds=calendar.timegm(datetime.datetime.strptime(match[1],'%Y-%m-%dT%H:%M:%S').timetuple())
    return seconds*1000000000+int((match[2] or '').ljust(9,'0'))
def static_projection(value):
    v=copy.deepcopy(value)
    for name in ('window_start_at','window_end_at','took'):v.pop(name)
    v['histogram'].pop('axis_times');return v
def verify_empty(value,before,after,tolerance=CLOCK_TOLERANCE_SECONDS):
    check(set(value)==set(STATIC)|{'histogram','took','window_start_at','window_end_at'},'exact partial trace DTO fields differ')
    check(all(value[k]==v for k,v in STATIC.items()) and value['partial'] is True,'honest empty/partial persisted metadata differs')
    check(type(value['total']) is int and type(value['excluded_running']) is int and all(type(v['count']) is int for v in value['views']),'trace count JSON types differ')
    check(isinstance(value['took'],str) and re.fullmatch(r'(0|[1-9][0-9]*) ms',value['took']),'actual nonnegative ms string required')
    check(set(value['histogram'])=={'counts','axis_times'} and value['histogram']['counts']==[[0,0] for _ in range(12)] and all(type(n) is int for pair in value['histogram']['counts'] for n in pair),'12 numeric empty histogram buckets differ')
    check(type(tolerance) is int and 0<=tolerance<=2,'reviewed bounded clock tolerance required')
    low,high=timestamp_ns(before),timestamp_ns(after);start,end=timestamp_ns(value['window_start_at']),timestamp_ns(value['window_end_at'])
    check(low<=high and low-tolerance*1000000000<=end<=high+tolerance*1000000000,'DB clock window outside real request wall bounds')
    check(end-start==24*60*60*1000000000,'actual window must be exactly24h including nanos')
    origin=datetime.datetime.fromtimestamp(start//1000000000,datetime.timezone.utc)
    expected=[(origin+datetime.timedelta(hours=2*i)).strftime('%Y-%m-%dT%H:%M:%SZ') for i in range(12)]
    check(value['histogram']['axis_times']==expected,'12 RFC 3339 bucket starts must derive from actual windowstart')
    return static_projection(value)
def same(before,after):check(before==after,'trace read/refusal changed full Agent run/span/feedback/audit/application ledger or public hashes')
class Chain(base.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.raw_number=0
        self.state.update(suite=MODULE,normal_added=0,normal_registration_pending_root=True,counts_as_whole_acceptance=False,binary_platform='darwin/arm64 Mach-O',targets=base.TARGETS,model_scope='inherited idle management-synthetic startup placeholder at127.0.0.1:9; no inference/Tempo/trace producer')
        base._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen();frozen_identity(self.data);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    def marker_case(self,suffix,actual):
        name='trace_agent_empty.'+suffix;check(name in REQUIRED_CASES,'unknown trace marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def seed(self):
        actor=self.users['reader'];role=self.roles['reader'];email='member-'+self.nonce+'-reader@testlab.invalid'
        self.sql('BEGIN; INSERT INTO users(id,email,name,role,password_hash) SELECT '+q(actor)+','+q(email)+','+q(self.marker+' reader')+",'viewer',password_hash FROM users WHERE email="+q(self.admin_email)+'; DELETE FROM rbac_user_roles WHERE user_id='+q(actor)+'; INSERT INTO rbac_roles(id,name,v3_kind) VALUES('+q(role)+','+q(role)+",'business'); INSERT INTO rbac_user_roles(user_id,role_id) VALUES("+q(actor)+','+q(role)+'); INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES '+','.join('('+','.join(q(v) for v in (role,module,'view','environment','test'))+')' for module in ('environment','agents','traces'))+'; COMMIT;')
        actual=json.loads(self.sql('SELECT json_build_object(\'actor\',(SELECT id FROM users WHERE email='+q(email)+"),'grants',(SELECT json_agg(json_build_object('module',module,'action',action,'scope_kind',scope_kind,'scope_id',scope_id) ORDER BY module) FROM v3_role_grants WHERE role_id="+q(role)+'))',read=True))
        expected=[dict(module=m,action='view',scope_kind='environment',scope_id='test') for m in ('agents','environment','traces')]
        check(actual==dict(actor=actor,grants=expected),'exact own human/three readonly environment grants differ')
        base._private(self.work/'identity-proof.json',actual);self.marker_case('identities',dict(actor=actor,read_only_environment_test_grants=expected,SQL_business_writes=0))
    def call(self,*args,**kwargs):
        self.frozen();original=base.transport
        def wire(origin,method,path,payload=None,headers=None,timeout=12):
            before=utc_now();result=original(origin,method,path,payload,headers,timeout);after=utc_now()
            if urlsplit(path).path=='/api/v1/traces':
                self.raw_number+=1;status,raw,response_headers=result
                self.last_window=(before,after)
                base._private(self.work/('trace-wire-'+str(self.raw_number)+'.json'),dict(method=method,path=path,API_index=base.BASES.index(origin),status=status,request_body=None,response_body=raw.decode('utf-8','replace'),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),request_wall_start=before,request_wall_end=after,clock_tolerance_seconds=CLOCK_TOLERANCE_SECONDS,Cache_Control=base.header(response_headers,'Cache-Control'),source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',synthetic_owned_only=True))
            return result
        base.transport=wire
        try:return super().call(*args,**kwargs)
        finally:base.transport=original
    def owner(self,deadline=None):
        query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        v=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline));check(v==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'ownUUID DB/role tags differ')
    def ledger(self):
        self.frozen();self.owner();excluded=','.join(q(t) for t in TABLES)
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+excluded+')',read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        hashes=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        fields=["'business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(hashes)+') hashes)']+[q(t)+",(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM public."+t+' t)' for t in TABLES]
        return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')',read=True))
    def save_ledger(self,name,value):base._private(self.work/(name+'-ledger.json'),dict(value,source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
    def read(self,index):
        before=self.ledger();value,headers=self.call('reader','GET',EXACT_QUERY,index=index)
        check(base.header(headers,'Cache-Control')=='no-store','real Agent trace no-store missing')
        stable=verify_empty(value,*self.last_window);same(before,self.ledger())
        self.marker_case('API'+str(index),dict(status=200,empty=True,partial=True,exact_24h=True,real_db_clock_request_bounds=True,histogram_buckets=12,views=4,facets=5));return stable
    def refusal(self,user,path,status,code):
        before=self.ledger();self.call(user,'GET',path,want=status,code=code,scenario='authorization' if status==401 else 'boundary');same(before,self.ledger())
    def run(self):
        self.provision();self.start();self.seed();self.login('reader')
        before=self.ledger();check(all(before[t]==[] for t in TABLES),'fresh own Agent registry/runs/spans/feedback/audit/app tables must really be empty')
        self.save_ledger('before',before);self.marker_case('empty_source',dict(real_fresh_PG_empty_tables=list(TABLES),SQL_business_writes=0,upstream_configuration=False))
        left,right=self.read(0),self.read(1);check(left==right,'dualAPI static metadata differs');self.marker_case('static_consistency',dict(two_APIs=True,window_took_axis_compared_perrequest=True,no_byte_equal_time_assumption=True))
        self.refusal(None,EXACT_QUERY,401,'unauthorized');self.marker_case('anonymous',dict(status=401,no_sideeffects=True))
        for path,code in INVALID_QUERIES:self.refusal('reader',path,422,code)
        self.marker_case('invalid_queries',dict(unknown_view=True,duplicate_scope=True,duplicate_view=True,invalid_terminal_status=True,status=422))
        after=self.ledger();same(before,after);self.save_ledger('after',after);self.marker_case('ledger',dict(full_agent_run_span_feedback_audit_app_tables_unchanged=True,all_other_public_digests_unchanged=True))
        self.frozen();result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],required_cases=REQUIRED_CASES,http_calls=self.calls,normal_added=0,normal_registration_pending_root=True,counts_as_whole_acceptance=False,partial=True,proof_scope='real empty Agent consumer; cannot close Tempo or authoritative execution/span/cost coverage',unverified=['application Tempo/alltraces completeness','Agent producer/runtime/spans/token/cost and per-object privileges','concurrency/performance/soak/fullchain'])
        base._private(self.work/'trace-agent-empty-result.json',result);return result
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;bounded_cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'owned trace API port remains occupied')
        emit('trace_agent_empty.cleanup','passed',dict(single_cleanup_seconds=40,owned_UUID_only=True,ports_free=list(PORTS),no_fault_DDL=True))
_source=textwrap.dedent(inspect.getsource(base.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_ns=dict(base.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared deadline]','exec'),_ns);bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,target=EXACT_QUERY,required_cases=REQUIRED_CASES,command_timeout_seconds=COMMAND_BUDGET,API_timeout_seconds=12,cleanup_seconds=40,cleanup_grace_seconds=45,estimated_HTTP_calls=8,normal_added=0,normal_registration_pending_root=True,partial=True,counts_as_whole_acceptance=False)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600);st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private management lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('trace_agent_empty.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('trace_agent_empty.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
