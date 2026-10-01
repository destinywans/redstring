"""Real UUID-owned Alertmanager route tree and read-only sample evaluation.

GET routing and POST /test both read configuration; /test does not notify or
write business/audit ledgers. An explicit local AM alert separately proves the
sole owned capture destination. Shared AM/Prom and delivery are untouched.
"""
from __future__ import annotations
import argparse, base64, contextlib, copy, datetime, fcntl, hashlib, http.client
import json, math, os, re, secrets, socket, stat, subprocess, threading, time, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit
import api_readonly_chain as readonly

ROOT=readonly.ROOT; OWNER=readonly.OWNER; MODULE='formal-api-notification-routes-functional'
PORTS=(18648,18649); BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
AM_PORT=18650; CAPTURE_PORT=18651; RELAY_PORT=18652
AM_IMAGE='prom/alertmanager:v0.34.1'
AM_IMAGE_ID='sha256:a9e72a5032ccb84a8855186b34db4a68bf1f4983d915a14fa35b0bfe7ff1f56b'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SOURCE_SHA='6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY_SHA='bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
AM_CONFIG='/etc/alertmanager/owned-routes.json'
TARGETS=(('GET','/notification-routes'),('POST','/notification-routes/test'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'),('GET','/alert-groups'))
REQUIRED_CASES=('routes.source_load','routes.local_capture','routes.native_ingress','routes.nonempty_tree',
 'routes.sample_paths','routes.authorization','routes.boundaries','routes.failure_recovery',
 'routes.inflight_revocation','routes.session_revocation','routes.readonly_consistency')
Blocked,check=readonly.Blocked,readonly.check; q=readonly.quote

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:
        fields['interface']=dict(fields['interface'],proof_scope='real owned AM current configuration; POST test is read-only route evaluation, zero delivery/write',test_name='FormalNotificationRoutesChain')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)

def hashes():
    return {f:hashlib.sha256((ROOT/'tools/testlab'/f).read_bytes()).hexdigest() for f in ('api_notification_routes_functional_coverage.py','api_readonly_chain.py')}

@contextlib.contextmanager
def context():
    old=readonly.PORTS,readonly.BASES,readonly.emit,readonly.transport
    readonly.PORTS,readonly.BASES,readonly.emit=PORTS,BASES,emit
    def transport(*args,**kwargs):
        kwargs.setdefault('timeout',20)
        return old[3](*args,**kwargs)
    readonly.transport=transport
    try:yield
    finally:readonly.PORTS,readonly.BASES,readonly.emit,readonly.transport=old

def ports_free():
    for port in (*PORTS,AM_PORT,CAPTURE_PORT,RELAY_PORT):
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1',port))==0:raise Blocked('owned notification port occupied: '+str(port))

def doctor(root):
    ports_free()
    with context():data=readonly.doctor(root)
    if data['source_tree_sha']!=SOURCE_SHA or data['binary_sha256']!=BINARY_SHA or readonly.inspect_pg(data['postgres_container'])['Id']!=PG_CID:
        raise Blocked('notification frozen Go/binary/exact PG CID mismatch')
    image=json.loads(subprocess.check_output(['docker','image','inspect',AM_IMAGE],text=True,timeout=8))
    if len(image)!=1 or image[0]['Id']!=AM_IMAGE_ID:raise Blocked('fixed local AM image missing/drift; no pull')
    return dict(data,helper_sha256=hashes())

class Contract(readonly.Contract):
    def canonical(self,method,path):
        p=urlsplit(path)
        if p.scheme or p.netloc or p.fragment or not p.path.startswith('/api/v1/'):raise Blocked('formal namespace required')
        short=p.path[len('/api/v1'):]
        matches=[r for m,r in (*TARGETS,*SUPPORT) if m==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(r)),short)]
        route=short if short in matches else matches[0] if len(matches)==1 else None
        if route is None or method.lower() not in self.api['paths'].get(route,{}):raise Blocked('outside bounded notification read/test contract')
        return '/api/v1'+route,'main' if (method,route) in self.main else 'auxiliary_or_historical'

def config_for(chain):
    names=chain.receiver_names
    destination='http://host.docker.internal:'+str(CAPTURE_PORT)+'/capture/'+chain.nonce
    route={'receiver':names[0],'group_by':['application','environment','alertname'],'group_wait':'1s','group_interval':'2s','repeat_interval':'1h',
     'routes':[{'matchers':['environment="test"'],'routes':[
       {'matchers':['application="'+chain.apps['test']['key']+'"','severity="P1"'],'receiver':names[1]},
       {'matchers':['application="'+chain.apps['test']['key']+'"','severity!="P1"'],'receiver':names[2]}]},
       {'matchers':['environment="staging"'],'receiver':names[3],
        'routes':[{'matchers':['application="'+chain.apps['staging']['key']+'"'],'receiver':names[3]}]}]}
    return {'global':{'resolve_timeout':'5m'},'route':route,'receivers':[{'name':n,'webhook_configs':[{'url':destination,'send_resolved':False,'http_config':{'follow_redirects':False}}]} for n in names]}

def validate_config(config,chain):
    check(config==config_for(chain),'exact owned source configuration differs')
    for receiver in config['receivers']:
        check(set(receiver)=={'name','webhook_configs'} and len(receiver['webhook_configs'])==1,'unsupported outbound channel')
        check(receiver['webhook_configs'][0]['url']=='http://host.docker.internal:'+str(CAPTURE_PORT)+'/capture/'+chain.nonce,'capture destination differs')

