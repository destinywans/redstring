"""Five native V3 projections in a fresh invocation-owned database.
Offline inventory/preparation is the default boundary; --live is explicit.
Cloned projection isolation/control/cleanup snapshot, not a mutable helper import.
Only identity/grant/config SQL writes; HTTP produces every business result.
"""

from __future__ import annotations

import argparse
import datetime

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


from urllib.parse import urlencode, urlsplit

from backend import control_proof

from env import PG_IMAGE, _private, _process_port

from faults import Blocked, private_json, validate_fixture, validate_identity

from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]

OWNER = 'testlab-3cbde3095125'

PORTS = (18610, 18611)

BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)

CONTROL = 'http://127.0.0.1:18560'

LIMIT = 2 << 20

MODULE = 'formal-api-projection'

WIRE_SCENARIOS = {'normal', 'authorization', 'boundary', 'idempotency', 'failure'}

TARGETS = (('GET', '/postmortems'), ('GET', '/ingest/push'), ('GET', '/webhooks'),
           ('GET', '/service-mappings'), ('PUT', '/service-mappings'),
           ('POST', '/alert-groups/bulk'), ('GET', '/memory'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'),
 ('POST', '/webhooks/alertmanager/{source}'), ('POST', '/events/change'),
 ('GET', '/alert-groups'), ('GET', '/incidents'), ('POST', '/incidents'), ('GET', '/incidents/{id}'),
 ('POST', '/incidents/{id}/status'), ('GET', '/postmortems/draft'),
 ('GET', '/postmortems/candidates'), ('POST', '/postmortems'),
 ('POST', '/postmortems/{id}/publish'), ('GET', '/audit'), ('GET', '/audit/{id}'))
MISSING = {'agent_catalog': 'No composition-root trusted importer or public producer; high priority TODO, not SQL-seeded.',
           'personal_authority': 'Oncall/contact/session-device authority unconfigured; no invented positive coverage.',
           'service_live_views': 'Complete historical metrics/release/completed native run evidence not fabricated.'}

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
        raise Blocked('projection acceptance requires the formal nonrace binary')
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
    path = Path(root) / '.local/testlab/api-projection.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private regular projection lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another projection chain owns the fixed ports') from None
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
                raise Blocked('dedicated projection port is occupied: ' + str(port))
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
            # v1.1 page-interaction rows are tracked separately from the frozen 166-route contract.
            if match and 'v1.1·' not in line:
                self.main.add(match.groups())
        if len(self.main) != 166:
            raise Blocked('expected current 166-route main contract')
        for method, path in (*TARGETS, *SUPPORT):
            if method.lower() not in self.api['paths'].get(path, {}):
                raise Blocked('projection inventory is absent from the current public contract')

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
            raise Blocked('HTTP operation is outside the bounded projection inventory')
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

