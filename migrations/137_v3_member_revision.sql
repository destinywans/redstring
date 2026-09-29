-- Contact edits are versioned even when they come from legacy account or
-- personal-profile writers. A trigger makes the V3 optimistic lock universal.
ALTER TABLE users ADD COLUMN v3_member_revision bigint NOT NULL DEFAULT 1
  CHECK (v3_member_revision > 0);

CREATE FUNCTION v3_bump_member_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.name IS DISTINCT FROM OLD.name OR NEW.phone IS DISTINCT FROM OLD.phone THEN
    NEW.v3_member_revision := OLD.v3_member_revision + 1;
  ELSE
    NEW.v3_member_revision := OLD.v3_member_revision;
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER v3_member_contact_revision BEFORE UPDATE ON users
  FOR EACH ROW EXECUTE FUNCTION v3_bump_member_revision();
