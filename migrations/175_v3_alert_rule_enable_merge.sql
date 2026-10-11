-- D11: the executor merges a file_pull deployment MR itself. merged is the
-- approved head on the protected target; failed is a loaded rule whose
-- evaluation errors (it runs, and only a disable removes it). failure_code
-- names why an execution needs attention, readback keeps the last Prometheus
-- readback and steps the state timeline. Earlier rows keep their states and
-- get an empty timeline.
ALTER TABLE v3_alert_rule_enable_executions
 DROP CONSTRAINT v3_alert_rule_enable_executions_state_check;
ALTER TABLE v3_alert_rule_enable_executions
 ADD CONSTRAINT v3_alert_rule_enable_executions_state_check CHECK (state IN
   ('reserved','exporting','uncertain','pending_review','merged','awaiting_provider','enabled','failed','stale_remote'));

ALTER TABLE v3_alert_rule_enable_executions
 ADD COLUMN failure_code text NOT NULL DEFAULT '' CHECK (failure_code ~ '^[a-z_]{0,64}$'),
 ADD COLUMN readback jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(readback)='object'),
 ADD COLUMN steps jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps)='array' AND jsonb_array_length(steps)<=64);
