-- One durable row per Feishu send attempt. A pending/uncertain row is never
-- retried automatically: the external API may have accepted a lost response.
CREATE TABLE v3_mfa_reminder_attempts (
 id text PRIMARY KEY,
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 target_id text NOT NULL REFERENCES users(id),
 actor_id text NOT NULL REFERENCES users(id),
 mfa_observed_at timestamptz NOT NULL,
 status text NOT NULL CHECK (status IN ('pending','accepted','uncertain','failed')),
 feishu_message_id text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 accepted_at timestamptz,
 CHECK ((status='accepted' AND feishu_message_id IS NOT NULL AND accepted_at IS NOT NULL)
     OR (status<>'accepted' AND feishu_message_id IS NULL AND accepted_at IS NULL))
);
CREATE INDEX v3_mfa_reminder_target_history ON v3_mfa_reminder_attempts
 (environment_id,target_id,created_at DESC);
CREATE INDEX v3_mfa_reminder_actor_limit ON v3_mfa_reminder_attempts
 (actor_id,created_at DESC);
