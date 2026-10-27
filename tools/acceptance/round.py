#!/usr/bin/env python3
"""One record per convergence round (WAN-434 T3; fields from docs/plans/2026-10-16-defect-convergence.md §2.3 and §7).

  python3 tools/acceptance/round.py start 1 --drill "重试风暴" --role operator --cards C1 C2 C3 --data-states 空 第一次
  python3 tools/acceptance/round.py finish 1 --exploration-minutes 30 \\
        --defect PA-105,P1,首次路径,任务卡 --defect PA-106,P3,重复路径,回归 --load-exemptions 1
  python3 tools/acceptance/round.py show 1          # the registry paragraph again, on stdout

The round id is "1", "2", ... for counted rounds of phase 2, or a label such as "phase0" / "phase1"
(phase rounds never count towards the streak). Records live in .local/acceptance/rounds/<id>.json.

start   artifact version (`go version -m` of .local/acceptance/bin/aiops: vcs.revision, vcs.modified; a modified
        tree gets a loud warning), host 1-minute load and core count, the credential check (runs credentials.py
        and reads its JSON; --credentials-json FILE reuses a result, --skip-credentials leaves it empty), drill,
        role, task cards, data states.
finish  host load again, exploration minutes, new defects, load exemptions, the streak and whether the round is
        valid, then prints the Markdown paragraph for the registry. --cards / --data-states / --first-combo record
        what was actually done, replacing what start planned.

Rules applied at finish (§2.2, §2.3):
  * a new P0/P1 sets the streak to 0, in a round that is void too; a void round otherwise leaves it unchanged;
  * a P0/P1 found between rounds (fixing, review, static checks) is kept in the previous round's record under
    after_round_defects and resets the streak from then on: the round's own count stays as it was recorded;
  * void: --invalid-reason TEXT (attach evidence: watchdog output, container state), more than 2 load exemptions,
    or an artifact built from a modified tree (counted rounds only);
  * at most one counted round per calendar day (a second valid one the same day is flagged);
  * model usage (finish): calls, failures and tokens recorded between start and finish are counted from the
    acceptance databases (read only, through audit_writes.readonly_json); any failure, or more calls than twice the
    median of earlier valid rounds (needs 3 of them), is flagged loudly. DeepSeek has no budget cap, so this is the
    only watch on it. --model-usage-json FILE reuses a result, --skip-model-usage leaves it out;
  * the cap: N + 5 valid counted rounds without reaching N (5) in a row stops the phase; the paragraph says so.
"""
import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/acceptance'
sys.path.insert(0, str(Path(__file__).resolve().parent))
BIN = LOCAL / 'bin/aiops'
N = 5
CAP = N + 5
SEVERITIES = ('P0', 'P1', 'P2', 'P3', '缺能力', '缺来源')  # the last two never reset the streak (§2.3)
NOVELTY = ('首次路径', '首次数据状态', '首次角色', '重复路径', '修复引入')
FOUND_BY = ('回归', '演练', '任务卡', '自由探索', '用户亲测', '审查')
LOAD_LIMIT = 24  # §2.3: 2 × 12 cores; timeouts above this load are re-run once below 12 before they count


def rounds_dir():
    return Path(os.environ.get('ACCEPTANCE_ROUNDS_DIR', LOCAL / 'rounds'))


def path_for(round_id):
    if not re.fullmatch(r'[A-Za-z0-9._-]+', round_id):
        sys.exit(f'round id must be letters, digits, . _ - only: {round_id!r}')
    return rounds_dir() / f'{round_id}.json'


def counted(round_id):
    return round_id.isdigit()


def now():
    return dt.datetime.now().astimezone().isoformat(timespec='seconds')


def host_load():
    return {'load1': round(os.getloadavg()[0], 2), 'cores': os.cpu_count(), 'at': now()}


def artifact(binary):
    """vcs.revision / vcs.modified of the deployed binary (not git HEAD, which says nothing about what runs)."""
    out = {'binary': str(binary)}
    try:
        text = subprocess.run(['go', 'version', '-m', str(binary)], capture_output=True, text=True, check=True, timeout=60).stdout
    except (OSError, subprocess.SubprocessError) as e:
        return {**out, 'error': f'go version -m failed: {type(e).__name__}'}
    for key in ('vcs.revision', 'vcs.time', 'vcs.modified'):
        m = re.search(rf'^\s*build\s+{re.escape(key)}=(\S+)', text, re.M)
        out[key.replace('vcs.', '')] = m[1] if m else None
    out['modified'] = out.get('modified') == 'true' if out.get('modified') else None
    return out


