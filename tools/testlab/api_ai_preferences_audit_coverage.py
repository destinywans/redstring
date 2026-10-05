"""Owned AI preference PUT audit rollback, recovery and repeated state.

Only formal local catalog/GET/PUT/login HTTP; no inference/probe or external calls.
SQL writes only synthetic identities/grants and an own UUID action/actor fault.
"""
from __future__ import annotations
import argparse, datetime, fcntl, hashlib, importlib.util, json, os, re, socket, stat, time, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18746, 18747)
MODULE = 'formal-api-ai-preferences-audit'
ACTION = 'v3.ai_preferences.updated'
# ro.start omits OPENAI_API; runner.command_env does not inherit it. The formal
# OpenAI runtime default is chat_completions (internal/inference/runtime.go).
INHERITED_MODEL_API = 'chat_completions'
TARGETS = (('PUT', '/me/ai-preferences'),)
SUPPORT = (('POST', '/auth/login'), ('GET', '/models'), ('GET', '/me/ai-preferences'))
REQUIRED_CASES = tuple('ai_preferences_audit.'+n for n in
    ('catalog', 'http_baseline', 'rollback', 'fault_removed', 'recovery', 'repeat_state', 'consistency', 'cleanup'))
_spec = importlib.util.spec_from_file_location('ai_preferences_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
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
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in
            ('api_ai_preferences_audit_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID, 'frozen source/binary/runtime/PG changed')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed root required')
    data = ro.doctor(root)  # Retains nonce/HMAC/controller and exact PG image/label/loopback guards.
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not any((parsed.scheme, parsed.netloc, parsed.query, parsed.fragment)), 'exact relative preference path required')
        canonical, kind = super().canonical(method, path)
        check((method, canonical.removeprefix('/api/v1')) in (*TARGETS, *SUPPORT), 'outside AI preference inventory')
        return canonical, kind

def verify_preferences(value):
    keys = {'default','canvas_inherit','show_reasoning','auto_context','memory','budget_warn_tokens'}
    check(isinstance(value, dict) and set(value) == keys and isinstance(value['default'], dict)
          and set(value['default']) in ({'model'}, {'model','reasoning'})
          and value['default'].get('model') == 'openai:readonly-synthetic', 'preference DTO/model identity differs')
    check(all(type(value[k]) is bool for k in ('canvas_inherit','auto_context','memory'))
          and value['show_reasoning'] in ('collapsed','expanded','hidden')
          and type(value['budget_warn_tokens']) is int and 100 <= value['budget_warn_tokens'] <= 1_000_000,
          'preference DTO field type/range differs')
    check('reasoning' not in value['default'] or isinstance(value['default']['reasoning'], str), 'reasoning DTO type differs')

def payload_from_catalog(catalog, current):
    verify_preferences(current)
    check(isinstance(catalog, dict) and set(catalog) == {'items','default'} and len(catalog['items']) == 1,
          'exact inherited local synthetic catalog required')
    item = catalog['items'][0]
    check(item.get('id') == current['default']['model'] and item.get('api') == INHERITED_MODEL_API
          and item.get('available') is True and catalog['default'] == current['default'],
          'catalog/model preference binding or representable protocol differs')
    reasoning = item.get('reasoning', {}); levels = reasoning.get('levels')
    check(isinstance(levels, list) and reasoning.get('kind') in ('none','levels','toggle')
          and all(isinstance(x,dict) and isinstance(x.get('id'),str) and x['id'] for x in levels),
          'advertised reasoning options differ')
    if current['default'].get('reasoning'):
        check(current['default']['reasoning'] in [x['id'] for x in levels], 'current choice is not advertised')
    # Retain the advertised default; only update local UI/context preferences.
    result = dict(current, **{'default':dict(current['default'])}, show_reasoning='hidden', auto_context=False,
                  memory=False, budget_warn_tokens=12345)
    check(result != current, 'synthetic baseline must differ from target preferences')
    return result

