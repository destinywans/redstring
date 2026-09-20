ALTER TABLE applications ADD COLUMN profile jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE application_environments ADD COLUMN configuration jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE application_environments ADD COLUMN configuration_revision bigint NOT NULL DEFAULT 1;
