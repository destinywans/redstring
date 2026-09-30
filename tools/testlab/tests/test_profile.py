"""Offline only: Docker, SQL, supervisor, listeners and HMAC transport mocked."""
import copy
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import uuid
import re

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
spec = importlib.util.spec_from_file_location('testlab_profile', Path(__file__).resolve().parents[1] / 'profile.py')
profile = importlib.util.module_from_spec(spec); spec.loader.exec_module(profile)
native_process_port = profile.process_port
SECOND_BEFORE = '8ac896b5-70c4-43c8-a934-309ce357e31d'
SECOND_SHA = 'd6f7bd11d4cf06e4b14ab56df83c4ef737e71c6c'
SECOND_SOURCE = '15125d6ecd371864b894edfd91b7ca4944bf69dc305359d0e385a879f8cc090a'
SECOND_OID = 9270802


class ProfileSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve(); self.run = str(uuid.uuid4())
        work = self.root / '.local/testlab/environments' / profile.OWNER
        (work / 'backend').mkdir(parents=True); (work / 'pgdata').mkdir()
        binary = work / 'backend/aiops'; binary.write_bytes(b'owned old source binary')
        self.environment = dict(owner=profile.OWNER, container='aiops-' + profile.OWNER + '-pg', work=str(work),
            run_id='wan-138-20260930', env={key: 'postgres://testlab:synthetic-private@127.0.0.1:15461/' + db + '?sslmode=disable'
                                        for key, db in profile.DSNS.items()})
        self.fixture = dict(owner=profile.OWNER, postgres_container=self.environment['container'], work=str(work / 'backend'),
            binary=str(binary), source_tree_sha='a' * 64, database_url=self.environment['env']['TESTLAB_APP_DSN'],
            base_urls=['http://127.0.0.1:18561', 'http://127.0.0.1:18562'], environment='test', other_environment='staging',
            control_secret='synthetic-control-secret', supervisor_pid=100, admin={'password': 'synthetic-admin-secret'})
        self.env_path = self.root / '.local/testlab/environment.json'
        self.fixture_path = self.root / '.local/testlab/backend-fixture.json'
        profile.env._private(self.env_path, self.environment); profile.env._private(self.fixture_path, self.fixture)
        self.ledger = self.root / '.local/testlab/state.sqlite3'
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('CREATE TABLE runs(id TEXT PRIMARY KEY,data TEXT NOT NULL)')
            connection.execute('INSERT INTO runs VALUES(?,?)', (profile.BEFORE, json.dumps({'status': 'failed', 'ended_at': 'finished', 'sha': 'old-red'})))
        os.chmod(self.ledger, 0o600)
        self.pg_info = dict(Id='owned-container-id', Name='/' + self.environment['container'], Image='sha256:owned-image',
            Config={'Image': profile.env.PG_IMAGE, 'Labels': {'aiops.testlab.owner': profile.OWNER}}, State={'Running': True},
            Mounts=[{'Type': 'bind', 'Source': str(work / 'pgdata'), 'Destination': '/var/lib/postgresql/data'}],
            NetworkSettings={'Ports': {'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '15461'}]}})
        self.running = True
        self.catalog = dict(canonical={'oid': 100, 'owner': 'testlab', 'tag': None}, archive=None, fresh=None, connections=[], pending_ddl=0)
        self.schemas = {'canonical': {'user_tables': 93, 'extensions': ['pgcrypto', 'vector']}}
        self.commands, self.fail_after = [], None
        self.enterContext(patch.object(profile, 'source_fingerprint', return_value='c' * 64))
        self.enterContext(patch.object(profile, 'inspect_pg', side_effect=lambda *args: self.pg_info))
        self.enterContext(patch.object(profile, 'process_port', return_value=True))
        self.enterContext(patch.object(profile, 'listeners', side_effect=lambda **kwargs: list(profile.PORTS) if self.running else []))
        self.enterContext(patch.object(profile, 'client_pids', side_effect=lambda **kwargs: {101, 102} if self.running else set()))
        self.enterContext(patch.object(profile, 'api_listener_pids', side_effect=lambda **kwargs: {101, 102} if self.running else set()))
        self.stop = Mock(side_effect=self.shutdown)
        self.enterContext(patch.object(profile.Rotation, 'control_request', side_effect=self.control))
        self.sql = self.enterContext(patch.object(profile.env, '_cmd', side_effect=self.command))
        self.rotation = self.make()

    def make(self):
        return profile.Rotation(self.root, profile.OWNER, self.run, profile.BEFORE)

    def attestation(self, url):
        nonce = url.rsplit('=', 1)[1]
        identity = dict(owner=profile.OWNER, pid=100, api_pids=[101, 102], ready=True, source_tree_sha=self.fixture['source_tree_sha'])
        return dict(nonce=nonce, identity=identity, proof=profile.backend.control_proof(self.fixture['control_secret'], nonce, identity))

    def second_context(self):
        self.fixture['source_tree_sha'] = SECOND_SOURCE
        profile.env._private(self.fixture_path, self.fixture)
        self.catalog['canonical']['oid'] = SECOND_OID
        self.catalog['canonical']['tag'] = 'prior approved fresh database marker'
        evidence = dict(status='interrupted', ended_at='2026-09-30T21:14:05.906231+00:00',
            started_at='2026-09-30T20:59:40.504994+00:00', selected_suite='performance',
            sha=SECOND_SHA, source_tree_sha=SECOND_SOURCE, pid=4118)
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('CREATE TABLE cases(id TEXT PRIMARY KEY,run_id TEXT,data TEXT NOT NULL)')
            connection.execute('INSERT INTO runs VALUES(?,?)', (SECOND_BEFORE, json.dumps(evidence)))
            connection.execute('INSERT INTO cases VALUES(?,?,?)', ('second-rps25-red', SECOND_BEFORE,
                json.dumps({'name': 'perf.rps25', 'status': 'failed'})))
        identity = dict(owner=profile.OWNER, pid=100, api_pids=[101, 102], ready=True,
            failed=False, source_tree_sha=SECOND_SOURCE)
        freeze = dict(run_id=SECOND_BEFORE, git_sha=SECOND_SHA, source_tree_sha=SECOND_SOURCE,
            run_started_at=evidence['started_at'], runner_pid=4118, fresh_hmac_identity=identity)
        stopped = dict(run_id=SECOND_BEFORE, source_tree_sha=SECOND_SOURCE, runner_pid=4118,
            at=1790802844.500179, fresh_hmac_identity=identity)
        directory = self.ledger.parent
        profile.env._private(directory / 'performance-run-8ac-freeze.json', freeze)
        profile.env._private(directory / 'performance-stop-8ac.json', stopped)
        observed = dict(run_id=SECOND_BEFORE, run_status='interrupted', run_ended_at=evidence['ended_at'],
            observed_at='2026-09-30T21:32:02.782537+00:00', observed_source_tree_sha=SECOND_SOURCE,
            original_ledger_end_source_present=False, fresh_hmac_identity=identity,
            binary_sha256=profile.digest(self.fixture['binary']),
            freeze_evidence_sha256=profile.digest(directory / 'performance-run-8ac-freeze.json'),
            stop_evidence_sha256=profile.digest(directory / 'performance-stop-8ac.json'))
        profile.env._private(directory / 'performance-post-stop-8ac-source.json', observed)
        return evidence

    def make_second(self, run=None):
        return profile.Rotation(self.root, profile.OWNER, run or self.run, SECOND_BEFORE)

    def test_second_approved_context_plan_apply_and_same_uuid_replay_preserve_old_archive(self):
        self.second_context()
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second()
            planned = self.rotation.plan()
            self.assertTrue(planned['before_evidence']['original_ledger_source_end_missing'])
            self.assertEqual(planned['before_evidence']['source_post_stop'], SECOND_SOURCE)
            self.assertNotIn('source_tree_sha_end', planned['before_evidence'])
            self.assertEqual(planned['catalog']['canonical']['oid'], SECOND_OID)
            self.assertEqual(self.commands, []); self.stop.assert_not_called()
            self.assertTrue(self.rotation.apply()['applied'])
            before = list(self.commands)
            self.assertEqual(self.catalog['archive']['oid'], SECOND_OID)
            replay = self.make_second().apply()
            self.assertTrue(replay['already_complete']); self.assertFalse(replay['applied'])
            self.assertEqual(self.commands, before); self.assertEqual(len(before), 6)

    def test_second_context_another_operation_cannot_archive_later_fresh_canonical(self):
        self.second_context()
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second(); self.rotation.apply()
            count = self.stop.call_count; statements = list(self.commands)
            # A new UUID names different absent archive/staging databases; it
            # must reject the later canonical OID, not borrow the old catalog.
            later = copy.deepcopy(self.catalog); later['archive'] = None; later['fresh'] = None
            other = self.make_second(str(uuid.uuid4()))
            with patch.object(other, 'catalog', return_value=later), self.assertRaisesRegex(profile.Blocked, 'OID differs'):
                other.plan()
            self.assertEqual(self.stop.call_count, count); self.assertEqual(self.commands, statements)

    def test_second_context_exact_terminal_source_suite_sha_failure_and_oid_are_required(self):
        good = self.second_context()
        changes = [('status', 'failed'), ('ended_at', None), ('selected_suite', 'entry'),
            ('sha', 'other'), ('source_tree_sha', 'a' * 64), ('source_tree_sha_end', 'b' * 64),
            ('source_tree_sha_end', None)]
        for key, value in changes:
            bad = {**good, key: value}
            with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
                connection.execute('UPDATE runs SET data=? WHERE id=?', (json.dumps(bad), SECOND_BEFORE))
            with self.subTest(key=key, value=value), patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises(profile.Blocked):
                self.make_second()
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('UPDATE runs SET data=? WHERE id=?', (json.dumps(good), SECOND_BEFORE))
            connection.execute('DELETE FROM cases WHERE run_id=?', (SECOND_BEFORE,))
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises(profile.Blocked): self.make_second()
        self.assertEqual(self.commands, []); self.stop.assert_not_called()

    def test_second_context_canonical_oid_and_registered_binary_source_are_bound(self):
        self.second_context()
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second()
            self.catalog['canonical']['oid'] = SECOND_OID + 1
            with self.assertRaises(profile.Blocked): self.rotation.plan()
            self.catalog['canonical']['oid'] = SECOND_OID
            self.fixture['source_tree_sha'] = 'a' * 64
            profile.env._private(self.fixture_path, self.fixture)
            with self.assertRaises(profile.Blocked): self.make_second()
        self.assertEqual(self.commands, []); self.stop.assert_not_called()

    def test_second_context_external_files_missing_malformed_symlink_or_public_are_rejected(self):
        self.second_context()
        paths = [self.ledger.parent / name for name in ('performance-run-8ac-freeze.json',
            'performance-stop-8ac.json', 'performance-post-stop-8ac-source.json')]
        for path in paths:
            original = path.read_bytes()
            for attack in ('missing', 'malformed', 'symlink', 'public'):
                with self.subTest(file=path.name, attack=attack):
                    if attack == 'missing': path.unlink()
                    elif attack == 'malformed': path.write_text('{invalid')
                    elif attack == 'public': os.chmod(path, 0o644)
                    else:
                        target = path.with_name(path.name + '.target'); target.write_bytes(original)
                        os.chmod(target, 0o600); path.unlink(); path.symlink_to(target)
                    with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises((profile.Blocked, ValueError, OSError)):
                        self.make_second()
                    if path.is_symlink(): path.unlink(); target.unlink()
                    path.write_bytes(original); os.chmod(path, 0o600)
        self.assertEqual(self.commands, []); self.stop.assert_not_called()

    def test_second_context_external_crosslinks_times_and_process_identity_are_exact(self):
        self.second_context()
        names = ('performance-run-8ac-freeze.json', 'performance-stop-8ac.json', 'performance-post-stop-8ac-source.json')
        paths = [self.ledger.parent / name for name in names]
        original = [json.loads(path.read_text()) for path in paths]
        attacks = [(0, 'run_id', str(uuid.uuid4())), (0, 'git_sha', 'foreign'),
            (0, 'source_tree_sha', 'a' * 64), (0, 'run_started_at', '2026-09-30T20:00:00+00:00'),
            (0, 'runner_pid', 999), (1, 'source_tree_sha', 'a' * 64), (1, 'at', 1790802944),
            (1, 'at', float('nan')), (1, 'at', True), (1, 'runner_pid', 999),
            (2, 'observed_source_tree_sha', 'a' * 64), (2, 'observed_at', '2026-09-30T21:00:00+00:00'),
            (2, 'observed_at', '2026-09-30T21:32:02'), (2, 'run_ended_at', 'foreign'),
            (2, 'run_status', 'failed'), (2, 'freeze_evidence_sha256', 'a' * 64),
            (2, 'stop_evidence_sha256', 'a' * 64), (2, 'binary_sha256', 'a' * 64),
            (2, 'original_ledger_end_source_present', True), (2, 'original_ledger_end_source_present', None)]
        for index in range(3):
            for field, value in [('owner', 'foreign'), ('pid', 999), ('api_pids', [101, 999]),
                    ('api_pids', [101, 101]), ('api_pids', [{}, {}]), ('ready', False),
                    ('failed', True), ('source_tree_sha', 'a' * 64)]:
                identity = {**original[index]['fresh_hmac_identity'], field: value}
                attacks.append((index, 'fresh_hmac_identity', identity))
        for index, field, value in attacks:
            rows = copy.deepcopy(original); rows[index][field] = value
            for path, row in zip(paths[:2], rows[:2]): profile.env._private(path, row)
            # Update valid crosslinks to reach the semantic guard, except when
            # the crosslink itself is the attack under test.
            if index != 2 or field not in ('freeze_evidence_sha256', 'stop_evidence_sha256'):
                rows[2]['freeze_evidence_sha256'] = profile.digest(paths[0])
                rows[2]['stop_evidence_sha256'] = profile.digest(paths[1])
            profile.env._private(paths[2], rows[2])
            with self.subTest(file=names[index], field=field), patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises(profile.Blocked):
                self.make_second()
        self.assertEqual(self.commands, []); self.stop.assert_not_called()

    def test_second_context_failed_case_must_belong_to_exact_run_and_completed_replay_accepts_later_source(self):
        self.second_context()
        for row in ({'name': 'perf.rps25', 'status': 'passed'}, {'name': 'perf.rps50', 'status': 'failed'},
                    {'name': 'perf.rps25', 'status': 'blocked'}):
            with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
                connection.execute('UPDATE cases SET data=?', (json.dumps(row),))
            with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises(profile.Blocked): self.make_second()
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('UPDATE cases SET data=?,run_id=?',
                (json.dumps({'name': 'perf.rps25', 'status': 'failed'}), profile.BEFORE))
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), self.assertRaises(profile.Blocked): self.make_second()
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('UPDATE cases SET run_id=?', (SECOND_BEFORE,))
        legacy_keys = set(self.make().plan()['before_evidence'])
        self.assertEqual(legacy_keys, {'status', 'sha', 'started_at', 'ended_at'})
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second(); self.rotation.apply()
        statements = list(self.commands)
        self.fixture['source_tree_sha'] = 'b' * 64; self.fixture['supervisor_pid'] = 500
        Path(self.fixture['binary']).write_bytes(b'new later source binary')
        profile.env._private(self.fixture_path, self.fixture)
        with patch.object(profile, 'source_fingerprint', return_value='b' * 64):
            result = self.make_second().apply()
        self.assertTrue(result['already_complete']); self.assertEqual(self.commands, statements)

    def test_second_context_matching_ledger_end_is_optional_but_conflicting_end_and_receipt_changes_block(self):
        evidence = self.second_context()
        evidence['source_tree_sha_end'] = SECOND_SOURCE
        with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
            connection.execute('UPDATE runs SET data=? WHERE id=?', (json.dumps(evidence), SECOND_BEFORE))
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second()
            self.assertFalse(self.rotation.evidence['original_ledger_source_end_missing'])
            self.assertEqual(self.rotation.evidence['ledger_source_tree_sha_end'], SECOND_SOURCE)
            self.rotation.apply()
            before = list(self.commands)
            path = self.ledger.parent / 'performance-post-stop-8ac-source.json'
            changed = json.loads(path.read_text()); changed['boundary'] = 'changed receipt'
            profile.env._private(path, changed)
            with self.assertRaisesRegex(profile.Blocked, 'receipt changed'): self.make_second()
        self.assertEqual(self.commands, before)

    def test_second_context_partial_resume_requires_exact_source_and_preserves_legacy_journal(self):
        self.second_context()
        legacy = self.ledger.parent / 'environments' / profile.OWNER / 'profile' / str(uuid.uuid4()) / 'journal.json'
        profile.env._private(legacy, {'schema': 1, 'before_evidence_run': profile.BEFORE, 'stage': 'complete'})
        original = legacy.read_bytes()
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE):
            self.rotation = self.make_second(); self.fail_after = 'create-fresh'
            with self.assertRaisesRegex(profile.Blocked, 'CREATE receipt'): self.rotation.apply()
        statements = list(self.commands)
        with patch.object(profile, 'source_fingerprint', return_value='b' * 64), self.assertRaisesRegex(profile.Blocked, 'current and registered'):
            self.make_second()
        self.assertEqual(self.commands, statements); self.assertEqual(legacy.read_bytes(), original)

    def test_receipt_atomic_replace_between_parse_and_hash_blocks_before_stop(self):
        self.second_context(); path = self.ledger.parent / 'performance-post-stop-8ac-source.json'
        native = json.loads; changed = [False]
        def parse(raw, *args, **kwargs):
            row = native(raw, *args, **kwargs)
            if isinstance(row, dict) and 'observed_source_tree_sha' in row and not changed[0]:
                changed[0] = True
                replacement = path.with_name('receipt-replacement.json')
                replacement.write_text(json.dumps({**row, 'observed_source_tree_sha': 'b' * 64}))
                os.chmod(replacement, 0o600); os.replace(replacement, path)
            return row
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), \
             patch.object(profile.json, 'loads', side_effect=parse), self.assertRaises(profile.Blocked):
            self.make_second()
        self.assertTrue(changed[0]); self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_receipt_first_file_replace_after_read_blocks_even_with_identical_bytes_and_valid_third(self):
        self.second_context(); path = self.ledger.parent / 'performance-run-8ac-freeze.json'
        raw = path.read_bytes(); native = json.loads; changed = [False]
        def parse(value, *args, **kwargs):
            row = native(value, *args, **kwargs)
            if isinstance(row, dict) and 'run_started_at' in row and not changed[0]:
                changed[0] = True
                replacement = path.with_name('receipt-replacement.json'); replacement.write_bytes(raw)
                os.chmod(replacement, 0o600); os.replace(replacement, path)
            return row
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), \
             patch.object(profile.json, 'loads', side_effect=parse), self.assertRaises(profile.Blocked):
            self.make_second()
        self.assertTrue(changed[0]); self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_receipt_in_place_rewrite_during_capture_blocks_even_with_identical_bytes(self):
        self.second_context(); path = self.ledger.parent / 'performance-post-stop-8ac-source.json'
        raw = path.read_bytes(); original_stat = path.stat(); native = json.loads; changed = [False]
        def parse(value, *args, **kwargs):
            row = native(value, *args, **kwargs)
            if isinstance(row, dict) and 'observed_source_tree_sha' in row and not changed[0]:
                changed[0] = True; path.write_bytes(raw)
                os.utime(path, ns=(original_stat.st_atime_ns, original_stat.st_mtime_ns + 1000000000))
            return row
        with patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), \
             patch.object(profile.json, 'loads', side_effect=parse), self.assertRaises(profile.Blocked):
            self.make_second()
        self.assertTrue(changed[0]); self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_receipt_symlink_or_permission_swap_between_lstat_and_open_blocks(self):
        self.second_context(); path = self.ledger.parent / 'performance-post-stop-8ac-source.json'
        raw = path.read_bytes(); native = Path.lstat
        for attack in ('symlink', 'permission'):
            changed = [False]; target = path.with_name('receipt-target.json')
            def inspect(candidate, *args, **kwargs):
                info = native(candidate, *args, **kwargs)
                if candidate == path and not changed[0]:
                    changed[0] = True
                    if attack == 'permission': os.chmod(path, 0o644)
                    else:
                        target.write_bytes(raw)
                        os.chmod(target, 0o600); path.unlink(); path.symlink_to(target)
                return info
            with self.subTest(attack=attack), patch.object(profile, 'source_fingerprint', return_value=SECOND_SOURCE), \
                 patch.object(Path, 'lstat', inspect), self.assertRaises(profile.Blocked):
                self.make_second()
            self.assertTrue(changed[0])
            if path.is_symlink(): path.unlink(); target.unlink()
            path.write_bytes(raw); os.chmod(path, 0o600)
        self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def shutdown(self, data):
        self.assertEqual(data['owner'], profile.OWNER); self.running = False

    def control(self, method, path):
        if method == 'GET': return 200, json.dumps(self.attestation(path)).encode()
        self.stop(self.fixture)
        return 202, b''

    def key(self, name):
        return {'aiops_testlab_app': 'canonical', self.rotation.archive: 'archive', self.rotation.fresh: 'fresh'}[name]

    def command(self, argv, *, input, timeout):
        if input.startswith('BEGIN READ ONLY; '):
            if "'connections'" in input: return json.dumps(self.catalog)
            if "'user_tables'" in input: return json.dumps(self.schemas[self.key(argv[-1])])
            raise AssertionError('unrecognized mock readonly SQL')
        self.commands.append(input)
        if input.startswith('ALTER DATABASE'):
            source, target = re.fullmatch(r'ALTER DATABASE "([^"]+)" RENAME TO "([^"]+)"', input).groups()
            source, target = self.key(source), self.key(target)
            self.catalog[target] = self.catalog[source]; self.catalog[source] = None
            self.schemas[target] = self.schemas.pop(source)
            action = 'rename-' + target
        elif input.startswith('CREATE DATABASE'):
            self.catalog['fresh'] = {'oid': 200, 'owner': 'testlab', 'tag': None}
            self.schemas['fresh'] = {'user_tables': 0, 'extensions': []}; action = 'create-fresh'
        elif input.startswith('COMMENT ON DATABASE'):
            name, tag = re.fullmatch(r"COMMENT ON DATABASE \"([^\"]+)\" IS '(.*)'", input).groups()
            key = self.key(name); self.catalog[key]['tag'] = tag.replace("''", "'"); action = 'comment-' + key
        elif input.startswith('CREATE EXTENSION'):
            self.schemas['fresh']['extensions'] = ['pgcrypto', 'vector']; action = 'extensions'
        else:
            raise AssertionError('unrecognized/unsafe mock DDL')
        if self.fail_after == action:
            self.fail_after = None
            raise subprocess.TimeoutExpired('private owned SQL', timeout)
        return ''

    def test_plan_is_readonly_requires_explicit_identity_and_distinguishes_old_binary_from_new_source(self):
        result = self.rotation.plan()
        self.assertFalse(result['ready']); self.assertTrue(result['apply_required'])
        self.assertEqual(result['registered_source_tree_sha'], 'a' * 64)
        self.assertEqual(result['current_source_tree_sha'], 'c' * 64)
        self.assertFalse(self.rotation.journal_path.exists()); self.assertEqual(self.commands, [])
        self.stop.assert_not_called()
        for owner, run, before in [('foreign', self.run, profile.BEFORE), (profile.OWNER, 'bad', profile.BEFORE),
                                   (profile.OWNER, self.run, str(uuid.uuid4()))]:
            with self.assertRaises(profile.Blocked): profile.Rotation(self.root, owner, run, before)

    def test_private_records_and_ancestors_reject_symlink_and_non0600(self):
        os.chmod(self.fixture_path, 0o644)
        with self.assertRaises(profile.Blocked): self.make()
        os.chmod(self.fixture_path, 0o600)
        stored = self.fixture_path.with_name('original-private.json'); self.fixture_path.rename(stored)
        self.fixture_path.symlink_to(stored)
        with self.assertRaises(profile.Blocked): self.make()

    def test_all_five_dsns_reject_foreign_host_database_user_or_port(self):
        for key, dsn in [('TESTLAB_APP_DSN', 'postgres://testlab:p@business.invalid:15461/aiops_testlab_app'),
                         ('TESTLAB_APP_DSN', 'postgres://testlab:p@127.0.0.1:15461/foreign'),
                         ('TESTLAB_FORMAL_DSN', 'postgres://other:p@127.0.0.1:15461/wan124test'),
                         ('TESTLAB_PASSKEY_DSN', 'postgres://testlab:p@127.0.0.1:5432/wan124passkey')]:
            changed = copy.deepcopy(self.environment); changed['env'][key] = dsn
            profile.env._private(self.env_path, changed)
            with self.subTest(dsn=dsn), self.assertRaises(profile.Blocked): self.make()

    def test_all_five_dsns_reject_query_overrides_duplicates_unknown_or_missing_sslmode(self):
        attacks = ['', 'host=business.invalid', 'sslmode=disable&host=business.invalid',
                   'sslmode=disable&port=5432', 'sslmode=disable&dbname=foreign',
                   'sslmode=disable&user=foreign', 'sslmode=disable&sslmode=disable',
                   'sslmode=disable&%68ost=business.invalid', 'sslmode=disable&options=-csearch_path=foreign',
                   'sslmode=disable&unknown=x', 'sslmode=disable&', 'sslmode=disable#fragment', 'sslmode=disable#']
        for key in profile.DSNS:
            for query in attacks:
                changed, fixture = copy.deepcopy(self.environment), copy.deepcopy(self.fixture)
                changed['env'][key] = changed['env'][key].split('?')[0] + '?' + query
                fixture['database_url'] = changed['env']['TESTLAB_APP_DSN']
                profile.env._private(self.env_path, changed); profile.env._private(self.fixture_path, fixture)
                with self.subTest(key=key, query=query), self.assertRaises(profile.Blocked): self.make()

    def test_supervisor_process_scans_share_remaining_apply_deadline(self):
        now = [0.0]; self.rotation.deadline = 1.0
        def old_scan(pid, port):
            now[0] += .7
            return True
        def bounded_scan(argv, **kwargs):
            timeout = kwargs['timeout']; duration = min(.7, timeout); now[0] += duration
            if timeout < .7: raise subprocess.TimeoutExpired('owned listener scan', timeout)
            port = int(next(x for x in argv if x.startswith('-iTCP:'))[6:])
            return Mock(returncode=0, stdout='p' + str(dict(zip(profile.PORTS, (100, 101, 102)))[port]) + '\n', stderr='')
        def control(method, path):
            return 200, json.dumps(self.attestation(path)).encode()
        with patch.object(profile.time, 'monotonic', side_effect=lambda: now[0]), \
             patch.object(profile.env, '_process_port', side_effect=old_scan), \
             patch.object(profile, 'process_port', wraps=native_process_port), \
             patch.object(profile.subprocess, 'run', side_effect=bounded_scan), \
             patch.object(profile.Rotation, 'control_request', side_effect=control, create=True):
            with self.assertRaises((profile.Blocked, subprocess.TimeoutExpired)): self.rotation.supervisor()
        self.assertLessEqual(now[0], 1.0)

    def test_slow_shutdown_does_not_delegate_to_unbounded_shared_stop(self):
        now = [0.0]
        def old_stop(data):
            now[0] += 41; self.running = False
        def control(method, path):
            if method == 'GET': return 200, json.dumps(self.attestation(path)).encode()
            timeout = self.rotation.budget(3); now[0] += timeout
            raise subprocess.TimeoutExpired('owned shutdown response', timeout)
        with patch.object(profile.time, 'monotonic', side_effect=lambda: now[0]), \
             patch.object(profile.time, 'sleep', side_effect=lambda delay: now.__setitem__(0, now[0] + delay)), \
             patch.object(profile.backend, 'stop', side_effect=old_stop) as shared_stop, \
             patch.object(profile.Rotation, 'control_request', side_effect=control, create=True):
            with self.assertRaises((profile.Blocked, subprocess.TimeoutExpired)): self.rotation.apply()
        self.assertLessEqual(now[0], 40.0); shared_stop.assert_not_called()
        self.assertEqual(self.commands, [])

    def test_shutdown_rejected_response_never_retries_post_or_starts_ddl(self):
        posts = []
        def rejected(method, path):
            if method == 'GET': return self.control(method, path)
            posts.append(path); return 403, b''
        with patch.object(profile.Rotation, 'control_request', side_effect=rejected), \
             self.assertRaisesRegex(profile.Blocked, 'shutdown rejected'):
            self.rotation.apply()
        self.assertEqual(posts, ['/shutdown']); self.assertTrue(self.running)
        self.assertEqual(self.commands, []); self.assertEqual(self.catalog['canonical']['oid'], 100)

    def test_container_tuple_volume_run_label_and_id_are_failclosed(self):
        for field in ['owner', 'image', 'port', 'volume', 'run']:
            original = self.pg_info; changed = copy.deepcopy(original)
            if field == 'owner': changed['Config']['Labels']['aiops.testlab.owner'] = 'foreign'
            elif field == 'image': changed['Config']['Image'] = 'postgres:latest'
            elif field == 'port': changed['NetworkSettings']['Ports']['5432/tcp'][0]['HostIp'] = '0.0.0.0'
            elif field == 'volume': changed['Mounts'][0]['Source'] = '/foreign/pgdata'
            elif field == 'run': changed['Config']['Labels']['aiops.testlab.run'] = 'foreign-run'
            self.pg_info = changed
            with self.subTest(field=field), self.assertRaises(profile.Blocked): self.make()
            self.pg_info = original

    def test_active_queued_or_missing_failed_evidence_blocks_before_any_shutdown(self):
        for status in ['running', 'queued', 'passed']:
            with contextlib.closing(sqlite3.connect(self.ledger)) as connection, connection:
                connection.execute('UPDATE runs SET data=?', (json.dumps({'status': status, 'ended_at': 'finished'}),))
            with self.subTest(status=status), self.assertRaises(profile.Blocked): self.make()
        self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_fresh_hmac_replay_forgery_and_unknown_host_api_or_client_rejected(self):
        good = self.attestation('http://127.0.0.1:18560/attest?nonce=' + 'd' * 32)
        with patch.object(profile.Rotation, 'control_request', return_value=(200, json.dumps(good).encode())), self.assertRaises(profile.Blocked): self.rotation.supervisor()
        for target in ['api_listener_pids', 'client_pids']:
            with patch.object(profile, target, return_value={101, 102, 999}), self.assertRaises(profile.Blocked): self.rotation.supervisor()
        self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_apply_preserves_oid_and_all_config_bytes_creates_empty_tagged_canonical_without_restart(self):
        env_bytes, fixture_bytes = self.env_path.read_bytes(), self.fixture_path.read_bytes()
        result = self.rotation.apply()
        self.assertTrue(result['applied']); self.assertFalse(result['ready'])
        self.assertEqual(self.catalog['archive']['oid'], 100); self.assertEqual(self.schemas['archive']['user_tables'], 93)
        self.assertEqual(self.catalog['canonical']['oid'], 200); self.assertIsNone(self.catalog['fresh'])
        self.assertEqual(result['fresh_initial_schema'], {'user_tables': 0, 'extensions': ['pgcrypto', 'vector']})
        self.assertEqual(len(self.commands), 6); self.stop.assert_called_once()
        self.assertEqual(self.env_path.read_bytes(), env_bytes); self.assertEqual(self.fixture_path.read_bytes(), fixture_bytes)
        text = self.rotation.journal_path.read_text(); journal = json.loads(text)
        self.assertEqual(journal['stage'], 'complete'); self.assertFalse(journal['migrations_executed'])
        self.assertFalse(journal['api_started']); self.assertFalse(journal['archive_data_deleted'])
        self.assertEqual(self.rotation.journal_path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(journal['initial_hmac_receipt']['identity']['source_tree_sha'], 'a' * 64)
        for secret in ['synthetic-private', 'synthetic-control-secret', 'synthetic-admin-secret', 'DROP DATABASE', 'FORCE']:
            self.assertNotIn(secret, text + '\n'.join(self.commands))

    def test_lost_rename_comment_extension_or_publish_response_reconciles_without_duplicate_ddl(self):
        # One invocation per independent fixture/test; restore the model and
        # delete only private offline journal files between subcases.
        for action in ['rename-archive', 'comment-archive', 'comment-fresh', 'extensions', 'rename-canonical']:
            with self.subTest(action=action):
                self.running = True; self.catalog.update(canonical={'oid': 100, 'owner': 'testlab', 'tag': None}, archive=None, fresh=None)
                self.schemas = {'canonical': {'user_tables': 93, 'extensions': ['pgcrypto', 'vector']}}
                if self.rotation.work.exists():
                    import shutil
                    shutil.rmtree(self.rotation.work)
                self.commands.clear(); self.rotation = self.make(); self.fail_after = action
                result = self.rotation.apply()
                self.assertTrue(result['applied']); self.assertEqual(len(self.commands), 6)
                self.assertEqual(self.catalog['archive']['oid'], 100)

    def test_uncertain_untagged_create_is_retained_and_never_adopted_or_repeated_on_resume(self):
        self.fail_after = 'create-fresh'
        with self.assertRaisesRegex(profile.Blocked, 'CREATE receipt'): self.rotation.apply()
        self.assertEqual(self.catalog['archive']['oid'], 100); self.assertIsNone(self.catalog['canonical'])
        self.assertIsNone(self.catalog['fresh']['tag'])
        before = list(self.commands)
        resumed = self.make()
        with self.assertRaisesRegex(profile.Blocked, 'unreceipted'): resumed.apply()
        self.assertEqual(self.commands, before)
        self.assertFalse(resumed.journal['fresh_create_completed'])

    def test_complete_replay_cannot_archive_new_canonical_again(self):
        self.rotation.apply(); before = list(self.commands); stop_count = self.stop.call_count
        resumed = self.make(); result = resumed.apply()
        self.assertTrue(result['already_complete']); self.assertFalse(result['applied'])
        self.assertEqual(self.commands, before); self.assertEqual(self.stop.call_count, stop_count)
        self.assertEqual(self.catalog['archive']['oid'], 100)

    def test_stop_lost_response_reconciles_closed_listeners_without_signaling_saved_pids(self):
        def shutdown_lost(data):
            self.shutdown(data)
            raise OSError('accepted shutdown response lost')
        self.stop.side_effect = shutdown_lost
        with patch.object(profile.os, 'kill') as signal:
            self.assertTrue(self.rotation.apply()['applied'])
        self.stop.assert_called_once(); signal.assert_not_called()
        self.assertEqual(self.rotation.journal['stop_error_type'], 'OSError')

    def test_rename_and_readback_lost_response_retains_stage_and_resumes_from_original_oid(self):
        native = self.command
        failed = [False]
        def command(argv, *, input, timeout):
            if input.startswith('ALTER DATABASE') and profile.APP + '" RENAME' in input and not failed[0]:
                failed[0] = True
                self.fail_after = 'rename-archive'
                return native(argv, input=input, timeout=timeout)
            if input.startswith('BEGIN READ ONLY; ') and failed[0] and self.catalog['archive'] and self.catalog['archive']['tag'] is None:
                failed[0] = False
                raise subprocess.TimeoutExpired('readback', timeout)
            return native(argv, input=input, timeout=timeout)
        with patch.object(profile.env, '_cmd', side_effect=command), self.assertRaisesRegex(profile.Blocked, 'reconciliation failed'):
            self.rotation.apply()
        persisted = json.loads(self.rotation.journal_path.read_text())
        self.assertEqual(persisted['stage'], 'rename_archive_intent')
        self.assertEqual(persisted['transport_errors'][0]['error_type'], 'TimeoutExpired')
        self.assertEqual(persisted['reconcile_error_type'], 'TimeoutExpired')
        resumed = self.make(); self.assertTrue(resumed.apply()['applied'])
        self.assertEqual(sum(x.startswith('ALTER DATABASE "aiops_testlab_app"') for x in self.commands), 1)
        self.assertEqual(self.catalog['archive']['oid'], 100)

    def test_write_ahead_intent_failure_before_rename_retains_entire_original_and_no_ddl(self):
        native = profile.env._private
        def write(path, row):
            if path == self.rotation.journal_path and row.get('stage') == 'rename_archive_intent':
                raise OSError('intent sync failed')
            return native(path, row)
        with patch.object(profile.env, '_private', side_effect=write), self.assertRaises(OSError): self.rotation.apply()
        self.assertEqual(self.commands, []); self.assertEqual(self.catalog['canonical']['oid'], 100)
        self.assertIsNone(self.catalog['archive']); self.assertEqual(self.schemas['canonical']['user_tables'], 93)

    def test_pending_ddl_or_active_app_client_blocks_without_termination(self):
        self.rotation.deadline = 100
        self.running = False; self.catalog['connections'] = [{'pid': 999, 'kind': 'client backend', 'database': profile.APP}]
        with patch.object(self.rotation, 'catalog', return_value=self.catalog), \
             patch.object(profile.time, 'monotonic', side_effect=[0, 9]), self.assertRaisesRegex(profile.Blocked, 'quiescence'):
            self.rotation.quiescent()
        self.assertEqual(self.commands, [])

    def test_plan_conflicting_archive_or_foreign_owner_refuses_shutdown(self):
        for value in [{'oid': 999, 'owner': 'testlab', 'tag': None}, {'oid': 100, 'owner': 'foreign', 'tag': None}]:
            self.catalog['archive'] = value
            with self.assertRaises(profile.Blocked): self.rotation.plan()
        self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_unknown_canonical_oid_and_changed_container_are_not_adopted_after_completion(self):
        self.rotation.apply()
        self.catalog['canonical']['oid'] = 999
        with self.assertRaises(profile.Blocked): self.make().apply()
        self.catalog['canonical']['oid'] = 200; self.pg_info['Id'] = 'replacement-container'
        with self.assertRaises(profile.Blocked): self.make()
        self.assertEqual(len(self.commands), 6)

    def test_write_ahead_persistence_failure_prevents_shutdown_and_ddl(self):
        with patch.object(profile, 'private_file', side_effect=OSError('journal unavailable')), self.assertRaises(OSError): self.rotation.apply()
        self.stop.assert_not_called(); self.assertEqual(self.commands, [])

    def test_mutation_allowlist_total_budget_and_readonly_mode_refuse_foreign_sql(self):
        with self.assertRaises(profile.Blocked): self.rotation.pg('DROP DATABASE "aiops_testlab_app"', read=False)
        self.rotation.deadline = profile.time.monotonic() + 40
        for statement, database in [('DROP DATABASE "foreign"', 'postgres'), ('TRUNCATE users', profile.APP), ('SELECT 1', 'foreign')]:
            with self.assertRaises(profile.Blocked): self.rotation.pg(statement, database, read=False)
        self.rotation.deadline = 0
        with self.assertRaises(profile.Blocked): self.rotation.pg('SELECT 1')
        self.assertEqual(self.commands, [])

    def test_default_cli_only_plans_without_taking_mutation_lock(self):
        with patch.object(sys, 'argv', ['profile.py', '--owner', profile.OWNER, '--run', self.run, '--before-evidence-run', profile.BEFORE]), \
             patch.object(profile, 'Rotation') as factory, patch.object(profile, 'exclusive') as lock, patch('sys.stdout', io.StringIO()):
            factory.return_value.plan.return_value = {'ready': False}
            self.assertEqual(profile.main(), 0)
        factory.return_value.plan.assert_called_once(); factory.return_value.apply.assert_not_called(); lock.assert_not_called()


class ProfileTopologyTests(unittest.TestCase):
    def test_receipt_snapshots_hash_exact_parsed_bytes_and_close_every_fd_on_success_and_failure(self):
        native_open, native_close, native_read = os.open, os.close, os.read
        with tempfile.TemporaryDirectory() as directory:
            paths = [Path(directory) / name for name in ('freeze.json', 'stop.json', 'observed.json')]
            for failure in ('none', 'parse', 'read', 'open'):
                opened, closed = [], []
                for index, path in enumerate(paths):
                    path.write_bytes(json.dumps({'row': index}).encode()); os.chmod(path, 0o600)
                if failure == 'parse': paths[1].write_text('{invalid')
                def open_file(path, flags, *args):
                    self.assertTrue(flags & os.O_NOFOLLOW); self.assertTrue(flags & os.O_NONBLOCK)
                    if failure == 'open' and path == paths[2]: raise OSError(profile.errno.EIO, 'offline open failure')
                    fd = native_open(path, flags, *args); opened.append(fd); return fd
                def close_file(fd): closed.append(fd); return native_close(fd)
                def read_file(fd, size):
                    if failure == 'read': raise OSError(profile.errno.EIO, 'offline read failure')
                    return native_read(fd, size)
                with self.subTest(failure=failure), patch.object(profile.os, 'open', side_effect=open_file), \
                     patch.object(profile.os, 'close', side_effect=close_file), patch.object(profile.os, 'read', side_effect=read_file):
                    if failure == 'none':
                        rows, hashes = profile.receipt_snapshots(paths)
                        self.assertEqual(rows, [{'row': index} for index in range(3)])
                        self.assertEqual(hashes, {path.name: profile.digest(path) for path in paths})
                    else:
                        with self.assertRaises(profile.Blocked): profile.receipt_snapshots(paths)
                self.assertEqual(closed, list(reversed(opened)))
                self.assertEqual(len(opened), {'none': 3, 'parse': 2, 'read': 1, 'open': 2}[failure])
                for fd in opened:
                    with self.assertRaises(OSError) as error: os.fstat(fd)
                    self.assertEqual(error.exception.errno, profile.errno.EBADF)

    def test_receipt_snapshot_size_type_and_midread_change_are_failclosed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'receipt.json'; path.write_text('{}'); os.chmod(path, 0o600)
            path.write_bytes(b'x' * ((4 << 20) + 1))
            with self.assertRaises(profile.Blocked): profile.receipt_snapshots([path])
            path.unlink(); path.mkdir(mode=0o700)
            with self.assertRaises(profile.Blocked): profile.receipt_snapshots([path])
            path.rmdir(); path.write_text('{}'); os.chmod(path, 0o600)
            native_read, changed = os.read, [False]
            def read_and_append(fd, size):
                raw = native_read(fd, size)
                if not changed[0]:
                    changed[0] = True
                    with path.open('ab') as stream: stream.write(b' ')
                return raw
            with patch.object(profile.os, 'read', side_effect=read_and_append), self.assertRaises(profile.Blocked):
                profile.receipt_snapshots([path])

    def rotation(self, deadline):
        rotation = object.__new__(profile.Rotation)
        rotation.deadline = deadline; rotation.data = {'control_secret': 'synthetic-private-token'}
        return rotation

    def test_controller_dripping_response_has_one_total_deadline_across_connect_send_receives(self):
        now = [0.0]; timeouts = []
        class Socket:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def settimeout(self, timeout): timeouts.append(timeout)
            def consume(self, delay):
                now[0] += min(delay, timeouts[-1])
                if delay > timeouts[-1]: raise TimeoutError('mock bounded socket call')
            def connect(self, address):
                self.address = address; self.consume(.2)
            def sendall(self, payload):
                self.payload = payload; self.consume(.2)
            def recv(self, count):
                self.consume(.4)
                return b'HTTP/1.0 200 OK\r\n'
        socket_ = Socket(); rotation = self.rotation(1.0)
        with patch.object(profile.socket, 'socket', return_value=socket_), \
             patch.object(profile.time, 'monotonic', side_effect=lambda: now[0]), self.assertRaises(TimeoutError):
            rotation.control_request('GET', '/attest?nonce=' + 'a' * 32)
        self.assertEqual(socket_.address, ('127.0.0.1', 18560)); self.assertLessEqual(now[0], 1.0)
        self.assertAlmostEqual(timeouts[-1], .2)
        self.assertNotIn(b'Authorization:', socket_.payload)

    def test_controller_shutdown_exact_token_and_length_contract_no_redirect_or_foreign_target(self):
        response = [b'HTTP/1.0 202 Accepted\r\nContent-Length: 0\r\n\r\n']
        socket_ = Mock(); socket_.__enter__ = Mock(return_value=socket_); socket_.__exit__ = Mock(return_value=False)
        socket_.recv.side_effect = lambda size: response.pop(0)
        rotation = self.rotation(profile.time.monotonic() + 40)
        with patch.object(profile.socket, 'socket', return_value=socket_):
            self.assertEqual(rotation.control_request('POST', '/shutdown'), (202, b''))
        socket_.connect.assert_called_once_with(('127.0.0.1', 18560))
        payload = socket_.sendall.call_args.args[0]
        self.assertTrue(payload.startswith(b'POST /shutdown HTTP/1.0\r\n'))
        self.assertIn(b'Authorization: Bearer synthetic-private-token\r\n', payload)
        for method, path in [('POST', '/fault'), ('GET', 'http://foreign/attest'), ('POST', '/shutdown?other=1')]:
            with self.assertRaises(profile.Blocked): rotation.control_request(method, path)
        rotation.deadline = None
        with self.assertRaises(profile.Blocked): rotation.control_request('POST', '/shutdown')
        for reply in [b'HTTP/1.0 200 OK\r\nContent-Length: 0\r\nContent-Length: 0\r\n\r\n',
                      b'HTTP/1.0 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n',
                      b'HTTP/1.0 200 OK\r\nContent-Length: 99999\r\n\r\n']:
            response[:] = [reply]
            with patch.object(profile.socket, 'socket', return_value=socket_), self.assertRaises(profile.Blocked):
                rotation.control_request('GET', '/attest?nonce=' + 'a' * 32)

    def test_port_scan_stops_before_third_connect_after_shared_deadline(self):
        now = [0.0]; opened = []
        class Socket:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def settimeout(self, timeout): self.timeout = timeout
            def connect_ex(self, address):
                opened.append(address); now[0] += self.timeout; return profile.errno.ECONNREFUSED
        with patch.object(profile.socket, 'socket', side_effect=Socket), \
             patch.object(profile.time, 'monotonic', side_effect=lambda: now[0]), self.assertRaises(profile.Blocked):
            profile.listeners(deadline=.3)
        self.assertEqual(len(opened), 2); self.assertEqual(now[0], .3)

    def test_timed_out_listener_probe_is_uncertain_rather_than_confirmed_shutdown(self):
        socket_ = Mock(); socket_.__enter__ = Mock(return_value=socket_); socket_.__exit__ = Mock(return_value=False)
        socket_.connect_ex.return_value = profile.errno.ETIMEDOUT
        with patch.object(profile.socket, 'socket', return_value=socket_), self.assertRaises(profile.Blocked):
            profile.listeners(deadline=profile.time.monotonic() + 1)

    def test_host_client_parser_uses_outbound_tuple_and_ignores_reverse_docker_socket(self):
        output = ('p101\nn127.0.0.1:60000->127.0.0.1:15461\n'
                  'p777\nn127.0.0.1:15461->127.0.0.1:60000\n'
                  'p999\nn[::1]:60001->[::1]:15461\n')
        with patch.object(profile.subprocess, 'run', return_value=Mock(returncode=0, stdout=output, stderr='')):
            self.assertEqual(profile.client_pids(), {101, 999})
        with patch.object(profile.subprocess, 'run', return_value=Mock(returncode=0, stdout='punknown\nn127.0.0.1:60000->127.0.0.1:15461\n', stderr='')):
            with self.assertRaises(profile.Blocked): profile.client_pids()

    def test_api_listener_scanner_requires_intersection_and_does_not_treat_permissions_as_absence(self):
        with patch.object(profile.subprocess, 'run', return_value=Mock(returncode=0, stdout='p101\np999\n', stderr='')) as command:
            self.assertEqual(profile.api_listener_pids(), {101, 999})
        self.assertIn('-a', command.call_args.args[0])
        for function in (profile.client_pids, profile.api_listener_pids):
            with patch.object(profile.subprocess, 'run', return_value=Mock(returncode=1, stdout='', stderr='permission denied')):
                with self.assertRaises(profile.Blocked): function()

    def test_lsof_scanners_receive_remaining_budget_including_exact_registered_process(self):
        for function, args in [(profile.client_pids, ()), (profile.api_listener_pids, ()), (profile.process_port, (101, 18561))]:
            with patch.object(profile.subprocess, 'run', return_value=Mock(returncode=0, stdout='p101\n', stderr='')) as command:
                function(*args, timeout=.125)
            self.assertEqual(command.call_args.kwargs['timeout'], .125)


if __name__ == '__main__':
    unittest.main()
