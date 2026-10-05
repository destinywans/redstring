"""One real small-model metrics question; no quality/performance/large-model gate."""
from __future__ import annotations
import argparse,base64,contextlib,copy,datetime,fcntl,hashlib,http.client,importlib.util,inspect,json,os,re,socket,sqlite3,stat,subprocess,sys,textwrap,threading,time,uuid
from http.server import BaseHTTPRequestHandler
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
MODULE='metrics-real-model'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
MODEL='qwen2.5:0.5b';MODEL_ID='openai:'+MODEL
MODEL_MANIFEST_SHA='a8b0c51577010a279d933d14c2a8ab4b268079d44c5c8830c0a93900f1827c67'
MODEL_LAYER_BYTES=397820829
OLLAMA_IMAGE='sha256:d772bbd3035a1cae8ffcd910ddbafce3b28846edfd4efbea4044009a3c71a932'
PORTS=(18768,18769);PROM_PORT,EXPORT_PORT,RELAY_PORT,MODEL_PORT=18770,18771,18772,18773
ALL_PORTS=(*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT,MODEL_PORT)
PULL_SECONDS,REPLY_SECONDS=600,220
REQUIRED_CASES=tuple('metrics_real.'+s for s in ('model','source','completed','replay','authorization','consistency','cleanup'))

# The private generation class must inherit the private readonly class too.
def _load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
ro=_load('metrics_real_private_readonly',ROOT/'tools/testlab/api_readonly_chain.py')
_prior=sys.modules.get('api_readonly_chain');sys.modules['api_readonly_chain']=ro
try:gen=_load('metrics_real_private_generation',ROOT/'tools/testlab/api_rule_generation_functional_coverage.py')
finally:
    if _prior is None:sys.modules.pop('api_readonly_chain',None)
    else:sys.modules['api_readonly_chain']=_prior
gen.PORTS,gen.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS)
gen.PROM_PORT,gen.EXPORT_PORT,gen.FIXTURE_PORT=PROM_PORT,EXPORT_PORT,RELAY_PORT
gen.SOURCE_SHA,gen.BINARY_SHA,gen.RUNTIME_SHA=SOURCE,BINARY,RUNTIME
check,Blocked,q=ro.check,ro.Blocked,ro.quote

def hashes():
    names=('api_metrics_real_model_coverage.py','tests/test_api_metrics_real_model_coverage.py',
        'api_rule_generation_functional_coverage.py','api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py')
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in names}
gen.hashes=hashes

def interface_evidence(row):
    return dict(row,evidence_mode='real_local',upstream_mode='real_local',test_name='FormalMetricsSmallRealModel',
        proof_scope='owned real Prometheus + pinned qwen2.5:0.5b; adapter/source/persistence only; no quality/performance/large-model gate')
gen.interface_evidence=interface_evidence
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=interface_evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
gen.emit=emit

TARGETS=(('POST','/metrics/ask'),)
SUPPORT=tuple(route for route in gen.SUPPORT)
class Contract(gen.Contract):
    def canonical(self,method,path):
        canonical,kind=super().canonical(method,path)
        check((method,canonical.removeprefix('/api/v1')) in (*TARGETS,*SUPPORT),'outside actual metrics-only contract')
        return canonical,kind

def manifest_pin():
    p=ROOT/'.local/testlab/cp300-public-model-manifest/manifest.json'
    info=p.lstat();check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid(),'private public manifest file required')
    raw=p.read_bytes();value=json.loads(raw)
    check(hashlib.sha256(raw).hexdigest()==MODEL_MANIFEST_SHA and sum(x['size'] for x in value['layers'])==MODEL_LAYER_BYTES,
        'public exact model manifest/disk budget changed; no replacement tag')
    return MODEL_MANIFEST_SHA

