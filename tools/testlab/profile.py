"""Plan an owned APP evidence rotation; only explicit --apply may stop/rename.

The archive keeps its original PostgreSQL OID and all data. A separately named
empty staging database is tagged before becoming canonical. No migration,
API restart, component reset, clone, DROP, FORCE or backend termination occurs.
"""
from __future__ import annotations

import argparse
import contextlib
import datetime
import errno
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import secrets
import socket
import sqlite3
import stat
import subprocess
import time
import uuid
from urllib.parse import urlsplit

import backend
import env
from faults import Blocked, private_json, validate_fixture
from api_readonly_chain import guard_pg, inspect_pg, private_file, signed_identity
from runner import source_fingerprint

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
BEFORE = '06a848d7-6821-463f-96bd-1ee2639ea6f8'
SECOND_BEFORE = '8ac896b5-70c4-43c8-a934-309ce357e31d'
SECOND_SHA = 'd6f7bd11d4cf06e4b14ab56df83c4ef737e71c6c'
SECOND_SOURCE = '15125d6ecd371864b894edfd91b7ca4944bf69dc305359d0e385a879f8cc090a'
SECOND_OID = 9270802
THIRD_BEFORE = '1a998623-7a3d-4e10-b49b-5610205ad336'
THIRD_SHA = '2851932739e47d9f241be0e78e57d27d3fba2acf'
THIRD_SOURCE = '6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
THIRD_BINARY_SHA = 'bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
THIRD_RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
THIRD_OID = 11131999
THIRD_RECEIPTS = ('wan163-formal-performance-run-freeze.json',
    'wan163-formal-performance-first-stop.json', 'wan163-formal-performance-post-stop-proof.json')
PROTECTED_ARCHIVES = (
    ('eeb805fd-25d9-4651-8ee3-fd399359df15', BEFORE, 16388),
    ('8a8aa6c1-de2c-4161-8760-02d4e3e81b00', SECOND_BEFORE, SECOND_OID))
APP = 'aiops_testlab_app'
PORTS = (18560, 18561, 18562)
DSNS = {'TEST_DATABASE_URL': 'aiops_testlab', 'TESTLAB_MFA_DSN': 'wan124mfa',
        'TESTLAB_PASSKEY_DSN': 'wan124passkey', 'TESTLAB_FORMAL_DSN': 'wan124test', 'TESTLAB_APP_DSN': APP}


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def receipt_snapshots(paths):
    """Capture private receipts once; retain all FDs through batch validation."""
    limit, handles, rows, hashes = 4 << 20, [], [], {}
    def signature(info):
        return (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns,
                info.st_ctime_ns, info.st_mode)
    def private(info):
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or not 0 <= info.st_size <= limit:
            raise Blocked('second approved source receipt must be a bounded private regular file')
    try:
        for path in paths:
            original = path.lstat(); private(original)
            # NONBLOCK prevents a racing FIFO replacement from blocking open;
            # fstat immediately rejects anything except the original regular file.
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            handles.append((path, fd, original))
            before = os.fstat(fd); private(before)
            if signature(before) != signature(original):
                raise Blocked('second approved source receipt changed before open')
            chunks, size = [], 0
            while True:
                part = os.read(fd, min(65536, limit + 1 - size))
                if not part:
                    break
                chunks.append(part); size += len(part)
                if size > limit:
                    raise Blocked('second approved source receipt exceeds bounded size')
            raw = b''.join(chunks)
            if size != before.st_size or signature(os.fstat(fd)) != signature(before):
                raise Blocked('second approved source receipt changed during read')
            rows.append(json.loads(raw))
            hashes[path.name] = hashlib.sha256(raw).hexdigest()
        # A later read/parse can race changes to any earlier receipt. Recheck
        # every still-open inode and every pathname before exposing the batch.
        for path, fd, original in handles:
            if signature(os.fstat(fd)) != signature(original) or signature(path.lstat()) != signature(original):
                raise Blocked('second approved source receipt changed during snapshot')
        return rows, hashes
    except (OSError, ValueError) as error:
        raise Blocked('second approved private external source receipt unavailable or malformed') from error
    finally:
        for _, fd, _ in reversed(handles):
            os.close(fd)


def canonical_uuid(value):
    try:
        if str(uuid.UUID(value)) == value:
            return uuid.UUID(value).hex
    except (ValueError, TypeError, AttributeError):
        pass
    raise Blocked('explicit canonical operation UUID required')


def safe_path(path):
    path = Path(path).absolute()
    if path.resolve() != path:
        raise Blocked('profile private path traverses a symlink')
    return path


def quoted(value):
    return "'" + str(value).replace("'", "''") + "'"


def identifier(value):
    if not re.fullmatch('[a-z][a-z0-9_]{0,62}', value):
        raise Blocked('noncanonical profile database identifier')
    return '"' + value + '"'


def timeout_for(deadline, maximum):
    if deadline is None:
        return maximum
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise Blocked('profile exhausted its bounded deadline; retained journal requires reconciliation')
    return min(maximum, remaining)


def listeners(ports=PORTS, *, deadline=None):
    occupied = []
    for port in ports:
        with socket.socket() as sock:
            sock.settimeout(timeout_for(deadline, .2))
            outcome = sock.connect_ex(('127.0.0.1', port))
            if outcome == 0:
                occupied.append(port)
            elif outcome != errno.ECONNREFUSED:
                raise Blocked('owned listener closure unproven; connection probe failed')
    return occupied


