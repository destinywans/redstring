"""Real owned-DB k6 load and continuous wall-clock soak. No compressed acceptance."""
from __future__ import annotations
import argparse,datetime,hashlib,hmac,http.client,json,os,re,secrets,signal,socket,subprocess,sys,threading,time,uuid
from pathlib import Path
from urllib.parse import urlparse
from load.seed import fixture,seed,sql,quote,private,stream_seed,stream_emit,stream_finish
from runner import source_fingerprint
from backend import control_proof
from faults import validate_identity
ROOT=Path(__file__).resolve().parents[2]
K6_IMAGE='grafana/k6:1.5.0@sha256:2072ea9eafa596532d9aee0cc0e0a50cfb0e7fb703981a46179af5f4f22c5ea4'
K6_RELEASE='https://github.com/grafana/k6/releases/tag/v1.5.0'
STOP=threading.Event()
DIAGNOSTIC_BUDGET=3

def result(id,status,actual,started=None,**fields):
    if isinstance(actual,dict):
        fields.setdefault('elapsed',actual.get('elapsed_seconds',0 if started is None else time.monotonic()-started))
        fields.setdefault('planned_seconds',actual.get('planned_seconds'))
        fields.setdefault('current_test',actual.get('stage',id))
        if status!='running':fields.setdefault('metrics',dashboard_metrics(actual,fields.get('name',id)))
    print(json.dumps(dict(id=id,name=fields.pop('name',id),status=status,module='performance',type=fields.pop('type','performance'),evidence_mode='real_local',expected=fields.pop('expected','真实隔离环境；时间和负载不压缩'),actual=actual,duration=0 if started is None else time.monotonic()-started,gap_ids=fields.pop('gap_ids',[37,43]),**fields),ensure_ascii=False),flush=True)

def dashboard_metrics(actual,name=None):
    summary=actual.get('summary',{})
    def value(key,stat):return summary.get(key,{}).get('values',{}).get(stat)
    return {'name':name or actual.get('name') or actual.get('stage'),'stage':actual.get('stage'),
            'scope':{'profile':actual.get('profile'),'measured_routes':actual.get('measured_routes')},
            'full_aggregate_scope_accepted':actual.get('full_aggregate_scope_accepted'),'excluded_aggregate_paths':actual.get('excluded_aggregate_paths'),
            'throughput_rps':value('iterations','rate'),'p50_ms':value('http_req_duration','med'),
            'p95_ms':value('http_req_duration','p(95)'),'p99_ms':value('http_req_duration','p(99)'),
            'error_rate':actual.get('unexpected_error_rate'), 'bounded_write':{'error_rate':value('bounded_write_errors','rate'),'p50_ms':value('bounded_write_latency','med'),'p95_ms':value('bounded_write_latency','p(95)'),'p99_ms':value('bounded_write_latency','p(99)')}, 'dropped_iterations':actual.get('dropped_iterations'),
            'generator_seconds':actual.get('generator_seconds'), 'resources':actual.get('resource_samples',[]), 'full_sample_count':actual.get('full_sample_count',len(actual.get('resource_samples',[]))), 'resource_evidence':actual.get('resource_evidence'), 'resource_trends':actual.get('resource_trends'),
            'missing':actual.get('resource_missing',sorted(set(k for sample in actual.get('resource_samples',[]) for k in sample.get('missing',[])))),
            'trial_window':actual.get('trial_final'), 'resource_recovery':actual.get('resource_recovery')}


def resource_summary(samples):
    series={}
    def add(name,at,value):
        if isinstance(value,(int,float)):series.setdefault(name,[]).append((at,value))
    for sample in samples:
        at=sample.get('at')
        if not isinstance(at,(int,float)):continue
        for name,value in sample.get('postgres',{}).items():add('postgres.'+name,at,value)
        for item in sample.get('backend_fds',[]):add('pid.'+str(item['pid'])+'.open_fds',at,item['open_fds'])
        for item in sample.get('backend_processes',[]):
            for name in ('rss_kib','cpu_percent'):add('pid.'+str(item['pid'])+'.'+name,at,item.get(name))
    trends={}
    for name,points in series.items():
        first,last=points[0],points[-1];delta=last[1]-first[1];seconds=last[0]-first[0]
        trends[name]={'sample_count':len(points),'first':first[1],'last':last[1],'min':min(v for _,v in points),'max':max(v for _,v in points),'delta':delta,'window_seconds':seconds,'delta_per_hour':delta*3600/seconds if seconds>0 else None}
    return {'resource_samples':samples[-20:],'full_sample_count':len(samples),'resource_trends':trends,'resource_missing':sorted(set(k for sample in samples for k in sample.get('missing',[]))),'sample_scope':'stdout keeps last 20; trend statistics use all raw samples; full evidence file retained'}


def request(data,base,method,path,user=None,body=None,headers=None,timeout=20):
    if base not in data['base_urls'] or not path.startswith('/api/v1/') or '://' in path:raise RuntimeError('only owned formal API paths are allowed')
    origin=urlparse(base);conn=http.client.HTTPConnection(origin.hostname,origin.port,timeout=timeout)
    h={'X-AIOps-Environment':data['environment'],'Origin':base}
    if user:h['Cookie']='aiops_session='+user['cookie']
    h.update(headers or {})
    payload=None
    if body is not None:payload=json.dumps(body).encode();h['Content-Type']='application/json'
    try:
        conn.request(method,path,payload,h);response=conn.getresponse();raw=response.read(4<<20)
        try:parsed=json.loads(raw)
        except ValueError:parsed={}
        return response.status,parsed,response.getheaders()
    finally:conn.close()

def login(data,index):
    prefix='perf-'+data['owner']+'-';email=prefix+'user-'+str(index+1)+'@testlab.invalid'
    status,body,headers=request(data,data['base_urls'][index%2],'POST','/api/v1/auth/login',body={'email':email,'password':data['perf_password']},headers={'X-Forwarded-For':f'192.0.2.{index+1}'})
    if status!=200:raise RuntimeError(f'owned performance user login returned {status}')
    tokens=[v.split(';',1)[0].split('=',1)[1] for k,v in headers if k.lower()=='set-cookie' and v.startswith('aiops_session=')]
    if len(tokens)!=1:raise RuntimeError('formal login did not issue one session')
    expires=float(sql(data,"SELECT extract(epoch FROM expires_at) FROM sessions WHERE token_hash="+quote(hashlib.sha256(tokens[0].encode()).hexdigest())+" AND user_id="+quote(body['user']['id'])+";"))
    return {'id':body['user']['id'],'email':email,'cookie':tokens[0],'session_expires_at':expires,'conversation_id':prefix+'conv-'+str(index+1)}

