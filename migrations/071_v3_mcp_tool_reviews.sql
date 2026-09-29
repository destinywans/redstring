-- Discovery order is assigned only after the registration lock is acquired.
-- Historical snapshots have no reviews, so their backfilled order cannot
-- carry an earlier approval into this new trust boundary.
CREATE SEQUENCE v3_mcp_discovery_order;
ALTER TABLE v3_mcp_discovery_snapshots
 ADD COLUMN snapshot_seq bigint;
-- Preserve the previous captured_at/id ordering for observations that exist
-- before this migration. Only the new ordering column is backfilled; the
-- original snapshot evidence remains untouched.
DROP TRIGGER v3_mcp_discovery_snapshot_no_change ON v3_mcp_discovery_snapshots;
WITH ranked AS (
 SELECT id,row_number() OVER (ORDER BY captured_at,id) AS seq
 FROM v3_mcp_discovery_snapshots
)
UPDATE v3_mcp_discovery_snapshots s SET snapshot_seq=ranked.seq
FROM ranked WHERE s.id=ranked.id;
CREATE TRIGGER v3_mcp_discovery_snapshot_no_change
 BEFORE UPDATE OR DELETE ON v3_mcp_discovery_snapshots
 FOR EACH ROW EXECUTE FUNCTION v3_mcp_discovery_snapshot_immutable();
SELECT setval('v3_mcp_discovery_order',
 GREATEST((SELECT COALESCE(max(snapshot_seq),0) FROM v3_mcp_discovery_snapshots),1),
 EXISTS(SELECT 1 FROM v3_mcp_discovery_snapshots));
ALTER TABLE v3_mcp_discovery_snapshots
 ALTER COLUMN snapshot_seq SET NOT NULL,
 ALTER COLUMN snapshot_seq SET DEFAULT nextval('v3_mcp_discovery_order');
CREATE UNIQUE INDEX v3_mcp_discovery_sequence
 ON v3_mcp_discovery_snapshots(snapshot_seq);
CREATE INDEX v3_mcp_discovery_latest
 ON v3_mcp_discovery_snapshots(extension_id,environment_id,snapshot_seq DESC);

-- A review is bound to the exact untrusted observation and its original
-- application, environment, binding instance and registration bytes.
CREATE TABLE v3_mcp_tool_reviews (
 snapshot_id text PRIMARY KEY REFERENCES v3_mcp_discovery_snapshots(id),
 extension_id text NOT NULL,
 binding_instance_id text NOT NULL,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 registration_version integer NOT NULL CHECK (registration_version > 0),
 registration_hash text NOT NULL CHECK (registration_hash ~ '^[0-9a-f]{64}$'),
 tools_hash text NOT NULL CHECK (tools_hash ~ '^[0-9a-f]{64}$'),
 reviewed_by text NOT NULL REFERENCES users(id),
 reviewed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_mcp_tool_reviews_owner
 ON v3_mcp_tool_reviews(extension_id,environment_id,reviewed_at DESC);
CREATE FUNCTION v3_mcp_tool_review_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'MCP tool reviews are immutable';
END $$;
CREATE TRIGGER v3_mcp_tool_review_no_change
 BEFORE UPDATE OR DELETE ON v3_mcp_tool_reviews
 FOR EACH ROW EXECUTE FUNCTION v3_mcp_tool_review_immutable();

-- The review decision is permanent. Only the bounded enable switch and its
-- revision can change. Effective enablement must also check the latest
-- snapshot and current registration/binding at read/use time.
CREATE TABLE v3_mcp_tool_review_decisions (
 snapshot_id text NOT NULL REFERENCES v3_mcp_tool_reviews(snapshot_id),
 tool_name text NOT NULL,
 tool_hash text NOT NULL CHECK (tool_hash ~ '^[0-9a-f]{64}$'),
 decision text NOT NULL CHECK (decision IN ('allow_read_only','deny')),
 enabled boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
 updated_by text NOT NULL REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(snapshot_id,tool_name),
 CHECK (NOT enabled OR decision='allow_read_only')
);
CREATE FUNCTION v3_mcp_tool_decision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'MCP tool review decisions cannot be deleted';
 END IF;
 IF NEW.snapshot_id <> OLD.snapshot_id OR NEW.tool_name <> OLD.tool_name OR
    NEW.tool_hash <> OLD.tool_hash OR NEW.decision <> OLD.decision OR
    NEW.revision <> OLD.revision + 1 OR NEW.enabled = OLD.enabled THEN
  RAISE EXCEPTION 'MCP tool review decisions cannot be rewritten';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_mcp_tool_decision_change_guard
 BEFORE UPDATE OR DELETE ON v3_mcp_tool_review_decisions
 FOR EACH ROW EXECUTE FUNCTION v3_mcp_tool_decision_guard();
