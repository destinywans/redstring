-- Raw evidence for successful governed MCP reads is private to the original
-- execution actor, like execution output. Shared Agent spans stay metadata-only.
ALTER TABLE v3_agent_execution_results ADD COLUMN mcp_evidence jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK (jsonb_typeof(mcp_evidence) = 'array'
  AND jsonb_array_length(mcp_evidence) <= 4
  AND octet_length(mcp_evidence::text) <= 131072);
