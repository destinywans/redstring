"""One true local rule-draft inference; strict product20s/256-token contract.
Private Linux/model lifecycle copies only. No answer fixture, JSON repair,
format injection, retries, paid provider, user11434, history or quality claim.
"""
from __future__ import annotations
import argparse,ast,copy,datetime,hashlib,http.client,importlib.util,inspect,json,os,queue,re,secrets,socket,sqlite3,sys,textwrap,threading,time,uuid
from http.server import BaseHTTPRequestHandler
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];MODULE='rule-generate-real-model'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
DARWIN='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
LINUX='c80084496a5c068116bf7fec8d48443eebb71985a5fd23e4d44f8bb662ba0b19'
BUILD_PROOF=ROOT/'.local/testlab/rule-assistant-container/build/a4db352b-c427-453a-b22b-20b98cb8c066/build-proof.json'
BUILD_SHA='1ba253bd49a92a0825fe2265e71ff35769820227425eb14577c5574ddcd98676'
PORTS=(18784,18785);RELAY_PORT=18786;MODEL_PORT=18787;ALL_PORTS=(*PORTS,RELAY_PORT,MODEL_PORT)
MODEL='qwen2.5:0.5b';MAX_TOKENS=256
REQUEST_TEXT='Draft a review-only alert using the supplied scoped rate query when its value is greater than 2 requests per second for 5 minutes, severity P2.'
SYSTEM_PROMPT=json.loads(re.search(r'^const generateSystemPrompt = (".*")$',(ROOT/'internal/v3ruleassistant/generate_adapter.go').read_text(),re.M).group(1))
TARGETS=(('POST','/alert-rules/generate'),);SUPPORT=(('POST','/auth/login'),('POST','/services'))
REQUIRED_CASES=tuple('generate_real.'+n for n in ('linux','model','network','tls','identity','normal','replay','authorization','consistency','cleanup'))
def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
ro=load('generate_real_ro',ROOT/'tools/testlab/api_readonly_chain.py')
saved={n:sys.modules.get(n) for n in ('api_readonly_chain','api_rule_generation_functional_coverage')}
try:
    sys.modules['api_readonly_chain']=ro;gen=load('generate_real_generation',ROOT/'tools/testlab/api_rule_generation_functional_coverage.py')
    sys.modules['api_rule_generation_functional_coverage']=gen;linux=load('generate_real_linux',ROOT/'tools/testlab/api_rule_assistant_container_coverage.py')
finally:
    for n,v in saved.items():
        if v is None:sys.modules.pop(n,None)
        else:sys.modules[n]=v
metrics=load('generate_real_model_lifecycle',ROOT/'tools/testlab/api_metrics_real_model_coverage.py')
check,Blocked,q=ro.check,ro.Blocked,ro.quote
gen.SOURCE_SHA,gen.BINARY_SHA,gen.RUNTIME_SHA=SOURCE,DARWIN,RUNTIME
linux.SOURCE_SHA,linux.DARWIN_SHA,linux.RUNTIME_SHA=SOURCE,DARWIN,RUNTIME
linux.PORTS,linux.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS)
linux.FIXTURE_PORT,linux.RESERVED_PORTS,linux.TARGETS=RELAY_PORT,ALL_PORTS,TARGETS
metrics.MODEL_PORT=MODEL_PORT
linux.NEW_MEMORY+=2<<30
original_resources=linux.resource_admission
def resources(total,available,cpus,cpu_used,**kwargs):return original_resources(total,available,cpus-2,cpu_used,**kwargs)
linux.resource_admission=resources

def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def private_bytes(path,raw):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'wb') as stream:stream.write(raw);stream.flush();os.fsync(stream.fileno())
def failed_inspect(work,args,result):
    if 'inspect' in args and result.returncode:
        ro._private(work/('failed-inspect-'+uuid.uuid4().hex+'.json'),dict(argv=list(args),exit_code=result.returncode,
            stderr=result.stderr[:2048],stderr_truncated=len(result.stderr)>2048,stdout_bytes=len(result.stdout.encode()),
            stdout_sha256=hashlib.sha256(result.stdout.encode()).hexdigest(),linux_binary_sha256=LINUX))
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_rule_generate_real_model_coverage.py','tests/test_api_rule_generate_real_model_coverage.py',
        'api_rule_assistant_container_coverage.py','api_rule_generation_functional_coverage.py','api_metrics_real_model_coverage.py',
        'api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py')}
