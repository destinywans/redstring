# Read-only service pages. A value without trustworthy evidence is null and its
# field path appears in the item's `unavailable` map with a reason; it is never
# zero, healthy, "not on call" or "never released". Configured metadata alone
# never becomes a healthy status or SLO measurement.
v3_svc_null = lambda schema: {'anyOf':[schema,{'type':'null'}]}
S['V3UnknownReason'] = string(enum=['not_connected','not_configured','no_authorized_target',
    'not_generated','unavailable','stale'])
v3_svc_unavailable = lambda: {'type':'object','additionalProperties':ref('V3UnknownReason'),
    'description':'Field path (dot-separated) of each null value -> reason. Empty when every value is known.'}
v3_svc_tier = string(enum=['T0','T1','T2','T3'])
v3_svc_health = string(enum=['ok','warn','danger'])
S['V3ServiceKpi'] = obj({'id':string(),'label':string(),'value':string(),
    'unit':string(),'sub':string(),'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    # PA-44: null marks a bin without a value (no traffic for the error ratio,
    # a gap in a latency or saturation panel).
    'series':arr(v3_nullable({'type':'number'}))},['id','label','value','series'])
S['V3ServiceCatalogKpi'] = obj({'id':string(),'label':string(),
    'value':v3_svc_null(string()),
    'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    'series':arr({'type':'number'}),
    'unknown':integer(minimum=0,description='Services left out of value because this input is unknown'),
    'reason':ref('V3UnknownReason')},['id','label','value','series','unknown'])
S['V3ServiceCatalogKpi']['description'] = ('Counts only services whose input is known. When every service '
    'in the result has an unknown input, value is null and reason gives the most common reason.')
S['V3ServiceBurn'] = obj({'window':string(),'value':string(),
    'tone':string(enum=['ok','warn','danger']),'pct':integer(minimum=0,maximum=100)},
    ['window','value','tone','pct'])
S['V3ServiceSummary'] = obj({**fieldnames('id name desc'),
    'tier':v3_svc_null(v3_svc_tier),'team':v3_svc_null(string()),'oncall':v3_svc_null(string()),
    'status':v3_svc_null(v3_svc_health),
    'slo':obj({'target':v3_svc_null(string()),
        'budget_left':v3_svc_null({'type':'number','minimum':0,'maximum':100})},['target','budget_left']),
    'release':v3_svc_null(obj(fieldnames('brand version when'),['brand','version','when'])),
    'alerts':v3_svc_null(integer(minimum=0)),'connectors':v3_svc_null(arr(string())),
    'unavailable':v3_svc_unavailable()},
    ['id','name','desc','tier','team','oncall','status','slo','release','alerts','connectors','unavailable'])
S['V3ServiceFacet'] = obj({'key':string(),'title':string(),
    'items':arr(obj({'label':string(),'count':integer(minimum=0),'on':v3_bool()},['label','count','on']))},
    ['key','title','items'])
S['V3ServiceCatalog'] = obj({'kpis':arr(ref('V3ServiceCatalogKpi')),
    'items':arr(ref('V3ServiceSummary')),'facets':arr(ref('V3ServiceFacet'))},
    ['kpis','items','facets'])
S['V3ServiceDependency'] = obj({'brand':string(),'name':string(),
    'status':v3_svc_null(v3_svc_health),'meta':string(),'unavailable':v3_svc_unavailable()},
    ['name','status','meta','unavailable'])
S['V3ServiceDetail'] = obj({**fieldnames('id name desc'),
    'tier':v3_svc_null(v3_svc_tier),'team':v3_svc_null(string()),
    'env':string(description='Environment key of the selected V3 instance, e.g. prod, staging, test or dev'),
    'status':v3_svc_null(v3_svc_health),
    'deeplinks':arr(obj(fieldnames('brand name url'),['brand','name','url'])),
    'ai':v3_svc_null(obj({'text':string(),'investigation':string(),'proposal':string(),'meta':string(),
             'at':string(format='date-time',description='PA-83: the time of the investigation note in RFC 3339; text and meta no longer carry it')},
             ['text','investigation','meta','at'])),
    'signals':v3_svc_null(arr(ref('V3ServiceKpi'))),
    'slo':obj({'availability':v3_svc_null(string()),'target':v3_svc_null(string()),
        'budget_left':v3_svc_null({'type':'number','minimum':0,'maximum':100}),
        'burn':v3_svc_null(arr(ref('V3ServiceBurn'))),'note':string()},
        ['availability','target','budget_left','burn','note']),
    'changes':v3_svc_null(arr(obj({'brand':string(),'title':string(),'meta':string(),'tag':string(),
        'at':string(format='date-time',description='PA-83: the deployment time in RFC 3339; meta no longer carries it')},
        ['brand','title','meta','at']))),
    'deps':v3_svc_null(obj({'upstream':arr(ref('V3ServiceDependency')),
        'downstream':arr(ref('V3ServiceDependency'))},['upstream','downstream'])),
    'tabs':obj({k:v3_svc_null(integer(minimum=0)) for k in ('changes','deps','incidents')},
        ['changes','deps','incidents']),
    'unavailable':v3_svc_unavailable()},
    ['id','name','tier','team','env','status','desc','deeplinks','ai','signals','slo','changes','deps','tabs','unavailable'])
