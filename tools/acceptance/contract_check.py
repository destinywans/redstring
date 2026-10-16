#!/usr/bin/env python3
"""Static three-way check: frontend requests, backend V3 routes, API contract (T0).

  python3 tools/acceptance/contract_check.py [--routes routes.json] [--out DIR]

Offline only; it calls no instance. Sources:
  * frontend: every request web/src/v3 (mock/ excluded) can send: api(), apiWithMeta(),
    useGet(), useGetWithEtag(), useSend() and raw "/api/v1/..." literals (fetch,
    EventSource, XHR). Paths come from string and template literals in the path
    argument (each branch of a ternary counts), ${...} becomes a path parameter;
  * backend: routescan (go run ./tools/acceptance/routescan, or --routes): routes
    marked for V3, legacy-only patterns (V3 answers 501 v3_not_implemented) and the
    auth/instance and signed-push exemptions;
  * contract: docs/api/v3-contract.md table rows with their status (implemented,
    mock, v1.1·已实现, v1.1·待实现) and the auxiliary-route bullets.

A request is matched the way Go's ServeMux does it: among the registered patterns
with the same method that match the path, the most specific one wins; the V3
middleware then answers 501 unless that pattern is marked or exempt.

Writes contract-check.json and contract-check.md to --out (default
.local/acceptance/contract-check). The report lists findings for a person to
confirm on an instance; it registers no defect.
"""
import argparse
import json
import re
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from permissions import ROOT, scan  # noqa: E402

WEB = ROOT / 'web/src/v3'
CONTRACT = ROOT / 'docs/api/v3-contract.md'
HELPERS = {'api': None, 'apiWithMeta': None, 'useSend': None, 'useGet': 'GET', 'useGetWithEtag': 'GET'}
METHODS = ('GET', 'POST', 'PUT', 'PATCH', 'DELETE')
CALL = re.compile(r'\b(api|apiWithMeta|useGet|useGetWithEtag|useSend)\s*(?=[<(])')
PENDING = {'mock', 'v1.1·待实现'}

# What a person found when reading the source for a hit (date, finding). Keyed by
# "METHOD /api/v1/path"; the report shows "not reviewed" for hits without a note.
NOTES = {
    'POST /api/v1/investigations/{}/evidence': {
        'date': '2026-10-16',
        'ui': '有：画布页工具栏“证据托盘”打开托盘，把托盘项拖到画布上（CanvasPage addEvidence）；真实模式直接调用，'
              '托盘项来自 GET /investigations/{id}/canvas 的 tray，托盘为空时无从触发',
    },
    'POST /api/v1/webhooks/{}/redeliver': {
        'date': '2026-10-16',
        'ui': '有：Webhook 与 API 页“最近投递”面板右上角“重新投递”（WebhooksPage:139），没有按真实/示意模式区分；'
              '默认选中项写死为 gitlab（WebhooksPage:18），于是实际请求 POST /api/v1/webhooks/gitlab/redeliver，'
              '会落到豁免的 GitLab 接收路由 POST /webhooks/gitlab/{source}（source=redeliver，配置了交付模块时注册），'
              '按代码返回 401 Invalid webhook identity，而 api() 遇到非 /auth/ 的 401 会跳登录页；待实例确认',
    },
}


# ---- a small JavaScript reader -------------------------------------------------

def skip_string(text, i):
    """Index after the quoted string or template literal starting at text[i]."""
    quote = text[i]
    i += 1
    while i < len(text):
        c = text[i]
        if c == '\\':
            i += 2
            continue
        if c == quote:
            return i + 1
        if quote == '`' and text.startswith('${', i):
            i = skip_balanced(text, i + 1)
            continue
        i += 1
    return i


def skip_comment(text, i):
    if text.startswith('//', i):
        end = text.find('\n', i)
        return len(text) if end < 0 else end
    if text.startswith('/*', i):
        end = text.find('*/', i + 2)
        return len(text) if end < 0 else end + 2
    return i


