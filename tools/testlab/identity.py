"""Owned short-lived formal V3 → real Keycloak authorization-code acceptance.

No business .env, existing Keycloak realm, base APIs or soak config is changed.
Only an in-memory Popen handle may be signaled; persisted PIDs are evidence.
"""
from __future__ import annotations

import argparse
import contextlib
from decimal import Decimal, InvalidOperation
import fcntl
import hashlib
import html
import http.client
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import ssl
import subprocess
import sys
import time
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import urllib.error
import urllib.parse
import urllib.request
import uuid

from env import _private, _inspect, _owner
from faults import Blocked, attest, private_json
from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
ORIGIN = 'https://localhost:18586'
INTERNAL = 'http://127.0.0.1:18587'
KEYCLOAK = 'https://localhost:18590'
IMAGE = 'quay.io/keycloak/keycloak:26.7.4'
OWNER_LABEL = 'aiops.testlab.owner'
RUN_LABEL = 'aiops.testlab.identity-run'
LIMIT = 2 << 20
KEYCLOAK_MEMORY_BYTES = 1 << 30
KEYCLOAK_RESERVE_BYTES = 256 << 20
CALLBACK = '/api/v1/auth/oidc/callback'


def emit(name, status, actual, **fields):
    module = fields.pop('module', 'formal-identity')
    print(json.dumps(dict(name=name, status=status, actual=actual, module='formal-identity',
                          evidence_mode='real_local', **fields) | {'module': module}, ensure_ascii=False), flush=True)


def free_port(port):
    with socket.socket() as sock:
        sock.settimeout(.3)
        if sock.connect_ex(('127.0.0.1', port)) == 0:
            raise Blocked('dedicated identity port occupied: ' + str(port))


