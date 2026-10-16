#!/usr/bin/env python3
"""Check the lab credentials the acceptance instances read from Vault, and that they still work.

  python3 tools/acceptance/credentials.py [--warn-hours 48] [--json PATH]

Reads each Vault entry with an instance's product token and prints only its expiry (never a value):
the Kubernetes reader and rollback tokens and the two GitLab tokens. Then reads checkout-api's
rollback candidates on both instances, which fail as credential_unavailable once the reader expires.
Exits 1 when a credential has expired or the candidates are blocked by credentials, and prints a
warning for any that expires within --warn-hours. The 2026-10-11 reader token expired unnoticed for
about five hours; renew it with kubectl create token (lab kubeconfig) and aiops-test's vault.py init.

Also checks (WAN-434) the GitLab administrator token (aiops-test/.local/delivery/gitlab-admin-token, the
lab GitLab on 127.0.0.1:28080) and the two config-repository tokens named by .local/acceptance/config-sync/
<instance>.json, by asking GitLab for `GET /personal_access_tokens/self` (expiry and active flag only).
Override the admin token file and GitLab address with ACCEPTANCE_GITLAB_ADMIN_TOKEN_FILE and
ACCEPTANCE_GITLAB_URL. Token values are never printed. The result, including `expiring` (entries that
expire within --warn-hours), is written to --json (default .local/acceptance/e2e/credentials.json) for
round.py.
"""
import argparse
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ENTRIES = ['lab/k8s/reader', 'lab/k8s/rollback', 'lab/gitlab/read', 'lab/gitlab/prometheus-rules']
ENV = {'prod': 'prod', 'nonprod': 'test'}
GITLAB_URL = os.environ.get('ACCEPTANCE_GITLAB_URL', 'http://127.0.0.1:28080')
ADMIN_TOKEN_FILE = Path(os.environ.get('ACCEPTANCE_GITLAB_ADMIN_TOKEN_FILE',
                                       Path.home() / 'Home/code/codex/aiops-test/.local/delivery/gitlab-admin-token'))


def expiry_time(expires):
    if len(expires) == 10:  # a date only (GitLab tokens): valid through the end of that day, UTC
        return datetime.fromisoformat(expires + 'T23:59:59+00:00')
    return datetime.fromisoformat(expires.replace('Z', '+00:00'))


def gitlab_token_info(base_url, token_file, ca_file=None):
    """(expires_at or None, detail) for a GitLab token read from a file; the value is never returned or printed."""
    try:
        token = Path(token_file).read_text().strip()
    except OSError as e:
        raise RuntimeError(f'token file unreadable ({type(e).__name__})') from None
    ctx = ssl.create_default_context(cafile=ca_file) if ca_file and base_url.startswith('https') else None
    req = urllib.request.Request(f'{base_url}/api/v4/personal_access_tokens/self', headers={'PRIVATE-TOKEN': token})
    try:
        info = json.load(urllib.request.urlopen(req, timeout=8, context=ctx))
    except urllib.error.HTTPError as e:
        raise RuntimeError(f'GitLab answered {e.code}') from None
    except Exception as e:  # noqa: BLE001
        raise RuntimeError(f'GitLab unreachable ({type(e).__name__})') from None
    if info.get('revoked') or info.get('active') is False:
        raise RuntimeError('token revoked or inactive')
    return info.get('expires_at'), f"scopes {','.join(info.get('scopes', []))}"


def setting(name):
    for line in (LOCAL / 'prod/extra.env').read_text().splitlines():
        if line.startswith(name + '='):
            return line.split('=', 1)[1].strip()
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--warn-hours', type=float, default=48)
    ap.add_argument('--json', default=str(LOCAL / 'e2e/credentials.json'))
    a = ap.parse_args()
    token, addr = setting('AIOPS_VAULT_TOKEN'), setting('AIOPS_VAULT_ADDR') or 'http://127.0.0.1:18200'
    now = time.time()
    results = []

    def record(name, status, expires=None, detail=''):
        """status: pass / warn / fail; warn and pass both count as passed, as before."""
        row = {'name': name, 'status': status, 'expires_at': expires, 'detail': detail}
        if expires:
            row['hours_left'] = round((expiry_time(expires).timestamp() - now) / 3600, 1)
        results.append(row)
        label = {'pass': 'PASS', 'warn': 'WARN', 'fail': 'FAIL'}[status]
        if expires and status != 'fail':
            text = f"expires {expires} ({row['hours_left']:.0f} h left)" + (f'; {detail}' if detail else '')
        else:
            text = detail or 'no expiry recorded'
        print(f'{label}  {name:<34} {text}')

    def expiry(name, expires, detail=''):
        left = (expiry_time(expires).timestamp() - now) / 3600
        if left <= 0:
            record(name, 'fail', expires, f'expired at {expires}')
        else:
            record(name, 'warn' if left < a.warn_hours else 'pass', expires, detail)

    for entry in ENTRIES:
        req = urllib.request.Request(f'{addr}/v1/aiops/data/{entry}', headers={'X-Vault-Token': token})
        try:
            data = json.load(urllib.request.urlopen(req, timeout=5))['data']['data']
        except Exception as e:  # noqa: BLE001
            record(f'vault {entry}', 'fail', detail=f'unreadable ({type(e).__name__})')
            continue
        if data.get('expires_at'):
            expiry(f'vault {entry}', data['expires_at'])
        else:
            record(f'vault {entry}', 'pass')
    # GitLab: the administrator token the lab uses to sign project tokens, and the two config-repository
    # tokens the config-sync executors use (prod and nonprod).
    checks = [('gitlab admin token', GITLAB_URL, ADMIN_TOKEN_FILE, None)]
    for instance in ENV:
        try:
            cfg = json.loads((LOCAL / 'config-sync' / f'{instance}.json').read_text())['gitlab']
            checks.append((f'gitlab config token {instance}', cfg['base_url'], cfg['token_file'], cfg.get('ca_file')))
        except (OSError, KeyError, ValueError) as e:
            record(f'gitlab config token {instance}', 'fail', detail=f'config-sync/{instance}.json unusable ({type(e).__name__})')
    for name, base_url, token_file, ca_file in checks:
        try:
            expires, detail = gitlab_token_info(base_url, token_file, ca_file)
        except RuntimeError as e:
            record(name, 'fail', detail=str(e))
            continue
        if expires:
            expiry(name, expires, detail)
        else:
            record(name, 'pass', detail=f'no expiry; {detail}')
    services = json.loads((LOCAL / 'rollback-services.json').read_text())
    for instance, env in ENV.items():
        status, body = call(instance, 'GET', f"/services/{services[instance]['checkout-api']}/rollback-candidates", env=env)
        blocked = (body or {}).get('blocked_reason') if status == 200 else status
        ok = status == 200 and blocked not in ('credential_unavailable', 'workload_unavailable')
        record(f'{instance} rollback candidates', 'pass' if ok else 'fail', detail=f'{status} blocked={blocked}')
    failed = sum(r['status'] == 'fail' for r in results)
    total = len(results)
    expiring = [{k: r[k] for k in ('name', 'expires_at', 'hours_left')} for r in results if r.get('hours_left') is not None and r['hours_left'] < a.warn_hours]
    print(f'{total - failed}/{total} passed')
    out = Path(a.json)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({'at': datetime.now(timezone.utc).isoformat(timespec='seconds'), 'warn_hours': a.warn_hours,
                               'passed': total - failed, 'total': total, 'failed': failed, 'expiring': expiring,
                               'checks': results}, ensure_ascii=False, indent=1))
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
