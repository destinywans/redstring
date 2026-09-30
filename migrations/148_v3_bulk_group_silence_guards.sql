-- Migration 146 may already have been applied. Add the readiness and
-- cross-proposal gates here so an upgraded database cannot silently skip them.
-- The immutable evidence row reserves both its local group and exact upstream
-- matcher scope. A later proposal for the same group or matcher is refused,
-- including after rejection or expiry, until a separately reviewed release
-- and reconciliation lifecycle is implemented.
ALTER TABLE v3_bulk_group_silence_evidence
 ADD CONSTRAINT v3_bulk_group_silence_evidence_group_unique
 UNIQUE (environment_row_id,application_id,source_id,fingerprint);
ALTER TABLE v3_bulk_group_silence_evidence
 ADD CONSTRAINT v3_bulk_group_silence_evidence_matcher_unique
 UNIQUE (endpoint_binding_fingerprint,identity_label_name,identity_label_value);

-- The generic proposal producer and bulk evidence attachment use separate
-- transactions. No vote, decision or approved state may cross that gap.
-- This trigger is a database backstop; the approval service should also call
-- the domain's read-only readiness hook before passkey work.
CREATE FUNCTION v3_bulk_group_silence_approval_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 checked_proposal_id text;
 checked_action_digest text;
 checked_action_kind text;
 checked_review_digest text;
 checked_environment_id text;
 checked_application_id text;
BEGIN
 IF TG_TABLE_NAME='v3_proposal_approval_votes' THEN
   checked_proposal_id := NEW.proposal_id;
   checked_action_digest := NEW.action_digest;
   checked_action_kind := NEW.action_kind;
   checked_review_digest := NEW.review_content_digest;
 ELSIF TG_TABLE_NAME='v3_proposal_approval_decisions' THEN
   checked_proposal_id := NEW.proposal_id;
   checked_action_digest := NEW.action_digest;
   checked_action_kind := NEW.action_kind;
   checked_review_digest := NEW.review_content_digest;
   checked_environment_id := NEW.environment_id;
   checked_application_id := NEW.application_id;
 ELSIF TG_TABLE_NAME='v3_proposals' THEN
   IF OLD.status IS NOT DISTINCT FROM NEW.status OR NEW.status<>'approved' THEN
     RETURN NEW;
   END IF;
   checked_proposal_id := NEW.id;
 ELSE
   RAISE EXCEPTION 'unsupported v3 bulk silence approval guard target';
 END IF;

 IF EXISTS (SELECT 1 FROM v3_proposal_actions a
            WHERE a.proposal_id=checked_proposal_id
              AND left(a.idempotency_key,5)='bulk-')
    AND NOT EXISTS (
      SELECT 1 FROM v3_proposal_actions a
      JOIN v3_proposals p ON p.id=a.proposal_id
      JOIN v3_bulk_group_silence_evidence e ON e.proposal_id=a.proposal_id
      WHERE a.proposal_id=checked_proposal_id
        AND a.action_kind='alertmanager.silence.create'
        AND e.action_digest=a.action_digest
        AND e.environment_id=a.environment_id AND e.environment_id=p.environment_id
        AND e.application_id=a.application_id AND e.application_id=p.application_id
        AND e.investigation_id=a.investigation_id AND e.investigation_id=p.investigation_id
        AND e.endpoint_binding_fingerprint=a.binding_fingerprint
        AND e.attestation_expires_at>clock_timestamp()+interval '1 hour'
        AND (checked_action_digest IS NULL OR checked_action_digest=a.action_digest)
        AND (checked_action_kind IS NULL OR checked_action_kind=a.action_kind)
        AND (checked_review_digest IS NULL OR checked_review_digest=a.review_content_digest)
        AND (checked_environment_id IS NULL OR checked_environment_id=e.environment_id)
        AND (checked_application_id IS NULL OR checked_application_id=e.application_id)
    ) THEN
   RAISE EXCEPTION 'bulk silence approval requires current exact-group evidence'
     USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_vote_guard
 BEFORE INSERT ON v3_proposal_approval_votes
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_approval_guard();
CREATE TRIGGER v3_bulk_group_silence_decision_guard
 BEFORE INSERT ON v3_proposal_approval_decisions
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_approval_guard();
CREATE TRIGGER v3_bulk_group_silence_status_guard
 BEFORE UPDATE OF status ON v3_proposals
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_approval_guard();
