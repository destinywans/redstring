"""Offline candidate safety checks. All processes, HTTP and sockets are mocked."""
import ast
import contextlib
import io
import inspect
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest.mock import Mock, patch
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_runbook_import_chain as candidate


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.data = dict(owner=candidate.base.OWNER, source_tree_sha=candidate.SOURCE,
                         binary_sha256=candidate.BINARY, binary='/not-executed/formal-aiops')
        self.run_id = str(uuid.uuid4())
        self.output = contextlib.redirect_stdout(io.StringIO())
        self.output.__enter__()
        self.addCleanup(self.output.__exit__, None, None, None)

    def chain(self):
        with patch.object(candidate.base, 'Contract') as contract:
            contract.return_value.canonical.side_effect = lambda method, path: (path.split('?')[0], 'main')
            return candidate.Chain(self.root, self.data, self.run_id)

    def test_private_module_rebind_does_not_mutate_shared_origins(self):
        self.assertIsNot(candidate.base, shared)
        self.assertEqual(shared.PORTS, (18602, 18603))
        self.assertEqual(shared.BASES, ('http://127.0.0.1:18602', 'http://127.0.0.1:18603'))
        self.assertEqual(candidate.base.PORTS, (18620, 18621))
        self.assertEqual(candidate.base.BASES, candidate.BASES)
        self.assertEqual(candidate.base.CONTROL, 'http://127.0.0.1:18560')

    def test_uuid_resource_and_private_evidence_identity(self):
        chain = self.chain()
        nonce = uuid.UUID(self.run_id).hex
        self.assertEqual(chain.database, 'aiops_testlab_runbook_' + nonce)
        self.assertEqual(chain.db_user, 'runbook_' + nonce)
        self.assertIn('/api-runbook-import/' + self.run_id, str(chain.work))
        self.assertEqual(chain.state['ports'], [18620, 18621])
        self.assertEqual((chain.work / 'credentials.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(chain.pg_application('api', 1), 'runbook-api-' + nonce + '-1')
        with self.assertRaises(candidate.base.Blocked):
            chain.pg_application('api', 2)

    def test_new_lock_is_independent_and_reentry_fails_closed(self):
        with candidate.exclusive(self.root):
            with self.assertRaises(candidate.base.Blocked):
                with candidate.exclusive(self.root):
                    self.fail('second group acquired same ports')
        self.assertFalse((self.root / '.local/testlab/api-readonly.lock').exists())

    def test_native_start_binds_both_database_and_origin_to_this_group(self):
        chain = self.chain()
        process = Mock(pid=12345)
        process.poll.return_value = None
        with patch.object(chain, 'frozen'), patch.object(candidate.base.subprocess, 'Popen', return_value=process) as popen, \
             patch.object(candidate.base.socket, 'socket') as sockets, \
             patch.object(candidate.base, 'transport', return_value=(200, b'{}', {})) as wire, \
             patch.object(candidate.base, '_process_port', return_value=True):
            sockets.return_value.__enter__.return_value.connect_ex.return_value = 1
            chain.start()
        self.assertEqual(popen.call_count, 2)
        for index, call in enumerate(popen.call_args_list):
            env = call.kwargs['env']
            self.assertEqual(env['AIOPS_ADDR'], '127.0.0.1:' + str(candidate.PORTS[index]))
            self.assertEqual(env['AIOPS_PUBLIC_ORIGIN'], candidate.BASES[index])
            self.assertIn('/' + chain.database + '?', env['DATABASE_URL'])
            self.assertIn(chain.pg_application('api', index), env['DATABASE_URL'])
            self.assertEqual(env['EMBEDDING_BASE_URL'], 'disabled://runbook-no-model')
            self.assertEqual(call.args[0], [self.data['binary']])
        self.assertEqual([c.args[0] for c in wire.call_args_list], list(candidate.BASES))

    def test_doctor_keeps_original_guard_but_requires_reviewed_source_binary(self):
        for changes in ({}, {'source_tree_sha': 'changed'}, {'binary_sha256': 'changed'}):
            with patch.object(candidate.base, 'doctor', return_value=dict(self.data, **changes)) as guard:
                if changes:
                    with self.assertRaises(candidate.base.Blocked):
                        candidate.doctor(self.root)
                else:
                    self.assertEqual(candidate.doctor(self.root), self.data)
                guard.assert_called_once_with(self.root)

    def test_raw_multipart_limit_route_and_no_second_payload(self):
        chain = self.chain()
        invalid = [dict(raw_body=b'x' * (candidate.RAW_LIMIT + 1), content_type='multipart/form-data; boundary=x'),
                   dict(raw_body=b'', content_type='multipart/form-data; boundary=x'),
                   dict(raw_body=b'x', body={}, content_type='multipart/form-data; boundary=x'),
                   dict(raw_body=b'x', content_type='multipart/form-data; boundary=x\r\nSecret: value')]
        with patch.object(candidate.base, 'transport') as transport:
            for value in invalid:
                with self.subTest(value=list(value)), self.assertRaises(candidate.base.Blocked):
                    chain.call('author', 'POST', '/api/v1/uploads', **value)
            with self.assertRaises(candidate.base.Blocked):
                chain.call('author', 'POST', '/api/v1/runbooks', raw_body=b'x', content_type='multipart/form-data; boundary=x')
            transport.assert_not_called()

    def test_multipart_keeps_nonempty_markdown_and_bounded_filename(self):
        raw, mime = candidate.multipart('owned-runbook.md', b'# Untrusted\n\n```sh\necho inert\n```\n')
        self.assertIn(b'name="kind"\r\n\r\nfile\r\n', raw)
        self.assertIn(b'filename="owned-runbook.md"', raw)
        self.assertIn(b'echo inert', raw)
        self.assertLess(len(raw), candidate.RAW_LIMIT)
        self.assertTrue(mime.startswith('multipart/form-data; boundary='))
        for filename, body in [('other.md', b'x'), ('owned-runbook.md', b''), ('owned-runbook.md', b'x' * 16001)]:
            with self.assertRaises(candidate.base.Blocked):
                candidate.multipart(filename, body)

    def test_raw_and_download_calls_use_private_origin_and_real_status_evidence(self):
        chain = self.chain()
        chain.cookies['author'] = 'aiops_session=synthetic'
        raw, mime = candidate.multipart('owned-runbook.md', b'# Inert')
        with patch.object(candidate.base, 'transport', return_value=(201, b'{"id":"actual"}', {})) as wire:
            value, _ = chain.call('author', 'POST', '/api/v1/uploads', raw_body=raw, content_type=mime, want=201, index=1)
        self.assertEqual(value['id'], 'actual')
        self.assertEqual(wire.call_args.args[0], candidate.BASES[1])
        self.assertEqual(wire.call_args.args[3], raw)
        self.assertEqual(wire.call_args.args[4]['Origin'], candidate.BASES[1])
        self.assertEqual(wire.call_args.args[4]['Content-Type'], mime)
        record = json.loads((chain.work / 'interfaces.jsonl').read_text())
        self.assertEqual(record['actual']['status'], 201)
        self.assertTrue(record['registration_evidence'])
        with patch.object(candidate.base, 'transport', return_value=(200, b'# Inert', {})):
            content, _ = chain.call('author', 'GET', '/api/v1/uploads/' + 'a' * 32 + '/content', binary=True)
        self.assertEqual(content, b'# Inert')

    def test_rejected_response_is_never_positive_registration(self):
        chain = self.chain()
        with patch.object(candidate.base, 'transport', return_value=(409, b'{"error":{"code":"invalid_state"}}', {})):
            chain.call('author', 'POST', '/api/v1/runbooks/id/submit', {}, want=409, code='invalid_state', scenario='idempotency')
        row = json.loads((chain.work / 'interfaces.jsonl').read_text())
        self.assertEqual(row['status'], 'passed')
        self.assertFalse(row['registration_evidence'])
        self.assertEqual(row['scenario'], 'idempotency')

    def test_sql_seed_contains_only_identities_and_scoped_grants(self):
        chain = self.chain()
        chain.sql = Mock(side_effect=['', '{"users":5,"global_grants":0}'])
        chain.seed()
        statements = chain.sql.call_args_list[0].args[0]
        for forbidden in ['INSERT INTO v3_uploads', 'INSERT INTO knowledge_', 'INSERT INTO v3_runbooks',
                          'INSERT INTO management_audit', 'INSERT INTO v3_runbook_reviews', 'INSERT INTO v3_proposal']:
            self.assertNotIn(forbidden, statements)
        self.assertIn("'knowledge','manage','environment','test'", statements)
        self.assertIn("'knowledge','manage','environment','staging'", statements)
        self.assertNotIn("'global'", statements)

    def test_no_side_effect_check_detects_changed_state_despite_same_row_count(self):
        chain = self.chain()
        chain.snapshot = Mock(side_effect=[dict(documents=1, document_states=[{'lifecycle': 'draft'}]),
                                          dict(documents=1, document_states=[{'lifecycle': 'review'}])])
        operation = Mock()
        with self.assertRaises(AssertionError):
            chain.unchanged('rejected', operation)
        operation.assert_called_once()

    def test_every_business_state_read_emits_one_unique_runner_result_key(self):
        chain = self.chain()
        chain.ids = dict(upload='a' * 32, digest='b' * 64)
        function = ast.parse(textwrap.dedent(inspect.getsource(candidate.Chain.business)))
        calls = sorted((node for node in ast.walk(function) if isinstance(node, ast.Call) and
                        isinstance(node.func, ast.Attribute) and isinstance(node.func.value, ast.Name) and
                        node.func.value.id == 'self' and node.func.attr == 'state_read'), key=lambda node: node.lineno)
        self.assertEqual(len(calls), 6)
        frames = []
        for node in calls:
            args = [ast.literal_eval(value) for value in node.args[1:]]
            kwargs = {value.arg: ast.literal_eval(value.value) for value in node.keywords}
            lifecycle, reviews, submissions, *preview = args
            expected = dict(lifecycle=lifecycle, version=1, owner=chain.users['author'],
                            source=chain.ids['upload'], digest=chain.ids['digest'], revisions=1,
                            reviews=reviews, submissions=submissions, previews=preview[0] if preview else 0)
            chain.sql = Mock(return_value=json.dumps(expected))
            with patch.object(candidate.base, 'emit') as emit:
                chain.state_read('c' * 32, *args, **kwargs)
            emit.assert_called_once()
            self.assertEqual(emit.call_args.args[1:], ('passed', expected))
            frames.append({'module': candidate.MODULE, 'name': emit.call_args.args[0]})
        # Same rule as Runner testlab_json: each (module, id-or-name) is one
        # immutable result. Original draft/published lifecycle-only names fail.
        keys = [(row['module'], row.get('id', row['name'])) for row in frames]
        self.assertEqual(len(keys), len(set(keys)), 'business state reads reuse a terminal Runner result key')

    def test_audit_fault_constraint_removed_even_when_http_assertion_fails(self):
        chain = self.chain()
        markdown = ('# Owned imported recovery notes\n\nPrivate synthetic input ' + chain.nonce +
                    '\n\n```sh\necho review-only-never-execute\n```\n').encode()
        upload = dict(id='a' * 32, kind='file', name='owned-runbook.md', size=len(markdown),
                      url='/api/v1/uploads/' + 'a' * 32 + '/content')
        draft = dict(source_sha256=candidate.hashlib.sha256(markdown).hexdigest(), draft_mode='source_outline',
                     steps=[dict(n=1, kind='check', title='Review imported Markdown', body=markdown.decode(), readonly=True)])
        def http(user, method, path, body=None, **kwargs):
            if kwargs.get('scenario') == 'failure':
                raise AssertionError('injected response/assertion transport failure')
            if kwargs.get('scenario', 'normal') != 'normal':
                return {}, {}
            if path == '/api/v1/uploads': return upload, {}
            if path.endswith('/content'): return markdown, {}
            if path.startswith('/api/v1/runbooks/draft?'): return draft, {}
            if path == '/api/v1/runbooks': return {'id': 'b' * 32}, {}
            if path.endswith('/submit'): return {'status': 'review'}, {}
            if path.endswith('/reviews'): return {'status': 'draft'}, {}
            self.fail('unexpected offline rehearsal operation')
        chain.call = Mock(side_effect=http)
        chain.unchanged = lambda name, operation: operation()
        chain.state_read = Mock()
        persisted_upload = dict(owner=chain.users['author'], environment='test', bytes=len(markdown),
                                digest=candidate.hashlib.sha256(markdown).hexdigest(), audits=1)
        chain.sql = Mock(side_effect=lambda statement, **kwargs: json.dumps(persisted_upload) if kwargs.get('read') else '')
        with self.assertRaisesRegex(AssertionError, 'injected response'):
            chain.business()
        ddl = [c.args[0] for c in chain.sql.call_args_list if c.args[0].startswith('ALTER TABLE')]
        self.assertEqual(len(ddl), 2)
        self.assertIn('ADD CONSTRAINT "runbook_audit_fault_' + chain.nonce + '"', ddl[0])
        self.assertIn('DROP CONSTRAINT "runbook_audit_fault_' + chain.nonce + '"', ddl[1])
        self.assertNotIn('INSERT', ''.join(ddl))

    def test_recovery_never_adopts_pids_and_rejects_different_resource_tags(self):
        original = self.chain()
        original.state['pids'] = [999999]
        candidate.base._private(original.work / 'state.json', original.state)
        with patch.object(candidate.socket, 'socket') as sockets:
            sockets.return_value.__enter__.return_value.connect_ex.return_value = 1
            recovered = candidate.Chain.from_cleanup_record(self.root, self.data, self.run_id)
        self.assertEqual(recovered.processes, [])
        self.assertEqual(recovered.pg_application('api', 0), original.pg_application('api', 0))
        candidate.base._private(original.work / 'state.json', dict(original.state, database='aiops_testlab_app'))
        with self.assertRaises(candidate.base.Blocked):
            candidate.Chain.from_cleanup_record(self.root, self.data, self.run_id)

    def test_cleanup_inheritance_never_forces_pg_backend_termination(self):
        chain = self.chain()
        chain.role_attempted = False
        child = Mock(pid=10001)
        child.poll.return_value = 0
        chain.processes = [child]
        with patch.object(chain, 'pg') as sql:
            chain.cleanup()
        sql.assert_not_called()
        child.terminate.assert_not_called()
        receipt = json.loads((chain.work / 'cleanup.json').read_text())
        self.assertTrue(receipt['owned_processes_exited'])
        self.assertEqual(receipt['owned_process_ids'], [10001])


if __name__ == '__main__':
    unittest.main()
