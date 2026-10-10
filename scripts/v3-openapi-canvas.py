# General canvases are persisted separately from investigation branches. The
# list projects both from their durable sources; unrecorded historical creators
# yield an empty participant list.
S['V3CanvasSummary'] = obj({**fieldnames('id title meta href'),
    'kind':string(enum=['investigation','plan','review','analysis','custom']),
    'status':string(enum=['draft','active','waiting_approval','done','resolved']),
    'participants':arr(string()),
    'updated_at':string(format='date-time'),
    'investigation_id':string(),'services':arr(string()),
    'thumb':obj({'nodes':arr({'type':'array','prefixItems':[integer(),integer(),integer(),integer(),string()],
        'minItems':5,'maxItems':5}), 'edges':arr(string())},['nodes','edges'])},
    ['id','title','kind','status','meta','participants','updated_at','href','services','thumb'])
S['V3CanvasTemplateRetention'] = obj({
    'steps':integer(minimum=0),'runbooks':integer(minimum=0),
    'content_blocks':integer(minimum=0),
    'runbook_source':string(enum=['linked_artifacts','no_structured_reference'])},
    ['steps','runbooks','content_blocks'])
S['V3CanvasTemplate'] = obj({**fieldnames('id kind name desc nodes'),
    'is_default':v3_bool(),'retention':ref('V3CanvasTemplateRetention')},
    ['id','kind','name','desc','nodes'])
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
S['V3CanvasDocWithoutThreads'] = obj({
    **{name: schema for name, schema in S['V3CanvasDoc']['properties'].items()
       if name != 'threads'},
    'threads_included': {'type':'boolean','const':False}},
    [name for name in S['V3CanvasDoc']['required'] if name != 'threads'] +
    ['threads_included'])
S['V3CanvasHistoryRecoveryLink'] = obj({
    'node_id':string(),'href':string()},['node_id','href'])
S['V3CanvasHistoryRecovery'] = obj({
    'canvas_href':string(),
    'node_messages':arr(ref('V3CanvasHistoryRecoveryLink'))},
    ['canvas_href','node_messages'])
S['V3CanvasHistoryTooLarge'] = obj({
    'error':obj({'code':string(enum=['canvas_history_too_large']),
                 'message':string()},['code','message']),
    'recovery':ref('V3CanvasHistoryRecovery')},['error','recovery'])
S['V3CanvasExchangeState'] = obj({
    'id':string(), 'state':string(enum=['running','completed','failed'])},
    ['id','state'])
S['V3CanvasNodeMessagesPage'] = obj({
    'canvas_id':string(), 'node_id':string(),
    'thread':ref('V3CanvasThread'),
    'exchange_states':arr(ref('V3CanvasExchangeState')),
    'has_more':v3_bool(),
    'next_cursor':{'type':['string','null']},
    'snapshot_through_sort_order':integer(minimum=0,format='int64'),
    'after_sort_order':{'type':['integer','null'],'minimum':0,'format':'int64'},
    'returned_through_sort_order':{'type':['integer','null'],'minimum':0,'format':'int64'}},
    ['canvas_id','node_id','thread','exchange_states','has_more','next_cursor',
     'snapshot_through_sort_order','after_sort_order','returned_through_sort_order'])
v3_route('get','/canvases','List scoped persisted canvases and investigations',
    response=obj({'items':arr(ref('V3CanvasSummary')),'counts':v3_map(integer()),
      'total':integer(),'page':integer(),'page_size':integer()}),
    query=[v3_query(x) for x in ['kind','q','page','page_size']],
    desc='Generic canvases are private to their owner; investigations use persisted environment and application/team grants. Current sessions and role sources are checked before a single database snapshot supplies counts and page items. Historical investigations without a recorded creator return an empty participants array. Persisted investigation graph detail is available at /investigations/{id}/canvas; this list does not include graph nodes.')
v3_route('post','/canvases','Create a persisted canvas or investigation',
    body=obj({**fieldnames('title kind template_id goal'),
      'services':arr(string()),'ai_draft':v3_bool()},['title','kind']),
    response=obj(fieldnames('id href'),['id','href']),status=201,
    desc='Requires one selected environment and a current canvas.change grant. Generic canvases are private and require an environment grant; investigations require exactly one active mapped application and accept environment, application or owner-team grants. A private generic template can be reused only by its owner in the same environment; retained read-only steps are reset to unexecuted review state, and exact linked Runbook references require the saved document version, current publication, service scope and knowledge.view again. A new generic canvas starts as draft, without copied exchanges or execution results. Investigation IDs are persisted INV-* IDs with an initial branch and context. Canvas/branch records and creation audit commit together. Optional AI drafting uses one configured model, no tools, and a validated one-to-three-node JSON outline; it is explicitly unverified and a failure leaves no creation. Missing model returns 503 canvas_draft_unavailable. Investigation templates return 422 invalid_template. Persisted investigation graph detail is available at /investigations/{id}/canvas.')
