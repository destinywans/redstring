"""Native invitation producer/acceptance with private local TLS SMTP capture.

Only root-reviewed --live acquires sockets/UUID resources. Production insists
on implicit TLS465, so this package owns only 127.0.0.1:465 after absence checks.
No DNS delivery, outbound relay, real mailbox, altered TTL, or business SQL seed.
"""
from __future__ import annotations
import argparse
import base64
import contextlib
import datetime
import fcntl
import hashlib
import hmac
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import socket
import socketserver
import ssl
import stat
import subprocess
import threading
import time
from urllib.parse import urlencode
import uuid

import api_readonly_chain as readonly

ROOT = readonly.ROOT
MODULE = 'formal-invitation-functional'
API_PORTS, HTTPS_PORT, SMTP_PORT = (18624,18625), 18626, 465
ORIGIN = 'https://testlab.local:18626'
FROM = 'noreply@testlab.invalid'
RELAY_ACTION = 'v3.member.invitation.relay_accepted'
ACCEPT_ACTION = 'v3.member.invitation.accepted'
TARGETS = ('POST /api/v1/members/invitations',)
REQUIRED_CASES = ('invitation.tls_ready', 'invitation.normal', 'invitation.acceptance',
    'invitation.authorization', 'invitation.inputs', 'invitation.data_response_loss',
    'invitation.quit_response_loss', 'invitation.audit_commit_failure', 'invitation.operation_linkage')
PROOF_SCOPE = 'Formal dual V3 HTTPS/native invitation/user/audit with local verified TLS SMTP capture; external mailbox delivery and reading not verified'
MODES = ('accept', 'reject_data', 'lose_data_response', 'lose_quit_response')
SCENARIOS = ('normal', 'data-loss', 'quit-loss', 'audit-failure', 'denied', 'invalid')
check, Blocked = readonly.check, readonly.Blocked


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
        evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)


def instant(value):
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))


def ports_free():
    for port in (*API_PORTS, HTTPS_PORT, SMTP_PORT):
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1', port)) == 0: raise Blocked('invitation dedicated listener occupied: ' + str(port))


@contextlib.contextmanager
def exclusive(root):
    path = Path(root) / '.local/testlab/api-invitation-functional.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private invitation lease required')
        try: fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise Blocked('invitation package already owns its listeners') from None
        yield
    finally: os.close(fd)


class SMTPProtocol:
    def __init__(self, nonce, username, password):
        if not re.fullmatch('[a-f0-9]{32}', nonce): raise Blocked('SMTP UUID nonce required')
        self.nonce, self.username, self.password = nonce, username, password
        self.lock, self.captures, self.mode = threading.Lock(), [], 'accept'

    def address(self, scenario):
        if scenario not in SCENARIOS: raise Blocked('SMTP recipient outside fixed synthetic scenario set')
        return scenario + '-' + self.nonce + '@testlab.invalid'

    def allowed(self, email):
        return email in [self.address(name) for name in SCENARIOS]

    def authenticate(self, encoded):
        try: authorization, username, password = base64.b64decode(encoded, validate=True).decode().split('\0')
        except (ValueError, UnicodeError): return False
        return authorization == '' and hmac.compare_digest(username, self.username) and hmac.compare_digest(password, self.password)

    def capture(self, recipient, raw):
        if not self.allowed(recipient) or self.mode not in MODES or not isinstance(raw, bytes) or len(raw) > 131072:
            raise Blocked('SMTP capture outside UUID recipient/mode/body bounds')
        outcome = dict(accept='accepted', reject_data='rejected_data', lose_data_response='accepted_lost_response',
                       lose_quit_response='accepted_quit_lost')[self.mode]
        with self.lock:
            self.captures.append(dict(sequence=len(self.captures)+1, recipient=recipient, sender=FROM, outcome=outcome,
                message=raw.decode('utf-8'), bytes=len(raw), at=datetime.datetime.now(datetime.timezone.utc).isoformat()))
        return outcome


class SMTPSink:
    def __init__(self, protocol, cert, key):
        self.protocol, self.cert, self.key = protocol, Path(cert), Path(key)
        self.server = self.thread = None
        self.connections, self.lock = set(), threading.Lock()

    def start(self):
        owner = self
        class Handler(socketserver.StreamRequestHandler):
            def reply(self, text): self.wfile.write((text + '\r\n').encode()); self.wfile.flush()
            def handle(self):
                self.connection.settimeout(5); authenticated = False; sender = recipient = None; accepted = False
                try:
                    self.reply('220 localhost owned testlab SMTP capture')
                    for _ in range(30):
                        raw = self.rfile.readline(4097)
                        if not raw or len(raw) > 4096: return
                        verb, _, argument = raw.decode('ascii').rstrip('\r\n').partition(' '); verb = verb.upper()
                        if verb == 'EHLO':
                            self.reply('250-localhost'); self.reply('250-AUTH PLAIN'); self.reply('250 SIZE 131072')
                        elif verb == 'AUTH':
                            method, _, encoded = argument.partition(' ')
                            authenticated = method == 'PLAIN' and owner.protocol.authenticate(encoded)
                            self.reply('235 authenticated' if authenticated else '535 invalid authentication')
                        elif not authenticated: self.reply('530 authentication required')
                        elif verb == 'MAIL':
                            sender = FROM if argument == 'FROM:<' + FROM + '>' else None; recipient = None; accepted = False
                            self.reply('250 sender accepted' if sender else '550 exact synthetic sender required')
                        elif verb == 'RCPT':
                            match = re.fullmatch('TO:<([^<>]+)>', argument)
                            recipient = match.group(1) if match and sender and owner.protocol.allowed(match.group(1)) else None
                            self.reply('250 recipient accepted' if recipient else '550 exact UUID recipient required')
                        elif verb == 'DATA' and sender and recipient:
                            self.reply('354 end with dot'); chunks = []; size = 0
                            while True:
                                line = self.rfile.readline(4097)
                                if not line or len(line) > 4096: return
                                if line == b'.\r\n': break
                                if line.startswith(b'..'): line = line[1:]
                                size += len(line)
                                if size > 131072: self.reply('552 message bound'); return
                                chunks.append(line)
                            outcome = owner.protocol.capture(recipient, b''.join(chunks))
                            if outcome == 'accepted_lost_response': return
                            if outcome == 'rejected_data': self.reply('550 controlled DATA rejection')
                            else: self.reply('250 local relay accepted'); accepted = True
                        elif verb == 'QUIT':
                            if accepted and owner.protocol.mode == 'lose_quit_response': return
                            self.reply('221 bye'); return
                        elif verb == 'RSET': sender = recipient = None; accepted = False; self.reply('250 reset')
                        else: self.reply('503 invalid SMTP state')
                except (OSError, ValueError, UnicodeError, Blocked): return
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.load_cert_chain(self.cert, self.key)
        class Server(socketserver.ThreadingTCPServer):
            allow_reuse_address = False
            daemon_threads = True
            def get_request(self):
                conn, address = super().get_request(); conn.settimeout(4)
                try: conn = context.wrap_socket(conn, server_side=True)
                except BaseException: conn.close(); raise
                with owner.lock: owner.connections.add(conn)
                return conn, address
            def shutdown_request(self, request):
                with owner.lock: owner.connections.discard(request)
                super().shutdown_request(request)
            def handle_error(self, *args): pass
        try:self.server = Server(('127.0.0.1', SMTP_PORT), Handler)
        except PermissionError as error:
            raise Blocked('owned loopback TLS465 bind denied by local OS privilege; no SMTP listener/API started') from error
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval':.1}, daemon=True); self.thread.start()

    def close(self):
        if self.server:
            if self.thread and self.thread.is_alive(): self.server.shutdown()
            with self.lock:
                for conn in tuple(self.connections):
                    try: conn.shutdown(socket.SHUT_RDWR)
                    except OSError: pass
            self.server.server_close()
        if self.thread: self.thread.join(6); check(not self.thread.is_alive(), 'owned SMTP listener leaked')


