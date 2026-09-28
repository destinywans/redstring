-- External objects are enrolled only after an administrator identifies the
-- exact application, environment, connector and upstream target. Historical
-- application_environments.bindings are intentionally not copied here.
ALTER TABLE v3_connectors
 ADD CONSTRAINT v3_connectors_id_environment_unique UNIQUE (id, environment_id);

CREATE TABLE v3_connector_targets (
 id text PRIMARY KEY,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 connector_id text NOT NULL,
 target_kind text NOT NULL CHECK (target_kind IN ('project', 'job', 'repository')),
 external_id text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 512),
 bound_by text NOT NULL REFERENCES users(id),
 bound_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (application_id, environment_id)
  REFERENCES v3_application_environments(application_id, environment_id) ON DELETE RESTRICT,
 FOREIGN KEY (connector_id, environment_id)
  REFERENCES v3_connectors(id, environment_id) ON DELETE RESTRICT,
 -- One external object cannot be attributed to two services in the same
 -- environment. Shared repositories need a narrower reviewed target model.
 CONSTRAINT v3_connector_targets_unique_target
  UNIQUE (environment_id, connector_id, target_kind, external_id)
);
CREATE INDEX v3_connector_targets_service
 ON v3_connector_targets(application_id, environment_id, target_kind);
