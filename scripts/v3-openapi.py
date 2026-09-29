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

# Keep the execution order fixed: later definitions may refine earlier paths.
# Each fragment runs in the build-openapi.py namespace and owns one V3 domain.
from pathlib import Path

for _v3_domain in (
    'identity',
    'config-sync',
    'identity-account',
    'conversations',
    'canvas',
    'search',
    'topology',
    'connectors',
    'ingest-push',
    'mcp',
    'alerts',
    'reviews',
    'runbooks',
    'memory',
    'agents',
    'observability',
    'roles',
    'audit',
    'webhooks',
):
    _v3_fragment = Path(f'scripts/v3-openapi-{_v3_domain}.py')
    exec(compile(_v3_fragment.read_text(encoding='utf-8'), str(_v3_fragment), 'exec'))
