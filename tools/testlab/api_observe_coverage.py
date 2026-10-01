"""Owned real Loki logs and OTel→Tempo trace-by-ID formal HTTP acceptance.

No sampled trace search is promoted to success: TempoSearchFailClosed remains
false. HTTP creates services, mappings and their audit; SQL writes identities
and grants only. Four UUID-scoped synthetic logs and two closed traces remain
in the owned components (Loki until retirement, Tempo configured 24h retention).
"""
from __future__ import annotations
import argparse,contextlib,copy,hashlib,http.client,json,re,socket,stat,threading,time,uuid
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import urlencode,urlsplit
import api_readonly_chain as readonly

ROOT=readonly.ROOT
OWNER=readonly.OWNER
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
MODULE='formal-api-observe'
RELAY_PORT=18616
RELAY_URL='http://127.0.0.1:'+str(RELAY_PORT)
LIMIT=4<<20
TARGETS=(('GET','/logs'),('GET','/traces/{id}'))
SUPPORT=(('GET','/traces'),('POST','/services'),('GET','/service-mappings'),('PUT','/service-mappings'),('POST','/auth/login'))
REQUIRED_CASES=('observe.source_scope','observe.real_ingest','observe.http_logs','observe.http_trace_detail',
                'observe.authorization','observe.boundaries','observe.failure_recovery',
                'observe.revocation','observe.audit_consistency')
COMPONENTS={
 'loki':dict(port=18573,internal='3100',image='grafana/loki:3.7.0',config='loki.yml'),
 'tempo':dict(port=18574,internal='3200',image='grafana/tempo:2.7.2',config='tempo.yml'),
 'otel':dict(port=18575,internal='4318',image='otel/opentelemetry-collector-contrib:0.157.0',config='otel.yml')}
check,Blocked=readonly.check,readonly.Blocked


def emit(name,status,actual,**fields):
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)


