# Read-only service discovery and externally queried telemetry.
S['V3CatalogEvidenceState'] = obj({'state':string(enum=['unavailable']), 'reason':string()},['state'])
S['V3CatalogConfiguredValue'] = obj({'state':string(enum=['configured','unavailable']), 'value':string()},['state'])
S['V3ServiceMetadata'] = obj({**fieldnames('id key name description environment'),
    'complete_v3_dto':v3_bool(),
    **{name:ref('V3CatalogConfiguredValue') for name in ['team','tier','owner','oncall']},
    **{name:ref('V3CatalogEvidenceState') for name in ['status','health','release','alerts']},
    'slo':obj({'target_percent':ref('V3CatalogConfiguredValue'),
        'availability':ref('V3CatalogEvidenceState'),'budget_left':ref('V3CatalogEvidenceState')}),
    'connectors':arr(obj({'kind':string(),'resource':string(),'state':string(enum=['configured'])}))},
    ['id','key','name','description','environment','complete_v3_dto','team','tier','owner',
     'oncall','status','health','slo','release','alerts','connectors'])
v3_route('get','/services','Read authorized persisted service metadata',
    response=obj({'items':arr(ref('V3ServiceMetadata')),'total':integer(),
        'complete_v3_dto':v3_bool()},['items','total','complete_v3_dto']),
    query=[v3_query(x) for x in ['q','tier','team','status']],
    desc='Partial V3 DTO: live status/health, measured SLO, releases and alerts are explicitly unavailable. '
         'Configured targets and bindings are not live evidence. A status filter returns 422 until live status is connected. '
         'A bounded scan covers all authorized services before response; more than 1000 visible rows returns 422 result_limit rather than an incomplete total.')
v3_route('get','/services/{id}','Read one authorized service metadata record',
    response=ref('V3ServiceMetadata'),
    desc='Partial V3 DTO with explicit evidence gaps; id binds application and environment.')
S['V3ExternalTarget'] = obj({**fieldnames('id connector_id provider target_kind external_id environment_id')},
    ['id','connector_id','provider','target_kind','external_id','environment_id'])
S['V3ExternalTargetInput'] = obj({'connector_id':string(),
    'target_kind':string(enum=['project']), 'external_id':string()},
    ['connector_id','target_kind','external_id'])
v3_route('get','/services/{id}/external-targets','List explicitly bound external service targets',
    response=obj({'items':arr(ref('V3ExternalTarget'))},['items']),
    desc='Requires services.view for the persisted application or team and connectors.view for its environment. '
         'At most 100 targets per service and environment; older application binding JSON is not inferred or copied. No endpoint or credentials are returned.')
v3_route('post','/services/{id}/external-targets','Bind an exact external target to a service',
    body=ref('V3ExternalTargetInput'),response=ref('V3ExternalTarget'),status=201,
    desc='Requires services.change for the persisted application or team and connectors.manage for its environment. '
         'Currently accepts only a positive numeric GitLab project ID on a connector in the same environment. '
         'A target cannot be attributed to two services in one environment; a service may bind at most 100 targets per environment; writes are audited. '
         'This metadata binding does not grant the connector new credentials or authorize an external write.')
v3_route('delete','/services/{id}/external-targets/{bindingId}','Remove an exact external target binding',
    status=204,
    desc='Requires services.change for the persisted application or team and connectors.manage for its environment. '
         'Only the scoped binding is removed and the change is audited; no external target is deleted.')
S['V3DiscoveredService'] = obj({**fieldnames('id resource_id uid environment_id cluster namespace kind image owner_hint tier_hint owner_label_hint'),
    'replicas':{'type':['integer','null']},'matched':arr(string()),'missing':arr(string())},
    ['id','resource_id','uid','environment_id','cluster','namespace','kind','image',
     'owner_hint','tier_hint','replicas','matched','missing'])
