"""Owner-only GitLab/kind/Argo; chain merges only its independently reviewed product MR."""
from __future__ import annotations
import argparse,base64,hashlib,json,os,re,secrets,shutil,socket,ssl,subprocess,sys,time,urllib.error,urllib.request
from pathlib import Path
from load.seed import fixture,private
ROOT=Path(__file__).resolve().parents[2]
GITLAB='gitlab/gitlab-ce:19.3.2-ce.0@sha256:05453dd1d9aba27c2c487613141596868409b4d03247647f7d66cb0b36f321b8'
NODE='kindest/node:v1.35.8'
ARGO_VERSION='v3.1.9'
ARGO_MANIFEST='https://raw.githubusercontent.com/argoproj/argo-cd/'+ARGO_VERSION+'/manifests/install.yaml'
CLUSTER='aiops-backend-testlab'
LABEL='aiops.testlab.owner'
PORT=18580


def emit(id,status,actual,started=None,**extra):
    print(json.dumps(dict(id=id,name=id,status=status,module='gitops',type='integration',evidence_mode='real_local',expected='全新owner GitLab/kind/Argo；实际协议与独立审批证据；不自动合并',actual=actual,duration=0 if started is None else time.monotonic()-started,gap_ids=[36,37,40],**extra),ensure_ascii=False),flush=True)

def command(argv,timeout=120,input=None):
    p=subprocess.run(argv,input=input,capture_output=True,text=True,timeout=timeout)
    if p.returncode:raise RuntimeError(argv[0]+' failed; bounded owned command')
    return p.stdout.strip()

def resources():
    info=json.loads(command(['docker','info','--format','{{json .}}']))
    raw=command(['docker','stats','--no-stream','--format','{{json .}}'])
    units={'B':1,'KiB':1024,'MiB':1024**2,'GiB':1024**3};used=0
    for line in raw.splitlines():
        m=re.fullmatch(r'([\d.]+)(B|KiB|MiB|GiB)',json.loads(line)['MemUsage'].split('/')[0].strip())
        if m:used+=float(m[1])*units[m[2]]
    return {'cpus':info['NCPU'],'memory_gib':info['MemTotal']/1024**3,'used_gib':used/1024**3,'headroom_gib':(info['MemTotal']-used)/1024**3}

def setup_state():
    backend=fixture();work=Path(backend['perf_work']).parent/'gitops';work.mkdir(mode=0o700,exist_ok=True)
    path=ROOT/'.local/testlab/gitops-environment.json'
    if path.exists():
        data=json.loads(path.read_text())
        if data['owner']!=backend['owner'] or Path(data['work'])!=work:raise RuntimeError('GitOps fixture owner mismatch')
        return data
    data={'owner':backend['owner'],'work':str(work),'gitlab_name':'aiops-'+backend['owner']+'-gitlab','gitlab_url':'https://localhost:'+str(PORT),'gitlab_image':GITLAB,'cluster':CLUSTER,'kubeconfig':str(work/'kubeconfig'),'argo_url':'https://localhost:18581','prometheus_url':'https://localhost:18582','gitlab_root_password':secrets.token_urlsafe(36),'accounts':{'proposer':{'username':'testlab-proposer','password':secrets.token_urlsafe(36)},'reviewer':{'username':'testlab-reviewer','password':secrets.token_urlsafe(36)}},'complete_rule_enable_chain':False}
    private(path,data);return data

def save(data):private(ROOT/'.local/testlab/gitops-environment.json',data)

def owned_container(name,owner):
    p=subprocess.run(['docker','inspect',name],capture_output=True,text=True)
    if p.returncode:return None
    info=json.loads(p.stdout)[0]
    if info['Config'].get('Labels',{}).get(LABEL)!=owner:raise RuntimeError('refusing an existing container with different owner: '+name)
    return info

def free_port(port):
    with socket.socket() as s:
        if s.connect_ex(('127.0.0.1',port))==0:raise RuntimeError('dedicated port occupied: '+str(port))

def cert(data):
    work=Path(data['work']);crt=work/'tls.crt';key=work/'tls.key'
    if not crt.exists():
        command(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(crt),'-days','3','-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,DNS:host.docker.internal,IP:127.0.0.1'])
        os.chmod(crt,0o600);os.chmod(key,0o600)
    data['ca_file']=str(crt);return crt,key

