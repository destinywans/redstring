"""Re-run three original Go opt-in cases against the pinned, owned local lab.

Invoke through testlab.py with an explicit manifest and --live. All generated
credentials/evidence live under .local/testlab/current-local-smokes/. No product
.env, existing application DB, real organization account or shared config is
used. One synthetic Loki log is retained until the owned component retires;
Keycloak test sessions follow its existing expiry. Grafana remains unverified.
"""
import argparse, fcntl, hashlib, http.client, json, os, re, secrets, signal, socket, stat, subprocess, sys, tarfile, time, uuid
from pathlib import Path
from urllib.parse import urlencode
ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'tools/testlab'))
import api_readonly_chain as ro
import api_observe_coverage as observe
import api_ingress_coverage as ingress
import identity
from env import _private
from runner import command_env, source_fingerprint
from faults import Blocked, private_json
BASE=ROOT/'.local/testlab/current-local-smokes'
GO='59df22ad1d9c120f0fb24dfd3c4a3c8e0d3933a8b9cdfd6df5766721ce57537d'
BINARY='89f9348d26d38b79ec0a1cdc2aff2c59508fd6cb033c4f2caff9760e7f4e058b'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SOURCE_FILES=['tools/testlab/api_readonly_chain.py','tools/testlab/api_observe_coverage.py','tools/testlab/api_ingress_coverage.py','tools/testlab/identity.py','tools/testlab/env.py','tools/testlab/faults.py','tools/testlab/runner.py','tools/testlab/backend.py']
def digest(p):return hashlib.sha256(Path(p).read_bytes()).hexdigest()
def require(v,msg):
    if not v:raise Blocked(msg)
def emit(name,status,actual,**kw):
    print(json.dumps(dict(name=name,status=status,actual=actual,module='local-original-smokes',evidence_mode='real_local',type='integration',**kw)),flush=True)
def artifact(p):return dict(path=str(Path(p).relative_to(ROOT)),name=Path(p).name)
def wire(port,method,path,payload=None):
    require(port in (18573,18576) and path.startswith('/') and not path.startswith('//') and '\\' not in path,'unapproved original-smoke HTTP destination')
    if port==18573:require((method,path.split('?')[0]) in (('POST','/loki/api/v1/push'),('GET','/loki/api/v1/query_range')),'Loki operation outside smoke scope')
    else:require(method=='GET' and path=='/realms/aiops-testlab/.well-known/openid-configuration','Keycloak operation outside read-only discovery scope')
    c=http.client.HTTPConnection('127.0.0.1',port,timeout=8)
    try:
        c.request(method,path,json.dumps(payload).encode() if payload is not None else None,{'Content-Type':'application/json'} if payload is not None else {})
        r=c.getresponse();raw=r.read((2<<20)+1);require(len(raw)<=2<<20,'component response budget exceeded');return r.status,raw
    finally:c.close()
def free_callback():
    with socket.socket() as s:s.settimeout(.3);require(s.connect_ex(('127.0.0.1',18089))!=0,'original OIDC callback 18089 already owned elsewhere')
def snapshot(data,pinned=None):
    require(source_fingerprint(ROOT)==GO==data['source_tree_sha'],'approved current Go source changed')
    require(digest(data['binary'])==BINARY==data['binary_sha256'],'approved binary changed')
    require(data['runtime_sha']==RUNTIME==digest(ROOT/'.local/testlab/runtime-config.json'),'approved runtime changed')
    pg=ro.guard_pg(ro.inspect_pg(data['postgres_container']),ro.OWNER,data['postgres_image_id']);require(pg['Id']==PG,'immutable owned PG CID changed')
    cfg=private_json(ROOT/'.local/testlab/component-environment.json');kcname=identity.validate_component(ROOT,data,cfg)
    targets={}
    for provider in ('prometheus','loki','tempo','keycloak'):
        info=ro.inspect_pg('aiops-'+ro.OWNER+'-'+provider)
        if provider=='prometheus':checked=ingress.component_guard(provider,info)
        elif provider in ('loki','tempo'):checked=observe.guard_component(provider,info)
        else:
            identity.guard_component_port(info,18576)
            require(info['Name']=='/'+kcname and info['Config']['Labels'].get('aiops.testlab.owner')==ro.OWNER and info['State']['Running'],'owned Keycloak identity mismatch')
            checked=dict(container_id=info['Id'],image_id=info['Image'],port=18576)
        mounts=[]
        for mount in info.get('Mounts',[]):
            if mount.get('Type')=='bind':
                source=Path(mount['Source']);require(source.is_file() and not source.is_symlink() and mount.get('RW') is False,'component bind config is not regular readonly source')
                mounts.append(dict(source=str(source),destination=mount['Destination'],sha256=digest(source)))
        targets[provider]=dict(identity=checked,started_at=info['State']['StartedAt'],path=info['Path'],args=info['Args'],binds=mounts)
    helpers={p:digest(ROOT/p) for p in SOURCE_FILES};helpers['entrypoint']=digest(__file__)
    value=dict(source_tree_sha=GO,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=pg['Id'],postgres_image_id=pg['Image'],components=targets,helpers=helpers)
    if pinned is not None:require(value==pinned,'source/component/config/helper drift during original smoke run')
    return value,cfg
