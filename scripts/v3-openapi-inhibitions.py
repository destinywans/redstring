# WAN-118 inhibition contract fragment, loaded after v3-openapi-alerts.py.
# The fragment only describes handlers in internal/v3alertmanager.
S['V3InhibitionMatcherRead'] = obj({
    'k':string(pattern='^[a-zA-Z_][a-zA-Z0-9_]*$'),
    'op':string(enum=['=','!=','=~','!~']),
    'value_redacted':v3_bool()}, ['k','op','value_redacted'])
S['V3InhibitionMatcherDraft'] = obj({
    'k':string(pattern='^[a-zA-Z_][a-zA-Z0-9_]*$'),
    'op':string(enum=['=','!=','=~','!~']),
    'v':string(minLength=1,maxLength=256)}, ['k','op','v'])
S['V3InhibitionRead'] = obj({
    'id':string(), 'source':arr(ref('V3InhibitionMatcherRead')),
    'target':arr(ref('V3InhibitionMatcherRead')), 'equal':arr(string()),
    'stat':string(), 'active':{'type':'null'}, 'hits_7d':{'type':'null'}},
    ['id','source','target','equal','stat','active','hits_7d'])
S['V3InhibitionDraftRequest'] = obj({
    'application_id':string(minLength=1,maxLength=128),
    'source':{'type':'array','minItems':1,'maxItems':20,'items':ref('V3InhibitionMatcherDraft')},
    'target':{'type':'array','minItems':1,'maxItems':20,'items':ref('V3InhibitionMatcherDraft')},
    'equal':{'type':'array','minItems':1,'maxItems':10,'items':string(pattern='^[a-zA-Z_][a-zA-Z0-9_]*$')}},
    ['application_id','source','target','equal'])
S['V3InhibitionPreview'] = obj({
    'environment_id':string(), 'application_id':string(), 'config_revision':string(pattern='^sha256:[a-f0-9]{64}$'),
    'window_start':string(format='date-time'), 'window_end':string(format='date-time'),
    'basis':string(enum=['complete_history_simulation']),
    'potentially_suppressed':integer(minimum=0),
    'sample':{'type':'array','maxItems':10,'items':string()},
    'actual_inhibition_hits':{'type':'null'}, 'history_complete':v3_bool()},
    ['environment_id','application_id','config_revision','window_start','window_end',
     'basis','potentially_suppressed','sample','actual_inhibition_hits','history_complete'])
S['V3InhibitionDraftResponse'] = obj({
    'id':string(), 'status':string(enum=['pending_review']),
    'status_basis':string(enum=['local_submission']),
    'effect':string(enum=['no_runtime_change_by_this_endpoint']),
    'current_mr_state':{'type':'null'}, 'runtime_state':{'type':'null'},
    'source_revision':string(pattern='^sha256:[a-f0-9]{64}$'),
    'review':obj({'project_id':integer(minimum=1),'merge_request_iid':integer(minimum=1),
                  'url':string()},['project_id','merge_request_iid','url'])},
    ['id','status','status_basis','effect','current_mr_state','runtime_state','source_revision','review'])
v3_route('get','/inhibitions','Read authorized Alertmanager inhibition configuration',
    response=obj({'items':arr(ref('V3InhibitionRead')),'partial':v3_bool(),
        'unavailable':arr(string()),'unavailable_environments':arr(string()),
        'unscoped_present':v3_bool(),
        'config_revisions':{'type':'object','additionalProperties':string(pattern='^sha256:[a-f0-9]{64}$')},
        'observed_at':{'type':'object','additionalProperties':string(format='date-time')},
        'empty_reason':v3_empty_reason()},
        ['items','partial','unavailable','unavailable_environments','unscoped_present','config_revisions','observed_at','empty_reason']),status=206,
    desc='Reads current environment-bound Alertmanager configuration. Only exactly scoped and currently authorized source/target rules are returned. Matcher values and receiver secrets are redacted. A single-environment response includes a quoted SHA-256 ETag over the exact observed config.original; config_revisions maps environments when multiple are selected. Activity and historical hit counts are null/unknown, even when no rule is visible. Application bindings and grants are rechecked after the upstream read. No rule is applied here. Nothing readable before the upstream read returns 200 with an empty list and empty_reason=no_authorized_target (PA-09); a scope that becomes unreadable during the read is still 403.')
P['/inhibitions']['get']['responses']['200'] = P['/inhibitions']['get']['responses']['206']
P['/inhibitions']['get']['responses']['503'] = {'description':'Inhibition source or authorization unavailable',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/inhibitions/preview','Simulate a bounded seven-day inhibition window',
    body=ref('V3InhibitionDraftRequest'),response=ref('V3InhibitionPreview'),
    desc='Requires one environment, current environment-wide silences.view/manage, an active bound application, and server-enforced exact environment/application matchers on both source and target. Target severity must be exactly P2/P3/P4 or a literal subset of those values; P1 cannot be targeted. A trusted read-only source must attest complete seven-day firing-interval occurrence coverage for its bound environment. More than 500 occurrences, incomplete coverage, invalid labels, or unavailable current config fail closed without a count. The integer is a simulated potential suppression count, not actual historical Alertmanager hits. Sample contains at most ten distinct real alertname values from matching targets. Missing source is 501 not_connected; no configuration, MR or notification is written.')
P['/inhibitions/preview']['post']['responses']['501'] = {'description':'Complete seven-day history or config source is not connected',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/inhibitions','Create an audited GitOps inhibition review draft',
    role='operator',body=ref('V3InhibitionDraftRequest'),response=ref('V3InhibitionDraftResponse'),status=202,
    desc='Requires one environment, environment-wide silences.view/manage, an active bound application, a quoted If-Match ETag from GET /inhibitions, and Idempotency-Key. The server adds exact environment/application constraints to both sides and restricts target severity to a finite non-P1 set. It reserves an immutable versioned draft with transaction-checked ownership, session, grants and audit, then opens a reviewed GitLab MR in a branch namespace and target isolated from config-sync promotions. Same-key retries reconcile the same branch/path/payload. pending_review is local submission state; current MR and runtime states remain null. This endpoint never merges, applies, reloads or sends notifications. Independent config-owner validation and activation are required after review.')
P['/inhibitions']['post']['parameters'] += [
    {'name':'Idempotency-Key','in':'header','required':True,'schema':string(minLength=8,maxLength=128)},
    {'name':'If-Match','in':'header','required':True,'schema':string(description='Quoted config_revision ETag from GET /inhibitions')}]
P['/inhibitions']['post']['responses']['501'] = {'description':'Reviewed GitOps repository or exact config source is not connected',
    'content':{'application/json':{'schema':ref('Error')}}}
