-- PA-55 / decision D6 (2026-10-10): the built-in viewer can see every
-- environment of this instance, like operator and change-approver. Without
-- environment.view every environment-scoped read was 403, so a member invited
-- as viewer only ever saw the no-access page. Members without any environment
-- grant (for example a custom role without one) still get that page.
-- Invitations accept exactly this one environment grant on the viewer.
-- Not executed on the existing business database.
WITH added AS (
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 SELECT 'viewer', 'environment', 'view', 'global', '*'
 WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='viewer')
 ON CONFLICT DO NOTHING
 RETURNING role_id
)
UPDATE rbac_roles SET v3_matrix_revision=v3_matrix_revision+1
WHERE id='viewer' AND EXISTS (SELECT 1 FROM added);
