"""Governed read-only manual Agent execution and immutable version identity."""
_config = {
    'model_id': string(minLength=1,maxLength=160),
    'prompt_id': string(minLength=1,maxLength=128),
    'prompt_version': integer(minimum=1),
    'tool_names': arr(string(enum=['read_service_profile','read_service_metrics','read_service_logs','search_knowledge'])),
    'max_model_calls': integer(minimum=1,maximum=5),
    'max_tool_calls': integer(minimum=0,maximum=4),
    'max_input_bytes': integer(minimum=1,maximum=8192),
    'max_output_bytes': integer(minimum=1,maximum=32768),
    'max_output_tokens': integer(minimum=1,maximum=4096),
    'timeout_seconds': integer(minimum=1,maximum=90),
}
S['V3AgentExecutionConfig'] = obj(_config,list(_config))
S['V3AgentExecutionConfig']['additionalProperties'] = False
v3_route('put','/agents/{id}/versions/{version}/execution','Bind an immutable executable configuration to an Agent version',
    body=ref('V3AgentExecutionConfig'),response=obj({**_config,'sha256':string()},list(_config)+['sha256']),
    desc='Current human and scoped agents.manage required. Prompt version must belong to this Agent. Configuration hash includes the immutable Prompt body and artifact reference. Identical replay succeeds; a different configuration returns 409 execution_config_immutable and requires a new Agent version. First configuration on an already stable/canary/previous rollback version returns 409 active_version_unconfigured; register a new draft version before release. This does not release the Agent or call a model. Tool names are the reviewed read-only allowlist; write and MCP execution are unavailable.')
_limit=obj({'environment_id':string(),'max_level':string(enum=['L0','L1','L2','L3'])},['environment_id','max_level'])
v3_route('get','/agents/environment-limit','Read an environment Agent execution ceiling',response=_limit,
    desc='One exact environment, current human platform administrator membership and global agents.manage required. An absent limit is L0.')
v3_route('put','/agents/environment-limit','Conditionally update an environment Agent execution ceiling',
    body=obj({'max_level':string(enum=['L0','L1','L2','L3']),'expected_max_level':string(enum=['L0','L1','L2','L3'])},['max_level','expected_max_level']),response=_limit,
    desc='Same platform administrator authority as GET. Compare-and-set expected_max_level; stale values return 409. Changes and audit commit atomically. Environment-scoped managers cannot raise this platform ceiling. L2 and L3 never grant write execution through the read-only run entry point.')
_result_fields={
    **fieldnames('id agent_id version model_id prompt_id config_sha256 status error_code'),
    'prompt_version':integer(minimum=1),'lifecycle_revision':integer(minimum=1),
    'output':{'type':['string','null']},'read_only':{'type':'boolean','const':True},
}
S['V3AgentExecutionResult']=obj(_result_fields,list(_result_fields))
v3_route('post','/agents/{id}/runs','Run the selected released Agent version under bounded read-only governance',
    body=obj({'input':string(minLength=1,maxLength=8192)},['input']),response=ref('V3AgentExecutionResult'),
    desc='Current session and scoped agents.view/change required. A server-generated run ID selects stable/canary using the persisted percentage; paused or unconfigured versions fail closed. Freezes model, Prompt, config digest, lifecycle revision and guard policies. At most 5 model calls, 4 allowlisted reads and 90 seconds; L0 has no tools. Current session, grants, release revision, environment ceiling and policies are rechecked at model/read boundaries and before success commit. Unknown enabled guard policies block execution. Run, spans, sanitized audits and private bounded output are persisted; failed/blocked runs never save successful output. HTTP 200 contains the terminal status, not an unconditional success assertion. POST is non-idempotent: after an uncertain response inspect run records before retrying. Only this manual entry point consumes configuration; aggregate/other runtime traffic is not asserted.')
v3_route('get','/agent-runs/{id}/result','Read a private governed Agent execution result',response=ref('V3AgentExecutionResult'),
    desc='Only the original run actor with current scoped agents.view and active application/environment mapping may read the bounded delivered snapshot. Other actors and inaccessible runs return 404. Shared run/trace views do not include output. Failed/blocked/running output is null. A delivered private snapshot is not retroactively deleted when a source document changes.')
