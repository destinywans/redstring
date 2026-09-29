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