linux.frozen_hashes=hashes
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',test_name='FormalRuleGenerateRealModel',
    binary_sha256=LINUX,linux_binary_sha256=LINUX,darwin_admission_binary_sha256=DARWIN,
    source_tree_sha=SOURCE,
    proof_scope='actual Linux nonrace/owned pinned small model/strict review-only draft; synthetic identity+reviewed catalog; no history or quality')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',
        actual_linux_binary_sha256=LINUX,darwin_admission_binary_sha256=DARWIN,**fields)),flush=True)
linux.emit,linux.evidence=emit,evidence
metrics.emit=emit

def replace(function,replacements,namespace):
    source=textwrap.dedent(inspect.getsource(function))
    for old,new in replacements:
        check(source.count(old)==1,'private lifecycle source changed; root review required');source=source.replace(old,new)
    # Embedded bridge scripts have zero-indented literal lines; dedent cannot
    # remove the method's indentation without altering those script bytes.
    if source.startswith(' '):
        node=ast.parse('class PrivateLifecycle:\n'+source).body[0].body[0]
        tree=ast.Module(body=[node],type_ignores=[])
    else:tree=ast.parse(source)
    scope=dict(namespace);exec(compile(tree,str(Path(__file__))+'[private lifecycle]','exec'),scope);return scope[function.__name__]
original_absence=metrics.explicit_absence
def absent(result,kind,name):
    if kind=='network':
        check(result.returncode==1 and result.stdout.strip() in ('','[]') and result.stderr.strip()=='Error response from daemon: network '+name+' not found',
            'unknown network outcome not absence');return True
    # Root actual Docker version also reports this exact lowercase spelling.
    # No broad case folding, substring or nonzero-returncode absence inference.
    if kind=='container' and result.stderr.strip()=='error: no such object: '+name:
        result=copy.copy(result);result.stderr='Error: No such object: '+name
    return original_absence(result,kind,name)
metrics.explicit_absence=absent

def admission(path,run_id):
    path=Path(path);check(path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'private admission required')
    permit=ro.private_json(path)
    expected=dict(status='root-admitted-rule-generate-real-model',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,
        binary_sha256=DARWIN,linux_binary_sha256=LINUX,build_proof_sha256=BUILD_SHA,runtime_sha256=RUNTIME,postgres_cid=gen.PG_CID,
        model_manifest_sha256=metrics.MODEL_MANIFEST_SHA,ollama_image_id=metrics.OLLAMA_IMAGE,ports=list(ALL_PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'exact source/model/root admission differs')
    for key in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','public_model_download_authorized','private_model_volume_cleanup_authorized'):
        check(permit.get(key) is True,'missing root prerequisite '+key)
    connection=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in connection.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:connection.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('manifest_sha')==permit.get('manifest_sha256')
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('source_tree_sha')==SOURCE
        and active[0].get('pid')==os.getppid(),'sole matching actual Runner required')
    return permit

def request_shape(raw,query):
    value=json.loads(raw)
    check(set(value)=={'model','messages','max_tokens','n','stream'} and value['model']==MODEL and value['max_tokens']==256
        and value['n']==1 and value['stream'] is False,'formal20s/256-token provider envelope altered')
    messages=value['messages'];check(len(messages)==2 and messages[0]['role']=='system' and messages[1]['role']=='user','formal messages altered')
    user=json.loads(messages[1]['content']);choices=user['choices']
    check(set(user)=={'description','choices'} and user['description']==REQUEST_TEXT and len(choices)==1
        and set(choices[0])=={'id','service','unit','description','query'} and choices[0]['id']=='owned_rate'
        and choices[0]['query']==query and choices[0]['service']=='owned-metrics' and choices[0]['unit']=='requests/s',
        'reviewed catalog/description changed')
    check(messages[0]==dict(role='system',content=SYSTEM_PROMPT) and set(messages[1])=={'role','content'},'formal system prompt altered')
    return value
