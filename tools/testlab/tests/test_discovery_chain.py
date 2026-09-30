"""Offline discovery guards; every SQL, HTTP and process lifecycle is mocked."""
import contextlib
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
import discovery_chain as discovery
from inventory import actual_interface


class DiscoveryProtectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.contract = readonly.Contract(readonly.ROOT)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        binary = self.root / '.local/testlab/environments' / readonly.OWNER / 'backend/aiops'
        binary.parent.mkdir(parents=True); binary.write_bytes(b'owned synthetic binary')
        self.data = dict(owner=readonly.OWNER, binary=str(binary), binary_sha256='binary-proof', source_tree_sha='source-proof')
        with patch.object(readonly, 'Contract', return_value=self.contract):
            self.chain = discovery.Chain(self.root, self.data, str(uuid.uuid4()))
        self.chain.cookies['reader'] = 'aiops_session=synthetic-owner-session'
        self.chain.export_url = '/api/v1/audit/exports/audit-' + 'a' * 36
        self.target = 'b' * 32
        self.audit_ids = {'1', '2', '3'}
        self.chain.ids['runbook'] = self.target
        self.audit_rows = {x: {'id': x, 'actor': 'synthetic', 'action': 'runbook.created'} for x in self.audit_ids}

    def csv(self, target=None):
        target = self.target if target is None else target
        return ('id,time,actor_id,actor_kind,action,target,outcome\r\n' + ''.join(
            f'{x},2026-10-01T00:00:00Z,synthetic,user,runbook.created,{target},succeeded\r\n'
            for x in sorted(self.audit_ids))).encode()

    def headers(self):
        return {'Content-Type': 'text/csv; charset=utf-8', 'Content-Disposition': 'attachment; filename="audit.csv"',
                'Cache-Control': 'no-store'}

    def test_constructor_reuses_exact_shared_tuple_and_rejects_foreign_owner(self):
        self.assertEqual(self.chain.database, 'aiops_testlab_readonly_' + self.chain.nonce)
        self.assertEqual(self.chain.state['ports'], [18602, 18603])
        self.assertEqual(self.chain.state['suite'], 'formal-discovery')
        self.assertNotIn(self.chain.password, (self.chain.work / 'state.json').read_text())
        with patch.object(readonly, 'Contract', return_value=self.contract), self.assertRaises(readonly.Blocked):
            discovery.Chain(self.root, dict(self.data, owner='foreign'), str(uuid.uuid4()))
        self.assertIs(discovery.Chain.cleanup, readonly.Chain.cleanup)
        self.assertIs(discovery.Chain.provision, readonly.Chain.provision)
        self.assertIs(discovery.Chain.start, readonly.Chain.start)

    def test_seed_adds_only_owned_grants_no_published_documents_exports_or_audits(self):
        with patch.object(readonly.Chain, 'seed') as seed, patch.object(self.chain, 'sql') as sql:
            self.chain.seed()
        seed.assert_called_once()
        statement = sql.call_args.args[0]
        for forbidden in ['knowledge_documents', 'published', 'management_audit', 'v3_audit_exports', "'global'"]:
            self.assertNotIn(forbidden, statement)
        self.assertEqual(statement.count('INSERT INTO v3_role_grants'), 6)
        self.assertIn(readonly.quote(self.chain.roles['reader']), statement)
        self.assertIn(readonly.quote(self.chain.roles['peer']), statement)
        self.assertNotIn(readonly.quote(self.chain.roles['denied']), statement)

    def test_keyword_positive_requires_nonempty_exact_content_and_explicit_degradation(self):
        good = {'mode': 'keyword', 'degraded': True, 'hits': [{'title': self.chain.title, 'snippet': self.chain.marker,
                'keyword': 1, 'vector': 0, 'score': 1}]}
        self.chain.keyword_hit(good)
        for bad in [dict(good, hits=[]), dict(good, degraded=False), dict(good, mode='hybrid'),
                    dict(good, hits=[dict(good['hits'][0], title='foreign')]),
                    dict(good, hits=[dict(good['hits'][0], vector=1)]),
                    dict(good, hits=[dict(good['hits'][0], snippet='missing source')])]:
            with self.subTest(bad=bad), self.assertRaises(AssertionError): self.chain.keyword_hit(bad)

    def test_publication_is_three_http_operations_with_independent_peer_before_positive_search(self):
        calls = []
        def call(user, method, path, body=None, **kwargs):
            calls.append((user, method, path, body, kwargs))
            if path == '/api/v1/runbooks': return {'id': self.target}, {}
            if '/reviews' in path and user == 'peer': value = {'status': 'published'}
            elif path.endswith('/knowledge/search'):
                positive = len(calls) == 6
                value = {'mode': 'keyword', 'degraded': True, 'hits': ([{'title': self.chain.title,
                    'snippet': self.chain.marker, 'keyword': 1, 'vector': 0, 'score': 1}] if positive else [])}
            else: value = {}
            if kwargs.get('validate'): kwargs['validate'](value)
            return value, {}
        with patch.object(self.chain, 'call', side_effect=call), patch.object(self.chain, 'sql') as sql:
            self.chain.knowledge()
        sql.assert_not_called()
        self.assertEqual(calls[0][3]['status'], 'draft')
        self.assertEqual(calls[2][2], '/api/v1/runbooks/' + self.target + '/submit')
        self.assertEqual(calls[3][4]['code'], 'self_review_forbidden')
        self.assertEqual(calls[4][0], 'peer')
        self.assertEqual(calls[6][4]['environment'], 'staging')
        self.assertEqual(calls[7][0], 'denied')
        self.assertEqual(calls[-1][4]['code'], 'invalid_query')

    def test_people_peek_uses_display_name_not_id_and_keeps_unknown_facts(self):
        with patch.object(self.chain, 'call') as call: self.chain.people()
        first = call.call_args_list[0]
        self.assertIn(self.chain.marker + '%20peer', first.args[2])
        self.assertNotIn(self.chain.users['peer'], first.args[2])
        value = {'id': self.chain.users['peer'], 'name': self.chain.marker + ' peer', 'role': None, 'oncall': None, 'load': None}
        first.kwargs['validate'](value)
        with self.assertRaises(AssertionError): first.kwargs['validate'](dict(value, oncall='off duty'))
        self.assertEqual([x.kwargs.get('want', 200) for x in call.call_args_list], [200, 404, 404, 422])

    def test_export_url_rejects_external_authority_queries_wrong_shape_and_second_export(self):
        self.chain.export_url = None
        for url in ['https://business.invalid/audit', '/api/v1/audit/exports/audit-' + 'a' * 35,
                    '/api/v1/audit/exports/audit-' + 'a' * 36 + '?ignored=true', '/api/v1/audit/exports/../foreign']:
            with self.assertRaises(AssertionError): self.chain.accept_export_url({'url': url})
        native = '/api/v1/audit/exports/audit-' + 'a' * 36
        self.chain.accept_export_url({'url': native})
        with self.assertRaises(AssertionError): self.chain.accept_export_url({'url': native})

    def test_csv_guard_rejects_absent_owner_or_unissued_foreign_route_before_transport(self):
        for url, cookie in [(None, 'owner'), ('https://foreign.invalid/file.csv', 'owner'),
                            ('/api/v1/audit/exports/audit-' + 'a' * 36, None)]:
            self.chain.export_url = url
            self.chain.cookies = {} if cookie is None else {'reader': cookie}
            with patch.object(readonly, 'transport') as network, self.assertRaises(readonly.Blocked):
                self.chain.download_csv(self.audit_rows, self.target)
            network.assert_not_called()

    def test_native_csv_exact_rows_headers_and_private_immutable_artifact(self):
        raw = self.csv()
        with patch.object(readonly, 'transport', return_value=(200, raw, self.headers())) as network, patch.object(discovery, 'emit') as output:
            self.chain.download_csv(self.audit_rows, self.target)
        args, kwargs = network.call_args
        self.assertEqual(args, (readonly.BASES[1], 'GET', self.chain.export_url))
        self.assertEqual(kwargs['headers']['X-AIOps-Environment'], 'test')
        self.assertEqual(kwargs['headers']['Cookie'], self.chain.cookies['reader'])
        self.assertEqual((self.chain.work / 'native-export.csv').stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.chain.csv_sha256, readonly.hashlib.sha256(raw).hexdigest())
        row = output.call_args.kwargs['interface']
        self.assertEqual(row['path'], '/api/v1/audit/exports/{id}')
        self.assertTrue(row['registration_evidence'])
        actual_interface(row)

    def test_csv_rejects_wrong_owner_rows_header_or_count_with_first_red(self):
        for raw, headers in [(self.csv('foreign-target'), self.headers()), (self.csv().splitlines()[0], self.headers()),
                             (self.csv().replace(b'synthetic,user', b'foreign-actor,user'), self.headers()),
                             (self.csv().replace(b'runbook.created', b'foreign.action'), self.headers()),
                             (self.csv(), dict(self.headers(), **{'Cache-Control': 'public'}))]:
            with patch.object(readonly, 'transport', return_value=(200, raw, headers)), patch.object(discovery, 'emit') as output:
                with self.assertRaises(AssertionError): self.chain.download_csv(self.audit_rows, self.target)
            self.assertFalse(output.call_args.kwargs['interface']['registration_evidence'])
            self.assertTrue((self.chain.work / 'first-failure.json').exists())

    def test_csv_404_does_not_count_route_registration(self):
        with patch.object(readonly, 'transport', return_value=(404, b'{}', {})), patch.object(discovery, 'emit') as output:
            with self.assertRaises(AssertionError): self.chain.download_csv(self.audit_rows, self.target)
        row = output.call_args.kwargs['interface']
        self.assertFalse(row['registration_evidence']); self.assertEqual(row['actual']['status'], 404)
        actual_interface(row)

    def test_csv_contract_error_survives_all_reporting_failures_and_shared_cleanup_runs_once(self):
        self.chain.run = lambda: self.chain.download_csv(self.audit_rows, self.target)
        with patch.object(readonly, 'transport', return_value=(404, b'{}', {})), \
             patch.object(self.chain, 'first_red', side_effect=OSError('storage failed')), \
             patch.object(readonly, 'append_json', side_effect=OSError('storage failed')), \
             patch.object(discovery, 'emit', side_effect=OSError('stdout failed')), \
             patch.object(readonly, 'emit'), patch.object(self.chain, 'cleanup') as cleanup:
            with self.assertRaisesRegex(AssertionError, 'CSV HTTP status'): readonly.execute(self.chain)
        cleanup.assert_called_once()

    def test_audit_source_export_readback_and_revocation_use_only_native_result_reads(self):
        self.chain.ids['runbook'] = self.target; self.chain.export_url = None; self.chain.csv_sha256 = 'hash'
        native = [{'id': str(i), 'action': action, 'actor': self.chain.users[actor]} for i, (action, actor) in enumerate(
            [('runbook.created', 'reader'), ('runbook.submitted', 'reader'), ('runbook.review.approved', 'peer')], 1)]
        def sql(statement, read=False):
            if 'FROM management_audit' in statement: return json.dumps(native)
            if 'FROM v3_audit_exports' in statement:
                return json.dumps({'owner': self.chain.users['reader'], 'environments': ['test'], 'includes_global': False, 'csv_sha256': 'hash'})
            return ''
        def call(user, method, path, body=None, **kwargs):
            value = {'items': [{'id': str(i)} for i in (1, 2, 3)]} if '/audit?' in path else {'id': '3', 'chain_ok': True}
            if path == '/api/v1/audit/export' and kwargs.get('want', 200) == 200:
                value = {'url': '/api/v1/audit/exports/audit-' + 'a' * 36}
            if kwargs.get('validate'): kwargs['validate'](value)
            return value, {}
        with patch.object(self.chain, 'sql', side_effect=sql) as database, patch.object(self.chain, 'call', side_effect=call) as http, \
             patch.object(self.chain, 'download_csv') as csv_download:
            self.chain.audit_export()
        csv_download.assert_called_once_with({x['id']: x for x in native}, self.target)
        mutations = [c.args[0] for c in database.call_args_list if not c.kwargs.get('read')]
        self.assertEqual(len(mutations), 1); self.assertTrue(mutations[0].startswith('DELETE FROM v3_role_grants'))
        self.assertTrue(any(c.args[0] == 'peer' and c.kwargs.get('want') == 404 for c in http.call_args_list))
        self.assertTrue(any(c.kwargs.get('environment') == 'staging' and c.kwargs.get('want') == 403 for c in http.call_args_list))
        self.assertEqual(http.call_args_list[-1].kwargs['want'], 403)

    def test_orchestration_reuses_one_finally_cleanup_and_source_checks(self):
        order = []
        patches = [patch.object(self.chain, name, side_effect=lambda name=name: order.append(name))
                   for name in ('provision', 'start', 'seed', 'knowledge', 'people', 'audit_export', 'frozen')]
        with contextlib.ExitStack() as stack:
            for item in patches: stack.enter_context(item)
            login = stack.enter_context(patch.object(self.chain, 'login'))
            cleanup = stack.enter_context(patch.object(self.chain, 'cleanup'))
            stack.enter_context(patch.object(readonly, 'emit'))
            external = stack.enter_context(patch.object(discovery, 'emit'))
            result = readonly.execute(self.chain)
        cleanup.assert_called_once(); self.assertEqual(login.call_count, 4)
        self.assertEqual(order, ['provision', 'start', 'seed', 'frozen', 'knowledge', 'frozen', 'people', 'frozen', 'audit_export', 'frozen'])
        self.assertEqual(result['business_operations'], 9)
        self.assertEqual(result['source_tree_sha'], 'source-proof')
        self.assertTrue(result['external_todo'])
        self.assertEqual(external.call_args.args[1], 'external_todo')

    def test_main_refuses_live_without_explicit_opt_in(self):
        with patch.object(sys, 'argv', ['discovery_chain.py']), patch('sys.stderr', io.StringIO()), \
             patch.object(readonly, 'doctor') as doctor, patch.object(readonly, 'execute') as execute:
            with self.assertRaises(SystemExit) as result: discovery.main()
        self.assertEqual(result.exception.code, 2); doctor.assert_not_called(); execute.assert_not_called()


if __name__ == '__main__':
    unittest.main()
