"""Offline topology authority/evidence counterexamples; no live dependencies."""
import copy
from contextlib import closing
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest
import uuid
from unittest.mock import Mock, patch
from urllib.parse import urlencode

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_topology_protocol_coverage as topology


def graph_fixture():
    return {'A': {'id': 'inventory-owned-a', 'name': 'Protocol A', 'external_id': 'uid-a',
                  'source_kind': 'docker', 'source_id': 'owned-source'},
            'B': {'id': 'inventory-owned-b', 'name': 'Protocol B', 'external_id': 'uid-b',
                  'source_kind': 'docker', 'source_id': 'owned-source'},
            'edge': {'id': 'owned-edge', 'revision': '2026-10-01T13:00:00.123456Z', 'kind': 'calls'}}


def view_fixture():
    return {'groups': [{'id': 'g:cluster:owned', 'label': 'owned', 'zone': 'protocol-zone',
                        'x': 20, 'y': 20, 'w': 200, 'h': 180}],
            'nodes': [{'id': 'inventory-owned-a', 'name': 'Protocol A', 'brand': 'docker', 'sub': 'host',
                       'status': 'ok', 'impacted': False, 'group': 'g:cluster:owned', 'x': 34, 'y': 52},
                      {'id': 'inventory-owned-b', 'name': 'Protocol B', 'brand': 'docker', 'sub': 'host',
                       'status': 'warn', 'impacted': True, 'group': 'g:cluster:owned', 'x': 34, 'y': 120}],
            'edges': [{'id': 'owned-edge', 'from': 'inventory-owned-a', 'to': 'inventory-owned-b',
                       'label': '25/s · 2.0%', 'tone': 'warn', 'fast': False, 'width': 2.9,
                       'path': 'M120 100 L120 120', 'lx': 142, 'ly': 110}],
            'blast': '1 observed impacted resources / 2 visible resources',
            'tree': [{'depth': 1, 'kind': 'host', 'name': 'Protocol A', 'status': 'ok', 'replicas': '—'},
                     {'depth': 1, 'kind': 'host', 'name': 'Protocol B', 'status': 'warn', 'replicas': '—'}],
            'sankey': {'cols': [[{'id': 'src:inventory-owned-a', 'label': 'Protocol A', 'color': 'var(--warn)', 'value': 25}],
                               [{'id': 'dst:inventory-owned-b', 'label': 'Protocol B', 'color': 'var(--warn)', 'value': 25}]],
                       'links': [{'from': 'src:inventory-owned-a', 'to': 'dst:inventory-owned-b',
                                  'color': 'var(--warn)', 'value': 25}]},
            'detail': {'id': 'inventory-owned-b', 'name': 'Protocol B', 'brand': 'docker', 'meta': 'host',
                       'conns': {'used': 3, 'max': 12}, 'from_service': {'used': 2, 'max': 8, 'service': 'Protocol A'},
                       'wait': [1, 2, 3, 4, 5, 6], 'health': [{'k': 'Status', 'v': 'warn'}],
                       'source': 'docker · owned-source', 'dependents': '0 visible explicit dependents', 'owner': 'Unassigned'}}


class SourceAndAdmissionTests(unittest.TestCase):
    def test_native_snapshot_scopes_every_node_and_keeps_directed_call_edge(self):
        nonce, app, env = uuid.uuid4().hex, uuid.uuid4().hex, uuid.uuid4().hex
        observed = '2026-10-01T13:00:00Z'
        value = topology.snapshot(nonce, app, env, observed)
        self.assertEqual(value, topology.snapshot(nonce, app, env, observed))
        self.assertEqual(len(value['sources']), 1)
        source = value['sources'][0]
        self.assertEqual(source['source_kind'], 'docker')
        self.assertEqual(source['environment_key'], 'test')
        self.assertEqual(source['observed_at'], observed)
        nodes = source['resources']
        self.assertEqual(len(nodes), 2)
        self.assertEqual(len({n['external_id'] for n in nodes}), 2)
        for node in nodes:
            self.assertEqual(node['kind'], 'host')
            self.assertEqual(node['observed_at'], observed)
            self.assertEqual(node['applications'], [{'application_id': app, 'environment_id': env}])
        self.assertEqual(source['relations'], [{'source_external_id': nodes[0]['external_id'],
                                               'target_external_id': nodes[1]['external_id'], 'kind': 'calls'}])
        other = topology.snapshot(uuid.uuid4().hex, app, env, observed)['sources'][0]
        self.assertNotEqual(other['source_id'], source['source_id'])
        self.assertTrue(set(n['external_id'] for n in nodes).isdisjoint(n['external_id'] for n in other['resources']))

    def test_resource_identity_changes_with_each_source_namespace_component(self):
        original = topology.resource_id('docker', 'owned-source', 'owned-node')
        self.assertEqual(original, 'inventory-' + hashlib.sha256(b'docker\0owned-source\0owned-node').hexdigest()[:40])
        self.assertEqual(original, topology.resource_id('docker', 'owned-source', 'owned-node'))
        for args in [('lima', 'owned-source', 'owned-node'), ('docker', 'foreign-source', 'owned-node'),
                     ('docker', 'owned-source', 'foreign-node')]:
            self.assertNotEqual(original, topology.resource_id(*args))

    def test_parallel_capacity_rejects_one_byte_below_each_budget(self):
        gib = 1024 ** 3
        vm = 4 * gib + 1152 * 1024 ** 2 + gib
        self.assertTrue(topology.resource_admission(vm, 2 * gib, 1)['passed'])
        for args in [(vm - 1, 2 * gib, 1), (vm, 2 * gib - 1, 1),
                     (vm, 2 * gib, 2), (vm, 2 * gib, 4), (vm, 2 * gib, 0)]:
            with self.subTest(args=args), self.assertRaises((topology.ro.Blocked, AssertionError)):
                topology.resource_admission(*args)


