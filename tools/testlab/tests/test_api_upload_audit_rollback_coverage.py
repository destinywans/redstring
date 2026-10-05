import contextlib
import copy
import hashlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import api_readonly_chain as shared
import api_upload_audit_rollback_coverage as upload


class UploadAuditTests(unittest.TestCase):
    def fixture(self):
        nonce, uid, actor = 'a'*32, 'b'*32, 'c'*32
        body = ('# inert upload audit fixture\n'+nonce+'\n').encode()
        filename = upload.multipart(nonce, body)[2]
        before = dict(uploads=[], revocations=[], audit=[])
        after = dict(uploads=[dict(id=uid, owner_id=actor, environment_id='test', kind='file',
            name=filename, size=len(body), mime='text/markdown', url='', bytes=len(body),
            sha256=hashlib.sha256(body).hexdigest())], revocations=[], audit=[dict(actor_id=actor,
            target=uid, outcome='success', kind='file', size=len(body), mime='text/markdown', environment_id='test')])
        value = dict(id=uid, kind='file', name=filename, mime='text/markdown', size=len(body),
                     url='/api/v1/uploads/'+uid+'/content')
        return nonce, actor, body, filename, before, after, value

    def test_private_module_does_not_mutate_shared_lifecycle(self):
        self.assertIsNot(upload.ro, shared)
        self.assertEqual(shared.PORTS, (18602, 18603))
        self.assertEqual(upload.ro.PORTS, (18726, 18727))

    def test_inventory_rejects_full_chain_and_external_origins(self):
        contract = upload.Contract(upload.ROOT)
        for method, path in (*upload.TARGETS, *upload.SUPPORT):
            contract.canonical(method, '/api/v1'+path.replace('{id}', 'a'*32))
        for method, path in [('POST', '/services'), ('POST', '/knowledge-sources'),
                             ('POST', '/incidents'), ('POST', '/uploads/'+'a'*32+'/revoke'),
                             ('GET', 'https://remote.invalid/api/v1/uploads/'+'a'*32+'/content')]:
            with self.assertRaises((upload.Blocked, AssertionError)):
                contract.canonical(method, path if path.startswith('https:') else '/api/v1'+path)

    def test_opt_in_precedes_lease_doctor_and_resources(self):
        with (mock.patch.object(sys, 'argv', ['upload']), mock.patch.object(upload, 'doctor') as doctor,
              mock.patch.object(upload.os, 'open') as opened, contextlib.redirect_stderr(io.StringIO())):
            with self.assertRaises(SystemExit): upload.main()
        doctor.assert_not_called(); opened.assert_not_called()

    def test_identity_and_every_pg_inspection_fail_closed(self):
        data = dict(source_tree_sha=upload.SOURCE_SHA, binary_sha256=upload.BINARY_SHA,
                    runtime_sha=upload.RUNTIME_SHA)
        upload.frozen_identity(data, upload.PG_CID)
        for key in data:
            with self.assertRaises(AssertionError):
                upload.frozen_identity(dict(data, **{key:'0'*64}), upload.PG_CID)
        with mock.patch.object(upload, '_inspect_pg', return_value={'Id':'other'}):
            with self.assertRaises(AssertionError): upload.ro.inspect_pg('owned')

    def test_source_drift_stops_http(self):
        chain = upload.Chain.__new__(upload.Chain)
        chain.frozen = mock.Mock(side_effect=upload.Blocked('source drift'))
        with mock.patch.object(upload.ro.Chain, 'call') as call:
            with self.assertRaises(upload.Blocked): chain.call('reader', 'POST', '/api/v1/uploads')
        call.assert_not_called()

    def test_fault_is_nonce_actor_action_env_limited_and_drops_exact_objects(self):
        name, create, drop = upload.fault_statements('a'*32, 'c'*32)
        self.assertIn("NEW.action='v3.upload.created' AND NEW.actor_id='"+'c'*32+"'", create)
        self.assertIn("NEW.changes->>'environment_id'='test'", create)
        self.assertIn('ON public.management_audit', create)
        self.assertEqual(create.count('CREATE FUNCTION'), 1)
        self.assertIn('DROP TRIGGER IF EXISTS '+name+' ON public.management_audit', drop)
        self.assertIn('DROP FUNCTION IF EXISTS public.'+name+'()', drop)
        for nonce, actor in [('bad', 'c'*32), ('a'*32, "x';DROP TABLE users;--")]:
            with self.assertRaises(AssertionError): upload.fault_statements(nonce, actor)

    def test_multipart_is_bounded_utf8_and_has_actual_bytes(self):
        nonce, _, body, filename, *_ = self.fixture()
        raw, media_type, actual = upload.multipart(nonce, body)
        self.assertEqual(actual, filename)
        self.assertIn(body, raw); self.assertIn('boundary=upload-audit-', media_type)
        self.assertIn(b'name="kind"\r\n\r\nfile', raw)
        for invalid in (b'', b'x'*4097, ('upload-audit-'+nonce).encode()):
            with self.assertRaises(AssertionError): upload.multipart(nonce, invalid)
        with self.assertRaises(UnicodeDecodeError): upload.multipart(nonce, b'\xff')

    def test_multipart_substitution_is_scoped_and_always_restored(self):
        nonce, *_ = self.fixture()
        chain = upload.Chain.__new__(upload.Chain)
        chain.nonce = nonce; chain.cookies = {'reader':'aiops_session=synthetic'}
        captured = []
        def original(base, method, path, body, headers, **options):
            captured.append((base, method, path, body, headers)); raise RuntimeError('transport failed')
        def call(*args, **kwargs):
            return upload.ro.transport(upload.ro.BASES[0], 'POST', '/api/v1/uploads', b'{}',
                {'Cookie':chain.cookies['reader'], 'X-AIOps-Environment':'test', 'Content-Type':'application/json'})
        chain.call = call
        with mock.patch.object(upload.ro, 'transport', side_effect=original) as transport:
            with self.assertRaises(RuntimeError): chain.upload(b'# inert\n', 503, 'failure')
            self.assertIs(upload.ro.transport, transport)
        self.assertIn(b'# inert\n', captured[0][3])
        self.assertTrue(captured[0][4]['Content-Type'].startswith('multipart/form-data;'))

    def test_recovery_rejects_missing_blob_wrong_hash_or_duplicate_audit(self):
        _, actor, body, filename, before, after, value = self.fixture()
        upload.verify_upload(value, filename, body)
        upload.verify_recovery(before, after, value['id'], actor, filename, body)
        for key, bad in [('sha256','0'*64), ('bytes',0), ('owner_id','d'*32), ('url','/wrong')]:
            altered = copy.deepcopy(after); altered['uploads'][0][key] = bad
            with self.assertRaises(AssertionError):
                upload.verify_recovery(before, altered, value['id'], actor, filename, body)
        duplicate = copy.deepcopy(after); duplicate['audit'].append(duplicate['audit'][0])
        with self.assertRaises(AssertionError):
            upload.verify_recovery(before, duplicate, value['id'], actor, filename, body)

    def test_download_requires_exact_bytes_and_no_store_headers(self):
        body = b'# inert\n'
        headers = {'Content-Type':'application/octet-stream', 'Content-Length':str(len(body)),
            'Content-Disposition':'attachment; filename="audit.md"', 'Cache-Control':'no-store',
            'Content-Security-Policy':'sandbox', 'X-Content-Type-Options':'nosniff'}
        upload.verify_content(200, body, headers, body)
        for status, raw, hs in [(200, b'wrong', headers), (503, body, headers),
                                (200, body, dict(headers, **{'Cache-Control':'public'}))]:
            with self.assertRaises(AssertionError): upload.verify_content(status, raw, hs, body)

    def test_filesystem_blob_residue_and_symlinks_are_observable(self):
        chain = upload.Chain.__new__(upload.Chain)
        with tempfile.TemporaryDirectory() as directory:
            chain.temp = Path(directory).resolve()
            before = chain.files(); self.assertEqual(before, {})
            (chain.temp/'blob').write_bytes(b'synthetic bytes')
            self.assertNotEqual(chain.files(), before)
            (chain.temp/'escape').symlink_to('/tmp')
            with self.assertRaises(AssertionError): chain.files()

    def test_failed_rollback_restores_fault_and_does_not_run_recovery(self):
        nonce, actor, _, _, before, after, _ = self.fixture()
        chain = upload.Chain.__new__(upload.Chain)
        chain.nonce = nonce; chain.users = {'reader':actor}
        for name in ('provision','start','seed','login','marker_case','install_fault','remove_fault'):
            setattr(chain, name, mock.Mock())
        chain.files = mock.Mock(return_value={})
        chain.ledger = mock.Mock(side_effect=[before, after])
        chain.upload = mock.Mock(return_value=({}, 'fixture.md'))
        with self.assertRaises(AssertionError): chain.run()
        chain.remove_fault.assert_called_once()
        self.assertEqual(chain.upload.call_count, 1)
        self.assertEqual(chain.upload.call_args.args[1:], (503, 'failure'))

    def test_cleanup_runs_parent_even_if_fault_removal_fails(self):
        chain = upload.Chain.__new__(upload.Chain)
        chain.remove_fault = mock.Mock(side_effect=RuntimeError('fault restoration failed'))
        with (mock.patch.object(upload.ro.Chain, 'cleanup') as cleanup,
              mock.patch.object(upload.socket, 'socket') as socket):
            socket.return_value.__enter__.return_value.connect_ex.return_value = 1
            with self.assertRaises(RuntimeError): chain.cleanup()
        cleanup.assert_called_once()

    def test_unknown_fault_commit_outcome_keeps_restoration_attempt(self):
        chain = upload.Chain.__new__(upload.Chain)
        chain.nonce = 'a'*32; chain.users = {'reader':'c'*32}; chain.fault_attempted = False
        chain.frozen = mock.Mock(); chain.owner = mock.Mock()
        chain.sql = mock.Mock(side_effect=upload.Blocked('SQL transport outcome unknown'))
        with self.assertRaises(upload.Blocked): chain.install_fault()
        self.assertTrue(chain.fault_attempted)

    def test_ledger_uses_actual_revocation_primary_key_and_hashes_bytes(self):
        migration = (upload.ROOT/'migrations/135_v3_upload_source_revoke.sql').read_text()
        self.assertIn('id text PRIMARY KEY', migration)
        chain = upload.Chain.__new__(upload.Chain)
        chain.frozen = mock.Mock(); chain.owner = mock.Mock()
        chain.sql = mock.Mock(return_value=json.dumps(dict(uploads=[], revocations=[], audit=[])))
        chain.ledger()
        query = chain.sql.call_args.args[0]
        self.assertNotIn('ORDER BY upload_id', query)
        self.assertIn("encode(sha256(content),'hex')", query)
        self.assertTrue(chain.sql.call_args.kwargs['read'])

    def test_fault_restore_shares_one_bounded_deadline_and_checks_absence(self):
        chain = upload.Chain.__new__(upload.Chain)
        chain.nonce = 'a'*32; chain.users = {'reader':'c'*32}; chain.fault_attempted = True
        chain.database = 'owned-db'; chain.db_user = 'owned-role'; chain.owner = mock.Mock()
        chain.pg = mock.Mock(side_effect=['', json.dumps(dict(trigger=False, function=False))])
        with mock.patch.object(upload.time, 'monotonic', return_value=100): chain.remove_fault()
        chain.owner.assert_called_once_with(deadline=104)
        self.assertEqual([call.kwargs['deadline'] for call in chain.pg.call_args_list], [104,104])
        self.assertFalse(chain.fault_attempted)


if __name__ == '__main__': unittest.main()
