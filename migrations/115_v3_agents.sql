-- Agent catalog and rollout state contain configuration only. No runnable
-- credential, model input, or tool payload belongs in these tables.
CREATE TABLE v3_agents (
 id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 summary text NOT NULL DEFAULT '',
 owner_id text REFERENCES users(id),
 level text NOT NULL DEFAULT 'L0' CHECK (level IN ('L0','L1','L2','L3')),
 stable_version text,
 previous_stable_version text,
 canary_version text,
 canary_percent integer NOT NULL DEFAULT 0 CHECK (canary_percent BETWEEN 0 AND 99),
 paused boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id),
 CHECK ((canary_version IS NULL AND canary_percent=0) OR
        (canary_version IS NOT NULL AND canary_percent BETWEEN 1 AND 99))
);
CREATE INDEX v3_agents_scope ON v3_agents(environment_id,application_id,id);
ALTER TABLE v3_agents ADD CONSTRAINT v3_agents_identity_scope_unique
 UNIQUE (id,application_id,environment_id);

CREATE TABLE v3_agent_versions (
 agent_id text NOT NULL REFERENCES v3_agents(id) ON DELETE CASCADE,
 version text NOT NULL CHECK (length(btrim(version)) BETWEEN 1 AND 80),
 artifact_ref text NOT NULL CHECK (length(artifact_ref) BETWEEN 1 AND 500
   AND artifact_ref ~ '^[A-Za-z0-9._:/@#-]+$'),
 created_by text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (agent_id,version)
);
ALTER TABLE v3_agents ADD CONSTRAINT v3_agent_stable_version_fk
  FOREIGN KEY (id,stable_version) REFERENCES v3_agent_versions(agent_id,version);
ALTER TABLE v3_agents ADD CONSTRAINT v3_agent_previous_version_fk
  FOREIGN KEY (id,previous_stable_version) REFERENCES v3_agent_versions(agent_id,version);
ALTER TABLE v3_agents ADD CONSTRAINT v3_agent_canary_version_fk
  FOREIGN KEY (id,canary_version) REFERENCES v3_agent_versions(agent_id,version);

-- An environment starts at L0 until an operator explicitly raises its cap.
CREATE TABLE v3_agent_environment_limits (
 environment_id text PRIMARY KEY,
 max_level text NOT NULL DEFAULT 'L0' CHECK (max_level IN ('L0','L1','L2','L3')),
 updated_by text REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE v3_agent_release_events (
 id text PRIMARY KEY,
 agent_id text NOT NULL REFERENCES v3_agents(id),
 from_version text,
 to_version text NOT NULL,
 percent integer NOT NULL CHECK (percent BETWEEN 0 AND 100),
 kind text NOT NULL CHECK (kind IN ('rollout','rollback')),
 gate_experiment_id text,
 override_reason text,
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_agent_release_events_order ON v3_agent_release_events(agent_id,created_at DESC,id DESC);

CREATE TABLE v3_agent_prompts (
 id text PRIMARY KEY,
 agent_id text NOT NULL REFERENCES v3_agents(id),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 current_version integer NOT NULL DEFAULT 0 CHECK (current_version >= 0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE v3_agent_prompt_versions (
 prompt_id text NOT NULL REFERENCES v3_agent_prompts(id) ON DELETE CASCADE,
 version integer NOT NULL CHECK (version > 0),
 body text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 20000),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (prompt_id,version)
);

-- Tool rows describe permissions; they never grant executable authority.
CREATE TABLE v3_agent_tools (
 id text PRIMARY KEY,
 agent_id text NOT NULL REFERENCES v3_agents(id),
 connector_id text REFERENCES v3_connectors(id),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 kind text NOT NULL CHECK (kind IN ('read','write')),
 risk text NOT NULL CHECK (risk IN ('low','medium','high')),
 approval_required boolean NOT NULL DEFAULT true,
 enabled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (kind='read' OR approval_required)
);

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','agents',action,'global','*'
FROM (VALUES ('view'),('change'),('manage')) permissions(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