def http(data,method,path,body=None,role='root',want=None):
    if not path.startswith('/') or '://' in path:raise RuntimeError('invalid isolated GitLab path')
    headers={'Content-Type':'application/json'}
    if 'tokens' in data:headers['PRIVATE-TOKEN']=data['tokens'][role]
    request=urllib.request.Request(data['gitlab_url']+path,data=None if body is None else json.dumps(body).encode(),headers=headers,method=method)
    context=ssl.create_default_context(cafile=data['ca_file'])
    try:
        with urllib.request.urlopen(request,context=context,timeout=20) as r:code=r.status;raw=r.read(2<<20)
    except urllib.error.HTTPError as e:code=e.code;raw=e.read(2<<20)
    try:result=json.loads(raw)
    except ValueError:result={}
    if want is not None and code not in (want if isinstance(want,tuple) else (want,)):raise RuntimeError('isolated GitLab API returned '+str(code)+' for '+path.split('?')[0])
    return code,result

def gitlab(data):
    started=time.monotonic();work=Path(data['work']);crt,key=cert(data);save(data)
    info=owned_container(data['gitlab_name'],data['owner'])
    if info is None:
        free_port(PORT);r=resources();emit('gitops.resources','passed' if r['headroom_gib']>=6 else 'blocked',r)
        if r['headroom_gib']<6:raise RuntimeError('less than 6GiB actual headroom for a fresh GitLab plus reserve; no existing service is stopped')
        # Own config only: no outbound SMTP, runners, registry, or organization account.
        config=f"external_url '{data['gitlab_url']}'\nnginx['listen_port'] = 443\nnginx['ssl_certificate'] = '/etc/gitlab/tls.crt'\nnginx['ssl_certificate_key'] = '/etc/gitlab/tls.key'\ngitlab_rails['initial_root_password'] = {json.dumps(data['gitlab_root_password'])}\ngitlab_rails['smtp_enable'] = false\ngitlab_rails['gitlab_email_enabled'] = false\ngitlab_rails['usage_ping_enabled'] = false\nprometheus_monitoring['enable'] = false\nregistry['enable'] = false\npuma['worker_processes'] = 0\npuma['min_threads'] = 1\npuma['max_threads'] = 4\nsidekiq['concurrency'] = 5\n"
        config_dir=work/'gitlab-config';config_dir.mkdir(mode=0o700,exist_ok=True)
        (config_dir/'gitlab.rb').write_text(config);os.chmod(config_dir/'gitlab.rb',0o600)
        shutil.copyfile(crt,config_dir/'tls.crt');shutil.copyfile(key,config_dir/'tls.key');os.chmod(config_dir/'tls.key',0o600)
        command(['docker','image','inspect',GITLAB])
        command(['docker','run','-d','--pull=never','--name',data['gitlab_name'],'--label',LABEL+'='+data['owner'],'--cpus','2','--memory','4g','--shm-size','256m','-p','127.0.0.1:'+str(PORT)+':443','-v',str(config_dir) + ':/etc/gitlab',GITLAB],timeout=90)
    elif not info['State']['Running']:command(['docker','start',data['gitlab_name']])
    deadline=time.monotonic()+900
    while time.monotonic()<deadline:
        try:
            code,_=http(data,'GET','/-/readiness')
            if code==200:break
            if code==404:
                # Omnibus monitoring paths hide behind an IP allowlist; validate inside our container with the same CA.
                raw=command(['docker','exec',data['gitlab_name'],'curl','--fail','--silent','--cacert','/etc/gitlab/tls.crt','https://localhost/-/readiness'],timeout=10)
                healthy=json.loads(raw)
                if healthy.get('status')=='ok':break
        except (OSError,ValueError):pass
        owned=owned_container(data['gitlab_name'],data['owner'])
        if not owned['State']['Running'] or owned['State'].get('OOMKilled'):raise RuntimeError('fresh GitLab exited or OOM; owned container/logs retained')
        emit('gitops.gitlab_startup','running',{'elapsed_seconds':time.monotonic()-started,'planned_seconds':900,'image':GITLAB},started);time.sleep(10)
    else:raise RuntimeError('fresh GitLab readiness timeout; no protocol mock fallback')
    emit('gitops.gitlab_startup','passed',{'image':GITLAB,'readiness_http':200,'readiness_probe':'owned-container CA-verified HTTPS (monitoring endpoint IP allowlist)','dedicated_url':data['gitlab_url'],'smtp_enabled':False},started)
    bootstrap(data);save(data)


