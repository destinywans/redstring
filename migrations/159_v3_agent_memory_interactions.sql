-- Catalog registration does not grant a tool to any Agent or runtime.
CREATE TABLE v3_agent_tool_catalog (
 id text PRIMARY KEY, environment_id text NOT NULL, connector_id text NOT NULL,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 160), kind text NOT NULL CHECK(kind IN ('read','write')),
 created_by text NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(connector_id,environment_id) REFERENCES v3_connectors(id,environment_id),
 UNIQUE(environment_id,connector_id,name,kind)
);
CREATE TABLE v3_eval_datasets (
 id text PRIMARY KEY, environment_id text NOT NULL, name text NOT NULL CHECK(length(name) BETWEEN 1 AND 160),
 source text NOT NULL CHECK(source IN ('incidents','runs','manual')),
 created_by text NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE v3_eval_dataset_items (
 dataset_id text NOT NULL REFERENCES v3_eval_datasets(id), run_id text NOT NULL REFERENCES v3_agent_runs(id),
 created_by text NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,run_id)
);
CREATE TABLE v3_agent_run_annotations (
 id text PRIMARY KEY, run_id text NOT NULL REFERENCES v3_agent_runs(id), actor_id text NOT NULL REFERENCES users(id),
 score integer NOT NULL CHECK(score BETWEEN 1 AND 5), note text NOT NULL DEFAULT '' CHECK(length(note)<=2000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE v3_memory_policies (
 environment_id text PRIMARY KEY, ttl_days integer NOT NULL CHECK(ttl_days IN (30,90,180)),
 proposers text NOT NULL CHECK(proposers IN ('human','human_ai')), revision bigint NOT NULL CHECK(revision>0),
 updated_by text NOT NULL REFERENCES users(id), updated_at timestamptz NOT NULL DEFAULT now()
);
