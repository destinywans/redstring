"""Owned Linux Go rule-assistant HTTP coverage with explicit protocol Mock scope.

Only a fresh UUID database, role, private Docker network and invocation CIDs
may be changed. Host Darwin admission, source and runtime config stay frozen.
TLS terminates at the held loopback fixture; its owned container forwards only
opaque bytes over443. DNS aliases and CA trust exist only in the own network.
"""
from __future__ import annotations
import argparse,base64,contextlib,copy,datetime,fcntl,hashlib,ipaddress,json,os,re,secrets,socket,stat,struct,subprocess,sys,threading,time,uuid
from pathlib import Path
from urllib.parse import urlencode
import api_readonly_chain as ro
import api_rule_generation_functional_coverage as generation
from inventory import actual_interface

ROOT=ro.ROOT;OWNER=ro.OWNER;MODULE='formal-api-rule-assistant-container'
PORTS=(18690,18691);BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS);FIXTURE_PORT=18692
RESERVED_PORTS=tuple(range(18690,18698));TARGETS=generation.TARGETS[:2]
SOURCE_SHA=generation.SOURCE_SHA;DARWIN_SHA=generation.BINARY_SHA;RUNTIME_SHA=generation.RUNTIME_SHA;PG_CID=generation.PG_CID
IMAGE_ID=generation.TLS_IMAGE;UID=os.getuid();GID=os.getgid()
NETWORK_OPTIONS={'com.docker.network.enable_ipv4':'true','com.docker.network.enable_ipv6':'false'}
API_MEMORY=512*1024**2;FIXTURE_MEMORY=128*1024**2;NEW_MEMORY=2*API_MEMORY+FIXTURE_MEMORY
SUPPORT=(('POST','/auth/login'),('POST','/services'))
REQUIRED_CASES=('container.linux_build','container.network','container.identity','container.tls','generation.rules_normal','generation.rules_authorization','generation.rules_boundaries','generation.rules_provider_faults','container.consistency')

def evidence(row):
    mode='protocol_mock' if row.get('path') in ('/api/v1/alert-rules/generate','/api/v1/alert-rules/preview') else 'real_local'
    return dict(row,evidence_mode=mode,upstream_mode=mode,test_name='FormalRuleAssistantContainer',proof_scope='actual Linux Go/UUID PG/private DNS and CA TLS; fixed model and synthetic2017 seven-day samples are protocol Mock, no real inference/history')

def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None);mode='real_local'
    if 'interface' in fields:fields['interface']=evidence(fields['interface']);mode=fields['interface']['evidence_mode']
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode=mode,**fields),ensure_ascii=False),flush=True)

@contextlib.contextmanager
def context():
    names=('PORTS','BASES','FIXTURE_PORT','emit','interface_evidence');old={n:getattr(generation,n) for n in names}
    generation.PORTS,generation.BASES,generation.FIXTURE_PORT=PORTS,BASES,FIXTURE_PORT
    generation.emit,generation.interface_evidence=emit,evidence
    try:
        with generation.context():yield
    finally:
        for n,v in old.items():setattr(generation,n,v)

def frozen_hashes():
    return {name:hashlib.sha256((ROOT/'tools/testlab'/name).read_bytes()).hexdigest() for name in ('api_rule_assistant_container_coverage.py','api_rule_generation_functional_coverage.py','api_readonly_chain.py')}

def reviewed_expression(app,env):
    if not re.fullmatch('[a-f0-9]{32}',app) or env not in ('test','staging'):raise ro.Blocked('exact scoped synthetic catalog identity')
    return 'sum by (application,environment,service) (testlab_generation_rate{application="'+app+'",environment="'+env+'",service="owned-metrics"})'

def free_ports():
    for port in RESERVED_PORTS:
        with socket.socket() as s:
            s.settimeout(.3)
            if s.connect_ex(('127.0.0.1',port))==0:raise ro.Blocked('exclusive rule-container port occupied: '+str(port))

def resource_admission(total,available,cpus,cpu_used,pending_memory=4*1024**3,pending_cpus=3):
    remaining=available-pending_memory;cpu_remaining=cpus-cpu_used-pending_cpus
    if not 0<=available<=total or remaining<NEW_MEMORY+1024**3 or cpu_remaining<2.5:raise ro.Blocked('fresh Docker headroom below fixed1.125GiB+1GiB reserve or2.5CPU after parallelGitLab reserve')
    return {'vm_total_bytes':total,'vm_available_bytes':available,'pending_gitlab_memory':pending_memory,'pending_gitlab_cpus':pending_cpus,'new_fixed_memory':NEW_MEMORY,'reserve_memory':1024**3,'memory_remaining_after_parallel_reserve':remaining,'cpu_remaining_after_parallel_reserve':cpu_remaining,'passed':True}

def gitlab_remaining_reserve(stats,info,cgroup=None):
    limit=4*1024**3
    if (info.get('Name')!='/aiops-'+OWNER+'-gitlab' or info.get('Id')!=stats.get('Container') or info.get('Config',{}).get('Labels',{}).get('aiops.testlab.owner')!=OWNER or
        info.get('State',{}).get('Running') is not True or info.get('HostConfig',{}).get('Memory')!=limit or not 0<info.get('HostConfig',{}).get('NanoCpus',0)<=3000000000):return limit,{'unattested_or_absent':True}
    cgroup=cgroup or {};anon=cgroup.get('anon')
    if type(anon) is not int or not 0<=anon<=limit or cgroup.get('memory_max')!=limit or cgroup.get('cpu_quota')!=info['HostConfig']['NanoCpus']/1e9:return limit,{'anon_or_cgroup_unavailable':True}
    # Anonymous resident pages are already deducted from VM MemAvailable.
    # Reclaimable file cache and total cgroup usage are never deducted here.
    return limit-anon,{'cid':info['Id'],'name':info['Name'],'image_id':info.get('Image'),'fixed_limit_bytes':limit,'anonymous_resident_bytes':anon,'remaining_limit_reserved_bytes':limit-anon,'cgroup':cgroup,'operation':'read-only accounting; no container adoption or mutation'}

