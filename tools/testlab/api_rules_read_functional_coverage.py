"""Real isolated Prometheus rule reads and immediate sampled historical dry-run.

Shared Prometheus/Alertmanager are never mutated. Native HTTP creates the app;
a fresh nonce-labelled Prometheus scrapes a local exporter and evaluates rules.
Its config has no notification channel. Cold current samples do not prove six
hours of retained history, a continuous trial, GitOps, or production tenancy.
"""
from __future__ import annotations
import argparse,contextlib,datetime,hashlib,http.client,json,math,os,re,socket,stat,subprocess,threading,time,uuid
from decimal import Decimal,ROUND_HALF_UP
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs,urlencode,urlsplit
import api_readonly_chain as readonly

ROOT=readonly.ROOT;OWNER=readonly.OWNER;MODULE='formal-api-rules-read-functional'
PORTS=(18628,18629);BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
PROM_PORT=18630;EXPORT_PORT=18631;RELAY_PORT=18635
PROM_IMAGE='prom/prometheus:v3.13.3';PROM_IMAGE_ID='sha256:249c7fa94198524d75d26cae28169774e41627c2d0fc1b7104f2feec8d192d0f'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
FILE='/etc/prometheus/own-rules.json';GROUP='owned-rules'
TARGETS=(('GET','/alert-rules'),('GET','/alert-rules/{id}'),('POST','/alert-rules/{id}/dry-run'))
SUPPORT=(('GET','/alert-rules/observed'),('GET','/alert-rules/observed/{id}'),('POST','/services'),('POST','/auth/login'))
REQUIRED_CASES=('rules.source_scope','rules.real_samples','rules.http_list_detail','rules.instant_dryrun','rules.authorization','rules.boundaries','rules.failure_recovery','rules.revocation','rules.audit_consistency')
Blocked,check=readonly.Blocked,readonly.check

def emit(name,status,actual,**fields):
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)

def hashes():
    return {f:hashlib.sha256((ROOT/f).read_bytes()).hexdigest() for f in ('tools/testlab/api_rules_read_functional_coverage.py','tools/testlab/api_readonly_chain.py')}

@contextlib.contextmanager
def api_ports():
    old=readonly.PORTS,readonly.BASES
    readonly.PORTS,readonly.BASES=PORTS,BASES
    try:yield
    finally:readonly.PORTS,readonly.BASES=old

def all_ports_free():
    for port in (*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT):
        with socket.socket() as sock:
            sock.settimeout(.3)
            if sock.connect_ex(('127.0.0.1',port))==0:raise Blocked('dedicated rules listener occupied: '+str(port))

def doctor(root):
    all_ports_free()
    with api_ports():data=readonly.doctor(root)
    if readonly.inspect_pg(data['postgres_container'])['Id']!=PG_CID:raise Blocked('owned PG exact CID drift')
    image=json.loads(subprocess.check_output(['docker','image','inspect',PROM_IMAGE],text=True,timeout=10))
    if len(image)!=1 or image[0]['Id']!=PROM_IMAGE_ID:raise Blocked('fixed locally available Prometheus image mismatch; no pull/adoption')
    return dict(data,helper_sha256=hashes())

class Contract(readonly.Contract):
    def canonical(self,method,path):
        p=urlsplit(path)
        if p.scheme or p.netloc or p.fragment or not p.path.startswith('/api/v1/'):raise Blocked('formal namespace required')
        short=p.path[len('/api/v1'):]
        options=[r for m,r in (*TARGETS,*SUPPORT) if m==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(r)),short)]
        route=short if short in options else options[0] if len(options)==1 else None
        if route is None or method.lower() not in self.api['paths'].get(route,{}):raise Blocked('outside rule-read bounded contract')
        return '/api/v1'+route,'main' if (method,route) in self.main else 'auxiliary_or_historical'

