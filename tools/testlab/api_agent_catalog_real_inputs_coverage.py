"""Real owned GitLab artifact bytes / approved MR input proof.

Eight consumers remain source-protocol-limited. No formal native exporter,
artifact runtime attestation, activation, inference or actual evaluation claim.
"""
from __future__ import annotations
import argparse,base64,hashlib,importlib.util,json,os,re,sqlite3,stat,urllib.parse,uuid
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2]
MODULE='agent-catalog-real-inputs'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
OWNER='testlab-3cbde3095125'
PORTS={'source':(18756,18757),'production':(18758,18759)}
PROXY_PORT=18760
REQUIRED_CASES=tuple('agent_real_inputs.'+n for n in
    ('repo_artifacts','reviewed_import','metadata_contracts','consistency','cleanup'))
_spec=importlib.util.spec_from_file_location('real_inputs_private_catalog',ROOT/'tools/testlab/api_agent_catalog_interfaces_coverage.py')
base=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(base)
base.MODULE,base.PORTS,base.PROXY_PORT=MODULE,PORTS,PROXY_PORT
base.promotion.PORTS=PORTS
base.promotion.GITLAB_PROXY_PORT=PROXY_PORT
base.promotion.GITLAB_PROXY_ORIGIN='https://localhost:'+str(PROXY_PORT)
check,Blocked=base.check,base.Blocked

def hashes():
    names=('api_agent_catalog_real_inputs_coverage.py','tests/test_api_agent_catalog_real_inputs_coverage.py',
           'api_agent_catalog_interfaces_coverage.py','promotion_chain.py','api_readonly_chain.py',
           'identity.py','backend.py','env.py','faults.py','runner.py','inventory.py','store.py','gitops.py')
    return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in names}
base.hashes=hashes

def emit(name,status,actual,**fields):
    base.emit(name,status,actual,**fields)

def admission(path, run_id):
    p = Path(path)
    check(p.is_absolute() and p.resolve() == p and p.is_relative_to(ROOT/'.local/testlab'), 'private root admission path required')
    base.digest(p, True); data = json.loads(p.read_text())
    check(data.get('status') == 'root-admitted-agent-catalog-real-inputs'
          and data.get('owner') == OWNER and data.get('resource_run_id') == run_id and data.get('source_tree_sha') == SOURCE
          and data.get('binary_sha256') == BINARY and data.get('runtime_sha256') == RUNTIME
          and data.get('postgres_cid') == base.promotion.PG_CID and data.get('gitlab_cid') == base.promotion.GITLAB_CID
          and data.get('ports') == {k:list(v) for k,v in PORTS.items()}
          and data.get('helper_sha256') == hashes(), 'root admitted source/helpers/UUID/resources mismatch')
    for key in ('fresh_nonce_hmac_process_tuple_verified', 'headroom_verified',
                'gitlab_recovery_credentials_verified', 'permanent_own_project_cleanup_authorized'):
        check(data.get(key) is True, 'root actual admission prerequisite absent')
    check(base.digest(ROOT/'.local/testlab/backend-fixture.json', True) == data.get('backend_fixture_sha256')
          and base.digest(ROOT/'.local/testlab/gitops-environment.json', True) == data.get('gitlab_config_sha256')
          and base.digest(ROOT/'.local/testlab/runtime-config.json', True) == RUNTIME,
          'main or recovered credential/config drift')
    config=json.loads((ROOT/'.local/testlab/gitops-environment.json').read_text())
    ca=Path(config.get('ca_file',''))
    check(config.get('owner')==OWNER and config.get('gitlab_name')=='aiops-'+OWNER+'-gitlab'
          and config.get('gitlab_url')==base.promotion.GITLAB_ORIGIN
          and ca.parent.resolve()==ROOT/'.local/testlab/environments'/OWNER/'gitops'
          and base.digest(ca,True)==data.get('gitlab_ca_sha256'),'recovered owned CA/origin drift')
    connection = sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro', uri=True, timeout=5)
    try:
        active = [json.loads(row[0]) for row in connection.execute('SELECT data FROM runs') if json.loads(row[0]).get('status') == 'running']
    finally: connection.close()
    check(len(active) == 1 and active[0].get('selected_suite') == MODULE
          and active[0].get('manifest_sha') == data.get('manifest_sha256')
          and active[0].get('source_tree_sha') == SOURCE and active[0].get('pid') == os.getppid(),
          'sole actual matching Runner required for shared GitLab window')
    return data

