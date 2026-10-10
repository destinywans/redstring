-- PA-19 P3 / PA-08 (2026-10-10): a service may bind one Kubernetes workload
-- through the unified registry. The external ID is a reviewed catalog key
-- (namespace and Deployment live in the catalog), never a resource name.
-- Not executed on existing business databases.
ALTER TABLE v3_connector_targets
 DROP CONSTRAINT v3_connector_targets_target_kind_check;
ALTER TABLE v3_connector_targets
 ADD CONSTRAINT v3_connector_targets_target_kind_check
 CHECK (target_kind IN ('project', 'job', 'repository', 'metric_resource',
  'log_stream', 'log_index', 'trace_service', 'workload'));
