"""Three independent content interfaces in an invocation-owned database.

Public HTTP creates every business input. No Runbook, upload, resolution,
postmortem, inference, notification, or cross-module acceptance is performed.
Ports deliberately share the completed rules-read reservation: run serially.
"""
from __future__ import annotations
import argparse, datetime, fcntl, hashlib, importlib.util, json, os, re, socket, stat, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
PORTS = (18628, 18629)
MODULE = 'formal-api-content-interfaces'
TARGETS = (('GET', '/canvas-templates'), ('POST', '/canvas-templates'),
           ('POST', '/incidents/{id}/timeline'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('POST', '/canvases'),
           ('GET', '/canvases/{id}'), ('POST', '/webhooks/alertmanager/{source}'),
           ('POST', '/incidents'), ('GET', '/incidents/{id}/timeline/manual'))
REQUIRED_CASES = ('content_interfaces.http_inputs', 'content_interfaces.private_templates',
    'content_interfaces.template_authorization', 'content_interfaces.template_boundaries',
    'content_interfaces.template_duplicate', 'content_interfaces.template_rollback',
    'content_interfaces.timeline', 'content_interfaces.timeline_authorization',
    'content_interfaces.timeline_boundaries', 'content_interfaces.timeline_rollback',
    'content_interfaces.revocation', 'content_interfaces.audit_consistency')

# A private module retains its own globals, avoiding changes in other helpers.
_spec = importlib.util.spec_from_file_location('content_interfaces_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(ro)
ro.PORTS = PORTS
ro.BASES = tuple('http://127.0.0.1:'+str(p) for p in PORTS)
ro.GETS = ()
Blocked, check, q = ro.Blocked, ro.check, ro.quote

def emit(name, status, actual, **fields):
    fields.pop('module', None)
    fields.pop('evidence_mode', None)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode='real_local', **fields), ensure_ascii=False), flush=True)
ro.emit = emit

def hashes():
    return {name: hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()
            for name in ('api_content_interfaces_coverage.py', 'api_readonly_chain.py')}

def frozen_identity(data, cid):
    check(data.get('source_tree_sha') == SOURCE_SHA and data.get('binary_sha256') == BINARY_SHA
          and cid == PG_CID, 'exact current source/nonrace binary/owned PG identity differs')

def doctor(root):
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
            raise Blocked('outside independent content interface inventory')
        return canonical, kind

def template_payload(canvas, name, keep_nodes=True):
    return dict(from_canvas=canvas, name=name, keep=dict(nodes=keep_nodes))

def incident_payload(app, alert, title):
    return dict(title=title, severity='P2', env='test', services=[app], alert_ids=[alert],
                war_room=False, notify=False, status_page=False, open_canvas=False)

def timeline_assert(value, text):
    check(value.get('text') == text and value.get('actor', {}).get('kind') == 'person'
          and isinstance(value.get('time'), str), 'native timeline text/actor/time mismatch')

