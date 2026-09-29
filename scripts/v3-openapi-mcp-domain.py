# WAN-122 main MCP server routes. The aggregate generator executes this after
# v3-openapi-mcp.py so it can refine the existing discovery schema and routes.
def mcp_nullable(schema):
    return {'anyOf':[schema,{'type':'null'}]}

S['V3McpCapabilityObservation'] = obj({
    'state':string(enum=['supported','unsupported','unknown']),
    'count':mcp_nullable(integer(minimum=0,maximum=128))}, ['state','count'])
S['V3McpDiscoveryDomain'] = obj({
    'snapshot_id':string(), 'captured_at':string(format='date-time'),
    'trust':string(enum=['untrusted_observation']),
    'server_name':mcp_nullable(string()), 'server_version':mcp_nullable(string()),
    'protocol_version':mcp_nullable(string()),
    'tools':ref('V3McpCapabilityObservation'),
    'resources':ref('V3McpCapabilityObservation'),
    'prompts':ref('V3McpCapabilityObservation')},
    ['snapshot_id','captured_at','trust','server_name','server_version',
     'protocol_version','tools','resources','prompts'])
S['V3McpCredentialDomain'] = obj({
    'source':string(enum=['none','private_file']),
    'reference_registered':v3_bool(), 'current_validity':{'type':'null'},
    'vault_state':string(enum=['not_integrated'])},
    ['source','reference_registered','current_validity','vault_state'])
S['V3McpConnectionDomain'] = obj({
    'state':string(enum=['unknown','observed_success']),
    'last_success_at':mcp_nullable(string(format='date-time')),
    'health':{'type':'null'}}, ['state','last_success_at','health'])
S['V3McpReviewDomain'] = obj({
    'reviewed':v3_bool(),
    'allowed_read_only_count':mcp_nullable(integer(minimum=0,maximum=128)),
    'enabled_count':mcp_nullable(integer(minimum=0,maximum=128))},
    ['reviewed','allowed_read_only_count','enabled_count'])
S['V3McpSummaryDomain'] = obj({
    **fieldnames('id brand name desc transport caps last application_id environment_id binding_instance_id'),
    'trust':obj({'label':string(),'tone':string()},['label','tone']),
    'apps':arr(obj({'label':string(),'tone':string()})),
    'status':string(enum=['pending']),
    'owner_team_id':mcp_nullable(string()),
    'registration_version':integer(minimum=1),
    'credential':ref('V3McpCredentialDomain'),
    'connection':ref('V3McpConnectionDomain'),
    'discovery':mcp_nullable(ref('V3McpDiscoveryDomain')),
    'review':ref('V3McpReviewDomain'),
    'call_metrics':{'type':'null'}},
    ['id','brand','name','desc','transport','caps','last','application_id',
     'environment_id','binding_instance_id','trust','apps','status','owner_team_id',
     'registration_version','credential','connection','discovery','review','call_metrics'])
S['V3McpKpiDomain'] = obj({
    **fieldnames('id label sub'),
    'value':mcp_nullable(string()), 'series':arr(v3_num())},
    ['id','label','value','sub','series'])
S['V3McpListDomain'] = obj({
    'kpis':arr(ref('V3McpKpiDomain')),
    'items':arr(ref('V3McpSummaryDomain')),
    'complete_v3_dto':{'const':False},
    'unavailable_fields':arr(string())},
    ['kpis','items','complete_v3_dto','unavailable_fields'])
S['V3McpObservedToolDomain'] = obj({
    **fieldnames('name desc'),
    'read_only_hint':v3_bool(),
    'decision':mcp_nullable(string(enum=['allow_read_only','deny'])),
    'enabled':v3_bool(),
    'revision':mcp_nullable(integer(minimum=1)),
    'trust':string(enum=['untrusted_observation']),
    'calls':{'type':'null'}},
    ['name','desc','read_only_hint','decision','enabled','revision','trust','calls'])
S['V3McpResourceDomain'] = obj(fieldnames('uri name description'),['uri','name','description'])
S['V3McpPromptDomain'] = obj(fieldnames('name description'),['name','description'])
S['V3McpTrustInfoDomain'] = obj({
    **fieldnames('readonly_check limit auth'),
    'scope':arr(string())}, ['readonly_check','scope','limit','auth'])
S['V3McpDetailDomain'] = obj({
    **S['V3McpSummaryDomain']['properties'],
    'version':mcp_nullable(string()),
    'counts':obj({k:mcp_nullable(integer(minimum=0,maximum=128))
                  for k in ['tools','resources','prompts']},
                 ['tools','resources','prompts']),
    'tools':mcp_nullable(arr(ref('V3McpObservedToolDomain'))),
    'resources':mcp_nullable(arr(ref('V3McpResourceDomain'))),
    'prompts':mcp_nullable(arr(ref('V3McpPromptDomain'))),
    'calls':{'type':'null'},
    'review_candidate':{'type':'null'},
    'trust_info':ref('V3McpTrustInfoDomain'),
    'complete_v3_dto':{'const':False},
    'unavailable_fields':arr(string())},
    S['V3McpSummaryDomain']['required']+['version','counts','tools','resources','prompts',
    'calls','review_candidate','trust_info','complete_v3_dto','unavailable_fields'])

