"""Owned Kubernetes → formal inventory → nonempty services/discover.

Explicit root admission and --live only. Existing workloads are read, never
created or changed. Kubernetes writes are confined to three UUID auth fixtures
and a short TokenRequest. No SQL-created CMDB/business snapshots, model calls,
Argo/CI/VM commands, namespace mutation, or external_snapshot shortcuts.
"""
from __future__ import annotations
import argparse, base64, datetime, fcntl, hashlib, http.client, importlib.util
import json, os, re, socket, sqlite3, ssl, stat, subprocess, threading, time, uuid
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

ROOT=Path(__file__).resolve().parents[2]
OWNER='testlab-3cbde3095125'
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
KIND_CID='eac66a03a8006c3fb030962710a9fbb2f3632d92a51d2714f1b241db67071905'
KIND_IMAGE='sha256:664b3989afaffcd2268ece28d6cf012b27700e6b8e81c3c7641cc167889075f5'
CLUSTER='aiops-backend-testlab'; CONTEXT='kind-'+CLUSTER
SERVER='https://127.0.0.1:55840'; NAMESPACE='testlab-rules'; PORTS=(18774,18775)
KUBECONFIG=ROOT/'.local/testlab/environments'/OWNER/'gitops/kubeconfig'
RECEIPT=ROOT/'.local/testlab/cp300-kubernetes-read-preflight/7860fda3-2e32-4d12-b797-dcc0a0a6849c/kube-read.json'
NAMESPACE_RECEIPT=RECEIPT.with_name('namespace-read.json')
MODULE='formal-api-service-discovery-real'
TARGETS=(('GET','/services/discover'),); SUPPORT=(('POST','/auth/login'),)
REQUIRED_CASES=tuple('discovery_real.'+x for x in ('auth_scope','real_lists','native_snapshot',
    'nonempty','second_api','authorization','parameters','revocation','ledger','cleanup'))
ENDPOINTS=(('/api/v1/namespaces','Namespace'),('/api/v1/nodes','Node'),
    ('/apis/apps/v1/deployments','Deployment'),('/apis/apps/v1/statefulsets','StatefulSet'),
    ('/apis/apps/v1/daemonsets','DaemonSet'),('/apis/apps/v1/replicasets','ReplicaSet'),
    ('/apis/batch/v1/jobs','Job'),('/apis/batch/v1/cronjobs','CronJob'),('/api/v1/pods','Pod'),
    ('/api/v1/services','Service'),('/apis/networking.k8s.io/v1/ingresses','Ingress'),
    ('/api/v1/persistentvolumes','PersistentVolume'),('/api/v1/persistentvolumeclaims','PersistentVolumeClaim'),
    ('/apis/storage.k8s.io/v1/storageclasses','StorageClass'),('/api/v1/events','Event'))
RULES=[dict(apiGroups=[''],resources=['namespaces','nodes','pods','services','persistentvolumes','persistentvolumeclaims','events'],verbs=['get','list','watch']),
    dict(apiGroups=['apps'],resources=['deployments','statefulsets','daemonsets','replicasets'],verbs=['get','list','watch']),
    dict(apiGroups=['batch'],resources=['jobs','cronjobs'],verbs=['get','list','watch']),
    dict(apiGroups=['networking.k8s.io'],resources=['ingresses'],verbs=['get','list','watch']),
    dict(apiGroups=['storage.k8s.io'],resources=['storageclasses'],verbs=['get','list','watch'])]
_spec=importlib.util.spec_from_file_location('discovery_real_private_readonly',ROOT/'tools/testlab/api_readonly_chain.py')
ro=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(ro)
ro.PORTS,ro.BASES,ro.GETS=PORTS,tuple('http://127.0.0.1:'+str(p) for p in PORTS),()
check,Blocked,q=ro.check,ro.Blocked,ro.quote
_inspect=ro.inspect_pg

def inspect_pg(*args,**kwargs):
    info=_inspect(*args,**kwargs);check(info.get('Id')==PG_CID,'exact PG CID differs');return info
ro.inspect_pg=inspect_pg

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:
        fields['interface']=dict(fields['interface'],evidence_mode='real_local',test_name='FormalServiceDiscoveryReal',
            upstream_mode='owned_real_kubernetes',proof_scope='formal inventory15LIST→convert→SaveSnapshot; existing lab workloads; no deployment writes')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
