-- Default V3 grants for the four built-in roles (production acceptance PA-17, 2026-10-09).
-- Before this, operator/viewer held only workspace.view and change-approver held nothing,
-- so on a new instance nobody but admin could work and nobody could approve a proposal.
-- User-approved separation of duties:
--   viewer           view business data once an admin grants an environment; the built-in
--                    viewer itself never carries environment access, because invitations
--                    hand out viewer and require it to stay environment-free
--                    (internal/v3admin safeInvitationViewer)
--   operator         view + day-to-day changes; may raise proposals, cannot approve
--   change-approver  view + audit view + proposals.approve with passkey assurance
--   admin            view/change/delete/manage everything, never approve (add the
--                    change-approver role to an admin account when one person must do both)
-- Grants are only added, never removed, so roles an operator has already extended keep them.
-- Silence and alert-rule approvals use proposals.approve, so the same separation applies.

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT r.role_id, m.module, 'view', 'global', '*'
FROM (VALUES ('viewer'),('operator'),('change-approver'),('admin')) AS r(role_id)
CROSS JOIN (VALUES
  ('workspace'),('home'),('conversation'),('canvas'),('events'),('alert_rules'),
  ('silences'),('proposals'),('runbooks'),('knowledge'),('services'),('resources'),('logs'),('traces'),
  ('connectors'),('models'),('agents')
) AS m(module)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id=r.role_id)
ON CONFLICT DO NOTHING;

-- Roles an admin assigns on purpose see every environment of this instance.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT role_id, 'environment', 'view', 'global', '*'
FROM (VALUES ('operator'),('change-approver'),('admin')) AS r(role_id)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id=r.role_id)
ON CONFLICT DO NOTHING;

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'operator', module, action, 'global', '*'
FROM (VALUES
  ('conversation','change'),('canvas','change'),('events','change'),
  ('alert_rules','change'),('alert_rules','manage'),('silences','manage'),
  ('proposals','change'),('runbooks','change'),('knowledge','change'),
  ('services','change'),('resources','change')
) AS g(module,action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='operator')
ON CONFLICT DO NOTHING;

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance)
SELECT 'change-approver', module, action, 'global', '*', assurance
FROM (VALUES ('audit','view','standard'),('proposals','approve','passkey')) AS g(module,action,assurance)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='change-approver')
ON CONFLICT DO NOTHING;

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin', m.module, a.action, 'global', '*'
FROM (VALUES
  ('environment'),('workspace'),('home'),('conversation'),('canvas'),('events'),('alert_rules'),
  ('silences'),('proposals'),('runbooks'),('knowledge'),('services'),('resources'),('logs'),('traces'),
  ('connectors'),('models'),('agents'),('members'),('roles'),('audit')
) AS m(module)
CROSS JOIN (VALUES ('view'),('change'),('delete'),('manage')) AS a(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
