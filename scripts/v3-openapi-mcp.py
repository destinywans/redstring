# MCP registrations are legacy application-bound records. Only an explicitly
# reviewed registration/environment binding may appear in V3; the migration
# does not guess an environment for historical registrations.
S['V3McpServerPartial'] = obj({**fieldnames('id brand name desc transport caps last'),
    'caps':string(description='Localized unverified capability text; no V3 reviewed tool is implied.'),
    'last':string(description='Localized call-history-unavailable text, not a registration timestamp or a last-call time.'),
    'trust':obj({'label':string(),'tone':string()}),
    'apps':arr(obj({'label':string(),'tone':string()})),
    'status':string(enum=['pending'],description='Placeholder while no verified V3 connection-health state exists.'),
    'complete_v3_dto':v3_bool(), 'unavailable_fields':arr(string())})
S['V3McpDetailPartial'] = obj({**S['V3McpServerPartial']['properties'],
    'version':string(description='Localized server-version-unverified text, not the legacy registration revision.'),
    'counts':obj({k:integer(minimum=0,description='Placeholder 0; actual V3 count is unknown and listed in unavailable_fields.') for k in ['tools','resources','prompts']}),
    'tools':arr(obj({'name':string(),'desc':string(),'write':v3_bool(),
        'enabled':v3_bool(),'calls':string()})),
    'resources':arr(obj({'uri':string(),'desc':string()})),
    'prompts':arr(obj({'name':string(),'desc':string()})),
    'calls':arr(obj({'time':string(),'agent':string(),'tool':string(),'ms':integer(),'ok':v3_bool()})),
    'trust_info':obj({'readonly_check':string(),'scope':arr(string()),'limit':string(),'auth':string()})})
v3_route('get','/mcp-servers','List accessible, environment-bound MCP extensions',
    response=obj({'kpis':arr(obj({'id':string(),'label':string(),'value':string(),
        'sub':string(),'series':arr(v3_num())})), 'items':arr(ref('V3McpServerPartial')),
        'complete_v3_dto':v3_bool(),'unavailable_fields':arr(string())}),
    desc='Partial registration metadata only. The KPIs count visible registrations and legacy read-only bindings, not V3 reviewed tools. Unbound historical MCP records are omitted, not copied across environments. A single authorization snapshot scans at most 2000 candidate registrations; a 2001st candidate returns 413 candidate_scan_limit rather than partial items, while more than 1000 visible registrations returns 413 scope_too_large. Health, discovered capabilities, V3 tool review, Agent grants, call metrics and pending changes are unavailable; status is a pending placeholder and complete_v3_dto is false.')
P['/mcp-servers']['get']['responses']['413'] = {
    'description':'Candidate scan or visible registration limit exceeded',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/mcp-servers/{id}','Read accessible MCP registration metadata',
    response=ref('V3McpDetailPartial'),
    desc='Partial registration metadata only: legacy fixed-argument bindings and untrusted discovery observations are not V3 reviewed Agent tools, so tools and Agent apps are empty. No verified service version, live health, actual tool/resource/prompt counts, Agent grants or tool-call history exists; counts of 0 are DTO placeholders and every unknown count is named in unavailable_fields. A credential reference may be reported as registered but never verified or returned. complete_v3_dto is false.')
v3_route('patch','/mcp-servers/{id}/tools/{name}','Change a reviewed MCP tool',
    body=obj({'enabled':v3_bool()},['enabled']),
    desc='The unversioned main-page route still returns 503 mcp_review_unavailable after scoped authorization. Use the version-bound auxiliary reviewed-tools route; no Agent tool execution exists.')
P['/mcp-servers/{id}/tools/{name}']['patch']['responses'].pop('200')
v3_route('post','/mcp-servers/{id}/review','Review newly discovered MCP tools',
    desc='The unversioned main-page route still returns 503 mcp_review_unavailable after scoped authorization. Use the version-bound auxiliary review-version route; remote discovery alone is untrusted.')
P['/mcp-servers/{id}/review']['post']['responses'].pop('200')
S['V3McpDiscoveryObservation'] = obj({
    'snapshot_id':string(), 'mcp_server_id':string(), 'application_id':string(),
    'environment_id':string(), 'registration_version':integer(minimum=1),
    'registration_hash':string(), 'tools_hash':string(),
    'tool_count':integer(minimum=0,maximum=128), 'captured_at':string(format='date-time'),
    'trust':string(enum=['untrusted_observation']),
    'approved':{'type':'boolean','const':False},
    'health_verified':{'type':'boolean','const':False}},
    ['snapshot_id','mcp_server_id','application_id','environment_id','registration_version',
     'registration_hash','tools_hash','tool_count','captured_at','trust','approved','health_verified'])
v3_route('post','/mcp-servers/{id}/discover','Observe MCP tool declarations in an immutable snapshot',
    response=ref('V3McpDiscoveryObservation'),
    desc='Auxiliary observation route outside the 166 V3 page contracts. Requires exactly one selected environment, an explicit active binding and current connectors.manage on the persisted application. No request body is accepted. A bounded Streamable HTTP tools/list runs only after authorization; registration version, raw record digest, binding instance and authorization are rechecked before an immutable observation and management audit commit. Tool names are sorted before hashing; remote descriptions, read-only hints and JSON input schemas remain untrusted claims. Discovery does not enable, approve, execute or attest the health of any tool. A still-authorized upstream or invalid-declaration failure is audited without endpoint, credential or remote text and returns 502; a missing discovery client or persistence/audit failure returns 503. A changed registration returns 409, while absent or inaccessible initial objects return 404.')
