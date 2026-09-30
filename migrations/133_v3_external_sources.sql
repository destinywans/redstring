-- Keep previously saved unbound Confluence/Notion registrations visible as
-- legacy configuration. New registrations must be bound by the service, and
-- an unbound legacy row cannot be synced as content.
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_git_binding_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_content_binding_check
 CHECK (
  (source_type = 'git_markdown' AND binding_fingerprint ~ '^[a-f0-9]{64}$'
   AND jsonb_typeof(binding_snapshot) = 'object' AND binding_snapshot <> '{}'::jsonb) OR
  (source_type IN ('confluence','notion') AND
   ((binding_fingerprint = '' AND binding_snapshot = '{}'::jsonb) OR
    (binding_fingerprint ~ '^[a-f0-9]{64}$' AND jsonb_typeof(binding_snapshot) = 'object'
     AND binding_snapshot <> '{}'::jsonb))) OR
  (source_type NOT IN ('git_markdown','confluence','notion')
   AND binding_fingerprint = '' AND binding_snapshot = '{}'::jsonb)
 );

-- The read, observation, snapshot and audit commit together. Private page
-- text stays outside legacy knowledge_documents and its unrelated routes.
CREATE TABLE v3_external_source_syncs (
 read_id text PRIMARY KEY REFERENCES v3_knowledge_source_reads(id) ON DELETE RESTRICT,
 registration_id text NOT NULL REFERENCES v3_knowledge_source_registrations(id) ON DELETE RESTRICT,
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 source_type text NOT NULL CHECK (source_type IN ('confluence','notion')),
 binding_fingerprint text NOT NULL CHECK (binding_fingerprint ~ '^[a-f0-9]{64}$'),
 binding_snapshot jsonb NOT NULL CHECK (jsonb_typeof(binding_snapshot) = 'object' AND binding_snapshot <> '{}'::jsonb),
 document_count integer NOT NULL CHECK (document_count BETWEEN 0 AND 32),
 indexed_chunk_count integer CHECK (indexed_chunk_count >= 0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX v3_external_source_syncs_registration
 ON v3_external_source_syncs(registration_id,created_at DESC,read_id);

CREATE TABLE v3_external_source_documents (
 read_id text NOT NULL REFERENCES v3_external_source_syncs(read_id) ON DELETE RESTRICT,
 external_id text NOT NULL CHECK (char_length(external_id) BETWEEN 1 AND 1000),
 title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 300),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 131072),
 source_version text NOT NULL CHECK (char_length(source_version) BETWEEN 1 AND 100),
 content_version integer NOT NULL CHECK (content_version > 0),
 item_hash text NOT NULL CHECK (item_hash ~ '^[a-f0-9]{64}$'),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 team_id text REFERENCES teams(id),
 PRIMARY KEY (read_id,external_id),
 UNIQUE (read_id,item_hash)
);
CREATE INDEX v3_external_source_documents_item
 ON v3_external_source_documents(item_hash,read_id);