SMTP_IMAGE='sha256:b2236d9e1563c507613962c1ebbd7b3d307969ec2ee355b781b68440f4f0bee3'


class ContainerProtocol(SMTPProtocol):
    def __init__(self,nonce,username,password,output):
        self.output=Path(output);self.mode_hook=None
        super().__init__(nonce,username,password)
    @property
    def mode(self):return self._mode
    @mode.setter
    def mode(self,value):
        if value not in MODES:raise Blocked('owned SMTP fault mode outside fixed modes')
        if self.mode_hook:self.mode_hook()
        self._mode=value;readonly._private(self.output/'mode.json',dict(mode=value))
    @property
    def captures(self):
        path=self.output/'captures.jsonl'
        if not path.exists():return []
        if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600 or path.stat().st_size>1<<20:
            raise Blocked('owned SMTP capture file identity/mode/bound differs')
        rows=[]
        for line in path.read_text().splitlines():
            row=json.loads(line)
            if (not isinstance(row,dict) or row.get('sender')!=FROM or not self.allowed(row.get('recipient'))
                or row.get('outcome') not in ('accepted','rejected_data','accepted_lost_response','accepted_quit_lost')
                or not isinstance(row.get('message'),str) or len(row['message'].encode())>131072 or len(rows)>=16):
                raise Blocked('owned SMTP capture UUID/identity/body differs')
            rows.append(dict(row,sequence=len(rows)+1))
        return rows
    @captures.setter
    def captures(self,value):
        if value!=[]:raise Blocked('container capture cannot be fabricated')


