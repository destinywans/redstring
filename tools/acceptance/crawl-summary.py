#!/usr/bin/env python3
"""Summarise web/e2e crawl results (.local/acceptance/e2e/crawl/*.json).

Groups failing API calls by method + normalised path + status + error code and lists
the routes each one breaks, so one root cause is triaged once. Writes
.local/acceptance/e2e/crawl-summary.md (and .json) and prints the headline counts.

Per role (WAN-434 T4): CRAWL_ROLE=viewer|operator|... reads e2e/crawl-<role> (as web/e2e/crawl.spec.ts
writes it) unless CRAWL_OUT says otherwise. Each failing group is then judged:
  exempt    listed in .local/acceptance/e2e/crawl-exemptions.json (CRAWL_EXEMPTIONS overrides the path), kept by
            the main session; an entry is {"method": "GET", "path": "/services", "status": [503, 504],
            "code": "service_view_unavailable", "instances": ["prod"], "roles": ["admin"], "reason": "D45 ..."};
            method and path are required, the rest narrow it down. Pass-through, but listed.
  expected  a 403 (404 for object-scoped routes) the role matrix says this role must get: the role's rows in
            e2e/permissions-<instance>.json (tools/acceptance/permissions.py --json) have allowed=false.
  unexpected everything else, including a 403 for a route the role is granted or one missing from the matrix.
Permission gates (.perm-gate, web/src/v3/ui/permission.tsx: a disabled entry plus text "需要 … 权限") are checked
too: every gate must be disabled and name a permission, none may name one the role holds, and a role that lacks
gated write grants (the Need tuples in web/src/v3) must show at least one on some page. Only gates that exist can be judged; an enabled write entry
that has no gate is not visible to a page load.
The headline keeps the old shape, with failing API groups counting only the unexpected ones.
"""
import collections
import json
import os
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
ROLE = os.environ.get('CRAWL_ROLE', 'admin')
# CRAWL_OUT (relative to .local/acceptance) matches web/e2e/crawl.spec.ts for fault-injection and role runs.
_DIR = os.environ.get('CRAWL_OUT', 'e2e/crawl' if ROLE == 'admin' else f'e2e/crawl-{ROLE}')
CRAWL = LOCAL / _DIR
OUT = LOCAL / f'{_DIR}-summary.md'
EXEMPTIONS = Path(os.environ.get('CRAWL_EXEMPTIONS', LOCAL / 'e2e/crawl-exemptions.json'))
# Names as web/src/v3/ui/permission.tsx shows them in the gate text (zh and en), back to the server's keys.
MODULES = {'对话与画布': 'conversation', '画布': 'canvas', '事件与告警': 'events', '告警规则与路由': 'alert_rules', '静默': 'silences',
           '写操作提案': 'proposals', 'Runbook': 'runbooks', '知识来源与检索': 'knowledge', '服务目录': 'services', '连接器与映射': 'connectors',
           'Agent 配置与发布': 'agents', '成员与角色': 'identity', '审计日志': 'audit', '资源目录与关系': 'resources', '工作区': 'workspace', '模型': 'models',
           'Conversations & canvases': 'conversation', 'Canvases': 'canvas', 'Incidents & alerts': 'events', 'Alert rules & routing': 'alert_rules',
           'Silences': 'silences', 'Write proposals': 'proposals', 'Knowledge sources & search': 'knowledge', 'Service catalog': 'services',
           'Connectors & mapping': 'connectors', 'Agent config & release': 'agents', 'Members & roles': 'identity', 'Audit log': 'audit',
           'Resource catalog & relations': 'resources', 'Workspace': 'workspace', 'Models': 'models'}
ACTIONS = {'查看': 'view', '修改': 'change', '删除': 'delete', '批准': 'approve', '管理': 'manage',
           'View': 'view', 'Change': 'change', 'Delete': 'delete', 'Approve': 'approve', 'Manage': 'manage'}
