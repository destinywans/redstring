-- An action snapshot is evidence for review, not approval or permission to
-- execute. Only the application-scoped Alertmanager silence-create variant is
-- admitted here. Existing display-only proposals are deliberately not
-- backfilled into executable actions.
CREATE TABLE v3_proposal_actions (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 action_kind text NOT NULL CHECK (action_kind='alertmanager.silence.create'),
 schema_version integer NOT NULL CHECK (schema_version=1),
 action jsonb NOT NULL CHECK (jsonb_typeof(action)='object'),
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 review_content_digest text NOT NULL CHECK (review_content_digest ~ '^[0-9a-f]{64}$'),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 binding_fingerprint text NOT NULL CHECK (binding_fingerprint ~ '^[0-9a-f]{64}$'),
 creator_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 investigation_id text NOT NULL REFERENCES investigations(id),
 investigation_updated_at timestamptz NOT NULL,
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 source_kind text NOT NULL CHECK (source_kind='human_request'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE (creator_id,idempotency_key),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id)
);

-- Product writes can reject a proposal, but cannot silently replace what was
-- shown to the reviewer or what was proposed for eventual approval.
CREATE FUNCTION v3_proposal_action_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'v3 proposal action snapshots are immutable';
END $$;
CREATE TRIGGER v3_proposal_action_immutable
 BEFORE UPDATE OR DELETE ON v3_proposal_actions
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_action_immutable();

CREATE FUNCTION v3_proposal_review_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.investigation_id IS DISTINCT FROM OLD.investigation_id
    OR NEW.application_id IS DISTINCT FROM OLD.application_id
    OR NEW.environment_id IS DISTINCT FROM OLD.environment_id
    OR NEW.content IS DISTINCT FROM OLD.content
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.creator_kind IS DISTINCT FROM OLD.creator_kind
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
   RAISE EXCEPTION 'v3 proposal review identity and content are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_proposal_review_identity_immutable
 BEFORE UPDATE ON v3_proposals
 FOR EACH ROW EXECUTE FUNCTION v3_proposal_review_identity_immutable();
