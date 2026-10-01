"""Owned metrics-question functional chain and honest rule-assistant prerequisites.

The real Prometheus scrapes explicit current synthetic metrics. Conversation
inference is a fixed HTTP protocol Mock, never a paid/real-model claim. The
rule assistant requires deployment-owned DNS HTTPS:443 and a full 7-day source;
the separately recorded prerequisite run does not invent that authority.
"""
from __future__ import annotations
import argparse,base64,contextlib,copy,datetime,fcntl,hashlib,http.client,json,os,re,secrets,socket,ssl,stat,subprocess,threading,time,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs,urlencode,urlsplit
import api_readonly_chain as ro

ROOT=ro.ROOT;OWNER=ro.OWNER;MODULE='formal-api-rule-generation-functional'
PORTS=(18654,18655);BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
PROM_PORT,EXPORT_PORT,FIXTURE_PORT=18656,18657,18658
PROM_IMAGE='prom/prometheus:v3.13.3';PROM_IMAGE_ID='sha256:249c7fa94198524d75d26cae28169774e41627c2d0fc1b7104f2feec8d192d0f'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SOURCE_SHA='6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437';BINARY_SHA='bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
RUNTIME_SHA='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
TLS_IMAGE='sha256:b2236d9e1563c507613962c1ebbd7b3d307969ec2ee355b781b68440f4f0bee3'
TARGETS=(('POST','/alert-rules/generate'),('POST','/alert-rules/preview'),('POST','/metrics/ask'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/connectors'),('POST','/services/{id}/external-targets'),('DELETE','/services/{id}/external-targets/{bindingId}'),('GET','/conversations/{id}'),('GET','/conversations/{id}/events'))
REQUIRED_CASES=('generation.real_source','generation.native_bindings','generation.completed_question','generation.idempotency','generation.authorization','generation.boundaries','generation.source_faults','generation.model_faults','generation.inflight_revocation','generation.audit_rollback','generation.saved_source_revocation','generation.session_revocation','generation.consistency')
Blocked,check,q=ro.Blocked,ro.check,ro.quote
_thread=threading.local()
MODEL_MOCK_PATHS=('/api/v1/metrics/ask','/api/v1/alert-rules/generate','/api/v1/alert-rules/preview','/api/v1/conversations/{id}','/api/v1/conversations/{id}/events')

def interface_evidence(row):
    mode='protocol_mock' if row.get('path') in MODEL_MOCK_PATHS else 'real_local'
    return dict(row,evidence_mode=mode,upstream_mode=mode,test_name='FormalRuleGenerationChain',proof_scope='owned real current Prometheus samples; fixed HTTP model protocol Mock; no real inference/history/production claim')

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    mode='real_local'
    if 'interface' in fields:
        fields['interface']=interface_evidence(fields['interface']);mode=fields['interface']['evidence_mode']
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode=mode,**fields),ensure_ascii=False),flush=True)

def hashes():
    return {f:hashlib.sha256((ROOT/'tools/testlab'/f).read_bytes()).hexdigest() for f in ('api_rule_generation_functional_coverage.py','api_readonly_chain.py')}

@contextlib.contextmanager
def context():
    old=ro.PORTS,ro.BASES,ro.emit,ro.transport,ro.append_json
    ro.PORTS,ro.BASES,ro.emit=PORTS,BASES,emit
    def transport(base,method,path,payload=None,headers=None,timeout=20):
        headers=dict(headers or {},**getattr(_thread,'headers',{}))
        return old[3](base,method,path,payload,headers,timeout)
    ro.transport=transport
    def append_json(path,row):
        # Preserve the same explicit mode in raw receipts and Runner stdout.
        return old[4](path,interface_evidence(row) if Path(path).name=='interfaces.jsonl' else row)
    ro.append_json=append_json
    try:yield
    finally:ro.PORTS,ro.BASES,ro.emit,ro.transport,ro.append_json=old

def free_ports():
    for port in (*PORTS,PROM_PORT,EXPORT_PORT,FIXTURE_PORT):
        with socket.socket() as s:
            s.settimeout(.3)
            if s.connect_ex(('127.0.0.1',port))==0:raise Blocked('owned generation port occupied: '+str(port))

def doctor(root):
    free_ports()
    with context():data=ro.doctor(root)
    if data['source_tree_sha']!=SOURCE_SHA or data['binary_sha256']!=BINARY_SHA or ro.inspect_pg(data['postgres_container'])['Id']!=PG_CID:raise Blocked('frozen Go/binary/PG CID mismatch')
    image=json.loads(subprocess.check_output(['docker','image','inspect',PROM_IMAGE],text=True,timeout=8))
    if len(image)!=1 or image[0]['Id']!=PROM_IMAGE_ID:raise Blocked('fixed locally available Prometheus image unavailable; no pull')
    return dict(data,helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self,method,path):
        p=urlsplit(path)
        if p.scheme or p.netloc or p.fragment or not p.path.startswith('/api/v1/'):raise Blocked('formal local namespace required')
        short=p.path[len('/api/v1'):]
        found=[r for m,r in (*TARGETS,*SUPPORT) if m==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(r)),short)]
        route=short if short in found else found[0] if len(found)==1 else None
        if route is None or method.lower() not in self.api['paths'].get(route,{}):raise Blocked('outside generation bounded contract')
        return '/api/v1'+route,'main' if (method,route) in self.main else 'auxiliary_or_historical'

def prom_config():
    return {'global':{'scrape_interval':'1s'},'scrape_configs':[{'job_name':'owned-current-generation','static_configs':[{'targets':['host.docker.internal:'+str(EXPORT_PORT)]}]}]}

def guard_prom(info,c,live=True):
    if (info.get('Name')!='/'+c.prom_name or info.get('Image')!=PROM_IMAGE_ID or not re.fullmatch('[a-f0-9]{64}',str(info.get('Id',''))) or
        any(info.get('Config',{}).get('Labels',{}).get(k)!=v for k,v in {'aiops.testlab.owner':OWNER,'aiops.testlab.run':c.run_id,'aiops.testlab.purpose':'rule-generation'}.items()) or
        info.get('HostConfig',{}).get('NanoCpus')!=500000000 or info.get('HostConfig',{}).get('Memory')!=268435456 or
        info.get('HostConfig',{}).get('Privileged') is not False or info.get('HostConfig',{}).get('ReadonlyRootfs') is not True or
        info.get('HostConfig',{}).get('CapDrop')!=['ALL'] or info.get('Config',{}).get('User')!=str(os.getuid())+':'+str(os.getgid()) or os.getuid()==0 or
        info.get('HostConfig',{}).get('SecurityOpt')!=['no-new-privileges'] or info.get('HostConfig',{}).get('Tmpfs')!={'/prometheus':c.tmpfs} or
        (info.get('NetworkSettings',{}).get('Ports',{}) if info.get('State',{}).get('Running') else info.get('HostConfig',{}).get('PortBindings',{})).get('9090/tcp')!=[{'HostIp':'127.0.0.1','HostPort':str(PROM_PORT)}] or
        (live and not info.get('State',{}).get('Running'))):raise Blocked('owned Prometheus identity/resource/listener guard')
    if c.prom_cid and info['Id']!=c.prom_cid:raise Blocked('owned immutable Prometheus CID drift')
    binds=[m for m in info.get('Mounts',[]) if m.get('Type')=='bind']
    if len(binds)!=1 or binds[0].get('Source')!=str(c.work/'prometheus.json') or binds[0].get('Destination')!='/etc/prometheus/owned.json' or binds[0].get('RW') is not False or any(m.get('Type')=='volume' for m in info.get('Mounts',[])):raise Blocked('exact readonly source/private storage guard')
    if info.get('Path')!='/bin/prometheus' or info.get('Args')!=c.prom_args:raise Blocked('Prometheus argv changed')
    return info['Id']

def prom_get(path):
    p=urlsplit(path)
    if p.scheme or p.netloc or p.fragment or p.path not in ('/-/ready','/api/v1/status/config','/api/v1/query_range'):raise Blocked('readonly exact Prometheus transport')
    conn=http.client.HTTPConnection('127.0.0.1',PROM_PORT,timeout=5)
    try:
        conn.request('GET',path);r=conn.getresponse();raw=r.read((2<<20)+1);check(len(raw)<=2<<20,'bounded real source response');return r.status,raw
    finally:conn.close()

class Server:
    def __init__(self,port,handler,cert=None,key=None):
        self.server=ThreadingHTTPServer(('127.0.0.1',port),handler);self.server.daemon_threads=True
        if cert:
            tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.minimum_version=ssl.TLSVersion.TLSv1_2;tls.load_cert_chain(cert,key);self.server.socket=tls.wrap_socket(self.server.socket,server_side=True)
        self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def close(self):
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=3);check(not self.thread.is_alive(),'held local server remains')

