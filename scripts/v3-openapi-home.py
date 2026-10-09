# The home projection uses persisted authorized facts. Nullable extensions are
# deliberate: the frozen frontend HomeSummary assumes AI/on-call/telemetry
# producers exist and must adapt before consuming unknown values safely.
S['V3HomeKPI'] = obj({'id': string(), 'label': string(), 'value': string(),
    'series': arr({'type': 'number'}), 'tone': string()},
    ['id', 'label', 'value', 'series'])
S['V3HomeAttention'] = obj({'id': string(), 'severity': {'type': ['string', 'null'],
    'enum': ['P1', 'P2', 'P3', 'P4', None]}, 'service': string(), 'env': string(),
    'symptom': string(), 'meta': string(), 'ai': {'type': ['string', 'null'],
    'enum': ['done', 'running', None]}, 'owner': string(), 'href': string(),
    'alert_state': {'type': ['string', 'null'], 'enum': ['firing', 'resolved', None],
        'description': 'Current alert state of the incident; null when the source reports neither. Resolved incidents stay listed until claimed (PA-58, D7).'}},
    ['id', 'severity', 'service', 'env', 'symptom', 'meta', 'ai', 'href', 'alert_state'])
S['V3HomeService'] = obj({'id': string(), 'name': string(), 'env': string(),
    'tier': {'type': ['string', 'null'], 'enum': ['T0', 'T1', 'T2', None]},
    'status': {'type': ['string', 'null'], 'enum': ['ok', 'warn', 'danger', None]},
    'budget_pct': {'type': ['number', 'null']}, 'p99': {'type': ['string', 'null']},
    'error_rate': {'type': ['string', 'null']},
    'series': arr({'type': 'number'})},
    ['id', 'name', 'env', 'tier', 'status', 'budget_pct', 'p99', 'error_rate', 'series'])
S['V3HomeActivity'] = obj({**fieldnames('id time kind tone text href')},
    ['id', 'time', 'kind', 'tone', 'text', 'href'])
S['V3HomeRecent'] = obj({**fieldnames('id kind title meta href')},
    ['id', 'kind', 'title', 'meta', 'href'])
S['V3HomeBriefing'] = obj({'summary': string(),
    'generated_at': string(format='date-time'),
    'citations': arr(obj({'n': integer(), 'label': string(), 'href': string()},
        ['n', 'label', 'href'])),
    'oncall': obj(fieldnames('name until next'), ['name', 'until', 'next'])},
    ['summary', 'generated_at', 'citations', 'oncall'])
S['V3HomeAlertTrend'] = obj({'labels': arr(string(description='UTC HH:MM bucket starts, kept for compatibility')),
    'start': string(format='date-time', description='Start of bucket 0 (whole seconds, UTC); bucket i covers [start+i*step, start+(i+1)*step)'),
    'step_seconds': {'type': 'number', 'exclusiveMinimum': 0, 'description': 'Bucket width in seconds; may be fractional (15m range: 37.5). Label buckets in the viewer time zone from start and step (PA-25).'},
    'counts': arr(arr(integer(minimum=0))), 'summary': string()},
    ['labels', 'start', 'step_seconds', 'counts', 'summary'])
S['V3HomeSummary'] = obj({
    'greeting': obj(fieldnames('name summary'), ['name', 'summary']),
    'briefing': {'anyOf': [ref('V3HomeBriefing'), {'type': 'null'}]},
    'suggestions': arr(obj({'id': string(), 'text': string(), 'tone': string(),
        'icon': string()}, ['id', 'text', 'icon'])),
    'kpis': arr(ref('V3HomeKPI')),
    'attention': arr(ref('V3HomeAttention')),
    'todos': arr(obj({**fieldnames('id kind title meta action href')},
        ['id', 'kind', 'title', 'meta', 'action', 'href'])),
    'services': arr(ref('V3HomeService')),
    'alert_trend': {'anyOf': [ref('V3HomeAlertTrend'), {'type': 'null'}]},
    'activity': arr(ref('V3HomeActivity')),
    'recent': arr(ref('V3HomeRecent')),
    'firing_services': {'type': ['integer', 'null'], 'minimum': 0,
        'description': 'Distinct visible services with a firing, non-suspected alert; null without an events grant (PA-58, D7).'},
    'availability': {'type': 'object', 'additionalProperties': string()},
}, ['greeting', 'briefing', 'suggestions', 'kpis', 'attention', 'todos',
    'services', 'alert_trend', 'activity', 'recent', 'firing_services', 'availability'])
v3_route('get', '/home', 'Read the authenticated scoped home summary',
    response=ref('V3HomeSummary'),
    desc='Uses an active session and a bounded consistent snapshot of application '
         'registrations, incidents, pending proposals, and owner conversations. '
         'Each section requires its own current V3 module grant and exact '
         'application or owner scope. Counts describe persisted visible records, '
         'not external platform health. AI briefing, authoritative on-call, '
         'service health and SLO numbers remain null until actual producers '
         'are wired. The optional trend counts only persisted occurrences with '
         'recognized severity, not complete external alert activity; '
         'availability explains each gap. '
         'The frozen HomeSummary has non-null assumptions for these sections '
         'and needs a later frontend type/rendering update.')
