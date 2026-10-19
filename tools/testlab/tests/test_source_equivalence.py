"""Offline counterexamples for the single registered HTTP equivalence transition."""
import copy
import difflib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import source_equivalence as eq
from store import Store, now


class EquivalenceTests(unittest.TestCase):
    def setUp(self):
        original = b'// fixture\n\tincident-b: now()-interval \'1 second\'\n\tincident-c: now()-interval \'2 seconds\'\n'
        candidate = original.replace(b'now()-interval', b"(SELECT updated_at FROM ops_incidents WHERE id='incident')-interval")
        self.patch = ''.join(difflib.unified_diff(original.decode().splitlines(True), candidate.decode().splitlines(True), fromfile='a/' + eq.FIXTURE, tofile='b/' + eq.FIXTURE)).encode()
        self.old = {'go.mod': b'module fixture\n', 'go.sum': b'', 'cmd/main.go': b'package main\n//go:embed data.txt\n',
                    'cmd/data.txt': b'embedded actual input', eq.FIXTURE: original,
                    'migrations/001.sql': b'CREATE TABLE fixture(id int);',
                    'internal/apicontract/openapi.json': b'{"paths":{}}'}
        self.new = dict(self.old, **{eq.FIXTURE: candidate})
        self.embedded = frozenset({'cmd/data.txt'})
        constants = {'OLD_SOURCE': eq.fingerprint(self.old), 'NEW_SOURCE': eq.fingerprint(self.new),
                     'PRODUCT_SOURCE': eq.fingerprint({k: v for k, v in self.old.items() if not k.endswith('_test.go')}),
                     'PATCH_SHA': eq.sha(self.patch), 'ORIGINAL_FIXTURE_SHA': eq.sha(original)}
        self.constants = patch.multiple(eq, **constants)
        self.constants.start()
        self.addCleanup(self.constants.stop)
        self.proof = eq.prepare_proof(eq.Snapshot(self.old, self.embedded), self.patch, eq.ORIGINAL_BINARY_SHA)
        self.digest = eq.proof_digest(self.proof)
        self.evidence = {'kind': 'http', 'source_tree_sha': eq.OLD_SOURCE, 'source_tree_sha_end': eq.OLD_SOURCE,
                         'actual_binary_sha': eq.ORIGINAL_BINARY_SHA, 'run_status': 'passed'}
        regression = {'status': 'passed', 'source_tree_sha': eq.NEW_SOURCE, 'source_tree_sha_end': eq.NEW_SOURCE,
                      'source_override_used': False, 'race': True, 'command_exit_code': 0, 'run_id': 'c1df5c71-4e9f-461f-9142-babc79bff261',
                      'evidence_ids': ['2dd19d1b-83b5-44c5-8bd2-08efcda0a229']}
        self.activation = {'proof_sha': self.digest, 'canonical_source_sha': eq.NEW_SOURCE,
                           'canonical_patch_sha': eq.PATCH_SHA, 'original_case': dict(regression, test_names=['TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/inbox/grant_revoke', 'TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/alert-groups/grant_revoke']),
                           'affected_regression': dict(regression, run_id='3241e212-ac16-4fb7-9395-d2766cffaa52', package='github.com/destinywans/redstring/internal/v3events', scope='full_package')}
        self.registry = {self.digest: {'proof': self.proof, 'activation': self.activation}}

    def qualify(self, evidence=None, registry=None, inputs=None, embedded=None):
        return eq.qualify_http_evidence(eq.Snapshot(self.new if inputs is None else inputs, self.embedded if embedded is None else embedded),
                                       self.evidence if evidence is None else evidence, self.registry if registry is None else registry)

    def test_preparation_is_inactive_and_original_current_remains_current(self):
        self.assertNotIn('activation', self.proof)
        self.assertEqual(eq.qualify_http_evidence(eq.Snapshot(self.old, self.embedded), self.evidence, {}), 'current')
        self.assertEqual(self.qualify(registry={}), 'ineligible')

    def test_explicit_complete_registration_labels_equivalence_without_mutating_record(self):
        before = copy.deepcopy(self.evidence)
        self.assertEqual(self.qualify(), 'current_product_inputs_equivalent')
        self.assertEqual(self.evidence, before)
        self.assertEqual(self.proof['changed_paths'], [eq.FIXTURE])
        self.assertEqual(self.proof['old_inputs']['cmd/data.txt'], self.proof['new_inputs']['cmd/data.txt'])

    def test_new_actual_source_is_strict_current_without_equivalence(self):
        e = dict(self.evidence, source_tree_sha=eq.NEW_SOURCE, source_tree_sha_end=eq.NEW_SOURCE)
        self.assertEqual(self.qualify(evidence=e, registry={}), 'current')

    def test_non_http_go_performance_soak_never_use_equivalence(self):
        for kind in ('go', 'regression', 'performance', 'soak', None):
            with self.subTest(kind=kind):
                self.assertEqual(self.qualify(evidence=dict(self.evidence, kind=kind)), 'ineligible')

    def test_missing_unknown_or_changed_source_end_is_ineligible(self):
        for end in (None, '', 'unknown', eq.NEW_SOURCE, 'f' * 64):
            with self.subTest(end=end):
                self.assertEqual(self.qualify(evidence=dict(self.evidence, source_tree_sha_end=end)), 'ineligible')

    def test_linux_or_unknown_binary_does_not_change_exact_product_input_qualification(self):
        linux = '0e222b144609dbb39214c5de15111a5e3d0b4e25a60453cafe3aa5884aa6c1fd'
        for binary in (None, linux, 'f' * 64):
            with self.subTest(binary=binary):
                evidence = dict(self.evidence, actual_binary_sha=binary)
                before = copy.deepcopy(evidence)
                self.assertEqual(self.qualify(evidence=evidence), 'current_product_inputs_equivalent')
                self.assertEqual(evidence, before)
        evidence = dict(self.evidence)
        del evidence['actual_binary_sha']
        before = copy.deepcopy(evidence)
        self.assertEqual(self.qualify(evidence=evidence), 'current_product_inputs_equivalent')
        self.assertEqual(evidence, before)

    def test_interrupted_running_or_queued_never_qualify_even_with_source_end(self):
        for status in ('interrupted', 'running', 'queued'):
            with self.subTest(status=status):
                self.assertEqual(self.qualify(evidence=dict(self.evidence, run_status=status)), 'ineligible')
                current = dict(self.evidence, source_tree_sha=eq.NEW_SOURCE,
                               source_tree_sha_end=eq.NEW_SOURCE, run_status=status)
                self.assertEqual(self.qualify(evidence=current), 'ineligible')

    def test_unrelated_original_source_cannot_use_binary_independent_policy(self):
        evidence = dict(self.evidence, source_tree_sha='f' * 64, actual_binary_sha=None)
        self.assertEqual(self.qualify(evidence=evidence), 'ineligible')

    def test_unregistered_or_modified_proof_rejected(self):
        p = copy.deepcopy(self.proof)
        p['scope'] = 'all'
        self.assertEqual(self.qualify(registry={self.digest: {'proof': p, 'activation': self.activation}}), 'ineligible')
        self.assertEqual(self.qualify(registry={'f' * 64: self.registry[self.digest]}), 'ineligible')

    def test_rehashed_tampered_registration_does_not_bypass_exact_transition(self):
        for key, value in [('new_source_sha', 'f' * 64), ('changed_paths', ['go.mod']), ('scope', 'all')]:
            proof = copy.deepcopy(self.proof)
            proof[key] = value
            digest = eq.proof_digest(proof)
            activation = copy.deepcopy(self.activation)
            activation['proof_sha'] = digest
            with self.subTest(key=key):
                self.assertEqual(self.qualify(registry={digest: {'proof': proof, 'activation': activation}}), 'ineligible')

    def test_registration_requires_both_canonical_original_and_affected_regression(self):
        for key in ('original_case', 'affected_regression', 'canonical_source_sha', 'canonical_patch_sha', 'proof_sha'):
            r = copy.deepcopy(self.registry)
            del r[self.digest]['activation'][key]
            with self.subTest(key=key):
                self.assertEqual(self.qualify(registry=r), 'ineligible')

    def test_overlay_partial_failed_unknown_end_or_no_immutable_evidence_cannot_activate(self):
        changes = {'status': 'failed', 'source_override_used': True, 'source_tree_sha_end': None,
                   'source_tree_sha': eq.OLD_SOURCE, 'run_id': 'invented', 'evidence_ids': []}
        for key, value in changes.items():
            r = copy.deepcopy(self.registry)
            r[self.digest]['activation']['affected_regression'][key] = value
            with self.subTest(key=key):
                self.assertEqual(self.qualify(registry=r), 'ineligible')

    def test_unrelated_or_narrow_non_race_tests_do_not_activate(self):
        for group, key, value in [('original_case', 'test_names', ['unrelated']),
                                  ('affected_regression', 'scope', 'targeted'),
                                  ('affected_regression', 'package', 'other'),
                                  ('affected_regression', 'race', False),
                                  ('affected_regression', 'command_exit_code', 1)]:
            registry = copy.deepcopy(self.registry)
            registry[self.digest]['activation'][group][key] = value
            with self.subTest(group=group, key=key):
                self.assertEqual(self.qualify(registry=registry), 'ineligible')

    def test_boolean_exit_is_not_a_real_successful_process_exit(self):
        registry = copy.deepcopy(self.registry)
        registry[self.digest]['activation']['affected_regression']['command_exit_code'] = False
        self.assertEqual(self.qualify(registry=registry), 'ineligible')

    def test_malformed_activation_fails_closed(self):
        for malformed in (None, [], {'original_case': []}):
            registry = copy.deepcopy(self.registry)
            registry[self.digest]['activation'] = malformed
            self.assertEqual(self.qualify(registry=registry), 'ineligible')

    def test_product_mod_sum_migration_openapi_embed_or_other_test_changes_rejected(self):
        for name in self.new:
            if name == eq.FIXTURE:
                continue
            inputs = dict(self.new)
            inputs[name] += b'changed'
            with self.subTest(name=name):
                self.assertEqual(self.qualify(inputs=inputs), 'ineligible')
        inputs = dict(self.new, **{'internal/other_test.go': b'package changed'})
        self.assertEqual(self.qualify(inputs=inputs), 'ineligible')

    def test_added_deleted_or_unknown_inputs_rejected(self):
        inputs = dict(self.new, **{'unrecognized-file': b'value'})
        self.assertEqual(self.qualify(inputs=inputs), 'ineligible')
        del inputs['unrecognized-file']
        del inputs['go.sum']
        self.assertEqual(self.qualify(inputs=inputs), 'ineligible')

    def test_embedded_fixture_or_changed_embed_manifest_rejected(self):
        self.assertEqual(self.qualify(embedded=self.embedded | {eq.FIXTURE}), 'ineligible')
        self.assertEqual(self.qualify(embedded=frozenset()), 'ineligible')
        with self.assertRaises(ValueError):
            eq.prepare_proof(eq.Snapshot(self.old, self.embedded | {eq.FIXTURE}), self.patch, eq.ORIGINAL_BINARY_SHA)

    def test_bad_patch_original_file_or_binary_fails_preparation(self):
        for bad in (self.patch + b'\n', self.patch.replace(b'incident-b', b'incident-x')):
            with self.assertRaises(ValueError):
                eq.prepare_proof(eq.Snapshot(self.old, self.embedded), bad, eq.ORIGINAL_BINARY_SHA)
        with self.assertRaises(ValueError):
            eq.prepare_proof(eq.Snapshot(self.old, self.embedded), self.patch, 'f' * 64)
        with self.assertRaises(ValueError):
            eq.prepare_proof(eq.Snapshot(self.new, self.embedded), self.patch, eq.ORIGINAL_BINARY_SHA)


