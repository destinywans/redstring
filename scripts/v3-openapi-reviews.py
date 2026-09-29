# V3 review records are separate from historical rollback action plans. A
# trusted in-process producer must create one with complete sourced evidence;
# approval and execution remain unavailable until passkey-bound authorization.
S['V3ProposalComment'] = obj({'author':string(),'ai':v3_bool(),
    'time':string(format='date-time'),'text':string()},['author','time','text'])
S['V3Proposal'] = obj({**fieldnames('id title impact proposed_by investigation_id target method verification_note requirement'),
    'status':string(enum=['pending','approved','rejected','executed']), 'risk':string(enum=['low','medium','high']),
    'reversible':v3_bool(),'proposed_at':string(format='date-time'),
    'diff':obj({'path':string(),'lines':arr(string())},['path','lines']),
    'evidence':arr(obj({'n':integer(),'icon':string(),'title':string(),'desc':string(),
        'series':arr(v3_num())},['n','icon','title','desc'])),
    'verification':arr(string()),'checklist':arr(string()),
    'discussion':arr(ref('V3ProposalComment'))},
    ['id','title','status','risk','impact','reversible','proposed_by','proposed_at',
     'investigation_id','target','diff','method','evidence','verification',
     'verification_note','checklist','requirement','discussion'])
v3_route('get','/proposals/{id}','Read a scoped V3 proposal review record',response=ref('V3Proposal'),
    desc='Only records created by a trusted V3 producer can appear; existing rollback action_plans are not relabeled.')
v3_route('post','/proposals/{id}/comments','Append an audited proposal comment',
    body=obj({'text':string()},['text']),response=ref('V3ProposalComment'),status=201,
    query=[{'name':'Idempotency-Key','in':'header','schema':string(maxLength=128)}],
    desc='Only pending proposals may be discussed. Idempotency-Key is optional and scoped to the actor/proposal.')
P['/proposals/{id}/comments']['post']['responses']['200'] = P['/proposals/{id}/comments']['post']['responses']['201']
v3_route('post','/proposals/{id}/reject','Reject a pending proposal without executing it',
    body=obj({'comment':string(),'note':string()}),response=ref('V3Proposal'),
    desc='Serializes state changes, audits rejection, and returns the persisted review record.')
