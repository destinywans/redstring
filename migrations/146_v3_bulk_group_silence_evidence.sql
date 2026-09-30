-- A bulk silence proposal is tied to the exact locked local group and the
-- operator-attested Alertmanager identity label observed on its members.
-- This evidence does not approve or execute the proposed silence. The worker
-- must revalidate it against current group and connector state before use.
CREATE TABLE v3_bulk_group_silence_evidence (
 proposal_id text PRIMARY KEY REFERENCES v3_proposals(id),
 action_digest text NOT NULL CHECK (action_digest ~ '^[0-9a-f]{64}$'),
 group_id text NOT NULL CHECK (group_id ~ '^group:[0-9a-f]{24}$'),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 environment_row_id text NOT NULL CHECK (environment_row_id<>''),
 application_id text NOT NULL REFERENCES applications(id),
 source_id text NOT NULL CHECK (btrim(source_id)<>''),
 fingerprint text NOT NULL CHECK (btrim(fingerprint)<>''),
 investigation_id text NOT NULL REFERENCES investigations(id),
 identity_label_name text NOT NULL
   CHECK (length(identity_label_name)<=128 AND identity_label_name ~ '^[a-zA-Z_][a-zA-Z0-9_]*$'),
 identity_label_value text NOT NULL
   CHECK (length(identity_label_value) BETWEEN 1 AND 256),
 config_revision text NOT NULL
   CHECK (config_revision ~ '^[a-zA-Z0-9._/-]{1,128}$'),
 attestation_digest text NOT NULL CHECK (attestation_digest ~ '^[0-9a-f]{64}$'),
 endpoint_binding_fingerprint text NOT NULL
   CHECK (endpoint_binding_fingerprint ~ '^[0-9a-f]{64}$'),
 member_digest text NOT NULL CHECK (member_digest ~ '^[0-9a-f]{64}$'),
 attestation_expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK (attestation_expires_at>created_at),
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id),
 FOREIGN KEY (environment_row_id,application_id)
   REFERENCES application_environments(id,application_id)
);

CREATE FUNCTION v3_bulk_group_silence_evidence_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'v3 bulk group silence evidence is immutable';
END $$;
CREATE TRIGGER v3_bulk_group_silence_evidence_immutable
 BEFORE UPDATE OR DELETE ON v3_bulk_group_silence_evidence
 FOR EACH ROW EXECUTE FUNCTION v3_bulk_group_silence_evidence_immutable();