def hash_file(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def helper_hashes():
    files=('tools/testlab/api_readonly_chain.py','tools/testlab/api_observe_coverage.py',
           'tools/testlab/components/loki.yml','tools/testlab/components/tempo.yml','tools/testlab/components/otel.yml')
    return {name:hash_file(ROOT/name) for name in files}


def guard_component(provider,info,pinned=None):
    if provider not in COMPONENTS or not isinstance(info,dict):raise Blocked('owned component identity is absent')
    spec=COMPONENTS[provider];name='aiops-'+OWNER+'-'+provider
    config=info.get('Config',{})
    expected_source=str(ROOT/'tools/testlab/components'/spec['config'])
    mounts=[m for m in info.get('Mounts',[]) if m.get('Source')==expected_source and m.get('Destination')=='/etc/testlab-config.yml' and m.get('RW') is False]
    if (info.get('Name')!='/'+name or config.get('Image')!=spec['image'] or
        config.get('Labels',{}).get('aiops.testlab.owner')!=OWNER or not info.get('State',{}).get('Running') or
        not re.fullmatch(r'[a-f0-9]{64}',str(info.get('Id',''))) or
        not re.fullmatch(r'sha256:[a-f0-9]{64}',str(info.get('Image',''))) or
        info.get('NetworkSettings',{}).get('Ports',{}).get(spec['internal']+'/tcp')!=[{'HostIp':'127.0.0.1','HostPort':str(spec['port'])}] or len(mounts)!=1):
        raise Blocked('owned '+provider+' name/CID/image/exact port/readonly config mismatch')
    result=dict(provider=provider,name=name,container_id=info['Id'],image_id=info['Image'],image=spec['image'],port=spec['port'],config_source=expected_source)
    if pinned is not None and result!=pinned:raise Blocked('owned '+provider+' immutable identity changed')
    return result


def guard_live_components(pinned):
    if set(pinned)!=set(COMPONENTS):raise Blocked('complete explicit Loki/Tempo/OTel identity set required')
    return {provider:guard_component(provider,readonly.inspect_pg(identity['name']),identity) for provider,identity in pinned.items()}


def doctor(root):
    data=readonly.doctor(root)
    if readonly.inspect_pg(data['postgres_container']).get('Id')!=PG_CID:raise Blocked('owned PG immutable CID changed')
    identities={provider:guard_component(provider,readonly.inspect_pg('aiops-'+OWNER+'-'+provider)) for provider in COMPONENTS}
    with socket.socket() as sock:
        sock.settimeout(.3)
        if sock.connect_ex(('127.0.0.1',RELAY_PORT))==0:raise Blocked('observe relay listener occupied; refusing adoption')
    return dict(data,component_identities=identities,helper_sha256=helper_hashes())


def component_request(provider,method,path,pinned,payload=None):
    raw=urlsplit(path)
    allowed=(provider=='loki' and ((method=='POST' and raw.path=='/loki/api/v1/push') or
             (method=='GET' and raw.path in ('/loki/api/v1/query','/loki/api/v1/query_range')))) or (
             provider=='otel' and method=='POST' and raw.path=='/v1/traces') or (
             provider=='tempo' and method=='GET' and re.fullmatch(r'/api/traces/[a-f0-9]{32}',raw.path))
    if not allowed or raw.scheme or raw.netloc or raw.fragment or len(path)>32768 or any(ord(x)<32 for x in path):
        raise Blocked('component operation outside exact ingestion/read-only endpoint boundary')
    if pinned is None:raise Blocked('immutable component identity required before transport')
    guard_component(provider,readonly.inspect_pg(pinned['name']),pinned)
    conn=http.client.HTTPConnection('127.0.0.1',COMPONENTS[provider]['port'],timeout=4)
    try:
        body=json.dumps(payload).encode() if payload is not None else None
        conn.request(method,path,body,{'Content-Type':'application/json'} if body is not None else {})
        response=conn.getresponse();raw_body=response.read(LIMIT+1)
        if len(raw_body)>LIMIT:raise Blocked('owned component response exceeds evidence budget')
        return response.status,raw_body
    finally:conn.close()


def relay_target(path):
    parsed=urlsplit(path)
    if parsed.scheme or parsed.netloc or parsed.fragment or len(path)>32768 or '\\' in path or any(ord(x)<32 for x in path):
        raise Blocked('unsafe source relay target')
    if parsed.path.startswith('/loki/') and parsed.path[len('/loki'):] in ('/loki/api/v1/query','/loki/api/v1/query_range'):
        return 'loki',path[len('/loki'):]
    if re.fullmatch(r'/tempo/api/traces/[a-f0-9]{32}',parsed.path) and not parsed.query:
        return 'tempo',parsed.path[len('/tempo'):]
    raise Blocked('relay only forwards bounded Loki reads and exact trace IDs; no search/write endpoint')


class Relay:
    def __init__(self,pinned):
        self.pinned=pinned;self.lock=threading.Lock();self.failed=set();self.counts_by_provider={k:0 for k in ('loki','tempo')};self.injected={k:0 for k in ('loki','tempo')};self.closed=False
        relay=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    provider,path=relay_target(self.path)
                    if self.headers.get('Authorization'):raise Blocked('no source credential forwarding')
                    with relay.lock:
                        relay.counts_by_provider[provider]+=1;failed=provider in relay.failed
                    if failed:
                        with relay.lock:relay.injected[provider]+=1
                        status,body=503,b'{"error":"Explicit owned relay fault"}'
                    else:status,body=component_request(provider,'GET',path,relay.pinned[provider])
                except (Blocked,OSError,http.client.HTTPException):status,body=502,b'{"error":"Owned source unavailable"}'
                self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.send_header('Connection','close');self.end_headers();self.wfile.write(body)
            def do_POST(self):self.send_error(405)
        self.server=ThreadingHTTPServer(('127.0.0.1',RELAY_PORT),Handler);self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def fail(self,provider,enabled):
        if provider not in ('loki','tempo'):raise Blocked('unknown source fault')
        with self.lock:
            if enabled:self.failed.add(provider)
            else:self.failed.discard(provider)
    def counts(self):
        with self.lock:return dict(requests=dict(self.counts_by_provider),injected=dict(self.injected))
    def close(self):
        if self.closed:return
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=4)
        check(not self.thread.is_alive() and self.server.socket.fileno()==-1,'owned observe relay did not close')
        self.closed=True