class Chain:

    def __init__(self, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != OWNER:
            raise Blocked('canonical run UUID and explicitly authorized owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database = 'aiops_testlab_projection_' + self.nonce
        self.db_user = 'projection_' + self.nonce
        self.marker = 'projectionfixture' + self.nonce
        self.owner_tag = OWNER + ':' + run_id + ':api-projection'
        self.work = self.root / '.local/testlab/environments' / OWNER / 'api-projection' / run_id
        if self.work.resolve() != self.work:
            raise Blocked('invocation evidence path traverses a symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid5(uuid.UUID(run_id), name).hex for name in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {name: 'projection-' + self.nonce + '-' + name for name in self.users}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.lock = threading.RLock()
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, binary_sha256=data['binary_sha256'],
                          source_tree_sha=data['source_tree_sha'], ports=list(PORTS), pids=[],
                          synthetic_input_scope='fresh identities/grants and explicit owned source/telemetry configuration only',
                          actual_results_source='HTTP service/native alert/case close/postmortem publish/Push receipt/mapping CAS and transactional audits',
                          model_scope='configured synthetic model; no inference/probe/embedding call')
        _private(self.work / 'state.json', self.state)
        _private(self.work / 'credentials.json', dict(database_password=self.db_password, password=self.password))
        self.contract = Contract(self.root)
        self.config_digest = None
        self.generation = 0
        self.source_id = 'projection-' + self.nonce
        self.files = ('push.json', 'operations.json', 'telemetry.json', 'source-token', 'push-token', 'push-hmac')

    def frozen(self):
        current_source = source_fingerprint(self.root)
        if self.config_digest is not None:
            check_config = hashlib.sha256(b''.join((self.work / x).read_bytes() for x in self.files)).hexdigest()
            if check_config != self.config_digest:
                raise Blocked('owned source/telemetry configuration changed during projection chain')
        if (current_source in ('unknown', '') or current_source != self.data['source_tree_sha'] or
                Path(self.data['binary']).is_symlink() or
                hashlib.sha256(Path(self.data['binary']).read_bytes()).hexdigest() != self.data['binary_sha256']):
            raise Blocked('formal source/binary changed during projection chain')

    def pg_application(self, kind, index=None):
        if kind == 'api' and index in (0, 1):
            return 'projection-api-' + self.nonce + '-' + str(index)
        if kind in ('sql', 'admin') and index is None:
            return 'projection-' + kind + '-' + self.nonce
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
                raise Blocked('projection cleanup exhausted its 40-second budget')
        guard_pg(inspect_pg(self.data['postgres_container'], min(timeout, 3)), OWNER, self.data['postgres_image_id'])
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
            if timeout <= 0:
                raise Blocked('projection cleanup exhausted its budget after PG inspection')
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
        _private(self.work / 'instance.json', dict(tier='nonprod', name='Projection acceptance ' + self.nonce,
            domain='testlab.local', default_env='test', environments=[
                dict(id='test', label='Test', kind='standing', tone='dev', clusters=[]),
                dict(id='staging', label='Staging', kind='standing', tone='stg', clusters=[])]))
        _private(self.work / 'models.json', dict(tier='nonprod', environments=[
            dict(id=env, models=['openai:projection-synthetic'], default_model='openai:projection-synthetic')
            for env in ('test', 'staging')]))
        _private(self.work / 'commands.json', [])
        emit('projection.fresh_database', 'passed', dict(database=self.database, user=self.db_user,
             owner=OWNER, owner_tag=self.owner_tag, synthetic_input=True, postgres_superuser=False,
             shared_app_database_touched=False))

    def start(self):
        generation = self.generation
        # Reserve before any acquisition/emission: a partial or cancelled launch
        # must not reuse its immutable readiness keys or private startup logs.
        self.generation += 1
        self.frozen()
        for index, port in enumerate(PORTS):
            with socket.socket() as sock:
                sock.settimeout(.3)
                if sock.connect_ex(('127.0.0.1', port)) == 0:
                    raise Blocked('projection API port occupied before launch')
            dsn = 'postgres://' + self.db_user + ':' + self.db_password + '@127.0.0.1:15461/' + self.database + '?' + urlencode(
                dict(sslmode='disable', options='-csearch_path=public', application_name=self.pg_application('api', index)))
            variables = dict(DATABASE_URL=dsn, AIOPS_V3_INSTANCE_CONFIG=str(self.work / 'instance.json'),
                AIOPS_ADDR='127.0.0.1:' + str(port), AIOPS_PUBLIC_ORIGIN=BASES[index], AIOPS_SECURE_COOKIES='false',
                AIOPS_ADMIN_EMAIL=self.admin_email, AIOPS_ADMIN_PASSWORD=self.password,
                AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32', AIOPS_V3_MODEL_BINDINGS_FILE=str(self.work / 'models.json'),
                AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.work / 'commands.json'),
                OPENAI_MODEL='projection-synthetic', OPENAI_API_KEY='synthetic-no-inference',
                OPENAI_BASE_URL='http://127.0.0.1:9/v1', EMBEDDING_PROVIDER='openai',
                EMBEDDING_MODEL='projection-synthetic', EMBEDDING_API_KEY='synthetic-no-embedding', EMBEDDING_BASE_URL='http://127.0.0.1:9/v1')
            if self.config_digest is not None:
                variables.update(AIOPS_OPERATIONS_CONFIG=str(self.work / 'operations.json'),
                    AIOPS_V3_PUSH_SOURCES_CONFIG=str(self.work / 'push.json'),
                    AIOPS_TELEMETRY_CONFIG=str(self.work / 'telemetry.json'))
            log = os.open(self.work / ('api-' + str(generation) + '-' + str(index) + '.log'), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
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
                        emit('projection.api_ready.' + str(generation) + '.' + str(index), 'passed', {'status': status, 'pid': proc.pid, 'port': port})
                        break
                except OSError:
                    pass
                time.sleep(.2)
            else:
                raise Blocked('fresh formal API startup deadline exceeded')
        self.frozen()

    def first_red(self, row):
        for path in (self.work / 'first-failure.json', self.root / '.local/testlab/first-failures' / ('api-projection-' + self.run_id + '.json')):
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            try:
                private_file(path, json.dumps(row, ensure_ascii=False))
            except FileExistsError:
                pass

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0,
             environment='test', validate=None, headers=None, purpose='target', conflict=None, raw_body=None):
        if index not in (0, 1) or environment not in ('test', 'staging', 'all') or user not in (None, 'admin', 'reader_old', *self.users):
            raise Blocked('projection account/origin/environment boundary mismatch')
        if scenario not in WIRE_SCENARIOS or purpose not in ('target', 'fixture', 'audit_evidence', 'dependency_unavailable', 'concurrency'):
            raise Blocked('projection scenario/purpose violates the Runner protocol')
        allowed_headers = {'Idempotency-Key'}
        if method == 'POST' and path == '/api/v1/events/change':
            allowed_headers |= {'Authorization', 'X-AIOps-Source', 'X-Timestamp', 'X-Signature'}
        if method == 'POST' and path == '/api/v1/webhooks/alertmanager/' + self.source_id:
            allowed_headers.add('Authorization')
        if headers and set(headers) - allowed_headers:
            raise Blocked('extra headers outside exact native producer boundary')
        if raw_body is not None and (method != 'POST' or path != '/api/v1/events/change' or user is not None or
                not isinstance(raw_body, bytes) or not 0 < len(raw_body) <= 64 << 10 or json.loads(raw_body) != body):
            raise Blocked('original signed bytes outside exact public Push producer boundary')
        canonical, route_class = self.contract.canonical(method, path)
        request_headers = {'Origin': BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en-US'}
        if user in self.cookies:
            request_headers['Cookie'] = self.cookies[user]
        request_headers.update(headers or {})
        if body is not None:
            request_headers['Content-Type'] = 'application/json'
        payload = raw_body if raw_body is not None else json.dumps(body).encode() if body is not None else None
        with self.lock:
            self.calls += 1
            number = self.calls
        expected = {'status': [want] if conflict is None else [want, conflict[0]], 'error_code': code}
        if conflict is not None:
            expected['conflict_error_code'] = conflict[1]
        row = dict(name='projection.http.' + str(number), method=method, path=canonical,
                   route_class=route_class, scenario=scenario, scenario_detail=purpose, expected=expected,
                   run_id=self.run_id, call_id=self.run_id + '-' + str(number), test_name='FormalProjectionChain',
                   status='failed', registration_evidence=False, actual={'status': None, 'error_code': None})
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
                if purpose == 'target' and scenario == 'normal':
                    check(validate is not None, 'target normal requires an explicit business-state validator')
                if validate:
                    validate(value)
            elif status >= 400:
                check(isinstance(value, dict) and isinstance(value.get('error'), dict) and
                      isinstance(actual_code, str) and isinstance(value['error'].get('message'), str),
                      'formal error DTO is invalid')
            row['status'] = 'passed'
            if method == 'GET' and canonical == '/api/v1/memory':
                row['proof_scope'] = 'legitimate empty scoped read with honest unavailable fields; no nonempty facts, producer, governance or recall acceptance'
            row['registration_evidence'] = (200 <= status < 300 and scenario == 'normal' and purpose == 'target'
                                            and (method, canonical[len('/api/v1'):]) in TARGETS)
        except BaseException as error:
            cancelled = isinstance(error, (Blocked, KeyboardInterrupt, SystemExit))
            status_name = 'blocked' if isinstance(error, Blocked) else 'interrupted' if cancelled else 'failed'
            row.update(status=status_name, registration_evidence=False)
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            if cancelled:
                row['actual']['cancellation_reason'] = str(error)
            def report(function, *args, **kwargs):
                try:function(*args, **kwargs)
                except BaseException as reporting_error:
                    row['actual'].setdefault('reporting_errors', []).append(type(reporting_error).__name__)
            report(self.first_red, row)
            if 'raw' in locals():
                report(_private, self.work / ('failure-response-' + str(number) + '.json'),
                         dict(status=status, request_body=body, response_body=raw.decode('utf-8', 'replace')))
            with self.lock:
                report(append_json, self.work / 'interfaces.jsonl', row)
                fields = {'interface': row} if type(row['actual']['status']) is int else {}
                report(emit, row['name'], status_name, row['actual'], **fields)
            if cancelled:
                raise
            raise AssertionError('formal projection HTTP/DTO assertion failed; private first red retained') from None
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

    def seed(self):
        """Synthetic accounts/grants only, before any HTTP business producer."""
        statements = ['BEGIN;']
        for name, uid in self.users.items():
            env, role = ('staging' if name == 'hidden' else 'test'), self.roles[name]
            statements += ['INSERT INTO users(id,email,name,role,password_hash) SELECT ' + quote(uid) + ',' +
                quote('member-' + self.nonce + '-' + name + '@testlab.invalid') + ',' + quote(self.marker + ' ' + name) +
                ",'viewer',password_hash FROM users WHERE email=" + quote(self.admin_email) + ';',
                'DELETE FROM rbac_user_roles WHERE user_id=' + quote(uid) + ';',
                'INSERT INTO rbac_roles(id,name,v3_kind) VALUES(' + quote(role) + ',' + quote(role) + ",'business');",
                'INSERT INTO rbac_user_roles(user_id,role_id) VALUES(' + quote(uid) + ',' + quote(role) + ');']
            grants = [('environment', 'view')]
            if name != 'denied':
                grants += [(m, 'view') for m in ('events', 'services', 'knowledge', 'connectors')]
            if name in ('reader', 'peer'):
                grants += [('knowledge', 'manage')]
            if name == 'reader':
                grants += [('events', 'change'), ('services', 'change'), ('connectors', 'manage')]
            for module, action in grants:
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                    ','.join(quote(v) for v in (role, module, action, 'environment', env)) + ');')
        statements += ['COMMIT;']
        self.sql('\n'.join(statements))
        actual = json.loads(self.sql("SELECT json_build_object('users',(SELECT count(*) FROM users WHERE id=ANY(ARRAY[" +
            ','.join(quote(x) for x in self.users.values()) + "])), 'applications',(SELECT count(*) FROM applications)," +
            "'business',(SELECT count(*) FROM v3_push_events)+(SELECT count(*) FROM v3_postmortems)+(SELECT count(*) FROM ops_incidents))", read=True))
        check(actual == {'users': 4, 'applications': 0, 'business': 0}, 'identity-only seed changed business state')
        emit('projection.identity_only_seed', 'passed', actual)

    def owned_prometheus(self):
        """Read-only fixed owned-component identity; never accept an arbitrary URL."""
        container = 'aiops-' + OWNER + '-prometheus'
        info = inspect_pg(container)
        config = info.get('Config', {})
        if (info.get('Name') != '/' + container or config.get('Labels', {}).get('aiops.testlab.owner') != OWNER or
                config.get('Image') != 'prom/prometheus:v3.13.3' or not info.get('State', {}).get('Running') or
                not str(info.get('Image', '')).startswith('sha256:') or
                info.get('NetworkSettings', {}).get('Ports', {}).get('9090/tcp') !=
                [{'HostIp': '127.0.0.1', 'HostPort': '18570'}]):
            raise Blocked('owned local Prometheus name/owner/image/exact listener is unavailable')
        return dict(name=container, image_id=info['Image'], url='http://127.0.0.1:18570',
                    proof='registered live owned component; mapping acceptance does not attest metric coverage')

    def stop_for_source_configuration(self):
        # No persisted/shared PID is signaled, and no configuration touches the base APIs.
        deadline = time.monotonic() + 20
        for proc in reversed(self.processes):
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=min(8, max(.1, deadline-time.monotonic())))
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=min(2, max(.1, deadline-time.monotonic())))
            check(proc.poll() is not None, 'owned setup process did not exit before reconfiguration')
        emit('projection.own_setup_stopped', 'passed', {'pids': [p.pid for p in self.processes], 'base_apis_touched': False})

    def configure_sources(self):
        self.service_key = 'projection-' + self.nonce
        self.call('admin', 'POST', '/api/v1/services', {'items': [dict(id=self.service_key, tier='T1')]},
                  want=201, purpose='fixture', validate=lambda v: check(v == {'created': 1}, 'service HTTP producer differs'))
        row = json.loads(self.sql("SELECT json_build_object('application_id',a.id,'environment_id',e.id,'binding_revision',e.binding_revision) " +
            'FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key=' + quote(self.service_key) +
            " AND e.key='test' AND NOT a.archived AND NOT e.archived", read=True))
        check(isinstance(row, dict) and all(isinstance(row.get(k), str) and row[k] for k in ('application_id', 'environment_id')) and
              type(row.get('binding_revision')) is int and row['binding_revision'] > 0, 'HTTP-created service scope/revision missing')
        self.service_row = row
        self.source_token, self.push_token, self.push_secret = (secrets.token_urlsafe(32) for _ in range(3))
        for file, value in (('source-token', self.source_token), ('push-token', self.push_token), ('push-hmac', self.push_secret)):
            private_file(self.work / file, value + '\n')
        _private(self.work / 'operations.json', {'sources': [dict(id=self.source_id, token_file=str(self.work / 'source-token'),
            mappings=[dict(labels={'projection_scope': self.nonce}, application_id=row['application_id'], environment_id=row['environment_id'])])], 'feishu_channels': []})
        _private(self.work / 'push.json', {'public_host': '', 'sources': [dict(id=self.source_id, provider='generic',
            application_id=row['application_id'], environment_id='test', token_file=str(self.work / 'push-token'),
            hmac_secret_file=str(self.work / 'push-hmac'), types=['change'], rate_per_minute=120)]})
        component = self.owned_prometheus()
        _private(self.work / 'telemetry.json', {'prometheus': {'url': component['url'], 'allow_http': True},
            'applications': {self.service_key: {'prometheus_queries': {'owned_up': 'up{job="testlab"}'}}}, 'timeout_seconds': 3})
        self.state.update(service=row, local_prometheus=component, source_id=self.source_id)
        _private(self.work / 'state.json', self.state)
        self.config_digest = hashlib.sha256(b''.join((self.work / x).read_bytes() for x in self.files)).hexdigest()
        self.frozen()
        self.stop_for_source_configuration()
        self.start()
        emit('projection.source_configuration', 'passed', {'scope': row, 'config_sha256': self.config_digest,
             'sql_business_writes': 0, 'source_secrets_generated': True, 'shared_configuration_modified': False})

    def postmortem(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        alert = {'status': 'firing', 'fingerprint': self.source_id, 'startsAt': (now-datetime.timedelta(minutes=1)).isoformat(),
            'labels': {'projection_scope': self.nonce, 'alertname': self.marker, 'severity': 'critical'},
            'annotations': {'summary': 'Controlled local functional alert; notifications disabled'}}
        self.call(None, 'POST', '/api/v1/webhooks/alertmanager/' + self.source_id, {'alerts': [alert]}, want=202,
            headers={'Authorization': 'Bearer ' + self.source_token}, purpose='fixture',
            validate=lambda v: check(v == {'accepted': 1, 'notification_delivery': 'not_configured'}, 'native alert acceptance differs'))
        incidents, _ = self.call('reader', 'GET', '/api/v1/incidents', purpose='fixture',
            validate=lambda v: check(len(v['items']) == 1 and v['counts']['all'] == 1, 'native alert producer is not nonempty/exact'))
        body = dict(title=self.marker+' case', severity='P1', env='test', services=[self.service_row['application_id']],
            alert_ids=[incidents['items'][0]['id']], war_room=False, notify=False, status_page=False, open_canvas=False)
        case, _ = self.call('reader', 'POST', '/api/v1/incidents', body, want=201, purpose='fixture',
            headers={'Idempotency-Key': self.source_id+'-case'})
        cid = case['id']
        self.call('reader', 'POST', '/api/v1/incidents/'+cid+'/status',
            dict(status='resolved', expected_revision=1, request_id=self.source_id+'-close', reason='Controlled local resolution'),
            want=201, index=1, purpose='fixture')
        closed, _ = self.call('reader', 'GET', '/api/v1/incidents/'+cid, purpose='fixture',
            validate=lambda v: check(v['status']=='resolved' and v['status_revision']==2 and v['resolved_status_event_id'] is not None,
                                     'case lacks actual CAS close-event evidence'))
        draft, _ = self.call('reader', 'GET', '/api/v1/postmortems/draft?'+urlencode({'incident': cid}), purpose='fixture')
        revision = draft.get('source_revision', '')
        check(re.fullmatch(r'case:[1-9][0-9]*', revision) is not None, 'draft lacks authoritative manual close revision')
        root_cause = 'Controlled local failure ' + self.nonce
        pm, _ = self.call('reader', 'POST', '/api/v1/postmortems', dict(incident_id=cid, source_revision=revision,
            sections=[dict(key='root', title='Root cause', text=root_cause)],
            actions=[dict(text='Follow up '+self.nonce, owner=self.users['reader'], due='2099-01-01')], status='review'),
            want=201, purpose='fixture')
        pmid = pm['id']
        self.call('reader', 'POST', '/api/v1/postmortems/'+pmid+'/publish', {}, want=403,
            code='self_review_forbidden', scenario='authorization', purpose='fixture')
        self.call('peer', 'POST', '/api/v1/postmortems/'+pmid+'/publish', {}, index=1, purpose='fixture',
            validate=lambda v: check(v == {'id': pmid, 'status': 'published'}, 'independent publication differs'))
        self.call('reader', 'GET', '/api/v1/postmortems?'+urlencode({'tab':'published','q':self.marker,'page':1,'page_size':1}),
            index=1, validate=lambda v: verify_postmortem(v, pmid, cid, root_cause))
        self.call('hidden', 'GET', '/api/v1/postmortems', environment='staging', scenario='authorization',
            validate=lambda v: check(v['items']==[] and v['total']==0, 'cross-environment retrospective leaked'))
        self.call('denied', 'GET', '/api/v1/postmortems', scenario='authorization',
            validate=lambda v: check(v['items']==[] and v['total']==0, 'denied retrospective leaked'))
        self.call('reader', 'GET', '/api/v1/postmortems?page=0', want=422, code='invalid_pagination', scenario='boundary')
        proof = json.loads(self.sql("SELECT json_build_object('close_event',(SELECT resolved_status_event_id FROM v3_incident_cases WHERE id="+
            quote(cid)+"),'source_event',(SELECT source_case_status_event_id FROM v3_postmortems WHERE id="+quote(pmid)+
            "),'publish_actor',(SELECT actor_id FROM management_audit WHERE target="+quote(pmid)+" AND action='postmortem.published'),"+
            "'publish_count',(SELECT count(*) FROM management_audit WHERE target="+quote(pmid)+" AND action='postmortem.published'))", read=True))
        check(proof['close_event'] == closed['resolved_status_event_id'] == proof['source_event'] and
              proof['publish_actor'] == self.users['peer'] and proof['publish_count']==1, 'close/source/independent audit lineage differs')
        self.ids.update(case=cid, postmortem=pmid)
        emit('projection.postmortem_lineage', 'passed', dict(proof,
            authority_limit='manual case has no authoritative start; duration unknown; summary proves action count, not action contents/completion'))

    def push(self):
        body = dict(schema='change.v1', event_id=self.source_id+'-event',
            occurred_at=datetime.datetime.now(datetime.timezone.utc).isoformat(), subject=self.service_key,
            title=self.marker+' change', state='succeeded', attributes={'source':'controlled-local-functional-input'})
        key = self.source_id+'-push'
        raw = json.dumps(body).encode()
        timestamp = str(int(time.time()))
        signature = push_signature(self.push_secret, timestamp, self.source_id, 'change', key, raw)
        headers = {'Authorization': 'Bearer '+self.push_token, 'X-AIOps-Source': self.source_id,
                   'X-Timestamp': timestamp, 'X-Signature': signature, 'Idempotency-Key': key}
        accepted, _ = self.call(None, 'POST', '/api/v1/events/change', body, want=202, headers=headers, raw_body=raw, purpose='fixture',
            validate=lambda v: check(v['accepted']==1 and type(v['receipt_id']) is int and v['receipt_id']>0, 'public ingress receipt missing'))
        self.call(None, 'POST', '/api/v1/events/change', body, want=409, code='duplicate', headers=headers, raw_body=raw,
                  index=1, scenario='idempotency', purpose='fixture')
        bad = dict(headers, **{'X-Signature': 'sha256='+'0'*64})
        self.call(None, 'POST', '/api/v1/events/change', body, want=401, code='unauthorized', headers=bad, raw_body=raw,
                  scenario='authorization', purpose='fixture')
        self.call('reader', 'GET', '/api/v1/ingest/push', index=1, validate=lambda v: verify_push(v, self.source_id))
        self.call('reader', 'GET', '/api/v1/webhooks', validate=lambda v: verify_webhooks(v, self.source_id))
        for path in ('/ingest/push', '/webhooks'):
            self.call('denied', 'GET', '/api/v1'+path, want=403, code='forbidden', scenario='authorization')
            self.call('hidden', 'GET', '/api/v1'+path, environment='staging', scenario='authorization',
                validate=lambda v: check(self.source_id not in json.dumps(v), 'cross-environment source/receipt leaked'))
        proof = json.loads(self.sql("SELECT json_build_object('events',(SELECT count(*) FROM v3_push_events WHERE source_id="+
            quote(self.source_id)+" AND NOT synthetic),'synthetic_events',(SELECT count(*) FROM v3_push_events WHERE source_id="+
            quote(self.source_id)+" AND synthetic),'receipts',(SELECT count(*) FROM v3_push_receipts WHERE source_id="+
            quote(self.source_id)+"),'accepted',(SELECT count(*) FROM management_audit WHERE actor_id="+quote(self.source_id)+
            " AND action='v3.push.accepted'),'duplicates',(SELECT count(*) FROM management_audit WHERE actor_id="+
            quote(self.source_id)+" AND action='v3.push.duplicate'))", read=True))
        check(proof == dict(events=1, synthetic_events=0, receipts=2, accepted=1, duplicates=1), 'public ingress transaction/idempotency proof differs')
        self.ids['push_receipt'] = accepted['receipt_id']
        emit('projection.push_native_receipts', 'passed', dict(proof,
            public_host_limit='public_host intentionally empty; receipt consumer evidence does not prove public endpoint reachability'))

    def mappings(self):
        rows, _ = self.call('reader', 'GET', '/api/v1/service-mappings', purpose='fixture')
        selected = [row for row in rows['rows'] if row['service']==self.service_key]
        check(len(selected)==1 and selected[0]['save']['editable'] is True, 'mapping save projection missing')
        initial = selected[0]['save']
        check(initial['service_id']==self.service_row['application_id'] and
              initial['service_environment_id']==self.service_row['environment_id'], 'mapping identity differs from HTTP service')
        binding = dict(system_id='metrics', kind='prometheus', connector_id='prometheus', resource=self.service_key)
        body = dict(environment_id='test', items=[dict(service_id=initial['service_id'],
            service_environment_id=initial['service_environment_id'], binding_revision=initial['binding_revision'], bindings=[binding])])
        def saved(v):
            check(v['environment_id']=='test' and len(v['items'])==1 and v['items'][0]['bindings']==[binding] and
                  v['items'][0]['binding_revision']==initial['binding_revision']+1, 'mapping CAS save differs')
        self.call('reader', 'PUT', '/api/v1/service-mappings', body, validate=saved)
        self.call('reader', 'GET', '/api/v1/service-mappings', index=1,
            validate=lambda v: verify_mapping(v, self.service_key, binding, initial['binding_revision']+1))
        self.call('reader', 'PUT', '/api/v1/service-mappings', body, want=409, code='stale_mapping',
                  scenario='idempotency', index=1)
        fresh = copy.deepcopy(body);fresh['items'][0]['binding_revision'] += 1
        self.call('peer', 'PUT', '/api/v1/service-mappings', fresh, want=403, code='forbidden', scenario='authorization')
        self.call('hidden', 'GET', '/api/v1/service-mappings', environment='staging', scenario='authorization',
                  validate=lambda v: check(v['rows']==[], 'cross-environment mapping leaked'))
        self.call('denied', 'GET', '/api/v1/service-mappings', scenario='authorization',
                  validate=lambda v: check(v['rows']==[], 'unauthorized mapping leaked'))
        self.call('reader', 'GET', '/api/v1/service-mappings',
                  validate=lambda v: verify_mapping(v, self.service_key, binding, initial['binding_revision']+1))
        proof = json.loads(self.sql("SELECT json_build_object('revision',(SELECT binding_revision FROM application_environments WHERE id="+
            quote(self.service_row['environment_id'])+"),'audits',(SELECT count(*) FROM management_audit WHERE target="+
            quote(self.service_row['environment_id'])+" AND action='v3.service_mapping.saved'),'actor',(SELECT actor_id FROM management_audit WHERE target="+
            quote(self.service_row['environment_id'])+" AND action='v3.service_mapping.saved'))", read=True))
        check(proof == dict(revision=initial['binding_revision']+1, audits=1, actor=self.users['reader']), 'mapping audit/revision changed after rejected writes')
        emit('projection.mapping_native_cas', 'passed', proof)

    def bulk_claim(self):
        groups, _ = self.call('reader', 'GET', '/api/v1/alert-groups', purpose='fixture')
        selected = [row for row in groups['items'] if row['source_id'] == self.source_id and
                    row['fingerprint'] == self.source_id]
        check(len(selected) == 1 and selected[0]['count'] == 1 and selected[0]['state'] == 'firing' and
              selected[0]['owner'] is None, 'native webhook group not exact/unclaimed/firing')
        gid = selected[0]['id']
        check(re.fullmatch(r'group:[0-9a-f]{24}', gid) is not None, 'public group identity invalid')
        body = dict(ids=[gid], action='claim')
        self.call('reader', 'POST', '/api/v1/alert-groups/bulk', body,
                  validate=lambda v: verify_bulk_claim(v, gid, 'claimed', 1))
        def readback(v):
            rows = [row for row in v['items'] if row['id'] == gid]
            check(len(rows) == 1 and rows[0]['owner'] == self.marker+' reader' and
                  rows[0]['count'] == 1 and rows[0]['state'] == 'firing', 'bulk claim owner/readback changed scope or alert state')
        self.call('reader', 'GET', '/api/v1/alert-groups', index=1, purpose='fixture', validate=readback)
        self.call('reader', 'POST', '/api/v1/alert-groups/bulk', body, index=1, scenario='idempotency',
                  validate=lambda v: verify_bulk_claim(v, gid, 'already_claimed', 0))
        def forbidden(v):
            check(v['action'] == 'claim' and len(v['results']) == 1 and
                  v['results'][0] == dict(id=gid, outcome='not_found', matched=0, changed=0,
                    investigation_id=None, proposal_id=None, error='not_found'), 'bulk denial leaked target or mutated state')
        self.call('peer', 'POST', '/api/v1/alert-groups/bulk', body, want=207,
                  scenario='authorization', validate=forbidden)
        self.call('reader', 'POST', '/api/v1/alert-groups/bulk', dict(ids=[gid,gid], action='claim'),
                  want=422, code='invalid_bulk_request', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/alert-groups', index=1, purpose='fixture', validate=readback)
        proof = json.loads(self.sql("SELECT json_build_object('members',(SELECT count(*) FROM ops_incidents WHERE source_id="+
            quote(self.source_id)+" AND assignee_id="+quote(self.users['reader'])+"),'audits',(SELECT count(*) FROM management_audit WHERE action='alert.claim' AND changes->>'alert_group_id'="+
            quote(gid)+"),'actor',(SELECT actor_id FROM management_audit WHERE action='alert.claim' AND changes->>'alert_group_id'="+quote(gid)+"))", read=True))
        check(proof == dict(members=1, audits=1, actor=self.users['reader']), 'bulk retry/denial added audit or changed assignment')
        emit('projection.bulk_claim_transaction', 'passed', dict(proof, group_id=gid,
             scope='claim only; no silence approval, upstream silence or investigation execution acceptance'))

    def memory_empty_read(self):
        before = json.loads(self.sql("SELECT json_build_object('facts',(SELECT count(*) FROM v3_memory_facts),"+
            "'conflicts',(SELECT count(*) FROM v3_memory_conflicts),'audits',(SELECT count(*) FROM management_audit))", read=True))
        check(before['facts'] == 0 and before['conflicts'] == 0, 'memory read cannot use fabricated fact/conflict seed')
        self.call('reader', 'GET', '/api/v1/memory', validate=verify_empty_memory)
        self.call('reader', 'GET', '/api/v1/memory', index=1, scenario='idempotency', validate=verify_empty_memory)
        self.call(None, 'GET', '/api/v1/memory', want=401, code='unauthorized', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/memory?service=one&service=two', want=422,
                  code='invalid_service', scenario='boundary')
        after = json.loads(self.sql("SELECT json_build_object('facts',(SELECT count(*) FROM v3_memory_facts),"+
            "'conflicts',(SELECT count(*) FROM v3_memory_conflicts),'audits',(SELECT count(*) FROM management_audit))", read=True))
        check(before == after, 'read-only memory success/rejection changed facts, conflicts or audit')
        emit('projection.memory_empty_read', 'passed', dict(after,
            authority_limit='legal empty read only; source-backed nonempty facts, fact production, governance and recall remain unproven'))

    def run(self):
        self.provision();self.start();self.seed()
        for name in ('admin', *self.users):
            self.login(name)
        self.configure_sources()
        for step in (self.postmortem, self.push, self.mappings, self.bulk_claim, self.memory_empty_read):
            self.frozen();step();self.frozen()
        rows = [json.loads(line) for line in (self.work/'interfaces.jsonl').read_text().splitlines()]
        positive = sorted({(row['method'],row['path']) for row in rows if row['registration_evidence']})
        check(set(positive)=={(m,'/api/v1'+p) for m,p in TARGETS}, 'seven exact normal targets not all evidenced')
        result = dict(run_id=self.run_id, database=self.database, http_results=self.calls, positive_routes=positive,
            main_positive=len(TARGETS), source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
            configuration_sha256=self.config_digest, sql_business_writes=0, external_notifications=False,
            model_calls=False, unproven=MISSING, performance_acceptance=False, continuity_24h_acceptance=False,
            postmortem_authority_limit='manual-case duration/MTTR unknown; summary action count only, not action contents/completion',
            push_public_host_limit='public_host intentionally empty; no public endpoint reachability acceptance',
            memory_authority_limit='legal empty read only; no nonempty facts, producer, governance or recall acceptance',
            bulk_authority_limit='native bulk claim/readback/retry only; no silence or investigation execution acceptance',
            unverified=['full JSON Schema validation', 'external providers', 'metric/service-health completeness', 'performance', '24-hour continuity'],
            local_component_scope='owned live Prom registration; mapping validation only, no metric or service-health completeness assertion')
        _private(self.work/'result.json', result)
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
                emit('projection.cleanup_timeout_resolved.' + phase, 'passed', detail, type='environment')
            except BaseException as error:
                self.cleanup_report_faults.append(error)
        if self.cleanup_report_faults:
            raise self.cleanup_report_faults[0]
        emit('projection.cleanup', 'passed', receipt)

def push_signature(secret, timestamp, source, event_type, key, original_bytes):
    if not isinstance(original_bytes, bytes) or event_type != 'change' or not re.fullmatch(r'[0-9]{10,11}', timestamp):
        raise Blocked('bounded canonical signed Push input required')
    material = (timestamp+'\n'+source+'\n'+event_type+'\n'+key+'\n').encode()+original_bytes
    return 'sha256='+hmac.new(secret.encode(), material, hashlib.sha256).hexdigest()


def verify_postmortem(value, pmid, incident, root_cause):
    check(value['total']==1 and value['page']==1 and value['page_size']==1 and len(value['items'])==1,
          'published retrospective summary is not exact/nonempty')
    item = value['items'][0]
    check(item['id']==pmid and item['incident_id']==incident and item['status']=='published' and
          item['workflow_status']=='published' and item['severity']=='P1' and item['root_cause']==root_cause and
          item['actions']=='unknown / 1',
          'published source/content/action summary differs')
    # This packet deliberately creates a human case, whose source mapping has
    # no authoritative started_at. Linked alert starts must not fabricate MTTR.
    check(item['duration'] is None and
          {'items.duration','reported_mttr','reported_mttr_coverage'}.issubset(value['unknown_fields']) and
          all(kpi.get('id')!='reported_mttr' for kpi in value['kpis']),
          'manual case fabricated duration/MTTR or omitted authority limitation')
    check(value['partial'] is True and value['complete_v3_dto'] is False and value['followups'] is None and
          'action_completion' in value['unknown_fields'] and all(x['series'] is None for x in value['kpis']),
          'retrospective summary fabricated unevidenced analytics')


def verify_push(value, source):
    change = [x for x in value['types'] if x['id']=='change']
    accepted = [x for x in value['deliveries'] if x['source']==source and str(x['code'])=='202']
    duplicates = [x for x in value['deliveries'] if x['source']==source and str(x['code'])=='409']
    check(len(change)==1 and change[0]['today']=='1' and len(accepted)==1 and len(duplicates)==1 and
          value['availability']=='endpoint_host_unconfigured' and accepted[0]['result']=='Accepted',
          'Push non-synthetic event/count/accepted/duplicate receipt projection differs')
    # The owned fixture deliberately leaves public_host unset. Persisted native
    # receipts are evidence of local ingress, not an advertised public address.
    endpoint = value.get('endpoint', {})
    check(endpoint.get('host')=='' and endpoint.get('method')=='POST' and
          endpoint.get('path')=='/api/v1/events/change',
          'Push unconfigured public endpoint projection differs')


def verify_bulk_claim(value, gid, outcome, changed):
    check(isinstance(value, dict) and value.get('action') == 'claim' and
          isinstance(value.get('results'), list) and len(value['results']) == 1, 'bulk claim response shape differs')
    check(value['results'][0] == dict(id=gid, outcome=outcome, matched=1, changed=changed,
          investigation_id=None, proposal_id=None, error=None), 'bulk claim exact effect/outcome differs')


def verify_empty_memory(value):
    check(isinstance(value, dict) and value.get('partial') is True and
          all(value.get(key) == [] for key in ('services', 'facts', 'conflicts')) and
          all(key in value and value[key] is None for key in ('service', 'conflict', 'policy')),
          'memory empty read fabricated content or completeness')
    check(value.get('unavailable_fields') == ['working_layer', 'episodic_layer', 'semantic_layer',
          'confidence', 'recalls', 'expires', 'policy'], 'memory unavailable capabilities not declared exactly')
    layers = value.get('layers')
    check(isinstance(layers, list) and len(layers) == 2 and
          [layer.get('id') for layer in layers] == ['service', 'workspace'] and
          all(layer.get('count') == 0 and layer.get('evidence') ==
              dict(kind='current_published_facts', fact_ids=[]) for layer in layers),
          'memory empty layer provenance/count differs')


def verify_webhooks(value, source):
    matches = [x for x in value['inbound'] if x['id']==source+':change']
    check(len(matches)==1 and matches[0]['source_id']==source and matches[0]['count']=='1' and
          matches[0]['status_basis']=='latest_receipt_in_window' and matches[0]['last'].startswith('409 · ') and
          len(value['deliveries'])==2 and {x['code'] for x in value['deliveries']}=={'202','409'},
          'Webhook non-synthetic accepted count/latest duplicate receipt projection differs')
    check(value['availability']['api_tokens']=='not_connected' and value['availability']['outbound']=='not_connected',
          'Webhook projection invented token registry or outbound delivery')


def verify_mapping(value, key, binding, revision):
    rows = [x for x in value['rows'] if x['service']==key]
    check(len(rows)==1 and rows[0]['save']['bindings']==[binding] and rows[0]['save']['binding_revision']==revision and
          rows[0]['save']['editable'] is True and rows[0]['cells']['metrics']==
          dict(state='ok', value=binding['resource'], brand='prometheus'),
          'native mapping/resource/revision projection differs')


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
        raise Blocked('projection process acquisition interrupted after retaining its owned handle')

@contextlib.contextmanager
def cooperative_signals():
    def stop(signum, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise Blocked('projection chain interrupted; invocation cleanup required')
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
            report(emit, 'projection.business', 'blocked' if isinstance(error, Blocked) else 'interrupted' if isinstance(error, (KeyboardInterrupt, SystemExit)) else 'failed', row)
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
                report(emit, 'projection.cleanup_failure', 'failed', row)
        finally:
            if registered:
                report(signal.signal, signal.SIGTERM, previous)
    if interrupted:
        row = dict(stage='cleanup_interrupted', cleanup_completed=cleanup is None)
        report(_private, chain.work / 'cleanup-interrupted.json', row)
        report(chain.first_red, row)
        report(emit, 'projection.cleanup_interrupted', 'blocked', row)
        primary = primary or Blocked('projection chain interrupted during bounded cleanup')
    if primary is not None:
        raise primary
    if cleanup is not None:
        raise cleanup
    if reporting is not None:
        raise reporting
    emit('projection.complete', 'passed', result)
    return result

def inventory(root):
    contract = Contract(root)
    return dict(module=MODULE, selected_route_count=len(TARGETS), total_operations_with_support=len(TARGETS) + len(SUPPORT),
        targets=[dict(method=m, path='/api/v1'+p, route_class='main' if (m,p) in contract.main else 'auxiliary_or_historical',
                      planned_evidence=('native_http_honest_empty_scoped_read_no_producer_acceptance' if p == '/memory' else 'native_http_nonempty_business_and_transactional_readback')) for m,p in TARGETS],
        support_routes=[list(x) for x in SUPPORT], ports=list(PORTS), owner=OWNER,
        fresh_database_prefix='aiops_testlab_projection_', missing_sources=MISSING,
        memory_authority_limit='legal empty read only; no nonempty facts, production, governance or recall acceptance',
        producer_scope='HTTP service registration, native Alertmanager webhook, manual case CAS close, independent postmortem review, public signed generic ingress, registered connector mapping CAS',
        exclusions=['synthetic Push test endpoint', 'SQL business or audit seed', 'Agent runtime/importer', 'personal authority simulation', 'full service health or history', 'external notifications/models', 'performance or 24h'],
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
        emit('projection.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        emit('projection.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0

if __name__ == '__main__':
    raise SystemExit(main())