def admission(path,run_id):
    p=Path(path);check(p.is_absolute() and p.resolve()==p and p.is_relative_to(ROOT/'.local/testlab'),'private admission required')
    info=p.lstat();check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid(),'own regular0600 admission required')
    d=json.loads(p.read_text())
    expected=dict(status='root-admitted-metrics-real-model',owner=ro.OWNER,resource_run_id=run_id,
        source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha256=RUNTIME,postgres_cid=gen.PG_CID,
        ollama_image_id=OLLAMA_IMAGE,model_manifest_sha256=MODEL_MANIFEST_SHA,ports=list(ALL_PORTS),helper_sha256=hashes())
    check(all(d.get(k)==v for k,v in expected.items()),'root exact model/source/UUID admission differs')
    for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','public_model_download_authorized','private_model_volume_cleanup_authorized'):
        check(d.get(key) is True,'root admission prerequisite missing: '+key)
    connection=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:
        active=[json.loads(row[0]) for row in connection.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
    finally:connection.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('manifest_sha')==d.get('manifest_sha256')
        and isinstance(d.get('manifest_sha256'),str) and re.fullmatch('[a-f0-9]{64}',d['manifest_sha256'])
        and active[0].get('source_tree_sha')==SOURCE and active[0].get('pid')==os.getppid(),'sole actual matching Runner required for real model resource budget')
    return d

def doctor():
    manifest_pin()
    for port in ALL_PORTS:
        with socket.socket() as sock:
            sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'owned metrics port occupied')
    data=gen.doctor(ROOT)
    check(data['runtime_sha']==RUNTIME,'frozen runtime differs')
    pg=ro.inspect_pg(data['postgres_container'])
    check(pg.get('Id')==gen.PG_CID and pg.get('HostConfig',{}).get('NanoCpus')==4_000_000_000
        and pg.get('HostConfig',{}).get('Memory')==2<<30,'exact frozen PG4CPU2GiB required')
    rows=json.loads(subprocess.check_output(['docker','image','inspect',OLLAMA_IMAGE],text=True,timeout=8))
    check(len(rows)==1 and rows[0].get('Id')==OLLAMA_IMAGE and rows[0].get('Architecture')=='arm64','immutable local arm64 Ollama unavailable; no image pull')
    return dict(data,helper_sha256=hashes())

def verify_tags(value):
    rows=value.get('models') if isinstance(value,dict) else None
    check(isinstance(rows,list) and len(rows)==1,'private model cache must contain only this model')
    row=rows[0]
    check(row.get('name')==MODEL and row.get('model')==MODEL and isinstance(row.get('digest'),str)
        and row['digest'].removeprefix('sha256:')==MODEL_MANIFEST_SHA
        and type(row.get('size')) is int and 0<row['size']<=MODEL_LAYER_BYTES+4096,
        'downloaded exact name/manifest/size differs; do not change model/tag')
    return row['digest'].removeprefix('sha256:')

def model_request_count(raw):
    text=re.sub(r'\x1b\[[0-9;]*m','',raw)
    return sum(bool(re.search(r'\bPOST\s+"/v1/chat/completions"',line)) for line in text.splitlines())

def explicit_absence(result,kind,name):
    # A daemon outage/permission/timeout is never evidence that a UUID vanished.
    accepted={'container':{'Error: No such object: '+name,'Error: No such container: '+name,
        'Error response from daemon: No such container: '+name},
        'volume':{'Error response from daemon: get '+name+': no such volume'}}
    if (kind not in accepted or result.returncode!=1 or result.stdout.strip() not in ('','[]')
            or result.stderr.strip() not in accepted[kind]):
        raise Blocked('exact UUID '+kind+' absence unconfirmed; no adoption/delete')
    return True

def guard_volume(info,c):
    check(info.get('Name')==c.model_volume and info.get('Driver')=='local' and info.get('Scope')=='local'
        and info.get('Labels')==c.model_labels,'exact UUID private model volume differs')
    return c.model_volume

