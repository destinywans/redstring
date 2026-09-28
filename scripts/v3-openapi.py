"""V3 operations backed by current handlers; types.ts defines public DTO fields.

This file intentionally omits mock-only routes. Reusing a path replaces its
pre-V3 operation because the V3 instance serves the new semantics there.
"""

def v3_object(names, extra=None, required=None):
    properties = fieldnames(names)
    properties.update(extra or {})
    return obj(properties, required)

def v3_bool(): return {'type': 'boolean'}
def v3_num(): return {'type': 'number'}
def v3_map(value): return {'type': 'object', 'additionalProperties': value}
def v3_query(name, schema=None): return {'name': name, 'in': 'query', 'schema': schema or string()}
def v3_route(method, path, summary, *, body=None, response=None, status=200, query=None, role='viewer', desc=''):
    route(method, path, summary, role, body, response, status,
          desc='V3 instance and environment scope apply. ' + desc, query=query)
    if role != 'public' and path != '/conversations/{id}/events':
        P[path][method].setdefault('parameters', []).append({
            'name': 'X-AIOps-Environment', 'in': 'header', 'schema': string(),
            'description': 'Selected environment; all is a read-only non-production aggregate.'})

# Identity and personal settings. Server returns exact data; unavailable MFA
# features are described as unavailable in the profile rather than simulated.
S['V3Preferences'] = obj({'theme': string(enum=['light','dark','system']),
    'locale': string(enum=['zh-CN','en-US']), 'density': string(enum=['comfortable','compact']),
    'reduce_motion': v3_bool()}, ['theme','locale','density','reduce_motion'])
S['V3PreferencesPatch'] = obj(S['V3Preferences']['properties'])
S['V3Me'] = v3_object('id name email phone title team department timezone', {
    'avatar_url': string(), 'oncall': obj({'active': v3_bool(), 'until': string(), 'schedule': string()}, ['active']),
    'roles': arr(string()), 'mfa': obj({'totp': v3_bool(), 'passkeys': integer()}, ['totp','passkeys']),
    'preferences': ref('V3Preferences')},
    ['id','name','email','phone','title','team','department','timezone','oncall','roles','mfa','preferences'])
S['V3Workspace'] = v3_object('name slug timezone locale week_start data_region', {
    'retention_days': integer(), 'brand': obj({'name': string(), 'logo': {'type':['string','null']}, 'pending': v3_bool()}, ['name','logo','pending'])},
    ['name','slug','timezone','locale','week_start','retention_days','data_region','brand'])
S['V3EnvironmentInfo'] = v3_object('id label note', {
    'kind': string(enum=['standing','feature']), 'tone': string(enum=['prod','stg','dev']), 'clusters': arr(string())},
    ['id','label','kind','tone','clusters'])
S['V3InstanceInfo'] = v3_object('name domain default_env', {
    'tier': string(enum=['production','nonprod']), 'environments': arr(ref('V3EnvironmentInfo')),
    'other': v3_object('label url domain')}, ['tier','name','domain','environments','default_env'])
S['V3LowerSource'] = v3_object('id name brand env env_label purpose credential',
    required=['id','name','brand','env','env_label','purpose','credential'])
S['V3ProfileView'] = v3_object('name display_name department title timezone', {
    'contacts': arr(obj({'kind': string(enum=['email','phone','feishu']), **fieldnames('label value'), 'verified': v3_bool(), 'note': string()})),
    'notifications': arr(obj({'event': string(), 'label': string(), 'channels': obj({k:v3_bool() for k in ['feishu','email','sms','phone']})})),
    'security': obj({'password_changed': string(), 'mfa_enabled': v3_bool(), 'org_rule': string(),
        'factors': arr(v3_object('kind name sub')), 'sessions': arr(obj({**fieldnames('id device sub'), 'current': v3_bool()})),
        'tokens': string()})})
S['V3ModelChoice'] = obj({'model': string(), 'reasoning': string()}, ['model'])
S['V3ModelInfo'] = v3_object('id name provider brand connector cost note unavailable_reason', {
    'api': string(enum=['responses','chat_completions','messages']),
    'reasoning': obj({'kind': string(enum=['levels','toggle','none']),
        'levels': arr(v3_object('id label hint')), 'default': string()}),
    'tools': {'type':'boolean','description':'True only when this configured model is explicitly enabled for V3 read tools by the server operator; not proof of successful remote function calling.'},
    'vision': v3_bool(), 'context_k': integer(), 'available': v3_bool()})
S['V3ModelCatalog'] = obj({'items': arr(ref('V3ModelInfo')), 'default': ref('V3ModelChoice')}, ['items','default'])
S['V3Upload'] = obj({'id':string(),'kind':string(enum=['file','image','link','paste']),
    'name':string(),'size':integer(minimum=0),'mime':string(),'url':string()},
    ['id','kind','name','size','mime'])
S['V3AIPreferences'] = obj({'default': ref('V3ModelChoice'), 'canvas_inherit': v3_bool(),
    'show_reasoning': string(enum=['expanded','collapsed','hidden']), 'auto_context': v3_bool(),
    'memory': v3_bool(), 'budget_warn_tokens': integer()},
    ['default','canvas_inherit','show_reasoning','auto_context','memory','budget_warn_tokens'])
S['V3NotificationSettings'] = obj({'quiet': obj({'enabled': v3_bool(), 'from': string(), 'to': string()}),
    'digest': string(enum=['off','daily','weekly']), 'subscriptions': arr(v3_object('id label sub', {'on': v3_bool()}))})
v3_route('get','/instance','Read the configured V3 isolation boundary',role='public',response=ref('V3InstanceInfo'))
v3_route('get','/integrations/lower-sources','List configured lower-environment source metadata',
    response=listed('V3LowerSource'),
    desc='Requires connectors.view in the production environment. Only available on the production instance; non-production returns 404 after route authorization. The credential field reports that a Vault reference is configured, not that connectivity or read-only source credentials have been verified. Source endpoints and Vault references are never returned.')
