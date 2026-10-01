"""One exact test-fixture transition; never a general 'ignore test files' policy.

Preparation and qualification are pure with respect to the project and ledger.
The caller must obtain registry entries and activation evidence from its trusted,
immutable evidence store; a downloaded proof or pass flag is not registration.
Only HTTP source eligibility can be extended. Existing case/mode/status/original
artifact checks still apply, and Go, performance and soak gates remain strict.
"""
from __future__ import annotations

import ast
import base64
from dataclasses import dataclass
import difflib
import glob
import hashlib
import json
from pathlib import Path
import re
import sqlite3
import subprocess
import uuid

from runner import source_fingerprint

OLD_SOURCE = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
NEW_SOURCE = '59df22ad1d9c120f0fb24dfd3c4a3c8e0d3933a8b9cdfd6df5766721ce57537d'
PRODUCT_SOURCE = 'e92f30aeec62941c56359e4da82b32c7642d854d9f0629b62235832a1a7e0a9a'
PATCH_SHA = '11667fad63e72290d4ecbf88be6a0c231e0b10d6ad0626fe5ed201bc420f128c'
ORIGINAL_BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
FIXTURE = 'internal/v3events/list_authority_guard_test.go'
ORIGINAL_FIXTURE_SHA = 'af9f2b4adb50b267178a6eaa0d704a32b9fd32398e40464cb24cf675efbe35e4'


def sha(data):
    return hashlib.sha256(data).hexdigest()


def fingerprint(inputs):
    digest = hashlib.sha256()
    for name in sorted(inputs):
        digest.update(name.encode() + b'\0')
        digest.update(inputs[name])
    return digest.hexdigest()


@dataclass(frozen=True)
class Snapshot:
    inputs: dict[str, bytes]
    embedded: frozenset[str]

    @property
    def source_sha(self):
        return fingerprint(self.inputs)


def collect_inputs(root):
    """Enumerate runner's input scope, fail closed, then cross-check its hash.

    runner currently exposes the fingerprint but not the input manifest. Embed
    enumeration therefore mirrors that narrow scope; an independent call to the
    existing runner implementation must agree before any proof can be prepared.
    """
    root = Path(root).resolve()
    before = source_fingerprint(root)
    if before == 'unknown':
        raise ValueError('unknown actual source')
    names = subprocess.check_output(['git', '-C', str(root), 'ls-files', '--cached', '--others', '--exclude-standard', '-z']).decode().split('\0')
    selected = {name for name in names if name and (
        name in ('go.mod', 'go.sum') or name.endswith('.go') and not name.startswith('web/')
        or name.startswith('migrations/') or name == 'internal/apicontract/openapi.json')}
    embedded = set()
    for name in tuple(selected):
        source = root / name
        if source.is_symlink() or not source.is_file():
            raise ValueError('missing or symbolic backend input: ' + name)
        if not name.endswith('.go'):
            continue
        for directive in re.finditer(r'^\s*//go:embed\s+([^\r\n]+)', source.read_text(), re.MULTILINE):
            rest = directive.group(1).strip()
            while rest:
                token = re.match(r'"(?:[^"\\]|\\.)*"|`[^`]*`|[^\s]+', rest)
                if not token:
                    raise ValueError('invalid Go embed directive')
                raw = token.group(0)
                rest = rest[token.end():]
                if rest and not rest[0].isspace():
                    raise ValueError('invalid Go embed separator')
                rest = rest.strip()
                pattern = ast.literal_eval(raw) if raw.startswith('"') else raw[1:-1] if raw.startswith('`') else raw
                hidden = pattern.startswith('all:')
                pattern = pattern[4:] if hidden else pattern
                if not pattern or pattern.startswith('/') or any(p in ('', '..', '.') for p in pattern.split('/')) or '\\' in pattern:
                    raise ValueError('invalid Go embed path')
                matches = glob.glob(pattern, root_dir=source.parent, recursive=False, include_hidden=hidden)
                if not matches:
                    raise ValueError('missing Go embed input')
                for match in matches:
                    path = source.parent / match
                    if path.is_symlink():
                        raise ValueError('symbolic Go embed input')
                    for item in path.rglob('*') if path.is_dir() else [path]:
                        if item.is_symlink():
                            raise ValueError('symbolic Go embed input')
                        if not item.is_file():
                            continue
                        if path.is_dir() and not hidden and any(p.startswith(('.', '_')) for p in item.relative_to(path).parts):
                            continue
                        relative = item.relative_to(root)
                        if any(p in ('.env', '.local', '.git') for p in relative.parts):
                            raise ValueError('private Go embed input')
                        embedded.add(relative.as_posix())
    selected.update(embedded)
    inputs = {name: (root / name).read_bytes() for name in sorted(selected)}
    if fingerprint(inputs) != before or source_fingerprint(root) != before:
        raise ValueError('source changed or input manifest differs from runner')
    return Snapshot(inputs, frozenset(embedded))


