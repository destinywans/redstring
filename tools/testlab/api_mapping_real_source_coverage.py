"""Two mapping POSTs with owned GitLab/Vault, without CI or business SQL seeds.

Parent-reviewed --live only. A project-1 Reporter read_api token is an auth
fixture, not a project write. Discovery consumes a recent project activation;
it does not promise a fresh remote lookup. Unknown credential creation outcomes
remain blocked and are never cleaned by searching names or adopting resources.
"""
from __future__ import annotations
import argparse, base64, datetime, fcntl, hashlib, http.client, importlib.util
import json, os, re, socket, ssl, stat, threading, time, uuid
from pathlib import Path
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
OWNER = 'testlab-3cbde3095125'
SOURCE_SHA = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY_SHA = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME_SHA = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID = 'c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
GITLAB_CID = 'e348f0a8682bd85219d878864569f54111a1d3195e24bb3e8b143b407b2356a6'
GITLAB_IMAGE_ID = 'sha256:17e6f56bb2a7307ad60073e2a26e82734ff80d729370e67d68067db298365b39'
GITLAB_IMAGE = 'gitlab/gitlab-ce:19.3.2-ce.0@sha256:05453dd1d9aba27c2c487613141596868409b4d03247647f7d66cb0b36f321b8'
PORTS = (18754, 18755)
ENDPOINT = 'https://localhost:18580'
PROJECT = 1
MODULE = 'formal-api-mapping-real-source'
TARGETS = (('POST','/service-mappings/discover'), ('POST','/service-mappings/accept'))
SUPPORT = (('POST','/auth/login'), ('POST','/services'), ('GET','/service-mappings'),
           ('POST','/connectors'), ('POST','/connectors/{id}/activate'),
           ('POST','/services/{id}/external-targets'))
REQUIRED_CASES = tuple('mapping_real.'+x for x in ('read_identity','vault_scope','activation',
    'nonempty_discovery','accepted','durable_readback','idempotency','ledger','cleanup'))
_spec = importlib.util.spec_from_file_location('mapping_real_private_readonly', ROOT/'tools/testlab/api_readonly_chain.py')
ro = importlib.util.module_from_spec(_spec); _spec.loader.exec_module(ro)
ro.PORTS, ro.BASES, ro.GETS = PORTS, tuple('http://127.0.0.1:'+str(p) for p in PORTS), ()
check, Blocked, q = ro.check, ro.Blocked, ro.quote
_inspect = ro.inspect_pg

def inspect_pg(*args, **kwargs):
    info = _inspect(*args, **kwargs)
    check(info.get('Id') == PG_CID, 'exact owned PG CID differs')
    return info
ro.inspect_pg = inspect_pg

def emit(name, status, actual, **fields):
    fields.pop('module',None); fields.pop('evidence_mode',None)
    if 'interface' in fields:
        fields['interface']=dict(fields['interface'],evidence_mode='real_local',
            test_name='FormalMappingRealSource',upstream_mode='owned_real_gitlab',
            proof_scope='formal binary HTTP; UUID PG/Vault; exact owned GitLab project GET; point-in-time activation; no CI')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,
                         evidence_mode='real_local',**fields)),flush=True)
ro.emit = emit

def digest(path): return hashlib.sha256(Path(path).read_bytes()).hexdigest()

def hashes():
    return {x:digest(ROOT/'tools/testlab'/x) for x in ('api_mapping_real_source_coverage.py',
        'api_readonly_chain.py','runner.py','store.py','faults.py')}

def remaining(deadline, maximum=8):
    if deadline is None: return maximum
    value = min(maximum, deadline-time.monotonic())
    if value <= 0: raise Blocked('shared external cleanup deadline exhausted')
    return value

def private_regular(path):
    path = Path(path)
    check(path.is_relative_to(ROOT/'.local/testlab') and path.resolve()==path,
          'owned private regular path required')
    for item in (path,*path.parents):
        if item == ROOT: break
        check(not item.is_symlink(), 'private path traverses symlink')
    info = path.stat()
    check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600
          and info.st_uid==os.getuid() and info.st_size <= 2<<20, 'private file owner/mode/size differs')
    return path

