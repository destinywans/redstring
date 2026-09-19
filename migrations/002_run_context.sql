ALTER TABLE runs ADD COLUMN context_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE runs ADD CONSTRAINT runs_context_revision FOREIGN KEY(branch_id,context_revision) REFERENCES context_revisions(branch_id,revision);