v3_route('get','/services','Read the authorized service catalog with explicit unknowns',
    response=ref('V3ServiceCatalog'),
    query=[v3_query(x) for x in ['q','tier','team','status']],
    desc='Uses registered application/environment/team identity, reviewed Prometheus dashboard evidence, covered operational alerts, verified successful deployments and explicit connector targets. Status is derived from measured SLO burn and current critical alerts, not Kubernetes readiness; a critical alert or a danger burn decides it alone, anything milder needs both inputs. Missing or ungranted evidence returns 200 with that field null and its path in the item unavailable map (not_connected, not_configured, no_authorized_target, not_generated, unavailable, stale); unknown is never zero, ok, empty or "not on call". KPIs and facets count known values only; a KPI with no known input is null with a reason. The status filter matches known statuses only and sorting puts unknown values last. More than 50 authorized rows returns 422 result_limit. Storage, policy or session failures return 503 service_view_unavailable; evidence that changes or is withdrawn between its read and the reply (metric source seal, alert coverage) returns 503 service_source_changed.')
v3_route('get','/services/{id}','Read one authorized service detail with explicit unknowns',
    response=ref('V3ServiceDetail'),
    desc='The opaque ID binds application and environment. The AI panel shows the authorized open investigation reply from the latest completed run within the last hour (older: stale; none: not_generated). Changes come from successful deployments; dependencies from non-mock CMDB depends_on service relations, and any relation that cannot be represented makes deps null rather than shorter. Each dependency carries its own status and unavailable map. Incident and dependency tab counts need events.view and resources.view; unknowns follow GET /services. 503 and 422 follow GET /services. The reviewed dashboard query may return a subset of panels (PA-41): status and slo.budget_left need slo_burn_1h and slo_month_availability_pct; slo.availability needs the latter; slo.burn lists only present windows (slo.burn.<1h|6h|3d>); signals lists only present signals (signals.<rps|err|p99|wait|cpu>; rps/err need requests_2xx/4xx/5xx/total). Each absent one is named in unavailable with not_configured; an empty list is null with slo.burn or signals. A missing or NaN sample is a gap (PA-44): it is null in a signal series, zero traffic is 0 with a null error ratio, and a value without a sample in the current bin is named with not_generated instead of failing the reply.')
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
         'At most 100 targets per service and environment; older application binding JSON is not inferred at read time. '
         'The PA-19 operator import may add reviewed log_stream, log_index, trace_service and metric_resource targets, which are listed too. No endpoint or credentials are returned.')
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
    # The GET save snapshot may be sent back as it is: these are ignored.
    'editable':{**v3_bool(),'description':'Accepted and ignored, so the GET save snapshot can be sent back as it is'},
    'unavailable_reason':{**string(),'description':'Accepted and ignored'},
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
# PA-19 P3 stage 2 steps 2 and 6 (the only mapping path since P4): the
# service mapping reads and writes the registry, and the editor's options for
# one service environment.
P['/service-mappings']['get']['description'] += (' The metrics, logs, traces and workload cells and save bindings come from '
    'v3_connector_targets (PA-19 P3 stage 2 step 2): connector_id is the real connector ID, kind the provider and resource the '
    'target external_id; code, deploy and dash keep their stored bindings, and stored telemetry or workload bindings are not '
    'shown. A save snapshot whose registry system has more than one target is editable=false with unavailable_reason '
    'ambiguous_targets. The snapshot binding_revision is the target revision of the service environment. Stored bindings of '
    'other systems are not checked against the legacy telemetry file or Kubernetes registrations (PA-19 P4).')
P['/service-mappings']['put']['description'] += (' binding_revision in the snapshot and in the request is the service '
    'environment target revision (migration 172, v3_service_target_revisions), '
    'raised by every target or stored-binding change, so a stale editor gets 409 stale_mapping. A changed metrics, logs, '
    'traces or workload binding replaces that system\'s single target and an omitted one removes it: the connector must be '
    'one of the environment whose provider serves the system and equals kind (422 connector_unavailable), resource must be '
    'a catalog resource of that connector for this service (422 resource_not_in_catalog), and a resource already mapped to '
    'another service is 409 target_owned_by_other_service; more than one target for a system is 409 ambiguous_targets. '
    'Each target change is audited as v3.connector.target_bound or '
    'v3.connector.target_unbound and the save as v3.service_mapping.saved with source registry. Code stays a stored binding '
    '(kept or removed; added only through candidate acceptance), deploy and dash stay read-only (422 system_not_supported), '
    'and the stored telemetry and workload bindings are kept as they are. The response returns the registry projection.')
S['V3MappingOptionCurrent'] = obj({
    'target_id':{'type':['string','null'],'description':'Registry target ID; null for a stored binding without a target'},
    'connector_id':string(),'resource_id':string()},
    ['target_id','connector_id','resource_id'])
S['V3MappingOptionConnector'] = obj({
    'connector_id':string(),'name':string(),'provider':string(),
    'ready':v3_bool(),
    'not_ready':{**arr(string(enum=['connector_disabled','tool_disabled','options_invalid','credential_missing','credential_not_approved'])),
        'description':'Why the connector cannot serve reads yet; empty when ready'},
    'resources':{**arr(string()),'description':'Resource IDs the catalog reviewed for this connector and service environment; for code, the GitLab project targets bound to the service'}},
    ['connector_id','name','provider','ready','not_ready','resources'])
S['V3MappingOptionSystem'] = obj({
    'system_id':string(enum=['metrics','logs','traces','code','deploy','dash','workload']),
    'editable':v3_bool(),
    'unavailable_reason':{'type':['string','null'],'enum':[None,'registry_not_enabled','registry_cutover','system_not_supported','ambiguous_targets','forbidden']},
    'current':v3_nullable(ref('V3MappingOptionCurrent')),
    'options':arr(ref('V3MappingOptionConnector'))},
    ['system_id','editable','unavailable_reason','current','options'])
