"""23 public V3 entry routes: anonymous401 on both isolated API instances.
No login, identity/ACL seed, business row seed, inference or source completeness claim.
Root admits one matching Runner; the released UUID/Popen cleanup remains sole40s.
"""
from __future__ import annotations
import argparse, hashlib, importlib.util, inspect, json, re, socket, textwrap
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
MODULE = 'formal-catalog-anonymous-boundaries'
PORTS = (18926, 18927)
spec = importlib.util.spec_from_file_location('catalog_anonymous_private_lifecycle', ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
h = importlib.util.module_from_spec(spec)
spec.loader.exec_module(h)
ro, check, Blocked = h.base, h.check, h.Blocked
SOURCE, BINARY, RUNTIME, PG_CID = h.SOURCE, h.BINARY, h.RUNTIME, h.PG_CID
BASE_HELPER_SHA = '6501cb3c5e9603d8071731b963eb92e19c1a65d286326ecff8639e3bfd829d11'
ORIGINAL_GETS = tuple(('GET', path) for path in ('/agent-runs', '/agent-tools', '/agents/lifecycle', '/commands', '/coverage', '/guardrails', '/prompts'))
CONFIRMED_ADDITIONS = (('GET','/audit'), ('GET','/mentions'), ('GET','/connector-types/{type}'), ('POST','/connectors'), ('GET','/investigation-templates'), ('POST','/canvases/{id}/nodes/{node}/messages'), ('POST','/investigations/{id}/branches'), ('PATCH','/investigations/{id}/nodes/{node}'), ('POST','/investigations/{id}/nodes/{node}/messages'), ('POST','/investigations/{id}/writeback'), ('GET','/investigations/{id}/writeback-preview'), ('POST','/service-mappings/accept'), ('POST','/service-mappings/discover'), ('GET','/config-sync'), ('POST','/config-sync/{id}/import'), ('POST','/promotions'))
TARGETS = ORIGINAL_GETS + CONFIRMED_ADDITIONS
EXPECTED_HTTP = 2 * len(TARGETS)
REQUIRED_CASES = ('catalog_anonymous.inputs', *tuple('catalog_anonymous.target.'+str(i) for i in range(len(TARGETS))), 'catalog_anonymous.ledger', 'catalog_anonymous.cleanup', 'readonly.fresh_database', 'readonly.api_ready.0', 'readonly.api_ready.1', 'readonly.cleanup', 'readonly.complete')
ro.PORTS = PORTS
ro.BASES = tuple('http://127.0.0.1:'+str(p) for p in PORTS)
h._namespace['BASES'] = ro.BASES

def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def hashes():
    return {name: digest(ROOT/'tools/testlab'/name) for name in ('api_catalog_anonymous_boundary.py', 'tests/test_api_catalog_anonymous_boundary.py', 'api_conversation_object_session_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py', 'backend.py', 'env.py', 'inventory.py', 'components.py')}

h.hashes = hashes

def evidence(row):
    return dict(row, registration_evidence=False, evidence_mode='real_local', upstream_mode='real_local', source_tree_sha=SOURCE, binary_sha256=BINARY, binary_platform='darwin/arm64 Mach-O', test_name='FormalCatalogAnonymousBoundary', proof_scope='23 anonymous entry401 on two own APIs plus all-public no-side-effect digest; no normal or whole-route acceptance')

def emit(name, status, actual, **fields):
    fields.pop('module', None)
    fields.pop('evidence_mode', None)
    if 'interface' in fields:
        fields['interface'] = evidence(fields['interface'])
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE, evidence_mode='real_local', **fields)), flush=True)

_append = ro.append_json

def append_json(path, row):
    return _append(path, evidence(row) if Path(path).name == 'interfaces.jsonl' else row)

ro.emit, ro.append_json = emit, append_json
h._namespace['emit'], h._namespace['append_json'] = emit, append_json

class Contract(h.Contract.__bases__[0]):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not any((parsed.scheme, parsed.netloc, parsed.query, parsed.fragment)), 'exact anonymous catalog path required')
        canonical, kind = super().canonical(method, path)
        check((method, canonical.removeprefix('/api/v1')) in TARGETS and kind == 'main', 'only reviewed 23 entry routes permitted; instance is public and excluded')
        return canonical, kind

