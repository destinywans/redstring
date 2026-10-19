# D11 §11.4–11.6: structured views of alert rule enable and disable
# proposals, their executions and the rule list's lifecycle. Runs after the
# reviews, executions, alerts and rollback fragments it extends.
_text_or_null = {'type':['string','null']}
_time_or_null = {'type':['string','null'],'format':'date-time'}
S['V3ProposalAlertRule'] = obj({
    'operation':string(enum=['enable','disable']),
    'rule':obj({**fieldnames('name group expr for severity'),
        'labels':v3_map(string()),'annotations':v3_map(string())},
        ['name','group','expr','for','severity','labels','annotations']),
    'repository':obj({'connector_id':_text_or_null,'project_id':string(),'project_path':_text_or_null,
        'web_url':_text_or_null,'target_branch':string(),'path':string(),'base_sha':string()},
        ['connector_id','project_id','project_path','web_url','target_branch','path','base_sha']),
    'change':v3_nullable(obj({
        'lines':arr(obj({'op':string(enum=['+','-',' ']),'text':string()},['op','text'])),
        'content_sha256':string()},['lines','content_sha256'])),
    'prometheus':obj({'group':string(),'file':string(),'readback_timeout_seconds':integer(minimum=0)},
        ['group','file','readback_timeout_seconds']),
    'merge':obj({'method':string(enum=['fast_forward','merge_request']),'by':string(enum=['executor','human'])},['method','by']),
}, ['operation','rule','repository','change','prometheus','merge'])
S['V3Proposal']['properties']['alert_rule'] = {**v3_nullable(ref('V3ProposalAlertRule')),
    'description':'D11 §11.4: set for prometheus.alert_rule.enable and .disable, null otherwise or when the view cannot be built '
        '(the review content still shows the whole action). connector_id, project_path and web_url are null when the deployment '
        'binding does not name them; change is null for a proposal created before differences were recorded. change is the whole '
        'file from the base revision to the approved bytes (content_sha256 equals the approved digest); it is review evidence, '
        'not part of the approval. merge is fast_forward by the executor for a file_pull binding, merge_request by a human otherwise.'}
S['V3Proposal']['required'] += ['alert_rule']

S['V3AlertRuleExecutionDetail'] = obj({
    'id':string(),
    'kind':string(enum=['prometheus.alert_rule.enable','prometheus.alert_rule.disable']),
    **fieldnames('proposal_id investigation_id application_id environment_id'),
    'approved_at':string(format='date-time'),
    'approvers':arr(obj({'name':string(),'verified_at':string(format='date-time')},['name','verified_at'])),
    'created_at':string(format='date-time'),'updated_at':string(format='date-time'),
    'alert_rule':obj({
        'proposal_id':string(),'operation':string(enum=['enable','disable']),
        'state':string(enum=['reserved','committing','merging','merged','loading','enabled','disabled','failed',
            'needs_attention','uncertain']),
        'steps':arr(obj({'state':string(enum=['reserved','committing','merging','merged','loading','enabled','disabled',
            'failed','needs_attention','uncertain','pending_review','recheck']),'at':string(format='date-time'),
            'reason':{**_text_or_null,'description':'PA-90: the failure code of needs_attention and failed, what left uncertain unknown, '
                'manual for a recheck; null otherwise'}},['state','at','reason'])),
        'repository':obj({'project_id':string(),'project_path':_text_or_null,'source_branch':string(),
            'target_branch':string(),'path':string(),'commit_sha':_text_or_null,'mr_iid':{'type':['integer','null']},
            'mr_url':_text_or_null,'merged_sha':_text_or_null,'merged_at':_time_or_null},
            ['project_id','project_path','source_branch','target_branch','path','commit_sha','mr_iid','mr_url','merged_sha','merged_at']),
        'readback':obj({'state':string(enum=['pending','loaded','absent','mismatch','error','timeout']),
            'checked_at':_time_or_null,'first_seen_at':_time_or_null,'health':_text_or_null,
            'last_evaluation_at':_time_or_null,'last_error':_text_or_null,
            'reload':v3_nullable(obj({'last_successful':{'type':['boolean','null']},'last_success_at':_time_or_null,
                'unavailable':{'type':['string','null'],'enum':[None,'not_found','ambiguous','source_unavailable','not_connected']}},
                ['last_successful','last_success_at','unavailable']))},
            ['state','checked_at','first_seen_at','health','last_evaluation_at','last_error','reload']),
        'failure':v3_nullable(obj({'code':string(enum=['stale_base','branch_conflict','mr_closed','protection_changed',
            'merge_refused','credential_rejected','approval_invalid','plan_changed','source_changed','load_timeout',
            'rule_health_error']),'message':string()},['code','message'])),
    }, ['proposal_id','operation','state','steps','repository','readback','failure']),
}, ['id','kind','proposal_id','investigation_id','application_id','environment_id','approved_at','approvers',
    'created_at','updated_at','alert_rule'])
