#!/usr/bin/env python3
"""Explicitly replace RBAC data in the configured local review database.

python3 scripts/reset-review-rbac.py --container aiops-canvas-db-1 --dry-run
python3 scripts/reset-review-rbac.py --container aiops-canvas-db-1 --apply

Only roles, permissions and authorization associations are replaced. Accounts,
departments, applications and infrastructure inventory are retained. A private
backup is written before the transaction; changed authorization data aborts it.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent.parent
TABLES = (
    'rbac_roles', 'rbac_permissions', 'rbac_role_permissions',
    'rbac_user_roles', 'rbac_department_roles', 'rbac_permission_resources',
    'application_grants', 'cmdb_resource_relations',
)
SNAPSHOT = "jsonb_build_object(" + ','.join(
    "'%s',COALESCE((SELECT jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text) FROM %s t),'[]'::jsonb)" % (table, table)
    for table in TABLES
) + ')'


def reset_sql(fingerprint):
    if len(fingerprint) != 32 or any(c not in '0123456789abcdef' for c in fingerprint):
        raise ValueError('Invalid backup fingerprint')
    # Repair only the known review fixture's missing owner/object links. Runtime
    # rows are retained and inherit view from these explicit relationships.
    runtime_links = []
    for i in range(4, 10):
        runtime_links.append((29 + i - 4, 'pod-' + str(i), 'deployment-' + str(i % 4), 'belongs_to'))
    for i in range(5):
        runtime_links.append((35 + i, 'event-' + str(i), 'pod-' + str(i), 'involved_object'))
    values = ','.join("('mock-relation-%d','mock-resource-%s','mock-resource-%s','%s')" % link for link in runtime_links)
    repair = """
INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,source_id,description,is_mock)
 SELECT v.id,s.id,t.id,v.kind,'discovered','mock','ui-review-2026-09-21','界面审阅示例的运行对象归属；非真实采集关系。',true
 FROM (VALUES """ + values + """) v(id,source_id,target_id,kind)
 JOIN cmdb_resources s ON s.id=v.source_id JOIN cmdb_resources t ON t.id=v.target_id
 WHERE s.is_mock AND t.is_mock AND s.source_kind='mock' AND t.source_kind='mock'
 AND s.source_id='ui-review-2026-09-21' AND t.source_id='ui-review-2026-09-21'
 ON CONFLICT DO NOTHING;
"""
    return """BEGIN;
SET LOCAL lock_timeout='10s';
SET LOCAL statement_timeout='30s';
DO $$ BEGIN
 PERFORM pg_advisory_xact_lock(752018992);
 PERFORM pg_advisory_xact_lock(752018995);
 PERFORM pg_advisory_xact_lock(752018994);
END $$;
LOCK TABLE rbac_roles,rbac_permissions,rbac_role_permissions,rbac_user_roles,
 rbac_department_roles,rbac_permission_resources,application_grants,cmdb_resource_relations IN EXCLUSIVE MODE;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM schema_migrations WHERE name='018_rbac_infrastructure_permissions.sql') THEN
  RAISE EXCEPTION 'Migration 018 is required';
 END IF;
 IF md5((""" + SNAPSHOT + ")::text)<>'" + fingerprint + """' THEN
  RAISE EXCEPTION 'Authorization changed after backup; no reset applied';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM users WHERE role='admin' AND NOT disabled
   AND deleted_at IS NULL AND source<>'settings_demo' AND password_hash<>'') THEN
  RAISE EXCEPTION 'A login-capable administrator must exist';
 END IF;
END $$;
CREATE TEMP TABLE retained_counts ON COMMIT DROP AS
 SELECT (SELECT count(*) FROM users) users_count,(SELECT count(*) FROM teams) teams_count,
 (SELECT count(*) FROM applications) applications_count,(SELECT count(*) FROM cmdb_resources) resources_count;
DELETE FROM rbac_department_roles;
DELETE FROM rbac_user_roles;
DELETE FROM rbac_role_permissions;
DELETE FROM rbac_permission_resources;
DELETE FROM rbac_permissions;
DELETE FROM rbac_roles;
DELETE FROM application_grants;
""" + repair + """
INSERT INTO rbac_roles(id,name,description,builtin) VALUES
 ('admin','平台管理员','管理平台设置、成员和授权；拥有当前基础设施资源的查看、变更、审批和删除权限。',true),
 ('operator','运维人员','查看和变更已关联的基础设施资源。',true),
 ('viewer','只读成员','查看已关联的基础设施资源。',true),
 ('change-approver','变更审批员','查看基础设施资源并审批其变更，不包含变更或删除权限。',true);
INSERT INTO rbac_permissions(id,name,description,builtin) VALUES
 ('resource-view','资源查看','查看已关联的基础设施资源。',true),
 ('resource-update','资源变更','变更已关联的基础设施资源。',true),
 ('resource-approve','资源审批','审批已关联的基础设施资源的变更。',true),
 ('resource-delete','资源删除','删除已关联的基础设施资源。',true);
INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
 SELECT p.id,'resource',r.id,substring(p.id FROM 10)
 FROM rbac_permissions p CROSS JOIN cmdb_resources r WHERE rbac_is_infrastructure_kind(r.kind);
INSERT INTO rbac_role_permissions(role_id,permission_id)
 SELECT 'admin',id FROM rbac_permissions;
INSERT INTO rbac_role_permissions VALUES
 ('operator','resource-view'),('operator','resource-update'),('viewer','resource-view'),
 ('change-approver','resource-view'),('change-approver','resource-approve');
INSERT INTO rbac_user_roles(user_id,role_id,origin)
 SELECT u.id,u.role,'legacy' FROM users u JOIN rbac_roles r ON r.id=u.role
 WHERE u.deleted_at IS NULL AND u.source<>'settings_demo';
ALTER TABLE rbac_permission_resources VALIDATE CONSTRAINT rbac_infrastructure_binding;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM rbac_platform_administrators a JOIN users u ON u.id=a.user_id WHERE u.password_hash<>'') THEN
  RAISE EXCEPTION 'Reset would remove all login-capable administrators';
 END IF;
 IF EXISTS(SELECT 1 FROM retained_counts WHERE users_count<>(SELECT count(*) FROM users)
  OR teams_count<>(SELECT count(*) FROM teams) OR applications_count<>(SELECT count(*) FROM applications)
  OR resources_count<>(SELECT count(*) FROM cmdb_resources)) THEN
  RAISE EXCEPTION 'Retained inventory changed';
 END IF;
