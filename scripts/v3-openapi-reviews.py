# V3 review records are separate from historical rollback action plans. A
# trusted in-process producer supplies immutable action and review evidence.
S['V3ProposalComment'] = obj({'author':string(),'ai':v3_bool(),
    'time':string(format='date-time'),'text':string()},['author','time','text'])
S['V3Proposal'] = obj({**fieldnames('id title impact proposed_by investigation_id target method verification_note requirement'),
    'status':string(enum=['pending','approved','rejected','executed']), 'risk':string(enum=['low','medium','high']),
    'reversible':v3_bool(),'proposed_at':string(format='date-time'),
    'diff':obj({'path':string(),'lines':arr(string())},['path','lines']),
    'evidence':arr(obj({'n':integer(),'icon':string(),'title':string(),'desc':string(),
        'series':arr(v3_num())},['n','icon','title','desc'])),
    'verification':arr(string()),'checklist':arr(string()),
    'discussion':arr(ref('V3ProposalComment')),
    # PA-36: null until a second approval allocates an execution.
    'execution':v3_nullable(obj({'execution_id':string(),'approved_at':string(format='date-time'),
        'action_kind':string(),'status':{'type':['string','null']},'upstream_id':{'type':['string','null']},
        'unavailable':ref('V3Unavailable')},
        ['execution_id','approved_at','action_kind','status','upstream_id','unavailable']))},
    ['id','title','status','risk','impact','reversible','proposed_by','proposed_at',
     'investigation_id','target','diff','method','evidence','verification',
     'verification_note','checklist','requirement','discussion','execution'])
v3_route('get','/proposals/{id}','Read a scoped V3 proposal review record',response=ref('V3Proposal'),
    desc='Only records created by a trusted V3 producer can appear; existing rollback action_plans are not relabeled. execution is null until the second approval; then it carries the decision execution_id and approved_at, and status/upstream_id from the independent executor record (silence: reserved, dispatching, unknown, confirmed, expired, manual_review; alert rule: the enable execution state), null with not_generated until the executor has recorded them (PA-36).')
v3_route('post','/proposals/{id}/comments','Append an audited proposal comment',
    body=obj({'text':string()},['text']),response=ref('V3ProposalComment'),status=201,
    query=[{'name':'Idempotency-Key','in':'header','schema':string(maxLength=128)}],
    desc='Only pending proposals may be discussed. Idempotency-Key is optional and scoped to the actor/proposal.')
P['/proposals/{id}/comments']['post']['responses']['200'] = P['/proposals/{id}/comments']['post']['responses']['201']
v3_route('post','/proposals/{id}/reject','Reject a pending proposal without executing it',
    body=obj({'comment':string(),'note':string()}),response=ref('V3Proposal'),
    desc='Requires a human reviewer with proposals.manage for the proposal, or the passkey proposals.approve grant that covers its environment, application or owner team (PA-57). Serializes state changes, audits rejection, and returns the persisted review record.')
S['V3ProposalApprovalChallenge'] = obj({
    'challenge_id':string(),'options':{'type':'object'},
    'expires_at':string(format='date-time'),'proposal_id':string(),
    'action_digest':string(),'review_content_digest':string()},
    ['challenge_id','options','expires_at','proposal_id','action_digest','review_content_digest'])
S['V3ProposalApprovalResult'] = obj({
    'proposal_id':string(),'status':string(enum=['pending_review','approved']),
    'required_approvals':integer(minimum=2),'received_approvals':integer(minimum=0),
    'execution_id':string(),'approval_digest':string()},
    ['proposal_id','status','required_approvals','received_approvals'])
v3_route('post','/proposals/{id}/approve/options',
    'Begin a proposal-bound passkey approval assertion',
    body=obj({'confirm':v3_bool()},['confirm']),
    response=ref('V3ProposalApprovalChallenge'),status=201,
    desc='Requires a single environment, an active human session, a current exact-scope proposals.approve grant with passkey assurance, and an executable immutable proposal whose creator is another person. The five-minute WebAuthn challenge binds the proposal ID, action kind, action digest, review digest, environment, account, session, instance and RP. It grants no execution authority.')
v3_route('post','/proposals/{id}/approve',
    'Cast a fresh, proposal-bound human approval vote',
    body=obj({'challenge_id':string(),'credential':{'type':'object'},'comment':string()},
             ['challenge_id','credential']),
    response=ref('V3ProposalApprovalResult'),status=202,
    desc='Verifies and consumes one WebAuthn assertion in the proposal transaction, rechecks the current app, team, environment, human session and passkey-assured approval grant, and audits one immutable vote. A first valid vote returns 202 pending_review with no execution_id. A second distinct valid human other than the creator produces one durable approved decision and execution_id for an independent executor and returns 200. The 200 response is a historical decision and handoff ID, not proof that execution is currently permitted, claimed, or complete. An exact challenge and payload replay can return the original decision even after its proof expires or is revoked; the executor rechecks current evidence before claiming. Changed payload or a stale new vote returns 409. This handler never performs an upstream write.')
P['/proposals/{id}/approve']['post']['responses']['200'] = P['/proposals/{id}/approve']['post']['responses']['202']
