"""Two scoped real Alertmanager projections, with two explicit missing prerequisites.

Only a UUID-local authenticated webhook produces an alert. Current AM config is
read, never installed. Empty inhibition rules and incomplete policy fields remain
honest. No seven-day history, delivery activity, or route-tree success is invented.
"""
from __future__ import annotations
import argparse,contextlib,datetime,fcntl,hashlib,http.client,importlib.util,json,os,re,secrets,socket,stat,subprocess,threading,time,uuid
from pathlib import Path
from urllib.parse import urlencode
import identity
import api_ingress_coverage as ingress

ROOT=Path(__file__).resolve().parents[2]
PORTS=(18632,18633)
BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
RELAY_PORT=18634
MODULE='formal-api-alert-policy-functional'
TARGETS=(('GET','/alert-groups/{id}/policy'),('GET','/inhibitions'))
SUPPORT=(('GET','/notification-routes'),('POST','/inhibitions/preview'),('POST','/auth/login'),('POST','/services'),('GET','/alert-groups'),('POST','/webhooks/alertmanager/{source}'))
REQUIRED_CASES=('alert_policy.source_scope','alert_policy.native_ingress','alert_policy.policy','alert_policy.inhibitions_empty','alert_policy.authorization','alert_policy.boundaries','alert_policy.failure_recovery','alert_policy.revocation','alert_policy.audit_consistency')
Blocked=identity.Blocked
check=lambda value,message:None if value else (_ for _ in ()).throw(AssertionError(message))
# A private module instance reuses the reviewed acquisition/ownership lifecycle
# without changing a shared module namespace or any helper source file.
spec=importlib.util.spec_from_file_location('alert_policy_private_projection',ROOT/'tools/testlab/api_native_projection_coverage.py')
native=importlib.util.module_from_spec(spec);spec.loader.exec_module(native)
native.PORTS,native.BASES,native.TARGETS,native.SUPPORT=PORTS,BASES,TARGETS,SUPPORT
_thread=threading.local();_original_transport=native.transport
ACTIVE_CHAIN=None


def emit(name,status,actual,**fields):
    row=dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)
    if ACTIVE_CHAIN is not None and isinstance(fields.get('interface'),dict):
        number=fields['interface'].get('call_id','').rsplit('-',1)[-1]
        path=ACTIVE_CHAIN.work/('http-response-'+number+'.json')
        if hasattr(_thread,'response'):
            native._private(path,dict(_thread.response,call_id=fields['interface']['call_id']))
        if path.is_file():row['artifact']=str(path.relative_to(ACTIVE_CHAIN.root))
    print(json.dumps(row,ensure_ascii=False),flush=True)
native.emit=emit


def capture_transport(*args,**kwargs):
    status,body,headers=_original_transport(*args,**kwargs)
    if hasattr(_thread,'chain'):
        chain=_thread.chain
        _thread.headers=headers
        _thread.response=dict(run_id=chain.run_id,source_tree_sha=chain.data['source_tree_sha'],binary_sha256=chain.data['binary_sha256'],helper_sha256=chain.data['helper_sha256'],method=args[1],path=args[2],status=status,response=json.loads(body) if body else None,headers={k:v for k,v in headers.items() if k.lower() in ('etag','cache-control','content-type')})
    return status,body,headers
native.transport=capture_transport


def helper_hashes():
    files=('api_alert_policy_functional_coverage.py','api_native_projection_coverage.py','api_ingress_coverage.py','identity.py','env.py','faults.py','tls_entry.py','components/prometheus.yml','components/testlab-rules.yml','components/alertmanager.yml')
    return {'tools/testlab/'+name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest()for name in files}


def allowed_relay(method,path):
    if method!='GET' or path!='/api/v2/status':raise Blocked('policy relay only permits exact read-only AM status')


