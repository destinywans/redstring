"""Isolated Runbook creation audit rollback/recovery; root admits --live.

SQL writes only synthetic identities/grants and an exact own-UUID audit fault.
The sole target is POST /runbooks; upload/draft/detail are HTTP prerequisites.
No submission, review, execution, indexing, investigation or full chain.
"""
from __future__ import annotations
import argparse, datetime, fcntl, hashlib, importlib.util, json, os, re, socket, stat, time, uuid
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlsplit
import api_upload_audit_rollback_coverage as upload_util

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18742, 18743)
MODULE = 'formal-api-runbook-audit-rollback'
TARGETS = (('POST', '/runbooks'),)
SUPPORT = (('POST', '/auth/login'), ('POST', '/uploads'), ('GET', '/uploads/{id}/content'),
           ('GET', '/runbooks/draft'), ('GET', '/runbooks/{id}'))
REQUIRED_CASES = tuple('runbook_audit.'+n for n in
    ('http_source', 'baseline', 'rollback', 'fault_removed', 'recovery', 'detail_consistency', 'cleanup'))
_spec = importlib.util.spec_from_file_location('runbook_audit_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect_pg = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs)
    host = info.get('HostConfig', {})
    check(info.get('Id') == PG_CID and host.get('NanoCpus') == 4_000_000_000
          and host.get('Memory') == 2 << 30, 'exact owned PG identity/4CPU/2GiB differs')
    return info
ro.inspect_pg = inspect_pg

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in
            ('api_runbook_audit_rollback_coverage.py', 'api_upload_audit_rollback_coverage.py',
             'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID,
          'frozen source/nonrace binary/runtime/PG identity changed')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed root required')
    data = ro.doctor(root)  # Signed nonce/HMAC/controller, image, labels and exact loopback PG.
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not any((parsed.scheme, parsed.netloc, parsed.fragment)), 'relative exact Runbook path required')
        if parsed.query:
            args = parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True)
            check(method == 'GET' and parsed.path == '/api/v1/runbooks/draft'
                  and set(args) == {'source', 'from'} and args['source'] == ['import']
                  and len(args['from']) == 1 and re.fullmatch('[a-f0-9]{32}', args['from'][0]),
                  'only exact owned import draft query allowed')
        canonical, kind = super().canonical(method, path)
        check((method, canonical.removeprefix('/api/v1')) in (*TARGETS, *SUPPORT), 'outside Runbook rollback inventory')
        return canonical, kind

