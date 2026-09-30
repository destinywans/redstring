"""Formal proposal -> two real software WebAuthn votes -> worker MR -> review -> GitOps receipt."""
import base64,hashlib,http.client,socket,importlib.util,json,os,re,ssl,time,urllib.error,urllib.request,uuid
from pathlib import Path

def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module

def record_http_failure(work,attempt,sequence,method,path,status,body):
    # Keep only the error envelope: no request, cookie, token, or successful auth payload.
    error=body.get('error') if isinstance(body,dict) else None
    safe_error={k:error[k] for k in ('code','message') if k in error} if isinstance(error,dict) else None
    evidence={'attempt':attempt,'sequence':sequence,'method':method,'path':path,'http_status':status,'error':safe_error}
    name='chain-red-'+attempt+'-'+str(sequence)+'-'+str(status)+'-'+hashlib.sha256(path.encode()).hexdigest()[:12]+'.json'
    destination=Path(work)/name
    fd=os.open(destination,os.O_WRONLY|os.O_CREAT|os.O_EXCL|getattr(os,'O_NOFOLLOW',0),0o600)
    with os.fdopen(fd,'w') as stream:json.dump(evidence,stream)
    pointer=Path(work)/'chain-last-red.json';pointer.write_text(json.dumps({'evidence_file':str(destination),'attempt':attempt}));os.chmod(pointer,0o600)
    return destination

def observe(sql,backend,statement):
    # Separate database observers from the deliberate, owned RBAC seed transaction.
    return sql(backend,"BEGIN READ ONLY; SET LOCAL statement_timeout='15s';"+statement+'COMMIT;')

