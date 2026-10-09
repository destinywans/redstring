"""Bounded formal V3 GET acceptance in a fresh, invocation-owned database.

SQL creates explicitly synthetic identities/grants only. Conversations and their
audit entries come from public HTTP; no SQL approval/audit/business result seed.
Empty catalog responses prove local reads, never external-source completeness.
Requires an explicit --live opt-in; the parent integrates/runs this work package.
"""
from __future__ import annotations

import argparse
import contextlib
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
import sqlite3
import stat
import subprocess
import time
import uuid
from urllib.parse import urlencode, urlsplit

from backend import control_proof
from env import PG_IMAGE, _private, _process_port
from faults import Blocked, private_json, validate_fixture, validate_identity
from runner import command_env, source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
PORTS = (18602, 18603)
BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)
CONTROL = 'http://127.0.0.1:18560'
LIMIT = 2 << 20
MODULE = 'formal-api-readonly'
BASELINE_RUNS = ('b8241602-b09f-47bd-a736-bdc0c3293c92',
                 'e8814ea6-326b-4a5c-b73b-1571a9998e57')
GETS = ('/me', '/me/watched-services', '/home', '/commands', '/models', '/models/admin',
        '/members', '/roles', '/conversations', '/mentions', '/investigation-templates',
        '/inbox', '/alert-groups', '/incidents', '/coverage', '/service-mappings', '/webhooks',
        '/agents/lifecycle', '/prompts', '/agent-tools', '/agent-runs', '/guardrails')
GRANTS = ('environment', 'conversation', 'canvas', 'home', 'members', 'roles', 'models',
          'services', 'connectors', 'events', 'agents', 'audit', 'workspace')
WIRE_SCENARIOS = {'normal', 'authorization', 'boundary', 'idempotency', 'failure'}
SCENARIO_ALIASES = {'fixture': 'normal', 'pagination': 'boundary', 'dependency_unavailable': 'failure'}


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
        raise Blocked('readonly acceptance requires the formal nonrace binary')
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
    path = Path(root) / '.local/testlab/api-readonly.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private regular readonly lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another readonly chain owns the fixed ports') from None
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
                raise Blocked('dedicated readonly port is occupied: ' + str(port))
    return dict(data, postgres_image_id=info['Image'],
                binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(), build=build,
                postgres_client_addresses=sorted(addresses))


class Contract:
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

    def canonical(self, method, path):
        raw = urlsplit(path).path
        if not raw.startswith('/api/v1/'):
            raise Blocked('business HTTP is restricted to the public API contract')
        short = raw[len('/api/v1'):]
        matches = []
        for pattern, operations in self.api['paths'].items():
            if method.lower() not in operations:
                continue
            expression = re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(pattern))
            if re.fullmatch(expression, short):
                matches.append(pattern)
        if short in matches:
            canonical = short
        elif len(matches) == 1:
            canonical = matches[0]
        else:
            raise Blocked('HTTP route lacks one canonical public contract operation')
        return '/api/v1' + canonical, 'main' if (method, canonical) in self.main else 'auxiliary_or_historical'

    def shape(self, method, path, status, value):
        canonical, _ = self.canonical(method, path)
        response = self.api['paths'][canonical[len('/api/v1'):]][method.lower()].get('responses', {}).get(str(status), {})
        schema = response.get('content', {}).get('application/json', {}).get('schema', {})
        if '$ref' in schema:
            schema = self.api['components']['schemas'][schema['$ref'].rsplit('/', 1)[-1]]
        # Deliberately a required-field smoke, not a complete JSON Schema claim.
        if schema.get('type') == 'object':
            check(isinstance(value, dict), 'contract response must be an object')
            missing = set(schema.get('required', [])) - set(value)
            check(not missing, 'contract response missing required top-level fields: ' + ','.join(sorted(missing)))


