-- The read ledger can identify four signed configuration kinds. Writers and
-- production import remain separately gated; this migration creates neither.
ALTER TABLE v3_config_exports DROP CONSTRAINT v3_config_exports_kind_check;
ALTER TABLE v3_config_exports ADD CONSTRAINT v3_config_exports_kind_check
 CHECK (kind IN ('template','runbook','alert_rule','agent_version'));
ALTER TABLE v3_config_imports DROP CONSTRAINT v3_config_imports_kind_check;
ALTER TABLE v3_config_imports ADD CONSTRAINT v3_config_imports_kind_check
 CHECK (kind IN ('template','runbook','alert_rule','agent_version'));

ALTER TABLE v3_config_exports ADD COLUMN application_id text;
ALTER TABLE v3_config_exports ADD COLUMN source_version text;
ALTER TABLE v3_config_exports ADD CONSTRAINT v3_config_exports_read_scope_check
 CHECK (kind='template' OR
   (application_id IS NOT NULL AND length(btrim(application_id)) BETWEEN 1 AND 200
    AND source_version IS NOT NULL AND length(btrim(source_version)) BETWEEN 1 AND 100));