@contextlib.contextmanager
def cooperative_signals():
    def interrupt(signum, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise Blocked('local chain interrupted; owned finally cleanup required')
    previous = signal.signal(signal.SIGTERM, interrupt)
    try:
        yield
    finally:
        signal.signal(signal.SIGTERM, previous)


@contextlib.contextmanager
def cleanup_signals():
    # The first cancellation can arrive after the body already succeeded.
    # Record it without throwing into, or abandoning, bounded cleanup steps.
    state={'interrupted':False}
    def interrupt(signum,frame):state['interrupted']=True
    previous=signal.signal(signal.SIGTERM,interrupt)
    try:yield state
    finally:signal.signal(signal.SIGTERM,previous)


@contextlib.contextmanager
def exclusive(root):
    path = Path(root) / '.local/testlab/identity.lock'
    fd = os.open(path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        if os.fstat(fd).st_mode & 0o777 != 0o600:
            raise Blocked('private identity lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another identity chain owns the fixed local ports')
        yield
    finally:
        os.close(fd)


def docker(argv, timeout=30, input=None):
    result = subprocess.run(['docker', *argv], input=input, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise Blocked('owned Docker operation failed; no command credentials exposed')
    return result.stdout


def resource_headroom():
    # Docker stats reports current container usage, not VM/kernel free memory.
    # Reserve another 256MiB and record this evidence as an estimate, not proof
    # that allocations cannot subsequently race or exhaust host memory.
    units = {'B': 1, 'kB': 1000, 'MB': 1000**2, 'GB': 1000**3,
             'KiB': 1024, 'MiB': 1024**2, 'GiB': 1024**3, 'TiB': 1024**4}
    try:
        total = int(docker(['info', '--format', '{{.MemTotal}}'], timeout=5).strip())
        samples = docker(['stats', '--no-stream', '--format', '{{.MemUsage}}'], timeout=10).splitlines()
        if total <= 0 or not samples:
            raise ValueError('missing memory evidence')
        used = 0
        for sample in samples:
            match = re.fullmatch(r'([0-9]+(?:\.[0-9]+)?)\s*(B|kB|MB|GB|KiB|MiB|GiB|TiB)', sample.split('/', 1)[0].strip())
            if not match:
                raise ValueError('invalid memory evidence')
            used += int(Decimal(match[1]) * units[match[2]])
    except (ValueError, InvalidOperation):
        raise Blocked('Docker memory headroom evidence unavailable') from None
    if total - used < KEYCLOAK_MEMORY_BYTES + KEYCLOAK_RESERVE_BYTES:
        raise Blocked('Docker estimated memory headroom insufficient for isolated 1GiB Keycloak plus reserve')
    image_id = docker(['image', 'inspect', '--format', '{{.Id}}', IMAGE], timeout=5).strip()
    if not image_id.startswith('sha256:'):
        raise Blocked('Keycloak immutable image identity unavailable')
    return dict(docker_memory_bytes=total, container_usage_bytes=used, estimated_headroom_bytes=total-used,
                memory_limit_bytes=KEYCLOAK_MEMORY_BYTES, reserve_bytes=KEYCLOAK_RESERVE_BYTES,
                cpu_limit=1, image_id=image_id, measurement='Docker current container usage; host/kernel reserve estimated')


def remaining(deadline, maximum):
    if deadline is None:
        return maximum
    seconds = deadline - time.monotonic()
    if seconds <= 0:
        raise Blocked('owned cleanup exceeded its 40-second budget')
    return min(seconds, maximum)


def budgeted_inspect(container, deadline=None):
    if deadline is None:
        return _inspect(container)
    result = subprocess.run(['docker', 'inspect', container], capture_output=True, text=True, timeout=remaining(deadline, 3))
    if result.returncode:
        if 'no such' in result.stderr.lower():
            return None
        raise Blocked('owned cleanup cannot attest Docker resource')
    return json.loads(result.stdout)[0]


def validate_component(root, backend, component):
    expected = Path(root).resolve() / '.local/testlab/environments' / backend['owner']
    if component.get('owner') != backend['owner'] or Path(component.get('work', '')).resolve() != expected:
        raise Blocked('identity component owner/path mismatch')
    services = [x for x in component.get('services', []) if x.get('name') == 'keycloak']
    if len(services) != 1 or services[0].get('container') != 'aiops-' + backend['owner'] + '-keycloak' or services[0].get('url') != 'http://127.0.0.1:18576':
        raise Blocked('generated owned Keycloak component required')
    return services[0]['container']


def guard_run_container(info, owner, run_id, name):
    if info is None or info.get('Name') != '/' + name or info.get('Config', {}).get('Image') != IMAGE:
        raise Blocked('identity run container name/image mismatch')
    labels = info.get('Config', {}).get('Labels', {})
    if labels.get(OWNER_LABEL) != owner or labels.get(RUN_LABEL) != run_id:
        raise Blocked('identity run container owner/run mismatch; refusing mutation')
    return info


def guard_component_port(info, port, internal_port=8080):
    bindings = info.get('NetworkSettings', {}).get('Ports', {}).get(str(internal_port)+'/tcp', []) or []
    if info.get('Config', {}).get('Image') != IMAGE or bindings != [{'HostIp': '127.0.0.1', 'HostPort': str(port)}]:
        raise Blocked('Keycloak loopback port/image ownership mismatch')


def doctor(root):
    backend, current = attest(root)
    component = private_json(Path(root) / '.local/testlab/component-environment.json')
    container = validate_component(root, backend, component)
    info = _inspect(container)
    if not info or not info.get('State', {}).get('Running'):
        raise Blocked('owned Keycloak component is not running')
    _owner(info, backend['owner'])
    guard_component_port(info, 18576)
    for filename in ('tls-cert.pem','tls-key.pem'):
        path=Path(component['work'])/filename
        if path.is_symlink() or not path.is_file() or path.stat().st_mode&0o777!=0o600:
            raise Blocked('owned private Keycloak HTTPS certificate/key required')
    docker(['image', 'inspect', IMAGE])  # No pull or opportunistic installation.
    for port in (18586, 18587, 18590):
        free_port(port)
    return backend, current, component


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def browser(cafile=None):
    jar = http.cookiejar.CookieJar()
    handlers=[urllib.request.HTTPCookieProcessor(jar),NoRedirect()]
    if cafile is not None:handlers.append(urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=cafile)))
    return urllib.request.build_opener(*handlers), jar


class TLSProxy:
    """Owned HTTPS entry to only this invocation's live Popen socket."""
    def __init__(self, chain):
        self.chain=chain
        proxy=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def relay(self):
                if not (self.path.startswith('/api/v1/') or self.path in ('/healthz','/readyz')):
                    self.send_error(404);return
                if proxy.chain.proc is None or proxy.chain.proc.poll() is not None:
                    self.send_error(503);return
                connection=None
                try:
                    self.connection.settimeout(5)
                    length=int(self.headers.get('Content-Length','0'))
                    if not 0<=length<=1<<20:self.send_error(413);return
                    body=self.rfile.read(length) if length else None
                    headers={k:v for k,v in self.headers.items() if k.lower() not in ('host','connection','transfer-encoding','content-length','x-forwarded-for')}
                    headers['X-Forwarded-For']='127.0.0.1'
                    connection=http.client.HTTPConnection('127.0.0.1',18587,timeout=10)
                    connection.request(self.command,self.path,body,headers);response=connection.getresponse();raw=response.read(LIMIT+1)
                    if len(raw)>LIMIT:raise ValueError('bounded response exceeded')
                    self.send_response(response.status)
                    for key,value in response.getheaders():
                        if key.lower() not in ('connection','transfer-encoding','content-length','server','date'):self.send_header(key,value)
                    self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (OSError,ValueError):
                    try:self.send_error(502)
                    except OSError:pass
                finally:
                    if connection:connection.close()
            do_GET=do_POST=do_DELETE=relay
        self.server=ThreadingHTTPServer(('127.0.0.1',18586),Handler);self.server.daemon_threads=True
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(str(chain.ca_file),str(chain.ca_file.with_name('tls-key.pem')))
        self.server.socket=context.wrap_socket(self.server.socket,server_side=True)
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True);self.thread.start()

    def close(self):
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=3)


