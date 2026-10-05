"""One real, empty Prometheus preview; no fabricated history or model call.

The private Linux helper retains its exact CID/network/PG/process guards.
Only its fixture, bounded cleanup clock and explicit absence checks differ.
This proves the advertised empty subset, never seven-day nonempty coverage.
"""
from __future__ import annotations
import argparse,copy,hashlib,importlib.util,inspect,json,os,re,secrets,socket,sqlite3,subprocess,sys,threading,time,uuid
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from urllib.parse import parse_qs,urlsplit

ROOT=Path(__file__).resolve().parents[2]
MODULE='rule-preview-real-empty'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
LINUX_SHA='c80084496a5c068116bf7fec8d48443eebb71985a5fd23e4d44f8bb662ba0b19'
BUILD_PROOF=ROOT/'.local/testlab/rule-assistant-container/build/a4db352b-c427-453a-b22b-20b98cb8c066/build-proof.json'
PROOF_SHA='1ba253bd49a92a0825fe2265e71ff35769820227425eb14577c5574ddcd98676'
PORTS=(18780,18781);RELAY_PORT=18782;PROM_PORT=18783
ALL_PORTS=(*PORTS,RELAY_PORT,PROM_PORT)
TARGETS=(('POST','/alert-rules/preview'),)
REQUIRED_CASES=tuple('preview_empty.'+n for n in ('linux','source','tls','normal','authorization','consistency','cleanup'))

def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
ro=load('empty_preview_readonly',ROOT/'tools/testlab/api_readonly_chain.py')
saved={name:sys.modules.get(name) for name in ('api_readonly_chain','api_rule_generation_functional_coverage')}
try:
    sys.modules['api_readonly_chain']=ro
    gen=load('empty_preview_generation',ROOT/'tools/testlab/api_rule_generation_functional_coverage.py')
    sys.modules['api_rule_generation_functional_coverage']=gen
    linux=load('empty_preview_linux',ROOT/'tools/testlab/api_rule_assistant_container_coverage.py')
finally:
    for name,value in saved.items():
        if value is None:sys.modules.pop(name,None)
        else:sys.modules[name]=value
check,Blocked,q=ro.check,ro.Blocked,ro.quote
gen.SOURCE_SHA,gen.BINARY_SHA,gen.RUNTIME_SHA=SOURCE,BINARY,RUNTIME
gen.PROM_PORT=PROM_PORT
linux.SOURCE_SHA,linux.DARWIN_SHA,linux.RUNTIME_SHA=SOURCE,BINARY,RUNTIME
linux.PORTS,linux.BASES=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS)
linux.FIXTURE_PORT,linux.RESERVED_PORTS=RELAY_PORT,ALL_PORTS
linux.TARGETS=TARGETS
linux.NEW_MEMORY+=256<<20
original_resources=linux.resource_admission
def resource_admission(total,available,cpus,cpu_used,**kwargs):
    # The original guard reserves 2.5CPU; the additional Prometheus needs .5CPU.
    return original_resources(total,available,cpus-.5,cpu_used,**kwargs)
linux.resource_admission=resource_admission

def hashes():
    names=('api_rule_preview_real_empty_coverage.py','tests/test_api_rule_preview_real_empty_coverage.py',
        'api_rule_assistant_container_coverage.py','api_rule_generation_functional_coverage.py','api_readonly_chain.py',
        'runner.py','store.py','identity.py','faults.py','backend.py','env.py')
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in names}
linux.frozen_hashes=hashes

def evidence(row):
    return dict(row,**execution_binding(),evidence_mode='real_local',upstream_mode='real_local',test_name='FormalRulePreviewRealEmpty',
        proof_scope='actual Linux binary/private DNS+CA/owned Prometheus empty matrix; no nonempty historical completeness or rule quality')
def execution_binding():
    return dict(container_platform='linux/arm64',binary_sha256=LINUX_SHA,actual_linux_binary_sha256=LINUX_SHA,main_admission_binary_sha256=BINARY,
        source_tree_sha_start=SOURCE,source_tree_sha_end=SOURCE,linux_build_proof=str(BUILD_PROOF),linux_build_proof_sha256=PROOF_SHA)
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
linux.emit,linux.evidence=emit,evidence