def rule_config(app,nonce):
    if not re.fullmatch('[a-f0-9]{32}',app) or not re.fullmatch('[a-f0-9]{32}',nonce):raise Blocked('actual HTTP UUID app and invocation nonce required')
    expr='testlab_rules_fixture_total{application="'+app+'",environment="test",fixture="'+nonce+'"} > 0'
    return {'groups':[{'name':GROUP,'interval':'15s','rules':[{'alert':'OwnedRule'+nonce,'expr':expr,'for':'0s','labels':{'application':app,'environment':'test','severity':'P3'},'annotations':{'summary':'Controlled current rule '+nonce}}]}]}

def prometheus_config():
    return {'global':{'scrape_interval':'2s','evaluation_interval':'15s'},'rule_files':[FILE],
            'scrape_configs':[{'job_name':'owned-business','static_configs':[{'targets':['host.docker.internal:'+str(EXPORT_PORT)]}]}]}

def guard_owned_prom(info,chain,expect_running=True):
    if (not isinstance(info,dict) or info.get('Name')!='/'+chain.prom_name or info.get('Image')!=PROM_IMAGE_ID or
        any(info.get('Config',{}).get('Labels',{}).get(k)!=v for k,v in {'aiops.testlab.owner':OWNER,'aiops.testlab.run':chain.run_id,'aiops.testlab.purpose':'rule-read'}.items()) or
        not re.fullmatch('[a-f0-9]{64}',str(info.get('Id',''))) or
        info.get('HostConfig',{}).get('NanoCpus')!=1000000000 or info.get('HostConfig',{}).get('Memory')!=536870912 or
        info.get('HostConfig',{}).get('Privileged') is not False or
        info.get('HostConfig',{}).get('ReadonlyRootfs') is not True or
        (info.get('NetworkSettings',{}).get('Ports',{}) if info.get('State',{}).get('Running') else info.get('HostConfig',{}).get('PortBindings',{})).get('9090/tcp')!=[{'HostIp':'127.0.0.1','HostPort':str(PROM_PORT)}] or
        (expect_running and not info.get('State',{}).get('Running'))):raise Blocked('owned Prometheus identity/resources/listener drift')
    expected={(str(chain.work/'prometheus.json'),'/etc/prometheus/prometheus.yml'),(str(chain.work/'rules.json'),FILE)}
    binds={(m.get('Source'),m.get('Destination')) for m in info.get('Mounts',[]) if m.get('Type')=='bind' and m.get('RW') is False}
    if binds!=expected or any(m.get('RW') is True and m.get('Type')=='bind' for m in info.get('Mounts',[])):raise Blocked('owned Prometheus exact readonly mounts drift')
    if chain.prom_cid is not None and info['Id']!=chain.prom_cid:raise Blocked('owned Prometheus exact CID changed')
    return info['Id']

def direct_get(path):
    p=urlsplit(path)
    if p.scheme or p.netloc or p.fragment or p.path not in ('/-/ready','/api/v1/rules','/api/v1/query_range'):raise Blocked('Prometheus transport outside readonly allowlist')
    conn=http.client.HTTPConnection('127.0.0.1',PROM_PORT,timeout=5)
    try:
        conn.request('GET',path);r=conn.getresponse();raw=r.read((2<<20)+1)
        if len(raw)>2<<20:raise Blocked('actual source evidence exceeds budget')
        return r.status,raw
    finally:conn.close()

class LocalServer:
    def __init__(self,port,handler):
        self.server=ThreadingHTTPServer(('127.0.0.1',port),handler);self.server.daemon_threads=True
        self.thread=threading.Thread(target=self.server.serve_forever,kwargs={'poll_interval':.1},daemon=True);self.thread.start()
    def close(self):
        self.server.shutdown();self.server.server_close();self.thread.join(timeout=3)
        check(not self.thread.is_alive() and self.server.socket.fileno()==-1,'owned listener did not close')

