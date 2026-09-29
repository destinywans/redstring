v3_route('get','/me','Read current account',response=ref('V3Me'))
v3_route('patch','/me','Update current account',body=v3_object('name phone title timezone'),response=ref('V3Me'))
v3_route('patch','/me/preferences','Update visual and language preferences',body=ref('V3PreferencesPatch'),response=ref('V3Preferences'))
v3_route('get','/me/profile','Read personal profile',response=ref('V3ProfileView'))
v3_route('put','/me/profile','Update personal profile',body=ref('V3ProfileView'),response=ref('V3ProfileView'))
v3_route('delete','/me/sessions/{id}','Revoke one account session',status=204)
S['V3MFATOTPStatus'] = obj({'state':string(enum=['none','pending','expired','active']),
    'recovery_remaining':integer(minimum=0)},['state','recovery_remaining'])
S['V3MFATOTPEnrollment'] = obj({'secret':string(),'otpauth_url':string(),
    'expires_at':string(format='date-time')},['secret','otpauth_url','expires_at'])
S['V3MFATOTPConfirmation'] = obj({'enabled':v3_bool(),
    'recovery_codes':arr(string())},['enabled','recovery_codes'])
S['V3MFATOTPVerification'] = obj({'verified':v3_bool(),
    'method':string(enum=['totp','recovery'])},['verified','method'])
v3_route('get','/me/mfa/totp','Read current account TOTP state',response=ref('V3MFATOTPStatus'),
    desc='Optional auxiliary route, registered only with a persistent server-owned AES-256 key file. Requires one authorized environment and a current human account/session. Returns no seed or recovery codes. TOTP enrollment is not evidence of login enforcement or passkey approval assurance.')
v3_route('post','/me/mfa/totp/enrollment','Begin current account TOTP enrollment',status=201,
    response=ref('V3MFATOTPEnrollment'),
    desc='Optional auxiliary route. Empty body and same-origin browser signal required. Requires one authorized environment and a login from the last ten minutes; older sessions with unknown issuance time must log in again. The pending seed is bound to the exact session, tier and environment, expires after ten minutes, and is returned only once. Treat the secret and otpauth_url as sensitive; do not log or cache.')
v3_route('post','/me/mfa/totp/confirm','Confirm current account TOTP enrollment',
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),response=ref('V3MFATOTPConfirmation'),
    desc='Optional auxiliary route. Same-origin browser signal and recent login required. Consumes one valid TOTP step and returns one-use recovery codes only in this response. The codes are stored only as salted hashes; this does not issue a passkey assurance token.')
v3_route('delete','/me/mfa/totp/enrollment','Cancel pending current account TOTP enrollment',status=204,
    desc='Optional auxiliary route. Empty body, same-origin browser signal, original initiating session and recent login required. Only pending enrollment is removed.')
v3_route('post','/me/mfa/verify','Verify one current account TOTP or recovery code',
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),response=ref('V3MFATOTPVerification'),
    desc='Optional auxiliary route. Same-origin browser signal and one authorized environment required. Consumes a TOTP time step or recovery code once, with lockout after repeated failures. Response is per-call verification only: it creates no login session, step-up proof or passkey approval assurance.')
v3_route('delete','/me/mfa/totp','Remove current account TOTP factor',status=204,
    body=obj({'code':string(minLength=1,maxLength=64)},['code']),
    desc='Optional auxiliary route. Same-origin browser signal, one authorized environment, recent login and a valid TOTP or recovery code required. Removes the active factor and unused recovery hashes without changing passkey approval policy.')
for _method, _path in [('post','/me/mfa/totp/confirm'),('post','/me/mfa/verify'),('delete','/me/mfa/totp')]:
    P[_path][_method]['responses']['429'] = {
        'description':'TOTP verification is temporarily rate limited',
        'content':{'application/json':{'schema':ref('Error')}}}
S['V3PasskeyCredential'] = obj({'id':string(),'label':string(),
    'created_at':string(format='date-time'),'last_used_at':string(format='date-time'),
    'backup_eligible':v3_bool(),'backup_state':v3_bool()},
    ['id','created_at','backup_eligible','backup_state'])