def skip_balanced(text, i):
    """text[i] is ( [ or {; returns the index after its partner."""
    pairs = {'(': ')', '[': ']', '{': '}'}
    stack = [pairs[text[i]]]
    i += 1
    while i < len(text) and stack:
        c = text[i]
        if c in '"\'`':
            i = skip_string(text, i)
            continue
        j = skip_comment(text, i)
        if j != i:
            i = j
            continue
        if c in pairs:
            stack.append(pairs[c])
        elif stack and c == stack[-1]:
            stack.pop()
        i += 1
    return i


def skip_generics(text, i):
    """text[i] is '<' of a call's type arguments; '=>' inside is an arrow, not a close."""
    depth = 0
    while i < len(text):
        c = text[i]
        if c in '"\'`':
            i = skip_string(text, i)
            continue
        if c in '([{':
            i = skip_balanced(text, i)
            continue
        if c == '=' and text.startswith('=>', i):
            i += 2
            continue
        if c == '<':
            depth += 1
        elif c == '>':
            depth -= 1
            if depth == 0:
                return i + 1
        i += 1
    return i


def split_args(text, i):
    """text[i] is '('; returns ([argument source], index after ')')."""
    args, start, j = [], i + 1, i + 1
    while j < len(text):
        c = text[j]
        if c in '"\'`':
            j = skip_string(text, j)
            continue
        k = skip_comment(text, j)
        if k != j:
            j = k
            continue
        if c in '([{':
            j = skip_balanced(text, j)
            continue
        if c == ',':
            args.append(text[start:j])
            start = j + 1
        elif c == ')':
            args.append(text[start:j])
            return [a.strip() for a in args if a.strip()], j + 1
        j += 1
    return [a.strip() for a in args if a.strip()], j


def literals(src):
    """(value, offset) of every string or template literal in src, outside comments.
    In templates ${...} becomes {}."""
    out, i = [], 0
    while i < len(src):
        c = src[i]
        k = skip_comment(src, i)
        if k != i:
            i = k
            continue
        if c in '"\'`':
            end = skip_string(src, i)
            body = src[i + 1:end - 1]
            if c == '`':
                parts, j = [], 0
                while j < len(body):
                    if body.startswith('${', j):
                        close = skip_balanced(body, j + 1)
                        parts.append('{}')
                        j = close
                    else:
                        parts.append(body[j])
                        j += 1
                body = ''.join(parts)
            out.append((body, i))
            i = end
            continue
        i += 1
    return out


def api_path(value):
    """'/x/{}/y?z={}' -> '/x/{}/y'; None when the literal is not an API path."""
    if not value.startswith('/') or value.startswith('//') or ' ' in value:
        return None
    path = value.split('?', 1)[0].split('#', 1)[0]
    if path.startswith('/api/v1/'):
        path = path[len('/api/v1'):]
    return path.rstrip('/') or '/'


def path_literals(src):
    """API paths among the literals of an expression, leaving out comparison
    operands (`kind === "/" ? "/commands" : null`)."""
    out = []
    for value, offset in literals(src):
        end = skip_string(src, offset)
        if re.search(r'[=!]==?\s*$', src[:offset]) or re.match(r'\s*[=!]==?', src[end:]):
            continue
        path = api_path(value)
        if path and path != '/':
            out.append(path)
    return out


def line_of(text, offset):
    return text.count('\n', 0, offset) + 1


def definition(text, name, before):
    """Source of the closest `const|let|var name = ...` above offset `before`."""
    best = None
    for m in re.finditer(r'\b(?:const|let|var)\s+' + re.escape(name) + r'\s*(?::[^=]+)?=\s*', text[:before]):
        best = m
    if not best:
        return None
    j = best.end()
    while j < len(text):
        c = text[j]
        if c in '"\'`':
            j = skip_string(text, j)
            continue
        if c in '([{':
            j = skip_balanced(text, j)
            continue
        if c in ';\n':
            break
        j += 1
    return text[best.end():j]


