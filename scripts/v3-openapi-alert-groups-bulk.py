"""Persisted, scoped alert-group bulk actions (WAN-118).

The aggregate builder loads this fragment after the V3 alert fragment.
"""

S['V3AlertGroupBulkRequest'] = obj({
    'ids': {'type':'array', 'minItems':1, 'maxItems':20, 'uniqueItems':True,
            'items':string(pattern='^group:[0-9a-f]{24}$')},
    'action':string(enum=['claim','silence','investigate']),
}, ['ids','action'])
S['V3AlertGroupBulkItem'] = obj({
    'id':string(pattern='^group:[0-9a-f]{24}$'),
    'outcome':string(enum=['claimed','already_claimed','investigating',
                           'already_investigating',
                           'not_found','conflict','not_connected',
                           'investigation_required','group_too_large',
                           'session_revoked','human_required','unavailable']),
    'matched':integer(minimum=0),
    'changed':integer(minimum=0),
    'investigation_id':{'type':['string','null']},
    'proposal_id':{'type':['string','null']},
    'error':{'type':['string','null']},
}, ['id','outcome','matched','changed','investigation_id','proposal_id','error'])
S['V3AlertGroupBulkResult'] = obj({
    'action':string(enum=['claim','silence','investigate']),
    'results':{'type':'array','minItems':1,'maxItems':20,
               'items':ref('V3AlertGroupBulkItem')},
}, ['action','results'])

v3_route('post','/alert-groups/bulk','Apply bounded actions to persisted alert groups',
    role='operator with current application or owner-team grants',
    body=ref('V3AlertGroupBulkRequest'),response=ref('V3AlertGroupBulkResult'),
    desc='Requires exactly one selected environment. Each group ID is recomputed using the same persisted environment row ID, application ID, source ID and fingerprint as GET /alert-groups; the handler locks only that current group and checks its current application or owner-team grant. Investigate also requires canvas.change. Input order and one result per ID are preserved. A missing or hidden group returns not_found; per-group failures do not erase successful groups. Claim changes platform assignees only, with same-user retries idempotent. Investigate creates or reuses a real persisted investigation and root branch for the locked group snapshot, returning the real ID. Silence always returns item-level not_connected and creates no proposal because the available Alertmanager matcher cannot prove exact fingerprint scope. changed counts incident rows changed. A response with only successful item outcomes is 200; any per-item failure yields 207 with all item results.')
P['/alert-groups/bulk']['post']['responses']['207'] = {
    'description':'At least one item failed; inspect every ordered result',
    'content':{'application/json':{'schema':ref('V3AlertGroupBulkResult')}}}
for _parameter in P['/alert-groups/bulk']['post']['parameters']:
    if _parameter['name'] == 'X-AIOps-Environment':
        _parameter['required'] = True
        _parameter['description'] = 'One selected active environment key; all is rejected.'
for _code, _description in {
    '401':'Current session is unavailable',
    '403':'Selected environment access is denied',
    '409':'Multiple or aggregate environments cannot be used for a bulk write',
    '422':'Malformed, duplicate, unsupported, or over-limit batch input',
    '503':'Shared persistence or authorization is unavailable before item results can be produced',
}.items():
    P['/alert-groups/bulk']['post']['responses'][_code] = {
        'description':_description,
        'content':{'application/json':{'schema':ref('Error')}}}
