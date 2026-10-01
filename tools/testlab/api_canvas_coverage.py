"""Owned HTTP Canvas lifecycle; --live is a parent-only opt-in after freezing.

SQL creates identities/grants and explicitly synthetic template configuration,
never investigation/context/reply/audit results. Model inference is protocol Mock.
"""
from __future__ import annotations
import argparse,hashlib,http.client,http.server,importlib.util,json,socket,sys,threading,time,uuid
from pathlib import Path
from urllib.parse import urlencode,urlsplit

ROOT=Path(__file__).resolve().parents[2]
# Independent globals: no mutation of the management helper used by other packets.
spec=importlib.util.spec_from_file_location('_canvas_owned_management',Path(__file__).with_name('api_management_chain.py'))
management=importlib.util.module_from_spec(spec);spec.loader.exec_module(management)
management.PORTS=(18608,18609);management.BASES=tuple('http://127.0.0.1:'+str(p) for p in management.PORTS)
management.MODULE='formal-api-canvas-coverage'
TARGETS=(('GET','/investigation-templates'),('POST','/investigation-templates/{id}/start'),
 ('GET','/investigations/{id}/canvas'),('GET','/investigations/{id}/writeback-preview'),
 ('PATCH','/investigations/{id}/nodes/{node}'),('POST','/investigations/{id}/branches'),
 ('POST','/investigations/{id}/writeback'),('POST','/investigations/{id}/nodes/{node}/messages'),
 ('POST','/canvases/{id}/nodes/{node}/messages'),('GET','/conversations/{id}/events'))
SUPPORT=(('POST','/auth/login'),('POST','/services'),('POST','/canvases'),('GET','/canvases/{id}'),
 ('POST','/conversations'),('POST','/conversations/{id}/messages'),('GET','/conversations/{id}'))
check,Blocked=management.check,management.Blocked

class Contract(management.Contract):
    def canonical(self,method,path):
        import re
        raw=urlsplit(path).path
        if not raw.startswith('/api/v1/'):raise Blocked('Canvas public prefix required')
        short=raw[len('/api/v1'):]
        matches=[p for m,p in (*TARGETS,*SUPPORT) if m==method and re.fullmatch(re.sub(r'\\\{[^}]+\\\}',r'[^/]+',re.escape(p)),short)]
        canonical=short if short in matches else matches[0] if len(matches)==1 else None
        if canonical is None or method.lower() not in self.api['paths'].get(canonical,{}):raise Blocked('outside bounded Canvas operations')
        return '/api/v1'+canonical,'main' if (method,canonical) in self.main else 'auxiliary_or_historical'

class ModelMock(http.server.BaseHTTPRequestHandler):
    def setup(self):
        super().setup();self.connection.settimeout(3)
    def log_message(self,*args):pass
    def do_POST(self):
        size=int(self.headers.get('Content-Length','0'))
        if self.path!='/v1/chat/completions' or not 0<size<=262144:self.send_error(400);return
        body=json.loads(self.rfile.read(size));answer=self.server.answer
        self.server.calls+=1
        if body.get('stream'):
            raw=('data: '+json.dumps({'id':'synthetic','choices':[{'index':0,'delta':{'role':'assistant','content':answer},'finish_reason':None}]})+'\n\ndata: '+json.dumps({'id':'synthetic','choices':[{'index':0,'delta':{},'finish_reason':'stop'}]})+'\n\ndata: [DONE]\n\n').encode();kind='text/event-stream'
        else:
            raw=json.dumps({'id':'synthetic','object':'chat.completion','model':body.get('model'),'choices':[{'index':0,'message':{'role':'assistant','content':answer},'finish_reason':'stop'}]}).encode();kind='application/json'
        self.send_response(200);self.send_header('Content-Type',kind);self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)

