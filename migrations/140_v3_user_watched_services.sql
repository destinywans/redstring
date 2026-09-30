-- A personal watch is an explicit user choice, never inferred from service
-- ownership, team membership, on-call duty or notification subscriptions.
CREATE TABLE v3_user_watched_services (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (user_id,application_id,environment_id),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id) ON DELETE CASCADE
);

CREATE INDEX v3_user_watched_services_scope
 ON v3_user_watched_services(user_id,environment_id,created_at,application_id);
