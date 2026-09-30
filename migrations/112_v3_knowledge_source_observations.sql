-- Hashes identify source items without persisting their private titles,
-- external IDs, bodies, model inputs, or credentials.
CREATE TABLE v3_knowledge_source_observations (
 read_id text NOT NULL REFERENCES v3_knowledge_source_reads(id) ON DELETE RESTRICT,
 item_hash text NOT NULL CHECK (item_hash ~ '^[a-f0-9]{64}$'),
 content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
 environment_id text NOT NULL,
 application_id text,
 team_id text,
 PRIMARY KEY (read_id,item_hash)
);
CREATE INDEX v3_knowledge_source_observations_item
 ON v3_knowledge_source_observations(item_hash,read_id);
