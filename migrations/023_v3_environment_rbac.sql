-- Add the V3 matrix alongside historical RBAC records. No legacy association
-- is deleted: callers switch to this policy after scoped grants are reviewed.
ALTER TABLE users ADD COLUMN v3_identity_kind text NOT NULL DEFAULT 'human'
 CHECK (v3_identity_kind IN ('human','machine'));
ALTER TABLE rbac_roles ADD COLUMN v3_kind text NOT NULL DEFAULT 'business'
 CHECK (v3_kind IN ('system','business','machine'));
UPDATE rbac_roles SET v3_kind='system' WHERE id IN ('admin','operator','viewer','change-approver');

CREATE TABLE v3_role_grants (
 role_id text NOT NULL REFERENCES rbac_roles(id) ON DELETE CASCADE,
 module text NOT NULL CHECK (module ~ '^[a-z][a-z0-9_]*$'),
 action text NOT NULL CHECK (action IN ('view','change','delete','approve','execute','manage')),
 scope_kind text NOT NULL CHECK (scope_kind IN ('global','environment','application','team')),
 scope_id text NOT NULL CHECK (scope_id <> ''),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (role_id,module,action,scope_kind,scope_id),
 CHECK ((scope_kind='global' AND scope_id='*') OR (scope_kind<>'global' AND scope_id<>'*'))
);
CREATE INDEX v3_role_grants_lookup ON v3_role_grants(module,action,scope_kind,scope_id);

-- Existing application environment keys are candidates for global instance
-- environments only when their IDs match exactly. A renamed/reclassified
-- environment requires an explicit mapping; no name-based fallback is used.
CREATE TABLE v3_application_environments (
 application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 PRIMARY KEY (application_id,environment_id)
);
INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT DISTINCT application_id,key FROM application_environments
 WHERE NOT archived AND key<>'' AND key<>'all';

-- Grant only the existing platform administrator access to choose an instance
-- environment. Other users need an explicit reviewed V3 environment grant.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 SELECT 'admin','environment','view','global','*'
 WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin');

CREATE VIEW v3_effective_role_grants AS
 SELECT DISTINCT u.id user_id,u.v3_identity_kind,r.v3_kind role_kind,g.module,g.action,g.scope_kind,g.scope_id
 FROM users u
 JOIN (
  SELECT user_id,role_id FROM rbac_user_roles
  UNION
  SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id
 ) assigned ON assigned.user_id=u.id
 JOIN rbac_roles r ON r.id=assigned.role_id
 JOIN v3_role_grants g ON g.role_id=r.id
 WHERE NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';
