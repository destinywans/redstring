#!/usr/bin/env python3
"""Normalize three seeded department display records in a local review database.

python3 scripts/normalize-settings-departments.py \
  --runtime .local/product-lab/runtime.json --container aiops-canvas-db-1 --dry-run
Omit --dry-run to apply. Existing edits, identities, memberships and grants remain.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import unquote, urlparse


REPLACEMENTS = [
    ('settings-demo-commerce', '交易平台团队 · Mock', '平台工程部',
     '负责交易平台应用建设与运维的示例团队。', '负责交易平台应用建设与运维。'),
    ('settings-demo-infrastructure', '基础设施团队 · Mock', '基础设施部',
     '负责集群、基础组件与平台基础设施的示例团队。', '负责集群、基础组件与平台基础设施。'),
    ('settings-demo-data', '数据团队 · Mock', '数据平台部',
     '负责数据平台与数据服务建设治理的示例团队。', '负责数据平台与数据服务建设治理。'),
]


def literal(value):
    return "'" + value.replace("'", "''") + "'"


def normalization_sql(dry_run):
    values = ',\n'.join('(' + ','.join(literal(v) for v in row) + ')'
                        for row in REPLACEMENTS)
    replacements = f'WITH replacements(id,old_name,new_name,old_description,new_description) AS (VALUES {values})'
    eligible = '''t.id=r.id AND EXISTS (
        SELECT 1 FROM settings_team_profiles p WHERE p.team_id=t.id AND p.is_mock
    ) AND (t.name=r.old_name OR t.description=r.old_description)'''
    if dry_run:
        rows = f'''SELECT t.id,r.new_name AS canonical_name FROM teams t,replacements r WHERE {eligible}'''
        result = replacements + f', affected AS ({rows})'
    else:
        result = replacements + f''', affected AS (
            UPDATE teams t SET
                name=CASE WHEN t.name=r.old_name THEN r.new_name ELSE t.name END,
                description=CASE WHEN t.description=r.old_description THEN r.new_description ELSE t.description END
            FROM replacements r WHERE {eligible}
            RETURNING t.id,r.new_name AS canonical_name
        )'''
    result += " SELECT json_build_object('count',count(*),'departments',COALESCE(json_agg(affected ORDER BY id),'[]'::json)) FROM affected;"
    # Take the seed script's advisory lock, without rerunning its writes. All
    # statements share one transaction; individual fields retain later edits.
    return '''BEGIN;
DO $$ BEGIN
    PERFORM pg_advisory_xact_lock(752018992);
    IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE name='012_settings.sql') THEN
        RAISE EXCEPTION 'Settings schema is not installed';
    END IF;
END $$;
''' + result + ('\nROLLBACK;' if dry_run else '\nCOMMIT;')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path)
    parser.add_argument('--container')
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args()
    config = json.loads(args.runtime.read_text()) if args.runtime else {}
    url = urlparse(config.get('DATABASE_URL') or os.environ.get('DATABASE_URL', ''))
    if (url.scheme not in ('postgres', 'postgresql')
            or url.hostname not in ('localhost', '127.0.0.1', '::1')
            or '_review_' not in url.path):
        raise SystemExit('Only an explicitly configured loopback *_review_* PostgreSQL database is allowed.')
    env = dict(os.environ)
    env.update(PGPASSWORD=unquote(url.password or ''), PGUSER=unquote(url.username or 'postgres'),
               PGDATABASE=url.path.lstrip('/'), PGHOST=url.hostname, PGPORT=str(url.port or 5432))
    command = (['docker', 'exec', '-i', '-e', 'PGPASSWORD', args.container, 'psql',
                '-U', env['PGUSER'], '-d', env['PGDATABASE']] if args.container else ['psql'])
    command += ['-X', '-v', 'ON_ERROR_STOP=1', '-qAt']
    result = subprocess.run(command, input=normalization_sql(args.dry_run), text=True,
                            env=env, capture_output=True)
    if result.returncode:
        # Do not echo connection errors or SQL details carrying private values.
        raise SystemExit('Department normalization did not finish successfully; check database state before retrying.')
    report = json.loads(result.stdout.strip())
    report['mode'] = 'dry_run' if args.dry_run else 'applied'
    print(json.dumps(report, ensure_ascii=False))


if __name__ == '__main__':
    main()
