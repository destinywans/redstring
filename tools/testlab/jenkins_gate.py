"""Original Go Jenkins live gate against a fresh, single-job local controller.

No existing Jenkins, plugins, SCM, remote agents, Docker socket or deployments.
The original gate is anonymous READ; authenticated mode is not claimed by it.
"""
from __future__ import annotations
import argparse, base64, contextlib, fcntl, hashlib, json, os, re, secrets, signal, socket, subprocess, time, urllib.error, urllib.request, uuid
from pathlib import Path
from env import _private, _inspect, _owner
from faults import Blocked, private_json
from runner import command_env, source_fingerprint

ROOT=Path(__file__).resolve().parents[2]
VERSION='2.568.3'
IMAGE='jenkins/jenkins@sha256:c1e4c349365f6d16d88595b2c5f7e8ff39b8ae1d061f62420bac193b4b9616d0'
PORT=18594
ORIGIN='http://127.0.0.1:'+str(PORT)
LABEL='aiops.testlab.owner'
RUN_LABEL='aiops.testlab.jenkins-run'
TEST='TestJenkinsLiveReadOnly'
JAVA_OPTIONS='-Xms128m -Xmx512m -Djenkins.install.runSetupWizard=false -Dhudson.remoting.ClassFilter=TestlabReadOnlyStrategy'
_cleaning=False
_interrupted=False

@contextlib.contextmanager
def cleanup_phase():
    global _cleaning
    previous=_cleaning;_cleaning=True
    try:yield
    finally:_cleaning=previous

def interrupted(*args):
    global _interrupted
    if _interrupted:return
    _interrupted=True
    if not _cleaning:raise KeyboardInterrupt('owned Jenkins gate cancelled')

def finalize(state,work):
    """A diagnostic failure must not skip exact owned-container removal."""
    with cleanup_phase():
        try:
            info=_inspect(state['container'])
            if info:
                guard_container(info,state)
                _private(work/'container-final-state.json',{'state':info.get('State',{}),'memory':info.get('HostConfig',{}).get('Memory'),'cpus':info.get('HostConfig',{}).get('NanoCpus')})
                logged=subprocess.run(['docker','logs',state['container']],capture_output=True,text=True,timeout=5)
                path=work/'controller.log';path.write_text(logged.stdout+logged.stderr);os.chmod(path,0o600)
        except (OSError,Blocked,subprocess.SubprocessError):pass
        cleanup(state)

def emit(name,status,actual):
    print(json.dumps({'id':name,'name':name,'module':'jenkins-live','type':'integration','evidence_mode':'real_local','status':status,'actual':actual}),flush=True)

def command(argv,timeout=30):
    result=subprocess.run(argv,capture_output=True,text=True,timeout=timeout)
    if result.returncode:raise Blocked('owned '+argv[0]+' operation failed; private logs only')
    return result.stdout.strip()

def owned():
    environment=private_json(ROOT/'.local/testlab/environment.json');backend=private_json(ROOT/'.local/testlab/backend-fixture.json')
    owner=environment['owner'];work=ROOT/'.local/testlab/environments'/owner
    if (not re.fullmatch('testlab-[0-9a-f]{12}',owner) or backend['owner']!=owner or Path(environment['work']).resolve()!=work or
        environment['container']!='aiops-'+owner+'-pg' or backend['postgres_container']!=environment['container'] or
        Path(backend['binary']).resolve()!=work/'backend/aiops'):
        raise Blocked('Jenkins owned environment identity mismatch')
    info=_inspect(environment['container']);_owner(info,owner)
    if not info or not info.get('State',{}).get('Running'):raise Blocked('owned PostgreSQL unavailable')
    if source_fingerprint(ROOT)!=backend['source_tree_sha']:raise Blocked('backend source changed; rebuild before live gate')
    return backend,work

def guard_container(info,state):
    if (not info or info.get('Name')!='/'+state['container'] or info.get('Image')!=state['image_id'] or
        info.get('Config',{}).get('Image')!=IMAGE or info.get('Config',{}).get('Labels',{}).get(LABEL)!=state['owner'] or
        info['Config'].get('Labels',{}).get(RUN_LABEL)!=state['run_id']):
        raise Blocked('Jenkins exact owner/run/name/image mismatch; refusing mutation')
    return info

def validate_build(view,job):
    builds=view.get('builds',[])
    if (view.get('fullName')!=job or len(builds)!=1 or builds[0].get('number')!=1 or builds[0].get('building') is not False or
        builds[0].get('result')!='SUCCESS' or not isinstance(builds[0].get('timestamp'),int) or builds[0]['timestamp']<=0):
        raise AssertionError('unique controlled Jenkins build is not successful and complete')
    return builds[0]

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args):return None

