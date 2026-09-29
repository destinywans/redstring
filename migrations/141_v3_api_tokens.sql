-- Personal V3 API token records. The plaintext is returned once by the issuer
-- and is never persisted. A 256-bit random token permits an unkeyed SHA-256
-- lookup digest without making an offline dictionary attack practical.
CREATE TABLE v3_api_tokens (
 id text PRIMARY KEY,
 owner_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80 AND octet_length(name) <= 200 AND btrim(name) = name),
 scopes text[] NOT NULL CHECK (
   cardinality(scopes) BETWEEN 1 AND 6
   AND array_position(scopes, NULL) IS NULL
   AND scopes <@ ARRAY['services:view','events:view','connectors:view',
     'resources:view','logs:view','traces:view']::text[]
 ),
 secret_hash bytea NOT NULL UNIQUE CHECK (octet_length(secret_hash) = 32),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 CHECK (expires_at > created_at AND expires_at <= created_at + interval '30 days')
);
CREATE INDEX v3_api_tokens_owner_environment
 ON v3_api_tokens(owner_id, environment_id, created_at DESC, id DESC);
CREATE INDEX v3_api_tokens_active_expiry ON v3_api_tokens(expires_at)
 WHERE revoked_at IS NULL;
