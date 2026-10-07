-- Explicit page configuration; no telemetry or team ownership is inferred.
CREATE TABLE v3_service_slo_settings (
 application_id text NOT NULL,
 environment_id text NOT NULL,
 target text NOT NULL CHECK (target IN ('99.5','99.9','99.95','99.99')),
 slo_window text NOT NULL CHECK (slo_window IN ('7d','30d')),
 revision bigint NOT NULL CHECK (revision > 0),
 updated_by text NOT NULL REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(application_id,environment_id),
 FOREIGN KEY(application_id,environment_id) REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE TABLE v3_trace_views (
 id text PRIMARY KEY,
 environment_id text NOT NULL,
 team_id text NOT NULL REFERENCES teams(id),
 scope text NOT NULL CHECK (scope IN ('app','agent')),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 query text NOT NULL CHECK (length(query) <= 2048),
 owner_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_trace_views_scope ON v3_trace_views(environment_id,team_id,scope,created_at,id);
