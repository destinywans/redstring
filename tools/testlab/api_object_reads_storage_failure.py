"""Three owned object GETs: repeat, per-table SELECT503 and recovery.
18 HTTP = two logins + service + plan/investigation Canvas + draft connector
and three GETs in four phases. Role detail/shared authorization stays excluded.
"""
from __future__ import annotations
import argparse, ast, datetime, hashlib, importlib.util, inspect, io, json, re, socket, textwrap, tokenize
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2]
MODULE='formal-object-reads-storage-failure';PORTS=(18938,18939)
_spec=importlib.util.spec_from_file_location('object_read_private_core',ROOT/'tools/testlab/api_core_reads_storage_failure.py')
core=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(core)
ro,inc=core.ro,core.inc
check,Blocked,q=ro.check,ro.Blocked,ro.quote
SOURCE,BINARY,RUNTIME,PG_CID=core.SOURCE,core.BINARY,core.RUNTIME,core.PG_CID
ro.PORTS=PORTS;ro.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/canvases/{id}'),('GET','/investigations/{id}/canvas'),('GET','/connectors/{id}'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/canvases'),('POST','/connectors'))
TABLES=('v3_canvases','branches','v3_connectors')
ERRORS=({'error':{'code':'canvas_unavailable','message':'Canvas unavailable'}},
        {'error':{'code':'investigation_canvas_unavailable','message':'Investigation canvas is unavailable'}},
        {'error':{'code':'storage_unavailable','message':'Connector storage is unavailable'}})
CONNECTOR_URL='http://127.0.0.1:9'
REQUIRED_CASES=tuple('object_read.'+n for n in ('http_inputs',*[str(i)+'.'+phase for i in range(3) for phase in ('baseline','repeat','fault','restore','recovery')],'ledger','cleanup'))+tuple('object_read.api_ready.'+str(i) for i in (0,1))+('readonly.fresh_database','readonly.synthetic_identities','readonly.cleanup','readonly.complete')
def hashes():
    return {**core.hashes(),**{n:core.digest(ROOT/'tools/testlab'/n) for n in ('api_object_reads_storage_failure.py','tests/test_api_object_reads_storage_failure.py','api_canvas_creation_audit_coverage.py','api_components_coverage.py')}}
inc.hashes=hashes
def evidence(row):
    return dict(row,registration_evidence=False,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',test_name='FormalObjectReadsStorageFailure',proof_scope='three formally created object GETs/repeat/per-table SELECT503/recovery; all-public no-write; role excluded; no inference/probe/notification/normal gain or whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields)),flush=True)
def append_json(path,row):return core._append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
ro.emit,ro.append_json=emit,append_json
class Contract(core.Contract.__bases__[0]):
    def canonical(self,method,path):
        p=urlsplit(path);check(not any((p.scheme,p.netloc,p.query,p.fragment)),'exact object read path required')
        canonical,kind=super().canonical(method,path);check((method,canonical.removeprefix('/api/v1')) in TARGETS+SUPPORT,'outside three reads and formal object fixtures');return canonical,kind
inc.Contract=Contract;ro.Contract=Contract
_source=inspect.getsource(core.cap.gate.admission)
for old,new in (('root-admitted-conversation-object-session','root-admitted-object-reads-storage-failure'),('conversation_boundaries_authorized','object_reads_storage_failure_authorized')):
    check(_source.count(old)==1,'released admission seam changed');_source=_source.replace(old,new)
