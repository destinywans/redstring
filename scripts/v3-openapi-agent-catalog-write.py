# WAN-242: fresh instances can register catalog metadata without legacy SQL.
S['V3AgentCatalogCreate'] = obj({'id':string(minLength=1,maxLength=128,pattern=r'^[A-Za-z0-9._-]+$'),
    'application_id':string(minLength=1,maxLength=128),'name':string(minLength=1,maxLength=160),
    'summary':string(maxLength=1000)},['id','application_id','name'])
S['V3AgentVersionCreate'] = obj({'version':string(minLength=1,maxLength=80),
    'artifact_ref':string(minLength=1,maxLength=500,pattern=r'^[A-Za-z0-9._:/@#-]+$')},['version','artifact_ref'])
v3_route('post','/agents','Register a paused L0 Agent in the selected environment',body=ref('V3AgentCatalogCreate'),
    response=obj({'id':string(),'created':v3_bool(),'application_id':string(),'environment_id':string()},['id','created','application_id','environment_id']),
    desc='Requires a current human Cookie session, active application/environment and agents.view/manage on the application or its current owner team. New identity is paused L0 without a stable release. Stable ID with identical content returns 200/created:false; conflicting content returns 409; other scopes are hidden. The environment is the selected request scope, never accepted from body metadata. Registration and sanitized audit commit atomically; does not configure or execute a model.')
P['/agents']['post']['responses']['201'] = P['/agents']['post']['responses']['200'].copy()
P['/agents']['post']['responses']['201']['description'] = 'Created paused Agent identity'
v3_route('post','/agents/{id}/versions','Register immutable Agent version artifact identity',body=ref('V3AgentVersionCreate'),
    response=obj({'agent_id':string(),'version':string(),'created':v3_bool()},['agent_id','version','created']),
    desc='Requires current human agents.manage and exact environment, session and active scope rechecks. Version and non-secret artifact reference are immutable. Identical retry returns 200/created:false; different reference for the same version returns 409. No artifact URL is fetched. Prompt binding, executable budgets and rollout are separate explicit operations.')
P['/agents/{id}/versions']['post']['responses']['201'] = P['/agents/{id}/versions']['post']['responses']['200'].copy()
P['/agents/{id}/versions']['post']['responses']['201']['description'] = 'Registered immutable version identity'
