-- The V3 service discovery projection exposes inventory fields. Keep its
-- resource-view check independent from services.view, while preserving the
-- legacy administrator's existing ability to read infrastructure resources.
-- Do not grant this permission to non-administrator roles implicitly.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','resources','view','global','*'
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