def bootstrap(data):
    if 'tokens' in data:return
    # A one-time Rails bootstrap in our own container creates only disposable human accounts and PATs.
    script="require 'json'; result={}; users={'root'=>User.find_by_username!('root')};\n"
    for role,a in data['accounts'].items():
        script+=f"u=User.find_by_username({json.dumps(a['username'])});unless u;u=Users::BuildService.new(users['root'],{{username:{json.dumps(a['username'])},email:{json.dumps(a['username']+'@testlab.invalid')},name:{json.dumps('Testlab '+role)},password:{json.dumps(a['password'])},password_confirmation:{json.dumps(a['password'])},skip_confirmation:true,organization_id:users['root'].organization_id}}).execute;u.save!;end;users[{json.dumps(role)}]=u;\n"
    script+="users.each do |role,u|; t=u.personal_access_tokens.create!(name:'owned-backend-testlab',scopes:['api','read_repository'],expires_at:Date.today+2);result[role]={id:u.id,token:t.token,bot:u.bot?,state:u.state};end;puts JSON.generate(result);\n"
    work=Path(data['work']);bootstrap_file=work/'gitlab-config/bootstrap.rb';bootstrap_file.write_text(script);os.chmod(bootstrap_file,0o600)
    raw=command(['docker','exec',data['gitlab_name'],'gitlab-rails','runner','/etc/gitlab/bootstrap.rb'],timeout=180)
    records=json.loads(raw.splitlines()[-1]);data['tokens']={k:v['token'] for k,v in records.items()};data['gitlab_user_ids']={k:v['id'] for k,v in records.items()}
    for role,value in data['tokens'].items():
        path=work/(role+'-token');path.write_text(value);os.chmod(path,0o600)
    emit('gitops.gitlab_accounts','passed',{'accounts':{k:{'id':v['id'],'bot':v['bot'],'state':v['state']} for k,v in records.items()},'production_organization_verified':False})
    save(data)


def review_probe(data):
    if 'probe_mr' not in data:
        _,project=http(data,'POST','/api/v4/projects',{'name':'testlab-rules','path':'testlab-rules','initialize_with_readme':True,'default_branch':'main','visibility':'private','only_allow_merge_if_all_discussions_are_resolved':True},want=201)
        pid=project['id'];data['project_id']=pid;data['repo_url']='https://host.docker.internal:'+str(PORT)+'/root/testlab-rules.git'
        for role,access in [('proposer',30),('reviewer',40)]:http(data,'POST',f'/api/v4/projects/{pid}/members',{'user_id':data['gitlab_user_ids'][role],'access_level':access},want=201)
        manifest=(ROOT/'tools/testlab/gitops/rules.yml').read_text()
        http(data,'POST',f'/api/v4/projects/{pid}/repository/files/workload%2Frules.yml',{'branch':'main','content':manifest,'commit_message':'Initialize dedicated native Prometheus rules'},want=201)
        # Initial seed is complete before protection; no subsequent direct push/force-push is allowed.
        http(data,'DELETE',f'/api/v4/projects/{pid}/protected_branches/main')
        http(data,'POST',f'/api/v4/projects/{pid}/protected_branches',{'name':'main','push_access_level':0,'merge_access_level':40,'allow_force_push':False},want=201)
        http(data,'POST',f'/api/v4/projects/{pid}/repository/branches',{'branch':'review-probe','ref':'main'},role='proposer',want=201)
        http(data,'PUT',f'/api/v4/projects/{pid}/repository/files/workload%2Frules.yml',{'branch':'review-probe','content':manifest.replace('for: 30s','for: 60s'),'commit_message':'Review exact native rule bytes'},role='proposer',want=200)
        _,mr=http(data,'POST',f'/api/v4/projects/{pid}/merge_requests',{'source_branch':'review-probe','target_branch':'main','title':'Testlab independent rule review probe; do not merge automatically'},role='proposer',want=201)
        data['probe_mr']=mr['iid'];data['probe_mr_url']=mr['web_url'];save(data)
    pid=data['project_id'];iid=data['probe_mr'];code,approval=http(data,'GET',f'/api/v4/projects/{pid}/merge_requests/{iid}/approvals',role='proposer')
    compatible=code==200 and isinstance(approval.get('approved'),bool) and isinstance(approval.get('approved_by'),list)
    data['approval_api_available']=compatible;save(data)
    emit('gitops.approval_api','passed' if compatible else 'blocked',{'http_status':code,'approval_fields_available':compatible,'edition':'GitLab CE19.3.2','required_product_endpoint':'GET /merge_requests/:iid/approvals','mr_url':data['probe_mr_url'],'mr_state':'opened','merged_by_tool':False,'product_chain_verified_by_this_probe':False,'remaining':'Separate chain command must verify software WebAuthn/proposal/worker MR/independent review/Argo revision/exact Prom content; component probe is not that acceptance'})
    return compatible


