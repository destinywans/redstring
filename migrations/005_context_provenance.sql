ALTER TABLE branches ADD COLUMN inherited_parent_revision bigint;
ALTER TABLE branches ADD COLUMN synced_parent_revision bigint;
UPDATE branches b SET inherited_parent_revision=(SELECT (e.payload->>'parent_revision')::bigint FROM events e WHERE e.branch_id=b.id AND e.type='branch.forked' ORDER BY seq LIMIT 1) WHERE parent_id IS NOT NULL;
UPDATE branches SET synced_parent_revision=inherited_parent_revision WHERE parent_id IS NOT NULL;
ALTER TABLE context_revisions ADD COLUMN provenance jsonb NOT NULL DEFAULT '{"kind":"legacy"}';
UPDATE context_revisions r SET provenance=jsonb_build_object('kind','fork','source_branch_id',b.parent_id,'source_revision',b.inherited_parent_revision) FROM branches b WHERE r.branch_id=b.id AND r.revision=0 AND b.parent_id IS NOT NULL;
ALTER TABLE merge_previews ADD COLUMN evidence jsonb NOT NULL DEFAULT '[]';