def source_configuration(label,app,env,source,token,am_token):
    check(re.fullmatch('[a-f0-9]{32}',str(app)) is not None and re.fullmatch('[a-f0-9]{32}',str(env)) is not None and label.startswith('policy-') and source,'HTTP-generated UUID service and environment required')
    return {'operations':{'sources':[{'id':source,'token_file':str(token),'mappings':[{'labels':{'application':label,'environment':'test','policy_scope':label[7:]},'application_id':app,'environment_id':env}]}],'feishu_channels':[]},'alertmanager':{'environments':{'test':{'url':'http://127.0.0.1:'+str(RELAY_PORT),'allow_http':True,'bearer_token_file':str(am_token),'environment_label':'environment','application_bindings':{label:app},'source_ids':[source]}}}}


def source_revision(value):
    raw=value.get('config',{}).get('original') if isinstance(value,dict) else None
    if not isinstance(raw,str) or not raw or len(raw)>65536:raise Blocked('actual AM original config unavailable')
    # Current reviewed source has no inhibition rule or direct environment
    # branches. Reject changed native shape rather than replacing its output.
    if re.search(r'^inhibit_rules:\s*\S|^\s+routes:\s*$',raw,re.M):raise Blocked('reviewed flat/no-inhibition configuration changed')
    return 'sha256:'+hashlib.sha256(raw.encode()).hexdigest()


def verify_policy(value,gid):
    check(value.get('group_id')==gid and value.get('group_by')==['application','environment','alertname'] and value.get('route')=='testlab-capture' and value.get('receivers')==['testlab-capture'],'native current group policy mismatch')
    check({r['key']:r['value']for r in value['timing']}=={'group_wait':'1s','group_interval':'3s','repeat_interval':'1h'},'actual loaded route timing mismatch')
    check(value.get('policy_source')=='configured_alertmanager' and isinstance(value.get('policy_observed_at'),str) and value['policy_observed_at'] and value.get('timeline')==[] and value.get('timeline_scope')=='platform_queue' and value.get('timeline_truncated') is False,'policy provenance/local no-notification timeline mismatch')
    check(value.get('partial') is True and value.get('inhibition') is None and value.get('local_grouping')==['environment_id','application_id','source_id','fingerprint'] and set(value.get('unavailable',[]))=={'inhibition','timeline[].done','external_notification_history','receiver_acknowledgement'},'missing real activity must remain explicit')


def verify_inhibitions(value,revision):
    check(value.get('items')==[] and value.get('partial') is True and value.get('unavailable')==['activity','hit_counts'] and value.get('unavailable_environments')==[] and value.get('unscoped_present') is False,'empty current rules must preserve partial activity contract')
    check(value.get('config_revisions')=={'test':revision} and set(value.get('observed_at',{}))=={'test'} and isinstance(value['observed_at']['test'],str) and value['observed_at']['test'],'empty rules require actual revision/time provenance')


def emit_blockers():
    emit('alert_policy.notification_routes_prerequisite','blocked',{'path':'/notification-routes','error_code':'route_source_unavailable','needed':'owned AM direct exact environment branch; current reviewed flat root has none','normal_verified':False})
    emit('alert_policy.inhibition_history_prerequisite','blocked',{'path':'/inhibitions/preview','error_code':'not_connected','needed':'trusted complete environment-bound seven-day history producer; not present','normal_verified':False,'synthetic_history_used':False})


class Contract(native.Contract):
    pass


def doctor(root):
    data=native.doctor(root)
    if data['source_tree_sha']!=ingress.SOURCE_SHA or native.inspect_pg(data['postgres_container']).get('Id')!=ingress.PG_CID:raise Blocked('policy frozen Go or exact owned PG differs')
    identity.free_port(RELAY_PORT)
    components={name:ingress.component_guard(name,native.inspect_pg('aiops-'+native.OWNER+'-'+name))for name in ('prometheus','alertmanager')}
    if helper_hashes()['tools/testlab/components/alertmanager.yml']!=ingress.AM_SOURCE_SHA:raise Blocked('reviewed AM sole-local receiver config changed')
    return dict(data,components=components,helper_sha256=helper_hashes())


