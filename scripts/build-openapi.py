#!/usr/bin/env python3
"""Build the checked-in, framework-independent backend contract."""
import json
from pathlib import Path
S = {}
def obj(properties, required=None):
    v={'type':'object','properties':properties,'additionalProperties':False}
    if required: v['required']=required
    return v
def ref(name): return {'$ref':'#/components/schemas/'+name}
def arr(items): return {'type':'array','items':items}
def string(**kw): return {'type':'string',**kw}
def integer(**kw): return {'type':'integer',**kw}
def fieldnames(names): return {n:string() for n in names.split()}
def listed(name): return obj({'items':arr(ref(name))},['items'])
S['Error']=obj({'error':obj(fieldnames('code message'),['code','message'])},['error'])
S['User']=obj({**fieldnames('id email name'),'role':string(enum=['admin','operator','viewer']),'disabled':{'type':'boolean'}},['id','email','name','role'])
S['Application']=obj(fieldnames('id name description environment'),['id','name','environment'])
S['Model']=obj({**fieldnames('id name provider availability'),'available':{'type':'boolean','description':'Configured adapter; not proof of a successful remote health check.'},'tools':{'type':'boolean'},'streaming':{'type':'boolean'}})
S['Integration']=obj({**fieldnames('id name kind provider model'),'configured':{'type':'boolean'},'status':string(enum=['configured_unverified','not_configured']),'application_ids':arr(string()),'capabilities':arr(string())},['id','name','kind','provider','configured','status','application_ids','capabilities'])
S['ContainerImage']=obj(fieldnames('name image'),['name','image'])
S['DeploymentSnapshot']=obj({**fieldnames('application_id namespace deployment uid template_digest'),'images':arr(ref('ContainerImage')),**{name:integer() for name in ['replicas','ready_replicas','updated_replicas','generation','observed_generation']},'revisions':arr(obj({**fieldnames('revision digest'),'images':arr(ref('ContainerImage'))})),'captured_at':string(format='date-time')})
S['Message']=obj({**fieldnames('id content model_id'),'role':string(enum=['user','assistant','tool']),'created_at':string(format='date-time'),'evidence_ids':arr(string())},['id','role','content','created_at'])
S['Branch']=obj({**fieldnames('id investigation_id parent_id title model_id status'),'revision':integer(),'messages':arr(ref('Message'))},['id','investigation_id','title','revision','messages'])
S['Evidence']=obj({**fieldnames('id branch_id kind title content source'),'created_at':string(format='date-time')})
S['Run']=obj({**fieldnames('id branch_id model_id error'),'status':string(enum=['queued','running','completed','failed','cancelled']),'attempt':integer(),'created_at':string(format='date-time')})
S['Investigation']=obj({**fieldnames('id application_id title status'),'created_at':string(format='date-time'),'updated_at':string(format='date-time'),'branches':arr(ref('Branch')),'evidence':arr(ref('Evidence')),'runs':arr(ref('Run'))})
S['MergePreview']=obj({**fieldnames('id source_branch_id target_branch_id'),'source_revision':integer(),'target_revision':integer(),'additions':arr(ref('Message')),'conflicts':arr(string())})
S['Event']=obj({'seq':integer(format='int64'),**fieldnames('type investigation_id branch_id'),'payload':{'type':'object','additionalProperties':True},'created_at':string(format='date-time')})
S['Layout']=obj({'layout':{'type':'object','additionalProperties':True}},['layout'])
S['Document']=obj({**fieldnames('id application_id title body source content_hash status error embedding_provider embedding_model'),'version':integer(minimum=1),'generation_id':integer(),'dimensions':integer(),'created_at':string(format='date-time')})
S['KnowledgeHit']=obj({**fieldnames('document_id application_id title source content'),'version':integer(),'chunk':integer(),'generation_id':integer(),'similarity':{'type':'number'}})
S['RollbackProposal']=obj({**fieldnames('namespace deployment uid expected_template_digest expected_spec_digest target_template_digest target_revision digest'),'target_template':{'type':'object','additionalProperties':True},'observation_seconds':integer(minimum=180)})
S['ActionPlan']=obj({**fieldnames('id investigation_id created_by approved_by'),'created_at':string(format='date-time'),'approved_at':string(format='date-time'),'proposal':ref('RollbackProposal')})
S['Execution']=obj({**fieldnames('id plan_id error'),'status':string(enum=['queued','executing','verifying','succeeded','failed','needs_attention']),'attempt':integer(),'result':{},'created_at':string(format='date-time'),'updated_at':string(format='date-time')})

