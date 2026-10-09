"""Formal HTTP Upload -> imported Runbook -> independent review acceptance.

Preparation is offline. --live is for the reviewing parent to authorize later.
Only synthetic identities/grants and a temporary audit-failure constraint use
SQL writes; every upload, Runbook, review, revision and audit uses native HTTP.
"""
from __future__ import annotations

import argparse
import contextlib
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import socket
import stat
import uuid
from urllib.parse import urlencode

# A private module instance keeps all inherited origin/start/cleanup lookups on
# this group's ports without rebinding the shared api_readonly_chain module.
_spec = importlib.util.spec_from_file_location(
    '_runbook_import_owned_base', Path(__file__).with_name('api_readonly_chain.py'))
base = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(base)
PORTS = (18620, 18621)
BASES = tuple('http://127.0.0.1:' + str(port) for port in PORTS)
MODULE = 'formal-runbook-import'
SOURCE = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
base.PORTS, base.BASES, base.MODULE = PORTS, BASES, MODULE
RAW_LIMIT = (64 << 10) + 4096
NAMES = ('author', 'reviewer1', 'reviewer2', 'viewer', 'crossenv')
GRANTS = {'author': ('test', 'staging'), 'reviewer1': ('test',),
          'reviewer2': ('test',), 'viewer': ('test',), 'crossenv': ('staging',)}


@contextlib.contextmanager
def exclusive(root):
    path = Path(root) / '.local/testlab/api-runbook-import.lock'
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode) or stat.S_IMODE(os.fstat(fd).st_mode) != 0o600:
            raise base.Blocked('private regular Runbook lock required')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise base.Blocked('another Runbook import chain owns ports 18620/18621') from None
        yield
    finally:
        os.close(fd)


def multipart(filename, markdown):
    if (not isinstance(markdown, bytes) or not 0 < len(markdown) <= 16000 or
            filename != 'owned-runbook.md'):
        raise base.Blocked('bounded inert Markdown filename/bytes required')
    boundary = 'runbook-' + secrets.token_hex(16)
    data = (('--' + boundary + '\r\nContent-Disposition: form-data; name="kind"\r\n\r\nfile\r\n'
             '--' + boundary + '\r\nContent-Disposition: form-data; name="file"; filename="' + filename +
             '"\r\nContent-Type: text/markdown\r\n\r\n').encode() + markdown +
            ('\r\n--' + boundary + '--\r\n').encode())
    return data, 'multipart/form-data; boundary=' + boundary


def doctor(root):
    data = base.doctor(root)  # PG/CID/owner/port + read-only nonce/HMAC receipt.
    if data['source_tree_sha'] != SOURCE or data['binary_sha256'] != BINARY:
        raise base.Blocked('reviewed source93f/main6d298 binding changed')
    return data


