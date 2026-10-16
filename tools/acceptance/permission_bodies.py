"""Valid request bodies for the object-scoped write probes of permissions.py.

An object-scoped handler validates the body before it resolves the target, so the
empty-body probe stops at 400/422 and proves nothing about authorization. Each
entry below builds a body that passes validation (shapes from
internal/apicontract/openapi.json and docs/api/v3-contract.md) and names the
target objects, read beforehand as admin.

Two probes per route, never with an account that may write the target:

  * visible: a built-in role WITHOUT the route's module grant, on an object it
    can see (every built-in role reads every module). Expected 403 (404 accepted).
  * foreign: the scoped account (role custom-acceptance-scoped: view, change and
    manage on business modules, but only in its environment), sending its own environment
    header, on an object of another environment named in the path. Expected 404
    (403 accepted). It holds the write grant, so only the object check stands
    between the request and a write. Only nonprod has several environments.
    Some objects belong to an application rather than an environment (agents,
    prompts, runbooks); if that application is also mapped to the scoped
    environment the account may legitimately act on it. So a foreign probe runs
    only when admin cannot read the object under the scoped environment, and
    only for guards that write nothing even when allowed (not creates/own_pref).

No probe runs as a role that holds the grant on the target. A working check
writes nothing; each entry's guard says why a MISSING check still writes nothing,
or only something harmless and easy to find:

  stale      a revision, version, hash or If-Match that can never match, so the
             write ends in 409/412 (also when authorization is checked after it:
             that answer is inconclusive, not a pass)
  crypto     needs a passkey assertion the probe cannot produce
  terminal   the target is already in a final state the action cannot leave
  missing    the body points at an object that does not exist, so nothing is
             created or executed (404/422 is inconclusive)
  no_op      the body restates the current state
  read_only  the route persists nothing (preview, dry run, test, search, options)
  creates    a missed check adds a record marked acceptance-probe (a new object,
             or a comment, note, version or participant on an existing one);
             nothing existing is changed or removed. The probe reports it for cleanup
  own_pref   changes only the probing account's own preference

Routes in SKIP have no such guard (a missed check would change or delete a real
object, export configuration, or run a model) or no sample can be found
statically; each has the reason.
"""
import base64
import json
import secrets as _secrets
import ssl
import time
import urllib.error
import urllib.request

from api import DEFAULT_ENV, LOCAL, ORIGINS, call, session_cookie

PROBE = 'permissions'
PREFIX = 'acceptance-probe'
STALE = 999_999_999
ZERO = '0' * 64
SCOPED_ROLE = 'custom-acceptance-scoped'
RESOURCES = ['conversation', 'events', 'alert_rules', 'silences', 'proposals', 'runbooks', 'knowledge', 'services',
             'connectors', 'agents', 'identity', 'audit', 'resources']
TERMINAL = {'approved', 'rejected', 'executed', 'expired', 'cancelled', 'canceled', 'failed', 'succeeded', 'completed',
            'finished', 'published', 'resolved', 'closed', 'done'}
# What every probe result is judged by.
DENIED = {403, 404}
GUARD_HIT = {409, 412, 428}


def tag():
    return f'{PREFIX}-{_secrets.token_hex(4)}'


def request_id():
    return f'{PREFIX}-{_secrets.token_hex(8)}'


LIST_KEYS = ('items', 'rules', 'agents', 'members', 'roles', 'silences', 'runs', 'tokens', 'servers', 'policies', 'cases')


def list_items(body, key=None):
    """The objects of a list response: the body itself when it is a list, else the
    first non-empty list under LIST_KEYS (or key). Never a nested or unrelated
    list such as kpis or trend, whose ids are not object ids."""
    rows = []
    if isinstance(body, list):
        rows = body
    elif isinstance(body, dict):
        rows = next((body[k] for k in ((key,) if key else LIST_KEYS) if isinstance(body.get(k), list) and body[k]), [])
    return [r for r in rows if isinstance(r, dict) and isinstance(r.get('id'), (str, int)) and str(r['id'])]


def walk(body, depth=0):
    """Every dict inside a JSON value, breadth first, a few levels deep."""
    level = [body]
    for _ in range(5):
        nxt = []
        for v in level:
            if isinstance(v, dict):
                yield v
                nxt.extend(x for x in v.values() if isinstance(x, (dict, list)))
            elif isinstance(v, list):
                nxt.extend(x for x in v if isinstance(x, (dict, list)))
        level = nxt