P['/mcp-servers/{id}/discover']['post']['responses']['502'] = {
    'description':'Bounded MCP discovery failed or returned invalid declarations',
    'content':{'application/json':{'schema':ref('Error')}}}
S['V3McpReviewCandidateTool'] = obj({'name':string(),'description':string(),
    'read_only_hint':v3_bool(),'input_schema':{'type':'object','additionalProperties':True},
    'tool_hash':string(),'decision':string(enum=['','allow_read_only','deny']),
    'enabled':v3_bool(),'revision':integer(minimum=0)},
    ['name','description','read_only_hint','input_schema','tool_hash','decision','enabled','revision'])
S['V3McpReviewCandidate'] = obj({'snapshot_id':string(),'mcp_server_id':string(),
    'application_id':string(),'environment_id':string(),
    'registration_version':integer(minimum=1),'registration_hash':string(),
    'tools_hash':string(),'tool_count':integer(minimum=0,maximum=128),
    'captured_at':string(format='date-time'),'tools':arr(ref('V3McpReviewCandidateTool')),
    'trust':string(enum=['untrusted_observation']),'reviewed':v3_bool(),
    'health_verified':{'const':False},'execution_available':{'const':False}},
    ['snapshot_id','mcp_server_id','application_id','environment_id','registration_version',
     'registration_hash','tools_hash','tool_count','captured_at','tools','trust','reviewed',
     'health_verified','execution_available'])
v3_route('get','/mcp-servers/{id}/review-candidate','Read current untrusted MCP tool candidate',
    response=ref('V3McpReviewCandidate'),
    desc='Auxiliary single-environment connectors.manage route. The observation, including remote description, read-only hint and input schema, is untrusted. After snapshot projection, a fresh transaction rechecks the current actor/session/grants, active registration/application/binding, latest discovery and review decisions; locks are held through a bounded response write. Changes fail closed without candidate metadata. Human review and tool enablement do not grant Agent execution.')
S['V3McpToolReviewDecision'] = obj({'name':string(),'tool_hash':string(),
    'decision':string(enum=['allow_read_only','deny'])},['name','tool_hash','decision'])
v3_route('post','/mcp-servers/{id}/review-version','Review every tool in the current MCP discovery version',
    body=obj({'snapshot_id':string(),'registration_version':integer(minimum=1),
        'registration_hash':string(),'tools_hash':string(),
        'decisions':arr(ref('V3McpToolReviewDecision'))},
        ['snapshot_id','registration_version','registration_hash','tools_hash','decisions']),
    response=obj({'snapshot_id':string(),'reviewed':{'const':True},
        'tool_count':integer(minimum=0,maximum=128),'allowed_read_only_count':integer(minimum=0),
        'enabled_count':{'const':0},'execution_available':{'const':False}},
        ['snapshot_id','reviewed','tool_count','allowed_read_only_count','enabled_count',
         'execution_available']),
    desc='Auxiliary version-bound write for a current human connectors.manage reviewer. The exact latest registration/binding and all discovered tool names and hashes must be reviewed once. Denied tools and allowed read-only tools are initially disabled; remote hints alone do not grant approval. The review, decisions and redacted audit commit atomically. No Agent tool execution exists.')
v3_route('patch','/mcp-servers/{id}/reviewed-tools/{name}','Change a version-bound reviewed MCP tool switch',
    body=obj({'snapshot_id':string(),'tools_hash':string(),'tool_hash':string(),
        'expected_revision':integer(minimum=1),'enabled':v3_bool()},
        ['snapshot_id','tools_hash','tool_hash','expected_revision','enabled']),
    response=obj({'snapshot_id':string(),'tool_name':string(),'enabled':v3_bool(),
        'revision':integer(minimum=1),'changed':v3_bool(),'execution_available':{'const':False}},
        ['snapshot_id','tool_name','enabled','revision','changed','execution_available']),
    desc='Auxiliary current-human connectors.manage write. Requires the latest fully reviewed snapshot, exact tool hash and expected revision. Denied tools cannot be enabled; changes and redacted audit share one transaction. An enabled switch does not make the tool callable by an Agent.')
S['V3McpEnvironmentBinding'] = obj({'id':string(),'application_id':string(),
    'environment_id':string(),'bound':v3_bool(),'changed':v3_bool()},
    ['id','application_id','environment_id','bound','changed'])
v3_route('put','/mcp-servers/{id}/environment-binding','Explicitly bind a registered MCP server to the selected environment',
    response=ref('V3McpEnvironmentBinding'),
    desc='Ancillary V3 metadata-only operation. Uses exactly one selected X-AIOps-Environment; body does not choose scope. Requires connectors.manage on the registered application. Only active registrations with operator-confirmed read-only bindings may be enrolled. This does not authorize tool execution or attest that endpoint, fixed arguments and credentials are safe for the selected environment. Idempotent and audited. Historical records are never automatically bound.')
v3_route('delete','/mcp-servers/{id}/environment-binding','Unbind a registered MCP server from the selected environment',
    response=ref('V3McpEnvironmentBinding'),
    desc='Ancillary V3 operation. Uses exactly one selected X-AIOps-Environment and connectors.manage on the registered application. Idempotent and audited when changed.')