def kubectl(data,*args,timeout=120,input=None):
    if owned_container(CLUSTER+'-control-plane',data['owner']) is None:raise RuntimeError('owned dedicated kind node is absent')
    path=Path(data['kubeconfig'])
    if path.is_symlink() or not path.is_file():raise RuntimeError('dedicated generated kubeconfig required')
    return command(['kubectl','--kubeconfig',str(path),'--context','kind-'+CLUSTER,*args],timeout=timeout,input=input)


def kind(data):
    started=time.monotonic();name=CLUSTER+'-control-plane';info=owned_container(name,data['owner'])
    if info is None:
        r=resources();emit('gitops.kind_resources','passed' if r['headroom_gib']>=4 else 'blocked',r)
        if r['headroom_gib']<4:raise RuntimeError('less than 4GiB headroom for new kind/Argo; other clusters remain untouched')
        config=Path(data['work'])/'kind.yml';config.write_text('kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n  - role: control-plane\n    image: '+NODE+'\n    extraPortMappings:\n      - containerPort: 30443\n        hostPort: 18581\n        listenAddress: 127.0.0.1\n      - containerPort: 30090\n        hostPort: 18582\n        listenAddress: 127.0.0.1\n');os.chmod(config,0o600)
        command(['docker','image','inspect',NODE])
        # Docker labels are immutable. Inject the owner label at kind's own docker run creation.
        wrapper=Path(data['work'])/'docker-wrapper';wrapper.mkdir(mode=0o700,exist_ok=True);docker=shutil.which('docker')
        if not docker:raise RuntimeError('Docker CLI unavailable')
        script='#!'+sys.executable+'\nimport os,sys\na=sys.argv[1:]\nif a and a[0]=="run":\n n=a[a.index("--name")+1] if "--name" in a else ""\n if n!='+repr(name)+':raise SystemExit(2)\n a=["run","--label",'+repr(LABEL+'='+data['owner'])+']+a[1:]\nos.execv('+repr(docker)+',['+repr(docker)+']+a)\n'
        (wrapper/'docker').write_text(script);os.chmod(wrapper/'docker',0o700)
        env={key:value for key,value in os.environ.items() if key in ('PATH','HOME','USER','TMPDIR','DOCKER_HOST','DOCKER_CONTEXT')};env['PATH']=str(wrapper)+os.pathsep+env.get('PATH','')
        created=subprocess.run(['kind','create','cluster','--name',CLUSTER,'--image',NODE,'--config',str(config),'--kubeconfig',data['kubeconfig'],'--wait','120s'],env=env,text=True,capture_output=True,timeout=240)
        if created.returncode:
            evidence=Path(data['work'])/'kind-create.log';evidence.write_text(created.stdout+created.stderr);os.chmod(evidence,0o600)
            raise RuntimeError('new kind creation failed; private kind-create.log retained')
        owned_container(name,data['owner'])
        command(['docker','update','--memory','3g','--memory-swap','4g','--cpus','2',name])
    kubectl(data,'get','nodes','-o','json');os.chmod(data['kubeconfig'],0o600)
    emit('gitops.kind','passed',{'cluster':CLUSTER,'kubeconfig':'dedicated generated 0600 file','image':NODE,'existing_context_changed':False},started)
    argo(data)
    if 'tokens' in data:runtime_application(data);product_config(data)


