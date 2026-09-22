"""Infrastructure resource-operation RBAC. Executed by build-openapi.py."""
rbac_bool = {'type': 'boolean'}
rbac_action = string(enum=['view','update','approve','delete'])
rbac_resource_scope = string(enum=['resource'])
S['RBACRole'] = obj({**fieldnames('id name description'), 'builtin': rbac_bool, 'revision': integer(minimum=1), **{k: integer(minimum=0) for k in ('permission_count','user_count','department_count')}}, ['id','name','description','builtin','revision','permission_count','user_count','department_count'])
rbac_optional_time = {'anyOf':[string(format='date-time'),{'type':'null'}]}
S['RBACPermission'] = obj({**fieldnames('id name description'), 'builtin': rbac_bool, 'revision': integer(minimum=1), 'resource_count': integer(minimum=0), 'actions':arr(rbac_action), 'created_at':rbac_optional_time, 'updated_at':rbac_optional_time}, ['id','name','description','builtin','revision','resource_count','actions','created_at','updated_at'])
S['RBACDelta'] = obj({'add': arr(string()), 'remove': arr(string())}, [])
S['RBACCandidate'] = obj({**fieldnames('id name description email kind application_id application_name environment department_id department_name lock_reason'), 'locked': rbac_bool, 'selected': rbac_bool, 'inherited': rbac_bool, 'is_mock': rbac_bool, 'source_departments': arr(string()), 'resource_count': integer(minimum=0)}, ['id','name','description','selected','inherited','is_mock','source_departments','resource_count'])
rbac_page_params = [ {'name': name, 'in': 'query', 'schema': schema} for name,schema in [('q',string()),('page',integer(minimum=1,maximum=1000000,default=1)),('page_size',integer(minimum=1,maximum=100,default=10))]]
def rbac_page(item, revision=False):
    fields={'items':arr(item),'total':integer(minimum=0),'page':integer(minimum=1),'page_size':integer(minimum=1,maximum=100)}
    if revision: fields['revision']=integer(minimum=1)
    return obj(fields,list(fields))
for kind,model in [('roles','RBACRole'),('permissions','RBACPermission')]:
    path='/rbac/'+kind
    route('get',path,'Search persisted '+kind,response=rbac_page(ref(model)))
    P[path]['get']['parameters']=list(rbac_page_params)
    P[path]['get']['description']='Text search supports literal Chinese and case-insensitive English, pinyin and initials before server count and pagination.'
    if kind=='permissions':
        P[path]['get']['parameters'] += [{'name':'action','in':'query','schema':rbac_action},{'name':'resource_kind','in':'query','schema':string(enum=['cluster','node','deployment','namespace','host','network','storage','redis','mysql','kafka'])}]
        P[path]['get']['description'] += ' action and resource_kind must match the same infrastructure resource binding. actions is an aggregate of associated operations, never an intrinsic permission field. Historical timestamps may be null; new definitions and subsequent metadata or resource-association edits record timestamps.'
    fields={'name':string(minLength=1,maxLength=200),'description':string(maxLength=2000)}
    if kind=='roles': fields['name']['description']='Unique role name after trimming outer whitespace. Duplicate names return 409 role_name_conflict. Internal stable IDs are not user-entered identifiers.'
    if kind=='permissions':
        fields['name']['description']='Unique permission name after trimming outer whitespace. Duplicate names return 409 permission_name_conflict. Internal stable IDs are not user-entered identifiers.'
    route('post',path,'Create '+kind,body=obj(fields,['name']),response=ref(model))
    route('get',path+'/{id}','Read '+kind,response=ref(model))
    route('patch',path+'/{id}','Update '+kind,body=obj({**fields,'revision':integer(minimum=1)},['name','revision']),response=ref(model),desc='Initialization-provided roles and permissions are editable like custom records. Permissions have no intrinsic operations or targets; resource bindings carry operations. Revision conflicts return 409.')
    route('delete',path+'/{id}','Delete '+kind,response=obj({'deleted':rbac_bool},['deleted']),desc='Initialization-provided records are deletable. Deletion atomically removes associations without deleting related users, departments, roles, permissions or resources. Referencing roles receive a new revision when a permission is deleted. Authorization is rechecked and the last enabled RBAC manager is protected with 409 last_rbac_manager.')
    P[path+'/{id}']['delete']['parameters'].append({'name':'revision','in':'query','required':True,'schema':integer(minimum=1)})
route('get','/rbac/me','Read current platform administration access',response=obj({'management_objects':arr(string())},['management_objects']),desc='Authenticated current identity only. management_objects is the compatibility response for platform administration navigation. An enabled real user must hold the actual admin role directly or through their department; ordinary resource bindings do not grant platform administration.')
route('get','/rbac/catalog','Read resource operations and department filter metadata',response=obj({'actions':arr(obj({'id':rbac_action,'name':string()},['id','name'])),'departments':arr(obj(fieldnames('id name'),['id','name']))},['actions','departments']),desc='Resource associations offer view, update, approve and delete on every infrastructure resource. Permission definitions have no intrinsic operations or resource scope. An unbound permission grants nothing.')
for path in ('/rbac/roles/{id}/associations/{kind}',):
    route('get',path,'Search association candidates',response=rbac_page(ref('RBACCandidate'),True),desc='Server count/search/filters precede LIMIT. selected is stored direct association. selected=yes/no filters projected direct selection including add_ids/remove_ids (comma-separated or repeated), leaving inherited source read-only. Mock resources retain is_mock; mock identities are excluded. User candidates with locked=true are account-managed base memberships; lock_reason explains how to change them.')
    P[path]['get']['parameters'] += rbac_page_params+[{'name':k,'in':'query','schema':string()} for k in ('selected','kind','type','application_id','department_id','add_ids','remove_ids')]
