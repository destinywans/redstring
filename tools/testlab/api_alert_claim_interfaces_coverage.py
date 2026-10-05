"""Formal alert detail/claim with HTTP-produced webhook alerts, no lease/TTL.

Preparation is offline. Root-reviewed --live is required for fresh UUID resources.
Existing binary CAS/auth evidence is retained; this group adds native ingress,
independent view/change authority and exact claim audit rollback/recovery only.
"""
from __future__ import annotations
import argparse,copy,datetime,fcntl,hashlib,importlib.util,inspect,json,os,re,socket,sqlite3,stat,textwrap,time,types,uuid
from pathlib import Path
from urllib.parse import urlsplit
ROOT=Path(__file__).resolve().parents[2];MODULE='formal-api-alert-claim-interfaces';PORTS=(18840,18841)
_spec=importlib.util.spec_from_file_location('alert_claim_private_workspace_lifecycle',ROOT/'tools/testlab/api_workspace_audit_rollback_coverage.py')
support=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(support);base=support.base
check,Blocked,q=base.check,base.Blocked,base.quote
SOURCE,BINARY,RUNTIME,PG_CID=support.SOURCE,support.BINARY,support.RUNTIME,support.PG_CID
base.PORTS=PORTS;base.BASES=tuple('http://127.0.0.1:'+str(p) for p in PORTS)
TARGETS=(('GET','/alerts/{id}'),('POST','/alerts/{id}/claim'))
SUPPORT=(('POST','/auth/login'),('POST','/auth/logout'),('POST','/services'),('POST','/webhooks/alertmanager/{source}'))
base.TARGETS=TARGETS;base.SUPPORT=SUPPORT
REQUIRED_CASES=tuple('alert_claim.'+n for n in ('http_ingress','detail','authorization','revision_boundaries','claim','current_revision_retry',
    'stale_and_assigned','audit_rollback','fault_removed','audit_recovery','current_authority','session_revocation','ledger','cleanup'))+('management.cleanup','management.complete')
READY_CASES=tuple('alert_claim.api_ready.'+stage+'.'+str(i) for stage in ('setup','source') for i in (0,1))
def digest(path):return hashlib.sha256(Path(path).read_bytes()).hexdigest()
def hashes():
    return {n:digest(ROOT/'tools/testlab'/n) for n in ('api_alert_claim_interfaces_coverage.py','tests/test_api_alert_claim_interfaces_coverage.py',
        'api_workspace_audit_rollback_coverage.py','api_core_coverage.py','api_management_chain.py','runner.py','store.py','identity.py','faults.py','backend.py','env.py','inventory.py','components.py')}
support.hashes=hashes
def evidence(row):return dict(row,evidence_mode='real_local',upstream_mode='real_local',source_tree_sha=SOURCE,binary_sha256=BINARY,
    binary_platform='darwin/arm64 Mach-O',test_name='FormalAlertDetailClaimInterfaces',registration_evidence=False,
    proof_scope='HTTP webhook produced alerts, view/change authorization and claim audit rollback/recovery; original binary CAS reused; normal_added=0; no whole acceptance')
