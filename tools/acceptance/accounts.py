#!/usr/bin/env python3
"""Create the per-role acceptance accounts through the product's own flow.

  python3 tools/acceptance/accounts.py prod

For each account: the admin sends an invitation (POST /members/invitations), the
code is read from the local SMTP sink (.local/acceptance/mail/inbox), the invitee
accepts it (POST /auth/invitations/accept), and the admin assigns the business role
(PUT /members/{id}/access). Credentials are written to .local/acceptance/<inst>/secrets.json
under "accounts"; nothing secret is printed.
"""
import email
import glob
import json
import os
import re
import secrets
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from api import LOCAL, call  # noqa: E402

ACCOUNTS = {'operator': 'operator', 'approver1': 'change-approver', 'approver2': 'change-approver'}


def domain(instance):
    return f'{instance}.redstring.test'


def invitation_code(address, since):
    for _ in range(30):
        for f in sorted(glob.glob(str(LOCAL / 'mail/inbox/*.eml')), reverse=True):
            if os.path.getmtime(f) < since:
                continue
            m = email.message_from_string(Path(f).read_text())
            if m['To'] == address:
                found = re.search(r'invitation code: (\S+)', m.get_payload())
                if found:
                    return found.group(1)
        time.sleep(1)
    raise SystemExit(f'no invitation mail for {address}')


def member_id(instance, address):
    status, body = call(instance, 'GET', f'/members?q={address}')
    if status != 200:
        raise SystemExit(f'members lookup failed: {status} {body}')
    for item in body.get('items', []):
        if item.get('email') == address:
            return item['id']
    raise SystemExit(f'member {address} not found')


def main():
    instance = sys.argv[1] if len(sys.argv) > 1 else 'prod'
    path = LOCAL / instance / 'secrets.json'
    store = json.loads(path.read_text())
    accounts = store.setdefault('accounts', {})
    for name, role in ACCOUNTS.items():
        address = f'{name}@{domain(instance)}'
        if name not in accounts:
            since = time.time() - 1
            status, body = call(instance, 'POST', '/members/invitations', {'email': address, 'role': 'viewer'})
            if status == 409:
                since = 0  # already invited: use the code from the mail already in the sink
            elif status != 200:
                raise SystemExit(f'invite {name}: {status} {body}')
            password = secrets.token_urlsafe(18)
            status, body = call(instance, 'POST', '/auth/invitations/accept',
                                {'code': invitation_code(address, since), 'name': name.capitalize(), 'password': password}, env='', cookie='')
            if status not in (200, 201):
                raise SystemExit(f'accept {name}: {status} {body}')
            accounts[name] = {'email': address, 'password': password, 'role': role}
            path.write_text(json.dumps(store, indent=2) + '\n')
            os.chmod(path, 0o600)
        mid = member_id(instance, address)
        status, access = call(instance, 'GET', f'/members/{mid}/access')
        if status != 200:
            raise SystemExit(f'access {name}: {status} {access}')
        # Legacy-origin assignments (the invitation's viewer) are not part of the V3-managed set.
        current = {r['id']: str(r['matrix_revision']) for r in access['roles'] if r.get('origin') != 'legacy'}
        if role not in current:
            headers = {}
            status, target = call(instance, 'GET', f'/roles/{role}', headers_out=headers)
            if status != 200 or not headers.get('X-Role-Matrix-Revision'):
                raise SystemExit(f'role {role}: {status} {target}')
            current[role] = headers['X-Role-Matrix-Revision']
            body = {'expected_revision': access['revision'], 'role_ids': sorted(current),
                    'role_revisions': current, 'enabled': True}
            status, result = call(instance, 'PUT', f'/members/{mid}/access', body)
            if status != 200:
                raise SystemExit(f'assign {role} to {name}: {status} {result}')
        print(f'{instance}: {name} ready as {role}')


if __name__ == '__main__':
    main()
