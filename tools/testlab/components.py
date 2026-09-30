"""Prepare and verify owned local components; never loads product credentials.

Every Docker mutation validates the testlab owner. Public output contains only
case results and credential-free URLs. Generated config/credentials stay private.
"""
from __future__ import annotations
import argparse, base64, hashlib, hmac, json, os, re, secrets, shutil, signal, smtplib, socket, ssl, subprocess, sys, time, urllib.parse, urllib.request
from pathlib import Path
from env import _private

LABEL='aiops.testlab.owner'
IMAGES={'prometheus':'prom/prometheus:v3.13.3','alertmanager':'prom/alertmanager:v0.34.1','vault':'hashicorp/vault:1.20.4','loki':'grafana/loki:3.7.0','tempo':'grafana/tempo:2.7.2','otel':'otel/opentelemetry-collector-contrib:0.157.0','keycloak':'quay.io/keycloak/keycloak:26.7.4'}
PORTS={'prometheus_tls':18569,'prometheus':18570,'alertmanager':18571,'vault':18572,'loki':18573,'tempo':18574,'otel':18575,'keycloak':18576,'smtp':18577,'fixtures':18578,'fixture_tls':18579}

def cmd(argv,timeout=90):
    p=subprocess.run(argv,text=True,capture_output=True,timeout=timeout)
    if p.returncode:raise RuntimeError(f'{argv[0]} failed ({p.returncode}); inspect owned component logs')
    return p.stdout.strip()

def request(url,payload=None,headers=None,context=None):
    raw=json.dumps(payload).encode() if payload is not None else None
    req=urllib.request.Request(url,raw,{'Content-Type':'application/json',**(headers or {})})
    with urllib.request.urlopen(req,timeout=5,context=context) as r:
        body=r.read(2*1024*1024)
        if len(body)>=2*1024*1024:raise RuntimeError('component response exceeds budget')
        return json.loads(body) if body and body[:1] in (b'{',b'[') else body.decode()

def owned(root, *, allow_pg_absent=False):
    root=Path(root).resolve();e=json.loads((root/'.local/testlab/environment.json').read_text())
    if not re.fullmatch(r'testlab-[a-z0-9]+',e.get('owner','')):raise RuntimeError('invalid owner')
    work=Path(e['work']).resolve()
    if not work.is_relative_to(root/'.local/testlab/environments') or work.name!=e['owner']:raise RuntimeError('work ownership mismatch')
    # A removed PG must not prevent reclaiming separately owned components.
    # Require a live daemon so an inspection failure is never mistaken for absence.
    cmd(['docker','info','--format','{{.ID}}'],timeout=15)
    pg=inspect(e['container'])
    if pg is None and not allow_pg_absent:raise RuntimeError('owned PG is absent')
    if pg is not None and pg['Config'].get('Labels',{}).get(LABEL)!=e['owner']:raise RuntimeError('PG ownership mismatch')
    return root,e,work

def name(owner,component):return 'aiops-'+owner+'-'+component

def inspect(container):
    p=subprocess.run(['docker','inspect',container],text=True,capture_output=True,timeout=15)
    return json.loads(p.stdout)[0] if p.returncode==0 else None

def verify_owner(info,owner):
    if info.get('Config',{}).get('Labels',{}).get(LABEL)!=owner:raise RuntimeError('refusing unowned container')

def port_free(port):
    with socket.socket() as s:
        if s.connect_ex(('127.0.0.1',port))==0:raise RuntimeError(f'dedicated component port {port} is occupied')

def wait_http(url,seconds=80):
    end=time.monotonic()+seconds
    while time.monotonic()<end:
        try:return request(url)
        except Exception:time.sleep(1)
    raise RuntimeError('owned component did not become ready')

def fixture_alive(pid,owner,work):
    try:
        challenge=secrets.token_hex(24)
        record=request('http://127.0.0.1:18578/health?challenge='+challenge)
        token=(Path(work)/'fixture-owner-token').read_text().strip()
        expected=hmac.new(token.encode(),f'{challenge}|{pid}|{owner}'.encode(),hashlib.sha256).hexdigest()
        return record.get('pid')==pid and record.get('owner')==owner and hmac.compare_digest(record.get('proof',''),expected)
    except Exception:return False

