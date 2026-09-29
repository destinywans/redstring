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
S['V3Resilience'] = obj({
    'synced':string(format='date-time'),
    'rollout':obj({**fieldnames('title status stable next source'),
        'steps':arr(obj({'weight':string(),'note':string(),
            'state':string(enum=['done','current','todo'])},['weight','note','state'])),
        'split':obj({'stable':{'type':'number','minimum':0,'maximum':100},
            'canary':{'type':'number','minimum':0,'maximum':100}},['stable','canary']),
        'analysis':arr(obj({'name':string(),'value':string(),'threshold':string(),
            'ok':v3_bool()},['name','value','threshold','ok'])),
        'stable_series':arr({'type':'number','minimum':0}),
        'canary_series':arr({'type':'number','minimum':0})},
        ['title','status','stable','steps','split','analysis','stable_series','canary_series','next','source']),
    'breakers':arr(obj({**fieldnames('brand upstream mechanism threshold ejected'),
        'state':string(enum=['closed','open','half_open','none'])},
        ['brand','upstream','mechanism','state','threshold','ejected'])),
    'breaker_timeline':arr(obj({'from':{'type':'number','minimum':0,'maximum':100},
        'to':{'type':'number','minimum':0,'maximum':100},
        'state':string(enum=['closed','open','half_open'])},['from','to','state'])),
    'breaker_note':string(),
    'limits':arr(obj({**fieldnames('brand route mechanism limit used'),
        'pct':{'type':'number','minimum':0,'maximum':100},
        'rejected':integer(minimum=0)},
        ['brand','route','mechanism','limit','used','pct','rejected'])),
    'limit_note':string(),
    'flags':arr(obj({**fieldnames('brand key desc action'),'on':v3_bool(),
        'tag':obj({'text':string(),'tone':string(enum=['neutral','accent','ok','warn','danger','info'])},['text','tone'])},
        ['brand','key','desc','on','tag','action'])),
    'hpa':obj({'min':integer(minimum=0),'max':integer(minimum=0),
        'current':integer(minimum=0),'desired':integer(minimum=0),
        'replicas':arr({'type':'number','minimum':0}),
        'desired_series':arr({'type':'number','minimum':0}),
        'cpu':arr({'type':'number','minimum':0}),
        'target':{'type':'number','minimum':0,'maximum':100},'note':string()},
        ['min','max','current','desired','replicas','desired_series','cpu','target','note']),
    'ai':arr(string())},
    ['synced','rollout','breakers','breaker_timeline','breaker_note','limits','limit_note','flags','hpa','ai'])
v3_route('get','/services/{id}/resilience','Read source-backed service resilience',
    response=ref('V3Resilience'),
    desc='The opaque service ID and selected environment must match. A complete current observation must cover rollout, breakers, rate limits, flags and HPA from authoritative sources for the exact application/environment. Missing, stale or mismatched fields return 503 resilience_source_unavailable; no zero or empty list represents unknown coverage. The domain route has no production reader until those scoped adapters are configured.')
def resilience_proposal(kind, desired):
    return obj({'kind':string(enum=[kind]),
        'params':obj({'investigation_id':string(minLength=1,maxLength=128),
            'reason':string(minLength=1,maxLength=500),'desired':desired},
            ['investigation_id','reason','desired'])},['kind','params'])
S['V3ResilienceProposalRequest'] = {'oneOf':[
    resilience_proposal('rollout.pause',
        obj({'rollout_id':string(minLength=1,maxLength=128)},['rollout_id'])),
    resilience_proposal('rollout.rollback',
        obj({'rollout_id':string(minLength=1,maxLength=128),
             'revision':string(minLength=1,maxLength=128)},['rollout_id','revision'])),
    resilience_proposal('breaker.threshold',
        obj({'breaker_id':string(minLength=1,maxLength=128),
             'threshold_percent':{'type':'number','exclusiveMinimum':0,'maximum':100}},
            ['breaker_id','threshold_percent'])),
    resilience_proposal('limit.threshold',
        obj({'route':string(minLength=1,maxLength=256),
             'requests_per_second':integer(minimum=1,maximum=1000000)},
            ['route','requests_per_second'])),
    resilience_proposal('flag.set',
        obj({'flag_key':string(minLength=1,maxLength=128),'enabled':v3_bool()},
            ['flag_key','enabled'])),
    resilience_proposal('hpa.bounds',
        obj({'hpa_id':string(minLength=1,maxLength=128),
             'min':integer(minimum=1,maximum=10000),
             'max':integer(minimum=1,maximum=10000)},['hpa_id','min','max']))]}