class Source:
    def __init__(self,chain):
        self.chain=chain;self.lock=threading.Lock();self.requests=[];self.injected=0;self.failed=None;self.scrapes=0;self.last_range=None
        source=self
        class Exporter(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                if self.path!='/metrics':self.send_error(404);return
                with source.lock:source.scrapes+=1;count=source.scrapes
                body=('# TYPE testlab_rules_fixture_total counter\ntestlab_rules_fixture_total{application="'+chain.app+'",environment="test",fixture="'+chain.nonce+'"} '+str(count)+'\n').encode()
                self.send_response(200);self.send_header('Content-Type','text/plain; version=0.0.4');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
        class Relay(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    provider_path=source.validate(self.path)
                    if self.headers.get('Authorization') or self.headers.get('PRIVATE-TOKEN'):raise Blocked('no credential forwarding')
                    chain.guard_prom()
                    with source.lock:failed=source.failed==urlsplit(provider_path).path
                    if failed:
                        status,body=503,b'{"status":"error","error":"Explicit owned source relay failure"}'
                        with source.lock:source.injected+=1
                    else:status,body=direct_get(provider_path)
                    row={'path':provider_path,'http_status':status,'response':json.loads(body) if status==200 else None,'synthetic_current_source':True}
                    with source.lock:
                        source.requests.append(row)
                        if status==200 and urlsplit(provider_path).path=='/api/v1/query_range':source.last_range=row
                    readonly.append_json(chain.work/'source-reads.jsonl',row)
                except (Blocked,OSError,http.client.HTTPException):status,body=502,b'{"status":"error","error":"Owned source unavailable"}'
                self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.send_header('Connection','close');self.end_headers();self.wfile.write(body)
            def do_POST(self):self.send_error(405)
        self.exporter=LocalServer(EXPORT_PORT,Exporter)
        try:self.relay=LocalServer(RELAY_PORT,Relay)
        except BaseException:self.exporter.close();raise
    def validate(self,path):
        p=urlsplit(path);q=parse_qs(p.query,keep_blank_values=True)
        if p.scheme or p.netloc or p.fragment or len(path)>32768 or any(ord(x)<32 for x in path) or any(len(v)!=1 for v in q.values()):raise Blocked('unsafe source request')
        if p.path=='/api/v1/rules' and q=={'type':['alert']}:return path
        if p.path=='/api/v1/query_range' and set(q)=={'query','start','end','step','limit','timeout'} and q['query']==[self.chain.expr] and q['step']==['15'] and q['limit']==['101'] and q['timeout']==['15s']:
            start,end=Decimal(q['start'][0]),Decimal(q['end'][0])
            if start.is_finite() and end.is_finite() and Decimal(21600)<=end-start<=Decimal(21630) and end<=Decimal(str(time.time()+15)):return path
        raise Blocked('source relay only permits exact configured rule query and six-hour bounded sample contract')
    def count(self):
        with self.lock:return len(self.requests)
    def close(self):
        failure=None
        for item in (self.relay,self.exporter):
            try:item.close()
            except BaseException as e:failure=failure or e
        if failure:raise failure

def validate_observed(value,app,nonce):
    expected=rule_config(app,nonce)['groups'][0]['rules'][0]
    groups=value.get('data',{}).get('groups',[]) if value.get('status')=='success' else []
    check(len(groups)==1 and groups[0].get('file')==FILE and groups[0].get('name')==GROUP and groups[0].get('interval')==15,'actual loaded group/file/interval differs')
    rules=groups[0].get('rules',[])
    check(len(rules)==1,'actual rule count differs');rule=rules[0]
    check(rule.get('type')=='alerting' and rule.get('name')==expected['alert'] and rule.get('query')==expected['expr'] and rule.get('labels')==expected['labels'] and rule.get('annotations')==expected['annotations'] and rule.get('duration')==0,'actual rule/query/labels/annotations differ')
    check(rule.get('health')=='ok' and rule.get('state')=='firing' and bool(rule.get('lastEvaluation')),'actual rule is not yet evaluated/firing')
    return rule

def iso_at(value):
    value=Decimal(value);whole=int(value);fraction=int((value-whole)*1000000000)
    base=datetime.datetime.fromtimestamp(whole,datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%S')
    return base+('.'+str(fraction).zfill(9).rstrip('0') if fraction else '')+'Z'

def current_windows(row,app,nonce):
    q=parse_qs(urlsplit(row['path']).query);start=Decimal(q['start'][0]);end=Decimal(q['end'][0]);step=Decimal(15);window_start=end-21600
    body=row['response'];check(body.get('status')=='success' and not body.get('warnings') and not body.get('infos'),'actual range query not complete protocol success')
    data=body.get('data',{});check(data.get('resultType')=='matrix','actual query response is not matrix');series=data.get('result',[])
    check(len(series)==1,'controlled current real range must have one nonempty series');metric=series[0]['metric']
    check(metric.get('application')==app and metric.get('environment')=='test' and metric.get('fixture')==nonce,'range series escaped exact scope')
    values=series[0]['values'];check(2<=len(values)<=2000,'two natural current samples required; not synthetic historical data')
    stamps=[]
    for at,val in values:
        check(math.isfinite(float(val)) and float(val)>0,'real counter sample not finite/positive');at=Decimal(str(at))
        aligned=start+((at-start)/step).to_integral_value(rounding=ROUND_HALF_UP)*step
        check(abs(aligned-at)<=Decimal('.05') and start<=aligned<=end and (not stamps or aligned>stamps[-1]),'actual sample off-grid/out of order')
        stamps.append(aligned)
    windows=[];runs=[]
    for at in stamps:
        if runs and at-runs[-1][-1]==step:runs[-1].append(at)
        else:runs.append([at])
    for run in runs:
        if run[0]>=window_start:windows.append(iso_at(run[0])+'/'+iso_at(run[-1]))
    return sorted(windows),{'actual_samples':len(values),'first_actual_sample':iso_at(stamps[0]),'last_actual_sample':iso_at(stamps[-1]),'sampled_seconds':str(stamps[-1]-stamps[0]),'six_hours_history_proven':False,'continuous_trial_proven':False}

class Chain(readonly.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id);self.contract=Contract(root);self.source=None;self.app=None;self.rule_id=None;self.expr=None;self.rule_source_config=None;self.config_hashes={};self.prom_cid=None;self.prom_attempted=False
        self.prom_name='aiops-'+OWNER+'-rules-'+self.nonce
        self.state.update(suite=MODULE,ports=[*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT],helper_sha256=data['helper_sha256'],source_type='invocation-owned actual Prometheus/exposed synthetic current counter; no Alertmanager/no history claim')
        readonly._private(self.work/'state.json',self.state)
    def case_marker(self,name,data):
        self.frozen();emit(name,'passed',dict(run_id=self.run_id,database=self.database,source_tree_sha=self.data['source_tree_sha'],**data))
    def pg(self,*args,**kwargs):
        deadline=getattr(self,'cleanup_deadline',None)
        if deadline is not None:
            remaining=deadline-time.monotonic()
            if remaining<=0:raise Blocked('rules aggregate cleanup budget exhausted; resources retained')
            kwargs['deadline']=min(kwargs.get('deadline',deadline),deadline)
        else:remaining=3
        if readonly.inspect_pg(self.data['postgres_container'],timeout=min(3,remaining))['Id']!=PG_CID:raise Blocked('exact PG CID changed')
        return super().pg(*args,**kwargs)
    def frozen(self):
        if hashes()!=self.data['helper_sha256']:raise Blocked('rules helper/reused helper changed during run')
        for name,want in self.config_hashes.items():
            f=self.work/name
            if f.is_symlink() or stat.S_IMODE(f.stat().st_mode)!=0o600 or hashlib.sha256(f.read_bytes()).hexdigest()!=want:raise Blocked('owned source config changed')
        if self.prom_cid:self.guard_prom()
        super().frozen()
    def guard_prom(self):return guard_owned_prom(readonly.inspect_pg(self.prom_name),self)
    def startup_environment(self):
        return {'AIOPS_V3_ALERT_RULES_CONFIG':str(self.rule_source_config)} if self.rule_source_config else {}
    def start(self):
        generation=len(self.processes)//2+1
        original_emit=readonly.emit
        def generation_emit(name,status,actual,**fields):
            if name in ('readonly.api_ready.0','readonly.api_ready.1'):
                name='rules.api_ready.generation.'+str(generation)+'.instance.'+name.rsplit('.',1)[1]
            original_emit(name,status,actual,**fields)
        # Both acquired generations need separate immutable result keys. This
        # override exists only inside this helper process during inherited start.
        readonly.emit=generation_emit
        try:
            with api_ports():super().start()
        finally:readonly.emit=original_emit
    def call(self,*args,**kwargs):
        method,path=args[1:3];want=kwargs.get('want',200)
        if 200<=want<300 and kwargs.get('scenario','normal')=='normal' and path.startswith('/api/v1/alert-rules') and kwargs.get('validate') is None:raise Blocked('normal rule target requires exact business/source validator')
        with api_ports():return super().call(*args,**kwargs)
    def seed(self):
        super().seed()
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(readonly.quote(x) for x in (self.roles['reader'],'alert_rules','view','application',self.app))+');')
    def prepare_source(self):
        self.call('admin','POST','/api/v1/services',{'items':[{'id':'rules-'+self.nonce,'tier':'T1'}]},want=201,scenario='fixture',validate=lambda v:check(v=={'created':1},'native service producer differs'))
        self.app=self.sql("SELECT id FROM applications WHERE key="+readonly.quote('rules-'+self.nonce),read=True)
        check(re.fullmatch('[a-f0-9]{32}',self.app) is not None,'HTTP application producer readback differs')
        rules=rule_config(self.app,self.nonce);self.expr=rules['groups'][0]['rules'][0]['expr']
        source={'environments':{'test':{'url':'http://127.0.0.1:'+str(RELAY_PORT),'allow_http':True}},'bindings':[{'environment_id':'test','application_id':self.app,'file':FILE,'group':GROUP}]}
        for name,payload in (('rules.json',rules),('prometheus.json',prometheus_config()),('rule-source.json',source)):
            readonly._private(self.work/name,payload);self.config_hashes[name]=hashlib.sha256((self.work/name).read_bytes()).hexdigest()
        self.rule_source_config=self.work/'rule-source.json';self.source=Source(self)
        absence=subprocess.run(['docker','container','inspect',self.prom_name],capture_output=True,text=True,timeout=10)
        if absence.returncode==0 or ('No such container' not in absence.stderr and 'No such object' not in absence.stderr):raise Blocked('fresh own container name not proven absent; no adoption')
        self.prom_attempted=True
        args=['docker','run','--detach','--name',self.prom_name,'--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-read','--cpus','1','--memory','512m','--pids-limit','128','--user',str(os.getuid())+':'+str(os.getgid()),'--cap-drop','ALL','--security-opt','no-new-privileges','--read-only','--tmpfs','/prometheus:rw,noexec,nosuid,size=128m,mode=1777','--publish','127.0.0.1:'+str(PROM_PORT)+':9090']
        for name,destination in (('prometheus.json','/etc/prometheus/prometheus.yml'),('rules.json',FILE)):
            args+=['--mount','type=bind,src='+str(self.work/name)+',dst='+destination+',readonly']
        args+=[PROM_IMAGE_ID,'--config.file=/etc/prometheus/prometheus.yml','--storage.tsdb.retention.time=2h','--storage.tsdb.path=/prometheus']
        readonly._private(self.work/'prom-launch-argv.json',{'argv':args,'shared_components_modified':False})
        proc=subprocess.run(args,capture_output=True,text=True,timeout=30)
        readonly._private(self.work/'prom-launch-result.json',{'exit':proc.returncode,'stdout':proc.stdout,'stderr':proc.stderr})
        if proc.returncode:raise Blocked('owned Prometheus launch failed; no mutation retry')
        self.prom_cid=guard_owned_prom(readonly.inspect_pg(self.prom_name),self);check(proc.stdout.strip()==self.prom_cid,'launch exact CID differs')
        deadline=time.monotonic()+90;observations=[]
        while time.monotonic()<deadline:
            self.guard_prom()
            try:
                status,raw=direct_get('/api/v1/rules?type=alert');value=json.loads(raw) if status==200 else {}
                rule=validate_observed(value,self.app,self.nonce)
                observations.append({'at':time.time(),'evaluated_firing':True});break
            except (OSError,ValueError,AssertionError,http.client.HTTPException):observations.append({'at':time.time(),'evaluated_firing':False});time.sleep(1)
        else:raise Blocked('natural real Prometheus scrape/evaluation not ready in90s; not retried/reclocked')
        readonly._private(self.work/'prom-loaded-rule.json',value);readonly._private(self.work/'prom-evaluation-readiness.json',observations)
        self.restart_with_source()
        self.case_marker('rules.source_scope',{'application_uuid':self.app,'producer':'POST /api/v1/services','file':FILE,'group':GROUP,'interval_seconds':15,'container_id':self.prom_cid,'image_id':PROM_IMAGE_ID,'no_alertmanager_or_notifications':True,'old_shared_source_proven':False})
        # The first natural evaluation can precede2samples; wait one actual interval.
        time.sleep(16)
        self.case_marker('rules.real_samples',{'natural_wait_seconds':16,'exporter_scrapes':self.source.scrapes,'data':'actual current counter scraped/evaluated by own Prom; no generated old timestamps','six_hour_retention_proof':False})
    def restart_with_source(self):
        if len(self.processes)!=2:raise Blocked('source binding restart requires only acquired first-generation handles')
        for held in self.processes:
            if held.poll() is None:held.terminate();held.wait(timeout=10)
        receipt=[]
        for index in (0,1):
            old=self.work/('api-'+str(index)+'.log');archive=self.work/('api-bootstrap-'+str(index)+'.log')
            if old.is_symlink() or not old.is_file() or stat.S_IMODE(old.stat().st_mode)!=0o600 or archive.exists():raise Blocked('exact owned first-generation log cannot be archived')
            receipt.append({'pid_snapshot':self.processes[index].pid,'exit':self.processes[index].poll(),'source_log':old.name,'archived_log':archive.name,'sha256':hashlib.sha256(old.read_bytes()).hexdigest()})
            old.rename(archive)
        readonly._private(self.work/'bootstrap-generation-exits.json',receipt)
        self.start()
    def list_detail(self):
        _,raw=direct_get('/api/v1/rules?type=alert');rule=validate_observed(json.loads(raw),self.app,self.nonce)
        identity='test\0'+self.app+'\0'+FILE+'\0'+GROUP+'\0'+rule['name'];self.rule_id='prom-'+hashlib.sha256(identity.encode()).hexdigest()[:24]
        self.detail='/api/v1/alert-rules/'+self.rule_id;self.dry=self.detail+'/dry-run'
        def validate_list(v):check(v.get('counts')=={'all':1,'firing':1,'scheduled':0} and v.get('items')==[{'id':self.rule_id,'folder':GROUP,'name':rule['name'],'state':'firing'}],'exact native summary/counts differ')
        self.call('reader','GET','/api/v1/alert-rules',validate=validate_list)
        self.call('reader','GET',self.detail,index=1,validate=lambda v:check(v['id']==self.rule_id and v['application_id']==self.app and v['environment_id']=='test' and v['expr']==self.expr and v['for_seconds']==0 and v['evaluation_interval_seconds']==15 and v['partial'] is True and {x['k']:x['v'] for x in v['labels']}==rule['labels'] and v['annotations']==rule['annotations'] and all(v[k] is None for k in ('preview','condition','evaluation','runbook','route_path','ai_triage')),'exact source detail/unknown fields differ'))
        self.call('reader','GET','/api/v1/alert-rules/observed',validate=lambda v:check(len(v['items'])==1 and v['items'][0]['id']==self.rule_id and v['items'][0]['application_id']==self.app and v['partial'] is False,'observed auxiliary source scope differs'))
        self.case_marker('rules.http_list_detail',{'normal_main':['GET /api/v1/alert-rules','GET /api/v1/alert-rules/{id}'],'observed_auxiliary_separate':True,'nonempty_rule':self.rule_id})
    def instant(self,mark=True):
        def validate(v):
            check(self.source.last_range is not None,'actual range request evidence absent')
            windows,proof=current_windows(self.source.last_range,self.app,self.nonce)
            check(v=={'would_fire':len(windows),'windows':windows} and len(windows)>=1,'native dry-run windows differ from exact actual Prometheus samples')
            readonly._private(self.work/'instant-real-sample-proof.json',dict(proof,expected_response=v,source_read=self.source.last_range))
        self.call('reader','POST',self.dry,{'expr':self.expr,'for':'0s'},validate=validate)
        if mark:self.case_marker('rules.instant_dryrun',{'normal_main':'POST /api/v1/alert-rules/{id}/dry-run','current_real_samples':True,'continuous_24h_trial':False,'six_hours_complete_history':False,'query_response_rewritten':False})
    def authorization(self):
        before=self.source.count()
        for method,path,body in (('GET','/api/v1/alert-rules',None),('GET',self.detail,None),('POST',self.dry,{'expr':self.expr,'for':'0s'})):
            self.call(None,method,path,body,want=401,code='unauthorized',scenario='authorization')
            self.call('denied',method,path,body,want=403,code='forbidden',scenario='authorization')
            self.call('reader',method,path,body,environment='staging',want=403,code='forbidden',scenario='authorization')
        check(self.source.count()==before,'unauthorized/cross-environment request reached source')
        self.call('reader','GET','/api/v1/alert-rules/prom-'+('0'*24),want=404,code='not_found',scenario='authorization')
        self.case_marker('rules.authorization',{'unauthenticated':401,'missing_grant':403,'foreign_env':403,'foreign_object':404,'rejected_scope_source_calls':0})
    def boundaries(self):
        for body,code in (({'expr':self.expr+' ','for':'0s'},'expression_mismatch'),({'expr':self.expr,'for':'2h'},'invalid_dry_run'),({'expr':self.expr,'for':'-1s'},'invalid_dry_run'),({'expr':'','for':'0s'},'invalid_dry_run')):
            self.call('reader','POST',self.dry,body,want=422,code=code,scenario='boundary')
        self.case_marker('rules.boundaries',{'no_arbitrary_promql':True,'duration_bounds':True,'changes_not_saved':True})
    def failure_recovery(self):
        for source_path,target,body,code in (('/api/v1/rules','/api/v1/alert-rules',None,'alert_rules_unavailable'),('/api/v1/query_range',self.dry,{'expr':self.expr,'for':'0s'},'rule_query_unavailable')):
            self.source.failed=source_path
            try:self.call('reader','GET' if body is None else 'POST',target,body,want=503,code=code,scenario='failure')
            finally:self.source.failed=None
            if body is not None:self.instant(mark=False)
            else:self.call('reader','GET',target,index=1,validate=lambda v:check(v['counts']=={'all':1,'firing':1,'scheduled':0},'source restoration differs'))
        check(self.source.injected>=2,'failure windows did not reach actual read relay')
        self.case_marker('rules.failure_recovery',{'explicit_relay_503':self.source.injected,'real_source_after_recovery':True,'source_or_config_not_modified':True})
    def audit(self):
        rows=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('action',action,'target',target,'application',application_id,'outcome',outcome)),'[]') FROM management_audit",read=True))
        check(len(rows)==1 and rows[0]['action']=='v3.service.created' and rows[0]['target']==self.app and rows[0]['outcome']=='success','rule reads/dryrun/rejections created fake audit or native app audit differs')
        self.case_marker('rules.audit_consistency',{'native_service_audit':1,'rule_read_or_dryrun_business_writes':0,'rule_written_via_sql':False})
    def revoke(self):
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+readonly.quote(self.roles['reader'])+" AND module='alert_rules' AND action='view';")
        before=self.source.count()
        self.call('reader','GET',self.detail,index=1,want=403,code='forbidden',scenario='authorization')
        self.call('reader','POST',self.dry,{'expr':self.expr,'for':'0s'},want=403,code='forbidden',scenario='authorization')
        check(self.source.count()==before,'revoked actor contacted source')
        self.case_marker('rules.revocation',{'both_api_instances':True,'live_grant_revoked':True,'source_reads_after_revoke':0})
    def run(self):
        self.provision();self.start();self.login('admin');self.prepare_source();self.seed()
        for name in ('reader','denied'):self.login(name)
        self.list_detail();self.instant();self.authorization();self.boundaries();self.failure_recovery();self.audit();self.revoke();self.frozen()
        return {'normal_main_routes':3,'own_source':'actual per-run Prometheus','shared_components_untouched':True,'continuous_soak_or_old_history_passed':False}
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40
        errors=[]
        # Stop acquired children first, leaving a single aggregate cleanup budget
        # for the source and PostgreSQL. Saved PID snapshots are never signaled.
        for held in reversed(self.processes):
            try:
                if held.poll() is None:
                    held.terminate()
                    try:held.wait(timeout=3)
                    except subprocess.TimeoutExpired:held.kill();held.wait(timeout=2)
            except BaseException as e:errors.append(e)
        if self.prom_attempted:
            try:
                info=readonly.inspect_pg(self.prom_name,timeout=max(.1,min(3,self.cleanup_deadline-time.monotonic())))
                cid=guard_owned_prom(info,self,expect_running=False)
                if info['State']['Running']:
                    p=subprocess.run(['docker','stop','--time','3',cid],capture_output=True,text=True,timeout=max(.1,min(8,self.cleanup_deadline-time.monotonic())))
                    if p.returncode:raise Blocked('owned Prom stop failed; no retry')
                info=readonly.inspect_pg(self.prom_name,timeout=max(.1,min(3,self.cleanup_deadline-time.monotonic())));guard_owned_prom(info,self,expect_running=False)
                if info['State']['Running']:raise Blocked('owned Prom still running')
                p=subprocess.run(['docker','rm',cid],capture_output=True,text=True,timeout=max(.1,min(6,self.cleanup_deadline-time.monotonic())))
                if p.returncode:raise Blocked('owned Prom removal failed; no retry')
                absence=subprocess.run(['docker','container','inspect',self.prom_name],capture_output=True,text=True,timeout=3)
                if absence.returncode==0 or ('No such container' not in absence.stderr and 'No such object' not in absence.stderr):raise Blocked('exact owned Prom absence cannot be verified')
                readonly._private(self.work/'prom-cleanup.json',{'name':self.prom_name,'cid':cid,'exact_name_absent':True,'exact_cid_removed':True,'held_owner':OWNER,'run_id':self.run_id})
            except BaseException as e:errors.append(e)
        if self.source:
            try:self.source.close()
            except BaseException as e:errors.append(e)
        try:
            with api_ports():super().cleanup()
        except BaseException as e:errors.append(e)
        if errors:raise errors[0]
        for port in (*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT):
            with socket.socket() as sock:
                sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'owned rules port still occupied')
        emit('rules.cleanup','passed',{'run_id':self.run_id,'source_tree_sha':self.data['source_tree_sha'],'owned_container_removed':bool(self.prom_cid),'ports_absent':list((*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT)),'shared_prom_am_modified':False,'live_source_after_removal_required':False})

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--live',action='store_true');p.add_argument('--inventory',action='store_true');p.add_argument('--root',type=Path,default=ROOT);a=p.parse_args(argv)
    if a.inventory:
        print(json.dumps({'targets':TARGETS,'ports':[*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT],'offline_only':True,'new_source_required':True,'full_history_verified':False}));return 0
    if not a.live:p.error('explicit authorized --live required')
    try:
        lock=a.root/'.local/testlab/rules-read-functional.lock'
        with readonly.cooperative_signals():
            import fcntl
            fd=os.open(lock,os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
            try:
                fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);data=doctor(a.root);chain=Chain(a.root,data,str(uuid.uuid4()));emit('rules.doctor','passed',{'source_tree_sha':data['source_tree_sha'],'helper_sha256':data['helper_sha256'],'ports':[*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT]});readonly.execute(chain)
            finally:os.close(fd)
    except Blocked as e:emit('rules.environment','blocked',{'error':str(e)});return 2
    except Exception as e:emit('rules.failure','failed',{'error':str(e),'error_type':type(e).__name__});return 1
    return 0

if __name__=='__main__':raise SystemExit(main())
