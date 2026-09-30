-- One process owns the model configuration. Observations are evidence from a
-- bounded metadata check, not proof of generation or continuing availability.
-- No endpoint, credential, provider response, model input, or output is stored.
CREATE TABLE v3_model_observations (
 environment_id text NOT NULL,
 model_id text NOT NULL CHECK (length(model_id) BETWEEN 1 AND 320),
 process_instance_id text NOT NULL CHECK (length(process_instance_id)=32),
 status text NOT NULL CHECK (status IN ('verified','error','unsupported')),
 checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 latency_ms bigint NOT NULL CHECK (latency_ms >= 0),
 error_code text NOT NULL DEFAULT '' CHECK (length(error_code) <= 80),
 checked_by text NOT NULL REFERENCES users(id),
 PRIMARY KEY (environment_id,model_id)
);

INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','models','manage','global','*'
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
