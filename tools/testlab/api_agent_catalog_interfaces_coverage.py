"""Accepted synthetic catalog inputs through reviewed GitLab and formal V3.

Only directory/release configuration and Prompt registration/version interfaces.
Synthetic signed source manifests are protocol Mock; no artifact execution,
traffic routing, evaluations, Run/Span producer, legacy startup or full chain.
"""
from __future__ import annotations

import argparse, base64, hashlib, http.client, importlib.util, json, os, re
import socket, sqlite3, stat, time, urllib.parse, uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SOURCE = '93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY = '6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME = '338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
OWNER = 'testlab-3cbde3095125'
PORTS = {'source': (18730, 18731), 'production': (18732, 18733)}
PROXY_PORT = 18734
MODULE = 'agent-catalog-interfaces'
MODE = 'protocol_mock'
TARGETS = (('GET', '/agents/{id}'), ('POST', '/agents/{id}/rollout'),
           ('POST', '/agents/{id}/rollback'), ('PATCH', '/agents/{id}/level'),
           ('GET', '/prompts/{id}'), ('POST', '/prompts/{id}/versions'),
           ('POST', '/evals'), ('GET', '/evals/{id}'))
SUPPORT = (('POST', '/auth/login'), ('POST', '/services'), ('GET', '/config-sync'),
           ('POST', '/config-sync/{id}/import'), ('POST', '/prompts'),
           ('GET', '/prompts'), ('GET', '/agents/lifecycle'))
REQUIRED_CASES = tuple('agent_catalog.'+s for s in ('inputs', 'reviewed_import',
    'configuration', 'authorization', 'boundaries', 'prompt_versions',
    'eval_metadata', 'audit_rollback', 'response_loss', 'revocation', 'consistency'))

# Private globals: other concurrent helpers keep their ports, class and emit.
spec = importlib.util.spec_from_file_location('agent_catalog_private_promotion', ROOT/'tools/testlab/promotion_chain.py')
promotion = importlib.util.module_from_spec(spec); spec.loader.exec_module(promotion)
promotion.PORTS = PORTS
promotion.GITLAB_PROXY_PORT = PROXY_PORT
promotion.GITLAB_PROXY_ORIGIN = 'https://localhost:'+str(PROXY_PORT)
spec = importlib.util.spec_from_file_location('agent_catalog_private_contract', ROOT/'tools/testlab/api_readonly_chain.py')
contract_module = importlib.util.module_from_spec(spec); spec.loader.exec_module(contract_module)
check, q, Blocked = promotion.check, promotion.quote, promotion.Blocked

def emit(name, status, actual, **fields):
    fields.pop('module', None)
    mode = fields.pop('evidence_mode', MODE)
    print(json.dumps(dict(name=name, status=status, actual=actual, module=MODULE,
                         evidence_mode=mode, **fields), ensure_ascii=False), flush=True)
promotion.emit = emit

def hashes():
    return {n: hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest()
            for n in ('api_agent_catalog_interfaces_coverage.py', 'promotion_chain.py',
                      'api_readonly_chain.py', 'identity.py', 'backend.py', 'env.py',
                      'faults.py', 'runner.py', 'inventory.py', 'store.py', 'gitops.py')}

def digest(path, private=False):
    p = Path(path)
    check(not p.is_symlink() and p.is_file(), 'regular frozen file required')
    if private:
        info = p.stat()
        check(stat.S_IMODE(info.st_mode) == 0o600 and info.st_uid == os.getuid(), 'owned private 0600 file required')
    return hashlib.sha256(p.read_bytes()).hexdigest()

def admission(path, run_id):
    p = Path(path)
    check(p.is_absolute() and p.resolve() == p and p.is_relative_to(ROOT/'.local/testlab'), 'private root admission path required')
    digest(p, True); data = json.loads(p.read_text())
    check(data.get('status') == 'root-admitted-agent-catalog-interfaces'
          and data.get('owner') == OWNER and data.get('resource_run_id') == run_id and data.get('source_tree_sha') == SOURCE
          and data.get('binary_sha256') == BINARY and data.get('runtime_sha256') == RUNTIME
          and data.get('postgres_cid') == promotion.PG_CID and data.get('gitlab_cid') == promotion.GITLAB_CID
          and data.get('ports') == {k:list(v) for k,v in PORTS.items()}
          and data.get('helper_sha256') == hashes(), 'root admitted source/helpers/UUID/resources mismatch')
    for key in ('fresh_nonce_hmac_process_tuple_verified', 'headroom_verified',
                'gitlab_recovery_credentials_verified', 'permanent_own_project_cleanup_authorized'):
        check(data.get(key) is True, 'root actual admission prerequisite absent')
    check(digest(ROOT/'.local/testlab/backend-fixture.json', True) == data.get('backend_fixture_sha256')
          and digest(ROOT/'.local/testlab/gitops-environment.json', True) == data.get('gitlab_config_sha256')
          and digest(ROOT/'.local/testlab/runtime-config.json', True) == RUNTIME,
          'main or recovered credential/config drift')
    config=json.loads((ROOT/'.local/testlab/gitops-environment.json').read_text())
    ca=Path(config.get('ca_file',''))
    check(config.get('owner')==OWNER and config.get('gitlab_name')=='aiops-'+OWNER+'-gitlab'
          and config.get('gitlab_url')==promotion.GITLAB_ORIGIN
          and ca.parent.resolve()==ROOT/'.local/testlab/environments'/OWNER/'gitops'
          and digest(ca,True)==data.get('gitlab_ca_sha256'),'recovered owned CA/origin drift')
    connection = sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro', uri=True, timeout=5)
    try:
        active = [json.loads(row[0]) for row in connection.execute('SELECT data FROM runs') if json.loads(row[0]).get('status') == 'running']
    finally: connection.close()
    check(len(active) == 1 and active[0].get('selected_suite') == MODULE
          and active[0].get('manifest_sha') == data.get('manifest_sha256')
          and active[0].get('source_tree_sha') == SOURCE and active[0].get('pid') == os.getppid(),
          'sole actual matching Runner required for shared GitLab window')
    return data

