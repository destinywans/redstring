-- V3 audit evidence. The historical prefix is sealed at migration time: the
-- chain proves continuity from this point onward, not pre-migration history.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

ALTER TABLE management_audit
  ADD COLUMN chain_seq bigint,
  ADD COLUMN chain_prev_hash text,
  ADD COLUMN chain_hash text;

CREATE SEQUENCE v3_audit_chain_seq;

-- Migration runs under the migration lock. The table lock keeps concurrent
-- writers out while the historical prefix is sealed.
LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE;
DO $$
DECLARE
  rec management_audit%ROWTYPE;
  previous text := '';
  next_seq bigint := 0;
  current_hash text;
BEGIN
  FOR rec IN SELECT * FROM management_audit ORDER BY id LOOP
    next_seq := next_seq + 1;
    current_hash := encode(digest(jsonb_build_array(
      previous, rec.id, rec.actor_id, rec.actor_kind,
      rec.application_id, rec.action, rec.target, rec.outcome,
      rec.changes, rec.created_at
    )::text, 'sha256'), 'hex');
    UPDATE management_audit SET chain_seq=next_seq,
      chain_prev_hash=previous, chain_hash=current_hash WHERE id=rec.id;
    previous := current_hash;
  END LOOP;
  PERFORM setval('v3_audit_chain_seq', GREATEST(next_seq, 1), next_seq > 0);
END $$;

ALTER TABLE management_audit ALTER COLUMN chain_seq SET NOT NULL;
ALTER TABLE management_audit ALTER COLUMN chain_prev_hash SET NOT NULL;
ALTER TABLE management_audit ALTER COLUMN chain_hash SET NOT NULL;
CREATE UNIQUE INDEX v3_audit_chain_order ON management_audit(chain_seq);

CREATE FUNCTION v3_audit_seal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  previous text;
BEGIN
  -- Serialize append order across transactions. Sequence values are assigned
  -- only after this lock, so chain_seq follows commit-safe insertion order.
  PERFORM pg_advisory_xact_lock(734911, 37);
  SELECT chain_hash INTO previous FROM management_audit ORDER BY chain_seq DESC LIMIT 1;
  NEW.chain_seq := nextval('v3_audit_chain_seq');
  NEW.chain_prev_hash := COALESCE(previous, '');
  NEW.chain_hash := encode(digest(jsonb_build_array(
    NEW.chain_prev_hash, NEW.id, NEW.actor_id, NEW.actor_kind,
    NEW.application_id, NEW.action, NEW.target, NEW.outcome,
    NEW.changes, NEW.created_at
  )::text, 'sha256'), 'hex');
  RETURN NEW;
END $$;

CREATE FUNCTION v3_audit_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'management_audit records are immutable';
END $$;

CREATE TRIGGER v3_audit_seal_insert BEFORE INSERT ON management_audit
  FOR EACH ROW EXECUTE FUNCTION v3_audit_seal();
CREATE TRIGGER v3_audit_no_change BEFORE UPDATE OR DELETE ON management_audit
  FOR EACH ROW EXECUTE FUNCTION v3_audit_immutable();

CREATE TABLE v3_audit_exports (
  id text PRIMARY KEY,
  owner_id text NOT NULL REFERENCES users(id),
  environment_ids text[] NOT NULL,
  includes_global boolean NOT NULL,
  csv_data bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT (now() + interval '1 hour')
);
CREATE INDEX v3_audit_exports_expiry ON v3_audit_exports(expires_at);

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','audit',action,'global','*'
FROM (VALUES ('view'),('manage')) AS permissions(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
