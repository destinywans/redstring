# Role matrix and membership are read from V3 grants. The flat writer edits
# global grants only; the selected environment authorizes the request.
S['V3Role'] = obj({**fieldnames('id name'),'members':integer(minimum=0),
    'kind':string(enum=['system','business','machine'])},['id','name','members','kind'])
S['V3RoleDetail'] = obj({**S['V3Role']['properties'],**fieldnames('desc notes'),
    'member_names':arr(string()),'scope':arr(string()),'actions':arr(string()),
    'matrix':{'type':'array','minItems':13,'maxItems':13,
        'items':obj({'resource':string(),
            'grants':{'type':'array','minItems':5,'maxItems':5,
                'items':integer(enum=[0,1,2])}},['resource','grants'])},
    # PA-55: environment.view is not a matrix row; members of a role without
    # it see no environment. "global" or the visible environment IDs.
    'environment_access':arr(string())},
    ['id','name','members','kind','desc','member_names','scope','actions','matrix','environment_access','notes'])
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
_member_revision = string(pattern=r'^[1-9][0-9]*$',description='Decimal revision of editable name and phone fields.')
S['V3MemberDetail'] = obj({**fieldnames('id name email recorded_source'),
    'department':{'type':['object','null'],'properties':fieldnames('id name'),
        'required':['id','name'],'additionalProperties':False},
    'roles':arr(obj(fieldnames('id name'),['id','name'])),
    'phone':{'type':['string','null']},
    'status':string(enum=['active','disabled']),
    'identity_kind':string(enum=['human','machine']),
    'mfa':string(enum=['unknown']),
    'last_login':{'type':'null'},
    'directory_sync_state':string(enum=['unknown']),
    'revision':_member_revision},
    ['id','name','email','department','roles','phone','status','identity_kind',
     'recorded_source','mfa','last_login','directory_sync_state','revision'])
S['V3MemberContactPatch'] = {**obj({'expected_revision':_member_revision,
    'name':string(minLength=1,maxLength=120),
    'phone':{'type':['string','null'],'maxLength':64}},['expected_revision']),
    'anyOf':[{'required':['name']},{'required':['phone']}]}
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
    desc='Rechecks the exact current session and selected-environment grants in a read transaction, then derives the complete visible account/role set from one database statement. Accepted invitation provenance is shown as source=invite. This is a partial DTO (complete_v3_dto=false): external groups and hierarchy, MFA enrollment and policy, successful last login, and directory sync state are not verified by the account store. Member mfa=na means unknown, not unenrolled; last_login, counts.no_mfa and mfa_grace_days are null. filter=no_mfa returns 503 member_mfa_unavailable instead of a misleading list. The read path does not wait for external MFA evidence while holding the account and role locks.')
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
v3_route('post','/members/sync','Synchronize reviewed Feishu directory members',
    response=obj({'added':integer(minimum=0),'updated':integer(minimum=0)},['added','updated']),
    desc='Requires one selected configured environment, a current human account and exact session, global members.manage, global environment.view and current-environment members.manage. A configured Feishu directory app reads every page of its reviewed address-book scope; empty, incomplete or invalid snapshots return 503 before database writes. Stable app binding plus external open_id prevents cross-binding collisions; changed or conflicting email cannot take over another account. The transaction rechecks authorization and the editable viewer role, creates new imported human viewer accounts disabled and without environment/application/team grants, updates only same-binding names and department text, and commits counts with a PII-free audit. A complete unchanged observed snapshot returns {added:0,updated:0}. Absence from a scoped directory snapshot never disables or deletes a member. Synchronization does not grant login or build the complete organization tree.')
P['/members/sync']['post']['responses']['409'] = {
    'description':'External identity or email conflicts with an existing account',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('post','/members/mfa-reminders','Remind verified unenrolled members through Feishu',
    response=obj({'sent':integer(minimum=0)},['sent']),
    desc='Requires one selected environment and the same current human/global-and-environment member-management authority as invitations. A trusted HTTPS MFA service must return a fresh complete exact-scope enrollment snapshot, grace policy and current Feishu open_id bindings for every active visible human. Only verified unenrolled recipients are eligible. More than 20 eligible people returns 429 before sending any message. Every Feishu API-accepted message has a durable attempt and an audit row; accepted attempts within 24 hours are skipped. An incomplete source, missing binding, uncertain delivery or partial batch returns 503, never a fabricated success count. sent counts newly Feishu-accepted messages in this request; Feishu acceptance does not prove the recipient read them.')
P['/members/mfa-reminders']['post']['responses']['429'] = {
    'description':'Bounded batch or actor rate limit reached',
    'content':{'application/json':{'schema':ref('Error')}}}
route('post','/auth/invitations/accept','Accept a one-time member invitation',role='public',
    body=ref('V3MemberInvitationAcceptance'),status=201,
    response=obj({'ok':v3_bool(),'access':string(enum=['pending_scope_grant'])},['ok','access']),
    desc='Public JSON auxiliary route. Requires a 256-bit one-time code in the body, never the URL, plus a name and password. A relay-accepted unexpired code is consumed in the same transaction that creates a local viewer account. The editable viewer role and the new account effective grants are checked again under role locks; any environment grant, application/team scope, non-view action, direct application ACL, team association or unexpected role association returns 409 invitation_role_unsafe. The account inherits the checked read-only viewer base role, including workspace.view global where present; an administrator must separately grant environment admission and application scope for V3 access. Legacy application-view bindings are inert in the current resource-only effective-permissions view. Anonymous attempts are locally bounded by the direct remote IP; a trusted ingress must also enforce a shared limit across replicas. The product acceptance UI is not implemented. The V3 environment/auth middleware exempts only this exact public POST route.')
P['/auth/invitations/accept']['post']['responses']['429'] = {
    'description':'Anonymous acceptance rate limit reached',
    'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/members/{id}','Read one visible member record',response=ref('V3MemberDetail'),
    desc='Auxiliary backend route outside the frozen 166-page Mock contract. Rechecks the active account, members.view and selected-environment admission after account/role locks, then reads the target and visible roles in one database snapshot. Unassigned historical accounts require global members.view and global environment.view. Unknown, deleted, demo and out-of-scope targets are indistinguishable 404. recorded_source is the stored origin marker, not proof of current directory synchronization; MFA, successful last login and directory sync state are unknown. The decimal revision versions only name and phone edits.')
v3_route('patch','/members/{id}','Change saved member contact display fields',
    body=ref('V3MemberContactPatch'),response=ref('V3MemberDetail'),
    desc='Auxiliary backend route outside the frozen 166-page Mock contract. Requires one selected environment, an active human account with members.view for that environment, global members.manage and global environment.view, and a visible locally owned target (recorded source=local). External or uncertain sources return 409 member_source_managed rather than overwriting directory-owned values. Changes only the globally stored name and phone; it cannot modify roles, department, status, email, MFA or external identity. expected_revision is a canonical decimal string; concurrent name/phone updates from this or other account writers return 409 member_changed. Strict nonempty patch, no unknown or duplicate fields, trimmed nonblank name up to 120 Unicode characters and trimmed nonblank phone up to 64 characters; null clears phone. The write and audit of changed field names commit together, then return the authorized persisted detail. The frozen frontend does not call this route.')
P['/members/{id}']['patch']['responses']['409']['description'] = 'Member name or phone revision changed (member_changed), or external source owns the target (member_source_managed)'
