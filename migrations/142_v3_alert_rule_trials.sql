-- A trial is a local, non-notifying intent. No Prometheus rule loader reads this
-- table. A separate, application-isolated evaluator must acknowledge dispatch
-- before the API reports an accepted trial.
CREATE TABLE v3_alert_rule_trials (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 application_id text NOT NULL REFERENCES applications(id),
 source_fingerprint text NOT NULL CHECK (source_fingerprint ~ '^[0-9a-f]{64}$'),
 provider_file_digest text NOT NULL CHECK (provider_file_digest ~ '^[0-9a-f]{64}$'),
 spec jsonb NOT NULL CHECK (jsonb_typeof(spec)='object' AND octet_length(spec::text)<=32768),
 status text NOT NULL DEFAULT 'dispatch_pending' CHECK
   (status IN ('dispatch_pending','queued','running','completed','inconclusive','stopped')),
 started_at timestamptz,
 expires_at timestamptz,
 next_eval_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 lease_token text NOT NULL DEFAULT '',
 lease_until timestamptz,
 active_series jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(active_series)='object'),
 evaluated_points integer NOT NULL DEFAULT 0 CHECK (evaluated_points>=0 AND evaluated_points<=1440),
 missing_points integer NOT NULL DEFAULT 0 CHECK (missing_points>=0 AND missing_points<=1440),
 would_fire integer NOT NULL DEFAULT 0 CHECK (would_fire>=0 AND would_fire<=500),
 windows jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(windows)='array' AND jsonb_array_length(windows)<=500),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,idempotency_key),
 CHECK ((started_at IS NULL AND expires_at IS NULL) OR
        (started_at IS NOT NULL AND expires_at=started_at+interval '24 hours'))
);
CREATE INDEX v3_alert_rule_trials_scope ON v3_alert_rule_trials(environment_id,application_id,created_at DESC);
CREATE UNIQUE INDEX v3_alert_rule_trials_one_active_app ON v3_alert_rule_trials(environment_id,application_id)
 WHERE status IN ('dispatch_pending','queued','running');
-- The current worker is serial and a single remote evaluation can consume its
-- entire 15-second deadline. Admit only one active trial globally. PostgreSQL
-- enforces this across API instances without a process-local capacity guess.
CREATE UNIQUE INDEX v3_alert_rule_trials_one_active_global ON v3_alert_rule_trials((true))
 WHERE status IN ('dispatch_pending','queued','running');
CREATE INDEX v3_alert_rule_trials_due ON v3_alert_rule_trials(next_eval_at,id)
 WHERE status IN ('queued','running');