class Chain(ro.Chain):
    def __init__(self, root, data, run_id):
        super().__init__(root, data, run_id)
        self.contract = Contract(root)
        self.config_digest = None
        self.source_id = 'content-'+self.nonce
        self.fault = False
        self.phase = 0
        self.state.update(suite=MODULE, targets=TARGETS, ports=list(PORTS),
            sql_business_result_writes=0, no_model_or_external_actions=True)
        ro._private(self.work/'state.json', self.state)

    def frozen(self):
        super().frozen()
        check(self.data['source_tree_sha'] == SOURCE_SHA and self.data['binary_sha256'] == BINARY_SHA,
              'frozen content identity changed')
        check(hashes() == self.data['helper_sha256'], 'helper inputs changed')
        if self.config_digest is not None:
            check(hashlib.sha256((self.work/'operations.json').read_bytes() +
                                (self.work/'source-token').read_bytes()).hexdigest() == self.config_digest,
                  'UUID source configuration changed')

    def startup_environment(self):
        return {} if self.config_digest is None else {'AIOPS_OPERATIONS_CONFIG': str(self.work/'operations.json')}

    def seed(self):
        super().seed()
        # Only role authorization inputs are added. No source/result rows.
        for module in ('canvas', 'events'):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                     ','.join(q(v) for v in (self.roles['reader'], module, 'change', 'environment', 'test'))+');')

    def start(self):
        original = ro.emit
        def phase_emit(name, status, actual, **fields):
            if name.startswith('readonly.api_ready.'):
                name = 'content_interfaces.api_ready.'+str(self.phase)+'.'+name.rsplit('.', 1)[1]
            original(name, status, actual, **fields)
        ro.emit = phase_emit
        try: super().start()
        finally: ro.emit = original

    def call(self, user, method, path, *args, **kwargs):
        original = ro.transport
        def transport(base, verb, target, payload=None, headers=None, **options):
            headers = dict(headers or {})
            if verb == 'POST' and target == '/api/v1/webhooks/alertmanager/'+self.source_id:
                check(user is None, 'source webhook must not use an actor cookie')
                headers['Authorization'] = 'Bearer '+self.source_token
            if verb == 'POST' and target == '/api/v1/incidents':
                headers['Idempotency-Key'] = 'content-'+self.nonce+'-incident'
            return original(base, verb, target, payload, headers, **options)
        ro.transport = transport
        try: return super().call(user, method, path, *args, **kwargs)
        finally: ro.transport = original

    def marker_case(self, name, actual):
        check(name in REQUIRED_CASES, 'unknown content marker')
        self.frozen()
        emit(name, 'passed', dict(run_id=self.run_id, **actual))

    def inputs(self):
        service_key = 'content-'+self.nonce
        self.call('admin', 'POST', '/api/v1/services', {'items': [{'id': service_key, 'tier': 'T1'}]}, want=201)
        row = json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(service_key)+" AND e.key='test'", read=True))
        check(row.get('app') and row.get('env'), 'HTTP service identity missing')
        self.app = row['app']
        self.source_token = ro.secrets.token_urlsafe(32)
        ro.private_file(self.work/'source-token', self.source_token+'\n')
        ro._private(self.work/'operations.json', dict(sources=[dict(id=self.source_id,
            token_file=str(self.work/'source-token'), mappings=[dict(labels={'content_scope': self.nonce},
            application_id=self.app, environment_id=row['env'])])], feishu_channels=[]))
        self.config_digest = hashlib.sha256((self.work/'operations.json').read_bytes()+
                                            (self.work/'source-token').read_bytes()).hexdigest()
        # Stop only held Popen children; never signal saved/global PIDs.
        for proc in self.processes:
            if proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=8)
        for index in (0, 1):
            (self.work/('api-'+str(index)+'.log')).rename(self.work/('bootstrap-api-'+str(index)+'.log'))
        self.phase = 1
        self.start()
        now = datetime.datetime.now(datetime.timezone.utc).isoformat()
        self.call(None, 'POST', '/api/v1/webhooks/alertmanager/'+self.source_id,
            {'alerts': [dict(status='firing', fingerprint=self.source_id, startsAt=now,
                labels={'content_scope': self.nonce, 'alertname': self.marker, 'severity': 'critical'},
                annotations={'summary': 'UUID synthetic local content input'})]}, want=202,
            validate=lambda v: check(v == {'accepted': 1, 'notification_delivery': 'not_configured'},
                                     'source ingest/disabled notification differs'))
        alert = self.sql('SELECT id FROM ops_incidents WHERE source_id='+q(self.source_id), read=True)
        check(bool(alert) and '\n' not in alert, 'exact one HTTP-produced alert required')
        value, _ = self.call('reader', 'POST', '/api/v1/incidents',
                            incident_payload(self.app, alert, self.marker), want=201)
        self.incident = value['id']
        source, _ = self.call('reader', 'POST', '/api/v1/canvases',
            dict(kind='plan', title=self.marker, services=[self.app], ai_draft=False), want=201)
        self.canvas = source['id']
        detail, _ = self.call('reader', 'GET', '/api/v1/canvases/'+self.canvas, index=1)
        check(detail.get('nodes') and detail['nodes'][0].get('id') == 'root',
              'formal generic source must contain its native root node')
        self.marker_case('content_interfaces.http_inputs', {'source_canvas': self.canvas,
            'incident': self.incident, 'synthetic_notifications': 0, 'sql_business_seed': False})

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('templates',(SELECT COALESCE(json_agg(id ORDER BY id),'[]'::json) FROM v3_canvas_templates WHERE owner_id="+q(self.users['reader'])+"),'notes',(SELECT COALESCE(json_agg(json_build_object('id',id,'text',text,'actor_id',actor_id) ORDER BY created_at,id),'[]'::json) FROM v3_incident_timeline WHERE incident_id="+q(self.incident)+"),'audits',(SELECT COALESCE(json_agg(json_build_object('id',id,'action',action,'target',target,'changes',changes) ORDER BY id),'[]'::json) FROM management_audit WHERE action IN ('v3.canvas_template.saved','incident.timeline.append')))", read=True))

    def audit_fault(self, enabled):
        if enabled:
            self.sql("CREATE FUNCTION content_interfaces_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $fault$ BEGIN IF NEW.actor_id="+q(self.users['reader'])+" AND NEW.action IN ('v3.canvas_template.saved','incident.timeline.append') THEN RAISE EXCEPTION 'UUID content audit fault'; END IF; RETURN NEW; END $fault$; CREATE TRIGGER content_interfaces_audit_fault BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION content_interfaces_audit_fault();")
        else:
            self.sql('DROP TRIGGER content_interfaces_audit_fault ON management_audit; DROP FUNCTION content_interfaces_audit_fault();')
        self.fault = enabled

    def template_peer_authorization(self, payload, expected_ledger):
        # First prove the entry gate with the original view-only peer. The
        # owner lookup is reachable only after this UUID-local peer receives
        # canvas change permission in the same environment as the source.
        self.call('peer', 'POST', '/api/v1/canvas-templates', payload, want=403,
                  code='forbidden', scenario='authorization')
        check(self.ledger() == expected_ledger, 'view-only peer rejection wrote content')
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                 ','.join(q(v) for v in (self.roles['peer'], 'canvas', 'change', 'environment', 'test'))+');')
        self.call('peer', 'POST', '/api/v1/canvas-templates', payload, want=404,
                  code='not_found', scenario='authorization', index=1)
        check(self.ledger() == expected_ledger, 'permitted nonowner rejection wrote content')
        return dict(view_only_peer_status=403, same_environment_change_nonowner_status=404,
                    both_rejections_zero_business_and_audit_writes=True)

    def templates(self):
        initial, _ = self.call('reader', 'GET', '/api/v1/canvas-templates')
        before = self.ledger()
        payload = template_payload(self.canvas, self.marker+' private')
        value, _ = self.call('reader', 'POST', '/api/v1/canvas-templates', payload, want=201)
        self.template = value['id']
        listed, _ = self.call('reader', 'GET', '/api/v1/canvas-templates', index=1)
        check(self.template in [r['id'] for r in listed['items']] and
              len(listed['items']) == len(initial['items'])+1, 'cross-instance private list mismatch')
        peer, _ = self.call('peer', 'GET', '/api/v1/canvas-templates')
        check(self.template not in [r['id'] for r in peer['items']], 'another owner sees private template')
        self.marker_case('content_interfaces.private_templates', {'native_nodes': 1, 'private_id': self.template})
        after = self.ledger()
        check(len(after['templates']) == len(before['templates'])+1 and
              len(after['audits']) == len(before['audits'])+1, 'template or audit atomicity mismatch')
        peer_boundaries = self.template_peer_authorization(payload, after)
        for user, want in ((None, 401), ('denied', 403)):
            for method in ('GET', 'POST'):
                self.call(user, method, '/api/v1/canvas-templates', None if method=='GET' else payload,
                          want=want, code='unauthorized' if want==401 else 'forbidden', scenario='authorization')
        self.call('reader', 'GET', '/api/v1/canvas-templates', environment='staging', want=403, code='forbidden', scenario='authorization')
        self.marker_case('content_interfaces.template_authorization',
                         dict(cross_owner_hidden=True, **peer_boundaries))
        for bad in (template_payload(self.canvas, ''), dict(from_canvas=self.canvas, name='missing-keep'),
                    template_payload(self.canvas, 'x'*201)):
            self.call('reader', 'POST', '/api/v1/canvas-templates', bad, want=422, code='invalid_template', scenario='boundary')
        self.marker_case('content_interfaces.template_boundaries', {'invalid_name_and_keep': True})
        self.call('reader', 'POST', '/api/v1/canvas-templates', payload, index=1, want=409,
                  code='duplicate_template', scenario='idempotency')
        check(self.ledger() == after, 'template reject/retry mutated ledger')
        self.marker_case('content_interfaces.template_duplicate', {'duplicate_did_not_write': True})
        self.audit_fault(True)
        try:
            self.call('reader', 'POST', '/api/v1/canvas-templates', template_payload(self.canvas, self.marker+' failed'),
                      want=503, code='audit_unavailable', scenario='failure')
            check(self.ledger() == after, 'template audit fault committed business state')
        finally: self.audit_fault(False)
        self.marker_case('content_interfaces.template_rollback', {'business_and_audit_unchanged': True})

    def timeline(self):
        path = '/api/v1/incidents/'+self.incident+'/timeline'
        before = self.ledger()
        note = self.marker+' operator note'
        self.call('reader', 'POST', path, {'text': note}, want=201,
                  validate=lambda v: timeline_assert(v, note))
        manual, _ = self.call('reader', 'GET', path+'/manual', index=1)
        check(manual.get('scope') == 'manual_only' and len(manual['items']) == 1,
              'cross-instance manual timeline count differs')
        timeline_assert(manual['items'][0], note)
        after = self.ledger()
        check(len(after['notes']) == len(before['notes'])+1 and
              len(after['audits']) == len(before['audits'])+1 and
              after['notes'][0]['actor_id'] == self.users['reader'], 'timeline native audit/actor mismatch')
        self.marker_case('content_interfaces.timeline', {'persisted_entries': 1, 'natural_append_idempotency': False})
        for user, want in ((None, 401), ('denied', 403)):
            self.call(user, 'POST', path, {'text': note}, want=want,
                      code='unauthorized' if want==401 else 'forbidden', scenario='authorization')
        self.call('reader', 'POST', path, {'text': note}, environment='staging', want=403, code='forbidden', scenario='authorization')
        self.marker_case('content_interfaces.timeline_authorization', {'scope_and_permission_enforced': True})
        for text in ('', ' '*3, 'x'*2001):
            self.call('reader', 'POST', path, {'text': text}, want=422, code='invalid_text', scenario='boundary')
        self.call('reader', 'POST', '/api/v1/incidents/INC-'+self.nonce+'/timeline', {'text': note},
                  want=404, code='not_found', scenario='boundary')
        check(self.ledger() == after, 'timeline rejection wrote state')
        self.marker_case('content_interfaces.timeline_boundaries', {'invalid_text_and_missing_id': True})
        self.audit_fault(True)
        try:
            self.call('reader', 'POST', path, {'text': 'must roll back'}, want=503, code='events_unavailable', scenario='failure')
            check(self.ledger() == after, 'timeline audit failure committed note')
        finally: self.audit_fault(False)
        self.marker_case('content_interfaces.timeline_rollback', {'business_and_audit_unchanged': True})
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module IN ('canvas','events') AND action='change';")
        self.call('reader', 'POST', '/api/v1/canvas-templates', template_payload(self.canvas, 'revoked'),
                  want=403, code='forbidden', scenario='authorization', index=1)
        self.call('reader', 'POST', path, {'text': 'revoked'}, want=403, code='forbidden', scenario='authorization', index=1)
        check(self.ledger() == after, 'revoked writes changed ledger')
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['reader'])+" AND module='canvas' AND action='view';")
        self.call('reader', 'GET', '/api/v1/canvas-templates', want=403, code='forbidden', scenario='authorization', index=1)
        self.marker_case('content_interfaces.revocation', {'current_permission_rechecked': True})
        check(len(after['templates']) == 1 and len(after['notes']) == 1 and
              [a['action'] for a in after['audits']].count('v3.canvas_template.saved') == 1 and
              [a['action'] for a in after['audits']].count('incident.timeline.append') == 1,
              'final exact content audit population differs')
        self.marker_case('content_interfaces.audit_consistency', {'template_saves': 1, 'timeline_appends': 1, 'audits': 2})

    def run(self):
        self.provision(); self.start(); self.seed()
        for name in ('admin', 'reader', 'peer', 'denied'): self.login(name)
        self.inputs(); self.templates(); self.timeline(); self.frozen()
        result = dict(run_id=self.run_id, source_tree_sha=SOURCE_SHA, binary_sha256=BINARY_SHA,
            targets=TARGETS, required_cases=REQUIRED_CASES, http_calls=self.calls,
            private_source='formal HTTP native generic root node', full_business_chain_verified=False,
            unverified=['official/team template authority', 'multi-node/content/Runbook retention',
                        'team ACL combination', 'inflight revoke/session expiry/crash recovery',
                        'list storage failure/capacity', 'timeline append has no idempotency contract'])
        ro._private(self.work/'content-interfaces-result.json', result)
        return result

    def cleanup(self):
        # Dropping only this invocation's tagged DB also removes any fault
        # trigger left after a cancelled HTTP call; never touch the shared DB.
        super().cleanup()
        for port in PORTS:
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1', port)) != 0, 'content API port remains occupied')
        emit('content_interfaces.cleanup', 'passed', {'ports_free': list(PORTS), 'owned_uuid_only': True})

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--live', action='store_true')
    args = parser.parse_args()
    if not args.live: parser.error('root-reviewed --live required')
    folder = args.root/'.local/testlab'
    fd = os.open(folder/'content-interfaces.lock', os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW, 0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600, 'private regular lease required')
        fcntl.flock(fd, fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():
            data=doctor(args.root)
            emit('content_interfaces.doctor', 'passed', {'source_tree_sha': SOURCE_SHA, 'ports': PORTS})
            ro.execute(Chain(args.root, data, str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('content_interfaces.environment', 'blocked', {'error': str(error)}); return 2
    except Exception as error:
        emit('content_interfaces.failure', 'failed', {'error_type': type(error).__name__, 'error': str(error)}); return 1
    finally: os.close(fd)

if __name__ == '__main__': raise SystemExit(main())
