"""WAN-160: bounded formal management HTTP acceptance in a new owned PG DB.

Offline preparation is safe by default (--inventory). Actual execution requires
--live and a fresh controller nonce/HMAC receipt. SQL prepares synthetic users
and least-privilege role inputs only. Public HTTP produces all business writes
and audit evidence. Authority-dependent profile reads/writes remain honest 503.
Safety routines are an independent snapshot; no mutable readonly-chain import.
"""
from __future__ import annotations
import argparse
import contextlib
import copy
import fcntl
import hashlib
import hmac
import http.client
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import stat
import subprocess
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from urllib.parse import urlencode, urlsplit
from backend import control_proof
from env import PG_IMAGE, _private, _process_port
from faults import Blocked, private_json, validate_fixture, validate_identity
from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
PORTS = (18606, 18607)
BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)
CONTROL = 'http://127.0.0.1:18560'
LIMIT = 2 << 20
MODULE = 'formal-api-management'
WIRE_SCENARIOS = {'normal', 'authorization', 'boundary', 'idempotency', 'failure'}
TARGETS = (
 ('POST', '/services'), ('GET', '/services/{id}/peek'),
 ('GET', '/workspace'), ('PUT', '/workspace'),
 ('GET', '/roles/{id}'), ('PUT', '/roles/{id}'),
 ('PATCH', '/me/preferences'),
 ('GET', '/me/notification-settings'), ('PUT', '/me/notification-settings'),
 ('GET', '/me/ai-preferences'), ('PUT', '/me/ai-preferences'),
 ('DELETE', '/me/sessions/{id}'),
 ('GET', '/me/profile'), ('PUT', '/me/profile'),
 ('GET', '/me/watched-services'), ('PUT', '/me/watched-services/{id}'),
 ('DELETE', '/me/watched-services/{id}'),
)
SUPPORT = (('POST', '/auth/login'), ('GET', '/audit'), ('GET', '/audit/{id}'))
MISSING = {
 '/me/profile': 'No configured authoritative HTTPS contacts/MFA/all-active-session device and login-method source; GET and PUT only prove 503 fail-closed.',
 '/services': 'Complete service list/detail need current scoped metrics, alert coverage and registered dependencies; manual registration and partial peek do not prove these views.',
 '/me': 'Authoritative HTTPS on-call source is unavailable; preferences persist independently.',
 'subscriptions': 'Fresh default has no subscription definitions; existing subscription toggles need a trusted producer, not SQL-fabricated business data.',
}

class SQLTransportTimeout(Blocked):
    """A Docker/psql transport deadline does not prove a DDL outcome."""



def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                          evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)



def check(condition, message):
    if not condition:
        raise AssertionError(message)



def quote(value):
    return "'" + str(value).replace("'", "''") + "'"



def identifier(value):
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', value):
        raise Blocked('noncanonical owned PostgreSQL identifier')
    return '"' + value + '"'



def inspect_pg(container, timeout=5):
    result = subprocess.run(['docker', 'inspect', container], capture_output=True,
                            text=True, timeout=timeout)
    if result.returncode:
        raise Blocked('dedicated PG cannot be inspected; refusing database operation')
    records = json.loads(result.stdout)
    if len(records) != 1:
        raise Blocked('ambiguous dedicated PG inspection')
    return records[0]



def binary_build_info(binary):
    result = subprocess.run(['go', 'version', '-m', str(binary)], capture_output=True,
                            text=True, timeout=8, env=command_env({}, {}))
    if result.returncode or not re.search(r'(?m)^\s*path\s+github\.com/wans/aiops-canvas/cmd/aiops\s*$', result.stdout):
        raise Blocked('formal binary Go build metadata is unavailable or has a different entry point')
    if re.search(r'(?m)^\s*build\s+-race=true\s*$', result.stdout):
        raise Blocked('management acceptance requires the formal nonrace binary')
    return {'entry': 'github.com/wans/aiops-canvas/cmd/aiops', 'race_enabled': False}



def guard_pg(info, owner, image_id=None):
    if owner != OWNER or not isinstance(info, dict):
        raise Blocked('explicit testlab owner required')
    expected = '/aiops-' + owner + '-pg'
    config = info.get('Config', {})
    if (info.get('Name') != expected or config.get('Image') != PG_IMAGE or
            config.get('Labels', {}).get('aiops.testlab.owner') != owner or
            not info.get('State', {}).get('Running') or
            info.get('NetworkSettings', {}).get('Ports', {}).get('5432/tcp') !=
            [{'HostIp': '127.0.0.1', 'HostPort': '15461'}] or
            not str(info.get('Image', '')).startswith('sha256:') or
            (image_id is not None and info['Image'] != image_id)):
        raise Blocked('PG name/image/owner/exact loopback port identity mismatch')
    return info



def signed_identity(data, nonce, receipt):
    if not isinstance(receipt, dict) or set(receipt) != {'nonce', 'identity', 'proof'}:
        raise Blocked('signed controller receipt shape unavailable')
    if (receipt['nonce'] != nonce or not re.fullmatch(r'[a-f0-9]{32}', nonce) or
            not isinstance(receipt['identity'], dict) or not isinstance(receipt['proof'], str) or
            not hmac.compare_digest(receipt['proof'], control_proof(data['control_secret'], nonce, receipt['identity']))):
        raise Blocked('controller nonce/HMAC identity mismatch')
    validate_identity(data, receipt['identity'])
    return receipt['identity']



def transport(base, method, path, payload=None, headers=None, timeout=12):
    parsed = urlsplit(path)
    if (base not in (*BASES, CONTROL) or not path.startswith('/') or path.startswith('//') or
            parsed.scheme or parsed.netloc or parsed.fragment or '://' in path or
            '\\' in path or any(ord(c) < 32 for c in path)):
        raise Blocked('request outside invocation-owned local origins')
    if base == CONTROL and (method != 'GET' or parsed.path != '/attest'):
        raise Blocked('controller access is limited to a read-only ownership challenge')
    target = urlsplit(base)
    connection = http.client.HTTPConnection(target.hostname, target.port, timeout=timeout)
    try:
        connection.request(method, path, payload, headers or {})
        response = connection.getresponse()
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise AssertionError('formal response exceeded bounded evidence limit')
        return response.status, raw, dict(response.getheaders())
    finally:
        connection.close()



def private_file(path, body):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())



@contextlib.contextmanager
def exclusive(root):
    path = Path(root) / '.local/testlab/api-management.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private regular management lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another management chain owns the fixed ports') from None
        yield
    finally:
        os.close(fd)



def doctor(root):
    root = Path(root).resolve()
    data = private_json(root / '.local/testlab/backend-fixture.json')
    environment = private_json(root / '.local/testlab/environment.json')
    validate_fixture(root, environment, data)
    if data['owner'] != OWNER:
        raise Blocked('this work package is restricted to its explicitly authorized owner')
    current_source = source_fingerprint(root)
    if current_source in ('unknown', '') or current_source != data['source_tree_sha']:
        raise Blocked('formal binary source fingerprint is unknown or changed')
    # Never use existing system accounts, passwords, app data, or runtime env.
    data = {k: v for k, v in data.items() if k != 'admin'}
    binary = Path(data['binary'])
    if binary.is_symlink() or not binary.is_file():
        raise Blocked('owned regular formal binary required')
    build = binary_build_info(binary)
    info = guard_pg(inspect_pg(data['postgres_container']), data['owner'])
    addresses = {'127.0.0.1', '::1'}
    for network in info.get('NetworkSettings', {}).get('Networks', {}).values():
        gateway = network.get('Gateway')
        if gateway:
            try:
                addresses.add(str(ipaddress.ip_address(gateway)))
            except ValueError:
                raise Blocked('owned PG network gateway identity is invalid') from None
    nonce = secrets.token_hex(16)
    status, raw, _ = transport(CONTROL, 'GET', '/attest?nonce=' + nonce, timeout=3)
    if status != 200:
        raise Blocked('read-only controller attestation unavailable')
    current = signed_identity(data, nonce, json.loads(raw))
    for port, pid in zip((18560, 18561, 18562), (current['pid'], *current['api_pids'])):
        if not _process_port(pid, port):
            raise Blocked('signed controller/API PID does not own its registered listener')
    for port in PORTS:
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1', port)) == 0:
                raise Blocked('dedicated management port is occupied: ' + str(port))
    return dict(data, postgres_image_id=info['Image'],
                binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(), build=build,
                postgres_client_addresses=sorted(addresses))


