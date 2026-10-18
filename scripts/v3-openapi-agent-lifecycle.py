"""WAN-123 Agent lifecycle contract refinement.

Execute after v3-openapi-agents.py. The five operations below replace that
fragment's provisional lifecycle operations; other Agent routes are untouched.
No runtime traffic router or aggregate lifecycle producer is connected.
"""

_agent_unknown = {'type': 'null'}
_agent_pending = string(enum=['configuration_saved_pending_activation'])
_agent_revision = integer(minimum=1, format='int64')

S['V3AgentEvidenceSummary'] = obj({
    **fieldnames('id icon name desc version level status status_source'),
    'revision': _agent_revision,
    'score': _agent_unknown,
    'runs_7d': _agent_unknown,
    'success': _agent_unknown,
    'avg_tokens': _agent_unknown,
    'available': obj({'evaluation': v3_bool(), 'usage': v3_bool()},
                     ['evaluation', 'usage']),
    'routing_connected': v3_bool(),
}, ['id', 'icon', 'name', 'desc', 'version', 'level', 'status', 'status_source',
    'revision', 'score', 'runs_7d', 'success', 'avg_tokens', 'available',
    'routing_connected'])

_agent_release = obj({
    **fieldnames('from to'),
    # PA-236: the version a rollback goes to, by the same rule the rollback runs.
    'rollback_to': {'type': ['string', 'null'], 'description': 'The stable version while a canary runs, else the previous stable version; null when there is nothing to roll back to'},
    'target': integer(minimum=0, maximum=100),
    'configured_canary_percent': integer(minimum=0, maximum=100),
    'current': _agent_unknown,
    'tokens': _agent_unknown,
    'observed_traffic': _agent_unknown,
    'gates': _agent_unknown,
    'routing_connected': v3_bool(),
    'missing': arr(string()),
}, ['from', 'to', 'target', 'configured_canary_percent', 'current', 'tokens',
    'observed_traffic', 'gates', 'routing_connected', 'missing'])

S['V3AgentConfiguredVersion'] = obj({
    'version': string(),
    'status': string(enum=['draft', 'stable', 'canary', 'retired']),
    'created': string(format='date-time'),
    'configured_traffic': string(),
    'traffic': _agent_unknown,
    'score': _agent_unknown,
    'success': _agent_unknown,
    'tokens': _agent_unknown,
    'routing_connected': v3_bool(),
}, ['version', 'status', 'created', 'configured_traffic', 'traffic', 'score',
    'success', 'tokens', 'routing_connected'])

_agent_write_result = obj({
    'id': string(),
    'revision': _agent_revision,
    'release_effect': _agent_pending,
    'routing_connected': v3_bool(),
    'observed_traffic': _agent_unknown,
}, ['id', 'revision', 'release_effect', 'routing_connected',
    'observed_traffic'])

v3_route('get', '/agents/lifecycle',
    'Read bounded persisted Agent lifecycle configuration',
    response=obj({
        'stages': _agent_unknown,
        'agents': arr(ref('V3AgentEvidenceSummary')),
        'todos': _agent_unknown,
        'missing': arr(string()),
        'partial': v3_bool(),
        'truncated': v3_bool(),
    }, ['stages', 'agents', 'todos', 'missing', 'partial', 'truncated']),
    desc='At most 200 currently visible persisted Agents. Status and version '
         'describe configured release metadata only. Aggregate lifecycle '
         'stages and todos have no producer and are null. Score and usage are '
         'null, routing_connected is false, and partial is true. This is not '
         'the frozen frontend Lifecycle DTO.')