def validate_url(url, origins=(ORIGIN, KEYCLOAK)):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme + '://' + parsed.netloc not in origins or parsed.username is not None or parsed.fragment:
        raise Blocked('identity request left fixed local origins')
    return parsed


def transport(opener, method, url, body=None, headers=None, origins=(ORIGIN, KEYCLOAK), timeout=15):
    validate_url(url, origins)
    req = urllib.request.Request(url, body, headers or {}, method=method)
    try:
        response = opener.open(req, timeout=timeout)
    except urllib.error.HTTPError as response_error:
        response = response_error
    with response:
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise AssertionError('identity HTTP response exceeded bound')
        return response.status, raw, response.headers


def cookie_value(jar, name):
    # CookieJar canonicalizes a host-only localhost cookie to localhost.local.
    # Use its effective request host without widening accepted cookie scope.
    host=http.cookiejar.eff_request_host(urllib.request.Request(ORIGIN))[1]
    matches = [cookie for cookie in jar if cookie.name == name and cookie.domain == host]
    expected_path=CALLBACK if name=='aiops_oidc_state' else '/'
    if len(matches) != 1 or matches[0].domain_specified or matches[0].path!=expected_path or matches[0].secure!=(urllib.parse.urlsplit(ORIGIN).scheme=='https') or not matches[0].value:
        raise AssertionError('expected one generated ' + name + ' cookie')
    return matches[0].value


def cleanup_process(proc, deadline=None):
    # A direct unreaped child handle cannot refer to a newly reused PID. Never
    # signal a PID reconstructed from a state file or obtained with ps/pgrep.
    if proc is None or proc.poll() is not None:
        return {'already_exited': True, 'forced_kill': False}
    proc.terminate()
    forced = False
    try:
        proc.wait(timeout=remaining(deadline, 20))
    except subprocess.TimeoutExpired:
        forced = True
        proc.kill()
        proc.wait(timeout=remaining(deadline, 5))
    return {'exit_code': proc.returncode, 'forced_kill': forced}


def safe_error(error):
    return str(error) if isinstance(error, (Blocked, AssertionError)) else type(error).__name__


def execute(chain):
    primary = cleanup = None
    actual = None
    evidence_failures = []
    def best_effort(operation, action):
        try:
            action()
        except BaseException as error:
            # Diagnostics cannot replace the contract error or strand owned
            # resources. Retain only the operation and type, never IO payloads
            # which can contain private paths or credentials.
            evidence_failures.append({'operation': operation, 'error_type': type(error).__name__})
    def report(stage, filename, error, event=None):
        row = dict(stage=stage, error_type=type(error).__name__, error=safe_error(error))
        best_effort(filename, lambda: _private(chain.work / filename, row))
        best_effort(stage+'.first_failure', lambda: preserve_first_failure(chain.work, row))
        if event is not None:
            best_effort(event+'.emit', lambda: emit(chain.module + '.' + event, 'failed', row, module=chain.module))
    try:
        try:
            actual = chain.run()
        except BaseException as error:
            primary = error
            best_effort('business_contract.report', lambda: report('business_contract', 'primary-error.json', error))
    finally:
        with cleanup_signals() as interrupted:
            try:
                chain.cleanup()
            except BaseException as error:
                cleanup = error
                best_effort('cleanup.report', lambda: report('cleanup', 'cleanup-error.json', error, 'cleanup_failure'))
    if interrupted['interrupted']:
        # Set the outcome before reporting so cancellation stays non-green
        # even when every evidence sink is unavailable.
        if primary is None and cleanup is None:
            primary=Blocked('local chain interrupted during cleanup; bounded cleanup completed')
        row=dict(stage='cleanup_interrupted',interrupted=True,cleanup_completed=cleanup is None)
        best_effort('cleanup-interrupted.json',lambda: _private(chain.work/'cleanup-interrupted.json',row))
        best_effort('cleanup_interrupted.first_failure',lambda: preserve_first_failure(chain.work,row))
        best_effort('cleanup_interrupted.emit',lambda: emit(chain.module+'.cleanup_interrupted','blocked',row,module=chain.module))
    if evidence_failures:
        best_effort('evidence_failure.emit', lambda: emit(chain.module+'.evidence_failure','failed',
                    {'failures':list(evidence_failures)},module=chain.module))
        error = primary if primary is not None else cleanup
        if error is not None:
            best_effort('evidence_failure.note',lambda: error.add_note('testlab evidence failures: '+json.dumps(evidence_failures)))
    if primary is not None:
        raise primary
    if cleanup is not None:
        raise cleanup
    return actual


