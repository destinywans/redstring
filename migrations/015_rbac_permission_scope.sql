-- Preserve legacy ACL evaluation until a manager explicitly edits its targets.
-- An explicit selection narrows the old per-user ACL; it never replaces it.
ALTER TABLE rbac_permissions ADD COLUMN resource_scope_mode text NOT NULL DEFAULT 'explicit'
 CHECK (resource_scope_mode IN ('explicit','legacy_acl'));
UPDATE rbac_permissions SET resource_scope_mode='legacy_acl' WHERE scope_type='legacy_application';
