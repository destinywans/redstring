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
S['V3AlertRuleImportRequest'] = obj({
    'application_id':string(minLength=1),
    'yaml':string(minLength=1,maxLength=131072,
        description='At most 128 KiB UTF-8, with a bounded number of groups and rules. The server parses and validates rule groups, labels, expressions and durations.'),
    'mode':string(enum=['draft']),
}, ['application_id','yaml','mode'])
S['V3AlertRuleImportCandidate'] = obj({
    'group':string(),
    'name':string(),
    'outcome':string(enum=['new','duplicate']),
    'status':string(enum=['pending_review','duplicate_skipped']),
    'draft_id':string(description='Present only for a new candidate.'),
    'version':integer(minimum=1,description='Present only for a new candidate.'),
}, ['group','name','outcome','status'])
S['V3AlertRuleImportResult'] = obj({
    'status':string(enum=['pending_review','duplicates_only']),
    'mr':ref('V3AlertRulePendingMR'),
    'candidates':{'type':'array','minItems':1,'maxItems':20,
        'items':ref('V3AlertRuleImportCandidate')},
}, ['status','candidates'])

v3_route('post','/alert-rules','Create a governed alert-rule draft',role='operator',
    body=ref('V3AlertRuleDraftSpec'),response=ref('V3AlertRuleDraftResult'),status=201,
    desc='Requires one selected environment, one active application in that environment, and the current alert_rules.change grant. The group is explicit. labels must contain application and environment with exact server-bound values; conflicting or missing scope labels return 422. Validate the complete rule and write an immutable, nondeployable GitOps draft JSON revision to a pending GitLab merge request. No rule is enabled, merged or applied. The same Idempotency-Key and payload return the same draft/MR; a changed payload or conflicting rule identity returns 409. An unconfigured or uncertain GitLab result returns 503 not_connected/upstream.')
v3_route('put','/alert-rules/{id}','Revise a governed alert-rule draft',role='operator',
    body=ref('V3AlertRuleUpdateDraftRequest'),response=ref('V3AlertRuleDraftResult'),
    desc='The path id identifies the governed draft. expected_version must match its current revision. When the draft references a provider-observed rule, observed_rule and rule.observed_rule_id must preserve that identity. A confirmed pending-review revision rejects provider content drift; rejected or stale-remote revisions may be explicitly superseded against the current observed content. The replacement remains in one selected environment/application/group with exact scope labels and mode=draft. A successful update writes a new immutable, nondeployable GitOps JSON revision to a pending GitLab MR; it never enables, merges or applies a rule. Same-key same-payload retry returns the same result; changed payload conflicts. Validation returns 422; missing or uncertain GitLab connectivity returns 503 not_connected/upstream.')
v3_route('post','/alert-rules/import','Import bounded rule YAML as review drafts',role='operator',
    body=ref('V3AlertRuleImportRequest'),response=ref('V3AlertRuleImportResult'),status=201,
    desc='Requires one selected environment and an active application with alert_rules.change. Parse at most 10 groups and 20 rules from one YAML document; never trust a caller-supplied group override, and require every rule to carry exact application/environment labels matching the selected binding. Reject malformed, unsafe, over-limit or cross-scope YAML with 422. Compare each candidate with currently authorized identities: report new/pending_review or duplicate/duplicate_skipped explicitly. An all-duplicate input returns 200 duplicates_only with no MR; new candidates return 201 and become immutable, nondeployable GitOps draft JSON in one pending GitLab MR. Duplicates are not written. No rule is enabled, merged or applied. Same-key same-payload retry returns the same pending draft/MR; changed payload conflicts. Missing or uncertain GitLab connectivity returns 503 not_connected/upstream.')

for _path, _method in [('/alert-rules','post'),('/alert-rules/{id}','put'),('/alert-rules/import','post')]:
    P[_path][_method].setdefault('parameters', []).append({
        'name':'Idempotency-Key','in':'header','required':True,
        'schema':string(minLength=1,maxLength=128),
        'description':'Unique per actor and operation; replay with the same payload returns the original result.'})
    P[_path][_method]['responses']['413']={'description':'Serialized draft exceeds the bounded GitOps payload','content':{'application/json':{'schema':ref('Error')}}}
    P[_path][_method]['responses']['501']={'description':'dry_run or enabled mode requires a separate preview or approval and execution path','content':{'application/json':{'schema':ref('Error')}}}
P['/alert-rules/import']['post']['responses']['200']={'description':'All input rules are duplicates; no GitOps MR was created','content':{'application/json':{'schema':ref('V3AlertRuleImportResult')}}}
