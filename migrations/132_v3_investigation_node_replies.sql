-- Investigation node replies have a durable attempt independent of immutable
-- branch context revisions. A failed attempt remains inspectable without
-- adding its unsuccessful prompt to later model context.
CREATE TABLE v3_investigation_node_replies (
 id text PRIMARY KEY,
 investigation_id text NOT NULL REFERENCES investigations(id),
 branch_id text NOT NULL REFERENCES branches(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES users(id),
 user_text text NOT NULL CHECK (char_length(user_text) BETWEEN 1 AND 8192),
 assistant_text text,
 model_id text NOT NULL,
 model_name text NOT NULL,
 model_provider text NOT NULL,
 reasoning text NOT NULL DEFAULT '',
 state text NOT NULL CHECK (state IN ('running','completed','failed')),
 base_revision bigint NOT NULL CHECK (base_revision >= 0),
 result_revision bigint CHECK (result_revision >= 0),
 failure_code text NOT NULL DEFAULT '',
 lease_expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 CHECK ((state='completed' AND assistant_text IS NOT NULL AND result_revision IS NOT NULL AND completed_at IS NOT NULL)
    OR (state<>'completed' AND assistant_text IS NULL AND result_revision IS NULL AND completed_at IS NULL))
);
CREATE UNIQUE INDEX v3_investigation_node_one_running
 ON v3_investigation_node_replies(branch_id) WHERE state='running';
CREATE INDEX v3_investigation_node_replies_history
 ON v3_investigation_node_replies(branch_id,created_at DESC,id DESC);
