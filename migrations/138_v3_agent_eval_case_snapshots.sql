-- Immutable case evidence reported by a trusted in-process evaluator. A case
-- registration alone never implies an executed candidate or automatic score.
ALTER TABLE v3_agent_eval_cases
 ADD COLUMN origin_application_id text,
 ADD COLUMN origin_environment_id text;
WITH earliest AS (
 SELECT DISTINCT ON (target) target,application_id,changes->>'environment_id' AS environment_id
 FROM management_audit
 WHERE action='v3.agent.eval.case.registered' AND outcome='recorded'
 ORDER BY target,id
)
UPDATE v3_agent_eval_cases c SET origin_application_id=e.application_id,
 origin_environment_id=e.environment_id FROM earliest e WHERE c.id=e.target;
ALTER TABLE v3_agent_eval_cases
 ADD CONSTRAINT v3_agent_eval_case_agent_unique UNIQUE(id,agent_id);

CREATE TABLE v3_agent_eval_case_snapshots (
 id text PRIMARY KEY,
 case_id text NOT NULL,
 agent_id text NOT NULL,
 revision bigint NOT NULL CHECK (revision > 0),
 producer_ref text NOT NULL CHECK (length(btrim(producer_ref)) BETWEEN 1 AND 500),
 input_ref text NOT NULL CHECK (length(btrim(input_ref)) BETWEEN 1 AND 500),
 expected_ref text NOT NULL CHECK (length(btrim(expected_ref)) BETWEEN 1 AND 500),
 output_ref text NOT NULL CHECK (length(btrim(output_ref)) BETWEEN 1 AND 500),
 baseline_ref text NOT NULL CHECK (length(btrim(baseline_ref)) BETWEEN 1 AND 500),
 evaluator_ref text NOT NULL CHECK (length(btrim(evaluator_ref)) BETWEEN 1 AND 500),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=262144),
 payload_sha256 text NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
 -- The trusted writer holds the case row lock before insertion. Use insertion
 -- time, not transaction-start time, to order serialized snapshots.
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY (case_id,agent_id) REFERENCES v3_agent_eval_cases(id,agent_id),
 UNIQUE (case_id,revision)
);
ALTER TABLE v3_agent_eval_labels
 ADD COLUMN snapshot_id text REFERENCES v3_agent_eval_case_snapshots(id);
