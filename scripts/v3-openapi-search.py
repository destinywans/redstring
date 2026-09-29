# Search and @mentions use persisted, scoped service, incident, investigation,
# person and conversation records. No recent list or command registry exists.
S['V3SearchHit'] = obj({**fieldnames('kind id title subtitle status href')})
S['V3MentionCandidate'] = obj({**fieldnames('kind id label meta status')})
S['V3MentionPreviewPartial'] = obj({**fieldnames('kind id title subtitle status_label href'),
    'tier':string(enum=['T0','T1','T2']),
    'status':string(enum=['neutral','accent','ok','warn','danger','info']),
    'metrics':arr(obj({**fieldnames('label value tone'),'series':arr(v3_num())},
        ['label','value','series'])),
    'change':obj(fieldnames('brand text meta'), ['brand','text','meta']),
    'related':arr(obj(fieldnames('label tone'), ['label']))},
    ['kind','id','title','subtitle','metrics','related'])
v3_route('get','/search','Search available persisted records',
    response=obj({'query':string(),'hits':arr(ref('V3SearchHit')),'recent':arr(ref('V3SearchHit'))}),
    query=[v3_query('q')],
    desc='Partial: scoped services, incidents, investigations, active visible human members and own conversations can be found with Chinese or pinyin matching. At most eight balanced hits are returned; real investigation IDs link to the canvas list until the frontend route accepts them. Recent is always empty until user navigation history exists.')
v3_route('get','/mentions','List available mention candidates',
    response=obj({'items':arr(ref('V3MentionCandidate'))}),
    query=[v3_query('q'),v3_query('kind',string(enum=['service','incident','investigation','conversation','person']))],
    desc='Partial: scoped service, incident, investigation, active visible human member and own conversation references are supported. At most 30 balanced candidates are returned; clients need a kind-specific query for a complete tab. Alert group, artifact and Runbook references have no verified source here.')
v3_route('get','/mentions/{kind}/{id}/preview','Preview an accessible reference',
    response=ref('V3MentionPreviewPartial'),
    desc='Partial: scoped service, incident, investigation, active visible human member and own conversation records have verified previews; other kinds return 503 preview_unavailable. Metrics and related records are empty until sources are connected. Incident status labels reflect persisted incident workflow state, not live alert health.')
for _path in ['/search','/mentions']:
    P[_path]['get']['responses']['413'] = {
        'description':'More than 100 matching records in a source kind exceed the bounded query; narrow the search',
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/commands','Read executable command catalog',
    desc='Currently always returns 503 commands_unavailable; no verified command registry is configured.')
P['/commands']['get']['responses'].pop('200')