def resources(work,stage):
    pg=ro.inspect_pg(PG_CID);ro.guard_pg(pg,OWNER)
    info=json.loads(subprocess.check_output(['docker','info','--format','{{json .}}'],text=True,timeout=8))
    text=subprocess.check_output(['docker','exec',PG_CID,'cat','/proc/meminfo'],text=True,timeout=5)
    mem={k:int(v)*1024 for k,v in re.findall(r'^(MemTotal|MemAvailable):\s+(\d+) kB$',text,re.M)}
    stats=[json.loads(x) for x in subprocess.check_output(['docker','stats','--no-stream','--format','{{json .}}'],text=True,timeout=12).splitlines()]
    cpu=sum(float(row['CPUPerc'].rstrip('%'))/100 for row in stats)
    pending=4*1024**3;accounting={'not_observed':True}
    for row in stats:
        if row.get('Name')!='aiops-'+OWNER+'-gitlab':continue
        result=subprocess.run(['docker','inspect',row['Container']],capture_output=True,text=True,timeout=4)
        if result.returncode==0:
            gitlab=json.loads(result.stdout)[0];cgroup=None
            if gitlab.get('Id')==row.get('Container') and gitlab.get('Config',{}).get('Labels',{}).get('aiops.testlab.owner')==OWNER and gitlab.get('State',{}).get('Running'):
                r=subprocess.run(['docker','exec',gitlab['Id'],'cat','/sys/fs/cgroup/memory.stat','/sys/fs/cgroup/memory.max','/sys/fs/cgroup/cpu.max'],capture_output=True,text=True,timeout=4)
                if r.returncode==0:
                    lines=r.stdout.splitlines();stat={k:int(v) for k,v in [line.split() for line in lines[:-2]]};quota=lines[-1].split();cgroup={'anon':stat.get('anon'),'memory_max':int(lines[-2]),'cpu_quota':int(quota[0])/int(quota[1]),'memory_stat':stat,'raw':r.stdout}
            pending,accounting=gitlab_remaining_reserve(row,gitlab,cgroup)
    observed={'stage':stage,'vm_total_bytes':mem['MemTotal'],'vm_available_bytes':mem['MemAvailable'],'docker_cpus':info['NCPU'],'observed_cpu_used':cpu,'timestamp':datetime.datetime.now(datetime.timezone.utc).isoformat(),'docker_architecture':info['Architecture'],'pending_gitlab_memory':pending,'pending_gitlab_cpus':3,'parallel_gitlab_accounting':accounting,'new_fixed_memory':NEW_MEMORY,'reserve_memory':1024**3,'raw_stats':stats}
    path=work/('resources-'+stage+'.json');ro._private(path,observed)
    try:result=resource_admission(mem['MemTotal'],mem['MemAvailable'],info['NCPU'],cpu,pending_memory=pending)
    except ro.Blocked as error:
        ro._private(path,dict(observed,passed=False,error=str(error)));raise
    ro._private(path,dict(observed,**result));return result

def linux_build(proof_path):
    p=Path(proof_path).absolute();base=ROOT/'.local/testlab/rule-assistant-container/build'
    if p!=p.resolve() or not p.is_relative_to(base) or p.name!='build-proof.json':raise ro.Blocked('private recorded cross-build required; no symlink traversal')
    d=ro.private_json(p);binary=Path(d['binary'])
    if binary!=binary.resolve() or binary.parent!=p.parent or not binary.is_file() or hashlib.sha256(binary.read_bytes()).hexdigest()!=d.get('binary_sha256'):raise ro.Blocked('Linux binary regular-file/SHA boundary')
    if any(d.get(k)!=v for k,v in {'source_tree_sha_start':SOURCE_SHA,'source_tree_sha_end':SOURCE_SHA,'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0','GOTOOLCHAIN':'local','flags':['-trimpath'],'package':'./cmd/aiops','exit_code':0}.items()):raise ro.Blocked('exact frozen nonrace Linux build metadata required')
    header=binary.read_bytes()[:64]
    if len(header)!=64 or header[:6]!=b'\x7fELF\x02\x01' or struct.unpack('<H',header[18:20])[0]!=183:raise ro.Blocked('actual little-endian arm64ELF required')
    if 'GOOS=linux' not in d['build_info'] or 'GOARCH=arm64' not in d['build_info'] or 'CGO_ENABLED=0' not in d['build_info'] or '-race=true' in d['build_info']:raise ro.Blocked('actual Go binary platform/nonrace build info')
    return d

def prepared_linux_build(root,override=None):
    """Use one explicit prepared proof; never build or search for a latest one."""
    try:
        proof=override
        if proof is None:
            index=Path(root).resolve()/'.local/testlab/rule-assistant-container/build/current.json'
            if index!=index.resolve():raise ro.Blocked('prepared build index traverses a symlink')
            pointer=ro.private_json(index)
            if not isinstance(pointer,dict) or set(pointer)!={'proof'} or not isinstance(pointer['proof'],str) or not Path(pointer['proof']).is_absolute():raise ro.Blocked('prepared build index requires exactly one absolute proof path')
            proof=pointer['proof']
        return linux_build(proof)
    except (OSError,ValueError,KeyError,TypeError) as error:
        raise ro.Blocked('prepared Linux build index or proof is invalid: '+type(error).__name__) from error

class Contract(generation.Contract):
    def canonical(self,method,path):
        canonical,kind=super().canonical(method,path)
        if (method,canonical[len('/api/v1'):]) not in (*TARGETS,*SUPPORT):raise ro.Blocked('outside rule-container exact public contract')
        return canonical,kind

def guard_network(info,c):
    if info.get('Id')!=c.network_id or not re.fullmatch('[a-f0-9]{64}',str(c.network_id)) or info.get('Name')!=c.network_name or info.get('Driver')!='bridge' or info.get('Scope')!='local' or info.get('Internal') is not False or info.get('Ingress') is not False or info.get('Attachable') is not False or info.get('Options')!=NETWORK_OPTIONS:raise ro.Blocked('fresh exact private Docker bridge identity drift')
    if info.get('Labels')!={'aiops.testlab.owner':OWNER,'aiops.testlab.run':c.run_id,'aiops.testlab.purpose':'rule-assistant-container'}:raise ro.Blocked('network ownership/run drift')
    if not set(info.get('Containers',{})).issubset({s['cid'] for s in c.containers if s.get('cid')}):raise ro.Blocked('foreign private-network member; no network mutation')
    return info['Id']