class Contract(contract_module.Contract):
    def canonical(self, method, path):
        parsed = urllib.parse.urlsplit(path)
        if parsed.scheme or parsed.netloc or parsed.fragment:
            raise Blocked('relative formal local HTTP required')
        canonical, kind = super().canonical(method, path)
        if (method, canonical[len('/api/v1'):]) not in TARGETS+SUPPORT:
            raise Blocked('outside Agent catalog/Prompt object interface scope')
        return canonical, kind

def encoded(value):
    return json.dumps(value, ensure_ascii=False, separators=(',', ':')).encode().replace(b'<', b'\\u003c').replace(b'>', b'\\u003e').replace(b'&', b'\\u0026')

def manifest(agent, app, version, source, promotion_id):
    check(re.fullmatch('[a-f0-9]{32}', promotion_id) is not None, 'fresh manifest UUID required')
    snapshot = dict(name='Synthetic catalog '+agent, version=version,
                    artifact_ref='testlab/catalog@sha256:'+hashlib.sha256(version.encode()).hexdigest())
    return dict(schema=2, promotion_id=promotion_id, kind='agent_version', source_id=agent,
                source_version=version, source_instance=source, source_environment='test',
                application_id=app, source_digest=hashlib.sha256(encoded(snapshot)).hexdigest(),
                agent_version=snapshot, signature='')

def configuration(value, revision, **expected):
    check({'revision','routing_connected','observed_traffic','release_effect'} <= value.keys()
          and value.get('revision') == revision and value.get('routing_connected') is False
          and value.get('observed_traffic') is None
          and value.get('release_effect') == 'configuration_saved_pending_activation'
          and all(key in value and value[key] == actual for key,actual in expected.items()),
          'saved configuration must not claim observed routing or traffic')

def detail(value, agent, app, version, revision):
    check({'score','runs_7d','release'} <= value.keys()
          and 'observed_traffic' in value.get('release', {})
          and value.get('id') == agent and value.get('scope', {}) ==
          dict(application_id=app, environment_id='prod', owner_team_id='')
          and value.get('version') == version and value.get('revision') == revision
          and value.get('routing_connected') is False
          and value.get('release', {}).get('routing_connected') is False
          and value.get('release', {}).get('observed_traffic') is None
          and value.get('score') is None and value.get('runs_7d') is None,
          'catalog detail identity/configuration/unknown runtime mismatch')
    versions=value.get('versions')
    check(isinstance(versions,list) and len(versions)==2 and {v.get('version') for v in versions}=={'v1','v2'}
          and all({'traffic','score','success','tokens'} <= v.keys() and all(v[k] is None for k in ('traffic','score','success','tokens'))
                  and v.get('routing_connected') is False for v in versions),
          'catalog versions must stay nonempty with unknown observed/evaluation data')

def prompt_detail(value, ident, agent, app, text, version):
    check({'runtime_usage','publication_status','evaluation_score'} <= value.keys()
          and value.get('id') == ident and value.get('version') == 'v'+str(version)
          and value.get('registered_agent', {}).get('id') == agent
          and value.get('scope') == dict(application_id=app, environment_id='prod')
          and value.get('lines') == [dict(text=s) for s in text.split('\n')]
          and value.get('partial') is True and value.get('runtime_usage') is None
          and value.get('publication_status') is None and value.get('evaluation_score') is None,
          'Prompt body/version/ownership/unknown runtime mismatch')

def eval_metadata(value, ident, title):
    unknown = {'dataset','metrics','radar','cases','datasets','evaluators','result_ref','finished_at'}
    check(unknown <= value.keys() and all(value[k] is None for k in unknown)
          and value.get('id') == ident and value.get('title') == title
          and value.get('status') == 'pending' and value.get('meta') == 'pending'
          and value.get('baseline_version') == 'v1' and value.get('candidate_version') == 'v2'
          and value.get('partial') is True and 'evaluation_result' in value.get('missing', []),
          'pending metadata must not claim executed evaluations/results')

class TLSProxy(promotion.TLSProxy):
    def arm_catalog_drop(self, agent):
        check(agent == self.chain.agent and self.tier == 'production', 'response drop requires exact owned Agent')
        with self.lock:
            check(self.drop is None, 'one exact response drop at a time')
            self.drop = ('PATCH', '/api/v1/agents/'+agent+'/level'); self.drop_receipt = None

