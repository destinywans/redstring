"""Persisted V3 execution detail and local report publication.

WAN-117 loads this fragment after the legacy Execution schema and route.
"""

S['V3ExecutionApproval'] = obj({
    'decision':string(enum=['pending','approved','rejected','revoked']),
    'approved_by':{'type':['string','null']},
    'digest_match':v3_bool(),
}, ['decision','approved_by','digest_match'])
S['V3ExecutionStep'] = obj({
    'seq':integer(minimum=1),
    'type':string(),
    'time':string(format='date-time'),
}, ['seq','type','time'])
S['V3ExecutionObservation'] = obj({
    'recovered':{'type':['boolean','null']},
    'started_at':{'type':['string','null'],'format':'date-time'},
    'finished_at':{'type':['string','null'],'format':'date-time'},
    'samples':{'type':['integer','null'],'minimum':0},
    'requests':{'type':['integer','null'],'minimum':0},
    'reason':{'type':['string','null']},
    **{name:{'type':'null'} for name in
       ['values','threshold','labels','band','marker','checks']},
}, ['recovered','started_at','finished_at','samples','requests','reason',
    'values','threshold','labels','band','marker','checks'])
S['V3ExecutionReport'] = obj({
    **fieldnames('title summary closed_by approved_by'),
    'closed_at':string(format='date-time'),
    'published':v3_bool(),
    'published_by':{'type':['string','null']},
    'published_at':{'type':['string','null'],'format':'date-time'},
    **{name:{'type':'null'} for name in
       ['severity','impact','timeline','actions','refs']},
}, ['title','summary','closed_by','closed_at','approved_by','published',
    'published_by','published_at','severity','impact','timeline','actions','refs'])
S['V3ExecutionDetail'] = obj({
    **fieldnames('id plan_id investigation_id application_id environment_id error status_source'),
    'proposal_id':{'type':'null'},
    'status':string(enum=['queued','executing','verifying','succeeded',
                          'failed','needs_attention']),
    'attempt':integer(minimum=0),
    'created_at':string(format='date-time'),
    'updated_at':string(format='date-time'),
    'approval':ref('V3ExecutionApproval'),
    'steps':arr(ref('V3ExecutionStep')),
    'steps_truncated':v3_bool(),
    'observation':{'oneOf':[{'type':'null'},ref('V3ExecutionObservation')]},
    'report':{'oneOf':[{'type':'null'},ref('V3ExecutionReport')]},
    'partial':v3_bool(),
    'missing':arr(string()),
}, ['id','plan_id','proposal_id','investigation_id','application_id',
    'environment_id','status','status_source','attempt','error','created_at',
    'updated_at','approval','steps','steps_truncated','observation','report',
    'partial','missing'])

v3_route('get','/executions/{id}','Read persisted execution and recovery evidence',
    response=ref('V3ExecutionDetail'),
    role='viewer with scoped proposals.view',
    desc='Reads the legacy execution, its approved action plan and investigation, up to 100 persisted events, optional recovery summary and optional closed report. Current application, environment and owner-team grants are checked. status_source distinguishes the authoritative legacy status from any missing derived evidence. proposal_id is null because no V3 proposal link exists. Steps contain persisted event sequence, type and timestamp only; an editorial timeline is not fabricated. Chart arrays, thresholds, report severity and action items have no producer and are null with missing reasons. A succeeded status is the legacy executor result and is not by itself proof that a report exists or is published. The frozen frontend Execution DTO expects populated mock fields and requires a null-aware adapter.')

S['V3ExecutionPublication'] = obj({
    'id':string(),
    'status':string(enum=['published']),
    'destination':string(enum=['local']),
    'published_at':string(format='date-time'),
    'published_by':string(),
    'report_digest':string(),
}, ['id','status','destination','published_at','published_by','report_digest'])
v3_route('post','/executions/{id}/report/publish',
    'Publish an approved completed execution report to the local ledger',
    response=ref('V3ExecutionPublication'),
    role='human with scoped proposals.view and knowledge.manage',
    desc='Requires one selected environment, a current human Cookie session, active application and environment, current team or application grants, an approved exact plan digest, a succeeded execution with persisted verified recovery, and a closed report matching the execution and investigation events. The local publication record and audit commit together. A repeat with unchanged report digest returns the original publication; changed report content or missing prerequisites return conflict. This endpoint performs no infrastructure write, notification or external postmortem publication. The current mock POST response {ok:true} needs an adapter.')

S['V3AlertRuleExecutionResult'] = obj({
    'proposal_id':string(),
    'execution_id':string(),
    'status':string(enum=['pending_review','merged','awaiting_provider','enabled','failed','waiting_sync']),
    'deployment':obj({'project_id':integer(),'mr_iid':integer(minimum=1),'url':string(),'source_branch':string(),
        'target_branch':string(),'path':string(),'content_digest':string(),'state':string(enum=['opened','merged']),
        'source_revision':string(),'merge_revision':string(),'merged_at':string(format='date-time')},
        ['project_id','mr_iid','url','source_branch','target_branch','path','content_digest','state']),
    'observed_rule_id':string(),
}, ['proposal_id','execution_id','status'])
v3_route('post','/executions/{id}/recheck',
    'Read an alert rule execution back now',
    response=ref('V3AlertRuleExecutionResult'),
    role='alert_rules.change on the execution\'s application',
    desc='D11 section 5.4. id is an alert rule enable execution ID. Requires one selected environment and alert_rules.change on the '
         'proposal\'s application (or its owner team). Runs the same read-only observation the executor worker runs every 15 seconds, also '
         'after the worker\'s 24-hour window: GitLab and Prometheus are only read, nothing is merged or written remotely, and only the local '
         'ledger records the readback. A late Prometheus load becomes enabled and clears load_timeout; an evaluation error becomes failed. '
         'Only pending_review, merged, awaiting_provider and enabled executions can be rechecked (409 execution_not_observable otherwise); '
         'an enabled rule that no longer matches is 409 rule_content_changed. deployment and observed_rule_id are omitted until known. '
         'An unknown execution or one in another environment is 404.')

for _path, _method, _errors in [
    ('/executions/{id}','get', {
        '403':'Selected environment access was denied',
        '404':'Execution is unknown or its object-scoped proposals.view grant is missing',
        '503':'Execution source or policy is unavailable',
    }),
    ('/executions/{id}/recheck','post', {
        '403':'alert_rules.change on the execution\'s application is required',
        '404':'The execution is unknown or belongs to another environment',
        '409':'The execution is not observable, or an enabled rule no longer matches its approval',
        '503':'The executor, GitLab or Prometheus could not be read',
    }),
    ('/executions/{id}/report/publish','post', {
        '401':'The current Cookie session was revoked or expired',
        '403':'A current human identity and selected environment access are required',
        '404':'Execution is unknown or an object-scoped proposals.view or knowledge.manage grant is missing',
        '409':'Execution is not eligible, required evidence is absent, or the published report digest changed',
        '503':'Publication storage or policy is unavailable',
    }),
]:
    for _code, _description in _errors.items():
        P[_path][_method]['responses'][_code] = {
            'description':_description,
            'content':{'application/json':{'schema':ref('Error')}}}
