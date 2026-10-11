# Governed runbooks and scoped knowledge search.
S['V3RunbookToolDisplay'] = obj(fieldnames('brand text'),['brand','text'])
S['V3RunbookToolDisplay']['description'] = 'Optional editor display text only; it does not bind or execute a connector.'
S['V3RunbookStep'] = obj({'n':integer(),'kind':string(enum=['query','check','action','verify']),
    'title':string(),'body':string(),'connectors':arr(string()),'code':string(),
    'readonly':v3_bool(),'approval':v3_bool(),'tool':ref('V3RunbookToolDisplay')})
S['V3RunbookCreateStep'] = obj({'n':integer(),'kind':string(enum=['query','check','action','verify','act','judge']),
    'title':string(),'body':string(),'connectors':arr(string()),'code':string(),
    'readonly':v3_bool(),'approval':v3_bool(),'tool':ref('V3RunbookToolDisplay')})
S['V3RunbookSummary'] = obj({**fieldnames('id title scope owner version application_id team_id'),
    'tags':arr(string()),'status':string(enum=['published','review','draft']),
    'environment_ids':arr(string()),
    'refs_30d':{'type':['integer','null']},'resolve_rate':{'type':['number','null']},
    'last_verified':{'type':['string','null']},'stale':{'type':['boolean','null']}},
    ['id','title','tags','scope','owner','version','status','refs_30d','resolve_rate',
     'last_verified','stale','environment_ids','application_id','team_id'])
S['V3Runbook'] = obj({**fieldnames('id title version base owner scope when status source_kind source_ref application_id team_id'),
    'tags':arr(string()),'steps':arr(ref('V3RunbookStep')),
    'environment_ids':arr(string()),'last_verified':{'type':['string','null']},
    'reviewers':arr(obj({'name':string(),'state':string(enum=['approved'])},['name','state'])),
    'versions':arr(v3_object('v who when')),
    'usage':obj({'refs':{'type':['integer','null']},'resolve_rate':{'type':['number','null']},
                 'weekly':{'oneOf':[arr(integer()),{'type':'null'}]},
                 'highlight':{'type':['integer','null']}},
                ['refs','resolve_rate','weekly','highlight']),
    'incidents':{'oneOf':[arr(v3_object('id title')),{'type':'null'}]},
    'complete_v3_dto':{'const':False},'partial':{'const':True},'unknown_fields':arr(string())},
    ['id','title','version','base','owner','scope','tags','when','steps','reviewers','versions','usage',
     'incidents','status','source_kind','source_ref','environment_ids','application_id','team_id',
     'last_verified','complete_v3_dto','partial','unknown_fields'])