S['V3PasskeyCeremony'] = obj({'id':string(),'options':{'type':'object','additionalProperties':True},
    'expires_at':string(format='date-time')},['id','options','expires_at'])
S['V3PasskeyFinish'] = obj({'challenge_id':string(minLength=32,maxLength=32),
    'credential':{'type':'object','additionalProperties':True},'label':string()},
    ['challenge_id','credential'])
_passkey_boundary = ('Optional self-account WebAuthn route, registered only when '
    'AIOPS_V3_PASSKEY_ENABLED=true with a trusted instance RP domain and exact origin. '
    'Requires an active human session and one authorized V3 environment. Writes require '
    'a same-origin browser signal, an exact trusted HTTPS origin and secure cookies. '
    'The current main-process integration does not enable local HTTP fixtures. Challenges are session, account, '
    'instance, tier and environment bound; these routes issue no proposal approval assurance.')
v3_route('get','/me/passkeys','List current account passkeys',
    response=obj({'credentials':arr(ref('V3PasskeyCredential'))},['credentials']),
    desc=_passkey_boundary+' Credential IDs are base64url; no authenticator private key is stored.')
v3_route('post','/me/passkeys/registration/options','Begin current account passkey registration',
    status=201,response=ref('V3PasskeyCeremony'),
    desc=_passkey_boundary+' Empty body and login within ten minutes required; the challenge expires after five minutes.')
v3_route('post','/me/passkeys/registration/finish','Complete current account passkey registration',
    status=201,body=ref('V3PasskeyFinish'),response=ref('V3PasskeyCredential'),
    desc=_passkey_boundary+' Accepts the browser WebAuthn credential JSON and optional label. Registration has no hardware attestation trust policy.')
v3_route('post','/me/passkeys/assertion/options','Begin account-only passkey assertion',
    status=201,response=ref('V3PasskeyCeremony'),
    desc=_passkey_boundary+' Empty body; at least one active credential is required.')
v3_route('post','/me/passkeys/assertion/finish','Complete account-only passkey assertion',
    body=ref('V3PasskeyFinish'),
    response=obj({'credential_id':string(),'verified':v3_bool(),
        'scope':string(enum=['account_only'])},['credential_id','verified','scope']),
    desc=_passkey_boundary+' A successful account assertion cannot be used as assurance=passkey for a proposal.')
v3_route('post','/me/passkeys/deletion/options','Begin current account passkey deletion',
    status=201,body=obj({'credential_id':string()},['credential_id']),
    response=ref('V3PasskeyCeremony'),
    desc=_passkey_boundary+' Requires recent login and an existing base64url credential ID.')
v3_route('post','/me/passkeys/deletion/finish','Complete current account passkey deletion',
    status=204,body=ref('V3PasskeyFinish'),
    desc=_passkey_boundary+' Requires recent login and a valid assertion for the selected credential; the credential is revoked.')
