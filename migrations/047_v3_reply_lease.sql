-- Reply ownership is issued with the queued turn, using the database clock.
-- Deployment gate: stop every old V3 instance and drain in-flight replies
-- before starting a new instance against this migration. Old workers neither
-- renew leases nor honor write fences; mixed-version execution is unsafe.
-- Any remaining old in-flight row has no lease and is recovered as expired.
ALTER TABLE v3_conversation_replies
    ADD COLUMN lease_expires_at timestamptz;

CREATE INDEX v3_conversation_replies_expired
    ON v3_conversation_replies(lease_expires_at)
    WHERE state IN ('queued', 'running');
