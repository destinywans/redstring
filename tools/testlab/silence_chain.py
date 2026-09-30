"""Owned formal HTTPS + real ES256 approval -> real Alertmanager silences.

The bearer/TLS relay is a declared transport fixture; Alertmanager state and
dispatch are real. This does not attest bulk grouping or historical coverage.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, http.client, importlib.util, json, os, re, secrets, socket, ssl, subprocess, threading, time, traceback, urllib.parse, uuid
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import identity
from env import _private, _inspect, _owner
from faults import Blocked, attest, private_json
from tls_entry import command_env

ROOT = Path(__file__).resolve().parents[2]
ORIGIN = 'https://testlab.local:18595'
ENDPOINT = 'https://localhost:18597'
q = identity.quote
LIMIT = 4 << 20


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module


def generated_password(root,backend):
    owner=backend.get('owner','')
    if not re.fullmatch('testlab-[0-9a-f]{12}',owner):raise Blocked('silence generated credential owner invalid')
    work=Path(root).resolve()/'.local/testlab/environments'/owner
    path=work/'perf/users-password.json'
    if path.resolve()!=path or work.is_symlink() or path.parent.is_symlink():raise Blocked('silence generated credential path mismatch')
    try:password=private_json(path).get('password')
    except (OSError,ValueError):raise Blocked('owned private performance identity password unavailable') from None
    if not isinstance(password,str) or len(password)<24:raise Blocked('owned generated performance identity password invalid')
    return password


def record_diagnostic(root,work,error,chain):
    """Private frames omit source text/locals; public errors remain redacted."""
    work.mkdir(parents=True,mode=0o700,exist_ok=True)
    stack=[]
    for frame in traceback.extract_tb(error.__traceback__):
        try:filename=str(Path(frame.filename).relative_to(root))
        except ValueError:filename=Path(frame.filename).name
        stack.append(dict(file=filename,line=frame.lineno,function=frame.name))
    row=dict(error_type=type(error).__name__,stage=getattr(chain,'stage','construct'),stack=stack)
    if isinstance(error,KeyError):row['missing_key']=str(error.args[0])[:512] if error.args else None
    fd=os.open(work/'diagnostic-error.json',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'w') as stream:json.dump(row,stream)
    identity.preserve_first_failure(work,row)


def doctor(root):
    backend, _ = attest(root)
    component = private_json(Path(root)/'.local/testlab/component-environment.json')
    work = Path(root)/'.local/testlab/environments'/backend['owner']
    if component.get('owner') != backend['owner'] or Path(component.get('work','')).resolve() != work.resolve():
        raise Blocked('silence component owner/work mismatch')
    for name, port in [('prometheus',18570),('alertmanager',18571)]:
        entries = [s for s in component.get('services',[]) if s.get('name') == name]
        if len(entries)!=1 or entries[0].get('container')!='aiops-'+backend['owner']+'-'+name or entries[0].get('url')!='http://127.0.0.1:'+str(port):
            raise Blocked('owned real Prometheus/Alertmanager component required')
        guard_component(backend, name, port)
    for port in (18595,18596,18597): identity.free_port(port)
    return backend, component


def guard_component(backend, name, port, deadline=None):
    info = identity.budgeted_inspect('aiops-'+backend['owner']+'-'+name, deadline)
    try:_owner(info,backend['owner'])
    except ValueError:raise Blocked('owned real component label mismatch') from None
    bindings=[x for values in info.get('NetworkSettings',{}).get('Ports',{}).values() for x in (values or [])]
    if not info.get('State',{}).get('Running') or {'HostIp':'127.0.0.1','HostPort':str(port)} not in bindings:
        raise Blocked('owned real component is not live on exact loopback port')


def request_local(port,method,path,body=None,headers=None,context=None,timeout=10):
    if port not in (18570,18571,18595,18596,18597) or not path.startswith('/') or path.startswith('//'):
        raise Blocked('silence fixed local transport boundary')
    conn = http.client.HTTPSConnection('testlab.local' if port==18595 else 'localhost',port,context=context,timeout=timeout) if context else http.client.HTTPConnection('127.0.0.1',port,timeout=timeout)
    if port==18595: conn._create_connection=lambda address,timeout,source_address=None:socket.create_connection(('127.0.0.1',port),timeout,source_address)
    try:
        conn.request(method,path,None if body is None else json.dumps(body).encode(),headers or {})
        response=conn.getresponse();raw=response.read(LIMIT+1)
        if len(raw)>LIMIT:raise AssertionError('silence response exceeded bounded snapshot size')
        return response.status, json.loads(raw) if raw else {}, response.headers
    finally:conn.close()


def instant(value):
    return datetime.fromisoformat(value.replace('Z','+00:00'))


def exact_create(observed,payload,operation,upstream):
    if observed.get('id')!=upstream or observed.get('annotations')!={'aiops_operation_id':operation} or payload.get('annotations')!={'aiops_operation_id':operation}:
        raise AssertionError('real silence operation/UUID marker mismatch')
    normalize=lambda values:sorted((m['name'],m['value'],m.get('isRegex',False),m.get('isEqual',True)) for m in values)
    if normalize(observed['matchers'])!=normalize(payload['matchers']) or observed['comment']!=payload['comment'] or observed['createdBy']!=payload['createdBy']:
        raise AssertionError('real silence canonical matcher/creator/comment mismatch')
    shift=(instant(observed['startsAt'])-instant(payload['startsAt'])).total_seconds()
    if not 0<=shift<=30 or instant(observed['endsAt'])!=instant(payload['endsAt']) or observed['status']['state']!='active':
        raise AssertionError('real silence exact approved window/state mismatch')


class Relay:
    def __init__(self,port,cert,key,upstream,token=None,backend=None):
        self.port,self.upstream,self.token,self.backend=port,upstream,token,backend
        self.lock=threading.Lock();self.records=[];self.payloads={};self.arm=None;self.accepted=threading.Event();self.release=threading.Event()
        self.inflight=0;self.idle=threading.Event();self.idle.set()
        relay=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def relay(self):
                if relay.token:
                    if not secrets.compare_digest(self.headers.get('Authorization',''),'Bearer '+relay.token):self.send_error(403);return
                    allowed=self.command=='GET' and self.path in ('/api/v2/silences','/api/v2/alerts','/api/v2/status') or self.command=='POST' and self.path=='/api/v2/silences' or self.command=='DELETE' and re.fullmatch(r'/api/v2/silence/[0-9a-f-]{36}',self.path)
                else:allowed=self.path.startswith('/api/v1/') or self.path in ('/healthz','/readyz')
                if not allowed:self.send_error(404);return
                conn=None;tracking=False
                try:
                    self.connection.settimeout(15)
                    size=int(self.headers.get('Content-Length','0'))
                    if not 0<=size<=65536:self.send_error(413);return
                    body=self.rfile.read(size) if size else None
                    marker=None
                    if relay.token and self.command=='POST':
                        payload=json.loads(body);marker=payload.get('annotations',{}).get('aiops_operation_id')
                        if not re.fullmatch('[0-9a-f]{32}',str(marker)) or 'id' in payload or not payload.get('comment','').startswith('Owned silence '+relay.run_id+' '):self.send_error(403);return
                        names={m['name']:m for m in payload['matchers']}
                        if any(names.get(k,{}).get('value')!=v or names[k].get('isRegex') or not names[k].get('isEqual') for k,v in relay.labels.items()):self.send_error(403);return
                        with relay.lock:
                            relay.payloads[marker]=payload
                            if hasattr(relay,'work'):_private(relay.work/'am-request-payloads.json',relay.payloads)
                    if relay.token and self.command=='DELETE':
                        upstream_id=self.path.rsplit('/',1)[1]
                        with relay.lock:known=any(r.get('upstream_id')==upstream_id for r in relay.records if r['method']=='POST' and r['status']==200)
                        if not known:self.send_error(403);return
                    if relay.token:guard_component(relay.backend,'alertmanager',18571)
                    with relay.lock:relay.inflight+=1;relay.idle.clear();tracking=True
                    headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','connection','transfer-encoding','content-length','x-forwarded-for','authorization')}
                    if not relay.token:headers['X-Forwarded-For']='127.0.0.1'
                    conn=http.client.HTTPConnection('127.0.0.1',relay.upstream,timeout=10);conn.request(self.command,self.path,body,headers)
                    response=conn.getresponse();raw=response.read(LIMIT+1)
                    if len(raw)>LIMIT:raise AssertionError('relay upstream response exceeds bound')
                    loss=None
                    if relay.token and self.command in ('POST','DELETE'):
                        result=json.loads(raw) if raw else {}
                        row=dict(method=self.command,path=self.path,status=response.status,operation_id=marker,upstream_id=result.get('silenceID') if self.command=='POST' else self.path.rsplit('/',1)[1])
                        with relay.lock:
                            relay.records.append(row)
                            if hasattr(relay,'work'):_private(relay.work/'am-write-receipts.json',relay.records)
                            if relay.arm and relay.arm[0]==self.command and relay.arm[1]==(payload['comment'] if self.command=='POST' else row['upstream_id']):loss=relay.arm[2];relay.arm=None
                        if loss:
                            if response.status!=200:raise AssertionError('fault injection requires actual upstream success')
                            relay.accepted.set()
                            if loss=='crash':relay.release.wait(20)
                            self.close_connection=True
                            try:self.connection.shutdown(socket.SHUT_RDWR)
                            except OSError:pass
                            self.connection.close();return
                    self.send_response(response.status)
                    for k,v in response.getheaders():
                        if k.lower() not in ('connection','transfer-encoding','content-length','server','date'):self.send_header(k,v)
                    self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (OSError,ValueError,KeyError,Blocked,AssertionError):
                    try:self.send_error(502)
                    except OSError:pass
                finally:
                    if conn:conn.close()
                    if tracking:
                        with relay.lock:
                            relay.inflight-=1
                            if relay.inflight==0:relay.idle.set()
            do_GET=do_POST=do_PATCH=do_DELETE=do_PUT=relay
        self.server=ThreadingHTTPServer(('127.0.0.1',port),Handler);self.server.daemon_threads=True
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(cert,key);self.server.socket=context.wrap_socket(self.server.socket,server_side=True)
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True)
    def start(self):self.thread.start()
    def fault(self,method,identity,mode):
        with self.lock:
            if self.arm is not None:raise AssertionError('previous precise relay fault not consumed')
            self.arm=(method,identity,mode);self.accepted.clear();self.release.clear()
    def count(self,method,upstream=None,operation=None):
        with self.lock:return sum(r['method']==method and (upstream is None or r['upstream_id']==upstream) and (operation is None or r['operation_id']==operation) for r in self.records)
    def close(self,deadline=None):
        self.release.set()
        if self.thread.is_alive():self.server.shutdown()
        self.server.server_close()
        if self.thread.is_alive():self.thread.join(timeout=identity.remaining(deadline,3))
        if not self.idle.wait(timeout=identity.remaining(deadline,10)):
            raise AssertionError('owned relay requests did not quiesce within bounded cleanup')


class Chain(identity.Chain):
    def __init__(self,root,backend,component,run_id,crash=True):
        self.root,self.backend,self.component,self.run_id=Path(root),backend,component,run_id
        self.work=self.root/'.local/testlab/environments'/backend['owner']/'silence-chain'/run_id;self.work.mkdir(parents=True,mode=0o700,exist_ok=False)
        self.origin=ORIGIN;self.module='formal-silence';self.test_name='FormalSilenceApproval';self.call_number=0
        self.proc=None;self.entry=None;self.am=None;self.seeded=False;self.crash=crash;self.phase=0;self.rows=[]
        self.stage='construct';self.uids=[uuid.uuid4().hex for _ in range(3)];self.roles=[uuid.uuid4().hex for _ in range(2)];self.password=generated_password(root,backend);self.creator_cookie=None
        self.image_hash=hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        self.state=dict(owner=backend['owner'],run_id=run_id,work=str(self.work),origin=ORIGIN,pid=None,uids=self.uids,roles=self.roles,
                        source_tree_sha=backend['source_tree_sha'],binary_sha256=self.image_hash,bulk_history_acceptance=False)
        _private(self.work/'state.json',self.state)

    def observe(self,statement):return json.loads(self.sql("BEGIN READ ONLY; SET LOCAL statement_timeout='3s';"+statement+'COMMIT;'))

    def sql(self,statement):
        deadline=getattr(self,'cleanup_deadline',None)
        info=identity.budgeted_inspect(self.backend['postgres_container'],deadline)
        if not info or not info.get('State',{}).get('Running'):raise Blocked('owned database not running')
        _owner(info,self.backend['owner'])
        argv=['docker','exec','-i',self.backend['postgres_container'],'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U','testlab','-d','aiops_testlab_app']
        result=subprocess.run(argv,input=statement,text=True,capture_output=True,timeout=identity.remaining(deadline,3 if deadline else 30))
        if result.returncode:
            number=getattr(self,'sql_failure_number',0)+1;self.sql_failure_number=number
            _private(self.work/('sql-failure-'+str(number)+'.json'),dict(command=argv,exit_code=result.returncode,stderr=result.stderr[:64<<10],stderr_truncated=len(result.stderr)>64<<10,statement_sha256=hashlib.sha256(statement.encode()).hexdigest()))
            if result.returncode==3:raise AssertionError('owned silence SQL fixture failed; private command and stderr retained')
            raise Blocked('owned silence database command failed; private command and stderr retained')
        return result.stdout.strip()

    def prepare(self):
        self.stage='prepare'
        cert,key=self.work/'tls.crt',self.work/'tls.key'
        result=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','2','-subj','/CN=testlab.local','-addext','subjectAltName=DNS:testlab.local,DNS:localhost,IP:127.0.0.1'],capture_output=True,timeout=15)
        if result.returncode:raise Blocked('private silence TLS certificate generation failed')
        for path in (cert,key):os.chmod(path,0o600)
        self.context=ssl.create_default_context(cafile=str(cert));self.token=secrets.token_urlsafe(32)
        token_file=self.work/'am-token';token_file.write_text(self.token);os.chmod(token_file,0o600)
        webhook=self.work/'webhook-token';webhook.write_text(secrets.token_urlsafe(32));os.chmod(webhook,0o600)
        operations=self.work/'operations.json';source='silence-'+self.run_id
        _private(operations,{'sources':[{'id':source,'token_file':str(webhook),'mappings':[{'labels':{'application':'testlab','environment':'test'},'application_id':'testlab','environment_id':'testlab-test'}]}],'feishu_channels':[]})
        config=self.work/'alertmanager.json'
        _private(config,{'environments':{'test':{'url':ENDPOINT,'bearer_token_file':str(token_file),'allow_http':False,'environment_label':'environment','application_bindings':{'testlab':'testlab'},'source_ids':[source]}}})
        self.variables=dict(DATABASE_URL=self.backend['database_url'],AIOPS_V3_INSTANCE_CONFIG=self.backend['instance_config'],AIOPS_ADDR='127.0.0.1:18596',
            AIOPS_ADMIN_EMAIL=self.backend['admin']['email'],AIOPS_ADMIN_PASSWORD=self.backend['admin']['password'],AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32',
            AIOPS_PUBLIC_ORIGIN=ORIGIN,AIOPS_SECURE_COOKIES='true',AIOPS_V3_PASSKEY_ENABLED='true',AIOPS_V3_SILENCE_EXECUTION_ENABLED='true',AIOPS_V3_ALERTMANAGER_CONFIG=str(config),
            AIOPS_OPERATIONS_CONFIG=str(operations),SSL_CERT_FILE=str(cert),OPENAI_MODEL='testlab',OPENAI_BASE_URL='http://127.0.0.1:18578/v1',OPENAI_API_KEY='synthetic-testlab-key',
            EMBEDDING_PROVIDER='openai',EMBEDDING_MODEL='synthetic-testlab',EMBEDDING_BASE_URL='http://127.0.0.1:18578/v1',EMBEDDING_API_KEY='synthetic-testlab-key')
        _private(self.work/'api-config.json',self.variables)
        self.seeded=True
        statement='BEGIN;'
        for role in self.roles:statement+='INSERT INTO rbac_roles(id,name) VALUES('+q(role)+','+q('Owned silence acceptance '+role)+');'
        for index,uid in enumerate(self.uids):
            statement+='INSERT INTO users(id,email,name,role,password_hash) SELECT '+q(uid)+','+q(uid+'@testlab.invalid')+",'Owned silence human','viewer',password_hash FROM users WHERE id="+q('perf-'+self.backend['owner']+'-user-5')+'; DELETE FROM rbac_user_roles WHERE user_id='+q(uid)+'; INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+q(uid)+','+q(self.roles[0 if index==0 else 1])+');'
        for role in self.roles:statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+q(role)+",'environment','view','environment','test');"
        for module,action in [('canvas','view'),('canvas','change'),('silences','view'),('silences','manage'),('proposals','view'),('proposals','change')]:statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+q(self.roles[0])+','+q(module)+','+q(action)+",'application','testlab');"
        statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES('+q(self.roles[1])+",'proposals','approve','application','testlab','passkey'),("+q(self.roles[1])+",'proposals','view','application','testlab','standard'); COMMIT;"
        self.sql(statement)
        if self.observe('SELECT count(*) FROM users WHERE id IN ('+','.join(map(q,self.uids))+');')!=3:raise Blocked('owned performance seed users unavailable for synthetic login hash')
        deadline=time.monotonic()+30
        while True:
            guard_component(self.backend,'prometheus',18570);guard_component(self.backend,'alertmanager',18571)
            status,prom,_=request_local(18570,'GET','/api/v1/alerts');am_status,alerts,_=request_local(18571,'GET','/api/v2/alerts')
            actual=prom.get('data',{}).get('alerts',[]) if status==200 else []
            self.signal=next((a['labels'] for a in actual if a.get('state')=='firing' and a.get('labels',{}).get('application')=='testlab' and a['labels'].get('environment')=='test' and a['labels'].get('alertname')=='TestlabRequestFailures' and a['labels'].get('signal_id') and am_status==200 and any(x.get('labels')==a['labels'] for x in alerts)),None)
            if self.signal:break
            if time.monotonic()>deadline:raise Blocked('actual Prometheus firing alert not yet present with identical labels in owned Alertmanager')
            time.sleep(1)
        self.labels={k:self.signal[k] for k in ('application','environment','alertname','signal_id')}
        _private(self.work/'real-alert-source.json',{'prometheus':'owned:18570','alertmanager':'owned:18571','labels':self.signal})
        self.am=Relay(18597,str(cert),str(key),18571,self.token,self.backend);self.am.run_id=self.run_id;self.am.labels=self.labels;self.am.work=self.work;self.am.start()
        self.entry=Relay(18595,str(cert),str(key),18596);self.entry.start()
        self.start_api()

    def start_api(self):
        self.stage='start_api'
        self.frozen();identity.free_port(18596);self.phase+=1
        path=self.work/('formal-api-'+str(self.phase)+'.log')
        with open(path,'ab') as stream:
            os.chmod(path,0o600);self.proc=subprocess.Popen([self.backend['binary']],cwd=self.root,env=command_env(self.variables,{}),stdout=stream,stderr=stream)
        self.state.update(pid=self.proc.pid,phase=self.phase);_private(self.work/'state.json',self.state)
        deadline=time.monotonic()+60
        while time.monotonic()<deadline:
            if self.proc.poll() is not None:raise AssertionError('formal silence binary exited during startup; private log retained')
            try:
                if request_local(18596,'GET','/healthz',timeout=2)[0]==200:return
            except (OSError,ValueError):pass
            time.sleep(.3)
        raise Blocked('formal silence binary readiness deadline exceeded')

    def call(self,method,path,body=None,cookie=None,want=200,headers=None,scenario=None):
        self.stage='formal_http '+method+' '+urllib.parse.urlsplit(path).path
        self.call_number+=1
        if not path.startswith('/api/v1/'):raise Blocked('formal silence API path boundary')
        h={'Content-Type':'application/json','Origin':ORIGIN,'X-AIOps-Environment':'test'};h.update(headers or {})
        if cookie:h['Cookie']='aiops_session='+cookie
        status,value,response_headers=request_local(18595,method,path,body,h,self.context)
        expected=want if isinstance(want,tuple) else (want,)
        if scenario is None:
            scenario='authorization' if all(x in (401,403) for x in expected) else 'idempotency' if expected==(409,) else 'failure' if all(x>=500 for x in expected) else 'boundary' if all(400<=x<500 for x in expected) else 'normal'
        clean_path=urllib.parse.urlsplit(path).path
        template=re.sub(r'(?<=/)(?:INV-[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{32})(?=/|$)','{id}',clean_path)
        positive_body=isinstance(value,dict) and bool(value) and 'error' not in value
        # 202 is the concrete successful pending-review contract for silence
        # submission and the first approval vote, not an anonymous rejection.
        positive_status=status in (200,201) or status==202 and positive_body and value.get('status')=='pending_review' and bool(value.get('proposal_id'))
        registered=status in expected and scenario in ('normal','idempotency') and positive_body and positive_status
        row=dict(name='silence.http.'+str(self.call_number),status='passed' if status in expected else 'failed',method=method,path=template,actual=status,expected=list(expected),test_name=self.test_name,scenario=scenario,registration_evidence=bool(registered))
        identity.emit(row['name'],row['status'],row['actual'],module=self.module,interface=dict(row),**{key:item for key,item in row.items() if key not in ('name','status','actual')})
        if status not in expected:
            failure=dict(stage='formal_http',method=method,path=clean_path,http_status=status,error=value.get('error') if isinstance(value,dict) else None)
            identity.preserve_first_failure(self.work,failure);_private(self.work/('http-red-'+str(self.call_number)+'.json'),failure)
            raise AssertionError('formal silence '+method+' '+row['path']+' status '+str(status)+' expected '+str(expected))
        return value,response_headers

    def login(self,uid):
        body,headers=self.call('POST','/api/v1/auth/login',{'email':uid+'@testlab.invalid','password':self.password})
        values=[x.split(';',1)[0].split('=',1)[1] for x in headers.get_all('Set-Cookie',[]) if x.startswith('aiops_session=')]
        if body.get('user',{}).get('id')!=uid or len(values)!=1:raise AssertionError('formal scoped human login/cookie identity mismatch')
        return values[0]

    def approve(self,pid):
        for index,(uid,cookie,authenticator) in enumerate(self.approvers):
            options,_=self.call('POST','/api/v1/proposals/'+pid+'/approve/options',{'confirm':True},cookie,want=201)
            credential=authenticator.assertion(options['options'].get('publicKey',options['options'])['challenge']);authenticator.save(self.work/('reviewer-'+str(index)+'.json'))
            vote,_=self.call('POST','/api/v1/proposals/'+pid+'/approve',{'challenge_id':options['challenge_id'],'credential':credential,'comment':'Owned real silence approval'},cookie,want=202 if index==0 else 200)
            if index==0:
                observed=self.observe("SELECT json_build_object('decisions',(SELECT count(*) FROM v3_proposal_approval_decisions WHERE proposal_id="+q(pid)+"),'claims',(SELECT count(*) FROM v3_silence_execution_claims WHERE proposal_id="+q(pid)+"));")
                if observed!={'decisions':0,'claims':0} or vote.get('status')!='pending_review' or vote.get('execution_id') or vote.get('received_approvals')!=1:raise AssertionError('first vote created execution authority')
                identity.emit('silence.first_vote','passed',observed,module=self.module)
            else:
                proof=self.observe("SELECT json_build_object('creator',d.creator_id,'first',v1.approver_id,'second',v2.approver_id,'execution',d.execution_id,'count',d.required_count) FROM v3_proposal_approval_decisions d JOIN v3_proposal_approval_votes v1 ON v1.id=d.first_vote_id JOIN v3_proposal_approval_votes v2 ON v2.id=d.second_vote_id WHERE d.proposal_id="+q(pid)+';')
                if proof['creator']!=self.uids[0] or {proof['first'],proof['second']}!=set(self.uids[1:]) or proof['count']!=2 or proof['execution']!=vote.get('execution_id') or vote.get('status')!='approved':raise AssertionError('second vote durable independent approval proof mismatch')

    def claim(self,pid):
        return self.observe("SELECT coalesce((SELECT json_build_object('state',c.state,'operation',c.create_operation_id,'upstream',c.upstream_id,'payload',o.payload) FROM v3_silence_execution_claims c JOIN v3_silence_create_operations o ON o.id=c.create_operation_id WHERE c.proposal_id="+q(pid)+"),'{}'::json);")

    def confirmed(self,pid):
        deadline=time.monotonic()+100
        while True:
            claim=self.claim(pid)
            if claim.get('state')=='confirmed':return claim
            if claim.get('state') in ('manual_review','expired'):
                _private(self.work/('claim-red-'+pid+'.json'),claim);raise AssertionError('real silence execution entered '+claim['state']+'; exact private payload retained')
            if time.monotonic()>deadline:
                _private(self.work/('claim-red-'+pid+'.json'),claim);raise AssertionError('real silence claim failed to confirm within bounded reconcile window')
            time.sleep(.5)

    def observed_silence(self,upstream):
        guard_component(self.backend,'alertmanager',18571)
        status,records,_=request_local(18571,'GET','/api/v2/silences')
        matched=[r for r in records if r.get('id')==upstream] if status==200 and isinstance(records,list) else []
        if len(matched)!=1:raise AssertionError('real complete Alertmanager snapshot lacks exact unique silence')
        return matched[0]

    def flow(self,mode):
        comment='Owned silence '+self.run_id+' '+mode
        canvas,_=self.call('POST','/api/v1/canvases',{'title':comment,'kind':'investigation','goal':'Controlled real Alertmanager silence '+mode,'services':['testlab'],'ai_draft':False},self.creator_cookie,want=201)
        inv=canvas.get('id') or canvas.get('investigation_id')
        if not re.fullmatch('(INV-)?[0-9a-f]{32}',str(inv)):raise AssertionError('formal open investigation identity missing')
        body={'investigation_id':inv,'matchers':[{'name':k,'op':'=','value':v} for k,v in self.labels.items() if k!='environment'],'duration_seconds':1800,'comment':comment}
        proposal,_=self.call('POST','/api/v1/silences',body,self.creator_cookie,want=202,headers={'Idempotency-Key':'silence-'+self.run_id+'-'+mode})
        if proposal.get('status')!='pending_review' or proposal.get('effect')!='no_upstream_write' or proposal.get('upstream_id') is not None or proposal.get('execution_status') is not None:raise AssertionError('silence submission wrote upstream or broke pending receipt')
        pid=proposal['proposal_id']
        if mode!='normal':self.am.fault('POST',comment,'crash' if mode=='crash' else 'drop')
        self.approve(pid)
        if mode=='crash':
            if not self.am.accepted.wait(40):raise AssertionError('owned crash fault did not reach actual accepted upstream POST')
            before=self.claim(pid)
            if before.get('state')!='dispatching':raise AssertionError('crash requires actual durable dispatching barrier')
            if self.proc.poll() is not None:raise AssertionError('owned product process already stopped before crash injection')
            self.proc.kill();self.proc.wait(timeout=5);self.proc=None;self.am.release.set()
            _private(self.work/'actual-crash-barrier.json',before);self.start_api()
        claim=self.confirmed(pid);upstream=claim['upstream'];actual=self.observed_silence(upstream)
        exact_create(actual,claim['payload'],claim['operation'],upstream)
        if self.am.count('POST',upstream,claim['operation'])!=1:raise AssertionError('real creation was dispatched more than once')
        public,_=self.call('GET','/api/v1/silences',cookie=self.creator_cookie)
        if not any(x.get('id')==upstream for x in public.get('silences',public.get('items',[]))):raise AssertionError('formal scoped silence read did not return created UUID')
        cancel,_=self.call('DELETE','/api/v1/silences/'+upstream,{'investigation_id':inv,'reason':'Owned short-chain expiration'},self.creator_cookie,want=202,headers={'Idempotency-Key':'cancel-'+self.run_id+'-'+mode})
        if cancel.get('status')!='pending_review' or cancel.get('effect')!='no_upstream_write':raise AssertionError('cancel submission bypassed pending review')
        if mode=='loss':self.am.fault('DELETE',upstream,'drop')
        self.approve(cancel['proposal_id']);cancel_claim=self.confirmed(cancel['proposal_id']);expired=self.observed_silence(upstream)
        if expired.get('status',{}).get('state')!='expired' or instant(expired['endsAt'])>=instant(actual['endsAt']) or expired.get('annotations')!=actual['annotations'] or cancel_claim['upstream']!=upstream or self.am.count('DELETE',upstream)!=1:raise AssertionError('real expiration/reconcile/write-once proof failed')
        result=dict(mode=mode,create='confirmed',cancel='confirmed',upstream_id=upstream,operation=claim['operation'],post_count=1,delete_count=1,real_prometheus_labels=True,upstream_expired=True)
        self.rows.append(result);_private(self.work/'flow-results.json',self.rows)
        identity.emit('silence.flow.'+mode,'passed',result,module=self.module)

    def run(self):
        self.prepare();self.creator_cookie=self.login(self.uids[0]);self.approvers=[]
        wa=load('silence_software_webauthn',Path(__file__).parent/'gitops/webauthn.py')
        for index,uid in enumerate(self.uids[1:]):
            cookie=self.login(uid);authenticator=wa.Authenticator(self.work,'reviewer-'+str(index),rp='testlab.local',origin=ORIGIN)
            options,_=self.call('POST','/api/v1/me/passkeys/registration/options',cookie=cookie,want=201)
            registration=authenticator.register(options['options'].get('publicKey',options['options'])['challenge'])
            self.call('POST','/api/v1/me/passkeys/registration/finish',{'challenge_id':options['id'],'credential':registration,'label':'Owned software ES256 silence review'},cookie,want=201)
            authenticator.save(self.work/('reviewer-'+str(index)+'.json'));self.approvers.append((uid,cookie,authenticator))
        self.flow('normal');self.flow('loss')
        if self.crash:self.flow('crash')
        self.frozen()
        return dict(flows=self.rows,crash_recovery_acceptance=self.crash,hardware_passkey_acceptance=False,bulk_group_history_acceptance=False,natural_1800s_expiry_acceptance=False)

    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;failures=[]
        try:receipt=identity.cleanup_process(self.proc,self.cleanup_deadline)
        except Exception as error:
            receipt={'error_type':type(error).__name__};failures.append('formal silence process cleanup failed')
        if receipt.get('forced_kill'):failures.append('formal silence process required cleanup SIGKILL')
        # Stop accepting and drain dispatched requests before the authoritative
        # teardown snapshot. A late AM response cannot evade marker cleanup.
        for relay in (self.entry,self.am):
            if relay:
                try:relay.close(self.cleanup_deadline)
                except Exception:failures.append('owned silence relay cleanup failed')
        if self.am:
            try:
                # Privileged fixture teardown only, never business acceptance.
                # Look up markers actually dispatched by this relay, including
                # uncertain POSTs lacking a response UUID. Preserve failed
                # product evidence and delete no pre-existing silence.
                guard_component(self.backend,'alertmanager',18571,self.cleanup_deadline)
                status,records,_=request_local(18571,'GET','/api/v2/silences',timeout=identity.remaining(self.cleanup_deadline,3))
                if status!=200 or not isinstance(records,list):raise AssertionError('owned cleanup requires complete AM snapshot')
                for actual in records:
                    operation=actual.get('annotations',{}).get('aiops_operation_id')
                    payload=self.am.payloads.get(operation)
                    if payload is None or actual.get('status',{}).get('state')=='expired':continue
                    exact_create(actual,payload,operation,actual['id'])
                    if payload.get('createdBy')!=self.uids[0]:raise AssertionError('owned cleanup creator mismatch')
                    guard_component(self.backend,'alertmanager',18571,self.cleanup_deadline)
                    status,_,_=request_local(18571,'DELETE','/api/v2/silence/'+actual['id'],timeout=identity.remaining(self.cleanup_deadline,3))
                    if status!=200:raise AssertionError('owned fixture teardown expiration failed')
                    identity.emit('silence.fixture_teardown','passed',{'upstream_id':actual['id'],'business_acceptance':False},module=self.module)
            except Exception:failures.append('owned AM silence fixture teardown failed; precise marker evidence retained')
        if self.seeded:
            try:
                self.sql('BEGIN; DELETE FROM sessions WHERE user_id IN ('+','.join(map(q,self.uids))+'); UPDATE users SET disabled=true WHERE id IN ('+','.join(map(q,self.uids))+'); DELETE FROM rbac_user_roles WHERE role_id IN ('+','.join(map(q,self.roles))+'); DELETE FROM v3_role_grants WHERE role_id IN ('+','.join(map(q,self.roles))+'); DELETE FROM rbac_roles WHERE id IN ('+','.join(map(q,self.roles))+'); COMMIT;')
            except Exception:failures.append('owned synthetic identities/roles cleanup failed')
        self.state.update(stopped=True,cleanup={'process':receipt,'failures':failures,'audit_and_execution_evidence_retained':True});_private(self.work/'state.json',self.state)
        if failures:raise AssertionError('; '.join(failures))
        identity.emit('silence.cleanup','passed',self.state['cleanup'],module=self.module)


def main():
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['doctor','chain']);parser.add_argument('--root',default=str(ROOT));parser.add_argument('--skip-crash',action='store_true');args=parser.parse_args();root=Path(args.root).resolve();chain=None;run_work=None
    fd=os.open(root/'.local/testlab/silence-chain.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    try:
        if os.fstat(fd).st_mode&0o777!=0o600:raise Blocked('private silence chain lock required')
        try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:raise Blocked('another owned silence chain owns fixed ports') from None
        backend,component=doctor(root);identity.emit('silence.doctor','passed',{'ports':[18595,18596,18597],'source_tree_sha':backend['source_tree_sha']},module='formal-silence')
        if args.command=='doctor':return 0
        run_id=uuid.uuid4().hex;run_work=root/'.local/testlab/environments'/backend['owner']/'silence-chain'/run_id
        chain=Chain(root,backend,component,run_id,not args.skip_crash);result=identity.execute(chain)
        _private(chain.work/'result.json',{'status':'passed','actual':result});identity.emit('silence.formal_chain','passed',result,module=chain.module,artifact=str((chain.work/'result.json').relative_to(root)));return 0
    except Exception as error:
        status='blocked' if isinstance(error,Blocked) else 'failed';message=identity.safe_error(error)
        if run_work:
            try:record_diagnostic(root,run_work,error,chain)
            except (OSError,ValueError) as diagnostic_error:identity.emit('silence.diagnostic_failure','failed',type(diagnostic_error).__name__,module='formal-silence')
        if chain:_private(chain.work/'result.json',{'status':status,'error':message})
        identity.emit('silence.formal_chain',status,message,module='formal-silence');return 2 if status=='blocked' else 1
    finally:os.close(fd)


if __name__=='__main__':
    with identity.cooperative_signals():raise SystemExit(main())