base.admission=admission

def verify_blob(value,path,commit,expected):
    check(isinstance(expected,bytes) and 0<len(expected)<=4096,'owned bounded opaque artifact bytes required')
    check(re.fullmatch('[a-f0-9]{40}',commit or '') is not None and path in
          ('agent-artifacts/v1/bundle.bin','agent-artifacts/v2/bundle.bin'),'immutable exact artifact path/commit required')
    check(isinstance(value,dict) and value.get('file_path')==path and value.get('commit_id')==commit
          and value.get('encoding')=='base64' and type(value.get('size')) is int and value['size']==len(expected),
          'GitLab immutable blob tuple differs')
    check(isinstance(value.get('content'),str),'GitLab base64 content required')
    actual=base64.b64decode(value['content'].replace('\n',''),validate=True)
    sha=hashlib.sha256(expected).hexdigest()
    blob=hashlib.sha1(b'blob '+str(len(expected)).encode()+b'\0'+expected).hexdigest()
    check(actual==expected and value.get('content_sha256')==sha and value.get('blob_id')==blob,
          'GitLab bytes/sha256/git blob identity differs')
    return sha

def artifact_manifest(agent,app,version,source,promotion_id,project,commit,sha,project_name):
    check(type(project) is int and project>0 and re.fullmatch('[a-f0-9]{40}',commit or '')
          and re.fullmatch('[a-f0-9]{64}',sha or ''),'exact owned artifact source tuple required')
    check(re.fullmatch('testlab-promotion-[a-f0-9]{32}',project_name or ''),'owned exact project name required')
    value=base.manifest(agent,app,version,source,promotion_id)
    authority=urllib.parse.urlsplit(base.promotion.GITLAB_ORIGIN).netloc
    ref=authority+'/root/'+project_name+'/-/raw/'+commit+'/agent-artifacts/'+version+'/bundle.bin@sha256:'+sha
    check('://' not in ref and len(ref)<=500 and ref.count('@')==1,'safe digest-pinned metadata reference required')
    value['agent_version']['artifact_ref']=ref
    value['source_digest']=hashlib.sha256(base.encoded(value['agent_version'])).hexdigest()
    return value

