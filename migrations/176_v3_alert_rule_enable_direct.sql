-- D11 decision 6: an enable proposal no longer needs an exported
-- nondeployable draft. Intent schema version 1 stays bound to its draft;
-- version 2 has no draft fields, so the draft-operation unique index (NULL
-- for version 2) does not apply to it. The action envelope stays at
-- schema_version 1.
ALTER TABLE v3_proposal_actions DROP CONSTRAINT v3_alert_rule_enable_action_shape;
ALTER TABLE v3_proposal_actions ADD CONSTRAINT v3_alert_rule_enable_action_shape
 CHECK (action_kind<>'prometheus.alert_rule.enable' OR
   (action->>'kind'='prometheus.alert_rule.enable' AND
    ((action->>'schema_version'='1' AND action->>'draft_operation_id' IS NOT NULL AND action->>'draft_id' IS NOT NULL) OR
     (action->>'schema_version'='2' AND action->>'draft_operation_id' IS NULL AND action->>'draft_id' IS NULL))));
