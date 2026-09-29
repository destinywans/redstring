-- Public Web Markdown bodies stay outside legacy knowledge_documents. Legacy
-- knowledge read/export/edit routes do not enforce V3 source provenance or
-- environment/team grants, so only the V3 source search path may read these.
CREATE TABLE v3_public_web_documents (
 registration_id text PRIMARY KEY REFERENCES v3_knowledge_source_registrations(id) ON DELETE RESTRICT,
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 team_id text REFERENCES teams(id),
 title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
 source_url text NOT NULL CHECK (char_length(source_url) BETWEEN 1 AND 1000),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 131072),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 item_hash text NOT NULL CHECK (item_hash ~ '^[a-f0-9]{64}$'),
 version integer NOT NULL CHECK (version > 0),
 last_read_id text NOT NULL REFERENCES v3_knowledge_source_reads(id) ON DELETE RESTRICT,
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY (application_id,environment_id)
  REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE INDEX v3_public_web_documents_scope
 ON v3_public_web_documents(application_id,environment_id,registration_id);

-- Revisions retain exact fetched bytes, provenance and actor. No vector
-- generation or chunk count is inferred from a keyword-searchable document.
CREATE TABLE v3_public_web_document_revisions (
 registration_id text NOT NULL REFERENCES v3_public_web_documents(registration_id) ON DELETE RESTRICT,
 version integer NOT NULL CHECK (version > 0),
 body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 131072),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 read_id text NOT NULL REFERENCES v3_knowledge_source_reads(id) ON DELETE RESTRICT,
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (registration_id,version)
);
