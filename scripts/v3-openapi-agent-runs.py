# Run after v3-openapi-agents.py. These four operations refine only the
# persisted Agent run and human feedback contracts; no live runtime is implied.
def _run_nullable(schema):
    return {'oneOf': [schema, {'type': 'null'}]}


_run_time = string(format='date-time')
_run_ms = integer(minimum=0)
_run_offset_ms = integer()
_run_vote = string(enum=['up', 'down'])
_run_status = string(enum=['running', 'success', 'failed', 'blocked'])

S['V3AgentRunFeedback'] = obj({
    'up': integer(minimum=0), 'down': integer(minimum=0),
    'my_vote': _run_nullable(_run_vote),
}, ['up', 'down', 'my_vote'])
S['V3AgentRunSummary'] = obj({
    **{name: integer(minimum=0) for name in
       ['persisted_runs', 'success', 'failed', 'blocked', 'running', 'negative', 'feedback_votes']},
    'avg_duration_ms': _run_nullable({'type': 'number', 'minimum': 0}),
}, ['persisted_runs', 'success', 'failed', 'blocked', 'running', 'negative',
    'avg_duration_ms', 'feedback_votes'])
S['V3AgentRunTrend'] = obj({
    'ok': {'type': 'array', 'items': integer(minimum=0), 'minItems': 24, 'maxItems': 24},
    'failed': {'type': 'array', 'items': integer(minimum=0), 'minItems': 24, 'maxItems': 24},
    'blocked': {'type': 'array', 'items': integer(minimum=0), 'minItems': 24, 'maxItems': 24},
    'window_start_at': _run_time,
    'interval_minutes': {'type': 'integer', 'const': 60},
}, ['ok', 'failed', 'blocked', 'window_start_at', 'interval_minutes'])
S['V3AgentRunCounts'] = obj({
    **{name: integer(minimum=0) for name in ['all', 'failed', 'blocked', 'negative']},
}, ['all', 'failed', 'blocked', 'negative'])
S['V3AgentRunItem'] = obj({
    **fieldnames('id agent agent_id application_id environment_id version trigger scope'),
    'status': _run_status,
    'steps': _run_nullable(integer(minimum=0)),
    'tokens': _run_nullable(integer(minimum=0)),
    'duration_ms': _run_nullable(_run_ms),
    'started_at': _run_time,
    'feedback': _run_nullable(ref('V3AgentRunFeedback')),
}, ['id', 'agent', 'agent_id', 'application_id', 'environment_id',
    'version', 'trigger', 'scope', 'status', 'steps', 'tokens',
    'duration_ms', 'started_at', 'feedback'])
S['V3AgentRunList'] = obj({
    'kpis': arr({'type': 'object', 'additionalProperties': True}),
    'summary': _run_nullable(ref('V3AgentRunSummary')),
    'trend': _run_nullable(ref('V3AgentRunTrend')),
    'durations': _run_nullable(arr(_run_ms)),
    'items': arr(ref('V3AgentRunItem')),
    'counts': _run_nullable(ref('V3AgentRunCounts')),
    'partial': {'type': 'boolean', 'const': True},
    'truncated': v3_bool(),
    'aggregation_scope': {'type': 'string', 'const': 'authorized_persisted_runs'},
    'missing': arr(string()),
}, ['kpis', 'summary', 'trend', 'durations', 'items', 'counts',
    'partial', 'truncated', 'aggregation_scope', 'missing'])

S['V3AgentRunStep'] = obj({
    **fieldnames('id icon name meta kind connector'),
    'start': _run_offset_ms,
    'dur': _run_nullable(_run_ms),
}, ['id', 'icon', 'name', 'start', 'dur', 'meta', 'kind', 'connector'])
S['V3AgentRunLabelValue'] = obj(fieldnames('label value icon'),
    ['label', 'value', 'icon'])
