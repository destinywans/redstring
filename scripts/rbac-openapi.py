"""Persisted explicit-scope RBAC. Executed by build-openapi.py."""
rbac_bool = {'type': 'boolean'}
S['RBACRole'] = obj({**fieldnames('id name description'), 'builtin': rbac_bool, 'revision': integer(minimum=1), **{k: integer(minimum=0) for k in ('permission_count','user_count','department_count')}}, ['id','name','description','builtin','revision','permission_count','user_count','department_count'])
S['RBACPermission'] = obj({**fieldnames('id name description action scope_type'), 'builtin': rbac_bool, 'revision': integer(minimum=1), 'resource_count': integer(minimum=0)}, ['id','name','description','action','scope_type','builtin','revision','resource_count'])
S['RBACDelta'] = obj({'add': arr(string()), 'remove': arr(string())}, [])
S['RBACCandidate'] = obj({**fieldnames('id name description email action scope_type kind application_id application_name environment department_id department_name'), 'selected': rbac_bool, 'inherited': rbac_bool, 'is_mock': rbac_bool, 'source_departments': arr(string()), 'resource_count': integer(minimum=0)}, ['id','name','description','selected','inherited','is_mock','source_departments','resource_count'])
rbac_page_params = [ {'name': name, 'in': 'query', 'schema': schema} for name,schema in [('q',string()),('page',integer(minimum=1,maximum=1000000,default=1)),('page_size',integer(minimum=1,maximum=100,default=10))]]
def rbac_page(item, revision=False):
    fields={'items':arr(item),'total':integer(minimum=0),'page':integer(minimum=1),'page_size':integer(minimum=1,maximum=100)}
    if revision: fields['revision']=integer(minimum=1)
    return obj(fields,list(fields))
for kind,model in [('roles','RBACRole'),('permissions','RBACPermission')]:
    path='/rbac/'+kind
    route('get',path,'Search persisted '+kind,response=rbac_page(ref(model)))
    P[path]['get']['parameters']=rbac_page_params+[{'name':k,'in':'query','schema':string()} for k in ('action','scope_type','builtin')]
    fields={'name':string(minLength=1,maxLength=200),'description':string(maxLength=2000)}
    if kind=='permissions':fields.update({'action':string(enum=['view','update','delete','investigate','approve','execute','manage']),'scope_type':string(enum=['resource','application','management'])})
    route('post',path,'Create '+kind,body=obj(fields,['name']+(['action','scope_type'] if kind=='permissions' else [])),response=ref(model))
    route('get',path+'/{id}','Read '+kind,response=ref(model))
    route('patch',path+'/{id}','Update '+kind,body=obj({**fields,'revision':integer(minimum=1)},['name','revision']+(['action','scope_type'] if kind=='permissions' else [])),response=ref(model),desc='Built-in records are protected. Permission action and scope_type are immutable after creation. Revision conflicts return 409.')
    route('delete',path+'/{id}','Delete unreferenced '+kind,response=obj({'deleted':rbac_bool},['deleted']),desc='Built-ins and dependencies return 409. Roles must have no permissions/users/departments. Referenced permissions cannot be deleted.')
    P[path+'/{id}']['delete']['parameters'].append({'name':'revision','in':'query','required':True,'schema':integer(minimum=1)})
route('get','/rbac/me','Read own explicit management scopes',response=obj({'management_objects':arr(string())},['management_objects']),desc='Authenticated current identity only. No role promotion; individual routes still enforce their management object.')
route('get','/rbac/catalog','Read fixed supported actions and filter metadata',response=obj({'actions':arr(obj(fieldnames('id name scope_type'),['id','name','scope_type'])),'management_objects':arr(obj(fieldnames('id name'),['id','name'])),'applications':arr(obj(fieldnames('id name'),['id','name'])),'departments':arr(obj(fieldnames('id name'),['id','name']))},['actions','management_objects','applications','departments']))
for path in ('/rbac/roles/{id}/associations/{kind}','/rbac/permissions/{id}/resources'):
    route('get',path,'Search association candidates',response=rbac_page(ref('RBACCandidate'),True),desc='Server count/search/filters precede LIMIT. selected is stored direct association. selected=yes/no filters projected direct selection including add_ids/remove_ids (comma-separated or repeated), leaving inherited source read-only. Mock resources retain is_mock; mock identities are excluded.')
    P[path]['get']['parameters'] += rbac_page_params+[{'name':k,'in':'query','schema':string()} for k in ('selected','action','scope_type','kind','type','application_id','department_id','add_ids','remove_ids')]
route('put','/rbac/roles/{id}/associations','Atomically apply permission, user and department deltas',body=obj({'revision':integer(minimum=1),**{k:ref('RBACDelta') for k in ('permissions','users','departments')}},['revision']),response=ref('RBACRole'),desc='All three tabs commit together, with audit. Unknown IDs, conflicting add/remove and protected legacy permission bindings are rejected. A stale revision returns 409; no partial replacement from visible pages.')
route('put','/rbac/permissions/{id}/resources','Atomically apply explicit scope targets',body=obj({'revision':integer(minimum=1),'add':arr(string()),'remove':arr(string())},['revision']),response=ref('RBACPermission'),desc='Targets must exist in the permission scope type. Unknown IDs reject the entire delta. Built-ins protected; stale revision returns 409.')
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