def warn_artifact(art):
    if art.get('modified'):
        bar = '!' * 72
        print(f"{bar}\n!! 验收二进制是在有未提交改动的工作区构建的（vcs.modified=true，revision {art.get('revision')}）。\n"
              f"!! 轮次记录要求 vcs.modified=false；请从干净提交重新构建并部署，否则本轮无效。\n{bar}", file=sys.stderr)
    elif art.get('modified') is None:
        print(f"WARNING: could not read vcs.modified from {art['binary']} ({art.get('error', 'no vcs stamp')})", file=sys.stderr)


def credentials(args):
    if args.skip_credentials:
        return None
    if args.credentials_json:
        return json.loads(Path(args.credentials_json).read_text())
    target = LOCAL / 'e2e/credentials.json'
    before = target.stat().st_mtime if target.exists() else 0
    r = subprocess.run([sys.executable, str(Path(__file__).with_name('credentials.py')), '--json', str(target)],
                       capture_output=True, text=True, timeout=300)
    if not target.exists() or target.stat().st_mtime == before:
        return {'error': f'credentials.py wrote no result (exit {r.returncode}): {(r.stdout + r.stderr).strip()[-300:]}'}
    return json.loads(target.read_text())


# Where the product leaves countable model calls. Every query counts a time window and returns numbers only:
# prompts, answers and error text are never selected. FAILED_ERR marks provider-side failures (429, 5xx, timeouts).
FAILED_ERR = "'429|rate.?limit|5[0-9]{2}|time.?out|deadline|unavailable'"
USAGE_QUERIES = {
    # investigation canvas/branch attempts (internal/store/run_steps.go): one row per model call
    'canvas_model_steps': f"""SELECT count(*) AS calls, count(*) FILTER (WHERE status='failed') AS failed,
        count(*) FILTER (WHERE status='failed' AND error ~* {FAILED_ERR}) AS provider_failed,
        count(*) FILTER (WHERE status='running') AS unfinished,
        coalesce(sum(input_tokens),0) AS input_tokens, coalesce(sum(output_tokens),0) AS output_tokens,
        count(*) FILTER (WHERE input_tokens IS NULL) AS tokens_unreported
        FROM run_steps WHERE kind='model' AND started_at >= :'since'::timestamptz AND started_at < :'until'::timestamptz""",
    # conversation turns (v3_conversation_replies): one row per reply, each with one or more model calls; only the total
    # token figure is kept, in the assistant message's run block
    'conversation_replies': f"""SELECT count(*) AS calls, count(*) FILTER (WHERE r.state='failed') AS failed,
        count(*) FILTER (WHERE r.state='failed' AND r.error ~* {FAILED_ERR}) AS provider_failed,
        count(*) FILTER (WHERE r.state IN ('queued','running')) AS unfinished,
        coalesce(sum(CASE WHEN jsonb_typeof(m.run->'tokens')='number' THEN (m.run->>'tokens')::bigint END),0) AS tokens_total
        FROM v3_conversation_replies r LEFT JOIN v3_conversation_messages m ON m.id=r.assistant_message_id
        WHERE r.started_at >= :'since'::timestamptz AND r.started_at < :'until'::timestamptz""",
    'prompt_trials': f"""SELECT count(*) AS calls, count(*) FILTER (WHERE status='failed') AS failed,
        count(*) FILTER (WHERE status='failed' AND error_code ~* {FAILED_ERR}) AS provider_failed,
        count(*) FILTER (WHERE status='started') AS unfinished,
        coalesce(sum(input_tokens),0) AS input_tokens, coalesce(sum(output_tokens),0) AS output_tokens
        FROM v3_agent_prompt_trial_runs WHERE status<>'pending' AND started_at >= :'since'::timestamptz AND started_at < :'until'::timestamptz""",
    'rule_assistant': f"""SELECT count(*) AS calls, count(*) FILTER (WHERE status IN ('failed','ambiguous')) AS failed,
        count(*) FILTER (WHERE status IN ('failed','ambiguous') AND error_code ~* {FAILED_ERR}) AS provider_failed,
        count(*) FILTER (WHERE status='running') AS unfinished,
        coalesce(sum(input_tokens),0) AS input_tokens, coalesce(sum(output_tokens),0) AS output_tokens
        FROM v3_rule_generate_attempts WHERE created_at >= :'since'::timestamptz AND created_at < :'until'::timestamptz""",
    'agent_llm_spans': """SELECT count(*) AS calls, count(*) FILTER (WHERE status='failed') AS failed, 0 AS provider_failed, 0 AS unfinished,
        coalesce(sum(token_input),0) AS input_tokens, coalesce(sum(token_output),0) AS output_tokens
        FROM v3_agent_run_spans WHERE kind='llm' AND started_at >= :'since'::timestamptz AND started_at < :'until'::timestamptz""",
    # V3 investigation canvas replies (v3_investigation_node_replies): one row per reply, each with one or more model
    # calls; run_steps above only has the legacy executor's steps (round 4 found them uncounted). No token columns.
    'investigation_replies': """SELECT count(*) AS calls, count(*) FILTER (WHERE state='failed') AS failed,
        count(*) FILTER (WHERE state='failed' AND failure_code IN ('model_unavailable','reply_timeout')) AS provider_failed,
        count(*) FILTER (WHERE state='running') AS unfinished
        FROM v3_investigation_node_replies WHERE created_at >= :'since'::timestamptz AND created_at < :'until'::timestamptz""",
}
USAGE_LABELS = {'canvas_model_steps': '画布/调查模型调用', 'conversation_replies': '对话回复（每条含 ≥1 次调用）', 'prompt_trials': 'Prompt 试运行',
                'rule_assistant': '规则助手', 'agent_llm_spans': 'Agent 运行 LLM 片段', 'investigation_replies': '调查画布回复（每条含 ≥1 次调用）'}


