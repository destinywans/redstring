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
    desc='Generic canvases are private to their owner; investigations use persisted environment and application/team grants. Current sessions and role sources are checked before a single database snapshot supplies counts and page items. Historical investigations without a recorded creator return an empty participants array. Persisted investigation graph detail is available at /investigations/{id}/canvas; this list does not include graph nodes.')
v3_route('post','/canvases','Create a persisted canvas or investigation',
    body=obj({**fieldnames('title kind template_id goal'),
      'services':arr(string()),'ai_draft':v3_bool()},['title','kind']),
    response=obj(fieldnames('id href'),['id','href']),status=201,
    desc='Requires one selected environment and a current canvas.change grant. Generic canvases are private and require an environment grant; investigations require exactly one active mapped application and accept environment, application or owner-team grants. Investigation IDs are persisted INV-* IDs with an initial branch and context. Canvas/branch records and creation audit commit together. Optional AI drafting uses one configured model, no tools, and a validated one-to-three-node JSON outline; it is explicitly unverified and a failure leaves no creation. Missing model returns 503 canvas_draft_unavailable. Investigation templates return 422 invalid_template. Persisted investigation graph detail is available at /investigations/{id}/canvas.')
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
    'evidence_refs':arr(string())},
    ['id','kind','title','subtitle','status','x','y','w','collapsed','layout_version','context','versions'])
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
    'messages': arr({})}, ['branch_id','revision','messages'])
v3_route('get','/investigations/{id}/nodes/{node}/messages',
    'Read the current persisted investigation branch context',
    response=ref('V3InvestigationBranchContext'),
    desc='Auxiliary read-only route outside the 166 V3 page contracts. Resolves the branch, investigation, application, environment, and owner team from persisted records and requires current canvas.view for the application or owner team. Returns only the current branch revision and its historical JSON messages, preserving legacy optional fields as stored; it does not synthesize nodes, timestamps, summaries, or model output. The stored context is limited to 24 KiB. An absent, mismatched, or inaccessible branch returns 404; a missing, oversized, or malformed current context returns 503. The graph and layout/branch writes have separate routes; ancestor writeback and investigation node model replies remain unavailable.')
v3_route('get','/canvas-templates','List available canvas structures',
    response=obj({'items':arr(ref('V3CanvasTemplate'))},['items']),
    desc='Currently lists only private, persisted generic-canvas structure templates owned by the current user in authorized non-production environments. Investigation and team templates are unavailable; the full page contract remains mock.')
v3_route('post','/canvas-templates','Save a safe canvas structure template',
    body=obj({'from_canvas':string(),'name':string(),
      'keep':obj({k:v3_bool() for k in ['nodes','steps','runbooks','content']})},
      ['from_canvas','name']),response=ref('V3CanvasTemplate'),status=201,
    desc='Partial main-contract operation: requires the current owner, selected source environment and canvas.change grant, rechecked with session and role sources inside the save transaction. Copies a user-chosen template name plus sanitized generic-canvas node/edge layout; node titles, blocks, artifacts and conversations are not copied. The graph is validated before template and audit commit together. keep.steps, keep.runbooks and keep.content return 422; the current frontend sends steps/runbooks by default and describes a team template, while this backend saves a private template. Investigation sources and read-only steps are unavailable, so the main route remains mock.')
