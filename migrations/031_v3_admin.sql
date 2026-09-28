-- The V3 role matrix stores stable module/action codes. A conditional cell is
-- never an ordinary approval grant; authorization must verify a step-up proof.
ALTER TABLE v3_role_grants ADD COLUMN assurance text NOT NULL DEFAULT 'standard'
  CHECK (assurance IN ('standard', 'passkey'));
ALTER TABLE v3_role_grants ADD CONSTRAINT v3_passkey_only_approval
  CHECK (assurance <> 'passkey' OR action = 'approve');
-- Existing V3 approval rows are also conditional. They cannot silently become
-- usable merely because the account has a role.
UPDATE v3_role_grants SET assurance='passkey' WHERE action='approve';

-- The legacy administrator already has RBAC and account-management authority.
-- No other legacy role or ambiguous application ACL is promoted here.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin', module, action, 'global', '*'
FROM (VALUES
  ('roles','view'), ('roles','manage'),
  ('members','view'), ('members','manage')
) AS mapped(module,action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;

-- Record V3 matrix changes independently of the older RBAC revision. The
-- current frontend does not send a revision, so this is not an ETag contract.
ALTER TABLE rbac_roles ADD COLUMN v3_matrix_revision bigint NOT NULL DEFAULT 1;

-- Append the assurance marker to the effective-grants view, preserving the
-- existing column order used by prior policy queries.
CREATE OR REPLACE VIEW v3_effective_role_grants AS
 SELECT DISTINCT u.id user_id,u.v3_identity_kind,r.v3_kind role_kind,
        g.module,g.action,g.scope_kind,g.scope_id,g.assurance
 FROM users u
 JOIN (
  SELECT user_id,role_id FROM rbac_user_roles
  UNION
  SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id
 ) assigned ON assigned.user_id=u.id
 JOIN rbac_roles r ON r.id=assigned.role_id
 JOIN v3_role_grants g ON g.role_id=r.id
 WHERE NOT u.disabled AND u.deleted_at IS NULL AND u.source<>'settings_demo';
