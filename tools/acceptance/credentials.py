#!/usr/bin/env python3
"""Check the lab credentials the acceptance instances read from Vault, and that they still work.

  python3 tools/acceptance/credentials.py [--warn-hours 48]

Reads each Vault entry with an instance's product token and prints only its expiry (never a value):
the Kubernetes reader and rollback tokens and the two GitLab tokens. Then reads checkout-api's
rollback candidates on both instances, which fail as credential_unavailable once the reader expires.
Exits 1 when a credential has expired or the candidates are blocked by credentials, and prints a
warning for any that expires within --warn-hours. The 2026-10-11 reader token expired unnoticed for
about five hours; renew it with kubectl create token (lab kubeconfig) and aiops-test's vault.py init.
"""
import argparse
import json
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENTRIES = ['lab/k8s/reader', 'lab/k8s/rollback', 'lab/gitlab/read', 'lab/gitlab/prometheus-rules']
ENV = {'prod': 'prod', 'nonprod': 'test'}


def setting(name):
    for line in (LOCAL / 'prod/extra.env').read_text().splitlines():
        if line.startswith(name + '='):
            return line.split('=', 1)[1].strip()
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--warn-hours', type=float, default=48)
    a = ap.parse_args()
    token, addr = setting('AIOPS_VAULT_TOKEN'), setting('AIOPS_VAULT_ADDR') or 'http://127.0.0.1:18200'
    now, failed, total = time.time(), 0, 0
    for entry in ENTRIES:
        total += 1
        req = urllib.request.Request(f'{addr}/v1/aiops/data/{entry}', headers={'X-Vault-Token': token})
        try:
            data = json.load(urllib.request.urlopen(req, timeout=5))['data']['data']
        except Exception as e:  # noqa: BLE001
            failed += 1
            print(f'FAIL  {entry:<28} unreadable ({type(e).__name__})')
            continue
        expires = data.get('expires_at')
        if not expires:
            print(f'PASS  {entry:<28} no expiry recorded')
            continue
        if len(expires) == 10:  # a date only (GitLab tokens): valid through the end of that day, UTC
            expires_at = datetime.fromisoformat(expires + 'T23:59:59+00:00')
        else:
            expires_at = datetime.fromisoformat(expires.replace('Z', '+00:00'))
        left = (expires_at.timestamp() - now) / 3600
        if left <= 0:
            failed += 1
            print(f'FAIL  {entry:<28} expired at {expires}')
        else:
            print(f"{'WARN' if left < a.warn_hours else 'PASS'}  {entry:<28} expires {expires} ({left:.0f} h left)")
    services = json.loads((LOCAL / 'rollback-services.json').read_text())
    for instance, env in ENV.items():
        total += 1
        status, body = call(instance, 'GET', f"/services/{services[instance]['checkout-api']}/rollback-candidates", env=env)
        blocked = (body or {}).get('blocked_reason') if status == 200 else status
        ok = status == 200 and blocked not in ('credential_unavailable', 'workload_unavailable')
        failed += not ok
        print(f"{'PASS' if ok else 'FAIL'}  {instance:<8} rollback candidates  {status} blocked={blocked}")
    print(f'{total - failed}/{total} passed')
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
