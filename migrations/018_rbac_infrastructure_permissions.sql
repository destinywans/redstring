-- No authorization data is deleted here. Existing review data is reset only by
-- an explicitly invoked local reset tool, never automatically on application start.
CREATE OR REPLACE FUNCTION rbac_is_infrastructure_kind(value text) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
 SELECT value IN ('cluster','node','deployment','namespace','host','network','storage','redis','mysql','kafka');
$$;

CREATE OR REPLACE VIEW rbac_platform_administrators AS
 SELECT DISTINCT u.id user_id FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id) ur ON ur.user_id=u.id
 JOIN rbac_roles r ON r.id=ur.role_id
 WHERE r.id='admin' AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';

CREATE OR REPLACE VIEW rbac_effective_permissions AS
 SELECT DISTINCT u.id user_id,p.id permission_id,pr.action,pr.scope_type,pr.resource_id FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id) ur ON ur.user_id=u.id
 JOIN rbac_role_permissions rp ON rp.role_id=ur.role_id JOIN rbac_permissions p ON p.id=rp.permission_id
 JOIN rbac_permission_resources pr ON pr.permission_id=p.id
 JOIN cmdb_resources r ON r.id=pr.resource_id AND rbac_is_infrastructure_kind(r.kind)
 WHERE pr.scope_type='resource' AND pr.action IN ('view','update','approve','delete')
 AND NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';

-- Runtime records are visible only through their single discovered owner/object.
-- No arbitrary dependency, reverse traversal, name matching or application-wide
-- inheritance is allowed; an event about a Pod follows one further owner edge.
CREATE OR REPLACE VIEW rbac_viewable_resources AS
 WITH pod_owners AS (
  SELECT child.id resource_id,min(parent.id) parent_id FROM cmdb_resources child
  JOIN cmdb_resource_relations rel ON rel.source_resource_id=child.id AND rel.kind='belongs_to' AND rel.provenance='discovered'
  JOIN cmdb_resources parent ON parent.id=rel.target_resource_id AND parent.kind='deployment'
  WHERE child.kind='pod' GROUP BY child.id HAVING count(DISTINCT parent.id)=1
 ), direct_views AS (
  SELECT DISTINCT user_id,resource_id FROM rbac_effective_permissions WHERE action='view'
 ), base_views AS (
  SELECT * FROM direct_views UNION SELECT p.user_id,o.resource_id FROM pod_owners o JOIN direct_views p ON p.resource_id=o.parent_id
 ), event_objects AS (
  SELECT child.id resource_id,min(parent.id) parent_id FROM cmdb_resources child
  JOIN cmdb_resource_relations rel ON rel.source_resource_id=child.id AND rel.kind='involved_object' AND rel.provenance='discovered'
  JOIN cmdb_resources parent ON parent.id=rel.target_resource_id AND (rbac_is_infrastructure_kind(parent.kind) OR parent.kind='pod')
  WHERE child.kind='event' GROUP BY child.id HAVING count(DISTINCT parent.id)=1
 ) SELECT * FROM base_views UNION SELECT p.user_id,o.resource_id FROM event_objects o JOIN base_views p ON p.resource_id=o.parent_id;

-- NOT VALID leaves historical rows untouched until the authorized reset, while
-- rejecting new writes of the retired scopes/actions immediately.
ALTER TABLE rbac_permission_resources ADD CONSTRAINT rbac_infrastructure_binding
 CHECK(scope_type='resource' AND action IN ('view','update','approve','delete')) NOT VALID;

-- First install runs migrations before Bootstrap creates any user. Replace only
-- the untouched historical seed identifiers in that empty-account case. Existing
-- installations require the explicit reset tool; subsequent starts never reseed.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM users)
 AND NOT EXISTS(SELECT 1 FROM rbac_roles WHERE id NOT IN ('admin','operator','viewer','catalog-reader','application-investigator','change-approver'))
 AND NOT EXISTS(SELECT 1 FROM rbac_permissions WHERE id NOT IN ('legacy-view','legacy-investigate','legacy-approve','legacy-execute','manage-rbac','manage-users','manage-teams','manage-settings','manage-applications','manage-resources','manage-audit','resource-view','resource-update','resource-delete','catalog-read-snapshot','application-investigate-snapshot','application-approve-snapshot')) THEN
  DELETE FROM rbac_role_permissions;
  DELETE FROM rbac_department_roles;
  DELETE FROM rbac_permissions;
  DELETE FROM rbac_roles;
  INSERT INTO rbac_roles(id,name,description,builtin) VALUES
   ('admin','平台管理员','管理平台设置与身份；资源操作仍按明确关联授权。',true),
   ('operator','运维人员','查看和变更已关联的基础设施资源。',true),
   ('viewer','只读成员','查看已关联的基础设施资源。',true),
   ('change-approver','变更审批员','查看并审批已关联资源的变更。',true);
  INSERT INTO rbac_permissions(id,name,builtin) VALUES
   ('resource-view','资源查看',true),('resource-update','资源变更',true),('resource-approve','资源审批',true),('resource-delete','资源删除',true);
  INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
   SELECT 'resource-'||a,'resource',r.id,a FROM cmdb_resources r CROSS JOIN unnest(ARRAY['view','update','approve','delete']) a WHERE rbac_is_infrastructure_kind(r.kind);
  INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES
   ('admin','resource-view'),('admin','resource-update'),('admin','resource-approve'),('admin','resource-delete'),
   ('operator','resource-view'),('operator','resource-update'),('viewer','resource-view'),
   ('change-approver','resource-view'),('change-approver','resource-approve');
 END IF;
END $$;
