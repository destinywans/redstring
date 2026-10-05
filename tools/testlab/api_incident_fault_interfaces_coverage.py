"""Independent incident POST authorization, format and audit rollback cases.

Only an HTTP-owned service and authenticated inbound alert are prerequisites.
No investigation, status transition, postmortem, Runbook or complete chain.
Resource acquisition is restricted to root-reviewed --live execution.
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
PORTS = (18740, 18741)
MODULE = 'formal-api-incident-fault-interfaces'
TARGETS = (('POST', '/incidents'),)
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'),
           ('POST', '/webhooks/alertmanager/{source}'))
REQUIRED_CASES = tuple('incident_fault.'+s for s in (
    'http_inputs', 'authorization', 'format_boundaries', 'scope_boundary',
    'external_actions_rejected', 'empty_case_storage', 'audit_rollback',
    'fault_removed', 'recovery', 'recovery_repeat', 'current_authorization', 'cleanup'))

_spec = importlib.util.spec_from_file_location('incident_fault_private_readonly',
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
            for name in ('api_incident_fault_interfaces_coverage.py', 'api_readonly_chain.py',
                         'runner.py', 'store.py', 'faults.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and data.get('runtime_sha') == RUNTIME_SHA and cid == PG_CID,
          'exact source/nonrace binary/runtime/PG identity differs')

def doctor(root):
    check(Path(root).resolve() == ROOT, 'reviewed checkout required')
    data = ro.doctor(root)
    frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        check(not (parsed.scheme or parsed.netloc or parsed.fragment or parsed.query),
              'relative exact incident interface path required')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in (*TARGETS, *SUPPORT):
            raise Blocked('outside independent incident POST inventory')
        return canonical, kind

def payload(app, alert, title):
    return dict(title=title, severity='P2', env='test', services=[app], alert_ids=[alert],
                war_room=False, notify=False, status_page=False, open_canvas=False)

def invalid_payloads(body):
    # 422 is the documented behavior, not a product failure.
    return [(dict(body, **{field:value}), 'invalid_incident') for field,value in (
        ('title',' '), ('title','故'*301), ('severity','critical'), ('env','all'),
        ('services',[]), ('services',body['services']*2), ('alert_ids',[]),
        ('alert_ids',body['alert_ids']*2), ('alert_ids',['alert-'+str(n) for n in range(21)]),
        ('alert_ids',[' '+body['alert_ids'][0]]), ('impact','影'*2001), ('commander','人'*201))
    ] + [(dict(body, invented_field=True), 'invalid_body')]

def fault_statements(nonce, actor, app):
    check(bool(re.fullmatch('[a-f0-9]{32}', nonce)) and bool(re.fullmatch('[a-f0-9]{32}', actor))
          and isinstance(app,str) and 0 < len(app) <= 128, 'exact invocation fault inputs required')
    name = 'incident_audit_fail_'+nonce
    create = ('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN '+
              "IF NEW.action='incident.create' AND NEW.actor_id="+q(actor)+
              ' AND NEW.application_id='+q(app)+" AND NEW.changes->>'environment_id'='test' THEN "+
              "RAISE EXCEPTION 'owned incident audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+
              name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    drop = ('BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; '+
            'DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;')
    return name, create, drop

def assert_empty_case_storage(ledger):
    check(all(ledger[key] == [] for key in ('cases','links','status_events','timeline'))
          and ledger['investigations'] == 0 and ledger['events'] == 0
          and not any(row['action'] == 'incident.create' for row in ledger['audit']),
          'fresh incident case storage is not empty')

def assert_recovery(before, after, result, body, actor, app, alert, request_key):
    assert_empty_case_storage(before)
    check(isinstance(result,dict) and set(result)=={'id'} and
          bool(re.fullmatch('INC-[a-f0-9]{32}',result['id'])), 'manual case result identity/fields differ')
    check(len(after['cases'])==1 and after['links']==[dict(case_id=result['id'],alert_id=alert)]
          and after['status_events']==[] and after['timeline']==[]
          and after['investigations']==0 and after['events']==0,
          'recovery left duplicate links/cases or unexpectedly created lifecycle/investigation rows')
    row = after['cases'][0]
    check(row['id']==result['id'] and row['application_id']==app and row['environment_key']=='test'
          and row['actor_id']==actor and row['request_key']==request_key
          and row['title']==body['title'] and row['severity']==body['severity']
          and row['impact']==body.get('impact') and row['commander_text']==body.get('commander')
          and row['title_provenance']=='human_confirmed' and row['severity_provenance']=='human_confirmed'
          and row['impact_provenance']=='human_confirmed' and row['commander_provenance']=='human_confirmed'
          and bool(re.fullmatch('[a-f0-9]{64}',row['request_hash']))
          and row['status']=='active' and row['status_revision']==1
          and row['investigation_id'] is None and row['resolved_status_event_id'] is None
          and row['resolved_at'] is None, 'recovered case body/actor/status differs')
    unchanged = ('alerts','status_events','timeline','investigations','events')
    check(all(before[k]==after[k] for k in unchanged), 'incident POST mutated its ingress alert or unrelated ledger')
    audits = [a for a in after['audit'] if a['action']=='incident.create']
    check(len(audits)==1 and len(after['audit'])==len(before['audit'])+1, 'recovery audit population differs')
    audit = audits[0]
    check(audit['actor_id']==actor and audit['actor_kind']=='user' and audit['application_id']==app
          and audit['target']==result['id'] and audit['outcome']=='succeeded'
          and audit['changes']==dict(environment_id='test',alert_ids=[alert],investigation_id=None),
          'recovered case audit identity/link/changes differ')
    check([a for a in after['audit'] if a['action']!='incident.create']==before['audit'],
          'recovery mutated prerequisite audit rows')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root,data,run_id)
        self.contract, self.config_digest = Contract(root), None
        self.source_id, self.phase = 'incident-fault-'+self.nonce, 0
        self.fault_attempted = False
        self.state.update(suite=MODULE,targets=TARGETS,ports=list(PORTS),sql_business_result_writes=0,
                          full_business_chain_verified=False,no_model_or_external_actions=True)
        ro._private(self.work/'state.json',self.state)

    def frozen(self):
        super().frozen(); frozen_identity(self.data,PG_CID)
        check(hashes()==self.data['helper_sha256'],'reviewed helper/Store inputs changed')
        if self.config_digest is not None:
            check(hashlib.sha256((self.work/'operations.json').read_bytes()+
                                (self.work/'source-token').read_bytes()).hexdigest()==self.config_digest,
                  'owned ingress configuration changed')

    def startup_environment(self):
        return {} if self.config_digest is None else {'AIOPS_OPERATIONS_CONFIG':str(self.work/'operations.json')}

    def seed(self):
        super().seed()
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                 ','.join(q(v) for v in (self.roles['reader'],'events','change','environment','test'))+');')

    def start(self):
        original = ro.emit
        def phase_emit(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):
                name='incident_fault.api_ready.'+str(self.phase)+'.'+name.rsplit('.',1)[1]
            original(name,status,actual,**fields)
        ro.emit=phase_emit
        try: super().start()
        finally: ro.emit=original

    def call(self,user,method,path,*args,request_key='default',raw_body=None,**kwargs):
        self.frozen()
        if request_key=='default': request_key='incident-'+self.nonce
        check(raw_body is None or isinstance(raw_body,bytes) and len(raw_body)<=16384,
              'bounded format fixture bytes required')
        original=ro.transport
        def transport(base,verb,target,body=None,headers=None,**options):
            wire=dict(headers or {})
            if verb=='POST' and target=='/api/v1/webhooks/alertmanager/'+self.source_id:
                check(user is None,'inbound source must not use actor session')
                wire['Authorization']='Bearer '+self.source_token
            if verb=='POST' and target=='/api/v1/incidents':
                # Explicit omission must also remove inherited/case-variant
                # values; an invalid-key fixture must send exactly its value.
                wire={k:v for k,v in wire.items() if k.lower()!='idempotency-key'}
                if request_key is not None: wire['Idempotency-Key']=request_key
                if raw_body is not None: body=raw_body
            else: check(raw_body is None,'format fixture substitution outside incident POST')
            return original(base,verb,target,body,wire,**options)
        ro.transport=transport
        try: return super().call(user,method,path,*args,**kwargs)
        finally: ro.transport=original

    def marker_case(self,suffix,actual):
        check('incident_fault.'+suffix in REQUIRED_CASES,'unknown incident fault marker')
        self.frozen(); emit('incident_fault.'+suffix,'passed',dict(run_id=self.run_id,**actual))

    def owner(self, deadline=None):
        query=("SELECT json_build_object('database',current_database(),'role',current_user,"+
            "'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),"+
            "'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))")
        actual=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                                 self.database,self.db_user,deadline=deadline))
        check(actual==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),
              'invocation DB/role ownership changed')

    def inputs(self):
        key='incident-fault-'+self.nonce
        self.call('admin','POST','/api/v1/services',{'items':[dict(id=key,tier='T1')]},want=201)
        row=json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(key)+" AND e.key='test'",read=True))
        check(row.get('app') and row.get('env'),'HTTP service identity missing')
        self.app=row['app']; self.source_token=ro.secrets.token_urlsafe(32)
        ro.private_file(self.work/'source-token',self.source_token+'\n')
        ro._private(self.work/'operations.json',dict(sources=[dict(id=self.source_id,
            token_file=str(self.work/'source-token'),mappings=[dict(labels={'incident_scope':self.nonce},
            application_id=self.app,environment_id=row['env'])])],feishu_channels=[]))
        self.config_digest=hashlib.sha256((self.work/'operations.json').read_bytes()+
                                         (self.work/'source-token').read_bytes()).hexdigest()
        for proc in self.processes:
            if proc.poll() is None: proc.terminate(); proc.wait(timeout=8)
        for index in (0,1):
            (self.work/('api-'+str(index)+'.log')).rename(self.work/('bootstrap-api-'+str(index)+'.log'))
        self.phase=1; self.start()
        self.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.source_id,
            {'alerts':[dict(status='firing',fingerprint=self.source_id,
                startsAt=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                labels={'incident_scope':self.nonce,'alertname':self.marker,'severity':'critical'},
                annotations={'summary':'UUID local incident interface input'})]},want=202,
            validate=lambda v:check(v==dict(accepted=1,notification_delivery='not_configured'),
                                   'native ingress/disabled notification differs'))
        self.alert=self.sql('SELECT id FROM ops_incidents WHERE source_id='+q(self.source_id),read=True)
        check(bool(self.alert) and '\n' not in self.alert,'exact one HTTP-owned ingress alert required')
        self.marker_case('http_inputs',dict(service_http=True,ingress_http=True,sql_business_seed=False,
                                      external_notifications=0,investigations=0))

    def ledger(self):
        self.frozen(); self.owner()
        tables={'cases':'v3_incident_cases','links':'v3_incident_case_alerts',
                'status_events':'v3_incident_case_status_events','timeline':'v3_incident_timeline',
                'alerts':'ops_incidents','audit':'management_audit'}
        entries=[]
        for key,table in tables.items():
            entries += [q(key),"(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+table+' t)']
        entries += ["'investigations'",'(SELECT count(*) FROM investigations)',"'events'",'(SELECT count(*) FROM events)']
        return json.loads(self.sql('SELECT json_build_object('+','.join(entries)+')',read=True))

    def install_fault(self):
        self.frozen(); self.owner(); self.fault_attempted=True
        self.sql(fault_statements(self.nonce,self.users['reader'],self.app)[1])

    def remove_fault(self):
        if not self.fault_attempted: return
        # Shares one recovery budget, leaving the inherited 40-second exact
        # process/DB/role cleanup inside the existing 45-second grace.
        # PG/CID/UUID guards remain active even after source drift.
        deadline=time.monotonic()+4
        self.owner(deadline=deadline)
        name,_,drop=fault_statements(self.nonce,self.users['reader'],self.app)
        self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        remaining=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',
                                    self.database,self.db_user,deadline=deadline))
        check(remaining==dict(trigger=False,function=False),'owned audit fault remains')
        self.fault_attempted=False

    def reject(self,before,body,want,code,**kwargs):
        self.call(kwargs.pop('user','reader'),'POST','/api/v1/incidents',body,want=want,code=code,**kwargs)
        check(self.ledger()==before,'rejected incident request changed business/audit ledger')

    def boundaries(self,before,body):
        for user,want,code,environment in (
            (None,401,'unauthorized','test'),('denied',404,'not_found','test'),
            ('peer',404,'not_found','test'),('hidden',404,'not_found','staging'),
            ('hidden',403,'forbidden','test'),('reader',403,'forbidden','staging')):
            self.reject(before,body,want,code,user=user,environment=environment,scenario='authorization')
        self.marker_case('authorization',dict(anonymous=401,view_only=404,environment_only=404,
                                         cross_environment=404,environment_denied=403))
        for invalid,code in invalid_payloads(body):
            self.reject(before,invalid,422,code,scenario='boundary')
        # Leading/trailing HTTP OWS is normalized by net/textproto. Keep the
        # invalid character inside the value so it reaches the handler.
        for key in (None,'short','invalid key-12345678','x'*129):
            self.reject(before,body,422,'idempotency_key_required',request_key=key,scenario='boundary')
        for raw in (b'{',json.dumps(body).encode()+b' {}'):
            self.reject(before,body,422,'invalid_body',raw_body=raw,scenario='boundary')
        self.marker_case('format_boundaries',dict(invalid_dto=13,invalid_keys=4,invalid_json=2,
                                             all_rejections_zero_writes=True))
        self.reject(before,body,409,'env_view_only',environment='all',scenario='boundary')
        self.reject(before,dict(body,env='staging'),409,'incident_scope_changed',scenario='boundary')
        self.reject(before,dict(body,services=['missing-'+self.nonce]),409,'incident_scope_changed',scenario='boundary')
        self.reject(before,dict(body,alert_ids=['missing-'+self.nonce]),404,'not_found',scenario='boundary')
        self.marker_case('scope_boundary',dict(all_write=409,confirmed_scope_mismatch=409,missing_alert=404))
        for flag in ('war_room','notify','status_page'):
            self.reject(before,dict(body,**{flag:True}),503,'incident_external_action_unavailable',scenario='failure')
        self.marker_case('external_actions_rejected',dict(flags=3,no_external_executor_invoked=True,zero_writes=True))

    def audit_recovery(self,before,body):
        assert_empty_case_storage(before)
        self.marker_case('empty_case_storage',dict(cases=0,links=0,status_events=0,investigations=0))
        try:
            self.install_fault()
            self.reject(before,body,503,'events_unavailable',scenario='failure')
            self.marker_case('audit_rollback',dict(action='incident.create',http_status=503,error_code='events_unavailable',
                cases_links_status_events_audit_unchanged=True,ingress_alert_unchanged=True))
        finally: self.remove_fault()
        self.marker_case('fault_removed',dict(trigger=False,function=False))
        result,_=self.call('reader','POST','/api/v1/incidents',body,want=201,index=1)
        after=self.ledger()
        assert_recovery(before,after,result,body,self.users['reader'],self.app,self.alert,'incident-'+self.nonce)
        self.marker_case('recovery',dict(id=result['id'],same_key_after_rollback=201,cases=1,links=1,
                                    audit=1,title_characters=len(body['title']),no_investigation=True))
        repeated,_=self.call('reader','POST','/api/v1/incidents',body,index=0,scenario='idempotency')
        check(repeated==result and self.ledger()==after,'recovery retry changed identity/business/audit')
        self.reject(after,dict(body,title='changed '+self.marker),409,'incident_conflict',scenario='idempotency')
        self.reject(after,body,409,'incident_conflict',request_key='other-'+self.nonce,scenario='idempotency')
        self.marker_case('recovery_repeat',dict(same_key_same_body=200,same_key_changed_body=409,
                                          other_key_linked_alert=409,zero_duplicate_rows=True))
        # Current grants must also be checked on the same-key retry path.
        for action in ('change','view'):
            self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+
                     " AND module='events' AND action="+q(action)+" AND scope_kind='environment' AND scope_id='test';")
            try: self.reject(after,body,404,'not_found',scenario='authorization',index=1)
            finally:
                self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                         ','.join(q(v) for v in (self.roles['reader'],'events',action,'environment','test'))+');')
        restored,_=self.call('reader','POST','/api/v1/incidents',body,scenario='authorization',index=1)
        check(restored==result and self.ledger()==after,'restored current grants failed exact same-key retry')
        self.marker_case('current_authorization',dict(revoked_change_retry=404,revoked_view_retry=404,
                                                restored_retry=200,exact_grants_restored=True,zero_writes=True))

    def run(self):
        self.provision(); self.start(); self.seed()
        for name in ('admin','reader','peer','hidden','denied'): self.login(name)
        self.inputs(); before=self.ledger(); assert_empty_case_storage(before)
        body=payload(self.app,self.alert,self.marker)
        self.boundaries(before,body)
        body.update(title='故'*300,impact='影'*2000,commander='人'*200)
        self.audit_recovery(before,body); self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'],targets=TARGETS,required_markers=REQUIRED_CASES,
            http_requests=self.calls,full_business_chain_verified=False,
            unverified=['20 distinct HTTP-produced alert maximum and concurrent create',
                        'inflight revoke/natural expiry','team ACL combinations','optional linked investigation',
                        'incident status lifecycle','postmortem/Runbook chain','performance/24h'])
        ro._private(self.work/'incident-fault-result.json',result)
        return result

    def cleanup(self):
        error=None
        try: self.remove_fault()
        except BaseException as caught: error=caught
        finally: super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1',port))!=0,'owned incident API listener remains')
        if error is not None: raise error
        emit('incident_fault.cleanup','passed',dict(ports_free=list(PORTS),owned_uuid_only=True))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT); parser.add_argument('--live',action='store_true')
    args=parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required before resource acquisition')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    fd=os.open(ROOT/'.local/testlab/incident-fault.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,
              'private regular incident lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT,doctor(ROOT),str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('incident_fault.environment','blocked',dict(error=str(error))); return 2
    except Exception as error:
        emit('incident_fault.failure','failed',dict(error_type=type(error).__name__,error=str(error))); return 1
    finally: os.close(fd)

if __name__=='__main__': raise SystemExit(main())
