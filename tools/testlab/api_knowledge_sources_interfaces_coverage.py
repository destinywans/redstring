"""Independent knowledge-source interfaces, with real local owner uploads.

Only explicit --live acquires resources. Source registration and point-in-time
read-only probing do not establish sync, retrieval, index or full-chain proof.
"""
from __future__ import annotations
import argparse, datetime, fcntl, hashlib, importlib.util, json, os, re, socket, stat, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18714, 18715)
MODULE = 'formal-api-knowledge-sources-interfaces'
TARGETS = (('GET', '/knowledge-sources'), ('POST', '/knowledge-sources'),
           ('POST', '/knowledge-sources/test'))
CONNECTION_FIELDS = ('type', 'url', 'secret_ref', 'spaces', 'application_id', 'team_id')
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/uploads'),
           ('POST', '/uploads/{id}/revoke'))
REQUIRED_CASES = tuple('knowledge_interfaces.'+name for name in (
    'http_inputs', 'registration', 'evidence', 'authorization', 'boundaries',
    'probe', 'probe_failure', 'replay_contract', 'audit_rollback', 'revocation', 'audit_consistency'))

_spec = importlib.util.spec_from_file_location('knowledge_interfaces_private_readonly',
                                            ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS = PORTS
ro.BASES = tuple('http://127.0.0.1:'+str(p) for p in PORTS)
ro.GETS = ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect_pg = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs)
    check(info.get('Id') == PG_CID, 'owned PG container was replaced')
    return info
ro.inspect_pg = inspect_pg

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {name: hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_knowledge_sources_interfaces_coverage.py', 'api_readonly_chain.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID,
          'exact source/nonrace binary/runtime/owned PG identity differs')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'helper is bound to its reviewed checkout')
    data = ro.doctor(root)
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment:
            raise Blocked('relative formal HTTP path required')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in (*TARGETS, *SUPPORT):
            raise Blocked('outside independent knowledge-source inventory')
        return canonical, kind

def registration(app, url, name):
    return dict(name=name, type='upload', url=url, secret_ref='', spaces=[],
                schedule='manual', chunk='heading', inherit_acl=True, application_id=app)

def multipart(nonce, body):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and isinstance(body, bytes) and
          0 < len(body) <= 128*1024, 'bounded UUID UTF-8 Markdown fixture required')
    body.decode('utf-8')
    boundary = 'knowledge-'+nonce
    check(boundary.encode() not in body, 'multipart delimiter occurs in fixture')
    name = boundary+'.md'
    raw = ('--'+boundary+'\r\nContent-Disposition: form-data; name="kind"\r\n\r\nfile\r\n--'+
           boundary+'\r\nContent-Disposition: form-data; name="file"; filename="'+name+
           '"\r\nContent-Type: text/markdown\r\n\r\n').encode()+body+('\r\n--'+boundary+'--\r\n').encode()
    return raw, 'multipart/form-data; boundary='+boundary, name

def evidence(value, ids, app):
    check(value.get('complete_v3_dto') is True and value.get('partial') is True and
          value.get('index') is None and value.get('reason') == 'scoped_source_evidence_partial'
          and value.get('truncated') is False, 'honest source DTO metadata differs')
    check(isinstance(value.get('items'), list) and {x['id'] for x in value['items']} == set(ids),
          'exact scoped source IDs differ')
    check({'connection_status', 'sync_status', 'index_status', 'index_settings'} <=
          set(value.get('unknown_fields', [])), 'unknown source evidence omitted')
    for item in value['items']:
        check(item.get('kind') == 'upload' and item.get('application_id') == app and
              item.get('environment_id') == 'test' and item.get('registration_status') == 'unverified',
              'registration ownership/scope/status differs')
        for key in ('last_read', 'keyword_content', 'connection_status', 'sync_status',
                    'document_count', 'chunk_count', 'last_sync_at', 'index_status', 'error'):
            check(key in item and item[key] is None, 'registration/probe fabricated '+key)
        check(not {'url', 'source_url', 'secret_ref', 'binding_snapshot', 'body'} & item.keys(),
              'private source material exposed in DTO')

