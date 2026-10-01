"""Bounded mapping discovery/acceptance using real UUID PG/Vault and GitLab Mock.

The Mock exposes authenticated GET user/project only. No GitLab writes, CI/CD,
shared configuration changes, or SQL-created candidates/activation/audit results.
Mapping discovery consumes point-in-time activation evidence, not a fresh remote
query. Every target result is therefore protocol_mock, never real GitLab proof.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import copy
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import secrets
import socket
import ssl
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

import api_readonly_chain as readonly

ROOT = readonly.ROOT
OWNER = readonly.OWNER
MODULE = 'formal-api-connectors-mapping-functional'
PORTS = (18644, 18645)
BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)
MOCK_PORT = 18646
RESERVED_PORT = 18647
ENDPOINT = 'https://localhost:' + str(MOCK_PORT)
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SOURCE_SHA = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
VAULT_IMAGE = 'hashicorp/vault:1.20.4'
TARGETS = (('POST', '/service-mappings/discover'), ('POST', '/service-mappings/accept'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('GET', '/service-mappings'),
           ('POST', '/connectors'), ('GET', '/connectors/{id}'),
           ('POST', '/connectors/{id}/activate'), ('POST', '/connectors/{id}/test'),
           ('POST', '/services/{id}/external-targets'),
           ('DELETE', '/services/{id}/external-targets/{bindingId}'))
REQUIRED_CASES = ('mapping.vault_scope', 'mapping.activation_faults', 'mapping.nonempty_discovery',
                  'mapping.discovery_versions', 'mapping.authorization', 'mapping.boundaries',
                  'mapping.upstream_recovery', 'mapping.audit_rollback', 'mapping.accept_idempotency',
                  'mapping.audit_consistency', 'mapping.revocation', 'mapping.discovery_storage_failure')
check, Blocked = readonly.check, readonly.Blocked
q = readonly.quote


def emit(name, status, actual, **fields):
    fields.pop('module', None)
    fields.pop('evidence_mode', None)
    if 'interface' in fields:
        fields['interface'] = dict(fields['interface'], evidence_mode='protocol_mock',
                                   proof_scope='real UUID local PG/Vault; GitLab authenticated GET-only HTTPS protocol Mock; no real GitLab acceptance',
                                   test_name='FormalConnectorsMappingChain')
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='protocol_mock', **fields), ensure_ascii=False), flush=True)


def hashes():
    return {f: hashlib.sha256((ROOT / 'tools/testlab' / f).read_bytes()).hexdigest()
            for f in ('api_connectors_mapping_functional_coverage.py', 'api_readonly_chain.py')}


@contextlib.contextmanager
def context():
    old = readonly.PORTS, readonly.BASES, readonly.emit
    readonly.PORTS, readonly.BASES, readonly.emit = PORTS, BASES, emit
    try:
        yield
    finally:
        readonly.PORTS, readonly.BASES, readonly.emit = old


def ports_free():
    for port in (*PORTS, MOCK_PORT, RESERVED_PORT):
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1', port)) == 0:
                raise Blocked('owned mapping port occupied; no adoption: ' + str(port))


def guard_vault(info, pinned=None):
    if (not isinstance(info, dict) or info.get('Name') != '/aiops-' + OWNER + '-vault' or
            info.get('Config', {}).get('Labels', {}).get('aiops.testlab.owner') != OWNER or
            info.get('Config', {}).get('Image') != VAULT_IMAGE or not info.get('State', {}).get('Running') or
            info.get('NetworkSettings', {}).get('Ports', {}).get('8200/tcp') !=
            [{'HostIp': '127.0.0.1', 'HostPort': '18572'}] or
            not re.fullmatch('[a-f0-9]{64}', str(info.get('Id', ''))) or
            not re.fullmatch('sha256:[a-f0-9]{64}', str(info.get('Image', '')))):
        raise Blocked('actual Vault owner/CID/image/exact listener mismatch')
    identity = {'container_id': info['Id'], 'image_id': info['Image'], 'started_at': info['State']['StartedAt']}
    if pinned is not None and identity != pinned:
        raise Blocked('owned Vault immutable identity drift')
    return identity


def doctor(root):
    ports_free()
    with context():
        data = readonly.doctor(root)
    if data['source_tree_sha'] != SOURCE_SHA or data['binary_sha256'] != BINARY_SHA:
        raise Blocked('mapping package frozen Go/binary mismatch')
    if readonly.inspect_pg(data['postgres_container'])['Id'] != PG_CID:
        raise Blocked('owned PG immutable CID mismatch')
    component = readonly.private_json(Path(root) / '.local/testlab/component-environment.json')
    work = Path(root) / '.local/testlab/environments' / OWNER
    if component.get('owner') != OWNER or Path(component.get('work', '')).resolve() != work.resolve():
        raise Blocked('component owner/work mismatch')
    vault = guard_vault(readonly.inspect_pg('aiops-' + OWNER + '-vault'))
    for name in ('tls-cert.pem', 'tls-key.pem'):
        p = work / name
        if p.is_symlink() or not p.is_file() or p.stat().st_mode & 0o777 != 0o600:
            raise Blocked('owned private TLS material required')
    if not component.get('credentials', {}).get('vault_token'):
        raise Blocked('owned Vault provisioning credential missing')
    return dict(data, vault_identity=vault, component_work=str(work),
                vault_root_token=component['credentials']['vault_token'], helper_sha256=hashes())


class Contract(readonly.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment or not parsed.path.startswith('/api/v1/'):
            raise Blocked('formal namespace required')
        short = parsed.path[len('/api/v1'):]
        options = [p for m, p in (*TARGETS, *SUPPORT) if m == method and
                   re.fullmatch(re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(p)), short)]
        route = short if short in options else options[0] if len(options) == 1 else None
        if route is None or method.lower() not in self.api['paths'].get(route, {}):
            raise Blocked('operation outside bounded mapping contract')
        return '/api/v1' + route, 'main' if (method, route) in self.main else 'auxiliary_or_historical'


class GitLabProtocol:
    """No forwarding or arbitrary source, no writes, and no stored raw token."""
    def __init__(self, token):
        self.token = token
        self.mode = 'normal'
        self.calls = []
        self.lock = threading.Lock()

    def dispatch(self, method, path, token):
        if method != 'GET':
            return 405, {'message': 'read only'}, 'normal'
        if path not in ('/api/v4/user', '/api/v4/projects/101', '/api/v4/projects/102', '/api/v4/projects/103'):
            return 404, {'message': 'bounded protocol'}, 'normal'
        if not secrets.compare_digest(token or '', self.token):
            return 401, {'message': 'unauthorized'}, 'normal'
        with self.lock:
            mode = self.mode
            self.calls.append({'method': method, 'path': path, 'mode': mode,
                               'token_sha256': hashlib.sha256(token.encode()).hexdigest()})
        if mode in ('503', '429'):
            return int(mode), {'message': 'explicit protocol failure'}, mode
        if path.endswith('/user'):
            return 200, {'id': 1, 'username': 'owned-mock'}, mode
        project = int(path.rsplit('/', 1)[1])
        return 200, {'id': project + 1 if mode == 'wrong_project' else project,
                     'name': 'Explicit synthetic project ' + str(project)}, mode

    def snapshot(self):
        with self.lock:
            return copy.deepcopy(self.calls)


class GitLabServer:
    def __init__(self, protocol, cert, key):
        self.protocol, self.closed = protocol, False
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass
            def do_GET(self):
                if self.headers.get('Authorization') or self.headers.get('Cookie'):
                    self.send_error(400)
                    return
                status, value, mode = protocol.dispatch('GET', self.path, self.headers.get('PRIVATE-TOKEN'))
                if mode == 'timeout':
                    time.sleep(5.3)
                if mode == 'response_loss':
                    self.close_connection = True
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                raw = json.dumps(value).encode()
                try:
                    self.send_response(status)
                    self.send_header('Content-Length', str(len(raw)))
                    self.send_header('Connection', 'close')
                    self.end_headers()
                    self.wfile.write(raw)
                except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                    pass
            def do_POST(self):
                self.send_error(405)
            do_PUT = do_DELETE = do_PATCH = do_POST
        self.server = ThreadingHTTPServer(('127.0.0.1', MOCK_PORT), Handler)
        self.server.daemon_threads = True
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        self.server.socket = tls.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        if self.closed:
            return
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        check(not self.thread.is_alive() and self.server.socket.fileno() == -1, 'owned Mock listener remains')
        self.closed = True


def accept_body(candidates):
    keys = ('id', 'version', 'service_id', 'environment_id', 'system_id', 'binding_revision')
    return {'candidates': [{k: c[k] for k in keys} for c in candidates]}


def validate_discovery(value, expected):
    check(value.get('source_state') == 'configured' and value.get('suggestions') == len(expected),
          'discovery source/count differs')
    candidates = value.get('candidates')
    check(isinstance(candidates, list) and len(candidates) == len(expected), 'candidate array differs')
    actual = {c['service_id']: c for c in candidates}
    check(set(actual) == set(expected), 'discovery service scope differs')
    for app, config in expected.items():
        row = actual[app]
        check(all(row.get(k) == v for k, v in config.items()) and row.get('id') and row.get('version') and
              row.get('expires_at') and row.get('environment_id') == 'test' and row.get('system_id') == 'code',
              'candidate source/revision/identity differs')
    return candidates


def validate_accept(value, candidates, connector, accepted=True):
    check(value.get('accepted') == (len(candidates) if accepted else 0) and
          len(value.get('items', [])) == len(candidates), 'accepted count differs')
    items = {i['candidate_id']: i for i in value['items']}
    check(set(items) == {c['id'] for c in candidates}, 'accept response lost candidates')
    for c in candidates:
        row = items[c['id']]
        check(row == {'candidate_id': c['id'], 'service_id': c['service_id'], 'environment_id': 'test',
                      'system_id': 'code', 'connector_id': connector, 'target_id': c['target_id'],
                      'external_id': c['external_id'], 'binding_revision': c['binding_revision'] + 1,
                      'binding': {'kind': 'gitlab', 'connector_id': connector, 'resource': c['external_id']},
                      'status': 'accepted' if accepted else 'already_accepted'}, 'accepted DTO linkage differs')


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        # Reuse lifecycle methods, but keep all this package's files under its
        # exclusive directory; never create readonly's shared lease/work path.
        self.root, self.data, self.run_id = Path(root), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database, self.db_user = 'aiops_testlab_mapping_' + self.nonce, 'mapping_' + self.nonce
        self.owner_tag = OWNER + ':' + run_id + ':mapping-functional'
        self.work = self.root / '.local/testlab/connectors-parallel' / run_id
        if self.work.resolve() != self.work:
            raise Blocked('private package work path traverses a symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.marker, self.admin_email = 'mappingfixture' + self.nonce, 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {n: uuid.uuid4().hex for n in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {n: 'mapping-' + self.nonce + '-' + n for n in self.users}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.contract = Contract(root)
        self.connector = ''
        self.binding = []
        self.generation = 0
        self.vault_tokens, self.vault_policies = [], []
        self.vault_path = 'testlab-mapping/' + run_id
        self.ref = 'vault://secret/' + self.vault_path + '#token'
        self.secret = secrets.token_urlsafe(32)
        self.protocol = GitLabProtocol(self.secret)
        self.server = None
        self.apps = {}
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, ports=list(PORTS), pids=[],
                          source_tree_sha=data['source_tree_sha'], binary_sha256=data['binary_sha256'],
                          helper_sha256=data['helper_sha256'], evidence_mode='protocol_mock',
                          actual_results_source='formal HTTP service/target/activation/discovery/accept; real local PG and Vault',
                          proof_scope='GitLab only authenticated read Mock; no real GitLab or CI/CD acceptance',
                          vault_path=self.vault_path)
        readonly._private(self.work / 'state.json', self.state)
        readonly._private(self.work / 'credentials.json', dict(database_password=self.db_password, password=self.password))

    def frozen(self):
        if hashes() != self.data['helper_sha256']:
            raise Blocked('mapping helper source drift')
        super().frozen()

    def pg(self, *args, **kwargs):
        if readonly.inspect_pg(self.data['postgres_container'])['Id'] != PG_CID:
            raise Blocked('exact PG CID drift before SQL')
        return super().pg(*args, **kwargs)

    def marker_case(self, name, actual):
        check(name in REQUIRED_CASES, 'unknown stable mapping marker')
        self.frozen()
        emit(name, 'passed', dict(run_id=self.run_id, **actual))

    def call(self, user, method, path, *args, **kwargs):
        canonical, _ = self.contract.canonical(method, path)
        if (kwargs.get('scenario', 'normal') == 'normal' and (method, canonical[len('/api/v1'):]) in TARGETS
                and kwargs.get('want', 200) < 300 and kwargs.get('validate') is None):
            raise Blocked('normal mapping target requires nonempty business validation')
        def validate(value):
            raw = json.dumps(value)
            check(not any(s and s in raw for s in [self.secret, self.ref, self.data['vault_root_token'], *self.vault_tokens]),
                  'HTTP DTO exposed credential/reference')
            if kwargs.get('validate'):
                kwargs['validate'](value)
        options = dict(kwargs, validate=validate)
        value, headers = super().call(user, method, path, *args, **options)
        return value, headers

    def seed(self):
        super().seed()
        statements = ['BEGIN;']
        for user, env in (('peer', 'test'), ('hidden', 'staging')):
            for module, action in (('connectors', 'manage'), ('services', 'change')):
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                  ','.join(q(v) for v in (self.roles[user], module, action, 'environment', env)) + ');')
        for action in ('view', 'manage'):
            statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                              ','.join(q(v) for v in (self.roles['denied'], 'connectors', action, 'environment', 'test')) + ');')
        self.sql(' '.join(statements) + ' COMMIT;')

    def startup_environment(self):
        if not self.binding:
            return {}
        return dict(AIOPS_VAULT_URL='http://127.0.0.1:18572', AIOPS_VAULT_TOKEN=self.vault_tokens[0],
                    AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS=json.dumps(self.binding),
                    SSL_CERT_FILE=str(Path(self.data['component_work']) / 'tls-cert.pem'))

    def start(self):
        self.generation += 1
        original = readonly.emit
        def generation_emit(name, status, actual, **fields):
            if name.startswith('readonly.api_ready.'):
                name = 'mapping.api_ready.generation.' + str(self.generation) + '.instance.' + name.rsplit('.', 1)[1]
            original(name, status, actual, **fields)
        readonly.emit = generation_emit
        try:
            super().start()
        finally:
            readonly.emit = original

    def restart(self):
        for p in reversed(self.processes):
            if p.poll() is None:
                p.terminate()
                p.wait(timeout=10)
        readonly._private(self.work / ('generation-' + str(self.generation) + '-exits.json'),
                          {'held_pids': [p.pid for p in self.processes], 'exit_codes': [p.poll() for p in self.processes]})
        for i in (0, 1):
            (self.work / ('api-' + str(i) + '.log')).rename(self.work / ('bootstrap-' + str(i) + '.log'))
        self.start()

    def vault(self, method, path, body=None, token=None, want=200):
        paths = ('/v1/secret/data/' + self.vault_path, '/v1/secret/data/' + self.vault_path + '-sibling',
                 '/v1/secret/metadata/' + self.vault_path, '/v1/auth/token/create', '/v1/auth/token/revoke')
        if path not in paths and not (path.startswith('/v1/sys/policies/acl/') and
                                     path.rsplit('/', 1)[1] in self.vault_policies):
            raise Blocked('Vault path outside this UUID')
        guard_vault(readonly.inspect_pg('aiops-' + OWNER + '-vault'), self.data['vault_identity'])
        conn = http.client.HTTPConnection('127.0.0.1', 18572, timeout=8)
        try:
            conn.request(method, path, None if body is None else json.dumps(body).encode(),
                         {'X-Vault-Token': token if token is not None else self.data['vault_root_token'],
                          'Content-Type': 'application/json'})
            response = conn.getresponse()
            raw = response.read(65537)
            check(response.status == want and len(raw) <= 65536, 'owned Vault exact operation failed')
            return json.loads(raw) if raw else {}
        finally:
            conn.close()

    def prepare_vault(self):
        name = 'mapping-' + self.run_id
        self.vault_policies.append(name)
        readonly._private(self.work / 'vault-resources.json', {'policies': self.vault_policies, 'path': self.vault_path})
        self.vault('PUT', '/v1/sys/policies/acl/' + name,
                   {'policy': 'path "secret/data/' + self.vault_path + '" { capabilities = ["read"] }'}, want=204)
        token = self.vault('POST', '/v1/auth/token/create', {'policies': [name], 'no_default_policy': True,
                           'ttl': '30m', 'explicit_max_ttl': '30m', 'renewable': False})['auth']['client_token']
        self.vault_tokens.append(token)
        readonly._private(self.work / 'vault-token.json', {'tokens': self.vault_tokens})
        self.vault('POST', '/v1/secret/data/' + self.vault_path, {'data': {'token': self.secret}})
        check(self.vault('GET', '/v1/secret/data/' + self.vault_path, token=token)['data']['data']['token'] == self.secret,
              'actual Vault exact token field differs')
        self.vault('GET', '/v1/secret/data/' + self.vault_path + '-sibling', token=token, want=403)
        self.vault('POST', '/v1/secret/data/' + self.vault_path, {'data': {'token': 'forbidden'}}, token=token, want=403)
        self.marker_case('mapping.vault_scope', {'exact_path_read': True, 'sibling_read_denied': True, 'write_denied': True})
        cert = Path(self.data['component_work']) / 'tls-cert.pem'
        key = Path(self.data['component_work']) / 'tls-key.pem'
        self.server = GitLabServer(self.protocol, str(cert), str(key))

    def state_snapshot(self):
        query = "SELECT json_build_object('envs',(SELECT COALESCE(json_agg(json_build_object('id',id,'bindings',bindings,'revision',binding_revision) ORDER BY id),'[]'::json) FROM application_environments), 'candidates',(SELECT COALESCE(json_agg(row_to_json(c) ORDER BY id),'[]'::json) FROM v3_service_mapping_candidates c),'audit',(SELECT COALESCE(json_agg(row_to_json(a) ORDER BY id),'[]'::json) FROM management_audit a));"
        return json.loads(self.sql(query, read=True))

    def binding_snapshot(self):
        return json.loads(self.sql('SELECT COALESCE(json_agg(json_build_object(\'app\',application_id,\'env\',key,\'bindings\',bindings,\'revision\',binding_revision) ORDER BY id),\'[]\'::json) FROM application_environments', read=True))

    def fixture(self):
        for env, suffixes in (('test', ('a', 'b')), ('staging', ('hidden',))):
            for suffix in suffixes:
                key = 'mapping-' + self.nonce + '-' + suffix
                self.call('admin', 'POST', '/api/v1/services', {'items': [{'id': key, 'tier': 'T1'}]},
                          want=201, environment=env, scenario='fixture')
                row = json.loads(self.sql('SELECT json_build_object(\'app\',a.id,\'env_id\',e.id,\'revision\',e.binding_revision) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key=' + q(key) + ' AND e.key=' + q(env), read=True))
                row['key'] = key
                row['service_id'] = base64.urlsafe_b64encode((row['app'] + '\x00' + env).encode()).decode().rstrip('=')
                self.apps[suffix] = row
        created, _ = self.call('peer', 'POST', '/api/v1/connectors',
                               {'type': 'gitlab', 'url': ENDPOINT, 'auth': 'pat', 'credential_ref': self.ref,
                                'tools': [], 'webhook': False}, want=201, scenario='fixture')
        self.connector = created['id']
        for suffix, project in (('a', '101'), ('b', '102')):
            row = self.apps[suffix]
            target, _ = self.call('peer', 'POST', '/api/v1/services/' + row['service_id'] + '/external-targets',
                                  {'connector_id': self.connector, 'target_kind': 'project', 'external_id': project},
                                  want=201, scenario='fixture')
            row['target_id'], row['project'] = target['id'], project
        before = self.protocol.snapshot()
        self.call('peer', 'POST', '/api/v1/connectors/' + self.connector + '/activate', {}, want=503,
                  code='credential_not_authorized', scenario='authorization')
        check(self.protocol.snapshot() == before, 'unbound credential reached upstream')
        self.binding = [dict(tier='nonprod', environment_id='test', connector_id=self.connector,
                             provider='gitlab', endpoint=ENDPOINT, credential_ref=self.ref)]
        self.restart()

    def expected(self):
        return {r['app']: dict(service=r['key'], connector_id=self.connector, target_id=r['target_id'],
                              external_id=r['project'], binding_revision=r['revision'])
                for k, r in self.apps.items() if k in ('a', 'b')}

    def activation(self):
        path = '/api/v1/connectors/' + self.connector
        upstream = self.protocol.snapshot()
        self.vault('POST', '/v1/secret/data/' + self.vault_path, {'data': {'other': 'explicit missing field fault'}})
        try:
            self.call('peer', 'POST', path + '/activate', {}, want=503,
                      code='credential_unavailable', scenario='failure')
            check(self.protocol.snapshot() == upstream, 'missing Vault field reached GitLab')
        finally:
            self.vault('POST', '/v1/secret/data/' + self.vault_path, {'data': {'token': self.secret}})
        for mode in ('503', '429', 'wrong_project', 'timeout', 'response_loss'):
            self.protocol.mode = mode
            before = len(self.protocol.snapshot())
            started = time.monotonic()
            self.call('peer', 'POST', path + '/activate', {}, want=502, code='project_unavailable', scenario='failure')
            elapsed = time.monotonic() - started
            row = json.loads(self.sql('SELECT json_build_object(\'enabled\',enabled,\'status\',status) FROM v3_connectors WHERE id=' + q(self.connector), read=True))
            check(row['enabled'] is False and len(self.protocol.snapshot()) == before + 1, 'failed activation enabled/retried projects')
            readonly.append_json(self.work / 'activation-faults.jsonl', dict(mode=mode, elapsed_seconds=elapsed, requests=1))
        self.protocol.mode = 'normal'
        before = self.state_snapshot()
        upstream = len(self.protocol.snapshot())
        self.sql(self.audit_fault('connector.activated', self.connector))
        try:
            self.call('peer', 'POST', path + '/activate', {}, want=503,
                      code='audit_unavailable', scenario='failure')
        finally:
            self.sql('DROP TRIGGER mapping_audit_fault ON management_audit; DROP FUNCTION mapping_audit_fault();')
        check(before == self.state_snapshot() and len(self.protocol.snapshot()) == upstream + 2,
              'successful project reads/local audit failure committed activation')
        check(self.sql('SELECT enabled FROM v3_connectors WHERE id=' + q(self.connector), read=True) == 'f',
              'failed local activation audit enabled source')
        self.call('peer', 'POST', path + '/activate', {}, scenario='fixture', validate=lambda v:
                  check(v == {'enabled': True, 'status': 'ok', 'verified_projects': 2, 'webhook_active': False}, 'activation DTO differs'))
        self.call('peer', 'POST', path + '/activate', {}, want=409, code='connector_enabled', scenario='idempotency')
        self.marker_case('mapping.activation_faults', {'faults': ['503', '429', 'wrong_project', 'timeout', 'response_loss'],
                                                       'project_verifications': 2, 'remote_writes': 0,
                                                       'successful_read_local_audit_failure': True})

    def discovery_storage_failure(self):
        before = self.state_snapshot()
        app = self.apps['a']['app']
        self.sql("CREATE FUNCTION mapping_candidate_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF " +
                 "NEW.application_id=" + q(app) + " AND NEW.environment_id='test' THEN RAISE EXCEPTION " +
                 "'UUID candidate storage fault'; END IF; RETURN NEW; END $$; " +
                 'CREATE TRIGGER mapping_candidate_fault BEFORE INSERT OR UPDATE ON v3_service_mapping_candidates '
                 'FOR EACH ROW EXECUTE FUNCTION mapping_candidate_fault();')
        try:
            self.call('peer', 'POST', '/api/v1/service-mappings/discover', want=503,
                      code='catalog_unavailable', scenario='failure')
        finally:
            self.sql('DROP TRIGGER mapping_candidate_fault ON v3_service_mapping_candidates; DROP FUNCTION mapping_candidate_fault();')
        check(before == self.state_snapshot(), 'discovery storage failure committed partial candidates')
        self.marker_case('mapping.discovery_storage_failure', {'exact_app_fault': True, 'batch_atomic_rollback': True})

    def discover(self, scenario='normal', index=0):
        before = self.binding_snapshot()
        upstream = self.protocol.snapshot()
        value, _ = self.call('peer', 'POST', '/api/v1/service-mappings/discover', scenario=scenario,
                             index=index, validate=lambda v: validate_discovery(v, self.expected()))
        check(before == self.binding_snapshot() and upstream == self.protocol.snapshot(), 'discovery wrote mapping/called upstream')
        return value['candidates']

    def permissions_boundaries(self, candidates):
        before = self.state_snapshot()
        upstream = self.protocol.snapshot()
        body = accept_body(candidates)
        for method, path, request in (('POST', '/api/v1/service-mappings/discover', None),
                                      ('POST', '/api/v1/service-mappings/accept', body)):
            self.call(None, method, path, request, want=401, code='unauthorized', scenario='authorization')
            self.call('reader', method, path, request, want=403, code='forbidden', scenario='authorization')
            self.call('peer', method, path, request, environment='all', want=409, code='env_view_only', scenario='boundary')
        self.call('hidden', 'POST', '/api/v1/service-mappings/discover', environment='staging', scenario='authorization',
                  validate=lambda v: check(v == {'suggestions': 0, 'source_state': 'not_configured', 'candidates': []}, 'staging discovery leaked test'))
        self.call('hidden', 'POST', '/api/v1/service-mappings/accept', body, environment='staging',
                  want=422, code='invalid_candidates', scenario='authorization')
        self.call('denied', 'POST', '/api/v1/service-mappings/discover', scenario='authorization',
                  validate=lambda v: check(v == {'suggestions': 0, 'source_state': 'not_configured', 'candidates': []},
                                          'connector manager without service authority saw candidates'))
        self.call('denied', 'POST', '/api/v1/service-mappings/accept', body,
                  want=404, code='not_found', scenario='authorization')
        self.call('peer', 'POST', '/api/v1/service-mappings/discover', {}, want=422, code='invalid_body', scenario='boundary')
        invalids = [[], body['candidates'] * 2]
        for invalid in invalids:
            self.call('peer', 'POST', '/api/v1/service-mappings/accept', {'candidates': invalid},
                      want=422, code='invalid_candidates', scenario='boundary')
        for field, replacement in (('version', 'invalid'), ('binding_revision', 0), ('system_id', 'metrics')):
            changed = copy.deepcopy(body)
            changed['candidates'][0][field] = replacement
            self.call('peer', 'POST', '/api/v1/service-mappings/accept', changed, want=422,
                      code='invalid_candidates', scenario='boundary')
        check(self.state_snapshot() == before and self.protocol.snapshot() == upstream, 'early rejection changed DB/upstream')
        self.marker_case('mapping.authorization', {'unauthenticated': 2, 'reader_forbidden': 2, 'staging_isolated': True,
                                                   'connector_manager_without_service_access_hidden': True})
        self.marker_case('mapping.boundaries', {'all_write_rejected': True, 'invalid_fields': 3, 'empty_duplicate_rejected': True})

    def upstream_recovery(self, candidates):
        before = self.binding_snapshot()
        self.protocol.mode = '503'
        self.call('peer', 'POST', '/api/v1/connectors/' + self.connector + '/test', {},
                  want=503, code='connector_test_failed', scenario='failure')
        self.call('peer', 'POST', '/api/v1/service-mappings/accept', accept_body(candidates),
                  want=409, code='stale_mapping', scenario='failure')
        self.call('peer', 'POST', '/api/v1/service-mappings/discover', scenario='failure',
                  validate=lambda v: check(v['suggestions'] == 0 and v['candidates'] == [], 'unhealthy source produced candidate'))
        check(before == self.binding_snapshot(), 'unhealthy mapping mutated bindings')
        self.protocol.mode = 'normal'
        self.call('peer', 'POST', '/api/v1/connectors/' + self.connector + '/test', {}, scenario='fixture',
                  validate=lambda v: check(v['ok'] is True, 'read probe did not recover'))
        fresh = self.discover(index=1)
        self.marker_case('mapping.upstream_recovery', {'unhealthy_accept_rejected': True, 'fresh_candidates': len(fresh),
                                                       'discovery_not_remote_health_check': True})
        return fresh

    def audit_fault(self, action, target, application=None):
        guard = 'NEW.actor_id=' + q(self.users['peer']) + ' AND NEW.action=' + q(action) + ' AND NEW.target=' + q(target)
        if application:
            guard += ' AND NEW.application_id=' + q(application)
        return ("CREATE FUNCTION mapping_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF " + guard +
                " THEN RAISE EXCEPTION 'UUID mapping audit fault'; END IF; RETURN NEW; END $$; "
                'CREATE TRIGGER mapping_audit_fault BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION mapping_audit_fault();')

    def accept_with_failure(self, candidates):
        before = self.state_snapshot()
        upstream = self.protocol.snapshot()
        first = self.apps['a']
        self.sql(self.audit_fault('v3.service_mapping.accepted', first['env_id'], first['app']))
        try:
            self.call('peer', 'POST', '/api/v1/service-mappings/accept', accept_body(candidates),
                      want=503, code='audit_unavailable', scenario='failure')
        finally:
            self.sql('DROP TRIGGER mapping_audit_fault ON management_audit; DROP FUNCTION mapping_audit_fault();')
        check(self.state_snapshot() == before and self.protocol.snapshot() == upstream, 'audit failure committed partial mapping/accepted row')
        self.marker_case('mapping.audit_rollback', {'batch_size': len(candidates), 'bindings_candidates_audit_unchanged': True,
                                                  'remote_writes': 0, 'external_success_local_fail_applicability': 'mapping has no external write'})
        self.call('peer', 'POST', '/api/v1/service-mappings/accept', accept_body(candidates),
                  validate=lambda v: validate_accept(v, candidates, self.connector))
        after = self.state_snapshot()
        self.call('peer', 'POST', '/api/v1/service-mappings/accept', accept_body(candidates), index=1,
                  scenario='idempotency', validate=lambda v: validate_accept(v, candidates, self.connector, False))
        check(after == self.state_snapshot() and self.protocol.snapshot() == upstream, 'idempotent accept changed DB/upstream')
        self.marker_case('mapping.accept_idempotency', {'accepted': len(candidates), 'repeat_accepted': 0, 'other_instance': True})

    def audits(self, candidates):
        data = self.state_snapshot()
        accepted = [a for a in data['audit'] if a['action'] == 'v3.service_mapping.accepted']
        check(len(accepted) == len(candidates), 'exact accepted audit count differs')
        for c in candidates:
            app = next(r for r in self.apps.values() if r['app'] == c['service_id'])
            row = next(a for a in accepted if a['application_id'] == c['service_id'])
            expected = {'environment': 'test', 'candidate_id': c['id'], 'source_kind': 'explicit_target',
                        'source_id': c['target_id'], 'connector_id': self.connector, 'project_id': c['external_id'],
                        'binding_revision_before': c['binding_revision'], 'binding_revision_after': c['binding_revision'] + 1}
            check(row['actor_id'] == self.users['peer'] and row['target'] == app['env_id'] and
                  row['outcome'] == 'success' and row['changes'] == expected, 'audit actor/app/env/source/revision linkage differs')
            saved = next(e for e in data['envs'] if e['id'] == app['env_id'])
            check(saved['revision'] == c['binding_revision'] + 1 and
                  saved['bindings'] == [{'kind': 'gitlab', 'connector_id': self.connector, 'resource': c['external_id']}],
                  'persisted mapping/revision differs')
            candidate = next(x for x in data['candidates'] if x['id'] == c['id'])
            check(candidate['accepted_by'] == self.users['peer'] and candidate['accepted_at'], 'candidate acceptance provenance differs')
        check(not any(s in json.dumps(data['audit']) for s in (self.secret, self.ref, *self.vault_tokens)), 'audit exposed secrets')
        self.call('peer', 'GET', '/api/v1/service-mappings', index=1, scenario='fixture', validate=lambda v:
                  check(len([r for r in v['rows'] if r['service'] in {self.apps['a']['key'], self.apps['b']['key']} and
                             r['cells']['code']['state'] == 'ok']) == 2, 'cross-instance persisted matrix readback failed'))
        readonly._private(self.work / 'business-readback.json', data)
        self.marker_case('mapping.audit_consistency', {'accepted_audits': len(accepted), 'exact_binding_revision_linkage': True,
                                                      'sql_business_seed': False, 'secrets_redacted': True})

    def revocation(self, candidates):
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + q(self.roles['peer']) + " AND module='connectors' AND action='manage';")
        before = self.state_snapshot()
        for path, body in (('/api/v1/service-mappings/discover', None), ('/api/v1/service-mappings/accept', accept_body(candidates))):
            self.call('peer', 'POST', path, body, index=1, want=403, code='forbidden', scenario='authorization')
        check(before == self.state_snapshot(), 'revoked mapping request changed state')
        self.marker_case('mapping.revocation', {'existing_session_other_instance': True, 'requests_denied': 2})

    def run(self):
        self.provision()
        self.start()
        self.seed()
        for name in ('reader', 'peer', 'hidden', 'denied', 'admin'):
            self.login(name)
        self.prepare_vault()
        self.fixture()
        self.activation()
        self.discovery_storage_failure()
        initial = self.discover()
        self.marker_case('mapping.nonempty_discovery', {'candidates': len(initial), 'unmapped_bindings_unchanged': True})
        fresh = self.discover(scenario='idempotency', index=1)
        check({c['id'] for c in initial} == {c['id'] for c in fresh} and
              all(next(c for c in fresh if c['id'] == old['id'])['version'] != old['version'] for old in initial),
              'rediscovery did not preserve ID/advance review version')
        self.call('peer', 'POST', '/api/v1/service-mappings/accept', accept_body(initial),
                  want=409, code='stale_mapping', scenario='idempotency')
        self.marker_case('mapping.discovery_versions', {'old_snapshot_rejected': True, 'candidate_ids_preserved': True})
        self.permissions_boundaries(fresh)
        fresh = self.upstream_recovery(fresh)
        self.accept_with_failure(fresh)
        self.audits(fresh)
        self.revocation(fresh)
        self.frozen()
        result = dict(run_id=self.run_id, http_calls=self.calls, candidates=2, accepted=2,
                      source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
                      evidence_mode='protocol_mock', actual_gitlab_verified=False, real_pg_vault=True,
                      required_cases=list(REQUIRED_CASES), protocol_calls=self.protocol.snapshot(),
                      unverified=['real GitLab permissions/tenancy', 'concurrent lock-wait expiry/revocation',
                                  'natural 15m expiry', '500-source cap', 'performance/24h', 'CI/CD'])
        readonly._private(self.work / 'result.json', result)
        return result

    def cleanup(self):
        failures = []
        try:
            super().cleanup()
        except BaseException as error:
            failures.append(error)
        if self.server:
            try:
                self.server.close()
            except BaseException as error:
                failures.append(error)
        for token in self.vault_tokens:
            try:
                self.vault('POST', '/v1/auth/token/revoke', {'token': token}, want=204)
            except BaseException as error:
                failures.append(error)
        if self.vault_policies:
            try:
                self.vault('DELETE', '/v1/secret/metadata/' + self.vault_path, want=204)
            except BaseException as error:
                failures.append(error)
        for policy in self.vault_policies:
            try:
                self.vault('DELETE', '/v1/sys/policies/acl/' + policy, want=204)
            except BaseException as error:
                failures.append(error)
        absence = {}
        for port in (*PORTS, MOCK_PORT, RESERVED_PORT):
            with socket.socket() as sock:
                absence[str(port)] = sock.connect_ex(('127.0.0.1', port)) != 0
        check(all(absence.values()), 'owned port residual')
        receipt = dict(ports_absent=absence, held_process_exits=[p.poll() for p in self.processes],
                       vault_tokens_revoked=len(self.vault_tokens), vault_policies_removed=len(self.vault_policies),
                       errors=[type(e).__name__ + ': ' + str(e) for e in failures])
        readonly._private(self.work / 'mapping-cleanup.json', receipt)
        if failures:
            raise Blocked('mapping cleanup failed; exact private receipts retained') from failures[0]
        emit('mapping.cleanup', 'passed', receipt)


@contextlib.contextmanager
def lease(root):
    work = Path(root) / '.local/testlab/connectors-parallel'
    work.mkdir(parents=True, mode=0o700, exist_ok=True)
    fd = os.open(work / 'lease.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(fd)


def inventory(root):
    Contract(root)
    return dict(module=MODULE, targets=[list(x) for x in TARGETS], ports=[*PORTS, MOCK_PORT, RESERVED_PORT],
                evidence_mode='protocol_mock', real_components=['PostgreSQL', 'Vault'],
                mock='authenticated read-only HTTPS GitLab user and project protocol',
                required_cases=list(REQUIRED_CASES), remote_writes=0, offline_only=True)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--run-id', default=None)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--inventory', action='store_true')
    mode.add_argument('--live', action='store_true')
    args = parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(inventory(args.root)))
        return 0
    run_id = args.run_id or str(uuid.uuid4())
    if str(uuid.UUID(run_id)) != run_id:
        raise Blocked('canonical fresh invocation UUID required')
    try:
        with lease(args.root), context(), readonly.cooperative_signals():
            data = doctor(args.root)
            chain = Chain(args.root, data, run_id)
            readonly.execute(chain)
    except Blocked as error:
        emit('mapping.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        emit('mapping.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