S['Branch']['properties'].update({k:integer() for k in ['inherited_parent_revision','synced_parent_revision','current_parent_revision']})
S['Branch']['properties']['parent_has_updates']={'type':'boolean'}
S['MergeConflict']=obj({**fieldnames('kind review message'),'source_message_ids':arr(string())})
S['MergePreview']['properties'].update({'evidence':arr(ref('Evidence')),'conflict_details':arr(ref('MergeConflict'))})
S['RevisionProvenance']=obj({**fieldnames('kind source_branch_id preview_id merge_mode'),'source_revision':integer(),'message_ids':arr(string())})
S['ContextRevision']=obj({'branch_id':string(),'revision':integer(),'message_count':integer(),'created_at':string(format='date-time'),'provenance':ref('RevisionProvenance'),'messages':arr(ref('Message')),'evidence':arr(ref('Evidence'))})
S['Document']['properties'].update({'investigation_id':string(),'evidence_ids':arr(string())})
S['KnowledgeDraft']=obj({**fieldnames('id investigation_id application_id title body created_by confirmed_by document_id'),'evidence_ids':arr(string()),'created_at':string(format='date-time'),'confirmed_at':string(format='date-time')})
S['ActionPlan']['properties'].update({'decision':string(enum=['pending','approved','rejected','revoked']),'decision_by':string(),'decision_reason':string(),'decided_at':string(format='date-time')})
S['InvestigationReport']=obj({**fieldnames('investigation_id application_id execution_id plan_id summary closed_by approved_by'),'closed_at':string(format='date-time'),'proposal':ref('RollbackProposal'),'recovery':{'type':'object','additionalProperties':True},'evidence':arr(ref('Evidence'))})
S['TelemetryWindow']=obj({'start':string(format='date-time'),'end':string(format='date-time'),'step_seconds':integer()})
S['TelemetrySource']=obj({**fieldnames('provider url application_id query_id query'),'window':ref('TelemetryWindow'),'captured_at':string(format='date-time'),'http_status':integer(),'warnings':arr(string()),'failure':{'type':'object','additionalProperties':True}})
S['MetricEvidence']=obj({'source':ref('TelemetrySource'),'empty':{'type':'boolean'},'series':arr(obj({'labels':{'type':'object','additionalProperties':string()},'samples':arr(obj({'timestamp':{'type':'number'},'value':string()}))}))})
S['LogEvidence']=obj({'source':ref('TelemetrySource'),'empty':{'type':'boolean'},'limit_reached':{'type':'boolean'},'entries':arr(obj({'timestamp':string(format='date-time'),'line':string(),'labels':{'type':'object','additionalProperties':string()}}))})

P={}
def route(method,path,summary,role='viewer',body=None,response=None,status=200,desc='',query=None):
    op={'summary':summary,'operationId':method+'_'+path.strip('/').replace('/','_').replace('{','').replace('}','').replace('-','_'),'description':f'Minimum role: {role}. '+desc,'responses':{},'tags':[path.strip('/').split('/')[0]],'security':[{'sessionCookie':[]}]}
    if role=='public': op['security']=[]
    import re
    params=re.findall(r'{([^}]+)}',path)
    if params: op['parameters']=[{'name':name,'in':'path','required':True,'schema':integer(minimum=0) if name=='revision' else string()} for name in params]
    if query: op.setdefault('parameters',[]).extend(query)
    if body is not None: op['requestBody']={'required':True,'content':{'application/json':{'schema':body}}}
    op['responses'][str(status)]={'description':'Success'}
    if response: op['responses'][str(status)]['content']={'application/json':{'schema':response}}
    for err in (401,403,404,409,422,503): op['responses'][str(err)]={'description':{401:'Authentication required',403:'Forbidden or cross-origin request',404:'Not found',409:'Conflict or stale version',422:'Invalid input',503:'Dependency unavailable'}[err],'content':{'application/json':{'schema':ref('Error')}}}
    P.setdefault(path,{})[method]=op