v3_route('get','/services/discover','Discover scoped workloads',
    response=obj({'cluster':string(),'items':arr(ref('V3DiscoveredService')),
                  'registered':integer(),'ambiguous_workloads':integer()},
                 ['cluster','items','registered','ambiguous_workloads']),
    query=[v3_query('cluster')],
    desc='Returns current, present Kubernetes workload inventory after resources.view and services.view checks. The optional single cluster is filtered before the 2000-candidate scan bound; repeated cluster values are rejected. A 20-second request budget covers inventory, policy and namesake checks. resource_id is the stable CMDB registration identity; uid is the source Kubernetes UID. Candidates blocked by trusted cross-cluster namesakes or raw same-name identities in the registration scope are omitted and counted in ambiguous_workloads without exposing those identities. registered counts authorized services across selected environments, not within the displayed cluster. Owner and tier are evidence hints, replicas may be null, and matched/missing are currently empty. The frontend does not yet send cluster or resource_id and still displays fixed service metadata, so this is not the complete V3 page contract.')
S['V3ServiceRegistration'] = obj({'items':arr(obj({
    'id':string(), 'namespace':string(), 'resource_id':string(), 'tier':string(enum=['T0','T1','T2','T3']),
    'owner':string(), 'slo':string(), 'p99':string()},['id']))},['items'])
v3_route('post','/services','Register scoped service metadata',
    role='admin',body=ref('V3ServiceRegistration'),
    response=obj({'created':integer(minimum=1)},['created']),status=201,
    desc='Atomically registers 1-50 service profiles. Optional resource_id must be the stable CMDB identity from discovery and requires namespace. It is revalidated in the selected environment for presence, UID, freshness, namespace, unclaimed state and resources.view authorization under transaction locks. Without resource_id, a name/namespace must resolve uniquely or remain a manual-only registration; stale, claimed, mismatched or ambiguous inventory returns 409. Registration is audited.')
S['V3MappingCandidate'] = obj({
    'id':string(),'version':string(format='date-time'),'service_id':string(),
    'service':string(),'environment_id':string(),'system_id':string(enum=['code']),
    'connector_id':string(),'target_id':string(),'external_id':string(),
    'binding_revision':integer(minimum=0),'expires_at':string(format='date-time')},
    ['id','version','service_id','service','environment_id','system_id','connector_id',
     'target_id','external_id','binding_revision','expires_at'])
S['V3MappingCandidateSelection'] = obj({
    'id':string(),'version':string(format='date-time'),'service_id':string(),
    'environment_id':string(),'system_id':string(enum=['code']),
    'binding_revision':integer(minimum=0)},
    ['id','version','service_id','environment_id','system_id','binding_revision'])
S['V3MappingAcceptItem'] = obj({
    'candidate_id':string(),'service_id':string(),'environment_id':string(),
    'system_id':string(enum=['code']),'connector_id':string(),'target_id':string(),
    'external_id':string(),'binding_revision':integer(minimum=0),
    'binding':obj({'kind':string(enum=['gitlab']),'connector_id':string(),
        'resource':string()},['kind','connector_id','resource']),
    'status':string(enum=['accepted','already_accepted'])},
    ['candidate_id','service_id','environment_id','system_id','connector_id','target_id',
     'external_id','binding_revision','binding','status'])
S['V3MappingCell'] = obj({'state':string(enum=['ok','suggested','missing']),
    'value':string(),'brand':string(),'candidate':ref('V3MappingCandidate')},['state'])
S['V3ServiceMapping'] = obj({'systems':arr(v3_object('id brand label')),
    'rows':arr(obj({'service':string(),'cells':v3_map(ref('V3MappingCell'))},['service','cells'])),
    'suggestions':integer(minimum=0)},['systems','rows','suggestions'])
v3_route('get','/service-mappings','Read persisted service connector mappings',
    response=ref('V3ServiceMapping'),
    desc='Shows only bindings for authorized applications and environments; missing means no persisted binding, not a health check. A suggested code cell carries the exact candidate ID, review version, target identity and binding revision needed for accept. Pending suggestions are shown only if their explicit GitLab project target, activation evidence and binding revision remain current. A V3-only validator permits exactly that source-derived GitLab binding while passing every other binding to the unchanged platform validator. Activation is historical project-access evidence; a recent generic connector probe does not prove current project access. More than 1000 visible services returns 422 result_limit instead of an incomplete map.')
