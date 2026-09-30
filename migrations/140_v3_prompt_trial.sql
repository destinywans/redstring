-- Trusted case input is separate from a registered case reference or a
-- historical evaluation report. Public HTTP cannot create these snapshots.
-- PostgreSQL requires an explicit matching unique key for the composite FK,
-- even though case id alone is already the primary key.
ALTER TABLE v3_agent_eval_cases
 ADD CONSTRAINT v3_agent_eval_cases_id_agent_unique UNIQUE (id,agent_id);

CREATE TABLE v3_agent_prompt_trial_case_inputs (
 id text PRIMARY KEY,
 case_id text NOT NULL,
 agent_id text NOT NULL,
 origin_application_id text NOT NULL,
 origin_environment_id text NOT NULL,
 revision bigint NOT NULL CHECK (revision > 0),
 producer_ref text NOT NULL CHECK (length(btrim(producer_ref)) BETWEEN 1 AND 500),
 input_text text NOT NULL CHECK (octet_length(input_text) BETWEEN 1 AND 8192),
 input_sha256 text NOT NULL CHECK (input_sha256 ~ '^[0-9a-f]{64}$'),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY (case_id,agent_id) REFERENCES v3_agent_eval_cases(id,agent_id),
 UNIQUE (case_id,revision)
);
CREATE INDEX v3_prompt_trial_case_input_latest
 ON v3_agent_prompt_trial_case_inputs(case_id,revision DESC);

-- A request key identifies one bounded, non-replayed pair of model calls.
-- Prompt and case input references are immutable once reserved.
CREATE TABLE v3_agent_prompt_trials (
 id text PRIMARY KEY,
 prompt_id text NOT NULL REFERENCES v3_agent_prompts(id),
 agent_id text NOT NULL REFERENCES v3_agents(id),
 application_id text NOT NULL,
 environment_id text NOT NULL,
 case_id text NOT NULL REFERENCES v3_agent_eval_cases(id),
 case_input_id text NOT NULL REFERENCES v3_agent_prompt_trial_case_inputs(id),
 actor_id text NOT NULL REFERENCES users(id),
 request_key text NOT NULL CHECK (length(request_key) BETWEEN 8 AND 128),
 request_sha256 text NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
 a_version integer NOT NULL CHECK (a_version > 0),
 b_version integer NOT NULL CHECK (b_version > 0 AND b_version <> a_version),
 a_prompt_sha256 text NOT NULL CHECK (a_prompt_sha256 ~ '^[0-9a-f]{64}$'),
 b_prompt_sha256 text NOT NULL CHECK (b_prompt_sha256 ~ '^[0-9a-f]{64}$'),
 input_sha256 text NOT NULL CHECK (input_sha256 ~ '^[0-9a-f]{64}$'),
 model_provider text NOT NULL,
 model_name text NOT NULL,
 max_output_tokens integer NOT NULL CHECK (max_output_tokens BETWEEN 1 AND 256),
 max_cost_micro_usd bigint NOT NULL CHECK (max_cost_micro_usd > 0),
 status text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
 error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 FOREIGN KEY (prompt_id,a_version) REFERENCES v3_agent_prompt_versions(prompt_id,version),
 FOREIGN KEY (prompt_id,b_version) REFERENCES v3_agent_prompt_versions(prompt_id,version),
 UNIQUE (actor_id,request_key)
);
CREATE INDEX v3_prompt_trials_prompt_order ON v3_agent_prompt_trials(prompt_id,created_at DESC,id DESC);

CREATE TABLE v3_agent_prompt_trial_runs (
 trial_id text NOT NULL REFERENCES v3_agent_prompt_trials(id),
 side text NOT NULL CHECK (side IN ('a','b')),
 status text NOT NULL CHECK (status IN ('pending','started','succeeded','failed')),
 output_text text CHECK (octet_length(output_text) <= 16384),
 provider_request_id text NOT NULL DEFAULT '' CHECK (length(provider_request_id) <= 256),
 input_tokens bigint CHECK (input_tokens >= 0),
 output_tokens bigint CHECK (output_tokens >= 0),
 duration_ms bigint CHECK (duration_ms >= 0),
 error_code text NOT NULL DEFAULT '',
 started_at timestamptz,
 finished_at timestamptz,
 PRIMARY KEY (trial_id,side)
);
