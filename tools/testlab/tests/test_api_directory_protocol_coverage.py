"""Offline guards; no listeners, databases, Docker, or outgoing requests."""
import copy
import contextlib
import io
import importlib
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
directory = importlib.import_module('api_directory_protocol_coverage')


class ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.p = directory.DirectoryProtocol('a' * 32)
        self.uid = 'b' * 32
        self.p.population = {self.uid}
        self.p.target = self.uid

    def token(self):
        status, value, lose = self.p.dispatch('open.feishu.cn', 'POST', directory.AUTH, {},
            {'app_id': self.p.directory_app, 'app_secret': self.p.directory_secret})
        self.assertEqual((status, lose), (200, False))
        return value['tenant_access_token']

    def page(self, token, second=False):
        path = directory.USERS + '?department_id=0&department_id_type=open_department_id&user_id_type=open_id&page_size=50'
        if second: path += '&page_token=page2'
        return self.p.dispatch('open.feishu.cn', 'GET', path, {'Authorization': 'Bearer ' + token}, None)

    def authority(self):
        return self.p.dispatch('mfa.testlab.invalid', 'POST', '/snapshot',
            {'Authorization': 'Bearer ' + self.p.mfa_token},
            {'instance_id': 'testlab.local', 'tenant_binding_id': self.p.binding,
             'environment_id': 'test', 'user_ids': [self.uid]})

    def test_connect_exact_allowlist(self):
        for name in ('open.feishu.cn:443', 'mfa.testlab.invalid:443'):
            self.assertEqual(directory.connect_host(name), name[:-4])
        for name in ('open.feishu.cn:444', '127.0.0.1:443', 'open.feishu.cn.evil:443',
                     'OPEN.FEISHU.CN:443', 'user@open.feishu.cn:443', 'open.feishu.cn:443/path'):
            with self.assertRaises(directory.Blocked): directory.connect_host(name)

    def test_unknown_host_never_dispatches(self):
        self.assertEqual(self.p.dispatch('evil.invalid', 'GET', '/', {}, None), (403, {}, False))
        self.assertEqual(self.p.events, [])

    def test_token_credentials_and_query_are_exact(self):
        self.assertEqual(self.p.dispatch('open.feishu.cn', 'POST', directory.AUTH, {}, {})[0], 401)
        token = self.token()
        self.assertEqual(self.p.dispatch('open.feishu.cn', 'GET', directory.USERS + '?page_size=500',
            {'Authorization': 'Bearer ' + token}, None)[0], 422)

    def test_two_pages_and_frozen_revision(self):
        token = self.token(); self.p.revision = 2
        first = self.page(token)[1]['data']; second = self.page(token, True)[1]['data']
        self.assertTrue(first['has_more']); self.assertEqual(first['page_token'], 'page2')
        self.assertFalse(second['has_more']); self.assertIn('r1', first['items'][0]['name'])
        self.assertNotEqual(first['items'][0]['open_id'], second['items'][0]['open_id'])
        self.assertEqual(len(first['items'][0]['status']), 5)

    def test_fault_keeps_first_page_valid(self):
        for mode in ('page_429', 'response_loss', 'missing_status', 'repeat_cursor'):
            with self.subTest(mode=mode):
                self.p.directory_mode = mode; token = self.token()
                self.assertEqual(self.page(token)[0], 200)
                status, value, lose = self.page(token, True)
                if mode == 'page_429': self.assertEqual(status, 429)
                elif mode == 'response_loss': self.assertTrue(lose)
                elif mode == 'missing_status': self.assertNotIn('is_exited', value['data']['items'][0]['status'])
                else: self.assertEqual(value['data']['page_token'], 'page2'); self.assertTrue(value['data']['has_more'])

    def test_authority_exact_population_scope_and_time(self):
        status, value, lose = self.authority()
        self.assertEqual((status, lose), (200, False)); self.assertEqual(set(value['snapshot']['members']), {self.uid})
        self.assertEqual(value['snapshot']['members'][self.uid]['feishu_open_id'], 'ou_' + self.uid)
        self.assertTrue(value['snapshot']['complete']); self.assertEqual(value['snapshot']['grace_days'], 7)
        self.assertEqual(self.p.dispatch('mfa.testlab.invalid', 'POST', '/snapshot',
            {'Authorization': 'Bearer ' + self.p.mfa_token}, {'user_ids': ['foreign']})[0], 422)

    def test_authority_faults_are_explicit(self):
        for mode in ('429', 'response_loss', 'wrong_scope', 'missing_member', 'stale'):
            with self.subTest(mode=mode):
                self.p.authority_mode = mode; status, value, lose = self.authority()
                if mode == '429': self.assertEqual(status, 429)
                elif mode == 'response_loss': self.assertTrue(lose)
                elif mode == 'wrong_scope': self.assertEqual(value['environment_id'], 'staging')
                elif mode == 'missing_member': self.assertEqual(value['snapshot']['members'], {})
                else: self.assertLess(directory.instant(value['snapshot']['observed_at']), directory.instant(directory.stamp(-300)))
        self.assertEqual(self.p.messages, [])

    def test_saved_authority_reply_is_actual_fault_payload(self):
        for mode in ('429', 'wrong_scope', 'missing_member', 'response_loss'):
            self.p.authority_mode = mode
            _, payload, _ = self.authority()
            self.assertEqual(self.p.authority_answers[-1], payload)

    def test_authority_rejects_duplicate_extra_nonstring_ids(self):
        for ids in ([self.uid, self.uid], [self.uid, 'foreign'], [[self.uid]]):
            with self.subTest(ids=ids):
                self.assertEqual(self.p.dispatch('mfa.testlab.invalid', 'POST', '/snapshot',
                    {'Authorization': 'Bearer ' + self.p.mfa_token}, {'instance_id': 'testlab.local',
                    'tenant_binding_id': self.p.binding, 'environment_id': 'test', 'user_ids': ids})[0], 422)
        self.assertEqual(self.p.authority_answers, [])

    def test_gate_preserves_old_token_does_not_lock_new_read(self):
        entered, release = self.p.gate_next(); old_token = self.token()
        with directory.ThreadPoolExecutor(max_workers=1) as pool:
            pending = pool.submit(self.page, old_token)
            try:
                self.assertTrue(entered.wait(.3)); self.p.revision = 4
                self.assertIn('r4', self.page(self.token())[1]['data']['items'][0]['name'])
            finally: release.set()
            self.assertIn('r1', pending.result(1)[1]['data']['items'][0]['name'])

    def test_constructor_never_acquires_socket(self):
        with patch.object(directory.socket, 'socket', side_effect=AssertionError('socket acquired')):
            directory.DirectoryProtocol('f' * 32)
            directory.ConnectMock(self.p, 'private-cert', 'private-key')

    def test_unknown_path_absolute_url_and_redirect_are_not_forwarded(self):
        for path in ('https://evil.invalid/secret', '//evil.invalid/secret', '/unknown', '/snapshot#fragment'):
            with self.subTest(path=path):
                self.assertNotEqual(self.p.dispatch('open.feishu.cn', 'GET', path, {}, None)[0], 200)
        self.assertEqual(self.p.messages, [])

    def test_bot_acceptance_precedes_loss_and_is_not_read_receipt(self):
        status, auth, _ = self.p.dispatch('open.feishu.cn', 'POST', directory.AUTH, {},
            {'app_id': self.p.bot_app, 'app_secret': self.p.bot_secret})
        self.assertEqual(status, 200); self.p.bot_mode = 'response_loss'
        delivery = directory.delivery_uuid('c' * 32)
        status, answer, lose = self.p.dispatch('open.feishu.cn', 'POST', directory.MESSAGES + '?receive_id_type=open_id',
            {'Authorization': 'Bearer ' + auth['tenant_access_token']},
            {'receive_id': 'ou_' + self.uid, 'msg_type': 'text', 'content': json.dumps({'text': directory.REMINDER_TEXT}), 'uuid': delivery})
        self.assertTrue(lose); self.assertEqual(len(self.p.messages), 1)
        self.assertEqual(self.p.accepted[delivery]['message_id'], answer['data']['message_id'])
        self.assertNotIn('read', self.p.accepted[delivery])

    def test_bot_rejects_non_owned_recipient_or_text(self):
        _, token, _ = self.p.dispatch('open.feishu.cn', 'POST', directory.AUTH, {},
            {'app_id': self.p.bot_app, 'app_secret': self.p.bot_secret})
        self.assertEqual(self.p.dispatch('open.feishu.cn', 'POST', directory.MESSAGES + '?receive_id_type=open_id',
            {'Authorization': 'Bearer ' + token['tenant_access_token']},
            {'receive_id': 'ou_foreign', 'msg_type': 'text', 'content': '{}', 'uuid': directory.delivery_uuid('c' * 32)})[0], 422)
        self.assertEqual(self.p.messages, [])

    def test_delivery_uuid_uses_native_bit_rules(self):
        self.assertEqual(directory.delivery_uuid('0' * 32), '00000000-0000-4000-8000-000000000000')
        for value in ('foreign', '0' * 31, 'A' * 32):
            with self.assertRaises(directory.Blocked): directory.delivery_uuid(value)


