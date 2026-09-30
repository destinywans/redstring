-- A Prometheus metric resource is enrolled for an exact application and
-- environment. The resource is only a trusted query-catalog key, not PromQL.
ALTER TABLE v3_connector_targets
 DROP CONSTRAINT v3_connector_targets_target_kind_check;
ALTER TABLE v3_connector_targets
 ADD CONSTRAINT v3_connector_targets_target_kind_check
 CHECK (target_kind IN ('project', 'job', 'repository', 'metric_resource'));