class Contract(readonly.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment or not parsed.path.startswith('/api/v1/'):raise Blocked('formal API namespace required')
        short=parsed.path[len('/api/v1'):]
        matches=[pattern for candidate,pattern in (*TARGETS,*SUPPORT) if candidate==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(pattern)),short)]
        canonical=short if short in matches else matches[0] if len(matches)==1 else None
        if canonical is None or method.lower() not in self.api['paths'].get(canonical,{}):raise Blocked('operation outside bounded observation contract')
        return '/api/v1'+canonical,'main' if (method,canonical) in self.main else 'auxiliary_or_historical'


def validate_logs(value,nonce,app):
    check(isinstance(value,dict) and value.get('total')=='3' and value.get('errors')=='1','exact scoped log total/error differs')
    items=value.get('items',[]);check(len(items)==3,'log sample does not contain exact three controlled records')
    check({item.get('level') for item in items}=={'ERROR','WARN','INFO'} and all(item.get('service')==app and json.loads(item['message']).get('marker')==nonce for item in items),'log scope/levels/current marker differs')
    patterns=value.get('patterns',[]);check(len(patterns)==3,'full trusted pattern set missing')
    check({(row['template'],row['level'],row['count']) for row in patterns}=={('controlled '+level,level.upper(),'1') for level in ('error','warn','info')},'exact pattern counts differ')
    check(all(sum(row['trend'])==1 for row in patterns) and sum(sum(bucket) for bucket in value['histogram'])==3,'pattern trend/histogram does not cover exact population')


def validate_trace(value,chain):
    check(isinstance(value,dict) and value.get('id')==chain.trace_id and value.get('trace_id')==chain.trace_id and
          value.get('op')=='GET /testlab/observe' and value.get('duration_ms')==20 and value.get('version')=='testlab-v1','trace identity/root operation/version/exact duration differs')
    spans=value.get('spans',[]);check(len(spans)==2,'trace is not the closed two-span fixture')
    check([(s.get('id'),s.get('depth'),s.get('service'),s.get('op'),s.get('status')) for s in spans]==[
        (chain.root_span_id,0,chain.service_name,'GET /testlab/observe','ok'),
        (chain.child_span_id,1,chain.service_name,'operation withheld','error')],'trace tree/scope/error or operation redaction differs')
    check('unreviewed secret operation' not in json.dumps(value) and 'synthetic-authorization-value' not in json.dumps(value),'unapproved operation/attribute leaked')


