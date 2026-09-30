-- One durable dispatch right per approved silence action. A committed
-- dispatching row may represent a request that never left this process, but
-- automatic retry is still forbidden because it may also have reached AM.
CREATE TABLE v3_silence_execution_claims (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 action_kind text NOT NULL CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel')),
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 review_content_digest text NOT NULL CHECK (review_content_digest ~ '^[0-9a-f]{64}$'),
 approval_digest text NOT NULL CHECK (approval_digest ~ '^[0-9a-f]{64}$'),
 create_operation_id text NOT NULL REFERENCES v3_silence_create_operations(id),
 cancel_intent_id text UNIQUE REFERENCES v3_silence_cancel_intents(id),
 upstream_id text CHECK (upstream_id IS NULL OR upstream_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 state text NOT NULL CHECK (state IN ('reserved','dispatching','unknown','confirmed','expired','manual_review')),
 dispatch_started_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(action_kind,create_operation_id),
 CHECK ((action_kind='alertmanager.silence.create' AND cancel_intent_id IS NULL)
     OR (action_kind='alertmanager.silence.cancel' AND cancel_intent_id IS NOT NULL AND upstream_id IS NOT NULL)),
 CHECK ((state='reserved' AND dispatch_started_at IS NULL)
     OR (state<>'reserved' AND dispatch_started_at IS NOT NULL)
     OR (state='expired' AND dispatch_started_at IS NULL))
);

CREATE FUNCTION v3_silence_execution_claim_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.proposal_id IS DISTINCT FROM OLD.proposal_id
    OR NEW.action_kind IS DISTINCT FROM OLD.action_kind
    OR NEW.action_digest IS DISTINCT FROM OLD.action_digest
    OR NEW.review_content_digest IS DISTINCT FROM OLD.review_content_digest
    OR NEW.approval_digest IS DISTINCT FROM OLD.approval_digest
    OR NEW.create_operation_id IS DISTINCT FROM OLD.create_operation_id
    OR NEW.cancel_intent_id IS DISTINCT FROM OLD.cancel_intent_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.dispatch_started_at IS DISTINCT FROM OLD.dispatch_started_at AND OLD.dispatch_started_at IS NOT NULL
    OR (OLD.upstream_id IS NOT NULL AND NEW.upstream_id IS DISTINCT FROM OLD.upstream_id) THEN
   RAISE EXCEPTION 'v3 silence execution claim identity is immutable';
 END IF;
 IF OLD.state<>NEW.state AND NOT (
      OLD.state='reserved' AND NEW.state IN ('dispatching','expired')
   OR OLD.state='dispatching' AND NEW.state IN ('unknown','confirmed','expired','manual_review')
   OR OLD.state='unknown' AND NEW.state IN ('confirmed','manual_review')) THEN
   RAISE EXCEPTION 'invalid v3 silence execution transition';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_silence_execution_claim_guard
 BEFORE UPDATE ON v3_silence_execution_claims
 FOR EACH ROW EXECUTE FUNCTION v3_silence_execution_claim_guard();