def frontend_calls(web=WEB):
    """Requests web/src/v3 can send, one entry per (call site, method, path)."""
    calls, unresolved = [], []
    files = sorted(p for p in web.rglob('*') if p.suffix in ('.ts', '.tsx') and 'mock' not in p.relative_to(web).parts
                   and not p.name.endswith(('.test.ts', '.test.tsx', '.spec.ts', '.spec.tsx')))
    for f in files:
        rel = str(f.relative_to(ROOT))
        text = f.read_text()
        for m in CALL.finditer(text):
            name = m.group(1)
            prefix = text[max(0, m.start() - 20):m.start()]
            if re.search(r'function\s*$', prefix) or rel.endswith('api/client.ts'):
                continue
            i = m.end()
            if text[i] == '<':
                i = skip_generics(text, i)
                while i < len(text) and text[i].isspace():
                    i += 1
            if i >= len(text) or text[i] != '(':
                continue
            args, _ = split_args(text, i)
            line = line_of(text, m.start())
            site = f'{rel}:{line}'
            method_src, path_src = (None, args[0] if args else '') if HELPERS[name] else (args[0] if args else '', args[1] if len(args) > 1 else '')
            methods = [HELPERS[name]] if HELPERS[name] else [v for v, _ in literals(method_src) if v in METHODS]
            if not methods:
                unresolved.append({'site': site, 'helper': name, 'reason': f'method not a literal: {method_src[:60]}'})
                continue
            paths = path_literals(path_src)
            via = 'literal'
            if not paths and re.fullmatch(r'[A-Za-z_$][\w$]*', path_src):
                src = definition(text, path_src, m.start())
                if src:
                    paths = path_literals(src)
                    via = f'variable {path_src}'
            if not paths:
                if path_src not in ('null', 'undefined'):
                    unresolved.append({'site': site, 'helper': name, 'reason': f'path not a literal: {path_src[:80]}'})
                continue
            for method in methods:
                for path in dict.fromkeys(paths):
                    calls.append({'method': method, 'path': path, 'site': site, 'helper': name, 'via': via})
        # Raw requests outside the helpers: fetch, EventSource, XMLHttpRequest.
        if rel.endswith('api/client.ts'):
            continue
        for value, offset in literals(text):
            if not value.startswith('/api/v1/'):
                continue
            path = api_path(value)
            around = text[max(0, offset - 80):offset + 300]
            method = 'GET'
            xhr = re.search(r'\.open\(\s*["\'](\w+)["\']\s*,\s*$', text[max(0, offset - 40):offset])
            if xhr:
                method = xhr.group(1).upper()
            else:
                own = re.search(r'method:\s*["\'](\w+)["\']', around[80:])
                if own and re.match(r'fetch\(', text[max(0, offset - 6):offset]):
                    method = own.group(1).upper()
            calls.append({'method': method, 'path': path, 'site': f'{rel}:{line_of(text, offset)}', 'helper': 'raw', 'via': 'literal'})
    return calls, unresolved


# ---- routes and contract -------------------------------------------------------

def segments(path):
    return [s for s in path.strip('/').split('/') if s] if path != '/' else []


def is_param(seg):
    return seg.startswith('{') and seg.endswith('}')


def matches(pattern_path, path, strict=True):
    """True when a request path (with {} for unknown segments) hits the pattern.
    Strict: an unknown request segment only fills a pattern parameter. Loose: it
    may also stand for a literal segment (a variable that happens to hold it)."""
    ps, rs = segments(pattern_path), segments(path)
    for i, p in enumerate(ps):
        if is_param(p) and p.endswith('...}'):
            return len(rs) >= i
        if i >= len(rs):
            return False
        if is_param(p):
            continue
        if is_param(rs[i]):
            if strict:
                return False
            continue
        if p != rs[i]:
            return False
    return len(ps) == len(rs)