v3_route('get','/canvases/{id}','Read owner-scoped general canvas',
    response={'oneOf':[ref('V3CanvasDoc'),ref('V3CanvasDocWithoutThreads')]},
    query=[v3_query('include_threads',{'type':'boolean'})],
    desc='Default response is the unchanged full V3CanvasDoc with persisted per-node exchanges in threads. Explicit include_threads=false returns the same canvas structure without a threads property and with threads_included=false; this is an omission marker, not empty history. The current owner, an allowed environment, and current canvas.view are required. In the default response, more than 500 exchanges, 2 MiB of thread history, or one assistant answer over 64 KiB returns 413 canvas_history_too_large with concrete recovery links for the omitted canvas and each node message page. Failed user prompts remain visible without a fabricated assistant reply. Invalid persisted canvas structure returns 503 canvas_unavailable. Investigation canvas detail is separate.')
P['/canvases/{id}']['get']['responses']['413'] = {
    'description':'Default full history exceeds a bounded canvas detail limit; recovery links provide an omitted canvas and per-node pages',
    'content':{'application/json':{'schema':ref('V3CanvasHistoryTooLarge')}}}
P['/canvases/{id}']['get']['responses']['503'] = {
    'description':'Persisted canvas structure, conversation history, or authorization source is unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/canvases/{id}/nodes/{node}/messages',
    'Read a bounded persisted general-canvas node history page',
    response=ref('V3CanvasNodeMessagesPage'),
    query=[v3_query('page_size',integer(minimum=1,maximum=50,default=20)),
           v3_query('cursor',string(minLength=1))],
    desc='Auxiliary read-only route outside the frozen 165 V3 page contracts. Requires the current owner, allowed canvas environment, and current canvas.view on every page. Reads at most page_size (1–50, default 20) persisted exchanges in stable sort_order within 2 MiB raw history. A completed exchange contributes user and assistant turns; running and failed exchanges contribute only the saved user turn, with state in exchange_states. The opaque cursor binds canvas, node, environment, and the first-page snapshot upper sort_order; later pages reauthorize and never silently truncate a visible exchange. The range fields describe the bounded snapshot without claiming a total count. Invalid query/cursor returns 422. One exchange over the 2 MiB page budget returns 413; investigation node history uses its separate route.')
P['/canvases/{id}/nodes/{node}/messages']['get']['responses']['413'] = {
    'description':'A single persisted exchange exceeds the 2 MiB page budget',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/canvases/{id}/nodes/{node}/messages','Run a scoped general-canvas node reply',
    body=obj({'text':string(),'model':string(),'reasoning':string()},['text']),
    response=obj({'reply':string()},['reply']),
    desc='Owner-only general canvas node, one selected environment and current canvas.change grant. The synchronous response follows a durable assistant turn; user prompt, model choice, status and audit are persisted. One node reply runs at a time and returns 409 reply_in_progress for a concurrent run. The configured model receives the current node blocks, blocks from all transitive ancestors, and prior completed node and ancestor turn pairs within a 24 KiB context budget; sibling, descendant, and failed turns are excluded. Oversize context returns 413 node_context_too_large. Tool invocations are rejected, and model failure returns 503 with the failed user prompt retained. Access is checked about every two seconds during the model call and again in the completion transaction against the initiating session, canvas ownership and current canvas.change grant; a reply cannot commit when that final check denies access. A brief revoke and restore entirely between checks may be missed. Investigation nodes use their separate investigation route.')
P['/canvases/{id}/nodes/{node}/messages']['post']['responses']['413'] = {
    'description':'Current and inherited node context exceeds the 24 KiB model budget',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3InvestigationCanvasContext'] = obj({
    'version':string(),'inherits':string(),
    'tokens':{'type':['integer','null'],'minimum':0,
        'description':'Current context token occupancy is unknown until measured for this exact context. Null means unknown; historical provider usage is not a current occupancy estimate. The frontend must render an unknown state instead of a numeric total.'},
    'messages':integer(minimum=0),'excludes':arr(string())},
    ['version','tokens','messages','excludes'])