def _candidate(original, patch):
    if sha(original) != ORIGINAL_FIXTURE_SHA or sha(patch) != PATCH_SHA:
        raise ValueError('unapproved original fixture or patch')
    candidate = original
    removed = [line[1:] for line in patch.splitlines(True) if line.startswith(b'-') and not line.startswith(b'---')]
    added = [line[1:] for line in patch.splitlines(True) if line.startswith(b'+') and not line.startswith(b'+++')]
    if len(removed) != 2 or len(added) != 2:
        raise ValueError('unapproved patch shape')
    for old, new in zip(removed, added):
        if candidate.count(old) != 1:
            raise ValueError('ambiguous fixture replacement')
        candidate = candidate.replace(old, new)
    exact = ''.join(difflib.unified_diff(original.decode().splitlines(True), candidate.decode().splitlines(True), fromfile='a/' + FIXTURE, tofile='b/' + FIXTURE)).encode()
    if exact != patch:
        raise ValueError('patch does not exactly describe candidate bytes')
    return candidate


def _manifest(inputs):
    return {name: sha(data) for name, data in sorted(inputs.items())}


def proof_digest(proof):
    return sha(json.dumps(proof, sort_keys=True, separators=(',', ':'), ensure_ascii=True).encode())


def prepare_proof(snapshot, patch, actual_binary_sha):
    """Prepare an inactive artifact from the actual old tree; no auto activation."""
    if snapshot.source_sha != OLD_SOURCE or actual_binary_sha != ORIGINAL_BINARY_SHA:
        raise ValueError('not the approved old source and original actual binary')
    if FIXTURE in snapshot.embedded:
        raise ValueError('changed fixture is a Go embed input')
    candidate = dict(snapshot.inputs)
    original = candidate[FIXTURE]
    candidate[FIXTURE] = _candidate(original, patch)
    product = {name: data for name, data in snapshot.inputs.items() if not name.endswith('_test.go') or name in snapshot.embedded}
    if fingerprint(candidate) != NEW_SOURCE or fingerprint(product) != PRODUCT_SOURCE:
        raise ValueError('unexpected candidate or product inputs')
    return {'version': 1, 'scope': 'http_only', 'old_source_sha': OLD_SOURCE,
            'new_source_sha': NEW_SOURCE, 'product_source_sha': PRODUCT_SOURCE,
            'actual_original_binary_sha': ORIGINAL_BINARY_SHA, 'patch_sha': PATCH_SHA,
            'changed_paths': [FIXTURE], 'fixture_original_base64': base64.b64encode(original).decode(),
            'patch_base64': base64.b64encode(patch).decode(), 'embedded_paths': sorted(snapshot.embedded),
            'old_inputs': _manifest(snapshot.inputs), 'new_inputs': _manifest(candidate)}


def _valid_activation(activation, digest):
    if activation.get('proof_sha') != digest or activation.get('canonical_source_sha') != NEW_SOURCE or activation.get('canonical_patch_sha') != PATCH_SHA:
        return False
    original = activation.get('original_case', {})
    regression = activation.get('affected_regression', {})
    expected_cases = {'TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/inbox/grant_revoke',
                      'TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/alert-groups/grant_revoke'}
    if set(original.get('test_names', [])) != expected_cases:
        return False
    if regression.get('package') != 'github.com/wans/aiops-canvas/internal/v3events' or regression.get('scope') != 'full_package':
        return False
    for key in ('original_case', 'affected_regression'):
        record = activation.get(key, {})
        if record.get('status') != 'passed' or record.get('source_tree_sha') != NEW_SOURCE or record.get('source_tree_sha_end') != NEW_SOURCE or record.get('source_override_used') is not False or record.get('race') is not True or type(record.get('command_exit_code')) is not int or record['command_exit_code'] != 0:
            return False
        try:
            uuid.UUID(record['run_id'])
            if not record.get('evidence_ids'):
                return False
            for evidence_id in record['evidence_ids']:
                uuid.UUID(evidence_id)
        except (KeyError, ValueError, TypeError, AttributeError):
            return False
    return True