v3_route('put','/service-mappings','Reject unavailable full mapping save',
    status=501,response=ref('Error'),
    desc='The frozen page sends no request body. Suggested code cells expose review identity for the separate accept operation, but the full matrix still lacks an accepted all-system save request and revision contract. This registered route remains outside the V3 allowed-route table and returns 501 v3_not_implemented before its fallback handler; it writes no binding or audit. No accepted full-save success response is claimed.')
P['/service-mappings']['put']['responses']['501']['description'] = 'Full mapping save is unavailable in V3'
v3_route('post','/service-mappings/discover','Stage scoped service mapping candidates',
    response=obj({'suggestions':integer(minimum=0),
        'source_state':string(enum=['configured','not_configured']),
        'candidates':arr(ref('V3MappingCandidate'))},
        ['suggestions','source_state','candidates']),
    desc='In exactly one selected environment, stages reviewable code mapping candidates only from explicitly enrolled numeric GitLab project targets after current services view/change, connectors view/manage, post-bind project activation and recent connector checks. The exact Cookie session and active account are locked and rechecked before commit. Each candidate includes an opaque ID, monotonic review version, exact service/environment/connector/target identity, binding revision and expiry; no binding is changed. source_state=not_configured distinguishes an absent supported source from a configured source with zero candidates. A V3-only validator checks the exact source-derived GitLab binding and delegates other bindings to the unchanged platform validator. More than 500 visible unmapped source rows returns 422. Prometheus metric resources and other matrix systems have no safe mapping producer here; names, labels and deployments are never inferred. The frozen frontend does not refresh after discovery.')
v3_route('post','/service-mappings/accept','Accept current scoped service mapping candidates',
    body=obj({'candidates':{'type':'array','items':ref('V3MappingCandidateSelection'),
        'minItems':1,'maxItems':500}},['candidates']),
    response=obj({'accepted':integer(minimum=0),
        'items':arr(ref('V3MappingAcceptItem'))},['accepted','items']),
    desc='In exactly one selected environment, accepts only explicitly identified, reviewed GitLab code candidates. The request must match candidate ID, monotonic version, service/environment/system and expected binding revision; stale or changed review snapshots return 409 and old service:system cell requests are rejected. Transaction-local services view/change and connectors view/manage checks, exact source identity, activation, unique target, binding CAS, a final wall-clock expiry check, the current Cookie session and active account, and V3-only binding validation precede an atomic binding/candidate/audit commit. Each success item returns the persisted binding and post-write revision; a verified repeat may return status=already_accepted and accepted=0. The frozen frontend sends the old cell shape and marks success before response, so page integration remains pending.')
S['V3GitLabPipelineRecord'] = obj({**fieldnames('id provider service_id environment_id binding_id connector_id project_id status ref sha source created_at updated_at')},
    ['id','provider','service_id','environment_id','binding_id','connector_id','project_id','status','ref','sha','source','created_at','updated_at'])
S['V3GitLabPipelineRecord']['properties']['provider'] = string(enum=['gitlab'])
for timestamp in ('created_at','updated_at'):
    S['V3GitLabPipelineRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/records','Read a bounded page of verified GitLab pipeline records',
    query=[v3_query('service_id'),v3_query('binding_id'),v3_query('page',integer(minimum=1,maximum=1000))],
    response=obj({'items':arr(ref('V3GitLabPipelineRecord')),'page':integer(minimum=1),
        'page_size':integer(enum=[20]),'next_page':integer(minimum=0)},['items','page','page_size','next_page']),
    desc='Auxiliary read-only source evidence. Requires an explicitly bound numeric GitLab project in the selected environment, enabled list_pipelines tool, services.view for the persisted application or team, and connectors.view for its environment. Scope and grant are rechecked after the upstream fetch. Does not provide DORA KPIs, deployment proof, risk score, diff or alert correlation. Zero next_page means no upstream next-page header.')
for parameter in P['/changes/records']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
P['/changes/records']['get']['responses']['502'] = {
    'description':'GitLab pipeline source failed or returned an invalid bounded response',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3GitLabMergeRequestRecord'] = obj({**fieldnames(
    'iid provider service_id environment_id binding_id connector_id project_id state source_branch target_branch sha merge_commit_sha squash_commit_sha created_at updated_at'),
    'merged_at':{'type':['string','null'],'format':'date-time'}},
    ['iid','provider','service_id','environment_id','binding_id','connector_id','project_id',
     'state','source_branch','target_branch','sha','merge_commit_sha','squash_commit_sha',
     'created_at','updated_at','merged_at'])
