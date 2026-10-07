# WAN-240: explicit human publication and exact-source memory production.
_memory_hash = string(pattern=r'^[a-f0-9]{64}$')
S['V3MemorySourcePublish'] = obj({
    'request_key':string(minLength=8,maxLength=128),
    'application_id':string(minLength=1,maxLength=128),
    'title':string(minLength=1,maxLength=200),
    'body':string(minLength=1,maxLength=16384,description='At most 16 KiB UTF-8; human-reviewed source text, not an automatic external sync.')},
    ['request_key','application_id','title','body'])
S['V3MemorySourcePublished'] = obj({
    'document_id':string(),'version':integer(minimum=1),'content_hash':_memory_hash,
    'environment_id':string(),'application_id':string(),'team_id':string(),
    'lifecycle':string(enum=['published']),'created':v3_bool()},
    ['document_id','version','content_hash','environment_id','application_id','lifecycle','created'])
S['V3MemoryProductionRequest'] = obj({
    'document_id':string(minLength=1,maxLength=128),
    'expected_version':integer(minimum=1),'expected_content_hash':_memory_hash,
    'exact_excerpt':string(minLength=1,maxLength=4000)},
    ['document_id','expected_version','expected_content_hash','exact_excerpt'])
S['V3MemoryProductionResult'] = obj({'fact':ref('V3MemoryFactEvidence'),
    'source_content_hash':_memory_hash,'created':v3_bool(),
    'production':string(enum=['human_exact_excerpt'])},
    ['fact','source_content_hash','created','production'])
S['V3MemoryConflictRecordRequest'] = obj({'fact_a_id':string(minLength=1,maxLength=128),
    'fact_b_id':string(minLength=1,maxLength=128),
    'expected_revision_a':integer(minimum=1),'expected_revision_b':integer(minimum=1),
    'reason':string(minLength=1,maxLength=1000)},
    ['fact_a_id','fact_b_id','expected_revision_a','expected_revision_b','reason'])
S['V3MemoryConflictRecord'] = obj({**fieldnames('id fact_a_id fact_b_id environment_id application_id team_id'),
    'state':string(enum=['open'])},['id','fact_a_id','fact_b_id','environment_id','state'])
S['V3MemoryConflictRecordResult'] = obj({'conflict':ref('V3MemoryConflictRecord'),
    'reason':string(),'created':v3_bool(),'assertion':string(enum=['human'])},
    ['conflict','reason','created','assertion'])
v3_route('post','/memory/sources','Publish a reviewed human memory source',
    body=ref('V3MemorySourcePublish'),response=ref('V3MemorySourcePublished'),
    desc='Exact active Cookie and current human identity, knowledge.view plus knowledge.manage for the current single environment, application and current owner team. Writes a published knowledge document with explicit human provenance, version snapshot, history and audit in one transaction. Application/environment/team are locked and cannot be supplied as arbitrary metadata. Request key is scoped to actor and environment; identical replay returns 200, drift or a withdrawn document returns 409. New publication returns 201. Body is limited to 16 KiB UTF-8. This is explicit human publication, not automatic model extraction or external-source approval; it does not create a vector index. All-environment writes remain 409.')
v3_route('post','/memory/facts','Produce an exact-excerpt memory candidate',
    body=ref('V3MemoryProductionRequest'),response=ref('V3MemoryProductionResult'),
    desc='Requires a human with current knowledge.view and knowledge.manage over exact environment/application/team and an active Cookie. Source must be an eligible currently published knowledge document with explicit environment attribution, exact expected version and SHA256 matching the actual body. V3 Runbooks and registered external-source content are excluded. Excerpt must occur verbatim. Semantic deduplication binds source version/hash, exact excerpt and scope; identical retries by authorized humans reuse the same fact, preserving rejected/expired states. No model similarity or generated text is treated as truth. Returns 201 on first production and 200 on replay, with one audit for the original production.')
v3_route('post','/memory/conflicts','Record a human-asserted memory conflict',
    body=ref('V3MemoryConflictRecordRequest'),response=ref('V3MemoryConflictRecordResult'),
    desc='Requires a human, exact active Cookie and knowledge.view plus manage over both facts. Both facts must be active, in the same exact scope and backed by current published sources, with matching submitted fact revisions. Reason is an explicit human assertion, never an automatic semantic contradiction. Stable fact lock order prevents reversed-pair deadlocks. The same open pair and reason is idempotent, reason drift or stale state is 409. Existing human conflict resolution decides keep/drop.')
for _memory_path in ('/memory/sources','/memory/facts','/memory/conflicts'):
    P[_memory_path]['post']['responses']['201'] = P[_memory_path]['post']['responses']['200'].copy()
    P[_memory_path]['post']['responses']['201']['description'] = 'New record persisted with atomic audit'
v3_route('post','/memory/sources/{id}/withdraw','Withdraw a manually published memory source',
    body=obj({'expected_version':integer(minimum=1),'expected_content_hash':_memory_hash},['expected_version','expected_content_hash']),
    response=obj({'document_id':string(),'lifecycle':string(enum=['archived']),
        'changed':v3_bool(),'expired_facts':integer(minimum=0)},['document_id','lifecycle','changed','expired_facts']),
    desc='Only manual memory sources, not arbitrary legacy or external documents. Requires human knowledge.view/manage in original current app/team/environment plus active Cookie. Exact current source version/hash are required. Atomically archives the source, expires up to 500 active derived facts, clears its vector generation and writes version history/audit; retains rejected facts and historical conflicts as nonactionable history. Replays do not duplicate audit. Stale version/hash returns 409 rather than withdrawing a newer source version. Source-scoped lock serializes production and withdrawal. Main reads, confirmation and conflict resolution fail closed after withdrawal.')
P['/memory/sources/{id}/withdraw']['post']['responses']['413'] = {
    'description':'More than 500 active derived facts; expire them individually before withdrawal',
    'content':{'application/json':{'schema':ref('Error')}}}
for _memory_path in ('/memory/facts/{id}/{action}','/memory/conflicts/resolve'):
    P[_memory_path]['post']['description'] += ' Governance writes require a current human identity; machine identities cannot confirm, reject, expire or resolve memory facts.'
