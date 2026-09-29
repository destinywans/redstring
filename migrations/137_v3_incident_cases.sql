-- A human-confirmed V3 incident is distinct from an Alertmanager occurrence.
-- Store migrations run before Operations creates ops_incidents, so alert links
-- retain the exact alert ID without a premature foreign key to that table.
-- The writer must lock and recheck every linked alert and its V3 scope.

-- Preserve the environment identity even when its current V3 selection mapping
-- is later revoked. The writer checks and locks that mapping at creation time.
-- The existing pairwise unique constraints do not by themselves establish
-- the three-column relationship with the persisted environment row.
CREATE UNIQUE INDEX v3_incident_case_environment_identity
 ON application_environments(id, application_id, key);

CREATE TABLE v3_incident_cases (
 id text PRIMARY KEY CHECK (char_length(id) BETWEEN 1 AND 128),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 environment_key text NOT NULL CHECK (environment_key <> '' AND environment_key <> 'all'),
 title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 300 AND title = btrim(title)),
 title_provenance text NOT NULL DEFAULT 'human_confirmed'
   CHECK (title_provenance = 'human_confirmed'),
 severity text NOT NULL CHECK (severity IN ('P1','P2','P3','P4')),
 severity_provenance text NOT NULL DEFAULT 'human_confirmed'
   CHECK (severity_provenance = 'human_confirmed'),
 impact text CHECK (impact IS NULL OR
   (char_length(impact) BETWEEN 1 AND 2000 AND impact = btrim(impact))),
 impact_provenance text GENERATED ALWAYS AS
   (CASE WHEN impact IS NULL THEN 'unknown' ELSE 'human_confirmed' END) STORED,
 -- This is the name entered or confirmed by a human, not a verified role grant.
 commander_text text CHECK (commander_text IS NULL OR
   (char_length(commander_text) BETWEEN 1 AND 200 AND commander_text = btrim(commander_text))),
 commander_provenance text GENERATED ALWAYS AS
   (CASE WHEN commander_text IS NULL THEN 'unknown' ELSE 'human_confirmed' END) STORED,
 status text NOT NULL DEFAULT 'active'
   CHECK (status IN ('active','mitigated','resolved')),
 investigation_id text REFERENCES investigations(id),
 actor_id text NOT NULL REFERENCES users(id),
 request_key text NOT NULL
   CHECK (char_length(request_key) BETWEEN 8 AND 128 AND
          request_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]*$'),
 request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK (updated_at >= created_at),
 UNIQUE (actor_id, request_key),
 FOREIGN KEY (environment_id, application_id, environment_key)
   REFERENCES application_environments(id, application_id, key)
);

CREATE INDEX v3_incident_cases_scope_recent
 ON v3_incident_cases(environment_key, application_id, updated_at DESC, id DESC);

CREATE TABLE v3_incident_case_alerts (
 case_id text NOT NULL REFERENCES v3_incident_cases(id) ON DELETE CASCADE,
 alert_id text NOT NULL CHECK (char_length(alert_id) BETWEEN 1 AND 128),
 PRIMARY KEY (case_id, alert_id),
 UNIQUE (alert_id)
);