def qualify_http_evidence(snapshot, evidence, registered_proofs):
    """Return an explicit label; never relabel the original stored source.

    registered_proofs maps immutable proof digests to {proof, activation} entries
    resolved by the parent from its trusted store. This function neither creates
    that registry nor verifies arbitrary user-supplied pass attestations.
    """
    if evidence.get('kind') != 'http':
        return 'ineligible'
    start, end = evidence.get('source_tree_sha'), evidence.get('source_tree_sha_end')
    if not start or not end or start == 'unknown' or end == 'unknown':
        return 'ineligible'
    if start == end == snapshot.source_sha:
        return 'current'
    if snapshot.source_sha != NEW_SOURCE or start != OLD_SOURCE or end != OLD_SOURCE or evidence.get('actual_binary_sha') != ORIGINAL_BINARY_SHA:
        return 'ineligible'
    for digest, entry in registered_proofs.items():
        try:
            proof = entry['proof']
            if proof_digest(proof) != digest or not _valid_activation(entry['activation'], digest):
                continue
            original = base64.b64decode(proof['fixture_original_base64'], validate=True)
            patch = base64.b64decode(proof['patch_base64'], validate=True)
            old_inputs = dict(snapshot.inputs)
            if old_inputs.get(FIXTURE) != _candidate(original, patch) or FIXTURE in snapshot.embedded:
                continue
            old_inputs[FIXTURE] = original
            expected = prepare_proof(Snapshot(old_inputs, snapshot.embedded), patch, ORIGINAL_BINARY_SHA)
            if expected != proof:
                continue
            return 'current_product_equivalent'
        except (KeyError, ValueError, TypeError, AttributeError):
            continue
    return 'ineligible'


GO_PACKAGE = 'github.com/wans/aiops-canvas/internal/v3events'
ORIGINAL_TESTS = frozenset({
    'TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/inbox/grant_revoke',
    'TestListAuthorityGuardHeldLocksSerializeRevocation//api/v1/alert-groups/grant_revoke'})


def _uuid(value):
    return isinstance(value, str) and str(uuid.UUID(value)) == value


def _stored_json(conn, table, ident):
    # table is an internal literal, never a registration-provided SQL fragment.
    if table not in ('runs', 'suites', 'cases', 'attempts') or not _uuid(ident):
        raise ValueError('unknown ledger reference')
    row = conn.execute('SELECT data FROM ' + table + ' WHERE id=?', (ident,)).fetchone()
    if row is None:
        raise ValueError('missing ledger reference')
    data = json.loads(row[0])
    if not isinstance(data, dict) or data.get('id') != ident:
        raise ValueError('ledger identity differs')
    return data


def _stored_bytes(store, conn, ref, run_id):
    if not isinstance(ref, dict) or not _uuid(ref.get('evidence_id')) or not re.fullmatch('[0-9a-f]{64}', ref.get('sha256', '')):
        raise ValueError('invalid registered artifact reference')
    ident = ref['evidence_id']
    row = conn.execute('SELECT run_id,filename,size FROM evidence WHERE id=?', (ident,)).fetchone()
    if row is None or row[0] != run_id or row[1] != ident + '.txt':
        raise ValueError('artifact is absent or belongs to another run')
    raw, _ = store.read_evidence(ident)
    if len(raw) != row[2] or sha(raw) != ref['sha256']:
        raise ValueError('artifact bytes differ from registered digest')
    return raw


def _canonical_command(argv, scope):
    if not isinstance(argv, list) or any(not isinstance(x, str) for x in argv) or len(argv) < 6:
        raise ValueError('missing actual go command')
    executable = Path(argv[0])
    if not executable.is_absolute() or executable.name != 'go' or argv[1] != 'test' or argv[-1] not in ('./internal/v3events', GO_PACKAGE):
        raise ValueError('not the exact canonical package command')
    flags, run_filter = [], None
    arguments = iter(argv[2:-1])
    for arg in arguments:
        if arg == '-run' and scope == 'original_case':
            if run_filter is not None:
                raise ValueError('duplicate test filter')
            run_filter = next(arguments, '')
            if not run_filter or run_filter.startswith('-'):
                raise ValueError('invalid test filter')
        elif arg in ('-race', '-json', '-count=1') or re.fullmatch(r'-timeout=\d+(?:\.\d+)?(?:ms|s|m|h)', arg):
            flags.append(arg)
        else:
            raise ValueError('unapproved test/build/source flag')
    if any(flags.count(flag) != 1 for flag in ('-race', '-json', '-count=1')) or len(flags) != len(set(flags)):
        raise ValueError('race/json/fresh execution must be explicit')
    if scope == 'original_case' and run_filter is None:
        raise ValueError('original-case command must be explicitly targeted')


