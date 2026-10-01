"""Native bulk investigation and pending silence proposals in isolated resources.

Only HTTP produces services, alerts, investigations and their audit/events.
SQL configures synthetic identities/grants or exact invocation-owned faults.
Normal independent silence workers run; no votes/approval bypass or upstream write.
"""
from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import fcntl
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import select
import socket
import ssl
import stat
import subprocess
import threading
import time
import uuid
from urllib.parse import urlsplit
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
PORTS = (18660, 18661)
FIXTURE_PORTS = (18662, 18663, 18664)
BASES = ('https://testlab.local:18663', 'https://testlab.local:18664')
SOURCE_SHA = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
MODULE = 'formal-api-bulk-actions'
TARGET = '/api/v1/alert-groups/bulk'
REQUIRED = ('bulk.native_producers', 'bulk.investigate_normal', 'bulk.idempotency_dual_instance',
            'bulk.member_link_extension', 'bulk.ordered_partial', 'bulk.authorization_parameters',
            'bulk.transaction_rollback_recovery', 'bulk.revocation_waiting', 'bulk.session_expiry_waiting',
            'bulk.silence_pending_protocol', 'bulk.silence_retry_partial', 'bulk.silence_audit_fault_recovery',
            'bulk.silence_grant_revocation', 'bulk.silence_approval_boundary', 'bulk.ledger_invariants')
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/webhooks/alertmanager/{source}'),
           ('GET', '/alert-groups'))
UNVERIFIED = ['real Alertmanager authority/execution', 'software/hardware passkey approval votes',
              'bulk companion-evidence orphan fault/reapproval generation', 'large group/candidate limits', 'performance/soak']

_spec = importlib.util.spec_from_file_location('bulk_private_readonly', ROOT / 'tools/testlab/api_readonly_chain.py')
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
base.PORTS, base.BASES, base.GETS = PORTS, BASES, ()
Blocked, check, quote = base.Blocked, base.check, base.quote
ACTIVE = None
_http_transport = base.transport


def transport(origin, method, path, payload=None, headers=None, timeout=12):
    if origin not in BASES:
        return _http_transport(origin, method, path, payload, headers, timeout)
    check(ACTIVE is not None and path.startswith('/'), 'owned TLS invocation required')
    port = urlsplit(origin).port
    conn = http.client.HTTPSConnection('testlab.local', port, context=ACTIVE.tls_context, timeout=timeout)
    conn._create_connection = lambda address, timeout, source_address=None: socket.create_connection(('127.0.0.1', port), timeout, source_address)
    try:
        conn.request(method, path, payload, headers or {})
        response = conn.getresponse(); raw = response.read(base.LIMIT + 1)
        check(len(raw) <= base.LIMIT, 'bounded TLS API response overflow')
        return response.status, raw, dict(response.getheaders())
    finally:
        conn.close()


base.transport = transport


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def hashes():
    return {name: hashlib.sha256((ROOT / 'tools/testlab' / name).read_bytes()).hexdigest()
            for name in ('api_bulk_actions_functional_coverage.py', 'api_readonly_chain.py', 'runner.py', 'backend.py', 'env.py', 'faults.py')}


def emit(name, status, actual, **fields):
    if name.startswith('readonly.api_ready.') and ACTIVE is not None:
        name = 'bulk.api_ready.' + str(ACTIVE.generation) + '.' + name.rsplit('.', 1)[-1]
    mode = fields.pop('evidence_mode', fields.get('interface', {}).get('evidence_mode', 'real_local'))
    row = dict(name=name, status=status, actual=actual, module=MODULE, evidence_mode=mode, **fields)
    if ACTIVE is not None:
        base.append_json(ACTIVE.work / 'cases.jsonl', row)
    print(json.dumps(row, ensure_ascii=False), flush=True)


base.emit = emit


def free_ports():
    for port in (*PORTS, *FIXTURE_PORTS):
        with socket.socket() as sock:
            sock.settimeout(.2)
            check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned bulk port occupied: ' + str(port))


@contextlib.contextmanager
def exclusive(root):
    folder = Path(root).resolve() / '.local/testlab/bulk-actions-parallel'
    check(folder.resolve() == folder, 'bulk evidence traverses symlink')
    folder.mkdir(parents=True, mode=0o700, exist_ok=True)
    fd = os.open(folder / 'lease', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular bulk lease required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('bulk package already running') from None
        yield
    finally:
        os.close(fd)


def doctor(root):
    free_ports()
    data = base.doctor(root)
    pg = base.inspect_pg(data['postgres_container'])
    runtime = Path(root).resolve() / '.local/testlab/runtime-config.json'
    check(data['source_tree_sha'] == SOURCE_SHA and data['binary_sha256'] == BINARY_SHA and
          data.get('runtime_sha') == RUNTIME_SHA and not runtime.is_symlink() and
          hashlib.sha256(runtime.read_bytes()).hexdigest() == RUNTIME_SHA and pg['Id'] == PG_CID,
          'bulk frozen source/binary/runtime/exact PG CID mismatch')
    return dict(data, helper_sha256=hashes(), postgres_container_id=pg['Id'])


class Contract(base.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not parsed.scheme and not parsed.netloc and not parsed.fragment and parsed.path.startswith('/api/v1/'), 'bounded API namespace required')
        short = parsed.path[len('/api/v1'):]
        patterns = [('POST', '/alert-groups/bulk'), *SUPPORT]
        matches = [route for verb, route in patterns if verb == method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(route)), short)]
        if not matches:
            raise Blocked('HTTP operation outside bounded bulk inventory')
        route = short if short in matches else matches[0]
        check(method.lower() in self.api['paths'].get(route, {}), 'bulk operation missing from frozen machine contract')
        return '/api/v1' + route, 'main' if (method, route) in self.main else 'auxiliary_or_historical'


def verify_result(value, action, expected):
    check(set(value) == {'action', 'results'} and value['action'] == action and len(value['results']) == len(expected), 'bulk envelope/order/length differs')
    for item, fields in zip(value['results'], expected):
        check(set(item) == {'id', 'outcome', 'matched', 'changed', 'investigation_id', 'proposal_id', 'error'}, 'bulk exact nullable DTO fields differ')
        check(all(item.get(key) == val for key, val in fields.items()), 'bulk ordered outcome/scope/count differs')
        if item['outcome'] in ('investigating', 'already_investigating'):
            check(re.fullmatch('[a-f0-9]{32}', item['investigation_id'] or '') is not None and item['proposal_id'] is None and item['error'] is None, 'native investigation ID/success missing')
        elif item['outcome'] == 'pending_approval':
            check(re.fullmatch('[a-f0-9]{32}', item['investigation_id'] or '') is not None and re.fullmatch('[a-f0-9]{32}', item['proposal_id'] or '') is not None and item['changed'] == 0 and item['error'] is None, 'pending proposal/investigation/approval boundary differs')
        else:
            check(item['investigation_id'] is None and item['proposal_id'] is None and item['changed'] == 0 and item['error'] == item['outcome'], 'failed group leaked result identity or mutation')


