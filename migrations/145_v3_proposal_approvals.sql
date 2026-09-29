-- Approval is available only for immutable, digest-verified action proposals.
-- Existing display-only proposals are not upgraded into executable actions.
ALTER TABLE v3_proposals DROP CONSTRAINT v3_proposals_status_check;
ALTER TABLE v3_proposals DROP CONSTRAINT v3_proposals_check;
ALTER TABLE v3_proposals ADD CONSTRAINT v3_proposals_status_check
 CHECK (status IN ('pending','rejected','approved'));
ALTER TABLE v3_proposals ADD CONSTRAINT v3_proposals_decision_check
 CHECK ((status='pending' AND rejected_by IS NULL AND rejected_at IS NULL)
     OR (status='rejected' AND rejected_by IS NOT NULL AND rejected_at IS NOT NULL)
     OR (status='approved' AND rejected_by IS NULL AND rejected_at IS NULL));

ALTER TABLE v3_passkey_challenges DROP CONSTRAINT v3_passkey_challenges_kind_check;
ALTER TABLE v3_passkey_challenges ADD CONSTRAINT v3_passkey_challenges_kind_check
 CHECK (kind IN ('register','verify','delete','proposal_approve'));
ALTER TABLE v3_passkey_challenges ADD COLUMN proposal_id text REFERENCES v3_proposals(id);
ALTER TABLE v3_passkey_challenges ADD COLUMN action_kind text;
ALTER TABLE v3_passkey_challenges ADD COLUMN action_digest text;
ALTER TABLE v3_passkey_challenges ADD COLUMN review_content_digest text;
ALTER TABLE v3_passkey_challenges ADD CONSTRAINT v3_passkey_challenges_proposal_binding_check
 CHECK ((kind='proposal_approve' AND proposal_id IS NOT NULL AND action_kind IS NOT NULL
         AND action_kind<>'' AND action_digest ~ '^[0-9a-f]{64}$'
         AND review_content_digest ~ '^[0-9a-f]{64}$' AND target_credential_id IS NULL)
     OR (kind<>'proposal_approve' AND proposal_id IS NULL AND action_kind IS NULL
         AND action_digest IS NULL AND review_content_digest IS NULL));

-- Each verified assertion is retained as one immutable vote. A new assertion
-- can refresh a vote after expiry, but a challenge cannot be reused.
CREATE TABLE v3_proposal_approval_votes (
 id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{32}$'),
 proposal_id text NOT NULL REFERENCES v3_proposals(id),
 approver_id text NOT NULL REFERENCES users(id),
 challenge_id text NOT NULL UNIQUE REFERENCES v3_passkey_challenges(id),
 action_kind text NOT NULL,
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 review_content_digest text NOT NULL CHECK (review_content_digest ~ '^[0-9a-f]{64}$'),
 instance_id text NOT NULL,
 rp_id text NOT NULL,
 credential_id bytea NOT NULL CHECK (octet_length(credential_id) BETWEEN 1 AND 1023),
 credential_fingerprint text NOT NULL CHECK (credential_fingerprint ~ '^[0-9a-f]{16}$'),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 comment text NOT NULL DEFAULT '' CHECK (length(comment)<=1000),
 verified_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (expires_at>verified_at AND expires_at<=verified_at+interval '20 minutes'),
 FOREIGN KEY (instance_id,rp_id,approver_id,credential_id)
   REFERENCES v3_passkey_credentials(instance_id,rp_id,user_id,credential_id)
);
CREATE INDEX v3_proposal_approval_votes_fresh
 ON v3_proposal_approval_votes(proposal_id,verified_at DESC,id);

-- Current executable actions require two independent humans. This table is
-- also the durable handoff queue: execution_id is allocated exactly once when
-- the threshold is reached, before an independent executor is started.
CREATE TABLE v3_proposal_approval_decisions (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 execution_id text NOT NULL UNIQUE CHECK (execution_id ~ '^[0-9a-f]{32}$'),
 action_kind text NOT NULL,
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 review_content_digest text NOT NULL CHECK (review_content_digest ~ '^[0-9a-f]{64}$'),
 approval_digest text NOT NULL CHECK (approval_digest ~ '^[0-9a-f]{64}$'),
 creator_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 application_id text NOT NULL REFERENCES applications(id),
 risk text NOT NULL CHECK (risk IN ('low','medium','high')),
 required_count integer NOT NULL CHECK (required_count=2),
 first_vote_id text NOT NULL REFERENCES v3_proposal_approval_votes(id),
 second_vote_id text NOT NULL REFERENCES v3_proposal_approval_votes(id),
 approved_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (first_vote_id<>second_vote_id AND expires_at>approved_at)
);

-- A later revocation flow can append a tombstone only while no execution
-- claim exists. Keeping the decision immutable preserves its audit digest.
CREATE TABLE v3_proposal_approval_revocations (
 proposal_id text PRIMARY KEY REFERENCES v3_proposal_approval_decisions(proposal_id),
 revoked_by text NOT NULL REFERENCES users(id),
 revoked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000)
);

CREATE FUNCTION v3_proposal_approval_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'v3 proposal approval evidence is immutable';
END $$;
CREATE TRIGGER v3_proposal_approval_vote_immutable
 BEFORE UPDATE OR DELETE ON v3_proposal_approval_votes
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_approval_immutable();
CREATE TRIGGER v3_proposal_approval_decision_immutable
 BEFORE UPDATE OR DELETE ON v3_proposal_approval_decisions
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_approval_immutable();
CREATE TRIGGER v3_proposal_approval_revocation_immutable
 BEFORE UPDATE OR DELETE ON v3_proposal_approval_revocations
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_approval_immutable();

CREATE FUNCTION v3_proposal_approval_state_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status IS DISTINCT FROM NEW.status THEN
   IF OLD.status<>'pending' OR NEW.status NOT IN ('approved','rejected') THEN
     RAISE EXCEPTION 'invalid v3 proposal decision transition';
   END IF;
   IF NEW.status='approved' AND NOT EXISTS (
       SELECT 1 FROM v3_proposal_approval_decisions d WHERE d.proposal_id=NEW.id) THEN
     RAISE EXCEPTION 'approved v3 proposal requires persisted approval decision';
   END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_proposal_approval_state_guard
 BEFORE UPDATE OF status ON v3_proposals
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_approval_state_guard();
