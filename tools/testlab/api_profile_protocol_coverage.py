"""Four formal self-account routes with HTTPS snapshot protocol Mocks.

Upstreams only read evidence. Account changes and audit are native HTTP/PG.
Real on-call assignments, verified contacts and device identity remain TODO.
Only root schedules --live; offline tests never acquire sockets or databases.
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
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import api_readonly_chain as readonly

ROOT = readonly.ROOT
MODULE = 'formal-profile-protocol'
MOCK_PORT = 18616
INSTANCE = 'i-' + hashlib.sha256(b'nonprod\0testlab.local').hexdigest()[:32]
TARGETS = ('GET /api/v1/me', 'PATCH /api/v1/me', 'GET /api/v1/me/profile', 'PUT /api/v1/me/profile')
REQUIRED_CASES = ('profile.https_ready', 'profile.me.normal', 'profile.profile.normal',
                  'profile.authorization', 'profile.readonly_inputs', 'profile.upstream_faults',
                  'profile.account_audit_linkage')
PROOF_SCOPE = 'Formal dual V3 self-account HTTP/local transaction/audit with HTTPS snapshot protocol Mock; not real oncall/contact/device/org authority'
check, Blocked = readonly.check, readonly.Blocked


def emit(name, status, actual, **fields):
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
        evidence_mode='protocol_mock', **fields), ensure_ascii=False), flush=True)


def stamp(delta=0):
    return (datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=delta)).isoformat().replace('+00:00', 'Z')


class SnapshotProtocol:
    def __init__(self, identities, tokens):
        self.identities, self.tokens = dict(identities), dict(tokens)
        self.lock, self.calls, self.requests = threading.Lock(), [], []
        self.mode = 'normal'

    def set_mode(self, mode):
        if mode not in ('normal', '429', 'response_loss', 'wrong_scope'):
            raise Blocked('unsupported bounded snapshot fault')
        with self.lock: self.mode = mode

    def dispatch(self, path, authorization, body):
        if path not in ('/oncall', '/profile'):
            return 404, {}, False
        if authorization != 'Bearer ' + self.tokens[path]:
            return 401, {}, False
        if not isinstance(body, dict) or body.get('user_id') not in self.identities:
            return 422, {}, False
        with self.lock:
            self.calls.append(path)
            if path == '/oncall':
                if set(body) != {'user_id', 'environment_ids'} or body['environment_ids'] != ['test']:
                    return 422, {}, False
                answer = dict(user_id=body['user_id'], environment_ids=['test'], observed_at=stamp(),
                              valid_until=stamp(60), assignments=[])
            else:
                required = {'instance_id', 'user_id', 'environment_id', 'current_session_id', 'contacts', 'session_ids'}
                sessions, current = body.get('session_ids'), body.get('current_session_id')
                if (set(body) != required or body['instance_id'] != INSTANCE or body['environment_id'] != 'test'
                        or body['contacts'] != [{'kind': 'email', 'value': self.identities[body['user_id']]}]
                        or not isinstance(sessions, list) or not 0 < len(sessions) <= 100
                        or any(not isinstance(x, str) or not re.fullmatch('[a-f0-9]{64}', x) for x in sessions)
                        or len(set(sessions)) != len(sessions) or current not in sessions):
                    return 422, {}, False
                answer = dict(instance_id=INSTANCE, user_id=body['user_id'], environment_id='test',
                    complete=True, observed_at=stamp(), valid_until=stamp(60),
                    contacts=[dict(kind='email', value=self.identities[body['user_id']], verified=False,
                                   label='Synthetic email', note='Protocol Mock; contact not verified')],
                    mfa_enabled=False, org_rule='Protocol Mock; real organization policy unverified', factors=[],
                    sessions=[dict(id=x, device='Synthetic protocol device', login_method='password') for x in sessions])
            self.requests.append(copy.deepcopy(body))
            if self.mode == '429': return 429, {}, False
            if self.mode == 'wrong_scope':
                if path == '/oncall': answer['user_id'] = 'foreign-account'
                else: answer['environment_id'] = 'staging'
            return 200, answer, self.mode == 'response_loss'


class MockSnapshots:
    def __init__(self, protocol, cert, key):
        self.protocol, self.cert, self.key = protocol, Path(cert), Path(key)
        self.server = self.thread = None

    def start(self):
        protocol = self.protocol
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_POST(self):
                self.connection.settimeout(5)
                try:
                    size = int(self.headers.get('Content-Length', '0'))
                    if self.headers.get('Content-Type') != 'application/json' or not 0 < size <= 65536:
                        self.send_error(400); return
                    body = json.loads(self.rfile.read(size))
                    status, answer, lose = protocol.dispatch(self.path, self.headers.get('Authorization'), body)
                    if lose:
                        self.close_connection = True; self.connection.shutdown(socket.SHUT_RDWR); return
                    raw = json.dumps(answer).encode()
                    self.send_response(status); self.send_header('Content-Type', 'application/json')
                    self.send_header('Content-Length', str(len(raw))); self.end_headers(); self.wfile.write(raw)
                except (ValueError, OSError): self.close_connection = True
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER); context.load_cert_chain(self.cert, self.key)
        class Server(ThreadingHTTPServer):
            def get_request(self):
                connection, address = super().get_request(); connection.settimeout(3)
                try: return context.wrap_socket(connection, server_side=True), address
                except BaseException: connection.close(); raise
        self.server = Server(('127.0.0.1', MOCK_PORT), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': .1}, daemon=True)
        self.thread.start()

    def close(self):
        if self.server:
            if self.thread and self.thread.is_alive(): self.server.shutdown()
            self.server.server_close()
        if self.thread:
            self.thread.join(timeout=6); check(not self.thread.is_alive(), 'owned snapshot Mock thread leaked')


class Chain(readonly.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        identities = {uid: 'member-' + self.nonce + '-' + name + '@testlab.invalid' for name, uid in self.users.items()}
        self.protocol = SnapshotProtocol(identities, {path: secrets.token_urlsafe(32) for path in ('/oncall', '/profile')})
        self.mock, self.protocol_files = None, {}
        self.state.update(suite=MODULE, mock_port=MOCK_PORT, upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE,
                          real_authoritative_identity_verified=False, external_write_operations=0)
        readonly._private(self.work / 'state.json', self.state)

    def prepare_protocol(self):
        cert, key = self.work / 'profile-cert.pem', self.work / 'profile-key.pem'
        for path, filename in (('/oncall', 'oncall-token'), ('/profile', 'profile-token')):
            readonly.private_file(self.work / filename, self.protocol.tokens[path])
        result = subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-sha256', '-nodes', '-days', '2',
            '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1', '-keyout', str(key), '-out', str(cert)],
            capture_output=True, timeout=20, env=readonly.command_env({}, {}), umask=0o077)
        if result.returncode: raise Blocked('private snapshot TLS preparation failed')
        for path in (cert, key, self.work / 'oncall-token', self.work / 'profile-token'):
            os.chmod(path, 0o600); self.protocol_files[path.name] = hashlib.sha256(path.read_bytes()).hexdigest()
        self.mock = MockSnapshots(self.protocol, cert, key); self.mock.start()
        emit('profile.https_ready', 'passed', dict(origin='https://localhost:' + str(MOCK_PORT),
             local_ca_only=True, real_authority=False, external_write_operations=0))

    def frozen(self):
        super().frozen()
        for name, digest in self.protocol_files.items():
            path = self.work / name
            if (path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode) != 0o600
                    or hashlib.sha256(path.read_bytes()).hexdigest() != digest):
                raise Blocked('snapshot TLS/credential input changed during acceptance')

    def startup_environment(self):
        self.frozen()
        if set(self.protocol_files) != {'profile-cert.pem', 'profile-key.pem', 'oncall-token', 'profile-token'}:
            raise Blocked('protected snapshot inputs are not prepared/frozen')
        base = 'https://localhost:' + str(MOCK_PORT)
        return dict(AIOPS_V3_ONCALL_CURRENT_URL=base + '/oncall', AIOPS_V3_ONCALL_TOKEN_FILE=str(self.work / 'oncall-token'),
            AIOPS_V3_PROFILE_EVIDENCE_URL=base + '/profile', AIOPS_V3_PROFILE_EVIDENCE_TOKEN_FILE=str(self.work / 'profile-token'),
            SSL_CERT_FILE=str(self.work / 'profile-cert.pem'))

    def call_account(self, user, method, path, body=None, *, want=200, code=None, scenario='normal', index=0,
                     environment='test', validate=None):
        if (user not in (None, 'reader', 'peer') or index not in (0, 1) or environment not in ('test', 'staging')
                or method + ' ' + path not in TARGETS or scenario not in readonly.WIRE_SCENARIOS):
            raise Blocked('account invocation outside exact self-account route/scope boundaries')
        self.frozen(); self.calls += 1
        canonical, route_class = self.contract.canonical(method, path)
        headers = {'Origin': readonly.BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en'}
        if user in self.cookies: headers['Cookie'] = self.cookies[user]
        if body is not None: headers['Content-Type'] = 'application/json'
        row = dict(name='profile.http.' + str(self.calls), method=method, path=canonical, route_class=route_class,
            scenario=scenario, expected={'status': [want], 'error_code': code}, run_id=self.run_id,
            call_id=self.run_id + '-' + str(self.calls), test_name='FormalProfileProtocolChain',
            evidence_mode='protocol_mock', upstream_mode='protocol_mock', proof_scope=PROOF_SCOPE, registration_evidence=False)
        try:
            status, raw, response_headers = readonly.transport(readonly.BASES[index], method, path,
                json.dumps(body).encode() if body is not None else None, headers)
            value = json.loads(raw); actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual'] = {'status': status, 'error_code': actual_code}
            check(status == want and (code is None or actual_code == code), 'formal account HTTP status/error differs')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate: validate(value)
            row.update(status='passed', registration_evidence=200 <= status < 300 and scenario == 'normal')
        except Exception as error:
            row.setdefault('actual', {'status': None}); row.update(status='failed', registration_evidence=False)
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row); readonly.append_json(self.work / 'interfaces.jsonl', row)
            emit(row['name'], 'failed', row['actual'], **({'interface': row} if type(row['actual']['status']) is int else {})); raise
        readonly.append_json(self.work / 'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row)
        return value, {k.lower(): v for k, v in response_headers.items()}

    def account_state(self):
        ids = ','.join(readonly.quote(self.users[name]) for name in ('reader', 'peer'))
        return json.loads(self.sql("SELECT json_build_object('accounts',(SELECT json_agg(json_build_object("
            "'id',u.id,'name',u.name,'display_name',u.v3_display_name,'title',u.job_title,'timezone',u.v3_timezone,"
            "'email',u.email,'department',u.department,'settings',row_to_json(s)) ORDER BY u.id) FROM users u "
            "LEFT JOIN v3_user_settings s ON s.user_id=u.id WHERE u.id IN (" + ids + ")),"
            "'audit',(SELECT COALESCE(json_agg(json_build_object('action',action,'actor',actor_id,'target',target,"
            "'outcome',outcome,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit "
            "WHERE action IN ('v3.me.updated','v3.profile.updated')))", read=True))

    def validate_me(self, value, user='reader'):
        check(value['id'] == self.users[user] and value['email'] == self.protocol.identities[self.users[user]]
              and value['oncall'] == {'active': False} and value['mfa'] == {'totp': False, 'passkeys': 0}
              and value['roles'] == [self.roles[user]], 'self-account identity/oncall/local factor/role binding differs')

    def validate_profile(self, value, user='reader'):
        contacts = value['contacts']; sessions = value['security']['sessions']
        expected_session = hashlib.sha256(self.cookies[user].split('=', 1)[1].encode()).hexdigest()
        check(len(contacts) == 1 and contacts[0]['kind'] == 'email' and contacts[0]['value'] ==
            self.protocol.identities[self.users[user]] and contacts[0]['verified'] is False
            and contacts[0].get('note') == 'Protocol Mock; contact not verified',
            'profile asserted real contact verification or selected another account')
        check(value['security']['mfa_enabled'] is False and value['security']['factors'] == []
            and value['security']['org_rule'] == 'Protocol Mock; real organization policy unverified'
            and len(sessions) == 1 and sessions[0]['id'] == expected_session and sessions[0].get('current') is True
            and sessions[0]['device'] == 'Synthetic protocol device' and sessions[0]['sub'].startswith('Password login · Expires '),
            'profile factor/current session/organization snapshot binding differs')

    def accounts(self):
        baseline = self.account_state()
        me, _ = self.call_account('reader', 'GET', '/api/v1/me', validate=self.validate_me)
        peer_me, _ = self.call_account('peer', 'GET', '/api/v1/me', index=1,
                                     validate=lambda value: self.validate_me(value, 'peer'))
        patch = dict(name=self.marker + ' patched', title='Synthetic protocol operator', timezone='UTC',
                     preferences=dict(theme='dark', locale='en-US', density='compact', reduce_motion=True))
        changed, _ = self.call_account('reader', 'PATCH', '/api/v1/me', patch, index=1,
            validate=lambda value: check(all(value[k] == v for k, v in patch.items()), 'self patch did not match editable fields'))
        self.validate_me(changed)
        self.call_account('reader', 'GET', '/api/v1/me', validate=lambda value: check(value == changed, 'cross-instance self patch read differs'))
        self.call_account('peer', 'GET', '/api/v1/me', validate=lambda value: check(value == peer_me, 'peer self account was changed'))
        emit('profile.me.normal', 'passed', {'routes': list(TARGETS[:2]), 'native_local_write': True, 'upstream_write': False})
        profile, headers = self.call_account('reader', 'GET', '/api/v1/me/profile', index=1, validate=self.validate_profile)
        check(headers.get('cache-control') == 'private, no-store', 'profile response missing private no-store')
        desired = copy.deepcopy(profile)
        desired.update(name=self.marker + ' profile updated', display_name='Synthetic protocol display', title='Synthetic profile title',
            notifications=[dict(event='synthetic-notice', label='No real notifications sent',
            channels=dict(feishu=False, email=False, sms=False, phone=False))])
        updated, _ = self.call_account('reader', 'PUT', '/api/v1/me/profile', desired,
            validate=lambda value: check(value == desired, 'profile local editable update differs'))
        self.validate_profile(updated)
        self.call_account('reader', 'GET', '/api/v1/me/profile', index=1,
                          validate=lambda value: check(value == updated, 'profile cross-instance readback differs'))
        final_me, _ = self.call_account('reader', 'GET', '/api/v1/me', index=1, validate=self.validate_me)
        check(final_me['name'] == desired['name'] and final_me['title'] == desired['title']
              and final_me['preferences'] == patch['preferences'], 'two account projections contradict updated data')
        emit('profile.profile.normal', 'passed', {'routes': list(TARGETS[2:]), 'contact_verified': False, 'upstream_write': False})
        committed = self.account_state()
        self.validate_account_audit(baseline, committed, patch, desired)
        before = len(self.protocol.calls)
        for method, path, body in (('GET', '/api/v1/me', None), ('PATCH', '/api/v1/me', patch),
                                  ('GET', '/api/v1/me/profile', None), ('PUT', '/api/v1/me/profile', desired)):
            self.call_account(None, method, path, body, want=401, code='unauthorized', scenario='authorization')
            self.call_account('reader', method, path, body, want=403, code='forbidden', environment='staging', scenario='authorization')
        check(before == len(self.protocol.calls) and self.account_state() == committed, 'unauthorized request reached snapshot or changed local state')
        emit('profile.authorization', 'passed', {'anonymous': 401, 'outside_environment': 403, 'upstream_attempts': 0})
        for invalid in ({'email': 'forged@testlab.invalid'}, {'name': None}, {'timezone': 'Invalid/Zone'}):
            self.call_account('reader', 'PATCH', '/api/v1/me', invalid, want=422, code='invalid_body', scenario='boundary')
        invalid_profile = copy.deepcopy(desired); invalid_profile['contacts'][0]['verified'] = True
        self.call_account('reader', 'PUT', '/api/v1/me/profile', invalid_profile, want=422,
                          code='invalid_settings', scenario='boundary')
        check(self.account_state() == committed, 'read-only/invalid account change committed local state')
        emit('profile.readonly_inputs', 'passed', {'readonly_contact_mutation': 422, 'invalid_patch': 422, 'local_state_unchanged': True})
        for mode in ('429', 'response_loss', 'wrong_scope'):
            self.protocol.set_mode(mode)
            for method, path, body in (('GET', '/api/v1/me', None), ('PATCH', '/api/v1/me', patch),
                                      ('GET', '/api/v1/me/profile', None), ('PUT', '/api/v1/me/profile', desired)):
                before = len(self.protocol.calls)
                self.call_account('reader', method, path, body, want=503,
                    code='oncall_unavailable' if path.endswith('/me') else 'identity_evidence_unavailable', scenario='failure', index=1)
                check(len(self.protocol.calls) == before + 1 and self.account_state() == committed,
                      'snapshot fault retried or committed account/audit change')
        self.protocol.set_mode('normal')
        self.call_account('reader', 'GET', '/api/v1/me/profile', validate=lambda value: check(value == updated, 'profile failed to recover after read-only dependency fault'))
        emit('profile.upstream_faults', 'passed', {'faults': ['429', 'response_loss', 'wrong_scope'], 'routes': list(TARGETS),
            'attempts_per_request': 1, 'native_state_audit_unchanged': True, 'external_write_operations': 0,
            'recovery_scope': 'read-only snapshot failure; no external apply or unknown-operation reconciliation'})
        self.validate_account_audit(baseline, self.account_state(), patch, desired)
        emit('profile.account_audit_linkage', 'passed', dict(matched=True, actor=self.users['reader'], audit_count=2,
            target=self.users['reader'], exact_changes=True, peer_unchanged=True, no_failure_write_audits=True))

    def validate_account_audit(self, baseline, value, patch, desired):
        uid = self.users['reader']
        check(value['audit'] == [dict(action='v3.me.updated', actor=uid, target=uid, outcome='success', changes=patch),
            dict(action='v3.profile.updated', actor=uid, target=uid, outcome='success',
            changes=dict(fields=['name', 'display_name', 'title', 'timezone', 'notifications'], notification_events=1))],
            'native account audit actor/target/changes binding differs')
        peer = self.users['peer']
        check([x for x in value['accounts'] if x['id'] == peer] == [x for x in baseline['accounts'] if x['id'] == peer],
              'peer account/settings changed while editing self')
        rows = [x for x in value['accounts'] if x['id'] == uid]
        check(len(rows) == 1 and all(rows[0][k] == desired[k] for k in ('name', 'display_name', 'title', 'timezone')),
              'native persisted self account differs from HTTP desired state')
        settings = rows[0]['settings']
        check(settings and settings['user_id'] == uid and all(settings[k] == v for k, v in patch['preferences'].items())
              and settings['profile_notifications'] == desired['notifications'], 'native persisted settings/notification account binding differs')

    def run(self):
        self.provision(); self.prepare_protocol(); self.start(); self.seed()
        for user in ('reader', 'peer'): self.login(user)
        self.accounts(); self.frozen()
        result = dict(run_id=self.run_id, targets=list(TARGETS), http_results=self.calls, proof_scope=PROOF_SCOPE,
            real_authority_verified=False, upstream_write_operations=0,
            remaining_todo=['Real organization/oncall/device/contact verification', 'Real factor enrollment and authority agreement',
                            'Client response loss after native commit is a distinct untested boundary'])
        readonly._private(self.work / 'profile-result.json', result); return result

    def cleanup(self):
        error = None
        try:
            if self.mock: self.mock.close()
        except BaseException as failure: error = failure
        super().cleanup()
        if error: raise error


def main():
    parser = argparse.ArgumentParser(description=__doc__); parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true'); args = parser.parse_args()
    if not args.live: parser.error('execution requires root-authorized --live')
    try:
        with readonly.exclusive(args.root), readonly.cooperative_signals():
            data = readonly.doctor(args.root)
            with socket.socket() as probe:
                probe.settimeout(.3)
                if probe.connect_ex(('127.0.0.1', MOCK_PORT)) == 0: raise Blocked('snapshot Mock port occupied; no resource acquired')
            chain = Chain(args.root, data, str(uuid.uuid4())); readonly.execute(chain)
    except Blocked as error: emit('profile.environment', 'blocked', {'error': str(error)}); return 2
    except Exception as error: emit('profile.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)}); return 1
    return 0


if __name__ == '__main__': raise SystemExit(main())
