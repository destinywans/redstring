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
