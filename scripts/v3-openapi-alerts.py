# Alerts and incidents: document the real partial responses explicitly, while
# completion status remains tracked separately in the human contract.
S['V3InboxItem'] = obj({**fieldnames('id title sub time href alert_id'),
    'kind':string(enum=['alert','approval','mention','runbook','report','access','agent']),
    'tone':string(enum=['neutral','accent','ok','warn','danger','info']),
    'unread':v3_bool(),'day':string(enum=['today','yesterday'])})
S['V3IncidentSummary'] = obj({**fieldnames('id title duration commander'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'status':string(enum=['active','mitigated','resolved'])},
    ['id','severity','title','status','duration','commander'])
S['V3IncidentTimelineEntry'] = obj({'time':string(format='date-time'),
    'actor':obj({'kind':string(enum=['brand','ai','person']),'name':string()},['kind','name']),
    'text':string(),'sub':string()},['time','actor','text'])
v3_route('get','/inbox','Read scoped inbox',response=obj({'items':arr(ref('V3InboxItem')),
    'counts':v3_map(integer())}),query=[v3_query('filter',string(enum=['all','mine','mentions','subscribed'])),v3_query('tab')],
    desc='Currently includes recent incident-backed alerts; other inbox kinds are not yet connected. tab remains an alias for filter.')
S['V3AlertNotification'] = obj({
    'id':string(),'channel':string(),
    'status':string(enum=['pending','sending','retrying','failed','sent']),
    'created_at':string(format='date-time',description='Local enqueue time, not upstream delivery or human receipt time.'),
    'attempts':integer(minimum=0)},['id','channel','status','created_at','attempts'])
S['V3AlertDetail'] = obj({
    **fieldnames('id title source_id env summary assignee_id investigation_id revision'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'state':string(enum=['firing','pending','resolved']),
    'since':string(format='date-time'),
    **{name:{'type':'null'} for name in ['source','rule','triage','series','labels','runbook']},
    'notifications':{'type':'array','maxItems':500,'items':ref('V3AlertNotification')},
    'notifications_truncated':v3_bool(),
    'grouping':obj({'group':{'type':'null'},'inhibited':{'type':'null'},
        'incident':obj({'id':string()},['id']),'change':{'type':'null'}},
        ['group','inhibited','incident','change']),
    'unavailable':arr(string())},
    ['id','revision','severity','title','state','source_id','source','env','since',
     'rule','triage','series','labels','summary','runbook','notifications','notifications_truncated',
     'grouping','assignee_id','investigation_id','unavailable'])
v3_route('get','/alerts/{id}','Read evidence-backed persisted alert detail',
    response=ref('V3AlertDetail'),
    desc='Resolves a persisted ops_incidents ID in the selected V3 environment. Current session, mapped application, current owner team, and events.view are checked under transaction locks before returning. revision is the incident-row xmin action token, not a representation ETag: notification status can change independently. Notification rows report local enqueue time and transport status only; at most the latest 500 are returned, with notifications_truncated and unavailable when more exist. The nonempty source_id is the persisted ingress identity, not proof that its configuration remains active. Provider name, original alert labels, metric series and threshold, rule, triage, runbook, real notification group, inhibition and change association have no exact occurrence binding; these fields are null and named in unavailable. The persisted incident ID is the only established grouping link. Malformed severity/state or cross-scope notification records return 503, not a synthetic value. This is a backend nullable contract; the frozen AlertDetail page requires non-null fields and is not yet accepted.')
v3_route('post','/alerts/{id}/claim','Claim a persisted alert with an incident revision',role='operator',
    body=obj({'expected_revision':string(pattern='^[1-9][0-9]{0,9}$')},['expected_revision']),
    response=ref('V3AlertDetail'),
    desc='Requires the incident-row revision returned by GET; a missing or invalid token is 422, a stale token is 412. Locks the incident, current application/environment/team mapping, human user and session, and V3 role/grant sources before evaluating both events.change and events.view; a change-only grant cannot receive detail data. An active unassigned alert can be claimed by the current human; same-user retry with the current revision is idempotent, another claimant or inactive alert is 409. Owner and durable audit update atomically. Returns the updated evidence-backed nullable V3AlertDetail, not an assertion that external escalation stopped. The frozen frontend sends no expected_revision and incorrectly says escalation stopped; page acceptance remains pending.')
S['V3AlertGroupPartial'] = obj({**fieldnames('id service since owner'),
    'severity':string(enum=['P1','P2','P3','P4']),
    'labels':arr(v3_object('k v')),'count':integer(),
    'state':string(enum=['firing','pending','silenced','resolved']),
    'notify':v3_object('channel text'),'badges':arr(v3_object('text tone')),
    'instances':arr(obj({**fieldnames('id value state duration'), 'series':arr(v3_num())}))})
v3_route('get','/alert-groups','Read incident-backed alert groups',
    response=obj({'items':arr(ref('V3AlertGroupPartial')),'counts':v3_map(integer())}),
    query=[v3_query('q'),v3_query('severity',{'type':'array','items':string(enum=['P1','P2','P3','P4','all']),'uniqueItems':True}),v3_query('env'),v3_query('state')],
    desc='severity accepts repeated query keys (form/explode) and matches each group\'s highest severity; all must stand alone. Partial: pending/silenced/inhibited states, metric series, and full notification grouping are not connected; no synthetic signal values are emitted.')
for v3_parameter in P['/alert-groups']['get']['parameters']:
    if v3_parameter['name'] == 'severity':
        v3_parameter.update({'style':'form','explode':True})
v3_route('get','/incidents','List scoped incidents',response=obj({'items':arr(ref('V3IncidentSummary')),
    'counts':obj({k:integer(minimum=0) for k in ['active','resolved','all']},
        ['active','resolved','all'])}, ['items','counts']),
    query=[v3_query('status',string(enum=['active','resolved','all'],default='active'))],
    desc='Counts cover all visible incidents regardless of the selected status. A mitigated incident remains active until work is resolved. Duplicate, empty, or unknown status values return 422.')
v3_route('get','/incidents/{id}','Read incident detail when impact is available',
    desc='Currently always returns 503 impact_unavailable after identity and scope checks; required SLO impact data is not connected.')
P['/incidents/{id}']['get']['responses'].pop('200')
v3_route('post','/incidents/{id}/timeline','Append a durable incident note',
    role='operator',body=obj({'text':string()},['text']),response=ref('V3IncidentTimelineEntry'),status=201)
v3_route('get','/incidents/{id}/timeline/manual','Read persisted human incident notes',
    response=obj({'items':{'type':'array','items':ref('V3IncidentTimelineEntry'),'maxItems':500},
        'scope':string(enum=['manual_only'])}, ['items','scope']),
    desc='Auxiliary read outside the 166 V3 page contracts. Resolves the incident application and environment from persisted records and checks current events.view before reading its manually authored v3_incident_timeline rows. Items are ordered by persisted created_at and ID; the actor name comes from the current user record. The response contains no inferred system or AI entries, impact, error budget, or complete incident detail. An absent or inaccessible incident returns 404; more than 500 manual entries or storage failure returns 503 without a partial list. This route does not replace GET /incidents/{id}.')
S['V3ObservedAlertRuleSummary'] = obj({**fieldnames('id folder name environment_id application_id'),
    'state':string(enum=['firing','pending','ok'])},
    ['id','folder','name','state','environment_id','application_id'])
S['V3ObservedAlertRuleDetail'] = obj({**S['V3ObservedAlertRuleSummary']['properties'],
    **fieldnames('datasource expr last_evaluation'), 'for_seconds':v3_num(),
    'labels':v3_map(string()),'annotations':v3_map(string()),
    'evaluation_ms':v3_num(),'partial':v3_bool(),'unavailable':arr(string())})
S['V3AlertRuleSummary'] = obj({**fieldnames('id folder name'),
    'state':string(enum=['firing','pending','ok']), 'schedule':string()},
    ['id','folder','name','state'])
S['V3AlertRuleList'] = obj({'items':arr(ref('V3AlertRuleSummary')),
    'counts':obj({k:integer(minimum=0) for k in ['all','firing','scheduled']},
        ['all','firing','scheduled'])}, ['items','counts'])
v3_route('get','/alert-rules','Read complete authorized alert rule summaries',
    response=ref('V3AlertRuleList'),
    desc='Rules come from configured Prometheus sources and exact server-owned file/group application bindings, with matching environment/application labels and current application or owner-team alert_rules.view. Application/team scope and grants are rechecked after provider reads; changed scope does not return the old result. Counts describe the complete authorized result. Missing configuration, upstream failure or a result above the bound returns an error rather than synthetic or partial counts. Scheduled is zero until a trusted schedule source is connected.')
v3_route('get','/alert-rules/observed','Read provider-observed alert rule summaries',
    response=obj({'items':arr(ref('V3ObservedAlertRuleSummary')),
        'counts':v3_map(integer()),'partial':v3_bool(),
        'unavailable_environments':arr(string())}),
    desc='Auxiliary read-only backend endpoint, not the V3 AlertRule DTO. Rule groups require explicit server-side application/environment binding, matching provider labels and current application or owner-team alert_rules.view. Scope and grants are rechecked after provider reads; changed scope does not return old summaries. Unconfigured sources return 503; partial source failure returns 206.')
P['/alert-rules/observed']['get']['responses']['206'] = P['/alert-rules/observed']['get']['responses']['200']
v3_route('get','/alert-rules/observed/{id}','Read one provider-observed alert rule',
    response=ref('V3ObservedAlertRuleDetail'),
    desc='Auxiliary read-only backend endpoint. The rule must be uniquely identified within the currently authorized application or owner-team provider result, with scope rechecked after upstream reads. Duplicate visible stable IDs return 503 alert_rule_identity_conflict rather than an arbitrary expression or state. An unknown ID with a failed in-scope provider returns 503 rule_source_partial; a complete miss returns 404. No historical preview, threshold condition, notification route or AI triage is inferred.')
S['V3RuleDryRunRequest'] = obj({
    'expr':string(minLength=1,maxLength=8192,description='Must exactly equal the current authorized provider-observed rule expression; arbitrary PromQL is rejected.'),
    'for':string(minLength=2,maxLength=32,description='Go duration from 0s to 1h, for example 5m.')}, ['expr','for'])
S['V3RuleDryRunResult'] = obj({
    'would_fire':integer(minimum=0,maximum=500),
    'windows':{'type':'array','maxItems':500,'items':string(description='UTC ISO-8601 interval start/end for one newly firing series episode in the last six hours.')}},
    ['would_fire','windows'])
v3_route('post','/alert-rules/{id}/dry-run','Simulate one authorized rule from real Prometheus range samples',
    body=ref('V3RuleDryRunRequest'), response=ref('V3RuleDryRunResult'),
    desc='Read-only, exactly one selected environment. Resolve a unique active rule using the server-owned file/group-to-application binding, matching provider labels and current application or owner-team alert_rules.view; unknown or hidden rules do not expose a result. The submitted expr must exactly equal that current rule expression, which must itself be reviewed as trusted configuration. The for duration may vary from 0s to 1h. Query the configured environment Prometheus query_range over the last six hours plus bounded pre-roll at the observed rule-group interval (15s to 5m, or a disclosed 1m fallback when omitted). Every returned vector series must itself carry the exact bound environment and application labels; missing or mixed scope fails closed. A vector element is active by its presence, including numeric zero. would_fire counts distinct series episodes that newly reach for within those six hours; windows contains one sorted UTC start/end interval per episode, at most 500. This is a sampled simulation from real query data, not persisted historical alert state or a notification replay; provider query_offset and keep_firing_for are not reproduced. The rule, owner, scope, and grants are rechecked after query_range; changed scope fails closed. Missing source or partial/malformed/over-limit provider data returns 503 without synthetic counts. The frozen frontend still sends threshold/for_minutes rather than expr/for; page acceptance remains pending.')

S['V3SilenceMatcher'] = obj(fieldnames('k v'), ['k','v'])
S['V3SilencePreviewMatcher'] = obj({'k':string(pattern='^[a-zA-Z_][a-zA-Z0-9_]*$'),'op':string(enum=['=','!=','=~','!~']),
    'v':string(minLength=1,maxLength=256,description='The server enforces a 256 UTF-8 byte maximum.')}, ['k','op','v'])
S['V3SilenceRemaining'] = obj({'pct':integer(minimum=0,maximum=100),'text':string()},['pct','text'])
S['V3SilenceRead'] = obj({**fieldnames('id window creator reason'),
    'matchers':arr(ref('V3SilenceMatcher')),
    'state':string(enum=['active','planned','expired']),
    'remaining':ref('V3SilenceRemaining'),'affects':integer(minimum=0)},
    ['id','matchers','state','window','affects','creator','reason'])
S['V3InhibitionRead'] = obj({'id':string(),'source':arr(ref('V3SilenceMatcher')),
    'target':arr(ref('V3SilenceMatcher')),'equal':arr(string()),'stat':string()},
    ['id','source','target','equal','stat'])
v3_route('get','/silences','Read environment-scoped Alertmanager silences',
    response=obj({'items':arr(ref('V3SilenceRead')),
        'counts':obj({k:integer(minimum=0) for k in ['active','planned','expired']},['active','planned','expired']),
        'partial':v3_bool(),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool()},
        ['items','counts','partial','unavailable_environments','unscoped_present']),
    query=[v3_query('state',string(enum=['active','planned','expired','all']))],
    desc='Only provider silences with an exact positive non-regex environment matcher are returned. Application-scoped records also require an explicit server-owned application-label-to-persisted-ID binding to an active application in that environment and a current V3 application/team view grant. Exact environment silences with wider application scope require environment-wide silence view and manage grants. Ambiguous matchers are hidden. Affects comes from provider-observed alerts restricted to the authorized environment and application; counts cover the visible records. Broad records without exact environment binding are hidden and set unscoped_present for environment administrators (206). An authorized source failure, a duplicate visible ID or more than 500 visible records fails closed, without partial counts. Provider responses are bounded to 2 MiB.')
P['/silences']['get']['responses']['206'] = P['/silences']['get']['responses']['200']
v3_route('post','/silences/preview','Count currently matching Alertmanager alerts without creating a silence',
    body=obj({'matchers':{'type':'array','items':ref('V3SilencePreviewMatcher'),
        'minItems':1,'maxItems':20}}, ['matchers']),
    response=obj({'affects':integer(minimum=0),'sample':{'type':'array','items':string(),'maxItems':5}}, ['affects','sample']),
    desc='Read-only live snapshot in exactly one selected environment; all is rejected with 409. The selected environment is an implicit exact matcher. An exact application= provider label must be bound by server configuration to an active persisted application and requires current silences.view on that application or owner team; unknown and hidden bindings both return 404. Without an exact application matcher, current environment-wide silences.view and silences.manage are required. Scope, grants, and application ownership are rechecked after the Alertmanager read; changed scope does not return old counts. Affects counts all matching current alerts in that selected environment, not historical or future alerts. Sample contains at most five distinct sorted real alertname values, not representative alert records. More than 500 current-environment alerts, an upstream error, or malformed upstream null returns 503 without partial counts. No silence is created. The current frontend form and stale-result handling are not compatible with the authorization/refresh semantics, so page acceptance remains pending.')
v3_route('get','/inhibitions','Read authorized Alertmanager inhibition configuration',
    response=obj({'items':arr(ref('V3InhibitionRead')),'partial':v3_bool(),
        'unavailable':arr(string()),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool()},
        ['items','partial','unavailable','unavailable_environments','unscoped_present']),status=206,
    desc='Always 206 partial when successful, including an empty list: only rules with exact source and target environment matchers are considered. An application-scoped rule also needs a server-owned application-label-to-active-ID binding and current application or owner-team silences.view grant for every exact application matcher. Wider application scope requires both current environment-wide silences.view and silences.manage. Cross-environment, inactive, unbound, hidden, mixed, and ambiguous rules are omitted; only authorized environment administrators may receive unscoped_present=true. Alertmanager status has no per-rule activity or hit counts, so active is omitted and stat explains unavailable data. Authorization is checked before reading upstream; unconfigured or failed upstream returns 503 without partial results.')
P['/inhibitions']['get']['responses']['503'] = {
    'description':'Authorized inhibition source, scope, or application catalog is unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