class Sampler:
    """Admin reads (tagged as probe traffic) that pick target objects. env=None
    reads the instance's default environment; a foreign sampler reads another."""

    def __init__(self, instance, env=None):
        self.instance, self.env, self.cache = instance, env, {}

    def get(self, path):
        if path not in self.cache:
            status, body = call(self.instance, 'GET', path, env=self.env, probe=PROBE)
            self.cache[path] = body if status == 200 else None
        return self.cache[path]

    def item(self, path, pred=lambda x: True, key=None):
        """First object of the list response that satisfies pred."""
        return next((d for d in list_items(self.get(path), key) if pred(d)), None)

    def id(self, path, pred=lambda x: True, key=None):
        found = self.item(path, pred, key)
        return str(found['id']) if found else None

    def field(self, path, *names):
        """A field of the list's objects (document_id, execution_id ...)."""
        for d in list_items(self.get(path)):
            for n in names:
                if isinstance(d.get(n), (str, int)) and str(d[n]):
                    return str(d[n])
        return None

    def app(self, sid=None):
        """An application id: services ids are base64url("<application>\\x00<environment>")."""
        sid = sid or self.id('/services')
        if not sid:
            return None
        try:
            return base64.urlsafe_b64decode(sid + '=' * (-len(sid) % 4)).decode().split('\x00')[0] or None
        except (ValueError, UnicodeDecodeError):
            return (self.item('/services') or {}).get('application_id')

    def alert(self):
        """An alert id: alert groups list their alerts under instances."""
        for group in list_items(self.get('/alert-groups')):
            for alert in group.get('instances') or []:
                if isinstance(alert, dict) and alert.get('id'):
                    return str(alert['id'])
        return None

    def incident_case(self):
        """A human incident case (INC-...), the only kind whose status can be changed."""
        return self.id('/incidents?status=all', lambda d: str(d['id']).startswith('INC-'))

    def eval_case(self):
        """A case of the first evaluation that lists cases (GET /evals/{id} has them)."""
        for e in list_items(self.get('/evals'))[:10]:
            case = next(iter(list_items(self.get(f"/evals/{e['id']}"), 'cases')), None)
            if case:
                return str(case['id'])
        return None

    def environment(self):
        return self.env or (self.get('/instance') or {}).get('default_env') or DEFAULT_ENV[self.instance]

    def investigation(self):
        return self.id('/canvases?kind=investigation')

    def node(self, inv):
        graph = self.get(f'/investigations/{inv}/canvas') if inv else None
        nodes = (graph or {}).get('nodes') or []
        return nodes[0] if nodes and isinstance(nodes[0], dict) else None

    def terminal(self, path):
        """An object already in a final state (status, state, lifecycle or execution_status)."""
        return self.id(path, lambda d: any(str(d.get(k, '')).lower() in TERMINAL
                                           for k in ('status', 'state', 'lifecycle', 'execution_status')))

    def proposal(self, closed):
        """A proposal id found in the inbox; closed=True only one no longer pending
        (checked on its detail), so a missed check on reject meets 409 proposal_closed."""
        ids = []
        for d in walk(self.get('/inbox')):
            for k in ('proposal_id', 'href', 'url'):
                v = str(d.get(k) or '')
                if k == 'proposal_id' and v:
                    ids.append(v)
                elif '/reviews/' in v or '/proposals/' in v:
                    ids.append(v.rstrip('/').rsplit('/', 1)[-1].split('?')[0])
        for pid in list(dict.fromkeys(ids))[:20]:
            detail = self.get(f'/proposals/{pid}')
            if detail is None:
                continue
            if not closed or str(detail.get('status', '')).lower() not in ('pending', ''):
                return pid
        return None


class Probe:
    """note: what the answer can and cannot prove for this route, copied to each row."""

    def __init__(self, guard, params=None, body=None, headers=None, effect='', note=''):
        self.guard, self.params, self.body, self.headers, self.effect, self.note = guard, params, body, headers, effect, note

    def build(self, s):
        """(path params, body, headers) for sampler s; a None param means no sample."""
        params = self.params(s) if self.params else {}
        body = self.body(s) if self.body else {}
        headers = self.headers(s) if self.headers else {}
        return params, body, headers


def _inv(s):
    return {'id': s.investigation()}


def _inv_node(s):
    inv = s.investigation()
    node = s.node(inv)
    return {'id': inv, 'node': node and str(node.get('id'))}


def _mcp_tool(s):
    sid = s.id('/mcp-servers')
    detail = s.get(f'/mcp-servers/{sid}') if sid else None
    tool = next((d for d in walk(detail) if isinstance(d.get('name'), str) and 'tool_hash' in d), None)
    return {'id': sid, 'name': tool and tool['name']}


def _rule(s):
    return {'application_id': s.app(), 'environment_id': s.environment(), 'group': PREFIX, 'name': tag(),
            'expr': 'vector(0) > 1', 'for': '5m', 'labels': {}, 'annotations': {}}


MCP_REVIEW = lambda s: {'snapshot_id': tag(), 'registration_version': STALE, 'registration_hash': ZERO, 'tools_hash': ZERO,
                        'decisions': [{'name': PREFIX, 'tool_hash': ZERO, 'decision': 'deny'}]}

