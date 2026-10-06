"""Four owned settings GET reads, SELECT503, exact ACL restore and no-write.
19 HTTP: one real login, two formal settings PUTs, four GETs in four phases.
Empty watched services is explicit; no SQL business seed, inference or notification.
"""
from __future__ import annotations
import argparse, hashlib, importlib.util, inspect, io, json, socket, textwrap, tokenize
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-settings-reads-storage-failure';PORTS=(18932,18933)
_spec=importlib.util.spec_from_file_location('settings_read_private_core',ROOT/'tools/testlab/api_core_reads_storage_failure.py')
core=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(core)
ro,inc=core.ro,core.inc
check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=core.SOURCE,core.BINARY,core.RUNTIME,core.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=tuple(('GET',p) for p in ('/me/ai-preferences','/me/notification-settings','/me/watched-services','/models'))
SUPPORT=(('POST','/auth/login'),('PUT','/me/ai-preferences'),('PUT','/me/notification-settings'))
TABLES=('v3_user_settings','v3_user_watched_services')
PREFERENCES=dict(default=dict(model='openai:readonly-synthetic'),canvas_inherit=True,show_reasoning='hidden',auto_context=False,memory=False,budget_warn_tokens=12345)
NOTIFICATIONS=dict(quiet=dict(enabled=True,**{'from':'22:00','to':'08:00'}),digest='daily',subscriptions=[])
ERRORS={'settings':dict(error=dict(code='settings_unavailable',message='Settings storage unavailable')),
        'watched':dict(error=dict(code='watched_services_unavailable',message='Watched services or policy are unavailable'))}
REQUIRED_CASES=tuple('settings_read.'+n for n in ('http_inputs',*[str(i)+'.'+phase for phase in ('baseline','repeat','fault','recovery') for i in range(4)],'fault_installed','restore','ledger','cleanup'))+tuple('settings_read.api_ready.'+str(i) for i in (0,1))+('readonly.fresh_database','readonly.synthetic_identities','readonly.cleanup','readonly.complete')
def hashes():
    return {**core.hashes(),**{n:core.digest(ROOT/'tools/testlab'/n) for n in ('api_settings_reads_storage_failure.py','tests/test_api_settings_reads_storage_failure.py')}}
inc.hashes=hashes
def evidence(row):
    return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalSettingsReadsStorageFailure',proof_scope='four GET reads/repeat/own SELECT503/recovery; empty watched; all-public unchanged; no model probe, notification, normal gain or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
def append_json(path,row):return core._append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(core.Contract.__bases__[0]):
    def canonical(self,method,path):
        p=urlsplit(path);check(not any((p.scheme,p.netloc,p.query,p.fragment)),'exact settings read path required')
        canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside four reads and two formal PUT fixtures');return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
