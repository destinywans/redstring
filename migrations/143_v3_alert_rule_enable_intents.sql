-- An enabled-mode request creates a human-reviewable action snapshot. The
-- existing proposal state remains pending/rejected; approval and execution
-- belong to the separate WAN-117 authority and executor.
ALTER TABLE v3_alert_rule_draft_operations
 DROP CONSTRAINT v3_alert_rule_draft_operations_operation_check;
ALTER TABLE v3_alert_rule_draft_operations
 ADD CONSTRAINT v3_alert_rule_draft_operations_operation_check
 CHECK (operation IN ('create','update','import','enable'));

ALTER TABLE v3_proposal_actions
 DROP CONSTRAINT v3_proposal_actions_action_kind_check;
ALTER TABLE v3_proposal_actions
 ADD CONSTRAINT v3_proposal_actions_action_kind_check
 CHECK (action_kind IN ('alertmanager.silence.create','prometheus.alert_rule.enable'));

-- One proposal is bound to exactly one confirmed draft export. An immutable
-- proposal action (migration 102) carries the full review and source digests.
CREATE UNIQUE INDEX v3_alert_rule_enable_operation
 ON v3_proposal_actions ((action->>'draft_operation_id'))
 WHERE action_kind='prometheus.alert_rule.enable';

ALTER TABLE v3_proposal_actions ADD CONSTRAINT v3_alert_rule_enable_action_shape
 CHECK (action_kind<>'prometheus.alert_rule.enable' OR
   (action->>'kind'='prometheus.alert_rule.enable' AND
    action->>'draft_operation_id' IS NOT NULL AND
    action->>'draft_id' IS NOT NULL));

-- The independent executor reserves one immutable remote operation identity.
-- A lost response is reconciled against that identity before retry. A pending
-- deployment MR is never equivalent to an enabled Prometheus rule.
CREATE TABLE v3_alert_rule_enable_executions (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 execution_id text NOT NULL UNIQUE,
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 approval_id text NOT NULL,
 approval_digest text NOT NULL CHECK (approval_digest ~ '^[0-9a-f]{64}$'),
 approver_id text NOT NULL REFERENCES users(id),
 state text NOT NULL CHECK (state IN
   ('reserved','exporting','uncertain','pending_review','awaiting_provider','enabled','stale_remote')),
 generation bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 deployment jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(deployment)='object'),
 observed_rule_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
