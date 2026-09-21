CREATE TABLE cmdb_resources (
 id text PRIMARY KEY, kind text NOT NULL, name text NOT NULL, description text NOT NULL DEFAULT '',
 external_id text NOT NULL DEFAULT '', source_kind text NOT NULL, source_id text NOT NULL DEFAULT '',
 owner_user_id text REFERENCES users(id), owner_team_id text REFERENCES teams(id),
 attributes jsonb NOT NULL DEFAULT '{}', tags jsonb NOT NULL DEFAULT '[]', is_mock boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), observed_at timestamptz
);
CREATE UNIQUE INDEX cmdb_source_identity ON cmdb_resources(source_kind,source_id,external_id) WHERE external_id<>'';
CREATE INDEX cmdb_kind ON cmdb_resources(kind);
CREATE TABLE cmdb_resource_applications (
 resource_id text NOT NULL REFERENCES cmdb_resources(id) ON DELETE CASCADE,
 application_id text NOT NULL REFERENCES applications(id), environment_id text NOT NULL,
 PRIMARY KEY(resource_id,application_id,environment_id),
 FOREIGN KEY(environment_id,application_id) REFERENCES application_environments(id,application_id)
);
CREATE INDEX cmdb_application_scope ON cmdb_resource_applications(application_id,environment_id,resource_id);
CREATE TABLE cmdb_resource_relations (
 id text PRIMARY KEY, source_resource_id text NOT NULL REFERENCES cmdb_resources(id) ON DELETE CASCADE,
 target_resource_id text NOT NULL REFERENCES cmdb_resources(id) ON DELETE CASCADE,
 kind text NOT NULL, provenance text NOT NULL CHECK(provenance IN ('declared','discovered')),
 source_kind text NOT NULL,source_id text NOT NULL DEFAULT '',description text NOT NULL DEFAULT '',
 is_mock boolean NOT NULL DEFAULT false, updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(source_resource_id<>target_resource_id), UNIQUE(source_resource_id,target_resource_id,kind,source_kind,source_id)
);
CREATE INDEX cmdb_edge_target ON cmdb_resource_relations(target_resource_id);
CREATE TABLE cmdb_resource_changes (
 id bigserial PRIMARY KEY, resource_id text NOT NULL REFERENCES cmdb_resources(id) ON DELETE CASCADE,
 action text NOT NULL, description text NOT NULL, is_mock boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX cmdb_change_resource ON cmdb_resource_changes(resource_id,id DESC);
