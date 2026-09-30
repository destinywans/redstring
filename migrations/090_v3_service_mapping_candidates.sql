-- Discovery records a short-lived, reviewable projection of an already
-- enrolled external target. The source row is deliberately not a foreign key:
-- deleting or changing the target must leave a stale candidate that accept
-- can reject without silently choosing another object.
CREATE TABLE v3_service_mapping_candidates (
 id text PRIMARY KEY,
 application_id text NOT NULL,
 environment_id text NOT NULL,
 service_key text NOT NULL,
 system_id text NOT NULL CHECK (system_id='code'),
 source_kind text NOT NULL CHECK (source_kind='explicit_target'),
 source_id text NOT NULL,
 source_bound_at timestamptz NOT NULL,
 connector_id text NOT NULL,
 external_id text NOT NULL,
 binding_revision bigint NOT NULL,
 discovered_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 accepted_at timestamptz,
 accepted_by text REFERENCES users(id),
 FOREIGN KEY (application_id,environment_id)
  REFERENCES v3_application_environments(application_id,environment_id) ON DELETE CASCADE,
 CHECK ((accepted_at IS NULL)=(accepted_by IS NULL))
);
-- Accepted rows remain for review while the application environment exists.
-- The management_audit entry is the durable decision record after its removal.
-- A later explicit removal may produce a new pending candidate for that target.
CREATE UNIQUE INDEX v3_service_mapping_candidate_pending_unique
 ON v3_service_mapping_candidates(application_id,environment_id,system_id,source_kind,source_id)
 WHERE accepted_at IS NULL;
CREATE INDEX v3_service_mapping_candidates_scope
 ON v3_service_mapping_candidates(environment_id,service_key,system_id,expires_at);
CREATE INDEX v3_service_mapping_candidates_expiry
 ON v3_service_mapping_candidates(environment_id,expires_at) WHERE accepted_at IS NULL;
-- Discovery and acceptance look up the connector's historical project
-- activation. Keep this bounded as the append-only management audit grows.
CREATE INDEX v3_mapping_connector_activation_evidence
 ON management_audit(target, (changes->>'environment'), created_at DESC)
 WHERE action='connector.activated' AND outcome='success';
