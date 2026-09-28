-- V3 proposal reviews are distinct from historical rollback action_plans.
-- Only an internal producer with evidence for every required field may create
-- a row. No approval or execution is enabled by this migration.
CREATE TABLE v3_proposals (
 id text PRIMARY KEY,
 investigation_id text NOT NULL REFERENCES investigations(id),
 application_id text NOT NULL REFERENCES applications(id),
 environment_id text NOT NULL,
 content jsonb NOT NULL CHECK (jsonb_typeof(content)='object'),
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','rejected')),
 created_by text NOT NULL,
 creator_kind text NOT NULL CHECK (creator_kind IN ('user','agent')),
 created_at timestamptz NOT NULL DEFAULT now(),
 rejected_by text REFERENCES users(id),
 rejected_at timestamptz,
 rejection_reason text NOT NULL DEFAULT '',
 FOREIGN KEY (application_id,environment_id)
   REFERENCES v3_application_environments(application_id,environment_id),
 CHECK ((status='pending' AND rejected_by IS NULL AND rejected_at IS NULL)
     OR (status='rejected' AND rejected_by IS NOT NULL AND rejected_at IS NOT NULL))
);
CREATE INDEX v3_proposals_scope ON v3_proposals(environment_id,application_id,created_at DESC);

CREATE TABLE v3_proposal_comments (
 id text PRIMARY KEY,
 proposal_id text NOT NULL REFERENCES v3_proposals(id),
 author_id text NOT NULL REFERENCES users(id),
 body text NOT NULL CHECK (length(btrim(body)) BETWEEN 1 AND 4000),
 idempotency_key text,
 request_hash text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (proposal_id,author_id,idempotency_key)
);
CREATE INDEX v3_proposal_comments_order ON v3_proposal_comments(proposal_id,created_at,id);

-- No approval grant is seeded: V3 approval still needs passkey-bound authority.
INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
SELECT 'admin','proposals',action,'global','*'
FROM (VALUES ('view'),('change'),('manage')) AS permissions(action)
WHERE EXISTS (SELECT 1 FROM rbac_roles WHERE id='admin')
ON CONFLICT DO NOTHING;
