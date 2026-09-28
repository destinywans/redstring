-- Public, single-page Web sources do not have credentials. Other registered
-- source types keep the existing Vault-reference requirement. An empty secret
-- never implies a successful read, sync, or indexed document.
ALTER TABLE v3_knowledge_source_registrations
 DROP CONSTRAINT v3_knowledge_source_registrations_secret_ref_check;
ALTER TABLE v3_knowledge_source_registrations
 ADD CONSTRAINT v3_knowledge_source_registrations_secret_ref_check
 CHECK (
  (source_type = 'web' AND secret_ref = '') OR
  (secret_ref ~ '^vault:[A-Za-z0-9_-]+(/[A-Za-z0-9_.-]+)+$'
   AND secret_ref !~ '(^|/)\.\.?(/|$)')
 );
