-- The policy authority is external to PostgreSQL. These rows record requests
-- and verified readbacks; they do not themselves activate a security setting.
CREATE TABLE v3_security_policy_operations (
 id text PRIMARY KEY CHECK (id <> ''),
 instance_id text NOT NULL CHECK (instance_id <> ''),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 actor_id text NOT NULL REFERENCES users(id),
 expected_revision bigint NOT NULL CHECK (expected_revision > 0),
 requested_controls jsonb NOT NULL CHECK (jsonb_typeof(requested_controls) = 'object'),
 status text NOT NULL DEFAULT 'pending'
   CHECK (status IN ('pending', 'applied', 'failed', 'unknown')),
 applied_revision bigint CHECK (applied_revision > expected_revision),
 error_code text NOT NULL DEFAULT '' CHECK (length(error_code) <= 128),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK ((status = 'applied') = (applied_revision IS NOT NULL))
);

-- A lost response may mean the authority applied the change. Keep the scope
-- blocked until a deliberate reconciliation has established its actual state.
CREATE UNIQUE INDEX v3_security_policy_one_unresolved_operation
 ON v3_security_policy_operations (instance_id, environment_id)
 WHERE status IN ('pending', 'unknown');
CREATE INDEX v3_security_policy_operation_history
 ON v3_security_policy_operations (instance_id, environment_id, created_at DESC, id DESC);

-- Only write this checkpoint after a complete, fresh authority readback has
-- matched the requested controls. GET still reads the authority each time.
CREATE TABLE v3_security_policy_applied (
 instance_id text NOT NULL CHECK (instance_id <> ''),
 environment_id text NOT NULL CHECK (environment_id <> '' AND environment_id <> 'all'),
 runtime_revision bigint NOT NULL CHECK (runtime_revision > 0),
 controls jsonb NOT NULL CHECK (jsonb_typeof(controls) = 'object'),
 operation_id text NOT NULL UNIQUE REFERENCES v3_security_policy_operations(id),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (instance_id, environment_id)
);