def prepare(profile):
    data=fixture();counts=seed(data,profile)
    users=[login(data,i) for i in range(20)]
    data['users']=users;data['counts']=counts
    for i,u in enumerate(users):
        alert=counts['prefix']+'alert-'+str(i+1)
        revision=sql(data,'SELECT xmin::text FROM ops_incidents WHERE id='+quote(alert)+';')
        status,body,_=request(data,data['base_urls'][i%2],'POST','/api/v1/alerts/'+alert+'/claim',u,{'expected_revision':revision})
        if status!=200:raise RuntimeError('bounded own synthetic alert claim preflight failed HTTP '+str(status))
        u['claim_alert_id']=alert;u['claim_revision']=sql(data,'SELECT xmin::text FROM ops_incidents WHERE id='+quote(alert)+';')
    result('perf.write_preflight','passed',{'path':'POST /api/v1/alerts/{id}/claim','scope':'20 own seeded alerts, each assigned once to its generated test user','http_status':200,'allocation':'4 percent bounded idempotent claim retry; row-lock transaction, no repeated audit or new objects'},expected='真实授权认领，后续相同用户幂等重试，不复写他人/无限造数据')
    route_candidates=[['/api/v1/conversations','db'],['/api/v1/conversations/'+users[0]['conversation_id'],'db'],['/api/v1/alerts/'+users[0]['claim_alert_id'],'db'],['/api/v1/inbox','db'],['/api/v1/alert-groups','aggregate'],['/api/v1/home','aggregate']]
    preflight=[];excluded=[]
    limits={'/api/v1/inbox':'pagination_required','/api/v1/alert-groups':'pagination_required','/api/v1/home':'home_result_limit'}
    for path,kind in route_candidates:
        status,body,_=request(data,data['base_urls'][0],'GET',path,users[0]);ok=status==200 and not body.get('error');error=body.get('error',{});code=error.get('code') if isinstance(error,dict) else None
        normal_limit=status==503 and path in limits and code==limits[path]
        preflight.append({'path':path,'http_status':status,'error_code':code,'normal_limit_boundary':normal_limit,'measured':not normal_limit})
        if normal_limit:
            excluded.append(path)
            result('perf.boundary.'+path.rsplit('/',1)[-1],'passed',{'path':path,'http_status':status,'error_code':code,'performance_acceptance':False},type='boundary',expected='明确当前契约限额拒绝语义，独立boundary通过不算性能通过')
            result('perf.aggregate.'+path.rsplit('/',1)[-1],'blocked',{'path':path,'http_status':status,'error_code':code,'p95_p99_accepted':False,'reason':'requested same-environment data exceeds existing complete-aggregation contract'},expected='原计划聚合性能路径200并测P95/P99；当前正常限额阻塞')
        elif not ok:raise RuntimeError('measured route preflight failed '+path+' HTTP '+str(status)+' '+str(code)+'; arbitrary error is not an accepted limit boundary')
    data['excluded_aggregate_paths']=excluded;data['aggregate_measurement_supported']=any(kind=='aggregate' and path not in excluded for path,kind in route_candidates);data['route_preflight']=preflight
    result('perf.route_preflight','passed',{'routes':preflight,'coverage_limit':'only currently legal measured routes; explicitly blocked aggregates are not performance acceptance','excluded_aggregate_paths':excluded},expected='全部实际计量路由200且非error envelope；精确限额allowlist单列blocked性能路径')
    return data

def diagnostic_get(port,path,limit,deadline):
    """One total wall-clock budget, including headers and EOF-delimited bodies."""
    if not (port==18560 and re.fullmatch(r'/attest\?nonce=[a-f0-9]{32}',path) or port in (18561,18562) and path=='/healthz'):raise RuntimeError('unknown diagnostic endpoint')
    remaining=deadline-time.monotonic()
    if remaining<=0:raise TimeoutError('diagnostic deadline elapsed')
    conn=http.client.HTTPConnection('127.0.0.1',port,timeout=min(1,remaining))
    expired=threading.Event();lock=threading.Lock();transport=[None];response=None
    def abort():
        expired.set()
        with lock:sock=transport[0]
        if sock:
            try:sock.shutdown(socket.SHUT_RDWR)
            except OSError:pass
    timer=threading.Timer(remaining,abort);timer.name='testlab-diagnostic-deadline';timer.daemon=True;timer.start()
    try:
        conn.connect()
        # getresponse() can transfer this socket to HTTPResponse and clear conn.sock.
        with lock:transport[0]=conn.sock
        if expired.is_set() or time.monotonic()>=deadline:abort();raise TimeoutError('diagnostic deadline elapsed')
        conn.request('GET',path);response=conn.getresponse();raw=response.read(limit+1)
        if expired.is_set() or time.monotonic()>=deadline:raise TimeoutError('diagnostic deadline elapsed')
        return response.status,raw
    except Exception:
        if expired.is_set() or time.monotonic()>=deadline:raise TimeoutError('diagnostic deadline elapsed') from None
        raise
    finally:
        timer.cancel();timer.join()
        if response is not None:response.close()
        conn.close()
        if transport[0] is not None:transport[0].close()


def identity(data,timeout=5,diagnostic_deadline=None):
    nonce=secrets.token_hex(16)
    if diagnostic_deadline is not None:
        status,raw=diagnostic_get(18560,'/attest?nonce='+nonce,1<<16,diagnostic_deadline)
        if status!=200 or len(raw)>1<<16:raise RuntimeError('bounded supervisor attestation unavailable')
        receipt=json.loads(raw)
    else:
        c=http.client.HTTPConnection('127.0.0.1',18560,timeout=timeout)
        try:
            c.request('GET','/attest?nonce='+nonce);r=c.getresponse();raw=r.read((1<<16)+1)
            if r.status!=200 or len(raw)>1<<16:raise RuntimeError('bounded supervisor attestation unavailable')
            receipt=json.loads(raw)
        finally:c.close()
    if (not isinstance(receipt,dict) or set(receipt)!={'nonce','identity','proof'} or receipt['nonce']!=nonce
        or not isinstance(receipt['identity'],dict) or not isinstance(receipt['proof'],str)
        or not hmac.compare_digest(receipt['proof'],control_proof(data['control_secret'],nonce,receipt['identity']))):
        raise RuntimeError('supervisor nonce/HMAC attestation mismatch')
    v=receipt['identity']
    try:validate_identity(data,v)
    except Exception as error:
        # Preserve the authenticated failing snapshot without relaxing readiness.
        if v.get('owner')==data['owner'] and v.get('pid')==data['supervisor_pid']:
            error.testlab_authenticated_identity=dict(v)
        raise
    return v


