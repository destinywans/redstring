-- No input, output, retrieved evidence, or credentials in this provenance row.
CREATE TABLE v3_agent_execution_snapshots (
 run_id text PRIMARY KEY REFERENCES v3_agent_runs(id),
 actor_id text NOT NULL REFERENCES users(id),
 lifecycle_revision bigint NOT NULL,
 config_sha256 text NOT NULL,
 model_id text NOT NULL,
 prompt_id text NOT NULL,
 prompt_version integer NOT NULL,
 governance_sha256 text NOT NULL,
 deadline timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_agent_execution_expiry ON v3_agent_execution_snapshots(deadline,run_id);
CREATE TABLE v3_agent_execution_results (
 run_id text PRIMARY KEY REFERENCES v3_agent_execution_snapshots(run_id) ON DELETE CASCADE,
 output_text text NOT NULL CHECK (octet_length(output_text)<=32768),
 created_at timestamptz NOT NULL DEFAULT now()
);