class Fixture:
    def __init__(self,c):
        self.c=c;self.lock=threading.Lock();self.source_reads=[];self.model_reads=[];self.scrapes=0
        self.prom_mode='normal';self.model_mode='normal';self.hold=False;self.seen=threading.Event();self.release=threading.Event();self.timeout_seen=threading.Event();self.timeout_closed=threading.Event();f=self
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
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    env,path=f.validate_source(self.path)
                    check(not self.headers.get('Authorization'),'no Prometheus credential forwarding');c.guard_prom()
                    with f.lock:mode,held=f.prom_mode,f.hold;f.hold=False
                    if held:f.seen.set();check(f.release.wait(10),'bounded source hold exceeded')
                    if mode in ('503','429'):status,raw=int(mode),b'{"status":"error","error":"explicit UUID source fault"}'
                    else:status,raw=prom_get(path)
                    original=json.loads(raw) if status==200 else None
                    with f.lock:f.source_reads.append({'environment':env,'path':path,'mode':mode,'status':status,'response':original,'held':held})
                    if mode=='timeout':time.sleep(5.3)
                    if mode=='response_loss':self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    if mode=='half_response':self.send_response(status);self.send_header('Content-Length',str(len(raw)+50));self.send_header('Connection','close');self.end_headers();self.wfile.write(raw[:len(raw)//2]);self.close_connection=True;return
                    if mode=='empty':raw=json.dumps({'status':'success','data':{'resultType':'matrix','result':[]}}).encode()
                    if mode=='warnings':value=json.loads(raw);value['warnings']=['explicit incomplete source fault'];raw=json.dumps(value).encode()
                    self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:
                    try:self.send_error(502)
                    except OSError:pass
            def do_POST(self):
                try:
                    check(self.path=='/v1/chat/completions' and self.headers.get('Authorization')=='Bearer '+c.model_token,'exact owned model path/token')
                    length=int(self.headers.get('Content-Length','0'));check(0<length<=256<<10,'bounded model body');body=json.loads(self.rfile.read(length));f.validate_model(body)
                    with f.lock:mode=f.model_mode;f.model_mode='normal';f.model_reads.append({'mode':mode,'body':body,'started_monotonic':time.monotonic()})
                    if mode=='timeout':
                        f.timeout_seen.set();time.sleep(241)
                        try:self.send_response(200);self.end_headers();self.wfile.write(b'data: [DONE]\n\n')
                        except OSError:pass
                        f.timeout_closed.set();return
                    if mode=='response_loss':self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    if mode in ('503','429'):self.send_response(int(mode));self.send_header('Content-Length','0');self.end_headers();return
                    self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Connection','close');self.end_headers()
                    if mode=='truncated':self.wfile.write(b'data: {"choices":[{"delta":{"content":"Unfinished mock"},"finish_reason":null}]}\n\n');self.close_connection=True;return
                    raw='data: '+json.dumps({'choices':[{'delta':{'content':c.model_answer},'finish_reason':None}]})+'\n\n'+'data: '+json.dumps({'choices':[{'delta':{},'finish_reason':'stop'}]})+'\n\n'+'data: [DONE]\n\n'
                    self.wfile.write(raw.encode());self.close_connection=True
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:
                    try:self.send_error(400)
                    except OSError:pass
            def do_DELETE(self):self.send_error(405)
            do_PUT=do_PATCH=do_DELETE
        self.export=Server(EXPORT_PORT,Export)
        try:self.server=Server(FIXTURE_PORT,Handler)
        except BaseException:self.export.close();raise
    def validate_source(self,path):
        p=urlsplit(path);args=parse_qs(p.query,keep_blank_values=True)
        for env,row in self.c.apps.items():
            if (p.path=='/'+env+'/api/v1/query_range' and set(args)=={'query','start','end','step','timeout','limit'} and all(len(v)==1 for v in args.values()) and
                args['query'][0] in row['expressions'].values() and args['timeout']==['5s'] and args['limit']==['100'] and args['step'][0] in ('30','120')):
                start=datetime.datetime.fromisoformat(args['start'][0].replace('Z','+00:00'));end=datetime.datetime.fromisoformat(args['end'][0].replace('Z','+00:00'))
                check((end-start).total_seconds()==(900 if args['step']==['30'] else 3600) and abs(time.time()-end.timestamp())<30,'exact owned range window')
                return env,'/api/v1/query_range?'+p.query
        raise Blocked('source path/expression/window outside exact owned bindings')
    def validate_model(self,body):
        check(body.get('model')=='readonly-synthetic' and body.get('stream') is True and not body.get('tools'),'fixed protocol model/stream/no tools')
        messages=body.get('messages');check(isinstance(messages,list) and messages,'model input missing')
        raw='\n'.join(m.get('content','') if isinstance(m.get('content'),str) else json.dumps(m.get('content')) for m in messages);check('Observed metric' in raw or 'metric' in raw,'metric context missing')
        check(not any(x in raw for x in [self.c.model_token,'private_label','untrusted-source-data','host.docker.internal',*sum([list(r['expressions'].values()) for r in self.c.apps.values()],[])]),'model exposed private source labels/expression/credential')
    def snapshot(self):
        with self.lock:return {'source_reads':copy.deepcopy(self.source_reads),'model_reads':copy.deepcopy(self.model_reads),'scrapes':self.scrapes}
    def close(self):
        self.release.set();errors=[]
        for server in (self.server,self.export):
            try:server.close()
            except BaseException as e:errors.append(e)
        if errors:raise errors[0]

def guard_tls(info,c,live=True):
    if (info.get('Name')!='/'+c.tls_name or info.get('Image')!=TLS_IMAGE or not re.fullmatch('[a-f0-9]{64}',str(info.get('Id',''))) or
        any(info.get('Config',{}).get('Labels',{}).get(k)!=v for k,v in {'aiops.testlab.owner':OWNER,'aiops.testlab.run':c.run_id,'aiops.testlab.purpose':'rule-generation-tls'}.items()) or
        info.get('HostConfig',{}).get('NanoCpus')!=250000000 or info.get('HostConfig',{}).get('Memory')!=134217728 or
        info.get('HostConfig',{}).get('Privileged') is not False or info.get('HostConfig',{}).get('ReadonlyRootfs') is not True or info.get('HostConfig',{}).get('CapDrop')!=['ALL'] or
        info.get('Config',{}).get('User')!=str(os.getuid())+':'+str(os.getgid()) or info.get('HostConfig',{}).get('SecurityOpt')!=['no-new-privileges'] or
        (info.get('NetworkSettings',{}).get('Ports',{}) if info.get('State',{}).get('Running') else info.get('HostConfig',{}).get('PortBindings',{})).get('8443/tcp')!=[{'HostIp':'127.0.0.1','HostPort':'443'}] or (live and not info.get('State',{}).get('Running'))):raise Blocked('owned TLS bridge CID/image/resource/loopback443 guard')
    if c.tls_cid and info['Id']!=c.tls_cid:raise Blocked('TLS immutable CID drift')
    binds=info.get('Mounts',[])
    if len(binds)!=1 or binds[0].get('Type')!='bind' or binds[0].get('Source')!=str(c.work/'tls-bridge.py') or binds[0].get('Destination')!='/tls-bridge.py' or binds[0].get('RW') is not False:raise Blocked('TLS exact readonly forwarder source guard')
    if info.get('Path')!='python' or info.get('Args')!=['-B','/tls-bridge.py']:raise Blocked('TLS argv guard')
    return info['Id']

class RuleFixture:
    def __init__(self,c):
        self.c=c;self.lock=threading.Lock();self.records=[];self.mode={'model':'normal','preview':'normal'};self.tokens={(kind,env):secrets.token_urlsafe(24) for kind in ('model','preview') for env in c.apps}
        f=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def handle_request(self):
                try:
                    if self.command=='GET' and self.path=='/healthz/'+c.nonce:
                        raw=json.dumps({'run_id':c.run_id}).encode();self.send_response(200);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw);return
                    kind,env,data=f.validate(self.command,self.path,self.headers,self.rfile)
                    with f.lock:mode=f.mode[kind];f.records.append({'kind':kind,'env':env,'mode':mode,'request':data,'synthetic_7day_history':kind=='preview'})
                    if mode=='timeout':time.sleep(15.3 if kind=='model' else 10.3)
                    if mode=='response_loss':self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    if mode=='503':status,raw=503,b'{"error":"explicit owned protocol source fault"}'
                    elif kind=='model':
                        choice=json.loads(data['messages'][1]['content'])['choices'][0]
                        draft={'query_id':choice['id'],'query':choice['query'] if mode!='invalid_query' else 'up','op':'>','threshold':2,'for_minutes':0,'severity':'P2','summary':'Owned fixed protocol review draft'}
                        status,raw=200,json.dumps({'id':'owned-'+c.nonce,'choices':[{'finish_reason':'stop','message':{'content':json.dumps(draft)}}],'usage':{'prompt_tokens':30,'completion_tokens':60}}).encode()
                    else:
                        start=float(data['start'][0]);end=float(data['end'][0]);values=[[start+i*300,'2.5'] for i in range(2017)]
                        metric={'application':c.apps[env]['app'],'environment':env,'service':'owned-metrics'}
                        if mode=='wrong_scope':metric['application']='unowned-app'
                        if mode=='partial_matrix':values=values[:-1]
                        status,raw=200,json.dumps({'status':'success','data':{'resultType':'matrix','result':[{'metric':metric,'values':values}]}}).encode()
                    self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:
                    try:self.send_error(400)
                    except OSError:pass
            do_GET=do_POST=handle_request
            def do_DELETE(self):self.send_error(405)
            do_PUT=do_PATCH=do_DELETE
        self.server=Server(FIXTURE_PORT,Handler,c.work/'tls-cert.pem',c.work/'tls-key.pem')
    def env_tokens(self):return {'TESTLAB_RULE_'+kind.upper()+'_'+env.upper():token for (kind,env),token in self.tokens.items()}
    def validate(self,method,path,headers,stream):
        p=urlsplit(path)
        for kind in ('model','preview'):
            for env,row in self.c.apps.items():
                expected='/'+kind+'/'+env+('/chat/completions' if kind=='model' else '/api/v1/query_range')
                if p.path!=expected or method!=('POST' if kind=='model' else 'GET'):continue
                check(headers.get('Authorization')=='Bearer '+self.tokens[kind,env],'exact source scoped token')
                if kind=='model':
                    length=int(headers.get('Content-Length','0'));check(0<length<=32768,'bounded draft model payload');body=json.loads(stream.read(length));check(body.get('stream') is False and body.get('model')=='owned-rule-model' and body.get('n')==1 and body.get('max_tokens')==128,'bounded fixed draft model envelope')
                    choices=json.loads(body['messages'][1]['content'])['choices'];check(len(choices)==1 and choices[0]['id']=='owned_rate' and choices[0]['query']==row['rule_query'],'model catalog source scope differs');return kind,env,body
                args=parse_qs(p.query);check(set(args)=={'query','start','end','step','limit','timeout'} and all(len(v)==1 for v in args.values()),'bounded preview query arguments')
                check(args['query']==[row['rule_query']] and args['step']==['300'] and args['limit']==['2'] and args['timeout']==['15s'] and float(args['end'][0])-float(args['start'][0])==604800,'exact synthetic seven-day reviewed query');return kind,env,args
        raise Blocked('HTTPS fixture request outside exact env/path/binding')
    def snapshot(self):
        with self.lock:return copy.deepcopy(self.records)
    def close(self):self.server.close()

class Chain(ro.Chain):
    def __init__(self,root,data,run_id,scope='metrics'):
        check(str(uuid.UUID(run_id))==run_id and data.get('owner')==OWNER,'owned canonical UUID')
        self.root,self.data,self.run_id=Path(root).resolve(),data,run_id;self.nonce=uuid.UUID(run_id).hex;self.scope=scope
        self.database,self.db_user='aiops_testlab_generation_'+self.nonce,'generation_'+self.nonce
        self.marker,self.owner_tag='generationfixture'+self.nonce,OWNER+':'+run_id+':rule-generation';self.work=self.root/'.local/testlab/rule-generation-parallel'/run_id
        if self.work.resolve()!=self.work:raise Blocked('private evidence symlink')
        self.work.mkdir(mode=0o700,parents=True,exist_ok=False);self.password,self.db_password=secrets.token_urlsafe(32),secrets.token_urlsafe(32);self.admin_email='bootstrap-'+self.nonce+'@testlab.invalid'
        self.users={n:uuid.uuid5(uuid.UUID(run_id),'20260930:'+n).hex for n in ('reader','peer','hidden','denied')};self.roles={n:'generation-'+self.nonce+'-'+n for n in self.users}
        self.cookies,self.ids,self.processes={},{},[];self.calls=0;self.role_attempted=self.db_attempted=False;self.contract=Contract(root)
        self.apps={};self.config_hashes={};self.fixture=None;self.prom_cid=None;self.prom_attempted=False;self.configured=False;self.generation=0;self.conversations=[]
        self.model_token=secrets.token_urlsafe(30);self.model_answer='Owned current metric samples received; this is a fixed protocol Mock response.'
        self.prom_name='aiops-'+OWNER+'-generation-'+self.nonce;self.tmpfs='rw,noexec,nosuid,size=33554432,uid='+str(os.getuid())+',gid='+str(os.getgid())+',mode=0700'
        self.prom_args=['--config.file=/etc/prometheus/owned.json','--storage.tsdb.path=/prometheus','--storage.tsdb.retention.time=1h','--web.listen-address=0.0.0.0:9090']
        self.tls_cid=None;self.tls_attempted=False;self.tls_name='aiops-'+OWNER+'-generation-tls-'+self.nonce;self.tls_hostname=self.nonce+'.localhost';self.rule_fixture=None
        self.state={'owner':OWNER,'run_id':run_id,'database':self.database,'database_user':self.db_user,'owner_tag':self.owner_tag,'binary_sha256':data['binary_sha256'],'source_tree_sha':data['source_tree_sha'],'ports':[*PORTS,PROM_PORT,EXPORT_PORT,FIXTURE_PORT],'pids':[],'suite':MODULE,'scope':scope,'seed':20260930,'model_scope':'fixed HTTP protocol Mock; real inference/paid/Ollama unverified','actual_results_source':'formal HTTP bindings, actual current Prometheus samples and durable metrics conversations'}
        ro._private(self.work/'state.json',self.state);ro._private(self.work/'credentials.json',{'password':self.password,'database_password':self.db_password,'model_token':self.model_token})
    def first_red(self,row):
        try:ro.private_file(self.work/'first-failure.json',json.dumps(row))
        except FileExistsError:pass
    def marker_case(self,name,value,status='passed'):
        self.frozen();emit(name,status,dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,**value))
    def frozen(self):
        if hashes()!=self.data['helper_sha256'] or hashlib.sha256((self.root/'.local/testlab/runtime-config.json').read_bytes()).hexdigest()!=RUNTIME_SHA:raise Blocked('generation helper/main runtime drift')
        for n,d in self.config_hashes.items():
            f=self.work/n
            if f.is_symlink() or stat.S_IMODE(f.stat().st_mode)!=0o600 or hashlib.sha256(f.read_bytes()).hexdigest()!=d:raise Blocked('owned source config drift')
        if self.tls_cid:guard_tls(ro.inspect_pg(self.tls_cid),self)
        super().frozen()
    def pg(self,*args,**kwargs):
        deadline=kwargs.get('deadline');remaining=3 if deadline is None else min(3,deadline-time.monotonic())
        if remaining<=0 or ro.inspect_pg(self.data['postgres_container'],timeout=remaining)['Id']!=PG_CID:raise Blocked('exact PG CID changed')
        return super().pg(*args,**kwargs)
    def guard_prom(self):return guard_prom(ro.inspect_pg(self.prom_cid),self)
    def apply_startup_environment(self,variables):
        # Only this owned fixture's model endpoint/token replaces inert defaults.
        extra={'AIOPS_V3_PROMETHEUS_QUERIES_CONFIG':str(self.work/'queries.json')} if self.configured else {}
        check(not set(extra).intersection(variables),'isolated startup config conflict');variables=dict(variables,**extra)
        if self.configured:variables.update(OPENAI_BASE_URL='http://127.0.0.1:'+str(FIXTURE_PORT)+'/v1',OPENAI_API_KEY=self.model_token)
        if self.rule_fixture:
            variables.update(AIOPS_V3_RULE_PREVIEW_CONFIG=str(self.work/'preview-config.json'),AIOPS_V3_RULE_PREVIEW_ALLOWED_HOSTS=self.tls_hostname,AIOPS_V3_RULE_DRAFT_MODEL_CONFIG=str(self.work/'draft-config.json'),AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS=self.tls_hostname,SSL_CERT_FILE=str(self.work/'tls-cert.pem'))
            variables.update(self.rule_fixture.env_tokens())
        return variables
    def start(self):
        self.generation+=1;old=ro.emit
        def wrapped(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='generation.api_ready.'+str(self.generation)+'.'+name.rsplit('.',1)[1]
            old(name,status,actual,**fields)
        ro.emit=wrapped
        try:super().start()
        finally:ro.emit=old
    def restart(self):
        for p in reversed(self.processes):
            if p.poll() is None:p.terminate();p.wait(timeout=10)
        ro._private(self.work/'bootstrap-exits.json',{'pids':[p.pid for p in self.processes],'exits':[p.poll() for p in self.processes]})
        for i in (0,1):(self.work/('api-'+str(i)+'.log')).rename(self.work/('bootstrap-'+str(i)+'.log'))
        self.start()
    def call(self,user,method,path,*args,headers=None,**kwargs):
        self.frozen();canonical,_=self.contract.canonical(method,path)
        if canonical=='/api/v1/metrics/ask' and kwargs.get('scenario','normal')=='normal' and kwargs.get('want',200)<300 and kwargs.get('validate') is None:raise Blocked('normal metrics ask requires persisted reply/source validator')
        _thread.headers=headers or {}
        try:return super().call(user,method,path,*args,**kwargs)
        finally:del _thread.headers
    def seed(self):
        super().seed()
        for n,env in (('reader','test'),('hidden','staging')):
            for action in ('view','change'):self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles[n],'alert_rules',action,'environment',env))+');')
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['peer'])+" AND module='conversation' AND action='change';")
    def prepare_apps(self):
        for env in ('test','staging'):
            key='generation-'+self.nonce+'-'+env;self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},environment=env,want=201,scenario='fixture')
            app=self.sql('SELECT id FROM applications WHERE key='+q(key),read=True);check(re.fullmatch('[a-f0-9]{32}',app),'stable native application id')
            self.apps[env]={'app':app,'key':key,'sid':base64.urlsafe_b64encode((app+'\0'+env).encode()).decode().rstrip('=')}
            self.apps[env]['expressions']={view:metric+'{application="'+app+'",environment="'+env+'",resource="owned-metrics",fixture="'+self.nonce+'"}' for view,metric in (('request_rate','testlab_generation_rate'),('http_p95_seconds','testlab_generation_latency_seconds'))}
    def prepare_source(self):
        queries=[]
        for env,row in self.apps.items():
            created,_=self.call('admin','POST','/api/v1/connectors',{'type':'prometheus','url':'http://127.0.0.1:'+str(FIXTURE_PORT)+'/'+env,'auth':'none','tools':['query_metrics'],'webhook':False},environment=env,want=201,scenario='fixture');row['connector']=created['id']
            target,_=self.call('admin','POST','/api/v1/services/'+row['sid']+'/external-targets',{'connector_id':created['id'],'target_kind':'metric_resource','external_id':'owned-metrics'},environment=env,want=201,scenario='fixture');row['target']=target['id']
            queries.extend({'connector_id':row['connector'],'application_id':row['app'],'environment_id':env,'resource_id':'owned-metrics','query_id':view,'expression':expression} for view,expression in row['expressions'].items())
        ro._private(self.work/'queries.json',{'queries':queries});ro._private(self.work/'prometheus.json',prom_config())
        self.config_hashes={n:hashlib.sha256((self.work/n).read_bytes()).hexdigest() for n in ('queries.json','prometheus.json')}
        self.fixture=Fixture(self);self.start_prom();self.configured=True;self.restart()
        self.marker_case('generation.native_bindings',{'applications':2,'connectors':2,'exact_http_targets':2,'reviewed_expressions':4,'sql_success_seed':False})
    def start_prom(self):
        check(subprocess.run(['docker','container','inspect',self.prom_name],capture_output=True,timeout=5).returncode!=0,'UUID source name exists; no adoption');self.prom_attempted=True
        cmd=['docker','create','--pull','never','--name',self.prom_name,'--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-generation','--cpus','.5','--memory','256m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),'--tmpfs','/prometheus:'+self.tmpfs,'-p','127.0.0.1:'+str(PROM_PORT)+':9090','--mount','type=bind,source='+str(self.work/'prometheus.json')+',target=/etc/prometheus/owned.json,readonly',PROM_IMAGE,*self.prom_args]
        r=subprocess.run(cmd,capture_output=True,text=True,timeout=15);ro._private(self.work/'prom-create.json',{'command':cmd,'exit':r.returncode,'stdout':r.stdout,'stderr':r.stderr});check(r.returncode==0 and re.fullmatch('[a-f0-9]{64}',r.stdout.strip()),'owned source create unconfirmed');self.prom_cid=r.stdout.strip();guard_prom(ro.inspect_pg(self.prom_cid),self,live=False)
        check(subprocess.run(['docker','start',self.prom_cid],capture_output=True,timeout=15).returncode==0,'owned source start failed')
        deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            self.guard_prom()
            try:
                if prom_get('/-/ready')[0]==200 and self.fixture.scrapes>=2:break
            except OSError:pass
            time.sleep(.2)
        else:raise Blocked('owned Prometheus readiness/scrape deadline')
        info=ro.inspect_pg(self.prom_cid);status,raw=prom_get('/api/v1/status/config');check(status==200,'actual loaded source config unavailable')
        actual=subprocess.check_output(['docker','exec',self.prom_cid,'/bin/cat','/etc/prometheus/owned.json'],timeout=5);check(actual==(self.work/'prometheus.json').read_bytes(),'actual readonly source bytes differ')
        check('host.docker.internal:'+str(EXPORT_PORT) in json.loads(raw)['data']['yaml'],'loaded exact local exporter missing')
        ro._private(self.work/'prom-proof.json',{'inspect':info,'loaded_status':json.loads(raw),'config_sha':hashlib.sha256(actual).hexdigest(),'scrapes':self.fixture.scrapes,'synthetic_current_data_only':True})
        # Ensure a 30-second query grid has at least one actual scrape in range.
        self.source_started=time.monotonic();self.marker_case('generation.real_source',{'cid':self.prom_cid,'image_id':PROM_IMAGE_ID,'cpu':.5,'memory':268435456,'nonroot_readonly':True,'current_scrapes':self.fixture.scrapes,'history_fabricated':False})
    def body(self,suffix,env='test',view='request_rate',range_='15m'):
        return {'service':self.apps[env]['app'],'view':view,'range':range_,'question':'Describe only observed UUID '+self.nonce+' metric '+suffix+'.','request_id':'metrics-'+self.nonce+'-'+suffix}
    def ledger(self):
        tables=('v3_metrics_questions','v3_conversations','v3_conversation_messages','v3_conversation_replies','v3_conversation_events','management_audit')
        return {t:json.loads(self.sql("SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+t+' t',read=True)) for t in tables}
    def wait_reply(self,conv,state='completed',deadline=None):
        deadline=deadline or time.monotonic()+15
        while time.monotonic()<deadline:
            raw=self.sql('SELECT COALESCE(json_agg(to_jsonb(r)),\'[]\'::json) FROM v3_conversation_replies r WHERE conversation_id='+q(conv),read=True);rows=json.loads(raw)
            if len(rows)==1 and rows[0]['state'] in ('completed','failed'):
                check(rows[0]['state']==state,'background model state differs');return rows[0]
            time.sleep(.15)
        raise Blocked('held background model did not reach exact durable terminal state')
    def detail(self,user,conv,env='test',state='completed',index=1):
        def validate(v):
            check(v['id']==conv and v['scope']['env']==env and v['scope']['service']==self.apps[env]['app'],'conversation detail identity/env differs');messages=v['messages'];check(len(messages)==2 and {m['role'] for m in messages}=={'user','assistant'},'durable message pair differs')
            assistant=next(m for m in messages if m['role']=='assistant');check(assistant.get('state')==state,'detail terminal state differs')
            text=json.dumps(assistant['parts']);check((self.model_answer in text) if state=='completed' else ('Model reply failed' in text),'exact protocol answer/failure status missing')
            raw=json.dumps(v);check(not any(x in raw for x in ('private_label','untrusted-source-data',self.model_token,*self.apps[env]['expressions'].values())),'detail exposed private provider data')
        return self.call(user,'GET','/api/v1/conversations/'+conv,environment=env,index=index,scenario='fixture',validate=validate)[0]
    def sse(self,user,conv,env='test',want=200):
        path='/api/v1/conversations/'+conv+'/events';canonical,route_class=self.contract.canonical('GET',path);self.calls+=1;number=self.calls
        row={'name':'generation.http.'+str(number),'method':'GET','path':canonical,'route_class':route_class,'scenario':'normal' if want==200 else 'authorization','expected':{'status':[want]},'run_id':self.run_id,'call_id':self.run_id+'-'+str(number),'test_name':'FormalRuleGenerationSSE','registration_evidence':False}
        try:
            status,raw,headers=ro.transport(BASES[1],'GET',path,None,{'Cookie':self.cookies[user],'X-AIOps-Environment':env});row['actual']={'status':status};check(status==want,'actual SSE status differs')
            if status==200:
                text=raw.decode();events=[json.loads(line[len('data: '):]) for line in text.splitlines() if line.startswith('data: ')];check(events and events[0]['type']=='message.started' and events[-1]['type']=='message.completed','durable SSE start/terminal missing');ro._private(self.work/('sse-'+conv+'.json'),{'headers':headers,'raw':text,'events':events})
            row['status']='passed';ro.append_json(self.work/'interfaces.jsonl',row);emit(row['name'],'passed',row['actual'],interface=row)
        except BaseException as e:
            row['status']='failed';row['actual']=dict(row.get('actual',{}),assertion=str(e));self.first_red(row);emit(row['name'],'failed',row['actual'],interface=row);raise
    def ask(self,body,user='reader',env='test',index=0,scenario='normal',wait=True,model_state='completed'):
        holder={}
        def validate(v):check(set(v)=={'conversation_id'} and isinstance(v['conversation_id'],str) and v['conversation_id'],'exact accepted conversation ID required');holder['id']=v['conversation_id']
        value,headers=self.call(user,'POST','/api/v1/metrics/ask',body,environment=env,index=index,want=201,scenario=scenario,validate=validate);conv=value['conversation_id'];self.conversations.append(conv)
        check(next((v for k,v in headers.items() if k.lower()=='cache-control'),'')=='no-store','ask cache contract differs')
        if wait:self.wait_reply(conv,model_state);self.detail(user,conv,env,model_state,index=1);self.sse(user,conv,env)
        return conv
    def normal(self):
        # A current, sparse source can still prove observed samples; never a 7-day history.
        deadline=time.monotonic()+35
        while time.monotonic()<deadline:
            now=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0);path='/api/v1/query_range?'+urlencode({'query':self.apps['test']['expressions']['request_rate'],'start':(now-datetime.timedelta(minutes=15)).isoformat(),'end':now.isoformat(),'step':'30','limit':'100','timeout':'5s'})
            status,raw=prom_get(path);ro._private(self.work/'initial-grid-probe.json',{'status':status,'path':path,'response':json.loads(raw)})
            if status==200 and json.loads(raw)['data']['result'] and json.loads(raw)['data']['result'][0]['values']:break
            time.sleep(.25)
        else:raise Blocked('actual current nonempty query grid missing')
        self.accepted_body=self.body('normal');self.accepted=self.ask(self.accepted_body)
        self.ask(self.body('p95','test','http_p95_seconds','1h'),index=1)
        self.ask(self.body('staging','staging'),user='hidden',env='staging',index=1)
        rows=self.fixture.snapshot()['source_reads'];check(len(rows)==3 and all(r['response']['data']['result'] for r in rows),'three actual bound nonempty engine reads required')
        for r in rows:
            metric=r['response']['data']['result'][0]['metric'];check(metric['application']==self.apps[r['environment']]['app'] and metric['environment']==r['environment'] and metric['fixture']==self.nonce,'actual engine sample lineage differs')
        self.marker_case('generation.completed_question',{'source':'owned real Prometheus current samples','model':'fixed HTTP protocol Mock','conversations':3,'test_staging':True,'request_rate_p95':True,'ranges':['15m','1h'],'real_inference':False})
    def idempotency(self):
        before=self.ledger();source=self.fixture.snapshot();self.call('reader','POST','/api/v1/metrics/ask',self.accepted_body,index=1,scenario='idempotency',validate=lambda v:check(v=={'conversation_id':self.accepted},'cross-instance replay differs'))
        conflict=dict(self.accepted_body,question='Changed explicit question');self.call('reader','POST','/api/v1/metrics/ask',conflict,want=409,code='request_conflict',scenario='idempotency')
        check(before==self.ledger() and self.fixture.snapshot()['source_reads']==source['source_reads'] and self.fixture.snapshot()['model_reads']==source['model_reads'],'replay/conflict created IO or DB mutation')
        self.marker_case('generation.idempotency',{'cross_instance_same_id':True,'source_model_audit_delta':0,'changed_payload409':True})
    def authorization(self):
        before=self.ledger();counts=self.fixture.snapshot()
        for user,status in ((None,401),('denied',403),('peer',403)):
            self.call(user,'POST','/api/v1/metrics/ask',self.body('auth-'+str(user)),want=status,code='unauthorized' if status==401 else 'forbidden',scenario='authorization')
        self.call('reader','POST','/api/v1/metrics/ask',self.body('foreign','staging'),environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('peer','GET','/api/v1/conversations/'+self.accepted,want=404,code='not_found',scenario='authorization')
        check(self.ledger()==before and self.fixture.snapshot()['source_reads']==counts['source_reads'] and self.fixture.snapshot()['model_reads']==counts['model_reads'],'unauthorized mutation/provider IO')
        self.marker_case('generation.authorization',{'anonymous401':True,'denied403':True,'missing_change403':True,'cross_env403':True,'foreign_owner404':True,'provider_calls':0})
    def boundaries(self):
        before=self.ledger();counts=self.fixture.snapshot()
        for changes in ({'service':''},{'view':'cpu'},{'range':'7d'},{'question':''},{'question':' leading'},{'question':'x'*4001},{'request_id':'short'},{'request_id':'x'*129}):
            self.call('reader','POST','/api/v1/metrics/ask',dict(self.body('boundary'),**changes),want=422,code='invalid_metric_request',scenario='boundary')
        self.call('reader','POST','/api/v1/metrics/ask',dict(self.body('unknownfield'),unknown=True),want=422,code='invalid_body',scenario='boundary')
        self.call('admin','POST','/api/v1/metrics/ask',self.body('all'),environment='all',want=409,code='env_view_only',scenario='boundary')
        self.call('reader','POST','/api/v1/metrics/ask',dict(self.body('unknownapp'),service='unknown-'+self.nonce),want=503,code='not_configured',scenario='boundary')
        check(before==self.ledger() and counts['source_reads']==self.fixture.snapshot()['source_reads'],'boundary created result or source reads')
        self.marker_case('generation.boundaries',{'request_id_16_128':True,'question_max4000':True,'view_range_allowlist':True,'all_public_middleware409':True,'unknownapp503':True,'zero_source':True})
    def source_faults(self):
        for mode in ('503','429','half_response','response_loss','timeout','empty','warnings'):
            before=self.ledger();counts=self.fixture.snapshot();started=time.monotonic();self.fixture.prom_mode=mode
            try:self.call('reader','POST','/api/v1/metrics/ask',self.body('source-'+mode),want=503,code='metric_result_incomplete' if mode=='empty' else 'metric_query_unavailable',scenario='failure')
            finally:self.fixture.prom_mode='normal'
            after=self.fixture.snapshot();check(self.ledger()==before and len(after['source_reads'])==len(counts['source_reads'])+1 and after['model_reads']==counts['model_reads'],'source failure retried/created a conversation/model')
            ro.append_json(self.work/'source-fault-times.jsonl',{'mode':mode,'elapsed':time.monotonic()-started,'requests':1})
            self.ask(self.body('recovery-'+mode),index=1,scenario='failure')
        self.marker_case('generation.source_faults',{'modes':['503','429','partial_response','response_loss','actual5s_timeout','empty','warnings'],'one_source_attempt':True,'no_conversation_on_failure':True,'other_instance_original_request_shape_recovered':True})
    def model_faults_start_timeout(self):
        self.fixture.model_mode='timeout';self.timeout_conv=self.ask(self.body('model-timeout'),scenario='failure',wait=False);check(self.fixture.timeout_seen.wait(5),'actual model timeout request not reached');self.timeout_started=time.monotonic()
        for mode in ('503','429','response_loss','truncated'):
            self.fixture.model_mode=mode;count=len(self.fixture.snapshot()['model_reads']);conv=self.ask(self.body('model-'+mode),scenario='failure',model_state='failed');check(len(self.fixture.snapshot()['model_reads'])==count+1,'model failed request automatically retried')
            self.ask(self.body('model-recovery-'+mode),index=1,scenario='failure')
    def inflight(self):
        self.fixture.seen.clear();self.fixture.release.clear();self.fixture.hold=True;errors=[];before=len(self.ledger()['v3_metrics_questions'])
        def request():
            try:self.call('reader','POST','/api/v1/metrics/ask',self.body('revoke'),want=403,code='forbidden',scenario='authorization')
            except BaseException as e:errors.append(e)
        t=threading.Thread(target=request);t.start()
        try:
            check(self.fixture.seen.wait(5),'owned source request not held');self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='connectors' AND action='view';")
            count=len(self.fixture.snapshot()['source_reads']);self.call('reader','POST','/api/v1/metrics/ask',self.body('after-revoke'),index=1,want=403,code='forbidden',scenario='authorization');check(len(self.fixture.snapshot()['source_reads'])==count,'new revoked request read provider')
        finally:self.fixture.release.set();t.join(12)
        check(not t.is_alive() and not errors and len(self.ledger()['v3_metrics_questions'])==before,'inflight revoke persisted question')
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles['reader'],'connectors','view','environment','test'))+');')
        self.ask(self.body('revocation-recovered'),index=1,scenario='failure');self.marker_case('generation.inflight_revocation',{'source_read_then403':True,'new_other_instance403':True,'result_not_accepted':True,'restored_recovery':True})
    def audit_rollback(self):
        before=self.ledger();c=len(self.fixture.snapshot()['model_reads']);actor=self.users['reader'];app=self.apps['test']['app']
        self.sql("CREATE FUNCTION generation_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id="+q(actor)+" AND NEW.changes->>'application_id'="+q(app)+" AND NEW.changes->>'request_id'="+q(self.body('audit-fault')['request_id'])+" AND NEW.action='v3.metrics.ask' THEN RAISE EXCEPTION 'Explicit UUID acceptance audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER generation_audit_fault BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION generation_audit_fault();")
        try:self.call('reader','POST','/api/v1/metrics/ask',self.body('audit-fault'),want=503,code='conversation_unavailable',scenario='failure')
        finally:self.sql('DROP TRIGGER generation_audit_fault ON management_audit; DROP FUNCTION generation_audit_fault();')
        check(before==self.ledger() and len(self.fixture.snapshot()['model_reads'])==c,'audit failure committed partial result/model')
        self.ask(self.body('audit-fault'),index=1,scenario='failure');self.marker_case('generation.audit_rollback',{'exact_actor_app_action_fault':True,'full_acceptance_rollback':True,'original_key_retry_succeeded':True})
    def finish_model_timeout(self):
        self.wait_reply(self.timeout_conv,'failed',deadline=self.timeout_started+255);elapsed=time.monotonic()-self.timeout_started;check(elapsed>=230,'model timeout was not actually awaited')
        self.detail('reader',self.timeout_conv,state='failed');self.sse('reader',self.timeout_conv);self.ask(self.body('model-timeout-recovered'),index=1,scenario='failure')
        ro._private(self.work/'model-timeout-proof.json',{'elapsed_seconds':elapsed,'actual_HTTP_default_timeout_seconds':240,'reply_state':'failed','fixture_handler_hold_seconds':241,'terminal_sse_not_model_success':True})
        self.marker_case('generation.model_faults',{'modes':['503','429','response_loss','truncated_SSE','actual4min_timeout'],'accepted201_then_durable_failed':True,'one_call_no_automatic_retry':True,'original_shape_recovery':True})
    def saved_source_revoke(self):
        row=self.apps['test'];self.call('admin','DELETE','/api/v1/services/'+row['sid']+'/external-targets/'+row['target'],want=204,scenario='fixture')
        count=len(self.fixture.snapshot()['source_reads']);self.call('reader','POST','/api/v1/metrics/ask',self.accepted_body,index=1,want=403,code='forbidden',scenario='authorization');self.call('reader','GET','/api/v1/conversations/'+self.accepted,want=403,code='forbidden',scenario='authorization');self.sse('reader',self.accepted,want=403)
        check(len(self.fixture.snapshot()['source_reads'])==count,'saved source revocation read upstream');self.marker_case('generation.saved_source_revocation',{'public_target_delete':True,'saved_replay_detail_SSE403':True,'no_upstream':True})
    def prepare_rules(self):
        with socket.socket() as sock:
            if sock.connect_ex(('127.0.0.1',443))==0:
                result=subprocess.run(['docker','ps','--filter','publish=443','--format','{{.ID}} {{.Names}} {{.Ports}}'],capture_output=True,text=True,timeout=5)
                ro._private(self.work/'https443-occupied.json',{'scope':'read-only occupancy; no container signal/adoption','listen':'127.0.0.1:443','actual_published_containers':result.stdout.splitlines(),'exit':result.returncode})
                raise Blocked('loopback443 already occupied by existing non-invocation container; no adoption/stop/reconfiguration')
        addresses=socket.gethostbyname_ex(self.tls_hostname)[2];check(set(addresses)=={'127.0.0.1'},'exact fixture DNS must actually resolve only owned loopback')
        cert,key=self.work/'tls-cert.pem',self.work/'tls-key.pem'
        r=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','2','-subj','/CN='+self.tls_hostname,'-addext','subjectAltName=DNS:'+self.tls_hostname,'-addext','basicConstraints=critical,CA:TRUE','-keyout',str(key),'-out',str(cert)],capture_output=True,timeout=20,umask=0o077);check(r.returncode==0,'private ephemeral TLS certificate creation failed')
        for f in (cert,key):os.chmod(f,0o600)
        self.rule_fixture=RuleFixture(self);preview={'sources':[]};models={'environments':{}}
        for env,row in self.apps.items():
            row['rule_query']='testlab_generation_rate{application="'+row['app']+'",environment="'+env+'",service="owned-metrics"}'
            preview['sources'].append({'environment_id':env,'application_id':row['app'],'service':'owned-metrics','query_id':'owned_rate','query':row['rule_query'],'unit':'requests/s','source':'owned-history-'+self.nonce+'-'+env,'endpoint':'https://'+self.tls_hostname+'/preview/'+env,'api_key_env':'TESTLAB_RULE_PREVIEW_'+env.upper(),'isolation_mode':'application_endpoint','label_keys':{'application':'application','environment':'environment','service':'service'}})
            models['environments'][env]={'provider':'openai-compatible','model':'owned-rule-model','endpoint':'https://'+self.tls_hostname+'/model/'+env+'/chat/completions','api_key_env':'TESTLAB_RULE_MODEL_'+env.upper(),'max_input_bytes':8192,'max_output_tokens':128,'max_cost_micro_usd':10000,'input_price_micro_usd_per_million':1000000,'output_price_micro_usd_per_million':1000000}
        ro._private(self.work/'preview-config.json',preview);ro._private(self.work/'draft-config.json',models)
        bridge='''import socket,socketserver,select
class Handler(socketserver.BaseRequestHandler):
 def handle(self):
  try:
   upstream=socket.create_connection(('host.docker.internal',18658),timeout=5)
   with upstream:
    while True:
     ready,_,_=select.select([self.request,upstream],[],[],35)
     if not ready:return
     for source in ready:
      data=source.recv(65536)
      if not data:return
      (upstream if source is self.request else self.request).sendall(data)
  except OSError:pass
class Server(socketserver.ThreadingTCPServer):
 allow_reuse_address=False
 daemon_threads=True
with Server(('0.0.0.0',8443),Handler) as server:server.serve_forever()
''';ro.private_file(self.work/'tls-bridge.py',bridge)
        for n in ('tls-cert.pem','tls-key.pem','preview-config.json','draft-config.json','tls-bridge.py'):self.config_hashes[n]=hashlib.sha256((self.work/n).read_bytes()).hexdigest()
        check(subprocess.run(['docker','image','inspect',TLS_IMAGE],capture_output=True,timeout=8).returncode==0,'exact already local pinned Python image unavailable; no pull')
        check(subprocess.run(['docker','inspect',self.tls_name],capture_output=True,timeout=5).returncode!=0,'UUID TLS bridge name exists; no adoption');self.tls_attempted=True
        cmd=['docker','create','--pull','never','--name',self.tls_name,'--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-generation-tls','--cpus','.25','--memory','128m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),'-p','127.0.0.1:443:8443','--mount','type=bind,source='+str(self.work/'tls-bridge.py')+',target=/tls-bridge.py,readonly','--entrypoint','python',TLS_IMAGE,'-B','/tls-bridge.py']
        r=subprocess.run(cmd,capture_output=True,text=True,timeout=15);ro._private(self.work/'tls-create.json',{'command':cmd,'exit':r.returncode,'stdout':r.stdout,'stderr':r.stderr});check(r.returncode==0 and re.fullmatch('[a-f0-9]{64}',r.stdout.strip()),'TLS bridge create outcome unconfirmed');self.tls_cid=r.stdout.strip();guard_tls(ro.inspect_pg(self.tls_cid),self,live=False);check(subprocess.run(['docker','start',self.tls_cid],capture_output=True,timeout=15).returncode==0,'owned TLS bridge start failed')
        deadline=time.monotonic()+20;ctx=ssl.create_default_context(cafile=str(cert))
        while time.monotonic()<deadline:
            guard_tls(ro.inspect_pg(self.tls_cid),self)
            try:
                conn=http.client.HTTPSConnection(self.tls_hostname,443,context=ctx,timeout=3);conn.connect();peer=conn.sock.getpeercert(binary_form=True);conn.request('GET','/healthz/'+self.nonce);r=conn.getresponse();raw=r.read(4096);conn.close()
                if r.status==200 and json.loads(raw)=={'run_id':self.run_id}:break
            except OSError:pass
            time.sleep(.2)
        else:raise Blocked('owned DNS HTTPS443 bridge actual TLS readiness unavailable')
        check(peer==ssl.PEM_cert_to_DER_cert(cert.read_text()),'exact server certificate did not match owned source')
        ro._private(self.work/'tls-proof.json',{'cid':self.tls_cid,'inspect':ro.inspect_pg(self.tls_cid),'hostname':self.tls_hostname,'actual_dns':addresses,'server_cert_sha':hashlib.sha256(peer).hexdigest(),'loopback_publish':443,'tls_termination':'held host fixture at18658; container forwards opaque bytes only to its exact host.docker.internal:18658','CA_installed_systemwide':False,'HTTP_PROXY_used':False})
        self.restart();self.marker_case('generation.rules_tls_source',{'owned_dns_resolves_loopback':True,'docker_nonroot_loopback443':True,'exact_CA_TLS_proof':True,'source':'explicit synthetic7day protocol Mock','real_7day_history':False})
    def rule_body(self,kind,env='test'):
        row=self.apps[env]
        return {'application_id':row['app'],'text':'Prepare an observed rate draft'} if kind=='model' else {'application_id':row['app'],'query':row['rule_query'],'op':'>','threshold':2,'for_minutes':0,'range':'1h'}
    def rule_ledger(self):
        return {t:json.loads(self.sql("SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+t+' t',read=True)) for t in ('v3_rule_generate_attempts','v3_rule_generate_budget','v3_rule_preview_budget','management_audit')}
    def rule_call(self,kind,key='normal',env='test',user=None,index=0,scenario='normal'):
        user=user or ('hidden' if env=='staging' else 'reader');body=self.rule_body(kind,env)
        def validate(v):
            if kind=='model':check(v['query']==self.apps[env]['rule_query'] and v['application_id']==self.apps[env]['app'] and v['environment_id']==env and v['status']=='review_required' and v['query_id']=='owned_rate' and v['unit']=='requests/s' and v['model']=={'provider':'openai-compatible','name':'owned-rule-model'} and v['op']=='>' and v['threshold']==2 and v['for_minutes']==0 and v['severity']=='P2' and v['draft_id'],'exact review-only fixed draft differs')
            else:check(v['valid'] is True and v['application_id']==self.apps[env]['app'] and v['environment_id']==env and v['series_count']==1 and v['coverage']=='sampled_5m' and v['range']=='1h' and v['values']==[2.5]*13 and len(v['points'])==13 and v['current']==2.5 and v['fires'] is None and v['route'] is None and v['route_status']=='unknown' and v['similar'] is None and v['similar_status']=='unknown' and v['backtest']['count'] is None and v['backtest']['avg_minutes'] is None and v['backtest']['max_minutes'] is None and v['backtest']['step_seconds']==300 and v['backtest']['sampled_count']==1,'sampled preview invented history/route/event counts')
        return self.call(user,'POST','/api/v1/alert-rules/'+('generate' if kind=='model' else 'preview'),body,headers={'Idempotency-Key':'rules-'+self.nonce+'-'+key} if kind=='model' else None,environment=env,index=index,scenario=scenario,validate=validate)
    def wait_budget(self,kind,user='reader',env='test'):
        table='v3_rule_generate_budget' if kind=='model' else 'v3_rule_preview_budget'
        deadline=time.monotonic()+11
        while time.monotonic()<deadline:
            ready=self.sql('SELECT COALESCE((SELECT next_allowed_at<=clock_timestamp() FROM '+table+' WHERE actor_id='+q(self.users[user])+" AND application_id="+q(self.apps[env]['app'])+' AND environment_id='+q(env)+'),true)',read=True)
            if ready=='t':return
            time.sleep(.3)
        raise Blocked('actual10s rule budget did not expire; no SQL budget reset')
    def rules_run(self):
        self.prepare_rules();first,headers=self.rule_call('model');self.rule_call('preview');self.rule_call('model','staging',env='staging',index=1);self.rule_call('preview',env='staging',index=1)
        before=self.rule_ledger();source=self.rule_fixture.snapshot();again,_=self.rule_call('model',index=1,scenario='idempotency');check(first==again and before==self.rule_ledger() and source==self.rule_fixture.snapshot(),'successful draft replay changed source/DB')
        self.call('reader','POST','/api/v1/alert-rules/generate',dict(self.rule_body('model'),text='Changed review request'),headers={'Idempotency-Key':'rules-'+self.nonce+'-normal'},want=409,code='idempotency_conflict',scenario='idempotency')
        self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body('model'),headers={'Idempotency-Key':'rules-'+self.nonce+'-throttled'},want=429,code='rule_generate_rate_limited',scenario='boundary');self.call('reader','POST','/api/v1/alert-rules/preview',self.rule_body('preview'),want=429,code='rule_preview_rate_limited',scenario='boundary')
        self.marker_case('generation.rules_normal',{'normal_routes':list(TARGETS[:2]),'source_model_mode':'protocol_mock','synthetic_2017_5m_points':True,'public_requested_range_points':13,'test_staging':True,'draft_review_required':True,'exact_saved_replay':True,'real_history':False})
        for kind,path in (('model','generate'),('preview','preview')):
            body=self.rule_body(kind);headers={'Idempotency-Key':'rules-'+self.nonce+'-auth'} if kind=='model' else None;before=self.rule_ledger();source=self.rule_fixture.snapshot()
            for user,status in ((None,401),('denied',403)):
                self.call(user,'POST','/api/v1/alert-rules/'+path,body,headers=headers,want=status,code='unauthorized' if status==401 else 'forbidden',scenario='authorization')
            self.call('reader','POST','/api/v1/alert-rules/'+path,body,headers=headers,environment='staging',want=403,code='forbidden',scenario='authorization');self.call('admin','POST','/api/v1/alert-rules/'+path,body,headers=headers,environment='all',want=409,code='env_view_only',scenario='boundary')
            check(before==self.rule_ledger() and source==self.rule_fixture.snapshot(),'rule authorization changed source/audit')
        self.marker_case('generation.rules_authorization',{'anonymous401':True,'no_grant_cross_env403':True,'all409':True,'no_provider':True})
        for changes in ({'text':''},{'text':'x'*2049},{'application_id':''}):self.call('reader','POST','/api/v1/alert-rules/generate',dict(self.rule_body('model'),**changes),headers={'Idempotency-Key':'rules-'+self.nonce+'-invalid'},want=422,code='invalid_rule_description',scenario='boundary')
        self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body('model'),want=422,code='idempotency_key_required',scenario='boundary')
        for changes in ({'query':''},{'op':'='},{'threshold':1e10},{'for_minutes':61},{'range':'2h'}):self.call('reader','POST','/api/v1/alert-rules/preview',dict(self.rule_body('preview'),**changes),want=422,code='invalid_rule_preview',scenario='boundary')
        self.call('reader','POST','/api/v1/alert-rules/preview',dict(self.rule_body('preview'),query='up'),want=422,code='unreviewed_query',scenario='boundary')
        self.marker_case('generation.rules_boundaries',{'strict_description_idempotency':True,'condition_range_limits':True,'unreviewed_query422':True})
        for kind,modes,path,code in (('model',('response_loss','timeout','invalid_query'),'generate','rule_generate_ambiguous'),('preview',('503','response_loss','timeout','partial_matrix','wrong_scope'),'preview','rule_preview_upstream_unavailable')):
            for mode in modes:
                self.wait_budget(kind);self.rule_fixture.mode[kind]=mode;count=len(self.rule_fixture.snapshot());key='fault-'+mode;started=time.monotonic()
                try:
                    _,h=self.call('reader','POST','/api/v1/alert-rules/'+path,self.rule_body(kind),headers={'Idempotency-Key':'rules-'+self.nonce+'-'+key} if kind=='model' else None,want=502 if kind=='model' else 503,code=code,scenario='failure')
                finally:self.rule_fixture.mode[kind]='normal'
                check(len(self.rule_fixture.snapshot())==count+1,'rule provider fault automatically retried');ro.append_json(self.work/'rule-fault-times.jsonl',{'kind':kind,'mode':mode,'elapsed':time.monotonic()-started,'requests':1})
                if kind=='model':
                    before=self.rule_ledger();self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body(kind),headers={'Idempotency-Key':'rules-'+self.nonce+'-'+key},want=502,code=code,scenario='idempotency');check(len(self.rule_fixture.snapshot())==count+1 and before==self.rule_ledger(),'ambiguous replay retried provider or changed ledger')
                self.wait_budget(kind);self.rule_call(kind,'recovery-'+mode,index=1,scenario='failure')
        self.marker_case('generation.rules_provider_faults',{'model':['response_loss','actual15s_header_timeout','unreviewed_model_query'],'preview':['503','response_loss','actual10s_header_timeout','partial_matrix','wrong_scope'],'one_provider_call_no_retry':True,'ambiguous_saved_replay':True,'other_instance_recovery':True,'synthetic_history_protocol_only':True})
        rows=self.rule_ledger();check(all(r['status'] in ('succeeded','ambiguous') for r in rows['v3_rule_generate_attempts']),'draft state source ledger inconsistent');ro._private(self.work/'rules-db-readback.json',rows);ro._private(self.work/'rules-source-receipts.json',self.rule_fixture.snapshot())
        result={'run_id':self.run_id,'scope':'rules','http_calls':self.calls,'normal_routes':[list(x) for x in TARGETS[:2]],'evidence_mode':'protocol_mock','real_https443_dns':True,'real_7day_history':False,'synthetic_7day_protocol':True,'model_protocol_mock':True,'unverified':['actual7day production history','real inference/paid capacity','draft enabling/GitLab MR/deploy','natural session expiry']};ro._private(self.work/'result.json',result);return result
    def session_revoke(self):
        self.fixture.seen.clear();self.fixture.release.clear();self.fixture.hold=True;errors=[];before=len(self.ledger()['v3_metrics_questions'])
        def request():
            try:self.call('hidden','POST','/api/v1/metrics/ask',self.body('session','staging'),environment='staging',want=401,code='unauthorized',scenario='authorization')
            except BaseException as e:errors.append(e)
        t=threading.Thread(target=request);t.start()
        try:check(self.fixture.seen.wait(5),'session request not held');self.sql('DELETE FROM sessions WHERE user_id='+q(self.users['hidden'])+';')
        finally:self.fixture.release.set();t.join(12)
        check(not t.is_alive() and not errors and len(self.ledger()['v3_metrics_questions'])==before,'revoked session accepted metric result')
        self.call('hidden','POST','/api/v1/metrics/ask',self.body('session-new','staging'),environment='staging',index=1,want=401,code='unauthorized',scenario='authorization');self.marker_case('generation.session_revocation',{'inflight_after_source401':True,'new_other_instance401':True,'no_result':True})
    def rules_prerequisites(self,blocker=None):
        before=self.ledger();app=self.apps['test']['app'];generate={'text':'Describe an alert from observed metrics','application_id':app};preview={'application_id':app,'query':self.apps['test']['expressions']['request_rate'],'op':'>','threshold':2,'for_minutes':0,'range':'1h'}
        for path,body in (('/api/v1/alert-rules/generate',generate),('/api/v1/alert-rules/preview',preview)):
            headers={'Idempotency-Key':'rules-'+self.nonce} if path.endswith('generate') else None
            self.call('reader','POST',path,body,headers=headers,want=503,code='rule_assistant_not_connected',scenario='failure')
            for user,status in ((None,401),('denied',403)):
                self.call(user,'POST',path,body,headers=headers,want=status,code='unauthorized' if status==401 else 'forbidden',scenario='authorization')
            self.call('reader','POST',path,body,headers=headers,environment='staging',want=403,code='forbidden',scenario='authorization')
            self.call('admin','POST',path,body,headers=headers,environment='all',want=409,code='env_view_only',scenario='boundary')
        for body in (dict(generate,text=''),dict(generate,text='x'*2049),dict(generate,application_id='')):
            self.call('reader','POST','/api/v1/alert-rules/generate',body,headers={'Idempotency-Key':'rules-'+self.nonce},want=422,code='invalid_rule_description',scenario='boundary')
        self.call('reader','POST','/api/v1/alert-rules/generate',generate,want=422,code='idempotency_key_required',scenario='boundary')
        for changes in ({'query':''},{'op':'='},{'threshold':1e10},{'for_minutes':61},{'range':'2h'}):
            self.call('reader','POST','/api/v1/alert-rules/preview',dict(preview,**changes),want=422,code='invalid_rule_preview',scenario='boundary')
        check(before==self.ledger(),'unconfigured assistant created conversation/audit')
        probes={}
        with socket.socket() as sock:
            try:sock.bind(('127.0.0.1',443));probes['direct_loopback443_bind']='bind possible but this unassigned privileged listener is not adopted'
            except OSError as error:probes['direct_loopback443_bind']={'type':type(error).__name__,'errno':error.errno,'uid':os.getuid()}
        for hostname in ('127.0.0.1.sslip.io','127.0.0.2.nip.io'):
            try:probes[hostname]=socket.gethostbyname_ex(hostname)[2]
            except OSError as error:probes[hostname]={'type':type(error).__name__}
        probes['owned_uuid_localhost']=socket.gethostbyname_ex(self.tls_hostname)[2]
        occupied=self.work/'https443-occupied.json'
        reasons={'required':'own free loopback443 for pinned Docker forwarder, exact UUID.localhost DNS and temporary CA TLS; explicitly synthetic 7day protocol matrix is authorized','available_scope':'additional own443 permitted, but existing non-invocation docker-nginx-1 publishes0.0.0.0:443; it is preserved','blocker':blocker,'observed':dict(probes,configured_rule_catalog=False,occupied_container=json.loads(occupied.read_text()) if occupied.exists() else None),'source_refs':['internal/v3ruleassistant/preview_reader.go:164','internal/v3ruleassistant/preview_reader.go:140','internal/v3ruleassistant/generate_adapter.go:136','internal/v3ruleassistant/preview.go:180'],'normal_provider_failure_timeout_response_loss_executable':False,'no_fabricated_history_or_success':True,'HTTP_PROXY_connect_possible':False,'tls_CA_trust_in_Go_unverified_because_no_owned_listener_created':True}
        ro._private(self.work/'rule-prerequisites.json',reasons)
        for method,path in TARGETS[:2]:self.marker_case('generation.blocked.'+path.rsplit('/',1)[1],dict(reasons,method=method,path='/api/v1'+path),'blocked')
        result={'scope':'rule-prerequisites','blocked_targets':list(TARGETS[:2]),'http_calls':self.calls,'normal_routes':[],'blocker':reasons};ro._private(self.work/'result.json',result);return result
    def run(self):
        self.provision();self.start();self.seed()
        for n in ('admin','reader','peer','hidden','denied'):self.login(n)
        self.prepare_apps()
        if self.scope=='rules':
            try:return self.rules_run()
            except Blocked as error:
                if (self.work/'https443-occupied.json').exists():return self.rules_prerequisites(str(error))
                raise
        self.prepare_source();self.normal();self.idempotency();self.authorization();self.boundaries();self.source_faults();self.inflight();self.audit_rollback();self.model_faults_start_timeout();self.finish_model_timeout();self.session_revoke();self.saved_source_revoke()
        rows=self.ledger();questions=rows['v3_metrics_questions'];check(len(questions)==len(self.conversations)==len(set(self.conversations)),'accepted question lineage count differs')
        check(len(rows['v3_conversations'])==len(questions) and len(rows['v3_conversation_messages'])==2*len(questions) and len(rows['v3_conversation_replies'])==len(questions),'question/conversation/message/reply consistency differs')
        for r in questions:check(r['owner_id'] in self.users.values() and r['environment_id'] in self.apps,'accepted source/actor outside UUID')
        ro._private(self.work/'db-readback.json',rows);ro._private(self.work/'source-model-receipts.json',self.fixture.snapshot())
        self.marker_case('generation.consistency',{'questions':len(questions),'messages':2*len(questions),'no_rule_create_enable_deploy':True,'remote_writes':0,'real_inference':False})
        result={'run_id':self.run_id,'scope':'metrics','http_calls':self.calls,'normal_routes':[['POST','/metrics/ask']],'evidence_mode':'protocol_mock','real_source':'owned current Prometheus exact bound samples','fixed_model_mock':True,'required_cases':list(REQUIRED_CASES),'unverified':['alert-rules/generate full normal and provider faults','alert-rules/preview full normal and provider faults','owned HTTPS443 DNS authority','7day complete source history','real model inference','paid/Ollama capacity','natural session expiry','production sources']};ro._private(self.work/'result.json',result);return result
    def cleanup(self):
        errors=[]
        try:super().cleanup()
        except BaseException as e:errors.append(e)
        if self.prom_cid:
            try:
                info=ro.inspect_pg(self.prom_cid);guard_prom(info,self,live=bool(info.get('State',{}).get('Running')))
                if info['State']['Running']:check(subprocess.run(['docker','stop','--time','3',self.prom_cid],capture_output=True,timeout=10).returncode==0,'exact owned source stop failed')
                guard_prom(ro.inspect_pg(self.prom_cid),self,live=False);check(subprocess.run(['docker','rm',self.prom_cid],capture_output=True,timeout=10).returncode==0,'exact owned source remove failed');self.prom_cid=None
                check(subprocess.run(['docker','inspect',self.prom_name],capture_output=True,timeout=5).returncode!=0,'owned source name remains')
            except BaseException as e:errors.append(e)
        elif self.prom_attempted and subprocess.run(['docker','inspect',self.prom_name],capture_output=True,timeout=5).returncode==0:errors.append(Blocked('unknown creation outcome; exact UUID identity recovery required'))
        if self.tls_cid:
            try:
                info=ro.inspect_pg(self.tls_cid);guard_tls(info,self,live=bool(info.get('State',{}).get('Running')))
                if info['State']['Running']:check(subprocess.run(['docker','stop','--time','3',self.tls_cid],capture_output=True,timeout=10).returncode==0,'exact TLS stop failed')
                guard_tls(ro.inspect_pg(self.tls_cid),self,live=False);check(subprocess.run(['docker','rm',self.tls_cid],capture_output=True,timeout=10).returncode==0,'exact TLS removal failed');self.tls_cid=None
                check(subprocess.run(['docker','inspect',self.tls_name],capture_output=True,timeout=5).returncode!=0,'exact TLS name remains')
            except BaseException as e:errors.append(e)
        elif self.tls_attempted and subprocess.run(['docker','inspect',self.tls_name],capture_output=True,timeout=5).returncode==0:errors.append(Blocked('unconfirmed TLS create identity; no adoption'))
        if self.rule_fixture:
            try:self.rule_fixture.close()
            except BaseException as e:errors.append(e)
        if self.fixture:
            try:self.fixture.close()
            except BaseException as e:errors.append(e)
        absence={}
        for port in (*PORTS,PROM_PORT,EXPORT_PORT,FIXTURE_PORT,*((443,) if self.tls_attempted else ())):
            with socket.socket() as s:absence[str(port)]=s.connect_ex(('127.0.0.1',port))!=0
        receipt={'ports_absent':absence,'owned_container_absent':self.prom_cid is None and self.tls_cid is None,'held_process_exits':[p.poll() for p in self.processes],'errors':[str(e) for e in errors],'saved_pids_signaled':False,'private_tmpfs_retired':self.prom_cid is None};ro._private(self.work/'generation-cleanup.json',receipt)
        if errors or not all(absence.values()):raise Blocked('generation cleanup incomplete; private exact evidence retained')
        emit('generation.cleanup','passed',receipt)

@contextlib.contextmanager
def lease(root):
    p=Path(root)/'.local/testlab/rule-generation-parallel';p.mkdir(mode=0o700,parents=True,exist_ok=True);fd=os.open(p/'lease.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);yield
    finally:os.close(fd)

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--run-id');p.add_argument('--scope',choices=('metrics','rules'),default='metrics');m=p.add_mutually_exclusive_group(required=True);m.add_argument('--inventory',action='store_true');m.add_argument('--live',action='store_true');a=p.parse_args(argv)
    if a.inventory:
        Contract(a.root);print(json.dumps({'targets':TARGETS,'ports':[*PORTS,PROM_PORT,EXPORT_PORT,FIXTURE_PORT],'scope':a.scope,'model_evidence_mode':'protocol_mock','rules_need_owned_dns_https443':True,'offline_only':True}));return 0
    run_id=a.run_id or str(uuid.uuid4())
    if str(uuid.UUID(run_id))!=run_id:raise Blocked('fresh canonical UUID required')
    try:
        with lease(a.root),context(),ro.cooperative_signals():data=doctor(a.root);c=Chain(a.root,data,run_id,a.scope);ro.execute(c)
    except Blocked as e:emit('generation.environment','blocked',{'error':str(e)});return 2
    except Exception as e:emit('generation.failure','failed',{'type':type(e).__name__,'error':str(e)});return 1
    return 0

if __name__=='__main__':raise SystemExit(main())