_source=inspect.getsource(core.cap.gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-settings-reads-storage-failure'),('conversation_boundaries_authorized','settings_reads_storage_failure_authorized')):
    check(_source.count(old)==1,'released admission seam changed');_source=_source.replace(old,new)
_ns=dict(core.cap.gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_source),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission']
core.admission=admission
def doctor(root):
    data=core.doctor(root);return dict(data,helper_sha256=hashes())

# Preserve the actually passed bigint OID/effective ACL algorithm. Only the
# selected table and evidence filename differ; tables are restricted below.
_acl_ns=dict(core.__dict__)
for _name in ('acl','revoke','restore'):
    _body=textwrap.dedent(inspect.getsource(getattr(core.Chain,_name)))
    for _phase in ('intent','revoked','restored'):
        _body=_body.replace("self.work/'acl-"+_phase+".json'","self.work/('acl-"+_phase+"-'+self.active_table+'.json')")
    _body=tokenize.untokenize((t.type,'self.active_table' if t.type==tokenize.NAME and t.string=='TABLE' else t.string)
                             for t in tokenize.generate_tokens(io.StringIO(_body).readline))
    exec(compile(_body,str(__file__)+'[passed '+_name+']','exec'),_acl_ns)
def error_for(path):return ERRORS['watched' if path=='/me/watched-services' else 'settings']
def verify_error(path,value,headers):
    check(value==error_for(path) and next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','exact sanitized storage503 required')
def verify_read(path,value):
    if path=='/me/ai-preferences':check(value==PREFERENCES,'HTTP-established own preferences differ')
    elif path=='/me/notification-settings':check(value==NOTIFICATIONS,'HTTP-established own notification settings differ')
    elif path=='/me/watched-services':check(value==dict(items=[],complete_v3_dto=True),'fresh empty watched contract differs')
    else:
        check(path=='/models' and isinstance(value,dict) and set(value)=={'items','default'} and value['default']==PREFERENCES['default'] and isinstance(value['items'],list) and len(value['items'])==1,'configured model catalog/default differs')
        item=value['items'][0];check(item.get('id')=='openai:readonly-synthetic' and item.get('api')=='chat_completions' and item.get('available') is True,'configured adapter identity/protocol differs')
        check(item.get('reasoning')==dict(kind='none',levels=[]),'unexpected model reasoning binding')

class Chain(core.Chain):
    acl=_acl_ns['acl'];revoke_table=_acl_ns['revoke'];restore_table=_acl_ns['restore']
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id,permit);self.contract=Contract(root)
        self.active_table=None;self.acl_states={};self.expected_reads={}
        self.state.update(suite=MODULE,ports=list(PORTS),targets=TARGETS,SQL_business_writes=0,model_scope='configured local catalog only; no inference/probe',watched_scope='fresh empty list',no_model_or_external_actions=True)
        ro._private(self.work/'state.json',self.state)
    def marker_case(self,name,actual):
        check('settings_read.'+name in REQUIRED_CASES,'unknown settings read marker');self.frozen();emit('settings_read.'+name,'passed',dict(run_id=self.run_id,**actual))
    def start(self):
        original=ro.emit
        def ready(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='settings_read.api_ready.'+name[-1]
            return original(name,status,actual,**fields)
        ro.emit=ready
        try:ro.Chain.start(self)
        finally:ro.emit=original
    def seed(self):ro.Chain.seed(self)
    def ledger(self):
        value=super().ledger();check(set(TABLES).issubset(value),'both settings tables required in complete public ledger');return value
    def table_operation(self,table,operation):
        check(table in TABLES and operation in ('revoke','restore'),'only two owned settings table faults permitted')
        self.active_table=table;self.acl_before,self.acl_fault=self.acl_states.get(table,(None,False))
        try:return (self.revoke_table if operation=='revoke' else self.restore_table)()
        finally:
            self.acl_states[table]=(self.acl_before,self.acl_fault)
            self.active_table=None;self.acl_fault=any(state[1] for state in self.acl_states.values())
    def restore_all(self):
        primary=None
        for table in reversed(TABLES):
            if not self.acl_states.get(table,(None,False))[1]:continue
            try:self.table_operation(table,'restore')
            except BaseException as error:
                if primary is None:primary=error
                else:ro._private(self.work/('restore-secondary-'+table+'.json'),dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
        if primary is not None:raise primary
    def call(self,user,method,path,*args,**kwargs):
        self.frozen();kwargs.setdefault('scenario','failure' if kwargs.get('want')==503 else 'boundary');original=ro.transport
        def wire(base,verb,target,payload=None,headers=None,**options):
            result=original(base,verb,target,payload,headers,**options)
            relative=target.removeprefix('/api/v1')
            if (verb,relative) in TARGETS:
                status,raw,hs=result;check(len(raw)<=65536,'bounded synthetic settings response required');value=json.loads(raw)
                if status==503:verify_error(relative,value,hs)
                ro._private(self.work/('settings-read-wire-'+str(self.calls)+'.json'),dict(method=verb,path=target,API_index=ro.BASES.index(base),status=status,response=value,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),headers={k:v for k,v in hs.items() if k.lower() in ('content-type','cache-control')},credentials_stored=False))
            return result
        ro.transport=wire
        try:return ro.Chain.call(self,user,method,path,*args,**kwargs)
        finally:ro.transport=original
    def phase_reads(self,phase,index,baseline):
        for i,(_,path) in enumerate(TARGETS):
            failed=phase=='fault'
            value,_=self.call('reader','GET','/api/v1'+path,index=index,want=503 if failed else 200,**({'code':error_for(path)['error']['code']} if failed else {'validate':lambda v,p=path:verify_read(p,v)}))
            if not failed:
                if phase=='baseline':self.expected_reads[path]=value
                else:check(value==self.expected_reads[path],'same persisted state/catalog cross-instance read differs')
            check(self.ledger()==baseline,'settings read changed complete public ledger including sessions/audit')
            self.marker_case(str(i)+'.'+phase,dict(status=503 if failed else 200,API_index=index,all_public_unchanged=True,own_table=TABLES[1] if path=='/me/watched-services' else TABLES[0]))
    def exercise(self,baseline):
        self.phase_reads('baseline',0,baseline);self.phase_reads('repeat',1,baseline);primary=None
        try:
            for table in TABLES:self.table_operation(table,'revoke')
            self.marker_case('fault_installed',dict(tables=TABLES,effective_SELECT=False,session_permissions_unchanged=True))
            self.phase_reads('fault',0,baseline)
        except BaseException as error:primary=error
        finally:
            try:self.restore_all()
            except BaseException as error:
                if primary is None:primary=error
                else:ro._private(self.work/'restore-secondary.json',dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
        if primary is not None:raise primary
        check(not self.acl_fault,'settings ACL remains revoked');self.marker_case('restore',dict(original_effective_ACLs_restored=True,no_retry=True))
        self.phase_reads('recovery',1,baseline)
    def run(self):
        self.provision();self.start();self.seed();self.login('reader')
        for path,payload in (('/me/ai-preferences',PREFERENCES),('/me/notification-settings',NOTIFICATIONS)):
            self.call('reader','PUT','/api/v1'+path,payload,scenario='fixture',validate=lambda v,p=payload:check(v==p,'formal PUT fixture response differs'))
        stored=json.loads(self.sql('SELECT json_build_object(\'ai\',ai_preferences,\'notification\',notification_settings) FROM v3_user_settings WHERE user_id='+q(self.users['reader']),read=True))
        check(stored==dict(ai=PREFERENCES,notification=NOTIFICATIONS),'formal HTTP settings fixture not persisted')
        check(self.sql('SELECT count(*) FROM v3_user_watched_services',read=True)=='0','fresh empty watched fixture required')
        self.marker_case('http_inputs',dict(real_login=1,formal_PUT=2,SQL_business_writes=0,watched_empty=True,inference_or_probe_requested=False,notification_delivery_requested=False))
        baseline=self.ledger();ro._private(self.work/'ledger-baseline.json',baseline);self.exercise(baseline)
        check(self.ledger()==baseline and not self.acl_fault and self.calls==19,'exact19 HTTP/final ACL/ledger boundary differs')
        ro._private(self.work/'ledger-final.json',baseline);self.marker_case('ledger',dict(all_public_tables=len(baseline),sessions_and_audit_unchanged=True,target_HTTP=16,fixture_HTTP=3))
        result=dict(run_id=self.run_id,http_calls=19,target_HTTP=16,normal_added=0,registration_evidence=False,required_cases=REQUIRED_CASES,counts_as_whole_acceptance=False,source_tree_sha=SOURCE,binary_sha256=BINARY)
        ro._private(self.work/'settings-read-result.json',result);return result
    def cleanup(self):
        # Failed restore stays red. UUID DROP retires own tables/ACL, no retry.
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own settings listener remains')
        emit('settings_read.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,unrestored_fault_resource_retired=self.acl_fault))
def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
    if a.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=19,target_HTTP=16,fixture_HTTP=3,normal_added=0,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
    if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
    try:
        check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
        with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
        return 0
    except Blocked:emit('settings_read.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as error:emit('settings_read.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
