"""Two bounded cross-instance partial-update pairs, not load or a new normal route.
Private copies retain the passed UUID/Popen/PG lifecycle. No settings SQL writes,
faults, models, delivery, retries, fake preference GET or required schedule order.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,threading,time,textwrap,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='user-settings-concurrency'
PORTS=(18748,18749)
spec=importlib.util.spec_from_file_location('settings_concurrency_private_settings',ROOT/'tools/testlab/api_user_settings_audit_coverage.py')
settings=importlib.util.module_from_spec(spec);spec.loader.exec_module(settings);ro=settings.ro
check,Blocked=settings.check,settings.Blocked
SOURCE,BINARY,RUNTIME,PG_CID=settings.SOURCE_SHA,settings.BINARY_SHA,settings.RUNTIME_SHA,settings.PG_CID
REQUIRED_CASES=tuple('settings_concurrency.'+name for name in ('http_baseline','preferences_pair','notifications_pair','ledger','cleanup'))+('readonly.cleanup','readonly.complete')
_wire=threading.local();_original_transport=ro.transport;_output_lock=threading.Lock();_receipt_lock=threading.Lock()
def hashes():
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in
        ('api_user_settings_concurrency_coverage.py','tests/test_api_user_settings_concurrency_coverage.py','api_user_settings_audit_coverage.py',
         'api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py')}
def evidence(row):return dict(row,test_name='FormalCrossInstanceSettingsPartialUpdates',evidence_mode='real_local',upstream_mode='real_local',
    binary_sha256=BINARY,source_tree_sha=SOURCE,proof_scope='one bounded pair per existing preferences/notifications route; synthetic identity only; no new normal count, performance or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    with _output_lock:print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=ro.append_json
def append_json(path,row):
    with _receipt_lock:return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
settings.hashes=hashes
ro.SCENARIO_ALIASES=dict(ro.SCENARIO_ALIASES,concurrent_partial_update='idempotency')
# Reserve immutable call IDs before the barrier; never hold a lock across HTTP.
_source=textwrap.dedent(inspect.getsource(ro.Chain.call))
check(_source.count('self.calls += 1')==1 and _source.count('str(self.calls)')==3,'call receipt source drift; review required')
_source=_source.replace('self.calls += 1','call_number = self.next_call()').replace('str(self.calls)','str(call_number)')
_namespace=dict(ro.__dict__);exec(compile(_source,str(Path(__file__))+'[private call allocation]','exec'),_namespace);ro.Chain.call=_namespace['call']

def admission(path,run_id):
    path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonical UUID/private admission required')
    info=path.lstat();check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid(),'own private0600 admission required')
    permit=ro.private_json(path)
    expected=dict(status='root-admitted-user-settings-concurrency',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,
        binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'exact settings concurrency admission differs')
    for field in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified'):check(permit.get(field) is True,'missing root prerequisite '+field)
    connection=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(row[0]) for row in connection.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
    finally:connection.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and active[0].get('manifest_sha')==permit.get('manifest_sha256') and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256',''))
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit

def applied(kind,value,patch):
    value=copy.deepcopy(value)
    if kind=='preferences':value.update(patch)
    else:
        if 'quiet' in patch:value['quiet']['enabled']=patch['quiet']
        if 'digest' in patch:value['digest']=patch['digest']
    return value
def verify_pair(kind,before,after,actor,initial,patches,responses):
    check(kind in settings.ACTIONS and len(patches)==len(responses)==2 and not(set(patches[0])&set(patches[1])),'two disjoint patches required')
    for response in responses:(settings.verify_preferences if kind=='preferences' else settings.verify_notifications)(response)
    final=applied(kind,applied(kind,initial,patches[0]),patches[1])
    legal=[]
    for order in ((0,1),(1,0)):
        state=copy.deepcopy(initial);values=[None,None]
        for index in order:state=applied(kind,state,patches[index]);values[index]=copy.deepcopy(state)
        legal.append(values)
    check(responses in legal,'responses do not match either legal serialization; no preferred schedule')
    old={r['user_id']:r for r in before['settings']};new={r['user_id']:r for r in after['settings']}
    check(len(old)==len(before['settings']) and len(new)==len(after['settings']) and set(old)==set(new) and actor in old,'HTTP-established row set changed')
    check(all(new[k]==v for k,v in old.items() if k!=actor),'other owner changed')
    allowed={'updated_at'}|({'theme','density'} if kind=='preferences' else {'notification_settings'})
    check({k:v for k,v in old[actor].items() if k not in allowed}=={k:v for k,v in new[actor].items() if k not in allowed},'unsubmitted AI/profile/notify/preference/created_at changed')
    saved={k:new[actor][k] for k in settings.PREF_KEYS} if kind=='preferences' else new[actor]['notification_settings']
    check(saved==final,'concurrent partial update lost a field')
    timestamp=lambda value:datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
    check(timestamp(new[actor]['updated_at'])>timestamp(old[actor]['updated_at']),'pair timestamp did not advance baseline')
    check(len(after['audit'])==len(before['audit'])+2 and all(a in after['audit'] for a in before['audit']),'prior audits changed or pair audit count differs')
    added=[a for a in after['audit'] if a not in before['audit']]
    check(len(added)==2 and len({a['id'] for a in added})==2 and all(all(a.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id='',
        action=settings.ACTIONS[kind],target=actor,outcome='success').items()) for a in added),'audit actor/scope/action/target differs')
    changes=[{k:patch.get(k) for k in settings.PREF_KEYS} for patch in patches] if kind=='preferences' else [dict(quiet=r['quiet']['enabled'],digest=r['digest'],subscriptions=len(r['subscriptions'])) for r in responses]
    check(sorted(json.dumps(a['changes'],sort_keys=True) for a in added)==sorted(json.dumps(v,sort_keys=True) for v in changes),'exact pair audit changes differ')
    return final

class Pair:
    def __init__(self,kind,method,path,patches):
        self.kind,self.method,self.path,self.patches=kind,method,path,patches;self.barrier=threading.Barrier(3);self.lock=threading.Lock();self.wires=[None,None]
    def send(self,index,base,method,path,payload,headers,timeout):
        check((base,method,path)==(ro.BASES[index],self.method,self.path) and json.loads(payload)==self.patches[index],'exact independent API/patch differs')
        with self.lock:check(self.wires[index] is None,'pair cannot send a retry');self.wires[index]=dict(index=index,base=base,method=method,path=path,request_sha256=hashlib.sha256(payload).hexdigest())
        self.barrier.wait(timeout=10)
        record=self.wires[index];record['started_ns']=time.monotonic_ns()
        try:
            result=_original_transport(base,method,path,payload,headers,timeout);record.update(status=result[0],response_sha256=hashlib.sha256(result[1]).hexdigest());return result
        finally:record['finished_ns']=time.monotonic_ns()
    def verify_overlap(self):
        check(all(w and w.get('status')==200 and w.get('finished_ns',0)>w.get('started_ns',0)>0 for w in self.wires),'pair actual HTTP receipt incomplete')
        if max(w['started_ns'] for w in self.wires)>=min(w['finished_ns'] for w in self.wires):raise Blocked('no actual request interval overlap observed; no retry or concurrency claim')
def transport(base,method,path,payload=None,headers=None,timeout=12):
    if hasattr(_wire,'pair'):return _wire.pair.send(_wire.index,base,method,path,payload,headers,timeout)
    return _original_transport(base,method,path,payload,headers,timeout)
ro.transport=transport
ro.Chain.call.__globals__['transport']=transport

class Chain(settings.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.call_id_lock=threading.Lock();self.threads=[];self.pairs=[]
        self.state.update(suite=MODULE,scope='two independent binary partial-update pairs; no new normal route or load',counts_as_whole_acceptance=False)
        ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen()
    def next_call(self):
        with self.call_id_lock:self.calls+=1;return self.calls
    def marker_case(self,suffix,actual):
        name='settings_concurrency.'+suffix;check(name in REQUIRED_CASES,'unknown concurrency marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def concurrent_pair(self,kind,initial,patches):
        method,path=('PATCH','/api/v1/me/preferences') if kind=='preferences' else ('PUT','/api/v1/me/notification-settings')
        before=self.ledger();pair=Pair(kind,method,path,patches);self.pairs.append(pair);responses=[None,None];errors=[None,None];first_error=[]
        def worker(index):
            _wire.pair,_wire.index=pair,index
            try:responses[index]=self.call('reader',method,path,patches[index],index=index,scenario='concurrent_partial_update')[0]
            except BaseException as error:
                errors[index]=error
                with pair.lock:
                    if not first_error:first_error.append(error)
                pair.barrier.abort()
            finally:del _wire.pair;del _wire.index
        jobs=[threading.Thread(target=worker,args=(index,),name=MODULE+'-'+kind+'-'+str(index),daemon=False) for index in (0,1)]
        try:
            for thread in jobs:self.threads.append(thread);thread.start()
            try:pair.barrier.wait(timeout=10)
            except threading.BrokenBarrierError:pass
            deadline=time.monotonic()+25
            for thread in jobs:thread.join(max(0,deadline-time.monotonic()))
            check(not any(t.is_alive() for t in jobs),'bounded original pair workers incomplete')
        finally:
            pair.barrier.abort();ro._private(self.work/(kind+'-pair-wire.json'),dict(kind=kind,wires=pair.wires,worker_errors=[None if e is None else type(e).__name__ for e in errors],single_original_pair=True))
        if first_error:raise first_error[0]
        pair.verify_overlap();after=self.ledger();final=verify_pair(kind,before,after,self.users['reader'],initial,patches,responses)
        self.marker_case(kind+'_pair',dict(API_indexes=[0,1],actual_HTTP_intervals_overlap=True,patches=patches,final_merged=final,audit_delta=2,legal_order='either',attempt_pairs=1))
        return final
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('reader','peer'):self.login(actor)
        prefs=dict(theme='light',locale='en-US',density='comfortable',reduce_motion=True);notify={}
        for actor in ('reader','peer'):
            self.call(actor,'PATCH','/api/v1/me/preferences',prefs,scenario='fixture',validate=lambda v:check(v==prefs,'HTTP preference baseline differs'))
            current,_=self.call(actor,'GET','/api/v1/me/notification-settings',scenario='fixture',validate=settings.verify_notifications)
            check(current==dict(quiet={'enabled':False,'from':'22:00','to':'08:00'},digest='daily',subscriptions=[]),'fresh notification baseline differs')
            self.call(actor,'PUT','/api/v1/me/notification-settings',current,scenario='fixture',validate=lambda v:check(v==current,'HTTP notification baseline differs'));notify[actor]=current
        baseline=self.ledger();rows={r['user_id']:r for r in baseline['settings']}
        check(set(rows)=={self.users['reader'],self.users['peer']} and all({k:rows[self.users[a]][k] for k in settings.PREF_KEYS}==prefs
            and rows[self.users[a]]['notification_settings']==notify[a] for a in ('reader','peer')),'HTTP-established two-owner baseline differs')
        self.marker_case('http_baseline',dict(owners=2,settings_business_SQL_writes=0))
        preferences=self.concurrent_pair('preferences',prefs,[{'theme':'dark'},{'density':'compact'}])
        notifications=self.concurrent_pair('notifications',notify['reader'],[{'quiet':True},{'digest':'weekly'}])
        before_read=self.ledger()
        for index in (0,1):
            self.notifications_read('reader',notifications,index);self.notifications_read('peer',notify['peer'],index)
        check(before_read==self.ledger(),'GET caused business/audit side effect')
        final_rows={r['user_id']:r for r in before_read['settings']}
        check({k:final_rows[self.users['reader']][k] for k in settings.PREF_KEYS}==preferences,'notification pair erased concurrent preference result')
        self.marker_case('ledger',dict(pair_HTTP_mutations=4,successful_target_audits=4,notification_GET_two_APIs=True,other_owner_unchanged=True,
            whole_settings_row_checked=True,no_model_or_delivery=True,preference_final_read='durable DB plus either legal response serialization; no fabricated GET'))
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],
            http_calls=self.calls,pairs=2,mutations_per_pair=2,normal_count_increase=0,counts_as_whole_acceptance=False,
            unverified=['preference final GET /me requires authoritative oncall source','insert path concurrency','dynamic subscriptions','real identities/external delivery','performance/24h/full chain'])
        ro._private(self.work/'settings-concurrency-result.json',result);return result
    def cleanup(self):
        deadline=time.monotonic()+40;error=None
        try:
            for pair in self.pairs:pair.barrier.abort()
            # The original wire timeout is12s. This is part of the same40s,
            # leaving the held API/PG cleanup its remaining budget on failure.
            worker_deadline=min(deadline,time.monotonic()+15)
            for thread in self.threads:
                if thread.ident is not None:thread.join(max(0,worker_deadline-time.monotonic()))
            check(not any(t.is_alive() for t in self.threads),'owned worker still in flight')
        except BaseException as caught:error=caught
        finally:
            self.cleanup_deadline=deadline;bounded_cleanup(self)
        if error is not None:raise error
        emit('settings_concurrency.cleanup','passed',dict(single_cleanup_seconds=40,worker_threads_done=True,owned_UUID_only=True,ports_free=list(PORTS)))

_cleanup=textwrap.dedent(inspect.getsource(ro.Chain.cleanup));check(_cleanup.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_namespace=dict(ro.__dict__);exec(compile(_cleanup.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared worker/PG cleanup deadline]','exec'),_namespace)
bounded_cleanup=_namespace['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--live',action='store_true')
    parser.add_argument('--inventory',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:
        settings.Contract(ROOT);print(json.dumps(dict(status='prepared-only',targets=settings.TARGETS,ports=PORTS,required_cases=REQUIRED_CASES,pairs=2,http_calls=16,normal_count_increase=0)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'exact reviewed root required')
    fd=None
    try:
        admission(args.admission,args.run_id)
        fd=os.open(ROOT/'.local/testlab/user-settings-audit.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private settings lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():ro.execute(Chain(ROOT,settings.doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('settings_concurrency.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('settings_concurrency.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
