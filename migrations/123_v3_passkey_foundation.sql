-- A stable, opaque WebAuthn user handle is scoped to a trusted application
-- instance and RP ID. It is never derived from email, name, or the user ID.
CREATE TABLE v3_passkey_accounts (
 instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 2 AND 64),
 rp_id text NOT NULL CHECK (length(rp_id) BETWEEN 1 AND 253),
 user_id text NOT NULL REFERENCES users(id),
 user_handle bytea NOT NULL CHECK (octet_length(user_handle)=32),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (instance_id,rp_id,user_id),
 UNIQUE (instance_id,rp_id,user_handle)
);

-- credential_json contains the validated WebAuthn library credential (public
-- key and protocol metadata). The typed columns support atomic counter,
-- backup, revocation, and account-owner checks without trusting JSON alone.
CREATE TABLE v3_passkey_credentials (
 instance_id text NOT NULL,
 rp_id text NOT NULL,
 user_id text NOT NULL,
 credential_id bytea NOT NULL CHECK (octet_length(credential_id) BETWEEN 1 AND 1023),
 credential_json jsonb NOT NULL CHECK (jsonb_typeof(credential_json)='object'),
 sign_count bigint NOT NULL DEFAULT 0 CHECK (sign_count BETWEEN 0 AND 4294967295),
 backup_eligible boolean NOT NULL DEFAULT false,
 backup_state boolean NOT NULL DEFAULT false,
 clone_warning boolean NOT NULL DEFAULT false,
 label text CHECK (label IS NULL OR length(btrim(label)) BETWEEN 1 AND 100),
 created_at timestamptz NOT NULL DEFAULT now(),
 last_used_at timestamptz,
 revoked_at timestamptz,
 PRIMARY KEY (instance_id,rp_id,credential_id),
 UNIQUE (instance_id,rp_id,user_id,credential_id),
 FOREIGN KEY (instance_id,rp_id,user_id)
   REFERENCES v3_passkey_accounts(instance_id,rp_id,user_id) ON DELETE CASCADE,
 CHECK (NOT backup_state OR backup_eligible)
);
CREATE INDEX v3_passkey_credentials_active_owner
 ON v3_passkey_credentials(instance_id,rp_id,user_id,created_at)
 WHERE revoked_at IS NULL;

-- The complete WebAuthn library session data must survive between ceremony
-- start and finish. Each challenge is tied to one login session and one V3
-- environment; application code consumes it under a row lock before use.
-- No proposal proof is issued by this foundation migration.
CREATE TABLE v3_passkey_challenges (
 id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 instance_id text NOT NULL,
 rp_id text NOT NULL,
 user_id text NOT NULL,
 session_hash text NOT NULL CHECK (session_hash ~ '^[0-9a-f]{64}$'),
 tier text NOT NULL CHECK (tier IN ('nonprod','production')),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 kind text NOT NULL CHECK (kind IN ('register','verify','delete')),
 target_credential_id bytea,
 session_data jsonb NOT NULL CHECK (jsonb_typeof(session_data)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 consumed_at timestamptz,
 FOREIGN KEY (instance_id,rp_id,user_id)
   REFERENCES v3_passkey_accounts(instance_id,rp_id,user_id) ON DELETE CASCADE,
 FOREIGN KEY (instance_id,rp_id,user_id,target_credential_id)
   REFERENCES v3_passkey_credentials(instance_id,rp_id,user_id,credential_id),
 CHECK ((kind='delete')=(target_credential_id IS NOT NULL)),
 CHECK (expires_at>created_at AND expires_at<=created_at+interval '10 minutes'),
 CHECK (consumed_at IS NULL OR consumed_at>=created_at)
);
CREATE INDEX v3_passkey_challenges_pending_scope
 ON v3_passkey_challenges(instance_id,rp_id,user_id,session_hash,expires_at)
 WHERE consumed_at IS NULL;
CREATE INDEX v3_passkey_challenges_expiry ON v3_passkey_challenges(expires_at);
CREATE INDEX v3_passkey_challenges_owner_created
 ON v3_passkey_challenges(instance_id,rp_id,user_id,created_at);
