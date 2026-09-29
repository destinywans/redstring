# Connectors and their configured provider capabilities.
S['V3TagChip'] = obj({'label':string(),'tone':string(),'mono':v3_bool()},['label'])
S['V3Connector'] = obj({**fieldnames('id brand name kind meta'),
    'category':string(enum=['model','observe','delivery','infra','im','knowledge','agent_obs','security']),
    'status':string(enum=['ok','unverified','error']), 'tags':arr(ref('V3TagChip'))})
S['V3ConnectorDetail'] = obj({**S['V3Connector']['properties'], **fieldnames('scope address checked deeplink'),
    'auth':v3_object('brand text', required=['text']), 'services':arr(string()), 'queries':arr(string()),
    'tools':arr(obj({**fieldnames('name desc'),'enabled':v3_bool()}))},
    ['id','brand','name','kind','category','status','tags','meta','scope','address','auth','checked','services','queries','tools'])
S['V3RegisteredConnectorTool'] = obj({**fieldnames('name desc'),'enabled':v3_bool()},['name','desc','enabled'])
S['V3ConnectorToolSettings'] = obj({'connector_id':string(),'environment_id':string(),
    'connector_enabled':v3_bool(),'registered_tools':arr(ref('V3RegisteredConnectorTool')),
    'agent_execution_available':v3_bool()},
    ['connector_id','environment_id','connector_enabled','registered_tools','agent_execution_available'])
S['V3ConnectorList'] = obj({'items':arr(ref('V3Connector')),
    'counts':obj({k:integer() for k in ['ok','unverified','error']}),
    'categories':arr(obj({'id':string(),'label':string(),'count':integer()}))})
S['V3ConnectorType'] = obj({**fieldnames('brand name kind version webhook_url'),
    'auth_methods':arr(v3_object('id label desc')),
    'scopes':arr(obj({**fieldnames('id label'),'required':v3_bool()})),
    'tools':arr(v3_object('name desc'))})
v3_route('get','/connector-types/{type}','Read one supported connector type',response=ref('V3ConnectorType'),
    role='viewer with scoped connectors.view',
    desc='Type-level descriptor only. Auth methods correspond to supported connection probes; unknown types return a localized 404. Version, upstream token scopes and webhook URL have no verified type-level source and are empty. Agent-executable connector tools are not wired, so tools is empty. No connector instance health or wizard side effects are implied.')
v3_route('get','/connectors','List configured connectors',role='admin',response=ref('V3ConnectorList'),
    query=[v3_query(x) for x in ['category','q']],
    desc='Counts cover all currently authorized connector registrations before category or search filters. Disabled or credential-free registrations are unverified and identified in meta. Persisted tool preferences do not produce Agent capability tags. Status ok/error reflects a persisted connection test from the last 15 minutes, not continuous health; older results become unverified with their last-check time. More than 1000 visible registrations returns 413 instead of an incomplete unpaged result.')
v3_route('get','/connectors/{id}','Read connector configuration',role='admin',response=ref('V3ConnectorDetail'),
    desc='Returns one persisted registration in the selected environment. Service names are included only after current target and application grants. Auth text distinguishes a missing credential from a registered Vault reference; it does not claim the secret was verified. Status is the most recent fresh connection probe, not continuous health; missing credentials are unverified even if an older successful check was stored. There is no connector-backed Agent executor or query binding, so tools, queries, and capability tags are empty even when registration preferences exist. More than 1000 visible target services returns 422 instead of truncation.')
v3_route('get','/connectors/{id}/tool-settings','Read stored connector tool switches',role='admin',
    response=ref('V3ConnectorToolSettings'),
    desc='Auxiliary read outside the 166 page routes. Requires current connectors.view in an authorized environment and selects the registration by ID and environment. Returns stored tool switches with localized catalog labels, connector enabled state and agent_execution_available=false; it exposes neither endpoint nor credential reference, and does not imply Agent execution. Unknown or cross-environment IDs return 404; unknown, duplicate or retired stored tools fail closed with 503 rather than silently changing the reported settings.')
v3_route('post','/connectors','Create a connector',role='admin',
    body=obj({'type':string(),'url':string(),'auth':string(),'credential_ref':string(),
        'tools':arr(string()),'webhook':v3_bool()},['type','url','auth']),
    response=obj({'id':string()},['id']),status=201,
    desc='Creates an audited environment-scoped registration. The current GitLab wizard omits Vault credential and project scope; its request is stored disabled and unverified. webhook=true records intent only and does not provision a remote Webhook. A disabled registration cannot be probed or have tools enabled; activation requires a Vault reference and explicit project binding. Fully configured non-GitLab read-only registrations may be enabled on creation.')
v3_route('post','/connectors/{id}/activate','Activate verified GitLab project binding',role='admin',
    body=obj({'credential_ref':string()},['credential_ref']),
    response=obj({'enabled':v3_bool(),'status':string(enum=['ok']),'verified_projects':integer(),'webhook_active':v3_bool()},
        ['enabled','status','verified_projects','webhook_active']),
    desc='Auxiliary V3 operation outside the 166 page routes. Requires one selected environment and connectors.manage. A disabled GitLab registration needs a Vault credential reference and at least one explicitly bound numeric GitLab project. The backend performs a GET for each exact project through GitLab, then rechecks authorization, connector configuration, and bindings before enabling it. This does not attest the token is read-only. Tools remain disabled; webhook=true remains intent only and no remote Webhook is provisioned. A failed check leaves the registration disabled; changing the project bindings requires reactivation. This is a point-in-time verification, not continuous health.')
P['/connectors/{id}/activate']['post']['responses']['502'] = {
    'description':'GitLab project verification failed',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('patch','/connectors/{id}','Update connector configuration',role='admin',
    body=obj({'tools':{**arr(obj({'name':string(),'enabled':v3_bool()},['name','enabled'])),'minItems':1}},['tools']),
    response=obj({'ok':v3_bool(),'tools':arr(ref('V3RegisteredConnectorTool'))},['ok','tools']),
    desc='Partially updates stored connector tool preferences under a row lock with current connectors.manage and atomic audit; distinct supported tool names and a present, non-null enabled boolean are required. Unknown, duplicate or retired stored tools return 409 tool_catalog_changed without silently deleting preferences. Enabling a tool on a disabled connector returns 409 connector_disabled. No object version precondition is implemented. The enabled flag gates some scoped read-only sources but is not an Agent execution grant; connector-backed Agent execution is not wired. The V3 frontend toggle currently does not call this route.')
P['/connectors/{id}']['patch']['responses']['409']['description'] = 'Connector disabled or stored tool catalog changed; no object version precondition'
v3_route('post','/connectors/{id}/test','Probe configured connector',role='admin',
    response=obj({'ok':v3_bool(),'latency_ms':integer(),'error_code':string()}),
    desc='Disabled registrations return 409 without contacting the provider. Other failures return an explicit error; a configured adapter is not silently reported healthy.')