class Scope(ro.Chain):
    def __init__(self,data,run_id,work):
        require(str(uuid.UUID(run_id))==run_id and data.get('owner')==ro.OWNER,'canonical UUID and owned lab required')
        require(work.parent==BASE and work.name==run_id and work.resolve()==work,'UUID evidence directory mismatch')
        self.root,self.data,self.run_id,self.work=ROOT,data,run_id,work
        self.nonce=uuid.UUID(run_id).hex;self.database='aiops_testlab_readonly_'+self.nonce;self.db_user='readonly_'+self.nonce
        self.owner_tag=ro.OWNER+':'+run_id+':api-readonly';self.db_password=secrets.token_urlsafe(32)
        self.processes=[];self.role_attempted=False;self.db_attempted=False
        self.state=dict(owner=ro.OWNER,run_id=run_id,database=self.database,database_user=self.db_user,owner_tag=self.owner_tag)
        _private(work/'database-state.json',self.state);_private(work/'database-credentials.json',dict(password=self.db_password))
    def pg(self,*args,**kwargs):
        require(ro.inspect_pg(self.data['postgres_container'])['Id']==PG,'owned PG CID changed before scoped SQL')
        return super().pg(*args,**kwargs)
    def provision(self):
        self.frozen();q=ro.quote;i=ro.identifier
        before=json.loads(self.pg("SELECT json_build_object('role',EXISTS(SELECT 1 FROM pg_roles WHERE rolname="+q(self.db_user)+"),'database',EXISTS(SELECT 1 FROM pg_database WHERE datname="+q(self.database)+"));"))
        require(before=={'role':False,'database':False},'fresh smoke UUID resources already exist')
        self.role_attempted=True;self.pg('BEGIN; CREATE ROLE '+i(self.db_user)+' LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '+q(self.db_password)+'; COMMENT ON ROLE '+i(self.db_user)+' IS '+q(self.owner_tag)+'; COMMIT;')
        self.db_attempted=True;self.pg('CREATE DATABASE '+i(self.database)+' OWNER '+i(self.db_user)+' TEMPLATE template0;')
        self.pg('COMMENT ON DATABASE '+i(self.database)+' IS '+q(self.owner_tag)+'; REVOKE CONNECT,TEMPORARY ON DATABASE '+i(self.database)+' FROM PUBLIC;')
        self.pg('CREATE EXTENSION vector WITH SCHEMA public; CREATE EXTENSION pgcrypto WITH SCHEMA public;',self.database)
        _private(self.work/'database-admission.json',dict(fresh=before,**self.state))
def exact_loki_labels(stream,expected):
    # Loki 3.7 derives service_name from application when no service label was supplied.
    return stream==expected or stream==dict(expected,service_name=expected['application'])
