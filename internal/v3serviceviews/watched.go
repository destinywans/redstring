package v3serviceviews

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// RegisterWatched is separate from Register so the composition root can wire
// the personal route batch independently of the already installed service GETs.
// Following a service changes only this user's preference, not service state.
func (s *Service) RegisterWatched(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /api/v1/me/watched-services", s.watched},
		{"PUT /api/v1/me/watched-services/{id}", s.follow},
		{"DELETE /api/v1/me/watched-services/{id}", s.unfollow},
	} {
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3ObjectRoute(route.pattern, "services", "view")
	}
}

type watchAvailability struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type watchedService struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       *string           `json:"status"`
	Availability watchAvailability `json:"availability"`
}

type watchEvidence struct {
	AlertSourceID string
	MetricSeal    metricSourceSeal
}

type watchQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const watchedRowsSQL = `SELECT a.id,a.key,a.name,a.description,w.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),
 COALESCE(u.name,''),COALESCE(ep.slo_target_percent::text,'')
 FROM v3_user_watched_services w
 JOIN applications a ON a.id=w.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=w.environment_id
 JOIN application_environments e ON e.application_id=a.id AND e.key=w.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=w.environment_id
 LEFT JOIN users u ON u.id=ep.oncall_user_id AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE w.user_id=$1 AND w.environment_id=ANY($2::text[])
 ORDER BY w.created_at,w.application_id,w.environment_id LIMIT 501`

var errWatchLimit = errors.New("personal watch limit exceeded")
var errWatchStorageLimit = errors.New("personal watch storage limit exceeded")
var errWatchChanged = errors.New("personal watch scope changed during read")

// The raw bound includes retained hidden watches. The handler separately
// limits currently visible watches to 50. The locked pass holds watched and
// service identity rows through response serialization.
func watchedRows(ctx context.Context, q watchQuerier, userID string, environments []string, lock bool) ([]serviceRow, error) {
	query := watchedRowsSQL
	if lock {
		query += " FOR SHARE OF w,a,ve,e"
	}
	rows, err := q.Query(ctx, query, userID, environments)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]serviceRow, 0, 32)
	for rows.Next() {
		var row serviceRow
		if err = rows.Scan(&row.AppID, &row.Key, &row.Name, &row.Description, &row.Env,
			&row.TeamID, &row.Team, &row.Tier, &row.Oncall, &row.Target); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 500 {
		return nil, errWatchStorageLimit
	}
	return out, nil
}

type watchTxAccess interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

func (s *Service) watchAccess() (watchTxAccess, bool) {
	access, ok := s.Access.(watchTxAccess)
	return access, ok && access != nil
}

func watchAuthorize(ctx context.Context, tx pgx.Tx, access watchTxAccess, user core.User, row serviceRow, module string) (bool, error) {
	d := rbac.V3Decision{Module: module, Action: "view", EnvironmentID: row.Env, ApplicationID: row.AppID}
	err := access.AuthorizeWithTx(ctx, tx, user, d)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, store.ErrForbidden) {
		return false, err
	}
	if row.TeamID == "" {
		return false, nil
	}
	d.ApplicationID, d.TeamID = "", row.TeamID
	err = access.AuthorizeWithTx(ctx, tx, user, d)
	if errors.Is(err, store.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

func watchCookie(r *http.Request) (string, error) {
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		return "", store.ErrSessionRevoked
	}
	return cookie.Value, nil
}

// All watched handlers use the same lock order: actor -> session -> this user's
// preference key -> role tables -> target rows. This prevents a logout, grant
// change or competing preference write from overtaking the final decision.
func lockWatchAuthority(ctx context.Context, tx pgx.Tx, token string, user core.User) error {
	if err := store.LockActiveSession(ctx, tx, token, user.ID); err != nil {
		return err
	}
	var human bool
	err := tx.QueryRow(ctx, `SELECT v3_identity_kind='human' AND source<>'settings_demo'
 FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL`, user.ID).Scan(&human)
	if err != nil {
		return err
	}
	if !human {
		return store.ErrForbidden
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,34817))`, user.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func watchSessionCurrent(ctx context.Context, tx pgx.Tx, token string, actor string) error {
	var current bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), actor).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !current) {
		return store.ErrSessionRevoked
	}
	return err
}

func watchEnvironmentCurrent(ctx context.Context, tx pgx.Tx, userID string, envs []string) error {
	for _, env := range envs {
		var current bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
 AND g.module='environment' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR
      (g.scope_kind='environment' AND g.scope_id=$2)))`, userID, env).Scan(&current)
		if err != nil {
			return err
		}
		if !current {
			return store.ErrForbidden
		}
	}
	return nil
}

func writeWatchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWatchLimit):
		httpapi.Error(w, 422, "watch_limit", "At most 50 currently visible services may be watched")
	case errors.Is(err, errWatchStorageLimit):
		httpapi.Error(w, 422, "watch_storage_limit", "Too many retained personal watches; remove old watches")
	case errors.Is(err, errWatchChanged):
		httpapi.Error(w, 409, "watch_changed", "Watched services changed; refresh and retry")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Current service access is required")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		httpapi.Error(w, 404, "not_found", "Service not found")
	default:
		httpapi.Error(w, 503, "watched_services_unavailable", "Watched services or policy are unavailable")
	}
}

func (s *Service) watched(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.scope(w, r)
	if !ok {
		return
	}
	access, ok := s.watchAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Service policy is unavailable")
		return
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_filter", "Watched services do not accept filters")
		return
	}
	token, err := watchCookie(r)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	initial, err := watchedRows(ctx, s.Store.DB, user.ID, scope.EnvironmentIDs, false)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	items := make([]watchedService, 0, len(initial))
	visible := make([]serviceRow, 0, len(initial))
	observed := make(map[string]watchEvidence, len(initial))
	complete := true
	now := nowOf(s)
	for _, row := range initial {
		allowed, e := s.canView(ctx, user, row, "services")
		if e != nil {
			writeWatchError(w, e)
			return
		}
		if !allowed {
			continue
		}
		if len(visible) == 50 {
			writeWatchError(w, errWatchLimit)
			return
		}
		visible = append(visible, row)
		item := watchedService{
			ID: encodeID(row.AppID, row.Env), Name: row.Name,
			Availability: watchAvailability{State: "unavailable", Reason: "Current scoped status evidence is unavailable"},
		}
		sourceID := ""
		if s.AlertCovered != nil {
			sourceID = s.AlertCovered(ctx, row.AppID, row.Env)
		}
		status, seal, e := statusOnly(ctx, s, scope, user, row, now)
		if e == nil && sourceID != "" && s.AlertCovered(ctx, row.AppID, row.Env) == sourceID {
			item.Status = &status
			item.Availability = watchAvailability{State: "available"}
			observed[item.ID] = watchEvidence{AlertSourceID: sourceID, MetricSeal: seal}
		} else if e != nil && !errors.Is(e, errIncomplete) {
			writeWatchError(w, e)
			return
		} else {
			complete = false
		}
		items = append(items, item)
	}
	if err = ctx.Err(); err != nil {
		writeWatchError(w, err)
		return
	}
	// The reviewed coverage callback itself queries the pool and a token
	// file. Run it before the final transaction so a one-connection pool
	// cannot self-deadlock while its transaction holds the connection.
	for _, row := range visible {
		if evidence, ok := observed[encodeID(row.AppID, row.Env)]; ok {
			if s.AlertCovered == nil || s.AlertCovered(ctx, row.AppID, row.Env) != evidence.AlertSourceID {
				writeWatchError(w, errIncomplete)
				return
			}
		}
	}
	// No external metric read happens while this transaction holds locks.
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	err = lockWatchAuthority(ctx, tx, token, user)
	if err == nil {
		err = watchEnvironmentCurrent(ctx, tx, user.ID, scope.EnvironmentIDs)
	}
	var current []serviceRow
	if err == nil {
		current, err = watchedRows(ctx, tx, user.ID, scope.EnvironmentIDs, true)
	}
	if err == nil && !reflect.DeepEqual(initial, current) {
		err = errWatchChanged
	}
	for _, row := range visible {
		if err != nil {
			break
		}
		var allowed bool
		allowed, err = watchAuthorize(ctx, tx, access, user, row, "services")
		if err == nil && !allowed {
			err = store.ErrForbidden
		}
		if err != nil {
			break
		}
		if evidence, hadStatus := observed[encodeID(row.AppID, row.Env)]; hadStatus {
			for _, module := range []string{"events", "connectors"} {
				allowed, err = watchAuthorize(ctx, tx, access, user, row, module)
				if err == nil && !allowed {
					err = store.ErrForbidden
				}
				if err != nil {
					break
				}
			}
			if err == nil {
				var seal metricSourceSeal
				seal, err = sourceSealFrom(ctx, tx, row, true)
				if err == nil && seal != evidence.MetricSeal {
					err = errIncomplete
				}
			}
		}
	}
	if err == nil {
		err = watchSessionCurrent(ctx, tx, token, user.ID)
	}
	if err != nil {
		writeWatchError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "complete_v3_dto": complete})
	// The deferred rollback releases the rows, session and role locks only
	// after response serialization.
}

func (s *Service) selectedWatch(w http.ResponseWriter, r *http.Request) (httpapi.V3EnvironmentScope, string, string, bool) {
	scope, ok := s.scope(w, r)
	if !ok {
		return scope, "", "", false
	}
	app, env, valid := decodeID(r.PathValue("id"))
	if !valid || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != env {
		httpapi.Error(w, 404, "not_found", "Service not found in the selected environment")
		return scope, "", "", false
	}
	if r.URL.RawQuery != "" {
		httpapi.Error(w, 422, "invalid_filter", "Watched service changes do not accept filters")
		return scope, "", "", false
	}
	return scope, app, env, true
}

