"""Independent actual upstream authorization failure/recovery; no new normal proof.

Reuse the passed discovery lifecycle/DTO/cleanup through a private module copy.
Only this run's recorded Binding UID is revoked/recreated; no workload writes,
clock/TTL/SQL source injection, model calls, or paused delivery components.
"""
from __future__ import annotations
import argparse, copy, datetime, fcntl, hashlib, importlib.util, json, os, stat, time
from pathlib import Path

ROOT=Path(__file__).resolve().parents[2]
_spec=importlib.util.spec_from_file_location('discovery_fault_private_passed',ROOT/'tools/testlab/api_service_discovery_real_source_coverage.py')
base=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(base)
ro=base.ro;check,Blocked,q=base.check,base.Blocked,base.q
MODULE='formal-api-service-discovery-fault-recovery';PORTS=(18776,18777)
REQUIRED_CASES=tuple('discovery_fault.'+x for x in ('auth_scope','real_lists','native_snapshot','baseline',
    'binding_revoked','error_anchor','failure_frozen','cached_readback','binding_recreated','recovered','ledger','cleanup'))
ERROR='Kubernetes inventory HTTP 403 for /api/v1/namespaces'
base.PORTS=ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);base.MODULE=MODULE

def hashes():
    names=('api_service_discovery_fault_recovery_coverage.py','tests/test_api_service_discovery_fault_recovery_coverage.py',
        'api_service_discovery_real_source_coverage.py','tests/test_api_service_discovery_real_source_coverage.py',
        'api_readonly_chain.py','runner.py','store.py','faults.py')
    return {name:base.digest(ROOT/'tools/testlab'/name) for name in names}
base.hashes=hashes

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if name=='discovery_real.cleanup':name='discovery_fault.cleanup'
    if 'interface' in fields:
        fields['interface']=dict(fields['interface'],evidence_mode='real_local',scenario='failure',
            test_name='FormalServiceDiscoverySourceFaultRecovery',upstream_mode='owned_real_kubernetes',
            proof_scope='independent binding403/cached snapshot/natural recovery; no new normal route or whole acceptance')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
base.emit=ro.emit=emit

def stamp(value):return datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
def fault_stable(value):
    result=copy.deepcopy(value)
    for key in ('last_attempt_at','updated_at','last_error'):result['source'].pop(key,None)
    return result

def assert_failure_frozen(anchor,current):
    check(anchor['source']['last_error']==current['source']['last_error']==ERROR,'not an actual Namespace LIST403')
    check(stamp(current['source']['last_attempt_at'])>stamp(anchor['source']['last_attempt_at']),
        'second independent natural failed attempt not observed')
    check(fault_stable(current)==fault_stable(anchor),'failed snapshot changed source success/scope or complete CMDB ledger')

def doctor(root,admission,run_id):
    # Same root wrapper, plus explicit authorization for this independent fault.
    permit=json.loads(base.private_regular(admission).read_text())
    check(permit.get('binding_fault_recovery_authorized') is True,'root binding fault admission required')
    return base.doctor(root,admission,run_id)