def model_usage(since, until, databases=None):
    """Count model calls, failures and tokens in [since, until) over the acceptance databases."""
    from acceptance import INSTANCES
    from audit_writes import readonly_json
    per_db, sources = {}, {}
    for spec in INSTANCES.values():
        db = spec['db']
        if databases and db not in databases:
            continue
        per_db[db] = readonly_json(db, USAGE_QUERIES, {'since': since, 'until': until})
    for name in USAGE_QUERIES:
        rows = [d[name][0] for d in per_db.values() if d.get(name)]
        if not rows:
            sources[name] = None  # no database answered: the table is missing or the query failed
            continue
        total = {}
        for row in rows:
            for k, v in row.items():
                total[k] = total.get(k, 0) + (v or 0)
        sources[name] = total
    return summarize_usage(since, until, sources, per_db)


def summarize_usage(since, until, sources, per_db=None):
    ok = {k: v for k, v in sources.items() if v}
    return {'window': {'since': since, 'until': until}, 'sources': sources,
            'unavailable': [k for k, v in sources.items() if v is None],
            'total_calls': sum(v['calls'] for v in ok.values()), 'failed': sum(v['failed'] for v in ok.values()),
            'provider_failed': sum(v['provider_failed'] for v in ok.values()), 'unfinished': sum(v['unfinished'] for v in ok.values()),
            'input_tokens': sum(v.get('input_tokens', 0) for v in ok.values()), 'output_tokens': sum(v.get('output_tokens', 0) for v in ok.values()),
            'conversation_tokens_total': (ok.get('conversation_replies') or {}).get('tokens_total', 0),
            'per_database': per_db}


