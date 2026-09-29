# Role matrix and membership are read from V3 grants. The writable role
# endpoint applies only to explicitly scoped grants and audits the change.
S['V3Role'] = obj({**fieldnames('id name kind'),'members':integer()},['id','name','members','kind'])
S['V3RoleDetail'] = obj({**S['V3Role']['properties'],**fieldnames('desc notes'),
    'member_names':arr(string()),'scope':arr(string()),'actions':arr(string()),
    'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))})
S['V3Member'] = obj({**fieldnames('id name email department phone'),
    'last_login':{'type':['string','null'],'description':'Null: no verified successful-login timestamp source is connected.'},
    'roles':arr(string()),'mfa':string(enum=['passkey','totp','none','na']),
    'source':string(enum=['feishu','ldap','system','invite']),
    'status':string(enum=['active','disabled']),'agent':v3_bool()})
S['V3MembersView'] = obj({'org':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'groups':arr(obj({**fieldnames('id name kind'),'count':integer(),'depth':integer()})),
    'items':arr(ref('V3Member')),
    'counts':obj({'all':integer(),'admin':integer(),
        'no_mfa':{'type':['integer','null'],'description':'Null: MFA enrollment is not verified.'},
        'disabled':integer()},['all','admin','no_mfa','disabled']),
    'mfa_grace_days':{'type':['integer','null'],'description':'Null: no verified MFA policy source is connected.'},
    'complete_v3_dto':v3_bool(),'unavailable_fields':arr(string())},
    ['org','groups','items','counts','mfa_grace_days','complete_v3_dto','unavailable_fields'])
v3_route('get','/roles','List scoped roles',response=listed('V3Role'),
    desc='Members and role grants are limited to the selected authorized environments. Only a reader with global roles.view and global environment.view can inventory unassigned historical roles and accounts.')
v3_route('get','/roles/{id}','Read role matrix and associations',response=ref('V3RoleDetail'),
    desc='Visible grants and member names are limited to the selected authorized environments. A role whose grants cannot be represented by the flat five-column matrix returns 409 matrix_editor_required. Other platform grants are described in notes, not projected into matrix cells.')
v3_route('put','/roles/{id}','Update role matrix',
    body=obj({'matrix':arr(obj({'resource':string(),'grants':arr(integer(enum=[0,1,2]))}))},['matrix']),
    response=obj({'ok':v3_bool()},['ok']),
    desc='Requires an active account with global roles.manage and global environment.view, rechecked inside the write transaction. Success returns only {ok:true}; a manager is not automatically authorized to read role detail after the write. Unrepresentable or scoped grants return 409 rather than being silently overwritten. Rejects removal of the last eligible global human role manager.')
v3_route('get','/members','List scoped members',response=ref('V3MembersView'),
    query=[v3_query('filter'),v3_query('q')],
    desc='Rechecks the current account and selected-environment grants in a read transaction, then derives the visible members and roles from one database statement. This is a partial DTO (complete_v3_dto=false): groups, full organization hierarchy, MFA enrollment, successful last login, MFA grace policy and directory synchronization source are not verified. Member mfa=na means unknown, not unenrolled; last_login, counts.no_mfa and mfa_grace_days are null. filter=no_mfa returns 503 member_mfa_unavailable rather than a misleading list.')
