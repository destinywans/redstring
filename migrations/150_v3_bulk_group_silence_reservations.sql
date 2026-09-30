-- Historical bulk evidence remains immutable. Only an active reservation
-- excludes another proposal for the same local group or upstream matcher.
-- A released reservation remains as an audit record beside its evidence.
CREATE TABLE v3_bulk_group_silence_reservations (
 proposal_id text PRIMARY KEY REFERENCES v3_bulk_group_silence_evidence(proposal_id),
 reservation_seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
 environment_row_id text NOT NULL,
 application_id text NOT NULL,
 source_id text NOT NULL,
 fingerprint text NOT NULL,
 endpoint_binding_fingerprint text NOT NULL,
 identity_label_name text NOT NULL,
 identity_label_value text NOT NULL,
 created_at timestamptz NOT NULL,
 released_at timestamptz,
 release_reason text CHECK (release_reason IN ('rejected','expired')),
 released_by text REFERENCES users(id),
 CHECK ((released_at IS NULL AND release_reason IS NULL AND released_by IS NULL)
     OR (released_at IS NOT NULL AND release_reason IS NOT NULL AND released_by IS NOT NULL
         AND released_at>=created_at))
);
CREATE UNIQUE INDEX v3_bulk_group_silence_reservation_active_group
 ON v3_bulk_group_silence_reservations
 (environment_row_id,application_id,source_id,fingerprint)
 WHERE released_at IS NULL;
CREATE UNIQUE INDEX v3_bulk_group_silence_reservation_active_matcher
 ON v3_bulk_group_silence_reservations
 (endpoint_binding_fingerprint,identity_label_name,identity_label_value)
 WHERE released_at IS NULL;
CREATE INDEX v3_bulk_group_silence_reservation_released_group
 ON v3_bulk_group_silence_reservations
 (environment_row_id,application_id,source_id,fingerprint,reservation_seq DESC)
 WHERE released_at IS NOT NULL;

-- Migration 148's permanent unique constraints guarantee this backfill is
-- collision-free. The migration runner applies all statements in one tx.
INSERT INTO v3_bulk_group_silence_reservations (
 proposal_id,environment_row_id,application_id,source_id,fingerprint,
 endpoint_binding_fingerprint,identity_label_name,identity_label_value,created_at
)
SELECT proposal_id,environment_row_id,application_id,source_id,fingerprint,
       endpoint_binding_fingerprint,identity_label_name,identity_label_value,created_at
FROM v3_bulk_group_silence_evidence;

CREATE FUNCTION v3_bulk_group_silence_reservation_insert_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (
   SELECT 1 FROM v3_bulk_group_silence_evidence e
   WHERE e.proposal_id=NEW.proposal_id
     AND e.environment_row_id=NEW.environment_row_id
     AND e.application_id=NEW.application_id
     AND e.source_id=NEW.source_id
     AND e.fingerprint=NEW.fingerprint
     AND e.endpoint_binding_fingerprint=NEW.endpoint_binding_fingerprint
     AND e.identity_label_name=NEW.identity_label_name
     AND e.identity_label_value=NEW.identity_label_value
     AND e.created_at=NEW.created_at
 ) OR NEW.released_at IS NOT NULL
   OR NEW.release_reason IS NOT NULL OR NEW.released_by IS NOT NULL THEN
   RAISE EXCEPTION 'bulk silence reservation must match immutable evidence and start active'
     USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_reservation_insert_guard
 BEFORE INSERT ON v3_bulk_group_silence_reservations
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_reservation_insert_guard();

-- Evidence and its active reservation are committed or rolled back together.
CREATE FUNCTION v3_bulk_group_silence_reserve_on_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO v3_bulk_group_silence_reservations (
   proposal_id,environment_row_id,application_id,source_id,fingerprint,
   endpoint_binding_fingerprint,identity_label_name,identity_label_value,created_at
 ) VALUES (
   NEW.proposal_id,NEW.environment_row_id,NEW.application_id,NEW.source_id,NEW.fingerprint,
   NEW.endpoint_binding_fingerprint,NEW.identity_label_name,NEW.identity_label_value,NEW.created_at
 );
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_reserve_on_evidence
 AFTER INSERT ON v3_bulk_group_silence_evidence
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_reserve_on_evidence();

-- The reservation itself is append-only except for a single terminal release.
-- Callers lock the proposal before updating this row. The guard reads that
-- locked state and rejects release after any approval or possible send.
CREATE FUNCTION v3_bulk_group_silence_reservation_release_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 proposal_status text;
 evidence_expires_at timestamptz;
 action_creator_id text;
 action_idempotency_key text;