def positive_records(root):
    """Read Store without constructing it (its constructor performs migrations)."""
    path = Path(root) / '.local/testlab/state.sqlite3'
    if not path.is_file() or path.is_symlink():
        raise Blocked('existing private Store ledger is required')
    source = source_fingerprint(root)
    source_known = bool(re.fullmatch(r'[0-9a-f]{64}', source or ''))
    result = {'required_runs': {}, 'positive_routes': [], 'positive_runs': [],
              'source_tree_sha': source, 'historical_positive_routes': [], 'historical_positive_runs': []}
    routes, runs, historical_routes, historical_runs = set(), set(), set(), set()
    connection = sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True, timeout=5)
    try:
        states = {row[0]: json.loads(row[1]) for row in connection.execute('SELECT id,data FROM runs')}
        for run_id in BASELINE_RUNS:
            data = states.get(run_id, {})
            if data.get('status') != 'passed':
                raise Blocked('required Store baseline run is not passed')
            result['required_runs'][run_id] = {k: data.get(k) for k in ('status', 'sha', 'ended_at')}
        for run_id, raw in connection.execute("SELECT run_id,data FROM records WHERE kind='interfaces'"):
            if states.get(run_id, {}).get('status') != 'passed':
                continue
            row = json.loads(raw)
            if row.get('evidence_mode', 'real_local') != 'real_local':
                continue
            normal = row.get('scenarios', {}).get('normal', {})
            if not normal and row.get('scenario') == 'normal':
                normal = {'status': row.get('status'), 'actual_status': row.get('actual', {}).get('status')}
            status = normal.get('actual_status')
            if normal.get('status') == 'passed' and type(status) is int and 200 <= status < 300:
                route = (row['method'], row['path'].removeprefix('/api/v1'))
                historical_routes.add(route)
                historical_runs.add(run_id)
                # Historical success cannot suppress a current-source acceptance test.
                state = states[run_id]
                if (source_known and state.get('source_tree_sha') == source and
                        state.get('source_tree_sha_end') == source):
                    routes.add(route)
                    runs.add(run_id)
        result['positive_routes'] = [list(x) for x in sorted(routes)]
        result['positive_runs'] = sorted(runs)
        result['historical_positive_routes'] = [list(x) for x in sorted(historical_routes)]
        result['historical_positive_runs'] = sorted(historical_runs)
        result['scope'] = 'Completed passed-run normal real-local HTTP cells with current-source start/end binding; historical cells retained separately'
        return result
    finally:
        connection.close()