class LocalSources:
    """Two TLS API entries and a strict read-only AM protocol capture; no egress."""
    def __init__(self, chain):
        self.chain, self.servers, self.records, self.lock = chain, [], [], threading.Lock()

    def am_response(self, method, path, authorization):
        allowed = method == 'GET' and path in ('/api/v2/alerts', '/api/v2/silences') and authorization == 'Bearer ' + self.chain.am_token
        return (200, []) if allowed else (405, {'error': 'owned_protocol_write_or_scope_rejected'})

    def start(self, cert, key):
        sources = self
        for port in FIXTURE_PORTS:
            class Handler(BaseHTTPRequestHandler):
                def log_message(self, *_args):
                    pass

                def relay(self):
                    own_port = self.server.server_port
                    if own_port == 18662:
                        status, body = sources.am_response(self.command, self.path, self.headers.get('Authorization'))
                        with sources.lock:
                            record = dict(method=self.command, path=self.path, status=status, evidence_mode='protocol_mock', upstream_forwarded=False)
                            sources.records.append(record)
                        base.append_json(sources.chain.work / 'am-capture.jsonl', record)
                        raw, response_headers = json.dumps(body).encode(), [('Content-Type', 'application/json')]
                    else:
                        check(self.path in ('/healthz', '/readyz') or self.path.startswith('/api/v1/'), 'bounded TLS proxy path')
                        length = int(self.headers.get('Content-Length', '0'))
                        if not 0 <= length <= base.LIMIT:
                            self.send_error(413); return
                        payload = self.rfile.read(length) if length else None
                        headers = {k: v for k, v in self.headers.items() if k.lower() not in ('host', 'connection', 'transfer-encoding', 'content-length', 'x-forwarded-for')}
                        headers['X-Forwarded-For'] = '127.0.0.1'
                        conn = http.client.HTTPConnection('127.0.0.1', 18660 if own_port == 18663 else 18661, timeout=15)
                        try:
                            conn.request(self.command, self.path, payload, headers)
                            response = conn.getresponse(); status = response.status
                            raw, response_headers = response.read(base.LIMIT + 1), response.getheaders()
                            check(len(raw) <= base.LIMIT, 'bounded API proxy response')
                        except OSError:
                            status, raw, response_headers = 502, b'{}', [('Content-Type', 'application/json')]
                        finally:
                            conn.close()
                    try:
                        self.send_response(status)
                        for name, value in response_headers:
                            if name.lower() not in ('connection', 'transfer-encoding', 'content-length', 'server', 'date'):
                                self.send_header(name, value)
                        self.send_header('Content-Length', str(len(raw))); self.send_header('Connection', 'close')
                        self.end_headers(); self.wfile.write(raw)
                    except OSError:
                        pass
                do_GET = do_POST = do_DELETE = do_PUT = do_PATCH = relay
            with base.acquisition_signals():
                server = ThreadingHTTPServer(('127.0.0.1', port), Handler)
                self.servers.append((server, None))
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.load_cert_chain(cert, key)
            server.socket = context.wrap_socket(server.socket, server_side=True); server.daemon_threads = True
            thread = threading.Thread(target=server.serve_forever, kwargs={'poll_interval': .1}, daemon=True)
            self.servers[-1] = (server, thread)
            with base.acquisition_signals():
                thread.start()

    def close(self):
        errors = []
        for server, thread in self.servers:
            try:
                if thread is not None and thread.is_alive():
                    server.shutdown()
                server.server_close()
                if thread is not None and thread.ident is not None:
                    thread.join(timeout=3)
                    check(not thread.is_alive(), 'held local TLS source did not stop')
            except BaseException as error:
                errors.append(error)
        check(not errors, 'one or more held TLS sources failed cleanup')


