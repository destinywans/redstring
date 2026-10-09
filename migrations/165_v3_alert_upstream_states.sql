-- PA-47 (2026-10-10): the last Alertmanager state the reconciler observed for
-- a firing incident from a reviewed source. A silence suppresses the resolved
-- notification, so the reconciler resolves incidents whose alert is gone
-- upstream and records silenced/inhibited state for the rest.
-- ops_incidents is created by the operations module after these migrations,
-- so incident_id has no foreign key; the reconciler deletes a row when it
-- resolves the incident. Not executed on the existing business database.
CREATE TABLE v3_alert_upstream_states (
 incident_id text PRIMARY KEY,
 environment_id text NOT NULL,
 state text NOT NULL CHECK (state IN ('active', 'silenced', 'inhibited')),
 checked_at timestamptz NOT NULL
);
