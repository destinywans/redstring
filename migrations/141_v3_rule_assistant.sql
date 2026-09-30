-- One actor key reserves one bounded AI draft attempt. A response loss or
-- uncertain provider outcome never authorizes another model call with that key.
CREATE TABLE v3_rule_generate_attempts (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 request_key text NOT NULL CHECK (length(request_key) BETWEEN 8 AND 128),
 request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 catalog_sha256 text NOT NULL CHECK (catalog_sha256 ~ '^[0-9a-f]{64}$'),
 model_provider text NOT NULL,
 model_name text NOT NULL,
 max_cost_micro_usd bigint NOT NULL CHECK (max_cost_micro_usd > 0),
 status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed','ambiguous')),
 draft jsonb,
 provider_request_id text NOT NULL DEFAULT '' CHECK (length(provider_request_id) <= 256),
 input_tokens bigint CHECK (input_tokens >= 0),
 output_tokens bigint CHECK (output_tokens >= 0),
 duration_ms bigint CHECK (duration_ms >= 0),
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 UNIQUE (actor_id,request_key),
 CHECK ((status='succeeded' AND draft IS NOT NULL) OR (status<>'succeeded' AND draft IS NULL))
);
CREATE INDEX v3_rule_generate_attempts_scope
 ON v3_rule_generate_attempts(application_id,environment_id,created_at DESC,id DESC);

CREATE TABLE v3_rule_generate_budget (
 actor_id text NOT NULL REFERENCES users(id),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 next_allowed_at timestamptz NOT NULL,
 PRIMARY KEY (actor_id,application_id,environment_id)
);

-- A 7-day Prometheus read is costly even though it is read-only. Coordinate
-- a small actor/application/environment cooldown across API processes.
CREATE TABLE v3_rule_preview_budget (
 actor_id text NOT NULL REFERENCES users(id),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 next_allowed_at timestamptz NOT NULL,
 PRIMARY KEY (actor_id,application_id,environment_id)
);
