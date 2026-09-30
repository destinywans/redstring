-- Keep protocol metadata in the same immutable observation as tools. Older
-- tool-only snapshots retain unknown capability states and nullable metadata.
ALTER TABLE v3_mcp_discovery_snapshots
 ADD COLUMN server_name text CHECK (server_name IS NULL OR length(server_name) BETWEEN 1 AND 160),
 ADD COLUMN server_version text CHECK (server_version IS NULL OR length(server_version) BETWEEN 1 AND 160),
 ADD COLUMN protocol_version text CHECK (protocol_version IS NULL OR length(protocol_version) BETWEEN 1 AND 80),
 ADD COLUMN tools_state text NOT NULL DEFAULT 'supported' CHECK (tools_state IN ('unknown','supported','unsupported')),
 ADD COLUMN resources_state text NOT NULL DEFAULT 'unknown' CHECK (resources_state IN ('unknown','supported','unsupported')),
 ADD COLUMN prompts_state text NOT NULL DEFAULT 'unknown' CHECK (prompts_state IN ('unknown','supported','unsupported')),
 ADD COLUMN resources json CHECK (resources IS NULL OR
  (json_typeof(resources)='array' AND json_array_length(resources)<=128 AND octet_length(resources::text)<=262144)),
 ADD COLUMN prompts json CHECK (prompts IS NULL OR
  (json_typeof(prompts)='array' AND json_array_length(prompts)<=128 AND octet_length(prompts::text)<=262144)),
 ADD CONSTRAINT v3_mcp_capability_observation_consistency CHECK (
  (tools_state<>'unsupported' OR tool_count=0) AND
  ((resources_state='supported')=(resources IS NOT NULL)) AND
  ((prompts_state='supported')=(prompts IS NOT NULL))
 );
