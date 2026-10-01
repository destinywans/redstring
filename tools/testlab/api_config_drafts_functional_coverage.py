"""Configuration draft candidate; only offline verification completed so far.

Reuses promotion_chain ownership and cleanup. Only explicit --live can allocate
invocation-owned resources after fresh promotion ownership/lease guards. Native draft/MR data must come from
HTTP, never SQL. Seven-day synthetic history is protocol_mock evidence only.
"""
from __future__ import annotations
import argparse,contextlib,hashlib,http.client,json,os,re,socket,stat,subprocess,time,uuid
from urllib.parse import parse_qs
import api_rules_read_functional_coverage as rules
import api_notification_routes_functional_coverage as routes
from pathlib import Path
from urllib.parse import urlsplit
import api_readonly_chain as ro
import promotion_chain as promotion
ROOT=ro.ROOT;MODULE='formal-api-config-drafts-functional'
PORTS={'source':(18706,18707)}
GITLAB_PROXY_PORT=18710;SOURCE_PORTS=(18711,18712)
TARGETS=(('POST','/alert-rules/import'),('PUT','/alert-rules/{id}'),('POST','/inhibitions/preview'),('POST','/inhibitions'),('POST','/notification-routes'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('GET','/notification-routes'),('GET','/inhibitions'))
REQUIRED_CASES=('drafts.rule_import','drafts.rule_update','drafts.inhibition_preview','drafts.inhibition_create','drafts.notification_create','drafts.exact_mr','drafts.idempotency','drafts.cas','drafts.authorization','drafts.audit_consistency','drafts.audit_rollback','drafts.response_loss','drafts.remote_local_recovery','drafts.cleanup')
Blocked,check=ro.Blocked,ro.check
SOURCE='6ad893bbb853bee78ae1c8622220e963745826ffec34409cb68c0f87ffdf7437'
BINARY='bfeb9b985ee4ab07c3008cecd5409c611ff58fed928beba563f082071d6d208c'

def emit(name,status,actual,**fields):
    mode=fields.pop('evidence_mode','real_local')
    print(json.dumps(dict(name=name,status=status,actual=actual,module=MODULE,evidence_mode=mode,**fields),ensure_ascii=False),flush=True)

def hashes():return {n:hashlib.sha256((ROOT/'tools/testlab'/n).read_bytes()).hexdigest() for n in ('api_config_drafts_functional_coverage.py','promotion_chain.py','api_readonly_chain.py','api_rules_read_functional_coverage.py','api_notification_routes_functional_coverage.py')}

class Contract(ro.Contract):
    def canonical(self,method,path):
        out=super().canonical(method,path)
        if (method,out[0][len('/api/v1'):]) not in (*TARGETS,*SUPPORT):raise Blocked('outside bounded draft workflow contract')
        return out

@contextlib.contextmanager
def ports():
    old=promotion.PORTS,promotion.GITLAB_PROXY_PORT,promotion.GITLAB_PROXY_ORIGIN,rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT,routes.AM_PORT,routes.CAPTURE_PORT
    promotion.PORTS,promotion.GITLAB_PROXY_PORT,promotion.GITLAB_PROXY_ORIGIN=PORTS,GITLAB_PROXY_PORT,'https://localhost:'+str(GITLAB_PROXY_PORT)
    rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT,routes.AM_PORT,routes.CAPTURE_PORT=18711,18708,18709,18712,18708
    try:yield
    finally:(promotion.PORTS,promotion.GITLAB_PROXY_PORT,promotion.GITLAB_PROXY_ORIGIN,rules.PROM_PORT,rules.EXPORT_PORT,rules.RELAY_PORT,routes.AM_PORT,routes.CAPTURE_PORT)=old

def branches(nonce):
    check(bool(re.fullmatch('[a-f0-9]{32}',nonce)),'run nonce required')
    return {kind:{'target':kind+'-'+nonce[:12],'prefix':'aiops-alert-rule-drafts/' if kind=='rules' else 'aiops-'+kind+'-drafts/'} for kind in ('rules','routes','inhibitions')}

def draft_spec(app,env,nonce,expr,group):
    check(bool(re.fullmatch('[a-f0-9]{32}',app)) and bool(re.fullmatch('[a-f0-9]{32}',nonce)),'native application and run nonce required')
    check(env=='test' and isinstance(expr,str) and 0<len(expr)<=8192 and isinstance(group,str) and bool(group),'bounded native source binding required')
    return dict(application_id=app,environment_id=env,group=group,name='Draft'+nonce,expr=expr,**{'for':'0s'},labels={'application':app,'environment':env,'severity':'P3'},annotations={'summary':'Owned draft '+nonce},mode='draft')

def import_input(spec):
    # JSON is valid YAML; no hand-written quoting or user interpolation.
    payload={'groups':[{'name':spec['group'],'rules':[{'alert':spec['name'],'expr':spec['expr'],'for':spec['for'],'labels':spec['labels'],'annotations':spec['annotations']}]}]}
    return dict(application_id=spec['application_id'],yaml=json.dumps(payload,ensure_ascii=False),mode='draft')

def inhibition_input(app):return dict(application_id=app,source=[{'k':'severity','op':'=','v':'P1'}],target=[{'k':'severity','op':'=','v':'P3'}],equal=['instance'])

def route_input(app,nonce,receiver):
    check(bool(re.fullmatch('receiver-[1-9][0-9]{0,3}',receiver)),'native receiver reference required')
    return dict(application_id=app,name='Owned '+nonce,matchers=[{'k':'severity','op':'=','v':'P3'}],receiver_ref=receiver,group_by=['alertname','instance'],group_wait='30s',group_interval='5m',repeat_interval='4h',**{'continue':False})

def etag(value):
    check(isinstance(value,str) and bool(re.fullmatch('"sha256:[a-f0-9]{64}"',value)),'exact source ETag must remain quoted')
    return value

def mr_identity(value,project_id):
    check(isinstance(value,dict) and type(project_id) is int and project_id>0,'exact owned GitLab project required')
    # Rule DTO serializes project_id as string, route DTO as integer.
    pid=value.get('project_id');iid=value.get('iid',value.get('merge_request_iid'))
    check((pid==str(project_id) or type(pid) is int and pid==project_id) and type(iid) is int and iid>0,'native MR identity escaped owned project')
    u=urlsplit(value.get('url',''))
    check(u.scheme=='https' and u.hostname=='localhost' and u.port==18580 and not u.username and not u.password and not u.query and not u.fragment and u.path.endswith('/-/merge_requests/'+str(iid)),'native MR URL is not owned GitLab exact IID')
    return iid

def verify_mr(mr,project_id,iid,target,prefix):
    check(isinstance(mr,dict) and mr.get('project_id')==project_id and mr.get('source_project_id')==project_id and mr.get('target_project_id')==project_id and mr.get('iid')==iid,'GitLab exact MR/project readback mismatch')
    check(mr.get('state')=='opened' and mr.get('merged_at') is None and mr.get('target_branch')==target and isinstance(mr.get('source_branch'),str) and mr['source_branch'].startswith(prefix) and mr['source_branch']!=target,'draft MR merged or branch scope differs')

def imported_draft(value,spec,project_id):
    check(isinstance(value,dict) and value.get('status')=='pending_review','rule import must create reviewed draft')
    rows=value.get('candidates');check(isinstance(rows,list) and len(rows)==1,'one genuinely new import candidate required')
    row=rows[0];check(row.get('name')==spec['name'] and row.get('group')==spec['group'] and row.get('outcome')=='new' and row.get('version')==1 and bool(re.fullmatch('[a-f0-9]{32}',row.get('draft_id',''))),'native import draft reference missing')
    iid=mr_identity(value.get('mr'),project_id)
    return row['draft_id'],iid

def updated_draft(value,draft,expected_version,project_id):
    check(isinstance(value,dict) and value.get('draft_id')==draft and value.get('status')=='pending_review' and type(value.get('version')) is int and value['version']==expected_version+1,'native CAS draft update differs')
    return mr_identity(value.get('mr'),project_id)

def config_draft(value,revision,project_id):
    check(isinstance(value,dict) and bool(re.fullmatch('[a-f0-9]{32}',value.get('id',''))) and value.get('status')=='pending_review' and value.get('source_revision')==etag(revision)[1:-1],'config draft identity/status/ETag differ')
    check(value.get('status_basis')=='local_submission' and value.get('effect')=='no_runtime_change_by_this_endpoint' and value.get('current_mr_state') is None and value.get('runtime_state') is None,'draft invented active runtime state')
    return mr_identity(value.get('review'),project_id)

def preview(value,app,revision,expected_count,mode):
    check(mode in ('real_local','protocol_mock'),'explicit history evidence mode required')
    check(isinstance(value,dict) and value.get('environment_id')=='test' and value.get('application_id')==app and value.get('config_revision')==etag(revision)[1:-1] and value.get('basis')=='complete_history_simulation' and value.get('history_complete') is True,'preview scope/source/history DTO differs')
    check(type(value.get('potentially_suppressed')) is int and value['potentially_suppressed']==expected_count and isinstance(value.get('sample'),list) and value.get('actual_inhibition_hits') is None,'preview misreported actual hits or simulation result')
    from datetime import datetime
    at=lambda x:datetime.fromisoformat(x.replace('Z','+00:00'))
    check((at(value['window_end'])-at(value['window_start'])).total_seconds()==604800,'seven-day preview window was shortened')

class DraftTLSProxy(promotion.TLSProxy):
    """Own source relay; a single native rule update may lose its HTTP200."""
    def arm_drop(self,method,path):
        draft=getattr(self.chain,'native_rule_draft',None)
        proc=self.chain.processes.get('source')
        if (method!='PUT' or self.tier!='source' or self.chain.proxies.get('source') is not self
                or not isinstance(draft,str) or not re.fullmatch('[a-f0-9]{32}',draft)
                or path!='/api/v1/alert-rules/'+draft or proc is None or proc.poll() is not None):
            raise Blocked('draft response loss requires this held source and exact native rule draft PUT')
        with self.lock:
            if self.drop is not None:raise Blocked('draft response loss already armed')
            self.drop=(method,path);self.drop_receipt=None

class Workflow:
    """Protocol and business validators; lifecycle remains promotion.Chain.

    Sources.prepare composes owned Prom/AM, native application and a explicitly
    synthetic history provider. It restarts only a held API and supplies current
    source ETags/receiver references; the history route stays protocol_mock.
    """
    def __init__(self,chain,source_bootstrap):
        self.chain=chain;self.bootstrap=source_bootstrap;self.contract=Contract(chain.root);self.calls=0
        if type(getattr(chain,'workflow_calls',None)) is not int:chain.workflow_calls=0
    def call(self,method,path,body=None,*,want=200,key=None,revision=None,scenario='normal',mode='real_local',validate=None,authenticated=True,environment='test',cookie=None,error_code=None):
        self.chain.frozen();canonical,_=self.contract.canonical(method,path)
        check(mode in ('real_local','protocol_mock') and scenario in ro.WIRE_SCENARIOS,'explicit consistent evidence mode/scenario required')
        check(scenario!='normal' or validate is not None,'normal success requires business validator')
        headers={'Origin':'https://localhost:'+str(PORTS['source'][0]),'X-AIOps-Environment':environment,'Content-Type':'application/json'}
        if authenticated:headers['Cookie']=cookie if cookie is not None else self.chain.cookies['source']
        if key is not None:headers['Idempotency-Key']=key
        if revision is not None:headers['If-Match']=etag(revision)
        status,raw,response_headers=promotion.transport(headers['Origin'],method,path,None if body is None else json.dumps(body).encode(),headers,self.chain.work/'entry.crt')
        self.last_headers=response_headers;self.calls+=1;self.chain.workflow_calls+=1;number=self.chain.workflow_calls
        value=json.loads(raw) if raw else {};passed=False
        ro._private(self.chain.work/('draft-http-'+str(number)+'.json'),{'method':method,'path':path,'canonical_path':canonical,
            'request_body':body,'environment':environment,'idempotency_key':key,'source_revision':revision,
            'status':status,'response':value,'response_headers':{k:v for k,v in response_headers.items() if k.lower() in ('etag','content-type')},
            'scenario':scenario,'evidence_mode':mode,'upstream_evidence_mode':mode})
        try:
            check(status==want,'formal draft HTTP status mismatch')
            check(error_code is None or value.get('error',{}).get('code')==error_code,'exact draft error code mismatch')
            if 200<=status<300:
                self.contract.shape(method,path,status,value)
                if validate:validate(value)
            passed=True
        finally:
            row={'method':method,'path':canonical,'scenario':scenario,'status':'passed' if passed else 'failed','expected':{'status':[want]},'actual':{'status':status},'registration_evidence':passed and scenario=='normal' and 200<=status<300,'evidence_mode':mode,'upstream_evidence_mode':mode}
            if not passed:promotion.preserve_first_failure(self.chain.work,row)
            emit('drafts.http.'+str(number),row['status'],row['actual'],interface=dict(promotion.actual_interface(row),evidence_mode=mode,upstream_evidence_mode=mode),evidence_mode=mode)
        return value
    def run(self):
        if self.bootstrap is None:raise Blocked('source composition pending: own Prom/AM, native app, source ETag/receiver and honest seven-day history; no resources allocated')
        self.chain.setup();source=self.bootstrap(self.chain)
        app=source['application_id'];spec=draft_spec(app,'test',self.chain.run_id,source['expr'],source['group']);pid=self.chain.projects['main']['id'];branch=branches(self.chain.run_id);mode=source['source_evidence_mode']
        v=self.call('POST','/api/v1/alert-rules/import',import_input(spec),want=201,key='import-'+self.chain.run_id,mode=mode,validate=lambda x:imported_draft(x,spec,pid));draft,iid=imported_draft(v,spec,pid);import_response=v;import_body=import_input(spec);self.mark('drafts.rule_import',{'draft':draft,'version':1,'real_mr_iid':iid})
        self.chain.native_rule_draft=draft
        self.read_mr(pid,iid,branch['rules'])
        # Newly imported draft always starts at native v1; never SQL-seed it.
        body={'expected_version':1,'rule':dict(spec,annotations={'summary':'Owned updated '+self.chain.run_id})}
        v=self.call('PUT','/api/v1/alert-rules/'+draft,body,key='update-'+self.chain.run_id,mode=mode,validate=lambda x:updated_draft(x,draft,1,pid));self.read_mr(pid,updated_draft(v,draft,1,pid),branch['rules']);update_body=body;self.mark('drafts.rule_update',{'draft':draft,'version':2,'real_mr_iid':updated_draft(v,draft,1,pid)})
        self.call('POST','/api/v1/inhibitions/preview',inhibition_input(app),mode=source['history_evidence_mode'],validate=lambda x:preview(x,app,source['inhibition_etag'],source['suppressed_count'],source['history_evidence_mode']));self.mark('drafts.inhibition_preview',{'source':'synthetic complete seven-day history','actual_hits_proven':False},source['history_evidence_mode'])
        for path,body,revision,kind in [('/api/v1/inhibitions',inhibition_input(app),source['inhibition_etag'],'inhibitions'),('/api/v1/notification-routes',route_input(app,self.chain.run_id,source['receiver_ref']),source['route_etag'],'routes')]:
            v=self.call('POST',path,body,want=202,key=kind+'-'+self.chain.run_id,revision=revision,mode=mode,validate=lambda x:config_draft(x,revision,pid));self.read_mr(pid,config_draft(v,revision,pid),branch[kind]);self.mark('drafts.inhibition_create' if kind=='inhibitions' else 'drafts.notification_create',{'draft_id':v['id'],'real_mr_iid':config_draft(v,revision,pid),'runtime_change':False})
        self.mark('drafts.exact_mr',{'four_opened_native_draft_mrs':True,'merged':False});self.faults(spec,draft,import_body,import_response,update_body,source,pid)
        return {'normal_routes':5,'real_local_routes':4,'protocol_mock_routes':1,'required_fault_regressions_complete':True,'continuous_history_proven':False}
    def read_mr(self,pid,iid,branch):
        self.chain.assert_project('main');mr,_=self.chain.git('GET',f'/api/v4/projects/{pid}/merge_requests/{iid}')
        ro._private(self.chain.work/('native-mr-'+str(iid)+'.json'),{'project_id':pid,'iid':iid,'expected_branch':branch,'actual':mr})
        verify_mr(mr,pid,iid,branch['target'],branch['prefix'])

    def mark(self,name,value,mode='real_local'):
        self.chain.frozen();emit(name,'passed',dict(run_id=self.chain.run_id,**value),evidence_mode=mode)
    def snapshot(self):
        c=self.chain;sql="SELECT json_build_object('rules',(SELECT count(*) FROM v3_alert_rule_drafts),'operations',(SELECT count(*) FROM v3_alert_rule_draft_operations),'revisions',(SELECT count(*) FROM v3_alert_rule_draft_revisions),'inhibitions',(SELECT count(*) FROM v3_inhibition_drafts),'routes',(SELECT count(*) FROM v3_notification_route_drafts),'audit',(SELECT count(*) FROM management_audit),'pending_audit',(SELECT count(*) FROM management_audit WHERE action='v3.alert_rule_draft.pending_review'))"
        data=json.loads(c.pg(c.databases['source'],sql,read=True));pid=c.projects['main']['id'];mrs,headers=c.git('GET',f'/api/v4/projects/{pid}/merge_requests?per_page=100&page=1');check(isinstance(mrs,list) and headers.get('X-Next-Page',headers.get('x-next-page',''))=='','owned MR inventory incomplete')
        check(all(m.get('source_project_id')==pid and m.get('target_project_id')==pid and m.get('state')=='opened' and m.get('merged_at') is None for m in mrs),'own draft MR escaped project or merged');data['mrs']=len(mrs);return data
    def faults(self,spec,draft,import_body,import_response,update_body,source,pid):
        c=self.chain;path='/api/v1/alert-rules/'+draft;before=self.snapshot()
        again=self.call('POST','/api/v1/alert-rules/import',import_body,want=201,key='import-'+c.run_id,scenario='idempotency',validate=lambda x:imported_draft(x,spec,pid));check(again==import_response and self.snapshot()==before,'import replay duplicated audit/ledger/MR')
        self.call('PUT',path,update_body,key='update-'+c.run_id,scenario='idempotency',validate=lambda x:updated_draft(x,draft,1,pid));check(self.snapshot()==before,'update replay duplicated ledger/MR')
        self.call('PUT',path,dict(update_body,expected_version=1),key='cas-'+c.run_id,want=409,scenario='boundary',error_code='draft_version_conflict');check(self.snapshot()==before,'stale CAS changed ledger/audit/MR')
        self.mark('drafts.idempotency',{'unchanged':before,'exact_native_import_response':True});self.mark('drafts.cas',{'original_version_rejected':409})
        # Product response is lost after native local commit and actual GitLab MR.
        lost_body={'expected_version':2,'rule':dict(spec,annotations={'summary':'Owned response loss '+c.run_id})};key='loss-'+c.run_id
        proxy=c.proxies['source'];proxy.arm_drop('PUT',path)
        try:self.call('PUT',path,lost_body,key=key,scenario='failure',validate=lambda x:updated_draft(x,draft,2,pid))
        except (OSError,http.client.HTTPException):pass
        else:raise AssertionError('armed native successful response was not lost')
        check(proxy.drop_receipt and proxy.drop_receipt['upstream_status']==200 and proxy.drop_receipt['path']==path,'loss occurred before actual native success')
        row=self.snapshot();check(row['mrs']==before['mrs']+1 and row['operations']==before['operations']+1 and row['revisions']==before['revisions']+1 and row['pending_audit']==before['pending_audit']+1,'lost response native commit/MR not exact')
        v=self.call('PUT',path,lost_body,key=key,scenario='idempotency',validate=lambda x:updated_draft(x,draft,2,pid));check(self.snapshot()==row,'response-loss replay duplicated native state or MR');self.read_mr(pid,updated_draft(v,draft,2,pid),branches(c.run_id)['rules']);ro._private(c.work/'draft-response-loss.json',proxy.drop_receipt)
        self.mark('drafts.response_loss',{'real_upstream_status':200,'final_native_version':3,'one_mr':True,'one_pending_audit':True})
        # Exact own DB fault, before upstream work; no fake committed results.
        fault_spec=dict(spec,name='Fault'+c.run_id);body=import_input(fault_spec);key='audit-'+c.run_id
        action='v3.alert_rule_draft.reserved';self.audit_trigger(action);before=self.snapshot()
        try:self.call('POST','/api/v1/alert-rules/import',body,want=503,key=key,scenario='failure',error_code='alert_rule_draft_unavailable')
        finally:self.drop_audit_trigger()
        check(self.snapshot()==before,'reservation audit failure committed partial local state or contacted GitLab')
        self.mark('drafts.audit_rollback',{'before_upstream_failure_unchanged':True})
        # Remote MR exists but final local audit/commit fails. Preserve first state,
        # allow the actual one-minute lease to expire, then make one explicit retry.
        self.audit_trigger('v3.alert_rule_draft.pending_review')
        try:self.call('POST','/api/v1/alert-rules/import',body,want=503,key=key,scenario='failure',error_code='alert_rule_draft_unavailable')
        finally:self.drop_audit_trigger()
        failed=self.snapshot();check(failed['mrs']==before['mrs']+1 and failed['operations']==before['operations']+1 and failed['pending_audit']==before['pending_audit'],'remote success/local audit failure state was washed green')
        state=json.loads(c.pg(c.databases['source'],"SELECT json_build_object('state',state,'seconds_until_lease',greatest(extract(epoch FROM lease_until-clock_timestamp()),0)) FROM v3_alert_rule_draft_operations WHERE idempotency_key="+promotion.quote(key),read=True));check(state['state']=='exporting' and 0<=state['seconds_until_lease']<=60,'failed final commit lease/state differs')
        ro._private(c.work/'first-remote-success-local-failure.json',{'native_state':state,'before':before,'after':failed,'real_source_sha':c.backend['source_tree_sha']})
        start=time.monotonic();deadline=start+float(state['seconds_until_lease'])+.2
        while time.monotonic()<deadline:time.sleep(min(1,deadline-time.monotonic()))
        v=self.call('POST','/api/v1/alert-rules/import',body,want=201,key=key,scenario='idempotency',validate=lambda x:imported_draft(x,fault_spec,pid));after=self.snapshot();check(after['mrs']==failed['mrs'] and after['operations']==failed['operations'] and after['pending_audit']==before['pending_audit']+1,'natural-lease retry duplicated MR or did not commit exact audit')
        self.read_mr(pid,imported_draft(v,fault_spec,pid)[1],branches(c.run_id)['rules']);self.mark('drafts.remote_local_recovery',{'lease_wait_seconds':time.monotonic()-start,'same_mr':True,'one_final_pending_audit':True,'clock_or_ttl_changed':False})
        self.authorization(spec,draft,source)
        rows=json.loads(c.pg(c.databases['source'],"SELECT coalesce(json_agg(json_build_object('action',action,'target',target,'outcome',outcome,'application',application_id,'changes',changes)),'[]') FROM management_audit",read=True));pending=[r for r in rows if r['action']=='v3.alert_rule_draft.pending_review'];check(len(pending)==4 and all(r['application']==spec['application_id'] and r['outcome']=='success' and r['changes'].get('project_id')==pid for r in pending),'native final rule audit count/application/project differs')
        check(c.sources.capture_count==0,'draft endpoint sent actual local notification');ro._private(c.work/'draft-native-audits.json',rows);self.mark('drafts.audit_consistency',{'final_rule_pending_audits':4,'opened_mrs':self.snapshot()['mrs'],'no_notification':True,'merge_activation_calls':0})
    def audit_trigger(self,action):
        c=self.chain;c.assert_database('source');c.pg(c.databases['source'],"CREATE FUNCTION testlab_draft_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action="+promotion.quote(action)+" THEN RAISE EXCEPTION 'owned testlab audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER testlab_draft_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION testlab_draft_audit();")
    def drop_audit_trigger(self):
        c=self.chain;c.assert_database('source');c.pg(c.databases['source'],'DROP TRIGGER testlab_draft_audit ON management_audit; DROP FUNCTION testlab_draft_audit();')
    def authorization(self,spec,draft,source):
        c=self.chain;before=self.snapshot();requests=[('POST','/api/v1/alert-rules/import',import_input(spec)),('PUT','/api/v1/alert-rules/'+draft,{'expected_version':3,'rule':spec}),('POST','/api/v1/inhibitions/preview',inhibition_input(spec['application_id'])),('POST','/api/v1/inhibitions',inhibition_input(spec['application_id'])),('POST','/api/v1/notification-routes',route_input(spec['application_id'],c.run_id,source['receiver_ref']))]
        metadata=[]
        for index,(method,path,body) in enumerate(requests):
            meta={} if path.endswith('/preview') else {'key':'authorization-'+c.run_id+'-'+str(index)}
            if path=='/api/v1/inhibitions':meta['revision']=source['inhibition_etag']
            if path=='/api/v1/notification-routes':meta['revision']=source['route_etag']
            metadata.append(meta)
            self.call(method,path,body,want=401,authenticated=False,scenario='authorization',**meta)
            self.call(method,path,body,want=403,cookie=c.denied_cookie,scenario='authorization',**meta)
            self.call(method,path,body,want=422,environment='staging',scenario='authorization',error_code='invalid_environment',**meta)
        check(self.snapshot()==before,'rejected authorization changed native/MR state')
        c.pg(c.databases['source'],'DELETE FROM v3_role_grants WHERE role_id='+promotion.quote(c.reader_role)+" AND module IN ('alert_rules','silences');")
        for (method,path,body),meta in zip(requests,metadata):self.call(method,path,body,want=403,scenario='authorization',**meta)
        check(self.snapshot()==before,'live revoked grants changed native/MR state');self.mark('drafts.authorization',{'anonymous401':5,'no_grant403':5,'invalid_env422':5,'live_revoked403':5,'native_and_MR_unchanged':True})

class Sources:
    """Reuse official rule and AM source helpers; keep their containers own."""
    def __init__(self,chain):
        self.chain=chain;self.root=chain.root;self.work=chain.work;self.run_id=chain.run_id;self.nonce=chain.run_id
        self.prom_name='aiops-'+ro.OWNER+'-draft-prom-'+self.nonce;self.prom_cid=None;self.prom_attempted=False
        self.am_name='aiops-'+ro.OWNER+'-draft-am-'+self.nonce;self.am_cid=None;self.am_attempted=False;self.source=None
        self.state=chain.state;self.config_hashes={};self.capture_count=0;self.history_requests=[];self.load_binding=None
        self.am_args=['--config.file='+routes.AM_CONFIG,'--storage.path=/alertmanager','--cluster.listen-address=','--web.listen-address=0.0.0.0:9093']
        self.tmpfs='rw,noexec,nosuid,size=16777216,uid='+str(os.getuid())+',gid='+str(os.getgid())+',mode=0700'
        self.receiver_names=['owned-'+self.nonce+'-'+s for s in ('default','critical','app','staging')]
    def guard_prom(self):return rules.guard_owned_prom(ro.inspect_pg(self.prom_name),self)
    def guard_am(self):return routes.guard_am(ro.inspect_pg(self.am_name),self)
    def marker_case(self,name,value):emit('drafts.'+name,'passed',value)
    receiver_proof=routes.Chain.receiver_proof
    start_am=routes.Chain.start_am
    def pin(self,*names):
        for n in names:self.config_hashes[n]=hashlib.sha256((self.work/n).read_bytes()).hexdigest()
    def prepare(self):
        c=self.chain;key='draft-app-'+self.nonce
        c.call('source','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201)
        app=json.loads(c.pg(c.databases['source'],"SELECT json_build_object('id',a.id,'env',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key="+promotion.quote(key)+" AND e.key='test'",read=True))
        self.app=app['id'];self.apps={env:{'app':self.app,'key':key,'env_id':app['env']} for env in ('test','staging')}
        rule=rules.rule_config(self.app,self.nonce);self.expr=rule['groups'][0]['rules'][0]['expr'];self.source_id='draft-history-'+self.nonce[:20];self.source_token=ro.secrets.token_urlsafe(32)
        ro.private_file(self.work/'history-token',self.source_token)
        for n,v in [('rules.json',rule),('prometheus.json',rules.prometheus_config()),('am-config.json',routes.config_for(self))]:ro._private(self.work/n,v)
        routes.validate_config(routes.config_for(self),self)
        self.source=rules.Source(self)
        self.install_handlers()
        # Use the read helper's exact argv/mount identity; no shared Prom rules.
        args=['docker','run','--detach','--pull','never','--name',self.prom_name,'--label','aiops.testlab.owner='+ro.OWNER,'--label','aiops.testlab.run='+self.run_id,'--label','aiops.testlab.purpose=rule-read','--cpus','1','--memory','512m','--pids-limit','128','--user',str(os.getuid())+':'+str(os.getgid()),'--cap-drop','ALL','--security-opt','no-new-privileges','--read-only','--tmpfs','/prometheus:rw,noexec,nosuid,size=128m,mode=1777','--publish','127.0.0.1:18711:9090']
        for name,destination in [('prometheus.json','/etc/prometheus/prometheus.yml'),('rules.json',rules.FILE)]:args+=['--mount','type=bind,src='+str(self.work/name)+',dst='+destination+',readonly']
        args+=[rules.PROM_IMAGE_ID,'--config.file=/etc/prometheus/prometheus.yml','--storage.tsdb.retention.time=2h','--storage.tsdb.path=/prometheus']
        absent=subprocess.run(['docker','container','inspect',self.prom_name],capture_output=True,text=True,timeout=5)
        check(absent.returncode!=0 and ('No such container' in absent.stderr or 'No such object' in absent.stderr),'fresh Prom container name not absent')
        self.prom_attempted=True;self.prom_cid=c.command(args,timeout=30).strip();self.guard_prom();c.state['draft_prom_cid']=self.prom_cid;c.save()
        deadline=time.monotonic()+90
        while time.monotonic()<deadline:
            try:
                status,raw=rules.direct_get('/api/v1/rules?type=alert');rules.validate_observed(json.loads(raw),self.app,self.nonce);check(status==200,'rule source status');break
            except (OSError,ValueError,AssertionError,http.client.HTTPException):time.sleep(.5)
        else:raise Blocked('owned natural Prom rule readiness deadline; no synthetic source fallback')
        self.start_am()
        bindings={'environments':{'test':{'url':'http://127.0.0.1:18709','allow_http':True}},'bindings':[{'environment_id':'test','application_id':self.app,'file':rules.FILE,'group':rules.GROUP}]}
        operations={'sources':[{'id':self.source_id,'token_file':str(self.work/'history-token'),'mappings':[{'labels':{'application':key,'environment':'test'},'application_id':self.app,'environment_id':app['env']}]}],'feishu_channels':[]}
        am={'environments':{'test':{'url':'http://127.0.0.1:18712','allow_http':True,'environment_label':'environment','application_bindings':{key:self.app},'source_ids':[self.source_id]}}}
        history={'environments':{'test':{'url':'http://127.0.0.1:18708','allow_http':True,'source_id':self.source_id,'bearer_token_file':str(self.work/'history-token')}}}
        for name,value in [('draft-rules.json',bindings),('draft-operations.json',operations),('draft-am.json',am),('draft-history.json',history)]:ro._private(self.work/name,value)
        self.pin('draft-rules.json','draft-operations.json','draft-am.json','draft-history.json','history-token','rules.json','prometheus.json','am-config.json')
        self.create_branches()
        c.extras={'AIOPS_V3_ALERT_RULES_CONFIG':str(self.work/'draft-rules.json'),'AIOPS_OPERATIONS_CONFIG':str(self.work/'draft-operations.json'),'AIOPS_V3_ALERTMANAGER_CONFIG':str(self.work/'draft-am.json'),'AIOPS_V3_INHIBITION_HISTORY_CONFIG':str(self.work/'draft-history.json')}
        for kind,vars_ in [('rules',('ALERT_RULE_DRAFT','ALERT_RULE_DRAFT')),('routes',('NOTIFICATION_ROUTE','NOTIFICATION_ROUTE')),('inhibitions',('INHIBITION','INHIBITION'))]:
            b=branches(self.nonce)[kind];c.extras['AIOPS_V3_'+vars_[0]+'_TARGET_BRANCH']=b['target'];c.extras['AIOPS_V3_'+vars_[1]+'_BRANCH_PREFIX']=b['prefix']
        c.stop('source');c.start('source');c.login('source')
        revision,receiver=self.read_current_tree()
        emit('drafts.source_ready','passed',{'application':self.app,'real_prom_cid':self.prom_cid,'real_am_cid':self.am_cid,'revision':self.revision,'receivers_only_owned_capture':True,'history':'synthetic seven-day protocol Mock only'})
        return {'application_id':self.app,'expr':self.expr,'group':rules.GROUP,'source_evidence_mode':'real_local','history_evidence_mode':'protocol_mock','suppressed_count':1,'inhibition_etag':revision,'route_etag':revision,'receiver_ref':receiver}
    def read_current_tree(self):
        # The shared tree requires explicit environment business grants. Prepare
        # its scoped reader before the first upstream read, after API restart.
        c=self.chain;c.seed_identities();c.workflow=Workflow(c,None)
        tree=c.workflow.call('GET','/api/v1/notification-routes',validate=lambda v:check(isinstance(v.get('root'),dict),'real route tree missing'))
        revision='"'+self.revision+'"'
        check(next((v for k,v in c.workflow.last_headers.items() if k.lower()=='etag'),'')==revision,'native current route ETag differs from actual loaded AM')
        return revision,self.find_receiver(tree)
    def find_receiver(self,value):
        # Reader returns structural receiver references, never receiver names.
        def walk(node):
            found=[]
            if isinstance(node,dict):
                receiver=node.get('receiver_ref') or node.get('receiver')
                refs=node.get('receiver_refs',[])
                found.extend(r for r in refs if isinstance(r,str) and re.fullmatch('receiver-[1-9][0-9]{0,3}',r))
                if isinstance(receiver,str) and re.fullmatch('receiver-[1-9][0-9]{0,3}',receiver):found.append(receiver)
                for v in node.values():found.extend(walk(v))
            elif isinstance(node,list):
                for v in node:found.extend(walk(v))
            return found
        refs=walk(value);check(bool(refs),'native receiver reference absent');return refs[0]
    def install_handlers(self):
        source=self;original=self.source.exporter.server.RequestHandlerClass
        class Exporter(original):
            def do_GET(self):
                if urlsplit(self.path).path!='/api/v1/inhibition-history':return super().do_GET()
                check(self.headers.get('Authorization')=='Bearer '+source.source_token,'history token differs');query=parse_qs(urlsplit(self.path).query)
                payload=history_payload(query,source.app,source.apps['test']['key'],source.source_id);source.history_requests.append({'path':self.path,'evidence_mode':'protocol_mock'});ro.append_json(source.work/'synthetic-history-reads.jsonl',{'request_path':self.path,'evidence_mode':'protocol_mock','data':'synthetic complete seven-day occurrences, not continuous collection','response':payload})
                body=json.dumps(payload).encode();self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            def do_POST(self):
                check(self.path=='/capture/'+source.nonce,'non-owned capture destination');self.rfile.read(int(self.headers.get('Content-Length','0')));source.capture_count+=1;self.send_response(200);self.send_header('Content-Length','2');self.end_headers();self.wfile.write(b'{}')
        self.source.exporter.server.RequestHandlerClass=Exporter
        original_relay=self.source.relay.server.RequestHandlerClass
        class Relay(original_relay):
            def do_POST(self):
                check(self.path=='/api/v1/format_query','only real PromQL syntax endpoint may POST');body=self.rfile.read(int(self.headers.get('Content-Length','0')));check(parse_qs(body.decode())=={'query':[source.expr]},'syntax query scope differs')
                source.guard_prom();conn=http.client.HTTPConnection('127.0.0.1',18711,timeout=5)
                try:conn.request('POST',self.path,body,{'Content-Type':'application/x-www-form-urlencoded'});response=conn.getresponse();data=response.read(65537);check(len(data)<=65536,'syntax response size');self.send_response(response.status);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
                finally:conn.close()
        self.source.relay.server.RequestHandlerClass=Relay
    def create_branches(self):
        c=self.chain;pid=c.projects['main']['id']
        for b in branches(self.nonce).values():
            c.git('POST',f'/api/v4/projects/{pid}/repository/branches',{'branch':b['target'],'ref':c.target},want=201)
            c.git('POST',f'/api/v4/projects/{pid}/protected_branches',{'name':b['target'],'push_access_level':0,'merge_access_level':40,'allow_force_push':False},want=201)
            branch,_=c.git('GET',f'/api/v4/projects/{pid}/repository/branches/'+b['target']);policy,_=c.git('GET',f'/api/v4/projects/{pid}/protected_branches/'+b['target']);policies,_=c.git('GET',f'/api/v4/projects/{pid}/protected_branches?per_page=100&page=1');promotion.safe_policy(branch,policy,policies,b['target'])
    def frozen(self):
        if self.prom_cid:self.guard_prom()
        if self.am_cid:self.guard_am()
        for n,w in self.config_hashes.items():
            file=self.work/n
            check(file.is_file() and not file.is_symlink() and stat.S_IMODE(file.stat().st_mode)==0o600 and hashlib.sha256(file.read_bytes()).hexdigest()==w,'draft source config identity/permissions/bytes drift')
    def cleanup(self):
        errors=[]
        for name,cid,attempted,guard in [(self.prom_name,self.prom_cid,self.prom_attempted,lambda info:rules.guard_owned_prom(info,self,expect_running=False)),(self.am_name,self.am_cid,self.am_attempted,lambda info:routes.guard_am(info,self,live=False))]:
            if not cid:
                if attempted:
                    result=subprocess.run(['docker','container','inspect',name],capture_output=True,text=True,timeout=2)
                    if result.returncode==0 or not ('No such container' in result.stderr or 'No such object' in result.stderr):errors.append('unconfirmed CREATE outcome retained for exact identity recovery: '+name)
                continue
            try:
                info=ro.inspect_pg(cid,timeout=2);guard(info)
                if info['State']['Running']:self.chain.command(['docker','stop','--time','1',cid],timeout=4)
                guard(ro.inspect_pg(cid,timeout=2));self.chain.command(['docker','rm',cid],timeout=3)
                absent=subprocess.run(['docker','container','inspect',name],capture_output=True,text=True,timeout=2);check(absent.returncode!=0 and ('No such container' in absent.stderr or 'No such object' in absent.stderr),'exact source absence not proven')
            except BaseException as e:errors.append(str(e))
        if self.source:
            try:self.source.close()
            except BaseException as e:errors.append(str(e))
        ro._private(self.work/'draft-source-cleanup.json',{'prom':self.prom_cid,'am':self.am_cid,'errors':errors,'captures':self.capture_count,'synthetic_history_requests':self.history_requests})
        if errors:raise Blocked('own draft source cleanup incomplete; evidence retained')
        self.prom_cid=self.am_cid=None

def history_payload(query,app,key,source_id):
    from datetime import datetime,timedelta
    check(set(query)=={'from','through'} and all(len(x)==1 for x in query.values()),'exact history request shape')
    at=lambda x:datetime.fromisoformat(x.replace('Z','+00:00'));start,end=at(query['from'][0]),at(query['through'][0]);check((end-start).total_seconds()==604800,'real requested seven-day window required')
    stamp=(end-timedelta(hours=1)).isoformat().replace('+00:00','Z')
    return {'environment_id':'test','source_id':source_id,'complete':True,'coverage_start':query['from'][0],'coverage_end':query['through'][0],'alerts':[{'id':'synthetic-'+severity,'labels':{'application':key,'environment':'test','severity':severity,'instance':'owned'},'starts_at':stamp,'ends_at':None,'firing_interval':True} for severity in ('P1','P3')]}

class DraftChain(promotion.Chain):
    def __init__(self,root,backend,gitlab,run_id,source_bootstrap=None):
        check(backend.get('source_tree_sha')==SOURCE and hashlib.sha256(Path(backend['binary']).read_bytes()).hexdigest()==BINARY,'frozen Go6ad/bfeb binary required')
        super().__init__(root,backend,gitlab,run_id);self.package_sha256=hashes();parent=root/'.local/testlab/config-drafts-parallel';parent.mkdir(parents=True,exist_ok=True,mode=0o700);destination=parent/run_id;self.work.rename(destination);self.work=destination;self.state['work']=str(destination);self.save()
        self.extras={};self.sources=None;self.cleanup_deadline=None;self.source_bootstrap=source_bootstrap or self.prepare_sources
    def frozen(self):
        super().frozen()
        if hashes()!=self.package_sha256:raise Blocked('draft package or shared helper changed during live invocation')
        if getattr(self,'sources',None):self.sources.frozen()
    def start(self,tier):
        old=promotion.command_env
        def env(variables,*args,**kwargs):return old(dict(variables,**self.extras),*args,**kwargs)
        promotion.command_env=env
        try:return super().start(tier)
        finally:promotion.command_env=old
    def command(self,argv,**kwargs):
        if getattr(self,'cleanup_deadline',None):
            remaining=self.cleanup_deadline-time.monotonic()
            if remaining<=0:raise Blocked('aggregate own draft cleanup deadline exhausted')
            kwargs['timeout']=min(kwargs.get('timeout',30),remaining)
        return super().command(argv,**kwargs)
    def prepare_sources(self,chain):
        self.proxies['source'].close()
        self.proxies['source']=DraftTLSProxy(self,'source')
        self.proxies['source'].server.RequestHandlerClass.do_PUT=self.proxies['source'].server.RequestHandlerClass.do_POST
        self.sources=Sources(self);return self.sources.prepare()
    def seed_identities(self):
        self.reader_role='draft-reader-'+self.run_id;self.reader_uid=uuid.uuid4().hex
        denied_uid=uuid.uuid4().hex;denied_role='draft-denied-'+self.run_id
        admin_email=self.admin['source']['email'];statements=['BEGIN;']
        for name,uid,role in [('reader',self.reader_uid,self.reader_role),('denied',denied_uid,denied_role)]:
            email='draft-'+name+'-'+self.run_id+'@testlab.invalid'
            statements.append("INSERT INTO users(id,email,name,role,password_hash) SELECT "+','.join(promotion.quote(v) for v in (uid,email,name))+",'viewer',password_hash FROM users WHERE email="+promotion.quote(admin_email)+';')
            statements.extend(['DELETE FROM rbac_user_roles WHERE user_id='+promotion.quote(uid)+';',"INSERT INTO rbac_roles(id,name,v3_kind) VALUES("+promotion.quote(role)+','+promotion.quote(role)+",'business');",'INSERT INTO rbac_user_roles(user_id,role_id) VALUES('+promotion.quote(uid)+','+promotion.quote(role)+');'])
            grants=[('environment','view')]+([('alert_rules','view'),('alert_rules','manage'),('alert_rules','change'),('silences','view'),('silences','manage')] if name=='reader' else [])
            for module,action in grants:statements.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(promotion.quote(v) for v in (role,module,action,'environment','test'))+');')
        statements.append('COMMIT;');self.assert_database('source');self.pg(self.databases['source'],' '.join(statements))
        for name in ('reader','denied'):
            value,headers=self.call('source','POST','/api/v1/auth/login',{'email':'draft-'+name+'-'+self.run_id+'@testlab.invalid','password':self.admin['source']['password']})
            cookie=headers.get('Set-Cookie','');check(cookie.startswith('aiops_session=') and '; Secure' in cookie,'synthetic least-privilege login failed')
            if name=='reader':self.cookies['source']=cookie.split(';',1)[0]
            else:self.denied_cookie=cookie.split(';',1)[0]
        emit('drafts.synthetic_identities','passed',{'business_reader':self.reader_uid,'environment_scoped':True,'denied_role_no_write':True,'SQL_success_business_seed':False})
    def run(self):return Workflow(self,self.source_bootstrap).run()
    def cleanup(self):
        self.cleanup_deadline=time.monotonic()+40;errors=[]
        # Stop own APIs before retiring source containers and DBs, using handles.
        for tier in list(self.processes):
            try:self.stop(tier,self.cleanup_deadline)
            except BaseException as error:errors.append(str(error))
        if self.sources:
            try:self.sources.cleanup()
            except BaseException as error:errors.append(str(error))
        try:super().cleanup()
        except BaseException as error:errors.append(str(error))
        if errors:raise Blocked('draft aggregate cleanup incomplete: '+ '; '.join(errors))
        absent={}
        for port in (18706,18707,18708,18709,18710,18711,18712):
            with socket.socket() as sock:absent[str(port)]=sock.connect_ex(('127.0.0.1',port))!=0
        check(all(absent.values()),'owned draft listener residual')
        emit('drafts.cleanup','passed',{'ports_absent':absent,'source_containers_retired':True,'held_children_exited':True,'own_DB_role_project_tokens_cleanup_verified':True})

def inventory():return {'mode':'offline_preparation','source_fingerprint':SOURCE,'binary_sha256':BINARY,'targets':TARGETS,'dedicated_ports':[18706,18707,18708,18709,18710,18711,18712],'source_bootstrap':'implemented candidate: native app, per-run owned real Prom/AM, source ETags and synthetic history; not live verified','live_fault_regressions':'implemented candidate: idempotency, CAS, permission/revocation, product response loss and remote MR success/local final audit failure; not live verified','no_live_pass_claim':True,'history':'seven-day complete provider required; synthetic_history must be protocol_mock','lifecycle':'promotion_chain.Chain exact UUID project/DB/role and held Popen cleanup','merge_or_activation':'excluded'}

def main(argv=None):
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--inventory',action='store_true');p.add_argument('--live',action='store_true');a=p.parse_args(argv)
    if not a.live:print(json.dumps(inventory(),ensure_ascii=False));return 0
    try:
        with promotion.cooperative_signals(),ports(),promotion.exclusive(ROOT):
            backend,gitlab=promotion.doctor(ROOT)
            for port in (18708,18709,18711,18712):promotion.free_port(port)
            for image,want in ((rules.PROM_IMAGE,rules.PROM_IMAGE_ID),(routes.AM_IMAGE,routes.AM_IMAGE_ID)):
                info=json.loads(subprocess.check_output(['docker','image','inspect',image],text=True,timeout=5));check(len(info)==1 and info[0]['Id']==want,'fixed local source image unavailable; no pull')
            chain=DraftChain(ROOT,backend,gitlab,uuid.uuid4().hex);actual=promotion.execute(chain);emit('drafts.complete','passed',actual);return 0
    except BaseException as error:
        emit('drafts.failure','blocked' if isinstance(error,Blocked) else 'failed',{'error':promotion.safe_error(error),'error_type':type(error).__name__});return 2 if isinstance(error,Blocked) else 1
if __name__=='__main__':raise SystemExit(main())
