-- V3 connector registrations are scoped to one instance database and one
-- environment. Secrets live in Vault; only references are persisted here.
CREATE TABLE v3_connectors (
    id text PRIMARY KEY,
    environment_id text NOT NULL,
    provider text NOT NULL,
    name text NOT NULL,
    category text NOT NULL,
    endpoint text NOT NULL,
    auth_method text NOT NULL,
    credential_ref text NOT NULL DEFAULT '',
    webhook_requested boolean NOT NULL DEFAULT false,
    tools jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('ok', 'unverified', 'error')),
    last_checked_at timestamptz,
    last_latency_ms integer,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT v3_connectors_unique_endpoint UNIQUE(environment_id, provider, endpoint),
    CONSTRAINT v3_connectors_vault_reference CHECK (credential_ref = '' OR credential_ref ~ '^vault://[A-Za-z0-9_./-]+#[A-Za-z0-9_-]+$')
);
CREATE INDEX v3_connectors_environment_category ON v3_connectors(environment_id, category, name);
