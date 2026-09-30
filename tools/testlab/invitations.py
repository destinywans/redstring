"""Formal HTTPS V3 invitations through an owned TLS465 SMTP capture sink."""
from __future__ import annotations
import argparse,contextlib,email.utils,hashlib,http.client,json,os,re,secrets,signal,socket,ssl,subprocess,sys,threading,time,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlparse
from runner import command_env,source_fingerprint
from env import _private

ROOT=Path(__file__).resolve().parents[2]
IMAGE='python:3.13.7-alpine3.22'
LABEL='aiops.testlab.owner'
RUN_LABEL='aiops.testlab.invitation_run'
SMTP_PORT=465
HTTPS_PORT=18588
API_PORT=18589
ORIGIN=f'https://testlab.local:{HTTPS_PORT}'
_interrupt_signal=None
_active_work=None
_cleanup_depth=0

class TestlabInterrupted(BaseException):
    """Unwind owned resources without converting cancellation into success."""

@contextlib.contextmanager
def cooperative_sigterm():
    global _interrupt_signal,_active_work
    _interrupt_signal=None;_active_work=None
    def stop(signum,frame):
        global _interrupt_signal
        if _interrupt_signal is not None:return
        _interrupt_signal=signum
        if not _cleanup_depth:raise TestlabInterrupted()
    previous=signal.signal(signal.SIGTERM,stop)
    try:yield
    finally:
        if _interrupt_signal is not None and _active_work is not None:
            _private(_active_work/'interrupted-exit.json',{'signal':_interrupt_signal,'pid':os.getpid(),'at':time.time(),
                      'cleanup_confirmed':(_active_work/'cleanup-confirmed.json').exists()})
        signal.signal(signal.SIGTERM,previous)

@contextlib.contextmanager
def interruption_cleanup():
    # A first TERM can arrive while ordinary finally is already stopping
    # resources. Record it but do not abort that cleanup halfway through.
    global _cleanup_depth
    _cleanup_depth+=1
    try:yield
    finally:_cleanup_depth-=1

def emit(name,status,actual,started=None,**fields):
    print(json.dumps(dict(id=name,name=name,module='member-invitations',type='integration',status=status,
          evidence_mode='real_local',actual=actual,duration=time.monotonic()-started if started else 0,
          **fields),ensure_ascii=False),flush=True)

def port_free(port):
    with socket.socket() as s:
        s.settimeout(.3)
        if s.connect_ex(('127.0.0.1',port))==0:raise RuntimeError(f'owned invitation port {port} occupied')

def command(argv,timeout=60,log=None):
    p=subprocess.run(argv,capture_output=True,text=True,timeout=timeout)
    if p.returncode:
        if log:
            fd=os.open(log,os.O_WRONLY|os.O_APPEND|os.O_CREAT|getattr(os,'O_NOFOLLOW',0),0o600)
            with os.fdopen(fd,'a') as stream:stream.write(p.stderr[-12000:]);stream.flush()
        raise RuntimeError(f'{argv[0]} command failed; no credentials or private logs published')
    return p.stdout.strip()

