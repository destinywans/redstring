"""Meaningful bounded safety/authority/evidence counterexamples; no live API/PG."""
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
import api_services_functional_coverage as services


class ProtocolScopeTests(unittest.TestCase):
    def setUp(self):
        self.chain = Mock(nonce=uuid.uuid4().hex)
        self.protocol = services.Protocols(self.chain)
        self.app = uuid.uuid4().hex
        self.protocol.allowed_apps.add(self.app)

    def test_only_fixed_tls_source_scopes_and_matching_private_token_are_allowed(self):
        path = '/snapshot?application_id=' + self.app + '&environment_id=test'
        auth = 'Bearer ' + self.protocol.tokens['resilience']
        status, body, record = self.protocol.respond(services.RESILIENCE_PORT, path, auth)
        self.assertEqual(status, 200)
        self.assertEqual(body['application_id'], self.app)
        self.assertEqual(record['application_id'], self.app)
        for target, credential in [(path, 'Bearer wrong'), (path.replace(self.app, 'foreign'), auth),
                                   (path.replace('=test', '=prod'), auth), (path + '&environment_id=test', auth),
                                   (path + '&url=https://business.invalid', auth), ('https://business.invalid' + path, auth),
                                   (path + '#private', auth), (path.replace('/snapshot', '/apply'), auth)]:
            with self.subTest(target=target), self.assertRaises(services.Blocked):
                self.protocol.respond(services.RESILIENCE_PORT, target, credential)

    def test_kubernetes_only_full_bounded_lists_and_exact_watches_no_egress_or_resource_writes(self):
        for target in services.KUBE_ENDPOINTS:
            self.assertEqual(services.kube_path('/test' + target + '?limit=500'), ('test', target, False))
        self.assertEqual(services.kube_path('/staging/api/v1/pods?watch=true&allowWatchBookmarks=true&resourceVersion=1&timeoutSeconds=25'),
                         ('staging', '/api/v1/pods', True))
        for path in ['/test/api/v1/secrets?limit=500', '/prod/api/v1/pods?limit=500',
                     '/test/api/v1/pods?limit=10000', '/test/api/v1/pods?limit=500&limit=500',
                     'http://business.invalid/test/api/v1/pods?limit=500', '/test/api/v1/pods?limit=500&continue=foreign',
                     '/test/api/v1/pods?watch=true&resourceVersion=2']:
            with self.subTest(path=path), self.assertRaises(services.Blocked):
                services.kube_path(path)

    def test_collector_fixture_supplies_cluster_uid_nonempty_workload_and_distinct_environment_identity(self):
        rows = services.workload_objects(self.chain.nonce, 'test')
        other = services.workload_objects(self.chain.nonce, 'staging')
        self.assertTrue(any(x['metadata']['name'] == 'kube-system' and x['metadata']['uid'] for x in rows['/api/v1/namespaces']))
        workload = rows['/apis/apps/v1/deployments'][0]
        self.assertNotEqual(workload['metadata']['uid'], other['/apis/apps/v1/deployments'][0]['metadata']['uid'])
        self.assertEqual(workload['spec']['replicas'], 2)
        self.assertEqual(workload['metadata']['labels']['team'], 'unverified-team-claim')
        self.assertNotIn('applications', workload)
        self.assertEqual(rows, services.workload_objects(self.chain.nonce, 'test'))

    def test_resilience_faults_mutate_exact_completeness_scope_and_parse_fields_without_external_write(self):
        path = '/snapshot?application_id=' + self.app + '&environment_id=test'
        auth = 'Bearer ' + self.protocol.tokens['resilience']
        original = services.resilience_observation(self.app, 'test')
        self.assertEqual(set(original['sources']), {'rollout', 'breakers', 'limits', 'flags', 'hpa'})
        self.assertTrue(all(x['complete'] and x['application_id'] == self.app and x['environment_id'] == 'test' and x['revision'] for x in original['sources'].values()))
        for mode in ('503', '429', 'cross_scope', 'incomplete', 'stale', 'count_mismatch', 'unknown'):
            self.protocol.mode = mode
            status, body, _ = self.protocol.respond(services.RESILIENCE_PORT, path, auth)
            if mode in ('503', '429'):
                self.assertEqual(status, int(mode))
            elif mode == 'cross_scope':
                self.assertNotEqual(body['sources']['flags']['application_id'], self.app)
            elif mode == 'incomplete':
                self.assertFalse(body['sources']['hpa']['complete'])
            elif mode == 'stale':
                self.assertEqual(body['captured_at'], '2000-01-01T00:00:00Z')
            elif mode == 'count_mismatch':
                self.assertNotEqual(body['sources']['flags']['item_count'], len(body['view']['flags']))
            else:
                self.assertIn('unknown_field', body)


class LifecycleEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.data = dict(owner=services.OWNER, source_tree_sha=services.SOURCE_SHA,
                         binary_sha256=services.BINARY_SHA, helper_sha256={}, postgres_container='owned')
        with patch.object(services, 'Contract'):
            self.chain = services.Chain(self.root, self.data, str(uuid.uuid4()))

    def test_contract_bounds_targets_and_support_routes_with_real_main_166_separation(self):
        contract = services.Contract(services.ROOT)
        self.assertEqual(len(contract.main), 166)
        cases = [('GET', '/api/v1/services/discover?cluster=owned', '/api/v1/services/discover'),
                 ('GET', '/api/v1/services/owned/resilience', '/api/v1/services/{id}/resilience'),
                 ('POST', '/api/v1/services/owned/resilience/proposals', '/api/v1/services/{id}/resilience/proposals')]
        for verb, path, expected in cases:
            self.assertEqual(contract.canonical(verb, path), (expected, 'main'))
        for verb, path in [('POST', '/api/v1/proposals/owned/approve'), ('GET', '/api/v1/credentials'),
                           ('POST', '/api/v1/services/owned/resilience'), ('GET', 'https://business.invalid/api/v1/services')]:
            with self.subTest(path=path), self.assertRaises(services.Blocked):
                contract.canonical(verb, path)

    def test_invocation_private_paths_bound_nonce_and_no_business_or_old_credentials_loaded(self):
        self.assertEqual(self.chain.work.parent, self.root / '.local/testlab/services-parallel')
        self.assertTrue(self.chain.database.endswith(uuid.UUID(self.chain.run_id).hex))
        self.assertTrue(self.chain.database.startswith('aiops_testlab_services_'))
        self.assertEqual((self.chain.work / 'credentials.json').stat().st_mode & 0o777, 0o600)
        state = (self.chain.work / 'state.json').read_text()
        self.assertNotIn(self.chain.password, state)
        self.assertNotIn(self.chain.db_password, state)
        self.assertIn('protocol_mock', state)
        self.assertFalse((self.root / '.local/testlab/environments').exists())

    def test_first_red_is_immutable_and_stays_within_exclusively_owned_evidence(self):
        self.chain.first_red({'error': 'original'})
        self.chain.first_red({'error': 'later'})
        path = self.chain.work / 'first-failure.json'
        self.assertEqual(json.loads(path.read_text()), {'error': 'original'})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.root / '.local/testlab/first-failures').exists())

    def test_unknown_pg_cid_prevents_all_sql_before_reused_pg_execution(self):
        with patch.object(services.readonly, 'inspect_pg', return_value={'Id': 'foreign'}), \
             patch.object(services.readonly.Chain, 'pg') as pg, self.assertRaises(services.Blocked):
            self.chain.pg('SELECT 1;')
        pg.assert_not_called()

    def test_normal_response_without_validator_is_rejected_before_transport(self):
        self.chain.contract.canonical.return_value = ('/api/v1/services/discover', 'main')
        with patch.object(self.chain, 'frozen'), patch.object(services.readonly.Chain, 'call') as call, self.assertRaises(services.Blocked):
            self.chain.call('peer', 'GET', '/api/v1/services/discover')
        call.assert_not_called()

    def test_protocol_mode_is_preserved_in_persistent_interfaces_and_not_just_stdout(self):
        services._thread.mode, services._thread.scope = 'protocol_mock', 'test protocol only'
        try:
            services.append_json(self.chain.work / 'interfaces.jsonl', dict(method='GET', path='/api/v1/services/discover', status='passed'))
        finally:
            del services._thread.mode
            del services._thread.scope
        row = json.loads((self.chain.work / 'interfaces.jsonl').read_text())
        self.assertEqual(row['evidence_mode'], 'protocol_mock')
        self.assertEqual(row['proof_scope'], 'test protocol only')

    def test_protocol_servers_close_even_when_owned_database_cleanup_fails(self):
        self.chain.protocol.close = Mock()
        with patch.object(services.readonly.Chain, 'cleanup', side_effect=services.Blocked('cleanup readback failed')), \
             patch.object(services, 'free_ports') as ports, self.assertRaises(services.Blocked):
            self.chain.cleanup()
        self.chain.protocol.close.assert_called_once()
        ports.assert_called_once()

    def test_private_lease_refuses_parallel_same_package_without_serializing_other_packages(self):
        with services.exclusive(self.root):
            with self.assertRaises(services.Blocked), services.exclusive(self.root):
                pass
        self.assertFalse((self.root / '.local/testlab/api-readonly.lock').exists())

    def test_partial_topology_missing_frozen_fields_stays_explicit_and_cannot_be_normal_source_proof(self):
        contract = services.Contract(services.ROOT)
        contract.shape('GET', '/api/v1/topology', 200, dict(complete_v3_dto=False, nodes=[{'id': 'owned'}], unavailable_fields=['complete_resource_coverage']))
        with self.assertRaises(AssertionError):
            contract.shape('GET', '/api/v1/topology', 200, dict(complete_v3_dto=False, nodes=[{'id': 'owned'}]))
        self.assertIn('GET /api/v1/topology', services.BLOCKERS)

    def test_inventory_mode_never_starts_resources_or_doctor(self):
        with patch.object(services, 'doctor') as doctor, patch.object(services, 'Chain') as chain, \
             contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(services.main(['--inventory']), 0)
        doctor.assert_not_called()
        chain.assert_not_called()

    def test_blocked_label_with_failure_cannot_claim_real_or_mock_normal_targets(self):
        self.chain.state['started_at'] = services.now()
        services.readonly.append_json(self.chain.work / 'cases.jsonl', dict(name='observed.failure', status='failed'))
        with patch.object(services.readonly, 'source_fingerprint', return_value='changed-source'):
            report = services.handoff(self.chain, 'blocked')
        self.assertEqual(report['real_normal_targets'], [])
        self.assertEqual(report['protocol_mock_normal_targets'], [])

    def test_passed_http_requires_completed_suite_cleanup_and_consistent_persistent_mode(self):
        self.chain.state['started_at'] = services.now()
        rows = [dict(name=name, status='passed', evidence_mode='real_local') for name in services.REQUIRED]
        interface = dict(name='http.mock', call_id=self.chain.run_id + '-1', run_id=self.chain.run_id,
                         method='GET', path='/api/v1/services/discover', status='passed', route_class='main',
                         scenario='normal', scenario_detail='normal', registration_evidence=True,
                         actual={'status': 200}, evidence_mode='protocol_mock')
        rows.append(dict(name=interface['name'], status='passed', evidence_mode='protocol_mock', interface=interface))
        services.readonly._private(self.chain.work / 'result.json', {'required_cases': list(services.REQUIRED)})
        services.readonly._private(self.chain.work / 'cleanup.json', dict(database_absent=True, role_absent=True, owned_processes_exited=True))
        services.readonly._private(self.chain.work / 'protocol-cleanup.json', dict(ports_free=[*services.PORTS, services.RESILIENCE_PORT, services.KUBE_PORT], held_servers_closed=True))
        services.readonly.private_file(self.chain.work / 'cases.jsonl', '\n'.join(json.dumps(row) for row in rows))
        services.readonly.private_file(self.chain.work / 'interfaces.jsonl', json.dumps(interface))
        with patch.object(services.readonly, 'source_fingerprint', return_value=services.SOURCE_SHA):
            report = services.handoff(self.chain, 'blocked')
        self.assertEqual(report['real_normal_targets'], [])
        self.assertEqual(report['protocol_mock_normal_targets'], ['GET /api/v1/services/discover'])
        # Persisted interface proof must agree with the case, including its Mock boundary.
        conflict = dict(interface, evidence_mode='real_local')
        (self.chain.work / 'interfaces.jsonl').write_text(json.dumps(conflict))
        proof = services.qualify(self.chain, rows, 'blocked', services.SOURCE_SHA)
        self.assertEqual(proof['normal_http_proofs']['protocol_mock'], {})
        self.assertEqual(proof['normal_http_proofs']['real_local'], {})
        (self.chain.work / 'interfaces.jsonl').write_text(json.dumps(interface))
        for final_source, observed in [('changed-source', rows), (services.SOURCE_SHA, rows[1:])]:
            proof = services.qualify(self.chain, observed, 'blocked', final_source)
            self.assertFalse(proof['ready'])
            self.assertEqual(proof['normal_http_proofs']['protocol_mock'], {})
        (self.chain.work / 'result.json').unlink()
        self.assertFalse(services.qualify(self.chain, rows, 'blocked', services.SOURCE_SHA)['ready'])
        services.readonly._private(self.chain.work / 'result.json', {'required_cases': list(services.REQUIRED)})
        (self.chain.work / 'cleanup.json').unlink()
        proof = services.qualify(self.chain, rows, 'blocked', services.SOURCE_SHA)
        self.assertFalse(proof['ready'])
        self.assertEqual(proof['normal_http_proofs']['protocol_mock'], {})

    def cli_result(self, ready=True, failure=None):
        data = dict(self.data, runtime_sha=services.RUNTIME_SHA, postgres_container_id=services.PG_CID)
        report = dict(case_counts={'passed': 0, 'failed': 0, 'blocked': 3}, qualification={'ready': ready})
        def completed(_chain):
            if failure:
                raise failure
            for index, reason in enumerate(services.BLOCKERS.values()):
                services.emit('services.prerequisite.' + str(index), 'blocked', dict(reason=reason, normal_verified=False))
        with patch.object(services, 'doctor', return_value=data), patch.object(services, 'Chain', return_value=self.chain), \
             patch.object(services.readonly, 'execute', side_effect=completed), patch.object(services, 'handoff', return_value=report), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            code = services.main(['--root', str(self.root), '--live', '--run-id', self.chain.run_id])
        return code, [json.loads(line) for line in output.getvalue().splitlines()]

    def test_cli_completed_collection_returns_zero_but_preserves_blocked_cases_for_runner(self):
        code, rows = self.cli_result()
        self.assertEqual(code, 0)
        blockers = [row for row in rows if row['name'].startswith('services.prerequisite.')]
        self.assertEqual(len(blockers), 3)
        self.assertTrue(all(row['status'] == 'blocked' and row['actual']['normal_verified'] is False for row in blockers))
        self.assertEqual(rows[-1]['name'], 'services.handoff')
        self.assertEqual(rows[-1]['status'], 'blocked')
        self.assertFalse(rows[-1]['actual']['full_acceptance'])

    def test_cli_incomplete_qualification_cannot_return_collection_success(self):
        code, _ = self.cli_result(ready=False)
        self.assertNotEqual(code, 0)

    def test_cli_actual_exception_stays_nonzero_even_with_a_ready_handoff_label(self):
        code, rows = self.cli_result(failure=services.Blocked('observed execution failure'))
        self.assertNotEqual(code, 0)
        self.assertTrue(any(row['name'] == 'services.package_error' and row['status'] == 'failed' for row in rows))


if __name__ == '__main__':
    unittest.main()