def guard_am(info,chain,live=True):
    labels={'aiops.testlab.owner':OWNER,'aiops.testlab.run':chain.run_id,'aiops.testlab.purpose':'notification-routes'}
    if (not isinstance(info,dict) or info.get('Name')!='/'+chain.am_name or info.get('Image')!=AM_IMAGE_ID or
        any(info.get('Config',{}).get('Labels',{}).get(k)!=v for k,v in labels.items()) or
        not re.fullmatch('[a-f0-9]{64}',str(info.get('Id',''))) or
        info.get('HostConfig',{}).get('NanoCpus')!=500000000 or info.get('HostConfig',{}).get('Memory')!=268435456 or
        info.get('HostConfig',{}).get('Privileged') is not False or info.get('HostConfig',{}).get('ReadonlyRootfs') is not True or
        info.get('HostConfig',{}).get('CapDrop')!=['ALL'] or
        info.get('HostConfig',{}).get('SecurityOpt')!=['no-new-privileges'] or
        info.get('Config',{}).get('User')!=str(os.getuid())+':'+str(os.getgid()) or
        os.getuid()==0 or
        (info.get('NetworkSettings',{}).get('Ports',{}) if info.get('State',{}).get('Running') else info.get('HostConfig',{}).get('PortBindings',{})).get('9093/tcp')!=[{'HostIp':'127.0.0.1','HostPort':str(AM_PORT)}] or
        (live and not info.get('State',{}).get('Running'))):raise Blocked('owned AM CID/image/resource/user/listener guard failed')
    if chain.am_cid and info['Id']!=chain.am_cid:raise Blocked('owned AM immutable CID drift')
    binds=[m for m in info.get('Mounts',[]) if m.get('Type')=='bind']
    if len(binds)!=1 or binds[0].get('Source')!=str(chain.work/'am-config.json') or binds[0].get('Destination')!=AM_CONFIG or binds[0].get('RW') is not False:
        raise Blocked('exact AM readonly bind mount mismatch')
    if any(m.get('Type')=='volume' for m in info.get('Mounts',[])):raise Blocked('unexpected AM volume')
    if info.get('HostConfig',{}).get('Tmpfs')!={'/alertmanager':chain.tmpfs}:raise Blocked('UUID private storage tmpfs differs')
    if info.get('Path')!='/bin/alertmanager' or info.get('Args')!=chain.am_args:raise Blocked('AM config/storage/listen argv differs')
    return info['Id']

def am_http(method,path,body=None):
    if (method,path) not in (('GET','/-/ready'),('GET','/api/v2/status'),('GET','/metrics'),('POST','/api/v2/alerts')):raise Blocked('AM exact transport boundary')
    conn=http.client.HTTPConnection('127.0.0.1',AM_PORT,timeout=5)
    try:
        conn.request(method,path,None if body is None else json.dumps(body).encode(),{'Content-Type':'application/json'})
        response=conn.getresponse();raw=response.read((2<<20)+1)
        check(len(raw)<=2<<20,'bounded actual AM response exceeded')
        return response.status,raw
    finally:conn.close()

class LocalServer:
    def __init__(self,port,handler):
        self.server=ThreadingHTTPServer(('127.0.0.1',port),handler);self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def close(self):
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=3)
        check(not self.thread.is_alive() and self.server.socket.fileno()==-1,'owned listener remains')