S['V3InvestigationCanvasVersion'] = obj({
    'v':string(),'text':string(),'at':string(format='date-time')},['v','text','at'])
S['V3InvestigationCanvasNode'] = obj({
    **fieldnames('id parent title subtitle'),
    'kind':string(enum=['root','branch','evidence']),
    'status':string(enum=['active','running','ruled_out','confirmed']),
    'x':integer(minimum=-100000,maximum=100000),'y':integer(minimum=-100000,maximum=100000),
    'w':integer(minimum=120,maximum=2000),
    'collapsed':v3_bool(),'layout_version':integer(minimum=0),
    'context':ref('V3InvestigationCanvasContext'),
    'versions':arr(ref('V3InvestigationCanvasVersion')),
    'evidence_refs':arr(string()),
    # PA-62 follow-up: persisted writeback context revisions this node received and their source nodes.
    'writebacks':obj({'received':integer(minimum=0),'sources':arr(string())},['received','sources'])},
    ['id','kind','title','subtitle','status','x','y','w','collapsed','layout_version','context','versions','writebacks'])
S['V3InvestigationEvidenceTrayItem'] = obj({
    **fieldnames('id title source'),
    'kind':string(enum=['table']),
    'body':obj({'type':string(enum=['table']),'rows':arr(arr(string()))},['type','rows'])},
    ['id','kind','title','source','body'])
S['V3InvestigationCanvasGraph'] = obj({
    'investigation':obj({**fieldnames('id title'),
        'status':string(enum=['active','resolved']),
        'env':string(enum=['prod','staging','test']),
        'participants':arr(string())},['id','title','status','env','participants']),
    'loop':arr({}), 'nodes':arr(ref('V3InvestigationCanvasNode')),
    'tray':arr(ref('V3InvestigationEvidenceTrayItem'))},
    ['investigation','loop','nodes','tray'])
v3_route('get','/investigations/{id}/canvas','Read a persisted investigation graph',
    response=ref('V3InvestigationCanvasGraph'),
    desc='Current session, environment and canvas.view for the persisted application or owner team are rechecked in one snapshot. Both INV-* and historical IDs use actual branch IDs. Nodes are real branches with current message counts and immutable revision history; layout rows or deterministic legacy drawing coordinates supply positions. Context tokens is null because current context token occupancy is unknown; historical provider usage is not summed as occupancy. Evidence is shown as exact recorded kind/content/source table rows; untyped workflow loop data yields an empty array. No AI conclusion or status is inferred. A source environment outside the frozen prod/staging/test DTO returns 422 canvas_environment_unsupported; invalid/oversized graph data returns 503 or 413. The current frontend must handle actual root IDs, nullable tokens, and persisted inspector data.')
P['/investigations/{id}/canvas']['get']['responses']['413'] = {
    'description':'Investigation graph exceeds bounded node, revision, or evidence limits',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/investigations/{id}/branches','Fork an investigation branch from its current context',
    body={**obj({'parent_id':string(),'parent':string(),'title':string(minLength=1,maxLength=200),
      'x':integer(minimum=-100000,maximum=100000),'y':integer(minimum=-100000,maximum=100000),
      'expected_parent_revision':integer(minimum=0)},['title']),
      'anyOf':[{'required':['parent_id']},{'required':['parent']}]},
    response=ref('V3InvestigationCanvasNode'),status=201,
    desc='At least one of parent_id (current frontend) or parent (contract spelling) identifies a persisted branch in this investigation. Both aliases may be sent when equal; conflicting values are rejected. A supplied expected_parent_revision must match the locked parent; omitted revision forks the latest committed parent snapshot under a row lock. The child receives its own immutable revision-zero context, layout, event and management audit in one transaction. Only an open investigation with current canvas.change for its persisted environment/application/team may fork. No model call or inference occurs.')
