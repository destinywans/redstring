"""Three formal persisted event reads: repeat/no-write, SELECT503 and recovery.
One HTTP-produced alert; owned UUID DB role ACL only. No SQL business seed,
normal-registration gain, model, notification, performance or chain claim.
"""
from __future__ import annotations
import argparse, hashlib, importlib.util, inspect, json, re, socket, textwrap
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-core-reads-storage-failure';PORTS=(18928,18929)
def load(name,path):
    s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
cap=load('core_read_private_capacity',ROOT/'tools/testlab/api_inbox_groups_capacity_boundary.py')
inc,ro=cap.inc,cap.ro
check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=cap.SOURCE,cap.BINARY,cap.RUNTIME,cap.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/inbox'),('GET','/alert-groups'),('GET','/incidents'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'))
TABLE='ops_incidents';ERROR={'error':{'code':'events_unavailable','message':'Event storage unavailable'}}
REQUIRED_CASES=tuple('core_read.'+n for n in ('http_inputs',*[str(i)+'.'+phase for i in range(3) for phase in ('baseline','repeat','fault','restore','recovery')],'ledger','cleanup'))+tuple('core_read.api_ready.'+phase+'.'+str(i) for phase in ('setup','source') for i in (0,1))+('readonly.fresh_database','readonly.synthetic_identities','readonly.cleanup','readonly.complete')
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_core_reads_storage_failure.py','tests/test_api_core_reads_storage_failure.py','api_inbox_groups_capacity_boundary.py','api_incident_fault_interfaces_coverage.py','api_conversation_object_session_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
inc.hashes=hashes

def evidence(row):
    return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalCoreReadsStorageFailure',proof_scope='three one-alert reads/repeat200/scoped SELECT503/recover200; all-public no-write; no new normal or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=cap._append
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(inc.Contract.__bases__[0]):
    def canonical(self,method,path):
        p=urlsplit(path);check(not any((p.scheme,p.netloc,p.query,p.fragment)),'exact core read path required')
        canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside three read and bounded fixture routes');return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
_src=inspect.getsource(cap.gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-core-reads-storage-failure'),('conversation_boundaries_authorized','core_reads_storage_failure_authorized')):
    check(_src.count(old)==1,'released admission seam changed');_src=_src.replace(old,new)
_ns=dict(cap.gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_src),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
def doctor(root):
    data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift')
    info=ro.inspect_pg(data['postgres_container']);check(info['Id']==PG_CID and info['HostConfig'].get('NanoCpus')==4000000000 and info['HostConfig'].get('Memory')==2147483648,'exact PG4CPU2GiB required')
    data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());inc.frozen_identity(data,info['Id']);return data