def emit(name,status,actual,**fields):
    fields.pop('module',None);fields.pop('evidence_mode',None)
    if 'interface' in fields:fields['interface']=evidence(fields['interface'])
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode='real_local',**fields),ensure_ascii=False),flush=True)
_append=support._append # Use the original writer, not the workspace evidence decorator.
def append_json(path,row):return _append(path,evidence(row) if Path(path).name=='interfaces.jsonl' else row)
base.emit=emit;base.append_json=append_json
# Compile an unchanged private core copy with the private management lifecycle,
# avoiding the canonical module import and all original whole-group run methods.
core=types.ModuleType('alert_claim_private_core');core.__file__=str(ROOT/'tools/testlab/api_core_coverage.py')
_core_source=Path(core.__file__).read_text();check(_core_source.count('import api_management_chain as management')==1,'private core import source drift')
core.__dict__['_management_private']=base
exec(compile(_core_source.replace('import api_management_chain as management','management = _management_private'),core.__file__+'[private import]','exec'),core.__dict__)
def admission(path,run_id):
    path=Path(path);check(str(uuid.UUID(run_id))==run_id and path.is_absolute() and path.resolve()==path and path.is_relative_to(ROOT/'.local/testlab'),'canonicalUUID/private admission required')
    st=path.lstat();check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600 and st.st_uid==os.getuid(),'own regular0600 admission required')
    permit=base.private_json(path)
    expected=dict(status='root-admitted-alert-claim-interfaces',owner=base.OWNER,resource_run_id=run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,
        runtime_sha256=RUNTIME,postgres_cid=PG_CID,ports=list(PORTS),helper_sha256=hashes())
    check(all(permit.get(k)==v for k,v in expected.items()),'alert claim root admission identity differs')
    for k in ('fresh_nonce_hmac_process_tuple_verified','headroom_verified','alert_claim_audit_fault_authorized'):check(permit.get(k) is True,'missing root prerequisite '+k)
    conn=sqlite3.connect((ROOT/'.local/testlab/state.sqlite3').resolve().as_uri()+'?mode=ro',uri=True,timeout=5)
    try:active=[json.loads(r[0]) for r in conn.execute('SELECT data FROM runs') if json.loads(r[0]).get('status')=='running']
    finally:conn.close()
    check(len(active)==1 and active[0].get('selected_suite')==MODULE and active[0].get('source_tree_sha')==SOURCE
        and re.fullmatch('[a-f0-9]{64}',permit.get('manifest_sha256','')) and active[0].get('manifest_sha')==permit['manifest_sha256']
        and active[0].get('pid')==os.getppid(),'sole actual matching Runner/manifest/source/parent required')
    return permit
class Contract(base.Contract):
    def canonical(self,method,path):
        parsed=urlsplit(path);check(not any((parsed.scheme,parsed.netloc,parsed.query,parsed.fragment)),'exact relative alert path required')
        return super().canonical(method,path)
def valid_revision(value):return isinstance(value,str) and bool(re.fullmatch('[1-9][0-9]*',value)) and int(value)<=4294967295
def fault_statements(nonce,actor,app,target):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)) and bool(re.fullmatch('[a-f0-9]{32}',actor))
        and all(bool(re.fullmatch('[A-Za-z0-9_-]+',v)) for v in (app,target)),'exact UUID/actor/app/target required')
    name='alert_claim_fail_'+nonce
    create=('BEGIN; CREATE FUNCTION public.'+name+'() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_id='+q(actor)+' AND NEW.application_id='+q(app)+
        " AND NEW.action='alert.claim' AND NEW.target="+q(target)+" AND NEW.outcome='succeeded' AND NEW.changes=jsonb_build_object('environment_id','test','assignee_id',"+q(actor)+
        ") THEN RAISE EXCEPTION 'owned claim audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER "+name+' BEFORE INSERT ON public.management_audit FOR EACH ROW EXECUTE FUNCTION public.'+name+'(); COMMIT;')
    return name,create,'BEGIN; DROP TRIGGER IF EXISTS '+name+' ON public.management_audit; DROP FUNCTION IF EXISTS public.'+name+'(); COMMIT;'
DETAIL_FIELDS=set('id revision severity title state source_id source env since rule triage series labels summary runbook notifications notifications_truncated grouping assignee_id investigation_id unavailable'.split())
UNAVAILABLE=['source','rule','triage','series','labels','runbook','grouping.group','grouping.inhibited','grouping.change']
def verify_detail(value,row,source_id,env):
    check(set(value)==DETAIL_FIELDS,'exact alert detail fields differ')
    expected=dict(id=row['id'],revision=row['revision'],severity='P1',title=row['title'],state=row['alert_status'],source_id=source_id,env=env,
        summary=row['summary'],assignee_id=row['assignee_id'],investigation_id=row['investigation_id'],notifications=[],notifications_truncated=False,
        grouping={'group':None,'inhibited':None,'incident':{'id':row['id']},'change':None},unavailable=UNAVAILABLE)
    check(all(value[k]==v for k,v in expected.items()) and valid_revision(value['revision']),'native detail occurrence/scope/revision differs')
    check(all(value[k] is None for k in ('source','rule','triage','series','labels','runbook')),'unbound provider metadata fabricated')
    stamp=lambda v:datetime.datetime.fromisoformat(v.replace('Z','+00:00'))
    check(stamp(value['since'])==stamp(row['starts_at']),'native occurrence start differs')
