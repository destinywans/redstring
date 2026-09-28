-- An observation is historical evidence, not an approval or a tool allowlist.
-- ai_extensions is created by the legacy migration after store.Migrate, so
-- this table deliberately has no foreign key to it.
-- A new binding receives a new identity even if an operator immediately
-- unbinds and rebinds the same extension and environment during discovery.
ALTER TABLE v3_mcp_environment_bindings
 ADD COLUMN binding_instance_id text NOT NULL DEFAULT gen_random_uuid()::text;

CREATE TABLE v3_mcp_discovery_snapshots (
 id text PRIMARY KEY,
 extension_id text NOT NULL,
 binding_instance_id text NOT NULL,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 registration_version integer NOT NULL CHECK (registration_version > 0),
 registration_hash text NOT NULL CHECK (registration_hash ~ '^[0-9a-f]{64}$'),
 tools_hash text NOT NULL CHECK (tools_hash ~ '^[0-9a-f]{64}$'),
 tools json NOT NULL CHECK (json_typeof(tools) = 'array' AND octet_length(tools::text) <= 524288),
 tool_count integer NOT NULL CHECK (tool_count BETWEEN 0 AND 128 AND tool_count = json_array_length(tools)),
 captured_by text NOT NULL,
 captured_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_mcp_discovery_snapshots_history
 ON v3_mcp_discovery_snapshots(extension_id,environment_id,captured_at DESC,id DESC);

CREATE FUNCTION v3_mcp_discovery_snapshot_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'MCP discovery snapshots are immutable';
END $$;
CREATE TRIGGER v3_mcp_discovery_snapshot_no_change
 BEFORE UPDATE OR DELETE ON v3_mcp_discovery_snapshots
 FOR EACH ROW EXECUTE FUNCTION v3_mcp_discovery_snapshot_immutable();
