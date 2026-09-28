-- Generic V3 canvases are distinct from application-bound investigations.
-- A canvas is private to its creator until a reviewed sharing model exists.
CREATE TABLE v3_canvases (
 id text PRIMARY KEY,
 owner_id text NOT NULL REFERENCES users(id),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
 kind text NOT NULL CHECK (kind IN ('plan','review','analysis','custom')),
 status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','waiting_approval','done','resolved')),
 goal text NOT NULL DEFAULT '',
 services text[] NOT NULL DEFAULT '{}',
 nodes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(nodes)='array'),
 edges jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(edges)='array'),
 artifacts jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(artifacts)='array'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX v3_canvases_owner_environment_updated
 ON v3_canvases(owner_id,environment_id,updated_at DESC,id);

-- System templates have no owner/environment; private templates retain both.
-- Only structure is stored, never evidence, conversation turns or credentials.
CREATE TABLE v3_canvas_templates (
 id text PRIMARY KEY,
 owner_id text REFERENCES users(id),
 environment_id text,
 kind text NOT NULL CHECK (kind IN ('plan','review','analysis','custom')),
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
 description text NOT NULL DEFAULT '',
 nodes jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(nodes)='array'),
 edges jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(edges)='array'),
 is_default boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((owner_id IS NULL AND environment_id IS NULL) OR
        (owner_id IS NOT NULL AND environment_id IS NOT NULL AND environment_id <> '' AND environment_id <> 'all'))
);
CREATE INDEX v3_canvas_templates_private ON v3_canvas_templates(owner_id,environment_id,kind);

-- Blank templates are product definitions, not sample business data.
INSERT INTO v3_canvas_templates(id,kind,name,description,nodes,is_default) VALUES
 ('v3-blank-plan','plan','空白计划','从一个主题节点开始','[{"id":"root","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"主题","sub":"主线","blocks":[]}]',true),
 ('v3-blank-review','review','空白评审','从一个主题节点开始','[{"id":"root","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"主题","sub":"主线","blocks":[]}]',true),
 ('v3-blank-analysis','analysis','空白分析','从一个主题节点开始','[{"id":"root","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"主题","sub":"主线","blocks":[]}]',true),
 ('v3-blank-custom','custom','空白画布','从一个主题节点开始','[{"id":"root","x":16,"y":140,"w":260,"icon":"layers","tone":"info","title":"主题","sub":"主线","blocks":[]}]',true);

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','canvas',action,'global','*'
FROM (VALUES ('view'),('change')) AS grant_action(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
