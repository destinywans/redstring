-- V3 personal settings extend the existing account and workspace records.
-- Defaults describe configuration, not an active MFA or external integration.
ALTER TABLE users
  ADD COLUMN v3_display_name text NOT NULL DEFAULT '',
  ADD COLUMN v3_timezone text NOT NULL DEFAULT 'Asia/Shanghai';

ALTER TABLE workspace_settings
  ADD COLUMN v3_slug text NOT NULL DEFAULT 'aiops',
  ADD COLUMN v3_week_start text NOT NULL DEFAULT 'monday' CHECK (v3_week_start IN ('monday','sunday')),
  ADD COLUMN v3_retention_days integer NOT NULL DEFAULT 90 CHECK (v3_retention_days BETWEEN 1 AND 3650),
  ADD COLUMN v3_data_region text NOT NULL DEFAULT 'local',
  ADD COLUMN v3_brand_name text NOT NULL DEFAULT 'AIOps',
  ADD COLUMN v3_brand_logo text;

CREATE TABLE v3_user_settings (
  user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('light','dark','system')),
  locale text NOT NULL DEFAULT 'zh-CN' CHECK (locale IN ('zh-CN','en-US')),
  density text NOT NULL DEFAULT 'comfortable' CHECK (density IN ('comfortable','compact')),
  reduce_motion boolean NOT NULL DEFAULT false,
  ai_preferences jsonb NOT NULL DEFAULT '{"default":{"model":""},"canvas_inherit":true,"show_reasoning":"collapsed","auto_context":true,"memory":true,"budget_warn_tokens":8000}'::jsonb,
  notification_settings jsonb NOT NULL DEFAULT '{"quiet":{"enabled":false,"from":"22:00","to":"08:00"},"digest":"daily","subscriptions":[]}'::jsonb,
  profile_notifications jsonb NOT NULL DEFAULT '[]'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);
