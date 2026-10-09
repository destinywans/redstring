-- PA-19 P1 (2026-10-09): the importer moves file-configured telemetry
-- bindings into the V3 registry. Logs and traces get their own target kinds;
-- the external ID stays a reviewed catalog key, never a query or selector.
-- Connector options hold non-secret attributes such as a tenant ID.
-- Kubernetes workloads stay in AIOPS_KUBERNETES_REGISTRY until P3.
ALTER TABLE v3_connectors
 ADD COLUMN options jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE v3_connectors
 ADD CONSTRAINT v3_connectors_options_object CHECK (jsonb_typeof(options) = 'object');

ALTER TABLE v3_connector_targets
 DROP CONSTRAINT v3_connector_targets_target_kind_check;
ALTER TABLE v3_connector_targets
 ADD CONSTRAINT v3_connector_targets_target_kind_check
 CHECK (target_kind IN ('project', 'job', 'repository', 'metric_resource',
  'log_stream', 'log_index', 'trace_service'));
