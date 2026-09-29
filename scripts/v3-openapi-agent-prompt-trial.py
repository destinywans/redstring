"""WAN-123 Prompt trial contract refinement.

Execute after v3-openapi-agents.py. This replaces only the provisional
POST /prompts/{id}/trial operation. WAN-117 owns main-contract registration.
"""

_trial_unknown = {'type': 'null'}
_trial_optional_text = {'oneOf': [string(), _trial_unknown]}

S['V3PromptTrialResult'] = obj({
    'version': string(pattern=r'^v[1-9][0-9]{0,8}$'),
    'text': string(),
    'score': _trial_unknown,
    'good': _trial_unknown,
    'score_status': string(enum=['unknown']),
    'tokens': _trial_optional_text,
    'duration': _trial_optional_text,
    'usage_source': string(enum=['provider_reported', 'unknown']),
    'trial_id': string(),
    'model': obj(fieldnames('provider model'), ['provider', 'model']),
}, ['version', 'text', 'score', 'good', 'score_status', 'tokens', 'duration',
    'usage_source', 'trial_id', 'model'])

v3_route('post', '/prompts/{id}/trial', 'Run a bounded two-version Prompt trial',
    role='viewer with scoped agents.change',
    body=obj({
        'case': string(minLength=1, maxLength=128),
        'a': string(pattern=r'^v[1-9][0-9]{0,8}$'),
        'b': string(pattern=r'^v[1-9][0-9]{0,8}$'),
    }, ['case', 'a', 'b']),
    response=arr(ref('V3PromptTrialResult')),
    desc='Requires Idempotency-Key, one selected environment, current session '
         'and scoped Agent authorization. Both distinct A/B versions must '
         'be persisted on this Prompt; the registered case must also have '
         'trusted versioned input content. A registered case reference alone '
         'does not provide runnable input. A configured restricted model '
         'adapter runs at most one no-tools call per version under input, '
         'output and time caps plus a reviewed cost admission estimate. Success persists both outputs and '
         'their exact case/version/model provenance before returning a raw '
         'two-item array. No automatic scorer is connected: score and good '
         'are null, score_status is unknown. Provider usage may be null. '
         'An in-flight or invalidated request returns 409; no configured '
         'runner returns 503; a failed or ambiguous upstream attempt '
         'returns 502, never a fabricated partial success.')
P['/prompts/{id}/trial']['post'].setdefault('parameters', []).append({
    'name': 'Idempotency-Key', 'in': 'header', 'required': True,
    'schema': string(minLength=8, maxLength=128),
    'description': 'Scoped to caller, Prompt, case and A/B versions. Reuse after response loss.'
})
for _status, _description in {
    '409': 'Case input, version or idempotency conflict; an identical trial may still be in flight',
    '503': 'Scoped Prompt trial model runner is not connected or storage/policy is unavailable',
    '502': 'Model attempt failed or had ambiguous completion; no partial trial result is returned',
}.items():
    P['/prompts/{id}/trial']['post']['responses'][_status] = {
        'description': _description,
        'content': {'application/json': {'schema': ref('Error')}}}
