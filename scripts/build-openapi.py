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

# Approved multi-application SRE workspace contract (WAN-74 through WAN-79).
boolean={'type':'boolean'}
def body_at(path,method,schema): P[path][method]['requestBody']={'required':True,'content':{'application/json':{'schema':schema}}}
def query_env(path,method): P[path][method].setdefault('parameters',[]).append({'name':'environment_id','in':'query','schema':string()})
S['Binding']=obj(fieldnames('kind connector_id resource'),['kind','connector_id','resource'])
S['Environment']=obj({**fieldnames('id application_id key name'),'archived':boolean,'binding_revision':integer(),'bindings':arr(ref('Binding'))})
S['Application']['properties'].update({**fieldnames('key owner_team_id maintainer_id repository_url runbook_url'),'tags':arr(string()),'capabilities':arr(string()),'archived':boolean,'environments':arr(ref('Environment'))})
S['Team']=obj(fieldnames('id name description'))
S['Grant']=obj({'subject_type':string(enum=['user','team']),'subject_id':string(),'permission':string(enum=['view','operate'])},['subject_type','subject_id','permission'])
S['Model']['properties'].update({'context_window':integer(),'input_budget':integer()})
S['Investigation']['properties']['environment_id']=string()
S['DeploymentSnapshot']['properties'].update({**fieldnames('environment_id connector_id'),'binding_revision':integer()})
S['RollbackProposal']['properties'].update({**fieldnames('environment_id connector_id configuration_digest'),'binding_revision':integer()})
app_fields={k:v for k,v in S['Application']['properties'].items() if k not in ['id','archived','environments','capabilities']}
body_at('/applications','post',obj(app_fields,['name']))
body_at('/investigations','post',obj(fieldnames('application_id environment_id title'),['application_id','title']))
route('get','/applications/{id}','Read authorized application',response=ref('Application'))
route('patch','/applications/{id}','Update application metadata','admin',obj({k:v for k,v in app_fields.items() if k!='environment'}),ref('Application'))
route('post','/applications/{id}/archive','Archive or restore without deleting history','admin',obj({'archived':boolean},['archived']),ref('Application'))
route('get','/applications/{id}/environments','List application environments',response=listed('Environment'))
route('post','/applications/{id}/environments','Create registered environment','admin',ref('Environment'),ref('Environment'))
route('patch','/environments/{id}','Update environment with binding revision','admin',ref('Environment'),ref('Environment'),desc='Full environment update; binding_revision is optimistic concurrency token. Secrets stay in server registration.')
route('get','/teams','List teams','admin',response=listed('Team'))
route('post','/teams','Create team','admin',ref('Team'),ref('Team'))
route('patch','/teams/{id}','Update team','admin',ref('Team'),ref('Team'))
route('get','/teams/{id}/members','Read team member IDs','admin',response=obj({'items':arr(string())}))
route('put','/teams/{id}/members','Replace team members','admin',obj({'user_ids':arr(string())},['user_ids']),obj({'items':arr(string())}))
route('get','/applications/{id}/grants','Read explicit application grants','admin',response=listed('Grant'))
route('put','/applications/{id}/grants','Replace application grants','admin',listed('Grant'),listed('Grant'),desc='Workspace role caps grants; owner fields do not confer permissions.')
route('get','/audit','Read sanitized management audit','admin',response=obj({'items':arr({'type':'object','additionalProperties':True})}))
route('get','/connector-registrations','Read safe server-managed binding options','admin',response=obj({'items':arr(obj(fieldnames('id name kind resource')))}))
for path in ['/applications/{id}/deployment','/applications/{id}/telemetry']: query_env(path,'get')
for path in ['/applications/{id}/metrics','/applications/{id}/logs']:
    P[path]['post']['requestBody']['content']['application/json']['schema']['properties']['environment_id']=string()
