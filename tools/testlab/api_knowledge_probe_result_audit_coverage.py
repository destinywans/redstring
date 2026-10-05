"""Second probe-result audit failure, preserving the separately committed attempt.

Preparation is offline. Explicit root admission is required before any resource
acquisition. Real local upload bytes only; no source sync, model or external read.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, importlib.util, inspect, json, os, re, socket, sqlite3, stat, textwrap, time, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-api-knowledge-probe-result-audit'; PORTS=(18820,18821)
spec=importlib.util.spec_from_file_location('knowledge_probe_private_sources',ROOT/'tools/testlab/api_knowledge_sources_interfaces_coverage.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);ro=base.ro
check,Blocked,q=base.check,base.Blocked,base.q
SOURCE,BINARY,RUNTIME,PG_CID=base.SOURCE_SHA,base.BINARY_SHA,base.RUNTIME_SHA,base.PG_CID
TARGETS=(('POST','/knowledge-sources/test'),)
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/uploads'))
REQUIRED_CASES=tuple('knowledge_probe.'+n for n in ('http_input','result_audit_failure','attempt_preserved','fault_removed','recovery','no_persistence','cleanup'))+('readonly.cleanup','readonly.complete')
base.PORTS=ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)

def hashes():
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in
        ('api_knowledge_probe_result_audit_coverage.py','tests/test_api_knowledge_probe_result_audit_coverage.py',
         'api_knowledge_sources_interfaces_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py')}
base.hashes=hashes
def evidence(row):
    return dict(row,evidence_mode='real_local',upstream_mode='real_local',test_name='FormalKnowledgeProbeSecondAuditFailure',
        source_tree_sha=SOURCE,binary_sha256=BINARY,registration_evidence=False,
        proof_scope='second result audit only; real local upload read; synthetic identity/ACL; no new normal count or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=ro.emit=emit;ro.append_json=append_json

def admission(path,run_id):
    path=Path(path)
    check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonical UUID/private admission required')
    info=path.lstat();check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid(),'own regular0600 admission required')
    permit=ro.private_json(path)
    expected=dict(status='root-admitted-knowledge-probe-result-audit',owner=ro.OWNER,resource_run_id=run_id,
        source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'exact probe fault admission differs')
    for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','second_result_audit_fault_authorized'):
        check(permit.get(key) is True,'missing root prerequisite '+key)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole matching actual Runner/manifest/source/parent required')
    return permit

class Contract(ro.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path)
        check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative probe path required')
        canonical,kind=super().canonical(method,path)
        check((method,canonical.removeprefix('/api/v1')) in (*TARGETS,*SUPPORT),'outside second-audit probe inventory')
        return canonical,kind

def fault_statements(nonce,actor,app):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)) and bool(re.fullmatch('[a-f0-9]{32}',actor))
        and bool(re.fullmatch('[A-Za-z0-9_-]+',app)),'exact UUID actor/application required')
    name='knowledge_probe_fail_'+nonce
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+
        ' AND NEW.application_id='+q(app)+" AND NEW.action='v3.knowledge_source.connection.test' AND NEW.target='upload' AND NEW.outcome='success'"+
        " AND NEW.changes=jsonb_build_object('environment_id','test','team_id','') THEN RAISE EXCEPTION 'owned second probe audit fault'; END IF; RETURN NEW; END $$; "+
        'CREATE TRIGGER '+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop='BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
    return name,create,drop

def assert_audits(before,after,outcomes,actor,app):
    check(before['business']==after['business'],'probe changed complete business/identity/source/bytes/sync/runbook ledger')
    old=before['audits'];rows=after['audits'][len(old):]
    check(after['audits'][:len(old)]==old and len(rows)==len(outcomes),'complete audit prefix/count differs')
    check([r['outcome'] for r in rows]==outcomes,'separately committed probe audit outcomes differ')
    for row in rows:
        check(all(row.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id=app,
            action='v3.knowledge_source.connection.test',target='upload',changes={'environment_id':'test','team_id':''}).items()),'exact audit actor/scope/redaction differs')
    return rows

class Chain(base.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.fault_attempted=False
        self.state.update(suite=MODULE,targets=TARGETS,ports=list(PORTS),normal_count_increase=0,counts_as_whole_acceptance=False)
        ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen()
        check(hashlib.sha256((ROOT/'.local/testlab/runtime-config.json').read_bytes()).hexdigest()==RUNTIME,'runtime bytes changed')
    def marker_case(self,suffix,actual):
        name='knowledge_probe.'+suffix;check(name in REQUIRED_CASES,'unknown probe marker')
        self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def owner(self,deadline=None):
        query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        value=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(value==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'UUID DB/role owner differs')
    def ledger(self):
        self.frozen();self.owner()
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename<>'management_audit'",read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public table inventory required')
        check({'v3_uploads','v3_upload_revocations','v3_knowledge_source_registrations','v3_knowledge_source_reads','v3_knowledge_source_observations',
            'v3_external_source_documents','v3_external_source_syncs','v3_runbooks'}<=set(tables),'required source/bytes/sync/runbook tables missing')
        # Hash whole ordered rows inside PG, including bytea and timestamps. No
        # plaintext business rows, session hashes or upload bytes leave PG.
        fields=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        statement="SELECT json_build_object('business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(fields)+") hashes),'audits',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))"
        return json.loads(self.sql(statement,read=True))
    def install_fault(self):
        self.frozen();self.owner()
        create=fault_statements(self.nonce,self.users['reader'],self.app)[1]
        self.fault_attempted=True # Retain recovery intent before any unknown DDL outcome.
        ro._private(self.work/'probe-fault-intent.json',dict(database=self.database,actor=self.users['reader'],application=self.app,second_success_only=True))
        self.sql(create)
    def remove_fault(self,deadline=None):
        if not self.fault_attempted:return
        deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4
        self.owner(deadline=deadline)
        name,_,drop=fault_statements(self.nonce,self.users['reader'],self.app)
        self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        absent=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(absent==dict(trigger=False,function=False),'own second-audit fault remains')
        self.fault_attempted=False
    def inputs(self):
        key='knowledge-probe-'+self.nonce
        self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201,scenario='fixture')
        self.app=self.sql('SELECT id FROM applications WHERE key='+q(key),read=True)
        check(bool(re.fullmatch('[A-Za-z0-9_-]+',self.app)),'exact HTTP application missing')
        self.grant('reader',self.app)
        body=('# Owned second-audit fixture\n\n'+self.marker+' inert text.\n').encode()
        url=self.upload('reader',body)
        self.payload={k:v for k,v in base.registration(self.app,url,self.marker).items() if k in base.CONNECTION_FIELDS}
        self.marker_case('http_input',dict(real_local_owner_upload=True,upload_bytes_sha256=hashlib.sha256(body).hexdigest(),SQL_business_writes=0,external_reads=0))
    def secondary(self,path,error,primary):
        try:self.diagnostic(path,dict(error_type=type(error).__name__,first_failure_preserved=True))
        except BaseException as report_error:
            primary.add_note('secondary diagnostic unavailable: '+type(report_error).__name__)
    def exercise(self):
        before=self.ledger();primary=None
        try:
            self.install_fault()
            self.call('reader','POST','/api/v1/knowledge-sources/test',self.payload,want=503,code='audit_unavailable',scenario='failure',index=0)
            failed=self.ledger();rows=assert_audits(before,failed,['attempted'],self.users['reader'],self.app)
            self.marker_case('result_audit_failure',dict(status=503,error_code='audit_unavailable',second_success_audit_rejected=True))
            self.marker_case('attempt_preserved',dict(attempted_delta=1,result_delta=0,attempted_id=rows[0]['id'],business_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.remove_fault()
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'fault-restore-secondary-error.json',error,primary)
        if primary is not None:raise primary
        self.marker_case('fault_removed',dict(trigger=False,function=False))
        self.call('reader','POST','/api/v1/knowledge-sources/test',self.payload,index=1,scenario='failure',validate=base.probe_result)
        recovered=self.ledger();assert_audits(failed,recovered,['attempted','success'],self.users['reader'],self.app)
        assert_audits(before,recovered,['attempted','attempted','success'],self.users['reader'],self.app)
        self.marker_case('recovery',dict(status=200,API_index=1,identical_payload=True,explicit_recovery=True,audit_delta=2,no_automatic_retry=True))
        self.marker_case('no_persistence',dict(all_public_business_tables_unchanged=True,upload_bytes_unchanged=True,total_probe_audits=3,persistent=False,scope_verified=False))
    def run(self):
        self.provision();self.start();self.seed()
        for user in ('admin','reader'):self.login(user)
        self.inputs();self.exercise();self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],
            required_cases=REQUIRED_CASES,http_calls=self.calls,normal_count_increase=0,counts_as_whole_acceptance=False,
            proof_scope='real local upload probe second result audit fault; separately committed attempted audit survives',
            unverified=['external credential readers','sync/retrieval/index','midflight revocation/crash','first attempted-audit fault','performance/full chain/real organization'])
        ro._private(self.work/'knowledge-probe-result-audit-result.json',result);return result
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
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'owned probe API port remains occupied')
        emit('knowledge_probe.cleanup','passed',dict(single_cleanup_seconds=40,restore_within_same_deadline=True,owned_UUID_only=True,ports_free=list(PORTS)))

_source=textwrap.dedent(inspect.getsource(ro.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup source drift')
_ns=dict(ro.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[single fault/PG cleanup deadline]','exec'),_ns)
bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true')
    parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,targets=TARGETS,required_cases=REQUIRED_CASES,ports=PORTS,http_calls=6,normal_count_increase=0)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id)
        fd=os.open(ROOT/'.local/testlab/knowledge-interfaces.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private knowledge lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():ro.execute(Chain(ROOT,base.doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('knowledge_probe.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('knowledge_probe.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
