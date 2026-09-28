-- Service registration records user-confirmed catalog metadata. The CMDB
-- link is created only when a unique, current Kubernetes workload is found.
ALTER TABLE v3_service_profiles ADD COLUMN owner_label text NOT NULL DEFAULT '';
ALTER TABLE v3_service_profiles ADD COLUMN p99_target_ms integer
 CHECK (p99_target_ms > 0);

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','services','change','global','*'
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