BEGIN
 IF TG_OP='DELETE' THEN
   RAISE EXCEPTION 'v3 bulk group silence reservation cannot be deleted'
     USING ERRCODE='23514';
 END IF;
 IF OLD.released_at IS NOT NULL
    OR NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
    OR NEW.reservation_seq IS DISTINCT FROM OLD.reservation_seq
    OR NEW.environment_row_id IS DISTINCT FROM OLD.environment_row_id
    OR NEW.application_id IS DISTINCT FROM OLD.application_id
    OR NEW.source_id IS DISTINCT FROM OLD.source_id
    OR NEW.fingerprint IS DISTINCT FROM OLD.fingerprint
    OR NEW.endpoint_binding_fingerprint IS DISTINCT FROM OLD.endpoint_binding_fingerprint
    OR NEW.identity_label_name IS DISTINCT FROM OLD.identity_label_name
    OR NEW.identity_label_value IS DISTINCT FROM OLD.identity_label_value
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.released_at IS NULL THEN
   RAISE EXCEPTION 'v3 bulk group silence reservation identity and release are immutable'
     USING ERRCODE='23514';
 END IF;

 SELECT p.status,e.attestation_expires_at,a.creator_id,a.idempotency_key
   INTO proposal_status,evidence_expires_at,action_creator_id,action_idempotency_key
 FROM v3_proposals p
 JOIN v3_bulk_group_silence_evidence e ON e.proposal_id=p.id
 JOIN v3_proposal_actions a ON a.proposal_id=p.id
 WHERE p.id=OLD.proposal_id
   AND a.action_kind='alertmanager.silence.create'
   AND left(a.idempotency_key,5)='bulk-';
 IF NOT FOUND OR NOT (
      proposal_status='rejected' AND NEW.release_reason='rejected'
   OR proposal_status='pending' AND evidence_expires_at<=clock_timestamp()
      AND NEW.release_reason='expired'
 ) THEN
   RAISE EXCEPTION 'bulk silence reservation requires rejected or expired unexecuted proposal'
     USING ERRCODE='23514';
 END IF;
 IF EXISTS (SELECT 1 FROM v3_proposal_approval_decisions d
            WHERE d.proposal_id=OLD.proposal_id)
    OR EXISTS (SELECT 1 FROM v3_silence_execution_claims c
               WHERE c.proposal_id=OLD.proposal_id)
    OR EXISTS (SELECT 1 FROM v3_silence_create_operations o
               WHERE o.actor_id=action_creator_id
                 AND o.idempotency_key=action_idempotency_key) THEN
   RAISE EXCEPTION 'bulk silence reservation cannot be released after approval or possible send'
     USING ERRCODE='23514';
 END IF;
 NEW.released_at := clock_timestamp();
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_reservation_release_guard
 BEFORE UPDATE OR DELETE ON v3_bulk_group_silence_reservations
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_reservation_release_guard();

-- Lock the active row while each approval or dispatch marker is inserted.
-- A release waits for that insert to commit and then sees its durable marker;
-- conversely an insert after release fails even though evidence is retained.
CREATE FUNCTION v3_bulk_group_silence_require_active_reservation(checked_proposal_id text)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM v3_proposal_actions a
                WHERE a.proposal_id=checked_proposal_id
                  AND left(a.idempotency_key,5)='bulk-') THEN
   RETURN;
 END IF;
 PERFORM 1 FROM v3_bulk_group_silence_reservations r
 WHERE r.proposal_id=checked_proposal_id AND r.released_at IS NULL
 FOR SHARE;
 IF NOT FOUND THEN
   RAISE EXCEPTION 'bulk silence approval or execution requires active reservation'
     USING ERRCODE='23514';
 END IF;
END $$;

CREATE FUNCTION v3_bulk_group_silence_active_approval_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='v3_proposals' THEN
   IF OLD.status IS DISTINCT FROM NEW.status AND NEW.status='approved' THEN
     PERFORM v3_bulk_group_silence_require_active_reservation(NEW.id);
   END IF;
 ELSE
   PERFORM v3_bulk_group_silence_require_active_reservation(NEW.proposal_id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_active_vote_guard
 BEFORE INSERT ON v3_proposal_approval_votes
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_active_approval_guard();
CREATE TRIGGER v3_bulk_group_silence_active_decision_guard
 BEFORE INSERT ON v3_proposal_approval_decisions
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_active_approval_guard();
CREATE TRIGGER v3_bulk_group_silence_active_status_guard
 BEFORE UPDATE OF status ON v3_proposals
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_active_approval_guard();
CREATE TRIGGER v3_bulk_group_silence_active_claim_guard
 BEFORE INSERT ON v3_silence_execution_claims
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_active_approval_guard();

-- Create operations have no proposal_id; the immutable action's actor/key
-- identifies any operation that might be sent for a bulk proposal.
CREATE FUNCTION v3_bulk_group_silence_active_operation_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 checked_proposal_id text;
BEGIN
 SELECT a.proposal_id INTO checked_proposal_id
 FROM v3_proposal_actions a
 WHERE a.creator_id=NEW.actor_id AND a.idempotency_key=NEW.idempotency_key
   AND left(a.idempotency_key,5)='bulk-';
 IF FOUND THEN
   PERFORM v3_bulk_group_silence_require_active_reservation(checked_proposal_id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_bulk_group_silence_active_operation_guard
 BEFORE INSERT ON v3_silence_create_operations
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_active_operation_guard();

-- Historical evidence no longer owns uniqueness. The partial active indexes
-- above continue to reject simultaneous group or matcher reservations.
ALTER TABLE v3_bulk_group_silence_evidence
 DROP CONSTRAINT v3_bulk_group_silence_evidence_group_unique;
ALTER TABLE v3_bulk_group_silence_evidence
 DROP CONSTRAINT v3_bulk_group_silence_evidence_matcher_unique;