class Relay:
    def __init__(self,data,token):
        self.data,self.token=data,token;self.lock=threading.Lock();self.failed=False;self.hold=False;self.seen=threading.Event();self.release=threading.Event();self.records=[];self.closed=False
        relay=self
        class Handler(native.BaseHTTPRequestHandler if hasattr(native,'BaseHTTPRequestHandler') else __import__('http.server',fromlist=['BaseHTTPRequestHandler']).BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    allowed_relay(self.command,self.path)
                    if not secrets.compare_digest(self.headers.get('Authorization',''),'Bearer '+relay.token):self.send_error(403);return
                    with relay.lock:failed,hold=relay.failed,relay.hold;relay.hold=False
                    if hold:relay.seen.set();check(relay.release.wait(15),'bounded native status hold expired')
                    if failed:status,body=503,b'{"error":"owned_read_fault"}'
                    else:
                        expected=relay.data['components']['alertmanager'];ingress.component_guard('alertmanager',native.inspect_pg(expected['name']),expected)
                        conn=http.client.HTTPConnection('127.0.0.1',18571,timeout=4)
                        try:conn.request('GET','/api/v2/status');response=conn.getresponse();status=response.status;body=response.read(65537);check(len(body)<=65536,'bounded AM read exceeded')
                        finally:conn.close()
                    with relay.lock:relay.records.append({'method':'GET','path':self.path,'status':status,'fault':failed,'held':hold})
                    self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
                except Exception:
                    try:self.send_error(502)
                    except OSError:pass
            def do_POST(self):self.send_error(405)
            do_DELETE=do_PUT=do_PATCH=do_POST
        from http.server import ThreadingHTTPServer
        self.server=ThreadingHTTPServer(('127.0.0.1',RELAY_PORT),Handler);self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True)
    def start(self):self.thread.start()
    def close(self):
        if self.closed:return
        self.release.set()
        if self.thread.is_alive():self.server.shutdown();self.thread.join(timeout=5)
        self.server.server_close();self.closed=True


