"""One isolated upload audit-failure/rollback/recovery interface case.

Resource acquisition requires root-reviewed --live. No model, knowledge source,
incident, revocation or full-chain execution is part of this package.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, importlib.util, json, os, re, socket, stat, time, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18726, 18727)
MODULE = 'formal-api-upload-audit-rollback'
TARGETS = (('POST', '/uploads'),)
SUPPORT = (('POST', '/auth/login'), ('GET', '/uploads/{id}/content'))
REQUIRED_CASES = tuple('upload_audit.'+suffix for suffix in (
    'empty_storage', 'rollback', 'fault_removed', 'recovery', 'content_bytes', 'cleanup'))

_spec = importlib.util.spec_from_file_location('upload_audit_private_readonly',
                                            ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect_pg = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs)
    check(info.get('Id') == PG_CID, 'exact owned PG container changed')
    return info
ro.inspect_pg = inspect_pg

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_upload_audit_rollback_coverage.py', 'api_readonly_chain.py',
                         'runner.py', 'store.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID,
          'exact source/nonrace binary/runtime/PG identity differs')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed checkout required')
    data = ro.doctor(root)  # Retains signed nonce/HMAC/controller and PG ownership checks.
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not (parsed.scheme or parsed.netloc or parsed.fragment or parsed.query),
              'relative exact upload path required')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in (*TARGETS, *SUPPORT):
            raise Blocked('outside upload audit rollback inventory')
        return canonical, kind

def multipart(nonce, body):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and isinstance(body, bytes)
          and 0 < len(body) <= 4096, 'bounded synthetic Markdown fixture required')
    body.decode('utf-8')
    boundary = 'upload-audit-'+nonce
    check(boundary.encode() not in body, 'fixture contains multipart delimiter')
    filename = boundary+'.md'
    raw = ('--'+boundary+'\r\nContent-Disposition: form-data; name="kind"\r\n\r\nfile\r\n--'+
           boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+filename+
           '"\r\nContent-Type: text/markdown\r\n\r\n').encode()+body+('\r\n--'+boundary+'--\r\n').encode()
    return raw, 'multipart/form-data; boundary='+boundary, filename

def fault_statements(nonce, actor):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and bool(re.fullmatch('[a-f0-9]{32}', actor)),
          'audit fault requires exact invocation UUID and actor')
    name = 'upload_audit_fail_'+nonce
    create = ('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN '+
              "IF NEW.action='v3.upload.created' AND NEW.actor_id="+q(actor)+
              " AND NEW.changes->>'environment_id'='test' THEN RAISE EXCEPTION 'owned upload audit fault'; "+
              'END IF; RETURN NEW; END $$; CREATE TRIGGER '+name+
              ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop = ('BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; '+
            'DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;')
    return name, create, drop

def verify_upload(value, filename, body):
    check(isinstance(value, dict) and re.fullmatch('[a-f0-9]{32}', value.get('id', ''))
          and value.get('kind') == 'file' and value.get('name') == filename
          and value.get('mime') == 'text/markdown' and value.get('size') == len(body)
          and value.get('url') == '/api/v1/uploads/'+value['id']+'/content',
          'recovered upload metadata/size/canonical URL differs')
    check(not {'text', 'content', 'body'} & value.keys(), 'response leaked upload bytes')

def verify_content(status, raw, headers, body):
    h = {key.lower():value for key,value in headers.items()}
    check(status == 200 and raw == body, 'owner download did not return exact synthetic bytes')
    check(h.get('content-length') == str(len(body)) and h.get('content-type') == 'application/octet-stream'
          and h.get('cache-control') == 'no-store' and h.get('x-content-type-options') == 'nosniff'
          and h.get('content-security-policy') == 'sandbox'
          and h.get('content-disposition', '').startswith('attachment;'),
          'owner byte response size/download/security headers differ')

def verify_recovery(before, after, uid, actor, filename, body):
    check(before == dict(uploads=[], revocations=[], audit=[]), 'fresh upload storage is not empty')
    check(after['revocations'] == [] and len(after['uploads']) == 1 and len(after['audit']) == 1,
          'recovery left duplicate bytes/rows/revocations/audit')
    row = after['uploads'][0]
    check(row == dict(id=uid, owner_id=actor, environment_id='test', kind='file', name=filename,
                      size=len(body), mime='text/markdown', url='', bytes=len(body),
                      sha256=hashlib.sha256(body).hexdigest()), 'stored blob/metadata ownership or bytes differ')
    check(after['audit'][0] == dict(actor_id=actor, target=uid, outcome='success', kind='file',
                                  size=len(body), mime='text/markdown', environment_id='test'),
          'recovered upload audit ownership/metadata differs')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.fault_attempted = Contract(root), False
        self.temp = self.work/'upload-tmp'
        self.temp.mkdir(mode=0o700)
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS),
                          blob_store='v3_uploads.content bytea; isolated TMPDIR also inspected',
                          business_success_sql_writes=0, full_business_chain_verified=False)
        ro._private(self.work/'state.json', self.state)

    def startup_environment(self):
        return {'TMPDIR':str(self.temp)}

    def frozen(self):
        super().frozen()
        frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper/Store inputs changed')

    def call(self, *args, **kwargs):
        self.frozen()
        return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('upload_audit.'+suffix in REQUIRED_CASES, 'unknown upload marker')
        self.frozen(); emit('upload_audit.'+suffix, 'passed', actual)

    def owner(self, deadline=None):
        # Restoration remains possible after source drift; still require exact
        # invocation DB/role/tags and the unchanged owned PG container.
        query = ("SELECT json_build_object('database',current_database(),'role',current_user,"+
            "'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),"+
            "'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))")
        actual = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                                   self.database, self.db_user, deadline=deadline))
        check(actual == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag,
                             role_tag=self.owner_tag), 'invocation DB/role ownership changed')

    def files(self):
        check(self.temp.resolve() == self.temp and self.temp.is_dir(), 'owned upload TMPDIR changed')
        inventory = {}
        for path in self.temp.rglob('*'):
            check(not path.is_symlink(), 'upload TMPDIR contains a symlink')
            if path.is_dir(): continue
            check(path.is_file() and path.stat().st_size <= ro.LIMIT, 'unsupported upload TMPDIR artifact')
            inventory[str(path.relative_to(self.temp))] = hashlib.sha256(path.read_bytes()).hexdigest()
        return inventory

    def ledger(self):
        self.frozen(); self.owner()
        return json.loads(self.sql("SELECT json_build_object('uploads',(SELECT COALESCE(json_agg(json_build_object("+
            "'id',id,'owner_id',owner_id,'environment_id',environment_id,'kind',kind,'name',name,"+
            "'size',size_bytes,'mime',mime,'url',url,'bytes',octet_length(content),'sha256',encode(sha256(content),'hex')) ORDER BY id),'[]'::json) FROM v3_uploads),"+
            "'revocations',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM v3_upload_revocations t),"+
            "'audit',(SELECT COALESCE(json_agg(json_build_object('actor_id',actor_id,'target',target,'outcome',outcome,"+
            "'kind',changes->>'kind','size',(changes->>'size')::integer,'mime',changes->>'mime',"+
            "'environment_id',changes->>'environment_id') ORDER BY id),'[]'::json) FROM management_audit WHERE action='v3.upload.created'))", read=True))

    def install_fault(self):
        self.frozen(); self.owner()
        self.fault_attempted = True  # Also restore if transport/commit outcome is unknown.
        self.sql(fault_statements(self.nonce, self.users['reader'])[1])

    def remove_fault(self):
        if not self.fault_attempted: return
        deadline = time.monotonic()+4
        self.owner(deadline=deadline)
        name, _, drop = fault_statements(self.nonce, self.users['reader'])
        self.pg(drop, self.database, self.db_user, deadline=deadline)
        query = "SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        residual = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                                     self.database, self.db_user, deadline=deadline))
        check(residual == dict(trigger=False, function=False), 'own audit fault was not removed')
        self.fault_attempted = False

    def upload(self, body, want, scenario, index=0):
        raw, media_type, filename = multipart(self.nonce, body)
        original = ro.transport
        def transport(base, method, path, payload=None, headers=None, **options):
            check(base == ro.BASES[index] and method == 'POST' and path == '/api/v1/uploads',
                  'multipart substitution outside exact owned upload request')
            wire = dict(headers or {})
            check(wire.get('Cookie') == self.cookies['reader'] and wire.get('X-AIOps-Environment') == 'test',
                  'multipart actor/environment mismatch')
            wire['Content-Type'] = media_type
            return original(base, method, path, raw, wire, **options)
        ro.transport = transport
        try:
            result, _ = self.call('reader', 'POST', '/api/v1/uploads', {}, want=want,
                code='uploads_unavailable' if want == 503 else None, scenario=scenario, index=index,
                validate=lambda value:verify_upload(value, filename, body))
        finally: ro.transport = original
        return result, filename

    def content(self, value, body):
        self.frozen()
        path = value['url']; canonical, kind = self.contract.canonical('GET', path)
        self.calls += 1
        row = dict(name='upload_audit.content_bytes', method='GET', path=canonical, route_class=kind,
            scenario='normal', scenario_detail='fixture', expected={'status':[200]}, run_id=self.run_id,
            call_id=self.run_id+'-'+str(self.calls), test_name='FormalUploadAuditRollback',
            registration_evidence=False, actual={'status':None})
        try:
            status, raw, headers = ro.transport(ro.BASES[1], 'GET', path, headers={
                'Cookie':self.cookies['reader'], 'X-AIOps-Environment':'test', 'Origin':ro.BASES[1]})
            row['actual'] = dict(status=status, bytes=len(raw), sha256=hashlib.sha256(raw).hexdigest())
            verify_content(status, raw, headers, body)
            row['status'] = 'passed'
        except Exception as error:
            row['status'] = 'failed'; row['actual']['assertion'] = type(error).__name__
            self.first_red(row); ro.append_json(self.work/'interfaces.jsonl', row)
            emit(row['name'], 'failed', row['actual'], **({'interface':row} if type(row['actual']['status']) is int else {}))
            raise AssertionError('owned upload content byte assertion failed; first red retained') from None
        ro.append_json(self.work/'interfaces.jsonl', row)
        emit(row['name'], 'passed', row['actual'], interface=row)

    def run(self):
        self.provision(); self.start(); self.seed(); self.login('reader')
        body = ('# inert upload audit fixture\n'+self.nonce+'\n').encode()
        before, files = self.ledger(), self.files()
        check(before == dict(uploads=[], revocations=[], audit=[]) and files == {}, 'fresh upload store not empty')
        self.marker_case('empty_storage', dict(rows=0, content_bytes=0, upload_audits=0, tmp_files=0))
        try:
            self.install_fault()
            self.upload(body, 503, 'failure')
            check(self.ledger() == before and self.files() == files, 'audit failure retained bytes/rows/audit/blob files')
            self.marker_case('rollback', dict(http_status=503, error_code='uploads_unavailable',
                rows=0, content_bytes=0, upload_audits=0, residual_tmp_files=0))
        finally: self.remove_fault()
        self.marker_case('fault_removed', dict(trigger=False, function=False))
        value, filename = self.upload(body, 201, 'normal', index=1)
        after = self.ledger()
        verify_recovery(before, after, value['id'], self.users['reader'], filename, body)
        check(self.files() == files, 'successful bytea upload left unexpected filesystem blobs')
        self.marker_case('recovery', dict(http_status=201, upload_id=value['id'], rows=1,
            content_bytes=len(body), sha256=hashlib.sha256(body).hexdigest(), upload_audits=1, residual_tmp_files=0))
        self.content(value, body)
        check(self.ledger() == after and self.files() == files, 'owner download mutated upload/audit/blob state')
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA,
            runtime_sha=RUNTIME_SHA, helper_sha256=self.data['helper_sha256'], database=self.database,
            ports=list(PORTS), http_requests=self.calls, target_scope='upload creation audit rollback and recovery only',
            storage_scope='all fresh-DB bytea upload rows and own TMPDIR; current service has no file-backed store',
            unverified=['other upload formats','slow-body expiry/revocation (reuse prior original cases)',
                        'token interfaces','knowledge-source sync','full chain','performance','24h soak'])
        ro._private(self.work/'upload-audit-result.json', result)
        return result

    def cleanup(self):
        error = None
        try: self.remove_fault()
        except BaseException as caught: error = caught
        finally: super().cleanup()  # Exact held Popen/DB/role cleanup always runs.
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned upload API listener remains')
        if error is not None: raise error
        emit('upload_audit.cleanup', 'passed', dict(ports_free=list(PORTS), owned_uuid_only=True))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resource acquisition')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/upload-audit.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600,
              'private regular upload lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('upload_audit.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error:
        emit('upload_audit.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
