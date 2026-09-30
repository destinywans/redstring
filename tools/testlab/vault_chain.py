"""Formal scoped connectors → exact credential binding → least-privilege Vault.

TLS bearer checking is a declared local auth fixture in front of real owned
Prometheus. Health acceptance does not prove Prometheus itself authenticates.
"""
from __future__ import annotations
import argparse, hashlib, http.client, json, os, secrets, signal, ssl, subprocess, threading, time, uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import identity
from env import _private, _inspect, _owner
from faults import Blocked, attest, private_json
from runner import command_env

ROOT=Path(__file__).resolve().parents[2]
ORIGIN='http://127.0.0.1:18591'
ENDPOINT='https://localhost:18592'
VAULT='http://127.0.0.1:18572'
PROMETHEUS='http://127.0.0.1:18570'
q=identity.quote


def doctor(root):
    backend,current=attest(root)
    component=private_json(Path(root)/'.local/testlab/component-environment.json')
    expected=Path(root)/'.local/testlab/environments'/backend['owner']
    if component.get('owner')!=backend['owner'] or Path(component.get('work','')).resolve()!=expected.resolve():
        raise Blocked('Vault component owner/work mismatch')
    for provider,endpoint,port in [('vault',VAULT,18572),('prometheus',PROMETHEUS,18570)]:
        entries=[s for s in component.get('services',[]) if s.get('name')==provider]
        if len(entries)!=1 or entries[0].get('container')!='aiops-'+backend['owner']+'-'+provider or entries[0].get('url')!=endpoint:
            raise Blocked('owned exact Vault/Prometheus service required')
        info=_inspect(entries[0]['container']);_owner(info,backend['owner'])
        if not info or not info.get('State',{}).get('Running'):
            raise Blocked('owned Vault/Prometheus container not live')
        bindings=[b for values in info.get('NetworkSettings',{}).get('Ports',{}).values() for b in (values or [])]
        if {'HostIp':'127.0.0.1','HostPort':str(port)} not in bindings:
            raise Blocked('owned Vault/Prometheus loopback port mismatch')
    for name in ['tls-cert.pem','tls-key.pem']:
        path=expected/name
        if path.is_symlink() or not path.is_file() or path.stat().st_mode&0o777!=0o600:
            raise Blocked('owned private local TLS material required')
    if not component.get('credentials',{}).get('vault_token'):
        raise Blocked('generated dev Vault provisioning credential required')
    for port in (18591,18592):identity.free_port(port)
    return backend,component


def binding_mutations(binding):
    return [(field,dict(binding,**{field:value})) for field,value in [
        ('tier','production'),('environment_id','staging'),('connector_id',uuid.uuid4().hex),
        ('provider','loki'),('endpoint',ENDPOINT+'/different'),('credential_ref',binding['credential_ref'].replace('#token','#different'))]]


class AuthProxy:
    def __init__(self,token,cert,key):
        self.token=token;self.accepted=0;self.rejected=0;self.upstream=[];self.lock=threading.Lock()
        proxy=self
        class Handler(BaseHTTPRequestHandler):
            def log_message(self,*args):pass
            def do_GET(self):
                if self.path!='/-/ready':self.send_error(404);return
                if not secrets.compare_digest(self.headers.get('Authorization',''),'Bearer '+proxy.token):
                    with proxy.lock:proxy.rejected+=1
                    self.send_error(403);return
                conn=http.client.HTTPConnection('127.0.0.1',18570,timeout=5)
                try:
                    # Never forward the credential to the unauthenticated source.
                    conn.request('GET','/-/ready');response=conn.getresponse();body=response.read(65537)
                    if len(body)>65536:raise RuntimeError('real Prometheus probe exceeded bound')
                    with proxy.lock:proxy.accepted+=1;proxy.upstream.append(response.status)
                    self.send_response(response.status);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
                except OSError:self.send_error(502)
                finally:conn.close()
        self.server=ThreadingHTTPServer(('127.0.0.1',18592),Handler)
        self.server.daemon_threads=True
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(cert,key)
        self.server.socket=context.wrap_socket(self.server.socket,server_side=True)
        self.thread=threading.Thread(target=self.server.serve_forever,daemon=True)
    def start(self):self.thread.start()
    def counts(self):
        with self.lock:return dict(accepted=self.accepted,rejected=self.rejected,real_prometheus_statuses=list(self.upstream))
    def close(self):self.server.shutdown();self.server.server_close();self.thread.join(timeout=5)