# Refine existing lifecycle descriptions without changing frozen frontend DTOs.
_agent_pending['enum'].append('governed_requests_consume_configuration')
for _path,_method in [('/agents/lifecycle','get'),('/agents/{id}','get'),('/agents/{id}/rollout','post'),('/agents/{id}/rollback','post'),('/agents/{id}/level','patch')]:
    P[_path][_method]['description'] = ('V3 environment and current scoped authority apply. Lifecycle configuration is consumed by POST /agents/{id}/runs when the execution runtime is connected; routing_connected describes that manual endpoint only. Release effects do not assert existing traffic completion or external runtime cutover. In-flight runs fail closed on revision changes. Aggregate scores, observed traffic and full usage remain unknown. Management requires a current human and matching expected_revision; rollout still requires a passing evaluation or explicit administrator override. See immutable execution config and manual-run contracts.')

v3_route('post','/guardrails','Register one implemented manual-execution guardrail in an exact environment',
    status=201,
    body=obj({'name':string(enum=['read_only','bounded_execution','deny_sensitive_content'])},['name']),
    response=obj({**fieldnames('id environment_id name description'),'forced':v3_bool(),'enabled':v3_bool(),'revision':integer(minimum=1),'created':v3_bool(),'manual_execution_connected':v3_bool()},['id','environment_id','name','description','forced','enabled','revision','created','manual_execution_connected']),
    desc='Current human actual platform administrator plus global agents.manage and exact environment required. Registers only implemented built-in rule names; unknown names return 422. Environment/name identity is immutable. First create is 201 with atomic initial revision and sanitized audit; duplicate is 200 without another audit or a toggle. Existing imported ambiguous identities return 409. read_only and bounded_execution are forced; deny_sensitive_content can be toggled through existing PATCH. Registration never accepts code, regex, prompt, model or external endpoint. Guard enforcement applies only to the governed manual run entry point.')
P['/guardrails']['post']['responses']['200']=P['/guardrails']['post']['responses']['201']
P['/guardrails']['post']['requestBody']['content']['application/json']['schema']['additionalProperties']=False
for _path,_method in [('/guardrails','get'),('/guardrails/{id}','patch')]:
    _guard_schema=P[_path][_method]['responses']['200']['content']['application/json']['schema']
    _guard_schema['properties'].update({'manual_execution_connected':v3_bool(),'execution_scope':string(enum=['governed_manual_runs'])})
    _guard_schema['required'] += ['manual_execution_connected','execution_scope']
P['/guardrails/{id}']['patch']['responses']['200']['content']['application/json']['schema']['properties']['runtime_status']['enum'].append('manual_readonly')
P['/guardrails/{id}']['patch']['description']='Current scoped agents.manage persists enabled state and revision with atomic audit; forced policies cannot be disabled. runtime_status is manual_readonly when the governed manual-run runtime is bound, otherwise unconnected. manual_execution_connected and execution_scope explicitly limit this connection to governed manual Agent runs. Revision changes block subsequent checks and success commits of in-flight governed runs; other executors and full production enforcement coverage are not asserted.'
P['/guardrails']['get']['description']='Persisted policies and reported events with bounded display, current authorization and revision-backed UTC aggregates. manual_execution_connected describes only governed manual Agent execution; execution_scope is governed_manual_runs. Global coverage, private source content and full enforcement coverage remain unknown. No legacy or external executor connection is implied.'
S['V3AgentEvidenceSummary']['properties'].update({'manual_execution_connected':v3_bool(),'execution_scope':string(enum=['governed_manual_runs'])})
S['V3AgentEvidenceSummary']['required'] += ['manual_execution_connected','execution_scope']
_detail_schema=P['/agents/{id}']['get']['responses']['200']['content']['application/json']['schema']
_detail_schema['properties'].update({'manual_execution_connected':v3_bool(),'execution_scope':string(enum=['governed_manual_runs'])})
_detail_schema['required'] += ['manual_execution_connected','execution_scope']
