-- Source configuration is separate from indexed documents. A registration
-- does not imply a successful connection, scheduled sync, or inherited ACL.
CREATE TABLE v3_knowledge_source_registrations (
 id text PRIMARY KEY,
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 application_id text REFERENCES applications(id),
 team_id text REFERENCES teams(id),
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 160),
 source_type text NOT NULL CHECK (source_type IN ('confluence','notion','feishu','git','web','upload')),
 source_url text NOT NULL CHECK (char_length(source_url) BETWEEN 1 AND 2000),
 secret_ref text NOT NULL CHECK (secret_ref ~ '^vault:[A-Za-z0-9_-]+(/[A-Za-z0-9_.-]+)+$' AND secret_ref !~ '(^|/)\.\.?(/|$)'),
 spaces jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(spaces) = 'array'),
 schedule text NOT NULL CHECK (schedule IN ('6h','24h','manual')),
 chunk text NOT NULL CHECK (chunk IN ('heading','fixed')),
 inherit_acl boolean NOT NULL,
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (team_id IS NULL OR application_id IS NOT NULL),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE INDEX v3_knowledge_source_registrations_scope
 ON v3_knowledge_source_registrations(environment_id,created_at DESC,id);
