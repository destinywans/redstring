"""Formal members sync/reminders through an owned CONNECT/TLS protocol Mock.

Root alone schedules --live. Offline tests never listen or acquire resources.
Local users/attempts/audits are produced by native V3 HTTP; only isolated
identity/grant fixtures and a scoped audit rejection trigger use SQL writes.
Protocol acceptance proves neither a real tenant binding nor recipient reading.
"""
from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
import copy
import contextlib
import datetime
import fcntl
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import stat
import subprocess
import threading
import time
from urllib.parse import parse_qs, urlsplit
import uuid

import api_readonly_chain as readonly

ROOT = readonly.ROOT
MODULE = 'formal-directory-protocol'
MOCK_PORT = 18622
HOSTS = ('open.feishu.cn', 'mfa.testlab.invalid')
AUTH = '/open-apis/auth/v3/tenant_access_token/internal'
USERS = '/open-apis/contact/v3/users/find_by_department'
CHILDREN = '/open-apis/contact/v3/departments/0/children'
MESSAGES = '/open-apis/im/v1/messages'
SYNC_ACTION = 'v3.members.feishu_synced'
SENT_ACTION = 'v3.member.mfa_reminder.feishu_accepted'
TARGETS = ('POST /api/v1/members/sync', 'POST /api/v1/members/mfa-reminders')
REQUIRED_CASES = ('directory.proxy_ready', 'directory.sync.normal', 'directory.sync.faults',
    'directory.sync.stale', 'directory.sync.authorization', 'directory.reminders.normal',
    'directory.reminders.authority_faults', 'directory.reminders.response_loss',
    'directory.reminders.audit_commit_failure', 'directory.operation_linkage')
PROOF_SCOPE = 'Native dual V3 HTTP/PG with bounded HTTPS directory/MFA/bot protocol Mock; real tenant authority and notification delivery remain external TODO'
REMINDER_TEXT = ('AIOps Canvas: your account has not completed the required two-step verification. '
    'Please open the trusted product sign-in page and complete enrollment. Contact your administrator if you need help.')
check, Blocked = readonly.check, readonly.Blocked


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
        evidence_mode='protocol_mock', **fields), ensure_ascii=False), flush=True)


def stamp(delta=0):
    return (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=delta)).isoformat().replace('+00:00', 'Z')


def instant(value):
    return datetime.datetime.fromisoformat(value.replace('Z', '+00:00'))


def connect_host(target):
    if target not in tuple(host + ':443' for host in HOSTS):
        raise Blocked('CONNECT target outside exact two-host port-443 allowlist')
    return target[:-4]


def delivery_uuid(value):
    if not isinstance(value, str) or not re.fullmatch('[a-f0-9]{32}', value):
        raise Blocked('delivery ID is not a native lowercase 32-hex attempt')
    raw = bytearray.fromhex(value); raw[6] = (raw[6] & 15) | 64; raw[8] = (raw[8] & 63) | 128
    return str(uuid.UUID(bytes=bytes(raw)))


@contextlib.contextmanager
def exclusive(root):
    """Only this package's three fixed listeners share this private lease."""
    path = Path(root) / '.local/testlab/api-directory-protocol.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('directory lease must be private regular file')
        try: fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise Blocked('directory protocol already owns its fixed listeners') from None
        yield
    finally: os.close(fd)


