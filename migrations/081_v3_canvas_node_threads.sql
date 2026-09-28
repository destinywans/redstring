-- A generic canvas node owns its conversation. A pending exchange is unique
-- per node so concurrent requests cannot fork the same context silently.
CREATE TABLE v3_canvas_node_exchanges (
 id text PRIMARY KEY,
 sort_order bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
 canvas_id text NOT NULL REFERENCES v3_canvases(id) ON DELETE CASCADE,
 node_id text NOT NULL,
 user_id text NOT NULL REFERENCES users(id),
 author text NOT NULL,
 user_text text NOT NULL CHECK (char_length(user_text) BETWEEN 1 AND 8192),
 assistant_text text,
 model_id text NOT NULL,
 model_name text NOT NULL,
 model_provider text NOT NULL,
 reasoning text NOT NULL DEFAULT '',
 state text NOT NULL CHECK (state IN ('running','completed','failed')),
 created_at timestamptz NOT NULL DEFAULT now(),
 lease_expires_at timestamptz NOT NULL,
 completed_at timestamptz,
 CHECK ((state='completed' AND assistant_text IS NOT NULL AND completed_at IS NOT NULL)
    OR (state<>'completed' AND assistant_text IS NULL AND completed_at IS NULL))
);
CREATE UNIQUE INDEX v3_canvas_node_one_running
 ON v3_canvas_node_exchanges(canvas_id,node_id) WHERE state='running';
CREATE INDEX v3_canvas_node_exchanges_ordered
 ON v3_canvas_node_exchanges(canvas_id,node_id,sort_order);
