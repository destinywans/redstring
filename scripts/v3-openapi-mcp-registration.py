# Auxiliary first-registration endpoint; no discovery, approval or execution.
S['V3McpRegistrationRequest'] = obj({
    'application_id':string(minLength=1,maxLength=100),
    'title':string(minLength=1,maxLength=160),
    'maintainer':string(minLength=1,maxLength=160),
    'endpoint':string(minLength=1,maxLength=2000),
    'credential_ref':string(maxLength=100),
    'allow_http':v3_bool()}, ['application_id','title','maintainer','endpoint'])
S['V3McpRegistrationRequest']['additionalProperties'] = False
S['V3McpRegistrationCreated'] = obj({
    'id':string(), 'application_id':string(), 'environment_id':string(),
    'version':integer(minimum=1,maximum=1), 'status':string(enum=['pending']),
    'bound':{'type':'boolean','const':True},
    'reviewed':{'type':'boolean','const':False},
    'execution_available':{'type':'boolean','const':False},
    'health_verified':{'type':'boolean','const':False}},
    ['id','application_id','environment_id','version','status','bound','reviewed',
     'execution_available','health_verified'])
v3_route('post','/mcp-servers','Register a pending MCP server in an exact application and environment',
    body=ref('V3McpRegistrationRequest'), response=ref('V3McpRegistrationCreated'),
    desc='Auxiliary registration route outside the 166 page contracts. Requires a current session, exact selected environment and connectors.manage on an active persisted application or its owner team. Deployment-owned AIOPS_V3_MCP_REGISTRATION_BINDINGS must exactly admit application, environment, endpoint, credential reference and HTTP opt-in; missing or mismatched admission returns 503. Body cannot supply environment, credentials, headers, tools, reviews or Agent grants. Registration, immutable revision 1, exact environment binding and redacted audit commit atomically; audit failure rolls everything back. Creates no reviewed tools and makes no network request. Endpoint and credential reference are never returned. New registrations support the existing explicit discovery/review/switch lifecycle; discovery and rebind recheck deployment admission. No Agent execution is enabled.')
P['/mcp-servers']['post']['responses']['201'] = P['/mcp-servers']['post']['responses'].pop('200')
P['/mcp-servers']['post']['responses']['201']['headers'] = {
    'Location':{'description':'New registration resource path','schema':string()}}
