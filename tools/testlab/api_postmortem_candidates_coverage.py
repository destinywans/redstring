"""Independent GET postmortem candidates with HTTP-owned manual case inputs.

No postmortem creation/publication, investigation, Runbook or complete business
chain. Ports share completed content reservation and must run serially.
"""
from __future__ import annotations
import argparse, datetime, fcntl, hashlib, importlib.util, json, os, socket, stat, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
_spec = importlib.util.spec_from_file_location('candidates_private_content', ROOT/'tools/testlab/api_content_interfaces_coverage.py')
content = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(content)
ro = content.ro
check, q, Blocked = ro.check, ro.quote, ro.Blocked
SOURCE_SHA, BINARY_SHA, PG_CID = content.SOURCE_SHA, content.BINARY_SHA, content.PG_CID
PORTS = (18628, 18629)
MODULE = 'formal-api-postmortem-candidates'
TARGETS = (('GET', '/postmortems/candidates'),)
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/webhooks/alertmanager/{source}'),
           ('POST', '/incidents'), ('POST', '/incidents/{id}/status'))
REQUIRED_CASES = ('candidates.http_inputs', 'candidates.open_excluded', 'candidates.closed_revision',
                  'candidates.authorization', 'candidates.scope_boundary', 'candidates.repeat_read',
                  'candidates.storage_failure', 'candidates.recovery', 'candidates.revocation',
                  'candidates.read_only_consistency')

def emit(name, status, actual, **fields):
    fields.pop('module', None); fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {name: hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_postmortem_candidates_coverage.py', 'api_content_interfaces_coverage.py',
                         'api_readonly_chain.py')}

def doctor(root):
    data = ro.doctor(root)
    content.frozen_identity(data, ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data, helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self, method, path):
        parsed = urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment:
            raise Blocked('relative formal HTTP path required')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in (*TARGETS, *SUPPORT):
            raise Blocked('outside independent candidate interface inventory')
        return canonical, kind

def assert_candidate(value, incident, title, event):
    check(isinstance(value.get('items'), list) and len(value['items']) == 1,
          'exact one nonempty native candidate required')
    row = value['items'][0]
    check(row.get('id') == incident and row.get('title') == title and row.get('severity') == 'P2'
          and row.get('source_revision') == 'case:'+str(event)
          and isinstance(row.get('resolved'), str) and bool(row['resolved'])
          and row.get('duration') is None and row.get('evidence') is None
          and set(row.get('unknown_fields', [])) == {'duration', 'evidence'},
          'native manual close source identity/revision/unknown fields differ')

