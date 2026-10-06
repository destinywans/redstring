"""HTTP-produced 5,000/5,001 candidate boundary, not scalable-list acceptance.

Only root admission can allocate one UUID database and two API instances.
Inbox uses mine (no assigned items); Groups returns one resolved group. No
default-all large response, synthetic SQL alert rows, performance or soak.
"""
from __future__ import annotations
import argparse, datetime, hashlib, importlib.util, inspect, json, re, socket, textwrap, time
from pathlib import Path
from urllib.parse import urlsplit

ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-inbox-groups-capacity-boundary'
PORTS=(18900,18901)
def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
inc=load('capacity_private_incident',ROOT/'tools/testlab/api_incident_fault_interfaces_coverage.py')
ro=inc.ro
gate=load('capacity_private_admission',ROOT/'tools/testlab/api_conversation_object_session_coverage.py')
check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=inc.SOURCE_SHA,inc.BINARY_SHA,inc.RUNTIME_SHA,inc.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/inbox'),('GET','/alert-groups'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'))
QUERIES={'/api/v1/inbox':'filter=mine','/api/v1/alert-groups':'state=resolved'}
REQUIRED_CASES=tuple('capacity.'+n for n in ('source_setup','fixture_5000','inbox_5000','groups_5000',
    'fixture_5001','inbox_5001','groups_5001','ledger','cleanup'))+tuple(
    'capacity.api_ready.'+phase+'.'+str(i) for phase in ('setup','source') for i in (0,1))+(
    'readonly.fresh_database','readonly.synthetic_identities','readonly.cleanup','readonly.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in (
        'api_inbox_groups_capacity_boundary.py','tests/test_api_inbox_groups_capacity_boundary.py',
        'api_incident_fault_interfaces_coverage.py','api_conversation_object_session_coverage.py',
        'api_readonly_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
inc.hashes=hashes
def evidence(row):
    return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',
        source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',
        test_name='FormalInboxGroupsCapacityBoundary',proof_scope='candidate 5000/5001; mine empty; one resolved group; not full list or performance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
_append=ro.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit=emit;ro.append_json=append_json
class Contract(inc.Contract.__bases__[0]):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.fragment)),'relative reviewed path required')
        if method=='GET':check(QUERIES.get(parsed.path)==parsed.query,'only mine/resolved reviewed filters')
        else:check(not parsed.query,'setup query forbidden')
        canonical,kind=super().canonical(method,path)
        check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside capacity inventory')
        return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
source=inspect.getsource(gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-inbox-groups-capacity-boundary'),
                ('conversation_boundaries_authorized','inbox_groups_capacity_authorized')):
    check(source.count(old)==1,'private admission seam changed');source=source.replace(old,new)
namespace=dict(gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,
               RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(source),str(__file__)+'[private admission]','exec'),namespace)
admission=namespace['admission']
def doctor(root):
    data=ro.doctor(root);check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes())
    inc.frozen_identity(data,ro.inspect_pg(data['postgres_container'])['Id']);return data
def fixture_batch(source_id,nonce,title,anchor,start,count):
    check(type(start) is int and type(count) is int and 0<=start<5001 and 1<=count<=200 and start+count<=5001,
          'bounded exact occurrence batch required')
    check(anchor.tzinfo is not None and anchor.utcoffset()==datetime.timedelta(0),'UTC anchor required')
    alerts=[]
    for offset in range(start,start+count):
        at=anchor+datetime.timedelta(seconds=offset)
        alerts.append(dict(status='resolved',fingerprint=source_id,startsAt=at.isoformat(),
            endsAt=(at+datetime.timedelta(seconds=1)).isoformat(),
            labels=dict(incident_scope=nonce,alertname=title,severity='critical'),annotations=dict(summary='Owned HTTP capacity fixture')))
    value=dict(alerts=alerts);check(len(json.dumps(value).encode())<1<<20,'native ingress 1MiB bound exceeded');return value
def verify_inbox(value,headers,count,before,after):
    check(value.get('items')==[] and value.get('counts')==dict(all=count,mine=0,mentions=None,subscribed=None)
        and value.get('partial') is True and value.get('window')=='today_and_yesterday'
        and value.get('all_scope')=='persisted_alert_incident_updates' and value.get('mine_scope')=='assigned_alert_incidents'
        and value.get('unavailable')==['unread_state','mentions','subscriptions','non_alert_event_types']
        and isinstance(value.get('timezone'),str) and bool(value['timezone']),'exact mine counts/partial scope differs')
    at=datetime.datetime.fromisoformat(value.get('as_of','').replace('Z','+00:00'))
    check(before<=at<=after,'inbox as_of outside actual HTTP window')
    check(next((v for k,v in headers.items() if k.lower()=='cache-control'),'')=='no-store','inbox cache policy differs')