class DirectoryProtocol:
    """Pure transport peer; never connects, resolves DNS, forwards, or follows URLs."""
    def __init__(self, nonce):
        if not re.fullmatch('[a-f0-9]{32}', nonce): raise Blocked('invalid protocol fixture nonce')
        self.nonce, self.binding = nonce, 'bind-' + nonce
        self.directory_app, self.bot_app = 'directory-' + nonce, 'bot-' + nonce
        self.directory_secret, self.bot_secret, self.mfa_token = (secrets.token_urlsafe(24) for _ in range(3))
        self.lock = threading.RLock()
        self.revision, self.directory_mode, self.authority_mode, self.bot_mode = 1, 'normal', 'normal', 'normal'
        self.tokens, self.events, self.authority_answers = {}, [], []
        self.messages, self.accepted, self.population, self.target = [], {}, set(), None
        self.next_gate, self.bot_gate, self.gates = None, None, []

    def gate_next(self):
        with self.lock:
            if self.next_gate: raise Blocked('directory gate already assigned')
            gate = (threading.Event(), threading.Event()); self.next_gate = gate; self.gates.append(gate); return gate

    def release_gates(self):
        for _, release in self.gates: release.set()

    def members(self, revision):
        return [dict(ExternalID=self.binding + ':ou_' + self.nonce + str(i),
            Email='directory-' + str(i) + '-' + self.nonce + '@testlab.invalid',
            Name='Synthetic directory ' + str(i) + ' r' + str(revision), DepartmentID='0', Department='') for i in (1, 2)]

    def dispatch(self, host, method, path, headers, body):
        if host not in HOSTS: return 403, {}, False
        address = urlsplit(path)
        if address.scheme or address.netloc or address.fragment: return 422, {}, False
        with self.lock:
            if host == 'mfa.testlab.invalid': return self.authority(method, path, headers, body)
            if method == 'POST' and path == AUTH:
                if body == dict(app_id=self.directory_app, app_secret=self.directory_secret):
                    token = secrets.token_urlsafe(24)
                    self.tokens[token] = dict(kind='directory', revision=self.revision, mode=self.directory_mode, gate=self.next_gate)
                    self.next_gate = None; self.events.append(dict(kind='directory_token', revision=self.revision))
                elif body == dict(app_id=self.bot_app, app_secret=self.bot_secret):
                    token = secrets.token_urlsafe(24); self.tokens[token] = dict(kind='bot')
                    self.events.append(dict(kind='bot_token'))
                else: return 401, {}, False
                return 200, dict(code=0, tenant_access_token=token, expire=3600), False
            auth = headers.get('Authorization', '')
            token = self.tokens.get(auth.removeprefix('Bearer ')) if auth.startswith('Bearer ') else None
            if token is None: return 401, {}, False
            if method == 'POST' and address.path == MESSAGES and token['kind'] == 'bot':
                return self.bot(address, body)
            if method != 'GET' or token['kind'] != 'directory': return 404, {}, False
            expected = dict(department_id_type=['open_department_id'], user_id_type=['open_id'], page_size=['50'])
            query = parse_qs(address.query, keep_blank_values=True)
            if address.path == CHILDREN:
                expected['fetch_child'] = ['false']
                if query != expected: return 422, {}, False
                self.events.append(dict(kind='children', revision=token['revision']))
                return 200, dict(code=0, data=dict(has_more=False, page_token='', items=[])), False
            if address.path != USERS: return 404, {}, False
            expected['department_id'] = ['0']; second = query.get('page_token') == ['page2']
            if second: expected['page_token'] = ['page2']
            if query != expected: return 422, {}, False
            gate = token['gate'] if not second else None
            row = self.members(token['revision'])[1 if second else 0]
            item = dict(open_id=row['ExternalID'].split(':')[1], name=row['Name'], email=row['Email'],
                status=dict(is_frozen=False, is_resigned=False, is_activated=True, is_exited=False, is_unjoin=False))
            answer = dict(code=0, data=dict(has_more=not second, page_token='' if second else 'page2', items=[item]))
            mode = token['mode']; self.events.append(dict(kind='users_page', page=2 if second else 1, revision=token['revision'], mode=mode))
        if gate:
            gate[0].set()
            if not gate[1].wait(8): return 504, {}, False
        if second:
            if mode == 'page_429': return 429, {}, False
            if mode == 'missing_status': del item['status']['is_exited']
            if mode == 'repeat_cursor': answer['data'].update(has_more=True, page_token='page2')
        return 200, answer, second and mode == 'response_loss'

    def authority(self, method, path, headers, body):
        if method != 'POST' or path != '/snapshot': return 404, {}, False
        if headers.get('Authorization') != 'Bearer ' + self.mfa_token: return 401, {}, False
        if not isinstance(body, dict) or set(body) != {'instance_id', 'tenant_binding_id', 'environment_id', 'user_ids'}:
            return 422, {}, False
        ids = body['user_ids']
        if (body['instance_id'] != 'testlab.local' or body['tenant_binding_id'] != self.binding
                or body['environment_id'] != 'test' or not isinstance(ids, list) or any(not isinstance(x, str) for x in ids)
                or len(ids) != len(set(ids))
                or set(ids) != self.population or self.target not in self.population): return 422, {}, False
        members = {x: dict(enrollment='enrolled', factor_kind='totp', feishu_open_id='') for x in ids}
        members[self.target] = dict(enrollment='unenrolled', factor_kind='none', feishu_open_id='ou_' + self.target)
        observed = stamp(-360 if self.authority_mode == 'stale' else 0)
        value = dict(instance_id='testlab.local', tenant_binding_id=self.binding, environment_id='test',
            snapshot=dict(complete=True, observed_at=observed, grace_days=7, members=members))
        self.events.append(dict(kind='authority', mode=self.authority_mode, target=self.target, population=sorted(ids)))
        if self.authority_mode == '429':
            self.authority_answers.append({}); return 429, {}, False
        if self.authority_mode == 'wrong_scope': value['environment_id'] = 'staging'
        if self.authority_mode == 'missing_member': del members[self.target]
        self.authority_answers.append(copy.deepcopy(value))
        return 200, value, self.authority_mode == 'response_loss'

    def bot(self, address, body):
        if parse_qs(address.query) != {'receive_id_type': ['open_id']} or not isinstance(body, dict): return 422, {}, False
        if set(body) != {'receive_id', 'msg_type', 'content', 'uuid'}: return 422, {}, False
        try: content = json.loads(body['content']); delivery = str(uuid.UUID(body['uuid']))
        except (ValueError, TypeError, AttributeError): return 422, {}, False
        if (body['receive_id'] != 'ou_' + str(self.target) or body['msg_type'] != 'text'
                or content != {'text': REMINDER_TEXT} or delivery != body['uuid']
                or uuid.UUID(delivery).version != 4): return 422, {}, False
        record = dict(uuid=delivery, receive_id=body['receive_id'], message_id='mock-message-' + delivery)
        self.messages.append(copy.deepcopy(record)); self.accepted.setdefault(delivery, record)
        if self.bot_gate:
            entered, release = self.bot_gate
            entered.set()
            # Do not hold the Mock state lock while another request reads MFA.
            # A competing prepare can honestly return pending before the sender
            # obtains the row lock. Hold only its external response for evidence.
            self.lock.release()
            try: release.wait(4)
            finally: self.lock.acquire()
        return 200, dict(code=0, data=dict(message_id=self.accepted[delivery]['message_id'])), self.bot_mode == 'response_loss'


