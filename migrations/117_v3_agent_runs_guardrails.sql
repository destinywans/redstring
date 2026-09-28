-- Only metadata from a trusted in-process producer may enter runs and spans.
-- Raw model messages, tool arguments/results, and private retrieved content
-- must never be stored here or in management_audit.
CREATE TABLE v3_agent_runs (
 id text PRIMARY KEY,
 agent_id text NOT NULL,
 version text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 trigger_kind text NOT NULL CHECK (trigger_kind IN ('manual','alert','scheduled','api')),
 status text NOT NULL CHECK (status IN ('running','success','failed','blocked')),
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 token_input integer CHECK (token_input >= 0),
 token_output integer CHECK (token_output >= 0),
 error_code text CHECK (error_code IS NULL OR error_code ~ '^[A-Za-z0-9._-]{1,80}$'),
 FOREIGN KEY (agent_id,version) REFERENCES v3_agent_versions(agent_id,version),
 FOREIGN KEY (agent_id,application_id,environment_id)
   REFERENCES v3_agents(id,application_id,environment_id),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id),
 CHECK ((status='running' AND finished_at IS NULL) OR
        (status<>'running' AND finished_at IS NOT NULL))
);
CREATE INDEX v3_agent_runs_scope ON v3_agent_runs(environment_id,application_id,started_at DESC,id DESC);
CREATE TABLE v3_agent_run_spans (
 id text PRIMARY KEY,
 run_id text NOT NULL REFERENCES v3_agent_runs(id) ON DELETE CASCADE,
 parent_id text,
 kind text NOT NULL CHECK (kind IN ('agent','chain','llm','tool','retriever','guardrail','output')),
 name text NOT NULL CHECK (name ~ '^[A-Za-z0-9._:/@#-]{1,160}$'),
 started_at timestamptz NOT NULL,
 duration_ms integer CHECK (duration_ms >= 0),
 status text NOT NULL CHECK (status IN ('ok','failed','blocked','unknown')),
 model_id text,
 tool_id text CHECK (tool_id IS NULL OR tool_id ~ '^[A-Za-z0-9._-]{1,160}$'),
 token_input integer CHECK (token_input >= 0),
 token_output integer CHECK (token_output >= 0),
 FOREIGN KEY (run_id,parent_id) REFERENCES v3_agent_run_spans(run_id,id),
 UNIQUE (run_id,id)
);
CREATE INDEX v3_agent_run_spans_order ON v3_agent_run_spans(run_id,started_at,id);
CREATE TABLE v3_agent_run_feedback (
 run_id text NOT NULL REFERENCES v3_agent_runs(id) ON DELETE CASCADE,
 actor_id text NOT NULL REFERENCES users(id),
 vote text NOT NULL CHECK (vote IN ('up','down')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (run_id,actor_id)
);

CREATE TABLE v3_agent_guard_policies (
 id text PRIMARY KEY,
 environment_id text NOT NULL,
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 description text NOT NULL DEFAULT '' CHECK (length(description)<=1000),
 forced boolean NOT NULL DEFAULT true,
 enabled boolean NOT NULL DEFAULT true,
 updated_by text REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (NOT forced OR enabled)
);
CREATE INDEX v3_agent_guard_policies_scope ON v3_agent_guard_policies(environment_id,id);
CREATE TABLE v3_agent_guard_events (
 id text PRIMARY KEY,
 run_id text NOT NULL REFERENCES v3_agent_runs(id),
 policy_id text NOT NULL REFERENCES v3_agent_guard_policies(id),
 action text NOT NULL CHECK (action IN ('blocked','masked')),
 target_kind text NOT NULL CHECK (target_kind IN ('tool','output','input','retrieval')),
 reason_code text NOT NULL CHECK (reason_code ~ '^[A-Za-z0-9._-]{1,80}$'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_agent_guard_events_order ON v3_agent_guard_events(run_id,created_at DESC,id DESC);
