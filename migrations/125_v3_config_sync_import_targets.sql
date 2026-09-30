-- Alert-rule promotion imports a reviewed declarative definition. Applying it
-- to Prometheus is a separate governed operation; an imported row is inert.
CREATE TABLE v3_config_alert_rules (
 id text PRIMARY KEY,
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL DEFAULT 'prod' CHECK (environment_id='prod'),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
 source_version text NOT NULL CHECK (length(btrim(source_version)) BETWEEN 1 AND 100),
 expression text NOT NULL CHECK (length(btrim(expression)) BETWEEN 1 AND 4096),
 duration_seconds integer NOT NULL CHECK (duration_seconds BETWEEN 0 AND 86400),
 labels jsonb NOT NULL CHECK (jsonb_typeof(labels)='object'),
 annotations jsonb NOT NULL CHECK (jsonb_typeof(annotations)='object'),
 source_identity_digest text NOT NULL CHECK (source_identity_digest ~ '^[0-9a-f]{64}$'),
 source_digest text NOT NULL CHECK (source_digest ~ '^[0-9a-f]{64}$'),
 source_instance text NOT NULL,
 source_environment text NOT NULL,
 source_id text NOT NULL,
 promotion_id text NOT NULL UNIQUE,
 activation_state text NOT NULL DEFAULT 'not_applied' CHECK (activation_state='not_applied'),
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id)
);