class Chain(base.Chain):
    def __init__(self,*args,**kwargs):
        super().__init__(*args,**kwargs)
        self.artifacts={}

    def proof_marker(self,suffix,actual,mode='real_local'):
        self.frozen();name='agent_real_inputs.'+suffix
        check(name in REQUIRED_CASES,'unknown real-input marker')
        emit(name,'passed',dict(resource_run_id=self.run_id,**actual),evidence_mode=mode)

    def reviewed_file(self,path,content,title,encoding=None):
        pid=self.projects['main']['id'];ident=uuid.uuid4().hex;branch=self.prefix+ident
        self.assert_project('main')
        self.git('POST',f'/api/v4/projects/{pid}/repository/branches',{'branch':branch,'ref':self.target},role='proposer',want=201)
        body=dict(branch=branch,content=content,commit_message=title)
        if encoding:body['encoding']=encoding
        self.git('POST',f'/api/v4/projects/{pid}/repository/files/'+urllib.parse.quote(path,safe=''),body,role='proposer',want=201)
        mr,_=self.git('POST',f'/api/v4/projects/{pid}/merge_requests',dict(source_branch=branch,target_branch=self.target,
            title=title,labels='aiops-config-promotion'),role='proposer',want=201)
        item=dict(id=ident,iid=mr['iid'],path=path)
        self.review_merge(item)
        return item

    def artifact_blob(self,version,commit,data):
        pid=self.projects['main']['id'];path='agent-artifacts/'+version+'/bundle.bin'
        value,_=self.git('GET',f'/api/v4/projects/{pid}/repository/files/'+urllib.parse.quote(path,safe='')+
            '?ref='+commit,role='reviewer')
        return verify_blob(value,path,commit,data)

    def provision_artifacts(self):
        for version in ('v1','v2'):
            # Opaque physical bytes. No invented executable Agent artifact format.
            data=b'owned-testlab-opaque-artifact\0'+self.run_id.encode()+b'\n'+version.encode()+b'\n'
            path='agent-artifacts/'+version+'/bundle.bin'
            base.promotion.private_bytes(self.work/('opaque-'+version+'.bin'),data)
            item=self.reviewed_file(path,base64.b64encode(data).decode(),'Owned opaque artifact '+version,'base64')
            sha=self.artifact_blob(version,item['merge_sha'],data)
            self.artifacts[version]=dict(data=data,commit=item['merge_sha'],sha256=sha,artifact_mr=item['iid'])
        self.proof_marker('repo_artifacts',dict(physical_artifact_versions=2,immutable_blob_reads=2,
            digest_source='actual bytes, not version string',approved_artifact_MRs=2,
            product_artifact_fetch_or_attestation=False,native_exporter=False))

    def signed_item(self,version):
        source=self.artifacts[version];pid=self.projects['main']['id'];ident=uuid.uuid4().hex
        value=artifact_manifest(self.agent,self.app,version,self.source_id,ident,pid,source['commit'],source['sha256'],self.projects['main']['name'])
        message=self.work/('real-input-message-'+ident);signature=self.work/('real-input-signature-'+ident)
        base.promotion.private_bytes(message,base.encoded(value))
        self.command(['openssl','pkeyutl','-sign','-inkey',str(self.work/'signer.pem'),'-rawin','-in',str(message),'-out',str(signature)])
        os.chmod(signature,0o600);check(len(signature.read_bytes())==64,'Ed25519 signature length differs')
        value['signature']=base64.urlsafe_b64encode(signature.read_bytes()).rstrip(b'=').decode()
        path='ops-config/agent-versions/'+ident+'.json';branch=self.prefix+ident
        self.git('POST',f'/api/v4/projects/{pid}/repository/branches',dict(branch=branch,ref=self.target),role='proposer',want=201)
        self.git('POST',f'/api/v4/projects/{pid}/repository/files/'+urllib.parse.quote(path,safe=''),
            dict(branch=branch,content=base.encoded(value).decode(),commit_message='Owned reviewed artifact reference'),role='proposer',want=201)
        mr,_=self.git('POST',f'/api/v4/projects/{pid}/merge_requests',dict(source_branch=branch,target_branch=self.target,
            title='Owned artifact catalog '+version,labels='aiops-config-promotion'),role='proposer',want=201)
        return dict(id=ident,iid=mr['iid'],path=path,manifest=value)

    def import_real_inputs(self):
        for version in ('v1','v2'):
            item=self.signed_item(version);self.review_merge(item)
            self.call('production','POST','/api/v1/config-sync/mr-'+str(item['iid'])+'/import',scenario='fixture')
            ref=self.sql('SELECT artifact_ref FROM v3_agent_versions WHERE agent_id='+base.q(self.agent)+
                ' AND version='+base.q(version),True)
            check(ref==item['manifest']['agent_version']['artifact_ref'],'imported real artifact reference differs')
            source=self.artifacts[version]
            check(self.artifact_blob(version,source['commit'],source['data'])==source['sha256'],'artifact bytes drift')
        self.proof_marker('reviewed_import',dict(actual_GitLab_repo_blob=True,approved_manifest_MRs=2,
            exact_digest_reference_persisted=True,synthetic_signing_exporter=True,source_metadata_native_exported=False,
            product_checks_signature_snapshot_digest_and_reference=True,product_fetches_artifact_bytes=False))

    def metadata_normal(self):
        path='/api/v1/agents/'+self.agent
        self.agent_detail('',1)
        self.call('production','POST',path+'/rollout',dict(to='v1',percent=100,override=True,
            override_reason='Owned real artifact input; configuration only',expected_revision=1),
            validate=lambda v:base.configuration(v,2,to='v1',configured_percent=100,override=True))
        self.call('production','POST',path+'/rollout',dict(to='v2',percent=25,override=True,
            override_reason='Owned real artifact canary metadata',expected_revision=2),
            validate=lambda v:base.configuration(v,3,to='v2',configured_percent=25,override=True))
        self.call('production','POST',path+'/rollback',dict(expected_revision=3),
            validate=lambda v:base.configuration(v,4,to='v1',configured_canary_percent=0))
        self.call('production','PATCH',path+'/level',dict(level='L0',expected_revision=4),
            validate=lambda v:base.configuration(v,5,level='L0',max_level='L0'))
        text='Owned stored prompt '+self.run_id+'\n{{service}}'
        created,_=self.call('production','POST','/api/v1/prompts',dict(agent_id=self.agent,name='Owned prompt',text=text),want=201,scenario='fixture')
        check(re.fullmatch('[a-f0-9]{32}',created.get('id','')) and created.get('version')=='v1','Prompt registration differs')
        prompt=created['id'];new=text+'\nSaved version'
        self.call('production','POST','/api/v1/prompts/'+prompt+'/versions',dict(text=new),want=201,
            validate=lambda v:check(v=={'version':'v2'},'Prompt revision differs'))
        self.call('production','GET','/api/v1/prompts/'+prompt,
            validate=lambda v:base.prompt_detail(v,prompt,self.agent,self.app,new,2))
        title='Owned pending Eval '+self.run_id
        result,_=self.call('production','POST','/api/v1/evals',dict(agent_id=self.agent,title=title,baseline_version='v1',candidate_version='v2'),want=201,
            validate=lambda v:check(re.fullmatch('[a-f0-9]{32}',v.get('id','')) and v.get('status')=='pending'
                and v.get('execution_available') is False,'Eval pending metadata differs'))
        self.call('production','GET','/api/v1/evals/'+result['id'],validate=lambda v:base.eval_metadata(v,result['id'],title))
        self.proof_marker('metadata_contracts',dict(normal_routes=8,real_binary_HTTP=True,source_mode='protocol_mock',
            routing_connected=False,execution_available=False,contract='metadata/configuration only'),mode='protocol_mock')

    def run(self):
        self.setup()
        for tier,old in list(self.proxies.items()):
            old.close();self.proxies[tier]=base.TLSProxy(self,tier)
            self.proxies[tier].server.RequestHandlerClass.do_PATCH=self.proxies[tier].server.RequestHandlerClass.do_POST
        self.inputs();self.provision_artifacts();self.import_real_inputs();self.metadata_normal()
        before=self.snapshot();self.agent_detail('v1',5)
        check(before==self.snapshot(),'Agent GET mutated metadata/audit')
        check(self.sql('SELECT count(*) FROM v3_agent_runs',True)=='0' and
            self.sql("SELECT count(*) FROM v3_agent_eval_experiments WHERE status='pending' AND result_ref IS NULL AND finished_at IS NULL",True)=='1',
            'must remain pending metadata without fabricated runs/results')
        for version,source in self.artifacts.items():self.artifact_blob(version,source['commit'],source['data'])
        self.proof_marker('consistency',dict(artifact_versions_reverified=2,run_rows=0,pending_evals=1,
            native_source_export=False,product_artifact_attestation=False,whole_business_chain=False))
        self.frozen();self.completed=True
        return dict(resource_run_id=self.run_id,real_input_bytes=True,evidence_mode='protocol_mock',
            synthetic_exporter=True,normal_metadata_routes=8,whole_business_chain=False)

    def cleanup(self):
        super().cleanup()
        emit('agent_real_inputs.cleanup','passed',dict(exact_owned_project_PAT_DB_role_and_Popen_cleanup=True),evidence_mode='real_local')