def _go_terminal_results(raw, argv):
    text = raw.decode('utf-8')
    lines = text.splitlines()
    if not lines or not lines[0].startswith('命令：') or json.loads(lines[0][3:]) != argv or '[日志达到大小上限' in text:
        raise ValueError('command log is missing, truncated, or mismatched')
    started, passed, package_passes = set(), set(), 0
    for line in lines[1:]:
        try:
            event = json.loads(line)
        except ValueError:
            continue  # go tool diagnostics are not test results.
        if not isinstance(event, dict) or event.get('Package') != GO_PACKAGE:
            raise ValueError('unexpected go result package')
        action, name = event.get('Action'), event.get('Test')
        if action in ('fail', 'skip'):
            raise ValueError('failed or unexplained skipped go result')
        if name:
            if action == 'run':
                if name in started:
                    raise ValueError('repeated test execution')
                started.add(name)
            elif action == 'pass':
                if name in passed or name not in started:
                    raise ValueError('terminal test without unique start')
                passed.add(name)
        elif action == 'pass':
            package_passes += 1
    if package_passes != 1 or not started or passed != started:
        raise ValueError('missing complete go/package terminal results')
    return passed


def _resolve_regression(store, conn, root, scope, reference):
    if not isinstance(reference, dict):
        raise ValueError('missing regression references')
    run_id = reference.get('run_id')
    run = _stored_json(conn, 'runs', run_id)
    if run.get('status') != 'passed' or not run.get('ended_at') or run.get('source_tree_sha') != NEW_SOURCE or run.get('source_tree_sha_end') != NEW_SOURCE:
        raise ValueError('regression was not completed on actual canonical new source')
    raw_completion = _stored_bytes(store, conn, reference.get('completion'), run_id)
    completion = json.loads(raw_completion)
    if not isinstance(completion, dict) or type(completion.get('version')) is not int or completion['version'] != 1 or completion.get('kind') != 'go_command_completion':
        raise ValueError('unknown command-completion schema')
    if completion.get('run_id') != run_id or completion.get('cwd') != str(root) or completion.get('parser') != 'go_json':
        raise ValueError('noncanonical command identity')
    if type(completion.get('exit_code')) is not int or completion['exit_code'] != 0 or any(completion.get(k) is not False for k in ('timed_out', 'cancelled', 'malformed')):
        raise ValueError('command did not finish successfully')
    if completion.get('source_tree_sha') != NEW_SOURCE or completion.get('source_tree_sha_end') != NEW_SOURCE:
        raise ValueError('command source changed or was not captured')
    environment = completion.get('go_environment')
    if not isinstance(environment, dict) or any(environment.get(k) != v for k, v in {'GOFLAGS': '', 'GOWORK': 'off', 'GOENV': 'off'}.items()):
        raise ValueError('effective Go source configuration was not pinned')
    argv = completion.get('argv')
    _canonical_command(argv, scope)
    output_ref = reference.get('output')
    raw_output = _stored_bytes(store, conn, output_ref, run_id)
    if completion.get('command_evidence_id') != output_ref['evidence_id']:
        raise ValueError('completion points to another command output')
    names = _go_terminal_results(raw_output, argv)
    suite = _stored_json(conn, 'suites', completion.get('suite_id'))
    if suite.get('run_id') != run_id or suite.get('status') != 'passed' or not suite.get('ended_at'):
        raise ValueError('command suite is foreign or not completed')
    pairs = completion.get('case_attempts')
    if not isinstance(pairs, list) or not pairs or any(not isinstance(p, dict) for p in pairs):
        raise ValueError('missing actual immutable attempts')
    case_ids = [p.get('case_id') for p in pairs]
    if len(case_ids) != len(set(case_ids)) or set(case_ids) != set(reference.get('case_ids', [])):
        raise ValueError('registered command case set differs')
    actual_names = set()
    for pair in pairs:
        case = _stored_json(conn, 'cases', pair.get('case_id'))
        attempt = _stored_json(conn, 'attempts', pair.get('attempt_id'))
        if (case.get('run_id') != run_id or case.get('suite_id') != suite['id'] or case.get('module') != GO_PACKAGE
                or case.get('status') != 'passed' or attempt.get('status') != 'passed'
                or not attempt.get('ended_at') or attempt.get('run_id') != run_id or attempt.get('case_id') != case['id']):
            raise ValueError('actual case/attempt did not pass in this command')
        # Runner go_json links its command output through this original record;
        # attempts themselves intentionally remain immutable with no late edits.
        links = conn.execute("SELECT data FROM records WHERE run_id=? AND kind='environment' AND json_extract(data,'$.kind')='command_evidence' AND json_extract(data,'$.case_id')=?", (run_id, case['id']))
        if not any((lambda row: row.get('attempt_id') == attempt['id'] and row.get('evidence_id') == output_ref['evidence_id'])(json.loads(row[0])) for row in links):
            raise ValueError('case has no original command-evidence association')
        actual_names.add(case['name'])
    if actual_names != names or not ORIGINAL_TESTS.issubset(names):
        raise ValueError('actual Go terminal cases differ or original regressions are missing')
    result = {'run_id': run_id, 'status': 'passed', 'source_tree_sha': NEW_SOURCE,
              'source_tree_sha_end': NEW_SOURCE, 'source_override_used': False,
              'race': True, 'command_exit_code': 0,
              'evidence_ids': [reference['completion']['evidence_id'], output_ref['evidence_id']]}
    if scope == 'original_case':
        result['test_names'] = sorted(ORIGINAL_TESTS)
    else:
        result.update(package=GO_PACKAGE, scope='full_package')
    return result