P['/investigations/{id}/branches']['post']['responses']['413'] = {
    'description':'Graph or parent context exceeds the bounded fork limit',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('patch','/investigations/{id}/nodes/{node}','Update an investigation branch layout or title',
    body=obj({'x':integer(minimum=-100000,maximum=100000),
      'y':integer(minimum=-100000,maximum=100000),'title':string(minLength=1,maxLength=200),
      'collapsed':v3_bool(),'collapse':v3_bool(),
      'expected_layout_version':integer(minimum=0),'expected_title':string()},['expected_layout_version']),
    response=ref('V3InvestigationCanvasNode'),
    desc='A locked concrete branch and current canvas.change grant protect a single-environment open investigation. expected_layout_version is always required; a title edit also requires expected_title from the last read. Missing preconditions return 428; stale layout version or mismatched title returns 409, including a title changed through the legacy API. Position, collapse and optional title update atomically with a version increment, branch event and management audit. Context revision and parent/child topology are unchanged. collapsed and layout_version are additive server fields; the frozen frontend drag call lacks the required version and needs integration.')
P['/investigations/{id}/nodes/{node}']['patch']['requestBody']['content']['application/json']['schema']['dependentRequired'] = {'title':['expected_title']}
P['/investigations/{id}/nodes/{node}']['patch']['responses']['428'] = {
    'description':'Expected layout version, or expected title for a title edit, is required',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/investigations/{id}/nodes/{node}']['patch']['responses']['413'] = {
    'description':'Investigation graph exceeds bounded display limits',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3InvestigationBranchContext'] = obj({
    'branch_id': string(), 'revision': integer(minimum=0),
    'messages': arr({}),
    'attempts':arr(ref('V3InvestigationReplyAttempt')),
    'attempts_has_more':v3_bool()},
    ['branch_id','revision','messages','attempts','attempts_has_more'])
S['V3InvestigationReplyAttempt'] = obj({
    'id':string(),'text':string(),
    'state':string(enum=['running','failed']),
    'error_code':string(),'created_at':string(format='date-time')},
    ['id','text','state','created_at'])
v3_route('get','/investigations/{id}/nodes/{node}/messages',
    'Read the current persisted investigation branch context',
    response=ref('V3InvestigationBranchContext'),
    desc='Auxiliary read-only route outside the 166 V3 page contracts. Resolves persisted branch scope and requires current canvas.view. Returns the current revision and historical JSON messages as stored, plus the 50 most recent running or failed reply attempts. attempts_has_more marks additional older attempts. An expired running lease is displayed as failed. No synthetic assistant reply is inserted for a failed attempt. The stored context is limited to 24 KiB. An absent, mismatched, or inaccessible branch returns 404; missing or malformed context returns 503.')
v3_route('post','/investigations/{id}/nodes/{node}/messages',
    'Run a persisted investigation-branch model reply',
    body=obj({'text':string(minLength=1,maxLength=8192),'model':string(),
      'reasoning':string()},['text']),
    response=obj({'summary':string()},['summary']),
    desc='Requires an open, selected-environment investigation and current canvas.change on its persisted application or owner team. One live reply per branch; a legacy run also prevents a reply. The configured model receives the current branch context snapshot, which already contains its inherited parent history at fork time, plus this prompt. It receives no live ancestor, sibling or descendant state. The prompt/model/state and audit are durably recorded as a running attempt before the model call. An explicitly allowed tool-capable model may use service profile, metrics, log metadata and knowledge read-only tools fixed to the persisted application and environment, with at most four tool calls and five model calls within the existing 90-second budget. Authorization and source access are rechecked before and after tools and in the completion transaction; knowledge references must still match published document versions. A successful reply atomically persists evidence, assistant evidence references, user and assistant messages, the new branch context revision and completion audit. Tool attempt audits persist independently without query or output bodies. Model failure or revoked access retains a failed attempt without fabricating an assistant message. Concurrent runs or a changed context return 409; context exceeding 24 KiB returns 413. No child branch is generated in this response.')
P['/investigations/{id}/nodes/{node}/messages']['post']['responses']['413'] = {
    'description':'Current context or completed reply exceeds the branch context limit',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3InvestigationWritebackCandidate'] = obj({
    'id':string(),'text':string(),'evidence':arr(string()),
    'recommended':v3_bool(),'tokens':integer(minimum=0)},
    ['id','text','evidence','recommended'])
S['V3InvestigationWritebackPreview'] = obj({
    'from':obj({'id':string(),'title':string(),'version':string()},
      ['id','title','version']),
    'to':obj({'id':string(),'title':string(),'version':string(),
      'next_version':string(),'hops':integer(minimum=1)},
      ['id','title','version','next_version','hops']),
    'source_revision':integer(minimum=0),'target_revision':integer(minimum=0),
    'target':string(),
    'conclusions':arr(ref('V3InvestigationWritebackCandidate')),
    'timeline':arr(obj({'v':string(),'text':string(),'meta':string(),
      'isNew':v3_bool()},['v','text','meta'])),
    'checks':arr(obj({'tone':string(enum=['ok','warn']),
      'text':string()},['tone','text'])),
    'budget':obj({key:{'type':['integer','null'],'minimum':0} for key in
      ['before','after','limit']},['before','after','limit'])},
    ['from','to','source_revision','target_revision','target','conclusions',
      'timeline','checks','budget'])