def guard_remote(info, kind, pinned=None):
    check(kind in ('gitlab','vault'), 'unknown owned dependency')
    port, internal = ('18580','443/tcp') if kind=='gitlab' else ('18572','8200/tcp')
    check(isinstance(info,dict) and info.get('Name')=='/aiops-'+OWNER+'-'+kind
          and info.get('Config',{}).get('Labels',{}).get('aiops.testlab.owner')==OWNER
          and info.get('State',{}).get('Running') is True
          and info.get('NetworkSettings',{}).get('Ports',{}).get(internal)==[{'HostIp':'127.0.0.1','HostPort':port}],
          'owned remote exact name/label/listener differs')
    check(info['Config'].get('Image')==(GITLAB_IMAGE if kind=='gitlab' else 'hashicorp/vault:1.20.4'),
          'owned dependency image differs')
    check(re.fullmatch('[a-f0-9]{64}',str(info.get('Id','')))
          and re.fullmatch('sha256:[a-f0-9]{64}',str(info.get('Image',''))), 'immutable remote identity missing')
    if kind=='gitlab': check(info['Id']==GITLAB_CID and info['Image']==GITLAB_IMAGE_ID,'exact GitLab CID/image differs')
    result = dict(container_id=info['Id'],image_id=info['Image'],started_at=info['State']['StartedAt'])
    check(pinned is None or pinned==result,'owned remote immutable identity drift')
    return result

def remote_allowed(kind,method,path,run_id,token_id=None,provisioner=False):
    check(str(uuid.UUID(run_id))==run_id,'canonical invocation UUID required')
    if kind=='gitlab':
        if not provisioner: return method=='GET' and path in ('/api/v4/user','/api/v4/projects/1')
        collection = '/api/v4/projects/1/access_tokens'
        return (method=='POST' and path==collection) or (type(token_id) is int and token_id>0
            and method in ('GET','DELETE') and path==collection+'/'+str(token_id))
    if kind=='vault':
        policy = '/v1/sys/policies/acl/mapping-real-'+run_id
        data = '/v1/secret/data/testlab-mapping-real/'+run_id
        return (path==policy and method in ('PUT','DELETE')) or (path==data and method in ('POST','GET')) or (
            path==data+'-sibling' and method=='GET') or (
            path=='/v1/secret/metadata/testlab-mapping-real/'+run_id and method=='DELETE') or (
            path in ('/v1/auth/token/create','/v1/auth/token/revoke') and method=='POST')
    return False

def validate_project_token(value,name,expiry):
    check(type(value.get('id')) is int and value['id']>0 and value.get('name')==name
          and value.get('scopes')==['read_api'] and value.get('access_level')==20
          and value.get('expires_at')==expiry and value.get('revoked') is False
          and isinstance(value.get('token'),str) and 0<len(value['token'])<=4096
          and not any(x in value['token'] for x in ('\r','\n','\0')), 'Reporter read_api project token differs')
    return value['id'],value['token']

def accept_body(candidates):
    return {'candidates':[{k:c[k] for k in ('id','version','service_id','environment_id','system_id','binding_revision')}
                          for c in candidates]}

def validate_discovery(value,expected):
    rows=value.get('candidates')
    check(value.get('source_state')=='configured' and value.get('suggestions')==1
          and isinstance(rows,list) and len(rows)==1,'nonempty real-source candidate required')
    check(all(rows[0].get(k)==v for k,v in expected.items()) and rows[0].get('id')
          and rows[0].get('version') and rows[0].get('expires_at'),'exact real project candidate differs')

def validate_accept(value,candidate,connector,replay=False):
    check(value==dict(accepted=0 if replay else 1,items=[dict(candidate_id=candidate['id'],
        service_id=candidate['service_id'],environment_id='test',system_id='code',connector_id=connector,
        target_id=candidate['target_id'],external_id='1',binding_revision=candidate['binding_revision']+1,
        binding=dict(kind='gitlab',connector_id=connector,resource='1'),
        status='already_accepted' if replay else 'accepted')]),'accept/replay exact linkage differs')

def validate_readback(value,key,app,connector):
    rows=value.get('rows')
    check(isinstance(rows,list) and len(rows)==1 and rows[0].get('service')==key
          and value.get('suggestions')==0,'exact single persisted service mapping missing')
    row=rows[0]
    check(row.get('cells',{}).get('code')==dict(state='ok',value='1',brand='gitlab')
          and row.get('save')==dict(service_id=app['id'],service_environment_id=app['env_id'],
              binding_revision=app['revision']+1,editable=True,
              bindings=[dict(system_id='code',kind='gitlab',connector_id=connector,resource='1')]),
          'second API display/native save linkage differs')