_ns=dict(core.cap.gate.__dict__,ROOT=ROOT,MODULE=MODULE,PORTS=PORTS,SOURCE=SOURCE,BINARY=BINARY,RUNTIME=RUNTIME,PG_CID=PG_CID,hashes=hashes,base=ro)
exec(compile(textwrap.dedent(_source),str(__file__)+'[private admission]','exec'),_ns);admission=_ns['admission'];core.admission=admission
def doctor(root):return dict(core.doctor(root),helper_sha256=hashes())
# Reuse only pure, frozen normal-fixture assertions, without importing their
# lifecycle modules or changing their globals. These are actual prior checks.
_read_ns=dict(check=check,datetime=datetime,RELAY_URL=CONNECTOR_URL)
for _file,_names in (('api_canvas_creation_audit_coverage.py',('root_node','verify_readback')),('api_components_coverage.py',('validate_detail',))):
    _tree=ast.parse((ROOT/'tools/testlab'/_file).read_text())
    for _name in _names:
        _nodes=[n for n in _tree.body if isinstance(n,ast.FunctionDef) and n.name==_name]
        check(len(_nodes)==1,'original pure read assertion changed')
        exec(compile(ast.Module(body=_nodes,type_ignores=[]),str(ROOT/'tools/testlab'/_file),'exec'),_read_ns)
# Same actually passed OID bigint, effective ACL, exact restoration algorithm;
# only Python TABLE NAME tokens and evidence filenames change. SQL TABLE stays.
_acl_ns=dict(core.__dict__)
for _name in ('acl','revoke','restore'):
    _body=textwrap.dedent(inspect.getsource(getattr(core.Chain,_name)))
    for _phase in ('intent','revoked','restored'):
        _body=_body.replace("self.work/'acl-"+_phase+".json'","self.work/('acl-"+_phase+"-'+self.active_table+'.json')")
    _body=tokenize.untokenize((t.type,'self.active_table' if t.type==tokenize.NAME and t.string=='TABLE' else t.string) for t in tokenize.generate_tokens(io.StringIO(_body).readline))
    exec(compile(_body,str(__file__)+'[passed '+_name+']','exec'),_acl_ns)
def verify_error(i,value,headers):
    check(type(i) is int and 0<=i<3 and value==ERRORS[i] and next((v for k,v in headers.items() if k.lower()=='content-type'),'').split(';')[0].strip()=='application/json','exact object mapper storage503 required, not earlier authorization failure')