def argo(data):
    started=time.monotonic();work=Path(data['work']);manifest=work/'argo-install.yaml'
    if not manifest.exists():
        with urllib.request.urlopen(ARGO_MANIFEST,timeout=60) as response:raw=response.read(4<<20)
        if len(raw)>=4<<20:raise RuntimeError('official Argo manifest exceeds limit')
        manifest.write_bytes(raw);os.chmod(manifest,0o600)
    data['argo_manifest_sha256']=hashlib.sha256(manifest.read_bytes()).hexdigest();save(data)
    kubectl(data,'create','namespace','argocd','--dry-run=client','-o','json')
    kubectl(data,'apply','-f','-',input=json.dumps({'apiVersion':'v1','kind':'Namespace','metadata':{'name':'argocd','labels':{'aiops.testlab.owner':data['owner']}}}))
    kubectl(data,'apply','-n','argocd','-f',str(manifest),timeout=180)
    deadline=time.monotonic()+600
    while time.monotonic()<deadline:
        try:
            raw=kubectl(data,'get','pods','-n','argocd','-o','json');pods=json.loads(raw)['items'];ready=[p for p in pods if p.get('status',{}).get('phase')=='Running' and all(c.get('ready') for c in p.get('status',{}).get('containerStatuses',[]))]
            if len(ready)>=5:break
        except (OSError,ValueError):pass
        emit('gitops.argo_startup','running',{'version':ARGO_VERSION,'elapsed_seconds':time.monotonic()-started,'planned_seconds':600},started);time.sleep(10)
    else:raise RuntimeError('fresh Argo startup/image pull timeout; owned pods retained for diagnosis')
    emit('gitops.argo_startup','passed',{'version':ARGO_VERSION,'manifest_sha256':data['argo_manifest_sha256'],'ready_pods':len(ready),'source':ARGO_MANIFEST,'rule_application_reconciliation':'not yet verified'},started)


def argo_http(data,method,path,body=None,token=None,want=200):
    request=urllib.request.Request(data['argo_url']+path,data=None if body is None else json.dumps(body).encode(),headers={'Content-Type':'application/json',**({'Authorization':'Bearer '+token} if token else {})},method=method)
    try:
        with urllib.request.urlopen(request,context=ssl.create_default_context(cafile=data['ca_file']),timeout=30) as response:code=response.status;raw=response.read(2<<20)
    except urllib.error.HTTPError as error:code=error.code;raw=error.read(2<<20)
    if code!=want:raise RuntimeError('owned Argo API '+path.split('?')[0]+' HTTP '+str(code))
    return json.loads(raw)

