package v3changeview

import (
	"context"
	"errors"
	"net/http"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

// Account, legacy RBAC and V3 role writers acquire 992 then 995 before row
// locks or grant changes. Use that same order before locking the session row,
// avoiding a cycle with account disable or association writes. The table
// locks also cover direct grant/association writes that do not take those
// advisory locks, including newly inserted rows; SHARE conflicts with their
// ROW EXCLUSIVE lock until the review audit commits.
func lockChangeReviewAuthoritySources(ctx context.Context, tx pgx.Tx) error {
	for _, key := range []int64{752018992, 752018995} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles, rbac_department_roles,
	 v3_role_grants, rbac_roles IN SHARE MODE`)
	return err
}

func (s *Service) changeReviewAction(ctx context.Context, tx pgx.Tx, user core.User, row serviceRow, module, action string) error {
	decision := rbac.V3Decision{Module: module, Action: action,
		EnvironmentID: row.EnvKey, ApplicationID: row.AppID}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && row.TeamID != "" {
		decision.ApplicationID = ""
		decision.TeamID = row.TeamID
		err = s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	}
	return err
}

func (s *Service) authorizeChangeReview(ctx context.Context, tx pgx.Tx, r *http.Request, row serviceRow) error {
	user := httpapi.CurrentUser(r)
	if err := s.changeReviewAction(ctx, tx, user, row, "services", "view"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			return errReviewHidden
		}
		return err
	}
	for _, permission := range [][2]string{{"events", "view"}, {"services", "change"}} {
		if err := s.changeReviewAction(ctx, tx, user, row, permission[0], permission[1]); err != nil {
			return err
		}
	}
	var human bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants g
 WHERE g.user_id=$1 AND g.v3_identity_kind='human' AND g.role_kind<>'machine'
 AND g.module='services' AND g.action='change'
 AND ((g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=$2)
   OR (g.scope_kind='application' AND g.scope_id=$3)
   OR ($4<>'' AND g.scope_kind='team' AND g.scope_id=$4))
	)`, user.ID, row.EnvKey, row.AppID, row.TeamID).Scan(&human)
	if err != nil {
		return err
	}
	if !human {
		return store.ErrForbidden
	}
	return nil
}