v3_route('get','/services/{id}/external-targets/options','Read the service mapping editor options of one service',
    response=obj({'service_id':string(),'environment_id':string(),'systems':arr(ref('V3MappingOptionSystem'))},
        ['service_id','environment_id','systems']),
    desc='PA-19 P3 stage 2 step 2. id is the service ID (base64url of application ID, NUL, environment ID) in a selected '
         'environment. Requires services.view on the application or its owner team (404 otherwise) and connectors.view in '
         'the environment (403 otherwise). One entry per mapping system in mapping order: editable and unavailable_reason '
         '(system_not_supported for deploy and dash; ambiguous_targets for more than one target; forbidden without '
         'services.change and connectors.manage; registry_not_enabled and registry_cutover are no longer returned since '
         'PA-19 P4), the current target or stored binding, and '
         'the connectors of the environment that can serve the system with their readiness and the catalog resource IDs '
         'reviewed for this service. Read-only; readiness is configuration, not live data.')
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
    'reviewed_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: the review time in RFC 3339; meta no longer carries it'},
    'stages':arr(obj({'name':string(),
                      'jobs':arr(obj({'name':string(),
                                      'state':string(enum=['ok','running']),
                                      'brand':string()},['name','state']))},['name','jobs'])),
    'diff':obj(fieldnames('path minus plus'),['path','minus','plus']),
    'risk':obj(fieldnames('title meta text'),['title','meta','text']),
    'alert':obj(fieldnames('text incident'),['text','incident'])},
    ['id','title','meta','reviewed_at','brand','stages','diff','risk','alert'])
S['V3ChangesView'] = obj({
    'kpis':arr(ref('V3ChangeKpi')),
    'sources':arr(obj({'brand':string(),'name':string(),
                       'count':integer(minimum=0),'on':v3_bool()},
                      ['brand','name','count','on'])),
    'lanes':arr(ref('V3ChangeLane')),
    'axis_times':{**arr(string(format='date-time')),'description':'PA-83: the axis instants in RFC 3339'},
    'pipelines':{'type':'array','items':ref('V3ChangePipeline'),'minItems':1,'maxItems':1},
    'detail':ref('V3ChangeDetail')},
    ['kpis','sources','lanes','axis_times','pipelines','detail'])
# D11: GitLab source of the change view, one service and range (sources=gitlab).
_gl_user = v3_nullable(obj({'username':string()},['username']))
_gl_str = {'type':['string','null']}
_gl_int = {'type':['integer','null']}
_gl_ids = arr(integer())
S['V3ChangeAdjacentAlert'] = obj({'incident_id':string(),
    'offset_seconds':integer(description='Alert start minus the change time; negative when the alert came first'),
    'basis':string(enum=['time_adjacent'],description='Only time adjacency; never a causal claim')},
    ['incident_id','offset_seconds','basis'])
S['V3GitLabDeployment'] = obj({'id':string(description='gitlab:deployment:<project>:<deployment id>'),
    'deployment_id':integer(),'iid':integer(),
    'status':string(enum=['created','running','success','failed','canceled','skipped','blocked']),
    'environment':string(),'ref':string(),'sha':string(),'short_sha':string(),'pipeline_id':_gl_int,
    'job':v3_nullable(obj({'id':integer(),'name':string()},['id','name'])),'user':_gl_user,
    'created_at':string(format='date-time'),'finished_at':{**_gl_str,'format':'date-time'},'web_url':_gl_str,
    'merge_request_iid':{**_gl_int,'description':'The merged merge request whose merge, squash or fast-forward head commit is this deployment commit; null when none or more than one'},
    'kubernetes_revision':{**_gl_str,'description':'The cluster revision whose change cause names this deployment pipeline'},
    'adjacent_alerts':{**v3_nullable(arr(ref('V3ChangeAdjacentAlert'))),'description':'Alerts starting 15 minutes before to 60 minutes after the deployment took effect; null when the alerts are unknown'}},
    ['id','deployment_id','iid','status','environment','ref','sha','short_sha','pipeline_id','job','user','created_at',
     'finished_at','web_url','merge_request_iid','kubernetes_revision','adjacent_alerts'])
S['V3GitLabPipeline'] = obj({'id':string(description='gitlab:pipeline:<project>:<pipeline id>'),'pipeline_id':integer(),
    'status':string(),'ref':string(),'sha':string(),'short_sha':string(),'source':string(),
    'created_at':string(format='date-time'),'updated_at':string(format='date-time'),'web_url':_gl_str,
    'merge_request_iid':{**_gl_int,'description':'From a merge request ref, or the merged merge request that landed as this commit'},
    'deployment_ids':_gl_ids},
    ['id','pipeline_id','status','ref','sha','short_sha','source','created_at','updated_at','web_url','merge_request_iid','deployment_ids'])
S['V3GitLabMergeRequest'] = obj({'id':string(description='gitlab:mr:<project>:<iid>'),'iid':integer(),
    'title':string(maxLength=200,description='User-written GitLab text, control characters replaced'),
    'state':string(enum=['opened','closed','merged','locked']),'source_branch':string(),'target_branch':string(),
    'author':_gl_user,'merged_by':_gl_user,'created_at':string(format='date-time'),
    'merged_at':{**_gl_str,'format':'date-time'},'closed_at':{**_gl_str,'format':'date-time'},
    'sha':_gl_str,'merge_commit_sha':_gl_str,'squash_commit_sha':_gl_str,'web_url':_gl_str,
    'pipeline_ids':_gl_ids,'deployment_ids':_gl_ids},
    ['id','iid','title','state','source_branch','target_branch','author','merged_by','created_at','merged_at','closed_at',
     'sha','merge_commit_sha','squash_commit_sha','web_url','pipeline_ids','deployment_ids'])