def replace_function(function,replacements,namespace):
    """Exact reviewed private adaptation; fail closed when upstream text changes."""
    source=inspect.getsource(function)
    for old,new in replacements:
        if source.count(old)!=1:raise Blocked('private helper adaptation anchor changed')
        source=source.replace(old,new)
    # Preserve column-zero lines inside the original embedded forwarder script.
    # dedent would leave the method indented while those literal lines stay zero.
    if source.startswith('    '):source='if True:\n'+source
    scope=dict(namespace);exec(compile(source,str(ROOT/'tools/testlab/api_rule_preview_real_empty_coverage.py')+'[private adapter]','exec'),scope)
    return scope[function.__name__]

# Extend only this private transport, retaining its origin, size and timeout
# guards. The canonical generation module still rejects this additional path.
gen.prom_get=replace_function(gen.prom_get,[("('/-/ready','/api/v1/status/config','/api/v1/query_range')",
    "('/-/ready','/api/v1/status/config','/api/v1/query_range','/api/v1/targets')")],gen.__dict__)

def explicit_absence(result,kind,name):
    expected={'container':{'Error: No such object: '+name,'error: no such object: '+name,'Error: No such container: '+name,
        'Error response from daemon: No such container: '+name},
        'network':{'Error response from daemon: network '+name+' not found'}}
    if not (kind in expected and result.returncode==1 and result.stdout.strip() in ('','[]') and result.stderr.strip() in expected[kind]):
        raise Blocked('exact owned '+kind+' absence unconfirmed; retain unknown acquisition')
    return True

def validate_range(c,tokens,path,headers):
    p=urlsplit(path);args=parse_qs(p.query,keep_blank_values=True)
    for env,row in c.apps.items():
        if p.path!='/preview/'+env+'/api/v1/query_range':continue
        check(headers.get('Authorization')=='Bearer '+tokens[env],'exact source scoped token')
        check(set(args)=={'query','start','end','step','limit','timeout'} and all(len(v)==1 for v in args.values()),'bounded preview arguments')
        check(args['query']==[row['rule_query']] and args['step']==['300'] and args['limit']==['2']
            and args['timeout']==['15s'] and float(args['end'][0])-float(args['start'][0])==604800
            and abs(time.time()-float(args['end'][0]))<=330,'reviewed scoped seven-day query window')
        return env,'/api/v1/query_range?'+p.query
    raise Blocked('outside exact owned application endpoint')

def empty_matrix(raw):
    value=json.loads(raw)
    check(isinstance(value,dict) and value.get('status')=='success' and value.get('data')=={'resultType':'matrix','result':[]}
        and not value.get('warnings') and not value.get('infos'),'actual Prometheus must return successful empty matrix')
    return value

def source_envelope(status,raw):
    value=json.loads(raw)
    check(status==200 and isinstance(value,dict) and set(value)=={'status','data'}
        and value['status']=='success' and isinstance(value['data'],dict),'strict actual Prom success envelope required')
    return value['data']

def empty_loaded_config(status,raw):
    data=source_envelope(status,raw)
    check(set(data)=={'yaml'} and isinstance(data['yaml'],str) and len(data['yaml'])<=65536,'bounded exact loaded YAML envelope required')
    # Closed scalar/mapping shape of the pinned 3.13.3 renderer for our own
    # minimal config. No external YAML dependency or general YAML evaluation.
    # Its root empty scrape_configs is omitted by Go omitempty or is exactly [].
    containers={('global',),('runtime',),('storage',),('storage','tsdb'),('storage','tsdb','retention'),('otlp',)}
    expected={('global','scrape_interval'):'1s',('global','scrape_timeout'):'1s',('global','evaluation_interval'):'1m',
        ('global','metric_name_validation_scheme'):'utf8',('global','scrape_native_histograms'):'false',('global','extra_scrape_metrics'):'false',
        ('runtime','gogc'):'75',('storage','tsdb','outofordertimewindow'):'0',('storage','tsdb','retention','time'):'1h',
        ('otlp','translation_strategy'):'UnderscoreEscapingWithSuffixes',('otlp','label_name_underscore_sanitization'):'true',
        ('otlp','label_name_preserve_multiple_underscores'):'true'}
    stack=[];seen=set();leaves={}
    for line in data['yaml'].splitlines():
        if not line:continue
        match=re.fullmatch(r'( *)([a-z][a-z0-9_]*):(?: (.*))?',line)
        check(match is not None and len(match[1])%2==0,'unsupported loaded config shape')
        depth=len(match[1])//2;check(depth<=len(stack),'loaded config indentation skipped a parent')
        stack=stack[:depth];key=tuple([*stack,match[2]]);scalar=match[3]
        check(key not in seen,'duplicate loaded config field');seen.add(key)
        if key==('scrape_configs',):check(scalar=='[]','configured scrape jobs must be exactly empty')
        elif key in containers:
            check(scalar is None,'loaded config mapping shape differs');stack.append(match[2])
        else:
            check(key in expected and scalar==expected[key],'unknown or changed pinned loaded config field');leaves[key]=scalar
    check(containers.issubset(seen) and leaves==expected,'incomplete loaded config document')
    return dict(scrape_configs=[],omitted=('scrape_configs',) not in seen)

