-- Upload source revocation deletes the original private bytes. Keep only a
-- scoped, non-content tombstone for audit and never resolve it as an upload.
CREATE TABLE v3_upload_revocations (
 id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{32}$'),
 owner_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 revoked_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX v3_upload_revocations_owner_scope
 ON v3_upload_revocations(owner_id,environment_id,revoked_at DESC);
