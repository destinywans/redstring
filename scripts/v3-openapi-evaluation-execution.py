"""WAN-241 bounded human-case and two-version evaluation execution.

Run after v3-openapi-agents.py; main generator registration belongs to root.
"""
S['V3EvalHumanInput'] = obj({
    'agent_id': string(maxLength=128), 'title': string(maxLength=240),
    'input': string(minLength=1, maxLength=8192), 'queue': string(maxLength=240),
    'alert': obj({'sev': string(enum=['P1','P2','P3','P4']),
        'title': string(maxLength=500),
        'labels': arr(obj({'k':string(maxLength=160),'v':string(maxLength=1000)}, ['k','v']))}, ['sev','title','labels']),
    'evidence': arr(obj({'brand':string(maxLength=80),'title':string(maxLength=500),
        'sub':string(maxLength=2000)}, ['brand','title','sub'])),
    'expected': obj({'cause':string(maxLength=20000),'advice':string(maxLength=20000)}, ['cause','advice']),
    'checks': {'type':'array','minItems':1,'maxItems':16,'uniqueItems':True,'items':string(minLength=1,maxLength=500)},
}, ['agent_id','title','input','queue','alert','evidence','expected','checks'])
S['V3EvalExecution'] = obj({
    'id':string(), 'case_id':string(),
    'status':string(enum=['running','succeeded','failed','cancelled','ambiguous']),
    'error_code':string(), 'material':{'type':'object','additionalProperties':True},
    'material_sha256':string(), 'results':{'oneOf':[{'type':'array','items':{'type':'object','additionalProperties':True}},{'type':'null'}]},
    'snapshot_id':string(), 'started_sides':integer(minimum=0,maximum=2),
    'created_at':string(), 'deadline':string(), 'finished_at':{'oneOf':[string(),{'type':'null'}]},
    'evaluation_kind':string(enum=['single_case_literal_checks']),
    'human_labels_modified':{'type':'boolean','enum':[False]}, 'usage_source':string(enum=['unknown']),
}, ['id','case_id','status','error_code','material','material_sha256','results','snapshot_id',
    'started_sides','created_at','deadline','finished_at','evaluation_kind','human_labels_modified','usage_source'])
v3_route('post','/eval-cases','Record a human-supplied runnable evaluation case',
    role='human with scoped agents.change',body=ref('V3EvalHumanInput'),
    response=obj({**fieldnames('id input_id input_sha256 material_sha256'),
        'source':string(enum=['human_supplied']), 'execution_state':string(enum=['not_run'])},
        ['id','input_id','input_sha256','material_sha256','source','execution_state']),
    desc='Atomically stores a case, frozen source material and versioned Prompt Trial input. No model call or human correctness label is created. Input is bounded by UTF-8 bytes; literal checks are case-sensitive.')
v3_route('post','/evals/{id}/execute','Execute one case against two frozen Agent versions',
    role='human with scoped agents.change',
    body=obj({'case_id':string(maxLength=128),'request_key':string(minLength=8,maxLength=128)},['case_id','request_key']),
    response=ref('V3EvalExecution'),
    desc='Requires two compatible tool-free execution configs and the configured model runtime. Each side makes at most one model call; no tools, at most 30 seconds per side and a 70-second persisted deadline. Same actor/key/case replays never call the provider twice. passed means all explicit literal checks matched, not semantic quality or a human label. Errors/cancellation/ambiguous completion never produce a successful report.')
v3_route('get','/evals/{id}/execution','Read exact evaluation material, outputs and terminal state',
    role='human with scoped agents.view', response=ref('V3EvalExecution'),
    desc='Private no-store read with current session and scope. Expired running executions are terminated as ambiguous/invalid without retry. Output usage is unknown; failed or cancelled execution has no result snapshot.')
v3_route('post','/evals/{id}/cancel','Cancel a running bounded evaluation',
    role='human with scoped agents.change',
    response=obj({'id':string(),'status':string(enum=['cancelled']),
        'provider_outcome':string(enum=['may_have_been_billed'])},['id','status','provider_outcome']),
    desc='Terminates the execution and invalidates the experiment. Already dispatched calls may have been accepted or billed upstream; no successful evaluation result is persisted.')
for _path,_method in [('/eval-cases','post'),('/evals/{id}/execute','post'),('/evals/{id}/execution','get'),('/evals/{id}/cancel','post')]:
    for _status,_text in [('409','Execution/key/state conflict'),('422','Invalid case or incompatible tool-free model configuration'),('503','Runtime, storage or policy unavailable')]:
        P[_path][_method]['responses'][_status]={'description':_text,'content':{'application/json':{'schema':ref('Error')}}}
P['/evals/{id}/execute']['post']['responses']['502']={'description':'Execution outcome was not confirmed; inspect saved execution without retrying provider calls','content':{'application/json':{'schema':ref('Error')}}}

P['/evals']['post']['description'] = ('Creates pending metadata only. A current human session is required. '
    'execution_available reflects registered runtime and compatible tool-free version configs; '
    'the selected case is admitted only by the separate execute request.')
_eval_nullable_object = {'oneOf':[{'type':'object','additionalProperties':True},{'type':'null'}]}
_eval_nullable_array = {'oneOf':[arr({'type':'object','additionalProperties':True}),{'type':'null'}]}
v3_route('get','/evals/{id}','Read evaluation metadata and explicit single-case membership',
    response=obj({**fieldnames('id title meta status baseline_version candidate_version created_at'),
        'dataset':{'type':'null'}, 'metrics':_eval_nullable_object, 'radar':{'type':'null'},
        'cases':_eval_nullable_array,'datasets':{'type':'null'},'evaluators':{'type':'null'},
        'result_ref':{'oneOf':[string(),{'type':'null'}]}, 'finished_at':{'oneOf':[string(),{'type':'null'}]},
        'execution':_eval_nullable_object,'missing':arr(string()),'partial':v3_bool()},
        ['id','title','meta','status','baseline_version','candidate_version','created_at','dataset','metrics',
         'radar','cases','datasets','evaluators','result_ref','finished_at','execution','missing','partial']),
    desc='Current authorized persisted experiment; execution, when present, identifies its exact single case and result endpoint. Dataset, radar and evaluator catalog remain partial. A passed execution means explicit literal checks matched, never a human correctness label.')
import copy as _eval_copy
_eval_human_case = _eval_copy.deepcopy(_eval_case_partial)
_eval_human_case['properties'].update({
    'alert':S['V3EvalHumanInput']['properties']['alert'],
    'evidence':S['V3EvalHumanInput']['properties']['evidence'],
    'expected':S['V3EvalHumanInput']['properties']['expected'],
    'source_state':string(enum=['human_supplied_not_run']),
})
for _variant in [_eval_case_complete,_eval_case_partial,_eval_human_case]:
    _variant['properties'].update({'input_available':v3_bool(),'input_id':string(),
        'human_input':{'oneOf':[ref('V3EvalHumanInput'),{'type':'null'}]},
        'revision':integer(minimum=0),'labels_truncated':v3_bool()})
    _variant['required'] += ['input_available','input_id','human_input','revision','labels_truncated']
v3_route('get','/eval-cases/{id}','Read human case inputs and any executed snapshot',
    response={'oneOf':[_eval_case_complete,_eval_case_partial,_eval_human_case]},
    desc='Frozen human inputs are reviewable before execution with source human_supplied_not_run and partial=true. Only a stored two-version result produces a complete case report. Labels retain their individual snapshot IDs and are never written automatically.')