def doctor(root):
    check(Path(root).resolve()==ROOT,'reviewed checkout required')
    data=ro.doctor(root)
    check(data['source_tree_sha']==SOURCE_SHA and data['binary_sha256']==BINARY_SHA
          and data['runtime_sha']==RUNTIME_SHA,'frozen Go/nonrace binary/runtime differs')
    identities={k:guard_remote(_inspect('aiops-'+OWNER+'-'+k),k) for k in ('gitlab','vault')}
    config_path=private_regular(ROOT/'.local/testlab/gitops-environment.json')
    component_path=private_regular(ROOT/'.local/testlab/component-environment.json')
    config=json.loads(config_path.read_text()); component=json.loads(component_path.read_text())
    work=ROOT/'.local/testlab/environments'/OWNER/'gitops'
    check(config.get('owner')==component.get('owner')==OWNER and config.get('gitlab_url')==ENDPOINT
          and Path(config.get('work',''))==work and config.get('gitlab_user_ids',{}).get('root')==1
          and Path(component.get('work',''))==work.parent,'owned credential metadata differs')
    ca=private_regular(work/'tls.crt')
    root_token=config.get('tokens',{}).get('root'); vault_root=component.get('credentials',{}).get('vault_token')
    check(all(isinstance(v,str) and 0<len(v)<=4096 and not any(x in v for x in ('\r','\n','\0'))
              for v in (root_token,vault_root)),
          'owned provisioning credentials unavailable')
    return dict(data,helper_sha256=hashes(),remote_identities=identities,ca=str(ca),ca_sha256=digest(ca),
                provisioning_config_sha256=digest(config_path),root_token=root_token,vault_root=vault_root)

class Contract(ro.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path)
        check(not(parsed.scheme or parsed.netloc or parsed.query or parsed.fragment),'exact formal mapping path required')
        canonical,kind=super().canonical(method,path)
        check((method,canonical[len('/api/v1'):]) in (*TARGETS,*SUPPORT),'outside bounded real mapping inventory')
        return canonical,kind