route('post','/investigations/{id}/evidence','Pin historical query snapshot','operator',obj(fieldnames('branch_id environment_id kind title source content'),['branch_id','environment_id','kind','title','content']),ref('Evidence'),201,desc='Human-pinned, not a fresh measurement. Source/time/query remain in immutable content. Same environment and idle branch required.')
S['ContextManifest']=obj({**fieldnames('run_id attempt_id model_id estimation summary'),'context_revision':integer(),'input_budget':integer(),'estimated_tokens':integer(),'summary_calls':integer(),'summary_source_ids':arr(string()),'recent_message_ids':arr(string()),'evidence_ids':arr(string()),'messages':arr(obj(fieldnames('role content'))),'created_at':string(format='date-time')})
route('get','/runs/{id}/context','Read actual prepared attempt input',response=ref('ContextManifest'),desc='Derived summary does not replace raw history; estimates are conservative UTF-8 byte bounds, not exact provider token usage. 404 means no prepared context yet.')
S['KnowledgeMetadata']=obj({**fieldnames('maintainer_id team_id origin actor_id generation_run_id generation_model visibility'),**{k:arr(string()) for k in ['evidence_ids','environment_ids','resource_categories','tags','shared_application_ids']}})
S['Document']['properties'].update({'lifecycle':string(enum=['draft','review','published','archived']),'metadata':ref('KnowledgeMetadata'),'updated_at':string(format='date-time')})
S['KnowledgeHit']['properties'].update({'retrieval':string(),'score':{'type':'number'}})
S['KnowledgeDraft']['properties'].update(fieldnames('origin generation_run_id generation_model'))
body_at('/knowledge','post',obj({**fieldnames('application_id title body source'),'version':integer(),'metadata':ref('KnowledgeMetadata')},['title','body']))
P['/knowledge']['post']['summary']='Create reviewable Runbook draft'
P['/knowledge']['post']['description']='Operator with application permission; workspace sharing requires administrator. Draft creation never publishes knowledge or grants access.'
P['/knowledge/search']['post']['requestBody']['content']['application/json']['schema']['properties']['environment_id']=string()
P['/knowledge/search']['post']['responses']['200']['content']['application/json']['schema']=obj({'items':arr(ref('KnowledgeHit')),'mode':string(),'degraded':boolean,'embedding':obj(fieldnames('provider model'))})
P['/knowledge/search']['post']['description']='Filter current ACL, publication state and environment before keyword/vector fusion. Embedding failure returns explicitly marked keyword fallback.'
route('get','/knowledge/templates','List editable Runbook templates',response=obj({'items':arr(obj(fieldnames('id title body')))}))
route('put','/knowledge/{id}','Save immutable Runbook revision','operator',obj({'expected_version':integer(),**fieldnames('title body source'),'metadata':ref('KnowledgeMetadata')},['expected_version','title','body']),ref('Document'))
route('post','/knowledge/{id}/transition','Review, publish or archive knowledge','operator',obj({'expected_version':integer(),'state':string(enum=['draft','review','published','archived'])},['expected_version','state']),ref('Document'))
route('get','/knowledge/{id}/revisions','List readable immutable document revisions',response=listed('Document'))
route('get','/knowledge/{id}/revisions/{version}','Read a cited immutable version',response=ref('Document'))
route('get','/knowledge/{id}/export','Export authorized source revisions and metadata',response=obj({'format':string(),'document_id':string(),'exported_at':string(format='date-time'),'document':ref('Document'),'revisions':arr(ref('Document'))}))
route('post','/investigations/{id}/knowledge-drafts/generate','Generate experience draft after verified closure','operator',response=ref('KnowledgeDraft'),desc='Uses bounded configured model with committed evidence; human review and publication remain required.')
S['Incident']=obj({**fieldnames('id source_id application_id environment_id fingerprint title severity status alert_status assignee_id investigation_id summary'),'suspected':boolean,'starts_at':string(format='date-time'),'ends_at':{'type':['string','null'],'format':'date-time'},'updated_at':string(format='date-time')})
S['Policy']=obj({**fieldnames('environment_id model_id actor_id'),**{k:boolean for k in ['auto_initial','patrol','generate_experience_draft','inherit']},**{k:integer() for k in ['interval_minutes','max_rounds','max_tool_calls','timeout_seconds','daily_budget','version']}})
S['AutomationRun']=obj({**fieldnames('id environment_id kind status incident_id investigation_id run_id error'),'created_at':string(format='date-time'),'finished_at':{'type':['string','null'],'format':'date-time'}})
S['AutomationRun']['properties']['result']={'type':'object','additionalProperties':True}
S['Notification']=obj({**fieldnames('id incident_id application_id environment_id channel status last_error'),'attempts':integer(),'next_attempt_at':string(format='date-time'),'created_at':string(format='date-time')})
S['Todo']=obj({**fieldnames('id application_id environment_id investigation_id title status kind'),'updated_at':string(format='date-time')})
route('get','/incidents','List authorized incoming abnormalities',response=listed('Incident'))
for name in ['application_id','environment_id']: P['/incidents']['get'].setdefault('parameters',[]).append({'name':name,'in':'query','schema':string()})
route('get','/incidents/{id}','Read authorized incident',response=ref('Incident'))
route('post','/incidents/{id}/claim','Claim or release incident','operator',obj(fieldnames('assignee_id'),['assignee_id']),ref('Incident'))
route('post','/incidents/{id}/investigation','Create or reuse incident investigation','operator',obj(fieldnames('model_id')),ref('Incident'),desc='Omit model_id for manual investigation without a model call.')
route('get','/notifications','Read scoped delivery status',response=listed('Notification'))
route('get','/automation/policies/{id}','Read effective environment policy',response=ref('Policy'),desc='Use workspace for administrator-only defaults. Background model calls default off.')
route('put','/automation/policies/{id}','Set versioned automation policy','admin',obj({**S['Policy']['properties'],'expected_version':integer()},['expected_version']),ref('Policy'))
route('get','/automation/runs','Read scoped bounded background tasks',response=listed('AutomationRun'))
query_env('/automation/runs','get')
route('post','/automation/runs/{id}/cancel','Cancel background work','operator',response=obj({'cancelled':boolean}))
route('get','/dashboard','Read workspace attention and derived todos',response=obj({'incidents':arr(ref('Incident')),'pending_approvals':arr(ref('Todo')),'executions':arr(ref('Todo')),'my_items':arr(ref('Todo')),'automation_runs':arr(ref('AutomationRun')),'notification_failures':arr(ref('Notification')),'data_sources':arr(ref('Integration'))}))
route('post','/webhooks/alertmanager/{source}','Accept authenticated Alertmanager envelope','public',{'type':'object','additionalProperties':True},obj({'accepted':integer()}),202,desc='Requires dedicated Bearer source token; server-side mappings bind scope. Deduplicates source/fingerprint/startsAt, late firing cannot undo resolved state.')
P['/webhooks/alertmanager/{source}']['post']['security']=[{'alertSourceToken':[]}]