def verify_groups(value,app,env,source_id,anchor,count):
    check(value.get('counts')==dict(all=1,firing=0,pending=0,resolved=1,silenced=None)
        and value.get('aggregation')=='local_persisted_incidents' and value.get('partial') is True
        and value.get('unavailable')==['provider_grouping','silence_state','inhibition','external_notification_status']
        and len(value.get('items',[]))==1,'one exact resolved group required')
    g=value['items'][0]
    check(set(g)==set(('id','severity','labels','service','source_id','fingerprint','environment_id','environment_key',
        'count','count_kind','state','since','notify','badges','owner','inhibited','instances','local_grouping','partial','unavailable')),
        'complete group DTO fields differ')
    expected_id='group:'+hashlib.sha256(('\0'.join((env,app,source_id,source_id))).encode()).hexdigest()[:24]
    check(g['id']==expected_id and g['severity']=='P1' and g['service']==app and g['source_id']==source_id
        and g['fingerprint']==source_id and g['environment_id']==env and g['environment_key']=='test'
        and g['count']==count and g['count_kind']=='persisted_incidents' and g['state']=='resolved'
        and datetime.datetime.fromisoformat(g['since'].replace('Z','+00:00'))==anchor
        and g['instances']==[] and g['badges']==[] and g['owner'] is None and g['inhibited'] is None
        and g['partial'] is True and g['local_grouping']==['environment_id','application_id','source_id','fingerprint'],
        'resolved group identity/count/earliest/full DTO differs')
    check(g['labels']==[dict(k='application_id',v=app),dict(k='environment',v='test'),dict(k='source_id',v=source_id),dict(k='fingerprint',v=source_id)]
        and g['notify']==dict(channel=None,text='平台无通知记录；外部通知状态未知',status=None,scope='platform_queue')
        and g['unavailable']==['provider_grouping','provider_labels','current_value','metric_series','pending_progress',
            'silence_state','inhibition','external_notification_status','single_owner'],'honest unavailable/notification scope differs')
def verify_limit(value,headers):
    check(value==dict(error=dict(code='pagination_required',message='Incident results exceed the current V3 list contract')),
          'exact honest capacity error differs')
    check(next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0]=='application/json',
          'capacity refusal must be JSON')
