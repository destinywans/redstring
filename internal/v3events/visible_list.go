package v3events

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

const (
	eventListLimit = 5000
	pageListLimit  = 500
	listFetchSize  = 256
	listScanBudget = 15 * time.Second
)

// scanVisibleList keeps one candidate statement snapshot and frees the
// connection between fetches for the caller's current authorization queries.
// Only authorized records consume the public cap. The deadline also bounds
// work for callers supplying a policy that cannot be narrowed in SQL.
func scanVisibleList[T any](ctx context.Context, tx pgx.Tx, limit int,
	scan func(pgx.Row) (T, error), visible func(context.Context, T) (bool, error), query string, args ...any,
) ([]T, error) {
	ctx, cancel := context.WithTimeout(ctx, listScanBudget)
	defer cancel()

	// The cursor lives within the caller's transaction. Exec mode binds
	// parameters in DECLARE without asking PostgreSQL to describe a portal.
	declareArgs := append([]any{pgx.QueryExecModeExec}, args...)
	if _, err := tx.Exec(ctx, "DECLARE v3_event_list NO SCROLL CURSOR FOR "+query, declareArgs...); err != nil {
		return nil, err
	}
	defer tx.Exec(ctx, "CLOSE v3_event_list")
	fetch := fmt.Sprintf("FETCH FORWARD %d FROM v3_event_list", listFetchSize)
	out := make([]T, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Each caller's cursor has a different result shape.
		rows, err := tx.Query(ctx, fetch, pgx.QueryExecModeExec)
		if err != nil {
			return nil, err
		}
		batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (T, error) {
			return scan(row)
		})
		if err != nil {
			return nil, err
		}
		for _, v := range batch {
			allowed, err := visible(ctx, v)
			if err != nil {
				return nil, err
			}
			if allowed {
				if len(out) == limit {
					return nil, errTooManyIncidents
				}
				out = append(out, v)
			}
		}
		if len(batch) < listFetchSize {
			return out, nil
		}
	}
}

// eventListGrants uses the SQL prefilter only where the composition root
// supplies the standard V3 matrix policy. Other authorizers still get the
// visible-row budget through scanVisibleList.
func (s *Service) eventListGrants(r *http.Request, env, app, team string, args ...any) (string, string, []any) {
	if !s.UseV3ListPrefilter {
		return "", "TRUE", args
	}
	if s.AuthorizeTeamTx == nil {
		team = ""
	}
	actor := fmt.Sprintf("$%d", len(args)+1)
	return rbac.V3ListGrantsCTE(actor),
		rbac.V3ListScopePredicate(env, app, team, "events", "view"),
		append(args, httpapi.CurrentUser(r).ID)
}

// visibleEventScope memoizes decisions within a single read transaction. A
// positive result remains current while the actor/session, mapping, owner and
// grant locks are held. Callers keep their final current-clock session check.
func (s *Service) visibleEventScope(r *http.Request, tx pgx.Tx, guard *listAuthorityGuard) func(context.Context, incidentRecord) (bool, error) {
	decisions := make(map[[3]string]bool)
	var request *http.Request
	return func(ctx context.Context, v incidentRecord) (bool, error) {
		key := [3]string{v.ApplicationID, v.EnvironmentID, v.EnvironmentKey}
		if allowed, ok := decisions[key]; ok {
			return allowed, nil
		}
		var err error
		if request == nil {
			request = r.WithContext(ctx)
			if guard != nil {
				guard.request = request
			}
		}
		if s.AuthorizeTx != nil {
			err = s.authorizeAlertWithGuard(request, tx, v, "view", guard)
		} else {
			err = s.scoped(request, v.ApplicationID, v.EnvironmentKey, "view")
		}
		if err != nil && !errors.Is(err, store.ErrForbidden) && !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		decisions[key] = err == nil
		return err == nil, nil
	}
}
