"""Bounded functional event/admin HTTP coverage in the management-owned DB.

No performance data profile, authority simulator or new product producer.
The parent must authorize --live after rebuilding/freezing the formal binary.
"""
from __future__ import annotations
import argparse,datetime,hashlib,json,re,secrets,uuid
from pathlib import Path
from urllib.parse import urlencode,urlsplit
import api_management_chain as management

ROOT=management.ROOT
CORE=(('GET','/inbox'),('GET','/alert-groups'),('GET','/incidents'),('GET','/incidents/draft'),
      ('POST','/incidents'),('POST','/webhooks/alertmanager/{source}'),('GET','/audit/exports/{id}'),
      ('GET','/roles'),('POST','/audit/export'))
check,Blocked=management.check,management.Blocked


class Contract(management.Contract):
    def canonical(self,method,path):
        raw=urlsplit(path).path
        if not raw.startswith('/api/v1/'):raise Blocked('core coverage requires formal API prefix')
        short=raw[len('/api/v1'):];allowed=(*management.TARGETS,*management.SUPPORT,*CORE)
        matches=[pattern for candidate,pattern in allowed if candidate==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(pattern)),short)]
        canonical=short if short in matches else matches[0] if len(matches)==1 else None
        if canonical is None:raise Blocked('HTTP outside bounded core coverage inventory')
        if method.lower() not in self.api['paths'].get(canonical,{}):raise Blocked('core operation absent from public contract')
        return '/api/v1'+canonical,'main' if (method,canonical) in self.main else 'auxiliary_or_historical'