class LedgerTests(unittest.TestCase):
    def test_concurrent_pending_is_honest_uncertain_not_second_send(self):
        self.assertEqual(directory.concurrent_result({'sent': 1}), 1)
        self.assertEqual(directory.concurrent_result({'sent': 0}), 0)
        self.assertIsNone(directory.concurrent_result({'error': {'code': 'mfa_reminder_uncertain', 'message': 'Delivery outcome requires reconciliation'}}))
        for value in ({'sent': 2}, {'sent': True}, {'sent': 1, 'other': 0},
                      {'error': {'code': 'mfa_reminder_unavailable'}}, {'error': {'code': 'forbidden'}}):
            with self.subTest(value=value):
                with self.assertRaises(AssertionError): directory.concurrent_result(value)

    def fixture(self, status='accepted'):
        attempt = dict(id='c' * 32, environment_id='test', target_id='b' * 32, actor_id='a' * 32,
            status=status, mfa_observed_at=directory.stamp(), feishu_message_id=None, accepted_at=None)
        receipt = dict(uuid=directory.delivery_uuid(attempt['id']), receive_id='ou_' + attempt['target_id'], message_id='mock-message-' + directory.delivery_uuid(attempt['id']))
        audit = []
        if status == 'accepted':
            attempt.update(feishu_message_id=receipt['message_id'], accepted_at=directory.stamp())
            audit = [dict(actor='a' * 32, target='b' * 32, action=directory.SENT_ACTION, outcome='success',
                changes=dict(environment='test', attempt_id=attempt['id'], feishu_message_id=receipt['message_id'], mfa_observed_at=attempt['mfa_observed_at']))]
        return attempt, receipt, audit

    def test_accepted_exact_linkage(self):
        row, receipt, audit = self.fixture()
        directory.validate_delivery(row, receipt, audit, 'a' * 32, 'b' * 32, False)
        for key in ('actor_id', 'target_id', 'environment_id', 'id', 'feishu_message_id'):
            with self.subTest(key=key):
                bad = dict(row); bad[key] = 'foreign'
                with self.assertRaises(AssertionError): directory.validate_delivery(bad, receipt, audit, 'a' * 32, 'b' * 32, False)
        for field in ('actor', 'target', 'action', 'outcome'):
            with self.subTest(field=field):
                bad = copy.deepcopy(audit); bad[0][field] = 'foreign'
                with self.assertRaises(AssertionError): directory.validate_delivery(row, receipt, bad, 'a' * 32, 'b' * 32, False)

    def test_unknown_forbids_success_fields_and_audit(self):
        for status in ('pending', 'uncertain'):
            row, receipt, audit = self.fixture(status)
            directory.validate_delivery(row, receipt, audit, 'a' * 32, 'b' * 32, True)
            row['feishu_message_id'] = receipt['message_id']
            with self.assertRaises(AssertionError): directory.validate_delivery(row, receipt, audit, 'a' * 32, 'b' * 32, True)

    def test_accepted_audit_changes_swap_is_rejected(self):
        row, receipt, audit = self.fixture()
        for field in ('attempt_id', 'environment', 'feishu_message_id', 'mfa_observed_at'):
            with self.subTest(field=field):
                bad = copy.deepcopy(audit); bad[0]['changes'][field] = 'foreign'
                with self.assertRaises(AssertionError): directory.validate_delivery(row, receipt, bad, 'a' * 32, 'b' * 32, False)

    def test_unknown_audit_pollution_is_rejected(self):
        row, receipt, _ = self.fixture('uncertain'); _, _, audit = self.fixture()
        with self.assertRaises(AssertionError): directory.validate_delivery(row, receipt, audit, 'a' * 32, 'b' * 32, True)

    def test_fault_ddl_is_conditional_and_private(self):
        ddl = directory.fault_ddl('a' * 32, 'b' * 32, 'c' * 32)
        self.assertIn("NEW.actor_id='" + 'b' * 32, ddl)
        self.assertIn("NEW.target='" + 'c' * 32, ddl)
        self.assertIn(directory.SENT_ACTION, ddl); self.assertIn('BEFORE INSERT ON public.management_audit', ddl)
        self.assertNotIn('UPDATE ', ddl); self.assertNotIn('INSERT INTO ', ddl)
        with self.assertRaises(directory.Blocked): directory.fault_ddl('other-db', 'b' * 32, 'c' * 32)

    def test_scope_guard_rejects_wrong_cid_owner_uuid(self):
        scope = dict(run_id='11111111-1111-4111-8111-111111111111', nonce='11111111111141118111111111111111',
            database='aiops_testlab_readonly_11111111111141118111111111111111', db_user='readonly_11111111111141118111111111111111',
            owner_tag=directory.readonly.OWNER + ':11111111-1111-4111-8111-111111111111:api-readonly', container_id='a' * 64)
        catalog = dict(database_owner=scope['db_user'], database_tag=scope['owner_tag'], role_tag=scope['owner_tag'])
        directory.validate_fault_scope(scope, catalog, 'a' * 64)
        for key in ('database', 'db_user', 'owner_tag', 'nonce', 'container_id'):
            with self.subTest(key=key):
                bad = dict(scope); bad[key] = 'foreign'
                with self.assertRaises(directory.Blocked): directory.validate_fault_scope(bad, catalog, 'a' * 64)
        with self.assertRaises(directory.Blocked): directory.validate_fault_scope(scope, catalog, 'b' * 64)
        for key in catalog:
            bad = dict(catalog); bad[key] = 'foreign'
            with self.assertRaises(directory.Blocked): directory.validate_fault_scope(scope, bad, 'a' * 64)

    def test_no_shared_or_product_module_edits_required(self):
        self.assertEqual(len(directory.TARGETS), 2)
        self.assertEqual(len(directory.REQUIRED_CASES), len(set(directory.REQUIRED_CASES)))
        self.assertIn('directory.reminders.audit_commit_failure', directory.REQUIRED_CASES)


