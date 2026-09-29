-- Generic V3 Push ingress is independent of the Alertmanager-specific bridge.
-- Source IDs are process configuration identities; no token or HMAC material is stored.
CREATE TABLE v3_push_source_bindings (
 source_id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 provider text NOT NULL,
 first_accepted_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE v3_push_events (
 id text PRIMARY KEY,
 source_id text NOT NULL,
 provider text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 event_type text NOT NULL CHECK (event_type IN ('alert','change','incident','resource','oncall')),
 external_event_id text NOT NULL,
 occurred_at timestamptz NOT NULL,
 subject text NOT NULL,
 title text NOT NULL,
 state text NOT NULL,
 severity text NOT NULL DEFAULT '',
 attributes jsonb NOT NULL DEFAULT '{}'::jsonb,
 synthetic boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_id,event_type,external_event_id)
);
CREATE INDEX v3_push_events_scope_time ON v3_push_events(environment_id,application_id,created_at DESC);

CREATE TABLE v3_push_idempotency (
 source_id text NOT NULL,
 event_type text NOT NULL CHECK (event_type IN ('alert','change','incident','resource','oncall')),
 key_hash text NOT NULL,
 event_id text NOT NULL REFERENCES v3_push_events(id),
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(source_id,event_type,key_hash)
);
CREATE INDEX v3_push_idempotency_expiry ON v3_push_idempotency(expires_at);

CREATE TABLE v3_push_rate_windows (
 source_id text NOT NULL,
 window_start timestamptz NOT NULL,
 count integer NOT NULL CHECK (count > 0),
 PRIMARY KEY(source_id,window_start)
);
CREATE INDEX v3_push_rate_windows_expiry ON v3_push_rate_windows(window_start);

-- Receipts contain no request body, token, signature, or clear idempotency key.
CREATE TABLE v3_push_receipts (
 id bigserial PRIMARY KEY,
 source_id text NOT NULL,
 provider text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 event_type text NOT NULL CHECK (event_type IN ('alert','change','incident','resource','oncall')),
 key_hash text NOT NULL DEFAULT '',
 code integer NOT NULL CHECK (code IN (202,409,422)),
 result text NOT NULL CHECK (result IN ('accepted','duplicate','invalid_event','test_accepted')),
 synthetic boolean NOT NULL DEFAULT false,
 latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_push_receipts_scope_time ON v3_push_receipts(environment_id,application_id,created_at DESC,id DESC);
