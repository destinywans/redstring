# Identity and personal settings. Server returns exact data; unavailable MFA
# features are described as unavailable in the profile rather than simulated.
S['V3Preferences'] = obj({'theme': string(enum=['light','dark','system']),
    'locale': string(enum=['zh-CN','en-US']), 'density': string(enum=['comfortable','compact']),
    'reduce_motion': v3_bool()}, ['theme','locale','density','reduce_motion'])
S['V3PreferencesPatch'] = obj(S['V3Preferences']['properties'])
S['V3Me'] = v3_object('id name email phone title team department timezone', {
    'avatar_url': string(), 'oncall': obj({'active': v3_bool(), 'until': string(), 'schedule': string()}, ['active']),
    'roles': arr(string()), 'mfa': obj({'totp': v3_bool(), 'passkeys': integer()}, ['totp','passkeys']),
    'preferences': ref('V3Preferences')},
    ['id','name','email','phone','title','team','department','timezone','oncall','roles','mfa','preferences'])
S['V3MePreferencesPatch'] = obj(S['V3Preferences']['properties'])
S['V3MePreferencesPatch']['minProperties'] = 1
S['V3MePatch'] = obj({'name':string(minLength=1,maxLength=120),
    'title':string(maxLength=120),'timezone':string(),
    'preferences':ref('V3MePreferencesPatch')})
S['V3MePatch']['minProperties'] = 1
S['V3Workspace'] = v3_object('name slug timezone locale week_start data_region', {
    'retention_days': integer(), 'brand': obj({'name': string(), 'logo': {'type':['string','null']}, 'pending': v3_bool()}, ['name','logo','pending'])},
    ['name','slug','timezone','locale','week_start','retention_days','data_region','brand'])
S['V3EnvironmentInfo'] = v3_object('id label note', {
    'kind': string(enum=['standing','feature']), 'tone': string(enum=['prod','stg','dev']), 'clusters': arr(string())},
    ['id','label','kind','tone','clusters'])
S['V3InstanceInfo'] = v3_object('name domain default_env', {
    'tier': string(enum=['production','nonprod']), 'environments': arr(ref('V3EnvironmentInfo')),
    'other': v3_object('label url domain')}, ['tier','name','domain','environments','default_env'])
S['V3LowerSource'] = v3_object('id name brand env env_label purpose credential',
    required=['id','name','brand','env','env_label','purpose','credential'])
S['V3ProfileView'] = v3_object('name display_name department title timezone', {
    'contacts': arr(obj({'kind': string(enum=['email','phone','feishu']), **fieldnames('label value'), 'verified': v3_bool(), 'note': string()},
                        ['kind','label','value','verified'])),
    'notifications': arr(obj({'event': string(), 'label': string(),
        'channels': obj({k:v3_bool() for k in ['feishu','email','sms','phone']}, ['feishu','email','sms','phone'])},
        ['event','label','channels'])),
    'security': obj({'password_changed': string(), 'mfa_enabled': v3_bool(), 'org_rule': string(),
        'factors': arr(obj({'kind':string(enum=['passkey','totp','recovery']), **fieldnames('name sub')},
                            ['kind','name','sub'])),
        'sessions': arr(obj({**fieldnames('id device sub'), 'current': v3_bool()}, ['id','device','sub'])),
        'tokens': string()}, ['password_changed','mfa_enabled','org_rule','factors','sessions','tokens'])},
    ['name','display_name','department','title','timezone','contacts','notifications','security'])
S['V3ModelChoice'] = obj({'model': string(), 'reasoning': string()}, ['model'])
S['V3ModelInfo'] = v3_object('id name provider brand connector cost note unavailable_reason', {
    'api': string(enum=['responses','chat_completions','messages']),
    'reasoning': obj({'kind': string(enum=['levels','toggle','none']),
        'levels': arr(v3_object('id label hint')), 'default': string()}),
    'tools': {'type':'boolean','description':'True only when this configured model is explicitly enabled for V3 read tools by the server operator; not proof of successful remote function calling.'},
    'vision': v3_bool(), 'context_k': integer(), 'available': v3_bool()})
S['V3ModelCatalog'] = obj({'items': arr(ref('V3ModelInfo')), 'default': ref('V3ModelChoice')}, ['items','default'])
S['V3Upload'] = obj({'id':string(),'kind':string(enum=['file','image','link','paste']),
    'name':string(),'size':integer(minimum=0),'mime':string(),'url':string()},
    ['id','kind','name','size','mime'])
S['V3AIPreferences'] = obj({'default': ref('V3ModelChoice'), 'canvas_inherit': v3_bool(),
    'show_reasoning': string(enum=['expanded','collapsed','hidden']), 'auto_context': v3_bool(),
    'memory': v3_bool(), 'budget_warn_tokens': integer()},
    ['default','canvas_inherit','show_reasoning','auto_context','memory','budget_warn_tokens'])
S['V3NotificationSettings'] = obj({'quiet': obj({'enabled': v3_bool(), 'from': string(), 'to': string()}, ['enabled','from','to']),
    'digest': string(enum=['off','daily','weekly']),
    'subscriptions': arr(obj({**fieldnames('id label sub'), 'on': v3_bool()}, ['id','label','sub','on']))},
    ['quiet','digest','subscriptions'])
S['V3NotificationSettingsPatch'] = obj({
    'quiet': {'oneOf':[v3_bool(), obj({'enabled':v3_bool(),'from':string(),'to':string()}, ['enabled','from','to'])]},
    'digest': string(enum=['off','daily','weekly']),
    'subscriptions': {'oneOf':[arr(obj({**fieldnames('id label sub'),'on':v3_bool()}, ['id','label','sub','on'])),
        {'type':'object','additionalProperties':v3_bool()}]},
})
S['V3NotificationSettingsPatch']['minProperties'] = 1
v3_route('get','/instance','Read the configured V3 isolation boundary',role='public',response=ref('V3InstanceInfo'))
v3_route('get','/integrations/lower-sources','List configured lower-environment source metadata',
    response=listed('V3LowerSource'),
    desc='Requires connectors.view in the production environment. Only available on the production instance; non-production returns 404 after route authorization. The credential field reports that a Vault reference is configured, not that connectivity or read-only source credentials have been verified. Source endpoints and Vault references are never returned.')
P['/integrations/lower-sources']['get']['responses']['404'] = {
    'description':'Not available on a non-production instance',
    'content':{'application/json':{'schema':ref('Error')}}}
