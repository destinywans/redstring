-- Presentation state is separate from immutable investigation context
-- revisions. Legacy branches without a row receive a deterministic read-only
-- position until a V3 edit writes their first explicit layout.
CREATE TABLE v3_investigation_node_layouts (
 branch_id text PRIMARY KEY REFERENCES branches(id) ON DELETE CASCADE,
 x integer NOT NULL CHECK (x BETWEEN -100000 AND 100000),
 y integer NOT NULL CHECK (y BETWEEN -100000 AND 100000),
 w integer NOT NULL DEFAULT 260 CHECK (w BETWEEN 120 AND 2000),
 collapsed boolean NOT NULL DEFAULT false,
 version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 updated_by text REFERENCES users(id)
);
