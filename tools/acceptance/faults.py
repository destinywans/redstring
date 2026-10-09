#!/usr/bin/env python3
"""Stop one lab data source, check the product degrades clearly, start it again
and check the product recovers on its own (no product restart).

  python3 tools/acceptance/faults.py loki|tempo|prometheus|elasticsearch|alertmanager [--no-crawl]

Per fault: baseline probes → `docker stop` the lab container → probes must answer
an explicit state (200 with `unavailable`/null fields, or 502/503/501 with an error
code; never 500 or a hang) → prod zh-CN dark crawl into .local/acceptance/e2e/fault-<name>
(uncaught exceptions and blank pages fail) and screenshots tagged fault-<name> →
`docker start` → probes must return to their baseline status within 3 minutes.
Writes .local/acceptance/e2e/faults/<name>.json. The container is always started
again, even when a stage fails. Lab containers only; nothing else is touched.
"""
import argparse
import json
import os
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
FAULTS = {
    'loki': {'container': 'aiops-test-loki-1', 'ready': 'http://127.0.0.1:13100/ready',
             'probes': ['/logs', '/services/{svc}'], 'pages': ['/logs', '/services/{svc}']},
    'tempo': {'container': 'aiops-enterprise-tempo-1', 'ready': 'http://127.0.0.1:13200/ready',
              'probes': ['/traces?scope=app'], 'pages': ['/traces']},
    'prometheus': {'container': 'aiops-test-prometheus-1', 'ready': 'http://127.0.0.1:19090/-/ready',
                   'probes': ['/services', '/services/{svc}', '/metrics/dashboard', '/home'], 'pages': ['/', '/services', '/metrics']},
    'elasticsearch': {'container': 'aiops-enterprise-elasticsearch-1', 'ready': None,
                      'probes': ['/logs', '/knowledge-sources'], 'pages': ['/logs', '/knowledge-sources']},
    'alertmanager': {'container': 'aiops-test-alertmanager-1', 'ready': 'http://127.0.0.1:19093/-/ready',
                     'probes': ['/silences', '/inhibitions', '/notification-routes', '/alert-groups'], 'pages': ['/silences', '/routing', '/alerts']},
}
OUT = LOCAL / 'e2e/faults'


def probe(paths):
    out = {}
    for path in paths:
        started = time.time()
        status, body = call('prod', 'GET', path)
        error = body.get('error', {}).get('code') if isinstance(body, dict) and isinstance(body.get('error'), dict) else None
        unavailable = body.get('unavailable') if isinstance(body, dict) else None
        out[path] = {'status': status, 'code': error, 'seconds': round(time.time() - started, 1),
                     'unavailable': unavailable if isinstance(unavailable, (dict, list)) and unavailable else None}
    return out


def ready(url, timeout=120):
    started = time.time()
    while time.time() - started < timeout:
        try:
            with urllib.request.urlopen(url, timeout=5) as r:
                if r.status == 200:
                    return round(time.time() - started)
        except Exception:
            pass
        time.sleep(3)
    return None


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument('fault', choices=FAULTS)
    ap.add_argument('--no-crawl', action='store_true')
    a = ap.parse_args()
    spec = FAULTS[a.fault]
    OUT.mkdir(parents=True, exist_ok=True)
    _, services = call('prod', 'GET', '/services')
    svc = next(i['id'] for i in services['items'] if i['name'] == 'checkout-api')
    paths = [p.replace('{svc}', svc) for p in spec['probes']]
    pages = [p.replace('{svc}', svc) for p in spec['pages']]
    report = {'fault': a.fault, 'container': spec['container'], 'started_at': time.strftime('%Y-%m-%d %H:%M:%S')}
    report['baseline'] = probe(paths)
    print(f"baseline: { {p: v['status'] for p, v in report['baseline'].items()} }", flush=True)
    stopped = time.time()
    try:
        r = run(['docker', 'stop', spec['container']])
        print(f"stopped {spec['container']}: {r.returncode}", flush=True)
        time.sleep(5)
        report['degraded'] = probe(paths)
        bad = {p: v for p, v in report['degraded'].items() if v['status'] == 500 or v['seconds'] > 30}
        report['degraded_ok'] = not bad
        print(f"degraded: { {p: (v['status'], v['code'], v['seconds']) for p, v in report['degraded'].items()} }", flush=True)
        if not a.no_crawl:
            env = dict(os.environ, E2E_WORKERS='6', CRAWL_OUT=f'e2e/fault-{a.fault}')
            crawl = run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'crawl.spec.ts', '--grep', '(?<!non)prod · zh-CN · dark'],
                        cwd=ROOT / 'web', env=env, timeout=900)
            report['crawl_exit'] = crawl.returncode
            summary = run(['python3', 'tools/acceptance/crawl-summary.py'], cwd=ROOT, env=env)
            report['crawl_summary'] = summary.stdout.strip().splitlines()[-1:] if summary.stdout else summary.stderr[-300:]
            print(f"crawl: {report['crawl_summary']}", flush=True)
            shots = run(['npx', 'playwright', 'test', '-c', 'e2e/playwright.config.ts', 'shot.spec.ts'], cwd=ROOT / 'web', timeout=600,
                        env=dict(os.environ, SHOT_ROUTES=','.join(pages), SHOT_TAG=f'fault-{a.fault}', SHOT_THEME='dark'))
            report['shots_exit'] = shots.returncode
    finally:
        r = run(['docker', 'start', spec['container']])
        report['restarted'] = r.returncode == 0
        report['down_seconds'] = round(time.time() - stopped)
        print(f"started {spec['container']}: {r.returncode}", flush=True)
    if spec['ready']:
        report['source_ready_seconds'] = ready(spec['ready'])
    started = time.time()
    while time.time() - started < 180:
        now = probe(paths)
        if all(now[p]['status'] == report['baseline'][p]['status'] for p in paths):
            break
        time.sleep(10)
    report['recovered'] = now
    report['recovered_ok'] = all(now[p]['status'] == report['baseline'][p]['status'] for p in paths)
    report['recovery_seconds'] = round(time.time() - started)
    print(f"recovered: {report['recovered_ok']} after {report['recovery_seconds']} s "
          f"{ {p: v['status'] for p, v in now.items()} }", flush=True)
    (OUT / f'{a.fault}.json').write_text(json.dumps(report, ensure_ascii=False, indent=1))


if __name__ == '__main__':
    main()
