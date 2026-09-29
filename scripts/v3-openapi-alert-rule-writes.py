"""Governed alert-rule writes; loaded after the alerts V3 fragment."""

S['V3AlertRuleDraftSpec'] = obj({
    'application_id':string(minLength=1),
    'environment_id':string(minLength=1),
    'group':string(minLength=1,maxLength=128),
    'name':string(minLength=1,maxLength=128),
    'expr':string(minLength=1,maxLength=8192),
    'for':string(minLength=1,maxLength=32),
    'labels':v3_map(string()),
    'annotations':v3_map(string()),
    'mode':string(enum=['draft']),
    'observed_rule_id':string(description='Optional provider-observed rule ID when proposing a change to an existing bound rule.'),
}, ['application_id','environment_id','group','name','expr','for','labels','annotations','mode'])
_alert_rule_create_properties = dict(S['V3AlertRuleDraftSpec']['properties'])
_alert_rule_create_properties['mode'] = string(enum=['draft','dry_run','enabled'])
_alert_rule_create_properties['investigation_id'] = string(description='Required for enabled mode: an open investigation in the selected application and environment.')
S['V3AlertRuleCreateSpec'] = obj(_alert_rule_create_properties,
    ['application_id','environment_id','group','name','expr','for','labels','annotations','mode'])
S['V3AlertRuleObservedIdentity'] = obj({
    'id':string(minLength=1),
    'group':string(minLength=1),
    'name':string(minLength=1),
}, ['id','group','name'])
S['V3AlertRuleUpdateDraftRequest'] = obj({
    'expected_version':integer(minimum=1),
    'observed_rule':ref('V3AlertRuleObservedIdentity'),
    'rule':ref('V3AlertRuleDraftSpec'),
}, ['expected_version','rule'])
S['V3AlertRulePendingMR'] = obj({
    'project_id':string(minLength=1),
    'iid':integer(minimum=1),
    'url':string(format='uri'),
}, ['project_id','iid','url'])
S['V3AlertRuleDraftResult'] = obj({
    'draft_id':string(minLength=1),
    'version':integer(minimum=1),
    'status':string(enum=['pending_review']),
    'mr':ref('V3AlertRulePendingMR'),
}, ['draft_id','version','status','mr'])
S['V3AlertRuleTrialResult'] = obj({
    'trial_id':string(minLength=1),
    'status':string(enum=['dispatch_pending','queued','running','completed','inconclusive','stopped','stalled']),
    'started_at':string(format='date-time',nullable=True),
    'expires_at':string(format='date-time',nullable=True),
    'last_update':string(format='date-time'),
    'evaluated_points':integer(minimum=0,maximum=1440),
    'missing_points':integer(minimum=0,maximum=1440),
    'would_fire':integer(minimum=0,maximum=500),
    'windows':{'type':'array','items':string()},
}, ['trial_id','status','started_at','expires_at','last_update','evaluated_points','missing_points','would_fire','windows'])
S['V3AlertRuleEnabledIntentResult'] = obj({
    'draft_id':string(minLength=1),
    'version':integer(minimum=1),
    'proposal_id':string(minLength=1),
    'status':string(enum=['pending_approval','rejected']),
    'mr':ref('V3AlertRulePendingMR'),
}, ['draft_id','version','proposal_id','status','mr'])
S['V3AlertRuleCreateResult'] = {'oneOf':[ref('V3AlertRuleDraftResult'),ref('V3AlertRuleTrialResult'),ref('V3AlertRuleEnabledIntentResult')]}
S['V3AlertRuleImportRequest'] = obj({
    'application_id':string(minLength=1,description='Optional; if omitted, infer one shared application from exact application labels on every rule.'),
    'yaml':string(minLength=1,maxLength=131072,
        description='At most 128 KiB UTF-8. One supported native Prometheus rule file or limited monitoring.coreos.com/v1 PrometheusRule document; unsupported fields are rejected.'),
    'mode':string(enum=['draft']),
}, ['yaml'])
S['V3AlertRuleImportCandidate'] = obj({
    'group':string(),
    'name':string(),
    'outcome':string(enum=['new','duplicate']),
    'status':string(enum=['pending_review','duplicate_skipped']),
    'reason':string(enum=['new_draft','existing_draft','observed_rule']),
    'draft_state':string(description='Present when an existing local draft identity was skipped.'),
    'draft_id':string(description='Present for a new draft or an existing local draft; absent for a provider-only duplicate.'),
    'remote_review':obj({'project_id':string(),'branch':string(),'mr_iid':integer(minimum=1),'mr_url':string(format='uri')},
        ['project_id','branch']),
    'version':integer(minimum=1,description='Present only for a new candidate.'),
}, ['group','name','outcome','status','reason'])
S['V3AlertRuleImportItem'] = obj({
    'name':string(),
    'status':string(enum=['new','duplicate']),
    'note':string(),
}, ['name','status','note'])
S['V3AlertRuleImportResult'] = obj({
    'status':string(enum=['pending_review','duplicates_only']),
    'mr':ref('V3AlertRulePendingMR'),
    'items':{'type':'array','minItems':1,'maxItems':20,'items':ref('V3AlertRuleImportItem')},
    'candidates':{'type':'array','minItems':1,'maxItems':20,
        'items':ref('V3AlertRuleImportCandidate')},
}, ['status','items','candidates'])

