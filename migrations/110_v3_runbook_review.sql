-- Human review and validation-only history. These rows are separate from
-- actual Runbook use or incident resolution evidence.
CREATE TABLE v3_runbook_reviews (
 id text PRIMARY KEY,
 document_id text NOT NULL,
 document_version integer NOT NULL CHECK (document_version > 0),
 decision text NOT NULL CHECK (decision IN ('approve','request_changes')),
 reviewer_id text NOT NULL REFERENCES users(id),
 note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 4000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_runbook_reviews_document
 ON v3_runbook_reviews(document_id,created_at DESC,id);

CREATE TABLE v3_runbook_validation_previews (
 id text PRIMARY KEY,
 document_id text NOT NULL,
 document_version integer NOT NULL CHECK (document_version > 0),
 actor_id text NOT NULL REFERENCES users(id),
 step_count integer NOT NULL CHECK (step_count >= 0),
 readonly_valid boolean NOT NULL,
 checks jsonb NOT NULL CHECK (jsonb_typeof(checks)='array'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_runbook_validation_document
 ON v3_runbook_validation_previews(document_id,created_at DESC,id);