v3_route('get', '/agents/{id}',
    'Read persisted Agent configuration and version metadata',
    response=obj({
        **S['V3AgentEvidenceSummary']['properties'],
        **fieldnames('owner summary'),
        'account': _agent_unknown,
        'versions': arr(ref('V3AgentConfiguredVersion')),
        'truncated': v3_bool(),
        'release': _agent_release,
        'diff': _agent_unknown,
        'config': _agent_unknown,
        'scope': obj(fieldnames('application_id environment_id owner_team_id'),
                     ['application_id', 'environment_id']),
    }, S['V3AgentEvidenceSummary']['required'] +
       ['owner', 'account', 'summary', 'versions', 'truncated', 'release',
        'diff', 'config', 'scope']),
    desc='At most 100 real version records are returned. Draft is a valid '
         'version status. Configured traffic is distinct from observed '
         'traffic, which is null. Release gates, token comparison, diff, '
         'config and account binding have no complete producer and are null. '
         'The revision is required for conditional writes. Unknown or hidden '
         'Agent returns 404.')

v3_route('post', '/agents/{id}/rollout',
    'Save a version-bound Agent release configuration',
    role='viewer with scoped agents.manage and active human identity',
    body=obj({
        'to': string(maxLength=80),
        'percent': integer(minimum=1, maximum=100),
        'expected_revision': _agent_revision,
        'override': v3_bool(),
        'override_reason': string(maxLength=500),
    }, ['to', 'percent', 'expected_revision']),
    response=obj({
        **_agent_write_result['properties'],
        'to': string(),
        'configured_percent': integer(minimum=1, maximum=100),
        'gate_experiment_id': string(),
        'override': v3_bool(),
    }, _agent_write_result['required'] +
       ['to', 'configured_percent', 'override']),
    desc='Requires one selected environment, current scoped management '
         'authority, matching lifecycle revision and a known candidate '
         'version. A latest persisted passing evaluation for stable to '
         'candidate is required unless an enabled human administrator '
         'provides a nonblank override_reason; the override is audited. '
         'Returns 409 version_conflict on stale expected_revision. Persists '
         'release intent only; there is no traffic router or cutover.')

v3_route('post', '/agents/{id}/rollback',
    'Save a version-bound Agent rollback configuration',
    role='viewer with scoped agents.manage and active human identity',
    body=obj({'expected_revision': _agent_revision}, ['expected_revision']),
    response=obj({
        **_agent_write_result['properties'],
        'to': string(),
        'configured_percent': integer(minimum=0, maximum=100),
        'configured_canary_percent': integer(minimum=0, maximum=100),
    }, _agent_write_result['required'] +
       ['to', 'configured_percent', 'configured_canary_percent']),
    desc='Requires a prior persisted release target and matching lifecycle '
         'revision; stale expected_revision returns 409 version_conflict. '
         'Saves and audits configured rollback intent only. The response '
         'does not assert that live traffic changed.')

v3_route('patch', '/agents/{id}/level',
    'Change version-bound Agent governance level',
    role='viewer with scoped agents.manage and active human identity',
    body=obj({
        'level': string(enum=['L0', 'L1', 'L2', 'L3']),
        'expected_revision': _agent_revision,
    }, ['level', 'expected_revision']),
    response=obj({
        **_agent_write_result['properties'],
        'level': string(enum=['L0', 'L1', 'L2', 'L3']),
        'max_level': string(enum=['L0', 'L1', 'L2', 'L3']),
    }, _agent_write_result['required'] + ['level', 'max_level']),
    desc='Requires one selected environment, current scoped management '
         'authority and matching lifecycle revision. Level above the '
         'persisted environment cap returns 409; stale expected_revision '
         'returns 409 version_conflict. The saved governance level does not '
         'enable an unconnected Agent tool executor.')

for _agent_write_path, _agent_write_method in (
    ('/agents/{id}/rollout', 'post'),
    ('/agents/{id}/rollback', 'post'),
    ('/agents/{id}/level', 'patch'),
):
    P[_agent_write_path][_agent_write_method]['responses']['409'] = {
        'description': 'version_conflict, release gate or environment cap conflict',
        'content': {'application/json': {'schema': ref('Error')}},
    }
    P[_agent_write_path][_agent_write_method]['responses']['403'] = {
        'description': 'Current human identity or scoped agents.manage is required',
        'content': {'application/json': {'schema': ref('Error')}},
    }
