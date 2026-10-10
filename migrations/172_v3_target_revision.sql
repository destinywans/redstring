-- PA-19 P3 stage 2 step 6: the service mapping's concurrency token moves
-- from application_environments.binding_revision to a revision of the V3
-- service environment, which counts every change of its registry targets
-- and of its stored bindings (code, deploy and dashboard stay there).
--
-- The revision has its own table: readers lock v3_application_environments
-- rows inside repeatable-read transactions, and a trigger updating those
-- rows would make them fail on every concurrent target write.
--
-- Existing service environments start at binding_revision + 1, so a page
-- that still holds the old token is refused (409) and reads the mapping
-- again. A service environment without a row is at revision 1.
CREATE TABLE v3_service_target_revisions (
 application_id text NOT NULL,
 environment_id text NOT NULL,
 revision bigint NOT NULL CHECK (revision >= 1),
 PRIMARY KEY (application_id, environment_id),
 FOREIGN KEY (application_id, environment_id)
  REFERENCES v3_application_environments(application_id, environment_id) ON DELETE CASCADE
);

INSERT INTO v3_service_target_revisions(application_id, environment_id, revision)
SELECT ve.application_id, ve.environment_id, e.binding_revision + 1
 FROM v3_application_environments ve
 JOIN application_environments e ON e.application_id = ve.application_id AND e.key = ve.environment_id;

CREATE FUNCTION v3_service_target_revision_bump(app text, env text) RETURNS void LANGUAGE sql AS $$
 INSERT INTO v3_service_target_revisions(application_id, environment_id, revision)
 SELECT app, env, 2 WHERE EXISTS (SELECT 1 FROM v3_application_environments
  WHERE application_id = app AND environment_id = env)
 ON CONFLICT (application_id, environment_id)
 DO UPDATE SET revision = v3_service_target_revisions.revision + 1
$$;

CREATE FUNCTION v3_target_revision_bump() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME = 'application_environments' THEN
  PERFORM v3_service_target_revision_bump(NEW.application_id, NEW.key);
  RETURN NULL;
 END IF;
 IF TG_OP <> 'INSERT' THEN
  PERFORM v3_service_target_revision_bump(OLD.application_id, OLD.environment_id);
 END IF;
 IF TG_OP = 'INSERT' OR (TG_OP = 'UPDATE' AND
   (NEW.application_id, NEW.environment_id) IS DISTINCT FROM (OLD.application_id, OLD.environment_id)) THEN
  PERFORM v3_service_target_revision_bump(NEW.application_id, NEW.environment_id);
 END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER v3_connector_targets_revision
 AFTER INSERT OR UPDATE OR DELETE ON v3_connector_targets
 FOR EACH ROW EXECUTE FUNCTION v3_target_revision_bump();

CREATE TRIGGER application_environments_target_revision
 AFTER UPDATE OF bindings ON application_environments
 FOR EACH ROW WHEN (OLD.bindings IS DISTINCT FROM NEW.bindings)
 EXECUTE FUNCTION v3_target_revision_bump();