route('post','/auth/login','Log in','public',obj(fieldnames('email password'),['email','password']),obj({'user':ref('User')}),desc='Sets HttpOnly SameSite=Strict cookie; browser writes require matching Origin.')
route('get','/auth/me','Current account',response=obj({'user':ref('User')}))
route('post','/auth/logout','Log out',status=204)
route('get','/users','List workspace users','admin',response=listed('User'))
route('post','/users','Create workspace user','admin',obj({**fieldnames('email name password'),'role':string(enum=['admin','operator','viewer'])},['email','name','password','role']),obj({'user':ref('User')}),201)
route('patch','/users/{id}','Update account and revoke sessions','admin',obj({**fieldnames('name password'),'role':string(enum=['admin','operator','viewer']),'disabled':{'type':'boolean'}}),obj({'user':ref('User')}),desc='Role/password/disabled changes revoke existing sessions; last active administrator cannot be removed.')
route('get','/applications','List registered applications',response=listed('Application'))
route('post','/applications','Register application metadata','admin',obj(fieldnames('name description environment'),['name']),ref('Application'),201,desc='Connector credentials remain server-side configuration; registering metadata does not grant tool access.')
route('get','/models','List configured model adapters',response=listed('Model'))
route('get','/integrations','Read safe integration configuration inventory',response=obj({'items':arr(ref('Integration')),'configuration_mode':string(enum=['server_managed'])},['items','configuration_mode']),desc='Contains no provider URLs, credential paths or secrets. Configured status is not a successful connectivity check. Configuration changes require server deployment configuration; there is no settings write API.')
route('get','/applications/{id}/deployment','Read registered Deployment image and revision summary',response=ref('DeploymentSnapshot'),desc='Only registered application scope is accepted. Returns images and replica counts, not full Pod templates or environment variables. Unconfigured applications return 404. This GET never executes a rollback.')
route('get','/investigations','List investigations',response=listed('Investigation'))
route('post','/investigations','Create investigation','operator',obj(fieldnames('application_id title'),['application_id','title']),ref('Investigation'),201)
route('get','/investigations/{id}','Get investigation and committed state',response=ref('Investigation'))
route('post','/branches/{id}/messages','Start a durable inference run','operator',obj({'content':string(maxLength=65536),'model_id':string(),'client_request_id':string(minLength=8,maxLength=128)},['content','model_id','client_request_id']),obj({'run':ref('Run')}),202,desc='One active run per branch. Repeated request ID with same payload returns same run; changed payload is 409. Model is frozen per run.')
route('post','/branches/{id}/fork','Fork from a committed snapshot','operator',obj({'title':string(),'revision':integer(minimum=0)},['title']),ref('Branch'),201)
route('patch','/branches/{id}','Set branch title or future model','operator',obj(fieldnames('title model_id')),ref('Branch'))
route('post','/branches/{id}/merge-preview','Preview an explicit merge or parent sync','operator',obj(fieldnames('source_branch_id'),['source_branch_id']),ref('MergePreview'),desc='No authority is transferred by merging context.')
route('post','/branches/{id}/merge','Confirm a version-bound merge','operator',obj({'preview_id':string(),'message_ids':arr(string()),'mode':string(enum=['messages','evidence_only'])},['preview_id']),ref('Branch'),desc='Source/target revision drift or consumed preview returns 409. Target becomes a new revision. Omitted message_ids selects all; explicit empty/unknown/duplicate selections are rejected. evidence_only creates read-only provenance references and does not copy approvals. Only full direct-parent message synchronization advances the parent watermark.')
route('post','/runs/{id}/cancel','Cancel inference; retain committed history','operator',response=obj({'run':ref('Run')}))
route('get','/investigations/{id}/layout','Read personal layout and drafts',response=ref('Layout'))
route('put','/investigations/{id}/layout','Save personal layout and drafts',body=ref('Layout'),response=ref('Layout'),desc='Scoped to current user. Does not mutate branch history or cancel a run.')
route('get','/investigations/{id}/events','Replay committed events using SSE',query=[{'name':'after','in':'query','schema':integer(minimum=0)},{'name':'Last-Event-ID','in':'header','schema':string()}],desc='Uses greatest supplied cursor. event:update, id:decimal sequence, data:Event JSON. Heartbeats are comments. Consumers deduplicate by seq. Delta events belong to an attempt and are not final assistant history; reconcile by GET investigation.')
P['/investigations/{id}/events']['get']['responses']['200']['content']={'text/event-stream':{'schema':string(),'example':'id: 42\nevent: update\ndata: {"seq":42,"type":"run.completed","investigation_id":"...","payload":{}}\n\n'}}
route('get','/knowledge','List source documents and latest index status',response=listed('Document'),query=[{'name':'application_id','in':'query','schema':string()}])
route('get','/knowledge/{id}','Read an immutable source document',response=ref('Document'))
route('post','/knowledge','Import Runbook and build index','operator',obj({**fieldnames('application_id title body source'),'version':integer(minimum=1)},['title','body']),ref('Document'),desc='Empty application_id means workspace-global. Embedding failure preserves original and reports failed index status; use reindex to recover.')
route('post','/knowledge/{id}/reindex','Build a new embedding generation','operator',response=ref('Document'),desc='Old successful generation remains until new build commits. Current embedding identity controls retrieval.')
route('post','/knowledge/search','Search application and global knowledge',body=obj({'application_id':string(),'query':string(),'limit':integer(minimum=1,maximum=20)},['application_id','query']),response=obj({'items':arr(ref('KnowledgeHit')),'embedding':obj(fieldnames('provider model'))}),desc='Filters scope, provider, model and dimensions before scoring. Citations include document version, chunk and generation.')
route('get','/investigations/{id}/action-plans','List immutable action plans',response=listed('ActionPlan'))
route('post','/investigations/{id}/action-plans','Propose rollback to an explicit owned revision','operator',obj(fieldnames('target_revision'),['target_revision']),ref('ActionPlan'))
route('get','/action-plans/{id}','Review target, digests and approval',response=ref('ActionPlan'))
route('post','/action-plans/{id}/approve','Explicitly approve exact digest','operator',obj(fieldnames('digest'),['digest']),ref('ActionPlan'),desc='Self-approval is permitted for operators/admins. Repeated approval does not duplicate authorization or audit.')
route('post','/action-plans/{id}/execute','Queue approved plan exactly once','operator',response=ref('Execution'),desc='One execution per immutable plan. Drift requires new plan/approval. Ambiguous outcome is reconciled without blind write retry.')
route('get','/executions/{id}','Get execution and recovery evidence',response=ref('Execution'),desc='succeeded requires 180 seconds of traffic, healthy probes and converged approved Deployment; command success alone is insufficient.')
route('get','/action-plans/{id}/execution','Find the existing execution without queueing work',response=ref('Execution'),desc='Read-only lookup for refresh/resume. Returns 404 if the plan has no execution; never authorizes or queues a write.')

