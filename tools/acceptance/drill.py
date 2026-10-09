#!/usr/bin/env python3
"""Run one aiops-test fault drill against the prod acceptance instance and time each stage.

  python3 tools/acceptance/drill.py retry-storm --alerts LabHigh5xxRate --apps checkout-api order-api \
      --question "…" [--fire-timeout 600] [--recover-timeout 900]

Stages: drill on (aiops-test scripts/lab-faults.py) → alert pending/firing in Prometheus →
active in Alertmanager → firing alert group in the product → AI investigation through the
product API (operator account, real model and read-only tools) → drill off → alert gone from
Prometheus → product group resolved. Writes .local/acceptance/e2e/drills/<drill>.json.
Every knob is turned off even when a stage fails. --lab-silence silences the drill's alerts directly
in the lab Alertmanager once they reach the product (a silence suppresses the resolved webhook), then
checks that the product reconciles the group to silenced and later resolved on its own (PA-47);
the silence is expired at the end.
"""
import argparse
import base64
import json
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

LAB = Path.home() / 'Home/code/codex/aiops-test'
PROM, AM = 'http://127.0.0.1:19090', 'http://127.0.0.1:19093'
OUT = LOCAL / 'e2e/drills'


def get(url):
    with urllib.request.urlopen(url, timeout=15) as r:
        return json.load(r)


def lab(*args):
    r = subprocess.run(['python3', 'scripts/lab-faults.py', *args], cwd=LAB, capture_output=True, text=True, timeout=600)
    return (r.stdout + r.stderr).strip().splitlines()[-3:]


def app_ids():
    """Service name -> application id (alert groups carry the application id; the catalog id encodes app\0env)."""
    _, catalog = call('prod', 'GET', '/services')
    out = {}
    for i in catalog.get('items', []):
        raw = base64.urlsafe_b64decode(i['id'] + '=' * (-len(i['id']) % 4)).decode()
        out[i['name']] = raw.split('\x00')[0]
    return out


def prom_alerts(names, apps):
    out = {}
    for a in get(f'{PROM}/api/v1/alerts')['data']['alerts']:
        if a['labels'].get('alertname') in names and (not apps or a['labels'].get('application') in apps):
            out[(a['labels']['alertname'], a['labels'].get('application'))] = a['state']
    return out


def am_alerts(names, apps):
    return {(a['labels']['alertname'], a['labels'].get('application')): a['status']['state']
            for a in get(f'{AM}/api/v2/alerts')
            if a['labels'].get('alertname') in names and (not apps or a['labels'].get('application') in apps)}


def product_groups(app_id_set, badges=False):
    _, groups = call('prod', 'GET', '/alert-groups')
    items = [i for i in (groups.get('items') or []) if i.get('service') in app_id_set]
    if badges:
        return {i['id']: [b.get('text') if isinstance(b, dict) else b for b in (i.get('badges') or [])] for i in items}
    return {i['id']: i['state'] for i in items}


