"""Bounded contract, lifecycle and evidence counterexamples; no live API/PG."""
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_bulk_actions_functional_coverage as bulk


class BulkSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.data = dict(owner=bulk.OWNER, source_tree_sha=bulk.SOURCE_SHA, binary_sha256=bulk.BINARY_SHA,
                         helper_sha256={}, postgres_container='owned')
        with patch.object(bulk, 'Contract'):
            self.chain = bulk.Chain(self.root, self.data, str(uuid.uuid4()))

    def test_contract_only_exact_bulk_and_required_native_producers_no_approval(self):
        contract = bulk.Contract(bulk.ROOT)
        self.assertEqual(len(contract.main), 166)
        self.assertEqual(contract.canonical('POST', bulk.TARGET), (bulk.TARGET, 'main'))
        for verb, path in [('POST', '/api/v1/proposals/owned/approve'), ('DELETE', bulk.TARGET),
                           ('POST', 'https://business.invalid' + bulk.TARGET), ('GET', '/api/v1/credentials'),
                           ('POST', '/api/v1/webhooks/alertmanager/owned/foreign')]:
            with self.subTest(path=path), self.assertRaises((bulk.Blocked, AssertionError)):
                contract.canonical(verb, path)

    def test_private_uuid_namespace_and_secret_modes_do_not_load_saved_credentials(self):
        self.assertEqual(self.chain.work.parent, self.root / '.local/testlab/bulk-actions-parallel')
        self.assertEqual(self.chain.database, 'aiops_testlab_bulk_' + uuid.UUID(self.chain.run_id).hex)
        credentials = self.chain.work / 'credentials.json'
        self.assertEqual(credentials.stat().st_mode & 0o777, 0o600)
        state = (self.chain.work / 'state.json').read_text()
        self.assertNotIn(self.chain.db_password, state)
        self.assertNotIn(self.chain.source_token, state)
        self.assertFalse(json.loads(state)['approval_actions_used'])

    def test_wrong_cid_rejects_sql_before_any_shared_container_command(self):
        with patch.object(bulk.base, 'inspect_pg', return_value={'Id': 'other'}), \
             patch.object(bulk.base.Chain, 'pg') as sql, self.assertRaises(AssertionError):
            self.chain.pg('SELECT 1')
        sql.assert_not_called()

    def test_first_red_is_immutable_inside_exclusive_package(self):
        self.chain.first_red({'source': 'original'})
        self.chain.first_red({'source': 'later'})
        path = self.chain.work / 'first-failure.json'
        self.assertEqual(json.loads(path.read_text()), {'source': 'original'})
        self.assertFalse((self.root / '.local/testlab/first-failures').exists())

    def test_lease_serializes_only_own_package_and_rejects_second_invocation(self):
        with bulk.exclusive(self.root):
            with self.assertRaises(bulk.Blocked), bulk.exclusive(self.root):
                pass
        self.assertFalse((self.root / '.local/testlab/api-readonly.lock').exists())

    def test_connected_environment_enables_normal_worker_with_own_configs_and_origin(self):
        self.chain.connected = True
        original = dict(AIOPS_ADDR='127.0.0.1:18660', AIOPS_PUBLIC_ORIGIN=bulk.BASES[0],
                        AIOPS_SECURE_COOKIES='false', DATABASE_URL='owned', OPENAI_BASE_URL='http://127.0.0.1:9/v1')
        actual = self.chain.apply_startup_environment(original)
        self.assertEqual(actual['AIOPS_V3_SILENCE_EXECUTION_ENABLED'], 'true')
        self.assertEqual(actual['AIOPS_V3_PASSKEY_ENABLED'], 'true')
        self.assertEqual(actual['AIOPS_SECURE_COOKIES'], 'true')
        self.assertEqual(actual['DATABASE_URL'], original['DATABASE_URL'])
        self.assertEqual(actual['OPENAI_BASE_URL'], original['OPENAI_BASE_URL'])
        self.assertTrue(actual['AIOPS_V3_ALERTMANAGER_CONFIG'].startswith(str(self.chain.work)))
        with self.assertRaises(AssertionError):
            self.chain.apply_startup_environment(dict(original, AIOPS_ADDR='127.0.0.1:18561'))

    def test_am_protocol_refuses_writes_redirect_urls_wrong_scope_and_credential(self):
        source = self.chain.local_sources
        auth = 'Bearer ' + self.chain.am_token
        self.assertEqual(source.am_response('GET', '/api/v2/alerts', auth), (200, []))
        for method, path, credential in [('POST', '/api/v2/silences', auth), ('DELETE', '/api/v2/silence/owned', auth),
                                          ('GET', '/api/v2/alerts', 'Bearer wrong'), ('GET', 'https://business.invalid/api/v2/alerts', auth),
                                          ('GET', '/api/v2/alerts?url=https://business.invalid', auth)]:
            self.assertEqual(source.am_response(method, path, credential)[0], 405)

    def test_tls_response_headers_preserve_base_login_mapping_contract(self):
        conn, response = Mock(), Mock(status=200)
        response.read.return_value = b'{}'
        response.getheaders.return_value = [('Set-Cookie', 'owned-cookie')]
        conn.getresponse.return_value = response
        self.chain.tls_context = Mock()
        with patch.object(bulk, 'ACTIVE', self.chain), patch.object(bulk.http.client, 'HTTPSConnection', return_value=conn):
            status, raw, headers = bulk.transport(bulk.BASES[0], 'POST', '/api/v1/auth/login')
        self.assertEqual((status, raw, headers), (200, b'{}', {'Set-Cookie':'owned-cookie'}))
        self.assertEqual(list(headers.items()), [('Set-Cookie', 'owned-cookie')])
        conn.close.assert_called_once()

    def test_partial_tls_acquisition_certificate_failure_closes_unstarted_listener(self):
        server = Mock()
        with patch.object(bulk, 'ThreadingHTTPServer', return_value=server), \
             patch.object(bulk.ssl, 'SSLContext') as tls:
            tls.return_value.load_cert_chain.side_effect = ValueError('bad cert')
            with self.assertRaises(ValueError):
                self.chain.local_sources.start('private-cert', 'private-key')
        self.chain.local_sources.close()
        server.server_close.assert_called_once()
        server.shutdown.assert_not_called()

    def test_tls_cleanup_attempts_all_acquired_servers_even_after_failure(self):
        first, second = Mock(), Mock()
        first.server_close.side_effect = OSError('first fault')
        self.chain.local_sources.servers = [(first, None), (second, None)]
        with self.assertRaises(AssertionError):
            self.chain.local_sources.close()
        second.server_close.assert_called_once()

    def test_tls_closes_when_owned_pg_cleanup_fails(self):
        self.chain.local_sources.close = Mock()
        with patch.object(bulk.base.Chain, 'cleanup', side_effect=bulk.Blocked('retained')), \
             patch.object(bulk, 'free_ports') as ports, self.assertRaises(bulk.Blocked):
            self.chain.cleanup()
        self.chain.local_sources.close.assert_called_once()
        ports.assert_called_once()

    def test_lock_cleanup_error_does_not_skip_owned_api_cleanup(self):
        proc = Mock()
        proc.poll.return_value = None
        proc.terminate.side_effect = OSError('lock failure')
        self.chain.lock_processes = [proc]
        self.chain.local_sources.close = Mock()
        with patch.object(bulk.base.Chain, 'cleanup') as cleanup, patch.object(bulk, 'free_ports'), self.assertRaises(AssertionError):
            self.chain.cleanup()
        cleanup.assert_called_once()
        self.chain.local_sources.close.assert_called_once()

    def test_lock_process_is_released_on_interrupt_after_acquisition(self):
        proc, close = Mock(), os.close
        proc.poll.side_effect = [None, 0]
        calls = []
        def interrupted_close(fd):
            if not calls:
                calls.append(fd)
                raise KeyboardInterrupt()
            close(fd)
        with patch.object(self.chain, 'sql', return_value=uuid.uuid4().hex), \
             patch.object(bulk.base, 'inspect_pg', return_value={'Id': bulk.PG_CID}), \
             patch.object(bulk.subprocess, 'Popen', return_value=proc), patch.object(bulk.os, 'close', interrupted_close):
            with self.assertRaises(KeyboardInterrupt), self.chain.hold_incident('a'):
                pass
        proc.wait.assert_called_once_with(timeout=5)
        self.assertEqual(self.chain.lock_processes, [proc])

    def test_waiting_request_is_joined_after_row_release_even_if_timing_fault_fails(self):
        order = []
        @contextlib.contextmanager
        def held(_key):
            try:
                yield 123
            finally:
                order.append('released')
        worker = Mock()
        worker.is_alive.return_value = False
        worker.join.side_effect = lambda **_: order.append('joined')
        self.chain.groups['a'], self.chain.counts['a'] = 'group:' + 'a'*24, 1
        with patch.object(self.chain, 'ledger', return_value={}), patch.object(self.chain, 'hold_incident', held), \
             patch.object(bulk.threading, 'Thread', return_value=worker), patch.object(self.chain, 'sql', return_value='[{"pid":9}]'):
            with self.assertRaises(ValueError):
                self.chain.waiting_fault('a', 'not_found', lambda: (_ for _ in ()).throw(ValueError('fault')))
        worker.join.assert_called_once_with(timeout=15)
        self.assertEqual(order, ['released', 'joined'])

    def test_no_inventory_run_can_claim_live_proof(self):
        with patch.object(bulk, 'doctor') as doctor, patch.object(bulk, 'Chain') as chain, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(bulk.main(['--inventory']), 0)
        doctor.assert_not_called(); chain.assert_not_called()

    def test_handoff_refuses_passed_label_without_required_cases_or_cleanup(self):
        with patch.object(bulk.base, 'source_fingerprint', return_value=bulk.SOURCE_SHA):
            report = bulk.handoff(self.chain, 'passed')
        self.assertFalse(report['collection_qualified'])
        self.assertFalse(report['investigate_business_chain_verified'])
        self.assertFalse(report['silence_pending_protocol_chain_verified'])