PROBES = {
    # agents
    'PATCH /api/v1/agents/{id}/level': Probe('stale', lambda s: {'id': s.id('/agents/lifecycle')},
                                             lambda s: {'level': 'L0', 'expected_revision': STALE}),
    'POST /api/v1/agents/{id}/rollback': Probe('stale', lambda s: {'id': s.id('/agents/lifecycle')},
                                               lambda s: {'expected_revision': STALE}),
    'POST /api/v1/agents/{id}/rollout': Probe('stale', lambda s: {'id': s.id('/agents/lifecycle')},
                                              lambda s: {'to': 'v1', 'percent': 1, 'expected_revision': STALE}),
    'POST /api/v1/agents': Probe('creates', None, lambda s: {'id': tag(), 'application_id': s.app(), 'name': PREFIX},
                                 effect='a new agent'),
    'POST /api/v1/agents/{id}/versions': Probe('creates', lambda s: {'id': s.id('/agents/lifecycle')},
                                               lambda s: {'version': tag(), 'artifact_ref': f'acceptance/{PREFIX}@v0'},
                                               effect='an unpublished version on an existing agent'),
    'POST /api/v1/agent-tools': Probe('creates', None, lambda s: {'name': tag(), 'kind': 'read', 'connector_id': s.id('/connectors')},
                                      effect='a tool catalog entry'),
    'POST /api/v1/agent-runs/{id}/annotations': Probe('creates', lambda s: {'id': s.id('/agent-runs')},
                                                      lambda s: {'score': 3, 'note': PREFIX}, effect='an annotation on a run'),
    'POST /api/v1/agent-runs/{id}/feedback': Probe('own_pref', lambda s: {'id': s.id('/agent-runs')}, lambda s: {'vote': 'up'},
                                                   effect="the probing account's own vote on a run"),
    'PATCH /api/v1/guardrails/{id}': Probe('no_op', lambda s: {'id': s.id('/guardrails')},
                                           lambda s: {'enabled': bool((s.item('/guardrails') or {}).get('enabled', True))}),
    'PUT /api/v1/agents/environment-limit': Probe(
        'stale', None, lambda s: {'max_level': 'L0', 'expected_max_level': next(
            x for x in ('L3', 'L2', 'L1', 'L0') if x != (s.get('/agents/environment-limit') or {}).get('max_level'))}),
    'POST /api/v1/prompts': Probe('creates', None, lambda s: {'agent_id': s.id('/agents/lifecycle'), 'name': tag(), 'text': PREFIX},
                                  effect='a new prompt'),
    'POST /api/v1/prompts/{id}/versions': Probe('creates', lambda s: {'id': s.id('/prompts')}, lambda s: {'text': PREFIX},
                                                effect='a new version of an existing prompt (not published)'),
    'POST /api/v1/prompts/{id}/trial': Probe('missing', lambda s: {'id': s.id('/prompts')},
                                             lambda s: {'case': f'{PREFIX}-missing', 'a': 'v1', 'b': 'v2'}),
    'POST /api/v1/eval-datasets': Probe('creates', None, lambda s: {'name': tag(), 'source': 'manual'}, effect='a dataset'),
    'POST /api/v1/eval-datasets/{id}/items': Probe('missing', lambda s: {'id': s.id('/eval-datasets')},
                                                   lambda s: {'run_id': f'{PREFIX}-missing'}),
    'POST /api/v1/evals': Probe('missing', None, lambda s: {'agent_id': s.id('/agents/lifecycle'), 'title': tag(),
                                                            'baseline_version': 'v1', 'candidate_version': f'{PREFIX}-missing'}),
    'POST /api/v1/evals/{id}/execute': Probe('missing', lambda s: {'id': s.id('/evals')},
                                             lambda s: {'case_id': f'{PREFIX}-missing', 'request_key': request_id()}),
    'POST /api/v1/evals/{id}/cancel': Probe('terminal', lambda s: {'id': s.terminal('/evals')}),
    'POST /api/v1/eval-cases': Probe('creates', None, lambda s: {
        'agent_id': s.id('/agents/lifecycle'), 'title': tag(), 'input': PREFIX, 'queue': PREFIX,
        'alert': {'sev': 'P4', 'title': PREFIX, 'labels': []}, 'evidence': [{'brand': 'manual', 'title': PREFIX, 'sub': PREFIX}],
        'expected': {'cause': PREFIX, 'advice': PREFIX},
        'checks': [PREFIX]}, effect='an evaluation case'),
    'POST /api/v1/eval-cases/{id}/labels': Probe('stale', lambda s: {'id': s.eval_case()},
                                                 lambda s: {'next': 'unsure', 'expected_revision': STALE}),
    # alert rules
    'POST /api/v1/alert-rules': Probe('creates', None, lambda s: {**_rule(s), 'mode': 'draft'}, effect='a draft rule (not enabled)'),
    'PUT /api/v1/alert-rules/{id}': Probe('stale', lambda s: {'id': s.id('/alert-rules')},
                                          lambda s: {'expected_version': STALE, 'rule': {**_rule(s), 'mode': 'draft'}}),
    'POST /api/v1/alert-rules/import': Probe('creates', None, lambda s: {
        'application_id': s.app(), 'mode': 'draft',
        'yaml': f'groups:\n- name: {PREFIX}\n  rules:\n  - alert: {tag()}\n    expr: vector(0) > 1\n    for: 5m\n'},
        effect='draft rules (not enabled)'),
    'POST /api/v1/alert-rules/{id}/disable': Probe('missing', lambda s: {'id': s.id('/alert-rules')},
                                                   lambda s: {'investigation_id': '0' * 32}),  # well-formed, never issued
    'POST /api/v1/alert-rules/generate': Probe('read_only', None, lambda s: {'text': 'error rate above 5% for 5 minutes',
                                                                              'application_id': s.app()},
                                               effect='one model call (draft text, not saved)'),
    'POST /api/v1/alert-rules/preview': Probe('read_only', None, lambda s: {
        'application_id': s.app(), 'query': 'vector(0)', 'op': '>', 'threshold': 1, 'for_minutes': 5, 'range': '1h'}),
    'POST /api/v1/alert-rules/{id}/dry-run': Probe('read_only', lambda s: {'id': s.id('/alert-rules')},
                                                   lambda s: {'expr': 'vector(0) > 1', 'for': '5m'}),
    'POST /api/v1/executions/{id}/recheck': Probe('read_only', lambda s: {'id': s.field('/alert-rules', 'execution_id')},
                                                  effect='re-reads the rule state for one execution'),
    # events
    'POST /api/v1/alerts/{id}/claim': Probe('stale', lambda s: {'id': s.alert()},
                                            lambda s: {'expected_revision': str(STALE)}),
    # A real alert (its events.change is checked first) with a service that is not
    # the alert's application: past the check the handler answers 409
    # incident_scope_changed before writing.
    'POST /api/v1/incidents': Probe('stale', None, lambda s: {
        'title': tag(), 'severity': 'P4', 'env': s.environment(), 'services': [f'{PREFIX}-mismatch'], 'alert_ids': [s.alert()],
        'war_room': False, 'notify': False, 'status_page': False, 'open_canvas': False, 'participants': []}),
    'POST /api/v1/incidents/{id}/status': Probe('stale', lambda s: {'id': s.incident_case()},
                                                lambda s: {'status': 'mitigated', 'expected_revision': STALE, 'request_id': request_id()}),
    'POST /api/v1/incidents/{id}/participants': Probe('creates', lambda s: {'id': s.terminal('/incidents?status=all') or s.id('/incidents?status=all')},
                                                      lambda s: {'names': [PREFIX]}, effect='a participant name on an incident'),
    'POST /api/v1/incidents/{id}/timeline': Probe('creates', lambda s: {'id': s.terminal('/incidents?status=all') or s.id('/incidents?status=all')},
                                                  lambda s: {'text': PREFIX}, effect='a manual timeline note on an incident'),
    'POST /api/v1/incidents/{id}/investigation': Probe('creates', lambda s: {'id': s.id('/incidents?status=all')}, lambda s: {},
                                                       effect='opens (or returns) the incident investigation'),
    'POST /api/v1/maintenance-windows': Probe('creates', None, lambda s: {
        'schedule': 'once 2099-01-01T00:00:00Z/1h', 'reason': PREFIX,
        'matchers': [f'application_id={s.app()}', f'environment={s.environment()}', f'alertname={PREFIX}']},
        effect='a maintenance plan record for alertname=acceptance-probe in one application'),
    'POST /api/v1/silences': Probe('creates', None, lambda s: {
        'investigation_id': s.investigation(), 'duration_seconds': 1800, 'comment': PREFIX,
        'matchers': [{'name': 'application', 'op': '=', 'value': s.app()}, {'name': 'alertname', 'op': '=', 'value': PREFIX}]},
        effect='a silence proposal (needs approval) for alertname=acceptance-probe in one application'),
    'DELETE /api/v1/silences/{id}': Probe('terminal', lambda s: {'id': s.terminal('/silences')},
                                          lambda s: {'investigation_id': s.investigation(), 'reason': PREFIX}),
    'POST /api/v1/silences/preview': Probe('read_only', None, lambda s: {'matchers': [{'k': 'alertname', 'op': '=', 'v': PREFIX}]}),
    # canvas and investigations
    'POST /api/v1/canvases': Probe('creates', None, lambda s: {'title': tag(), 'kind': 'custom', 'goal': PREFIX,
                                                               'services': [], 'ai_draft': False}, effect='an empty canvas'),
    'PATCH /api/v1/investigations/{id}/nodes/{node}': Probe('stale', _inv_node,
                                                            lambda s: {'x': 0, 'y': 0, 'expected_layout_version': STALE}),
    'POST /api/v1/investigations/{id}/branches': Probe('stale', _inv_node, lambda s: {
        'parent_id': (s.node(s.investigation()) or {}).get('id'), 'title': PREFIX, 'expected_parent_revision': STALE}),
    'POST /api/v1/investigations/{id}/writeback': Probe('stale', _inv_node, lambda s: {
        'node': (s.node(s.investigation()) or {}).get('id'), 'target': (s.node(s.investigation()) or {}).get('id'),
        'conclusions': [], 'mode': 'summary', 'expected_source_revision': STALE, 'expected_target_revision': STALE}),
    'POST /api/v1/investigation-templates/{id}/start': Probe('stale', lambda s: {'id': s.id('/investigation-templates')},
                                                             lambda s: {'inputs': {'application_id': s.app(), 'template_version': f'{PREFIX}-stale',
                                                                                   'request_id': request_id()}}),
    'PUT /api/v1/investigation-templates/{id}': Probe('stale', lambda s: {'id': s.id('/investigation-templates')},
                                                      lambda s: {'expected_revision': STALE, 'name': PREFIX, 'budget': {'tools': 1, 'minutes': 1}}),
    # knowledge, memory, runbooks, postmortems
    'PATCH /api/v1/memory/facts/{id}': Probe('stale', lambda s: {'id': s.id('/memory/facts')},
                                             lambda s: {'expected_revision': STALE, 'fact': PREFIX}),
    'POST /api/v1/memory/facts': Probe('stale', None, lambda s: {'document_id': s.field('/memory/facts', 'document_id', 'source_id'),
                                                                 'expected_version': STALE, 'expected_content_hash': ZERO,
                                                                 'exact_excerpt': PREFIX}),
    'POST /api/v1/memory/conflicts': Probe('stale', None, lambda s: {
        'fact_a_id': s.id('/memory/facts'), 'fact_b_id': s.id('/memory/facts', lambda d: str(d['id']) != s.id('/memory/facts')),
        'expected_revision_a': STALE, 'expected_revision_b': STALE, 'reason': PREFIX}),
    'POST /api/v1/memory/sources/{id}/withdraw': Probe('stale', lambda s: {'id': s.field('/memory/facts', 'document_id', 'source_id')},
                                                       lambda s: {'expected_version': STALE, 'expected_content_hash': ZERO}),
    'POST /api/v1/memory/sources': Probe('creates', None, lambda s: {'request_key': request_id(), 'application_id': s.app(),
                                                                     'title': tag(), 'body': PREFIX},
                                         effect='a published memory source titled acceptance-probe-... (can feed answers until withdrawn)'),
    'PUT /api/v1/memory/policy': Probe('stale', None, lambda s: {'expected_revision': STALE, 'ttl_days': 90, 'proposers': 'human'}),
    'PATCH /api/v1/postmortems/{id}': Probe('stale', lambda s: {'id': s.id('/postmortems/records')}, lambda s: {'status': 'draft'},
                                            lambda s: {'If-Match': f'"{STALE}"'}),
    'POST /api/v1/postmortems': Probe('stale', None, lambda s: {
        'incident_id': s.terminal('/incidents?status=all') or s.id('/incidents?status=all'), 'source_revision': f'case:{STALE}',
        'sections': [{'key': 'summary', 'title': PREFIX, 'text': PREFIX}], 'actions': [], 'status': 'draft'}),
    'POST /api/v1/postmortems/{id}/publish': Probe('terminal', lambda s: {'id': s.terminal('/postmortems/records')}),
    'POST /api/v1/runbooks/{id}/submit': Probe('terminal', lambda s: {'id': s.terminal('/runbooks')}),
    'POST /api/v1/runbooks/{id}/reviews': Probe('terminal', lambda s: {'id': s.terminal('/runbooks')},
                                                lambda s: {'decision': 'request_changes', 'note': PREFIX}),
    'POST /api/v1/runbooks/{id}/dry-run': Probe('read_only', lambda s: {'id': s.id('/runbooks')}),
    'POST /api/v1/runbooks/{id}/reindex': Probe('no_op', lambda s: {'id': s.id('/runbooks')}, effect='re-indexes the same text'),
    'POST /api/v1/executions/{id}/report/publish': Probe('terminal', lambda s: {'id': s.field('/runbooks', 'execution_id')}),
    'POST /api/v1/knowledge-sources': Probe('creates', None, lambda s: {
        'name': tag(), 'type': 'web', 'url': 'https://acceptance-probe.invalid/', 'application_id': s.app(), 'spaces': [],
        'schedule': 'manual', 'chunk': 'heading', 'inherit_acl': False}, effect='a source registration that is never synced'),
    'POST /api/v1/knowledge-sources/registrations': Probe('creates', None, lambda s: {
        'name': tag(), 'type': 'web', 'url': 'https://acceptance-probe.invalid/', 'application_id': s.app(), 'spaces': [],
        'schedule': 'manual', 'chunk': 'heading', 'inherit_acl': False}, effect='a source registration that is never synced'),
    'POST /api/v1/knowledge-sources/test': Probe('read_only', None, lambda s: {'type': 'web', 'url': 'https://acceptance-probe.invalid/',
                                                                              'application_id': s.app(), 'spaces': []}),
    'POST /api/v1/knowledge-sources/registrations/{id}/sync': Probe('no_op', lambda s: {'id': s.id('/knowledge-sources/registrations')},
                                                                    effect='syncs the registered source again'),
    'POST /api/v1/knowledge/search': Probe('read_only', None, lambda s: {'q': PREFIX, 'top_k': 1}),
    # connectors, ingest, MCP
    'POST /api/v1/mcp-servers': Probe('creates', None, lambda s: {'application_id': s.app(), 'title': tag(), 'maintainer': PREFIX,
                                                                  'endpoint': 'https://acceptance-probe.invalid/mcp'},
                                      effect='an MCP registration that is never bound or discovered'),
    'POST /api/v1/mcp-servers/{id}/review': Probe('stale', lambda s: {'id': s.id('/mcp-servers')}, MCP_REVIEW),
    'POST /api/v1/mcp-servers/{id}/review-version': Probe('stale', lambda s: {'id': s.id('/mcp-servers')}, MCP_REVIEW),
    'POST /api/v1/mcp-servers/{id}/review-dismiss': Probe('stale', lambda s: {'id': s.id('/mcp-servers')},
                                                          lambda s: {'candidate_version': f'{PREFIX}-stale'}),
    'PATCH /api/v1/mcp-servers/{id}/tools/{name}': Probe('stale', _mcp_tool, lambda s: {
        'snapshot_id': tag(), 'registration_version': STALE, 'registration_hash': ZERO, 'tools_hash': ZERO, 'tool_hash': ZERO,
        'expected_revision': STALE, 'enabled': False}),
    'PATCH /api/v1/mcp-servers/{id}/reviewed-tools/{name}': Probe('stale', _mcp_tool, lambda s: {
        'snapshot_id': tag(), 'tools_hash': ZERO, 'tool_hash': ZERO, 'expected_revision': STALE, 'enabled': False}),
    'POST /api/v1/mcp-servers/{id}/discover': Probe('creates', lambda s: {'id': s.id('/mcp-servers')},
                                                    effect='one tools/list call and an immutable observation snapshot'),
    'POST /api/v1/ingest/pull/{id}/test': Probe('read_only', lambda s: {'id': s.id('/ingest/pull')}),
    'POST /api/v1/ingest/push/test': Probe('read_only', None, lambda s: {'type': 'alert'}),
    # services, proposals
    'PUT /api/v1/services/{id}/slo': Probe('stale', lambda s: {'id': s.id('/services')},
                                           lambda s: {'expected_revision': STALE, 'target': '99.9', 'window': '30d'}),
    'POST /api/v1/services/{id}/external-targets': Probe('creates', lambda s: {'id': s.id('/services')}, lambda s: {
        'connector_id': s.id('/connectors'), 'target_kind': 'project', 'external_id': str(STALE)},
        effect='a binding to a GitLab project id that does not exist'),
    'POST /api/v1/services/{id}/resilience/proposals': Probe('missing', lambda s: {'id': s.id('/services')}, lambda s: {
        'kind': 'rollout.pause', 'params': {'investigation_id': s.investigation(), 'reason': PREFIX,
                                            'desired': {'rollout_id': f'{PREFIX}-missing'}}}),
    'POST /api/v1/rollback-proposals': Probe('stale', None, lambda s: {
        'investigation_id': s.investigation(), 'service_id': s.id('/services'), 'target_revision': str(STALE),
        'expected_template_digest': ZERO, 'reason': PREFIX},
        note='proposals.change is checked in the write transaction, after the Deployment digest; a role that can '
             'read the Deployment (resources.view) gets 409 rollback_target_changed, which proves nothing. Only a '
             'real digest and target would reach the check, and a missed check would then file a real proposal'),
    'PUT /api/v1/me/watched-services/{id}': Probe('own_pref', lambda s: {'id': s.id('/services')},
                                                  effect="the probing account's own watch list"),
    'DELETE /api/v1/me/watched-services/{id}': Probe('own_pref', lambda s: {'id': s.id('/services')},
                                                     effect="the probing account's own watch list"),
    'POST /api/v1/proposals/{id}/approve': Probe('crypto', lambda s: {'id': s.proposal(closed=False)},
                                                 lambda s: {'challenge_id': f'{PREFIX}-missing', 'credential': {}}),
    'POST /api/v1/proposals/{id}/approve/options': Probe('read_only', lambda s: {'id': s.proposal(closed=False)},
                                                         lambda s: {'confirm': True}, effect='a short-lived passkey challenge'),
    'POST /api/v1/proposals/{id}/reject': Probe('terminal', lambda s: {'id': s.proposal(closed=True)},
                                                lambda s: {'comment': PREFIX}),
    'POST /api/v1/proposals/{id}/comments': Probe('creates', lambda s: {'id': s.proposal(closed=True) or s.proposal(closed=False)},
                                                  lambda s: {'text': PREFIX}, effect='a comment on a proposal'),
    'POST /api/v1/metrics/ask': Probe('read_only', None, lambda s: {
        'service': s.id('/services'), 'view': 'request_rate', 'range': '15m', 'question': PREFIX,
        'request_id': _secrets.token_hex(12)}, effect='one model call'),
}

