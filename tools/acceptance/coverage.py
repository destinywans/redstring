#!/usr/bin/env python3
"""Which API routes the acceptance instances really served, from the TLS proxy's access log (WAN-434 T2).

  python3 tools/acceptance/coverage.py [--since 2026-10-16] [--patterns openapi|<routescan.json>]
                                       [--log PATH ...] [--verified PATH] [--out-dir DIR]
  python3 tools/acceptance/coverage.py --print-verified-template

Input: .local/acceptance/proxy-logs/*.log, one JSON object per request (acceptance.py tls writes
them: t, port, m, uri without query string, s, rt, probe, urt). The instance comes from the
listening port. Requests that carry X-Acceptance-Probe (api.py call(probe=...), e.g. the permission
matrix) are left out, so only real use counts. Paths are folded into route patterns:

  --patterns openapi      the paths of internal/apicontract/openapi.json (default)
  --patterns FILE.json    the t1-route-matrix routescan output; accepted shapes: a list of
                          "METHOD /api/v1/path" strings, a list of objects with method + path
                          (or route), or an object holding such a list under routes/items.

Two columns per route, kept apart on purpose:
  called    from the log (this script)
  verified  from a hand-kept file (--verified, default .local/acceptance/coverage-verified.json); this
            script only reads it. A route counts as verified when its layers plus its not-applicable
            layers cover all six: page, data, permission, env, error, ai. Shape:
              {"entries": [{"route": "POST /silences", "layers": ["page", "data"], "na": ["ai"],
                            "evidence": "PA-83 / golden-silence-ui"}]}
Only "called" says a route ran; the entry gate of the convergence plan counts "verified" only.

The front end's write calls are read from web/src/v3 (api(...) and useSend(...), mock/ excluded) and listed
as called / not called per instance. Output: .local/acceptance/e2e/coverage.md and coverage.json.
"""
import argparse
import collections
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from acceptance import INSTANCES, LOCAL, ROOT  # noqa: E402

LAYERS = ['page', 'data', 'permission', 'env', 'error', 'ai']
LAYER_NAMES = {'page': '页面', 'data': '数据', 'permission': '权限', 'env': '环境', 'error': '异常', 'ai': 'AI'}
WRITES = ('POST', 'PUT', 'PATCH', 'DELETE')
PORTS = {str(spec['https']): name for name, spec in INSTANCES.items()}
ID = re.compile(r'/(?:[0-9a-f]{16,}|[A-Za-z]+-[0-9a-f]{6,}|[A-Z]+-\d+|\d+)(?=/|$)')
VERIFIED_TEMPLATE = {'entries': [{'route': 'POST /silences', 'layers': ['page', 'data', 'permission'], 'na': ['ai'],
                                  'evidence': 'where it was checked (registry entry, spec name)'}]}


def strip_prefix(path):
    return path[len('/api/v1'):] if path.startswith('/api/v1/') else path


def load_patterns(source):
    """Return {(method, pattern): True}; pattern is the path after /api/v1 with {param} segments."""
    out = {}
    if source == 'openapi':
        spec = json.loads((ROOT / 'internal/apicontract/openapi.json').read_text())
        for path, ops in spec['paths'].items():
            for method in ops:
                if method.upper() in ('GET', 'HEAD', *WRITES):
                    out[(method.upper(), path)] = True
        return out
    data = json.loads(Path(source).read_text())
    if isinstance(data, dict):
        data = data.get('routes') or data.get('items') or []
    for item in data:
        if isinstance(item, str):
            method, _, path = item.partition(' ')
        elif isinstance(item, dict):
            method, path = item.get('method'), item.get('path') or item.get('route')
            if path and ' ' in path and not method:
                method, _, path = path.partition(' ')
        else:
            continue
        if method and path:
            out[(method.upper(), strip_prefix(path.strip()))] = True
    if not out:
        sys.exit(f'{source}: no routes recognised (see --help for the accepted shapes)')
    return out


class Matcher:
    """Most specific pattern first: more literal segments, fewer parameters."""

    def __init__(self, patterns):
        self.by_method = collections.defaultdict(list)
        for method, pattern in patterns:
            regex = re.compile('^' + re.sub(r'\\\{[^}]+\\\}', '[^/]+', re.escape(pattern)) + '$')
            literal = len([s for s in pattern.split('/') if s and not s.startswith('{')])
            self.by_method[method].append((-literal, pattern.count('{'), pattern, regex))
        for rules in self.by_method.values():
            rules.sort()

    def match(self, method, path):
        path = path.rstrip('/') or '/'
        for _, _, pattern, regex in self.by_method.get(method, ()):
            if regex.match(path):
                return pattern
        return None


