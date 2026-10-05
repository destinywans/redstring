"""Formal plan/investigation creation audit rollback with explicit HTTP recovery.

Only --live with root admission acquires resources. SQL writes are synthetic
identity/ACL and UUID DDL faults; canvas business writes use formal HTTP.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-canvas-creation-audit';PORTS=(18844,18845);COMMAND_BUDGET=420
SOURCE='93f768edb580edd84f5dceb702e808103e77d38bbe42ee29b11a74558b0aeeeb'
BINARY='6d29875d2b57c51fe812bc9e1f8746e662c75117c1403e7bfb3c7cab43005c70'
RUNTIME='338e14784bb40f2d05000a8b76322cc8bea158e65ce3357c014f1111373fe48d'
PG_CID='c15f900dd06c1026de8b79734eb689f088a5f6d60da3c8a88e618499743ab3a7'
spec=importlib.util.spec_from_file_location('canvas_creation_private_management',ROOT/'tools/testlab/api_management_chain.py')
base=importlib.util.module_from_spec(spec);spec.loader.exec_module(base);check,Blocked,q=base.check,base.Blocked,base.quote
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
base.TARGETS=(('POST','/canvases'),);base.SUPPORT=(('POST','/auth/login'),('POST','/services'),('GET','/canvases/{id}'),('GET','/investigations/{id}/canvas'))
REQUIRED_CASES=('canvas_creation.http_service','canvas_creation.permissions','canvas_creation.boundaries',)+tuple('canvas_creation.'+kind+'.'+stage for kind in ('plan','investigation') for stage in ('rollback','fault_removed','recovery','readback'))+('canvas_creation.cleanup','management.cleanup','management.complete')
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_canvas_creation_audit_coverage.py','tests/test_api_canvas_creation_audit_coverage.py',
        'api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,
    binary_platform='darwin/arm64 Mach-O',test_name='FormalCanvasCreationAudit',registration_evidence=False,
    proof_scope='plan/investigation creation audit rollback and samebody recovery only; no model/template/chain; normal_added=0; no whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=base.append_json
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=emit;base.append_json=append_json
_inspect=base.inspect_pg
def inspect_pg(*args,**kwargs):
    value=_inspect(*args,**kwargs);check(value.get('Id')==PG_CID,'exact ownedPG differs');return value
base.inspect_pg=inspect_pg
def frozen_identity(data):
    check(data.get('source_tree_sha')==SOURCE and data.get('binary_sha256')==BINARY and data.get('runtime_sha')==RUNTIME
        and data.get('helper_sha256')==hashes(),'source/binary/runtime/transitive helpers differ')
def doctor(root):
    check(Path(root).resolve()==ROOT,'reviewed checkout required');data=base.doctor(root)
    check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    data=dict(data,runtime_sha=RUNTIME,helper_sha256=hashes());frozen_identity(data);return data
def admission(path,run_id):
    path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonicalUUID/private admission required')
    st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 admission required')
    permit=base.private_json(path)
    expected=dict(status='root-admitted-canvas-creation-audit',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'canvas creation root admission identity differs')
    for k in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','canvas_creation_audit_fault_authorized'):check(permit.get(k) is True,'missing root prerequisite '+k)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative canvas path required')
        return super().canonical(method,path)
TABLES=('v3_canvases','investigations','branches','context_revisions','events','management_audit')
ACTIONS={'plan':'v3.canvas.created','investigation':'v3.investigation_canvas.created'}
def root_node(title):return dict(id='root',x=16,y=140,w=260,icon='layers',tone='info',title=title,sub='主线',blocks=[])
def changes(payload):
    v=dict(environment_id='test',kind=payload['kind'],services=payload['services'],ai_draft=False,model_id='')
    if payload['kind']=='plan':v.update(template_id='',retained_runbooks=0)
    return v
def fault_statements(nonce,actor,app,envrow,payload):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)) and bool(re.fullmatch('[a-f0-9]{32}',actor)) and payload['kind'] in ACTIONS,'exactUUID/actor/kind required')
    check(all(isinstance(v,str) and re.fullmatch('[A-Za-z0-9_-]+',v) for v in (app,envrow)) and payload['services']==[app] and payload['ai_draft'] is False and 'template_id' not in payload,'exact own app/readonly environment/no-model payload required')
    kind=payload['kind'];name='canvas_creation_fail_'+nonce
    if kind=='plan':lineage='EXISTS(SELECT 1 FROM v3_canvases c WHERE c.id=NEW.target AND c.owner_id='+q(actor)+" AND c.environment_id='test' AND c.title="+q(payload['title'])+' AND c.goal='+q(payload.get('goal',''))+' AND c.kind=\'plan\' AND c.services=ARRAY['+q(app)+']::text[])';auditapp=''
    else:lineage='EXISTS(SELECT 1 FROM investigations i WHERE i.id=NEW.target AND i.created_by='+q(actor)+' AND i.application_id='+q(app)+' AND i.environment_id='+q(envrow)+' AND i.title='+q(payload['title'])+' AND i.goal='+q(payload.get('goal',''))+')';auditapp=app
    create='BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+" AND NEW.actor_kind='user' AND NEW.application_id="+q(auditapp)+' AND NEW.action='+q(ACTIONS[kind])+" AND NEW.outcome='success' AND NEW.changes="+q(json.dumps(changes(payload)))+'::jsonb AND '+lineage+" THEN RAISE EXCEPTION 'owned canvas creation audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;'
    return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
def additions(old,new,count):
    # Compare complete prior rows, independent of random ID sort order.
    from collections import Counter
    key=lambda r:json.dumps(r,sort_keys=True,separators=(',',':'))
    before,after=Counter(map(key,old)),Counter(map(key,new));check(not(before-after),'creation changed/deleted complete prior rows')
    delta=after-before;check(sum(delta.values())==count,'creation exact row delta differs')
    return [json.loads(k) for k,n in delta.items() for _ in range(n)]
def verify_creation(before,after,payload,actor,app,envrow,id):
    kind=payload['kind'];check(re.fullmatch(('CV-' if kind=='plan' else 'INV-')+'[a-f0-9]{32}',id) is not None,'formal creation id prefix/shape differs')
    check(before['business']==after['business'],'creation changed other complete public tables')
    wanted={t:0 for t in TABLES};wanted['management_audit']=1
    wanted.update({'v3_canvases':1} if kind=='plan' else dict(investigations=1,branches=1,context_revisions=1,events=1))
    rows={t:additions(before[t],after[t],wanted[t]) for t in TABLES}
    audit=rows['management_audit'][0]
    expected=dict(actor_id=actor,actor_kind='user',application_id='' if kind=='plan' else app,action=ACTIONS[kind],target=id,outcome='success',changes=changes(payload))
    check(all(audit.get(k)==v for k,v in expected.items()),'creation exact audit actor/app/action/target/changes differs')
    if kind=='plan':
        row=rows['v3_canvases'][0];expected=dict(id=id,owner_id=actor,environment_id='test',title=payload['title'],kind='plan',goal=payload.get('goal',''),services=[app],status='draft',nodes=[root_node(payload['title'])],edges=[],artifacts=[])
        check(all(row.get(k)==v for k,v in expected.items()),'plan persisted blank root/owner/env/services differs')
        check(row['created_at']==row['updated_at'] and row['created_at'],'plan initial timestamps differ')
    else:
        inv,branch,revision,event=(rows[t][0] for t in ('investigations','branches','context_revisions','events'))
        expected=dict(id=id,application_id=app,environment_id=envrow,created_by=actor,title=payload['title'],goal=payload['goal'],status='open')
        check(all(inv.get(k)==v for k,v in expected.items()) and inv['created_at']==inv['updated_at'],'investigation owner/app/real envrow/initial status differs')
        check(bool(re.fullmatch('[a-f0-9]{32}',branch['id'])) and all(branch.get(k)==v for k,v in dict(investigation_id=id,parent_id=None,title='主调查',model_id='',revision=0,inherited_parent_revision=None,synced_parent_revision=None).items()),'investigation blank root lineage differs')
        check(all(revision.get(k)==v for k,v in dict(branch_id=branch['id'],revision=0,messages=[],provenance={'kind':'initial'}).items()),'initial context contains fabricated model/history')
        check(all(event.get(k)==v for k,v in dict(investigation_id=id,branch_id=branch['id'],type='investigation.created',payload={'id':id}).items()),'initial event lineage differs')
    return rows
def same(before,after):check(before==after,'failure/read changed six complete tables or other public digest')
def verify_readback(value,rows,payload,id,owner):
    if payload['kind']=='plan':
        expected=dict(id=id,title=payload['title'],kind='plan',status='draft',participants=[owner],note=payload.get('goal',''),nodes=[root_node(payload['title'])],edges=[],artifacts=[],threads={'root':{'context':'主线','turns':[]}},env='test')
        check(value==expected,'plan secondAPI exact detail/root/emptythreads differs')
    else:
        root=rows['branches'][0];rev=rows['context_revisions'][0]
        check(set(value)=={'investigation','loop','nodes','tray'} and value['investigation']==dict(id=id,title=payload['title'],status='active',env='test',participants=[owner]) and value['loop']==[] and value['tray']==[],'investigation exact scope/header/empty evidence differs')
        check(len(value['nodes'])==1,'investigation must have one actual root only')
        node=value['nodes'][0];version=node.get('versions',[])
        expected=dict(id=root['id'],kind='root',title='主调查',subtitle='Main investigation',status='active',x=16,y=40,w=260,collapsed=False,layout_version=0,context={'version':'v0','tokens':None,'messages':0,'excludes':[]},versions=version)
        check(node==expected and len(version)==1 and set(version[0])=={'v','text','at'} and version[0]['v']=='v0' and version[0]['text']=='Context revision 0','investigation native graph/root/context projection differs')
        stamp=lambda v:datetime.datetime.fromisoformat(v.replace('Z','+00:00'))
        check(stamp(version[0]['at'])==stamp(rev['created_at']),'graph revision time not persisted context')

class Chain(base.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.fault_attempted=False;self.raw_number=0;self.actor=None;self.fault_payload=None;self.contract=Contract(root)
        self.state.update(suite=MODULE,normal_added=0,counts_as_whole_acceptance=False,binary_platform='darwin/arm64 Mach-O',targets=base.TARGETS,runtime_sha=RUNTIME,model_scope='ai_draft=false; no inference or template request')
        base._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen();frozen_identity(self.data)
        check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    def marker_case(self,suffix,actual):
        name='canvas_creation.'+suffix;check(name in REQUIRED_CASES,'unknown canvas marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def call(self,*args,**kwargs):
        self.frozen();original=base.transport
        def wire(origin,method,path,payload=None,headers=None,timeout=12):
            result=original(origin,method,path,payload,headers,timeout)
            if path=='/api/v1/canvases' or path.startswith('/api/v1/canvases/') or path.startswith('/api/v1/investigations/'):
                self.raw_number+=1;status,raw,_=result
                base._private(self.work/('canvas-wire-'+str(self.raw_number)+'.json'),dict(method=method,path=path,API_index=base.BASES.index(origin),status=status,request_body=None if payload is None else payload.decode('utf-8'),response_body=raw.decode('utf-8','replace'),request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',synthetic_owned_only=True))
            return result
        base.transport=wire
        try:return super().call(*args,**kwargs)
        finally:base.transport=original
    def owner(self,deadline=None):
        query="SELECT json_build_object('database',current_database(),'role',current_user,'db_tag',(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()),'role_tag',(SELECT shobj_description(oid,'pg_authid') FROM pg_roles WHERE rolname=current_user))"
        v=json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline));check(v==dict(database=self.database,role=self.db_user,db_tag=self.owner_tag,role_tag=self.owner_tag),'ownUUID DB/role tags differ')
    def secondary(self,path,error,primary):
        try:self.diagnostic(path,dict(error_type=type(error).__name__,first_failure_preserved=True))
        except BaseException as error:primary.add_note('secondary diagnostic unavailable: '+type(error).__name__)
    def ledger(self):
        self.frozen();self.owner();excluded=','.join(q(t) for t in TABLES)
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ("+excluded+')',read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        hashes=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        fields=["'business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(hashes)+') hashes)']
        fields += [q(t)+",(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM public."+t+' t)' for t in TABLES]
        return json.loads(self.sql('SELECT json_build_object('+','.join(fields)+')',read=True))
    def save_ledger(self,name,value):base._private(self.work/(name+'-ledger.json'),dict(value,source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
    def inputs(self):
        key='canvas-audit-'+self.nonce;self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201,purpose='fixture')
        row=json.loads(self.sql("SELECT json_build_object('app',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+q(key)+" AND e.key='test'",read=True))
        check(all(isinstance(row.get(k),str) and re.fullmatch('[A-Za-z0-9_-]+',row[k]) for k in ('app','env')),'HTTP-created app/env mapping missing');self.app,self.envrow=row['app'],row['env'];self.actor=self.users['reader'];self.owner_name=self.marker+' reader'
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES '+','.join('('+','.join(q(v) for v in (self.roles['reader'],'canvas',action,'environment','test'))+')' for action in ('view','change'))+';')
        base._private(self.work/'input-proof.json',dict(app=self.app,envrow=self.envrow,actor=self.actor,source='HTTPservices201->readonlyIDs',source_tree_sha=SOURCE,binary_sha256=BINARY,SQL_business_writes=0));self.marker_case('http_service',dict(single_HTTP_service=True,ai_draft=False,no_template=True,SQL_business_writes=0))
    def payload(self,kind):return dict(kind=kind,title=self.marker+' '+kind,goal='Bounded synthetic '+kind+' creation; no inference',services=[self.app],ai_draft=False)
    def reject(self,user,payload,status,code):
        before=self.ledger();self.call(user,'POST','/api/v1/canvases',payload,want=status,code=code,scenario='authorization' if status in (401,403) else 'boundary');same(before,self.ledger())
    def permissions(self):
        for kind in ACTIONS:
            payload=self.payload(kind);self.reject(None,payload,401,'unauthorized');self.reject('denied',payload,403,'forbidden')
        self.marker_case('permissions',dict(two_kinds_anonymous401_noGrant403=True,zero_sideeffects=True))
        self.reject('reader',dict(self.payload('plan'),title=''),422,'invalid_canvas');self.reject('reader',dict(self.payload('investigation'),services=[]),422,'invalid_service')
        self.marker_case('boundaries',dict(blank_title422=True,investigation_single_service_required=True,zero_sideeffects=True))
    def install_fault(self,payload):
        self.frozen();self.owner();self.fault_payload=copy.deepcopy(payload);self.fault_attempted=True
        base._private(self.work/'canvas-fault-intent.json',dict(database=self.database,actor=self.actor,app=self.app,envrow=self.envrow,payload=payload,action=ACTIONS[payload['kind']]))
        self.sql(fault_statements(self.nonce,self.actor,self.app,self.envrow,payload)[1])
    def remove_fault(self,deadline=None):
        if not self.fault_attempted:return
        deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4
        self.owner(deadline=deadline);name,_,drop=fault_statements(self.nonce,self.actor,self.app,self.envrow,self.fault_payload);self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        check(json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))==dict(trigger=False,function=False),'own canvas fault remains');self.fault_attempted=False
    def exercise(self,kind):
        payload=self.payload(kind);before=self.ledger();self.save_ledger(kind+'-before',before);primary=None
        try:
            self.install_fault(payload);self.call('reader','POST','/api/v1/canvases',payload,want=503,code='audit_unavailable',scenario='failure')
            failed=self.ledger();self.save_ledger(kind+'-failed',failed);same(before,failed)
            self.marker_case(kind+'.rollback',dict(status=503,error_code='audit_unavailable',six_complete_tables_unchanged=True,other_public_digest_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.remove_fault()
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'restore-secondary-error.json',error,primary)
        if primary is not None:raise primary
        self.marker_case(kind+'.fault_removed',dict(trigger=False,function=False))
        value,_=self.call('reader','POST','/api/v1/canvases',payload,want=201,index=1,scenario='failure');check(set(value)=={'id','href'} and value['href']=='/canvas/'+value['id'],'create exact response differs')
        after=self.ledger();rows=verify_creation(before,after,payload,self.actor,self.app,self.envrow,value['id']);self.save_ledger(kind+'-recovered',after)
        self.marker_case(kind+'.recovery',dict(status=201,same_body=True,API_index=1,exact_audit_delta=1,actual_id=value['id'],explicit_recovery=True))
        path='/api/v1/canvases/'+value['id'] if kind=='plan' else '/api/v1/investigations/'+value['id']+'/canvas'
        read,_=self.call('reader','GET',path,index=1,scenario='failure');verify_readback(read,rows,payload,value['id'],self.owner_name);same(after,self.ledger())
        self.marker_case(kind+'.readback',dict(API_index=1,exact_native_root_context=True,no_read_sideeffects=True))
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader','denied'):self.login(actor)
        self.inputs();self.permissions()
        for kind in ACTIONS:self.exercise(kind)
        self.frozen();result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],required_cases=REQUIRED_CASES,http_calls=self.calls,normal_added=0,counts_as_whole_acceptance=False,unverified=['AI draft/model/template authority','branch/reply/writeback/full chain','all scope/concurrency/crash races','performance/soak'])
        base._private(self.work/'canvas-creation-result.json',result);return result
    def cleanup(self):
        deadline=time.monotonic()+40;primary=None
        try:self.remove_fault(deadline=min(deadline,time.monotonic()+4))
        except BaseException as error:primary=error
        finally:
            self.cleanup_deadline=deadline
            try:bounded_cleanup(self)
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'cleanup-secondary-error.json',error,primary)
        if primary is not None:raise primary
        for port in PORTS:
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'canvas owned API port occupied')
        emit('canvas_creation.cleanup','passed',dict(single_cleanup_seconds=40,restore_within4=True,owned_UUID_only=True,ports_free=list(PORTS)))
_source=textwrap.dedent(inspect.getsource(base.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_ns=dict(base.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared fault/PG deadline]','exec'),_ns);bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,required_cases=REQUIRED_CASES,estimated_HTTP_calls=16,command_timeout_seconds=COMMAND_BUDGET,API_timeout_seconds=12,cleanup_seconds=40,cleanup_grace_seconds=45,normal_added=0)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private management lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with base.cooperative_signals():base.execute(Chain(ROOT,doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('canvas_creation.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('canvas_creation.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
