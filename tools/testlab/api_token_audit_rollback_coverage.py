"""Owned UUID Token issue/revoke audit rollback; --live requires root admission.

Only synthetic identity/grant setup and exact own-DB audit fault use SQL writes.
All successful token objects originate at formal HTTP. Secrets remain in memory.
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
PORTS = (18738, 18739)
MODULE = 'formal-api-token-audit-rollback'
TARGETS = (('POST', '/api-tokens'), ('DELETE', '/api-tokens/{id}'))
SUPPORT = (('POST', '/auth/login'), ('GET', '/api-tokens'), ('GET', '/connectors'))
REQUIRED_CASES = tuple('token_audit.' + n for n in (
    'native_issue_consume', 'issue_rollback', 'issue_recovery', 'revoke_rollback',
    'revoke_recovery', 'fault_removed', 'consistency', 'cleanup'))
_spec = importlib.util.spec_from_file_location('token_audit_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect_pg = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs)
    check(info.get('Id') == PG_CID, 'exact owned PG identity differs')
    return info
ro.inspect_pg = inspect_pg

def safe(value):
    """Discard secret/hash fields before diagnostics, files or stdout."""
    if isinstance(value, dict):
        return {k: '[REDACTED]' if any(s in k.lower() for s in ('secret', 'password', 'cookie', 'authorization', 'hash')) else safe(v)
                for k, v in value.items()}
    if isinstance(value, list): return [safe(v) for v in value]
    if isinstance(value, str): return re.sub(r'aiops_pat_[A-Za-z0-9_-]+', '[REDACTED]', value)
    return value

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(safe(dict(name=name, status=status, actual=actual, module=MODULE,
                              evidence_mode='real_local', **fields))), flush=True)
ro.emit = emit

def hashes():
    return {n: hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in
            ('api_token_audit_rollback_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID, 'frozen source/binary/runtime/PG changed')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed root required')
    data = ro.doctor(root)
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not any((parsed.scheme, parsed.netloc, parsed.query, parsed.fragment)), 'exact relative Token path required')
        canonical, kind = super().canonical(method, path)
        check((method, canonical.removeprefix('/api/v1')) in (*TARGETS, *SUPPORT), 'outside Token rollback inventory')
        return canonical, kind

def fault_statements(nonce, actor, action):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and bool(re.fullmatch('[a-f0-9]{32}', actor))
          and action in ('v3.api_token.create', 'v3.api_token.revoke'), 'exact UUID/actor/action fault required')
    name = 'token_audit_fail_' + nonce
    create = ('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='+q(action)+
              ' AND NEW.actor_id='+q(actor)+" AND NEW.changes->>'environment_id'='test' THEN RAISE EXCEPTION 'owned token audit fault'; "+
              'END IF; RETURN NEW; END $$; CREATE TRIGGER '+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop = 'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
    return name, create, drop

def verify_transition(before, after, uid, action):
    """Compare complete hash/state rows in memory; never publish this ledger."""
    old = {r['id']: r for r in before['tokens']}; new = {r['id']: r for r in after['tokens']}
    if action == 'v3.api_token.create':
        check(set(new) == set(old) | {uid} and uid not in old and all(new[k] == v for k, v in old.items()), 'issue recovery changed prior tokens/duplicate row')
        check(new[uid]['revoked_at'] is None and len(new[uid]['digest']) == 64, 'issued token hash/state differs')
    else:
        check(action == 'v3.api_token.revoke' and set(new) == set(old) and uid in old, 'revoke changed token set')
        check(old[uid]['revoked_at'] is None and new[uid]['revoked_at'] is not None, 'revoke state absent')
        check({**new[uid], 'revoked_at': None} == old[uid] and all(new[k] == v for k, v in old.items() if k != uid), 'revoke changed hash/other token fields')
    check(after['audit'][:-1] == before['audit'] and len(after['audit']) == len(before['audit']) + 1
          and after['audit'][-1]['action'] == action and after['audit'][-1]['target'] == uid, 'recovery audit is not exactly once')
    audit = after['audit'][-1]
    check(audit['actor_id'] == new[uid]['owner_id'] and audit['outcome'] == 'success'
          and audit['changes']['environment_id'] == 'test', 'audit actor/outcome/environment differs')
    if action == 'v3.api_token.create':
        check(audit['changes']['scopes'] == ['connectors:view'] and audit['changes']['ttl_seconds'] == 3600,
              'issue audit scope/TTL differs')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.fault_attempted = Contract(root), False

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'] and hashlib.sha256((self.root/'.local/testlab/runtime-config.json').read_bytes()).hexdigest() == RUNTIME_SHA, 'helper/runtime changed')

    def owner(self, deadline=None):
        query = "SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        v = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                              self.database, self.db_user, deadline=deadline))
        check(v == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag), 'own DB/role markers differ')

    def ledger(self):
        self.frozen(); self.owner()
        return json.loads(self.sql("SELECT json_build_object('tokens',(SELECT COALESCE(json_agg(json_build_object('id',id,'owner_id',owner_id,'environment_id',environment_id,'name',name,'scopes',scopes,'digest',encode(secret_hash,'hex'),'created_at',created_at,'expires_at',expires_at,'revoked_at',revoked_at) ORDER BY id),'[]'::json) FROM v3_api_tokens),'audit',(SELECT COALESCE(json_agg(json_build_object('action',action,'target',target,'actor_id',actor_id,'outcome',outcome,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit WHERE action IN ('v3.api_token.create','v3.api_token.revoke')))", read=True))

    def marker_case(self, name, actual):
        check('token_audit.'+name in REQUIRED_CASES, 'unknown marker'); self.frozen()
        emit('token_audit.'+name, 'passed', dict(run_id=self.run_id, **actual))

    def request(self, method, path, body=None, *, want=200, code=None, index=0, bearer=None, scenario='normal'):
        self.frozen(); check(index in (0, 1), 'exact API index required')
        canonical, kind = self.contract.canonical(method, path)
        headers = {'Origin':ro.BASES[index], 'X-AIOps-Environment':'test', 'Content-Type':'application/json'}
        if bearer is None: headers['Cookie'] = self.cookies['reader']
        else:
            check(method == 'GET' and path == '/api/v1/connectors', 'Bearer consumer boundary differs')
            headers['Authorization'] = 'Bearer '+bearer
        self.calls += 1
        row = dict(name='token_audit.http.'+str(self.calls), method=method, path=canonical, route_class=kind,
                   scenario=scenario, run_id=self.run_id, expected=dict(status=[want], error_code=code))
        try:
            status, raw, response_headers = ro.transport(ro.BASES[index], method, path, json.dumps(body).encode() if body is not None else None, headers)
            value = json.loads(raw) if raw else {}
            actual_code = value.get('error', {}).get('code')
            row['actual'] = dict(status=status, error_code=actual_code)
            check(status == want and (code is None or actual_code == code), 'Token HTTP status/error differs')
            if 200 <= status < 300: self.contract.shape(method, path, status, value)
            row.update(status='passed', registration_evidence=200 <= status < 300 and scenario == 'normal')
        except Exception:
            row.setdefault('actual', dict(status=None)); row.update(status='failed', registration_evidence=False)
            self.first_red(safe(row)); ro.append_json(self.work/'interfaces.jsonl', safe(row))
            emit(row['name'], 'failed', row['actual'], interface=row)
            raise AssertionError('Token request failed; secret-free first red retained') from None
        # Never persist response bodies, cookies, bearer values, token hashes or raw errors.
        ro.append_json(self.work/'interfaces.jsonl', safe(row)); emit(row['name'], 'passed', row['actual'], interface=row)
        return value, response_headers

    def issue(self, want=201, index=0, scenario='normal'):
        value, headers = self.request('POST', '/api/v1/api-tokens', dict(name='Owned audit '+self.nonce, scopes=['connectors:view'], ttl=3600), want=want, code='tokens_unavailable' if want == 503 else None, index=index, scenario=scenario)
        if want != 201: return None, None
        check(headers.get('Cache-Control', headers.get('cache-control')) == 'no-store', 'secret response must not cache')
        uid, secret = value.get('id',''), value.get('secret_shown_once','')
        check(bool(re.fullmatch('[a-f0-9]{32}', uid)) and bool(re.fullmatch(r'aiops_pat_[A-Za-z0-9_-]{43}', secret)), 'issued credential identity/encoding differs')
        ledger = self.ledger(); row = next((r for r in ledger['tokens'] if r['id'] == uid), None)
        check(row is not None and row['digest'] == hashlib.sha256(secret.encode()).hexdigest() and row['owner_id'] == self.users['reader'] and row['environment_id'] == 'test', 'native issued hash/owner/environment differs')
        return uid, secret

    def consume(self, secret, want=200, index=1):
        value, _ = self.request('GET', '/api/v1/connectors', want=want, code='unauthorized' if want == 401 else None, index=index, bearer=secret, scenario='normal' if want == 200 else 'authorization')
        if want == 200: check(value.get('items') == [], 'fresh connector consumer not empty')

    def install_fault(self, action):
        self.frozen(); self.owner(); self.fault_attempted = True
        self.sql(fault_statements(self.nonce, self.users['reader'], action)[1])

    def remove_fault(self):
        if not self.fault_attempted: return
        deadline = time.monotonic()+4
        self.owner(deadline=deadline)
        name, _, drop = fault_statements(self.nonce, self.users['reader'], 'v3.api_token.create')
        self.pg(drop, self.database, self.db_user, deadline=deadline)
        query = "SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        v = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                              self.database, self.db_user, deadline=deadline))
        check(v == dict(trigger=False, function=False), 'own fault remains'); self.fault_attempted = False

    def run(self):
        self.provision(); self.start(); self.seed(); self.owner()
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x) for x in (self.roles['reader'],'connectors','manage','environment','test'))+');')
        self.login('reader'); empty = self.ledger(); check(empty == dict(tokens=[], audit=[]), 'fresh Token ledger not empty')
        uid, old_secret = self.issue(); before = self.ledger(); verify_transition(empty, before, uid, 'v3.api_token.create'); self.consume(old_secret)
        self.marker_case('native_issue_consume', dict(tokens=1, audit=1, cross_instance_bearer200=True))
        try:
            self.install_fault('v3.api_token.create'); self.issue(want=503, scenario='failure')
            check(self.ledger() == before, 'issue fault changed hash/state/audit'); self.consume(old_secret)
            self.marker_case('issue_rollback', dict(http503=True, full_token_hash_state_audit_unchanged=True, old_bearer200=True))
        finally: self.remove_fault()
        new_uid, new_secret = self.issue(index=1); after = self.ledger(); verify_transition(before, after, new_uid, 'v3.api_token.create'); self.consume(new_secret, index=0)
        self.marker_case('issue_recovery', dict(one_new_token=True, one_new_audit=True, cross_instance_bearer200=True))
        before = self.ledger()
        try:
            self.install_fault('v3.api_token.revoke')
            self.request('DELETE', '/api/v1/api-tokens/'+uid, want=503, code='tokens_unavailable', scenario='failure', index=1)
            check(self.ledger() == before, 'revoke fault changed hash/state/audit'); self.consume(old_secret)
            self.marker_case('revoke_rollback', dict(http503=True, full_token_hash_state_audit_unchanged=True, old_bearer200=True))
        finally: self.remove_fault()
        self.request('DELETE', '/api/v1/api-tokens/'+uid, want=204, index=1); after = self.ledger(); verify_transition(before, after, uid, 'v3.api_token.revoke'); self.consume(old_secret, want=401)
        self.request('DELETE', '/api/v1/api-tokens/'+uid, want=204, scenario='idempotency')
        check(self.ledger() == after, 'repeat revoke duplicated audit'); self.consume(new_secret, index=0)
        self.marker_case('revoke_recovery', dict(one_revoke_audit=True, repeat_no_audit=True, old_bearer401=True, other_bearer200=True))
        self.marker_case('fault_removed', dict(trigger=False, function=False))
        listing, _ = self.request('GET', '/api/v1/api-tokens', index=1)
        check(set(r['id'] for r in listing['items']) == {uid, new_uid} and len(listing['items']) == 2
              and all('secret_shown_once' not in r and 'secret_hash' not in r for r in listing['items']),
              'native list omits tokens or exposes credentials')
        check(self.ledger() == after, 'list/consumption changed Token ledger')
        self.marker_case('consistency', dict(token_rows=2, create_audits=2, revoke_audits=1, raw_credentials_persisted=False))
        return dict(run_id=self.run_id, required_cases=list(REQUIRED_CASES), http_requests=self.calls, ports=list(PORTS), real_local=True, business_SQL_seed=False, unverified=['full business chain','performance','24h'])

    def cleanup(self):
        error = None
        try: self.remove_fault()
        except BaseException as caught: error = caught
        finally: super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                sock.settimeout(.3); check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned Token API remains')
        if error is not None: raise error
        emit('token_audit.cleanup', 'passed', dict(run_id=self.run_id, ports_free=list(PORTS), owned_UUID_DB_role_process_cleanup_verified=True, exact_fault_absent=True, no_token_file_created=True))

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true'); parser.add_argument('--run-id')
    args = parser.parse_args(argv)
    if not args.live: parser.error('root-reviewed --live required')
    check(args.root.resolve() == ROOT, 'reviewed root required before lease')
    run_id = args.run_id or str(uuid.uuid4()); check(str(uuid.UUID(run_id)) == run_id, 'canonical fresh UUID required')
    fd = os.open(ROOT/'.local/testlab/token-audit.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), run_id))
        return 0
    except Blocked:
        emit('token_audit.environment', 'blocked', dict(reason='owned preparation/live admission blocked')); return 2
    except Exception as error:
        emit('token_audit.failure', 'failed', dict(error_type=type(error).__name__)); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