def resolve_store_registry(store, root, registration_ids=()):
    """Resolve explicit v1 registrations from the formal Store, never pass flags.

    No automatic discovery or activation. A registration is an environment record
    with kind=http_source_equivalence_registration, version=1, canonical_root,
    proof_sha, proof={evidence_id,sha256}, and original_case/affected_regression
    references {run_id,case_ids,completion:{evidence_id,sha256},
    output:{evidence_id,sha256}}. Completion schema is checked below against actual
    ledger identities and original Go JSON. The Runner writer is intentionally a
    separate integration; missing completion evidence leaves this inactive.
    """
    if not registration_ids:
        return {}
    root = Path(root).resolve()
    if Path(store.root).resolve() != root:
        return {}
    registry = {}
    try:
        snapshot = collect_inputs(root)
        if snapshot.source_sha != NEW_SOURCE:
            return {}
        with store.connect() as conn:
            conn.execute('BEGIN')
            for ident in registration_ids:
                try:
                    if not _uuid(ident):
                        continue
                    row = conn.execute("SELECT run_id,data FROM records WHERE id=? AND kind='environment'", (ident,)).fetchone()
                    if row is None:
                        continue
                    registration = json.loads(row[1])
                    if (not isinstance(registration, dict) or registration.get('kind') != 'http_source_equivalence_registration'
                            or type(registration.get('version')) is not int or registration['version'] != 1
                            or registration.get('canonical_root') != str(root)):
                        continue
                    if row[0] not in {registration.get(scope, {}).get('run_id') for scope in ('original_case', 'affected_regression')}:
                        continue
                    proof_raw = _stored_bytes(store, conn, registration.get('proof'), row[0])
                    proof = json.loads(proof_raw)
                    digest = registration.get('proof_sha')
                    if proof_digest(proof) != digest:
                        continue
                    activation = {'proof_sha': digest, 'canonical_source_sha': NEW_SOURCE, 'canonical_patch_sha': PATCH_SHA,
                                  **{scope: _resolve_regression(store, conn, root, scope, registration.get(scope))
                                     for scope in ('original_case', 'affected_regression')}}
                    entry = {'proof': proof, 'activation': activation,
                             'registration_id': ident, 'proof_evidence_id': registration['proof']['evidence_id']}
                    probe = {'kind': 'http', 'source_tree_sha': OLD_SOURCE, 'source_tree_sha_end': OLD_SOURCE,
                             'actual_binary_sha': ORIGINAL_BINARY_SHA}
                    if qualify_http_evidence(snapshot, probe, {digest: entry}) == 'current_product_equivalent':
                        registry[digest] = entry
                except (OSError, KeyError, ValueError, TypeError, AttributeError, RecursionError):
                    continue  # A bad candidate cannot qualify or suppress another.
    except (OSError, ValueError, sqlite3.Error, subprocess.SubprocessError):
        return {}
    # The scan and ledger reads must not straddle another canonical edit.
    try:
        if source_fingerprint(root) != snapshot.source_sha:
            return {}
    except (OSError, ValueError, subprocess.SubprocessError):
        return {}
    return registry
