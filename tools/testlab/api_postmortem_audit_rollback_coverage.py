"""Two independent postmortem audit rollback/recovery HTTP cases.

Minimal formal HTTP prerequisites: service, authenticated inbound alert, manual
incident CAS resolution and source-revision draft. No investigation or Runbook.
Native acquisition is restricted to root-reviewed --live execution.
"""
from __future__ import annotations
import argparse, fcntl, hashlib, importlib.util, json, os, re, socket, stat, time, uuid
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

ROOT=Path(__file__).resolve().parents[2]
_spec=importlib.util.spec_from_file_location('postmortem_audit_private_incident',
                                           ROOT/'tools/testlab/api_incident_fault_interfaces_coverage.py')
incident=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(incident)
ro=incident.ro
SOURCE_SHA,BINARY_SHA,RUNTIME_SHA,PG_CID=incident.SOURCE_SHA,incident.BINARY_SHA,incident.RUNTIME_SHA,incident.PG_CID
PORTS=(18744,18745)
MODULE='formal-api-postmortem-audit-rollback'
TARGETS=(('POST','/postmortems'),('POST','/postmortems/{id}/publish'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'),
         ('POST','/incidents'),('POST','/incidents/{id}/status'),('GET','/postmortems/draft'),
         ('GET','/postmortems/records'),('GET','/postmortems'))
REQUIRED_CASES=tuple('postmortem_audit.'+s for s in (
    'http_inputs','resolved_source','create_rollback','create_fault_removed','create_recovery',
    'review_readback','independent_review','publish_rollback','publish_fault_removed',
    'publish_recovery','published_readback','cleanup'))
ro.PORTS,ro.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS)
Blocked,check,q=ro.Blocked,ro.check,ro.quote

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,
                         evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
ro.emit=emit

def hashes():
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest() for name in
            ('api_postmortem_audit_rollback_coverage.py','api_incident_fault_interfaces_coverage.py',
             'api_readonly_chain.py','runner.py','store.py','faults.py')}

def doctor(root):
    check(Path(root).resolve()==ROOT,'reviewed checkout required')
    data=ro.doctor(root)
    incident.frozen_identity(data,ro.inspect_pg(data['postgres_container'])['Id'])
    return dict(data,helper_sha256=hashes())

class Contract(ro.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path)
        check(not(parsed.scheme or parsed.netloc or parsed.fragment),'relative formal path required')
        canonical,kind=super().canonical(method,path)
        check((method,canonical[len('/api/v1'):]) in (*TARGETS,*SUPPORT),'outside PM audit interface inventory')
        params=parse_qs(parsed.query,keep_blank_values=True)
        if method=='GET' and canonical=='/api/v1/postmortems/draft':
            check(set(params)=={'incident'} and len(params['incident'])==1
                  and bool(re.fullmatch('INC-[a-f0-9]{32}',params['incident'][0])),
                  'exact manual incident draft query required')
        elif method=='GET' and canonical in ('/api/v1/postmortems/records','/api/v1/postmortems'):
            check(params in ({'tab':['review']},{'tab':['published']}),'bounded PM status query required')
        else:check(not parsed.query,'query outside owned PM read inventory')
        return canonical,kind

def fault_name(nonce):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)),'canonical invocation nonce required')
    return 'postmortem_audit_fail_'+nonce

def fault_statements(nonce,actor,app,case,revision,action,target=None):
    name=fault_name(nonce)
    check(bool(re.fullmatch('[a-f0-9]{32}',actor)) and bool(re.fullmatch('[a-f0-9]{32}',app))
          and bool(re.fullmatch('INC-[a-f0-9]{32}',case))
          and bool(re.fullmatch('case:[1-9][0-9]*',revision))
          and action in ('postmortem.created','postmortem.published'), 'exact PM fault inputs required')
    check(target is None if action=='postmortem.created' else
          bool(re.fullmatch('[a-f0-9]{32}',target or '')),'exact published target required')
    condition="NEW.action="+q(action)+' AND NEW.actor_id='+q(actor)+' AND NEW.application_id='+q(app)+\
              " AND NEW.changes->>'environment_id'='test' AND NEW.changes->>'incident_id'="+q(case)+\
              " AND NEW.changes->>'source_revision'="+q(revision)
    if target is not None:condition+=' AND NEW.target='+q(target)
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF '+
            condition+" THEN RAISE EXCEPTION 'owned postmortem audit fault'; END IF; RETURN NEW; END $$; "+
            'CREATE TRIGGER '+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    return name,create,drop_statement(nonce)

def drop_statement(nonce):
    name=fault_name(nonce)
    return 'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'