def read_log(paths, since):
    for path in paths:
        with open(path, errors='replace') as f:
            for line in f:
                try:
                    rec = json.loads(line)
                except ValueError:
                    continue
                if not isinstance(rec, dict) or (since and rec.get('t', '') < since):
                    continue
                yield rec


def tally(paths, matcher, since):
    """{instance: {(method, pattern): Counter(status)}}, plus counts of what was left out."""
    counts = collections.defaultdict(lambda: collections.defaultdict(collections.Counter))
    skipped = collections.Counter()
    first = last = None
    for rec in read_log(paths, since):
        instance = PORTS.get(str(rec.get('port')))
        uri = rec.get('uri', '')
        if not instance or not uri.startswith('/api/v1/'):
            skipped['other port or not /api/v1'] += 1
            continue
        if rec.get('probe'):
            skipped[f"probe:{rec['probe']}"] += 1
            continue
        method, path = rec.get('m', ''), strip_prefix(uri)
        pattern = matcher.match(method, path) or f'(unmatched) {ID.sub("/{id}", path)}'
        counts[instance][(method, pattern)][str(rec.get('s'))] += 1
        first = min(first, rec['t']) if first else rec.get('t')
        last = max(last, rec['t']) if last else rec.get('t')
    return counts, skipped, first, last


API_CALL = re.compile(r'\b(?:api|apiWithMeta|useSend)\b')
METHOD = re.compile(r'"(POST|PUT|PATCH|DELETE)"')
PATH = re.compile(r'(?:\([^)]*\)\s*=>\s*)?([`"\'])((?:(?!\1).)*)\1', re.S)


def frontend_writes():
    """(method, path, file) for each write call in web/src/v3 except mock/; template parts become {id}."""
    found = {}
    for f in sorted((ROOT / 'web/src/v3').rglob('*.ts*')):
        if '/mock/' in str(f):
            continue
        text = f.read_text()
        for call in API_CALL.finditer(text):
            window = text[call.end():call.end() + 400]
            m = METHOD.search(window)
            if not m or window[:m.start()].count('(') > 4:
                continue
            p = PATH.match(window[m.end():].lstrip(' ,\n'))
            if not p or not p[2].startswith('/'):
                continue
            path = re.sub(r'\$\{[^}]*\}', '{id}', p[2]).split('?', 1)[0]
            found[(m[1], path)] = str(f.relative_to(ROOT))
    return [(m, p, f) for (m, p), f in sorted(found.items(), key=lambda kv: (kv[0][1], kv[0][0]))]


def to_pattern(matcher, method, path):
    probe = re.sub(r'\{[^}]*\}', 'x', path)
    return matcher.match(method, probe)


def load_verified(path):
    if not path.exists():
        return None
    entries = json.loads(path.read_text()).get('entries', [])
    return {e['route']: e for e in entries if 'route' in e}


def layers_text(entry):
    if not entry:
        return '—'
    have, na = set(entry.get('layers', [])), set(entry.get('na', []))
    return ' '.join(f"{LAYER_NAMES[l]}{'✓' if l in have else ('·' if l in na else '✗')}" for l in LAYERS)


def is_verified(entry):
    return bool(entry) and set(LAYERS) <= set(entry.get('layers', [])) | set(entry.get('na', []))


def build(args):
    matcher = Matcher(load_patterns(args.patterns))
    paths = [Path(p) for p in args.log] if args.log else sorted((LOCAL / 'proxy-logs').glob('*.log*'))
    if not paths:
        sys.exit(f'no access log under {LOCAL / "proxy-logs"} (acceptance.py tls writes it)')
    counts, skipped, first, last = tally(paths, matcher, args.since)
    verified = load_verified(Path(args.verified))
    instances = list(INSTANCES)

    def summary(instance, key):
        statuses = counts[instance].get(key, collections.Counter())
        return {'calls': sum(statuses.values()), 'ok': sum(n for s, n in statuses.items() if s[:1] in '23'),
                'status': dict(sorted(statuses.items()))}

    writes = []
    for method, path, file in frontend_writes():
        pattern = to_pattern(matcher, method, path)
        key = (method, pattern or path)
        entry = (verified or {}).get(f'{method} {pattern or path}')
        writes.append({'route': f'{method} {pattern or path}', 'frontend_path': path, 'file': file, 'in_contract': pattern is not None,
                       **{i: summary(i, key) for i in instances}, 'layers': layers_text(entry), 'verified': is_verified(entry)})
    routes = []
    for method, pattern in sorted(matcher_keys(matcher), key=lambda k: (k[1], k[0])):
        entry = (verified or {}).get(f'{method} {pattern}')
        routes.append({'route': f'{method} {pattern}', 'write': method in WRITES, **{i: summary(i, (method, pattern)) for i in instances},
                       'verified': is_verified(entry)})
    unmatched = sorted(({'route': f'{m} {p}', 'instance': i, **summary(i, (m, p))} for i in instances for (m, p) in counts[i] if p.startswith('(unmatched)')),
                       key=lambda r: -r['calls'])
    return {'since': args.since, 'log_first': first, 'log_last': last, 'patterns': args.patterns, 'logs': [p.name for p in paths],
            'skipped': dict(skipped), 'verified_file': args.verified if verified is not None else None,
            'frontend_writes': writes, 'routes': routes, 'unmatched': unmatched}