def stage_error_evidence(data,work,error,last_resource=None):
    """Bounded diagnostics only; never make an unready identity acceptable."""
    deadline=time.monotonic()+DIAGNOSTIC_BUDGET
    def owned(value):
        return isinstance(value,dict) and value.get('owner')==data['owner'] and value.get('pid')==data['supervisor_pid']
    def snapshot(value):
        return {'owner':data['owner'],'pid':data['supervisor_pid'],'api_pids':[pid for pid in value.get('api_pids',[])[:2] if type(pid) is int],'source_tree_sha':str(value.get('source_tree_sha',''))[:64],'ready':value.get('ready') is True,'failed':value.get('failed') is True}
    def numeric_sample(value):
        if not isinstance(value,dict):return None
        # Existing metrics contain numeric counters; no arbitrary strings or credentials.
        sampled={'at':value.get('at') if isinstance(value.get('at'),(int,float)) else None}
        sampled['postgres']={key:val for key,val in value.get('postgres',{}).items() if key in ('connections','active','waiting','locks','queued_replies','running_replies','database_bytes') and isinstance(val,(int,float))}
        for key,fields in (('backend_processes',('pid','rss_kib','cpu_percent')),('backend_fds',('pid','open_fds'))):
            sampled[key]=[{field:row[field] for field in fields if isinstance(row.get(field),(int,float))} for row in value.get(key,[])[:2] if isinstance(row,dict)]
        sampled['missing']=[item for item in value.get('missing',[])[:16] if isinstance(item,str) and re.fullmatch(r'[a-z_]{1,64}',item)]
        return sampled
    failed=getattr(error,'testlab_authenticated_identity',None)
    reason=str(error) if str(error) in ('two ready registered child PIDs required','supervisor identity changed; refusing fault','supervisor nonce/HMAC attestation mismatch','bounded supervisor attestation unavailable','backend restarted or runtime/helper configuration changed; continuous window invalid','backend source changed or binary does not match source; restart/re-freeze required') else 'stage guard or execution exception'
    evidence={'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'error_type':type(error).__name__,'reason':reason,'failure_authenticated_identity':snapshot(failed) if owned(failed) else None,'fresh_authenticated_identity':None,'health':[],'resources_last':numeric_sample(last_resource),'missing':['postgres_pool_internal_wait'],'diagnostic_scope':'fixed own public health endpoints; no auth material or response headers'}
    if last_resource is None:evidence['missing'].append('last_resource_sample')
    if not owned(failed):evidence['missing'].append('failure_authenticated_controller_identity')
    path=Path(work)/'error-evidence.json'
    if path.parent.is_symlink() or not path.parent.resolve().is_relative_to(Path(data['perf_work']).resolve()) or not path.resolve().is_relative_to((ROOT/'.local/testlab').resolve()):raise RuntimeError('diagnostic evidence outside owned stage')
    # Keep the failure-instant snapshot even if later diagnostic collection fails.
    private(path,evidence)
    fresh=None
    try:fresh=identity(data,timeout=1,diagnostic_deadline=deadline)
    except Exception as diagnostic_error:
        fresh=getattr(diagnostic_error,'testlab_authenticated_identity',None)
        evidence['attestation_error_type']=type(diagnostic_error).__name__
        if isinstance(diagnostic_error,TimeoutError):evidence['missing'].append('controller_attestation_deadline')
    baseline=data.get('_freeze_baseline')
    if owned(fresh):evidence['fresh_authenticated_identity']=snapshot(fresh)
    else:evidence['missing'].append('fresh_authenticated_controller_identity')
    # A failed proof/owner is not a basis for probing any API, even if data contains URLs.
    pids=fresh.get('api_pids',[]) if owned(fresh) else []
    mapped=owned(fresh) and fresh.get('source_tree_sha')==data['source_tree_sha'] and baseline and len(baseline)>=2 and baseline[0]==fresh['pid'] and len(pids)==2 and all(type(pid) is int and pid>1 for pid in pids) and len(set(pids))==2 and tuple(pids)==baseline[1]
    if mapped:
        for port in (18561,18562):
            started=time.monotonic();row={'url':f'http://127.0.0.1:{port}/healthz'}
            try:
                status,raw=diagnostic_get(port,'/healthz',4096,deadline)
                row.update(http_status=status,body_bytes_observed=len(raw),body_truncated=len(raw)>4096,body_sha256=hashlib.sha256(raw).hexdigest())
                if len(raw)<=4096:
                    try:body=json.loads(raw)
                    except ValueError:body=None
                    if isinstance(body,dict):
                        if body.get('status')=='ok':row['status']='ok'
                        code=body.get('error',{}).get('code') if isinstance(body.get('error'),dict) else None
                        if isinstance(code,str) and re.fullmatch(r'[a-z_]{1,64}',code):row['error_code']=code
            except Exception as diagnostic_error:
                row['error_type']=type(diagnostic_error).__name__
                if isinstance(diagnostic_error,TimeoutError):evidence['missing'].append('health_'+str(port)+'_deadline')
            row['elapsed_seconds']=round(time.monotonic()-started,3);evidence['health'].append(row)
    else:evidence['missing'].append('owned_api_health_mapping')
    evidence['diagnostic_elapsed_seconds']=round(time.monotonic()-(deadline-DIAGNOSTIC_BUDGET),3)
    private(path,evidence)
    return {'error_evidence':str(path.relative_to(ROOT)),'reason':reason,'diagnostic_missing':evidence['missing']}

def frozen_inputs(data):
    names={Path(data['instance_config']),Path(data['binary']),ROOT/'tools/testlab/perf.py',ROOT/'tools/testlab/backend.py',ROOT/'tools/testlab/faults.py',ROOT/'tools/testlab/load/seed.py',ROOT/'tools/testlab/load/traffic.js',ROOT/'tools/testlab/fixtures.py',ROOT/'.local/testlab/component-environment.json'}
    owner_work=Path(data['perf_work']).parent.resolve()
    runtime=ROOT/'.local/testlab/runtime-config.json'
    hook=Path(data['perf_work'])/'soak-hooks.json'
    def owned_path(value):
        path=Path(value)
        if not path.is_absolute():raise RuntimeError('runtime input must be an explicit absolute owned path')
        if path.is_symlink() or not path.resolve().is_relative_to(owner_work):raise RuntimeError('runtime configuration input escapes the owned work directory')
        return path
    def collect(value,depth=0):
        if depth>20 or len(names)>100:raise RuntimeError('runtime reference list exceeds bound')
        if isinstance(value,dict):
            for key,item in value.items():
                if isinstance(item,str) and (key.endswith('_file') or key in ('tls_cert','tls_key') or key.endswith('_CONFIG') or key.endswith('_FILE')):
                    path=owned_path(item);names.add(path)
                    if path.suffix=='.json' and path.is_file():collect(json.loads(path.read_text()),depth+1)
                else:collect(item,depth+1)
        elif isinstance(value,list):
            for item in value:collect(item,depth+1)
    if runtime.exists():
        names.add(runtime);r=json.loads(runtime.read_text())
        if r.get('owner')!=data['owner']:raise RuntimeError('runtime owner mismatch')
        collect(r)
        # Exact Prom scrape/isolation inputs are behavior too, even without env references.
        runtime_work=owned_path(r['work'])
        for path in runtime_work.iterdir():
            if path.is_file() and path.suffix in ('.json','.yml','.yaml'):names.add(path)
    if hook.exists():names.add(hook)
    component=json.loads((ROOT/'.local/testlab/component-environment.json').read_text())
    ca=component.get('host_config',{}).get('ca_file')
    if ca:names.add(owned_path(ca))
    return sorted(names,key=str)


def frozen(data):
    v=identity(data);current=source_fingerprint(ROOT)
    if current=='unknown' or current!=data['source_tree_sha'] or current!=v.get('source_tree_sha'):raise RuntimeError('backend source changed or binary does not match source; restart/re-freeze required')
    digest=hashlib.sha256()
    names=frozen_inputs(data)
    for path in names:
        if not path.is_file() or path.is_symlink():raise RuntimeError('frozen runtime configuration input unavailable')
        digest.update(str(path).encode());digest.update(path.read_bytes())
    baseline=(v['pid'],tuple(v.get('api_pids',[])),digest.hexdigest())
    if '_freeze_baseline' not in data:data['_freeze_baseline']=baseline
    if baseline!=data['_freeze_baseline']:raise RuntimeError('backend restarted or runtime/helper configuration changed; continuous window invalid')
    return current

def metrics(data):
    captured={'at':time.time(),'missing':[]}
    try:
        captured['postgres']=json.loads(sql(data,"SELECT json_build_object('connections',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()),'active',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND state='active'),'waiting',(SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'),'locks',(SELECT count(*) FROM pg_locks WHERE NOT granted),'queued_replies',(SELECT count(*) FROM v3_conversation_replies WHERE state='queued'),'running_replies',(SELECT count(*) FROM v3_conversation_replies WHERE state='running'),'database_bytes',pg_database_size(current_database()));"))
    except Exception:captured['missing'].append('postgres_connections_locks_queue')
    try:
        pid=identity(data).get('api_pids',[]);captured['backend_processes']=[]
        for p in pid:
            r=subprocess.run(['ps','-p',str(int(p)),'-o','rss=,%cpu='],capture_output=True,text=True,timeout=5)
            if r.returncode or not r.stdout.strip():raise RuntimeError()
            rss,cpu=r.stdout.split();captured['backend_processes'].append({'pid':p,'rss_kib':int(rss),'cpu_percent':float(cpu)})
    except Exception:captured['missing'].extend(['backend_cpu','backend_rss'])
    # macOS does not expose /proc; lsof permissions are explicitly distinguished from zero FDs.
    try:
        pids=identity(data).get('api_pids',[]);captured['backend_fds']=[]
        for p in pids:
            r=subprocess.run(['lsof','-a','-p',str(int(p)),'-Ff'],capture_output=True,text=True,timeout=5)
            if r.returncode:raise RuntimeError()
            captured['backend_fds'].append({'pid':p,'open_fds':sum(line[1:].isdigit() for line in r.stdout.splitlines() if line.startswith('f'))})
    except Exception:captured['missing'].append('backend_fd')
    captured['missing'].extend(['backend_goroutines','heap_profile'])
    return captured

def own_remove(name,owner):
    p=subprocess.run(['docker','inspect',name,'--format','{{json .Config.Labels}}'],capture_output=True,text=True)
    if p.returncode:return
    if json.loads(p.stdout).get('aiops.testlab.owner')!=owner:raise RuntimeError('refusing to stop unowned k6 container')
    subprocess.run(['docker','rm','-f',name],capture_output=True,timeout=30)

class SSEProtocolError(RuntimeError):pass

class SSEParser:
    def __init__(self,cursor=0):self.cursor=cursor;self.frame_id=None;self.data=[]
    def feed(self,line):
        if len(line)>=1<<20:raise SSEProtocolError('SSE line exceeds bounded parser limit')
        if line.startswith(b'id:'):
            try:self.frame_id=int(line[3:].strip())
            except ValueError:raise SSEProtocolError('invalid SSE event ID')
        elif line.startswith(b'data:'):self.data.append(line[5:].strip())
        elif line in (b'\n',b'\r\n'):
            if self.frame_id is None and not self.data:return False
            if self.frame_id is None or self.frame_id<=self.cursor:raise SSEProtocolError('non-monotonic/duplicate replay ID')
            try:payload=json.loads(b'\n'.join(self.data))
            except ValueError:raise SSEProtocolError('invalid SSE JSON payload')
            if not isinstance(payload,dict) or not isinstance(payload.get('type'),str):raise SSEProtocolError('invalid SSE event contract')
            self.cursor=self.frame_id;self.frame_id=None;self.data=[];return True
        elif not line.startswith((b':',b'event:',b'retry:')):raise SSEProtocolError('unrecognized SSE protocol line')
        return False

def sse_accepted(stats):
    return bool(stats and stats.get('stable_samples',0)>0 and not stats.get('window_violations',0) and not stats.get('protocol_errors',0) and not stats.get('producer_errors',0) and stats.get('normal_frames',0)>0 and stats.get('slow_bytes',0)>0)

class Streams:
    """60 real HTTP SSE sockets; payload producer is explicit synthetic DB seed."""
    def __init__(self,data,run,normal=50,slow=10,reconnect_seconds=60,large=True):
        self.normal=normal;self.slow=slow;self.reconnect_seconds=reconnect_seconds;self.large=large
        self.data=data;self.prefix=stream_seed(data,data['users'],run,normal+slow);self.done=threading.Event();self.lock=threading.Lock();self.stats={'normal_target':normal,'slow_target':slow,'active_normal':0,'active_slow':0,'peak_normal':0,'peak_slow':0,'normal_frames':0,'slow_bytes':0,'reconnects':0,'errors':0,'protocol_errors':0,'expected_disconnects':0,'renewal_disconnects':0,'producer_errors':0,'stable_samples':0,'window_violations':0,'last_window':None,'source':'synthetic DB event producer; real formal HTTP SSE transport; no inference timing claim'};self.connections=[];self.threads=[];self.clients={i:{'active':False,'last_progress':0,'disconnected_at':0} for i in range(normal+slow)};self.renew_until=0
    def begin_renewal(self):
        with self.lock:self.renew_until=time.monotonic()+30
    def sample_health(self,elapsed,duration,startup=15):
        now=time.monotonic()
        with self.lock:
            if elapsed<startup or elapsed>duration-1 or now<self.renew_until:return
            unhealthy=[i for i,s in self.clients.items() if now-s['last_progress']>15 or not s['active'] and now-s['disconnected_at']>5]
            self.stats['stable_samples']+=1;self.stats['window_violations']+=bool(unhealthy)
            self.stats['last_window']={'elapsed_seconds':elapsed,'unhealthy_clients':unhealthy,'progress_deadline_seconds':15,'reconnect_grace_seconds':5,'renewal_grace_seconds':30}
    def start(self):
        for i in range(self.normal+self.slow):
            thread=threading.Thread(target=self.consume,args=(i,),daemon=True);thread.start();self.threads.append(thread)
        thread=threading.Thread(target=self.produce,daemon=True);thread.start();self.threads.append(thread)
    def produce(self):
        while not self.done.is_set() and not STOP.is_set():
            try:stream_emit(self.data,self.prefix,self.large)
            except Exception:
                with self.lock:self.stats['producer_errors']+=1
            self.done.wait(1)
    def consume(self,index):
        slow=index>=self.normal;kind='slow' if slow else 'normal';u=self.data['users'][index%20];base=urlparse(self.data['base_urls'][index%2]);cursor=0
        while not self.done.is_set() and not STOP.is_set():
            u=self.data['users'][index%20]
            conn=http.client.HTTPConnection(base.hostname,base.port,timeout=5);active=False
            try:
                # A smaller socket receive buffer creates genuine TCP backpressure, not delayed application parsing alone.
                conn.connect();conn.sock.setsockopt(__import__('socket').SOL_SOCKET,__import__('socket').SO_RCVBUF,1024 if slow else 65536)
                headers={'Cookie':'aiops_session='+u['cookie'],'Last-Event-ID':str(cursor)}
                conv=self.prefix+str(index);conn.request('GET','/api/v1/conversations/'+conv+'/events?reply_id='+conv+'-reply',headers=headers)
                response=conn.getresponse()
                if response.status==403 and time.monotonic()<self.renew_until:
                    with self.lock:self.stats['renewal_disconnects']+=1
                    self.done.wait(.5)
                    continue
                if response.status!=200 or response.getheader('Content-Type')!='text/event-stream':raise SSEProtocolError('unexpected SSE HTTP status or content type')
                with self.lock:
                    self.connections.append(conn);self.stats['active_'+kind]+=1;self.stats['peak_'+kind]=max(self.stats['peak_'+kind],self.stats['active_'+kind]);self.clients[index]['active']=True
                active=True;connected=time.monotonic();parser=SSEParser(cursor)
                while not self.done.is_set() and not STOP.is_set():
                    if slow:
                        chunk=response.read(1)
                        if not chunk:break
                        with self.lock:self.stats['slow_bytes']+=1;self.clients[index]['last_progress']=time.monotonic()
                        self.done.wait(.1)
                    else:
                        line=response.readline(1<<20)
                        if not line:break
                        if parser.feed(line):
                            cursor=parser.cursor
                            with self.lock:self.stats['normal_frames']+=1;self.clients[index]['last_progress']=time.monotonic()
                    # Normal sockets reconnect to the exact persisted event ID once/minute.
                    if not slow and time.monotonic()-connected>=self.reconnect_seconds:break
                with self.lock:self.stats['reconnects']+=1;self.stats['expected_disconnects']+=1
            except (OSError,http.client.HTTPException):
                if not self.done.is_set() and not STOP.is_set():
                    with self.lock:self.stats['expected_disconnects']+=1
            except Exception:
                if not self.done.is_set() and not STOP.is_set():
                    with self.lock:self.stats['errors']+=1;self.stats['protocol_errors']+=1
            finally:
                conn.close()
                if active:
                    with self.lock:self.stats['active_'+kind]-=1;self.connections.remove(conn);self.clients[index]['active']=False;self.clients[index]['disconnected_at']=time.monotonic()
            self.done.wait(.5)
    def snapshot(self):
        with self.lock:return dict(self.stats)
    def close(self):
        self.done.set()
        with self.lock:connections=list(self.connections)
        for conn in connections:
            try:conn.sock.shutdown(2)
            except (OSError,AttributeError):pass
            conn.close()
        for thread in self.threads:thread.join(timeout=6)
        stream_finish(self.data,self.prefix)


def k6(data,run,stage,rate,duration,gate=False,streams=None,hooks=None,strict_source=True):
    started=time.monotonic();name='aiops-'+data['owner']+'-k6-'+run+'-'+stage
    work=Path(data['perf_work'])/run/stage;work.mkdir(parents=True,mode=0o700,exist_ok=True)
    # Docker reads this generated file from a private mount. Tokens never enter command arguments/env/stdout.
    private(work/'workload.json',{'base_urls':data['base_urls'],'environment':data['environment'],'users':data['users'],'password':data['perf_password'],'excluded_aggregate_paths':data.get('excluded_aggregate_paths',[]),'aggregate_measurement_supported':data.get('aggregate_measurement_supported',True),'force_relogin':stage=='self_check'})
    command=['docker','run','--rm','--name',name,'--label','aiops.testlab.owner='+data['owner'],'--user','0:0','--cpus','2','--memory','1g','-v',str(work)+':/private','-v',str(ROOT/'tools/testlab/load/traffic.js')+':/load.js:ro','-e','RATE='+str(rate),'-e','DURATION='+str(duration)+'s','-e','GATE='+('1' if gate else '0'),'-e','SELF_CHECK='+('1' if stage=='self_check' else '0'),K6_IMAGE,'run','--quiet','/load.js']
    log=open(work/'k6.log','w');os.chmod(work/'k6.log',0o600)
    proc=subprocess.Popen(command,stdout=log,stderr=log);next_metric=started;next_hook=started;next_chat=started;next_login=started+max(1,min(u['session_expires_at'] for u in data['users'])-time.time()-300);resources=[];hook_stats={'dryrun_calls':0,'webhook_calls':0,'chat_turns':0,'relogins':0,'failures':0,'chat_e2e_protocol_mock_ms_total':0,'chat_e2e_protocol_mock_ms_max':0,'model_evidence_mode':'protocol_mock'};resource_path=work/'resources.jsonl'
    resource=open(resource_path,'w');os.chmod(resource_path,0o600)
    try:
        while proc.poll() is None:
            now=time.monotonic()
            if stage=='soak24h' and 'last_heartbeat' in locals() and now-last_heartbeat>120:raise RuntimeError('soak heartbeat gap indicates suspension/interruption; continuous window invalid')
            last_heartbeat=now
            if STOP.is_set():raise KeyboardInterrupt()
            if strict_source:frozen(data)
            if streams:streams.sample_health(now-started,duration,startup=3 if stage=='self_check' else 15)
            # Detect a killed/stalled generator before accepting elapsed wall-clock time.
            if now-started>duration+120:raise RuntimeError('k6 exceeded planned duration plus bounded grace')
            if now>=next_metric:
                measure=metrics(data);resource.write(json.dumps(measure)+'\n');resource.flush();resources.append(measure);resources=resources[-3:];next_metric=now+30
            if hooks and now>=next_hook:
                for key in ('dryrun','webhook'):
                    h=hooks[key];hook_user=data['hook_user'] if h.get('account')=='admin' else data['users'][0]
                    status,body,_=request(data,data['base_urls'][0],h.get('method','POST'),h['path'],hook_user,hook_body(h.get('body')),h.get('headers'))
                    hook_stats[key+'_calls']+=1
                    if status!=h['expected_status'] or body.get('error'):hook_stats['failures']+=1
                next_hook=now+hooks.get('interval_seconds',60)
            if hooks and now>=next_chat:
                chat_started=time.monotonic()
                u=data['users'][0];base=data['base_urls'][0];status,body,_=request(data,base,'POST','/api/v1/conversations',u,{'text':'Testlab continuous chat','model':data['model_id']})
                if status!=201:hook_stats['failures']+=1
                else:
                    status,turn,_=request(data,base,'POST','/api/v1/conversations/'+body['id']+'/messages',u,{'text':'Controlled protocol response','model':data['model_id']})
                    if status!=202:hook_stats['failures']+=1
                    else:
                        # Real reply stream and cursor reconnect: read all bounded events, then reconnect at terminal cursor.
                        b=urlparse(base);c=http.client.HTTPConnection(b.hostname,b.port,timeout=30);path='/api/v1/conversations/'+body['id']+'/events?reply_id='+turn['reply_id'];c.request('GET',path,headers={'Cookie':'aiops_session='+u['cookie']});reply=c.getresponse();raw=reply.read(1<<20);c.close()
                        ids=[int(line[3:].strip()) for line in raw.splitlines() if line.startswith(b'id:')]
                        if reply.status!=200 or not ids or ids!=sorted(set(ids)) or b'message.completed' not in raw:hook_stats['failures']+=1
                        else:
                            c=http.client.HTTPConnection(b.hostname,b.port,timeout=10);c.request('GET',path,headers={'Cookie':'aiops_session='+u['cookie'],'Last-Event-ID':str(ids[-1])});replay=c.getresponse();empty=replay.read(1<<20);c.close()
                            if replay.status!=200 or empty:hook_stats['failures']+=1
                        hook_stats['chat_turns']+=1
                        chat_ms=(time.monotonic()-chat_started)*1000;hook_stats['chat_e2e_protocol_mock_ms_total']+=chat_ms;hook_stats['chat_e2e_protocol_mock_ms_max']=max(hook_stats['chat_e2e_protocol_mock_ms_max'],chat_ms)
                next_chat=now+60
            if hooks and now>=next_login:
                if streams:streams.begin_renewal()
                data['users']=[login(data,i) for i in range(20)];hook_stats['relogins']+=20;next_login=now+max(1,min(u['session_expires_at'] for u in data['users'])-time.time()-300)
                if 'hook_user' in data:hooks_config(data);hook_stats['relogins']+=1
                # Active SSE replies must remain bound to a current session, not the expired initial session.
                if streams:
                    p=quote(streams.prefix)
                    for i,u in enumerate(data['users']):
                        session=__import__('hashlib').sha256(u['cookie'].encode()).hexdigest()
                        sql(data,f"UPDATE v3_conversation_replies r SET initiating_session_hash={quote(session)} FROM v3_conversations c WHERE c.id=r.conversation_id AND starts_with(c.id,{p}) AND c.owner_id={quote(u['id'])} AND r.state='running';")
                    streams.data['users']=data['users']
            result('perf.'+stage,'running',{'stage':stage,'rate_rps':rate,'elapsed_seconds':round(now-started,1),'planned_seconds':duration,'source_sha':data['source_tree_sha'],'sse':streams.snapshot() if streams else None,'hooks':hook_stats,'resources':resources[-1] if resources else None},started)
            STOP.wait(2 if stage=='self_check' else 10)
        elapsed=time.monotonic()-started
        summary_path=work/'summary.json'
        summary=json.loads(summary_path.read_text()) if summary_path.exists() else None
        if summary is None:raise RuntimeError('k6 exited without real summary')
        generator_seconds=float(summary.get('state',{}).get('testRunDurationMs',0))/1000
        iterations=float(summary.get('metrics',{}).get('iterations',{}).get('values',{}).get('count',0))
        dropped=float(summary.get('metrics',{}).get('dropped_iterations',{}).get('values',{}).get('count',0));error_rate=float(summary.get('metrics',{}).get('unexpected_errors',{}).get('values',{}).get('rate',0))
        resource_samples=[json.loads(line) for line in resource_path.read_text().splitlines() if line.strip()]
        actual={**resource_summary(resource_samples),'stage':stage,'profile':data.get('counts',{}).get('profile'),'measurement_only':not gate and stage!='self_check','saturated':dropped>0 or error_rate>=0.001,'unexpected_error_rate':error_rate,'dropped_iterations':dropped,'generator_seconds':generator_seconds,'iterations':iterations,'minimum_iterations':rate*duration-1,'rate_rps':rate,'elapsed_seconds':elapsed,'planned_seconds':duration,'k6_exit':proc.returncode,'summary':summary.get('metrics',{}),'resource_evidence':str(resource_path.relative_to(ROOT)),'resources_last':resources[-1] if resources else None,'hooks':hook_stats,'sse':streams.snapshot() if streams else None,'source_sha':data['source_tree_sha'],'k6_image':K6_IMAGE,'route_preflight':data.get('route_preflight'),'measured_routes':[r['path'] for r in data.get('route_preflight',[]) if r['measured']]+['own alert claim'], 'excluded_aggregate_paths':data.get('excluded_aggregate_paths',[]), 'full_aggregate_scope_accepted':not data.get('excluded_aggregate_paths'),'coverage_limit':'services seed is real metadata; service health/metrics/deployment sources are not fabricated or benchmarked'}
        if stage=='self_check':actual['renewal_plumbing']={'tool_schedule_override_only':True,'actual_product_ttl_changed':False,'k6_real_relogins':summary.get('metrics',{}).get('session_relogins',{}).get('values',{}).get('count',0),'twelve_hour_elapsed_acceptance':False}
        good=proc.returncode==0 and elapsed>=duration and generator_seconds>=duration and (iterations+dropped>=rate*duration-1 if not gate else iterations>=rate*duration-1) and not hook_stats['failures'] and (streams is None or sse_accepted(actual['sse'])) and (stage!='self_check' or actual['renewal_plumbing']['k6_real_relogins']>0)
        result('perf.'+stage,'passed' if good else 'failed',actual,started,expected='真实完整时长、无遗漏iteration；25RPS阶段执行计划延迟门槛' if gate else '真实完整时长，报告饱和点与非预期错误')
        return good,actual
    except KeyboardInterrupt:
        own_remove(name,data['owner']);proc.wait(timeout=30);result('perf.'+stage,'interrupted',{'elapsed_seconds':time.monotonic()-started,'planned_seconds':duration,'acceptance':False},started);raise
    except Exception as error:
        try:diagnostic=stage_error_evidence(data,work,error,resources[-1] if resources else None)
        except Exception as diagnostic_error:diagnostic={'diagnostic_error_type':type(diagnostic_error).__name__,'diagnostic_missing':['stage_error_evidence']}
        own_remove(name,data['owner']);proc.wait(timeout=30)
        result('perf.'+stage,'failed',{'error':type(error).__name__,'elapsed_seconds':time.monotonic()-started,'planned_seconds':duration,'continuous_acceptance':False,**diagnostic},started)
        raise
    finally:resource.close();log.close()


def resource_recovery(data,baseline,started):
    # Stop producers/sockets before this call; recovery remains inside the frozen window.
    deadline=time.monotonic()+60;samples=[]
    while time.monotonic()<deadline:
        if STOP.is_set():raise KeyboardInterrupt()
        frozen(data);samples.append(metrics(data));STOP.wait(10)
    final=samples[-1];missing=sorted(set(baseline.get('missing',[])+final.get('missing',[])))
    checks={};b=baseline.get('postgres');v=final.get('postgres')
    if b is not None and v is not None:
        for name in ('queued_replies','running_replies','locks','waiting'):
            checks[name]={'before':b[name],'after':v[name],'limit':b[name],'passed':v[name]<=b[name]}
        checks['connections']={'before':b['connections'],'after':v['connections'],'limit':b['connections']+20,'passed':v['connections']<=b['connections']+20}
    before={x['pid']:x['open_fds'] for x in baseline.get('backend_fds',[])}
    for x in final.get('backend_fds',[]):
        if x['pid'] in before:checks['fd_'+str(x['pid'])]={'before':before[x['pid']],'after':x['open_fds'],'limit':before[x['pid']]+10,'passed':x['open_fds']<=before[x['pid']]+10}
    required_missing=[x for x in missing if x in ('postgres_connections_locks_queue','backend_fd')]
    good=bool(checks) and not required_missing and all(x['passed'] for x in checks.values())
    actual={'resource_recovery':{'quiet_seconds':60,'checks':checks,'missing':missing,'rss_boundary':'RSS growth is reported as actual samples; no leak conclusion from cache growth alone'},'resource_samples':[baseline]+samples}
    result('perf.resource_recovery','blocked' if required_missing else ('passed' if good else 'failed'),actual,started,expected='负载停止60s：队列/锁回基线，连接<=基线+20，每PID FD<=基线+10；缺失不按0')
    return good,actual['resource_recovery']


def consistency(data):
    p=quote(data['counts']['prefix']);v=json.loads(sql(data,f"SELECT json_build_object('services',(SELECT count(*) FROM applications WHERE starts_with(id,{p}||'app-')),'alerts',(SELECT count(*) FROM ops_incidents WHERE starts_with(id,{p}||'alert-')),'messages',(SELECT count(*) FROM v3_conversation_messages WHERE starts_with(id,{p}||'msg-')),'orphan_alerts',(SELECT count(*) FROM ops_incidents i LEFT JOIN applications a ON a.id=i.application_id LEFT JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id WHERE starts_with(i.id,{p}||'alert-') AND (a.id IS NULL OR e.id IS NULL)),'orphan_messages',(SELECT count(*) FROM v3_conversation_messages m LEFT JOIN v3_conversations c ON c.id=m.conversation_id WHERE starts_with(m.id,{p}||'msg-') AND c.id IS NULL));"))
    good=not v['orphan_alerts'] and not v['orphan_messages'] and all(v[k]>=data['counts']['actual'][k] for k in ('services','alerts','messages'))
    result('perf.data_consistency','passed' if good else 'failed',v,expected='负载后真实APP seed行不丢失、告警应用/环境引用及消息父对象一致')
    return good


def hook_body(value):
    # A small explicit template contract creates unique real webhook deliveries.
    replacements={'${TESTLAB_NOW_RFC3339}':__import__('datetime').datetime.now(__import__('datetime').timezone.utc).isoformat(),'${TESTLAB_EVENT_ID}':uuid.uuid4().hex}
    def visit(v):
        if isinstance(v,str):
            for key,replacement in replacements.items():v=v.replace(key,replacement)
            return v
        if isinstance(v,list):return [visit(item) for item in v]
        if isinstance(v,dict):return {key:visit(item) for key,item in v.items()}
        return v
    return visit(value)


def start_trial(data,hooks,run):
    d=hooks['dryrun'];headers=dict(d.get('headers',{}));headers['Idempotency-Key']='testlab-soak-'+run
    status,body,_=request(data,data['base_urls'][0],d.get('method','POST'),d['path'],data['hook_user'],d.get('body'),headers)
    identifier=body.get('trial_id','')
    if status!=202 or not re.fullmatch(r'[a-f0-9]{32}',identifier):raise RuntimeError('fresh formal soak trial was not accepted; no historical trial reused')
    data['_soak_trial']={'id':identifier,'key':headers['Idempotency-Key']}
    hooks['dryrun']['headers']=headers
    hooks['trial_status_path']=hooks['trial_status_path'].replace('${TESTLAB_TRIAL_ID}',identifier)
    for _ in range(30):
        status,trial,_=request(data,data['base_urls'][0],'GET',hooks['trial_status_path'],data['hook_user'])
        if status==200 and trial.get('status')=='running' and trial.get('evaluated_points',0)>0:
            data['_soak_trial'].update(started_at=trial['started_at'],expires_at=trial['expires_at'])
            result('perf.trial_start','running',{'trial_id':identifier,'fresh_idempotency_key':True,'started_at':trial['started_at'],'expires_at':trial['expires_at'],'evaluated_points':trial['evaluated_points'],'missing_points':trial.get('missing_points'),'planned_seconds':86400},type='soak')
            return
        if status!=200 or trial.get('status') in ('stopped','stalled','inconclusive','completed'):raise RuntimeError('new trial did not enter actual running evaluator')
        if STOP.wait(1):raise KeyboardInterrupt()
    raise RuntimeError('fresh trial worker start timeout')


def finish_trial(data,hooks,load_start,load_end):
    deadline=time.monotonic()+120
    while True:
        frozen(data)
        status,body,_=request(data,data['base_urls'][0],'GET',hooks['trial_status_path'],data['hook_user']);trial=body.get('trial',body)
        state=trial.get('status')
        if state not in ('queued','running') or time.monotonic()>=deadline:break
        result('perf.trial_finish','running',{'trial_id':trial.get('trial_id'),'status':state,'evaluated_points':trial.get('evaluated_points'),'missing_points':trial.get('missing_points'),'load_finished_at':load_end,'bounded_wait_seconds':120},type='soak')
        if STOP.wait(10):raise KeyboardInterrupt()
    start=datetime.datetime.fromisoformat(trial['started_at']).timestamp() if trial.get('started_at') else 0
    end=datetime.datetime.fromisoformat(trial['expires_at']).timestamp() if trial.get('expires_at') else 0
    good=status==200 and state=='completed' and trial.get('evaluated_points')==1440 and trial.get('missing_points')==0 and 0<=load_start-start<=60 and end-start==86400
    return good,{'http_status':status,'status':state,'trial_id':trial.get('trial_id'),'evaluated_points':trial.get('evaluated_points'),'missing_points':trial.get('missing_points'),'trial_started_at':trial.get('started_at'),'trial_expires_at':trial.get('expires_at'),'load_started_at':load_start,'load_finished_at':load_end,'trial_window_seconds':end-start,'overlap_seconds':max(0,min(end,load_end)-max(start,load_start)),'start_alignment_seconds':load_start-start,'accepted':good}


def hooks_config(data):
    path=Path(data['perf_work'])/'soak-hooks.json'
    if not path.exists():raise RuntimeError('private perf/soak-hooks.json required: actual dryrun+webhook+trial_status_path; no protocol fallback')
    if path.is_symlink() or path.stat().st_mode&0o777!=0o600:raise RuntimeError('soak hooks must be generated private 0600 regular file')
    h=json.loads(path.read_text())
    for key in ('dryrun','webhook'):
        v=h.get(key,{})
        if not str(v.get('path','')).startswith('/api/v1/') or not isinstance(v.get('expected_status'),int):raise RuntimeError('actual '+key+' API contract missing')
    if '${TESTLAB_TRIAL_ID}' not in h.get('trial_status_path','') or h.get('trial_expected_statuses')!=['completed']:raise RuntimeError('fresh trial status template and completed-only terminal contract required')
    if any(h[k].get('account')=='admin' for k in ('dryrun','webhook')):
        status,body,headers=request(data,data['base_urls'][0],'POST','/api/v1/auth/login',body=data['admin'],headers={'X-Forwarded-For':'192.0.2.241'})
        token=[v.split(';',1)[0].split('=',1)[1] for k,v in headers if k.lower()=='set-cookie' and v.startswith('aiops_session=')]
        if status!=200 or len(token)!=1:raise RuntimeError('owned admin login unavailable for actual trial hooks')
        data['hook_user']={'id':body['user']['id'],'cookie':token[0]}
    return h

def main():
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['prepare','self-check','load','soak']);parser.add_argument('--profile',choices=['base','scale'],default='base');parser.add_argument('--duration',type=int,default=86400);a=parser.parse_args();started=time.monotonic();streams=None
    signal.signal(signal.SIGINT,lambda *args:STOP.set());signal.signal(signal.SIGTERM,lambda *args:STOP.set())
    try:
        if a.command in ('load','soak'):
            guard=fixture();frozen(guard)
            if a.command=='soak':
                if a.duration!=86400:raise RuntimeError('formal soak requires exactly 86400 seconds')
                hooks_config(guard)
        data=prepare('check' if a.command=='self-check' else a.profile);result('perf.seed','passed',data['counts'],started,expected='专用APP DB真实schema数据+20受权用户、提交后count读回')
        if a.command=='prepare':return 0
        if a.command in ('load','soak'):
            frozen(data)
            if not data['counts']['exact_profile']:raise RuntimeError('owned DB already contains a larger profile; requested base gate is invalid; use scale or a fresh owned environment')
        # Non-race formal binary verified by backend startup record, never rebuild here.
        version=subprocess.run(['docker','run','--rm',K6_IMAGE,'version'],capture_output=True,text=True,timeout=30)
        if version.returncode or 'v1.5.0' not in version.stdout:raise RuntimeError('pinned k6 1.5.0 not available')
        run=uuid.uuid4().hex[:12]
        if a.command=='soak':
            if a.duration!=86400:raise RuntimeError('formal soak requires exactly 86400 seconds')
            hooks=hooks_config(data)
            streams=Streams(data,run,normal=2,slow=1,large=False)
        else:streams=Streams(data,run,reconnect_seconds=5 if a.command=='self-check' else 60)
        resource_baseline=metrics(data)
        streams.start()
        if a.command=='self-check':
            good,actual=k6(data,run,'self_check',3,12,streams=streams,strict_source=False)
            good=good and actual['sse']['reconnects']>0 and actual['sse']['peak_normal']==50 and actual['sse']['peak_slow']==10 and sse_accepted(actual['sse'])
            result('perf.self_check_scope','passed' if good else 'failed',{'formal_load_acceptance':False,'formal_24h_acceptance':False,'seed_profile':'check','scope':'12 seconds plumbing and cursor reconnect only; no compressed gate'},started)
            return 0 if good else 1
        if a.command=='load':
            good=True
            for stage,rate,seconds,gate in [('warmup',10,120,False),('rps10',10,300,False),('rps25',25,300,True),('rps50',50,300,False),('burst100',100,60,False)]:
                passed,_=k6(data,run,stage,rate,seconds,gate and a.profile=='base',streams);good=good and passed
            stats=streams.snapshot();sse_good=stats['peak_normal']==50 and stats['peak_slow']==10 and sse_accepted(stats)
            result('perf.sse','passed' if sse_good else 'failed',stats,started,expected='50正常/10真实慢读socket，数据库样本→正式SSE，重连cursor单调')
            streams.close();streams=None
            recovered,_=resource_recovery(data,resource_baseline,started)
            consistent=consistency(data)
            return 0 if good and sse_good and recovered and consistent else 1
        if a.duration!=86400:raise RuntimeError('formal soak duration must be exactly 86400 seconds; use self-check for plumbing')
        start_trial(data,hooks,run)
        load_start=time.time()
        good,actual=k6(data,run,'soak24h',10,86400,True,streams,hooks)
        load_end=time.time()
        trial_good,actual['trial_final']=finish_trial(data,hooks,load_start,load_end)
        good=good and trial_good and actual['generator_seconds']>=86400 and actual['hooks']['dryrun_calls']>0 and actual['hooks']['webhook_calls']>0 and actual['hooks']['chat_turns']>0 and actual['hooks']['relogins']>0
        stats=streams.snapshot();good=good and stats['peak_normal']==2 and stats['peak_slow']==1 and stats['reconnects']>0 and sse_accepted(stats) and consistency(data)
        streams.close();streams=None
        recovered,actual['resource_recovery']=resource_recovery(data,resource_baseline,started);good=good and recovered
        result('perf.soak_acceptance','passed' if good else 'failed',actual,started,type='soak',gap_ids=[7,12,40,41,42,43],expected='同一冻结源码连续真实86400秒+10RPS+实际trial/webhook+对话重连+续登；中断不得累计')
        data['_soak_trial_accepted']=good
        return 0 if good else 1
    except KeyboardInterrupt:
        result('perf.interrupted','interrupted',{'continuous_acceptance':False,'elapsed_seconds':time.monotonic()-started},started);return 130
    except Exception as e:
        result('perf.environment','blocked',{'error':type(e).__name__+': '+str(e),'acceptance':False},started);return 2
    finally:
        if 'data' in locals() and data.get('_soak_trial') and not data.get('_soak_trial_accepted'):
            own=data['_soak_trial']
            try:sql(data,"UPDATE v3_alert_rule_trials SET status='stopped',lease_token='',lease_until=NULL,updated_at=clock_timestamp() WHERE id="+quote(own['id'])+" AND idempotency_key="+quote(own['key'])+" AND status IN ('dispatch_pending','queued','running');")
            except Exception:result('perf.trial_cleanup','failed','only own unfinished soak trial could not be stopped; retained for diagnosis')
        if streams:
            try:streams.close()
            except Exception:result('perf.sse_cleanup','failed','owned SSE producer cleanup failed; rows retained for evidence')

if __name__=='__main__':sys.exit(main())
