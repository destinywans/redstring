-- A human-pinned observation has no inference Run; provenance stays explicit.
ALTER TABLE evidence ALTER COLUMN run_id DROP NOT NULL;
