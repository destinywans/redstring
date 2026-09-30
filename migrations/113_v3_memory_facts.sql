-- Human-governed memory candidates. The source knowledge module is migrated
-- after numbered migrations, so source identity is checked in the writer's
-- transaction rather than with a foreign key here.
CREATE TABLE IF NOT EXISTS v3_memory_facts (
  id text PRIMARY KEY,
  environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
  application_id text NOT NULL DEFAULT '',
  team_id text NOT NULL DEFAULT '',
  source_document_id text NOT NULL,
  source_version integer NOT NULL CHECK (source_version > 0),
  source_content_hash text NOT NULL CHECK (source_content_hash <> ''),
  fact text NOT NULL CHECK (length(fact) BETWEEN 1 AND 4000),
  state text NOT NULL DEFAULT 'candidate' CHECK (state IN ('candidate','confirmed','rejected','expired')),
  created_by text NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  confirmed_by text REFERENCES users(id),
  confirmed_at timestamptz,
  decided_by text REFERENCES users(id),
  decided_at timestamptz,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  CHECK ((state='candidate' AND decided_by IS NULL AND decided_at IS NULL)
      OR (state<>'candidate' AND decided_by IS NOT NULL AND decided_at IS NOT NULL)),
  CHECK ((confirmed_by IS NULL AND confirmed_at IS NULL) OR (confirmed_by IS NOT NULL AND confirmed_at IS NOT NULL)),
  CHECK (state<>'confirmed' OR confirmed_at IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS v3_memory_facts_scope ON v3_memory_facts(environment_id,application_id,team_id,created_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS v3_memory_facts_source ON v3_memory_facts(source_document_id,source_version);
