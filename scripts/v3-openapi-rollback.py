"""Kubernetes Deployment rollback through a V3 proposal (PA-08, WAN-305).

Design: docs/plans/2026-10-10-rollback-executor-and-registry-p3.md. Loaded
after the reviews and executions fragments, whose schemas it extends.
"""

# One container-level difference between the current Pod template and a
# candidate revision. Literal environment values are shown; values that come
# from a Secret or ConfigMap are not (value_hidden).
S['V3RollbackChange'] = obj({
    'container':string(),
    'field':string(enum=['image','env','resources','command','other']),
    'name':{'type':['string','null'],'description':'Environment variable name for field env'},
    'from':{'type':['string','null']},
    'to':{'type':['string','null']},
    'value_hidden':v3_bool(),
}, ['container','field','name','from','to','value_hidden'])
S['V3RollbackContainer'] = obj({'name':string(),'image':string()}, ['name','image'])
S['V3RollbackVerificationPlan'] = obj({
    'observation_seconds':integer(minimum=60),
    'request_rate_query_id':string(),
    'error_ratio_query_id':string(),
    'sample_age_query_id':string(),
    'max_error_ratio':v3_num(),
    'min_request_rate':v3_num(),
}, ['observation_seconds','request_rate_query_id','error_ratio_query_id','sample_age_query_id',
    'max_error_ratio','min_request_rate'])
S['V3RollbackCandidate'] = obj({
    'revision':string(),
    'created_at':{'type':['string','null'],'format':'date-time'},
    'change_cause':{'type':['string','null']},
    'current':v3_bool(),
    'containers':arr(ref('V3RollbackContainer')),
    'changes':arr(ref('V3RollbackChange')),
    'template_digest':string(pattern='^[0-9a-f]{64}$'),
}, ['revision','created_at','change_cause','current','containers','changes','template_digest'])
S['V3RollbackCandidates'] = obj({
    'service_id':string(),'application_id':string(),'environment_id':string(),
    'workload':v3_nullable(obj({
        'connector_id':string(),'namespace':string(),'deployment':string(),'uid':string(),
        'paused':v3_bool(),'current_revision':string(),
        'current_template_digest':string(pattern='^[0-9a-f]{64}$'),
        'replicas':obj({'desired':integer(minimum=0),'updated':integer(minimum=0),
            'available':integer(minimum=0)},['desired','updated','available'])},
        ['connector_id','namespace','deployment','uid','paused','current_revision',
         'current_template_digest','replicas'])),
    'verification':v3_nullable(ref('V3RollbackVerificationPlan')),
    'candidates':arr(ref('V3RollbackCandidate')),
    'blocked_reason':{'type':['string','null'],'enum':[None,'workload_not_configured','rollback_not_allowed',
        'write_not_approved','deployment_paused','credential_unavailable','workload_unavailable',
        'executor_not_ready']},
    'observed_at':string(format='date-time'),
}, ['service_id','application_id','environment_id','workload','verification','candidates',
    'blocked_reason','observed_at'])

v3_route('get','/services/{id}/rollback-candidates','List revisions a Deployment can be rolled back to',
    response=ref('V3RollbackCandidates'),
    role='viewer with scoped resources.view',
    desc='Requires one selected environment equal to the service environment, current resources.view on the '
         'application or its owner team. Resolves the single workload target of the service in the source '
         'registry and reads the Deployment and its ReplicaSets with the connector read credential. Candidates are '
         'the ReplicaSet revisions the Deployment still owns, newest first, each with its differences from the '
         'current Pod template; the current revision is listed with current true and is not proposable. A missing '
         'target, a disallowed rollback, a missing write approval or a paused Deployment are reported in '
         'blocked_reason with an empty candidate list; an unreadable cluster is 503 workload_unavailable. No '
         'expression, token or Secret value is returned (PA-08).')

S['V3RollbackProposalCreate'] = obj({
    'investigation_id':string(),
    'service_id':string(),
    'target_revision':string(),
    'expected_template_digest':string(pattern='^[0-9a-f]{64}$',
        description='current_template_digest from GET rollback-candidates; a change since then is 409'),
    'reason':string(minLength=1,maxLength=2000),
}, ['investigation_id','service_id','target_revision','expected_template_digest','reason'])
v3_route('post','/rollback-proposals','Propose a Deployment rollback for two-person approval',
    body=ref('V3RollbackProposalCreate'),
    response=obj({'proposal_id':string(),'status':string(enum=['pending']),
        'action_kind':string(enum=['kubernetes.deployment.rollback'])},['proposal_id','status','action_kind']),
    status=201,
    role='operator with scoped resources.change and proposals.change',
    desc='Requires a human session, one selected environment equal to the service environment, an Idempotency-Key, '
         'and current resources.view, resources.change and proposals.change on the application or its owner team; '
         'the investigation must belong to the same application and environment. The server re-reads the '
         'Deployment, requires the expected current template, a candidate revision other than the current one, a '
         'catalog scope that allows rollback and an approved write binding, then stores an immutable action snapshot '
         'and the review content in one transaction with an audit row. Approval is the existing two-person passkey '
         'flow. Replaying the same key and body returns 200 with the same proposal; a different body is 409. '
         '409 rollback_target_changed when the Deployment changed since the candidates were read.')