ro.Contract = Contract

_source = inspect.getsource(h.admission)
for old, new in (('root-admitted-conversation-object-session', 'root-admitted-catalog-anonymous-boundary'), ('conversation_boundaries_authorized', 'catalog_anonymous_boundaries_authorized')):
    check(_source.count(old) == 1, 'released admission seam changed')
    _source = _source.replace(old, new)
_scope = dict(h.__dict__, MODULE=MODULE, PORTS=PORTS, hashes=hashes)
exec(compile(textwrap.dedent(_source), str(__file__)+'[private admission]', 'exec'), _scope)
admission = _scope['admission']

def doctor(root):
    check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py') == BASE_HELPER_SHA, 'released lifecycle changed')
    data = h.doctor(root)
    info = ro.inspect_pg(data['postgres_container'])
    check(info['Id'] == PG_CID and info['HostConfig'].get('NanoCpus') == 4000000000 and info['HostConfig'].get('Memory') == 2147483648, 'exact PG4CPU2GiB required')
    return data

def verify_error(value, headers, status, code):
    content_type = next((v for k, v in headers.items() if k.lower() == 'content-type'), '')
    check(status == 401 and code == 'unauthorized', 'only anonymous401 unauthorized permitted')
    check(content_type.split(';', 1)[0].strip().lower() == 'application/json', 'anonymous refusal must be JSON')
    check(value == {'error': {'code': 'unauthorized', 'message': 'Login required'}}, 'anonymous refusal DTO reflected input or differs')

h._namespace['verify_error'] = verify_error

