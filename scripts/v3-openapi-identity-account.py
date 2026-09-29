v3_route('get','/me','Read current account',response=ref('V3Me'),
    desc='Returns the current active human account, team, department, assigned role IDs, preferences and instance-scoped active TOTP/passkey registration state. MFA fields are enrollment evidence, not proof of enforced login MFA or proposal approval assurance. Current on-call state must come from an operator-configured authoritative HTTPS source covering the requested environments with a fresh, unambiguous assignment answer. Missing, stale, malformed or mismatched source evidence returns 503 oncall_unavailable; it never means inactive. The current frontend shell depends on this route and cannot show a complete account card until that source is configured.')
v3_route('patch','/me','Update current account',body=ref('V3MePatch'),response=ref('V3Me'),
    desc='Requires one selected authorized environment and an active human account/session. Only name, title, timezone and supported preference fields are self-editable; email, phone, team, department, roles, MFA and on-call are server-owned and rejected. The transaction locks the account before its session, rechecks authority and evidence, then saves profile/preferences and audit atomically before returning the full Me response. Missing or stale on-call evidence returns 503 without changing the account. Production startup must inject the configured on-call source; without it the route remains unavailable.')
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
    desc='Rechecks the active account and current workspace.view grant for a selected authorized environment in the same read transaction, then returns the persisted workspace record and its strong revision ETag. A missing or invalid record returns 503 workspace_unavailable rather than invented defaults. data_region is a saved deployment declaration, not proof of physical data residency. Saved slug, week start and retention do not yet drive dependent product behavior; the frozen frontend page still uses mock data.')
v3_route('put','/workspace','Update workspace settings',role='admin',body=ref('V3Workspace'),response=ref('V3Workspace'),
    desc='Requires one selected environment, an active platform administrator with a global workspace.manage grant, and the exact strong ETag from GET /workspace in If-Match. Accepts one complete strict eight-field JSON object including complete brand fields; rejects missing, duplicate, unknown or invalid fields. The transaction rechecks authority and row revision, then commits settings and audit together and returns the persisted record with a new ETag. Data region and branding fields are deployment-owned and must match the saved values. Newly created local and imported directory members inherit saved locale and timezone; existing members are unchanged. Saved slug, week start and retention do not yet drive dependent product behavior. The current frontend does not forward ETag, so its real-mode save receives 428 until adapted.')
_workspace_etag = {'description':'Strong ETag for the persisted workspace_settings revision',
    'schema':string(pattern=r'^"[1-9][0-9]*"$')}
for _method in ('get','put'):
    P['/workspace'][_method]['responses']['200']['headers'] = {'ETag':_workspace_etag}
P['/workspace']['put'].setdefault('parameters', []).append({
    'name':'If-Match','in':'header','required':True,'schema':string(pattern=r'^"[1-9][0-9]*"$'),
    'description':'Exactly one strong revision ETag returned by GET /workspace.'})
for _code, _description in [('400','Malformed or multiple If-Match values'),
    ('412','Workspace revision changed'),('428','If-Match precondition required')]:
    P['/workspace']['put']['responses'][_code] = {
        'description':_description,'content':{'application/json':{'schema':ref('Error')}}}
P['/workspace']['put']['responses']['409']['description'] = 'All-environment read-only scope cannot be written; stale If-Match revisions return 412'
v3_route('get','/models','List configured inference models',response=ref('V3ModelCatalog'),
    desc='available means configured, not remotely health-checked. tools reflects the operator-enabled V3 read-tool allowlist rather than provider-wide capability; remote function calling is not attested by this response. Native Ollama returns 503 model_catalog_unavailable until the client ModelInfo.api enum can represent it.')
S['V3ModelObservation'] = obj({
    'model':string(maxLength=320),
    'status':string(enum=['configured_unverified','verified','error','unsupported']),
    'checked_at':{'anyOf':[string(format='date-time'),{'type':'null'}]},
    'latency_ms':{'anyOf':[integer(minimum=0),{'type':'null'}]},
    'error_code':string(maxLength=80),
},['model','status','checked_at','latency_ms'])
v3_route('post','/models/check','Check configured model metadata',
    body=obj({'model':string(minLength=1,maxLength=320)},['model']),
    response=ref('V3ModelObservation'),
    desc='Auxiliary models.manage route for one selected environment. Checks only a credential-free literal loopback model metadata endpoint with a five-second limit. Remote or credentialed models return unsupported without a request. The response records one observation, not generation, streaming, tools, or future health. A write transaction rechecks current authority and audits the observation.')
v3_route('get','/models/observations','Read one model metadata observation',
    query=[{'name':'model','in':'query','required':True,'schema':string(minLength=1,maxLength=320)}],
    response=ref('V3ModelObservation'),
    desc='Auxiliary models.view route for one selected environment. Missing checks and observations from another service process return configured_unverified with null checked_at and latency_ms; current verification is not shared across replicas. No endpoint, credentials, or upstream body is returned.')
for _method in ('get','patch'):
    P['/me'][_method]['responses']['503']['description'] = 'Authoritative current on-call evidence is unavailable or stale'
P['/me/profile']['get']['description'] += (' Requires one selected authorized environment and a current human account/session. '
    'An operator-configured trusted HTTPS source must return a fresh, complete, exact instance/user/environment/contact/session-bound '
    'snapshot of contact verification, MFA enrollment and factors, organization rule, and device/login method for every active session. '
    'Factor types and counts must agree with the same-instance TOTP/passkey registrations used by GET /me. '
    'The account database supplies editable fields, notifications, password audit time and session expiry. Unknown, stale, incomplete '
    'or unconfigured evidence returns 503 identity_evidence_unavailable; no security state is guessed.')
P['/me/profile']['put']['description'] += (' Accepts one complete ProfileView in a selected authorized environment. Only name, display_name, '
    'title, timezone and notifications are editable; department, contacts and every security field must exactly match the current '
    'authoritative view. The source is called before database locks, then the transaction rechecks exact session, human account, '
    'environment.view, source freshness and the complete contact/session set before writing fields and a secret-free audit atomically. '
    'Unconfigured, stale or incomplete evidence returns 503 without a partial write. Production startup must inject both the trusted '
    'source and instance ID; a configured source has not yet been verified against a real provider.')
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