func (s *Service) follow(w http.ResponseWriter, r *http.Request) {
	s.changeWatch(w, r, true)
}

func (s *Service) unfollow(w http.ResponseWriter, r *http.Request) {
	s.changeWatch(w, r, false)
}

func watchAudit(ctx context.Context, tx pgx.Tx, userID, appID, env, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,$3,$4,'success',jsonb_build_object('environment_id',$5::text))`,
		userID, appID, action, encodeID(appID, env), env)
	return err
}

func (s *Service) changeWatch(w http.ResponseWriter, r *http.Request, follow bool) {
	_, appID, env, ok := s.selectedWatch(w, r)
	if !ok {
		return
	}
	access, ok := s.watchAccess()
	if !ok {
		httpapi.Error(w, 503, "authorization_unavailable", "Service policy is unavailable")
		return
	}
	token, err := watchCookie(r)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	user := httpapi.CurrentUser(r)
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockWatchAuthority(ctx, tx, token, user); err != nil {
		writeWatchError(w, err)
		return
	}
	if !follow {
		// A former follower must be able to clear their own exact preference
		// after losing service access or after the service is archived. The
		// current environment grant and user ID still scope this mutation; no
		// service metadata is read or returned.
		err = watchEnvironmentCurrent(ctx, tx, user.ID, []string{env})
		var changed bool
		if err == nil {
			tag, execErr := tx.Exec(ctx, `DELETE FROM v3_user_watched_services
 WHERE user_id=$1 AND application_id=$2 AND environment_id=$3`, user.ID, appID, env)
			err = execErr
			changed = err == nil && tag.RowsAffected() == 1
		}
		if err == nil && changed {
			err = watchAudit(ctx, tx, user.ID, appID, env, "v3.watched_service.unfollowed")
		}
		if err == nil {
			err = watchSessionCurrent(ctx, tx, token, user.ID)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			writeWatchError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(204)
		return
	}
	var row serviceRow
	err = tx.QueryRow(ctx, `SELECT a.id,a.key,a.name,a.description,ve.environment_id,
 COALESCE(a.owner_team_id,''),COALESCE(t.name,''),COALESCE(ep.tier,''),
 COALESCE(u.name,''),COALESCE(ep.slo_target_percent::text,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 LEFT JOIN teams t ON t.id=a.owner_team_id
 LEFT JOIN v3_service_environment_profiles ep ON ep.application_id=a.id AND ep.environment_id=ve.environment_id
 LEFT JOIN users u ON u.id=ep.oncall_user_id AND NOT u.disabled AND u.deleted_at IS NULL
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`, appID, env).Scan(
		&row.AppID, &row.Key, &row.Name, &row.Description, &row.Env,
		&row.TeamID, &row.Team, &row.Tier, &row.Oncall, &row.Target)
	if err != nil {
		writeWatchError(w, err)
		return
	}
	allowed, err := watchAuthorize(ctx, tx, access, user, row, "services")
	if err == nil && !allowed {
		err = store.ErrNotFound
	}
	if err != nil {
		writeWatchError(w, err)
		return
	}
	changed := false
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_user_watched_services
 WHERE user_id=$1 AND application_id=$2 AND environment_id=$3)`, user.ID, appID, env).Scan(&exists)
	if err == nil && !exists {
		var stored, visible int
		err = tx.QueryRow(ctx, `SELECT count(*)::int FROM v3_user_watched_services WHERE user_id=$1`, user.ID).Scan(&stored)
		if err == nil && stored >= 500 {
			err = errWatchStorageLimit
		}
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT count(*)::int
 FROM v3_user_watched_services w
 JOIN applications a ON a.id=w.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=w.environment_id
 JOIN application_environments e ON e.application_id=a.id AND e.key=w.environment_id AND NOT e.archived
 WHERE w.user_id=$1
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='environment' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*') OR
        (g.scope_kind='environment' AND g.scope_id=w.environment_id)))
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
   AND g.module='services' AND g.action='view'
   AND ((g.scope_kind='global' AND g.scope_id='*') OR
        (g.scope_kind='environment' AND g.scope_id=w.environment_id) OR
        (g.scope_kind='application' AND g.scope_id=w.application_id) OR
        (a.owner_team_id IS NOT NULL AND g.scope_kind='team' AND g.scope_id=a.owner_team_id)))`,
				user.ID).Scan(&visible)
		}
		if err == nil && visible >= 50 {
			err = errWatchLimit
		}
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO v3_user_watched_services(user_id,application_id,environment_id)
 VALUES($1,$2,$3)`, user.ID, appID, env)
			changed = err == nil
		}
	}
	if err == nil && changed {
		err = watchAudit(ctx, tx, user.ID, appID, env, "v3.watched_service.followed")
	}
	if err == nil {
		err = watchSessionCurrent(ctx, tx, token, user.ID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeWatchError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"id": encodeID(appID, env), "watched": true})
}
