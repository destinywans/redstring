-- One-time, short-lived browser authorization-code transactions. The verifier
-- is needed only until the callback and is never returned to the browser.
CREATE TABLE v3_oidc_auth_states (
 state_hash text PRIMARY KEY,
 nonce text NOT NULL,
 code_verifier text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX v3_oidc_auth_states_expiry ON v3_oidc_auth_states(expires_at);

-- Explicit administrator-reviewed mapping; never derive account identity
-- from an ID token email or an imported member source label.
CREATE TABLE v3_oidc_identities (
 issuer_hash text NOT NULL CHECK (issuer_hash ~ '^[0-9a-f]{64}$'),
 subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 200),
 user_id text NOT NULL REFERENCES users(id),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (issuer_hash,subject),
 UNIQUE (issuer_hash,user_id)
);