def best(candidates, method, path):
    """Most specific (method, path) pattern for a request, as Go's mux picks it,
    and the patterns a loose match could reach instead."""
    hits = [c for c in candidates if c[0] == method and matches(c[1], path)]
    hits.sort(key=lambda c: sum(1 for s in segments(c[1]) if is_param(s)))
    loose = [] if hits else [c for c in candidates if c[0] == method and matches(c[1], path, strict=False)]
    return (hits[0] if hits else None), loose


def backend(scanned):
    """{(method, path): {'kind': v3|legacy_only|exempt, ...}} with /api/v1 stripped."""
    out = {}
    for r in scanned['routes']:
        out[(r['method'], r['path'][len('/api/v1'):])] = {'kind': 'v3', 'module': r['module'], 'action': r['action'],
                                                         'object_scoped': r['object_scoped'], 'source': r['source']}
    for kind, rows in (('legacy_only', scanned['legacy_only']), ('exempt', scanned['exempt'])):
        for r in rows:
            out[(r['method'], r['path'][len('/api/v1'):])] = {'kind': kind, 'exempt': r.get('exempt', ''),
                                                             'source': (r['registered_at'] or [r.get('source', '')])[0]}
    return out


def contract(path=CONTRACT):
    """Contract entries: table rows (with status) and auxiliary bullets."""
    rows, section = [], ''
    for n, line in enumerate(path.read_text().splitlines(), 1):
        if line.startswith('#'):
            section = line.lstrip('#').strip()
            continue
        m = re.match(r'^\|\s*(GET|POST|PUT|PATCH|DELETE)\s*\|\s*`([^`]+)`\s*\|', line)
        if m:
            cells = [c.strip() for c in re.split(r'(?<!\\)\|', line.strip().strip('|'))]
            rows.append({'method': m[1], 'path': api_path(m[2]) or m[2], 'status': cells[-1], 'line': n,
                         'section': section, 'page': cells[4] if len(cells) == 6 else ''})
            continue
        b = re.match(r'^- `(GET|POST|PUT|PATCH|DELETE) (/[^`?\s]*)', line)
        if b and section.startswith('后端辅助接口'):
            rows.append({'method': b[1], 'path': api_path(b[2]) or b[2], 'status': 'auxiliary', 'line': n,
                         'section': section, 'page': ''})
    return rows


# ---- report --------------------------------------------------------------------

