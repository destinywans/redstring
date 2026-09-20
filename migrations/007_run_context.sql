-- Each attempt's submitted input is independent of canonical context revisions.
CREATE TABLE IF NOT EXISTS run_context_manifests (
 attempt_id text PRIMARY KEY REFERENCES attempts(id),
 run_id text NOT NULL REFERENCES runs(id),
 manifest jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS run_context_manifests_run ON run_context_manifests(run_id,created_at DESC);