class Chain(identity.Chain):
    def __init__(self,root,backend,component,run_id):
        self.root=Path(root);self.backend=backend;self.component=component;self.run_id=run_id
        self.work=self.root/'.local/testlab/environments'/backend['owner']/'vault-chain'/run_id
        self.work.mkdir(mode=0o700,parents=True,exist_ok=False)
        self.origin=ORIGIN;self.module='formal-vault';self.test_name='FormalVaultCredentialBinding';self.call_number=0
        self.proc=None;self.proxy=None;self.user_id='';self.id_placeholder='{id}';self.uid=uuid.uuid4().hex;self.role=uuid.uuid4().hex
        self.connector_id='';self.phase=0;self.tokens=[];self.policies=[];self.secret_path='testlab-credentials/'+run_id
        self.ref='vault://secret/'+self.secret_path+'#token';self.marker=secrets.token_urlsafe(32)
        self.root_token=component['credentials']['vault_token'];self.session=secrets.token_urlsafe(32);self.seeded=False
        self.image_hash=hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()
        self.state=dict(owner=backend['owner'],run_id=run_id,work=str(self.work),origin=ORIGIN,proxy=ENDPOINT,
                        source_tree_sha=backend['source_tree_sha'],binary_sha256=self.image_hash,pid=None,uid=self.uid,role=self.role,
                        secret_path=self.secret_path,policies=self.policies,connector_id=None)
        _private(self.work/'state.json',self.state)

    def vault(self,method,path,body=None,token=None,want=200):
        if not path.startswith('/v1/') or '..' in path:
            raise Blocked('Vault provisioning path boundary')
        deadline=getattr(self,'cleanup_deadline',None)
        info=identity.budgeted_inspect('aiops-'+self.backend['owner']+'-vault',deadline)
        try:_owner(info,self.backend['owner'])
        except ValueError:raise Blocked('Vault owner changed; refusing credential transmission') from None
        bindings=[b for values in (info or {}).get('NetworkSettings',{}).get('Ports',{}).values() for b in (values or [])]
        if not info or not info.get('State',{}).get('Running') or {'HostIp':'127.0.0.1','HostPort':'18572'} not in bindings:
            raise Blocked('Vault changed ownership/live port; refusing credential transmission')
        opener,_=identity.browser()
        status,raw,headers=identity.transport(opener,method,VAULT+path,None if body is None else json.dumps(body).encode(),
                                            {'Content-Type':'application/json','X-Vault-Token':token if token is not None else self.root_token},origins=(VAULT,),timeout=identity.remaining(deadline,3 if deadline else 15))
        if status!=want:raise AssertionError('owned Vault operation status '+str(status)+' expected '+str(want))
        return json.loads(raw) if raw else {}

    def provision(self):
        for suffix,rule in [('read','read'),('deny','deny')]:
            name='testlab-'+self.run_id+'-'+suffix
            # Persist owned names before a possibly uncertain creation response.
            self.policies.append(name);_private(self.work/'state.json',self.state)
            policy='path "secret/data/'+self.secret_path+'" { capabilities = ["'+rule+'"] }'
            self.vault('PUT','/v1/sys/policies/acl/'+name,{'policy':policy},want=204)
            token=self.vault('POST','/v1/auth/token/create',{'policies':[name],'no_default_policy':True,'ttl':'30m',
                              'explicit_max_ttl':'30m','renewable':False})['auth']['client_token']
            self.tokens.append(token);_private(self.work/'tokens.json',{'tokens':self.tokens})
        self.vault('POST','/v1/secret/data/'+self.secret_path,{'data':{'token':self.marker}})
        value=self.vault('GET','/v1/secret/data/'+self.secret_path,token=self.tokens[0])
        if value['data']['data']['token']!=self.marker:raise AssertionError('Vault exact-field readback mismatch')
        self.vault('GET','/v1/secret/data/'+self.secret_path+'-sibling',token=self.tokens[0],want=403)
        self.vault('POST','/v1/secret/data/'+self.secret_path,{'data':{'token':'forbidden'}},token=self.tokens[0],want=403)
        self.vault('GET','/v1/secret/data/'+self.secret_path,token=self.tokens[1],want=403)
        identity.emit('vault.least_privilege','passed',{'exact_path_read':True,'sibling_read_denied':True,'write_denied':True,'deny_policy_denied':True},module=self.module)
        work=Path(self.component['work']);self.proxy=AuthProxy(self.marker,str(work/'tls-cert.pem'),str(work/'tls-key.pem'));self.proxy.start()
        self.seeded=True
        self.sql('BEGIN; INSERT INTO users(id,email,name,role,password_hash) VALUES('+q(self.uid)+','+q(self.uid+'@example.invalid')+",'Owned credential manager','viewer','unused');"+
                 'DELETE FROM rbac_user_roles WHERE user_id='+q(self.uid)+'; INSERT INTO rbac_roles(id,name) VALUES('+q(self.role)+",'Owned scoped connector manager');"+
                 'INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+q(self.uid)+','+q(self.role)+'); '+
                 'INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+q(self.role)+",'environment','view','environment','test'),("+q(self.role)+",'connectors','view','environment','test'),("+q(self.role)+",'connectors','manage','environment','test'); "+
                 'INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('+q(hashlib.sha256(self.session.encode()).hexdigest())+','+q(self.uid)+",clock_timestamp()+interval '1 hour'); COMMIT;")

    def start_api(self,bindings,token):
        receipt=identity.cleanup_process(self.proc)
        if receipt.get('forced_kill'):raise AssertionError('prior credential process required SIGKILL')
        self.proc=None;identity.free_port(18591);self.frozen();self.phase+=1
        config=dict(DATABASE_URL=self.backend['database_url'],AIOPS_V3_INSTANCE_CONFIG=self.backend['instance_config'],
                    AIOPS_ADDR='127.0.0.1:18591',AIOPS_PUBLIC_ORIGIN=ORIGIN,AIOPS_SECURE_COOKIES='false',
                    AIOPS_ADMIN_EMAIL=self.backend['admin']['email'],AIOPS_ADMIN_PASSWORD=self.backend['admin']['password'],
                    AIOPS_VAULT_URL=VAULT,AIOPS_VAULT_TOKEN=token,AIOPS_V3_CONNECTOR_CREDENTIAL_BINDINGS=json.dumps(bindings),
                    SSL_CERT_FILE=str(Path(self.component['work'])/'tls-cert.pem'),OPENAI_MODEL='testlab',
                    OPENAI_BASE_URL='http://127.0.0.1:18578/v1',OPENAI_API_KEY='synthetic-testlab-key',
                    EMBEDDING_PROVIDER='openai',EMBEDDING_MODEL='synthetic-testlab',EMBEDDING_BASE_URL='http://127.0.0.1:18578/v1',EMBEDDING_API_KEY='synthetic-testlab-key')
        _private(self.work/('phase-'+str(self.phase)+'-config.json'),config)
        path=self.work/('phase-'+str(self.phase)+'-api.log')
        with open(path,'ab') as log:
            os.chmod(path,0o600);self.proc=subprocess.Popen([self.backend['binary']],cwd=self.root,env=command_env(config,{}),stdout=log,stderr=log)
        self.state.update(pid=self.proc.pid,phase=self.phase);_private(self.work/'state.json',self.state)
        opener,_=identity.browser();deadline=time.monotonic()+60
        while True:
            if self.proc.poll() is not None:raise AssertionError('formal credential process failed startup; private phase log retained')
            try:
                if identity.transport(opener,'GET',ORIGIN+'/healthz',origins=(ORIGIN,))[0]==200:break
            except (OSError,identity.urllib.error.URLError):pass
            if time.monotonic()>deadline:raise Blocked('formal credential process readiness timeout')
            time.sleep(.5)

    def scoped(self,method,path,body=None,want=200,code=None,environment='test',scenario='normal'):
        opener,_=identity.browser()
        data,headers=self.call(opener,method,path,body,want,code,headers={'Cookie':'aiops_session='+self.session,'X-AIOps-Environment':environment},scenario=scenario)
        serialized=json.dumps(data)
        if any(secret in serialized for secret in [self.marker,self.root_token,*self.tokens,self.ref]):
            raise AssertionError('formal credential response exposed a secret/reference')
        return data

    def assert_disabled_without_upstream(self,before):
        if self.proxy.counts()!=before:raise AssertionError('denied credential activation reached the TLS/Prometheus upstream')
        row=self.sql('SELECT enabled FROM v3_connectors WHERE id='+q(self.connector_id)+' AND environment_id='+q(self.backend['environment'])+';')
        if row!='f':raise AssertionError('failed credential activation enabled its registration')

    def run(self):
        self.provision();self.start_api([],self.tokens[0])
        created=self.scoped('POST','/api/v1/connectors',dict(type='prometheus',url=ENDPOINT,auth='bearer',credential_ref=self.ref,tools=['query_metrics']),want=201)
        self.connector_id=created['id'];self.user_id=self.connector_id;self.state['connector_id']=self.connector_id;_private(self.work/'state.json',self.state)
        path='/api/v1/connectors/'+self.connector_id
        binding=dict(tier='nonprod',environment_id='test',connector_id=self.connector_id,provider='prometheus',endpoint=ENDPOINT,credential_ref=self.ref)
        before=self.proxy.counts()
        self.scoped('POST',path+'/activate',{},want=503,code='credential_not_authorized',scenario='authorization');self.assert_disabled_without_upstream(before)
        for field,changed in binding_mutations(binding):
            self.start_api([changed],self.tokens[0]);before=self.proxy.counts()
            self.scoped('POST',path+'/activate',{},want=503,code='credential_not_authorized',scenario='authorization');self.assert_disabled_without_upstream(before)
            identity.emit('vault.binding_drift.'+field,'passed',{'fail_closed':True,'upstream_requests':0},module=self.module)
        self.start_api([binding],self.tokens[1]);before=self.proxy.counts()
        self.scoped('POST',path+'/activate',{},want=503,code='credential_unavailable',scenario='failure');self.assert_disabled_without_upstream(before)
        self.vault('POST','/v1/secret/data/'+self.secret_path,{'data':{'other':'missing-field-fixture'}})
        self.start_api([binding],self.tokens[0]);before=self.proxy.counts()
        self.scoped('POST',path+'/activate',{},want=503,code='credential_unavailable',scenario='failure');self.assert_disabled_without_upstream(before)
        self.vault('POST','/v1/secret/data/'+self.secret_path,{'data':{'token':self.marker}})
        self.scoped('POST',path+'/activate',{},want=403,code='forbidden',environment='staging',scenario='authorization');self.assert_disabled_without_upstream(before)
        enabled=self.scoped('POST',path+'/activate',{})
        if enabled.get('enabled') is not True or enabled.get('credential_verified') is not False:raise AssertionError('activation misrepresented registration or credential verification')
        check=self.scoped('POST',path+'/test',{})
        if check.get('ok') is not True:raise AssertionError('real scoped connector probe did not pass')
        detail=self.scoped('GET',path)
        if detail.get('id')!=self.connector_id or detail.get('scope')!='test':raise AssertionError('connector detail scope/id mismatch')
        snapshot=self.proxy.counts()
        if snapshot['accepted']!=2 or snapshot['rejected']!=0 or snapshot['real_prometheus_statuses']!=[200,200]:raise AssertionError('exact authenticated product→real Prometheus observation mismatch')
        audit=json.loads(self.sql("SELECT json_build_object('created',(SELECT count(*) FROM management_audit WHERE target="+q(self.connector_id)+" AND action='connector.created'),'activated',(SELECT count(*) FROM management_audit WHERE target="+q(self.connector_id)+" AND action='connector.activated'),'tested',(SELECT count(*) FROM management_audit WHERE target="+q(self.connector_id)+" AND action='connector.connection.tested'),'failed_vault_attempts',(SELECT count(*) FROM management_audit WHERE target="+q(self.connector_id)+" AND action='connector.activation.attempted' AND changes->>'reason'='credential_unavailable'))::text;"))
        if audit!={'created':1,'activated':1,'tested':1,'failed_vault_attempts':2}:raise AssertionError('formal connector audit lifecycle mismatch')
        self.frozen();return dict(calls=self.call_number,binding_drift_fields=6,audit=audit,proxy=snapshot,
             secret_scope='exact generated KV v2 path read-only',auth_check='explicit TLS protocol fixture forwarding owned real Prometheus',
             actual_prometheus_authentication=False,production_token_permissions=False)

    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40
        failures=[]
        receipt=identity.cleanup_process(self.proc,self.cleanup_deadline)
        if receipt.get('forced_kill'):failures.append('credential process required SIGKILL')
        if self.proxy:self.proxy.close()
        if self.seeded:
            try:
                self.sql('BEGIN; '+('DELETE FROM v3_connectors WHERE id='+q(self.connector_id)+' AND endpoint='+q(ENDPOINT)+';' if self.connector_id else '')+
                   'DELETE FROM sessions WHERE user_id='+q(self.uid)+'; UPDATE users SET disabled=true WHERE id='+q(self.uid)+';'+
                   'DELETE FROM rbac_user_roles WHERE user_id='+q(self.uid)+' AND role_id='+q(self.role)+'; DELETE FROM v3_role_grants WHERE role_id='+q(self.role)+'; DELETE FROM rbac_roles WHERE id='+q(self.role)+'; COMMIT;')
            except Exception:failures.append('owned scoped database cleanup failed')
        for token in self.tokens:
            try:self.vault('POST','/v1/auth/token/revoke',{'token':token},want=204)
            except Exception:failures.append('owned Vault token revocation failed')
        try:self.vault('DELETE','/v1/secret/metadata/'+self.secret_path,want=204)
        except Exception:failures.append('owned KV version cleanup failed')
        for policy in self.policies:
            try:self.vault('DELETE','/v1/sys/policies/acl/'+policy,want=204)
            except Exception:failures.append('owned Vault policy cleanup failed')
        self.state.update(stopped=True,cleanup={'process':receipt,'failures':failures});_private(self.work/'state.json',self.state)
        if failures:raise AssertionError('; '.join(failures))
        identity.emit('vault.cleanup','passed',{'process':receipt,'owned_tokens_revoked':len(self.tokens),'owned_policies_removed':len(self.policies),'immutable_audit_retained':True},module=self.module)


