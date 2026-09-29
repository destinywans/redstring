S['V3ConfigSyncItem'] = obj({
    **fieldnames('id kind_label title mr author meta'),
    'kind':string(enum=['template']),
    'status':string(enum=['pending','imported','exported'])},
    ['id','kind','kind_label','title','mr','author','meta','status'])
S['V3ConfigSyncList'] = obj({
    'direction':string(enum=['import','export']),
    'items':arr(ref('V3ConfigSyncItem'))}, ['direction','items'])
S['V3PromotionRequest'] = obj({'kind':string(enum=['template','templates','runbooks','alert-rules','agents'],
    description='Only template/templates are currently supported; other kinds return 501 promotion_kind_unavailable.'),
    'id':string(minLength=1,maxLength=200)}, ['kind','id'])
S['V3PromotionResult'] = obj(fieldnames('mr path'), ['mr','path'])
v3_route('get','/config-sync','List configuration promotion records or merged candidates',
    response=ref('V3ConfigSyncList'),
    desc='Partial main-contract operation: only sanitized canvas templates are supported. Non-production lists up to 100 exports by the current authenticated actor in selected authorized environments; each still requires canvas.change. Production requires a human platform administrator with global workspace.manage and lists up to 100 merged MR candidates. Candidate title, author and status are repository metadata, not verified provenance; the signed manifest, single changed file and merge commit are checked on explicit import. A missing repository or source signing/trust configuration returns 501. An over-limit list returns 413. The four-kind frontend page remains mock.')
v3_route('post','/promotions','Export a sanitized canvas template to a GitLab merge request',
    body=ref('V3PromotionRequest'),response=ref('V3PromotionResult'),
    desc='Partial main-contract operation: one selected non-production environment, an active source template owner and canvas.change grant are required. The signed manifest contains the user-provided template name, source identifiers, template kind and allowlisted graph layout; source node content, evidence, execution state, credentials and account ID are excluded. The current frontend may default the name to the source canvas title, so the name must be reviewed before export to the configuration repository. Creates or reuses a durable signed manifest and MR in the separately configured repository; an uncertain remote result returns 503. Production returns 409 already_production; all returns 409 env_view_only. Kinds other than template/templates return 501 promotion_kind_unavailable. Missing repository or signing configuration returns 501. The four-kind frontend workflow remains mock.')
v3_route('post','/config-sync/{id}/import','Import a merged signed canvas template',
    response=ref('V3ConfigSyncItem'),
    desc='Partial main-contract operation: id is mr-<positive IID>. Requires the production prod environment and a human platform administrator with global workspace.manage, rechecked inside the import transaction. The repository must prove the MR is merged with exactly one expected changed file at its fixed merge commit; the bounded allowlisted manifest must verify against a configured trusted source public key. Import also requires current satisfied MR approval state, a distinct non-bot approver after the final diff version and before merge, and currently verifiable protected target-branch policy. API reads cannot prove the governance policy in force at merge time; deployment must retain that evidence. Import and audit commit together; repeated identical imports converge, target drift returns 409. Non-production and all return 409. Missing repository or trust configuration returns 501. Unverifiable provenance returns 503. The four-kind frontend workflow remains mock.')
P['/config-sync/{id}/import']['post']['description'] += ' A unique target or import-ledger conflict returns 409 without persisting a partial import.'
for _method, _path in [('get','/config-sync'),('post','/promotions'),('post','/config-sync/{id}/import')]:
    P[_path][_method]['responses']['501'] = {
        'description':'Configuration repository or source trust is unavailable, or requested promotion kind is unsupported',
        'content':{'application/json':{'schema':ref('Error')}}}
P['/config-sync']['get']['responses']['413'] = {
    'description':'Export or merged candidate list exceeds 100 items',
    'content':{'application/json':{'schema':ref('Error')}}}