class Chain(native.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id);self.contract=Contract(root);self.relay=None;self.pinned={};self.configured=False;self.receiver_load=None;self.module=MODULE
        self.service_key='policy-'+self.nonce;self.source_id='policy-source-'+self.nonce
        self.state.update(suite=MODULE,actual_results_source='HTTP fresh service + authenticated UUID-local AM webhook; current real AM status',proof_scope='two native scoped projections; routing/history prerequisites remain blocked',ports=list(PORTS)+[RELAY_PORT],notifications='disabled; no exporter signal or shared AM write')
        native._private(self.work/'state.json',self.state)
    def frozen(self):
        super().frozen()
        if helper_hashes()!=self.data['helper_sha256']:raise Blocked('policy helper/mounted source changed')
        for raw,expected in self.pinned.items():
            path=Path(raw)
            if path.is_symlink() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hashlib.sha256(path.read_bytes()).hexdigest()!=expected:raise Blocked('UUID private configuration changed')
    def pin(self,*paths):
        for path in paths:self.pinned[str(path)]=hashlib.sha256(Path(path).read_bytes()).hexdigest()
    def pg(self,*args,**kwargs):
        if native.inspect_pg(self.data['postgres_container']).get('Id')!=ingress.PG_CID:raise Blocked('policy owned PG CID changed')
        return super().pg(*args,**kwargs)
    def key_guard(self,stage):
        self.frozen()
        for name,expected in self.data['components'].items():ingress.component_guard(name,native.inspect_pg(expected['name']),expected)
        ingress.Chain.receiver_proof(self,stage)
    def start(self):
        generation=self.generation;self.generation+=1;self.frozen()
        for index,port in enumerate(PORTS):
            identity.free_port(port)
            dsn='postgres://'+self.db_user+':'+self.db_password+'@127.0.0.1:15461/'+self.database+'?'+urlencode(dict(sslmode='disable',options='-csearch_path=public',application_name=self.pg_application('api',index)))
            variables=dict(DATABASE_URL=dsn,AIOPS_V3_INSTANCE_CONFIG=str(self.work/'instance.json'),AIOPS_ADDR='127.0.0.1:'+str(port),AIOPS_PUBLIC_ORIGIN=BASES[index],AIOPS_SECURE_COOKIES='false',AIOPS_ADMIN_EMAIL=self.admin_email,AIOPS_ADMIN_PASSWORD=self.password,AIOPS_TRUSTED_PROXY_CIDRS='127.0.0.1/32',AIOPS_V3_MODEL_BINDINGS_FILE=str(self.work/'models.json'),AIOPS_V3_TEXT_COMMANDS_CONFIG=str(self.work/'commands.json'),OPENAI_MODEL='projection-synthetic',OPENAI_API_KEY='synthetic-no-inference',OPENAI_BASE_URL='http://127.0.0.1:9/v1',EMBEDDING_PROVIDER='openai',EMBEDDING_MODEL='projection-synthetic',EMBEDDING_API_KEY='synthetic-no-embedding',EMBEDDING_BASE_URL='http://127.0.0.1:9/v1',AIOPS_V3_SILENCE_EXECUTION_ENABLED='false',AIOPS_V3_PASSKEY_ENABLED='false')
            if self.configured:variables.update(AIOPS_OPERATIONS_CONFIG=str(self.work/'operations.json'),AIOPS_V3_ALERTMANAGER_CONFIG=str(self.work/'alertmanager.json'))
            config=self.work/('startup-'+str(generation)+'-'+str(index)+'.json');native._private(config,variables);self.pin(config)
            log=os.open(self.work/('api-'+str(generation)+'-'+str(index)+'.log'),os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:
                with native.acquisition_signals():
                    proc=subprocess.Popen([self.data['binary']],cwd=self.root,env=native.command_env(variables,{}),stdout=log,stderr=log)
                    self.processes.append(proc);self.state['pids'].append(proc.pid);native._private(self.work/'state.json',self.state)
            finally:os.close(log)
            deadline=time.monotonic()+90
            while time.monotonic()<deadline:
                if proc.poll() is not None:raise Blocked('fresh policy API exited; immutable private generation log retained')
                try:
                    if _original_transport(BASES[index],'GET','/healthz',timeout=2)[0]==200:
                        check(native._process_port(proc.pid,port),'held child does not own exact policy listener');break
                except OSError:pass
                time.sleep(.2)
            else:raise Blocked('fresh policy readiness timeout')
            emit('alert_policy.api_ready.'+str(generation)+'.'+str(index),'passed',{'port':port,'held_pid':proc.pid})
    def call(self,*args,**kwargs):
        self.frozen();_thread.chain=self
        try:return super().call(*args,**kwargs)
        finally:
            del _thread.chain
            if hasattr(_thread,'headers'):del _thread.headers
            if hasattr(_thread,'response'):del _thread.response
    def configure(self):
        self.call('admin','POST','/api/v1/services',{'items':[{'id':self.service_key,'tier':'T1'}]},want=201,purpose='fixture',validate=lambda v:check(v=={'created':1},'public service producer differs'))
        row=json.loads(self.sql("SELECT json_build_object('application_id',a.id,'environment_id',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+native.quote(self.service_key)+" AND e.key='test' AND NOT a.archived AND NOT e.archived",read=True));self.service_row=row
        self.source_token,self.am_token=secrets.token_urlsafe(32),secrets.token_urlsafe(32)
        token,am_token=self.work/'source-token',self.work/'am-token'
        native.private_file(token,self.source_token);native.private_file(am_token,self.am_token)
        config=source_configuration(self.service_key,row['application_id'],row['environment_id'],self.source_id,token,am_token)
        for name in ('operations','alertmanager'):native._private(self.work/(name+'.json'),config[name])
        self.pin(token,am_token,self.work/'operations.json',self.work/'alertmanager.json',self.work/'instance.json',self.work/'models.json',self.work/'commands.json',self.work/'credentials.json')
        self.key_guard('before-readonly-setup');self.stop_for_source_configuration()
        self.relay=Relay(self.data,self.am_token);self.relay.start();self.configured=True;self.start()
        statements=[]
        for module,action in [('silences','view'),('silences','manage'),('alert_rules','view'),('alert_rules','manage')]:
            statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(map(native.quote,(self.roles['reader'],module,action,'environment','test')))+');')
        self.sql('BEGIN;'+''.join(statements)+'COMMIT;')
        native._private(self.work/'source-scope.json',{'run_id':self.run_id,'source_id':self.source_id,'service':row,'synthetic_input':'HTTP-generated UUID-local alert; not a real AM delivery event','native_policy_source':'owned actual AM status; configuration unchanged','history_provider':None})
        emit('alert_policy.source_scope','passed',{'source_id':self.source_id,'application_id':row['application_id'],'environment_id':row['environment_id'],'am_read_only':True})
    def revision(self):
        expected=self.data['components']['alertmanager'];ingress.component_guard('alertmanager',native.inspect_pg(expected['name']),expected)
        conn=http.client.HTTPConnection('127.0.0.1',18571,timeout=4)
        try:conn.request('GET','/api/v2/status');response=conn.getresponse();raw=response.read(65537);check(response.status==200 and len(raw)<=65536,'owned AM source read invalid');value=json.loads(raw)
        finally:conn.close()
        native._private(self.work/'current-am-status.json',value);return source_revision(value)
    def normal(self,index=0,scenario='normal'):
        self.call('reader','GET','/api/v1/alert-groups/'+self.gid+'/policy',index=index,scenario=scenario,validate=lambda v:verify_policy(v,self.gid))
        def verify(v):
            verify_inhibitions(v,self.config_revision);check(native.header(_thread.headers,'ETag')=='"'+self.config_revision+'"','real revision ETag mismatch')
        self.call('reader','GET','/api/v1/inhibitions',index=index,want=206,scenario=scenario,validate=verify)
    def run(self):
        self.provision();self.start();self.seed();self.login('admin');self.configure()
        for name in self.users:self.login(name)
        self.config_revision=self.revision()
        now=datetime.datetime.now(datetime.timezone.utc)
        alert={'status':'firing','fingerprint':self.source_id,'startsAt':now.isoformat(),'labels':{'application':self.service_key,'environment':'test','policy_scope':self.nonce,'alertname':'OwnedPolicyRead','severity':'P2'},'annotations':{'summary':'UUID local policy functional input; no notification'}}
        self.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.source_id,{'alerts':[alert]},want=202,headers={'Authorization':'Bearer '+self.source_token},purpose='fixture',validate=lambda v:check(v=={'accepted':1,'notification_delivery':'not_configured'},'public webhook unexpectedly delivered a notification'))
        groups,_=self.call('reader','GET','/api/v1/alert-groups',purpose='fixture')
        matches=[r for r in groups['items']if r['source_id']==self.source_id and r['fingerprint']==self.source_id]
        check(len(matches)==1 and matches[0]['count']==1 and matches[0]['state']=='firing','one real HTTP-produced group required');self.gid=matches[0]['id']
        emit('alert_policy.native_ingress','passed',{'group_id':self.gid,'source_id':self.source_id,'synthetic_input':True,'actual_notification':False})
        before=json.loads(self.sql("SELECT json_build_object('notifications',(SELECT count(*) FROM ops_notifications),'incidents',(SELECT count(*) FROM ops_incidents),'audit',(SELECT count(*) FROM management_audit),'claims',(SELECT count(*) FROM v3_silence_execution_claims))",read=True))
        self.normal();self.normal(index=1)
        emit('alert_policy.policy','passed',{'group_id':self.gid,'actual_AM_policy':True,'missing_delivery_activity':True})
        emit('alert_policy.inhibitions_empty','passed',{'revision':self.config_revision,'real_rules':0,'success_status':206,'activity_hits_unverified':True})
        policy='/api/v1/alert-groups/'+self.gid+'/policy'
        preview={'application_id':self.service_row['application_id'],'source':[{'k':'severity','op':'=','v':'P1'}],'target':[{'k':'severity','op':'=','v':'P2'}],'equal':['alertname']}
        for method,path,body in [('GET',policy,None),('GET','/api/v1/inhibitions',None),('GET','/api/v1/notification-routes',None),('POST','/api/v1/inhibitions/preview',preview)]:
            self.call(None,method,path,body,want=401,code='unauthorized',scenario='authorization')
        self.call('denied','GET',policy,want=404,code='not_found',scenario='authorization')
        self.call('denied','GET','/api/v1/inhibitions',want=403,code='forbidden',scenario='authorization')
        self.call('reader','GET',policy,environment='staging',want=403,code='forbidden',scenario='authorization')
        emit('alert_policy.authorization','passed',{'anonymous':4,'denied_module':2,'cross_environment':1})
        self.call('reader','GET','/api/v1/alert-groups/unknown/policy',want=404,code='not_found',scenario='boundary')
        self.call('reader','GET','/api/v1/notification-routes',environment='all',want=409,code='single_environment_required',scenario='boundary')
        invalid=dict(preview,target=[{'k':'severity','op':'=','v':'P1'}])
        self.call('reader','POST','/api/v1/inhibitions/preview',invalid,want=422,code='invalid_inhibition_draft',scenario='boundary')
        self.call('reader','POST','/api/v1/inhibitions/preview',dict(preview,application_id='foreign'),want=404,code='application_not_found',scenario='boundary')
        emit('alert_policy.boundaries','passed',{'missing_group':True,'single_environment':True,'P1_target_forbidden':True,'unbound_application':True})
        self.call('reader','GET','/api/v1/notification-routes',want=503,code='route_source_unavailable',scenario='failure',purpose='dependency_unavailable')
        self.call('reader','POST','/api/v1/inhibitions/preview',preview,want=501,code='not_connected',scenario='failure',purpose='dependency_unavailable')
        with self.relay.lock:self.relay.failed=True
        try:
            self.call('reader','GET',policy,want=503,code='alert_policy_unavailable',scenario='failure')
            self.call('reader','GET','/api/v1/inhibitions',want=503,code='alertmanager_unavailable',scenario='failure')
        finally:
            with self.relay.lock:self.relay.failed=False
        self.normal(index=1,scenario='failure')
        check(sum(r['fault'] for r in self.relay.records)==2,'actual dependency failures must be consumed exactly twice')
        emit('alert_policy.failure_recovery','passed',{'actual_relay503':2,'recovery_instance':1,'AM_modified':False})
        self.revocation(policy)
        after=json.loads(self.sql("SELECT json_build_object('notifications',(SELECT count(*) FROM ops_notifications),'incidents',(SELECT count(*) FROM ops_incidents),'audit',(SELECT count(*) FROM management_audit),'claims',(SELECT count(*) FROM v3_silence_execution_claims))",read=True))
        check(before==after and before['notifications']==0 and before['claims']==0 and before['incidents']==1,'read-only/negative/fault/revoke operations changed business state or dispatched notifications')
        evidence={table:json.loads(self.sql('SELECT COALESCE(json_agg(to_jsonb(t)),\'[]\'::json) FROM '+table+' t',read=True))for table in ('ops_incidents','ops_alert_occurrences','management_audit','ops_notifications','v3_silence_execution_claims')}
        incident=evidence['ops_incidents'][0]
        check(len(evidence['ops_incidents'])==1 and incident['source_id']==self.source_id and incident['application_id']==self.service_row['application_id'] and incident['environment_id']==self.service_row['environment_id'] and incident['labels']==alert['labels'] and len(evidence['ops_alert_occurrences'])==1 and evidence['ops_alert_occurrences'][0]['incident_id']==incident['id'],'actual webhook/receipt/application/environment lineage differs')
        native._private(self.work/'database-evidence.json',evidence);native._private(self.work/'relay-receipts.json',self.relay.records)
        emit('alert_policy.audit_consistency','passed',{'before':before,'after':after,'business_sql_writes':0,'private_evidence':True})
        self.key_guard('acceptance-end');emit_blockers()
        return {'group_id':self.gid,'qualified_targets':TARGETS,'config_revision':self.config_revision,'source_tree_sha':self.data['source_tree_sha'],'helper_sha256':self.data['helper_sha256'],'normal_targets':2,'blocked_targets':2,'data_source':'UUID-local synthetic authenticated webhook input + real owned AM config','unverified':['route environment subtree','complete seven-day inhibition history','nonempty inhibition rules/activity/hit count','actual external notifications','performance/soak']}
    def revocation(self,path):
        self.relay.seen.clear();self.relay.release.clear()
        with self.relay.lock:self.relay.hold=True
        errors=[]
        def read():
            try:self.call('reader','GET',path,want=404,code='not_found',scenario='authorization',purpose='concurrency')
            except BaseException as error:errors.append(error)
        thread=threading.Thread(target=read);thread.start()
        try:
            check(self.relay.seen.wait(5),'request did not reach real provider boundary before revocation')
            self.sql('BEGIN; DELETE FROM v3_role_grants WHERE role_id='+native.quote(self.roles['reader'])+" AND module='events' AND action='view'; COMMIT;")
            self.call('reader','GET',path,index=1,want=404,code='not_found',scenario='authorization',purpose='concurrency')
        finally:self.relay.release.set();thread.join(timeout=15)
        check(not thread.is_alive() and not errors,'inflight or cross-instance request failed exact revocation contract')
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(map(native.quote,(self.roles['reader'],'events','view','environment','test')))+')')
        self.normal();emit('alert_policy.revocation','passed',{'inflight_rechecked':True,'second_instance_rejected':True,'restored_exact_fixture_grant':True})
    def cleanup(self):
        errors=[]
        if self.relay:
            try:self.relay.close()
            except Exception:errors.append('read-only AM relay cleanup failed')
        super().cleanup()
        for port in (*PORTS,RELAY_PORT):identity.free_port(port)
        if errors:raise AssertionError('; '.join(errors))
        emit('alert_policy.cleanup','passed',{'own_ports_absent':list(PORTS)+[RELAY_PORT],'AM_config_changed':False,'all_resources_owned_UUID':True})


