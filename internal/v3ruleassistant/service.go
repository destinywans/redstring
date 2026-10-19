// Package v3ruleassistant prepares bounded, source-backed alert rule drafts.
// It does not create, enable, or deploy a rule.
package v3ruleassistant

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB      *pgxpool.Pool
	Access  Access
	Model   GenerateModel
	Preview PreviewReader
	Now     func() time.Time
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("POST /api/v1/alert-rules/generate", "viewer", s.Generate)
	api.MarkV3ObjectRoute("POST /api/v1/alert-rules/generate", "alert_rules", "change")
	api.Register("POST /api/v1/alert-rules/preview", "viewer", s.PreviewRule)
	api.MarkV3ObjectRoute("POST /api/v1/alert-rules/preview", "alert_rules", "view")
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func singleEnvironment(w http.ResponseWriter, r *http.Request) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment")
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

func boundedText(value string, n int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= n && !strings.ContainsRune(value, 0)
}

func (s *Service) bindings(environmentID string) []PreviewBinding {
	if s == nil || s.Preview == nil {
		return nil
	}
	return s.Preview.Bindings(environmentID)
}

// selectBinding never infers application ownership from provider labels or
// request text. The caller authorizes the explicit application first.
func (s *Service) selectBinding(w http.ResponseWriter, environmentID, applicationID, query string) (PreviewBinding, bool) {
	bindings := s.bindings(environmentID)
	if len(bindings) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_not_connected", "Reviewed rule query catalog is not configured")
		return PreviewBinding{}, false
	}
	var selected *PreviewBinding
	for _, b := range bindings {
		if b.EnvironmentID != environmentID || b.ApplicationID != applicationID || query != "" && b.Query != query {
			continue
		}
		if selected != nil && (query != "" || selected.ApplicationID != b.ApplicationID) {
			httpapi.Error(w, http.StatusConflict, "query_ambiguous", "Reviewed query selection is ambiguous")
			return PreviewBinding{}, false
		}
		copy := b
		selected = &copy
	}
	if selected == nil {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Application or reviewed query is unavailable")
		return PreviewBinding{}, false
	}
	return *selected, true
}

func (s *Service) choices(environmentID, applicationID string) []PreviewBinding {
	items := []PreviewBinding{}
	for _, b := range s.bindings(environmentID) {
		if b.EnvironmentID == environmentID && b.ApplicationID == applicationID {
			items = append(items, b)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].QueryID < items[j].QueryID })
	return items
}

func (s *Service) draftBinding(w http.ResponseWriter, environmentID, applicationID, query string) (PreviewBinding, bool) {
	var selected *PreviewBinding
	for _, b := range s.choices(environmentID, applicationID) {
		if validateDraftPromQL(b, query) != nil {
			continue
		}
		if selected != nil {
			httpapi.Error(w, http.StatusConflict, "query_ambiguous", "Draft matches more than one reviewed query range")
			return PreviewBinding{}, false
		}
		copy := b
		selected = &copy
	}
	if selected == nil {
		httpapi.Error(w, http.StatusUnprocessableEntity, "unreviewed_query", "PromQL is invalid or outside the reviewed scope")
		return PreviewBinding{}, false
	}
	return *selected, true
}

func (s *Service) beginAuthorized(w http.ResponseWriter, r *http.Request, env, app, action string) (pgx.Tx, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Rule assistant storage or policy is unavailable")
		return nil, false
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Rule assistant storage is unavailable")
		return nil, false
	}
	if !s.authorizeTx(w, r, tx, env, app, action) {
		_ = tx.Rollback(r.Context())
		return nil, false
	}
	return tx, true
}

func (s *Service) authorizeTx(w http.ResponseWriter, r *http.Request, tx pgx.Tx, env, app, action string) bool {
	user := httpapi.CurrentUser(r)
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		return false
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Session verification is unavailable")
		}
		return false
	}
	var ownerTeam string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,'') FROM applications a
 JOIN v3_application_environments mapped ON mapped.application_id=a.id AND mapped.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=$2
 WHERE a.id=$1 AND NOT a.archived AND NOT e.archived FOR SHARE OF a,mapped,e`, app, env).Scan(&ownerTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Application not found")
		return false
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Application scope is unavailable")
		return false
	}
	// Serialize grant revocation with the final authorization check and commit.
	if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Grant verification is unavailable")
		return false
	}
	decision := rbac.V3Decision{Module: "alert_rules", Action: action, EnvironmentID: env, ApplicationID: app}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, user, decision)
	if errors.Is(err, store.ErrForbidden) && ownerTeam != "" {
		decision.ApplicationID, decision.TeamID = "", ownerTeam
		err = s.Access.AuthorizeWithTx(r.Context(), tx, user, decision)
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Alert rule permission required")
		return false
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Alert rule policy is unavailable")
		return false
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Session verification is unavailable")
		}
		return false
	}
	return true
}

func audit(ctx context.Context, tx pgx.Tx, app, actor, action, target string, changes any) error {
	_, err := tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,'recorded',$5)`, actor, app, action, target, changes)
	return err
}