class Contract:
    """Required fields, JSON types, enums and nested references; not full schema validation."""
    def __init__(self, root):
        self.api = json.loads((Path(root) / 'internal/apicontract/openapi.json').read_text())
        self.main = set()
        for line in (Path(root) / 'docs/api/v3-contract.md').read_text().splitlines():
            match = re.match(r'^\| (GET|POST|PUT|PATCH|DELETE) \| `([^`]+)` \|', line)
            if match:
                self.main.add(match.groups())
        if len(self.main) != 166:
            raise Blocked('expected current 166-route main contract')
        for method, path in (*TARGETS, *SUPPORT):
            if method.lower() not in self.api['paths'].get(path, {}):
                raise Blocked('management inventory is absent from the current public contract')

    def canonical(self, method, path):
        raw = urlsplit(path).path
        if not raw.startswith('/api/v1/'):
            raise Blocked('business HTTP must use the public API prefix')
        short = raw[len('/api/v1'):]
        matches = []
        for pattern, operations in self.api['paths'].items():
            if method.lower() in operations and re.fullmatch(
                    re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(pattern)), short):
                matches.append(pattern)
        canonical = short if short in matches else matches[0] if len(matches) == 1 else None
        if canonical is None or (method, canonical) not in (*TARGETS, *SUPPORT):
            raise Blocked('HTTP operation is outside the bounded management inventory')
        return '/api/v1' + canonical, 'main' if (method, canonical) in self.main else 'auxiliary_or_historical'

    def shape(self, method, path, status, value):
        canonical, _ = self.canonical(method, path)
        responses = self.api['paths'][canonical[len('/api/v1'):]][method.lower()]['responses']
        check(str(status) in responses, 'success status is not declared by the exact operation')
        schema = responses[str(status)].get('content', {}).get('application/json', {}).get('schema')
        if status == 204:
            check(value is None, '204 response must have an empty body')
        elif schema:
            self.validate(schema, value)

    def validate(self, schema, value, depth=0):
        check(depth < 40, 'bounded DTO recursion exceeded')
        if '$ref' in schema:
            schema = self.api['components']['schemas'][schema['$ref'].rsplit('/', 1)[-1]]
        if value is None and schema.get('nullable'):
            return
        for branch in schema.get('allOf', []):
            self.validate(branch, value, depth + 1)
        for keyword in ('anyOf', 'oneOf'):
            if keyword in schema:
                successes = 0
                for branch in schema[keyword]:
                    try:
                        self.validate(branch, value, depth + 1)
                        successes += 1
                    except AssertionError:
                        pass
                check(successes >= 1 if keyword == 'anyOf' else successes == 1,
                      'DTO alternatives do not match')
        kind = schema.get('type')
        types = {'object': isinstance(value, dict), 'array': isinstance(value, list),
                 'string': isinstance(value, str), 'boolean': type(value) is bool,
                 'integer': type(value) is int, 'number': type(value) in (int, float),
                 'null': value is None}
        if isinstance(kind, list):
            check(any(types.get(k, False) for k in kind), 'DTO JSON type differs')
        elif kind:
            check(types.get(kind, False), 'DTO JSON type differs: ' + kind)
        if 'enum' in schema:
            check(value in schema['enum'], 'DTO enum differs')
        if 'const' in schema:
            check(value == schema['const'], 'DTO constant differs')
        if isinstance(value, dict):
            check(set(schema.get('required', [])) <= set(value), 'DTO required fields are missing')
            props = schema.get('properties', {})
            if schema.get('additionalProperties') is False:
                check(set(value) <= set(props), 'DTO has undeclared fields')
            for key, item in value.items():
                rule = props.get(key, schema.get('additionalProperties'))
                if isinstance(rule, dict):
                    self.validate(rule, item, depth + 1)
        if isinstance(value, list) and isinstance(schema.get('items'), dict):
            for item in value:
                self.validate(schema['items'], item, depth + 1)


def header(headers, name):
    return next((value for key, value in headers.items() if key.lower() == name.lower()), '')


def revision(headers, name, quoted=False):
    value = header(headers, name)
    pattern = r'"[1-9][0-9]*"' if quoted else r'[1-9][0-9]*'
    check(isinstance(value, str) and re.fullmatch(pattern, value), 'canonical positive revision header required')
    return int(value.strip('"'))


def concurrent_pair(function):
    """Two different formal instances start their requests from one client barrier."""
    barrier = threading.Barrier(2, timeout=5)
    def invoke(index):
        barrier.wait()
        return function(index)
    with ThreadPoolExecutor(max_workers=2) as pool:
        pending = [pool.submit(invoke, index) for index in range(2)]
        return [future.result(timeout=25) for future in pending]