S['V3AgentRunStat'] = obj(fieldnames('k v'), ['k', 'v'])
S['V3AgentRunDetail'] = obj({
    **fieldnames('id agent agent_id application_id environment_id version trigger'),
    'status': _run_status,
    'error_code': _run_nullable(string()),
    'started_at': _run_time,
    'finished_at': _run_nullable(_run_time),
    'chain': arr(ref('V3AgentRunLabelValue')),
    'stats': arr(ref('V3AgentRunStat')),
    'steps': _run_nullable(arr(ref('V3AgentRunStep'))),
    'total': _run_nullable(_run_ms),
    'context': {'type': 'null'},
    'budget': {'type': 'null'},
    'isolated': {'type': 'null'},
    'memory': {'type': 'null'},
    'feedback': _run_nullable(ref('V3AgentRunFeedback')),
    'partial': {'type': 'boolean', 'const': True},
    'missing': arr(string()),
}, ['id', 'agent', 'agent_id', 'application_id', 'environment_id',
    'version', 'trigger', 'status', 'error_code', 'started_at', 'finished_at',
    'chain', 'stats', 'steps', 'total', 'context', 'budget', 'isolated',
    'memory', 'feedback', 'partial', 'missing'])

S['V3AgentRunSpan'] = obj({
    **fieldnames('id parent_id kind name meta model_id tool_id'),
    'depth': integer(minimum=0),
    'start': _run_offset_ms,
    'dur': _run_nullable(_run_ms),
    'token_input': _run_nullable(integer(minimum=0)),
    'token_output': _run_nullable(integer(minimum=0)),
}, ['id', 'parent_id', 'depth', 'kind', 'name', 'meta', 'start', 'dur',
    'model_id', 'tool_id', 'token_input', 'token_output'])
S['V3AgentRunTrace'] = obj({
    'id': string(), 'agent': string(), 'status': _run_status,
    'stats': arr(ref('V3AgentRunStat')),
    'scores': {'type': 'null'},
    'session': {'type': 'null'},
    'turns': {'type': 'null'},
    'total': _run_nullable(_run_ms),
    'spans': _run_nullable(arr(ref('V3AgentRunSpan'))),
    'partial': {'type': 'boolean', 'const': True},
    'missing': arr(string()),
}, ['id', 'agent', 'status', 'stats', 'scores', 'session', 'turns',
    'total', 'spans', 'partial', 'missing'])

v3_route('get', '/agent-runs', 'Read authorized persisted Agent run records',
    query=[v3_query('filter', string(enum=['all', 'failed', 'blocked', 'negative']))],
    response=ref('V3AgentRunList'),
    desc='Session, selected environment, active Agent/application ownership and current scoped agents.view are rechecked in one read transaction; an application with a current owner team also requires separate team-scope access. Aggregates use all authorized persisted records, not just the 200 list items. No visible records produce null summary/trend/durations/counts, not observed zero. More than 200 matching items set truncated true; more than 2000 aggregate candidates return 413. No live runtime producer is connected.')
v3_route('get', '/agent-runs/{id}', 'Read one authorized persisted Agent run',
    response=ref('V3AgentRunDetail'),
    desc='Current scoped agents.view and persisted Agent/application/environment ownership apply. Only sanitized reported span metadata can appear as steps. Without reported spans, steps is null. More than 500 spans returns 413 instead of a silent partial trace. Conversation context, budget and memory remain null.')
v3_route('get', '/agent-runs/{id}/trace', 'Read authorized reported Agent trace metadata',
    response=ref('V3AgentRunTrace'),
    desc='Current scoped agents.view and persisted Agent/application/environment ownership apply. Only sanitized span metadata is exposed; raw model messages, tool arguments/results, session turns and scores have no producer. Without reported spans, spans is null. More than 500 spans returns 413.')
v3_route('post', '/agent-runs/{id}/feedback', 'Save current-user vote on completed Agent run',
    role='viewer with scoped agents.change',
    body=obj({'vote': _run_vote}, ['vote']),
    response=obj({'vote': _run_vote, 'feedback': ref('V3AgentRunFeedback')},
        ['vote', 'feedback']),
    desc='Requires a current authenticated session, selected environment and active Agent/application/team scope. The completed-run check, scoped permission check, current-user vote upsert and audit are one transaction. Running records return 409; no evaluation score or runtime quality metric is inferred.')

for _run_path, _run_method in (
    ('/agent-runs', 'get'),
    ('/agent-runs/{id}', 'get'),
    ('/agent-runs/{id}/trace', 'get'),
):
    P[_run_path][_run_method]['responses']['413'] = {
        'description': 'Authorized persisted run or span bound exceeded',
        'content': {'application/json': {'schema': ref('Error')}},
    }