class Chain(ro.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id)
        self.contract=Contract(root); self.binding=[]; self.generation=0
        self.token_id=None; self.secret=''; self.vault_token=''
        self.token_unknown=False; self.vault_unknown=False; self.policy_pending=False; self.secret_pending=False
        self.token_name='mapping-real-'+run_id
        self.expiry=(datetime.datetime.now(datetime.timezone.utc).date()+datetime.timedelta(days=1)).isoformat()
        self.policy='mapping-real-'+run_id; self.vault_path='testlab-mapping-real/'+run_id
        self.ref='vault://secret/'+self.vault_path+'#token'
        self.state.update(suite=MODULE,sql_business_result_writes=0,remote_project_writes=0,ports=list(PORTS))
        ro._private(self.work/'state.json',self.state)

    def frozen(self):
        super().frozen()
        check(hashes()==self.data['helper_sha256'] and digest(private_regular(self.data['ca']))==self.data['ca_sha256'],
              'helper or owned CA source drift')

    def marker_case(self,name,actual):
        check('mapping_real.'+name in REQUIRED_CASES,'unknown real mapping marker')
        self.frozen(); emit('mapping_real.'+name,'passed',dict(run_id=self.run_id,**actual))

    def remote(self,kind,method,path,body=None,token=None,want=200,deadline=None,provisioner=False):
        check(remote_allowed(kind,method,path,self.run_id,self.token_id,provisioner),'remote operation outside exact fixture')
        guard_remote(_inspect('aiops-'+OWNER+'-'+kind,timeout=remaining(deadline,3)),kind,self.data['remote_identities'][kind])
        if kind=='gitlab':
            if provisioner:
                check(digest(private_regular(ROOT/'.local/testlab/gitops-environment.json'))==self.data['provisioning_config_sha256'],
                      'owned root credential metadata drift before fixture operation')
            check(digest(private_regular(self.data['ca']))==self.data['ca_sha256'],'owned CA drift before credential send')
            conn=http.client.HTTPSConnection('localhost',18580,context=ssl.create_default_context(cafile=self.data['ca']),timeout=remaining(deadline))
            credential=self.data['root_token'] if provisioner else self.secret
            headers={'PRIVATE-TOKEN':credential,'Content-Type':'application/json'}
        else:
            conn=http.client.HTTPConnection('127.0.0.1',18572,timeout=remaining(deadline))
            headers={'X-Vault-Token':token if token is not None else self.data['vault_root'],'Content-Type':'application/json'}
        timer=None
        try:
            conn.connect()
            if deadline is not None:
                sock=conn.sock
                def expire():
                    try: sock.shutdown(socket.SHUT_RDWR)
                    except OSError: pass
                timer=threading.Timer(remaining(deadline),expire); timer.daemon=True; timer.start()
                conn.sock.settimeout(remaining(deadline))
            conn.request(method,path,None if body is None else json.dumps(body).encode(),headers)
            if deadline is not None: conn.sock.settimeout(remaining(deadline))
            response=conn.getresponse(); raw=response.read(65537)
            remaining(deadline)
            check(response.status==want and len(raw)<=65536,'exact owned remote status/body bound differs')
            return json.loads(raw) if raw else {}
        finally:
            if timer: timer.cancel()
            conn.close()

    def prepare_sources(self):
        self.token_unknown=True
        ro._private(self.work/'gitlab-token-intent.json',dict(project_id=1,name=self.token_name,scopes=['read_api'],access_level=20,
            expires_at=self.expiry,run_id=self.run_id,container_id=GITLAB_CID,unknown_requires_exact_operator_readback=True))
        value=self.remote('gitlab','POST','/api/v4/projects/1/access_tokens',
            dict(name=self.token_name,scopes=['read_api'],access_level=20,expires_at=self.expiry),want=201,provisioner=True)
        if type(value.get('id')) is int and value['id']>0: self.token_id=value['id']
        # Persist returned auth material privately even when validation later fails.
        ro._private(self.work/'gitlab-token-response.json',value)
        self.token_id,self.secret=validate_project_token(value,self.token_name,self.expiry)
        self.token_unknown=False
        user=self.remote('gitlab','GET','/api/v4/user')
        project=self.remote('gitlab','GET','/api/v4/projects/1')
        check(type(user.get('id')) is int and user['id'] not in (1,2,3) and user.get('bot') is True
              and project.get('id')==1,'real project Reporter identity differs')
        self.marker_case('read_identity',dict(project_id=1,user_id=user['id'],token_id=self.token_id,scopes=['read_api'],access_level=20,
            upstream_mode='owned_real_gitlab',business_methods=['GET'],root_pat_business_use=False))
        self.policy_pending=True
        ro._private(self.work/'vault-intent.json',dict(run_id=self.run_id,path=self.vault_path,policy=self.policy,
            token_ttl='30m',unknown_creation_requires_operator_readback=True))
        self.remote('vault','PUT','/v1/sys/policies/acl/'+self.policy,
            {'policy':'path "secret/data/'+self.vault_path+'" { capabilities = ["read"] }'},want=204)
        self.vault_unknown=True
        auth=self.remote('vault','POST','/v1/auth/token/create',dict(policies=[self.policy],no_default_policy=True,
            ttl='30m',explicit_max_ttl='30m',renewable=False))
        self.vault_token=auth.get('auth',{}).get('client_token','')
        ro._private(self.work/'vault-token-response.json',auth)
        check(bool(self.vault_token),'owned Vault token response missing')
        self.vault_unknown=False; self.secret_pending=True
        path='/v1/secret/data/'+self.vault_path
        self.remote('vault','POST',path,{'data':{'token':self.secret}})
        check(self.remote('vault','GET',path,token=self.vault_token)['data']['data']['token']==self.secret,'Vault exact read differs')
        self.remote('vault','GET',path+'-sibling',token=self.vault_token,want=403)
        self.remote('vault','POST',path,{'data':{'token':'forbidden'}},token=self.vault_token,want=403)
        self.marker_case('vault_scope',dict(exact_uuid_path=True,sibling_read403=True,write403=True))

    def startup_environment(self):
        if not self.binding: return {}
        return dict(AIOPS_VAULT_URL='http://127.0.0.1:18572',AIOPS_VAULT_TOKEN=self.vault_token,
            AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS=json.dumps(self.binding),SSL_CERT_FILE=self.data['ca'])

    def start(self):
        self.generation+=1; original=ro.emit
        def scoped(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):
                name='mapping_real.api_ready.'+str(self.generation)+'.'+name.rsplit('.',1)[1]
            original(name,status,actual,**fields)
        ro.emit=scoped
        try: super().start()
        finally: ro.emit=original

    def restart(self):
        for proc in reversed(self.processes):
            if proc.poll() is None: proc.terminate(); proc.wait(timeout=10)
        for index in (0,1): (self.work/('api-'+str(index)+'.log')).rename(self.work/('bootstrap-'+str(index)+'.log'))
        self.start()

    def call(self,*args,**kwargs):
        self.frozen(); validator=kwargs.get('validate')
        def safe(value):
            text=json.dumps(value)
            check(not any(x and x in text for x in (self.secret,self.vault_token,self.data['root_token'],self.data['vault_root'])),
                  'formal DTO exposed credential')
            if validator: validator(value)
        return super().call(*args,**dict(kwargs,validate=safe))

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('envs',(SELECT json_agg(to_jsonb(t) ORDER BY id) FROM application_environments t),'candidates',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM v3_service_mapping_candidates t),'audit',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))",read=True))

    def run(self):
        self.provision(); self.start(); self.seed()
        for module,action in (('services','change'),('connectors','manage')):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v)
                for v in (self.roles['peer'],module,action,'environment','test'))+');')
        for user in ('admin','peer'): self.login(user)
        self.prepare_sources()
        key='mapping-real-'+self.nonce
        self.call('admin','POST','/api/v1/services',{'items':[dict(id=key,tier='T1')]},want=201,scenario='fixture')
        app=json.loads(self.sql('SELECT json_build_object(\'id\',a.id,\'env_id\',e.id,\'revision\',e.binding_revision) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key='+q(key)+" AND e.key='test'",read=True))
        service=base64.urlsafe_b64encode((app['id']+'\0test').encode()).decode().rstrip('=')
        connector,_=self.call('peer','POST','/api/v1/connectors',dict(type='gitlab',url=ENDPOINT,auth='pat',
            credential_ref=self.ref,tools=[],webhook=False),want=201,scenario='fixture')
        connector_id=connector['id']; check(bool(re.fullmatch('[a-f0-9]{32}',connector_id)),'fresh native connector identity required')
        target,_=self.call('peer','POST','/api/v1/services/'+service+'/external-targets',
            dict(connector_id=connector_id,target_kind='project',external_id='1'),want=201,scenario='fixture')
        self.binding=[dict(tier='nonprod',environment_id='test',connector_id=connector_id,provider='gitlab',endpoint=ENDPOINT,credential_ref=self.ref)]
        self.restart()
        self.call('peer','POST','/api/v1/connectors/'+connector_id+'/activate',{},
            validate=lambda v:check(v==dict(enabled=True,status='ok',verified_projects=1,webhook_active=False),'real project activation differs'))
        self.marker_case('activation',dict(real_project_id=1,webhook_active=False,point_in_time=True))
        before=self.ledger()
        activation=[x for x in before['audit'] if x['action']=='connector.activated']
        check(len(activation)==1 and activation[0]['target']==connector_id
              and activation[0]['actor_id']==self.users['peer'] and activation[0]['outcome']=='success'
              and activation[0]['changes']==dict(environment='test',provider='gitlab',verified_projects=1,webhook_active=False),
              'real source activation audit differs')
        expected=dict(service_id=app['id'],environment_id='test',system_id='code',connector_id=connector_id,
            target_id=target['id'],external_id='1',binding_revision=app['revision'])
        value,_=self.call('peer','POST','/api/v1/service-mappings/discover',index=1,
            validate=lambda v:validate_discovery(v,expected))
        candidate=value['candidates'][0]; discovered=self.ledger()
        check(discovered['envs']==before['envs'] and discovered['audit']==before['audit']
              and len(discovered['candidates'])==1,'discover changed bindings/audit or candidate cardinality')
        self.marker_case('nonempty_discovery',dict(candidates=1,exact_project=1,sql_business_seed=False,fresh_remote_lookup=False))
        body=accept_body([candidate])
        self.call('peer','POST','/api/v1/service-mappings/accept',body,
            validate=lambda v:validate_accept(v,candidate,connector_id))
        accepted=self.ledger(); audits=[x for x in accepted['audit'] if x['action']=='v3.service_mapping.accepted']
        check(len(audits)==1 and audits[0]['actor_id']==self.users['peer'] and audits[0]['outcome']=='success'
              and audits[0]['application_id']==app['id'] and audits[0]['changes']['source_id']==target['id']
              and audits[0]['changes']['project_id']=='1' and accepted['candidates'][0]['accepted_by']==self.users['peer'],
              'accepted candidate/audit durable linkage differs')
        self.marker_case('accepted',dict(accepted=1,audits=1))
        # The read DTO and persisted native binding must agree on the second API.
        self.call('peer','GET','/api/v1/service-mappings',index=1,
            validate=lambda v:validate_readback(v,key,app,connector_id))
        env=accepted['envs'][0]
        check(env['binding_revision']==app['revision']+1 and env['bindings']==[dict(kind='gitlab',connector_id=connector_id,resource='1')],
              'native binding/revision differs')
        self.marker_case('durable_readback',dict(second_api=True,binding_revision=env['binding_revision']))
        self.call('peer','POST','/api/v1/service-mappings/accept',body,index=1,scenario='idempotency',
            validate=lambda v:validate_accept(v,candidate,connector_id,True))
        check(self.ledger()==accepted,'replay/read changed full accepted ledger')
        self.marker_case('idempotency',dict(accepted=0,audits=1)); self.marker_case('ledger',dict(no_read_or_replay_mutations=True))
        return dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,binary_sha256=BINARY_SHA,runtime_sha=RUNTIME_SHA,
            helper_sha256=self.data['helper_sha256'],required_cases=REQUIRED_CASES,http_calls=self.calls,
            upstream_mode='owned_real_gitlab',full_business_chain_verified=False,counts_as_whole_acceptance=False,
            unverified=['live GitLab changes after point-in-time activation','production GitLab','CI/Argo/VM/deployment','performance/24h'])

    def cleanup_external(self):
        deadline=time.monotonic()+4; errors=[]
        def attempt(function):
            try: function()
            except BaseException as error: errors.append(type(error).__name__)
        if self.token_id is not None:
            path='/api/v4/projects/1/access_tokens/'+str(self.token_id)
            def revoke():
                value=self.remote('gitlab','GET',path,deadline=deadline,provisioner=True)
                check(value.get('id')==self.token_id and value.get('name')==self.token_name
                      and value.get('scopes')==['read_api'] and value.get('access_level')==20,'exact token cleanup identity differs')
                self.remote('gitlab','DELETE',path,want=204,deadline=deadline,provisioner=True)
                if self.secret: self.remote('gitlab','GET','/api/v4/user',want=401,deadline=deadline)
                self.token_unknown=False
            attempt(revoke)
        if self.vault_token: attempt(lambda:self.remote('vault','POST','/v1/auth/token/revoke',{'token':self.vault_token},want=204,deadline=deadline))
        if self.secret_pending: attempt(lambda:self.remote('vault','DELETE','/v1/secret/metadata/'+self.vault_path,want=204,deadline=deadline))
        if self.policy_pending: attempt(lambda:self.remote('vault','DELETE','/v1/sys/policies/acl/'+self.policy,want=204,deadline=deadline))
        ro._private(self.work/'external-cleanup.json',dict(errors=errors,gitlab_creation_unknown=self.token_unknown,
            vault_creation_unknown=self.vault_unknown,shared_budget_seconds=4,project_id=1,token_id=self.token_id))
        check(not errors and not self.token_unknown and not self.vault_unknown,'external cleanup blocked; exact receipt retained')

    def cleanup(self):
        error=None
        try: self.cleanup_external()
        except BaseException as caught: error=caught
        finally: super().cleanup()
        if error is not None: raise error
        emit('mapping_real.cleanup','passed',dict(external_budget_seconds=4,inherited_cleanup_seconds=40,owned_uuid_only=True))

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT); parser.add_argument('--live',action='store_true')
    args=parser.parse_args(argv)
    if not args.live: parser.error('parent-reviewed --live required')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    fd=os.open(ROOT/'.local/testlab/mapping-real.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'owned private lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals(): ro.execute(Chain(ROOT,doctor(ROOT),str(uuid.uuid4())))
        return 0
    except Blocked: emit('mapping_real.environment','blocked',dict(error='guard/dependency blocked; private receipt retained')); return 2
    except Exception as error: emit('mapping_real.failure','failed',dict(error_type=type(error).__name__)); return 1
    finally: os.close(fd)

if __name__=='__main__': raise SystemExit(main())
