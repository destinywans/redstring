-- Bind a queued V3 model reply to the session that started it. Only the
-- one-way session hash is persisted; old queued replies with no hash fail
-- closed during restart recovery. The claim ID makes the initial worker claim
-- safe to retry when a database error leaves the outcome unknown.
ALTER TABLE v3_conversation_replies
    ADD COLUMN initiating_session_hash text NOT NULL DEFAULT '',
    ADD COLUMN worker_claim_id text NOT NULL DEFAULT '';