for _method, _path in [('post','/me/passkeys/registration/options'),
    ('post','/me/passkeys/registration/finish'),('post','/me/passkeys/assertion/options'),
    ('post','/me/passkeys/assertion/finish'),('post','/me/passkeys/deletion/options'),
    ('post','/me/passkeys/deletion/finish')]:
    P[_path][_method]['responses']['409'] = {
        'description':'Credential or challenge state changed, expired or was already used',
        'content':{'application/json':{'schema':ref('Error')}}}
    P[_path][_method]['responses']['429'] = {
        'description':'Passkey ceremony issuance is temporarily rate limited',
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/me/ai-preferences','Read personal AI preferences',response=ref('V3AIPreferences'))
v3_route('put','/me/ai-preferences','Update personal AI preferences',body=ref('V3AIPreferences'),response=ref('V3AIPreferences'))
v3_route('get','/me/notification-settings','Read personal notification settings',response=ref('V3NotificationSettings'),
    desc='Reads the authenticated user\'s persisted preference document. A user with no saved document receives platform defaults. This is not evidence that any notification delivery channel is connected or has delivered a message. Corrupt stored settings return 503 rather than silently replacing them with defaults.')
v3_route('put','/me/notification-settings','Update personal notification settings',body=ref('V3NotificationSettingsPatch'),response=ref('V3NotificationSettings'),
    desc='Merges the authenticated user\'s supported preference fields in a locked, audited transaction and returns the stored result. The current UI sends quiet as a boolean, digest as an enum and subscriptions as existing subscription IDs mapped to booleans. Full objects preserve existing subscription identity and metadata. This saves preferences only; it does not enable or send notifications.')
v3_route('get','/workspace','Read workspace settings',response=ref('V3Workspace'),
    desc='Rechecks the active account and current workspace.view grant for a selected authorized environment in the same read transaction, then returns the persisted workspace record. A missing or invalid record returns 503 workspace_unavailable rather than invented defaults. data_region is a saved deployment declaration, not proof of physical data residency. Saved slug, defaults and retention do not yet drive dependent product behavior; the frozen frontend page still uses mock data.')
v3_route('put','/workspace','Update workspace settings',role='admin',body=ref('V3Workspace'),response=ref('V3Workspace'),
    desc='Requires an active platform administrator with a global workspace.manage grant. Accepts one strict JSON object with known editable fields; rejects unknown or invalid fields. The transaction rechecks authority and returns persisted settings. Data region and branding fields are deployment-owned and cannot be changed here. No version precondition is supported, and the saved slug, default settings, and retention values do not yet drive dependent page behavior.')
v3_route('get','/models','List configured inference models',response=ref('V3ModelCatalog'),
    desc='available means configured, not remotely health-checked. tools reflects the operator-enabled V3 read-tool allowlist rather than provider-wide capability; remote function calling is not attested by this response. Native Ollama returns 503 model_catalog_unavailable until the client ModelInfo.api enum can represent it.')
for _path, _methods in {
    '/me': ['get', 'patch'],
    '/me/profile': ['get', 'put'],
}.items():
    for _method in _methods:
        P[_path][_method]['description'] += ' Currently returns 503 identity_evidence_unavailable until authoritative MFA and on-call sources are connected; writes are not partially applied.'
v3_route('post','/uploads','Store an inert conversation attachment',
    body=obj({'kind':string(enum=['link','paste']),'name':string(),
        'url':string(),'text':string(),'size':integer(),'mime':string()},['kind','name']),
    response=ref('V3Upload'),status=201,
    desc='An explicit single X-AIOps-Environment header is required, including in production. JSON supports link or paste; file and image bytes require multipart. Maximum 20 MiB.')
P['/uploads']['post']['requestBody']['content']['multipart/form-data'] = {'schema':obj({
    'kind':string(enum=['file','image']),
    'file':{'type':'string','format':'binary'}},['kind','file'])}
for upload_status, upload_description in [('413','Attachment too large'),('415','Unsupported media type')]:
    P['/uploads']['post']['responses'][upload_status] = {'description':upload_description,
        'content':{'application/json':{'schema':ref('Error')}}}
v3_route('get','/uploads/{id}/content','Read an owned uploaded file or image',
    desc='Browser reads may omit X-AIOps-Environment; the handler resolves the saved environment and rechecks the current conversation.view grant. Explicit environments must match the saved environment. Files download as opaque octet-stream; verified PNG, JPEG, GIF and WebP images may render inline. No cache; invalid stored images fail closed.')
P['/uploads/{id}/content']['get']['responses']['200'] = {
    'description':'Owned attachment bytes',
    'content':{'application/octet-stream':{'schema':{'type':'string','format':'binary'}},
               'image/png':{'schema':{'type':'string','format':'binary'}},
               'image/jpeg':{'schema':{'type':'string','format':'binary'}},
               'image/gif':{'schema':{'type':'string','format':'binary'}},
               'image/webp':{'schema':{'type':'string','format':'binary'}}}}