def seed(work,run_id):
    labels={'application':'testlab','environment':'test','testlab_owner':ro.OWNER,'testlab_smoke_run':run_id}
    line=json.dumps({'level':'error','message_template':'controlled current original smoke log','message':'original-smoke-'+run_id},sort_keys=True)
    ts=str(time.time_ns());payload={'streams':[{'stream':labels,'values':[[ts,line]]}]}
    _private(work/'loki-write-intent.json',dict(payload=payload,write_attempts=1,history_proof=False))
    status,_=wire(18573,'POST','/loki/api/v1/push',payload);_private(work/'loki-write-receipt.json',dict(status=status,retried=False));require(status==204,'single owned Loki append refused; no write retry')
    query='{'+','.join(k+'='+json.dumps(v) for k,v in sorted(labels.items()))+'}'
    deadline=time.monotonic()+45;attempt=0
    while True:
        attempt+=1;status,raw=wire(18573,'GET','/loki/api/v1/query_range?'+urlencode(dict(query=query,start=int(ts)-1000000000,end=time.time_ns(),limit=10)))
        _private(work/('loki-readback-'+str(attempt)+'.json'),dict(status=status,body=json.loads(raw)))
        require(status==200,'Loki readback failed')
        data=json.loads(raw);records=[(s['stream'],v) for s in data.get('data',{}).get('result',[]) for v in s.get('values',[])]
        if records:
            require(len(records)==1 and exact_loki_labels(records[0][0],labels) and records[0][1]==[ts,line],
                    'observed UUID Loki record labels/timestamp/body mismatch; no delayed polling or write retry')
            break
        require(time.monotonic()<deadline,'UUID log exact readback exceeded 45s');time.sleep(1)
    require(len(records)==1,'fresh UUID Loki stream is not uniquely scoped')
    _private(work/'loki-synthetic-input.json',dict(payload=payload,synthetic_history=False,retention='preserved until owned Loki component retirement; no shared log deletion',normal_window_collection_proof=False))
    config=work/'telemetry-config.json'
    _private(config,{'loki':{'url':'http://127.0.0.1:18573','allow_http':True},'applications':{'lab-order-api':{'loki_labels':labels,'loki_pattern_field':'message_template','loki_complete_lookback_seconds':86400}},'timeout_seconds':10,'max_window_seconds':3600,'max_lookback_seconds':86400,'max_log_entries':100})
    emit('original_smokes.nonempty_loki_fixture','passed',dict(single_append=True,exact_readback=True,labels=labels,original_case_asserts_nonempty=False,history_proof=False),artifacts=[artifact(work/'loki-synthetic-input.json'),artifact(work/('loki-readback-'+str(attempt)+'.json'))])
    return config
def read_container_realm(container_id,raw,work):
    # Read fixed container bytes via Docker's archive API; never extract paths.
    require(isinstance(container_id,str) and re.fullmatch(r'[a-f0-9]{64}',container_id),'exact owned Keycloak CID required for archive read')
    archive=work/'realm-container.tar';stderr=work/'realm-container-cp.stderr'
    with open(archive,'xb') as stream,open(stderr,'xb') as errors:
        os.chmod(archive,0o600);os.chmod(stderr,0o600)
        result=subprocess.run(['docker','cp',container_id+':/opt/keycloak/data/import/realm.json','-'],stdout=stream,stderr=errors,timeout=10)
    require(result.returncode==0 and archive.stat().st_size<=65536,'bounded actual owned Keycloak import archive unavailable')
    with tarfile.open(archive) as stream:
        members=stream.getmembers();require(len(members)==1 and members[0].name=='realm.json' and members[0].isfile() and members[0].size==len(raw),'actual realm archive member/size/type mismatch')
        actual=stream.extractfile(members[0]).read(len(raw)+1)
    require(actual==raw,'actual owned Keycloak import bytes differ from private fixture')
    return hashlib.sha256(actual).hexdigest()