def guard_container(info,s,c,live=True):
    h=info.get('HostConfig',{});config=info.get('Config',{});nets=info.get('NetworkSettings',{}).get('Networks',{})
    # Before first start Docker may not yet assign an endpoint ID. The exact
    # fresh bridge is separately inspected before start; every live CID must
    # have its full recorded NetworkID, never an empty or different one.
    endpoint=nets.get(c.network_name,{})
    network_matches=endpoint.get('NetworkID')==c.network_id or (not live and info.get('State',{}).get('Status')=='created' and info['State'].get('Pid')==0 and endpoint.get('NetworkID')=='')
    if (info.get('Id')!=s.get('cid') or not re.fullmatch('[a-f0-9]{64}',str(s.get('cid'))) or info.get('Name')!='/'+s['name'] or info.get('Image')!=IMAGE_ID or
        config.get('Labels')!={'aiops.testlab.owner':OWNER,'aiops.testlab.run':c.run_id,'aiops.testlab.purpose':s['kind']} or config.get('User')!=str(UID)+':'+str(GID) or UID==0 or
        h.get('Memory')!=s['memory'] or h.get('NanoCpus')!=s['cpu'] or h.get('Privileged') is not False or h.get('ReadonlyRootfs') is not True or h.get('CapDrop')!=['ALL'] or h.get('CapAdd') not in (None,[]) or h.get('SecurityOpt')!=['no-new-privileges'] or
        h.get('NetworkMode')!=c.network_name or h.get('PidMode') not in ('',None) or h.get('IpcMode')!='private' or h.get('Devices') not in (None,[]) or h.get('DeviceRequests') not in (None,[]) or h.get('VolumesFrom') not in (None,[]) or h.get('Binds') not in (None,[]) or h.get('Tmpfs') not in (None,{}) or h.get('RestartPolicy',{}).get('Name')!='no' or h.get('AutoRemove') is not False or
        (h.get('Sysctls') or {})!=s['sysctls'] or (h.get('PortBindings') or {})!=s['bindings'] or info.get('Path')!=s['path'] or info.get('Args')!=s['args'] or
        set(nets)!={c.network_name} or not network_matches or (live and info.get('State',{}).get('Running') is not True)):
        raise ro.Blocked('fresh CID/name/owner/image/resource/network/argv/port safety boundary')
    aliases=set(nets[c.network_name].get('Aliases') or [])
    if not set(s['aliases']).issubset(aliases) or not aliases.issubset(set(s['aliases'])|{s['name'],s['cid'][:12]}):raise ro.Blocked('container DNS aliases drift')
    mounts=info.get('Mounts',[])
    actual={(m.get('Source'),m.get('Destination'),m.get('Type'),m.get('RW')) for m in mounts}
    wanted={(str(source),target,'bind',False) for source,target in s['mounts']}
    if len(mounts)!=len(wanted) or actual!=wanted:raise ro.Blocked('extra/RW/volume/private-secret/Docker-socket mount rejected')
    if set(config.get('Env') or [])!=set(s['env']):raise ro.Blocked('container exact environment drift')
    if config.get('WorkingDir')!='/owned':raise ro.Blocked('container working directory drift')
    if info.get('State',{}).get('Running'):
        ports=info.get('NetworkSettings',{}).get('Ports',{})
        if {k:v for k,v in ports.items() if v is not None}!=s['bindings']:raise ro.Blocked('actual published listener drift')
    return s['cid']

def guard_process(row,s,c):
    if (row.get('uid')!=UID or row.get('gid')!=GID or row.get('proc1_uid')!=UID or row.get('argv')!=[s['path'],*s['args']] or
        not isinstance(row.get('start_ticks'),int) or row['start_ticks']<=0 or row.get('memory_max')!=s['memory'] or abs(row.get('cpu_quota',0)-s['cpu']/1e9)>1e-8 or
        (s['kind']=='rule-assistant-api' and row.get('exe_sha256')!=c.build['binary_sha256'])):raise ro.Blocked('actual container UID/PID1 argv/ELF/cgroup identity drift')
    if s.get('process_identity') and any(row.get(k)!=s['process_identity'][k] for k in ('uid','gid','proc1_uid','argv','start_ticks','exe_sha256')):raise ro.Blocked('held CID process identity changed')
    return row