def runtime_application(data):
    import importlib.util
    module_spec=importlib.util.spec_from_file_location('testlab_gitops_workload',ROOT/'tools/testlab/gitops/workload.py');module=importlib.util.module_from_spec(module_spec);module_spec.loader.exec_module(module);files=module.files
    work=Path(data['work']);crt,key=cert(data)
    def apply(namespace,obj):kubectl(data,'apply','-n',namespace,'-f','-',input=json.dumps(obj))
    def tls_secret(namespace,name):
        apply(namespace,{'apiVersion':'v1','kind':'Secret','metadata':{'name':name},'type':'kubernetes.io/tls','data':{'tls.crt':base64.b64encode(crt.read_bytes()).decode(),'tls.key':base64.b64encode(key.read_bytes()).decode()}})
    tls_secret('argocd','argocd-server-tls')
    kubectl(data,'patch','service','argocd-server','-n','argocd','--type','merge','-p',json.dumps({'spec':{'type':'NodePort','ports':[{'name':'https','port':443,'targetPort':8080,'nodePort':30443}]}}))
    kubectl(data,'rollout','restart','deployment/argocd-server','-n','argocd')
    kubectl(data,'rollout','status','deployment/argocd-server','-n','argocd','--timeout=90s',timeout=100)
    secret=json.loads(kubectl(data,'get','secret','argocd-initial-admin-secret','-n','argocd','-o','json'));password=base64.b64decode(secret['data']['password']).decode()
    token=argo_http(data,'POST','/api/v1/session',{'username':'admin','password':password})['token']
    host=command(['docker','exec',CLUSTER+'-control-plane','getent','hosts','host.docker.internal']).split()[0]
    if 'runtime_project_id' not in data:
        _,project=http(data,'POST','/api/v4/projects',{'name':'testlab-rule-runtime','path':'testlab-rule-runtime','initialize_with_readme':True,'default_branch':'main','visibility':'private'},want=201)
        pid=project['id'];data['runtime_project_id']=pid;data['runtime_repo_url']='https://host.docker.internal:'+str(PORT)+'/root/testlab-rule-runtime.git';save(data)
        for role,access in [('proposer',30),('reviewer',40)]:http(data,'POST',f'/api/v4/projects/{pid}/members',{'user_id':data['gitlab_user_ids'][role],'access_level':access},want=201)
        actions=[{'action':'create','file_path':name,'content':content} for name,content in files(ROOT,host).items()]
        http(data,'POST',f'/api/v4/projects/{pid}/repository/commits',{'branch':'main','commit_message':'Initialize native rules and Argo-managed local Prometheus','actions':actions},want=201)
        http(data,'DELETE',f'/api/v4/projects/{pid}/protected_branches/main')
        http(data,'POST',f'/api/v4/projects/{pid}/protected_branches',{'name':'main','push_access_level':0,'merge_access_level':40,'allow_force_push':False},want=201)
        data['runtime_project_initialized']=True;save(data)
    if not data.get('runtime_project_initialized'):raise RuntimeError('runtime project initialization interrupted; retain evidence, do not bypass branch protection')
    apply('argocd',{'apiVersion':'v1','kind':'ConfigMap','metadata':{'name':'argocd-tls-certs-cm'},'data':{'host.docker.internal':crt.read_text()}})
    apply('argocd',{'apiVersion':'v1','kind':'Secret','metadata':{'name':'testlab-rule-repository','labels':{'argocd.argoproj.io/secret-type':'repository'}},'stringData':{'type':'git','url':data['runtime_repo_url'],'username':data['accounts']['proposer']['username'],'password':data['tokens']['proposer']}})
    kubectl(data,'patch','deployment','argocd-repo-server','-n','argocd','--type','merge','-p',json.dumps({'spec':{'template':{'spec':{'hostAliases':[{'ip':host,'hostnames':['host.docker.internal']}]}}}}))
    kubectl(data,'apply','-f','-',input=json.dumps({'apiVersion':'v1','kind':'Namespace','metadata':{'name':'testlab-rules','labels':{'aiops.testlab.owner':data['owner']}}}))
    tls_secret('testlab-rules','testlab-prometheus-tls')
    app={'apiVersion':'argoproj.io/v1alpha1','kind':'Application','metadata':{'name':'testlab-rules','namespace':'argocd'},'spec':{'project':'default','source':{'repoURL':data['runtime_repo_url'],'targetRevision':'main','path':'workload'},'destination':{'server':'https://kubernetes.default.svc','namespace':'testlab-rules'}}}
    apply('argocd',app)
    # Bootstrap only the pre-protection initial main commit, no MR is merged or approved here.
    kubectl(data,'rollout','status','deployment/argocd-repo-server','-n','argocd','--timeout=90s',timeout=100)
    compare_deadline=time.monotonic()+90
    while time.monotonic()<compare_deadline:
        current=argo_http(data,'GET','/api/v1/applications/testlab-rules?refresh=hard',token=token)
        if not current.get('status',{}).get('conditions'):break
        time.sleep(5)
    else:raise RuntimeError('Argo exact repository comparison failed; private app conditions retained')
    argo_http(data,'POST','/api/v1/applications/testlab-rules/sync',{},token)
    deadline=time.monotonic()+180
    while time.monotonic()<deadline:
        app=argo_http(data,'GET','/api/v1/applications/testlab-rules?refresh=normal',token=token)
        if app.get('status',{}).get('sync',{}).get('status')=='Synced' and app.get('status',{}).get('health',{}).get('status')=='Healthy':break
        time.sleep(5)
    else:raise RuntimeError('initial Argo main deployment did not become Synced/Healthy; no final rule-enable claim')
    # Product gets only a read-only account token; bootstrap admin token is not persisted.
    kubectl(data,'patch','configmap','argocd-cm','-n','argocd','--type','merge','-p',json.dumps({'data':{'accounts.testlab':'apiKey','accounts.testlab.enabled':'true'}}))
    kubectl(data,'patch','configmap','argocd-rbac-cm','-n','argocd','--type','merge','-p',json.dumps({'data':{'policy.csv':'p, role:testlab-read, applications, get, default/testlab-rules, allow\ng, testlab, role:testlab-read','policy.default':'role:readonly'}}))
    if 'argo_token_file' not in data:
        value=argo_http(data,'POST','/api/v1/account/testlab/token',{'expiresIn':172800},token)['token'];path=work/'argo-read-token';path.write_text(value);os.chmod(path,0o600);data['argo_token_file']=str(path)
    with urllib.request.urlopen(data['prometheus_url']+'/api/v1/rules',context=ssl.create_default_context(cafile=data['ca_file']),timeout=20) as response:rules=json.loads(response.read(2<<20))
    groups=rules.get('data',{}).get('groups',[])
    exact=[g for g in groups if g.get('name')=='testlab-rules' and g.get('file')=='/etc/prometheus/rules/testlab-rules.yml']
    if len(exact)!=1:raise RuntimeError('Prom native group/file receipt mismatch')
    data['runtime_provider_file']=exact[0]['file'];data['initial_revision']=app['status']['sync']['revision'];save(data)
    emit('gitops.initial_argo_prom','passed',{'argo_sync':'Synced','argo_health':'Healthy','revision':data['initial_revision'],'provider_file':data['runtime_provider_file'],'rule_names':[r.get('name') for r in exact[0]['rules']],'product_argo_permission':'read-only','final_review_chain':False})