P['/executions/{id}']['get']['responses']['200']['content']['application/json']['schema']['oneOf'].append(ref('V3AlertRuleExecutionDetail'))
P['/executions/{id}']['get']['description'] += (' An alert rule enable or disable execution (D11 §11.5, kind prometheus.alert_rule.enable '
    'or .disable) is projected from the executor ledger: state (exporting is committing, or merging once the MR exists; '
    'awaiting_provider is loading, or needs_attention after load_timeout; stale_remote is needs_attention), the step timeline, '
    'the commit, MR and merge, the last Prometheus readback and the failure code. Fields of steps not reached yet are null; '
    'readback.reload is set while a load has timed out: the last configuration reload of Prometheus '
    '(prometheus_config_last_reload_successful and its last success time, or why they could not be read), also named in the '
    'load_timeout failure message (PA-90). Each step may carry a reason; a load timeout is a needs_attention step and a manual '
    'recheck a recheck step, so recovery keeps both on the timeline. It needs proposals.view on the application or its owner team.')

S['V3AlertRuleLifecycle'] = obj({'state':string(enum=['active','disable_pending_approval','disabling','needs_attention']),
    'proposal_id':_text_or_null,'execution_id':_text_or_null},['state','proposal_id','execution_id'])
S['V3AlertRuleSummary']['properties']['lifecycle'] = ref('V3AlertRuleLifecycle')
S['V3AlertRulePending'] = obj({
    'proposal_id':string(),'execution_id':_text_or_null,'operation':string(enum=['enable','disable']),
    **fieldnames('rule_name group application_id environment_id href'),
    'state':string(enum=['pending_approval','executing','needs_attention','failed','rejected','expired'],
        description='expired (PA-217): approved, but the approval expired or was revoked before any execution, so the change will not run; updated_at is when it lapsed'),
    'updated_at':string(format='date-time')},
    ['proposal_id','execution_id','operation','rule_name','group','application_id','environment_id','state','updated_at','href'])
S['V3AlertRuleList']['properties']['pending'] = {**v3_nullable(arr(ref('V3AlertRulePending'))),
    'description':'D11 §11.6: enables and disables not in effect yet, only proposals the caller may view (proposals.view on the '
        'application or its owner team), newest first, at most 50; failed, rejected and expired only within seven days. href is the review '
        'page until the executor has an execution, then the execution page. Null, with unavailable.pending, when it cannot be read; '
        'absent when no executor is wired.'}
P['/alert-rules']['get']['description'] += (' D11 §11.6: with the executor wired each item carries lifecycle (active, or '
    'disable_pending_approval, disabling or needs_attention with the disable proposal and execution IDs), apart from whether '
    'the rule fires; when it cannot be read, lifecycle is omitted and unavailable names items[].lifecycle.')

# PA-88: V3 opens an incident's investigation itself (the historical route is
# no longer registered on a V3 instance).
P['/incidents/{id}/investigation']['post']['description'] += (' V3 (PA-88): id is an alert incident or a manual '
    'incident. Needs one selected environment, events.view on the incident (404 otherwise) and events.change on it (403 '
    'forbidden otherwise, nothing written; PA-252), like starting one from an alert group. The investigation '
    'already linked to the incident is reused; otherwise one is created for its service and environment (an INV- ID like the '
    'canvas, titled after the incident, the alert summary or the manual impact as its goal, a main branch with an empty '
    'context) and linked back; an open alert incident becomes investigating. No model is called: a model_id is refused '
    'with 422 initial_analysis_unavailable, and the AI is asked from the canvas. In an investigation opened this way, '
    'read_service_changes defaults to 2 hours before the earliest linked alert started until now (D11 §4.4). The '
    'response is the incident as GET /incidents/{id} returns it, with investigation_id. Unknown or invisible incidents are 404.')
P['/incidents/{id}/investigation']['post']['responses']['422'] = {'description':'Invalid body, or a model_id (initial_analysis_unavailable)',
    'content':{'application/json':{'schema':ref('Error')}}}
