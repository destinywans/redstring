"""Owned ordinary preference PATCH / notification PUT audit transactions.

Only formal local PATCH/PUT/GET/login HTTP; no inference/probe or external calls.
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
PORTS = (18748, 18749)
MODULE = 'formal-api-user-settings-audit'
ACTIONS = {'preferences':'v3.preferences.updated', 'notifications':'v3.notification_settings.updated'}
PREF_KEYS = ('theme','locale','density','reduce_motion')
TARGETS = (('PATCH', '/me/preferences'), ('PUT', '/me/notification-settings'))
SUPPORT = (('POST', '/auth/login'), ('GET', '/me/notification-settings'))
REQUIRED_CASES = tuple('user_settings_audit.'+kind+'.'+stage for kind in ACTIONS
    for stage in ('rollback','fault_removed','recovery','repeat_state','consistency')) + (
    'user_settings_audit.http_baseline', 'user_settings_audit.authorization', 'user_settings_audit.cleanup')
_spec = importlib.util.spec_from_file_location('user_settings_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
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
            ('api_user_settings_audit_coverage.py', 'api_readonly_chain.py', 'runner.py', 'store.py', 'identity.py', 'faults.py')}

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
        check((method, canonical.removeprefix('/api/v1')) in (*TARGETS, *SUPPORT), 'outside ordinary preference/notification inventory')
        return canonical, kind

def verify_preferences(value):
    check(isinstance(value,dict) and set(value) == set(PREF_KEYS)
          and value['theme'] in ('system','dark','light') and value['locale'] in ('zh-CN','en-US')
          and value['density'] in ('comfortable','compact') and type(value['reduce_motion']) is bool,
          'preference DTO keys/type differ')

def verify_notifications(value):
    check(isinstance(value,dict) and set(value)=={'quiet','digest','subscriptions'}
          and value['digest'] in ('daily','weekly','off') and isinstance(value['subscriptions'],list),
          'notification DTO keys/type differ')
    quiet=value['quiet']
    check(isinstance(quiet,dict) and set(quiet)=={'enabled','from','to'} and type(quiet['enabled']) is bool
          and all(isinstance(quiet[k],str) and re.fullmatch(r'(?:[01][0-9]|2[0-3]):[0-5][0-9]',quiet[k]) for k in ('from','to')),
          'notification quiet DTO differs')
    check(value['subscriptions']==[], 'own fresh UUID must not have subscription producers')

def fault_statements(nonce, actor, kind):
    check(kind in ACTIONS and bool(re.fullmatch('[a-f0-9]{32}',nonce))
          and bool(re.fullmatch('[a-f0-9]{32}',actor)), 'exact kind/nonce/actor required')
    name='user_settings_fail_'+nonce
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN '+
        'IF NEW.action='+q(ACTIONS[kind])+' AND NEW.actor_id='+q(actor)+' AND NEW.target='+q(actor)+
        " THEN RAISE EXCEPTION 'owned user settings audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+
        ' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop='BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
    return name,create,drop

def verify_rollback(before,after):
    check(before==after, 'audit failure changed settings/timestamps or audit rows')

def verify_update(before,after,actor,kind,expected,patch):
    check(kind in ACTIONS,'unknown settings action')
    old={r['user_id']:r for r in before['settings']};new={r['user_id']:r for r in after['settings']}
    check(len(old)==len(before['settings']) and len(new)==len(after['settings']) and set(old)==set(new)
          and actor in old,'HTTP-established settings row set differs')
    check(all(new[k]==v for k,v in old.items() if k!=actor),'other owner changed')
    allowed=set(patch) if kind=='preferences' else {'notification_settings'}
    check(allowed <= set(PREF_KEYS) if kind=='preferences' else True,'unknown preference patch')
    allowed.add('updated_at')
    strip=lambda r:{k:v for k,v in r.items() if k not in allowed}
    check(strip(old[actor])==strip(new[actor]),'unsubmitted settings/AI/notification/profile/created_at changed')
    saved={k:new[actor][k] for k in PREF_KEYS} if kind=='preferences' else new[actor]['notification_settings']
    check(saved==expected,'saved DTO differs')
    parse=lambda value:datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
    check(parse(new[actor]['updated_at'])>parse(old[actor]['updated_at']),'successful mutation timestamp did not advance')
    check(len(after['audit'])==len(before['audit'])+1 and all(r in after['audit'] for r in before['audit']),
          'prior audit changed or new audit count differs')
    added=[r for r in after['audit'] if r not in before['audit']]
    changes={k:patch.get(k) for k in PREF_KEYS} if kind=='preferences' else dict(
        quiet=expected['quiet']['enabled'],digest=expected['digest'],subscriptions=len(expected['subscriptions']))
    check(len(added)==1 and all(added[0].get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',
        application_id='',action=ACTIONS[kind],target=actor,outcome='success',changes=changes).items()),
        'audit actor/action/outcome/exact changes differ')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract, self.fault_attempted, self.fault_kind = Contract(root), False, None
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS), business_success_sql_writes=0,
            settings_scope='ordinary preferences PATCH and notification PUT; no AI preference mutation', full_business_chain_verified=False)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data, PG_CID)
        check(hashes() == self.data['helper_sha256'], 'reviewed helper/Store inputs changed')

    def call(self, *args, **kwargs):
        self.frozen(); return super().call(*args, **kwargs)

    def marker_case(self, suffix, actual):
        check('user_settings_audit.'+suffix in REQUIRED_CASES, 'unknown user settings marker')
        self.frozen(); emit('user_settings_audit.'+suffix, 'passed', dict(run_id=self.run_id, **actual))

    def owner(self, deadline=None):
        query = "SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        value = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(value == dict(database=self.database, role=self.db_user, db_tag=self.owner_tag, role_tag=self.owner_tag), 'UUID DB/role owner differs')

    def ledger(self):
        self.frozen(); self.owner()
        return json.loads(self.sql("SELECT json_build_object('settings',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY user_id),'[]'::json) FROM v3_user_settings t),'audit',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))", read=True))

    def install_fault(self, kind):
        self.frozen(); self.owner(); check(kind in ACTIONS, 'unknown audit fault kind'); self.fault_kind = kind; self.fault_attempted = True
        self.sql(fault_statements(self.nonce, self.users['reader'], self.fault_kind)[1])

    def remove_fault(self):
        if not self.fault_attempted: return
        deadline = time.monotonic()+4
        self.owner(deadline=deadline)
        name, _, drop = fault_statements(self.nonce, self.users['reader'], self.fault_kind)
        self.pg(drop, self.database, self.db_user, deadline=deadline)
        query = "SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        absent = json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;', self.database, self.db_user, deadline=deadline))
        check(absent == dict(trigger=False, function=False), 'own user settings fault remains')
        self.fault_attempted = False

    def notifications_read(self, actor, expected, index=1):
        self.call(actor,'GET','/api/v1/me/notification-settings',index=index,scenario='fixture',
            validate=lambda v:check(v==expected,'cross-instance notification value differs'))

    def exercise(self, kind, method, path, old_value, patch, expected, peer_value):
        before=self.ledger()
        try:
            self.install_fault(kind)
            self.call('reader',method,path,patch,want=503,code='settings_unavailable',scenario='failure')
            if kind=='notifications':
                self.notifications_read('reader',old_value);self.notifications_read('peer',peer_value)
            verify_rollback(before,self.ledger())
            self.marker_case(kind+'.rollback',dict(http_status=503,error_code='settings_unavailable',
                complete_rows_unchanged=True,timestamps_unchanged=True,audits_unchanged=True))
        finally:self.remove_fault()
        self.marker_case(kind+'.fault_removed',dict(trigger=False,function=False))
        self.call('reader',method,path,patch,index=1,
            validate=lambda v:check(v==expected,'recovery DTO/partial-update semantics differ'))
        recovered=self.ledger();verify_update(before,recovered,self.users['reader'],kind,expected,patch)
        self.marker_case(kind+'.recovery',dict(http_status=200,audit_delta=1,other_owner_unchanged=True,
            API_index=1,partial_patch=True,unsubmitted_columns_unchanged=True))
        self.call('reader',method,path,patch,index=0,scenario='idempotency',
            validate=lambda v:check(v==expected,'repeat DTO differs'))
        repeated=self.ledger();verify_update(recovered,repeated,self.users['reader'],kind,expected,patch)
        self.marker_case(kind+'.repeat_state',dict(state_idempotent=True,successful_repeat_audit_delta=1,
            request_deduplication_contract=False,updated_at_advanced=True,API_index=0))
        if kind=='notifications':
            for index in (0,1):
                self.notifications_read('reader',expected,index);self.notifications_read('peer',peer_value,index)
            verify_rollback(repeated,self.ledger())
        self.marker_case(kind+'.consistency',dict(cross_instance_successful_mutation=True,
            notification_cross_instance_GET=kind=='notifications',
            preference_GET_unverified=kind=='preferences',read_side_effects=0 if kind=='notifications' else None,
            successful_target_audits=2,other_owner_unchanged=True))

    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('reader','peer'):self.login(actor)
        prefs=dict(theme='light',locale='en-US',density='compact',reduce_motion=True)
        prefs_path='/api/v1/me/preferences';notify_path='/api/v1/me/notification-settings'
        for actor in ('reader','peer'):
            self.call(actor,'PATCH',prefs_path,prefs,scenario='fixture',validate=lambda v:check(v==prefs,'baseline preference differs'))
        notifications={}
        for actor in ('reader','peer'):
            current,_=self.call(actor,'GET',notify_path,scenario='fixture',validate=verify_notifications)
            check(current==dict(quiet=dict(enabled=False,**{'from':'22:00','to':'08:00'}),digest='daily',subscriptions=[]),
                'own synthetic defaults differ')
            self.call(actor,'PUT',notify_path,current,scenario='fixture',validate=lambda v:check(v==current,'HTTP notification baseline differs'))
            notifications[actor]=current
        baseline=self.ledger();rows={r['user_id']:r for r in baseline['settings']}
        check(all({k:rows[self.users[a]][k] for k in PREF_KEYS}==prefs
            and rows[self.users[a]]['notification_settings']==notifications[a] for a in ('reader','peer')),'formal HTTP baseline not persisted')
        self.marker_case('http_baseline',dict(owners=2,SQL_business_success_writes=0,existing_rows=True))
        for method,path in TARGETS:
            payload={'theme':'dark'} if method=='PATCH' else {'quiet':True}
            self.call(None,method,'/api/v1'+path,payload,want=401,code='unauthorized',scenario='authorization')
        verify_rollback(baseline,self.ledger())
        self.marker_case('authorization',dict(anonymous_401=2,ledger_unchanged=True))
        self.exercise('preferences','PATCH',prefs_path,prefs,{'theme':'dark'},dict(prefs,theme='dark'),prefs)
        current=notifications['reader']
        target=dict(current,quiet=dict(current['quiet'],enabled=True))
        self.exercise('notifications','PUT',notify_path,current,{'quiet':True},target,notifications['peer'])
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'],database=self.database,ports=list(PORTS),http_requests=self.calls,
            target_scope='ordinary preference PATCH + notification PUT audit rollback/recovery/repeated partial mutation',
            full_business_chain_verified=False,
            unverified=['preferences GET /me requires authoritative on-call source','insert-path audit fault',
                'concurrent writers and dynamic subscriptions','full chain','performance','24h soak','external notification delivery'])
        ro._private(self.work/'user-settings-audit-result.json',result)
        return result

    def cleanup(self):
        error = None
        try: self.remove_fault()
        except BaseException as caught: error = caught
        finally: super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'owned user settings API listener remains')
        if error is not None: raise error
        emit('user_settings_audit.cleanup', 'passed', dict(ports_free=list(PORTS), owned_uuid_only=True))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT); parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resources')
    check(args.root.resolve() == ROOT, 'reviewed checkout required before lease')
    fd = os.open(ROOT/'.local/testlab/user-settings-audit.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode) == 0o600, 'private regular lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT, doctor(ROOT), str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('user_settings_audit.environment', 'blocked', dict(error=str(error))); return 2
    except Exception as error:
        emit('user_settings_audit.failure', 'failed', dict(error_type=type(error).__name__, error=str(error))); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