class Chain:
    def __init__(self, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != OWNER:
            raise Blocked('canonical run UUID and explicitly authorized owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database = 'aiops_testlab_management_' + self.nonce
        self.db_user = 'management_' + self.nonce
        self.marker = 'managementfixture' + self.nonce
        self.owner_tag = OWNER + ':' + run_id + ':api-management'
        self.work = self.root / '.local/testlab/environments' / OWNER / 'api-management' / run_id
        if self.work.resolve() != self.work:
            raise Blocked('invocation evidence path traverses a symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid5(uuid.UUID(run_id), name).hex for name in ('reader', 'peer', 'hidden', 'denied', 'target')}
        self.roles = {name: 'management-' + self.nonce + '-' + name for name in self.users}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.lock = threading.RLock()
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, binary_sha256=data['binary_sha256'],
                          source_tree_sha=data['source_tree_sha'], ports=list(PORTS), pids=[],
                          synthetic_input_scope='fresh users and least-privilege grants only',
                          actual_results_source='public service/role/workspace/preference/session writes and their transactional audits',
                          model_scope='configured synthetic model; no inference/probe/embedding call')
        _private(self.work / 'state.json', self.state)
        _private(self.work / 'credentials.json', dict(database_password=self.db_password, password=self.password))
        self.contract = Contract(self.root)
        self.roles['machine'] = 'management-' + self.nonce + '-machine'


    def frozen(self):
        current_source = source_fingerprint(self.root)
        if (current_source in ('unknown', '') or current_source != self.data['source_tree_sha'] or
                Path(self.data['binary']).is_symlink() or
                hashlib.sha256(Path(self.data['binary']).read_bytes()).hexdigest() != self.data['binary_sha256']):
            raise Blocked('formal source/binary changed during management chain')

    def pg_application(self, kind, index=None):
        if kind == 'api' and index in (0, 1):
            return 'management-api-' + self.nonce + '-' + str(index)
        if kind in ('sql', 'admin') and index is None:
            return 'management-' + kind + '-' + self.nonce
        raise Blocked('unknown owned PostgreSQL application identity')

    def connection_origin(self, row):
        if (not isinstance(row, dict) or row.get('datname') != self.database or
                type(row.get('pid')) is not int or row['pid'] <= 1 or
                not isinstance(row.get('backend_start'), str) or
                not row['backend_start']):
            raise Blocked('unattributed backend in owned database; refusing cleanup wait')
        # Exact PG maintenance identity permits a bounded read-only wait.
        # This PID is never treated as an owned API process or signaled.
        if (row.get('backend_type') == 'autovacuum worker' and row.get('usename') is None and
                row.get('application_name') == '' and row.get('client_addr') is None and
                row.get('client_port') is None):
            return 'postgres_background'
        if row.get('backend_type') != 'client backend':
            raise Blocked('unattributed backend in owned database; refusing cleanup wait')
        application, user = row.get('application_name'), row.get('usename')
        if application in (self.pg_application('api', 0), self.pg_application('api', 1)):
            if (user != self.db_user or row.get('client_addr') not in self.data.get('postgres_client_addresses', ()) or
                    type(row.get('client_port')) is not int or not 0 < row['client_port'] <= 65535):
                raise Blocked('API backend role/client-address attribution mismatch')
            return 'owned_api'
        if application in (self.pg_application('sql'), self.pg_application('admin')):
            expected_user = self.db_user if application == self.pg_application('sql') else 'testlab'
            if (user != expected_user or row.get('client_addr') is not None or
                    type(row.get('client_port')) is not int or row['client_port'] != -1):
                raise Blocked('SQL backend role/Unix-socket attribution mismatch')
            return 'owned_fixture_sql'
        raise Blocked('foreign or unattributed application in owned database; refusing cleanup wait')


    def pg(self, statement, database='postgres', user='testlab', timeout=12, deadline=None):
        if database not in ('postgres', self.database) or user not in ('testlab', self.db_user):
            raise Blocked('SQL outside this invocation database/user boundary')
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
            if timeout <= 0:
                raise Blocked('management cleanup exhausted its 40-second budget')
        guard_pg(inspect_pg(self.data['postgres_container'], min(timeout, 3)), OWNER, self.data['postgres_image_id'])
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
            if timeout <= 0:
                raise Blocked('management cleanup exhausted its budget after PG inspection')
        prefix = 'SET search_path=public; SET statement_timeout=8000; SET lock_timeout=2000; '
        if database == self.database:
            prefix += ('DO $ro$ BEGIN IF current_database()<>' + quote(self.database) + ' OR current_user<>' + quote(user) +
                       " OR current_schema()<>'public' OR current_setting('search_path')<>'public'" +
                       ' OR (SELECT shobj_description(oid,\'pg_database\') FROM pg_database WHERE datname=current_database()) IS DISTINCT FROM ' + quote(self.owner_tag) +
                       ' OR (SELECT shobj_description(oid,\'pg_authid\') FROM pg_roles WHERE rolname=' + quote(self.db_user) + ') IS DISTINCT FROM ' + quote(self.owner_tag) +
                       " THEN RAISE EXCEPTION 'owned SQL identity mismatch'; END IF; END $ro$; ")
        try:
            app_name = self.pg_application('admin' if user == 'testlab' else 'sql')
            result = subprocess.run(['docker', 'exec', '-i', '-e', 'PGAPPNAME=' + app_name, self.data['postgres_container'], 'psql',
                                     '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-U', user, '-d', database],
                                    input=prefix + statement, text=True, capture_output=True, timeout=timeout)
        except subprocess.TimeoutExpired:
            phase = getattr(self, 'cleanup_phase', 'fixture_sql')
            detail = dict(stage=phase, database=database, user=user, timeout_seconds=timeout,
                          mutation_outcome='unknown' if phase.startswith('drop_') else 'not_attested')
            self.diagnostic(self.work / ('sql-timeout-' + uuid.uuid4().hex + '.json'), detail)
            self.diagnostic(self.work / 'first-sql-timeout.json', detail, exclusive=True)
            raise SQLTransportTimeout('owned SQL transport deadline at ' + phase + '; outcome requires readback; private diagnostic retained') from None
        if result.returncode:
            self.diagnostic(self.work / ('sql-error-' + uuid.uuid4().hex + '.json'),
                            {'stderr': result.stderr, 'database': database, 'user': user})
            raise Blocked('owned SQL failed; private diagnostic retained without printing credentials')
        return result.stdout.strip()


    def sql(self, statement, read=False):
        if read:
            statement = 'BEGIN READ ONLY; ' + statement + '; COMMIT;'
        return self.pg(statement, self.database, self.db_user)


    def diagnostic(self, path, value, exclusive=False):
        try:
            if exclusive:
                private_file(path, json.dumps(value))
            else:
                _private(path, value)
        except FileExistsError:
            if not exclusive:
                raise
        except BaseException as error:
            # During resource cleanup, diagnostic IO must not interrupt DDL
            # or its ownership/absence readbacks. Still fail after cleanup.
            faults = getattr(self, 'cleanup_report_faults', None)
            if faults is None:
                raise
            faults.append(error)

    def provision(self):
        self.frozen()
        absence = json.loads(self.pg("SELECT json_build_object('role',EXISTS(SELECT 1 FROM pg_roles WHERE rolname=" + quote(self.db_user) +
                                    "),'database',EXISTS(SELECT 1 FROM pg_database WHERE datname=" + quote(self.database) + '));'))
        if absence != {'role': False, 'database': False}:
            raise Blocked('fresh role/database already exists; refusing adoption')
        self.role_attempted = True
        self.pg('BEGIN; CREATE ROLE ' + identifier(self.db_user) + ' LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD ' +
                quote(self.db_password) + '; COMMENT ON ROLE ' + identifier(self.db_user) + ' IS ' + quote(self.owner_tag) + '; COMMIT;')
        self.db_attempted = True
        self.pg('CREATE DATABASE ' + identifier(self.database) + ' OWNER ' + identifier(self.db_user) + ' TEMPLATE template0;')
        self.pg('COMMENT ON DATABASE ' + identifier(self.database) + ' IS ' + quote(self.owner_tag) +
                '; REVOKE CONNECT,TEMPORARY ON DATABASE ' + identifier(self.database) + ' FROM PUBLIC;')
        self.pg('CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;', self.database)
        _private(self.work / 'instance.json', dict(tier='nonprod', name='Management acceptance ' + self.nonce,
            domain='testlab.local', default_env='test', environments=[
                dict(id='test', label='Test', kind='standing', tone='dev', clusters=[]),
                dict(id='staging', label='Staging', kind='standing', tone='stg', clusters=[])]))
        _private(self.work / 'models.json', dict(tier='nonprod', environments=[
            dict(id=env, models=['openai:management-synthetic'], default_model='openai:management-synthetic')
            for env in ('test', 'staging')]))
        _private(self.work / 'commands.json', [])
        emit('management.fresh_database', 'passed', dict(database=self.database, user=self.db_user,
             owner=OWNER, owner_tag=self.owner_tag, synthetic_input=True, postgres_superuser=False,
             shared_app_database_touched=False))

    def seed(self):
        statements = ['BEGIN;']
        for name, uid in self.users.items():
            env, role = ('staging' if name == 'hidden' else 'test'), self.roles[name]
            statements.append('INSERT INTO users(id,email,name,role,password_hash) SELECT ' + quote(uid) + ',' +
                quote('member-' + self.nonce + '-' + name + '@testlab.invalid') + ',' + quote(self.marker + ' ' + name) +
                ",'viewer',password_hash FROM users WHERE email=" + quote(self.admin_email) + ';')
            statements.extend(['DELETE FROM rbac_user_roles WHERE user_id=' + quote(uid) + ';',
                'INSERT INTO rbac_roles(id,name,v3_kind) VALUES(' + quote(role) + ',' + quote(role) + ",'business');",
                'INSERT INTO rbac_user_roles(user_id,role_id) VALUES(' + quote(uid) + ',' + quote(role) + ');'])
            modules = ('environment',) if name in ('denied', 'target') else ('environment', 'services', 'roles', 'members', 'workspace', 'audit')
            for module in modules:
                scope, sid = ('global', '*') if name == 'target' else ('environment', env)
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                  ','.join(quote(v) for v in (role, module, 'view', scope, sid)) + ');')
            if name == 'target':
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                  ','.join(quote(v) for v in (role, 'events', 'view', 'global', '*')) + ');')
        role = self.roles['machine']
        statements.append('INSERT INTO rbac_roles(id,name,v3_kind) VALUES(' + quote(role) + ',' + quote(role) + ",'machine');")
        for module in ('environment', 'events'):
            statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                              ','.join(quote(v) for v in (role, module, 'view', 'global', '*')) + ');')
        statements.append('COMMIT;')
        self.sql('\n'.join(statements))
        value = json.loads(self.sql("SELECT json_build_object('users',(SELECT count(*) FROM users WHERE id=ANY(ARRAY[" +
            ','.join(quote(x) for x in self.users.values()) + "])), 'business_rows',(SELECT count(*) FROM applications)," +
            "'manager_grants',(SELECT count(*) FROM v3_role_grants WHERE role_id=ANY(ARRAY[" +
            ','.join(quote(x) for x in self.roles.values()) + "]) AND action='manage'))", read=True))
        check(value == {'users': 5, 'business_rows': 0, 'manager_grants': 0}, 'synthetic identity-only seed differs')
        emit('management.synthetic_identities', 'passed', value)


    def start(self):
        self.frozen()
        for index, port in enumerate(PORTS):
            with socket.socket() as sock:
                sock.settimeout(.3)
                if sock.connect_ex(('127.0.0.1', port)) == 0:
                    raise Blocked('management API port occupied before launch')
            dsn = 'postgres://' + self.db_user + ':' + self.db_password + '@127.0.0.1:15461/' + self.database + '?' + urlencode(
                dict(sslmode='disable', options='-csearch_path=public', application_name=self.pg_application('api', index)))
            variables = dict(DATABASE_URL=dsn, AIOPS_V3_INSTANCE_CONFIG=str(self.work / 'instance.json'),
                AIOPS_ADDR='127.0.0.1:' + str(port), AIOPS_PUBLIC_ORIGIN=BASES[index], AIOPS_SECURE_COOKIES='false',
                AIOPS_ADMIN_EMAIL=self.admin_email, AIOPS_ADMIN_PASSWORD=self.password,
                AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32', AIOPS_V3_MODEL_BINDINGS_FILE=str(self.work / 'models.json'),
                AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.work / 'commands.json'),
                OPENAI_MODEL='management-synthetic', OPENAI_API_KEY='synthetic-no-inference',
                OPENAI_BASE_URL='http://127.0.0.1:9/v1', EMBEDDING_PROVIDER='openai',
                EMBEDDING_MODEL='management-synthetic', EMBEDDING_API_KEY='synthetic-no-embedding', EMBEDDING_BASE_URL='http://127.0.0.1:9/v1')
            log = os.open(self.work / ('api-' + str(index) + '.log'), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            try:
                with acquisition_signals():
                    proc = subprocess.Popen([self.data['binary']], cwd=self.root,
                                            env=command_env(variables, {}), stdout=log, stderr=log)
                    self.processes.append(proc)
                    self.state['pids'].append(proc.pid)
                    _private(self.work / 'state.json', self.state)
            finally:
                os.close(log)
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                if proc.poll() is not None:
                    raise Blocked('fresh formal API exited at startup; private startup log retained')
                try:
                    status, raw, _ = transport(BASES[index], 'GET', '/healthz', timeout=2)
                    if status == 200:
                        if not _process_port(proc.pid, port):
                            raise Blocked('fresh Popen child does not own its exact listener')
                        emit('management.api_ready.' + str(index), 'passed', {'status': status, 'pid': proc.pid, 'port': port})
                        break
                except OSError:
                    pass
                time.sleep(.2)
            else:
                raise Blocked('fresh formal API startup deadline exceeded')
        self.frozen()


    def first_red(self, row):
        for path in (self.work / 'first-failure.json', self.root / '.local/testlab/first-failures' / ('api-management-' + self.run_id + '.json')):
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            try:
                private_file(path, json.dumps(row, ensure_ascii=False))
            except FileExistsError:
                pass

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0,
             environment='test', validate=None, headers=None, purpose='target', conflict=None):
        if index not in (0, 1) or environment not in ('test', 'staging', 'all') or user not in (None, 'admin', 'reader_old', *self.users):
            raise Blocked('management account/origin/environment boundary mismatch')
        if scenario not in WIRE_SCENARIOS or purpose not in ('target', 'fixture', 'audit_evidence', 'dependency_unavailable', 'concurrency'):
            raise Blocked('management scenario/purpose violates the Runner protocol')
        if headers and set(headers) - {'If-Match', 'If-Role-Matrix-Revision'}:
            raise Blocked('only revision preconditions may be supplied as extra headers')
        canonical, route_class = self.contract.canonical(method, path)
        request_headers = {'Origin': BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en-US'}
        if user in self.cookies:
            request_headers['Cookie'] = self.cookies[user]
        request_headers.update(headers or {})
        if body is not None:
            request_headers['Content-Type'] = 'application/json'
        payload = json.dumps(body).encode() if body is not None else None
        with self.lock:
            self.calls += 1
            number = self.calls
        expected = {'status': [want] if conflict is None else [want, conflict[0]], 'error_code': code}
        if conflict is not None:
            expected['conflict_error_code'] = conflict[1]
        row = dict(name='management.http.' + str(number), method=method, path=canonical,
                   route_class=route_class, scenario=scenario, scenario_detail=purpose, expected=expected,
                   run_id=self.run_id, call_id=self.run_id + '-' + str(number), test_name='FormalManagementChain')
        try:
            status, raw, response_headers = transport(BASES[index], method, path, payload, request_headers)
            row['actual'] = {'status': status, 'error_code': None}
            value = json.loads(raw) if raw else None
            actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual'] = {'status': status, 'error_code': actual_code}
            check(status in expected['status'], 'formal HTTP differs from the exact expected status')
            wanted_code = conflict[1] if conflict is not None and status == conflict[0] else code
            check(wanted_code is None or actual_code == wanted_code, 'formal HTTP error code differs')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate:
                    validate(value)
            elif status >= 400:
                check(isinstance(value, dict) and isinstance(value.get('error'), dict) and
                      isinstance(actual_code, str) and isinstance(value['error'].get('message'), str),
                      'formal error DTO is invalid')
            row['status'] = 'passed'
            row['registration_evidence'] = 200 <= status < 300 and scenario == 'normal' and purpose == 'target'
        except Exception as error:
            row.setdefault('actual', {'status': None})
            row.update(status='failed', registration_evidence=False)
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row)
            if 'raw' in locals():
                _private(self.work / ('failure-response-' + str(number) + '.json'),
                         dict(status=status, request_body=body, response_body=raw.decode('utf-8', 'replace')))
            with self.lock:
                append_json(self.work / 'interfaces.jsonl', row)
                fields = {'interface': row} if type(row['actual']['status']) is int else {}
                emit(row['name'], 'failed', row['actual'], **fields)
            raise AssertionError('formal management HTTP/DTO assertion failed; private first red retained') from None
        with self.lock:
            append_json(self.work / 'interfaces.jsonl', row)
            emit(row['name'], 'passed', row['actual'], interface=row)
        return value, response_headers

    def login(self, name, index=0):
        env = 'staging' if name == 'hidden' else 'test'
        email = self.admin_email if name == 'admin' else 'member-' + self.nonce + '-' + name + '@testlab.invalid'
        value, headers = self.call(name, 'POST', '/api/v1/auth/login', {'email': email, 'password': self.password},
                                   index=index, environment=env, purpose='fixture')
        check(value['user']['email'] == email and (name == 'admin' or value['user']['id'] == self.users[name]),
              'formal login returned a different synthetic identity')
        cookie = header(headers, 'Set-Cookie').split(';', 1)[0]
        check(cookie.startswith('aiops_session=') and len(cookie) > 40 and '\r' not in cookie and '\n' not in cookie,
              'formal login omitted its generated host-local session')
        self.cookies[name] = cookie
        return cookie


    def native_services(self):
        key = 'management-' + self.nonce[:20]
        hidden_key = key + '-staging'
        body = {'items': [dict(id=key, tier='T1', owner='Synthetic owner', slo='99.9', p99='250')]}
        self.call('admin', 'POST', '/api/v1/services', body, want=201,
                  validate=lambda v: check(v == {'created': 1}, 'native service creation count differs'))
        self.call('admin', 'POST', '/api/v1/services', body, want=409, code='service_exists', scenario='idempotency', index=1)
        self.call('admin', 'POST', '/api/v1/services', {'items': [dict(id=hidden_key, tier='T2')]},
                  environment='staging', want=201, index=1,
                  validate=lambda v: check(v == {'created': 1}, 'native staging service creation count differs'))
        self.call('reader', 'POST', '/api/v1/services', body, want=403, code='forbidden', scenario='authorization')
        self.call('admin', 'POST', '/api/v1/services', {'items': []}, want=422, code='invalid_service', scenario='boundary')
        self.call('admin', 'POST', '/api/v1/services', {'items': [dict(id=key + '-invalid', slo='101')]},
                  want=422, code='invalid_service', scenario='boundary')
        self.call('admin', 'POST', '/api/v1/services', body, environment='all', want=409,
                  code='env_view_only', scenario='boundary')
        def peek(v):
            check(v['name'] == key and v['env'] == 'test' and v['tier'] == 'T1' and v['owner'] == 'Synthetic owner',
                  'native partial service metadata differs')
            check(v['status'] is None and v['oncall'] is None and v['change'] is None and v['kpis'] == [],
                  'service peek invented live health, on-call, change or KPI evidence')
            check(isinstance(v['id'], str) and bool(v['id']), 'native service id is missing')
        value, _ = self.call('reader', 'GET', '/api/v1/services/' + key + '/peek', index=1, validate=peek)
        sid = self.ids['service'] = value['id']
        self.call('reader', 'GET', '/api/v1/services/' + sid + '/peek', validate=peek)
        self.call('reader', 'GET', '/api/v1/services/' + hidden_key + '/peek', want=404,
                  code='not_found', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/services/' + sid + '/peek', environment='staging', want=403,
                  code='forbidden', scenario='authorization')
        self.call('denied', 'GET', '/api/v1/services/' + sid + '/peek', want=404,
                  code='not_found', scenario='authorization')
        path = '/api/v1/me/watched-services/' + sid
        validate_watch = lambda v: check(v == {'id': sid, 'watched': True}, 'follow DTO differs')
        self.call('reader', 'PUT', path, validate=validate_watch)
        self.call('reader', 'PUT', path, index=1, scenario='idempotency', validate=validate_watch)
        def watched(v):
            check(len(v['items']) == 1 and v['items'][0]['id'] == sid and v['items'][0]['name'] == key,
                  'native watch readback differs')
            check(v['items'][0]['status'] is None and v['items'][0]['availability']['state'] == 'unavailable'
                  and v['complete_v3_dto'] is False, 'watched service falsely claimed complete health evidence')
        self.call('reader', 'GET', '/api/v1/me/watched-services', index=1, validate=watched)
        self.call('peer', 'GET', '/api/v1/me/watched-services', scenario='authorization',
                  validate=lambda v: check(v['items'] == [], 'another account inherited private follows'))
        self.call('admin', 'PUT', path, environment='staging', want=404, code='not_found', scenario='authorization')
        self.call('denied', 'PUT', path, want=404, code='not_found', scenario='authorization')
        self.call('reader', 'PUT', path + '?q=forged', want=422, code='invalid_filter', scenario='boundary')
        self.call(None, 'PUT', path, want=401, code='unauthorized', scenario='authorization')
        self.call('reader', 'DELETE', path, want=204)
        self.call('reader', 'DELETE', path, want=204, scenario='idempotency', index=1)
        self.call('reader', 'GET', '/api/v1/me/watched-services', validate=lambda v: check(v['items'] == [], 'unfollow did not persist'))
        self.audit_count('v3.service.created', 2)
        self.audit_count('v3.watched_service.followed', 1)
        self.audit_count('v3.watched_service.unfollowed', 1)

    def workspace(self):
        path = '/api/v1/workspace'
        initial, headers = self.call('reader', 'GET', path)
        old = revision(headers, 'ETag', quoted=True)
        etag = header(headers, 'ETag')
        expected = copy.deepcopy(initial)
        expected.update(name='Synthetic Management Workspace', slug='management-test', timezone='Asia/Shanghai',
                        locale='en-US', week_start='sunday', retention_days=180)
        self.call('admin', 'PUT', path, expected, want=428, code='workspace_precondition_required', scenario='boundary')
        for bad in ('*', 'W/' + etag, '"01"', etag + ',' + etag):
            self.call('admin', 'PUT', path, expected, headers={'If-Match': bad},
                      want=400, code='invalid_precondition', scenario='boundary')
        self.call('reader', 'PUT', path, expected, headers={'If-Match': etag}, want=403,
                  code='forbidden', scenario='authorization')
        self.call('reader', 'GET', path, environment='staging', want=403, code='forbidden', scenario='authorization')
        self.call('denied', 'GET', path, want=403, code='forbidden', scenario='authorization')
        self.call('admin', 'PUT', path, {}, headers={'If-Match': etag}, want=422, code='invalid_body', scenario='boundary')
        immutable = copy.deepcopy(expected)
        immutable['data_region'] = 'unapproved-region'
        self.call('admin', 'PUT', path, immutable, headers={'If-Match': etag}, want=422,
                  code='invalid_settings', scenario='boundary')
        saved, hs = self.call('admin', 'PUT', path, expected, headers={'If-Match': etag}, index=1,
                              validate=lambda v: check(v == expected, 'saved workspace differs'))
        check(revision(hs, 'ETag', quoted=True) == old + 1, 'workspace revision did not advance once')
        self.call('admin', 'PUT', path, expected, headers={'If-Match': etag}, want=412,
                  code='workspace_changed', scenario='idempotency')
        current = header(hs, 'ETag')
        candidates = [dict(saved, name='Concurrent Workspace ' + str(i)) for i in range(2)]
        pair = concurrent_pair(lambda i: self.call('admin', 'PUT', path, candidates[i], index=i,
            headers={'If-Match': current}, conflict=(412, 'workspace_changed'),
            scenario='idempotency', purpose='concurrency',
            validate=lambda v: check(v == candidates[i], 'concurrent workspace winner DTO differs')))
        winners = [i for i, (v, _) in enumerate(pair) if 'error' not in v]
        check(len(winners) == 1, 'same If-Match concurrent workspace writes did not yield exactly one winner')
        winner = winners[0]
        check(revision(pair[winner][1], 'ETag', quoted=True) == old + 2, 'workspace concurrent revision advanced incorrectly')
        self.call('reader', 'GET', path, index=1,
                  validate=lambda v: check(v == candidates[winner], 'workspace lost or overwrote its concurrent winner'))
        self.audit_count('v3.workspace.updated', 2)

    def role_matrix(self):
        path = '/api/v1/roles/' + self.roles['target']
        initial, headers = self.call('admin', 'GET', path)
        old = revision(headers, 'X-Role-Matrix-Revision')
        check(initial['id'] == self.roles['target'] and initial['kind'] == 'business', 'native target role identity differs')
        check(len(initial['matrix']) == 13 and all(len(row['grants']) == 5 for row in initial['matrix']),
              'role editor catalog differs; inspect the current source before updating runner')
        # This target role never grants the requester access.
        # Index 1 is the verified Incidents & alerts row in v3admin/catalog.go.
        matrix = copy.deepcopy(initial['matrix'])
        matrix[1]['grants'][1] = 1
        req = {'matrix': matrix}
        rh = {'If-Role-Matrix-Revision': str(old)}
        self.call('admin', 'PUT', path, req, want=428, code='role_revision_required', scenario='boundary')
        self.call('admin', 'PUT', path, req, headers={'If-Role-Matrix-Revision': '01'},
                  want=400, code='invalid_precondition', scenario='boundary')
        self.call('admin', 'PUT', path, {'matrix': []}, headers=rh, want=422, code='invalid_matrix', scenario='boundary')
        self.call('reader', 'PUT', path, req, headers=rh, want=403, code='forbidden', scenario='authorization')
        self.call('denied', 'GET', path, want=403, code='forbidden', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/roles/' + self.roles['hidden'], want=404,
                  code='not_found', scenario='authorization')
        self.call('reader', 'GET', path, environment='staging', want=403, code='forbidden', scenario='authorization')
        _, saved_headers = self.call('admin', 'PUT', path, req, headers=rh, index=1,
                  validate=lambda v: check(v == {'ok': True}, 'role save must return its exact acknowledgement only'))
        check(revision(saved_headers, 'X-Role-Matrix-Revision') == old + 1, 'role matrix revision did not advance once')
        current = header(saved_headers, 'X-Role-Matrix-Revision')
        self.call('admin', 'PUT', path, req, headers=rh, want=409, code='role_changed', scenario='idempotency')
        self.call('admin', 'GET', path, index=1,
                  validate=lambda v: check(v['matrix'] == matrix, 'saved global matrix differs'))
        same, unchanged_headers = self.call('admin', 'PUT', path, req,
            headers={'If-Role-Matrix-Revision': current}, scenario='idempotency')
        check(same == {'ok': True} and header(unchanged_headers, 'X-Role-Matrix-Revision') == current,
              'identical role save advanced its matrix revision')
        candidates = [copy.deepcopy(matrix) for _ in range(2)]
        candidates[0][1]['grants'][2] = 1
        candidates[1][0]['grants'][0] = 1
        pair = concurrent_pair(lambda i: self.call('admin', 'PUT', path, {'matrix': candidates[i]}, index=i,
            headers={'If-Role-Matrix-Revision': current}, conflict=(409, 'role_changed'),
            scenario='idempotency', purpose='concurrency',
            validate=lambda v: check(v == {'ok': True}, 'concurrent role acknowledgement differs')))
        winners = [i for i, (v, _) in enumerate(pair) if v == {'ok': True}]
        check(len(winners) == 1, 'same-revision role writes did not yield exactly one winner')
        winner = winners[0]
        check(revision(pair[winner][1], 'X-Role-Matrix-Revision') == old + 2, 'concurrent role revision differs')
        self.call('admin', 'GET', path, index=1,
                  validate=lambda v: check(v['matrix'] == candidates[winner], 'role lost its concurrent winner'))
        machine_path = '/api/v1/roles/' + self.roles['machine']
        machine, mh = self.call('admin', 'GET', machine_path)
        machine_matrix = copy.deepcopy(machine['matrix'])
        machine_matrix[4]['grants'][3] = 1
        self.call('admin', 'PUT', machine_path, {'matrix': machine_matrix},
                  headers={'If-Role-Matrix-Revision': header(mh, 'X-Role-Matrix-Revision')},
                  want=422, code='machine_approval_forbidden', scenario='authorization')
        self.call('admin', 'GET', machine_path,
                  validate=lambda v: check(v['matrix'] == machine['matrix'], 'rejected machine approval mutated its role'))
        self.audit_count('v3.role.matrix.updated', 2)

    def preferences(self):
        expected = dict(theme='dark', locale='en-US', density='compact', reduce_motion=True)
        path = '/api/v1/me/preferences'
        self.call('reader', 'PATCH', path, expected,
                  validate=lambda v: check(v == expected, 'saved UI preferences differ'))
        self.call('reader', 'PATCH', path, {'theme': 'light'}, index=1,
                  validate=lambda v: check(v == dict(expected, theme='light'), 'partial preference update erased other fields'))
        for body, code in [({'theme': 'invalid'}, 'invalid_settings'), ({'theme': None}, 'invalid_body'),
                           ({'unknown': True}, 'invalid_body')]:
            self.call('reader', 'PATCH', path, body, want=422, code=code, scenario='boundary')
        self.call('reader', 'PATCH', path, expected, environment='staging', want=403, code='forbidden', scenario='authorization')
        prefs = json.loads(self.sql('SELECT json_build_object(\'reader\',(SELECT json_build_object(\'theme\',theme,\'locale\',locale,' +
            "'density',density,'reduce_motion',reduce_motion) FROM v3_user_settings WHERE user_id=" + quote(self.users['reader']) +
            "),'peer_rows',(SELECT count(*) FROM v3_user_settings WHERE user_id=" + quote(self.users['peer']) + '))', read=True))
        check(prefs == {'reader': dict(expected, theme='light'), 'peer_rows': 0}, 'native preference persistence/ownership differs')
        notify_path = '/api/v1/me/notification-settings'
        original, _ = self.call('reader', 'GET', notify_path)
        check(original == dict(quiet=dict(enabled=False, **{'from': '22:00', 'to': '08:00'}), digest='daily', subscriptions=[]),
              'fresh native notification defaults differ')
        update = {'quiet': {'enabled': True, 'from': '23:00', 'to': '07:00'}, 'digest': 'weekly'}
        notification = dict(update, subscriptions=[])
        self.call('reader', 'PUT', notify_path, update, validate=lambda v: check(v == notification, 'notification save differs'))
        notification['digest'] = 'off'
        self.call('reader', 'PUT', notify_path, {'digest': 'off'}, index=1,
                  validate=lambda v: check(v == notification, 'notification partial save erased quiet hours'))
        self.call('reader', 'GET', notify_path, index=1, validate=lambda v: check(v == notification, 'notification readback differs'))
        self.call('peer', 'GET', notify_path, scenario='authorization',
                  validate=lambda v: check(v == original, 'notification settings crossed accounts'))
        for bad in ({'digest': 'instant'}, {'quiet': {'enabled': True, 'from': '25:00', 'to': '07:00'}},
                    {'subscriptions': {'invented-subscription': True}}, {'unexpected': True}):
            self.call('reader', 'PUT', notify_path, bad, want=422, code='invalid_settings', scenario='boundary')
        ai_path = '/api/v1/me/ai-preferences'
        ai, _ = self.call('reader', 'GET', ai_path)
        check(ai['default']['model'] == 'openai:management-synthetic', 'AI default is not the explicitly configured synthetic model')
        updated = dict(ai, show_reasoning='hidden', auto_context=False, memory=False, budget_warn_tokens=12345)
        self.call('reader', 'PUT', ai_path, updated, validate=lambda v: check(v == updated, 'AI preference save differs'))
        self.call('reader', 'GET', ai_path, index=1, validate=lambda v: check(v == updated, 'AI preference readback differs'))
        self.call('peer', 'GET', ai_path, scenario='authorization', validate=lambda v: check(v == ai, 'AI preferences crossed accounts'))
        for invalid in (dict(updated, default={'model': 'unregistered-model'}), dict(updated, budget_warn_tokens=99),
                        dict(updated, show_reasoning='invalid')):
            self.call('reader', 'PUT', ai_path, invalid, want=422, code='invalid_settings', scenario='boundary')
        self.call('reader', 'GET', ai_path, environment='all', want=422, code='invalid_environment', scenario='boundary')
        self.call('reader', 'PUT', ai_path, updated, environment='staging', want=403, code='forbidden', scenario='authorization')
        self.audit_count('v3.preferences.updated', 2)
        self.audit_count('v3.notification_settings.updated', 2)
        self.audit_count('v3.ai_preferences.updated', 1)

    def sessions(self):
        old_cookie = self.cookies['reader']
        self.cookies['reader_old'] = old_cookie
        self.login('reader', index=1)
        digest = lambda cookie: hashlib.sha256(cookie.split('=', 1)[1].encode()).hexdigest()
        old_id, current_id, peer_id = digest(old_cookie), digest(self.cookies['reader']), digest(self.cookies['peer'])
        path = '/api/v1/me/sessions/'
        for target in ('invalid-id', current_id):
            self.call('reader', 'DELETE', path + target, want=422, code='invalid_settings', scenario='boundary')
        self.call('reader', 'DELETE', path + peer_id, want=404, code='not_found', scenario='authorization')
        self.call('reader', 'DELETE', path + old_id, environment='all', want=409, code='env_view_only', scenario='boundary')
        self.call('reader', 'DELETE', path + old_id, want=204)
        self.call('reader', 'DELETE', path + old_id, want=404, code='not_found', scenario='idempotency', index=1)
        self.call('reader_old', 'GET', '/api/v1/me/notification-settings', want=401, code='unauthorized', scenario='authorization', index=1)
        self.call('reader', 'GET', '/api/v1/me/notification-settings', scenario='authorization')
        self.call('peer', 'GET', '/api/v1/me/notification-settings', scenario='authorization', index=1)
        proof = json.loads(self.sql("SELECT json_build_object('old_exists',EXISTS(SELECT 1 FROM sessions WHERE token_hash=" + quote(old_id) +
            "),'current_exists',EXISTS(SELECT 1 FROM sessions WHERE token_hash=" + quote(current_id) +
            "),'peer_exists',EXISTS(SELECT 1 FROM sessions WHERE token_hash=" + quote(peer_id) +
            "),'raw_recorded',EXISTS(SELECT 1 FROM management_audit WHERE changes::text LIKE " +
            quote('%' + old_cookie.split('=', 1)[1] + '%') + '))', read=True))
        check(proof == {'old_exists': False, 'current_exists': True, 'peer_exists': True, 'raw_recorded': False},
              'native session revoke affected wrong account or recorded its raw bearer')
        self.audit_count('v3.session.revoked', 1)

    def unavailable_profile(self):
        for method in ('GET', 'PUT'):
            self.call('reader', method, '/api/v1/me/profile', {} if method == 'PUT' else None,
                      want=503, code='identity_evidence_unavailable', scenario='failure', purpose='dependency_unavailable')
        self.audit_count('v3.profile.updated', 0)
        emit('management.authority_scope', 'passed', dict(positive_profile_coverage=False,
             profile_evidence='unconfigured HTTPS authority, exact 503 fail-closed only',
             model_inference_performed=False, notification_delivery_performed=False))

    def audit_count(self, action, expected):
        # Readback of HTTP-generated audit only; no INSERT/UPDATE result seed.
        count = int(self.sql('SELECT count(*) FROM management_audit WHERE action=' + quote(action) + " AND outcome='success'", read=True))
        check(count == expected, 'HTTP-generated audit count differs for ' + action)
        emit('management.audit_count.' + action, 'passed', dict(action=action, expected=expected, actual=count,
             source='read-only query of native HTTP transaction audit'))

    def audits(self):
        value, _ = self.call('admin', 'GET', '/api/v1/audit', environment='all', purpose='audit_evidence')
        expected = {'v3.service.created': 2, 'v3.watched_service.followed': 1, 'v3.watched_service.unfollowed': 1,
                    'v3.workspace.updated': 2, 'v3.role.matrix.updated': 2, 'v3.preferences.updated': 2,
                    'v3.notification_settings.updated': 2, 'v3.ai_preferences.updated': 1, 'v3.session.revoked': 1}
        selected = [row for row in value['items'] if row['action'] in expected]
        check(len(selected) == sum(expected.values()), 'public audit omitted or duplicated native management writes')
        check(all(row['result'] == 'ok' for row in selected), 'native successful audit outcome differs')
        for action, count in expected.items():
            check(sum(row['action'] == action for row in selected) == count, 'public audit action count differs: ' + action)
        for row in selected:
            self.call('admin', 'GET', '/api/v1/audit/' + row['id'], index=1, environment='all', purpose='audit_evidence',
                      validate=lambda v, row=row: check(v['id'] == row['id'] and v['chain_ok'] is True,
                                                       'native management audit seal/detail differs'))

    def run(self):
        self.provision()
        self.start()
        self.seed()
        for name in ('admin', 'reader', 'peer', 'denied', 'hidden'):
            self.login(name)
        self.native_services()
        self.workspace()
        self.role_matrix()
        self.preferences()
        self.sessions()
        self.unavailable_profile()
        self.audits()
        self.frozen()
        rows = [json.loads(line) for line in (self.work / 'interfaces.jsonl').read_text().splitlines()]
        positive = sorted({(x['method'], x['path'], x['route_class']) for x in rows if x['registration_evidence']})
        result = dict(run_id=self.run_id, database=self.database, http_results=self.calls,
            positive_routes=[list(x) for x in positive], main_positive=sum(x[2] == 'main' for x in positive),
            auxiliary_positive=sum(x[2] != 'main' for x in positive),
            selected_route_count=len(TARGETS), support_routes=[list(x) for x in SUPPORT],
            source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
            unproven=MISSING, unverified=['real IdP/MFA/on-call authority', 'model inference and delivery',
              'full JSON Schema validation', 'real external service observations', 'performance', '24-hour continuity'])
        _private(self.work / 'result.json', result)
        return result

    def cleanup(self):
        deadline = time.monotonic() + 40
        self.cleanup_report_faults = []
        timeout_events = []
        # Handles acquired in this invocation only. A persisted PID is never signaled.
        failures = []
        for proc in reversed(self.processes):
            try:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=min(8, max(.1, deadline - time.monotonic())))
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=min(2, max(.1, deadline - time.monotonic())))
            except Exception as error:
                failures.append(type(error).__name__)
        if failures:
            raise Blocked('owned API cleanup failed; database retained without forced session termination')
        exit_codes = [p.poll() for p in self.processes]
        if any(code is None for code in exit_codes):
            raise Blocked('owned API process remains active; refusing database cleanup')
        owned_process_ids = [p.pid for p in self.processes if type(p.pid) is int]
        proof = None
        quiescence = dict(samples=0, waited_seconds=0, owned_pg_backend_pids=[], postgres_background_backend_pids=[])
        def operation(phase, statement, limit=None):
            self.cleanup_phase = phase
            self.diagnostic(self.work / 'cleanup-progress.json', dict(stage=phase, database=self.database,
                     database_user=self.db_user, owner_tag=self.owner_tag,
                     remaining_seconds=max(0, deadline - time.monotonic())))
            # The SQL statement timeout is 8s; Docker exec/startup must not
            # preempt it at 3/5s. Every operation still shares the 40s deadline.
            return self.pg(statement, timeout=12, deadline=deadline if limit is None else min(deadline, limit))
        resource_query = ("SELECT json_build_object('role_exists',EXISTS(SELECT 1 FROM pg_roles WHERE rolname=" + quote(self.db_user) +
            "),'database_exists',EXISTS(SELECT 1 FROM pg_database WHERE datname=" + quote(self.database) +
            "),'role',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=" + quote(self.db_user) +
            "),'database_owner',(SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=" + quote(self.database) +
            "),'database_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=" + quote(self.database) +
            "),'connections',(SELECT count(*) FROM pg_stat_activity WHERE datname=" + quote(self.database) +
            "),'connection_details',(SELECT COALESCE(json_agg(json_build_object('pid',pid,'datname',datname,'usename',usename," +
            "'application_name',application_name,'client_addr',host(client_addr),'client_port',client_port,'backend_type',backend_type," +
            "'backend_start',backend_start::text,'state',state)), '[]'::json) FROM pg_stat_activity WHERE datname=" + quote(self.database) + '));')
        def resources(phase, limit=None):
            state = json.loads(operation(phase, resource_query, limit))
            if (type(state.get('role_exists')) is not bool or type(state.get('database_exists')) is not bool or
                    type(state.get('connections')) is not int or state['connections'] < 0):
                raise Blocked('fresh SQL resource catalog readback is incomplete')
            if state['role_exists'] and state.get('role') != self.owner_tag:
                raise Blocked('fresh SQL role ownership tag changed; refusing cleanup')
            if state['database_exists'] and (not self.db_attempted or not state['role_exists'] or
                    state.get('database_owner') != self.db_user or state.get('database_tag') != self.owner_tag):
                raise Blocked('fresh SQL database owner/tag changed; refusing cleanup')
            details = state.get('connection_details', [])
            if (not isinstance(details, list) or len(details) != state['connections'] or len(details) > 256 or
                    len({row.get('pid') for row in details if isinstance(row, dict)}) != len(details)):
                raise Blocked('owned database connection catalog is incomplete or ambiguous')
            return state
        def await_quiescence(state):
            if state['connections'] == 0:
                return state
            started = time.monotonic()
            limit = min(deadline, started + 8)
            snapshots, observed_pids, background_pids = [], set(), set()
            while True:
                details, attribution_error = [], None
                for row in state.get('connection_details', []):
                    try:
                        origin = self.connection_origin(row)
                        (background_pids if origin == 'postgres_background' else observed_pids).add(row['pid'])
                        details.append(dict(row, proven_origin=origin))
                    except Blocked as error:
                        attribution_error = attribution_error or error
                        details.append(dict(row, proven_origin='unproven', attribution_error=str(error)))
                # Preserve identities before refusing foreign backends; never
                # collect query text or signal a PostgreSQL backend PID.
                snapshot = dict(database=self.database, database_user=self.db_user,
                                api_owned_process_ids=owned_process_ids,
                                connections=state['connections'], details=details,
                                elapsed_seconds=max(0, time.monotonic() - started))
                snapshots.append(snapshot)
                self.diagnostic(self.work / 'first-connection-snapshot.json', snapshot, exclusive=True)
                self.diagnostic(self.work / 'connection-quiescence.json', snapshots)
                if attribution_error is not None:
                    raise attribution_error
                if state['connections'] == 0:
                    quiescence.update(samples=len(snapshots), waited_seconds=snapshot['elapsed_seconds'],
                                      owned_pg_backend_pids=sorted(observed_pids),
                                      postgres_background_backend_pids=sorted(background_pids))
                    return state
                remaining = limit - time.monotonic()
                if remaining <= 0 or len(snapshots) >= 64:
                    raise Blocked('owned PostgreSQL backends did not quiesce within bounded cleanup budget; no forced termination')
                time.sleep(min(.15, remaining))
                state = resources('connection_quiescence', limit)
        def drop(phase, statement):
            try:
                operation(phase, statement)
                return None
            except SQLTransportTimeout:
                # Do not replay DROP. The server may have finished after the
                # client disconnected. Only exact current absence can resolve
                # the timeout; remaining/unreadable/foreign resources fail.
                observed = resources('timeout_readback_' + phase)
                absent = not observed['database_exists'] if phase == 'drop_database' else not observed['role_exists'] and not observed['database_exists']
                detail = dict(stage=phase, database=self.database, database_user=self.db_user,
                              transport_timed_out=True, resource_absent=absent, resource_readback=observed)
                self.diagnostic(self.work / (phase + '-timeout-readback.json'), detail)
                if not absent:
                    raise Blocked('owned ' + phase + ' transport timed out and exact resource remains; no DDL retry performed')
                # Defer stdout until both resources have been cleared. A
                # BrokenPipe here must not leave the remaining role behind.
                timeout_events.append((phase, detail))
                return observed
        if self.role_attempted:
            state = resources('identity_readback')
            if state['database_exists']:
                state = await_quiescence(state)
                if state['database_exists']:
                    observed = drop('drop_database', 'DROP DATABASE ' + identifier(self.database) + ';')
                    if observed is not None:
                        state = observed
            if state['role_exists']:
                drop('drop_role', 'DROP ROLE ' + identifier(self.db_user) + ';')
            proof = json.loads(operation('absence_readback', "SELECT json_build_object('role',EXISTS(SELECT 1 FROM pg_roles WHERE rolname=" + quote(self.db_user) +
                "),'database',EXISTS(SELECT 1 FROM pg_database WHERE datname=" + quote(self.database) + '));'))
            check(proof == {'role': False, 'database': False}, 'owned SQL cleanup readback is incomplete')
        receipt = dict(database_absent=proof is not None and not proof['database'],
                       role_absent=proof is not None and not proof['role'],
                       resources_allocated_by_invocation=self.role_attempted,
                       owned_processes_exited=True,
                       owned_process_ids=owned_process_ids,
                       connection_quiescence=quiescence,
                       shared_container_removed=False, shared_database_touched=False, private_evidence_preserved=True)
        self.diagnostic(self.work / 'cleanup.json', receipt)
        for phase, detail in timeout_events:
            try:
                emit('management.cleanup_timeout_resolved.' + phase, 'passed', detail, type='environment')
            except BaseException as error:
                self.cleanup_report_faults.append(error)
        if self.cleanup_report_faults:
            raise self.cleanup_report_faults[0]
        emit('management.cleanup', 'passed', receipt)


