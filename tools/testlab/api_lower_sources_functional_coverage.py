"""Formal metadata-only lower-source acceptance; no connection is claimed."""
from __future__ import annotations
import argparse, contextlib, fcntl, hashlib, json, os, socket, subprocess, threading, uuid
from pathlib import Path
import api_readonly_chain as ro
ROOT=ro.ROOT; PORTS=(18670,18671); BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
CAPTURE=18672; MODULE='formal-api-lower-sources-functional'; TARGETS=(('GET','/integrations/lower-sources'),)
REQUIRED_CASES=('lower.nonempty_dto','lower.authorization','lower.revocation','lower.readonly_zero_io','lower.empty_config','lower.invalid_config','lower.nonprod')
SOURCE='6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY='bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'
CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
check=ro.check; Blocked=ro.Blocked
PHASE='' 

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if PHASE and name!='lower.nonprod':name+=PHASE
    if 'interface' in fields:fields['interface']=dict(fields['interface'],proof_scope='native configured metadata only; endpoint and Vault connection unverified',test_name='FormalLowerSourcesChain')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)

def hashes():return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in ('api_lower_sources_functional_coverage.py','api_readonly_chain.py')}

@contextlib.contextmanager
def context(chain=None):
    old=ro.PORTS,ro.BASES,ro.emit,ro.transport
    def transport(base,method,path,body=None,headers=None,**kw):
        headers=dict(headers or {})
        if chain and chain.production and headers.get('X-AIOps-Environment')=='test':headers['X-AIOps-Environment']='prod'
        if chain:headers['Accept-Language']=chain.language
        return old[3](base,method,path,body,headers,**kw)
    ro.PORTS,ro.BASES,ro.emit,ro.transport=PORTS,BASES,emit,transport
    try:yield
    finally:ro.PORTS,ro.BASES,ro.emit,ro.transport=old

class Contract(ro.Contract):
    def canonical(self,method,path):
        if (method,path) not in (('GET','/api/v1/integrations/lower-sources'),('POST','/api/v1/auth/login')):raise Blocked('outside single GET metadata package')
        return super().canonical(method,path)

def config():
    return {'environments':[{'id':'staging','label':{'zh':'预发布','en':'Staging'}},{'id':'test','label':{'zh':'测试','en':'Test'}}],
      'sources':[{'id':'staging-gitlab','brand':'gitlab','environment_id':'staging','name':{'zh':'预发布仓库','en':'Staging repository'},'purpose':{'zh':'配置预览','en':'Configuration preview'},'endpoint':'https://127.0.0.1:18672/private-source','vault_ref':'vault://127.0.0.1:18672/secret/private-source#token'},
      {'id':'test-prometheus','brand':'prometheus','environment_id':'test','name':{'zh':'测试指标','en':'Test metrics'},'purpose':{'zh':'规则预览','en':'Rule preview'},'endpoint':'http://127.0.0.1:18672/private-metrics','vault_ref':'vault://127.0.0.1:18672/secret/private-metrics#password'}]}

def expected(language):
    c=config();lang='en' if language.lower().startswith('en') else 'zh';labels={e['id']:e['label'][lang] for e in c['environments']}
    return {'items':[dict(id=s['id'],brand=s['brand'],env=s['environment_id'],name=s['name'][lang],purpose=s['purpose'][lang],env_label=labels[s['environment_id']],credential='Production Vault reference configured; connection unverified' if lang=='en' else '生产 Vault 引用已配置；连接未验证') for s in c['sources']]}

def verify(value,language):check(value==expected(language),'exact localized metadata/order/redaction DTO differs')

class Capture:
    def __init__(self):
        self.count=0;self.stop=threading.Event();self.sock=socket.socket();self.sock.bind(('127.0.0.1',CAPTURE));self.sock.listen();self.sock.settimeout(.1)
        def run():
            while not self.stop.is_set():
                try:c,_=self.sock.accept();self.count+=1;c.close()
                except socket.timeout:pass
                except OSError:break
        self.thread=threading.Thread(target=run,daemon=True);self.thread.start()
    def close(self):self.stop.set();self.sock.close();self.thread.join(2);check(not self.thread.is_alive(),'capture thread leaked')

