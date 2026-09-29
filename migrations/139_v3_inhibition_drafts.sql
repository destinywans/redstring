-- An inhibition save reserves a scoped, versioned review proposal before
-- opening a GitLab MR. This ledger never represents a running suppression.
CREATE TABLE v3_inhibition_drafts (
 id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{32}$'),
 actor_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 application_id text NOT NULL CHECK (application_id <> ''),
 source_revision text NOT NULL CHECK (source_revision ~ '^sha256:[a-f0-9]{64}$'),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
 branch text NOT NULL UNIQUE CHECK (branch <> ''),
 target_branch text NOT NULL CHECK (target_branch <> ''),
 path text NOT NULL UNIQUE CHECK (path <> ''),
 project_id bigint NOT NULL CHECK (project_id > 0),
 mr_iid bigint CHECK (mr_iid > 0),
 mr_url text NOT NULL DEFAULT '',
 state text NOT NULL CHECK (state IN ('reserved','exporting','uncertain','exported')),
 attempt_generation bigint NOT NULL DEFAULT 0 CHECK (attempt_generation >= 0),
 lease_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (actor_id,idempotency_key),
 CHECK (mr_iid IS NULL OR mr_url <> ''),
 CHECK (state <> 'exported' OR mr_iid IS NOT NULL),
 CHECK (state <> 'exported' OR lease_until IS NULL)
);
CREATE INDEX v3_inhibition_drafts_scope
 ON v3_inhibition_drafts(environment_id,application_id,created_at DESC);
CREATE INDEX v3_inhibition_drafts_actor_scope
 ON v3_inhibition_drafts(actor_id,environment_id,created_at DESC);
CREATE UNIQUE INDEX v3_inhibition_drafts_mr_identity
 ON v3_inhibition_drafts(project_id,mr_iid) WHERE mr_iid IS NOT NULL;