def owned_fixture():
    # Reuse the generated formal DB/account, never read product .env. This
    # helper intentionally does not create performance data or credentials.
    path=ROOT/'.local/testlab/backend-fixture.json';env_path=ROOT/'.local/testlab/environment.json'
    if any(p.is_symlink() or not p.is_file() or p.stat().st_mode&0o777!=0o600 for p in (path,env_path)):
        raise RuntimeError('private formal fixture and environment required')
    data=json.loads(path.read_text());env=json.loads(env_path.read_text());dsn=urlparse(data['database_url'])
    if not re.fullmatch(r'testlab-[a-f0-9]{12}',data['owner']) or data['owner']!=env['owner'] or data['postgres_container']!=env['container'] or env['container']!='aiops-'+data['owner']+'-pg' or dsn.hostname!='127.0.0.1' or dsn.port!=15461 or dsn.username!='testlab' or dsn.path!='/aiops_testlab_app' or data['database_url']!=env.get('env',{}).get('TESTLAB_APP_DSN'):
        raise RuntimeError('formal invitation owner/database mismatch')
    expected=ROOT/'.local/testlab/environments'/data['owner']
    if Path(env['work']).resolve()!=expected or Path(data['instance_config']).resolve()!=expected/'instance.json' or Path(data['instance_config']).is_symlink() or Path(data['work']).resolve()!=expected/'backend' or Path(data['binary']).resolve()!=expected/'backend/aiops':
        raise RuntimeError('formal invitation owned workspace mismatch')
    labels=json.loads(command(['docker','inspect',data['postgres_container'],'--format','{{json .Config.Labels}}']))
    if labels.get(LABEL)!=data['owner']:raise RuntimeError('formal PG owner label mismatch')
    if source_fingerprint(ROOT)!=data['source_tree_sha']:raise RuntimeError('formal binary differs from current source; root rebuild required')
    if json.loads(Path(data['instance_config']).read_text()).get('domain')!='testlab.local':raise RuntimeError('owned HTTPS domain mismatch')
    return data,env

def setup():
    global _active_work
    data,environment=owned_fixture()
    for port in (SMTP_PORT,HTTPS_PORT,API_PORT):port_free(port)
    work=Path(environment['work'])/'invitations'/str(uuid.uuid4());work.mkdir(parents=True,mode=0o700)
    _active_work=work
    config={'owner':data['owner'],'run_id':work.name,'work':str(work),'container':'aiops-'+data['owner']+'-smtp-'+work.name[:8],
            'username':'testlab-invitation','password':secrets.token_urlsafe(32),'image':IMAGE,
            'source_tree_sha':data['source_tree_sha'],'domain':'testlab.invalid','origin':ORIGIN,'container_started':False}
    _private(work/'smtp-config.json',{'username':config['username'],'password':config['password']})
    _private(work/'mode.json',{'mode':'accept'})
    password=work/'smtp-password';password.write_text(config['password']);os.chmod(password,0o600)
    for stem,domain,san in [('smtp','localhost','DNS:localhost,IP:127.0.0.1'),('entry','testlab.local','DNS:testlab.local,IP:127.0.0.1')]:
        command(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(work/(stem+'.key')),
                 '-out',str(work/(stem+'.crt')),'-days','3','-subj','/CN='+domain,'-addext','subjectAltName='+san],log=work/'setup-errors.log')
        os.chmod(work/(stem+'.key'),0o600);os.chmod(work/(stem+'.crt'),0o600)
    runtime=json.loads((ROOT/'.local/testlab/runtime-config.json').read_text())
    if runtime['owner']!=data['owner']:raise RuntimeError('runtime owner mismatch')
    bundle=work/'trusted-ca.pem';original=runtime.get('env',{}).get('SSL_CERT_FILE')
    bundle.write_bytes((Path(original).read_bytes() if original else b'')+b'\n'+(work/'smtp.crt').read_bytes()+b'\n'+(work/'entry.crt').read_bytes());os.chmod(bundle,0o600)
    config['product_env']={'AIOPS_PUBLIC_ORIGIN':ORIGIN,'AIOPS_SECURE_COOKIES':'true','SSL_CERT_FILE':str(bundle),
          'AIOPS_V3_INVITATION_SMTP_HOST':'localhost','AIOPS_V3_INVITATION_SMTP_FROM':'noreply@testlab.invalid',
          'AIOPS_V3_INVITATION_SMTP_USERNAME':config['username'],'AIOPS_V3_INVITATION_SMTP_PASSWORD_FILE':str(password),
          'AIOPS_V3_INVITATION_ENTRY_URL':ORIGIN+'/accept-invitation'}
    _private(work/'environment.json',config)
    # Immutable image identity is recorded even if the fixed tag is refreshed.
    command(['docker','pull',IMAGE],timeout=120,log=work/'setup-errors.log')
    config['image_id']=command(['docker','image','inspect',IMAGE,'--format','{{.Id}}'])
    try:
        command(['docker','run','-d','--name',config['container'],'--label',LABEL+'='+data['owner'],'--label',RUN_LABEL+'='+config['run_id'],
                 '--memory','128m','--cpus','.5','--cap-drop','ALL','--security-opt','no-new-privileges',
                 '-p','127.0.0.1:465:465','-v',str(work)+':/work',
                 '-v',str(ROOT/'tools/testlab/smtp_capture.py')+':/smtp_capture.py:ro',config['image_id'],
                 'python','-B','/smtp_capture.py','/work'],log=work/'setup-errors.log')
        config['container_started']=True;_private(work/'environment.json',config)
        context=ssl.create_default_context(cafile=str(bundle));deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            try:
                with socket.create_connection(('127.0.0.1',465),timeout=2) as raw:
                    with context.wrap_socket(raw,server_hostname='localhost') as conn:
                        if conn.recv(1024).startswith(b'220 '):return data,config
            except OSError:time.sleep(.2)
        raise RuntimeError('owned TLS SMTP sink did not become ready')
    except BaseException:
        try:cleanup(config)
        except (OSError,RuntimeError,ValueError,subprocess.SubprocessError) as exc:
            _private(work/'cleanup-failure.json',{'error_type':type(exc).__name__,'detail':str(exc)})
            emit('smtp.cleanup','blocked',{'error_type':type(exc).__name__,'private_evidence_dir':str(work.relative_to(ROOT))})
        raise