class Chain(ro.Chain):
    def __init__(self,root,data,run_id,production=True):
        super().__init__(root,data,run_id)
        private=Path(root)/'.local/testlab/lower-sources-parallel'/run_id;private.parent.mkdir(parents=True,exist_ok=True,mode=0o700);self.work.rename(private);self.work=private
        self.production=production;self.language='en';self.contract=Contract(root);self.capture=None;self.generation=0;self.configured=production
        self.state.update(actual_results_source='formal configured lower-source metadata only',helper_sha256=data['helper_sha256'],production_tier_synthetic=production);ro._private(self.work/'state.json',self.state)
    def frozen(self):
        check(hashes()==self.data['helper_sha256'],'lower-source helper changed');super().frozen()
    def provision(self):
        super().provision()
        if self.production:
            ro._private(self.work/'instance.json',dict(tier='production',name='Synthetic production metadata acceptance',domain='testlab.invalid',default_env='prod',environments=[dict(id='prod',label='Owned synthetic production',kind='standing',tone='prod',clusters=[])]))
            ro._private(self.work/'models.json',dict(tier='production',environments=[dict(id='prod',models=['openai:readonly-synthetic'],default_model='openai:readonly-synthetic')]))
            ro._private(self.work/'commands.json',[]);ro._private(self.work/'lower.json',config())
    def startup_environment(self):return {'AIOPS_V3_LOWER_SOURCES_CONFIG':str(self.work/'lower.json')} if self.configured else {}
    def apply_startup_environment(self,variables):
        result=super().apply_startup_environment(variables);self.last_variables=dict(result);return result
    def start(self):
        self.generation+=1;old=ro.emit
        def generation(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='lower.api_ready.'+str(self.generation)+'.'+name.rsplit('.',1)[1]
            old(name,status,actual,**fields)
        ro.emit=generation
        try:super().start()
        finally:ro.emit=old
    def seed(self):
        super().seed()
        if self.production:self.sql("UPDATE v3_role_grants SET scope_id='prod' WHERE scope_id='test';")
    def mark(self,name,value):self.frozen();emit(name,'passed',value)
    def snapshot(self):
        tables=('ops_incidents','ops_alert_occurrences','ops_notifications','management_audit','v3_audit_exports','conversations','messages')
        # Existing migrations define business tables; exact row JSON makes mutations visible.
        available=set(self.sql("SELECT tablename FROM pg_tables WHERE schemaname='public'",read=True).splitlines())
        tables=tuple(t for t in tables if t in available)
        return {t:json.loads(self.sql('SELECT COALESCE(json_agg(t ORDER BY row_to_json(t)::text),\'[]\') FROM '+t+' t',read=True)) for t in tables}
    def run(self):
        self.provision();self.capture=Capture();self.start();self.seed()
        for name in ('admin','reader','peer','denied'):self.login(name)
        before=self.snapshot()
        path='/api/v1/integrations/lower-sources'
        if not self.production:
            self.call('reader','GET',path,want=404,code='not_available',scenario='boundary');self.mark('lower.nonprod',{'status':404,'configuration':'absent','connection_verified':False})
        else:
            for language,index in (('en-US',0),('zh-CN',1)):
                self.language=language;self.call('reader','GET',path,index=index,validate=lambda v:verify(v,self.language))
            self.mark('lower.nonempty_dto',{'sources':2,'environments':2,'languages':['en-US','zh-CN'],'ordered':True,'endpoint_vault_ref_absent':True,'connection_verified':False})
            self.language='en'
            self.call(None,'GET',path,want=401,code='unauthorized',scenario='authorization')
            self.call('denied','GET',path,want=403,code='forbidden',scenario='authorization')
            for env in ('staging','all'):self.call('reader','GET',path,environment=env,want=422,code='invalid_environment',scenario='boundary')
            self.mark('lower.authorization',{'anonymous':401,'no_connectors_view':403,'cross_environment_and_all':422})
            self.sql('DELETE FROM v3_role_grants WHERE role_id='+ro.quote(self.roles['reader'])+" AND module='connectors';")
            self.call('reader','GET',path,index=1,want=403,code='forbidden',scenario='authorization')
            self.sql('DELETE FROM sessions WHERE user_id='+ro.quote(self.users['peer'])+';')
            self.call('peer','GET',path,index=1,want=401,code='unauthorized',scenario='authorization')
            self.mark('lower.revocation',{'other_instance_grant_revoke':403,'deleted_live_session':401})
            for p in reversed(self.processes):
                if p.poll() is None:p.terminate();p.wait(timeout=8)
            for i in (0,1):(self.work/('api-'+str(i)+'.log')).rename(self.work/('configured-api-'+str(i)+'.log'))
            self.configured=False;self.start()
            self.call('admin','GET',path,scenario='boundary',validate=lambda v:check(v=={'items':[]},'default missing config invented sources'))
            self.mark('lower.empty_config',{'items':[],'absent_configuration':True})
            for p in reversed(self.processes):
                if p.poll() is None:p.terminate();p.wait(timeout=8)
            invalid=config();invalid['sources'][0]['environment_id']='unconfigured'
            ro._private(self.work/'invalid-lower.json',invalid)
            variables=dict(self.last_variables,AIOPS_V3_LOWER_SOURCES_CONFIG=str(self.work/'invalid-lower.json'))
            fd=os.open(self.work/'invalid-startup.log',os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
            try:
                with ro.acquisition_signals():
                    child=subprocess.Popen([self.data['binary']],cwd=self.root,env=ro.command_env(variables,{}),stdout=fd,stderr=fd);self.processes.append(child)
            finally:os.close(fd)
            child.wait(timeout=10)
            log=(self.work/'invalid-startup.log').read_text()
            check(child.returncode!=0 and 'invalid lower-source configuration' in log,'invalid source environment not rejected at actual startup')
            self.mark('lower.invalid_config',{'actual_nonzero_exit':child.returncode,'unbound_environment_rejected':True,'public_listener_started':False})
        after=self.snapshot();check(before==after,'metadata GET mutated business/audit rows');check(self.capture.count==0,'metadata endpoint contacted configured source or Vault')
        ro._private(self.work/'readonly-proof.json',dict(before=before,after=after,capture_connections=self.capture.count))
        self.mark('lower.readonly_zero_io',{'business_audit_unchanged':True,'source_and_vault_tcp_connections':0})
        return dict(run_id=self.run_id,normal_routes=[list(t) for t in TARGETS] if self.production else [],source_tree_sha=self.data['source_tree_sha'],binary_sha256=self.data['binary_sha256'],http_calls=self.calls,production_tier_synthetic=self.production,connection_unverified=True,helper_sha256=hashes())
    def cleanup(self):
        try:super().cleanup()
        finally:
            if self.capture:self.capture.close();emit('lower.capture_cleanup','passed',{'connections':self.capture.count,'listener_closed':True})

@contextlib.contextmanager
def lease(root):
    path=Path(root)/'.local/testlab/lower-sources-parallel';path.mkdir(parents=True,exist_ok=True,mode=0o700)
    with open(path/'lease.lock','a') as f:
        os.chmod(f.name,0o600);fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
        for p in (*PORTS,CAPTURE):
            with socket.socket() as sock:
                check(sock.connect_ex(('127.0.0.1',p))!=0,'owned port already active')
        yield

def main(argv=None):
    global PHASE
    p=argparse.ArgumentParser();p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--live',action='store_true');p.add_argument('--inventory',action='store_true');a=p.parse_args(argv)
    if a.inventory:print(json.dumps(dict(targets=TARGETS,required_cases=REQUIRED_CASES,proof_scope='metadata only',offline_only=True)));return 0
    if not a.live:p.error('--live or --inventory required')
    try:
        with lease(a.root),context(),ro.cooperative_signals():
            data=ro.doctor(a.root)
            check(data['source_tree_sha']==SOURCE and data['binary_sha256']==BINARY and ro.inspect_pg(data['postgres_container'])['Id']==CID,'frozen exact source/binary/PG differs')
            data['helper_sha256']=hashes()
            for production in (True,False):
                PHASE='' if production else '.nonprod'
                chain=Chain(a.root,data,str(uuid.uuid4()),production)
                with context(chain):ro.execute(chain)
    except Blocked as e:emit('lower.environment','blocked',{'error':str(e)});return 2
    except Exception as e:emit('lower.failure','failed',{'type':type(e).__name__,'error':str(e)});return 1
    return 0
if __name__=='__main__':raise SystemExit(main())
