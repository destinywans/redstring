-- A bounded, per-source request budget. The source definition is held in a
-- private operator catalog, so there is intentionally no source FK here.
-- Telemetry, query expressions, endpoints and credentials are never stored.
CREATE TABLE v3_pull_source_identities (
 source_id text PRIMARY KEY,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 connector_id text NOT NULL,
 target_id text NOT NULL,
 resource_id text NOT NULL,
 query_id text NOT NULL,
 config_digest text NOT NULL CHECK (config_digest ~ '^[0-9a-f]{64}$'),
 first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE v3_pull_attempts (
 id bigserial PRIMARY KEY,
 source_id text NOT NULL REFERENCES v3_pull_source_identities(source_id),
 actor_id text NOT NULL REFERENCES users(id),
 kind text NOT NULL CHECK (kind IN ('preview','test')),
 status text NOT NULL CHECK (status IN ('pending','success','failure')),
 rows_count integer NOT NULL DEFAULT 0 CHECK (rows_count >= 0),
 dropped_count integer NOT NULL DEFAULT 0 CHECK (dropped_count >= 0),
 error_code text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 completed_at timestamptz
);
CREATE INDEX v3_pull_attempts_source_time ON v3_pull_attempts(source_id,started_at DESC);
CREATE INDEX v3_pull_attempts_actor_time ON v3_pull_attempts(source_id,actor_id,started_at DESC);