def am_silence(names, minutes=30):
    body = json.dumps({'matchers': [{'name': 'alertname', 'value': '|'.join(names), 'isRegex': True, 'isEqual': True},
                                    {'name': 'redstring_env', 'value': 'prod', 'isRegex': False, 'isEqual': True}],
                       'startsAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                       'endsAt': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime(time.time() + minutes * 60)),
                       'createdBy': 'redstring-acceptance', 'comment': 'acceptance drill: PA-47 reconcile check'}).encode()
    req = urllib.request.Request(f'{AM}/api/v2/silences', body, {'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(req, timeout=15) as r:
        return json.load(r)['silenceID']


def am_expire(silence_id):
    req = urllib.request.Request(f'{AM}/api/v2/silence/{silence_id}', method='DELETE')
    with urllib.request.urlopen(req, timeout=15) as r:
        return r.status


def ask(question, timeout=300):
    status, conv = call('prod', 'POST', '/conversations', {'text': question}, role='operator')
    if status >= 300:
        return {'error': f'create {status} {conv}'}
    cid = conv['id']
    status, posted = call('prod', 'POST', f'/conversations/{cid}/messages', {'text': question}, role='operator')
    if status >= 300:
        return {'conversation': cid, 'error': f'message {status} {posted}'}
    started = time.time()
    last = {}
    while time.time() - started < timeout:
        _, detail = call('prod', 'GET', f'/conversations/{cid}', role='operator')
        msgs = detail.get('messages') or []
        last = msgs[-1] if msgs else {}
        if last.get('role') == 'assistant' and last.get('state') in ('completed', 'failed'):
            break
        time.sleep(5)
    text = ''.join(p.get('markdown', '') for p in last.get('parts', []) if p.get('type') == 'text')
    tools = [p.get('text') for p in last.get('parts', []) if p.get('type') == 'status' and p.get('text') != 'Model reply failed']
    return {'conversation': cid, 'state': last.get('state'), 'seconds': round(time.time() - started), 'tools': tools, 'answer': text}


def wait(label, check, timeout, log):
    started = time.time()
    while time.time() - started < timeout:
        value = check()
        if value:
            log[label] = round(time.time() - log['t0'])
            print(f"  +{log[label]:>4}s {label}", flush=True)
            return value
        time.sleep(10)
    log[label] = None
    print(f"  timeout {label}", flush=True)
    return None


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('drill')
    ap.add_argument('--alerts', nargs='+', required=True)
    ap.add_argument('--apps', nargs='*', default=[])
    ap.add_argument('--question', required=True)
    ap.add_argument('--extra', nargs='*', default=[], help='extra lab-faults.py arguments, e.g. --app order-api')
    ap.add_argument('--fire-timeout', type=int, default=600)
    ap.add_argument('--recover-timeout', type=int, default=900)
    ap.add_argument('--lab-silence', action='store_true', help='silence the alerts in the lab Alertmanager (PA-47 check)')
    a = ap.parse_args()
    OUT.mkdir(parents=True, exist_ok=True)
    ids = app_ids()
    watched = {ids[n] for n in a.apps if n in ids} or set(ids.values())
    before = product_groups(watched)
    log = {'drill': a.drill, 'alerts': a.alerts, 'apps': a.apps, 't0': time.time(), 'started_at': time.strftime('%Y-%m-%d %H:%M:%S')}
    print(f"{log['started_at']} drill {a.drill} on: {lab('drill', a.drill, 'on', *a.extra)}", flush=True)
    try:
        wait('prometheus_firing', lambda: {k: v for k, v in prom_alerts(a.alerts, a.apps).items() if v == 'firing'}, a.fire_timeout, log)
        log['alertmanager'] = [list(k) for k in (wait('alertmanager_active', lambda: am_alerts(a.alerts, a.apps), 120, log) or {})]
        firing = wait('product_firing', lambda: {g: s for g, s in product_groups(watched).items() if s == 'firing' and before.get(g) != 'firing'} or
                      {g: s for g, s in product_groups(watched).items() if s == 'firing'}, 180, log)
        log['product_groups'] = sorted(firing or {})
        if a.lab_silence:
            log['lab_silence'] = am_silence(a.alerts)
            print(f"  lab silence {log['lab_silence']}", flush=True)
            badge = wait('product_silenced_badge', lambda: {g: b for g, b in product_groups(watched, badges=True).items()
                                                           if g in log['product_groups'] and any('静默' in str(x) for x in b)}, 300, log)
            log['silenced_badges'] = badge
        print('  asking the AI…', flush=True)
        log['ai'] = ask(a.question)
        log['ai_done'] = round(time.time() - log['t0'])
        print(f"  +{log['ai_done']:>4}s ai {log['ai'].get('state')} ({len(log['ai'].get('tools', []))} evidence)", flush=True)
    finally:
        log['off_at'] = round(time.time() - log['t0'])
        print(f"  +{log['off_at']:>4}s drill off: {lab('drill', a.drill, 'off', *a.extra)}", flush=True)
    wait('prometheus_cleared', lambda: not prom_alerts(a.alerts, a.apps) or None, a.recover_timeout, log)
    wait('product_resolved', lambda: (all(s != 'firing' for g, s in product_groups(watched).items() if g in set(log.get('product_groups') or [])) or None), 300, log)
    if log.get('lab_silence'):
        log['lab_silence_expired'] = am_expire(log['lab_silence'])
    log['lab_status'] = lab('status')
    (OUT / f'{a.drill}.json').write_text(json.dumps(log, ensure_ascii=False, indent=2))
    print(f"report: {OUT / (a.drill + '.json')}")


if __name__ == '__main__':
    main()