def check(scanned, calls, unresolved, entries):
    routes = backend(scanned)
    keys = list(routes)
    contract_keys = [(e['method'], e['path']) for e in entries]
    by_contract = defaultdict(list)
    for e in entries:
        by_contract[(e['method'], e['path'])].append(e)

    requests = defaultdict(list)  # (method, path) -> call sites
    for c in calls:
        requests[(c['method'], c['path'])].append(c)
    frontend = []
    for (method, path), sites in sorted(requests.items()):
        hit, loose = best(keys, method, path)
        chit, _ = best(contract_keys, method, path)
        kind = routes[hit]['kind'] if hit else 'unregistered'
        statuses = sorted({e['status'] for e in by_contract[chit]}) if chit else []
        frontend.append({
            'method': method, 'path': path, 'sites': sorted({s['site'] for s in sites}), 'helpers': sorted({s['helper'] for s in sites}),
            'backend': {'pattern': f'{hit[0]} /api/v1{hit[1]}' if hit else None, **(routes[hit] if hit else {'kind': 'unregistered'})},
            'loose_matches': [f'{a[0]} /api/v1{a[1]}' for a in loose],
            'contract': {'path': chit[1] if chit else None, 'status': statuses,
                         'lines': [e['line'] for e in by_contract[chit]] if chit else []},
            'v3_available': kind in ('v3', 'exempt'),
        })

    used_backend = {(f['method'], f['backend']['pattern'].split(' ', 1)[1][len('/api/v1'):]) for f in frontend if f['backend']['pattern']}
    used_contract = {(f['method'], f['contract']['path']) for f in frontend if f['contract']['path']}
    contract_only = []
    for e in entries:
        key = (e['method'], e['path'])
        if key in used_contract:
            continue
        hit, _ = best(keys, e['method'], e['path'])
        contract_only.append({**e, 'backend': routes[hit]['kind'] if hit else 'unregistered',
                              'backend_pattern': f'{hit[0]} /api/v1{hit[1]}' if hit else None})

    findings = {
        'frontend_unavailable_in_v3': [f for f in frontend if not f['v3_available']],
        'frontend_calls_contract_pending': [f for f in frontend if set(f['contract']['status']) & PENDING],
        'contract_without_frontend_or_backend': [e for e in contract_only if e['backend'] == 'unregistered'],
    }
    for group in findings.values():
        for f in group:
            key = f"{f['method']} /api/v1{f['path']}"
            f['note'] = NOTES.get(key)
    v3_unused = [r for r in scanned['routes'] if (r['method'], r['path'][len('/api/v1'):]) not in used_backend]
    summary = {
        'frontend_requests': len(frontend), 'frontend_call_sites': len(calls), 'frontend_unresolved': len(unresolved),
        'frontend_writes': sum(1 for f in frontend if f['method'] != 'GET'),
        'backend_v3_routes': len(scanned['routes']), 'backend_legacy_only': len(scanned['legacy_only']), 'backend_exempt': len(scanned['exempt']),
        'contract_entries': len(entries), 'contract_by_status': dict(sorted(defaultdict(int, {s: sum(1 for e in entries if e['status'] == s) for s in {e['status'] for e in entries}}).items())),
        'contract_without_frontend': len(contract_only),
        'v3_routes_without_frontend': len(v3_unused),
        'findings': {k: len(v) for k, v in findings.items()},
    }
    return {'summary': summary, 'findings': findings, 'frontend': frontend, 'frontend_unresolved': unresolved,
            'contract_without_frontend': contract_only,
            'v3_routes_without_frontend': [r['pattern'] for r in v3_unused],
            'frontend_without_contract': [f for f in frontend if not f['contract']['path']]}


