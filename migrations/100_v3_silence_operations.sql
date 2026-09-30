-- A silence create is reserved before any Alertmanager write. Its scope and
-- payload remain historical evidence even if an application is later renamed,
-- archived, or deleted. No row here grants permission to expire a silence.
CREATE TABLE v3_silence_create_operations (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 environment_label text NOT NULL,
 application_id text NOT NULL DEFAULT '',
 application_label text NOT NULL DEFAULT '',
 owner_team_id text NOT NULL DEFAULT '',
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 payload_hash text NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
 upstream_id text UNIQUE,
 state text NOT NULL CHECK (state IN ('reserved','dispatching','unknown','acknowledged','confirmed')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,idempotency_key),
 CHECK ((application_id='' AND application_label='' AND owner_team_id='') OR
        (application_id<>'' AND application_label<>'')),
 CHECK (upstream_id IS NULL OR upstream_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 CHECK (state NOT IN ('acknowledged','confirmed') OR upstream_id IS NOT NULL)
);
CREATE INDEX v3_silence_create_scope ON v3_silence_create_operations(environment_id,application_id,created_at DESC);
