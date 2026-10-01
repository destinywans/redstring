"""Offline regressions for the bounded original-Go smoke entrypoint."""
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import local_original_smokes as smoke


def archive(entries):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w') as output:
        for name, raw, kind in entries:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.size = len(raw) if kind == tarfile.REGTYPE else 0
            output.addfile(member, io.BytesIO(raw) if kind == tarfile.REGTYPE else None)
    return buffer.getvalue()


class OriginalSmokeTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work = self.root / '.local/testlab/current-local-smokes/fixture'
        self.work.mkdir(parents=True)
        self.root_patch = patch.object(smoke, 'ROOT', self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)

    def test_http_namespace_regression_calls_stdlib_client_and_closes(self):
        connection = Mock()
        connection.getresponse.return_value.status = 200
        connection.getresponse.return_value.read.return_value = b'{}'
        with patch.object(smoke.http.client, 'HTTPConnection', return_value=connection) as factory:
            self.assertEqual((200, b'{}'), smoke.wire(18573, 'GET', '/loki/api/v1/query_range?query=owned'))
        factory.assert_called_once_with('127.0.0.1', 18573, timeout=8)
        connection.close.assert_called_once()

    def test_http_close_on_read_failure(self):
        connection = Mock()
        connection.getresponse.side_effect = OSError('controlled read failure')
        with patch.object(smoke.http.client, 'HTTPConnection', return_value=connection):
            with self.assertRaises(OSError):
                smoke.wire(18573, 'GET', '/loki/api/v1/query_range')
        connection.close.assert_called_once()

    def test_excluded_wire_never_constructs_client(self):
        for port, method, path in [(80, 'GET', '/'), (18576, 'POST', '/realms/aiops-testlab/protocol/openid-connect/token'),
                                   (18573, 'DELETE', '/loki/api/v1/push'), (18573, 'GET', '//other'),
                                   (18573, 'GET', '/loki\\x')]:
            with self.subTest(port=port, method=method, path=path), patch.object(smoke.http.client, 'HTTPConnection') as factory:
                with self.assertRaises(smoke.Blocked):
                    smoke.wire(port, method, path)
                factory.assert_not_called()

    def realm_read(self, entries, expected=b'{"synthetic":true}', returncode=0):
        payload = archive(entries)
        def copied(argv, **kwargs):
            self.assertEqual(['docker', 'cp', 'a' * 64 + ':/opt/keycloak/data/import/realm.json', '-'], argv)
            self.assertNotIn('shell', kwargs)
            kwargs['stdout'].write(payload)
            return Mock(returncode=returncode)
        with patch.object(smoke.subprocess, 'run', side_effect=copied):
            return smoke.read_container_realm('a' * 64, expected, self.work)

    def test_archive_reads_actual_bytes_without_exec_cat_or_extract(self):
        raw = b'{"synthetic":true}'
        with patch.object(tarfile.TarFile, 'extractall', side_effect=AssertionError('must not extract')):
            self.assertEqual(smoke.hashlib.sha256(raw).hexdigest(), self.realm_read([('realm.json', raw, tarfile.REGTYPE)]))
        self.assertEqual(0o600, (self.work / 'realm-container.tar').stat().st_mode & 0o777)

    def test_archive_actual_bytes_mismatch_rejected(self):
        with self.assertRaises(smoke.Blocked):
            self.realm_read([('realm.json', b'{"synthetic":fals}', tarfile.REGTYPE)])

    def test_archive_path_traversal_rejected(self):
        with self.assertRaises(smoke.Blocked):
            self.realm_read([('../realm.json', b'{"synthetic":true}', tarfile.REGTYPE)])

    def test_archive_symlink_rejected(self):
        with self.assertRaises(smoke.Blocked):
            self.realm_read([('realm.json', b'', tarfile.SYMTYPE)])

    def test_archive_extra_member_rejected(self):
        with self.assertRaises(smoke.Blocked):
            self.realm_read([('realm.json', b'{"synthetic":true}', tarfile.REGTYPE), ('other', b'x', tarfile.REGTYPE)])

    def test_archive_failed_copy_rejected(self):
        with self.assertRaises(smoke.Blocked):
            self.realm_read([('realm.json', b'{"synthetic":true}', tarfile.REGTYPE)], returncode=1)

    def test_archive_requires_exact_cid_before_command(self):
        with patch.object(smoke.subprocess, 'run') as run:
            with self.assertRaises(smoke.Blocked):
                smoke.read_container_realm('old-name', b'{}', self.work)
            run.assert_not_called()

    def test_loki_exact_and_one_known_enrichment_only(self):
        expected = {'application': 'testlab', 'environment': 'test', 'testlab_smoke_run': 'this-UUID'}
        self.assertTrue(smoke.exact_loki_labels(expected, expected))
        self.assertTrue(smoke.exact_loki_labels(dict(expected, service_name='testlab'), expected))
        for actual in [dict(expected, service_name='elsewhere'), dict(expected, other='extra'),
                       dict(expected, testlab_smoke_run='other-UUID'), {'application': 'testlab'}]:
            with self.subTest(actual=actual):
                self.assertFalse(smoke.exact_loki_labels(actual, expected))

    def test_seed_observed_mismatch_fails_immediately_without_write_retry(self):
        seen = []
        def wire(port, method, path, payload=None):
            seen.append(method)
            if method == 'POST':
                self.payload = payload
                return 204, b''
            stream = self.payload['streams'][0]
            result = {'stream': dict(stream['stream'], unknown='extra'), 'values': stream['values']}
            return 200, json.dumps({'data': {'result': [result]}}).encode()
        with patch.object(smoke, 'wire', side_effect=wire), patch.object(smoke.time, 'sleep') as sleep:
            with self.assertRaisesRegex(smoke.Blocked, 'observed UUID Loki'):
                smoke.seed(self.work, 'this-UUID')
        self.assertEqual(['POST', 'GET'], seen)
        sleep.assert_not_called()
        self.assertEqual(204, json.loads((self.work / 'loki-write-receipt.json').read_text())['status'])

    def test_pg_exact_cid_blocks_before_scoped_sql(self):
        scope = smoke.Scope.__new__(smoke.Scope)
        scope.data = {'postgres_container': 'dedicated-name'}
        with patch.object(smoke.ro, 'inspect_pg', return_value={'Id': 'other'}), patch.object(smoke.ro.Chain, 'pg') as pg:
            with self.assertRaises(smoke.Blocked):
                scope.pg('SELECT 1')
            pg.assert_not_called()

    def original_go(self, terminal='pass', returncode=0, name='TestLocalReadcheck'):
        proc = Mock(returncode=returncode)
        def popen(argv, **kwargs):
            self.assertIn('-race', argv)
            self.assertIn('-json', argv)
            self.assertIn('-count=1', argv)
            self.assertIn('^' + name + '$', argv)
            kwargs['stdout'].write((json.dumps({'Action': terminal, 'Test': name, 'Elapsed': 0.1}) + '\n').encode())
            return proc
        scope = Mock(processes=[])
        with patch.object(smoke.subprocess, 'Popen', side_effect=popen), patch.object(smoke, 'emit'):
            result = smoke.go_case(scope, self.work, './internal/connectors', name, {})
        self.assertEqual([proc], scope.processes)
        return result

    def test_original_go_root_terminal_pass_and_held_handle(self):
        self.assertEqual(['TestLocalReadcheck'], self.original_go()['terminal_tests'])

    def test_original_go_skip_is_not_pass(self):
        with self.assertRaises(smoke.Blocked):
            self.original_go(terminal='skip')

    def test_original_go_nonzero_is_not_pass(self):
        with self.assertRaises(smoke.Blocked):
            self.original_go(returncode=1)

    def test_source_drift_blocks_before_component_inspection(self):
        with patch.object(smoke, 'source_fingerprint', return_value='other'), patch.object(smoke.ro, 'inspect_pg') as inspect:
            with self.assertRaises(smoke.Blocked):
                smoke.snapshot({'source_tree_sha': smoke.GO})
            inspect.assert_not_called()

    def test_explicit_scope_constructor_rejects_unowned(self):
        with self.assertRaises(smoke.Blocked):
            smoke.Scope({'owner': 'different-owner'}, '00000000-0000-4000-8000-000000000000', self.work)


if __name__ == '__main__':
    unittest.main()