route('put','/rbac/roles/{id}/associations','Atomically apply permission, user and department deltas',body=obj({'revision':integer(minimum=1),**{k:ref('RBACDelta') for k in ('permissions','users','departments')}},['revision']),response=ref('RBACRole'),desc='All three tabs commit together, with audit, including built-in roles. Account-managed base user memberships are locked and return 409 legacy_role_membership. Unknown IDs and conflicting add/remove are rejected. Authorization is rechecked in the transaction; at least one enabled RBAC manager must remain, otherwise 409 last_rbac_manager. A stale revision returns 409; no partial replacement from visible pages.')
S['RBACResourceTarget'] = obj({'resource_id':string(),'scope_type':rbac_resource_scope},['resource_id','scope_type'])
S['RBACResourceBinding'] = obj({'resource_id':string(),'scope_type':rbac_resource_scope,'actions':{'type':'array','items':rbac_action,'minItems':1,'maxItems':4,'uniqueItems':True}},['resource_id','scope_type','actions'])
S['RBACResourceTreeItem'] = obj({**fieldnames('id name kind description'),'scope_type':rbac_resource_scope,'assigned_actions':arr(rbac_action),'available_actions':arr(rbac_action),'is_mock':rbac_bool,'missing':rbac_bool},['id','name','scope_type','kind','description','assigned_actions','available_actions','is_mock','missing'])
S['RBACResourceTreeGroup'] = obj({**fieldnames('kind label'),'total':integer(minimum=0),'offset':integer(minimum=0),'has_more':rbac_bool,'items':arr(ref('RBACResourceTreeItem'))},['kind','label','total','offset','has_more','items'])
S['RBACResourceTree'] = obj({'revision':integer(minimum=1),'groups':arr(ref('RBACResourceTreeGroup'))},['revision','groups'])
route('get','/rbac/permissions/{id}/resource-tree','Search resources grouped by type',response=ref('RBACResourceTree'),desc='Groups contain only concrete CMDB infrastructure resources, keyed resource:<kind>. Allowed kinds: cluster, node, deployment, namespace, host, network, storage, redis, mysql, kafka. No application, legacy application or management-object groups. Every leaf supports view/update/approve/delete. scope_type is always resource; assigned_actions reflects stored bindings. Search and group load-more never modify authorization.')
P['/rbac/permissions/{id}/resource-tree']['get']['parameters'] += [{'name':k,'in':'query','schema':v} for k,v in [('q',string()),('kind',string()),('limit',integer(minimum=1,maximum=100,default=50)),('offset',integer(minimum=0,default=0))]]
route('get','/rbac/permissions/{id}/resources','Resource tree compatibility alias',response=ref('RBACResourceTree'),desc='Alias of resource-tree, returns groups instead of the former paginated candidate table.')
P['/rbac/permissions/{id}/resources']['get']['parameters'] = P['/rbac/permissions/{id}/resource-tree']['get']['parameters']
route('put','/rbac/permissions/{id}/resources','Atomically edit resource operation bindings',body=obj({'revision':integer(minimum=1),'upsert':arr(ref('RBACResourceBinding')),'remove':arr(ref('RBACResourceTarget'))},['revision']),response=ref('RBACPermission'),desc='Each upsert replaces operations only for the supplied infrastructure resource. scope_type must be resource and actions must contain one or more of view/update/approve/delete. Remove deletes that resource binding; unmentioned targets remain unchanged. Retired scopes and actions are rejected, including when previously stored. Permission definitions carry no operations. Authorization and revision checks are atomic. Platform administration depends on actual admin role membership, not a resource operation. Recovery approval checks exact resolved infrastructure targets rather than an application-level grant.')

rbac_dept=obj({**fieldnames('id name'),'revision':integer(minimum=1),'member_count':integer(minimum=0)},['id','name','revision'])
route('get','/rbac/departments','Page canonical existing departments',response=rbac_page(rbac_dept));P['/rbac/departments']['get']['parameters']=rbac_page_params
route('post','/rbac/departments','Create department',body=obj({'name':string(minLength=1,maxLength=200)},['name']),response=rbac_dept)
route('patch','/rbac/departments/{id}','Rename department with revision',body=obj({'name':string(minLength=1,maxLength=200),'revision':integer(minimum=1)},['name','revision']),response=rbac_dept)
nullable_department={'anyOf':[string(),{'type':'null'}]}
route('patch','/rbac/users/{id}/department','Set the single department',body=obj({'department_id':nullable_department},['department_id']),response=obj({'department_id':nullable_department},['department_id']),desc='Changes the same teams/team_members used by existing department settings. Null clears membership; reassignment is explicit. Mock identities rejected.')
route('patch','/resources/{id}','Update catalog resource metadata',body=obj({'name':string(minLength=1,maxLength=240),'description':string(maxLength=4000),'tags':arr(string())},['name']),response=obj(fieldnames('id action'),['id','action']),desc='Requires update permission on this exact resource. Changes only the catalog, never infrastructure.')
route('delete','/resources/{id}','Delete a manual catalog resource',response=obj(fieldnames('id action'),['id','action']),desc='Requires delete permission on this exact resource. Only manual source resources can be removed; other sources return 409.')
S['PersonalProfile']['properties']['roles']=arr(obj({**fieldnames('id name source'),'department_id':string()},['id','name','source']))
S['PersonalProfile']['required'].append('roles')

# Account writes cannot recreate an intentionally deleted initialization role.
for path, method in [('/users', 'post'), ('/users/{id}', 'patch')]:
    operation = P[path][method]
    operation['description'] = operation.get('description', '') + ' Creating an account or explicitly changing to a deleted initialization base role returns 409 base_role_unavailable. Existing account metadata edits do not recreate deleted roles.'
