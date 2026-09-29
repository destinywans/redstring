# Role matrix and membership are read from V3 grants. The flat writer edits
# global grants only; the selected environment authorizes the request.
S['V3Role'] = obj({**fieldnames('id name'),'members':integer(minimum=0),
    'kind':string(enum=['system','business','machine'])},['id','name','members','kind'])
S['V3RoleDetail'] = obj({**S['V3Role']['properties'],**fieldnames('desc notes'),
    'member_names':arr(string()),'scope':arr(string()),'actions':arr(string()),
    'matrix':{'type':'array','minItems':13,'maxItems':13,
        'items':obj({'resource':string(),
            'grants':{'type':'array','minItems':5,'maxItems':5,
                'items':integer(enum=[0,1,2])}},['resource','grants'])}},
    ['id','name','members','kind','desc','member_names','scope','actions','matrix','notes'])
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
    desc='Rechecks the active account, current roles.view and selected-environment admission in one read transaction. Role, visible members and grants form one authorized snapshot. A role whose grants cannot be represented by the flat five-column matrix returns 409 matrix_editor_required; no unrepresentable grant is silently projected into a cell. Other platform grants are described in notes, not projected into matrix cells. X-Role-Matrix-Revision versions matrix writes only, not the entire detail representation.')
v3_route('put','/roles/{id}','Update role matrix',
    body=obj({'matrix':{'type':'array','minItems':13,'maxItems':13,
        'items':obj({'resource':string(),
            'grants':{'type':'array','minItems':5,'maxItems':5,
                'items':integer(enum=[0,1,2])}},['resource','grants'])}},['matrix']),
    response=obj({'ok':v3_bool()},['ok']),
    desc='Requires one selected environment as authorization context, an active account with global roles.manage and global environment.view, and the exact decimal matrix revision returned by GET /roles/{id} in If-Role-Matrix-Revision. A successful edit changes global role grants across every environment, not just the selected environment. Rechecks authority and revision under the write lock; stale revisions return 409 role_changed. Success returns only {ok:true} plus the new matrix revision header; a manager is not automatically authorized to read role detail after the write. Unrepresentable or scoped grants return 409 rather than being silently overwritten. Machine identities cannot approve. Rejects removal of the last eligible global human role manager. The frozen frontend does not yet forward the revision header, so its real-mode save receives 428 until adapted.')
_role_matrix_revision = {'description':'Decimal version of this role matrix; it does not version membership or descriptive role fields.',
    'schema':string(pattern=r'^[1-9][0-9]*$')}
for _method in ('get','put'):
    P['/roles/{id}'][_method]['responses']['200']['headers'] = {
        'X-Role-Matrix-Revision':_role_matrix_revision}
P['/roles/{id}']['put'].setdefault('parameters', []).append({
    'name':'If-Role-Matrix-Revision','in':'header','required':True,
    'schema':string(pattern=r'^[1-9][0-9]*$'),
    'description':'Exactly one decimal matrix revision returned by GET /roles/{id}.'})
for _code, _description in [('400','Malformed or multiple If-Role-Matrix-Revision values'),
    ('428','Role matrix revision precondition required')]:
    P['/roles/{id}']['put']['responses'][_code] = {
        'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
P['/roles/{id}']['put']['responses']['409']['description'] = \
    'Stale matrix revision (role_changed), unrepresentable or scoped grants, or last eligible human role manager'
v3_route('get','/members','List scoped members',response=ref('V3MembersView'),
    query=[v3_query('filter'),v3_query('q')],
    desc='Rechecks the current account and selected-environment grants in a read transaction, then derives the visible members and roles from one database statement. This is a partial DTO (complete_v3_dto=false): groups, full organization hierarchy, MFA enrollment, successful last login, MFA grace policy and directory synchronization source are not verified. Member mfa=na means unknown, not unenrolled; last_login, counts.no_mfa and mfa_grace_days are null. filter=no_mfa returns 503 member_mfa_unavailable rather than a misleading list.')
S['V3MemberInvitationRequest'] = obj({'email':string(format='email',maxLength=320),
    'role':string(enum=['viewer'])},['email'])
S['V3MemberInvitationAcceptance'] = obj({'code':string(pattern='^[A-Za-z0-9_-]{43}$'),
    'name':string(minLength=1,maxLength=120),
    'password':string(minLength=12,maxLength=1024,format='password')},
    ['code','name','password'])
v3_route('post','/members/invitations','Send a read-only member invitation',
    body=ref('V3MemberInvitationRequest'),
    response=obj({'ok':v3_bool()},['ok']),
    desc='Requires one selected environment, current human account and exact session, global members.manage and global environment.view. Optional role is viewer only. Before sending, the editable viewer role may have V3 view grants such as workspace.view global but no environment module grant or application/team scope, and legacy resource or legacy_application view bindings only. An unsafe grant returns 409 invitation_role_unsafe. The future account inherits this checked read-only base role; the operation grants no environment admission, direct application ACL or team association. A configured, certificate-validated implicit-TLS SMTP relay and trusted HTTPS product entry are required. The service persists only a hash of a random one-time code, rate-limits actor and recipient, and returns {ok:true} only after SMTP DATA is accepted and the invitation is durably marked delivered. Relay acceptance is not final mailbox delivery. A failed or uncertain send never returns success. The existing product has no invitation acceptance UI yet.')
P['/members/invitations']['post']['responses']['429'] = {
    'description':'Actor or recipient invitation limit reached',
    'content':{'application/json':{'schema':ref('Error')}}}
route('post','/auth/invitations/accept','Accept a one-time member invitation',role='public',
    body=ref('V3MemberInvitationAcceptance'),status=201,
    response=obj({'ok':v3_bool(),'access':string(enum=['pending_scope_grant'])},['ok','access']),
    desc='Public JSON auxiliary route. Requires a 256-bit one-time code in the body, never the URL, plus a name and password. A relay-accepted unexpired code is consumed in the same transaction that creates a local viewer account. The editable viewer role and the new account effective grants are checked again under role locks; any environment grant, application/team scope, non-view action, direct application ACL, team association or unexpected role association returns 409 invitation_role_unsafe. The account inherits the checked read-only viewer base role, including workspace.view global where present; an administrator must separately grant environment admission and application scope for V3 access. Legacy application-view bindings are inert in the current resource-only effective-permissions view. Anonymous attempts are locally bounded by the direct remote IP; a trusted ingress must also enforce a shared limit across replicas. The product acceptance UI is not implemented. The V3 environment/auth middleware exempts only this exact public POST route.')
P['/auth/invitations/accept']['post']['responses']['429'] = {
    'description':'Anonymous acceptance rate limit reached',
    'content':{'application/json':{'schema':ref('Error')}}}
