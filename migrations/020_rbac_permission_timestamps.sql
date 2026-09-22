-- Historical creation/update times were not stored. Keep those values unknown;
-- defaults apply only to new permissions rather than inventing migration dates.
ALTER TABLE rbac_permissions ADD COLUMN created_at timestamptz;
ALTER TABLE rbac_permissions ADD COLUMN updated_at timestamptz;
ALTER TABLE rbac_permissions ALTER COLUMN created_at SET DEFAULT now();
ALTER TABLE rbac_permissions ALTER COLUMN updated_at SET DEFAULT now();

CREATE FUNCTION rbac_permission_mark_updated() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.updated_at=now();
 RETURN NEW;
END $$;
CREATE TRIGGER rbac_permission_updated BEFORE UPDATE ON rbac_permissions
 FOR EACH ROW EXECUTE FUNCTION rbac_permission_mark_updated();

-- Resource operations may change via the association editor or resource removal.
-- Track both without coupling correctness to one HTTP handler. now() is stable
-- within a transaction, including a batch of resource association changes.
CREATE FUNCTION rbac_permission_binding_updated() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN
  UPDATE rbac_permissions SET updated_at=now() WHERE id=OLD.permission_id;
 END IF;
 IF TG_OP<>'DELETE' THEN
  UPDATE rbac_permissions SET updated_at=now() WHERE id=NEW.permission_id;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER rbac_permission_binding_updated AFTER INSERT OR UPDATE OR DELETE ON rbac_permission_resources
 FOR EACH ROW EXECUTE FUNCTION rbac_permission_binding_updated();
