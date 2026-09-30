# Independent WAN-118 product silence fragment. WAN-117 integrates this after
# v3-openapi-alerts.py when updating the aggregate OpenAPI document.
S['V3SilenceProductMatcher'] = obj({
    'name': string(pattern='^[a-zA-Z_][a-zA-Z0-9_]*$'),
    'op': string(enum=['=', '!=', '=~', '!~']),
    'value': string(minLength=1, maxLength=256,
                    description='Maximum 256 UTF-8 bytes; the server enforces the byte limit.')},
    ['name', 'op', 'value'])
S['V3SilenceProductCreateRequest'] = obj({
    'investigation_id': string(minLength=1, maxLength=128,
                               description='Maximum 128 UTF-8 bytes.'),
    'matchers': {'type': 'array', 'minItems': 1, 'maxItems': 20,
                 'items': ref('V3SilenceProductMatcher')},
    'duration_seconds': integer(minimum=1800, maximum=86400),
    'comment': string(minLength=1, maxLength=1000,
                      description='Maximum 1000 UTF-8 bytes.')},
    ['investigation_id', 'matchers', 'duration_seconds', 'comment'])
S['V3SilenceProductCancelRequest'] = obj({
    'investigation_id': string(minLength=1, maxLength=128,
                               description='Maximum 128 UTF-8 bytes.'),
    'reason': string(minLength=1, maxLength=1000,
                     description='Maximum 1000 UTF-8 bytes.')},
    ['investigation_id', 'reason'])
S['V3SilenceProductProposalReceipt'] = obj({
    'proposal_id': string(minLength=1),
    'status': string(enum=['pending_review']),
    'action_digest': string(pattern='^[0-9a-f]{64}$'),
    'effect': string(enum=['no_upstream_write']),
    'upstream_id': {'type': 'null'},
    'execution_status': {'type': 'null'}},
    ['proposal_id', 'status', 'action_digest', 'effect', 'upstream_id', 'execution_status'])

v3_route('post', '/silences', 'Propose a bounded Alertmanager silence',
    role='operator', body=ref('V3SilenceProductCreateRequest'),
    response=ref('V3SilenceProductProposalReceipt'), status=202,
    desc='Requires one selected environment, an open investigation in the exact active application, current human session, silences.manage and proposals.change. Matchers must contain exactly one exact application equality; the server adds the exact environment matcher. Idempotency-Key is scoped to the human actor. An immutable digest-bound pending proposal and audit are committed in one transaction. This request does not reserve or create an upstream silence. The approval and independent executor must be configured for this environment, otherwise 501 not_connected is returned.')
P['/silences']['post']['parameters'].append({
    'name': 'Idempotency-Key', 'in': 'header', 'required': True,
    'schema': string(minLength=8, maxLength=128, description='The bulk- prefix is reserved for internal alert-group proposals.')})
P['/silences']['post']['responses']['501'] = {
    'description': 'Approval or independent execution workflow is not connected',
    'content': {'application/json': {'schema': ref('Error')}}}

v3_route('delete', '/silences/{id}', 'Propose cancellation of one owned silence',
    role='operator', body=ref('V3SilenceProductCancelRequest'),
    response=ref('V3SilenceProductProposalReceipt'), status=202,
    desc='The path ID is an Alertmanager UUID, but ID alone grants no cancellation authority. Requires a confirmed AIOps create operation for that exact ID, current application/environment ownership, an exact operation annotation and full payload match in a fresh complete Alertmanager read, an open investigation, current human session, silences.view/manage and proposals.change. Stores immutable evidence, pending proposal and audit atomically. The handler never expires the upstream silence. A separate approved executor must revalidate everything before the write. Missing approval/executor integration returns 501 not_connected.')
P['/silences/{id}']['delete']['parameters'].append({
    'name': 'Idempotency-Key', 'in': 'header', 'required': True,
    'schema': string(minLength=8, maxLength=128, description='The bulk- prefix is reserved for internal alert-group proposals.')})
P['/silences/{id}']['delete']['responses']['501'] = {
    'description': 'Approval or independent execution workflow is not connected',
    'content': {'application/json': {'schema': ref('Error')}}}