SKIP = {
    'DELETE /api/v1/trace-views/{id}': 'no guard: a missed check deletes a real shared view; needs a throwaway view created by admin',
    'POST /api/v1/trace-views': 'team-scoped, not application-scoped: the scoped account may legitimately create it; '
                                'visible probes cover the module check (empty body already reaches 403/422)',
    'DELETE /api/v1/services/{id}/external-targets/{bindingId}': 'no guard: a missed check deletes a real binding; '
                                                                   'needs a binding created by admin for the probe',
    'PUT /api/v1/mcp-servers/{id}/environment-binding': 'no guard: a missed check binds a real registration to the environment',
    'DELETE /api/v1/mcp-servers/{id}/environment-binding': 'no guard: a missed check unbinds a real registration',
    'POST /api/v1/mcp-servers/{id}/disable': 'no guard: a missed check disables a real MCP registration',
    'POST /api/v1/memory/conflicts/resolve': 'no guard: a missed check drops a real fact',
    'POST /api/v1/memory/facts/{id}/{action}': 'no body, no guard: a missed check changes a real fact state',
    'POST /api/v1/alert-groups/bulk': 'every action (claim, silence, investigate) changes real alerts or creates silences',
    'POST /api/v1/config-sync/{id}/import': 'a missed check imports configuration into the production instance',
    'POST /api/v1/promotions': 'environment.view (every role has it); a missed object check exports configuration',
    'PUT /api/v1/agents/{id}/versions/{version}/execution': 'no revision in the body: a missed check replaces a real execution config',
    'POST /api/v1/guardrails': 'a missed check enables an execution guardrail for the whole environment',
    'POST /api/v1/agents/{id}/runs': 'a missed check runs the agent (model calls); golden.py already checks viewer gets 403',
    'POST /api/v1/investigations/{id}/nodes/{node}/messages': 'a missed check posts a message and runs the model on a real branch',
    'POST /api/v1/changes/{run_id}/review': 'needs the digests of a real delivery run; no list route gives run ids',
    'POST /api/v1/service-mappings/discover': 'recomputes mapping candidates for the environment; no object to target',
    'POST /api/v1/service-mappings/accept': 'needs a candidate from discover; a missed check would change service bindings',
    'PUT /api/v1/service-mappings': 'needs every binding of a service environment; a missed check rewrites bindings '
                                    '(binding_revision guards it, but the body cannot be built from list routes)',
}


