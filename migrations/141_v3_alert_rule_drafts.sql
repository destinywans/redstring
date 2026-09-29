-- Alert-rule edits are immutable, nondeployable review proposals. Neither this
-- ledger nor its GitLab JSON files are read by a Prometheus rule loader.
CREATE TABLE v3_alert_rule_drafts (
 id text PRIMARY KEY,
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 application_id text NOT NULL REFERENCES applications(id),
 provider_file text NOT NULL,
 provider_group text NOT NULL,
 alert_name text NOT NULL,
 observed_rule_id text NOT NULL DEFAULT '',
 observed_digest text NOT NULL DEFAULT '',
 current_version integer NOT NULL CHECK (current_version>=1),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(environment_id,application_id,provider_file,provider_group,alert_name),
 CHECK (observed_digest='' OR observed_digest ~ '^[0-9a-f]{64}$'),
 CHECK ((observed_rule_id='')=(observed_digest=''))
);
CREATE INDEX v3_alert_rule_drafts_scope ON v3_alert_rule_drafts(environment_id,application_id,updated_at DESC);

-- One idempotency identity reserves the exact remote branch, path and bytes.
-- A timed-out GitLab write is retried against that same identity.
CREATE TABLE v3_alert_rule_draft_operations (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 operation text NOT NULL CHECK (operation IN ('create','update','import')),
 idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
 request_digest text NOT NULL CHECK (request_digest ~ '^[0-9a-f]{64}$'),
 environment_id text NOT NULL,
 application_id text NOT NULL REFERENCES applications(id),
 payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object' AND octet_length(payload::text)<=262144),
 response jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(response)='object'),
 branch text NOT NULL UNIQUE,
 path text NOT NULL UNIQUE,
 project_id bigint NOT NULL CHECK (project_id>0),
 state text NOT NULL CHECK (state IN ('reserved','exporting','uncertain','pending_review','rejected','stale_remote','duplicates_only')),
 attempt_generation bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 mr_iid bigint CHECK (mr_iid>0),
 mr_url text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor_id,operation,idempotency_key)
);
CREATE INDEX v3_alert_rule_operations_scope ON v3_alert_rule_draft_operations(environment_id,application_id,created_at DESC);

CREATE TABLE v3_alert_rule_draft_revisions (
 draft_id text NOT NULL REFERENCES v3_alert_rule_drafts(id),
 version integer NOT NULL CHECK (version>=1),
 operation_id text NOT NULL REFERENCES v3_alert_rule_draft_operations(id),
 spec jsonb NOT NULL CHECK (jsonb_typeof(spec)='object'),
 spec_digest text NOT NULL CHECK (spec_digest ~ '^[0-9a-f]{64}$'),
 actor_id text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(draft_id,version),
 UNIQUE(draft_id,operation_id)
);

-- Existing administrators may review a draft; operators still require an
-- explicit application, team or environment scoped grant.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','alert_rules',action,'global','*'
FROM (VALUES ('view'),('change')) AS permissions(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