# Grants a built-in role lacks on purpose. proposals.approve belongs to the approver roles only (approving is a
# separate duty from proposing and executing); its one gate (ReviewPage `can(["proposals","approve"])`) sits on a
# detail page, so the static crawl cannot show it and the admin's missing grant must not count as a gate problem.
BY_DESIGN = {'admin': {('proposals', 'approve')}}
GATE_TEXT = re.compile(r'^(?:需要 (?P<zm>.+)·(?P<za>.+) 权限|Needs (?P<em>.+) · (?P<ea>.+) permission)$')

ID = re.compile(r'/(?:[0-9a-f]{16,}|[A-Z]+-\d+|\d+)(?=/|$)')


def norm(path):
    path = path.split('?', 1)[0]
    return ID.sub('/{id}', path)


def pattern_regex(pattern):
    return re.compile('^' + re.sub(r'\\\{[^}]+\\\}', '[^/]+', re.escape(pattern)) + '$')


def load_matrix(instance):
    """(rows by role, grants by role) from permissions.py --json, or (None, None)."""
    try:
        data = json.loads((LOCAL / f'e2e/permissions-{instance}.json').read_text())
    except (OSError, ValueError):
        return None, None
    rows = collections.defaultdict(list)
    for r in data.get('results', []):
        method, _, route = r['route'].partition(' ')
        rows[r['role']].append((method, pattern_regex(route.replace('/api/v1', '', 1)), r))
    grants = {role: {tuple(g) for g in gs} for role, gs in data.get('grants', {}).items()}
    return rows, grants


def load_exemptions():
    if not EXEMPTIONS.exists():
        return []
    return json.loads(EXEMPTIONS.read_text()).get('exemptions', [])


def exempt(rule_list, method, path, status, code, instances):
    for e in rule_list:
        if e['method'].upper() != method or not pattern_regex(e['path']).match(path):
            continue
        wanted = e.get('status')
        if wanted is not None and status not in (wanted if isinstance(wanted, list) else [wanted]):
            continue
        if e.get('code') and e['code'] != code:
            continue
        if e.get('instances') and not set(instances) <= set(e['instances']):
            continue
        if e.get('roles') and ROLE not in e['roles']:
            continue
        return e.get('reason') or 'exempt'
    return None


def judge_denial(matrix, method, path, status, instance):
    """'expected' | reason string for a failing call by ROLE; admin is never expected to be denied."""
    if status not in (403, 404):
        return None
    rows = (matrix[0] or {}).get(ROLE) if matrix and matrix[0] else None
    if rows is None:
        return 'no role matrix for this instance (run permissions.py --json)'
    for m, regex, row in rows:
        if m == method and regex.match(path):
            if row['allowed']:
                return f"role holds {row['module']}.{row['action']} but was refused"
            if status == 403 or row['object_scoped']:
                return 'expected'
            return f'{status} from a route the role is denied (the matrix expects 403)'
    return 'route is not in the role matrix' if status == 403 else None


def gated_needs():
    """(module, action) pairs the front end gates on: the Need tuples in web/src/v3 (permission.tsx), mock/ aside."""
    pairs = set()
    for f in (ROOT / 'web/src/v3').rglob('*.ts*'):
        if '/mock/' not in str(f):
            pairs.update(re.findall(r'\[\s*"([a-z_]+)",\s*"(change|delete|approve|manage)"\s*\]', f.read_text()))
    return pairs


