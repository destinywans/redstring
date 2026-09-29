-- Preserve the identity and digest of the source observed when a V3 Runbook
-- is saved. Historical rows predate this observation and retain {}.
-- Source text stays in its governed source table, not in this metadata column.
ALTER TABLE v3_runbooks
 ADD COLUMN source_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb
 CHECK (jsonb_typeof(source_snapshot) = 'object'
    AND octet_length(source_snapshot::text) <= 2048);
