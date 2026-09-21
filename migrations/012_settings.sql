-- Workspace metadata is configuration, not observed deployment health.
CREATE TABLE workspace_settings (
 id text PRIMARY KEY CHECK(id='default'),
 name text NOT NULL DEFAULT 'AIOps Platform',
 description text NOT NULL DEFAULT '',
 owner_id text REFERENCES users(id),
 deployment_mode text NOT NULL DEFAULT 'self_hosted',
 timezone text NOT NULL DEFAULT 'Asia/Shanghai',
 language text NOT NULL DEFAULT 'zh-CN',
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO workspace_settings(id,owner_id) VALUES ('default',
 (SELECT id FROM users WHERE role='admin' AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo' ORDER BY id LIMIT 1));
CREATE TABLE settings_team_profiles (
 team_id text PRIMARY KEY REFERENCES teams(id) ON DELETE CASCADE,
 owner_id text REFERENCES users(id),
 department text NOT NULL DEFAULT '',
 is_mock boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
-- Demo users can never authenticate or count toward enabled-administrator safety.
ALTER TABLE users ADD CONSTRAINT settings_demo_never_login CHECK(
 source<>'settings_demo' OR (disabled AND password_hash=''));
