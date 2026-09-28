-- V3 Runbook presentation metadata. The authoritative content and lifecycle
-- remain in knowledge_documents, which is migrated by internal/knowledge.
-- This migration runs before module migrations, so the document reference is
-- validated by the write transaction instead of a foreign key here.
CREATE TABLE IF NOT EXISTS v3_runbooks (
  document_id text PRIMARY KEY,
  source_kind text NOT NULL CHECK (source_kind IN ('blank','template','postmortem','conversation','import')),
  source_ref text NOT NULL DEFAULT '',
  conversation_id text REFERENCES v3_conversations(id) ON DELETE RESTRICT,
  when_text text NOT NULL DEFAULT '',
  steps jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps) = 'array'),
  reviewers jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(reviewers) = 'array'),
  last_verified_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- If a local preview already created the first form of this table, preserve
-- its rows while adding the durable conversation reference.
ALTER TABLE v3_runbooks ADD COLUMN IF NOT EXISTS conversation_id text REFERENCES v3_conversations(id) ON DELETE RESTRICT;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'v3_runbooks_conversation_source_check') THEN
    ALTER TABLE v3_runbooks ADD CONSTRAINT v3_runbooks_conversation_source_check
      CHECK ((source_kind = 'conversation' AND conversation_id IS NOT NULL AND source_ref = conversation_id)
          OR (source_kind <> 'conversation' AND conversation_id IS NULL)) NOT VALID;
  END IF;
END $$;
ALTER TABLE v3_runbooks VALIDATE CONSTRAINT v3_runbooks_conversation_source_check;
CREATE INDEX IF NOT EXISTS v3_runbooks_updated_at ON v3_runbooks(updated_at DESC);
