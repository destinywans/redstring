-- Keep the V3 canvas investigation count and bounded branch preview indexed.
-- This runs in the migration transaction; lock duration on an existing large
-- branches table must be assessed before application to a business database.
CREATE INDEX branches_investigation_id_id ON branches(investigation_id,id);