class Source:
    def __init__(self,chain):
        self.chain=chain;self.lock=threading.Lock();self.captures=[];self.requests=[];self.mode='normal'
        self.hold=False;self.seen=threading.Event();self.release=threading.Event();source=self
        class Capture(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_POST(self):
                try:
                    if self.path!='/capture/'+chain.nonce:raise Blocked('wrong local capture UUID')
                    length=int(self.headers.get('Content-Length','0'));check(0<length<=65536,'bounded capture payload')
                    body=json.loads(self.rfile.read(length));check(body.get('receiver') in chain.receiver_names and body.get('status')=='firing','capture receiver/status differs')
                    alerts=body.get('alerts');check(isinstance(alerts,list) and len(alerts)==1,'one explicitly owned alert only')
                    for alert in alerts:
                        labels=alert.get('labels',{});env=labels.get('environment')
                        check(env in chain.apps and labels.get('application')==chain.apps[env]['key'] and labels.get('testlab_scope')==chain.nonce,'capture scope differs')
                    with source.lock:source.captures.append(body)
                    self.send_response(200);self.send_header('Content-Length','0');self.end_headers()
                except Exception:
                    self.send_error(400)
            def do_GET(self):self.send_error(405)
        class Relay(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    env=source.validate_request(self.path,self.headers.get('Authorization'))
                    chain.guard_am()
                    with source.lock:mode,hold=source.mode,source.hold;source.hold=False
                    if hold:source.seen.set();check(source.release.wait(10),'controlled status hold exceeded')
                    if mode=='503':status,raw=503,b'{"error":"explicit local relay fault"}'
                    else:status,raw=am_http('GET','/api/v2/status')
                    with source.lock:source.requests.append({'environment':env,'path':self.path,'mode':mode,'actual_source_status':status,'held':hold,'source_sha256':hashlib.sha256(raw).hexdigest(),'bytes':len(raw)})
                    if mode=='timeout':time.sleep(15.3)
                    if mode=='response_loss':
                        self.close_connection=True;self.connection.shutdown(socket.SHUT_RDWR);self.connection.close();return
                    if mode=='half_response':
                        self.send_response(status);self.send_header('Content-Length',str(len(raw)+100));self.send_header('Connection','close');self.end_headers();self.wfile.write(raw[:max(1,len(raw)//2)]);self.close_connection=True;return
                    self.send_response(status);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:
                    try:self.send_error(502)
                    except OSError:pass
            def do_POST(self):self.send_error(405)
            do_PUT=do_DELETE=do_PATCH=do_POST
        self.capture=LocalServer(CAPTURE_PORT,Capture)
        try:self.relay=LocalServer(RELAY_PORT,Relay)
        except BaseException:self.capture.close();raise
    def validate_request(self,path,authorization):
        for env,token in self.chain.am_tokens.items():
            if path=='/'+env+'/api/v2/status' and secrets.compare_digest(authorization or '','Bearer '+token):return env
        raise Blocked('relay env/path/credential mismatch')
    def snapshot(self):
        with self.lock:return {'requests':copy.deepcopy(self.requests),'captures':copy.deepcopy(self.captures)}
    def close(self):
        self.release.set();errors=[]
        for server in (self.relay,self.capture):
            try:server.close()
            except BaseException as error:errors.append(error)
        if errors:raise errors[0]

def verify_tree(value,environment,revision):
    check(value.get('environment_id')==environment and value.get('source')=='alertmanager' and value.get('config_revision')==revision and value.get('observed_at'),'tree source/scope/revision missing')
    check(value.get('partial') is True and value.get('unavailable')==['matcher_values','receiver_names','receiver_targets','channel_types','hits_24h','on_call','escalation','current_interval_activity'],'tree unavailable contract differs')
    root=value['root'];check(root['id']=='route-root' and root['matchers']==[{'k':'environment','op':'=','value_redacted':True}] and root['receiver_refs']==['receiver-1'],'exact environment subtree root differs')
    check(root['group_by']==['application','environment','alertname'] and root['group_wait']=='1s' and root['group_interval']=='2s' and root['repeat_interval']=='1h' and root['conditional'] is False and root['continue'] is False,'inherited policy differs')
    children=root['children'];check(len(children)==(2 if environment=='test' else 1),'cross-environment tree child count differs')
    for index,child in enumerate(children):
        check(child['id']=='route-root-'+str(index+1) and child['title']=='Route '+str(index+1) and child['children']==[] and child['conditional'] is False,'route projection identity/shape differs')
        check(child['matchers']==([{'k':'application','op':'=','value_redacted':True},{'k':'severity','op':'=' if index==0 else '!=','value_redacted':True}] if environment=='test' else [{'k':'application','op':'=','value_redacted':True}]),'matcher value redaction/operators differ')
        check(child['receiver_refs']==(['receiver-'+str(index+2)] if environment=='test' else ['receiver-1']),'opaque receiver mapping differs')

def verify_path(value,app,environment,revision,severity):
    check(value.get('environment_id')==environment and value.get('application_id')==app and value.get('config_revision')==revision and value.get('observed_at'),'path provenance differs')
    index=1 if severity=='P1' else 2
    check(value.get('paths')==[{'node_ids':['route-root','route-root-'+str(index)],'receiver_refs':['receiver-'+str(index+1)],'group_by':['application','environment','alertname'],'group_wait':'1s','group_interval':'2s','repeat_interval':'1h'}],'sample path/receiver/inheritance differs')
    check(value.get('partial') is True and value.get('hits') is None and value.get('channel_targets') is None and value.get('unavailable')==['receiver_names','hits','channel_targets','current_interval_activity'],'test invented delivery/hit/channel authority')

class Chain(readonly.Chain):
    def __init__(self,root,data,run_id):
        self.root,self.data,self.run_id=Path(root).resolve(),data,run_id;self.nonce=uuid.UUID(run_id).hex
        self.database='aiops_testlab_routes_'+self.nonce;self.db_user='routes_'+self.nonce;self.owner_tag=OWNER+':'+run_id+':notification-routes'
        self.work=self.root/'.local/testlab/notification-routes-parallel'/run_id
        if self.work.resolve()!=self.work:raise Blocked('private work path symlink')
        self.work.mkdir(mode=0o700,parents=True,exist_ok=False)
        self.password,self.db_password=secrets.token_urlsafe(32),secrets.token_urlsafe(32)
        self.marker='routesfixture'+self.nonce;self.admin_email='bootstrap-'+self.nonce+'@testlab.invalid'
        self.users={n:uuid.uuid5(uuid.UUID(run_id),n).hex for n in ('reader','peer','hidden','denied')};self.roles={n:'routes-'+self.nonce+'-'+n for n in self.users}
        self.cookies,self.ids,self.processes={},{},[];self.calls=0;self.role_attempted=self.db_attempted=False
        self.contract=Contract(self.root);self.apps={};self.source=None;self.am_cid=None;self.am_attempted=False;self.configured=False;self.generation=0
        self.am_name='aiops-'+OWNER+'-notify-'+self.nonce;self.am_args=['--config.file='+AM_CONFIG,'--storage.path=/alertmanager','--cluster.listen-address=','--web.listen-address=0.0.0.0:9093']
        self.tmpfs='rw,noexec,nosuid,size=16777216,uid='+str(os.getuid())+',gid='+str(os.getgid())+',mode=0700'
        self.receiver_names=['owned-'+self.nonce+'-'+s for s in ('default','critical','app','staging')]
        self.am_tokens={e:secrets.token_urlsafe(32) for e in ('test','staging')};self.source_tokens={e:secrets.token_urlsafe(32) for e in ('test','staging')}
        self.source_ids={e:'routes-'+self.nonce+'-'+e for e in ('test','staging')};self.config_hashes={};self.load_binding=None
        self.state=dict(owner=OWNER,run_id=run_id,database=self.database,database_user=self.db_user,owner_tag=self.owner_tag,
                        ports=list(PORTS),pids=[],source_tree_sha=data['source_tree_sha'],binary_sha256=data['binary_sha256'],helper_sha256=data['helper_sha256'],
                        actual_results_source='formal HTTP app/native webhook; current own AM route tree; POST test read only',source_id=None)
        readonly._private(self.work/'state.json',self.state);readonly._private(self.work/'credentials.json',dict(database_password=self.db_password,password=self.password))
    def marker_case(self,name,actual):
        check(name in REQUIRED_CASES,'unknown stable routes marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def pg(self,*args,**kwargs):
        if readonly.inspect_pg(self.data['postgres_container'])['Id']!=PG_CID:raise Blocked('exact PG CID drift')
        return super().pg(*args,**kwargs)
    def frozen(self):
        if hashes()!=self.data['helper_sha256']:raise Blocked('routes helper source drift')
        for name,want in self.config_hashes.items():
            f=self.work/name
            if f.is_symlink() or stat.S_IMODE(f.stat().st_mode)!=0o600 or hashlib.sha256(f.read_bytes()).hexdigest()!=want:raise Blocked('UUID private configuration drift')
        if self.am_cid:self.guard_am()
        super().frozen()
    def guard_am(self):return guard_am(readonly.inspect_pg(self.am_name),self)
    def pin(self,*names):
        for name in names:self.config_hashes[name]=hashlib.sha256((self.work/name).read_bytes()).hexdigest()
    def seed(self):
        super().seed()
        extra=[]
        for user,env in (('reader','test'),('hidden','staging')):
            for action in ('view','manage'):
                extra.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles[user],'alert_rules',action,'environment',env))+');')
        self.sql('BEGIN; '+' '.join(extra)+' COMMIT;')
    def startup_environment(self):
        return {'AIOPS_OPERATIONS_CONFIG':str(self.work/'operations.json'),'AIOPS_V3_ALERTMANAGER_CONFIG':str(self.work/'alertmanager.json')} if self.configured else {}
    def start(self):
        self.generation+=1;old=readonly.emit
        def generation_emit(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='routes.api_ready.'+str(self.generation)+'.'+name.rsplit('.',1)[1]
            old(name,status,actual,**fields)
        readonly.emit=generation_emit
        try:super().start()
        finally:readonly.emit=old
    def restart(self):
        for p in reversed(self.processes):
            if p.poll() is None:p.terminate();p.wait(timeout=10)
        readonly._private(self.work/'bootstrap-exits.json',{'held_pids':[p.pid for p in self.processes],'exits':[p.poll() for p in self.processes]})
        for i in (0,1):(self.work/('api-'+str(i)+'.log')).rename(self.work/('bootstrap-'+str(i)+'.log'))
        self.start()
    def call(self,user,method,path,*args,**kwargs):
        self.frozen()
        route,_=self.contract.canonical(method,path)
        if (method,route[len('/api/v1'):]) in TARGETS and kwargs.get('scenario','normal')=='normal' and kwargs.get('want',200)<300 and kwargs.get('validate') is None:
            raise Blocked('normal routes target requires exact nonempty source validator')
        old_validator=kwargs.get('validate')
        def validate(value):
            raw=json.dumps(value)
            check(not any(v in raw for v in [*self.receiver_names,*self.am_tokens.values(),*self.source_tokens.values(),'host.docker.internal',self.nonce]),'route DTO exposed source target/name/secret/value')
            if old_validator:old_validator(value)
        # Fixture service IDs contain the nonce and are valid only as producer results.
        if (method,route[len('/api/v1'):]) in TARGETS:kwargs=dict(kwargs,validate=validate)
        return super().call(user,method,path,*args,**kwargs)
    def prepare(self):
        for env in ('test','staging'):
            key='route-app-'+self.nonce+'-'+env
            self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},environment=env,want=201,scenario='fixture')
            row=json.loads(self.sql('SELECT json_build_object(\'app\',a.id,\'env_id\',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key='+q(key)+' AND e.key='+q(env),read=True));row['key']=key;self.apps[env]=row
        # An app-scoped rule manager cannot inspect a shared environment-wide tree.
        self.sql('BEGIN; '+' '.join('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles['peer'],'alert_rules',a,'application',self.apps['test']['app']))+');' for a in ('view','manage'))+' COMMIT;')
        operations={'sources':[],'feishu_channels':[]};am={'environments':{}}
        for env in ('test','staging'):
            readonly.private_file(self.work/(env+'-source-token'),self.source_tokens[env]);readonly.private_file(self.work/(env+'-am-token'),self.am_tokens[env])
            row=self.apps[env];operations['sources'].append({'id':self.source_ids[env],'token_file':str(self.work/(env+'-source-token')),
                'mappings':[{'labels':{'application':row['key'],'environment':env,'testlab_scope':self.nonce},'application_id':row['app'],'environment_id':row['env_id']}]})
            am['environments'][env]={'url':'http://127.0.0.1:'+str(RELAY_PORT)+'/'+env,'allow_http':True,'bearer_token_file':str(self.work/(env+'-am-token')),
                                    'environment_label':'environment','application_bindings':{row['key']:row['app']},'source_ids':[self.source_ids[env]]}
        readonly._private(self.work/'operations.json',operations);readonly._private(self.work/'alertmanager.json',am)
        config=config_for(self);validate_config(config,self);readonly._private(self.work/'am-config.json',config)
        self.pin('operations.json','alertmanager.json','am-config.json','test-source-token','staging-source-token','test-am-token','staging-am-token')
        self.source=Source(self);self.start_am();self.configured=True;self.restart()
    def start_am(self):
        existing=subprocess.run(['docker','container','inspect',self.am_name],capture_output=True,text=True,timeout=5)
        if existing.returncode==0:raise Blocked('UUID AM name exists; refuse adoption')
        self.am_attempted=True
        cmd=['docker','create','--pull','never','--name',self.am_name,'--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,
             '--label','aiops.testlab.purpose=notification-routes','--cpus','0.5','--memory','256m','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
             '--user',str(os.getuid())+':'+str(os.getgid()),'--tmpfs','/alertmanager:'+self.tmpfs,'-p','127.0.0.1:'+str(AM_PORT)+':9093',
             '--mount','type=bind,source='+str(self.work/'am-config.json')+',target='+AM_CONFIG+',readonly',AM_IMAGE,*self.am_args]
        created=subprocess.run(cmd,capture_output=True,text=True,timeout=15);readonly._private(self.work/'am-create.json',{'command':cmd,'exit':created.returncode,'stdout':created.stdout,'stderr':created.stderr})
        if created.returncode!=0 or not re.fullmatch('[a-f0-9]{64}',created.stdout.strip()):raise Blocked('UUID AM create outcome not confirmed; exact record retained')
        self.am_cid=created.stdout.strip();self.state['am_cid']=self.am_cid;readonly._private(self.work/'state.json',self.state)
        guard_am(readonly.inspect_pg(self.am_cid),self,live=False)
        started=subprocess.run(['docker','start',self.am_cid],capture_output=True,text=True,timeout=15)
        check(started.returncode==0,'owned AM start failed')
        deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            self.guard_am()
            try:
                if am_http('GET','/-/ready')[0]==200:break
            except OSError:pass
            time.sleep(.2)
        else:raise Blocked('owned AM readiness deadline')
        self.receiver_proof('startup')
    def receiver_proof(self,stage):
        info=readonly.inspect_pg(self.am_cid);self.guard_am()
        f=self.work/'am-config.json';fd=os.open(f,os.O_RDONLY|os.O_NOFOLLOW)
        try:
            before=os.fstat(fd);raw=os.read(fd,65537);after=os.fstat(fd)
            check((before.st_dev,before.st_ino,before.st_size,before.st_ctime_ns,before.st_mtime_ns)==(after.st_dev,after.st_ino,after.st_size,after.st_ctime_ns,after.st_mtime_ns) and len(raw)==before.st_size,'source FD drift')
        finally:os.close(fd)
        actual=subprocess.check_output(['docker','exec',self.am_cid,'/bin/cat',AM_CONFIG],timeout=5)
        check(actual==raw,'actual container config differs from readonly host source')
        status,mraw=am_http('GET','/metrics');check(status==200,'actual AM load metrics missing')
        metrics={}
        for name in ('alertmanager_config_last_reload_successful','alertmanager_config_last_reload_success_timestamp_seconds','alertmanager_config_hash'):
            values=re.findall(r'(?m)^'+name+r'\s+([^\s]+)\s*$',mraw.decode());check(len(values)==1,'unique config metric required');metrics[name]=float(values[0]);check(math.isfinite(metrics[name]),'nonfinite AM config metric')
        loaded=metrics['alertmanager_config_last_reload_success_timestamp_seconds'];started=datetime.datetime.fromisoformat(info['State']['StartedAt'].replace('Z','+00:00')).timestamp()
        check(metrics['alertmanager_config_last_reload_successful']==1 and started<=loaded<=time.time() and max(before.st_ctime_ns,before.st_mtime_ns)<=int(loaded*1e9),'config successful load time/FD freshness mismatch')
        check(metrics['alertmanager_config_hash']==float(int.from_bytes(hashlib.md5(raw).digest()[:6],'little')),'actual official config hash differs')
        validate_config(json.loads(raw),self)
        status,sraw=am_http('GET','/api/v2/status');check(status==200,'actual AM status missing');value=json.loads(sraw)
        original=value['config']['original'];self.revision='sha256:'+hashlib.sha256(original.encode()).hexdigest()
        # Status redacts URLs; actual bytes/load evidence above establishes the destination.
        check(all(name in original for name in self.receiver_names) and 'environment="test"' in original and 'environment="staging"' in original,'loaded scoped tree absent')
        binding={'cid':self.am_cid,'image_id':info['Image'],'started_at':info['State']['StartedAt'],'source_sha':hashlib.sha256(raw).hexdigest(),'device':before.st_dev,'inode':before.st_ino,'mtime_ns':before.st_mtime_ns,'ctime_ns':before.st_ctime_ns,'loaded':loaded,'metric_hash':metrics['alertmanager_config_hash'],'revision':self.revision}
        if self.load_binding is not None:check(binding==self.load_binding,'loaded source identity/config drift')
        self.load_binding=binding
        readonly._private(self.work/('receiver-proof-'+stage+'.json'),{'binding':binding,'inspect':info,'source':raw.decode(),'actual_container_config':actual.decode(),'status':value,'load_metrics':metrics,'sole_capture_destination':True})
        if stage=='startup':self.marker_case('routes.source_load',{'cid':self.am_cid,'image_id':AM_IMAGE_ID,'source_revision':self.revision,'config_argv_ro_mount_actual_bytes_official_metric':True,'all_receivers_only_owned_capture':True})
    def webhook(self,env):
        now=datetime.datetime.now(datetime.timezone.utc).isoformat();source=self.source_ids[env]
        alert={'status':'firing','fingerprint':source,'startsAt':now,'labels':{'application':self.apps[env]['key'],'environment':env,'testlab_scope':self.nonce,'alertname':'OwnedRouteRead','severity':'P2'},'annotations':{'summary':'Explicit UUID local routing fixture'}}
        self.calls+=1;number=self.calls;path='/api/v1/webhooks/alertmanager/'+source
        status,raw,_=readonly.transport(BASES[0],'POST',path,json.dumps({'alerts':[alert]}).encode(),{'Content-Type':'application/json','Authorization':'Bearer '+self.source_tokens[env]})
        value=json.loads(raw);check(status==202 and value=={'accepted':1,'notification_delivery':'not_configured'},'native UUID webhook must not send notification')
        row={'name':'routes.http.'+str(number),'method':'POST','path':'/api/v1/webhooks/alertmanager/{source}','scenario':'normal','registration_evidence':False,'run_id':self.run_id,'call_id':self.run_id+'-'+str(number),'test_name':'FormalNotificationRoutesFixture','status':'passed','actual':{'status':status},'expected':{'status':[202]}}
        readonly.append_json(self.work/'interfaces.jsonl',row);emit(row['name'],'passed',row['actual'],interface=row)
        self.apps[env]['alert']=alert
    def produce(self):
        for env in ('test','staging'):self.webhook(env)
        for env,user in (('test','reader'),('staging','hidden')):
            self.call(user,'GET','/api/v1/alert-groups',environment=env,scenario='fixture',validate=lambda v,env=env:
                      check(len(v['items'])==1 and v['items'][0]['source_id']==self.source_ids[env] and v['items'][0]['count']==1,'HTTP native group lineage differs'))
        self.marker_case('routes.native_ingress',{'native_incidents':2,'test_staging_bound':True,'notifications_not_configured':True,'sql_success_seed':False})
        # A separate explicit component alert proves the physical receiver; /test never sends.
        self.receiver_proof('before-local-alert')
        alert=copy.deepcopy(self.apps['test']['alert']);alert.pop('status');alert.pop('fingerprint');alert['endsAt']=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(minutes=5)).isoformat()
        status,_=am_http('POST','/api/v2/alerts',[alert]);check(status==200,'controlled real AM alert input failed')
        deadline=time.monotonic()+10
        while time.monotonic()<deadline and len(self.source.snapshot()['captures'])<1:time.sleep(.1)
        captured=self.source.snapshot()['captures'];check(len(captured)==1 and captured[0]['receiver']==self.receiver_names[2],'actual scoped AM delivery missing/duplicate')
        alert['endsAt']=(datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(seconds=1)).isoformat();check(am_http('POST','/api/v2/alerts',[alert])[0]==200,'owned alert resolution failed')
        time.sleep(2.2)
        check(len(self.source.snapshot()['captures'])==1,'resolved-disabled owned capture unexpectedly changed')
        self.marker_case('routes.local_capture',{'captured':1,'actual_AM_http_input':True,'resolved_send_disabled':True,'product_test_sends':False,'external_notification':False})
    def snapshot_db(self):
        return {table:json.loads(self.sql('SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),\'[]\'::json) FROM '+table+' t',read=True)) for table in ('ops_incidents','ops_alert_occurrences','management_audit','ops_notifications','v3_silence_execution_claims')}
    def tree(self,user='reader',env='test',index=0,scenario='normal'):
        value,headers=self.call(user,'GET','/api/v1/notification-routes',environment=env,index=index,scenario=scenario,validate=lambda v:verify_tree(v,env,self.revision))
        header=lambda name:next((v for k,v in headers.items() if k.lower()==name.lower()),'')
        check(header('ETag')=='"'+self.revision+'"' and header('Cache-Control')=='no-store','tree cache/revision headers differ')
        return value
    def sample(self,severity='P1',index=0,scenario='normal'):
        value,headers=self.call('reader','POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key'],'severity':severity}},index=index,scenario=scenario,validate=lambda v:verify_path(v,self.apps['test']['app'],'test',self.revision,severity))
        check(next((v for k,v in headers.items() if k.lower()=='cache-control'),'')=='no-store','sample cache header differs');return value
    def normal(self):
        self.tree();self.tree('hidden','staging',index=1);self.tree(index=1)
        self.marker_case('routes.nonempty_tree',{'test_children':2,'staging_children':1,'receiver_and_matcher_values_redacted':True,'revision':self.revision})
        first=self.sample('P1');other=self.sample('P2',index=1);repeat=self.sample('P1',index=1,scenario='idempotency')
        check({k:v for k,v in first.items() if k!='observed_at'}=={k:v for k,v in repeat.items() if k!='observed_at'},'repeat sample changed policy/path')
        self.marker_case('routes.sample_paths',{'P1_path':'route-root-1','P2_path':'route-root-2','repeat_policy_same':True,'sample_delivery_delta':0})
    def authorization(self):
        before=self.source.snapshot()
        for method,path,body in (('GET','/api/v1/notification-routes',None),('POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key']}})):
            for user in (None,'denied','peer'):
                self.call(user,method,path,body,want=401 if user is None else 403,code='unauthorized' if user is None else 'forbidden',scenario='authorization')
            self.call('reader',method,path,body,environment='staging',want=403,code='forbidden',scenario='authorization')
            self.call('hidden',method,path,body,environment='test',want=403,code='forbidden',scenario='authorization')
        check(self.source.snapshot()==before,'unauthorized source access/delivery')
        self.marker_case('routes.authorization',{'anonymous':2,'no_permission':2,'application_only_manager':2,'cross_environment':4,'source_calls':0})
    def boundaries(self):
        before=self.source.snapshot()
        for method,path,body in (('GET','/api/v1/notification-routes',None),('POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key']}})):
            # Public POST is rejected by the shared environment middleware first.
            self.call('admin',method,path,body,environment='all',want=409,code='single_environment_required' if method=='GET' else 'env_view_only',scenario='boundary')
        invalids=[{}, {'bad-name':'x','application':self.apps['test']['key']},{'application':self.apps['test']['key'],'x':'x'*1025},
                  {'application':self.apps['test']['key'],'x':'bad\nvalue'},{'application':self.apps['test']['key'],'environment':'staging'},
                  {'application':self.apps['test']['key'],**{'l'+str(i):'v' for i in range(100)}},
                  {'application':self.apps['test']['key'],**{'l'+str(i):'v' for i in range(99)}}]
        for labels in invalids:self.call('reader','POST','/api/v1/notification-routes/test',{'labels':labels},want=422,code='invalid_labels',scenario='boundary')
        self.call('reader','POST','/api/v1/notification-routes/test',{'labels':{'application':'unbound'}},want=404,code='application_not_found',scenario='boundary')
        check(self.source.snapshot()==before,'invalid sample read source/dispatched')
        self.marker_case('routes.boundaries',{'max_matchers':100,'max_string':1024,'environment_autoinsert_budget':True,'unbound_application404':True,'invalid_source_calls':0})
    def faults(self):
        for mode in ('503','half_response','response_loss','timeout'):
            self.source.mode=mode
            try:
                for method,path,body in (('GET','/api/v1/notification-routes',None),('POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key']}})):
                    count=len(self.source.snapshot()['requests']);started=time.monotonic()
                    self.call('reader',method,path,body,want=503,code='route_source_unavailable',scenario='failure')
                    rows=self.source.snapshot()['requests'];check(len(rows)==count+1 and rows[-1]['mode']==mode,'source fault not executed exactly once')
                    readonly.append_json(self.work/'fault-times.jsonl',{'mode':mode,'method':method,'elapsed':time.monotonic()-started,'source_calls':1})
            finally:self.source.mode='normal'
            self.tree(index=1,scenario='failure');self.sample(index=1,scenario='failure')
        self.marker_case('routes.failure_recovery',{'modes':['503','half_response','response_loss','15s_timeout'],'requests_per_failure':1,'recovery_instance':1,'actual_AM_restart':False,'local_submit_failure_applicability':'read-only source, no business/audit write'})
    def grant(self,enabled):
        if enabled:self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles['reader'],'alert_rules','manage','environment','test'))+');')
        else:self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='alert_rules' AND action='manage';")
    def inflight(self):
        for method,path,body in (('GET','/api/v1/notification-routes',None),('POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key']}})):
            self.source.seen.clear();self.source.release.clear();self.source.hold=True;errors=[]
            def request():
                try:self.call('reader',method,path,body,want=403,code='forbidden',scenario='authorization')
                except BaseException as error:errors.append(error)
            thread=threading.Thread(target=request);thread.start()
            try:
                check(self.source.seen.wait(5),'request did not reach source before revoke');self.grant(False)
                self.call('reader',method,path,body,index=1,want=403,code='forbidden',scenario='authorization')
            finally:self.source.release.set();thread.join(timeout=12)
            check(not errors and not thread.is_alive(),'inflight revoke result failed');self.grant(True)
        self.marker_case('routes.inflight_revocation',{'routes':2,'source_read_then_revoked403':True,'new_other_instance403_without_source':True})
    def session_revoke(self):
        self.source.seen.clear();self.source.release.clear();self.source.hold=True;errors=[]
        def request():
            try:self.call('reader','POST','/api/v1/notification-routes/test',{'labels':{'application':self.apps['test']['key']}},want=401,code='unauthorized',scenario='authorization')
            except BaseException as error:errors.append(error)
        thread=threading.Thread(target=request);thread.start()
        try:
            check(self.source.seen.wait(5),'session request did not reach source');self.sql('DELETE FROM sessions WHERE user_id='+q(self.users['reader'])+';')
        finally:self.source.release.set();thread.join(timeout=12)
        check(not errors and not thread.is_alive(),'inflight session revoke failed')
        self.call('reader','GET','/api/v1/notification-routes',index=1,want=401,code='unauthorized',scenario='authorization')
        self.marker_case('routes.session_revocation',{'inflight_after_source401':True,'other_instance401':True})
    def run(self):
        self.provision();self.start();self.seed()
        for n in ('admin','reader','peer','hidden','denied'):self.login(n)
        self.prepare();self.produce();before=self.snapshot_db();captures=self.source.snapshot()['captures']
        self.normal();self.authorization();self.boundaries();self.faults();self.inflight();self.session_revoke()
        after=self.snapshot_db();check(before==after and self.source.snapshot()['captures']==captures,'route reads/evaluations changed DB/audit/capture')
        check(len(before['ops_incidents'])==2 and len(before['ops_alert_occurrences'])==2 and before['ops_notifications']==[] and before['v3_silence_execution_claims']==[],'native input/tables/source count differs')
        for env,row in self.apps.items():
            incident=next(i for i in before['ops_incidents'] if i['source_id']==self.source_ids[env])
            check(incident['application_id']==row['app'] and incident['environment_id']==row['env_id'] and incident['labels']==row['alert']['labels'],'native source/app/env receipt differs')
        readonly._private(self.work/'db-readback.json',after);readonly._private(self.work/'source-receipts.json',self.source.snapshot())
        self.receiver_proof('acceptance-end');self.marker_case('routes.readonly_consistency',{'before_after_all_business_audit_tables_identical':True,'captured_fixture':1,'product_test_capture_delta':0,'remote_writes':0,'new_audits':0})
        result={'run_id':self.run_id,'http_calls':self.calls,'source_tree_sha':self.data['source_tree_sha'],'binary_sha256':self.data['binary_sha256'],'am_cid':self.am_cid,'image_id':AM_IMAGE_ID,'config_revision':self.revision,'normal_routes':[list(t) for t in TARGETS],'required_cases':list(REQUIRED_CASES),'capture_count':1,'product_test_capture_delta':0,'proof_scope':'current real owned AM and formal HTTP read-only route evaluation; separate controlled local fixture delivery','unverified':['actual production tenancy/targets','notification draft MR/activation','delivery reliability/acknowledgement','natural session expiry while locked','500nodes/32depth source cap','history7days','performance/24h','nativeCI-CD']}
        readonly._private(self.work/'result.json',result);return result
    def cleanup(self):
        failures=[]
        try:super().cleanup()
        except BaseException as error:failures.append(error)
        if self.am_cid:
            try:
                info=readonly.inspect_pg(self.am_cid);guard_am(info,self,live=bool(info.get('State',{}).get('Running')))
                if info['State']['Running']:
                    check(subprocess.run(['docker','stop','--time','3',self.am_cid],capture_output=True,timeout=10).returncode==0,'own AM stop failed')
                guard_am(readonly.inspect_pg(self.am_cid),self,live=False)
                check(subprocess.run(['docker','rm',self.am_cid],capture_output=True,timeout=10).returncode==0,'own AM removal failed')
                self.am_cid=None
                check(subprocess.run(['docker','container','inspect',self.am_name],capture_output=True,timeout=5).returncode!=0,'exact AM name still exists')
            except BaseException as error:failures.append(error)
        elif self.am_attempted:
            # Unknown CREATE outcome is deliberately not adopted or signaled.
            if subprocess.run(['docker','container','inspect',self.am_name],capture_output=True,timeout=5).returncode==0:failures.append(Blocked('unconfirmed created AM requires explicit identity recovery'))
        if self.source:
            try:self.source.close()
            except BaseException as error:failures.append(error)
        absence={}
        for port in (*PORTS,AM_PORT,CAPTURE_PORT,RELAY_PORT):
            with socket.socket() as sock:absence[str(port)]=sock.connect_ex(('127.0.0.1',port))!=0
        receipt={'ports_absent':absence,'am_exact_name_absent':self.am_cid is None,'held_process_exits':[p.poll() for p in self.processes],'private_tmpfs_retired_with_container':True,'errors':[type(e).__name__+': '+str(e) for e in failures]}
        readonly._private(self.work/'routes-cleanup.json',receipt)
        if failures or not all(absence.values()):raise Blocked('notification cleanup failed; exact residual evidence retained')
        emit('routes.cleanup','passed',receipt)