def validate_realm(data,cfg,work):
    folder=Path(cfg['work']);path=folder/'realm.json';require(path.is_file() and not path.is_symlink() and stat.S_IMODE(path.stat().st_mode)==0o600,'private generated realm source required')
    raw=path.read_bytes();realm=json.loads(raw);e=cfg.get('env',{});issuer='http://127.0.0.1:18576/realms/aiops-testlab'
    require(e.get('AIOPS_OIDC_TEST_ISSUER')==issuer and e.get('AIOPS_OIDC_TEST_USERNAME')=='testlab-user' and e.get('AIOPS_OIDC_TEST_PASSWORD'),'explicit dedicated synthetic realm environment mismatch')
    clients=realm.get('clients',[]);users=realm.get('users',[])
    require(realm.get('realm')=='aiops-testlab' and len(clients)==1 and len(users)==1,'realm/client/account is outside original smoke fixture scope')
    client=clients[0];user=users[0]
    require(client.get('clientId')=='aiops-oidc-smoke' and client.get('publicClient') is True and client.get('directAccessGrantsEnabled') is True and client.get('standardFlowEnabled') is True and client.get('redirectUris')==['http://127.0.0.1:18089/api/v1/auth/oidc/callback'],'fixed original smoke OIDC client configuration changed')
    require(user.get('username')=='testlab-user' and user.get('email')=='testlab@example.invalid' and user.get('enabled') is True and user.get('credentials')==[{'type':'password','value':e['AIOPS_OIDC_TEST_PASSWORD'],'temporary':False}],'only generated synthetic account credentials allowed')
    info=ro.inspect_pg('aiops-'+ro.OWNER+'-keycloak');mounts=[m for m in info.get('Mounts',[]) if m.get('Source')==str(path) and m.get('Destination')=='/opt/keycloak/data/import/realm.json' and m.get('RW') is False]
    require(len(mounts)==1,'realm source is not the owned readonly import mount')
    read_container_realm(info['Id'],raw,work)
    status,body=wire(18576,'GET','/realms/aiops-testlab/.well-known/openid-configuration');doc=json.loads(body)
    require(status==200 and doc.get('issuer')==issuer and doc.get('authorization_endpoint')==issuer+'/protocol/openid-connect/auth' and doc.get('token_endpoint')==issuer+'/protocol/openid-connect/token' and doc.get('jwks_uri')==issuer+'/protocol/openid-connect/certs','actual discovery endpoints are not the fixed owned realm')
    _private(work/'keycloak-discovery.json',doc)
    _private(work/'realm-scope.json',dict(realm_sha256=hashlib.sha256(raw).hexdigest(),actual_container_bytes_matched=True,issuer=issuer,user='testlab-user',client='aiops-oidc-smoke',redirect_uri='http://127.0.0.1:18089/api/v1/auth/oidc/callback',fixture_only=True,realm_config_mutated=False))
    return {k:e[k] for k in ('AIOPS_OIDC_TEST_ISSUER','AIOPS_OIDC_TEST_USERNAME','AIOPS_OIDC_TEST_PASSWORD')}
def go_case(scope,work,package,name,explicit):
    environment=command_env(explicit,{});environment['GOCACHE']=str(ROOT/'.local/go-cache')
    _private(work/(name+'-explicit-env.json'),explicit)
    argv=['go','test','-mod=readonly','-p','1','-race','-json','-count=1','-run','^'+name+'$','-timeout','120s',package]
    _private(work/(name+'-command.json'),dict(argv=argv,environment_keys=sorted(explicit),scope='original case only; not entire module'))
    started=time.time();out=work/(name+'.go.jsonl');err=work/(name+'.stderr')
    with open(out,'xb') as stdout,open(err,'xb') as stderr:
        os.chmod(out,0o600);os.chmod(err,0o600)
        with ro.acquisition_signals():
            proc=subprocess.Popen(argv,cwd=ROOT,env=environment,stdout=stdout,stderr=stderr);scope.processes.append(proc)
        try:proc.wait(timeout=180)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:proc.wait(timeout=8)
            except subprocess.TimeoutExpired:proc.kill();proc.wait(timeout=3)
            raise Blocked(name+' Go subprocess deadline; original raw retained')
    frames=[json.loads(line) for line in out.read_text().splitlines()]
    terminals=[f for f in frames if f.get('Test') and f.get('Action') in ('pass','fail','skip')]
    exact=[f for f in terminals if f['Test']==name]
    for frame in terminals:
        emit(frame['Test'],{'pass':'passed','fail':'failed','skip':'skipped'}[frame['Action']],dict(package=package,original_test=True,action=frame['Action'],elapsed=frame.get('Elapsed'),go_race=True,exit_code=proc.returncode,input_keys=sorted(explicit)),artifacts=[artifact(out),artifact(err)])
    require(len(exact)==1 and exact[0]['Action']=='pass' and proc.returncode==0 and all(f['Action']=='pass' for f in terminals),name+' failed/skipped or missing original terminal result; raw retained')
    return dict(case=name,package=package,exit_code=proc.returncode,terminal_tests=[f['Test'] for f in terminals],elapsed_seconds=time.time()-started,raw=str(out.relative_to(ROOT)))