def guard_model(info,c,running=True):
    host=info.get('HostConfig',{});config=info.get('Config',{});mounts=info.get('Mounts',[])
    check(info.get('Name')=='/'+c.model_name and info.get('Image')==OLLAMA_IMAGE
        and re.fullmatch('[a-f0-9]{64}',info.get('Id',''))
        and all(config.get('Labels',{}).get(k)==v for k,v in c.model_labels.items())
        and host.get('NanoCpus')==2_000_000_000 and host.get('Memory')==2<<30 and host.get('MemorySwap')==2<<30
        and host.get('Privileged') is False and host.get('ReadonlyRootfs') is True and host.get('CapDrop')==['ALL']
        and host.get('SecurityOpt')==['no-new-privileges'] and host.get('Tmpfs')=={'/tmp':'rw,noexec,nosuid,size=67108864'},
        'own immutable Ollama/resource guard differs')
    ports=info.get('NetworkSettings',{}).get('Ports',{}) if info.get('State',{}).get('Running') else host.get('PortBindings',{})
    check(ports.get('11434/tcp')==[dict(HostIp='127.0.0.1',HostPort=str(MODEL_PORT))]
        and all(k=='11434/tcp' or v is None for k,v in ports.items()),'model loopback publishing differs')
    volumes=[m for m in mounts if m.get('Type')=='volume']
    check(len(volumes)==1 and volumes[0].get('Name')==c.model_volume and volumes[0].get('Destination')=='/root/.ollama'
        and volumes[0].get('RW') is True and not any(m.get('Type')=='bind' for m in mounts),
        'shared models or bind mount forbidden')
    env=dict(x.split('=',1) for x in config.get('Env',[]) if '=' in x)
    check(all(env.get(k)==v for k,v in c.model_environment.items()) and info.get('Path')=='/bin/ollama'
        and info.get('Args')==['serve'],'bounded exact Ollama environment/command differs')
    if c.model_cid:check(info['Id']==c.model_cid,'immutable owned model CID changed')
    if running:check(info.get('State',{}).get('Running') is True,'own Ollama is not running')
    return info['Id']