def prepare(root):
    root,e,work=owned(root);owner=e['owner'];state_path=work/'component-environment.json'
    prior=json.loads(state_path.read_text()) if state_path.exists() else {}
    network=name(owner,'components')
    existing=subprocess.run(['docker','network','inspect',network],text=True,capture_output=True,timeout=15)
    if existing.returncode==0:
        if json.loads(existing.stdout)[0].get('Labels',{}).get(LABEL)!=owner:raise RuntimeError('network ownership mismatch')
    else:cmd(['docker','network','create','--label',f'{LABEL}={owner}',network])
    for image in IMAGES.values():cmd(['docker','image','inspect',image],timeout=15)
    work.mkdir(parents=True,exist_ok=True,mode=0o700)
    cert,key=work/'tls-cert.pem',work/'tls-key.pem'
    if not cert.exists():
        cmd(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','2','-keyout',str(key),'-out',str(cert),'-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,IP:127.0.0.1'])
        os.chmod(key,0o600);os.chmod(cert,0o600)
    password_file=work/'smtp-password'
    if not password_file.exists():password_file.write_text(secrets.token_hex(20));os.chmod(password_file,0o600)
    pid=prior.get('fixture_pid')
    fixture_sha=hashlib.sha256((root/'tools/testlab/fixtures.py').read_bytes()).hexdigest()
    if pid and fixture_alive(pid,owner,work) and prior.get('fixture_sha')!=fixture_sha:
        nonce=secrets.token_hex(24);token=(work/'fixture-owner-token').read_text().strip()
        proof=hmac.new(token.encode(),('shutdown|'+nonce+'|'+owner).encode(),hashlib.sha256).hexdigest()
        request('http://127.0.0.1:18578/admin/shutdown',{'nonce':nonce,'proof':proof})
        for _ in range(30):
            try:port_free(PORTS['fixtures']);break
            except RuntimeError:time.sleep(.1)
        pid=None
    token_file=work/'fixture-owner-token'
    if not token_file.exists():token_file.write_text(secrets.token_hex(32));os.chmod(token_file,0o600)
    if not pid or not fixture_alive(pid,owner,work):
        for k in ('smtp','fixtures','fixture_tls','prometheus_tls'):port_free(PORTS[k])
        log=open(work/'fixture-server.log','ab',buffering=0)
        proc=subprocess.Popen([sys.executable,str(root/'tools/testlab/fixtures.py'),'serve','--work',str(work),'--owner',owner],stdout=log,stderr=log,start_new_session=True)
        pid=proc.pid;log.close()
    data={'owner':owner,'network':network,'work':str(work),'fixture_pid':pid,'fixture_sha':fixture_sha,'services':[],'env':{'SSL_CERT_FILE':str(cert)},'credentials':{'smtp_password_file':str(password_file)},'host_config':{}}
    # Save cleanup ownership before each optional component starts.
    _private(state_path,data)
    wait_http('http://127.0.0.1:18578/health',10)
    fixture_services=[{'name':k,'url':f'{"https" if k=="fixture_tls" else "http"}://127.0.0.1:{PORTS[k]}','mode':'real_local' if k=='smtp' else 'protocol_mock'} for k in ('fixtures','fixture_tls','smtp')]
    data['services'].extend(fixture_services)
    vault_token=prior.get('credentials',{}).get('vault_token') or secrets.token_hex(24)
    kcpassword=prior.get('credentials',{}).get('keycloak_password') or secrets.token_hex(20)
    data['credentials'].update(vault_token=vault_token,keycloak_password=kcpassword)
    realm={'realm':'aiops-testlab','enabled':True,'sslRequired':'none','clients':[{'clientId':'aiops-oidc-smoke','enabled':True,'publicClient':True,'directAccessGrantsEnabled':True,'standardFlowEnabled':True,'redirectUris':['http://127.0.0.1:18089/api/v1/auth/oidc/callback']}],'users':[{'username':'testlab-user','enabled':True,'firstName':'Testlab','lastName':'Only','email':'testlab@example.invalid','emailVerified':True,'credentials':[{'type':'password','value':kcpassword,'temporary':False}]}]}
    _private(work/'realm.json',realm)
    for component,image in IMAGES.items():
        cname=name(owner,component);info=inspect(cname)
        if info:
            verify_owner(info,owner)
            if info['Config']['Image']!=image:raise RuntimeError('owned image changed')
            if not info['State']['Running']:cmd(['docker','start',cname])
        else:
            port_free(PORTS[component]);internal={'prometheus':9090,'alertmanager':9093,'vault':8200,'loki':3100,'tempo':3200,'otel':4318,'keycloak':8080}[component]
            argv=['docker','run','--pull=never','-d','--name',cname,'--label',f'{LABEL}={owner}','--network',network,'--network-alias',component,'--memory','1g' if component=='keycloak' else '512m','--cpus','1','-p',f'127.0.0.1:{PORTS[component]}:{internal}']
            folder=root/'tools/testlab/components'
            if component=='prometheus':argv+=['-v',f'{folder}/prometheus.yml:/etc/prometheus/prometheus.yml:ro','-v',f'{folder}/testlab-rules.yml:/etc/prometheus/testlab-rules.yml:ro']
            elif component=='alertmanager':argv+=['-v',f'{folder}/alertmanager.yml:/etc/alertmanager/alertmanager.yml:ro']
            elif component in ('loki','tempo','otel'):argv+=['-v',f'{folder}/{component}.yml:/etc/testlab-config.yml:ro']
            elif component=='vault':
                envfile=work/'vault.env';envfile.write_text('VAULT_DEV_ROOT_TOKEN_ID='+vault_token+'\nVAULT_DEV_LISTEN_ADDRESS=0.0.0.0:8200\n');os.chmod(envfile,0o600);argv+=['--env-file',str(envfile),'--cap-add','IPC_LOCK']
            elif component=='keycloak':argv+=['-v',f'{work}/realm.json:/opt/keycloak/data/import/realm.json:ro']
            argv.append(image)
            if component=='loki':argv+=['-config.file=/etc/testlab-config.yml']
            if component=='tempo':argv+=['-config.file=/etc/testlab-config.yml']
            if component=='otel':argv+=['--config=/etc/testlab-config.yml']
            if component=='vault':argv+=['server','-dev']
            if component=='keycloak':argv+=['start-dev','--import-realm','--hostname-strict=false']
            cmd(argv,timeout=120)
        data['services'].append({'name':component,'container':cname,'image':image,'url':f'http://127.0.0.1:{PORTS[component]}','mode':'real_local'})
        _private(state_path,data)
    data['env'].update(AIOPS_OIDC_TEST_ISSUER='http://127.0.0.1:18576/realms/aiops-testlab',AIOPS_OIDC_TEST_USERNAME='testlab-user',AIOPS_OIDC_TEST_PASSWORD=kcpassword)
    data['host_config']={'application_id':'testlab','environment_id':'test','service':'testlab-api','prometheus_url':'http://127.0.0.1:18570','prometheus_tls_url':'https://localhost:18569','alertmanager_url':'http://127.0.0.1:18571','vault_url':'http://127.0.0.1:18572','loki_url':'http://127.0.0.1:18573','tempo_url':'http://127.0.0.1:18574','otlp_http_url':'http://127.0.0.1:18575/v1/traces','smtp_host':'localhost','smtp_port':18577,'fixture_https_url':'https://localhost:18579','ca_file':str(cert),'loki_labels':{'application':'testlab','environment':'test'},'loki_pattern_field':'message_template','loki_complete_lookback_seconds':86400,'tempo_search_fail_closed':False,'promql':'testlab_requests_total{application="testlab",environment="test"}','limits':'local loopback protocol/data evidence; no application isolation or seven-day coverage attestation'}
    _private(state_path,data);_private(root/'.local/testlab/component-environment.json',data)
    return data,e

def emit(id,name,status,expected,actual,duration,module='components',mode='real_local',gaps=()):
    print(json.dumps({'id':id,'name':name,'status':status,'expected':expected,'actual':actual,'duration':round(duration,3),'module':module,'type':'integration','evidence_mode':mode,'gap_ids':list(gaps)},ensure_ascii=False),flush=True)

def verify(root):
    started=time.monotonic()
    try:data,e=prepare(root)
    except Exception as error:
        emit('components.prepare','独立本地组件搭建','blocked','受label约束的专用组件启动',str(error),time.monotonic()-started);return 2
    work=Path(data['work']);owner=data['owner'];failures=0;run_signal={}
    def case(id,title,fn,expected,mode='real_local',gaps=()):
        nonlocal failures
        start=time.monotonic()
        try:actual=fn();status='passed'
        except Exception as error:actual=f'{type(error).__name__}: {error}';status='failed' if isinstance(error,AssertionError) else 'blocked';failures+=1
        emit(id,title,status,expected,actual,time.monotonic()-start,mode=mode,gaps=gaps)
    def prometheus():
        wait_http('http://127.0.0.1:18570/-/ready')
        run_signal.update(request('http://127.0.0.1:18578/signal',{}))
        query=urllib.parse.urlencode({'query':'testlab_request_failures_total{application="testlab",environment="test"} > 0'})
        end=time.monotonic()+45
        while time.monotonic()<end:
            result=request('http://127.0.0.1:18570/api/v1/query?'+query)
            if result.get('status')=='success' and any(x['metric'].get('signal_id')==run_signal['signal_id'] for x in result['data']['result']):return {'series':len(result['data']['result']),'application':'testlab','environment':'test','source':'actual Prometheus scrape'}
            time.sleep(2)
        raise RuntimeError('business scrape did not produce the labelled failure series')
    def alertmanager():
        wait_http('http://127.0.0.1:18571/-/ready')
        end=time.monotonic()+45
        while time.monotonic()<end:
            captures=request('http://127.0.0.1:18578/captures')['items']
            matched=[x for x in captures if x['kind']=='alertmanager' and any(a.get('labels',{}).get('alertname')=='TestlabRequestFailures' and a.get('labels',{}).get('signal_id')==run_signal.get('signal_id') for a in x['payload'].get('alerts',[]))]
            if matched:return {'captured_webhooks':len(matched),'current_signal_id':run_signal['signal_id'],'source':'actual Prometheus rule → Alertmanager → local webhook'}
            time.sleep(2)
        raise RuntimeError('Alertmanager did not deliver the actual Prometheus alert')
    def vault():
        wait_http('http://127.0.0.1:18572/v1/sys/health')
        token=data['credentials']['vault_token'];headers={'X-Vault-Token':token}
        marker=secrets.token_hex(12)
        request('http://127.0.0.1:18572/v1/secret/data/testlab-connector',{'data':{'token':marker}},headers)
        record=request('http://127.0.0.1:18572/v1/secret/data/testlab-connector',headers=headers)
        assert record['data']['data']['token']==marker,'Vault KV v2 mismatch'
        try:request('http://127.0.0.1:18572/v1/secret/data/testlab-connector',headers={'X-Vault-Token':'invalid-local-test-token'})
        except urllib.error.HTTPError as error:
            assert error.code==403,'unexpected unauthenticated status'
        else:raise AssertionError('Vault accepted untrusted token')
        return {'kv_version':2,'roundtrip':True,'invalid_token_rejected':True,'scope':'dedicated dev Vault only'}
    def loki():
        wait_http('http://127.0.0.1:18573/ready')
        start=time.time_ns();record=json.dumps({'level':'error','message_template':'testlab request failed','message':'Controlled synthetic local error','owner':owner})
        request('http://127.0.0.1:18573/loki/api/v1/push',{'streams':[{'stream':{'application':'testlab','environment':'test','service':'testlab-api','testlab_owner':owner},'values':[[str(start),record]]}]})
        query=urllib.parse.urlencode({'query':'{application="testlab",environment="test",testlab_owner="'+owner+'"}','start':str(start-1000000000),'end':str(start+1000000000)})
        result=request('http://127.0.0.1:18573/loki/api/v1/query_range?'+query)
        rows=[v for stream in result['data']['result'] for v in stream['values']]
        assert any(json.loads(v[1]).get('owner')==owner for v in rows),'Loki lost exact record'
        return {'stored_rows':len(rows),'level_and_template':True,'coverage':'only generated samples, not historical completeness'}
    def tempo():
        wait_http('http://127.0.0.1:18574/ready')
        trace=secrets.token_hex(16);span=secrets.token_hex(8);begin=time.time_ns()
        payload={'resourceSpans':[{'resource':{'attributes':[{'key':'service.name','value':{'stringValue':'testlab-api'}},{'key':'application','value':{'stringValue':'testlab'}},{'key':'environment','value':{'stringValue':'test'}}]},'scopeSpans':[{'scope':{'name':'testlab-real-local'},'spans':[{'traceId':trace,'spanId':span,'name':'GET /controlled-signal','kind':2,'startTimeUnixNano':str(begin),'endTimeUnixNano':str(begin+10000000),'attributes':[{'key':'http.request.method','value':{'stringValue':'GET'}},{'key':'http.route','value':{'stringValue':'/controlled-signal'}}],'status':{'code':1}}]}]}]}
        request('http://127.0.0.1:18575/v1/traces',payload)
        end=time.monotonic()+30
        while time.monotonic()<end:
            try:
                result=request('http://127.0.0.1:18574/api/traces/'+trace)
                if base64.b64encode(bytes.fromhex(span)).decode() in json.dumps(result):return {'trace_id':trace,'span_id':span,'source':'actual OTLP HTTP → OTel collector → Tempo storage/readback'}
            except Exception:pass
            time.sleep(1)
        raise RuntimeError('trace was not read back from Tempo after collector delivery')
    def smtp():
        context=ssl.create_default_context(cafile=str(work/'tls-cert.pem'))
        marker='testlab-'+secrets.token_hex(8)
        with smtplib.SMTP_SSL('localhost',18577,context=context,timeout=10) as client:
            client.login('testlab',(work/'smtp-password').read_text().strip())
            client.sendmail('sender@example.invalid',['receiver@example.invalid'],'Subject: '+marker+'\r\n\r\nLocal capture only.\r\n')
        records=request('http://127.0.0.1:18578/captures')['items']
        assert any(x['kind']=='smtp' and marker in x['payload']['body'] for x in records),'SMTP DATA not captured'
        return {'tls_certificate_verified':True,'authenticated':True,'data_captured':True,'product_invitation_api':'not verified: production entry fixes SMTP port 465'}
    def keycloak():
        issuer=data['env']['AIOPS_OIDC_TEST_ISSUER'];wait_http(issuer+'/.well-known/openid-configuration',100)
        env=os.environ.copy();env.update(e['env']);env.update(data['env']);env['GOCACHE']=str(Path(root)/'.local/go-cache')
        p=subprocess.run(['go','test','-mod=readonly','-json','-count=1','./internal/v3identity','-run','^TestOIDCKeycloakLocalSmoke$'],cwd=root,env=env,capture_output=True,text=True,timeout=180)
        with open(work/'keycloak-go.jsonl','w') as f:f.write(p.stdout)
        os.chmod(work/'keycloak-go.jsonl',0o600)
        if p.returncode:raise RuntimeError('formal OIDC Keycloak smoke failed; inspect private keycloak-go.jsonl')
        events=[json.loads(line) for line in p.stdout.splitlines() if line.startswith('{')]
        assert any(x.get('Test')=='TestOIDCKeycloakLocalSmoke' and x.get('Action')=='pass' for x in events),'formal test did not pass'
        return {'formal_test':'TestOIDCKeycloakLocalSmoke','realm':'aiops-testlab','flow':'real Keycloak authorization code → formal registered callback → isolated PG session/audit','production_idp':'not validated'}
    case('components.prometheus','真实 Prometheus 业务指标采集',prometheus,'精确应用/环境series真实抓取入库',gaps=(8,17))
    case('components.alertmanager','真实规则到 Alertmanager 投递',alertmanager,'实际Prom规则触发并由AM投递到独立捕获器',gaps=(9,10))
    case('components.vault','真实 Vault KV v2 和认证拒绝',vault,'KV写读一致且错误token403',gaps=(15,28))
    case('components.loki','真实 Loki 日志入库与模板读取',loki,'顶层level/template和精确scope日志可读回',gaps=(18,))
    case('components.otel_tempo','真实 OTel 到 Tempo Trace',tempo,'生成Trace经Collector后Tempo精确读回',gaps=(19,))
    case('components.smtp','TLS SMTP 协议捕获',smtp,'验证证书/认证/DATA进入独立捕获台账',gaps=(5,))
    case('components.keycloak','真实 Keycloak 正式 OIDC 回调',keycloak,'正式Go测试授权码/本地Session/审计通过',gaps=(1,))
    context=ssl.create_default_context(cafile=str(work/'tls-cert.pem'))
    def fixture(path,payload=None):
        result=request('https://localhost:18579'+path,payload,context=context)
        return {'response_received':bool(result),'evidence_mode':'protocol_mock','external_tenant_or_paid_model_verified':False}
    for id,title,path,payload,gaps in [('feishu','飞书协议替身','/open-apis/contact/v3/users',None,(4,26)),('notion','Notion协议替身','/v1/pages/00000000-0000-0000-0000-000000000001/markdown',None,(26,)),('confluence','Confluence协议替身','/wiki/api/v2/spaces/1/pages',None,(26,)),('model','模型协议替身','/v1/chat/completions',{'model':'protocol_mock','messages':[{'role':'user','content':'synthetic test'}]},(6,7))]:
        case('fixtures.'+id,title,lambda path=path,payload=payload:fixture(path,payload),'本地受控协议响应；不证明真实租户或模型','protocol_mock',gaps)
    return 1 if failures else 0

def cleanup(root):
    root,e,work=owned(root,allow_pg_absent=True);owner=e['owner'];path=work/'component-environment.json'
    data=json.loads(path.read_text()) if path.exists() else {}
    if data and (data.get('owner')!=owner or Path(data.get('work','')).resolve()!=work):raise RuntimeError('component state owner/work mismatch')
    # Validate every owner before the first mutation, avoiding partial cleanup on mismatch.
    containers=[]
    for component in IMAGES:
        cname=name(owner,component);info=inspect(cname)
        if info:verify_owner(info,owner);containers.append(cname)
    network=name(owner,'components')
    network_info=subprocess.run(['docker','network','inspect',network],capture_output=True,text=True)
    if network_info.returncode==0 and json.loads(network_info.stdout)[0].get('Labels',{}).get(LABEL)!=owner:raise RuntimeError('refusing unowned network')
    pid=data.get('fixture_pid')
    if pid and not fixture_alive(pid,owner,work):
        try:record=request('http://127.0.0.1:18578/health')
        except Exception:record=None
        if record is not None:raise RuntimeError('fixture ownership proof mismatch; refusing cleanup')
    for cname in containers:cmd(['docker','rm','-f',cname])
    if pid and fixture_alive(pid,owner,work):
        nonce=secrets.token_hex(24);token=(work/'fixture-owner-token').read_text().strip()
        proof=hmac.new(token.encode(),('shutdown|'+nonce+'|'+owner).encode(),hashlib.sha256).hexdigest()
        request('http://127.0.0.1:18578/admin/shutdown',{'nonce':nonce,'proof':proof})
    network=name(owner,'components')
    info=subprocess.run(['docker','network','inspect',network],capture_output=True,text=True)
    if info.returncode==0:
        if json.loads(info.stdout)[0].get('Labels',{}).get(LABEL)!=owner:raise RuntimeError('refusing unowned network')
        cmd(['docker','network','rm',network])
    if path.exists():path.unlink()
    global_state=root/'.local/testlab/component-environment.json'
    if global_state.exists() and json.loads(global_state.read_text()).get('owner')==owner:global_state.unlink()
    return {'owner':owner,'cleaned_components':True,'evidence_retained':True,'postgres_untouched':True}

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','verify','cleanup']);p.add_argument('--root',default=str(Path(__file__).resolve().parents[2]));a=p.parse_args()
    if a.command=='verify':raise SystemExit(verify(a.root))
    try:
        if a.command=='cleanup':result=cleanup(a.root)
        else:
            data,_=prepare(a.root);result={k:v for k,v in data.items() if k not in ('env','credentials')}
        print(json.dumps(result,ensure_ascii=False))
    except Exception as error:print(json.dumps({'status':'blocked','error':str(error)},ensure_ascii=False));raise SystemExit(2)
