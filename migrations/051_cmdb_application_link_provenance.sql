-- Collector configuration and explicit user registration own different links.
-- The existing triple remains unique: an explicit registration takes ownership
-- when both paths choose the same resource/application/environment.
ALTER TABLE cmdb_resource_applications
 ADD COLUMN provenance text NOT NULL DEFAULT 'discovered'
 CHECK (provenance IN ('discovered','manual'));

-- Older V3 audits recorded that a link was made but not the resource ID. Only
-- recover an unambiguous single link for that service environment. Already
-- deleted links and environments with several links cannot be reconstructed.
UPDATE cmdb_resource_applications l SET provenance='manual'
FROM application_environments e, applications app,
 v3_service_environment_profiles profile, cmdb_resources r
WHERE e.id=l.environment_id AND e.application_id=l.application_id
 AND app.id=l.application_id
 AND profile.application_id=l.application_id AND profile.environment_id=e.key
 AND r.id=l.resource_id AND r.kind='deployment' AND r.source_kind='kubernetes'
 AND lower(r.name)=lower(app.name)
 AND r.attributes->>'namespace'=profile.namespace
 AND r.attributes->>'inventory_presence'='present'
 AND r.external_id<>'' AND r.attributes->>'uid'=r.external_id
 AND (SELECT count(*) FROM cmdb_resource_applications other
      WHERE other.application_id=l.application_id AND other.environment_id=l.environment_id)=1
 AND EXISTS (
  SELECT 1 FROM management_audit audit
  WHERE audit.application_id=l.application_id
   AND audit.action IN ('v3.service.created','v3.service.environment_linked')
   AND audit.outcome='success'
   AND audit.changes->>'environment'=e.key
   AND audit.changes->>'resource_linked'='true'
   AND audit.created_at>=r.created_at
 );