def verify_claim(before,after,target,actor,app):
    old={r['id']:r for r in before['incidents']};new={r['id']:r for r in after['incidents']}
    check(set(old)==set(new) and target in old and all(new[k]==v for k,v in old.items() if k!=target),'claim changed unrelated complete incident rows')
    check(old[target]['assignee_id']=='' and new[target]['assignee_id']==actor,'claim assignment differs')
    check(all(valid_revision(r[target]['revision']) for r in (old,new)) and old[target]['revision']!=new[target]['revision'],'claim xmin must change; not an assumed +1 counter')
    strip=lambda r:{k:v for k,v in r.items() if k not in ('assignee_id','updated_at','revision')}
    check(strip(old[target])==strip(new[target]),'claim changed persisted alert/source/binding/status/labels')
    stamp=lambda v:datetime.datetime.fromisoformat(v.replace('Z','+00:00'))
    check(stamp(new[target]['updated_at'])>stamp(old[target]['updated_at']),'claim timestamp did not advance')
    check(before['business']==after['business'],'claim changed other complete public table hashes')
    check(after['audits'][:-1]==before['audits'] and len(after['audits'])==len(before['audits'])+1,'claim complete audit prefix/exact one differs')
    row=after['audits'][-1]
    check(all(row.get(k)==v for k,v in dict(actor_id=actor,actor_kind='user',application_id=app,action='alert.claim',target=target,outcome='succeeded',changes={'environment_id':'test','assignee_id':actor}).items()),'claim exact actor/action/target/audit scope differs')
    return new[target]
def verify_unchanged(before,after):check(before==after,'refusal/retry/read/audit failure changed full incident/business/audit ledger')

