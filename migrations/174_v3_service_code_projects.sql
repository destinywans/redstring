-- D20 (D11 design, approved 2026-10-11): the code mapping of a service
-- environment moves from application_environments.bindings into the
-- registry. A row says which enrolled GitLab project target is the
-- service's code project; candidate acceptance writes it and the change
-- readers (GET /changes with sources=gitlab, read_service_changes) read it.
-- A service environment has at most one code project and a target is the
-- code project of at most one service. Removing the target removes the row.
ALTER TABLE v3_connector_targets
 ADD CONSTRAINT v3_connector_targets_id_scope_unique UNIQUE (id, application_id, environment_id, target_kind);

CREATE TABLE v3_service_code_projects (
 application_id text NOT NULL,
 environment_id text NOT NULL,
 target_id text NOT NULL,
 target_kind text NOT NULL DEFAULT 'project' CHECK (target_kind = 'project'),
 source text NOT NULL CHECK (source IN ('candidate', 'migrated_binding')),
 candidate_id text,
 confirmed_by text REFERENCES users(id),
 confirmed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (application_id, environment_id),
 UNIQUE (target_id),
 CHECK ((source = 'candidate') = (confirmed_by IS NOT NULL AND candidate_id IS NOT NULL)),
 FOREIGN KEY (application_id, environment_id)
  REFERENCES v3_application_environments(application_id, environment_id) ON DELETE CASCADE,
 FOREIGN KEY (target_id, application_id, environment_id, target_kind)
  REFERENCES v3_connector_targets(id, application_id, environment_id, target_kind) ON DELETE CASCADE
);

-- A change of the code project changes the service mapping, so it raises
-- the mapping revision like a target change (migration 172).
CREATE TRIGGER v3_service_code_projects_revision
 AFTER INSERT OR UPDATE OR DELETE ON v3_service_code_projects
 FOR EACH ROW EXECUTE FUNCTION v3_target_revision_bump();

-- Existing code mappings move only on a database bound to a V3 instance; the
-- legacy mode keeps reading its bindings. A stored GitLab code binding that
-- names an enrolled GitLab project target of the same service environment
-- becomes a row; every code binding (GitLab, GitHub) then leaves the stored
-- bindings, and each is audited as migrated or not migrated. A not migrated
-- one is accepted again from its candidate. A legacy database bound to V3
-- after this migration keeps its code bindings, which V3 no longer reads.
DO $$
BEGIN
 IF to_regclass('v3_instance_boundary') IS NULL THEN
  RETURN;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM v3_instance_boundary) THEN
  RETURN;
 END IF;
 INSERT INTO v3_service_code_projects(application_id, environment_id, target_id, source)
 SELECT DISTINCT ON (e.application_id, e.key) e.application_id, e.key, t.id, 'migrated_binding'
 FROM application_environments e
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.bindings) = 'array' THEN e.bindings ELSE '[]'::jsonb END) b
 JOIN v3_application_environments ve ON ve.application_id = e.application_id AND ve.environment_id = e.key
 JOIN v3_connector_targets t ON t.application_id = e.application_id AND t.environment_id = e.key
  AND t.target_kind = 'project' AND t.connector_id = b.value->>'connector_id' AND t.external_id = b.value->>'resource'
 JOIN v3_connectors c ON c.id = t.connector_id AND c.environment_id = t.environment_id AND c.provider = 'gitlab'
 WHERE lower(b.value->>'kind') = 'gitlab'
 ORDER BY e.application_id, e.key, t.id
 ON CONFLICT DO NOTHING;

 INSERT INTO management_audit(actor_id, actor_kind, application_id, action, target, outcome, changes)
 SELECT '', 'system', e.application_id,
  CASE WHEN p.target_id IS NULL THEN 'v3.service_code_project.not_migrated' ELSE 'v3.service_code_project.migrated' END,
  e.id, 'success',
  jsonb_build_object('environment_id', e.key, 'binding', b.value, 'target_id', p.target_id)
 FROM application_environments e
 CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(e.bindings) = 'array' THEN e.bindings ELSE '[]'::jsonb END) b
 LEFT JOIN LATERAL (
  SELECT p.target_id FROM v3_service_code_projects p
  JOIN v3_connector_targets t ON t.id = p.target_id
  WHERE p.application_id = e.application_id AND p.environment_id = e.key AND p.source = 'migrated_binding'
   AND lower(b.value->>'kind') = 'gitlab'
   AND t.connector_id = b.value->>'connector_id' AND t.external_id = b.value->>'resource'
 ) p ON true
 WHERE lower(b.value->>'kind') IN ('gitlab', 'github', 'githubactions');

 UPDATE application_environments e
 SET bindings = (SELECT COALESCE(jsonb_agg(b.value ORDER BY b.ordinality), '[]'::jsonb)
                 FROM jsonb_array_elements(e.bindings) WITH ORDINALITY b
                 WHERE lower(b.value->>'kind') NOT IN ('gitlab', 'github', 'githubactions')),
     binding_revision = binding_revision + 1
 WHERE jsonb_typeof(e.bindings) = 'array'
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(e.bindings) b
              WHERE lower(b.value->>'kind') IN ('gitlab', 'github', 'githubactions'));
END $$;
