-- A cancellation is reviewable intent, never permission to call Alertmanager.
-- Its evidence and proposal binding are immutable; a separate approved
-- executor must record any dispatch/outcome in its own ledger.
ALTER TABLE v3_proposal_actions
 DROP CONSTRAINT v3_proposal_actions_action_kind_check;
ALTER TABLE v3_proposal_actions
 ADD CONSTRAINT v3_proposal_actions_action_kind_check
 CHECK (action_kind IN ('alertmanager.silence.create','alertmanager.silence.cancel'));

CREATE TABLE v3_silence_cancel_intents (
 id text PRIMARY KEY,
 create_operation_id text NOT NULL REFERENCES v3_silence_create_operations(id),
 proposal_id text NOT NULL UNIQUE REFERENCES v3_proposals(id),
 actor_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 application_id text NOT NULL REFERENCES applications(id),
 investigation_id text NOT NULL REFERENCES investigations(id),
 upstream_id text NOT NULL CHECK (upstream_id ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 8 AND 128),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 observed_digest text NOT NULL CHECK (observed_digest ~ '^[0-9a-f]{64}$'),
 observed_snapshot jsonb NOT NULL CHECK (jsonb_typeof(observed_snapshot)='object'),
 observed_at timestamptz NOT NULL,
 reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(actor_id,idempotency_key)
);
CREATE INDEX v3_silence_cancel_target
 ON v3_silence_cancel_intents(create_operation_id,created_at DESC);

CREATE FUNCTION v3_silence_cancel_intent_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'v3 silence cancel intents are immutable';
END $$;
CREATE TRIGGER v3_silence_cancel_intent_immutable
 BEFORE UPDATE OR DELETE ON v3_silence_cancel_intents
 FOR EACH ROW EXECUTE FUNCTION v3_silence_cancel_intent_immutable();
