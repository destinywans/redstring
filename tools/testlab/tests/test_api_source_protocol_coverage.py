"""No sockets, Docker, API subprocesses, certificates or external calls in these tests."""
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
import api_source_protocol_coverage as protocol


def apply(revision=1, op=None):
    return dict(instance_id='testlab.local', environment_id='test', expected_revision=revision,
        operation_id=op or 'a' * 32, controls=dict(scim=False, mfa_policy='none', mfa_methods=[],
            approval_passkey=False, ip_allow='', new_device_notice=True))


class AuthorityTests(unittest.TestCase):
    def setUp(self):
        self.authority = protocol.AuthorityProtocol('offline-secret')
        self.auth = 'Bearer offline-secret'

    def dispatch(self, path='/authority/snapshot', body=None):
        return self.authority.dispatch(path, self.auth, body or dict(instance_id='testlab.local', environment_id='test'))

    def test_bearer_failures_do_not_read_or_write_mock(self):
        for token in ('', None, 'Bearer foreign'):
            self.assertEqual(self.authority.dispatch('/authority/apply', token, apply())[0], 401)
        self.assertEqual(self.authority.calls, [])
        self.assertEqual(self.authority.revision, 1)

    def test_wrong_origin_path_scope_and_unknown_fields_fail_closed(self):
        for path in ('/apply', '/authority/apply?override=true', '//authority/snapshot', 'https://evil/authority/snapshot'):
            self.assertEqual(self.dispatch(path, apply())[0], 404)
        for body in (dict(instance_id='other', environment_id='test'), dict(instance_id='testlab.local', environment_id='staging'),
                     dict(instance_id='testlab.local', environment_id='test', extra=True)):
            self.assertEqual(self.dispatch(body=body)[0], 422)
        self.assertEqual(self.authority.revision, 1)

    def test_snapshot_and_apply_have_exact_fresh_scope_and_readback(self):
        status, value, lose = self.dispatch()
        self.assertEqual((status, lose), (200, False))
        self.assertEqual(value['view'], protocol.initial_view())
        self.assertEqual(set(value), {'instance_id', 'environment_id', 'revision', 'complete', 'enforced', 'observed_at', 'valid_until', 'view'})
        request = apply()
        status, receipt, lose = self.dispatch('/authority/apply', request)
        self.assertEqual((status, lose), (200, False))
        self.assertEqual(receipt['controls_sha256'], protocol.controls_digest(request['controls']))
        self.assertEqual(receipt['revision'], 2)
        self.assertTrue(self.dispatch()[1]['view']['session']['new_device_notice'])
        receipt['revision'] = 999
        self.assertEqual(self.dispatch('/authority/apply', request)[1]['revision'], 2)

    def test_idempotent_operation_does_not_increment_revision_twice(self):
        request = apply()
        first = self.dispatch('/authority/apply', request)
        self.assertEqual(first, self.dispatch('/authority/apply', request))
        self.assertEqual(len(self.authority.applies), 1)
        changed = copy.deepcopy(request); changed['controls']['scim'] = True
        self.assertEqual(self.dispatch('/authority/apply', changed)[0], 409)
        self.assertEqual(self.authority.revision, 2)

    def test_stale_invalid_and_noncanonical_apply_never_mutate(self):
        bodies = [apply(2), apply(True), apply(op='foreign'), dict(apply(), extra=True), dict(apply(), controls={})]
        for body in bodies:
            self.assertIn(self.dispatch('/authority/apply', body)[0], (409, 422))
        self.assertEqual(self.authority.revision, 1)
        self.assertEqual(self.authority.applies, [])

    def test_429_is_single_request_without_a_state_mutation(self):
        self.authority.set_mode('snapshot_429')
        self.assertEqual(self.dispatch(), (429, {}, False))
        self.assertEqual(len(self.authority.calls), 1)
        self.assertEqual(self.authority.revision, 1)
        self.authority.set_mode('normal')
        self.assertEqual(self.dispatch()[0], 200)

    def test_response_loss_happens_after_committing_synthetic_authority(self):
        self.authority.set_mode('apply_response_loss')
        self.assertEqual(self.dispatch('/authority/apply', apply())[2], True)
        self.assertEqual(self.authority.revision, 2)
        self.assertEqual(len(self.authority.applies), 1)
        self.assertEqual(self.dispatch('/authority/apply', apply())[2], False)
        self.assertEqual(len(self.authority.applies), 1)

    def test_unknown_fault_mode_is_rejected(self):
        with self.assertRaises(readonly.Blocked): self.authority.set_mode('random_retry')

    def test_controls_digest_is_exact_go_declaration_order_and_order_independent_input(self):
        controls = apply()['controls']
        exact = b'{"scim":false,"mfa_policy":"none","mfa_methods":[],"approval_passkey":false,"ip_allow":"","new_device_notice":true}'
        self.assertEqual(protocol.controls_digest(dict(reversed(list(controls.items())))), hashlib.sha256(exact).hexdigest())


class ChainProtectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.contract = readonly.Contract(readonly.ROOT)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.data = dict(owner=readonly.OWNER, binary='unused', binary_sha256='unused', source_tree_sha='unused')
        with patch.object(readonly, 'Contract', return_value=self.contract):
            self.chain = protocol.Chain(self.root, self.data, str(uuid.uuid4()))
        self.chain.cookies['admin'] = 'aiops_session=synthetic-no-live-session'

    def test_uses_shared_ownership_lifecycle_and_private_nonce(self):
        self.assertIs(protocol.Chain.provision, readonly.Chain.provision)
        self.assertIs(protocol.Chain.start, readonly.Chain.start)
        self.assertIs(protocol.Chain.pg, readonly.Chain.pg)
        self.assertIs(protocol.Chain.seed, readonly.Chain.seed)
        self.assertEqual(self.chain.state['ports'], list(readonly.PORTS))
        self.assertEqual(self.chain.state['mock_port'], 18614)
        self.assertEqual(self.chain.state['real_security_enforcement_verified'], False)
        self.assertNotIn(self.chain.authority.token, (self.chain.work / 'state.json').read_text())
        with patch.object(readonly, 'Contract', return_value=self.contract), self.assertRaises(readonly.Blocked):
            protocol.Chain(self.root, dict(self.data, owner='foreign'), str(uuid.uuid4()))

    def prepare_files(self):
        for name in ('authority-cert.pem', 'authority-key.pem', 'authority-token'):
            path = self.chain.work / name
            readonly.private_file(path, 'offline ' + name)
            self.chain.protocol_files[name] = hashlib.sha256(path.read_bytes()).hexdigest()

    def test_startup_hook_requires_prepared_private_frozen_files_only_three_variables(self):
        with patch.object(readonly.Chain, 'frozen'), self.assertRaises(readonly.Blocked): self.chain.startup_environment()
        self.prepare_files()
        with patch.object(readonly.Chain, 'frozen'):
            value = self.chain.startup_environment()
        self.assertEqual(set(value), {'AIOPS_V3_SECURITY_AUTHORITY_URL', 'AIOPS_V3_SECURITY_AUTHORITY_TOKEN_FILE', 'SSL_CERT_FILE'})
        self.assertEqual(value['AIOPS_V3_SECURITY_AUTHORITY_URL'], 'https://localhost:18614/authority')
        self.assertEqual(value['SSL_CERT_FILE'], str(self.chain.work / 'authority-cert.pem'))

    def test_freeze_rejects_changed_world_readable_and_symlinked_secrets(self):
        self.prepare_files(); path = self.chain.work / 'authority-token'
        with patch.object(readonly.Chain, 'frozen'):
            path.chmod(0o644)
            with self.assertRaises(readonly.Blocked): self.chain.frozen()
            path.chmod(0o600); path.write_text('changed')
            with self.assertRaises(readonly.Blocked): self.chain.frozen()
            path.unlink(); path.symlink_to(self.chain.work / 'authority-cert.pem')
            with self.assertRaises(readonly.Blocked): self.chain.frozen()

    def test_evidence_normal_requires_real_formal_success_and_payload_assertion(self):
        with patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport', return_value=(200, json.dumps(protocol.initial_view()).encode(), {'ETag': 'synthetic'})), patch.object(protocol, 'emit') as emit:
            value, headers = self.chain.call_security('admin', 'GET', index=1,
                validate=lambda v: readonly.check(v == protocol.initial_view(), 'DTO mismatch'))
        row = emit.call_args.kwargs['interface']
        self.assertTrue(row['registration_evidence'])
        self.assertEqual(row['path'], '/api/v1/security')
        self.assertEqual(row['upstream_mode'], 'protocol_mock')
        self.assertEqual(row['evidence_mode'], 'protocol_mock')
        self.assertIn('not enforced security', row['proof_scope'])
        self.assertEqual(headers['etag'], 'synthetic')

    def test_denial_is_passed_scenario_but_never_normal_proof(self):
        body = json.dumps({'error': {'code': 'forbidden'}}).encode()
        with patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport', return_value=(403, body, {})), patch.object(protocol, 'emit') as emit:
            self.chain.call_security('reader', 'GET', want=403, code='forbidden', scenario='authorization')
        self.assertFalse(emit.call_args.kwargs['interface']['registration_evidence'])

    def test_top_level_protocol_outputs_do_not_claim_real_upstream(self):
        with patch('sys.stdout', new=io.StringIO()) as output:
            protocol.emit('protocol.offline', 'passed', {'upstream_mode': 'protocol_mock'})
        self.assertEqual(json.loads(output.getvalue())['evidence_mode'], 'protocol_mock')

    def test_wrong_status_and_transport_failure_save_first_red_without_fabricated_http(self):
        for behavior in ('status', 'transport'):
            side_effect = OSError('offline dropped connection') if behavior == 'transport' else None
            with patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport', side_effect=side_effect,
                return_value=(503, b'{"error":{"code":"security_authority_unavailable"}}', {})), patch.object(protocol, 'emit') as emit:
                with self.assertRaises((AssertionError, OSError)):
                    self.chain.call_security('admin', 'GET')
            if behavior == 'status': self.assertFalse(emit.call_args.kwargs['interface']['registration_evidence'])
            else: self.assertNotIn('interface', emit.call_args.kwargs)
        self.assertTrue((self.chain.work / 'first-failure.json').is_file())
        self.assertEqual(json.loads((self.chain.work / 'first-failure.json').read_text())['actual']['status'], 503)

    def test_account_verb_origin_and_header_boundaries_reject_without_transport(self):
        for args, fields in [(('peer', 'GET'), {}), (('admin', 'POST'), {}), (('admin', 'GET'), {'index': 2}),
                             (('admin', 'PUT'), {'etag': 'tag\r\nAuthorization: foreign'})]:
            with patch.object(self.chain, 'frozen'), patch.object(readonly, 'transport') as transport, self.assertRaises(readonly.Blocked):
                self.chain.call_security(*args, **fields)
            transport.assert_not_called()

    def test_ledger_is_readonly_no_pending_reconciliation_sql_mutations(self):
        with patch.object(self.chain, 'sql', return_value='{}') as sql: self.chain.ledger()
        self.assertEqual(sql.call_args.kwargs, {'read': True})
        statement = sql.call_args.args[0].upper()
        for forbidden in ('INSERT', 'UPDATE', 'DELETE', 'TRUNCATE'): self.assertNotIn(forbidden, statement)

    def test_mock_close_failure_does_not_bypass_exact_database_cleanup(self):
        self.chain.mock = Mock()
        self.chain.mock.close.side_effect = RuntimeError('mock close failed')
        with patch.object(readonly.Chain, 'cleanup') as cleanup, self.assertRaisesRegex(RuntimeError, 'mock close failed'):
            self.chain.cleanup()
        cleanup.assert_called_once()

    def run_offline_security(self, corrupt=None):
        operations, audits, calls = [], [], []
        local_revision = []
        authority = self.chain.authority
        actor = 'offline-admin'
        def ledger():
            value = copy.deepcopy(dict(operations=operations, applied=local_revision, audit=audits))
            if corrupt: corrupt(value)
            return value
        def call(user, method, body=None, **kwargs):
            calls.append((user, method, kwargs))
            if user is None: status, code, value = 401, 'unauthorized', {}
            elif user == 'reader': status, code, value = 403, 'forbidden', {}
            elif operations and operations[-1]['status'] == 'unknown':
                status, code, value = (503 if method == 'GET' else 409), 'security_reconciliation_required', {}
            elif method == 'PUT' and kwargs.get('etag') is None:
                status, code, value = 428, 'security_precondition_required', {}
            else:
                scope = dict(instance_id='testlab.local', environment_id='test')
                upstream, snapshot, _ = authority.dispatch('/authority/snapshot', 'Bearer ' + authority.token, scope)
                if upstream == 429: status, code, value = 503, 'security_authority_unavailable', {}
                elif method == 'GET': status, code, value = 200, None, snapshot['view']
                elif kwargs['etag'] != 'tag-' + str(authority.revision): status, code, value = 412, 'security_changed', {}
                else:
                    request = apply(authority.revision, str(len(operations) + 1) * 32)
                    request['controls']['new_device_notice'] = body['session']['new_device_notice']
                    _, receipt, lose = authority.dispatch('/authority/apply', 'Bearer ' + authority.token, request)
                    operations.append(dict(id=request['operation_id'], status='unknown' if lose else 'applied',
                        instance_id='testlab.local', environment_id='test', actor_id=actor,
                        requested_controls=copy.deepcopy(request['controls']),
                        expected_revision=request['expected_revision'], applied_revision=None if lose else receipt['revision'],
                        error_code='apply_outcome_unknown' if lose else ''))
                    audits.extend([dict(action='v3.security.change_requested', outcome='pending', actor=actor,
                        target='testlab.local/test', changes=dict(operation_id=request['operation_id'],
                        expected_revision=request['expected_revision'], controls=copy.deepcopy(request['controls']))),
                        dict(action='v3.security.change_finished', outcome='unknown' if lose else 'success', actor=actor,
                        target='testlab.local/test', changes=dict(operation_id=request['operation_id'],
                        **({'error_code': 'apply_outcome_unknown'} if lose else dict(revision_before=request['expected_revision'],
                            revision_after=receipt['revision'], controls=copy.deepcopy(request['controls'])))))])
                    if not lose: local_revision[:] = [dict(instance_id='testlab.local', environment_id='test',
                        runtime_revision=receipt['revision'], operation_id=request['operation_id'],
                        controls=copy.deepcopy(request['controls']))]
                    status, code, value = (503, 'security_authority_unavailable', {}) if lose else (200, None, copy.deepcopy(authority.view))
            self.assertEqual(status, kwargs.get('want', 200))
            self.assertEqual(code, kwargs.get('code'))
            if kwargs.get('validate'): kwargs['validate'](value)
            return value, {'etag': 'tag-' + str(authority.revision), 'cache-control': 'private, no-store'}
        with (patch.object(self.chain, 'call_security', side_effect=call), patch.object(self.chain, 'ledger', side_effect=ledger),
              patch.object(self.chain, 'admin_id', return_value=actor), patch.object(protocol, 'emit') as emit):
            self.chain.security()
        names = [x.args[0] for x in emit.call_args_list]
        self.assertEqual(names, list(protocol.REQUIRED_CASES[1:]))
        linkage = [x for x in emit.call_args_list if x.args[0] == 'protocol.security.operation_linkage']
        self.assertEqual(len(linkage), 1)
        self.assertTrue(linkage[0].args[2]['matched'])
        self.assertEqual(linkage[0].args[2]['external_operation_ids'], linkage[0].args[2]['local_operation_ids'])
        self.assertEqual(len(authority.applies), 2)
        self.assertEqual([x['runtime_revision'] for x in local_revision], [2])
        self.assertEqual(operations[-1]['status'], 'unknown')
        self.assertEqual(calls[-1][2]['index'], 0)
        self.assertEqual(calls[1][2]['index'], 1)

    def test_full_security_sequence_keeps_unknown_ledger_and_requires_cross_instance_no_duplicate(self):
        self.run_offline_security()

    def test_swapped_operation_identity_is_rejected_even_when_counts_and_statuses_match(self):
        def corrupt(value): value['operations'][0]['id'] = 'f' * 32
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_wrong_operation_actor_is_rejected_even_when_audit_actor_matches(self):
        def corrupt(value): value['operations'][0]['actor_id'] = 'another-user'
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_wrong_audit_target_is_rejected_even_when_action_outcome_and_counts_match(self):
        def corrupt(value): value['audit'][0]['target'] = 'other-instance/test'
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_requested_controls_pollution_is_rejected(self):
        def corrupt(value): value['operations'][0]['requested_controls']['scim'] = True
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_wrong_audit_operation_id_is_rejected(self):
        def corrupt(value): value['audit'][1]['changes']['operation_id'] = 'f' * 32
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_wrong_applied_operation_id_is_rejected(self):
        def corrupt(value): value['applied'][0]['operation_id'] = 'f' * 32
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_applied_controls_pollution_is_rejected(self):
        def corrupt(value): value['applied'][0]['controls']['scim'] = True
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_wrong_operation_scope_is_rejected(self):
        def corrupt(value): value['operations'][0]['environment_id'] = 'staging'
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_unknown_operation_cannot_claim_applied_revision(self):
        def corrupt(value):
            if len(value['operations']) == 2: value['operations'][1]['applied_revision'] = 3
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_audit_operation_ids_cannot_be_swapped_across_success_and_unknown(self):
        def corrupt(value):
            if len(value['operations']) == 2:
                value['audit'][0]['changes']['operation_id'], value['audit'][2]['changes']['operation_id'] = (
                    value['audit'][2]['changes']['operation_id'], value['audit'][0]['changes']['operation_id'])
        with self.assertRaises(AssertionError): self.run_offline_security(corrupt)

    def test_run_calls_only_two_formal_targets_and_no_live_action_without_explicit_flag(self):
        self.assertEqual(protocol.TARGETS, ('GET /api/v1/security', 'PUT /api/v1/security'))
        with (patch.object(sys, 'argv', ['api_source_protocol_coverage.py']), patch.object(readonly, 'doctor') as doctor,
              patch('sys.stderr', new=io.StringIO()), self.assertRaises(SystemExit) as error):
            protocol.main()
        self.assertEqual(error.exception.code, 2); doctor.assert_not_called()


if __name__ == '__main__': unittest.main()
