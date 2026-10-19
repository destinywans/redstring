package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type routeTreeSource interface {
	ObserveRouteTree(context.Context, string, string) (RouteTreeObservation, error)
}

type routeTreeResponse struct {
	EnvironmentID  string         `json:"environment_id"`
	Source         string         `json:"source"`
	ObservedAt     string         `json:"observed_at"`
	ConfigRevision *string        `json:"config_revision"`
	Root           *RouteTreeNode `json:"root"`
	Partial        bool           `json:"partial"`
	Unavailable    []string       `json:"unavailable"`
	// RootUnavailable is the reason Root is null: not_configured when the
	// Alertmanager config has no exact branch for this environment (PA-09).
	RootUnavailable *string `json:"root_unavailable"`
}

// NotificationRoutes reads a sanitized routing subtree from the currently
// configured Alertmanager. Receiver identities and matcher values are opaque
// in the response; raw config and delivery targets never leave this source.
func (s *Service) NotificationRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment to view routing")
		return
	}
	if s == nil || s.Access == nil || s.Catalog == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization is unavailable")
		return
	}
	env := scope.EnvironmentIDs[0]
	if status, err := s.routeTreeReader(r.Context(), httpapi.CurrentUser(r), env); err != nil {
		httpapi.Error(w, status, routeTestErrorCode(status), "Routing access is unavailable or denied")
		return
	}
	db, txAccess := routeTestAuthority(s.Catalog, s.Access)
	if db == nil || txAccess == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization snapshot is unavailable")
		return
	}
	envLabel := s.EnvironmentLabels[env]
	if envLabel == "" || envLabel == "application" || !alertLabelName.MatchString(envLabel) {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An exact environment label is not configured for routing")
		return
	}
	source, ok := s.Source.(routeTreeSource)
	client, isClient := s.Source.(*Client)
	if !ok || source == nil || isClient && client == nil {
		httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An environment-bound Alertmanager route source is not connected")
		return
	}
	observation, err := source.ObserveRouteTree(r.Context(), env, envLabel)
	noRoute := errors.Is(err, ErrNoEnvironmentRoute)
	if err != nil && !noRoute {
		if errors.Is(err, ErrNotConfigured) {
			httpapi.Error(w, http.StatusNotImplemented, "not_connected", "An environment-bound Alertmanager route source is not connected")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "route_source_unavailable", "Routing tree is unavailable or cannot be scoped exactly")
		}
		return
	}
	if !noRoute && (observation.ObservedAt.IsZero() || len(observation.ConfigSHA256) != 64) || s.EnvironmentLabels[env] != envLabel {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_changed", "Routing scope changed during source read")
		return
	}
	if err := routeTreeFinalAuthority(r, db, txAccess, env); err != nil {
		switch {
		case errors.Is(err, store.ErrSessionRevoked):
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		case errors.Is(err, store.ErrForbidden):
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Routing access changed during source read")
		default:
			httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_changed", "Routing scope changed during source read")
		}
		return
	}
	unavailable := []string{"matcher_values", "receiver_names", "receiver_targets", "channel_types", "hits_24h", "on_call", "escalation", "current_interval_activity"}
	if !s.routeDraftsReady(env) {
		unavailable = append(unavailable, "route_drafts")
	}
	if noRoute {
		// The configuration has no exact branch for this environment: a
		// deployment state, not an outage. No revision is claimed for it.
		reason := "not_configured"
		httpapi.WriteJSON(w, http.StatusOK, routeTreeResponse{EnvironmentID: env, Source: "alertmanager",
			ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Partial: true,
			Unavailable: append([]string{"root"}, unavailable...), RootUnavailable: &reason})
		return
	}
	revision := "sha256:" + observation.ConfigSHA256
	w.Header().Set("ETag", `"`+revision+`"`)
	root := observation.Root
	httpapi.WriteJSON(w, http.StatusOK, routeTreeResponse{
		EnvironmentID: env, Source: "alertmanager", ObservedAt: observation.ObservedAt.UTC().Format(time.RFC3339Nano),
		ConfigRevision: &revision, Root: &root, Partial: true, Unavailable: unavailable,
	})
}

// routeTreeReader authorizes reading the sanitized tree: alert_rules.view is
// enough, as the route is marked (PA-56). Testing and drafting routes still
// need manage through routeTestManager.
func (s *Service) routeTreeReader(ctx context.Context, user core.User, env string) (int, error) {
	err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "alert_rules", Action: "view", EnvironmentID: env})
	if errors.Is(err, store.ErrForbidden) {
		return http.StatusForbidden, err
	}
	if err != nil {
		return http.StatusServiceUnavailable, err
	}
	return http.StatusOK, nil
}

func routeTreeFinalAuthority(r *http.Request, db *pgxpool.Pool, access routeTxAccess, env string) error {
	ctx := r.Context()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	user := httpapi.CurrentUser(r)
	if err := store.LockActiveSession(ctx, tx, cookie.Value, user.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,rbac_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return err
	}
	if err := access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "alert_rules", Action: "view", EnvironmentID: env}); err != nil {
		return err
	}
	var unexpired bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1 AND user_id=$2`,
		store.Hash(cookie.Value), user.ID).Scan(&unexpired)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !unexpired {
		return store.ErrSessionRevoked
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