def inventory():
    return dict(status='prepare_only',module=MODULE,targets=base.TARGETS,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,ports=PORTS,proxy_port=PROXY_PORT,required_markers=REQUIRED_CASES,
        real_input_scope='actual opaque repo artifact bytes, SHA256, immutable commits, independent approved MR',
        source_exporter='synthetic; native producer gap preserved',consumer_mode='protocol_mock',
        prohibited=['artifact execution','model','CI/CD','Eval result producer','source SQL business rows','full chain','performance','24h'])

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--live',action='store_true')
    parser.add_argument('--inventory',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission');args=parser.parse_args(argv)
    if not args.live:print(json.dumps(inventory(),ensure_ascii=False));return 0
    try:
        check(re.fullmatch('[a-f0-9]{32}',args.run_id or '') and args.admission,'fresh root-admitted UUID required')
        admission(args.admission,args.run_id)
        with base.promotion.cooperative_signals(),base.promotion.exclusive(ROOT):
            backend,gitlab=base.promotion.doctor(ROOT)
            result=base.promotion.execute(Chain(ROOT,backend,gitlab,args.run_id,args.admission))
            emit('agent_real_inputs.complete','passed',result,evidence_mode='protocol_mock');return 0
    except BaseException as error:
        emit('agent_real_inputs.failure','blocked' if isinstance(error,Blocked) else 'failed',
            dict(error_type=type(error).__name__,error=base.promotion.safe_error(error)))
        return 2 if isinstance(error,Blocked) else 1

if __name__=='__main__':raise SystemExit(main())