def fault_statements(nonce, actor):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and bool(re.fullmatch('[a-f0-9]{32}', actor)), 'exact nonce/actor required')
    name = 'ai_preferences_fail_'+nonce
    create = ('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN '+
              'IF NEW.action='+q(ACTION)+' AND NEW.actor_id='+q(actor)+' AND NEW.target='+q(actor)+
              " THEN RAISE EXCEPTION 'owned AI preference audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+
              ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    # The current audit changes contains model/reasoning, not environment_id.
    drop = 'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
    return name, create, drop

def verify_rollback(before, after):
    check(before == after, 'audit failure changed settings/timestamps or audit rows')

def verify_update(before, after, actor, payload):
    """Exactly one successful operation audit, including repeated identical PUT.

    Compare complete settings rows; only ai_preferences/updated_at may change.
    Serial IDs may have gaps after rollback, so compare rows, not sequence values.
    """
    old = {x['user_id']:x for x in before['settings']}; new = {x['user_id']:x for x in after['settings']}
    check(len(old) == len(before['settings']) and len(new) == len(after['settings'])
          and set(old) == set(new) and actor in old, 'HTTP-established settings row set changed')
    check(all(new[k] == v for k,v in old.items() if k != actor), 'another user settings changed')
    strip = lambda row:{k:v for k,v in row.items() if k not in ('ai_preferences','updated_at')}
    check(strip(old[actor]) == strip(new[actor]) and new[actor]['ai_preferences'] == payload,
          'saved preference partial row/defaults/created_at changed')
    parse = lambda value:datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
    check(parse(new[actor]['updated_at']) > parse(old[actor]['updated_at']), 'successful PUT did not update its timestamp')
    check(len(after['audit']) == len(before['audit'])+1 and all(x in after['audit'] for x in before['audit']),
          'successful PUT changed prior audits or did not add exactly one')
    added = [x for x in after['audit'] if x not in before['audit']]
    check(len(added) == 1 and all(added[0].get(k) == v for k,v in dict(actor_id=actor, actor_kind='user',
        application_id='', action=ACTION, target=actor, outcome='success',
        changes=dict(model=payload['default']['model'], reasoning=payload['default'].get('reasoning',''))).items()),
        'successful PUT audit action/actor/outcome/choice differs')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.fault_attempted = Contract(root), False
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS), business_success_sql_writes=0,
            model_scope='inherited local synthetic catalog; no inference/probe/embedding', full_business_chain_verified=False)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper/Store inputs changed')

    def call(self, *args, **kwargs):
        self.frozen(); return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('ai_preferences_audit.'+suffix in REQUIRED_CASES, 'unknown AI preference marker')
        self.frozen(); emit('ai_preferences_audit.'+suffix, 'passed', dict(run_id=self.run_id, **actual))

    def owner(self, deadline=None):
        query = "SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        value = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(value == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag), 'UUID DB/role owner differs')

    def ledger(self):
        self.frozen(); self.owner()
        return json.loads(self.sql("SELECT json_build_object('settings',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY user_id),'[]'::json) FROM v3_user_settings t),'audit',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))", read=True))

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
        check(absent == dict(trigger=False, function=False), 'own AI preference fault remains')
        self.fault_attempted = False

    def read(self, actor, expected, index=1):
        self.call(actor, 'GET', '/api/v1/me/ai-preferences', index=index, scenario='fixture',
            validate=lambda value:check(value == expected, 'cross-instance/owner preference value differs'))

    def run(self):
        self.provision(); self.start(); self.seed()
        for actor in ('reader','peer'): self.login(actor)
        current, _ = self.call('reader', 'GET', '/api/v1/me/ai-preferences', scenario='fixture', validate=verify_preferences)
        catalog, _ = self.call('reader', 'GET', '/api/v1/models', scenario='fixture')
        payload = payload_from_catalog(catalog, current)
        self.marker_case('catalog', dict(model=current['default']['model'], API=catalog['items'][0]['api'], remote_probe=False, inference=False))
        # Both old rows/audits originate through HTTP, so rollback exercises an
        # existing update and preserves unrelated user/default columns.
        for actor in ('reader','peer'):
            self.call(actor, 'PUT', '/api/v1/me/ai-preferences', current, scenario='fixture',
                validate=lambda value:check(value == current, 'HTTP baseline differs'))
        before = self.ledger()
        owned_rows = {r['user_id']:r for r in before['settings']}
        check(all(uid in owned_rows and owned_rows[uid]['ai_preferences'] == current
                  for uid in (self.users['reader'],self.users['peer'])), 'HTTP baseline rows differ')
        self.marker_case('http_baseline', dict(owners=2, SQL_business_success_writes=0, persisted_old_value=True))
        try:
            self.install_fault()
            self.call('reader', 'PUT', '/api/v1/me/ai-preferences', payload, want=503, code='settings_unavailable', scenario='failure')
            self.read('reader', current); self.read('peer', current)
            verify_rollback(before, self.ledger())
            self.marker_case('rollback', dict(http_status=503, error_code='settings_unavailable', complete_rows_unchanged=True,
                                              timestamps_unchanged=True, audits_unchanged=True))
        finally: self.remove_fault()
        self.marker_case('fault_removed', dict(trigger=False, function=False))
        self.call('reader', 'PUT', '/api/v1/me/ai-preferences', payload,
            validate=lambda value:check(value == payload, 'recovered preference DTO differs'))
        recovered = self.ledger(); verify_update(before, recovered, self.users['reader'], payload)
        self.read('reader', payload); self.read('peer', current)
        verify_rollback(recovered, self.ledger())
        self.marker_case('recovery', dict(http_status=200, audit_delta=1, other_owner_unchanged=True, cross_instance=True))
        self.call('reader', 'PUT', '/api/v1/me/ai-preferences', payload, index=1, scenario='idempotency',
            validate=lambda value:check(value == payload, 'repeated preference DTO differs'))
        repeated = self.ledger(); verify_update(recovered, repeated, self.users['reader'], payload)
        self.marker_case('repeat_state', dict(state_idempotent=True, successful_repeat_audit_delta=1,
                                              request_deduplication_contract=False, updated_at_advanced=True))
        self.read('reader', payload, index=0); self.read('peer', current, index=0)
        verify_rollback(repeated, self.ledger())
        self.marker_case('consistency', dict(read_side_effects=0, other_owner_unchanged=True, successful_target_audits=2))
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA, runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'], database=self.database, ports=list(PORTS), http_requests=self.calls,
            target_scope='AI preference PUT audit rollback/recovery/repeated state only', full_business_chain_verified=False,
            unverified=['reasoning/model option combinations','insert-path audit fault','concurrent writers/revocation',
                        'real model connectivity/inference','full chain','performance','24h soak'])
        ro._private(self.work/'ai-preferences-audit-result.json', result)
        return result

    def cleanup(self):
        error = None
        try: self.remove_fault()
        except BaseException as caught: error = caught
        finally: super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned preference API listener remains')
        if error is not None: raise error
        emit('ai_preferences_audit.cleanup', 'passed', dict(ports_free=list(PORTS), owned_uuid_only=True))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resources')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/ai-preferences-audit.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('ai_preferences_audit.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error:
        emit('ai_preferences_audit.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