class ChainGuards(unittest.TestCase):
    def make(self, work):
        chain = directory.Chain.__new__(directory.Chain)
        chain.work, chain.run_id, chain.calls = Path(work), '11111111-1111-4111-8111-111111111111', 0
        chain.database = 'aiops_testlab_readonly_' + directory.uuid.UUID(chain.run_id).hex
        chain.users = {'reader': 'a' * 32}; chain.cookies = {'reader': 'aiops_session=private-secret'}
        chain.protocol = directory.DirectoryProtocol('1' * 32); chain.protocol_files = {}
        chain.call_lock = threading.Lock(); chain.first_red = lambda row: None
        chain.frozen = lambda: None
        chain.contract = unittest.mock.Mock()
        chain.contract.canonical.return_value = ('/api/v1/members/sync', 'main')
        return chain

    def test_every_http_cell_is_protocol_mock_and_errors_validate(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); checked = []
            with patch.object(directory.readonly, 'transport', return_value=(503, b'{"error":{"code":"member_sync_unavailable"}}', {})), contextlib.redirect_stdout(io.StringIO()):
                chain.call('reader', 'POST', '/api/v1/members/sync', want=503, code='member_sync_unavailable',
                    scenario='failure', validate=lambda value: checked.append(value))
            row = json.loads((chain.work / 'interfaces.jsonl').read_text())
            self.assertEqual(row['evidence_mode'], 'protocol_mock'); self.assertFalse(row['registration_evidence'])
            self.assertEqual(len(checked), 1); self.assertNotIn('private-secret', (chain.work / 'interfaces.jsonl').read_text())

    def test_success_normal_requires_semantic_validation(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work)
            with patch.object(directory.readonly, 'transport', return_value=(200, b'{"added":999,"updated":0}', {})), contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaises(AssertionError): chain.call('reader', 'POST', '/api/v1/members/sync',
                    validate=lambda value: directory.check(value['added'] == 2, 'synthetic wrong added count'))
            row = json.loads((chain.work / 'interfaces.jsonl').read_text())
            self.assertFalse(row['registration_evidence']); self.assertEqual(row['status'], 'failed')

    def test_concurrent_503_requires_immediate_native_unknown_and_no_duplicates(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); chain.users['peer'] = 'b' * 32
            row = dict(id='c' * 32, target_id='b' * 32, actor_id='a' * 32, environment_id='test',
                status='pending', accepted_at=None, feishu_message_id=None)
            chain.ledger = lambda: {'attempts': [copy.deepcopy(row)]}
            chain.concurrent_validation({'error': {'code': 'mfa_reminder_uncertain'}})
            proof = json.loads((chain.work / 'concurrency-pending-proof.json').read_text())
            self.assertEqual(proof['native_attempt']['status'], 'pending'); self.assertTrue(proof['no_retry'])
            row['status'] = 'accepted'
            with self.assertRaises(AssertionError): chain.concurrent_validation({'error': {'code': 'mfa_reminder_uncertain'}})
            row['status'] = 'pending'; chain.protocol.messages = [dict(receive_id='ou_' + 'b' * 32, uuid=directory.delivery_uuid('c' * 32))] * 2
            with self.assertRaises(AssertionError): chain.concurrent_validation({'error': {'code': 'mfa_reminder_uncertain'}})

    def test_generic_multi_status_does_not_relax_http_contract(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work)
            with patch.object(directory.readonly, 'transport', side_effect=AssertionError('transport called')):
                with self.assertRaises(directory.Blocked): chain.call('reader', 'POST', '/api/v1/members/sync', want=(200, 503))
                with self.assertRaises(directory.Blocked): chain.call('reader', 'POST', '/api/v1/members/mfa-reminders', want=(200, 503), validate=directory.concurrent_result)

    def test_route_and_environment_guards_before_transport(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work)
            with patch.object(directory.readonly, 'transport', side_effect=AssertionError('transport called')):
                for kwargs in ({'path': '/api/v1/other'}, {'index': 2}, {'user': 'foreign'}, {'environment': 'production'}):
                    args = dict(user='reader', method='POST', path='/api/v1/members/sync'); args.update(kwargs)
                    with self.assertRaises(directory.Blocked): chain.call(**args)

    def test_startup_extends_only_three_bounded_variables(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work)
            chain.protocol_files = dict.fromkeys(('directory-ca.pem', 'directory-key.pem', 'directory-secret', 'bot-secret', 'mfa-token', 'member-sources.json'))
            env = chain.startup_environment()
            self.assertEqual(set(env), {'HTTPS_PROXY', 'SSL_CERT_FILE', 'AIOPS_V3_MEMBER_SOURCES_CONFIG'})
            self.assertEqual(env['HTTPS_PROXY'], 'http://127.0.0.1:18622')
            self.assertTrue(all(not x.endswith('.env') for x in env.values()))
            with self.assertRaises(directory.Blocked): chain.apply_startup_environment({'HTTPS_PROXY': 'foreign'})
            chain.protocol_files.pop('bot-secret')
            with self.assertRaises(directory.Blocked): chain.startup_environment()

    def test_frozen_credentials_reject_mode_symlink_and_content(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); del chain.frozen
            file = chain.work / 'directory-ca.pem'; file.write_bytes(b'synthetic'); os.chmod(file, 0o600)
            chain.protocol_files = {file.name: directory.hashlib.sha256(file.read_bytes()).hexdigest()}
            with patch.object(directory.readonly.Chain, 'frozen'):
                chain.frozen(); os.chmod(file, 0o644)
                with self.assertRaises(directory.Blocked): chain.frozen()
                os.chmod(file, 0o600); file.write_bytes(b'changed')
                with self.assertRaises(directory.Blocked): chain.frozen()
                file.unlink(); file.symlink_to(chain.work / 'elsewhere')
                with self.assertRaises(directory.Blocked): chain.frozen()

    def test_fault_guard_precedes_mutation(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); chain.fault_guard = unittest.mock.Mock(side_effect=directory.Blocked('CID mismatch'))
            chain.sql = unittest.mock.Mock()
            with self.assertRaises(directory.Blocked): chain.install_fault()
            chain.sql.assert_not_called()

    def test_removal_is_owned_exact_trigger_and_function(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); chain.nonce = '1' * 32; chain.fault_installed = True
            chain.fault_guard = unittest.mock.Mock(); chain.sql = unittest.mock.Mock()
            chain.remove_fault(); chain.fault_guard.assert_called_once()
            text = chain.sql.call_args.args[0]
            self.assertIn('DROP TRIGGER IF EXISTS "testlab_reject_mfa_' + '1' * 32 + '" ON public.management_audit', text)
            self.assertNotIn('CASCADE', text); self.assertFalse(chain.fault_installed)

    def test_native_unknown_row_and_zero_success_audit_are_private_evidence(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); row, receipt, audit = LedgerTests().fixture('uncertain')
            after = dict(users=[], sync=[], others=[], workspace={}, attempts=[row], audit=audit)
            before = dict(users=[], sync=[], others=[], workspace={}, attempts=[], audit=[])
            chain.ledger = lambda: after
            chain.protocol.accepted = {receipt['uuid']: receipt}; chain.protocol.messages = [receipt]
            chain.protocol.authority_answers = [{'snapshot': {'observed_at': row['mfa_observed_at'],
                'members': {row['target_id']: {'enrollment': 'unenrolled'}}}}]
            chain.delivery_check(before, row['target_id'], True)
            file = chain.work / ('native-delivery-' + row['id'] + '.json')
            proof = json.loads(file.read_text())
            self.assertEqual(proof['native_attempt'], row); self.assertEqual(proof['native_audit'], [])
            self.assertEqual(proof['external_receipt'], receipt); self.assertEqual(file.stat().st_mode & 0o777, 0o600)
            self.assertEqual(proof['run_id'], chain.run_id); self.assertEqual(proof['attempt_id'], row['id'])
            self.assertEqual(proof['native_database'], chain.database); self.assertEqual(proof['target_id'], row['target_id'])

    def test_cleanup_attempts_native_drop_after_fault_cleanup_error(self):
        with tempfile.TemporaryDirectory() as work:
            chain = self.make(work); chain.fault_installed = True; chain.mock = None
            chain.remove_fault = unittest.mock.Mock(side_effect=directory.Blocked('guard rejected'))
            with patch.object(directory.readonly.Chain, 'cleanup') as cleanup:
                with self.assertRaises(directory.Blocked): chain.cleanup()
                cleanup.assert_called_once()
            self.assertEqual(json.loads((chain.work / 'mock-acceptances.json').read_text())['evidence_mode'], 'protocol_mock')

    def test_own_lease_does_not_call_global_readonly_lease(self):
        with tempfile.TemporaryDirectory() as root, patch.object(directory.readonly, 'exclusive', side_effect=AssertionError('global lease used')):
            with directory.exclusive(root):
                with self.assertRaises(directory.Blocked):
                    with directory.exclusive(root): pass
            self.assertEqual((Path(root) / '.local/testlab/api-directory-protocol.lock').stat().st_mode & 0o777, 0o600)

    def test_private_emit_never_claims_real_authority(self):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            directory.emit('directory.mock_case', 'passed', {'real_tenant': False})
        row = json.loads(output.getvalue())
        self.assertEqual(row['evidence_mode'], 'protocol_mock'); self.assertFalse(row['actual']['real_tenant'])


if __name__ == '__main__': unittest.main()
