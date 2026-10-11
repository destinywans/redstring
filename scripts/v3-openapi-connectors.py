# Connectors and their configured provider capabilities.
S['V3TagChip'] = obj({'label':string(),'tone':string(),'mono':v3_bool()},['label'])
S['V3Connector'] = obj({**fieldnames('id brand name kind meta'),
    'category':string(enum=['model','observe','delivery','infra','im','knowledge','agent_obs','security']),
    'status':string(enum=['ok','unverified','error']), 'tags':arr(ref('V3TagChip')),
    'checked_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: the last connection test in RFC 3339, null when never tested; meta no longer carries the time'}})
S['V3ConnectorDetail'] = obj({**S['V3Connector']['properties'], **fieldnames('scope address deeplink'),
    'auth':v3_object('brand text', required=['text']), 'services':arr(string()), 'queries':arr(string()),
    'tools':arr(obj({**fieldnames('name desc'),'enabled':v3_bool()})),'agent_execution_available':v3_bool()},
    ['id','brand','name','kind','category','status','tags','meta','checked_at','scope','address','auth','services','queries','tools'])
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
    desc='Counts cover all currently authorized connector registrations before category or search filters. Disabled, required-credential-missing, or server-binding-missing registrations are unverified and identified in meta. Persisted tool preferences do not produce Agent capability tags. Status ok/error reflects a persisted connection test from the last 15 minutes, not continuous health; older results become unverified with their last-check time. More than 1000 visible registrations returns 413 instead of an incomplete unpaged result.')
v3_route('get','/connectors/{id}','Read connector configuration',role='admin',response=ref('V3ConnectorDetail'),
    desc='Returns one persisted registration in the selected environment. Service names are included only after current target and application grants. Auth text distinguishes a missing credential, missing server-side binding, and registered Vault reference; it does not claim the secret was verified. Status is the most recent fresh connection probe, not continuous health; missing credentials or bindings are unverified even if an older successful check was stored. There is no connector-backed Agent executor or query binding, so queries and capability tags are empty even when registration preferences exist. More than 1000 visible target services returns 422 instead of truncation. agent_execution_available is false. tools lists the saved switches (name, desc, enabled; PA-19 P3 stage 2 step 7): they decide whether the registry readers may read this connector and grant no Agent execution.')
v3_route('get','/connectors/{id}/tool-settings','Read stored connector tool switches',role='admin',
    response=ref('V3ConnectorToolSettings'),
    desc='Auxiliary read outside the 166 page routes. Requires current connectors.view in an authorized environment and selects the registration by ID and environment. Returns stored tool switches with localized catalog labels, effective connector enabled state after server credential-binding policy, and agent_execution_available=false; it exposes neither endpoint nor credential reference, and does not imply Agent execution. Unknown or cross-environment IDs return 404; unknown, duplicate or retired stored tools fail closed with 503 rather than silently changing the reported settings.')
v3_route('post','/connectors','Create a connector',role='admin',
    body=obj({'type':string(),'url':string(),'auth':string(),'credential_ref':string(),
        'tools':arr(string()),'webhook':v3_bool()},['type','url','auth']),
    response=obj({'id':string()},['id']),status=201,
    desc='Creates an audited environment-scoped registration. Credentialed registrations remain disabled even when a Vault reference is supplied: their generated ID must first be approved in a private server-side exact binding. webhook=true records intent only and does not provision a remote Webhook. A disabled registration cannot be probed or have tools enabled; GitLab also requires explicit project binding. Only unauthenticated non-GitLab read-only registrations without webhook intent may be enabled on creation.')
v3_route('post','/connectors/{id}/activate','Activate a checked connector registration',role='admin',
    body=obj({'credential_ref':string()}),
    response=obj({'enabled':v3_bool(),'status':string(enum=['ok']),'verified_projects':integer(),'webhook_active':v3_bool(),
                  'latency_ms':integer(),'credential_verified':v3_bool()},
        ['enabled','status','verified_projects','webhook_active']),
    desc='Auxiliary V3 operation outside the 166 page routes. Requires one selected environment and connectors.manage. Credentialed activations require a server-managed exact tier/environment/connector ID/provider/endpoint/Vault reference binding; no binding returns 503 before Vault or upstream access. GitLab additionally requires one or more explicitly bound numeric projects and GET verification of each exact project. Other supported providers use one bounded read-only probe and return credential_verified=false because a health endpoint may ignore authentication. An unauthenticated source can send an empty object; a stored reference can be reused without repeating it in the body. After the probe, authorization and connector configuration are locked and rechecked before activation and audit. This does not attest a token is read-only. Tools remain disabled; webhook=true remains intent only and no remote Webhook is provisioned. A failed check leaves the registration disabled; changing GitLab project bindings requires reactivation. This is a point-in-time connection observation, not continuous health.')