P['/rollback-proposals']['post'].setdefault('parameters', []).append(
    {'name':'Idempotency-Key','in':'header','required':True,'schema':string(minLength=8,maxLength=128)})

# The proposal detail names its action and, for a rollback, its structured
# summary. action_kind is null for display-only proposals.
S['V3ProposalRollback'] = obj({
    'service_id':string(),'application_id':string(),'environment_id':string(),
    'namespace':string(),'deployment':string(),
    'from_revision':string(),'target_revision':string(),
    'containers':arr(ref('V3RollbackContainer')),
    'changes':arr(ref('V3RollbackChange')),
    'verification':ref('V3RollbackVerificationPlan'),
    'reason':string(),
}, ['service_id','application_id','environment_id','namespace','deployment','from_revision',
    'target_revision','containers','changes','verification','reason'])
S['V3Proposal']['properties']['action_kind'] = {'type':['string','null']}
S['V3Proposal']['properties']['rollback'] = v3_nullable(ref('V3ProposalRollback'))
S['V3Proposal']['required'] += ['action_kind','rollback']

# GET /executions/{id} also projects V3 rollback executions. The two variants
# are told apart by kind, which the legacy projection does not carry.
S['V3RolloutVerificationSample'] = obj({
    'at':string(format='date-time'),
    'request_rate':{'type':['number','null']},
    'error_ratio':{'type':['number','null']},
    'sample_age_seconds':{'type':['number','null']},
    'ok':v3_bool(),
}, ['at','request_rate','error_ratio','sample_age_seconds','ok'])
S['V3RolloutExecutionDetail'] = obj({
    'id':string(),
    'kind':string(enum=['kubernetes.deployment.rollback']),
    'proposal_id':string(),'investigation_id':string(),'application_id':string(),'environment_id':string(),
    'status':string(enum=['reserved','dispatching','unknown','applied','rolling_out','verifying',
        'succeeded','failed','needs_attention']),
    'status_reason':{'type':['string','null'],'enum':[None,'drift','approval_invalid','authorization_changed',
        'binding_changed','write_forbidden','write_unavailable','reconcile_unknown','rollout_timeout',
        'progress_deadline_exceeded','verification_failed','verification_unavailable','target_changed']},
    'approved_at':string(format='date-time'),
    'approvers':arr(obj({'name':string(),'verified_at':string(format='date-time')},['name','verified_at'])),
    'created_at':string(format='date-time'),'updated_at':string(format='date-time'),
    'target':obj({'namespace':string(),'deployment':string(),'from_revision':string(),
        'target_revision':string()},['namespace','deployment','from_revision','target_revision']),
    'timeline':arr(obj({'state':string(),'at':string(format='date-time')},['state','at'])),
    'rollout':v3_nullable(obj({'desired':integer(minimum=0),'updated':integer(minimum=0),
        'available':integer(minimum=0),'current_revision':{'type':['string','null']},
        'observed_at':string(format='date-time')},
        ['desired','updated','available','current_revision','observed_at'])),
    'verification':v3_nullable(obj({
        'plan':ref('V3RollbackVerificationPlan'),
        'started_at':{'type':['string','null'],'format':'date-time'},
        'finished_at':{'type':['string','null'],'format':'date-time'},
        'result':string(enum=['pending','passed','failed','unavailable']),
        'samples':arr(ref('V3RolloutVerificationSample'))},
        ['plan','started_at','finished_at','result','samples'])),
}, ['id','kind','proposal_id','investigation_id','application_id','environment_id','status','status_reason',
    'approved_at','approvers','created_at','updated_at','target','timeline','rollout','verification'])
P['/executions/{id}']['get']['responses']['200']['content']['application/json']['schema'] = {
    'oneOf':[ref('V3ExecutionDetail'),ref('V3RolloutExecutionDetail')]}
P['/executions/{id}']['get']['description'] += (' A V3 rollback execution (kind kubernetes.deployment.rollback) is '
    'projected from the rollout ledger with its timeline, rollout readback and verification samples (PA-08).')
