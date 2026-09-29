"""Bounded, review-only alert rule generation and sampled preview.

Loaded after v3-openapi-alerts.py by the aggregate V3 contract builder.
"""

S['V3RuleDraftGenerateRequest'] = obj({
    'text': string(minLength=1, maxLength=2048),
    'application_id': string(minLength=1, maxLength=128)}, ['text', 'application_id'])
S['V3RuleDraftModel'] = obj({'provider': string(), 'name': string()}, ['provider', 'name'])
S['V3GeneratedRuleDraft'] = obj({
    'query': string(), 'op': string(enum=['>', '<']), 'threshold': v3_num(),
    'unit': string(description='Reviewed seed unit only for an identical query; unknown after expression changes.'), 'for_minutes': integer(minimum=0, maximum=60),
    'severity': string(enum=['P1', 'P2', 'P3', 'P4']), 'service': string(),
    'summary': string(), 'application_id': string(), 'environment_id': string(),
    'query_id': string(), 'draft_id': string(),
    'status': string(enum=['review_required']), 'model': ref('V3RuleDraftModel')},
    ['query', 'op', 'threshold', 'unit', 'for_minutes', 'severity', 'service',
     'summary', 'application_id', 'environment_id', 'query_id', 'draft_id',
     'status', 'model'])
v3_route('post', '/alert-rules/generate', 'Generate a review-only alert rule draft',
    role='operator', body=ref('V3RuleDraftGenerateRequest'),
    response=ref('V3GeneratedRuleDraft'),
    desc='Requires one selected environment, explicit application_id, and current application or owner-team alert_rules.change before catalog access. The server uses one explicitly configured bounded model call with no tools and validates its JSON and PromQL against the reviewed application-isolated query catalog. A changed expression has unit=unknown until human review. It persists an audited attempt; it never creates, enables, or deploys an alert rule. The Idempotency-Key is actor-bound, 8–128 characters, and one key never causes a second model call. A running key returns 409; new-key rate limit returns 429; a stale, uncertain, or provider-budget-violating result returns 502 and requires inspection before a new key. The cost cap is a configured price estimate for admission, not provider billing proof. Unconfigured model or catalog returns 503.')
P['/alert-rules/generate']['post'].setdefault('parameters', []).append({
    'name': 'Idempotency-Key', 'in': 'header', 'required': True,
    'schema': string(minLength=8, maxLength=128),
    'description': 'Actor-bound request key for one model attempt.'})
P['/alert-rules/generate']['post']['responses']['200']['headers'] = {
    'X-Rule-Draft-Attempt-ID': {'schema': string(),
        'description': 'Persisted attempt ID, also returned on replay and known failures.'}}
for _code, _description in [('502', 'Model failed, returned an invalid draft, or outcome is uncertain')]:
    P['/alert-rules/generate']['post']['responses'][_code] = {
        'description': _description,
        'content': {'application/json': {'schema': ref('Error')}}}
P['/alert-rules/generate']['post']['responses']['429'] = {
    'description': 'New-key model draft rate limit exceeded',
    'content': {'application/json': {'schema': ref('Error')}}}

S['V3RulePreviewRequest'] = obj({
    'application_id': string(minLength=1, maxLength=128),
    'query': string(minLength=1, maxLength=2048),
    'op': string(enum=['>', '<']), 'threshold': v3_num(),
    'for_minutes': integer(minimum=0, maximum=60),
    'range': string(enum=['1h', '24h', '7d'])},
    ['application_id', 'query', 'op', 'threshold', 'for_minutes', 'range'])
S['V3RuleSampleWindow'] = obj({
    'start': string(format='date-time'), 'end': string(format='date-time')},
    ['start', 'end'])
S['V3RulePreviewPoint'] = obj({
    'at': string(format='date-time'), 'value': v3_num()}, ['at', 'value'])
S['V3RuleSampledBacktest'] = obj({
    'status': string(enum=['sampled']),
    'from': string(format='date-time'), 'to': string(format='date-time'),
    'step_seconds': integer(enum=[300]), 'sampled_count': integer(minimum=0),
    'count': {'type': ['integer', 'null'], 'description': 'Unknown historical total; currently null.'},
    'avg_minutes': {'type': ['number', 'null']},
    'max_minutes': {'type': ['number', 'null']},
    'overlaps': {'type': ['object', 'array', 'null']},
    'noise': {'type': ['object', 'array', 'null']},
    'sampled_windows': arr(ref('V3RuleSampleWindow')),
    'completeness_notice': string()},
    ['status', 'from', 'to', 'step_seconds', 'sampled_count', 'count',
     'avg_minutes', 'max_minutes', 'overlaps', 'noise', 'sampled_windows',
     'completeness_notice'])
S['V3RulePreviewResult'] = obj({
    'valid': {'type': 'boolean', 'description': 'Query passed admission; coverage may still be no_observed_series.'}, 'series_count': integer(minimum=0), 'source': string(),
    'application_id': string(), 'environment_id': string(), 'query_id': string(),
    'unit': string(description='Reviewed seed unit only for an identical query; unknown after expression changes.'), 'range': string(enum=['1h', '24h', '7d']),
    'values': arr(v3_num()), 'points': arr(ref('V3RulePreviewPoint')),
    'current': {'type': ['number', 'null']},
    'fires': {'type': ['boolean', 'null'], 'description': 'Currently null; sampled historical windows are separate.'},
    'trigger_intervals': arr(ref('V3RuleSampleWindow')),
    'backtest': {'anyOf': [ref('V3RuleSampledBacktest'), {'type': 'null'}]},
    'route': {'type': ['object', 'null']},
    'route_status': string(enum=['unknown']),
    'similar': {'type': ['array', 'null']},
    'similar_status': string(enum=['unknown']),
    'coverage': string(enum=['sampled_5m', 'no_observed_series'])},
    ['valid', 'series_count', 'source', 'application_id', 'environment_id',
     'query_id', 'unit', 'range', 'values', 'points', 'current', 'fires',
     'trigger_intervals', 'backtest', 'route', 'route_status', 'similar',
     'similar_status', 'coverage'])
v3_route('post', '/alert-rules/preview', 'Preview a bounded rule draft from Prometheus samples',
    role='viewer', body=ref('V3RulePreviewRequest'), response=ref('V3RulePreviewResult'),
    desc='Requires one selected environment, explicit application_id, and current application or owner-team alert_rules.view before catalog access. The query must parse within exactly one reviewed PromQL metric and label scope. One configured application-isolated Prometheus query_range reads the last seven days at a five-minute step; values and points reflect the requested display range. A changed expression has unit=unknown until human review. Trigger intervals and sampled_count are sampled evaluations, not historical alert records or complete counts. Unknown full-history count, noise, overlap, route, and similar-rule data remain null or unknown. The endpoint checks scope and grants again after upstream I/O and never creates or enables a rule. Missing source or incomplete/over-limit provider data returns 503; a per-actor/application/environment preview budget may return 429.')
P['/alert-rules/preview']['post']['responses']['429'] = {
    'description': 'Preview rate limit exceeded',
    'content': {'application/json': {'schema': ref('Error')}}}