def client_pids(*, timeout=3):
    result = subprocess.run(['lsof', '-nP', '-iTCP:15461', '-sTCP:ESTABLISHED', '-Fpn'],
                            capture_output=True, text=True, timeout=timeout)
    if result.returncode not in (0, 1) or (result.returncode == 1 and (result.stdout or result.stderr)):
        raise Blocked('cannot attribute host PostgreSQL clients')
    current, found = None, set()
    for line in result.stdout.splitlines():
        if line.startswith('p'):
            current = int(line[1:]) if line[1:].isdigit() else None
        if line.startswith('n') and re.search(r'->(?:127\.0\.0\.1|\[::1\]):15461$', line):
            if current is None or current <= 1:
                raise Blocked('unattributed host PostgreSQL client')
            found.add(current)
    return found


def api_listener_pids(*, timeout=3):
    result = subprocess.run(['lsof', '-nP', '-a', '-c', 'aiops', '-iTCP', '-sTCP:LISTEN', '-Fp'],
                            capture_output=True, text=True, timeout=timeout)
    if result.returncode not in (0, 1) or (result.returncode == 1 and (result.stdout or result.stderr)):
        raise Blocked('cannot attribute formal API listeners')
    pids = {int(line[1:]) for line in result.stdout.splitlines() if re.fullmatch('p[0-9]+', line)}
    return pids


def process_port(pid, port, *, timeout=3):
    result = subprocess.run(['lsof', '-nP', '-iTCP:' + str(port), '-sTCP:LISTEN', '-Fp'],
                            capture_output=True, text=True, timeout=timeout)
    if result.returncode not in (0, 1) or (result.returncode == 1 and (result.stdout or result.stderr)):
        raise Blocked('cannot attribute registered listener')
    return {int(line[1:]) for line in result.stdout.splitlines() if re.fullmatch('p[0-9]+', line)} == {pid}


