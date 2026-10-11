"""V3 operations backed by current handlers; types.ts defines public DTO fields.

This file omits frontend-only mock routes. Registered partial or explicitly
unavailable handlers are documented with their actual response boundaries.
Reusing a path replaces its pre-V3 operation on the V3 instance.
"""

def v3_object(names, extra=None, required=None):
    properties = fieldnames(names)
    properties.update(extra or {})
    return obj(properties, required)

def v3_bool(): return {'type': 'boolean'}
def v3_num(): return {'type': 'number'}
def v3_map(value): return {'type': 'object', 'additionalProperties': value}
def v3_query(name, schema=None): return {'name': name, 'in': 'query', 'schema': schema or string()}
def v3_route(method, path, summary, *, body=None, response=None, status=200, query=None, role='viewer', desc=''):
    route(method, path, summary, role, body, response, status,
          desc='V3 instance and environment scope apply. ' + desc, query=query)
    if role != 'public' and path != '/conversations/{id}/events':
        P[path][method].setdefault('parameters', []).append({
            'name': 'X-AIOps-Environment', 'in': 'header', 'schema': string(),
            'description': 'Selected environment; all is a read-only non-production aggregate.'})

# PA-05 (2026-10-09): a read with missing optional evidence returns 200, sets
# the affected fields to null and names each one in an "unavailable" map. A
# null is never replaced by 0, false, an empty list, "healthy" or a placeholder.
V3_UNAVAILABLE_REASONS = ['not_connected', 'not_configured', 'no_authorized_target', 'not_generated', 'unavailable', 'stale']
def v3_nullable(schema): return {'oneOf': [schema, {'type': 'null'}]}
# PA-09: a list that is empty because nothing is visible to the caller says so
# instead of returning 403; null when the list is not empty for that reason.
def v3_empty_reason(): return v3_nullable(string(enum=['no_authorized_target']))
S['V3Unavailable'] = {'type': 'object', 'additionalProperties': string(enum=V3_UNAVAILABLE_REASONS),
    'description': 'Field path (dot-separated; [] marks a field of every array item) -> reason for each unknown '
        'field, which is null in the body. Empty when every field is proven. not_configured: no source or registration; '
        'not_connected: no integration; no_authorized_target: nothing visible to this user; not_generated: the platform '
        'has not produced or recorded it; unavailable: a configured source failed or did not prove it; stale: evidence expired.'}

# Keep the execution order fixed: later definitions may refine earlier paths.
# Each fragment runs in the build-openapi.py namespace and owns one V3 domain.
from pathlib import Path

for _v3_domain in (
    'identity',
    'home',
    'config-sync',
    'identity-account',
    'conversations',
    'canvas',
    'search',
    'topology',
    'connectors',
    'coverage',
    'ingest-pull',
    'ingest-push',
    'mcp',
    'mcp-domain',
    'mcp-registration',
    'alerts',
    'silence-product',
    'alert-rule-writes',
    'alert-groups-bulk',
    'rule-assistant',
    'inhibitions',
    'reviews',
    'runbooks',
    'memory',
    'memory-production',
    'agents',
    'agent-runs',
    'eval-detail',
    'agent-lifecycle',
    'agent-prompt-trial',
    'agent-catalog-write',
    'evaluation-execution',
    'agent-execution',
    'executions',
    'observability',
    'roles',
    'member-access',
    'audit',
    'webhooks',
    'api-tokens',
    'agent-interactions',
    'page-query',
    'page-operations',
    'rollback',
    'alert-rule-views',
):
    _v3_fragment = Path(f'scripts/v3-openapi-{_v3_domain}.py')
    exec(compile(_v3_fragment.read_text(encoding='utf-8'), str(_v3_fragment), 'exec'))
