-- Branding readiness is an explicit deployment decision. A missing logo alone
-- does not prove that the product name and brand are still undecided.
ALTER TABLE workspace_settings
  ADD COLUMN v3_brand_pending boolean NOT NULL DEFAULT true;