class GitLabRemainingReservationTests(unittest.TestCase):
    LIMIT = 4 * 1024 ** 3

    def identity(self):
        return {'Id': topology.GITLAB_CID, 'Image': topology.GITLAB_IMAGE,
                'Name': '/aiops-' + topology.OWNER + '-gitlab',
                'Config': {'Labels': {'aiops.testlab.owner': topology.OWNER}},
                'State': {'Running': True},
                'HostConfig': {'Memory': self.LIMIT, 'NanoCpus': 2_000_000_000}}

    def cgroup(self, anon=1024 ** 3):
        return {'anon': anon, 'memory_max': self.LIMIT, 'cpu_quota': 2}

    def test_only_current_resident_anonymous_bytes_reduce_exact_limit(self):
        for anon in (0, 1, self.LIMIT // 2, self.LIMIT - 1, self.LIMIT):
            with self.subTest(anon=anon):
                pending, evidence = topology.gitlab_remaining_reserve(self.identity(), self.cgroup(anon))
                self.assertEqual(pending, self.LIMIT - anon)
                self.assertEqual(evidence['anonymous_resident_bytes'], anon)
                self.assertEqual(evidence['remaining_limit_reserved_bytes'], self.LIMIT - anon)
                self.assertEqual(evidence['cid'], topology.GITLAB_CID)
                self.assertEqual(evidence['image'], topology.GITLAB_IMAGE)

    def test_foreign_or_stopped_container_never_reduces_reservation(self):
        mutations = [lambda x: x.update(Id='f' * 64), lambda x: x.update(Image='sha256:' + 'f' * 64),
                     lambda x: x.update(Name='/foreign-gitlab'),
                     lambda x: x['Config']['Labels'].update({'aiops.testlab.owner': 'foreign'}),
                     lambda x: x['Config']['Labels'].clear(),
                     lambda x: x['State'].update(Running=False),
                     lambda x: x['State'].update(Running=1),
                     lambda x: x['HostConfig'].update(Memory=self.LIMIT - 1),
                     lambda x: x['HostConfig'].update(Memory=self.LIMIT + 1),
                     lambda x: x['HostConfig'].update(Memory=0),
                     lambda x: x['HostConfig'].update(NanoCpus=1_000_000_000),
                     lambda x: x.pop('HostConfig')]
        for index, mutate in enumerate(mutations):
            info = self.identity()
            mutate(info)
            with self.subTest(index=index):
                pending, evidence = topology.gitlab_remaining_reserve(info, self.cgroup(self.LIMIT))
                self.assertEqual(pending, self.LIMIT)
                self.assertTrue(evidence['unattested_or_stopped'])

    def test_invalid_or_missing_cgroup_limits_never_reduce_reservation(self):
        values = [dict(self.cgroup(), memory_max=self.LIMIT - 1),
                  dict(self.cgroup(), memory_max=self.LIMIT + 1),
                  dict(self.cgroup(), memory_max='max'), dict(self.cgroup(), memory_max=str(self.LIMIT)),
                  dict(self.cgroup(), cpu_quota=1), dict(self.cgroup(), cpu_quota='2'),
                  dict(self.cgroup(), cpu_quota=None), {'anon': self.LIMIT}, {}]
        for cgroup in values:
            with self.subTest(cgroup=cgroup):
                pending, evidence = topology.gitlab_remaining_reserve(self.identity(), cgroup)
                self.assertEqual(pending, self.LIMIT)
                self.assertTrue(evidence['anonymous_or_limits_unattested'])

    def test_anonymous_bytes_require_bounded_integer_not_bool_string_float(self):
        for anon in (None, True, False, -1, self.LIMIT + 1, '1073741824', 1024.0, float('nan'), [], {}):
            with self.subTest(anon=anon):
                pending, evidence = topology.gitlab_remaining_reserve(self.identity(), self.cgroup(anon))
                self.assertEqual(pending, self.LIMIT)
                self.assertTrue(evidence['anonymous_or_limits_unattested'])

    def test_file_cache_and_total_current_usage_cannot_decrease_remaining_reserve(self):
        group = self.cgroup(1024 ** 3)
        expected, _ = topology.gitlab_remaining_reserve(self.identity(), group)
        for extra in ({'file': self.LIMIT}, {'cache': self.LIMIT}, {'memory.current': self.LIMIT},
                      {'memory_current': self.LIMIT}, {'file': self.LIMIT * 2, 'cache': self.LIMIT * 2,
                                                       'memory.current': self.LIMIT * 2}):
            with self.subTest(extra=extra):
                self.assertEqual(topology.gitlab_remaining_reserve(self.identity(), dict(group, **extra))[0], expected)
        no_anon = {'file': self.LIMIT, 'cache': self.LIMIT, 'memory.current': self.LIMIT,
                   'memory_max': self.LIMIT, 'cpu_quota': 2}
        self.assertEqual(topology.gitlab_remaining_reserve(self.identity(), no_anon)[0], self.LIMIT)

    def test_pending_reserve_changes_only_gitlab_slice_and_keeps_rule_and_safety_floor(self):
        floor = int(1.125 * 1024 ** 3) + 1024 ** 3
        for pending in (0, 1, self.LIMIT // 2, self.LIMIT):
            with self.subTest(pending=pending):
                result = topology.resource_admission(pending + floor, 2 * 1024 ** 3, 1, pending)
                self.assertTrue(result['passed'])
                self.assertEqual(result['pending_gitlab_reserve_bytes'], pending)
                self.assertEqual(result['rule_reserve_bytes'], int(1.125 * 1024 ** 3))
                self.assertEqual(result['reserve_bytes'], 1024 ** 3)
                with self.assertRaises(topology.ro.Blocked):
                    topology.resource_admission(pending + floor - 1, 2 * 1024 ** 3, 1, pending)
        self.assertEqual(topology.resource_admission(self.LIMIT + floor, 2 * 1024 ** 3, 1)
                         ['pending_gitlab_reserve_bytes'], self.LIMIT)

    def test_invalid_pending_never_relaxes_capacity_admission(self):
        for pending in (None, True, False, -1, self.LIMIT + 1, '0', 0.0, float('nan'), [], {}):
            with self.subTest(pending=pending), self.assertRaises(topology.ro.Blocked):
                topology.resource_admission(100 * self.LIMIT, 100 * self.LIMIT, 1, pending)


class ContractTests(unittest.TestCase):
    def test_full_topology_is_main_and_unrelated_read_write_routes_are_refused(self):
        contract = topology.Contract(topology.ROOT)
        self.assertEqual(contract.canonical('GET', '/api/v1/topology?around=owned&depth=1'),
                         ('/api/v1/topology', 'main'))
        for method, path in [('GET', '/api/v1/services'), ('GET', '/api/v1/home'),
                             ('POST', '/api/v1/promotions'), ('POST', '/api/v1/resources')]:
            with self.subTest(method=method, path=path), self.assertRaises(topology.ro.Blocked):
                contract.canonical(method, path)

    def test_url_escape_fragment_and_noncontract_method_are_rejected(self):
        contract = topology.Contract(topology.ROOT)
        for path in ['https://business.invalid/api/v1/topology', '//business.invalid/api/v1/topology',
                     '/api/v1/topology#foreign', '/legacy/topology']:
            with self.subTest(path=path), self.assertRaises(topology.ro.Blocked):
                contract.canonical('GET', path)
        with self.assertRaises(topology.ro.Blocked):
            contract.canonical('POST', '/api/v1/topology')


class ProjectionAndProtocolTests(unittest.TestCase):
    def test_complete_handwritten_view_satisfies_frozen_recursive_schema(self):
        schemas = topology.topology_schemas()
        topology.validate_schema(view_fixture(), schemas['V3Topology'], schemas)
        topology.validate_view(view_fixture(), graph_fixture())

    def test_missing_nested_frozen_required_fields_never_pass_full_dto(self):
        schemas = topology.topology_schemas()
        sections = [('groups', 'V3TopologyGroup'), ('nodes', 'V3TopologyNode'),
                    ('edges', 'V3TopologyEdge'), ('tree', 'V3TopologyTreeRow')]
        for section, schema_name in sections:
            for field in schemas[schema_name]['required']:
                value = view_fixture()
                value[section][0].pop(field)
                with self.subTest(section=section, field=field), self.assertRaises(AssertionError):
                    topology.validate_view(value, graph_fixture())
        for field in schemas['V3TopologyDetail']['required']:
            value = view_fixture()
            value['detail'].pop(field)
            with self.subTest(section='detail', field=field), self.assertRaises(AssertionError):
                topology.validate_view(value, graph_fixture())

    def test_nested_types_bounds_enums_extra_fields_and_nonfinite_numbers_are_rejected(self):
        mutations = [lambda x: x['groups'][0].update(w=True),
                     lambda x: x['groups'][0].update(unreviewed='field'),
                     lambda x: x['tree'][0].update(depth=-1),
                     lambda x: x['tree'][0].update(status='unobserved'),
                     lambda x: x['tree'][0].update(depth=1.0),
                     lambda x: x['detail'].update(owner=None),
                     lambda x: x['detail']['conns'].update(max=0),
                     lambda x: x['detail']['conns'].update(used=-1),
                     lambda x: x['detail']['from_service'].update(max=0),
                     lambda x: x['detail']['health'][0].pop('v'),
                     lambda x: x['detail']['wait'].__setitem__(0, float('nan')),
                     lambda x: x['edges'][0].update(width=float('inf')),
                     lambda x: x['nodes'][0].update(impacted=0)]
        for index, mutate in enumerate(mutations):
            value = view_fixture()
            mutate(value)
            with self.subTest(index=index), self.assertRaises(AssertionError):
                topology.validate_view(value, graph_fixture())
        for value in (-1, 2, True, 1.0):
            with self.subTest(value=value), self.assertRaises(AssertionError):
                topology.validate_schema(value, {'type': 'integer', 'minimum': 0, 'maximum': 1}, {})

    def test_sankey_column_node_ids_and_names_must_match_exact_owned_graph(self):
        for column in (0, 1):
            for field in ('id', 'label'):
                value = view_fixture()
                value['sankey']['cols'][column][0][field] = 'foreign'
                with self.subTest(column=column, field=field), self.assertRaises(AssertionError):
                    topology.validate_view(value, graph_fixture())

    def test_nonempty_full_dto_accepts_source_bound_fixture_and_rejects_contradictions(self):
        topology.validate_view(view_fixture(), graph_fixture())
        mutations = [lambda x: x.update(partial=True), lambda x: x.update(nodes=[]),
                     lambda x: x['nodes'].append(copy.deepcopy(x['nodes'][0])),
                     lambda x: x['nodes'][0].update(id='foreign'),
                     lambda x: x['nodes'][0].update(brand='kubernetes'),
                     lambda x: x['nodes'][1].update(impacted=False),
                     lambda x: x['nodes'][0].update(x=900),
                     lambda x: x['nodes'][0].update(group='foreign'),
                     lambda x: x['edges'][0].update(to='foreign'),
                     lambda x: x['edges'][0].update(label='0/s · 2.0%'),
                     lambda x: x.update(blast='0 observed impacted resources / 2 visible resources'),
                     lambda x: x.update(tree=[]),
                     lambda x: x['sankey']['links'][0].update(value=0),
                     lambda x: x['sankey']['links'][0].update(to='foreign'),
                     lambda x: x['detail']['from_service'].update(service='foreign'),
                     lambda x: x['detail']['conns'].update(used=999),
                     lambda x: x['detail'].update(wait=[], source='foreign')]
        for index, mutate in enumerate(mutations):
            value = view_fixture()
            mutate(value)
            with self.subTest(index=index), self.assertRaises((AssertionError, KeyError, topology.ro.Blocked)):
                topology.validate_view(value, graph_fixture())

    def test_matrix_is_exact_six_point_identity_and_revision_bound_source(self):
        graph = graph_fixture()
        result = topology.matrix(graph, 'owned-app', 1000, 1300, 60)
        self.assertEqual(result['status'], 'success')
        self.assertEqual(result['data']['resultType'], 'matrix')
        rows = result['data']['result']
        self.assertEqual(len(rows), 11)
        self.assertEqual(len({json.dumps(r['metric'], sort_keys=True) for r in rows}), 11)
        for row in rows:
            self.assertEqual(row['metric']['schema'], 'topology_v1')
            self.assertEqual(row['metric']['application_id'], 'owned-app')
            self.assertEqual(row['metric']['environment_id'], 'test')
            self.assertEqual([p[0] for p in row['values']], [1000, 1060, 1120, 1180, 1240, 1300])
            if row['metric']['field'].startswith('edge_'):
                self.assertEqual(row['metric']['relation_id'], graph['edge']['id'])
                self.assertEqual(row['metric']['relation_revision'], graph['edge']['revision'])
                self.assertEqual(row['metric']['from_resource_id'], graph['A']['id'])
                self.assertEqual(row['metric']['to_resource_id'], graph['B']['id'])
            else:
                node = graph['A'] if row['metric']['resource_id'] == graph['A']['id'] else graph['B']
                self.assertEqual(row['metric']['external_uid'], node['external_id'])
                self.assertEqual(row['metric']['source_id'], node['source_id'])
        for args in [(1000, 1300, 30), (1000, 1240, 60), (1000, 1360, 60)]:
            with self.assertRaises(AssertionError):
                topology.matrix(graph, 'owned-app', *args)
        with self.assertRaises(topology.ro.Blocked):
            topology.matrix(graph, 'owned-app', 1000, 1300, 60, 'unknown')

    def test_fault_variants_really_change_identity_grid_count_or_complete_response(self):
        graph = graph_fixture()
        normal = topology.matrix(graph, 'owned-app', 1000, 1300, 60)
        for mode in ('identity', 'cross_scope', 'missing', 'grid', 'revision', 'zero_rps',
                     'labels', 'duplicate', 'bounds', 'warning'):
            with self.subTest(mode=mode):
                result = topology.matrix(graph, 'owned-app', 1000, 1300, 60, mode)
                self.assertNotEqual(result, normal)
                self.assertEqual(topology.matrix(graph, 'owned-app', 1000, 1300, 60), normal)

    def test_protocol_only_exact_current_reviewed_query_without_network(self):
        chain = Mock(graph=graph_fixture(), app='owned-app', expression='owned_topology_v1{app="owned-app"}')
        protocol = topology.Protocol(chain)
        end = 1_759_324_800
        end -= end % 60
        def date(value):
            return dt.datetime.fromtimestamp(value, dt.timezone.utc).isoformat().replace('+00:00', 'Z')
        parameters = {'query': chain.expression, 'start': date(end - 300), 'end': date(end),
                      'step': '60', 'timeout': '5s', 'limit': '100'}
        path = '/api/v1/query_range?' + urlencode(parameters)
        with patch.object(topology.time, 'time', return_value=end):
            self.assertEqual(protocol.respond('/-/ready'), (200, {'ready': True}))
            self.assertEqual(protocol.respond(path)[0], 200)
            for bad in ['https://foreign.invalid' + path, path + '#foreign', path + '&query=foreign',
                        path.replace('step=60', 'step=30'), path + '&url=https://foreign.invalid',
                        '/api/v1/write?' + urlencode(parameters),
                        '/api/v1/query_range?' + urlencode(dict(parameters, query='foreign')),
                        '/api/v1/query_range?' + urlencode(dict(parameters, end=date(end - 600), start=date(end - 900)))]:
                with self.subTest(path=bad), self.assertRaises((AssertionError, topology.ro.Blocked)):
                    protocol.respond(bad)
            protocol.mode = '503'
            self.assertEqual(protocol.respond(path)[0], 503)
            for mode in ('graph_change', 'revoke_grant', 'revoke_session'):
                protocol.mode = mode
                self.assertEqual(protocol.respond(path)[0], 200)
                chain.inflight_fault.assert_called_with(mode)


class LifecycleAndEvidenceTests(unittest.TestCase):
    def test_bootstrap_and_full_api_ready_use_four_distinct_runner_case_names(self):
        import contextlib
        import io
        active = Mock(stage='bootstrap', work=Path('/unused-private'))
        rows = []
        with (patch.object(topology, 'ACTIVE', active), patch.object(topology, '_append', side_effect=lambda path, row: rows.append(row)),
              contextlib.redirect_stdout(io.StringIO()) as stream):
            for stage in ('bootstrap', 'full'):
                active.stage = stage
                for index in (0, 1):
                    topology.emit('readonly.api_ready.' + str(index), 'passed', {'index': index})
        expected = {'topology.api_ready.bootstrap.0', 'topology.api_ready.bootstrap.1',
                    'topology.api_ready.full.0', 'topology.api_ready.full.1'}
        self.assertEqual({row['name'] for row in rows}, expected)
        self.assertEqual(len(rows), 4)
        printed = [json.loads(line) for line in stream.getvalue().splitlines()]
        self.assertEqual(printed, rows)
        self.assertEqual(len({(row['module'], row['name']) for row in printed}), 4)
        self.assertTrue(all(row['status'] == 'passed' for row in rows))

    def test_inventory_is_offline_and_never_calls_doctor_or_subprocess(self):
        import contextlib
        import io
        with (patch.object(topology, 'doctor') as doctor, patch.object(topology.subprocess, 'run') as run,
              patch.object(topology.subprocess, 'check_output') as output, contextlib.redirect_stdout(io.StringIO()) as stream):
            self.assertEqual(topology.main(['--inventory']), 0)
        self.assertEqual(json.loads(stream.getvalue())['evidence_mode'], 'protocol_mock')
        doctor.assert_not_called()
        run.assert_not_called()
        output.assert_not_called()

    def test_api_not_exited_retains_database_without_pg_inspection_or_ddl(self):
        with tempfile.TemporaryDirectory() as directory:
            chain = topology.Chain.__new__(topology.Chain)
            chain.work, chain.protocol = Path(directory), Mock()
            proc = Mock()
            proc.poll.return_value = None
            chain.processes, chain.role_attempted = [proc], True
            with (patch.object(topology, 'free_ports'), patch.object(chain, 'pg') as pg,
                  patch.object(topology.ro, 'inspect_pg') as inspect,
                  self.assertRaises(topology.ro.Blocked)):
                chain.cleanup()
            pg.assert_not_called()
            inspect.assert_not_called()
            chain.protocol.close.assert_called_once()
            self.assertIsNotNone(proc.terminate.call_args)

    def active_fixture(self, root, script='tools/testlab/promotion_chain.py', child=None):
        path = root / '.local/testlab/private-manifest.json'
        path.parent.mkdir(parents=True)
        argv = ['python3', script]
        if child:
            argv.extend(['--run-id', child])
        path.write_text(json.dumps({'suites': [{'id': 'owned-suite', 'commands': [{'argv': argv}]}]}))
        path.chmod(0o600)
        run = {'id': str(uuid.uuid4()), 'status': 'running', 'pid': os.getppid(), 'selected_suite': 'owned-suite',
               'source_tree_sha': topology.SOURCE_SHA, 'manifest_sha': hashlib.sha256(path.read_bytes()).hexdigest()}
        self.ledger(root, run, {'kind': 'source', 'manifest': '.local/testlab/private-manifest.json'})
        return run, path

    def change_run(self, root, run, **changes):
        value = dict(run, **changes)
        with closing(sqlite3.connect(root / '.local/testlab/state.sqlite3')) as connection, connection:
            connection.execute('UPDATE runs SET data=? WHERE id=?', (json.dumps(value), run['id']))

    def test_exact_other_authorized_script_and_relative_manifest_are_admitted(self):
        for script in ('tools/testlab/promotion_chain.py', 'tools/testlab/api_rule_assistant_container_coverage.py'):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                run, path = self.active_fixture(root, script)
                before = (root / '.local/testlab/state.sqlite3').read_bytes()
                found = topology.active_runs(root)
                self.assertEqual(found[0]['run_id'], run['id'])
                self.assertEqual(found[0]['manifest'], str(path))
                self.assertEqual(found[0]['scripts'], [script])
                self.assertFalse(found[0]['current'])
                self.assertEqual((root / '.local/testlab/state.sqlite3').read_bytes(), before)

    def test_foreign_manifest_sha_selected_suite_source_script_and_mode_are_refused(self):
        for change in ({'manifest_sha': 'f' * 64}, {'selected_suite': 'foreign'},
                       {'selected_suite': 'owned-suite,foreign'}, {'source_tree_sha': 'f' * 64}):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                run, _ = self.active_fixture(root)
                self.change_run(root, run, **change)
                with self.subTest(change=change), self.assertRaises((AssertionError, topology.ro.Blocked)):
                    topology.active_runs(root)
        for kind in ('foreign_script', 'public_mode'):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                run, path = self.active_fixture(root, 'tools/testlab/foreign.py' if kind == 'foreign_script' else 'tools/testlab/promotion_chain.py')
                if kind == 'public_mode':
                    path.chmod(0o644)
                with self.subTest(kind=kind), self.assertRaises((AssertionError, topology.ro.Blocked)):
                    topology.active_runs(root)

    def test_current_child_requires_exact_run_argument_and_real_runner_parent(self):
        child = str(uuid.uuid4())
        for mutation in ('none', 'missing_allow', 'foreign_child', 'foreign_parent', 'duplicate'):
            with tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                run, path = self.active_fixture(root, 'tools/testlab/api_topology_protocol_coverage.py', child)
                allow = child
                if mutation == 'missing_allow':
                    allow = None
                elif mutation == 'foreign_child':
                    allow = str(uuid.uuid4())
                elif mutation == 'foreign_parent':
                    self.change_run(root, run, pid=os.getppid() + 99999)
                elif mutation == 'duplicate':
                    other = dict(run, id=str(uuid.uuid4()), pid=os.getppid() + 99999)
                    with closing(sqlite3.connect(root / '.local/testlab/state.sqlite3')) as connection, connection:
                        connection.execute('INSERT INTO runs VALUES(?,?)', (other['id'], json.dumps(other)))
                        connection.execute('INSERT INTO records VALUES(?,?,?,?)',
                                           (str(uuid.uuid4()), other['id'], 'environment', json.dumps({'kind': 'source', 'manifest': str(path)})))
                if mutation == 'none':
                    self.assertTrue(topology.active_runs(root, allow)[0]['current'])
                else:
                    with self.subTest(mutation=mutation), self.assertRaises((AssertionError, topology.ro.Blocked)):
                        topology.active_runs(root, allow)

    def proof(self):
        run_id = str(uuid.uuid4())
        cell = {'method': 'GET', 'path': '/api/v1/topology', 'scenario': 'normal', 'scenario_detail': 'normal',
                'status': 'passed', 'route_class': 'main', 'registration_evidence': True,
                'evidence_mode': 'protocol_mock', 'upstream_mode': 'protocol_mock',
                'run_id': run_id, 'call_id': run_id + '-1', 'actual': {'status': 200}}
        row = {'name': 'http', 'status': 'passed', 'evidence_mode': 'protocol_mock',
               'upstream_mode': 'protocol_mock', 'interface': cell}
        return run_id, row, copy.deepcopy(cell)

    def test_qualified_only_mock_proof_with_all_three_matching_receipts(self):
        run_id, row, receipt = self.proof()
        result = topology.qualified([row], [receipt], run_id, True, True)
        self.assertTrue(result['ready'])
        self.assertEqual(result['normal_http_proofs']['real_local'], {})
        self.assertEqual(result['normal_http_proofs']['protocol_mock'],
                         {'GET /api/v1/topology': [receipt['call_id']]})

    def test_each_of_three_scope_layers_must_be_explicitly_protocol_mock(self):
        for layer in ('case', 'interface', 'receipt'):
            for key in ('evidence_mode', 'upstream_mode'):
                for value in (None, 'real_local', 'unknown'):
                    run_id, row, receipt = self.proof()
                    target = row if layer == 'case' else row['interface'] if layer == 'interface' else receipt
                    if value is None:
                        target.pop(key)
                    else:
                        target[key] = value
                    with self.subTest(layer=layer, key=key, value=value):
                        result = topology.qualified([row], [receipt], run_id, True, True)
                        self.assertFalse(result['ready'])
                        self.assertEqual(result['normal_http_proofs']['real_local'], {})
                        self.assertEqual(result['normal_http_proofs']['protocol_mock'], {})

    def test_normal_proof_requires_own_registration_status_route_and_exact_receipt(self):
        mutations = [{'scenario': 'failure'}, {'scenario_detail': 'dependency_unavailable'},
                     {'method': 'POST'}, {'path': '/api/v1/services'}, {'route_class': 'auxiliary_or_historical'},
                     {'registration_evidence': False}, {'registration_evidence': 1}, {'status': 'blocked'},
                     {'actual': {'status': 503}}, {'actual': {'status': True}}, {'actual': {'status': '200'}},
                     {'actual': {'status': 201}}, {'actual': {'status': 204}},
                     {'run_id': str(uuid.uuid4())}, {'call_id': str(uuid.uuid4()) + '-1'}]
        for mutation in mutations:
            run_id, row, receipt = self.proof()
            row['interface'].update(mutation)
            receipt = copy.deepcopy(row['interface'])
            with self.subTest(mutation=mutation):
                self.assertFalse(topology.qualified([row], [receipt], run_id, True, True)['ready'])
        for receipts in ([], [dict(self.proof()[2], call_id='foreign')]):
            run_id, row, _ = self.proof()
            self.assertFalse(topology.qualified([row], receipts, run_id, True, True)['ready'])

    def test_duplicate_or_conflicting_persistent_receipts_are_not_unambiguous_proof(self):
        run_id, row, receipt = self.proof()
        for receipts in ([copy.deepcopy(receipt), receipt], [dict(receipt, evidence_mode='real_local'), receipt],
                         [dict(receipt, actual={'status': 503}), receipt]):
            with self.subTest(receipts=receipts):
                self.assertFalse(topology.qualified([row], receipts, run_id, True, True)['ready'])

    def test_nonpassed_cases_cleanup_or_missing_required_group_never_qualify(self):
        run_id, row, receipt = self.proof()
        for status in ('failed', 'interrupted', 'blocked', 'skipped', 'queued', 'running',
                       'not_implemented', 'external_todo', None):
            with self.subTest(status=status):
                result = topology.qualified([row, {'name': 'fault', 'status': status}], [receipt], run_id, True, True)
                self.assertFalse(result['ready'])
                self.assertEqual(result['normal_http_proofs'], {'real_local': {}, 'protocol_mock': {}})
        self.assertFalse(topology.qualified([row], [receipt], run_id, True, False)['ready'])
        self.assertFalse(topology.qualified([row], [receipt], run_id, False, True)['ready'])

    def ledger(self, root, run, source_record):
        database = root / '.local/testlab/state.sqlite3'
        database.parent.mkdir(parents=True, exist_ok=True)
        with closing(sqlite3.connect(database)) as connection, connection:
            connection.executescript('CREATE TABLE runs(id TEXT PRIMARY KEY,data TEXT);'
                                     'CREATE TABLE records(id TEXT PRIMARY KEY,run_id TEXT,kind TEXT,data TEXT);')
            connection.execute('INSERT INTO runs VALUES(?,?)', (run['id'], json.dumps(run)))
            connection.execute('INSERT INTO records VALUES(?,?,?,?)',
                               (str(uuid.uuid4()), run['id'], 'environment', json.dumps(source_record)))
        database.chmod(0o600)

    def test_unknown_active_manifest_is_not_concurrent_authorization(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            run_id = str(uuid.uuid4())
            manifest = root / 'unknown.json'
            manifest.write_text(json.dumps({'suites': [{'id': 'foreign-load', 'commands': [
                {'argv': ['python3', 'tools/testlab/foreign.py'], 'parser': 'testlab_json'}]}]}))
            run = {'id': run_id, 'status': 'running', 'pid': os.getpid(),
                   'selected_suite': 'foreign-load', 'manifest_sha': hashlib.sha256(manifest.read_bytes()).hexdigest()}
            self.ledger(root, run, {'kind': 'source', 'manifest': 'unknown.json'})
            with self.assertRaises((topology.ro.Blocked, AssertionError)):
                topology.active_runs(root)

    def test_allow_current_uuid_cannot_silently_adopt_own_duplicate_run(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            run_id = str(uuid.uuid4())
            run = {'id': run_id, 'status': 'running', 'pid': os.getpid(),
                   'selected_suite': 'topology-protocol', 'manifest_sha': 'a' * 64}
            self.ledger(root, run, {'kind': 'source', 'manifest': 'missing.json'})
            with self.assertRaises((topology.ro.Blocked, AssertionError)):
                topology.active_runs(root, allow_current_uuid=run_id)

    def test_missing_business_receipts_never_qualify_even_with_cleanup(self):
        run_id = str(uuid.uuid4())
        for required, cleanup in [(False, False), (True, False), (False, True), (True, True)]:
            result = topology.qualified([], [], run_id, required, cleanup)
            self.assertFalse(result['ready'])
            self.assertEqual(result['normal_http_proofs'], {'real_local': {}, 'protocol_mock': {}})

    def test_parent_cleanup_gets_remaining_budget_without_mutating_global_time(self):
        import time
        original = topology.ro.time
        actual_monotonic = time.monotonic
        deadline = actual_monotonic() + 20
        proxy = topology.cleanup_clock(deadline)
        with patch.object(topology.ro, 'time', proxy):
            self.assertIsNot(proxy, original)
            # Existing parent computes monotonic()+40 once at entry.
            self.assertAlmostEqual(proxy.monotonic() + 40, deadline, places=5)
            self.assertAlmostEqual(proxy.monotonic(), actual_monotonic(), delta=.1)
            self.assertIs(time.monotonic, actual_monotonic)
        self.assertIs(topology.ro.time, original)

    def test_parent_cleanup_clock_restores_module_reference_after_failure(self):
        import time
        original = topology.ro.time
        with self.assertRaisesRegex(RuntimeError, 'owned fault'):
            with patch.object(topology.ro, 'time', topology.cleanup_clock(time.monotonic() + 10)):
                raise RuntimeError('owned fault')
        self.assertIs(topology.ro.time, original)


if __name__ == '__main__':
    unittest.main()