class SMTPDockerSink:
    def __init__(self,protocol,work,run_id):
        self.protocol,self.work,self.run_id=protocol,Path(work),str(uuid.UUID(run_id))
        self.name='aiops-'+readonly.OWNER+'-invitation-smtp-'+uuid.UUID(run_id).hex
        self.cid=None;self.labels={'testlab.owner':readonly.OWNER,'testlab.invitation.run':self.run_id}
        self.mounts=[(self.protocol.output,'/work',True),(self.work/'invitation-cert.pem','/work/smtp.crt',False),
            (self.work/'invitation-key.pem','/work/smtp.key',False),(self.work/'smtp-config.json','/work/smtp-config.json',False),
            (ROOT/'tools/testlab/smtp_capture.py','/smtp_capture.py',False),(self.work/'smtp-wrapper.py','/smtp-wrapper.py',False)]
    def docker(self,args,timeout=15):
        result=subprocess.run(['docker',*args],capture_output=True,text=True,timeout=timeout)
        if result.returncode:raise Blocked('owned invitation SMTP Docker operation failed: '+args[0])
        return result.stdout.strip()
    def expected_inspect_fixture(self):
        return dict(Id=self.cid,Image=SMTP_IMAGE,Name='/'+self.name,Config=dict(Labels=self.labels,Cmd=['python','-B','/smtp-wrapper.py']),
            HostConfig=dict(NanoCpus=500000000,Memory=134217728,Privileged=False,CapDrop=['ALL'],SecurityOpt=['no-new-privileges'],
                PortBindings={'465/tcp':[{'HostIp':'127.0.0.1','HostPort':'465'}]}),
            Mounts=[dict(Type='bind',Source=str(path.resolve()),Destination=target,RW=rw) for path,target,rw in self.mounts])
    def validate(self,info):
        expected=self.expected_inspect_fixture()
        if (not isinstance(info,dict) or not re.fullmatch('[a-f0-9]{64}',self.cid or '')
            or any(info.get(k)!=expected[k] for k in ('Id','Image','Name'))
            or info.get('Config',{}).get('Labels')!=self.labels or info.get('Config',{}).get('Cmd')!=expected['Config']['Cmd']
            or any(info.get('HostConfig',{}).get(k)!=v for k,v in expected['HostConfig'].items())):
            raise Blocked('invitation SMTP container CID/owner/run/image/resource/port/command mismatch')
        actual=[{k:x.get(k) for k in ('Type','Source','Destination','RW')} for x in info.get('Mounts',[])]
        if sorted(actual,key=lambda x:str(x.get('Destination')))!=sorted(expected['Mounts'],key=lambda x:x['Destination']):
            raise Blocked('invitation SMTP container private mount mismatch')
        return info
    def guard(self):
        if self.cid is None:raise Blocked('invitation SMTP container not acquired')
        values=json.loads(self.docker(['inspect',self.cid]))
        if len(values)!=1:raise Blocked('ambiguous invitation SMTP inspect')
        return self.validate(values[0])
    def start(self):
        readonly._private(self.work/'smtp-config.json',dict(username=self.protocol.username,password=self.protocol.password))
        wrapper="import smtp_capture as c\nallowed="+repr([FROM,*[self.protocol.address(x) for x in SCENARIOS]])+"\noriginal=c.local_address\ndef exact(value):\n address=original(value)\n return address if address in allowed else None\nc.local_address=exact\nwith c.SMTPServer('/work') as server:server.serve_forever()\n"
        readonly.private_file(self.work/'smtp-wrapper.py',wrapper)
        if self.docker(['image','inspect',SMTP_IMAGE,'--format','{{.Id}}'])!=SMTP_IMAGE:raise Blocked('local pinned SMTP image unavailable')
        args=['create','--name',self.name,'--memory','128m','--cpus','.5','--cap-drop','ALL','--security-opt','no-new-privileges',
            '-p','127.0.0.1:465:465']
        for key,value in self.labels.items():args+=['--label',key+'='+value]
        for path,target,rw in self.mounts:args+=['-v',str(path.resolve())+':'+target+('' if rw else ':ro')]
        args += [SMTP_IMAGE,'python','-B','/smtp-wrapper.py']
        readonly._private(self.work/'smtp-acquisition.json',dict(name=self.name,labels=self.labels,image=SMTP_IMAGE,creation_pending=True,unknown_outcome_adoption_allowed=False))
        with readonly.acquisition_signals():
            cid=self.docker(args)
            if not re.fullmatch('[a-f0-9]{64}',cid):raise Blocked('unknown SMTP creation outcome; container not adopted')
            self.cid=cid;readonly._private(self.work/'smtp-container.json',dict(cid=cid,name=self.name,image=SMTP_IMAGE,labels=self.labels))
        self.guard();self.docker(['start',self.cid]);self.protocol.mode_hook=self.guard
        context=ssl.create_default_context(cafile=str(self.work/'invitation-cert.pem'));deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            try:
                with socket.create_connection(('127.0.0.1',465),timeout=2) as raw:
                    with context.wrap_socket(raw,server_hostname='localhost') as conn:
                        check(conn.recv(1024).startswith(b'220 '),'owned Docker SMTP did not greet');self.guard();return
            except OSError:time.sleep(.2)
        raise Blocked('owned Docker loopback TLS465 publish/readiness unavailable')
    def close(self):
        if self.cid is None:return
        self.guard();self.docker(['stop','--time','3',self.cid],timeout=10);self.guard();self.docker(['rm',self.cid])
        result=subprocess.run(['docker','inspect',self.cid],capture_output=True,text=True,timeout=5)
        check(result.returncode!=0 and result.stdout.strip()=='[]' and self.cid in result.stderr and ('no such object' in result.stderr.lower() or 'no such container' in result.stderr.lower()),'owned SMTP container removal not confirmed')
        readonly._private(self.work/'smtp-cleanup.json',dict(cid=self.cid,exact_owned_container_absent=True));self.protocol.mode_hook=None;self.cid=None


def mail_evidence(capture):
    message = capture['message']
    check('From: ' + FROM + '\r\n' in message and 'To: ' + capture['recipient'] + '\r\n' in message,
          'native mail/envelope identity differs')
    entry = re.findall(r'^Trusted product entry: (.+)\r?$', message, re.M)
    code = re.findall(r'^One-time invitation code: ([A-Za-z0-9_-]{43})\r?$', message, re.M)
    expiry = re.findall(r'^Code expires at: (.+)\r?$', message, re.M)
    check(len(entry) == len(code) == len(expiry) == 1 and entry[0].rstrip('\r') == ORIGIN + '/accept-invitation', 'mail trusted entry/code/expiry differs')
    try: raw = base64.urlsafe_b64decode(code[0] + '='); expires = expiry[0].rstrip('\r'); instant(expires)
    except (ValueError, TypeError): raise AssertionError('mail native code/expiry invalid') from None
    check(len(raw) == 32 and base64.urlsafe_b64encode(raw).decode().rstrip('=') == code[0], 'mail code not canonical 256-bit invitation')
    return dict(code=code[0], token_hash=hashlib.sha256(raw).hexdigest(), expires_at=expires,
                recipient=capture['recipient'], outcome=capture['outcome'])


def validate_invitation(row, audits, mail, actor, status):
    check(row['inviter_id'] == actor and row['email'] == mail['recipient'] and row['environment_id'] == 'test'
          and row['role'] == 'viewer' and row['status'] == status and row['token_hash'] == mail['token_hash'],
          'native invitation actor/email/scope/status/code binding differs')
    try: expiry_matches = instant(row['expires_at']).replace(microsecond=0) == instant(mail['expires_at'])
    except (ValueError, TypeError): expiry_matches = False
    check(expiry_matches, 'mail expiry not bound to native invitation')
    linked = [x for x in audits if x['target'] == row['id'] or x['changes'].get('invitation_id') == row['id']]
    check(len(linked)==len(audits),'invitation generated an unrelated extra audit')
    if status == 'delivered':
        check(row['delivered_at'] is not None and row['account_id'] is None and row['accepted_at'] is None
            and [{k:x[k] for k in ('actor','action','target','outcome','changes')} for x in linked] == [dict(actor=actor, action=RELAY_ACTION,
                target=row['id'], outcome='success', changes={'environment':'test'})], 'relay acceptance and native audit differ')
    elif status in ('pending','superseded'):
        check(row['delivered_at'] is None and row['account_id'] is None and row['accepted_at'] is None and linked == [],
              'uncertain/rejected mail acquired native success or account')
    else: raise AssertionError('only pre-consumption native invitation states validated here')