S['V3GitLabChangesView'] = obj({'service_id':string(),'environment_id':string(),'source':string(enum=['gitlab']),
    'window':obj({'start_at':string(format='date-time'),'end_at':string(format='date-time')},['start_at','end_at']),
    'project':obj({**fieldnames('connector_id project_id path web_url deploy_environment'),'refs':arr(string())},
        ['connector_id','project_id','path','web_url','deploy_environment','refs']),
    'kpis':{**arr(ref('V3ChangeKpi')),'description':'deployments, failed_deployments, pipelines, merged_merge_requests; a list whose connector tool is off has no KPI'},
    'series_start_at':string(format='date-time'),'series_step_seconds':integer(enum=[3600,86400]),
    'deployments':v3_nullable(arr(ref('V3GitLabDeployment'))),
    'pipelines':v3_nullable(arr(ref('V3GitLabPipeline'))),
    'merge_requests':v3_nullable(arr(ref('V3GitLabMergeRequest'))),
    'alerts':v3_nullable(arr(obj({**fieldnames('incident_id title href'),'started_at':string(format='date-time'),
        'ended_at':{**_gl_str,'format':'date-time'}},['incident_id','title','started_at','ended_at','href']))),
    'kubernetes_revisions':v3_nullable(arr(obj({'revision':string(),'created_at':string(format='date-time'),
        'change_cause':_gl_str,'gitlab_pipeline_id':_gl_int},['revision','created_at','change_cause','gitlab_pipeline_id']))),
    'coverage':obj({'gitlab':string(enum=['complete']),'alerts':string(enum=['complete','unknown','not_authorized']),
        'kubernetes':string(enum=['history_limited','not_configured','no_authorized_target','unavailable'])},['gitlab','alerts','kubernetes']),
    'captured_at':string(format='date-time'),'unavailable':ref('V3Unavailable')},
    ['service_id','environment_id','source','window','project','kpis','series_start_at','series_step_seconds',
     'deployments','pipelines','merge_requests','alerts','kubernetes_revisions','coverage','captured_at','unavailable'])
v3_route('get','/changes','Read one native delivery run, or a service\'s GitLab changes',
    query=[v3_query('service_id'),v3_query('run_id'),
           v3_query('range',string(enum=['24h','7d'],default='24h')),
           v3_query('sources',string(enum=['native','gitlab'],default='native'))],
    response={'oneOf':[ref('V3ChangesView'),ref('V3GitLabChangesView')]},
    desc='sources=native (default) requires one selected environment, an exact base64url application-NUL-environment service_id, '
         'and one persisted native Argo Workflows delivery run_id. Current services.view, events.view, '
         'source identity and session are rechecked before returning. Only that run appears in pipelines and detail. '
         'The selected 24h or 7d window bounds persisted delivery runs, verified GitOps deployment events and '
         'ops_incidents from the exact reviewed Alertmanager source only when operator-attested complete_from covers the full window. '
         'KPIs are countable run and deployment facts, never inferred change failure rate, '
         'lead time, MTTR or alert causality. Complete diff, risk and alert text require separately reviewed evidence '
         'anchored to the exact run, snapshot and artifact digests, execution and incident; missing or changed review evidence, deployment '
         'verification or alert coverage returns 503 rather than invented values. '
         'sources=gitlab (D11) takes service_id and range without run_id and returns V3GitLabChangesView: the deployments, pipelines and '
         'merge requests of the service\'s confirmed GitLab code project read on demand within the reviewed catalog scope, with exact links '
         '(commit, pipeline, merge request number), the covered alerts of the window and the cluster revisions. It needs services.view on the '
         'service and connectors.view on the environment; alerts need events.view and a reviewed coverage, otherwise they are null with a reason. '
         'An alert next to a change is only time-adjacent. Not configured, credential or GitLab failures are 503, more than 100 items of a kind 413, '
         'a source change during the read 409; nothing partial is returned.')
for parameter in P['/changes']['get']['parameters']:
    if parameter['name'] == 'service_id':
        parameter['required'] = True
P['/changes']['get']['responses']['422']['description'] = 'Missing, repeated or invalid service_id, run_id, range or sources; run_id is required for native and refused for gitlab'
P['/changes']['get']['responses']['413'] = {
    'description':'Bounded change source exceeds 500 runs or deployment events or 200 incidents, or 100 GitLab items of a kind',
    'content':{'application/json':{'schema':ref('Error')}}}
P['/changes']['get']['responses']['409'] = {
    'description':'GitLab source changed during the read','content':{'application/json':{'schema':ref('Error')}}}
P['/changes']['get']['responses']['403'] = {
    'description':'GitLab source outside the caller\'s connector scope','content':{'application/json':{'schema':ref('Error')}}}
P['/changes']['get']['responses']['503']['description'] = 'Required delivery, GitOps, incident coverage or reviewed detail evidence unavailable or changed; GitLab source not configured, credential unavailable or GitLab unavailable'
S['V3GitLabPipelineDetail'] = obj({'id':string(),'pipeline_id':integer(),**fieldnames('status ref sha source'),'user':_gl_user,
    'created_at':string(format='date-time'),'started_at':{**_gl_str,'format':'date-time'},'finished_at':{**_gl_str,'format':'date-time'},
    'duration_seconds':{'type':['number','null']},'web_url':_gl_str,
    'stages':arr(obj({'name':string(),'status':string(),'jobs':arr(obj({'id':integer(),'name':string(),'status':string(),
        'manual':v3_bool(),'started_at':{**_gl_str,'format':'date-time'},'finished_at':{**_gl_str,'format':'date-time'},
        'duration_seconds':{'type':['number','null']},'web_url':_gl_str},
        ['id','name','status','manual','started_at','finished_at','duration_seconds','web_url']))},['name','status','jobs'])),
    'commit':v3_nullable(obj({'sha':string(),'title':string(maxLength=200),'authored_at':string(format='date-time'),
        'parent_ids':arr(string()),'stats':obj({'files':_gl_int,'additions':integer(minimum=0),'deletions':integer(minimum=0)},
        ['files','additions','deletions']),'paths':{**v3_nullable(arr(string())),'description':'At most 20 changed paths; never diff text'}},
        ['sha','title','authored_at','parent_ids','stats','paths'])),
    'merge_request_iid':_gl_int,
    'adjacent_alerts':{**v3_nullable(arr(ref('V3ChangeAdjacentAlert'))),'description':'Relative to the pipeline end (its last update while it runs)'},
    'unavailable':ref('V3Unavailable')},
    ['id','pipeline_id','status','ref','sha','source','user','created_at','started_at','finished_at','duration_seconds','web_url',
     'stages','commit','merge_request_iid','adjacent_alerts','unavailable'])
