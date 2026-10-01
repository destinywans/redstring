"""Offline guards and protocol assertions; no Docker, Go or real component calls."""
import contextlib
import copy
import io
import json
import sys
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_components_coverage as coverage


class ComponentCoverageTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for file in ('internal/apicontract/openapi.json', 'docs/api/v3-contract.md'):
            target = self.root / file
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((coverage.ROOT / file).read_bytes())
        self.info = {'Id': 'a' * 64, 'Name': '/aiops-' + coverage.OWNER + '-prometheus',
                     'Image': 'sha256:' + 'b' * 64,
                     'Config': {'Image': coverage.PROM_IMAGE,
                                'Labels': {'aiops.testlab.owner': coverage.OWNER}},
                     'State': {'Running': True},
                     'NetworkSettings': {'Ports': {'9090/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18570'}]}}}
        self.data = {'owner': coverage.OWNER, 'binary_sha256': 'synthetic',
                     'source_tree_sha': 'synthetic', 'postgres_container': 'owned-pg',
                     'component_identity': coverage.guard_prometheus(self.info),
                     'helper_sha256': coverage.helper_hashes(coverage.ROOT)}
        self.chain = coverage.Chain(self.root, self.data, str(uuid.uuid4()))

    def test_inventory_is_offline_and_has_only_bounded_six_main_routes(self):
        with patch.object(coverage, 'doctor') as doctor, patch.object(coverage.management, 'execute') as execute, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(coverage.main(['--inventory']), 0)
        result = json.loads(output.getvalue())
        self.assertTrue(result['offline_only'])
        self.assertEqual(len(result['targets']), 6)
        self.assertEqual(result['ports'], [18606, 18607, 18612])
        doctor.assert_not_called()
        execute.assert_not_called()

    def test_execution_requires_explicit_live(self):
        with contextlib.redirect_stderr(io.StringIO()):
            with self.assertRaises(SystemExit): coverage.main([])

    def test_component_guard_rejects_each_ownership_or_listener_drift(self):
        for field, value in [('Id', 'invalid'), ('Name', '/foreign'), ('Image', 'invalid')]:
            info = copy.deepcopy(self.info); info[field] = value
            with self.subTest(field=field):
                with self.assertRaises(coverage.Blocked): coverage.guard_prometheus(info)
        for section, field, value in [('State', 'Running', False), ('Config', 'Image', 'prom/prometheus:latest'), ('Config', 'Labels', {})]:
            info = copy.deepcopy(self.info); info[section][field] = value
            with self.subTest(field=field):
                with self.assertRaises(coverage.Blocked): coverage.guard_prometheus(info)
        for mapping in [[{'HostIp': '0.0.0.0', 'HostPort': '18570'}], [{'HostIp': '127.0.0.1', 'HostPort': '9090'}], None]:
            info = copy.deepcopy(self.info); info['NetworkSettings']['Ports']['9090/tcp'] = mapping
            with self.assertRaises(coverage.Blocked): coverage.guard_prometheus(info)

    def test_component_cid_and_image_pin_are_both_immutable(self):
        pinned = coverage.guard_prometheus(self.info)
        for field in ('Id', 'Image'):
            info = copy.deepcopy(self.info)
            info[field] = 'c' * 64 if field == 'Id' else 'sha256:' + 'c' * 64
            with self.assertRaises(coverage.Blocked): coverage.guard_prometheus(info, pinned)

    def test_pg_cid_mismatch_prevents_parent_sql_operation(self):
        with patch.object(coverage.management, 'inspect_pg', return_value={'Id': 'foreign'}), patch.object(coverage.management.Chain, 'pg') as sql:
            with self.assertRaises(coverage.Blocked): self.chain.pg('CREATE DATABASE forbidden')
        sql.assert_not_called()
        with patch.object(coverage.management, 'inspect_pg', return_value={'Id': coverage.PG_CID}), patch.object(coverage.management.Chain, 'pg', return_value='safe') as sql:
            self.assertEqual(self.chain.pg('SELECT 1'), 'safe')
        sql.assert_called_once()

    def test_exact_route_canonical_and_auxiliary_support(self):
        contract = self.chain.contract
        for method, path in coverage.TARGETS:
            materialized = path.replace('{id}', 'owned').replace('{type}', 'prometheus')
            self.assertEqual(contract.canonical(method, '/api/v1' + materialized)[1], 'main')
        for method, path in [('POST', '/api/v1/connectors/owned/activate'), ('GET', '/api/v1/connectors/owned/tool-settings')]:
            self.assertEqual(contract.canonical(method, path)[1], 'auxiliary_or_historical')
        for method, path in [('DELETE', '/api/v1/connectors/owned'), ('POST', '/api/v1/proposals/owned/approve'), ('GET', '//outside'), ('GET', '/api/v1/connectors/owned/extra')]:
            with self.assertRaises(coverage.Blocked): contract.canonical(method, path)

    def test_target_normal_requires_business_validator_before_transport(self):
        with patch.object(coverage.management.Chain, 'call') as call:
            with self.assertRaises(coverage.Blocked): self.chain.call('reader', 'GET', '/api/v1/connectors')
            self.chain.call('reader', 'GET', '/api/v1/connectors', validate=lambda value: None)
            self.chain.call(None, 'GET', '/api/v1/connectors', want=401, scenario='authorization')
        self.assertEqual(call.call_count, 2)

    def test_helper_drift_is_blocked_before_parent_freeze(self):
        with patch.object(coverage, 'helper_hashes', return_value={'changed': 'hash'}), patch.object(coverage.management.Chain, 'frozen') as frozen:
            with self.assertRaises(coverage.Blocked): self.chain.frozen()
        frozen.assert_not_called()

    def test_seed_adds_only_identity_grants_and_no_business_rows(self):
        self.chain.sql = Mock()
        with patch.object(coverage.management.Chain, 'seed') as parent: self.chain.seed()
        parent.assert_called_once()
        statement = self.chain.sql.call_args.args[0]
        self.assertIn('v3_role_grants', statement)
        self.assertIn("'connectors','manage'", statement)
        for forbidden in ('v3_connectors', 'management_audit', 'applications', 'deployment_events'):
            self.assertNotIn(forbidden, statement)

    def test_business_sql_readback_is_explicitly_readonly(self):
        self.chain.connector_id = 'own-id'
        expected = {'enabled': True, 'status': 'ok', 'error_code': '',
                    'tools': [{'name': 'query_metrics', 'enabled': True}]}
        self.chain.sql = Mock(return_value=json.dumps(expected))
        self.assertEqual(self.chain.registration(), expected)
        self.assertTrue(self.chain.sql.call_args.kwargs['read'])
        self.assertTrue(self.chain.sql.call_args.args[0].startswith('SELECT '))

    def test_real_probe_never_follows_arbitrary_path_or_forwards_auth(self):
        response = Mock(status=200); response.read.return_value = b'Prometheus ready'
        conn = Mock(); conn.getresponse.return_value = response
        with patch.object(coverage.management, 'inspect_pg', return_value=self.info), patch.object(coverage.http.client, 'HTTPConnection', return_value=conn) as connection:
            self.assertEqual(coverage.read_ready(self.data['component_identity']), (200, b'Prometheus ready'))
        connection.assert_called_once_with('127.0.0.1', 18570, timeout=3)
        conn.request.assert_called_once_with('GET', '/-/ready')
        conn.close.assert_called_once()

    def test_relay_checks_component_pin_before_contacting_upstream(self):
        with patch.object(coverage.management, 'inspect_pg', return_value={'Id': 'foreign'}), patch.object(coverage.http.client, 'HTTPConnection') as conn:
            with self.assertRaises(coverage.Blocked): coverage.read_ready(self.data['component_identity'])
        conn.assert_not_called()

    def test_relay_response_budget_and_cleanup_close(self):
        response = Mock(status=200); response.read.return_value = b'x' * (coverage.PROBE_LIMIT + 1)
        conn = Mock(); conn.getresponse.return_value = response
        with patch.object(coverage.management, 'inspect_pg', return_value=self.info), patch.object(coverage.http.client, 'HTTPConnection', return_value=conn):
            with self.assertRaises(coverage.Blocked): coverage.read_ready(self.data['component_identity'])
        conn.close.assert_called_once()
        relay = Mock(); self.chain.relay = relay
        with patch.object(coverage.management.Chain, 'cleanup', side_effect=AssertionError('first cleanup')):
            with self.assertRaises(AssertionError): self.chain.cleanup()
        relay.close.assert_called_once()

    def test_healthy_dto_never_claims_tools_or_credential_authority(self):
        value = {'id': 'owned', 'brand': 'prometheus', 'scope': 'test', 'address': coverage.RELAY_URL,
                 'status': 'ok', 'services': [], 'tools': [], 'queries': [], 'tags': []}
        coverage.validate_detail(value, 'owned', 'ok')
        for field, wrong in [('tools', [{'name': 'query_metrics'}]), ('scope', 'staging'), ('status', 'unverified'), ('address', 'https://outside')]:
            mutated = dict(value, **{field: wrong})
            with self.assertRaises(AssertionError): coverage.validate_detail(mutated, 'owned', 'ok')

    def test_marker_success_requires_exact_stable_main_positive_set(self):
        self.chain.frozen = Mock()
        coverage.management.append_json(self.chain.work / 'interfaces.jsonl', {'method': 'GET', 'path': '/api/v1/connectors', 'route_class': 'main', 'registration_evidence': True})
        with self.assertRaises(AssertionError): self.chain.result()


    def test_cleanup_preserves_first_error_if_relay_also_fails(self):
        relay = Mock(); relay.close.side_effect = RuntimeError('relay failure')
        self.chain.relay = relay
        first = AssertionError('database cleanup failure')
        with patch.object(coverage.management.Chain, 'cleanup', side_effect=first):
            with self.assertRaises(AssertionError) as caught: self.chain.cleanup()
        self.assertIs(caught.exception, first)
        relay.close.assert_called_once()
        self.assertTrue((self.chain.work / 'relay-cleanup-error.json').is_file())

    def test_relay_failure_alone_cannot_report_success(self):
        relay = Mock(); relay.close.side_effect = RuntimeError('relay failure')
        self.chain.relay = relay
        with patch.object(coverage.management.Chain, 'cleanup'), patch.object(coverage, 'emit') as emit:
            with self.assertRaises(RuntimeError): self.chain.cleanup()
        emit.assert_not_called()

    def test_all_lifecycle_cases_use_valid_contract_and_rollback_aware_expected_statuses(self):
        # Test-harness simulation only: no evidence is inserted in the real Store.
        self.chain.frozen = Mock()
        self.chain.provision = Mock(); self.chain.start = Mock(); self.chain.seed = Mock()
        self.chain.login = Mock()
        state = {'enabled': False, 'status': 'unverified', 'error_code': '',
                 'tools': [{'name': 'query_metrics', 'enabled': False}]}
        counts = {'requests': 0, 'forwarded': 0, 'injected': 0, 'real_prometheus_statuses': []}
        relay = Mock(); relay.mode = 'ready'; relay.counts.side_effect = lambda: copy.deepcopy(counts)
        relay.set_mode.side_effect = lambda mode: setattr(relay, 'mode', mode)
        def sql(statement, read=False):
            if 'FROM v3_connectors' in statement:
                self.assertTrue(read); return json.dumps(state)
            if 'FROM management_audit' in statement:
                self.assertTrue(read)
                entries = [('connector.created', 'success'), ('connector.activation.attempted', 'failure'),
                           ('connector.activated', 'success'), ('connector.tools.updated', 'success'),
                           ('connector.connection.tested', 'success'), ('connector.connection.tested', 'failure'),
                           ('connector.connection.tested', 'success')]
                return json.dumps([{'action': action, 'outcome': outcome, 'actor_id': self.chain.users['peer'], 'changes': {}} for action, outcome in entries])
            self.assertTrue(statement.startswith('DELETE FROM v3_role_grants '))
            return ''
        self.chain.sql = Mock(side_effect=sql)
        def call(user, method, path, body=None, **kwargs):
            canonical, route_class = self.chain.contract.canonical(method, path)
            wanted = kwargs.get('want', 200)
            if method == 'POST' and (path.endswith('/activate') or path.endswith('/test')) and wanted in (200, 502, 503):
                counts['requests'] += 1
                if relay.mode == 'unavailable': counts['injected'] += 1
                else:
                    counts['forwarded'] += 1; counts['real_prometheus_statuses'].append(200)
            if wanted >= 400:
                value = {'error': {'code': kwargs['code'], 'message': 'declared fixture error', 'details': {'reason': 'upstream_503'}}}
                if wanted == 503: state.update(status='error', error_code='upstream_503')
            elif path == '/api/v1/connector-types/prometheus':
                value = {'brand': 'prometheus', 'auth_methods': [{'id': 'none'}, {'id': 'bearer'}], 'tools': [], 'version': '', 'webhook_url': ''}
            elif method == 'POST' and path == '/api/v1/connectors': value = {'id': 'owned'}
            elif path.endswith('/activate'):
                state.update(enabled=True, status='ok', error_code='')
                value = {'enabled': True, 'status': 'ok', 'webhook_active': False, 'credential_verified': False, 'verified_projects': 0}
            elif method == 'PATCH':
                state['tools'][0]['enabled'] = True
                value = {'ok': True, 'tools': copy.deepcopy(state['tools'])}
            elif path.endswith('/test'):
                state.update(status='ok', error_code=''); value = {'ok': True, 'latency_ms': 1}
            elif path.endswith('/tool-settings'):
                value = {'connector_id': 'owned', 'connector_enabled': state['enabled'], 'registered_tools': copy.deepcopy(state['tools']), 'agent_execution_available': False}
            elif path == '/api/v1/connectors' or path.startswith('/api/v1/connectors?'):
                if user == 'hidden': value = {'items': [], 'counts': {'ok': 0, 'unverified': 0, 'error': 0}}
                else:
                    status = state['status']; value = {'items': [{'id': 'owned'}], 'counts': {x: int(x == status) for x in ('ok', 'unverified', 'error')}}
            else:
                value = {'id': 'owned', 'brand': 'prometheus', 'scope': 'test', 'address': coverage.RELAY_URL,
                         'status': state['status'], 'services': [], 'tools': [], 'queries': [], 'tags': []}
            validator = kwargs.get('validate')
            if wanted < 300 and validator is not None: validator(value)
            coverage.management.append_json(self.chain.work / 'interfaces.jsonl',
                {'method': method, 'path': canonical, 'route_class': route_class,
                 'registration_evidence': wanted < 300 and kwargs.get('scenario', 'normal') == 'normal' and kwargs.get('purpose', 'target') == 'target'})
            return value, {}
        self.chain.call = Mock(side_effect=call)
        with patch.object(coverage, 'Relay', return_value=relay), patch.object(coverage, 'emit') as emit:
            result = self.chain.run()
        self.assertEqual(set(result['positive_routes'][i][0] + ' ' + result['positive_routes'][i][1] for i in range(6)),
                         {method + ' /api/v1' + path for method, path in coverage.TARGETS})
        self.assertEqual([c.args[0] for c in emit.call_args_list], list(coverage.REQUIRED_CASES))
        self.assertEqual(counts['requests'], 5)
        self.assertFalse(result['formal_performance_acceptance'])
        self.assertFalse(result['formal_24h_acceptance'])
        for invocation in self.chain.sql.call_args_list:
            self.assertTrue(invocation.kwargs.get('read') or invocation.args[0].startswith('DELETE FROM v3_role_grants '))


if __name__ == '__main__': unittest.main()