@contextlib.contextmanager
def formal_entry(data,config):
    work=Path(config['work']);runtime=json.loads((ROOT/'.local/testlab/runtime-config.json').read_text())
    if runtime['owner']!=data['owner']:raise RuntimeError('runtime owner mismatch')
    variables={'DATABASE_URL':data['database_url'],'AIOPS_V3_INSTANCE_CONFIG':data['instance_config'],
        'AIOPS_ADDR':f'127.0.0.1:{API_PORT}','AIOPS_ADMIN_EMAIL':data['admin']['email'],
        'AIOPS_ADMIN_PASSWORD':data['admin']['password'],'AIOPS_TRUSTED_PROXY_CIDRS':'127.0.0.1/32',
        'OPENAI_MODEL':'testlab','OPENAI_BASE_URL':'http://127.0.0.1:18578/v1','OPENAI_API_KEY':'synthetic-testlab-key',
        'EMBEDDING_PROVIDER':'openai','EMBEDDING_MODEL':'synthetic-testlab','EMBEDDING_BASE_URL':'http://127.0.0.1:18578/v1','EMBEDDING_API_KEY':'synthetic-testlab-key'}
    variables.update(runtime.get('env',{}));variables.pop('AIOPS_V3_ALERT_RULE_TRIAL_CONFIG',None);variables.update(config['product_env'])
    log=os.open(work/'formal-api.log',os.O_CREAT|os.O_APPEND|os.O_WRONLY,0o600)
    proc=subprocess.Popen([data['binary']],cwd=ROOT,env=command_env(variables,{}),stdout=log,stderr=log);os.close(log);server=None
    class Proxy(BaseHTTPRequestHandler):
        def log_message(self,*args):pass
        def relay(self):
            if not (self.path.startswith('/api/v1/') or self.path=='/healthz'):self.send_error(404);return
            try:
                length=int(self.headers.get('Content-Length','0'))
                if not 0<=length<=65536:self.send_error(413);return
                raw=self.rfile.read(length) if length else None
                headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','connection','transfer-encoding','content-length','x-forwarded-for')}
                headers['X-Forwarded-For']='127.0.0.1'
                connection=http.client.HTTPConnection('127.0.0.1',API_PORT,timeout=30);connection.request(self.command,self.path,raw,headers)
                response=connection.getresponse();body=response.read(1<<20)
                self.send_response(response.status)
                for key,value in response.getheaders():
                    if key.lower() not in ('server','date','connection','transfer-encoding','content-length'):self.send_header(key,value)
                self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);connection.close()
            except (ValueError,OSError):self.send_error(502)
        do_GET=do_POST=do_DELETE=relay
    try:
        deadline=time.monotonic()+60
        while time.monotonic()<deadline:
            if proc.poll() is not None:raise RuntimeError('formal SMTP entry exited; private formal-api.log retained')
            try:
                connection=http.client.HTTPConnection('127.0.0.1',API_PORT,timeout=2);connection.request('GET','/healthz');response=connection.getresponse();response.read();connection.close()
                if response.status==200:break
            except OSError:pass
            time.sleep(.2)
        else:raise RuntimeError('formal SMTP entry readiness timeout')
        server=ThreadingHTTPServer(('127.0.0.1',HTTPS_PORT),Proxy);context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(work/'entry.crt',work/'entry.key');server.socket=context.wrap_socket(server.socket,server_side=True)
        thread=threading.Thread(target=lambda:server.serve_forever(poll_interval=.1),daemon=True);thread.start()
        yield
    finally:
        with interruption_cleanup():
            if server:server.shutdown();server.server_close()
            forced_kill=False
            if proc.poll() is None:
                proc.terminate()
                try:proc.wait(timeout=20)
                except subprocess.TimeoutExpired:
                    forced_kill=True;proc.kill();proc.wait(timeout=5)
            _private(work/'process-exit.json',{'pid':proc.pid,'returncode':proc.returncode,'forced_kill':forced_kill,'at':time.time()})

