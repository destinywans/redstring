-- Each Agent governance write consumes a revision. Clients must present the
-- revision they read so a stale release or level intent cannot overwrite a
-- newer persisted configuration. Existing rows start at revision 1.
ALTER TABLE v3_agents
 ADD COLUMN lifecycle_revision bigint NOT NULL DEFAULT 1
 CHECK (lifecycle_revision > 0);