v3_route('get','/changes/pipelines/{pipeline_id}','Read one GitLab pipeline of a service',
    query=[v3_query('service_id')],
    response=ref('V3GitLabPipelineDetail'),
    desc='D11 auxiliary read: one pipeline of the service\'s confirmed GitLab code project on a reviewed branch or one of its merge requests, '
         'with jobs grouped by stage in pipeline order, the commit title, statistics and at most 20 changed paths (no message body or diff text) '
         'and the covered alerts adjacent to the pipeline end. Needs get_pipeline; get_commit and get_commit_diff each add their part, otherwise '
         'it is null with not_configured. Same authorization, errors and recheck as the GitLab change view; a pipeline outside the scope is 404.')
for parameter in P['/changes/pipelines/{pipeline_id}']['get']['parameters']:
    if parameter['name'] == 'service_id':
        parameter['required'] = True
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
# PA-44: a bin without a value is null (no traffic for the error ratio, a gap
# in a latency, saturation or heat panel); zero traffic is 0.
_dashboard_points = {'type':'array','items':v3_nullable({'type':'number','minimum':0}),'minItems':36,'maxItems':36}
_dashboard_intensities = {'type':'array','items':v3_nullable({'type':'number','minimum':0,'maximum':1}),'minItems':36,'maxItems':36}
_dashboard_tone = string(enum=['neutral','accent','ok','warn','danger','info'])
S['V3MetricsKpi'] = obj({'id':string(enum=['rps','err','p99','wait','cpu']),
    'label':string(),'value':string(),'unit':string(),'delta':string(),
    'delta_good':v3_bool(),'tone':_dashboard_tone,'sub':string(),
    'series':_dashboard_points},['id','label','value','series'])
S['V3MetricsBurn'] = obj({'window':string(),
    'value':string(),'tone':_dashboard_tone,'pct':{'type':'number','minimum':0,'maximum':100}},
    ['window','value','tone','pct'])
S['V3MetricsDashboard'] = obj({
    'window':obj({'label_times':{'type':'array','items':string(format='date-time'),'minItems':36,'maxItems':36,
            'description':'PA-83: the instant of each label in RFC 3339, for display in the viewer time zone'},
        'tick_times':{'type':'array','items':string(format='date-time'),'minItems':5,'maxItems':5,
            'description':'PA-83: the instant of each tick in RFC 3339'},
        'release_at':v3_nullable(integer(minimum=0,maximum=35)),
        'rollback_at':v3_nullable(integer(minimum=0,maximum=35)),
        'incident':v3_nullable({'type':'array','items':integer(minimum=0,maximum=35),'minItems':2,'maxItems':2}),
        'incident_start_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: start of the incident marker, null without one'},
        'incident_end_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: end of the incident marker (the window end while it is open), null without one'}},
        ['label_times','tick_times','release_at','rollback_at','incident','incident_start_at','incident_end_at']),
    # PA-41: lists keep only the entries whose panels are present.
    'kpis':v3_nullable({'type':'array','items':ref('V3MetricsKpi'),'minItems':1,'maxItems':5}),
    'requests':v3_nullable(obj({'ok':_dashboard_points,'client':_dashboard_points,
        'server':_dashboard_points},['ok','client','server'])),
    'slo':v3_nullable(obj({'remaining':{'type':'number','minimum':0,'maximum':100},'availability':string(),'delta':string(),
        'burn':{'type':'array','items':ref('V3MetricsBurn'),'maxItems':3},
        'note':string()},['remaining','availability','delta','burn','note'])),
    'latency':v3_nullable(obj({'p50':_dashboard_points,'p95':_dashboard_points,
        'p99':_dashboard_points,
        'heat':v3_nullable({'type':'array','items':_dashboard_intensities,'minItems':8,'maxItems':8}),
        'buckets':v3_nullable({'type':'array','items':string(),'minItems':8,'maxItems':8})},
        ['p50','p95','p99','heat','buckets'])),
    'endpoints':v3_nullable({'type':'array','minItems':1,'maxItems':12,'items':obj({
        'label':string(),'value':{'type':'number','minimum':0},'tone':_dashboard_tone},['label','value'])}),
    'instances':v3_nullable({'type':'array','minItems':1,'maxItems':12,'items':obj({
        'label':string(),'values':_dashboard_points,'tone':_dashboard_tone},
        ['label','values'])}),
    'unavailable':ref('V3Unavailable')},
    ['window','kpis','requests','slo','latency','endpoints','instances','unavailable'])
