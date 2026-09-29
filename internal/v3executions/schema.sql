-- Run after actions.PostgresRepository.Migrate, which owns both references.
-- Publication is local evidence only; it does not post to an external system.
CREATE TABLE IF NOT EXISTS v3_execution_report_publications (
 execution_id text PRIMARY KEY REFERENCES executions(id) ON DELETE RESTRICT,
 investigation_id text NOT NULL UNIQUE
  REFERENCES investigation_reports(investigation_id) ON DELETE RESTRICT,
 report_digest text NOT NULL CHECK (report_digest ~ '^[0-9a-f]{64}$'),
 published_by text NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
 published_at timestamptz NOT NULL DEFAULT now()
);
