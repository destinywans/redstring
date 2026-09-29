# Read-only service pages. An incomplete required field fails the whole request;
# configured metadata alone never becomes a healthy status or SLO measurement.
S['V3ServiceKpi'] = obj({'id':string(),'label':string(),'value':string(),
    'unit':string(),'sub':string(),'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    'series':arr({'type':'number'})},['id','label','value','series'])
S['V3ServiceBurn'] = obj({'window':string(),'value':string(),
    'tone':string(enum=['ok','warn','danger']),'pct':integer(minimum=0,maximum=100)},
    ['window','value','tone','pct'])
S['V3ServiceSummary'] = obj({**fieldnames('id name desc team oncall'),
    'tier':string(enum=['T0','T1','T2']),'status':string(enum=['ok','warn','danger']),
    'slo':obj({'target':string(),'budget_left':{'type':'number','minimum':0,'maximum':100}},['target','budget_left']),
    'release':obj(fieldnames('brand version when'),['brand','version','when']),
    'alerts':integer(minimum=0),'connectors':arr(string())},
    ['id','name','desc','tier','team','oncall','status','slo','release','alerts','connectors'])
S['V3ServiceFacet'] = obj({'key':string(),'title':string(),
    'items':arr(obj({'label':string(),'count':integer(minimum=0),'on':v3_bool()},['label','count','on']))},
    ['key','title','items'])
S['V3ServiceCatalog'] = obj({'kpis':arr(ref('V3ServiceKpi')),
    'items':arr(ref('V3ServiceSummary')),'facets':arr(ref('V3ServiceFacet'))},
    ['kpis','items','facets'])
S['V3ServiceDependency'] = obj({'brand':string(),'name':string(),
    'status':string(enum=['ok','warn','danger']),'meta':string()},['name','status','meta'])
S['V3ServiceDetail'] = obj({**fieldnames('id name team desc'),
    'tier':string(enum=['T0','T1','T2']),
    'env':string(enum=['prod','staging','test']),
    'status':string(enum=['ok','warn','danger']),
    'deeplinks':arr(obj(fieldnames('brand name url'),['brand','name','url'])),
    'ai':obj({'text':string(),'investigation':string(),'proposal':string(),'meta':string()},
             ['text','investigation','meta']),
    'signals':arr(ref('V3ServiceKpi')),
    'slo':obj({'availability':string(),'target':string(),
        'budget_left':{'type':'number','minimum':0,'maximum':100},
        'burn':arr(ref('V3ServiceBurn')),'note':string()},
        ['availability','target','budget_left','burn','note']),
    'changes':arr(obj({'brand':string(),'title':string(),'meta':string(),'tag':string()},
        ['brand','title','meta'])),
    'deps':obj({'upstream':arr(ref('V3ServiceDependency')),
        'downstream':arr(ref('V3ServiceDependency'))},['upstream','downstream']),
    'tabs':v3_map(integer(minimum=0))},
    ['id','name','tier','team','env','status','desc','deeplinks','ai','signals','slo','changes','deps','tabs'])
v3_route('get','/services','Read complete source-backed authorized service catalog',
    response=ref('V3ServiceCatalog'),
    query=[v3_query(x) for x in ['q','tier','team','status']],
    desc='Uses registered application/environment/team identity, reviewed Prometheus dashboard evidence, covered operational alerts, verified successful deployments and explicit connector targets. Status is derived from measured SLO burn and current critical alerts, not Kubernetes readiness. All required evidence must be present and current or the full request returns 503 service_view_incomplete. More than 50 authorized rows returns 422 result_limit; no partial counts or synthetic healthy state.')
v3_route('get','/services/{id}','Read one complete authorized service detail',
    response=ref('V3ServiceDetail'),
    desc='The opaque ID binds application and environment. Detail additionally requires authorized persisted investigation model text for the AI panel; changes come from successful deployments and dependencies from non-mock CMDB depends_on service relations. Unrepresentable required evidence returns 503 rather than placeholder data.')
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
    desc='Shows only bindings for authorized applications and environments; missing means no persisted binding, not a health check. A single-environment row includes an additive save snapshot with exact service/environment IDs, binding revision and persisted binding identities only for callers with current services view/change and connectors view/manage; unsupported or duplicate stored categories set editable=false. An all-environment aggregate has no save snapshot. A suggested code cell carries the exact candidate ID, review version, target identity and binding revision needed for accept. Pending suggestions are shown only if their explicit GitLab project target, activation evidence and binding revision remain current. A V3-only validator permits exactly that source-derived GitLab binding while passing every other binding to the unchanged platform validator. Activation is historical project-access evidence; a recent generic connector probe does not prove current project access. More than 1000 visible services returns 422 result_limit instead of an incomplete map.')
S['V3MappingSaveBinding'] = obj({
    'system_id':string(enum=['metrics','logs','traces','code','deploy','dash','workload']),
    'kind':string(), 'connector_id':string(), 'resource':string(),
}, ['system_id','kind','connector_id','resource'])
S['V3MappingReadBinding'] = obj({
    'system_id':string(description='Unknown stored kinds appear as an empty string and make the save snapshot non-editable.'),
    'kind':string(), 'connector_id':string(), 'resource':string(),
}, ['system_id','kind','connector_id','resource'])
S['V3MappingSaveItem'] = obj({
    'service_id':string(), 'service_environment_id':string(),
    'binding_revision':integer(minimum=1),
    'bindings':{'type':'array','maxItems':7,'items':ref('V3MappingSaveBinding')},
}, ['service_id','service_environment_id','binding_revision','bindings'])
S['V3MappingReadSave'] = obj({
    **S['V3MappingSaveItem']['properties'],
    'bindings':arr(ref('V3MappingReadBinding')),
    'editable':v3_bool(), 'unavailable_reason':string(),
}, ['service_id','service_environment_id','binding_revision','bindings','editable'])
# Add a reviewable save snapshot only to single-environment rows for callers
# with the write and connector grants. The existing display grid is unchanged.
S['V3ServiceMapping']['properties']['rows']['items']['properties']['save'] = ref('V3MappingReadSave')
S['V3MappingSaveRequest'] = obj({
    'environment_id':string(),
    'items':{'type':'array','minItems':1,'maxItems':50,'items':ref('V3MappingSaveItem')},
}, ['environment_id','items'])
S['V3MappingSaveResult'] = obj({
    'environment_id':string(),
    'items':arr(ref('V3MappingSaveItem')),
}, ['environment_id','items'])
v3_route('put','/service-mappings','Replace versioned service connector mappings',
    body=ref('V3MappingSaveRequest'),response=ref('V3MappingSaveResult'),
    desc='Requires one selected environment matching environment_id. Each of 1-50 distinct service and service-environment identities replaces its complete mapping set using a current binding_revision; stale or changed identities return 409. Changed items, their revision increments and audit are committed atomically; an identical item returns its current revision. Empty body, display-only cells and duplicate or unknown system IDs are rejected. A code GitLab binding may only be retained when identical to the persisted binding; new or changed GitLab mappings must use discover/accept. Other bindings must pass the configured runtime binding validator. Current services view/change and connectors view/manage grants and the active session are rechecked in the transaction. The response contains persisted binding identities and post-write revisions. The frozen frontend still sends an empty PUT and cannot use this route until it submits the versioned request.')
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
_dashboard_points = {'type':'array','items':{'type':'number','minimum':0},'minItems':36,'maxItems':36}
_dashboard_intensities = {'type':'array','items':{'type':'number','minimum':0,'maximum':1},'minItems':36,'maxItems':36}
_dashboard_labels = {'type':'array','items':string(),'minItems':36,'maxItems':36}
_dashboard_tone = string(enum=['neutral','accent','ok','warn','danger','info'])
S['V3MetricsKpi'] = obj({'id':string(enum=['rps','err','p99','wait','cpu']),
    'label':string(),'value':string(),'unit':string(),'delta':string(),
    'delta_good':v3_bool(),'tone':_dashboard_tone,'sub':string(),
    'series':_dashboard_points},['id','label','value','series'])
S['V3MetricsBurn'] = obj({'window':string(),
    'value':string(),'tone':_dashboard_tone,'pct':{'type':'number','minimum':0,'maximum':100}},
    ['window','value','tone','pct'])
S['V3MetricsDashboard'] = obj({
    'window':obj({'labels':_dashboard_labels,
        'ticks':{'type':'array','items':string(),'minItems':5,'maxItems':5},
        'release_at':integer(minimum=0,maximum=35),
        'rollback_at':integer(minimum=0,maximum=35),
        'incident':{'type':'array','items':integer(minimum=0,maximum=35),'minItems':2,'maxItems':2},
        'incident_label':string()},
        ['labels','ticks','release_at','rollback_at','incident','incident_label']),
    'kpis':{'type':'array','items':ref('V3MetricsKpi'),'minItems':5,'maxItems':5},
    'requests':obj({'ok':_dashboard_points,'client':_dashboard_points,
        'server':_dashboard_points},['ok','client','server']),
    'slo':obj({'remaining':{'type':'number','minimum':0,'maximum':100},'availability':string(),'delta':string(),
        'burn':{'type':'array','items':ref('V3MetricsBurn'),'minItems':3,'maxItems':3},
        'note':string()},['remaining','availability','delta','burn','note']),
    'latency':obj({'p50':_dashboard_points,'p95':_dashboard_points,
        'p99':_dashboard_points,
        'heat':{'type':'array','items':_dashboard_intensities,'minItems':8,'maxItems':8},
        'buckets':{'type':'array','items':string(),'minItems':8,'maxItems':8}},
        ['p50','p95','p99','heat','buckets']),
    'endpoints':{'type':'array','minItems':1,'maxItems':12,'items':obj({
        'label':string(),'value':{'type':'number','minimum':0},'tone':_dashboard_tone},['label','value'])},
    'instances':{'type':'array','minItems':1,'maxItems':12,'items':obj({
        'label':string(),'values':_dashboard_points,'tone':_dashboard_tone},
        ['label','values'])}},
    ['window','kpis','requests','slo','latency','endpoints','instances'])
v3_route('get','/metrics/dashboard','Read a complete scoped Prometheus metrics dashboard',
    query=[v3_query('service'),v3_query('range',string(enum=['1h'])),v3_query('align',string(enum=['true','false']))],
    response=ref('V3MetricsDashboard'),
    desc='Requires one selected environment, current services.view, environment connectors.view and incident events.view, exactly one persisted V3 Prometheus metric-resource binding, and an operator-reviewed exact-scope dashboard query. Distinct metric bindings return 409 ambiguous_binding. One bounded one-hour source read must cover the baseline plus 36 visible bin ends for every labeled panel. Request total must conserve 2xx/4xx/5xx and independently observed other statuses must be zero; SLO budget must agree with monthly availability and persisted request-based target; heat intensities must be normalized to 0..1. Empty, partial or conflicting data returns 503, never invented zeroes or trends. Release, rollback and incident markers require matching persisted success/event records within the window; absent evidence returns 503. These are independent timestamps, not proof that one rollback reversed the displayed release. Source, binding and authorization are rechecked after the external read and local marker reads. align requests an incident-anchored window; the frozen frontend currently does not send it and still hardcodes version, causal and analysis text that must be corrected before page acceptance.')
for _parameter in P['/metrics/dashboard']['get']['parameters']:
    if _parameter['name'] == 'service':
        _parameter['required'] = True
P['/metrics/dashboard']['get']['responses']['503'] = {
    'description':'Complete V3 dashboard unavailable from configured source, complete series or persisted markers',
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
    'services':arr(string()),'duration_ms':v3_num(),'spans':integer(),'errors':integer()},
    ['id','time','op','href','services','duration_ms','spans','errors'])
