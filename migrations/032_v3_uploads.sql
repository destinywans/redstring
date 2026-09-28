-- Small V3 attachments are stored in PostgreSQL so the metadata, content,
-- ownership and audit record commit atomically. No uploaded bytes are executed.
CREATE TABLE v3_uploads (
    id text PRIMARY KEY,
    owner_id text NOT NULL REFERENCES users(id),
    environment_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('file','image','link','paste')),
    name text NOT NULL,
    size_bytes integer NOT NULL CHECK (size_bytes BETWEEN 0 AND 20971520),
    mime text NOT NULL,
    url text NOT NULL DEFAULT '',
    content bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT v3_upload_content_kind CHECK (
        (kind IN ('file','image','paste') AND content IS NOT NULL AND url = '' AND size_bytes = octet_length(content)) OR
        (kind = 'link' AND content IS NULL AND size_bytes = 0 AND url <> '')
    )
);
CREATE INDEX v3_uploads_owner_scope ON v3_uploads(owner_id, environment_id, created_at DESC);