class Chain(management.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id)
        self.contract=Contract(root);self.config_digest=None;self.source_id='core-'+self.nonce
        self.state.update(suite='formal-api-core-coverage',fixture_scope='two HTTP-created services, three authenticated native webhook alerts; not performance base',sql_business_result_writes=0)
        management._private(self.work/'state.json',self.state)

    def frozen(self):
        super().frozen()
        if self.config_digest is not None:
            current=hashlib.sha256((self.work/'operations.json').read_bytes()+(self.work/'source-token').read_bytes()).hexdigest()
            if current!=self.config_digest:raise Blocked('own core source configuration changed during HTTP coverage')

    def start(self):
        phase='setup' if self.config_digest is None else 'source'
        original=management.emit
        def phase_emit(name,status,actual,**fields):
            if name in ('management.api_ready.0','management.api_ready.1'):
                name='core.'+phase+'.api_ready.'+name[-1]
            return original(name,status,actual,**fields)
        management.emit=phase_emit
        try:return super().start()
        finally:management.emit=original

    def seed(self):
        super().seed()
        # Only identity/admission inputs. No business events, cases or audit SQL seed.
        self.sql('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(management.quote(x) for x in (self.roles['reader'],'events','view','environment','test'))+');')

    def call(self,user,method,path,*args,source_token=None,incident_key=None,**kwargs):
        if source_token is None and incident_key is None:return super().call(user,method,path,*args,**kwargs)
        if source_token is not None and not (user is None and method=='POST' and path=='/api/v1/webhooks/alertmanager/'+self.source_id and source_token==self.source_token):raise Blocked('source authentication outside exact own webhook')
        if incident_key is not None and not (method=='POST' and path=='/api/v1/incidents' and incident_key=='core-'+self.nonce+'-case'):raise Blocked('incident key outside exact own case request')
        original=management.transport
        def scoped_transport(base,request_method,request_path,payload=None,headers=None,timeout=12):
            if request_method!=method or request_path!=path:raise Blocked('core scoped header request changed')
            headers=dict(headers or {})
            if source_token is not None:headers['Authorization']='Bearer '+source_token
            if incident_key is not None:headers['Idempotency-Key']=incident_key
            return original(base,request_method,request_path,payload,headers,timeout)
        management.transport=scoped_transport
        try:return super().call(user,method,path,*args,**kwargs)
        finally:management.transport=original

    def services_and_ingress(self):
        mappings=[];self.service_rows={}
        for environment in ('test','staging'):
            key='core-'+self.nonce+'-'+environment
            self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201,environment=environment)
            row=json.loads(self.sql('SELECT json_build_object(\'application_id\',a.id,\'environment_id\',e.id) FROM applications a JOIN application_environments e ON e.application_id=a.id WHERE a.key='+management.quote(key)+' AND e.key='+management.quote(environment)+';',read=True))
            check(all(isinstance(row.get(k),str) and row[k] for k in ('application_id','environment_id')),'HTTP-created service mapping missing')
            self.service_rows[environment]=row;mappings.append(dict(labels={'core_scope':environment},**row))
        self.source_token=secrets.token_urlsafe(32)
        management.private_file(self.work/'source-token',self.source_token+'\n')
        management._private(self.work/'operations.json',{'sources':[{'id':self.source_id,'token_file':str(self.work/'source-token'),'mappings':mappings}],'feishu_channels':[]})
        self.config_digest=hashlib.sha256((self.work/'operations.json').read_bytes()+(self.work/'source-token').read_bytes()).hexdigest()
        # Only Popen handles acquired by this invocation, never recorded/global PIDs.
        self.frozen()
        for process in self.processes:
            if process.poll() is None:process.terminate();process.wait(timeout=8)
        for index in (0,1):(self.work/('api-'+str(index)+'.log')).rename(self.work/('setup-api-'+str(index)+'.log'))
        original=management.command_env
        def own_environment(variables,preserve):
            if urlsplit(variables.get('DATABASE_URL','')).path!='/'+self.database:raise Blocked('core operations config outside own DB launch')
            return original({**variables,'AIOPS_OPERATIONS_CONFIG':str(self.work/'operations.json')},preserve)
        management.command_env=own_environment
        try:self.start()
        finally:management.command_env=original
        at=(datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(minutes=1)).isoformat()
        alerts=[{'status':'firing','fingerprint':'core-'+self.nonce+'-'+str(i),'startsAt':at,'labels':{'alertname':self.marker+' alert '+str(i),'severity':'critical','core_scope':environment},'annotations':{'summary':'Explicit synthetic functional input; no real notification'}} for i,environment in enumerate(('test','test','staging'))]
        self.call(None,'POST','/api/v1/webhooks/alertmanager/'+self.source_id,{'alerts':alerts},want=202,source_token=self.source_token,validate=lambda v:check(v=={'accepted':3,'notification_delivery':'not_configured'},'native webhook did not persist exact batch with notifications disabled'))
        rows=json.loads(self.sql('SELECT json_agg(json_build_object(\'id\',id,\'application_id\',application_id,\'environment_id\',environment_id)) FROM ops_incidents WHERE source_id='+management.quote(self.source_id)+';',read=True))
        check(len(rows)==3,'authenticated webhook rows differ')
        self.alert_ids=sorted(row['id'] for row in rows if row['application_id']==self.service_rows['test']['application_id'])
        self.hidden_alert=next(row['id'] for row in rows if row['application_id']==self.service_rows['staging']['application_id'])
        check(len(self.alert_ids)==2,'webhook application isolation differs')

    def events(self):
        expected=set(self.alert_ids)
        self.call('reader','GET','/api/v1/inbox',validate=lambda v:check(v['counts']['all']==2 and {x['alert_id'] for x in v['items']}==expected,'nonempty inbox scope/count differs'))
        self.call('reader','GET','/api/v1/alert-groups',index=1,validate=lambda v:check(len(v['items'])==2 and sum(x['count'] for x in v['items'])==2 and all(x['environment_key']=='test' for x in v['items']),'nonempty groups differ'))
        self.call('reader','GET','/api/v1/incidents',validate=lambda v:check(v['counts']['all']==2 and {x['id'] for x in v['items']}==expected,'raw occurrence list differs'))
        query='/api/v1/incidents/draft?'+urlencode({'alerts':','.join(self.alert_ids)})
        self.call('reader','GET',query,index=1,validate=lambda v:check({x['id'] for x in v['alerts']}==expected and v['source']=='persisted_alerts','draft lacks selected native alerts'))
        self.call('reader','GET','/api/v1/incidents/draft',want=422,code='invalid_alerts',scenario='boundary')
        self.call('reader','GET','/api/v1/incidents/draft?'+urlencode({'alerts':self.hidden_alert}),want=404,code='not_found',scenario='authorization')
        for path in ('/inbox','/alert-groups','/incidents'):
            self.call('denied','GET','/api/v1'+path,scenario='authorization',validate=lambda v:check(not v['items'] and v['counts']['all']==0,'denied reader leaked event rows'))
        body={'title':self.marker+' confirmed case','severity':'P1','env':'test','services':[self.service_rows['test']['application_id']],'alert_ids':self.alert_ids,'war_room':False,'notify':False,'status_page':False,'open_canvas':False}
        key='core-'+self.nonce+'-case'
        case,_=self.call('admin','POST','/api/v1/incidents',body,want=201,incident_key=key)
        check(isinstance(case.get('id'),str) and case['id'],'HTTP case identity missing');self.case_id=case['id']
        self.call('admin','POST','/api/v1/incidents',body,incident_key=key,scenario='idempotency',index=1,validate=lambda v:check(v['id']==self.case_id,'case replay duplicated native case'))
        self.call('reader','GET','/api/v1/incidents',index=1,validate=lambda v:check(v['counts']['all']==1 and [x['id'] for x in v['items']]==[self.case_id],'native case did not deduplicate linked occurrences'))

    def audits(self):
        value,_=self.call('admin','GET','/api/v1/audit',environment='all')
        expected={'v3.service.created':2,'v3.role.matrix.updated':2,'v3.session.revoked':1,'incident.create':1}
        selected=[row for row in value['items'] if row['action'] in expected]
        check(len(selected)==sum(expected.values()) and all(sum(row['action']==action for row in selected)==count for action,count in expected.items()),'native HTTP audit counts differ')
        for row in selected:self.call('admin','GET','/api/v1/audit/'+row['id'],environment='all',index=1,validate=lambda v,row=row:check(v['id']==row['id'] and v['chain_ok'] is True,'audit detail/seal differs'))
        export,_=self.call('admin','POST','/api/v1/audit/export',{},environment='test')
        url=export.get('url');check(isinstance(url,str) and re.fullmatch('/api/v1/audit/exports/audit-[a-f0-9]{36}',url),'export URL left exact native namespace')
        self.call('peer','GET',url,want=404,code='not_found',scenario='authorization')
        self.call('denied','POST','/api/v1/audit/export',{},want=403,code='forbidden',scenario='authorization')
        self.call('admin','POST','/api/v1/audit/export',{'filter':'invalid'},want=422,code='invalid_filter',scenario='boundary',environment='test')
        self.call('admin','POST','/api/v1/audit/export',{},want=409,code='env_view_only',scenario='boundary',environment='all')

    def run(self):
        self.provision();self.start();self.seed()
        for user in ('admin','reader','peer','denied','hidden'):self.login(user)
        self.frozen();self.services_and_ingress();self.frozen();self.events();self.frozen()
        self.call('admin','GET','/api/v1/roles',validate=lambda v:check(any(row['id']==self.roles['target'] for row in v['items']),'target role absent'))
        for step in (self.role_matrix,self.sessions,self.audits):self.frozen();step()
        self.frozen()
        rows=[json.loads(line) for line in (self.work/'interfaces.jsonl').read_text().splitlines()]
        positive=sorted({(row['method'],row['path'],row['route_class']) for row in rows if row['registration_evidence']})
        result={'suite':'formal-api-core-coverage','run_id':self.run_id,'database':self.database,'http_results':self.calls,'positive_routes':[list(x) for x in positive],'source_tree_sha':self.data['source_tree_sha'],'binary_sha256':self.data['binary_sha256'],'fixture':'HTTP services/native authenticated webhook/case/role/session/audit; SQL identity inputs and readback only','formal_performance_acceptance':False,'formal_24h_acceptance':False,'unverified':['100/5000/10000 performance profile','real authority or external notification','physical identity','full 166 coverage','CSV owner download']}
        management._private(self.work/'core-result.json',result);return result


def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT)
    mode=parser.add_mutually_exclusive_group(required=True);mode.add_argument('--inventory',action='store_true');mode.add_argument('--live',action='store_true')
    args=parser.parse_args(argv)
    if args.inventory:
        print(json.dumps({'module':'formal-api-core-coverage','core_routes':CORE,'ports':management.PORTS,'offline_only':True,'fixture':'small functional sample; not performance base'},ensure_ascii=False));return 0
    try:
        with management.exclusive(args.root),management.cooperative_signals():
            management.execute(Chain(args.root,management.doctor(args.root),str(uuid.uuid4())))
    except Blocked as error:management.emit('core.environment','blocked',{'error_type':type(error).__name__});return 2
    except Exception as error:management.emit('core.failure','failed',{'error_type':type(error).__name__});return 1
    return 0


if __name__=='__main__':raise SystemExit(main())
