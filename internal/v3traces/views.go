package v3traces

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

type traceView struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Query string `json:"query"`
	Owner string `json:"owner"`
}
type traceViewInput struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	Query string `json:"query"`
}
type viewAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

func validTraceView(in traceViewInput) bool {
	return (in.Scope == "app" || in.Scope == "agent") && strings.TrimSpace(in.Name) == in.Name && utf8.RuneCountInString(in.Name) > 0 && utf8.RuneCountInString(in.Name) <= 160 && len(in.Query) <= 2048 && !strings.ContainsAny(in.Name+in.Query, "\x00")
}
func (s *Service) registerViews(api *httpapi.Server) {
	for _, rt := range []struct {
		p, a string
		h    http.HandlerFunc
	}{{"GET /api/v1/trace-views", "view", s.views}, {"POST /api/v1/trace-views", "manage", s.views}, {"DELETE /api/v1/trace-views/{id}", "manage", s.views}} {
		handler := rt.h
		if rt.a != "view" && s != nil && s.Store != nil {
			v3interactionaudit.Mark(api, s.Store.DB, rt.p, "v3.trace_view.write")
		}
		api.Register(rt.p, "viewer", handler)
		api.MarkV3ObjectRoute(rt.p, "traces", rt.a)
	}
}
func viewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Current team trace permission required")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, 404, "not_found", "Trace view not found")
	default:
		httpapi.Error(w, 503, "trace_views_unavailable", "Saved views or policy unavailable")
	}
}
func (s *Service) views(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	write := r.Method != http.MethodGet
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment")
		return
	}
	var in traceViewInput
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if !validTraceView(in) {
			httpapi.Error(w, 422, "invalid_trace_view", "Use app or agent scope, a bounded name and query")
			return
		}
	} else if r.Method == http.MethodGet {
		if len(r.URL.Query()["scope"]) != 1 {
			httpapi.Error(w, 422, "invalid_scope", "Select one trace scope")
			return
		}
		in.Scope = r.URL.Query().Get("scope")
		if in.Scope != "app" && in.Scope != "agent" {
			httpapi.Error(w, 422, "invalid_scope", "Use app or agent scope")
			return
		}
	}
	if s == nil || s.Store == nil || s.Store.DB == nil {
		viewError(w, errors.New("storage"))
		return
	}
	access, ok := s.Access.(viewAccess)
	if !ok {
		viewError(w, errors.New("policy"))
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		viewError(w, store.ErrSessionRevoked)
		return
	}
	user := httpapi.CurrentUser(r)
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		viewError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Retain the current session, membership and grant sources through serialization.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			viewError(w, err)
			return
		}
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		viewError(w, err)
		return
	}
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' AND source<>'settings_demo' FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, user.ID).Scan(&human); err != nil {
		viewError(w, err)
		return
	}
	if !human {
		viewError(w, store.ErrForbidden)
		return
	}
	if _, err = tx.Exec(r.Context(), `LOCK TABLE team_members,rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		viewError(w, err)
		return
	}
	action := "view"
	if write {
		action = "manage"
	}
	authorize := func(team, env string) error {
		return access.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{Module: "traces", Action: action, EnvironmentID: env, TeamID: team})
	}
	if r.Method == http.MethodPost {
		rows, e := tx.Query(r.Context(), `SELECT DISTINCT team_id FROM team_members WHERE user_id=$1 ORDER BY team_id LIMIT 2`, user.ID)
		if e != nil {
			viewError(w, e)
			return
		}
		teams := []string{}
		for rows.Next() {
			var team string
			if e = rows.Scan(&team); e != nil {
				break
			}
			teams = append(teams, team)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			viewError(w, e)
			return
		}
		if len(teams) != 1 {
			httpapi.Error(w, 422, "ambiguous_team", "Saved views require exactly one current team membership")
			return
		}
		env := scope.EnvironmentIDs[0]
		if err = authorize(teams[0], env); err != nil {
			viewError(w, err)
			return
		}
		id := store.ID()
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_trace_views(id,environment_id,team_id,scope,name,query,owner_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, env, teams[0], in.Scope, in.Name, in.Query, user.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.trace_view.created',$2,'success',jsonb_build_object('environment_id',$3::text,'team_id',$4::text,'scope',$5::text))`, user.ID, id, env, teams[0], in.Scope)
		}
		if err == nil {
			err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			viewError(w, err)
			return
		}
		httpapi.WriteJSON(w, 201, map[string]string{"id": id})
		return
	}
	if r.Method == http.MethodDelete {
		var team, env string
		err = tx.QueryRow(r.Context(), `SELECT v.team_id,v.environment_id FROM v3_trace_views v WHERE v.id=$1 AND v.environment_id=ANY($2::text[]) AND EXISTS(SELECT 1 FROM team_members m WHERE m.user_id=$3 AND m.team_id=v.team_id) FOR UPDATE`, r.PathValue("id"), scope.EnvironmentIDs, user.ID).Scan(&team, &env)
		if err == nil {
			err = authorize(team, env)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `DELETE FROM v3_trace_views WHERE id=$1`, r.PathValue("id"))
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.trace_view.deleted',$2,'success',jsonb_build_object('environment_id',$3::text,'team_id',$4::text))`, user.ID, r.PathValue("id"), env, team)
		}
		if err == nil {
			err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			viewError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT v.id,v.name,v.query,COALESCE(u.name,v.owner_id),v.team_id,v.environment_id FROM v3_trace_views v LEFT JOIN users u ON u.id=v.owner_id WHERE v.environment_id=ANY($1::text[]) AND v.scope=$2 AND EXISTS(SELECT 1 FROM team_members m WHERE m.user_id=$3 AND m.team_id=v.team_id) AND EXISTS(SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$3 AND g.module='traces' AND g.action='view' AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=v.environment_id) OR (g.scope_kind='team' AND g.scope_id=v.team_id))) ORDER BY v.created_at DESC,v.id LIMIT 501 FOR SHARE OF v`, scope.EnvironmentIDs, in.Scope, user.ID)
	if err != nil {
		viewError(w, err)
		return
	}
	type scopedView struct {
		v         traceView
		team, env string
	}
	candidates := []scopedView{}
	items := []traceView{}
	for rows.Next() {
		var v traceView
		var team, env string
		if err = rows.Scan(&v.ID, &v.Name, &v.Query, &v.Owner, &team, &env); err != nil {
			break
		}
		candidates = append(candidates, scopedView{v, team, env})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		viewError(w, err)
		return
	}
	for _, candidate := range candidates {
		if err = authorize(candidate.team, candidate.env); err != nil {
			viewError(w, err)
			return
		}
		items = append(items, candidate.v)
	}
	if len(items) > 500 {
		httpapi.Error(w, 413, "trace_view_limit", "Narrow saved view scope")
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		viewError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}