def ensure_scoped_account(instance, env):
    """Create role custom-acceptance-scoped (view/change/manage on business
    modules, view only on members, roles and audit, no delete or approve, only in env) and the account scoped@<instance>, through
    the product's own flow (as accounts.py). Writes the account to secrets.json.
    An instance write: run only when the session running the instance says so."""
    from accounts import domain, invitation_code, member_id
    # Members, roles and audit stay read-only: the account exists to write business objects.
    matrix = [{'resource': r, 'grants': [1, 0, 0, 0, 0] if r in ('identity', 'audit') else [1, 1, 0, 0, 1]} for r in RESOURCES]
    status, body = call(instance, 'POST', '/roles', {'id': SCOPED_ROLE, 'name': '验收：单环境探测',
                                                     'description': f'权限探测专用，只在 {env} 环境有权限（WAN-434）',
                                                     'scopes': [{'kind': 'environment', 'id': env}], 'matrix': matrix},
                        env=env, probe=PROBE)
    if status not in (200, 201):
        raise SystemExit(f'create {SCOPED_ROLE}: {status} {body}')
    path = LOCAL / instance / 'secrets.json'
    store = json.loads(path.read_text())
    accounts = store.setdefault('accounts', {})
    address = f'scoped@{domain(instance)}'
    if 'scoped' not in accounts:
        since = time.time() - 1
        status, body = call(instance, 'POST', '/members/invitations', {'email': address, 'role': 'viewer'}, env=env, probe=PROBE)
        if status == 409:
            since = 0
        elif status != 200:
            raise SystemExit(f'invite scoped: {status} {body}')
        password = _secrets.token_urlsafe(18)
        status, body = call(instance, 'POST', '/auth/invitations/accept',
                            {'code': invitation_code(address, since), 'name': 'Scoped', 'password': password},
                            env='', cookie='', probe=PROBE)
        if status not in (200, 201):
            raise SystemExit(f'accept scoped: {status} {body}')
        accounts['scoped'] = {'email': address, 'password': password, 'role': SCOPED_ROLE, 'environment': env}
        path.write_text(json.dumps(store, indent=2) + '\n')
        path.chmod(0o600)
    mid = member_id(instance, address)
    status, access = call(instance, 'GET', f'/members/{mid}/access', env=env, probe=PROBE)
    if status != 200:
        raise SystemExit(f'access scoped: {status} {access}')
    current = {r['id']: str(r['matrix_revision']) for r in access['roles'] if r.get('origin') == 'direct'}
    if SCOPED_ROLE not in current:
        headers = {}
        call(instance, 'GET', f'/roles/{SCOPED_ROLE}', env=env, headers_out=headers, probe=PROBE)
        current[SCOPED_ROLE] = headers.get('X-Role-Matrix-Revision', '1')
        status, body = call(instance, 'PUT', f'/members/{mid}/access', {'expected_revision': access['revision'],
                            'role_ids': sorted(current), 'role_revisions': current, 'enabled': True}, env=env, probe=PROBE)
        if status != 200:
            raise SystemExit(f'assign {SCOPED_ROLE}: {status} {body}')
    return accounts['scoped']