v3_route('get','/runbooks','List accessible runbooks',response=obj({'items':arr(ref('V3RunbookSummary')),
    'counts':v3_map(integer()),'kpis':{'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]},
    'suggestions':{'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]},
    'page':integer(minimum=1),'per_page':integer(minimum=1,maximum=100),
    'total':integer(minimum=0),'has_next':v3_bool(),
    'complete_v3_dto':{'const':False},'partial':{'const':True},'unknown_fields':arr(string())},
    ['items','counts','kpis','suggestions','page','per_page','total','has_next',
     'complete_v3_dto','partial','unknown_fields']),
    query=[v3_query(x) for x in ['q','service','tab','page','per_page']],
    desc='Scoped persisted Runbook inventory. A repeatable-read snapshot locks the active session and current role grant rows, then checks each document, environment/application/team binding and object grant before counting, filtering and paging. Counts reflect visible q/service matches before tab paging; at most 10000 visible matches are accepted. Without a recorded verification time, stale is null and counts.verification_unknown increases; the stale tab includes only records with an old recorded time. Reference, outcome, KPI and suggestion producers are absent, so their values are null with explicit unknown_fields and complete_v3_dto=false. This partial main-route response does not meet the frozen frontend DTO yet.')
P['/runbooks']['get']['responses']['413'] = {
    'description':'More than 10000 visible Runbooks match the bounded collection',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/runbooks/{id}','Read governed runbook',response=ref('V3Runbook'),
    desc='Reads the persisted Runbook, current object authorization, structured steps, revisions and recorded approvals in one repeatable snapshot with session and role grant row locks. Hidden, out-of-scope or non-Runbook objects return 404; a missing or unrepresentable structured record returns 503. Usage, incident association and pending reviewer assignment have no complete producer and remain explicitly unknown. A stored last_verified_at is shown only as a recorded timestamp, not proof of execution. complete_v3_dto=false means this partial main-route response does not meet the frozen frontend DTO yet.')
for _path in ['/runbooks','/runbooks/{id}']:
    P[_path]['get']['responses']['503'] = {
        'description':'Runbook storage, structured steps, or authorization is unavailable',
        'content':{'application/json':{'schema':ref('Error')}}}
P['/runbooks/{id}']['get']['responses']['503']['description'] = (
    'Structured V3 steps are missing or cannot be represented, or storage or authorization is unavailable')
P['/runbooks/{id}']['get']['responses']['413'] = {
    'description':'Runbook revision or recorded approval history exceeds 100 entries',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/runbooks','Create governed runbook draft',
    body=obj({'title':string(),'source':string(enum=['blank','template','postmortem','conversation','import']),
        'from':string(),'expected_source_sha256':string(pattern='^[0-9a-f]{64}$'),
        'steps':arr(ref('V3RunbookCreateStep')),
        'status':string(enum=['draft','review'])},['title','source']),response=obj({'id':string()},['id']),status=201,
    desc='Requires one selected environment and current scoped knowledge:manage. A nonblank source requires an explicit authorized reference; blank forbids one. A current session, source state and permissions are rechecked inside the creation transaction. The bounded source identity and content digest saved with structured steps are an observation at save time. A client may pass expected_source_sha256 from GET /runbooks/draft to reject source drift with 409 source_changed; without it, submitted steps are not proven to derive from the saved digest. Initial version, revision history and audit are saved atomically; source text remains in its governed source table. An action step requires approval; review status requires at least one step. A draft is visible only to its maintainer with manage permission; review is visible to current knowledge managers. Neither status publishes or executes a Runbook. The frozen frontend creation payload still needs reconciliation with this request schema.')
v3_route('post','/runbooks/{id}/submit','Submit runbook for review',
    desc='Requires scoped knowledge:manage grant and maintainer ownership.',
    response=obj({'ok':v3_bool(),'status':string(enum=['review'])}))
v3_route('post','/runbooks/{id}/dry-run','Validate runbook without execution',
    role='viewer with scoped knowledge.manage',
    response=obj({'steps':integer(),'ok':v3_bool(),'mode':string(enum=['validation_only']),
        'checks':arr({'type':'object','additionalProperties':True})}),
    desc='Requires knowledge.manage because the validation preview and audit are persisted. Performs structural checks on persisted step kind, declared readonly flag and connector presence without a request body. It does not verify a connector capability or execute any external step.')
v3_route('get','/runbooks/draft','Read a source-validated Runbook outline',
    query=[v3_query('source',string(enum=['template','postmortem','conversation','import'])),v3_query('from')],
    response=obj({'title':string(),'applies_to':arr(string()),'trigger':string(),
        'steps':arr(ref('V3RunbookStep')),'sources':arr(string()),'outline':string(),
        'draft_mode':string(enum=['template_outline','source_outline']),'missing':arr(string()),
        'source_sha256':string(pattern='^[0-9a-f]{64}$')},
        ['title','applies_to','trigger','steps','sources','outline','draft_mode','missing','source_sha256']),
    desc='Requires one selected environment and a valid explicit source ID. Registered code templates return a truthful outline with empty steps until a person supplies operational details. Published postmortem, owned completed conversation and owned Markdown import return bounded, source-backed structured outlines only after current source authorization. source_sha256 identifies the exact source content consumed by this response and can bind a later create request. Source text is treated as untrusted data, not model instructions or verified remediation; explicit write actions require approval. The response is a deterministic source outline, not proof of AI generation or a validated recovery procedure. It does not create a Runbook.')
P['/runbooks/draft']['get']['description'] += (' A published postmortem of an ops incident must still be closed by its recorded'
    ' investigation.closed event; one of a manual incident (case) must still be resolved by its recorded resolved status'
    ' event (PA-63). For a postmortem the caller may read, 422 source_not_published, source_not_current or'
    ' source_kind_unsupported says why it cannot seed a Runbook; an unknown or unreadable source stays 422 invalid_source.'
    ' POST /runbooks returns the same codes.')
P['/runbooks/draft']['get']['responses']['413'] = {
    'description':'The selected source exceeds the bounded draft input limit',
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
S['V3PostmortemDashboardKPI'] = obj({'id':string(),'label':string(),'value':string(),
    'unit':string(),'series':{'oneOf':[arr(v3_num()),{'type':'null'}]}},['id','label','value','series'])
S['V3PostmortemDashboardItem'] = obj({**fieldnames('id title date duration cause root_cause actions incident_id'),
    'severity':{'oneOf':[string(enum=['P1','P2','P3','P4']),{'type':'null'}]},
    'duration':{'type':['string','null']},'root_cause':{'type':['string','null']},
    'status':string(enum=['draft','published']),
    'workflow_status':string(enum=['draft','review','published'])},
    ['id','title','severity','date','duration','cause','root_cause','actions','status','workflow_status','incident_id'])
v3_route('get','/postmortems','Read scoped persisted postmortem dashboard evidence',
    query=[v3_query('tab',string(enum=['all','draft','review','published'])),v3_query('q'),
           v3_query('page',integer(minimum=1,maximum=1000)),
           v3_query('page_size',integer(minimum=1,maximum=1000))],
    response=obj({'kpis':arr(ref('V3PostmortemDashboardKPI')),
        'items':arr(ref('V3PostmortemDashboardItem')),
        'causes':{'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]},
        'impact_by_service':{'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]},
        'followups':{'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]},
        'complete_v3_dto':{'const':False},'partial':{'const':True},'unknown_fields':arr(string()),
        'page':integer(minimum=1),'page_size':integer(minimum=1),'total':integer(minimum=0)},
        ['kpis','items','causes','impact_by_service','followups','complete_v3_dto','partial',
         'unknown_fields','page','page_size','total']),
    desc='Reads persisted reports and their exact legacy investigation close or manual case resolved status event in one repeatable snapshot with current session and role grant locks, then checks events:view and knowledge:view on each object; unpublished reports additionally require the author or knowledge:manage. Visible records supply title, real source event date, recorded workflow status and action total. A manual case has no proven outage duration, so duration stays null and dashboard duration coverage is unknown. Invalid severity or absent root text are nullable rather than blocking other valid reports. The unknown cause category is labeled Unknown, never inferred from prose. KPI values count visible reports and mean resolution time only for valid legacy durations; time series have no producer and remain null. Cause categories, action completion, impact minutes, MTTA and recurrence lack producers, so those collections are null and complete_v3_dto=false. This partial main-route response does not meet the frozen dashboard DTO. More than 1000 visible records returns 413.')
P['/postmortems']['get']['responses']['413'] = {
    'description':'More than 1000 visible reports exceed the bounded dashboard view',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3PostmortemCandidate'] = obj({**fieldnames('id title resolved source_revision'),
    'duration':{'type':['string','null']},
    'severity':string(enum=['P1','P2','P3','P4']),
    'evidence':{'type':['integer','null'],'minimum':0},
    'unknown_fields':arr(string())},
    ['id','title','severity','resolved','duration','source_revision'])
S['V3PostmortemSectionInput'] = obj(fieldnames('key title text'),['key','title','text'])
S['V3PostmortemActionInput'] = obj(fieldnames('text owner due'),['text','owner','due'])
S['V3PersistedPostmortemRecord'] = obj({**fieldnames('id incident_id title resolved created_at source_revision'),
    'duration':{'type':['string','null']},
    'severity':string(enum=['P1','P2','P3','P4']),
    'status':string(enum=['draft','review','published']),
    'action_count':integer(minimum=0),'unknown_fields':arr(string())},
    ['id','incident_id','title','severity','resolved','duration','status','created_at','action_count','source_revision'])
v3_route('get','/postmortems/candidates','List resolved incidents eligible for a postmortem',
    response=obj({'items':arr(ref('V3PostmortemCandidate'))},['items']),
    desc='Legacy ops incidents require a real investigation.closed event and retained report; human-confirmed cases require a real resolved status event and may have no Canvas. Both require no existing postmortem and current events:view plus knowledge:view on their registered application or owner team. source_revision binds the observed close event or case status event. Case outage duration and investigation evidence remain null/unknown unless independently proven. More than 1000 visible candidates returns 413; unknown severity returns 503 rather than an invented classification.')
P['/postmortems/candidates']['get']['responses']['413'] = {
    'description':'More than 1000 visible eligible incidents exceed the unpaged response limit',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/postmortems/records','Read scoped persisted postmortem records',
    query=[v3_query('tab',string(enum=['all','draft','review','published']))],
    response=obj({'items':arr(ref('V3PersistedPostmortemRecord'))},['items']),
    desc='Auxiliary read-only inventory, not the PostmortemSummary dashboard DTO. Requires current events:view and knowledge:view on each source application or owner team. Returns stored status, real source event time, source_revision and action count; unproven manual-case outage duration is null with unknown_fields. No invented cause categories, completion rate or KPI. More than 1000 visible records returns 413.')
P['/postmortems/records']['get']['responses']['413'] = {
    'description':'More than 1000 visible persisted records',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/postmortems','Create a governed incident postmortem',
    body=obj({'incident_id':string(),'source_revision':string(pattern='^(ops|case):[1-9][0-9]*$'),
        'source_close_event_seq':integer(minimum=1),
        'sections':arr(ref('V3PostmortemSectionInput')),
        'actions':arr(ref('V3PostmortemActionInput')),
        'status':string(enum=['draft','review'])},
        ['incident_id','sections','actions','status']),
    response=obj({'id':string()},['id']),status=201,
    desc='Requires a single selected environment and current events:view plus knowledge:manage on the source application or its persisted owner team. GET /postmortems/draft returns source_revision: ops:<close event seq> or case:<resolved status event id>. A case create must submit that exact source_revision; legacy ops create also accepts the existing source_close_event_seq for compatibility. Missing or invalid source identity returns 422; a changed lifecycle revision or closing source returns 409 and requires re-preview. The write locks session, grants, scope and current source, then stores sections, actions, exact source reference and audit together without executing or notifying. A separate human reviewer publishes.')
v3_route('get','/postmortems/draft','Read an evidence-only postmortem starting point',
    query=[v3_query('incident')],
    response=obj({'incident':obj({**fieldnames('id title severity'),
            'duration':{'type':['string','null']},
            'started_at':{'type':['string','null'],'format':'date-time','description':'PA-83: incident start in RFC 3339, null with closed_at when unknown'},
            'closed_at':{'type':['string','null'],'format':'date-time','description':'PA-83: incident close in RFC 3339, null with started_at when unknown'}},
            ['id','title','severity','duration','started_at','closed_at']),
        'sections':arr(ref('V3PostmortemSectionInput')),
        'timeline':arr(obj({**fieldnames('at text tone')},['at','text','tone'])),
        'actions':arr(ref('V3PostmortemActionInput')),
        'source_revision':string(pattern='^(ops|case):[1-9][0-9]*$'),
        'source_close_event_seq':integer(minimum=1),
        'draft_mode':string(enum=['evidence_only']),'complete_v3_dto':{'const':False},
        'partial':{'const':True},'missing':arr(string())},
        ['incident','sections','timeline','actions','source_revision','draft_mode',
         'complete_v3_dto','partial','missing']),
    desc='Partial evidence-only operation in one authorized repeatable snapshot. Legacy ops incidents require their investigation report and exact investigation.closed event; a human case requires its persisted resolved status event and may have no investigation. source_revision is the exact source token required for case create; ops also returns the existing source_close_event_seq. Source changes return 409 on save and require re-preview. NoCanvas cases have no fabricated report/timeline or outage duration; impact, root cause and recommendations remain blank or unknown. More than 200 real timeline rows returns 413.')
P['/postmortems/draft']['get']['responses']['409'] = {
    'description':'postmortem_exists: the incident already has a postmortem the caller may see; postmortem_id names it (PA-65)',
    'content':{'application/json':{'schema':obj({'error':obj({'code':string(enum=['postmortem_exists']),'message':string()},['code','message']),
        'postmortem_id':string()},['error','postmortem_id'])}}}
P['/postmortems/draft']['get']['responses']['413'] = {
    'description':'Incident evidence timeline exceeds 200 events',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3PostmortemDetail'] = obj({'id':string(),'incident_id':string(),
    'status':string(enum=['draft','review','published']),'revision':integer(minimum=1),
    'source_revision':string(pattern='^(ops|case):[1-9][0-9]*$'),
    'sections':arr(ref('V3PostmortemSectionInput')),'actions':arr(ref('V3PostmortemActionInput')),
    'authored_by_me':v3_bool(),'created_at':string(format='date-time'),'updated_at':string(format='date-time')},
    ['id','incident_id','status','revision','source_revision','sections','actions','authored_by_me','created_at','updated_at'])
v3_route('get','/postmortems/{id}','Read one saved postmortem with its content',
    response=ref('V3PostmortemDetail'),
    desc='Requires events:view and knowledge:view on the source application or its owner team; a draft or review is visible to its author and knowledge managers only, otherwise 404. Returns the revision as a quoted ETag for PATCH (PA-65).')
v3_route('patch','/postmortems/{id}','Edit a draft postmortem or submit it for review',
    body=obj({'sections':arr(ref('V3PostmortemSectionInput')),'actions':arr(ref('V3PostmortemActionInput')),
        'status':string(enum=['draft','review'])}),
    response=ref('V3PostmortemDetail'),
    desc='Requires one selected environment, current events:view plus knowledge:manage on the source, and If-Match with the ETag from GET (428 without it, 412 postmortem_revision_conflict when stale). Only a draft can change (409 invalid_state); omitted fields keep their saved value. status review submits it: every section must have text (422) and the incident must still be resolved by the recorded source event (409 postmortem_source_changed). The edit and an audit row (postmortem.updated or postmortem.submitted) commit together and the response carries the new ETag. Publication remains a second reviewer\'s POST /postmortems/{id}/publish (PA-65).')
P['/postmortems/{id}']['patch'].setdefault('parameters', []).append(
    {'name':'If-Match','in':'header','required':True,'schema':string(description='Quoted revision ETag from GET /postmortems/{id}')})
v3_route('post','/postmortems/{id}/publish','Publish a reviewed postmortem',
    response=obj({'id':string(),'status':string(enum=['published'])},['id','status']),
    desc='Auxiliary human review operation requiring scoped knowledge.manage. The author cannot publish their own report. The saved exact legacy investigation close source or human case resolved status event and current source revision are rechecked under authorization before publication; drift returns 409 and requires refresh. This records review and audit locally without external publication.')
S['V3KnowledgeSearchHit'] = obj({**fieldnames('title snippet'),
    'keyword':v3_num(),'vector':v3_num(),'score':v3_num()})
v3_route('post','/knowledge/search','Search scoped knowledge with keyword/vector ranking',
    body=obj({'q':string(),'service':string(),'top_k':integer(minimum=1,maximum=20)},['q']),
    response=obj({'hits':arr(ref('V3KnowledgeSearchHit')),
        'mode':string(enum=['hybrid','keyword','none']),'degraded':v3_bool(),
        'corpora':arr(obj({'corpus':string(enum=['documents','public_web_sources','upload_sources','external_sources','git_markdown_sources']),
            'keyword':string(enum=['searched','unavailable','not_indexed','not_searched']),
            'vector':string(enum=['searched','unavailable','not_indexed','not_searched']),
            'reason':string()},['corpus','keyword','vector']))},['hits','mode','degraded','corpora']),
    desc='Keyword and vector candidates use the same current explicit environment, active application mapping and V3 environment/knowledge grants. A document carrying team_id must match the application current owner team and have an additional team knowledge grant. Candidate document ID/version and current grants are rechecked before output; malformed or unknown scope and legacy source_registration_id documents are excluded pending trusted provenance. A separately stored, credential-free, single-page public HTTPS Markdown body can supply bounded keyword passages only when its latest read observation, content hash, revision, application/environment/team binding and current grants all match at candidate and final output checks. One 30-second budget covers the whole search, and more than 80 SQL-matched public Web sources across all visible application/environment scopes returns 413 public_web_source_scope_too_large. No public Web vector indexing, Agent tool, source page DTO or full source index is proven. When embeddings are unavailable, returns a keyword result with degraded=true; an unavailable keyword index returns 503.')
P['/knowledge/search']['post']['description'] += (' Synced upload sources are keyword-searchable from their latest read under'
    ' the same scope and grants as their evidence proof; they have no vector index (PA-66). Feishu and other bound external'
    ' readers and Git Markdown sources are not searched yet. corpora lists, per corpus, whether keyword and vector search'
    ' ran (searched), failed (unavailable), have no index (not_indexed) or are not read by this search (not_searched).')
P['/knowledge/search']['post']['responses']['413'] = {
    'description':'Authorized candidate scope is too large or more than 80 public Web sources match across all visible scopes; narrow the search to one service',
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
S['V3RegisteredSourceRead'] = obj({'id':string(), 'observed_at':string(format='date-time'),
    'observed_count':integer(minimum=0)}, ['id','observed_at','observed_count'])
S['V3RegisteredSourceKeywordContent'] = obj({'persisted_document_count':integer(minimum=1),
    'version':integer(minimum=1), 'read_id':string()},
    ['persisted_document_count','version','read_id'])
S['V3RegisteredSourceEvidence'] = obj({**fieldnames('id name kind environment_id application_id team_id schedule chunk_strategy'),
    'registration_status':string(enum=['unverified']),
    'last_read':{'oneOf':[ref('V3RegisteredSourceRead'),{'type':'null'}]},
    'keyword_content':{'oneOf':[ref('V3RegisteredSourceKeywordContent'),{'type':'null'}]},
    'document_count':{'type':['integer','null'],'minimum':0},
    'last_sync_at':{'type':['string','null'],'format':'date-time'},
    **{key:{'type':'null'} for key in ['connection_status','sync_status',
        'chunk_count','index_status','error']}},
    ['id','name','kind','environment_id','schedule','chunk_strategy','registration_status',
     'last_read','keyword_content','connection_status','sync_status','document_count',
     'chunk_count','last_sync_at','index_status','error'])
v3_route('get','/knowledge-sources/registered-evidence','Read registered source and observed content evidence',
    response=obj({'items':{'type':'array','items':ref('V3RegisteredSourceEvidence'),'maxItems':200},
        'index':{'type':'null'},'complete_v3_dto':{'const':False},
        'partial':{'const':True},'reason':string(enum=['scoped_source_evidence_partial']),
        'truncated':v3_bool(),'unknown_fields':arr(string()),
        'unconfigured_external_bindings':arr(string(enum=['git_markdown','confluence','notion','feishu']))},
        ['items','index','complete_v3_dto','partial','reason','truncated','unknown_fields',
         'unconfigured_external_bindings']),
    desc='Auxiliary current-scope projection, not the full KnowledgeSource DTO. The configured registration is unverified; last_read is a completed observation. A public Web keyword_content requires a matching latest body/hash/revision. Git Markdown, Confluence, Notion, Feishu and local-upload document_count and last_sync_at require matching latest private documents, hashes, source versions, scope and observations. Local-upload proof additionally requires the original owned file to remain present, unchanged and not revoked. Zero is reported only for an actual empty read. No connected vector index, connection or sync health is claimed. No body or credentials are returned. Current grants and source evidence are rechecked in a fresh final statement; changed state returns 409.')
v3_route('get','/knowledge-sources','Read registered knowledge sources and verified observation evidence',
    response=obj({'items':{'type':'array','items':ref('V3RegisteredSourceEvidence'),'maxItems':200},
        'index':{'type':'null'},'complete_v3_dto':{'const':True},
        'partial':{'const':True},'reason':string(enum=['scoped_source_evidence_partial']),
        'truncated':v3_bool(),'unknown_fields':arr(string()),
        'unconfigured_external_bindings':arr(string(enum=['git_markdown','confluence','notion','feishu']))},
        ['items','index','complete_v3_dto','partial','reason','truncated','unknown_fields',
         'unconfigured_external_bindings']),
    desc='Complete nullable evidence DTO for the real source page; partial=true describes evidence coverage, not missing response fields. It shares the current-scope and fresh final recheck of /knowledge-sources/registered-evidence. Git Markdown, Confluence, Notion and Feishu require an exact current operator binding to appear; uploaded files require their owner-bound source. Completed public Web, Git Markdown, Confluence, Notion, Feishu and local-upload reads can supply observed_at/count. Document count and last sync time require matching private document, version, scope and observation proof; local upload also requires the original owned file to remain present, unchanged and not revoked. Unconfigured external binding types are named at process level; this does not reveal hidden registrations or prove connectivity for any configured type. Connection health, scheduled sync, vector chunks and index settings remain unknown. No synthetic catalog cards are emitted; null means unknown or unindexed, never zero or connected.')
for status, description in [('409','Selected source state, authorization or content changed; retry'),
                            ('413','Candidate scope exceeds 10000 registrations'),
                            ('503','Source registry or authorization is unavailable')]:
    P['/knowledge-sources/registered-evidence']['get']['responses'][status] = {
        'description':description,'content':{'application/json':{'schema':ref('Error')}}}
    P['/knowledge-sources']['get']['responses'][status] = {
        'description':description,'content':{'application/json':{'schema':ref('Error')}}}
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
    desc='Auxiliary operation. A single public HTTPS Markdown Web page may omit secret_ref. Git Markdown, Confluence, Notion and Feishu require an exact server-owned source and Vault binding. A local upload requires an exact owned same-environment text-file URL and empty secret_ref. All bound sources require one active application and optional owner team, manual schedule and inherited source access. Confluence requires its configured numeric space ID; Notion, Feishu and upload require empty spaces. Registration does not prove connection or begin ingestion. Requires one environment and knowledge.manage; audit is transactional.')
S['V3PublicWebSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['web']),'url':string(maxLength=1000),
    'secret_ref':{'const':''},'application_id':string(), 'team_id':string(),
    'spaces':{'type':'array','items':string(),'maxItems':0},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','application_id','schedule','chunk','inherit_acl'])
S['V3GitMarkdownSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['git_markdown']),'url':string(maxLength=2000),
    'secret_ref':string(),'application_id':string(),'team_id':string(),
    'spaces':{'type':'array','items':string(),'maxItems':0},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','secret_ref','application_id','schedule','chunk','inherit_acl'])
S['V3ConfluenceSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['confluence']),'url':string(maxLength=2000),
    'secret_ref':string(),'application_id':string(),'team_id':string(),
    'spaces':{'type':'array','items':string(pattern='^[1-9][0-9]*$',maxLength=19),'minItems':1,'maxItems':1},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','secret_ref','application_id','spaces','schedule','chunk','inherit_acl'])
S['V3NotionSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['notion']),'url':string(maxLength=2000),
    'secret_ref':string(),'application_id':string(),'team_id':string(),
    'spaces':{'type':'array','items':string(),'maxItems':0},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','secret_ref','application_id','schedule','chunk','inherit_acl'])
S['V3FeishuSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['feishu']),'url':string(maxLength=2000),
    'secret_ref':string(),'application_id':string(),'team_id':string(),
    'spaces':{'type':'array','items':string(),'maxItems':0},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','secret_ref','application_id','schedule','chunk','inherit_acl'])
S['V3LocalUploadSourceCreateInput'] = obj({'name':string(maxLength=160),
    'type':string(enum=['upload']),'url':string(pattern='^/api/v1/uploads/[a-f0-9]{32}/content$'),
    'secret_ref':{'const':''},'application_id':string(),'team_id':string(),
    'spaces':{'type':'array','items':string(),'maxItems':0},
    'schedule':string(enum=['manual']),'chunk':string(enum=['heading','fixed']),
    'inherit_acl':{'const':True}},
    ['name','type','url','application_id','schedule','chunk','inherit_acl'])
v3_route('post','/knowledge-sources','Register one configured application-bound knowledge source',
    body={'oneOf':[ref('V3PublicWebSourceCreateInput'),ref('V3GitMarkdownSourceCreateInput'),
        ref('V3ConfluenceSourceCreateInput'),ref('V3NotionSourceCreateInput'),
        ref('V3FeishuSourceCreateInput'),ref('V3LocalUploadSourceCreateInput')]},status=201,
    response=obj({'id':string(),'registration_status':string(enum=['unverified']),
        'sync_started':{'const':False}},['id','registration_status','sync_started']),
    desc='Requires one selected environment, explicit active application_id and current knowledge.manage for the application and optional matching owner team. Public Web accepts one credential-free HTTPS Markdown URL; git_markdown, Confluence, Notion and Feishu require exact server-configured source, scope and vault://mount/path#field binding. Feishu binds one Docx ID. Local upload requires one owned same-environment text-file content URL and no Vault reference. Confluence binds one numeric space ID; Notion binds one page UUID. All families require manual schedule, heading/fixed setting and inherited ACL. Registration and audit commit together after current session/scope/grant checks; no read, sync or index starts. Unconfigured types/bindings return 501, malformed requests 422.')
for status, description in [('409','Select exactly one environment'),
                            ('422','Invalid source URL, options or application/team scope'),
                            ('501','No reader/importer for this source type or credentialed Web'),
                            ('503','Source registry, authorization or audit unavailable')]:
    P['/knowledge-sources']['post']['responses'][status] = {
        'description':description,'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/knowledge-sources/registrations/{id}/sync','Observe one registered source through a configured read-only reader',
    response=obj({'read_id':string(),'observed_count':integer(minimum=0),
        'index_state':string(enum=['not_proven']),'complete_v3_dto':v3_bool(),
        'keyword_content_persisted':v3_bool(),'content_version':integer(minimum=1),
        'commit_sha':string(pattern='^([a-f0-9]{40}|[a-f0-9]{64})$'),'document_count':integer(minimum=0,maximum=32)},
        ['read_id','observed_count','index_state','complete_v3_dto']),
    desc='Auxiliary manual lineage operation requiring one environment and knowledge.manage for the saved application/team scope. Explicit AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true configures the single-page public Web reader. Server-configured Git Markdown, Confluence, Notion and Feishu readers require exact Vault bindings; Git pins one commit, Confluence reads one space, Notion one page UUID, and Feishu one Docx. Local upload reads one original owned text file without Vault. Application/team liveness and saved binding identity are checked before access and again under transaction before persistence; local upload also rechecks owner and file version under lock. A complete read, observations, private bodies, source versions, binding snapshot and audit commit together; incomplete or failed reads do not persist. External responses include document_count and keyword_content_persisted; Git additionally includes commit_sha; public Web includes content_version. No vector chunk or index is claimed; absent reader returns 501. Legacy vault: references are rejected for new bound source registration.')
P['/knowledge-sources/registrations/{id}/sync']['post']['responses']['501'] = {
    'description':'No read-only source reader is configured',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/knowledge-sources/registrations/{id}/evidence','Read observed source lineage and attributed index evidence',
    response=obj({'id':string(),'last_read':{'type':['object','null']},
        'index':{'type':['object','null']},'keyword':{'type':['object','null']},
        'git_markdown':{'type':['object','null']},'external_source':{'type':['object','null']},
        'reason':string(),'complete_v3_dto':v3_bool()},
        ['id','last_read','index','complete_v3_dto']),
    desc='Auxiliary read of current authorized registration. An absent read returns null evidence. Git Markdown includes commit_sha and document_count only when the latest read has matching private documents, hashes, scope and observations. Before returning Git evidence, the current session, access, exact server binding and latest persisted proof are rechecked; changed state returns 409. Confluence, Notion, Feishu and upload analogously use external_source with source_type, document_count and content_persisted only after proof. Upload evidence also requires the original owned file to remain present and unchanged. Public Web keyword evidence and generic attributed ready documents/chunks never imply a complete source index. No source content is returned.')
P['/knowledge-sources/registrations/{id}/evidence']['get']['responses']['409'] = {
    'description':'Git Markdown or external source state, current binding, access or evidence changed; retry',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/knowledge-sources/test','Test a knowledge source connection',
    body=obj({**fieldnames('type url secret_ref application_id team_id'),
        'spaces':{'type':'array','items':string(),'maxItems':1}},['type','url']),
    response=obj({'ok':v3_bool(),'message':string(),'connected':v3_bool(),
        'observed_at':string(format='date-time'),'persistent':v3_bool(),
        'scope_verified':v3_bool(),'spaces':arr(string())},
        ['ok','message','connected','observed_at','persistent','scope_verified']),
    desc='With AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true, a credential-free public HTTPS Markdown Web URL receives one bounded read-only fetch after validation and environment-scoped knowledge.manage. Configured Git Markdown, Confluence, Notion and Feishu probes require application_id and optional matching team_id; Confluence also requires its single numeric spaces ID. Local upload requires application_id, owner/same-environment file access, empty secret_ref and empty spaces. Current app/team knowledge.manage and active scope are checked before and after the read. A committed attempt audit precedes Vault/upstream or local-file access; a bounded outcome audit follows. Success proves only point-in-time readability. Nothing is registered, synchronized or indexed. Source kinds without a configured prober return 501 source_connection_test_unavailable; malformed URLs or missing required Vault references return 422. A configured failed read returns 503 source_connection_failed. A credentialed Web URL has no supported prober. Successful probe does not authorize a subsequent create or prove future availability.')
P['/knowledge-sources/test']['post']['responses']['501'] = {
    'description':'No source-specific read-only connection prober is configured',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/knowledge-sources/test']['post']['responses']['503'] = {
    'description':'Configured read-only source probe, authorization or storage unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/uploads/{id}/revoke','Revoke one owned uploaded file',
    role='viewer with scoped conversation.change',
    response=obj({'id':string(pattern='^[a-f0-9]{32}$'),'revoked':{'const':True}},
        ['id','revoked']),
    desc='Requires one explicit selected environment, the current conversation.change grant and ownership of the original file. Session and grant are rechecked in the deletion transaction. The original bytes and linked private knowledge-source body copies are deleted, with a content-free tombstone and audit committed atomically. Existing source registrations and historic read lineage remain, but revoked content no longer satisfies current document evidence. A missing or already revoked file returns 404.')
for status, description in [('403','Current conversation change permission denied'),
                            ('404','Owned file is missing or already revoked'),
                            ('422','One explicit environment is required'),
                            ('503','Upload storage or authorization is unavailable')]:
    P['/uploads/{id}/revoke']['post']['responses'][status] = {
        'description':description,'content':{'application/json':{'schema':ref('Error')}}}