class Chain(core.Chain):
    acl=_acl_ns['acl'];revoke_table=_acl_ns['revoke'];restore_table=_acl_ns['restore']
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id,permit);self.contract=Contract(root);self.active_table=None;self.paths=[];self.payloads={};self.rows={};self.expected={}
        self.state.update(suite=MODULE,ports=list(PORTS),targets=TARGETS,SQL_business_writes=0,no_model_or_external_actions=True,role_fault_excluded=True);ro._private(self.work/'state.json',self.state)
    def marker_case(self,name,actual):
        check('object_read.'+name in REQUIRED_CASES,'unknown object read marker');self.frozen();emit('object_read.'+name,'passed',dict(run_id=self.run_id,**actual))
    def start(self):
        original=ro.emit
        def ready(name,status,actual,**fields):
            if name.startswith('readonly.api_ready.'):name='object_read.api_ready.'+name[-1]
            return original(name,status,actual,**fields)
        ro.emit=ready
        try:ro.Chain.start(self)
        finally:ro.emit=original
    def seed(self):
        ro.Chain.seed(self)
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in (self.roles['reader'],'canvas','change','environment','test'))+');')
    def ledger(self):
        value=super().ledger();check(set(TABLES).issubset(value),'all three fault tables required in full public ledger');return value
    def inputs(self):
        key='object-read-'+self.nonce;self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201,scenario='fixture')
        row=json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(key)+" AND e.key='test'",read=True))
        check(all(isinstance(row.get(k),str) and re.fullmatch('[A-Za-z0-9_-]+',row[k]) for k in ('app','env')),'formal own service mapping missing');self.app,self.envrow=row['app'],row['env'];self.owner_name=self.marker+' reader'
        ids=[]
        for kind,prefix in (('plan','CV-'),('investigation','INV-')):
            payload=dict(kind=kind,title=self.marker+' '+kind,goal='Owned object read '+kind,services=[self.app],ai_draft=False);value,_=self.call('reader','POST','/api/v1/canvases',payload,want=201,scenario='fixture')
            check(set(value)=={'id','href'} and isinstance(value['id'],str) and re.fullmatch(prefix+'[a-f0-9]{32}',value['id']) and value['href']=='/canvas/'+value['id'],'formal Canvas fixture identity differs');ids.append(value['id']);self.payloads[kind]=payload
        self.plan,self.inv=ids
        query="SELECT json_build_object('plan',(SELECT to_jsonb(c) FROM v3_canvases c WHERE c.id="+q(self.plan)+"),'branches',(SELECT json_agg(to_jsonb(b) ORDER BY b.id) FROM branches b WHERE b.investigation_id="+q(self.inv)+"),'context_revisions',(SELECT json_agg(to_jsonb(r) ORDER BY r.branch_id,r.revision) FROM context_revisions r JOIN branches b ON b.id=r.branch_id WHERE b.investigation_id="+q(self.inv)+'))'
        self.rows=json.loads(self.sql(query,read=True));plan=self.rows['plan'];check(plan['owner_id']==self.users['reader'] and plan['environment_id']=='test' and plan['services']==[self.app] and plan['nodes']==[_read_ns['root_node'](self.payloads['plan']['title'])] and plan['edges']==[] and plan['artifacts']==[],'native blank plan fixture differs')
        check(len(self.rows['branches'])==len(self.rows['context_revisions'])==1 and self.rows['branches'][0]['parent_id'] is None and self.rows['branches'][0]['model_id']=='' and self.rows['branches'][0]['revision']==0 and self.rows['context_revisions'][0]['messages']==[],'native blank root/context fixture differs')
        body=dict(type='prometheus',url=CONNECTOR_URL,auth='none',webhook=True,tools=['query_metrics']);value,_=self.call('admin','POST','/api/v1/connectors',body,want=201,scenario='fixture')
        check(set(value)=={'id'} and isinstance(value['id'],str) and re.fullmatch('[A-Za-z0-9_-]{1,128}',value['id']),'formal connector fixture ID differs');self.connector=value['id']
        state=json.loads(self.sql("SELECT json_build_object('enabled',enabled,'status',status,'environment',environment_id) FROM v3_connectors WHERE id="+q(self.connector),read=True));check(state==dict(enabled=False,status='unverified',environment='test'),'draft connector unexpectedly activated')
        self.paths=['/api/v1/canvases/'+self.plan,'/api/v1/investigations/'+self.inv+'/canvas','/api/v1/connectors/'+self.connector]
        ro._private(self.work/'fixture-proof.json',dict(service=row,plan=self.plan,investigation=self.inv,connector=self.connector,payloads=self.payloads,rows=self.rows,connector_state=state,SQL_business_writes=0))
        self.marker_case('http_inputs',dict(logins=2,HTTP_service=1,HTTP_Canvas=2,HTTP_connector=1,ai_draft=False,connector_unverified=True,inference_or_probe_requested=False,role_excluded=True))
    def verify_read(self,i,value):
        if i<2:
            kind=('plan','investigation')[i];_read_ns['verify_readback'](value,self.rows,self.payloads[kind],(self.plan,self.inv)[i],self.owner_name)
        else:_read_ns['validate_detail'](value,self.connector,'unverified')
    def call(self,user,method,path,*args,**kwargs):
        self.frozen();kwargs.setdefault('scenario','failure' if kwargs.get('want')==503 else 'boundary');original=ro.transport
        def wire(base,verb,target,payload=None,headers=None,**options):
            result=original(base,verb,target,payload,headers,**options)
            if verb=='GET' and target in self.paths:
                status,raw,hs=result;check(len(raw)<=65536,'bounded synthetic object read required');value=json.loads(raw);i=self.paths.index(target)
                ro._private(self.work/('object-read-wire-'+str(self.calls)+'.json'),dict(method=verb,path=target,API_index=ro.BASES.index(base),status=status,response=value,response_bytes=len(raw),response_sha256=hashlib.sha256(raw).hexdigest(),headers={k:v for k,v in hs.items() if k.lower() in ('content-type','cache-control')},credentials_stored=False))
                if status==503:verify_error(i,value,hs)
            return result
        ro.transport=wire
        try:return ro.Chain.call(self,user,method,path,*args,**kwargs)
        finally:ro.transport=original
    def exercise(self,i,baseline):
        check(type(i) is int and 0<=i<3,'only three object readers');path=self.paths[i];self.active_table=TABLES[i]
        for phase,index in (('baseline',0),('repeat',1)):
            value,_=self.call('reader','GET',path,index=index,want=200,validate=lambda v:self.verify_read(i,v))
            if phase=='baseline':self.expected[i]=value
            else:check(value==self.expected[i],'crossAPI same object detail differs')
            check(self.ledger()==baseline,'object read changed full public ledger');self.marker_case(str(i)+'.'+phase,dict(HTTP200=True,API_index=index,all_public_unchanged=True))
        primary=None
        try:
            self.revoke_table();self.call('reader','GET',path,index=0,want=503,code=ERRORS[i]['error']['code'])
            check(self.ledger()==baseline,'failed object read changed full public ledger');self.marker_case(str(i)+'.fault',dict(HTTP503=True,table=self.active_table,exact_mapper=True,all_public_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.restore_table()
            except BaseException as error:
                if primary is None:primary=error
                else:
                    try:ro._private(self.work/('restore-secondary-'+str(i)+'.json'),dict(primary_type=type(primary).__name__,restore_type=type(error).__name__))
                    except BaseException:self.restore_secondary_note_failed=True
        if primary is not None:raise primary
        self.marker_case(str(i)+'.restore',dict(original_effective_ACL_restored=True,no_retry=True))
        value,_=self.call('reader','GET',path,index=1,want=200,validate=lambda v:self.verify_read(i,v));check(value==self.expected[i] and self.ledger()==baseline,'recovered object DTO/full ledger differs');self.marker_case(str(i)+'.recovery',dict(HTTP200=True,API_index=1,same_cookie=True,all_public_unchanged=True));self.active_table=None
    def run(self):
        self.provision();self.start();self.seed();self.login('admin');self.login('reader');self.inputs();baseline=self.ledger();ro._private(self.work/'ledger-baseline.json',baseline)
        for i in range(3):self.exercise(i,baseline)
        check(self.ledger()==baseline and not self.acl_fault and self.calls==18,'exact18 HTTP/final ACL/ledger boundary differs');ro._private(self.work/'ledger-final.json',baseline);self.marker_case('ledger',dict(target_HTTP=12,fixture_HTTP=6,all_public_tables=len(baseline),sessions_audit_unchanged=True,role_fault_unverified=True))
        result=dict(run_id=self.run_id,http_calls=18,target_HTTP=12,fixture_HTTP=6,required_cases=REQUIRED_CASES,normal_added=0,registration_evidence=False,counts_as_whole_acceptance=False,role_fault_unverified=True,source_tree_sha=SOURCE,binary_sha256=BINARY);ro._private(self.work/'object-read-result.json',result);return result
    def cleanup(self):
        ro.Chain.cleanup(self)
        for port in PORTS:
            with socket.socket() as sock:sock.settimeout(.3);check(sock.connect_ex(('127.0.0.1',port))!=0,'own object read listener remains')
        emit('object_read.cleanup','passed',dict(single40=True,grace45=True,owned_UUID_only=True,unrestored_fault_resource_retired=self.acl_fault))
def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=ROOT);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');p.add_argument('--run-id');p.add_argument('--admission',type=Path);a=p.parse_args(argv)
    if a.inventory:
        Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES,http_calls=18,target_HTTP=12,fixture_HTTP=6,API_seconds=12,SQL_seconds=8,outer_seconds=420,cleanup_seconds=40,grace_seconds=45,role_fault_unverified=True)));return 0
    if not(a.live and a.run_id and a.admission):p.error('root explicit --live/run-id/admission required')
    try:
        check(a.root.resolve()==ROOT,'reviewed checkout required');admission(a.admission,a.run_id)
        with ro.exclusive(ROOT),ro.cooperative_signals():ro.execute(Chain(ROOT,doctor(ROOT),a.run_id,a.admission))
        return 0
    except Blocked:emit('object_read.environment','blocked',dict(first_failure_preserved=True));return 2
    except Exception as error:emit('object_read.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
if __name__=='__main__':raise SystemExit(main())
