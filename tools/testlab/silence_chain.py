"""Owned formal HTTPS + real ES256 approval -> real Alertmanager silences.

The bearer/TLS relay is a declared transport fixture; Alertmanager state and
dispatch are real. This does not attest bulk grouping or historical coverage.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, http.client, importlib.util, json, os, re, secrets, socket, ssl, stat, subprocess, threading, time, traceback, urllib.parse, uuid
from datetime import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import identity
import api_readonly_chain as readonly
import api_ingress_coverage as ingress
from env import _private, _inspect, _owner
from faults import Blocked, attest, private_json
from tls_entry import command_env

ROOT = Path(__file__).resolve().parents[2]
ORIGIN = 'https://testlab.local:18595'
ENDPOINT = 'https://localhost:18597'
q = identity.quote
LIMIT = 4 << 20
TARGETS={('GET','/api/v1/silences'),('POST','/api/v1/silences/preview'),('POST','/api/v1/silences'),('DELETE','/api/v1/silences/{id}'),('GET','/api/v1/proposals/{id}'),('POST','/api/v1/proposals/{id}/approve'),('POST','/api/v1/proposals/{id}/comments'),('POST','/api/v1/proposals/{id}/reject')}


def helper_hashes():
    names=('silence_chain.py','identity.py','api_readonly_chain.py','api_ingress_coverage.py','env.py','faults.py','tls_entry.py','gitops/webauthn.py','components/prometheus.yml','components/testlab-rules.yml','components/alertmanager.yml')
    return {'tools/testlab/'+name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest() for name in names}


def unique_operation(records,operation,upstream):
    found=[r for r in records if r.get('annotations',{}).get('aiops_operation_id')==operation]
    if len(found)!=1 or found[0].get('id')!=upstream:raise AssertionError('complete real AM snapshot has duplicate/missing operation UUID')
    return found[0]


def exact_fault(records,method,mode,operation=None,upstream=None):
    found=[r for r in records if r.get('method')==method and (operation is None or r.get('operation_id')==operation) and (upstream is None or r.get('upstream_id')==upstream)]
    if len(found)!=1 or found[0].get('status')!=200 or found[0].get('fault_mode')!=mode or found[0].get('fault_phase')!='response_closed':
        raise AssertionError('lost-response proof requires one actual accepted write and consumed socket close')
    return found[0]


def owned_source_config(application,environment_row,run_id,token_file,webhook):
    if not application or application=='testlab' or not environment_row:raise AssertionError('fresh HTTP-generated UUID application/environment required')
    source='silence-'+run_id
    operations={'sources':[{'id':source,'token_file':str(webhook),'mappings':[{'labels':{'application':'testlab','environment':'test'},'application_id':application,'environment_id':environment_row}]}],'feishu_channels':[]}
    am={'environments':{'test':{'url':ENDPOINT,'bearer_token_file':str(token_file),'allow_http':False,'environment_label':'environment','application_bindings':{'testlab':application},'source_ids':[source]}}}
    return operations,am


class DatabaseScope(readonly.Chain):
    """Reuse only fresh DB/role preparation and exact ownership cleanup."""
    def pg(self,*args,**kwargs):
        deadline=kwargs.get('deadline')
        info=identity.budgeted_inspect(self.data['postgres_container'],deadline)
        if info.get('Id')!=ingress.PG_CID:raise Blocked('silence immutable owned PG CID changed')
        return super().pg(*args,**kwargs)


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
    # The reused doctor performs source/build/nonce-HMAC attestation. Its
    # default API ports belong to another package, so scope this process's
    # port-only preflight to our listeners and immediately restore the module.
    previous=readonly.PORTS
    try:readonly.PORTS=(18595,18596,18597);backend=readonly.doctor(root)
    finally:readonly.PORTS=previous
    if backend['source_tree_sha']!=ingress.SOURCE_SHA:raise Blocked('silence frozen Go fingerprint differs')
    if identity.budgeted_inspect(backend['postgres_container']).get('Id')!=ingress.PG_CID:raise Blocked('silence immutable PG identity differs')
    if hashlib.sha256((ROOT/'tools/testlab/components/alertmanager.yml').read_bytes()).hexdigest()!=ingress.AM_SOURCE_SHA:raise Blocked('reviewed sole-local AM source changed')
    component = private_json(Path(root)/'.local/testlab/component-environment.json')
    work = Path(root)/'.local/testlab/environments'/backend['owner']
    if component.get('owner') != backend['owner'] or Path(component.get('work','')).resolve() != work.resolve():
        raise Blocked('silence component owner/work mismatch')
    for name, port in [('prometheus',18570),('alertmanager',18571)]:
        entries = [s for s in component.get('services',[]) if s.get('name') == name]
        if len(entries)!=1 or entries[0].get('container')!='aiops-'+backend['owner']+'-'+name or entries[0].get('url')!='http://127.0.0.1:'+str(port):
            raise Blocked('owned real Prometheus/Alertmanager component required')
        backend.setdefault('silence_components',{})[name]=guard_component(backend, name, port)
    backend['silence_helper_hashes']=helper_hashes()
    for port in (18595,18596,18597): identity.free_port(port)
    return backend, component


def guard_component(backend, name, port, deadline=None):
    info = identity.budgeted_inspect('aiops-'+backend['owner']+'-'+name, deadline)
    if backend['owner']!=readonly.OWNER or ingress.COMPONENTS.get(name,(None,))[0]!=port:raise Blocked('silence component scope differs')
    return ingress.component_guard(name,info,backend.get('silence_components',{}).get(name))


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
                if not relay.token and hasattr(relay,'chain'):
                    chain=relay.chain
                    if chain.proc is None or chain.proc.poll() is not None or not readonly._process_port(chain.proc.pid,18596):self.send_error(503);return
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
                            if relay.arm and relay.arm[0]==self.command and relay.arm[1]==(payload['comment'] if self.command=='POST' else row['upstream_id']):loss=relay.arm[2];relay.arm=None
                            row.update(fault_mode=loss,fault_phase='upstream_accepted' if loss else 'none')
                            relay.records.append(row)
                            if hasattr(relay,'work'):_private(relay.work/'am-write-receipts.json',relay.records)
                        if loss:
                            if response.status!=200:raise AssertionError('fault injection requires actual upstream success')
                            relay.accepted.set()
                            if loss=='crash':relay.release.wait(20)
                            self.close_connection=True
                            try:self.connection.shutdown(socket.SHUT_RDWR)
                            except OSError:pass
                            self.connection.close()
                            with relay.lock:
                                row['fault_phase']='response_closed'
                                if hasattr(relay,'work'):_private(relay.work/'am-write-receipts.json',relay.records)
                            return
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
        self.resource=None;self.config_hashes={};self.http_records={};self.qualified=set();self.receiver_load=None
        self.helper_sha256=backend.get('silence_helper_hashes',helper_hashes())
        self.data=dict(components=backend.get('silence_components',{}),helper_sha256=self.helper_sha256)
        self.stage='construct';self.uids=[uuid.uuid4().hex for _ in range(3)];self.roles=[uuid.uuid4().hex for _ in range(2)];self.password=secrets.token_urlsafe(32);self.creator_cookie=None
        self.image_hash=hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        self.state=dict(owner=backend['owner'],run_id=run_id,work=str(self.work),origin=ORIGIN,pid=None,uids=self.uids,roles=self.roles,
                        source_tree_sha=backend['source_tree_sha'],binary_sha256=self.image_hash,bulk_history_acceptance=False,helper_sha256=self.helper_sha256,shared_app_database_touched=False,ports=[18595,18596,18597],source_components=self.data['components'],postgres_container_id=ingress.PG_CID)
        _private(self.work/'state.json',self.state)

    def observe(self,statement):return json.loads(self.sql("BEGIN READ ONLY; SET LOCAL statement_timeout='3s';"+statement+'COMMIT;'))

    def sql(self,statement):
        if self.resource is None:raise Blocked('fresh UUID database must exist before silence SQL')
        deadline=getattr(self,'cleanup_deadline',None)
        info=identity.budgeted_inspect(self.backend['postgres_container'],deadline)
        if not info or not info.get('State',{}).get('Running'):raise Blocked('owned database not running')
        _owner(info,self.backend['owner'])
        if info.get('Id')!=ingress.PG_CID:raise Blocked('silence immutable owned PG CID changed')
        database,user=self.resource.database,self.resource.db_user
        if database!='aiops_testlab_readonly_'+uuid.UUID(self.run_id).hex or user!='readonly_'+uuid.UUID(self.run_id).hex:raise Blocked('silence SQL UUID database/role boundary changed')
        prefix='SET search_path=public; DO $silence$ BEGIN IF current_database()<>'+q(database)+' OR current_user<>'+q(user)+" THEN RAISE EXCEPTION 'silence SQL scope mismatch'; END IF; END $silence$; "
        argv=['docker','exec','-i','-e','PGAPPNAME='+self.resource.pg_application('sql'),self.backend['postgres_container'],'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U',user,'-d',database]
        result=subprocess.run(argv,input=prefix+statement,text=True,capture_output=True,timeout=identity.remaining(deadline,3 if deadline else 30))
        if result.returncode:
            number=getattr(self,'sql_failure_number',0)+1;self.sql_failure_number=number
            _private(self.work/('sql-failure-'+str(number)+'.json'),dict(command=argv,exit_code=result.returncode,stderr=result.stderr[:64<<10],stderr_truncated=len(result.stderr)>64<<10,statement_sha256=hashlib.sha256(statement.encode()).hexdigest()))
            if result.returncode==3:raise AssertionError('owned silence SQL fixture failed; private command and stderr retained')
            raise Blocked('owned silence database command failed; private command and stderr retained')
        return result.stdout.strip()

    def frozen(self):
        super().frozen()
        if helper_hashes()!=self.helper_sha256:raise Blocked('silence helper or mounted configuration changed')
        for name,expected in self.data['components'].items():guard_component(self.backend,name,expected['port'])
        for raw,expected in self.config_hashes.items():
            path=Path(raw)
            if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hashlib.sha256(path.read_bytes()).hexdigest()!=expected:raise Blocked('silence private source/TLS/runtime configuration changed')

    def pin_files(self,*paths):
        for path in paths:
            path=Path(path);self.config_hashes[str(path)]=hashlib.sha256(path.read_bytes()).hexdigest()

    def receiver_proof(self,stage):
        # Public pinned proof implementation is reused read-only, including the
        # actual mounted container bytes and official successful-load witness.
        return ingress.Chain.receiver_proof(self,stage)

    def seed_identities(self):
        self.seeded=True;statement='BEGIN;'
        for role in self.roles:statement+='INSERT INTO rbac_roles(id,name) VALUES('+q(role)+','+q('Owned silence acceptance '+role)+');'
        for index,uid in enumerate(self.uids):
            statement+='INSERT INTO users(id,email,name,role,password_hash) SELECT '+q(uid)+','+q(uid+'@testlab.invalid')+",'Owned silence human','viewer',password_hash FROM users WHERE id="+q(self.admin_id)+'; DELETE FROM rbac_user_roles WHERE user_id='+q(uid)+'; INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+q(uid)+','+q(self.roles[0 if index==0 else 1])+');'
        for role in self.roles:statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+q(role)+",'environment','view','environment','test');"
        for module,action in [('services','view'),('canvas','view'),('canvas','change'),('silences','view'),('silences','manage'),('proposals','view'),('proposals','change')]:statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+q(self.roles[0])+','+q(module)+','+q(action)+",'application',"+q(self.app)+');'
        for action,assurance in (('approve','passkey'),('view','standard'),('manage','standard')):statement+='INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES('+q(self.roles[1])+",'proposals',"+q(action)+",'application',"+q(self.app)+','+q(assurance)+');'
        self.sql(statement+' COMMIT;')
        if self.observe('SELECT count(*) FROM users WHERE id IN ('+','.join(map(q,self.uids))+');')!=3:raise Blocked('fresh synthetic human identities missing')

    def prepare(self):
        self.stage='prepare'
        self.resource=DatabaseScope(self.root,self.backend,str(uuid.UUID(self.run_id)))
        self.resource.state['ports']=[18596]
        _private(self.resource.work/'state.json',self.resource.state)
        self.resource.frozen=self.frozen
        self.resource.provision();self.password=self.resource.password
        self.state.update(database=self.resource.database,database_user=self.resource.db_user,owner_tag=self.resource.owner_tag,resource_evidence=str(self.resource.work))
        _private(self.work/'state.json',self.state)
        cert,key=self.work/'tls.crt',self.work/'tls.key'
        result=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','2','-subj','/CN=testlab.local','-addext','subjectAltName=DNS:testlab.local,DNS:localhost,IP:127.0.0.1'],capture_output=True,timeout=15)
        if result.returncode:raise Blocked('private silence TLS certificate generation failed')
        for path in (cert,key):os.chmod(path,0o600)
        self.context=ssl.create_default_context(cafile=str(cert));self.token=secrets.token_urlsafe(32)
        token_file=self.work/'am-token';readonly.private_file(token_file,self.token)
        webhook=self.work/'webhook-token';readonly.private_file(webhook,secrets.token_urlsafe(32))
        self.variables=dict(DATABASE_URL='postgres://'+self.resource.db_user+':'+self.resource.db_password+'@127.0.0.1:15461/'+self.resource.database+'?'+urllib.parse.urlencode(dict(sslmode='disable',options='-csearch_path=public',application_name=self.resource.pg_application('api',0))),
            AIOPS_V3_INSTANCE_CONFIG=str(self.resource.work/'instance.json'),AIOPS_V3_MODEL_BINDINGS_FILE=str(self.resource.work/'models.json'),AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.resource.work/'commands.json'),AIOPS_ADDR='127.0.0.1:18596',
            AIOPS_ADMIN_EMAIL=self.resource.admin_email,AIOPS_ADMIN_PASSWORD=self.password,AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32',
            AIOPS_PUBLIC_ORIGIN=ORIGIN,AIOPS_SECURE_COOKIES='true',AIOPS_V3_PASSKEY_ENABLED='false',AIOPS_V3_SILENCE_EXECUTION_ENABLED='false',
            SSL_CERT_FILE=str(cert),OPENAI_MODEL='readonly-synthetic',OPENAI_BASE_URL='http://127.0.0.1:9/v1',OPENAI_API_KEY='synthetic-no-inference',
            EMBEDDING_PROVIDER='openai',EMBEDDING_MODEL='readonly-synthetic',EMBEDDING_BASE_URL='http://127.0.0.1:9/v1',EMBEDDING_API_KEY='synthetic-no-embedding')
        self.pin_files(cert,key,token_file,webhook,*(self.resource.work/name for name in ('instance.json','models.json','commands.json','credentials.json')))
        _private(self.work/'api-config-bootstrap.json',self.variables);self.pin_files(self.work/'api-config-bootstrap.json')
        self.entry=Relay(18595,str(cert),str(key),18596);self.entry.chain=self;self.entry.start();self.start_api()
        login,headers=self.call('POST','/api/v1/auth/login',{'email':self.resource.admin_email,'password':self.password})
        cookies=[x.split(';',1)[0].split('=',1)[1] for x in headers.get_all('Set-Cookie',[]) if x.startswith('aiops_session=')]
        if len(cookies)!=1 or not login.get('user',{}).get('id'):raise AssertionError('fresh bootstrap login identity absent')
        self.admin_id=login['user']['id'];cookie=cookies[0]
        created,_=self.call('POST','/api/v1/services',{'items':[{'id':'testlab','tier':'T1'}]},cookie,want=201)
        if created!={'created':1}:raise AssertionError('fresh HTTP service producer result differs')
        mapping,_=self.call('GET','/api/v1/service-mappings',cookie=cookie)
        matches=[row for row in mapping['rows'] if row.get('service')=='testlab']
        if len(matches)!=1:raise AssertionError('fresh HTTP-produced application missing')
        self.app=matches[0]['save']['service_id']
        self.environment_row=self.observe("SELECT json_build_object('id',id) FROM application_environments WHERE application_id="+q(self.app)+" AND key='test';")['id']
        if not self.app or not self.environment_row:raise AssertionError('HTTP application environment source absent')
        self.seed_identities()
        deadline=time.monotonic()+30
        while True:
            guard_component(self.backend,'prometheus',18570);guard_component(self.backend,'alertmanager',18571)
            status,prom,_=request_local(18570,'GET','/api/v1/alerts');am_status,alerts,_=request_local(18571,'GET','/api/v2/alerts')
            actual=prom.get('data',{}).get('alerts',[]) if status==200 else []
            self.signal=next((a['labels'] for a in actual if a.get('state')=='firing' and a.get('labels',{}).get('application')=='testlab' and a['labels'].get('environment')=='test' and a['labels'].get('alertname')=='TestlabRequestFailures' and a['labels'].get('signal_id') and am_status==200 and any(x.get('labels')==a['labels'] for x in alerts)),None)
            if self.signal:break
            if time.monotonic()>deadline:raise Blocked('actual Prometheus firing alert lacks identical labels in owned AM')
            time.sleep(1)
        self.labels={k:self.signal[k] for k in ('application','environment','alertname','signal_id')}
        _private(self.work/'real-alert-source.json',{'prometheus':'owned:18570','alertmanager':'owned:18571','labels':self.signal,'application_id':self.app,'environment_row_id':self.environment_row,'data_scope':'current real owned alert, not historical collection'})
        self.receiver_proof('before-silence')
        receipt=identity.cleanup_process(self.proc)
        if receipt.get('forced_kill'):raise AssertionError('bootstrap API required forced stop')
        self.proc=None
        operations=self.work/'operations.json'
        config=self.work/'alertmanager.json'
        operational,am_config=owned_source_config(self.app,self.environment_row,self.run_id,token_file,webhook)
        _private(operations,operational);_private(config,am_config)
        self.variables.update(AIOPS_V3_PASSKEY_ENABLED='true',AIOPS_V3_SILENCE_EXECUTION_ENABLED='true',AIOPS_V3_ALERTMANAGER_CONFIG=str(config),AIOPS_OPERATIONS_CONFIG=str(operations))
        _private(self.work/'api-config-execution.json',self.variables);self.pin_files(operations,config,self.work/'api-config-execution.json')
        self.am=Relay(18597,str(cert),str(key),18571,self.token,self.backend);self.am.run_id=self.run_id;self.am.labels=self.labels;self.am.work=self.work;self.am.start()
        self.start_api()

    def start_api(self):
        self.stage='start_api'
        self.frozen();identity.free_port(18596);self.phase+=1
        path=self.work/('formal-api-'+str(self.phase)+'.log')
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
        try:
            with readonly.acquisition_signals():
                self.proc=subprocess.Popen([self.backend['binary']],cwd=self.root,env=command_env(self.variables,{}),stdout=fd,stderr=fd)
                self.resource.processes.append(self.proc)
                self.state.update(pid=self.proc.pid,phase=self.phase);_private(self.work/'state.json',self.state)
        finally:os.close(fd)
        deadline=time.monotonic()+60
        while time.monotonic()<deadline:
            if self.proc.poll() is not None:raise AssertionError('formal silence binary exited during startup; private log retained')
            try:
                if request_local(18596,'GET','/healthz',timeout=2)[0]==200:
                    if not readonly._process_port(self.proc.pid,18596):raise Blocked('held formal silence child does not own exact listener')
                    return
            except (OSError,ValueError):pass
            time.sleep(.3)
        raise Blocked('formal silence binary readiness deadline exceeded')

    def call(self,method,path,body=None,cookie=None,want=200,headers=None,scenario=None):
        self.frozen()
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
        row=dict(name='silence.http.'+str(self.call_number),status='passed' if status in expected else 'failed',method=method,path=template,actual=status,expected=list(expected),test_name=self.test_name,scenario=scenario,registration_evidence=False,run_id=self.run_id,source_tree_sha=self.backend['source_tree_sha'],evidence_mode='real_local',api_surface='main' if (method,template) in TARGETS else 'auxiliary_or_historical',validation_scope='HTTP status only; business semantics pending')
        response_file=self.work/('http-response-'+str(self.call_number)+'.json')
        saved=dict(row,response=value,request_environment=h['X-AIOps-Environment'],binary_sha256=self.image_hash,helper_sha256=self.helper_sha256,config_sha256=dict(self.config_hashes))
        _private(response_file,saved);self.http_records[self.call_number]=dict(row,response=value,file=str(response_file),sha256=hashlib.sha256(response_file.read_bytes()).hexdigest())
        identity.emit(row['name'],row['status'],row['actual'],module=self.module,interface=dict(row),artifact=str(response_file.relative_to(self.root)),**{key:item for key,item in row.items() if key not in ('name','status','actual','evidence_mode')})
        if status not in expected:
            failure=dict(stage='formal_http',method=method,path=clean_path,http_status=status,error=value.get('error') if isinstance(value,dict) else None)
            identity.preserve_first_failure(self.work,failure);_private(self.work/('http-red-'+str(self.call_number)+'.json'),failure)
            raise AssertionError('formal silence '+method+' '+row['path']+' status '+str(status)+' expected '+str(expected))
        return value,response_headers

    def qualify(self,call_id,case,proof):
        self.frozen();record=self.http_records[call_id]
        if (call_id in self.qualified or record['status']!='passed' or record['scenario'] not in ('normal','idempotency') or not 200<=record['actual']<300 or
                (record['method'],record['path']) not in TARGETS or not isinstance(proof,dict) or proof.get('verified') is not True):raise AssertionError('normal qualification requires unique exact target HTTP response and verified business proof')
        path=Path(record['file'])
        if path.is_symlink() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hashlib.sha256(path.read_bytes()).hexdigest()!=record['sha256']:raise Blocked('original HTTP response evidence changed before qualification')
        if any(proof.get(key,value)!=value for key,value in {'run_id':self.run_id,'source_tree_sha':self.backend['source_tree_sha']}.items()):raise AssertionError('qualified proof run/source scope mismatch')
        name=record['name']+'.qualified';qualified=dict(record,name=name,registration_evidence=True,validation_scope=case,original_call_id=call_id,response_evidence_id='http-response-'+str(call_id),response_sha256=record['sha256'],proof=proof)
        for field in ('response','file','sha256'):qualified.pop(field,None)
        _private(self.work/('qualified-'+str(call_id)+'.json'),qualified);self.qualified.add(call_id)
        identity.emit(name,'passed',dict(original_call_id=call_id,case=case,proof=proof),module=self.module,interface=qualified,artifact=str((self.work/('qualified-'+str(call_id)+'.json')).relative_to(self.root)))

    def expect_error(self,value,code):
        if not isinstance(value,dict) or value.get('error',{}).get('code')!=code:raise AssertionError('negative response code differs from declared contract')

    def login(self,uid):
        body,headers=self.call('POST','/api/v1/auth/login',{'email':uid+'@testlab.invalid','password':self.password})
        values=[x.split(';',1)[0].split('=',1)[1] for x in headers.get_all('Set-Cookie',[]) if x.startswith('aiops_session=')]
        if body.get('user',{}).get('id')!=uid or len(values)!=1:raise AssertionError('formal scoped human login/cookie identity mismatch')
        return values[0]

    def approve(self,pid):
        calls=[]
        for index,(uid,cookie,authenticator) in enumerate(self.approvers):
            options,_=self.call('POST','/api/v1/proposals/'+pid+'/approve/options',{'confirm':True},cookie,want=201)
            credential=authenticator.assertion(options['options'].get('publicKey',options['options'])['challenge']);authenticator.save(self.work/('reviewer-'+str(index)+'.json'))
            vote,_=self.call('POST','/api/v1/proposals/'+pid+'/approve',{'challenge_id':options['challenge_id'],'credential':credential,'comment':'Owned real silence approval'},cookie,want=202 if index==0 else 200)
            calls.append(self.call_number)
            if index==0:
                observed=self.observe("SELECT json_build_object('decisions',(SELECT count(*) FROM v3_proposal_approval_decisions WHERE proposal_id="+q(pid)+"),'claims',(SELECT count(*) FROM v3_silence_execution_claims WHERE proposal_id="+q(pid)+"));")
                if observed!={'decisions':0,'claims':0} or vote.get('status')!='pending_review' or vote.get('execution_id') or vote.get('received_approvals')!=1:raise AssertionError('first vote created execution authority')
                identity.emit('silence.first_vote.'+pid,'passed',observed,module=self.module)
            else:
                proof=self.observe("SELECT json_build_object('creator',d.creator_id,'first',v1.approver_id,'second',v2.approver_id,'execution',d.execution_id,'count',d.required_count) FROM v3_proposal_approval_decisions d JOIN v3_proposal_approval_votes v1 ON v1.id=d.first_vote_id JOIN v3_proposal_approval_votes v2 ON v2.id=d.second_vote_id WHERE d.proposal_id="+q(pid)+';')
                if proof['creator']!=self.uids[0] or {proof['first'],proof['second']}!=set(self.uids[1:]) or proof['count']!=2 or proof['execution']!=vote.get('execution_id') or vote.get('status')!='approved':raise AssertionError('second vote durable independent approval proof mismatch')
        return calls

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

    def observed_silence(self,upstream,operation=None):
        guard_component(self.backend,'alertmanager',18571)
        status,records,_=request_local(18571,'GET','/api/v2/silences')
        matched=[r for r in records if r.get('id')==upstream] if status==200 and isinstance(records,list) else []
        if len(matched)!=1:raise AssertionError('real complete Alertmanager snapshot lacks exact unique silence')
        if operation is not None:unique_operation(records,operation,upstream)
        return matched[0]

    def proposal_record(self,pid):
        row=self.observe("SELECT json_build_object('id',p.id,'status',p.status,'application',p.application_id,'environment',p.environment_id,'creator',p.created_by,'content',p.content,'created_at',p.created_at,'rejected_by',p.rejected_by,'reason',p.rejection_reason,'action',(SELECT to_jsonb(a) FROM v3_proposal_actions a WHERE a.proposal_id=p.id),'discussion',COALESCE((SELECT json_agg(json_build_object('id',c.id,'author_id',c.author_id,'author',COALESCE(NULLIF(u.name,''),c.author_id),'body',c.body,'time',c.created_at,'key',c.idempotency_key) ORDER BY c.created_at,c.id) FROM v3_proposal_comments c JOIN users u ON u.id=c.author_id WHERE c.proposal_id=p.id),'[]'::json),'audits',COALESCE((SELECT json_agg(json_build_object('actor',actor_id,'actor_kind',actor_kind,'application',application_id,'action',action,'target',target,'outcome',outcome,'changes',changes)) FROM management_audit WHERE target=p.id),'[]'::json),'decisions',(SELECT count(*) FROM v3_proposal_approval_decisions WHERE proposal_id=p.id),'claims',(SELECT count(*) FROM v3_silence_execution_claims WHERE proposal_id=p.id)) FROM v3_proposals p WHERE p.id="+q(pid)+';')
        number=getattr(self,'proposal_observation_number',0)+1;self.proposal_observation_number=number
        _private(self.work/('proposal-observation-'+str(number)+'.json'),dict(run_id=self.run_id,source_tree_sha=self.backend['source_tree_sha'],proposal_id=pid,record=row))
        return row

    def check_detail(self,value,row):
        if row['application']!=self.app or row['environment']!='test' or row['creator']!=self.uids[0] or value.get('id')!=row['id'] or value.get('status')!=row['status'] or any(value.get(key)!=expected for key,expected in row['content'].items()) or int(instant(value['proposed_at']).timestamp())!=int(instant(row['created_at']).timestamp()):raise AssertionError('proposal detail differs from exact HTTP-produced persisted scope/content')
        expected=[dict(author=item['author'],text=item['body'],time=int(instant(item['time']).timestamp())) for item in row['discussion']]
        actual=[dict(author=item['author'],text=item['text'],time=int(instant(item['time']).timestamp())) for item in value['discussion']]
        if actual!=expected:raise AssertionError('proposal discussion differs from exact stored comment records')

    def create_pending(self,label):
        comment='Owned silence '+self.run_id+' '+label
        canvas,_=self.call('POST','/api/v1/canvases',{'title':comment,'kind':'investigation','goal':'Controlled real Alertmanager silence '+label,'services':[self.app],'ai_draft':False},self.creator_cookie,want=201)
        inv=canvas.get('id') or canvas.get('investigation_id')
        if not re.fullmatch('(INV-)?[0-9a-f]{32}',str(inv)):raise AssertionError('formal open investigation identity missing')
        body={'investigation_id':inv,'matchers':[{'name':k,'op':'=','value':v} for k,v in self.labels.items() if k!='environment'],'duration_seconds':1800,'comment':comment}
        before=len(self.am.records)
        proposal,_=self.call('POST','/api/v1/silences',body,self.creator_cookie,want=202,headers={'Idempotency-Key':'silence-'+self.run_id+'-'+label})
        call_id=self.call_number;pid=proposal.get('proposal_id')
        if not re.fullmatch('[a-f0-9]{32}',str(pid)) or proposal.get('status')!='pending_review' or proposal.get('effect')!='no_upstream_write' or proposal.get('upstream_id') is not None or proposal.get('execution_status') is not None or not re.fullmatch('[a-f0-9]{64}',str(proposal.get('action_digest'))):raise AssertionError('silence submission broke immutable pending/no-write receipt')
        row=self.proposal_record(pid)
        if row['status']!='pending' or row['creator']!=self.uids[0] or row['application']!=self.app or row['environment']!='test' or row['content']['investigation_id']!=inv or row['decisions'] or row['claims'] or len(self.am.records)!=before:raise AssertionError('pending submission bypassed independent review or mismatched scope')
        self.check_pending_action(row,proposal,inv,'v3.proposal.silence_create_requested')
        return inv,body,proposal,call_id

    def check_pending_action(self,row,receipt,inv,action_name):
        action=row['action'];audits=[a for a in row['audits'] if a['action']==action_name]
        if not action or action['creator_id']!=self.uids[0] or action['application_id']!=self.app or action['environment_id']!='test' or action['investigation_id']!=inv or action['action_digest']!=receipt['action_digest'] or len(audits)!=1:raise AssertionError('pending proposal action/receipt/audit is not atomically linked to its exact human and scope')
        audit=audits[0];changes=audit['changes']
        if audit['actor']!=self.uids[0] or audit['actor_kind']!='user' or audit['application']!=self.app or audit['target']!=receipt['proposal_id'] or audit['outcome']!='success' or changes.get('environment_id')!='test' or changes.get('investigation_id')!=inv or changes.get('action_digest')!=action['action_digest'] or changes.get('review_content_digest')!=action['review_content_digest']:raise AssertionError('pending proposal audit digest/creator/scope differs from immutable action')

    def review_only(self):
        inv,body,proposal,create_id=self.create_pending('review-only');pid=proposal['proposal_id'];before=len(self.am.records)
        detail,_=self.call('GET','/api/v1/proposals/'+pid,cookie=self.creator_cookie);detail_id=self.call_number
        self.check_detail(detail,self.proposal_record(pid))
        text='Owned review evidence '+self.run_id;key='comment-'+self.run_id
        comment,_=self.call('POST','/api/v1/proposals/'+pid+'/comments',{'text':' '+text+' '},self.creator_cookie,want=201,headers={'Idempotency-Key':key});comment_id=self.call_number
        repeated,_=self.call('POST','/api/v1/proposals/'+pid+'/comments',{'text':text},self.creator_cookie,headers={'Idempotency-Key':key},scenario='idempotency');repeat_id=self.call_number
        if repeated!=comment or comment.get('text')!=text:raise AssertionError('normalized comment idempotency result differs')
        conflict,_=self.call('POST','/api/v1/proposals/'+pid+'/comments',{'text':'Different '+text},self.creator_cookie,want=409,headers={'Idempotency-Key':key},scenario='idempotency');self.expect_error(conflict,'idempotency_conflict')
        row=self.proposal_record(pid);comments=row['discussion'];audits=[a for a in row['audits'] if a['action']=='v3.proposal.comment_added']
        if len(comments)!=1 or comments[0]['author_id']!=self.uids[0] or comments[0]['body']!=text or comments[0]['key']!=key or comment.get('author')!=comments[0]['author'] or int(instant(comment['time']).timestamp())!=int(instant(comments[0]['time']).timestamp()) or len(audits)!=1 or audits[0]['actor']!=self.uids[0] or audits[0]['actor_kind']!='user' or audits[0]['application']!=self.app or audits[0]['outcome']!='success' or audits[0]['changes']!={'environment_id':'test','comment_id':comments[0]['id']}:raise AssertionError('comment/audit exact actor/idempotency/scope mismatch')
        updated,_=self.call('GET','/api/v1/proposals/'+pid,cookie=self.creator_cookie);updated_id=self.call_number;self.check_detail(updated,row)
        reason='Owned human rejection '+self.run_id;reviewer=self.approvers[0][1]
        rejected,_=self.call('POST','/api/v1/proposals/'+pid+'/reject',{'comment':reason},reviewer);reject_id=self.call_number
        repeated_reject,_=self.call('POST','/api/v1/proposals/'+pid+'/reject',{'comment':reason},reviewer,scenario='idempotency');reject_repeat_id=self.call_number
        if rejected!=repeated_reject:raise AssertionError('same reviewer/reason rejection replay differs')
        row=self.proposal_record(pid);self.check_detail(rejected,row)
        reject_audits=[a for a in row['audits'] if a['action']=='v3.proposal.rejected']
        if row['status']!='rejected' or row['rejected_by']!=self.uids[1] or row['reason']!=reason or len(reject_audits)!=1 or reject_audits[0]['actor']!=self.uids[1] or reject_audits[0]['actor_kind']!='user' or reject_audits[0]['application']!=self.app or reject_audits[0]['outcome']!='success' or reject_audits[0]['changes']!={'environment_id':'test','reason_present':True} or row['decisions'] or row['claims'] or len(self.am.records)!=before:raise AssertionError('human rejection dispatched work or audit/replay differs')
        closed,_=self.call('POST','/api/v1/proposals/'+pid+'/comments',{'text':'After close'},self.creator_cookie,want=409,headers={'Idempotency-Key':'closed-'+self.run_id});self.expect_error(closed,'proposal_closed')
        another,_=self.call('POST','/api/v1/proposals/'+pid+'/reject',{'comment':'Other reason'},self.approvers[1][1],want=409);self.expect_error(another,'proposal_closed')
        if self.proposal_record(pid)!=row or len(self.am.records)!=before:raise AssertionError('closed proposal mutation changed state or upstream')
        proof=dict(verified=True,proposal_id=pid,application=self.app,environment='test',comment_count=1,comment_audit=1,rejection_audit=1,independent_rejecter=self.uids[1],execution_claims=0,upstream_writes=0)
        for call_id in (create_id,detail_id,comment_id,repeat_id,updated_id,reject_id,reject_repeat_id):self.qualify(call_id,'silence.review_only',proof)
        self.negative_paths(pid,inv,body)
        identity.emit('silence.review_only','passed',proof,module=self.module)

    def negative_paths(self,pid,inv,body):
        before=self.proposal_record(pid);writes=len(self.am.records)
        for method,path,payload in [('GET','/api/v1/silences',None),('POST','/api/v1/silences/preview',{'matchers':[{'k':'application','op':'=','v':'testlab'}]}),('POST','/api/v1/silences',body),('DELETE','/api/v1/silences/12345678-1234-1234-1234-123456789abc',{'investigation_id':inv,'reason':'denied'}),('GET','/api/v1/proposals/'+pid,None),('POST','/api/v1/proposals/'+pid+'/comments',{'text':'denied'}),('POST','/api/v1/proposals/'+pid+'/reject',{'comment':'denied'}),('POST','/api/v1/proposals/'+pid+'/approve',{'challenge_id':'denied','credential':{}})]:
            denied,_=self.call(method,path,payload,want=401,headers={'Idempotency-Key':'denied-'+self.run_id},scenario='authorization');self.expect_error(denied,'unauthorized')
        denied,_=self.call('GET','/api/v1/silences',cookie=self.approvers[0][1],want=403,scenario='authorization');self.expect_error(denied,'forbidden')
        denied,_=self.call('POST','/api/v1/proposals/'+pid+'/reject',{'comment':'No manage grant'},self.creator_cookie,want=403,scenario='authorization');self.expect_error(denied,'forbidden')
        denied,_=self.call('GET','/api/v1/proposals/'+pid,cookie=self.creator_cookie,want=403,headers={'X-AIOps-Environment':'staging'},scenario='authorization');self.expect_error(denied,'forbidden')
        invalid,_=self.call('POST','/api/v1/silences/preview',{'matchers':[{'k':'application','op':'=~','v':'.*'}]},self.creator_cookie,want=422);self.expect_error(invalid,'invalid_matchers')
        invalid,_=self.call('POST','/api/v1/proposals/'+pid+'/comments',{'text':' '},self.creator_cookie,want=422);self.expect_error(invalid,'invalid_comment')
        invalid,_=self.call('POST','/api/v1/proposals/'+pid+'/reject',{'comment':'one','note':'other'},self.approvers[0][1],want=422);self.expect_error(invalid,'invalid_reason')
        missing,_=self.call('GET','/api/v1/proposals/'+'0'*32,cookie=self.creator_cookie,want=404);self.expect_error(missing,'not_found')
        if self.proposal_record(pid)!=before or len(self.am.records)!=writes:raise AssertionError('negative scope/body request changed proposal or wrote upstream')
        identity.emit('silence.authorization_boundary','passed',dict(state_unchanged=True,upstream_writes=0),module=self.module)

    def preview(self):
        before=len(self.am.records);guard_component(self.backend,'alertmanager',18571)
        status,alerts,_=request_local(18571,'GET','/api/v2/alerts')
        matches=[a for a in alerts if all(a['labels'].get(k)==v for k,v in self.labels.items())] if status==200 else []
        if not matches:raise Blocked('preview requires exact nonempty current owned alert snapshot')
        body={'matchers':[{'k':k,'op':'=','v':v} for k,v in self.labels.items()]}
        value,_=self.call('POST','/api/v1/silences/preview',body,self.creator_cookie)
        expected=dict(affects=len(matches),sample=sorted({a['labels']['alertname'] for a in matches})[:5])
        if value!=expected or len(self.am.records)!=before:raise AssertionError('preview count/sample differs from current precise real source or wrote upstream')
        self.qualify(self.call_number,'silence.preview',dict(verified=True,labels=self.labels,actual_snapshot_matches=len(matches),upstream_writes=0))
        identity.emit('silence.preview','passed',expected,module=self.module)

    def flow(self,mode):
        inv,body,proposal,create_id=self.create_pending(mode);pid=proposal['proposal_id'];comment=body['comment'];qualifications=[create_id]
        replay,_=self.call('POST','/api/v1/silences',body,self.creator_cookie,want=202,headers={'Idempotency-Key':'silence-'+self.run_id+'-'+mode},scenario='idempotency');qualifications.append(self.call_number)
        if replay!=proposal or self.proposal_record(pid)['claims'] or self.proposal_record(pid)['decisions']:raise AssertionError('create replay changed pending action or created execution authority')
        if mode!='normal':self.am.fault('POST',comment,'crash' if mode=='crash' else 'drop')
        qualifications+=self.approve(pid)
        if mode=='crash':
            if not self.am.accepted.wait(40):raise AssertionError('owned crash fault did not reach actual accepted upstream POST')
            before=self.claim(pid)
            if before.get('state')!='dispatching':raise AssertionError('crash requires actual durable dispatching barrier')
            if self.proc.poll() is not None:raise AssertionError('owned product process already stopped before crash injection')
            self.proc.kill();self.proc.wait(timeout=5);self.proc=None;self.am.release.set()
            _private(self.work/'actual-crash-barrier.json',before);self.start_api()
        claim=self.confirmed(pid);upstream=claim['upstream'];actual=self.observed_silence(upstream,operation=claim['operation'])
        exact_create(actual,claim['payload'],claim['operation'],upstream)
        payload=claim['payload']
        if payload['comment']!=comment or payload['createdBy']!=self.uids[0] or {m['name']:m['value'] for m in payload['matchers']}!=self.labels or any(m.get('isRegex') or not m.get('isEqual') for m in payload['matchers']):raise AssertionError('approved action differs from the original exact HTTP input')
        if self.am.count('POST',operation=claim['operation'])!=1:raise AssertionError('real creation operation dispatched more than once, including different UUIDs')
        if mode!='normal':exact_fault(self.am.records,'POST','crash' if mode=='crash' else 'drop',operation=claim['operation'])
        public,_=self.call('GET','/api/v1/silences',cookie=self.creator_cookie);qualifications.append(self.call_number)
        current=[x for x in public.get('items',[]) if x.get('id')==upstream]
        expected_matchers=sorted((k,v) for k,v in self.labels.items())
        if len(current)!=1 or current[0]['state']!='active' or current[0]['creator']!=self.uids[0] or current[0]['reason']!=comment or sorted((m['k'],m['v']) for m in current[0]['matchers'])!=expected_matchers or public.get('partial') is not False or public.get('unavailable_environments')!=[]:raise AssertionError('formal scoped silence DTO differs from exact current upstream record')
        cancel_body={'investigation_id':inv,'reason':'Owned short-chain expiration'};cancel_key='cancel-'+self.run_id+'-'+mode
        cancel_before=len(self.am.records)
        cancel,_=self.call('DELETE','/api/v1/silences/'+upstream,cancel_body,self.creator_cookie,want=202,headers={'Idempotency-Key':cancel_key});qualifications.append(self.call_number)
        if cancel.get('status')!='pending_review' or cancel.get('effect')!='no_upstream_write' or cancel.get('upstream_id') is not None or cancel.get('execution_status') is not None:raise AssertionError('cancel submission bypassed pending review')
        cancel_replay,_=self.call('DELETE','/api/v1/silences/'+upstream,cancel_body,self.creator_cookie,want=202,headers={'Idempotency-Key':cancel_key},scenario='idempotency');qualifications.append(self.call_number)
        if cancel_replay!=cancel:raise AssertionError('pending cancellation replay differs')
        pending_cancel=self.proposal_record(cancel['proposal_id'])
        if pending_cancel['status']!='pending' or pending_cancel['claims'] or pending_cancel['decisions'] or len(self.am.records)!=cancel_before:raise AssertionError('pending cancellation wrote upstream before its independent approval')
        self.check_pending_action(pending_cancel,cancel,inv,'v3.proposal.silence_cancel_requested')
        if mode=='loss':self.am.fault('DELETE',upstream,'drop')
        qualifications+=self.approve(cancel['proposal_id']);cancel_claim=self.confirmed(cancel['proposal_id']);expired=self.observed_silence(upstream,operation=claim['operation'])
        if expired.get('status',{}).get('state')!='expired' or instant(expired['endsAt'])>=instant(actual['endsAt']) or expired.get('annotations')!=actual['annotations'] or cancel_claim['upstream']!=upstream or self.am.count('DELETE',upstream)!=1:raise AssertionError('real expiration/reconcile/write-once proof failed')
        if mode=='loss':exact_fault(self.am.records,'DELETE','drop',upstream=upstream)
        result=dict(verified=True,mode=mode,create='confirmed',cancel='confirmed',upstream_id=upstream,operation=claim['operation'],post_count=1,delete_count=1,real_prometheus_labels=True,upstream_expired=True,application=self.app,environment='test',independent_humans=self.uids,consumed_faults=mode!='normal')
        self.rows.append(result);_private(self.work/'flow-results.json',self.rows)
        for call_id in qualifications:self.qualify(call_id,'silence.flow.'+mode,result)
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
        self.preview();self.review_only()
        self.flow('normal');self.flow('loss')
        if self.crash:self.flow('crash')
        self.receiver_proof('acceptance-end')
        self.frozen()
        covered={(self.http_records[i]['method'],self.http_records[i]['path']) for i in self.qualified}
        if covered!=TARGETS:raise AssertionError('eight formal targets lack qualified current semantic evidence')
        tables=('v3_proposals','v3_proposal_actions','v3_proposal_comments','v3_proposal_approval_votes','v3_proposal_approval_decisions','v3_silence_execution_claims','v3_silence_create_operations','management_audit')
        evidence={name:self.observe('SELECT COALESCE(json_agg(to_jsonb(t)),\'[]\'::json) FROM '+name+' t;') for name in tables}
        _private(self.work/'database-evidence.json',evidence)
        flows=3 if self.crash else 2
        if len(evidence['v3_proposals'])!=1+2*flows or len(evidence['v3_proposal_actions'])!=1+2*flows or len(evidence['v3_proposal_comments'])!=1 or len(evidence['v3_proposal_approval_votes'])!=4*flows or len(evidence['v3_proposal_approval_decisions'])!=2*flows or len(evidence['v3_silence_execution_claims'])!=2*flows or len(evidence['v3_silence_create_operations'])!=flows or any(c['state']!='confirmed' for c in evidence['v3_silence_execution_claims']):raise AssertionError('fresh database contains unexpected/missing producer, vote, claim or dispatch records')
        return dict(flows=self.rows,qualified_routes=sorted(covered),qualified_responses=len(self.qualified),http_responses=self.call_number,source_tree_sha=self.backend['source_tree_sha'],binary_sha256=self.image_hash,helper_sha256=self.helper_sha256,config_sha256=self.config_hashes,components=self.data['components'],postgres_container_id=ingress.PG_CID,database=self.resource.database,application_id=self.app,real_alert_labels=self.labels,crash_recovery_acceptance=self.crash,hardware_passkey_acceptance=False,bulk_group_history_acceptance=False,natural_1800s_expiry_acceptance=False,transport_scope='owned HTTPS/bearer relay fixture; actual Prom/AM state and dispatch',unverified=['external vendor credentials/real hardware passkeys','bulk-group history and natural 1800s expiry','production SLA/performance/24h continuity','distributed multi-worker crash/revoke races'])

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
                    identity.emit('silence.fixture_teardown.'+actual['id'],'passed',{'upstream_id':actual['id'],'business_acceptance':False},module=self.module)
            except Exception:failures.append('owned AM silence fixture teardown failed; precise marker evidence retained')
        if self.seeded:
            try:
                self.sql('BEGIN; DELETE FROM sessions WHERE user_id IN ('+','.join(map(q,self.uids))+'); UPDATE users SET disabled=true WHERE id IN ('+','.join(map(q,self.uids))+'); DELETE FROM rbac_user_roles WHERE role_id IN ('+','.join(map(q,self.roles))+'); DELETE FROM v3_role_grants WHERE role_id IN ('+','.join(map(q,self.roles))+'); DELETE FROM rbac_roles WHERE id IN ('+','.join(map(q,self.roles))+'); COMMIT;')
            except Exception:failures.append('owned synthetic identities/roles cleanup failed')
        if self.resource:
            try:self.resource.cleanup()
            except Exception:failures.append('owned UUID database/role cleanup failed; exact private resource evidence retained')
        self.state.update(stopped=True,cleanup={'process':receipt,'failures':failures,'audit_and_execution_evidence_retained':True,'evidence_location':'private response/DB exports; fresh UUID DB is dropped only after ownership/quiescence proof'});_private(self.work/'state.json',self.state)
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
