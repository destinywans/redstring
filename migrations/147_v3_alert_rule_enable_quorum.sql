-- Preserve both independent human approvers on each alert-rule execution.
-- Earlier records without this column remain non-executable: the domain
-- requires the complete two-person proof before preparing or observing an MR.
ALTER TABLE v3_alert_rule_enable_executions
 ADD COLUMN first_approver_id text REFERENCES users(id);

ALTER TABLE v3_alert_rule_enable_executions
 ADD CONSTRAINT v3_alert_rule_enable_distinct_approvers
 CHECK (first_approver_id IS NULL OR first_approver_id <> approver_id);
