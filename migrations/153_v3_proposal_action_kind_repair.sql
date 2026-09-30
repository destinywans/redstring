-- Migration 143 previously dropped the cancellation kind admitted by 140.
-- Its filename is already recorded on deployed databases; add a new repair
-- rather than relying on replay of that historical file. Keep the allowlist
-- and existing immutable action/approval/execution guards unchanged.
ALTER TABLE v3_proposal_actions
 DROP CONSTRAINT v3_proposal_actions_action_kind_check;
ALTER TABLE v3_proposal_actions
 ADD CONSTRAINT v3_proposal_actions_action_kind_check
 CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel','prometheus.alert_rule.enable'));