def matcher_keys(matcher):
    for method, rules in matcher.by_method.items():
        for _, _, pattern, _ in rules:
            yield method, pattern


def cell(s):
    if not s['calls']:
        return '没调用过'
    return f"调用过 {s['calls']}（{', '.join(f'{k}×{v}' for k, v in s['status'].items())}）"


def markdown(r):
    instances = list(INSTANCES)
    total = len(r['frontend_writes'])
    seen = sum(any(w[i]['calls'] for i in instances) for w in r['frontend_writes'])
    both = sum(all(w[i]['calls'] for i in instances) for w in r['frontend_writes'])
    verified = sum(w['verified'] for w in r['frontend_writes'])
    lines = ['# 接口覆盖（代理访问日志）', '',
             f"- 日志范围：{r['log_first'] or '—'} ～ {r['log_last'] or '—'}（`--since` {r['since'] or '不限'}）；文件：{', '.join(r['logs'])}",
             f"- 路由模式来源：{r['patterns']}；排除的请求：{json.dumps(r['skipped'], ensure_ascii=False) or '无'}",
             f"- 前端写接口 {total} 个：至少一个实例调用过 {seen}，两个实例都调用过 {both}，按六层标准验证过 {verified}"
             + ('' if r['verified_file'] else '（没有人工清单，验证栏为空）'), '',
             '## 前端写接口：调用过 / 没调用过', '',
             f"| 接口 | {' | '.join(instances)} | 六层（页面 数据 权限 环境 异常 AI） | 前端位置 |", f"| --- | {' | '.join('---' for _ in instances)} | --- | --- |"]
    for w in sorted(r['frontend_writes'], key=lambda w: (any(w[i]['calls'] for i in instances), w['route'])):
        flag = '' if w['in_contract'] else ' ⚠ 不在路由清单'
        lines.append(f"| `{w['route']}`{flag} | {' | '.join(cell(w[i]) for i in instances)} | {w['layers']} | {w['file'].split('/')[-1]} |")
    never = [x for x in r['routes'] if not any(x[i]['calls'] for i in instances)]
    lines += ['', f"## 路由清单里两个实例都没调用过的（{len(never)} / {len(r['routes'])}）", '']
    for kind, title in ((True, '写'), (False, '读')):
        items = [x['route'] for x in never if x['write'] == kind]
        lines += [f'**{title}（{len(items)}）**', ''] + [f'- `{x}`' for x in items] + ['']
    lines += ['## 调用过的路由（次数与状态码）', '', f"| 路由 | {' | '.join(instances)} |", f"| --- | {' | '.join('---' for _ in instances)} |"]
    lines += [f"| `{x['route']}` | {' | '.join(cell(x[i]) for i in instances)} |" for x in r['routes'] if any(x[i]['calls'] for i in instances)]
    lines += ['', f"## 日志里有、路由清单里匹配不上的（{len(r['unmatched'])}）", '']
    lines += [f"- `{u['route']}` {u['instance']}：{u['calls']} 次 {u['status']}" for u in r['unmatched']]
    return '\n'.join(lines) + '\n'


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--log', nargs='*', help='access log files (default: .local/acceptance/proxy-logs/*.log*)')
    ap.add_argument('--since', help='ISO date or time, e.g. 2026-10-16 (compared with the log timestamp as text)')
    ap.add_argument('--patterns', default='openapi')
    ap.add_argument('--verified', default=str(LOCAL / 'coverage-verified.json'))
    ap.add_argument('--out-dir', default=str(LOCAL / 'e2e'))
    ap.add_argument('--print-verified-template', action='store_true')
    a = ap.parse_args()
    if a.print_verified_template:
        print(json.dumps(VERIFIED_TEMPLATE, ensure_ascii=False, indent=2))
        return
    report = build(a)
    out = Path(a.out_dir)
    out.mkdir(parents=True, exist_ok=True)
    (out / 'coverage.json').write_text(json.dumps(report, ensure_ascii=False, indent=1))
    (out / 'coverage.md').write_text(markdown(report))
    writes = report['frontend_writes']
    print(f"{len(writes)} frontend writes: {sum(any(w[i]['calls'] for i in INSTANCES) for w in writes)} called, "
          f"{sum(w['verified'] for w in writes)} verified; {len(report['routes'])} routes, {len(report['unmatched'])} unmatched -> {out / 'coverage.md'}")


if __name__ == '__main__':
    main()
