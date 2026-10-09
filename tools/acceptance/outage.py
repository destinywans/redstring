#!/usr/bin/env python3
"""Product-side outages on the prod acceptance instance.

  python3 tools/acceptance/outage.py model|database|restart-midstream

model              point OPENAI_BASE_URL at a closed local port (extra.env override),
                   restart, ask once: the reply must fail fast with a clear state;
                   remove the override, restart, ask again: it must answer.
database           stop aiops-acceptance-pg for 20 s under a running instance:
                   reads must answer 503 (not 500, not hang) while it is down and
                   recover without restarting the product.
kubernetes         point a copy of the inventory config at a closed port, restart: topology
                   and discovery must degrade explicitly; restore and restart.
restart-midstream  start a reply, restart the instance while it streams: the reply
                   must end failed/interrupted (not stay running), and a new
                   message must work.

Writes .local/acceptance/e2e/outages/<name>.json. Every override is removed and the
instance restarted again even when a stage fails.
"""
import argparse
import json
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
EXTRA = LOCAL / 'prod/extra.env'
OUT = LOCAL / 'e2e/outages'
MARK = '# outage.py override'


def restart():
    r = subprocess.run(['python3', 'tools/acceptance/acceptance.py', 'restart'], cwd=ROOT, capture_output=True, text=True, timeout=300)
    return r.stdout.strip().splitlines()[-4:]


def ask(text, timeout=240):
    """Operator conversation; returns the final assistant state, error and seconds."""
    status, conv = call('prod', 'POST', '/conversations', {'text': 'acceptance outage probe'}, role='operator')
    if status >= 300:
        return {'error': f'create {status} {conv}'}
    cid = conv['id']
    started = time.time()
    status, posted = call('prod', 'POST', f'/conversations/{cid}/messages', {'text': text}, role='operator')
    if status >= 300:
        return {'conversation': cid, 'error': f'message {status} {posted}'}
    last = {}
    while time.time() - started < timeout:
        _, detail = call('prod', 'GET', f'/conversations/{cid}', role='operator')
        msgs = (detail or {}).get('messages') or []
        last = msgs[-1] if msgs else {}
        if last.get('role') == 'assistant' and last.get('state') in ('completed', 'failed'):
            break
        time.sleep(3)
    statuses = [p.get('text') for p in last.get('parts', []) if p.get('type') == 'status']
    text_out = ''.join(p.get('markdown', '') for p in last.get('parts', []) if p.get('type') == 'text')
    return {'conversation': cid, 'state': last.get('state'), 'seconds': round(time.time() - started),
            'statuses': statuses[-3:], 'answer': text_out[:400]}


def reply_error(cid):
    r = subprocess.run(['docker', 'exec', 'aiops-acceptance-pg', 'psql', '-U', 'aiops', '-d', 'redstring_prod', '-Atc',
                        f"select state, left(coalesce(error,''),160) from v3_conversation_replies where conversation_id='{cid}' order by started_at desc limit 1"],
                       capture_output=True, text=True)
    return r.stdout.strip()


def model(report):
    original = EXTRA.read_text()
    try:
        EXTRA.write_text(original.rstrip('\n') + f'\n{MARK}\nOPENAI_BASE_URL=https://127.0.0.1:9\n')
        report['restart_broken'] = restart()
        report['broken'] = ask('checkout-api 现在健康吗？一句话回答。')
        report['broken']['reply_row'] = reply_error(report['broken'].get('conversation', ''))
    finally:
        EXTRA.write_text(original)
        report['restart_restored'] = restart()
    report['restored'] = ask('只回答“好”。')
    report['ok'] = report['broken'].get('state') == 'failed' and report['broken'].get('seconds', 999) < 120 \
        and report['restored'].get('state') == 'completed'


