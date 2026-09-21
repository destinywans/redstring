#!/usr/bin/env python3
"""Seed clearly marked disabled Mock identities and teams into a local review DB.

python3 scripts/seed-settings-demo.py --runtime .local/product-lab/runtime.json \
  --container aiops-canvas-db-1
No passwords, real identities, application ownership, or existing grants change.
Use --dry-run to inspect intended counts without writing.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlparse, unquote

SOURCE = 'settings_demo'
MEMBERS = [
 ('lin', '林澈', '平台工程部', '平台负责人', 'admin'),
 ('su', '苏禾', '基础设施部', 'SRE 工程师', 'operator'),
 ('zhou', '周远', '数据平台部', '数据工程师', 'operator'),
 ('chen', '陈曦', '平台工程部', '后端工程师', 'operator'),
 ('wang', '王宁', '平台工程部', '质量工程师', 'viewer'),
 ('liu', '刘悦', '基础设施部', '运维工程师', 'operator'),
 ('zhao', '赵然', '数据平台部', '数据分析师', 'viewer'),
 ('sun', '孙琪', '平台工程部', '产品经理', 'viewer'),
]
TEAMS = [
 ('commerce', '平台工程部', '平台工程部', 'lin', ['lin', 'chen', 'wang', 'sun'], '负责交易平台应用建设与运维。'),
 ('infrastructure', '基础设施部', '基础设施部', 'su', ['su', 'liu', 'lin'], '负责集群、基础组件与平台基础设施。'),
 ('data', '数据平台部', '数据平台部', 'zhou', ['zhou', 'zhao', 'chen'], '负责数据平台与数据服务建设治理。'),
]

def literal(value):
    return "'" + str(value).replace("'", "''") + "'"

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path)
    parser.add_argument('--container')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    config = json.loads(args.runtime.read_text()) if args.runtime else {}
    url = urlparse(config.get('DATABASE_URL') or os.environ.get('DATABASE_URL', ''))
    if url.scheme not in ('postgres', 'postgresql') or url.hostname not in ('localhost', '127.0.0.1', '::1') or '_review_' not in url.path:
        raise SystemExit('Only an explicitly configured loopback *_review_* PostgreSQL database is allowed.')
    env = dict(os.environ)
    env.update(PGPASSWORD=unquote(url.password or ''), PGUSER=unquote(url.username or 'postgres'),
               PGDATABASE=url.path.lstrip('/'), PGHOST=url.hostname, PGPORT=str(url.port or 5432))
    command = (['docker', 'exec', '-i', '-e', 'PGPASSWORD', args.container, 'psql', '-U', env['PGUSER'], '-d', env['PGDATABASE']]
               if args.container else ['psql'])
    command += ['-X', '-v', 'ON_ERROR_STOP=1', '-qAt']
    def sql(statement):
        result = subprocess.run(command, input=statement, text=True, env=env, capture_output=True)
        if result.returncode:
            raise SystemExit(result.stderr.strip())
        return result.stdout.strip()
    migration = sql("SELECT count(*) FROM schema_migrations WHERE name='012_settings.sql';")
    if migration != '1':
        raise SystemExit('Apply migration 012_settings.sql through Store.Migrate before seeding.')
    apps = json.loads(sql("SELECT COALESCE(json_agg(id ORDER BY key),'[]') FROM applications WHERE NOT archived;"))
    if args.dry_run:
        print(json.dumps({'mock_members': len(MEMBERS), 'mock_teams': len(TEAMS), 'existing_applications': len(apps), 'mode': 'dry_run'}))
        return
    sql(build_seed_sql(apps))
    print('Seeded 8 disabled Mock identities and 3 Mock teams. Existing real accounts, passwords, ownership, and grants were preserved.')

def build_seed_sql(apps):
    """Return one transactional seed plan; exposed for isolated PG tests."""
    statements = ['BEGIN;', 'SELECT pg_advisory_xact_lock(752018992);']
    # Reject namespace collisions instead of changing a real record.
    statements.append("""DO $$ BEGIN
      IF EXISTS(SELECT 1 FROM users WHERE (id LIKE 'settings-demo-%' OR email LIKE '%@settings-demo.invalid') AND source<>'settings_demo')
      OR EXISTS(SELECT 1 FROM teams t LEFT JOIN settings_team_profiles p ON p.team_id=t.id WHERE t.id LIKE 'settings-demo-%' AND NOT COALESCE(p.is_mock,false))
      THEN RAISE EXCEPTION 'Settings demo namespace collision; no changes applied'; END IF;
    END $$;""")
    for key, name, department, job, role in MEMBERS:
        values = ['settings-demo-' + key, key + '@settings-demo.invalid', name, role, department, job, key]
        statements.append("INSERT INTO users(id,email,name,role,password_hash,disabled,department,job_title,source,external_id) VALUES(" + ','.join(literal(v) for v in values[:4]) + ",'',true," + ','.join(literal(v) for v in values[4:6]) + ",'settings_demo'," + literal(values[6]) + ") ON CONFLICT(id) DO NOTHING;")
    for key, name, department, owner, members, description in TEAMS:
        tid = 'settings-demo-' + key
        statements.append('INSERT INTO teams(id,name,description) VALUES(' + ','.join(literal(v) for v in [tid, name, description]) + ') ON CONFLICT(id) DO NOTHING;')
        statements.append('INSERT INTO settings_team_profiles(team_id,owner_id,department,is_mock) VALUES(' + ','.join(literal(v) for v in [tid, 'settings-demo-' + owner, department]) + ',true) ON CONFLICT(team_id) DO NOTHING;')
        for member in members:
            statements.append('INSERT INTO team_members(team_id,user_id) SELECT ' + literal(tid) + ',' + literal('settings-demo-' + member) + ' WHERE NOT EXISTS(SELECT 1 FROM management_audit WHERE target=' + literal(tid) + " AND action='team.members.updated') ON CONFLICT DO NOTHING;")
        # Seed grants only during the first seed transaction, and only if the
        # team contains no real member. A rerun can never expand authorization.
        for app in apps[:3]:
            statements.append('INSERT INTO application_grants(application_id,subject_type,subject_id,permission) SELECT ' + literal(app) + ",'team'," + literal(tid) + ",'view' WHERE NOT EXISTS(SELECT 1 FROM management_audit WHERE action='settings.demo.seeded' AND target='settings_demo') AND NOT EXISTS(SELECT 1 FROM team_members m JOIN users u ON u.id=m.user_id WHERE m.team_id=" + literal(tid) + " AND u.source<>'settings_demo') AND NOT EXISTS(SELECT 1 FROM management_audit WHERE application_id=" + literal(app) + " AND action='application.grants.updated') ON CONFLICT DO NOTHING;")
    statements.append("INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes) SELECT 'settings-demo-seed','system','settings.demo.seeded','settings_demo','success','{\"source\":\"settings_demo\",\"login_enabled\":false}'::jsonb WHERE NOT EXISTS(SELECT 1 FROM management_audit WHERE action='settings.demo.seeded' AND target='settings_demo');")
    statements.append('COMMIT;')
    return '\n'.join(statements)

if __name__ == '__main__':
    main()