class Chain(content.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract = Contract(root)
        self.state.update(suite=MODULE, targets=TARGETS)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        ro.Chain.frozen(self)
        check(self.data['source_tree_sha'] == SOURCE_SHA and self.data['binary_sha256'] == BINARY_SHA,
              'frozen candidate source/binary changed')
        check(hashes() == self.data['helper_sha256'], 'candidate helper inputs changed')
        if self.config_digest is not None:
            check(hashlib.sha256((self.work/'operations.json').read_bytes()+
                                (self.work/'source-token').read_bytes()).hexdigest() == self.config_digest,
                  'owned candidate source configuration changed')

    def seed(self):
        ro.Chain.seed(self)
        for module, action in (('events', 'change'), ('knowledge', 'view')):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                     ','.join(q(v) for v in (self.roles['reader'], module, action, 'environment', 'test'))+');')

    def start(self):
        original = ro.emit
        def phase_emit(name, status, actual, **fields):
            if name.startswith('readonly.api_ready.'):
                name = 'candidates.api_ready.'+str(self.phase)+'.'+name.rsplit('.',1)[1]
            original(name, status, actual, **fields)
        ro.emit = phase_emit
        try: ro.Chain.start(self)
        finally: ro.emit = original

    def marker_case(self, name, actual):
        check(name in REQUIRED_CASES, 'unknown candidate marker')
        self.frozen(); emit(name, 'passed', dict(run_id=self.run_id, **actual))

    def inputs(self):
        key = 'candidate-'+self.nonce
        self.call('admin', 'POST', '/api/v1/services', {'items':[{'id':key, 'tier':'T1'}]}, want=201)
        row=json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(key)+" AND e.key='test'", read=True))
        check(row.get('app') and row.get('env'), 'HTTP service identity missing')
        self.app=row['app']; self.source_token=ro.secrets.token_urlsafe(32)
        ro.private_file(self.work/'source-token', self.source_token+'\n')
        ro._private(self.work/'operations.json', dict(sources=[dict(id=self.source_id,
            token_file=str(self.work/'source-token'), mappings=[dict(labels={'candidate_scope':self.nonce},
            application_id=self.app, environment_id=row['env'])])], feishu_channels=[]))
        self.config_digest=hashlib.sha256((self.work/'operations.json').read_bytes()+
                                         (self.work/'source-token').read_bytes()).hexdigest()
        for proc in self.processes:
            if proc.poll() is None: proc.terminate(); proc.wait(timeout=8)
        for index in (0,1):
            (self.work/('api-'+str(index)+'.log')).rename(self.work/('bootstrap-api-'+str(index)+'.log'))
        self.phase=1; self.start()
        self.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.source_id,
            {'alerts':[dict(status='firing', fingerprint=self.source_id,
                startsAt=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                labels={'candidate_scope':self.nonce,'alertname':self.marker,'severity':'critical'},
                annotations={'summary':'UUID local candidate input'})]}, want=202,
            validate=lambda v: check(v=={'accepted':1,'notification_delivery':'not_configured'},
                                     'exact native ingest/disabled notification required'))
        alert=self.sql('SELECT id FROM ops_incidents WHERE source_id='+q(self.source_id),read=True)
        check(bool(alert) and '\n' not in alert,'exact one HTTP-produced alert required')
        case,_=self.call('reader','POST','/api/v1/incidents',
                        content.incident_payload(self.app,alert,self.marker),want=201)
        self.incident=case['id']
        self.marker_case('candidates.http_inputs',dict(sql_business_seed=False,external_notifications=0,
                                                     incident=self.incident))

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('cases',(SELECT COALESCE(json_agg(to_jsonb(c) ORDER BY id),'[]'::json) FROM v3_incident_cases c),'events',(SELECT COALESCE(json_agg(to_jsonb(s) ORDER BY id),'[]'::json) FROM v3_incident_case_status_events s),'postmortems',(SELECT count(*) FROM v3_postmortems),'audit',(SELECT COALESCE(json_agg(to_jsonb(a) ORDER BY id),'[]'::json) FROM management_audit a))",read=True))

    def storage_select(self, enabled):
        # Database/role ownership is checked by inherited sql()/pg() before this
        # grant fault. No business row or shared PG setting is mutated.
        self.sql(('GRANT' if enabled else 'REVOKE')+' SELECT ON v3_incident_case_status_events '+
                 ('TO ' if enabled else 'FROM ')+ro.identifier(self.db_user)+';')

    def candidates(self):
        path='/api/v1/postmortems/candidates'
        opened,_=self.call('reader','GET',path)
        check(opened=={'items':[]},'open manual case incorrectly qualifies')
        self.marker_case('candidates.open_excluded',dict(open_case_candidate_count=0))
        closed,_=self.call('reader','POST','/api/v1/incidents/'+self.incident+'/status',
            dict(status='resolved',expected_revision=1,request_id='candidate-'+self.nonce+'-close',
                 reason='UUID local candidate precondition'),want=201,index=1)
        self.event=closed['event']['id']
        check(closed['current_status']=='resolved' and closed['current_revision']==2
              and closed['current_resolved_status_event_id']==self.event,
              'native CAS close event/revision mismatch')
        before=self.ledger()
        value,_=self.call('reader','GET',path,index=1,
                         validate=lambda v:assert_candidate(v,self.incident,self.marker,self.event))
        self.marker_case('candidates.closed_revision',dict(source_revision='case:'+str(self.event),
                                                         natural_http_status_event=True))
        self.call(None,'GET',path,want=401,code='unauthorized',scenario='authorization')
        denied,_=self.call('denied','GET',path,scenario='authorization')
        check(denied=={'items':[]},'environment-only member sees protected candidate')
        hidden,_=self.call('hidden','GET',path,environment='staging',scenario='authorization')
        check(hidden=={'items':[]},'cross-environment candidate disclosed')
        self.call('hidden','GET',path,want=403,code='forbidden',scenario='authorization')
        self.marker_case('candidates.authorization',dict(anonymous=401,object_denied_empty=True,
                                                       cross_environment_hidden=True))
        self.call('reader','GET',path,environment='staging',want=403,code='forbidden',scenario='boundary')
        all_value,_=self.call('reader','GET',path,environment='all',scenario='boundary')
        check(all_value==value,'authorized all scope differs from sole permitted environment')
        self.marker_case('candidates.scope_boundary',dict(staging_forbidden=True,authorized_all_exact=True))
        repeated,_=self.call('reader','GET',path,scenario='idempotency')
        check(repeated==value,'GET duplicate changed source revision/payload')
        check(self.ledger()==before,'candidate reads changed lifecycle/audit ledger')
        self.marker_case('candidates.repeat_read',dict(two_instances_same_payload=True))
        self.storage_select(False)
        try:
            self.call('reader','GET',path,want=503,code='postmortems_unavailable',scenario='failure',index=1)
        finally: self.storage_select(True)
        self.marker_case('candidates.storage_failure',dict(owned_role_select_denied=True,restore_attempted=True))
        recovered,_=self.call('reader','GET',path,index=1,scenario='failure')
        check(recovered==value,'storage recovery lost authoritative candidate')
        self.marker_case('candidates.recovery',dict(exact_original_candidate=True))
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='knowledge' AND action='view';")
        revoked,_=self.call('reader','GET',path,index=1,scenario='authorization')
        check(revoked=={'items':[]},'live knowledge permission revoke was ignored')
        self.marker_case('candidates.revocation',dict(current_object_permissions_rechecked=True))
        check(self.ledger()==before,'GET/fault/recovery/revoke wrote business or audit state')
        check(before['postmortems']==0 and len(before['cases'])==1 and len(before['events'])==1,
              'fixture must remain a single case/status event without postmortem creation')
        self.marker_case('candidates.read_only_consistency',dict(postmortems=0,status_events=1,
                                                              reads_zero_business_audit_writes=True))

    def run(self):
        self.provision(); self.start(); self.seed()
        for name in ('admin','reader','peer','hidden','denied'): self.login(name)
        self.inputs(); self.candidates(); self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,
            targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=self.calls,
            full_business_chain_verified=False,
            unverified=['ops investigation close producer','1000 candidate cap and 256 scan pagination',
                        'inflight revoke/session expiry','team ACL combinations','postmortem creation/publication'])
        ro._private(self.work/'postmortem-candidates-result.json',result)
        return result

    def cleanup(self):
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1',port))!=0,'candidate API port remains occupied')
        emit('candidates.cleanup','passed',dict(ports_free=list(PORTS),owned_uuid_only=True))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--live',action='store_true')
    args=parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required')
    fd=os.open(args.root/'.local/testlab/postmortem-candidates.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():
            data=doctor(args.root);emit('candidates.doctor','passed',dict(source_tree_sha=SOURCE_SHA,ports=PORTS))
            ro.execute(Chain(args.root,data,str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('candidates.environment','blocked',dict(error=str(error)));return 2
    except Exception as error:
        emit('candidates.failure','failed',dict(error_type=type(error).__name__,error=str(error)));return 1
    finally: os.close(fd)

if __name__=='__main__':raise SystemExit(main())
