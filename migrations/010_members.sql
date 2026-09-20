ALTER TABLE users
  ADD COLUMN department text NOT NULL DEFAULT '',
  ADD COLUMN job_title text NOT NULL DEFAULT '',
  ADD COLUMN source text NOT NULL DEFAULT 'local',
  ADD COLUMN external_id text NOT NULL DEFAULT '',
  ADD COLUMN deleted_at timestamptz,
  ADD CONSTRAINT users_deleted_disabled CHECK(deleted_at IS NULL OR disabled),
  ADD CONSTRAINT users_external_identity CHECK((source='local' AND external_id='') OR (source<>'local' AND external_id<>''));
CREATE UNIQUE INDEX users_external_identity_unique ON users(source,external_id) WHERE source<>'local';