v3_route('get','/investigations/{id}/writeback-preview',
    'Preview persisted branch-to-ancestor context writeback',
    query=[v3_query(name) for name in ['node','from','target']],
    response=ref('V3InvestigationWritebackPreview'),
    desc='Supply node or its from alias; equal aliases are accepted. An omitted target selects the persisted root ancestor. A supplied target must be a strict ancestor of the source. Requires current canvas.view over the investigation. Candidate conclusions are exact assistant messages in the current source context whose IDs have not already been written to this target, including historical writeback provenance; recommended is always false. Source/target revision integers are supplied for writeback preconditions. Timeline labels describe persisted target revisions without inferring conclusions. Budget fields are null because no current token estimate is available; the frozen frontend numeric budget type needs adaptation. Preview is informational and does not reserve a revision or synthesize a summary. Oversized source/target context or graph returns 413.')
P['/investigations/{id}/writeback-preview']['get']['responses']['413'] = {
    'description':'Branch graph or source/target context exceeds bounded preview limits',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/investigations/{id}/writeback',
    'Write selected persisted source messages into an ancestor context',
    body={**obj({'node':string(),'from':string(),'target':string(),
      'items':arr(string()),'conclusions':arr(string()),
      'mode':string(enum=['summary','messages','evidence']),
      'expected_source_revision':integer(minimum=0),
      'expected_target_revision':integer(minimum=0)},
      ['target','mode','expected_source_revision','expected_target_revision']),
      'allOf':[
        {'anyOf':[{'required':['node']},{'required':['from']}]},
        {'anyOf':[{'required':['items']},{'required':['conclusions']}]}]},
    response=obj({'version':string(),'source_revision':integer(minimum=0),
      'target_revision':integer(minimum=0)},['version','source_revision','target_revision']),
    desc='Supply node or its from alias, an explicit strict-ancestor target, and selected current source assistant message IDs in items or the conclusions alias. Equal aliases are accepted. Requires an open selected-environment investigation, current canvas.change, and both numeric revisions from a recent preview. Missing revision preconditions return 428; stale source or target returns 409. A source message already written to this target returns 409 even after later context rewrites. The summary mode records selected historical assistant text as a marked untrusted user context message, without model synthesis. The messages mode copies selected assistant messages unchanged. Evidence mode requires persisted evidence references and stores a marked historical evidence snapshot. A successful writeback appends only a new target context revision, event and audit; source context and infrastructure are untouched. Invalid ancestry/selection/evidence returns 422; context over 24 KiB or oversized graph returns 413.')
P['/investigations/{id}/writeback']['post']['responses']['428'] = {
    'description':'Expected source and target revisions are required',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/investigations/{id}/writeback']['post']['responses']['413'] = {
    'description':'Branch graph or source/target context exceeds writeback limits',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/canvas-templates','List available canvas structures',
    response=obj({'items':arr(ref('V3CanvasTemplate'))},['items']),
    desc='Lists persisted generic-canvas system blank templates plus the current owner private templates in currently authorized environments. The session, environment.view and canvas.view grants are rechecked against one bounded database snapshot; more than 500 visible items returns 422. A private template has no team scope, and investigation templates belong to a separate page; the frozen Templates page does not consume this list.')
P['/canvas-templates']['get']['responses']['422'] = {
    'description':'Visible template list exceeds 500 items',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/canvas-templates','Save a private canvas template with selected retention',
    body=obj({'from_canvas':string(),'name':string(),
      'keep':obj({k:v3_bool() for k in ['nodes','steps','runbooks','content']},['nodes'])},
      ['from_canvas','name','keep']),response=ref('V3CanvasTemplate'),status=201,
    desc='Requires one selected environment, current ownership of the generic source canvas, active referenced services and current canvas.change grants in the save transaction. keep.nodes is explicit: false discards graph edges/layout but allows selected content or steps as unlinked nodes. keep.steps retains persisted steps descriptions and resets done to todo with an unexecuted review marker. keep.content copies validated private node text and selected static blocks only when explicitly true; it does not automatically redact service names or numbers. Live bars, row values/states, badges, evidence, exchanges and execution results are never copied. keep.runbooks accepts only exact /runbooks/{id} source artifact links, confirms current published document identity/version, same selected environment, application binding and knowledge.view, then saves version-bound private metadata. No explicit link returns retention.runbooks=0 and runbook_source=no_structured_reference; plain text never becomes a Runbook reference. Use of the template rechecks version and scope and recreates only a read-only Runbook link artifact. Rich private templates cannot pass strict system/config-export validation; the template is usable only by the same owner in the same environment. Shared capacity admission protects the 500-item list across private saves and system imports. Template and audit commit together. Duplicate names, changed Runbooks or source bindings return a controlled conflict; invalid structure returns 422. The frozen page still calls these team templates and promises content redaction, so its product copy remains unverified. Investigation/team template creation remains separate.')