class Chain(readonly.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id)
        self.contract=Contract(root);self.relay=None;self.config_digest=None;self.services={}
        self.resource='observe-'+self.nonce+'-test';self.hidden_resource='observe-'+self.nonce+'-staging'
        self.service_name='observe-service-'+self.nonce;self.hidden_service_name='observe-hidden-'+self.nonce
        self.trace_id=uuid.uuid5(uuid.UUID(run_id),'trace-visible').hex;self.hidden_trace_id=uuid.uuid5(uuid.UUID(run_id),'trace-hidden').hex
        self.root_span_id=uuid.uuid5(uuid.UUID(run_id),'span-root').hex[:16];self.child_span_id=uuid.uuid5(uuid.UUID(run_id),'span-child').hex[:16]
        self.state.update(suite=MODULE,component_identities=data['component_identities'],helper_sha256=data['helper_sha256'],
                          actual_results_source='HTTP-created services/mappings/audit; controlled real Loki and OTel/Tempo telemetry',
                          source_records_retained='Loki fixture records until owned component retirement; Tempo configured 24h retention; no shared data deletion',
                          synthetic_input_scope='UUID identity/grants, trusted telemetry source config and four current logs/two closed traces')
        readonly._private(self.work/'state.json',self.state)
    def frozen(self):
        if helper_hashes()!=self.data['helper_sha256']:raise Blocked('observe helper/component mounted config changed during run')
        guard_live_components(self.data['component_identities'])
        if self.config_digest is not None:
            path=self.work/'telemetry.json'
            if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hash_file(path)!=self.config_digest:raise Blocked('owned telemetry source config changed')
        super().frozen()
    def pg(self,*args,**kwargs):
        deadline=kwargs.get('deadline');timeout=3 if deadline is None else min(3,deadline-time.monotonic())
        if timeout<=0:raise Blocked('owned observe SQL cleanup budget exhausted')
        if readonly.inspect_pg(self.data['postgres_container'],timeout=timeout).get('Id')!=PG_CID:raise Blocked('owned PG immutable CID changed')
        return super().pg(*args,**kwargs)
    def telemetry_config(self):
        applications={}
        for resource,env,service in ((self.resource,'test',self.service_name),(self.hidden_resource,'staging',self.hidden_service_name)):
            applications[resource]=dict(loki_labels={'testlab_run':self.nonce,'application':resource,'environment':env},
                loki_pattern_field='message_template',loki_complete_lookback_seconds=86400,
                tempo_service=service,tempo_attributes={'application':resource,'environment':env,'testlab_run':self.nonce},
                tempo_safe_operations=['GET /testlab/observe'])
        return dict(loki={'url':RELAY_URL+'/loki','allow_http':True},tempo={'url':RELAY_URL+'/tempo','allow_http':True},
                    tempo_search_fail_closed=False,applications=applications,timeout_seconds=4,max_window_seconds=3600,max_lookback_seconds=86400,max_log_entries=100)
    def startup_environment(self):
        self.frozen()
        if self.config_digest is None:raise Blocked('owned telemetry config is not frozen before API startup')
        return {'AIOPS_TELEMETRY_CONFIG':str(self.work/'telemetry.json')}
    def seed(self):
        super().seed();statements=[]
        for user,env in (('reader','test'),('hidden','staging')):
            for module in ('logs','traces'):
                statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(readonly.quote(x) for x in (self.roles[user],module,'view','environment',env))+');')
        self.sql('BEGIN; '+''.join(statements)+' COMMIT;')
    def call(self,user,method,path,*args,**kwargs):
        self.contract.canonical(method,path)
        if kwargs.get('scenario','normal')=='normal' and kwargs.get('want',200) in range(200,300) and kwargs.get('validate') is None:raise Blocked('normal target requires explicit business validator')
        self.frozen()
        # Functional complete-count reads issue bounded per-bucket source calls.
        # This 90s client budget is not a latency/performance acceptance limit.
        original=readonly.transport
        def bounded(base,verb,route,payload=None,headers=None,timeout=12):
            if base not in readonly.BASES or verb!=method or route!=path:raise Blocked('observe transport scope changed')
            return original(base,verb,route,payload,headers,timeout=90 if method=='GET' and urlsplit(path).path=='/api/v1/logs' else timeout)
        readonly.transport=bounded
        try:return super().call(user,method,path,*args,**kwargs)
        finally:readonly.transport=original
    def case_marker(self,name,actual):
        check(name in REQUIRED_CASES,'unknown stable observation scenario marker');self.frozen()
        emit(name,'passed',dict(run_id=self.run_id,database=self.database,source_tree_sha=self.data['source_tree_sha'],**actual))
    def source_scope(self):
        for key,env,resource in ((self.resource,'test',self.resource),(self.hidden_resource,'staging',self.hidden_resource)):
            self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201,environment=env,scenario='fixture',validate=lambda v:check(v=={'created':1},'native service producer differs'))
            rows,_=self.call('admin','GET','/api/v1/service-mappings',environment=env,scenario='fixture')
            selected=[r for r in rows['rows'] if r['service']==key];check(len(selected)==1 and selected[0]['save']['editable'],'HTTP mapping revision absent')
            initial=selected[0]['save'];self.services[env]=initial
            bindings=[dict(system_id='logs',kind='loki',connector_id='loki',resource=resource),dict(system_id='traces',kind='tempo',connector_id='tempo',resource=resource)]
            body={'environment_id':env,'items':[dict(service_id=initial['service_id'],service_environment_id=initial['service_environment_id'],binding_revision=initial['binding_revision'],bindings=bindings)]}
            self.call('admin','PUT','/api/v1/service-mappings',body,environment=env,index=1,scenario='fixture',validate=lambda v,bindings=bindings,initial=initial,env=env:check(v['environment_id']==env and len(v['items'])==1 and v['items'][0]['bindings']==bindings and v['items'][0]['binding_revision']==initial['binding_revision']+1,'native source mapping commit differs'))
        self.app=self.services['test']['service_id']
        self.logs_path='/api/v1/logs?'+urlencode({'service':self.app,'provider':'loki'})
        self.trace_path='/api/v1/traces/'+self.trace_id+'?'+urlencode({'service':self.app})
        self.case_marker('observe.source_scope',{'http_services':2,'http_mapping_replacements':2,'static_config':str(self.work/'telemetry.json'),'source_config_sha256':self.config_digest,'source_type':'trusted test config only; SQL did not create bindings/business/audit'})
    def make_source_data(self,now):
        streams=[];resource_spans=[]
        for env,resource,service,tid in (('test',self.resource,self.service_name,self.trace_id),('staging',self.hidden_resource,self.hidden_service_name,self.hidden_trace_id)):
            levels=('error','warn','info') if env=='test' else ('error',)
            lines=[[str(now-2_000_000_000+i*1_000_000),json.dumps(dict(level=level,message_template='controlled '+level,message='Controlled current observation '+env,marker=self.nonce,trace_id=tid))] for i,level in enumerate(levels)]
            streams.append({'stream':{'testlab_run':self.nonce,'application':resource,'environment':env},'values':lines})
            begin=now-2_000_000_000
            attrs=[{'key':key,'value':{'stringValue':value}} for key,value in {'service.name':service,'service.version':'testlab-v1','application':resource,'environment':env,'testlab_run':self.nonce}.items()]
            root=dict(traceId=tid,spanId=self.root_span_id,name='unreviewed secret operation',kind=2,startTimeUnixNano=str(begin),endTimeUnixNano=str(begin+20_000_000),status={'code':1},attributes=[{'key':'http.request.method','value':{'stringValue':'GET'}},{'key':'http.route','value':{'stringValue':'/testlab/observe'}},{'key':'Authorization','value':{'stringValue':'synthetic-authorization-value'}}])
            child=dict(traceId=tid,spanId=self.child_span_id,parentSpanId=self.root_span_id,name='unreviewed secret operation',kind=1,startTimeUnixNano=str(begin+3_000_000),endTimeUnixNano=str(begin+13_000_000),status={'code':2},attributes=[])
            resource_spans.append({'resource':{'attributes':attrs},'scopeSpans':[{'scope':{'name':'testlab-observe-controlled-input'},'spans':[root,child]}]})
        return {'loki':{'streams':streams},'otel':{'resourceSpans':resource_spans},'generated_at_unix_ns':now,'data_mode':'synthetic current telemetry via real ingestion; not historical/continuous observation'}
    def real_ingest(self):
        fixture=self.make_source_data(time.time_ns());readonly._private(self.work/'source-data.json',fixture)
        for provider,path,want,payload in (('loki','/loki/api/v1/push',204,fixture['loki']),('otel','/v1/traces',200,fixture['otel'])):
            status,body=component_request(provider,'POST',path,self.data['component_identities'][provider],payload)
            readonly._private(self.work/(provider+'-ingest-receipt.json'),{'status':status,'response_body':body.decode('utf-8','replace'),'writes':1})
            check(status==want,'single real ingestion submission was not accepted; no write retry')
        # Read-only eventual storage readiness, max45s; never replay ingestion.
        deadline=time.monotonic()+45;attempts=[]
        pending={self.trace_id,self.hidden_trace_id}
        while pending:
            for tid in list(pending):
                if time.monotonic()>=deadline:raise Blocked('real Tempo readiness window exhausted; ingestion not retried')
                status,raw=component_request('tempo','GET','/api/traces/'+tid,self.data['component_identities']['tempo'])
                # Tempo may encode IDs as base64; decode canonical raw OTLP below.
                decoded=json.loads(raw) if status==200 else None
                if status==200 and stored_trace_span_ids(decoded)=={self.root_span_id,self.child_span_id}:pending.remove(tid)
                attempts.append({'trace_id':tid,'status':status,'accepted_complete_fixture':tid not in pending})
                readonly._private(self.work/'source-readiness.json',{'trace_readiness_attempts':attempts,'acceptance_window_seconds':45,'ingestion_retried':False})
                if time.monotonic()>deadline:raise Blocked('real Tempo storage responded after the 45s acceptance window; ingestion not retried')
            if not pending:break
            if time.monotonic()>=deadline:raise Blocked('real Tempo trace storage did not become complete within 45s; ingestion not retried')
            time.sleep(1)
        # Confirm exact scoped log ingestion independently of the product DTO.
        selector='{'+','.join(key+'='+json.dumps(value) for key,value in self.telemetry_config()['applications'][self.resource]['loki_labels'].items())+'}'
        query=urlencode({'query':selector,'start':str(fixture['generated_at_unix_ns']-5_000_000_000),'end':str(time.time_ns()),'limit':10})
        status,raw=component_request('loki','GET','/loki/api/v1/query_range?'+query,self.data['component_identities']['loki'])
        rows=[line for stream in json.loads(raw).get('data',{}).get('result',[]) for line in stream['values']]
        check(status==200 and len(rows)==3 and all(json.loads(line[1])['marker']==self.nonce for line in rows),'exact real Loki readback failed')
        readonly._private(self.work/'source-readback.json',{'trace_readiness_attempts':attempts,'visible_log_rows':len(rows),'loki_writes':1,'otel_writes':1,'records_retained':self.state['source_records_retained']})
        self.case_marker('observe.real_ingest',{'source':'owned real Loki and OTLP→OTel→Tempo','controlled_log_records':4,'controlled_closed_traces':2,'real_readback':True,'source_data_file':'source-data.json','historical_window_proof':False})
    def observations(self):
        self.call('reader','GET',self.logs_path,validate=lambda v:validate_logs(v,self.nonce,self.app))
        self.case_marker('observe.http_logs',{'normal_route':'GET /api/v1/logs','nonempty_current_rows':3,'complete_counts_patterns_and_histogram':True,'scope':self.resource,'lookback_config_seconds':86400,'historical_population_verified':False})
        self.call('reader','GET',self.trace_path,index=1,validate=lambda v:validate_trace(v,self))
        self.case_marker('observe.http_trace_detail',{'normal_route':'GET /api/v1/traces/{id}','trace_id':self.trace_id,'complete_stored_spans':2,'otel_collector_ingestion':True,'uninstrumented_work_verified':False,'search_completeness_verified':False})
    def authorization(self):
        before=self.relay.counts()
        for path in (self.logs_path,self.trace_path):self.call(None,'GET',path,want=401,code='unauthorized',scenario='authorization')
        self.call('denied','GET',self.logs_path,want=403,code='forbidden',scenario='authorization')
        self.call('denied','GET',self.trace_path,want=404,code='not_found',scenario='authorization')
        self.call('reader','GET',self.logs_path,environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('reader','GET',self.trace_path,environment='staging',want=403,code='forbidden',scenario='authorization')
        check(self.relay.counts()==before,'permission rejection contacted telemetry source')
        self.call('reader','GET','/api/v1/traces/'+self.hidden_trace_id+'?'+urlencode({'service':self.app}),want=404,code='not_found',scenario='authorization')
        self.case_marker('observe.authorization',{'foreign_trace_id_denied':True,'unauthenticated':401,'logs_without_grant':403,'trace_without_grant':404,'rejected_auth_source_requests':0})
    def boundaries(self):
        for suffix,code in (('&levels=debug','invalid_levels'),('&provider=tempo','invalid_provider'),('&q='+('x'*257),'invalid_query')):
            self.call('reader','GET',self.logs_path+suffix,want=422,code=code,scenario='boundary')
        self.call('reader','GET',self.logs_path+'&levels=',scenario='boundary',validate=lambda v:check(v=={'total':'0','errors':'0','histogram':[],'items':[],'patterns':[]},'explicit empty level selection queried/alluded to full levels'))
        self.call('reader','GET','/api/v1/traces/nothex?'+urlencode({'service':self.app}),want=422,code='invalid_trace',scenario='boundary')
        self.call('reader','GET','/api/v1/traces?'+urlencode({'service':self.app}),want=503,code='not_configured',scenario='failure')
        emit('observe.trace_search_completeness','blocked',{'normal_route':'GET /api/v1/traces','reason':'Owned Tempo live-store lag search fail-closed behavior is unverified; flag stays false','priority':'HIGH','trace_detail_pass_does_not_close_list':True})
        self.case_marker('observe.boundaries',{'invalid_level/provider/query/trace_rejected':True,'empty_level_queried_none':True,'trace_list_honest_not_configured':True})
    def failure_recovery(self):
        for provider,path,code,validator in (('loki',self.logs_path,'telemetry_unavailable',lambda v:validate_logs(v,self.nonce,self.app)),('tempo',self.trace_path,'telemetry_unavailable',lambda v:validate_trace(v,self))):
            self.relay.fail(provider,True)
            try:self.call('reader','GET',path,want=503,code=code,scenario='failure')
            finally:self.relay.fail(provider,False)
            self.call('reader','GET',path,index=1,validate=validator)
        check(all(self.relay.counts()['injected'][provider]>=1 for provider in ('loki','tempo')),'explicit fault window did not reach source proxy')
        self.case_marker('observe.failure_recovery',{'fault_source':'own fixed-path transparent source relay503; actual components not stopped','actual_source_reads_after_restore':True,'fault_counters':self.relay.counts()['injected']})
    def revocation(self):
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+readonly.quote(self.roles['reader'])+" AND module IN ('logs','traces') AND action='view' AND scope_kind='environment' AND scope_id='test';")
        before=self.relay.counts()
        self.call('reader','GET',self.logs_path,want=403,code='forbidden',scenario='authorization',index=1)
        self.call('reader','GET',self.trace_path,want=404,code='not_found',scenario='authorization')
        check(self.relay.counts()==before,'revoked grant still contacted/read source')
        self.case_marker('observe.revocation',{'cross_instance_live_grant_revoked':True,'source_requests_after_revoke':0,'concurrent_slow_response_revocation_verified':False})
    def audit_consistency(self):
        ids=[self.services[env]['service_environment_id'] for env in ('test','staging')]
        rows=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('action',action,'application',application_id,'target',target,'actor',actor_id,'outcome',outcome,'changes',changes)),'[]'::json) FROM management_audit",read=True))
        admin=self.sql('SELECT id FROM users WHERE email='+readonly.quote(self.admin_email),read=True)
        selected=[r for r in rows if r['action'] in ('v3.service.created','v3.service_mapping.saved')]
        check(len(rows)==4 and len(selected)==4 and all(r['actor']==admin and r['outcome']=='success' for r in selected) and
              {r['target'] for r in selected if r['action']=='v3.service_mapping.saved'}==set(ids),
              'read-only observation/rejection added audit or HTTP producer actor/target count differs')
        proof=json.loads(self.sql("SELECT json_agg(json_build_object('id',id,'revision',binding_revision,'bindings',bindings)) FROM application_environments WHERE id=ANY(ARRAY["+','.join(readonly.quote(x) for x in ids)+'])',read=True))
        check(len(proof)==2 and {r['id'] for r in proof}==set(ids),'HTTP mapping state readback is incomplete')
        for env,resource in (('test',self.resource),('staging',self.hidden_resource)):
            initial=self.services[env];expected=[dict(kind=provider,connector_id=provider,resource=resource) for provider in ('loki','tempo')]
            saved=[r for r in proof if r['id']==initial['service_environment_id']][0]
            check(saved['revision']==initial['binding_revision']+1 and saved['bindings']==expected,'observations changed exact binding state')
            created=[r for r in rows if r['action']=='v3.service.created' and r['target']==initial['service_id']]
            mappings=[r for r in rows if r['action']=='v3.service_mapping.saved' and r['target']==initial['service_environment_id']]
            check(len(created)==len(mappings)==1 and all(r['application']==initial['service_id'] and r['changes']['environment']==env for r in created+mappings),'HTTP producer audit application/environment differs')
            changes=mappings[0]['changes']
            check(changes['binding_revision_before']==initial['binding_revision'] and changes['binding_revision_after']==saved['revision'] and changes['bindings_after']==saved['bindings'],'audit mapping revision/exact bindings differ from persisted state')
        self.case_marker('observe.audit_consistency',{'http_audit_rows':len(rows),'producer_actor':admin,'source_mapping_targets':ids,'observation_business_or_audit_writes':0,'sql_business_result_writes':0})
    def run(self):
        self.relay=Relay(self.data['component_identities'])
        readonly.private_file(self.work/'telemetry.json',json.dumps(self.telemetry_config()))
        self.config_digest=hash_file(self.work/'telemetry.json')
        self.provision();self.start();self.seed()
        for user in ('admin','reader','hidden','denied'):self.login(user)
        for step in (self.source_scope,self.real_ingest,self.observations,self.authorization,self.boundaries,self.failure_recovery,self.revocation,self.audit_consistency):self.frozen();step()
        self.frozen()
        rows=[json.loads(line) for line in (self.work/'interfaces.jsonl').read_text().splitlines()]
        positive=sorted({(r['method'],r['path']) for r in rows if r.get('registration_evidence') and (r['method'],r['path'].removeprefix('/api/v1')) in TARGETS})
        check(set(positive)=={(method,'/api/v1'+path) for method,path in TARGETS},'two bounded normal routes lack registered exact HTTP evidence')
        result=dict(suite=MODULE,run_id=self.run_id,http_results=self.calls,positive_routes=positive,source_tree_sha=self.data['source_tree_sha'],binary_sha256=self.data['binary_sha256'],component_identities=self.data['component_identities'],source_config_sha256=self.config_digest,helper_sha256=self.data['helper_sha256'],source_data='synthetic current telemetry through real Loki/OTel/Tempo ingestion',records_retained=self.state['source_records_retained'],formal_performance_acceptance=False,formal_24h_acceptance=False,unverified=['GET /traces search completeness under live-store lag','old historical/24h windows','uninstrumented work','concurrent slow read grant/session revoke','real tenant authentication'])
        readonly._private(self.work/'observe-result.json',result);return result
    def cleanup(self):
        first=None
        try:super().cleanup()
        except BaseException as error:first=error
        if self.relay:
            try:
                self.relay.close();readonly._private(self.work/'relay-cleanup.json',{'port':RELAY_PORT,'stopped':True,'shared_source_records_deleted':False})
                emit('observe.relay_cleanup','passed',{'port':RELAY_PORT,'source_components_stopped':False,'source_records_retained':self.state['source_records_retained']})
            except BaseException as error:
                if first is None:first=error
                else:
                    try:readonly._private(self.work/'relay-cleanup-error.json',{'error_type':type(error).__name__})
                    except BaseException:pass
        if first is not None:raise first