def fault_statements(nonce, actor):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and bool(re.fullmatch('[a-f0-9]{32}', actor)),
          'exact UUID nonce/actor required')
    name = 'runbook_audit_fail_'+nonce
    create = ('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN '+
              "IF NEW.action='runbook.created' AND NEW.actor_id="+q(actor)+
              " AND NEW.changes->>'environment_id'='test' AND NEW.changes->>'source'='import' "+
              "AND NEW.changes->>'status'='draft' THEN RAISE EXCEPTION 'owned Runbook audit fault'; "+
              'END IF; RETURN NEW; END $$; CREATE TRIGGER '+name+
              ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop = 'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
    return name, create, drop

def payload_for(draft, upload_id, body, title):
    steps = [dict(n=1, kind='check', title='Review imported Markdown', body=body.decode(), readonly=True)]
    check(draft.get('source_sha256') == hashlib.sha256(body).hexdigest()
          and draft.get('sources') == ['import:'+upload_id] and draft.get('draft_mode') == 'source_outline'
          and draft.get('steps') == steps and draft.get('missing') == ['service_scope', 'trigger', 'validated_steps'],
          'formal draft lost inert Markdown/byte digest/source binding')
    return dict(title=title, source='import', **{'from':upload_id}, expected_source_sha256=draft['source_sha256'],
                steps=steps, status='draft')

def rendered_body(payload):
    return '# '+payload['title'].strip()+'\n\n## 1. Review imported Markdown\n'+payload['steps'][0]['body']

def verify_rollback(before, after):
    check(after == before, 'audit failure retained/changed document, Runbook, revision, history, audit or source bytes')

def added_once(before, after, key):
    check(len(after) == len(before)+1 and all(row in after for row in before), 'recovery changed old rows or row cardinality: '+key)
    added = [row for row in after if row not in before]
    check(len(added) == 1, 'recovery did not create exactly one row: '+key)
    return added[0]

def verify_recovery(before, after, uid, actor, payload):
    check(bool(re.fullmatch('[a-f0-9]{32}', uid)) and after['uploads'] == before['uploads']
          and after['revocations'] == before['revocations'], 'recovery changed uploaded source bytes or tombstones')
    rows = {key:added_once(before[key], after[key], key) for key in ('documents','runbooks','revisions','history','audit')}
    body = rendered_body(payload); digest = hashlib.sha256(body.encode()).hexdigest()
    metadata = dict(maintainer_id=actor, origin='human', actor_id=actor, visibility='workspace', environment_ids=['test'])
    doc, rb, rev, history, audit = [rows[k] for k in ('documents','runbooks','revisions','history','audit')]
    check(all(doc.get(k) == v for k,v in dict(id=uid, application_id=None, title=payload['title'], body=body,
          source='runbook://v3/'+uid, version=1, content_hash=digest, created_by=actor,
          lifecycle='draft', metadata=metadata, active_generation=None).items()), 'document content/identity/lifecycle differs')
    check(all(rb.get(k) == v for k,v in dict(document_id=uid, source_kind='import', source_ref=payload['from'],
          conversation_id=None, steps=payload['steps'], source_snapshot=dict(source_id=payload['from'], source_kind='import',
          content_sha256=payload['expected_source_sha256']), reviewers=[], when_text='', last_verified_at=None).items()),
          'structured Runbook source/steps differ')
    snap = rev.get('snapshot', {})
    check(rev.get('document_id') == uid and rev.get('version') == 1 and rev.get('created_by') == actor
          and all(snap.get(k) == v for k,v in dict(id=uid, application_id='', title=payload['title'], body=body,
          source='runbook://v3/'+uid, version=1, content_hash=digest, lifecycle='draft', metadata=metadata).items()),
          'revision snapshot is partial or differs from persisted document')
    check(all(history.get(k) == v for k,v in dict(document_id=uid, version=1, actor_id=actor,
          operation='v3.runbook.created').items()), 'Runbook history lineage differs')
    check(all(audit.get(k) == v for k,v in dict(actor_id=actor, application_id='', action='runbook.created',
          target=uid, outcome='succeeded', changes=dict(status='draft', source='import', environment_id='test')).items()),
          'Runbook creation audit lineage differs')
    return rows

def verify_detail(value, uid, payload, owner, revision):
    keys = {'id','title','version','status','base','source_kind','source_ref','owner','scope','environment_ids',
            'application_id','team_id','tags','when','steps','reviewers','versions','usage','incidents',
            'last_verified','complete_v3_dto','partial','unknown_fields'}
    check(isinstance(value, dict) and set(value) == keys, 'Runbook detail DTO fields differ')
    expected = dict(id=uid, title=payload['title'], version='v1', status='draft', base='import:'+payload['from'],
        source_kind='import', source_ref=payload['from'], owner=owner, scope='workspace · env:test',
        environment_ids=['test'], application_id='', team_id='', tags=[], when='', steps=payload['steps'],
        reviewers=[], usage=dict(refs=None, resolve_rate=None, weekly=None, highlight=None), incidents=None,
        last_verified=None, complete_v3_dto=False, partial=True,
        unknown_fields=['usage.refs','usage.resolve_rate','usage.weekly','usage.highlight','incidents',
                        'pending_reviewers','execution_verification','pending_reviewers'])
    check(all(value[k] == v for k,v in expected.items()), 'detail source/bytes/scope or unknown-field contract differs')
    versions = value['versions']
    check(len(versions) == 1 and set(versions[0]) == {'v','who','when'}
          and versions[0]['v'] == 'v1' and versions[0]['who'] == owner, 'detail revision lineage differs')
    at = datetime.datetime.fromisoformat(revision['created_at'].replace('Z', '+00:00')).replace(microsecond=0)
    check(datetime.datetime.fromisoformat(versions[0]['when'].replace('Z','+00:00')) == at,
          'detail version timestamp does not match committed revision')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.fault_attempted = Contract(root), False
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS), business_success_sql_writes=0,
                          full_business_chain_verified=False, model_scope='no model/embedding/probe call')
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper/Store inputs changed')

    def call(self, *args, **kwargs):
        self.frozen(); return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('runbook_audit.'+suffix in REQUIRED_CASES, 'unknown Runbook marker')
        self.frozen(); emit('runbook_audit.'+suffix, 'passed', actual)

    def seed(self):
        super().seed()
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(
            q(v) for v in (self.roles['reader'], 'knowledge', 'view', 'environment', 'test'))+'),('+','.join(
            q(v) for v in (self.roles['reader'], 'knowledge', 'manage', 'environment', 'test'))+');')

    def owner(self, deadline=None):
        query = ("SELECT json_build_object('database',current_database(),'role',current_user,"+
            "'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),"+
            "'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))")
        actual = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(actual == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag),
              'invocation DB/role ownership changed')

    def ledger(self):
        self.frozen(); self.owner()
        parts = []
        for key, table, order in (('documents','knowledge_documents','id'), ('runbooks','v3_runbooks','document_id'),
            ('revisions','knowledge_revisions','document_id,version'), ('history','knowledge_history','id'),
            ('audit','management_audit','id'), ('revocations','v3_upload_revocations','id')):
            parts.append(q(key)+",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY "+order+"),'[]'::json) FROM "+table+' t)')
        parts.append("'uploads',(SELECT COALESCE(json_agg((to_jsonb(t)-'content')||jsonb_build_object("+
                     "'bytes',octet_length(content),'sha256',encode(sha256(content),'hex')) ORDER BY id),'[]'::json) FROM v3_uploads t)")
        return json.loads(self.sql('SELECT json_build_object('+','.join(parts)+')', read=True))

    def install_fault(self):
        self.frozen(); self.owner(); self.fault_attempted = True
        self.sql(fault_statements(self.nonce, self.users['reader'])[1])

    def remove_fault(self):
        if not self.fault_attempted: return
        deadline = time.monotonic()+4
        self.owner(deadline=deadline)
        name, _, drop = fault_statements(self.nonce, self.users['reader'])
        self.pg(drop, self.database, self.db_user, deadline=deadline)
        query = "SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        absent = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(absent == dict(trigger=False, function=False), 'own Runbook audit fault remains')
        self.fault_attempted = False

    def upload(self, body):
        raw, mime, filename = upload_util.multipart(self.nonce, body)
        original = ro.transport
        def transport(base, method, path, payload=None, headers=None, **options):
            check(base == ro.BASES[0] and method == 'POST' and path == '/api/v1/uploads', 'multipart outside owned fixture')
            wire = dict(headers or {})
            check(wire.get('Cookie') == self.cookies['reader'] and wire.get('X-AIOps-Environment') == 'test', 'fixture actor/env differs')
            wire['Content-Type'] = mime
            return original(base, method, path, raw, wire, **options)
        ro.transport = transport
        try:
            value, _ = self.call('reader', 'POST', '/api/v1/uploads', {}, want=201, scenario='fixture',
                validate=lambda v:upload_util.verify_upload(v, filename, body))
        finally: ro.transport = original
        return value

    def source_bytes(self, upload, body):
        self.frozen(); self.contract.canonical('GET', upload['url'])
        status, raw, headers = ro.transport(ro.BASES[1], 'GET', upload['url'], headers={
            'Cookie':self.cookies['reader'], 'X-AIOps-Environment':'test', 'Origin':ro.BASES[1]})
        self.calls += 1
        upload_util.verify_content(status, raw, headers, body)

    def run(self):
        self.provision(); self.start(); self.seed(); self.login('reader')
        body = ('# inert Runbook source\n'+self.nonce+'\n').encode()
        upload = self.upload(body); self.source_bytes(upload, body)
        draft, _ = self.call('reader', 'GET', '/api/v1/runbooks/draft?'+urlencode(dict(source='import', **{'from':upload['id']})),
                             index=1, scenario='fixture')
        payload = payload_for(draft, upload['id'], body, self.marker+' audit Runbook')
        self.marker_case('http_source', dict(upload_id=upload['id'], source_sha256=hashlib.sha256(body).hexdigest(),
                                           bytes=len(body), inert_steps=1, source='import'))
        before = self.ledger()
        check(all(before[k] == [] for k in ('documents','runbooks','revisions','history')) and len(before['uploads']) == 1,
              'fresh Runbook ledger is not empty or source is not unique')
        check(before['uploads'][0]['id'] == upload['id'] and before['uploads'][0]['owner_id'] == self.users['reader']
              and before['uploads'][0]['sha256'] == hashlib.sha256(body).hexdigest(), 'owned HTTP source ledger differs')
        self.marker_case('baseline', dict(runbook_rows=0, uploaded_sources=1, SQL_business_success_writes=0))
        try:
            self.install_fault()
            self.call('reader', 'POST', '/api/v1/runbooks', payload, want=503, code='knowledge_unavailable', scenario='failure')
            verify_rollback(before, self.ledger())
            self.marker_case('rollback', dict(http_status=503, error_code='knowledge_unavailable',
                unchanged=['documents','runbooks','revisions','history','audit','uploads','revocations']))
        finally: self.remove_fault()
        self.marker_case('fault_removed', dict(trigger=False, function=False))
        value, _ = self.call('reader', 'POST', '/api/v1/runbooks', payload, want=201,
            validate=lambda v:check(isinstance(v, dict) and set(v) == {'id'} and re.fullmatch('[a-f0-9]{32}', v['id']),
                                   'creation response identity differs'))
        after = self.ledger()
        rows = verify_recovery(before, after, value['id'], self.users['reader'], payload)
        self.marker_case('recovery', dict(http_status=201, id=value['id'], exactly_once=['documents','runbooks','revisions','history','audit']))
        self.call('reader', 'GET', '/api/v1/runbooks/'+value['id'], index=1, scenario='fixture',
            validate=lambda v:verify_detail(v, value['id'], payload, self.marker+' reader', rows['revisions']))
        self.source_bytes(upload, body)
        verify_rollback(after, self.ledger())
        self.marker_case('detail_consistency', dict(id=value['id'], cross_instance=True, source_bytes_unchanged=True,
            complete_v3_dto=False, partial=True, read_side_effects=0))
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA, runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'], database=self.database, ports=list(PORTS), http_requests=self.calls,
            target_scope='POST Runbook creation audit rollback and recovery only', full_business_chain_verified=False,
            unverified=['submission/reviews (reuse original evidence)','source variants other than import','concurrent revocation',
                        'indexing/model/tool execution','full chain','performance','24h soak'])
        ro._private(self.work/'runbook-audit-result.json', result)
        return result

    def cleanup(self):
        error = None
        try: self.remove_fault()
        except BaseException as caught: error = caught
        finally: super().cleanup()  # Held Popen, tagged UUID DB/role; inherited shared 40s budget.
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned Runbook API listener remains')
        if error is not None: raise error
        emit('runbook_audit.cleanup', 'passed', dict(ports_free=list(PORTS), owned_uuid_only=True))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resource acquisition')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/runbook-audit.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('runbook_audit.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error:
        emit('runbook_audit.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