class NullableOrderedResultTests(unittest.TestCase):
    def setUp(self):
        self.group = 'group:' + 'a'*24
        self.inv, self.proposal = uuid.uuid4().hex, uuid.uuid4().hex
        self.item = dict(id=self.group, outcome='pending_approval', matched=2, changed=0,
                         investigation_id=self.inv, proposal_id=self.proposal, error=None)
        self.expect = [dict(id=self.group, outcome='pending_approval', matched=2, changed=0)]

    def test_pending_native_ids_require_no_changed_members_and_no_error(self):
        bulk.verify_result(dict(action='silence', results=[self.item]), 'silence', self.expect)
        for key, value in [('investigation_id', 'INV-fake'), ('proposal_id', 'P-fake'), ('changed', 1), ('error', 'pending_approval')]:
            with self.subTest(key=key), self.assertRaises(AssertionError):
                bulk.verify_result(dict(action='silence', results=[dict(self.item, **{key:value})]), 'silence', self.expect)

    def test_failure_nullable_fields_and_result_order_are_not_optional(self):
        failure = dict(self.item, outcome='not_found', matched=0, investigation_id=None, proposal_id=None, error='not_found')
        expected = [dict(id=self.group, outcome='not_found', matched=0, changed=0)]
        bulk.verify_result(dict(action='investigate', results=[failure]), 'investigate', expected)
        for item in [dict(failure, proposal_id=self.proposal), dict(failure, id='group:'+'b'*24), {key:value for key,value in failure.items() if key!='proposal_id'}]:
            with self.assertRaises(AssertionError):
                bulk.verify_result(dict(action='investigate', results=[item]), 'investigate', expected)


if __name__ == '__main__':
    unittest.main()
