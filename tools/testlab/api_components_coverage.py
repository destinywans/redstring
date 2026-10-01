"""Six formal connector interfaces against an owned real Prometheus.

Reuses management's fresh UUID database, two nonrace V3 children, nonce/HMAC
admission, SQL attribution and bounded cleanup. SQL writes synthetic identities
and grants only; HTTP produces every connector and audit result. The only relay
endpoint is GET /-/ready on the exact owned Prometheus. Relay failures are
explicit protocol fault injection, not a claim that Prometheus itself crashed.
"""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import http.client
import json
import re
import socket
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlsplit

import api_management_chain as management

ROOT = management.ROOT
OWNER = management.OWNER
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PROM_IMAGE = 'prom/prometheus:v3.13.3'
RELAY_PORT = 18612
RELAY_URL = 'http://127.0.0.1:18612'
PROBE_LIMIT = 65536
MODULE = 'formal-api-components'
TARGETS = (('GET', '/connectors'), ('GET', '/connectors/{id}'),
           ('POST', '/connectors'), ('PATCH', '/connectors/{id}'),
           ('POST', '/connectors/{id}/test'), ('GET', '/connector-types/{type}'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/connectors/{id}/activate'),
           ('GET', '/connectors/{id}/tool-settings'))
REQUIRED_CASES = ('components.connector.register', 'components.connector.authorization',
                  'components.connector.activation_failure', 'components.connector.activation',
                  'components.connector.patch', 'components.connector.probe_failure',
                  'components.connector.recovery', 'components.connector.revocation',
                  'components.connector.audit_consistency')
Blocked, check = management.Blocked, management.check


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                          evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)


def helper_hashes(root=ROOT):
    return {name: hashlib.sha256((Path(root) / 'tools/testlab' / name).read_bytes()).hexdigest()
            for name in ('api_management_chain.py', 'api_components_coverage.py')}


def guard_prometheus(info, pinned=None):
    name = 'aiops-' + OWNER + '-prometheus'
    if not isinstance(info, dict): raise Blocked('owned Prometheus inspection is unavailable')
    config = info.get('Config', {})
    if (info.get('Name') != '/' + name or
            config.get('Labels', {}).get('aiops.testlab.owner') != OWNER or
            config.get('Image') != PROM_IMAGE or not info.get('State', {}).get('Running') or
            not re.fullmatch(r'[a-f0-9]{64}', str(info.get('Id', ''))) or
            not re.fullmatch(r'sha256:[a-f0-9]{64}', str(info.get('Image', ''))) or
            info.get('NetworkSettings', {}).get('Ports', {}).get('9090/tcp') !=
            [{'HostIp': '127.0.0.1', 'HostPort': '18570'}]):
        raise Blocked('Prometheus owner/name/image/CID/exact loopback listener mismatch')
    identity = {'name': name, 'container_id': info['Id'], 'image_id': info['Image'],
                'image': PROM_IMAGE, 'port': 18570}
    if pinned is not None and identity != pinned:
        raise Blocked('owned Prometheus immutable container/image identity changed')
    return identity


def doctor(root):
    # The reused admission verifies fresh controller HMAC and exact live API
    # listeners. It never reads business .env or adopts the canonical app DB.
    data = management.doctor(root)
    info = management.inspect_pg(data['postgres_container'])
    if info.get('Id') != PG_CID:
        raise Blocked('dedicated PostgreSQL immutable container ID changed')
    component = guard_prometheus(management.inspect_pg('aiops-' + OWNER + '-prometheus'))
    with socket.socket() as sock:
        sock.settimeout(.3)
        if sock.connect_ex(('127.0.0.1', RELAY_PORT)) == 0:
            raise Blocked('invocation relay port 18612 is occupied; refusing adoption')
    return dict(data, component_identity=component, helper_sha256=helper_hashes(ROOT))


def read_ready(pinned):
    guard_prometheus(management.inspect_pg(pinned['name']), pinned)
    conn = http.client.HTTPConnection('127.0.0.1', 18570, timeout=3)
    try:
        # No arbitrary URL/path or caller headers, redirects or credentials.
        conn.request('GET', '/-/ready')
        response = conn.getresponse()
        body = response.read(PROBE_LIMIT + 1)
        if len(body) > PROBE_LIMIT: raise Blocked('owned readiness body exceeds evidence budget')
        return response.status, body
    finally:
        conn.close()


