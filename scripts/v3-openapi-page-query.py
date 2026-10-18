# Page v1.1 persistence and bounded server query extensions. Executed after
# existing domain fragments. Query values never widen environment/module ACL.
S['V3ServiceSLOSettings'] = obj({'target':v3_svc_null(string(enum=['99.5','99.9','99.95','99.99'])),
    'window':v3_svc_null(string(enum=['7d','30d'])), 'revision':integer(minimum=0),
    'unavailable':v3_svc_unavailable()},['target','window','revision','unavailable'])
v3_route('get','/services/{id}/slo','Read explicit service/environment SLO configuration',
    response=ref('V3ServiceSLOSettings'),
    desc='Configuration only; does not assert measured SLO or window coverage. Before the first save target and window are null with not_configured in unavailable and revision is 0; initialize via PUT expected_revision=0. The opaque service ID binds application and environment; current services.view and human session are checked transactionally.')
v3_route('put','/services/{id}/slo','Update explicit service/environment SLO configuration',
    body=obj({'expected_revision':integer(minimum=0),'target':string(enum=['99.5','99.9','99.95','99.99']),
              'window':string(enum=['7d','30d'])},['expected_revision','target','window']),
    response=ref('V3ServiceSLOSettings'),
    desc='Current services.manage in exactly one environment. First write uses revision 0; later stale writes return 409 slo_revision_changed. Success audit and profile target synchronize atomically. Runtime budget and telemetry calculations remain independently sourced.')
S['V3TraceSavedView'] = obj(fieldnames('id name query owner'),['id','name','query','owner'])
v3_route('get','/trace-views','List team-shared saved trace queries',
    query=[v3_query('scope',string(enum=['app','agent']))],
    response=obj({'items':arr(ref('V3TraceSavedView'))},['items']),
    desc='Requires current team membership and traces.view in each visible environment/team. Scope is required and single-valued. More than 500 visible views returns 413, never a silently truncated list.')
v3_route('post','/trace-views','Save a trace query for the current team',
    body=obj({'scope':string(enum=['app','agent']), 'name':string(minLength=1,maxLength=160),'query':string(maxLength=2048)},['scope','name','query']),
    response=obj({'id':string()},['id']),status=201,
    desc='Requires one selected environment, exactly one persisted team membership and current team traces.manage. Ambiguous or missing team returns 422 ambiguous_team. Query is inert saved text, not a query execution or an ACL grant.')
v3_route('delete','/trace-views/{id}','Delete a team-shared saved query',status=204,
    desc='Requires current membership in the persisted team, matching environment and traces.manage. Unrelated or missing IDs return 404; success audit is atomic with deletion.')

_page_range=string(enum=['15m','30m','1h','6h','24h','7d','30d'])
def page_parameter(path,name,schema=None,multiple=False):
    operation=P.get(path,{}).get('get')
    if operation is None: return
    params=operation.setdefault('parameters',[])
    params[:]=[p for p in params if not (p.get('in')=='query' and p.get('name')==name)]
    parameter=v3_query(name,schema)
    if multiple:
        parameter['schema']=arr(schema or string())
        parameter['style']='form';parameter['explode']=True
    params.append(parameter)
for _path in ['/home','/logs','/traces','/audit']:
    page_parameter(_path,'range',_page_range)
for _path,_name,_enum in [('/logs','source',['elasticsearch','loki']),('/traces','source',['tempo','jaeger']),('/metrics/dashboard','source',['prometheus','victoriametrics']),('/services','tab',['all','mine','watched']),('/services','sort',['status','budget','alerts','name']),('/conversations','sort',['recent','title','tokens'])]:
    page_parameter(_path,_name,string(enum=_enum))
for _path,_names in [('/canvases',['participant','service','status']),('/conversations',['scope','status']),('/runbooks',['scope','owner']),('/postmortems',['service','cause']),('/members',['role','source']),('/audit',['action','result'])]:
    for _name in _names: page_parameter(_path,_name,multiple=True)
P['/logs']['get']['description'] += ' source is a compatible alias for provider; both must agree. range changes the actual upstream window (default 30m). Existing provider max-window limits apply and an excess returns 503 without shrinking the requested window; a window beyond the registered Loki lookback leaves counts and patterns null with unavailable.'
P['/traces']['get']['description'] += ' App traces default 1h; range changes the actual complete-trace query. Only Tempo has a connected complete trace provider; requested Jaeger returns 503 trace_source_not_connected. Agent traces have no external source and reject source.'
P['/metrics/dashboard']['get']['description'] += ' source=prometheus selects the implemented provider; victoriametrics returns 503 metrics_source_not_connected rather than querying Prometheus.'
P['/home']['get']['description'] += ' range changes the actual ingress window, 24 equal histogram buckets, activity cutoff and recent-conversation cutoff; current unresolved attention remains current state, independent of range.'
P['/canvases']['get']['description'] += ' Repeated participant matches recorded creator display names, service matches persisted service IDs, status matches public persisted states. Same-key values OR; dimensions AND; applied before counts and page.'
P['/conversations']['get']['description'] += ' Repeated scope matches the displayed scope label; repeated public statuses OR. Sorting is single-valued; title/recent/token usage are computed before pagination. Unknown token totals sort after known totals; no unknown usage is invented.'
P['/runbooks']['get']['description'] += ' Repeated owner accepts maintainer ID or current display name and scope uses the persisted public scope label; filters precede counts and pagination.'
P['/postmortems']['get']['description'] += ' service filters stable application IDs before aggregation/page; cause categories have no producer and requests containing cause return 503 cause_classification_unavailable.'
P['/members']['get']['description'] += ' Repeated role matches visible role IDs or current role display names; source uses feishu/ldap/invite/system. OR within each dimension, AND across dimensions; counts use the same filtered authorized accounts.'
P['/audit']['get']['description'] += ' Repeated action is exact persisted action code; result uses ok/pending/blocked/failed/unconfirmed/expired/needs_attention with the same canonical outcome mapping as the response (outcome unknown is unconfirmed, not failed; expired is expired, PA-206; lifecycle outcomes such as started, merged, enabled, stale_remote and needs_attention are classified by outcome and, for rollout steps, by action, PA-230); an unknown result filter value is 422; a row whose outcome cannot be classified has result null and unavailable.result. Time/action/result filters apply before cursor selection, counts and volume. range volume uses seven equal-duration buckets; default volume retains seven UTC calendar days.'
