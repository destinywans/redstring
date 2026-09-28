-- Explicitly map only privileges already held by the legacy base roles.
-- No legacy permission is consulted at request time once a route is marked V3.
-- Additional operators, teams and application scopes need reviewed grants.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin', module, action, 'global', '*'
FROM (VALUES
  ('workspace','view'),('workspace','manage'),
  ('connectors','view'),('connectors','manage'),
  ('conversation','view'),('conversation','change'),
  ('models','view'),
  ('services','view'),('logs','view'),('traces','view'),
  ('events','view'),('events','change'),
  ('knowledge','view'),('knowledge','manage')
) AS grants(module,action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT role_id,'workspace','view','global','*'
FROM (VALUES ('operator'),('viewer')) AS roles(role_id)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id=role_id)
ON CONFLICT DO NOTHING;