class Chain(management.Chain):
    def __init__(self,root,data,run_id):
        super().__init__(root,data,run_id);self.contract=Contract(root)
        self.mock=None;self.mock_thread=None;self.mock_stop=threading.Event();self.definition_digest=None
        self.answer='Synthetic protocol Mock '+self.nonce+'. No real inference.'
        self.helper_digest=hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        self.template='canvas-'+self.nonce
        self.state.update(suite=management.MODULE,ports=list(management.PORTS),fixture_origin='synthetic_test_configuration',model_evidence='protocol_mock')
        management._private(self.work/'state.json',self.state)
    def frozen(self):
        super().frozen()
        check(hashlib.sha256(Path(__file__).read_bytes()).hexdigest()==self.helper_digest,'Canvas test helper changed during run')
    def start(self):
        with management.acquisition_signals():
            self.mock=http.server.ThreadingHTTPServer(('127.0.0.1',0),ModelMock);self.mock.daemon_threads=True;self.mock.timeout=.2
            self.mock.answer=self.answer;self.mock.calls=0
            def serve():
                while not self.mock_stop.is_set():self.mock.handle_request()
            self.mock_thread=threading.Thread(target=serve,name='canvas-model-'+self.nonce,daemon=True);self.mock_thread.start()
        original=management.command_env
        def own_env(variables,preserve):
            check(urlsplit(variables.get('DATABASE_URL','')).path=='/'+self.database,'Mock config outside owned DB')
            return original({**variables,'OPENAI_BASE_URL':'http://127.0.0.1:'+str(self.mock.server_port)+'/v1'},preserve)
        management.command_env=own_env
        try:return super().start()
        finally:management.command_env=original
    def cleanup(self):
        try:return super().cleanup()
        finally:
            primary=sys.exc_info()[0];errors=[];self.mock_stop.set()
            # No BaseServer.shutdown: it waits forever if serving never started.
            # Join the .2s polling loop before closing its selector socket.
            if self.mock_thread and self.mock_thread.ident is not None:
                try:
                    self.mock_thread.join(timeout=3)
                    if self.mock_thread.is_alive():errors.append('MockThreadStillAlive')
                except BaseException as error:errors.append(type(error).__name__)
            if self.mock:
                try:self.mock.server_close()
                except BaseException as error:errors.append(type(error).__name__)
            self.mock_cleanup_errors=errors
            if errors and primary is None:raise Blocked('own Mock cleanup failed: '+','.join(errors))
    def seed(self):
        super().seed()
        rows=[]
        for module in ('canvas','conversation'):
            for action in ('view','change'):
                rows.append('INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('+','.join(management.quote(x) for x in (self.roles['reader'],module,action,'environment','test'))+');')
        self.sql('\n'.join(rows))
    def configure_template(self):
        self.definition={'branches':[{'name':'Manual bounded check','step':'Read synthetic context only','tools':[]}],
                         'tools':[],'runbooks':[],'triggers':{'manual':True,'auto_triage':False,'matchers':[]},'budget':{'turns':1,'tools':0,'minutes':1}}
        self.definition_digest=hashlib.sha256(json.dumps(self.definition,sort_keys=True).encode()).hexdigest()
        management._private(self.work/'template-definition.json',{'origin':'synthetic_test_configuration','schema':'migrations/134_v3_investigation_templates.sql','seed':'owned UUID DB INSERT definition only','definition':self.definition,'sha256':self.definition_digest,'author_import_producer':'missing; High TODO'})
        fields=[self.template,'official','test',self.app,self.marker+' template','Explicit synthetic manual outline']
        self.sql('INSERT INTO v3_investigation_templates(id,owner,environment_id,application_id,name,description,version,branches,tools,runbooks,triggers,budget) VALUES('+','.join(management.quote(x) for x in fields)+',1,'+','.join(management.quote(json.dumps(self.definition[k]))+'::jsonb' for k in ('branches','tools','runbooks','triggers','budget'))+');')
    def templates(self):
        self.call('reader','GET','/api/v1/investigation-templates',validate=lambda v:check(any(x['id']==self.template for x in v['items']),'own definition not listed'))
        path='/api/v1/investigation-templates/'+self.template+'/start'
        body={'inputs':{'application_id':self.app,'template_version':'v1','request_id':'canvas-'+self.nonce}}
        started,_=self.call('reader','POST',path,body,want=201);self.investigation=started['investigation_id']
        self.call('reader','POST',path,body,index=1,scenario='idempotency',validate=lambda v:check(v['investigation_id']==self.investigation,'template retry duplicated investigation'))
        bad={'inputs':{**body['inputs'],'template_version':'v2'}}
        self.call('reader','POST',path,bad,want=409,code='template_request_conflict',scenario='idempotency')
        self.call('reader','POST',path,{'inputs':{}},want=422,code='invalid_template_start',scenario='boundary')
        self.call('reader','POST',path,body,environment='staging',want=403,code='forbidden',scenario='authorization')
        count=self.sql('SELECT uses FROM v3_investigation_templates WHERE id='+management.quote(self.template)+';',read=True)
        check(int(count)==1,'retry changed template use count')
    def graph(self):
        value,_=self.call('reader','GET','/api/v1/investigations/'+self.investigation+'/canvas')
        check(value['investigation']['id']==self.investigation and len(value['nodes'])>=2,'template did not create native graph')
        return value
    def canvas(self):
        base='/api/v1/investigations/'+self.investigation
        graph=self.graph();root=next(n for n in graph['nodes'] if not n.get('parent'));root_id=root['id']
        revision=lambda node:int(node['context']['version'][1:])
        child,_=self.call('reader','POST',base+'/branches',{'parent_id':root_id,'title':self.marker+' child','expected_parent_revision':revision(root)},want=201)
        check(child.get('parent')==root_id,'fork lost exact parent')
        path=base+'/nodes/'+child['id']
        patch={'x':123,'y':234,'expected_layout_version':child['layout_version']}
        changed,_=self.call('reader','PATCH',path,patch)
        check(changed['x']==123 and changed['y']==234 and changed['layout_version']==child['layout_version']+1,'layout CAS did not advance')
        self.call('reader','PATCH',path,patch,want=409,code='layout_version_conflict',scenario='boundary',index=1)
        self.call('reader','POST',path+'/messages',{'text':self.marker+' child question','model':'openai:management-synthetic'},validate=lambda v:check(v['summary']==self.answer,'node Mock answer not committed'))
        preview,_=self.call('reader','GET',base+'/writeback-preview?'+urlencode({'node':child['id'],'target':root_id}))
        check(preview['target']==root_id and preview['conclusions'],'preview omitted committed child message')
        selected=[preview['conclusions'][0]['id']]
        payload={'node':child['id'],'target':root_id,'items':selected,'mode':'summary','expected_source_revision':preview['source_revision'],'expected_target_revision':preview['target_revision']}
        done,_=self.call('reader','POST',base+'/writeback',payload)
        check(done['target_revision']==preview['target_revision']+1 and done['source_revision']==preview['source_revision'],'writeback revisions differ')
        self.call('reader','POST',base+'/writeback',payload,want=409,code='writeback_revision_conflict',scenario='idempotency',index=1)
        current=self.graph();nodes={n['id']:n for n in current['nodes']}
        check(nodes[child['id']]['parent']==root_id and revision(nodes[child['id']])==preview['source_revision'] and revision(nodes[root_id])==done['target_revision'],'writeback changed source or topology')
        self.call('reader','GET',base+'/canvas',environment='staging',want=403,code='forbidden',scenario='authorization')
        self.call('denied','GET',base+'/canvas',want=404,code='not_found',scenario='authorization')
        generic,_=self.call('reader','POST','/api/v1/canvases',{'title':self.marker+' plan','kind':'plan','services':[self.app]},want=201)
        self.call('reader','POST','/api/v1/canvases/'+generic['id']+'/nodes/root/messages',{'text':self.marker+' generic question','model':'openai:management-synthetic'},validate=lambda v:check(v['reply']==self.answer,'generic node Mock answer differs'))
    def sse_response(self,path):
        # Fixed own origin only; a wall-clock deadline also ends trickle/keepalive reads.
        self.contract.canonical('GET',path)
        target=urlsplit(management.BASES[0]);connection=http.client.HTTPConnection(target.hostname,target.port,timeout=12)
        started=time.monotonic();transport=None;timer=None;response=None
        def stop():
            if transport:
                try:transport.shutdown(socket.SHUT_RDWR)
                except OSError:pass
        try:
            connection.connect();transport=connection.sock
            timer=threading.Timer(max(.001,12-(time.monotonic()-started)),stop);timer.start()
            connection.request('GET',path,headers={'Cookie':self.cookies['reader'],'X-AIOps-Environment':'test','Accept':'text/event-stream'})
            response=connection.getresponse();raw=response.read(management.LIMIT+1)
            check(len(raw)<=management.LIMIT and time.monotonic()-started<12,'SSE evidence limit/deadline exceeded')
            return response.status,raw,dict(response.getheaders())
        finally:
            if timer:timer.cancel();timer.join()
            if response:response.close()
            connection.close()
            if transport:transport.close()
    def events(self):
        conversation,_=self.call('reader','POST','/api/v1/conversations',{'text':self.marker+' stream','model':'openai:management-synthetic'},want=201)
        sent,_=self.call('reader','POST','/api/v1/conversations/'+conversation['id']+'/messages',{'text':self.marker+' SSE question','model':'openai:management-synthetic'},want=202)
        path='/api/v1/conversations/'+conversation['id']+'/events?'+urlencode({'reply_id':sent['reply_id']})
        canonical,route_class=self.contract.canonical('GET',path);self.calls+=1
        row=dict(name='canvas.events.'+str(self.calls),method='GET',path=canonical,route_class=route_class,scenario='normal',expected={'status':[200]},run_id=self.run_id,call_id=self.run_id+'-'+str(self.calls),test_name='CanvasNativeEvents',registration_evidence=False,status='failed',actual={'status':None,'model_source':'protocol_mock'})
        primary=None
        try:
            status,raw,headers=self.sse_response(path)
            row['actual']={'status':status,'bytes':len(raw),'model_source':'protocol_mock'}
            check(status==200 and management.header(headers,'Content-Type').startswith('text/event-stream'),'events is not native SSE')
            events=[json.loads(line[6:]) for line in raw.decode().splitlines() if line.startswith('data: ')]
            check(any(x.get('type')=='message.completed' for x in events),'native SSE did not reach terminal')
            check(self.answer in raw.decode(),'SSE lost Mock answer')
            row.update(status='passed',registration_evidence=True)
        except BaseException as error:
            primary=error;row['status']='interrupted' if isinstance(error,(KeyboardInterrupt,SystemExit)) else 'failed'
            row['actual']['error_type']=type(error).__name__
            try:self.first_red(row)
            except BaseException as reporting_error:row['actual']['first_red_error_type']=type(reporting_error).__name__
            raise
        finally:
            for report in (lambda:management.append_json(self.work/'interfaces.jsonl',row),lambda:management.emit(row['name'],row['status'],row['actual'],**({'interface':row} if isinstance(row['actual']['status'],int) else {}))):
                try:report()
                except BaseException:
                    if primary is None:raise
    def run(self):
        self.provision();self.start();self.seed()
        for user in ('admin','reader','denied','hidden'):self.login(user)
        key='canvas-'+self.nonce
        self.call('admin','POST','/api/v1/services',{'items':[{'id':key,'tier':'T1'}]},want=201)
        self.app=json.loads(self.sql('SELECT json_build_object(\'id\',id) FROM applications WHERE key='+management.quote(key)+';',read=True))['id']
        self.configure_template()
        for step in (self.templates,self.canvas,self.events):self.frozen();step()
        management.emit('canvas.template_producer','not_implemented',{'priority':'High','missing':'author/import/authoritative investigation template producer','definition_origin':'synthetic_test_configuration','consumer_http_only_verified':True})
        print(json.dumps({'name':'canvas.model_scope','status':'passed','module':management.MODULE,'evidence_mode':'protocol_mock','actual':{'real_inference_verified':False,'mock_requests':self.mock.calls,'proof':'native HTTP persistence, graph and SSE logic only'}}),flush=True)
        self.frozen();rows=[json.loads(line) for line in (self.work/'interfaces.jsonl').read_text().splitlines()]
        positive=sorted({(r['method'],r['path'],r['route_class']) for r in rows if r['registration_evidence']})
        result={'suite':management.MODULE,'run_id':self.run_id,'source_tree_sha':self.data['source_tree_sha'],'binary_sha256':self.data['binary_sha256'],'positive_routes':positive,'template_definition_origin':'synthetic_test_configuration','template_definition_sha256':self.definition_digest,'business_results':'native HTTP only','model_source':'protocol_mock','formal_performance_acceptance':False,'formal_24h_acceptance':False,'unverified':['High TODO: investigation template author/import/authority producer','real model inference','production templates','full166 coverage']}
        management._private(self.work/'canvas-result.json',result);return result

def main(argv=None):
    parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=ROOT)
    mode=parser.add_mutually_exclusive_group(required=True);mode.add_argument('--inventory',action='store_true');mode.add_argument('--live',action='store_true');args=parser.parse_args(argv)
    if args.inventory:print(json.dumps({'routes':TARGETS,'ports':management.PORTS,'offline_only':True,'model_source':'protocol_mock','template_origin':'synthetic_test_configuration'}));return 0
    try:
        with management.exclusive(args.root),management.cooperative_signals():management.execute(Chain(args.root,management.doctor(args.root),str(uuid.uuid4())))
    except Blocked as error:management.emit('canvas.environment','blocked',{'error_type':type(error).__name__});return 2
    except Exception as error:management.emit('canvas.failure','failed',{'error_type':type(error).__name__});return 1
    return 0
if __name__=='__main__':raise SystemExit(main())