def run(data,root):
    import gitops,perf
    from load.seed import fixture,sql,quote
    from tls_entry import entry
    wa=load('testlab_software_webauthn',Path(__file__).with_name('webauthn.py'));backend=fixture();work=Path(data['work']);origin='https://testlab.local:18583';context=ssl.create_default_context(cafile=data['product_env']['SSL_CERT_FILE']);nonce=uuid.uuid4().hex;call_sequence=0
    def call(method,path,body=None,cookie=None,want=200,headers=None):
        nonlocal call_sequence
        call_sequence+=1
        if not path.startswith('/api/v1/'):raise RuntimeError('third API path boundary')
        h={'Content-Type':'application/json','Origin':origin,'X-AIOps-Environment':backend['environment']};h.update(headers or {})
        if cookie:h['Cookie']='aiops_session='+cookie
        # Fixed in-process loopback transport preserves real RP hostname and TLS SAN checking.
        c=http.client.HTTPSConnection('testlab.local',18583,context=context,timeout=60)
        c._create_connection=lambda address,timeout,source_address=None:socket.create_connection(('127.0.0.1',18583),timeout,source_address)
        try:
            c.request(method,path,None if body is None else json.dumps(body).encode(),h);r=c.getresponse();code=r.status;raw=r.read(4<<20);response_headers=r.headers
        finally:c.close()
        parsed=json.loads(raw) if raw else {}
        if code not in (want if isinstance(want,tuple) else (want,)):
            record_http_failure(work,nonce,call_sequence,method,path,code,parsed)
            raise RuntimeError('formal third API '+path+' HTTP '+str(code)+'; first red private evidence retained')
        return parsed,response_headers
    def login(credentials):
        body,headers=call('POST','/api/v1/auth/login',credentials)
        cookies=[x.split(';',1)[0].split('=',1)[1] for x in headers.get_all('Set-Cookie',[]) if x.startswith('aiops_session=')]
        if len(cookies)!=1:raise RuntimeError('third secure session not issued')
        return body['user'],cookies[0]
    def challenge(options):return options.get('publicKey',options)['challenge']
    # Disposable seeded human users receive a separate application-scoped role only.
    role='gitops-'+data['owner']+'-approve';q=quote
    users=['perf-'+data['owner']+'-user-'+str(i) for i in (1,2)]
    statement=f"INSERT INTO rbac_roles(id,name) VALUES({q(role)},'Owned GitOps two-person approval') ON CONFLICT DO NOTHING;"
    for uid in users:statement+=f"INSERT INTO rbac_user_roles(user_id,role_id) VALUES({q(uid)},{q(role)}) ON CONFLICT DO NOTHING;"
    statement+=f"INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) SELECT {q(role)},m,a,'application','testlab' FROM unnest(ARRAY['alert_rules','proposals','canvas','events','services']) m CROSS JOIN unnest(ARRAY['view','change','approve']) a ON CONFLICT DO NOTHING;"
    sql(backend,'BEGIN;'+statement+'COMMIT;')
    with entry(data):
        creator,creator_cookie=login(backend['admin'])
        approvers=[]
        for i,uid in enumerate(users):
            user,cookie=login({'email':uid+'@testlab.invalid','password':backend['perf_password']})
            state_path=work/('software-reviewer-'+str(i)+'.json')
            if state_path.exists():
                authenticator=wa.Authenticator.restore(state_path,work,rp='testlab.local',origin=origin)
                listed,_=call('GET','/api/v1/me/passkeys',cookie=cookie)
                if not any(c.get('id')==wa.b64(authenticator.id) for c in listed.get('credentials',[])):raise RuntimeError('saved software credential absent from formal account; no silent identity substitution')
            else:
                authenticator=wa.Authenticator(work,'reviewer-'+str(i)+'-'+nonce,rp='testlab.local',origin=origin)
                options,_=call('POST','/api/v1/me/passkeys/registration/options',cookie=cookie,want=201)
                call('POST','/api/v1/me/passkeys/registration/finish',{'challenge_id':options['id'],'credential':authenticator.register(challenge(options['options'])),'label':'Testlab software ES256'},cookie,want=201)
                authenticator.save(state_path)
            approvers.append((user,cookie,authenticator))
        gitops.emit('gitops.software_passkeys','passed',{'independent_accounts':[u['id'] for u,_,_ in approvers],'creator':creator['id'],'protocol':'real ES256 WebAuthn registration + same-origin HTTPS','hardware_verified':False})
        canvas,_=call('POST','/api/v1/canvases',{'title':'Testlab GitOps '+nonce,'kind':'investigation','goal':'Controlled isolated rule deployment','services':['testlab'],'ai_draft':False},creator_cookie,want=201)
        investigation=canvas.get('id') or canvas.get('investigation_id')
        if not re.fullmatch('(INV-)?[0-9a-f]{32}',str(investigation)):raise RuntimeError('formal investigation identity unavailable')
        rule_name='TestlabApproved'+nonce
        body={'mode':'enabled','investigation_id':investigation,'application_id':'testlab','environment_id':backend['environment'],'group':'testlab-rules','name':rule_name,'expr':'testlab_request_failures_total{application="testlab",environment="test"} > 0','for':'0s','labels':{'severity':'P3'},'annotations':{'summary':'Controlled GitOps approval acceptance'}}
        proposal,_=call('POST','/api/v1/alert-rules',body,creator_cookie,want=(201,202),headers={'Idempotency-Key':'testlab-gitops-'+nonce});pid=proposal['proposal_id']
        for index,(user,cookie,authenticator) in enumerate(approvers):
            options,_=call('POST','/api/v1/proposals/'+pid+'/approve/options',{'confirm':True},cookie,want=201)
            assertion=authenticator.assertion(challenge(options['options']));authenticator.save(work/('software-reviewer-'+str(index)+'.json'))
            vote,_=call('POST','/api/v1/proposals/'+pid+'/approve',{'challenge_id':options['challenge_id'],'credential':assertion,'comment':'Controlled software WebAuthn independent review'},cookie,want=(200,202))
        if vote.get('status')!='approved':raise RuntimeError('two real proposal votes did not reach approved')
        gitops.emit('gitops.product_approval','passed',{'proposal_id':pid,'independent_voters':[u['id'] for u,_,_ in approvers],'status':vote['status'],'no_sql_approval_insertion':True})
        wait_started=time.monotonic();deadline=wait_started+120;execution=None;next_event=wait_started
        while time.monotonic()<deadline:
            raw=observe(sql,backend,'SELECT row_to_json(e) FROM v3_alert_rule_enable_executions e WHERE proposal_id='+q(pid)+';')
            if raw:
                execution=json.loads(raw)
                receipt=execution.get('deployment') or {}
                if isinstance(receipt,str):receipt=json.loads(receipt)
                if receipt.get('mr_iid') or receipt.get('mr'):break
            if time.monotonic()>=next_event:
                gitops.emit('gitops.worker_mr','running',{'proposal_id':pid,'state':execution.get('state') if execution else 'not_created','elapsed_seconds':time.monotonic()-wait_started,'planned_seconds':120});next_event=time.monotonic()+10
            time.sleep(2)
        if not execution:raise RuntimeError('product enable worker produced no execution in 120s')
        # Derive the exact worker-created MR from its immutable deterministic branch, not the toy probe MR.
        project=data['runtime_project_id'];branch='aiops-alert-rule-enable/'+execution['execution_id']
        _,mrs=gitops.http(data,'GET',f'/api/v4/projects/{project}/merge_requests?source_branch='+branch+'&state=opened',role='reviewer',want=200)
        if len(mrs)!=1:raise RuntimeError('exact product worker MR unavailable; no substitute merge')
        mr=mrs[0];iid=mr['iid']
        _,diffs=gitops.http(data,'GET',f'/api/v4/projects/{project}/merge_requests/{iid}/diffs',role='reviewer',want=200)
        if len(diffs)!=1 or diffs[0]['new_path']!='workload/rules.yml' or rule_name not in diffs[0]['diff']:raise RuntimeError('independent GitLab reviewer exact changed rule file mismatch')
        # Independent reviewer uses SHA binding. No auto_merge, force push, or approval-table fabrication.
        gitops.http(data,'POST',f'/api/v4/projects/{project}/merge_requests/{iid}/approve',{'sha':mr['sha']},role='reviewer',want=(200,201))
        _,approval=gitops.http(data,'GET',f'/api/v4/projects/{project}/merge_requests/{iid}/approvals',role='reviewer',want=200)
        if not approval.get('approved') or not any(r.get('user',{}).get('id')==data['gitlab_user_ids']['reviewer'] and r.get('approved_at') for r in approval.get('approved_by',[])):raise RuntimeError('actual independent GitLab approval missing')
        _,merged=gitops.http(data,'PUT',f'/api/v4/projects/{project}/merge_requests/{iid}/merge',{'sha':mr['sha'],'should_remove_source_branch':False},role='reviewer',want=200)
        if merged.get('state')!='merged' or merged.get('merge_user',{}).get('id')==mr.get('author',{}).get('id'):raise RuntimeError('merge actor not independently bound')
        # Explicit local Argo sync after approved protected-branch merge; this is the deployment controller, not product approval authority.
        secret=json.loads(gitops.kubectl(data,'get','secret','argocd-initial-admin-secret','-n','argocd','-o','json'));password=base64.b64decode(secret['data']['password']).decode();token=gitops.argo_http(data,'POST','/api/v1/session',{'username':'admin','password':password})['token']
        gitops.argo_http(data,'GET','/api/v1/applications/testlab-rules?refresh=hard',token=token);gitops.argo_http(data,'POST','/api/v1/applications/testlab-rules/sync',{},token)
        wait_started=time.monotonic();deadline=wait_started+240;next_event=wait_started
        while time.monotonic()<deadline:
            raw=observe(sql,backend,'SELECT row_to_json(e) FROM v3_alert_rule_enable_executions e WHERE proposal_id='+q(pid)+';');execution=json.loads(raw)
            if execution.get('state')=='enabled':break
            if time.monotonic()>=next_event:
                gitops.emit('gitops.final_receipt','running',{'proposal_id':pid,'state':execution.get('state'),'elapsed_seconds':time.monotonic()-wait_started,'planned_seconds':240});next_event=time.monotonic()+10
            time.sleep(3)
        else:
            path=work/('chain-execution-'+nonce+'.json');fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
            with os.fdopen(fd,'w') as stream:json.dump(execution,stream)
            raise RuntimeError('product exact GitOps/Prom receipt not enabled in 240s; immutable private execution evidence retained')
        with urllib.request.urlopen(data['prometheus_url']+'/api/v1/rules',context=ssl.create_default_context(cafile=data['ca_file']),timeout=15) as response:rules=json.loads(response.read(2<<20))
        observed=[r for g in rules['data']['groups'] if g['name']=='testlab-rules' and g['file']==data['runtime_provider_file'] for r in g['rules'] if r.get('name')==rule_name]
        if len(observed)!=1:raise RuntimeError('final exact Prom rule receipt missing')
        data['complete_rule_enable_chain']=True;data['product_proposal_id']=pid;data['product_execution_id']=execution['execution_id'];gitops.save(data)
        gitops.emit('gitops.product_rule_chain','passed',{'proposal_id':pid,'execution_id':execution['execution_id'],'mr_iid':iid,'merge_commit':merged['merge_commit_sha'],'rule_name':rule_name,'final_state':'enabled','actual_prom_rule_count':len(observed),'software_authenticator_hardware_claim':False})
    gitops.emit('gitops.third_cleanup','passed',{'third_process_stopped':True,'ports':[18583,18585],'base_api_runtime_hooks_unchanged':True})