END $$;
INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes)
 VALUES('review-rbac-reset','system','rbac.defaults.reset','infrastructure','success',
 jsonb_build_object('roles',4,'permissions',4,'resource_count',(SELECT count(*) FROM cmdb_resources WHERE rbac_is_infrastructure_kind(kind))));
SELECT json_build_object('roles',(SELECT count(*) FROM rbac_roles),
 'permissions',(SELECT count(*) FROM rbac_permissions),
 'infrastructure_resources',(SELECT count(*) FROM cmdb_resources WHERE rbac_is_infrastructure_kind(kind)),
 'resource_operation_bindings',(SELECT count(*) FROM rbac_permission_resources),
 'user_role_bindings',(SELECT count(*) FROM rbac_user_roles),
 'department_role_bindings',(SELECT count(*) FROM rbac_department_roles),
 'retained',(SELECT row_to_json(retained_counts) FROM retained_counts));
COMMIT;
"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path, default=ROOT / '.local/product-lab/runtime.json')
    parser.add_argument('--container')
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--dry-run', action='store_true')
    mode.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    config = json.loads(args.runtime.read_text())
    url = urlsplit(config.get('DATABASE_URL', ''))
    if (url.scheme not in ('postgres', 'postgresql')
            or url.hostname not in ('localhost', '127.0.0.1', '::1')
            or not url.path.startswith('/aiops_') or '_review_' not in url.path
            or config.get('AIOPS_ADDR') != '127.0.0.1:18081'):
        raise SystemExit('Reset is restricted to the explicitly configured local review workspace.')
    env = dict(os.environ, PGPASSWORD=unquote(url.password or ''),
               PGUSER=unquote(url.username or 'postgres'), PGDATABASE=url.path[1:],
               PGHOST=url.hostname, PGPORT=str(url.port or 5432))
    if args.container:
        # Verify the named container actually publishes the configured database
        # port, rather than trusting any similarly named database inside it.
        mapping = subprocess.run(['docker', 'port', args.container, '5432/tcp'], capture_output=True, text=True)
        expected = str(url.port or 5432)
        if mapping.returncode or not any(line.rsplit(':', 1)[-1] == expected for line in mapping.stdout.splitlines()):
            raise SystemExit('Container does not publish the configured review database port.')
        command = ['docker', 'exec', '-i', '-e', 'PGPASSWORD', args.container,
                   'psql', '-U', env['PGUSER'], '-d', env['PGDATABASE']]
    else:
        command = ['psql']
    command += ['-X', '-qAt', '-v', 'ON_ERROR_STOP=1']

    def sql(statement):
        result = subprocess.run(command, input=statement, text=True, env=env, capture_output=True)
        if result.returncode:
            # Connection details and account data must not enter shared output.
            raise SystemExit('RBAC reset stopped. No successful reset is claimed; inspect private database state before retrying.')
        return result.stdout.strip()

    snapshot = json.loads(sql('SELECT jsonb_build_object(\'data\',data,\'fingerprint\',md5(data::text)) FROM (SELECT ' + SNAPSHOT + ' data) s;'))
    if args.dry_run:
        print(json.dumps({'mode': 'dry_run', 'replace_counts': {k: len(v) for k, v in snapshot['data'].items() if k != 'cmdb_resource_relations'},
                          'new_roles': 4, 'new_permissions': 4}, ensure_ascii=False))
        return
    backup_dir = ROOT / '.local/rbac-backups'
    backup_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ')
    backup = backup_dir / (stamp + '.json')
    fd = os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as out:
        json.dump(snapshot, out, ensure_ascii=False, indent=2)
        out.flush()
        os.fsync(out.fileno())
    report = json.loads(sql(reset_sql(snapshot['fingerprint'])))
    report.update(mode='applied', backup=str(backup.relative_to(ROOT)))
    print(json.dumps(report, ensure_ascii=False))


if __name__ == '__main__':
    main()
