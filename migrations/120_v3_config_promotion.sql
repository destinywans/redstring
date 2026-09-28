-- Configuration promotion is an explicit, replayable operation. The export
-- payload contains only the allowlisted template structure, never runtime
-- evidence or credentials. A reserved export is not evidence of a GitLab MR.
CREATE TABLE v3_config_exports (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind='template'),
 object_id text NOT NULL,
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'prod' AND environment_id<>'all'),
 source_digest text NOT NULL CHECK (source_digest ~ '^[0-9a-f]{64}$'),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
 branch text NOT NULL UNIQUE,
 path text NOT NULL UNIQUE,
 project_id bigint NOT NULL CHECK (project_id>0),
 mr_iid bigint CHECK (mr_iid>0),
 mr_url text NOT NULL DEFAULT '',
 state text NOT NULL CHECK (state IN ('reserved','exporting','uncertain','exported')),
 lease_until timestamptz,
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(kind,object_id,environment_id,source_digest)
);
CREATE INDEX v3_config_exports_actor_scope ON v3_config_exports(actor_id,environment_id,created_at DESC);

-- Production imports bind the exact reviewed GitLab MR and merged commit to
-- one local object. They never copy a lower-environment execution record.
CREATE TABLE v3_config_imports (
 id text PRIMARY KEY,
 project_id bigint NOT NULL CHECK (project_id>0),
 mr_iid bigint NOT NULL CHECK (mr_iid>0),
 merge_sha text NOT NULL CHECK (merge_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
 promotion_id text NOT NULL,
 kind text NOT NULL CHECK (kind='template'),
 target_id text NOT NULL UNIQUE,
 actor_id text NOT NULL REFERENCES users(id),
 imported_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(project_id,mr_iid),
 UNIQUE(project_id,promotion_id)
);
