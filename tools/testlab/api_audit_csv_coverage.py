"""Bounded formal audit CSV permissions and read-side-effect coverage.

Discovery ecba6f08 already proves owner/peer/environment/revoked CSV access.
This candidate adds anonymous access, headerless browser download, strict CSV
and complete audit/export read ledgers. Only HTTP service creation precedes it;
no Runbook/investigation/model/external notification or full business chain.
--live remains a parent-reviewed opt-in. No resources are acquired on import.
"""
from __future__ import annotations
import argparse, csv, fcntl, hashlib, importlib.util, io, json, os, re, stat, time, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18750, 18751)
MODULE = 'formal-api-audit-csv'
TARGETS = (('GET', '/audit'), ('GET', '/audit/{id}'),
           ('POST', '/audit/export'), ('GET', '/audit/exports/{id}'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'))
COLUMNS = ['id', 'time', 'actor_id', 'actor_kind', 'action', 'target', 'outcome']
REQUIRED_CASES = tuple('audit_csv.'+x for x in (
    'native_inputs', 'native_export', 'owner_csv', 'headerless_csv', 'anonymous',
    'other_owner', 'wrong_environment', 'revoked', 'restored', 'read_ledger', 'cleanup'))

_spec = importlib.util.spec_from_file_location('audit_csv_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    value = _inspect(*args, **kwargs)
    check(value.get('Id') == PG_CID, 'exact owned PG container differs')
    return value
ro.inspect_pg = inspect_pg

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_audit_csv_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID,
          'frozen source/nonrace binary/runtime/exact PG identity differs')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed checkout required')
    data = ro.doctor(root)
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not (parsed.scheme or parsed.netloc or parsed.query or parsed.fragment),
              'bounded audit CSV path requires no origin/query/fragment')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in (*TARGETS, *SUPPORT):
            raise Blocked('outside independent audit CSV inventory')
        return canonical, kind

def verify_csv(raw, headers, expected, sensitive):
    normalized = {k.lower():v for k,v in headers.items()}
    check(normalized.get('content-type') == 'text/csv; charset=utf-8'
          and normalized.get('content-disposition') == 'attachment; filename="audit.csv"'
          and normalized.get('cache-control') == 'no-store', 'CSV security/attachment headers differ')
    check(isinstance(raw, bytes) and 0 < len(raw) <= 65536, 'bounded CSV bytes required')
    text = raw.decode('utf-8')
    check('\x00' not in text and all(value and value not in text for value in sensitive),
          'CSV exposed private identity, credential or excluded environment marker')
    records = list(csv.reader(io.StringIO(text, newline=''), strict=True))
    check(records and records[0] == COLUMNS and all(len(row) == len(COLUMNS) for row in records[1:]),
          'CSV columns/record width differ; payload/changes must be absent')
    rows = [dict(zip(COLUMNS, row)) for row in records[1:]]
    check(bool(expected) and rows == expected, 'CSV native ordered rows/IDs/times/actors/actions/targets/outcomes differ')
    return hashlib.sha256(raw).hexdigest()

def grant_statements(role):
    check(bool(re.fullmatch(r'readonly-[a-f0-9]{32}-reader', role)), 'exact invocation reader role required')
    where = 'role_id='+q(role)+" AND module='audit' AND action='view' AND scope_kind='environment' AND scope_id='test'"
    remove = 'DELETE FROM v3_role_grants WHERE '+where+';'
    restore = "INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) SELECT "+q(role)+",'audit','view','environment','test' WHERE NOT EXISTS(SELECT 1 FROM v3_role_grants WHERE "+where+');'
    read = 'SELECT count(*) FROM v3_role_grants WHERE '+where+';'
    return remove, restore, read

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.restore_pending, self.export_url = Contract(root), False, None
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS), sql_business_result_writes=0,
                          fixture='HTTP service-created audit only; no Runbook/investigation chain')
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper inputs changed')

    def call(self, *args, **kwargs):
        self.frozen(); return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('audit_csv.'+suffix in REQUIRED_CASES, 'unknown audit CSV marker')
        self.frozen(); emit('audit_csv.'+suffix, 'passed', dict(run_id=self.run_id, **actual))

    def owner(self, deadline=None):
        query = "SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        value = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(value == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag),
              'exact UUID DB/role owner differs')

    def ledger(self):
        self.frozen(); self.owner()
        return json.loads(self.sql("SELECT json_build_object('audit',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t),'exports',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM v3_audit_exports t))", read=True))

    def restore_grant(self):
        if not self.restore_pending: return
        # Cleanup ignores source drift, but never ignores exact PG/UUID ownership.
        # Owner check, restore and readback share one four-second deadline.
        deadline = time.monotonic()+4
        self.owner(deadline=deadline)
        _, restore, read = grant_statements(self.roles['reader'])
        self.pg(restore, self.database, self.db_user, deadline=deadline)
        count = self.pg('BEGIN READ ONLY; '+read+' COMMIT;', self.database, self.db_user, deadline=deadline)
        check(count == '1', 'original reader audit grant not exactly restored')
        self.restore_pending = False

    def read_without_write(self, before, function, *args, **kwargs):
        value = function(*args, **kwargs)
        check(self.ledger() == before, 'audit/export GET or rejection changed complete persisted ledger')
        return value

    def download_csv(self, expected, sensitive, *, index, environment):
        self.frozen()
        check(index in (0,1) and environment in (None,'test') and self.export_url is not None,
              'exact owner CSV request required')
        check(re.fullmatch('/api/v1/audit/exports/audit-[a-f0-9]{36}', self.export_url)
              and 'reader' in self.cookies, 'exact newly issued owner export URL/session required')
        canonical, kind = self.contract.canonical('GET', self.export_url)
        self.calls += 1
        row = dict(name='audit_csv.http.'+str(self.calls), method='GET', path=canonical, route_class=kind,
                   scenario='normal', expected={'status':[200]}, run_id=self.run_id,
                   call_id=self.run_id+'-'+str(self.calls), test_name='FormalAuditCSV', registration_evidence=False)
        headers = {'Origin':ro.BASES[index], 'Cookie':self.cookies['reader']}
        if environment is not None: headers['X-AIOps-Environment'] = environment
        primary = reporting = None
        try:
            status, raw, response = ro.transport(ro.BASES[index], 'GET', self.export_url, headers=headers)
            row['actual'] = {'status':status}
            check(status == 200, 'owner CSV HTTP status differs')
            digest = verify_csv(raw, response, expected, sensitive)
            ro.private_file(self.work/('owner-'+str(self.calls)+'.csv'), raw.decode('utf-8'))
            row.update(status='passed', registration_evidence=True)
        except BaseException as error:
            primary = error; row.setdefault('actual', {'status':None})
            row['actual']['assertion'] = str(error) if isinstance(error, AssertionError) else type(error).__name__
            row['status'] = 'failed'
            try: self.first_red(row)
            except BaseException as caught: reporting = caught
        finally:
            try: ro.append_json(self.work/'interfaces.jsonl', row)
            except BaseException as caught: reporting = reporting or caught
            try:
                fields = {'interface':row} if type(row['actual']['status']) is int else {}
                emit(row['name'], row['status'], row['actual'], **fields)
            except BaseException as caught: reporting = reporting or caught
        if primary is not None: raise primary
        if reporting is not None: raise reporting
        return digest

    def audits(self):
        for env in ('test','staging'):
            self.call('admin', 'POST', '/api/v1/services', {'items':[dict(id='csv-'+self.nonce+'-'+env,tier='T1')]},
                      want=201, environment=env, scenario='fixture')
        before = self.ledger()
        rows = before['audit']
        check(len(rows) == 2 and all(row['action']=='v3.service.created' and row['actor_kind']=='user'
              and row['outcome']=='success' for row in rows), 'two HTTP-created service audit inputs differ')
        test = [x for x in rows if x['changes'].get('environment')=='test']
        hidden = [x for x in rows if x['changes'].get('environment')=='staging']
        check(len(test)==len(hidden)==1 and before['exports']==[], 'fresh native environment audit ledger differs')
        expected = json.loads(self.sql("SELECT json_agg(json_build_object('id',id::text,'time',to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),'actor_id',actor_id,'actor_kind',actor_kind,'action',action,'target',target,'outcome',outcome) ORDER BY id DESC) FROM management_audit WHERE id="+str(test[0]['id']), read=True))
        self.marker_case('native_inputs', dict(service_audits=2, sql_business_seed=False, no_full_chain=True))
        self.read_without_write(before, self.call, 'reader', 'GET', '/api/v1/audit', index=1,
            validate=lambda v:check([x['id'] for x in v['items']]==[str(test[0]['id'])], 'audit list scope differs'))
        self.read_without_write(before, self.call, 'reader', 'GET', '/api/v1/audit/'+str(test[0]['id']),
            validate=lambda v:check(v['id']==str(test[0]['id']) and v['chain_ok'] is True, 'native audit seal differs'))
        exported,_ = self.call('reader', 'POST', '/api/v1/audit/export', {})
        self.export_url = exported.get('url')
        check(isinstance(self.export_url,str) and re.fullmatch('/api/v1/audit/exports/audit-[a-f0-9]{36}',self.export_url),
              'export left exact native local namespace')
        after = self.ledger()
        check(after['audit']==before['audit'] and len(after['exports'])==1, 'export modified audits or made duplicate export')
        export = after['exports'][0]
        check(export['id']==self.export_url.rsplit('/',1)[1] and export['owner_id']==self.users['reader']
              and export['environment_ids']==['test'] and export['includes_global'] is False,
              'export ownership/scope differs')
        self.marker_case('native_export', dict(exports=1, audit_unchanged=True, owner=self.users['reader'], scope=['test']))
        sensitive = (self.password,self.db_password,self.marker,self.admin_email,hidden[0]['target'],
                     *(cookie.split('=',1)[-1] for cookie in self.cookies.values()))
        digest = self.read_without_write(after, self.download_csv, expected, sensitive, index=1, environment='test')
        stored = self.sql("SELECT encode(digest(csv_data,'sha256'),'hex') FROM v3_audit_exports WHERE id="+q(export['id']),read=True)
        check(stored==digest, 'download differs from exact persisted CSV bytes')
        self.marker_case('owner_csv', dict(http200=True, exact_columns=COLUMNS, native_rows=1, csv_sha256=digest,
                                          sensitive_fields_absent=True, hidden_environment_absent=True))
        bare = self.read_without_write(after, self.download_csv, expected, sensitive, index=0, environment=None)
        check(bare==digest, 'headerless browser download differs')
        self.marker_case('headerless_csv', dict(http200=True, persisted_environment_reauthorized=True))
        for suffix,user,env,status,code,index in [('anonymous',None,'test',401,'unauthorized',0),
                ('other_owner','peer','test',404,'not_found',1),('wrong_environment','reader','staging',403,'forbidden',0)]:
            self.read_without_write(after, self.call, user, 'GET', self.export_url,
                                    environment=env,want=status,code=code,scenario='authorization',index=index)
            self.marker_case(suffix, dict(http_status=status,error_code=code,zero_read_mutations=True))
        self.owner()
        remove,_,read = grant_statements(self.roles['reader'])
        check(self.sql(read,read=True)=='1', 'original reader audit view grant differs')
        self.restore_pending = True
        self.sql(remove)
        self.read_without_write(after, self.call, 'reader', 'GET', self.export_url,
                                want=403,code='forbidden',scenario='authorization',index=1)
        self.marker_case('revoked', dict(http403=True, second_api=True, current_grant_checked=True))
        self.restore_grant()
        recovered = self.read_without_write(after, self.download_csv, expected, sensitive, index=1, environment=None)
        check(recovered==digest, 'restored exact grant changed CSV')
        self.marker_case('restored', dict(original_grant_only=True,http200=True,csv_unchanged=True))
        self.marker_case('read_ledger', dict(all_gets_and_denials_unchanged=True,exports=1,service_audits=2))

    def run(self):
        self.provision(); self.start(); self.seed()
        self.sql("INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES("+
                 ','.join(q(v) for v in (self.roles['reader'],'environment','view','environment','staging'))+');')
        for user in ('admin','reader','peer'): self.login(user)
        self.audits(); self.frozen()
        result = dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,
                      helper_sha256=self.data['helper_sha256'],required_cases=REQUIRED_CASES,http_calls=self.calls,
                      reused_original='ecba6f08-a3a5-4fc9-88c2-65f8bfca5075',full_business_chain_verified=False,
                      unverified=['export expiry/worker retention','inflight expiry/revocation','CSV formula business inputs',
                                  'application-selector contract absent; owner/environment only','pagination/capacity','performance/24h'])
        ro._private(self.work/'audit-csv-result.json',result)
        return result

    def cleanup(self):
        error = None
        try: self.restore_grant()
        except BaseException as caught: error = caught
        finally: super().cleanup()
        if error is not None: raise error
        emit('audit_csv.cleanup','passed',dict(owned_uuid_only=True,inherited_cleanup_seconds=40,restore_budget_seconds=4))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT); parser.add_argument('--live',action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('parent-reviewed --live required before resource acquisition')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/audit-csv.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular CSV lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT,doctor(ROOT),str(uuid.uuid4())))
        return 0
    except Blocked as error: emit('audit_csv.environment','blocked',dict(error=str(error))); return 2
    except Exception as error: emit('audit_csv.failure','failed',dict(error_type=type(error).__name__,error=str(error))); return 1
    finally: os.close(fd)

if __name__=='__main__': raise SystemExit(main())
