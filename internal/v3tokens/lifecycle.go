package v3tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

const secretPrefix = "aiops_pat_"

func newSecret() (string, [32]byte, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", [32]byte{}, err
	}
	plain := secretPrefix + base64.RawURLEncoding.EncodeToString(random[:])
	return plain, sha256.Sum256([]byte(plain)), nil
}

// Issue checks the current human session, connector-management permission and
// every requested V3 read permission in the same transaction as the hash and
// audit insert. The raw secret exists only in this return value.
func (s *Service) Issue(ctx context.Context, actor Actor, in IssueInput) (IssueResult, error) {
	var out IssueResult
	if !s.ready() {
		return out, ErrUnavailable
	}
	scopes, err := validateIssue(in)
	if err != nil {
		return out, err
	}
	if !validActor(actor) {
		return out, store.ErrSessionRevoked
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockGovernance(ctx, tx); err != nil {
		return out, err
	}
	// Serialize the active-token cap for this owner and environment without
	// serializing independent owners. This is not the credential lookup key.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"v3.api_tokens.issue\x00"+actor.User.ID+"\x00"+actor.EnvironmentID); err != nil {
		return out, err
	}
	if err = currentHuman(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = s.Access.AuthorizeWithTx(ctx, tx, actor.User, rbac.V3Decision{
		Module: "connectors", Action: "manage", EnvironmentID: actor.EnvironmentID,
	}); err != nil {
		return out, err
	}
	for _, scope := range scopes {
		if err = s.Access.AuthorizeWithTx(ctx, tx, actor.User, rbac.V3Decision{
			Module: allowedScopes[scope], Action: "view", EnvironmentID: actor.EnvironmentID,
		}); err != nil {
			return out, err
		}
	}
	var active int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_api_tokens
	 WHERE owner_id=$1 AND environment_id=$2 AND revoked_at IS NULL
	 AND expires_at>clock_timestamp()`, actor.User.ID, actor.EnvironmentID).Scan(&active)
	if err != nil {
		return out, err
	}
	if active >= maxActiveTokens {
		return out, ErrTokenLimit
	}
	plain, digest, err := newSecret()
	if err != nil {
		return out, err
	}
	out.ID = store.ID()
	out.Name, out.EnvironmentID, out.Scopes = in.Name, actor.EnvironmentID, scopes
	err = tx.QueryRow(ctx, `WITH stamp AS (SELECT clock_timestamp() AS at)
	 INSERT INTO v3_api_tokens
	 (id,owner_id,environment_id,name,scopes,secret_hash,created_at,expires_at)
	 SELECT $1,$2,$3,$4,$5::text[],$6,stamp.at,
	        stamp.at+$7::bigint*interval '1 second' FROM stamp
	 RETURNING created_at,expires_at`, out.ID, actor.User.ID, actor.EnvironmentID,
		in.Name, scopes, digest[:], in.TTL).Scan(&out.CreatedAt, &out.ExpiresAt)
	if err != nil {
		return IssueResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit
	 (actor_id,actor_kind,application_id,action,target,outcome,changes)
	 VALUES($1,'user','','v3.api_token.create',$2,'success',
	 jsonb_build_object('environment_id',$3::text,'scopes',$4::text[],
	 'ttl_seconds',$5::bigint))`, actor.User.ID, out.ID, actor.EnvironmentID, scopes, in.TTL)
	if err != nil {
		return IssueResult{}, err
	}
	if err = sessionStillActive(ctx, tx, actor); err != nil {
		return IssueResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IssueResult{}, err
	}
	out.SecretShownOnce = plain
	return out, nil
}

// List returns only this human owner's metadata for the one selected
// environment. Active entries sort first so all currently usable tokens fit
// within the 50-token cap even when revoked history exceeds the list limit.
// There is no secret or digest in the projection. has_more prevents silent
// truncation of the older history.
func (s *Service) List(ctx context.Context, actor Actor) (TokenList, error) {
	out := TokenList{Items: []TokenView{}}
	if !s.ready() {
		return out, ErrUnavailable
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = currentHuman(ctx, tx, actor); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,environment_id,scopes,created_at,expires_at,revoked_at
	 FROM v3_api_tokens WHERE owner_id=$1 AND environment_id=$2
	 ORDER BY (revoked_at IS NULL AND expires_at>statement_timestamp()) DESC,
	          created_at DESC,id DESC LIMIT 101`, actor.User.ID, actor.EnvironmentID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		if len(out.Items) == listLimit {
			out.HasMore = true
			break
		}
		var item TokenView
		if err = rows.Scan(&item.ID, &item.Name, &item.EnvironmentID,
			&item.Scopes, &item.CreatedAt, &item.ExpiresAt, &item.RevokedAt); err != nil {
			rows.Close()
			return TokenList{}, err
		}
		out.Items = append(out.Items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return TokenList{}, err
	}
	rows.Close()
	if err = sessionStillActive(ctx, tx, actor); err != nil {
		return TokenList{}, err
	}
	return out, tx.Commit(ctx)
}

// Revoke is idempotent for an owned token. Foreign or missing IDs share the
// same not-found result; repeat revocation does not add a duplicate audit.
func (s *Service) Revoke(ctx context.Context, actor Actor, id string) error {
	if !s.ready() {
		return ErrUnavailable
	}
	if !tokenID.MatchString(id) {
		return store.ErrNotFound
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = currentHuman(ctx, tx, actor); err != nil {
		return err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT revoked_at IS NULL FROM v3_api_tokens
	 WHERE id=$1 AND owner_id=$2 AND environment_id=$3 FOR UPDATE`,
		id, actor.User.ID, actor.EnvironmentID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if active {
		if _, err = tx.Exec(ctx, `UPDATE v3_api_tokens SET revoked_at=clock_timestamp()
		 WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO management_audit
		 (actor_id,actor_kind,application_id,action,target,outcome,changes)
		 VALUES($1,'user','','v3.api_token.revoke',$2,'success',
		 jsonb_build_object('environment_id',$3::text))`,
			actor.User.ID, id, actor.EnvironmentID); err != nil {
			return err
		}
	}
	if err = sessionStillActive(ctx, tx, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
