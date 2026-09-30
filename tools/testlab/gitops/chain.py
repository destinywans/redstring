"""Formal proposal -> two real software WebAuthn votes -> worker MR -> review -> GitOps receipt."""
import base64,hashlib,http.client,socket,importlib.util,json,os,re,ssl,time,urllib.error,urllib.request,uuid
from pathlib import Path
from urllib.parse import urlsplit

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

def enabled_rule_request(investigation,environment,rule_name):
    return {'mode':'enabled','investigation_id':investigation,'application_id':'testlab','environment_id':environment,'group':'testlab-rules','name':rule_name,'expr':'testlab_request_failures_total{application="testlab",environment="test"} > 0','for':'0s','labels':{'application':'testlab','environment':environment,'severity':'P3'},'annotations':{'summary':'Controlled GitOps approval acceptance'}}

def approval_grant_seed(owner):
    if not re.fullmatch('testlab-[0-9a-f]{12}',owner):raise RuntimeError('owned approval role identifier required')
    role='gitops-'+owner+'-approve'
    return "INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance) VALUES('"+role+"','proposals','approve','application','testlab','passkey') ON CONFLICT(role_id,module,action,scope_kind,scope_id) DO UPDATE SET assurance=EXCLUDED.assurance;"

def validate_first_vote(vote,observation,proposal_id):
    if vote.get('proposal_id')!=proposal_id or vote.get('status')!='pending_review' or vote.get('execution_id') or vote.get('required_approvals')!=2 or vote.get('received_approvals')!=1:
        raise RuntimeError('first vote did not preserve pending_review without execution authority')
    if observation!={'approval_decision_exists':False,'enable_execution_exists':False}:
        raise RuntimeError('first vote already created approval decision or enable execution')

def validate_second_vote(vote,observation,proposal_id,creator_id,expected_approvers):
    execution_id=vote.get('execution_id','')
    if vote.get('proposal_id')!=proposal_id or vote.get('status')!='approved' or not re.fullmatch('[0-9a-f]{32}',execution_id) or vote.get('required_approvals')!=2 or vote.get('received_approvals')!=2:
        raise RuntimeError('second vote did not allocate approved execution authority')
    actual=[observation.get('first_approver_id'),observation.get('second_approver_id')]
    if len(set(expected_approvers))!=2 or len(set(actual))!=2 or set(actual)!=set(expected_approvers) or creator_id in actual or observation.get('creator_id')!=creator_id or observation.get('execution_id')!=execution_id or observation.get('required_count')!=2:
        raise RuntimeError('actual durable approval decision did not bind two independent non-creator actors')