def validate_draft(value,raw,app):
    envelope=json.loads(raw);check(len(envelope['choices'])==1 and envelope['choices'][0]['finish_reason']=='stop','real provider did not stop')
    content=envelope['choices'][0]['message']['content'];proposed=json.loads(content)
    check(set(proposed)=={'query_id','query','op','threshold','for_minutes','severity','summary'},'strict7fields not real model content')
    for key,item in proposed.items():check(value.get(key)==item,'HTTP DTO differs from raw real model output')
    check(value.get('unit')==('requests/s' if proposed['query']==linux.reviewed_expression(app,'test') else 'unknown'),
        'reviewed or generated expression unit differs from product projection')
    check(value['application_id']==app and value['environment_id']=='test' and value['service']=='owned-metrics'
        and value['query_id']=='owned_rate' and value['status']=='review_required' and re.fullmatch('[a-f0-9]{32}',value['draft_id'])
        and value['model']=={'provider':'openai-compatible','name':MODEL},'review-only scope/model/draft identity differs')
    return proposed

class Model(metrics.Chain):
    def __init__(self,owner):
        self.owner=owner;self.root=owner.root;self.work=owner.work;self.run_id=owner.run_id;self.nonce=owner.nonce;self.state=owner.state
        self.model_cid=None;self.volume_acquired=self.model_attempted=self.volume_attempted=False
        self.model_name='aiops-'+ro.OWNER+'-rule-real-model-'+self.nonce;self.model_volume='aiops-'+ro.OWNER+'-rule-real-models-'+self.nonce
        self.model_labels={'aiops.testlab.owner':ro.OWNER,'aiops.testlab.run':self.run_id,'aiops.testlab.purpose':'rule-generate-real-model'}
        self.model_environment=dict(OLLAMA_HOST='0.0.0.0:11434',OLLAMA_MODELS='/root/.ollama/models',OLLAMA_NUM_PARALLEL='1',
            OLLAMA_MAX_LOADED_MODELS='1',OLLAMA_CONTEXT_LENGTH='2048',OLLAMA_KEEP_ALIVE='5m',OLLAMA_DEBUG='false')
    def frozen(self):self.owner.frozen()
    def docker(self,*args,**kwargs):
        result=super().docker(*args,**kwargs);failed_inspect(self.work,args,result);return result
    def model_http(self,method,path,body=None,timeout=5):
        value=super().model_http(method,path,body,timeout)
        if path=='/api/version':check(value.get('version')=='0.34.2','pinned Ollama version differs')
        if path in ('/api/version','/api/tags'):
            if path=='/api/tags':metrics.verify_tags(value)
            self.management_reads=getattr(self,'management_reads',0)+1
            ro._private(self.work/('model-management-'+str(self.management_reads)+'.json'),dict(path=path,value=value,
                model_cid=self.model_cid,linux_binary_sha256=LINUX))
        return value
    def marker_case(self,*args):self.owner.marker_case('generate_real.model',dict(image=metrics.OLLAMA_IMAGE,model=MODEL,manifest_sha=metrics.MODEL_MANIFEST_SHA,cpu=2,memory=2<<30))
    def cleanup_owned(self,deadline):
        reporting=[]
        if self.model_cid:
            metrics.guard_model(self.inspect_model(deadline),self,running=False)
            # Retain actual inference evidence even when the original generate call is502.
            try:
                logs=self.docker('logs',self.model_cid,deadline=deadline)
                check(logs.returncode==0,'owned model logs unavailable during cleanup')
                ro._private(self.work/'model-final-logs.json',dict(stdout=logs.stdout,stderr=logs.stderr,model_cid=self.model_cid,
                    inference_POST_count=metrics.model_request_count(logs.stdout+logs.stderr),linux_binary_sha256=LINUX))
            except BaseException as error:reporting.append(type(error).__name__)
            if self.inspect_model(deadline)['State']['Running']:check(self.docker('stop','--time','1',self.model_cid,deadline=deadline).returncode==0,'model stop failed')
            metrics.guard_model(self.inspect_model(deadline),self,running=False)
            cid=self.model_cid;check(self.docker('rm',cid,deadline=deadline).returncode==0,'model removal failed')
            absent(self.docker('inspect',cid,deadline=deadline),'container',cid);self.model_cid=None
        elif self.model_attempted:absent(self.docker('inspect',self.model_name,deadline=deadline),'container',self.model_name)
        if self.volume_acquired:
            metrics.guard_volume(self.inspect_volume(deadline),self)
            refs=self.docker('ps','-aq','--filter','volume='+self.model_volume,deadline=deadline)
            check(refs.returncode==0 and not refs.stdout.strip(),'UUID model volume still referenced')
            check(self.docker('volume','rm',self.model_volume,deadline=deadline).returncode==0,'UUID volume removal failed')
            absent(self.docker('volume','inspect',self.model_volume,deadline=deadline),'volume',self.model_volume);self.volume_acquired=False
        elif self.volume_attempted:absent(self.docker('volume','inspect',self.model_volume,deadline=deadline),'volume',self.model_volume)
        check(not reporting,'model evidence save failed; exact resource cleanup still completed')

