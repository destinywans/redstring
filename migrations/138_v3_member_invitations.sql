-- An invitation is not an account or a grant. Only a relay-accepted, unexpired
-- one-time code may open a local viewer account; scope is granted separately.
CREATE TABLE v3_member_invitations (
 id text PRIMARY KEY,
 email text NOT NULL CHECK (email=lower(email) AND length(email) BETWEEN 3 AND 320),
 role text NOT NULL CHECK (role='viewer'),
 environment_id text NOT NULL CHECK (environment_id<>'' AND environment_id<>'all'),
 inviter_id text NOT NULL REFERENCES users(id),
 token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
 status text NOT NULL CHECK (status IN ('pending','delivered','superseded','accepted')),
 account_id text UNIQUE REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 delivered_at timestamptz,
 accepted_at timestamptz,
 CHECK (expires_at>created_at AND expires_at<=created_at+interval '48 hours'),
 CHECK ((status='pending' AND delivered_at IS NULL AND accepted_at IS NULL AND account_id IS NULL)
    OR (status='delivered' AND delivered_at IS NOT NULL AND accepted_at IS NULL AND account_id IS NULL)
    OR (status='superseded' AND accepted_at IS NULL AND account_id IS NULL)
    OR (status='accepted' AND delivered_at IS NOT NULL AND accepted_at IS NOT NULL AND account_id IS NOT NULL))
);
CREATE UNIQUE INDEX v3_member_invitation_active_email
 ON v3_member_invitations(email) WHERE status IN ('pending','delivered');
CREATE INDEX v3_member_invitation_actor_limit
 ON v3_member_invitations(inviter_id,created_at);
CREATE INDEX v3_member_invitation_email_limit
 ON v3_member_invitations(email,created_at);