def main():
    BASE.mkdir(parents=True,mode=0o700,exist_ok=True)
    require(BASE.resolve()==BASE,'original smoke evidence root traverses symlink')
    scope=None;failed=False;work=BASE/str(uuid.uuid4());work.mkdir(mode=0o700)
    fd=os.open(BASE/'smokes.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    try:
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        old=ro.PORTS;ro.PORTS=(18089,)
        try:data=ro.doctor(ROOT)
        finally:ro.PORTS=old
        free_callback();pinned,cfg=snapshot(data);_private(work/'admission.json',pinned)
        emit('original_smokes.fresh_admission','passed',dict(work=str(work.relative_to(ROOT)),source_tree_sha=GO,pg_cid=PG,callback_port_free=True,components=list(pinned['components'])),artifacts=[artifact(work/'admission.json')])
        scope=Scope(data,work.name,work);scope.provision();kcenv=validate_realm(data,cfg,work)
        config=seed(work,work.name);results=[]
        snapshot(data,pinned)
        results.append(go_case(scope,work,'./internal/connectors','TestLocalReadcheck',{'AIOPS_TESTLAB_TELEMETRY_CONFIG':str(config)}))
        snapshot(data,pinned);free_callback()
        dsn='postgres://'+scope.db_user+':'+scope.db_password+'@127.0.0.1:15461/'+scope.database+'?'+urlencode(dict(sslmode='disable',options='-csearch_path=public',application_name=scope.pg_application('api',0)))
        results.append(go_case(scope,work,'./internal/v3identity','TestOIDCKeycloakLocalSmoke',dict(kcenv,TEST_DATABASE_URL=dsn)))
        schemas=json.loads(scope.sql("SELECT COALESCE(json_agg(nspname ORDER BY nspname),'[]'::json) FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname<>'information_schema'",read=True))
        require(schemas==['public'],'original OIDC cleanup left a schema in its UUID database')
        _private(work/'oidc-schema-residual.json',dict(schemas=schemas,expected=['public'],audit_assertion='original case requires account.oidc_login count=1 before dropping private schema'))
        free_callback();snapshot(data,pinned)
        results.append(go_case(scope,work,'./internal/v3integrations','TestRealLocalConnectorProbes',{'AIOPS_TEST_PROMETHEUS_URL':'http://127.0.0.1:18570','AIOPS_TEST_LOKI_URL':'http://127.0.0.1:18573','AIOPS_TEST_TEMPO_URL':'http://127.0.0.1:18574'}))
        after,_=snapshot(data,pinned);_private(work/'source-end.json',after)
        result=dict(results=results,source_tree_sha_start=GO,source_tree_sha_end=GO,scope='three exact original Go opt-in cases only; no formal main HTTP increment',unverified=['Grafana (not configured/no new resource)','whole modules/full regression','real organization users','30-minute continuous log collection','performance/24-hour continuity'],log_retention='one UUID controlled Loki log retained until owned component retirement')
        _private(work/'result.json',result);emit('original_smokes.complete_scope','passed',result,artifacts=[artifact(work/'result.json'),artifact(work/'source-end.json'),artifact(work/'oidc-schema-residual.json')])
    except BaseException as error:
        failed=True;_private(work/'first-failure.json',dict(type=type(error).__name__,message=str(error)))
        emit('original_smokes.first_failure','blocked' if isinstance(error,Blocked) else 'failed',dict(error=type(error).__name__,message=str(error),work=str(work.relative_to(ROOT))),artifacts=[artifact(work/'first-failure.json')])
    finally:
        if scope:
            try:scope.cleanup();free_callback()
            except BaseException as error:
                failed=True;_private(work/'cleanup-failure.json',dict(type=type(error).__name__,message=str(error)));emit('original_smokes.cleanup','failed',dict(error=type(error).__name__,message=str(error)),artifacts=[artifact(work/'cleanup-failure.json')])
        _private(BASE/'latest-invocation.json',dict(work=str(work),failed=failed,scope='no shared config/API mutation or shared resource cleanup'))
        os.close(fd)
    return int(failed)
if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--live',action='store_true')
    if not parser.parse_args().live:parser.error('explicit --live required for owned component/database access')
    signal.signal(signal.SIGTERM,lambda *_:(_ for _ in ()).throw(KeyboardInterrupt('original smoke invocation interrupted')))
    raise SystemExit(main())