def judge_gates(reports, matrices):
    """Problems with permission gates; also the distinct gates seen, reason -> pages."""
    problems, seen = [], collections.defaultdict(set)
    for r in reports:
        grants = (matrices.get(r['instance']) or (None, None))[1]
        held = (grants or {}).get(ROLE)
        for g in r.get('gates', []):
            tag = f"{r['instance']}:{r['route']}"
            seen[g['reason'] or '(no reason text)'].add(tag)
            m = GATE_TEXT.match(g['reason'])
            if not g['disabled']:
                problems.append(f'{tag}: gate is not disabled ({g["reason"]!r})')
            if not m:
                problems.append(f'{tag}: gate names no permission ({g["reason"]!r})')
                continue
            module = MODULES.get(m['zm'] or m['em'], m['zm'] or m['em'])
            action = ACTIONS.get(m['za'] or m['ea'], m['za'] or m['ea'])
            if held is not None and (module, action) in held:
                problems.append(f'{tag}: gated on {module}.{action} although {ROLE} holds it')
    # A role that lacks a grant the front end gates on must show at least one gate somewhere. Grants no gate
    # uses (the admin's missing proposals.approve is by design: approving is a separate role's duty, and no page
    # gates on it) cannot show up on a page, so they are listed as a note and never judged.
    grants_all = [m[1] for m in matrices.values() if m and m[1]]
    notes = []
    if grants_all:
        universe = {g for gs in grants_all for roles in gs.values() for g in roles if g[1] != 'view'}
        held = set().union(*(gs.get(ROLE, set()) for gs in grants_all))
        gated = gated_needs()
        by_design = BY_DESIGN.get(ROLE, set()) & (universe - held)
        missing = universe - held - by_design
        if by_design:
            notes.append(f'{ROLE} lacks {", ".join(f"{m}.{a}" for m, a in sorted(by_design))} by design (not judged): the only gate on it is on /review/:id, a detail page the static crawl does not load')
        if missing & gated and not seen:
            problems.append(f'{ROLE} lacks gated write grants ({", ".join(f"{m}.{a}" for m, a in sorted(missing & gated))}), yet no page showed a permission gate')
        if ROLE == 'admin' or len(missing - gated) <= 12:
            notes.append(f'{ROLE} lacks write grants that no front-end gate uses (not judged): ' + (', '.join(f'{m}.{a}' for m, a in sorted(missing - gated)) or 'none'))
        else:
            notes.append(f'{ROLE} lacks {len(missing - gated)} write grants that no front-end gate uses (not judged)')
    return problems, seen, notes