ro.emit=emit

def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {x:digest(ROOT/'tools/testlab'/x) for x in ('api_service_discovery_real_source_coverage.py',
        'tests/test_api_service_discovery_real_source_coverage.py','api_readonly_chain.py','runner.py','store.py','faults.py')}
def private_regular(path):
    path=Path(path)
    check(path.is_relative_to(ROOT/'.local/testlab') and path.resolve()==path,'private exact path required')
    for p in (path,*path.parents):
        if p==ROOT:break
        check(not p.is_symlink(),'private path traverses symlink')
    info=path.stat()
    check(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode)==0o600 and info.st_uid==os.getuid()
          and info.st_size<=2<<20,'private owner/mode/size differs')
    return path
def remaining(deadline,maximum=8):
    if deadline is None:return maximum
    value=min(maximum,deadline-time.monotonic())
    if value<=0:raise Blocked('shared auth cleanup deadline exhausted')
    return value

def guard_kind(info,pinned=None):
    labels=info.get('Config',{}).get('Labels',{})
    check(info.get('Id')==KIND_CID and info.get('Image')==KIND_IMAGE
        and info.get('Name')=='/'+CLUSTER+'-control-plane' and info.get('State',{}).get('Running') is True
        and labels.get('aiops.testlab.owner')==OWNER and labels.get('io.x-k8s.kind.cluster')==CLUSTER
        and labels.get('io.x-k8s.kind.role')=='control-plane'
        and info.get('NetworkSettings',{}).get('Ports',{}).get('6443/tcp')==[{'HostIp':'127.0.0.1','HostPort':'55840'}],
        'exact owned kind identity/listener differs')
    identity=dict(container_id=info['Id'],image_id=info['Image'],started_at=info['State']['StartedAt'])
    check(pinned is None or identity==pinned,'owned kind immutable runtime drift')
    return identity

def auth_specs(run_id):
    nonce=uuid.UUID(run_id).hex;check(str(uuid.UUID(run_id))==run_id,'canonical resource UUID required')
    name='discovery-'+nonce
    labels={'aiops.testlab.owner':OWNER,'aiops.testlab.run':run_id,'aiops.testlab.purpose':'service-discovery-read'}
    sa=dict(apiVersion='v1',kind='ServiceAccount',metadata=dict(name=name,namespace=NAMESPACE,labels=labels),automountServiceAccountToken=False)
    role=dict(apiVersion='rbac.authorization.k8s.io/v1',kind='ClusterRole',metadata=dict(name=name,labels=labels),rules=RULES)
    binding=dict(apiVersion='rbac.authorization.k8s.io/v1',kind='ClusterRoleBinding',metadata=dict(name=name,labels=labels),
        roleRef=dict(apiGroup='rbac.authorization.k8s.io',kind='ClusterRole',name=name),
        subjects=[dict(kind='ServiceAccount',name=name,namespace=NAMESPACE)])
    return [('/api/v1/namespaces/'+NAMESPACE+'/serviceaccounts',sa),
        ('/apis/rbac.authorization.k8s.io/v1/clusterroles',role),
        ('/apis/rbac.authorization.k8s.io/v1/clusterrolebindings',binding)]

def owned_auth(value,spec,uid=None):
    meta=value.get('metadata',{});expected=spec['metadata']
    check(value.get('kind')==spec['kind'] and meta.get('name')==expected['name']
        and meta.get('namespace','')==expected.get('namespace','')
        and all(meta.get('labels',{}).get(k)==v for k,v in expected['labels'].items())
        and isinstance(meta.get('uid'),str) and bool(re.fullmatch('[a-f0-9-]{36}',meta['uid']))
        and (uid is None or meta['uid']==uid),'exact auth UUID/UID/owner differs')
    if spec['kind']=='ClusterRole':check(value.get('rules')==RULES,'readonly inventory role differs')
    if spec['kind']=='ClusterRoleBinding':check(value.get('roleRef')==spec['roleRef'] and value.get('subjects')==spec['subjects'],'exact readonly binding differs')
    if spec['kind']=='ServiceAccount':check(value.get('automountServiceAccountToken') is False,'implicit credential mounting enabled')
    return meta['uid']

