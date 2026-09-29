-- A pre-existing session has no trustworthy issuance time. It must log in
-- again before enrolling or removing an authenticator. New sessions get one.
ALTER TABLE sessions ADD COLUMN issued_at timestamptz;
ALTER TABLE sessions ALTER COLUMN issued_at SET DEFAULT now();

-- One TOTP authenticator per human account and application instance. Pending
-- seeds are bound to the exact initiating session and selected environment.
-- Ciphertext is authenticated by the application with instance/user/generation
-- associated data; no plaintext seed or recovery code is stored here.
CREATE TABLE v3_mfa_totp (
 instance_id text NOT NULL CHECK (length(instance_id) BETWEEN 2 AND 64),
 user_id text NOT NULL REFERENCES users(id),
 generation text NOT NULL CHECK (generation ~ '^[0-9a-f]{32}$'),
 state text NOT NULL CHECK (state IN ('pending','active')),
 seed_nonce bytea NOT NULL CHECK (octet_length(seed_nonce)=12),
 seed_ciphertext bytea NOT NULL CHECK (octet_length(seed_ciphertext) BETWEEN 36 AND 128),
 key_id text NOT NULL CHECK (key_id ~ '^[0-9a-f]{32}$'),
 bound_session_hash text CHECK (bound_session_hash ~ '^[0-9a-f]{64}$'),
 bound_tier text CHECK (bound_tier IN ('nonprod','production')),
 bound_environment_id text,
 pending_expires_at timestamptz,
 last_accepted_step bigint NOT NULL DEFAULT -1 CHECK (last_accepted_step>=-1),
 failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts BETWEEN 0 AND 5),
 locked_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 activated_at timestamptz,
 PRIMARY KEY(instance_id,user_id),
 UNIQUE(instance_id,user_id,generation),
 CHECK ((state='pending' AND bound_session_hash IS NOT NULL AND bound_tier IS NOT NULL
   AND bound_environment_id IS NOT NULL AND pending_expires_at IS NOT NULL
   AND activated_at IS NULL AND last_accepted_step=-1)
  OR (state='active' AND bound_session_hash IS NULL AND bound_tier IS NULL
   AND bound_environment_id IS NULL AND pending_expires_at IS NULL
   AND activated_at IS NOT NULL))
);

CREATE TABLE v3_mfa_recovery_codes (
 instance_id text NOT NULL,
 user_id text NOT NULL,
 generation text NOT NULL,
 salt bytea NOT NULL CHECK (octet_length(salt)=16),
 code_hash bytea NOT NULL CHECK (octet_length(code_hash)=32),
 consumed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(instance_id,user_id,generation,code_hash),
 FOREIGN KEY(instance_id,user_id,generation)
   REFERENCES v3_mfa_totp(instance_id,user_id,generation) ON DELETE CASCADE
);
CREATE INDEX v3_mfa_recovery_unused ON v3_mfa_recovery_codes(instance_id,user_id,generation)
 WHERE consumed_at IS NULL;