class StoreResolutionTests(unittest.TestCase):
    def setUp(self):
        # Reuse the existing synthetic exact-transition builder; never claim the
        # actual current repository has become the approved new source.
        EquivalenceTests.setUp(self)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.store = Store(self.root)
        self.groups = {}
        self.expected_names = sorted(self.activation['original_case']['test_names'])
        for scope in ('original_case', 'affected_regression'):
            run = self.store.create_run(source_tree_sha=eq.NEW_SOURCE,
                                        source_tree_sha_end=eq.NEW_SOURCE)
            self.store.update('runs', run['id'], status='passed', ended_at=now())
            suite = self.store.add_suite(run['id'], {'id': scope})
            self.store.update('suites', suite['id'], status='passed', ended_at=now())
            names = self.expected_names if scope == 'original_case' else self.expected_names + ['TestOtherRegression']
            pairs = []
            output = []
            for name in names:
                case = self.store.add_case(run['id'], suite['id'], name=name, module='github.com/destinywans/redstring/internal/v3events')
                attempt = self.store.start_attempt(case)
                self.store.finish_attempt(case, attempt, 'passed')
                pairs.append({'case_id': case['id'], 'attempt_id': attempt['id']})
                output.extend([{'Action': 'run', 'Package': case['module'], 'Test': name},
                               {'Action': 'pass', 'Package': case['module'], 'Test': name}])
            output.append({'Action': 'pass', 'Package': case['module']})
            argv = ['/local/go/bin/go', 'test', '-race', '-json', '-count=1']
            if scope == 'original_case':
                argv += ['-run', 'TestListAuthorityGuardHeldLocksSerializeRevocation/.*/grant_revoke$']
            argv += ['./internal/v3events']
            raw = '命令：' + json.dumps(argv) + '\n' + ''.join(json.dumps(row) + '\n' for row in output)
            log = self.store.write_evidence(run['id'], raw)
            for pair in pairs:
                self.store.add_record(run['id'], 'environment', {'kind': 'command_evidence', **pair, 'evidence_id': log})
            completion = {'version': 1, 'kind': 'go_command_completion', 'run_id': run['id'],
                          'suite_id': suite['id'], 'cwd': str(self.root), 'argv': argv,
                          'parser': 'go_json', 'exit_code': 0, 'timed_out': False,
                          'cancelled': False, 'malformed': False,
                          'source_tree_sha': eq.NEW_SOURCE, 'source_tree_sha_end': eq.NEW_SOURCE,
                          'go_environment': {'GOFLAGS': '', 'GOWORK': 'off', 'GOENV': 'off'},
                          'command_evidence_id': log, 'case_attempts': pairs}
            eid = self.store.write_evidence(run['id'], json.dumps(completion))
            refs = {'run_id': run['id'], 'case_ids': [x['case_id'] for x in pairs],
                    'completion': self.ref(eid), 'output': self.ref(log)}
            self.groups[scope] = {'run': run, 'suite': suite, 'pairs': pairs, 'completion': completion,
                                  'completion_id': eid, 'output': output, 'output_id': log, 'refs': refs}
        self.proof_run = self.groups['affected_regression']['run']['id']
        peid = self.store.write_evidence(self.proof_run, json.dumps(self.proof))
        self.registration = {'version': 1, 'kind': 'http_source_equivalence_registration',
                             'canonical_root': str(self.root), 'proof_sha': self.digest,
                             'proof': self.ref(peid),
                             **{scope: group['refs'] for scope, group in self.groups.items()}}
        record = self.store.add_record(self.proof_run, 'environment', self.registration)
        self.registration_id = record['id']

    def ref(self, eid):
        raw, _ = self.store.read_evidence(eid)
        return {'evidence_id': eid, 'sha256': eq.sha(raw)}

    def resolve(self):
        with patch.object(eq, 'collect_inputs', return_value=eq.Snapshot(self.new, self.embedded)), \
                patch.object(eq, 'source_fingerprint', return_value=eq.NEW_SOURCE):
            return eq.resolve_store_registry(self.store, self.root, [self.registration_id])

    def change_registration(self, mutate):
        import json
        value = copy.deepcopy(self.registration)
        mutate(value)
        with self.store.connect() as conn:
            conn.execute('UPDATE records SET data=? WHERE id=?', (json.dumps(value), self.registration_id))

    def change_completion(self, scope, mutate, update_digest=True):
        import json
        group = self.groups[scope]
        value = copy.deepcopy(group['completion'])
        mutate(value)
        path = self.store.evidence_dir / (group['completion_id'] + '.txt')
        path.write_text(json.dumps(value))
        with self.store.connect() as conn:
            conn.execute('UPDATE evidence SET size=? WHERE id=?', (path.stat().st_size, group['completion_id']))
        if update_digest:
            self.change_registration(lambda r: r[scope].update(completion=self.ref(group['completion_id'])))

    def test_real_store_references_construct_activation_without_external_flags(self):
        resolved = self.resolve()
        self.assertEqual(list(resolved), [self.digest])
        self.assertEqual(resolved[self.digest]['activation']['original_case']['test_names'], self.expected_names)
        self.assertEqual(eq.qualify_http_evidence(eq.Snapshot(self.new, self.embedded), self.evidence, resolved), 'current_product_inputs_equivalent')

    def test_no_explicit_registration_or_actual_new_source_never_activates(self):
        self.assertEqual(eq.resolve_store_registry(self.store, self.root, []), {})
        with patch.object(eq, 'collect_inputs', return_value=eq.Snapshot(self.old, self.embedded)):
            self.assertEqual(eq.resolve_store_registry(self.store, self.root, [self.registration_id]), {})

    def test_external_pass_flags_cannot_replace_missing_completion(self):
        self.change_registration(lambda r: r['original_case'].update(completion={}, status='passed', race=True, command_exit_code=0))
        self.assertEqual(self.resolve(), {})

    def test_cross_run_completion_and_case_are_rejected(self):
        self.change_registration(lambda r: r['original_case'].update(completion=self.groups['affected_regression']['refs']['completion']))
        self.assertEqual(self.resolve(), {})

    def test_missing_command_relation_rejects_otherwise_passing_case(self):
        with self.store.connect() as conn:
            conn.execute("DELETE FROM records WHERE run_id=? AND json_extract(data,'$.kind')='command_evidence'", (self.groups['original_case']['run']['id'],))
        self.assertEqual(self.resolve(), {})

    def test_evidence_digest_mismatch_or_unregistered_file_rejected(self):
        self.change_completion('original_case', lambda v: v.update(exit_code=1), update_digest=False)
        self.assertEqual(self.resolve(), {})
        self.change_completion('original_case', lambda v: None)
        self.assertEqual(list(self.resolve()), [self.digest])
        with self.store.connect() as conn:
            conn.execute('DELETE FROM evidence WHERE id=?', (self.groups['affected_regression']['output_id'],))
        self.assertEqual(self.resolve(), {})

    def test_failed_cancelled_malformed_or_boolean_exit_completion_rejected(self):
        for key, value in [('exit_code', False), ('exit_code', 1), ('timed_out', True), ('cancelled', True), ('malformed', True)]:
            with self.subTest(key=key, value=value):
                self.change_completion('original_case', lambda v: v.update({key: value}))
                self.assertEqual(self.resolve(), {})

    def test_wrong_cwd_unknown_end_or_overlay_environment_rejected(self):
        mutations = [lambda v: v.update(cwd=str(self.root / 'elsewhere')),
                     lambda v: v.update(source_tree_sha_end='unknown'),
                     lambda v: v['go_environment'].update(GOFLAGS='-overlay=fixture.json'),
                     lambda v: v['go_environment'].update(GOWORK='workspace.work'),
                     lambda v: v['go_environment'].pop('GOENV')]
        for change in mutations:
            self.change_completion('original_case', change)
            self.assertEqual(self.resolve(), {})

    def test_full_package_cannot_use_run_filter_or_missing_race(self):
        for arguments in [ ['test', '-json', '-count=1', './internal/v3events'],
                           ['test', '-race', '-json', '-count=1', '-run', 'OnlyOne', './internal/v3events'],
                           ['test', '-race', '-json', '-count=1', '-overlay=fixture.json', './internal/v3events'],
                           ['test', '-race', '-json', '-count=1', './...'] ]:
            self.change_completion('affected_regression', lambda v: v.update(argv=['/local/go/bin/go'] + arguments))
            self.assertEqual(self.resolve(), {})

    def test_immutable_attempt_and_actual_go_json_must_both_pass(self):
        pair = self.groups['original_case']['pairs'][0]
        with self.store.connect() as conn:
            import json
            row = json.loads(conn.execute('SELECT data FROM attempts WHERE id=?', (pair['attempt_id'],)).fetchone()[0])
            row['status'] = 'failed'
            conn.execute('UPDATE attempts SET data=? WHERE id=?', (json.dumps(row), pair['attempt_id']))
        self.assertEqual(self.resolve(), {})

    def test_fake_success_completion_does_not_override_fail_or_truncated_go_output(self):
        import json
        group = self.groups['affected_regression']
        path = self.store.evidence_dir / (group['output_id'] + '.txt')
        for output in [group['output'][:-1], group['output'] + [{'Action': 'fail', 'Package': 'github.com/destinywans/redstring/internal/v3events'}]]:
            raw = '命令：' + json.dumps(group['completion']['argv']) + '\n' + ''.join(json.dumps(row) + '\n' for row in output)
            path.write_text(raw)
            with self.store.connect() as conn:
                conn.execute('UPDATE evidence SET size=? WHERE id=?', (path.stat().st_size, group['output_id']))
            self.change_registration(lambda r: r['affected_regression'].update(output=self.ref(group['output_id'])))
            self.assertEqual(self.resolve(), {})

    def test_malformed_registration_id_symlink_or_wrong_root_rejected(self):
        self.assertEqual(eq.resolve_store_registry(self.store, self.root, ['invented']), {})
        eid = self.groups['original_case']['completion_id']
        path = self.store.evidence_dir / (eid + '.txt')
        raw = path.read_bytes()
        target = self.root / 'unregistered-copy.txt'
        target.write_bytes(raw)
        path.unlink()
        path.symlink_to(target)
        self.assertEqual(self.resolve(), {})
        path.unlink()
        path.write_bytes(raw)
        self.assertEqual(list(self.resolve()), [self.digest])
        self.change_registration(lambda r: r.update(canonical_root='/unrelated'))
        self.assertEqual(self.resolve(), {})

    def test_original_case_cannot_borrow_a_completed_case_from_another_run(self):
        original = self.groups['original_case']
        foreign = self.groups['affected_regression']['pairs'][0]
        self.change_completion('original_case', lambda v: v['case_attempts'].__setitem__(0, foreign))
        self.change_registration(lambda r: r['original_case'].update(case_ids=[foreign['case_id']] + [p['case_id'] for p in original['pairs'][1:]]))
        self.assertEqual(self.resolve(), {})

    def test_missing_original_go_terminal_name_cannot_be_replaced_by_ledger_flag(self):
        group = self.groups['original_case']
        path = self.store.evidence_dir / (group['output_id'] + '.txt')
        output = [row for row in group['output'] if row.get('Test') != self.expected_names[0]]
        raw = '命令：' + json.dumps(group['completion']['argv']) + '\n' + ''.join(json.dumps(row) + '\n' for row in output)
        path.write_text(raw)
        with self.store.connect() as conn:
            conn.execute('UPDATE evidence SET size=? WHERE id=?', (path.stat().st_size, group['output_id']))
        self.change_registration(lambda r: r['original_case'].update(output=self.ref(group['output_id'])))
        self.assertEqual(self.resolve(), {})

    def test_source_change_after_ledger_read_never_returns_active_registry(self):
        with patch.object(eq, 'collect_inputs', return_value=eq.Snapshot(self.new, self.embedded)), \
                patch.object(eq, 'source_fingerprint', return_value='f' * 64):
            self.assertEqual(eq.resolve_store_registry(self.store, self.root, [self.registration_id]), {})

    def test_unknown_schema_or_unrelated_credential_record_does_not_activate(self):
        self.change_registration(lambda r: r.update(version=2, status='passed'))
        self.assertEqual(self.resolve(), {})

    def test_running_or_unfinished_actual_suite_cannot_activate(self):
        self.store.update('suites', self.groups['original_case']['suite']['id'], status='running', ended_at=None)
        self.assertEqual(self.resolve(), {})

    def test_skipped_go_result_cannot_be_washed_green_by_completion(self):
        group = self.groups['affected_regression']
        path = self.store.evidence_dir / (group['output_id'] + '.txt')
        output = copy.deepcopy(group['output'])
        output[1]['Action'] = 'skip'
        raw = '命令：' + json.dumps(group['completion']['argv']) + '\n' + ''.join(json.dumps(row) + '\n' for row in output)
        path.write_text(raw)
        with self.store.connect() as conn:
            conn.execute('UPDATE evidence SET size=? WHERE id=?', (path.stat().st_size, group['output_id']))
        self.change_registration(lambda r: r['affected_regression'].update(output=self.ref(group['output_id'])))
        self.assertEqual(self.resolve(), {})


class InputEnumerationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        subprocess.run(['git', 'init', '-q', str(self.root)], check=True)
        for name, data in {'go.mod': 'module fixture\n', 'go.sum': '', 'cmd/main.go': 'package main\n//go:embed "data.txt" `assets` all:allassets\n',
                           'cmd/data.txt': 'one', 'cmd/assets/visible.txt': 'two', 'cmd/assets/.hidden': 'excluded',
                           'cmd/allassets/.hidden': 'included', 'migrations/001.sql': 'SQL',
                           'internal/apicontract/openapi.json': '{}', 'web/main.go': 'excluded'}.items():
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(data)
        subprocess.run(['git', '-C', str(self.root), 'add', '.'], check=True)

    def test_runner_crosscheck_and_all_required_input_categories(self):
        s = eq.collect_inputs(self.root)
        self.assertEqual(s.source_sha, eq.source_fingerprint(self.root))
        self.assertEqual(s.embedded, {'cmd/data.txt', 'cmd/assets/visible.txt', 'cmd/allassets/.hidden'})
        self.assertNotIn('web/main.go', s.inputs)
        self.assertNotIn('cmd/assets/.hidden', s.inputs)
        for name in ('go.mod', 'go.sum', 'migrations/001.sql', 'internal/apicontract/openapi.json'):
            self.assertIn(name, s.inputs)

    def test_embed_byte_change_changes_full_input_fingerprint(self):
        before = eq.collect_inputs(self.root)
        (self.root / 'cmd/data.txt').write_text('changed')
        after = eq.collect_inputs(self.root)
        self.assertNotEqual(before.source_sha, after.source_sha)

    def test_tracked_missing_input_fails_closed(self):
        (self.root / 'go.sum').unlink()
        with self.assertRaises(ValueError):
            eq.collect_inputs(self.root)

    def test_symbolic_input_fails_closed(self):
        (self.root / 'cmd/data.txt').unlink()
        (self.root / 'cmd/data.txt').symlink_to('main.go')
        with self.assertRaises(ValueError):
            eq.collect_inputs(self.root)

    def test_private_embed_fails_closed(self):
        (self.root / 'cmd/main.go').write_text('package main\n//go:embed .local/secret\n')
        p = self.root / 'cmd/.local/secret'
        p.parent.mkdir(parents=True)
        p.write_text('synthetic private value')
        with self.assertRaises(ValueError):
            eq.collect_inputs(self.root)

    def test_runner_drift_or_concurrent_change_fails_closed(self):
        actual = eq.source_fingerprint(self.root)
        with patch.object(eq, 'source_fingerprint', side_effect=[actual, 'f' * 64]):
            with self.assertRaises(ValueError):
                eq.collect_inputs(self.root)
        with patch.object(eq, 'source_fingerprint', return_value='f' * 64):
            with self.assertRaises(ValueError):
                eq.collect_inputs(self.root)


if __name__ == '__main__':
    unittest.main()