def kube_allowed(method,path,admin,run_id):
    parsed=urlsplit(path)
    if parsed.scheme or parsed.netloc or parsed.fragment:return False
    if not admin:
        if method=='POST':return path=='/apis/authorization.k8s.io/v1/selfsubjectaccessreviews'
        query=parse_qs(parsed.query,keep_blank_values=True)
        return method=='GET' and parsed.path in {x[0] for x in ENDPOINTS} and (not query or query=={'limit':['500']})
    if method=='GET' and path=='/api/v1/namespaces/'+NAMESPACE:return True
    for collection,spec in auth_specs(run_id):
        item=collection+'/'+spec['metadata']['name']
        if method=='POST' and path==collection:return True
        if method in ('GET','DELETE') and path==item:return True
        if spec['kind']=='ServiceAccount' and method=='POST' and path==item+'/token':return True
    return False

def projected_replicas(original):
    # inventory.convert: DaemonSet uses status, other workload kinds default to 1.
    if original['kind']=='DaemonSet':return original.get('status',{}).get('desiredNumberScheduled',0)
    return original.get('spec',{}).get('replicas',1)

def resource_id(source_id,uid):
    return 'inventory-'+hashlib.sha256(('kubernetes\0'+source_id+'\0'+uid).encode()).hexdigest()[:40]

def validate_discovery(value,expected,source_id,empty=False,cluster=CLUSTER):
    check(value.get('cluster')==cluster and value.get('registered')==0
          and value.get('ambiguous_workloads')==0 and isinstance(value.get('items'),list),'discovery root fields differ')
    if empty:
        check(set(value)=={'cluster','registered','ambiguous_workloads','items'} and value['items']==[]
            and type(value['registered']) is int and type(value['ambiguous_workloads']) is int,
            'hidden/empty discovery leaked workload metadata or changed exact DTO')
        return
    rows=value['items'];check(bool(rows) and len(rows)==len(expected) and {r.get('uid') for r in rows}==set(expected),'nonempty real workload UID set differs')
    for item in rows:
        original=expected[item['uid']];meta=original['metadata'];kind=original['kind'];spec=original['spec']
        image=spec.get('template',{}).get('spec',{}).get('containers',[{}])[0].get('image','')
        check(item.get('id')==meta['name'] and item.get('namespace')==meta['namespace'] and item.get('kind')==kind
            and item.get('environment_id')=='test' and item.get('cluster')==CLUSTER and item.get('image')==image
            and item.get('replicas')==projected_replicas(original) and item.get('resource_id')==resource_id(source_id,item['uid'])
            and item.get('matched')==[] and item.get('missing')==[] and item.get('owner_hint')=='Unknown',
            'original real workload scope/spec projection differs')
        labels=meta.get('labels',{})
        check(item.get('owner_label_hint','')==('team='+labels['team'].strip() if labels.get('team','').strip() else '')
            and item.get('tier_hint','')==(labels.get('tier','').strip() if labels.get('tier','').strip() in ('T0','T1','T2') else ''),
            'labels became verified ownership/tier')
        source=item.get('source',{})
        check(source.get('kind')=='kubernetes' and source.get('id')==source_id,'source provenance differs')
        for key in ('observed_at','last_success_at'):
            at=datetime.datetime.fromisoformat(source[key].replace('Z','+00:00'))
            age=(datetime.datetime.now(datetime.timezone.utc)-at).total_seconds()
            check(-60<=age<=300,'real inventory observation is stale')
        unavailable=['health','verified_tier','connector_coverage','runbook_gaps','verified_owner']
        if not image:unavailable.append('image')
        if projected_replicas(original) is None:unavailable.append('desired_replicas')
        check(item.get('unavailable_fields')==unavailable,'unknown fields were fabricated or removed')

class Contract(ro.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);canonical,kind=super().canonical(method,path)
        check((method,canonical[len('/api/v1'):]) in (*TARGETS,*SUPPORT)
            and not(parsed.scheme or parsed.netloc or parsed.fragment),'outside independent discovery inventory')
        if canonical!='/api/v1/services/discover':check(not parsed.query,'login query forbidden')
        return canonical,kind