def main():
    parser=argparse.ArgumentParser();parser.add_argument('command',choices=['doctor','chain']);parser.add_argument('--root',default=str(ROOT));args=parser.parse_args()
    root=Path(args.root).resolve();chain=None
    try:
        # Separate fixed-port lock from identity.py so independent tool preparation is safe.
        import fcntl
        fd=os.open(root/'.local/testlab/vault-chain.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
        try:
            fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
            backend,component=doctor(root);identity.emit('vault.doctor','passed',{'ports':[18591,18592],'source_tree_sha':backend['source_tree_sha']},module='formal-vault')
            if args.command=='doctor':return 0
            chain=Chain(root,backend,component,uuid.uuid4().hex)
            actual=identity.execute(chain)
            _private(chain.work/'result.json',{'status':'passed','actual':actual,'source_tree_sha':backend['source_tree_sha']})
            identity.emit('vault.formal_chain','passed',actual,module='formal-vault',artifact=str((chain.work/'result.json').relative_to(root)))
        finally:os.close(fd)
    except Exception as error:
        status='blocked' if isinstance(error,Blocked) else 'failed'
        message=str(error) if isinstance(error,(Blocked,AssertionError)) else type(error).__name__
        if chain:_private(chain.work/'result.json',{'status':status,'error':message})
        identity.emit('vault.formal_chain',status,message,module='formal-vault');return 2 if status=='blocked' else 1
    return 0


if __name__=='__main__':
    with identity.cooperative_signals():raise SystemExit(main())
