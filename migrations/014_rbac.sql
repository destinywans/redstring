-- Existing teams are departments. Canonicalize their membership without losing existing application ACLs.
ALTER TABLE teams ADD COLUMN revision bigint NOT NULL DEFAULT 1;
CREATE VIEW rbac_departments AS SELECT id,name,revision FROM teams;
-- Preserve exact old effective ACL before enforcing a single department.
CREATE TEMP TABLE rbac_retained_department ON COMMIT DROP AS
 SELECT DISTINCT ON (m.user_id) m.user_id,m.team_id FROM team_members m JOIN users u ON u.id=m.user_id
 JOIN teams t ON t.id=m.team_id ORDER BY m.user_id,(t.name=u.department) DESC,t.id;
INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 SELECT '', 'rbac.department.migrated',m.user_id,'success',jsonb_build_object('retained_department',r.team_id,'removed_department',m.team_id,'acl_preserved',true)
 FROM team_members m JOIN rbac_retained_department r USING(user_id) WHERE m.team_id<>r.team_id;
INSERT INTO application_grants(application_id,subject_type,subject_id,permission)
 SELECT g.application_id,'user',m.user_id,CASE WHEN bool_or(g.permission='operate') AND u.role<>'viewer' THEN 'operate' ELSE 'view' END
 FROM team_members m JOIN rbac_retained_department r USING(user_id) JOIN users u ON u.id=m.user_id
 JOIN application_grants g ON g.subject_type='team' AND g.subject_id=m.team_id
 WHERE m.team_id<>r.team_id GROUP BY g.application_id,m.user_id,u.role
 ON CONFLICT(application_id,subject_type,subject_id) DO UPDATE SET permission=CASE WHEN application_grants.permission='operate' OR excluded.permission='operate' THEN 'operate' ELSE 'view' END;
DELETE FROM team_members m USING rbac_retained_department r WHERE m.user_id=r.user_id AND m.team_id<>r.team_id;
CREATE UNIQUE INDEX team_members_single_department ON team_members(user_id);
ALTER TABLE users ADD COLUMN department_id text REFERENCES teams(id);
UPDATE users u SET department_id=r.team_id FROM rbac_retained_department r WHERE u.id=r.user_id;
CREATE TABLE rbac_roles(id text PRIMARY KEY,name text NOT NULL UNIQUE,description text NOT NULL DEFAULT '',builtin boolean NOT NULL DEFAULT false,revision bigint NOT NULL DEFAULT 1);
CREATE TABLE rbac_permissions(id text PRIMARY KEY,name text NOT NULL UNIQUE,description text NOT NULL DEFAULT '',action text NOT NULL CHECK(action IN ('view','update','delete','investigate','approve','execute','manage')),scope_type text NOT NULL CHECK(scope_type IN ('resource','application','management','legacy_application')),builtin boolean NOT NULL DEFAULT false,revision bigint NOT NULL DEFAULT 1);
CREATE TABLE rbac_permission_resources(permission_id text REFERENCES rbac_permissions(id) ON DELETE CASCADE,resource_id text NOT NULL,PRIMARY KEY(permission_id,resource_id));
CREATE TABLE rbac_role_permissions(role_id text REFERENCES rbac_roles(id),permission_id text REFERENCES rbac_permissions(id),PRIMARY KEY(role_id,permission_id));
CREATE TABLE rbac_user_roles(user_id text REFERENCES users(id),role_id text REFERENCES rbac_roles(id),origin text NOT NULL DEFAULT 'direct' CHECK(origin IN ('direct','legacy')),PRIMARY KEY(user_id,role_id));
CREATE TABLE rbac_department_roles(department_id text REFERENCES teams(id),role_id text REFERENCES rbac_roles(id),PRIMARY KEY(department_id,role_id));
INSERT INTO rbac_roles(id,name,description,builtin) VALUES ('admin','平台管理员','保留现有管理员权限；管理对象逐项声明，执行仍须人工审批。',true),('operator','运维人员','保留现有应用 ACL 范围，不自动授权新应用。',true),('viewer','只读成员','保留现有应用 ACL 只读范围。',true);
INSERT INTO rbac_permissions(id,name,action,scope_type,builtin)
 SELECT 'legacy-'||a,CASE a WHEN 'view' THEN '查看' WHEN 'investigate' THEN '调查' WHEN 'approve' THEN '审批' ELSE '执行' END||' · 原应用 ACL',a,'legacy_application',true FROM unnest(ARRAY['view','investigate','approve','execute']) a;