# Member administration and richer catalog (WAN-80 / WAN-81).
S['Member']=obj({**S['User']['properties'],**fieldnames('department job_title source external_id'),'deleted_at':{'type':['string','null'],'format':'date-time'}})
P['/users']['get']['responses']['200']['content']['application/json']['schema']=listed('Member')
P['/users']['get']['parameters']=[{'name':'include_deleted','in':'query','schema':boolean}]
for path,method in [('/users','post'),('/users/{id}','patch')]:
    P[path][method]['requestBody']['content']['application/json']['schema']['properties'].update(fieldnames('department job_title'))
    P[path][method]['responses']['201' if method=='post' else '200']['content']['application/json']['schema']=obj({'user':ref('Member')})
P['/users/{id}']['patch']['requestBody']['content']['application/json']['schema']['properties']['email']=string(format='email')
route('get','/users/{id}','Read member details','admin',response=obj({'user':ref('Member')}))
route('delete','/users/{id}','Remove member while retaining historical identity','admin',status=204,desc='Soft-delete and disable account, revoke sessions. Last enabled administrator is protected. No destructive deletion of investigation or audit history.')
S['MemberImportRow']=obj(fieldnames('external_id email name department job_title'),['external_id','email','name'])
S['MemberImportPreview']=obj({'fingerprint':string(),'can_import':boolean,'items':arr(obj({'external_id':string(),'email':string(),'action':string(enum=['create','update','conflict']),'reason':string()}))})
member_import_fields={'source':string(pattern='^[A-Za-z0-9._-]{1,100}$'),'rows':{'type':'array','items':ref('MemberImportRow'),'minItems':1,'maxItems':200}}
route('post','/users/import/preview','Preview enterprise directory import','admin',obj(member_import_fields,['source','rows']),ref('MemberImportPreview'),desc='Administrator-supplied exported directory data; no remote IAM requests or SSO. Email collisions do not take over local identities.')
route('post','/users/import','Confirm unchanged directory import','admin',obj({**member_import_fields,'fingerprint':string()},['source','rows','fingerprint']),obj({'created':integer(),'updated':integer()}),desc='Recomputes preview and commits atomically. New entries are disabled viewers without a usable password; current roles and app grants are not imported.')
S['ApplicationProfile']=obj(fieldnames('business_unit service_type lifecycle criticality language framework default_branch build_command image_repository documentation_url'))
S['Application']['properties'].update({'profile':ref('ApplicationProfile'),'maintainer_name':string(readOnly=True),'owner_team_name':string(readOnly=True)})
for path,method in [('/applications','post'),('/applications/{id}','patch')]:
    P[path][method]['requestBody']['content']['application/json']['schema']['properties']['profile']=ref('ApplicationProfile')
