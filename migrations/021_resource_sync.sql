-- Collector state is separate from the last successful resource observation.
-- No automatic grants, seed removal, or source deletion happens in migration.
CREATE TABLE cmdb_sync_sources (
 id text PRIMARY KEY,
 kind text NOT NULL,
 name text NOT NULL,
 last_attempt_at timestamptz,
 last_success_at timestamptz,
 last_error text NOT NULL DEFAULT '',
 resource_count integer NOT NULL DEFAULT 0,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cmdb_resource_sync_source ON cmdb_resources(source_kind,source_id);

-- A runtime Pod has one authorization parent: its resolved controller, a real
-- Node owner (mirror/static Pod), or its namespace when it has no controller.
-- Ambiguous/multiple parents fail closed. Ordinary belongs_to namespace/cluster
-- topology edges never grant runtime visibility.
CREATE OR REPLACE VIEW rbac_viewable_resources AS
 WITH pod_owners AS (
  SELECT child.id resource_id,min(parent.id) parent_id FROM cmdb_resources child
  JOIN cmdb_resource_relations rel ON rel.source_resource_id=child.id AND rel.provenance='discovered'
  JOIN cmdb_resources parent ON parent.id=rel.target_resource_id
   AND ((rel.kind='belongs_to' AND parent.kind='deployment') OR (rel.kind='runtime_owner' AND parent.kind IN ('node','namespace')))
  WHERE child.kind='pod' GROUP BY child.id HAVING count(DISTINCT parent.id)=1
 ), direct_views AS (
  SELECT DISTINCT user_id,resource_id FROM rbac_effective_permissions WHERE action='view'
 ), base_views AS (
  SELECT * FROM direct_views UNION SELECT p.user_id,o.resource_id FROM pod_owners o JOIN direct_views p ON p.resource_id=o.parent_id
 ), event_objects AS (
  SELECT child.id resource_id,min(parent.id) parent_id FROM cmdb_resources child
  JOIN cmdb_resource_relations rel ON rel.source_resource_id=child.id AND rel.kind='involved_object' AND rel.provenance='discovered'
  JOIN cmdb_resources parent ON parent.id=rel.target_resource_id AND (rbac_is_infrastructure_kind(parent.kind) OR parent.kind='pod')
  WHERE child.kind='event' GROUP BY child.id HAVING count(DISTINCT parent.id)=1
 ) SELECT * FROM base_views UNION SELECT p.user_id,o.resource_id FROM event_objects o JOIN base_views p ON p.resource_id=o.parent_id;