class Chain(inc.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root)
        self.state.update(suite=MODULE,ports=list(PORTS),normal_added=0,SQL_business_writes=0)
        ro._private(self.work/'state.json',self.state)
    def frozen(self):admission(self.permit,self.run_id);super().frozen()
    def marker_case(self,name,actual):
        check('capacity.'+name in REQUIRED_CASES,'unknown capacity marker');self.frozen()
        emit('capacity.'+name,'passed',dict(run_id=self.run_id,**actual))
    def start(self):
        phase=('setup','source')[self.phase];original=ro.emit
        def ready(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='capacity.api_ready.'+phase+'.'+name[-1]
            return original(name,status,actual,**fields)
        ro.emit=ready
        try:ro.Chain.start(self)
        finally:ro.emit=original
    def call(self,*args,**kwargs):
        # Fixture setup never grants normal-registration evidence either.
        kwargs.setdefault('scenario','boundary');return super().call(*args,**kwargs)
    def inputs(self):
        # Deliberately does not invoke inc.Chain.inputs (which inserts one firing alert).
        key='capacity-'+self.nonce
        self.call('admin','POST','/api/v1/services',dict(items=[dict(id=key,tier='T1')]),want=201,scenario='fixture')
        row=json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(key)+" AND e.key='test'",read=True))
        check(row.get('app') and row.get('env'),'formal own service binding missing');self.app,self.env=row['app'],row['env']
        self.source_token=ro.secrets.token_urlsafe(32);ro.private_file(self.work/'source-token',self.source_token+'\n')
        ro._private(self.work/'operations.json',dict(sources=[dict(id=self.source_id,token_file=str(self.work/'source-token'),
            mappings=[dict(labels=dict(incident_scope=self.nonce),application_id=self.app,environment_id=self.env)])],feishu_channels=[]))
        self.config_digest=digest_bytes((self.work/'operations.json').read_bytes()+(self.work/'source-token').read_bytes())
        for proc in self.processes:
            if proc.poll() is None:proc.terminate();proc.wait(timeout=8)
        for i in (0,1):(self.work/('api-'+str(i)+'.log')).rename(self.work/('bootstrap-api-'+str(i)+'.log'))
        self.phase=1;self.start();self.marker_case('source_setup',dict(service_HTTP=True,no_notifications=True,SQL_business_writes=0))
    def ledger(self):
        self.frozen();self.owner()
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public'",read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded public table names')
        parts=['SELECT '+q(t)+" name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') digest FROM public."+t+' t' for t in tables]
        return json.loads(self.sql('SELECT json_object_agg(name,digest) FROM ('+' UNION ALL '.join(parts)+') h',read=True))
    def assert_fixture(self,count):
        value=json.loads(self.sql("SELECT json_build_object('incidents',(SELECT count(*) FROM ops_incidents),'occurrences',(SELECT count(*) FROM ops_alert_occurrences),'valid',(SELECT count(*) FROM ops_incidents WHERE source_id="+q(self.source_id)+" AND fingerprint="+q(self.source_id)+" AND application_id="+q(self.app)+" AND environment_id="+q(self.env)+" AND alert_status='resolved' AND NOT suspected AND assignee_id='' AND ends_at=starts_at+interval '1 second' AND starts_at>="+q(self.anchor.isoformat())+"::timestamptz AND starts_at<="+q((self.anchor+datetime.timedelta(seconds=count-1)).isoformat())+"::timestamptz),'distinct_starts',(SELECT count(DISTINCT starts_at) FROM ops_incidents),'notifications',(SELECT count(*) FROM ops_notifications))",read=True))
        check(value==dict(incidents=count,occurrences=count,valid=count,distinct_starts=count,notifications=0),'actual HTTP incident/occurrence scope or notifications differ')
        return value
    def ingress(self,anchor,start,count,index):
        body=fixture_batch(self.source_id,self.nonce,self.marker,anchor,start,count)
        self.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.source_id,body,want=202,index=index,scenario='fixture',
            validate=lambda v:check(v==dict(accepted=count,notification_delivery='not_configured'),'exact ingress acceptance differs'))
    def read_boundary(self,count):
        before=self.ledger();ro._private(self.work/('ledger-'+str(count)+'-before.json'),before)
        for name,path in QUERIES.items():
            index=0 if name.endswith('inbox') else 1
            at=datetime.datetime.now(datetime.timezone.utc)
            value,headers=self.call('reader','GET',name+'?'+path,want=200 if count==5000 else 503,
                code=None if count==5000 else 'pagination_required',index=index,scenario='boundary')
            end=datetime.datetime.now(datetime.timezone.utc)
            if count==5000:
                if index==0:verify_inbox(value,headers,count,at,end)
                else:verify_groups(value,self.app,self.env,self.source_id,self.anchor,count)
            else:verify_limit(value,headers)
            selected_headers={k:v for k,v in headers.items() if k.lower() in ('content-type','cache-control')}
            ro._private(self.work/('read-'+str(count)+'-'+str(index)+'.json'),dict(response=value,request_started=at.isoformat(),request_ended=end.isoformat(),headers=selected_headers))
            self.marker_case(('inbox' if index==0 else 'groups')+'_'+str(count),dict(count=count,status=200 if count==5000 else 503,API_index=index))
        after=self.ledger();ro._private(self.work/('ledger-'+str(count)+'-after.json'),after)
        check(before==after,'four candidate boundary reads must not write any public table')
        return before
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader'):self.login(actor)
        self.inputs();initial=self.ledger();ro._private(self.work/'ledger-initial.json',initial)
        self.anchor=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)-datetime.timedelta(seconds=6000)
        for batch in range(25):self.ingress(self.anchor,batch*200,200,batch%2)
        witness5000=self.assert_fixture(5000);self.marker_case('fixture_5000',dict(ingress=25,**witness5000))
        ledger5000=self.read_boundary(5000)
        self.ingress(self.anchor,5000,1,1);witness5001=self.assert_fixture(5001)
        self.marker_case('fixture_5001',dict(ingress=26,**witness5001));ledger5001=self.read_boundary(5001)
        changed={t for t in initial if initial[t]!=ledger5000[t]}
        check(changed=={'ops_incidents','ops_alert_occurrences'} and set(initial)==set(ledger5000)==set(ledger5001)
            and {t for t in ledger5000 if ledger5000[t]!=ledger5001[t]}==changed,'ingress modified tables outside exact alert/occurrence ledger')
        self.marker_case('ledger',dict(all_public_tables=len(initial),read_delta=0,ingress_tables=sorted(changed)))
        check(self.calls==33,'reviewed 33 HTTP requests differ')
        result=dict(run_id=self.run_id,http_calls=self.calls,fixture_ingress=26,target_GET=4,setup_HTTP=3,
            normal_added=0,registration_evidence=False,source_tree_sha=SOURCE,binary_sha256=BINARY,
            required_cases=REQUIRED_CASES,synthetic_history=True,full_list_and_pagination_acceptance=False,
            unverified=['Inbox default all 5000 serialization','over-capacity complete reading/pagination','full chain','performance','24h'])
        ro._private(self.work/'capacity-result.json',result);return result
    def cleanup(self):
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:sock.settimeout(.2);check(sock.connect_ex(('127.0.0.1',port))!=0,'own capacity listener remains')
        emit('capacity.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True))
def digest_bytes(value):return hashlib.sha256(value).hexdigest()
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true')
    parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path)
    args=parser.parse_args(argv)
    if args.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,
            required_cases=REQUIRED_CASES,http_calls=33,ingress=26,target_GET=4,setup_HTTP=3,normal_added=0,
            API_seconds=12,provider_seconds=5,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('root --live/run-id/admission required')
    try:
        check(args.root.resolve()==ROOT,'reviewed checkout required');admission(args.admission,args.run_id)
        with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked:emit('capacity.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as error:emit('capacity.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