def fault_ddl(nonce, actor, email):
    if (not re.fullmatch('[a-f0-9]{32}', nonce) or not re.fullmatch('[a-f0-9]{32}', actor)
            or email != 'audit-failure-' + nonce + '@testlab.invalid'): raise Blocked('invitation fault outside exact UUID actor/email')
    name = readonly.identifier('testlab_reject_invite_' + nonce)
    return ('BEGIN; CREATE FUNCTION public.' + name + '() RETURNS trigger LANGUAGE plpgsql AS $testlab$ BEGIN IF NEW.actor_id=' + readonly.quote(actor)
        + ' AND NEW.action=' + readonly.quote(RELAY_ACTION) + ' AND EXISTS(SELECT 1 FROM public.v3_member_invitations i WHERE i.id=NEW.target AND i.inviter_id='
        + readonly.quote(actor) + ' AND i.email=' + readonly.quote(email) + " AND i.environment_id='test') THEN RAISE EXCEPTION 'testlab scoped invitation audit failure'; END IF; RETURN NEW; END $testlab$; "
        + 'CREATE TRIGGER ' + name + ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.' + name + '(); COMMIT;')


class HTTPSRelay:
    def __init__(self, cert, key): self.cert, self.key, self.server, self.thread = cert, key, None, None
    def start(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def relay(self):
                self.connection.settimeout(25); conn = None
                try:
                    if self.path not in ('/healthz', '/api/v1/auth/login', '/api/v1/auth/logout', '/api/v1/auth/me',
                        '/api/v1/members/invitations', '/api/v1/auth/invitations/accept'): self.send_error(404); return
                    index = self.headers.get('X-Testlab-Instance', '0')
                    if index not in ('0','1') or self.headers.get('Host') != 'testlab.local:18626' or self.headers.get('Transfer-Encoding'):
                        self.send_error(400); return
                    size = int(self.headers.get('Content-Length','0'))
                    if not 0 <= size <= 65536: self.send_error(413); return
                    body = self.rfile.read(size) if size else None
                    headers = {k:v for k,v in self.headers.items() if k.lower() not in ('x-testlab-instance','content-length','connection','transfer-encoding','x-forwarded-for','x-forwarded-proto')}
                    headers.update({'X-Forwarded-For':'127.0.0.1','X-Forwarded-Proto':'https'})
                    conn = http.client.HTTPConnection('127.0.0.1', API_PORTS[int(index)], timeout=25)
                    conn.request(self.command, self.path, body, headers); response = conn.getresponse(); raw = response.read(1<<20)
                    self.send_response(response.status)
                    for key,value in response.getheaders():
                        if key.lower() not in ('server','date','content-length','connection','transfer-encoding'): self.send_header(key,value)
                    self.send_header('Content-Length',str(len(raw))); self.end_headers(); self.wfile.write(raw)
                except (OSError,ValueError): self.close_connection = True
                finally:
                    if conn: conn.close()
            do_GET = do_POST = relay
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.minimum_version = ssl.TLSVersion.TLSv1_2; context.load_cert_chain(self.cert,self.key)
        class Server(ThreadingHTTPServer):
            daemon_threads = True
            def get_request(self):
                conn,address = super().get_request(); conn.settimeout(4)
                try: return context.wrap_socket(conn,server_side=True),address
                except BaseException: conn.close(); raise
        self.server = Server(('127.0.0.1',HTTPS_PORT),Handler)
        self.thread = threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def close(self):
        if self.server:
            if self.thread and self.thread.is_alive():self.server.shutdown()
            self.server.server_close()
        if self.thread:self.thread.join(6);check(not self.thread.is_alive(),'owned HTTPS relay leaked')


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root,data,run_id)
        self.protocol = ContainerProtocol(self.nonce,'testlab-invitation-'+self.nonce,secrets.token_urlsafe(32),self.work/'smtp-output')
        self.smtp = self.relay = None; self.protocol_files = {}; self.fault_installed = False
        self.http_records, self.qualified = {}, set()
        self.state.update(suite=MODULE,ports=list(API_PORTS),https_port=HTTPS_PORT,smtp_port=SMTP_PORT,
            proof_scope=PROOF_SCOPE,actual_results_source='native HTTPS invitation producer/acceptance plus TLS SMTP capture',external_mailbox_verified=False)
        readonly._private(self.work/'state.json',self.state)

    def prepare(self):
        cert,key = self.work/'invitation-cert.pem',self.work/'invitation-key.pem'
        result = subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','2','-subj','/CN=localhost',
            '-addext','subjectAltName=DNS:localhost,DNS:testlab.local,IP:127.0.0.1','-keyout',str(key),'-out',str(cert)],
            capture_output=True,timeout=20,env=readonly.command_env({},{}),umask=0o077)
        if result.returncode:raise Blocked('private invitation TLS setup failed')
        readonly.private_file(self.work/'smtp-password',self.protocol.password)
        for path in (cert,key,self.work/'smtp-password',*(self.work/name for name in ('instance.json','models.json','commands.json'))):
            os.chmod(path,0o600);self.protocol_files[path.name]=hashlib.sha256(path.read_bytes()).hexdigest()
        self.smtp_helper_sha=hashlib.sha256((self.root/'tools/testlab/smtp_capture.py').read_bytes()).hexdigest()
        self.frozen();self.smtp=SMTPDockerSink(self.protocol,self.work,self.run_id);self.smtp.start()
        for path in (self.work/'smtp-config.json',self.work/'smtp-wrapper.py'):
            self.protocol_files[path.name]=hashlib.sha256(path.read_bytes()).hexdigest()
        self.smtp_helper_sha=hashlib.sha256((self.root/'tools/testlab/smtp_capture.py').read_bytes()).hexdigest()

    def frozen(self):
        super().frozen()
        if hasattr(self,'smtp_helper_sha') and hashlib.sha256((self.root/'tools/testlab/smtp_capture.py').read_bytes()).hexdigest()!=self.smtp_helper_sha:
            raise Blocked('shared readonly SMTP capture source changed')
        for name,digest in self.protocol_files.items():
            path=self.work/name
            if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:
                raise Blocked('invitation private TLS/SMTP input changed')

    def start(self):
        self.frozen()
        for index,port in enumerate(API_PORTS):
            with socket.socket() as probe:
                probe.settimeout(.3)
                if probe.connect_ex(('127.0.0.1',port))==0:raise Blocked('invitation API port occupied before owned launch')
            dsn='postgres://'+self.db_user+':'+self.db_password+'@127.0.0.1:15461/'+self.database+'?'+urlencode(dict(sslmode='disable',options='-csearch_path=public',application_name=self.pg_application('api',index)))
            variables=dict(DATABASE_URL=dsn,AIOPS_V3_INSTANCE_CONFIG=str(self.work/'instance.json'),AIOPS_ADDR='127.0.0.1:'+str(port),
                AIOPS_PUBLIC_ORIGIN=ORIGIN,AIOPS_SECURE_COOKIES='true',AIOPS_ADMIN_EMAIL=self.admin_email,AIOPS_ADMIN_PASSWORD=self.password,
                AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32',AIOPS_V3_MODEL_BINDINGS_FILE=str(self.work/'models.json'),AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.work/'commands.json'),
                OPENAI_MODEL='readonly-synthetic',OPENAI_API_KEY='synthetic-no-inference',OPENAI_BASE_URL='http://127.0.0.1:9/v1',
                EMBEDDING_PROVIDER='openai',EMBEDDING_MODEL='readonly-synthetic',EMBEDDING_API_KEY='synthetic-no-embedding',EMBEDDING_BASE_URL='http://127.0.0.1:9/v1',
                AIOPS_V3_INVITATION_SMTP_HOST='localhost',AIOPS_V3_INVITATION_SMTP_FROM=FROM,AIOPS_V3_INVITATION_SMTP_USERNAME=self.protocol.username,
                AIOPS_V3_INVITATION_SMTP_PASSWORD_FILE=str(self.work/'smtp-password'),AIOPS_V3_INVITATION_ENTRY_URL=ORIGIN+'/accept-invitation',SSL_CERT_FILE=str(self.work/'invitation-cert.pem'))
            log=os.open(self.work/('api-'+str(index)+'.log'),os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:
                with readonly.acquisition_signals():
                    process=subprocess.Popen([self.data['binary']],cwd=self.root,env=readonly.command_env(variables,{}),stdout=log,stderr=log)
                    self.processes.append(process);self.state['pids'].append(process.pid);readonly._private(self.work/'state.json',self.state)
            finally:os.close(log)
            deadline=time.monotonic()+90
            while time.monotonic()<deadline:
                if process.poll() is not None:raise Blocked('private invitation API startup exited; private log retained')
                conn=http.client.HTTPConnection('127.0.0.1',port,timeout=2)
                try:
                    conn.request('GET','/healthz');response=conn.getresponse();response.read()
                    if response.status==200:
                        check(readonly._process_port(process.pid,port),'held invitation Popen does not own registered listener');break
                except OSError:pass
                finally:conn.close()
                time.sleep(.2)
            else:raise Blocked('private invitation API readiness deadline')
            emit('invitation.api_ready.'+str(index),'passed',dict(port=port,pid=process.pid))
        self.relay=HTTPSRelay(self.work/'invitation-cert.pem',self.work/'invitation-key.pem');self.relay.start()
        self.frozen();emit('invitation.tls_ready','passed',dict(https_origin=ORIGIN,smtp_bind='127.0.0.1:465',secure_cookie=True,native_tls=True,external_delivery=False))

    def call(self,user,method,path,body=None,want=200,code=None,scenario='normal',index=0,environment='test',validate=None,origin=ORIGIN):
        allowed={'POST /api/v1/auth/login','POST /api/v1/auth/logout','GET /api/v1/auth/me','POST /api/v1/members/invitations','POST /api/v1/auth/invitations/accept'}
        wire=readonly.SCENARIO_ALIASES.get(scenario,scenario)
        if method+' '+path not in allowed or user not in (None,'admin',*self.users) or index not in (0,1) or environment not in ('test','staging','all') or wire not in readonly.WIRE_SCENARIOS:
            raise Blocked('invitation HTTPS route/actor/scope mismatch')
        self.calls+=1;number=self.calls;canonical,route_class=self.contract.canonical(method,path)
        headers={'Host':'testlab.local:18626','Origin':origin,'X-Testlab-Instance':str(index),'X-AIOps-Environment':environment,'Accept-Language':'en'}
        if user in self.cookies:headers['Cookie']=self.cookies[user]
        if body is not None:headers['Content-Type']='application/json'
        row=dict(name='invitation.http.'+str(number),method=method,path=canonical,route_class=route_class,scenario=wire,
            scenario_detail=scenario,expected={'status':[want],'error_code':code},run_id=self.run_id,call_id=self.run_id+'-'+str(number),
            test_name='FormalInvitationFunctionalChain',evidence_mode='real_local',upstream_mode='real_local',proof_scope=PROOF_SCOPE,registration_evidence=False)
        conn=None
        try:
            context=ssl.create_default_context(cafile=str(self.work/'invitation-cert.pem'))
            conn=http.client.HTTPSConnection('127.0.0.1',HTTPS_PORT,context=context,timeout=25)
            conn.request(method,path,json.dumps(body).encode() if body is not None else None,headers)
            response=conn.getresponse();raw=response.read((1<<20)+1);check(len(raw)<=1<<20,'invitation HTTPS response bound')
            value=json.loads(raw) if raw else {};actual=value.get('error',{}).get('code') if isinstance(value,dict) else None
            row['actual']=dict(status=response.status,error_code=actual);check(response.status==want and (code is None or actual==code),'invitation HTTP status/error differs')
            if 200<=response.status<300:self.contract.shape(method,path,response.status,value)
            if validate:validate(value)
            if path=='/api/v1/auth/login' and response.status==200:
                cookie=response.getheader('Set-Cookie','')
                check('Secure' in cookie.split('; ') and 'HttpOnly' in cookie.split('; '),'formal HTTPS login missing secure session cookie flags')
            row.update(status='passed',registration_evidence=False)
            response_headers=dict(response.getheaders())
        except Exception as error:
            row.setdefault('actual',dict(status=None));row.update(status='failed');row['actual']['assertion']=str(error) if isinstance(error,AssertionError) else type(error).__name__
            self.first_red(row);readonly.append_json(self.work/'interfaces.jsonl',row)
            emit(row['name'],'failed',row['actual'],**({'interface':row} if type(row['actual']['status']) is int else {}));raise
        finally:
            if conn:conn.close()
        response_path=self.work/('http-response-'+str(number)+'.json')
        readonly.private_file(response_path,raw.decode('utf-8'))
        intent=dict(actor=self.users.get(user),environment=environment)
        if path=='/api/v1/members/invitations' and isinstance(body,dict):intent['email']=body.get('email');intent['role']=body.get('role','viewer')
        if path=='/api/v1/auth/invitations/accept' and isinstance(body,dict):
            try:intent['token_hash']=hashlib.sha256(base64.urlsafe_b64decode(body.get('code','')+'=')).hexdigest()
            except (ValueError,TypeError):intent['token_hash']=None
        self.http_records[row['call_id']]=dict(row=row,response_path=response_path,response_sha256=hashlib.sha256(raw).hexdigest(),intent=intent)
        readonly.append_json(self.work/'interfaces.jsonl',row);emit(row['name'],'passed',row['actual'],interface=row);return value,response_headers

    def qualify(self,call_id,proof,artifact):
        self.frozen();record=self.http_records.get(call_id);check(record is not None and call_id not in self.qualified,'native qualification requires unique exact HTTP call')
        row,intent=record['row'],record['intent'];target=row['method']+' '+row['path']
        check(row['status']=='passed' and row['scenario']=='normal' and 200<=row['actual']['status']<300
            and target in (*TARGETS,'POST /api/v1/auth/invitations/accept'),'native qualification lacks successful exact target')
        check(proof.get('verified') is True and proof.get('run_id')==self.run_id and proof.get('database')==self.database
            and proof.get('source_tree_sha')==self.data['source_tree_sha'] and proof.get('http_call_id')==call_id
            and re.fullmatch('[a-f0-9]{32}',proof.get('invitation_id','')) is not None,'native qualification source/UUID/call binding differs')
        if target in TARGETS:
            check(proof.get('kind')=='invitation' and proof.get('status')=='delivered' and proof.get('email')==intent.get('email')
                and proof.get('actor')==intent.get('actor') and proof.get('environment')==intent.get('environment')
                and intent.get('role')=='viewer','native invitation proof belongs to a different HTTP target/actor/scope')
        else:
            check(proof.get('kind')=='acceptance' and proof.get('token_hash')==intent.get('token_hash')
                and re.fullmatch('[a-f0-9]{32}',proof.get('account_id','')) is not None,'native acceptance proof belongs to another invitation code/account')
        for path,digest in ((record['response_path'],record['response_sha256']),(Path(artifact),None)):
            check(path.parent==self.work and not path.is_symlink() and path.is_file() and stat.S_IMODE(path.stat().st_mode)==0o600,'qualified evidence must be private owned regular file')
            if digest:check(hashlib.sha256(path.read_bytes()).hexdigest()==digest,'original HTTP response changed before qualification')
        check(json.loads(Path(artifact).read_text())==proof,'native qualification file differs from validated object')
        qualified=dict(row,name=row['name']+'.qualified',registration_evidence=True,original_call_id=call_id,
            response_sha256=record['response_sha256'],native_proof_sha256=hashlib.sha256(Path(artifact).read_bytes()).hexdigest(),
            proof={key:proof[key] for key in ('kind','invitation_id','http_call_id','verified')},
            validation_scope='Exact native invitation/mail/user/audit correspondence after original HTTP response')
        self.qualified.add(call_id);readonly.append_json(self.work/'interfaces.jsonl',qualified)
        emit(qualified['name'],'passed',qualified['actual'],interface=qualified)


    def seed(self):
        super().seed()
        for name in ('reader','peer'):
            role=readonly.quote(self.roles[name]);self.sql("INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES("+role+",'members','manage','global','*'),("+role+",'environment','view','global','*') ON CONFLICT DO NOTHING")

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('invitations',(SELECT COALESCE(json_agg(json_build_object('id',id,'email',email,'role',role,'environment_id',environment_id,'inviter_id',inviter_id,'token_hash',encode(token_hash,'hex'),'status',status,'expires_at',expires_at,'created_at',created_at,'delivered_at',delivered_at,'accepted_at',accepted_at,'account_id',account_id) ORDER BY id),'[]'::json) FROM v3_member_invitations),"
            "'users',(SELECT COALESCE(json_agg(json_build_object('id',u.id,'email',u.email,'name',u.name,'role',u.role,'source',u.source,'disabled',u.disabled,'kind',u.v3_identity_kind,'timezone',u.v3_timezone,'settings',row_to_json(s),'roles',(SELECT COALESCE(json_agg(json_build_object('id',role_id,'origin',origin) ORDER BY role_id),'[]'::json) FROM rbac_user_roles WHERE user_id=u.id),'grants',(SELECT COALESCE(json_agg(row_to_json(g) ORDER BY g.module,g.action,g.scope_kind,g.scope_id),'[]'::json) FROM v3_effective_role_grants g WHERE user_id=u.id)) ORDER BY u.id),'[]'::json) FROM users u LEFT JOIN v3_user_settings s ON s.user_id=u.id),"
            "'audit',(SELECT COALESCE(json_agg(json_build_object('id',id,'actor',actor_id,'action',action,'target',target,'outcome',outcome,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit),"
            "'workspace',(SELECT json_build_object('timezone',timezone,'locale',language) FROM workspace_settings WHERE id='default'))",read=True))

    def invite(self,scenario,index=0,want=200,code=None,user='reader',environment='test',body=None):
        return self.call(user,'POST','/api/v1/members/invitations',body if body is not None else dict(email=self.protocol.address(scenario),role='viewer'),
            index=index,want=want,code=code,environment=environment,scenario='normal' if want==200 else 'failure',
            validate=(lambda x:check(x=={'ok':True},'native invitation normal response differs')) if want==200 else None)

    def proof(self,before,email,status):
        after=self.ledger();old={x['id']:x for x in before['invitations']};new=[x for x in after['invitations'] if x['id'] not in old]
        check(len(new)==1 and all(x==old[x['id']] for x in after['invitations'] if x['id'] in old),'invitation replaced prior ledger or added wrong count')
        check(after['users']==before['users'] and after['workspace']==before['workspace'] and after['audit'][:len(before['audit'])]==before['audit'],'invitation changed account/workspace/prior audit')
        captures=[x for x in self.protocol.captures if x['recipient']==email];check(captures,'native invitation did not reach owned SMTP')
        mail=mail_evidence(captures[-1]);validate_invitation(new[0],after['audit'][len(before['audit']):],mail,self.users['reader'],status)
        call_id=self.run_id+'-'+str(self.calls);artifact=self.work/('native-invitation-'+new[0]['id']+'.json')
        proof=dict(run_id=self.run_id,database=self.database,source_tree_sha=self.data['source_tree_sha'],http_call_id=call_id,verified=True,
            kind='invitation',invitation_id=new[0]['id'],email=email,actor=self.users['reader'],environment='test',status=status,token_hash=new[0]['token_hash'],
            native_invitation=new[0],native_audit=after['audit'][len(before['audit']):],smtp_capture=captures[-1],code_hash_matches=True,proof_scope=PROOF_SCOPE,external_mailbox_verified=False)
        readonly._private(artifact,proof)
        if status=='delivered':self.qualify(call_id,proof,artifact)
        return after,mail,new[0]

    def accept(self,mail,index=1,want=201,code=None):
        return self.call(None,'POST','/api/v1/auth/invitations/accept',dict(code=mail['code'],name='Synthetic invited member',password=self.password),
            want=want,code=code,index=index,scenario='normal' if want==201 else 'failure',
            validate=(lambda x:check(x==dict(ok=True,access='pending_scope_grant'),'native acceptance response differs')) if want==201 else None)

    def acceptance_proof(self,before,row):
        after=self.ledger();old={x['id']:x for x in before['users']};new=[x for x in after['users'] if x['id'] not in old]
        check(len(new)==1 and all(x==old[x['id']] for x in after['users'] if x['id'] in old),'acceptance modified pre-existing accounts')
        user=new[0];current=next(x for x in after['invitations'] if x['id']==row['id'])
        check(user['email']==row['email'] and user['name']=='Synthetic invited member' and user['source']=='local' and user['role']=='viewer'
            and user['disabled'] is False and user['kind']=='human' and user['timezone']==after['workspace']['timezone']
            and user['roles']==[dict(id='viewer',origin='legacy')] and user['settings']['locale']==after['workspace']['locale']
            and all(g['module']!='environment' and g['action']=='view' and g['scope_kind'] not in ('application','team') for g in user['grants']),
            'accepted member obtained unexpected identity/grant defaults')
        expected=dict(row);expected.update(status='accepted',account_id=user['id'],accepted_at=current['accepted_at'])
        check(current==expected and current['accepted_at'] is not None,'accepted invitation account/consumption binding differs')
        check(after['audit'][:-1]==before['audit'] and {k:after['audit'][-1][k] for k in ('actor','action','target','outcome','changes')}==dict(actor=user['id'],action=ACCEPT_ACTION,
            target=user['id'],outcome='success',changes=dict(invitation_id=row['id'],access='pending_scope_grant')),'native acceptance audit account/invitation differs')
        call_id=self.run_id+'-'+str(self.calls);artifact=self.work/('native-acceptance-'+row['id']+'.json')
        proof=dict(run_id=self.run_id,database=self.database,source_tree_sha=self.data['source_tree_sha'],http_call_id=call_id,verified=True,
            kind='acceptance',invitation_id=row['id'],account_id=user['id'],token_hash=row['token_hash'],user=user,invitation=current,audit=after['audit'][-1],scope_granted=False)
        readonly._private(artifact,proof);self.qualify(call_id,proof,artifact)
        return after

    def fault_guard(self):
        info=readonly.guard_pg(readonly.inspect_pg(self.data['postgres_container']),readonly.OWNER,self.data['postgres_image_id'])
        nonce=uuid.UUID(self.run_id).hex
        if (info.get('Id')!=self.data['postgres_container_id'] or self.nonce!=nonce or self.database!='aiops_testlab_readonly_'+nonce
                or self.db_user!='readonly_'+nonce or self.owner_tag!=readonly.OWNER+':'+self.run_id+':api-readonly'):
            raise Blocked('invitation fault UUID/container mismatch')
        catalog=json.loads(self.pg("SELECT json_build_object('owner',(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname="+readonly.quote(self.database)
            +"),'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname="+readonly.quote(self.database)
            +"),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname="+readonly.quote(self.db_user)+'))'))
        if catalog!=dict(owner=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag):raise Blocked('invitation fault ownership/catalog mismatch')
        return dict(run_id=self.run_id,database=self.database,role=self.db_user,owner_tag=self.owner_tag,container_id=info['Id'])

    def install_fault(self):
        self.frozen();scope=self.fault_guard();ddl=fault_ddl(self.nonce,self.users['reader'],self.protocol.address('audit-failure'))
        self.fault_installed=True;readonly._private(self.work/'audit-fault-proof.json',dict(scope=scope,ddl=ddl,writes_business_results=False))
        self.sql(ddl)

    def remove_fault(self):
        if not self.fault_installed:return
        self.fault_guard();name=readonly.identifier('testlab_reject_invite_'+self.nonce)
        self.sql('BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;');self.fault_installed=False

    def cases(self):
        before=self.ledger();count=len(self.protocol.captures)
        self.invite('normal');before,mail,row=self.proof(before,self.protocol.address('normal'),'delivered')
        check(len(self.protocol.captures)==count+1,'one normal HTTP invitation issued multiple SMTP submissions')
        self.invite('normal',index=1,want=409,code='invitation_conflict');check(self.ledger()==before and len(self.protocol.captures)==count+1,'duplicate delivered request resent invitation')
        emit('invitation.normal','passed',dict(relay_accepted=True,duplicate=409,mailbox_verified=False))
        self.accept(mail);before=self.acceptance_proof(before,row);self.accept(mail,index=0,want=409,code='invitation_invalid')
        self.invite('normal',want=409,code='invitation_conflict');check(self.ledger()==before and len(self.protocol.captures)==count+1,'consumed code replay changed account or resent')
        emit('invitation.acceptance','passed',dict(native_user_created=True,scope_granted=False,code_replay=409,account_audit=True))
        for user,index,env in ((None,0,'test'),('denied',1,'test'),('denied',0,'staging')):
            self.invite('denied',user=user,index=index,environment=env,want=401 if user is None else 403,code='unauthorized' if user is None else 'forbidden')
        self.call('reader','POST','/api/v1/members/invitations',dict(email=self.protocol.address('denied')),want=409,code='env_view_only',environment='all',scenario='boundary')
        self.call('reader','POST','/api/v1/members/invitations',dict(email=self.protocol.address('denied')),want=403,code='origin_rejected',origin='https://foreign.testlab.invalid',scenario='authorization')
        check(self.ledger()==before and len(self.protocol.captures)==count+1,'denied invitation touched SMTP/native state')
        emit('invitation.authorization','passed',dict(anonymous=401,denied=403,all_scope=409,wrong_origin=403,smtp_attempts=0))
        for body in ({'email':'bad-address'},{'email':self.protocol.address('invalid'),'role':'admin'},{'email':self.protocol.address('invalid'),'unknown':'x'}):
            self.call('reader','POST','/api/v1/members/invitations',body,want=422,code='invalid_invitation',scenario='boundary')
        check(self.ledger()==before and len(self.protocol.captures)==count+1,'invalid invitation modified local state')
        emit('invitation.inputs','passed',dict(invalid_email=422,unsafe_role=422,unknown_field=422,smtp_attempts=0))
        self.protocol.mode='lose_data_response';count=len(self.protocol.captures)
        self.invite('data-loss',want=503,code='invitation_unavailable');before,lost,oldrow=self.proof(before,self.protocol.address('data-loss'),'superseded')
        check(len(self.protocol.captures)==count+1,'DATA response loss blindly retried SMTP')
        self.accept(lost,want=409,code='invitation_invalid');check(self.ledger()==before,'unconfirmed relay code created account')
        # A distinct explicit new HTTP request is intentionally allowed to send
        # another one-time code. This is not an automatic retry by this tool.
        self.protocol.mode='accept';self.invite('data-loss',index=1);before,replacement,newrow=self.proof(before,self.protocol.address('data-loss'),'delivered')
        check(newrow['id']!=oldrow['id'] and replacement['code']!=lost['code'] and len(self.protocol.captures)==count+2,'explicit replacement did not create distinct bounded attempt')
        self.accept(lost,want=409,code='invitation_invalid');self.accept(replacement,index=0);before=self.acceptance_proof(before,newrow)
        emit('invitation.data_response_loss','passed',dict(first_http=503,first_native='superseded',old_code=409,explicit_new_http=True,
            smtp_submissions=2,valid_code_count=1,automatic_retries=0,semantics='at-least-once mail, distinct one-time attempts'))
        self.protocol.mode='lose_quit_response';count=len(self.protocol.captures)
        self.invite('quit-loss');before,mail,row=self.proof(before,self.protocol.address('quit-loss'),'delivered')
        self.invite('quit-loss',index=1,want=409,code='invitation_conflict');check(len(self.protocol.captures)==count+1 and self.ledger()==before,'QUIT uncertainty caused repeated SMTP')
        emit('invitation.quit_response_loss','passed',dict(data_250_accepted=True,http_status=200,native_status='delivered',duplicates=0))
        self.protocol.mode='accept';count=len(self.protocol.captures)
        try:
            self.install_fault();self.invite('audit-failure',want=503,code='invitation_unavailable')
            before,mail,row=self.proof(before,self.protocol.address('audit-failure'),'pending')
        finally:self.remove_fault()
        self.invite('audit-failure',index=1,want=409,code='invitation_conflict');self.accept(mail,want=409,code='invitation_invalid')
        check(self.ledger()==before and len(self.protocol.captures)==count+1,'pending audit rollback invitation became usable or resent')
        emit('invitation.audit_commit_failure','passed',dict(relay_accepted=True,native_pending=True,no_success_audit=True,code_invalid=True,immediate_retries=0,
            recovery='bounded pending expiry/replacement after real two-minute window remains TODO'))
        check(len(before['invitations'])==5 and len(self.protocol.captures)==5 and sum(x['status']=='accepted' for x in before['invitations'])==2
            and sum(x['status']=='delivered' for x in before['invitations'])==1 and sum(x['status']=='superseded' for x in before['invitations'])==1
            and sum(x['status']=='pending' for x in before['invitations'])==1,'complete SMTP/native attempt population differs')
        emit('invitation.operation_linkage','passed',dict(native_attempts=5,smtp_submissions=5,accepted_accounts=2,delivered_unconsumed=1,superseded=1,pending=1,exact_linkage=True))

    def run(self):
        self.provision();self.prepare();self.start()
        self.login('admin');identity,_=self.call('admin','GET','/api/v1/auth/me',scenario='fixture')
        check(identity['user']['email']==self.admin_email and identity['user']['role']=='admin'
            and re.fullmatch('[a-f0-9]{32}',identity['user']['id']) is not None,'native bootstrap identity not proved by public HTTPS')
        self.seed()
        for name in ('reader','peer','denied'):self.login(name)
        self.cases();self.frozen()
        result=dict(run_id=self.run_id,targets=list(TARGETS),http_calls=self.calls,proof_scope=PROOF_SCOPE,external_mailbox_verified=False,
            remaining_todo=['Real SMTP organization/mailbox delivery and reading','Real continuous 24-hour expiry','Two-minute pending crash recovery window',
                'Tenant-owned reviewed viewer-role and global invitation authority'])
        readonly._private(self.work/'invitation-result.json',result);return result

    def cleanup(self):
        error=None
        try:
            if self.fault_installed:self.remove_fault()
        except BaseException as failure:error=failure
        for server in (self.relay,self.smtp):
            try:
                if server:server.close()
            except BaseException as failure:error=error or failure
        try:readonly._private(self.work/'smtp-captures.json',dict(run_id=self.run_id,captures=self.protocol.captures,external_delivery=False))
        except BaseException as failure:error=error or failure
        super().cleanup()
        if error:raise error


def main():
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--live',action='store_true');args=parser.parse_args()
    if not args.live:parser.error('root-reviewed --live required')
    try:
        with exclusive(args.root),readonly.cooperative_signals():
            ports_free();data=readonly.doctor(args.root);info=readonly.guard_pg(readonly.inspect_pg(data['postgres_container']),readonly.OWNER,data['postgres_image_id'])
            if not re.fullmatch('[a-f0-9]{64}',info.get('Id','')):raise Blocked('exact invitation PG container identity missing')
            data=dict(data,postgres_container_id=info['Id']);readonly.execute(Chain(args.root,data,str(uuid.uuid4())))
    except Blocked as error:emit('invitation.environment','blocked',dict(error=str(error)));return 2
    except Exception as error:emit('invitation.failure','failed',dict(error_type=type(error).__name__,error=str(error)));return 1
    return 0


if __name__=='__main__':raise SystemExit(main())