def markdown(res):
    s = res['summary']
    out = ['# T0 静态比对：前端调用 × V3 路由 × 契约', '',
           '由 `tools/acceptance/contract_check.py` 生成，只读源码，没有访问实例。命中项需在实例上确认后再登记。', '',
           '## 概况', '',
           f"- 前端请求（方法 + 路径模式）{s['frontend_requests']} 个，其中写 {s['frontend_writes']} 个，来自 {s['frontend_call_sites']} 处调用；静态解析不出的调用 {s['frontend_unresolved']} 处（见文末）。",
           f"- 后端：标了 V3 的路由 {s['backend_v3_routes']} 条，只在旧版注册的 /api/v1 路由 {s['backend_legacy_only']} 条（V3 下返回 501），豁免 {s['backend_exempt']} 条。",
           f"- 契约条目 {s['contract_entries']} 个：" + '，'.join(f'{k} {v}' for k, v in s['contract_by_status'].items()) + '。',
           f"- 契约有、前端不调用的 {s['contract_without_frontend']} 个；标了 V3、前端不调用的路由 {s['v3_routes_without_frontend']} 条。", '']

    def sites(f):
        return '<br>'.join(f'`{x}`' for x in f['sites'])

    def note(f):
        if not f.get('note'):
            return '未人工核对'
        return f"{f['note']['ui']}（{f['note']['date']} 读源码）"

    out += ['## 1. 前端会调用、后端在 V3 下不可用', '',
            '“只在旧版注册”的命中由 V3 中间件返回 501 `v3_not_implemented`；“未注册”返回 404/405。', '',
            '| 请求 | 后端 | 契约 | 前端调用位置 | 界面入口 |', '| --- | --- | --- | --- | --- |']
    for f in res['findings']['frontend_unavailable_in_v3']:
        b = f['backend']
        backend_text = '未注册' if b['kind'] == 'unregistered' else f"只在旧版注册 `{b['source']}`"
        out.append(f"| `{f['method']} {f['path']}` | {backend_text} | {', '.join(f['contract']['status']) or '无'} | {sites(f)} | {note(f)} |")
    out += ['', '## 2. 契约标 mock 或待实现、前端仍会调用', '',
            '| 请求 | 契约状态（行） | 后端 | 前端调用位置 | 界面入口 |', '| --- | --- | --- | --- | --- |']
    for f in res['findings']['frontend_calls_contract_pending']:
        b = f['backend']
        out.append(f"| `{f['method']} {f['path']}` | {', '.join(f['contract']['status'])}（{', '.join(map(str, f['contract']['lines']))}） | "
                   f"{b['kind']} | {sites(f)} | {note(f)} |")
    out += ['', '## 3. 契约有、前端和后端都没有', '', '| 请求 | 契约状态（行） | 章节 |', '| --- | --- | --- |']
    for e in res['findings']['contract_without_frontend_or_backend']:
        out.append(f"| `{e['method']} {e['path']}` | {e['status']}（{e['line']}） | {e['section']} |")
    out += ['', '## 附：契约有、前端不调用（后端已有）', '', '| 请求 | 契约状态 | 后端 |', '| --- | --- | --- |']
    for e in res['contract_without_frontend']:
        if e['backend'] != 'unregistered':
            out.append(f"| `{e['method']} {e['path']}` | {e['status']} | {e['backend']} |")
    out += ['', '## 附：前端调用、契约里没有', '', '| 请求 | 后端 | 前端调用位置 |', '| --- | --- | --- |']
    for f in res['frontend_without_contract']:
        out.append(f"| `{f['method']} {f['path']}` | {f['backend']['kind']} | {sites(f)} |")
    amb = [f for f in res['frontend'] if f['loose_matches']]
    if amb:
        out += ['', '## 附：没有严格匹配、变量段取特定值时可能命中的路由', '',
                '前端路径段是变量时，只用来填路由参数；下列请求没有严格匹配，只有变量恰好等于某个字面量段时才会命中。', '']
        for f in amb:
            out.append(f"- `{f['method']} {f['path']}` → `{f['backend']['pattern'] or '未注册'}`；也可能是 " + '、'.join(f'`{a}`' for a in f['loose_matches']))
    out += ['', '## 附：静态解析不出的前端调用', '']
    for u in res['frontend_unresolved']:
        out.append(f"- `{u['site']}` {u['helper']}：{u['reason']}")
    return '\n'.join(out) + '\n'


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--routes', help='saved routescan JSON instead of running go run ./tools/acceptance/routescan')
    ap.add_argument('--out', default=str(ROOT / '.local/acceptance/contract-check'))
    a = ap.parse_args()
    calls, unresolved = frontend_calls()
    res = check(scan(a.routes), calls, unresolved, contract())
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)
    (out / 'contract-check.json').write_text(json.dumps(res, ensure_ascii=False, indent=1))
    (out / 'contract-check.md').write_text(markdown(res))
    s = res['summary']
    print(f"contract check: {s['frontend_requests']} frontend requests, {s['findings']['frontend_unavailable_in_v3']} unavailable in V3, "
          f"{s['findings']['frontend_calls_contract_pending']} calling mock/pending contract entries, "
          f"{s['findings']['contract_without_frontend_or_backend']} contract entries with neither side, "
          f"{s['frontend_unresolved']} unresolved call sites -> {out}")


if __name__ == '__main__':
    main()
