-- Visible agent steps per Attempt: each model call and read-only tool call.
-- Token counts come from provider usage when reported; NULL means not reported.
CREATE TABLE IF NOT EXISTS run_steps (
 id text PRIMARY KEY,
 run_id text NOT NULL REFERENCES runs(id),
 attempt_id text NOT NULL REFERENCES attempts(id),
 seq integer NOT NULL,
 kind text NOT NULL CHECK (kind IN ('model','tool')),
 name text NOT NULL,
 status text NOT NULL CHECK (status IN ('running','succeeded','failed')),
 input text NOT NULL DEFAULT '',
 output text NOT NULL DEFAULT '',
 error text NOT NULL DEFAULT '',
 evidence_id text REFERENCES evidence(id),
 input_tokens integer,
 output_tokens integer,
 tool_calls jsonb NOT NULL DEFAULT '[]',
 trace_id text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL,
 finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS run_steps_run ON run_steps(run_id,started_at,seq);
