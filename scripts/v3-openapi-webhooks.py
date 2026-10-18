# Browser Webhook overview combines the generic Push ingress with optional
# metadata from already-installed native Alertmanager and GitLab receivers.
S['V3WebhookInbound'] = obj({
    **fieldnames('id source_id event_type brand name url verify count status_basis'),
    'last_at':{**v3_nullable(string(format='date-time')),'description':'PA-83: the latest receipt time in RFC 3339, null when unknown'},
    'last_code':{**v3_nullable(integer()),'description':'PA-83: the HTTP status of the latest receipt, null when unknown'},
    'status':string(enum=['ok','error','unverified']),
}, ['id','source_id','event_type','brand','name','url','verify','count','last_at','last_code','status','status_basis'])
# PA-227: source_id and event_type name the inbound entry (inbound[].id is
# "<source_id>:<event_type>") so the page lists the selected entry's receipts.
S['V3WebhookReceipt'] = obj(fieldnames('time source_id event_type event code ms'), ['time','source_id','event_type','event','code','ms'])
S['V3WebhookAvailability'] = obj(fieldnames(
    'inbound native_inbound api_tokens public_mcp outbound receipt_window'),
    ['inbound','native_inbound','api_tokens','public_mcp','outbound','receipt_window'])
S['V3WebhookView'] = obj({
    'inbound':arr(ref('V3WebhookInbound')),
    'deliveries':arr(ref('V3WebhookReceipt')),
    'tokens':arr(obj({**fieldnames('name used expires'),'scopes':arr(string())},
        ['name','scopes','used','expires'])),
    'mcp':obj({'enabled':v3_bool(),'url':string(),'tools':arr(string()),'clients':string()},
        ['enabled','url','tools','clients']),
    'outbound':arr(v3_object('event target brand')),
    'availability':ref('V3WebhookAvailability'),
}, ['inbound','deliveries','tokens','mcp','outbound','availability'])
v3_route('get','/webhooks','Read scoped Webhook and API integration overview',
    role='viewer with scoped connectors.view and services.view',response=ref('V3WebhookView'),
    desc='Shows only configured generic V3 Push routes and optionally injected native Alertmanager/GitLab receivers whose application and environment are currently visible. '
    'The generic count is accepted, non-synthetic ingress receipts in the past 24 hours; its last and status reflect only the latest receipt in that window, not continuous source health. '
    'Alertmanager count and last derive from accepted deduplicated occurrences in the past 24 hours; its status stays unverified because acceptance does not prove ongoing sender health. '
    'GitLab historical receipts lack persisted application/environment attribution and may predate a project rebind, so its count and last are unknown and status remains unverified. '
    'Delivery rows are generic Push inbound receipts only, not vendor receipts or outbound delivery; each carries the source_id and event_type of its inbound entry, at most the latest 30 per entry (PA-227). No visible source is reported as no_visible_sources without exposing other environments. '
    'API tokens, public MCP serving and outbound delivery are not connected; their arrays are empty and availability states disclose that limit. Outbound redelivery is disabled. '
    'No credential or source secret is returned. The frozen frontend only types ok/error for inbound status and may need to render unverified and availability.')

# The V3 Alertmanager receiver replaces the historical browser-independent
# route with a source-token and fixed mapping boundary. It never accepts a
# client-selected environment and does not require a session cookie.
P['/webhooks/alertmanager/{source}']['post']['description'] = (
    'V3 source Bearer token required. Each alert must uniquely match a configured '
    'application/environment mapping that is still active in this instance. '
    'The receiver atomically persists incidents and deduplicated occurrences; '
    'unmapped or cross-instance events are rejected. An alert whose own environment '
    'label names another environment than its mapping is not stored (D32): it is '
    'logged and counted in ignored, and the rest of the envelope is stored. External notification '
    'delivery remains disconnected until a V3-authorized worker is available.')
P['/webhooks/alertmanager/{source}']['post']['responses']['202']['content']['application/json']['schema'] = obj({
    'accepted': integer(),
    'ignored': integer(description='Alerts not stored because their environment label differs from their mapping (D32)'),
    'notification_delivery': {'type': 'string', 'enum': ['not_configured', 'not_connected']},
}, ['accepted', 'ignored', 'notification_delivery'])
v3_route('post','/webhooks','Request an inbound Webhook registration',
    role='human connectors.manage',body=obj({'name':string(maxLength=1000),'type':string(enum=['change','alert','incident']),'verify':string(enum=['hmac','token'])},['name','type','verify']),
    response=obj({'id':string(),'url':string(),'secret_shown_once':string()},['id','url','secret_shown_once']),status=201,
    desc='Currently fail-closed with 503 webhook_registration_unavailable after authorized failure audit. The request cannot identify an exact application/environment/source binding and the live ingress consumes deployment-owned static Push sources. No URL, secret or unconsumable registration is minted. Dynamic admission and verified ingress consumption must be implemented before 201 is available.')
