# Search and @mentions use persisted, scoped service, incident, investigation,
# person and conversation records. There is no persisted recent navigation list.
v3_discovery_kinds = ['service','incident','investigation','conversation','person']
v3_discovery_tones = ['neutral','accent','ok','warn','danger','info']
S['V3SearchHit'] = obj({**fieldnames('id title subtitle href'),
    'kind':string(enum=v3_discovery_kinds), 'status':string(enum=v3_discovery_tones)},
    ['kind','id','title','href'])
S['V3MentionCandidate'] = obj({**fieldnames('id label meta'),
    'kind':string(enum=v3_discovery_kinds), 'status':string(enum=v3_discovery_tones),
    'tier':string(enum=['T0','T1','T2'])}, ['kind','id','label'])
S['V3MentionPreviewPartial'] = obj({**fieldnames('id title subtitle status_label href'),
    'kind':string(enum=v3_discovery_kinds),
    'tier':string(enum=['T0','T1','T2']),
    'status':string(enum=v3_discovery_tones),
    'metrics':arr(obj({**fieldnames('label value tone'),'series':arr(v3_num())},
        ['label','value','series'])),
    'change':obj(fieldnames('brand text meta'), ['brand','text','meta']),
    'related':arr(obj(fieldnames('label tone'), ['label']))},
    ['kind','id','title','subtitle','metrics','related'])
v3_route('get','/search','Search available persisted records',
    response=obj({'query':string(),'hits':arr(ref('V3SearchHit')),'recent':arr(ref('V3SearchHit'))},
        ['query','hits','recent']),
    query=[v3_query('q')],
    desc='The supported persisted kinds are scoped services, incidents, investigations, active visible human members and own conversations, with Chinese or pinyin matching. At most eight balanced hits are returned; real investigation IDs link to the canvas list until the frontend route accepts them. Recent is always empty until user navigation history exists; no navigation history is synthesized.')
v3_route('get','/mentions','List available mention candidates',
    response=obj({'items':arr(ref('V3MentionCandidate'))},['items']),
    query=[v3_query('q'),v3_query('kind',string(enum=['service','incident','investigation','conversation','person']))],
    desc='Scoped service, incident, investigation, active visible human member and own conversation references are supported. At most 30 balanced candidates are returned; clients need a kind-specific query for a complete tab. Other RefKind values return 422 unsupported_kind when requested by kind, because their complete scope and reference-send path are not established.')
v3_route('get','/mentions/{kind}/{id}/preview','Preview an accessible reference',
    response=ref('V3MentionPreviewPartial'),
    desc='Scoped service, incident, investigation, active visible human member and own conversation records have verified previews; other kinds return 503 preview_unavailable. A missing or unauthorized ID returns 404 without revealing existence. Metrics and related records are empty until sources are connected. Incident status labels reflect persisted incident workflow state, not live alert health.')
for _path in ['/search','/mentions']:
    P[_path]['get']['responses']['413'] = {
        'description':'More than 100 matching records in a source kind exceed the bounded query; narrow the search',
        'content':{'application/json':{'schema':ref('Error')}}}
S['V3TextCommandItem'] = obj({'id':string(),'group':string(enum=['command']),
    'name':string(),'desc':string(),'icon':string()},
    ['id','group','name','desc','icon'])
v3_route('get','/commands','Read configured text command suggestions',
    response=obj({'items':arr(ref('V3TextCommandItem'))},['items']),
    desc='A deployment may explicitly configure at most 32 environment-bound, permission-checked slash text suggestions. They insert literal text into the composer and do not execute an action or imply a model/tool capability. Only the command group is currently returned; skill, Runbook and template chips require a verified reference-send path. A configured empty catalog returns items:[]; an unconfigured catalog returns 503 commands_unavailable. Aggregate all-environment view returns no sendable suggestions.')

# These hover cards intentionally expose partial, evidence-backed records. The
# frozen frontend DTOs still require guessed health/load/duty strings; see
# docs/api/v3-peeks.md for the exact consumer differences.
v3_peek_nullable = lambda schema: {'anyOf':[schema,{'type':'null'}]}
S['V3ServicePeekKpi'] = obj({'k':string(),'v':string(),
    'tone':string(enum=['neutral','accent','ok','warn','danger','info'])},['k','v'])
S['V3ServicePeekPartial'] = obj({
    'id':string(), 'name':string(),
    'tier':v3_peek_nullable(string(enum=['T0','T1','T2'])),
    'team':v3_peek_nullable(string()), 'env':string(),
    'status':v3_peek_nullable(string(enum=['ok','warn','danger'])),
    'kpis':arr(ref('V3ServicePeekKpi')),
    'owner':v3_peek_nullable(string()),
    'oncall':v3_peek_nullable(string()),
    'change':v3_peek_nullable(string())},
    ['id','name','tier','team','env','status','kpis','owner','oncall','change'])
S['V3PersonPeekPartial'] = obj({
    'id':string(), 'name':string(),
    'role':v3_peek_nullable(string()),
    'oncall':v3_peek_nullable(string()),
    'load':v3_peek_nullable(string())},
    ['id','name','role','oncall','load'])
v3_route('get','/services/{id}/peek','Preview a visible persisted service',
    response=ref('V3ServicePeekPartial'),
    desc='Accepts the composite service ID, the exact application ID used by persisted alert groups, or an exact application key within the selected environment scope. Multiple visible matches across applications or environments return 409; absent and unauthorized records both return 404. Identity, tier, team and configured owner label are persisted metadata. No live observation source is wired to this domain: status, current on-call and last change are null, and KPIs are empty. Null is unknown, never healthy or off duty. The frozen ServicePeek frontend type and card require adaptation before this is a truthful visible UI.')
v3_route('get','/people/{name}/peek','Preview an exact visible human member name',
    response=ref('V3PersonPeekPartial'),
    desc='Exact display-name lookup uses the current selected-environment members.view scope and excludes disabled, deleted, demo and machine accounts. Multiple visible matching accounts return 409; absent and unauthorized accounts both return 404. Role is the persisted user-maintained job title, not an RBAC grant, and may be null. Current on-call and total workload are null without authorized complete sources. The frozen PersonPeek frontend type and card require adaptation so unknown duty is not presented as off duty.')