def preserve_first_failure(work, row):
    try:
        fd = os.open(Path(work) / 'first-failure.json', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'w') as stream:
            json.dump(row, stream)
    except FileExistsError:
        pass


class Chain:
    def __init__(self, root, backend, run_id):
        self.root, self.backend, self.run_id = Path(root), backend, run_id
        self.work = self.root / '.local/testlab/environments' / backend['owner'] / 'identity' / run_id
        self.work.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.call_number = 0
        self.origin = ORIGIN
        self.module = 'formal-identity'
        self.test_name = 'FormalKeycloakOIDC'
        self.proc = None
        self.tls_proxy = None
        self.container = 'aiops-' + backend['owner'] + '-identity-' + run_id[:12]
        self.realm = 'identity-' + run_id
        self.client = 'formal-identity-' + run_id
        self.username = 'identity-' + run_id
        self.password = secrets.token_urlsafe(32)
        self.user_id = uuid.uuid4().hex
        self.user_created = False
        self.ca_file = self.root / '.local/testlab/environments' / backend['owner'] / 'tls-cert.pem'
        self.auth_phases=[]
        self.image_hash = hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        self.issuer = KEYCLOAK + '/realms/' + self.realm
        self.state = dict(owner=backend['owner'], run_id=run_id, container=self.container, work=str(self.work),
                          origin=ORIGIN, issuer=self.issuer, image=IMAGE, source_tree_sha=backend['source_tree_sha'],
                          binary_sha256=self.image_hash, pid=None, user_id=self.user_id)
        _private(self.work / 'state.json', self.state)

    def frozen(self):
        if source_fingerprint(self.root) != self.backend['source_tree_sha'] or hashlib.sha256(Path(self.backend['binary']).read_bytes()).hexdigest() != self.image_hash:
            raise Blocked('formal identity source/binary changed during acceptance')

    def keycloak_call(self, opener, method, url, body=None, headers=None):
        try:
            return transport(opener, method, url, body, headers)
        except Exception as error:
            # Preserve stage and terminal container state before finally removes
            # the resource. Never persist a code/state query or login body.
            info = guard_run_container(_inspect(self.container), self.backend['owner'], self.run_id, self.container)
            state = info.get('State', {})
            observation = {key: state.get(key) for key in ('Running', 'OOMKilled', 'ExitCode', 'FinishedAt')}
            row = dict(stage='keycloak_transport', method=method, path=urllib.parse.urlsplit(url).path,
                       error=type(error).__name__, container_state=observation)
            _private(self.work / 'keycloak-transport-failure.json', row)
            identity_status = 'blocked' if state.get('OOMKilled') else 'failed'
            emit('identity.keycloak_transport', identity_status, row)
            if state.get('OOMKilled'):
                raise Blocked('owned Keycloak was OOM-killed during authorization; environment resource gate') from None
            raise AssertionError('Keycloak transport failed at ' + method + ' ' + row['path'] + '; private stage/state evidence retained') from None

    def sql(self, statement):
        deadline = getattr(self, 'cleanup_deadline', None)
        info = budgeted_inspect(self.backend['postgres_container'], deadline)
        if not info or not info.get('State', {}).get('Running'):
            raise Blocked('owned database not running')
        _owner(info, self.backend['owner'])
        return docker(['exec', '-i', self.backend['postgres_container'], 'psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1',
                       '-U', 'testlab', '-d', 'aiops_testlab_app'], input=statement, timeout=remaining(deadline, 3 if deadline else 30)).strip()

    def call(self, opener, method, path, body=None, want=200, code=None, headers=None, scenario='normal'):
        if not path.startswith('/api/v1/'):
            raise Blocked('identity business route boundary')
        h = {'Origin': self.origin, 'X-AIOps-Environment': self.backend['environment']}
        if body is not None:
            h['Content-Type'] = 'application/json'
        h.update(headers or {})
        status, raw, response_headers = transport(opener, method, self.origin + path,
                                                  json.dumps(body).encode() if body is not None else None, h, origins=(self.origin,))
        try:
            parsed = json.loads(raw) if raw and status not in (204, 302) else {}
        except (ValueError, UnicodeDecodeError):
            parsed = {'error': {'code': 'non_json_response'}}
        actual_code = parsed.get('error', {}).get('code') if isinstance(parsed, dict) else None
        self.call_number += 1
        safe_path = urllib.parse.urlsplit(path).path
        if self.user_id:
            safe_path = safe_path.replace(self.user_id, getattr(self, 'id_placeholder', '{user}'))
        expected = want if isinstance(want, tuple) else (want,)
        matched = status in expected and actual_code != 'non_json_response' and (code is None or actual_code == code)
        row = dict(name=self.module + '.http.' + str(self.call_number), test_name=self.test_name, method=method,
                   path=safe_path, scenario=scenario, expected={'status': list(expected), 'error_code': code},
                   actual={'status': status, 'error_code': actual_code}, status='passed' if matched else 'failed',
                   run_id=self.run_id, call_id=self.run_id + '-' + str(self.call_number))
        with open(self.work / 'interfaces.jsonl', 'a', encoding='utf-8') as stream:
            os.chmod(self.work / 'interfaces.jsonl', 0o600)
            stream.write(json.dumps(row) + '\n')
        emit(row['name'], row['status'], row['actual'], interface=row, module=self.module)
        if not matched:
            # O_EXCL keeps first red forever, without code/state/cookie/HTML.
            red = self.work / 'first-failure.json'
            try:
                fd = os.open(red, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                with os.fdopen(fd, 'w') as stream:
                    json.dump(row, stream)
            except FileExistsError:
                pass
            raise AssertionError('formal OIDC HTTP contract failed; sanitized first red retained')
        return parsed, response_headers

    def start(self):
        self.frozen()
        realm = dict(realm=self.realm, enabled=True, sslRequired='none', clients=[dict(clientId=self.client, enabled=True,
                     publicClient=True, directAccessGrantsEnabled=True, standardFlowEnabled=True,
                     redirectUris=[ORIGIN + CALLBACK])], users=[dict(username=self.username, enabled=True,
                     firstName='Identity', lastName='Fixture', email=self.username + '@example.invalid', emailVerified=True,
                     credentials=[dict(type='password', value=self.password, temporary=False)])])
        _private(self.work / 'realm.json', realm)
        if _inspect(self.container) is not None:
            raise Blocked('fresh identity container name already exists')
        resources = resource_headroom()
        self.state['resources'] = resources
        self.state['transport'] = dict(issuer='verified_https',callback='verified_https',internal_origin=INTERNAL,
                                       ca_sha256=hashlib.sha256(self.ca_file.read_bytes()).hexdigest(),container_https_port=8443)
        _private(self.work / 'state.json', self.state)
        emit('identity.resource_gate', 'passed', resources)
        docker(['run', '--pull=never', '-d', '--name', self.container, '--label', OWNER_LABEL + '=' + self.backend['owner'],
                '--label', RUN_LABEL + '=' + self.run_id, '--memory', '1g', '--cpus', '1',
                '-p', '127.0.0.1:18590:8443', '-v', str(self.work / 'realm.json') + ':/opt/keycloak/data/import/realm.json:ro',
                '-v', str(self.ca_file)+':/opt/keycloak/conf/testlab.crt:ro',
                '-v', str(self.ca_file.with_name('tls-key.pem'))+':/opt/keycloak/conf/testlab.key:ro',
                IMAGE, 'start-dev', '--import-realm', '--hostname-strict=false',
                '--https-certificate-file=/opt/keycloak/conf/testlab.crt', '--https-certificate-key-file=/opt/keycloak/conf/testlab.key'], timeout=60)
        info = guard_run_container(_inspect(self.container), self.backend['owner'], self.run_id, self.container)
        guard_component_port(info, 18590, internal_port=8443)
        if info.get('Image') != resources['image_id'] or info.get('HostConfig', {}).get('Memory') != KEYCLOAK_MEMORY_BYTES or info.get('HostConfig', {}).get('NanoCpus') != 1000000000:
            raise Blocked('new Keycloak image/resource limits differ from attested startup gate')
        opener, _ = browser(cafile=str(self.ca_file))
        deadline = time.monotonic() + 120
        while True:
            try:
                status, raw, _ = transport(opener, 'GET', self.issuer + '/.well-known/openid-configuration')
                if status == 200 and json.loads(raw).get('issuer') == self.issuer:
                    break
            except (OSError, urllib.error.URLError):
                pass
            if time.monotonic() >= deadline:
                raise Blocked('owned 1GiB Keycloak readiness timeout; private log retained')
            time.sleep(1)
        variables = dict(DATABASE_URL=self.backend['database_url'], AIOPS_V3_INSTANCE_CONFIG=self.backend['instance_config'],
                         AIOPS_ADDR='127.0.0.1:18587', AIOPS_PUBLIC_ORIGIN=ORIGIN, AIOPS_SECURE_COOKIES='true',
                         AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32',
                         AIOPS_ADMIN_EMAIL=self.backend['admin']['email'], AIOPS_ADMIN_PASSWORD=self.backend['admin']['password'],
                         AIOPS_V3_OIDC_ISSUER=self.issuer, AIOPS_V3_OIDC_CLIENT_ID=self.client,
                         AIOPS_V3_OIDC_REDIRECT_URL=ORIGIN + CALLBACK, AIOPS_V3_OIDC_ALLOW_LOOPBACK_HTTP='false',
                         SSL_CERT_FILE=str(self.ca_file),
                         OPENAI_MODEL='testlab', OPENAI_BASE_URL='http://127.0.0.1:18578/v1', OPENAI_API_KEY='synthetic-testlab-key',
                         EMBEDDING_PROVIDER='openai', EMBEDDING_MODEL='synthetic-testlab',
                         EMBEDDING_BASE_URL='http://127.0.0.1:18578/v1', EMBEDDING_API_KEY='synthetic-testlab-key')
        self.frozen()
        with open(self.work / 'formal-api.log', 'ab') as log:
            os.chmod(self.work / 'formal-api.log', 0o600)
            self.proc = subprocess.Popen([self.backend['binary']], cwd=self.root, env=command_env(variables, {}), stdout=log, stderr=log)
        self.state['pid'] = self.proc.pid
        _private(self.work / 'state.json', self.state)
        deadline = time.monotonic() + 60
        while True:
            if self.proc.poll() is not None:
                raise AssertionError('formal OIDC process exited at startup; private API log retained')
            try:
                if transport(opener, 'GET', INTERNAL + '/healthz',origins=(INTERNAL,))[0] == 200:
                    break
            except (OSError, urllib.error.URLError):
                pass
            if time.monotonic() >= deadline:
                raise Blocked('formal OIDC API readiness timeout')
            time.sleep(.5)
        self.tls_proxy=TLSProxy(self)

    def record_auth_phase(self,stage,status,headers,jar):
        location=urllib.parse.urlsplit(headers.get('Location',''))
        self.auth_phases.append(dict(stage=stage,status=status,
            location_origin=location.scheme+'://'+location.netloc if location.netloc else '',location_path=location.path,
            cookies=[dict(name=c.name,domain=c.domain,path=c.path,secure=c.secure) for c in jar]))
        _private(self.work/'keycloak-auth-phases.json',self.auth_phases)

    def authorize(self, want, error=None):
        opener, jar = browser(cafile=str(self.ca_file))
        _, start_headers = self.call(opener, 'GET', '/api/v1/auth/oidc/start', want=302)
        self.record_auth_phase('formal_oidc_start',302,start_headers,jar)
        state = cookie_value(jar, 'aiops_oidc_state')
        auth_url = start_headers.get('Location', '')
        parsed = validate_url(auth_url)
        if parsed.scheme + '://' + parsed.netloc != KEYCLOAK or parsed.path != '/realms/' + self.realm + '/protocol/openid-connect/auth':
            raise AssertionError('authorization redirect did not target exact owned Keycloak realm')
        status, raw, headers = self.keycloak_call(opener, 'GET', auth_url)
        self.record_auth_phase('authorization_form',status,headers,jar)
        if status != 200:
            raise AssertionError('real Keycloak authorization form unavailable')
        action = re.search(r'<form\b[^>]*\baction="([^"]+)"', raw.decode(), re.S)
        if not action:
            raise AssertionError('real Keycloak login form absent')
        action_url = html.unescape(action.group(1))
        if validate_url(action_url).path != '/realms/' + self.realm + '/login-actions/authenticate':
            raise AssertionError('Keycloak form action left exact owned realm')
        form = urllib.parse.urlencode({'username': self.username, 'password': self.password}).encode()
        status, raw, headers = self.keycloak_call(opener, 'POST', action_url, form, {'Content-Type': 'application/x-www-form-urlencoded'})
        self.record_auth_phase('credentials_submitted',status,headers,jar)
        callback_url = headers.get('Location', '')
        if status != 302 or validate_url(callback_url).path != CALLBACK or not callback_url.startswith(ORIGIN + CALLBACK + '?'):
            raise AssertionError('real Keycloak did not issue the exact formal callback redirect')
        parsed, callback_headers = self.call(opener, 'GET', callback_url[len(ORIGIN):], want=want, code=error,
                                             scenario='normal' if want == 302 else 'authorization')
        if want == 302 and callback_headers.get('Location') != '/':
            raise AssertionError('successful OIDC callback did not redirect to product root')
        return opener, jar, callback_url, state

    def run(self):
        self.start()
        admin, _ = browser(cafile=str(self.ca_file))
        self.call(admin, 'POST', '/api/v1/auth/login', self.backend['admin'])
        # Exact disposable local user only; binding itself uses formal API.
        self.user_created = True  # Also retire after an uncertain INSERT acknowledgement.
        self.sql("INSERT INTO users(id,email,name,role,password_hash,source,external_id) VALUES(" + quote(self.user_id) + ',' +
                 quote(self.username + '@example.invalid') + ",'Owned OIDC test human','viewer','unused-local-password','local','');")
        _, _, _, _ = self.authorize(403, 'identity_not_provisioned')
        opener, _ = browser(cafile=str(self.ca_file))
        form = urllib.parse.urlencode({'grant_type': 'password', 'client_id': self.client, 'scope': 'openid',
                                       'username': self.username, 'password': self.password}).encode()
        status, raw, _ = self.keycloak_call(opener, 'POST', self.issuer + '/protocol/openid-connect/token', form,
                                    {'Content-Type': 'application/x-www-form-urlencoded'})
        token = json.loads(raw)
        if status != 200 or not token.get('id_token'):
            raise AssertionError('owned Keycloak subject pre-provisioning failed')
        import base64
        part = token['id_token'].split('.')[1]
        subject = json.loads(base64.urlsafe_b64decode(part + '=' * (-len(part) % 4)))['sub']
        self.call(admin, 'POST', '/api/v1/auth/oidc/bindings', {'user_id': self.user_id, 'subject': subject}, want=201)
        binding, _ = self.call(admin, 'GET', '/api/v1/auth/oidc/bindings/' + self.user_id)
        if binding.get('subject') != subject or binding.get('user_id') != self.user_id:
            raise AssertionError('formal binding did not persist exact owned subject')
        user, jar, callback_url, state = self.authorize(302)
        me, _ = self.call(user, 'GET', '/api/v1/auth/me')
        if me.get('user', {}).get('id') != self.user_id:
            raise AssertionError('formal OIDC session mapped to wrong local user')
        session_hash = hashlib.sha256(cookie_value(jar, 'aiops_session').encode()).hexdigest()
        observation = json.loads(self.sql("SELECT json_build_object('session',EXISTS(SELECT 1 FROM sessions WHERE token_hash=" + quote(session_hash) +
            ' AND user_id=' + quote(self.user_id) + " AND expires_at>clock_timestamp()),'login_audit',(SELECT count(*) FROM management_audit WHERE actor_id=" +
            quote(self.user_id) + " AND action='account.oidc_login'),'bound_audit',(SELECT count(*) FROM management_audit WHERE target=" +
            quote(self.user_id) + " AND action='account.oidc_bound'))::text;"))
        if observation != {'session': True, 'login_audit': 1, 'bound_audit': 1}:
            raise AssertionError('formal OIDC session/audit durable observation mismatch')
        emit('identity.durable_login', 'passed', observation)
        self.call(user, 'GET', callback_url[len(ORIGIN):], want=401, code='identity_rejected',
                  headers={'Cookie': 'aiops_oidc_state=' + state}, scenario='idempotency')
        self.call(admin, 'DELETE', '/api/v1/auth/oidc/bindings/' + self.user_id, want=204)
        self.call(user, 'GET', '/api/v1/auth/me', want=401, scenario='authorization')
        self.authorize(403, 'identity_not_provisioned')
        observation = json.loads(self.sql("SELECT json_build_object('sessions',(SELECT count(*) FROM sessions WHERE user_id=" + quote(self.user_id) +
            "),'bindings',(SELECT count(*) FROM v3_oidc_identities WHERE user_id=" + quote(self.user_id) +
            "),'login_audit',(SELECT count(*) FROM management_audit WHERE actor_id=" + quote(self.user_id) +
            " AND action='account.oidc_login'),'unbound_audit',(SELECT count(*) FROM management_audit WHERE target=" + quote(self.user_id) +
            " AND action='account.oidc_unbound'))::text;"))
        if observation != {'sessions': 0, 'bindings': 0, 'login_audit': 1, 'unbound_audit': 1}:
            raise AssertionError('unbind/rejected callbacks changed durable session or audit contract')
        self.frozen()
        return dict(flow='formal binary → real Keycloak auth code → binding/session/audit → replay/unbind fail closed',
                    observed=observation, calls=self.call_number, production_idp=False, https_cookie_acceptance=True,
                    idp_cookie_transport='verified_https',product_cookie_transport='verified_https')

    def cleanup(self):
        self.cleanup_deadline = time.monotonic() + 40
        failures = []
        try:process = cleanup_process(self.proc, self.cleanup_deadline)
        except Exception as error:
            process={'error_type':type(error).__name__};failures.append('formal identity process cleanup failed')
        if process.get('forced_kill'):
            failures.append('formal identity process required SIGKILL')
        if self.tls_proxy:
            try:self.tls_proxy.close()
            except Exception:failures.append('owned identity TLS entry cleanup failed')
        if self.user_created:
            try:
                # Keep immutable audit and the disabled user it references.
                self.sql('BEGIN; DELETE FROM sessions WHERE user_id=' + quote(self.user_id) +
                         '; DELETE FROM v3_oidc_identities WHERE user_id=' + quote(self.user_id) +
                         '; UPDATE users SET disabled=true WHERE id=' + quote(self.user_id) + '; COMMIT;')
            except Exception:
                failures.append('owned identity user retirement failed')
        info = budgeted_inspect(self.container, self.cleanup_deadline)
        if info is not None:
            guard_run_container(info, self.backend['owner'], self.run_id, self.container)
            _private(self.work / 'keycloak-final-state.json', {key: info.get('State', {}).get(key) for key in ('Running', 'OOMKilled', 'ExitCode', 'FinishedAt')})
            try:
                logs = docker(['logs', '--tail', '100', self.container], timeout=remaining(self.cleanup_deadline, 3))
                with open(self.work / 'keycloak.log', 'w') as stream:
                    os.chmod(self.work / 'keycloak.log', 0o600)
                    stream.write(logs)
            except Exception:failures.append('owned Keycloak final log capture failed')
            try:docker(['rm', '-f', self.container], timeout=remaining(self.cleanup_deadline, 3))
            except Exception:failures.append('owned Keycloak container cleanup failed')
        self.state.update(stopped=True, cleanup=dict(process=process, failures=failures))
        _private(self.work / 'state.json', self.state)
        if failures:
            raise AssertionError('; '.join(failures))
        emit('identity.cleanup', 'passed', dict(process=process, run_container_removed=True, owned_user_retired=self.user_created))


def quote(value):
    return "'" + str(value).replace("'", "''") + "'"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['doctor', 'chain'])
    parser.add_argument('--root', default=str(ROOT))
    args = parser.parse_args()
    root = Path(args.root).resolve()
    chain = None
    exit_code = 0
    try:
        with exclusive(root):
            backend, current, _ = doctor(root)
            emit('identity.doctor', 'passed', dict(owner=backend['owner'], source_tree_sha=backend['source_tree_sha'],
                 ports=[18586,18587,18590], scope='owned PG/Keycloak/base supervisor attested; exact source binary; verified HTTPS IdP/product + owned private HTTP socket'))
            if args.command == 'doctor':
                return 0
            chain = Chain(root, backend, uuid.uuid4().hex)
            actual = execute(chain)
        _private(chain.work / 'result.json', dict(status='passed', actual=actual, source_tree_sha=backend['source_tree_sha']))
        emit('identity.formal_keycloak', 'passed', actual, artifact=str((chain.work/'result.json').relative_to(root)))
    except Exception as error:
        status = 'blocked' if isinstance(error, Blocked) else 'failed'
        exit_code = 2 if status == 'blocked' else 1
        # Exceptions use authored fixed messages, never raw redirect/token/HTML.
        message = safe_error(error)
        if chain:
            _private(chain.work / 'result.json', dict(status=status, error=message))
        emit('identity.formal_keycloak', status, message)
    return exit_code


if __name__ == '__main__':
    with cooperative_signals():
        raise SystemExit(main())
