-- ops_incidents is created by Operations after the embedded store migrations.
-- The API locks and validates that incident before insertion; this table keeps
-- its identity unique without an FK that would break the existing boot order.
CREATE TABLE v3_postmortems (
 id text PRIMARY KEY,
 incident_id text NOT NULL UNIQUE,
 investigation_id text NOT NULL REFERENCES investigations(id),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 close_event_seq bigint NOT NULL REFERENCES events(seq),
 sections jsonb NOT NULL CHECK (jsonb_typeof(sections)='array'),
 actions jsonb NOT NULL CHECK (jsonb_typeof(actions)='array'),
 status text NOT NULL CHECK (status IN ('draft','review','published')),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE INDEX v3_postmortems_scope ON v3_postmortems(environment_id,application_id,created_at DESC);