class Relay:
    def __init__(self,c):
        self.c=c;self.tokens={env:secrets.token_urlsafe(30) for env in c.apps};self.lock=threading.Lock();self.started=0;self.completed=0;self.source_reads=0;self.raw=None;f=self
        self.active={};self.serial=0;self.jobs=queue.Queue(maxsize=16);self.evidence_errors=[];self.cleanup_errors=[];self.forced=set()
        self.closing=False;self.writer_stopped=False;self.closer=None;self.server=None;self.creation_failed=False
        # A failed listener constructor must still leave its writer owned by
        # the original Chain cleanup, not an untracked daemon.
        c.rule_fixture=self
        self.writer=threading.Thread(target=self.write_receipts,daemon=True);self.writer.start()
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                with f.lock:f.source_reads+=1
                self.send_error(404)
            def do_POST(self):
                conn=None;raw=None;body=None;provider_status=None;reason='request_invalid';phase='request';serial=0
                origin=time.monotonic();stamps={};elapsed={};worker=threading.current_thread()
                def mark(name):stamps[name]=f.stamp();elapsed[name]=round(max(0,time.monotonic()-origin),6)
                mark('accepted_at')
                with f.lock:
                    f.serial+=1;serial=f.serial;f.active[worker]={'downstream':getattr(self,'connection',None),'upstream':None}
                try:
                    if getattr(self,'connection',None):self.connection.settimeout(21)
                    check(not f.closing,'relay cleanup already started')
                    check(self.path=='/model/test/chat/completions' and self.headers.get('Authorization')=='Bearer '+f.tokens['test'],'exact private model capability/path')
                    length=int(self.headers.get('Content-Length','0'));check(0<length<=32768,'bounded real model request')
                    raw=self.rfile.read(length);check(len(raw)==length,'truncated model request');request_shape(raw,c.apps['test']['rule_query'])
                    with f.lock:f.started+=1;count=f.started
                    f.save('real-request-'+str(count)+'.json',raw)
                    reason='duplicate_request'
                    check(count==1,'unexpected second inference request; no model retry')
                    reason='model_identity_unverified';phase='model_identity'
                    metrics.guard_model(c.model.inspect_model(),c.model)
                    conn=http.client.HTTPConnection('127.0.0.1',MODEL_PORT,timeout=21)
                    with f.lock:f.active[worker]['upstream']=conn
                    phase='upstream_headers';reason='provider_io_failure'
                    conn.request('POST','/v1/chat/completions',raw,{'Content-Type':'application/json'})
                    with f.lock:f.active[worker]['upstream_socket']=conn.sock
                    mark('upstream_request_sent_at')
                    response=conn.getresponse();provider_status=response.status;mark('upstream_headers_at');phase='upstream_body'
                    body=response.read(65537);mark('upstream_body_at')
                    reason='provider_body_limit' if len(body)>65536 else 'upstream_partial_body' if type(response.length) is int and response.length>0 else 'provider_forwarded'
                    check(len(body)<=65536,'real provider body exceeds product bound')
                    with f.lock:f.raw=body;f.completed+=1
                    f.save('real-response.json',body)
                    f.save('real-provider-receipt.json',dict(status=response.status,
                        request_sha256=hashlib.sha256(raw).hexdigest(),response_sha256=hashlib.sha256(body).hexdigest(),model_cid=c.model.model_cid,
                        actual_linux_binary_sha256=LINUX,darwin_admission_sha256=DARWIN,unchanged_bytes=True))
                    phase='downstream_write'
                    self.send_response(response.status);self.send_header('Content-Type',response.getheader('Content-Type',''))
                    self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
                    mark('downstream_write_at')
                except (BrokenPipeError,ConnectionResetError) as error:
                    prefix='downstream_' if phase=='downstream_write' else 'upstream_' if phase in ('upstream_headers','upstream_body') else 'request_'
                    reason=prefix+('broken_pipe' if isinstance(error,BrokenPipeError) else 'reset')
                except Exception as error:
                    if isinstance(error,TimeoutError):reason='upstream_header_timeout' if phase=='upstream_headers' else 'upstream_body_timeout' if phase=='upstream_body' else 'downstream_write_timeout' if phase=='downstream_write' else 'request_timeout'
                    elif isinstance(error,http.client.IncompleteRead):body=error.partial[:65537];reason='upstream_partial_body'
                    elif isinstance(error,OSError):reason='provider_io_failure' if phase in ('upstream_headers','upstream_body') else 'downstream_write_failure' if phase=='downstream_write' else 'request_io_failure'
                    f.save('real-provider-failure.json',dict(reason=reason,phase=phase,started=f.started,completed=f.completed,
                        actual_linux_binary_sha256=LINUX,classification='environment/protocol/model ambiguity; no response repair or retry'))
                    try:self.send_error(502)
                    except OSError:pass
                finally:
                    if conn:
                        try:conn.close()
                        except OSError:pass
                    if body is not None and reason in ('provider_body_limit','upstream_partial_body'):
                        f.save('real-response-observed.json',body[:65536])
                    mark('terminal_at')
                    with f.lock:forced=worker in f.forced
                    f.save('real-provider-terminal-'+str(serial)+'.json',dict(reason='cleanup_forced_cutoff' if forced else reason,
                        observed_transport_reason=reason if forced else None,phase=phase,timestamps=stamps,elapsed_seconds=elapsed,
                        provider_status=provider_status,request_bytes=len(raw) if raw is not None else None,
                        request_sha256=hashlib.sha256(raw).hexdigest() if raw is not None else None,
                        response_bytes=len(body) if body is not None else None,response_sha256=hashlib.sha256(body).hexdigest() if body is not None else None,
                        observed_body_truncated=body is not None and len(body)>65536,started=f.started,completed=f.completed,
                        actual_linux_binary_sha256=LINUX,darwin_admission_sha256=DARWIN))
                    with f.lock:f.active.pop(worker,None)
        try:self.server=gen.Server(RELAY_PORT,Handler,c.work/'tls-cert.pem',c.work/'tls-key.pem')
        except BaseException:self.creation_failed=True;raise
    @staticmethod
    def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
    def save(self,name,value):
        raw=value if isinstance(value,bytes) else json.dumps(value,separators=(',',':')).encode()
        if len(raw)>65536:
            self.error('evidence_size_limit')
            return
        try:self.jobs.put_nowait((self.c.work/name,raw))
        except queue.Full:
            self.error('evidence_queue_full')
    def error(self,reason,cleanup=False):
        with self.lock:
            errors=self.cleanup_errors if cleanup else self.evidence_errors
            if reason not in errors:errors.append(reason)
    def write_receipts(self):
        while True:
            item=self.jobs.get()
            try:
                if item is None:return
                private_bytes(*item)
            except BaseException:
                self.error('evidence_write_failed')
            finally:self.jobs.task_done()
    def env_tokens(self):return {'TESTLAB_RULE_'+kind+'_'+env.upper():token for env,token in self.tokens.items() for kind in ('MODEL','PREVIEW')}
    def snapshot(self):
        with self.lock:return dict(started=self.started,completed=self.completed,source_reads=self.source_reads,response_sha256=None if self.raw is None else hashlib.sha256(self.raw).hexdigest())
    def close(self,deadline=None):
        # Drain the original inference before stopping its model. Every join uses
        # the same cleanup deadline; a stuck evidence writer cannot block it.
        if deadline is None:deadline=getattr(self.c,'cleanup_deadline',None)
        if deadline is None:deadline=time.monotonic()+3
        end=min(deadline,time.monotonic()+8);self.closing=True
        if self.closer is None and self.server is not None:
            def stop_listener():
                try:self.server.close()
                except BaseException:self.error('listener_close_failed',cleanup=True)
            self.closer=threading.Thread(target=stop_listener,daemon=True);self.closer.start()
        if self.closer:self.closer.join(max(0,end-time.monotonic()))
        with self.lock:workers=list(self.active)
        drain=max(time.monotonic(),end-.25)
        for worker in workers:worker.join(max(0,drain-time.monotonic()))
        with self.lock:pending=list(self.active.items())
        if pending:
            self.error('cleanup_forced_cutoff',cleanup=True)
            with self.lock:self.forced.update(worker for worker,_ in pending)
        for worker,connections in pending:
            for name,channel in connections.items():
                channel=getattr(channel,'sock',None) if name=='upstream' else channel
                if channel is not None:
                    try:channel.shutdown(socket.SHUT_RDWR)
                    except OSError:pass
            worker.join(max(0,end-time.monotonic()))
        if not self.writer_stopped and not self.active:
            try:self.jobs.put_nowait(None);self.writer_stopped=True
            except queue.Full:
                self.error('evidence_queue_full')
        self.writer.join(max(0,end-time.monotonic()))
        if self.closer and self.closer.is_alive():self.error('listener_join_incomplete',cleanup=True)
        if self.active:self.error('worker_join_incomplete',cleanup=True)
        if self.writer.is_alive():self.error('writer_join_incomplete',cleanup=True)
        check(not self.cleanup_errors and not self.evidence_errors,'relay terminal/evidence cleanup incomplete; original failure retained')
    def cleanup_summary(self):
        with self.lock:return dict(accepted_requests=self.serial,active_workers=len(self.active),forced_cutoffs=len(self.forced),
            writer_alive=self.writer.is_alive(),listener_alive=bool(self.closer and self.closer.is_alive()),
            creation_failed=self.creation_failed,evidence_errors=list(self.evidence_errors),cleanup_errors=list(self.cleanup_errors),
            actual_linux_binary_sha256=LINUX,darwin_admission_sha256=DARWIN)