def main():
    global ACTIVE_CHAIN
    parser=argparse.ArgumentParser();parser.add_argument('--root',default=str(ROOT));parser.add_argument('--live',action='store_true');args=parser.parse_args();root=Path(args.root).resolve()
    if not args.live:print(json.dumps({'normal_targets':TARGETS,'blocked':['notification-routes','inhibitions/preview'],'live':False}));return 0
    chain=None
    fd=os.open(root/'.local/testlab/alert-policy-functional.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private own lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        data=doctor(root);emit('alert_policy.doctor','passed',{'ports':list(PORTS)+[RELAY_PORT],'source_tree_sha':data['source_tree_sha']})
        chain=Chain(root,data,str(uuid.uuid4()));ACTIVE_CHAIN=chain
        result=identity.execute(chain);native._private(chain.work/'result.json',{'status':'blocked','supported_paths_verified':True,'actual':result})
        emit('alert_policy.formal_chain','passed',result,artifact=str((chain.work/'result.json').relative_to(root)));return 0
    except BaseException as error:
        status='blocked'if isinstance(error,Blocked)else'interrupted'if isinstance(error,(KeyboardInterrupt,SystemExit))else'failed'
        if chain:
            native._private(chain.work/'result.json',{'status':status,'error':identity.safe_error(error)})
            native._private(chain.work/'diagnostic-error.json',{'error_type':type(error).__name__,'error':identity.safe_error(error)})
        emit('alert_policy.formal_chain',status,identity.safe_error(error));return 2 if status=='blocked'else 1
    finally:ACTIVE_CHAIN=None;os.close(fd)

if __name__=='__main__':
    with identity.cooperative_signals():raise SystemExit(main())
