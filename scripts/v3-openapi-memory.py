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
    response=ref('V3MemoryFactDecision'),
    desc='Persists a valid source-backed fact state transition and audit in one transaction. Only confirm, reject and expire are supported; no forget action. Requires current account, environment, application/team knowledge.manage. Confirm requires a current published source and matching version; reject and expire may clean up a stale source under its original still-active scope and redact fact text and source identity. Confidence, recall and expiry metrics remain unavailable. The frozen frontend forget action is incompatible.')
P['/memory/facts/{id}/{action}']['post']['parameters'][1]['schema'] = string(enum=['confirm','reject','expire'])
v3_route('post','/memory/conflicts/resolve','Resolve an open conflict between two persisted facts',
    body=obj({'keep':string(minLength=1,maxLength=128),'drop':string(minLength=1,maxLength=128)},['keep','drop']),
    response=obj({'id':string(),'state':string(enum=['resolved']),
        'keep':string(),'drop':string()},['id','state','keep','drop']),
    desc='Atomically resolves an existing open conflict, updates both distinct source-backed facts and writes an audit. Requires actual fact IDs, current source provenance/version and current account, environment, application/team knowledge.manage. The frozen frontend sends a display choice without both IDs, so its interaction remains unavailable.')