class SourceOnlyFixture:
    validate_source=gen.Fixture.validate_source
    def __init__(self,c):
        self.c=c;self.lock=threading.Lock();self.source_reads=[];self.model_reads=[];self.scrapes=0;f=self
        class Export(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                if self.path!='/metrics':self.send_error(404);return
                with f.lock:f.scrapes+=1
                text=''
                for env,row in c.apps.items():
                    for metric,value in (('testlab_generation_rate',2.5),('testlab_generation_latency_seconds',.125)):
                        text+=metric+'{application="'+row['app']+'",environment="'+env+'",resource="owned-metrics",fixture="'+c.nonce+'",private_label="untrusted-source-data"} '+str(value)+'\n'
                raw=text.encode();self.send_response(200);self.send_header('Content-Type','text/plain; version=0.0.4');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
        class Relay(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    env,path=f.validate_source(self.path);check(not self.headers.get('Authorization'),'no source credential forwarding');c.guard_prom()
                    status,raw=gen.prom_get(path)
                    with f.lock:f.source_reads.append(dict(environment=env,path=path,status=status,response=json.loads(raw) if status==200 else None))
                    self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:self.send_error(502)
            def do_POST(self):
                with f.lock:f.model_reads.append(dict(method='POST',path=self.path))
                self.send_error(405)  # No fixed answer or model rewriting endpoint.
        self.export=gen.Server(EXPORT_PORT,Export)
        try:self.server=gen.Server(RELAY_PORT,Relay)
        except BaseException:self.export.close();raise
    def snapshot(self):
        with self.lock:return dict(source_reads=copy.deepcopy(self.source_reads),model_reads=copy.deepcopy(self.model_reads),scrapes=self.scrapes)
    def close(self):
        errors=[]
        for server in (self.server,self.export):
            try:server.close()
            except BaseException as error:errors.append(error)
        if errors:raise errors[0]
gen.Fixture=SourceOnlyFixture

def request_snapshot(fixture):
    # Prometheus scrapes continuously, independently of replay/authorization.
    snapshot=fixture.snapshot()
    return {key:snapshot[key] for key in ('source_reads','model_reads')}

def assistant_text(value,state='completed'):
    check(isinstance(value,dict) and len(value.get('messages',[]))==2,'durable two-message pair required')
    assistants=[m for m in value['messages'] if m.get('role')=='assistant'];users=[m for m in value['messages'] if m.get('role')=='user']
    check(len(assistants)==len(users)==1 and assistants[0].get('state')==state,'durable model terminal state differs')
    parts=assistants[0].get('parts',[])
    text=''.join(p.get('markdown','') for p in parts if p.get('type')=='text' and isinstance(p.get('markdown'),str))
    check(bool(text.strip()) if state=='completed' else any(p.get('type')=='status' and p.get('text')=='Model reply failed' for p in parts),
        'actual nonempty assistant text / explicit failure required')
    return text

def verify_persistence(rows,c,conv,body):
    questions=rows['v3_metrics_questions'];convs=rows['v3_conversations'];messages=rows['v3_conversation_messages'];replies=rows['v3_conversation_replies']
    check(len(questions)==len(convs)==len(replies)==1 and len(messages)==2,'one accepted question/conversation/reply/message pair required')
    question=questions[0];reply=replies[0];row=c.apps['test']
    check(all(question.get(k)==v for k,v in dict(owner_id=c.users['reader'],request_id=body['request_id'],conversation_id=conv,
        environment_id='test',application_id=row['app'],binding_id=row['target'],connector_id=row['connector'],
        resource_id='owned-metrics',query_id='request_rate',view_id='request_rate',range_id='15m').items())
        and re.fullmatch('[a-f0-9]{64}',question.get('evidence_sha256',''))
        and re.fullmatch('[a-f0-9]{64}',question.get('source_fingerprint','')),'exact accepted native source lineage differs')
    check(convs[0].get('id')==conv and convs[0].get('owner_id')==c.users['reader']
        and convs[0].get('metrics_source_required') is True and convs[0].get('default_model_id')==MODEL_ID
        and convs[0].get('active_reply_id') is None and reply.get('id')==question.get('initial_reply_id')
        and reply.get('conversation_id')==conv and reply.get('model_id')==MODEL_ID and reply.get('state')=='completed',
        'model identity/terminal lineage differs')
    check(all(m.get('conversation_id')==conv for m in messages) and {m.get('role') for m in messages}=={'user','assistant'},'message lineage differs')
    by_role={m['role']:m for m in messages}
    check(reply.get('assistant_message_id')==by_role['assistant'].get('id') and reply.get('user_message_id')==by_role['user'].get('id'),
        'reply/message identities differ')
    assistant_text(dict(messages=messages))
    ask=[a for a in rows['management_audit'] if a.get('action')=='v3.metrics.ask']
    done=[a for a in rows['management_audit'] if a.get('action')=='v3.conversation.reply']
    check(len(ask)==1 and ask[0].get('actor_id')==c.users['reader'] and ask[0].get('target')==conv
        and ask[0].get('actor_kind')=='user' and ask[0].get('changes',{}).get('view')==body['view']
        and ask[0].get('changes',{}).get('range')==body['range']
        and ask[0].get('outcome')=='accepted' and all(ask[0].get('changes',{}).get(k)==question.get(k) for k in
            ('request_id','application_id','binding_id','connector_id','resource_id','query_id','evidence_sha256','source_fingerprint')),
        'acceptance audit/source digest differs')
    check(len(done)==1 and done[0].get('target')==reply['id'] and done[0].get('outcome')=='completed'
        and done[0].get('actor_id')==c.users['reader'] and done[0].get('actor_kind')=='system'
        and done[0].get('changes',{}).get('conversation_id')==conv,'exact completed model audit required')
    events=[e['payload'] for e in rows['v3_conversation_events'] if e.get('conversation_id')==conv]
    check(any(e.get('type')=='message.started' for e in events) and sum(e.get('type')=='message.completed' for e in events)==1,
        'durable stream start/single terminal event differs')

class Chain(gen.Chain):
    def __init__(self,root,data,run_id,admission_path):
        super().__init__(root,data,run_id,'metrics');self.contract=Contract(root);self.admission_path=admission_path
        self.model_cid=None;self.volume_acquired=False;self.model_attempted=False;self.volume_attempted=False
        self.model_name='aiops-'+ro.OWNER+'-metrics-model-'+self.nonce;self.model_volume='aiops-'+ro.OWNER+'-metrics-models-'+self.nonce
        self.model_labels={'aiops.testlab.owner':ro.OWNER,'aiops.testlab.run':run_id,'aiops.testlab.purpose':'metrics-real-model'}
        self.model_environment=dict(OLLAMA_HOST='0.0.0.0:11434',OLLAMA_MODELS='/root/.ollama/models',OLLAMA_NUM_PARALLEL='1',
            OLLAMA_MAX_LOADED_MODELS='1',OLLAMA_CONTEXT_LENGTH='2048',OLLAMA_KEEP_ALIVE='5m',OLLAMA_DEBUG='false')
        self.model_answer=None
        self.state.update(suite=MODULE,ports=list(ALL_PORTS),model_scope='real small local pinned model; no quality/performance/large-model gate',
            model_image=OLLAMA_IMAGE,model_manifest=MODEL_MANIFEST_SHA,model_volume=self.model_volume,model_name=self.model_name)
        ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.admission_path,self.run_id);manifest_pin();super().frozen()
    def docker(self,*args,deadline=None,timeout=8):
        budget=timeout if deadline is None else min(timeout,max(.01,deadline-time.monotonic()))
        check(budget>.01,'owned resource deadline exhausted')
        return subprocess.run(['docker',*args],capture_output=True,text=True,timeout=budget)
    def inspect_model(self,deadline=None):
        r=self.docker('inspect',self.model_cid or self.model_name,deadline=deadline);check(r.returncode==0,'owned Ollama inspect failed')
        rows=json.loads(r.stdout);check(len(rows)==1,'one own model identity required');return rows[0]
    def inspect_volume(self,deadline=None):
        r=self.docker('volume','inspect',self.model_volume,deadline=deadline);check(r.returncode==0,'owned volume inspect failed')
        rows=json.loads(r.stdout);check(len(rows)==1,'one own volume identity required');return rows[0]
    def model_http(self,method,path,body=None,timeout=5):
        check((method,path) in (('GET','/api/version'),('GET','/api/tags'),('POST','/api/pull')),'only owned non-inference model management requests allowed')
        if body is not None:check(method=='POST' and body==dict(model=MODEL,stream=False),'only exact public model pull allowed')
        guard_model(self.inspect_model(),self)
        conn=http.client.HTTPConnection('127.0.0.1',MODEL_PORT,timeout=timeout)
        try:
            conn.request(method,path,None if body is None else json.dumps(body).encode(),{'Content-Type':'application/json'})
            response=conn.getresponse();raw=response.read((1<<20)+1);check(len(raw)<=1<<20,'bounded model management response')
            check(response.status==200,'owned model management HTTP failure');return json.loads(raw)
        finally:conn.close()
    def start_model(self):
        self.frozen()
        explicit_absence(self.docker('volume','inspect',self.model_volume),'volume',self.model_volume)
        explicit_absence(self.docker('container','inspect',self.model_name),'container',self.model_name)
        self.volume_attempted=True;ro._private(self.work/'model-resource-intent.json',dict(name=self.model_name,volume=self.model_volume,labels=self.model_labels,image=OLLAMA_IMAGE,ports=[MODEL_PORT]))
        argv=['volume','create']
        for k,v in self.model_labels.items():argv.extend(['--label',k+'='+v])
        r=self.docker(*argv,self.model_volume);check(r.returncode==0 and r.stdout.strip()==self.model_volume,'UUID model volume creation unconfirmed')
        guard_volume(self.inspect_volume(),self);self.volume_acquired=True
        argv=['create','--pull','never','--name',self.model_name,'--cpus','2','--memory','2g','--memory-swap','2g','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
            '--tmpfs','/tmp:rw,noexec,nosuid,size=67108864','-p','127.0.0.1:'+str(MODEL_PORT)+':11434','--mount','type=volume,source='+self.model_volume+',target=/root/.ollama']
        for k,v in self.model_labels.items():argv.extend(['--label',k+'='+v])
        for k,v in self.model_environment.items():argv.extend(['--env',k+'='+v])
        self.model_attempted=True;r=self.docker(*argv,OLLAMA_IMAGE,'serve',timeout=15)
        check(r.returncode==0 and re.fullmatch('[a-f0-9]{64}',r.stdout.strip()),'UUID model creation unconfirmed')
        self.model_cid=r.stdout.strip();guard_model(self.inspect_model(),self,running=False)
        check(self.docker('start',self.model_cid,timeout=15).returncode==0,'owned model start failed')
        deadline=time.monotonic()+25
        while time.monotonic()<deadline:
            try:
                version=self.model_http('GET','/api/version');check(isinstance(version.get('version'),str) and version['version'],'real Ollama version required');break
            except (OSError,http.client.HTTPException):time.sleep(.2)
        else:raise Blocked('owned real Ollama readiness deadline')
        emit('metrics_real.model_download_started','passed',dict(public_model=MODEL,manifest_sha=MODEL_MANIFEST_SHA,max_seconds=PULL_SECONDS,bytes=MODEL_LAYER_BYTES))
        pull=self.model_http('POST','/api/pull',dict(model=MODEL,stream=False),timeout=PULL_SECONDS)
        check(pull.get('status')=='success','exact public model download failed; no replacement')
        verify_tags(self.model_http('GET','/api/tags'))
        self.marker_case('metrics_real.model',dict(image=OLLAMA_IMAGE,manifest=MODEL_MANIFEST_SHA,model=MODEL,
            cpu=2,memory=2<<30,private_volume=True,user_11434_touched=False,paid_provider=False))
    def provision(self):
        super().provision()
        ro._private(self.work/'models.json',dict(tier='nonprod',environments=[dict(id=env,models=[MODEL_ID],default_model=MODEL_ID) for env in ('test','staging')]))
    def apply_startup_environment(self,variables):
        value=super().apply_startup_environment(variables)
        value.update(OPENAI_MODEL=MODEL,OPENAI_API='chat_completions',OPENAI_BASE_URL='http://127.0.0.1:'+str(MODEL_PORT)+'/v1',OPENAI_MAX_OUTPUT_TOKENS='256')
        return value
    def prepare_source(self):
        super().prepare_source()
        self.config_hashes['models.json']=hashlib.sha256((self.work/'models.json').read_bytes()).hexdigest()
    def wait_reply(self,conv,state='completed',deadline=None):return super().wait_reply(conv,state,deadline or time.monotonic()+REPLY_SECONDS)
    def detail(self,user,conv,env='test',state='completed',index=1):
        def validate(value):
            check(value.get('id')==conv and value.get('scope')==dict(env=env,service=self.apps[env]['app'],inferred=False), 'actual conversation scope differs')
            assistant_text(value,state)
            raw=json.dumps(value);check(not any(s in raw for s in ('private_label','untrusted-source-data',self.model_token,*self.apps[env]['expressions'].values())),'source private values exposed')
        return self.call(user,'GET','/api/v1/conversations/'+conv,environment=env,index=index,scenario='fixture',validate=validate)[0]
    def model_count(self):
        guard_model(self.inspect_model(),self);r=self.docker('logs','--tail','2000',self.model_cid)
        check(r.returncode==0,'actual model request log unavailable')
        return model_request_count(r.stdout+r.stderr)
    def run(self):
        self.start_model();self.provision();self.start();self.seed()
        for actor in ('admin','reader','peer','hidden','denied'):self.login(actor)
        self.prepare_apps();self.prepare_source()
        deadline=time.monotonic()+35
        while time.monotonic()<deadline:
            now=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
            path='/api/v1/query_range?'+gen.urlencode(dict(query=self.apps['test']['expressions']['request_rate'],start=(now-datetime.timedelta(minutes=15)).isoformat(),end=now.isoformat(),step='30',limit='100',timeout='5s'))
            status,raw=gen.prom_get(path)
            if status==200 and json.loads(raw)['data']['result'] and json.loads(raw)['data']['result'][0]['values']:break
            time.sleep(.25)
        else:raise Blocked('real nonempty current Prometheus sample missing')
        before_count=self.model_count();check(before_count==0,'fresh model received unexpected inference')
        body=self.body('real-normal');body['question']='Describe these observed metric samples briefly.'
        conv=self.ask(body);self.accepted,self.accepted_body=conv,body
        detail=self.detail('reader',conv);text=assistant_text(detail)
        verify_persistence(self.ledger(),self,conv,body)
        source=self.fixture.snapshot();check(source['model_reads']==[] and len(source['source_reads'])==1,'model proxy used or source attempt count differs')
        observed=source['source_reads'][0];series=observed['response']['data']['result']
        check(observed['environment']=='test' and observed['status']==200 and len(series)==1 and series[0]['values']
            and all(series[0]['metric'].get(k)==v for k,v in dict(application=self.apps['test']['app'],environment='test',fixture=self.nonce,resource='owned-metrics').items()),'actual scoped Prometheus sample lineage differs')
        completed_count=self.model_count();check(completed_count==1,'exact one real model HTTP inference required; unknown logger not counted as proof')
        self.marker_case('metrics_real.source',dict(real_Prometheus=True,actual_nonempty_current_samples=True,scope='test own application',model_proxy_reads=0,sparse_history=True))
        self.marker_case('metrics_real.completed',dict(real_inference=True,model_requests=1,assistant_bytes=len(text.encode()),assistant_sha256=hashlib.sha256(text.encode()).hexdigest(),persisted_terminal='completed',SSE_terminal=True,answer_quality_evaluated=False))
        baseline=self.ledger();source_before=request_snapshot(self.fixture)
        self.call('reader','POST','/api/v1/metrics/ask',body,index=1,scenario='idempotency',validate=lambda v:check(v==dict(conversation_id=conv),'cross-instance original request replay differs'))
        check(baseline==self.ledger() and source_before==request_snapshot(self.fixture) and self.model_count()==completed_count,'replay produced source/model/DB/audit side effects')
        self.marker_case('metrics_real.replay',dict(cross_API_same_id=True,second_inference=0,DB_audit_delta=0))
        for actor,status in ((None,401),('peer',403)):
            self.call(actor,'POST','/api/v1/metrics/ask',self.body('refused-'+str(actor)),want=status,code='unauthorized' if status==401 else 'forbidden',scenario='authorization')
        self.call('reader','POST','/api/v1/metrics/ask',self.body('cross-env','staging'),environment='staging',want=403,code='forbidden',scenario='authorization')
        check(baseline==self.ledger() and source_before==request_snapshot(self.fixture) and self.model_count()==completed_count,'refusal caused inference/source/DB side effect')
        self.marker_case('metrics_real.authorization',dict(anonymous_401=True,no_change_403=True,cross_environment_403=True,inference_delta=0))
        final=self.detail('reader',conv,index=0);verify_persistence(self.ledger(),self,conv,body)
        check(assistant_text(final)==text and baseline==self.ledger() and source_before==request_snapshot(self.fixture)
            and self.model_count()==completed_count,'cross-instance durable reply changed or GET produced side effects')
        self.marker_case('metrics_real.consistency',dict(questions=1,messages=2,replies=1,acceptance_audit=1,completion_audit=1,real_model_stream=True,model_reads=[],whole_AI_acceptance=False))
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,normal_routes=[['POST','/metrics/ask']],model_manifest=MODEL_MANIFEST_SHA,model=MODEL,
            evidence_mode='real_local',scope='actual small model adapter/scoped source/durable reply only',quality_evaluated=False,performance_evaluated=False,old_large_model_gate_closed=False,
            unverified=['real model unavailable failure and recovery','answer correctness/reasoning quality','qwen3.6+bge-m3 old gate','production sources','full chain','performance','24h'])
        ro._private(self.work/'metrics-real-result.json',result);return result
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;errors=[]
        model_removed,volume_removed,Prom_removed=not self.model_attempted,not self.volume_attempted,not self.prom_attempted
        # Same original UUID DB/role/Popen cleanup, sharing this bounded deadline.
        try:
            source=textwrap.dedent(inspect.getsource(ro.Chain.cleanup));old='deadline = time.monotonic() + 40'
            check(source.count(old)==1,'private cleanup contract changed; root review required')
            namespace=dict(ro.__dict__);exec(compile(source.replace(old,'deadline = self.cleanup_deadline'),str(ROOT/'tools/testlab/api_readonly_chain.py')+'[private shared deadline]','exec'),namespace)
            namespace['cleanup'](self)
        except BaseException as error:errors.append(error)
        if self.fixture:
            try:self.fixture.close()
            except BaseException as error:errors.append(error)
        try:
            if self.prom_cid:
                info=ro.inspect_pg(self.prom_cid,timeout=min(3,max(.1,self.cleanup_deadline-time.monotonic())))
                gen.guard_prom(info,self,live=False)
                if info.get('State',{}).get('Running'):check(self.docker('stop','--time','1',self.prom_cid,deadline=self.cleanup_deadline).returncode==0,'own Prom stop failed')
                gen.guard_prom(ro.inspect_pg(self.prom_cid,timeout=min(3,max(.1,self.cleanup_deadline-time.monotonic()))),self,live=False)
                check(self.docker('rm',self.prom_cid,deadline=self.cleanup_deadline).returncode==0,'own Prom removal failed');self.prom_cid=None;Prom_removed=True
            elif self.prom_attempted:Prom_removed=explicit_absence(self.docker('inspect',self.prom_name,deadline=self.cleanup_deadline),'container',self.prom_name)
        except BaseException as error:errors.append(error)
        try:
            if self.model_cid:
                info=self.inspect_model(self.cleanup_deadline);guard_model(info,self,running=False)
                if info.get('State',{}).get('Running'):check(self.docker('stop','--time','1',self.model_cid,deadline=self.cleanup_deadline).returncode==0,'own model stop failed')
                guard_model(self.inspect_model(self.cleanup_deadline),self,running=False)
                check(self.docker('rm',self.model_cid,deadline=self.cleanup_deadline).returncode==0,'own model removal failed');self.model_cid=None;model_removed=True
            elif self.model_attempted:model_removed=explicit_absence(self.docker('inspect',self.model_name,deadline=self.cleanup_deadline),'container',self.model_name)
            if self.volume_acquired:
                guard_volume(self.inspect_volume(self.cleanup_deadline),self)
                refs=self.docker('ps','-aq','--filter','volume='+self.model_volume,deadline=self.cleanup_deadline)
                check(refs.returncode==0 and not refs.stdout.strip(),'model volume still referenced; no shared removal')
                check(self.docker('volume','rm',self.model_volume,deadline=self.cleanup_deadline).returncode==0,'exact private model volume removal failed');self.volume_acquired=False;volume_removed=True
            elif self.volume_attempted:volume_removed=explicit_absence(self.docker('volume','inspect',self.model_volume,deadline=self.cleanup_deadline),'volume',self.model_volume)
        except BaseException as error:errors.append(error)
        absent={}
        for port in ALL_PORTS:
            with socket.socket() as sock:sock.settimeout(.3);absent[str(port)]=sock.connect_ex(('127.0.0.1',port))!=0
        receipt=dict(ports_absent=absent,model_removed=model_removed,volume_removed=volume_removed,Prom_removed=Prom_removed,
            errors=[type(e).__name__ for e in errors],cleanup_budget_seconds=40,saved_PIDs_signaled=False)
        ro._private(self.work/'metrics-real-cleanup.json',receipt)
        if errors or not all(absent.values()):raise Blocked('owned metrics cleanup incomplete; private exact evidence retained')
        emit('metrics_real.cleanup','passed',receipt)

