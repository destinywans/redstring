-- V3-created investigations have a known human creator. Historical rows are
-- left NULL because neither their creator nor their participants can be
-- reconstructed reliably from layouts, branches, or application ownership.
ALTER TABLE investigations ADD COLUMN created_by text REFERENCES users(id);
-- The V3 creation request has a goal; keep it as submitted input instead of
-- disguising it as a branch message or dropping it from durable history.
ALTER TABLE investigations ADD COLUMN goal text NOT NULL DEFAULT '';