class Chain(generation.Chain):
    def __init__(self,root,data,run_id,build):
        ro.check(str(uuid.UUID(run_id))==run_id and data.get('owner')==OWNER,'canonical owned UUID required')
        self.root,self.data,self.run_id=Path(root).resolve(),data,run_id;self.nonce=uuid.UUID(run_id).hex
        self.database,self.db_user='aiops_testlab_rulecontainer_'+self.nonce,'rulecontainer_'+self.nonce
        self.owner_tag,self.marker=OWNER+':'+run_id+':rule-container','rulecontainerfixture'+self.nonce
        self.work=self.root/'.local/testlab/rule-assistant-container'/run_id
        if self.work.resolve()!=self.work:raise ro.Blocked('private path symlink')
        self.work.mkdir(mode=0o700,parents=True,exist_ok=False);self.password,self.db_password=secrets.token_urlsafe(32),secrets.token_urlsafe(32);self.admin_email='bootstrap-'+self.nonce+'@testlab.invalid'
        self.users={n:uuid.uuid5(uuid.UUID(run_id),'20260930:'+n).hex for n in ('reader','peer','hidden','denied')};self.roles={n:'rulecontainer-'+self.nonce+'-'+n for n in self.users}
        self.cookies,self.ids,self.processes={},{},[];self.calls=0;self.role_attempted=self.db_attempted=False;self.contract=Contract(root);self.apps={};self.config_hashes={};self.rule_fixture=None;self.configured=False
        self.build=build;self.network_name='aiops-'+OWNER+'-rule-network-'+self.nonce;self.network_id=None;self.network_attempted=False;self.containers=[];self.api=[];self.generation=0;self.cleanup_deadline=None;self.attested_pg_sessions=set()
        self.hosts=('query-'+self.nonce+'.testlab.invalid','model-'+self.nonce+'.testlab.invalid');self.own_hashes=frozen_hashes()
        image=json.loads(subprocess.check_output(['docker','image','inspect',IMAGE_ID],text=True,timeout=8))[0]
        if image.get('Id')!=IMAGE_ID or image.get('Os')!='linux' or image.get('Architecture')!='arm64':raise ro.Blocked('pinned Linux image platform mismatch')
        self.image_env=image['Config']['Env']
        self.state={'owner':OWNER,'run_id':run_id,'database':self.database,'database_user':self.db_user,'owner_tag':self.owner_tag,'binary_sha256':DARWIN_SHA,'linux_binary_sha256':build['binary_sha256'],'source_tree_sha':SOURCE_SHA,'ports':list(RESERVED_PORTS),'pids':[],'seed':20260930,'evidence_mode':'protocol_mock','containers':[]}
        ro._private(self.work/'state.json',self.state);ro._private(self.work/'credentials.json',{'password':self.password,'database_password':self.db_password})
    def marker_case(self,name,value,status='passed'):self.frozen();emit(name,status,dict(run_id=self.run_id,source_tree_sha=SOURCE_SHA,**value))
    def frozen(self):
        ro.Chain.frozen(self)
        if frozen_hashes()!=self.own_hashes or hashlib.sha256((self.root/'.local/testlab/runtime-config.json').read_bytes()).hexdigest()!=RUNTIME_SHA or hashlib.sha256(Path(self.build['binary']).read_bytes()).hexdigest()!=self.build['binary_sha256']:raise ro.Blocked('frozen helper/Linux binary/main runtime drift')
        for name,digest in self.config_hashes.items():
            path=self.work/name
            if path.is_symlink() or stat.S_IMODE(path.stat().st_mode)!=0o600 or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:raise ro.Blocked('owned mounted config bytes drift')
        for s in self.containers:
            if s.get('active'):guard_container(self.inspect_container(s['cid']),s,self)
        if self.network_id:guard_network(self.network_info(),self)
    def pg(self,*args,**kwargs):
        if self.cleanup_deadline:
            if self.cleanup_deadline<=time.monotonic():raise ro.Blocked('single cleanup deadline exhausted before PG inspection')
            kwargs['deadline']=min(kwargs.get('deadline') or self.cleanup_deadline,self.cleanup_deadline)
        timeout=min(3,max(.1,self.cleanup_deadline-time.monotonic())) if self.cleanup_deadline else 3
        if ro.inspect_pg(self.data['postgres_container'],timeout=timeout)['Id']!=PG_CID:raise ro.Blocked('shared exact PG CID drift')
        return ro.Chain.pg(self,*args,**kwargs)
    def pg_application(self,kind,index=None):
        if kind=='api' and index in (0,1):return 'rulecontainer-api-'+self.nonce+'-'+str(index)
        if kind in ('sql','admin') and index is None:return 'rulecontainer-'+kind+'-'+self.nonce
        raise ro.Blocked('unowned PG application identity')
    @staticmethod
    def pg_tuple(row):return tuple(row.get(k) for k in ('pid','backend_start','datname','usename','application_name','client_addr','client_port','backend_type'))
    def connection_origin(self,row):
        if row.get('application_name') in (self.pg_application('api',0),self.pg_application('api',1)):
            if self.pg_tuple(row) not in self.attested_pg_sessions:raise ro.Blocked('container API PG backend not in exact fresh attested tuple set')
            return 'owned_api'
        return ro.Chain.connection_origin(self,row)
    def capture_pg(self):
        for s in self.api:
            if s.get('active'):self.process_attestation(s)
        rows=json.loads(self.sql("SELECT COALESCE(json_agg(json_build_object('pid',pid,'backend_start',backend_start::text,'datname',datname,'usename',usename,'application_name',application_name,'client_addr',host(client_addr),'client_port',client_port,'backend_type',backend_type)),'[]'::json) FROM pg_stat_activity WHERE datname="+ro.quote(self.database),read=True))
        for row in rows:
            if row['application_name'] not in (self.pg_application('api',0),self.pg_application('api',1)):continue
            if row['datname']!=self.database or row['usename']!=self.db_user or row['backend_type']!='client backend' or type(row['pid']) is not int or row['pid']<=1 or not row['backend_start'] or type(row['client_port']) is not int or not 0<row['client_port']<=65535:raise ro.Blocked('exact owned TCP PG identity mismatch')
            ipaddress.ip_address(row['client_addr']);self.attested_pg_sessions.add(self.pg_tuple(row))
        ro._private(self.work/('pg-origin-'+str(self.generation)+'.json'),{'rows':rows,'scope':'fresh guarded containers and nonce application identities; NAT addresses are observations, not native CID attribution'})
    def provision(self):
        self.frozen();q=ro.quote;i=ro.identifier
        absent=json.loads(self.pg("SELECT json_build_object('role',EXISTS(SELECT 1 FROM pg_roles WHERE rolname="+q(self.db_user)+"),'database',EXISTS(SELECT 1 FROM pg_database WHERE datname="+q(self.database)+'));'))
        if absent!={'role':False,'database':False}:raise ro.Blocked('fresh UUID PG resource already exists')
        self.role_attempted=True;self.pg('BEGIN; CREATE ROLE '+i(self.db_user)+' LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '+q(self.db_password)+'; COMMENT ON ROLE '+i(self.db_user)+' IS '+q(self.owner_tag)+'; COMMIT;')
        self.db_attempted=True;self.pg('CREATE DATABASE '+i(self.database)+' OWNER '+i(self.db_user)+' TEMPLATE template0;');self.pg('COMMENT ON DATABASE '+i(self.database)+' IS '+q(self.owner_tag)+'; REVOKE CONNECT,TEMPORARY ON DATABASE '+i(self.database)+' FROM PUBLIC;');self.pg('CREATE EXTENSION vector WITH SCHEMA public; CREATE EXTENSION pgcrypto WITH SCHEMA public;',self.database)
        ro._private(self.work/'instance.json',{'tier':'nonprod','name':'Rule Linux '+self.nonce,'domain':'testlab.local','default_env':'test','environments':[{'id':env,'label':env,'kind':'standing','tone':'dev','clusters':[]} for env in ('test','staging')]})
        ro._private(self.work/'models.json',{'tier':'nonprod','environments':[{'id':env,'models':['openai:readonly-synthetic'],'default_model':'openai:readonly-synthetic'} for env in ('test','staging')]});ro._private(self.work/'commands.json',[])
        for name in ('instance.json','models.json','commands.json'):self.config_hashes[name]=hashlib.sha256((self.work/name).read_bytes()).hexdigest()
        self.marker_case('container.fresh_database',{'database':self.database,'role':self.db_user,'business_success_seeded':False})
    def docker(self,argv,timeout=12):
        if self.cleanup_deadline:
            timeout=min(timeout,self.cleanup_deadline-time.monotonic())
            if timeout<=0:raise ro.Blocked('single container cleanup deadline exhausted')
        return subprocess.run(['docker',*argv],capture_output=True,text=True,timeout=timeout)
    def network_info(self):
        r=self.docker(['network','inspect',self.network_id],timeout=3)
        if r.returncode:raise ro.Blocked('own network inspect unavailable')
        return json.loads(r.stdout)[0]
    def inspect_container(self,cid):
        r=self.docker(['inspect',cid],timeout=3)
        if r.returncode:raise ro.Blocked('fresh own CID inspect unavailable')
        rows=json.loads(r.stdout)
        if len(rows)!=1 or rows[0].get('Id')!=cid:raise ro.Blocked('fresh exact inspect CID mismatch')
        return rows[0]
    def create_network(self):
        if self.docker(['network','inspect',self.network_name]).returncode==0:raise ro.Blocked('UUID network already exists; no adoption')
        self.network_attempted=True;r=self.docker(['network','create','--driver','bridge','--opt','com.docker.network.enable_ipv4=true','--opt','com.docker.network.enable_ipv6=false','--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-assistant-container',self.network_name])
        ro.check(r.returncode==0 and re.fullmatch('[a-f0-9]{64}',r.stdout.strip()),'fresh network create outcome not confirmed');self.network_id=r.stdout.strip();guard_network(self.network_info(),self)
        self.marker_case('container.network',{'id':self.network_id,'private_bridge':True,'host_DNS_or_CA_changed':False,'external_egress_isolation_claim':False})
    def create_container(self,kind,index=None):
        resources(self.work,kind+'-'+str(self.generation)+'-'+str(index));name='aiops-'+OWNER+'-rule-'+self.nonce+'-'+kind+'-'+str(self.generation)+'-'+str(index)
        if self.docker(['inspect',name]).returncode==0:raise ro.Blocked('UUID container already exists; no adoption')
        api=kind=='api';mounts=[(Path(self.build['binary']),'/owned/aiops-linux')] if api else [(self.work/'tls-bridge.py','/owned/tls-bridge.py')]
        values={}
        if api:
            names=['instance.json','models.json','commands.json']+(['tls-cert.pem','preview-config.json','draft-config.json'] if self.configured else [])
            mounts += [(self.work/n,'/owned/'+n) for n in names]
            values={'DATABASE_URL':'postgres://'+self.db_user+':'+self.db_password+'@host.docker.internal:15461/'+self.database+'?'+urlencode({'sslmode':'disable','options':'-csearch_path=public','application_name':self.pg_application('api',index)}),'AIOPS_V3_INSTANCE_CONFIG':'/owned/instance.json','AIOPS_ADDR':'0.0.0.0:8080','AIOPS_PUBLIC_ORIGIN':BASES[index],'AIOPS_SECURE_COOKIES':'false','AIOPS_ADMIN_EMAIL':self.admin_email,'AIOPS_ADMIN_PASSWORD':self.password,'AIOPS_TRUSTED_PROXY_CIDRS':'127.0.0.1/32','AIOPS_V3_MODEL_BINDINGS_FILE':'/owned/models.json','AIOPS_V3_TEXT_COMMANDS_CONFIG':'/owned/commands.json','OPENAI_MODEL':'readonly-synthetic','OPENAI_API_KEY':'synthetic-no-inference','OPENAI_BASE_URL':'http://127.0.0.1:9/v1','EMBEDDING_PROVIDER':'openai','EMBEDDING_MODEL':'readonly-synthetic','EMBEDDING_API_KEY':'synthetic-no-embedding','EMBEDDING_BASE_URL':'http://127.0.0.1:9/v1'}
            if self.configured:values.update(AIOPS_V3_RULE_PREVIEW_CONFIG='/owned/preview-config.json',AIOPS_V3_RULE_PREVIEW_ALLOWED_HOSTS=self.hosts[0],AIOPS_V3_RULE_DRAFT_MODEL_CONFIG='/owned/draft-config.json',AIOPS_V3_RULE_DRAFT_MODEL_ALLOWED_HOSTS=self.hosts[1],SSL_CERT_FILE='/owned/tls-cert.pem',**self.rule_fixture.env_tokens())
        env={v.split('=',1)[0]:v.split('=',1)[1] for v in self.image_env};env.update(values)
        s={'name':name,'kind':'rule-assistant-api' if api else 'rule-assistant-fixture','memory':API_MEMORY if api else FIXTURE_MEMORY,'cpu':1000000000 if api else 500000000,'mounts':mounts,'env':[k+'='+v for k,v in env.items()],'aliases':[] if api else list(self.hosts),'bindings':{'8080/tcp':[{'HostIp':'127.0.0.1','HostPort':str(PORTS[index])}]} if api else {},'sysctls':{} if api else {'net.ipv4.ip_unprivileged_port_start':'0'},'path':'/owned/aiops-linux' if api else 'python','args':[] if api else ['-B','/owned/tls-bridge.py'],'cid':None,'active':False,'index':index}
        self.containers.append(s);envfile=self.work/(name+'.env');ro.private_file(envfile,'\n'.join(k+'='+v for k,v in values.items())+'\n');cidfile=self.work/(name+'.cid')
        cmd=['create','--pull','never','--name',name,'--cidfile',str(cidfile),'--network',self.network_name,'--label','aiops.testlab.owner='+OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose='+s['kind'],'--cpus',str(s['cpu']/1e9),'--memory',str(s['memory']),'--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user',str(UID)+':'+str(GID),'--workdir','/owned','--env-file',str(envfile)]
        for source,target in mounts:cmd+=['--mount','type=bind,source='+str(source)+',target='+target+',readonly']
        for alias in s['aliases']:cmd+=['--network-alias',alias]
        for key,value in s['sysctls'].items():cmd+=['--sysctl',key+'='+value]
        if api:cmd+=['-p','127.0.0.1:'+str(PORTS[index])+':8080','--entrypoint','',IMAGE_ID,s['path']]
        else:cmd+=['--entrypoint','python',IMAGE_ID,*s['args']]
        with ro.acquisition_signals():
            try:r=self.docker(cmd)
            finally:
                if cidfile.exists() and not cidfile.is_symlink():
                    cid=cidfile.read_text().strip()
                    if re.fullmatch('[a-f0-9]{64}',cid):s['cid']=cid;cidfile.chmod(0o600)
        if not s['cid']:raise ro.Blocked('create has no trusted fresh cidfile; no name adoption')
        guard_container(self.inspect_container(s['cid']),s,self,live=False);guard_network(self.network_info(),self);ro.check(r.returncode==0 and r.stdout.strip()==s['cid'],'fresh create outcome not confirmed')
        ro.check(self.docker(['start',s['cid']]).returncode==0,'exact fresh container start failed');s['active']=True
        deadline=time.monotonic()+70
        while time.monotonic()<deadline:
            guard_container(self.inspect_container(s['cid']),s,self)
            if not api:break
            try:
                status,raw,_=ro.transport(BASES[index],'GET','/healthz',timeout=2)
                if status==200:break
            except OSError:pass
            time.sleep(.2)
        else:raise ro.Blocked('owned Linux API readiness deadline')
        self.process_attestation(s);self.state['containers'].append({'cid':s['cid'],'name':name,'kind':s['kind']});ro._private(self.work/'state.json',self.state)
        return s
    def process_attestation(self,s):
        guard_container(self.inspect_container(s['cid']),s,self)
        program="import os,json,hashlib; a=open('/proc/1/cmdline','rb').read().rstrip(bytes([0])).split(bytes([0])); fields=open('/proc/1/stat').read().rsplit(')',1)[1].split(); cpu=open('/sys/fs/cgroup/cpu.max').read().split(); print(json.dumps(dict(uid=os.getuid(),gid=os.getgid(),proc1_uid=os.stat('/proc/1').st_uid,argv=[x.decode() for x in a],start_ticks=int(fields[19]),exe_sha256=hashlib.sha256(open('/proc/1/exe','rb').read()).hexdigest(),memory_max=int(open('/sys/fs/cgroup/memory.max').read()),cpu_quota=int(cpu[0])/int(cpu[1]))))"
        r=self.docker(['exec',s['cid'],'python','-c',program],timeout=4);ro.check(r.returncode==0,'actual container PID1/UID/cgroup proof unavailable');row=guard_process(json.loads(r.stdout),s,self);s['process_identity']=row
        ro._private(self.work/(s['name']+'-identity.json'),{'cid':s['cid'],'name':s['name'],'image_id':IMAGE_ID,'network_id':self.network_id,'process':row,'mounts':[(str(p),t) for p,t in s['mounts']],'memory_limit':s['memory'],'cpu_limit':s['cpu'],'ports':s['bindings']});return row
    def start(self):
        self.generation+=1;self.api=[]
        for index in (0,1):self.api.append(self.create_container('api',index))
    def stop_container(self,s):
        if s.get('removed'):return
        if not s.get('cid'):
            if self.docker(['inspect',s['name']],timeout=3).returncode==0:raise ro.Blocked('unconfirmed creation exists withoutfresh CID; retained')
            s['removed']=True;return
        info=self.inspect_container(s['cid']);guard_container(info,s,self,live=bool(info['State']['Running']))
        if info['State']['Running']:
            self.process_attestation(s);ro.check(self.docker(['stop','--time','3',s['cid']],timeout=8).returncode==0,'fresh own CID stop failed')
        info=self.inspect_container(s['cid']);guard_container(info,s,self,live=False)
        ro.check(not info['State']['Running'],'own CID still running; PG retained');s['active']=False;s['exit_code']=info['State']['ExitCode']
        logs=self.docker(['logs',s['cid']],timeout=3);ro._private(self.work/(s['name']+'-logs.json'),{'stdout':logs.stdout,'stderr':logs.stderr,'exit_code':s['exit_code']})
        ro.check(self.docker(['rm',s['cid']],timeout=4).returncode==0,'fresh own CID rm failed');ro.check(self.docker(['inspect',s['cid']],timeout=3).returncode!=0,'removed own CID still exists');s['removed']=True
    def restart(self):
        self.capture_pg()
        for s in reversed(self.api):self.stop_container(s)
        self.start()
    def prepare_rules(self):
        cert,key=self.work/'tls-cert.pem',self.work/'tls-key.pem'
        sans=','.join('DNS:'+h for h in self.hosts)
        r=subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','2','-subj','/CN='+self.hosts[0],'-addext','subjectAltName='+sans,'-addext','basicConstraints=critical,CA:TRUE','-keyout',str(key),'-out',str(cert)],capture_output=True,timeout=20,umask=0o077);ro.check(r.returncode==0,'own SAN TLS certificate generation failed')
        for p in (cert,key):p.chmod(0o600)
        self.rule_fixture=generation.RuleFixture(self);preview={'sources':[]};models={'environments':{}}
        for env,row in self.apps.items():
            row['rule_query']=reviewed_expression(row['app'],env)
            preview['sources'].append({'environment_id':env,'application_id':row['app'],'service':'owned-metrics','query_id':'owned_rate','query':row['rule_query'],'unit':'requests/s','source':'owned-history-'+self.nonce+'-'+env,'endpoint':'https://'+self.hosts[0]+'/preview/'+env,'api_key_env':'TESTLAB_RULE_PREVIEW_'+env.upper(),'isolation_mode':'application_endpoint','label_keys':{'application':'application','environment':'environment','service':'service'}})
            models['environments'][env]={'provider':'openai-compatible','model':'owned-rule-model','endpoint':'https://'+self.hosts[1]+'/model/'+env+'/chat/completions','api_key_env':'TESTLAB_RULE_MODEL_'+env.upper(),'max_input_bytes':8192,'max_output_tokens':128,'max_cost_micro_usd':10000,'input_price_micro_usd_per_million':1000000,'output_price_micro_usd_per_million':1000000}
        ro._private(self.work/'preview-config.json',preview);ro._private(self.work/'draft-config.json',models)
        bridge="""import socket,socketserver,select,signal
def stop(signum,frame):raise SystemExit(0)
signal.signal(signal.SIGTERM,stop)
class Handler(socketserver.BaseRequestHandler):
 def handle(self):
  try:
   with socket.create_connection(('host.docker.internal',18692),timeout=5) as upstream:
    while True:
     ready,_,_=select.select([self.request,upstream],[],[],35)
     if not ready:return
     for source in ready:
      data=source.recv(65536)
      if not data:return
      (upstream if source is self.request else self.request).sendall(data)
  except OSError:pass
class Server(socketserver.ThreadingTCPServer):
 allow_reuse_address=False
 daemon_threads=True
with Server(('0.0.0.0',443),Handler) as server:server.serve_forever()
""";ro.private_file(self.work/'tls-bridge.py',bridge)
        for n in ('tls-cert.pem','tls-key.pem','preview-config.json','draft-config.json','tls-bridge.py'):self.config_hashes[n]=hashlib.sha256((self.work/n).read_bytes()).hexdigest()
        self.create_container('fixture');self.configured=True;self.restart()
        s=self.api[0];cert_bytes=__import__('ssl').PEM_cert_to_DER_cert(cert.read_text());program="import ssl,socket,json,hashlib; hosts="+repr(self.hosts)+"; ctx=ssl.create_default_context(cafile='/owned/tls-cert.pem'); result=[]\nfor host in hosts:\n with socket.create_connection((host,443),timeout=4) as raw:\n  with ctx.wrap_socket(raw,server_hostname=host) as tls: result.append(dict(host=host,dns=sorted(set(x[4][0] for x in socket.getaddrinfo(host,443,type=socket.SOCK_STREAM))),cert_sha256=hashlib.sha256(tls.getpeercert(binary_form=True)).hexdigest(),version=tls.version()))\nprint(json.dumps(result))"
        r=self.docker(['exec',s['cid'],'python','-c',program],timeout=10);ro.check(r.returncode==0,'actual container DNS and strict CA/SAN TLS unsuccessful');proof=json.loads(r.stdout)
        fixture=next(x for x in self.containers if x['kind']=='rule-assistant-fixture');ip=self.inspect_container(fixture['cid'])['NetworkSettings']['Networks'][self.network_name]['IPAddress']
        ro.check(len(proof)==2 and all(x['dns']==[ip] and x['cert_sha256']==hashlib.sha256(cert_bytes).hexdigest() and x['version'] in ('TLSv1.2','TLSv1.3') for x in proof),'exact fixture DNS/IP/server certificate differs')
        ro._private(self.work/'tls-proof.json',{'api_cid':s['cid'],'fixture_cid':fixture['cid'],'network_id':self.network_id,'strict_CA_SAN_checks':proof,'host443_published':False,'host_DNS_CA_modified':False,'tls_termination':'held ownHTTPS18692; fresh fixture container443 opaque forwarder only'})
        self.marker_case('container.tls',{'actual_two_DNS_aliases_CA_SAN':True,'ownTLS443_inside_private_network':True,'host443_unchanged':True,'fixture_cid':fixture['cid']})
        self.marker_case('container.identity',{'linux_binary_sha':self.build['binary_sha256'],'api_cids':[s['cid'] for s in self.api],'all_actual_UID_argv_cgroup_proven':True,'model_and_history_mode':'protocol_mock'})
    def run(self):
        self.marker_case('container.linux_build',{'binary_sha':self.build['binary_sha256'],'source_start_end':SOURCE_SHA,'toolchain':self.build['go_version'],'GOOS':'linux','GOARCH':'arm64','CGO_ENABLED':'0','nonrace':True})
        self.provision();self.create_network();self.start();self.seed()
        for name in ('admin','reader','peer','hidden','denied'):self.login(name)
        self.prepare_apps();result=self.rules_run()
        rows=self.rule_ledger();receipts=self.rule_fixture.snapshot()
        ro.check(all(r['application_id'] in {a['app'] for a in self.apps.values()} for r in rows['v3_rule_generate_attempts']),'draft attempt escaped UUID apps')
        for table in ('v3_alert_rule_drafts','v3_alert_rule_draft_operations','v3_alert_rule_enable_executions','v3_metrics_questions','v3_conversations'):
            ro.check(self.sql('SELECT count(*) FROM '+table,read=True)=='0','unexpected rule enable/question/conversation side effect')
        self.marker_case('container.consistency',{'normal_primary_routes':[list(t) for t in TARGETS],'fixed_model':True,'synthetic_2017_points':True,'real_inference_history':False,'rule_create_enable_MR_notifications':False,'provider_receipts':len(receipts),'sql_business_success_seed':False})
        result.update(container_platform='linux/arm64',binary_sha256=self.build['binary_sha256'],normal_real_increment=0,normal_protocol_mock_increment=2,required_cases=list(REQUIRED_CASES));ro._private(self.work/'result.json',result);return result
    def rules_run(self):
        # Exercise the real ten-second budget immediately after its acceptance;
        # container identity checks must not accidentally let the budget expire.
        self.prepare_rules();first,_=self.rule_call('model')
        self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body('model'),headers={'Idempotency-Key':'rules-'+self.nonce+'-throttled'},want=429,code='rule_generate_rate_limited',scenario='boundary')
        self.rule_call('preview');self.call('reader','POST','/api/v1/alert-rules/preview',self.rule_body('preview'),want=429,code='rule_preview_rate_limited',scenario='boundary')
        self.rule_call('model','staging',env='staging',index=1);self.rule_call('preview',env='staging',index=1)
        before=self.rule_ledger();source=self.rule_fixture.snapshot();again,_=self.rule_call('model',index=1,scenario='idempotency')
        ro.check(first==again and before==self.rule_ledger() and source==self.rule_fixture.snapshot(),'saved draft replay changed provider or audit')
        self.call('reader','POST','/api/v1/alert-rules/generate',dict(self.rule_body('model'),text='Changed review request'),headers={'Idempotency-Key':'rules-'+self.nonce+'-normal'},want=409,code='idempotency_conflict',scenario='idempotency')
        self.marker_case('generation.rules_normal',{'normal_routes':list(TARGETS),'source_model_mode':'protocol_mock','synthetic_2017_5m_points':True,'public_requested_range_points':13,'test_staging':True,'draft_review_required':True,'exact_saved_replay':True,'real_history':False})
        for kind,path in (('model','generate'),('preview','preview')):
            body=self.rule_body(kind);headers={'Idempotency-Key':'rules-'+self.nonce+'-auth'} if kind=='model' else None;before=self.rule_ledger();source=self.rule_fixture.snapshot()
            for user,status in ((None,401),('denied',403)):
                self.call(user,'POST','/api/v1/alert-rules/'+path,body,headers=headers,want=status,code='unauthorized' if status==401 else 'forbidden',scenario='authorization')
            self.call('reader','POST','/api/v1/alert-rules/'+path,body,headers=headers,environment='staging',want=403,code='forbidden',scenario='authorization');self.call('admin','POST','/api/v1/alert-rules/'+path,body,headers=headers,environment='all',want=409,code='env_view_only',scenario='boundary')
            ro.check(before==self.rule_ledger() and source==self.rule_fixture.snapshot(),'authorization changed upstream/audit')
        self.marker_case('generation.rules_authorization',{'anonymous401':True,'missing_grant_cross_env403':True,'all409':True,'zero_provider_or_audit_delta':True})
        for change in ({'text':''},{'text':'x'*2049},{'application_id':''}):self.call('reader','POST','/api/v1/alert-rules/generate',dict(self.rule_body('model'),**change),headers={'Idempotency-Key':'rules-'+self.nonce+'-invalid'},want=422,code='invalid_rule_description',scenario='boundary')
        self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body('model'),want=422,code='idempotency_key_required',scenario='boundary')
        for change in ({'query':''},{'op':'='},{'threshold':1e10},{'for_minutes':61},{'range':'2h'}):self.call('reader','POST','/api/v1/alert-rules/preview',dict(self.rule_body('preview'),**change),want=422,code='invalid_rule_preview',scenario='boundary')
        self.call('reader','POST','/api/v1/alert-rules/preview',dict(self.rule_body('preview'),query='up'),want=422,code='unreviewed_query',scenario='boundary')
        self.marker_case('generation.rules_boundaries',{'strict_description_key_condition_range':True,'unreviewed_query422':True,'actual10s_budget429':True})
        for kind,modes,path,code in (('model',('503','response_loss','timeout','invalid_query'),'generate','rule_generate_ambiguous'),('preview',('503','response_loss','timeout','partial_matrix','wrong_scope'),'preview','rule_preview_upstream_unavailable')):
            for mode in modes:
                self.wait_budget(kind);self.rule_fixture.mode[kind]=mode;count=len(self.rule_fixture.snapshot());started=time.monotonic();key='fault-'+mode
                try:self.call('reader','POST','/api/v1/alert-rules/'+path,self.rule_body(kind),headers={'Idempotency-Key':'rules-'+self.nonce+'-'+key} if kind=='model' else None,want=502 if kind=='model' else 503,code=code,scenario='failure')
                finally:self.rule_fixture.mode[kind]='normal'
                ro.check(len(self.rule_fixture.snapshot())==count+1,'provider fault automatically retried');ro.append_json(self.work/'rule-fault-times.jsonl',{'kind':kind,'mode':mode,'elapsed_seconds':time.monotonic()-started,'provider_requests':1})
                if kind=='model':
                    before=self.rule_ledger();self.call('reader','POST','/api/v1/alert-rules/generate',self.rule_body(kind),headers={'Idempotency-Key':'rules-'+self.nonce+'-'+key},want=502,code=code,scenario='idempotency');ro.check(before==self.rule_ledger() and len(self.rule_fixture.snapshot())==count+1,'ambiguous saved replay retried provider/changed ledger')
                self.wait_budget(kind);self.rule_call(kind,'recovery-'+mode,index=1,scenario='failure')
        self.marker_case('generation.rules_provider_faults',{'model':['503','response_loss','actual15s_header_timeout','unreviewed_generated_query'],'preview':['503','response_loss','actual10s_header_timeout','partial_matrix','wrong_scope'],'one_call_no_retry':True,'ambiguous_replay':True,'fresh_key_other_API_recovery':True})
        rows=self.rule_ledger();ro.check(all(x['status'] in ('succeeded','ambiguous') for x in rows['v3_rule_generate_attempts']),'rule attempt ledger inconsistent')
        ro._private(self.work/'rules-db-readback.json',rows);ro._private(self.work/'rules-source-receipts.json',self.rule_fixture.snapshot())
        return {'run_id':self.run_id,'scope':'rules','http_calls':self.calls,'normal_routes':[list(x) for x in TARGETS],'evidence_mode':'protocol_mock','synthetic_7day_protocol':True,'real_7day_history':False,'real_model_inference':False}
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;errors=[];pg_allowed=False
        if self.db_attempted and self.api:
            try:self.capture_pg()
            except BaseException as e:errors.append(str(e))
        for s in reversed(self.containers):
            try:self.stop_container(s)
            except BaseException as e:errors.append(str(e))
        pg_allowed=all(s.get('removed') for s in self.containers if s['kind']=='rule-assistant-api') and not errors
        if self.rule_fixture:
            try:self.rule_fixture.close()
            except BaseException as e:errors.append(str(e))
        if self.network_id:
            try:
                info=self.network_info();guard_network(info,self)
                if info.get('Containers'):raise ro.Blocked('own private network still has members; retained')
                ro.check(self.docker(['network','rm',self.network_id],timeout=4).returncode==0,'fresh network remove failed');ro.check(self.docker(['network','inspect',self.network_id],timeout=3).returncode!=0,'ownnetwork persists')
            except BaseException as e:errors.append(str(e))
        elif self.network_attempted and self.docker(['network','inspect',self.network_name],timeout=3).returncode==0:errors.append('unknown network create outcome retained; no adoption')
        if pg_allowed and not errors:
            try:ro.Chain.cleanup(self)
            except BaseException as e:errors.append(str(e))
        elif self.db_attempted:errors.append('UUID PG retained because own API exit not fully attested')
        absence={}
        for port in RESERVED_PORTS:
            with socket.socket() as s:absence[str(port)]=s.connect_ex(('127.0.0.1',port))!=0
        receipt={'ports_absent':absence,'containers':[{'cid':s.get('cid'),'name':s['name'],'removed':s.get('removed',False),'exit_code':s.get('exit_code')} for s in self.containers],'pg_cleanup_eligible':pg_allowed,'network_id':self.network_id,'errors':errors,'single_cleanup_deadline_seconds':40,'saved_PID_or_PID1_signaled':False}
        ro._private(self.work/'container-cleanup.json',receipt)
        if errors or not all(absence.values()):raise ro.Blocked('container cleanup incomplete; exact private evidence retained')
        emit('container.cleanup','passed',receipt)

