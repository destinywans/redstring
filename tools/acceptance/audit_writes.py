#!/usr/bin/env python3
"""Summarise the write operations the acceptance databases have audited since a date (read only).

  python3 tools/acceptance/audit_writes.py [--since 2026-10-09] [--db redstring_prod redstring_nonprod]

The product appends one row to management_audit (migrations/006_platform.sql, sealed with a hash chain
by 037_v3_audit.sql) for every audited write: actor_id, actor_kind, application_id, action, target,
outcome, changes, created_at. This script counts them per action and outcome with the first and last
time, a second look at which writes really ran during acceptance (coverage.py counts requests; this counts
what the server recorded). Connection details come from .local/acceptance/pg.env (POSTGRES_PASSWORD; user
aiops, as in acceptance.py). psql runs inside the aiops-acceptance-pg container through `docker exec`
(the host has no client), in a READ ONLY transaction; nothing is created, changed or deleted, and
`changes` (which can hold request data) is never selected.
Writes .local/acceptance/e2e/audit-writes.json and audit-writes.md.
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from acceptance import INSTANCES, LOCAL, PG_ENV, read_env  # noqa: E402

CONTAINER = 'aiops-acceptance-pg'
QUERIES = {
    'by_action': """SELECT action, outcome, count(*) AS n, min(created_at) AS first_at, max(created_at) AS last_at,
                           count(DISTINCT actor_id) AS actors, count(DISTINCT target) AS targets
                    FROM management_audit WHERE created_at >= :'since'::timestamptz
                    GROUP BY action, outcome ORDER BY action, outcome""",
    'by_actor_kind': """SELECT actor_kind, count(*) AS n FROM management_audit
                        WHERE created_at >= :'since'::timestamptz GROUP BY actor_kind ORDER BY n DESC""",
    'span': """SELECT count(*) AS n, min(created_at) AS first_at, max(created_at) AS last_at, max(chain_seq) AS chain_seq
               FROM management_audit WHERE created_at >= :'since'::timestamptz""",
}


def readonly_json(db, queries, variables=None):
    """Run named SELECTs, each in its own READ ONLY transaction, and return {name: parsed JSON or None}.

    A query that fails (for example a table that this build does not have) yields None; the others still run.
    Variables become psql variables, so a query refers to them as :'name'. Used by round.py too.
    """
    script = ['\\set ON_ERROR_STOP off']
    for key, value in (variables or {}).items():
        if not re.fullmatch(r'[0-9T:+\-. Z]+', value):
            sys.exit(f'--{key} looks wrong: {value}')
        script.append(f"\\set {key} '{value}'")
    for name, sql in queries.items():
        # json_agg puts a newline between elements; the output is read one line per query, so flatten it.
        script += ['BEGIN READ ONLY;', f"SELECT '{name}', replace(coalesce(json_agg(t), '[]'::json)::text, E'\\n', ' ') FROM ({sql}) t;", 'COMMIT;']
    env = dict(os.environ, PGPASSWORD=read_env(PG_ENV)['POSTGRES_PASSWORD'])
    r = subprocess.run(['docker', 'exec', '-i', '-e', 'PGPASSWORD', CONTAINER, 'psql', '-h', '127.0.0.1', '-U', 'aiops', '-d', db,
                        '-X', '-A', '-t', '-F', '\t', '-q'], input='\n'.join(script) + '\n', env=env, capture_output=True, text=True)
    if r.returncode:
        sys.exit(f'{db}: psql failed ({r.returncode}): {r.stderr.strip()[:300]}')
    out = {name: None for name in queries}
    for line in r.stdout.splitlines():
        name, _, doc = line.partition('\t')
        if name in queries:
            out[name] = json.loads(doc)
    return out


def psql(db, since):
    out = readonly_json(db, QUERIES, {'since': since})
    missing = [k for k, v in out.items() if v is None]
    if missing:
        sys.exit(f'{db}: queries failed: {", ".join(missing)} (is the schema migrated?)')
    return out


def markdown(report):
    lines = ['# 审计表里的写操作', '', f"- 起点：{report['since']}；表：management_audit（只读事务，不读 changes 字段）", '']
    for db, data in report['databases'].items():
        span = data['span'][0]
        lines += [f"## {db}", '', f"- {span['n']} 条，{span['first_at']} ～ {span['last_at']}；最大 chain_seq {span['chain_seq']}",
                  '- 按主体类型：' + ('、'.join(f"{r['actor_kind']} {r['n']}" for r in data['by_actor_kind']) or '无'), '',
                  '| action | outcome | 次数 | 首次 | 末次 | 主体数 | 对象数 |', '| --- | --- | --- | --- | --- | --- | --- |']
        lines += [f"| `{r['action']}` | {r['outcome']} | {r['n']} | {r['first_at']} | {r['last_at']} | {r['actors']} | {r['targets']} |"
                  for r in data['by_action']]
        lines.append('')
    return '\n'.join(lines)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('--since', default='2026-10-09')
    ap.add_argument('--db', nargs='*', default=[spec['db'] for spec in INSTANCES.values()])
    ap.add_argument('--out-dir', default=str(LOCAL / 'e2e'))
    a = ap.parse_args()
    report = {'since': a.since, 'databases': {db: psql(db, a.since) for db in a.db}}
    out = Path(a.out_dir)
    out.mkdir(parents=True, exist_ok=True)
    (out / 'audit-writes.json').write_text(json.dumps(report, ensure_ascii=False, indent=1, default=str))
    (out / 'audit-writes.md').write_text(markdown(report) + '\n')
    for db, data in report['databases'].items():
        print(f"{db}: {data['span'][0]['n']} audited rows, {len({r['action'] for r in data['by_action']})} distinct actions -> {out / 'audit-writes.md'}")


if __name__ == '__main__':
    main()
