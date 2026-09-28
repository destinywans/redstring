-- A guard event can arrive after its policy was disabled. Preserve enabled
-- intervals so a trusted producer can report the policy revision and DB time
-- captured when the decision was made. Existing events lack that evidence and
-- remain explicitly unverified; their created_at is not backfilled as action time.
ALTER TABLE v3_agent_guard_policies
 ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);

CREATE TABLE v3_agent_guard_policy_revisions (
 policy_id text NOT NULL REFERENCES v3_agent_guard_policies(id),
 revision bigint NOT NULL CHECK (revision > 0),
 enabled boolean NOT NULL,
 valid_from timestamptz NOT NULL,
 valid_to timestamptz,
 PRIMARY KEY (policy_id,revision),
 CHECK (valid_to IS NULL OR valid_to >= valid_from)
);
CREATE INDEX v3_agent_guard_policy_revision_intervals
 ON v3_agent_guard_policy_revisions(policy_id,valid_from DESC);

-- Only the current state from this migration statement onward is known.
-- updated_at is not proof that the state held before this migration.
INSERT INTO v3_agent_guard_policy_revisions(policy_id,revision,enabled,valid_from)
 SELECT id,1,enabled,statement_timestamp() FROM v3_agent_guard_policies;

ALTER TABLE v3_agent_guard_events
 ADD COLUMN observed_at timestamptz,
 ADD COLUMN policy_revision bigint,
 ADD CONSTRAINT v3_agent_guard_event_evidence_pair CHECK
   ((observed_at IS NULL AND policy_revision IS NULL) OR
    (observed_at IS NOT NULL AND policy_revision IS NOT NULL)),
 ADD CONSTRAINT v3_agent_guard_event_revision_fk
   FOREIGN KEY (policy_id,policy_revision)
   REFERENCES v3_agent_guard_policy_revisions(policy_id,revision);