def unchanged_except(before,after,*allowed):
    check(before.keys()==after.keys() and all(before[k]==after[k] for k in before if k not in allowed),
          'postmortem write changed immutable source/prerequisite ledger')

def audit_delta(before,after,actor,app,pmid,case,revision,action):
    previous=before['audit'];new=[row for row in after['audit'] if row not in previous]
    check(len(new)==1 and len(after['audit'])==len(previous)+1
          and [row for row in after['audit'] if row in previous]==previous,'exact one immutable audit delta required')
    a=new[0]
    changes=dict(incident_id=case,environment_id='test',source_kind='case',source_revision=revision)
    if action=='postmortem.created':changes['status']='review'
    check(a['action']==action and a['actor_id']==actor and a['actor_kind']=='user' and a['application_id']==app
          and a['target']==pmid and a['outcome']=='succeeded' and a['changes']==changes,'PM audit actor/source/target/changes differ')

def verify_create(before,after,result,body,actor,app,event):
    check(before['postmortems']==[] and isinstance(result,dict) and set(result)=={'id'}
          and bool(re.fullmatch('[a-f0-9]{32}',result['id'])),'exact new PM identity required')
    unchanged_except(before,after,'postmortems','audit')
    check(len(after['postmortems'])==1,'create recovery left duplicate PM records')
    row=after['postmortems'][0]
    check(row['id']==result['id'] and row['incident_id']==body['incident_id'] and row['created_by']==actor
          and row['application_id']==app and row['environment_id']=='test' and row['status']=='review'
          and row['sections']==body['sections'] and row['actions']==body['actions']
          and row['source_kind']=='case' and row['source_case_status_event_id']==event
          and row['close_event_seq'] is None and row['investigation_id'] is None
          and bool(row['created_at']) and row['updated_at']==row['created_at'],
          'create recovery PM body/status/source/timestamps differ')
    audit_delta(before,after,actor,app,result['id'],body['incident_id'],body['source_revision'],'postmortem.created')

def verify_publish(before,after,result,actor,app,case,revision):
    check(len(before['postmortems'])==len(after['postmortems'])==1,'publish PM population changed')
    old,row=before['postmortems'][0],after['postmortems'][0]
    check(result==dict(id=old['id'],status='published') and old['status']=='review'
          and row['status']=='published' and actor!=old['created_by'],'independent publication identity/status differs')
    check({k:v for k,v in row.items() if k not in ('status','updated_at')}==
          {k:v for k,v in old.items() if k not in ('status','updated_at')}
          and isinstance(row['updated_at'],str) and row['updated_at']>=old['updated_at'],
          'publish changed source/sections/actions/author/creation or regressed update time')
    unchanged_except(before,after,'postmortems','audit')
    audit_delta(before,after,actor,app,old['id'],case,revision,'postmortem.published')

def verify_record(value,pmid,case,revision,status,title):
    check(isinstance(value.get('items'),list) and len(value['items'])==1,'exact single PM readback required')
    row=value['items'][0]
    check(row.get('id')==pmid and row.get('incident_id')==case and row.get('source_revision')==revision
          and row.get('status')==status and row.get('title')==title and row.get('severity')=='P2'
          and row.get('action_count')==1 and row.get('duration') is None
          and row.get('unknown_fields')==['duration'] and bool(row.get('resolved')) and bool(row.get('created_at')),
          'PM readback status/source/body/unknown fields differ')