# The auxiliary discovery route now persists the complete advertised catalog
# from one bounded initialize session, not only tools/list.
S['V3McpDiscoveryObservation']['properties'].update({
    'tools_state':string(enum=['supported','unsupported']),
    'resources_state':string(enum=['supported','unsupported','unknown']),
    'prompts_state':string(enum=['supported','unsupported','unknown'])})
S['V3McpDiscoveryObservation']['required'].extend(
    ['tools_state','resources_state','prompts_state'])
P['/mcp-servers/{id}/discover']['post']['description'] = (
    'After current single-environment connectors.manage authorization, run a bounded '
    'Streamable HTTP initialize and each advertised tools/list, resources/list and prompts/list. '
    'List failure aborts the entire discovery; unsupported and observed empty differ. '
    'Recheck source, session and grants after network I/O, then persist one immutable untrusted '
    'catalog snapshot and redacted audit. No tool execution, resource read, prompt retrieval, '
    'current health assertion or Agent grant is implied.')

v3_route('get','/mcp-servers',
    'List current authorized MCP environment bindings and sourced observations',
    response=ref('V3McpListDomain'),
    desc='One item per explicitly bound environment, with current application or owner-team connectors.view authorization. A compatible immutable discovery supplies observed initialize version and capability counts; unsupported and undiscovered counts are null. Human review and enabled preferences are separate from untrusted declarations and do not grant Agent execution. V3 call metrics and current connection health have no producer and remain unknown. At most 2000 candidates and 1000 visible bindings; excess fails with 413. Frozen UI uses an ID-only row key and cannot distinguish duplicate registration IDs across environments.')
P['/mcp-servers']['get']['responses']['413'] = {
    'description':'Candidate scan or visible binding limit exceeded',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/mcp-servers/{id}',
    'Read one current authorized MCP binding, discovery and reviewed tools',
    response=ref('V3McpDetailDomain'),
    desc='Requires one selected environment; all is rejected with 422. The response is based on a current binding, exact registration version/hash and latest immutable discovery. Remote tool descriptions/read-only hints, resources and prompts remain untrusted observations. The last successful discovery does not attest current health. The credential source is a private file, not V3 Vault, and no reference or secret is returned. Tool hashes and write revision tokens are available only through the manage-only review-candidate route. No V3 Agent MCP execution or attributable call-history producer exists; unknown counts and calls are null. The frozen UI expects fabricated non-null counts and cannot yet render this truthful DTO.')

S['V3McpMainReviewRequest'] = obj({
    'snapshot_id':string(), 'registration_version':integer(minimum=1),
    'registration_hash':string(), 'tools_hash':string(),
    'decisions':arr(ref('V3McpToolReviewDecision'))},
    ['snapshot_id','registration_version','registration_hash','tools_hash','decisions'])
S['V3McpMainReviewResult'] = obj({
    'snapshot_id':string(), 'reviewed':{'const':True},
    'tool_count':integer(minimum=0,maximum=128),
    'allowed_read_only_count':integer(minimum=0),
    'enabled_count':{'const':0}, 'execution_available':{'const':False}},
    ['snapshot_id','reviewed','tool_count','allowed_read_only_count','enabled_count',
     'execution_available'])
v3_route('post','/mcp-servers/{id}/review',
    'Review every tool of the exact current MCP discovery version',
    body=ref('V3McpMainReviewRequest'), response=ref('V3McpMainReviewResult'),
    desc='Current human connectors.manage reviewer only, in one selected environment. Exact snapshot, registration version/hash, tools hash and every name/tool hash decision are required; an empty request from the frozen UI is rejected. Reuses the version-bound review transaction, source/session/grant recheck and atomic redacted audit. No Agent execution is enabled.')
S['V3McpMainToolChangeRequest'] = obj({
    'snapshot_id':string(), 'registration_version':integer(minimum=1),
    'registration_hash':string(), 'tools_hash':string(), 'tool_hash':string(),
    'expected_revision':integer(minimum=1), 'enabled':v3_bool()},
    ['snapshot_id','registration_version','registration_hash','tools_hash',
     'tool_hash','expected_revision','enabled'])
S['V3McpMainToolChangeResult'] = obj({
    'snapshot_id':string(), 'tool_name':string(), 'enabled':v3_bool(),
    'revision':integer(minimum=1), 'changed':v3_bool(),
    'execution_available':{'const':False}},
    ['snapshot_id','tool_name','enabled','revision','changed','execution_available'])
v3_route('patch','/mcp-servers/{id}/tools/{name}',
    'Change one reviewed MCP tool switch with a current revision',
    body=ref('V3McpMainToolChangeRequest'), response=ref('V3McpMainToolChangeResult'),
    desc='Requires current human connectors.manage in one selected environment, exact registration/snapshot/tools/tool hashes and expected revision. Missing version fields from the frozen UI are rejected. Denied tools cannot be enabled; a remote read-only hint alone is not approval. Review state and redacted audit update atomically; enabled preference does not grant Agent execution.')