S['V3TraceFacet'] = obj({'key':string(),'label':string(),'items':arr(obj({
    'value':string(),'count':integer(minimum=0)},['value','count']))},['key','label','items'])
S['V3TraceList'] = obj({'scope':string(enum=['app','agent']),'total':integer(minimum=0),'took':string(),
    'colors':v3_map(string()),'facets':arr(ref('V3TraceFacet')),
    'views':arr(obj({'id':string(),'label':string(),'count':integer(minimum=0),'query':string()},
        ['id','label','query'])),
    'histogram':obj({'counts':arr(arr(integer(minimum=0))),'axis':arr(string())},['counts','axis']),
    'scatter':arr(obj({'x':v3_num(),'y':v3_num(),'error':v3_bool(),'id':string()},
        ['x','y','error'])),
    'items':arr(ref('V3TraceRow'))},
    ['scope','total','took','colors','facets','views','histogram','scatter','items'])
S['V3TraceSpan'] = obj({**fieldnames('id service op'),'depth':integer(),'start':v3_num(),'end':v3_num(),
    'status':string(enum=['ok','error']),'critical':v3_bool(),
    'attrs':arr(obj(fieldnames('k v'),['k','v'])),
    'events':arr(obj({'at':string(format='date-time'),**fieldnames('name text')},['at','name','text']))},
    ['id','depth','service','op','start','end','status'])