class Relay:
    def __init__(self, pinned):
        self.pinned, self.mode = pinned, 'ready'
        self.lock = threading.Lock()
        self.requests = self.forwarded = self.injected = 0
        self.upstream_statuses = []
        self.closed = False
        relay = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_GET(self):
                if self.path != '/-/ready': self.send_error(404); return
                if self.headers.get('Authorization') or self.headers.get('PRIVATE-TOKEN'):
                    self.send_error(400); return
                with relay.lock:
                    relay.requests += 1
                    mode = relay.mode
                if mode == 'unavailable':
                    status, body = 503, b'Explicit testlab readiness fault injection'
                    with relay.lock: relay.injected += 1
                else:
                    try:
                        status, body = read_ready(relay.pinned)
                        with relay.lock:
                            relay.forwarded += 1
                            relay.upstream_statuses.append(status)
                    except (Blocked, OSError, http.client.HTTPException):
                        status, body = 502, b'Owned readiness unavailable'
                self.send_response(status)
                self.send_header('Content-Length', str(len(body)))
                self.send_header('Connection', 'close')
                self.end_headers()
                self.wfile.write(body)
            def do_POST(self): self.send_error(405)

        self.server = ThreadingHTTPServer(('127.0.0.1', RELAY_PORT), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    def set_mode(self, mode):
        if mode not in ('ready', 'unavailable'): raise Blocked('unsupported owned readiness fault')
        with self.lock: self.mode = mode

    def counts(self):
        with self.lock:
            return {'requests': self.requests, 'forwarded': self.forwarded, 'injected': self.injected,
                    'real_prometheus_statuses': list(self.upstream_statuses)}

    def close(self):
        if self.closed: return
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)
        if self.thread.is_alive() or self.server.socket.fileno() != -1:
            raise Blocked('owned readiness relay did not stop/close its listener')
        self.closed = True


