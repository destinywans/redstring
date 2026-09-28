-- A read run proves only what a configured source-specific adapter observed.
-- It does not imply successful indexing or that a scheduler exists.
CREATE TABLE v3_knowledge_source_reads (
 id text PRIMARY KEY,
 registration_id text NOT NULL REFERENCES v3_knowledge_source_registrations(id) ON DELETE RESTRICT,
 environment_id text NOT NULL,
 observed_count integer NOT NULL CHECK (observed_count BETWEEN 0 AND 500),
 reader_type text NOT NULL,
 observed_at timestamptz NOT NULL DEFAULT now(),
 requested_by text NOT NULL REFERENCES users(id)
);
CREATE INDEX v3_knowledge_source_reads_latest
 ON v3_knowledge_source_reads(registration_id,observed_at DESC,id DESC);
