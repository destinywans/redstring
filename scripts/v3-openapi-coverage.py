# V3 coverage is a configuration-evidence matrix, not a claim that every
# registered source is currently delivering telemetry. The frozen frontend
# CoverageView uses non-null numbers and has no unknown cell state; that type
# needs a separate frontend adaptation before rendering this safe response.
S['V3CoverageCell'] = obj({
    'state': string(enum=['partial', 'missing', 'unknown']),
    'brand': string(), 'text': string(),
    'availability': string(enum=['configured_unverified', 'not_configured', 'unavailable']),
}, ['state', 'availability'])
S['V3CoverageRow'] = obj({
    'id': string(), 'service': string(), 'environment_id': string(),
    'tier': string(), 'owner': string(),
    'cells': v3_map(ref('V3CoverageCell')),
    'score': {'type': ['integer', 'null'], 'minimum': 0, 'maximum': 100},
    'score_available': v3_bool(),
}, ['id', 'service', 'environment_id', 'tier', 'cells', 'score', 'score_available'])
S['V3CoverageView'] = obj({
    'kpis': arr(obj({'id': string(), 'label': string(), 'value': string(),
                     'sub': string(), 'series': arr({'type': 'number'})},
                    ['id', 'label', 'value', 'series'])),
    'types': arr(obj({'id': string(), 'label': string(),
                      'pct': {'type': ['integer', 'null'], 'minimum': 0, 'maximum': 100},
                      'tone': string()}, ['id', 'label', 'pct', 'tone'])),
    'columns': arr(obj(fieldnames('id label'), ['id', 'label'])),
    'rows': arr(ref('V3CoverageRow')),
    'impact': obj({'answers': string(), 'flagged': string(), 'delta': string(),
                   'lowered': string(),
                   'by_type': arr(obj({'label': string(), 'value': {'type': ['integer', 'null']}},
                                      ['label', 'value'])),
                   'eval_failures': {'type': ['integer', 'null'], 'minimum': 0},
                   'availability': string()},
                  ['answers', 'flagged', 'delta', 'lowered', 'by_type', 'eval_failures', 'availability']),
    'fixes': arr(obj(fieldnames('text impact action href'),
                     ['text', 'impact', 'action', 'href'])),
    'availability': obj(fieldnames('live_coverage scoring agent_impact'),
                        ['live_coverage', 'scoring', 'agent_impact']),
}, ['kpis', 'types', 'columns', 'rows', 'impact', 'fixes', 'availability'])
v3_route('get', '/coverage', 'Read scoped service configuration and coverage evidence',
    response=ref('V3CoverageView'), query=[v3_query('app')],
    desc='Lists up to 200 authorized application/environment profiles. Each row requires '
         'services.view; connector, resource and Runbook cells additionally require their '
         'own view permission. A matching V3 connector binding or target proves only '
         'configuration, and an indexed CMDB resource or stored Runbook proves only '
         'registration. No probe or inferred health becomes live coverage. Missing means '
         'a precise binding or association is not configured, not that external data is '
         'absent. Ambiguous or unauthorized evidence is unknown. The app filter accepts '
         'one exact application ID or key. Score, per-type percentages and eval-failure '
         'attribution are null until a measured coverage/Agent impact source exists; '
         'availability fields explain this. The frozen frontend CoverageView requires '
         'non-null numbers and has no unknown cell state, so page integration requires '
         'a frontend type/rendering update later.')