class Client:
    def __init__(self,config):
        self.context=ssl.create_default_context(cafile=str(Path(config['work'])/'trusted-ca.pem'));self.cookie=None
    def request(self,method,path,body=None,cookie=True):
        if not path.startswith('/api/v1/'):raise ValueError('formal API path required')
        headers={'Host':f'testlab.local:{HTTPS_PORT}','Origin':ORIGIN,'X-AIOps-Environment':'test','Content-Type':'application/json'}
        if cookie and self.cookie:headers['Cookie']=self.cookie
        connection=http.client.HTTPSConnection('127.0.0.1',HTTPS_PORT,context=self.context,timeout=30)
        connection.request(method,path,json.dumps(body) if body is not None else None,headers)
        response=connection.getresponse();raw=response.read(1<<20);returned=dict(response.getheaders());status=response.status;connection.close()
        return status,json.loads(raw) if raw else {},returned
    def login(self,email,password):
        status,body,headers=self.request('POST','/api/v1/auth/login',{'email':email,'password':password},cookie=False)
        check(status==200,'formal password login did not succeed')
        header=headers.get('Set-Cookie','');check('Secure' in header and 'HttpOnly' in header,'formal cookie protection missing')
        self.cookie=header.split(';',1)[0];return body

def check(condition,message):
    if not condition:raise AssertionError(message)

def sql(data,statement):
    p=subprocess.run(['docker','exec','-i',data['postgres_container'],'psql','-X','-qAt','-v','ON_ERROR_STOP=1','-U','testlab','-d','aiops_testlab_app'],
           input='BEGIN READ ONLY; '+statement+'; COMMIT;',capture_output=True,text=True,timeout=15)
    if p.returncode:raise RuntimeError('owned read-only invitation evidence query failed')
    return json.loads(p.stdout.strip())

def snapshot(data,address):
    if not re.fullmatch(r'invite-[a-f0-9]+@testlab\.invalid',address):raise ValueError('generated local recipient required')
    return sql(data,f"""SELECT json_build_object('invitations',COALESCE((SELECT json_agg(json_build_object(
      'id',id,'status',status,'delivered',delivered_at IS NOT NULL,'accepted',accepted_at IS NOT NULL,
      'account_id',account_id,'relay_audits',(SELECT count(*) FROM management_audit WHERE action='v3.member.invitation.relay_accepted' AND target=i.id),
      'accept_audits',(SELECT count(*) FROM management_audit WHERE action='v3.member.invitation.accepted' AND changes->>'invitation_id'=i.id)))
      FROM v3_member_invitations i WHERE email='{address}'),'[]'::json),'users',
      (SELECT count(*) FROM users WHERE email='{address}'),'environment_grants',
      (SELECT count(*) FROM v3_effective_role_grants g JOIN users u ON g.user_id=u.id WHERE u.email='{address}' AND g.module='environment'))""")

