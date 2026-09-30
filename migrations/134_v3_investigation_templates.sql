-- Investigation templates are explicit configuration. An empty catalog is a
-- valid deployment state; no example data is installed by this migration.
CREATE TABLE v3_investigation_templates (
 id text PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 120),
 owner text NOT NULL CHECK (owner IN ('official','team')),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 application_id text REFERENCES applications(id),
 team_id text REFERENCES teams(id),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
 description text NOT NULL DEFAULT '',
 version integer NOT NULL CHECK (version > 0),
 maintainer text NOT NULL DEFAULT '',
 branches jsonb NOT NULL CHECK (jsonb_typeof(branches)='array'),
 tools jsonb NOT NULL CHECK (jsonb_typeof(tools)='array'),
 runbooks jsonb NOT NULL CHECK (jsonb_typeof(runbooks)='array'),
 triggers jsonb NOT NULL CHECK (jsonb_typeof(triggers)='object'),
 budget jsonb NOT NULL CHECK (jsonb_typeof(budget)='object'),
 active boolean NOT NULL DEFAULT true,
 uses bigint NOT NULL DEFAULT 0 CHECK (uses>=0),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id),
 CHECK (owner<>'team' OR team_id IS NOT NULL)
);
CREATE INDEX v3_investigation_templates_catalog
 ON v3_investigation_templates(environment_id,active,owner,id);

-- Operators may configure rows through SQL or a later importer. A definition
-- change cannot silently reuse a version already shown to a caller.
CREATE FUNCTION v3_investigation_template_version_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP = 'DELETE' THEN
  RAISE EXCEPTION 'disable investigation templates instead of deleting them';
 END IF;
 IF NEW.id <> OLD.id THEN
  RAISE EXCEPTION 'investigation template IDs are immutable';
 END IF;
 IF NEW.version < OLD.version OR NEW.version > OLD.version + 1 THEN
  RAISE EXCEPTION 'investigation template version must advance by one';
 END IF;
 IF ROW(NEW.owner,NEW.environment_id,NEW.application_id,NEW.team_id,NEW.name,
        NEW.description,NEW.maintainer,NEW.branches,NEW.tools,NEW.runbooks,
        NEW.triggers,NEW.budget,NEW.active)
    IS DISTINCT FROM
    ROW(OLD.owner,OLD.environment_id,OLD.application_id,OLD.team_id,OLD.name,
        OLD.description,OLD.maintainer,OLD.branches,OLD.tools,OLD.runbooks,
        OLD.triggers,OLD.budget,OLD.active) THEN
  IF NEW.version <> OLD.version + 1 THEN
   RAISE EXCEPTION 'investigation template definition requires a new version';
  END IF;
  NEW.updated_at := now();
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER v3_investigation_template_version_guard
 BEFORE UPDATE OR DELETE ON v3_investigation_templates
 FOR EACH ROW EXECUTE FUNCTION v3_investigation_template_version_guard();

-- Request IDs make concurrent retries one durable start. The snapshot pins
-- the exact template definition used for the created investigation.
CREATE TABLE v3_investigation_template_starts (
 actor_id text NOT NULL REFERENCES users(id),
 request_id text NOT NULL CHECK (length(request_id) BETWEEN 8 AND 128),
 investigation_id text NOT NULL UNIQUE REFERENCES investigations(id),
 template_id text NOT NULL REFERENCES v3_investigation_templates(id),
 template_version integer NOT NULL CHECK (template_version>0),
 application_id text NOT NULL,
 environment_id text NOT NULL,
 snapshot jsonb NOT NULL CHECK (jsonb_typeof(snapshot)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (actor_id,request_id),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id)
);