v3_route('post','/services/{id}/resilience/proposals','Create a scoped non-executable resilience review request',
    body=ref('V3ResilienceProposalRequest'),
    response=obj({'proposal_id':string()},['proposal_id']),status=201,
    desc='Requires an active human session, one selected environment, exact persisted service, services.view, proposals.change and canvas.view, and an open investigation for that service/environment. desired has a kind-specific bounded schema; see docs/api/v3-resilience.md. The immutable V3 review content records the human reason and requested values as unverified. It creates no v3_proposal_actions row, approval, merge request or external write. Repeated POST creates another review record.')
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
S['V3DiscoverySource'] = obj({'kind':string(enum=['kubernetes']),'id':string(),
    'observed_at':string(format='date-time'),'last_success_at':string(format='date-time')},
    ['kind','id','observed_at','last_success_at'])
S['V3DiscoveredService'] = obj({**fieldnames('id resource_id uid environment_id cluster namespace kind image owner_hint tier_hint owner_label_hint'),
    'replicas':{'type':['integer','null']},'matched':arr(string()),'missing':arr(string()),
    'source':ref('V3DiscoverySource'),'unavailable_fields':arr(string())},
    ['id','resource_id','uid','environment_id','cluster','namespace','kind','image',
     'owner_hint','tier_hint','replicas','matched','missing','source','unavailable_fields'])
