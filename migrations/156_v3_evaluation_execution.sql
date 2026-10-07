-- One explicit, non-replayed two-version execution per experiment. Human
-- source material is separate from model output and human correctness labels.
CREATE TABLE v3_agent_eval_inputs (
 case_id text PRIMARY KEY REFERENCES v3_agent_eval_cases(id),
 input_id text NOT NULL REFERENCES v3_agent_prompt_trial_case_inputs(id),
 material jsonb NOT NULL CHECK(jsonb_typeof(material)='object' AND octet_length(material::text)<=65536),
 material_sha256 text NOT NULL CHECK(material_sha256 ~ '^[0-9a-f]{64}$'),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE v3_agent_eval_executions (
 experiment_id text PRIMARY KEY REFERENCES v3_agent_eval_experiments(id),
 case_id text NOT NULL REFERENCES v3_agent_eval_inputs(case_id),
 actor_id text NOT NULL REFERENCES users(id),
 application_id text NOT NULL,
 environment_id text NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 8 AND 128),
 material jsonb NOT NULL CHECK(jsonb_typeof(material)='object' AND octet_length(material::text)<=262144),
 material_sha256 text NOT NULL CHECK(material_sha256 ~ '^[0-9a-f]{64}$'),
 status text NOT NULL CHECK(status IN ('running','succeeded','failed','cancelled','ambiguous')),
 error_code text NOT NULL DEFAULT '',
 started_sides integer NOT NULL DEFAULT 0 CHECK(started_sides BETWEEN 0 AND 2),
 results jsonb,
 snapshot_id text REFERENCES v3_agent_eval_case_snapshots(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL,
 finished_at timestamptz,
 UNIQUE(actor_id,request_key),
 CHECK((status='running' AND finished_at IS NULL AND results IS NULL AND snapshot_id IS NULL)
 OR (status='succeeded' AND finished_at IS NOT NULL AND results IS NOT NULL AND snapshot_id IS NOT NULL)
 OR (status IN ('failed','cancelled','ambiguous') AND finished_at IS NOT NULL AND results IS NULL AND snapshot_id IS NULL))
);
