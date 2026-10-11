-- D11 decision 7: a disable removes an active rule through the same
-- approval and executor as an enable. Its intent has no draft (schema
-- version 2) and names the observed rule it removes. disabled is the
-- executor's readback that the rule is gone.
ALTER TABLE v3_proposal_actions
 DROP CONSTRAINT v3_proposal_actions_action_kind_check;
ALTER TABLE v3_proposal_actions
 ADD CONSTRAINT v3_proposal_actions_action_kind_check
 CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel',
  'prometheus.alert_rule.enable','prometheus.alert_rule.disable','kubernetes.deployment.rollback'));
ALTER TABLE v3_proposal_actions ADD CONSTRAINT v3_alert_rule_disable_action_shape
 CHECK (action_kind<>'prometheus.alert_rule.disable' OR
   (action->>'kind'='prometheus.alert_rule.disable' AND action->>'schema_version'='2' AND
    action->>'draft_operation_id' IS NULL AND action->>'draft_id' IS NULL AND
    action->>'observed_rule_id' IS NOT NULL AND action->>'observed_digest' IS NOT NULL));

ALTER TABLE v3_alert_rule_enable_executions
 DROP CONSTRAINT v3_alert_rule_enable_executions_state_check;
ALTER TABLE v3_alert_rule_enable_executions
 ADD CONSTRAINT v3_alert_rule_enable_executions_state_check CHECK (state IN
   ('reserved','exporting','uncertain','pending_review','merged','awaiting_provider','enabled','disabled','failed','stale_remote'));
