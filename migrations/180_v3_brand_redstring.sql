-- The product name is Redstring (PA-259). New workspaces start with it and
-- without the "name and logo pending" badge; a workspace still on the original
-- placeholder (AIOps, no logo, pending) moves to it. A brand the deployment
-- set is left alone. The brand is read-only through the API.
ALTER TABLE workspace_settings
  ALTER COLUMN v3_brand_name SET DEFAULT 'Redstring',
  ALTER COLUMN v3_brand_pending SET DEFAULT false;

UPDATE workspace_settings
   SET v3_brand_name = 'Redstring', v3_brand_pending = false, revision = revision + 1, updated_at = now()
 WHERE v3_brand_name = 'AIOps' AND v3_brand_logo IS NULL AND v3_brand_pending;