def has_none(v):
    """A body built from a missing sample: some value is None."""
    if v is None:
        return True
    if isinstance(v, dict):
        return any(has_none(x) for x in v.values())
    if isinstance(v, list):
        return any(has_none(x) for x in v)
    return False


def judge(status, guard):
    """(ok, verdict) for one valid-body probe answer."""
    if status in DENIED:
        return True, 'denied'
    if 200 <= status < 300:
        return False, 'ACCEPTED'
    if status in GUARD_HIT and guard in ('stale', 'terminal'):
        return False, 'guard_hit'  # authorization missing, or checked after the version/state check
    if status in (400, 422):
        return False, 'body_rejected'  # the table entry needs fixing
    if status in (404, 409, 412, 428, 501, 503):
        return False, 'inconclusive'
    return False, 'unexpected'


def send(instance, method, path, body, env, cookie, headers):
    """call() for the few probes that need request headers (If-Match); it sets
    X-Acceptance-Probe itself, as permissions.login() does."""
    ctx = ssl.create_default_context(cafile=str(LOCAL / 'tls/cert.pem'))
    h = {'Accept-Language': 'zh-CN', 'Origin': ORIGINS[instance], 'Cookie': cookie or session_cookie(instance, 'admin'),
         'X-AIOps-Environment': env or DEFAULT_ENV[instance], 'X-Acceptance-Probe': PROBE,
         'Idempotency-Key': _secrets.token_hex(16), 'Content-Type': 'application/json', **headers}
    req = urllib.request.Request(ORIGINS[instance] + '/api/v1' + path, json.dumps(body or {}).encode(), method=method, headers=h)
    try:
        with urllib.request.urlopen(req, context=ctx, timeout=60) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode(errors='replace')[:500]