def verify_error(value,headers):
    check(value==ERROR and next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','exact sanitized SELECT503 error required')
def verify_read(path,value,alert,app,source):
    check(isinstance(value,dict) and isinstance(value.get('items'),list) and len(value['items'])==1 and value.get('counts',{}).get('all')==1,'one real ingress item and exact all count required')
    item=value['items'][0]
    if path=='/inbox':
        check(item.get('alert_id')==alert and item.get('id')=='alert:'+alert and value.get('partial') is True and value.get('window')=='today_and_yesterday','HTTP alert Inbox binding differs')
    elif path=='/alert-groups':
        check(item.get('service')==app and item.get('source_id')==source and item.get('fingerprint')==source and item.get('count')==1 and value.get('partial') is True and value.get('aggregation')=='local_persisted_incidents','HTTP alert Group source binding differs')
    else:
        check(path=='/incidents' and item.get('id')==alert,'HTTP alert Incidents identity differs')

class Chain(inc.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root)
        self.acl_before=None;self.acl_fault=False
        self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0);ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime drift during read batch');super().frozen()
    def marker_case(self,name,actual):
        check('core_read.'+name in REQUIRED_CASES,'unknown core read marker');self.frozen();emit('core_read.'+name,'passed',dict(run_id=self.run_id,**actual))
    def start(self):
        phase=('setup','source')[self.phase];original=ro.emit
        def ready(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='core_read.api_ready.'+phase+'.'+name[-1]
            return original(name,status,actual,**fields)
        ro.emit=ready
        try:ro.Chain.start(self)
        finally:ro.emit=original
    def ledger(self):
        self.frozen();self.owner()
        def read(statement):return self.pg('BEGIN READ ONLY; '+statement+'; COMMIT;',self.database,'testlab')
        tables=json.loads(read("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'"))
        check(tables and len(tables)<=512 and {'sessions','management_audit',TABLE}.issubset(tables) and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'all-public bounded tables required')
        parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+ro.identifier(t)+' t' for t in tables]
        value=json.loads(read('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h'))
        check(set(value)==set(tables) and all(re.fullmatch('[a-f0-9]{64}',v) for v in value.values()),'complete row digests required');return value
    def acl(self):
        self.owner()
        query="SELECT json_build_object('database',current_database(),'table',c.relname,'owner',pg_get_userbyid(c.relowner),'select',has_table_privilege("+q(self.db_user)+",c.oid,'SELECT'),'role_oid',(SELECT oid::bigint FROM pg_roles WHERE rolname="+q(self.db_user)+"),'columns',(SELECT count(*) FROM pg_attribute WHERE attrelid=c.oid AND attacl IS NOT NULL),'entries',(SELECT COALESCE(jsonb_agg(jsonb_build_object('grantor',pg_get_userbyid(a.grantor),'grantee',a.grantee::bigint,'privilege',a.privilege_type,'grantable',a.is_grantable) ORDER BY a.grantor,a.grantee,a.privilege_type),'[]'::jsonb) FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) a)) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname="+q(TABLE)
        v=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,'testlab'))
        check(v.get('database')==self.database and v.get('table')==TABLE and v.get('owner')==self.db_user and v.get('columns')==0 and type(v.get('role_oid')) is int and isinstance(v.get('entries'),list),'exact own table ACL/owner required');return v
    def revoke(self):
        self.frozen();self.owner();before=self.acl()
        check(before['select'] is True and len([e for e in before['entries'] if e['privilege']=='SELECT'])==1 and all(e['grantor']==self.db_user and e['grantee']==before['role_oid'] and not e['grantable'] for e in before['entries']),'only original unambiguous owner SELECT without grantoption permitted')
        self.acl_before=before;self.acl_fault=True;ro._private(self.work/'acl-intent.json',before)
        self.pg('REVOKE SELECT ON TABLE public.'+ro.identifier(TABLE)+' FROM '+ro.identifier(self.db_user)+';',self.database,'testlab')
        after=self.acl();expected=dict(before,select=False,entries=[e for e in before['entries'] if e['privilege']!='SELECT'])
        check(after==expected,'only exact owner SELECT may change');ro._private(self.work/'acl-revoked.json',after)
    def restore(self):
        if not self.acl_fault:return
        current=self.acl();before=self.acl_before
        expected=dict(before,select=False,entries=[e for e in before['entries'] if e['privilege']!='SELECT'])
        if current!=before:
            check(current==expected,'foreign ACL drift; no restore mutation permitted')
            self.pg('GRANT SELECT ON TABLE public.'+ro.identifier(TABLE)+' TO '+ro.identifier(self.db_user)+';',self.database,'testlab')
        restored=self.acl();check(restored==before,'original effective ACL not restored exactly');self.acl_fault=False;ro._private(self.work/'acl-restored.json',restored)
    def call(self,user,method,path,*args,**kwargs):
        kwargs.setdefault('scenario','failure' if kwargs.get('want')==503 else 'boundary')
        original=ro.transport
        def wire(base,verb,target,payload=None,headers=None,**options):
            result=original(base,verb,target,payload,headers,**options)
            if (verb,target.removeprefix('/api/v1')) in TARGETS:
                status,raw,hs=result;check(len(raw)<=65536,'bounded synthetic read evidence required');value=json.loads(raw)
                if status==503:verify_error(value,hs)
                ro._private(self.work/('core-read-wire-'+str(self.calls)+'.json'),dict(method=verb,path=target,API_index=ro.BASES.index(base),status=status,response=value,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),headers={k:v for k,v in hs.items() if k.lower() in ('content-type','cache-control')},credentials_stored=False))
            return result
        ro.transport=wire
        try:return super().call(user,method,path,*args,**kwargs)
        finally:ro.transport=original
    def exercise(self,i,path,baseline):
        for phase,index in (('baseline',0),('repeat',1)):
            value,_=self.call('reader','GET','/api/v1'+path,index=index,want=200,validate=lambda v:verify_read(path,v,self.alert,self.app,self.source_id))
            check(self.ledger()==baseline,'read/repeat changed complete public ledger');self.marker_case(str(i)+'.'+phase,dict(HTTP200=True,API_index=index,all_public_unchanged=True))
        primary=None
        try:
            self.revoke();self.call('reader','GET','/api/v1'+path,index=0,want=503,code='events_unavailable')
            check(self.ledger()==baseline,'failed read changed complete public ledger');self.marker_case(str(i)+'.fault',dict(HTTP503=True,table=TABLE,scoped_own_role=True,all_public_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.restore()
            except BaseException as error:
                if primary is None:primary=error
                else:ro._private(self.work/'restore-secondary.json',dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
        if primary is not None:raise primary
        self.marker_case(str(i)+'.restore',dict(original_effective_ACL_restored=True,no_retry=True))
        self.call('reader','GET','/api/v1'+path,index=1,want=200,validate=lambda v:verify_read(path,v,self.alert,self.app,self.source_id))
        check(self.ledger()==baseline,'recovered read changed complete public ledger');self.marker_case(str(i)+'.recovery',dict(HTTP200=True,opposite_API=True,same_cookie=True,all_public_unchanged=True))
    def run(self):
        self.provision();self.start();self.seed();self.login('admin');self.login('reader');self.inputs()
        baseline=self.ledger();ro._private(self.work/'ledger-baseline.json',baseline)
        for i,(_,path) in enumerate(TARGETS):self.exercise(i,path,baseline)
        check(self.ledger()==baseline and not self.acl_fault and self.calls==16,'exact16 HTTP/final ACL/ledger boundary differs')
        ro._private(self.work/'ledger-final.json',baseline);self.marker_case('ledger',dict(all_public_tables=len(baseline),sessions_and_audit_unchanged=True,target_HTTP=12,fixture_HTTP=4))
        result=dict(run_id=self.run_id,http_calls=16,target_HTTP=12,normal_added=0,registration_evidence=False,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY)
        ro._private(self.work/'core-read-result.json',result);return result
    def cleanup(self):
        # A failed restore remains failed. Exact UUID DROP retires its ACL with
        # its table; do not add a second cleanup clock or retry a GRANT here.
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own read listener remains')
        emit('core_read.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,unrestored_fault_resource_retired=self.acl_fault))
def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
    if a.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=16,target_HTTP=12,fixture_HTTP=4,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
    if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
    try:
        check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
        with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
        return 0
    except Blocked:emit('core_read.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as e:emit('core_read.failure','failed',dict(error_type=type(e).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
