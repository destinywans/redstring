-- Default roles are ordinary persisted records, not startup seeds. Deleting a
-- base role must not make subsequent account updates fail or resurrect it.
CREATE OR REPLACE FUNCTION rbac_sync_user_role() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM rbac_user_roles WHERE user_id=NEW.id AND origin='legacy';
 IF NEW.source<>'settings_demo' AND NEW.deleted_at IS NULL THEN
  INSERT INTO rbac_user_roles(user_id,role_id,origin)
   SELECT NEW.id,r.id,'legacy' FROM rbac_roles r WHERE r.id=NEW.role
   ON CONFLICT(user_id,role_id) DO UPDATE SET origin='legacy';
 END IF;
 RETURN NEW;
END $$;