def stored_trace_span_ids(data):
    import base64
    result=set()
    if not isinstance(data,dict):return result
    for resource in data.get('resourceSpans',[])+data.get('batches',[]):
        for scope in resource.get('scopeSpans',[])+resource.get('instrumentationLibrarySpans',[]):
            for span in scope.get('spans',[]):
                value=span.get('spanId','')
                if re.fullmatch(r'[a-f0-9]{16}',value):result.add(value)
                else:
                    try:
                        raw=base64.b64decode(value,validate=True)
                        if len(raw)==8:result.add(raw.hex())
                    except (ValueError,TypeError):pass
    return result


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT)
    mode=parser.add_mutually_exclusive_group(required=True);mode.add_argument('--inventory',action='store_true');mode.add_argument('--live',action='store_true');args=parser.parse_args(argv)
    if args.inventory:
        print(json.dumps(dict(module=MODULE,normal_targets=['GET /api/v1/logs','GET /api/v1/traces/{id}'],blocked_normal_targets=['GET /api/v1/traces'],required_cases=REQUIRED_CASES,ports=[*readonly.PORTS,RELAY_PORT],components=COMPONENTS,offline_only=True,trace_search_fail_closed=False),ensure_ascii=False));return 0
    original=readonly.emit;readonly.emit=emit
    try:
        with readonly.exclusive(args.root),readonly.cooperative_signals():
            data=doctor(args.root);emit('observe.doctor','passed',dict(component_identities=data['component_identities'],helper_sha256=data['helper_sha256'],source_tree_sha=data['source_tree_sha'],postgres_container_id=PG_CID))
            readonly.execute(Chain(args.root,data,str(uuid.uuid4())))
    except Blocked as error:emit('observe.environment','blocked',{'reason':str(error)});return 2
    except BaseException as error:emit('observe.failure','interrupted' if isinstance(error,(KeyboardInterrupt,SystemExit)) else 'failed',{'error_type':type(error).__name__});return 1
    finally:readonly.emit=original
    return 0


if __name__=='__main__':raise SystemExit(main())