class Chain(base.Chain):
    def __init__(self, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != base.OWNER:
            raise base.Blocked('canonical invocation UUID and explicit testlab owner required')
        self.root, self.data, self.run_id = Path(root).resolve(), data, run_id
        self.nonce = uuid.UUID(run_id).hex
        self.database = 'aiops_testlab_runbook_' + self.nonce
        self.db_user = 'runbook_' + self.nonce
        self.owner_tag = base.OWNER + ':' + run_id + ':api-runbook-import'
        self.marker = 'rbfixture' + self.nonce
        self.work = self.root / '.local/testlab/environments' / base.OWNER / 'api-runbook-import' / run_id
        if self.work.resolve() != self.work:
            raise base.Blocked('invocation evidence path traverses a symlink')
        self.work.mkdir(parents=True, mode=0o700, exist_ok=False)
        self.password, self.db_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        self.admin_email = 'bootstrap-' + self.nonce + '@testlab.invalid'
        self.users = {name: uuid.uuid4().hex for name in NAMES}
        self.roles = {name: 'runbook-' + self.nonce + '-' + name for name in NAMES}
        self.cookies, self.ids, self.processes = {}, {}, []
        self.calls, self.role_attempted, self.db_attempted = 0, False, False
        self.state = dict(owner=base.OWNER, run_id=run_id, database=self.database, database_user=self.db_user,
                          owner_tag=self.owner_tag, ports=list(PORTS), pids=[],
                          source_tree_sha=data['source_tree_sha'], binary_sha256=data['binary_sha256'],
                          synthetic_input_scope='five identities and environment knowledge/conversation grants only',
                          actual_results_source='native public HTTP and read-only owned PostgreSQL readbacks',
                          model_scope='no inference; embedding disabled by unsupported private URL scheme')
        base._private(self.work / 'state.json', self.state)
        base._private(self.work / 'credentials.json', dict(password=self.password, database_password=self.db_password))
        self.contract = base.Contract(self.root)

    @classmethod
    def from_cleanup_record(cls, root, data, run_id):
        if str(uuid.UUID(run_id)) != run_id or data.get('owner') != base.OWNER:
            raise base.Blocked('canonical recovery UUID and explicit owner required')
        root = Path(root).resolve()
        work = root / '.local/testlab/environments' / base.OWNER / 'api-runbook-import' / run_id
        if work.resolve() != work:
            raise base.Blocked('recovery path traverses a symlink')
        saved = base.private_json(work / 'state.json')
        nonce = uuid.UUID(run_id).hex
        expected = dict(owner=base.OWNER, run_id=run_id, database='aiops_testlab_runbook_' + nonce,
                        database_user='runbook_' + nonce,
                        owner_tag=base.OWNER + ':' + run_id + ':api-runbook-import', ports=list(PORTS))
        if not isinstance(saved, dict) or any(saved.get(k) != v for k, v in expected.items()):
            raise base.Blocked('recovery UUID/database/role/tag/port binding mismatch')
        for port in PORTS:
            with socket.socket() as sock:
                sock.settimeout(.3)
                if sock.connect_ex(('127.0.0.1', port)) == 0:
                    raise base.Blocked('recovery requires both owned listeners absent')
        chain = cls.__new__(cls)
        chain.root, chain.data, chain.run_id, chain.nonce, chain.work = root, data, run_id, nonce, work
        chain.database, chain.db_user, chain.owner_tag = expected['database'], expected['database_user'], expected['owner_tag']
        chain.state, chain.processes = saved, []  # Never adopt or signal stored PIDs.
        chain.role_attempted = chain.db_attempted = True
        chain.cleanup_recovery = True
        return chain

    def pg_application(self, kind, index=None):
        if kind == 'api' and index in (0, 1):
            return 'runbook-api-' + self.nonce + '-' + str(index)
        if kind in ('sql', 'admin') and index is None:
            return 'runbook-' + kind + '-' + self.nonce
        raise base.Blocked('unknown Runbook PostgreSQL application identity')

    def apply_startup_environment(self, variables):
        variables = super().apply_startup_environment(variables)
        # Native approval still attempts indexing. Go's HTTP client rejects this
        # non-HTTP scheme before transport: no network/model invocation occurs.
        variables['EMBEDDING_BASE_URL'] = 'disabled://runbook-no-model'
        return variables

    def seed(self):
        statements = ['BEGIN;']
        for name, uid in self.users.items():
            role = self.roles[name]
            q = base.quote
            statements.extend([
                'INSERT INTO users(id,email,name,role,password_hash) SELECT ' + q(uid) + ',' +
                q('member-' + self.nonce + '-' + name + '@testlab.invalid') + ',' + q(self.marker + ' ' + name) +
                ",'viewer',password_hash FROM users WHERE email=" + q(self.admin_email) + ';',
                'DELETE FROM rbac_user_roles WHERE user_id=' + q(uid) + ';',
                'INSERT INTO rbac_roles(id,name,v3_kind) VALUES(' + q(role) + ',' + q(role) + ",'business');",
                'INSERT INTO rbac_user_roles(user_id,role_id) VALUES(' + q(uid) + ',' + q(role) + ');'])
            for env in GRANTS[name]:
                permissions = [('environment', 'view'), ('conversation', 'view'), ('knowledge', 'view')]
                if name != 'viewer':
                    permissions += [('conversation', 'change'), ('knowledge', 'manage')]
                for module, action in permissions:
                    statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES(' +
                                      ','.join(q(v) for v in (role, module, action, 'environment', env)) + ');')
        statements.append('COMMIT;')
        self.sql('\n'.join(statements))
        counts = json.loads(self.sql("SELECT json_build_object('users',(SELECT count(*) FROM users WHERE id=ANY(ARRAY[" +
                         ','.join(base.quote(x) for x in self.users.values()) + "])), 'global_grants',(SELECT count(*) FROM v3_role_grants WHERE role_id=ANY(ARRAY[" +
                         ','.join(base.quote(x) for x in self.roles.values()) + "]) AND scope_kind='global'))", read=True))
        base.check(counts == {'users': 5, 'global_grants': 0}, 'identity-only fixture readback differs')
        base.emit('runbook.synthetic_identities', 'passed', counts)

    def first_red(self, row):
        for path in (self.work / 'first-failure.json', self.root / '.local/testlab/first-failures' /
                     ('api-runbook-import-' + self.run_id + '.json')):
            path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            try:
                base.private_file(path, json.dumps(row, ensure_ascii=False))
            except FileExistsError:
                pass

    def call(self, user, method, path, body=None, want=200, code=None, scenario='normal', index=0,
             environment='test', raw_body=None, content_type=None, binary=False):
        if (index not in (0, 1) or user not in (None, *NAMES) or environment not in ('test', 'staging', 'all') or
                scenario not in base.WIRE_SCENARIOS):
            raise base.Blocked('Runbook account/origin/environment/scenario boundary mismatch')
        if raw_body is not None:
            if (body is not None or method != 'POST' or path != '/api/v1/uploads' or
                    not isinstance(raw_body, bytes) or not 0 < len(raw_body) <= RAW_LIMIT or
                    not isinstance(content_type, str) or not content_type.startswith('multipart/form-data; boundary=') or
                    '\r' in content_type or '\n' in content_type):
                raise base.Blocked('raw body restricted to bounded native multipart uploads')
        if binary and (method != 'GET' or not base.re.fullmatch(r'/api/v1/uploads/[a-f0-9]{32}/content', path)):
            raise base.Blocked('binary response restricted to owned upload content')
        canonical, route_class = self.contract.canonical(method, path)
        headers = {'Origin': BASES[index], 'X-AIOps-Environment': environment, 'Accept-Language': 'en'}
        if user in self.cookies:
            headers['Cookie'] = self.cookies[user]
        payload = raw_body
        if body is not None:
            payload, content_type = json.dumps(body).encode(), 'application/json'
        if content_type:
            headers['Content-Type'] = content_type
        self.calls += 1
        row = dict(name='runbook.http.' + str(self.calls), method=method, path=canonical, route_class=route_class,
                   scenario=scenario, expected={'status': [want], 'error_code': code}, run_id=self.run_id,
                   call_id=self.run_id + '-' + str(self.calls), test_name='FormalRunbookImportChain')
        try:
            status, raw, response_headers = base.transport(BASES[index], method, path, payload, headers)
            row['actual'] = {'status': status, 'error_code': None}
            value = raw if binary and status == 200 else (json.loads(raw) if raw else {})
            actual_code = value.get('error', {}).get('code') if isinstance(value, dict) else None
            row['actual']['error_code'] = actual_code
            base.check(status == want and (code is None or code == actual_code), 'exact formal HTTP status/error differs')
            if 200 <= status < 300 and not binary:
                self.contract.shape(method, path, status, value)
            row['status'], row['registration_evidence'] = 'passed', 200 <= status < 300 and scenario == 'normal'
        except Exception as error:
            row.setdefault('actual', {'status': None})
            row['status'], row['registration_evidence'] = 'failed', False
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            self.first_red(row)
            if 'raw' in locals():
                base._private(self.work / ('failure-response-' + str(self.calls) + '.json'),
                              dict(status=status, response_body=raw.decode('utf-8', 'replace')))
            base.append_json(self.work / 'interfaces.jsonl', row)
            base.emit(row['name'], 'failed', row['actual'], **({'interface': row} if type(row['actual']['status']) is int else {}))
            raise AssertionError('formal Runbook HTTP assertion failed; private first red retained') from None
        base.append_json(self.work / 'interfaces.jsonl', row)
        base.emit(row['name'], 'passed', row['actual'], interface=row)
        return value, response_headers

    def login(self, name):
        _, headers = self.call(name, 'POST', '/api/v1/auth/login',
            {'email': 'member-' + self.nonce + '-' + name + '@testlab.invalid', 'password': self.password},
            environment='staging' if name == 'crossenv' else 'test')
        cookie = next((v for k, v in headers.items() if k.lower() == 'set-cookie'), '').split(';', 1)[0]
        base.check(cookie.startswith('aiops_session=') and len(cookie) > 40 and '\r' not in cookie and '\n' not in cookie,
                   'native login omitted valid host-local session')
        self.cookies[name] = cookie

    def snapshot(self):
        # No query text, credentials or source body is emitted.
        return json.loads(self.sql("SELECT json_build_object(" + ','.join(
            base.quote(key) + ',(SELECT count(*) FROM ' + table + ')' for key, table in (
                ('uploads', 'v3_uploads'), ('documents', 'knowledge_documents'), ('runbooks', 'v3_runbooks'),
                ('revisions', 'knowledge_revisions'), ('history', 'knowledge_history'), ('reviews', 'v3_runbook_reviews'),
                ('previews', 'v3_runbook_validation_previews'), ('audits', 'management_audit'),
                ('generations', 'knowledge_generations'), ('chunks', 'knowledge_chunks'),
                ('proposals', 'v3_proposals'), ('action_plans', 'action_plans'),
                ('executions', 'executions'), ('actions', 'v3_proposal_actions'))) +
            ", 'document_states',(SELECT COALESCE(json_agg(json_build_object('id',id,'version',version," +
            "'lifecycle',lifecycle,'content_hash',content_hash) ORDER BY id),'[]'::json) FROM knowledge_documents))", read=True))

    def unchanged(self, name, request):
        before = self.snapshot()
        request()
        after = self.snapshot()
        base.check(before == after, name + ' changed persisted business/audit counts')
        base.emit('runbook.no_side_effect.' + name, 'passed', dict(before=before, after=after))

    def state_read(self, runbook, lifecycle, reviews, submissions, previews=0, *, checkpoint):
        value = json.loads(self.sql("SELECT json_build_object('lifecycle',d.lifecycle,'version',d.version," +
            "'owner',d.metadata->>'maintainer_id','source',r.source_ref,'digest',r.source_snapshot->>'content_sha256'," +
            "'revisions',(SELECT count(*) FROM knowledge_revisions WHERE document_id=d.id)," +
            "'reviews',(SELECT count(*) FROM v3_runbook_reviews WHERE document_id=d.id)," +
            "'submissions',(SELECT count(*) FROM knowledge_history WHERE document_id=d.id AND operation='lifecycle.review')," +
            "'previews',(SELECT count(*) FROM v3_runbook_validation_previews WHERE document_id=d.id)) " +
            'FROM knowledge_documents d JOIN v3_runbooks r ON r.document_id=d.id WHERE d.id=' + base.quote(runbook), read=True))
        expected = dict(lifecycle=lifecycle, version=1, owner=self.users['author'], source=self.ids['upload'],
                        digest=self.ids['digest'], revisions=1, reviews=reviews, submissions=submissions, previews=previews)
        base.check(value == expected, 'same-object provenance/lifecycle/revision readback differs')
        base.emit('runbook.same_object.' + checkpoint, 'passed', value)

    def business(self):
        markdown = ('# Owned imported recovery notes\n\nPrivate synthetic input ' + self.nonce +
                    '\n\n```sh\necho review-only-never-execute\n```\n').encode()
        raw, mime = multipart('owned-runbook.md', markdown)
        self.unchanged('unauthenticated_upload', lambda: self.call(None, 'POST', '/api/v1/uploads',
            raw_body=raw, content_type=mime, want=401, scenario='authorization'))
        upload, _ = self.call('author', 'POST', '/api/v1/uploads', raw_body=raw, content_type=mime, want=201)
        base.check(upload['kind'] == 'file' and upload['name'] == 'owned-runbook.md' and upload['size'] == len(markdown),
                   'native multipart upload metadata differs')
        self.ids['upload'] = upload['id']
        base.check(base.re.fullmatch('[a-f0-9]{32}', upload['id']) and
                   upload['url'] == '/api/v1/uploads/' + upload['id'] + '/content', 'noncanonical native upload identity')
        downloaded, _ = self.call('author', 'GET', upload['url'], index=1, binary=True)
        base.check(downloaded == markdown, 'cross-instance upload bytes differ')
        persisted_upload = json.loads(self.sql("SELECT json_build_object('owner',owner_id,'environment',environment_id," +
            "'bytes',size_bytes,'digest',encode(digest(content,'sha256'),'hex'),'audits'," +
            "(SELECT count(*) FROM management_audit WHERE target=v3_uploads.id AND action='v3.upload.created' AND actor_id=" +
            base.quote(self.users['author']) + ")) FROM v3_uploads WHERE id=" + base.quote(upload['id']), read=True))
        base.check(persisted_upload == dict(owner=self.users['author'], environment='test', bytes=len(markdown),
                   digest=hashlib.sha256(markdown).hexdigest(), audits=1), 'native upload digest/scope/audit binding differs')
        draft_path = '/api/v1/runbooks/draft?' + urlencode({'source': 'import', 'from': upload['id']})
        draft, _ = self.call('author', 'GET', draft_path, index=1)
        self.ids['digest'] = hashlib.sha256(markdown).hexdigest()
        base.check(draft['source_sha256'] == self.ids['digest'] and draft['draft_mode'] == 'source_outline' and
                   len(draft['steps']) == 1 and draft['steps'][0]['body'] == markdown.decode() and
                   not draft['steps'][0].get('code') and not draft['steps'][0].get('tool'), 'import outline lost inert source provenance')
        body = dict(title=self.marker + ' Imported Runbook', source='import', **{'from': upload['id']},
                    expected_source_sha256=draft['source_sha256'], steps=draft['steps'], status='draft')
        # Same author has staging grants, so cross-environment rejection reaches
        # upload source ownership validation rather than only a route 403.
        for actor, env in [('reviewer1', 'test'), ('author', 'staging')]:
            self.unchanged('cross_source_' + actor + '_' + env, lambda a=actor, e=env: self.call(a, 'POST',
                '/api/v1/runbooks', body, want=422, code='invalid_source', environment=e, scenario='authorization'))
            self.unchanged('cross_outline_' + actor + '_' + env, lambda a=actor, e=env: self.call(a, 'GET',
                draft_path, want=422, code='invalid_source', environment=e, scenario='authorization'))
        bad_digest = ('0' if self.ids['digest'][0] != '0' else '1') + self.ids['digest'][1:]
        invalid = [('digest', dict(body, expected_source_sha256=bad_digest), 409, 'source_changed'),
                   ('source', dict(body, **{'from': 'f' * 32}), 422, 'invalid_source'),
                   ('step', dict(body, steps=[dict(n=1, kind='invalid', title='bad')]), 422, 'invalid_runbook')]
        for label, candidate, want, code in invalid:
            self.unchanged('invalid_' + label, lambda b=candidate, w=want, c=code: self.call('author', 'POST',
                '/api/v1/runbooks', b, want=w, code=c, scenario='boundary'))
        created, _ = self.call('author', 'POST', '/api/v1/runbooks', body, want=201)
        runbook = created['id']
        self.ids['runbook'] = runbook
        path = '/api/v1/runbooks/' + runbook
        self.state_read(runbook, 'draft', 0, 0, checkpoint='created_draft')
        self.unchanged('draft_hidden_from_reviewer', lambda: self.call('reviewer1', 'GET', path + '/record',
            want=404, code='not_found', scenario='authorization'))
        submitted, _ = self.call('author', 'POST', path + '/submit', {}, index=1)
        base.check(submitted['status'] == 'review', 'native submit omitted review status')
        self.state_read(runbook, 'review', 0, 1, checkpoint='first_submission_review')
        self.unchanged('repeat_submit', lambda: self.call('author', 'POST', path + '/submit', {},
            want=409, code='invalid_state', scenario='idempotency'))
        self.unchanged('self_review', lambda: self.call('author', 'POST', path + '/reviews', {'decision': 'approve'},
            want=403, code='self_review_forbidden', scenario='authorization'))
        changed, _ = self.call('reviewer1', 'POST', path + '/reviews',
                              {'decision': 'request_changes', 'note': 'Independently checked source; author must resubmit.'})
        base.check(changed['status'] == 'draft', 'request_changes did not return same object to draft')
        self.state_read(runbook, 'draft', 1, 1, checkpoint='changes_requested_draft')
        self.unchanged('repeat_changes_review', lambda: self.call('reviewer1', 'POST', path + '/reviews',
            {'decision': 'request_changes', 'note': 'retry'}, want=409, code='invalid_state', scenario='idempotency'))
        # Only a failure constraint is inserted by SQL, never an audit/result.
        constraint = 'runbook_audit_fault_' + self.nonce
        self.sql('ALTER TABLE management_audit ADD CONSTRAINT ' + base.identifier(constraint) +
                 " CHECK (action <> 'runbook.submitted' OR target <> " + base.quote(runbook) + ') NOT VALID;')
        try:
            self.unchanged('submit_audit_rollback', lambda: self.call('author', 'POST', path + '/submit', {},
                want=503, code='knowledge_unavailable', scenario='failure'))
            self.state_read(runbook, 'draft', 1, 1, checkpoint='failed_resubmit_rollback_draft')
        finally:
            self.sql('ALTER TABLE management_audit DROP CONSTRAINT ' + base.identifier(constraint) + ';')
        self.call('author', 'POST', path + '/submit', {})
        approved, _ = self.call('reviewer2', 'POST', path + '/reviews', {'decision': 'approve', 'note': 'Independent approval.'}, index=1)
        base.check(approved['status'] == 'published' and approved['version'] == 1 and
                   approved['semantic_search_ready'] is False and approved['index_retry_required'] is True and
                   approved['index_status'] in ('failed', 'unavailable'), 'publication/index degradation boundary differs')
        self.state_read(runbook, 'published', 2, 2, checkpoint='approved_publication')
        self.unchanged('repeat_approval', lambda: self.call('reviewer2', 'POST', path + '/reviews', {'decision': 'approve'},
            want=409, code='invalid_state', scenario='idempotency'))
        record, _ = self.call('viewer', 'GET', path + '/record', index=1)
        base.check(record['id'] == runbook and record['status'] == 'published' and record['version'] == 'v1' and
                   record['owner'] == self.users['author'] and record['steps'] == draft['steps'] and
                   record['partial'] is True and 'execution_verification' in record['unavailable_fields'], 'persisted record DTO differs')
        detail, _ = self.call('author', 'GET', path)
        base.check(detail['id'] == runbook and detail['source_ref'] == upload['id'] and
                   detail['source_kind'] == 'import' and len(detail['versions']) == 1 and
                   detail['partial'] is True and detail['complete_v3_dto'] is False, 'partial native detail/source/revisions differ')
        self.unchanged('viewer_dry_run', lambda: self.call('viewer', 'POST', path + '/dry-run', {},
            want=404, code='not_found', scenario='authorization'))
        before = self.snapshot()
        preview, _ = self.call('author', 'POST', path + '/dry-run', {}, index=1)
        base.check(preview['mode'] == 'validation_only' and preview['steps'] == 1 and preview['ok'] is False and
                   len(preview['checks']) == 1 and all(c['executed'] is False for c in preview['checks']), 'dry-run pretends execution')
        after = self.snapshot()
        expected = dict(before, previews=before['previews'] + 1, audits=before['audits'] + 1)
        base.check(after == expected, 'dry-run created anything beyond one validation/audit record')
        self.state_read(runbook, 'published', 2, 2, 1, checkpoint='recorded_validation_published')
        history, _ = self.call('author', 'GET', path + '/history', index=1)
        base.check(history['id'] == runbook and len(history['versions']) == 1 and history['versions'][0]['version'] == 1 and
                   {(r['actor_id'], r['decision']) for r in history['reviews']} ==
                   {(self.users['reviewer1'], 'request_changes'), (self.users['reviewer2'], 'approve')} and
                   len(history['validation_previews']) == 1 and history['validation_previews'][0]['id'] == preview['id'] and
                   history['execution_verification_available'] is False, 'history lost actual reviewer/version/preview lineage')
        audit = json.loads(self.sql('SELECT COALESCE(json_agg(json_build_object(\'action\',action,\'actor\',actor_id) ORDER BY id),\'[]\'::json) '
            'FROM management_audit WHERE target=' + base.quote(runbook), read=True))
        base.check([(a['action'], a['actor']) for a in audit] == [('runbook.created', self.users['author']),
            ('runbook.submitted', self.users['author']), ('runbook.review.changes_requested', self.users['reviewer1']),
            ('runbook.submitted', self.users['author']), ('runbook.review.approved', self.users['reviewer2']),
            ('runbook.validation_preview', self.users['author'])], 'native audit lineage has missing or duplicate effects')
        # Only synthetic grant revocation; persisted business rows untouched.
        self.sql('DELETE FROM v3_role_grants WHERE role_id=' + base.quote(self.roles['author']) +
                 " AND module='knowledge' AND action='manage' AND scope_kind='environment' AND scope_id='test';")
        self.unchanged('revoked_manager_preview', lambda: self.call('author', 'POST', path + '/dry-run', {},
            want=404, code='not_found', scenario='authorization'))
        self.unchanged('revoked_manager_create', lambda: self.call('author', 'POST', '/api/v1/runbooks', body,
            want=403, code='forbidden', scenario='authorization'))
        self.call('viewer', 'GET', path + '/history')
        self.unchanged('cross_environment_record', lambda: self.call('crossenv', 'GET', path + '/record',
            environment='staging', want=404, code='not_found', scenario='authorization'))
        base._private(self.work / 'objects.json', dict(upload_id=upload['id'], runbook_id=runbook,
            source_sha256=self.ids['digest'], preview_id=preview['id'], native_audit=audit))

    def run(self):
        self.provision()  # Inherited fresh DB/role/extensions; reads Store baseline only.
        self.start()      # Inherited source/PG/listener/Popen ownership guards.
        self.seed()
        for name in NAMES:
            self.login(name)
        self.frozen()
        self.business()
        self.frozen()
        result = dict(database=self.database, database_user=self.db_user, http_results=self.calls,
            ports=list(PORTS), source_tree_sha=self.data['source_tree_sha'], binary_sha256=self.data['binary_sha256'],
            flow='same upload/digest -> native import -> submit -> changes -> resubmit -> independent approval -> record/history/validation',
            external_calls=False, semantic_search_ready=False,
            unverified=['external model/embedding, GitLab, IdP, real user data', 'frontend UX', 'performance/soak'])
        base._private(self.work / 'result.json', result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=base.ROOT)
    parser.add_argument('--live', action='store_true')
    parser.add_argument('--cleanup-run', help='parent-authorized nonce recovery; refuses live listeners and stored PID adoption')
    args = parser.parse_args()
    if not args.live:
        parser.error('candidate only: parent review and --live required before any HTTP/PG/Docker activity')
    try:
        with exclusive(args.root), base.cooperative_signals():
            data = doctor(args.root)
            chain = (Chain.from_cleanup_record(args.root, data, args.cleanup_run) if args.cleanup_run else
                     Chain(args.root, data, str(uuid.uuid4())))
            base.execute(chain, cleanup_only=bool(args.cleanup_run))
    except base.Blocked as error:
        base.emit('runbook.environment', 'blocked', {'error': str(error)})
        return 2
    except Exception as error:
        base.emit('runbook.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)})
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