S['V3TraceGraphNode'] = obj({'id':string(),'x':v3_num(),'y':v3_num(),'sub':string(),
    'danger':v3_bool()},['id','x','y','sub'])
S['V3TraceGraphEdge'] = obj({'from':string(),'to':string(),'label':string(),
    'danger':v3_bool()},['from','to','label'])
S['V3TraceView'] = obj({**fieldnames('id trace_id op time version flame_note'),
    'duration_ms':v3_num(),'colors':v3_map(string()),'spans':arr(ref('V3TraceSpan')),
    'graph':obj({'nodes':arr(ref('V3TraceGraphNode')),
        'edges':arr(ref('V3TraceGraphEdge'))},['nodes','edges'])},
    ['id','trace_id','op','duration_ms','time','version','colors','spans','flame_note','graph'])
v3_route('get','/traces','Query scoped traces',
    response=ref('V3TraceList'),
    query=[v3_query(x) for x in ['scope','view','service','q']],
    desc='Requires persisted service/environment/owner-team Tempo binding and current traces.view. App scope exhausts a one-hour search by bounded window splitting; all 200 counts, facets, histogram and scatter refer to that full result set. At most 40 traces, 64 searches, 6 targets and 500 spans per trace; a reached cap returns 413 rather than a sampled 200. q accepts one bounded TraceQL span selector with simple comparisons and inner AND/OR, but no regex, pipeline or outer union. Complete counts require a server-attested fail-closed Tempo search configuration; otherwise 503. Agent scope has no complete provider and returns 503. Source and grants are rechecked before response.')
P['/traces']['get']['responses']['413'] = {
    'description':'Complete trace population exceeds the bounded query budget',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/traces/{id}','Read scoped trace',response=ref('V3TraceView'),
    query=[v3_query('service')],
    desc='Reads the stored Tempo trace without a time bound, requiring every returned resource to match an authorized persisted binding and exactly one closed parent tree. Projects only safe attributes and exception types; absent version is rendered as unknown, and unproven logs or links are omitted. The highlighted path follows the latest finishing descendant. A trace exceeding 500 spans returns 413; incomplete trees return 503. Source and grants are rechecked before response.')
P['/traces/{id}']['get']['responses']['413'] = {
    'description':'Trace exceeds the scoped span limit',
    'content':{'application/json':{'schema':ref('Error')}}}