class Chain(base.Chain):
    def __init__(self,*args):
        super().__init__(*args);self.token_expiry=None;self.binding_absent=None;self.binding_history=[]

    def marker_case(self,name,actual):
        check('discovery_fault.'+name in REQUIRED_CASES,'unknown fault marker')
        self.frozen();emit('discovery_fault.'+name,'passed',dict(run_id=self.run_id,**actual))

    def kube(self,method,path,body=None,**kwargs):
        value=super().kube(method,path,body,**kwargs)
        if method=='POST' and path.endswith('/token'):
            self.token_expiry=stamp(value['status']['expirationTimestamp'])
        return value

    def token_budget(self,seconds):
        check(self.token_expiry is not None and (self.token_expiry-datetime.datetime.now(datetime.timezone.utc)).total_seconds()>seconds,
            'short issued token cannot cover bounded fault/recovery/cleanup; no TTL renewal')

    def call(self,*args,**kwargs):
        # Even baseline success is support for a fault case, never new normal proof.
        return super().call(*args,**dict(kwargs,scenario='failure'))

    def source_ledger(self,deadline):
        self.frozen()
        statement="""SELECT json_build_object(
 'source',(SELECT to_jsonb(s) FROM cmdb_sync_sources s WHERE id={id}),
 'resources',(SELECT COALESCE(json_agg(to_jsonb(r) ORDER BY r.id),'[]'::json) FROM cmdb_resources r WHERE r.source_id={id} AND r.source_kind='kubernetes'),
 'relations',(SELECT COALESCE(json_agg(to_jsonb(r) ORDER BY r.id),'[]'::json) FROM cmdb_resource_relations r WHERE r.source_id={id} AND r.source_kind='kubernetes'),
 'links',(SELECT COALESCE(json_agg(to_jsonb(l) ORDER BY l.resource_id,l.application_id,l.environment_id),'[]'::json) FROM cmdb_resource_applications l JOIN cmdb_resources r ON r.id=l.resource_id WHERE r.source_id={id} AND r.source_kind='kubernetes'))""".format(id=q(self.source_id))
        return json.loads(self.pg('BEGIN READ ONLY; '+statement+'; COMMIT;',self.database,self.db_user,deadline=deadline))

    def complete(self,value):
        source=value['source'];rows=value['resources']
        present=[r for r in rows if r['attributes']['inventory_presence']=='present']
        check(source and source['id']==self.source_id and source['kind']=='kubernetes' and source['environment_key']=='test'
            and source['resource_count']==len(present) and bool(present) and source['last_success_at'],'complete real source identity/count differs')
        workload_uids=set()
        for row in rows:
            check(row['source_id']==self.source_id and row['source_kind']=='kubernetes' and row['is_mock'] is False
                and row['attributes']['environment_key']=='test' and row['attributes']['inventory_presence'] in ('present','missing')
                and stamp(row['observed_at'])<=stamp(source['last_success_at']),'complete source row provenance/time differs')
            if row['attributes']['inventory_presence']=='missing':continue
            check(stamp(row['observed_at'])==stamp(source['last_success_at']),'present row observation is not the complete snapshot')
            if row['kind']=='deployment' and row['attributes']['resource_type'] in ('Deployment','StatefulSet','DaemonSet'):
                workload_uids.add(row['external_id'])
        check(workload_uids==set(self.expected) and value['links']==[],'real workload UID set/link scope changed')

    def wait_error(self,baseline,deadline):
        while True:
            value=self.source_ledger(deadline)
            source=value['source']
            if source and source['last_error']==ERROR and stamp(source['last_attempt_at'])>stamp(baseline['source']['last_attempt_at']):
                self.complete(value)
                check(stamp(source['last_success_at'])>=stamp(baseline['source']['last_success_at']),
                    'last complete success moved backwards')
                return value
            base.remaining(deadline);time.sleep(min(.2,base.remaining(deadline)))

    def wait_second_failure(self,anchor,deadline):
        while True:
            current=self.source_ledger(deadline)
            if stamp(current['source']['last_attempt_at'])>stamp(anchor['source']['last_attempt_at']):
                assert_failure_frozen(anchor,current);return current
            base.remaining(deadline);time.sleep(min(.2,base.remaining(deadline)))

    def wait_recovery(self,anchor,deadline):
        while True:
            current=self.source_ledger(deadline);source=current['source']
            if not source['last_error'] and stamp(source['last_success_at'])>stamp(anchor['source']['last_success_at']):
                self.complete(current);return current
            base.remaining(deadline);time.sleep(min(.2,base.remaining(deadline)))

    def journal(self):
        ro._private(self.work/'binding-incarnations.json',dict(active=self.auth,unknown=self.unknown,
            verified_absent=self.binding_absent,history=self.binding_history))

    def revoke_binding(self):
        records=[r for r in self.auth if r['spec']['kind']=='ClusterRoleBinding']
        check(len(records)==1 and self.binding_absent is None,'one live recorded binding required')
        record=copy.deepcopy(records[0]);deadline=time.monotonic()+4
        base.owned_auth(self.kube('GET',record['path'],admin=True,deadline=deadline),record['spec'],record['uid'])
        self.binding_history.append(dict(phase='delete_intent',record=record));self.journal()
        self.kube('DELETE',record['path'],dict(apiVersion='v1',kind='DeleteOptions',preconditions=dict(uid=record['uid']),
            propagationPolicy='Background'),want=200,admin=True,deadline=deadline)
        self.kube('GET',record['path'],want=404,admin=True,deadline=deadline)
        self.auth.remove(records[0]);self.binding_absent=record;self.journal()
        self.marker_case('binding_revoked',dict(old_uid=record['uid'],exact404=True,other_grants_unchanged=True))

    def recreate_binding(self):
        record=self.binding_absent;check(record is not None and len(self.auth)==2,'known absent original binding required')
        deadline=time.monotonic()+4
        self.kube('GET',record['path'],want=404,admin=True,deadline=deadline)
        self.unknown.append(record['path']);self.binding_history.append(dict(phase='create_intent',record=record));self.journal()
        collection=record['path'].rsplit('/',1)[0]
        value=self.kube('POST',collection,copy.deepcopy(record['spec']),want=201,admin=True,deadline=deadline)
        uid=base.owned_auth(value,record['spec'])
        check(uid!=record['uid'],'recreated binding did not get a new UID')
        self.auth.append(dict(path=record['path'],spec=record['spec'],uid=uid))
        self.unknown.remove(record['path']);self.binding_history.append(dict(phase='new_uid_recorded',old_uid=record['uid'],new_uid=uid))
        ro._private(self.work/'binding-recreate-response.json',value)
        self.journal()
        self.marker_case('binding_recreated',dict(old_uid=record['uid'],new_uid=uid,pristine_scope_unchanged=True))

    def pair(self,anchor,phase):
        def validate(value):
            base.validate_discovery(value,self.expected,self.source_id)
            for item in value['items']:
                observed=stamp(item['source']['observed_at']);success=stamp(item['source']['last_success_at'])
                check(observed==success and (success==stamp(anchor['source']['last_success_at']) if phase=='cached'
                    else success>=stamp(anchor['source']['last_success_at'])),
                    'formal API invented a source observation or moved success backwards')
        path='/api/v1/services/discover?cluster='+base.CLUSTER
        first,_=self.call('reader','GET',path,validate=validate)
        second,_=self.call('peer','GET',path,index=1,validate=validate)
        def stable(value):return [dict((k,v) for k,v in item.items() if k!='source') for item in value['items']]
        check(stable(first)==stable(second),'two APIs do not expose exact same stable complete DTO')
        if phase=='cached':check(first==second,'faulted source did not retain exact same anchored DTO')
        ro._private(self.work/(phase+'-api.json'),first)

    def run(self):
        self.prepare_auth();self.real_lists();self.provision();self.start();self.seed()
        for user in ('reader','peer'):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x)
                for x in (self.roles[user],'resources','view','environment','test'))+');')
        for user in ('reader','peer','denied'):self.login(user)
        self.native_snapshot();business=self.ledger();baseline=self.source_ledger(time.monotonic()+8);self.complete(baseline)
        check(business==dict(applications=0,links=0,audit=[],candidates=0,investigations=0,proposals=0,actions=0,executions=0),
            'fresh fault fixture created unrelated business results')
        check(not baseline['source']['last_error'],'initial real snapshot is already failing')
        self.pair(baseline,'baseline');self.marker_case('baseline',dict(normal_support_only=True,new_normal_routes=0))
        self.token_budget(130);self.revoke_binding();deadline=time.monotonic()+30
        anchor=self.wait_error(baseline,deadline);ro._private(self.work/'fault-anchor.json',anchor)
        self.marker_case('error_anchor',dict(http403=True,last_success_at=anchor['source']['last_success_at'],
            inflight_completion_allowed=stamp(anchor['source']['last_success_at'])>stamp(baseline['source']['last_success_at'])))
        second=self.wait_second_failure(anchor,deadline);ro._private(self.work/'second-failure.json',second)
        self.marker_case('failure_frozen',dict(natural_failed_attempts=2,source_and_full_cmdb_unchanged=True))
        self.pair(anchor,'cached')
        self.call('denied','GET','/api/v1/services/discover?cluster='+base.CLUSTER,
            validate=lambda v:base.validate_discovery(v,{},self.source_id,True))
        check(self.ledger()==business,'source fault/cached reads changed business/audit ledger')
        self.marker_case('cached_readback',dict(two_apis200=True,old_success_and_observation_unchanged=True,denied_empty200=True))
        self.token_budget(75);self.recreate_binding();recovered=self.wait_recovery(anchor,time.monotonic()+30)
        ro._private(self.work/'recovered-source.json',recovered)
        self.pair(recovered,'recovered');self.marker_case('recovered',dict(natural_complete_snapshot=True,error_cleared=True,
            last_success_at=recovered['source']['last_success_at']))
        check(self.ledger()==business,'natural recovery/read changed business/audit ledger')
        self.marker_case('ledger',dict(business_and_audit_unchanged=True,full_cmdb_failure_freeze=True))
        return dict(run_id=self.run_id,source_tree_sha=base.SOURCE,binary_sha256=base.BINARY,runtime_sha=base.RUNTIME,
            required_cases=REQUIRED_CASES,http_calls=self.calls,helper_sha256=self.data['helper_sha256'],
            verification_scope='independent actual source failure/recovery only',new_normal_routes=0,counts_as_whole_acceptance=False,
            unverified=['outage beyond5min freshness','UID churn/ambiguous workloads','WATCH loss/reconnect','production Kubernetes','full business chain/Argo/CI/VM/performance/24h'])

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT)
    parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path)
    args=parser.parse_args(argv)
    if not(args.live and args.run_id and args.admission):parser.error('root-reviewed --live --run-id --admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    # Same lease as passed group: never permit concurrent shared owned dependency use.
    fd=os.open(ROOT/'.local/testlab/service-discovery-real.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT,args.admission,args.run_id),args.run_id))
        return 0
    except Blocked:emit('discovery_fault.environment','blocked',dict(error='private source/admission/cleanup receipt retained'));return 2
    except Exception as error:emit('discovery_fault.failure','failed',dict(error_type=type(error).__name__));return 1
    finally:os.close(fd)

if __name__=='__main__':raise SystemExit(main())