v3_route('get','/metrics/dashboard','Read a scoped Prometheus metrics dashboard with explicit unknowns',
    query=[v3_query('service'),v3_query('range',string(enum=['1h'])),v3_query('align',string(enum=['true','false']))],
    response=ref('V3MetricsDashboard'),
    desc='Requires one selected environment and current services.view on the persisted service; a hidden or absent service is 403. Without environment connectors.view the metric blocks are null with unavailable=no_authorized_target and no source is looked up; without incident events.view the markers are null with no_authorized_target and align returns 403 (PA-09: a visible service is not a page-level 403). The three grants are rechecked as a set before responding; a change returns 409 metric_source_changed. The metric blocks need exactly one persisted V3 Prometheus metric-resource binding on an enabled connector with the query_metrics tool, and an operator-reviewed exact-scope dashboard query (AIOPS_V3_PROMETHEUS_QUERIES_CONFIG). Without that source or query the response is 200 with kpis, requests, latency, endpoints, instances and slo null and unavailable=not_configured; the legacy CMDB telemetry binding is not read. Distinct metric bindings return 409 ambiguous_binding; a failing provider is still 503. One bounded one-hour source read returns 36 visible bins per panel; a missing or NaN sample is a gap (PA-44): its bin is null in the series, zero traffic is 0 with a null error ratio, and a KPI, endpoint or SLO value without a sample in the current bin is omitted or null with not_generated. The reviewed query may return any subset of the known panels (PA-41): requests and the rps/err KPIs need requests_2xx/4xx/5xx/total (classes must sum to the total; requests_other must be zero when present); latency needs p50/p95/p99 (latency.heat and buckets also need all eight latency_heat rows); each other KPI, endpoints and instances need their own panel; the SLO block needs slo_month_availability_pct and a persisted target strictly between 0 and 100, and lists only the present burn windows. A block, KPI or burn window whose panels are absent is null or omitted and named in unavailable with not_configured (kpis.<id>, slo.burn.<1h|6h|3d>, latency.heat); nothing absent becomes zero. A malformed series, unknown panel, partial heat map or contradiction between present panels leaves every metric block null with unavailable; budget or burn panels that contradict the requests leave only slo null with unavailable. Release, rollback and incident markers come from matching persisted success/event records within the window; a marker without a record is null with not_generated (the platform recorded no such event; external releases are not connected). These are independent timestamps, not proof that one rollback reversed the displayed release. Source, binding, SLO target, authorization and the Cookie session are rechecked after the external read and local marker reads, including for a not-configured response. align requests an incident-anchored window and returns 503 when no incident can anchor it.')
for _parameter in P['/metrics/dashboard']['get']['parameters']:
    if _parameter['name'] == 'service':
        _parameter['required'] = True
P['/metrics/dashboard']['get']['responses']['503'] = {
    'description':'Configured metric provider, source lookup, SLO target read or persisted marker read failed',
    'content':{'application/json':{'schema':ref('Error')}}}
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
v3_route('get','/logs','Search configured log provider',
    response=obj({'total':v3_nullable(string()),'errors':v3_nullable(string()),'histogram':v3_nullable(arr(arr(integer()))),
        'items':v3_nullable(arr(ref('V3LogLine'))),'patterns':v3_nullable(arr(ref('V3LogPattern'))),
        'unavailable':ref('V3Unavailable')},
        ['total','errors','histogram','items','patterns','unavailable']),
    query=[v3_query(x) for x in ['q','levels','service']] +
        [v3_query('provider',string(enum=['loki','elasticsearch']))],
    desc='Requires explicit persisted service/environment/owner-team binding and current logs.view. A single provider may be selected when more than one source is bound; arbitrary endpoints or unbound sources are never accepted. items are the bounded, level-checked lines (at most 100, newest first) from every selected source and level. total, errors and histogram are exact full-window counts only when every selected source proved them; patterns only when complete pattern counts and trends account for that total. Otherwise each is null and unavailable names it: not_configured when a Loki application has no registered loki_complete_lookback_seconds (counts) or loki_pattern_field (patterns), unavailable when a registered source did not prove them; items stay a sample either way. Loki counts need a declared lookback covering the requested start; Loki patterns also need trusted top-level JSON level and message_template fields. Elasticsearch accepts only a registered message_template.keyword field or its existing categorize_text path on one concrete index. No log binding for the scope returns 200 with every field null and not_configured. A failing source query is 503 telemetry_unavailable. Distinct authorized bindings return 409 ambiguous_binding; changed source or grant is rechecked before response. More than 3 targets or other excessive authorized scope returns 422 scope_limit. The targets are registered log_stream/log_index targets (PA-19 P3 stage 2 step 3; the only source since P4) read through their own connector and Vault credential: legacy bindings are not read (no registered target is the not_configured answer), two registered log targets of one service are 409 ambiguous_binding whichever provider is selected, a registered source that is not ready is 503 source_not_ready with the reason codes in the message (connector_disabled, tool_disabled, options_invalid, credential_missing, credential_not_approved, scope_missing), and an unreadable Vault credential is 503 source_unavailable.')
P['/logs']['get']['responses']['200']['description'] = 'Scoped LogSearch with proven lines; counts and patterns are null with reasons when unproven, and every field is null when no source is bound'
P['/logs']['get']['responses']['503'] = {
    'description':'A configured log provider failed, a selected binding references an unconfigured provider, or (registry reader) the registered source is not ready (source_not_ready) or its credential is unavailable (source_unavailable)',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3TraceRow'] = obj({**fieldnames('id time op href'),
    'services':arr(string()),'duration_ms':v3_num(),'spans':integer(),'errors':integer(),
    **fieldnames('agent agent_id model tokens application_id environment_id'),
    'status':string(enum=['success','failed','blocked']),'missing':arr(string())},
    ['id','time','op','href','services','duration_ms','spans','errors'])
S['V3TraceFacet'] = obj({'key':string(),'label':string(),'items':arr(obj({
    'value':string(),'count':integer(minimum=0)},['value','count']))},['key','label','items'])
