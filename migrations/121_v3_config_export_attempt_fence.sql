-- Every export lease claim advances its generation. A late result from an
-- expired lease may only change its own attempt, not a newer claimant's state
-- or audit trail. Migration 120 remains unchanged for already-applied DBs.
ALTER TABLE v3_config_exports
 ADD COLUMN attempt_generation bigint NOT NULL DEFAULT 0 CHECK (attempt_generation>=0);