def product_config(data):
    work=Path(data['work']);root=fixture();component=json.loads((ROOT/'.local/testlab/component-environment.json').read_text())
    if component['owner']!=data['owner']:raise RuntimeError('GitOps/component owner mismatch')
    ca=work/'combined-ca.pem';ca.write_bytes(Path(component['host_config']['ca_file']).read_bytes()+b'\n'+Path(data['ca_file']).read_bytes());os.chmod(ca,0o600)
    signing=work/'promotion-signing-key'
    if not signing.exists():
        pem=work/'promotion-private.pem';command(['openssl','genpkey','-algorithm','ED25519','-out',str(pem)]);os.chmod(pem,0o600)
        private_der=subprocess.check_output(['openssl','pkey','-in',str(pem),'-outform','DER']);public_der=subprocess.check_output(['openssl','pkey','-in',str(pem),'-pubout','-outform','DER'])
        signing.write_text(base64.urlsafe_b64encode(private_der[-32:]+public_der[-32:]).decode().rstrip('='));os.chmod(signing,0o600)
    for branch in ('testlab-config-review','testlab-rule-drafts'):
        code,_=http(data,'GET',f"/api/v4/projects/{data['runtime_project_id']}/repository/branches/{branch}")
        if code==404:http(data,'POST',f"/api/v4/projects/{data['runtime_project_id']}/repository/branches",{'branch':branch,'ref':'main'},want=201)
        elif code!=200:raise RuntimeError('isolated draft base branch unavailable')
    promotion=work/'config-sync.json';private(promotion,{'tier':'nonprod','source_instance_id':'testlab-nonprod','signing_key_file':str(signing),'gitlab':{'base_url':data['gitlab_url'],'project_id':data['runtime_project_id'],'target_branch':'testlab-config-review','branch_prefix':'aiops-config-promotions/','token_file':str(work/'proposer-token')}})
    enable=work/'rule-enable.json';private(enable,{'gitlab_url':data['gitlab_url'],'gitlab_token_file':str(work/'proposer-token'),'argo_url':data['argo_url'],'argo_token_file':data['argo_token_file'],'bindings':[{'environment_id':root['environment'],'application_id':'testlab','group':'testlab-rules','provider_file':data['runtime_provider_file'],'project_id':data['runtime_project_id'],'target_branch':'main','yaml_path':'workload/rules.yml','repo_url':data['runtime_repo_url'],'argo_application':'testlab-rules','argo_project':'default','argo_namespace':'argocd','argo_source_path':'workload','destination_server':'https://kubernetes.default.svc','destination_namespace':'testlab-rules'}]})
    source=work/'rule-source.json';trial=json.loads((ROOT/'.local/testlab/runtime-config.json').read_text());private(source,{'environments':{root['environment']:{'url':data['prometheus_url'],'bearer_token_file':str(Path(trial['work'])/'source-token')}},'bindings':[{'environment_id':root['environment'],'application_id':'testlab','file':data['runtime_provider_file'],'group':'testlab-rules'}]})
    third_cert=work/'third-tls.crt';third_key=work/'third-tls.key'
    if not third_cert.exists():
        command(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(third_key),'-out',str(third_cert),'-days','3','-subj','/CN=testlab.local','-addext','subjectAltName=DNS:testlab.local,IP:127.0.0.1']);os.chmod(third_cert,0o600);os.chmod(third_key,0o600)
    ca.write_bytes(ca.read_bytes()+b'\n'+third_cert.read_bytes());data['third_cert']=str(third_cert);data['third_key']=str(third_key)
    data['product_env']={'AIOPS_V3_CONFIG_SYNC_CONFIG':str(promotion),'AIOPS_V3_ALERT_RULE_DRAFT_TARGET_BRANCH':'testlab-rule-drafts','AIOPS_V3_ALERT_RULE_DRAFT_BRANCH_PREFIX':'aiops-alert-rule-drafts/','AIOPS_V3_ALERT_RULE_ENABLE_CONFIG':str(enable),'AIOPS_V3_ALERT_RULES_CONFIG':str(source),'SSL_CERT_FILE':str(ca),'AIOPS_V3_PASSKEY_ENABLED':'true','AIOPS_SECURE_COOKIES':'true','AIOPS_PUBLIC_ORIGIN':'https://testlab.local:18583'};save(data)
    emit('gitops.product_config','passed',{'config_files':'0600 generated owner-only; root integration pending','keys':list(data['product_env']),'remaining':'HTTPS formal application origin/proxy, two software WebAuthn accounts, proposal approval/worker/MR final receipt'})