S['V3TraceList'] = obj({'scope':string(enum=['app','agent']),'total':integer(minimum=0),'took':string(),
    'colors':v3_map(string()),'facets':arr(ref('V3TraceFacet')),
    'views':arr(obj({'id':string(),'label':string(),'count':integer(minimum=0),'query':string()},
        ['id','label','query'])),
    'histogram':obj({'counts':arr(arr(integer(minimum=0))),
        'axis_times':{**arr(string(format='date-time')),'description':'PA-83: bucket starts in RFC 3339'}},['counts','axis_times']),
    'scatter':arr(obj({'x':v3_num(),'y':v3_num(),'error':v3_bool(),'id':string()},
        ['x','y','error'])),
    'items':arr(ref('V3TraceRow')),
    'source':string(),'aggregation_scope':string(),'window_start_at':string(format='date-time'),
    'window_end_at':string(format='date-time'),'excluded_running':integer(minimum=0),
    'partial':v3_bool(),'missing':arr(string()),'histogram_non_success_states':arr(string()),
    'partial_reason':string(enum=['search_budget'],description='App scope: the search stopped at its budget; every figure refers to [window_start_at, window_end_at)'),
    'unavailable':ref('V3Unavailable')},
    ['scope','total','took','colors','facets','views','histogram','scatter','items','unavailable'])
# App scope without a completeness attestation (PA-05): closed traces only.
S['V3ObservedTraceList'] = obj({'scope':string(enum=['app']),'total':{'type':'null'},'took':string(),
    'colors':v3_map(string()),'facets':{'type':'null'},
    'views':arr(obj({'id':string(),'label':string(),'count':{'type':'null'},'query':string()},
        ['id','label','count','query'])),
    'histogram':{'type':'null'},'scatter':{'type':'null'},
    'items':v3_nullable(arr(ref('V3TraceRow'))),'partial':{'type':'boolean','const':True},
    'partial_reason':string(enum=['search_budget','search_unattested','incomplete_traces','not_configured'],
        description='search_budget: the search stopped at its budget and items cover [window_start_at, window_end_at) only, '
            'whether or not incomplete traces were also left out (D30); search_unattested: search completeness is not attested; '
            'incomplete_traces: completeness is attested and the search covered the range, but found traces were left out as '
            'incomplete trees (excluded_incomplete, PA-87); not_configured: no Tempo source'),
    'excluded_incomplete':integer(minimum=1,description='PA-87: found traces left out because Tempo returned an incomplete tree '
        '(a missing parent span, not exactly one root, a cycle), for example after a span was lost in transit; absent when none'),
    'window_start_at':string(format='date-time'),'window_end_at':string(format='date-time'),
    'unavailable':ref('V3Unavailable')},
    ['scope','total','took','colors','facets','views','histogram','scatter','items','partial','partial_reason','unavailable'])
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
    response={'oneOf':[ref('V3TraceList'),ref('V3ObservedTraceList')]},
    query=[v3_query(x) for x in ['scope','view','service','q']],
    desc='Requires persisted service/environment/owner-team Tempo binding and current traces.view. App scope exhausts a one-hour search by bounded window splitting and reads every found trace without a time bound as one closed parent tree. At most 40 traces, 64 searches, 6 targets and 500 spans per trace; a reached cap returns 413 rather than a sampled 200. q accepts one bounded TraceQL span selector with simple comparisons and inner AND/OR, but no regex, pipeline or outer union. Only a server-attested fail-closed Tempo search configuration (the tempo_search_fail_closed connector list of the source catalog since PA-19 P4, for every searched connector) makes the found set a complete population: then the TraceList total, view counts, facets, histogram and scatter all refer to it and unavailable is empty. Without that attestation the response is V3ObservedTraceList: the closed traces found, partial=true, and total, views[].count, facets, histogram and scatter null with unavailable=not_configured; no count is inferred from them. With no Tempo binding for an unfiltered scope every field including items is null with not_configured; a service without an accessible source is 404. A failing Tempo source is 503. A found trace that Tempo returns as an incomplete tree (a missing parent span, not exactly one root, a cycle) no longer fails the list: it is left out and counted in excluded_incomplete, and with an attested search the population figures are withdrawn: partial_reason stays search_budget when the search stopped at its budget (window_start_at as usual) and is incomplete_traces when it covered the range (PA-87, D30); its own detail is still 503. Every 503 is logged with its cause. Agent scope instead reads the complete bounded set of currently authorized persisted terminal runs started in the database-clock last 24 hours, requiring both agents.view and traces.view, with an empty unavailable map. Current application and owner-team grant boundaries, Agent catalog relationships and Cookie session are checked in one transaction; up to 2000 qualifying runs and 20000 run/model identities, otherwise 413 without sampled counts. Running runs are excluded explicitly rather than assigned fabricated final duration. Optional service matches persisted app ID or key. Agent q accepts at most eight agent/model/status/version/trigger equality terms and duration comparisons joined by AND; unknown grammar returns 422. Token/model/cost or instrumentation coverage with no trusted report remain absent and listed as missing; span/error counts mean persisted reported rows only. Agent rows link to the separately authorized Agent run detail, not the Tempo detail route. Sources and grants are rechecked before response. PA-78: app-scope searches run newest-first in windows that every selected service answers below the search limit; when 40 traces or 64 searches are reached the search stops and returns 200 for the most recent complete part, with partial=true, partial_reason=search_budget and window_start_at later than the requested start (figures of an attested list then refer to [window_start_at, window_end_at)); 413 trace_scope_limit remains only when not even the newest second fits (20 or more traces in it) or a trace exceeds 500 spans. Every app-scope list carries window_start_at and window_end_at; an unattested list without truncation has partial_reason=search_unattested. PA-79: range is the outer bound of the search; each Tempo request covers at most max_window_seconds (1 h by default), newest first, and those requests count against the same 64-search budget, so a long range with little traffic can still stop at search_budget (with three services 64 searches cover 21 one-hour windows). A range longer than max_lookback_seconds (7 days by default) is 422 range_exceeds_lookback with max_lookback_seconds and max_range, the longest range value within it. PA-80: view=errors adds { status = error } and view=slow { trace:duration >= 1s } (a trace of at least one second) to the search, so a view covers the whole window; with q both selectors must match (a trace can satisfy them with different spans), and the grammar and 8 KiB rules apply as before. Rows are checked against the same rules again. An attested list searched with a view counts only that view (the other views have no count); unattested view counts stay null. TraceQL search is a GET request: a search URI over 8 KiB is refused before it is sent with 422 query_too_long (registered scope selectors are bounded so the longest accepted q fits). App scope reads registered trace_service targets (PA-19 P3 stage 2 step 4; the legacy tempo bindings are not read since P4): the targets of one connector are read together through its client and Vault credential, several connectors one after another under the same 64-search budget with the 40 traces split evenly, each searching only the window the previous ones covered, and the population is complete only when every searched connector is attested; a trace found by two registered Tempos is 503. Legacy bindings are not read; two registered trace targets of one service or overlapping catalog selectors fail closed (409 ambiguous_binding, 503 not_configured), an unready source is 503 source_not_ready with the reason codes in the message, and an unreadable credential is 503 source_unavailable.')
