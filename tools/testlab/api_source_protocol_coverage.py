"""Formal security HTTP adapter with an isolated HTTPS protocol authority.

This proves the adapter/operation ledger only, never effective policy enforcement.
No service starts without --live; the parent owns scheduling all live resources.
Reuses readonly's exact nonce/HMAC, owned PG, UUID role/DB and cleanup guards.
"""
from __future__ import annotations

import argparse
import copy
import datetime
import hashlib
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
from urllib.parse import urlencode

import api_readonly_chain as readonly

ROOT = readonly.ROOT
MODULE = 'formal-source-protocol'
MOCK_PORT = 18614
TARGETS = ('GET /api/v1/security', 'PUT /api/v1/security')
REQUIRED_CASES = ('protocol.https_ready', 'protocol.security.normal',
                  'protocol.security.authorization', 'protocol.security.preconditions',
                  'protocol.security.upstream_429', 'protocol.security.response_loss',
                  'protocol.security.operation_linkage')
PROOF_SCOPE = 'Formal dual V3 HTTP adapter and PG operation/audit ledger; HTTPS upstream is a protocol Mock, not enforced security or real tenant acceptance'
check, Blocked = readonly.check, readonly.Blocked


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
        evidence_mode='protocol_mock', **fields), ensure_ascii=False), flush=True)


def stamp(delta=0):
    return (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=delta)).isoformat().replace('+00:00', 'Z')


def initial_view():
    return dict(sso=[], scim=False, mfa_policy='none', mfa_methods=[], approval_passkey=False,
        session=dict(idle='30m', max='12h', ip_allow='', new_device_notice=False),
        vault=dict(name='Protocol mock', sub='Not real Vault enforcement', connected=False),
        token_ttl='12h', password_policy='Protocol fixture only')