route('get','/branches/{id}/revisions','List immutable context versions',response=listed('ContextRevision'))
route('get','/branches/{id}/revisions/{revision}','Read context snapshot, provenance and referenced evidence',response=ref('ContextRevision'))
route('post','/action-plans/{id}/reject','Reject a pending immutable proposal','operator',obj(fieldnames('digest reason'),['digest','reason']),ref('ActionPlan'),desc='Terminal decision for this plan; repeated same decision is idempotent. No external write.')
route('post','/action-plans/{id}/revoke','Revoke approval before execution is queued','operator',obj(fieldnames('digest reason'),['digest','reason']),ref('ActionPlan'),desc='Rejected if an execution record already exists. Revocation cannot undo queued or completed effects.')
route('post','/investigations/{id}/close','Close after successful recovery and save report','operator',obj(fieldnames('execution_id summary'),['execution_id']),ref('InvestigationReport'),desc='Requires the latest completed action to have succeeded and no active inference or action execution in this investigation. Same execution replay returns immutable report. Closed investigations reject new branch/context mutations.')
route('get','/investigations/{id}/report','Read immutable closure evidence',response=ref('InvestigationReport'))
route('get','/investigations/{id}/knowledge-drafts','List unconfirmed and confirmed experience drafts',response=listed('KnowledgeDraft'))
route('post','/investigations/{id}/knowledge-drafts','Draft experience with scoped evidence','operator',obj({'title':string(),'body':string(),'evidence_ids':{'type':'array','items':string(),'minItems':1,'maxItems':100,'uniqueItems':True}},['title','body','evidence_ids']),ref('KnowledgeDraft'),desc='Evidence IDs must belong to this investigation; drafts are not searchable knowledge.')
route('post','/knowledge-drafts/{id}/confirm','Confirm closed investigation experience','operator',response=ref('Document'),desc='Requires closed investigation. Atomically creates one source document; retries return same document. Index failure keeps source for explicit reindex.')
route('get','/applications/{id}/telemetry','List configured telemetry queries',response=obj({'metric_query_ids':arr(string()),'logs_configured':{'type':'boolean'}}),desc='Routes are available when AIOPS_TELEMETRY_CONFIG is configured; no credentials are returned.')
route('post','/applications/{id}/metrics','Query registered application metrics','operator',obj({'query_id':string(),'start':string(format='date-time'),'end':string(format='date-time'),'step_seconds':integer()},['query_id']),ref('MetricEvidence'),desc='Server-registered query IDs only, no arbitrary PromQL. Defaults up to last15m and 15s, constrained by registered window/sample limits; explicit time and response limits enforced. Empty is not healthy proof.')
route('post','/applications/{id}/logs','Query scoped application logs','operator',obj({'start':string(format='date-time'),'end':string(format='date-time'),'limit':integer()}),ref('LogEvidence'),desc='Registered exact Loki labels; cannot override LogQL/endpoint/scope. Default last15m/100 lines; empty is not healthy proof.')

spec={'openapi':'3.1.0','info':{'title':'AIOps Canvas Backend API','version':'0.2.0','description':'Single-workspace backend contract. Frontend implementation is deferred. All business history is server authoritative.','license':{'name':'Apache-2.0','identifier':'Apache-2.0'}},'servers':[{'url':'/api/v1'}],'security':[{'sessionCookie':[]}],'paths':P,'components':{'schemas':S,'securitySchemes':{'sessionCookie':{'type':'apiKey','in':'cookie','name':'aiops_session'}}}}
Path('internal/apicontract/openapi.json').write_text(json.dumps(spec,ensure_ascii=False,indent=2)+'\n')
print(f'Generated {len(P)} paths, {sum(len(x) for x in P.values())} operations, {len(S)} schemas.')