INSERT INTO rbac_role_permissions SELECT r.id,p.id FROM rbac_roles r CROSS JOIN rbac_permissions p WHERE r.id IN ('admin','operator') OR p.action='view';
INSERT INTO rbac_permissions(id,name,action,scope_type,builtin) SELECT 'manage-'||o,'管理 · '||CASE o WHEN 'rbac' THEN '角色与权限' WHEN 'users' THEN '成员账户' WHEN 'teams' THEN '部门' WHEN 'settings' THEN '平台设置' WHEN 'applications' THEN '应用目录' WHEN 'resources' THEN '资源登记' ELSE '审计记录' END,'manage','management',true FROM unnest(ARRAY['rbac','users','teams','settings','applications','resources','audit']) o;
INSERT INTO rbac_permission_resources SELECT id,substring(id from 8) FROM rbac_permissions WHERE action='manage';
INSERT INTO rbac_role_permissions SELECT 'admin',id FROM rbac_permissions WHERE action='manage';
INSERT INTO rbac_permissions(id,name,action,scope_type,builtin) SELECT 'resource-'||a,CASE a WHEN 'view' THEN '查看' WHEN 'update' THEN '修改' ELSE '删除' END||' · 现有资源',a,'resource',true FROM unnest(ARRAY['view','update','delete']) a;
INSERT INTO rbac_permission_resources SELECT p.id,r.id FROM rbac_permissions p CROSS JOIN cmdb_resources r WHERE p.id LIKE 'resource-%';
INSERT INTO rbac_role_permissions SELECT 'admin',id FROM rbac_permissions WHERE id LIKE 'resource-%';
INSERT INTO rbac_user_roles(user_id,role_id,origin) SELECT id,role,'legacy' FROM users WHERE source<>'settings_demo' AND deleted_at IS NULL;
-- Account-management remains authoritative for the protected legacy role and last admin.
CREATE FUNCTION rbac_sync_department() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN UPDATE users SET department_id=NULL WHERE id=OLD.user_id AND department_id=OLD.team_id; RETURN OLD; END IF;
 UPDATE users SET department_id=NEW.team_id WHERE id=NEW.user_id; RETURN NEW;
END $$;
CREATE TRIGGER rbac_department_member AFTER INSERT OR DELETE ON team_members FOR EACH ROW EXECUTE FUNCTION rbac_sync_department();
CREATE FUNCTION rbac_sync_user_role() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM rbac_user_roles WHERE user_id=NEW.id AND origin='legacy';
 IF NEW.source<>'settings_demo' AND NEW.deleted_at IS NULL THEN
  INSERT INTO rbac_user_roles(user_id,role_id,origin) VALUES(NEW.id,NEW.role,'legacy') ON CONFLICT(user_id,role_id) DO UPDATE SET origin='legacy';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER rbac_user_role AFTER INSERT OR UPDATE OF role,source,deleted_at ON users FOR EACH ROW EXECUTE FUNCTION rbac_sync_user_role();
-- Direct and department grants share this view; no global administrator bypass.
CREATE VIEW rbac_effective_permissions AS
 SELECT DISTINCT u.id user_id,p.id permission_id,p.action,p.scope_type,pr.resource_id FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id) ur ON ur.user_id=u.id
 JOIN rbac_role_permissions rp ON rp.role_id=ur.role_id JOIN rbac_permissions p ON p.id=rp.permission_id
 LEFT JOIN rbac_permission_resources pr ON pr.permission_id=p.id
 WHERE NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';
CREATE INDEX rbac_permission_target ON rbac_permission_resources(resource_id);

-- Usable editable examples are real policies, with explicit current-object snapshots.
-- They are intentionally unassigned and never reseeded on restart.
INSERT INTO rbac_roles(id,name,description) VALUES ('catalog-reader','资源只读','查看迁移时资源目录的显式范围；尚未分配成员或部门。'),('application-investigator','应用诊断','调查迁移时未归档应用；不包含审批或执行。'),('change-approver','变更审批','审批迁移时未归档应用；不包含执行。');
INSERT INTO rbac_permissions(id,name,description,action,scope_type) VALUES ('catalog-read-snapshot','查看现有资源目录','显式对象快照，可在资源关联中调整。','view','resource'),('application-investigate-snapshot','调查现有应用','迁移时未归档应用快照。','investigate','application'),('application-approve-snapshot','审批现有应用变更','迁移时未归档应用快照；审批与执行独立。','approve','application');
INSERT INTO rbac_permission_resources SELECT 'catalog-read-snapshot',id FROM cmdb_resources;
INSERT INTO rbac_permission_resources SELECT p.id,a.id FROM rbac_permissions p CROSS JOIN applications a WHERE p.id IN ('application-investigate-snapshot','application-approve-snapshot') AND NOT a.archived;
INSERT INTO rbac_role_permissions VALUES('catalog-reader','catalog-read-snapshot'),('application-investigator','application-investigate-snapshot'),('change-approver','application-approve-snapshot');
