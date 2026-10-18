# Source-backed memory governance. The write routes have their own persisted
# result contracts; neither route supplies the complete frozen MemoryView.
S['V3MemoryFactEvidence'] = obj({**fieldnames('id fact source state environment_id application_id team_id'),
    'source_version':integer(),'source_current':v3_bool(),
    'confirmed':{'type':['string','null'],'format':'date-time'},
    'created_at':string(format='date-time'),'revision':integer()},
    ['id','fact','source','source_version','state','environment_id','confirmed','created_at','revision'])
S['V3MemoryFactRedacted'] = obj({'id':string(),
    'state':string(enum=['rejected','expired']), 'revision':integer(minimum=1),
    'confirmed':{'type':['string','null'],'format':'date-time'}},
    ['id','state','revision','confirmed'])
S['V3MemoryFactDecision'] = {'oneOf':[
    obj({'fact':ref('V3MemoryFactEvidence'),
        'fact_redacted':{'type':'boolean','enum':[False]},
        'partial':{'type':'boolean','enum':[True]},
        'unavailable_fields':arr(string())},
        ['fact','fact_redacted','partial','unavailable_fields']),
    obj({'fact':ref('V3MemoryFactRedacted'),
        'fact_redacted':{'type':'boolean','enum':[True]},
        'partial':{'type':'boolean','enum':[True]},
        'unavailable_fields':arr(string())},
        ['fact','fact_redacted','partial','unavailable_fields'])]}
S['V3MemoryLayerEvidence'] = obj({
    'kind':string(enum=['current_published_facts']),
    'fact_ids':arr(string())}, ['kind','fact_ids'])
S['V3MemoryLayer'] = obj({
    'id':string(enum=['service','workspace']), 'name':string(),
    'count':integer(minimum=0), 'description':string(),
    'evidence':ref('V3MemoryLayerEvidence')},
    ['id','name','count','description','evidence'])
S['V3MemoryService'] = obj({
    **fieldnames('id application_id environment_id name key'),
    'count':integer(minimum=0), 'fact_ids':arr(string())},
    ['id','application_id','environment_id','name','key','count','fact_ids'])
S['V3MemoryViewFactEvidence'] = obj({
    'document_id':string(), 'version':integer(minimum=1)},
    ['document_id','version'])
S['V3MemoryViewFact'] = obj({
    **fieldnames('id fact source state environment_id application_id team_id'),
    'state':string(enum=['candidate','confirmed']),
    'source_version':integer(minimum=1),
    'source_current':{'type':'boolean','enum':[True]},
    'confirmed':{'type':['string','null'],'format':'date-time'},
    'created_at':string(format='date-time'), 'revision':integer(minimum=1),
    'confidence':{'type':'null'}, 'recalls':{'type':'null'},
    'expires':{'type':'null'}, 'evidence':ref('V3MemoryViewFactEvidence'),
    'source_title':{'type':['string','null'],'description':'Title of the source document; null when it has none (PA-166).'}},
    ['id','fact','source','source_title','source_version','source_current','state',
     'environment_id','confirmed','created_at','revision',
     'confidence','recalls','expires','evidence'])
S['V3MemoryConflictSide'] = obj({**fieldnames('id text source'), 'source_title':{'type':['string','null']}}, ['id','text','source','source_title'])
S['V3MemoryConflict'] = obj({
    **fieldnames('id state environment_id application_id team_id'),
    'state':string(enum=['open']),
    'a':ref('V3MemoryConflictSide'), 'b':ref('V3MemoryConflictSide')},
    ['id','state','environment_id','a','b'])
S['V3MemoryView'] = obj({
    'layers':{'type':'array','minItems':2,'maxItems':2,'items':ref('V3MemoryLayer')},
    'services':arr(ref('V3MemoryService')),
    'service':{'type':['string','null']},
    'facts':arr(ref('V3MemoryViewFact')),
    'conflicts':arr(ref('V3MemoryConflict')),
    'conflict':{'anyOf':[ref('V3MemoryConflict'),{'type':'null'}]},
    'policy':{'type':'null'},
    'partial':{'type':'boolean','enum':[True]},
    'unavailable_fields':arr(string())},
    ['layers','services','service','facts','conflicts','conflict',
     'policy','partial','unavailable_fields'])
v3_route('get','/memory','Read authorized source-backed memory view',
    response=ref('V3MemoryView'),query=[v3_query('service')],
    desc='Returns only candidate or confirmed facts from exact current published document versions and current environment/application/team knowledge.view scope. Layer counts derive from returned bounded facts; services is the visible service-fact directory across current scope. Service and workspace are evidence categories, not claims of a complete memory system. Optional service must resolve exactly to a visible stable service ID, application ID or key; stable IDs take precedence over aliases. It filters facts and conflicts to that application/environment, while workspace layer count becomes zero. Ambiguous aliases return 409, no visible match 404. Open conflicts appear only when both fact sides remain visible. Facts are capped at 500 and open conflicts at 100; excess returns 413. Confidence, recall, expiry and policy are unknown, represented as null. This partial response differs from the frozen frontend MemoryView.')
P['/memory']['get']['responses']['413'] = {
    'description':'More than 500 visible facts or 100 visible open conflicts exceed the bounded view',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/memory/facts','Read current scoped source-backed memory facts',
    response=obj({'items':arr(ref('V3MemoryFactEvidence')),'partial':v3_bool(),
        'unavailable_fields':arr(string())},['items','partial','unavailable_fields']),
    desc='Auxiliary partial fact-governance list including candidate, confirmed, rejected and expired states. Only current published source versions and current knowledge.view scope are returned. Confidence, recall, expiry, layer counts and policy are unavailable; more than 500 qualifying facts returns 413.')
P['/memory/facts']['get']['responses']['413'] = {
    'description':'More than 500 qualifying memory facts exceed the bounded view',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/memory/facts/{id}/{action}','Confirm, reject or expire a source-backed memory fact',
    response=ref('V3MemoryFactDecision'),
    desc='Persists a valid source-backed fact state transition and audit in one transaction. Only confirm, reject and expire are supported; no forget action. Requires current account, environment, application/team knowledge.manage. Confirm requires a current published source and matching version; reject and expire may clean up a stale source under its original still-active scope and redact fact text and source identity. Confidence, recall and expiry metrics remain unavailable. The frozen frontend forget action is incompatible.')
P['/memory/facts/{id}/{action}']['post']['parameters'][1]['schema'] = string(enum=['confirm','reject','expire'])
v3_route('post','/memory/conflicts/resolve','Resolve an open conflict between two persisted facts',
    body=obj({'keep':string(minLength=1,maxLength=128),'drop':string(minLength=1,maxLength=128)},['keep','drop']),
    response=obj({'id':string(),'state':string(enum=['resolved']),
        'keep':string(),'drop':string()},['id','state','keep','drop']),
    desc='Atomically resolves an existing open conflict, updates both distinct source-backed facts and writes an audit. Requires actual fact IDs, current source provenance/version and current account, environment, application/team knowledge.manage. The frozen frontend sends a display choice without both IDs, so its interaction remains unavailable.')