class Chain(promotion.Chain):
    def __init__(self, root, backend, gitlab, run_id, admission_path):
        check(backend.get('owner') == OWNER and backend.get('source_tree_sha') == SOURCE and backend.get('runtime_sha') == RUNTIME
              and digest(backend['binary']) == BINARY, 'current Go/nonrace/runtime required')
        self.admission_path, self.package_hashes = admission_path, hashes()
        super().__init__(root, backend, gitlab, run_id)
        self.contract = Contract(root); self.user_cookies = {}; self.roles = {}
        self.agent = 'agent-'+run_id; self.app = None
        self.resource_intents = []
        self.state['catalog_resource_intents'] = self.resource_intents
        self.save()

    def frozen(self):
        super().frozen()
        check(hashes() == self.package_hashes, 'Agent tool or dependency changed')
        admission(self.admission_path, self.run_id)

    def intent_token_rows(self, intent, deadline=None):
        path = '/api/v4/personal_access_tokens?user_id=3&search='+urllib.parse.quote(intent['name'], safe='')+'&per_page=100&page=1'
        rows, headers = super().git('GET', path, deadline=deadline)
        check(isinstance(rows, list) and not headers.get('X-Next-Page', headers.get('x-next-page', '')),
              'owned PAT intent inventory incomplete')
        exact = [r for r in rows if r.get('name') == intent['name']]
        check(len(exact) <= 1 and all(r.get('user_id') == 3 and r.get('scopes') == ['read_api']
              and r.get('expires_at') == intent['expires_at'] for r in exact),
              'PAT intent exact user/name/scope/expiry mismatch; no revoke')
        return exact

    def git(self, method, path, body=None, **kwargs):
        intent = None
        if method == 'POST' and path == '/api/v4/projects':
            key = next((k for k in ('main',) if body.get('name') == self.project_name and
                        body.get('description') == self.db_marker('project-'+k)), None)
            check(key is not None and body.get('path') == self.project_name,
                  'catalog creation outside exact run project')
            rows, headers = super().git('GET', '/api/v4/namespaces?search=root&per_page=100&page=1')
            check(isinstance(rows,list) and not headers.get('X-Next-Page',headers.get('x-next-page','')),
                  'root namespace inventory incomplete')
            namespaces = [r for r in rows if r.get('full_path') == 'root' and r.get('path') == 'root' and r.get('kind') == 'user']
            check(len(namespaces) == 1 and isinstance(namespaces[0].get('id'),int), 'exact synthetic root namespace required')
            intent = dict(kind='project', key=key, name=self.project_name, namespace_id=namespaces[0]['id'],
                          full_path='root/'+self.project_name, marker=self.db_marker('project-'+key), status='pending')
            existing, _, status = super().git('GET','/api/v4/projects/'+urllib.parse.quote(intent['full_path'],safe=''),want=(200,404),with_status=True)
            check(status == 404, 'fresh project intent collides with existing resource')
            body = dict(body, namespace_id=intent['namespace_id'])
        elif method == 'POST' and path == '/api/v4/users/3/personal_access_tokens':
            check(body.get('name') == 'promotion-read-'+self.run_id and body.get('scopes') == ['read_api'],
                  'catalog PAT creation outside exact run/user/scope')
            intent = dict(kind='token', name=body['name'], user_id=3, expires_at=body['expires_at'], status='pending')
            check(not self.intent_token_rows(intent), 'fresh PAT intent collides with existing resource')
        if intent is not None:
            check(not any(i['kind'] == intent['kind'] for i in self.resource_intents), 'creation intent cannot POST retry')
            self.resource_intents.append(intent); self.save()  # durable before remote mutation
        result = super().git(method,path,body,**kwargs)
        if intent is not None:
            self.accept_intent(intent,result[0],register_token=False); self.save()
        return result

    def accept_intent(self, intent, row, *, register_token=True):
        check(isinstance(row,dict) and isinstance(row.get('id'),int) and row['id'] > 0,
              'created resource handle missing')
        if intent['kind'] == 'project':
            check(row.get('name') == intent['name'] and row.get('path') == intent['name']
                  and row.get('path_with_namespace') == intent['full_path']
                  and row.get('namespace',{}).get('id') == intent['namespace_id']
                  and row.get('description') == intent['marker'], 'project intent foreign owner/run/namespace; retained')
            self.projects[intent['key']] = dict(id=row['id'],name=intent['name'],namespace_id=intent['namespace_id'])
        else:
            check(row.get('name') == intent['name'] and row.get('user_id') == 3
                  and row.get('scopes') == ['read_api'] and row.get('expires_at') == intent['expires_at'],
                  'PAT intent foreign user/run/scope; retained')
            if register_token and not any(t['id'] == row['id'] for t in self.tokens):
                self.tokens.append(dict(id=row['id'],name=intent['name'],user_id=3))
        intent.update(status='handle-verified',id=row['id'])

    def recover_intents(self):
        # No retry POST and no resurrection of a lost token secret. Unknown
        # absent outcomes can still commit late, so cannot certify cleanup.
        # Runner preserves the original 45s cleanup grace; inherited cleanup
        # has a 40s budget. Reconciliation shares at most the first 4s.
        errors=[]; deadline=time.monotonic()+4
        for intent in self.resource_intents:
            try:
                if intent['status'] == 'handle-verified':
                    if intent['kind']=='token' and not any(t['id']==intent['id'] for t in self.tokens):
                        self.tokens.append(dict(id=intent['id'],name=intent['name'],user_id=3));self.save()
                    continue
                if intent['kind'] == 'project':
                    row,_,status=super().git('GET','/api/v4/projects/'+urllib.parse.quote(intent['full_path'],safe=''),
                                            want=(200,404),with_status=True,deadline=deadline)
                    check(status == 200,'uncertain project creation still absent; retain intent')
                else:
                    rows=self.intent_token_rows(intent,deadline)
                    check(len(rows)==1,'uncertain PAT creation still absent; retain intent'); row=rows[0]
                self.accept_intent(intent,row); self.save()
            except BaseException as error:
                errors.append(dict(kind=intent['kind'],name=intent['name'],error_type=type(error).__name__))
        return errors

    def call(self, tier, method, path, body=None, *, want=200, scenario='normal',
             validate=None, authenticated=True, cookie=None, environment=None, code=None):
        self.frozen(); canonical, _ = self.contract.canonical(method, path)
        target = (method, canonical[len('/api/v1'):]) in TARGETS
        check(not (target and scenario == 'normal' and 200 <= want < 300) or validate is not None,
              'normal catalog target requires business validator before success evidence')
        origin = 'https://localhost:'+str(PORTS[tier][0])
        headers = dict(Origin=origin, **{'X-AIOps-Environment': environment or ('prod' if tier == 'production' else 'test'), 'Content-Type':'application/json'})
        if authenticated and (cookie or tier in self.cookies): headers['Cookie'] = cookie or self.cookies[tier]
        status, raw, response_headers = promotion.transport(origin, method, path,
            None if body is None else json.dumps(body).encode(), headers, self.work/'entry.crt')
        self.calls += 1; value = json.loads(raw) if raw else {}; passed = False
        mode = MODE if target or '/prompts' in canonical or '/config-sync' in canonical or '/agents' in canonical else 'real_local'
        promotion._private(self.work/('catalog-http-'+str(self.calls)+'.json'), dict(method=method, path=path,
            status=status, response=value, scenario=scenario, evidence_mode=mode,
            source='synthetic_signed_catalog_input', request_credentials_recorded=False))
        try:
            check(status == want and (code is None or value.get('error', {}).get('code') == code), 'formal catalog HTTP status/error mismatch')
            if 200 <= status < 300:
                self.contract.shape(method, path, status, value)
                if validate: validate(value)
            passed = True
        finally:
            row = dict(method=method, path=canonical, scenario=promotion.scenario_kind(scenario), status='passed' if passed else 'failed',
                       expected={'status':[want], 'error_code':code}, actual={'status':status, 'error_code':value.get('error', {}).get('code')},
                       evidence_mode=mode, upstream_mode=mode, registration_evidence=passed and 200 <= status < 300,
                       proof_scope='Accepted synthetic signed catalog; no authoritative source/artifact/runtime/whole chain')
            if not passed: promotion.preserve_first_failure(self.work, row)
            emit('agent_catalog.http.'+str(self.calls), row['status'], row['actual'],
                 interface=promotion.actual_interface(row), evidence_mode=mode)
        return value, response_headers

    def marker(self, name, value):
        check('agent_catalog.'+name in REQUIRED_CASES, 'unknown catalog marker')
        emit('agent_catalog.'+name, 'passed', dict(resource_run_id=self.run_id, **value))

    def sql(self, statement, read=False):
        self.assert_database('production')
        return self.pg(self.databases['production'], statement, read=read)

    def snapshot(self):
        return json.loads(self.sql("SELECT json_build_object('agents',(SELECT json_agg(row_to_json(a) ORDER BY id) FROM v3_agents a),"
            "'versions',(SELECT json_agg(row_to_json(v) ORDER BY version) FROM v3_agent_versions v),"
            "'releases',(SELECT json_agg(row_to_json(v) ORDER BY id) FROM v3_agent_release_events v),"
            "'prompts',(SELECT json_agg(row_to_json(p) ORDER BY id) FROM v3_agent_prompts p),"
            "'prompt_versions',(SELECT json_agg(row_to_json(v) ORDER BY prompt_id,version) FROM v3_agent_prompt_versions v),"
            "'evals',(SELECT json_agg(row_to_json(e) ORDER BY id) FROM v3_agent_eval_experiments e),"
            "'audit',(SELECT json_agg(row_to_json(a) ORDER BY id) FROM management_audit a WHERE action LIKE 'v3.agent.%'))", True))

    def inputs(self):
        for key in ('visible', 'foreign'):
            name = 'catalog-'+self.run_id+'-'+key
            self.call('production', 'POST', '/api/v1/services', {'items':[{'id':name,'tier':'T1'}]}, want=201,
                      scenario='fixture', validate=lambda v: check(v == {'created':1}, 'HTTP-owned application missing'))
            app = self.sql('SELECT id FROM applications WHERE key='+q(name), True)
            check(re.fullmatch('[a-f0-9]{32}', app) is not None, 'HTTP application identity missing')
            if key == 'visible': self.app = app
            else: self.foreign_app = app
        for name, application, permissions in (('viewer', self.app, ('view',)),
                ('peer', self.foreign_app, ('view','change','manage')), ('denied', self.app, ())):
            uid, role = uuid.uuid4().hex, 'catalog-'+name+'-'+self.run_id; self.roles[name] = role
            email = name+'-'+self.run_id+'@testlab.invalid'
            statements = ["INSERT INTO users(id,email,name,role,password_hash,v3_identity_kind) SELECT "+','.join(q(v) for v in (uid,email,name))+",'viewer',password_hash,'human' FROM users WHERE email="+q(self.admin['production']['email'])+';',
                'DELETE FROM rbac_user_roles WHERE user_id='+q(uid)+';',
                "INSERT INTO rbac_roles(id,name,v3_kind) VALUES("+q(role)+','+q(role)+",'business');",
                'INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+q(uid)+','+q(role)+');',
                "INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES("+q(role)+",'environment','view','environment','prod');"]
            statements += ['INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (role,'agents',action,'application',application))+');' for action in permissions]
            self.sql('BEGIN; '+' '.join(statements)+' COMMIT;')
            _, headers = self.call('production','POST','/api/v1/auth/login',{'email':email,'password':self.admin['production']['password']},scenario='fixture')
            cookie = headers.get('Set-Cookie','')
            check(cookie.startswith('aiops_session=') and '; Secure' in cookie, 'synthetic identity HTTPS login missing')
            self.user_cookies[name] = cookie.split(';',1)[0]
        self.marker('inputs', {'HTTP_applications':2, 'SQL_business_records':False, 'synthetic_roles':3})

    def signed_item(self, version):
        value = manifest(self.agent, self.app, version, self.source_id, uuid.uuid4().hex)
        message, signature = self.work/('agent-message-'+value['promotion_id']), self.work/('agent-signature-'+value['promotion_id'])
        promotion.private_bytes(message, encoded(value))
        self.command(['openssl','pkeyutl','-sign','-inkey',str(self.work/'signer.pem'),'-rawin','-in',str(message),'-out',str(signature)])
        os.chmod(signature,0o600)
        check(len(signature.read_bytes()) == 64, 'Ed25519 signature length mismatch')
        value['signature'] = base64.urlsafe_b64encode(signature.read_bytes()).rstrip(b'=').decode()
        ident, pid = value['promotion_id'], self.projects['main']['id']
        path, branch = 'ops-config/agent-versions/'+ident+'.json', self.prefix+ident
        self.git('POST',f'/api/v4/projects/{pid}/repository/branches',{'branch':branch,'ref':self.target},role='proposer',want=201)
        self.git('POST',f'/api/v4/projects/{pid}/repository/files/'+urllib.parse.quote(path,safe=''),
                 {'branch':branch,'content':encoded(value).decode(),'commit_message':'Synthetic reviewed Agent metadata'},role='proposer',want=201)
        mr,_ = self.git('POST',f'/api/v4/projects/{pid}/merge_requests',{'source_branch':branch,'target_branch':self.target,
            'title':'Synthetic catalog '+version,'labels':'aiops-config-promotion'},role='proposer',want=201)
        return dict(id=ident,iid=mr['iid'],path=path,manifest=value)

    def import_catalog(self):
        for version in ('v1','v2'):
            item = self.signed_item(version)
            path = '/api/v1/config-sync/mr-'+str(item['iid'])+'/import'
            self.call('production','POST',path,want=503,scenario='boundary')
            before = self.snapshot(); self.review_merge(item)
            self.call('production','POST',path,scenario='fixture')
            repeat,_ = self.call('production','POST',path,scenario='idempotency')
            after = self.snapshot()
            ledger = json.loads(self.sql('SELECT row_to_json(x) FROM (SELECT kind,target_id,merge_sha FROM v3_config_imports WHERE promotion_id='+q(item['id'])+') x',True))
            check(ledger == dict(kind='agent_version',target_id=self.agent+'@'+version,merge_sha=item['merge_sha']), 'formal imported catalog ledger mismatch')
            self.call('production','POST',path,scenario='idempotency')
            check(self.snapshot() == after and before != after, 'repeat import changed accepted directory state')
            check(repeat.get('status') == 'imported', 'catalog repeat did not confirm import')
        self.marker('reviewed_import', {'versions':2,'source':'synthetic_signed_manifest','source_mode':MODE,
            'independent_real_local_GitLab_review':True,'artifact_bytes_verified':False,'execution_used':False})

    def agent_detail(self, version, revision, **kwargs):
        return self.call('production','GET','/api/v1/agents/'+self.agent,
            validate=lambda v: detail(v,self.agent,self.app,version,revision), **kwargs)[0]

    def configuration_tests(self):
        self.agent_detail('',1)
        base = '/api/v1/agents/'+self.agent
        self.call('production','POST',base+'/rollout',dict(to='v1',percent=100,expected_revision=1),want=409,scenario='boundary',code='eval_gate_unverified')
        self.call('production','POST',base+'/rollout',dict(to='v1',percent=100,override=True,override_reason='Synthetic local directory acceptance; no runtime activation',expected_revision=1),
                  validate=lambda v: configuration(v,2,to='v1',configured_percent=100,override=True))
        self.agent_detail('v1',2)
        self.call('production','POST',base+'/rollout',dict(to='v2',percent=25,override=True,override_reason='Synthetic canary configuration only',expected_revision=2),
                  validate=lambda v: configuration(v,3,to='v2',configured_percent=25,override=True))
        canary=self.agent_detail('v1',3)
        check(canary.get('status')=='canary' and canary['release'].get('to')=='v2'
              and canary['release'].get('configured_canary_percent')==25,'saved canary reference/percent mismatch')
        self.call('production','POST',base+'/rollback',dict(expected_revision=3),validate=lambda v: configuration(v,4,to='v1',configured_canary_percent=0))
        self.call('production','PATCH',base+'/level',dict(level='L0',expected_revision=4),validate=lambda v: configuration(v,5,level='L0',max_level='L0'))
        self.agent_detail('v1',5)
        self.marker('configuration',{'stable':'v1','canary_rollback':True,'level':'L0','revision':5,'observed_traffic':None,'routing_connected':False})
        state = self.snapshot()
        self.call('production','PATCH',base+'/level',dict(level='L0'),want=422,scenario='boundary',code='invalid_agent_request')
        self.call('production','PATCH',base+'/level',dict(level='L0',expected_revision=4),want=409,scenario='idempotency',code='version_conflict')
        self.call('production','PATCH',base+'/level',dict(level='L1',expected_revision=5),want=409,scenario='boundary',code='agent_level_cap')
        self.call('production','GET',base,want=401,scenario='authorization',authenticated=False)
        self.call('production','GET',base,want=404,scenario='authorization',cookie=self.user_cookies['peer'])
        self.call('production','GET',base,want=404,scenario='authorization',cookie=self.user_cookies['denied'])
        self.call('production','PATCH',base+'/level',dict(level='L0',expected_revision=5),want=403,scenario='authorization',cookie=self.user_cookies['viewer'])
        self.call('source','GET',base,want=404,scenario='boundary')
        check(self.snapshot() == state, 'refused or cross-scope directory request mutated business/audit state')
        self.marker('authorization',{'anonymous':401,'cross_application':404,'visible_no_manage':403})
        self.marker('boundaries',{'missing_revision':422,'stale_CAS':409,'unconfigured_cap':409,'source_instance_object':404,'state_unchanged':True})

    def prompt_tests(self):
        payload = dict(agent_id=self.agent,name='Synthetic Prompt '+self.run_id,text='Read {{service}} only.\nNo tool execution.')
        value,_ = self.call('production','POST','/api/v1/prompts',payload,want=201,validate=lambda v:check(v.get('version')=='v1' and bool(re.fullmatch('[a-f0-9]{32}',v.get('id',''))),'Prompt registration identity missing'))
        self.prompt = value['id']; path = '/api/v1/prompts/'+self.prompt
        get = lambda version,text: self.call('production','GET',path,validate=lambda v:prompt_detail(v,self.prompt,self.agent,self.app,text,version))[0]
        get(1,payload['text']); new = 'Current {{service}} synthetic version.'
        for version in (2,3):
            self.call('production','POST',path+'/versions',{'text':new},want=201,scenario='normal' if version==2 else 'idempotency',
                validate=lambda v,version=version:check(v=={'version':'v'+str(version)},'same-text request must append a distinct revision'))
            get(version,new)
        duplicate,_ = self.call('production','POST','/api/v1/prompts',payload,want=201,scenario='idempotency')
        check(duplicate.get('id') != self.prompt and duplicate.get('version')=='v1','Prompt registration has no idempotency-key contract; duplicate should create another object')
        state = self.snapshot(); get(3,new); get(3,new)
        self.call('production','GET',path,want=401,scenario='authorization',authenticated=False)
        self.call('production','GET',path,want=404,scenario='authorization',cookie=self.user_cookies['peer'])
        self.call('production','POST',path+'/versions',{'text':'denied'},want=403,scenario='authorization',cookie=self.user_cookies['viewer'])
        self.call('production','POST',path+'/versions',{'text':''},want=422,scenario='boundary')
        self.call('source','GET',path,want=404,scenario='boundary')
        check(self.snapshot()==state,'Prompt read or refusal altered revision/audit')
        self.prompt_text = new
        self.marker('prompt_versions',{'formal_created':2,'first_prompt_version':3,'same_text_appends_revision':True,'read_audit_delta':0,'runtime_usage':None})

    def eval_tests(self):
        title='Synthetic pending metadata '+self.run_id
        body=dict(agent_id=self.agent,title=title,baseline_version='v1',candidate_version='v2')
        created=[]
        def valid(v):
            check(re.fullmatch('[a-f0-9]{32}',v.get('id','')) is not None and v.get('status')=='pending'
                  and v.get('execution_available') is False,'Eval creation must be pending and not executable')
        for scenario in ('normal','idempotency'):
            value,_=self.call('production','POST','/api/v1/evals',body,want=201,scenario=scenario,validate=valid)
            created.append(value['id'])
        check(len(set(created))==2,'no Eval idempotency-key contract: repeated create must produce independent metadata')
        self.eval_id=created[0]; self.eval_title=title; path='/api/v1/evals/'+self.eval_id
        before=self.snapshot()
        self.call('production','GET',path,validate=lambda v:eval_metadata(v,self.eval_id,title))
        self.call('production','GET',path,scenario='idempotency',validate=lambda v:eval_metadata(v,self.eval_id,title))
        self.call('production','GET',path,want=401,scenario='authorization',authenticated=False)
        self.call('production','GET',path,want=404,scenario='authorization',cookie=self.user_cookies['peer'])
        self.call('production','POST','/api/v1/evals',body,want=403,scenario='authorization',cookie=self.user_cookies['viewer'])
        self.call('production','POST','/api/v1/evals',dict(body,agent_id='unknown-'+self.run_id),want=404,scenario='boundary')
        self.call('production','POST','/api/v1/evals',dict(body,candidate_version='v1'),want=422,scenario='boundary',code='invalid_agent_request')
        self.call('production','POST','/api/v1/evals',dict(body,candidate_version='unknown'),want=404,scenario='boundary')
        self.call('source','GET',path,want=404,scenario='boundary')
        check(self.snapshot()==before,'Eval reads/refused references mutated metadata/audit')
        self.marker('eval_metadata',{'HTTP_created':2,'pending':2,'execution_available':False,'result_ref':None,'read_audit_delta':0,'same_input_creates_distinct_metadata':True})

    def audit_fault(self, action, request):
        check(action in ('v3.agent.level','v3.agent.prompt.version','v3.agent.eval.created'), 'only exact owned catalog audit fault allowed')
        actor = self.sql('SELECT id FROM users WHERE email='+q(self.admin['production']['email']),True)
        self.sql("CREATE FUNCTION testlab_catalog_audit_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id="+q(actor)+" AND NEW.action="+q(action)+" THEN RAISE EXCEPTION 'owned catalog audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER testlab_catalog_audit_fault BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION testlab_catalog_audit_fault();")
        before=self.snapshot()
        try: request()
        finally: self.sql('DROP TRIGGER testlab_catalog_audit_fault ON management_audit; DROP FUNCTION testlab_catalog_audit_fault();')
        check(self.snapshot()==before,'failed final audit left a directory/Prompt mutation')

    def fault_tests(self):
        base='/api/v1/agents/'+self.agent
        self.audit_fault('v3.agent.level',lambda:self.call('production','PATCH',base+'/level',dict(level='L0',expected_revision=5),want=503,scenario='failure',code='agent_console_unavailable'))
        self.audit_fault('v3.agent.prompt.version',lambda:self.call('production','POST','/api/v1/prompts/'+self.prompt+'/versions',{'text':'must rollback'},want=503,scenario='failure',code='agent_console_unavailable'))
        self.audit_fault('v3.agent.eval.created',lambda:self.call('production','POST','/api/v1/evals',dict(agent_id=self.agent,title='Audit rollback only',baseline_version='v1',candidate_version='v2'),want=503,scenario='failure',code='agent_console_unavailable'))
        self.agent_detail('v1',5)
        self.call('production','GET','/api/v1/prompts/'+self.prompt,validate=lambda v:prompt_detail(v,self.prompt,self.agent,self.app,self.prompt_text,3))
        self.marker('audit_rollback',{'directory_revision':5,'prompt_version':3,'audit_failure_atomic':True,'recovery_readback':True})
        before=self.snapshot(); proxy=self.proxies['production']; proxy.arm_catalog_drop(self.agent)
        body=dict(level='L0',expected_revision=5)
        try: self.call('production','PATCH',base+'/level',body,validate=lambda v:configuration(v,6,level='L0',max_level='L0'))
        except (OSError,http.client.HTTPException): pass
        else: raise AssertionError('exact committed catalog response was not dropped')
        check(proxy.drop_receipt and proxy.drop_receipt.get('path')==base+'/level' and proxy.drop_receipt.get('upstream_status')==200,'loss was not after exact successful product response')
        self.agent_detail('v1',6); state=self.snapshot()
        levels=lambda value:sum(row['action']=='v3.agent.level' for row in value['audit'])
        check(levels(state)==levels(before)+1 and state['releases']==before['releases']
              and state['prompt_versions']==before['prompt_versions'] and state['evals']==before['evals'],
              'response-loss commit must add exactly one level audit and no unrelated business mutations')
        self.call('production','PATCH',base+'/level',body,want=409,scenario='idempotency',code='version_conflict')
        check(self.snapshot()==state,'stale retry after response loss applied twice')
        self.marker('response_loss',dict(proxy.drop_receipt,client_response_lost=True,readback_revision=6,stale_retry=409,level_audit_delta=1))

    def revocation_tests(self):
        before=self.snapshot(); viewer=self.user_cookies['viewer']
        paths=[('/api/v1/agents/'+self.agent,lambda v:detail(v,self.agent,self.app,'v1',6)),
               ('/api/v1/prompts/'+self.prompt,lambda v:prompt_detail(v,self.prompt,self.agent,self.app,self.prompt_text,3)),
               ('/api/v1/evals/'+self.eval_id,lambda v:eval_metadata(v,self.eval_id,self.eval_title))]
        for path,validator in paths:
            self.call('production','GET',path,cookie=viewer,validate=validator)
        check(self.snapshot()==before,'authorized viewer reads altered object/audit state')
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['viewer'])+" AND module='agents';")
        for path,_ in paths:
            self.call('production','GET',path,want=404,scenario='authorization',cookie=viewer)
        check(self.snapshot()==before,'revoked viewer reads altered object/audit state')
        self.marker('revocation',{'current_grant_revocation':True,'viewer_normal_before_revoke':3,'consumers_hidden_after':3})

    def run(self):
        self.setup()
        for tier,old in list(self.proxies.items()):
            old.close(); self.proxies[tier]=TLSProxy(self,tier)
            self.proxies[tier].server.RequestHandlerClass.do_PATCH=self.proxies[tier].server.RequestHandlerClass.do_POST
        self.inputs(); self.import_catalog(); self.configuration_tests(); self.prompt_tests(); self.eval_tests(); self.fault_tests()
        self.revocation_tests()
        before=self.snapshot(); self.agent_detail('v1',6)
        self.call('production','GET','/api/v1/prompts/'+self.prompt,validate=lambda v:prompt_detail(v,self.prompt,self.agent,self.app,self.prompt_text,3))
        check(self.snapshot()==before,'final GET altered business/audit')
        check(self.sql('SELECT count(*) FROM v3_agent_runs',True)=='0'
              and self.sql("SELECT count(*) FROM v3_agent_eval_experiments WHERE status='pending' AND result_ref IS NULL AND finished_at IS NULL",True)=='2'
              and self.sql('SELECT count(*) FROM v3_agent_eval_experiments',True)=='2',
              'metadata fixture must not create Run/Eval executed results')
        self.marker('consistency',{'real_local_HTTP_DB_audit':True,'accepted_source':MODE,'run_rows':0,'eval_pending':2,'eval_results':0,'read_audit_delta':0})
        self.frozen(); self.completed=True
        return dict(resource_run_id=self.run_id,evidence_mode=MODE,normal_targets=8,source='synthetic_signed_catalog',
                    authority_artifact_execution_traffic_evaluation_verified=False,whole_business_chain=False)

    def cleanup(self):
        try: unresolved=self.recover_intents()
        except BaseException as error:
            # Evidence persistence/cancellation failures never bypass cleanup
            # of already acquired resources or become a clean success.
            unresolved=[dict(kind='intent-reconciliation',error_type=type(error).__name__)]
        original_emit=promotion.emit
        def cleanup_emit(name,status,actual,**fields):
            if name == 'promotion.cleanup' and status == 'passed' and unresolved:
                status='blocked';actual=dict(actual,creation_intents_unresolved=unresolved)
            original_emit(name,status,actual,**fields)
        promotion.emit=cleanup_emit
        try: super().cleanup()
        finally: promotion.emit=original_emit
        if unresolved:
            emit('agent_catalog.cleanup','blocked',{'creation_intents_unresolved':unresolved},evidence_mode='real_local')
            raise Blocked('uncertain creation outcome; exact resources retained for reconciliation')
        absent={}
        for port in [*sum((list(v) for v in PORTS.values()),[]),PROXY_PORT]:
            with socket.socket() as sock: absent[str(port)]=sock.connect_ex(('127.0.0.1',port))!=0
        check(all(absent.values()),'owned catalog interface listener residual')
        emit('agent_catalog.cleanup','passed',{'ports_absent':absent,'original_exact_DB_role_project_PAT_cleanup_verified':True},evidence_mode='real_local')

