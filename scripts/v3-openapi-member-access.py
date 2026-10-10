# WAN-237: explicit V3 role creation and complete member assignment snapshots.
S['V3AccessScope'] = obj({'kind':string(enum=['global','environment']), 'id':string(minLength=1)}, ['kind','id'])
S['V3AccessGrant'] = obj({**fieldnames('module action scope_kind scope_id assurance')}, ['module','action','scope_kind','scope_id','assurance'])
S['V3CreateRole'] = obj({
    'id':string(pattern=r'^custom-[a-z0-9][a-z0-9_-]{0,56}$',maxLength=64),
    'name':string(minLength=1,maxLength=120), 'description':string(maxLength=2000),
    'scopes':{'type':'array','minItems':1,'maxItems':1,'items':ref('V3AccessScope')},
    'matrix':S['V3RoleDetail']['properties']['matrix']}, ['id','name','scopes','matrix'])
S['V3CreateRoleResult'] = obj({'id':string(), 'created':v3_bool(), 'kind':string(enum=['business']),
    'scopes':arr(ref('V3AccessScope')), 'matrix_revision':string(pattern=r'^[1-9][0-9]*$')},
    ['id','created','kind','scopes','matrix_revision'])
S['V3AccessRole'] = obj({**fieldnames('id name'), 'kind':string(enum=['system','business','machine']),
    'origin':string(enum=['direct','legacy','department']), 'matrix_revision':integer(minimum=1),
    'grants':arr(ref('V3AccessGrant'))}, ['id','name','kind','origin','matrix_revision','grants'])
S['V3MemberAccess'] = obj({'id':string(),'enabled':v3_bool(), 'identity_kind':string(enum=['human','machine']),
    'source':string(),'local_login_ready':v3_bool(),'roles':arr(ref('V3AccessRole')),
    'revision':string(pattern=r'^[a-f0-9]{64}$')},
    ['id','enabled','identity_kind','source','local_login_ready','roles','revision'])
S['V3MemberAccessUpdate'] = obj({'expected_revision':string(pattern=r'^[a-f0-9]{64}$'),
    'role_ids':{'type':'array','maxItems':64,'uniqueItems':True,'items':string(minLength=1,maxLength=128)},
    'role_revisions':{'type':'object','additionalProperties':string(pattern=r'^[1-9][0-9]*$')},
    'enabled':v3_bool()}, ['expected_revision','role_ids','role_revisions','enabled'])
v3_route('post','/roles','Create an explicitly scoped custom business role',body=ref('V3CreateRole'),
    response=ref('V3CreateRoleResult'),
    desc='Requires the exact active Cookie session, a human with global roles.manage and global environment.view, and one selected environment. Exactly one explicit scope: the selected environment, or global:* reviewed explicitly. Writes the complete matrix plus environment.view in one audited transaction. Stable custom-* IDs are idempotent only for identical content; conflicting IDs/names return 409. No legacy permission or machine/system role is created. All-environment writes remain 409.')
P['/roles']['post']['responses']['201'] = P['/roles']['post']['responses']['200'].copy()
P['/roles']['post']['responses']['201']['description'] = 'Created custom business role'
v3_route('get','/members/{id}/access','Read complete member access for an administrator',response=ref('V3MemberAccess'),
    desc='Requires human global roles.manage, members.manage and environment.view, rechecked with the exact Cookie in the same locked transaction. Shows every direct, legacy and department role and its full V3 grants across environments; selected environment is admission, not a snapshot filter. Revision fingerprints the full returned access state. local_login_ready describes stored local credentials only, not external authentication readiness.')
v3_route('put','/members/{id}/access','Replace direct member roles and activation state',body=ref('V3MemberAccessUpdate'),response=ref('V3MemberAccess'),
    desc='Same human global authority as the access read. Replaces direct roles only; inherited legacy/department roles are preserved. expected_revision must match the complete access snapshot, and role_revisions must match each submitted role current matrix revision. Machine targets/roles, duplicate IDs, unknown fields and password input are rejected. Assigning legacy permissions or changing privileged legacy identities additionally requires actual platform-administrator membership. Enabled members require environment admission. Imported Feishu/LDAP activation does not provision authentication. Protects final usable platform administrator and human global role manager, writes atomic audit and revokes target sessions after changes. Idempotent no-op preserves sessions and audit count. Audit/storage/auth failures roll back the entire edit. Self-disable is rejected; all-environment writes remain 409.')
P['/members/{id}/access']['put']['description'] += ' A direct role the member already holds through the invitation (legacy origin) is refused with 409 role_already_inherited (PA-54).'