def empty_targets(status,raw):
    data=source_envelope(status,raw)
    check(data=={'activeTargets':[],'droppedTargets':[],'droppedTargetCounts':{}},'actual active/dropped targets/counts must be exactly empty')
    return data

def save_source_result(chain,kind,status,raw):
    check(kind in ('config','targets') and isinstance(raw,bytes) and len(raw)<=2<<20,'bounded private raw source evidence')
    path=chain.work/('prom-'+kind+'-actual.raw')
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'wb') as stream:stream.write(raw)
    ro._private(chain.work/('prom-'+kind+'-actual.json'),dict(status=status,bytes=len(raw),sha256=hashlib.sha256(raw).hexdigest(),raw=str(path)))

class EmptyFixture:
    def __init__(self,c):
        self.c=c;self.lock=threading.Lock();self.records=[];self.tokens={env:secrets.token_urlsafe(24) for env in c.apps};f=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                try:
                    env,path=validate_range(c,f.tokens,self.path,self.headers)
                    status,raw=gen.prom_get(path)
                    check(status==200,'actual owned Prometheus range failed');value=empty_matrix(raw)
                    with f.lock:f.records.append(dict(env=env,path=path,status=status,response=value,
                        raw_sha256=hashlib.sha256(raw).hexdigest(),source_cid=c.prom_cid))
                    # Preserve real upstream bytes; never replace/backfill matrix samples.
                    self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
                except (BrokenPipeError,ConnectionResetError):pass
                except Exception:self.send_error(502)
            def do_POST(self):self.send_error(405)
            do_PUT=do_PATCH=do_DELETE=do_POST
        self.server=gen.Server(RELAY_PORT,Handler,c.work/'tls-cert.pem',c.work/'tls-key.pem')
    def env_tokens(self):return {'TESTLAB_RULE_PREVIEW_'+env.upper():token for env,token in self.tokens.items()}
    def snapshot(self):
        with self.lock:return copy.deepcopy(self.records)
    def close(self):self.server.close()

def expected_dto(row,env,nonce):
    return dict(valid=True,series_count=0,source='owned-history-'+nonce+'-'+env,application_id=row['app'],environment_id=env,
        query_id='owned_rate',unit='requests/s',range='7d',values=[],points=[],current=None,fires=None,
        trigger_intervals=[],backtest=None,route=None,route_status='unknown',similar=None,similar_status='unknown',coverage='no_observed_series')

def verify_preview_audits(before,after,c,env):
    prior={a['id'] for a in before['management_audit']};added=[a for a in after['management_audit'] if a['id'] not in prior]
    check(len(added)==2 and {a['action'] for a in added}=={'v3.alert_rule.preview.requested','v3.alert_rule.preview.observed'},'exact two preview audit writes')
    digest=hashlib.sha256(c.apps[env]['rule_query'].encode()).hexdigest()
    for a in added:
        check(a['actor_id']==c.users['reader'] and a['actor_kind']=='user' and a['application_id']==c.apps[env]['app']
            and a['target']=='owned_rate' and a['outcome']=='recorded'
            and a['changes']['environment_id']==env and a['changes']['query_sha256']==digest,'preview audit actor/source/outcome differs')
    observed=next(a for a in added if a['action'].endswith('.observed'))
    check(all(observed['changes'].get(k)==v for k,v in dict(series_count=0,sampled_intervals=0,coverage='no_observed_series').items()),'empty audit invented observations')
    check(after['v3_rule_generate_attempts']==before['v3_rule_generate_attempts'] and after['v3_rule_generate_budget']==before['v3_rule_generate_budget'],'preview unexpectedly invoked generation')
    check(len(after['management_audit'])==len(before['management_audit'])+2 and len(after['v3_rule_preview_budget'])==1
        and all(after['v3_rule_preview_budget'][0].get(k)==v for k,v in dict(actor_id=c.users['reader'],application_id=c.apps[env]['app'],environment_id=env).items()),'exact preview budget/source identity')