configuration_limits={'replicas':(0,10000),'cpu_request_millicores':(0,1000000),'cpu_limit_millicores':(1,1000000),'memory_request_mib':(0,1048576),'memory_limit_mib':(1,1048576),'port':(1,65535),'connection_pool_max':(1,1000000),'request_timeout_ms':(1,3600000),'rate_limit_rps':(0,100000000),'retries':(0,100),'circuit_breaker_percent':(0,100)}
S['EnvironmentConfiguration']=obj({k:{'type':['integer','null'],'minimum':bounds[0],'maximum':bounds[1]} for k,bounds in configuration_limits.items()})
S['Environment']['properties'].update({'configuration':ref('EnvironmentConfiguration'),'configuration_revision':integer(),'capabilities':arr(string())})
# Capabilities describe a registered environment, not application catalog metadata.
S['Application']['properties'].pop('capabilities',None)
P['/environments/{id}']['patch']['description']+=' Configuration is catalog metadata, not applied infrastructure state. configuration_revision is independently checked when configuration is submitted; metadata-only changes do not redirect approved bindings.'

spec={'openapi':'3.1.0','info':{'title':'AIOps Canvas Backend API','version':'0.4.0','description':'Single-workspace multi-application SRE API. All business history is server authoritative; application authorization is enforced beyond minimum workspace role.','license':{'name':'Apache-2.0','identifier':'Apache-2.0'}},'servers':[{'url':'/api/v1'}],'security':[{'sessionCookie':[]}],'paths':P,'components':{'schemas':S,'securitySchemes':{'sessionCookie':{'type':'apiKey','in':'cookie','name':'aiops_session'},'alertSourceToken':{'type':'http','scheme':'bearer'}}}}
Path('internal/apicontract/openapi.json').write_text(json.dumps(spec,ensure_ascii=False,indent=2)+'\n')
print(f'Generated {len(P)} paths, {sum(len(x) for x in P.values())} operations, {len(S)} schemas.')
