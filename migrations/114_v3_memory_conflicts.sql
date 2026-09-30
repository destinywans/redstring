-- Conflicts are explicitly recorded by an authorized human. They are never
-- inferred from semantic similarity or manufactured from two search hits.
CREATE TABLE IF NOT EXISTS v3_memory_conflicts (
  id text PRIMARY KEY,
  environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
  application_id text NOT NULL DEFAULT '',
  team_id text NOT NULL DEFAULT '',
  fact_a_id text NOT NULL REFERENCES v3_memory_facts(id) ON DELETE RESTRICT,
  fact_b_id text NOT NULL REFERENCES v3_memory_facts(id) ON DELETE RESTRICT,
  state text NOT NULL DEFAULT 'open' CHECK (state IN ('open','resolved')),
  keep_fact_id text REFERENCES v3_memory_facts(id),
  drop_fact_id text REFERENCES v3_memory_facts(id),
  created_by text NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_by text REFERENCES users(id),
  resolved_at timestamptz,
  CHECK (fact_a_id <> fact_b_id),
  CHECK ((state='open' AND keep_fact_id IS NULL AND drop_fact_id IS NULL AND resolved_by IS NULL AND resolved_at IS NULL)
      OR (state='resolved' AND keep_fact_id IS NOT NULL AND drop_fact_id IS NOT NULL AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL
          AND ((keep_fact_id=fact_a_id AND drop_fact_id=fact_b_id) OR (keep_fact_id=fact_b_id AND drop_fact_id=fact_a_id))))
);
CREATE UNIQUE INDEX IF NOT EXISTS v3_memory_conflicts_pair ON v3_memory_conflicts(LEAST(fact_a_id,fact_b_id),GREATEST(fact_a_id,fact_b_id)) WHERE state='open';
CREATE INDEX IF NOT EXISTS v3_memory_conflicts_scope ON v3_memory_conflicts(environment_id,application_id,team_id,state,created_at DESC);