def controls_digest(controls):
    # Exactly Go SecurityControls declaration order; other JSON field order is irrelevant.
    ordered = {k: controls[k] for k in ('scim', 'mfa_policy', 'mfa_methods',
              'approval_passkey', 'ip_allow', 'new_device_notice')}
    ordered['mfa_methods'] = [dict(id=x['id'], enabled=x['enabled']) for x in controls['mfa_methods']]
    return hashlib.sha256(json.dumps(ordered, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()


class AuthorityProtocol:
    """Bounded synthetic state; dispatch is transport-free for offline tests."""
    def __init__(self, token):
        self.token, self.lock = token, threading.Lock()
        self.view, self.revision, self.mode = initial_view(), 1, 'normal'
        self.applies, self.calls, self.receipts = [], [], {}

    def set_mode(self, mode):
        if mode not in ('normal', 'snapshot_429', 'apply_response_loss'):
            raise Blocked('unknown explicitly scheduled protocol fault')
        with self.lock:
            self.mode = mode

    def dispatch(self, path, authorization, body):
        if authorization != 'Bearer ' + self.token:
            return 401, {}, False
        if path not in ('/authority/snapshot', '/authority/apply') or not isinstance(body, dict):
            return 404, {}, False
        with self.lock:
            self.calls.append(path)
            if body.get('instance_id') != 'testlab.local' or body.get('environment_id') != 'test':
                return 422, {}, False
            if path.endswith('/snapshot'):
                if set(body) != {'instance_id', 'environment_id'}:
                    return 422, {}, False
                if self.mode == 'snapshot_429':
                    return 429, {}, False
                return 200, dict(instance_id='testlab.local', environment_id='test', revision=self.revision,
                    complete=True, enforced=True, observed_at=stamp(), valid_until=stamp(60),
                    view=copy.deepcopy(self.view)), False
            if set(body) != {'instance_id', 'environment_id', 'expected_revision', 'operation_id', 'controls'}:
                return 422, {}, False
            op, controls = body['operation_id'], body['controls']
            if not isinstance(op, str) or not re.fullmatch('[a-f0-9]{32}', op) or not isinstance(controls, dict):
                return 422, {}, False
            if op in self.receipts:
                old_body, receipt = self.receipts[op]
                return (200, copy.deepcopy(receipt), False) if old_body == body else (409, {}, False)
            if type(body['expected_revision']) is not int or body['expected_revision'] != self.revision:
                return 409, {}, False
            try:
                digest = controls_digest(controls)
            except (KeyError, TypeError):
                return 422, {}, False
            if (set(controls) != {'scim', 'mfa_policy', 'mfa_methods', 'approval_passkey', 'ip_allow', 'new_device_notice'}
                    or controls['mfa_policy'] != 'none' or controls['mfa_methods'] != []
                    or any(type(controls[k]) is not bool for k in ('scim', 'approval_passkey', 'new_device_notice'))):
                return 422, {}, False
            self.revision += 1
            for key in ('scim', 'mfa_policy', 'approval_passkey'):
                self.view[key] = controls[key]
            self.view['session'].update(ip_allow=controls['ip_allow'], new_device_notice=controls['new_device_notice'])
            receipt = dict(instance_id='testlab.local', environment_id='test',
                expected_revision=body['expected_revision'], operation_id=op, controls_sha256=digest,
                revision=self.revision, durable=True, enforced=True)
            self.applies.append(dict(operation_id=op, expected_revision=body['expected_revision'], revision=self.revision,
                                     controls_sha256=digest))
            self.receipts[op] = (copy.deepcopy(body), receipt)
            return 200, copy.deepcopy(receipt), self.mode == 'apply_response_loss'


class MockAuthority:
    def __init__(self, protocol, cert, key):
        self.protocol, self.cert, self.key = protocol, Path(cert), Path(key)
        self.server = self.thread = None

    def start(self):
        protocol = self.protocol
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass  # Never log Authorization or request/session payloads.
            def do_POST(self):
                self.connection.settimeout(5)
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if self.headers.get('Content-Type') != 'application/json' or not 0 < length <= 65536:
                        self.send_error(400); return
                    body = json.loads(self.rfile.read(length))
                    status, result, lose = protocol.dispatch(self.path, self.headers.get('Authorization'), body)
                    if lose:
                        self.close_connection = True
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    raw = json.dumps(result).encode()
                    self.send_response(status)
                    self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(raw)))
                    self.end_headers(); self.wfile.write(raw)
                except (ValueError, OSError):
                    self.close_connection = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(self.cert, self.key)
        class TLSServer(ThreadingHTTPServer):
            def get_request(self):
                connection, address = super().get_request()
                connection.settimeout(3)
                try:
                    return context.wrap_socket(connection, server_side=True), address
                except BaseException:
                    connection.close()
                    raise
        server = TLSServer(('127.0.0.1', MOCK_PORT), Handler)
        self.server = server  # Adopt before wrapping/thread creation so failure cleanup still closes it.
        server.daemon_threads = True
        self.thread = threading.Thread(target=server.serve_forever, kwargs={'poll_interval': .1}, daemon=True)
        self.thread.start()

    def close(self):
        if self.server:
            if self.thread and self.thread.is_alive():
                self.server.shutdown()
            self.server.server_close()
        if self.thread:
            self.thread.join(timeout=6)
            check(not self.thread.is_alive(), 'own HTTPS mock thread remained after cleanup')


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.authority = AuthorityProtocol(secrets.token_urlsafe(32))
        self.mock = None
        self.protocol_files = {}
        self.state.update(suite=MODULE, upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE,
                          mock_port=MOCK_PORT, real_security_enforcement_verified=False)
        readonly._private(self.work / 'state.json', self.state)

    def prepare_protocol(self):
        cert, key, token = (self.work / x for x in ('authority-cert.pem', 'authority-key.pem', 'authority-token'))
        readonly.private_file(token, self.authority.token)
        command = ['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '2',
            '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1',
            '-keyout', str(key), '-out', str(cert)]
        result = subprocess.run(command, capture_output=True, timeout=20, env=readonly.command_env({}, {}), umask=0o077)
        if result.returncode:
            raise Blocked('own HTTPS certificate preparation failed; no system trust configuration changed')
        for path in (cert, key):
            os.chmod(path, 0o600)
        self.protocol_files = {path.name: hashlib.sha256(path.read_bytes()).hexdigest() for path in (cert, key, token)}
        self.mock = MockAuthority(self.authority, cert, key)
        self.mock.start()
        emit('protocol.https_ready', 'passed', dict(origin='https://localhost:' + str(MOCK_PORT),
            upstream_mode='protocol_mock', local_ca_only=True, real_enforcement=False))

    def frozen(self):
        super().frozen()
        for name, expected in self.protocol_files.items():
            path = self.work / name
            if (path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode) != 0o600
                    or hashlib.sha256(path.read_bytes()).hexdigest() != expected):
                raise Blocked('HTTPS protocol config changed during acceptance')

    def startup_environment(self):
        self.frozen()
        if set(self.protocol_files) != {'authority-cert.pem', 'authority-key.pem', 'authority-token'}:
            raise Blocked('owned HTTPS protocol files are not prepared and frozen')
        return dict(AIOPS_V3_SECURITY_AUTHORITY_URL='https://localhost:' + str(MOCK_PORT) + '/authority',
                    AIOPS_V3_SECURITY_AUTHORITY_TOKEN_FILE=str(self.work / 'authority-token'),
                    SSL_CERT_FILE=str(self.work / 'authority-cert.pem'))

    def call_security(self, user, method, body=None, *, want=200, code=None, index=0,
                      scenario='normal', etag=None, validate=None):
        if (user not in (None, 'admin', 'reader') or method not in ('GET', 'PUT') or index not in (0, 1)
                or scenario not in readonly.WIRE_SCENARIOS):
            raise Blocked('security HTTP invocation outside owned accounts/verbs/origins')
        self.frozen()
        self.calls += 1
        path = '/api/v1/security'
        canonical, route_class = self.contract.canonical(method, path)
        headers = dict(Origin=readonly.BASES[index], **{'X-AIOps-Environment': 'test', 'Accept-Language': 'en'})
        if user in self.cookies:
            headers['Cookie'] = self.cookies[user]
        if body is not None:
            headers['Content-Type'] = 'application/json'
        if etag is not None:
            if not isinstance(etag, str) or '\r' in etag or '\n' in etag:
                raise Blocked('invalid explicit security revision header')
            headers['If-Match'] = etag
        row = dict(name='protocol.http.' + str(self.calls), method=method, path=canonical, route_class=route_class,
            scenario=scenario, expected={'status': [want], 'error_code': code}, run_id=self.run_id,
            call_id=self.run_id + '-' + str(self.calls), test_name='FormalSecurityProtocolChain',
            proof_scope=PROOF_SCOPE, evidence_mode='protocol_mock', upstream_mode='protocol_mock', registration_evidence=False)
        try:
            status, raw, response_headers = readonly.transport(readonly.BASES[index], method, path,
                json.dumps(body).encode() if body is not None else None, headers)
            value = json.loads(raw)
            actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual'] = {'status': status, 'error_code': actual_code}
            check(status == want and (code is None or actual_code == code), 'formal security HTTP status/error differs')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate: validate(value)
            row.update(status='passed', registration_evidence=200 <= status < 300 and scenario == 'normal')
        except Exception as error:
            row.setdefault('actual', {'status': None})
            row.update(status='failed', registration_evidence=False)
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row)
            readonly.append_json(self.work / 'interfaces.jsonl', row)
            emit(row['name'], 'failed', row['actual'], **({'interface': row} if type(row['actual']['status']) is int else {}))
            raise
        readonly.append_json(self.work / 'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row)
        return value, {k.lower(): v for k, v in response_headers.items()}

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('operations', (SELECT COALESCE(json_agg(json_build_object("
            "'id',id,'instance_id',instance_id,'environment_id',environment_id,'actor_id',actor_id,"
            "'requested_controls',requested_controls,'status',status,'expected_revision',expected_revision,"
            "'applied_revision',applied_revision,'error_code',error_code) "
            "ORDER BY expected_revision),'[]'::json) FROM v3_security_policy_operations),"
            "'applied', (SELECT COALESCE(json_agg(json_build_object('instance_id',instance_id,'environment_id',environment_id,"
            "'runtime_revision',runtime_revision,'operation_id',operation_id,'controls',controls)),'[]'::json) FROM v3_security_policy_applied),"
            "'audit',(SELECT COALESCE(json_agg(json_build_object('action',action,'outcome',outcome,'actor',actor_id,"
            "'target',target,'changes',changes) ORDER BY id),"
            "'[]'::json) FROM management_audit WHERE action LIKE 'v3.security.%'))", read=True))

    def validate_ledger(self, value, lost=False):
        expected_count = 2 if lost else 1
        operations, applied, audits = (value[name] for name in ('operations', 'applied', 'audit'))
        check(len(operations) == len(self.authority.applies) == expected_count
              and len(applied) == 1 and len(audits) == expected_count * 2,
              'security operation/applied/audit count differs from exact authority attempts')
        actor = self.admin_id()
        for index, (operation, external) in enumerate(zip(operations, self.authority.applies)):
            expected_revision = index + 1
            op_id = external['operation_id']
            request, receipt = self.authority.receipts[op_id]
            controls = request['controls']
            unknown = index == 1
            check(operation == dict(id=op_id, instance_id='testlab.local', environment_id='test',
                  actor_id=actor, requested_controls=controls, expected_revision=expected_revision,
                  applied_revision=None if unknown else 2, status='unknown' if unknown else 'applied',
                  error_code='apply_outcome_unknown' if unknown else ''),
                  'security operation identity/scope/actor/revision/controls binding differs')
            check(request == dict(instance_id='testlab.local', environment_id='test', operation_id=op_id,
                  expected_revision=expected_revision, controls=controls)
                  and external == dict(operation_id=op_id, expected_revision=expected_revision,
                      revision=expected_revision + 1, controls_sha256=controls_digest(controls))
                  and receipt['operation_id'] == op_id and receipt['revision'] == expected_revision + 1,
                  'authority attempt/request/receipt operation binding differs')
            request_changes = dict(operation_id=op_id, expected_revision=expected_revision, controls=controls)
            finished_changes = (dict(operation_id=op_id, error_code='apply_outcome_unknown') if unknown
                else dict(operation_id=op_id, revision_before=expected_revision, revision_after=2, controls=controls))
            check(audits[index * 2:index * 2 + 2] == [
                dict(action='v3.security.change_requested', outcome='pending', actor=actor,
                     target='testlab.local/test', changes=request_changes),
                dict(action='v3.security.change_finished', outcome='unknown' if unknown else 'success', actor=actor,
                     target='testlab.local/test', changes=finished_changes)],
                'security audit target/actor/operation/revision/controls binding differs')
        first = operations[0]
        check(applied == [dict(instance_id='testlab.local', environment_id='test', runtime_revision=2,
              operation_id=first['id'], controls=first['requested_controls'])],
              'applied policy lost its successful operation/control binding or advanced after unknown apply')

    def security(self):
        view, headers = self.call_security('admin', 'GET', validate=lambda v: check(v == initial_view(), 'initial mock DTO differs'))
        tag = headers.get('etag')
        check(tag and headers.get('cache-control') == 'private, no-store', 'security ETag/cache policy missing')
        desired = copy.deepcopy(view); desired['session']['new_device_notice'] = True
        changed, changed_headers = self.call_security('admin', 'PUT', desired, etag=tag, index=1,
            validate=lambda v: check(v == desired, 'applied/readback DTO differs'))
        next_tag = changed_headers.get('etag')
        check(next_tag and next_tag != tag and len(self.authority.applies) == 1, 'exact security apply/revision missing')
        self.call_security('admin', 'GET', index=0, validate=lambda v: check(v == changed, 'cross-instance policy read differs'))
        stored = self.ledger()
        self.validate_ledger(stored)
        emit('protocol.security.normal', 'passed', dict(ledger=stored, upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE))
        before = len(self.authority.calls)
        for user, status in ((None, 401), ('reader', 403)):
            for method in ('GET', 'PUT'):
                self.call_security(user, method, desired if method == 'PUT' else None,
                    want=status, code='unauthorized' if user is None else 'forbidden', etag=next_tag, scenario='authorization')
        check(before == len(self.authority.calls), 'authorization rejection contacted upstream')
        emit('protocol.security.authorization', 'passed', {'upstream_calls_on_rejection': 0})
        self.call_security('admin', 'PUT', view, want=428, code='security_precondition_required', scenario='boundary')
        self.call_security('admin', 'PUT', view, etag=tag, index=1, want=412, code='security_changed', scenario='idempotency')
        check(len(self.authority.applies) == 1 and len(self.ledger()['operations']) == 1, 'stale/missing precondition issued an operation')
        emit('protocol.security.preconditions', 'passed', {'stale_etag': 412, 'missing_etag': 428, 'extra_applies': 0})
        self.authority.set_mode('snapshot_429')
        before = len(self.authority.calls)
        self.call_security('admin', 'GET', want=503, code='security_authority_unavailable', scenario='failure')
        check(len(self.authority.calls) == before + 1 and self.ledger() == stored, '429 retried or mutated policy ledger')
        self.authority.set_mode('normal')
        emit('protocol.security.upstream_429', 'passed', {'upstream_status': 429, 'formal_status': 503, 'attempts': 1, 'ledger_unchanged': True})
        self.authority.set_mode('apply_response_loss')
        self.call_security('admin', 'PUT', view, etag=next_tag, index=1, want=503,
                           code='security_authority_unavailable', scenario='failure')
        lost = self.ledger()
        self.validate_ledger(lost, lost=True)
        check(self.authority.revision == 3, 'response-loss synthetic authority did not advance once')
        before = len(self.authority.calls)
        self.call_security('admin', 'GET', index=0, want=503, code='security_reconciliation_required', scenario='failure')
        self.call_security('admin', 'PUT', view, etag=next_tag, index=0, want=409,
                           code='security_reconciliation_required', scenario='idempotency')
        check(len(self.authority.calls) == before and self.ledger() == lost, 'cross-instance retry duplicated unknown apply')
        emit('protocol.security.response_loss', 'passed', dict(ledger=lost, upstream_revision=3,
            local_applied_revision=2, duplicate_apply_attempts=0, remaining='No public reconciliation API; manual recovery remains TODO'))
        emit('protocol.security.operation_linkage', 'passed', dict(
            matched=True, normal_and_unknown_verified=True, external_operation_count=2,
            local_operation_count=2, audit_row_count=4, audit_operation_count=2, applied_checkpoint_count=1,
            external_operation_ids=[x['operation_id'] for x in self.authority.applies],
            local_operation_ids=[x['id'] for x in lost['operations']],
            audit_operation_ids=[x['changes']['operation_id'] for x in lost['audit']],
            applied_operation_id=lost['applied'][0]['operation_id'],
            unknown_operation_id=lost['operations'][1]['id'], unknown_applied_revision=None,
            instance_id='testlab.local', environment_id='test', scope_actor_revision_controls_matched=True))

    def admin_id(self):
        return self.sql('SELECT id FROM users WHERE email=' + readonly.quote(self.admin_email), read=True)

    def run(self):
        self.provision(); self.prepare_protocol(); self.start(); self.seed()
        for user in ('admin', 'reader'):
            self.login(user)
        self.security(); self.frozen()
        result = dict(run_id=self.run_id, targets=list(TARGETS), http_results=self.calls, proof_scope=PROOF_SCOPE,
            upstream_mode='protocol_mock', real_security_enforcement_verified=False,
            remaining_todo=['Real controller enforcement and tenant identity', 'Manual unknown-operation recovery acceptance',
                            'Knowledge-source, MCP, MFA and profile protocol routes remain separate work packages'])
        readonly._private(self.work / 'protocol-result.json', result)
        return result

    def cleanup(self):
        mock_error = None
        try:
            if self.mock: self.mock.close()
        except BaseException as error:
            mock_error = error
        super().cleanup()
        if mock_error is not None: raise mock_error


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live:
        parser.error('live resources require explicit parent-authorized --live')
    try:
        with readonly.exclusive(args.root), readonly.cooperative_signals():
            data = readonly.doctor(args.root)
            with socket.socket() as probe:
                probe.settimeout(.3)
                if probe.connect_ex(('127.0.0.1', MOCK_PORT)) == 0:
                    raise Blocked('protocol Mock port occupied; refusing resource acquisition')
            chain = Chain(args.root, data, str(uuid.uuid4()))
            readonly.execute(chain)
    except Blocked as error:
        emit('protocol.environment', 'blocked', {'error': str(error)}); return 2
    except Exception as error:
        emit('protocol.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)}); return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
