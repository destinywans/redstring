-- Git Markdown is a server-bound, manually read source. Preserve legacy
-- registrations for migration, but require resolvable Vault references for
-- new Git Markdown registrations. The application rejects legacy refs for
-- new requests rather than extending that format.
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_source_type_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_source_type_check
 CHECK (source_type IN ('confluence','notion','feishu','git','git_markdown','web','upload'));
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_secret_ref_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_secret_ref_check
 CHECK (
  (source_type = 'web' AND secret_ref = '') OR
  (source_type = 'git_markdown' AND
   secret_ref ~ '^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$' AND
   secret_ref !~ '(^|/)\.\.?(/|#|$)') OR
  (source_type <> 'git_markdown' AND secret_ref <> '' AND
   ((secret_ref ~ '^vault:[A-Za-z0-9_-]+(/[A-Za-z0-9_.-]+)+$' AND
     secret_ref !~ '(^|/)\.\.?(/|$)') OR
    (secret_ref ~ '^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$' AND
     secret_ref !~ '(^|/)\.\.?(/|#|$)')))
 );

-- Freeze the exact operator binding used at registration. A later server
-- configuration change cannot silently redirect a saved source, and old
-- reads retain the nonsecret project/branch/path that produced their SHA.
ALTER TABLE v3_knowledge_source_registrations
 ADD COLUMN binding_fingerprint text NOT NULL DEFAULT '',
 ADD COLUMN binding_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_git_binding_check
 CHECK (
  (source_type = 'git_markdown' AND binding_fingerprint ~ '^[a-f0-9]{64}$'
   AND jsonb_typeof(binding_snapshot) = 'object' AND binding_snapshot <> '{}'::jsonb) OR
  (source_type <> 'git_markdown' AND binding_fingerprint = '' AND binding_snapshot = '{}'::jsonb)
 );

-- One immutable Git commit and exact document set are attached to each
-- completed read. No vector generation is inferred from the stored bodies.
CREATE TABLE v3_git_markdown_syncs (
 read_id text PRIMARY KEY REFERENCES v3_knowledge_source_reads(id) ON DELETE RESTRICT,
 registration_id text NOT NULL REFERENCES v3_knowledge_source_registrations(id) ON DELETE RESTRICT,
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 binding_fingerprint text NOT NULL CHECK (binding_fingerprint ~ '^[a-f0-9]{64}$'),
 binding_snapshot jsonb NOT NULL CHECK (jsonb_typeof(binding_snapshot) = 'object' AND binding_snapshot <> '{}'::jsonb),
 commit_sha text NOT NULL CHECK (commit_sha ~ '^([a-f0-9]{40}|[a-f0-9]{64})$'),
 document_count integer NOT NULL CHECK (document_count BETWEEN 0 AND 32),
 indexed_chunk_count integer CHECK (indexed_chunk_count >= 0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX v3_git_markdown_syncs_registration
 ON v3_git_markdown_syncs(registration_id,created_at DESC,read_id);

-- These private Markdown bytes are isolated from legacy knowledge_documents:
-- its routes do not enforce V3 source provenance, environment and team ACL.
CREATE TABLE v3_git_markdown_documents (
 read_id text NOT NULL REFERENCES v3_git_markdown_syncs(read_id) ON DELETE RESTRICT,
 path text NOT NULL CHECK (char_length(path) BETWEEN 1 AND 1000 AND path !~ '(^/|//|(^|/)\.\.?(/|$))'),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 131072),
 item_hash text NOT NULL CHECK (item_hash ~ '^[a-f0-9]{64}$'),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 team_id text REFERENCES teams(id),
 PRIMARY KEY (read_id,path),
 UNIQUE (read_id,item_hash)
);
CREATE INDEX v3_git_markdown_documents_item
 ON v3_git_markdown_documents(item_hash,read_id);