class ConnectMock:
    """Terminates native verified TLS locally; zero forwarding capability."""
    def __init__(self, protocol, cert, key):
        self.protocol, self.cert, self.key = protocol, Path(cert), Path(key)
        self.server = self.thread = None
        self.connections, self.lock = set(), threading.Lock()

    def start(self):
        owner = self
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.load_cert_chain(self.cert, self.key)
        def sni(connection, name, _):
            if name not in HOSTS: raise ssl.SSLError('unexpected local test SNI')
        context.set_servername_callback(sni)
        class Inner(BaseHTTPRequestHandler):
            protocol_version = 'HTTP/1.1'
            def log_message(self, *args): pass
            def handle_one_request(self):
                self.connection.settimeout(4)
                super().handle_one_request()
            def answer(self):
                try:
                    host = self.server.tunnel_hosts[threading.get_ident()]
                    if self.headers.get('Host') not in (host, host + ':443') or self.headers.get('Transfer-Encoding'):
                        self.send_error(400); self.close_connection = True; return
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= length <= 65536: self.send_error(413); self.close_connection = True; return
                    raw = self.rfile.read(length)
                    if len(raw) != length: self.close_connection = True; return
                    if length and self.headers.get('Content-Type') not in ('application/json', 'application/json; charset=utf-8'):
                        self.send_error(400); self.close_connection = True; return
                    value = json.loads(raw) if raw else None
                    status, answer, lose = owner.protocol.dispatch(host, self.command, self.path, dict(self.headers), value)
                    if lose:
                        self.close_connection = True; self.connection.shutdown(socket.SHUT_RDWR); return
                    encoded = json.dumps(answer, separators=(',', ':')).encode()
                    self.send_response(status); self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(encoded))); self.end_headers(); self.wfile.write(encoded)
                except (ValueError, TypeError, OSError): self.close_connection = True
            do_GET = answer
            do_POST = answer
        class Outer(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_CONNECT(self):
                try: host = connect_host(self.path)
                except Blocked:
                    self.send_error(403); self.close_connection = True; return
                self.connection.settimeout(4)
                self.send_response(200); self.end_headers(); self.wfile.flush(); self.close_connection = True
                connection = None
                try:
                    connection = context.wrap_socket(self.connection, server_side=True)
                    with owner.lock: owner.connections.add(connection)
                    self.server.tunnel_hosts[threading.get_ident()] = host
                    Inner(connection, self.client_address, self.server)
                except OSError: pass
                finally:
                    self.server.tunnel_hosts.pop(threading.get_ident(), None)
                    if connection:
                        with owner.lock: owner.connections.discard(connection)
                        connection.close()
            def do_GET(self): self.send_error(403)
            do_POST = do_GET
        class Server(ThreadingHTTPServer):
            def get_request(self):
                connection, address = super().get_request(); connection.settimeout(4); return connection, address
        self.server = Server(('127.0.0.1', MOCK_PORT), Outer)
        self.server.daemon_threads = True; self.server.tunnel_hosts = {}
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': .1}, daemon=True)
        self.thread.start()

    def close(self):
        self.protocol.release_gates()
        if self.server:
            if self.thread and self.thread.is_alive(): self.server.shutdown()
            with self.lock:
                for connection in tuple(self.connections):
                    try: connection.shutdown(socket.SHUT_RDWR)
                    except OSError: pass
            self.server.server_close()
        if self.thread:
            self.thread.join(6); check(not self.thread.is_alive(), 'owned CONNECT listener leaked')
        deadline = time.monotonic() + 5
        while True:
            with self.lock: remaining = len(self.connections)
            if remaining == 0: break
            if time.monotonic() >= deadline: raise Blocked('owned TLS handler did not release its connection')
            time.sleep(.02)


def validate_delivery(row, receipt, audits, actor, target, unknown):
    check(row['actor_id'] == actor and row['target_id'] == target and row['environment_id'] == 'test', 'reminder actor/target/environment differs')
    check(isinstance(row['id'], str) and re.fullmatch('[a-f0-9]{32}', row['id']) is not None, 'native reminder attempt ID differs')
    check(receipt['uuid'] == delivery_uuid(row['id']) and receipt['receive_id'] == 'ou_' + target,
          'external UUID/recipient not linked to native attempt')
    if unknown:
        check(row['status'] in ('pending', 'uncertain') and row['feishu_message_id'] is None
              and row['accepted_at'] is None and audits == [], 'unknown external acceptance laundered into native success')
    else:
        check(row['status'] == 'accepted' and row['accepted_at'] is not None and row['feishu_message_id'] == receipt['message_id'],
              'accepted attempt and external receipt differ')
        check(len(audits) == 1, 'accepted operation must have exactly one linked audit')
        audit = audits[0]
        check({k: audit[k] for k in ('actor', 'target', 'action', 'outcome')} == dict(actor=actor, target=target, action=SENT_ACTION, outcome='success'),
              'accepted audit actor/target/action/outcome differs')
        changes = audit['changes']
        check(set(changes) == {'environment', 'attempt_id', 'feishu_message_id', 'mfa_observed_at'}
              and changes['environment'] == 'test' and changes['attempt_id'] == row['id']
              and changes['feishu_message_id'] == receipt['message_id'], 'accepted audit operation/receipt binding differs')
        try: same = instant(changes['mfa_observed_at']) == instant(row['mfa_observed_at'])
        except (ValueError, TypeError): same = False
        check(same, 'accepted audit evidence timestamp differs')


def concurrent_result(value):
    """Native pending rows are visible between prepare and the bounded send."""
    if isinstance(value, dict) and set(value) == {'sent'}:
        check(type(value['sent']) is int and value['sent'] in (0, 1), 'concurrent send count differs')
        return value['sent']
    check(isinstance(value, dict) and set(value) == {'error'} and isinstance(value['error'], dict)
          and value['error'].get('code') == 'mfa_reminder_uncertain', 'concurrent request did not return exact pending outcome')
    return None


def validate_fault_scope(scope, catalog, container_id):
    try: nonce = uuid.UUID(scope['run_id']).hex
    except (ValueError, KeyError, TypeError): raise Blocked('audit fault requires exact fresh run UUID') from None
    if (scope['nonce'] != nonce or scope['database'] != 'aiops_testlab_readonly_' + nonce
            or scope['db_user'] != 'readonly_' + nonce
            or scope['owner_tag'] != readonly.OWNER + ':' + scope['run_id'] + ':api-readonly'
            or not re.fullmatch('[a-f0-9]{64}', scope['container_id']) or container_id != scope['container_id']
            or catalog != dict(database_owner=scope['db_user'], database_tag=scope['owner_tag'], role_tag=scope['owner_tag'])):
        raise Blocked('audit failure DDL escaped fresh UUID/owner/container boundary')


def fault_ddl(nonce, actor, target):
    if any(not isinstance(v, str) or not re.fullmatch('[a-f0-9]{32}', v) for v in (nonce, actor, target)):
        raise Blocked('audit fault identifier/scope must be owned lowercase hex')
    function = readonly.identifier('testlab_reject_mfa_' + nonce)
    trigger = readonly.identifier('testlab_reject_mfa_' + nonce)
    return ('BEGIN; CREATE FUNCTION public.' + function + '() RETURNS trigger LANGUAGE plpgsql AS $testlab$ BEGIN '
        'IF NEW.actor_id=' + readonly.quote(actor) + ' AND NEW.target=' + readonly.quote(target) +
        ' AND NEW.action=' + readonly.quote(SENT_ACTION) + " THEN RAISE EXCEPTION 'testlab scoped audit rejection'; END IF; RETURN NEW; END $testlab$; "
        'CREATE TRIGGER ' + trigger + ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.' + function + '(); COMMIT;')


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.protocol = DirectoryProtocol(self.nonce)
        self.mock, self.protocol_files, self.fault_installed = None, {}, False
        self.call_lock = threading.Lock(); self.admin_id = None
        self.state.update(suite=MODULE, mock_port=MOCK_PORT, upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE,
            real_tenant_verified=False, audit_fault_scope='fresh UUID DB, reader actor, bootstrap admin target, accepted action only')
        readonly._private(self.work / 'state.json', self.state)

    def prepare_protocol(self):
        cert, key = self.work / 'directory-ca.pem', self.work / 'directory-key.pem'
        result = subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '2',
            '-subj', '/CN=open.feishu.cn', '-addext', 'subjectAltName=DNS:open.feishu.cn,DNS:mfa.testlab.invalid',
            '-keyout', str(key), '-out', str(cert)], capture_output=True, timeout=20,
            env=readonly.command_env({}, {}), umask=0o077)
        if result.returncode: raise Blocked('private directory TLS preparation failed')
        for filename, value in (('directory-secret', self.protocol.directory_secret), ('bot-secret', self.protocol.bot_secret), ('mfa-token', self.protocol.mfa_token)):
            readonly.private_file(self.work / filename, value)
        config = dict(tier='nonprod', instance_id='testlab.local', environment_id='test', tenant_binding_id=self.protocol.binding,
            directory=dict(app_id=self.protocol.directory_app, app_secret_file=str(self.work / 'directory-secret'), root_department_id='0', address_book_scope_approved=True),
            mfa_evidence=dict(url='https://mfa.testlab.invalid/snapshot', bearer_token_file=str(self.work / 'mfa-token')),
            mfa_reminders=dict(app_id=self.protocol.bot_app, app_secret_file=str(self.work / 'bot-secret'), tenant_scope_approved=True))
        readonly.private_file(self.work / 'member-sources.json', json.dumps(config))
        for path in (cert, key, *(self.work / x for x in ('directory-secret', 'bot-secret', 'mfa-token', 'member-sources.json'))):
            os.chmod(path, 0o600); self.protocol_files[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
        self.mock = ConnectMock(self.protocol, cert, key); self.mock.start()
        emit('directory.proxy_ready', 'passed', dict(bind='127.0.0.1:' + str(MOCK_PORT), allowed_connect=[x + ':443' for x in HOSTS],
            native_tls_validation=True, system_changes=False, forwarding=False, real_tenant=False))

    def frozen(self):
        super().frozen()
        for filename, digest in self.protocol_files.items():
            path = self.work / filename
            if (path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode) != 0o600
                    or hashlib.sha256(path.read_bytes()).hexdigest() != digest): raise Blocked('directory protected input changed')

    def startup_environment(self):
        self.frozen()
        if set(self.protocol_files) != {'directory-ca.pem', 'directory-key.pem', 'directory-secret', 'bot-secret', 'mfa-token', 'member-sources.json'}:
            raise Blocked('directory protected inputs not prepared')
        return dict(HTTPS_PROXY='http://127.0.0.1:' + str(MOCK_PORT), SSL_CERT_FILE=str(self.work / 'directory-ca.pem'),
                    AIOPS_V3_MEMBER_SOURCES_CONFIG=str(self.work / 'member-sources.json'))

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0, environment='test', validate=None):
        allowed = set(TARGETS) | {'POST /api/v1/auth/login', 'POST /api/v1/auth/logout', 'GET /api/v1/auth/me'}
        wire = readonly.SCENARIO_ALIASES.get(scenario, scenario)
        if (method + ' ' + path not in allowed or index not in (0, 1) or environment not in ('test', 'staging', 'all')
                or user not in (None, 'admin', *self.users) or wire not in readonly.WIRE_SCENARIOS):
            raise Blocked('directory HTTP route/actor/origin boundary mismatch')
        if isinstance(want, tuple) and (want != (200, 503) or path != '/api/v1/members/mfa-reminders'
                or getattr(validate, '__self__', None) is not self or getattr(validate, '__name__', None) != 'concurrent_validation'):
            raise Blocked('multiple outcomes only allowed for exact concurrent reminder contract')
        expected_status = list(want) if isinstance(want, tuple) else [want]
        self.frozen()
        with self.call_lock: self.calls += 1; number = self.calls
        canonical, route_class = self.contract.canonical(method, path)
        headers = dict(Origin=readonly.BASES[index], **{'X-AIOps-Environment': environment, 'Accept-Language': 'en'})
        if user in self.cookies: headers['Cookie'] = self.cookies[user]
        if body is not None: headers['Content-Type'] = 'application/json'
        row = dict(name='directory.http.' + str(number), method=method, path=canonical, route_class=route_class,
            scenario=wire, scenario_detail=scenario, expected=dict(status=expected_status, error_code=code), run_id=self.run_id,
            call_id=self.run_id + '-' + str(number), test_name='FormalDirectoryProtocolChain', evidence_mode='protocol_mock',
            upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE, registration_evidence=False)
        try:
            status, raw, response_headers = readonly.transport(readonly.BASES[index], method, path, json.dumps(body).encode() if body is not None else None, headers)
            value = json.loads(raw) if raw else {}; actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual'] = dict(status=status, error_code=actual_code)
            check(status in expected_status and (code is None or actual_code == code), 'directory HTTP status/error differs')
            if isinstance(want, tuple): check((status == 200 and actual_code is None) or (status == 503 and actual_code == 'mfa_reminder_uncertain'),
                'concurrent status must match exact native pending outcome')
            if 200 <= status < 300: self.contract.shape(method, path, status, value)
            if validate: validate(value)
            row.update(status='passed', registration_evidence=200 <= status < 300 and scenario == 'normal')
        except Exception as error:
            row.setdefault('actual', dict(status=None)); row.update(status='failed', registration_evidence=False)
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row)
            if 'raw' in locals(): readonly._private(self.work / ('failure-response-' + str(number) + '.json'), dict(status=status, body=raw.decode('utf-8', 'replace')))
            with self.call_lock: readonly.append_json(self.work / 'interfaces.jsonl', row)
            emit(row['name'], 'failed', row['actual'], **({'interface': row} if type(row['actual']['status']) is int else {})); raise
        with self.call_lock: readonly.append_json(self.work / 'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row); return value, response_headers

    def manager_grants(self, name, enabled=True):
        if name not in ('reader', 'peer'): raise Blocked('manager fixture outside two owned roles')
        role = readonly.quote(self.roles[name])
        if enabled:
            self.sql("INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(" + role + ",'members','manage','global','*'),(" + role + ",'environment','view','global','*') ON CONFLICT DO NOTHING")
        else:
            self.sql("DELETE FROM v3_role_grants WHERE role_id=" + role + " AND module='members' AND action='manage' AND scope_kind='global' AND scope_id='*'")

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('workspace',(SELECT json_build_object('timezone',timezone,'locale',language) FROM workspace_settings WHERE id='default'),"
            "'others',(SELECT COALESCE(json_agg(json_build_object('id',id,'email',email,'name',name,'disabled',disabled,'source',source,'external_id',external_id,'department',department,'kind',v3_identity_kind,'timezone',v3_timezone) ORDER BY id),'[]'::json) FROM users WHERE source<>'feishu'),"
            "'users',(SELECT COALESCE(json_agg(json_build_object("
            "'id',u.id,'email',u.email,'name',u.name,'department',u.department,'source',u.source,'external_id',u.external_id,"
            "'disabled',u.disabled,'role',u.role,'identity_kind',u.v3_identity_kind,'timezone',u.v3_timezone,"
            "'settings',row_to_json(s)) ORDER BY u.external_id),'[]'::json) FROM users u LEFT JOIN v3_user_settings s ON s.user_id=u.id WHERE u.source='feishu'),"
            "'sync',(SELECT COALESCE(json_agg(row_to_json(s) ORDER BY binding_id),'[]'::json) FROM v3_feishu_member_sync_state s),"
            "'attempts',(SELECT COALESCE(json_agg(row_to_json(a) ORDER BY id),'[]'::json) FROM v3_mfa_reminder_attempts a),"
            "'audit',(SELECT COALESCE(json_agg(json_build_object('id',id,'actor',actor_id,'target',target,'action',action,'outcome',outcome,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit))", read=True))

    def sync_check(self, before, revision, added, updated, actor):
        after = self.ledger(); expected = self.protocol.members(revision)
        check(len(after['users']) == 2 and len(after['sync']) == 1 and after['attempts'] == before['attempts'], 'sync native counts differ or altered reminder ledger')
        check(after['workspace'] == before['workspace'] and after['others'] == before['others'], 'sync modified workspace or non-directory identities')
        prior_ids = {x['external_id']: x['id'] for x in before['users']}
        for actual, row in zip(after['users'], expected):
            check(actual['external_id'] not in prior_ids or actual['id'] == prior_ids[actual['external_id']], 'sync replaced stable native identity ID')
            check(all(actual[k] == row[v] for k, v in (('email', 'Email'), ('name', 'Name'), ('external_id', 'ExternalID'), ('department', 'Department')))
                and actual['disabled'] is True and actual['source'] == 'feishu' and actual['role'] == 'viewer'
                and actual['identity_kind'] == 'human' and actual['timezone'] == after['workspace']['timezone']
                and actual['settings']['user_id'] == actual['id'] and actual['settings']['locale'] == after['workspace']['locale'],
                'native imported identity/defaults/binding differ')
        fingerprint = hashlib.sha256(json.dumps(expected, ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()
        state = after['sync'][0]
        check(state['binding_id'] == self.protocol.binding and state['app_id_hash'] == hashlib.sha256(self.protocol.directory_app.encode()).hexdigest()
            and state['root_department_id'] == '0' and state['last_fingerprint'] == fingerprint and state['last_observed'] == 2,
            'native sync state does not bind full accepted snapshot')
        check(after['audit'][:-1] == before['audit'], 'sync changed prior audit evidence')
        audit = after['audit'][-1]
        check({k: audit[k] for k in ('actor', 'target', 'action', 'outcome', 'changes')} == dict(actor=self.users[actor], target='test', action=SYNC_ACTION,
            outcome='success', changes=dict(source='feishu', binding_id=self.protocol.binding, environment='test', root_department='0', observed=2,
                added=added, updated=updated, fingerprint=fingerprint)), 'sync audit actor/scope/fingerprint/result binding differs')
        return after

    def sync(self, actor='reader', index=0, want=200, code=None, scenario='normal', result=None):
        return self.call(actor, 'POST', '/api/v1/members/sync', index=index, want=want, code=code, scenario=scenario,
            validate=(lambda value: check(value == result, 'sync added/updated result differs')) if result is not None else None)

    def sync_cases(self):
        before = self.ledger()
        for revision, added, updated, actor, index in ((1, 2, 0, 'reader', 0), (2, 0, 2, 'peer', 1), (2, 0, 0, 'reader', 1)):
            self.protocol.revision = revision
            self.sync(actor, index, result=dict(added=added, updated=updated)); before = self.sync_check(before, revision, added, updated, actor)
        emit('directory.sync.normal', 'passed', dict(pages=2, identities=2, disabled=True, updated_without_duplication=True, native_audit=True))
        for mode in ('page_429', 'response_loss', 'missing_status', 'repeat_cursor'):
            self.protocol.directory_mode = mode
            self.sync(want=503, code='member_sync_unavailable', scenario='failure')
            check(self.ledger() == before, 'incomplete pagination committed partial identities/state/audit')
        self.protocol.directory_mode = 'normal'
        emit('directory.sync.faults', 'passed', dict(faults=['page_429', 'response_loss', 'missing_status', 'repeat_cursor'], partial_commit=False))
        gate = self.protocol.gate_next(); self.protocol.revision = 3
        with ThreadPoolExecutor(max_workers=1) as pool:
            future = pool.submit(self.sync, 'reader', 0, 409, 'member_sync_stale', 'failure')
            try:
                check(gate[0].wait(4), 'old directory read did not reach controlled gate')
                self.protocol.revision = 4
                self.sync('peer', 1, result=dict(added=0, updated=2)); before = self.sync_check(before, 4, 0, 2, 'peer')
            finally: gate[1].set()
            future.result(timeout=15)
        check(self.ledger() == before, 'late old snapshot replaced newer committed state')
        emit('directory.sync.stale', 'passed', dict(old_revision=3, newer_revision=4, stale_status=409, newer_preserved=True))
        for mutation in ('logout', 'revoke'):
            gate = self.protocol.gate_next()
            with ThreadPoolExecutor(max_workers=1) as pool:
                future = pool.submit(self.sync, 'reader', 0, 401 if mutation == 'logout' else 403,
                                     'unauthorized' if mutation == 'logout' else 'forbidden', 'authorization')
                try:
                    check(gate[0].wait(4), 'authorization race did not reach external-read gate')
                    if mutation == 'logout': self.call('reader', 'POST', '/api/v1/auth/logout', want=204, scenario='fixture', index=1)
                    else: self.manager_grants('reader', False)
                finally: gate[1].set()
                future.result(timeout=15)
            check(self.ledger() == before, 'revoked caller committed directory identities/audit')
            if mutation == 'logout': self.login('reader')
            else: self.manager_grants('reader')
        for user, want, code in ((None, 401, 'unauthorized'), ('denied', 403, 'forbidden')):
            count = len(self.protocol.events); self.sync(user, want=want, code=code, scenario='authorization')
            check(len(self.protocol.events) == count and self.ledger() == before, 'early-denied sync reached source or wrote local data')
        self.call('reader', 'POST', '/api/v1/members/sync', {}, want=422, code='invalid_body', scenario='boundary')
        emit('directory.sync.authorization', 'passed', dict(session_revoked=401, grant_revoked=403, early_denied_no_source=True, no_failed_commit=True))

    def reminder(self, index=0, want=200, code=None, sent=None, user='reader', scenario='normal'):
        return self.call(user, 'POST', '/api/v1/members/mfa-reminders', want=want, code=code, index=index, scenario=scenario,
            validate=(lambda value: check(value == {'sent': sent}, 'reminder count differs')) if sent is not None else None)

    def concurrent_validation(self, value):
        result = concurrent_result(value)
        if result is None:
            observed_at = stamp(); ledger = self.ledger(); target = self.users['peer']
            rows = [x for x in ledger['attempts'] if x['target_id'] == target]
            check(len(rows) == 1 and rows[0]['status'] in ('pending', 'uncertain')
                and rows[0]['actor_id'] == self.users['reader'] and rows[0]['environment_id'] == 'test'
                and rows[0]['accepted_at'] is None and rows[0]['feishu_message_id'] is None,
                'competing 503 lacks immediate native pending/uncertain proof')
            with self.protocol.lock:
                messages = copy.deepcopy([x for x in self.protocol.messages if x['receive_id'] == 'ou_' + target])
            check(len(messages) <= 1 and all(x['uuid'] == delivery_uuid(rows[0]['id']) for x in messages),
                'competing pending outcome has duplicate/unrelated external request')
            readonly.private_file(self.work / 'concurrency-pending-proof.json', json.dumps(dict(observed_at=observed_at,
                http_status=503, error_code='mfa_reminder_uncertain', native_attempt=rows[0], mock_messages=messages,
                evidence_mode='protocol_mock', no_retry=True)))
            if self.protocol.bot_gate: self.protocol.bot_gate[1].set()
        return result

    def delivery_check(self, before, target, unknown=False):
        after = self.ledger()
        check(all(after[k] == before[k] for k in ('users', 'sync', 'others', 'workspace')), 'reminder mutated directory/workspace/native identities')
        old = {x['id']: x for x in before['attempts']}; new = [x for x in after['attempts'] if x['id'] not in old]
        check(len(new) == 1 and all(x == old[x['id']] for x in after['attempts'] if x['id'] in old), 'reminder attempts changed prior receipt or did not add exactly one')
        check(after['audit'][:len(before['audit'])] == before['audit'], 'reminder changed existing audit evidence')
        audits = after['audit'][len(before['audit']):]; row = new[0]
        with self.protocol.lock:
            receipt = self.protocol.accepted.get(delivery_uuid(row['id']))
            check(receipt is not None, 'native attempt has no matching actually accepted Mock message')
            check(sum(x['uuid'] == receipt['uuid'] for x in self.protocol.messages) == 1, 'native attempt issued duplicate external message requests')
            observations = [x['snapshot']['observed_at'] for x in self.protocol.authority_answers
                if x.get('snapshot', {}).get('members', {}).get(target, {}).get('enrollment') == 'unenrolled']
        check(any(instant(x) == instant(row['mfa_observed_at']) for x in observations), 'native attempt evidence time not supplied by full scoped authority')
        validate_delivery(row, receipt, audits, self.users['reader'], target, unknown)
        readonly._private(self.work / ('native-delivery-' + row['id'] + '.json'), dict(run_id=self.run_id,
            native_database=self.database, attempt_id=row['id'], actor_id=self.users['reader'], target_id=target,
            http_evidence_file='interfaces.jsonl', completed_call_sequence=self.calls,
            native_attempt=row,
            native_audit=audits, external_receipt=receipt, authority_observed_at_matches=True,
            prior_attempts_and_audit_unchanged=True, outcome='unknown' if unknown else 'accepted',
            evidence_mode='protocol_mock', proof_scope=PROOF_SCOPE, sql_business_writes=False))
        return after

    def fault_guard(self):
        info = readonly.guard_pg(readonly.inspect_pg(self.data['postgres_container']), readonly.OWNER, self.data['postgres_image_id'])
        catalog = json.loads(self.pg("SELECT json_build_object('database_owner',(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=" + readonly.quote(self.database)
            + "),'database_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=" + readonly.quote(self.database)
            + "),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=" + readonly.quote(self.db_user) + '))'))
        scope = dict(run_id=self.run_id, nonce=self.nonce, database=self.database, db_user=self.db_user,
            owner_tag=self.owner_tag, container_id=self.data['postgres_container_id'])
        validate_fault_scope(scope, catalog, info.get('Id'))
        return scope

    def install_fault(self):
        self.frozen(); scope = self.fault_guard()
        if self.admin_id not in self.protocol.population: raise Blocked('audit rejection target is not native bootstrap identity')
        ddl = fault_ddl(self.nonce, self.users['reader'], self.admin_id)
        self.fault_installed = True  # cleanup also covers a lost DDL response
        readonly._private(self.work / 'audit-fault-proof.json', dict(scope=scope, actor=self.users['reader'], target=self.admin_id,
            action=SENT_ACTION, ddl=ddl, writes_business_results=False, real_organization=False))
        self.sql(ddl)
        count = self.sql("SELECT count(*) FROM pg_trigger WHERE tgname=" + readonly.quote('testlab_reject_mfa_' + self.nonce)
            + " AND tgrelid='public.management_audit'::regclass AND NOT tgisinternal", read=True)
        check(count.strip() == '1', 'scoped rejection trigger was not installed exactly once')

    def remove_fault(self):
        if not self.fault_installed: return
        self.fault_guard()
        name = readonly.identifier('testlab_reject_mfa_' + self.nonce)
        self.sql('BEGIN; DROP TRIGGER IF EXISTS ' + name + ' ON public.management_audit; DROP FUNCTION IF EXISTS public.' + name + '(); COMMIT;')
        self.fault_installed = False

    def reminder_cases(self):
        self.protocol.population = {self.admin_id, *(self.users[x] for x in ('reader', 'peer', 'denied'))}
        before = self.ledger(); self.protocol.target = self.users['reader']
        self.reminder(sent=1); before = self.delivery_check(before, self.protocol.target)
        count = len(self.protocol.messages); self.reminder(index=1, sent=0)
        check(self.ledger() == before and len(self.protocol.messages) == count, 'accepted reminder resent on other API')
        self.protocol.target = self.users['peer']
        gate = (threading.Event(), threading.Event()); self.protocol.bot_gate = gate; self.protocol.gates.append(gate)
        try:
            with ThreadPoolExecutor(max_workers=2) as pool:
                results = list(pool.map(lambda index: self.call('reader', 'POST', '/api/v1/members/mfa-reminders', index=index,
                    want=(200, 503), validate=self.concurrent_validation), (0, 1)))
        finally: gate[1].set(); self.protocol.bot_gate = None
        outcomes = [concurrent_result(value) for value, _ in results]
        check(outcomes.count(1) == 1 and all(x in (0, 1, None) for x in outcomes), 'two-instance first send did not serialize')
        before = self.delivery_check(before, self.protocol.target)
        count = len(self.protocol.messages); self.reminder(index=1, sent=0)
        check(self.ledger() == before and len(self.protocol.messages) == count, 'completed concurrent send retried on follow-up')
        emit('directory.reminders.normal', 'passed', dict(single_send=True, cross_instance_idempotency=True, concurrent_sent=outcomes,
            pending_response='503 uncertainty allowed only while competing native pending send', completed_followup_sent=0, recipient_read_verified=False))
        self.protocol.target = self.users['denied']
        for mode in ('429', 'response_loss', 'wrong_scope', 'missing_member', 'stale'):
            self.protocol.authority_mode = mode; count = len(self.protocol.messages)
            self.reminder(want=503, code='mfa_reminder_unavailable', scenario='failure')
            check(self.ledger() == before and len(self.protocol.messages) == count, 'invalid authority caused partial attempt/send/audit')
        self.protocol.authority_mode = 'normal'
        for user, want, code in ((None, 401, 'unauthorized'), ('denied', 403, 'forbidden')):
            count = len(self.protocol.events)
            self.reminder(user=user, want=want, code=code, scenario='authorization')
            check(len(self.protocol.events) == count and self.ledger() == before, 'unauthorized reminder reached authority or sender')
        self.call('reader', 'POST', '/api/v1/members/mfa-reminders', {}, want=422, code='invalid_request', scenario='boundary')
        emit('directory.reminders.authority_faults', 'passed', dict(faults=['429', 'response_loss', 'wrong_scope', 'missing_member', 'stale'], sends=0, local_changes=0))
        self.protocol.bot_mode = 'response_loss'
        self.reminder(want=503, code='mfa_reminder_uncertain', scenario='failure'); before = self.delivery_check(before, self.protocol.target, True)
        self.protocol.bot_mode = 'normal'; count = len(self.protocol.messages)
        self.reminder(index=1, want=503, code='mfa_reminder_uncertain', scenario='failure')
        check(self.ledger() == before and len(self.protocol.messages) == count, 'unknown accepted message retried after response loss')
        emit('directory.reminders.response_loss', 'passed', dict(external_accepted=True, native_accepted=False, retries=0, recovery='requires manual authoritative reconciliation'))
        self.protocol.target = self.admin_id
        try:
            self.install_fault()
            self.reminder(want=503, code='mfa_reminder_uncertain', scenario='failure'); before = self.delivery_check(before, self.admin_id, True)
        finally: self.remove_fault()
        count = len(self.protocol.messages)
        self.reminder(index=1, want=503, code='mfa_reminder_uncertain', scenario='failure')
        check(self.ledger() == before and len(self.protocol.messages) == count, 'audit rollback unknown delivery retried or became accepted')
        emit('directory.reminders.audit_commit_failure', 'passed', dict(external_accepted=True, local_transaction_rolled_back=True,
            native_status=[x['status'] for x in before['attempts'] if x['target_id'] == self.admin_id][0], accepted_audits=0, retries=0, scoped_trigger_removed=True))
        final = self.ledger()
        check(len(final['attempts']) == 4 and len(self.protocol.messages) == 4 and len(self.protocol.accepted) == 4
            and sum(x['status'] == 'accepted' for x in final['attempts']) == 2
            and sum(x['status'] in ('pending', 'uncertain') for x in final['attempts']) == 2
            and {delivery_uuid(x['id']) for x in final['attempts']} == set(self.protocol.accepted),
            'combined native/external attempt population is not exact')
        emit('directory.operation_linkage', 'passed', dict(external_acceptances=4, native_accepted=2, native_unknown=2, exact_actor_target_attempt_receipt_audit=True))

    def run(self):
        self.provision(); self.prepare_protocol(); self.start(); self.seed()
        for name in ('reader', 'peer'): self.manager_grants(name)
        for name in ('admin', 'reader', 'peer', 'denied'): self.login(name)
        me, _ = self.call('admin', 'GET', '/api/v1/auth/me', scenario='fixture')
        self.admin_id = me['user']['id']; check(re.fullmatch('[a-f0-9]{32}', self.admin_id) is not None, 'native bootstrap account ID differs')
        self.sync_cases(); self.reminder_cases(); self.frozen()
        readonly._private(self.work / 'mock-acceptances.json', dict(events=self.protocol.events, messages=self.protocol.messages, accepted=self.protocol.accepted,
            authority_answers=self.protocol.authority_answers, evidence_mode='protocol_mock', proof_scope=PROOF_SCOPE))
        result = dict(run_id=self.run_id, targets=list(TARGETS), http_results=self.calls, proof_scope=PROOF_SCOPE,
            real_tenant_verified=False, remaining_todo=['Real Feishu tenant directory permissions and stability',
            'Real complete MFA authority and verified recipient bindings', 'Real notification acceptance/read delivery',
            'Manual authoritative reconciliation of unknown sends; no public reconciler implemented'])
        readonly._private(self.work / 'directory-result.json', result); return result

    def cleanup(self):
        self.protocol.release_gates(); error = None
        try:
            if self.fault_installed: self.remove_fault()
        except BaseException as failure: error = failure
        try:
            if self.mock: self.mock.close()
        except BaseException as failure: error = error or failure
        try:
            readonly._private(self.work / 'mock-acceptances.json', dict(events=self.protocol.events, messages=self.protocol.messages,
                accepted=self.protocol.accepted, authority_answers=self.protocol.authority_answers,
                evidence_mode='protocol_mock', proof_scope=PROOF_SCOPE))
        except BaseException as failure: error = error or failure
        super().cleanup()
        if error: raise error


def main():
    parser = argparse.ArgumentParser(description=__doc__); parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true'); args = parser.parse_args()
    if not args.live: parser.error('root-authorized --live required')
    try:
        with exclusive(args.root), readonly.cooperative_signals():
            data = readonly.doctor(args.root)
            info = readonly.guard_pg(readonly.inspect_pg(data['postgres_container']), readonly.OWNER, data['postgres_image_id'])
            if not re.fullmatch('[a-f0-9]{64}', info.get('Id', '')): raise Blocked('owned PG exact container ID missing')
            data = dict(data, postgres_container_id=info['Id'])
            with socket.socket() as probe:
                probe.settimeout(.3)
                if probe.connect_ex(('127.0.0.1', MOCK_PORT)) == 0: raise Blocked('owned CONNECT Mock port occupied')
            readonly.execute(Chain(args.root, data, str(uuid.uuid4())))
    except Blocked as error: emit('directory.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error: emit('directory.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    return 0


if __name__ == '__main__': raise SystemExit(main())