S['V3GitLabMergeRequestRecord']['properties']['provider'] = string(enum=['gitlab'])
S['V3GitLabMergeRequestRecord']['properties']['state'] = string(enum=['opened','closed','locked','merged'])
for timestamp in ('created_at','updated_at'):
    S['V3GitLabMergeRequestRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/merge-requests','Read bound GitLab merge request metadata',
    query=[v3_query('service_id'),v3_query('binding_id'),
           v3_query('page',integer(minimum=1,maximum=1000)),
           v3_query('state',string(enum=['all','opened','closed','locked','merged'])),
           v3_query('target_branch')],
    response=obj({'items':arr(ref('V3GitLabMergeRequestRecord')),
                  'page':integer(minimum=1),'page_size':integer(enum=[20]),
                  'next_page':integer(minimum=0)},['items','page','page_size','next_page']),
    desc='Auxiliary read-only metadata from one explicitly bound numeric GitLab project in the selected environment. Requires services.view, connectors.view and the enabled list_merge_requests tool. Source and authorization are rechecked after the bounded upstream GET. Returns branches, state, commit identifiers and GitLab times only, not diff, deployment, risk or alert correlation. Invalid filters return 422, revoked or changed binding returns 404/409, unavailable source returns 503 and invalid upstream response returns 502.')
for parameter in P['/changes/merge-requests']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
P['/changes/merge-requests']['get']['responses']['502'] = {
    'description':'GitLab merge request source failed or returned an invalid bounded response',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3GitLabCommitRecord'] = obj({**fieldnames(
    'sha provider service_id environment_id binding_id connector_id project_id title authored_at committed_at'),
    'parent_shas':arr(string())},
    ['sha','provider','service_id','environment_id','binding_id','connector_id','project_id',
     'title','authored_at','committed_at','parent_shas'])
S['V3GitLabCommitRecord']['properties']['provider'] = string(enum=['gitlab'])
for timestamp in ('authored_at','committed_at'):
    S['V3GitLabCommitRecord']['properties'][timestamp] = string(format='date-time')
v3_route('get','/changes/commits/{sha}','Read bound GitLab commit metadata',
    query=[v3_query('service_id'),v3_query('binding_id')],
    response=ref('V3GitLabCommitRecord'),
    desc='Auxiliary read-only metadata for an exact 40- or 64-hex commit SHA from one explicitly bound numeric GitLab project. Requires services.view, connectors.view and the separate enabled get_commit tool; get_commit_diff never grants this read. Source and authorization are rechecked after the bounded upstream GET. Returns title, times and parent SHAs, not message body, diff, author email, deployment or risk. Invalid SHA/target returns 422, missing or revoked record returns 404, changed binding returns 409, unavailable source returns 503 and invalid upstream response returns 502.')
for parameter in P['/changes/commits/{sha}']['get']['parameters']:
    if parameter['name'] in ('service_id','binding_id'):
        parameter['required'] = True
    if parameter['name'] == 'sha':
        parameter['schema'] = string(pattern='^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$')
P['/changes/commits/{sha}']['get']['responses']['502'] = {
    'description':'GitLab commit source failed or returned invalid bounded metadata',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3MetricProbe'] = obj({'status':string(enum=['observed','no_samples','not_registered','not_configured','unavailable','truncated']),
    'series':integer(minimum=0),'samples':integer(minimum=0)},['status','series','samples'])
S['V3IncompleteMetrics'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'service':string(),'environment_id':string(),'available':v3_map(ref('V3MetricProbe')),
    'missing':arr(string())},['error','service','environment_id','available','missing'])
