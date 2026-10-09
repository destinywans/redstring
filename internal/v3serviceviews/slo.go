package v3serviceviews

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

// sloSettings is null target/window with revision 0 until the first save;
// the configured target is never guessed from defaults.
type sloSettings struct {
	Target      *string           `json:"target"`
	Window      *string           `json:"window"`
	Revision    int64             `json:"revision"`
	Unavailable map[string]string `json:"unavailable"`
}
type sloPut struct {
	ExpectedRevision *int64 `json:"expected_revision"`
	Target           string `json:"target"`
	Window           string `json:"window"`
}

func validSLO(in sloPut) bool {
	return in.ExpectedRevision != nil && *in.ExpectedRevision >= 0 && (in.Target == "99.5" || in.Target == "99.9" || in.Target == "99.95" || in.Target == "99.99") && (in.Window == "7d" || in.Window == "30d")
}
func (s *Service) registerSLO(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"GET /api/v1/services/{id}/slo", "view", s.slo}, {"PUT /api/v1/services/{id}/slo", "manage", s.slo},
	} {
		handler := route.handler
		if route.action != "view" && s != nil && s.Store != nil {
			v3interactionaudit.Mark(api, s.Store.DB, route.pattern, "v3.service.slo.update")
		}
		api.Register(route.pattern, "viewer", handler)
		api.MarkV3ObjectRoute(route.pattern, "services", route.action)
	}
}
func (s *Service) slo(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	app, env, ok := decodeID(r.PathValue("id"))
	if !ok {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	selected := false
	for _, e := range scope.EnvironmentIDs {
		if e == env {
			selected = true
		}
	}
	if !selected {
		httpapi.Error(w, 404, "not_found", "Service not found")
		return
	}
	write := r.Method == http.MethodPut
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment")
		return
	}
	var in sloPut
	if write {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if !httpapi.Decode(w, r, &in) {
			return
		}
		if !validSLO(in) {
			httpapi.Error(w, 422, "invalid_slo", "Use a supported target, window and expected revision")
			return
		}
	}
	access, ok := s.watchAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Transactional policy unavailable")
		return
	}
	token, err := watchCookie(r)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		writeWatchError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	user := httpapi.CurrentUser(r)
	if err = lockWatchAuthority(r.Context(), tx, token, user); err == nil {
		err = watchEnvironmentCurrent(r.Context(), tx, user.ID, []string{env})
	}
	var team string
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,'') FROM applications a JOIN application_environments e ON e.application_id=a.id AND e.key=$2 AND NOT e.archived JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=e.key WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,e,ve`, app, env).Scan(&team)
	}
	action := "view"
	if write {
		action = "manage"
	}
	if err == nil {
		d := rbac.V3Decision{Module: "services", Action: action, EnvironmentID: env, ApplicationID: app}
		err = access.AuthorizeWithTx(r.Context(), tx, user, d)
		if errors.Is(err, store.ErrForbidden) && team != "" {
			d.ApplicationID = ""
			d.TeamID = team
			err = access.AuthorizeWithTx(r.Context(), tx, user, d)
		}
	}
	if err != nil {
		writeWatchError(w, err)
		return
	}
	out := sloSettings{Unavailable: map[string]string{}}
	if write {
		// The application/environment key serializes first creation as well as CAS.
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,16001))`, app+":"+env); err != nil {
			writeWatchError(w, err)
			return
		}
		var current int64
		err = tx.QueryRow(r.Context(), `SELECT revision FROM v3_service_slo_settings WHERE application_id=$1 AND environment_id=$2 FOR UPDATE`, app, env).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		if err != nil {
			writeWatchError(w, err)
			return
		}
		if current != *in.ExpectedRevision {
			httpapi.Error(w, 409, "slo_revision_changed", "Reload SLO settings before editing")
			return
		}
		out.Target, out.Window, out.Revision = &in.Target, &in.Window, current+1
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_service_slo_settings(application_id,environment_id,target,slo_window,revision,updated_by) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(application_id,environment_id) DO UPDATE SET target=EXCLUDED.target,slo_window=EXCLUDED.slo_window,revision=EXCLUDED.revision,updated_by=EXCLUDED.updated_by,updated_at=now()`, app, env, in.Target, in.Window, out.Revision, user.ID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO v3_service_environment_profiles(application_id,environment_id,slo_target_percent) VALUES($1,$2,$3::numeric) ON CONFLICT(application_id,environment_id) DO UPDATE SET slo_target_percent=EXCLUDED.slo_target_percent,updated_at=now()`, app, env, in.Target)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes) VALUES($1,$2,'v3.service.slo.updated',$2,'success',jsonb_build_object('environment_id',$3::text,'revision',$4::bigint,'target',$5::text,'window',$6::text))`, user.ID, app, env, out.Revision, in.Target, in.Window)
		}
	} else {
		err = tx.QueryRow(r.Context(), `SELECT target,slo_window,revision FROM v3_service_slo_settings WHERE application_id=$1 AND environment_id=$2 FOR SHARE`, app, env).Scan(&out.Target, &out.Window, &out.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			// Never saved: initialize with expected_revision 0.
			out.Target, out.Window, out.Revision, err = nil, nil, 0, nil
			out.Unavailable["target"], out.Unavailable["window"] = reasonNotConfigured, reasonNotConfigured
		}
	}
	if err == nil {
		err = watchSessionCurrent(r.Context(), tx, token, user.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeWatchError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, out)
}