def database(report):
    timeline = []
    # Stop for 20 s (a plain restart is over in ~2 s), probing throughout, then start again.
    subprocess.run(['docker', 'stop', 'aiops-acceptance-pg'], capture_output=True)
    started = time.time()
    restarted = False
    recovered = None
    while time.time() - started < 180:
        if not restarted and time.time() - started > 20:
            subprocess.Popen(['docker', 'start', 'aiops-acceptance-pg'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            restarted = True
        t0 = time.time()
        try:
            status, body = call('prod', 'GET', '/services')
            code = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
        except Exception as e:  # timeout or connection reset
            status, code = 'exception', type(e).__name__
        timeline.append({'t': round(t0 - started, 1), 'status': status, 'code': code, 'seconds': round(time.time() - t0, 1)})
        if status == 200 and any(x['status'] != 200 for x in timeline):
            recovered = round(time.time() - started)
            break
        time.sleep(2)
    report['timeline'] = timeline
    report['recovered_after_seconds'] = recovered
    report['ok'] = recovered is not None and all(x['status'] in (200, 503) for x in timeline) and \
        all(x['seconds'] < 30 for x in timeline)


def restart_midstream(report):
    status, conv = call('prod', 'POST', '/conversations', {'text': 'acceptance restart probe'}, role='operator')
    cid = conv['id']
    call('prod', 'POST', f'/conversations/{cid}/messages',
         {'text': '请分别读取 checkout-api、order-api 的错误率和 p95，并写一段 300 字的总结。'}, role='operator')
    time.sleep(6)
    _, detail = call('prod', 'GET', f'/conversations/{cid}', role='operator')
    report['before_restart'] = ((detail or {}).get('messages') or [{}])[-1].get('state')
    report['restart'] = restart()
    started = time.time()
    state = None
    while time.time() - started < 120:
        _, detail = call('prod', 'GET', f'/conversations/{cid}', role='operator')
        state = ((detail or {}).get('messages') or [{}])[-1].get('state')
        if state in ('completed', 'failed'):
            break
        time.sleep(5)
    report['after_restart'] = {'state': state, 'seconds': round(time.time() - started), 'reply_row': reply_error(cid)}
    report['new_message'] = ask('只回答“好”。')
    report['ok'] = state in ('failed', 'completed') and report['new_message'].get('state') == 'completed'


def kubernetes(report):
    """Point a copy of the inventory config at a closed port (the cluster itself is untouched)."""
    original = EXTRA.read_text()
    inventory = LOCAL / 'prod/inventory.json'
    broken = LOCAL / 'prod/inventory-unreachable.json'
    config = json.loads(inventory.read_text())
    for source in config.get('sources', []):
        source['api_url'] = 'https://127.0.0.1:9'
    broken.write_text(json.dumps(config, indent=1))
    broken.chmod(0o600)
    paths = ['/topology', '/services/discover', '/services', '/home']
    try:
        report['baseline'] = {p: call('prod', 'GET', p)[0] for p in paths}
        EXTRA.write_text(original.rstrip('\n') + f'\n{MARK}\nAIOPS_INVENTORY_CONFIG={broken}\n')
        report['restart_broken'] = restart()
        time.sleep(int(__import__('os').environ.get('K8S_DOWN_SECONDS', '40')))  # past one inventory interval; 330 s crosses the 5-minute freshness window
        degraded = {}
        for p in paths:
            t0 = time.time()
            status, body = call('prod', 'GET', p)
            code = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
            degraded[p] = {'status': status, 'code': code, 'seconds': round(time.time() - t0, 1),
                           'unavailable': body.get('unavailable') if isinstance(body, dict) else None}
        report['degraded'] = degraded
        shots = subprocess.run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'shot.spec.ts'], cwd=ROOT / 'web',
                               capture_output=True, text=True, timeout=600,
                               env=dict(__import__('os').environ, SHOT_ROUTES='/resources,/services/new', SHOT_TAG='outage-kubernetes', SHOT_THEME='dark'))
        report['shots_exit'] = shots.returncode
    finally:
        EXTRA.write_text(original)
        broken.unlink(missing_ok=True)
        report['restart_restored'] = restart()
    time.sleep(40)
    report['restored'] = {p: call('prod', 'GET', p)[0] for p in paths}
    report['ok'] = all(v['status'] != 500 and v['seconds'] < 30 for v in report['degraded'].values()) and \
        report['restored'] == report['baseline']


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('outage', choices=['model', 'database', 'restart-midstream', 'kubernetes'])
    a = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    report = {'outage': a.outage, 'started_at': time.strftime('%Y-%m-%d %H:%M:%S')}
    {'model': model, 'database': database, 'restart-midstream': restart_midstream, 'kubernetes': kubernetes}[a.outage](report)
    (OUT / f'{a.outage}.json').write_text(json.dumps(report, ensure_ascii=False, indent=1))
    print(json.dumps(report, ensure_ascii=False, indent=1)[:3000])


if __name__ == '__main__':
    main()
