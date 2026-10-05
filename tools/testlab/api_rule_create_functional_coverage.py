"""Native rule trial admission with real owned Prometheus; never 24h acceptance."""
from __future__ import annotations
import argparse,contextlib,datetime,fcntl,hashlib,http.client,json,os,re,socket,ssl,subprocess,uuid
from pathlib import Path
from urllib.parse import parse_qs,urlencode,urlsplit
import api_readonly_chain as ro
import api_rules_read_functional_coverage as rules
ROOT=ro.ROOT;PORTS=(18680,18681);BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
PROM_PORT,EXPORT_PORT,RELAY_PORT,TRIAL_PORT=18682,18683,18684,18685
MODULE='formal-api-rule-create-functional';TARGETS=(('POST','/alert-rules'),)
SUPPORT=(('GET','/alert-rules/dry-runs/{id}'),('POST','/services'),('POST','/auth/login'))
REQUIRED_CASES=('create.accepted','create.cross_instance','create.idempotency','create.authorization','create.boundaries','create.audit_rollback','create.live_evaluation','create.revocation')
check,Blocked=ro.check,ro.Blocked

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
def hashes():return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in ('api_rule_create_functional_coverage.py','api_rules_read_functional_coverage.py','api_readonly_chain.py')}

@contextlib.contextmanager
def context(chain=None):
    old=(rules.PORTS,rules.BASES,rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT,rules.Source,rules.emit,rules.hashes,ro.emit,ro.transport)
    def wire(base,method,path,body=None,headers=None,**kw):
        headers=dict(headers or {})
        if chain and chain.key is not None and method=='POST' and path=='/api/v1/alert-rules':headers['Idempotency-Key']=chain.key
        return old[-1](base,method,path,body,headers,**kw)
    rules.PORTS,rules.BASES,rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT=PORTS,BASES,PROM_PORT,EXPORT_PORT,RELAY_PORT
    rules.Source,rules.emit,rules.hashes,ro.emit,ro.transport=Source,emit,hashes,emit,wire
    try:yield
    finally:(rules.PORTS,rules.BASES,rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT,rules.Source,rules.emit,rules.hashes,ro.emit,ro.transport)=old

class Contract(ro.Contract):
    def canonical(self,method,path):
        result=super().canonical(method,path);short=result[0][len('/api/v1'):]
        if (method,short) not in (*TARGETS,*SUPPORT):raise Blocked('outside native trial admission package')
        return result

def spec(app,nonce,expr):
    check(bool(re.fullmatch('[a-f0-9]{32}',app)) and bool(re.fullmatch('[a-f0-9]{32}',nonce)),'UUID producer scope required')
    return dict(application_id=app,environment_id='test',group=rules.GROUP,name='NewTrial'+nonce,expr=expr,**{'for':'0s'},labels={'application':app,'environment':'test','severity':'P3'},annotations={'summary':'Owned admission trial'},mode='dry_run')
def validate_trial(value,expected_id=None):
    required={'trial_id','status','started_at','expires_at','last_update','evaluated_points','missing_points','would_fire','windows'}
    check(isinstance(value,dict) and set(value)==required,'exact native trial DTO keys differ')
    check(bool(re.fullmatch('[a-f0-9]{32}',value['trial_id'])) and (expected_id is None or value['trial_id']==expected_id),'trial identity differs')
    check(value['status'] in ('queued','running'),'acceptance must reflect actual queued/running state')
    for k in ('evaluated_points','missing_points','would_fire'):check(type(value[k]) is int and 0<=value[k]<=1440,'invalid bounded trial counters')
    check(isinstance(value['windows'],list) and all(isinstance(w,str) for w in value['windows']),'trial windows not string array')
    if value['started_at'] is not None:
        at=lambda x:datetime.datetime.fromisoformat(x.replace('Z','+00:00'))
        check((at(value['expires_at'])-at(value['started_at'])).total_seconds()==86400,'trial window was shortened')
    else:check(value['expires_at'] is None,'queued expiry differs')