class Chain(core.Chain):
    def __init__(self,root,data,run_id,permit):
        super().__init__(root,data,run_id);self.permit=permit;self.contract=Contract(root);self.fault_attempted=False;self.raw_number=0;self.generation=0
        self.state.update(suite=MODULE,normal_added=0,counts_as_whole_acceptance=False,binary_platform='darwin/arm64 Mach-O',targets=TARGETS)
        base._private(self.work/'state.json',self.state)
    def frozen(self):
        admission(self.permit,self.run_id);super().frozen();support.frozen_identity(self.data)
        check(digest(ROOT/'.local/testlab/runtime-config.json')==RUNTIME,'runtime bytes changed')
    def start(self):
        check(self.generation in (0,1),'only two declared API generations permitted')
        stage=('setup','source')[self.generation];original=base.emit
        def generation_emit(name,status,actual,**fields):
            if name in ('management.api_ready.0','management.api_ready.1'):name='alert_claim.api_ready.'+stage+'.'+name[-1]
            return original(name,status,actual,**fields)
        base.emit=generation_emit
        try:base.Chain.start(self)
        finally:base.emit=original
        self.generation+=1
    def seed(self):base.Chain.seed(self) # No inherited broad events environment grant.
    owner=support.Chain.owner
    secondary=support.Chain.secondary
    def marker_case(self,suffix,actual):
        name='alert_claim.'+suffix;check(name in REQUIRED_CASES,'unknown alert marker');self.frozen();emit(name,'passed',dict(run_id=self.run_id,**actual))
    def call(self,user,method,path,*args,raw_body=None,**kwargs):
        self.frozen();check(raw_body is None or isinstance(raw_body,bytes) and len(raw_body)<=8192,'bounded malformed-body fixture required')
        if raw_body is not None:check(method=='POST' and bool(re.fullmatch('/api/v1/alerts/[A-Za-z0-9_-]+/claim',path)),'malformed body only for exact own claim')
        original=base.transport
        def wire(origin,verb,target,payload=None,headers=None,timeout=12):
            if raw_body is not None:check((verb,target)==(method,path),'malformed fixture request mismatch');payload=raw_body
            result=original(origin,verb,target,payload,headers,timeout)
            if target.startswith('/api/v1/alerts/') or target=='/api/v1/webhooks/alertmanager/'+self.source_id:
                self.raw_number+=1;status,raw,response_headers=result
                base._private(self.work/('alert-wire-'+str(self.raw_number)+'.json'),dict(method=verb,path=target,API_index=base.BASES.index(origin),status=status,
                    request_body=None if payload is None else payload.decode('utf-8','replace'),response_body=raw.decode('utf-8','replace'),
                    request_sha256=hashlib.sha256(payload or b'').hexdigest(),response_sha256=hashlib.sha256(raw).hexdigest(),Cache_Control=base.header(response_headers,'Cache-Control'),
                    source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',synthetic_owned_only=True))
            return result
        base.transport=wire
        try:return super().call(user,method,path,*args,**kwargs)
        finally:base.transport=original
    def ledger(self):
        self.frozen();self.owner()
        tables=json.loads(self.sql("SELECT COALESCE(json_agg(tablename ORDER BY tablename),'[]'::json) FROM pg_tables WHERE schemaname='public' AND tablename NOT IN ('management_audit','ops_incidents')",read=True))
        check(tables and len(tables)<=512 and all(re.fullmatch('[a-z][a-z0-9_]*',t) for t in tables),'bounded exact public tables required')
        fields=['SELECT '+q(t)+" AS name,encode(sha256(convert_to(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text,'UTF8')),'hex') AS digest FROM public."+t+' t' for t in tables]
        return json.loads(self.sql("SELECT json_build_object('business',(SELECT json_object_agg(name,digest) FROM ("+' UNION ALL '.join(fields)+") hashes),'incidents',(SELECT COALESCE(json_agg(to_jsonb(t)||jsonb_build_object('revision',xmin::text) ORDER BY id),'[]'::json) FROM ops_incidents t),'audits',(SELECT COALESCE(json_agg(to_jsonb(t) ORDER BY id),'[]'::json) FROM management_audit t))",read=True))
    def save_ledger(self,name,value):base._private(self.work/(name+'-ledger.json'),dict(value,source_tree_sha=SOURCE,binary_sha256=BINARY,synthetic_owned_only=True))
    def row(self,value,id):return next(r for r in value['incidents'] if r['id']==id)
    def grant(self,actor,action,enabled):
        check(actor in ('reader','peer') and action in ('view','change'),'exact synthetic event grant required')
        values=(self.roles[actor],'events',action,'application',self.app)
        if enabled:self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(q(v) for v in values)+');')
        else:self.sql('DELETE FROM v3_role_grants WHERE '+ ' AND '.join(k+'='+q(v) for k,v in zip(('role_id','module','action','scope_kind','scope_id'),values))+';')
    def detail(self,id,index=0,actor='reader'):
        before=self.ledger();row=self.row(before,id)
        value,headers=self.call(actor,'GET','/api/v1/alerts/'+id,index=index,validate=lambda v:verify_detail(v,row,self.source_id,'test'))
        check(base.header(headers,'Cache-Control')=='no-store','detail no-store header missing');verify_unchanged(before,self.ledger());return value
    def reject(self,actor,method,id,body,status,code,**kwargs):
        before=self.ledger();self.call(actor,method,'/api/v1/alerts/'+id+('/claim' if method=='POST' else ''),body,want=status,code=code,**kwargs)
        verify_unchanged(before,self.ledger())
    def claim(self,id,revision,index=0,actor='reader',want=200,code=None,scenario='normal'):
        return self.call(actor,'POST','/api/v1/alerts/'+id+'/claim',{'expected_revision':revision},index=index,want=want,code=code,scenario=scenario)[0]
    def inputs(self):
        self.services_and_ingress();self.app=self.service_rows['test']['application_id']
        for actor in ('reader','peer'):
            for action in ('view','change'):self.grant(actor,action,True)
        self.first,self.fault_target=self.alert_ids
        ledger=self.ledger();check(len(ledger['incidents'])==3 and all(r['source_id']==self.source_id and r['assignee_id']=='' and r['investigation_id']=='' for r in ledger['incidents']),'exact three webhook alerts without assignments/investigation required')
        base._private(self.work/'source-input-proof.json',dict(source_id=self.source_id,token_path=str(self.work/'source-token'),token_value_saved=False,
            operations_sha256=digest(self.work/'operations.json'),source_config_digest=self.config_digest,service_rows=self.service_rows,alert_ids=self.alert_ids,hidden_alert=self.hidden_alert,
            business_origin='POSTservices->BearerPOSTwebhooks202',source_tree_sha=SOURCE,binary_sha256=BINARY))
        self.save_ledger('ingress',ledger);self.marker_case('http_ingress',dict(services=2,authenticated_webhook_alerts=3,SQL_business_writes=0,notifications_not_configured=True,API_generations=2))
    def authorization(self,revision):
        for method in ('GET','POST'):
            body=None if method=='GET' else {'expected_revision':revision}
            self.reject(None,method,self.first,body,401,'unauthorized',scenario='authorization')
            self.reject('denied',method,self.first,body,403,'forbidden',scenario='authorization')
            self.reject('reader',method,self.hidden_alert,body,404,'not_found',scenario='authorization')
            self.reject('hidden',method,self.first,body,404,'not_found',environment='staging',scenario='authorization')
        self.grant('reader','change',False);self.detail(self.first);self.reject('reader','POST',self.first,{'expected_revision':revision},403,'forbidden',scenario='authorization');self.grant('reader','change',True)
        self.grant('reader','view',False)
        for method in ('GET','POST'):self.reject('reader',method,self.first,None if method=='GET' else {'expected_revision':revision},403,'forbidden',scenario='authorization')
        self.grant('reader','view',True);self.marker_case('authorization',dict(anonymous401=True,no_grant403=True,cross_env404=True,view_and_change_independently_required=True))
    def boundaries(self):
        for bad in ('','0','01','4294967296',' 1','-1'):
            self.reject('reader','POST',self.first,{'expected_revision':bad},422,'invalid_revision',scenario='boundary')
        for body in ({'expected_revision':1},{'expected_revision':'1','lease_seconds':5}):
            self.reject('reader','POST',self.first,body,422,'invalid_body',scenario='boundary')
        self.reject('reader','POST',self.first,{},422,'invalid_body',raw_body=b'{',scenario='boundary')
        self.marker_case('revision_boundaries',dict(positive_decimal_uint32=True,only_expected_revision=True,no_lease_or_timeout_contract=True))
    def normal_and_CAS(self):
        before=self.ledger();old=self.row(before,self.first);value=self.claim(self.first,old['revision']);after=self.ledger()
        new=verify_claim(before,after,self.first,self.users['reader'],self.app);verify_detail(value,new,self.source_id,'test');self.save_ledger('claimed',after)
        self.marker_case('claim',dict(assignee=self.users['reader'],audit_delta=1,revision_changed=True,external_action=False))
        repeat=self.claim(self.first,new['revision'],index=1,scenario='idempotency');check(repeat==value,'sameactor currentrevision DTO differs');verify_unchanged(after,self.ledger())
        self.marker_case('current_revision_retry',dict(status=200,API_index=1,audit_delta=0,updated_at_unchanged=True))
        self.reject('reader','POST',self.first,{'expected_revision':old['revision']},412,'claim_revision_changed',scenario='idempotency')
        self.reject('peer','POST',self.first,{'expected_revision':new['revision']},409,'already_claimed_or_closed',scenario='idempotency')
        check(self.detail(self.first,index=1)==value,'peer readback differs');self.marker_case('stale_and_assigned',dict(stale412=True,other_assignee409=True,write_audit_delta=0,original_binary_contract_regression_only=True))
    def install_fault(self):
        self.frozen();self.owner();statement=fault_statements(self.nonce,self.users['reader'],self.app,self.fault_target)[1];self.fault_attempted=True
        base._private(self.work/'claim-fault-intent.json',dict(database=self.database,actor=self.users['reader'],app=self.app,target=self.fault_target,action='alert.claim',outcome='succeeded'))
        self.sql(statement)
    def remove_fault(self,deadline=None):
        if not self.fault_attempted:return
        deadline=min(deadline,time.monotonic()+4) if deadline is not None else time.monotonic()+4
        self.owner(deadline=deadline);name,_,drop=fault_statements(self.nonce,self.users['reader'],self.app,self.fault_target);self.pg(drop,self.database,self.db_user,deadline=deadline)
        query="SELECT json_build_object('trigger',EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='public.management_audit'::regclass AND tgname="+q(name)+"),'function',to_regprocedure("+q('public.'+name+'()')+") IS NOT NULL)"
        check(json.loads(self.pg('BEGIN READ ONLY; '+query+'; COMMIT;',self.database,self.db_user,deadline=deadline))==dict(trigger=False,function=False),'own claim fault remains');self.fault_attempted=False
    def audit_recovery(self):
        before=self.ledger();old=self.row(before,self.fault_target);self.save_ledger('before-fault',before);primary=None
        try:
            self.install_fault();self.claim(self.fault_target,old['revision'],want=503,code='events_unavailable',scenario='failure')
            failed=self.ledger();self.save_ledger('failed',failed);verify_unchanged(before,failed)
            self.detail(self.fault_target,index=1);verify_unchanged(before,self.ledger())
            self.marker_case('audit_rollback',dict(status=503,error_code='events_unavailable',all_rows_xmin_assignee_updated_at_audit_unchanged=True))
        except BaseException as error:primary=error
        finally:
            try:self.remove_fault()
            except BaseException as error:
                if primary is None:primary=error
                else:self.secondary(self.work/'restore-secondary-error.json',error,primary)
        if primary is not None:raise primary
        self.marker_case('fault_removed',dict(trigger=False,function=False))
        value=self.claim(self.fault_target,old['revision'],index=1,scenario='failure');after=self.ledger();self.save_ledger('recovered',after)
        new=verify_claim(before,after,self.fault_target,self.users['reader'],self.app);verify_detail(value,new,self.source_id,'test')
        for index in (0,1):check(self.detail(self.fault_target,index=index)==value,'recovery dualAPI detail differs')
        self.marker_case('audit_recovery',dict(status=200,API_index=1,same_payload_prior_revision=True,audit_delta=1,all_other_rows_unchanged=True,explicit_recovery_not_auto_retry=True))
    def current_authority(self):
        revision=self.row(self.ledger(),self.first)['revision'];self.grant('reader','change',False)
        self.reject('reader','POST',self.first,{'expected_revision':revision},403,'forbidden',scenario='authorization',index=1);self.detail(self.first,index=1)
        self.grant('reader','change',True);before=self.ledger();self.claim(self.first,revision,index=1,scenario='idempotency');verify_unchanged(before,self.ledger())
        self.marker_case('current_authority',dict(same_actor_retry_rechecks_change=True,current_view_read_preserved=True,restore_retry_audit_delta=0))
    def session_revocation(self):
        # Public logout is the existing lifecycle action; no artificial lease,
        # wall-clock adjustment, SQL session expiry, or slow-read simulator.
        revision=self.row(self.ledger(),self.first)['revision'];self.call('reader','POST','/api/v1/auth/logout',want=204,scenario='authorization')
        for index in (0,1):
            self.reject('reader','GET',self.first,None,401,'unauthorized',index=index,scenario='authorization')
            self.reject('reader','POST',self.first,{'expected_revision':revision},401,'unauthorized',index=index,scenario='authorization')
        self.detail(self.first,actor='peer',index=1);self.marker_case('session_revocation',dict(formal_logout=True,old_cookie_twoAPIs401=True,other_actor_still200=True,natural_expiry_not_claimed=True))
    def run(self):
        self.provision();self.start();self.seed()
        for actor in ('admin','reader','peer','denied','hidden'):self.login(actor)
        self.inputs();value=self.detail(self.first);check(self.detail(self.first,index=1)==value,'initial twoAPI detail differs')
        self.marker_case('detail',dict(nonempty_native_alert=True,provider_fields_null=True,source_binding_exact=True,two_APIs=True))
        self.authorization(value['revision']);self.boundaries();self.normal_and_CAS();self.audit_recovery();self.current_authority();self.session_revocation()
        final=self.ledger();self.save_ledger('final',final);claims=[r for r in final['audits'] if r['action']=='alert.claim']
        check(len(claims)==2 and {r['target'] for r in claims}==set(self.alert_ids),'final exact two claim audits differ')
        check(len(final['incidents'])==3 and self.row(final,self.hidden_alert)['assignee_id']=='' and all(r['investigation_id']=='' for r in final['incidents']),'unrelated hidden alert/investigation mutated')
        self.marker_case('ledger',dict(native_alerts=3,claimed=2,claim_audits=2,source_notifications_none=True,no_investigation=True))
        self.frozen();result=dict(run_id=self.run_id,source_tree_sha=SOURCE,binary_sha256=BINARY,binary_platform='darwin/arm64 Mach-O',runtime_sha=RUNTIME,helper_sha256=self.data['helper_sha256'],
            required_cases=REQUIRED_CASES+READY_CASES,http_calls=self.calls,normal_added=0,counts_as_whole_acceptance=False,
            unverified=['natural expiry/inflight grant/session waiting races','real organizational identities','notification delivery/AM execution','all status/team ACL combinations','pagination/performance/full chain'])
        base._private(self.work/'alert-claim-result.json',result);return result
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
            with socket.socket() as sock:check(sock.connect_ex(('127.0.0.1',port))!=0,'owned alert API port remains occupied')
        emit('alert_claim.cleanup','passed',dict(single_cleanup_seconds=40,restore_within4=True,owned_UUID_only=True,ports_free=list(PORTS),held_Popen_generations_only=True))
_source=textwrap.dedent(inspect.getsource(base.Chain.cleanup));check(_source.count('deadline = time.monotonic() + 40')==1,'original cleanup drift')
_ns=dict(base.__dict__);exec(compile(_source.replace('deadline = time.monotonic() + 40','deadline = self.cleanup_deadline'),str(Path(__file__))+'[shared fault/PG deadline]','exec'),_ns);bounded_cleanup=_ns['cleanup']
def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT);parser.add_argument('--inventory',action='store_true');parser.add_argument('--live',action='store_true');parser.add_argument('--run-id');parser.add_argument('--admission',type=Path);args=parser.parse_args(argv)
    if args.inventory:Contract(ROOT);print(json.dumps(dict(status='prepared-only',module=MODULE,ports=PORTS,targets=TARGETS,required_cases=REQUIRED_CASES+READY_CASES,normal_added=0)));return 0
    if not(args.live and args.run_id and args.admission):parser.error('explicit root --live/run-id/admission required')
    check(args.root.resolve()==ROOT,'reviewed checkout required');fd=None
    try:
        admission(args.admission,args.run_id);fd=os.open(ROOT/'.local/testlab/api-management.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
        st=os.fstat(fd);check(stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o600,'private management lease required');fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
        with base.cooperative_signals():base.execute(Chain(ROOT,support.doctor(ROOT),args.run_id,args.admission))
        return 0
    except Blocked as error:emit('alert_claim.environment','blocked',dict(error=str(error),first_failure_preserved=True));return 2
    except Exception as error:emit('alert_claim.failure','failed',dict(error_type=type(error).__name__,first_failure_preserved=True));return 1
    finally:
        if fd is not None:os.close(fd)
if __name__=='__main__':raise SystemExit(main())