class Chain:
    def __init__(self, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != OWNER:
            raise Blocked('canonical run UUID and explicitly authorized owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database = 'aiops_testlab_readonly_' + self.nonce
        self.db_user = 'readonly_' + self.nonce
        self.marker = 'rofixture' + self.nonce
        self.owner_tag = OWNER + ':' + run_id + ':api-readonly'
        self.work = self.root / '.local/testlab/environments' / OWNER / 'api-readonly' / run_id
        if self.work.resolve() != self.work:
            raise Blocked('invocation evidence path traverses a symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid4().hex for name in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {name: 'readonly-' + self.nonce + '-' + name for name in self.users}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, binary_sha256=data['binary_sha256'],
                          source_tree_sha=data['source_tree_sha'], ports=list(PORTS), pids=[],
                          synthetic_input_scope='fresh users and least-privilege grants only',
                          actual_results_source='public conversations and their transactional audit entries',
                          model_scope='configured synthetic model; no inference/probe/embedding call')
        _private(self.work / 'state.json', self.state)
        _private(self.work / 'credentials.json', dict(database_password=self.db_password, password=self.password))
        self.contract = Contract(self.root)

    @classmethod
    def from_cleanup_record(cls, root, data, run_id):
        """Explicit operator recovery of one nonce; never adopt/signal saved PIDs."""
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != OWNER:
            raise Blocked('canonical run UUID and authorized owner required for recovery')
        root = Path(root).resolve()
        work = root / '.local/testlab/environments' / OWNER / 'api-readonly' / run_id
        if work.resolve() != work:
            raise Blocked('cleanup recovery evidence path traverses a symlink')
        saved = private_json(work / 'state.json')
        nonce = uuid.UUID(run_id).hex
        expected = dict(owner=OWNER, run_id=run_id, database='aiops_testlab_readonly_' + nonce,
                        database_user='readonly_' + nonce, owner_tag=OWNER + ':' + run_id + ':api-readonly',
                        ports=list(PORTS))
        if not isinstance(saved, dict) or any(saved.get(k) != v for k, v in expected.items()):
            raise Blocked('recorded cleanup nonce/database/user/owner boundary mismatch')
        for port in PORTS:
            with socket.socket() as sock:
                sock.settimeout(.3)
                if sock.connect_ex(('127.0.0.1', port)) == 0:
                    raise Blocked('cleanup recovery requires both dedicated API listeners to be absent')
        chain = cls.__new__(cls)
        chain.root, chain.data, chain.run_id, chain.nonce, chain.work = root, data, run_id, nonce, work
        chain.database, chain.db_user, chain.owner_tag = expected['database'], expected['database_user'], expected['owner_tag']
        chain.state, chain.processes = saved, []
        chain.role_attempted = chain.db_attempted = True
        chain.cleanup_recovery = True
        return chain

    def pg_application(self, kind, index=None):
        if kind == 'api' and index in (0, 1):
            return 'readonly-api-' + self.nonce + '-' + str(index)
        if kind in ('sql', 'admin') and index is None:
            return 'readonly-' + kind + '-' + self.nonce
        raise Blocked('unknown owned PostgreSQL application identity')

    def connection_origin(self, row):
        if (not isinstance(row, dict) or row.get('datname') != self.database or
                type(row.get('pid')) is not int or row['pid'] <= 1 or
                not isinstance(row.get('backend_start'), str) or
                not row['backend_start']):
            raise Blocked('unattributed backend in owned database; refusing cleanup wait')
        # pg_stat_activity includes PostgreSQL maintenance, not just client
        # sessions. This exact internal identity permits read-only waiting;
        # its PID is never treated as one of our API processes or signaled.
        if (row.get('backend_type') == 'autovacuum worker' and row.get('usename') is None and
                row.get('application_name') == '' and row.get('client_addr') is None and
                row.get('client_port') is None):
            return 'postgres_background'
        if row.get('backend_type') != 'client backend':
            raise Blocked('unattributed backend in owned database; refusing cleanup wait')
        application = row.get('application_name')
        user = row.get('usename')
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
        raise Blocked('foreign or legacy-unattributed application in owned database; refusing cleanup wait')

    def frozen(self):
        current_source = source_fingerprint(self.root)
        if (current_source in ('unknown', '') or current_source != self.data['source_tree_sha'] or
                Path(self.data['binary']).is_symlink() or
                hashlib.sha256(Path(self.data['binary']).read_bytes()).hexdigest() != self.data['binary_sha256']):
            raise Blocked('formal source/binary changed during readonly chain')

    def pg(self, statement, database='postgres', user='testlab', timeout=12, deadline=None):
        if database not in ('postgres', self.database) or user not in ('testlab', self.db_user):
            raise Blocked('SQL outside this invocation database/user boundary')
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
            if timeout <= 0:
                raise Blocked('readonly cleanup exhausted its 40-second budget')
        guard_pg(inspect_pg(self.data['postgres_container'], min(timeout, 3)), OWNER, self.data['postgres_image_id'])
        if deadline is not None:
            timeout = min(timeout, deadline - time.monotonic())
            if timeout <= 0:
                raise Blocked('readonly cleanup exhausted its budget after PG inspection')
        prefix = 'SET search_path=public; SET statement_timeout=8000; SET lock_timeout=2000; '
        if database == self.database:
            prefix += ('DO $ro$ BEGIN IF current_database()<>' + quote(self.database) + ' OR current_user<>' + quote(user) +
                       " OR current_schema()<>'public' OR current_setting('search_path')<>'public' THEN RAISE EXCEPTION 'owned SQL identity mismatch'; END IF; END $ro$; ")
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
            raise Blocked('fresh role/database already exists; refusing adoption or mutation')
        self.role_attempted = True
        self.pg('BEGIN; CREATE ROLE ' + identifier(self.db_user) + ' LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD ' +
                quote(self.db_password) + '; COMMENT ON ROLE ' + identifier(self.db_user) + ' IS ' + quote(self.owner_tag) + '; COMMIT;')
        self.db_attempted = True
        self.pg('CREATE DATABASE ' + identifier(self.database) + ' OWNER ' + identifier(self.db_user) + ' TEMPLATE template0;')
        self.pg('COMMENT ON DATABASE ' + identifier(self.database) + ' IS ' + quote(self.owner_tag) +
                '; REVOKE CONNECT,TEMPORARY ON DATABASE ' + identifier(self.database) + ' FROM PUBLIC;')
        self.pg('CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;', self.database)
        _private(self.work / 'instance.json', dict(tier='nonprod', name='Readonly acceptance ' + self.nonce,
                  domain='testlab.local', default_env='test', environments=[
                      dict(id='test', label='Test', kind='standing', tone='dev', clusters=[]),
                      dict(id='staging', label='Staging', kind='standing', tone='stg', clusters=[])]))
        _private(self.work / 'models.json', dict(tier='nonprod', environments=[
            dict(id=env, models=['openai:readonly-synthetic'], default_model='openai:readonly-synthetic') for env in ('test', 'staging')]))
        _private(self.work / 'commands.json', [dict(environment_id=env, id='readonly-' + env,
                  name='/readonly-context', module='conversation') for env in ('test', 'staging')])
        baseline = positive_records(self.root)
        _private(self.work / 'store-baseline.json', baseline)
        self.unproven = [path for path in GETS if ['GET', path] not in baseline['positive_routes']]
        self.state['selected_unproven_gets'] = self.unproven
        self.state['known_missing_source_reads'] = {'/me': {'status': 503, 'error_code': 'oncall_unavailable'}}
        self.state['necessary_existing_fixture_reads'] = ['/conversations/{id}']
        _private(self.work / 'state.json', self.state)
        emit('readonly.fresh_database', 'passed', dict(database=self.database, user=self.db_user, owner=OWNER,
             synthetic_input=True, postgres_superuser=False, shared_app_database_touched=False))

    def startup_environment(self):
        """Trusted test subclasses may add their explicitly guarded dependencies."""
        return {}

    def apply_startup_environment(self, variables):
        extra = self.startup_environment()
        if (not isinstance(extra, dict) or set(extra).intersection(variables) or
                any(not isinstance(key, str) or not re.fullmatch(r'[A-Z][A-Z0-9_]*', key) or
                    not isinstance(value, str) or '\x00' in value for key, value in extra.items())):
            raise Blocked('startup extension must preserve isolated database, address and model settings')
        return dict(variables, **extra)

    def start(self):
        self.frozen()
        for index, port in enumerate(PORTS):
            with socket.socket() as sock:
                sock.settimeout(.3)
                if sock.connect_ex(('127.0.0.1', port)) == 0:
                    raise Blocked('readonly API port occupied before launch')
            dsn = 'postgres://' + self.db_user + ':' + self.db_password + '@127.0.0.1:15461/' + self.database + '?' + urlencode(
                dict(sslmode='disable', options='-csearch_path=public', application_name=self.pg_application('api', index)))
            variables = dict(DATABASE_URL=dsn, AIOPS_V3_INSTANCE_CONFIG=str(self.work / 'instance.json'),
                AIOPS_ADDR='127.0.0.1:' + str(port), AIOPS_PUBLIC_ORIGIN=BASES[index], AIOPS_SECURE_COOKIES='false',
                AIOPS_ADMIN_EMAIL=self.admin_email, AIOPS_ADMIN_PASSWORD=self.password,
                AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32', AIOPS_V3_MODEL_BINDINGS_FILE=str(self.work / 'models.json'),
                AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.work / 'commands.json'),
                OPENAI_MODEL='readonly-synthetic', OPENAI_API_KEY='synthetic-no-inference',
                OPENAI_BASE_URL='http://127.0.0.1:9/v1', EMBEDDING_PROVIDER='openai',
                EMBEDDING_MODEL='readonly-synthetic', EMBEDDING_API_KEY='synthetic-no-embedding', EMBEDDING_BASE_URL='http://127.0.0.1:9/v1')
            variables = self.apply_startup_environment(variables)
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
                        emit('readonly.api_ready.' + str(index), 'passed', {'status': status, 'pid': proc.pid, 'port': port})
                        break
                except OSError:
                    pass
                time.sleep(.2)
            else:
                raise Blocked('fresh formal API startup deadline exceeded')
        self.frozen()

    def seed(self):
        statements = ['BEGIN;']
        for name, uid in self.users.items():
            env = 'staging' if name == 'hidden' else 'test'
            role = self.roles[name]
            statements.append('INSERT INTO users(id,email,name,role,password_hash) SELECT ' + quote(uid) + ',' +
                quote('member-' + self.nonce + '-' + name + '@testlab.invalid') + ',' + quote(self.marker + ' ' + name) +
                ",'viewer',password_hash FROM users WHERE email=" + quote(self.admin_email) + ';')
            statements.extend(['DELETE FROM rbac_user_roles WHERE user_id=' + quote(uid) + ';',
                'INSERT INTO rbac_roles(id,name,v3_kind) VALUES(' + quote(role) + ',' + quote(role) + ",'business');",
                'INSERT INTO rbac_user_roles(user_id,role_id) VALUES(' + quote(uid) + ',' + quote(role) + ');'])
            for module in (('environment',) if name == 'denied' else GRANTS):
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                  ','.join(quote(v) for v in (role, module, 'view', 'environment', env)) + ');')
            if name != 'denied':
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                  ','.join(quote(v) for v in (role, 'conversation', 'change', 'environment', env)) + ');')
        statements.append('COMMIT;')
        self.sql('\n'.join(statements))
        stored = json.loads(self.sql("SELECT json_build_object('users',(SELECT count(*) FROM users WHERE id=ANY(ARRAY[" +
                       ','.join(quote(x) for x in self.users.values()) + "])), 'global_grants',(SELECT count(*) FROM v3_role_grants WHERE role_id=ANY(ARRAY[" +
                       ','.join(quote(x) for x in self.roles.values()) + "]) AND scope_kind='global'))", read=True))
        check(stored == {'users': 4, 'global_grants': 0}, 'synthetic least-privilege fixture readback differs')
        emit('readonly.synthetic_identities', 'passed', stored)

    def first_red(self, row):
        for path in (self.work / 'first-failure.json', self.root / '.local/testlab/first-failures' / ('api-readonly-' + self.run_id + '.json')):
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            try:
                private_file(path, json.dumps(row, ensure_ascii=False))
            except FileExistsError:
                pass

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0,
             environment='test', validate=None):
        if index not in (0, 1) or environment not in ('test', 'staging', 'all') or user not in (None, 'admin', *self.users):
            raise Blocked('readonly account/origin/environment boundary mismatch')
        canonical, route_class = self.contract.canonical(method, path)
        wire_scenario = SCENARIO_ALIASES.get(scenario, scenario)
        if wire_scenario not in WIRE_SCENARIOS:
            raise Blocked('readonly HTTP scenario is not supported by the Runner protocol')
        headers = {'Origin': BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en'}
        if user in self.cookies:
            headers['Cookie'] = self.cookies[user]
        if body is not None:
            headers['Content-Type'] = 'application/json'
        payload = json.dumps(body).encode() if body is not None else None
        self.calls += 1
        expected = {'status': [want], 'error_code': code}
        if wire_scenario != scenario:
            expected['scenario_detail'] = scenario
        row = dict(name='readonly.http.' + str(self.calls), method=method, path=canonical,
                   route_class=route_class, scenario=wire_scenario, scenario_detail=scenario,
                   expected=expected, run_id=self.run_id,
                   call_id=self.run_id + '-' + str(self.calls), test_name='FormalReadOnlyChain')
        try:
            status, raw, response_headers = transport(BASES[index], method, path, payload, headers)
            row['actual'] = {'status': status, 'error_code': None}
            value = json.loads(raw) if raw else {}
            actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual'] = {'status': status, 'error_code': actual_code}
            check(status == want, 'formal HTTP status differs from exact expected status')
            check(code is None or actual_code == code, 'formal HTTP error code differs')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate:
                    validate(value)
            row['status'] = 'passed'
            row['registration_evidence'] = 200 <= status < 300 and scenario == 'normal'
        except Exception as error:
            row.setdefault('actual', {'status': None})
            row['status'] = 'failed'
            row['registration_evidence'] = False
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row)
            if 'raw' in locals():
                _private(self.work / ('failure-response-' + str(self.calls) + '.json'),
                         dict(status=status, response_body=raw.decode('utf-8', 'replace')))
            append_json(self.work / 'interfaces.jsonl', row)
            # A transport failure with no received HTTP status is a failed
            # test, not a fabricated HTTP cell rejected by actual_interface.
            fields = {'interface': row} if type(row['actual']['status']) is int else {}
            emit(row['name'], 'failed', row['actual'], **fields)
            raise AssertionError('formal readonly HTTP/DTO assertion failed; private first red retained') from None
        append_json(self.work / 'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row)
        return value, response_headers

    def login(self, name):
        env = 'staging' if name == 'hidden' else 'test'
        email = self.admin_email if name == 'admin' else 'member-' + self.nonce + '-' + name + '@testlab.invalid'
        _, headers = self.call(name, 'POST', '/api/v1/auth/login', {'email': email, 'password': self.password},
                               environment=env, scenario='fixture')
        cookie = next((v for k, v in headers.items() if k.lower() == 'set-cookie'), '').split(';', 1)[0]
        check(cookie.startswith('aiops_session=') and len(cookie) > 40 and '\r' not in cookie and '\n' not in cookie,
              'fresh formal login omitted a valid host-local session')
        self.cookies[name] = cookie

    def catalogs(self):
        self.call(None, 'GET', '/api/v1/instance', validate=lambda v: check(v.get('tier') == 'nonprod', 'instance tier differs'))
        def home(value):
            check(value['briefing'] is None and value['alert_trend'] is None and not value['recent'] and
                  value['availability']['service_metrics'] == 'not_connected', 'unconnected home fabricated a briefing/observation')
        def commands(value):
            check(len(value['items']) == 1 and value['items'][0]['id'] == 'readonly-test',
                  'text command catalog did not use its exact environment/permission binding')
        validators = {'/home': home, '/commands': commands,
                      '/me/watched-services': lambda v: check(not v['items'], 'fresh account unexpectedly has watched services'),
                      '/models': lambda v: check(len(v['items']) == 1, 'configured synthetic model catalog differs'),
                      '/models/admin': lambda v: check(len(v['items']) == 1, 'environment-bound model admin catalog differs')}
        for index, path in enumerate(self.unproven):
            if path == '/me':
                # /me requires an authoritative HTTPS on-call source in
                # v3settings.meEvidence. No such source is configured here.
                self.call('reader', 'GET', '/api/v1/me', want=503, code='oncall_unavailable',
                          scenario='dependency_unavailable', index=index % 2)
                continue
            self.call('reader', 'GET', '/api/v1' + path, index=index % 2, validate=validators.get(path))
        for path in ('/home', '/models/admin', '/members', '/roles'):
            self.call(None, 'GET', '/api/v1' + path, want=401, code='unauthorized', scenario='authorization')
            if path == '/home':
                # Composite home filters each section by its own grant. An
                # admitted reader gets an honest empty summary, not a 403.
                self.call('denied', 'GET', '/api/v1/home', scenario='authorization', index=1,
                          validate=lambda v: check(not v['recent'] and not v['attention'] and not v['services'],
                                                   'permissionless home exposed protected sections'))
            else:
                self.call('denied', 'GET', '/api/v1' + path, want=403, code='forbidden', scenario='authorization', index=1)
            self.call('reader', 'GET', '/api/v1' + path, want=403, code='forbidden', environment='staging', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/home?unknown=1', want=422, code='invalid_query', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/coverage?app=a&app=b', want=422, code='invalid_filter', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/investigation-templates?owner=invalid', want=422, scenario='boundary')
        self.call('reader', 'GET', '/api/v1/models/admin', environment='all', want=422, code='invalid_environment', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/me/profile', want=503, scenario='dependency_unavailable')
        self.call('admin', 'GET', '/api/v1/security', want=503, scenario='dependency_unavailable')
        self.call('reader', 'GET', '/api/v1/config-sync', want=501, code='config_repository_unavailable', scenario='dependency_unavailable')
        emit('readonly.catalog_scope', 'passed', dict(positive_scope='local persisted/configured catalog reads only',
             empty_agent_alert_registry=True, external_observation_coverage=False, conditionally_unavailable_success=False))

    def conversations(self):
        for user, count in (('reader', 3), ('peer', 1), ('hidden', 1)):
            for number in range(count):
                env = 'staging' if user == 'hidden' else 'test'
                value, _ = self.call(user, 'POST', '/api/v1/conversations',
                                     {'text': self.marker + ' ' + user + ' ' + str(number), 'model': 'openai:readonly-synthetic'},
                                     want=201, scenario='fixture', environment=env, index=number % 2)
                check(isinstance(value.get('id'), str) and value['id'], 'native create omitted an actual conversation id')
                self.ids[user + str(number)] = value['id']
        expected_ids = {self.ids['reader' + str(i)] for i in range(3)}
        pages = []
        for page, length in ((1, 2), (2, 1), (3, 0)):
            def validate(value, length=length, page=page):
                check(value.get('total') == 3 and value.get('page') == page and value.get('page_size') == 2,
                      'private conversation pagination metadata differs')
                check(isinstance(value.get('items'), list) and len(value['items']) == length,
                      'private conversation page size differs')
                check({x['id'] for x in value['items']} <= expected_ids, 'private conversation page disclosed another owner/environment')
            value, _ = self.call('reader', 'GET', '/api/v1/conversations?page=' + str(page) + '&page_size=2',
                                 scenario='pagination', index=(page - 1) % 2, validate=validate)
            pages.extend(x['id'] for x in value['items'])
        check(set(pages) == expected_ids and len(pages) == len(set(pages)), 'pagination dropped or duplicated native private conversations')
        self.call('reader', 'GET', '/api/v1/conversations?page=1&page_size=100', scenario='normal',
                  validate=lambda v: check(len(v['items']) == 3 and v['total'] == 3, 'maximum valid page differs'))
        for query, code in (('page=0', 'invalid_page'), ('page=100001', 'invalid_page'),
                            ('page_size=0', 'invalid_page_size'), ('page_size=101', 'invalid_page_size'),
                            ('page_size=invalid', 'invalid_page_size'), ('tab=invalid', 'invalid_filter'),
                            ('mode=invalid', 'invalid_mode'), ('status=invalid', 'invalid_status')):
            self.call('reader', 'GET', '/api/v1/conversations?' + query, want=422, code=code, scenario='boundary')
        self.call('reader', 'GET', '/api/v1/conversations?' + urlencode({'q': self.marker + ' reader 1'}),
                  validate=lambda v: check(v['total'] == 1 and v['items'][0]['id'] == self.ids['reader1'], 'conversation search differs'))
        for user in ('peer', 'hidden'):
            self.call('reader', 'GET', '/api/v1/conversations/' + self.ids[user + '0'], want=404, code='not_found', scenario='authorization', index=1)
        query = urlencode({'kind': 'conversation', 'q': self.marker})
        self.call('reader', 'GET', '/api/v1/mentions?' + query,
                  validate=lambda v: check({x['id'] for x in v['items']} == expected_ids, 'mention candidates violate private ownership'))
        self.call('reader', 'GET', '/api/v1/mentions/conversation/' + self.ids['reader0'] + '/preview')
        self.call('reader', 'GET', '/api/v1/mentions/conversation/' + self.ids['peer0'] + '/preview',
                  want=404, code='not_found', scenario='authorization')
        emit('readonly.native_conversations', 'passed', dict(native_created=5, own_visible=3, other_owner_excluded=1,
             other_environment_excluded=1, page_sizes=[2, 1, 0], inference_called=False))

    def directories(self):
        def members(value):
            visible = {x['id'] for x in value['items']}
            check(self.users['reader'] in visible and self.users['peer'] in visible and self.users['hidden'] not in visible,
                  'member directory did not enforce exact environment assignments')
            check(value.get('complete_v3_dto') is False and value.get('mfa_grace_days') is None and value['counts']['no_mfa'] is None,
                  'unconnected member MFA evidence was represented as complete')
        self.call('reader', 'GET', '/api/v1/members?' + urlencode({'q': self.marker}), validate=members, index=1)
        self.call('reader', 'GET', '/api/v1/members?filter=invalid', want=422, code='invalid_filter', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/members?filter=no_mfa', want=503, code='member_mfa_unavailable', scenario='dependency_unavailable')
        self.call('reader', 'GET', '/api/v1/roles', validate=lambda v: check(
            self.roles['reader'] in {x['id'] for x in v['items']} and self.roles['hidden'] not in {x['id'] for x in v['items']},
            'role list did not enforce selected environment'))
        self.call('reader', 'GET', '/api/v1/roles/' + self.roles['reader'], index=1,
                  validate=lambda v: check(v['id'] == self.roles['reader'] and v['kind'] == 'business', 'role detail identity/kind differs'))
        self.call('reader', 'GET', '/api/v1/roles/' + self.roles['hidden'], want=404, code='not_found', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/members/' + self.users['reader'], index=1)
        self.call('reader', 'GET', '/api/v1/members/' + self.users['hidden'], want=404, code='not_found', scenario='authorization')

    def audits(self):
        # These entries were generated by public POST /conversations, never SQL.
        query = urlencode({'q': self.marker})
        def validate(value):
            check(len(value['items']) == 4, 'native test-environment audit list differs')
            check(all(x.get('actor', {}).get('kind') == 'user' for x in value['items']), 'native conversation audit actor kind differs')
        value, _ = self.call('reader', 'GET', '/api/v1/audit?' + query, validate=validate, index=1)
        ids = [x['id'] for x in value['items']]
        check(all(isinstance(x, str) and x.isdigit() for x in ids), 'audit id is not a canonical persisted integer')
        first = max(ids, key=int)
        self.call('reader', 'GET', '/api/v1/audit?' + query + '&cursor=' + first, scenario='pagination',
                  validate=lambda v: check({x['id'] for x in v['items']} == set(ids) - {first}, 'audit cursor duplicated or dropped visible native rows'))
        self.call('reader', 'GET', '/api/v1/audit/' + first, index=1,
                  validate=lambda v: check(v['id'] == first and v['chain_ok'] is True, 'native sealed audit detail failed verification'))
        hidden = self.sql('SELECT id FROM management_audit WHERE target=' + quote(self.ids['hidden0']) +
                          " AND action='v3.conversation.create'", read=True)
        check(hidden.isdigit(), 'native hidden-environment audit source is absent or ambiguous')
        self.call('reader', 'GET', '/api/v1/audit/' + hidden, want=404, code='not_found', scenario='authorization')
        for suffix, code in (('cursor=0', 'invalid_cursor'), ('cursor=-1', 'invalid_cursor'),
                             ('cursor=invalid', 'invalid_cursor'), ('filter=invalid', 'invalid_filter')):
            self.call('reader', 'GET', '/api/v1/audit?' + suffix, want=422, code=code, scenario='boundary')
        self.call('reader', 'GET', '/api/v1/audit/not-an-id', want=404, code='not_found', scenario='boundary')
        emit('readonly.audit_source', 'passed', dict(source='native public conversation transaction audits',
             visible_test=4, excluded_staging=1, persisted_chain_verified=True, fabricated_audit_rows=0))

    def revocations(self):
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + quote(self.roles['reader']) + " AND module='models';")
        self.call('reader', 'GET', '/api/v1/models/admin', want=403, code='forbidden', scenario='authorization', index=1)
        self.sql('DELETE FROM sessions WHERE user_id=' + quote(self.users['peer']) + ';')
        self.call('peer', 'GET', '/api/v1/conversations', want=401, code='unauthorized', scenario='authorization', index=1)

    def run(self):
        self.provision()
        self.start()
        self.seed()
        for name in ('reader', 'peer', 'hidden', 'denied', 'admin'):
            self.login(name)
        for step in (self.catalogs, self.conversations, self.directories, self.audits, self.revocations):
            self.frozen()
            step()
        self.frozen()
        rows = [json.loads(line) for line in (self.work / 'interfaces.jsonl').read_text().splitlines()]
        positive = sorted({(x['method'], x['path'], x['route_class']) for x in rows
                           if x['method'] == 'GET' and x['status'] == 'passed' and x['scenario'] == 'normal'
                           and 200 <= x['actual']['status'] < 300})
        fixtures = sorted({(x['method'], x['path'], x['route_class']) for x in rows
                           if x.get('scenario_detail') == 'fixture' and x['status'] == 'passed'
                           and 200 <= x['actual']['status'] < 300})
        result = dict(database=self.database, http_results=self.calls, positive_routes=[list(x) for x in positive],
                      fixture_operations=[list(x) for x in fixtures],
                      main_positive=sum(x[2] == 'main' for x in positive),
                      auxiliary_positive=sum(x[2] != 'main' for x in positive),
                      permission_checks='least-privilege synthetic users; native current-session policy',
                      source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
                      unverified=['external sources', 'real IdP accounts', 'real MFA policy', 'nonempty Agent/alert registry semantics',
                                  'complete nested DTO/schema validation', 'performance', '24-hour continuity'])
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
                    state.get('database_owner') != self.db_user or state.get('database_tag') not in (None, self.owner_tag)):
                raise Blocked('fresh SQL database owner/tag changed; refusing cleanup')
            details = state.get('connection_details', [])
            if (not isinstance(details, list) or len(details) != state['connections'] or len(details) > 256 or
                    any(not isinstance(row, dict) or type(row.get('pid')) is not int for row in details) or
                    len({row['pid'] for row in details}) != len(details)):
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
                # Preserve the sanitized PG identities even when a foreign
                # connection prohibits waiting. Never collect query text.
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
                                      owned_pg_backend_pids=sorted(observed_pids), postgres_background_backend_pids=sorted(background_pids))
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
                emit('readonly.cleanup_timeout_resolved.' + phase, 'passed', detail, type='environment')
            except BaseException as error:
                self.cleanup_report_faults.append(error)
        if self.cleanup_report_faults:
            raise self.cleanup_report_faults[0]
        emit('readonly.cleanup', 'passed', receipt)


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
        raise Blocked('readonly process acquisition interrupted after retaining its owned handle')


@contextlib.contextmanager
def cooperative_signals():
    def stop(signum, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise Blocked('readonly chain interrupted; invocation cleanup required')
    previous = signal.signal(signal.SIGTERM, stop)
    try:
        yield
    finally:
        signal.signal(signal.SIGTERM, previous)


def execute(chain, cleanup_only=False):
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
            result = dict(cleanup_only=True, recovered_run=chain.run_id) if cleanup_only else chain.run()
        except BaseException as error:
            primary = error
            row = dict(stage='business_contract', error_type=type(error).__name__, error=str(error))
            report(chain.first_red, row)
            report(emit, 'readonly.business', 'blocked' if isinstance(error, Blocked) else 'failed', row)
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
                report(emit, 'readonly.cleanup_failure', 'failed', row)
        finally:
            if registered:
                report(signal.signal, signal.SIGTERM, previous)
    if interrupted:
        row = dict(stage='cleanup_interrupted', cleanup_completed=cleanup is None)
        report(_private, chain.work / 'cleanup-interrupted.json', row)
        report(chain.first_red, row)
        report(emit, 'readonly.cleanup_interrupted', 'blocked', row)
        primary = primary or Blocked('readonly chain interrupted during bounded cleanup')
    if primary is not None:
        raise primary
    if cleanup is not None:
        raise cleanup
    if reporting is not None:
        raise reporting
    emit('readonly.complete', 'passed', result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true', help='explicit parent-authorized real HTTP opt-in')
    parser.add_argument('--inventory', action='store_true', help='read-only Store comparison, without Docker or live HTTP')
    args = parser.parse_args()
    if args.inventory:
        contract, prior = Contract(args.root), positive_records(args.root)
        proposed = [path for path in GETS if ['GET', path] not in prior['positive_routes']]
        print(json.dumps(dict(required_runs=prior['required_runs'], proposed_unproven_gets=proposed,
                              main_routes=len(contract.main), source='read-only Store normal 2xx cells',
                              note='401/403/dependency failures and source inventory are not positive proof'), ensure_ascii=False))
        return 0
    if not args.live:
        parser.error('actual execution requires --live; this work package is offline-only until parent integration')
    try:
        with exclusive(args.root), cooperative_signals():
            data = doctor(args.root)
            chain = Chain(args.root, data, str(uuid.uuid4()))
            execute(chain)
    except Blocked as error:
        emit('readonly.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        emit('readonly.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
