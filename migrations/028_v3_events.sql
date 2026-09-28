-- Operations creates ops_incidents during its own module migration, after the
-- embedded store migrations. Keep V3 notes independent of that startup order.
CREATE TABLE IF NOT EXISTS v3_incident_timeline (
 id text PRIMARY KEY,
 incident_id text NOT NULL,
 actor_id text NOT NULL REFERENCES users(id),
 text text NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS v3_incident_timeline_incident_time
 ON v3_incident_timeline(incident_id, created_at, id);

-- Bootstrap only the administrator capability. Other V3 grants require an
-- explicit review, and old application ACLs are never used as a fallback.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 SELECT 'admin','events',action,'global','*'
 FROM (VALUES ('view'),('change')) AS operations(action)
 WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
 ON CONFLICT DO NOTHING;
