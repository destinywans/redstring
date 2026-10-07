-- WAN-246: configuration CAS, one-use token rotation and exact snapshot dismissals.
ALTER TABLE v3_connectors ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision>0);
CREATE FUNCTION v3_connector_revision_bump() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.revision := OLD.revision+1; RETURN NEW; END $$;
CREATE TRIGGER v3_connector_revision BEFORE UPDATE ON v3_connectors
FOR EACH ROW EXECUTE FUNCTION v3_connector_revision_bump();
ALTER TABLE v3_api_tokens ADD COLUMN rotated_at timestamptz;
CREATE TABLE v3_mcp_review_dismissals (
 snapshot_id text PRIMARY KEY REFERENCES v3_mcp_discovery_snapshots(id) ON DELETE CASCADE,
 dismissed_by text NOT NULL REFERENCES users(id),
 dismissed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
