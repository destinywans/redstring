-- Existing registrations retain their previous behavior. New registrations
-- start disabled unless the service can establish their complete scope.
ALTER TABLE v3_connectors
 ADD COLUMN enabled boolean NOT NULL DEFAULT true;
ALTER TABLE v3_connectors
 ALTER COLUMN enabled SET DEFAULT false;
