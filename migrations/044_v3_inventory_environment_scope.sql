-- Resource attributes are presentation data, not an authorization boundary.
-- Historical sources remain unassigned until a scoped successful resync;
-- no environment is inferred from old resource attributes.
ALTER TABLE cmdb_sync_sources ADD COLUMN environment_key text NOT NULL DEFAULT '';
ALTER TABLE cmdb_sync_sources ADD CONSTRAINT cmdb_sync_source_environment_key
 CHECK (environment_key = '' OR (char_length(environment_key) <= 100
   AND environment_key ~ '^[A-Za-z0-9_.-]+$' AND environment_key <> 'all'));