class Chain(base.Chain):
    def __init__(self, root, data, run_id):
        check(str(uuid.UUID(run_id)) == run_id and data.get('owner') == OWNER, 'canonical nonce/owner required')
        self.root, self.data, self.run_id, self.nonce = Path(root).resolve(), data, run_id, uuid.UUID(run_id).hex
        self.database, self.db_user = 'aiops_testlab_bulk_' + self.nonce, 'bulk_' + self.nonce
        self.owner_tag, self.marker = OWNER + ':' + run_id + ':bulk-actions', 'bulkfixture' + self.nonce
        self.work = self.root / '.local/testlab/bulk-actions-parallel' / run_id
        check(self.work.resolve() == self.work, 'bulk invocation evidence traverses symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid5(uuid.UUID(run_id), '20260930:' + name).hex for name in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {name: 'bulk-' + self.nonce + '-' + name for name in self.users}
        self.cookies, self.ids, self.processes, self.apps, self.groups, self.counts = {}, {}, [], {}, {}, {}
        self.calls, self.generation, self.role_attempted, self.db_attempted = 0, 0, False, False
        self.contract, self.config_hashes, self.call_lock = Contract(self.root), {}, threading.Lock()
        self.local_sources, self.connected, self.lock_processes = LocalSources(self), False, []
        self.am_token = secrets.token_urlsafe(32)
        self.alert_start = dt.datetime.now(dt.timezone.utc) - dt.timedelta(minutes=1)
        self.source_id = 'bulk-source-' + self.nonce
        self.source_ids = {name: self.source_id + ('' if name == 'visible' else '-' + name) for name in ('visible', 'foreign', 'staging')}
        self.source_token = secrets.token_urlsafe(32)
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user, owner_tag=self.owner_tag,
                          source_tree_sha=data['source_tree_sha'], binary_sha256=data['binary_sha256'], ports=[*PORTS, *FIXTURE_PORTS], pids=[],
                          actual_results_source='public native webhook/service/bulk HTTP; owned AM configuration is protocol_mock',
                          silence_execution_enabled='after isolated configuration', automatic_approval=False,
                          approval_actions_used=False, external_notifications=False, model_inference=False)
        base._private(self.work / 'state.json', self.state)
        base._private(self.work / 'credentials.json', dict(password=self.password, database_password=self.db_password, ingress_token=self.source_token))

    def frozen(self):
        super().frozen()
        runtime = self.root / '.local/testlab/runtime-config.json'
        check(hashes() == self.data['helper_sha256'] and not runtime.is_symlink() and hashlib.sha256(runtime.read_bytes()).hexdigest() == RUNTIME_SHA, 'bulk helper/main runtime changed')
        for name, sha in self.config_hashes.items():
            path = self.work / name
            check(not path.is_symlink() and stat.S_IMODE(path.stat().st_mode) == 0o600 and hashlib.sha256(path.read_bytes()).hexdigest() == sha, 'bulk private configuration changed')

    def pg(self, *args, **kwargs):
        deadline = kwargs.get('deadline')
        budget = 3 if deadline is None else min(3, deadline - time.monotonic())
        check(budget > 0 and base.inspect_pg(self.data['postgres_container'], timeout=budget)['Id'] == PG_CID, 'bulk exact PG CID/deadline mismatch')
        return super().pg(*args, **kwargs)

    def first_red(self, row):
        try:
            base.private_file(self.work / 'first-failure.json', json.dumps(row, ensure_ascii=False))
        except FileExistsError:
            pass

    def startup_environment(self):
        result = dict(AIOPS_V3_SILENCE_EXECUTION_ENABLED='true' if self.connected else 'false',
                      AIOPS_V3_PASSKEY_ENABLED='true' if self.connected else 'false', SSL_CERT_FILE=str(self.work / 'tls.crt'))
        if self.connected:
            result.update(AIOPS_OPERATIONS_CONFIG=str(self.work / 'operations.json'), AIOPS_V3_ALERTMANAGER_CONFIG=str(self.work / 'alertmanager.json'),
                          AIOPS_V3_BULK_GROUP_SILENCE_CONFIG=str(self.work / 'bulk-bindings.json'), AIOPS_V3_SERVICE_ALERT_COVERAGE_CONFIG=str(self.work / 'alert-coverage.json'))
        return result

    def apply_startup_environment(self, variables):
        check(variables['AIOPS_PUBLIC_ORIGIN'] in BASES and variables['AIOPS_ADDR'] in ('127.0.0.1:18660', '127.0.0.1:18661'), 'exact owned TLS/API origin required')
        return super().apply_startup_environment(dict(variables, AIOPS_SECURE_COOKIES='true'))

    def prepare_tls(self):
        cert, key = self.work / 'tls.crt', self.work / 'tls.key'
        result = subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=testlab.local',
                                 '-addext', 'subjectAltName=DNS:testlab.local,DNS:localhost,IP:127.0.0.1', '-keyout', str(key), '-out', str(cert)],
                                capture_output=True, timeout=20, umask=0o077)
        check(result.returncode == 0, 'private TLS certificate preparation failed')
        for path in (cert, key):
            os.chmod(path, 0o600); self.config_hashes[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
        self.tls_context = ssl.create_default_context(cafile=str(cert))
        self.local_sources.start(cert, key)

    def start(self):
        super().start()
        self.generation += 1

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0, environment='test', validate=None, headers=None):
        self.frozen()
        check(index in (0, 1) and environment in ('test', 'staging', 'all') and user in (None, 'admin', *self.users), 'bulk account/origin/environment boundary')
        canonical, kind = self.contract.canonical(method, path)
        wire = base.SCENARIO_ALIASES.get(scenario, scenario)
        check(wire in base.WIRE_SCENARIOS, 'unsupported wire scenario')
        check(scenario != 'normal' or not 200 <= want < 300 or validate is not None, 'normal bulk response requires nonempty business validation')
        extra = headers or {}
        check(not extra or (canonical == '/api/v1/webhooks/alertmanager/{source}' and method == 'POST' and user is None and
                           path in {'/api/v1/webhooks/alertmanager/' + source for source in self.source_ids.values()} and extra == {'Authorization': 'Bearer ' + self.source_token}), 'headers only allow exact owned ingress credential')
        request_headers = {'Origin': BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en', **extra}
        if user in self.cookies:
            request_headers['Cookie'] = self.cookies[user]
        if body is not None:
            request_headers['Content-Type'] = 'application/json'
        with self.call_lock:
            self.calls += 1
            ordinal = self.calls
        expected = dict(status=[want], error_code=code, scenario_detail=scenario)
        if canonical == TARGET:
            expected['business_action'] = (body or {}).get('action')
        mode = 'protocol_mock' if canonical == TARGET and (body or {}).get('action') == 'silence' else 'real_local'
        row = dict(name='bulk.http.' + str(ordinal), method=method, path=canonical, route_class=kind, scenario=wire,
                   scenario_detail=scenario, expected=expected, run_id=self.run_id, call_id=self.run_id + '-' + str(ordinal),
                   test_name='NativeBulkActions', evidence_mode=mode, proof_scope='native HTTP + pending silence only; AM source is strict owned protocol Mock')
        try:
            status, raw, response_headers = base.transport(BASES[index], method, path, json.dumps(body).encode() if body is not None else None, request_headers)
            value = json.loads(raw) if raw else None
            actual_code = value.get('error', {}).get('code') if isinstance(value, dict) and isinstance(value.get('error'), dict) else None
            row['actual'] = dict(status=status, error_code=actual_code)
            base._private(self.work / ('http-response-' + str(ordinal) + '.json'), dict(method=method, path=path, status=status, response=value, source_tree_sha=SOURCE_SHA, evidence_mode=mode))
            check(status == want and (code is None or actual_code == code), 'bulk HTTP status/error differs')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate:
                    validate(value)
            row.update(status='passed', registration_evidence=200 <= status < 300 and scenario == 'normal')
        except Exception as error:
            row.setdefault('actual', {'status': None})
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            row.update(status='failed', registration_evidence=False)
            self.first_red(row)
            base.append_json(self.work / 'interfaces.jsonl', row)
            emit(row['name'], 'failed', row['actual'], **({'interface': row} if type(row['actual']['status']) is int else {}))
            raise AssertionError('bulk HTTP assertion failed; immutable private first red retained') from None
        base.append_json(self.work / 'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row)
        return value, response_headers

    def marker_case(self, name, actual, status='passed', mode='real_local'):
        self.frozen()
        emit(name, status, dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, **actual), evidence_mode=mode)

    def ledger(self):
        zero = ('v3_proposals', 'v3_proposal_actions', 'v3_bulk_group_silence_evidence', 'v3_bulk_group_silence_reservations',
                'v3_silence_execution_claims', 'v3_silence_create_operations', 'v3_proposal_approval_votes', 'v3_proposal_approval_decisions', 'executions', 'action_plans', 'runs')
        pairs = [quote(name) + ',(SELECT count(*) FROM ' + name + ')' for name in zero]
        for name, order in (('management_audit', 'id'), ('ops_incidents', 'id'), ('investigations', 'id'), ('branches', 'id'), ('context_revisions', 'branch_id,revision'), ('events', 'seq')):
            pairs.append(quote(name) + ",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY " + order + "),'[]') FROM " + name + ' t)')
        return json.loads(self.sql('SELECT json_build_object(' + ','.join(pairs) + ')', read=True))

    def produce_alerts(self, key, occurrences=1):
        scope = key if key in ('foreign', 'staging') else 'visible'
        fingerprint = self.nonce + '-' + key
        start = self.alert_start
        alerts = [dict(status='firing', fingerprint=fingerprint, startsAt=(start + dt.timedelta(seconds=n)).isoformat(),
                       labels=dict(alertname='Owned bulk ' + key, severity='critical', application=self.apps[scope]['label'],
                                   environment='staging' if scope == 'staging' else 'test', aiops_group_identity=self.nonce + '-' + key),
                       annotations=dict(summary='Local native bulk input; no external notification')) for n in range(occurrences)]
        self.call(None, 'POST', '/api/v1/webhooks/alertmanager/' + self.source_ids[scope], {'alerts': alerts}, want=202, scenario='fixture',
                  headers={'Authorization': 'Bearer ' + self.source_token}, validate=lambda value: check(value == dict(accepted=occurrences, notification_delivery='not_configured'), 'native alert ingress result differs'))
        app = self.apps[scope]
        raw = '\0'.join([app['environment_row'], app['application_id'], self.source_ids[scope], fingerprint])
        self.groups[key] = 'group:' + hashlib.sha256(raw.encode()).hexdigest()[:24]
        self.counts[key] = occurrences
        return alerts

    def native_producers(self):
        self.login('admin')
        for key, env in (('visible', 'test'), ('foreign', 'test'), ('staging', 'staging')):
            name = 'bulk-' + self.nonce + '-' + key
            self.call('admin', 'POST', '/api/v1/services', {'items': [dict(id=name, tier='T1')]}, want=201, environment=env, scenario='fixture', validate=lambda v: check(v == {'created': 1}, 'native service creation differs'))
            self.apps[key] = json.loads(self.sql('SELECT json_build_object(\'application_id\',a.id,\'environment_row\',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key=' + quote(name) + ' AND e.key=' + quote(env), read=True))
            self.apps[key]['label'], self.apps[key]['env'] = name, env
        super().seed()
        self.sql('DELETE FROM v3_role_grants WHERE role_id=ANY(ARRAY[' + ','.join(quote(role) for role in self.roles.values()) + "]) AND module<>'environment';")
        grants = [('reader', module, action, self.apps['visible']['application_id']) for module, action in (('events', 'view'), ('events', 'change'), ('canvas', 'change'), ('silences', 'manage'), ('proposals', 'change'))]
        for user, module, action, app in grants:
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' + ','.join(quote(x) for x in (self.roles[user], module, action, 'application', app)) + ');')
        base.private_file(self.work / 'source-token', self.source_token)
        base._private(self.work / 'operations.json', dict(sources=[dict(id=self.source_ids[key], token_file=str(self.work / 'source-token'), mappings=[dict(labels={'application': value['label'], 'environment': value['env']}, application_id=value['application_id'], environment_id=value['environment_row'])]) for key, value in self.apps.items()], feishu_channels=[]))
        base.private_file(self.work / 'am-token', self.am_token)
        app = self.apps['visible']
        binding = dict(domain='aiops.v3.alertmanager.silence.create.binding.v1', endpointUrl='https://localhost:18662', bearerTokenFile=str(self.work / 'am-token'),
                       allowHttp=False, environmentId='test', environmentLabel='environment', applicationLabel=app['label'], applicationId=app['application_id'])
        self.endpoint_fingerprint = hashlib.sha256(json.dumps(binding, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()
        invariant = dict(scope='strict invocation-owned protocol Mock only', source_id=self.source_id, application_id=app['application_id'], environment='test',
                         group_identity_label='aiops_group_identity', identity_values='invocation UUID + group key; same tuple immutable, different tuple unique; fixed producer under this exclusive lease',
                         upstream='loopback18662; write-deny capture; no forwarding or other producer', helper_sha256=self.data['helper_sha256'], no_real_authority_attested=True)
        self.attestation_digest = hashlib.sha256(json.dumps(invariant, separators=(',', ':'), sort_keys=True).encode()).hexdigest()
        base._private(self.work / 'producer-invariant.json', dict(material=invariant, attestation_digest=self.attestation_digest, endpoint_binding_material=binding, endpoint_binding_fingerprint=self.endpoint_fingerprint))
        base._private(self.work / 'alertmanager.json', dict(environments={'test': dict(url='https://localhost:18662', bearer_token_file=str(self.work / 'am-token'), allow_http=False,
                          environment_label='environment', application_bindings={app['label']: app['application_id']}, source_ids=[self.source_id])}))
        base._private(self.work / 'bulk-bindings.json', dict(bindings=[dict(environment_id='test', source_id=self.source_id, application_id=app['application_id'], label_name='aiops_group_identity',
                          config_revision='owned-bulk-v1', attestation_digest=self.attestation_digest, endpoint_binding_fingerprint=self.endpoint_fingerprint,
                          attestation='upstream-equality-unique-stable-v1', expires_at=(dt.datetime.now(dt.timezone.utc)+dt.timedelta(hours=3)).isoformat())]))
        base._private(self.work / 'alert-coverage.json', dict(bindings=[dict(source_id=self.source_id, application_id=app['application_id'], environment_id='test',
                          labels={'application': app['label'], 'environment': 'test'}, complete=True, complete_from=(self.alert_start-dt.timedelta(minutes=1)).isoformat())]))
        self.config_hashes.update({name: hashlib.sha256((self.work / name).read_bytes()).hexdigest() for name in ('source-token', 'operations.json', 'am-token', 'alertmanager.json', 'bulk-bindings.json', 'alert-coverage.json', 'producer-invariant.json')})
        self.connected = True
        self.state['silence_execution_enabled'] = True
        self.state['am_source_mode'] = 'protocol_mock'; base._private(self.work / 'state.json', self.state)
        for proc in reversed(self.processes):
            if proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=8)
        for index in range(2):
            (self.work / ('api-' + str(index) + '.log')).rename(self.work / ('api-bootstrap-' + str(index) + '.log'))
        self.start()
        for user in self.users:
            self.login(user)
        for key in ('a', 'b', 'c', 'd', 'e', 'f', 'g', 'foreign', 'staging'):
            self.produce_alerts(key, 2 if key == 'a' else 1)
        groups, _ = self.call('reader', 'GET', '/api/v1/alert-groups', scenario='fixture', validate=lambda v: check(len(v['items']) == 7, 'native visible nonempty groups differ'))
        check({row['id'] for row in groups['items']} == {self.groups[key] for key in 'abcdefg'}, 'GET-derived native group identities differ')
        check(next(row['count'] for row in groups['items'] if row['id'] == self.groups['a']) == 2, 'native multi-occurrence group absent')
        self.marker_case('bulk.native_producers', dict(http_services=3, native_alert_occurrences=10, visible_groups=7, foreign_scopes_hidden=True, ingress='formal V3 authenticated webhook', sql_business_rows=0, outbound_notification_configured=False))

    def request(self, keys, action, expected, scenario='normal', want=200, user='reader', index=0, environment='test'):
        return self.call(user, 'POST', TARGET, dict(ids=[self.groups.get(key, key) for key in keys], action=action), want=want, scenario=scenario, index=index, environment=environment,
                         validate=lambda value: verify_result(value, action, expected))[0]

    def expectation(self, key, outcome, changed=0, matched=None):
        return dict(id=self.groups.get(key, key), outcome=outcome, matched=self.counts.get(key, 0) if matched is None else matched, changed=changed)

    def prove_investigation(self, key, value, before, changed, created=True):
        item = next(item for item in value['results'] if item['id'] == self.groups[key])
        inv = item['investigation_id']
        after = self.ledger()
        native = next(row for row in after['investigations'] if row['id'] == inv)
        members = [row for row in after['ops_incidents'] if row['fingerprint'] == self.nonce + '-' + key]
        branch = [row for row in after['branches'] if row['investigation_id'] == inv and row['parent_id'] is None]
        contexts = [row for row in after['context_revisions'] if row['branch_id'] == branch[0]['id']] if len(branch) == 1 else []
        audits = [row for row in after['management_audit'] if row['target'] == self.groups[key] and row not in before['management_audit']]
        check(native['application_id'] == self.apps['visible']['application_id'] and native['environment_id'] == self.apps['visible']['environment_row'] and native['created_by'] == self.users['reader'] and native['status'] == 'open', 'investigation scope/person mismatch')
        check(len(members) == self.counts[key] and all(row['investigation_id'] == inv and row['status'] == 'investigating' and row['alert_status'] == 'firing' for row in members), 'native group members not exactly linked')
        check(len(branch) == 1 and branch[0]['revision'] == 0 and len(contexts) == 1 and contexts[0]['revision'] == 0 and contexts[0]['messages'] == [] and contexts[0]['provenance'] == {'kind': 'incident', 'group_id': self.groups[key]}, 'native root/context proof differs')
        check(len(audits) == 1 and audits[0]['actor_id'] == self.users['reader'] and audits[0]['actor_kind'] == 'user' and audits[0]['action'] == 'v3.alert_group.investigate' and audits[0]['outcome'] == 'success' and audits[0]['application_id'] == self.apps['visible']['application_id'] and audits[0]['changes'] == dict(environment_id='test', investigation_id=inv, linked_members=changed, created_investigation=created), 'precise bulk audit differs')
        events = [row for row in after['events'] if row['investigation_id'] == inv and row not in before['events']]
        check(len(events) == 1 and events[0]['type'] == ('investigation.created' if created else 'investigation.alert_group_linked') and events[0]['payload']['group_id'] == self.groups[key] and events[0]['payload']['created_by'] == self.users['reader'] and len(events[0]['payload']['members']) == changed, 'native creation/link event differs')
        base.append_json(self.work / 'native-investigation-proofs.jsonl', dict(group_id=self.groups[key], investigation=native, members=members, root=branch, contexts=contexts, audit=audits[0], event=events[0], before=before, after=after))
        return inv

    def unchanged(self, function):
        before = self.ledger()
        result = function()
        after = self.ledger()
        base.append_json(self.work / 'unchanged-ledger-proofs.jsonl', dict(before=before, after=after, unchanged=before == after))
        check(before == after, 'retry/refusal changed business/events/audit ledger')
        return result

    def normal_and_retry(self):
        before = self.ledger()
        value = self.request(['a'], 'investigate', [self.expectation('a', 'investigating', 2)])
        inv = self.prove_investigation('a', value, before, 2)
        self.marker_case('bulk.investigate_normal', dict(group_members=2, investigation_id=inv, actual_root_context_event_audit=True, model_inference=False))
        self.unchanged(lambda: self.request(['a'], 'investigate', [dict(self.expectation('a', 'already_investigating'), investigation_id=inv)], scenario='idempotency', index=1))
        self.marker_case('bulk.idempotency_dual_instance', dict(same_investigation=True, duplicate_audit=0, changed=0))
        self.produce_alerts('a', 3)
        before = self.ledger()
        value = self.request(['a'], 'investigate', [dict(self.expectation('a', 'investigating', 1), investigation_id=inv)], scenario='idempotency', index=1)
        self.prove_investigation('a', value, before, 1, created=False)
        self.marker_case('bulk.member_link_extension', dict(reused_investigation=True, original_occurrences_deduplicated=True, added_native_member=1, precise_link_event=True))

    def partial_and_bounds(self):
        unknown = 'group:' + '0' * 24
        before = self.ledger()
        value = self.request(['foreign', 'b', unknown], 'investigate', [self.expectation('foreign', 'not_found', matched=0), self.expectation('b', 'investigating', 1), self.expectation(unknown, 'not_found', matched=0)], want=207, scenario='authorization')
        self.prove_investigation('b', value, before, 1)
        self.marker_case('bulk.ordered_partial', dict(ordered_items=3, hidden_and_missing_indistinguishable=True, successful_group_committed=True))
        for key in ('foreign', 'staging'):
            self.unchanged(lambda key=key: self.request([key], 'investigate', [self.expectation(key, 'not_found', matched=0)], want=207, scenario='authorization'))
        self.unchanged(lambda: self.request(['a'], 'investigate', [self.expectation('a', 'not_found', matched=0)], want=207, scenario='authorization', user='peer'))
        for body in (dict(ids=[], action='investigate'), dict(ids=[self.groups['a']]*2, action='investigate'), dict(ids=['group:'+format(n, '024x') for n in range(21)], action='investigate'), dict(ids=['group:'+'A'*24], action='investigate'), dict(ids=[self.groups['a']], action='merge')):
            self.unchanged(lambda body=body: self.call('reader', 'POST', TARGET, body, want=422, code='invalid_bulk_request', scenario='boundary'))
        self.unchanged(lambda: self.call(None, 'POST', TARGET, dict(ids=[self.groups['a']], action='investigate'), want=401, code='unauthorized', scenario='authorization'))
        self.unchanged(lambda: self.call('admin', 'POST', TARGET, dict(ids=[self.groups['a']], action='investigate'), environment='all', want=409, code='env_view_only', scenario='boundary'))
        self.marker_case('bulk.authorization_parameters', dict(cross_application=True, cross_environment=True, unauthenticated=True, count_unique_syntax_action_bounds=True, rejected_ledger_unchanged=True))

    def rollback_recovery(self):
        func, trigger = 'bulk_fault_' + self.nonce, 'bulk_audit_fault_' + self.nonce
        self.sql('CREATE FUNCTION ' + base.identifier(func) + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.alert_group.investigate' AND NEW.target=" + quote(self.groups['c']) + " THEN RAISE EXCEPTION 'exact owned bulk audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER " + base.identifier(trigger) + ' BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION ' + base.identifier(func) + '();')
        before = self.ledger()
        try:
            value = self.request(['c', 'd'], 'investigate', [self.expectation('c', 'unavailable'), self.expectation('d', 'investigating', 1)], want=207, scenario='failure')
            self.prove_investigation('d', value, before, 1)
            after = self.ledger()
            check([row for row in after['ops_incidents'] if row['fingerprint'] == self.nonce+'-c'] == [row for row in before['ops_incidents'] if row['fingerprint'] == self.nonce+'-c'] and len(after['investigations']) == len(before['investigations']) + 1 and len(after['branches']) == len(before['branches']) + 1 and len(after['context_revisions']) == len(before['context_revisions']) + 1, 'failed group left partial investigation/root/context/member mutation')
            base._private(self.work / 'transaction-fault-proof.json', dict(exact_group=self.groups['c'], before=before, after=after, failed_group_rolled_back=True, sibling_group_committed=True))
        finally:
            self.sql('DROP TRIGGER ' + base.identifier(trigger) + ' ON management_audit; DROP FUNCTION ' + base.identifier(func) + '();')
        before = self.ledger()
        value = self.request(['c', 'd'], 'investigate', [self.expectation('c', 'investigating', 1), self.expectation('d', 'already_investigating')], scenario='failure', index=1)
        self.prove_investigation('c', value, before, 1)
        self.marker_case('bulk.transaction_rollback_recovery', dict(investigation_root_context_event_members_audit_rolled_back=True, other_group_committed=True, original_batch_recovered=True))

    @contextlib.contextmanager
    def hold_incident(self, key):
        member = self.sql('SELECT id FROM ops_incidents WHERE fingerprint=' + quote(self.nonce+'-'+key) + ' ORDER BY id LIMIT 1', read=True)
        check(re.fullmatch('[a-f0-9]{32}', member) is not None, 'owned incident lock identity missing')
        check(base.inspect_pg(self.data['postgres_container'])['Id'] == PG_CID, 'exact PG CID changed before own lock')
        log = os.open(self.work / ('lock-holder-' + key + '.log'), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        proc = None
        try:
            with base.acquisition_signals():
                proc = subprocess.Popen(['docker', 'exec', '-i', '-e', 'PGAPPNAME=' + self.pg_application('sql'), self.data['postgres_container'], 'psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-U', self.db_user, '-d', self.database], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, text=True)
                self.lock_processes.append(proc)
                os.close(log); log = None
            proc.stdin.write('SET search_path=public; BEGIN; SET LOCAL statement_timeout=2000; SELECT pg_backend_pid() FROM ops_incidents WHERE id=' + quote(member) + ' FOR UPDATE;\n')
            proc.stdin.flush()
            ready = select.select([proc.stdout], [], [], 5)[0]
            check(bool(ready), 'owned lock acquisition did not return a backend identity')
            backend = proc.stdout.readline().strip()
            check(backend.isdigit() and int(backend) > 1 and proc.poll() is None, 'owned lock failed or backend identity invalid')
            yield int(backend)
        finally:
            if log is not None:
                os.close(log)
            if proc is not None and proc.poll() is None:
                try:
                    proc.stdin.write('ROLLBACK;\n\\q\n'); proc.stdin.flush(); proc.stdin.close()
                    proc.wait(timeout=5)
                except (OSError, subprocess.TimeoutExpired):
                    proc.terminate()
                    try:
                        proc.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        proc.kill(); proc.wait(timeout=2)
            if proc is not None:
                proc.stdout.close()
                check(proc.poll() is not None, 'held owned SQL Popen did not stop')

    def waiting_fault(self, key, outcome, apply_fault):
        errors, worker = [], None
        before = self.ledger()
        try:
            with self.hold_incident(key) as blocker:
                def pending():
                    try:
                        self.request([key], 'investigate', [self.expectation(key, outcome, matched=0)], want=207, scenario='authorization', index=1)
                    except BaseException as error:
                        errors.append(error)
                worker = threading.Thread(target=pending)
                worker.start()
                deadline = time.monotonic() + 5
                while time.monotonic() < deadline:
                    waiters = json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('pid',pid,'backend_start',backend_start,'application_name',application_name,'wait_event_type',wait_event_type,'blockers',pg_blocking_pids(pid))),'[]') FROM pg_stat_activity WHERE datname=" + quote(self.database) + ' AND usename=' + quote(self.db_user) + ' AND application_name=' + quote(self.pg_application('api', 1)) + ' AND ' + str(blocker) + '=ANY(pg_blocking_pids(pid))', read=True))
                    if waiters:
                        break
                    time.sleep(.05)
                else:
                    raise Blocked('exact owned API backend did not reach held incident lock')
                apply_fault()
                base.append_json(self.work / 'waiting-fault-proofs.jsonl', dict(group_id=self.groups[key], blocker_backend=blocker, waiters=waiters, fault_committed_before_unlock=True, expected_outcome=outcome))
        finally:
            # Always release the held transaction before joining, including failed
            # timing-proof/fault injection, before outer API/PG cleanup runs.
            if worker is not None:
                worker.join(timeout=15)
                check(not worker.is_alive(), 'waiting bulk HTTP thread did not stop')
        check(not errors, 'waiting bulk request did not fail closed')
        check(self.ledger() == before, 'waiting revoked/expired request changed native ledger')

    def revoke_and_expire(self):
        for key, module, action in (('e', 'events', 'change'), ('f', 'canvas', 'change')):
            clause = 'role_id=' + quote(self.roles['reader']) + ' AND module=' + quote(module) + ' AND action=' + quote(action) + " AND scope_kind='application' AND scope_id=" + quote(self.apps['visible']['application_id'])
            try:
                self.waiting_fault(key, 'not_found', lambda clause=clause: self.sql('DELETE FROM v3_role_grants WHERE ' + clause + ';'))
            finally:
                self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' + ','.join(quote(val) for val in (self.roles['reader'], module, action, 'application', self.apps['visible']['application_id'])) + ');')
            before = self.ledger()
            value = self.request([key], 'investigate', [self.expectation(key, 'investigating', 1)], scenario='authorization')
            self.prove_investigation(key, value, before, 1)
        self.marker_case('bulk.revocation_waiting', dict(exact_post_middleware_pg_blocker_observed=True, events_change_and_canvas_change_revoked_before_final_policy=True, restored_native_success=True))
        token = self.cookies['reader'].split('=', 1)[1]
        token_hash = hashlib.sha256(token.encode()).hexdigest()
        self.waiting_fault('g', 'session_revoked', lambda: self.sql('UPDATE sessions SET expires_at=clock_timestamp()-interval \'1 second\' WHERE token_hash=' + quote(token_hash) + ' AND user_id=' + quote(self.users['reader']) + ';'))
        self.unchanged(lambda: self.call('reader', 'POST', TARGET, dict(ids=[self.groups['g']], action='investigate'), want=401, code='unauthorized', scenario='authorization'))
        self.login('reader')
        before = self.ledger()
        value = self.request(['g'], 'investigate', [self.expectation('g', 'investigating', 1)], scenario='authorization')
        self.prove_investigation('g', value, before, 1)
        self.marker_case('bulk.session_expiry_waiting', dict(expired_after_middleware_before_group_authorization=True, exact_pg_wait_proof=True, post_expiry_http401=True, new_session_recovered=True))

    def prove_silence(self, key, value, before):
        item = next(item for item in value['results'] if item['id'] == self.groups[key])
        proposal = item['proposal_id']
        native = json.loads(self.sql("SELECT json_build_object('proposal',to_jsonb(p),'action',to_jsonb(a),'evidence',to_jsonb(e),'reservation',to_jsonb(r)) FROM v3_proposals p JOIN v3_proposal_actions a ON a.proposal_id=p.id JOIN v3_bulk_group_silence_evidence e ON e.proposal_id=p.id JOIN v3_bulk_group_silence_reservations r ON r.proposal_id=p.id WHERE p.id=" + quote(proposal), read=True))
        base.append_json(self.work / 'native-silence-inputs.jsonl', dict(group_id=self.groups[key], native=native, evidence_mode='protocol_mock'))
        p, action, evidence, reservation = (native[name] for name in ('proposal', 'action', 'evidence', 'reservation'))
        app, actor = self.apps['visible'], self.users['reader']
        check(p['status'] == 'pending' and p['created_by'] == actor and p['creator_kind'] == 'user' and p['application_id'] == app['application_id'] and p['environment_id'] == 'test' and p['investigation_id'] == item['investigation_id'], 'pending proposal native actor/scope/link differs')
        snapshot = action['action']
        check(snapshot['source_kind'] == 'human_request' and snapshot['investigation_id'] == item['investigation_id'], 'silence snapshot source/link differs')
        prepared = snapshot['silence']
        matchers = {row['name']: row['value'] for row in prepared['matchers'] if row['isEqual'] is True and row['isRegex'] is False}
        check(len(prepared['matchers']) == 3 and matchers == {'application': app['label'], 'environment': 'test', 'aiops_group_identity': self.nonce + '-' + key} and prepared['duration_seconds'] == 3600, 'exact equality group silence matchers/duration differ')
        check(action['action_kind'] == 'alertmanager.silence.create' and action['schema_version'] == 1 and action['creator_id'] == actor and action['source_kind'] == 'human_request' and action['idempotency_key'].startswith('bulk-') and action['binding_fingerprint'] == self.endpoint_fingerprint and prepared['binding_fingerprint'] == self.endpoint_fingerprint and prepared['application_id'] == app['application_id'] and prepared['environment_id'] == 'test' and prepared['actor_id'] == actor, 'native immutable silence action differs')
        for name, expected in dict(group_id=self.groups[key], environment_id='test', environment_row_id=app['environment_row'], application_id=app['application_id'], source_id=self.source_id,
                                  fingerprint=self.nonce+'-'+key, investigation_id=item['investigation_id'], identity_label_name='aiops_group_identity', identity_label_value=self.nonce+'-'+key,
                                  config_revision='owned-bulk-v1', attestation_digest=self.attestation_digest, endpoint_binding_fingerprint=self.endpoint_fingerprint, action_digest=action['action_digest']).items():
            check(evidence.get(name) == expected, 'native companion evidence differs: ' + name)
        check(re.fullmatch('[a-f0-9]{64}', evidence['member_digest']) is not None and reservation['proposal_id'] == proposal and reservation['released_at'] is None and reservation['identity_label_value'] == self.nonce+'-'+key and reservation['endpoint_binding_fingerprint'] == self.endpoint_fingerprint, 'native reservation/member digest differs')
        after = self.ledger()
        audits = [row for row in after['management_audit'] if row not in before['management_audit']]
        check(len(audits) == 1 and audits[0]['action'] == 'v3.proposal.silence_create_requested' and audits[0]['target'] == proposal and audits[0]['actor_id'] == actor and audits[0]['actor_kind'] == 'user' and audits[0]['outcome'] == 'success' and audits[0]['application_id'] == app['application_id'] and audits[0]['changes'] == dict(environment_id='test', investigation_id=item['investigation_id'], action_digest=action['action_digest'], review_content_digest=action['review_content_digest']), 'native silence precise audit differs')
        added = {'v3_proposals', 'v3_proposal_actions', 'v3_bulk_group_silence_evidence', 'v3_bulk_group_silence_reservations'}
        check(all(after[name] == before[name]+1 for name in added) and all(after[name] == before[name] for name in after if name not in added | {'management_audit'}), 'pending silence changed incidents/investigations/execution ledger')
        base.append_json(self.work / 'native-silence-proofs.jsonl', dict(group_id=self.groups[key], native=native, audit=audits[0], before=before, after=after, evidence_mode='protocol_mock', scope='native pending proposal; owned protocol AM binding; no real authority asserted'))
        return proposal

    def silence_pending(self):
        before = self.ledger()
        value = self.request(['a'], 'silence', [self.expectation('a', 'pending_approval')])
        proposal = self.prove_silence('a', value, before)
        self.marker_case('bulk.silence_pending_protocol', dict(native_pending_proposal=proposal, exact_three_equality_matchers=True, native_evidence_reservation_audit=True, worker_enabled=True, no_approval_used=True), mode='protocol_mock')
        repeat = dict(self.expectation('a', 'pending_approval'), proposal_id=proposal, investigation_id=value['results'][0]['investigation_id'])
        self.unchanged(lambda: self.request(['a'], 'silence', [repeat], scenario='idempotency', index=1))
        unknown = 'group:' + '0'*24
        before = self.ledger()
        value = self.request(['foreign', 'b', unknown], 'silence', [self.expectation('foreign', 'not_found', matched=0), self.expectation('b', 'pending_approval'), self.expectation(unknown, 'not_found', matched=0)], want=207, scenario='authorization', index=1)
        self.prove_silence('b', value, before)
        self.marker_case('bulk.silence_retry_partial', dict(ordered_207_partial=True, sibling_pending_committed=True, repeated_second_instance_same_proposal=True, no_duplicate_audit_evidence=True), mode='protocol_mock')
        func, trigger = 'silence_fault_' + self.nonce, 'silence_audit_fault_' + self.nonce
        self.sql('CREATE FUNCTION ' + base.identifier(func) + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.proposal.silence_create_requested' AND NEW.actor_id=" + quote(self.users['reader']) + " AND NEW.application_id=" + quote(self.apps['visible']['application_id']) + " THEN RAISE EXCEPTION 'exact owned producer audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER " + base.identifier(trigger) + ' BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION ' + base.identifier(func) + '();')
        try:
            self.unchanged(lambda: self.request(['c'], 'silence', [self.expectation('c', 'unavailable')], want=207, scenario='failure'))
        finally:
            self.sql('DROP TRIGGER ' + base.identifier(trigger) + ' ON management_audit; DROP FUNCTION ' + base.identifier(func) + '();')
        before = self.ledger()
        value = self.request(['c'], 'silence', [self.expectation('c', 'pending_approval')], scenario='failure', index=1)
        self.prove_silence('c', value, before)
        self.marker_case('bulk.silence_audit_fault_recovery', dict(producer_proposal_action_audit_rollback=True, original_request_recovered=True, companion_separate_transaction_rollback_not_claimed=True), mode='protocol_mock')
        for module, action in (('silences', 'manage'), ('proposals', 'change')):
            clause = 'role_id=' + quote(self.roles['reader']) + ' AND module=' + quote(module) + ' AND action=' + quote(action) + " AND scope_kind='application' AND scope_id=" + quote(self.apps['visible']['application_id'])
            self.sql('DELETE FROM v3_role_grants WHERE ' + clause + ';')
            try:
                self.unchanged(lambda: self.request(['a'], 'silence', [self.expectation('a', 'not_found')], want=207, scenario='authorization', index=1))
            finally:
                self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' + ','.join(quote(val) for val in (self.roles['reader'], module, action, 'application', self.apps['visible']['application_id'])) + ');')
            self.unchanged(lambda: self.request(['a'], 'silence', [repeat], scenario='authorization', index=1))
        self.marker_case('bulk.silence_grant_revocation', dict(each_precise_manage_change_grant_revoked=True, already_pending_denied=True, restored_same_proposal=True), mode='protocol_mock')
        final = self.ledger()
        zero = ('v3_silence_execution_claims', 'v3_silence_create_operations', 'v3_proposal_approval_votes', 'v3_proposal_approval_decisions', 'executions', 'action_plans', 'runs')
        pending_count = int(self.sql("SELECT count(*) FROM v3_proposals WHERE status='pending' AND created_by=" + quote(self.users['reader']), read=True))
        check(pending_count == 3 and all(final[name] == 0 for name in zero) and all(row['method'] == 'GET' and not row['upstream_forwarded'] for row in self.local_sources.records), 'bulk crossed pending approval or upstream write boundary')
        base._private(self.work / 'approval-boundary-proof.json', dict(normal_worker_enabled=True, pending_count=pending_count, native_zero={name: final[name] for name in zero}, am_capture=list(self.local_sources.records), approval_http_used=False, source_mode='protocol_mock', upstream_forwarded=False))
        self.marker_case('bulk.silence_approval_boundary', dict(pending_proposals=3, approvals=0, worker_claims=0, create_operations=0, external_writes=0, model_runs=0, upstream_forwarded=False), mode='protocol_mock')

    def run(self):
        self.provision(); self.prepare_tls(); self.start(); self.native_producers()
        baseline = self.ledger(); base._private(self.work / 'native-baseline.json', baseline)
        self.unchanged(lambda: self.request(['a'], 'silence', [self.expectation('a', 'investigation_required')], want=207, scenario='boundary'))
        self.normal_and_retry(); self.partial_and_bounds(); self.rollback_recovery(); self.revoke_and_expire(); self.silence_pending()
        final = self.ledger()
        check(len(final['investigations']) == 7 and len(final['branches']) == 7 and len(final['context_revisions']) == 7 and len(final['ops_incidents']) == 11, 'final native group/occurrence/branch/context counts differ')
        audits = [row for row in final['management_audit'] if row['action'] == 'v3.alert_group.investigate']
        check(len(audits) == 8 and len(final['events']) == 8 and final['v3_proposals'] == final['v3_proposal_actions'] == final['v3_bulk_group_silence_evidence'] == final['v3_bulk_group_silence_reservations'] == 3, 'native audit/event/proposal invariant differs')
        self.marker_case('bulk.ledger_invariants', dict(investigations=7, roots=7, contexts=7, investigate_audits=8, silence_audits=3, native_events=8, alert_occurrences=11, pending_proposals=3, approvals=0, external_execution=0, model_runs=0))
        result = dict(investigate_business_chain_verified=True, silence_pending_protocol_chain_verified=True, source_mode='native_http + protocol_mock_AM_binding', existing_route_only=True, http_results=self.calls, required_cases=list(REQUIRED), unverified=UNVERIFIED, full_acceptance=False)
        base._private(self.work / 'database-final-ledger.json', final); base._private(self.work / 'result.json', result)
        return result

    def cleanup(self):
        lock_errors = []
        try:
            for proc in reversed(self.lock_processes):
                try:
                    if proc.poll() is None:
                        proc.terminate()
                        try:
                            proc.wait(timeout=2)
                        except subprocess.TimeoutExpired:
                            proc.kill(); proc.wait(timeout=2)
                except BaseException as error:
                    lock_errors.append(type(error).__name__)
            super().cleanup()
        finally:
            self.local_sources.close()
            free_ports()
            base._private(self.work / 'ports-cleanup.json', dict(ports_free=[*PORTS, *FIXTURE_PORTS], held_TLS_sources_closed=True, upstream_forwarded=False))
        check(not lock_errors, 'owned lock cleanup errors: ' + ','.join(lock_errors))