class Chain(linux.Chain):
    def __init__(self,root,data,run_id,build,admission_path):
        super().__init__(root,data,run_id,build);self.admission_path=admission_path
        self.prom_cid=None;self.prom_attempted=False;self.prom_name='aiops-'+ro.OWNER+'-generation-'+self.nonce
        self.tmpfs='rw,noexec,nosuid,size=33554432,uid='+str(os.getuid())+',gid='+str(os.getgid())+',mode=0700'
        self.prom_args=['--config.file=/etc/prometheus/owned.json','--storage.tsdb.path=/prometheus','--storage.tsdb.retention.time=1h','--web.listen-address=0.0.0.0:9090']
        self.state.update(suite=MODULE,evidence_mode='real_local',model_calls=0,nonempty_history_proven=False,**execution_binding())
        ro._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.admission_path,self.run_id);super().frozen()
        if self.prom_cid:gen.guard_prom(self.inspect_container(self.prom_cid),self,live=False)
    def docker(self,argv,timeout=12):
        result=super().docker(argv,timeout)
        kind='network' if argv[:2]==['network','inspect'] else 'container' if argv[:1]==['inspect'] else None
        if kind and result.returncode:
            name=argv[-1];owned={self.network_name,self.network_id,self.prom_name,self.prom_cid}
            owned.update(x[k] for x in self.containers for k in ('name','cid') if x.get(k))
            # A UUID planned name is permitted only for its own pre-create check.
            planned='aiops-'+ro.OWNER+'-rule-'+self.nonce+'-'
            check(name in owned or name.startswith(planned),'absence check outside owned handles')
            # Preserve the actual return before classification. This is private,
            # bounded diagnostic evidence, not an adopted resource or a retry.
            ro.append_json(self.work/'docker-absence-checks.jsonl',dict(kind=kind,target=name,
                phase='cleanup' if self.cleanup_deadline else 'before_create_or_lookup',returncode=result.returncode,
                stdout=result.stdout[:4096],stderr=result.stderr[:4096],
                stdout_truncated=len(result.stdout)>4096,stderr_truncated=len(result.stderr)>4096))
            explicit_absence(result,kind,name)
        return result
    def start_prom(self):
        explicit_absence(self.docker(['inspect',self.prom_name]),'container',self.prom_name)
        ro._private(self.work/'prometheus.json',{'global':{'scrape_interval':'1s'},'scrape_configs':[]})
        self.config_hashes['prometheus.json']=hashlib.sha256((self.work/'prometheus.json').read_bytes()).hexdigest()
        self.prom_attempted=True;cidfile=self.work/'prom.cid'
        cmd=['create','--pull','never','--name',self.prom_name,'--cidfile',str(cidfile),'--label','aiops.testlab.owner='+ro.OWNER,
            '--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-generation','--cpus','.5','--memory','256m',
            '--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(os.getuid())+':'+str(os.getgid()),
            '--tmpfs','/prometheus:'+self.tmpfs,'-p','127.0.0.1:'+str(PROM_PORT)+':9090',
            '--mount','type=bind,source='+str(self.work/'prometheus.json')+',target=/etc/prometheus/owned.json,readonly',gen.PROM_IMAGE_ID,*self.prom_args]
        with ro.acquisition_signals():
            try:r=self.docker(cmd,timeout=15)
            finally:
                if cidfile.exists() and not cidfile.is_symlink():
                    cid=cidfile.read_text().strip()
                    if re.fullmatch('[a-f0-9]{64}',cid):self.prom_cid=cid;cidfile.chmod(0o600)
        check(self.prom_cid and r.returncode==0 and r.stdout.strip()==self.prom_cid,'own Prom create unconfirmed; no name adoption')
        gen.guard_prom(self.inspect_container(self.prom_cid),self,live=False)
        check(self.docker(['start',self.prom_cid]).returncode==0,'own Prom start failed')
        deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            gen.guard_prom(self.inspect_container(self.prom_cid),self)
            try:
                if gen.prom_get('/-/ready')[0]==200:break
            except OSError:pass
            time.sleep(.2)
        else:raise Blocked('real empty Prom readiness unavailable')
        actual=self.docker(['exec',self.prom_cid,'/bin/cat','/etc/prometheus/owned.json'])
        check(actual.returncode==0 and actual.stdout.encode()==(self.work/'prometheus.json').read_bytes(),'actual loaded Prom mount bytes differ')
        config_status,config_raw=gen.prom_get('/api/v1/status/config');save_source_result(self,'config',config_status,config_raw)
        targets_status,targets_raw=gen.prom_get('/api/v1/targets');save_source_result(self,'targets',targets_status,targets_raw)
        empty_loaded_config(config_status,config_raw);empty_targets(targets_status,targets_raw)
        self.marker_case('preview_empty.source',dict(cid=self.prom_cid,image_id=gen.PROM_IMAGE_ID,real_empty_TSDB=True,history_backfilled=False))
    def marker_case(self,name,value,status='passed'):
        if name=='container.tls':name='preview_empty.tls'
        super().marker_case(name,value,status)
    def run(self):
        self.marker_case('preview_empty.linux',dict(linux_sha256=self.build['binary_sha256'],source_sha=SOURCE))
        self.provision();self.create_network();self.start();self.seed()
        for name in ('admin','reader','peer','hidden','denied'):self.login(name)
        self.prepare_apps();self.start_prom();self.prepare_rules()
        row=self.apps['test'];body=dict(application_id=row['app'],query=row['rule_query'],op='>',threshold=2,for_minutes=0,range='7d')
        before=self.rule_ledger();count=len(self.rule_fixture.snapshot())
        value,_=self.call('reader','POST','/api/v1/alert-rules/preview',body,environment='test',index=1,
            validate=lambda v:check(v==expected_dto(row,'test',self.nonce),'exact empty preview DTO; absence must remain unknown'))
        after=self.rule_ledger();verify_preview_audits(before,after,self,'test')
        check(len(self.rule_fixture.snapshot())==count+1,'exact one actual range read')
        self.marker_case('preview_empty.normal',dict(no_observed_series=True,nonempty_history=False,API_index=1))
        before=self.rule_ledger();receipts=self.rule_fixture.snapshot()
        for user,status,env,code in ((None,401,'test','unauthorized'),('denied',403,'test','forbidden'),('reader',403,'staging','forbidden')):
            self.call(user,'POST','/api/v1/alert-rules/preview',body,environment=env,want=status,code=code,scenario='authorization')
        check(before==self.rule_ledger() and receipts==self.rule_fixture.snapshot(),'denied preview changed DB/source')
        self.marker_case('preview_empty.authorization',dict(anonymous401=True,no_grant403=True,cross_env403=True,no_source_or_audit_delta=True))
        for table in ('v3_alert_rule_drafts','v3_alert_rule_draft_operations','v3_alert_rule_enable_executions','v3_metrics_questions','v3_conversations'):
            check(self.sql('SELECT count(*) FROM '+table,read=True)=='0','unexpected model/rule/question side effect')
        ro._private(self.work/'empty-preview-db.json',after);ro._private(self.work/'empty-preview-source.json',receipts)
        self.marker_case('preview_empty.consistency',dict(actual_empty_source=True,model_calls=0,rule_writes=0,business_SQL_seed=False))
        return dict(run_id=self.run_id,normal_routes=list(TARGETS),http_calls=self.calls,empty_only=True,nonempty_7day_history=False,
            model_calls=0,required_cases=list(REQUIRED_CASES),cleanup_scope='own UUID role/database, exact CID/process/network/TLS handles/Prom tmpfs; one40s clock, all four ports absent',**execution_binding())
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;errors=[]
        try:
            if self.prom_cid:
                info=self.inspect_container(self.prom_cid);gen.guard_prom(info,self,live=False)
                if info['State']['Running']:check(self.docker(['stop','--time','1',self.prom_cid],timeout=5).returncode==0,'own Prom stop failed')
                gen.guard_prom(self.inspect_container(self.prom_cid),self,live=False)
                check(self.docker(['rm',self.prom_cid],timeout=4).returncode==0,'own Prom remove failed')
                explicit_absence(self.docker(['inspect',self.prom_cid],timeout=3),'container',self.prom_cid);self.prom_cid=None
            elif self.prom_attempted:explicit_absence(self.docker(['inspect',self.prom_name],timeout=3),'container',self.prom_name)
        except BaseException as e:errors.append(type(e).__name__)
        try:bounded_linux_cleanup(self)
        except BaseException as e:errors.append(type(e).__name__)
        check(not errors,'exact resource cleanup incomplete; private original cleanup receipts retained')
        emit('preview_empty.cleanup','passed',dict(run_id=self.run_id,deadline_seconds=40,grace_seconds=45,own_CIDs_only=True,ports_absent=True))