def cleanup(data):
    from store import Store
    store=Store(ROOT);store.reconcile_dead_runs()
    if any(r.get('status') in ('queued','running') for r in store.runs()):raise RuntimeError('active recorded run prevents heavy cleanup')
    free_port(18583);free_port(18585)
    # Validate every candidate before removing any. No kind shared network/context mutation.
    backend=json.loads((ROOT/'.local/testlab/environment.json').read_text());expected=Path(backend['work'])/'gitops'
    if not re.fullmatch(r'testlab-[a-f0-9]{12}',backend['owner']):raise RuntimeError('heavy cleanup owner identifier invalid')
    if data['owner']!=backend['owner'] or Path(data['work'])!=expected:raise RuntimeError('heavy cleanup ownership mismatch')
    if data['gitlab_name']!='aiops-'+data['owner']+'-gitlab' or data['cluster']!=CLUSTER:raise RuntimeError('heavy cleanup names mismatch')
    candidates=[]
    for name in (data['gitlab_name'],CLUSTER+'-control-plane'):
        info=owned_container(name,data['owner'])
        if info is not None:candidates.append(name)
    for name in candidates:command(['docker','rm','-f',name],timeout=90)
    emit('gitops.cleanup','passed',{'removed_owned_containers':candidates,'shared_kind_network_removed':False,'private_evidence_retained':True})


def main():
    p=argparse.ArgumentParser();p.add_argument('command',choices=['prepare','verify','gitlab','kind','chain']);a=p.parse_args();data=setup_state();errors=0
    if a.command=='chain':
        try:
            import importlib.util
            spec=importlib.util.spec_from_file_location('testlab_gitops_chain',ROOT/'tools/testlab/gitops/chain.py');module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);module.run(data,ROOT);return 0
        except Exception as error:emit('gitops.product_rule_chain','failed',{'error':type(error).__name__+': '+str(error),'complete':False,'mock_fallback':False});return 1
    # Independent systems continue after the other is blocked. Never stop existing resources to free room.
    if a.command in ('prepare','verify','gitlab'):
        try:
            gitlab(data)
            if not review_probe(data):errors+=1
        except Exception as e:emit('gitops.gitlab','blocked',{'error':type(e).__name__+': '+str(e),'mock_fallback':False});errors+=1
    if a.command in ('prepare','verify','kind'):
        try:kind(data)
        except Exception as e:emit('gitops.kind_argo','blocked',{'error':type(e).__name__+': '+str(e),'mock_fallback':False});errors+=1
    return 2 if errors else 0

if __name__=='__main__':sys.exit(main())