def captured(config,address):
    path=Path(config['work'])/'captures.jsonl'
    if path.is_symlink() or (path.exists() and path.stat().st_mode&0o777!=0o600):raise RuntimeError('private SMTP capture required')
    records=[json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []
    rows=[r for r in records if r['recipient']==address]
    check(len(rows)==1,'expected exactly one private SMTP handoff')
    code=re.search(r'^One-time invitation code: ([A-Za-z0-9_-]{43})\r?$',rows[0]['message'],re.MULTILINE)
    check(code is not None,'SMTP invitation code missing')
    check(ORIGIN+'/accept-invitation' in rows[0]['message'],'email trusted entry differs from HTTPS origin')
    return code.group(1),rows[0]['outcome']

def cleanup(config):
    with interruption_cleanup():return _cleanup(config)

def _cleanup(config):
    run=str(uuid.UUID(config['run_id']))
    expected=ROOT/'.local/testlab/environments'/config['owner']/'invitations'/run
    if run!=config['run_id'] or not re.fullmatch(r'testlab-[a-f0-9]{12}',config['owner']) or Path(config['work']).resolve()!=expected or config['container']!='aiops-'+config['owner']+'-smtp-'+run[:8]:
        raise RuntimeError('SMTP cleanup exact owned resource/work/run required')
    inspected=subprocess.run(['docker','inspect',config['container']],capture_output=True,text=True,timeout=5 if _interrupt_signal is not None else 15)
    if inspected.returncode:
        if 'No such object' in inspected.stderr or 'No such container' in inspected.stderr:
            if Path(config['work']).is_dir():_private(Path(config['work'])/'cleanup-confirmed.json',{'container_exists':False,'at':time.time()})
            return
        raise RuntimeError('SMTP cleanup cannot confirm Docker resource identity')
    records=json.loads(inspected.stdout)
    if len(records)!=1:raise RuntimeError('SMTP cleanup invalid inspection result')
    container=records[0];labels=container['Config'].get('Labels',{})
    if labels.get(LABEL)!=config['owner'] or labels.get(RUN_LABEL)!=run or container.get('Image')!=config['image_id'] or container.get('Name')!='/'+config['container']:
        raise RuntimeError('SMTP cleanup owner/run/image/name mismatch')
    command(['docker','stop','--time','5',config['container']],timeout=10 if _interrupt_signal is not None else 15)
    _private(Path(config['work'])/'cleanup-confirmed.json',{'container_stopped':True,'at':time.time()})

def chain():
    config=None;started=time.monotonic()
    try:
        data,config=setup();work=Path(config['work'])
        emit('smtp.environment','passed',{'ports':[465,HTTPS_PORT,API_PORT],'image_id':config['image_id'],'source_tree_sha':data['source_tree_sha'],
             'transport':'real TLS SMTP AUTH; controlled capture sink','external_mailbox_verified':False},started)
        with formal_entry(data,config):
            admin=Client(config);admin.login(data['admin']['email'],data['admin']['password'])
            evidence=[]
            for mode in ['accept','reject_data','lose_data_response','lose_quit_response']:
                began=time.monotonic();address='invite-'+uuid.uuid4().hex+'@testlab.invalid';password=secrets.token_urlsafe(24)
                _private(work/'mode.json',{'mode':mode})
                status,body,_=admin.request('POST','/api/v1/members/invitations',{'email':address,'role':'viewer'})
                _private(work/(mode+'-http.json'),{'status':status,'body':body,'address':address})
                code,outcome=captured(config,address);before=snapshot(data,address)
                _private(work/(mode+'-before.json'),before)
                row=before['invitations'][0] if len(before['invitations'])==1 else None
                check(row is not None,'invitation ledger missing or duplicate')
                client=Client(config)
                if mode in ('accept','lose_quit_response'):
                    check(status==200,'relay-confirmed invitation HTTP did not succeed')
                    check(row['status']=='delivered' and row['delivered'] and row['relay_audits']==1,'relay success not committed with exactly one audit')
                    accepted,body,_=client.request('POST','/api/v1/auth/invitations/accept',{'code':code,'name':'Synthetic invited member','password':password},cookie=False)
                    check(accepted==201 and body.get('access')=='pending_scope_grant','delivered code did not create pending-scope account')
                    client.login(address,password)
                    denied,_,_=client.request('GET','/api/v1/canvases');check(denied==403,'new member acquired environment access')
                    again,_,_=client.request('POST','/api/v1/auth/invitations/accept',{'code':code,'name':'Duplicate','password':password},cookie=False)
                    check(again==409,'consumed invitation code accepted again')
                    after=snapshot(data,address);check(after['users']==1 and after['environment_grants']==0,'account grant boundary or uniqueness failed')
                    row=after['invitations'][0];check(row['status']=='accepted' and row['accepted'] and row['accept_audits']==1,'account acceptance missing durable audit')
                else:
                    check(status==503 and body.get('error',{}).get('code')=='invitation_unavailable','DATA failure did not report unavailable')
                    check(row['status']=='superseded' and not row['delivered'] and row['relay_audits']==0,'failed handoff left usable delivery claim')
                    rejected,_,_=client.request('POST','/api/v1/auth/invitations/accept',{'code':code,'name':'Uncertain member','password':password},cookie=False)
                    check(rejected==409,'rejected or uncertain SMTP code accepted')
                    after=snapshot(data,address);check(after['users']==0,'failed SMTP handoff created account')
                proof={'mode':mode,'smtp_outcome':outcome,'invite_http':status,'before':before,'after':after,'private_capture_count':1,
                       'source_tree_sha':data['source_tree_sha'],'model':'not involved','external_delivery':'not verified'}
                evidence.append(proof);_private(work/'proofs.json',evidence)
                emit('smtp.'+mode,'passed',proof,began)
        emit('smtp.scope','passed',{'covered':'formal HTTPS/SecureCookie, SMTP TLS465 AUTH, durable handoff/acceptance, DATA rejection/lost acknowledgement, uncertain QUIT',
             'not_verified':['external recipient mailbox delivery','process crash between relay acceptance and local delivery commit','arbitrary DB commit fault injection']})
    except TestlabInterrupted:
        emit('smtp.interrupted','blocked',{'reason':'runner SIGTERM interrupted this owned invitation chain'},started)
        return 128+signal.SIGTERM
    except AssertionError as exc:
        if config:
            red=ROOT/'.local/testlab/first-failures'/('invitation-http-red-'+uuid.uuid4().hex+'.json')
            red.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
            fd=os.open(red,os.O_WRONLY|os.O_CREAT|os.O_EXCL|getattr(os,'O_NOFOLLOW',0),0o600)
            with os.fdopen(fd,'w') as stream:
                json.dump({'reason':str(exc),'source_tree_sha':config['source_tree_sha'],
                           'private_evidence_dir':config['work'],'at':time.time()},stream);stream.flush();os.fsync(stream.fileno())
        emit('smtp.chain','failed',{'reason':str(exc),'private_evidence_dir':str(Path(config['work']).relative_to(ROOT)) if config else None},started)
        return 1
    except (OSError,RuntimeError,ValueError,subprocess.SubprocessError) as exc:
        emit('smtp.environment','blocked',{'reason':str(exc),'private_evidence_dir':str(Path(config['work']).relative_to(ROOT)) if config else None},started)
        return 2
    finally:
        if config:
            try:cleanup(config)
            except (OSError,RuntimeError,ValueError,subprocess.SubprocessError) as exc:
                _private(Path(config['work'])/'cleanup-failure.json',{'error_type':type(exc).__name__,'detail':str(exc)})
                emit('smtp.cleanup','blocked',{'error_type':type(exc).__name__,'reason':'owned SMTP container shutdown not confirmed'})
    return 0

def main():
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['chain']);parser.parse_args()
    with cooperative_sigterm():
        try:
            result=chain()
            return 128+signal.SIGTERM if _interrupt_signal is not None else result
        except TestlabInterrupted:return 128+signal.SIGTERM

if __name__=='__main__':raise SystemExit(main())