P['/connectors/{id}/activate']['post']['responses']['502'] = {
    'description':'GitLab project verification or generic connector probe failed',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('patch','/connectors/{id}','Update connector configuration',role='admin',
    body=obj({'tools':{**arr(obj({'name':string(),'enabled':v3_bool()},['name','enabled'])),'minItems':1}},['tools']),
    response=obj({'ok':v3_bool(),'tools':arr(ref('V3RegisteredConnectorTool'))},['ok','tools']),
    desc='Partially updates stored connector tool preferences under a row lock with current connectors.manage and atomic audit; distinct supported tool names and a present, non-null enabled boolean are required. Unknown, duplicate or retired stored tools return 409 tool_catalog_changed without silently deleting preferences. Enabling a tool on a disabled or server-binding-missing connector returns 409 connector_disabled. No object version precondition is implemented. The enabled flag gates some scoped read-only sources but is not an Agent execution grant; connector-backed Agent execution is not wired. The V3 frontend toggle currently does not call this route.')
P['/connectors/{id}']['patch']['responses']['409']['description'] = 'Connector disabled or stored tool catalog changed; no object version precondition'
v3_route('post','/connectors/{id}/test','Probe configured connector',role='admin',
    response=obj({'ok':v3_bool(),'latency_ms':integer(),'error_code':string()}),
    desc='Disabled registrations return 409 without contacting the provider; a missing exact server credential binding returns 503 before Vault or upstream access. Other failures return an explicit error; a configured adapter is not silently reported healthy. A successful health endpoint response does not necessarily verify credentials.')

# WAN-122 model administration. This fragment is already loaded by the V3
# generator; the checked-in aggregate contract is refreshed by integration.
S['V3ModelAdminItem'] = obj({
    **S['V3ModelInfo']['properties'],
    'used_by':string(), 'scope':string(),
    'status':string(enum=['ok','unverified']), 'is_default':v3_bool(),
}, ['id','name','provider','brand','connector','api','reasoning','tools',
    'vision','context_k','cost','available','used_by','scope','status'])
S['V3ModelAdminProvider'] = obj({
    'brand':string(), 'name':string(), 'api':string(), 'models':integer(minimum=0),
    'status':string(enum=['ok','unverified','error']),
}, ['brand','name','api','models','status'])
S['V3ModelAdminDetail'] = obj({
    'model':string(),
    'api_chain':arr(obj({'api':string(),'note':string()},['api','note'])),
    'mapping':arr(obj({'level':string(),'param':string(),'is_default':v3_bool()},['level','param'])),
    'checks':arr(string()), 'roles':string(), 'agents':string(), 'limits':string(),
    'checked_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: the metadata check time in RFC 3339, null when this process has no check; checks no longer carry the time'},
}, ['model','api_chain','mapping','checks','checked_at','roles','agents','limits'])
S['V3ModelAdminView'] = obj({
    'providers':arr(ref('V3ModelAdminProvider')),
    'items':arr(ref('V3ModelAdminItem')),
    'detail':ref('V3ModelAdminDetail'),
}, ['providers','items','detail'])
v3_route('get','/models/admin','Read environment-bound model administration view',
    response=ref('V3ModelAdminView'),
    desc='Requires one selected environment and current models.view authorization. Returns 503 until a server-side exact environment/model binding and the operator-enabled V3 read-tool allowlist are injected into the same V3 selection runtime. Model metadata comes from the configured inference runtime; historical use comes from persisted conversation, canvas, and Agent run records. tools reflects the injected read-tool allowlist, not a proven remote function call. A local metadata observation is point-in-time evidence only: provider and item status remain unverified, and no generation, streaming, vision, token cost, active Agent assignment, model-specific role rule, or API fallback is inferred. The frontend ModelInfo API enum cannot represent native Ollama, which returns 503 instead of a false protocol label. The detail object describes the configured default model for the selected environment.')
# WAN-246 interaction configuration writes.
S['V3ConnectorDetail']['properties'].update({'revision':integer(minimum=1),'enabled':v3_bool()})
S['V3ConnectorDetail']['required'] += ['revision','enabled']
v3_route('put','/connectors/{id}/settings','Update connector address with revision CAS',role='human connectors.manage',
    body=obj({'expected_revision':integer(minimum=1),'address':string(),'scope':string()},['expected_revision','address','scope']),response=ref('V3ConnectorDetail'),
    desc='scope must equal the persisted environment ID. An address change invalidates probe evidence, disables connector/tools and retains the Vault reference; the changed endpoint requires a new exact deployment-owned credential binding and activate. All connector updates advance revision. Stale revisions return 409. No provider is called by this write.')
v3_route('post','/connectors/{id}/disable','Disable a connector registration',role='human connectors.manage',
    body=obj({'reason':string(maxLength=1000)}),response=ref('V3ConnectorDetail'),
    desc='Disables the connector and all stored tool switches, clears probe evidence and preserves credential reference. One selected environment, current human session and management grant; state and redacted audit commit atomically. activate restores only after exact binding/probe validation.')
