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