def append_json(path, row):
    fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'a') as stream:
        stream.write(json.dumps(row, ensure_ascii=False) + '\n')



@contextlib.contextmanager
def acquisition_signals():
    """Keep a new Popen handle reachable before honoring cancellation."""
    interrupted = []
    previous = signal.signal(signal.SIGTERM, lambda signum, frame: interrupted.append(True))
    try:
        yield
    finally:
        signal.signal(signal.SIGTERM, previous)
    if interrupted:
        raise Blocked('management process acquisition interrupted after retaining its owned handle')



@contextlib.contextmanager
def cooperative_signals():
    def stop(signum, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise Blocked('management chain interrupted; invocation cleanup required')
    previous = signal.signal(signal.SIGTERM, stop)
    try:
        yield
    finally:
        signal.signal(signal.SIGTERM, previous)



def execute(chain):
    primary = cleanup = reporting = result = None
    interrupted = []
    def report(function, *args, **kwargs):
        nonlocal reporting
        try:
            function(*args, **kwargs)
        except BaseException as error:
            reporting = reporting or error
    try:
        try:
            result = chain.run()
        except BaseException as error:
            primary = error
            row = dict(stage='business_contract', error_type=type(error).__name__, error=str(error))
            report(chain.first_red, row)
            report(emit, 'management.business', 'blocked' if isinstance(error, Blocked) else 'failed', row)
    finally:
        # Cleanup covers run *and* its evidence/stdout handling. Neither ENOSPC
        # nor BrokenPipe in error reporting can bypass this outer finally.
        registered, previous = False, None
        try:
            previous = signal.signal(signal.SIGTERM, lambda signum, frame: interrupted.append(True))
            registered = True
        except BaseException as error:
            reporting = reporting or error
        try:
            try:
                chain.cleanup()
            except BaseException as error:
                cleanup = error
                row = dict(stage='cleanup', error_type=type(error).__name__, error=str(error))
                report(_private, chain.work / 'cleanup-error.json', row)
                report(chain.first_red, row)
                report(emit, 'management.cleanup_failure', 'failed', row)
        finally:
            if registered:
                report(signal.signal, signal.SIGTERM, previous)
    if interrupted:
        row = dict(stage='cleanup_interrupted', cleanup_completed=cleanup is None)
        report(_private, chain.work / 'cleanup-interrupted.json', row)
        report(chain.first_red, row)
        report(emit, 'management.cleanup_interrupted', 'blocked', row)
        primary = primary or Blocked('management chain interrupted during bounded cleanup')
    if primary is not None:
        raise primary
    if cleanup is not None:
        raise cleanup
    if reporting is not None:
        raise reporting
    emit('management.complete', 'passed', result)
    return result


def inventory(root):
    contract = Contract(root)
    return dict(module=MODULE, selected_route_count=len(TARGETS), total_operations_with_support=len(TARGETS) + len(SUPPORT),
        targets=[dict(method=method, path='/api/v1' + path,
                      route_class='main' if (method, path) in contract.main else 'auxiliary_or_historical',
                      planned_evidence='dependency_failure_only' if path == '/me/profile' else 'normal_2xx_and_domain_readback')
                 for method, path in TARGETS],
        support_routes=[list(route) for route in SUPPORT], missing_sources=MISSING,
        ports=list(PORTS), owner=OWNER, fresh_database_prefix='aiops_testlab_management_',
        proof='source inventory only; no Docker/PG/API execution and no new positive acceptance evidence')


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--inventory', action='store_true', help='read current source contract only; never start the environment')
    mode.add_argument('--live', action='store_true', help='explicit parent-run opt-in for a fresh owned formal HTTP chain')
    args = parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(inventory(args.root), ensure_ascii=False))
        return 0
    try:
        with exclusive(args.root), cooperative_signals():
            data = doctor(args.root)
            chain = Chain(args.root, data, str(uuid.uuid4()))
            execute(chain)
    except Blocked as error:
        emit('management.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        emit('management.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