def usage_alerts(usage, history):
    """Reasons to look at this round's model usage; history = total_calls of earlier valid rounds."""
    alerts = []
    if usage.get('failed'):
        alerts.append(f"模型调用失败 {usage['failed']} 次（其中 429/5xx/超时类 {usage['provider_failed']} 次）：DeepSeek 外部故障要有明确提示且可重试，否则按缺陷定级")
    if usage.get('unfinished'):
        alerts.append(f"有 {usage['unfinished']} 条调用在收工时仍未结束")
    if len(history) >= 3:
        median = sorted(history)[len(history) // 2] if len(history) % 2 else sum(sorted(history)[len(history) // 2 - 1:len(history) // 2 + 1]) / 2
        if median and usage['total_calls'] > 2 * median:
            alerts.append(f"调用次数 {usage['total_calls']} 超过此前 {len(history)} 个有效轮次中位数 {median:g} 的 2 倍")
    if usage.get('unavailable'):
        alerts.append('这些来源没查到（表缺失或查询失败）：' + '、'.join(USAGE_LABELS[k] for k in usage['unavailable']))
    return alerts


def load(round_id):
    p = path_for(round_id)
    if not p.exists():
        sys.exit(f'{p} does not exist; run `round.py start {round_id}` first')
    return json.loads(p.read_text())


def save(record):
    p = path_for(record['round'])
    p.parent.mkdir(parents=True, exist_ok=True)
    tmp = p.with_suffix('.json.tmp')
    tmp.write_text(json.dumps(record, ensure_ascii=False, indent=1) + '\n')
    tmp.replace(p)
    return p


def cmd_start(a):
    p = path_for(a.round)
    if p.exists() and not a.force:
        sys.exit(f'{p} already exists (--force to overwrite)')
    art = artifact(Path(a.binary))
    warn_artifact(art)
    start = host_load()
    if start['load1'] > LOAD_LIMIT:
        print(f"WARNING: host load {start['load1']} is above {LOAD_LIMIT}; timeouts now must be re-run below {LOAD_LIMIT // 2} to count.", file=sys.stderr)
    record = {'round': a.round, 'counted': counted(a.round), 'date': dt.date.today().isoformat(), 'started_at': now(),
              'artifact': art, 'host': {'start': start}, 'credentials': credentials(a),
              'drill': a.drill, 'role': a.role, 'cards': a.cards, 'data_states': a.data_states,
              'exploration_minutes': None, 'new_defects': [], 'load_exemptions': 0, 'streak': None,
              'valid': None, 'invalid_reason': None, 'notes': a.note or '', 'first_time_combos': a.first_combo or []}
    creds = record['credentials'] or {}
    if creds.get('expiring'):
        print('WARNING: credentials expiring within 48 h: ' + ', '.join(f"{c['name']} ({c['hours_left']} h)" for c in creds['expiring']), file=sys.stderr)
    if creds.get('failed'):
        print(f"WARNING: {creds['failed']} credential check(s) failed; a round cannot start on dead credentials.", file=sys.stderr)
    print(f'started round {a.round}: {save(record)}')


def parse_defect(text):
    parts = [x.strip() for x in text.split(',')]
    if len(parts) != 4:
        sys.exit(f'--defect needs ID,级别,新颖性,发现方式: {text!r}')
    ident, sev, novelty, found = parts
    for value, allowed, what in ((sev, SEVERITIES, '级别'), (novelty, NOVELTY, '新颖性'), (found, FOUND_BY, '发现方式')):
        if value not in allowed:
            sys.exit(f'{what} must be one of {", ".join(allowed)}: {value!r}')
    return {'id': ident, 'severity': sev, 'novelty': novelty, 'found_by': found}


def streak_before(round_id, records):
    """Streak after the counted rounds that precede this one, in numeric order."""
    streak = 0
    for r in sorted((r for r in records if r['counted'] and r.get('valid') is not None and int(r['round']) < int(round_id)), key=lambda r: int(r['round'])):
        if any(d['severity'] in ('P0', 'P1') for d in r['new_defects']):
            streak = 0
        elif r['valid']:
            streak += 1
        # Found after the round, before the next one: the next round starts from 0.
        if any(d['severity'] in ('P0', 'P1') for d in r.get('after_round_defects', [])):
            streak = 0
    return streak


def all_records():
    d = rounds_dir()
    return [json.loads(p.read_text()) for p in sorted(d.glob('*.json'))] if d.exists() else []


def cmd_finish(a):
    record = load(a.round)
    record['finished_at'] = now()
    record['host']['end'] = host_load()
    end_art = artifact(Path(a.binary))
    if end_art.get('revision') != record['artifact'].get('revision'):
        record['artifact_end'] = end_art
        print(f"WARNING: the deployed artifact changed during the round ({record['artifact'].get('revision')} -> {end_art.get('revision')}).", file=sys.stderr)
    if a.exploration_minutes is not None:
        record['exploration_minutes'] = a.exploration_minutes
    # What was actually done replaces what start planned (cards skipped, a data state swapped, first-time combos
    # only known at the end).
    for key, value in (('cards', a.cards), ('data_states', a.data_states), ('first_time_combos', a.first_combo)):
        if value is not None:
            record[key] = value
    record['new_defects'] = [parse_defect(d) for d in a.defect or []] or record['new_defects']
    record['load_exemptions'] = a.load_exemptions if a.load_exemptions is not None else record['load_exemptions']
    reasons = [a.invalid_reason] if a.invalid_reason else []
    if record['load_exemptions'] > 2:
        reasons.append(f"load exemptions {record['load_exemptions']} > 2")
    if record['counted'] and record['artifact'].get('modified'):
        reasons.append('artifact built from a modified working tree (vcs.modified=true)')
    records = [r for r in all_records() if r['round'] != record['round']]
    if record['counted'] and not reasons and any(r['counted'] and r['valid'] and r['date'] == record['date'] for r in records):
        reasons.append(f"another valid counted round already exists on {record['date']} (one per calendar day)")
    record['valid'] = not reasons
    record['invalid_reason'] = '; '.join(reasons) or None
    reset = any(d['severity'] in ('P0', 'P1') for d in record['new_defects'])
    if record['counted']:
        prior = streak_before(record['round'], records)
        record['streak'] = 0 if reset else (prior + 1 if record['valid'] else prior)
        record['valid_rounds_so_far'] = sum(1 for r in records if r['counted'] and r['valid'] and int(r['round']) < int(record['round'])) + int(record['valid'])
    if not a.skip_model_usage:
        try:
            if a.model_usage_json:
                usage = json.loads(Path(a.model_usage_json).read_text())
            else:
                usage = model_usage(record['started_at'], record['finished_at'])
            history = [r['model_usage']['total_calls'] for r in records if r.get('valid') and (r.get('model_usage') or {}).get('total_calls') is not None]
            usage['alerts'] = usage_alerts(usage, history)
            record['model_usage'] = usage
        except (SystemExit, Exception) as e:  # noqa: BLE001
            record['model_usage'] = {'error': str(e)[:300], 'alerts': ['模型调用次数没统计到：' + str(e)[:200]]}
        for alert in record['model_usage']['alerts']:
            print(f'!! 模型调用：{alert}', file=sys.stderr)
    if a.note:
        record['notes'] = (record['notes'] + '\n' + a.note).strip()
    print(f'finished round {a.round}: {save(record)}')
    print()
    print(paragraph(record))


def usage_text(u):
    if not u:
        return '未统计'
    if u.get('error'):
        return f"⚠ 没统计到：{u['error']}"
    parts = [f"共 {u['total_calls']} 次"] + [f"{USAGE_LABELS[k]} {v['calls']}" for k, v in u['sources'].items() if v and v['calls']]
    parts.append(f"失败 {u['failed']}（429/5xx/超时类 {u['provider_failed']}）")
    parts.append(f"token 输入 {u['input_tokens']} / 输出 {u['output_tokens']}" + (f"；对话回复合计 {u['conversation_tokens_total']}" if u.get('conversation_tokens_total') else ''))
    text = '；'.join(parts)
    return text + ''.join(f'<br>⚠ {a}' for a in u.get('alerts', []))


def paragraph(r):
    art, creds = r['artifact'], r.get('credentials') or {}
    h = r['host']
    host = lambda x: f"{x['load1']}（{x['cores']} 核）" if x else '—'  # noqa: E731
    cred = '未检查' if not r.get('credentials') else (creds.get('error') or f"{creds['passed']}/{creds['total']} 通过"
                                                  + (f"；48 小时内到期：{', '.join(c['name'] + ' ' + str(c['expires_at']) for c in creds['expiring'])}" if creds.get('expiring') else ''))
    valid = '—（未收工）' if r['valid'] is None else ('是' if r['valid'] else f"否：{r['invalid_reason']}")
    defects = r['new_defects']
    repeat = sum(d['novelty'] == '重复路径' for d in defects)
    lines = [f"### 轮次 {r['round']}（{r['date']}）", '',
             '| 项 | 内容 |', '| --- | --- |',
             f"| 产物 | `{art.get('revision') or '?'}`，vcs.modified={art.get('modified')}{' ⚠ 有未提交改动' if art.get('modified') else ''} |",
             f"| 宿主 1 分钟负载（开始 / 结束） | {host(h.get('start'))} / {host(h.get('end'))} |",
             f"| 凭据检查 | {cred} |",
             f"| 演练场景 | {r['drill'] or '—'} |", f"| 角色 | {r['role'] or '—'} |",
             f"| 任务卡 | {'、'.join(r['cards']) if r['cards'] else '—'} |",
             f"| 数据状态 | {'、'.join(r['data_states']) if r['data_states'] else '—'} |",
             f"| 自由探索 | {r['exploration_minutes'] if r['exploration_minutes'] is not None else '—'} 分钟 |",
             f"| 模型调用 | {usage_text(r.get('model_usage'))} |",
             f"| 负载豁免 | {r['load_exemptions']} 次（超过 2 次本轮作废） |",
             f"| 本轮有效 | {valid} |"]
    if r['counted']:
        streak = '—' if r['streak'] is None else f"{r['streak']} / {N}"
        lines.append(f"| 连续无 P0/P1 计数 | {streak}；有效轮次累计 {r.get('valid_rounds_so_far', '—')} / 上限 {CAP} |")
        if r.get('valid_rounds_so_far') and r['valid_rounds_so_far'] >= CAP and (r['streak'] or 0) < N:
            lines.append(f"| ⚠ 已到上限 | 有效轮次已达 {CAP} 仍未连续 {N} 轮：停下，按模块聚类复盘，由用户决定 |")
    lines += ['', '新缺陷：' + ('无' if not defects else ''), '']
    if defects:
        lines += ['| 编号 | 级别 | 新颖性 | 发现方式 |', '| --- | --- | --- | --- |'] + \
                 [f"| {d['id']} | {d['severity']} | {d['novelty']} | {d['found_by']} |" for d in defects] + ['']
    lines += [f"三个数：新 P0/P1 {sum(d['severity'] in ('P0', 'P1') for d in defects)} 条；"
              f"本轮第一次执行的路径与数据状态组合 {len(r.get('first_time_combos', []))} 个"
              + (f"（{'、'.join(r['first_time_combos'])}）" if r.get('first_time_combos') else '')
              + f"；新颖性为“重复路径”的缺陷 {repeat} 条。"]
    if r.get('notes'):
        lines += ['', f"备注：{r['notes']}"]
    return '\n'.join(lines)


def cmd_show(a):
    print(paragraph(load(a.round)))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest='cmd', required=True)
    s = sub.add_parser('start')
    s.add_argument('round')
    s.add_argument('--drill')
    s.add_argument('--role')
    s.add_argument('--cards', nargs='*', default=[])
    s.add_argument('--data-states', nargs='*', default=[])
    s.add_argument('--first-combo', nargs='*', help='paths / data states run for the first time this round')
    s.add_argument('--binary', default=str(BIN))
    s.add_argument('--credentials-json')
    s.add_argument('--skip-credentials', action='store_true')
    s.add_argument('--note')
    s.add_argument('--force', action='store_true')
    s.set_defaults(fn=cmd_start)
    f = sub.add_parser('finish')
    f.add_argument('round')
    f.add_argument('--exploration-minutes', type=int)
    f.add_argument('--defect', action='append', help='ID,级别,新颖性,发现方式 (repeatable); 新颖性: ' + '/'.join(NOVELTY) + '; 发现方式: ' + '/'.join(FOUND_BY))
    f.add_argument('--load-exemptions', type=int)
    f.add_argument('--cards', nargs='*', help='the task cards actually done (replaces the planned list)')
    f.add_argument('--data-states', nargs='*', help='the data states actually run (replaces the planned list)')
    f.add_argument('--first-combo', nargs='*', help='paths / data states run for the first time this round')
    f.add_argument('--invalid-reason', help='void the round with this reason (attach evidence in the registry)')
    f.add_argument('--binary', default=str(BIN))
    f.add_argument('--note')
    f.add_argument('--model-usage-json', help='reuse a model usage result instead of querying the databases')
    f.add_argument('--skip-model-usage', action='store_true')
    f.set_defaults(fn=cmd_finish)
    w = sub.add_parser('show')
    w.add_argument('round')
    w.set_defaults(fn=cmd_show)
    a = ap.parse_args()
    a.fn(a)


if __name__ == '__main__':
    main()
