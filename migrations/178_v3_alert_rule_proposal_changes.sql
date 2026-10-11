-- D11 §11.4: the whole-file difference of an alert rule enable or disable
-- proposal, rendered from the plan's base revision when the proposal is
-- created. It is review evidence beside the immutable action, whose digest
-- already binds the proposed bytes (content_sha256); it is not part of the
-- approval. Proposals created before this table have no row.
CREATE TABLE v3_alert_rule_proposal_changes (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 base_revision text NOT NULL CHECK (base_revision ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
 content_sha256 text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
 lines jsonb NOT NULL CHECK (jsonb_typeof(lines)='array'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