P['/canvas-templates']['post']['responses']['422'] = {
    'description':'Invalid retained structure, malformed explicit Runbook link, or template list capacity exceeded',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/canvas-templates']['post']['responses']['409'] = {
    'description':'Duplicate template name, changed source service, or unavailable linked Runbook',
    'content':{'application/json':{'schema':ref('Error')}}}

S['V3InvestigationTemplateBranch'] = obj({
    'name':string(),'step':string(),'tools':arr(string())},
    ['name','step','tools'])
S['V3InvestigationTemplate'] = obj({
    'id':string(),'name':string(),'owner':string(enum=['official','team']),
    'description':string(),'branches':arr(ref('V3InvestigationTemplateBranch')),
    'tools':arr(string()),'uses':string(),'version':string(),
    'maintainer':string(),
    'triggers':obj({'manual':v3_bool(),'auto_triage':v3_bool(),
      'matchers':arr(obj({'name':string(),'value':string()},['name','value']))},
      ['manual','auto_triage','matchers']),
    'runbooks':arr(string()),
    'budget':obj({'turns':integer(minimum=1,maximum=8),
      'tools':integer(minimum=0,maximum=16),
      'minutes':integer(minimum=1,maximum=5)},['turns','tools','minutes'])},
    ['id','name','owner','description','branches','tools','uses','version',
      'maintainer','triggers','runbooks','budget'])
v3_route('get','/investigation-templates','List persisted, scoped investigation templates',
    query=[v3_query('owner',string(enum=['all','official','team'])),v3_query('q')],
    response=obj({'items':arr(ref('V3InvestigationTemplate')),
      'counts':obj({k:integer(minimum=0) for k in ['all','official','team']},
        ['all','official','team'])},['items','counts']),
    desc='An empty configured catalog returns an empty list. Current session, environment and canvas.view grants, active application mappings, team membership and reference grants filter persisted definitions in one bounded read transaction. Owner and q filter items; counts cover the full visible set. At most 500 scoped candidate rows may be read. Definitions are manual outlines only; no auto-triage, model or tool execution is implied. MCP tool names and Runbook IDs are configured references, checked again at start.')
v3_route('post','/investigation-templates/{id}/start',
    'Start a persisted investigation from a reviewed template outline',
    body=obj({'inputs':obj({'application_id':string(),
      'template_version':string(),'request_id':string(minLength=8,maxLength=128)},
      ['application_id','template_version','request_id'])},['inputs']),
    response=obj({'investigation_id':string()},['investigation_id']),status=201,
    desc='Requires one selected environment; all returns 409. Current session, canvas.change on the active mapped application or owner team, team-template membership, listed template version, published application-scoped Runbooks and knowledge.view, and current read-only reviewed MCP tool bindings with connectors.view are checked in the start transaction. A successful start atomically creates an investigation, root and template child branches with empty contexts and source provenance, a frozen definition and budget snapshot, events and audit. It does not run an Agent, model, Runbook or tool. The user-scoped request_id deduplicates concurrent retries; a matching committed start returns 200 after current session and canvas.change rechecks, even if its source template was subsequently disabled. Conflicting inputs return 409. Disabled/stale templates and revoked scopes do not create a new investigation. The frozen frontend currently sends no inputs, so its start button needs application selection, version, request ID and error handling before real-mode use.')
P['/investigation-templates/{id}/start']['post']['responses']['200'] = {
    'description':'Idempotent replay of an already committed template start',
    'content':{'application/json':{'schema':obj({'investigation_id':string()},['investigation_id'])}}}
for _status,_description in {
    '409':'Selected environment is read-only, template/dependency changed, or request ID conflicts',
    '422':'Application, template version or request ID is invalid',
    '503':'Template definition, authorization source or persistence is unavailable',
}.items():
    P['/investigation-templates/{id}/start']['post']['responses'][_status] = {
      'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