def probe_result(value):
    check(set(value) == {'ok', 'message', 'connected', 'observed_at', 'persistent', 'scope_verified'}
          and value['ok'] is True and value['connected'] is True and value['persistent'] is False
          and value['scope_verified'] is False and isinstance(value['message'], str),
          'point-in-time probe fabricated persistence or verified scope')
    stamp = datetime.datetime.fromisoformat(value['observed_at'].replace('Z', '+00:00'))
    check(stamp.tzinfo is not None and abs((datetime.datetime.now(datetime.timezone.utc)-stamp).total_seconds()) < 60,
          'probe observation timestamp is not current')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract = Contract(root)
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS),
                          sql_business_result_writes=0, full_business_chain_verified=False)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen()
        frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper inputs changed')

    def marker_case(self, suffix, actual):
        name = 'knowledge_interfaces.'+suffix
        check(name in REQUIRED_CASES, 'unknown knowledge marker')
        self.frozen(); emit(name, 'passed', dict(run_id=self.run_id, **actual))

    def call(self, user, method, path, body=None, *args, **kwargs):
        self.frozen()
        # The strict probe decoder takes connectionRequest, not the create
        # registration DTO. Keep all create fields and boundary values intact.
        if method == 'POST' and path == '/api/v1/knowledge-sources/test' and isinstance(body, dict):
            body = {key:body[key] for key in CONNECTION_FIELDS if key in body}
        return super().call(user, method, path, body, *args, **kwargs)

    def grant(self, user, app):
        for action in ('view', 'manage'):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                     ','.join(q(v) for v in (self.roles[user], 'knowledge', action, 'application', app))+');')

    def upload(self, user, body, environment='test'):
        raw, content_type, filename = multipart(self.nonce, body)
        original = ro.transport
        def transport(base, method, path, payload=None, headers=None, **options):
            check(base in ro.BASES and method == 'POST' and path == '/api/v1/uploads',
                  'multipart substitution outside exact owned upload request')
            wire = dict(headers or {})
            check(wire.get('Cookie') == self.cookies[user] and wire.get('X-AIOps-Environment') == environment,
                  'multipart actor/environment mismatch')
            wire['Content-Type'] = content_type
            return original(base, method, path, raw, wire, **options)
        ro.transport = transport
        try:
            value, _ = self.call(user, 'POST', '/api/v1/uploads', {}, want=201,
                                 environment=environment, scenario='fixture')
        finally: ro.transport = original
        check(re.fullmatch('[a-f0-9]{32}', value.get('id', '')) and value.get('kind') == 'file'
              and value.get('mime') == 'text/markdown' and value.get('name') == filename
              and value.get('size') == len(body) and value.get('url') == '/api/v1/uploads/'+value['id']+'/content',
              'actual HTTP upload metadata differs')
        return value['url']

    def ledger(self):
        # Read the entire fresh UUID source state; hash upload bytes instead of
        # copying private body data into comparison/evidence artifacts.
        tables = ('v3_knowledge_source_registrations', 'v3_knowledge_source_reads',
                  'v3_knowledge_source_observations', 'v3_external_source_documents',
                  'v3_external_source_syncs', 'v3_upload_revocations')
        fields = [q(t)+",(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+t+' t)' for t in tables]
        fields.append("'uploads',(SELECT COALESCE(json_agg((to_jsonb(t)-'content') || jsonb_build_object('content_hash',encode(sha256(content),'hex')) ORDER BY id),'[]'::json) FROM v3_uploads t)")
        fields.append("'audits',(SELECT COALESCE(json_agg(json_build_object('id',id,'actor_id',actor_id,'application_id',application_id,'action',action,'target',target,'outcome',outcome,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit WHERE action LIKE 'v3.knowledge_source.%')")
        return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')', read=True))

    def unchanged(self, before):
        check(self.ledger() == before, 'rejected interface or read changed source/audit state')

    def reject(self, user, method, path, body, status, code, scenario='authorization', environment='test'):
        before = self.ledger()
        self.call(user, method, path, body, want=status, code=code, scenario=scenario, environment=environment)
        self.unchanged(before)

    def assert_new_audits(self, before, after, outcomes, app, actor='reader', target='upload'):
        check({k:v for k,v in before.items() if k != 'audits'} ==
              {k:v for k,v in after.items() if k != 'audits'}, 'probe persisted source content/read/sync')
        old = before['audits']; rows = after['audits'][len(old):]
        check(after['audits'][:len(old)] == old and len(rows) == len(outcomes), 'probe audit prefix/count differs')
        check([r['outcome'] for r in rows] == outcomes, 'probe audit outcomes differ')
        for row in rows:
            check(row['action'] == 'v3.knowledge_source.connection.test' and row['actor_id'] == self.users[actor]
                  and row['application_id'] == app and row['target'] == target and
                  row['changes'] == {'environment_id':'test', 'team_id':''}, 'probe audit scope/redaction differs')

    def probe(self, user, payload, success):
        before = self.ledger()
        self.call(user, 'POST', '/api/v1/knowledge-sources/test', payload, index=1,
                  want=200 if success else 503, code=None if success else 'source_connection_failed',
                  scenario='normal' if success else 'failure', validate=probe_result if success else None)
        self.assert_new_audits(before, self.ledger(), ['attempted', 'success' if success else 'failure'],
                               payload['application_id'], actor=user)

    def inputs(self):
        self.apps = []
        for suffix in ('a','b'):
            key = 'knowledge-'+self.nonce+'-'+suffix
            self.call('admin', 'POST', '/api/v1/services', {'items':[{'id':key, 'tier':'T1'}]}, want=201, scenario='fixture')
            app = self.sql('SELECT id FROM applications WHERE key='+q(key), read=True)
            check(bool(app) and '\n' not in app, 'exact HTTP-produced application missing')
            self.apps.append(app)
        self.grant('reader', self.apps[0]); self.grant('peer', self.apps[1])
        self.body = ('# UUID local source\n\n'+self.marker+' inert synthetic text.\n').encode()
        self.url = self.upload('reader', self.body)
        self.foreign_url = self.upload('hidden', b'# Other environment\n', environment='staging')
        self.payload = registration(self.apps[0], self.url, self.marker)
        self.marker_case('http_inputs', {'http_applications':2, 'real_local_uploads':2,
                                       'external_protocol_mock':False, 'sql_business_seed':False})

    def create(self):
        before = self.ledger()
        value, _ = self.call('reader', 'POST', '/api/v1/knowledge-sources', self.payload, want=201)
        check(set(value) == {'id','registration_status','sync_started'} and value['registration_status'] == 'unverified'
              and value['sync_started'] is False and str(uuid.UUID(value['id'])) == value['id'], 'registration response differs')
        after = self.ledger()
        table = 'v3_knowledge_source_registrations'
        rows = after[table]
        check(len(rows) == len(before[table])+1 and len(after['audits']) == len(before['audits'])+1,
              'registration/audit cardinality differs')
        row = next(r for r in rows if r['id'] == value['id'])
        check(row['created_by'] == self.users['reader'] and row['application_id'] == self.apps[0]
              and row['environment_id'] == 'test' and row['source_url'] == self.url and row['secret_ref'] == ''
              and re.fullmatch('[a-f0-9]{64}', row['binding_fingerprint'])
              and row['binding_snapshot'].get('initial_source_version'), 'native upload registration binding differs')
        audit = after['audits'][-1]
        check(audit['action'] == 'v3.knowledge_source.registered' and audit['target'] == value['id']
              and audit['actor_id'] == self.users['reader'] and audit['application_id'] == self.apps[0]
              and audit['outcome'] == 'success' and audit['changes'] ==
              {'environment_id':'test','source_type':'upload','team_id':''}, 'registration audit/redaction differs')
        for key in before:
            if key not in (table, 'audits'): check(after[key] == before[key], 'registration started content persistence')
        return value['id']

    def listed(self, user, ids, environment='test'):
        before = self.ledger()
        self.call(user, 'GET', '/api/v1/knowledge-sources', scenario='normal' if user=='reader' else 'authorization',
                  environment=environment, index=1, validate=lambda v: evidence(v, ids, self.apps[0]))
        self.unchanged(before)

    def audit_fault(self, enabled):
        if enabled:
            self.sql("CREATE FUNCTION knowledge_interfaces_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $fault$ BEGIN IF NEW.actor_id="+q(self.users['reader'])+" AND NEW.action='v3.knowledge_source.registered' THEN RAISE EXCEPTION 'UUID source registration audit fault'; END IF; RETURN NEW; END $fault$; CREATE TRIGGER knowledge_interfaces_audit_fault BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION knowledge_interfaces_audit_fault();")
        else: self.sql('DROP TRIGGER knowledge_interfaces_audit_fault ON management_audit; DROP FUNCTION knowledge_interfaces_audit_fault();')

    def scenarios(self):
        self.listed('reader', [])
        first = self.create(); self.marker_case('registration', {'native_owner_registration':first, 'sync_started':False})
        self.listed('reader', [first]); self.listed('peer', []); self.listed('hidden', [], 'staging'); self.listed('denied', [])
        self.marker_case('evidence', {'unverified':True, 'index_unknown':True, 'cross_app_env_filtered':True})
        for method, path, body in [('GET','/api/v1/knowledge-sources',None),
                                   ('POST','/api/v1/knowledge-sources',self.payload),
                                   ('POST','/api/v1/knowledge-sources/test',self.payload)]:
            self.reject(None, method, path, body, 401, 'unauthorized')
            self.reject('reader', method, path, body, 403, 'forbidden', environment='staging')
        for user in ('denied','peer'):
            for path in ('/api/v1/knowledge-sources','/api/v1/knowledge-sources/test'):
                self.reject(user, 'POST', path, self.payload, 403, 'forbidden')
        self.reject('reader', 'POST', '/api/v1/knowledge-sources', dict(self.payload, application_id=self.apps[1]), 403, 'forbidden')
        # Separate the app permission gate from the original-upload owner gate.
        self.grant('peer', self.apps[0])
        self.reject('peer', 'POST', '/api/v1/knowledge-sources', self.payload, 422, 'source_upload_unavailable')
        self.marker_case('authorization', {'app_permission_before_lookup':True, 'permitted_nonowner_status':422})
        for patch in ({'name':''}, {'name':'x'*161}, {'type':'unknown'}, {'application_id':''},
                      {'schedule':'hourly'}, {'chunk':'unknown'}, {'inherit_acl':False}, {'spaces':['1']},
                      {'url':'upload://bad'}, {'secret_ref':'vault://invalid/fixture#token'}):
            self.reject('reader', 'POST', '/api/v1/knowledge-sources', dict(self.payload, **patch),
                        422, 'invalid_source_registration', 'boundary')
        for patch, code in (({'application_id':''}, 'invalid_source_scope'),
                            ({'url':'upload://bad'}, 'invalid_source_connection'),
                            ({'secret_ref':'vault://invalid/fixture#token'}, 'invalid_source_connection')):
            self.reject('reader','POST','/api/v1/knowledge-sources/test',dict(self.payload,**patch),422,code,'boundary')
        for path in ('/api/v1/knowledge-sources','/api/v1/knowledge-sources/test'):
            self.reject('reader','POST',path,self.payload,409,'env_view_only','boundary',environment='all')
        self.marker_case('boundaries', {'syntactic_invalid_upload_secret_rejected_without_audit':True})
        self.probe('reader', self.payload, True)
        self.listed('reader', [first])
        self.marker_case('probe', {'actual_local_bytes':True, 'persistent':False, 'scope_verified':False})
        self.probe('peer', self.payload, False)
        foreign = dict(self.payload, url=self.foreign_url)
        self.reject('reader','POST','/api/v1/knowledge-sources',foreign,422,'source_upload_unavailable')
        self.probe('reader', foreign, False)
        self.marker_case('probe_failure', {'same_app_nonowner':True, 'foreign_environment':True, 'audited_outcomes':True})
        # Main POST has no replay/idempotency key contract: two identical
        # requests create independent registrations, each with one audit.
        second = self.create(); check(first != second, 'independent creates reused an ID')
        self.listed('reader', [first,second])
        self.marker_case('replay_contract', {'identical_creates':2, 'distinct_ids':True, 'idempotent':False})
        before = self.ledger(); self.audit_fault(True)
        try:
            self.call('reader','POST','/api/v1/knowledge-sources',dict(self.payload,name='audit-fault-'+self.nonce),
                      want=503,code='audit_unavailable',scenario='failure')
            self.unchanged(before)
        finally: self.audit_fault(False)
        self.marker_case('audit_rollback', {'business_and_audit_unchanged':True})
        upload_id = self.url.split('/')[-2]
        self.call('reader','POST','/api/v1/uploads/'+upload_id+'/revoke',{},
                  validate=lambda v: check(v == {'id':upload_id,'revoked':True},'owner revoke response differs'),scenario='fixture')
        self.reject('reader','POST','/api/v1/knowledge-sources',self.payload,422,'source_upload_unavailable')
        self.probe('reader', self.payload, False)
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='knowledge' AND action='manage';")
        for path in ('/api/v1/knowledge-sources','/api/v1/knowledge-sources/test'):
            self.reject('reader','POST',path,self.payload,403,'forbidden')
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='knowledge' AND action='view';")
        self.listed('reader', [])
        self.marker_case('revocation', {'upload_revoked_via_http':True, 'current_grants_rechecked':True})
        final = self.ledger()
        check(len(final['v3_knowledge_source_registrations']) == 2 and not final['v3_knowledge_source_reads']
              and not final['v3_external_source_syncs'] and len(final['audits']) == 10,
              'final exact source audit/read population differs')
        self.marker_case('audit_consistency', {'registration_audits':2,'probe_audits':8,'source_reads':0})

    def run(self):
        self.provision(); self.start(); self.seed()
        for name in ('admin','reader','peer','hidden','denied'): self.login(name)
        self.inputs(); self.scenarios(); self.frozen()
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA,
            runtime_sha=RUNTIME_SHA, targets=TARGETS, required_cases=REQUIRED_CASES, http_calls=self.calls,
            full_business_chain_verified=False, local_upload_probe=True, external_protocol_mock=False,
            unverified=['external source readers and real credentials','sync/retrieval/vector index',
                        'team ACL combinations','capacity/truncation/pagination/storage failure',
                        'inflight revoke/session expiry/crash recovery','probe second-audit fault',
                        'all upload MIME/size boundaries','inactive application and same-owner cross-environment upload'])
        ro._private(self.work/'knowledge-interfaces-result.json',result)
        return result

    def cleanup(self):
        super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1',port)) != 0,'knowledge API port remains occupied')
        emit('knowledge_interfaces.cleanup','passed',{'ports_free':list(PORTS),'owned_uuid_only':True})

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT); parser.add_argument('--live',action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease acquisition')
    fd = os.open(args.root/'.local/testlab/knowledge-interfaces.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():
            data=doctor(args.root)
            emit('knowledge_interfaces.doctor','passed',{'source_tree_sha':SOURCE_SHA,'ports':PORTS})
            ro.execute(Chain(args.root,data,str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('knowledge_interfaces.environment','blocked',{'error':str(error)}); return 2
    except Exception as error:
        emit('knowledge_interfaces.failure','failed',{'error_type':type(error).__name__,'error':str(error)}); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