def request(path,token=None,method='GET'):
    if not path.startswith('/') or path.startswith('//'):raise Blocked('fixed Jenkins path required')
    headers={}
    if token:headers['Authorization']='Basic '+base64.b64encode(('testlab-reader:'+token).encode()).decode()
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())
    try:response=opener.open(urllib.request.Request(ORIGIN+path,b'' if method=='POST' else None,headers,method=method),timeout=10)
    except urllib.error.HTTPError as error:response=error
    with response:
        body=response.read((1<<20)+1)
        if len(body)>1<<20:raise AssertionError('Jenkins response exceeds bound')
        return response.status,body,response.headers

def original_go_result(result):
    terminals=[]
    for line in result.stdout.splitlines():
        try:frame=json.loads(line)
        except ValueError:continue
        if frame.get('Test')==TEST and frame.get('Action') in ('pass','fail','skip'):terminals.append(frame['Action'])
    if result.returncode or terminals!=['pass']:
        raise AssertionError('original Jenkins live case did not pass exactly once; no skip/empty substitute')

@contextlib.contextmanager
def exclusive():
    fd=os.open(ROOT/'.local/testlab/jenkins.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        if os.fstat(fd).st_mode&0o777!=0o600:raise Blocked('private Jenkins lock required')
        try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:raise Blocked('another Jenkins gate owns fixed loopback port')
        yield
    finally:os.close(fd)

def cleanup(state):
    info=_inspect(state['container'])
    if info is None:return
    guard_container(info,state)
    # No diagnostics step may prevent removal; state and credentials remain private.
    command(['docker','rm','-f','-v',state['container']],timeout=20)

def run():
    state=None;primary=None;cleanup_error=None;work=None
    try:
        backend,base=owned();start_sha=source_fingerprint(ROOT);binary_sha=hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1',PORT))==0:raise Blocked('dedicated Jenkins port occupied')
        run_id=uuid.uuid4().hex;work=base/'jenkins'/run_id;work.mkdir(parents=True,mode=0o700,exist_ok=False)
        job='testlab-readcheck-'+run_id[:12];home=work/'home';home.mkdir(mode=0o700)
        _private(work/'controller.json',{'admin_password':secrets.token_urlsafe(32),'reader_password':secrets.token_urlsafe(32),'job':job,'run_id':run_id})
        try:image=json.loads(command(['docker','image','inspect',IMAGE]))[0]
        except Blocked:
            command(['docker','pull',IMAGE],timeout=240);image=json.loads(command(['docker','image','inspect',IMAGE]))[0]
        if image['Config'].get('Labels',{}).get('org.opencontainers.image.version')!=VERSION:raise Blocked('official immutable Jenkins version mismatch')
        state={'owner':backend['owner'],'run_id':run_id,'container':'aiops-'+backend['owner']+'-jenkins-'+run_id[:12],'image_id':image['Id'],'image':IMAGE,'work':str(work),'source_tree_sha':start_sha,'binary_sha256':binary_sha,'job':job}
        _private(work/'state.json',state)
        if _inspect(state['container']) is not None:raise Blocked('fresh Jenkins name already allocated')
        command(['docker','run','--pull=never','-d','--name',state['container'],'--label',LABEL+'='+state['owner'],'--label',RUN_LABEL+'='+run_id,
            '--memory','1g','--cpus','1','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),
            '-p','127.0.0.1:'+str(PORT)+':8080','-e','JAVA_OPTS='+JAVA_OPTIONS,
            '-v',str(home)+':/var/jenkins_home','-v',str(work/'controller.json')+':/testlab-config.json:ro',
            '-v',str(ROOT/'tools/testlab/fixtures/jenkins/init.groovy')+':/usr/share/jenkins/ref/init.groovy.d/testlab.groovy:ro',IMAGE],timeout=45)
        info=guard_container(_inspect(state['container']),state)
        if info['NetworkSettings']['Ports'].get('8080/tcp')!=[{'HostIp':'127.0.0.1','HostPort':str(PORT)}]:raise Blocked('Jenkins loopback port ownership mismatch')
        began=time.monotonic();deadline=began+180;next_event=began;view=None
        while time.monotonic()<deadline:
            info=guard_container(_inspect(state['container']),state)
            if not info.get('State',{}).get('Running'):raise Blocked('owned Jenkins exited at startup; private controller log retained')
            try:
                code,raw,headers=request('/job/'+job+'/api/json?tree=fullName,builds[number,result,building,timestamp,duration]')
                _private(work/'readiness-last.json',{'status':code,'jenkins_version':headers.get('X-Jenkins'),'response':raw.decode('utf-8','replace')})
                if code==200 and headers.get('X-Jenkins')==VERSION:
                    candidate=json.loads(raw)
                    try:validate_build(candidate,job);view=candidate;break
                    except AssertionError:pass
            except (OSError,urllib.error.URLError):pass
            if time.monotonic()>=next_event:
                emit(TEST,'running',{'stage':'controller-and-build-readiness','elapsed':time.monotonic()-began,'planned_seconds':180,'port':PORT})
                next_event=time.monotonic()+10
            time.sleep(1)
        if view is None:raise Blocked('owned Jenkins controlled build readiness exceeded 180s')
        build=validate_build(view,job);token_path=home/'testlab-reader-token'
        if token_path.is_symlink() or token_path.stat().st_mode&0o777!=0o600:raise Blocked('private Jenkins reader API token required')
        token=token_path.read_text().strip()
        code,raw,_=request('/whoAmI/api/json',token)
        if code!=200 or json.loads(raw).get('name')!='testlab-reader':raise AssertionError('real Jenkins reader API token was not authenticated')
        code,raw,_=request('/job/'+job+'/api/json?tree=fullName,builds[number,result,building,timestamp,duration]',token)
        if code!=200:raise AssertionError('actual reader could not read own synthetic job')
        validate_build(json.loads(raw),job)
        denied,_,_=request('/job/'+job+'/build',token,method='POST')
        if denied!=403:raise AssertionError('read-only reader could trigger a build')
        _private(work/'consumer-config.json',{'go_type':'connectors.JenkinsConfig','config':{'BaseURL':ORIGIN,'Jobs':{'readcheck':job},'AuthMode':'basic','Username':'testlab-reader','TokenFile':str(token_path),'AllowHTTP':True},'formal_api_wiring_verified':False})
        env=command_env({'JENKINS_READCHECK_URL':ORIGIN,'JENKINS_READCHECK_JOB':job},{})
        env['GOCACHE']=str(ROOT/'.local/go-cache')
        emit(TEST,'running',{'stage':'original-go-live-read-only','port':PORT,'job':job})
        result=subprocess.run(['go','test','-mod=readonly','-race','-json','-count=1','-run','^'+TEST+'$','-timeout','90s','./internal/connectors'],cwd=ROOT,env=env,capture_output=True,text=True,timeout=120)
        evidence=work/'original-go.jsonl';evidence.write_text(result.stdout+result.stderr);os.chmod(evidence,0o600)
        original_go_result(result)
        if source_fingerprint(ROOT)!=start_sha or hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()!=binary_sha:raise Blocked('source/binary changed during Jenkins acceptance')
        _private(work/'proof.json',{'job':job,'build':build,'version':VERSION,'source_tree_sha':start_sha,'binary_sha256':binary_sha,'image_id':state['image_id'],'reader_authenticated':True,'reader_build_denied':True,'original_provider_auth_mode':'anonymous_read_only','authenticated_provider_mode_verified':False,'external_scm_or_deployment':False})
    except BaseException as error:primary=error
    finally:
        if state:
            try:
                finalize(state,work)
            except BaseException as error:cleanup_error=error
        if _interrupted and primary is None:primary=Blocked('owned Jenkins gate interrupted during cleanup')
        if work:
            _private(work/'result.json',{'status':'blocked' if isinstance(primary,Blocked) else 'failed' if primary else 'blocked' if cleanup_error else 'passed','primary_error_type':type(primary).__name__ if primary else None,'cleanup_error_type':type(cleanup_error).__name__ if cleanup_error else None})
    if primary or cleanup_error:
        error=primary or cleanup_error;status='blocked' if isinstance(error,(Blocked,KeyboardInterrupt,subprocess.TimeoutExpired)) else 'failed'
        if work:
            first=work/'first-failure.json';fd=os.open(first,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            with os.fdopen(fd,'w') as stream:json.dump({'status':status,'primary_error_type':type(primary).__name__ if primary else None,'cleanup_error_type':type(cleanup_error).__name__ if cleanup_error else None},stream)
        emit(TEST,status,{'error_type':type(error).__name__,'reason':str(error) if isinstance(error,(Blocked,AssertionError)) else 'owned gate interrupted or dependency failed','private_evidence_dir':str(work.relative_to(ROOT)) if work else None})
        return 2 if status=='blocked' else 1
    emit(TEST,'passed',{'job':job,'build_number':1,'result':'SUCCESS','version':VERSION,'image':IMAGE,'original_go_live_case_passed':True,'reader_authenticated':True,'reader_build_http':403,'provider_auth_mode':'anonymous_read_only','authenticated_provider_mode_verified':False,'private_evidence_dir':str(work.relative_to(ROOT)),'ports':[PORT],'owned_container_removed':True})
    return 0

def main():
    global _interrupted
    _interrupted=False
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['run']);parser.parse_args()
    previous=signal.signal(signal.SIGTERM,interrupted)
    try:
        with exclusive():return run()
    except (OSError,Blocked) as error:
        emit(TEST,'blocked',{'error_type':type(error).__name__,'reason':'private lock/owned environment unavailable'});return 2
    finally:signal.signal(signal.SIGTERM,previous)

if __name__=='__main__':raise SystemExit(main())
