"""V3 operations backed by current handlers; types.ts defines public DTO fields.

This file omits frontend-only mock routes. Registered partial or explicitly
unavailable handlers are documented with their actual response boundaries.
Reusing a path replaces its pre-V3 operation on the V3 instance.
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
S['V3NotificationSettings'] = obj({'quiet': obj({'enabled': v3_bool(), 'from': string(), 'to': string()}, ['enabled','from','to']),
    'digest': string(enum=['off','daily','weekly']),
    'subscriptions': arr(obj({**fieldnames('id label sub'), 'on': v3_bool()}, ['id','label','sub','on']))},
    ['quiet','digest','subscriptions'])
S['V3NotificationSettingsPatch'] = obj({
    'quiet': {'oneOf':[v3_bool(), obj({'enabled':v3_bool(),'from':string(),'to':string()}, ['enabled','from','to'])]},
    'digest': string(enum=['off','daily','weekly']),
    'subscriptions': {'oneOf':[arr(obj({**fieldnames('id label sub'),'on':v3_bool()}, ['id','label','sub','on'])),
        {'type':'object','additionalProperties':v3_bool()}]},
})
S['V3NotificationSettingsPatch']['minProperties'] = 1
v3_route('get','/instance','Read the configured V3 isolation boundary',role='public',response=ref('V3InstanceInfo'))
v3_route('get','/integrations/lower-sources','List configured lower-environment source metadata',
    response=listed('V3LowerSource'),
    desc='Requires connectors.view in the production environment. Only available on the production instance; non-production returns 404 after route authorization. The credential field reports that a Vault reference is configured, not that connectivity or read-only source credentials have been verified. Source endpoints and Vault references are never returned.')
P['/integrations/lower-sources']['get']['responses']['404'] = {
    'description':'Not available on a non-production instance',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3ConfigSyncItem'] = obj({
    **fieldnames('id kind_label title mr author meta'),
    'kind':string(enum=['template']),
    'status':string(enum=['pending','imported','exported'])},
    ['id','kind','kind_label','title','mr','author','meta','status'])
S['V3ConfigSyncList'] = obj({
    'direction':string(enum=['import','export']),
    'items':arr(ref('V3ConfigSyncItem'))}, ['direction','items'])
S['V3PromotionRequest'] = obj({'kind':string(enum=['template','templates','runbooks','alert-rules','agents'],
    description='Only template/templates are currently supported; other kinds return 501 promotion_kind_unavailable.'),
    'id':string(minLength=1,maxLength=200)}, ['kind','id'])
S['V3PromotionResult'] = obj(fieldnames('mr path'), ['mr','path'])
v3_route('get','/config-sync','List configuration promotion records or merged candidates',
    response=ref('V3ConfigSyncList'),
    desc='Partial main-contract operation: only sanitized canvas templates are supported. Non-production lists up to 100 exports by the current authenticated actor in selected authorized environments; each still requires canvas.change. Production requires a human platform administrator with global workspace.manage and lists up to 100 merged MR candidates. Candidate title, author and status are repository metadata, not verified provenance; the signed manifest, single changed file and merge commit are checked on explicit import. A missing repository or source signing/trust configuration returns 501. An over-limit list returns 413. The four-kind frontend page remains mock.')
v3_route('post','/promotions','Export a sanitized canvas template to a GitLab merge request',
    body=ref('V3PromotionRequest'),response=ref('V3PromotionResult'),
    desc='Partial main-contract operation: one selected non-production environment, an active source template owner and canvas.change grant are required. The signed manifest contains the user-provided template name, source identifiers, template kind and allowlisted graph layout; source node content, evidence, execution state, credentials and account ID are excluded. The current frontend may default the name to the source canvas title, so the name must be reviewed before export to the configuration repository. Creates or reuses a durable signed manifest and MR in the separately configured repository; an uncertain remote result returns 503. Production returns 409 already_production; all returns 409 env_view_only. Kinds other than template/templates return 501 promotion_kind_unavailable. Missing repository or signing configuration returns 501. The four-kind frontend workflow remains mock.')
v3_route('post','/config-sync/{id}/import','Import a merged signed canvas template',
    response=ref('V3ConfigSyncItem'),
    desc='Partial main-contract operation: id is mr-<positive IID>. Requires the production prod environment and a human platform administrator with global workspace.manage, rechecked inside the import transaction. The repository must prove the MR is merged with exactly one expected changed file at its fixed merge commit; the bounded allowlisted manifest must verify against a configured trusted source public key. Import also requires current satisfied MR approval state, a distinct non-bot approver after the final diff version and before merge, and currently verifiable protected target-branch policy. API reads cannot prove the governance policy in force at merge time; deployment must retain that evidence. Import and audit commit together; repeated identical imports converge, target drift returns 409. Non-production and all return 409. Missing repository or trust configuration returns 501. Unverifiable provenance returns 503. The four-kind frontend workflow remains mock.')
P['/config-sync/{id}/import']['post']['description'] += ' A unique target or import-ledger conflict returns 409 without persisting a partial import.'
for _method, _path in [('get','/config-sync'),('post','/promotions'),('post','/config-sync/{id}/import')]:
    P[_path][_method]['responses']['501'] = {
        'description':'Configuration repository or source trust is unavailable, or requested promotion kind is unsupported',
        'content':{'application/json':{'schema':ref('Error')}}}
P['/config-sync']['get']['responses']['413'] = {
    'description':'Export or merged candidate list exceeds 100 items',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/me','Read current account',response=ref('V3Me'))
v3_route('patch','/me','Update current account',body=v3_object('name phone title timezone'),response=ref('V3Me'))
v3_route('patch','/me/preferences','Update visual and language preferences',body=ref('V3PreferencesPatch'),response=ref('V3Preferences'))
v3_route('get','/me/profile','Read personal profile',response=ref('V3ProfileView'))
v3_route('put','/me/profile','Update personal profile',body=ref('V3ProfileView'),response=ref('V3ProfileView'))
v3_route('delete','/me/sessions/{id}','Revoke one account session',status=204)
S['V3MFATOTPStatus'] = obj({'state':string(enum=['none','pending','expired','active']),
    'recovery_remaining':integer(minimum=0)},['state','recovery_remaining'])
S['V3MFATOTPEnrollment'] = obj({'secret':string(),'otpauth_url':string(),
    'expires_at':string(format='date-time')},['secret','otpauth_url','expires_at'])
S['V3MFATOTPConfirmation'] = obj({'enabled':v3_bool(),
    'recovery_codes':arr(string())},['enabled','recovery_codes'])
S['V3MFATOTPVerification'] = obj({'verified':v3_bool(),
    'method':string(enum=['totp','recovery'])},['verified','method'])
v3_route('get','/me/mfa/totp','Read current account TOTP state',response=ref('V3MFATOTPStatus'),
    desc='Optional auxiliary route, registered only with a persistent server-owned AES-256 key file. Requires one authorized environment and a current human account/session. Returns no seed or recovery codes. TOTP enrollment is not evidence of login enforcement or passkey approval assurance.')
v3_route('post','/me/mfa/totp/enrollment','Begin current account TOTP enrollment',status=201,
    response=ref('V3MFATOTPEnrollment'),
    desc='Optional auxiliary route. Empty body and same-origin browser signal required. Requires one authorized environment and a login from the last ten minutes; older sessions with unknown issuance time must log in again. The pending seed is bound to the exact session, tier and environment, expires after ten minutes, and is returned only once. Treat the secret and otpauth_url as sensitive; do not log or cache.')
v3_route('post','/me/mfa/totp/confirm','Confirm current account TOTP enrollment',
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),response=ref('V3MFATOTPConfirmation'),
    desc='Optional auxiliary route. Same-origin browser signal and recent login required. Consumes one valid TOTP step and returns one-use recovery codes only in this response. The codes are stored only as salted hashes; this does not issue a passkey assurance token.')
v3_route('delete','/me/mfa/totp/enrollment','Cancel pending current account TOTP enrollment',status=204,
    desc='Optional auxiliary route. Empty body, same-origin browser signal, original initiating session and recent login required. Only pending enrollment is removed.')
v3_route('post','/me/mfa/verify','Verify one current account TOTP or recovery code',
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),response=ref('V3MFATOTPVerification'),
    desc='Optional auxiliary route. Same-origin browser signal and one authorized environment required. Consumes a TOTP time step or recovery code once, with lockout after repeated failures. Response is per-call verification only: it creates no login session, step-up proof or passkey approval assurance.')
v3_route('delete','/me/mfa/totp','Remove current account TOTP factor',status=204,
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),
    desc='Optional auxiliary route. Same-origin browser signal, one authorized environment, recent login and a valid TOTP or recovery code required. Removes the active factor and unused recovery hashes without changing passkey approval policy.')
for _method, _path in [('post','/me/mfa/totp/confirm'),('post','/me/mfa/verify'),('delete','/me/mfa/totp')]:
    P[_path][_method]['responses']['429'] = {
        'description':'TOTP verification is temporarily rate limited',
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/me/ai-preferences','Read personal AI preferences',response=ref('V3AIPreferences'))
v3_route('put','/me/ai-preferences','Update personal AI preferences',body=ref('V3AIPreferences'),response=ref('V3AIPreferences'))
v3_route('get','/me/notification-settings','Read personal notification settings',response=ref('V3NotificationSettings'),
    desc='Reads the authenticated user\'s persisted preference document. A user with no saved document receives platform defaults. This is not evidence that any notification delivery channel is connected or has delivered a message. Corrupt stored settings return 503 rather than silently replacing them with defaults.')
v3_route('put','/me/notification-settings','Update personal notification settings',body=ref('V3NotificationSettingsPatch'),response=ref('V3NotificationSettings'),
    desc='Merges the authenticated user\'s supported preference fields in a locked, audited transaction and returns the stored result. The current UI sends quiet as a boolean, digest as an enum and subscriptions as existing subscription IDs mapped to booleans. Full objects preserve existing subscription identity and metadata. This saves preferences only; it does not enable or send notifications.')
v3_route('get','/workspace','Read workspace settings',response=ref('V3Workspace'),
    desc='Reads the persisted workspace record. A missing or invalid record returns 503 workspace_unavailable rather than invented defaults. This is backend persistence only: deployment-owned fields and the page-wide workspace contract are not yet wired.')
v3_route('put','/workspace','Update workspace settings',role='admin',body=ref('V3Workspace'),response=ref('V3Workspace'),
    desc='Requires an active platform administrator with a global workspace.manage grant. Accepts one strict JSON object with known editable fields; rejects unknown or invalid fields. The transaction rechecks authority and returns persisted settings. Data region and branding fields are deployment-owned and cannot be changed here. No version precondition is supported, and the saved slug, default settings, and retention values do not yet drive dependent page behavior.')
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
    'participants': arr(string()), 'tokens': {'type':['integer','null'],'minimum':0,
        'description':'Null when any completed reply lacks trustworthy provider usage; the V3 frontend currently requires a number.'}})
S['V3Conversation'] = obj({'id': string(), 'title': string(), 'mode': string(enum=['chat','canvas']),
    'status': string(enum=['active','waiting_approval','resolved','archived']),
    'scope': obj({'service': string(), 'env': string(), 'inferred': v3_bool()}),
    'investigation_id': string(), 'messages': arr(ref('V3Message')),
    'artifacts': arr({'type':'object','additionalProperties':True}),
    'side': obj({'connectors': arr(v3_object('name', {'calls': integer()})),
        'context': obj({'used': {'type':['integer','null']}, 'budget': {'type':['integer','null']},
            'parts': arr(v3_object('label color', {'tokens': integer()}))}),
        'recalled': arr(v3_object('kind title', {'score': v3_num()}))})})
S['V3ConversationList'] = obj({'items': arr(ref('V3ConversationSummary')),
    'total': integer(), 'page': integer(), 'page_size': integer(),
    'stats': arr({'type':'object','additionalProperties':True}),
    'counts': obj({'mine':integer(),'team':{'type':['integer','null']},
        'triage':{'type':['integer','null']},'archived':integer()})})
v3_route('get','/conversations','List scoped conversations',response=ref('V3ConversationList'),
    query=[v3_query(x) for x in ['filter','q','tab','status','mode','page','page_size']],
    desc='The mine/archived projection merges privately owned manual chats and generic canvases in one authorized snapshot with consistent search, page, total and known counts. Team and triage have no reliable unified source and return 503 rather than an empty list; their counts are null, stats is empty and unknown provider usage is null. Historical investigations lack trustworthy mine/participants attribution. The current frontend expects numeric tokens and a conversation-view entry gate also excludes canvas-only users, so this is not the complete page contract.')
v3_route('post','/conversations','Create conversation',body=obj({'text':string(minLength=1),'context':{'type':['string','null']},'model':{'type':['string','null']}},['text']),
    response=obj({'id':string()},['id']),status=201,
    desc='text must contain non-whitespace and fit within 2 MiB UTF-8; context is limited to 4096 bytes and model to 256 bytes. The selected default model and creation audit are persisted. The first message is submitted separately.')
v3_route('get','/conversations/{id}','Read conversation and saved messages',response=ref('V3Conversation'),
    desc='Saved user and assistant messages use durable turn order, including messages written in one transaction with tied timestamps. Accepted audited turns follow the serialized audit chain. Imported or unlinked historical rows use a deterministic timestamp fallback whose original commit order cannot be proven.')
v3_route('post','/conversations/{id}/messages','Start a durable assistant reply',
    body=obj({'text':string(),'model':string(),'reasoning':string(),
        'attachments':arr(ref('V3Upload'))},['text']),
    response=obj({'message':ref('V3Message'),'reply_id':string()},['message','reply_id']),status=202,
    desc='A second concurrent reply is rejected with 409. The user message is durable before 202. Persisted uploads are rechecked inside the turn transaction for exact reference metadata, owner, environment, supported text type, UTF-8 content and an aggregate 16 KiB source limit. Paste and .txt/.log/.md/.csv/.json/.yaml/.yml files enter a private bounded model-only text snapshot; public message parts keep references without uploaded bytes. Images, links, PDFs and other unsupported types return 422 before a turn is created; invalid references return 422 and excessive attachment or model context returns 413. The snapshot remains in later model context without refetching the upload; a later text-only turn with attachment history also checks the selected model budget before persistence. Start and terminal writes recheck the persisted owner, environment, initiating session and current V3 conversation authorization in their transactions. A revoked terminal turn drops partial output rather than saving it. Provider usage in message.completed.run is optional and must not be inferred when unavailable. This text-only slice does not establish support for every frontend Upload kind.')
P['/conversations/{id}/messages']['post']['responses']['413'] = {
    'description':'Attachment source or model input budget exceeded before turn persistence',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/conversations/{id}/events','Resume ordered assistant events',
    query=[{'name':'Last-Event-ID','in':'header','schema':string(pattern='^[0-9]+$')}],
    desc='SSE sequence IDs support reconnection. Last-Event-ID must be one nonnegative decimal event ID; invalid or duplicate values return 422 invalid_event_cursor. A positive cursor must belong to this currently authorized conversation and replays its original reply even after a newer turn starts; an unknown or deleted cursor returns 422. An omitted or zero cursor selects the latest reply. Connection and each event batch recheck current conversation view; queued/running replies also recheck the persisted initiating session and conversation change before replay, while completed/failed history requires view only. Revocation between a batch check and network delivery can still expose that selected batch. Recovery rechecks the initiating session and current V3 authorization before retaining partial output. Early failure emits an empty message.started before status and terminal events. message.completed follows durable persistence; its run is optional when trustworthy provider usage is unavailable.')

# General canvases are persisted separately from investigation branches. The
# current list projects both, but legacy investigation routes and participant
# evidence do not yet satisfy the frontend contract.
S['V3CanvasSummary'] = obj({**fieldnames('id title kind status meta updated_at href'),
    'participants':{'type':['array','null'],'items':string()},
    'investigation_id':string(),'services':arr(string()),
    'thumb':obj({'nodes':arr(arr({})), 'edges':arr(string())})},
    ['id','title','kind','status','meta','participants','updated_at','href','services','thumb'])
S['V3CanvasTemplate'] = obj({**fieldnames('id kind name desc nodes'),
    'is_default':v3_bool()},['id','kind','name','desc','nodes'])
S['V3CanvasTurn'] = obj({**fieldnames('id author text model reasoning_label brand'),
    'role':string(enum=['user','assistant'])},['id','role','author','text'])
S['V3CanvasThread'] = obj({'context':string(),'turns':arr(ref('V3CanvasTurn'))},
    ['context','turns'])
S['V3CanvasTone'] = string(enum=['neutral','accent','ok','warn','danger','info'])
S['V3CanvasTileTone'] = string(enum=['neutral','accent','ok','warn','danger','info','t1','dev'])
S['V3CanvasKVBlock'] = obj({'type':string(enum=['kv']),
    'items':{'type':'array','items':{'type':'array','items':string(),'minItems':2,'maxItems':2},'maxItems':200}},['type','items'])
S['V3CanvasTagsBlock'] = obj({'type':string(enum=['tags']),
    'items':{'type':'array','items':string(),'maxItems':200}},['type','items'])
S['V3CanvasTextBlock'] = obj({'type':string(enum=['text']),'text':string()},['type','text'])
S['V3CanvasBarsBlock'] = obj({'type':string(enum=['bars']),
    'values':{'type':'array','items':v3_num(),'maxItems':200},
    'highlight':integer(minimum=0),'color':string()},['type','values'])
S['V3CanvasRow'] = obj({**fieldnames('badge text value state'),
    'badge_tone':ref('V3CanvasTone'),'dot':ref('V3CanvasTone'),
    'state_tone':ref('V3CanvasTone')},['text'])
S['V3CanvasRowsBlock'] = obj({'type':string(enum=['rows']),
    'items':{'type':'array','items':ref('V3CanvasRow'),'maxItems':200}},['type','items'])
S['V3CanvasStep'] = obj({'text':string(),'sub':string(),
    'state':string(enum=['done','gate','todo'])},['text','sub','state'])
S['V3CanvasStepsBlock'] = obj({'type':string(enum=['steps']),
    'items':{'type':'array','items':ref('V3CanvasStep'),'maxItems':200}},['type','items'])
S['V3CanvasCalloutBlock'] = obj({'type':string(enum=['callout']),
    'tone':ref('V3CanvasTone'),'icon':string(),'text':string()},['type','tone','text'])
S['V3CanvasBlock'] = {'oneOf':[ref('V3Canvas'+name+'Block') for name in
    ['KV','Tags','Text','Bars','Rows','Steps','Callout']],
    'discriminator':{'propertyName':'type','mapping':{
        kind:'#/components/schemas/V3Canvas'+name+'Block' for kind,name in
        [('kv','KV'),('tags','Tags'),('text','Text'),('bars','Bars'),
         ('rows','Rows'),('steps','Steps'),('callout','Callout')]}}}
S['V3CanvasNode'] = obj({**fieldnames('id icon title sub'),
    'x':integer(),'y':integer(),'w':integer(minimum=1,maximum=2000),
    'tone':ref('V3CanvasTileTone'),
    'badge':obj({'text':string(),'tone':ref('V3CanvasTone')},['text','tone']),
    'blocks':{'type':'array','items':ref('V3CanvasBlock'),'maxItems':50}},
    ['id','x','y','w','icon','tone','title','sub','blocks'])
S['V3CanvasEdge'] = obj({'from':string(),'to':string(),
    'tone':ref('V3CanvasTone'),'dash':v3_bool(),'live':v3_bool()},['from','to'])
S['V3CanvasArtifact'] = obj({**fieldnames('id icon brand ext title sub href'),
    'x':integer(),'y':integer(),'tone':ref('V3CanvasTone')},
    ['id','x','y','title','sub'])
S['V3CanvasDoc'] = obj({**fieldnames('id title kind status note env'),
    'participants':arr(string()),
    'nodes':{'type':'array','items':ref('V3CanvasNode'),'maxItems':100},
    'edges':{'type':'array','items':ref('V3CanvasEdge'),'maxItems':200},
    'artifacts':{'type':'array','items':ref('V3CanvasArtifact'),'maxItems':100},
    'threads':v3_map(ref('V3CanvasThread'))},
    ['id','title','kind','status','participants','note','nodes','edges','artifacts','threads'])
v3_route('get','/canvases','List scoped persisted canvases and investigations',
    response=obj({'items':arr(ref('V3CanvasSummary')),'counts':v3_map(integer()),
      'total':integer(),'page':integer(),'page_size':integer()}),
    query=[v3_query(x) for x in ['kind','q','page','page_size']],
    desc='Generic canvases are private to their owner; investigations use persisted environment and application/team grants. Legacy investigations have unknown participants (null), and their historical IDs do not yet route to a complete V3 detail page. This partial projection does not satisfy the frontend CanvasSummary contract.')
v3_route('post','/canvases','Create a persisted general canvas',
    body=obj({**fieldnames('title kind template_id goal'),
      'services':arr(string()),'ai_draft':v3_bool()},['title','kind']),
    response=obj(fieldnames('id href'),['id','href']),status=201,
    desc='Investigation creation and AI drafting currently return 503; selected services must be mapped to the request environment.')
v3_route('get','/canvases/{id}','Read owner-scoped general canvas',response=ref('V3CanvasDoc'),
    desc='General canvases return validated typed nodes, seven block variants, edges and artifacts plus persisted per-node exchanges in threads. Failed user prompts remain visible without a fabricated assistant reply; only completed exchanges are used as later model context. More than 500 exchanges, 2 MiB of thread history, or one assistant answer over 64 KiB returns 413 canvas_history_too_large. Invalid persisted canvas structure returns 503 canvas_unavailable. Investigation canvas detail is not implemented by this route.')
P['/canvases/{id}']['get']['responses']['413'] = {
    'description':'Conversation history exceeds the bounded canvas detail limit',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/canvases/{id}']['get']['responses']['503'] = {
    'description':'Persisted canvas structure, conversation history, or authorization source is unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/canvases/{id}/nodes/{node}/messages','Run a scoped general-canvas node reply',
    body=obj({'text':string(),'model':string(),'reasoning':string()},['text']),
    response=obj({'reply':string()},['reply']),
    desc='Owner-only general canvas node, one selected environment and current canvas.change grant. The synchronous response follows a durable assistant turn; user prompt, model choice, status and audit are persisted. One node reply runs at a time and returns 409 reply_in_progress for a concurrent run. The configured model receives the current node blocks, blocks from all transitive ancestors, and prior completed node and ancestor turn pairs within a 24 KiB context budget; sibling, descendant, and failed turns are excluded. Oversize context returns 413 node_context_too_large. Tool invocations are rejected, and model failure returns 503 with the failed user prompt retained. Access is checked about every two seconds during the model call and again in the completion transaction against the initiating session, canvas ownership and current canvas.change grant; a reply cannot commit when that final check denies access. A brief revoke and restore entirely between checks may be missed. Investigation nodes are separate and remain unavailable here.')
P['/canvases/{id}/nodes/{node}/messages']['post']['responses']['413'] = {
    'description':'Current and inherited node context exceeds the 24 KiB model budget',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3InvestigationBranchContext'] = obj({
    'branch_id': string(), 'revision': integer(minimum=0),
    'messages': arr({})}, ['branch_id','revision','messages'])
v3_route('get','/investigations/{id}/nodes/{node}/messages',
    'Read the current persisted investigation branch context',
    response=ref('V3InvestigationBranchContext'),
    desc='Auxiliary read-only route outside the 166 V3 page contracts. Resolves the branch, investigation, application, environment, and owner team from persisted records and requires current canvas.view for the application or owner team. Returns only the current branch revision and its historical JSON messages, preserving legacy optional fields as stored; it does not synthesize nodes, timestamps, summaries, or model output. The stored context is limited to 24 KiB. An absent, mismatched, or inaccessible branch returns 404; a missing, oversized, or malformed current context returns 503. The complete investigation graph, node write, and ancestor writeback remain unavailable.')
v3_route('get','/canvas-templates','List available canvas structures',
    response=obj({'items':arr(ref('V3CanvasTemplate'))},['items']),
    desc='Currently lists only private, persisted generic-canvas structure templates owned by the current user in authorized non-production environments. Investigation and team templates are unavailable; the full page contract remains mock.')
v3_route('post','/canvas-templates','Save a safe canvas structure template',
    body=obj({'from_canvas':string(),'name':string(),
      'keep':obj({k:v3_bool() for k in ['nodes','steps','runbooks','content']})},
      ['from_canvas','name']),response=ref('V3CanvasTemplate'),status=201,
    desc='Partial main-contract operation: requires the current owner, selected source environment and canvas.change grant, rechecked with session and role sources inside the save transaction. Copies a user-chosen template name plus sanitized generic-canvas node/edge layout; node titles, blocks, artifacts and conversations are not copied. The graph is validated before template and audit commit together. keep.steps, keep.runbooks and keep.content return 422; the current frontend sends steps/runbooks by default and describes a team template, while this backend saves a private template. Investigation sources and read-only steps are unavailable, so the main route remains mock.')
P['/conversations/{id}/events']['get']['responses']['200']['content'] = {'text/event-stream':{'schema':string()}}

# Search and @mentions use persisted, scoped service, incident, investigation,
# person and conversation records. No recent list or command registry exists.
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
    desc='Partial: scoped services, incidents, investigations, active visible human members and own conversations can be found with Chinese or pinyin matching. At most eight balanced hits are returned; real investigation IDs link to the canvas list until the frontend route accepts them. Recent is always empty until user navigation history exists.')
v3_route('get','/mentions','List available mention candidates',
    response=obj({'items':arr(ref('V3MentionCandidate'))}),
    query=[v3_query('q'),v3_query('kind',string(enum=['service','incident','investigation','conversation','person']))],
    desc='Partial: scoped service, incident, investigation, active visible human member and own conversation references are supported. At most 30 balanced candidates are returned; clients need a kind-specific query for a complete tab. Alert group, artifact and Runbook references have no verified source here.')
v3_route('get','/mentions/{kind}/{id}/preview','Preview an accessible reference',
    response=ref('V3MentionPreviewPartial'),
    desc='Partial: scoped service, incident, investigation, active visible human member and own conversation records have verified previews; other kinds return 503 preview_unavailable. Metrics and related records are empty until sources are connected. Incident status labels reflect persisted incident workflow state, not live alert health.')
for _path in ['/search','/mentions']:
    P[_path]['get']['responses']['413'] = {
        'description':'More than 100 matching records in a source kind exceed the bounded query; narrow the search',
        'content':{'application/json':{'schema':ref('Error')}}}
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
    desc='Partial CMDB projection only. Collector nodes require present inventory with an observed resource and successful source no older than five minutes, with matching environment. Each node is checked against resources.view in its persisted environment/application/team; a linked resource cannot borrow its application owner-team grant. Edges require both endpoints in a common authorized environment. group_by is currently unsupported (422); layout, health, blast radius, tree and Sankey are unavailable. No resources.view grant is automatically seeded from legacy RBAC.')

# Connectors and their configured provider capabilities.
S['V3TagChip'] = obj({'label':string(),'tone':string(),'mono':v3_bool()},['label'])
S['V3Connector'] = obj({**fieldnames('id brand name kind meta'),
    'category':string(enum=['model','observe','delivery','infra','im','knowledge','agent_obs','security']),
    'status':string(enum=['ok','unverified','error']), 'tags':arr(ref('V3TagChip'))})
S['V3ConnectorDetail'] = obj({**S['V3Connector']['properties'], **fieldnames('scope address checked deeplink'),
    'auth':v3_object('brand text', required=['text']), 'services':arr(string()), 'queries':arr(string()),
    'tools':arr(obj({**fieldnames('name desc'),'enabled':v3_bool()}))},
    ['id','brand','name','kind','category','status','tags','meta','scope','address','auth','checked','services','queries','tools'])
S['V3RegisteredConnectorTool'] = obj({**fieldnames('name desc'),'enabled':v3_bool()},['name','desc','enabled'])
S['V3ConnectorToolSettings'] = obj({'connector_id':string(),'environment_id':string(),
    'connector_enabled':v3_bool(),'registered_tools':arr(ref('V3RegisteredConnectorTool')),
    'agent_execution_available':v3_bool()},
    ['connector_id','environment_id','connector_enabled','registered_tools','agent_execution_available'])
S['V3ConnectorList'] = obj({'items':arr(ref('V3Connector')),
    'counts':obj({k:integer() for k in ['ok','unverified','error']}),
    'categories':arr(obj({'id':string(),'label':string(),'count':integer()}))})
S['V3ConnectorType'] = obj({**fieldnames('brand name kind version webhook_url'),
    'auth_methods':arr(v3_object('id label desc')),
    'scopes':arr(obj({**fieldnames('id label'),'required':v3_bool()})),
    'tools':arr(v3_object('name desc'))})
v3_route('get','/connector-types/{type}','Read one supported connector type',response=ref('V3ConnectorType'),
    role='viewer with scoped connectors.view',
    desc='Type-level descriptor only. Auth methods correspond to supported connection probes; unknown types return a localized 404. Version, upstream token scopes and webhook URL have no verified type-level source and are empty. Agent-executable connector tools are not wired, so tools is empty. No connector instance health or wizard side effects are implied.')
v3_route('get','/connectors','List configured connectors',role='admin',response=ref('V3ConnectorList'),
    query=[v3_query(x) for x in ['category','q']],
    desc='Counts cover all currently authorized connector registrations before category or search filters. Disabled or credential-free registrations are unverified and identified in meta. Persisted tool preferences do not produce Agent capability tags. Status ok/error reflects a persisted connection test from the last 15 minutes, not continuous health; older results become unverified with their last-check time. More than 1000 visible registrations returns 413 instead of an incomplete unpaged result.')
v3_route('get','/connectors/{id}','Read connector configuration',role='admin',response=ref('V3ConnectorDetail'),
    desc='Returns one persisted registration in the selected environment. Service names are included only after current target and application grants. Auth text distinguishes a missing credential from a registered Vault reference; it does not claim the secret was verified. Status is the most recent fresh connection probe, not continuous health; missing credentials are unverified even if an older successful check was stored. There is no connector-backed Agent executor or query binding, so tools, queries, and capability tags are empty even when registration preferences exist. More than 1000 visible target services returns 422 instead of truncation.')
v3_route('get','/connectors/{id}/tool-settings','Read stored connector tool switches',role='admin',
    response=ref('V3ConnectorToolSettings'),
    desc='Auxiliary read outside the 166 page routes. Requires current connectors.view in an authorized environment and selects the registration by ID and environment. Returns stored tool switches with localized catalog labels, connector enabled state and agent_execution_available=false; it exposes neither endpoint nor credential reference, and does not imply Agent execution. Unknown or cross-environment IDs return 404; unknown, duplicate or retired stored tools fail closed with 503 rather than silently changing the reported settings.')
v3_route('post','/connectors','Create a connector',role='admin',
    body=obj({'type':string(),'url':string(),'auth':string(),'credential_ref':string(),
        'tools':arr(string()),'webhook':v3_bool()},['type','url','auth']),
    response=obj({'id':string()},['id']),status=201,
    desc='Creates an audited environment-scoped registration. The current GitLab wizard omits Vault credential and project scope; its request is stored disabled and unverified. webhook=true records intent only and does not provision a remote Webhook. A disabled registration cannot be probed or have tools enabled; activation requires a Vault reference and explicit project binding. Fully configured non-GitLab read-only registrations may be enabled on creation.')
v3_route('post','/connectors/{id}/activate','Activate verified GitLab project binding',role='admin',
    body=obj({'credential_ref':string()},['credential_ref']),
    response=obj({'enabled':v3_bool(),'status':string(enum=['ok']),'verified_projects':integer(),'webhook_active':v3_bool()},
        ['enabled','status','verified_projects','webhook_active']),
    desc='Auxiliary V3 operation outside the 166 page routes. Requires one selected environment and connectors.manage. A disabled GitLab registration needs a Vault credential reference and at least one explicitly bound numeric GitLab project. The backend performs a GET for each exact project through GitLab, then rechecks authorization, connector configuration, and bindings before enabling it. This does not attest the token is read-only. Tools remain disabled; webhook=true remains intent only and no remote Webhook is provisioned. A failed check leaves the registration disabled; changing the project bindings requires reactivation. This is a point-in-time verification, not continuous health.')
P['/connectors/{id}/activate']['post']['responses']['502'] = {
    'description':'GitLab project verification failed',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('patch','/connectors/{id}','Update connector configuration',role='admin',
    body=obj({'tools':{**arr(obj({'name':string(),'enabled':v3_bool()},['name','enabled'])),'minItems':1}},['tools']),
    response=obj({'ok':v3_bool(),'tools':arr(ref('V3RegisteredConnectorTool'))},['ok','tools']),
    desc='Partially updates stored connector tool preferences under a row lock with current connectors.manage and atomic audit; distinct supported tool names and a present, non-null enabled boolean are required. Unknown, duplicate or retired stored tools return 409 tool_catalog_changed without silently deleting preferences. Enabling a tool on a disabled connector returns 409 connector_disabled. No object version precondition is implemented. The enabled flag gates some scoped read-only sources but is not an Agent execution grant; connector-backed Agent execution is not wired. The V3 frontend toggle currently does not call this route.')
P['/connectors/{id}']['patch']['responses']['409']['description'] = 'Connector disabled or stored tool catalog changed; no object version precondition'
v3_route('post','/connectors/{id}/test','Probe configured connector',role='admin',
    response=obj({'ok':v3_bool(),'latency_ms':integer(),'error_code':string()}),
    desc='Disabled registrations return 409 without contacting the provider. Other failures return an explicit error; a configured adapter is not silently reported healthy.')

# MCP registrations are legacy application-bound records. Only an explicitly
# reviewed registration/environment binding may appear in V3; the migration
# does not guess an environment for historical registrations.
S['V3McpServerPartial'] = obj({**fieldnames('id brand name desc transport caps last'),
    'caps':string(description='Localized unverified capability text; no V3 reviewed tool is implied.'),
    'last':string(description='Localized call-history-unavailable text, not a registration timestamp or a last-call time.'),
    'trust':obj({'label':string(),'tone':string()}),
    'apps':arr(obj({'label':string(),'tone':string()})),
    'status':string(enum=['pending'],description='Placeholder while no verified V3 connection-health state exists.'),
    'complete_v3_dto':v3_bool(), 'unavailable_fields':arr(string())})
S['V3McpDetailPartial'] = obj({**S['V3McpServerPartial']['properties'],
    'version':string(description='Localized server-version-unverified text, not the legacy registration revision.'),
    'counts':obj({k:integer(minimum=0,description='Placeholder 0; actual V3 count is unknown and listed in unavailable_fields.') for k in ['tools','resources','prompts']}),
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
    desc='Partial registration metadata only. The KPIs count visible registrations and legacy read-only bindings, not V3 reviewed tools. Unbound historical MCP records are omitted, not copied across environments. A single authorization snapshot scans at most 2000 candidate registrations; a 2001st candidate returns 413 candidate_scan_limit rather than partial items, while more than 1000 visible registrations returns 413 scope_too_large. Health, discovered capabilities, V3 tool review, Agent grants, call metrics and pending changes are unavailable; status is a pending placeholder and complete_v3_dto is false.')
P['/mcp-servers']['get']['responses']['413'] = {
    'description':'Candidate scan or visible registration limit exceeded',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/mcp-servers/{id}','Read accessible MCP registration metadata',
    response=ref('V3McpDetailPartial'),
    desc='Partial registration metadata only: legacy fixed-argument bindings and untrusted discovery observations are not V3 reviewed Agent tools, so tools and Agent apps are empty. No verified service version, live health, actual tool/resource/prompt counts, Agent grants or tool-call history exists; counts of 0 are DTO placeholders and every unknown count is named in unavailable_fields. A credential reference may be reported as registered but never verified or returned. complete_v3_dto is false.')
v3_route('patch','/mcp-servers/{id}/tools/{name}','Change a reviewed MCP tool',
    body=obj({'enabled':v3_bool()},['enabled']),
    desc='Currently always returns 503 mcp_review_unavailable after scoped authorization; no trusted discovery snapshot exists.')
P['/mcp-servers/{id}/tools/{name}']['patch']['responses'].pop('200')
v3_route('post','/mcp-servers/{id}/review','Review newly discovered MCP tools',
    desc='Currently always returns 503 mcp_review_unavailable after scoped authorization; no trusted discovery snapshot exists.')
P['/mcp-servers/{id}/review']['post']['responses'].pop('200')
S['V3McpDiscoveryObservation'] = obj({
    'snapshot_id':string(), 'mcp_server_id':string(), 'application_id':string(),
    'environment_id':string(), 'registration_version':integer(minimum=1),
    'registration_hash':string(), 'tools_hash':string(),
    'tool_count':integer(minimum=0,maximum=128), 'captured_at':string(format='date-time'),
    'trust':string(enum=['untrusted_observation']),
    'approved':{'type':'boolean','const':False},
    'health_verified':{'type':'boolean','const':False}},
    ['snapshot_id','mcp_server_id','application_id','environment_id','registration_version',
     'registration_hash','tools_hash','tool_count','captured_at','trust','approved','health_verified'])
v3_route('post','/mcp-servers/{id}/discover','Observe MCP tool declarations in an immutable snapshot',
    response=ref('V3McpDiscoveryObservation'),
    desc='Auxiliary observation route outside the 166 V3 page contracts. Requires exactly one selected environment, an explicit active binding and current connectors.manage on the persisted application. No request body is accepted. A bounded Streamable HTTP tools/list runs only after authorization; registration version, raw record digest, binding instance and authorization are rechecked before an immutable observation and management audit commit. Tool names are sorted before hashing; remote descriptions, read-only hints and JSON input schemas remain untrusted claims. Discovery does not enable, approve, execute or attest the health of any tool. A still-authorized upstream or invalid-declaration failure is audited without endpoint, credential or remote text and returns 502; a missing discovery client or persistence/audit failure returns 503. A changed registration returns 409, while absent or inaccessible initial objects return 404.')
P['/mcp-servers/{id}/discover']['post']['responses']['502'] = {
    'description':'Bounded MCP discovery failed or returned invalid declarations',
    'content':{'application/json':{'schema':ref('Error')}}}
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
    desc='Partial V3 behavior: rechecks the current application/environment change grant while the incident row is locked, then transactionally sets platform ownership and audits the change; '
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
v3_route('get','/incidents/{id}/timeline/manual','Read persisted human incident notes',
    response=obj({'items':{'type':'array','items':ref('V3IncidentTimelineEntry'),'maxItems':500},
        'scope':string(enum=['manual_only'])}, ['items','scope']),
    desc='Auxiliary read outside the 166 V3 page contracts. Resolves the incident application and environment from persisted records and checks current events.view before reading its manually authored v3_incident_timeline rows. Items are ordered by persisted created_at and ID; the actor name comes from the current user record. The response contains no inferred system or AI entries, impact, error budget, or complete incident detail. An absent or inaccessible incident returns 404; more than 500 manual entries or storage failure returns 503 without a partial list. This route does not replace GET /incidents/{id}.')
S['V3ObservedAlertRuleSummary'] = obj({**fieldnames('id folder name environment_id application_id'),
    'state':string(enum=['firing','pending','ok'])},
    ['id','folder','name','state','environment_id','application_id'])
S['V3ObservedAlertRuleDetail'] = obj({**S['V3ObservedAlertRuleSummary']['properties'],
    **fieldnames('datasource expr last_evaluation'), 'for_seconds':v3_num(),
    'labels':v3_map(string()),'annotations':v3_map(string()),
    'evaluation_ms':v3_num(),'partial':v3_bool(),'unavailable':arr(string())})
S['V3AlertRuleSummary'] = obj({**fieldnames('id folder name'),
    'state':string(enum=['firing','pending','ok']), 'schedule':string()},
    ['id','folder','name','state'])
S['V3AlertRuleList'] = obj({'items':arr(ref('V3AlertRuleSummary')),
    'counts':obj({k:integer(minimum=0) for k in ['all','firing','scheduled']},
        ['all','firing','scheduled'])}, ['items','counts'])
v3_route('get','/alert-rules','Read complete authorized alert rule summaries',
    response=ref('V3AlertRuleList'),
    desc='Rules come from configured Prometheus sources and exact server-owned file/group application bindings, with matching environment/application labels and V3 authorization. Counts describe the complete authorized result. Missing configuration, upstream failure or a result above the bound returns an error rather than synthetic or partial counts. Scheduled is zero until a trusted schedule source is connected.')
v3_route('get','/alert-rules/observed','Read provider-observed alert rule summaries',
    response=obj({'items':arr(ref('V3ObservedAlertRuleSummary')),
        'counts':v3_map(integer()),'partial':v3_bool(),
        'unavailable_environments':arr(string())}),
    desc='Auxiliary read-only backend endpoint, not the V3 AlertRule DTO. Rule groups require explicit server-side application/environment binding and matching provider labels. Unconfigured sources return 503; partial source failure returns 206.')
P['/alert-rules/observed']['get']['responses']['206'] = P['/alert-rules/observed']['get']['responses']['200']
v3_route('get','/alert-rules/observed/{id}','Read one provider-observed alert rule',
    response=ref('V3ObservedAlertRuleDetail'),
    desc='Auxiliary read-only backend endpoint. The rule must be uniquely identified within the currently authorized provider result; duplicate visible stable IDs return 503 alert_rule_identity_conflict rather than an arbitrary expression or state. An unknown ID with a failed in-scope provider returns 503 rule_source_partial; a complete miss returns 404. No historical preview, threshold condition, notification route or AI triage is inferred.')

S['V3SilenceMatcher'] = obj(fieldnames('k v'), ['k','v'])
S['V3SilencePreviewMatcher'] = obj({'k':string(),'op':string(enum=['=','!=','=~','!~']),
    'v':string(minLength=1,maxLength=256)}, ['k','op','v'])
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
    desc='Only provider silences with an exact positive non-regex environment matcher are returned. Application-scoped records also require an explicit server-owned application-label-to-persisted-ID binding to an active application in that environment and a current V3 application/team view grant. Exact environment silences with wider application scope require environment-wide silence view and manage grants. Ambiguous matchers are hidden. Affects comes from provider-observed alerts restricted to the authorized environment and application; counts cover the visible records. Broad records without exact environment binding are hidden and set unscoped_present for environment administrators (206). An authorized source failure, a duplicate visible ID or more than 500 visible records fails closed, without partial counts. Provider responses are bounded to 2 MiB.')
P['/silences']['get']['responses']['206'] = P['/silences']['get']['responses']['200']
v3_route('post','/silences/preview','Count currently matching Alertmanager alerts without creating a silence',
    body=obj({'matchers':{'type':'array','items':ref('V3SilencePreviewMatcher'),
        'minItems':1,'maxItems':20}}, ['matchers']),
    response=obj({'affects':integer(minimum=0),'sample':{'type':'array','items':string(),'maxItems':5}}, ['affects','sample']),
    desc='Read-only live snapshot in exactly one selected environment; all is rejected with 409. The selected environment is an implicit exact matcher. An exact application= provider label must be bound by server configuration to an active persisted application and requires current silences.view on that application or owner team; unknown and hidden bindings both return 404. Without an exact application matcher, current environment-wide silences.view and silences.manage are required. Affects counts all matching current alerts in that selected environment, not historical or future alerts. Sample contains at most five distinct sorted real alertname values, not representative alert records. More than 500 current-environment alerts, an upstream error, or malformed upstream null returns 503 without partial counts. No silence is created. The current frontend form and stale-result handling are not compatible with the authorization/refresh semantics, so page acceptance remains pending.')
v3_route('get','/inhibitions','Read authorized Alertmanager inhibition configuration',
    response=obj({'items':arr(ref('V3InhibitionRead')),'partial':v3_bool(),
        'unavailable':arr(string()),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool()},
        ['items','partial','unavailable','unavailable_environments','unscoped_present']),status=206,
    desc='Always 206 partial when successful, including an empty list: only rules with exact source and target environment matchers are considered. An application-scoped rule also needs a server-owned application-label-to-active-ID binding and current application or owner-team silences.view grant for every exact application matcher. Wider application scope requires both current environment-wide silences.view and silences.manage. Cross-environment, inactive, unbound, hidden, mixed, and ambiguous rules are omitted; only authorized environment administrators may receive unscoped_present=true. Alertmanager status has no per-rule activity or hit counts, so active is omitted and stat explains unavailable data. Authorization is checked before reading upstream; unconfigured or failed upstream returns 503 without partial results.')
P['/inhibitions']['get']['responses']['503'] = {
    'description':'Authorized inhibition source, scope, or application catalog is unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}

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
P['/runbooks']['get']['responses']['413'] = {
    'description':'More than 1000 candidate Runbooks exceed the unpaged response limit',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/runbooks/{id}','Read governed runbook',response=ref('V3Runbook'),
    desc='Reads the Runbook document, current object authorization and structured V3 steps in one read-only snapshot. Hidden, out-of-scope or non-Runbook objects return 404. An authorized document without a structured record returns 503 structured_steps_unavailable; an authorized structured Runbook returns 503 runbook_read_unavailable until complete usage, outcome, review lineage and execution verification are persisted. No zero usage or fabricated version timestamp is returned; persisted partial fields are available at /runbooks/{id}/record.')
for _path in ['/runbooks','/runbooks/{id}']:
    P[_path]['get']['responses']['503'] = {
        'description':'Mandatory Runbook read fields lack an authoritative persisted source',
        'content':{'application/json':{'schema':ref('Error')}}}
P['/runbooks/{id}']['get']['responses']['503']['description'] = (
    'Structured V3 steps are missing, or mandatory full-detail fields lack an authoritative persisted source')
v3_route('post','/runbooks','Create governed runbook draft',
    body=obj({'title':string(),'source':string(enum=['blank','template','postmortem','conversation','import']),
        'from':string(),'steps':arr(ref('V3RunbookCreateStep')),
        'status':string(enum=['draft','review'])},['title','source']),response=obj({'id':string()},['id']),status=201,
    desc='Requires scoped knowledge:manage grant. A nonblank source requires a validated reference; blank forbids one. Template IDs are registered server-side; imports require a nonempty Markdown upload owned by the current user in the selected environment. Conversations are checked in the same transaction; postmortem source remains unavailable. No source reference is trusted solely because the client provided an ID.')
v3_route('post','/runbooks/{id}/submit','Submit runbook for review',
    desc='Requires scoped knowledge:manage grant and maintainer ownership.',
    response=obj({'ok':v3_bool(),'status':string(enum=['review'])}))
v3_route('post','/runbooks/{id}/dry-run','Validate runbook without execution',
    role='viewer with scoped knowledge.manage',
    response=obj({'steps':integer(),'ok':v3_bool(),'mode':string(enum=['validation_only']),
        'checks':arr({'type':'object','additionalProperties':True})}),
    desc='Requires knowledge.manage because the validation preview and audit are persisted. Validates persisted steps without a request body and never runs external write steps.')
v3_route('get','/runbooks/draft','Read a source-validated Runbook outline',
    query=[v3_query('source',string(enum=['template','postmortem','conversation','import'])),v3_query('from')],
    response=obj({'title':string(),'applies_to':arr(string()),'trigger':string(),
        'steps':arr(ref('V3RunbookStep')),'sources':arr(string()),'outline':string(),
        'draft_mode':string(enum=['template_outline']),'missing':arr(string())},
        ['title','applies_to','trigger','steps','sources','outline','draft_mode','missing']),
    desc='Partial main-contract operation: only a registered template and explicit from ID return an outline with empty steps. Other validated sources return 501 draft_generator_unavailable. The complete AI-drafted page remains mock.')
P['/runbooks/draft']['get']['responses']['501'] = {
    'description':'Source-backed Runbook draft generator is not configured',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/runbooks/{id}/reviews','Review a submitted Runbook',
    body=obj({'decision':string(enum=['approve','request_changes']),'note':string()},['decision']),
    response=obj({'id':string(),'status':string(enum=['published','draft']),'version':integer(),
        'index_status':string(),'semantic_search_ready':v3_bool(),
        'index_retry_required':v3_bool()},
        ['id','status','version']),
    desc='Auxiliary governance operation. Requires scoped knowledge.manage and a reviewer other than the maintainer. Requesting changes requires a note. Approval first commits publication, then attempts indexing outside the transaction; optional response fields report actual index status, semantic search readiness and retry need. An unavailable embedder does not undo human publication.')
v3_route('post','/runbooks/{id}/reindex','Retry semantic indexing for one published Runbook',
    role='viewer with scoped knowledge.manage',
    response=obj({'id':string(),'status':string(enum=['published']),'version':integer(),
        'index_status':string(),'semantic_search_ready':v3_bool(),
        'index_retry_required':v3_bool()},
        ['id','status','version','index_status','semantic_search_ready','index_retry_required']),
    desc='Auxiliary V3 retry operation for a published Runbook in one selected environment. It rechecks the persisted object and scoped knowledge.manage, audits the request, then uses the existing governed indexer outside the transaction. Keyword publication persists when embedding fails; the response separates semantic readiness and retry need. This does not bypass the V3 object scope or use the legacy operate ACL.')
v3_route('get','/runbooks/{id}/record','Read persisted structured Runbook fields',
    response=obj({**fieldnames('id title version status owner application_id team_id when source_kind'),
        'environment_ids':arr(string()),'tags':arr(string()),'steps':arr(ref('V3RunbookCreateStep')),
        'partial':v3_bool(),'unavailable_fields':arr(string())},
        ['id','title','version','status','owner','application_id','environment_ids','team_id',
         'tags','when','steps','source_kind','partial','unavailable_fields']),
    desc='Auxiliary partial record. Usage, incident resolution and execution verification are unavailable; an older document without structured V3 steps returns 503.')
v3_route('get','/runbooks/{id}/history','Read bounded Runbook governance history',
    response=obj({'id':string(),
        'versions':arr(obj({'version':integer(),'actor_id':string(),'created_at':string()},
            ['version','actor_id','created_at'])),
        'reviews':arr(obj({'id':string(),'version':integer(),'actor_id':string(),
            'decision':string(),'note':string(),'created_at':string()},
            ['id','version','actor_id','decision','created_at'])),
        'validation_previews':arr(obj({'id':string(),'version':integer(),'actor_id':string(),
            'step_count':integer(),'readonly_valid':v3_bool(),
            'checks':arr({'type':'object','additionalProperties':True}),'created_at':string()},
            ['id','version','actor_id','step_count','readonly_valid','checks','created_at'])),
        'execution_verification_available':v3_bool()},
        ['id','versions','reviews','validation_previews','execution_verification_available']),
    desc='Auxiliary history; each collection is limited to 100 rows and fails with 413 if larger. Review notes are visible only to the maintainer or knowledge managers. No execution verification producer is connected.')
P['/runbooks/{id}/history']['get']['responses']['413'] = {
    'description':'Runbook history exceeds 100 rows in any unpaged collection',
    'content':{'application/json':{'schema':ref('Error')}}}
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
P['/postmortems/candidates']['get']['responses']['413'] = {
    'description':'More than 1000 visible eligible incidents exceed the unpaged response limit',
    'content':{'application/json':{'schema':ref('Error')}}}
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
    desc='Requires a single selected environment and current events:view plus knowledge:manage on the incident application or its persisted owner team. Locks and rechecks the resolved incident, investigation report and closing event; one postmortem per incident. Stores sections and actions with an audit record in one transaction, without executing or notifying. Publishing requires a separate human review.')
v3_route('get','/postmortems/draft','Read an evidence-only postmortem starting point',
    query=[v3_query('incident')],
    response=obj({'incident':obj({**fieldnames('id title severity window duration')},
            ['id','title','severity','window','duration']),
        'sections':arr(ref('V3PostmortemSectionInput')),
        'timeline':arr(obj({**fieldnames('at text tone')},['at','text','tone'])),
        'actions':arr(ref('V3PostmortemActionInput')),
        'draft_mode':string(enum=['evidence_only']),'missing':arr(string())},
        ['incident','sections','timeline','actions','draft_mode','missing']),
    desc='Partial main-contract operation. Uses a resolved incident and persisted close/report evidence, leaving impact, root cause and action recommendations blank. It does not generate the AI-drafted page. More than 200 timeline rows returns 413.')
P['/postmortems/draft']['get']['responses']['413'] = {
    'description':'Incident evidence timeline exceeds 200 events',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/postmortems/{id}/publish','Publish a reviewed postmortem',
    response=obj({'id':string(),'status':string(enum=['published'])},['id','status']),
    desc='Auxiliary human review operation requiring scoped knowledge.manage. The author cannot publish their own report; the resolved incident, closed investigation, retained investigation report and saved close event are checked again before publication.')
S['V3KnowledgeSearchHit'] = obj({**fieldnames('title snippet'),
    'keyword':v3_num(),'vector':v3_num(),'score':v3_num()})
v3_route('post','/knowledge/search','Search scoped knowledge with keyword/vector ranking',
    body=obj({'q':string(),'service':string(),'top_k':integer(minimum=1,maximum=20)},['q']),
    response=obj({'hits':arr(ref('V3KnowledgeSearchHit')),
        'mode':string(enum=['hybrid','keyword','none']),'degraded':v3_bool()}),
    desc='Keyword and vector candidates use the same current explicit environment, active application mapping and V3 environment/knowledge grants. A document carrying team_id must match the application current owner team and have an additional team knowledge grant. Candidate document ID/version and current grants are rechecked before output; malformed or unknown scope and source_registration_id documents are excluded pending trusted provenance. When embeddings are unavailable, returns a keyword result with degraded=true; an unavailable keyword index returns 503.')
P['/knowledge/search']['post']['responses']['413'] = {
    'description':'Authorized knowledge candidate scope is too large; narrow the search to one service',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3ObservedKnowledgeDocument'] = obj({**fieldnames('id name kind'),
    'docs':{'type':['integer','null']},
    **{key:{'type':['string','null']} for key in ['sync','chunks','last','status']}},
    ['id','name','kind','docs','sync','chunks','last','status'])
v3_route('get','/knowledge-sources/observed-documents','Read scoped published document identities',
    response=obj({'items':arr(ref('V3ObservedKnowledgeDocument')),
        'index':{'type':'null'},'partial':v3_bool(),'reason':string(),
        'truncated':v3_bool()},
        ['items','index','partial','reason','truncated']),
    desc='Auxiliary read-only backend endpoint, not the V3 KnowledgeSource DTO. Current document title, explicit environment, active application mapping, matching owner team and V3 grants are rechecked before output. Malformed or unknown scope and source_registration_id documents are excluded. This document-only projection does not join available source registrations, observed reads or index attribution, so reason is document_only_projection and sync/index fields remain unknown. No document body is returned.')
S['V3KnowledgeSourceRegistration'] = obj({**fieldnames('id environment_id application_id team_id name type url schedule chunk registration_status created_at'),
    'spaces':arr(string()),'inherit_acl':v3_bool()},
    ['id','environment_id','name','type','url','spaces','schedule','chunk','inherit_acl','registration_status','created_at'])
S['V3KnowledgeSourceRegistrationInput'] = obj({**fieldnames('name type url secret_ref application_id team_id schedule chunk'),
    'spaces':arr(string()),'inherit_acl':v3_bool()},
    ['name','type','url','spaces','schedule','chunk','inherit_acl'])
v3_route('get','/knowledge-sources/registrations','Read scoped, unverified source registrations',
    response=obj({'items':arr(ref('V3KnowledgeSourceRegistration')),'index':{'type':'null'},
        'complete_v3_dto':v3_bool(),'truncated':v3_bool(),'unavailable_fields':arr(string())},
        ['items','index','complete_v3_dto','truncated','unavailable_fields']),
    desc='Auxiliary registration inventory only; no connection, sync, document count or index status is claimed. Returns 422 when a bounded candidate scan cannot prove completeness.')
v3_route('post','/knowledge-sources/registrations','Register scoped source configuration without activating sync',
    body=ref('V3KnowledgeSourceRegistrationInput'),status=201,
    response=obj({'id':string(),'registration_status':string(enum=['unverified'])},['id','registration_status']),
    desc='Auxiliary operation. A single public HTTPS Markdown Web page may omit secret_ref, use no spaces, manual schedule and inherited source access; other source kinds still require a Vault reference, never a plaintext credential. Registration does not prove connection or begin ingestion. Requires one environment and knowledge.manage; audit is transactional.')
v3_route('post','/knowledge-sources/registrations/{id}/sync','Observe one registered source through a configured read-only reader',
    response=obj({'read_id':string(),'observed_count':integer(minimum=0),
        'index_state':string(enum=['not_proven']),'complete_v3_dto':v3_bool()},
        ['read_id','observed_count','index_state','complete_v3_dto']),
    desc='Auxiliary lineage operation requiring one environment and knowledge.manage. Explicit AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true configures only the application-bound, credential-free, single-page public HTTPS Markdown reader. An environment-only public Web registration returns 422 source_application_required before network access; unset/false or another unconfigured source type returns 501 source_reader_unavailable; a credentialed Web source is not supported by this reader. Application/team liveness is checked before network access and again under transaction before persistence. Success proves only a complete bounded ACL-verified hash observation; it does not import document bodies or prove indexing.')
P['/knowledge-sources/registrations/{id}/sync']['post']['responses']['501'] = {
    'description':'No read-only source reader is configured',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/knowledge-sources/registrations/{id}/evidence','Read observed source lineage and attributed index evidence',
    response=obj({'id':string(),'last_read':{'type':['object','null']},
        'index':{'type':['object','null']},'reason':string(),'complete_v3_dto':v3_bool()},
        ['id','last_read','index','complete_v3_dto']),
    desc='Auxiliary read of current authorized registration. An absent read returns null evidence; attributed ready documents and chunks are reported only for an observed item and never imply a complete source index. No source content is returned.')
v3_route('post','/knowledge-sources/test','Test a knowledge source connection',
    body=obj(fieldnames('type url secret_ref'),['type','url']),
    response=obj({'ok':v3_bool(),'message':string(),'connected':v3_bool(),
        'observed_at':string(format='date-time'),'persistent':v3_bool(),
        'scope_verified':v3_bool(),'spaces':arr(string())},
        ['ok','message','connected','observed_at','persistent','scope_verified']),
    desc='A configured source-specific read-only prober can return a point-in-time successful access observation only after validation and knowledge.manage. Production AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true configures an application-bound observation Reader, not a Prober: an unsaved public Web test still returns 501 source_connection_test_unavailable. Other source kinds require a Vault reference and return 501 without a configured Prober. Any future successful probe would not prove persisted sync, indexing, user-wide access or application/team scope.')
P['/knowledge-sources/test']['post']['responses']['501'] = {
    'description':'No source-specific read-only connection prober is configured',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/knowledge-sources/test']['post']['responses']['503'] = {
    'description':'Configured read-only source probe, authorization or storage unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}

# Source-backed memory governance. These partial operations do not satisfy the
# frozen MemoryView or frontend memory-action contract.
S['V3MemoryFactEvidence'] = obj({**fieldnames('id fact source state environment_id application_id team_id'),
    'source_version':integer(),'source_current':v3_bool(),
    'confirmed':{'type':['string','null'],'format':'date-time'},
    'created_at':string(format='date-time'),'revision':integer()},
    ['id','fact','source','source_version','state','environment_id','confirmed','created_at','revision'])
v3_route('get','/memory','Read the complete V3 memory view',status=503,
    desc='The complete MemoryView requires unavailable layer counts, policy, recall, confidence and expiry evidence. Always returns 503 memory_view_unavailable with partial_endpoint /api/v1/memory/facts; the main contract remains mock.')
P['/memory']['get']['responses']['503'] = {
    'description':'Complete MemoryView is unavailable; response identifies the partial fact endpoint',
    'content':{'application/json':{'schema':obj({'error':obj({'code':string(),'message':string(),
        'partial_endpoint':string()},['code','message','partial_endpoint'])},['error'])}}}
v3_route('get','/memory/facts','Read current scoped source-backed memory facts',
    response=obj({'items':arr(ref('V3MemoryFactEvidence')),'partial':v3_bool(),
        'unavailable_fields':arr(string())},['items','partial','unavailable_fields']),
    desc='Auxiliary partial facts. Only current published source versions and current knowledge.view scope are returned. Confidence, recall, expiry, layer counts and policy are unavailable; more than 500 candidate facts returns 413.')
P['/memory/facts']['get']['responses']['413'] = {
    'description':'More than 500 candidate memory facts exceed the bounded view',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/memory/facts/{id}/{action}','Confirm, reject or expire a source-backed memory fact',
    response=obj({'fact':{'type':'object','additionalProperties':True},
        'fact_redacted':v3_bool(),'partial':v3_bool(),'unavailable_fields':arr(string())},
        ['fact','fact_redacted','partial','unavailable_fields']),
    desc='Partial main-contract operation. Only confirm, reject and expire are supported; no forget action. Requires knowledge.manage, a current published source and a valid state transition. Rejected or expired fact text is redacted in the response; frontend action semantics remain mock.')
v3_route('post','/memory/conflicts/resolve','Resolve an open conflict between two persisted facts',
    body=obj({'keep':string(),'drop':string()},['keep','drop']),
    response=obj({'id':string(),'state':string(enum=['resolved']),
        'keep':string(),'drop':string()},['id','state','keep','drop']),
    desc='Partial main-contract operation. Requires actual distinct fact IDs for both keep and drop, an open conflict, current sources and knowledge.manage. The current frontend does not submit this body, so its interaction remains mock.')

# Persisted Agent governance and reported observations. The runtime router,
# evaluator, prompt trial, trace producer and tool executor are not connected.
S['V3AgentEvidenceSummary'] = obj({**fieldnames('id icon name desc version level status runs_7d success avg_tokens status_source'),
    'score':{'type':'null'},'routing_connected':v3_bool(),
    'available':obj({'evaluation':v3_bool(),'usage':v3_bool()},['evaluation','usage'])},
    ['id','icon','name','desc','version','level','status','score','runs_7d',
     'success','avg_tokens','available','status_source','routing_connected'])
v3_route('get','/agents/lifecycle','Read bounded persisted Agent lifecycle evidence',
    response=obj({'stages':arr({'type':'object','additionalProperties':True}),
        'agents':arr(ref('V3AgentEvidenceSummary')),
        'todos':arr({'type':'object','additionalProperties':True}),
        'missing':arr(string()),'partial':v3_bool(),'truncated':v3_bool()},
        ['stages','agents','todos','missing','partial','truncated']),
    desc='Partial main-contract projection, at most 200 Agents. Score is null and usage/routing are unavailable; the current frontend requires a number and must not render this as a complete Lifecycle.')
v3_route('get','/agents/{id}','Read one persisted Agent and version metadata',
    response=obj({**S['V3AgentEvidenceSummary']['properties'], **fieldnames('owner account summary'),
        'versions':arr({'type':'object','additionalProperties':True}),
        'truncated':v3_bool(),
        'release':obj({**fieldnames('from to'), 'target':integer(),
            'configured_canary_percent':integer(),
            'observed_traffic':{'type':'null'},
            'current':{'type':'null'},'tokens':{'type':'null'},
            'gates':arr({'type':'object','additionalProperties':True}),
            'routing_connected':v3_bool(),'missing':arr(string())},
            ['from','to','target','configured_canary_percent','observed_traffic',
             'current','tokens','gates','routing_connected','missing']),
        'diff':arr({'type':'object','additionalProperties':True}),
        'config':arr({'type':'object','additionalProperties':True}),
        'scope':{'type':'object','additionalProperties':True}},
        S['V3AgentEvidenceSummary']['required'] +
        ['owner','account','summary','versions','truncated','release','diff','config','scope']),
    desc='Partial main-contract projection: at most 100 versions are returned and truncated reports whether more exist. Version status may be draft; actual traffic and token counts are unavailable. Null release fields are incompatible with the current Agent page; no live routing is implied.')
v3_route('post','/agents/{id}/rollout','Save a gated Agent release configuration',
    role='viewer with scoped agents.manage',
    body=obj({'to':string(),'percent':integer(minimum=1,maximum=100),
        'override':v3_bool(),'override_reason':string()},['to','percent']),
    response=obj({'id':string(),'to':string(),'configured_percent':integer(),
        'observed_traffic':{'type':'null'},'routing_connected':v3_bool(),
        'gate_experiment_id':string(),'override':v3_bool()},
        ['id','to','configured_percent','observed_traffic','routing_connected','override']),
    desc='Persists requested release configuration only; no traffic router is connected. Requires the latest passing evaluation for current stable to candidate. Override requires an enabled human administrator, a nonempty override_reason and audit; machine identities cannot override. The current frontend omits that reason. Observed traffic remains null.')
v3_route('post','/agents/{id}/rollback','Save a rollback release configuration',
    role='viewer with scoped agents.manage',
    response=obj({'id':string(),'to':string()},['id','to']),
    desc='Requires a prior persisted release target. Saves governance metadata only; no traffic router is connected and no actual traffic change is claimed.')
v3_route('patch','/agents/{id}/level','Change an Agent permission level within its environment cap',
    role='viewer with scoped agents.manage',
    body=obj({'level':string(enum=['L0','L1','L2','L3'])},['level']),
    response=obj({'level':string(),'max_level':string()},['level','max_level']),
    desc='Requires scoped agents.manage; a requested level above the persisted environment maximum returns 409. This metadata does not grant an unconnected runtime tool execution path.')
v3_route('get','/prompts','List bounded persisted Prompt versions',
    query=[v3_query('q',string(maxLength=160))],
    response=obj({'items':arr(obj({**fieldnames('id kind name version')},
        ['id','kind','name','version'])),'truncated':v3_bool(),
        'partial':v3_bool(),'unavailable_fields':arr(string())},
        ['items','truncated','partial','unavailable_fields']),
    desc='At most 200 visible persisted Prompt identities with a nonzero version. Optional q matches ID or name, trimmed and bounded to 160 Unicode characters; excess returns localized 422. More than 200 visible matches returns 413 prompt_list_too_large rather than silent truncation. Skills and publication status are unavailable fields; runtime binding is not attested. The full Prompt page remains mock.')
P['/prompts']['get']['responses']['413'] = {
    'description':'More than 200 visible Prompt identities; narrow q',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/prompts']['get']['responses']['422'] = {
    'description':'Search term exceeds 160 Unicode characters; message follows Accept-Language zh/en',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/prompts/{id}','Read one stored Prompt version',
    response=obj({**fieldnames('id name version notes'),
        'variables':arr(string()),
        'used_by':string(),'lines':arr(obj({'text':string()},['text'])),
        'registered_agent':obj(fieldnames('id name'),['id','name']),
        'scope':obj(fieldnames('application_id environment_id'),['application_id','environment_id']),
        'partial':v3_bool(),'unavailable_fields':arr(string())},
        ['id','name','version','variables','used_by','lines','notes','registered_agent','scope','partial','unavailable_fields']),
    desc='Reads the current persisted Prompt version and registered Agent under one scoped, authorized transaction. Variables are parsed from persisted text; lines preserve the text for the frontend editor. Publication status, release notes and runtime usage are unavailable, with localized explanatory used_by and notes. Unknown or hidden Prompt returns 404; a Prompt with no version returns 409; storage or policy failure returns 503. This does not prove a runtime-bound Prompt or complete frontend page.')
for _status, _description in {
    '404':'Unknown or hidden Prompt identity',
    '409':'Registered Prompt has no persisted version',
    '503':'Prompt storage, authorization policy, or current version is unavailable',
}.items():
    P['/prompts/{id}']['get']['responses'][_status] = {
        'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/prompts/{id}/versions','Store a new Prompt text revision',
    role='viewer with scoped agents.change',
    body=obj({'text':string()},['text']),response=obj({'version':string(pattern=r'^v[1-9][0-9]*$')},['version']),status=201,
    desc='Stores a nonblank text revision of at most 20000 Unicode characters with original whitespace and transactional audit, returning its vN display version. Invalid JSON, multiple JSON objects or invalid text return 422 with an Accept-Language zh/en message. Requires a registered Prompt under the current scoped Agent; it does not publish or execute the Prompt.')
P['/prompts/{id}/versions']['post']['responses']['422'] = {
    'description':'Invalid JSON body or Prompt text; message follows Accept-Language zh/en',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/prompts/{id}/trial','Run a Prompt comparison trial',
    role='viewer with scoped agents.change',
    body=obj({'case':string(),'a':string(),'b':string()},['case','a','b']),status=503,
    desc='Always returns 503 prompt_trial_unavailable after request and authorization validation; no trial runner or model execution is configured.')
P['/prompts/{id}/trial']['post']['responses']['503'] = {
    'description':'Prompt trial runner is unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/agent-tools','Read persisted Agent tool governance metadata',
    query=[v3_query('kind',string(enum=['all','read','write']))],
    response=obj({'items':arr({'type':'object','additionalProperties':True}),
        'levels':arr({'type':'object','additionalProperties':True}),
        'env_limits':arr(obj(fieldnames('env max note'),['env','max','note'])),
        'rules':arr(string()),
        'missing':arr(string()),'truncated':v3_bool()},
        ['items','levels','env_limits','rules','missing','truncated']),
    desc='At most 200 registered tools. Calls/errors are unavailable and executable is false; no live tool executor is connected. The full ToolsView remains mock.')
v3_route('get','/evals/{id}','Read one stored Agent evaluation experiment',
    response=obj({**fieldnames('id title status baseline_version candidate_version result_ref created_at'),
        'dataset':string(),'meta':string(),
        'metrics':{'type':['object','null'],'additionalProperties':True},
        'radar':{'type':'null'},'cases':arr({'type':'object','additionalProperties':True}),
        'datasets':arr({'type':'object','additionalProperties':True}),
        'evaluators':arr({'type':'object','additionalProperties':True}),
        'missing':arr(string()),'partial':v3_bool()},
        ['id','title','dataset','meta','status','baseline_version','candidate_version',
         'metrics','radar','cases','datasets','evaluators','result_ref','created_at','missing','partial']),
    desc='Partial stored evaluation record. Dataset, radar and case detail are unavailable; a passed status is a persisted trusted-producer report, not proof that this service ran quality thresholds.')
v3_route('post','/evals','Create pending Agent evaluation metadata',
    role='viewer with scoped agents.change',
    body=obj({**fieldnames('agent_id title baseline_version candidate_version')},
        ['agent_id','title','baseline_version','candidate_version']),
    response=obj({'id':string(),'status':string(enum=['pending']),
        'execution_available':v3_bool()},['id','status','execution_available']),status=201,
    desc='Requires two distinct known versions. Stores a pending experiment only; it does not queue or execute evaluation, and execution_available is false. The current frontend rerun request has no matching body.')
v3_route('get','/eval-cases/{id}','Read one partially evidenced evaluation case',
    response=obj({**fieldnames('id title queue expected_ref'),
        'labels':arr({'type':'object','additionalProperties':True}),
        **{key:{'type':'null'} for key in ['alert','evidence','expected','output','baseline','auto']},
        'missing':arr(string()),'partial':v3_bool()},
        ['id','title','queue','expected_ref','labels','alert','evidence','expected',
         'output','baseline','auto','missing','partial']),
    desc='At most 50 human labels; input evidence, expected output, baseline and automatic scoring are unavailable. This does not satisfy the frontend EvalCase DTO.')
v3_route('post','/eval-cases/{id}/labels','Record one human evaluation label',
    role='viewer with scoped agents.change',
    body=obj({'next':string(enum=['correct','incorrect','partial','unsure']),
        'note':string()},['next']),
    response=obj({'next':string(),'id':string()},['next','id']),
    desc='Requires an explicit next label and optional note up to 2000 runes; saved with audit. The current frontend sends different fields and would receive 422.')
v3_route('get','/agent-runs','Read bounded reported Agent runs',
    query=[v3_query('filter',string(enum=['all','failed','blocked','negative']))],
    response=obj({'kpis':arr({'type':'object','additionalProperties':True}),
        'trend':{'type':'null'},'durations':{'type':'null'},
        'items':arr({'type':'object','additionalProperties':True}),
        'counts':{'type':'null'},'partial':v3_bool(),'truncated':v3_bool(),
        'missing':arr(string())},
        ['kpis','trend','durations','items','counts','partial','truncated','missing']),
    desc='At most 200 persisted run reports. Complete steps, aggregate KPIs, trends and counts are unavailable; no live runtime producer is connected.')
v3_route('get','/agent-runs/{id}','Read one reported Agent run and bounded spans',
    response=obj({'id':string(),'chain':arr(obj(fieldnames('label value icon'),
            ['label','value','icon'])),
        'stats':arr(obj(fieldnames('k v'),['k','v'])),
        'steps':arr({'type':'object','additionalProperties':True}),
        'total':{'type':['number','null']},'context':{'type':'null'},
        'budget':string(),'isolated':arr({'type':'object','additionalProperties':True}),
        'memory':{'type':'null'},'partial':v3_bool(),'missing':arr(string())},
        ['id','chain','stats','steps','total','context','budget','isolated','memory','partial','missing']),
    desc='At most 500 reported spans, without complete conversation, context, memory or token accounting. Does not satisfy the RunDetail page DTO.')
v3_route('get','/agent-runs/{id}/trace','Read bounded reported Agent trace metadata',
    response=obj({'id':string(),'agent':string(),'status':string(),
        'stats':arr({'type':'object','additionalProperties':True}),
        'scores':arr({'type':'object','additionalProperties':True}),
        'session':string(),'turns':arr({'type':'object','additionalProperties':True}),
        'total':{'type':['number','null']},
        'spans':arr({'type':'object','additionalProperties':True}),
        'partial':v3_bool(),'missing':arr(string())},
        ['id','agent','status','stats','scores','session','turns','total','spans','partial','missing']),
    desc='At most 500 producer-reported spans; LLM messages, tool arguments/results, scores and complete session turns are unavailable. Reported tool spans do not prove two-phase executor audit. The full AgentTrace remains mock.')
v3_route('post','/agent-runs/{id}/feedback','Save a user vote for a completed Agent run',
    role='viewer with scoped agents.change',
    body=obj({'vote':string(enum=['up','down'])},['vote']),
    response=obj({'vote':string(enum=['up','down'])},['vote']),
    desc='One current-user vote is upserted on a completed run with transactional audit. An active run returns 409; this does not derive evaluation quality metrics.')
v3_route('get','/guardrails','Read persisted Agent guardrail policies and reported events',
    query=[v3_query('filter',string(enum=['all','blocked','masked']))],
    response=obj({'policies':arr({'type':'object','additionalProperties':True}),
        'trend':{'type':'null'},'events':arr({'type':'object','additionalProperties':True}),
        'counts':{'type':'null'},'detail':{'type':'null'},
        'partial':v3_bool(),'truncated':v3_bool(),'missing':arr(string())},
        ['policies','trend','events','counts','detail','partial','truncated','missing']),
    desc='At most 200 policies and events. Runtime enforcement, trend, counts and detail are unproven; events rely on a trusted reporting hook and the full GuardrailsView remains mock.')
v3_route('patch','/guardrails/{id}','Change one persisted guardrail policy setting',
    role='viewer with scoped agents.manage',
    body=obj({'enabled':v3_bool()},['enabled']),
    response=obj({'id':string(),'enabled':v3_bool(),'revision':integer()},
        ['id','enabled','revision']),
    desc='Scoped agents.manage setting with audit and a persisted policy revision; a forced guardrail cannot be disabled. This does not prove a live runtime consumes the policy.')

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
         'Configured targets and bindings are not live evidence. A status filter returns 422 until live status is connected. '
         'A bounded scan covers all authorized services before response; more than 1000 visible rows returns 422 result_limit rather than an incomplete total.')
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
S['V3DiscoveredService'] = obj({**fieldnames('id resource_id uid environment_id cluster namespace kind image owner_hint tier_hint owner_label_hint'),
    'replicas':{'type':['integer','null']},'matched':arr(string()),'missing':arr(string())},
    ['id','resource_id','uid','environment_id','cluster','namespace','kind','image',
     'owner_hint','tier_hint','replicas','matched','missing'])
v3_route('get','/services/discover','Discover scoped workloads',
    response=obj({'cluster':string(),'items':arr(ref('V3DiscoveredService')),
                  'registered':integer(),'ambiguous_workloads':integer()},
                 ['cluster','items','registered','ambiguous_workloads']),
    query=[v3_query('cluster')],
    desc='Returns current, present Kubernetes workload inventory after resources.view and services.view checks. The optional single cluster is filtered before the 2000-candidate scan bound; repeated cluster values are rejected. A 20-second request budget covers inventory, policy and namesake checks. resource_id is the stable CMDB registration identity; uid is the source Kubernetes UID. Candidates blocked by trusted cross-cluster namesakes or raw same-name identities in the registration scope are omitted and counted in ambiguous_workloads without exposing those identities. registered counts authorized services across selected environments, not within the displayed cluster. Owner and tier are evidence hints, replicas may be null, and matched/missing are currently empty. The frontend does not yet send cluster or resource_id and still displays fixed service metadata, so this is not the complete V3 page contract.')
S['V3ServiceRegistration'] = obj({'items':arr(obj({
    'id':string(), 'namespace':string(), 'resource_id':string(), 'tier':string(enum=['T0','T1','T2','T3']),
    'owner':string(), 'slo':string(), 'p99':string()},['id']))},['items'])
v3_route('post','/services','Register scoped service metadata',
    role='admin',body=ref('V3ServiceRegistration'),
    response=obj({'created':integer(minimum=1)},['created']),status=201,
    desc='Atomically registers 1-50 service profiles. Optional resource_id must be the stable CMDB identity from discovery and requires namespace. It is revalidated in the selected environment for presence, UID, freshness, namespace, unclaimed state and resources.view authorization under transaction locks. Without resource_id, a name/namespace must resolve uniquely or remain a manual-only registration; stale, claimed, mismatched or ambiguous inventory returns 409. Registration is audited.')
S['V3MappingCell'] = obj({'state':string(enum=['ok','suggested','missing']),
    'value':string(),'brand':string()},['state'])
S['V3ServiceMapping'] = obj({'systems':arr(v3_object('id brand label')),
    'rows':arr(obj({'service':string(),'cells':v3_map(ref('V3MappingCell'))},['service','cells'])),
    'suggestions':integer(minimum=0)},['systems','rows','suggestions'])
v3_route('get','/service-mappings','Read persisted service connector mappings',
    response=ref('V3ServiceMapping'),
    desc='Shows only bindings for authorized applications and environments; missing means no persisted binding, not a health check. Pending suggestions are shown only if their explicit GitLab project target, activation evidence and binding revision remain current. A V3-only validator permits exactly that source-derived GitLab binding while passing every other binding to the unchanged platform validator. Activation is historical project-access evidence; a recent generic connector probe does not prove current project access. More than 1000 visible services returns 422 result_limit instead of an incomplete map.')
v3_route('put','/service-mappings','Reject unavailable full mapping save',
    status=501,response=ref('Error'),
    desc='The frozen page sends no request body, and GET cells contain display values without stable service, environment, connector, target identities or binding revisions. This registered route remains outside the V3 allowed-route table and returns 501 v3_not_implemented before its fallback handler; it writes no binding or audit. No accepted full-save request schema or success response is claimed.')
P['/service-mappings']['put']['responses']['501']['description'] = 'Full mapping save is unavailable in V3'
v3_route('post','/service-mappings/discover','Stage scoped service mapping candidates',
    response=obj({'suggestions':integer(minimum=0)},['suggestions']),
    desc='Partial operation in exactly one selected environment. Requires current services.change on each visible service and environment connectors.view/manage. Only a single explicitly enrolled numeric GitLab project target per service is eligible; connector activation evidence must postdate the target binding and a recent successful check must exist. The 15-minute candidate is saved without changing the service binding. A V3-only validator checks the exact source-derived GitLab binding and delegates all other bindings to the unchanged platform validator. More than 500 visible unmapped source rows returns 422. No label, name or deployment inference is performed; the frontend does not refresh the list after discovery.')
v3_route('post','/service-mappings/accept','Accept current scoped service mapping candidates',
    body=obj({'cells':{'type':'array','items':string(),'maxItems':1000}},['cells']),
    response=obj({'accepted':integer(minimum=0)},['accepted']),
    desc='Partial operation in exactly one selected environment. Only service:code cells are accepted after transaction-local services.view/change and connectors.view/manage checks, a unique unexpired candidate, current source identity, activation evidence, sole GitLab project target, binding revision and V3-only exact GitLab binding validation. All other bindings still use the unchanged platform validator. Binding, accepted candidate and audit commit together; a verified repeat can return accepted=0. The frontend marks accepted before response and does not recover on failure; the page contract remains incomplete.')
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
S['V3GitLabMergeRequestRecord'] = obj({**fieldnames(
    'iid provider service_id environment_id binding_id connector_id project_id state source_branch target_branch sha merge_commit_sha squash_commit_sha created_at updated_at'),
    'merged_at':{'type':['string','null'],'format':'date-time'}},
    ['iid','provider','service_id','environment_id','binding_id','connector_id','project_id',
     'state','source_branch','target_branch','sha','merge_commit_sha','squash_commit_sha',
     'created_at','updated_at','merged_at'])
S['V3GitLabMergeRequestRecord']['properties']['provider'] = string(enum=['gitlab'])
S['V3GitLabMergeRequestRecord']['properties']['state'] = string(enum=['opened','closed','locked','merged'])
for timestamp in ('created_at','updated_at'):
    S['V3GitLabMergeRequestRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/merge-requests','Read bound GitLab merge request metadata',
    query=[v3_query('service_id'),v3_query('binding_id'),
           v3_query('page',integer(minimum=1,maximum=1000)),
           v3_query('state',string(enum=['all','opened','closed','locked','merged'])),
           v3_query('target_branch')],
    response=obj({'items':arr(ref('V3GitLabMergeRequestRecord')),
                  'page':integer(minimum=1),'page_size':integer(enum=[20]),
                  'next_page':integer(minimum=0)},['items','page','page_size','next_page']),
    desc='Auxiliary read-only metadata from one explicitly bound numeric GitLab project in the selected environment. Requires services.view, connectors.view and the enabled list_merge_requests tool. Source and authorization are rechecked after the bounded upstream GET. Returns branches, state, commit identifiers and GitLab times only, not diff, deployment, risk or alert correlation. Invalid filters return 422, revoked or changed binding returns 404/409, unavailable source returns 503 and invalid upstream response returns 502.')
for parameter in P['/changes/merge-requests']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
P['/changes/merge-requests']['get']['responses']['502'] = {
    'description':'GitLab merge request source failed or returned an invalid bounded response',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3GitLabCommitRecord'] = obj({**fieldnames(
    'sha provider service_id environment_id binding_id connector_id project_id title authored_at committed_at'),
    'parent_shas':arr(string())},
    ['sha','provider','service_id','environment_id','binding_id','connector_id','project_id',
     'title','authored_at','committed_at','parent_shas'])
S['V3GitLabCommitRecord']['properties']['provider'] = string(enum=['gitlab'])
for timestamp in ('authored_at','committed_at'):
    S['V3GitLabCommitRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/commits/{sha}','Read bound GitLab commit metadata',
    query=[v3_query('service_id'),v3_query('binding_id')],
    response=ref('V3GitLabCommitRecord'),
    desc='Auxiliary read-only metadata for an exact 40- or 64-hex commit SHA from one explicitly bound numeric GitLab project. Requires services.view, connectors.view and the separate enabled get_commit tool; get_commit_diff never grants this read. Source and authorization are rechecked after the bounded upstream GET. Returns title, times and parent SHAs, not message body, diff, author email, deployment or risk. Invalid SHA/target returns 422, missing or revoked record returns 404, changed binding returns 409, unavailable source returns 503 and invalid upstream response returns 502.')
for parameter in P['/changes/commits/{sha}']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
    if parameter['name'] == 'sha':
        parameter['schema'] = string(pattern='^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$')
P['/changes/commits/{sha}']['get']['responses']['502'] = {
    'description':'GitLab commit source failed or returned invalid bounded metadata',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3MetricProbe'] = obj({'status':string(enum=['observed','no_samples','not_registered','not_configured','unavailable','truncated']),
    'series':integer(minimum=0),'samples':integer(minimum=0)},['status','series','samples'])
S['V3IncompleteMetrics'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'service':string(),'environment_id':string(),'available':v3_map(ref('V3MetricProbe')),
    'missing':arr(string())},['error','service','environment_id','available','missing'])
v3_route('get','/metrics/dashboard','Inspect scoped metric dashboard readiness',
    query=[v3_query('service'),v3_query('range',string(enum=['1h'])),v3_query('align',string(enum=['true','false']))],
    desc='Requires exactly one persisted service/environment Prometheus binding and current services.view. Ambiguous distinct bindings return 409 ambiguous_binding; excessive authorized scope returns 422 scope_limit. The currently registered queries cannot fill the required V3 MetricsDashboard; when scoped data is available, returns 503 dashboard_unavailable with observed query availability and missing sections. No KPI or SLO values are fabricated.')
P['/metrics/dashboard']['get']['responses'].pop('200')
P['/metrics/dashboard']['get']['responses']['503'] = {
    'description':'Complete V3 dashboard unavailable from registered sources',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteMetrics')]}}}}
S['V3RegisteredMetricWindow'] = obj({'start':string(format='date-time'),
    'end':string(format='date-time'),'step_seconds':integer(minimum=30,maximum=30)},
    ['start','end','step_seconds'])
S['V3RegisteredMetricSample'] = obj({'timestamp':v3_num(),'value':string()},
    ['timestamp','value'])
S['V3RegisteredMetricSamples'] = obj({'application_id':string(),
    'environment_id':string(),'query_id':string(enum=['request_rate','http_p95_seconds']),
    'source':string(enum=['prometheus']),'window':ref('V3RegisteredMetricWindow'),
    'captured_at':string(format='date-time'),'status':string(enum=['observed','no_samples']),
    'samples':arr(ref('V3RegisteredMetricSample'))},
    ['application_id','environment_id','query_id','source','window','captured_at','status','samples'])
S['V3RegisteredMetricSamples']['properties']['samples']['maxItems'] = 32
v3_route('get','/metrics/samples','Read bounded registered Prometheus metric samples',
    query=[v3_query('service'),v3_query('query_id',string(enum=['request_rate','http_p95_seconds']))],
    response=ref('V3RegisteredMetricSamples'),
    desc='Auxiliary read for one selected environment and a persisted service with exactly one explicit V3 Prometheus metric-resource target. Requires current services.view and connectors.view plus an enabled query_metrics tool. The query ID maps to an operator-reviewed exact connector/application/environment/resource PromQL catalog entry; identifiers do not prove expression data scope. A bounded 15-minute, 30-second-step scalar range read rechecks binding and authorization after the provider returns. Returns at most 32 samples with no labels, raw expression, endpoint or credential. Missing source/catalog returns 503; ambiguous or changed binding returns 409; invalid request/environment returns 422. This does not complete the MetricsDashboard page.')
for _parameter in P['/metrics/samples']['get']['parameters']:
    if _parameter['name'] in ('service','query_id'):
        _parameter['required'] = True
for _status, _description in [('409','Metric binding is ambiguous or changed'),
    ('422','Invalid metric request or environment')]:
    P['/metrics/samples']['get']['responses'][_status] = {
        'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
S['V3LogLine'] = obj({**fieldnames('time service message trace'),
    'level':string(enum=['ERROR','WARN','INFO'])})
S['V3IncompleteLogs'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'available':obj({'total':string(),'errors':string(),
        'histogram':arr(arr(integer())),'items_sample':arr(ref('V3LogLine'))},
        ['total','errors','histogram','items_sample']),
    'missing':arr(string())},['error','available','missing'])
v3_route('get','/logs','Search configured log provider',
    response=obj({'total':string(),'errors':string(),'histogram':arr(arr(integer())),
        'items':arr(ref('V3LogLine')),'patterns':arr({'type':'object','additionalProperties':True})}),
    query=[v3_query(x) for x in ['q','levels','service']],
    desc='Requires explicit persisted service/environment/owner-team binding and current logs.view. A normal query returns 503 log_patterns_unavailable with exact available counts, histogram and bounded line sample; no pattern counts or trends are inferred from that sample. Only an explicitly empty levels selection returns a complete 200 empty result. Distinct authorized bindings return 409 ambiguous_binding; more than 3 targets or other excessive authorized scope returns 422 scope_limit.')
P['/logs']['get']['responses']['200']['description'] = 'Explicitly empty levels selection; no upstream log query'
P['/logs']['get']['responses']['503'] = {
    'description':'Complete log patterns unavailable or configured provider failed',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteLogs')]}}}}
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
        'status':string(enum=['ok','error']),
        'attrs':arr(obj(fieldnames('k v'),['k','v'])),
        'events':arr(obj({'at':string(format='date-time'),**fieldnames('name text')},['at','name']))})),
    'graph':obj({'nodes':arr({'type':'object','additionalProperties':True}),
        'edges':arr({'type':'object','additionalProperties':True})}),
    'partial':{'type':'boolean','const':True},'missing':arr(string())},
    ['partial','missing'])
S['V3IncompleteTraceList'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'scope':string(enum=['app']),'view':string(),'took':string(),
    'observed_sample':arr(ref('V3TraceRow')),'missing':arr(string())},
    ['error','scope','view','took','observed_sample','missing'])
v3_route('get','/traces','Query scoped traces',
    query=[v3_query(x) for x in ['scope','view','service']],
    desc='Requires explicit persisted service/environment/owner-team Tempo binding and current traces.view. A bounded trace sample cannot establish full totals, facets, histogram or scatter population, so returns 503 trace_aggregation_unavailable with observed_sample and missing sections even for an empty sample. Agent scope is not configured; TraceQL q returns 422. Distinct authorized bindings return 409 ambiguous_binding; more than 6 targets or other excessive authorized scope returns 422 scope_limit.')
P['/traces']['get']['responses'].pop('200')
P['/traces']['get']['responses']['503'] = {
    'description':'Complete trace aggregation unavailable or scoped provider failed',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteTraceList')]}}}}
v3_route('get','/traces/{id}','Read scoped trace',response=ref('V3TraceView'),
    query=[v3_query('service')],
    desc='Requires explicit persisted service/environment/owner-team Tempo binding and current traces.view. Returns only scoped spans, partial=true and missing complete-trace, critical-path and correlation sections. The href from the list preserves an encoded service filter. A trace exceeding the bounded span limit returns 413 trace_too_large; distinct authorized bindings return 409 ambiguous_binding and excessive authorized scope returns 422 scope_limit.')
P['/traces/{id}']['get']['responses']['413'] = {
    'description':'Trace exceeds the scoped span limit',
    'content':{'application/json':{'schema':ref('Error')}}}

# Role matrix and membership are read from V3 grants. The writable role
# endpoint applies only to explicitly scoped grants and audits the change.
S['V3Role'] = obj({**fieldnames('id name kind'),'members':integer()},['id','name','members','kind'])
S['V3RoleDetail'] = obj({**S['V3Role']['properties'],**fieldnames('desc notes'),
    'member_names':arr(string()),'scope':arr(string()),'actions':arr(string()),
    'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))})
S['V3Member'] = obj({**fieldnames('id name email department phone'),
    'last_login':{'type':['string','null'],'description':'Null: no verified successful-login timestamp source is connected.'},
    'roles':arr(string()),'mfa':string(enum=['passkey','totp','none','na']),
    'source':string(enum=['feishu','ldap','system','invite']),
    'status':string(enum=['active','disabled']),'agent':v3_bool()})
S['V3MembersView'] = obj({'org':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'groups':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'items':arr(ref('V3Member')),
    'counts':obj({'all':integer(),'admin':integer(),
        'no_mfa':{'type':['integer','null'],'description':'Null: MFA enrollment is not verified.'},
        'disabled':integer()},['all','admin','no_mfa','disabled']),
    'mfa_grace_days':{'type':['integer','null'],'description':'Null: no verified MFA policy source is connected.'},
    'complete_v3_dto':v3_bool(),'unavailable_fields':arr(string())},
    ['org','groups','items','counts','mfa_grace_days','complete_v3_dto','unavailable_fields'])
v3_route('get','/roles','List scoped roles',response=listed('V3Role'),
    desc='Members and role grants are limited to the selected authorized environments. Only a reader with global roles.view and global environment.view can inventory unassigned historical roles and accounts.')
v3_route('get','/roles/{id}','Read role matrix and associations',response=ref('V3RoleDetail'),
    desc='Visible grants and member names are limited to the selected authorized environments. A role whose grants cannot be represented by the flat five-column matrix returns 409 matrix_editor_required. Other platform grants are described in notes, not projected into matrix cells.')
v3_route('put','/roles/{id}','Update role matrix',
    body=obj({'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))},['matrix']),
    response=obj({'ok':v3_bool()},['ok']),
    desc='Requires an active account with global roles.manage and global environment.view, rechecked inside the write transaction. Success returns only {ok:true}; a manager is not automatically authorized to read role detail after the write. Unrepresentable or scoped grants return 409 rather than being silently overwritten. Rejects removal of the last eligible global human role manager.')
v3_route('get','/members','List scoped members',response=ref('V3MembersView'),
    query=[v3_query('filter'),v3_query('q')],
    desc='Rechecks the current account and selected-environment grants in a read transaction, then derives the visible members and roles from one database statement. This is a partial DTO (complete_v3_dto=false): groups, full organization hierarchy, MFA enrollment, successful last login, MFA grace policy and directory synchronization source are not verified. Member mfa=na means unknown, not unenrolled; last_login, counts.no_mfa and mfa_grace_days are null. filter=no_mfa returns 503 member_mfa_unavailable rather than a misleading list.')

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
