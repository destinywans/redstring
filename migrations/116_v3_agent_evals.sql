-- Evaluation records are configuration and reported results from a trusted
-- evaluator. Creating an experiment alone does not imply an executed score.
CREATE TABLE v3_agent_eval_cases (
 id text PRIMARY KEY,
 agent_id text NOT NULL REFERENCES v3_agents(id),
 title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 240),
 expected_ref text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE v3_agent_eval_experiments (
 id text PRIMARY KEY,
 agent_id text NOT NULL REFERENCES v3_agents(id),
 title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 240),
 baseline_version text NOT NULL,
 candidate_version text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','passed','failed','invalid')),
 metrics jsonb,
 result_ref text,
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 FOREIGN KEY (agent_id,baseline_version) REFERENCES v3_agent_versions(agent_id,version),
 FOREIGN KEY (agent_id,candidate_version) REFERENCES v3_agent_versions(agent_id,version),
 CHECK ((status IN ('pending','running') AND finished_at IS NULL AND metrics IS NULL)
     OR (status IN ('passed','failed','invalid') AND finished_at IS NOT NULL))
);
CREATE INDEX v3_agent_eval_gate ON v3_agent_eval_experiments(agent_id,candidate_version,status,finished_at DESC);
CREATE TABLE v3_agent_eval_labels (
 id text PRIMARY KEY,
 case_id text NOT NULL REFERENCES v3_agent_eval_cases(id),
 label text NOT NULL CHECK (label IN ('correct','incorrect','partial','unsure')),
 note text NOT NULL DEFAULT '' CHECK (length(note)<=2000),
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_agent_eval_labels_order ON v3_agent_eval_labels(case_id,created_at DESC,id DESC);