# Only these private module objects change; no canonical helper/class is patched.
ro.Chain.cleanup=replace_function(ro.Chain.cleanup,[('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline')],ro.__dict__)
bounded_linux_cleanup=replace_function(linux.Chain.cleanup,[('self.cleanup_deadline=time.monotonic()+40','self.cleanup_deadline=self.cleanup_deadline or time.monotonic()+40')],linux.__dict__)
Chain.prepare_rules=replace_function(linux.Chain.prepare_rules,[('generation.RuleFixture(self)','EmptyFixture(self)'),
    ("('host.docker.internal',18692)","('host.docker.internal',"+str(RELAY_PORT)+")"),
    ("'model_and_history_mode':'protocol_mock'","'model_and_history_mode':'inert model configuration; real empty preview'")],dict(linux.__dict__,EmptyFixture=EmptyFixture))
Chain.create_container=replace_function(linux.Chain.create_container,[
    ("AIOPS_V3_RULE_DRAFT_MODEL_CONFIG='/owned/draft-config.json',AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS=self.hosts[1],",'')],linux.__dict__)

def admission(path,run_id):
    p=Path(path);check(p.is_absolute() and p.resolve()==p and p.is_relative_to(ROOT/'.local/testlab'),'private admission path required')
    d=ro.private_json(p)
    expected=dict(status='root-admitted-rule-preview-real-empty',owner=ro.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,
        binary_sha256=BINARY,linux_binary_sha256=LINUX_SHA,runtime_sha256=RUNTIME,postgres_cid=gen.PG_CID,ports=list(ALL_PORTS),helper_sha256=hashes())
    check(all(d.get(k)==v for k,v in expected.items()),'exact root preview admission differs')
    check(d.get('fresh_nonce_hmac_process_tuple_verified') is True and d.get('headroom_verified') is True,'fresh HMAC/resource root admission missing')
    db=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(row[0]) for row in db.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
    finally:db.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('pid')==os.getppid()
        and active[0].get('source_tree_sha')==SOURCE and active[0].get('manifest_sha')==d.get('manifest_sha256')
        and re.fullmatch('[a-f0-9]{64}',str(d.get('manifest_sha256',''))),'sole exact matching Runner required')
    return d