class Contract(linux.Contract):pass
class Chain(linux.Chain):
    def __init__(self,root,data,run_id,build,permit):
        super().__init__(root,data,run_id,build);self.permit=permit;self.model=Model(self)
        self.state.update(suite=MODULE,binary_sha256=LINUX,darwin_admission_binary_sha256=DARWIN,evidence_mode='real_local',
            model_scope='one real bounded inference; no quality/history',ports=list(ALL_PORTS));ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);metrics.manifest_pin();super().frozen()
    def docker(self,args,**kwargs):
        result=super().docker(args,**kwargs);failed_inspect(self.work,args,result);return result
    def marker_case(self,name,value,status='passed'):
        mapping={'container.network':'generate_real.network','container.tls':'generate_real.tls','container.identity':'generate_real.identity'}
        name=mapping.get(name,name)
        if name not in REQUIRED_CASES:return
        self.frozen();emit(name,status,dict(run_id=self.run_id,actual_linux_binary_sha256=LINUX,darwin_admission_sha256=DARWIN,**value))
    def snapshot(self):return self.rule_ledger(),self.rule_fixture.snapshot()
    def run(self):
        self.marker_case('generate_real.linux',dict(build_proof_sha256=BUILD_SHA,linux_binary_sha256=LINUX,source_tree_sha=SOURCE))
        self.provision();self.create_network();self.start();self.seed()
        for user in ('admin','reader','denied'):self.login(user)
        key='rule-real-'+self.nonce
        self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},environment='test',want=201,scenario='fixture')
        app=self.sql('SELECT id FROM applications WHERE key='+q(key),read=True);check(re.fullmatch('[a-f0-9]{32}',app),'native app id')
        self.apps['test']={'app':app,'key':key};self.model.start_model()
        self.prepare_rules();before_model=self.model.docker('logs',self.model.model_cid);check(before_model.returncode==0,'model baseline logs unavailable')
        check(metrics.model_request_count(before_model.stdout+before_model.stderr)==0,'hidden prewarm inference')
        request=dict(application_id=app,text=REQUEST_TEXT)
        headers={'Idempotency-Key':'draft-'+self.nonce}
        def validate(value):
            check(self.rule_fixture.raw is not None,'no real provider raw response');validate_draft(value,self.rule_fixture.raw,app)
        # One original call only; any502/timeout remains first red and is not retried.
        value,response_headers=self.call('reader','POST','/api/v1/alert-rules/generate',request,headers=headers,validate=validate)
        check(response_headers.get('X-Rule-Draft-Attempt-ID')==value['draft_id'],'HTTP attempt header differs')
        rows=self.rule_ledger();attempts=rows['v3_rule_generate_attempts'];check(len(attempts)==1 and attempts[0]['status']=='succeeded'
            and attempts[0]['draft']==value and attempts[0]['id']==value['draft_id'] and attempts[0]['model_name']==MODEL,'durable real draft differs')
        audits=[r for r in rows['management_audit'] if r['action'].startswith('v3.alert_rule.generate.')]
        check(len(audits)==2 and {a['action'] for a in audits}=={'v3.alert_rule.generate.reserved','v3.alert_rule.generate.finished'}
            and all(a['actor_id']==self.users['reader'] and a['application_id']==app and a['target']==value['draft_id'] for a in audits),
            'single reservation/finish audit actor/scope/target differs')
        self.marker_case('generate_real.normal',dict(strict7JSON=True,max_output_tokens=256,product_timeout_seconds=20,review_only=True,quality_evaluated=False))
        baseline=self.snapshot();again,_=self.call('reader','POST','/api/v1/alert-rules/generate',request,headers=headers,index=1,scenario='idempotency',validate=validate)
        check(again==value and self.snapshot()==baseline,'second API replay changed inference/attempt/audit')
        self.marker_case('generate_real.replay',dict(second_api=True,no_second_inference=True))
        for user,env,want in ((None,'test',401),('denied','test',403),('reader','staging',403)):
            self.call(user,'POST','/api/v1/alert-rules/generate',request,headers=headers,environment=env,want=want,code='unauthorized' if want==401 else 'forbidden',scenario='authorization')
        check(self.snapshot()==baseline,'authorization advanced provider/source/business/audit')
        self.marker_case('generate_real.authorization',dict(anonymous401=True,denied_cross_env403=True,no_model_source_audit_delta=True))
        logs=self.model.docker('logs',self.model.model_cid);check(logs.returncode==0,'model inference logs unavailable')
        ro._private(self.work/'owned-model-logs.json',dict(stdout=logs.stdout,stderr=logs.stderr,model_cid=self.model.model_cid))
        check(metrics.model_request_count(logs.stdout+logs.stderr)==1 and self.rule_fixture.snapshot()['started']==self.rule_fixture.snapshot()['completed']==1
            and self.rule_fixture.source_reads==0,'actual model/source count differs')
        metrics.verify_tags(self.model.model_http('GET','/api/tags'))
        for table in ('v3_alert_rule_drafts','v3_alert_rule_draft_operations','v3_alert_rule_enable_executions','v3_metrics_questions','v3_conversations'):
            check(self.sql('SELECT count(*) FROM '+table,read=True)=='0','generate created execution/preview/conversation')
        self.marker_case('generate_real.consistency',dict(actual_model_requests=1,query_range_requests=0,full_manifest_pin=True))
        return dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=LINUX,linux_binary_sha256=LINUX,darwin_admission_binary_sha256=DARWIN,
            runtime_sha=RUNTIME,model_manifest_sha256=metrics.MODEL_MANIFEST_SHA,model=MODEL,required_cases=REQUIRED_CASES,http_calls=self.calls,
            real_model_inference=True,synthetic_catalog=True,real_history=False,quality_evaluated=False,counts_as_whole_acceptance=False)
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;errors=[]
        try:
            if self.api:self.capture_pg()
        except BaseException as error:errors.append(type(error).__name__)
        for item in reversed(self.api):
            try:self.stop_container(item)
            except BaseException as error:errors.append(type(error).__name__)
        relay=getattr(self,'rule_fixture',None)
        if relay:
            try:relay.close(self.cleanup_deadline)
            except BaseException as error:errors.append(type(error).__name__)
        try:self.model.cleanup_owned(self.cleanup_deadline)
        except BaseException as error:errors.append(type(error).__name__)
        try:
            # Relay evidence failure must not prevent the original owned API,
            # network and UUID DB cleanup from using the remaining same budget.
            self.rule_fixture=None;bounded_cleanup(self)
        except BaseException as error:errors.append(type(error).__name__)
        finally:self.rule_fixture=relay
        summary=relay.cleanup_summary() if relay else None
        if summary and (summary['cleanup_errors'] or summary['evidence_errors']):
            emit('generate_real.relay_terminal','failed',summary)
        ro._private(self.work/'real-generate-cleanup.json',dict(errors=errors,relay=summary,single_seconds=40,grace_seconds=45,linux_binary_sha256=LINUX))
        check(not errors,'combined exact cleanup incomplete; first failure retained')
        emit('generate_real.cleanup','passed',dict(shared_deadline_seconds=40,grace_seconds=45,actual_linux_binary_sha256=LINUX))

