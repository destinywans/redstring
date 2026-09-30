-- A metric question owns one auditable, single-service evidence snapshot.
-- The unique owner/request key fences concurrent retries before model work.
ALTER TABLE v3_conversations ADD COLUMN metrics_source_required boolean NOT NULL DEFAULT false;

CREATE TABLE v3_metrics_questions (
    owner_id text NOT NULL REFERENCES users(id),
    request_id text NOT NULL,
    conversation_id text NOT NULL UNIQUE REFERENCES v3_conversations(id) DEFERRABLE INITIALLY DEFERRED,
    initial_reply_id text NOT NULL REFERENCES v3_conversation_replies(id) DEFERRABLE INITIALLY DEFERRED,
    environment_id text NOT NULL,
    application_id text NOT NULL,
    view_id text NOT NULL,
    range_id text NOT NULL,
    payload_hash text NOT NULL,
    binding_id text NOT NULL,
    connector_id text NOT NULL,
    resource_id text NOT NULL,
    query_id text NOT NULL,
    source_fingerprint text NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    evidence_sha256 text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, request_id),
    CHECK (window_start < window_end),
    CHECK (octet_length(request_id) BETWEEN 1 AND 128)
);
