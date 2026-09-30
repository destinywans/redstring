"""Offline WAN-160 protection/protocol tests. No Docker, PG or API starts."""
import contextlib
import copy
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_management_chain as management
from inventory import actual_interface


class ManagementProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.binary = self.root / '.local/testlab/environments' / management.OWNER / 'backend/aiops'
        self.binary.parent.mkdir(parents=True)
        self.binary.write_bytes(b'formal nonrace owned binary')
        self.data = dict(owner=management.OWNER, binary=str(self.binary), source_tree_sha='frozen-source',
            binary_sha256=management.hashlib.sha256(self.binary.read_bytes()).hexdigest(),
            postgres_container='aiops-' + management.OWNER + '-pg', postgres_image_id='sha256:pinned',
            postgres_client_addresses=['127.0.0.1', '::1', '172.17.0.1'])
        self.contract = management.Contract(management.ROOT)
        with patch.object(management, 'Contract', return_value=self.contract):
            self.chain = management.Chain(self.root, self.data, str(uuid.uuid4()))

    def pg_info(self):
        return dict(Name='/aiops-' + management.OWNER + '-pg', Image='sha256:pinned',
            Config=dict(Image=management.PG_IMAGE, Labels={'aiops.testlab.owner': management.OWNER}),
            State={'Running': True}, NetworkSettings={'Ports': {'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '15461'}]}})

    def resources(self, **changes):
        return dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
            database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0,
            connection_details=[], **changes)

    def pg_context(self, result=None):
        result = result or Mock(returncode=0, stdout='1', stderr='')
        stack = contextlib.ExitStack()
        stack.enter_context(patch.object(management, 'inspect_pg', return_value=self.pg_info()))
        run = stack.enter_context(patch.object(management.subprocess, 'run', return_value=result))
        return stack, run

    def test_inventory_has_17_targets_20_total_current_contract_and_no_acceptance_claim(self):
        with patch.object(management, 'doctor') as doctor, patch.object(management.subprocess, 'run') as run:
            result = management.inventory(management.ROOT)
        self.assertEqual(result['selected_route_count'], 17)
        self.assertEqual(result['total_operations_with_support'], 20)
        self.assertEqual(sum(x['route_class'] == 'main' for x in result['targets']), 15)
        self.assertEqual(sum(x['planned_evidence'] == 'dependency_failure_only' for x in result['targets']), 2)
        self.assertIn('no new positive acceptance', result['proof'])
        self.assertEqual(result['ports'], [18606, 18607])
        doctor.assert_not_called()
        run.assert_not_called()

    def test_cli_inventory_never_reads_private_environment_or_runs_live(self):
        with patch.object(management, 'doctor') as doctor, patch.object(management, 'execute') as execute, \
             patch.object(management, 'private_json') as private, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(management.main(['--inventory']), 0)
        self.assertEqual(json.loads(output.getvalue())['selected_route_count'], 17)
        doctor.assert_not_called()
        execute.assert_not_called()
        private.assert_not_called()

    def test_cli_requires_explicit_mode_before_any_environment_operation(self):
        with patch.object(management, 'doctor') as doctor, contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            management.main([])
        doctor.assert_not_called()

    def test_exact_pg_name_owner_pinned_image_and_loopback_required(self):
        info = self.pg_info()
        management.guard_pg(info, management.OWNER, 'sha256:pinned')
        variants = [dict(info, Name='/shared'), dict(info, Image='sha256:changed'),
            dict(info, State={'Running': False}),
            dict(info, Config=dict(Image='postgres:latest', Labels=info['Config']['Labels'])),
            dict(info, Config=dict(Image=management.PG_IMAGE, Labels={'aiops.testlab.owner': 'foreign'}))]
        for bindings in ([{'HostIp': '0.0.0.0', 'HostPort': '15461'}], [{'HostIp': '127.0.0.1', 'HostPort': '5432'}],
                         [{'HostIp': '127.0.0.1', 'HostPort': '15461'}, {'HostIp': '::1', 'HostPort': '15461'}]):
            variants.append(dict(info, NetworkSettings={'Ports': {'5432/tcp': bindings}}))
        for item in variants:
            with self.subTest(item=item), self.assertRaises(management.Blocked):
                management.guard_pg(item, management.OWNER, 'sha256:pinned')

    def test_nonce_hmac_replay_and_wrong_live_identity_refused(self):
        data = dict(self.data, control_secret='generated-nonbusiness-secret', supervisor_pid=500)
        current = dict(owner=management.OWNER, pid=500, api_pids=[501, 502], ready=True, source_tree_sha='frozen-source')
        nonce = 'a' * 32
        receipt = dict(nonce=nonce, identity=current, proof=management.control_proof(data['control_secret'], nonce, current))
        self.assertEqual(management.signed_identity(data, nonce, receipt), current)
        for row in (dict(receipt, nonce='b' * 32), dict(receipt, proof='forged'), dict(receipt, extra=True)):
            with self.assertRaises(management.Blocked):
                management.signed_identity(data, nonce, row)
        for change in ({'ready': False}, {'owner': 'foreign'}, {'api_pids': [501, 501]}, {'source_tree_sha': 'changed'}):
            wrong = dict(current, **change)
            row = dict(nonce=nonce, identity=wrong, proof=management.control_proof(data['control_secret'], nonce, wrong))
            with self.assertRaises(management.Blocked):
                management.signed_identity(data, nonce, row)

    def test_transport_refuses_foreign_authority_old_ports_and_controller_writes(self):
        cases = [('https://business.invalid', 'GET', '/api/v1/workspace'),
                 ('http://127.0.0.1:18602', 'GET', '/api/v1/workspace'),
                 (management.BASES[0], 'GET', '//business.invalid/private'),
                 (management.BASES[0], 'GET', '/api/v1/me#secret'),
                 (management.BASES[0], 'GET', '/api/v1/me\r\nAuthorization: secret'),
                 (management.CONTROL, 'POST', '/shutdown'), (management.CONTROL, 'GET', '/identity')]
        for base, method, path in cases:
            with patch.object(management.http.client, 'HTTPConnection') as connect, self.assertRaises(management.Blocked):
                management.transport(base, method, path)
            connect.assert_not_called()

    def test_transport_response_limit_always_closes_connection(self):
        connection, response = Mock(), Mock(status=200)
        response.read.return_value = b'x' * (management.LIMIT + 1)
        connection.getresponse.return_value = response
        with patch.object(management.http.client, 'HTTPConnection', return_value=connection), self.assertRaises(AssertionError):
            management.transport(management.BASES[0], 'GET', '/api/v1/workspace')
        connection.close.assert_called_once()
        response.read.assert_called_once_with(management.LIMIT + 1)

    def test_fresh_uuid_paths_private_credentials_and_no_secret_in_public_state(self):
        self.assertTrue(self.chain.database.startswith('aiops_testlab_management_'))
        self.assertEqual(self.chain.state['ports'], [18606, 18607])
        for name in ('state.json', 'credentials.json'):
            self.assertEqual((self.chain.work / name).stat().st_mode & 0o777, 0o600)
        state = (self.chain.work / 'state.json').read_text()
        self.assertNotIn(self.chain.password, state)
        self.assertNotIn(self.chain.db_password, state)
        with self.assertRaises((management.Blocked, ValueError)):
            management.Chain(self.root, self.data, 'old-db-alias')
        with self.assertRaises(FileExistsError):
            management.Chain(self.root, self.data, self.chain.run_id)

    def test_source_binary_unknown_changed_and_symlink_refused(self):
        with patch.object(management, 'source_fingerprint', return_value='frozen-source'):
            self.chain.frozen()
        for value in ('unknown', '', 'changed'):
            with patch.object(management, 'source_fingerprint', return_value=value), self.assertRaises(management.Blocked):
                self.chain.frozen()
        self.binary.write_bytes(b'changed binary')
        with patch.object(management, 'source_fingerprint', return_value='frozen-source'), self.assertRaises(management.Blocked):
            self.chain.frozen()

    def test_formal_nonrace_build_metadata_required(self):
        correct = 'aiops: go1.26\n\tpath\tgithub.com/wans/aiops-canvas/cmd/aiops\n'
        with patch.object(management.subprocess, 'run', return_value=Mock(returncode=0, stdout=correct)):
            self.assertFalse(management.binary_build_info(self.binary)['race_enabled'])
        for output in ('', correct.replace('/cmd/aiops', '/cmd/foreign'), correct + '\tbuild\t-race=true\n'):
            with patch.object(management.subprocess, 'run', return_value=Mock(returncode=0, stdout=output)), self.assertRaises(management.Blocked):
                management.binary_build_info(self.binary)

    def test_doctor_fresh_challenge_validates_every_registered_listener_and_discards_admin(self):
        original_bases = ['http://127.0.0.1:18561', 'http://127.0.0.1:18562']
        dsn = 'postgres://testlab:synthetic-only@127.0.0.1:15461/aiops_testlab_app'
        owned_work = self.binary.parent.parent
        fixture = dict(self.data, work=str(self.binary.parent), database_url=dsn, base_urls=original_bases,
            environment='test', other_environment='staging', control_secret='synthetic-controller-secret',
            supervisor_pid=500, admin={'email': 'existing-synthetic-account', 'password': 'never-reused'})
        environment = dict(owner=management.OWNER, work=str(owned_work), container=self.data['postgres_container'],
                           env={'TESTLAB_APP_DSN': dsn})
        management._private(self.root / '.local/testlab/backend-fixture.json', fixture)
        management._private(self.root / '.local/testlab/environment.json', environment)
        def challenge(base, method, path, timeout):
            self.assertEqual((base, method), (management.CONTROL, 'GET'))
            nonce = path.split('nonce=', 1)[1]
            identity = dict(owner=management.OWNER, pid=500, api_pids=[501, 502], ready=True, source_tree_sha='frozen-source')
            receipt = dict(nonce=nonce, identity=identity,
                           proof=management.control_proof(fixture['control_secret'], nonce, identity))
            return 200, json.dumps(receipt).encode(), {}
        socket = Mock()
        socket.__enter__ = Mock(return_value=Mock(connect_ex=Mock(return_value=1)))
        socket.__exit__ = Mock(return_value=False)
        with patch.object(management, 'source_fingerprint', return_value='frozen-source'), \
             patch.object(management, 'binary_build_info', return_value={'race_enabled': False}), \
             patch.object(management, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(management, 'transport', side_effect=challenge) as transport, \
             patch.object(management, '_process_port', return_value=True) as process_port, \
             patch.object(management.socket, 'socket', return_value=socket):
            first = management.doctor(self.root)
            management.doctor(self.root)
        self.assertNotIn('admin', first)
        self.assertEqual([row.args for row in process_port.call_args_list[:3]], [(500, 18560), (501, 18561), (502, 18562)])
        self.assertNotEqual(transport.call_args_list[0].args[2], transport.call_args_list[1].args[2])

    def test_failed_nested_success_dto_preserves_actual_200_as_failed_private_first_red(self):
        response = b'{"quiet":{"enabled":true,"from":"22:00"},"digest":"daily","subscriptions":[]}'
        with patch.object(management, 'transport', return_value=(200, response, {})), \
             contextlib.redirect_stdout(io.StringIO()) as output, self.assertRaises(AssertionError):
            self.chain.call('reader', 'GET', '/api/v1/me/notification-settings')
        public = json.loads(output.getvalue())
        self.assertEqual((public['status'], public['actual']['status']), ('failed', 200))
        self.assertFalse(public['interface']['registration_evidence'])
        self.assertEqual((self.chain.work / 'first-failure.json').stat().st_mode & 0o777, 0o600)

    def test_sql_scope_container_guard_before_exec(self):
        for database, user in (('aiops_testlab_app', 'testlab'), (self.chain.database, 'business')):
            with patch.object(management.subprocess, 'run') as run, self.assertRaises(management.Blocked):
                self.chain.pg('SELECT 1;', database=database, user=user)
            run.assert_not_called()
        with patch.object(management, 'inspect_pg', return_value=dict(self.pg_info(), Name='/foreign')), \
             patch.object(management.subprocess, 'run') as run, self.assertRaises(management.Blocked):
            self.chain.sql('SELECT 1')
        run.assert_not_called()

    def test_sql_stdin_readonly_scope_catalog_markers_and_app_name(self):
        stack, run = self.pg_context()
        with stack:
            self.assertEqual(self.chain.sql('SELECT 1', read=True), '1')
        statement = run.call_args.kwargs['input']
        self.assertIn('BEGIN READ ONLY;', statement)
        self.assertIn('current_database()', statement)
        self.assertIn('current_user', statement)
        self.assertIn("'pg_database'", statement)
        self.assertIn("'pg_authid'", statement)
        self.assertIn(self.chain.owner_tag, statement)
        self.assertIn('PGAPPNAME=' + self.chain.pg_application('sql'), run.call_args.args[0])
        self.assertNotIn(self.chain.db_password, ' '.join(run.call_args.args[0]))

    def test_sql_timeout_records_private_first_evidence_and_never_retries(self):
        stack, run = self.pg_context()
        run.side_effect = subprocess.TimeoutExpired('docker', 12)
        with stack, self.assertRaises(management.SQLTransportTimeout):
            self.chain.sql('SELECT 1', read=True)
        self.assertEqual(run.call_count, 1)
        evidence = self.chain.work / 'first-sql-timeout.json'
        self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)

    def test_sql_error_does_not_print_credentials(self):
        stack, _ = self.pg_context(Mock(returncode=1, stdout='', stderr='private ' + self.chain.db_password))
        with stack, self.assertRaises(management.Blocked) as error:
            self.chain.sql('SELECT 1')
        self.assertNotIn(self.chain.db_password, str(error.exception))
        paths = list(self.chain.work.glob('sql-error-*.json'))
        self.assertEqual(paths[0].stat().st_mode & 0o777, 0o600)

    def test_provision_refuses_preexisting_database_without_adopting_it(self):
        with patch.object(self.chain, 'frozen'), patch.object(self.chain, 'pg', return_value='{"role":false,"database":true}') as pg, \
             self.assertRaises(management.Blocked):
            self.chain.provision()
        self.assertEqual(pg.call_count, 1)
        self.assertFalse(self.chain.db_attempted)

    def test_provision_owned_nonsuperuser_markers_and_no_business_seeding(self):
        with patch.object(self.chain, 'frozen'), patch.object(self.chain, 'pg', side_effect=['{"role":false,"database":false}', '', '', '', '']) as pg, \
             patch.object(management, 'emit'):
            self.chain.provision()
        sql = [row.args[0] for row in pg.call_args_list]
        self.assertIn('NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION', sql[1])
        self.assertIn(self.chain.owner_tag, sql[1])
        self.assertIn('OWNER "' + self.chain.db_user + '" TEMPLATE template0', sql[2])
        self.assertIn('REVOKE CONNECT,TEMPORARY', sql[3])
        self.assertNotIn('INSERT INTO management_audit', '\n'.join(sql))

    def test_seed_only_users_roles_and_minimum_grants_not_business_or_audits(self):
        with patch.object(self.chain, 'sql', side_effect=['', '{"users":5,"business_rows":0,"manager_grants":0}']) as sql, \
             patch.object(management, 'emit'):
            self.chain.seed()
        source = sql.call_args_list[0].args[0]
        self.assertIn('INSERT INTO users', source)
        self.assertIn('INSERT INTO v3_role_grants', source)
        for table in ('applications', 'v3_user_settings', 'management_audit', 'sessions', 'v3_user_watched_services'):
            self.assertNotIn('INSERT INTO ' + table, source)
        self.assertTrue(sql.call_args_list[1].kwargs['read'])

    def test_contract_canonical_selected_patterns_and_rejects_scope_creep(self):
        route, classification = self.contract.canonical('GET', '/api/v1/services/opaque-id/peek')
        self.assertEqual((route, classification), ('/api/v1/services/{id}/peek', 'main'))
        for method, path in (('POST', '/api/v1/conversations'), ('GET', '/api/v1/models'), ('GET', '/private')):
            with self.assertRaises(management.Blocked):
                self.contract.canonical(method, path)

    def test_dto_nested_required_types_nullable_enum_and_204(self):
        schema = dict(type='object', required=['child'], properties={'child': dict(type='object', required=['n'],
            properties={'n': {'type': 'integer'}, 'state': {'type': 'string', 'enum': ['saved'], 'nullable': True}})})
        self.contract.validate(schema, {'child': {'n': 1, 'state': None}})
        for value in ({}, {'child': {}}, {'child': {'n': True}}, {'child': {'n': 1, 'state': 'healthy'}}):
            with self.assertRaises(AssertionError):
                self.contract.validate(schema, value)
        self.contract.shape('DELETE', '/api/v1/me/sessions/opaque', 204, None)
        with self.assertRaises(AssertionError):
            self.contract.shape('DELETE', '/api/v1/me/sessions/opaque', 204, {})
        with self.assertRaises(AssertionError):
            self.contract.shape('GET', '/api/v1/workspace', 201, {})

    def test_revision_headers_reject_weak_noncanonical_empty_multiple(self):
        self.assertEqual(management.revision({'etag': '"12"'}, 'ETag', True), 12)
        self.assertEqual(management.revision({'X-Role-Matrix-Revision': '3'}, 'X-Role-Matrix-Revision'), 3)
        for bad in ('', '0', '01', '-1', '*', '1,2'):
            with self.assertRaises(AssertionError):
                management.revision({'revision': bad}, 'revision')

    def test_actual_protocol_preserves_503_as_failure_not_positive(self):
        response = {'error': {'code': 'identity_evidence_unavailable', 'message': 'not configured'}}
        with patch.object(management, 'transport', return_value=(503, json.dumps(response).encode(), {})), \
             contextlib.redirect_stdout(io.StringIO()) as output:
            self.chain.call('reader', 'GET', '/api/v1/me/profile', want=503,
                code='identity_evidence_unavailable', scenario='failure', purpose='dependency_unavailable')
        row = json.loads(output.getvalue())['interface']
        self.assertEqual(actual_interface(row)['scenarios']['failure']['actual_status'], 503)
        self.assertFalse(row['registration_evidence'])
        self.assertFalse(actual_interface(row)['registration_evidence'])

    def test_no_http_response_is_not_fabricated_as_status_zero_or_503(self):
        with patch.object(management, 'transport', side_effect=OSError('network error with private details')), \
             contextlib.redirect_stdout(io.StringIO()) as output, self.assertRaises(AssertionError):
            self.chain.call('reader', 'GET', '/api/v1/me/notification-settings')
        public = json.loads(output.getvalue())
        self.assertIsNone(public['actual']['status'])
        self.assertNotIn('interface', public)
        self.assertNotIn('private details', output.getvalue())
        row = json.loads((self.chain.work / 'interfaces.jsonl').read_text())
        self.assertFalse(row['registration_evidence'])

    def test_204_receipt_empty_and_native_route_canonical(self):
        with patch.object(management, 'transport', return_value=(204, b'', {})), contextlib.redirect_stdout(io.StringIO()) as output:
            value, _ = self.chain.call('reader', 'DELETE', '/api/v1/me/sessions/' + 'a' * 64, want=204)
        self.assertIsNone(value)
        row = json.loads(output.getvalue())['interface']
        self.assertEqual(row['path'], '/api/v1/me/sessions/{id}')
        self.assertEqual(actual_interface(row)['scenarios']['normal']['actual_status'], 204)

    def test_noncanonical_scenarios_or_extra_headers_rejected_before_http(self):
        for options in ({'scenario': 'fixture'}, {'scenario': 'concurrency'}, {'headers': {'Authorization': 'foreign-secret'}}):
            with patch.object(management, 'transport') as transport, self.assertRaises(management.Blocked):
                self.chain.call('reader', 'GET', '/api/v1/me/notification-settings', **options)
            transport.assert_not_called()

    def test_concurrent_interface_call_ids_unique_and_corresponding_error_kept(self):
        def request(base, method, path, body, headers):
            if base == management.BASES[0]:
                return 200, b'{"ok":true}', {'X-Role-Matrix-Revision': '2'}
            return 409, b'{"error":{"code":"role_changed","message":"stale"}}', {}
        with patch.object(management, 'transport', side_effect=request), contextlib.redirect_stdout(io.StringIO()) as output:
            pair = management.concurrent_pair(lambda index: self.chain.call('admin', 'PUT', '/api/v1/roles/' + self.chain.roles['target'],
                {'matrix': []}, conflict=(409, 'role_changed'), index=index, scenario='idempotency', purpose='concurrency'))
        self.assertEqual(len(pair), 2)
        rows = [json.loads(line)['interface'] for line in output.getvalue().splitlines()]
        self.assertEqual(len({row['call_id'] for row in rows}), 2)
        self.assertEqual({row['actual']['status'] for row in rows}, {200, 409})
        for row in rows:
            self.assertIn('idempotency', actual_interface(row)['scenarios'])
            self.assertFalse(row['registration_evidence'])

    def test_invalid_conflict_error_fails_instead_of_accepting_any_409(self):
        response = b'{"error":{"code":"other_conflict","message":"wrong cause"}}'
        with patch.object(management, 'transport', return_value=(409, response, {})), \
             contextlib.redirect_stdout(io.StringIO()), self.assertRaises(AssertionError):
            self.chain.call('admin', 'PUT', '/api/v1/roles/' + self.chain.roles['target'], {'matrix': []},
                conflict=(409, 'role_changed'), scenario='idempotency', purpose='concurrency')

    def test_profile_failures_have_no_sql_success_seed_or_positive_claim(self):
        with patch.object(self.chain, 'call') as call, patch.object(self.chain, 'audit_count') as audit, patch.object(management, 'emit'):
            self.chain.unavailable_profile()
        self.assertEqual(call.call_count, 2)
        self.assertEqual([row.args[1] for row in call.call_args_list], ['GET', 'PUT'])
        for row in call.call_args_list:
            self.assertEqual((row.kwargs['want'], row.kwargs['scenario']), (503, 'failure'))
        audit.assert_called_once_with('v3.profile.updated', 0)

    def test_connection_origin_needs_exact_role_app_name_and_client(self):
        valid = dict(pid=400, datname=self.chain.database, usename=self.chain.db_user, backend_type='client backend',
            backend_start='2026-10-01T00:00:00Z', application_name=self.chain.pg_application('api', 0),
            client_addr='172.17.0.1', client_port=44000)
        self.assertEqual(self.chain.connection_origin(valid), 'owned_api')
        for change in ({'usename': 'testlab'}, {'application_name': 'unknown'}, {'client_addr': '8.8.8.8'}, {'datname': 'shared'}):
            with self.assertRaises(management.Blocked):
                self.chain.connection_origin(dict(valid, **change))

    def test_autovacuum_only_exact_internal_identity_in_owned_database(self):
        valid = dict(pid=22303, datname=self.chain.database, usename=None, backend_type='autovacuum worker',
            backend_start='2026-10-01T00:00:00Z', application_name='', client_addr=None, client_port=None)
        self.assertEqual(self.chain.connection_origin(valid), 'postgres_background')
        for change in ({'datname': 'shared'}, {'usename': self.chain.db_user}, {'application_name': 'management'},
                       {'client_addr': '127.0.0.1'}, {'client_port': -1}, {'backend_type': 'parallel worker'}):
            with self.subTest(change=change), self.assertRaises(management.Blocked):
                self.chain.connection_origin(dict(valid, **change))

    def test_autovacuum_readonly_wait_separates_background_pids_and_never_signals(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        worker = dict(pid=22303, datname=self.chain.database, usename=None, backend_type='autovacuum worker',
            backend_start='2026-10-01T00:00:00Z', application_name='', client_addr=None, client_port=None)
        state = dict(self.resources(), connections=1, connection_details=[worker])
        replies = [json.dumps(state), json.dumps(self.resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(management.time, 'sleep'), \
             patch.object(management.os, 'kill') as kill, patch.object(management, 'emit'):
            self.chain.cleanup()
        kill.assert_not_called()
        self.assertNotIn('pg_terminate_backend', '\n'.join(row.args[0] for row in pg.call_args_list))
        quiescence = json.loads((self.chain.work / 'cleanup.json').read_text())['connection_quiescence']
        self.assertEqual(quiescence['owned_pg_backend_pids'], [])
        self.assertEqual(quiescence['postgres_background_backend_pids'], [22303])
        snapshot = json.loads((self.chain.work / 'first-connection-snapshot.json').read_text())
        self.assertEqual(snapshot['details'][0]['proven_origin'], 'postgres_background')

    def test_foreign_backend_saved_privately_before_cleanup_refuses_wait_or_drop(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        worker = dict(pid=22303, datname=self.chain.database, usename='foreign', backend_type='client backend',
            backend_start='2026-10-01T00:00:00Z', application_name='unknown', client_addr=None, client_port=-1)
        state = dict(self.resources(), connections=1, connection_details=[worker])
        with patch.object(self.chain, 'pg', return_value=json.dumps(state)) as pg, \
             patch.object(management.time, 'sleep') as sleep, self.assertRaises(management.Blocked):
            self.chain.cleanup()
        self.assertEqual(pg.call_count, 1)
        sleep.assert_not_called()
        snapshot_file = self.chain.work / 'first-connection-snapshot.json'
        self.assertEqual(snapshot_file.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(snapshot_file.read_text())['details'][0]['proven_origin'], 'unproven')

    def test_cleanup_exact_catalog_markers_no_forced_database_termination(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        replies = [json.dumps(self.resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(management, 'emit'):
            self.chain.cleanup()
        statements = '\n'.join(row.args[0] for row in pg.call_args_list)
        self.assertIn('DROP DATABASE "' + self.chain.database + '";', statements)
        self.assertIn('DROP ROLE "' + self.chain.db_user + '";', statements)
        self.assertNotIn('FORCE', statements)
        self.assertNotIn('pg_terminate_backend', statements)
        for row in pg.call_args_list:
            self.assertEqual(row.kwargs['timeout'], 12)
            self.assertIsNotNone(row.kwargs['deadline'])
        self.assertTrue(json.loads((self.chain.work / 'cleanup.json').read_text())['database_absent'])

    def test_cleanup_refuses_missing_or_foreign_tags_before_drop(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        for change in ({'database_tag': None}, {'database_tag': 'foreign'}, {'role': 'foreign'}, {'database_owner': 'testlab'}):
            state = dict(self.resources(), **change)
            with patch.object(self.chain, 'pg', return_value=json.dumps(state)) as pg, self.assertRaises(management.Blocked):
                self.chain.cleanup()
            self.assertEqual(pg.call_count, 1)

    def test_drop_timeout_reconciles_exact_absence_once_without_replaying(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        absent = dict(self.resources(), database_exists=False, database_owner=None, database_tag=None)
        replies = [json.dumps(self.resources()), management.SQLTransportTimeout('drop deadline'), json.dumps(absent), '',
                   '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(management, 'emit'):
            self.chain.cleanup()
        self.assertEqual(sum(row.args[0].startswith('DROP DATABASE') for row in pg.call_args_list), 1)
        self.assertEqual(sum(row.args[0].startswith('DROP ROLE') for row in pg.call_args_list), 1)
        self.assertTrue(json.loads((self.chain.work / 'drop_database-timeout-readback.json').read_text())['resource_absent'])

    def test_drop_timeout_with_resource_remaining_blocks_without_retry(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        replies = [json.dumps(self.resources()), management.SQLTransportTimeout('drop deadline'), json.dumps(self.resources())]
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, self.assertRaises(management.Blocked):
            self.chain.cleanup()
        self.assertEqual(sum(row.args[0].startswith('DROP DATABASE') for row in pg.call_args_list), 1)
        self.assertEqual(sum(row.args[0].startswith('DROP ROLE') for row in pg.call_args_list), 0)

    def test_cleanup_diagnostic_io_failure_cannot_skip_both_drops(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        replies = [json.dumps(self.resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(management, '_private', side_effect=OSError('full disk')), \
             self.assertRaises(OSError):
            self.chain.cleanup()
        self.assertEqual(sum(row.args[0].startswith('DROP DATABASE') for row in pg.call_args_list), 1)
        self.assertEqual(sum(row.args[0].startswith('DROP ROLE') for row in pg.call_args_list), 1)

    def test_cleanup_only_signals_held_popen_never_recorded_pids(self):
        proc = Mock(pid=111)
        proc.poll.side_effect = [None, 0, 0]
        proc.wait.return_value = 0
        self.chain.processes = [proc]
        self.chain.state['pids'] = [999]
        with patch.object(management.os, 'kill') as kill, patch.object(management, 'emit'):
            self.chain.cleanup()
        proc.terminate.assert_called_once()
        kill.assert_not_called()

    def test_owned_connections_can_quiesce_by_readonly_wait_without_termination(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        detail = dict(pid=444, datname=self.chain.database, usename=self.chain.db_user, backend_type='client backend',
            backend_start='2026-10-01T00:00:00Z', application_name=self.chain.pg_application('api', 0),
            client_addr='127.0.0.1', client_port=45500)
        live = dict(self.resources(), connections=1, connection_details=[detail])
        replies = [json.dumps(live), json.dumps(self.resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(management.time, 'sleep'), patch.object(management, 'emit'):
            self.chain.cleanup()
        self.assertEqual(pg.call_count, 5)
        self.assertNotIn('pg_terminate_backend', '\n'.join(row.args[0] for row in pg.call_args_list))
        receipt = json.loads((self.chain.work / 'cleanup.json').read_text())
        self.assertEqual(receipt['connection_quiescence']['owned_pg_backend_pids'], [444])

    def test_execute_cleanup_when_first_red_and_emit_both_fail(self):
        chain = Mock(work=self.chain.work, run_id=self.chain.run_id)
        chain.run.side_effect = AssertionError('first red')
        chain.first_red.side_effect = OSError('full disk')
        with patch.object(management, 'emit', side_effect=BrokenPipeError('stdout closed')), self.assertRaises(AssertionError):
            management.execute(chain)
        chain.cleanup.assert_called_once()

    def test_execute_cleanup_on_success_reporting_or_keyboard_interrupt(self):
        for exception in (KeyboardInterrupt(), SystemExit(130), None):
            chain = Mock(work=self.chain.work, run_id=self.chain.run_id)
            chain.run.side_effect = exception
            chain.run.return_value = {'passed': True}
            with patch.object(management, 'emit', side_effect=BrokenPipeError('closed')):
                with self.assertRaises(type(exception) if exception else BrokenPipeError):
                    management.execute(chain)
            chain.cleanup.assert_called_once()

    def test_execute_cleanup_failure_never_emits_complete(self):
        chain = Mock(work=self.chain.work, run_id=self.chain.run_id)
        chain.run.return_value = {'passed': True}
        chain.cleanup.side_effect = management.Blocked('owned database retained')
        with patch.object(management, 'emit') as emit, self.assertRaises(management.Blocked):
            management.execute(chain)
        self.assertNotIn('management.complete', [row.args[0] for row in emit.call_args_list])

    def test_acquisition_signal_retains_handle_boundary_before_interrupt(self):
        with self.assertRaises(management.Blocked):
            with management.acquisition_signals():
                signal.raise_signal(signal.SIGTERM)


if __name__ == '__main__':
    unittest.main()
