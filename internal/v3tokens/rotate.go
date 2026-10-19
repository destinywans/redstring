package v3tokens

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

var ErrTokenInactive = errors.New("token is expired, revoked or already rotated")

type RotationResult struct {
	ID              string    `json:"id"`
	SecretShownOnce string    `json:"secret_shown_once"`
	OldExpiresAt    time.Time `json:"old_expires_at"`
}

// Rotate keeps the old credential alive for at most 24h, never beyond its
// original expiry. The successor keeps the original lifespan and read scopes.
func (s *Service) Rotate(ctx context.Context, actor Actor, id string) (RotationResult, error) {
	var out RotationResult
	if !s.ready() {
		return out, ErrUnavailable
	}
	if !tokenID.MatchString(id) {
		return out, store.ErrNotFound
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if err = lockGovernance(ctx, tx); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, issueLockKey(actor.User.ID, actor.EnvironmentID)); err != nil {
		return out, err
	}
	if err = currentHuman(ctx, tx, actor); err != nil {
		return out, err
	}
	var name string
	var scopes []string
	var ttl float64
	var active bool
	err = tx.QueryRow(ctx, `SELECT name,scopes,extract(epoch FROM expires_at-created_at),revoked_at IS NULL AND rotated_at IS NULL AND expires_at>clock_timestamp() FROM v3_api_tokens WHERE id=$1 AND owner_id=$2 AND environment_id=$3 FOR UPDATE`, id, actor.User.ID, actor.EnvironmentID).Scan(&name, &scopes, &ttl, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, store.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if !active {
		return out, ErrTokenInactive
	}
	// Only currently supported scopes/lifespans can produce a successor.
	if _, err = validateIssue(IssueInput{Name: name, Scopes: scopes, TTL: int64(ttl)}); err != nil {
		return out, err
	}
	for _, decision := range []rbac.V3Decision{{Module: "connectors", Action: "manage", EnvironmentID: actor.EnvironmentID}, {Module: "connectors", Action: "view", EnvironmentID: actor.EnvironmentID}} {
		if err = s.Access.AuthorizeWithTx(ctx, tx, actor.User, decision); err != nil {
			return out, err
		}
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM v3_api_tokens WHERE owner_id=$1 AND environment_id=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, actor.User.ID, actor.EnvironmentID).Scan(&count); err != nil {
		return out, err
	}
	if count >= maxActiveTokens {
		return out, ErrTokenLimit
	}
	plain, digest, err := newSecret()
	if err != nil {
		return out, err
	}
	out.ID = store.ID()
	err = tx.QueryRow(ctx, `UPDATE v3_api_tokens SET rotated_at=clock_timestamp(),expires_at=LEAST(expires_at,clock_timestamp()+interval '24 hours') WHERE id=$1 RETURNING expires_at`, id).Scan(&out.OldExpiresAt)
	if err != nil {
		return RotationResult{}, err
	}
	_, err = tx.Exec(ctx, `WITH stamp AS (SELECT clock_timestamp() AS at) INSERT INTO v3_api_tokens(id,owner_id,environment_id,name,scopes,secret_hash,created_at,expires_at) SELECT $1,$2,$3,$4,$5::text[],$6,at,at+$7::bigint*interval '1 second' FROM stamp`, out.ID, actor.User.ID, actor.EnvironmentID, name, scopes, digest[:], int64(ttl))
	if err != nil {
		return RotationResult{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user','','v3.api_token.rotate',$2,'success',jsonb_build_object('environment_id',$3::text,'old_token_id',$4::text,'old_expires_at',$5::timestamptz,'scopes',$6::text[]))`, actor.User.ID, out.ID, actor.EnvironmentID, id, out.OldExpiresAt, scopes)
	if err != nil {
		return RotationResult{}, err
	}
	if err = sessionStillActive(ctx, tx, actor); err != nil {
		return RotationResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return RotationResult{}, err
	}
	out.SecretShownOnce = plain
	return out, nil
}
func (s *Service) rotateHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	actor, ok := tokenActor(w, r)
	if !ok {
		return
	}
	result, err := s.Rotate(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		tokenError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, result)
}