def worker_review_receipt(execution,proposal_id,approved_execution_id,project_id,gitlab_url):
    if execution is None:return None
    if execution.get('proposal_id')!=proposal_id or execution.get('execution_id')!=approved_execution_id:
        raise RuntimeError('worker execution differs from durable approved authority')
    if execution.get('state')!='pending_review':return None
    receipt=execution.get('deployment') or {}
    if isinstance(receipt,str):receipt=json.loads(receipt)
    if not isinstance(receipt,dict):raise RuntimeError('worker persisted receipt is invalid')
    iid=receipt.get('mr_iid');url=urlsplit(receipt.get('url',''));base=urlsplit(gitlab_url)
    if (not isinstance(iid,int) or isinstance(iid,bool) or iid<=0 or receipt.get('project_id')!=project_id or
        receipt.get('source_branch')!='aiops-alert-rule-enable/'+approved_execution_id or receipt.get('target_branch')!='main' or
        receipt.get('path')!='workload/rules.yml' or receipt.get('state')!='opened' or
        not re.fullmatch('[0-9a-f]{64}',str(receipt.get('content_digest',''))) or
        url.scheme!='https' or url.netloc!=base.netloc or url.username is not None or url.query or url.fragment or
        not url.path.endswith('/-/merge_requests/'+str(iid))):
        raise RuntimeError('worker pending_review lacks an exact persisted MR receipt; no substitute merge')
    return receipt

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
    # This conditional cell requires the real bound WebAuthn proof; it is not an ordinary approval grant.
    statement+=approval_grant_seed(data['owner'])
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
        body=enabled_rule_request(investigation,backend['environment'],rule_name)
        proposal,_=call('POST','/api/v1/alert-rules',body,creator_cookie,want=(201,202),headers={'Idempotency-Key':'testlab-gitops-'+nonce});pid=proposal['proposal_id']
        for index,(user,cookie,authenticator) in enumerate(approvers):
            options,_=call('POST','/api/v1/proposals/'+pid+'/approve/options',{'confirm':True},cookie,want=201)
            assertion=authenticator.assertion(challenge(options['options']));authenticator.save(work/('software-reviewer-'+str(index)+'.json'))
            vote,_=call('POST','/api/v1/proposals/'+pid+'/approve',{'challenge_id':options['challenge_id'],'credential':assertion,'comment':'Controlled software WebAuthn independent review'},cookie,want=202 if index==0 else 200)
            if index==0:
                observed=json.loads(observe(sql,backend,"SELECT json_build_object('approval_decision_exists',EXISTS(SELECT 1 FROM v3_proposal_approval_decisions WHERE proposal_id="+q(pid)+"),'enable_execution_exists',EXISTS(SELECT 1 FROM v3_alert_rule_enable_executions WHERE proposal_id="+q(pid)+"));"))
                validate_first_vote(vote,observed,pid)
                gitops.emit('gitops.first_vote_boundary','passed',{'proposal_id':pid,'http_status':202,'status':vote['status'],'received_approvals':vote['received_approvals'],'execution_id_absent':True,**observed,'database_observer':'READ ONLY'})
        observed=json.loads(observe(sql,backend,"SELECT json_build_object('creator_id',p.created_by,'execution_id',d.execution_id,'required_count',d.required_count,'first_approver_id',v1.approver_id,'second_approver_id',v2.approver_id) FROM v3_proposals p JOIN v3_proposal_approval_decisions d ON d.proposal_id=p.id JOIN v3_proposal_approval_votes v1 ON v1.id=d.first_vote_id AND v1.proposal_id=d.proposal_id JOIN v3_proposal_approval_votes v2 ON v2.id=d.second_vote_id AND v2.proposal_id=d.proposal_id WHERE p.id="+q(pid)+";"))
        validate_second_vote(vote,observed,pid,creator['id'],[u['id'] for u,_,_ in approvers])
        gitops.emit('gitops.product_approval','passed',{'proposal_id':pid,'http_status':200,'independent_voters':[observed['first_approver_id'],observed['second_approver_id']],'creator_id':observed['creator_id'],'execution_id':observed['execution_id'],'status':vote['status'],'database_observer':'READ ONLY','no_sql_approval_insertion':True})
        wait_started=time.monotonic();deadline=wait_started+120;execution=None;receipt=None;next_event=wait_started
        while time.monotonic()<deadline:
            raw=observe(sql,backend,'SELECT row_to_json(e) FROM v3_alert_rule_enable_executions e WHERE proposal_id='+q(pid)+';')
            if raw:
                execution=json.loads(raw)
                receipt=worker_review_receipt(execution,pid,vote['execution_id'],data['runtime_project_id'],data['gitlab_url'])
                if receipt is not None:break
            if time.monotonic()>=next_event:
                gitops.emit('gitops.worker_mr','running',{'proposal_id':pid,'state':execution.get('state') if execution else 'not_created','elapsed_seconds':time.monotonic()-wait_started,'planned_seconds':120});next_event=time.monotonic()+10
            time.sleep(2)
        if receipt is None:
            path=work/('chain-execution-'+nonce+'-prepare.json');fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
            with os.fdopen(fd,'w') as stream:json.dump(execution,stream)
            raise RuntimeError('product worker did not persist pending_review and exact MR receipt in 120s; no approval/merge; immutable private execution evidence retained')
        # Derive the exact worker-created MR from its immutable deterministic branch, not the toy probe MR.
        project=data['runtime_project_id'];branch='aiops-alert-rule-enable/'+execution['execution_id']
        _,mrs=gitops.http(data,'GET',f'/api/v4/projects/{project}/merge_requests?source_branch='+branch+'&state=opened',role='reviewer',want=200)
        if len(mrs)!=1:raise RuntimeError('exact product worker MR unavailable; no substitute merge')
        mr=mrs[0];iid=mr['iid']
        if (iid!=receipt['mr_iid'] or mr.get('source_branch')!=receipt['source_branch'] or mr.get('target_branch')!=receipt['target_branch'] or
            mr.get('source_project_id')!=receipt['project_id'] or mr.get('target_project_id')!=receipt['project_id'] or mr.get('web_url')!=receipt['url']):
            raise RuntimeError('independent reviewer MR differs from product persisted receipt; no substitute merge')
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