v3_route('get','/services/discover','Discover scoped workloads',
    response=obj({'cluster':string(),'items':arr(ref('V3DiscoveredService')),
                  'registered':integer(),'ambiguous_workloads':integer()},
                 ['cluster','items','registered','ambiguous_workloads']),
    query=[v3_query('cluster')],
    desc='Returns fresh, present Kubernetes workload inventory after resources.view and services.view checks. The optional single cluster is filtered before the 2000-candidate scan bound; repeated cluster values are rejected. A 20-second request budget covers inventory, policy and namesake checks. resource_id is the stable CMDB registration identity; uid is the source Kubernetes UID. source identifies the collector and its observation/success timestamps; neither timestamp is a live readiness measurement. Candidates blocked by trusted cross-cluster namesakes or raw same-name identities in the registration scope are omitted and counted in ambiguous_workloads without exposing those identities. registered counts authorized services across selected environments, not within the displayed cluster. Owner and tier are hints, replicas means desired replicas and may be null, and unavailable_fields names fields without a verified source, including health, verified_tier, connector_coverage and runbook_gaps. matched/missing remain empty for compatibility and do not claim coverage. The frontend does not yet send cluster or resource_id and still displays fixed service metadata; page integration remains separate from this backend response.')
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
S['V3ChangeKpi'] = obj({
    'id':string(enum=['runs','succeeded','failed','deployments']),
    'label':string(), 'value':string(), 'unit':string(), 'delta':string(),
    'delta_good':v3_bool(), 'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    'sub':string(), 'series':arr(v3_num())},
    ['id','label','value','series'])
S['V3ChangePipeline'] = obj({
    **fieldnames('id brand ref service title trigger duration time'),
    'status':string(enum=['success','failed','running','canceled']),
    'stages':arr(string(enum=['ok','danger','accent','neutral']))},
    ['id','brand','ref','service','title','trigger','status','stages','duration','time'])
S['V3ChangeLane'] = obj({
    'service':string(),
    'marks':arr(obj({'at':v3_num(),'tone':string(enum=['ok','danger']),
                     'label':string()},['at','tone'])),
    'alerts':arr({'type':'array','items':v3_num(),'minItems':2,'maxItems':2})},
    ['service','marks','alerts'])
S['V3ChangeDetail'] = obj({
    **fieldnames('id title meta brand'),
    'stages':arr(obj({'name':string(),
                      'jobs':arr(obj({'name':string(),
                                      'state':string(enum=['ok','running']),
                                      'brand':string()},['name','state']))},['name','jobs'])),
    'diff':obj(fieldnames('path minus plus'),['path','minus','plus']),
    'risk':obj(fieldnames('title meta text'),['title','meta','text']),
    'alert':obj(fieldnames('text incident'),['text','incident'])},
    ['id','title','meta','brand','stages','diff','risk','alert'])
S['V3ChangesView'] = obj({
    'kpis':arr(ref('V3ChangeKpi')),
    'sources':arr(obj({'brand':string(),'name':string(),
                       'count':integer(minimum=0),'on':v3_bool()},
                      ['brand','name','count','on'])),
    'lanes':arr(ref('V3ChangeLane')),
    'axis':arr(string()),
    'pipelines':{'type':'array','items':ref('V3ChangePipeline'),'minItems':1,'maxItems':1},
    'detail':ref('V3ChangeDetail')},
    ['kpis','sources','lanes','axis','pipelines','detail'])
v3_route('get','/changes','Read one evidence-bound native delivery run',
    query=[v3_query('service_id'),v3_query('run_id'),
           v3_query('range',string(enum=['24h','7d'],default='24h')),
           v3_query('sources',string(enum=['native'],default='native'))],
    response=ref('V3ChangesView'),
    desc='Requires one selected environment, an exact base64url application-NUL-environment service_id, '
         'and one persisted native Argo Workflows delivery run_id. Current services.view, events.view, '
         'source identity and session are rechecked before returning. Only that run appears in pipelines and detail. '
         'The selected 24h or 7d window bounds persisted delivery runs, verified GitOps deployment events and '
         'ops_incidents from the exact reviewed Alertmanager source only when operator-attested complete_from covers the full window. '
         'KPIs are countable run and deployment facts, never inferred change failure rate, '
         'lead time, MTTR or alert causality. Complete diff, risk and alert text require separately reviewed evidence '
         'anchored to the exact run, snapshot and artifact digests, execution and incident; missing or changed review evidence, deployment '
         'verification or alert coverage returns 503 rather than invented values. The current frontend omits '
         'service_id and run_id and cannot receive a 200 until updated.')
for parameter in P['/changes']['get']['parameters']:
    if parameter['name'] in ('service_id','run_id'):
        parameter['required'] = True
P['/changes']['get']['responses']['422']['description'] = 'Missing, repeated or invalid service_id, run_id, range or sources'
P['/changes']['get']['responses']['413'] = {
    'description':'Bounded change source exceeds 500 runs or deployment events or 200 incidents',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/changes']['get']['responses']['503']['description'] = 'Required delivery, GitOps, incident coverage or reviewed detail evidence unavailable or changed'
S['V3ChangeReviewInput'] = obj({
    'service_id':string(),
    'decision':string(enum=['accept_for_display']),
    'expected_run_digest':string(pattern='^[0-9a-f]{64}$'),
    'expected_run_updated_at':string(format='date-time'),
    'execution_id':string(),
    'expected_plan_digest':string(),
    'expected_artifact_digest':string(pattern='^sha256:[0-9a-f]{64}$'),
    'expected_commit_sha':string(pattern='^(?:[0-9a-f]{40}|[0-9a-f]{64})$'),
    'incident_id':string(),
    'expected_incident_updated_at':string(format='date-time'),
    'expected_diff_digest':string(pattern='^[0-9a-f]{64}$',
        description='Lowercase SHA-256 of compact UTF-8 JSON diff with ordered path, minus, plus fields.'),
    'expected_previous_review_id':string(pattern='^[1-9][0-9]{0,18}$',description='Current latest accepted review ID for a new revision; omit for the first review.'),
    'diff':obj(fieldnames('path minus plus'),['path','minus','plus']),
    'risk':obj(fieldnames('title meta text'),['title','meta','text']),
    'alert_text':string(),
}, ['service_id','decision','expected_run_digest','expected_run_updated_at',
    'execution_id','expected_plan_digest','expected_artifact_digest','expected_commit_sha',
    'incident_id','expected_incident_updated_at','expected_diff_digest','diff','risk','alert_text'])
S['V3ChangeReviewResult'] = obj({
    'review_id':string(), 'run_id':string(),
    'reviewed_at':string(format='date-time'),
    'status':string(enum=['accepted']),
}, ['review_id','run_id','reviewed_at','status'])
v3_route('post','/changes/{run_id}/review','Publish a human-reviewed change detail assertion',
    body=ref('V3ChangeReviewInput'),response=ref('V3ChangeReviewResult'),status=201,
    desc='Requires one exact selected environment, a current human non-machine session, and services.view, '
         'events.view and services.change for the selected service. The service_id and run_id bind a persisted '
         'successful native delivery run. The server verifies the expected run version and digest, successful '
         'GitOps execution and deployment event, plan/artifact/commit identities, and incident version before '
         'atomically appending an immutable change.review.accepted audit record. A new revision requires '
         'expected_previous_review_id equal to the latest accepted review, preserving the audit history. '
         'Diff, risk and alert text are '
         'human assertions; expected_diff_digest binds the asserted diff bytes and expected_commit_sha binds '
         'a verified commit identity, but neither proves a source-derived diff or incident causality. First '
         'acceptance returns 201; exact same actor and payload retry returns 200 without another record. '
         'Conflicting accepted review or stale expected version returns 409. The GET /changes still requires '
         'independent alert source coverage.')
P['/changes/{run_id}/review']['post']['responses']['200'] = {
    'description':'Exact same actor and payload already accepted; no new audit record',
    'content':{'application/json':{'schema':ref('V3ChangeReviewResult')}}}
P['/changes/{run_id}/review']['post']['responses']['409']['description'] = 'Stale source version or conflicting accepted review'
P['/changes/{run_id}/review']['post']['responses']['404']['description'] = 'Run or selected service scope not visible'
P['/changes/{run_id}/review']['post']['responses']['503']['description'] = 'Required storage or source verification unavailable'
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
S['V3MetricsAskRequest'] = obj({
    'service':string(description='Canonical persisted application ID, not a display name or service key.'),
    'view':string(enum=['request_rate','http_p95_seconds']),
    'range':string(enum=['15m','1h']),
    'question':string(minLength=1,maxLength=4000),
    'request_id':string(minLength=16,maxLength=128,pattern='^[A-Za-z0-9_-]{16,128}$')},
    ['service','view','range','question','request_id'])
S['V3MetricsAskAccepted'] = obj({'conversation_id':string()},['conversation_id'])
v3_route('post','/metrics/ask','Ask a model about one verified metric snapshot',
    body=ref('V3MetricsAskRequest'),response=ref('V3MetricsAskAccepted'),status=201,
    desc='Requires one selected environment, live Cookie, conversation.change and conversation.view, services.view for the persisted application or owner team, connectors.view, exactly one enabled V3 Prometheus metric-resource target and an operator-reviewed exact-scope query. The service field must be its stable application ID. The server captures one successful bounded scalar range response (1 to 32 observed samples) for the selected view over 15m/30s or 1h/120s; samples may be sparse and do not prove continuous window coverage or trend. An empty or provider-partial result is unavailable. Metric values and provenance are stored as bounded private untrusted model context, not instructions or client-supplied evidence. Conversation, user turn, queued reply, source binding and audit commit atomically; the environment-bound model then runs under current grants. A per-owner request_id deduplicates identical concurrent/retried submissions; changed payload returns 409. Successful retry returns 200 with the original conversation_id, including when the metric provider is temporarily unavailable. SSE /conversations/{id}/events and detail recover the persisted reply; service/source revocation blocks subsequent model work and reads. This metric-backed conversation offers no generic model tools or external write access.')
P['/metrics/ask']['post']['responses']['200'] = {
    'description':'Previously accepted identical request; returns its original conversation ID',
    'content':{'application/json':{'schema':ref('V3MetricsAskAccepted')}}}
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