class Contract(management.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment or not parsed.path.startswith('/api/v1/'):
            raise Blocked('component coverage requires the exact formal API namespace')
        short = parsed.path[len('/api/v1'):]
        matches = [pattern for candidate, pattern in (*TARGETS, *SUPPORT)
                   if candidate == method and re.fullmatch(
                       re.sub(r'\\\{[^}]+\\\}', r'[^/]+', re.escape(pattern)), short)]
        canonical = short if short in matches else matches[0] if len(matches) == 1 else None
        if canonical is None or method.lower() not in self.api['paths'].get(canonical, {}):
            raise Blocked('operation outside bounded connector coverage/public contract')
        return '/api/v1' + canonical, 'main' if (method, canonical) in self.main else 'auxiliary_or_historical'


def validate_detail(value, connector_id, status):
    check(isinstance(value, dict) and value.get('id') == connector_id and
          value.get('brand') == 'prometheus' and value.get('scope') == 'test' and
          value.get('address') == RELAY_URL and value.get('status') == status,
          'connector detail lost exact identity/scope/address/probe state')
    check(value.get('services') == [] and value.get('tools') == [] and
          value.get('queries') == [] and value.get('tags') == [],
          'registration/probe fabricated service, Agent capability or query authority')


class Chain(management.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.relay = Contract(root), None
        self.connector_id = ''
        self.state.update(suite='formal-api-components-coverage', ports=[*management.PORTS, RELAY_PORT],
                          component_identity=data['component_identity'], helper_sha256=data['helper_sha256'],
                          synthetic_input_scope='fresh synthetic identities/grants only',
                          actual_results_source='formal connector HTTP writes and read-only exact audit/registration checks',
                          proof_scope='real owned Prometheus readiness via fixed no-auth relay; injected relay failure; no metrics/Agent/credential/notification acceptance')
        management._private(self.work / 'state.json', self.state)

    def frozen(self):
        if helper_hashes(ROOT) != self.data['helper_sha256']:
            raise Blocked('component coverage or reused management helper changed during run')
        guard_prometheus(management.inspect_pg(self.data['component_identity']['name']), self.data['component_identity'])
        super().frozen()

    def pg(self, *args, **kwargs):
        deadline = kwargs.get('deadline')
        timeout = 3 if deadline is None else min(3, deadline - time.monotonic())
        if timeout <= 0: raise Blocked('owned component SQL cleanup budget exhausted')
        info = management.inspect_pg(self.data['postgres_container'], timeout=timeout)
        if info.get('Id') != PG_CID: raise Blocked('dedicated PostgreSQL immutable CID changed')
        return super().pg(*args, **kwargs)

    def seed(self):
        super().seed()
        grants = []
        for user in ('reader', 'peer', 'hidden'):
            env = 'staging' if user == 'hidden' else 'test'
            for action in (('view', 'manage') if user != 'reader' else ('view',)):
                grants.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                              ','.join(management.quote(x) for x in
                                       (self.roles[user], 'connectors', action, 'environment', env)) + ');')
        self.sql('BEGIN; ' + ' '.join(grants) + ' COMMIT;')

    def call(self, user, method, path, *args, **kwargs):
        self.contract.canonical(method, path)
        if (kwargs.get('scenario', 'normal') == 'normal' and
                kwargs.get('purpose', 'target') == 'target' and
                kwargs.get('want', 200) in range(200, 300) and kwargs.get('validate') is None):
            raise Blocked('normal target requires an explicit business-state validator')
        return super().call(user, method, path, *args, **kwargs)

    def case_marker(self, name, actual):
        check(name in REQUIRED_CASES, 'unstable component scenario marker')
        self.frozen()
        emit(name, 'passed', dict(run_id=self.run_id, database=self.database,
                                source_tree_sha=self.data['source_tree_sha'], **actual))

    def registration(self):
        return json.loads(self.sql("SELECT json_build_object('enabled',enabled,'status',status,'error_code',last_error_code,'tools',tools) FROM v3_connectors WHERE id=" +
                                   management.quote(self.connector_id) + " AND environment_id='test'", read=True))

    def assert_unchanged(self, before, label):
        check(self.relay.counts() == before, label + ' unexpectedly contacted readiness upstream')

    def register(self):
        self.call('reader', 'GET', '/api/v1/connector-types/prometheus', validate=lambda v:
                  check(v['brand'] == 'prometheus' and {x['id'] for x in v['auth_methods']} == {'none', 'bearer'} and
                        v['tools'] == [] and v['version'] == '' and v['webhook_url'] == '',
                        'type descriptor invented live capability/version/webhook'))
        body = {'type': 'prometheus', 'url': RELAY_URL, 'auth': 'none',
                'webhook': True, 'tools': ['query_metrics']}
        created, _ = self.call('peer', 'POST', '/api/v1/connectors', body, want=201,
                               validate=lambda v: check(set(v) == {'id'} and isinstance(v['id'], str) and v['id'], 'connector create identity missing'))
        self.connector_id = created['id']
        self.state['connector_id'] = self.connector_id
        management._private(self.work / 'state.json', self.state)
        self.path = '/api/v1/connectors/' + self.connector_id
        self.call('reader', 'GET', self.path, index=1,
                  validate=lambda v: validate_detail(v, self.connector_id, 'unverified'))
        self.call('reader', 'GET', '/api/v1/connectors?category=observe', validate=lambda v:
                  check([x['id'] for x in v['items']] == [self.connector_id] and
                        v['counts'] == {'ok': 0, 'unverified': 1, 'error': 0}, 'draft list scope/count differs'))
        self.call('peer', 'POST', '/api/v1/connectors', body, index=1,
                  want=409, code='connector_exists', scenario='idempotency')
        self.call('peer', 'POST', '/api/v1/connectors', dict(body, tools=['query_metrics', 'query_metrics']),
                  want=422, code='invalid_tools', scenario='boundary')
        self.call('peer', 'POST', '/api/v1/connectors', dict(body, url='http://outside.example.invalid'),
                  want=422, code='invalid_endpoint', scenario='boundary')
        self.call('reader', 'GET', '/api/v1/connector-types/unknown', want=404, code='not_found', scenario='boundary')
        check(self.registration()['enabled'] is False and self.relay.counts()['requests'] == 0,
              'draft registration reached upstream or enabled itself')
        self.case_marker('components.connector.register', {'http_created_id': self.connector_id,
                    'sql_business_writes': 0, 'upstream_requests': 0, 'webhook_requested_only': True})

    def authorization(self):
        before = self.relay.counts()
        self.call(None, 'GET', '/api/v1/connectors', want=401, code='unauthorized', scenario='authorization')
        self.call('denied', 'GET', '/api/v1/connectors', want=403, code='forbidden', scenario='authorization')
        for method, path, body in [('POST', self.path + '/activate', {}),
                                   ('POST', self.path + '/test', {}),
                                   ('PATCH', self.path, {'tools': [{'name': 'query_metrics', 'enabled': True}]})]:
            self.call('reader', method, path, body, want=403, code='forbidden', scenario='authorization')
            self.call('hidden', method, path, body, environment='staging', index=1,
                      want=404, code='not_found', scenario='authorization')
        self.call('hidden', 'GET', self.path, environment='staging', want=404, code='not_found', scenario='authorization')
        self.call('hidden', 'GET', '/api/v1/connectors', environment='staging', scenario='authorization',
                  validate=lambda v: check(v['items'] == [] and sum(v['counts'].values()) == 0, 'foreign environment leaked connector'))
        self.call('peer', 'POST', self.path + '/activate', {}, environment='all',
                  want=409, code='env_view_only', scenario='boundary')
        self.call('peer', 'POST', self.path + '/test', {}, want=409, code='connector_disabled', scenario='boundary')
        self.call('peer', 'PATCH', self.path, {'tools': [{'name': 'query_metrics', 'enabled': True}]},
                  want=409, code='connector_disabled', scenario='boundary')
        self.assert_unchanged(before, 'unauthorized/disabled draft')
        self.case_marker('components.connector.authorization', {'scope': 'reader manage denied / staging 404 / anonymous 401', 'upstream_requests': 0})

    def lifecycle(self):
        self.relay.set_mode('unavailable')
        self.call('peer', 'POST', self.path + '/activate', {}, want=502,
                  code='connector_test_failed', scenario='failure', purpose='dependency_unavailable')
        row = self.registration()
        check(row['enabled'] is False and self.relay.counts()['forwarded'] == 0,
              'failed readiness enabled draft or reached real source')
        self.case_marker('components.connector.activation_failure', {'draft_enabled': False, 'fault_source': 'owned relay HTTP 503', 'real_source_requests': 0})
        self.relay.set_mode('ready')
        self.call('peer', 'POST', self.path + '/activate', {}, index=1, validate=lambda v:
                  check(v['enabled'] is True and v['status'] == 'ok' and v['webhook_active'] is False and
                        v['credential_verified'] is False and v['verified_projects'] == 0,
                        'activation overclaimed credential/webhook/project authority'))
        check(self.registration()['enabled'] is True, 'activation did not commit')
        self.call('reader', 'GET', self.path + '/tool-settings', index=1, validate=lambda v:
                  check(v['connector_id'] == self.connector_id and v['connector_enabled'] is True and
                        v['agent_execution_available'] is False and len(v['registered_tools']) == 1 and
                        v['registered_tools'][0]['enabled'] is False, 'activation incorrectly enabled tools'))
        self.call('peer', 'POST', self.path + '/activate', {}, want=409, code='connector_enabled', scenario='idempotency')
        self.case_marker('components.connector.activation', {'source': 'owned real Prometheus /-/ready', 'webhook_active': False, 'credential_verified': False})
        tools = {'tools': [{'name': 'query_metrics', 'enabled': True}]}
        self.call('peer', 'PATCH', self.path, tools, validate=lambda v:
                  check(v['ok'] is True and len(v['tools']) == 1 and v['tools'][0]['name'] == 'query_metrics' and
                        v['tools'][0]['enabled'] is True, 'tool preference response differs'))
        self.call('reader', 'GET', self.path + '/tool-settings', index=1, validate=lambda v:
                  check(v['agent_execution_available'] is False and v['registered_tools'][0]['enabled'] is True,
                        'cross-instance preference readback differs or invented Agent execution'))
        check(self.registration()['tools'][0]['enabled'] is True, 'PATCH preference did not persist')
        self.call('peer', 'PATCH', self.path, {'tools': []}, want=422, code='invalid_tools', scenario='boundary')
        self.call('peer', 'PATCH', self.path, {'tools': [tools['tools'][0], tools['tools'][0]]},
                  want=422, code='invalid_tools', scenario='boundary')
        self.case_marker('components.connector.patch', {'exact_tool': 'query_metrics', 'enabled': True, 'agent_execution_available': False})
        self.call('peer', 'POST', self.path + '/test', {}, validate=lambda v:
                  check(v['ok'] is True and type(v['latency_ms']) is int and v['latency_ms'] >= 0, 'read-only readiness probe result differs'))
        self.relay.set_mode('unavailable')
        failed, _ = self.call('peer', 'POST', self.path + '/test', {}, want=503,
                              code='connector_test_failed', scenario='failure', purpose='dependency_unavailable')
        check(failed['error']['details']['reason'] == 'upstream_503', 'upstream failure root cause lost')
        check(self.registration()['status'] == 'error' and self.registration()['error_code'] == 'upstream_503',
              'failed probe did not commit honest registration status')
        self.call('reader', 'GET', self.path, scenario='failure',
                  validate=lambda v: validate_detail(v, self.connector_id, 'error'))
        self.case_marker('components.connector.probe_failure', {'fault_source': 'owned relay HTTP 503', 'persisted_status': 'error', 'reason': 'upstream_503'})
        self.relay.set_mode('ready')
        self.call('peer', 'POST', self.path + '/test', {}, index=1, validate=lambda v:
                  check(v['ok'] is True and v['latency_ms'] >= 0, 'cross-instance recovered readiness failed'))
        self.call('reader', 'GET', self.path, index=1, validate=lambda v: validate_detail(v, self.connector_id, 'ok'))
        self.call('reader', 'GET', '/api/v1/connectors', index=1, validate=lambda v:
                  check([x['id'] for x in v['items']] == [self.connector_id] and
                        v['counts'] == {'ok': 1, 'unverified': 0, 'error': 0}, 'recovery list/status count differs'))
        self.case_marker('components.connector.recovery', {'source': 'owned real Prometheus restored through relay', 'status': 'ok'})

    def revocation(self):
        # Authorization input mutation only; no connector or audit result SQL.
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + management.quote(self.roles['peer']) +
                 " AND module='connectors' AND action='manage' AND scope_kind='environment' AND scope_id='test';")
        before = self.relay.counts()
        self.call('peer', 'POST', self.path + '/test', {}, want=403, code='forbidden',
                  scenario='authorization', index=1)
        self.call('peer', 'PATCH', self.path, {'tools': [{'name': 'query_metrics', 'enabled': False}]},
                  want=403, code='forbidden', scenario='authorization')
        self.call('peer', 'GET', self.path, validate=lambda v: validate_detail(v, self.connector_id, 'ok'))
        self.assert_unchanged(before, 'revoked connector manager')
        self.case_marker('components.connector.revocation', {'manage_revoked': True, 'view_remains': True, 'upstream_requests_after_revoke': 0})

    def audit_consistency(self):
        rows = json.loads(self.sql('SELECT COALESCE(json_agg(json_build_object(\'action\',action,\'outcome\',outcome,\'actor_id\',actor_id,\'changes\',changes)),\'[]\'::json) FROM management_audit WHERE target=' +
                                  management.quote(self.connector_id), read=True))
        expected = {('connector.created', 'success'): 1, ('connector.activation.attempted', 'failure'): 1,
                    ('connector.activated', 'success'): 1, ('connector.tools.updated', 'success'): 1,
                    ('connector.connection.tested', 'success'): 2, ('connector.connection.tested', 'failure'): 1}
        actual = {(action, outcome): sum(row['action'] == action and row['outcome'] == outcome for row in rows)
                  for action, outcome in expected}
        check(len(rows) == sum(expected.values()) and actual == expected and
              all(row['actor_id'] == self.users['peer'] for row in rows),
              'connector lifecycle audit cardinality/outcome/actor differs')
        check(self.relay.counts() == {'requests': 5, 'forwarded': 3, 'injected': 2,
                                    'real_prometheus_statuses': [200, 200, 200]}, 'exact readiness call accounting differs')
        row = self.registration()
        check(row['enabled'] is True and row['status'] == 'ok' and row['error_code'] == '' and
              len(row['tools']) == 1 and row['tools'][0]['enabled'] is True,
              'registration recovery/revocation terminal state differs')
        self.case_marker('components.connector.audit_consistency', {'audit_entries': len(rows), 'audit_actor': self.users['peer'],
                    'readiness': self.relay.counts(), 'all_business_results_created_via_http': True})

    def result(self):
        rows = [json.loads(line) for line in (self.work / 'interfaces.jsonl').read_text().splitlines()]
        positive = sorted({(row['method'], row['path']) for row in rows
                           if row['registration_evidence'] and row['route_class'] == 'main'})
        check(set(positive) == {(method, '/api/v1' + path) for method, path in TARGETS},
              'bounded six main normal paths lack registered actual HTTP evidence')
        result = dict(suite='formal-api-components-coverage', run_id=self.run_id,
                      database=self.database, http_results=self.calls, positive_routes=[list(x) for x in positive],
                      source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
                      component_identity=self.data['component_identity'], helper_sha256=self.data['helper_sha256'],
                      required_cases=list(REQUIRED_CASES), formal_performance_acceptance=False, formal_24h_acceptance=False,
                      proof_scope=self.state['proof_scope'],
                      unverified=['credentialed Vault binding', 'metrics/logs/trace data reads', 'registered target/tool execution',
                                  'external tenants', 'concurrent revocation while waiting for connector row lock'])
        management._private(self.work / 'components-result.json', result)
        return result

    def run(self):
        self.relay = Relay(self.data['component_identity'])
        self.provision(); self.start(); self.seed()
        for user in ('admin', 'reader', 'peer', 'hidden', 'denied'): self.login(user)
        for step in (self.register, self.authorization, self.lifecycle, self.revocation, self.audit_consistency):
            self.frozen(); step()
        self.frozen()
        return self.result()

    def cleanup(self):
        primary = None
        try:
            super().cleanup()
        except BaseException as error:
            primary = error
        if self.relay is not None:
            try:
                self.relay.close()
                management._private(self.work / 'relay-cleanup.json', {'port': RELAY_PORT, 'stopped': True, 'listener_closed': True})
                emit('components.relay_cleanup', 'passed', {'port': RELAY_PORT, 'base_api_or_component_restart': False})
            except BaseException as error:
                if primary is None:
                    primary = error
                else:
                    # Keep the first cleanup failure even if closing/reporting
                    # the owned relay also fails. Never resume a leaked run.
                    try:
                        management._private(self.work / 'relay-cleanup-error.json',
                                            {'error_type': type(error).__name__, 'reason': str(error)})
                    except BaseException:
                        pass
        if primary is not None:
            raise primary


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--inventory', action='store_true')
    mode.add_argument('--live', action='store_true')
    args = parser.parse_args(argv)
    if args.inventory:
        print(json.dumps({'module': MODULE, 'targets': TARGETS, 'support': SUPPORT,
                          'required_cases': REQUIRED_CASES, 'ports': [*management.PORTS, RELAY_PORT],
                          'offline_only': True, 'sql_scope': 'synthetic identity/grants only and READ ONLY business readback',
                          'required_component': {'name': 'owned Prometheus', 'port': 18570, 'image': PROM_IMAGE},
                          'proof_scope': 'real source readiness; fault injected at owned relay; no metric or credential acceptance'}, ensure_ascii=False))
        return 0
    original = management.emit
    management.emit = emit
    try:
        with management.exclusive(args.root), management.cooperative_signals():
            data = doctor(args.root)
            emit('components.doctor', 'passed', {'source_tree_sha': data['source_tree_sha'],
                 'component_identity': data['component_identity'], 'helper_sha256': data['helper_sha256'], 'postgres_container_id': PG_CID})
            management.execute(Chain(args.root, data, str(uuid.uuid4())))
    except Blocked as error:
        emit('components.environment', 'blocked', {'error_type': type(error).__name__, 'reason': str(error)})
        return 2
    except BaseException as error:
        emit('components.failure', 'interrupted' if isinstance(error, (KeyboardInterrupt, SystemExit)) else 'failed',
             {'error_type': type(error).__name__})
        return 1
    finally:
        management.emit = original
    return 0


if __name__ == '__main__': raise SystemExit(main())