class Chain(incident.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id)
        self.contract=Contract(root);self.source_id='postmortem-audit-'+self.nonce
        self.state.update(suite=MODULE,targets=TARGETS,ports=list(PORTS),
                          actual_results_source='formal manual resolved case plus governed postmortem writes and audits')
        ro._private(self.work/'state.json',self.state)

    def frozen(self):
        ro.Chain.frozen(self);incident.frozen_identity(self.data,PG_CID)
        check(hashes()==self.data['helper_sha256'],'reviewed PM helper/Store inputs changed')
        if self.config_digest is not None:
            check(hashlib.sha256((self.work/'operations.json').read_bytes()+
                                (self.work/'source-token').read_bytes()).hexdigest()==self.config_digest,
                  'owned PM ingress configuration changed')

    def marker_case(self,suffix,actual):
        check('postmortem_audit.'+suffix in REQUIRED_CASES,'unknown PM marker')
        self.frozen();emit('postmortem_audit.'+suffix,'passed',dict(run_id=self.run_id,**actual))

    def start(self):
        original=ro.emit
        def phase_emit(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='postmortem_audit.api_ready.'+str(self.phase)+'.'+name.rsplit('.',1)[1]
            original(name,status,actual,**fields)
        ro.emit=phase_emit
        try:ro.Chain.start(self)
        finally:ro.emit=original

    def seed(self):
        super().seed()
        for actor in ('reader','peer'):
            for action in ('view','manage'):
                self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+
                         ','.join(q(v) for v in (self.roles[actor],'knowledge',action,'environment','test'))+');')
        people=json.loads(self.sql("SELECT json_build_object('count',count(*),'all_human',bool_and(v3_identity_kind='human')) FROM users WHERE id IN ("+
                                   q(self.users['reader'])+','+q(self.users['peer'])+')',read=True))
        check(self.users['reader']!=self.users['peer'] and people==dict(count=2,all_human=True),'two independent human identities required')

    def resolved_source(self):
        self.inputs()
        created,_=self.call('reader','POST','/api/v1/incidents',incident.payload(self.app,self.alert,self.marker),want=201)
        check(set(created)=={'id'} and bool(re.fullmatch('INC-[a-f0-9]{32}',created['id'])),'manual case must not create investigation')
        self.case=created['id']
        closed,_=self.call('reader','POST','/api/v1/incidents/'+self.case+'/status',
            dict(status='resolved',expected_revision=1,request_id='postmortem-'+self.nonce+'-close',
                 reason='UUID local postmortem source prerequisite'),want=201,index=1)
        self.event=closed['event']['id'];self.revision='case:'+str(self.event)
        check(type(self.event) is int and self.event>0 and closed['current_status']=='resolved'
              and closed['current_revision']==2 and closed['current_resolved_status_event_id']==self.event,
              'actual manual CAS source event/revision differs')
        draft,_=self.call('reader','GET','/api/v1/postmortems/draft?incident='+self.case,index=1)
        check(draft.get('source_revision')==self.revision and draft.get('draft_mode')=='evidence_only'
              and draft.get('partial') is True and draft.get('incident',{}).get('id')==self.case
              and draft['incident'].get('duration') is None and draft['incident'].get('window') is None
              and draft.get('timeline') and draft['timeline'][-1].get('text')=='incident.resolved',
              'draft lacks real manual-close lineage/unknown duration')
        self.body=dict(incident_id=self.case,source_revision=self.revision,status='review',
            sections=[dict(key='root',title='Root cause',text='UUID synthetic root '+self.nonce)],
            actions=[dict(text='UUID follow up '+self.nonce,owner=self.users['reader'],due='2099-01-01')])
        self.marker_case('resolved_source',dict(case=self.case,resolved_event=self.event,source_revision=self.revision,
                                               actual_http_CAS=True,investigation_created=False))

    def ledger(self):
        self.frozen();self.owner()
        tables=dict(postmortems='v3_postmortems',cases='v3_incident_cases',links='v3_incident_case_alerts',
                    status_events='v3_incident_case_status_events',timeline='v3_incident_timeline',
                    alerts='ops_incidents',audit='management_audit')
        entries=[]
        for key,table in tables.items():
            entries += [q(key),"(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::json) FROM "+table+' t)']
        entries += ["'investigations'",'(SELECT count(*) FROM investigations)',"'events'",'(SELECT count(*) FROM events)']
        return json.loads(self.sql('SELECT json_build_object('+','.join(entries)+')',read=True))

    def install_fault(self,action,actor):
        self.frozen();self.owner();self.fault_attempted=True
        self.sql(fault_statements(self.nonce,self.users[actor],self.app,self.case,self.revision,action,
                                 self.postmortem if action=='postmortem.published' else None)[1])

    def remove_fault(self):
        if not self.fault_attempted:return
        deadline=time.monotonic()+4;self.owner(deadline=deadline)
        name=fault_name(self.nonce)
        self.pg(drop_statement(self.nonce),self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        remaining=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))
        check(remaining==dict(trigger=False,function=False),'own PM audit fault remains')
        self.fault_attempted=False

    def readback(self,status,before,index=1):
        self.call('reader','GET','/api/v1/postmortems/records?tab='+status,index=index,
            validate=lambda v:verify_record(v,self.postmortem,self.case,self.revision,status,self.marker))
        summary,_=self.call('peer','GET','/api/v1/postmortems?tab='+status,index=index)
        check(isinstance(summary.get('items'),list) and len(summary['items'])==1,'exact PM dashboard readback required')
        row=summary['items'][0]
        check(row.get('id')==self.postmortem and row.get('incident_id')==self.case
              and row.get('workflow_status')==status and row.get('root_cause')==self.body['sections'][0]['text']
              and row.get('actions')=='unknown / 1' and row.get('duration') is None,
              'PM second-API body/status/unknown action completion differs')
        check(self.ledger()==before,'PM readback changed business/audit ledger')
        self.marker_case(('review' if status=='review' else 'published')+'_readback',
                         dict(status=status,source_revision=self.revision,root_text_exact=True,read_api=index,zero_writes=True))

    def writes(self):
        before=self.ledger()
        check(before['postmortems']==[] and before['investigations']==before['events']==0
              and len(before['cases'])==len(before['links'])==len(before['status_events'])==1,
              'minimal resolved case fixture population differs')
        source,event=before['cases'][0],before['status_events'][0]
        check(source['id']==self.case and source['status']=='resolved' and source['status_revision']==2
              and source['resolved_status_event_id']==self.event and source['investigation_id'] is None
              and bool(source['resolved_at']) and event['id']==self.event and event['case_id']==self.case
              and event['revision']==2 and event['from_status']=='active' and event['to_status']=='resolved'
              and event['actor_id']==self.users['reader'],'persisted HTTP case/CAS close lineage differs')
        try:
            self.install_fault('postmortem.created','reader')
            self.call('reader','POST','/api/v1/postmortems',self.body,want=503,code='postmortems_unavailable',scenario='failure')
            check(self.ledger()==before,'created audit failure committed PM/body/source/audit')
            self.marker_case('create_rollback',dict(http503=True,whole_ledger_unchanged=True,postmortems=0))
        finally:self.remove_fault()
        self.marker_case('create_fault_removed',dict(trigger=False,function=False))
        made,_=self.call('reader','POST','/api/v1/postmortems',self.body,want=201,index=1)
        review=self.ledger();verify_create(before,review,made,self.body,self.users['reader'],self.app,self.event)
        self.postmortem=made['id']
        self.marker_case('create_recovery',dict(postmortem=self.postmortem,http201=True,exact_created_audit=1,status='review'))
        self.readback('review',review,index=0)
        self.call('reader','POST','/api/v1/postmortems/'+self.postmortem+'/publish',{},want=403,
                  code='self_review_forbidden',scenario='authorization')
        check(self.ledger()==review,'self-review rejection changed review/body/audit')
        self.marker_case('independent_review',dict(self_review=403,reviewer_distinct=self.users['peer']!=self.users['reader'],
                                                 two_synthetic_humans=True,physical_reviewer_verified=False))
        try:
            self.install_fault('postmortem.published','peer')
            self.call('peer','POST','/api/v1/postmortems/'+self.postmortem+'/publish',{},want=503,
                      code='postmortems_unavailable',scenario='failure',index=1)
            check(self.ledger()==review,'published audit failure changed review/source/sections/actions/updated_at/audit')
            self.marker_case('publish_rollback',dict(http503=True,whole_ledger_unchanged=True,
                status='review',updated_at_unchanged=True,source_sections_actions_unchanged=True))
        finally:self.remove_fault()
        self.marker_case('publish_fault_removed',dict(trigger=False,function=False))
        published,_=self.call('peer','POST','/api/v1/postmortems/'+self.postmortem+'/publish',{},index=0)
        after=self.ledger();verify_publish(review,after,published,self.users['peer'],self.app,self.case,self.revision)
        self.marker_case('publish_recovery',dict(http200=True,exact_published_audit=1,independent_actor=self.users['peer']))
        self.readback('published',after,index=1)

    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader','peer'):self.login(actor)
        self.resolved_source();self.writes();self.frozen()
        result=dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'],targets=TARGETS,required_markers=REQUIRED_CASES,
            http_requests=self.calls,full_business_chain_verified=False,
            unverified=['ops investigation close producer','concurrent write/inflight revoke/natural expiry',
                        'team ACL combinations','physical second reviewer','postmortem editing','Runbook/full chain','performance/24h'])
        ro._private(self.work/'postmortem-audit-result.json',result)
        return result

    def cleanup(self):
        error=None
        try:self.remove_fault()
        except BaseException as caught:error=caught
        finally:ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'owned PM API listener remains')
        if error is not None:raise error
        emit('postmortem_audit.cleanup','passed',dict(ports_free=list(PORTS),owned_uuid_only=True))

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--live',action='store_true')
    args=parser.parse_args()
    if not args.live:parser.error('root-reviewed --live required before resource acquisition')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    fd=os.open(ROOT/'.local/testlab/postmortem-audit.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular PM lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),str(uuid.uuid4())))
        return 0
    except Blocked as error:
        emit('postmortem_audit.environment','blocked',dict(error=str(error)));return 2
    except Exception as error:
        emit('postmortem_audit.failure','failed',dict(error_type=type(error).__name__,error=str(error)));return 1
    finally:os.close(fd)

if __name__=='__main__':raise SystemExit(main())
