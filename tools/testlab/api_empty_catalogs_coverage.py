"""Formal empty MCP/service catalogs in a root-admitted, isolated UUID database.

Only login/logout and the two catalog GETs. SQL writes synthetic identities and
grants only; no MCP registration, service, deployment, model or producer is made.
Empty collection success never establishes the corresponding nonempty capability.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, importlib.util, json, os, socket, stat, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18766, 18767)
MODULE = 'formal-api-empty-catalogs'
TARGETS = (('GET', '/mcp-servers'), ('GET', '/services'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/auth/logout'))
SERVICE_BOUNDARIES = ('unexpected=1', 'tier=T9', 'status=healthy', 'q=one&q=two')
REQUIRED_CASES = tuple('empty_catalogs.'+name for name in
    ('baseline', 'normal', 'authorization', 'boundary', 'session_revoked', 'session_recovered', 'consistency', 'cleanup'))
TABLES = ('applications', 'application_environments', 'v3_application_environments',
          'v3_service_environment_profiles', 'ai_extensions', 'ai_extension_revisions',
          'v3_mcp_environment_bindings', 'v3_mcp_discovery_snapshots',
          'v3_mcp_tool_reviews', 'v3_mcp_tool_review_decisions')
_spec = importlib.util.spec_from_file_location('empty_catalogs_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote
_inspect_pg = ro.inspect_pg


def inspect_pg(*args, **kwargs):
    info = _inspect_pg(*args, **kwargs); host = info.get('HostConfig', {})
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
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest() for name in
            ('api_empty_catalogs_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py')}


def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID, 'frozen source/binary/runtime/PG changed')


def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed root required')
    data = ro.doctor(root)  # Original nonce/HMAC, listener, PG image/label/loopback guards.
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())


class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not any((parsed.scheme, parsed.netloc, parsed.fragment)), 'exact relative empty-catalog path required')
        if parsed.query:
            check(method == 'GET' and parsed.path == '/api/v1/services'
                  and parsed.query in SERVICE_BOUNDARIES, 'only exact reviewed invalid service filters allowed')
        canonical, kind = super().canonical(method, path)
        check((method, canonical.removeprefix('/api/v1')) in (*TARGETS, *SUPPORT), 'outside empty-catalog inventory')
        return canonical, kind


def mcp_empty_dto():
    labels = (
        ('servers', 'Bound registrations', 'Only explicit environment bindings visible to you'),
        ('bindings', 'Registered read-only bindings', 'Not V3 reviewed or executable'),
        ('tools', 'Current observed tools', 'Unknown without current discovery; declarations untrusted'))
    return dict(kpis=[dict(id=key, label=label, value='0', sub=sub, series=[]) for key,label,sub in labels],
                items=[], complete_v3_dto=False, unavailable_fields=['unbound_legacy_registrations',
                'connection_health', 'agent_authorized_apps', 'call_metrics', 'pending_change'])


def verify_mcp(value):
    check(isinstance(value, dict) and type(value.get('complete_v3_dto')) is bool
          and value == mcp_empty_dto(), 'empty MCP DTO fabricated/omitted observations or registrations')


def services_empty_dto():
    kpis = [dict(id=key, label=label, value='0', series=[], **({'tone':tone} if tone else {})) for key,label,tone in
            (('n','Services',''), ('bad','Degraded','danger'), ('warn','Alerting','warn'),
             ('budget','Budget < 50%','warn'), ('rel','Releases this week',''))]
    facets = [dict(key=key, title=title, items=[]) for key,title in
              (('team','Team'), ('tier','Tier'), ('status','Status'), ('env','Environment'))]
    return dict(kpis=kpis, items=[], facets=facets)


def verify_services(value):
    check(isinstance(value, dict) and value == services_empty_dto(), 'empty service DTO fabricated rows, facets or measured state')


def verify_empty_ledger(value):
    check(isinstance(value, dict) and set(value) == {*TABLES, 'audit'}
          and all(value[name] == [] for name in TABLES) and isinstance(value['audit'], list),
          'catalog database contains registrations, services, discovery or production evidence')


def verify_unchanged(before, after):
    check(after == before, 'catalog/rejected reads or session lifecycle changed business rows or audits')


class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract = Contract(root)
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS), business_success_sql_writes=0,
            actual_results_source='formal empty MCP/service catalogs', empty_collection_only=True,
            nonempty_capabilities_verified=False, model_scope='no inference/probe/embedding', full_business_chain_verified=False)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper/Store inputs changed')

    def call(self, *args, **kwargs):
        self.frozen(); return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('empty_catalogs.'+suffix in REQUIRED_CASES, 'unknown empty-catalog marker')
        self.frozen(); emit('empty_catalogs.'+suffix, 'passed', dict(run_id=self.run_id, **actual))

    def owner(self):
        query = "SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        actual = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user))
        check(actual == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag),
              'UUID database/role owner differs')

    def ledger(self):
        self.frozen(); self.owner()
        parts = [q(table)+",(SELECT COALESCE(json_agg(to_jsonb(t)),'[]'::json) FROM "+table+' t)' for table in TABLES]
        parts.append("'audit',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t)")
        return json.loads(self.sql('SELECT json_build_object('+','.join(parts)+')', read=True))

    def catalogs(self, index, scenario='normal'):
        for _, path in TARGETS:
            value, headers = self.call('reader', 'GET', '/api/v1'+path, index=index, scenario=scenario,
                validate=verify_mcp if path == '/mcp-servers' else verify_services)
            if path == '/services':
                check({k.lower():v for k,v in headers.items()}.get('cache-control') == 'no-store',
                      'empty service catalog omitted no-store')

    def run(self):
        self.provision(); self.start(); self.seed(); self.login('reader')
        before = self.ledger(); verify_empty_ledger(before)
        self.marker_case('baseline', dict(empty_catalog_tables=len(TABLES), SQL_business_success_writes=0))
        self.catalogs(0); self.catalogs(1, scenario='fixture')
        verify_unchanged(before, self.ledger())
        self.marker_case('normal', dict(targets=[path for _,path in TARGETS], empty_collection_only=True,
            cross_instance=True, nonempty_capabilities_verified=False))
        for _, path in TARGETS:
            self.call(None, 'GET', '/api/v1'+path, want=401, code='unauthorized', scenario='authorization')
            self.call('reader', 'GET', '/api/v1'+path, environment='staging', index=1,
                      want=403, code='forbidden', scenario='authorization')
        verify_unchanged(before, self.ledger())
        self.marker_case('authorization', dict(anonymous_http_status=401, inaccessible_environment_http_status=403,
            object_permission_with_nonempty_rows_verified=False))
        for query in SERVICE_BOUNDARIES:
            self.call('reader', 'GET', '/api/v1/services?'+query, index=1, want=422,
                      code='invalid_filter', scenario='boundary')
        verify_unchanged(before, self.ledger())
        self.marker_case('boundary', dict(service_invalid_filters=len(SERVICE_BOUNDARIES), mcp_query_contract=False))
        old_cookie = self.cookies['reader']  # Memory only: replay the cookie revoked by formal logout.
        self.call('reader', 'POST', '/api/v1/auth/logout', want=204, scenario='fixture')
        for index in (0,1):
            for _, path in TARGETS:
                self.call('reader', 'GET', '/api/v1'+path, index=index, want=401,
                          code='unauthorized', scenario='authorization')
        self.marker_case('session_revoked', dict(formal_logout=True, cross_instance=True, revoked_cookie_http_status=401))
        self.login('reader')
        check(self.cookies['reader'] != old_cookie, 'formal re-login reused the revoked cookie')
        self.catalogs(0, scenario='fixture'); self.catalogs(1, scenario='fixture')
        self.marker_case('session_recovered', dict(formal_relogin=True, cross_instance=True, HTTP_status=200))
        verify_unchanged(before, self.ledger())
        self.marker_case('consistency', dict(business_rows_unchanged=True, audits_unchanged=True, no_producer_created=True))
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA, runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'], database=self.database, ports=list(PORTS), http_requests=self.calls,
            target_scope='empty MCP/services catalog normal/DTO/environment/session/filter contracts only',
            empty_collection_only=True, nonempty_capabilities_verified=False, full_business_chain_verified=False,
            unverified=['nonempty MCP registration/discovery/review/tool preference', 'nonempty service metrics/release/oncall',
                        'object ACL filtering of nonempty rows', 'natural session TTL expiry/concurrent revocation',
                        'dependency fault recovery', 'full chain', 'performance', '24h soak'])
        ro._private(self.work/'empty-catalogs-result.json', result)
        return result

    def cleanup(self):
        super().cleanup()  # Original 40-second total budget and held-Popen/tagged UUID cleanup.
        for port in PORTS:
            with socket.socket() as sock:
                sock.settimeout(.3)
                check(sock.connect_ex(('127.0.0.1',port)) != 0, 'owned empty-catalog API listener remains')
        emit('empty_catalogs.cleanup', 'passed', dict(ports_free=list(PORTS), owned_uuid_only=True))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resource acquisition')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/empty-catalogs.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.getuid(),
              'private own-UID regular lease required')
        try: fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError: raise Blocked('owned empty-catalog suite already holds its lease') from None
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('empty_catalogs.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error:
        emit('empty_catalogs.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    finally: os.close(fd)


if __name__ == '__main__': raise SystemExit(main())
