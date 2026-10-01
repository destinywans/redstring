"""Bounded service/discovery/resilience acceptance, with explicit source limits.

HTTP produces services, investigation canvases and human review proposals.
Two localhost TLS protocol Mocks exercise Kubernetes LIST ingestion and the
five-family resilience aggregate. Neither proves a real Kubernetes cluster or
the five authoritative systems. Missing release/AI/full topology stays blocked.
SQL writes only identities, grants and an exact audit-rejection fault trigger.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import copy
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
import socket
import ssl
import stat
import subprocess
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
PORTS = (18640, 18641)
BASES = tuple('http://127.0.0.1:' + str(p) for p in PORTS)
RESILIENCE_PORT, KUBE_PORT = 18642, 18643
MODULE = 'formal-api-services-functional'
SOURCE_SHA = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
SEED = 20260930
TARGETS = (('GET', '/services'), ('GET', '/services/discover'), ('GET', '/services/{id}'),
           ('GET', '/topology'), ('GET', '/services/{id}/resilience'),
           ('POST', '/services/{id}/resilience/proposals'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/canvases'))
REQUIRED = ('services.native_producers', 'services.discovery_protocol', 'services.resilience_protocol',
            'services.human_proposals', 'services.authorization', 'services.parameters',
            'services.failure_recovery', 'services.discovery_failure_recovery', 'services.proposal_grant_revocation',
            'services.revocation', 'services.audit_consistency')
NORMAL_TARGET_MODES = {'POST /api/v1/services/{id}/resilience/proposals': 'real_local',
                       'GET /api/v1/services/discover': 'protocol_mock',
                       'GET /api/v1/services/{id}/resilience': 'protocol_mock'}
BLOCKERS = {
    'GET /api/v1/services': 'Complete view needs successful native deployment, full service metrics, owner/oncall and alert coverage; no release/AI success fabricated or paused CI/CD resumed.',
    'GET /api/v1/services/{id}': 'Complete detail additionally needs recent native completed model run and matching assistant reply; real producer not present in this package.',
    'GET /api/v1/topology': 'Current CMDB projection is explicitly partial; complete topology_v1 requires exact application-linked graph and full source/version-bound metrics.',
}

# Private module namespace; the reused lifecycle source and other workers'
# imports are immutable. No shared process-global port settings are changed.
_spec = importlib.util.spec_from_file_location('services_private_readonly', ROOT / 'tools/testlab/api_readonly_chain.py')
readonly = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(readonly)
readonly.PORTS, readonly.BASES, readonly.GETS = PORTS, BASES, ()
Blocked, check = readonly.Blocked, readonly.check
_thread = threading.local()
ACTIVE = None
_transport = readonly.transport
_append_json = readonly.append_json


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def hashes():
    files = ('api_services_functional_coverage.py', 'api_readonly_chain.py', 'runner.py',
             'backend.py', 'env.py', 'faults.py')
    return {'tools/testlab/' + name: hashlib.sha256((ROOT / 'tools/testlab' / name).read_bytes()).hexdigest() for name in files}


def emit(name, status, actual, **fields):
    mode = fields.pop('evidence_mode', 'real_local')
    interface = fields.get('interface')
    if isinstance(interface, dict):
        mode = getattr(_thread, 'mode', mode)
        interface.update(evidence_mode=mode, proof_scope=getattr(_thread, 'scope', 'native local HTTP'))
    row = dict(name=name, status=status, actual=actual, module=MODULE, evidence_mode=mode, **fields)
    if ACTIVE is not None:
        readonly.append_json(ACTIVE.work / 'cases.jsonl', row)
    print(json.dumps(row, ensure_ascii=False), flush=True)


readonly.emit = emit


def append_json(path, row):
    if Path(path).name == 'interfaces.jsonl' and isinstance(row, dict):
        row = dict(row, evidence_mode=getattr(_thread, 'mode', 'real_local'),
                   proof_scope=getattr(_thread, 'scope', 'native local HTTP'))
    _append_json(path, row)


readonly.append_json = append_json


def capture_transport(*args, **kwargs):
    status, body, headers = _transport(*args, **kwargs)
    if ACTIVE is not None and hasattr(_thread, 'call_id'):
        response = json.loads(body) if body else None
        readonly._private(ACTIVE.work / ('http-response-' + str(_thread.call_id) + '.json'),
                          dict(method=args[1], path=args[2], status=status, response=response,
                               evidence_mode=_thread.mode, source_tree_sha=ACTIVE.data['source_tree_sha'],
                               headers={k: v for k, v in headers.items() if k.lower() in ('content-type', 'cache-control', 'etag')}))
    return status, body, headers


readonly.transport = capture_transport


def free_ports():
    for port in (*PORTS, RESILIENCE_PORT, KUBE_PORT):
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1', port)) == 0:
                raise Blocked('assigned services port occupied: ' + str(port))


@contextlib.contextmanager
def exclusive(root):
    folder = Path(root).resolve() / '.local/testlab/services-parallel'
    if folder.resolve() != folder:
        raise Blocked('private services lease traverses symlink')
    folder.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd = os.open(folder / 'lease', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private regular services lease required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('services package already active') from None
        yield
    finally:
        os.close(fd)


def doctor(root):
    free_ports()
    data = readonly.doctor(root)
    pg = readonly.inspect_pg(data['postgres_container'])
    runtime = Path(root).resolve() / '.local/testlab/runtime-config.json'
    if (data['source_tree_sha'] != SOURCE_SHA or data['binary_sha256'] != BINARY_SHA or
            data.get('runtime_sha') != RUNTIME_SHA or runtime.is_symlink() or
            hashlib.sha256(runtime.read_bytes()).hexdigest() != RUNTIME_SHA or pg.get('Id') != PG_CID):
        raise Blocked('frozen source/nonrace binary/main runtime/exact PG CID mismatch')
    return dict(data, helper_sha256=hashes(), postgres_container_id=pg['Id'])


class Contract(readonly.Contract):
    def canonical(self, method, path):
        p = urlsplit(path)
        if p.scheme or p.netloc or p.fragment or not p.path.startswith('/api/v1/'):
            raise Blocked('formal namespace required')
        short = p.path[len('/api/v1'):]
        matches = [route for verb, route in (*TARGETS, *SUPPORT) if verb == method and
                   re.fullmatch(re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(route)), short)]
        route = short if short in matches else matches[0] if len(matches) == 1 else None
        if route is None or method.lower() not in self.api['paths'].get(route, {}):
            raise Blocked('outside bounded services contract')
        return '/api/v1' + route, 'main' if (method, route) in self.main else 'auxiliary_or_historical'

    def shape(self, method, path, status, value):
        if method == 'GET' and urlsplit(path).path == '/api/v1/topology' and value.get('complete_v3_dto') is False:
            # Explicit partial producer is an observed failure of the complete
            # prerequisite, not a frozen DTO or normal-coverage claim.
            check(isinstance(value.get('nodes'), list) and isinstance(value.get('unavailable_fields'), list), 'partial topology contract missing')
            return
        super().shape(method, path, status, value)


KUBE_ENDPOINTS = {
    '/api/v1/namespaces': 'Namespace', '/api/v1/nodes': 'Node', '/apis/apps/v1/deployments': 'Deployment',
    '/apis/apps/v1/statefulsets': 'StatefulSet', '/apis/apps/v1/daemonsets': 'DaemonSet', '/apis/apps/v1/replicasets': 'ReplicaSet',
    '/apis/batch/v1/jobs': 'Job', '/apis/batch/v1/cronjobs': 'CronJob', '/api/v1/pods': 'Pod', '/api/v1/services': 'Service',
    '/apis/networking.k8s.io/v1/ingresses': 'Ingress', '/api/v1/persistentvolumes': 'PersistentVolume',
    '/api/v1/persistentvolumeclaims': 'PersistentVolumeClaim', '/apis/storage.k8s.io/v1/storageclasses': 'StorageClass', '/api/v1/events': 'Event',
}


def kube_path(path):
    parsed = urlsplit(path)
    parts = parsed.path.split('/', 2)
    if parsed.scheme or parsed.netloc or parsed.fragment or len(parts) != 3 or parts[1] not in ('test', 'staging'):
        raise Blocked('Kubernetes Mock scope invalid')
    target = '/' + parts[2]
    query = parse_qs(parsed.query, keep_blank_values=True)
    if target not in KUBE_ENDPOINTS or any(len(v) != 1 for v in query.values()):
        raise Blocked('Kubernetes Mock endpoint invalid')
    if query == {'limit': ['500']}:
        return parts[1], target, False
    if query == {'watch': ['true'], 'allowWatchBookmarks': ['true'], 'resourceVersion': ['1'], 'timeoutSeconds': ['25']}:
        return parts[1], target, True
    raise Blocked('only bounded complete LIST and exact invalidation WATCH allowed')


def workload_objects(nonce, env):
    uid = lambda name: str(uuid.uuid5(uuid.UUID(hex=nonce), str(SEED) + ':' + env + ':' + name))
    name = 'services-discover-' + nonce[:12] + '-' + env
    namespaces = [{'metadata': {'uid': uid('system'), 'name': 'kube-system', 'resourceVersion': '1'}, 'status': {'phase': 'Active'}},
                  {'metadata': {'uid': uid('namespace'), 'name': 'owned', 'resourceVersion': '1'}, 'status': {'phase': 'Active'}}]
    deployment = {'metadata': {'uid': uid('workload'), 'name': name, 'namespace': 'owned', 'resourceVersion': '1',
                              'labels': {'tier': 'T1', 'team': 'unverified-team-claim', 'testlab_run': nonce}},
                  'spec': {'replicas': 2, 'template': {'metadata': {'labels': {'testlab_run': nonce}},
                           'spec': {'containers': [{'name': 'business', 'image': 'testlab/protocol-only:v1'}]}}},
                  'status': {'availableReplicas': 2, 'readyReplicas': 2, 'replicas': 2}}
    return {'/api/v1/namespaces': namespaces, '/apis/apps/v1/deployments': [deployment]}


def resilience_observation(app, env):
    captured = now()
    view = dict(synced=captured, rollout=dict(title='Protocol Mock rollout', status='stable', stable='mock-v1',
                steps=[dict(weight='0%', note='Protocol only', state='current')], split=dict(stable=100, canary=0),
                analysis=[dict(name='synthetic check', value='pass', threshold='Mock', ok=True)],
                stable_series=[10, 11], canary_series=[0, 0], next='Human review', source='protocol_mock'),
                breakers=[dict(brand='Mock', upstream='owned-upstream', mechanism='synthetic', state='closed', threshold='50%', ejected='0')],
                breaker_timeline=[dict(**{'from': 0, 'to': 100}, state='closed')], breaker_note='Protocol Mock',
                limits=[dict(brand='Mock', route='/owned', mechanism='synthetic', limit='100/s', used='10/s', pct=10, rejected=0)],
                limit_note='Protocol Mock', flags=[dict(brand='Mock', key='owned-flag', desc='Synthetic protocol flag', on=False,
                tag=dict(text='Mock', tone='neutral'), action='Human review')],
                hpa=dict(min=1, max=4, current=2, desired=2, replicas=[2, 2], desired_series=[2, 2], cpu=[40, 41], target=70, note='Protocol Mock'),
                ai=['Protocol fixture text; no model inference'])
    return dict(application_id=app, environment_id=env, captured_at=captured,
                sources={kind: dict(id='mock-' + kind, application_id=app, environment_id=env, revision='synthetic-v1',
                                    captured_at=captured, complete=True, item_count=1) for kind in ('rollout', 'breakers', 'limits', 'flags', 'hpa')}, view=view)


class Protocols:
    def __init__(self, chain):
        self.chain = chain
        self.tokens = {kind: secrets.token_urlsafe(32) for kind in ('resilience', 'test', 'staging')}
        self.lock = threading.Lock()
        self.records = []
        self.mode = 'normal'
        self.hold, self.seen, self.release = False, threading.Event(), threading.Event()
        self.servers = []
        self.allowed_apps = set()
        self.kube_failure = set()

    def respond(self, port, path, authorization):
        parsed = urlsplit(path)
        if port == KUBE_PORT:
            env, target, watch = kube_path(path)
            if not secrets.compare_digest(authorization, 'Bearer ' + self.tokens[env]):
                raise Blocked('Kubernetes Mock private token mismatch')
            with self.lock:
                failed = env in self.kube_failure
            if failed:
                return 503, {'error': 'explicit_protocol_source_failure'}, dict(kind='kube', env=env, target=target, watch=watch, failed=True)
            body = {'type': 'BOOKMARK', 'object': {'metadata': {'resourceVersion': '1'}}} if watch else dict(
                apiVersion='v1', kind=KUBE_ENDPOINTS[target] + 'List', metadata=dict(resourceVersion='1', **{'continue': ''}),
                items=workload_objects(self.chain.nonce, env).get(target, []))
            return 200, body, dict(kind='kube', env=env, target=target, watch=watch, failed=False)
        q = parse_qs(parsed.query, keep_blank_values=True)
        if (port != RESILIENCE_PORT or parsed.scheme or parsed.netloc or parsed.fragment or parsed.path != '/snapshot' or
                set(q) != {'application_id', 'environment_id'} or any(len(v) != 1 for v in q.values()) or
                q['application_id'][0] not in self.allowed_apps or q['environment_id'] != ['test'] or
                not secrets.compare_digest(authorization, 'Bearer ' + self.tokens['resilience'])):
            raise Blocked('resilience Mock scope/path/token mismatch')
        app, env = q['application_id'][0], q['environment_id'][0]
        with self.lock:
            mode, hold = self.mode, self.hold
            self.hold = False
        if hold:
            self.seen.set()
            check(self.release.wait(8), 'bounded protocol read hold expired')
        body = resilience_observation(app, env)
        status = 200
        if mode == '503' or mode == '429':
            status, body = int(mode), {'error': 'explicit_protocol_failure'}
        elif mode == 'cross_scope':
            body['sources']['flags']['application_id'] = 'foreign-application'
        elif mode == 'incomplete':
            body['sources']['hpa']['complete'] = False
        elif mode == 'stale':
            body['captured_at'] = '2000-01-01T00:00:00Z'
        elif mode == 'count_mismatch':
            body['sources']['flags']['item_count'] = 2
        elif mode == 'unknown':
            body['unknown_field'] = True
        return status, body, dict(kind='resilience', env=env, application_id=app, mode=mode, held=hold)

    def start(self, cert, key):
        protocol = self
        for port in (RESILIENCE_PORT, KUBE_PORT):
            class Handler(BaseHTTPRequestHandler):
                def log_message(self, *_args):
                    pass

                def do_GET(self):
                    try:
                        status, body, record = protocol.respond(self.server.server_port, self.path, self.headers.get('Authorization', ''))
                    except (Blocked, AssertionError):
                        status, body, record = 403, {'error': 'protocol_scope_rejected'}, {'kind': 'rejected', 'port': self.server.server_port}
                    record.update(status=status, observed_at=now())
                    with protocol.lock:
                        protocol.records.append(record)
                        ordinal = len(protocol.records)
                    readonly._private(protocol.chain.work / ('source-response-' + str(ordinal) + '.json'),
                                      dict(record=record, response=body, evidence_mode='protocol_mock', actual_authority=False))
                    readonly.append_json(protocol.chain.work / 'source-reads.jsonl', record)
                    raw = json.dumps(body).encode()
                    try:
                        self.send_response(status)
                        self.send_header('Content-Type', 'application/json')
                        self.send_header('Content-Length', str(len(raw)))
                        self.send_header('Connection', 'close')
                        self.end_headers()
                        self.wfile.write(raw)
                    except OSError:
                        pass

                def do_POST(self):
                    self.send_error(405)
                do_PUT = do_PATCH = do_DELETE = do_POST
            server = ThreadingHTTPServer(('127.0.0.1', port), Handler)
            server.daemon_threads = True
            self.servers.append((server, None))
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            server.socket = context.wrap_socket(server.socket, server_side=True)
            thread = threading.Thread(target=server.serve_forever, kwargs={'poll_interval': .1}, daemon=True)
            self.servers[-1] = server, thread
            thread.start()

    def count(self):
        with self.lock:
            return sum(row.get('kind') == 'resilience' for row in self.records)

    def close(self):
        self.release.set()
        for server, thread in self.servers:
            if thread is not None and thread.is_alive():
                server.shutdown()
                thread.join(timeout=3)
                check(not thread.is_alive(), 'owned protocol server did not stop')
            server.server_close()


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != OWNER:
            raise Blocked('canonical invocation UUID and explicit owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database, self.db_user = 'aiops_testlab_services_' + self.nonce, 'services_' + self.nonce
        self.marker, self.owner_tag = 'servicesfixture' + self.nonce, OWNER + ':' + run_id + ':services-functional'
        self.work = self.root / '.local/testlab/services-parallel' / run_id
        if self.work.resolve() != self.work:
            raise Blocked('services private evidence traverses symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid5(uuid.UUID(run_id), str(SEED) + ':' + name).hex for name in ('reader', 'peer', 'hidden', 'denied')}
        self.roles = {name: 'services-' + self.nonce + '-' + name for name in self.users}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.contract, self.protocol = Contract(self.root), Protocols(self)
        self.config_hashes, self.apps, self.investigations = {}, {}, {}
        self.state = dict(owner=OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, source_tree_sha=data['source_tree_sha'], binary_sha256=data['binary_sha256'],
                          ports=[*PORTS, RESILIENCE_PORT, KUBE_PORT], pids=[], seed=SEED, suite=MODULE,
                          actual_results_source='public service/canvas/human-proposal HTTP; native collector of explicitly Mock Kubernetes protocol',
                          upstream_modes={'discovery': 'protocol_mock', 'resilience_detail': 'protocol_mock', 'proposal': 'real_local'},
                          no_external_notifications=True, no_model_inference=True, shared_components_untouched=True)
        readonly._private(self.work / 'state.json', self.state)
        readonly._private(self.work / 'credentials.json', dict(password=self.password, database_password=self.db_password))

    def first_red(self, row):
        try:
            readonly.private_file(self.work / 'first-failure.json', json.dumps(row, ensure_ascii=False))
        except FileExistsError:
            pass

    def frozen(self):
        super().frozen()
        runtime = self.root / '.local/testlab/runtime-config.json'
        if hashes() != self.data['helper_sha256'] or runtime.is_symlink() or hashlib.sha256(runtime.read_bytes()).hexdigest() != RUNTIME_SHA:
            raise Blocked('services helper or frozen main runtime changed')
        for name, digest in self.config_hashes.items():
            path = self.work / name
            if path.is_symlink() or stat.S_IMODE(path.stat().st_mode) != 0o600 or hashlib.sha256(path.read_bytes()).hexdigest() != digest:
                raise Blocked('UUID-local protocol config changed')

    def pg(self, *args, **kwargs):
        deadline = kwargs.get('deadline')
        budget = 3 if deadline is None else min(3, deadline - time.monotonic())
        if budget <= 0 or readonly.inspect_pg(self.data['postgres_container'], timeout=budget).get('Id') != PG_CID:
            raise Blocked('services exact PG CID or cleanup deadline changed')
        return super().pg(*args, **kwargs)

    def prepare_protocols(self):
        cert, key = self.work / 'source-cert.pem', self.work / 'source-key.pem'
        result = subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '2',
            '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1', '-keyout', str(key), '-out', str(cert)],
            capture_output=True, timeout=20, env=readonly.command_env({}, {}), umask=0o077)
        if result.returncode:
            raise Blocked('private services TLS preparation failed')
        sources = []
        for env in ('test', 'staging'):
            token = self.work / ('kube-' + env + '-token')
            readonly.private_file(token, self.protocol.tokens[env])
            sources.append(dict(id='services-kube-' + self.nonce + '-' + env, name='services-cluster-' + self.nonce + '-' + env,
                api_url='https://localhost:' + str(KUBE_PORT) + '/' + env, ca_file=str(cert), token_file=str(token),
                environment_key=env, application_bindings=[]))
        readonly.private_file(self.work / 'resilience-token', self.protocol.tokens['resilience'])
        readonly._private(self.work / 'inventory.json', dict(interval_seconds=10, sources=sources))
        for path in (cert, key, self.work / 'kube-test-token', self.work / 'kube-staging-token', self.work / 'resilience-token', self.work / 'inventory.json'):
            os.chmod(path, 0o600)
            self.config_hashes[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
        self.protocol.start(cert, key)
        emit('services.protocol_ready', 'passed', dict(ports=[RESILIENCE_PORT, KUBE_PORT], private_ca=True, sources='protocol_mock', real_authorities=False))

    def startup_environment(self):
        return dict(AIOPS_INVENTORY_CONFIG=str(self.work / 'inventory.json'),
                    AIOPS_V3_RESILIENCE_SOURCE_URL='https://localhost:' + str(RESILIENCE_PORT) + '/snapshot',
                    AIOPS_V3_RESILIENCE_TOKEN_FILE=str(self.work / 'resilience-token'), SSL_CERT_FILE=str(self.work / 'source-cert.pem'))

    def call(self, user, method, path, *args, mode=None, scope=None, **kwargs):
        self.frozen()
        canonical = self.contract.canonical(method, path)[0]
        if mode is None:
            mode = 'protocol_mock' if canonical in ('/api/v1/services/discover', '/api/v1/services/{id}/resilience', '/api/v1/topology') else 'real_local'
        if kwargs.get('scenario', 'normal') == 'normal' and 200 <= kwargs.get('want', 200) < 300 and kwargs.get('validate') is None:
            raise Blocked('normal services response requires exact nonempty business/source validator')
        _thread.mode, _thread.scope, _thread.call_id = mode, scope or ('TLS protocol Mock; real source completeness unverified' if mode == 'protocol_mock' else 'native local human review only'), self.calls + 1
        try:
            return super().call(user, method, path, *args, **kwargs)
        finally:
            for key in ('mode', 'scope', 'call_id'):
                delattr(_thread, key)

    def native_producers(self):
        for key, env in (('visible', 'test'), ('foreign', 'test'), ('staging', 'staging')):
            name = 'services-' + self.nonce + '-' + key
            self.call('admin', 'POST', '/api/v1/services', {'items': [{'id': name, 'tier': 'T1'}]}, want=201,
                      environment=env, scenario='fixture', validate=lambda v: check(v == {'created': 1}, 'native service creation mismatch'))
            self.apps[key] = self.sql('SELECT id FROM applications WHERE key=' + readonly.quote(name), read=True)
            check(re.fullmatch('[a-f0-9]{32}', self.apps[key]) is not None, 'native application identity absent')
        self.protocol.allowed_apps.add(self.apps['visible'])
        super().seed()
        self.sql('DELETE FROM v3_role_grants WHERE role_id=ANY(ARRAY[' + ','.join(readonly.quote(x) for x in self.roles.values()) + "]) AND module<>'environment';")
        grants = [('reader', 'services', 'view', 'application', self.apps['visible']),
                  ('reader', 'canvas', 'view', 'application', self.apps['visible']),
                  ('reader', 'canvas', 'change', 'application', self.apps['visible']),
                  ('reader', 'proposals', 'change', 'application', self.apps['visible'])]
        grants += [(user, module, 'view', 'environment', env) for user, env in (('peer', 'test'), ('hidden', 'staging')) for module in ('services', 'resources')]
        for user, module, action, kind, ident in grants:
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' + ','.join(readonly.quote(v) for v in (self.roles[user], module, action, kind, ident)) + ');')
        for user in self.users:
            self.login(user)
        for key, user, env in (('visible', 'reader', 'test'), ('foreign', 'admin', 'test'), ('staging', 'admin', 'staging')):
            result, _ = self.call(user, 'POST', '/api/v1/canvases', dict(title='Owned resilience review ' + key, kind='investigation', services=[self.apps[key]], ai_draft=False),
                                 want=201, environment=env, scenario='fixture', validate=lambda v: check(isinstance(v.get('id'), str) and v['id'].startswith('INV-') and v.get('href') == '/canvas/' + v['id'], 'native investigation response mismatch'))
            self.investigations[key] = result['id']
        self.sid = base64.urlsafe_b64encode((self.apps['visible'] + '\0test').encode()).decode().rstrip('=')
        self.detail = '/api/v1/services/' + self.sid
        self.resilience, self.proposal = self.detail + '/resilience', self.detail + '/resilience/proposals'
        self.marker_case('services.native_producers', dict(applications=3, investigations=3, producer='public HTTP', sql_business_rows=0, inference_calls=0))

    def marker_case(self, name, actual, status='passed', mode='real_local'):
        self.frozen()
        emit(name, status, dict(run_id=self.run_id, database=self.database, source_tree_sha=SOURCE_SHA, **actual), evidence_mode=mode)

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('proposals',(SELECT count(*) FROM v3_proposals), 'audit',(SELECT COALESCE(json_agg(to_jsonb(a) ORDER BY id),'[]') FROM management_audit a), 'applications',(SELECT count(*) FROM applications),'investigations',(SELECT count(*) FROM investigations), 'executions',(SELECT count(*) FROM executions), 'actions',(SELECT count(*) FROM action_plans))", read=True))

    def discovery(self):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            value = self.sql("SELECT count(*) FROM cmdb_resources WHERE source_kind='kubernetes' AND kind='deployment' AND NOT is_mock", read=True)
            if value == '2':
                break
            time.sleep(.2)
        else:
            raise Blocked('native inventory did not produce both complete protocol snapshots')
        self.call('peer', 'GET', '/api/v1/services/discover', validate=lambda v: self.validate_discovery(v, 'test'))
        self.call('hidden', 'GET', '/api/v1/services/discover', environment='staging', index=1, validate=lambda v: self.validate_discovery(v, 'staging'))
        with self.protocol.lock:
            kinds = {row.get('target') for row in self.protocol.records if row.get('kind') == 'kube' and row.get('env') == 'test' and not row.get('watch') and row.get('status') == 200}
        check(kinds == set(KUBE_ENDPOINTS), 'complete 15-kind LIST provenance missing')
        self.marker_case('services.discovery_protocol', dict(nonempty=True, complete_list_kinds=15, producer='native inventory convert→SaveSnapshot', real_kubernetes=False), mode='protocol_mock')

    def validate_discovery(self, value, env):
        items = value.get('items', [])
        expected = workload_objects(self.nonce, env)['/apis/apps/v1/deployments'][0]
        check(len(items) == 1 and items[0]['id'] == expected['metadata']['name'] and items[0]['uid'] == expected['metadata']['uid'] and
              items[0]['environment_id'] == env and items[0]['kind'] == 'Deployment' and items[0]['namespace'] == 'owned' and
              items[0]['image'] == 'testlab/protocol-only:v1' and items[0]['replicas'] == 2 and items[0]['source']['kind'] == 'kubernetes' and
              items[0]['source']['id'] == 'services-kube-' + self.nonce + '-' + env and items[0]['source']['observed_at'] and items[0]['source']['last_success_at'] and
              items[0]['owner_hint'] == 'Unknown' and items[0]['owner_label_hint'] == 'team=unverified-team-claim' and items[0]['tier_hint'] == 'T1' and
              items[0]['matched'] == [] and 'verified_owner' in items[0]['unavailable_fields'] and value.get('ambiguous_workloads') == 0,
              'discovered workload/source/scope/unknown-field contract mismatch')

    def validate_resilience(self, value):
        expected = resilience_observation(self.apps['visible'], 'test')['view']
        check(set(value) == set(expected), 'full resilience DTO field set differs')
        for key in expected:
            if key != 'synced':
                check(value[key] == expected[key], 'nonempty protocol resilience field differs: ' + key)
        check(isinstance(value['synced'], str) and bool(value['synced']), 'resilience capture time absent')

    def proposal_body(self, investigation=None, kind='flag.set', desired=None):
        return dict(kind=kind, params=dict(investigation_id=investigation or self.investigations['visible'],
                    reason='Bounded human review; no change execution', desired=desired if desired is not None else dict(flag_key='owned-flag', enabled=False)))

    def make_proposal(self, body=None, scenario='normal'):
        before = self.ledger()
        value, _ = self.call('reader', 'POST', self.proposal, body or self.proposal_body(), want=201, scenario=scenario,
                             validate=lambda v: check(set(v) == {'proposal_id'} and re.fullmatch('[a-f0-9]{32}', v['proposal_id']), 'native proposal identity absent'))
        result = json.loads(self.sql('SELECT row_to_json(p) FROM v3_proposals p WHERE id=' + readonly.quote(value['proposal_id']), read=True))
        content = result['content']
        check(result['application_id'] == self.apps['visible'] and result['environment_id'] == 'test' and result['investigation_id'] == self.investigations['visible'] and
              result['created_by'] == self.users['reader'] and result['creator_kind'] == 'user' and content['reversible'] is False and
              'no action snapshot' in content['method'].lower(), 'native review request has wrong scope or claims execution')
        after = self.ledger()
        new = after['audit'][len(before['audit']):]
        check(after['proposals'] == before['proposals'] + 1 and len(new) == 1 and new[0]['action'] == 'v3.resilience.proposal_requested' and
              new[0]['actor_id'] == self.users['reader'] and new[0]['target'] == value['proposal_id'] and new[0]['application_id'] == self.apps['visible'] and
              new[0]['changes'] == dict(environment_id='test', investigation_id=self.investigations['visible'], kind=(body or self.proposal_body())['kind'], executable=False) and
              new[0]['outcome'] == 'success' and after['audit'][:len(before['audit'])] == before['audit'] and
              after['executions'] == before['executions'] and after['actions'] == before['actions'], 'proposal atomic audit/execution invariance differs')
        readonly.append_json(self.work / 'native-proposal-proofs.jsonl', dict(call_id=self.run_id + '-' + str(self.calls),
                             proposal=result, new_audit=new[0], before=before, after=after, evidence_mode='real_local'))
        return value['proposal_id']

    def normal(self):
        before = self.ledger()
        self.call('reader', 'GET', self.resilience, validate=self.validate_resilience)
        check(self.ledger() == before, 'resilience read changed business/audit')
        self.marker_case('services.resilience_protocol', dict(nonempty_families=5, authorities='protocol_mock', real_sources=False), mode='protocol_mock')
        first = self.make_proposal()
        second = self.make_proposal(scenario='boundary')
        check(first != second, 'human requests unexpectedly reused non-idempotent identity')
        self.marker_case('services.human_proposals', dict(proposals=2, independent_ids=True, executor_calls=0, request_idempotency_supported=False))

    def reject_unchanged(self, user, method, path, body=None, **kwargs):
        before = self.ledger()
        self.call(user, method, path, body, **kwargs)
        after = self.ledger()
        readonly.append_json(self.work / 'rejection-ledger-proofs.jsonl', dict(call_id=self.run_id + '-' + str(self.calls),
                             method=method, path=path, before=before, after=after, unchanged=after == before))
        check(after == before, 'rejected request changed business or audit')

    def authorization(self):
        before = self.protocol.count()
        for method, path, body in (('GET', self.resilience, None), ('POST', self.proposal, self.proposal_body())):
            for user, fields in ((None, dict(want=401, code='unauthorized')), ('denied', dict(want=403, code='forbidden')),
                                 ('reader', dict(environment='staging', want=403, code='forbidden'))):
                self.reject_unchanged(user, method, path, body, scenario='authorization', **fields)
        foreign = base64.urlsafe_b64encode((self.apps['foreign'] + '\0test').encode()).decode().rstrip('=')
        self.reject_unchanged('reader', 'GET', '/api/v1/services/' + foreign + '/resilience', want=403, code='forbidden', scenario='authorization')
        self.reject_unchanged('reader', 'POST', self.proposal, self.proposal_body(self.investigations['foreign']), want=404, code='not_found', scenario='authorization')
        self.reject_unchanged('reader', 'POST', self.proposal, self.proposal_body(self.investigations['staging']), want=404, code='not_found', scenario='authorization')
        check(self.protocol.count() == before, 'rejected service scope reached resilience authority')
        self.reject_unchanged('peer', 'GET', '/api/v1/services/discover', environment='staging', want=403, code='forbidden', scenario='authorization')
        self.marker_case('services.authorization', dict(cross_application=True, cross_environment=True, denied_upstream_reads=0))

    def parameters(self):
        before = self.protocol.count()
        for body in (self.proposal_body(kind='unknown'), self.proposal_body(desired={'flag_key': 'owned', 'enabled': False, 'extra': True}),
                     self.proposal_body(kind='hpa.bounds', desired={'hpa_id': 'owned', 'min': 3, 'max': 2}),
                     self.proposal_body(kind='breaker.threshold', desired={'breaker_id': 'owned', 'threshold_percent': 101})):
            self.reject_unchanged('reader', 'POST', self.proposal, body, want=422, code='invalid_resilience_proposal', scenario='boundary')
        self.reject_unchanged('reader', 'POST', self.proposal + '?unexpected=true', self.proposal_body(), want=422, code='invalid_filter', scenario='boundary')
        self.reject_unchanged('reader', 'GET', self.resilience + '?unexpected=true', want=422, code='invalid_filter', scenario='boundary')
        self.reject_unchanged('peer', 'GET', '/api/v1/services/discover?cluster=a&cluster=b', want=422, code='invalid_cluster', scenario='boundary')
        self.reject_unchanged('peer', 'GET', '/api/v1/services/discover?cluster=' + 'a' * 201, want=422, code='invalid_cluster', scenario='boundary')
        check(self.protocol.count() == before, 'invalid parameters contacted resilience source')
        self.marker_case('services.parameters', dict(unknown_fields=True, invalid_threshold=True, invalid_scope_filters=True))

    def failure_recovery(self):
        for mode in ('503', '429', 'cross_scope', 'incomplete', 'stale', 'count_mismatch', 'unknown'):
            self.protocol.mode = mode
            try:
                self.reject_unchanged('reader', 'GET', self.resilience, want=503, code='resilience_source_unavailable', scenario='failure')
            finally:
                self.protocol.mode = 'normal'
            self.call('reader', 'GET', self.resilience, index=1, scenario='failure', validate=self.validate_resilience)
        func, trigger = 'services_fault_' + self.nonce, 'services_audit_fault_' + self.nonce
        self.sql('CREATE FUNCTION ' + readonly.identifier(func) + "() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.resilience.proposal_requested' AND NEW.actor_id=" +
                 readonly.quote(self.users['reader']) + " THEN RAISE EXCEPTION 'owned audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER " +
                 readonly.identifier(trigger) + ' BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION ' + readonly.identifier(func) + '();')
        try:
            self.reject_unchanged('reader', 'POST', self.proposal, self.proposal_body(), want=503, code='resilience_unavailable', scenario='failure')
        finally:
            self.sql('DROP TRIGGER ' + readonly.identifier(trigger) + ' ON management_audit; DROP FUNCTION ' + readonly.identifier(func) + '();')
        self.make_proposal(scenario='failure')
        self.marker_case('services.failure_recovery', dict(protocol_failure_modes=7, atomic_audit_rejection=True, original_request_recovered=True, external_write_calls=0))

    def discovery_failure_recovery(self):
        source = 'services-kube-' + self.nonce + '-test'
        before = self.ledger()
        initial = json.loads(self.sql("SELECT row_to_json(s) FROM cmdb_sync_sources s WHERE id=" + readonly.quote(source), read=True))
        self.protocol.kube_failure.add('test')
        try:
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline:
                failed = json.loads(self.sql("SELECT row_to_json(s) FROM cmdb_sync_sources s WHERE id=" + readonly.quote(source), read=True))
                if failed.get('last_error'):
                    break
                time.sleep(.25)
            else:
                raise Blocked('native collector source-failure window not observed')
            # A success already in flight when the fault starts may legitimately
            # finish. Compare two observed failed attempts, beyond that boundary.
            first_failed = failed
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline:
                failed = json.loads(self.sql("SELECT row_to_json(s) FROM cmdb_sync_sources s WHERE id=" + readonly.quote(source), read=True))
                if failed.get('last_error') and failed['last_attempt_at'] != first_failed['last_attempt_at']:
                    break
                time.sleep(.25)
            else:
                raise Blocked('second native collector failure attempt not observed')
            check(failed['last_success_at'] == first_failed['last_success_at'], 'failed LIST fabricated a new successful source revision')
            # Discovery is a last-fresh-snapshot read. A current collector error
            # must not manufacture deletion or promote incomplete LIST data.
            self.call('peer', 'GET', '/api/v1/services/discover', scenario='failure', validate=lambda v: self.validate_discovery(v, 'test'))
            # Explicit exact-source stale timestamp fault, never a success seed.
            # Recovery below is exclusively a subsequent complete native LIST.
            self.sql("UPDATE cmdb_resources SET observed_at=clock_timestamp()-interval '6 minutes' WHERE source_kind='kubernetes' AND source_id=" + readonly.quote(source) + ";")
            self.call('peer', 'GET', '/api/v1/services/discover', scenario='failure', validate=lambda v: check(v.get('items') == [], 'stale source workload was not hidden'))
            readonly._private(self.work / 'discovery-failure-snapshot.json', dict(initial=initial, first_failed=first_failed, failed=failed, stale_fault_scope=source,
                              last_fresh_snapshot_retained=True, stale_hidden=True, actual_kubernetes=False))
        finally:
            self.protocol.kube_failure.discard('test')
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            recovered = json.loads(self.sql("SELECT row_to_json(s) FROM cmdb_sync_sources s WHERE id=" + readonly.quote(source), read=True))
            if not recovered.get('last_error') and recovered['last_success_at'] != failed['last_success_at']:
                break
            time.sleep(.25)
        else:
            raise Blocked('native collector complete LIST recovery not observed')
        self.call('peer', 'GET', '/api/v1/services/discover', index=1, scenario='failure', validate=lambda v: self.validate_discovery(v, 'test'))
        check(self.ledger() == before, 'collector failure/stale/recovery changed product/audit ledger')
        readonly._private(self.work / 'discovery-recovery-snapshot.json', recovered)
        self.marker_case('services.discovery_failure_recovery', dict(complete_list_failure_observed=True, snapshot_retained=True,
                         stale_fault_exact_source=source, native_complete_list_recovered=True, business_audit_unchanged=True), mode='protocol_mock')

    def revocation(self):
        self.protocol.hold = True
        self.protocol.release.clear()
        self.protocol.seen.clear()
        before = self.ledger()
        failures = []
        def pending_read():
            try:
                self.call('reader', 'GET', self.resilience, want=403, code='forbidden', scenario='authorization')
            except BaseException as error:
                failures.append(error)
        worker = threading.Thread(target=pending_read)
        worker.start()
        try:
            check(self.protocol.seen.wait(5), 'resilience read did not reach controlled external hold')
            self.sql('DELETE FROM v3_role_grants WHERE role_id=' + readonly.quote(self.roles['reader']) + " AND module='services' AND action='view';")
        finally:
            self.protocol.release.set()
            worker.join(timeout=15)
        check(not worker.is_alive() and not failures, 'read during grant revocation did not fail closed')
        check(self.ledger() == before, 'read-time revoke changed business/audit')
        before_source = self.protocol.count()
        self.reject_unchanged('reader', 'GET', self.resilience, index=1, want=403, code='forbidden', scenario='authorization')
        self.reject_unchanged('reader', 'POST', self.proposal, self.proposal_body(), index=1, want=403, code='forbidden', scenario='authorization')
        check(self.protocol.count() == before_source, 'post-revocation requests contacted source')
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + readonly.quote(self.roles['peer']) + " AND module='resources' AND action='view';")
        self.call('peer', 'GET', '/api/v1/services/discover', scenario='authorization', validate=lambda v: check(v.get('items') == [], 'revoked resource authority leaked workload'))
        self.marker_case('services.revocation', dict(read_during_source_io=True, cross_instance=True, post_revoke_upstream_reads=0, collector_workload_hidden=True))

    def proposal_grant_revocation(self):
        query = "SELECT COALESCE(json_agg(json_build_object('module',module,'action',action,'scope_kind',scope_kind,'scope_id',scope_id) ORDER BY module,action,scope_kind,scope_id),'[]') FROM v3_role_grants WHERE role_id=" + readonly.quote(self.roles['reader'])
        for module, action in (('proposals', 'change'), ('canvas', 'view')):
            grants = json.loads(self.sql(query, read=True))
            exact = dict(module=module, action=action, scope_kind='application', scope_id=self.apps['visible'])
            check(grants.count(exact) == 1, 'proposal-specific grant missing before isolated revocation')
            source_reads = self.protocol.count()
            self.sql('DELETE FROM v3_role_grants WHERE role_id=' + readonly.quote(self.roles['reader']) +
                     ' AND module=' + readonly.quote(module) + ' AND action=' + readonly.quote(action) +
                     " AND scope_kind='application' AND scope_id=" + readonly.quote(self.apps['visible']) + ';')
            try:
                revoked = json.loads(self.sql(query, read=True))
                check(revoked == [grant for grant in grants if grant != exact], 'isolated revoke changed another grant')
                self.reject_unchanged('reader', 'POST', self.proposal, self.proposal_body(), index=1,
                                      want=403, code='forbidden', scenario='authorization')
            finally:
                self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                         ','.join(readonly.quote(value) for value in (self.roles['reader'], module, action, 'application', self.apps['visible'])) + ');')
            restored = json.loads(self.sql(query, read=True))
            check(restored == grants, 'exact proposal-specific grant restoration differs')
            proposal_id = self.make_proposal(scenario='authorization')
            check(self.protocol.count() == source_reads, 'proposal-specific authority check or recovery contacted read authority')
            readonly.append_json(self.work / 'grant-revocation-proofs.jsonl', dict(grant=exact, before=grants, revoked=revoked,
                                 restored=restored, recovered_proposal_id=proposal_id, denied_http_status=403, recovered_http_status=201))
        self.marker_case('services.proposal_grant_revocation', dict(independent_grants=['proposals.change', 'canvas.view'],
                         other_grants_preserved=True, cross_instance=True, each_recovered=True, external_reads=0))

    def missing_sources(self):
        for path in ('/api/v1/services', self.detail):
            self.reject_unchanged('admin', 'GET', path, want=503, code='service_view_incomplete', scenario='failure')
        self.call('hidden', 'GET', '/api/v1/topology', environment='staging', scenario='failure',
                  validate=lambda v: check(v.get('complete_v3_dto') is False and v.get('source') == 'cmdb_resource_relations' and bool(v.get('nodes')) and
                        'complete_resource_coverage' in v.get('unavailable_fields', []) and all(x.get('environment_ids') == ['staging'] for x in v['nodes']), 'partial topology must preserve missing full coverage and environment'))
        for path, reason in BLOCKERS.items():
            self.marker_case('services.prerequisite.' + path.rsplit('/', 1)[-1], dict(path=path, reason=reason, normal_verified=False), status='blocked')

    def run(self):
        self.provision()
        self.prepare_protocols()
        self.start()
        self.login('admin')
        self.native_producers()
        self.discovery()
        before = self.ledger()
        readonly._private(self.work / 'native-baseline.json', dict(applications=self.apps, investigations=self.investigations, ledger=before))
        self.normal()
        self.authorization()
        self.parameters()
        self.failure_recovery()
        self.discovery_failure_recovery()
        self.missing_sources()
        self.proposal_grant_revocation()
        self.revocation()
        final = self.ledger()
        readonly._private(self.work / 'database-final-ledger.json', final)
        audits = final['audit'][len(before['audit']):]
        check(final['proposals'] == before['proposals'] + 5 and len(audits) == 5 and
              all(audit['action'] == 'v3.resilience.proposal_requested' for audit in audits) and
              final['audit'][:len(before['audit'])] == before['audit'] and
              final['executions'] == before['executions'] and final['actions'] == before['actions'], 'native review count/external execution invariant differs')
        self.marker_case('services.audit_consistency', dict(native_proposals=5, proposal_audits=5, read_or_rejection_audits=0, native_execution_delta=0))
        self.frozen()
        result = dict(real_normal_targets=['POST /api/v1/services/{id}/resilience/proposals'],
                      protocol_mock_normal_targets=['GET /api/v1/services/discover', 'GET /api/v1/services/{id}/resilience'],
                      blocked_normal_targets=BLOCKERS, frozen_dto_topology_accepted=False,
                      real_kubernetes_or_five_authorities_accepted=False, shared_components_untouched=True,
                      http_results=self.calls, required_cases=list(REQUIRED), acceptance_complete=False)
        readonly._private(self.work / 'result.json', result)
        return result

    def cleanup(self):
        primary = None
        try:
            super().cleanup()
        except BaseException as error:
            primary = error
        try:
            self.protocol.close()
            free_ports()
            readonly._private(self.work / 'protocol-cleanup.json', dict(ports_free=[*PORTS, RESILIENCE_PORT, KUBE_PORT], held_servers_closed=True, shared_components_removed=False))
        except BaseException as error:
            primary = primary or error
        if primary is not None:
            raise primary


def qualify(chain, rows, status, final_source):
    """Derive normal proof from completed cases and receipts, never a status label."""
    def private_json(name, lines=False):
        path = chain.work / name
        try:
            if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode) != 0o600:
                return None
            content = path.read_text()
            return [json.loads(line) for line in content.splitlines()] if lines else json.loads(content)
        except (OSError, ValueError):
            return None

    result, cleanup, protocols = (private_json(name) for name in ('result.json', 'cleanup.json', 'protocol-cleanup.json'))
    interfaces = private_json('interfaces.jsonl', lines=True)
    passed = {row.get('name') for row in rows if row.get('status') == 'passed'}
    reasons = []
    if status not in ('passed', 'blocked') or any(row.get('status') in ('failed', 'interrupted') for row in rows):
        reasons.append('failed or interrupted package/case')
    if chain.data['source_tree_sha'] != SOURCE_SHA or final_source != SOURCE_SHA:
        reasons.append('frozen source mismatch')
    if not set(REQUIRED).issubset(passed) or not isinstance(result, dict) or result.get('required_cases') != list(REQUIRED):
        reasons.append('required scenario completion/result missing')
    if not isinstance(cleanup, dict) or not all(cleanup.get(field) is True for field in ('database_absent', 'role_absent', 'owned_processes_exited')):
        reasons.append('owned API/database/role cleanup unverified')
    if not isinstance(protocols, dict) or protocols.get('held_servers_closed') is not True or protocols.get('ports_free') != [*PORTS, RESILIENCE_PORT, KUBE_PORT]:
        reasons.append('protocol server/four-port cleanup unverified')
    if not isinstance(interfaces, list):
        reasons.append('persistent HTTP evidence missing')
    qualified = {'real_local': {}, 'protocol_mock': {}}
    if not reasons:
        persistent = {row.get('call_id'): row for row in interfaces}
        for row in rows:
            cell = row.get('interface', {})
            target = cell.get('method', '') + ' ' + cell.get('path', '')
            mode = NORMAL_TARGET_MODES.get(target)
            code = cell.get('actual', {}).get('status', 0)
            if (mode and row.get('status') == cell.get('status') == 'passed' and
                row.get('evidence_mode') == cell.get('evidence_mode') == mode and
                cell.get('route_class') == 'main' and cell.get('scenario') == 'normal' and
                cell.get('scenario_detail', 'normal') == 'normal' and cell.get('registration_evidence') is True and
                isinstance(code, int) and 200 <= code < 300 and cell.get('run_id') == chain.run_id and
                cell.get('call_id', '').startswith(chain.run_id + '-') and persistent.get(cell.get('call_id')) == cell):
                qualified[mode].setdefault(target, []).append(cell['call_id'])
    return dict(ready=not reasons, rejection_reasons=reasons, normal_http_proofs=qualified)


def handoff(chain, status, error=None):
    rows = [json.loads(line) for line in (chain.work / 'cases.jsonl').read_text().splitlines()] if (chain.work / 'cases.jsonl').exists() else []
    final_source = readonly.source_fingerprint(chain.root)
    qualification = qualify(chain, rows, status, final_source)
    evidence = dict(run_id=chain.run_id, status=status, source_tree_sha=chain.data['source_tree_sha'], source_tree_sha_end=final_source,
                    binary_sha256=chain.data['binary_sha256'], main_runtime_sha=RUNTIME_SHA, helper_sha256=chain.data['helper_sha256'],
                    source_unchanged=final_source == SOURCE_SHA, module=MODULE, started_at=chain.state['started_at'], ended_at=now(),
                    case_counts={kind: sum(row['status'] == kind for row in rows) for kind in ('passed', 'failed', 'blocked')},
                    real_normal_targets=list(qualification['normal_http_proofs']['real_local']),
                    protocol_mock_normal_targets=list(qualification['normal_http_proofs']['protocol_mock']), qualification=qualification,
                    remaining=BLOCKERS, error_type=type(error).__name__ if error else None, cases=rows,
                    artifacts={'first_red': str(chain.work / 'first-failure.json'), 'cleanup': str(chain.work / 'cleanup.json'),
                               'raw_http': str(chain.work), 'protocol_reads': str(chain.work / 'source-reads.jsonl')},
                    no_git_or_shared_manifest_changes=True, performance_or_soak=False, full_acceptance=False)
    readonly._private(chain.work / 'handoff.json', evidence)
    text = '# Services functional package\n\n' + json.dumps({k: v for k, v in evidence.items() if k != 'cases'}, ensure_ascii=False, indent=2) + '\n'
    readonly.private_file(chain.work / 'handoff.md', text)
    import html
    readonly.private_file(chain.work / 'handoff.html', '<!doctype html><meta charset="utf-8"><title>Services functional package</title><pre>' + html.escape(text) + '</pre>')
    readonly._private(chain.root / '.local/testlab/services-parallel/handoff-latest.json', dict(run_id=chain.run_id, status=status, handoff=str(chain.work / 'handoff.json')))
    return evidence


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--run-id')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--inventory', action='store_true')
    mode.add_argument('--live', action='store_true')
    args = parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(dict(module=MODULE, targets=TARGETS, blocked=BLOCKERS, real_targets=['POST resilience/proposals'],
                              protocol_targets=['GET services/discover', 'GET resilience'], ports=[*PORTS, RESILIENCE_PORT, KUBE_PORT], seed=SEED, offline_only=True)))
        return 0
    global ACTIVE
    chain, error, status, report = None, None, 'failed', None
    try:
        with exclusive(args.root), readonly.cooperative_signals():
            data = doctor(args.root)
            chain = Chain(args.root, data, args.run_id or str(uuid.uuid4()))
            ACTIVE = chain
            chain.state['started_at'] = now()
            readonly._private(chain.work / 'state.json', chain.state)
            readonly._private(chain.work / 'doctor-before.json', {k: data[k] for k in ('owner', 'source_tree_sha', 'binary_sha256', 'runtime_sha', 'postgres_container_id', 'helper_sha256')})
            readonly.execute(chain)
            status = 'blocked'  # Real/Mock subscenarios passed; full-source prerequisites did not.
    except BaseException as failure:
        error = failure
        status = 'interrupted' if isinstance(failure, (KeyboardInterrupt, SystemExit)) else 'failed'
        emit('services.package_error', status, dict(error_type=type(failure).__name__, reason=str(failure)))
    finally:
        if chain is not None:
            report = handoff(chain, status, error)
            emit('services.handoff', status, dict(run_id=chain.run_id, handoff=str(chain.work / 'handoff.json'), counts=report['case_counts'], full_acceptance=False))
        ACTIVE = None
    # Exit reports collection/lifecycle completion. The formal Runner persists
    # the explicit prerequisite cases and derives overall blocked (exit 2).
    return 0 if status == 'blocked' and error is None and report['qualification']['ready'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