def main():
    reports = [json.loads(p.read_text()) for p in sorted(CRAWL.glob('*.json'))]
    if not reports:
        sys.exit('no crawl results')
    matrices = {inst: load_matrix(inst) for inst in {r['instance'] for r in reports}}
    rules = load_exemptions()
    failures = collections.defaultdict(lambda: {'routes': set(), 'instances': set(), 'message': '', 'count': 0})
    bad_pages = collections.defaultdict(set)  # page tag -> why it is not clean
    page_errors = collections.defaultdict(set)
    console = collections.Counter()
    blank = set()
    for r in reports:
        tag = f"{r['instance']}:{r['route']}"
        for c in r['api']:
            if c['status'] >= 400:
                key = (c['method'], norm(c['path']), c['status'], c.get('code') or '')
                f = failures[key]
                f['routes'].add(r['route'])
                f['instances'].add(r['instance'])
                f['message'] = f['message'] or (c.get('message') or '')
                f['count'] += 1
        for e in r['pageErrors']:
            page_errors[e].add(tag)
            bad_pages[tag].add('exception')
        if r['blank']:
            blank.add(tag)
            bad_pages[tag].add('blank')
        for e in r['consoleErrors']:
            if not e.startswith('Failed to load resource'):
                console[e[:160]] += 1
    # Judge every failing group: exempt, expected for this role, or unexpected.
    verdicts = {}
    for (method, path, status, code), f in failures.items():
        reason = exempt(rules, method, path, status, code, f['instances'])
        if reason:
            verdicts[(method, path, status, code)] = ('exempt', reason)
            continue
        denial = [judge_denial(matrices.get(i), method, path, status, i) for i in sorted(f['instances'])] if ROLE != 'admin' or status in (403, 404) else [None]
        if status in (403, 404) and denial and all(d == 'expected' for d in denial):
            verdicts[(method, path, status, code)] = ('expected', 'role matrix denies it')
        else:
            why = next((d for d in denial if d and d != 'expected'), '')
            verdicts[(method, path, status, code)] = ('unexpected', why)
    for (method, path, status, code), f in failures.items():
        if verdicts[(method, path, status, code)][0] == 'unexpected':
            for route in f['routes']:
                for inst in f['instances']:
                    bad_pages[f'{inst}:{route}'].add('api')
    gate_problems, gates_seen, gate_notes = judge_gates(reports, matrices)
    pages_ok = len(reports) - len(bad_pages)
    unexpected = {k: v for k, v in verdicts.items() if v[0] == 'unexpected'}
    count = collections.Counter(v[0] for v in verdicts.values())
    lines = [f'# Crawl summary ({ROLE})', '',
             f'- page loads: {len(reports)}; clean (no unexpected API error, exception or blank): {pages_ok}',
             f"- failing API groups: {len(failures)} (unexpected {count['unexpected']}, exempt {count['exempt']}, expected for the role {count['expected']}); "
             f'uncaught exception kinds: {len(page_errors)}; blank pages: {len(blank)}; permission gate problems: {len(gate_problems)}', '',
             '## Failing API calls (grouped)', '', '| # | call | status | code | verdict | instances | routes | message |', '| --- | --- | --- | --- | --- | --- | --- | --- |']
    order = {'unexpected': 0, 'exempt': 1, 'expected': 2}
    ranked = sorted(failures.items(), key=lambda kv: (order[verdicts[kv[0]][0]], -len(kv[1]['routes']), kv[0]))
    for i, ((method, path, status, code), f) in enumerate(ranked, 1):
        routes = sorted(f['routes'])
        shown = ', '.join(routes[:6]) + (f' …(+{len(routes) - 6})' if len(routes) > 6 else '')
        kind, why = verdicts[(method, path, status, code)]
        lines.append(f"| {i} | `{method} {path}` | {status} | {code} | {kind}{': ' + why if why else ''} | {','.join(sorted(f['instances']))} | {len(routes)}: {shown} | {f['message'][:120]} |")
    lines += ['', '## Permission gates', '']
    lines += [f'- {len(gates_seen)} distinct gate text(s) on {len({t for v in gates_seen.values() for t in v})} page(s)'] + \
             [f'- `{reason}` — {len(tags)} page(s)' for reason, tags in sorted(gates_seen.items(), key=lambda kv: -len(kv[1]))]
    lines += ['', '### Gate problems', ''] + ([f'- {p}' for p in gate_problems] or ['- none'])
    lines += [''] + [f'- note: {n}' for n in gate_notes]
    lines += ['', '## Uncaught exceptions', '']
    for e, tags in sorted(page_errors.items(), key=lambda kv: -len(kv[1])):
        lines.append(f'- **{e}** — {len(tags)} page(s): ' + ', '.join(sorted(tags)[:8]))
    lines += ['', '## Blank pages', ''] + [f'- {t}' for t in sorted(blank)]
    lines += ['', '## Console errors (excluding failed resource loads)', ''] + [f'- ({n}) {e}' for e, n in console.most_common(30)]
    OUT.write_text('\n'.join(lines) + '\n')
    OUT.with_suffix('.json').write_text(json.dumps({
        'role': ROLE, 'loads': len(reports), 'clean': pages_ok, 'unexpected_groups': len(unexpected), 'exempt_groups': count['exempt'],
        'expected_groups': count['expected'], 'exception_kinds': len(page_errors), 'blank': len(blank), 'gate_problems': gate_problems,
        'groups': [{'call': f'{m} {p}', 'status': st, 'code': c, 'verdict': v[0], 'why': v[1], 'instances': sorted(failures[(m, p, st, c)]['instances']),
                    'routes': sorted(failures[(m, p, st, c)]['routes'])} for (m, p, st, c), v in sorted(verdicts.items(), key=lambda kv: order[kv[1][0]])]},
        ensure_ascii=False, indent=1))
    print(f'{len(reports)} page loads, {pages_ok} clean, {len(unexpected)} failing API groups, {len(page_errors)} exception kinds, {len(blank)} blank'
          f", {count['exempt']} exempt, {count['expected']} expected-denied, {len(gate_problems)} gate problems ({ROLE}) -> {OUT.relative_to(ROOT)}")


if __name__ == '__main__':
    main()