P['/integrations/lower-sources']['get']['responses']['404'] = {
    'description':'Not available on a non-production instance',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/me','Read current account',response=ref('V3Me'))
v3_route('patch','/me','Update current account',body=v3_object('name phone title timezone'),response=ref('V3Me'))
v3_route('patch','/me/preferences','Update visual and language preferences',body=ref('V3PreferencesPatch'),response=ref('V3Preferences'))
v3_route('get','/me/profile','Read personal profile',response=ref('V3ProfileView'))
v3_route('put','/me/profile','Update personal profile',body=ref('V3ProfileView'),response=ref('V3ProfileView'))
v3_route('delete','/me/sessions/{id}','Revoke one account session',status=204)
v3_route('get','/me/ai-preferences','Read personal AI preferences',response=ref('V3AIPreferences'))
v3_route('put','/me/ai-preferences','Update personal AI preferences',body=ref('V3AIPreferences'),response=ref('V3AIPreferences'))
v3_route('get','/me/notification-settings','Read personal notification settings',response=ref('V3NotificationSettings'))
v3_route('put','/me/notification-settings','Update personal notification settings',body=ref('V3NotificationSettings'),response=ref('V3NotificationSettings'))
v3_route('get','/workspace','Read workspace settings',response=ref('V3Workspace'))
v3_route('put','/workspace','Update workspace settings',role='admin',body=ref('V3Workspace'),response=ref('V3Workspace'))
v3_route('get','/models','List configured inference models',response=ref('V3ModelCatalog'),
    desc='available means configured, not remotely health-checked. tools reflects the operator-enabled V3 read-tool allowlist rather than provider-wide capability; remote function calling is not attested by this response. Native Ollama returns 503 model_catalog_unavailable until the client ModelInfo.api enum can represent it.')
for _path, _methods in {
    '/me': ['get', 'patch'],
    '/me/profile': ['get', 'put'],
}.items():
    for _method in _methods:
        P[_path][_method]['description'] += ' Currently returns 503 identity_evidence_unavailable until authoritative MFA and on-call sources are connected; writes are not partially applied.'
v3_route('post','/uploads','Store an inert conversation attachment',
    body=obj({'kind':string(enum=['link','paste']),'name':string(),
        'url':string(),'text':string(),'size':integer(),'mime':string()},['kind','name']),
    response=ref('V3Upload'),status=201,
    desc='An explicit single X-AIOps-Environment header is required, including in production. JSON supports link or paste; file and image bytes require multipart. Maximum 20 MiB.')
P['/uploads']['post']['requestBody']['content']['multipart/form-data'] = {'schema':obj({
    'kind':string(enum=['file','image']),
    'file':{'type':'string','format':'binary'}},['kind','file'])}
for upload_status, upload_description in [('413','Attachment too large'),('415','Unsupported media type')]:
    P['/uploads']['post']['responses'][upload_status] = {'description':upload_description,
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/uploads/{id}/content','Read an owned uploaded file or image',
    desc='Browser reads may omit X-AIOps-Environment; the handler resolves the saved environment and rechecks the current conversation.view grant. Explicit environments must match the saved environment. Files download as opaque octet-stream; verified PNG, JPEG, GIF and WebP images may render inline. No cache; invalid stored images fail closed.')
P['/uploads/{id}/content']['get']['responses']['200'] = {
    'description':'Owned attachment bytes',
    'content':{'application/octet-stream':{'schema':{'type':'string','format':'binary'}},
               'image/png':{'schema':{'type':'string','format':'binary'}},
               'image/jpeg':{'schema':{'type':'string','format':'binary'}},
               'image/gif':{'schema':{'type':'string','format':'binary'}},
               'image/webp':{'schema':{'type':'string','format':'binary'}}}}

# Durable conversations; complete assistant parts are persisted before the
# completion event is published. Part variants follow the types.ts tagged union.
def v3_part(kind, fields, required=None):
    return obj({'type': string(enum=[kind]), **fields}, ['type'] + (required or []))
S['V3Part'] = {'oneOf': [
    v3_part('text', {'markdown': string()}, ['markdown']),
    v3_part('tool_calls', {'summary': string(), 'calls': arr(obj({**fieldnames('id connector tool args status'),
        'duration_ms': v3_num()})), 'tokens': integer(), 'duration_s': v3_num()},
        ['summary','calls','tokens','duration_s']),
    v3_part('chart', {'title': string(), 'unit': string(), 'labels': arr(string()),
        'series': arr(obj({'name': string(), 'values': arr(v3_num())})), 'threshold': v3_num(),
        'markers': arr(obj({'at': integer(), 'label': string(), 'tone': string()})),
        'source': string(), 'deep_link': string()}, ['title','unit','labels','series']),
    v3_part('table', {'title': string(), 'columns': arr(string()),
        'rows': arr(arr({'type': ['string','number']})), 'filter': string()}, ['title','columns','rows']),
    v3_part('image', {'title': string(), 'caption': string(), 'alt': string(),
        'diagram': string(enum=['call_graph'])}, ['title','caption','alt','diagram']),
    v3_part('files', {'files': arr(obj({**fieldnames('id name size preview'),
        'kind': string(enum=['md','csv','yaml','log','pdf'])}))}, ['files']),
    v3_part('diff', {**fieldnames('path from to'),'left': arr(string()),'right': arr(string())},
        ['path','from','to','left','right']),
    v3_part('topology', fieldnames('caption href'), ['caption','href']),
    v3_part('proposal', {**fieldnames('id title target detail'),
        'risk': string(enum=['low','medium','high']), 'reversible': v3_bool()},
        ['id','title','risk','reversible','target','detail']),
    v3_part('form', {'title': string(), 'fields': arr(obj({**fieldnames('id label'),
        'kind': string(enum=['radio','select','check']), 'options': arr(string()),
        'value': {'type': ['string','boolean']}}))}, ['title','fields']),
    v3_part('tasks', {'title': string(), 'items': arr(obj({'label': string(),
        'state': string(enum=['done','running','todo']), 'meta': string()}))}, ['title','items']),
    v3_part('citations', {'items': arr(obj({'n': integer(),'label': string(),
        'ref': v3_object('kind id label')}))}, ['items']),
    v3_part('data_gap', fieldnames('text href'), ['text','href']),
    v3_part('next_steps', {'items': arr(v3_object('icon label action'))}, ['items']),
    v3_part('status', fieldnames('text detail'), ['text']),
    v3_part('artifacts', {'ids': arr(string())}, ['ids']),
]}
S['V3Message'] = obj({'id': string(), 'role': string(enum=['user','assistant']), 'author': string(),
    'created_at': string(format='date-time'), 'parts': arr(ref('V3Part')),
    'run': {'type':'object','additionalProperties':True}, 'attachments': arr({'type':'object','additionalProperties':True}),
    'streaming': v3_bool()}, ['id','role','author','created_at','parts'])
S['V3ConversationSummary'] = v3_object('id title scope size updated_at incident_id', {
    'mode': string(enum=['chat','canvas']), 'status': string(enum=['active','waiting_approval','resolved','archived']),
    'source': string(enum=['manual','alert_triage','schedule','im']),
    'participants': arr(string()), 'tokens': integer()})
S['V3Conversation'] = obj({'id': string(), 'title': string(), 'mode': string(enum=['chat','canvas']),
    'status': string(enum=['active','waiting_approval','resolved','archived']),
    'scope': obj({'service': string(), 'env': string(), 'inferred': v3_bool()}),
    'investigation_id': string(), 'messages': arr(ref('V3Message')),
    'artifacts': arr({'type':'object','additionalProperties':True}),
    'side': obj({'connectors': arr(v3_object('name', {'calls': integer()})),
        'context': obj({'used': integer(), 'budget': integer(), 'parts': arr(v3_object('label color', {'tokens': integer()}))}),
        'recalled': arr(v3_object('kind title', {'score': v3_num()}))})})
S['V3ConversationList'] = obj({'items': arr(ref('V3ConversationSummary')),
    'total': integer(), 'page': integer(), 'page_size': integer(),
    'stats': arr({'type':'object','additionalProperties':True}),
    'counts': obj({k:integer() for k in ['mine','team','triage','archived']})})
v3_route('get','/conversations','List scoped conversations',response=ref('V3ConversationList'),
    query=[v3_query(x) for x in ['filter','q','tab','status','mode','page','page_size']])
v3_route('post','/conversations','Create conversation',body=obj({'text':string(minLength=1),'context':{'type':['string','null']},'model':{'type':['string','null']}},['text']),
    response=obj({'id':string()},['id']),status=201,
    desc='text must contain non-whitespace and fit within 2 MiB UTF-8; context is limited to 4096 bytes and model to 256 bytes. The selected default model and creation audit are persisted. The first message is submitted separately.')
v3_route('get','/conversations/{id}','Read conversation and saved messages',response=ref('V3Conversation'))
v3_route('post','/conversations/{id}/messages','Start a durable assistant reply',
    body=obj({'text':string(),'model':string(),'reasoning':string(),
        'attachments':arr({'type':'object','additionalProperties':True})},['text']),
    response=obj({'message':ref('V3Message'),'reply_id':string()},['message','reply_id']),status=202,
    desc='A second concurrent reply is rejected with 409. The user message is durable before 202.')
v3_route('get','/conversations/{id}/events','Resume ordered assistant events',
    query=[{'name':'Last-Event-ID','in':'header','schema':string()}],
    desc='SSE sequence IDs support reconnection. message.completed follows durable persistence.')

# General canvases are persisted separately from investigation branches.
# Until the investigation projection exists, the list flags that kind as
# incomplete and requests for it return 503, never an invented zero count.
S['V3CanvasSummary'] = obj({**fieldnames('id title kind status meta updated_at href'),
    'participants':arr(string()),'services':arr(string()),
    'thumb':obj({'nodes':arr(arr({})), 'edges':arr(string())})},
    ['id','title','kind','status','meta','participants','updated_at','href','services','thumb'])
S['V3CanvasTemplate'] = obj({**fieldnames('id kind name desc nodes'),
    'is_default':v3_bool()},['id','kind','name','desc','nodes'])
S['V3CanvasDoc'] = obj({**fieldnames('id title kind status note env'),
    'participants':arr(string()),'nodes':arr({'type':'object','additionalProperties':True}),
    'edges':arr({'type':'object','additionalProperties':True}),
    'artifacts':arr({'type':'object','additionalProperties':True}),
    'threads':v3_map({'type':'object','additionalProperties':True})},
    ['id','title','kind','status','participants','note','nodes','edges','artifacts','threads'])
v3_route('get','/canvases','List owner-scoped general canvases',
    response=obj({'items':arr(ref('V3CanvasSummary')),'counts':v3_map(integer()),
      'total':integer(),'page':integer(),'page_size':integer(),'incomplete_kinds':arr(string())}),
    query=[v3_query(x) for x in ['kind','q','page','page_size']],
    desc='Investigation kind is not projected and returns 503; counts exclude investigation canvases.')
v3_route('post','/canvases','Create a persisted general canvas',
    body=obj({**fieldnames('title kind template_id goal'),
      'services':arr(string()),'ai_draft':v3_bool()},['title','kind']),
    response=obj(fieldnames('id href'),['id','href']),status=201,
    desc='Investigation creation and AI drafting currently return 503; selected services must be mapped to the request environment.')
v3_route('get','/canvases/{id}','Read owner-scoped general canvas',response=ref('V3CanvasDoc'))
v3_route('get','/canvas-templates','List available canvas structures',
    response=obj({'items':arr(ref('V3CanvasTemplate'))},['items']))
v3_route('post','/canvas-templates','Save a safe canvas structure template',
    body=obj({'from_canvas':string(),'name':string(),
      'keep':obj({k:v3_bool() for k in ['nodes','steps','runbooks','content']})},
      ['from_canvas','name']),response=ref('V3CanvasTemplate'),status=201,
    desc='Only structure is accepted; steps, Runbooks and content return 422.')
P['/conversations/{id}/events']['get']['responses']['200']['content'] = {'text/event-stream':{'schema':string()}}

# Search and @mentions use persisted, scoped service, incident and conversation
# records. There is no persisted recent list or executable command registry.
S['V3SearchHit'] = obj({**fieldnames('kind id title subtitle status href')})
S['V3MentionCandidate'] = obj({**fieldnames('kind id label meta status')})
S['V3MentionPreviewPartial'] = obj({**fieldnames('kind id title subtitle status_label href'),
    'tier':string(enum=['T0','T1','T2']),
    'status':string(enum=['neutral','accent','ok','warn','danger','info']),
    'metrics':arr(obj({**fieldnames('label value tone'),'series':arr(v3_num())},
        ['label','value','series'])),
    'change':obj(fieldnames('brand text meta'), ['brand','text','meta']),
    'related':arr(obj(fieldnames('label tone'), ['label']))},
    ['kind','id','title','subtitle','metrics','related'])
v3_route('get','/search','Search available persisted records',
    response=obj({'query':string(),'hits':arr(ref('V3SearchHit')),'recent':arr(ref('V3SearchHit'))}),
    query=[v3_query('q')],
    desc='Partial: only scoped services, incidents and own conversations are indexed; recent is always empty until user navigation history exists.')
v3_route('get','/mentions','List available mention candidates',
    response=obj({'items':arr(ref('V3MentionCandidate'))}),
    query=[v3_query('q'),v3_query('kind',string(enum=['service','incident','conversation']))],
    desc='Partial: only scoped service, incident and conversation references are supported.')
v3_route('get','/mentions/{kind}/{id}/preview','Preview an accessible reference',
    response=ref('V3MentionPreviewPartial'),
    desc='Partial: only service, incident and conversation records have verified previews; other kinds return 503 preview_unavailable. Metrics and related records are empty until sources are connected. Incident status labels reflect persisted incident workflow state, not live alert health.')
v3_route('get','/commands','Read executable command catalog',
    desc='Currently always returns 503 commands_unavailable; no verified command registry is configured.')
P['/commands']['get']['responses'].pop('200')

# The resource index has evidence-backed identities and relationships, but not
# the V3 topology layout, health, blast-radius or telemetry view-model yet.
S['V3ResourceTopologyPartial'] = obj({
    'complete_v3_dto':v3_bool(), 'source':string(),
    'nodes':arr(obj({'id':string(),'name':string(),'kind':string(),'cluster':string(),
        'environment_ids':arr(string())}, ['id','name','kind','environment_ids'])),
    'edges':arr(obj({'id':string(),'from':string(),'to':string(),'kind':string(),
        'provenance':string(),'environment_ids':arr(string())},
        ['id','from','to','kind','provenance','environment_ids'])),
    'unavailable_fields':arr(string())},
    ['complete_v3_dto','source','nodes','edges','unavailable_fields'])
v3_route('get','/topology','Read authorized resource identities and relationships',
    response=ref('V3ResourceTopologyPartial'),
    query=[v3_query(x) for x in ['cluster','around','depth','group_by']],
    desc='Partial CMDB projection only; each node is checked against resources.view in its persisted environment/application/team. Edges require both endpoints in a common authorized environment. group_by is currently unsupported (422); layout, health, blast radius, tree and Sankey are unavailable. No resources.view grant is automatically seeded from legacy RBAC.')

# Connectors and their configured provider capabilities.
S['V3TagChip'] = obj({'label':string(),'tone':string(),'mono':v3_bool()},['label'])
S['V3Connector'] = obj({**fieldnames('id brand name kind meta'),
    'category':string(enum=['model','observe','delivery','infra','im','knowledge','agent_obs','security']),
    'status':string(enum=['ok','unverified','error']), 'tags':arr(ref('V3TagChip'))})
S['V3ConnectorDetail'] = obj({**S['V3Connector']['properties'], **fieldnames('scope address checked deeplink'),
    'auth':v3_object('brand text'), 'services':arr(string()), 'queries':arr(string()),
    'tools':arr(obj({**fieldnames('name desc'),'enabled':v3_bool()}))})
S['V3ConnectorList'] = obj({'items':arr(ref('V3Connector')),
    'counts':obj({k:integer() for k in ['ok','unverified','error']}),
    'categories':arr(obj({'id':string(),'label':string(),'count':integer()}))})
S['V3ConnectorType'] = obj({**fieldnames('brand name kind version webhook_url'),
    'auth_methods':arr(v3_object('id label desc')),
    'scopes':arr(obj({**fieldnames('id label'),'required':v3_bool()})),
    'tools':arr(v3_object('name desc'))})
v3_route('get','/connector-types/{type}','Read one supported connector type',response=ref('V3ConnectorType'))
v3_route('get','/connectors','List configured connectors',role='admin',response=ref('V3ConnectorList'),
    query=[v3_query(x) for x in ['category','q']])
v3_route('get','/connectors/{id}','Read connector configuration',role='admin',response=ref('V3ConnectorDetail'))
v3_route('post','/connectors','Create a connector',role='admin',
    body=obj({'type':string(),'url':string(),'auth':string(),'credential_ref':string(),
        'tools':arr(string()),'webhook':v3_bool()},['type','url','auth']),
    response=obj({'id':string()},['id']),status=201)
v3_route('patch','/connectors/{id}','Update connector configuration',role='admin',
    body=obj({'tools':arr(obj({'name':string(),'enabled':v3_bool()}))},['tools']),
    response=obj({'ok':v3_bool(),'tools':arr(obj({'name':string(),'desc':string(),'enabled':v3_bool()}))}))
v3_route('post','/connectors/{id}/test','Probe configured connector',role='admin',
    response=obj({'ok':v3_bool(),'latency_ms':integer(),'error_code':string()}),
    desc='Failure returns an explicit error; a configured adapter is not silently reported healthy.')

# MCP registrations are legacy application-bound records. Only an explicitly
# reviewed registration/environment binding may appear in V3; the migration
# does not guess an environment for historical registrations.
S['V3McpServerPartial'] = obj({**fieldnames('id brand name desc transport caps last'),
    'trust':obj({'label':string(),'tone':string()}),
    'apps':arr(obj({'label':string(),'tone':string()})),
    'status':string(enum=['pending']),
    'complete_v3_dto':v3_bool(), 'unavailable_fields':arr(string())})
S['V3McpDetailPartial'] = obj({**S['V3McpServerPartial']['properties'],
    'version':string(), 'counts':obj({k:integer() for k in ['tools','resources','prompts']}),
    'tools':arr(obj({'name':string(),'desc':string(),'write':v3_bool(),
        'enabled':v3_bool(),'calls':string()})),
    'resources':arr(obj({'uri':string(),'desc':string()})),
    'prompts':arr(obj({'name':string(),'desc':string()})),
    'calls':arr(obj({'time':string(),'agent':string(),'tool':string(),'ms':integer(),'ok':v3_bool()})),
    'trust_info':obj({'readonly_check':string(),'scope':arr(string()),'limit':string(),'auth':string()})})
v3_route('get','/mcp-servers','List accessible, environment-bound MCP extensions',
    response=obj({'kpis':arr(obj({'id':string(),'label':string(),'value':string(),
        'sub':string(),'series':arr(v3_num())})), 'items':arr(ref('V3McpServerPartial')),
        'complete_v3_dto':v3_bool(),'unavailable_fields':arr(string())}),
    desc='Partial registration metadata only. Unbound historical MCP records are omitted, not copied across environments. Health, discovered tools, call metrics and pending changes are unavailable; status is always pending.')
v3_route('get','/mcp-servers/{id}','Read accessible MCP registration metadata',
    response=ref('V3McpDetailPartial'),
    desc='Partial: no live discovery, health or tool-call history. Counts for resources and prompts are placeholders; unavailable_fields names the gaps. Credentials are never returned.')
v3_route('patch','/mcp-servers/{id}/tools/{name}','Change a reviewed MCP tool',
    body=obj({'enabled':v3_bool()},['enabled']),
    desc='Currently always returns 503 mcp_review_unavailable after scoped authorization; no trusted discovery snapshot exists.')
P['/mcp-servers/{id}/tools/{name}']['patch']['responses'].pop('200')
v3_route('post','/mcp-servers/{id}/review','Review newly discovered MCP tools',
    desc='Currently always returns 503 mcp_review_unavailable after scoped authorization; no trusted discovery snapshot exists.')
P['/mcp-servers/{id}/review']['post']['responses'].pop('200')
S['V3McpEnvironmentBinding'] = obj({'id':string(),'application_id':string(),
    'environment_id':string(),'bound':v3_bool(),'changed':v3_bool()},
    ['id','application_id','environment_id','bound','changed'])
v3_route('put','/mcp-servers/{id}/environment-binding','Explicitly bind a registered MCP server to the selected environment',
    response=ref('V3McpEnvironmentBinding'),
    desc='Ancillary V3 metadata-only operation. Uses exactly one selected X-AIOps-Environment; body does not choose scope. Requires connectors.manage on the registered application. Only active registrations with operator-confirmed read-only bindings may be enrolled. This does not authorize tool execution or attest that endpoint, fixed arguments and credentials are safe for the selected environment. Idempotent and audited. Historical records are never automatically bound.')
v3_route('delete','/mcp-servers/{id}/environment-binding','Unbind a registered MCP server from the selected environment',
    response=ref('V3McpEnvironmentBinding'),
    desc='Ancillary V3 operation. Uses exactly one selected X-AIOps-Environment and connectors.manage on the registered application. Idempotent and audited when changed.')

# Alerts and incidents: document the real partial responses explicitly, while
# completion status remains tracked separately in the human contract.
S['V3InboxItem'] = obj({**fieldnames('id title sub time href alert_id'),
    'kind':string(enum=['alert','approval','mention','runbook','report','access','agent']),
    'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    'unread':v3_bool(),'day':string(enum=['today','yesterday'])})
S['V3IncidentSummary'] = obj({**fieldnames('id title duration commander'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'status':string(enum=['active','mitigated','resolved'])},
    ['id','severity','title','status','duration','commander'])
S['V3IncidentTimelineEntry'] = obj({'time':string(format='date-time'),
    'actor':obj({'kind':string(enum=['brand','ai','person']),'name':string()},['kind','name']),
    'text':string(),'sub':string()},['time','actor','text'])
v3_route('get','/inbox','Read scoped inbox',response=obj({'items':arr(ref('V3InboxItem')),
    'counts':v3_map(integer())}),query=[v3_query('filter',string(enum=['all','mine','mentions','subscribed'])),v3_query('tab')],
    desc='Currently includes recent incident-backed alerts; other inbox kinds are not yet connected. tab remains an alias for filter.')
v3_route('post','/alerts/{id}/claim','Claim a persisted active alert',role='operator',
    response=obj({'id':string(),'assignee_id':string()},['id','assignee_id']),
    desc='Partial V3 behavior: transactionally sets platform ownership and audits the change; '
         'same-user retry is idempotent. The full AlertDetail response, metric series, and '
         'grouping policy are not available yet; no external escalation is stopped.')
S['V3AlertGroupPartial'] = obj({**fieldnames('id service since owner'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'labels':arr(v3_object('k v')),'count':integer(),
    'state':string(enum=['firing','pending','silenced','resolved']),
    'notify':v3_object('channel text'),'badges':arr(v3_object('text tone')),
    'instances':arr(obj({**fieldnames('id value state duration'), 'series':arr(v3_num())}))})
v3_route('get','/alert-groups','Read incident-backed alert groups',
    response=obj({'items':arr(ref('V3AlertGroupPartial')),'counts':v3_map(integer())}),
    query=[v3_query('q'),v3_query('severity',{'type':'array','items':string(enum=['P1','P2','P3','P4','all']),'uniqueItems':True}),v3_query('env'),v3_query('state')],
    desc='severity accepts repeated query keys (form/explode) and matches each group\'s highest severity; all must stand alone. Partial: pending/silenced/inhibited states, metric series, and full notification grouping are not connected; no synthetic signal values are emitted.')
for v3_parameter in P['/alert-groups']['get']['parameters']:
    if v3_parameter['name'] == 'severity':
        v3_parameter.update({'style':'form','explode':True})
v3_route('get','/incidents','List scoped incidents',response=obj({'items':arr(ref('V3IncidentSummary')),
    'counts':obj({k:integer(minimum=0) for k in ['active','resolved','all']},
        ['active','resolved','all'])}, ['items','counts']),
    query=[v3_query('status',string(enum=['active','resolved','all'],default='active'))],
    desc='Counts cover all visible incidents regardless of the selected status. A mitigated incident remains active until work is resolved. Duplicate, empty, or unknown status values return 422.')
v3_route('get','/incidents/{id}','Read incident detail when impact is available',
    desc='Currently always returns 503 impact_unavailable after identity and scope checks; required SLO impact data is not connected.')
P['/incidents/{id}']['get']['responses'].pop('200')
v3_route('post','/incidents/{id}/timeline','Append a durable incident note',
    role='operator',body=obj({'text':string()},['text']),response=ref('V3IncidentTimelineEntry'),status=201)
S['V3ObservedAlertRuleSummary'] = obj({**fieldnames('id folder name environment_id application_id'),
    'state':string(enum=['firing','pending','ok'])},
    ['id','folder','name','state','environment_id','application_id'])
S['V3ObservedAlertRuleDetail'] = obj({**S['V3ObservedAlertRuleSummary']['properties'],
    **fieldnames('datasource expr last_evaluation'), 'for_seconds':v3_num(),
    'labels':v3_map(string()),'annotations':v3_map(string()),
    'evaluation_ms':v3_num(),'partial':v3_bool(),'unavailable':arr(string())})
v3_route('get','/alert-rules/observed','Read provider-observed alert rule summaries',
    response=obj({'items':arr(ref('V3ObservedAlertRuleSummary')),
        'counts':v3_map(integer()),'partial':v3_bool(),
        'unavailable_environments':arr(string())}),
    desc='Auxiliary read-only backend endpoint, not the V3 AlertRule DTO. Rule groups require explicit server-side application/environment binding and matching provider labels. Unconfigured sources return 503; partial source failure returns 206.')
P['/alert-rules/observed']['get']['responses']['206'] = P['/alert-rules/observed']['get']['responses']['200']
v3_route('get','/alert-rules/observed/{id}','Read one provider-observed alert rule',
    response=ref('V3ObservedAlertRuleDetail'),
    desc='Auxiliary read-only backend endpoint. No historical preview, threshold condition, notification route or AI triage is inferred.')

S['V3SilenceMatcher'] = obj(fieldnames('k v'), ['k','v'])
S['V3SilenceRemaining'] = obj({'pct':integer(minimum=0,maximum=100),'text':string()},['pct','text'])
S['V3SilenceRead'] = obj({**fieldnames('id window creator reason'),
    'matchers':arr(ref('V3SilenceMatcher')),
    'state':string(enum=['active','planned','expired']),
    'remaining':ref('V3SilenceRemaining'),'affects':integer(minimum=0)},
    ['id','matchers','state','window','affects','creator','reason'])
S['V3InhibitionRead'] = obj({'id':string(),'source':arr(ref('V3SilenceMatcher')),
    'target':arr(ref('V3SilenceMatcher')),'equal':arr(string()),'stat':string()},
    ['id','source','target','equal','stat'])
v3_route('get','/silences','Read environment-scoped Alertmanager silences',
    response=obj({'items':arr(ref('V3SilenceRead')),
        'counts':obj({k:integer(minimum=0) for k in ['active','planned','expired']},['active','planned','expired']),
        'partial':v3_bool(),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool()},
        ['items','counts','partial','unavailable_environments','unscoped_present']),
    query=[v3_query('state',string(enum=['active','planned','expired','all']))],
    desc='Only provider silences with an exact configured environment-label matcher are returned. Current affects is counted from provider alerts silenced by each ID, not inferred from matcher overlap. Other-environment records do not affect counts, status, or coverage; broad records set only unscoped_present. Unconfigured or failed sources return 503; broad records or partial environment failure return 206. More than 500 visible silences returns 503 without partial counts; the shared provider response is bounded to 2 MiB. Still incomplete for application/team-scoped grants and larger shared upstream responses.')
P['/silences']['get']['responses']['206'] = P['/silences']['get']['responses']['200']
v3_route('get','/inhibitions','Read environment-scoped Alertmanager inhibition configuration',
    response=obj({'items':arr(ref('V3InhibitionRead')),'partial':v3_bool(),
        'unavailable':arr(string()),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool()},
        ['items','partial','unavailable','unavailable_environments','unscoped_present']),
    desc='Partial projection: only rules with exact source and target environment matchers are returned. Other-environment rules do not affect status or coverage; broad rules set only unscoped_present. Alertmanager status does not expose per-rule active or hit counts, so active is omitted and stat explains unavailable data. Populated lists return 206, not a fabricated full V3 DTO. Unconfigured or failed sources return 503.')
P['/inhibitions']['get']['responses']['206'] = P['/inhibitions']['get']['responses']['200']

# V3 review records are separate from historical rollback action plans. A
# trusted in-process producer must create one with complete sourced evidence;
# approval and execution remain unavailable until passkey-bound authorization.
S['V3ProposalComment'] = obj({'author':string(),'ai':v3_bool(),
    'time':string(format='date-time'),'text':string()},['author','time','text'])
S['V3Proposal'] = obj({**fieldnames('id title impact proposed_by investigation_id target method verification_note requirement'),
    'status':string(enum=['pending','approved','rejected','executed']), 'risk':string(enum=['low','medium','high']),
    'reversible':v3_bool(),'proposed_at':string(format='date-time'),
    'diff':obj({'path':string(),'lines':arr(string())},['path','lines']),
    'evidence':arr(obj({'n':integer(),'icon':string(),'title':string(),'desc':string(),
        'series':arr(v3_num())},['n','icon','title','desc'])),
    'verification':arr(string()),'checklist':arr(string()),
    'discussion':arr(ref('V3ProposalComment'))},
    ['id','title','status','risk','impact','reversible','proposed_by','proposed_at',
     'investigation_id','target','diff','method','evidence','verification',
     'verification_note','checklist','requirement','discussion'])
v3_route('get','/proposals/{id}','Read a scoped V3 proposal review record',response=ref('V3Proposal'),
    desc='Only records created by a trusted V3 producer can appear; existing rollback action_plans are not relabeled.')
v3_route('post','/proposals/{id}/comments','Append an audited proposal comment',
    body=obj({'text':string()},['text']),response=ref('V3ProposalComment'),status=201,
    query=[{'name':'Idempotency-Key','in':'header','schema':string(maxLength=128)}],
    desc='Only pending proposals may be discussed. Idempotency-Key is optional and scoped to the actor/proposal.')
P['/proposals/{id}/comments']['post']['responses']['200'] = P['/proposals/{id}/comments']['post']['responses']['201']
v3_route('post','/proposals/{id}/reject','Reject a pending proposal without executing it',
    body=obj({'comment':string(),'note':string()}),response=ref('V3Proposal'),
    desc='Serializes state changes, audits rejection, and returns the persisted review record.')

# Governed runbooks and scoped knowledge search.
S['V3RunbookStep'] = obj({'n':integer(),'kind':string(enum=['query','check','action','verify']),
    'title':string(),'body':string(),'connectors':arr(string()),'code':string(),
    'readonly':v3_bool(),'approval':v3_bool()})
S['V3RunbookCreateStep'] = obj({'n':integer(),'kind':string(enum=['query','check','action','verify','act','judge']),
    'title':string(),'body':string(),'connectors':arr(string()),'code':string(),
    'readonly':v3_bool(),'approval':v3_bool()})
S['V3RunbookSummary'] = obj({**fieldnames('id title scope owner version last_verified'),
    'tags':arr(string()),'status':string(enum=['published','review','draft']),
    'refs_30d':integer(),'resolve_rate':v3_num(),'stale':v3_bool()},
    ['id','title','tags','scope','owner','version','status','refs_30d','last_verified','stale'])
S['V3Runbook'] = obj({**fieldnames('id title version base owner scope when'),
    'tags':arr(string()),'steps':arr(ref('V3RunbookStep')),
    'reviewers':arr(v3_object('name state')),'versions':arr(v3_object('v who when')),
    'usage':obj({'refs':integer(),'resolve_rate':v3_num(),'weekly':arr(integer()),'highlight':integer()}),
    'incidents':arr(v3_object('id title'))},
    ['id','title','version','base','owner','scope','tags','when','steps','reviewers','versions','usage','incidents'])
v3_route('get','/runbooks','List accessible runbooks',response=obj({'items':arr(ref('V3RunbookSummary')),
    'counts':v3_map(integer()),'kpis':arr({'type':'object','additionalProperties':True}),
    'suggestions':arr({'type':'object','additionalProperties':True})}),
    query=[v3_query(x) for x in ['q','service','tab']],
    desc='Returns 503 runbook_read_unavailable when an accessible runbook exists: complete reference and verification history is not persisted. An empty accessible collection returns empty items and counts.')
v3_route('get','/runbooks/{id}','Read governed runbook',response=ref('V3Runbook'),
    desc='Returns 503 runbook_read_unavailable for an accessible runbook until complete usage, outcome, and verification history is persisted. No zero usage or fabricated version timestamp is returned.')
for _path in ['/runbooks','/runbooks/{id}']:
    P[_path]['get']['responses']['503'] = {
        'description':'Mandatory Runbook read fields lack an authoritative persisted source',
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/runbooks','Create governed runbook draft',
    body=obj({'title':string(),'source':string(enum=['blank','template','postmortem','conversation','import']),
        'from':string(),'steps':arr(ref('V3RunbookCreateStep')),
        'status':string(enum=['draft','review'])},['title','source']),response=obj({'id':string()},['id']),status=201,
    desc='Requires scoped knowledge:manage grant. A nonblank source requires a validated reference; blank forbids one. Template IDs are registered server-side; imports require a nonempty Markdown upload owned by the current user in the selected environment. Conversations are checked in the same transaction; postmortem source remains unavailable. No source reference is trusted solely because the client provided an ID.')
v3_route('post','/runbooks/{id}/submit','Submit runbook for review',
    desc='Requires scoped knowledge:manage grant and maintainer ownership.',
    response=obj({'ok':v3_bool(),'status':string(enum=['review'])}))
v3_route('post','/runbooks/{id}/dry-run','Validate runbook without execution',
    response=obj({'steps':integer(),'ok':v3_bool(),'mode':string(enum=['validation_only']),
        'checks':arr({'type':'object','additionalProperties':True})}),
    desc='Validation only against persisted steps; request body is not used. Never runs external write steps.')
S['V3PostmortemCandidate'] = obj({**fieldnames('id title resolved duration'),
    'severity':string(enum=['P1','P2','P3','P4']), 'evidence':integer(minimum=0)},
    ['id','title','severity','resolved','duration','evidence'])
S['V3PostmortemSectionInput'] = obj(fieldnames('key title text'),['key','title','text'])
S['V3PostmortemActionInput'] = obj(fieldnames('text owner due'),['text','owner','due'])
S['V3PersistedPostmortemRecord'] = obj({**fieldnames('id incident_id title resolved duration created_at'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'status':string(enum=['draft','review','published']),
    'action_count':integer(minimum=0)},
    ['id','incident_id','title','severity','resolved','duration','status','created_at','action_count'])
v3_route('get','/postmortems/candidates','List resolved incidents eligible for a postmortem',
    response=obj({'items':arr(ref('V3PostmortemCandidate'))},['items']),
    desc='Only incidents with a persisted closing event and investigation report, no existing postmortem, and current events:view plus knowledge:view on their registered application or owner team. Evidence and duration come from stored records. More than 1000 visible candidates returns 413; unknown source severity returns 503 rather than an invented classification.')
v3_route('get','/postmortems/records','Read scoped persisted postmortem records',
    query=[v3_query('tab',string(enum=['all','draft','review','published']))],
    response=obj({'items':arr(ref('V3PersistedPostmortemRecord'))},['items']),
    desc='Auxiliary read-only inventory, not the PostmortemSummary dashboard DTO. Requires current events:view and knowledge:view on each registered incident application or owner team. Returns stored status, close time, duration and action count; no invented cause categories, completion rate or KPI. More than 1000 visible records returns 413.')
P['/postmortems/records']['get']['responses']['413'] = {
    'description':'More than 1000 visible persisted records',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/postmortems','Create a governed incident postmortem',
    body=obj({'incident_id':string(),'sections':arr(ref('V3PostmortemSectionInput')),
        'actions':arr(ref('V3PostmortemActionInput')),
        'status':string(enum=['draft','review'])},['incident_id','sections','actions','status']),
    response=obj({'id':string()},['id']),status=201,
    desc='Requires a single selected environment and current events:view plus knowledge:manage on the incident application or its persisted owner team. Locks and rechecks the resolved incident, investigation report and closing event; one postmortem per incident. Stores sections and actions with an audit record in one transaction, without executing or notifying. Publishing is a separate future step.')
S['V3KnowledgeSearchHit'] = obj({**fieldnames('title snippet'),
    'keyword':v3_num(),'vector':v3_num(),'score':v3_num()})
v3_route('post','/knowledge/search','Search scoped knowledge with keyword/vector ranking',
    body=obj({'q':string(),'service':string(),'top_k':integer(minimum=1,maximum=20)},['q']),
    response=obj({'hits':arr(ref('V3KnowledgeSearchHit')),
        'mode':string(enum=['hybrid','keyword','none']),'degraded':v3_bool()}),
    desc='When embeddings are unavailable, returns a keyword result with degraded=true; an unavailable keyword index returns 503.')
S['V3ObservedKnowledgeDocument'] = obj({**fieldnames('id name kind'),
    'docs':{'type':['integer','null']},
    **{key:{'type':['string','null']} for key in ['sync','chunks','last','status']}},
    ['id','name','kind','docs','sync','chunks','last','status'])
v3_route('get','/knowledge-sources/observed-documents','Read scoped published document identities',
    response=obj({'items':arr(ref('V3ObservedKnowledgeDocument')),
        'index':{'type':'null'},'partial':v3_bool(),'reason':string(),
        'truncated':v3_bool()},
        ['items','index','partial','reason','truncated']),
    desc='Auxiliary read-only backend endpoint, not the V3 KnowledgeSource DTO. A source registry, synchronization state and index summary are unavailable. No document body is returned.')
S['V3KnowledgeSourceRegistration'] = obj({**fieldnames('id environment_id application_id team_id name type url schedule chunk registration_status created_at'),
    'spaces':arr(string()),'inherit_acl':v3_bool()},
    ['id','environment_id','name','type','url','spaces','schedule','chunk','inherit_acl','registration_status','created_at'])
S['V3KnowledgeSourceRegistrationInput'] = obj({**fieldnames('name type url secret_ref application_id team_id schedule chunk'),
    'spaces':arr(string()),'inherit_acl':v3_bool()},
    ['name','type','url','secret_ref','spaces','schedule','chunk','inherit_acl'])
v3_route('get','/knowledge-sources/registrations','Read scoped, unverified source registrations',
    response=obj({'items':arr(ref('V3KnowledgeSourceRegistration')),'index':{'type':'null'},
        'complete_v3_dto':v3_bool(),'truncated':v3_bool(),'unavailable_fields':arr(string())},
        ['items','index','complete_v3_dto','truncated','unavailable_fields']),
    desc='Auxiliary registration inventory only; no connection, sync, document count or index status is claimed. Returns 422 when a bounded candidate scan cannot prove completeness.')
v3_route('post','/knowledge-sources/registrations','Register scoped source configuration without activating sync',
    body=ref('V3KnowledgeSourceRegistrationInput'),status=201,
    response=obj({'id':string(),'registration_status':string(enum=['unverified'])},['id','registration_status']),
    desc='Auxiliary operation; stores a Vault reference, not a plaintext credential. Registration does not prove connection or begin ingestion. Requires one environment and knowledge.manage; audit is transactional.')
v3_route('post','/knowledge-sources/test','Test a knowledge source connection',
    body=obj(fieldnames('type url secret_ref'),['type','url','secret_ref']),status=501,
    desc='The Vault-backed source connector is not yet configured. After validating the request and knowledge.manage, the endpoint fails closed with 501 source_connection_test_unavailable; it never returns a fixed success.')
P['/knowledge-sources/test']['post']['responses']['501'] = {
    'description':'Vault-backed source connection test is not available',
    'content':{'application/json':{'schema':ref('Error')}}}

# Read-only service discovery and externally queried telemetry.
S['V3CatalogEvidenceState'] = obj({'state':string(enum=['unavailable']), 'reason':string()},['state'])
S['V3CatalogConfiguredValue'] = obj({'state':string(enum=['configured','unavailable']), 'value':string()},['state'])
S['V3ServiceMetadata'] = obj({**fieldnames('id key name description environment'),
    'complete_v3_dto':v3_bool(),
    **{name:ref('V3CatalogConfiguredValue') for name in ['team','tier','owner','oncall']},
    **{name:ref('V3CatalogEvidenceState') for name in ['status','health','release','alerts']},
    'slo':obj({'target_percent':ref('V3CatalogConfiguredValue'),
        'availability':ref('V3CatalogEvidenceState'),'budget_left':ref('V3CatalogEvidenceState')}),
    'connectors':arr(obj({'kind':string(),'resource':string(),'state':string(enum=['configured'])}))},
    ['id','key','name','description','environment','complete_v3_dto','team','tier','owner',
     'oncall','status','health','slo','release','alerts','connectors'])
v3_route('get','/services','Read authorized persisted service metadata',
    response=obj({'items':arr(ref('V3ServiceMetadata')),'total':integer(),
        'complete_v3_dto':v3_bool()},['items','total','complete_v3_dto']),
    query=[v3_query(x) for x in ['q','tier','team','status']],
    desc='Partial V3 DTO: live status/health, measured SLO, releases and alerts are explicitly unavailable. '
         'Configured targets and bindings are not live evidence. A status filter returns 422 until live status is connected.')
v3_route('get','/services/{id}','Read one authorized service metadata record',
    response=ref('V3ServiceMetadata'),
    desc='Partial V3 DTO with explicit evidence gaps; id binds application and environment.')
S['V3ExternalTarget'] = obj({**fieldnames('id connector_id provider target_kind external_id environment_id')},
    ['id','connector_id','provider','target_kind','external_id','environment_id'])
S['V3ExternalTargetInput'] = obj({'connector_id':string(),
    'target_kind':string(enum=['project']), 'external_id':string()},
    ['connector_id','target_kind','external_id'])
v3_route('get','/services/{id}/external-targets','List explicitly bound external service targets',
    response=obj({'items':arr(ref('V3ExternalTarget'))},['items']),
    desc='Requires services.view for the persisted application or team and connectors.view for its environment. '
         'At most 100 targets per service and environment; older application binding JSON is not inferred or copied. No endpoint or credentials are returned.')
v3_route('post','/services/{id}/external-targets','Bind an exact external target to a service',
    body=ref('V3ExternalTargetInput'),response=ref('V3ExternalTarget'),status=201,
    desc='Requires services.change for the persisted application or team and connectors.manage for its environment. '
         'Currently accepts only a positive numeric GitLab project ID on a connector in the same environment. '
         'A target cannot be attributed to two services in one environment; a service may bind at most 100 targets per environment; writes are audited. '
         'This metadata binding does not grant the connector new credentials or authorize an external write.')
v3_route('delete','/services/{id}/external-targets/{bindingId}','Remove an exact external target binding',
    status=204,
    desc='Requires services.change for the persisted application or team and connectors.manage for its environment. '
         'Only the scoped binding is removed and the change is audited; no external target is deleted.')
S['V3DiscoveredService'] = obj({**fieldnames('id namespace kind image owner_hint tier_hint'),
    'replicas':{'type':['integer','null']},'matched':arr(string()),'missing':arr(string())})
v3_route('get','/services/discover','Discover scoped workloads',
    response=obj({'cluster':string(),'items':arr(ref('V3DiscoveredService')),'registered':integer()}),
    query=[v3_query('cluster')])
S['V3ServiceRegistration'] = obj({'items':arr(obj({
    'id':string(), 'namespace':string(), 'tier':string(enum=['T0','T1','T2','T3']),
    'owner':string(), 'slo':string(), 'p99':string()},['id']))},['items'])
v3_route('post','/services','Register scoped service metadata',
    role='admin',body=ref('V3ServiceRegistration'),
    response=obj({'created':integer(minimum=1)},['created']),status=201,
    desc='Atomically registers 1-50 service profiles. Links a current Kubernetes workload only when its identity is unique; registration is audited.')
S['V3MappingCell'] = obj({'state':string(enum=['ok','suggested','missing']),
    'value':string(),'brand':string()},['state'])
S['V3ServiceMapping'] = obj({'systems':arr(v3_object('id brand label')),
    'rows':arr(obj({'service':string(),'cells':v3_map(ref('V3MappingCell'))},['service','cells'])),
    'suggestions':integer(minimum=0)},['systems','rows','suggestions'])
v3_route('get','/service-mappings','Read persisted service connector mappings',
    response=ref('V3ServiceMapping'),
    desc='Shows only bindings for authorized applications and environments; missing means no persisted binding, not a health check.')
S['V3GitLabPipelineRecord'] = obj({**fieldnames('id provider service_id environment_id binding_id connector_id project_id status ref sha source created_at updated_at')},
    ['id','provider','service_id','environment_id','binding_id','connector_id','project_id','status','ref','sha','source','created_at','updated_at'])
S['V3GitLabPipelineRecord']['properties']['provider'] = string(enum=['gitlab'])
for timestamp in ('created_at','updated_at'):
    S['V3GitLabPipelineRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/records','Read a bounded page of verified GitLab pipeline records',
    query=[v3_query('service_id'),v3_query('binding_id'),v3_query('page',integer(minimum=1,maximum=1000))],
    response=obj({'items':arr(ref('V3GitLabPipelineRecord')),'page':integer(minimum=1),
        'page_size':integer(enum=[20]),'next_page':integer(minimum=0)},['items','page','page_size','next_page']),
    desc='Auxiliary read-only source evidence. Requires an explicitly bound numeric GitLab project in the selected environment, enabled list_pipelines tool, services.view for the persisted application or team, and connectors.view for its environment. Scope and grant are rechecked after the upstream fetch. Does not provide DORA KPIs, deployment proof, risk score, diff or alert correlation. Zero next_page means no upstream next-page header.')
for parameter in P['/changes/records']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
P['/changes/records']['get']['responses']['502'] = {
    'description':'GitLab pipeline source failed or returned an invalid bounded response',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3MetricProbe'] = obj({'status':string(enum=['observed','no_samples','not_registered','not_configured','unavailable','truncated']),
    'series':integer(minimum=0),'samples':integer(minimum=0)},['status','series','samples'])
S['V3IncompleteMetrics'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'service':string(),'environment_id':string(),'available':v3_map(ref('V3MetricProbe')),
    'missing':arr(string())},['error','service','environment_id','available','missing'])
v3_route('get','/metrics/dashboard','Inspect scoped metric dashboard readiness',
    query=[v3_query('service'),v3_query('range',string(enum=['1h'])),v3_query('align',string(enum=['true','false']))],
    desc='Requires a persisted service/environment Prometheus binding. The currently registered queries cannot fill the required V3 MetricsDashboard; when scoped data is available, returns 503 dashboard_unavailable with observed query availability and missing sections. No KPI or SLO values are fabricated.')
P['/metrics/dashboard']['get']['responses'].pop('200')
P['/metrics/dashboard']['get']['responses']['503'] = {
    'description':'Complete V3 dashboard unavailable from registered sources',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteMetrics')]}}}}
S['V3LogLine'] = obj({**fieldnames('time service message trace'),
    'level':string(enum=['ERROR','WARN','INFO'])})
v3_route('get','/logs','Search configured log provider',
    response=obj({'total':string(),'errors':string(),'histogram':arr(arr(integer())),
        'items':arr(ref('V3LogLine')),'patterns':arr({'type':'object','additionalProperties':True})}),
    query=[v3_query(x) for x in ['q','levels','service']])
S['V3TraceRow'] = obj({**fieldnames('id time op href'),
    'services':arr(string()),'duration_ms':v3_num(),'spans':integer(),'errors':integer()})
S['V3TraceList'] = obj({'scope':string(enum=['app','agent']),'total':integer(),'took':string(),
    'colors':v3_map(string()),'facets':arr({'type':'object','additionalProperties':True}),
    'views':arr({'type':'object','additionalProperties':True}),
    'histogram':obj({'counts':arr(arr(integer())),'axis':arr(string())}),
    'scatter':arr(obj({'x':integer(),'y':v3_num(),'error':v3_bool(),'id':string()})),
    'items':arr(ref('V3TraceRow'))})
S['V3TraceView'] = obj({**fieldnames('id trace_id op time version flame_note'),
    'duration_ms':v3_num(),'colors':v3_map(string()),
    'spans':arr(obj({**fieldnames('id service op'),'depth':integer(),'start':v3_num(),'end':v3_num(),
        'status':string(enum=['ok','error'])})),
    'graph':obj({'nodes':arr({'type':'object','additionalProperties':True}),
        'edges':arr({'type':'object','additionalProperties':True})})})
v3_route('get','/traces','Query scoped traces',response=ref('V3TraceList'),
    query=[v3_query(x) for x in ['scope','view','service']])
v3_route('get','/traces/{id}','Read scoped trace',response=ref('V3TraceView'),
    query=[v3_query('service')])

# Role matrix and membership are read from V3 grants. The writable role
# endpoint applies only to explicitly scoped grants and audits the change.
S['V3Role'] = obj({**fieldnames('id name kind'),'members':integer()},['id','name','members','kind'])
S['V3RoleDetail'] = obj({**S['V3Role']['properties'],**fieldnames('desc notes'),
    'member_names':arr(string()),'scope':arr(string()),'actions':arr(string()),
    'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))})
S['V3Member'] = obj({**fieldnames('id name email department phone last_login'),
    'roles':arr(string()),'mfa':string(enum=['passkey','totp','none','na']),
    'source':string(enum=['feishu','ldap','system','invite']),
    'status':string(enum=['active','disabled']),'agent':v3_bool()})
S['V3MembersView'] = obj({'org':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'groups':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'items':arr(ref('V3Member')),'counts':v3_map(integer()),'mfa_grace_days':integer()})
v3_route('get','/roles','List scoped roles',response=listed('V3Role'))
v3_route('get','/roles/{id}','Read role matrix and associations',response=ref('V3RoleDetail'))
v3_route('put','/roles/{id}','Update role matrix',
    body=obj({'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))},['matrix']),
    response=ref('V3RoleDetail'),desc='Requires a global roles:manage grant; rejects removal of the last global role manager.')
v3_route('get','/members','List scoped members',response=ref('V3MembersView'),
    query=[v3_query('filter'),v3_query('q')])

# Scoped audit uses the existing management_audit source. The hash chain is
# sealed from migration time; pre-migration history is not independently proven.
S['V3AuditEntry'] = obj({**fieldnames('id time action target source'),
    'actor':obj({'kind':string(enum=['user','agent','system']),'name':string()},['kind','name']),
    'result':string(enum=['ok','blocked','failed'])},
    ['id','time','actor','action','target','result','source'])
S['V3AuditView'] = obj({'items':arr(ref('V3AuditEntry')),
    'counts':v3_map(integer()),'volume':arr(arr(integer())),'next_cursor':string()},
    ['items','counts','volume'])
S['V3AuditDetail'] = obj({**fieldnames('id title time actor_kind actor_name payload'),
    'actor':arr(v3_object('k v')),'links':arr(v3_object('k v to')),
    'chain_ok':v3_bool()},
    ['id','title','time','actor_kind','actor_name','actor','links','payload','chain_ok'])
v3_route('get','/audit','Read environment-scoped audit events',
    response=ref('V3AuditView'),
    query=[v3_query(x) for x in ['filter','q','cursor']],
    desc='Unknown or ambiguous historical environment rows are omitted; counts reflect visible records only.')
v3_route('get','/audit/{id}','Verify audit record chain and read redacted detail',
    response=ref('V3AuditDetail'),
    desc='chain_ok validates the sealed prefix from migration time, not independent proof of pre-migration history.')
v3_route('post','/audit/export','Create a scoped CSV export',
    body=obj(fieldnames('filter q')),response=obj({'url':string()},['url']),
    desc='Export is limited to 10,000 visible records and retained for one hour.')
v3_route('get','/audit/exports/{id}','Download owner and environment-bound audit CSV',
    desc='Authenticated download checks the original environment set and export expiry.')
P['/audit/exports/{id}']['get']['responses']['200']['content'] = {
    'text/csv': {'schema':{'type':'string','format':'binary'}}}

# The V3 Alertmanager receiver replaces the historical browser-independent
# route with a source-token and fixed mapping boundary. It never accepts a
# client-selected environment and does not require a session cookie.
P['/webhooks/alertmanager/{source}']['post']['description'] = (
    'V3 source Bearer token required. Each alert must uniquely match a configured '
    'application/environment mapping that is still active in this instance. '
    'The receiver atomically persists incidents and deduplicated occurrences; '
    'unmapped or cross-instance events are rejected. External notification '
    'delivery remains disconnected until a V3-authorized worker is available.')
P['/webhooks/alertmanager/{source}']['post']['responses']['202']['content']['application/json']['schema'] = obj({
    'accepted': integer(),
    'notification_delivery': {'type': 'string', 'enum': ['not_configured', 'not_connected']},
}, ['accepted', 'notification_delivery'])