def runner_admission(permit):
    connection=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:
        active=[json.loads(row[0]) for row in connection.execute('SELECT data FROM runs') if json.loads(row[0]).get('status')=='running']
    finally:connection.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE
        and active[0].get('manifest_sha')==permit.get('manifest_sha256')
        and isinstance(permit.get('manifest_sha256'),str) and re.fullmatch('[a-f0-9]{64}',permit['manifest_sha256'])
        and active[0].get('source_tree_sha')==SOURCE and active[0].get('pid')==os.getppid(),
        'sole actual source/manifest/parent Runner required before auth acquisition')

def doctor(root,admission,run_id):
    check(Path(root).resolve()==ROOT and str(uuid.UUID(run_id))==run_id,'reviewed root/canonical UUID required')
    permit=json.loads(private_regular(admission).read_text())
    check(permit.get('status')=='root-admitted-service-discovery-real' and permit.get('resource_run_id')==run_id
          and permit.get('owner')==OWNER and permit.get('helper_sha256')==hashes()
          and permit.get('ports')==list(PORTS) and permit.get('sole_runner_verified') is True
          and permit.get('headroom_verified') is True and permit.get('auth_fixture_cleanup_authorized') is True,
          'fresh root resource admission required')
    check(all(permit.get(k)==v for k,v in dict(source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,kind_cid=KIND_CID,kind_image_id=KIND_IMAGE).items()),'root exact frozen source/dependency admission differs')
    runner_admission(permit)
    data=ro.doctor(root)
    check(data['source_tree_sha']==SOURCE and data['binary_sha256']==BINARY and data['runtime_sha']==RUNTIME,
          'frozen Go/nonrace/runtime differs')
    info=guard_kind(_inspect(KIND_CID))
    receipt=json.loads(private_regular(RECEIPT).read_text())
    check(receipt.get('owner')==OWNER and receipt.get('node_cid')==KIND_CID and receipt.get('image')==KIND_IMAGE
          and receipt.get('server')==SERVER and receipt.get('context')==CONTEXT
          and receipt.get('write_performed') is False and digest(private_regular(KUBECONFIG))==receipt['config_sha256']
          and permit.get('kubeconfig_sha256')==receipt['config_sha256'] and permit.get('receipt_sha256')==digest(RECEIPT),
          'owned dedicated Kubernetes input receipt differs')
    namespace=json.loads(private_regular(NAMESPACE_RECEIPT).read_text())
    check(namespace.get('namespace')==NAMESPACE and namespace.get('owner')==OWNER and namespace.get('phase')=='Active'
        and namespace.get('write_performed') is False and permit.get('namespace_receipt_sha256')==digest(NAMESPACE_RECEIPT)
        and permit.get('namespace_uid')==namespace.get('uid'),'owned existing Namespace admission differs')
    return dict(data,helper_sha256=hashes(),kind_identity=info,kubeconfig_sha256=receipt['config_sha256'],receipt=receipt,namespace_uid=namespace['uid'])