class Rotation:
    def __init__(self, root, owner, run_id, before):
        if owner != OWNER or before not in (BEFORE, SECOND_BEFORE, THIRD_BEFORE):
            raise Blocked('profile is restricted to the explicit current owner and before-evidence run')
        self.nonce = canonical_uuid(run_id)
        self.root, self.run_id, self.before = Path(root).resolve(), run_id, before
        self.archive = APP + '_evidence_' + self.nonce
        self.fresh = APP + '_fresh_' + self.nonce
        self.work = safe_path(self.root / '.local/testlab/environments' / owner / 'profile' / run_id)
        self.journal_path = self.work / 'journal.json'
        self.environment_path = safe_path(self.root / '.local/testlab/environment.json')
        self.backend_path = safe_path(self.root / '.local/testlab/backend-fixture.json')
        self.environment, self.data = private_json(self.environment_path), private_json(self.backend_path)
        validate_fixture(self.root, self.environment, self.data)
        if not self.environment.get('run_id') or self.environment.get('status') in ('creating', 'failed'):
            raise Blocked('original environment run and complete ownership record required')
        for key, name in DSNS.items():
            dsn = self.environment.get('env', {}).get(key, '')
            parsed = urlsplit(dsn)
            if (parsed.scheme not in ('postgres', 'postgresql') or parsed.hostname != '127.0.0.1'
                    or parsed.port != 15461 or parsed.path != '/' + name or parsed.username != 'testlab'
                    or parsed.query != 'sslmode=disable' or '#' in dsn):
                raise Blocked('all five original DSNs must retain their exact owned loopback databases')
        self.binary = safe_path(self.data['binary'])
        if not self.binary.is_file() or not re.fullmatch('[a-f0-9]{64}', self.data.get('source_tree_sha', '')):
            raise Blocked('owned binary and registered source fingerprint required')
        self.binary_sha = digest(self.binary)
        self.environment_sha, self.fixture_sha = digest(self.environment_path), digest(self.backend_path)
        self.current_source = source_fingerprint(self.root)
        if not re.fullmatch('[a-f0-9]{64}', self.current_source):
            raise Blocked('current source fingerprint unavailable')
        self.deadline = None
        self.journal = private_json(safe_path(self.journal_path)) if self.journal_path.exists() else None
        if self.journal:
            if any(self.journal.get(k) != v for k, v in self.identity_fields().items()):
                raise Blocked('profile journal operation/owner/name/evidence identity differs')
            if before == SECOND_BEFORE and self.journal.get('original_oid') != SECOND_OID:
                raise Blocked('second approved evidence journal original APP OID differs')
        if before == SECOND_BEFORE and not (self.journal and self.journal.get('stage') == 'complete'):
            if self.data['source_tree_sha'] != SECOND_SOURCE or self.current_source != SECOND_SOURCE:
                raise Blocked('second approved evidence requires its exact current and registered binary source')
        if before == THIRD_BEFORE:
            if self.journal and self.journal.get('stage') not in ('planned','stop_intent','stopped','rename_archive_intent','archive_tag_intent','create_fresh_intent','fresh_created','fresh_tag_intent','extensions_intent','publish_canonical_intent','complete'):
                raise Blocked('third approved journal phase unknown')
            if self.journal and (self.journal.get('original_oid') != THIRD_OID
                    or self.journal.get('binary_sha256') != THIRD_BINARY_SHA
                    or self.journal.get('runtime_sha256') != THIRD_RUNTIME_SHA
                    or self.journal.get('registered_source_tree_sha') != THIRD_SOURCE):
                raise Blocked('third approved journal OID/binary/runtime/source differs')
            if not (self.journal and self.journal.get('stage') == 'complete'):
                runtime_path = safe_path(self.root / '.local/testlab/runtime-config.json')
                if (self.data['source_tree_sha'] != THIRD_SOURCE or self.current_source != THIRD_SOURCE
                        or self.binary_sha != THIRD_BINARY_SHA or self.data.get('runtime_sha') != THIRD_RUNTIME_SHA
                        or digest(runtime_path) != THIRD_RUNTIME_SHA):
                    raise Blocked('third approved current and registered source/binary/runtime differs')
        self.info = self.pg_guard()
        self.ledger_guard()

    def identity_fields(self):
        return dict(schema=1, owner=OWNER, run_id=self.run_id, before_evidence_run=self.before,
                    canonical=APP, archive=self.archive, fresh=self.fresh)

    def tag(self, kind):
        return json.dumps(dict(self.identity_fields(), kind=kind), sort_keys=True, separators=(',', ':'))

    def budget(self, maximum=8):
        return timeout_for(self.deadline, maximum)

    def ledger_guard(self):
        path = safe_path(self.root / '.local/testlab/state.sqlite3')
        if not path.is_file() or stat.S_IMODE(path.stat().st_mode) != 0o600:
            raise Blocked('private existing test ledger required')
        connection = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=self.budget(2))
        try:
            evidence = None
            for identifier_, raw in connection.execute('SELECT id,data FROM runs'):
                self.budget(2)
                row = json.loads(raw)
                if row.get('status') in ('running', 'queued'):
                    raise Blocked('active or queued testlab run blocks profile rotation')
                if row.get('status') not in ('passed', 'failed', 'skipped', 'blocked', 'not_implemented', 'external_todo', 'interrupted'):
                    raise Blocked('unknown ledger test state blocks profile rotation')
                if identifier_ == self.before:
                    evidence = row
            required_status = 'interrupted' if self.before in (SECOND_BEFORE, THIRD_BEFORE) else 'failed'
            if not evidence or evidence.get('status') != required_status or not evidence.get('ended_at'):
                raise Blocked('exact terminal before-evidence performance red required')
            self.evidence = {key: evidence.get(key) for key in ('status', 'sha', 'started_at', 'ended_at')}
            if self.before == SECOND_BEFORE:
                expected = dict(selected_suite='performance', sha=SECOND_SHA,
                    source_tree_sha=SECOND_SOURCE)
                if any(evidence.get(key) != value for key, value in expected.items()):
                    raise Blocked('second approved performance evidence suite/commit/source differs or is missing')
                if 'source_tree_sha_end' in evidence and evidence['source_tree_sha_end'] != SECOND_SOURCE:
                    raise Blocked('second approved ledger end source conflicts with its frozen source')
                failures = []
                for case_id, raw in connection.execute('SELECT id,data FROM cases WHERE run_id=?', (self.before,)):
                    self.budget(2)
                    case = json.loads(raw)
                    if case.get('name') == 'perf.rps25' and case.get('status') == 'failed':
                        failures.append(case_id)
                if not failures:
                    raise Blocked('second approved interruption lacks the actual failed perf.rps25 case')
                self.evidence.update(expected, failed_rps25_case_ids=sorted(failures))
                self.evidence.update(self.second_external_evidence(evidence))
            if self.before == THIRD_BEFORE:
                expected = dict(selected_suite='performance', sha=THIRD_SHA, source_tree_sha=THIRD_SOURCE)
                if any(evidence.get(key) != value for key,value in expected.items()) or 'source_tree_sha_end' in evidence:
                    raise Blocked('third approved ledger source/suite/commit differs or missing original end was backfilled')
                statuses,failures = {},[]
                for case_id,raw in connection.execute('SELECT id,data FROM cases WHERE run_id=?',(self.before,)):
                    self.budget(2);case=json.loads(raw);status=case.get('status');statuses[status]=statuses.get(status,0)+1
                    if case.get('name')=='perf.rps50' and status=='failed':failures.append(case_id)
                if statuses != {'passed':14,'failed':2,'blocked':5} or not failures:
                    raise Blocked('third approved original 14P/2F/5B and actual failed perf.rps50 required')
                self.evidence.update(expected,failed_rps50_case_ids=sorted(failures),original_case_counts=statuses)
                self.evidence.update(self.third_external_evidence(evidence))
            if self.journal and self.journal['before_evidence'] != self.evidence:
                raise Blocked('historical before-evidence receipt changed')
        finally:
            connection.close()

    def second_external_evidence(self, evidence):
        # This is a later observation, not the missing Runner end fingerprint.
        # Fixed private receipts bind only the second explicitly approved run.
        names = ('performance-run-8ac-freeze.json', 'performance-stop-8ac.json',
                 'performance-post-stop-8ac-source.json')
        paths = [safe_path(self.root / '.local/testlab' / name) for name in names]
        rows, hashes = receipt_snapshots(paths)
        freeze, stopped, observed = rows
        if any(not isinstance(row, dict) for row in (freeze, stopped, observed)):
            raise Blocked('second approved external source receipts must be objects')
        if any(row.get('run_id') != SECOND_BEFORE for row in (freeze, stopped, observed)):
            raise Blocked('second approved external source evidence run differs')
        if (freeze.get('git_sha') != SECOND_SHA or freeze.get('source_tree_sha') != SECOND_SOURCE
                or freeze.get('run_started_at') != evidence.get('started_at')
                or stopped.get('source_tree_sha') != SECOND_SOURCE
                or observed.get('observed_source_tree_sha') != SECOND_SOURCE
                or observed.get('run_status') != 'interrupted'
                or observed.get('run_ended_at') != evidence.get('ended_at')
                or observed.get('freeze_evidence_sha256') != hashes[names[0]]
                or observed.get('stop_evidence_sha256') != hashes[names[1]]
                or type(observed.get('original_ledger_end_source_present')) is not bool):
            raise Blocked('second approved external source/commit/hash/ledger receipt differs')
        if 'source_tree_sha_end' not in evidence and observed['original_ledger_end_source_present']:
            raise Blocked('post-stop receipt claims a missing original ledger end source')
        try:
            def timestamp(value):
                parsed = datetime.datetime.fromisoformat(value)
                if parsed.tzinfo is None:
                    raise ValueError('timezone required')
                return parsed.timestamp()
            start, end, later = (timestamp(value) for value in
                (evidence['started_at'], evidence['ended_at'], observed['observed_at']))
            stop = stopped['at']
            if type(stop) not in (int, float) or not math.isfinite(stop) or not start <= stop <= end + 5 or not start <= end <= later:
                raise ValueError('source observation times differ')
        except (ValueError, TypeError, KeyError, OverflowError) as error:
            raise Blocked('second approved external source observation times invalid') from error
        initial = self.journal.get('initial_hmac_identity') if self.journal else None
        pid = initial.get('pid') if initial else self.data['supervisor_pid']
        identities = [row.get('fresh_hmac_identity') for row in (freeze, stopped, observed)]
        for identity in identities:
            if not isinstance(identity, dict):
                raise Blocked('second approved authenticated source identity missing')
            pids = identity.get('api_pids', [])
            if (identity.get('owner') != OWNER or identity.get('pid') != pid
                    or identity.get('source_tree_sha') != SECOND_SOURCE
                    or identity.get('ready') is not True or identity.get('failed') is not False
                    or not isinstance(pids, list) or len(pids) != 2
                    or any(type(p) is not int or p <= 1 for p in pids) or len(set(pids)) != 2):
                raise Blocked('second approved authenticated source identity differs')
        if (identities[1] != identities[0] or identities[2] != identities[0]
                or freeze.get('runner_pid') != evidence.get('pid') or stopped.get('runner_pid') != evidence.get('pid')
                or observed.get('binary_sha256') != (self.journal['binary_sha256'] if self.journal else self.binary_sha)):
            raise Blocked('second approved source process/binary identity differs')
        return dict(source_post_stop=SECOND_SOURCE, source_observed_at=observed['observed_at'],
            source_evidence_mode='actual_post_stop_observation', source_evidence_sha256=hashes,
            original_ledger_source_end_missing='source_tree_sha_end' not in evidence,
            ledger_source_tree_sha_end=evidence.get('source_tree_sha_end'))

    def third_external_evidence(self,evidence):
        # Use the actual receipt schemas. No original Runner end or post-stop
        # HMAC/binary field is invented; the latter record is a later observation.
        paths=[safe_path(self.root/'.local/testlab'/name) for name in THIRD_RECEIPTS]
        (freeze,stopped,observed),hashes=receipt_snapshots(paths)
        if not all(isinstance(row,dict) for row in (freeze,stopped,observed)):
            raise Blocked('third approved source receipts must be objects')
        observed_run=observed.get('run')
        if not isinstance(observed_run,dict):raise Blocked('third approved post-stop ledger snapshot missing')
        if freeze.get('run_id')!=THIRD_BEFORE or stopped.get('run_id')!=THIRD_BEFORE or observed_run.get('id')!=THIRD_BEFORE:
            raise Blocked('third approved source receipt run differs')
        for key in ('status','sha','started_at','ended_at','selected_suite','source_tree_sha','pid'):
            if observed_run.get(key)!=evidence.get(key):raise Blocked('third approved post-stop ledger snapshot differs')
        if ('source_tree_sha_end' in observed_run or 'source_tree_sha_end_original' not in observed or observed.get('source_tree_sha_end_original') is not None
                or observed.get('do_not_backfill_missing_end') is not True
                or observed.get('observed_source_after_stop')!=THIRD_SOURCE
                or stopped.get('source_tree_sha')!=THIRD_SOURCE
                or freeze.get('run_started_at')!=evidence['started_at']
                or observed.get('active_runs')!=[]
                or observed.get('counts')!={'oid':THIRD_OID,'services':1000,'alerts':100000,'messages':500000,'users':20}
                or observed.get('error_root_message_from_original_case')!='RuntimeError: k6 exited without real summary'
                or stopped.get('formal_performance_accepted') is not False
                or stopped.get('formal_24h_accepted') is not False
                or observed.get('formal_performance_accepted') is not False
                or observed.get('formal_24h_started') is not False):
            raise Blocked('third approved scope/source/nonacceptance receipt differs')
        try:
            def timestamp(value):
                parsed=datetime.datetime.fromisoformat(value)
                if parsed.tzinfo is None:raise ValueError('timezone required')
                return parsed.timestamp()
            start,end,initial,stop,later=map(timestamp,(evidence['started_at'],evidence['ended_at'],freeze['at'],stopped['at'],observed['at']))
            if not start<=initial<=stop<=end+5 or not start<=end<=later:raise ValueError('observation times differ')
        except (ValueError,TypeError,KeyError,OverflowError) as error:
            raise Blocked('third approved source observation times invalid') from error
        initial=self.journal.get('initial_hmac_identity') if self.journal else None
        pid=initial['pid'] if initial else self.data['supervisor_pid']
        identities=[row.get('identity') for row in (freeze,stopped)]
        for current in identities:
            if not isinstance(current,dict):raise Blocked('third approved authenticated stop/freeze identity missing')
            pids=current.get('api_pids',[])
            if (current.get('owner')!=OWNER or current.get('pid')!=pid or current.get('source_tree_sha')!=THIRD_SOURCE
                    or current.get('ready') is not True or current.get('failed') is not False
                    or not isinstance(pids,list) or len(pids)!=2
                    or any(type(p) is not int or p<=1 for p in pids) or len(set(pids))!=2):
                raise Blocked('third approved freeze/stop identity differs')
        if (identities[0]!=identities[1] or initial and identities[0]!=initial
                or any(row.get('runner_pid_snapshot')!=evidence.get('pid') for row in (freeze,stopped))):
            raise Blocked('third approved process/runner identity differs')
        metrics=observed.get('metrics',{})
        if not isinstance(metrics,dict):raise Blocked('third approved post-stop metrics missing')
        postgres=metrics.get('postgres',{});processes=metrics.get('backend_processes')
        if (not isinstance(postgres,dict) or not isinstance(processes,list) or len(processes)!=2 or not all(isinstance(row,dict) and type(row.get('pid')) is int for row in processes)
                or any(type(postgres.get(key)) is not int or postgres.get(key)!=0 for key in ('running_replies','queued_replies','locks','waiting'))
                or [row.get('pid') for row in processes]!=identities[0]['api_pids']):
            raise Blocked('third approved post-stop worker/lock/process observation differs')
        self.third_identity=identities[0]
        return dict(source_post_stop=THIRD_SOURCE,source_observed_at=observed['at'],
            source_evidence_mode='actual_post_stop_observation',source_evidence_sha256=hashes,
            original_ledger_source_end_missing=True,ledger_source_tree_sha_end=None,
            approved_binary_sha256=THIRD_BINARY_SHA,approved_runtime_sha256=THIRD_RUNTIME_SHA)

    def protected_archives_guard(self,catalog):
        if self.before!=THIRD_BEFORE:return
        expected={}
        for run,before,oid in PROTECTED_ARCHIVES:
            nonce=canonical_uuid(run);archive=APP+'_evidence_'+nonce
            tag=json.dumps(dict(schema=1,owner=OWNER,run_id=run,before_evidence_run=before,
                canonical=APP,archive=archive,fresh=APP+'_fresh_'+nonce,kind='archive'),sort_keys=True,separators=(',',':'))
            expected[archive]={'oid':oid,'owner':'testlab','tag':tag}
        if catalog.get('protected_archives')!=expected:
            raise Blocked('third approved prior archive OID/owner/tag differs')
        if self.journal and (self.journal.get('protected_archives_before')!=expected or self.journal.get('stage')=='complete' and self.journal.get('protected_archives_after')!=expected):
            raise Blocked('third approved prior archive journal snapshot differs')

    def pg_guard(self):
        info = guard_pg(inspect_pg(self.data['postgres_container'], self.budget(3)), OWNER,
                        self.journal.get('postgres_image_id') if self.journal else None)
        if not info.get('Id'):
            raise Blocked('exact PostgreSQL container ID required')
        if self.journal and info['Id'] != self.journal['postgres_container_id']:
            raise Blocked('PostgreSQL container was replaced')
        mounts = [x for x in info.get('Mounts', []) if x.get('Destination') == '/var/lib/postgresql/data']
        if len(mounts) != 1 or mounts[0].get('Type') != 'bind' or mounts[0].get('Source') != str(safe_path(Path(self.environment['work']) / 'pgdata')):
            raise Blocked('PostgreSQL data volume is outside the exact original owner directory')
        run_label = info.get('Config', {}).get('Labels', {}).get('aiops.testlab.run')
        if run_label is not None and run_label != self.environment['run_id']:
            raise Blocked('PostgreSQL explicit run label differs')
        return info

    def unchanged(self):
        if digest(self.environment_path) != self.environment_sha or digest(self.backend_path) != self.fixture_sha or digest(self.binary) != self.binary_sha:
            raise Blocked('profile original environment/backend/binary changed')
        if self.before == THIRD_BEFORE and not (self.journal and self.journal.get('stage') == 'complete'):
            if digest(safe_path(self.root/'.local/testlab/runtime-config.json'))!=THIRD_RUNTIME_SHA or source_fingerprint(self.root)!=THIRD_SOURCE:
                raise Blocked('third approved source/runtime changed during partial operation')
        if self.journal and self.journal.get('stage') != 'complete':
            for key, value in [('environment_sha256', self.environment_sha), ('fixture_sha256', self.fixture_sha), ('binary_sha256', self.binary_sha)]:
                if self.journal.get(key) != value:
                    raise Blocked('partial profile original configuration changed')

    def pg(self, statement, database='postgres', read=True):
        if database not in ('postgres', APP, self.archive, self.fresh):
            raise Blocked('profile SQL outside exact operation databases')
        if not read and self.deadline is None:
            raise Blocked('DDL requires explicit apply scope')
        allowed = {
            ('postgres', 'ALTER DATABASE ' + identifier(APP) + ' RENAME TO ' + identifier(self.archive)),
            ('postgres', 'COMMENT ON DATABASE ' + identifier(self.archive) + ' IS ' + quoted(self.tag('archive'))),
            ('postgres', 'CREATE DATABASE ' + identifier(self.fresh) + ' OWNER testlab TEMPLATE template0'),
            ('postgres', 'COMMENT ON DATABASE ' + identifier(self.fresh) + ' IS ' + quoted(self.tag('fresh'))),
            (self.fresh, 'CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;'),
            ('postgres', 'ALTER DATABASE ' + identifier(self.fresh) + ' RENAME TO ' + identifier(APP)),
        }
        if not read and (database, statement) not in allowed:
            raise Blocked('profile mutation is outside the six exact journal operations')
        self.unchanged(); self.ledger_guard(); self.pg_guard()
        app = 'profile-' + ('read-' if read else 'ddl-') + self.nonce
        payload = 'BEGIN READ ONLY; ' + statement + '; COMMIT;' if read else statement
        # Server limits are session options: CREATE/ALTER DATABASE must not be
        # accidentally wrapped in a multi-statement implicit transaction.
        return env._cmd(['docker', 'exec', '-i', '-e', 'PGAPPNAME=' + app,
            '-e', 'PGOPTIONS=-c statement_timeout=7000 -c lock_timeout=2000', self.data['postgres_container'],
            'psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', '-U', 'testlab', '-d', database],
            input=payload, timeout=self.budget()).strip()

    def catalog(self):
        parts = []
        for key, name in [('canonical', APP), ('archive', self.archive), ('fresh', self.fresh)]:
            parts.append(quoted(key) + ", (SELECT json_build_object('oid',d.oid::bigint,'owner',pg_get_userbyid(d.datdba),"
                "'tag',shobj_description(d.oid,'pg_database')) FROM pg_database d WHERE datname=" + quoted(name) + ')')
        parts.append("'connections',COALESCE((SELECT json_agg(json_build_object('pid',pid,'database',datname,'kind',backend_type," 
            "'user',usename,'application',application_name)) FROM pg_stat_activity WHERE datname IN(" +
            ','.join(quoted(x) for x in (APP, self.archive, self.fresh)) + ")), '[]'::json)")
        parts.append("'pending_ddl',(SELECT count(*) FROM pg_stat_activity WHERE application_name=" +
                     quoted('profile-ddl-' + self.nonce) + " AND state='active')")
        if self.before == THIRD_BEFORE:
            protected=[]
            for run,_,_ in PROTECTED_ARCHIVES:
                name=APP+'_evidence_'+canonical_uuid(run)
                protected.append(quoted(name)+",(SELECT json_build_object('oid',d.oid::bigint,'owner',pg_get_userbyid(d.datdba),'tag',shobj_description(d.oid,'pg_database')) FROM pg_database d WHERE datname="+quoted(name)+')')
            parts.append("'protected_archives',json_build_object("+','.join(protected)+')')
        catalog=json.loads(self.pg('SELECT json_build_object(' + ','.join(parts) + ')'))
        self.protected_archives_guard(catalog)
        return catalog

    def layout(self, catalog):
        old = self.journal['original_oid']
        canonical, archive, fresh = (catalog[k] for k in ('canonical', 'archive', 'fresh'))
        for value in (canonical, archive, fresh):
            if value and (type(value.get('oid')) is not int or value.get('owner') != 'testlab'):
                raise Blocked('profile database has unknown OID or owner')
        if canonical and canonical['oid'] == old and not archive and not fresh:
            if canonical['tag'] != self.journal['original_tag']:
                raise Blocked('original APP catalog marker changed')
            return 'original'
        if not archive or archive['oid'] != old or archive['tag'] not in (self.journal['original_tag'], self.tag('archive')):
            raise Blocked('archive does not match original OID and exact journal marker')
        if archive['tag'] != self.tag('archive'):
            if canonical or fresh:
                raise Blocked('unmarked archive has unexpected new databases')
            return 'archive_unmarked'
        if canonical:
            if (fresh or canonical['oid'] != self.journal.get('fresh_oid') or canonical['tag'] != self.tag('fresh')):
                raise Blocked('canonical database is an unknown instance; refusing adoption or re-archive')
            return 'published'
        if not fresh:
            return 'archived'
        if (fresh['oid'] != self.journal.get('fresh_oid') or not self.journal.get('fresh_create_completed')):
            raise Blocked('untagged/unreceipted staging CREATE is uncertain; independent creation evidence required')
        if fresh['tag'] is None:
            return 'fresh_unmarked'
        if fresh['tag'] != self.tag('fresh'):
            raise Blocked('staging marker belongs to an unknown operation')
        return 'fresh_marked'

    def control_request(self, method, path):
        # The owned controller has a tiny, length-delimited HTTP/1.0 contract.
        # Recompute remaining time before connect/send/every recv so a peer
        # dripping headers/body cannot multiply a per-read timeout past 40s.
        if not ((method == 'GET' and re.fullmatch('/attest\\?nonce=[a-f0-9]{32}', path))
                or (method == 'POST' and path == '/shutdown' and self.deadline is not None)):
            raise Blocked('controller operation is outside exact profile scope')
        end = time.monotonic() + self.budget(3)
        headers = 'Host: 127.0.0.1:18560\r\nConnection: close\r\nContent-Length: 0\r\n'
        if method == 'POST':
            secret = self.data['control_secret']
            if not isinstance(secret, str) or any(c in secret for c in '\r\n'):
                raise Blocked('invalid private controller token')
            headers += 'Authorization: Bearer ' + secret + '\r\n'
        wire = (method + ' ' + path + ' HTTP/1.0\r\n' + headers + '\r\n').encode('ascii')
        with socket.socket() as sock:
            sock.settimeout(timeout_for(end, 3)); sock.connect(('127.0.0.1', 18560))
            sock.settimeout(timeout_for(end, 3)); sock.sendall(wire)
            received, header_end, length, status = b'', None, None, None
            while True:
                if header_end is not None and len(received) - header_end == length:
                    timeout_for(end, 3)
                    return status, received[header_end:]
                sock.settimeout(timeout_for(end, 3))
                part = sock.recv(4096)
                if not part:
                    raise OSError('owned controller response closed before its exact receipt')
                received += part
                if len(received) > 32768:
                    raise Blocked('owned controller receipt exceeds bounded size')
                if header_end is None and b'\r\n\r\n' in received:
                    raw, _ = received.split(b'\r\n\r\n', 1); header_end = len(raw) + 4
                    lines = raw.decode('ascii').split('\r\n')
                    match = re.fullmatch(r'HTTP/1\.[01] ([0-9]{3}) [^\r\n]*', lines[0])
                    if not match:
                        raise Blocked('owned controller HTTP status malformed')
                    status = int(match[1]); fields = {}
                    for line in lines[1:]:
                        key, separator, value = line.partition(':')
                        key = key.lower()
                        if not separator or key in fields:
                            raise Blocked('owned controller headers malformed or repeated')
                        fields[key] = value.strip()
                    if 'transfer-encoding' in fields or not re.fullmatch('[0-9]{1,5}', fields.get('content-length', '')):
                        raise Blocked('owned controller receipt requires exact content length')
                    length = int(fields['content-length'])
                    if header_end > 16384 or length > 16384:
                        raise Blocked('owned controller receipt exceeds bounded size')
                if header_end is not None and len(received) - header_end > length:
                    raise Blocked('owned controller sent unexpected receipt bytes')

    def supervisor(self):
        nonce = secrets.token_hex(16)
        status, body = self.control_request('GET', '/attest?nonce=' + nonce)
        if status != 200:
            raise Blocked('owned controller attestation rejected')
        receipt = json.loads(body)
        current = signed_identity(self.data, nonce, receipt)
        self.hmac_receipt = receipt
        for port, pid in zip(PORTS, (current['pid'], *current['api_pids'])):
            if not process_port(pid, port, timeout=self.budget(3)):
                raise Blocked('fresh HMAC process does not own its registered listener')
        allowed = set(current['api_pids'])
        if client_pids(timeout=self.budget(3)) - allowed or api_listener_pids(timeout=self.budget(3)) - allowed:
            raise Blocked('unknown third API or foreign host PostgreSQL client blocks rotation')
        return current

    def shutdown(self):
        if self.deadline is None:
            raise Blocked('controller shutdown requires explicit apply scope')
        self.supervisor()  # Fresh HMAC and exact listener/client ownership.
        try:
            status, _ = self.control_request('POST', '/shutdown')
        except (OSError, subprocess.TimeoutExpired) as error:
            self.save(stop_error_type=type(error).__name__)
        else:
            if status != 202:
                raise Blocked('owned controller shutdown rejected')
        # At most one POST; loss is reconciled only by all exact ports closing.
        while listeners(deadline=self.deadline):
            time.sleep(self.budget(.1))

    def plan(self):
        catalog = self.catalog()
        self.protected_archives_guard(catalog)
        if self.journal:
            state = self.layout(catalog)
        else:
            current = self.supervisor()
            if self.before == THIRD_BEFORE and current != self.third_identity:
                raise Blocked('third approved fresh supervisor differs from freeze/stop process identity')
            canonical = catalog['canonical']
            if not canonical or canonical['owner'] != 'testlab' or catalog['archive'] or catalog['fresh']:
                raise Blocked('canonical APP missing/foreign or archive/staging name already exists')
            if self.before == SECOND_BEFORE and canonical['oid'] != SECOND_OID:
                raise Blocked('second approved evidence original APP OID differs; refusing re-archive')
            if self.before == THIRD_BEFORE and canonical['oid'] != THIRD_OID:
                raise Blocked('third approved original APP OID differs; refusing re-archive')
            state = 'original'
            self.initial_identity = current
        return dict(self.identity_fields(), phase=state, catalog=catalog, environment_run_id=self.environment['run_id'],
            registered_source_tree_sha=self.data['source_tree_sha'], current_source_tree_sha=self.current_source,
            binary_sha256=self.binary_sha, before_evidence=self.evidence, apply_required=True,
            ready=False, operations='authenticated stop; original OID archive; fresh tagged staging; canonical rename')

    def save(self, **fields):
        self.journal.update(fields)
        env._private(self.journal_path, self.journal)
        self.sync_journal_directory()

    def sync_journal_directory(self):
        for directory in (self.work, self.work.parent, self.work.parent.parent):
            fd = os.open(directory, os.O_RDONLY | os.O_NOFOLLOW)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)

    def quiescent(self):
        original_deadline = self.deadline
        self.deadline = min(time.monotonic() + 8, original_deadline)
        try:
            while True:
                catalog = self.catalog()
                if (not catalog['connections'] and catalog['pending_ddl'] == 0
                        and not client_pids(timeout=self.budget(3))
                        and not api_listener_pids(timeout=self.budget(3)) and not listeners(deadline=self.deadline)):
                    return catalog
                if time.monotonic() >= self.deadline:
                    raise Blocked('8-second profile quiescence unproven; no PG backend is terminated')
                time.sleep(self.budget(.2))
        finally:
            self.deadline = original_deadline

    def schema(self, database):
        return json.loads(self.pg("SELECT json_build_object('user_tables',(SELECT count(*) FROM pg_class c JOIN pg_namespace n "
            "ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p')), 'extensions',COALESCE(" 
            "(SELECT json_agg(extname ORDER BY extname) FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace "
            "WHERE n.nspname='public' AND extname IN('vector','pgcrypto')), '[]'::json))", database))

    def mutate(self, phase, statement, database='postgres'):
        before = self.quiescent()
        expected = {'rename_archive_intent': 'original', 'archive_tag_intent': 'archive_unmarked',
                    'create_fresh_intent': 'archived', 'fresh_tag_intent': 'fresh_unmarked',
                    'extensions_intent': 'fresh_marked', 'publish_canonical_intent': 'fresh_marked'}
        if phase not in expected or self.layout(before) != expected[phase]:
            raise Blocked('profile catalog phase changed before mutation')
        self.save(stage=phase, intended_statement_sha256=hashlib.sha256(statement.encode()).hexdigest())
        error = None
        try:
            self.pg(statement, database, read=False)
        except Exception as failure:
            error = failure
            events = [*self.journal.get('transport_errors', []), {'phase': phase, 'error_type': type(error).__name__}]
            self.save(transport_errors=events, last_error_type=type(error).__name__)
        # Every outcome, including transport timeout, requires a new read.
        # No destructive command is retried inside this invocation.
        try:
            catalog = self.catalog()
        except Exception as failure:
            self.save(reconcile_error_type=type(failure).__name__, reconcile_outcome='unverified')
            raise Blocked('profile catalog reconciliation failed; DDL not retried') from (error or failure)
        self.save(last_catalog=catalog, last_operation=phase, last_error_type=type(error).__name__ if error else None)
        if catalog['pending_ddl']:
            raise Blocked('profile DDL remains active; retained journal requires later reconciliation')
        return catalog, error

    def apply(self):
        self.deadline = time.monotonic() + 40
        planned = self.plan()
        if self.journal and self.journal.get('stage') == 'complete':
            if planned['phase'] != 'published':
                raise Blocked('completed profile catalog changed; no repeat rotation allowed')
            return dict(planned, applied=False, already_complete=True)
        if not self.journal:
            self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
            old = planned['catalog']['canonical']
            self.journal = dict(self.identity_fields(), stage='planned', original_oid=old['oid'], original_tag=old['tag'],
                environment_run_id=self.environment['run_id'], environment_sha256=self.environment_sha,
                fixture_sha256=self.fixture_sha, binary_sha256=self.binary_sha,
                registered_source_tree_sha=self.data['source_tree_sha'], current_source_tree_sha=self.current_source,
                postgres_container_id=self.info['Id'], postgres_image_id=self.info['Image'],
                before_evidence=self.evidence, initial_hmac_identity=self.initial_identity,
                initial_hmac_receipt=self.hmac_receipt,
                fresh_oid=None, fresh_create_completed=False, ready=False)
            if self.before == THIRD_BEFORE:
                self.journal.update(runtime_sha256=THIRD_RUNTIME_SHA,protected_archives_before=planned['catalog']['protected_archives'])
            private_file(self.journal_path, json.dumps(self.journal))
            self.sync_journal_directory()
        self.unchanged()
        if listeners(deadline=self.deadline):
            if self.layout(self.catalog()) != 'original':
                raise Blocked('API restarted during partial rotation; refusing stop or DDL')
            self.save(stage='stop_intent')
            self.shutdown()
        self.save(stage='stopped')
        for _ in range(8):
            catalog = self.quiescent()
            phase = self.layout(catalog)
            if phase == 'original':
                observed, error = self.mutate('rename_archive_intent', 'ALTER DATABASE ' + identifier(APP) + ' RENAME TO ' + identifier(self.archive))
                if self.layout(observed) != 'archive_unmarked':
                    raise Blocked('archive rename was not confirmed; no retry') from error
            elif phase == 'archive_unmarked':
                observed, error = self.mutate('archive_tag_intent', 'COMMENT ON DATABASE ' + identifier(self.archive) + ' IS ' + quoted(self.tag('archive')))
                if self.layout(observed) != 'archived':
                    raise Blocked('archive marker was not confirmed; no retry') from error
            elif phase == 'archived':
                observed, error = self.mutate('create_fresh_intent', 'CREATE DATABASE ' + identifier(self.fresh) + ' OWNER testlab TEMPLATE template0')
                fresh = observed['fresh']
                if error or not fresh or fresh['owner'] != 'testlab' or fresh['tag'] is not None or observed['canonical']:
                    raise Blocked('staging CREATE receipt unavailable/unknown; no untagged database adoption') from error
                self.save(stage='fresh_created', fresh_oid=fresh['oid'], fresh_create_completed=True)
            elif phase == 'fresh_unmarked':
                observed, error = self.mutate('fresh_tag_intent', 'COMMENT ON DATABASE ' + identifier(self.fresh) + ' IS ' + quoted(self.tag('fresh')))
                if self.layout(observed) != 'fresh_marked':
                    raise Blocked('fresh marker was not confirmed; no retry') from error
            elif phase == 'fresh_marked':
                contents = self.schema(self.fresh)
                if contents['user_tables'] != 0:
                    raise Blocked('staging contains business tables; refusing canonical promotion')
                if contents['extensions'] != ['pgcrypto', 'vector']:
                    _, error = self.mutate('extensions_intent', 'CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; '
                        'CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;', self.fresh)
                    if self.schema(self.fresh) != {'user_tables': 0, 'extensions': ['pgcrypto', 'vector']}:
                        raise Blocked('empty staging extensions unconfirmed; no retry') from error
                observed, error = self.mutate('publish_canonical_intent', 'ALTER DATABASE ' + identifier(self.fresh) + ' RENAME TO ' + identifier(APP))
                if self.layout(observed) != 'published':
                    raise Blocked('fresh canonical rename unconfirmed; no retry') from error
            elif phase == 'published':
                contents = self.schema(APP)
                if contents != {'user_tables': 0, 'extensions': ['pgcrypto', 'vector']}:
                    raise Blocked('fresh canonical is not empty with exact public extensions')
                if self.before == THIRD_BEFORE:self.save(protected_archives_after=catalog['protected_archives'])
                self.save(stage='complete', last_catalog=catalog, fresh_initial_schema=contents, archive_data_deleted=False,
                          migrations_executed=False, api_started=False, runtime_reinitialized=False, ready=False)
                return dict(self.identity_fields(), applied=True, archive_original_oid=self.journal['original_oid'],
                            fresh_oid=self.journal['fresh_oid'], fresh_initial_schema=contents, archive_data_deleted=False,
                            ready=False, next_step='root initializes runtime and starts verified formal binary separately')
        raise Blocked('profile phase bound exhausted; journal retained')


@contextlib.contextmanager
def exclusive(root):
    path = safe_path(Path(root).resolve() / '.local/testlab/profile.lock')
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise Blocked('private regular profile lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Blocked('another profile owns rotation') from None
        yield
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--owner', required=True)
    parser.add_argument('--run', required=True)
    parser.add_argument('--before-evidence-run', required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    try:
        with exclusive(args.root) if args.apply else contextlib.nullcontext():
            rotation = Rotation(args.root, args.owner, args.run, args.before_evidence_run)
            result = rotation.apply() if args.apply else rotation.plan()
        print(json.dumps(result), flush=True)
    except Exception as error:
        print(json.dumps({'status': 'blocked', 'error_type': type(error).__name__,
                          'error': str(error) if isinstance(error, Blocked) else 'profile failed; private journal retained'}), flush=True)
        return 2
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