def prepared_build():
    if hashlib.sha256(BUILD_PROOF.read_bytes()).hexdigest()!=PROOF_SHA:raise Blocked('exact current Linux build proof changed; no fallback/rebuild')
    build=linux.prepared_linux_build(ROOT,BUILD_PROOF);check(build['binary_sha256']==LINUX_SHA,'exact actual Linux binary changed')
    return build

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--run-id');p.add_argument('--admission',type=Path)
    mode=p.add_mutually_exclusive_group(required=True);mode.add_argument('--inventory',action='store_true');mode.add_argument('--live',action='store_true');a=p.parse_args(argv)
    if a.inventory:
        linux.Contract(ROOT);print(json.dumps(dict(module=MODULE,targets=TARGETS,ports=ALL_PORTS,required_cases=REQUIRED_CASES,offline_only=True)));return 0
    try:
        check(a.admission is not None and a.run_id is not None,'explicit new UUID/root admission required');run_id=str(uuid.UUID(a.run_id))
        check(run_id==a.run_id,'canonical UUID required');admission(a.admission,run_id);build=prepared_build()
        with linux.lease(ROOT),linux.context(),ro.cooperative_signals():
            linux.free_ports();data=ro.doctor(ROOT)
            check(data['source_tree_sha']==SOURCE and data['binary_sha256']==BINARY and data['runtime_sha']==RUNTIME,'frozen main/source/runtime differ')
            pg=ro.inspect_pg(data['postgres_container']);check(pg['Id']==gen.PG_CID and pg['HostConfig']['Memory']==2<<30 and pg['HostConfig']['NanoCpus']==4_000_000_000,'exact PG4CPU2GiB required')
            image=json.loads(subprocess.check_output(['docker','image','inspect',gen.PROM_IMAGE_ID],text=True,timeout=8))
            check(len(image)==1 and image[0]['Id']==gen.PROM_IMAGE_ID and image[0]['Architecture']=='arm64','immutable local Prom image unavailable; no pull')
            chain=Chain(ROOT,data,run_id,build,a.admission);linux.resources(chain.work,'admission');ro.execute(chain)
    except Blocked as e:emit('preview_empty.environment','blocked',dict(error=str(e)));return 2
    except Exception as e:emit('preview_empty.failure','failed',dict(error_type=type(e).__name__,error=str(e)));return 1
    return 0

if __name__=='__main__':raise SystemExit(main())