class Chain(ro.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id)
        self.contract=Contract(root);self.auth=[];self.unknown=[];self.read_token='';self.expected={}
        self.source_id='discovery-kube-'+self.nonce;self.tls_admin=None;self.tls_reader=None
        self.private_hashes={}
        self.state.update(suite=MODULE,ports=list(PORTS),sql_business_result_writes=0,workload_writes=0,
            collector_instances=1,source_id=self.source_id)
        ro._private(self.work/'state.json',self.state)

    def frozen(self):
        super().frozen();check(hashes()==self.data['helper_sha256'],'helper source drift')
        check(digest(private_regular(KUBECONFIG))==self.data['kubeconfig_sha256'],'dedicated kubeconfig drift')
        check(all(digest(private_regular(path))==sha for path,sha in self.private_hashes.items()),'private inventory/auth input drift')

    def marker_case(self,name,actual):
        check('discovery_real.'+name in REQUIRED_CASES,'unknown discovery marker')
        self.frozen();emit('discovery_real.'+name,'passed',dict(run_id=self.run_id,**actual))

    def kube(self,method,path,body=None,want=200,admin=False,deadline=None):
        check(kube_allowed(method,path,admin,self.run_id),'Kubernetes request outside bounded auth/read paths')
        guard_kind(_inspect(KIND_CID,timeout=remaining(deadline,3)),self.data['kind_identity'])
        check(digest(private_regular(KUBECONFIG))==self.data['kubeconfig_sha256'],'Kubernetes credential source drift')
        context=self.tls_admin if admin else self.tls_reader
        conn=http.client.HTTPSConnection('127.0.0.1',55840,context=context,timeout=remaining(deadline))
        headers={'Content-Type':'application/json','Accept':'application/json'}
        if not admin:headers['Authorization']='Bearer '+self.read_token
        timer=None
        try:
            if deadline is not None:
                def expire():
                    try:
                        if conn.sock is not None:conn.sock.shutdown(socket.SHUT_RDWR)
                    except OSError:pass
                timer=threading.Timer(remaining(deadline),expire);timer.daemon=True;timer.start()
            conn.connect()
            if deadline is not None:
                conn.sock.settimeout(remaining(deadline))
            conn.request(method,path,None if body is None else json.dumps(body).encode(),headers)
            if deadline is not None:conn.sock.settimeout(remaining(deadline))
            response=conn.getresponse();raw=response.read((2<<20)+1);remaining(deadline)
            check(response.status==want and len(raw)<=2<<20,'exact Kubernetes status/body bound differs')
            return json.loads(raw) if raw else {}
        finally:
            if timer:timer.cancel()
            conn.close()

    def prepare_auth(self):
        # Local config view only, explicit dedicated context; never log raw key material.
        guard_kind(_inspect(KIND_CID),self.data['kind_identity']);self.frozen()
        result=subprocess.run(['kubectl','--kubeconfig',str(KUBECONFIG),'--context',CONTEXT,
            'config','view','--raw','--minify','-o','json'],capture_output=True,timeout=8,env=ro.command_env({},{}))
        check(result.returncode==0 and len(result.stdout)<=65536,'dedicated local config view failed')
        value=json.loads(result.stdout);check(value.get('current-context')==CONTEXT
            and len(value.get('clusters',[]))==len(value.get('users',[]))==1,'dedicated context identity differs')
        cluster=value['clusters'][0]['cluster'];user=value['users'][0]['user']
        check(cluster.get('server')==SERVER and not cluster.get('insecure-skip-tls-verify')
              and set(user)=={'client-certificate-data','client-key-data'},'only exact owned mTLS credentials supported')
        for field,name in [('certificate-authority-data','kube-ca.pem')]:
            ro.private_file(self.work/name,base64.b64decode(cluster[field],validate=True).decode())
        for field,name in [('client-certificate-data','admin-cert.pem'),('client-key-data','admin-key.pem')]:
            ro.private_file(self.work/name,base64.b64decode(user[field],validate=True).decode())
        self.tls_reader=ssl.create_default_context(cafile=self.work/'kube-ca.pem')
        self.tls_admin=ssl.create_default_context(cafile=self.work/'kube-ca.pem')
        self.tls_admin.load_cert_chain(self.work/'admin-cert.pem',self.work/'admin-key.pem')
        ns=self.kube('GET','/api/v1/namespaces/'+NAMESPACE,admin=True)
        check(ns.get('metadata',{}).get('labels',{}).get('aiops.testlab.owner')==OWNER
            and ns['metadata'].get('uid')==self.data['namespace_uid'] and ns.get('status',{}).get('phase')=='Active'
            and not ns['metadata'].get('deletionTimestamp'),'existing auth namespace owner/UID unavailable')
        for collection,spec in auth_specs(self.run_id):
            self.kube('GET',collection+'/'+spec['metadata']['name'],want=404,admin=True)
        for collection,spec in auth_specs(self.run_id):
            item=collection+'/'+spec['metadata']['name']
            self.unknown.append(item)
            ro._private(self.work/'auth-intents.json',dict(unknown=self.unknown,known=self.auth))
            value=self.kube('POST',collection,spec,want=201,admin=True)
            ro._private(self.work/('auth-response-'+spec['kind']+'.json'),value)
            uid=owned_auth(value,spec)
            self.auth.append(dict(path=item,uid=uid,spec=spec));self.unknown.remove(item)
            ro._private(self.work/'auth-intents.json',dict(unknown=self.unknown,known=self.auth))
        sa=self.auth[0]
        token=self.kube('POST',sa['path']+'/token',dict(apiVersion='authentication.k8s.io/v1',kind='TokenRequest',
            spec=dict(expirationSeconds=600)),want=201,admin=True)
        self.read_token=token.get('status',{}).get('token','')
        check(isinstance(self.read_token,str) and 0<len(self.read_token)<=16384
            and not any(c in self.read_token for c in ('\r','\n','\0')),'short readonly token missing')
        expires=datetime.datetime.fromisoformat(token['status']['expirationTimestamp'].replace('Z','+00:00'))
        check(0<(expires-datetime.datetime.now(datetime.timezone.utc)).total_seconds()<=660,'requested short auth expiry differs')
        ro.private_file(self.work/'read-token',self.read_token)
        self.private_hashes={self.work/name:digest(self.work/name) for name in ('kube-ca.pem','admin-cert.pem','admin-key.pem','read-token')}
        for group,resource,verb in [('', 'secrets','get'),('', 'configmaps','get'),('apps','deployments','create'),('', 'pods/exec','create')]:
            review=self.kube('POST','/apis/authorization.k8s.io/v1/selfsubjectaccessreviews',dict(
                apiVersion='authorization.k8s.io/v1',kind='SelfSubjectAccessReview',
                spec=dict(resourceAttributes=dict(group=group,resource=resource,verb=verb,namespace=NAMESPACE))),want=201)
            check(review.get('status',{}).get('allowed') is False,'readonly auth unexpectedly permits sensitive/write action')
        self.marker_case('auth_scope',dict(new_auth_objects=3,namespace_created=False,ttl_seconds=600,
            verbs=['get','list','watch'],sensitive_and_write_ssar_denied=4,workload_mutations=0))

    def real_lists(self):
        objects=[];counts={}
        for endpoint,kind in ENDPOINTS:
            value=self.kube('GET',endpoint+'?limit=500')
            check(isinstance(value.get('items'),list) and value.get('metadata',{}).get('resourceVersion')
                and not value['metadata'].get('continue'),'bounded initial owned LIST incomplete; no truncation accepted')
            counts[kind]=len(value['items'])
            for item in value['items']:
                item['kind']=kind;objects.append(item)
        workloads=[x for x in objects if x['kind'] in ('Deployment','StatefulSet','DaemonSet')]
        names=[x['metadata']['name'].lower() for x in workloads]
        check(len(names)==len(set(names)),'real workload names ambiguous; do not weaken original candidate assertion')
        self.expected={x['metadata']['uid']:x for x in workloads}
        core=next((x for x in self.data['receipt']['deployments'] if x['namespace']=='kube-system' and x['name']=='coredns'),None)
        check(core is not None and core['uid'] in self.expected and self.expected[core['uid']]['metadata']['namespace']=='kube-system',
            'preexisting admitted real workload UID missing')
        # Store only projected workload fields, not raw Pods or environment payloads.
        projected=[dict(uid=x['metadata']['uid'],name=x['metadata']['name'],namespace=x['metadata']['namespace'],
            kind=x['kind'],resource_version=x['metadata'].get('resourceVersion'),replicas=projected_replicas(x),
            images=[c['image'] for c in x['spec'].get('template',{}).get('spec',{}).get('containers',[])]) for x in workloads]
        ro._private(self.work/'actual-list-receipt.json',dict(counts=counts,workloads=projected))
        self.marker_case('real_lists',dict(list_kinds=15,existing_workloads=len(workloads),real_uid=True,synthetic_snapshots=False))
        ro._private(self.work/'inventory.json',dict(interval_seconds=10,sources=[dict(id=self.source_id,name=CLUSTER,
            api_url=SERVER,ca_file=str(self.work/'kube-ca.pem'),token_file=str(self.work/'read-token'),
            environment_key='test',grant_permission_ids=[],application_bindings=[])]))
        self.private_hashes[self.work/'inventory.json']=digest(self.work/'inventory.json')

    def apply_startup_environment(self,variables):
        result=super().apply_startup_environment(variables)
        if variables['AIOPS_ADDR']=='127.0.0.1:'+str(PORTS[0]):result['AIOPS_INVENTORY_CONFIG']=str(self.work/'inventory.json')
        return result

    def call(self,*args,**kwargs):
        self.frozen();validator=kwargs.get('validate')
        def safe(value):
            check(not self.read_token or self.read_token not in json.dumps(value),'formal DTO leaked readonly credential')
            if validator:validator(value)
        return super().call(*args,**dict(kwargs,validate=safe))

    def ledger(self):
        return json.loads(self.sql("SELECT json_build_object('applications',(SELECT count(*) FROM applications),'links',(SELECT count(*) FROM cmdb_resource_applications),'audit',(SELECT COALESCE(json_agg(to_jsonb(a) ORDER BY id),'[]'::json) FROM management_audit a),'candidates',(SELECT count(*) FROM v3_service_mapping_candidates),'investigations',(SELECT count(*) FROM investigations),'proposals',(SELECT count(*) FROM v3_proposals),'actions',(SELECT count(*) FROM action_plans),'executions',(SELECT count(*) FROM executions))",read=True))

    def native_snapshot(self):
        deadline=time.monotonic()+25
        while True:
            value=json.loads(self.sql("SELECT json_build_object('source',(SELECT to_jsonb(s) FROM cmdb_sync_sources s WHERE id="+q(self.source_id)+"),'workloads',(SELECT COALESCE(json_agg(json_build_object('external_id',external_id,'source_id',source_id,'source_kind',source_kind,'is_mock',is_mock,'attributes',attributes,'observed_at',observed_at) ORDER BY external_id),'[]'::json) FROM cmdb_resources WHERE source_id="+q(self.source_id)+" AND kind='deployment' AND attributes->>'resource_type' IN ('Deployment','StatefulSet','DaemonSet')))",read=True))
            if value['source'] and value['source'].get('last_success_at'):break
            check(time.monotonic()<deadline,'formal15LIST complete snapshot not observed before bounded wait')
            time.sleep(.2)
        source=value['source']
        check(source['kind']=='kubernetes' and source['environment_key']=='test' and not source['last_error']
            and {x['external_id'] for x in value['workloads']}==set(self.expected),'native source scope/workload set differs')
        for row in value['workloads']:
            check(row['source_id']==self.source_id and row['source_kind']=='kubernetes' and row['is_mock'] is False
                and row['attributes']['inventory_presence']=='present' and row['attributes']['uid']==row['external_id']
                and row['attributes']['environment_key']=='test','actual snapshot provenance differs')
        self.marker_case('native_snapshot',dict(producer='formal main inventory15LIST→convert→SaveSnapshot',sql_business_seed=False,source_id=self.source_id))

    def run(self):
        self.prepare_auth();self.real_lists();self.provision();self.start();self.seed()
        for user in ('reader','peer'):
            self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(x)
                for x in (self.roles[user],'resources','view','environment','test'))+');')
        for user in ('reader','peer','hidden','denied'):self.login(user)
        self.native_snapshot();before=self.ledger()
        check(before==dict(applications=0,links=0,audit=[],candidates=0,investigations=0,proposals=0,actions=0,executions=0),
            'fresh source fixture created unrelated business results')
        path='/api/v1/services/discover?cluster='+CLUSTER
        validator=lambda value:validate_discovery(value,self.expected,self.source_id)
        first,_=self.call('reader','GET',path,validate=validator)
        self.marker_case('nonempty',dict(workloads=len(first['items']),real_source=True,no_verified_owner_guess=True))
        second,_=self.call('peer','GET',path,index=1,validate=validator)
        # Capture times may advance while the real watcher runs; all other fields stay exact.
        def stable(value):return [dict((k,v) for k,v in x.items() if k!='source') for x in value['items']]
        check(stable(first)==stable(second),'second API persisted workload projection differs')
        self.marker_case('second_api',dict(shared_pg=True,watcher_instances=1))
        self.call(None,'GET',path,want=401,code='unauthorized',scenario='authorization')
        self.call('denied','GET',path,validate=lambda v:validate_discovery(v,{},self.source_id,True),scenario='authorization')
        self.call('reader','GET',path,environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('hidden','GET',path,environment='staging',validate=lambda v:validate_discovery(v,{},self.source_id,True),scenario='authorization')
        self.marker_case('authorization',dict(anonymous401=True,no_services_empty200=True,cross_environment403=True,staging_hidden=True))
        for query in ('cluster='+CLUSTER+'&cluster='+CLUSTER,'cluster='+'x'*201):
            self.call('reader','GET','/api/v1/services/discover?'+query,want=422,code='invalid_cluster',scenario='boundary')
        self.call('reader','GET','/api/v1/services/discover?cluster=does-not-exist',validate=lambda v:validate_discovery(
            v,{},self.source_id,True,cluster='does-not-exist'),scenario='boundary')
        self.marker_case('parameters',dict(repeated_cluster422=True,oversized_cluster422=True,unknown_cluster_empty=True))
        self.sql('DELETE FROM v3_role_grants WHERE role_id='+q(self.roles['peer'])+" AND module='resources' AND action='view' AND scope_kind='environment' AND scope_id='test';")
        self.call('peer','GET',path,index=1,validate=lambda v:validate_discovery(v,{},self.source_id,True),scenario='authorization')
        self.marker_case('revocation',dict(current_resources_grant_checked=True,second_api_hidden=True))
        check(self.ledger()==before,'discover/read/rejection/revocation changed complete business/audit ledger')
        self.marker_case('ledger',dict(business_and_audit_unchanged=True,collector_cmdb_updates_expected=True))
        return dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,runtime_sha=RUNTIME,
            helper_sha256=self.data['helper_sha256'],required_cases=REQUIRED_CASES,http_calls=self.calls,
            real_kubernetes=True,workload_writes=0,sql_business_seed=False,full_business_chain_verified=False,
            counts_as_whole_acceptance=False,unverified=['inflight source failure/UID churn/name ambiguity','production Kubernetes',
                'watch loss/reconnect semantics','actual service registration/full service projection','Argo/CI/VM/24h/performance'])

    def cleanup_auth(self):
        deadline=time.monotonic()+4;errors=[]
        for record in reversed(self.auth):
            try:
                value=self.kube('GET',record['path'],admin=True,deadline=deadline)
                owned_auth(value,record['spec'],record['uid'])
                self.kube('DELETE',record['path'],dict(apiVersion='v1',kind='DeleteOptions',
                    preconditions=dict(uid=record['uid']),propagationPolicy='Background'),want=200,admin=True,deadline=deadline)
                self.kube('GET',record['path'],want=404,admin=True,deadline=deadline)
            except BaseException as error:errors.append(dict(path=record['path'],uid=record['uid'],error_type=type(error).__name__))
        ro._private(self.work/'auth-cleanup.json',dict(unknown_create_paths=self.unknown,errors=errors,
            shared_budget_seconds=4,namespace_deleted=False,existing_workloads_untouched=True))
        check(not self.unknown and not errors,'exact auth cleanup blocked; UUID/UID receipt retained')

    def cleanup(self):
        primary=extra=None
        try:super().cleanup()  # stop held watcher and drop owned PG before auth revocation
        except BaseException as error:primary=error
        finally:
            try:self.cleanup_auth()
            except BaseException as error:extra=error
        if primary is not None:raise primary
        if extra is not None:raise extra
        emit('discovery_real.cleanup','passed',dict(owned_pg_process_seconds=40,auth_budget_seconds=4,runner_grace=45,
            exact_recorded_uids=True,workload_mutations=0))

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--live',action='store_true')
    parser.add_argument('--run-id');parser.add_argument('--admission',type=Path)
    args=parser.parse_args(argv)
    if not(args.live and args.run_id and args.admission):parser.error('root-reviewed --live --run-id --admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required before lease')
    fd=os.open(ROOT/'.local/testlab/service-discovery-real.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:
        check(stat.S_ISREG(os.fstat(fd).st_mode) and stat.S_IMODE(os.fstat(fd).st_mode)==0o600,'private regular lease required')
        fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT,args.admission,args.run_id),args.run_id))
        return 0
    except Blocked:emit('discovery_real.environment','blocked',dict(error='identity/source/admission blocked; private receipt retained'));return 2
    except Exception as error:emit('discovery_real.failure','failed',dict(error_type=type(error).__name__));return 1
    finally:os.close(fd)

if __name__=='__main__':raise SystemExit(main())