ro.Chain.cleanup=replace(ro.Chain.cleanup,[('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')],ro.__dict__)
bounded_cleanup=replace(linux.Chain.cleanup,[('self.cleanup_deadline=time.monotonic()+40','self.cleanup_deadline=self.cleanup_deadline or time.monotonic()+40'),
    ("self.docker(['network','inspect',self.network_id],timeout=3).returncode!=0","absent(self.docker(['network','inspect',self.network_id],timeout=3),'network',self.network_id)"),
    ("elif self.network_attempted and self.docker(['network','inspect',self.network_name],timeout=3).returncode==0:errors.append('unknown network create outcome retained; no adoption')",
     "elif self.network_attempted:\n        try:absent(self.docker(['network','inspect',self.network_name],timeout=3),'network',self.network_name)\n        except BaseException as e:errors.append(str(e))")],dict(linux.__dict__,absent=absent))
Chain.create_network=replace(linux.Chain.create_network,[("if self.docker(['network','inspect',self.network_name]).returncode==0:raise ro.Blocked('UUID network already exists; no adoption')",
    "absent(self.docker(['network','inspect',self.network_name]),'network',self.network_name)")],dict(linux.__dict__,absent=absent))
Chain.create_container=replace(linux.Chain.create_container,[("if self.docker(['inspect',name]).returncode==0:raise ro.Blocked('UUID container already exists; no adoption')",
    "absent(self.docker(['inspect',name]),'container',name)")],dict(linux.__dict__,absent=absent))
