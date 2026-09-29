-- Feishu Docx and locally owned text uploads use the existing immutable
-- external-source read proof. Old unbound registrations remain visible as
-- configuration but cannot be synced by the bounded readers.
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_content_binding_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_content_binding_check
 CHECK (
  (source_type = 'git_markdown' AND binding_fingerprint ~ '^[a-f0-9]{64}$'
   AND jsonb_typeof(binding_snapshot) = 'object' AND binding_snapshot <> '{}'::jsonb) OR
  (source_type IN ('confluence','notion','feishu','upload') AND
   ((binding_fingerprint = '' AND binding_snapshot = '{}'::jsonb) OR
    (binding_fingerprint ~ '^[a-f0-9]{64}$' AND jsonb_typeof(binding_snapshot) = 'object'
     AND binding_snapshot <> '{}'::jsonb))) OR
  (source_type NOT IN ('git_markdown','confluence','notion','feishu','upload')
   AND binding_fingerprint = '' AND binding_snapshot = '{}'::jsonb)
 );

-- New local-upload sources carry no credential. Preserve any older upload
-- registration with a Vault reference; the service refuses to sync it.
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_secret_ref_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_secret_ref_check
 CHECK (
  (source_type IN ('web','upload') AND secret_ref = '') OR
  (source_type = 'git_markdown' AND
   secret_ref ~ '^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$' AND
   secret_ref !~ '(^|/)\.\.?(/|#|$)') OR
  (source_type <> 'git_markdown' AND secret_ref <> '' AND
   ((secret_ref ~ '^vault:[A-Za-z0-9_-]+(/[A-Za-z0-9_.-]+)+$' AND
     secret_ref !~ '(^|/)\.\.?(/|$)') OR
    (secret_ref ~ '^vault://[A-Za-z0-9_-]+/[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$' AND
     secret_ref !~ '(^|/)\.\.?(/|#|$)')))
 );

ALTER TABLE v3_external_source_syncs
 DROP CONSTRAINT v3_external_source_syncs_source_type_check;
ALTER TABLE v3_external_source_syncs
 ADD CONSTRAINT v3_external_source_syncs_source_type_check
 CHECK (source_type IN ('confluence','notion','feishu','upload'));

-- Deleting an owned upload through its audited revoke route removes every
-- private copied body. Historical reads remain audit evidence but no longer
-- satisfy the complete-document proof in auxiliary or main source lists.
ALTER TABLE v3_external_source_documents
 ADD COLUMN upload_id text REFERENCES v3_uploads(id) ON DELETE CASCADE,
 ADD COLUMN upload_mime text;
CREATE INDEX v3_external_source_documents_upload
 ON v3_external_source_documents(upload_id) WHERE upload_id IS NOT NULL;
