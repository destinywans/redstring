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
    desc='Requires scoped knowledge:manage grant. A nonblank source requires a validated reference; blank forbids one. Template IDs are registered server-side; imports require a nonempty Markdown upload owned by the current user in the selected environment. Conversations are checked in the same transaction. A published postmortem in the selected environment has a creation-source path with current source access checks, while the separate postmortem draft generator still returns 501. The frozen creation flow and transaction authorization/source-state checks remain incomplete, so this main operation stays mock. No source reference is trusted solely because the client provided an ID.')
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
    desc='Keyword and vector candidates use the same current explicit environment, active application mapping and V3 environment/knowledge grants. A document carrying team_id must match the application current owner team and have an additional team knowledge grant. Candidate document ID/version and current grants are rechecked before output; malformed or unknown scope and legacy source_registration_id documents are excluded pending trusted provenance. A separately stored, credential-free, single-page public HTTPS Markdown body can supply bounded keyword passages only when its latest read observation, content hash, revision, application/environment/team binding and current grants all match at candidate and final output checks. One 30-second budget covers the whole search, and more than 80 SQL-matched public Web sources across all visible application/environment scopes returns 413 public_web_source_scope_too_large. No public Web vector indexing, Agent tool, source page DTO or full source index is proven. When embeddings are unavailable, returns a keyword result with degraded=true; an unavailable keyword index returns 503.')
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
    **{key:{'type':'null'} for key in ['connection_status','sync_status','document_count',
        'chunk_count','last_sync_at','index_status','error']}},
    ['id','name','kind','environment_id','schedule','chunk_strategy','registration_status',
     'last_read','keyword_content','connection_status','sync_status','document_count',
     'chunk_count','last_sync_at','index_status','error'])
v3_route('get','/knowledge-sources/registered-evidence','Read registered source and observed content evidence',
    response=obj({'items':{'type':'array','items':ref('V3RegisteredSourceEvidence'),'maxItems':200},
        'index':{'type':'null'},'complete_v3_dto':{'const':False},
        'partial':{'const':True},'reason':string(enum=['registration_and_observation_evidence_only']),
        'truncated':v3_bool(),'unknown_fields':arr(string())},
        ['items','index','complete_v3_dto','partial','reason','truncated','unknown_fields']),
    desc='Auxiliary current-scope projection, not the main KnowledgeSource DTO. The configured registration is unverified; last_read is only a completed observation, and keyword_content proves one public Web Markdown body matched the latest read, hash and revision. Connection, sync, total document/chunk counts, index status and error remain null; no body or credentials are returned. Up to 200 visible sources are emitted, with truncated=true if more are visible. More than 10000 candidate rows returns 413. Selected records are rechecked against current grants, active account/application/team binding and body version in a final fresh SQL statement after the initial read; a changed selection returns 409 without source metadata. Revocation committed after that final statement snapshot begins may still precede response delivery.')
for status, description in [('409','Selected source state, authorization or content changed; retry'),
                            ('413','Candidate scope exceeds 10000 registrations'),
                            ('503','Source registry or authorization is unavailable')]:
    P['/knowledge-sources/registered-evidence']['get']['responses'][status] = {
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
    desc='Auxiliary operation. A single public HTTPS Markdown Web page may omit secret_ref, use no spaces, manual schedule and inherited source access; other source kinds still require a Vault reference, never a plaintext credential. Registration does not prove connection or begin ingestion. Requires one environment and knowledge.manage; audit is transactional.')
v3_route('post','/knowledge-sources/registrations/{id}/sync','Observe one registered source through a configured read-only reader',
    response=obj({'read_id':string(),'observed_count':integer(minimum=0),
        'index_state':string(enum=['not_proven']),'complete_v3_dto':v3_bool()},
        ['read_id','observed_count','index_state','complete_v3_dto']),
    desc='Auxiliary lineage operation requiring one environment and knowledge.manage. Explicit AIOPS_V3_PUBLIC_WEB_READER_ENABLED=true configures only the application-bound, credential-free, single-page public HTTPS Markdown reader. An environment-only public Web registration returns 422 source_application_required before network access; unset/false or another unconfigured source type returns 501 source_reader_unavailable; a credentialed Web source is not supported by this reader. Application/team liveness is checked before network access and again under transaction before persistence. A successful same-fetch observation imports at most 128 KiB of UTF-8 body into separate V3 public Web document/revision tables with matching hash and read identity; version advances only when content changes. The response does not claim indexing or complete V3 source DTO.')
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