def inventory():
    return dict(status='prepare_only',module=MODULE,source=SOURCE,binary=BINARY,runtime=RUNTIME,ports=list(ALL_PORTS),model=MODEL,
        image=OLLAMA_IMAGE,manifest=MODEL_MANIFEST_SHA,public_download_bytes=MODEL_LAYER_BYTES,model_CPU=2,model_memory=2<<30,
        pull_deadline=PULL_SECONDS,reply_deadline=REPLY_SECONDS,cleanup_grace=45,required_markers=REQUIRED_CASES,
        scope='one actual metrics ask + replay/refusals; adapter/source/persistence only',old_large_model_gate_closed=False)
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission');args=parser.parse_args(argv)
    if not args.live:print(json.dumps(inventory()));return 0
    try:
        check(str(uuid.UUID(args.run_id or ''))==args.run_id and args.admission,'root fresh canonical UUID/admission required')
        admission(args.admission,args.run_id)
        fd=os.open(ROOT/'.local/testlab/metrics-real-model.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        try:
            check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular lease required')
            fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
            with ro.cooperative_signals(),gen.context():
                result=ro.execute(Chain(ROOT,doctor(),args.run_id,args.admission));emit('metrics_real.complete','passed',result);return 0
        finally:os.close(fd)
    except BaseException as error:
        emit('metrics_real.failure','blocked' if isinstance(error,Blocked) else 'failed',dict(error_type=type(error).__name__,error=str(error)))
        return 2 if isinstance(error,Blocked) else 1
if __name__=='__main__':raise SystemExit(main())