@contextlib.contextmanager
def lease(root):
    work=Path(root)/'.local/testlab/notification-routes-parallel';work.mkdir(mode=0o700,parents=True,exist_ok=True)
    fd=os.open(work/'lease.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);yield
    finally:os.close(fd)

def inventory(root):
    Contract(root)
    return dict(module=MODULE,targets=[list(t) for t in TARGETS],ports=[*PORTS,AM_PORT,CAPTURE_PORT,RELAY_PORT],evidence_mode='real_local',required_cases=list(REQUIRED_CASES),am_image=AM_IMAGE,am_image_id=AM_IMAGE_ID,am_cpu=.5,am_memory=268435456,product_test_sends_notification=False,offline_only=True)

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--run-id')
    mode=parser.add_mutually_exclusive_group(required=True);mode.add_argument('--inventory',action='store_true');mode.add_argument('--live',action='store_true');args=parser.parse_args(argv)
    if args.inventory:print(json.dumps(inventory(args.root)));return 0
    run_id=args.run_id or str(uuid.uuid4())
    if str(uuid.UUID(run_id))!=run_id:raise Blocked('canonical fresh UUID required')
    try:
        with lease(args.root),context(),readonly.cooperative_signals():
            data=doctor(args.root);chain=Chain(args.root,data,run_id);readonly.execute(chain)
    except Blocked as error:emit('routes.environment','blocked',{'error':str(error)});return 2
    except Exception as error:emit('routes.failure','failed',{'type':type(error).__name__,'error':str(error)});return 1
    return 0

if __name__=='__main__':raise SystemExit(main())
