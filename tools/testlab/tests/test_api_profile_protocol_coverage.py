"""Offline account protocol boundaries: every HTTP/SQL/process/socket is mocked."""
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as readonly
import api_profile_protocol_coverage as profile


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.uid = 'a' * 32; self.email = 'synthetic@testlab.invalid'
        self.source = profile.SnapshotProtocol({self.uid: self.email}, {'/oncall': 'oncall-token', '/profile': 'profile-token'})
        self.request = dict(instance_id=profile.INSTANCE, user_id=self.uid, environment_id='test',
            current_session_id='b' * 64, contacts=[{'kind': 'email', 'value': self.email}], session_ids=['b' * 64])

    def ask(self, path='/profile', body=None):
        return self.source.dispatch(path, 'Bearer ' + self.source.tokens[path], body or self.request)

    def test_snapshots_are_readonly_bound_and_never_assert_verified_contact_or_factor(self):
        status, value, lose = self.ask()
        self.assertEqual((status, lose), (200, False))
        self.assertEqual(value['instance_id'], profile.INSTANCE)
        self.assertEqual(value['user_id'], self.uid)
        self.assertFalse(value['contacts'][0]['verified']); self.assertFalse(value['mfa_enabled'])
        self.assertEqual(value['factors'], [])
        self.assertEqual(value['sessions'][0]['id'], self.request['current_session_id'])
        self.assertEqual(self.source.requests, [self.request])
        self.assertNotIn('operation_id', value)

    def test_oncall_inactive_is_explicit_complete_requested_scope_without_invented_assignment(self):
        request = dict(user_id=self.uid, environment_ids=['test'])
        status, value, _ = self.ask('/oncall', request)
        self.assertEqual(status, 200); self.assertEqual(value['user_id'], self.uid)
        self.assertEqual(value['environment_ids'], ['test']); self.assertEqual(value['assignments'], [])

    def test_tokens_are_path_specific_and_foreign_tokens_do_not_read_snapshot(self):
        for path, token in [('/profile', 'oncall-token'), ('/oncall', 'profile-token'), ('/profile', '')]:
            self.assertEqual(self.source.dispatch(path, 'Bearer ' + token, self.request)[0], 401)
        self.assertEqual(self.source.requests, []); self.assertEqual(self.source.calls, [])

    def test_profile_rejects_foreign_identity_instance_environment_contact_and_current_session(self):
        for update in [dict(user_id='foreign'), dict(instance_id='foreign'), dict(environment_id='staging'),
            dict(contacts=[{'kind': 'email', 'value': 'foreign@testlab.invalid'}]), dict(current_session_id='c' * 64),
            dict(session_ids=['raw-cookie']), dict(session_ids=['b' * 64, 'b' * 64]), dict(extra='field')]:
            with self.subTest(update=update): self.assertEqual(self.ask(body=dict(self.request, **update))[0], 422)
        self.assertEqual(self.source.requests, [])

    def test_oncall_does_not_accept_extra_fields_or_ambiguous_scope(self):
        for request in [dict(user_id=self.uid, environment_ids=['test', 'test']), dict(user_id=self.uid, environment_ids=['staging']),
            dict(user_id=self.uid, environment_ids=['test'], instance_id=profile.INSTANCE)]:
            self.assertEqual(self.ask('/oncall', request)[0], 422)

    def test_faults_never_turn_reads_into_external_writes_and_snapshot_loss_occurs_after_answer(self):
        for mode, want, lose in [('429', 429, False), ('response_loss', 200, True), ('wrong_scope', 200, False)]:
            self.source.set_mode(mode)
            status, value, lost = self.ask()
            self.assertEqual((status, lost), (want, lose))
            if mode == 'wrong_scope': self.assertEqual(value['environment_id'], 'staging')
        self.assertEqual(len(self.source.calls), 3); self.assertEqual(len(self.source.requests), 3)
        self.assertFalse(hasattr(self.source, 'applies'))

    def test_unknown_faults_and_paths_fail_closed(self):
        with self.assertRaises(readonly.Blocked): self.source.set_mode('retry_apply')
        self.assertEqual(self.source.dispatch('/apply', 'Bearer profile-token', self.request)[0], 404)


class ChainTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls): cls.contract = readonly.Contract(readonly.ROOT)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        data = dict(owner=readonly.OWNER, binary='unused', binary_sha256='unused', source_tree_sha='unused')
        with patch.object(readonly, 'Contract', return_value=self.contract):
            self.chain = profile.Chain(self.root, data, str(uuid.uuid4()))
        self.chain.cookies['reader'] = 'aiops_session=offline-synthetic-cookie'
        self.uid = self.chain.users['reader']
        self.body = dict(name=self.chain.marker + ' reader', display_name='', department='', title='', timezone='UTC',
            contacts=[dict(kind='email', label='Email', value=self.chain.protocol.identities[self.uid], verified=False,
                note='Protocol Mock; contact not verified')], notifications=[],
            security=dict(password_changed='Password change time not recorded', mfa_enabled=False,
                org_rule='Protocol Mock; real organization policy unverified', factors=[],
                sessions=[dict(id=hashlib.sha256(b'offline-synthetic-cookie').hexdigest(), device='Synthetic protocol device',
                    sub='Password login · Expires 2026-10-02T00:00:00Z', current=True)], tokens='Personal tokens are not configured'))

    def test_reuses_owned_db_nonce_and_lifecycle_without_touching_security_helper(self):
        for name in ('start', 'seed', 'provision', 'pg'):
            self.assertIs(getattr(profile.Chain, name), getattr(readonly.Chain, name))
        self.assertEqual(self.chain.state['ports'], list(readonly.PORTS))
        self.assertEqual(self.chain.state['mock_port'], 18616)
        self.assertEqual(self.chain.state['external_write_operations'], 0)
        self.assertFalse(self.chain.state['real_authoritative_identity_verified'])
        for token in self.chain.protocol.tokens.values(): self.assertNotIn(token, (self.chain.work / 'state.json').read_text())

    def protected_files(self):
        for name in ('profile-cert.pem', 'profile-key.pem', 'oncall-token', 'profile-token'):
            path = self.chain.work / name; readonly.private_file(path, 'offline ' + name)
            self.chain.protocol_files[name] = hashlib.sha256(path.read_bytes()).hexdigest()

    def test_startup_variables_require_private_frozen_exact_files_and_do_not_override_parent_env(self):
        with patch.object(readonly.Chain, 'frozen'), self.assertRaises(readonly.Blocked): self.chain.startup_environment()
        self.protected_files()
        with patch.object(readonly.Chain, 'frozen'): variables = self.chain.startup_environment()
        self.assertEqual(set(variables), {'AIOPS_V3_ONCALL_CURRENT_URL', 'AIOPS_V3_ONCALL_TOKEN_FILE',
            'AIOPS_V3_PROFILE_EVIDENCE_URL', 'AIOPS_V3_PROFILE_EVIDENCE_TOKEN_FILE', 'SSL_CERT_FILE'})
        self.assertEqual(variables['AIOPS_V3_PROFILE_EVIDENCE_URL'], 'https://localhost:18616/profile')
        self.assertNotIn('DATABASE_URL', variables); self.assertNotIn('AIOPS_ADDR', variables)

    def test_mode_change_secret_mutation_and_symlink_cannot_escape_freeze(self):
        self.protected_files(); path = self.chain.work / 'profile-token'
        with patch.object(readonly.Chain, 'frozen'):
            path.chmod(0o644)
            with self.assertRaises(readonly.Blocked): self.chain.frozen()
            path.chmod(0o600); path.write_text('changed')
            with self.assertRaises(readonly.Blocked): self.chain.frozen()
            path.unlink(); path.symlink_to(self.chain.work / 'oncall-token')
            with self.assertRaises(readonly.Blocked): self.chain.frozen()

    def test_profile_projection_is_bound_to_unverified_contact_and_current_hashed_session(self):
        self.chain.validate_profile(self.body)
        for mutate in [lambda value: value['contacts'][0].update(verified=True),
                       lambda value: value['security']['sessions'][0].update(id='f' * 64),
                       lambda value: value['security'].update(mfa_enabled=True)]:
            value = copy.deepcopy(self.body); mutate(value)
            with self.assertRaises(AssertionError): self.chain.validate_profile(value)

    def test_profile_rejects_organization_policy_not_from_exact_protocol_snapshot(self):
        value = copy.deepcopy(self.body); value['security']['org_rule'] = 'Real tenant verified'
        with self.assertRaises(AssertionError): self.chain.validate_profile(value)

    def test_formal_normal_cells_remain_protocol_mock_despite_real_http_registration(self):
        with (patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport', return_value=(200, json.dumps(self.body).encode(), {})),
              patch.object(profile, 'emit') as emit):
            self.chain.call_account('reader', 'GET', '/api/v1/me/profile', validate=self.chain.validate_profile)
        row = emit.call_args.kwargs['interface']
        self.assertTrue(row['registration_evidence']); self.assertEqual(row['evidence_mode'], 'protocol_mock')
        self.assertEqual(row['upstream_mode'], 'protocol_mock')

    def test_transport_failure_keeps_first_failure_and_does_not_invent_a_received_http_cell(self):
        with (patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport', side_effect=OSError('offline read loss')),
              patch.object(profile, 'emit') as emit, self.assertRaises(OSError)):
            self.chain.call_account('reader', 'GET', '/api/v1/me')
        self.assertNotIn('interface', emit.call_args.kwargs)
        self.assertTrue((self.chain.work / 'first-failure.json').is_file())

    def test_account_origin_user_scope_verb_and_route_boundary_reject_before_transport(self):
        for args, options in [(('admin', 'GET', '/api/v1/me'), {}), (('reader', 'PUT', '/api/v1/me'), {}),
            (('reader', 'GET', '/api/v1/me/profile'), {'environment': 'all'}),
            (('reader', 'GET', '/api/v1/me'), {'index': 2}), (('reader', 'GET', '//evil'), {})]:
            with patch.object(readonly, 'transport') as transport, self.assertRaises(readonly.Blocked):
                self.chain.call_account(*args, **options)
            transport.assert_not_called()

    def test_readback_is_only_sql_select_and_audit_includes_binding_fields(self):
        with patch.object(self.chain, 'sql', return_value='{}') as sql: self.chain.account_state()
        self.assertEqual(sql.call_args.kwargs, {'read': True})
        statement = sql.call_args.args[0]
        for forbidden in ('INSERT ', 'UPDATE ', 'DELETE ', 'TRUNCATE '): self.assertNotIn(forbidden, statement)
        for required in ('actor_id', 'target', 'changes', 'v3_user_settings'): self.assertIn(required, statement)

    def audit_fixture(self):
        uid, peer = self.uid, self.chain.users['peer']
        preferences = dict(theme='dark', locale='en-US', density='compact', reduce_motion=True)
        desired = copy.deepcopy(self.body)
        desired.update(name='updated self', display_name='Display', title='Synthetic title',
            notifications=[dict(event='synthetic', label='No notification', channels=dict(feishu=False, email=False, sms=False, phone=False))])
        patch_body = dict(name='initial patch', title='initial title', timezone='UTC', preferences=preferences)
        baseline = dict(accounts=[dict(id=uid), dict(id=peer, name='peer', settings=None)], audit=[])
        account = {k: desired[k] for k in ('name', 'display_name', 'title', 'timezone')}
        account.update(id=uid, settings=dict(user_id=uid, **preferences, profile_notifications=desired['notifications']))
        final = dict(accounts=[account, copy.deepcopy(baseline['accounts'][1])], audit=[
            dict(action='v3.me.updated', actor=uid, target=uid, outcome='success', changes=copy.deepcopy(patch_body)),
            dict(action='v3.profile.updated', actor=uid, target=uid, outcome='success',
                changes=dict(fields=['name', 'display_name', 'title', 'timezone', 'notifications'], notification_events=1))])
        return baseline, final, patch_body, desired

    def test_exact_local_account_audit_changes_and_settings_linkage(self):
        self.chain.validate_account_audit(*self.audit_fixture())

    def test_actor_target_changes_settings_and_peer_pollution_fail_precisely(self):
        for mutate in [lambda value: value['audit'][0].update(actor='foreign'),
                       lambda value: value['audit'][1].update(target='foreign'),
                       lambda value: value['audit'][0]['changes'].update(title='unrelated write'),
                       lambda value: value['accounts'][0]['settings'].update(user_id='foreign'),
                       lambda value: value['accounts'][0]['settings'].update(reduce_motion=False),
                       lambda value: value['accounts'][1].update(name='peer modified')]:
            baseline, value, patch_body, desired = self.audit_fixture(); mutate(value)
            with self.assertRaises(AssertionError): self.chain.validate_account_audit(baseline, value, patch_body, desired)

    def test_four_route_workflow_faults_use_readonly_snapshot_and_keep_two_native_audits(self):
        self.chain.cookies['peer'] = 'aiops_session=offline-peer-cookie'
        state = dict(accounts=[], audit=[])
        for name in ('reader', 'peer'):
            state['accounts'].append(dict(id=self.chain.users[name], name=self.chain.marker + ' ' + name, display_name='',
                title='', timezone='UTC', email=self.chain.protocol.identities[self.chain.users[name]], department='', settings=None))
        def account(user): return next(x for x in state['accounts'] if x['id'] == self.chain.users[user])
        def me(user):
            row = account(user)
            settings = row['settings'] or dict(theme='system', locale='zh-CN', density='comfortable', reduce_motion=False)
            return dict(id=row['id'], name=row['name'], email=row['email'], phone='', title=row['title'], team='',
                department='', timezone=row['timezone'], oncall={'active': False}, roles=[self.chain.roles[user]],
                mfa={'totp': False, 'passkeys': 0}, preferences={key: settings[key] for key in ('theme', 'locale', 'density', 'reduce_motion')})
        def profile_view(user):
            row = account(user); value = copy.deepcopy(self.body)
            value.update(**{key: row[key] for key in ('name', 'display_name', 'title', 'timezone')},
                notifications=(row['settings'] or {}).get('profile_notifications', []))
            value['contacts'][0]['value'] = row['email']
            value['security']['sessions'][0]['id'] = hashlib.sha256(self.chain.cookies[user].split('=', 1)[1].encode()).hexdigest()
            return value
        def call(user, method, path, body=None, **kwargs):
            code = None
            if user is None: status, code, value = 401, 'unauthorized', {}
            elif kwargs.get('environment', 'test') == 'staging': status, code, value = 403, 'forbidden', {}
            elif method == 'PATCH' and (set(body) - {'name', 'title', 'timezone', 'preferences'} or body.get('name', '') is None
                or body.get('timezone', 'UTC') == 'Invalid/Zone'): status, code, value = 422, 'invalid_body', {}
            else:
                endpoint = '/oncall' if path.endswith('/me') else '/profile'
                uid = self.chain.users[user]; session = hashlib.sha256(self.chain.cookies[user].split('=', 1)[1].encode()).hexdigest()
                request = (dict(user_id=uid, environment_ids=['test']) if endpoint == '/oncall' else
                    dict(instance_id=profile.INSTANCE, user_id=uid, environment_id='test', current_session_id=session,
                         session_ids=[session], contacts=[{'kind': 'email', 'value': self.chain.protocol.identities[uid]}]))
                upstream, snapshot, lost = self.chain.protocol.dispatch(endpoint, 'Bearer ' + self.chain.protocol.tokens[endpoint], request)
                if upstream != 200 or lost or (endpoint == '/oncall' and snapshot['user_id'] != uid) or (
                    endpoint == '/profile' and snapshot['environment_id'] != 'test'):
                    status, code, value = 503, 'oncall_unavailable' if endpoint == '/oncall' else 'identity_evidence_unavailable', {}
                elif method == 'PUT' and body['contacts'] != profile_view(user)['contacts']:
                    status, code, value = 422, 'invalid_settings', {}
                else:
                    row = account(user)
                    if method == 'PATCH':
                        for key in ('name', 'title', 'timezone'): row[key] = body[key]
                        row['settings'] = dict(user_id=uid, **body['preferences'], profile_notifications=[])
                        state['audit'].append(dict(action='v3.me.updated', actor=uid, target=uid, outcome='success', changes=copy.deepcopy(body)))
                    elif method == 'PUT':
                        for key in ('name', 'display_name', 'title', 'timezone'): row[key] = body[key]
                        row['settings']['profile_notifications'] = copy.deepcopy(body['notifications'])
                        state['audit'].append(dict(action='v3.profile.updated', actor=uid, target=uid, outcome='success',
                            changes=dict(fields=['name', 'display_name', 'title', 'timezone', 'notifications'], notification_events=1)))
                    status, value = 200, me(user) if endpoint == '/oncall' else profile_view(user)
            self.assertEqual(status, kwargs.get('want', 200)); self.assertEqual(code, kwargs.get('code'))
            if kwargs.get('validate'): kwargs['validate'](value)
            return value, {'cache-control': 'private, no-store'}
        with (patch.object(self.chain, 'account_state', side_effect=lambda: copy.deepcopy(state)),
              patch.object(self.chain, 'call_account', side_effect=call), patch.object(profile, 'emit') as emit):
            self.chain.accounts()
        self.assertEqual([item.args[0] for item in emit.call_args_list], list(profile.REQUIRED_CASES[1:]))
        self.assertEqual(len(state['audit']), 2)
        self.assertTrue(emit.call_args.args[2]['matched'])

    def test_mock_shutdown_failure_still_performs_owned_database_cleanup(self):
        self.chain.mock = Mock(); self.chain.mock.close.side_effect = RuntimeError('offline close failure')
        with patch.object(readonly.Chain, 'cleanup') as cleanup, self.assertRaises(RuntimeError): self.chain.cleanup()
        cleanup.assert_called_once()

    def test_cli_without_live_flag_never_attests_or_starts_resources(self):
        with (patch.object(sys, 'argv', ['profile']), patch('sys.stderr', new=io.StringIO()),
              patch.object(readonly, 'doctor') as doctor, self.assertRaises(SystemExit)): profile.main()
        doctor.assert_not_called()


if __name__ == '__main__': unittest.main()