# PA-79: a range longer than the trace lookback names the limit.
S['V3TraceRangeError'] = obj({'error':obj({**fieldnames('code message'),
    'max_lookback_seconds':integer(minimum=1,description='range_exceeds_lookback: the configured trace lookback'),
    'max_range':string(description='range_exceeds_lookback: the longest range value within the lookback, empty when none')},
    ['code','message'])},['error'])
P['/traces']['get']['responses']['422'] = {
    'description':'Invalid view, range, source, scope or query; query_too_long; range_exceeds_lookback with the limit',
    'content':{'application/json':{'schema':ref('V3TraceRangeError')}}}
P['/traces']['get']['responses']['413'] = {
    'description':'Complete trace population exceeds the bounded query budget',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/traces/{id}','Read scoped trace',response=ref('V3TraceView'),
    query=[v3_query('service')],
    desc='Reads the stored Tempo trace without a time bound, requiring every returned resource to match an authorized persisted binding and exactly one closed parent tree. Projects only safe attributes and exception types; absent version is rendered as unknown, and unproven logs or links are omitted. The highlighted path follows the latest finishing descendant. A trace exceeding 500 spans returns 413; incomplete trees return 503. Source and grants are rechecked before response. Every registered Tempo connector with a selected target is asked for the trace; one that cannot show it within its authorized registrations does not have it, the trace must be found in exactly one, and the other registry failures are those of the list.')
P['/traces/{id}']['get']['responses']['413'] = {
    'description':'Trace exceeds the scoped span limit',
    'content':{'application/json':{'schema':ref('Error')}}}

# A personal watch is persisted independently of ownership and notification
# subscriptions. Status is nullable until this specific service has current
# authorized metrics and complete reviewed alert coverage.
S['V3WatchedServiceAvailability'] = obj({
    'state':string(enum=['available','unavailable']),
    'reason':string()},['state'])
S['V3WatchedService'] = obj({
    'id':string(),'name':string(),
    'status':{'type':['string','null'],'enum':['ok','warn','danger',None]},
    'availability':ref('V3WatchedServiceAvailability')},
    ['id','name','status','availability'])
S['V3WatchedServices'] = obj({
    'items':arr(ref('V3WatchedService')),
    'complete_v3_dto':v3_bool()},['items','complete_v3_dto'])
v3_route('get','/me/watched-services','Read explicitly watched services',
    response=ref('V3WatchedServices'),
    desc='Lists at most 50 currently visible personal watch records after examining at most 500 valid records in selected authorized environments; all may aggregate authorized non-production environments. Service IDs are the same opaque application/environment IDs as /services/{id}. Current services.view for the application or persisted owner team controls visibility. Current scoped SLO-burn and reviewed alert evidence supply status only when complete and authorized; otherwise status is null, availability.state is unavailable and complete_v3_dto is false. No owner, team, on-call or notification relationship creates a watch. After remote evidence reads, the exact Cookie session, environment and service grants, watch membership and metric source rows are rechecked and held through response. Alert coverage and token readability are rechecked just before the final transaction but cannot be atomically locked with its response. Frozen Shell.tsx requires a non-null status and needs an unknown-state adaptation before real-mode acceptance.')
v3_route('put','/me/watched-services/{id}','Follow one authorized service',
    response=obj({'id':string(),'watched':{'const':True}},['id','watched']),
    desc='Idempotently persists one personal watch for the selected single environment after current human session and services.view for the exact registered service or owner team. At most 50 currently visible watches count across authorized environments, with a separate 500-record storage bound. Hidden watches do not consume visible capacity; records in revoked environments still count toward the storage bound. No service state, owner assignment, notification subscription or external infrastructure is changed. The preference and audit are committed together.')
v3_route('delete','/me/watched-services/{id}','Unfollow one authorized service',
    status=204,
    desc='Idempotently removes only the current human user personal watch with this exact application/environment ID in the selected currently authorized environment. It remains possible after service archival or loss of services.view, without exposing service metadata. If the environment grant itself is revoked, the V3 scope middleware cannot select it; such historical watches do not count toward current follow capacity. No other user or service state is changed. A real deletion and its audit are committed together.')
