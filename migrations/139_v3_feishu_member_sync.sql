-- One configured Feishu application/binding owns its external-ID namespace.
-- Sequence committed snapshots by database time so an older concurrent read
-- cannot overwrite a newer completed import on another server instance.
CREATE TABLE v3_feishu_member_sync_state (
 binding_id text PRIMARY KEY CHECK (binding_id ~ '^[A-Za-z0-9_-]{1,40}$'),
 app_id_hash text NOT NULL CHECK (app_id_hash ~ '^[0-9a-f]{64}$'),
 root_department_id text NOT NULL CHECK (root_department_id='0'),
 last_started_at timestamptz NOT NULL,
 last_completed_at timestamptz NOT NULL,
 last_fingerprint text NOT NULL CHECK (last_fingerprint ~ '^[0-9a-f]{64}$'),
 last_observed integer NOT NULL CHECK (last_observed BETWEEN 1 AND 10000)
);
