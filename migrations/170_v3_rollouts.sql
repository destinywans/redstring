-- PA-08 (2026-10-10): Kubernetes Deployment rollback as an approved V3
-- proposal action, executed by an independent worker with its own write
-- credential. Not executed on existing business databases.
ALTER TABLE v3_proposal_actions
 DROP CONSTRAINT v3_proposal_actions_action_kind_check;
ALTER TABLE v3_proposal_actions
 ADD CONSTRAINT v3_proposal_actions_action_kind_check
 CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel',
  'prometheus.alert_rule.enable','kubernetes.deployment.rollback'));
ALTER TABLE v3_proposal_actions ADD CONSTRAINT v3_rollback_action_shape
 CHECK (action_kind<>'kubernetes.deployment.rollback' OR
   (action->>'kind'='kubernetes.deployment.rollback' AND
    action->>'namespace' IS NOT NULL AND action->>'deployment' IS NOT NULL AND
    action->>'uid' IS NOT NULL AND
    action->>'target_template_digest' ~ '^[0-9a-f]{64}$' AND
    action->>'expected_template_digest' ~ '^[0-9a-f]{64}$'));

-- One row per approved rollback proposal: the executor's ledger. A row is
-- inserted only by the claim, after the approval proof is verified in the
-- same transaction; the write to Kubernetes happens after the claim commits.
CREATE TABLE v3_rollout_executions (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 execution_id text NOT NULL UNIQUE,
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 approval_digest text NOT NULL CHECK (approval_digest ~ '^[0-9a-f]{64}$'),
 status text NOT NULL CHECK (status IN ('reserved','dispatching','unknown','applied',
  'rolling_out','verifying','succeeded','failed','needs_attention')),
 status_reason text CHECK (status_reason IS NULL OR status_reason IN ('drift','approval_invalid',
  'authorization_changed','binding_changed','write_forbidden','write_unavailable','reconcile_unknown',
  'rollout_timeout','progress_deadline_exceeded','verification_failed','verification_unavailable',
  'target_changed')),
 timeline jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(timeline)='array'),
 rollout jsonb CHECK (rollout IS NULL OR jsonb_typeof(rollout)='object'),
 verification jsonb CHECK (verification IS NULL OR jsonb_typeof(verification)='object'),
 lease_owner text,
 lease_expires_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK ((status IN ('failed','needs_attention')) = (status_reason IS NOT NULL)),
 FOREIGN KEY (application_id, environment_id)
   REFERENCES v3_application_environments(application_id, environment_id)
);
CREATE INDEX v3_rollout_executions_active ON v3_rollout_executions(updated_at)
 WHERE status NOT IN ('succeeded','failed','needs_attention');

-- A terminal execution never changes again.
CREATE FUNCTION v3_rollout_execution_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status IN ('succeeded','failed','needs_attention') THEN
   RAISE EXCEPTION 'v3 rollout execution is terminal';
 END IF;
 IF NEW.proposal_id IS DISTINCT FROM OLD.proposal_id OR NEW.execution_id IS DISTINCT FROM OLD.execution_id
    OR NEW.action_digest IS DISTINCT FROM OLD.action_digest OR NEW.approval_digest IS DISTINCT FROM OLD.approval_digest THEN
   RAISE EXCEPTION 'v3 rollout execution identity is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_rollout_execution_terminal
 BEFORE UPDATE ON v3_rollout_executions
 FOR EACH ROW EXECUTE FUNCTION v3_rollout_execution_terminal();
CREATE TRIGGER v3_rollout_execution_no_delete
 BEFORE DELETE ON v3_rollout_executions
 FOR EACH ROW EXECUTE FUNCTION v3_rollout_execution_terminal();