Chain.prepare_rules=replace(linux.Chain.prepare_rules,[('generation.RuleFixture(self)','Relay(self)'),("'model':'owned-rule-model'","'model':MODEL"),
    ("'max_output_tokens':128","'max_output_tokens':256"),("('host.docker.internal',18692)","('host.docker.internal',18786)"),
    ('held ownHTTPS18692; fresh fixture container443 opaque forwarder only','held ownHTTPS18786; fresh fixture container443 opaque forwarder only'),
    ("'model_and_history_mode':'protocol_mock'","'model_and_history_mode':'real pinned model; catalog synthetic; no history'")],dict(linux.__dict__,Relay=Relay,MODEL=MODEL))
Chain.stop_container=replace(linux.Chain.stop_container,[
    ("if self.docker(['inspect',s['name']],timeout=3).returncode==0:raise ro.Blocked('unconfirmed creation exists withoutfresh CID; retained')",
     "absent(self.docker(['inspect',s['name']],timeout=3),'container',s['name'])"),
    ("self.docker(['inspect',s['cid']],timeout=3).returncode!=0","absent(self.docker(['inspect',s['cid']],timeout=3),'container',s['cid'])")],dict(linux.__dict__,absent=absent))

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true')
    parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',targets=TARGETS,ports=ALL_PORTS,markers=REQUIRED_CASES,linux_binary_sha256=LINUX,max_tokens=256,product_seconds=20)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'exact reviewed root required')
    try:
        admission(args.admission,args.run_id);check(digest(BUILD_PROOF)==BUILD_SHA,'recorded Linux proof drift');metrics.manifest_pin()
        build=linux.prepared_linux_build(ROOT,BUILD_PROOF);check(build['binary_sha256']==LINUX,'frozen Linux SHA differs')
        with linux.lease(ROOT),linux.context(),ro.cooperative_signals():
            linux.free_ports();data=ro.doctor(ROOT);check(data['source_tree_sha']==SOURCE and data['binary_sha256']==DARWIN and data['runtime_sha']==RUNTIME,'Darwin main admission differs')
            chain=Chain(ROOT,data,args.run_id,build,args.admission);linux.resources(chain.work,'admission');ro.execute(chain)
        return 0
    except Blocked:emit('generate_real.environment','blocked',dict(private_receipt_retained=True));return 2
    except Exception as error:emit('generate_real.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True,no_retry=True));return 1

if __name__=='__main__':raise SystemExit(main())
