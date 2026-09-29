# Browser Webhook overview currently has one independently verifiable inbound
# source family: the generic, application-bound V3 Push ingress.
S['V3WebhookInbound'] = obj({
    **fieldnames('id source_id event_type brand name url verify count last status_basis'),
    'status':string(enum=['ok','error','unverified']),
}, ['id','source_id','event_type','brand','name','url','verify','count','last','status','status_basis'])
S['V3WebhookReceipt'] = obj(fieldnames('time event code ms'), ['time','event','code','ms'])
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
    desc='Shows only configured generic V3 Push source/event routes whose application and environment are currently visible. '
    'The count is accepted, non-synthetic ingress receipts in the past 24 hours; last and status reflect only the latest receipt in that window, not continuous source health. '
    'An unobserved route has status=unverified. Delivery rows are inbound receipts, not outbound notification delivery. '
    'No visible generic source is reported as no_visible_sources; this does not claim no source exists in another environment. '
    'API tokens, native vendor receivers, public MCP serving and outbound delivery lack one connected V3 registry here; their arrays are empty and availability explicitly says not_connected. '
    'No credential or source secret is returned. The frozen frontend only types ok/error for inbound status and may need to render unverified and availability.')

# The V3 Alertmanager receiver replaces the historical browser-independent
# route with a source-token and fixed mapping boundary. It never accepts a
# client-selected environment and does not require a session cookie.
P['/webhooks/alertmanager/{source}']['post']['description'] = (
    'V3 source Bearer token required. Each alert must uniquely match a configured '
    'application/environment mapping that is still active in this instance. '
    'The receiver atomically persists incidents and deduplicated occurrences; '
    'unmapped or cross-instance events are rejected. External notification '
    'delivery remains disconnected until a V3-authorized worker is available.')
P['/webhooks/alertmanager/{source}']['post']['responses']['202']['content']['application/json']['schema'] = obj({
    'accepted': integer(),
    'notification_delivery': {'type': 'string', 'enum': ['not_configured', 'not_connected']},
}, ['accepted', 'notification_delivery'])
