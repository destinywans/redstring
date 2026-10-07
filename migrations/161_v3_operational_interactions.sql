-- Display participants never grant access to the incident or its investigation.
CREATE TABLE v3_incident_participants (
 incident_id text NOT NULL,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100 AND name=btrim(name)),
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (incident_id,name)
);

-- Avatars are private account assets; no client supplied path or URL is stored.
CREATE TABLE v3_user_avatars (
 user_id text PRIMARY KEY REFERENCES users(id),
 revision text NOT NULL,
 content_type text NOT NULL CHECK (content_type IN ('image/png','image/jpeg')),
 content bytea NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 2097152),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- A maintenance plan describes human intent. It is never an Alertmanager
-- silence, executor instruction or approval.
CREATE TABLE v3_maintenance_plans (
 id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 schedule text NOT NULL CHECK (length(btrim(schedule)) BETWEEN 1 AND 500),
 matchers jsonb NOT NULL CHECK (jsonb_typeof(matchers)='array'),
 reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2000),
 owner_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(application_id,environment_id) REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE TABLE v3_item_assignments (
 item_kind text NOT NULL CHECK(item_kind IN ('alert','incident','proposal')),
 item_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 assignee_id text NOT NULL REFERENCES users(id),
 actor_id text NOT NULL REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(item_kind,item_id),
 FOREIGN KEY(application_id,environment_id) REFERENCES v3_application_environments(application_id,environment_id)
);
CREATE TABLE v3_assignment_notifications (
 id text PRIMARY KEY,
 item_kind text NOT NULL,
 item_id text NOT NULL,
 recipient_id text NOT NULL REFERENCES users(id),
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(item_kind,item_id) REFERENCES v3_item_assignments(item_kind,item_id)
);
CREATE INDEX v3_assignment_notifications_recipient ON v3_assignment_notifications(recipient_id,created_at DESC,id);