@contextlib.contextmanager
def lease(root):
    base=Path(root)/'.local/testlab/rule-assistant-container';base.mkdir(mode=0o700,parents=True,exist_ok=True);fd=os.open(base/'lease.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);yield
    finally:os.close(fd)

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--run-id');p.add_argument('--build-proof',type=Path);m=p.add_mutually_exclusive_group(required=True);m.add_argument('--inventory',action='store_true');m.add_argument('--live',action='store_true');a=p.parse_args(argv)
    if a.inventory:Contract(a.root);print(json.dumps({'targets':TARGETS,'ports':RESERVED_PORTS,'evidence_mode':'protocol_mock','offline_only':True}));return 0
    run_id=a.run_id or str(uuid.uuid4())
    try:
        build=prepared_linux_build(a.root,a.build_proof)
        with lease(a.root),context(),ro.cooperative_signals():
            free_ports();data=ro.doctor(a.root)
            if data['source_tree_sha']!=SOURCE_SHA or data['binary_sha256']!=DARWIN_SHA or ro.inspect_pg(data['postgres_container'])['Id']!=PG_CID:raise ro.Blocked('unchanged Darwin/source/PG admission required')
            chain=Chain(a.root,data,run_id,build);resources(chain.work,'admission');ro.execute(chain)
    except ro.Blocked as e:emit('container.environment','blocked',{'error':str(e)});return 2
    except Exception as e:emit('container.failure','failed',{'error_type':type(e).__name__,'error':str(e)});return 1
    return 0

if __name__=='__main__':raise SystemExit(main())