def inventory():
    return dict(mode='offline_preparation',targets=TARGETS,source_fingerprint=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,ports=PORTS,gitlab_control_proxy_port=PROXY_PORT,required_markers=REQUIRED_CASES,
        accepted_input_mode=MODE,source_authority='pending',whole_business_chain=False,
        excluded=['model','Run/Span','evaluation','traffic router','artifact bytes','legacy MCP','CI-CD','performance','24h'])

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--live',action='store_true'); parser.add_argument('--inventory',action='store_true')
    parser.add_argument('--run-id'); parser.add_argument('--admission'); args=parser.parse_args(argv)
    if not args.live: print(json.dumps(inventory(),ensure_ascii=False)); return 0
    try:
        check(re.fullmatch('[a-f0-9]{32}',args.run_id or '') is not None and args.admission,'root admitted fresh UUID required')
        admission(args.admission,args.run_id)
        with promotion.cooperative_signals(),promotion.exclusive(ROOT):
            backend,gitlab=promotion.doctor(ROOT)
            chain=Chain(ROOT,backend,gitlab,args.run_id,args.admission)
            result=promotion.execute(chain); emit('agent_catalog.complete','passed',result); return 0
    except BaseException as error:
        emit('agent_catalog.failure','blocked' if isinstance(error,Blocked) else 'failed',
             {'error_type':type(error).__name__,'error':promotion.safe_error(error)})
        return 2 if isinstance(error,Blocked) else 1

if __name__=='__main__': raise SystemExit(main())