v3_route('post','/alert-rules','Create a governed alert-rule draft, isolated trial, or enable proposal',role='operator',
    body=ref('V3AlertRuleCreateSpec'),response=ref('V3AlertRuleCreateResult'),status=201,
    desc='Requires one selected environment, one active application, alert_rules.change and a safe Idempotency-Key. draft creates an immutable nondeployable GitOps JSON revision and pending GitLab MR; no rule is applied. dry_run requires an explicitly reviewed, hard application-isolated Prometheus query view plus a running durable worker; the entrypoint accepts private per-application configuration but none is configured by default. It records one bounded 24-hour non-notifying trial and returns 202, or 503/429 when unavailable or full. enabled requires an open investigation, current human and proposals.change, a trusted exact deployable YAML plan, and persists a digest-bound proposal action with pending_approval status after exporting a nondeployable draft MR; it returns 202 and does not enable the rule. A separate passkey-bound human approval, independent executor, protected deployable MR and observed GitOps sync plus exact Prometheus rule readback are required before enabled is recorded. No approval producer or rule applier is wired into the product entrypoint.')
P['/alert-rules']['post']['responses']['202']={'description':'Isolated trial queued or enable intent pending human approval','content':{'application/json':{'schema':S['V3AlertRuleCreateResult']}}}
v3_route('put','/alert-rules/{id}','Revise a governed alert-rule draft',role='operator',
    body=ref('V3AlertRuleUpdateDraftRequest'),response=ref('V3AlertRuleDraftResult'),
    desc='The path id identifies the governed draft. expected_version must match its current revision. When the draft references a provider-observed rule, observed_rule and rule.observed_rule_id must preserve that identity. A confirmed pending-review revision rejects provider content drift; rejected or stale-remote revisions may be explicitly superseded against the current observed content. The replacement remains in one selected environment/application/group with exact scope labels and mode=draft. A successful update writes a new immutable, nondeployable GitOps JSON revision to a pending GitLab MR; it never enables, merges or applies a rule. Same-key same-payload retry returns the same result; changed payload conflicts. Validation returns 422; missing or uncertain GitLab connectivity returns 503 not_connected/upstream.')
v3_route('post','/alert-rules/import','Import bounded rule YAML as review drafts',role='operator',
    body=ref('V3AlertRuleImportRequest'),response=ref('V3AlertRuleImportResult'),status=201,
    desc='Accepts the frozen frontend {yaml} body; application_id and mode=draft are optional. One selected environment and one application inferred from exact per-rule labels require current alert_rules.change. Parse at most 10 groups and 20 alerting rules from one native Prometheus rule file or limited PrometheusRule CRD document; reject unsupported fields, recording rules, group-level evaluation options, unsafe YAML and mixed or missing application/environment/severity labels. Every candidate expression receives PromQL syntax validation, including duplicates. items is the frozen frontend new/duplicate/note projection; candidates adds group, draft identity and duplicate reason. The parse request immediately creates nondeployable review drafts for new rules, with one pending GitLab MR; an all-duplicate import returns 200 and no MR. No rule is enabled, merged or applied. If Idempotency-Key is omitted, the server reuses the most recent matching operation while remote result is unfinished, or for five minutes after completion when observed provider identities are unchanged; later or changed sources are reclassified. A rejected or stale remote prior draft is reported as an existing local duplicate and is not exported again; other newly eligible rules in the same YAML may still create a separate pending MR. Stale remote candidates include project, branch and known MR for human review. Explicit keys retain durable replay, and changed payload with the same explicit key conflicts. Missing or uncertain GitLab connectivity returns 503 not_connected/upstream.')

for _path, _method in [('/alert-rules','post'),('/alert-rules/{id}','put'),('/alert-rules/import','post')]:
    P[_path][_method].setdefault('parameters', []).append({
        'name':'Idempotency-Key','in':'header','required':_path != '/alert-rules/import',
        'schema':string(minLength=1,maxLength=128),
        'description':'Unique per actor and operation; replay with the same payload returns the original result. Optional on import: omitted key reuses unfinished operations and recent completed operations with unchanged observed source identities.'})
    P[_path][_method]['responses']['413']={'description':'Serialized draft exceeds the bounded GitOps payload','content':{'application/json':{'schema':ref('Error')}}}
    if _path != '/alert-rules':
        P[_path][_method]['responses']['501']={'description':'Only draft mode is supported for update and import','content':{'application/json':{'schema':ref('Error')}}}
P['/alert-rules/import']['post']['responses']['200']={'description':'All input rules are duplicates; no GitOps MR was created','content':{'application/json':{'schema':ref('V3AlertRuleImportResult')}}}

v3_route('get','/alert-rules/dry-runs/{id}','Read one isolated alert-rule trial',role='operator',
    response=ref('V3AlertRuleTrialResult'),
    desc='Reads only the creating actor\'s trial in the selected environment after current alert_rules.change and active application access. Status stalled is derived when a queued/running worker has not updated the row for over two minutes. Completed requires all 1440 one-minute evaluations; missed or failed evaluations are inconclusive rather than zero firing. This route and the worker require a deployment-reviewed, application-isolated query source and are not connected by default.')