v3_route('get','/metrics/dashboard','Inspect scoped metric dashboard readiness',
    query=[v3_query('service'),v3_query('range',string(enum=['1h'])),v3_query('align',string(enum=['true','false']))],
    desc='Requires exactly one persisted service/environment Prometheus binding and current services.view. Ambiguous distinct bindings return 409 ambiguous_binding; excessive authorized scope returns 422 scope_limit. The currently registered queries cannot fill the required V3 MetricsDashboard; when scoped data is available, returns 503 dashboard_unavailable with observed query availability and missing sections. No KPI or SLO values are fabricated.')
P['/metrics/dashboard']['get']['responses'].pop('200')
P['/metrics/dashboard']['get']['responses']['503'] = {
    'description':'Complete V3 dashboard unavailable from registered sources',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteMetrics')]}}}}
S['V3RegisteredMetricWindow'] = obj({'start':string(format='date-time'),
    'end':string(format='date-time'),'step_seconds':integer(minimum=30,maximum=30)},
    ['start','end','step_seconds'])
S['V3RegisteredMetricSample'] = obj({'timestamp':v3_num(),'value':string()},
    ['timestamp','value'])
S['V3RegisteredMetricSamples'] = obj({'application_id':string(),
    'environment_id':string(),'query_id':string(enum=['request_rate','http_p95_seconds']),
    'source':string(enum=['prometheus']),'window':ref('V3RegisteredMetricWindow'),
    'captured_at':string(format='date-time'),'status':string(enum=['observed','no_samples']),
    'samples':arr(ref('V3RegisteredMetricSample'))},
    ['application_id','environment_id','query_id','source','window','captured_at','status','samples'])
S['V3RegisteredMetricSamples']['properties']['samples']['maxItems'] = 32
v3_route('get','/metrics/samples','Read bounded registered Prometheus metric samples',
    query=[v3_query('service'),v3_query('query_id',string(enum=['request_rate','http_p95_seconds']))],
    response=ref('V3RegisteredMetricSamples'),
    desc='Auxiliary read for one selected environment and a persisted service with exactly one explicit V3 Prometheus metric-resource target. Requires current services.view and connectors.view plus an enabled query_metrics tool. A visible service without connectors.view returns 403 before target binding lookup; a hidden or absent service has the same missing-resource response. The query ID maps to an operator-reviewed exact connector/application/environment/resource PromQL catalog entry; identifiers do not prove expression data scope. A bounded 15-minute, 30-second-step scalar range read rechecks binding and authorization after the provider returns and returns no samples after revocation. Returns at most 32 samples with no labels, raw expression, endpoint or credential. Missing source/catalog returns 503; ambiguous or changed binding returns 409; invalid request/environment returns 422. This does not complete the MetricsDashboard page.')
for _parameter in P['/metrics/samples']['get']['parameters']:
    if _parameter['name'] in ('service','query_id'):
        _parameter['required'] = True
for _status, _description in [('409','Metric binding is ambiguous or changed'),
    ('422','Invalid metric request or environment')]:
    P['/metrics/samples']['get']['responses'][_status] = {
        'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
S['V3LogLine'] = obj({**fieldnames('time service message trace'),
    'level':string(enum=['ERROR','WARN','INFO'])})
S['V3LogPattern'] = obj({'template':string(),'level':string(enum=['ERROR','WARN','INFO']),
    'count':string(),'trend':arr(integer())},['template','level','count','trend'])
S['V3IncompleteLogs'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'available':obj({'total':string(),'errors':string(),
        'histogram':arr(arr(integer())),'items_sample':arr(ref('V3LogLine'))},
        ['total','errors','histogram','items_sample']),
    'missing':arr(string())},['error','available','missing'])
v3_route('get','/logs','Search configured log provider',
    response=obj({'total':string(),'errors':string(),'histogram':arr(arr(integer())),
        'items':arr(ref('V3LogLine')),'patterns':arr(ref('V3LogPattern'))},
        ['total','errors','histogram','items','patterns']),
    query=[v3_query(x) for x in ['q','levels','service']] +
        [v3_query('provider',string(enum=['loki','elasticsearch']))],
    desc='Requires explicit persisted service/environment/owner-team binding and current logs.view. A single provider may be selected when more than one source is bound; arbitrary endpoints or unbound sources are never accepted. A complete 200 requires exact full-window counts and complete pattern trends from the configured source, including an exact zero-match window, or an explicitly empty levels selection with no upstream query. Elasticsearch accepts only a registered message_template.keyword field or its existing categorize_text path on one concrete index. Configured Loki patterns require trusted top-level JSON level and message_template fields and a declared complete historical lookback covering the requested start. Missing, truncated, approximate or unavailable aggregations return 503 rather than inferring patterns from a bounded line sample. Distinct authorized bindings return 409 ambiguous_binding; changed source or grant is rechecked before response. More than 3 targets or other excessive authorized scope returns 422 scope_limit.')
P['/logs']['get']['responses']['200']['description'] = 'Complete scoped LogSearch, including exact zero matches, or explicitly empty levels selection without an upstream query'
P['/logs']['get']['responses']['503'] = {
    'description':'Complete log patterns unavailable or configured provider failed',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteLogs')]}}}}
S['V3TraceRow'] = obj({**fieldnames('id time op href'),
    'services':arr(string()),'duration_ms':v3_num(),'spans':integer(),'errors':integer()})
S['V3TraceList'] = obj({'scope':string(enum=['app','agent']),'total':integer(),'took':string(),
    'colors':v3_map(string()),'facets':arr({'type':'object','additionalProperties':True}),
    'views':arr({'type':'object','additionalProperties':True}),
    'histogram':obj({'counts':arr(arr(integer())),'axis':arr(string())}),
    'scatter':arr(obj({'x':integer(),'y':v3_num(),'error':v3_bool(),'id':string()})),
    'items':arr(ref('V3TraceRow'))})
S['V3TraceView'] = obj({**fieldnames('id trace_id op time version flame_note'),
    'duration_ms':v3_num(),'colors':v3_map(string()),
    'spans':arr(obj({**fieldnames('id service op'),'depth':integer(),'start':v3_num(),'end':v3_num(),
        'status':string(enum=['ok','error']),
        'attrs':arr(obj(fieldnames('k v'),['k','v'])),
        'events':arr(obj({'at':string(format='date-time'),**fieldnames('name text')},['at','name']))})),
    'graph':obj({'nodes':arr({'type':'object','additionalProperties':True}),
        'edges':arr({'type':'object','additionalProperties':True})}),
    'partial':{'type':'boolean','const':True},'missing':arr(string())},
    ['partial','missing'])
S['V3IncompleteTraceList'] = obj({'error':obj({'code':string(),'message':string()},['code','message']),
    'scope':string(enum=['app']),'view':string(),'took':string(),
    'observed_sample':arr(ref('V3TraceRow')),'missing':arr(string())},
    ['error','scope','view','took','observed_sample','missing'])
v3_route('get','/traces','Query scoped traces',
    query=[v3_query(x) for x in ['scope','view','service']],
    desc='Requires explicit persisted service/environment/owner-team Tempo binding and current traces.view. A bounded trace sample cannot establish full totals, facets, histogram or scatter population, so returns 503 trace_aggregation_unavailable with observed_sample and missing sections even for an empty sample. Agent scope is not configured; TraceQL q returns 422. Distinct authorized bindings return 409 ambiguous_binding; more than 6 targets or other excessive authorized scope returns 422 scope_limit.')
P['/traces']['get']['responses'].pop('200')
P['/traces']['get']['responses']['503'] = {
    'description':'Complete trace aggregation unavailable or scoped provider failed',
    'content':{'application/json':{'schema':{'oneOf':[ref('Error'),ref('V3IncompleteTraceList')]}}}}
v3_route('get','/traces/{id}','Read scoped trace',response=ref('V3TraceView'),
    query=[v3_query('service')],
    desc='Requires explicit persisted service/environment/owner-team Tempo binding and current traces.view. Returns only scoped spans, partial=true and missing complete-trace, critical-path and correlation sections. The href from the list preserves an encoded service filter. A trace exceeding the bounded span limit returns 413 trace_too_large; distinct authorized bindings return 409 ambiguous_binding and excessive authorized scope returns 422 scope_limit.')
P['/traces/{id}']['get']['responses']['413'] = {
    'description':'Trace exceeds the scoped span limit',
    'content':{'application/json':{'schema':ref('Error')}}}
