-- PA-65 (production acceptance, 2026-10-10): a saved postmortem draft can be
-- edited and submitted for review under an optimistic revision (If-Match).
-- Existing rows start at revision 1. Not executed on the existing business
-- database; the acceptance instances apply it on deployment.
ALTER TABLE v3_postmortems
 ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