class Chain(ro.Chain):
    def __init__(self, root, data, run_id, permit):
        h.frozen_identity(data)
        super().__init__(root, data, run_id)
        self.permit = Path(permit)
        self.users, self.roles, self.cookies = {}, {}, {}
        self.state.update(suite=MODULE, ports=list(PORTS), normal_added=0, synthetic_input_scope='no test identity/ACL or business SQL seed; startup bootstrap only', actual_results_source='anonymous public GET refusals', model_scope='inert configuration only; no model call')
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        h.frozen_identity(self.data)
        admission(self.permit, self.run_id)
        check(digest(ROOT/'tools/testlab/api_conversation_object_session_coverage.py') == BASE_HELPER_SHA and digest(ROOT/'.local/testlab/runtime-config.json') == RUNTIME, 'released helper/runtime changed')
        super().frozen()

    def marker_case(self, name, actual):
        self.frozen()
        emit('catalog_anonymous.'+name, 'passed', dict(run_id=self.run_id, **actual))

    def ledger(self):
        self.frozen()
        h.Chain.owner(self)
        tables = json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'", read=True))
        check(tables and len(tables) <= 512 and all(re.fullmatch('[a-z][a-z0-9_]*', t) for t in tables) and {'sessions', 'management_audit'}.issubset(tables), 'bounded all-public tables including sessions/audit required')
        parts = ['SELECT '+ro.quote(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+ro.identifier(t)+' t' for t in tables]
        value = json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') hashes', read=True))
        check(set(value) == set(tables) and all(re.fullmatch('[a-f0-9]{64}', v) for v in value.values()), 'full row digests required, not table counts')
        return value

    def call(self, user, method, path, body=None, **kwargs):
        check(user is None and method in ('GET', 'POST', 'PATCH') and body == (None if method == 'GET' else {}) and kwargs.get('want') == 401 and kwargs.get('code') == 'unauthorized' and kwargs.get('scenario') == 'authorization' and not self.cookies, 'anonymous exact refusal only; no login/cookie')
        self.frozen()
        original, cloned = ro.transport, h._namespace['transport']
        def wire(origin, verb, target, payload=None, headers=None, timeout=12):
            check(payload == (None if verb == 'GET' else b'{}') and not any(k.lower() in ('cookie', 'authorization') for k in (headers or {})) and timeout == 12, 'anonymous header/body/timeout boundary differs')
            status, raw, response_headers = original(origin, verb, target, payload, headers, timeout)
            check(len(raw) <= 4096, 'bounded anonymous response required')
            # Only the exact safe error is stored. Other response bodies never enter evidence.
            value = json.loads(raw)
            verify_error(value, response_headers, status, 'unauthorized')
            ro._private(self.work/('anonymous-wire-'+str(self.calls)+'.json'), dict(method=verb, path=target, API_index=ro.BASES.index(origin), status=status, response_bytes=len(raw), response_sha256=hashlib.sha256(raw).hexdigest(), response_body=value, source_tree_sha=SOURCE, binary_sha256=BINARY, request_body_stored=False, credentials_stored=False))
            return status, raw, response_headers
        ro.transport, h._namespace['transport'] = wire, wire
        try:
            return h._namespace['call'](self, user, method, path, body, **kwargs)
        finally:
            ro.transport, h._namespace['transport'] = original, cloned

    def run(self):
        self.provision()
        self.start()
        check(not self.cookies and not self.users, 'no test login/seed identities required')
        before = self.ledger()
        ro._private(self.work/'ledger-before.json', before)
        self.marker_case('inputs', dict(targets=TARGETS, API_instances=2, no_login=True, SQL_business_writes=0, public_instance_excluded=True))
        for i, (method, path) in enumerate(TARGETS):
            target = path.format(id=self.nonce, node='root', type='gitlab')
            for index in (0, 1):
                self.call(None, method, '/api/v1'+target, None if method == 'GET' else {}, want=401, code='unauthorized', scenario='authorization', index=index)
            self.marker_case('target.'+str(i), dict(method=method, path=path, HTTP401=2, API_instances=2, registration_evidence=False))
        after = self.ledger()
        ro._private(self.work/'ledger-after.json', after)
        check(before == after, 'anonymous reads changed complete public row digests including audit/sessions')
        self.marker_case('ledger', dict(all_public_tables=len(before), complete_row_digests_unchanged=True, sessions_and_audit_included=True))
        check(self.calls == EXPECTED_HTTP, 'reviewed46HTTP scope differs')
        result = dict(run_id=self.run_id, http_calls=EXPECTED_HTTP, required_cases=REQUIRED_CASES, normal_added=0, registration_evidence=False, counts_as_whole_acceptance=False, source_tree_sha=SOURCE, binary_sha256=BINARY)
        ro._private(self.work/'anonymous-result.json', result)
        return result

    def cleanup(self):
        # Released cleanup is the sole40s clock; no restore or fault DDL.
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:
                sock.settimeout(.3)
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'own anonymous API port remains')
        emit('catalog_anonymous.cleanup', 'passed', dict(single_seconds=40, grace_seconds=45, owned_UUID_only=True))

def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--inventory', action='store_true')
    parser.add_argument('--live', action='store_true')
    parser.add_argument('--run-id')
    parser.add_argument('--admission', type=Path)
    args = parser.parse_args(argv)
    if args.inventory:
        Contract(ROOT)
        print(json.dumps(dict(status='prepared-only', module=MODULE, ports=PORTS, targets=TARGETS, required_cases=REQUIRED_CASES, estimated_HTTP_calls=EXPECTED_HTTP, normal_added=0, registration_evidence=False, API_seconds=12, outer_seconds=420, cleanup_seconds=40, grace_seconds=45)))
        return 0
    if not (args.live and args.run_id and args.admission):
        parser.error('root explicit --live/run-id/admission required')
    try:
        check(args.root.resolve() == ROOT, 'reviewed checkout required')
        admission(args.admission, args.run_id)
        with ro.exclusive(ROOT), ro.cooperative_signals():
            ro.execute(Chain(ROOT, doctor(ROOT), args.run_id, args.admission))
        return 0
    except Blocked:
        emit('catalog_anonymous.environment', 'blocked', dict(first_failure_preserved=True))
        return 2
    except Exception as error:
        emit('catalog_anonymous.failure', 'failed', dict(error_type=type(error).__name__, first_failure_preserved=True))
        return 1

if __name__ == '__main__':
    raise SystemExit(main())
