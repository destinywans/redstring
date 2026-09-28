-- Legacy MCP extensions are application-scoped and cannot be safely assigned
-- to a V3 environment by copying every application/environment mapping.
-- Bindings require a reviewed, explicit enrollment; historical registrations
-- intentionally receive no automatic binding.
CREATE TABLE v3_mcp_environment_bindings (
 extension_id text NOT NULL,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 bound_by text NOT NULL REFERENCES users(id),
 bound_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (extension_id,environment_id),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id) ON DELETE CASCADE
);
CREATE INDEX v3_mcp_environment_bindings_application
 ON v3_mcp_environment_bindings(application_id,environment_id);