class Source(rules.Source):
    def __init__(self,chain):
        super().__init__(chain);self.extra=[];self.trial=None
        try:self.prepare_trial(chain)
        except BaseException:
            self.close();raise
    def prepare_trial(self,chain):
        source=self
        class Handler(rules.BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                # Existing source relay remains GET-only and validates rules.
                try:path=source.validate(self.path);status,body=rules.direct_get(path)
                except Exception:status,body=502,b'{}'
                self.send_response(status);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            def do_POST(self):
                try:
                    chain.guard_prom();length=int(self.headers.get('Content-Length','0'));check(0<length<32768,'source body budget')
                    body=self.rfile.read(length);form=parse_qs(body.decode(),keep_blank_values=True)
                    if self.server.server_port==RELAY_PORT:
                        check(self.path=='/api/v1/format_query' and form=={'query':[chain.expr]} and not self.headers.get('Authorization'),'syntax relay escaped exact query')
                    else:
                        check(self.path=='/api/v1/query' and set(form)=={'query','time','timeout','limit'} and form['query']==[chain.expr] and form['timeout']==['15s'] and form['limit']==['101'] and self.headers.get('Authorization')=='Bearer '+chain.trial_token,'isolated query escaped scope')
                        check(abs(float(form['time'][0])-rules.time.time())<20,'trial query timestamp not current')
                    conn=http.client.HTTPConnection('127.0.0.1',PROM_PORT,timeout=5)
                    try:
                        conn.request('POST',self.path,body,{'Content-Type':'application/x-www-form-urlencoded'});r=conn.getresponse();status,data=r.status,r.read((2<<20)+1);check(len(data)<=2<<20,'source response budget')
                    finally:conn.close()
                    row={'path':self.path,'status':status,'response':json.loads(data),'query_time':form.get('time',[None])[0]}
                    source.extra.append(row);ro.append_json(chain.work/'trial-source.jsonl',row)
                except Exception:status,data=502,b'{}'
                self.send_response(status);self.send_header('Content-Length',str(len(data)));self.send_header('Connection','close');self.end_headers();self.wfile.write(data)
        # Replace only our acquired relay, preserving exporter and own Prom.
        self.relay.close();self.relay=rules.LocalServer(RELAY_PORT,Handler)
        cert,key=chain.work/'tls-cert.pem',chain.work/'tls-key.pem'
        # Fresh invocation-owned trust avoids inheriting expired shared component material.
        result=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','2','-subj','/CN=localhost',
            '-addext','subjectAltName=DNS:localhost,IP:127.0.0.1','-keyout',str(key),'-out',str(cert)],capture_output=True,timeout=20,umask=0o077)
        check(result.returncode==0,'invocation TLS certificate generation failed')
        chain.trial_token=ro.secrets.token_urlsafe(32);ro.private_file(chain.work/'trial-token',chain.trial_token)
        self.trial=rules.LocalServer(TRIAL_PORT,Handler);tls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);tls.load_cert_chain(cert,key);self.trial.server.socket=tls.wrap_socket(self.trial.server.socket,server_side=True)
        config={'endpoints':[dict(environment_id='test',application_id=chain.app,url='https://localhost:'+str(TRIAL_PORT),bearer_token_file=str(chain.work/'trial-token'),isolation_attested=True,isolation_reviewed_by='UUID-owned single-scrape Prometheus and exact source HTTP guards',isolation_reviewed_at=datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z'))]}
        ro._private(chain.work/'trial.json',config)
        for name in ('trial.json','trial-token','tls-cert.pem','tls-key.pem'):chain.config_hashes[name]=hashlib.sha256((chain.work/name).read_bytes()).hexdigest()
    def close(self):
        try:
            if self.trial:self.trial.close()
        finally:super().close()

class Chain(rules.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id);self.contract=Contract(root);self.key=None;self.trial_token=None;self.trial_id=None
        private=root/'.local/testlab/rule-create-parallel'/run_id;private.parent.mkdir(parents=True,exist_ok=True,mode=0o700);self.work.rename(private);self.work=private
        self.state.update(suite=MODULE,ports=[*PORTS,PROM_PORT,EXPORT_PORT,RELAY_PORT,TRIAL_PORT]);ro._private(self.work/'state.json',self.state)
    def startup_environment(self):
        env=super().startup_environment()
        if self.rule_source_config:env.update(AIOPS_V3_ALERT_RULE_TRIAL_CONFIG=str(self.work/'trial.json'),SSL_CERT_FILE=str(self.work/'tls-cert.pem'))
        return env
    def call(self,*args,**kwargs):
        with context(self):return super().call(*args,**kwargs)
    def counts(self):return json.loads(self.sql("SELECT json_build_object('trials',(SELECT count(*) FROM v3_alert_rule_trials),'audit',(SELECT count(*) FROM management_audit WHERE action='v3.alert_rule_trial.requested'))",read=True))
    def run(self):
        self.provision();self.start();self.login('admin');self.prepare_source();self.seed()
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES'+','.join('('+','.join(ro.quote(x) for x in (self.roles[n],'alert_rules','change','application',self.app))+')' for n in ('reader','peer'))+';')
        for n in ('reader','peer','denied'):self.login(n)
        body=spec(self.app,self.nonce,self.expr);path='/api/v1/alert-rules';self.key='trial-'+self.nonce
        # Failure before admission: transactional audit failure must leave no intent.
        self.sql("CREATE FUNCTION testlab_reject_trial_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='v3.alert_rule_trial.requested' THEN RAISE EXCEPTION 'Owned audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER testlab_trial_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION testlab_reject_trial_audit();")
        try:self.call('reader','POST',path,body,want=503,code='alert_rule_draft_unavailable',scenario='failure')
        finally:self.sql('DROP TRIGGER testlab_trial_audit ON management_audit; DROP FUNCTION testlab_reject_trial_audit();')
        check(self.counts()=={'trials':0,'audit':0},'audit failure committed partial intent');self.case_marker('create.audit_rollback',{'trial_rows':0,'audit_rows':0,'fault':'own audit trigger'})
        v,_=self.call('reader','POST',path,body,want=202,validate=validate_trial);self.trial_id=v['trial_id']
        check(self.counts()=={'trials':1,'audit':1},'admission intent/audit not atomic')
        audit=json.loads(self.sql("SELECT json_build_object('actor',actor_id,'application',application_id,'target',target,'outcome',outcome,'environment',changes->>'environment_id','request_digest',changes->>'request_digest','source_fingerprint',changes->>'source_fingerprint') FROM management_audit WHERE action='v3.alert_rule_trial.requested'",read=True))
        check(all(audit[k]==v for k,v in {'actor':self.users['reader'],'application':self.app,'target':self.trial_id,'outcome':'pending','environment':'test'}.items()) and all(re.fullmatch('[a-f0-9]{64}',audit[k]) for k in ('request_digest','source_fingerprint')),'native audit target/actor/scope/digests differ')
        ro._private(self.work/'exact-native-audit.json',audit)
        self.case_marker('create.accepted',{'normal_main':'POST /api/v1/alert-rules','trial_id':self.trial_id,'mode':'dry_run','duration_seconds':86400,'24h_pass':False,'provider_rule_written':False})
        status='/api/v1/alert-rules/dry-runs/'+self.trial_id
        self.call('reader','GET',status,index=1,validate=lambda v:validate_trial(v,self.trial_id));self.case_marker('create.cross_instance',{'persisted_trial':self.trial_id,'two_api_workers':True})
        self.call('reader','POST',path,body,want=202,index=1,scenario='idempotency',validate=lambda v:validate_trial(v,self.trial_id))
        changed=dict(body,annotations={'summary':'changed'});self.call('reader','POST',path,changed,want=409,code='idempotency_conflict',scenario='idempotency')
        check(self.counts()=={'trials':1,'audit':1},'idempotent replay duplicated intent/audit');self.case_marker('create.idempotency',{'same_id':True,'rows':1,'audit':1,'changed_payload':409})
        for user in (None,'denied'):
            self.call(user,'POST',path,body,want=401 if user is None else 403,code='unauthorized' if user is None else 'forbidden',scenario='authorization')
        self.call('reader','POST',path,body,environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('peer','GET',status,index=1,want=404,code='not_found',scenario='authorization')
        self.case_marker('create.authorization',{'anonymous':401,'no_change_grant':403,'cross_env':403,'other_actor_trial':404})
        self.key=None;self.call('reader','POST',path,body,want=422,code='idempotency_key_required',scenario='boundary');self.key='different-'+self.nonce
        self.call('reader','POST',path,dict(body,**{'for':'2h'}),want=422,code='invalid_alert_rule',scenario='boundary')
        self.call('reader','POST',path,dict(body,name='OwnedRule'+self.nonce),want=409,code='rule_identity_conflict',scenario='boundary')
        self.call('reader','POST',path,body,want=429,code='trial_capacity_full',scenario='boundary')
        self.case_marker('create.boundaries',{'missing_key':422,'invalid_duration':422,'existing_rule_name':409,'single_active_capacity':429})
        deadline=rules.time.monotonic()+20
        while rules.time.monotonic()<deadline:
            state=json.loads(self.sql("SELECT json_build_object('status',status,'points',evaluated_points,'missing',missing_points,'window_seconds',extract(epoch FROM expires_at-started_at)) FROM v3_alert_rule_trials WHERE id="+ro.quote(self.trial_id),read=True))
            if state['points']>=1:break
            rules.time.sleep(.3)
        else:raise Blocked('actual TLS isolated trial first point not available in20s; not a 24h pass')
        check(state=={'status':'running','points':1,'missing':0,'window_seconds':86400},'native first evaluation/wall-clock window differs')
        reads=[r for r in self.source.extra if r['path']=='/api/v1/query'];check(len(reads)==1 and reads[0]['status']==200 and len(reads[0]['response']['data']['result'])==1,'real scoped Prometheus first query proof absent')
        self.case_marker('create.live_evaluation',{'actual_worker_points':1,'actual_source_queries':1,'real_TLS':True,'window_seconds':86400,'24h_acceptance':False})
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+ro.quote(self.roles['reader'])+" AND module='alert_rules' AND action='change';")
        self.key='trial-'+self.nonce;self.call('reader','POST',path,body,index=1,want=403,code='forbidden',scenario='authorization');self.call('reader','GET',status,index=1,want=403,code='forbidden',scenario='authorization')
        check(self.counts()=={'trials':1,'audit':1},'rejection mutated accepted records');self.case_marker('create.revocation',{'second_instance_rejects':403,'intents':1,'audit':1})
        ro._private(self.work/'interrupted-trial.json',{'trial_id':self.trial_id,'actual_state':state,'reason':'bounded functional package cleanup stops own workers and drops own UUID DB','continuous_24h_completed':False})
        self.frozen();return dict(normal_main_routes=1,continuous_24h_pass=False,real_current_source=True)
    def cleanup(self):
        super().cleanup()
        with socket.socket() as s:check(s.connect_ex(('127.0.0.1',TRIAL_PORT))!=0,'own trial listener remains')
        emit('create.cleanup','passed',{'owned_trial_window_interrupted':self.trial_id is not None,'trial_port_absent':TRIAL_PORT,'continuous_24h_pass':False})

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--live',action='store_true');p.add_argument('--root',type=Path,default=ROOT);a=p.parse_args(argv)
    if not a.live:p.error('explicit --live required')
    folder=a.root/'.local/testlab/rule-create-parallel';folder.mkdir(parents=True,exist_ok=True,mode=0o700)
    try:
        with ro.cooperative_signals(),context():
            fd=os.open(folder/'lease.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
            try:
                fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
                with socket.socket() as s:check(s.connect_ex(('127.0.0.1',TRIAL_PORT))!=0,'trial port occupied')
                data=rules.doctor(a.root);chain=Chain(a.root,data,str(uuid.uuid4()));emit('create.doctor','passed',{'source_tree_sha':data['source_tree_sha'],'binary_sha256':data['binary_sha256'],'helper_sha256':hashes()});ro.execute(chain)
            finally:os.close(fd)
    except Blocked as e:emit('create.environment','blocked',{'error':str(e)});return 2
    except Exception as e:emit('create.failure','failed',{'error':str(e),'type':type(e).__name__});return 1
    return 0
if __name__=='__main__':raise SystemExit(main())
