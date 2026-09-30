# Domain override for persisted evaluation detail and human labels. WAN-117
# must load this fragment after v3-openapi-agents.py in v3-openapi.py.
S['V3EvalDetailEvidence'] = obj({
    **fieldnames('id title meta baseline_version candidate_version created_at'),
    'status':string(enum=['pending','running','passed','failed','invalid']),
    'dataset':{'type':'null'},
    'metrics':{'type':['object','null'],'additionalProperties':{'type':'number'}},
    'radar':{'type':'null'},
    'cases':{'type':'null'},
    'datasets':{'type':'null'},
    'evaluators':{'type':'null'},
    'result_ref':{'type':['string','null']},
    'finished_at':{'type':['string','null'],'format':'date-time'},
    'missing':arr(string()),
    'partial':v3_bool(),
}, ['id','title','meta','status','baseline_version','candidate_version',
    'created_at','dataset','metrics','radar','cases','datasets','evaluators',
    'result_ref','finished_at','missing','partial'])
v3_route('get','/evals/{id}','Read a persisted evaluation experiment',
    response=ref('V3EvalDetailEvidence'),
    desc='Returns the actual baseline/candidate versions and reported aggregate metrics only after a passed or failed outcome. Pending, running and invalid results have null metrics. There is no experiment-case membership, dataset, per-case output, evaluator catalog or radar producer, so those fields are null with explicit missing reasons; no Agent cases are guessed into an experiment. The immutable creation audit must match the current Agent application/environment and active scope. The active Cookie session and application or current owner-team grants are checked in the same transaction. This partial response cannot render the frozen evaluation page without a null-aware frontend adapter.')

for _case_schema in (_eval_case_complete, _eval_case_partial):
    _case_schema['properties']['revision'] = integer(minimum=0)
    _case_schema['properties']['labels_truncated'] = v3_bool()
_eval_case_required.extend(['revision', 'labels_truncated'])
P['/eval-cases/{id}']['get']['description'] += (' The committed label count is returned as revision '
    'for the next human label request; labels_truncated marks the 50-label display limit. '
    'Case origin is stored in immutable application/environment columns and a trusted '
    'snapshot is required for complete case content.')

v3_route('post','/eval-cases/{id}/labels','Append a human evaluation label',
    role='viewer with scoped agents.change',
    body=obj({'next':string(enum=['correct','incorrect','partial','unsure']),
        'note':string(maxLength=2000),
        'expected_revision':integer(minimum=0)},
        ['next','expected_revision']),
    response=obj({'next':string(enum=['correct','incorrect','partial','unsure']),
        'id':string(),'revision':integer(minimum=1)},
        ['next','id','revision']),
    desc='The next field is the selected label, not a next-case ID. Requires one selected environment, an active human Cookie session, matching case origin, active application/environment, current application or owner-team grant, a trusted case snapshot, and expected_revision from GET. The case row serializes concurrent labels; stale revisions return 409. Label, snapshot binding and audit commit together. The frozen case UI sends a different body and must be adapted before wiring.')
for _status, _description in {
    '401':'The current Cookie session was revoked or expired',
    '403':'A human identity with current scoped agents.change permission is required',
    '404':'Unknown or hidden case, including missing or changed origin',
    '409':'The expected case revision is stale; read the case again',
    '422':'Invalid label body or note',
    '428':'expected_revision is required',
    '503':'Storage or policy is unavailable',
}.items():
    P['/eval-cases/{id}/labels']['post']['responses'][_status] = {
        'description':_description,
        'content':{'application/json':{'schema':ref('Error')}}}
