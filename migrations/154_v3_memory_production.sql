-- A deterministic semantic key prevents retries (including another authorized
-- human's identical excerpt) from manufacturing duplicate memory candidates.
-- Historical rows keep NULL: do not rewrite or collapse earlier decisions.
ALTER TABLE v3_memory_facts ADD COLUMN production_key text
 CHECK (production_key IS NULL OR production_key ~ '^[a-f0-9]{64}$');
CREATE UNIQUE INDEX v3_memory_facts_production_key ON v3_memory_facts(production_key)
 WHERE production_key IS NOT NULL;
ALTER TABLE v3_memory_conflicts ADD COLUMN reason text NOT NULL DEFAULT ''
 CHECK (length(reason)<=1000);
