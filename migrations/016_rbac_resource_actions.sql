-- Permissions are named bundles. Operations belong to each resource binding.
-- Freeze unedited compatibility permissions at the current application set;
-- their authorization still intersects the original member/application ACL.
INSERT INTO rbac_permission_resources(permission_id,resource_id)
 SELECT p.id,a.id FROM rbac_permissions p CROSS JOIN applications a
 WHERE p.scope_type='legacy_application' AND p.resource_scope_mode='legacy_acl'
 ON CONFLICT DO NOTHING;

ALTER TABLE rbac_permission_resources ADD COLUMN scope_type text;
ALTER TABLE rbac_permission_resources ADD COLUMN action text;
UPDATE rbac_permission_resources pr SET scope_type=p.scope_type,action=p.action
 FROM rbac_permissions p WHERE p.id=pr.permission_id;
ALTER TABLE rbac_permission_resources ALTER COLUMN scope_type SET NOT NULL;
ALTER TABLE rbac_permission_resources ALTER COLUMN action SET NOT NULL;
ALTER TABLE rbac_permission_resources ADD CHECK(scope_type IN ('resource','application','management','legacy_application'));
ALTER TABLE rbac_permission_resources ADD CHECK(action IN ('view','update','delete','investigate','approve','execute','manage'));
ALTER TABLE rbac_permission_resources DROP CONSTRAINT rbac_permission_resources_pkey;
ALTER TABLE rbac_permission_resources ADD PRIMARY KEY(permission_id,scope_type,resource_id,action);

CREATE OR REPLACE VIEW rbac_effective_permissions AS
 SELECT DISTINCT u.id user_id,p.id permission_id,pr.action,pr.scope_type,pr.resource_id FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id) ur ON ur.user_id=u.id
 JOIN rbac_role_permissions rp ON rp.role_id=ur.role_id JOIN rbac_permissions p ON p.id=rp.permission_id
 JOIN rbac_permission_resources pr ON pr.permission_id=p.id
 WHERE NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';

ALTER TABLE rbac_permissions DROP COLUMN action;
ALTER TABLE rbac_permissions DROP COLUMN scope_type;
ALTER TABLE rbac_permissions DROP COLUMN resource_scope_mode;
