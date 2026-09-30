-- A human-confirmed incident case has its own durable governance lifecycle.
-- Existing rows receive revision 1 without invented transition evidence.
-- In particular, a preexisting resolved row without a resolved event is not
-- retrospectively treated as a verified postmortem closing source.
ALTER TABLE v3_incident_cases
 ADD COLUMN status_revision bigint NOT NULL DEFAULT 1 CHECK (status_revision > 0),
 ADD COLUMN resolved_status_event_id bigint,
 ADD COLUMN resolved_at timestamptz;

CREATE TABLE v3_incident_case_status_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 case_id text NOT NULL REFERENCES v3_incident_cases(id),
 revision bigint NOT NULL CHECK (revision > 1),
 from_status text NOT NULL CHECK (from_status IN ('active','mitigated')),
 to_status text NOT NULL CHECK (to_status IN ('mitigated','resolved')),
 actor_id text NOT NULL REFERENCES users(id),
 request_id text NOT NULL
   CHECK (char_length(request_id) BETWEEN 8 AND 128 AND
          request_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$'),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 reason text CHECK (reason IS NULL OR
   (char_length(reason) BETWEEN 1 AND 2000 AND reason = btrim(reason))),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK ((from_status='active' AND to_status IN ('mitigated','resolved')) OR
        (from_status='mitigated' AND to_status='resolved')),
 UNIQUE (case_id, revision),
 UNIQUE (actor_id, request_id),
 UNIQUE (case_id, id)
);

ALTER TABLE v3_incident_cases
 ADD CONSTRAINT v3_incident_case_resolved_event_same_case
   FOREIGN KEY (id, resolved_status_event_id)
   REFERENCES v3_incident_case_status_events(case_id, id),
 ADD CONSTRAINT v3_incident_case_resolved_evidence_pair
   CHECK ((resolved_status_event_id IS NULL AND resolved_at IS NULL) OR
          (status='resolved' AND resolved_status_event_id IS NOT NULL AND resolved_at IS NOT NULL));