def handoff(chain, status, error=None):
    case_path = chain.work / 'cases.jsonl'
    rows = [json.loads(line) for line in case_path.read_text().splitlines()] if case_path.is_file() else []
    source_end = base.source_fingerprint(chain.root)
    def receipt(name):
        path = chain.work / name
        return json.loads(path.read_text()) if path.is_file() and not path.is_symlink() and stat.S_IMODE(path.stat().st_mode) == 0o600 else {}
    cleanup, ports, result = (receipt(name) for name in ('cleanup.json', 'ports-cleanup.json', 'result.json'))
    ready = error is None and status == 'passed' and source_end == chain.data['source_tree_sha'] == SOURCE_SHA and set(REQUIRED) <= {row['name'] for row in rows if row['status'] == 'passed'} and not any(row['status'] in ('failed', 'interrupted', 'blocked') for row in rows) and result.get('required_cases') == list(REQUIRED) and result.get('silence_pending_protocol_chain_verified') is True and all(cleanup.get(key) is True for key in ('database_absent', 'role_absent', 'owned_processes_exited')) and ports.get('ports_free') == [*PORTS, *FIXTURE_PORTS]
    report = dict(run_id=chain.run_id, status=status, collection_qualified=ready, source_tree_sha=chain.data['source_tree_sha'], source_tree_sha_end=source_end, binary_sha256=chain.data['binary_sha256'], main_runtime_sha=RUNTIME_SHA, helper_sha256=chain.data['helper_sha256'], case_counts={key: sum(row['status'] == key for row in rows) for key in ('passed', 'failed', 'blocked')}, required_cases=list(REQUIRED), existing_route_only=True, investigate_business_chain_verified=ready, silence_pending_protocol_chain_verified=ready, unverified=UNVERIFIED, error_type=type(error).__name__ if error else None, http_results=chain.calls, full_acceptance=False, private_work=str(chain.work))
    base._private(chain.work / 'handoff.json', report)
    base.private_file(chain.work / 'handoff.md', '# Native bulk actions\n\n' + json.dumps(report, ensure_ascii=False, indent=2))
    base._private(chain.root / '.local/testlab/bulk-actions-parallel/handoff-latest.json', dict(run_id=chain.run_id, handoff=str(chain.work / 'handoff.json')))
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--run-id')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--inventory', action='store_true'); mode.add_argument('--live', action='store_true')
    args = parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(dict(target=TARGET, actions=['investigate', 'silence'], normal_worker_enabled_in_owned_live_run=True, upstream_source_mode='protocol_mock', unverified=UNVERIFIED, ports=[*PORTS, *FIXTURE_PORTS], offline_only=True)))
        return 0
    global ACTIVE
    chain, status, error, report = None, 'failed', None, None
    try:
        with exclusive(args.root), base.cooperative_signals():
            data = doctor(args.root); chain = Chain(args.root, data, args.run_id or str(uuid.uuid4())); ACTIVE = chain
            base._private(chain.work / 'doctor-before.json', {key: data[key] for key in ('owner', 'source_tree_sha', 'binary_sha256', 'runtime_sha', 'postgres_container_id', 'helper_sha256')})
            base.execute(chain)
            status = 'passed'
    except BaseException as failure:
        error = failure
        status = 'interrupted' if isinstance(failure, (KeyboardInterrupt, SystemExit)) else 'failed'
        emit('bulk.package_error', status, dict(error_type=type(failure).__name__, reason=str(failure)))
    finally:
        if chain is not None:
            report = handoff(chain, status, error)
            emit('bulk.handoff', status, dict(run_id=chain.run_id, handoff=str(chain.work / 'handoff.json'), collection_qualified=report['collection_qualified'], full_acceptance=False))
        ACTIVE = None
    return 0 if report and report['collection_qualified'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