def fill(path, params):
    out = path
    for k, v in params.items():
        out = out.replace('{' + k + '}', str(v))
    return out


def run(instance, table, effective, cookies, roles, scoped=None):
    """Valid-body probes for every object-scoped write route in table. Returns
    result rows shaped like permissions.py rows plus probe, guard and verdict."""
    rows, skipped = [], []
    visible = Sampler(instance)
    foreign = None
    if scoped:
        envs = {e for e in ((visible.get('/instance') or {}).get('environments') or []) if isinstance(e, str)}
        envs |= {d['id'] for d in walk((visible.get('/instance') or {}).get('environments')) if isinstance(d.get('id'), str)}
        other = sorted(e for e in envs if e not in (scoped['environment'], 'all'))
        # The default environment holds the lab data; others (dev, staging) may be empty.
        default = (visible.get('/instance') or {}).get('default_env')
        if default in other:
            other.insert(0, other.pop(other.index(default)))
        foreign = Sampler(instance, other[0]) if other else None
        if not foreign:
            skipped.append(('*', 'scoped', 'no second environment on this instance'))
    for key in sorted(table):
        method, path = key.split(' ', 1)
        scoped_route, module, action = table[key]
        if method == 'GET' or not scoped_route:
            continue
        if key in SKIP or key not in PROBES:
            skipped.append((key, '*', SKIP.get(key, 'no valid-body entry')))
            continue
        probe = PROBES[key]
        runs = [(role, cookies[role], visible, None) for role in roles if (module, action) not in effective[role]]
        if foreign:
            runs.append(('scoped', scoped['cookie'], foreign, scoped['environment']))
        for role, cookie, sampler, env in runs:
            if role == 'scoped' and (probe.guard in ('creates', 'own_pref') or '{' not in path):
                skipped.append((key, role, 'foreign probe needs an object in the path and a guard that writes nothing'))
                continue
            try:
                params, body, headers = probe.build(sampler)
            except (StopIteration, KeyError, TypeError, AttributeError) as e:
                skipped.append((key, role, f'no sample ({type(e).__name__})'))
                continue
            if any(v in (None, 'None') for v in params.values()) or has_none(body):
                skipped.append((key, role, 'no sample'))
                continue
            if role == 'scoped':
                first = path.split('}', 1)[0] + '}'
                if call(instance, 'GET', fill(first, params).replace('/api/v1', '', 1), env=env, probe=PROBE)[0] == 200:
                    skipped.append((key, role, 'object also readable in the scoped environment'))
                    continue
            target = fill(path, params).replace('/api/v1', '', 1)
            if headers:
                status, resp = send(instance, method, target, body, env, cookie if role != 'admin' else None, headers)
            else:
                status, resp = call(instance, method, target, body or None, env=env,
                                    cookie=cookie if role != 'admin' else None, role='admin', probe=PROBE)
            code = resp.get('error', {}).get('code') if isinstance(resp, dict) and isinstance(resp.get('error'), dict) else None
            ok, verdict = judge(status, probe.guard)
            row = {'route': key, 'role': role, 'module': module, 'action': action, 'object_scoped': True, 'allowed': False,
                   'status': status, 'code': code, 'ok': ok, 'inconclusive': verdict in ('guard_hit', 'body_rejected', 'inconclusive'),
                   'probe': 'valid_body:' + ('foreign' if role == 'scoped' else 'visible'), 'guard': probe.guard,
                   'verdict': verdict, 'target': params, **({'note': probe.note} if probe.note else {}),
                   'body_text': resp if isinstance(resp, str) else None}  # permissions.record() pops it
            if verdict == 'ACCEPTED':
                created = resp.get('id') if isinstance(resp, dict) else None
                row['cleanup'] = {'guard': probe.guard, 'effect': probe.effect or 'none expected (see guard)', 'created_id': created}
            rows.append(row)
    return rows, skipped
