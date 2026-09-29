package v3alertmanager

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// routePolicySource reads a configured Alertmanager policy for one exact label
// set. Its result contains receiver names, never receiver targets or secrets.
type routePolicySource interface {
	ObserveGroupPolicy(context.Context, string, map[string]string) (GroupPolicyObservation, error)
}

type routeTxAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type routeTestResult struct {
	EnvironmentID  string   `json:"environment_id"`
	ApplicationID  string   `json:"application_id"`
	ReceiverNames  []string `json:"receiver_names"`
	GroupBy        []string `json:"group_by"`
	GroupWait      string   `json:"group_wait"`
	GroupInterval  string   `json:"group_interval"`
	RepeatInterval string   `json:"repeat_interval"`
	ObservedAt     string   `json:"observed_at"`
	Path           any      `json:"path"`
	Hits           any      `json:"hits"`
	ChannelTargets any      `json:"channel_targets"`
	Partial        bool     `json:"partial"`
	Unavailable    []string `json:"unavailable"`
}

// TestNotificationRoute evaluates a sample against the live configured policy.
// Only an environment-wide rule manager may probe arbitrary sample labels;
// application-scoped rights do not permit learning a shared routing tree.
func (s *Service) TestNotificationRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment to test routing")
		return
	}
	if s == nil || s.Access == nil || s.Catalog == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization is unavailable")
		return
	}
	env := scope.EnvironmentIDs[0]
	user := httpapi.CurrentUser(r)
	if status, err := s.routeTestManager(r.Context(), user, env); err != nil {
		httpapi.Error(w, status, routeTestErrorCode(status), "Routing access is unavailable or denied")
		return
	}
	db, txAccess := routeTestAuthority(s.Catalog, s.Access)
	if db == nil || txAccess == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing authorization snapshot is unavailable")
		return
	}
	var input struct {
		Labels map[string]string `json:"labels"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	envLabel := s.EnvironmentLabels[env]
	if envLabel == "" || envLabel == "application" || !alertLabelName.MatchString(envLabel) {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_unavailable", "Routing environment binding is unavailable")
		return
	}
	if len(input.Labels) == 0 || len(input.Labels) > maxPolicyMatchers {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_labels", "Provide a bounded alert label set")
		return
	}
	for name, value := range input.Labels {
		if !alertLabelName.MatchString(name) || len(value) > maxPolicyString || strings.ContainsAny(value, "\x00\r\n") {
			httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_labels", "Invalid alert label set")
			return
		}
	}
	providedEnv, hasEnv := input.Labels[envLabel]
	if input.Labels["application"] == "" || (hasEnv && providedEnv != env) || (!hasEnv && len(input.Labels) == maxPolicyMatchers) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_labels", "Exact bound application and selected environment labels are required")
		return
	}
	providerApp := input.Labels["application"]
	app, status, err := s.routeTestApplication(r.Context(), env, providerApp)
	if err != nil {
		httpapi.Error(w, status, routeTestErrorCode(status), "Bound application is unavailable")
		return
	}
	labels := make(map[string]string, len(input.Labels)+1)
	for key, value := range input.Labels {
		labels[key] = value
	}
	labels[envLabel] = env
	policySource, ok := s.Source.(routePolicySource)
	client, isClient := s.Source.(*Client)
	if !ok || policySource == nil || isClient && client == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_source_unavailable", "Routing policy source is unavailable")
		return
	}
	policy, err := policySource.ObserveGroupPolicy(r.Context(), env, labels)
	if err != nil {
		code := "route_source_unavailable"
		if errors.Is(err, ErrGroupPolicyAmbiguous) {
			code = "route_policy_ambiguous"
		}
		httpapi.Error(w, http.StatusServiceUnavailable, code, "Routing policy cannot be determined for this label set")
		return
	}
	if len(policy.Receivers) == 0 || policy.ObservedAt.IsZero() || policy.GroupWait == "" || policy.GroupInterval == "" || policy.RepeatInterval == "" {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_source_unavailable", "Routing policy source returned incomplete data")
		return
	}
	if s.EnvironmentLabels[env] != envLabel || s.ApplicationBindings[env][providerApp] != app.ID {
		httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_changed", "Routing scope changed during policy read")
		return
	}
	// A single locked snapshot proves the session, current owner and both
	// environment-wide grants still exist after the upstream configuration read.
	if err := routeTestFinalAuthority(r, db, txAccess, env, app); err != nil {
		switch {
		case errors.Is(err, store.ErrSessionRevoked):
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		case errors.Is(err, store.ErrForbidden):
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Routing access changed during policy read")
		default:
			httpapi.Error(w, http.StatusServiceUnavailable, "route_scope_changed", "Routing scope changed during policy read")
		}
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, routeTestResult{
		EnvironmentID: env, ApplicationID: app.ID,
		ReceiverNames: policy.Receivers, GroupBy: policy.GroupBy,
		GroupWait: policy.GroupWait, GroupInterval: policy.GroupInterval,
		RepeatInterval: policy.RepeatInterval, ObservedAt: policy.ObservedAt.UTC().Format(time.RFC3339Nano),
		Partial: true, Unavailable: []string{"path", "hits", "channel_targets", "current_interval_activity"},
	})
}

func (s *Service) routeTestManager(ctx context.Context, user core.User, env string) (int, error) {
	for _, action := range []string{"view", "manage"} {
		err := s.Access.Authorize(ctx, user, rbac.V3Decision{Module: "alert_rules", Action: action, EnvironmentID: env})
		if errors.Is(err, store.ErrForbidden) {
			return http.StatusForbidden, err
		}
		if err != nil {
			return http.StatusServiceUnavailable, err
		}
	}
	return http.StatusOK, nil
}

func (s *Service) routeTestApplication(ctx context.Context, env, providerApp string) (Application, int, error) {
	appID := s.ApplicationBindings[env][providerApp]
	if appID == "" {
		return Application{}, http.StatusNotFound, store.ErrNotFound
	}
	apps, err := s.Catalog.Applications(ctx, []string{env})
	if err != nil {
		return Application{}, http.StatusServiceUnavailable, err
	}
	var selected Application
	for _, app := range apps {
		if app.ID != appID || app.EnvironmentID != env {
			continue
		}
		if selected.ID != "" {
			return Application{}, http.StatusServiceUnavailable, errors.New("duplicate application mapping")
		}
		selected = app
	}
	if selected.ID == "" {
		return Application{}, http.StatusNotFound, store.ErrNotFound
	}
	return selected, http.StatusOK, nil
}

func routeTestAuthority(catalog Catalog, access Access) (*pgxpool.Pool, routeTxAccess) {
	var db *pgxpool.Pool
	switch c := catalog.(type) {
	case DatabaseCatalog:
		db = c.DB
	case *DatabaseCatalog:
		if c != nil {
			db = c.DB
		}
	}
	txAccess, _ := access.(routeTxAccess)
	return db, txAccess
}

func routeTestFinalAuthority(r *http.Request, db *pgxpool.Pool, access routeTxAccess, env string, expected Application) error {
	ctx := r.Context()
	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current Application
	err = tx.QueryRow(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,'')
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND ve.environment_id=$2 AND NOT a.archived
 FOR SHARE OF a,ve,e`, expected.ID, env).Scan(&current.ID, &current.EnvironmentID, &current.OwnerTeamID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && current != expected {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
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
	for _, action := range []string{"view", "manage"} {
		if err := access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "alert_rules", Action: action, EnvironmentID: env}); err != nil {
			return err
		}
	}
	// The role-table lock may have waited beyond the session expiry instant
	// checked by LockActiveSession. The session row remains locked in this tx.
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

func routeTestErrorCode(status int) string {
	switch status {
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "application_not_found"
	default:
		return "route_scope_unavailable"
	}
}
