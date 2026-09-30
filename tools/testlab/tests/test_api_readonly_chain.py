"""Offline safety/protocol tests. No Docker, PG or formal API is started."""
import contextlib
import io
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid
from urllib.parse import parse_qs

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as readonly
from env import _private
from inventory import actual_interface


class ReadonlyProtectionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        binary = self.root / '.local/testlab/environments' / readonly.OWNER / 'backend/aiops'
        binary.parent.mkdir(parents=True)
        binary.write_bytes(b'owned formal nonrace binary')
        self.data = dict(owner=readonly.OWNER, binary=str(binary), source_tree_sha='known-source',
                         binary_sha256=readonly.hashlib.sha256(binary.read_bytes()).hexdigest(),
                         postgres_container='aiops-' + readonly.OWNER + '-pg', postgres_image_id='sha256:owned',
                         postgres_client_addresses=['127.0.0.1', '::1', '172.17.0.1'])
        self.run_id = str(uuid.uuid4())
        self.contract = Mock()
        self.contract.canonical.return_value = ('/api/v1/roles/{id}', 'main')
        with patch.object(readonly, 'Contract', return_value=self.contract):
            self.chain = readonly.Chain(self.root, self.data, self.run_id)

    def pg_info(self):
        return dict(Name='/aiops-' + readonly.OWNER + '-pg', Image='sha256:owned',
                    Config=dict(Image=readonly.PG_IMAGE, Labels={'aiops.testlab.owner': readonly.OWNER}),
                    State={'Running': True}, NetworkSettings={'Ports': {'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '15461'}]}})

    def pg_connection(self, **fields):
        return dict(pid=321, datname=self.chain.database, usename=self.chain.db_user,
                    application_name=self.chain.pg_application('api', 0), client_addr='172.17.0.1',
                    client_port=45000, backend_type='client backend', backend_start='2026-09-30 16:00:00+00',
                    state='idle') | fields

    def pg_resources(self, details=None, **fields):
        details = [] if details is None else details
        return dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                    database_owner=self.chain.db_user, database_tag=self.chain.owner_tag,
                    connections=len(details), connection_details=details) | fields

    def test_exact_owner_name_image_and_loopback_binding_required(self):
        info = self.pg_info()
        readonly.guard_pg(info, readonly.OWNER, 'sha256:owned')
        variants = [None, dict(info, Name='/other'), dict(info, Image='sha256:changed'),
                    dict(info, Config=dict(Image='postgres:latest', Labels=info['Config']['Labels'])),
                    dict(info, State={'Running': False}),
                    dict(info, Config=dict(Image=readonly.PG_IMAGE, Labels={'aiops.testlab.owner': 'other'}))]
        for ports in [[{'HostIp': '0.0.0.0', 'HostPort': '15461'}],
                      [{'HostIp': '127.0.0.1', 'HostPort': '5432'}],
                      [{'HostIp': '127.0.0.1', 'HostPort': '15461'}, {'HostIp': '::1', 'HostPort': '15461'}]]:
            variants.append(dict(info, NetworkSettings={'Ports': {'5432/tcp': ports}}))
        for value in variants:
            with self.subTest(value=value), self.assertRaises(readonly.Blocked):
                readonly.guard_pg(value, readonly.OWNER, 'sha256:owned')
        with self.assertRaises(readonly.Blocked):
            readonly.guard_pg(info, 'testlab-foreign')

    def test_controller_nonce_hmac_replay_and_live_pid_boundary(self):
        data = dict(self.data, control_secret='generated-test-secret', supervisor_pid=100)
        current = dict(owner=readonly.OWNER, pid=100, source_tree_sha='known-source', ready=True, api_pids=[101, 102])
        nonce = 'a' * 32
        receipt = dict(nonce=nonce, identity=current, proof=readonly.control_proof(data['control_secret'], nonce, current))
        self.assertEqual(readonly.signed_identity(data, nonce, receipt), current)
        for changed in [dict(receipt, nonce='b' * 32), dict(receipt, proof='forged'),
                        dict(receipt, identity=dict(current, pid=999)), dict(receipt, extra=True), None]:
            with self.assertRaises(readonly.Blocked):
                readonly.signed_identity(data, nonce, changed)
        for fields in [{'ready': False}, {'api_pids': [101, 101]}, {'source_tree_sha': 'changed'}, {'owner': 'foreign'}]:
            state = dict(current, **fields)
            bad = dict(nonce=nonce, identity=state, proof=readonly.control_proof(data['control_secret'], nonce, state))
            with self.assertRaises(readonly.Blocked):
                readonly.signed_identity(data, nonce, bad)

    def test_http_rejects_foreign_authority_redirect_controller_writes(self):
        for base, method, path in [('https://business.invalid', 'GET', '/api/v1/me'),
                                  (readonly.BASES[0], 'GET', '//business.invalid/me'),
                                  (readonly.BASES[0], 'GET', 'https://business.invalid/me'),
                                  (readonly.BASES[0], 'GET', '/api/v1/me#private'),
                                  (readonly.BASES[0], 'GET', '/api/v1/me\r\nAuthorization: secret'),
                                  (readonly.CONTROL, 'POST', '/shutdown'),
                                  (readonly.CONTROL, 'GET', '/identity')]:
            with patch.object(readonly.http.client, 'HTTPConnection') as connect, self.assertRaises(readonly.Blocked):
                readonly.transport(base, method, path)
            connect.assert_not_called()

    def test_response_bound_closes_connection(self):
        response = Mock(status=200)
        response.read.return_value = b'x' * (readonly.LIMIT + 1)
        connection = Mock()
        connection.getresponse.return_value = response
        with patch.object(readonly.http.client, 'HTTPConnection', return_value=connection), self.assertRaises(AssertionError):
            readonly.transport(readonly.BASES[0], 'GET', '/api/v1/roles')
        connection.close.assert_called_once()
        response.read.assert_called_once_with(readonly.LIMIT + 1)

    def test_fresh_state_credentials_private_and_never_password_in_public_state(self):
        state = self.chain.work / 'state.json'
        secret_file = self.chain.work / 'credentials.json'
        self.assertEqual(state.stat().st_mode & 0o777, 0o600)
        self.assertEqual(secret_file.stat().st_mode & 0o777, 0o600)
        text = state.read_text()
        self.assertNotIn(self.chain.password, text)
        self.assertNotIn(self.chain.db_password, text)
        self.assertEqual(json.loads(text)['pids'], [])
        self.assertTrue(self.chain.database.startswith('aiops_testlab_readonly_'))
        self.assertNotEqual(self.chain.database, 'aiops_testlab_app')

    def test_unknown_changed_source_and_changed_binary_block_launch(self):
        with patch.object(readonly, 'source_fingerprint', return_value='known-source'):
            self.chain.frozen()
        for digest in ['unknown', '', 'changed']:
            with patch.object(readonly, 'source_fingerprint', return_value=digest), self.assertRaises(readonly.Blocked):
                self.chain.frozen()
        Path(self.data['binary']).write_bytes(b'replaced')
        with patch.object(readonly, 'source_fingerprint', return_value='known-source'), self.assertRaises(readonly.Blocked):
            self.chain.frozen()

    def test_binary_build_metadata_rejects_unknown_entry_and_race_build(self):
        metadata = 'binary: go1.26\n\tpath\tgithub.com/wans/aiops-canvas/cmd/aiops\n\tbuild\tCGO_ENABLED=1\n'
        result = Mock(returncode=0, stdout=metadata)
        with patch.object(readonly.subprocess, 'run', return_value=result):
            self.assertFalse(readonly.binary_build_info(Path(self.data['binary']))['race_enabled'])
        for output in ['', metadata.replace('/cmd/aiops', '/cmd/other'), metadata + '\tbuild\t-race=true\n']:
            result.stdout = output
            with patch.object(readonly.subprocess, 'run', return_value=result), self.assertRaises(readonly.Blocked):
                readonly.binary_build_info(Path(self.data['binary']))

    def test_sql_scope_refuses_shared_db_other_user_and_foreign_container_before_exec(self):
        for database, user in [('aiops_testlab_app', 'testlab'), (self.chain.database, 'business')]:
            with patch.object(readonly.subprocess, 'run') as run, self.assertRaises(readonly.Blocked):
                self.chain.pg('SELECT 1;', database, user)
            run.assert_not_called()
        with patch.object(readonly, 'inspect_pg', return_value=dict(self.pg_info(), Name='/foreign')), \
             patch.object(readonly.subprocess, 'run') as run, self.assertRaises(readonly.Blocked):
            self.chain.sql('SELECT 1;')
        run.assert_not_called()

    def test_sql_uses_stdin_fixed_search_path_and_sanitizes_private_error(self):
        result = Mock(returncode=0, stdout='1', stderr='')
        with patch.object(readonly, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(readonly.subprocess, 'run', return_value=result) as run:
            self.assertEqual(self.chain.sql('SELECT 1', read=True), '1')
        argv = run.call_args.args[0]
        self.assertEqual(argv[-4:], ['-U', self.chain.db_user, '-d', self.chain.database])
        self.assertIn('PGAPPNAME=' + self.chain.pg_application('sql'), argv)
        sql = run.call_args.kwargs['input']
        self.assertIn('SET search_path=public;', sql)
        self.assertIn('BEGIN READ ONLY;', sql)
        self.assertIn('current_database()', sql)
        self.assertIn('current_user', sql)
        self.assertIn('current_schema()', sql)
        self.assertNotIn(self.chain.db_password, ' '.join(argv))
        result.returncode, result.stderr = 1, 'diagnostic ' + self.chain.db_password
        with patch.object(readonly, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(readonly.subprocess, 'run', return_value=result), self.assertRaises(readonly.Blocked) as error:
            self.chain.sql('SELECT 1;')
        self.assertNotIn(self.chain.db_password, str(error.exception))
        evidence = list(self.chain.work.glob('sql-error-*.json'))
        self.assertEqual(evidence[0].stat().st_mode & 0o777, 0o600)

    def test_admin_sql_and_both_api_pools_carry_exact_nonce_application_names(self):
        result = Mock(returncode=0, stdout='1', stderr='')
        with patch.object(readonly, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(readonly.subprocess, 'run', return_value=result) as run:
            self.chain.pg('SELECT 1;')
        self.assertIn('PGAPPNAME=' + self.chain.pg_application('admin'), run.call_args.args[0])
        processes = [Mock(pid=900, poll=Mock(return_value=None)), Mock(pid=901, poll=Mock(return_value=None))]
        with patch.object(self.chain, 'frozen'), patch.object(readonly.socket, 'socket') as socket, \
             patch.object(readonly.subprocess, 'Popen', side_effect=processes) as popen, \
             patch.object(readonly, 'transport', return_value=(200, b'{}', {})), \
             patch.object(readonly, '_process_port', return_value=True), patch.object(readonly, 'emit'):
            socket.return_value.__enter__.return_value.connect_ex.return_value = 1
            self.chain.start()
        for index, call in enumerate(popen.call_args_list):
            dsn = readonly.urlsplit(call.kwargs['env']['DATABASE_URL'])
            self.assertEqual(parse_qs(dsn.query)['application_name'], [self.chain.pg_application('api', index)])
            self.assertEqual(dsn.username, self.chain.db_user)
            self.assertEqual(dsn.path, '/' + self.chain.database)
            self.assertNotIn(self.chain.db_password, str(call.args))

    def test_role_database_creation_refuses_adoption_and_never_changes_shared_inputs(self):
        with patch.object(self.chain, 'frozen'), patch.object(self.chain, 'pg', return_value='{"role":true,"database":false}') as pg, \
             self.assertRaises(readonly.Blocked):
            self.chain.provision()
        self.assertEqual(pg.call_count, 1)
        self.assertFalse(self.chain.role_attempted)
        self.assertFalse(self.chain.db_attempted)

    def test_provision_creates_only_fresh_nonsuperuser_role_and_owned_database(self):
        replies = ['{"role":false,"database":false}', '', '', '', '']
        with patch.object(self.chain, 'frozen'), patch.object(self.chain, 'pg', side_effect=replies) as pg, \
             patch.object(readonly, 'positive_records', return_value={'positive_routes': []}), patch.object(readonly, 'emit'):
            self.chain.provision()
        sqls = [call.args[0] for call in pg.call_args_list]
        self.assertIn('NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION', sqls[1])
        self.assertIn('TEMPLATE template0;', sqls[2])
        self.assertIn('OWNER "' + self.chain.db_user + '"', sqls[2])
        self.assertIn('FROM PUBLIC', sqls[3])
        self.assertEqual(pg.call_args_list[-1].args[1], self.chain.database)
        self.assertTrue(self.chain.role_attempted and self.chain.db_attempted)
        self.assertFalse(any('aiops_testlab_app' in sql or 'management_audit' in sql or 'users-password' in sql for sql in sqls))
        for call in pg.call_args_list:
            self.assertNotIn(self.chain.db_password, str(call.kwargs))


    def test_sql_identifiers_reject_injection(self):
        for value in ['', 'readonly; DROP DATABASE business', 'readonly"other', 'a' * 64, '../business']:
            with self.assertRaises(readonly.Blocked):
                readonly.identifier(value)
        self.assertEqual(readonly.quote("fixture's"), "'fixture''s'")

    def test_fixed_ports_lock_rejects_overlap_symlink_and_public_lock(self):
        with readonly.exclusive(self.root):
            with self.assertRaises(readonly.Blocked), readonly.exclusive(self.root):
                pass
        lock = self.root / '.local/testlab/api-readonly.lock'
        os.chmod(lock, 0o644)
        with self.assertRaises(readonly.Blocked), readonly.exclusive(self.root):
            pass
        lock.unlink()
        target = self.root / 'foreign.txt'
        target.write_text('unchanged')
        lock.symlink_to(target)
        with self.assertRaises(OSError), readonly.exclusive(self.root):
            pass
        self.assertEqual(target.read_text(), 'unchanged')

    def test_exact_http_cells_do_not_derive_normal_coverage_from_fail_closed_or_401(self):
        response = json.dumps({'error': {'code': 'credential_unavailable'}}).encode()
        with patch.object(readonly, 'transport', return_value=(503, response, {})), patch.object(readonly, 'emit') as emit:
            self.chain.call('reader', 'GET', '/api/v1/roles/fixture?q=private-query',
                            want=503, code='credential_unavailable', scenario='dependency_unavailable')
        row = emit.call_args.kwargs['interface']
        self.assertEqual(row['path'], '/api/v1/roles/{id}')
        self.assertEqual(row['actual']['status'], 503)
        self.assertEqual(row['scenario'], 'failure')
        self.assertEqual(row['expected']['scenario_detail'], 'dependency_unavailable')
        self.assertEqual(row['expected']['status'], [503])
        self.assertEqual(row['route_class'], 'main')
        self.assertFalse(row['registration_evidence'])
        self.assertNotIn('private-query', json.dumps(row))
        self.assertEqual((self.chain.work / 'interfaces.jsonl').stat().st_mode & 0o777, 0o600)
        normalized = actual_interface(row)
        self.assertFalse(normalized['registration_evidence'])
        self.assertEqual(normalized['scenarios']['failure']['actual_status'], 503)

    def test_fixture_and_pagination_frames_are_accepted_by_actual_runner_normalizer(self):
        with patch.object(readonly, 'transport', return_value=(200, b'{}', {})), patch.object(readonly, 'emit') as emit:
            for purpose, category in [('fixture', 'normal'), ('pagination', 'boundary')]:
                self.chain.call('reader', 'GET', '/api/v1/roles/fixture', scenario=purpose)
                row = emit.call_args.kwargs['interface']
                self.assertEqual(row['scenario'], category)
                normalized = actual_interface(row)
                self.assertEqual(normalized['scenarios'][category]['expected_detail']['scenario_detail'], purpose)
        with patch.object(readonly, 'transport') as transport, self.assertRaises(readonly.Blocked):
            self.chain.call('reader', 'GET', '/api/v1/roles/fixture', scenario='unsupported')
        transport.assert_not_called()

    def test_http_first_red_is_immutable_private_and_contains_no_cookie_body_or_query(self):
        self.chain.cookies['reader'] = 'aiops_session=private-cookie'
        response = json.dumps({'error': {'code': 'denied', 'message': 'private-message'}}).encode()
        with patch.object(readonly, 'transport', return_value=(403, response, {})), patch.object(readonly, 'emit'):
            for want in [200, 201]:
                with self.assertRaises(AssertionError):
                    self.chain.call('reader', 'GET', '/api/v1/roles/actual-secret-id?private=query', want=want)
        text = (self.chain.work / 'first-failure.json').read_text()
        self.assertEqual(json.loads(text)['expected']['status'], [200])
        for secret in ['actual-secret-id', 'private-cookie', 'private-message', 'private=query']:
            self.assertNotIn(secret, text)
        self.assertEqual((self.chain.work / 'first-failure.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(len((self.chain.work / 'interfaces.jsonl').read_text().splitlines()), 2)

    def test_semantic_200_red_retained_and_cannot_emit_green(self):
        def boundary(value):
            readonly.check(value.get('id') == 'mine', 'private object ownership differs')
        with patch.object(readonly, 'transport', return_value=(200, b'{"id":"foreign"}', {})), \
             patch.object(readonly, 'emit') as emit, self.assertRaises(AssertionError):
            self.chain.call('reader', 'GET', '/api/v1/roles/fixture', validate=boundary)
        row = emit.call_args.kwargs['interface']
        self.assertEqual(row['status'], 'failed')
        self.assertEqual(row['actual']['status'], 200)
        self.assertFalse(row['registration_evidence'])
        self.assertIn('ownership', row['actual']['assertion'])

    def test_non_json_redirect_and_http_transport_failure_are_red(self):
        for status, raw in [(200, b'html response'), (302, b''), (200, b'{bad')]:
            with patch.object(readonly, 'transport', return_value=(status, raw, {})), \
                 patch.object(readonly, 'emit'), self.assertRaises(AssertionError):
                self.chain.call('reader', 'GET', '/api/v1/roles/fixture')
        with patch.object(readonly, 'transport', side_effect=ConnectionResetError('private transport detail')), \
             patch.object(readonly, 'emit') as emit, self.assertRaises(AssertionError):
            self.chain.call('reader', 'GET', '/api/v1/roles/fixture')
        self.assertNotIn('interface', emit.call_args.kwargs)
        rows = [json.loads(x) for x in (self.chain.work / 'interfaces.jsonl').read_text().splitlines()]
        self.assertEqual(rows[-1]['actual']['status'], None)
        self.assertNotIn('private transport detail', json.dumps(rows[-1]))

    def test_http_account_and_environment_guard_runs_before_transport(self):
        for fields in [dict(user='foreign'), dict(index=2), dict(environment='prod')]:
            args = dict(user='reader', method='GET', path='/api/v1/roles/fixture') | fields
            with patch.object(readonly, 'transport') as transport, self.assertRaises(readonly.Blocked):
                self.chain.call(**args)
            transport.assert_not_called()

    def test_cleanup_uses_direct_handles_term_then_bounded_kill_not_saved_pid(self):
        proc = Mock()
        proc.poll.side_effect = [None, -9]
        proc.wait.side_effect = [subprocess.TimeoutExpired('owned', 8), -9]
        self.chain.processes = [proc]
        self.chain.state['pids'] = [99999]
        with patch.object(readonly, 'emit'), patch.object(readonly.os, 'kill') as kill:
            self.chain.cleanup()
        proc.terminate.assert_called_once()
        proc.kill.assert_called_once()
        kill.assert_not_called()
        self.assertLessEqual(proc.wait.call_args_list[0].kwargs['timeout'], 8)
        self.assertLessEqual(proc.wait.call_args_list[1].kwargs['timeout'], 2)

    def test_cancel_during_popen_acquisition_retains_handle_before_honoring_stop(self):
        handles = []
        with patch.object(readonly.signal, 'signal') as register, self.assertRaises(readonly.Blocked):
            with readonly.acquisition_signals():
                stop = register.call_args.args[1]
                stop(signal.SIGTERM, None)
                handles.append('owned Popen handle')
        self.assertEqual(handles, ['owned Popen handle'])

    def test_cleanup_deadline_includes_pg_inspection_time(self):
        with patch.object(readonly.time, 'monotonic', side_effect=[1, 5]), \
             patch.object(readonly, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(readonly.subprocess, 'run') as run, self.assertRaises(readonly.Blocked):
            self.chain.pg('SELECT 1;', deadline=4)
        run.assert_not_called()

    def test_cleanup_exact_role_db_owner_tag_and_readback(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        state = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                     database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        replies = [json.dumps(state), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(readonly, 'emit'):
            self.chain.cleanup()
        sqls = [call.args[0] for call in pg.call_args_list]
        self.assertIn('DROP DATABASE "' + self.chain.database + '";', sqls)
        self.assertIn('DROP ROLE "' + self.chain.db_user + '";', sqls)
        self.assertFalse(any('FORCE' in sql or 'pg_terminate_backend' in sql or 'aiops_testlab_app' in sql for sql in sqls))
        for call in pg.call_args_list:
            self.assertIn('deadline', call.kwargs)
            self.assertEqual(call.kwargs['timeout'], 12)
        self.assertTrue(json.loads((self.chain.work / 'cleanup.json').read_text())['database_absent'])
        self.assertEqual(json.loads((self.chain.work / 'cleanup-progress.json').read_text())['stage'], 'absence_readback')

    def test_connection_attribution_requires_nonce_database_role_backend_pid_and_client_origin(self):
        api = self.pg_connection()
        self.assertEqual(self.chain.connection_origin(api), 'owned_api')
        for kind, user in [('sql', self.chain.db_user), ('admin', 'testlab')]:
            unix = self.pg_connection(application_name=self.chain.pg_application(kind), usename=user,
                                      client_addr=None, client_port=-1)
            self.assertEqual(self.chain.connection_origin(unix), 'owned_fixture_sql')
            for changed in [dict(unix, usename='business'), dict(unix, client_addr='127.0.0.1'),
                            dict(unix, client_port=1234), dict(unix, client_port=-1.0)]:
                with self.subTest(changed=changed), self.assertRaises(readonly.Blocked):
                    self.chain.connection_origin(changed)
        for fields in [dict(pid=True), dict(pid=0), dict(pid='321'), dict(datname='aiops_testlab_app'),
                       dict(usename='testlab'), dict(application_name='psql'), dict(application_name=''),
                       dict(application_name='readonly-api-' + '0' * 32 + '-0'), dict(client_addr='10.0.0.1'),
                       dict(client_port=0), dict(client_port=True), dict(backend_type='autovacuum worker'),
                       dict(backend_start=None), dict(backend_start='')]:
            with self.subTest(fields=fields), self.assertRaises(readonly.Blocked):
                self.chain.connection_origin(api | fields)

    def test_postgres_autovacuum_is_maintenance_in_exact_owned_database(self):
        row = dict(pid=22303, datname=self.chain.database, backend_type='autovacuum worker',
                   backend_start='2026-09-30 16:39:28+00', usename=None,
                   application_name='', client_addr=None, client_port=None)
        self.assertEqual(self.chain.connection_origin(row), 'postgres_background')
        for field, value in [('datname', 'foreign'), ('backend_type', 'client backend'),
                             ('usename', self.chain.db_user), ('application_name', 'foreign'),
                             ('client_addr', '192.0.2.1'), ('client_port', 321)]:
            with self.subTest(field=field), self.assertRaises(readonly.Blocked):
                self.chain.connection_origin({**row, field: value})

    def test_autovacuum_exits_before_drop_without_being_signaled(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        row = dict(pid=22303, datname=self.chain.database, backend_type='autovacuum worker',
                   backend_start='2026-09-30 16:39:28+00', usename=None,
                   application_name='', client_addr=None, client_port=None)
        replies = [json.dumps(self.pg_resources([row])), json.dumps(self.pg_resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(readonly, 'emit'), patch.object(readonly.time, 'sleep'):
            self.chain.cleanup()
        statements = [c.args[0] for c in pg.call_args_list]
        self.assertEqual(sum(s.startswith('DROP DATABASE') for s in statements), 1)
        self.assertFalse(any('FORCE' in s or 'pg_terminate_backend' in s for s in statements))
        q = json.loads((self.chain.work / 'cleanup.json').read_text())['connection_quiescence']
        self.assertEqual(q['owned_pg_backend_pids'], [])
        self.assertEqual(q['postgres_background_backend_pids'], [22303])

    def test_owned_backend_quiescence_reads_until_absent_then_drops_each_resource_once(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        self.chain.processes = [Mock(pid=900, poll=Mock(return_value=0))]
        initial = self.pg_resources([self.pg_connection()])
        replies = [json.dumps(initial), json.dumps(self.pg_resources()), '', '', '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(readonly, 'emit'), \
             patch.object(readonly.time, 'sleep') as sleep:
            self.chain.cleanup()
        sqls = [call.args[0] for call in pg.call_args_list]
        self.assertTrue(sqls[0].startswith('SELECT ') and sqls[1].startswith('SELECT '))
        self.assertIn('backend_start::text', sqls[0])
        self.assertNotIn("'query'", sqls[0])
        self.assertEqual(sum(sql.startswith('DROP DATABASE') for sql in sqls), 1)
        self.assertEqual(sum(sql.startswith('DROP ROLE') for sql in sqls), 1)
        self.assertFalse(any('FORCE' in sql or 'pg_terminate_backend' in sql for sql in sqls))
        sleep.assert_called_once()
        self.assertLessEqual(sleep.call_args.args[0], .15)
        self.assertLess(pg.call_args_list[1].kwargs['deadline'], pg.call_args_list[0].kwargs['deadline'])
        snapshot_path = self.chain.work / 'first-connection-snapshot.json'
        snapshot = json.loads(snapshot_path.read_text())
        self.assertEqual(snapshot_path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(snapshot['api_owned_process_ids'], [900])
        self.assertEqual(snapshot['details'][0]['pid'], 321)
        self.assertEqual(snapshot['details'][0]['proven_origin'], 'owned_api')
        receipt = json.loads((self.chain.work / 'cleanup.json').read_text())
        self.assertEqual(receipt['connection_quiescence']['samples'], 2)
        self.assertEqual(receipt['connection_quiescence']['owned_pg_backend_pids'], [321])
        self.assertEqual(receipt['owned_process_ids'], [900])

    def test_foreign_or_untagged_backend_stops_without_wait_or_ddl_and_preserves_first_snapshot(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        state = self.pg_resources([self.pg_connection(application_name='psql')])
        with patch.object(self.chain, 'pg', return_value=json.dumps(state)) as pg, \
             patch.object(readonly.time, 'sleep') as sleep, self.assertRaises(readonly.Blocked):
            self.chain.cleanup()
        pg.assert_called_once()
        sleep.assert_not_called()
        snapshot = json.loads((self.chain.work / 'first-connection-snapshot.json').read_text())
        self.assertEqual(snapshot['details'][0]['application_name'], 'psql')
        self.assertEqual(snapshot['details'][0]['proven_origin'], 'unproven')
        self.assertFalse((self.chain.work / 'cleanup.json').exists())

    def test_owned_backend_wait_stops_at_eight_seconds_without_any_drop_or_force(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        state = self.pg_resources([self.pg_connection()])
        now = [0.0]
        def exhaust(_seconds):
            now[0] = 8.0
        with patch.object(self.chain, 'pg', return_value=json.dumps(state)) as pg, \
             patch.object(readonly.time, 'monotonic', side_effect=lambda: now[0]), \
             patch.object(readonly.time, 'sleep', side_effect=exhaust) as sleep, self.assertRaises(readonly.Blocked):
            self.chain.cleanup()
        self.assertEqual(pg.call_count, 2)
        sleep.assert_called_once()
        self.assertTrue(all(call.args[0].startswith('SELECT ') for call in pg.call_args_list))
        self.assertEqual(pg.call_args_list[0].kwargs['deadline'], 40)
        self.assertEqual(pg.call_args_list[1].kwargs['deadline'], 8)
        last = json.loads((self.chain.work / 'connection-quiescence.json').read_text())[-1]
        self.assertEqual(last['elapsed_seconds'], 8)
        self.assertFalse((self.chain.work / 'cleanup.json').exists())

    def test_ownership_change_during_quiescence_fails_before_ddl(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        initial = self.pg_resources([self.pg_connection()])
        changed = self.pg_resources(database_tag='foreign')
        with patch.object(self.chain, 'pg', side_effect=[json.dumps(initial), json.dumps(changed)]) as pg, \
             patch.object(readonly.time, 'sleep'), self.assertRaises(readonly.Blocked):
            self.chain.cleanup()
        self.assertEqual(pg.call_count, 2)
        self.assertTrue(all(call.args[0].startswith('SELECT ') for call in pg.call_args_list))

    def test_duplicate_or_incomplete_connection_catalog_cannot_trigger_wait_or_drop(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        variants = [self.pg_resources([self.pg_connection()], connections=2),
                    self.pg_resources([self.pg_connection(), self.pg_connection()]),
                    self.pg_resources([self.pg_connection(pid=[321])]),
                    self.pg_resources([None]), self.pg_resources([self.pg_connection(pid=True)])]
        for state in variants:
            with self.subTest(state=state), patch.object(self.chain, 'pg', return_value=json.dumps(state)) as pg, \
                 patch.object(readonly.time, 'sleep') as sleep, self.assertRaises(readonly.Blocked):
                self.chain.cleanup()
            pg.assert_called_once()
            sleep.assert_not_called()

    def test_cleanup_recovery_loads_only_bound_private_nonce_without_adopting_saved_pids_or_credentials(self):
        self.chain.state['pids'] = [99999]
        _private(self.chain.work / 'state.json', self.chain.state)
        # Recovery does not need (or read) secret credentials or the product
        # contract. It uses current doctor data plus exact resource readback.
        (self.chain.work / 'credentials.json').unlink()
        with patch.object(readonly.socket, 'socket') as socket, patch.object(readonly.Chain, 'pg') as pg, \
             patch.object(readonly, 'Contract') as contract:
            socket.return_value.__enter__.return_value.connect_ex.return_value = 1
            recovered = readonly.Chain.from_cleanup_record(self.root, self.data, self.run_id)
        pg.assert_not_called()
        contract.assert_not_called()
        self.assertEqual(recovered.processes, [])
        self.assertFalse(hasattr(recovered, 'password') or hasattr(recovered, 'db_password'))
        self.assertEqual(recovered.database, self.chain.database)
        self.assertEqual(recovered.owner_tag, self.chain.owner_tag)
        recovered.first_red({'stage': 'original', 'error': 'retained'})
        original = (recovered.work / 'first-failure.json').read_bytes()
        replies = [json.dumps(self.pg_resources()), '', '', '{"role":false,"database":false}']
        with patch.object(recovered, 'pg', side_effect=replies), patch.object(recovered, 'run') as run, \
             patch.object(readonly.os, 'kill') as kill, patch.object(readonly, 'emit'):
            result = readonly.execute(recovered, cleanup_only=True)
        run.assert_not_called()
        kill.assert_not_called()
        self.assertEqual(result, dict(cleanup_only=True, recovered_run=self.run_id))
        self.assertEqual((recovered.work / 'first-failure.json').read_bytes(), original)
        self.assertEqual(json.loads((recovered.work / 'cleanup.json').read_text())['owned_process_ids'], [])

    def test_cleanup_recovery_rejects_foreign_nonce_ownership_busy_ports_and_nonprivate_state(self):
        path = self.chain.work / 'state.json'
        for fields in [dict(database='aiops_testlab_app'), dict(database_user='testlab'), dict(owner_tag='foreign'),
                       dict(owner='foreign'), dict(run_id=str(uuid.uuid4())), dict(ports=[18561, 18562])]:
            _private(path, self.chain.state | fields)
            with self.subTest(fields=fields), patch.object(readonly.socket, 'socket') as socket, self.assertRaises(readonly.Blocked):
                readonly.Chain.from_cleanup_record(self.root, self.data, self.run_id)
            socket.assert_not_called()
        _private(path, self.chain.state)
        with patch.object(readonly.socket, 'socket') as socket, self.assertRaises(readonly.Blocked):
            socket.return_value.__enter__.return_value.connect_ex.return_value = 0
            readonly.Chain.from_cleanup_record(self.root, self.data, self.run_id)
        os.chmod(path, 0o644)
        with patch.object(readonly.socket, 'socket') as socket, self.assertRaises(readonly.Blocked):
            readonly.Chain.from_cleanup_record(self.root, self.data, self.run_id)
        socket.assert_not_called()

    def test_cleanup_recovery_rejects_symlink_state_before_sql_or_listener_checks(self):
        path = self.chain.work / 'state.json'
        target = self.chain.work / 'other-state.json'
        path.rename(target)
        path.symlink_to(target)
        with patch.object(readonly.socket, 'socket') as socket, patch.object(readonly.Chain, 'pg') as pg, \
             self.assertRaises(readonly.Blocked):
            readonly.Chain.from_cleanup_record(self.root, self.data, self.run_id)
        socket.assert_not_called()
        pg.assert_not_called()

    def test_cleanup_refuses_foreign_role_db_owner_or_tag(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        expected = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                        database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        for change in [{'role': 'foreign'}, {'database_owner': 'business'}, {'database_tag': 'foreign'}, {'role': None}, {'connections': 1}]:
            with patch.object(self.chain, 'pg', return_value=json.dumps(expected | change)) as pg, self.assertRaises(readonly.Blocked):
                self.chain.cleanup()
            self.assertEqual(pg.call_count, 1)

    def test_client_sql_timeout_keeps_stage_private_and_does_not_claim_cleanup_success(self):
        self.chain.cleanup_phase = 'drop_database'
        with patch.object(readonly, 'inspect_pg', return_value=self.pg_info()), \
             patch.object(readonly.subprocess, 'run', side_effect=subprocess.TimeoutExpired('docker', 12)), self.assertRaises(readonly.Blocked) as error:
            self.chain.pg('DROP DATABASE ' + readonly.identifier(self.chain.database) + ';', timeout=12)
        self.assertIn('drop_database', str(error.exception))
        evidence = list(self.chain.work.glob('sql-timeout-*.json'))
        self.assertEqual(len(evidence), 1)
        detail = json.loads(evidence[0].read_text())
        self.assertEqual(detail['mutation_outcome'], 'unknown')
        self.assertEqual(evidence[0].stat().st_mode & 0o777, 0o600)
        self.assertFalse((self.chain.work / 'cleanup.json').exists())
        self.assertEqual(json.loads((self.chain.work / 'first-sql-timeout.json').read_text())['stage'], 'drop_database')

    def test_drop_database_timeout_resolved_only_by_exact_absence_without_replaying_ddl(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        before = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                      database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        absent_db = dict(before, database_exists=False, database_owner=None, database_tag=None)
        replies = [json.dumps(before), readonly.SQLTransportTimeout('first client timeout'), json.dumps(absent_db), '',
                   '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, patch.object(readonly, 'emit') as emit:
            self.chain.cleanup()
        drops = [call.args[0] for call in pg.call_args_list if call.args[0].startswith('DROP DATABASE')]
        self.assertEqual(len(drops), 1)
        self.assertTrue(json.loads((self.chain.work / 'drop_database-timeout-readback.json').read_text())['resource_absent'])
        self.assertTrue(any(call.args[0] == 'readonly.cleanup_timeout_resolved.drop_database' for call in emit.call_args_list))
        self.assertTrue(json.loads((self.chain.work / 'cleanup.json').read_text())['database_absent'])

    def test_timeout_remaining_database_unreadable_catalog_or_changed_owner_stays_failed_no_retry(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        before = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                      database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        for readback in [json.dumps(before), readonly.Blocked('readback unavailable'),
                         json.dumps(dict(before, role='foreign'))]:
            with patch.object(self.chain, 'pg', side_effect=[json.dumps(before), readonly.SQLTransportTimeout('first timeout'), readback]) as pg, \
                 patch.object(readonly, 'emit') as emit, self.assertRaises(readonly.Blocked):
                self.chain.cleanup()
            self.assertEqual(sum(call.args[0].startswith('DROP DATABASE') for call in pg.call_args_list), 1)
            self.assertFalse(any(call.args[0] == 'readonly.cleanup' for call in emit.call_args_list))

    def test_drop_role_timeout_requires_both_role_and_database_absent(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        before = dict(role_exists=True, database_exists=False, role=self.chain.owner_tag,
                      database_owner=None, database_tag=None, connections=0)
        absent = dict(before, role_exists=False, role=None)
        with patch.object(self.chain, 'pg', side_effect=[json.dumps(before), readonly.SQLTransportTimeout('first timeout'), json.dumps(absent),
                                                       '{"role":false,"database":false}']) as pg, patch.object(readonly, 'emit'):
            self.chain.cleanup()
        self.assertEqual(sum(call.args[0].startswith('DROP ROLE') for call in pg.call_args_list), 1)
        self.assertFalse(any(call.args[0].startswith('DROP DATABASE') for call in pg.call_args_list))
        self.assertTrue(json.loads((self.chain.work / 'drop_role-timeout-readback.json').read_text())['resource_absent'])

    def test_me_missing_oncall_is_exact_503_failure_while_home_still_requires_200(self):
        self.chain.unproven = ['/me', '/home']
        home = dict(briefing=None, alert_trend=None, recent=[], services=[], attention=[],
                    availability={'service_metrics': 'not_connected'})
        calls = []
        def call(user, method, path, **fields):
            calls.append((user, path, fields))
            if path == '/api/v1/home' and fields.get('want', 200) == 200:
                fields['validate'](home)
            return {}, {}
        with patch.object(self.chain, 'call', side_effect=call), patch.object(readonly, 'emit'):
            self.chain.catalogs()
        account = next(fields for user, path, fields in calls if path == '/api/v1/me')
        self.assertEqual(account['want'], 503)
        self.assertEqual(account['code'], 'oncall_unavailable')
        self.assertEqual(account['scenario'], 'dependency_unavailable')
        main_home = next(fields for user, path, fields in calls if path == '/api/v1/home' and user == 'reader' and 'want' not in fields)
        self.assertEqual(main_home.get('want', 200), 200)

    def test_unproven_cleanup_readback_and_unstopped_process_prevent_green(self):
        self.chain.role_attempted = True
        with patch.object(self.chain, 'pg', side_effect=['{"role_exists":false,"database_exists":false,"role":null,"database_owner":null,"database_tag":null,"connections":0}', '{"role":true,"database":false}']), \
             patch.object(readonly, 'emit'), self.assertRaises(AssertionError):
            self.chain.cleanup()
        proc = Mock()
        proc.poll.return_value = None
        proc.wait.side_effect = OSError('cannot reap child')
        self.chain.processes = [proc]
        with patch.object(self.chain, 'pg') as pg, self.assertRaises(readonly.Blocked):
            self.chain.cleanup()
        pg.assert_not_called()

    def test_cleanup_failure_does_not_replace_first_business_red(self):
        primary = AssertionError('private object boundary differs')
        with patch.object(self.chain, 'run', side_effect=primary), \
             patch.object(self.chain, 'cleanup', side_effect=readonly.Blocked('cleanup failed')), \
             patch.object(readonly, 'emit'), self.assertRaises(AssertionError) as error:
            readonly.execute(self.chain)
        self.assertIs(error.exception, primary)
        self.assertEqual(json.loads((self.chain.work / 'first-failure.json').read_text())['error'], str(primary))
        self.assertTrue((self.chain.work / 'cleanup-error.json').exists())
        with patch.object(self.chain, 'run', return_value={}), patch.object(self.chain, 'cleanup', side_effect=RuntimeError('cannot cleanup')), \
             patch.object(readonly, 'emit'), self.assertRaises(RuntimeError):
            readonly.execute(self.chain)

    def test_business_first_red_io_failure_never_skips_cleanup_or_replaces_business_error(self):
        primary = AssertionError('original business boundary failed')
        with patch.object(self.chain, 'run', side_effect=primary), \
             patch.object(self.chain, 'first_red', side_effect=OSError('disk full')), \
             patch.object(self.chain, 'cleanup') as cleanup, patch.object(readonly, 'emit'), self.assertRaises(AssertionError) as error:
            readonly.execute(self.chain)
        cleanup.assert_called_once()
        self.assertIs(error.exception, primary)

    def test_business_stdout_broken_pipe_never_skips_cleanup_or_replaces_business_error(self):
        primary = AssertionError('original HTTP status mismatch')
        with patch.object(self.chain, 'run', side_effect=primary), \
             patch.object(readonly, 'emit', side_effect=BrokenPipeError('stdout closed')), \
             patch.object(self.chain, 'cleanup') as cleanup, self.assertRaises(AssertionError) as error:
            readonly.execute(self.chain)
        cleanup.assert_called_once()
        self.assertIs(error.exception, primary)

    def test_cleanup_diagnostic_io_failure_does_not_skip_owned_drops_but_cannot_turn_green(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        before = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                      database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        with patch.object(self.chain, 'pg', side_effect=[json.dumps(before), '', '', '{"role":false,"database":false}']) as pg, \
             patch.object(readonly, '_private', side_effect=OSError('diagnostic disk unavailable')), \
             patch.object(readonly, 'emit') as emit, self.assertRaises(OSError):
            self.chain.cleanup()
        self.assertEqual(sum(call.args[0].startswith('DROP DATABASE') for call in pg.call_args_list), 1)
        self.assertEqual(sum(call.args[0].startswith('DROP ROLE') for call in pg.call_args_list), 1)
        self.assertEqual(pg.call_count, 4)
        self.assertFalse(any(call.args[0] == 'readonly.cleanup' for call in emit.call_args_list))

    def test_timeout_recovery_stdout_failure_occurs_after_role_drop_and_absence_proof(self):
        self.chain.role_attempted = self.chain.db_attempted = True
        before = dict(role_exists=True, database_exists=True, role=self.chain.owner_tag,
                      database_owner=self.chain.db_user, database_tag=self.chain.owner_tag, connections=0)
        absent_db = dict(before, database_exists=False, database_owner=None, database_tag=None)
        replies = [json.dumps(before), readonly.SQLTransportTimeout('timeout'), json.dumps(absent_db), '',
                   '{"role":false,"database":false}']
        with patch.object(self.chain, 'pg', side_effect=replies) as pg, \
             patch.object(readonly, 'emit', side_effect=BrokenPipeError('stdout closed')), self.assertRaises(BrokenPipeError):
            self.chain.cleanup()
        self.assertEqual(pg.call_count, 5)
        self.assertTrue(any(call.args[0].startswith('DROP ROLE') for call in pg.call_args_list))
        self.assertTrue(json.loads((self.chain.work / 'cleanup.json').read_text())['database_absent'])

    def test_sigterm_during_cleanup_retains_evidence_and_prevents_complete_green(self):
        with patch.object(readonly.signal, 'signal') as register:
            def cleanup():
                register.call_args.args[1](signal.SIGTERM, None)
            with patch.object(self.chain, 'run', return_value={}), patch.object(self.chain, 'cleanup', side_effect=cleanup), \
                 patch.object(readonly, 'emit') as emit, self.assertRaises(readonly.Blocked):
                readonly.execute(self.chain)
        self.assertFalse(any(call.args[0] == 'readonly.complete' for call in emit.call_args_list))
        self.assertEqual(json.loads((self.chain.work / 'first-failure.json').read_text())['stage'], 'cleanup_interrupted')
        self.assertEqual((self.chain.work / 'cleanup-interrupted.json').stat().st_mode & 0o777, 0o600)

    def test_sigterm_interruption_enters_finally_and_restores_handler(self):
        def previous(signum, frame):
            pass
        cleaned = []
        with patch.object(readonly.signal, 'signal', return_value=previous) as register:
            with self.assertRaises(readonly.Blocked), readonly.cooperative_signals():
                handler = register.call_args.args[1]
                try:
                    handler(signal.SIGTERM, None)
                finally:
                    cleaned.append(True)
        self.assertEqual(cleaned, [True])
        self.assertEqual(register.call_args.args, (signal.SIGTERM, previous))


class ReadonlyEvidenceTests(unittest.TestCase):
    def test_store_source_inventory_401_failed_run_and_503_do_not_become_positive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = root / '.local/testlab'
            data.mkdir(parents=True)
            path = data / 'state.sqlite3'
            connection = sqlite3.connect(path)
            connection.executescript('CREATE TABLE runs(id TEXT,data TEXT); CREATE TABLE records(run_id TEXT,kind TEXT,data TEXT);')
            for run in readonly.BASELINE_RUNS:
                connection.execute('INSERT INTO runs VALUES(?,?)', (run, json.dumps({'status': 'passed'})))
            connection.execute('INSERT INTO runs VALUES(?,?)', ('failed-run', '{"status":"failed"}'))
            positive = dict(method='GET', path='/api/v1/me', evidence_mode='real_local',
                            scenarios={'normal': {'status': 'passed', 'actual_status': 200}})
            fixture = [positive, positive | {'evidence_mode': 'source', 'path': '/api/v1/source'},
                       positive | {'path': '/api/v1/unauth', 'scenarios': {'normal': {'status': 'passed', 'actual_status': 401}}},
                       positive | {'path': '/api/v1/503', 'scenarios': {'normal': {'status': 'passed', 'actual_status': 503}}},
                       positive | {'path': '/api/v1/authz200', 'scenarios': {'authorization': {'status': 'passed', 'actual_status': 200}}},
                       dict(method='GET', path='/api/v1/members', scenario='normal', status='passed', actual={'status': 200})]
            for row in fixture:
                connection.execute('INSERT INTO records VALUES(?,?,?)', (readonly.BASELINE_RUNS[0], 'interfaces', json.dumps(row)))
            connection.execute('INSERT INTO records VALUES(?,?,?)', ('failed-run', 'interfaces', json.dumps(positive | {'path': '/api/v1/failed'})))
            connection.commit()
            connection.close()
            before = path.read_bytes()
            result = readonly.positive_records(root)
            self.assertEqual(result['positive_routes'], [['GET', '/me'], ['GET', '/members']])
            self.assertEqual(path.read_bytes(), before)
            self.assertEqual(result['positive_runs'], [readonly.BASELINE_RUNS[0]])

    def test_actual_contract_canonicalizes_details_without_conflating_166_main_with_auxiliary(self):
        contract = readonly.Contract(readonly.ROOT)
        self.assertEqual(len(contract.main), 166)
        cases = [('GET', '/api/v1/roles/fixture?cursor=private', '/api/v1/roles/{id}', 'main'),
                 ('GET', '/api/v1/mentions/conversation/fixture/preview', '/api/v1/mentions/{kind}/{id}/preview', 'main'),
                 ('GET', '/api/v1/members/fixture', '/api/v1/members/{id}', 'auxiliary_or_historical'),
                 ('POST', '/api/v1/auth/login', '/api/v1/auth/login', 'auxiliary_or_historical')]
        for method, path, expected, category in cases:
            with self.subTest(path=path):
                self.assertEqual(contract.canonical(method, path), (expected, category))
        for method, path in [('GET', '/business/me'), ('POST', '/api/v1/audit/fixture'), ('GET', '/api/v1/does-not-exist')]:
            with self.assertRaises(readonly.Blocked):
                contract.canonical(method, path)

    def test_top_level_dto_required_field_smoke_catches_200_missing_fields(self):
        contract = readonly.Contract(readonly.ROOT)
        contract.shape('GET', '/api/v1/roles', 200, {'items': []})
        with self.assertRaises(AssertionError):
            contract.shape('GET', '/api/v1/roles', 200, {})
        with self.assertRaises(AssertionError):
            contract.shape('GET', '/api/v1/roles', 200, [])

    def test_cli_offline_inventory_does_not_invoke_doctor_or_start_resources(self):
        with patch.object(sys, 'argv', ['api_readonly_chain.py', '--inventory']), \
             patch.object(readonly, 'positive_records', return_value={'required_runs': {}, 'positive_routes': []}), \
             patch.object(readonly, 'doctor') as doctor, patch.object(readonly, 'execute') as execute, \
             contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(readonly.main(), 0)
        doctor.assert_not_called()
        execute.assert_not_called()
        with patch.object(sys, 'argv', ['api_readonly_chain.py']), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            readonly.main()


if __name__ == '__main__':
    unittest.main()
