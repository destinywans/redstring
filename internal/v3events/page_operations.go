package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3interactionaudit"
	"github.com/destinywans/redstring/internal/v3pagequery"
	"github.com/jackc/pgx/v5"
)

func (s *Service) RegisterPageOperations(api *httpapi.Server) {
	for _, v := range []struct {
		pattern, module, action, audit string
		handler                        http.HandlerFunc
	}{
		{"GET /api/v1/notification-templates", "alert_rules", "view", "", s.notificationTemplatesUnavailable},
		{"GET /api/v1/oncall-schedules", "alert_rules", "view", "", s.oncallSchedulesUnavailable},
		{"GET /api/v1/notification-deliveries", "events", "view", "", s.notificationDeliveries},
		{"GET /api/v1/maintenance-windows", "events", "view", "", s.maintenancePlans},
		{"POST /api/v1/maintenance-windows", "events", "change", "maintenance_plan.create", s.createMaintenancePlan},
		{"POST /api/v1/assignments", "", "", "item.assign", s.assignItem},
	} {
		handler := v.handler
		if v.audit != "" {
			v3interactionaudit.Mark(api, s.DB, v.pattern, v.audit)
		}
		api.Register(v.pattern, "viewer", handler)
		if v.module == "alert_rules" {
			api.MarkV3Route(v.pattern, v.module, v.action)
		} else {
			api.MarkV3ObjectRoute(v.pattern, v.module, v.action)
		}
	}
}

func (s *Service) notificationTemplatesUnavailable(w http.ResponseWriter, r *http.Request) {
	httpapi.Error(w, 503, "notification_templates_unavailable", "An authoritative notification template catalog is not configured")
}
func (s *Service) oncallSchedulesUnavailable(w http.ResponseWriter, r *http.Request) {
	httpapi.Error(w, 503, "oncall_schedules_unavailable", "An authoritative schedule and escalation catalog is not configured")
}

func pageText(v string, max int) bool {
	if v == "" || strings.TrimSpace(v) != v || !utf8.ValidString(v) || utf8.RuneCountInString(v) > max || len(v) > max*4 {
		return false
	}
	for _, c := range v {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	return true
}

func (s *Service) pageTx(r *http.Request) (pgx.Tx, error) {
	if s.DB == nil {
		return nil, errors.New("storage unavailable")
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return nil, err
	}
	cookie, err := r.Cookie("aiops_session")
	if err == nil {
		err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		return nil, err
	}
	return tx, nil
}

func (s *Service) pageObject(r *http.Request, tx pgx.Tx, kind, id string, lock bool) (incidentRecord, string, string, error) {
	suffix := " FOR SHARE"
	if lock {
		suffix = " FOR UPDATE"
	}
	if kind == "alert" {
		v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1`+suffix+` OF i`, id))
		return v, v.Title, "alert", err
	}
	if kind == "incident" {
		c, err := scanCase(tx.QueryRow(r.Context(), caseSelect+` WHERE c.id=$1`+suffix+` OF c`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			v, err := scanIncident(tx.QueryRow(r.Context(), incidentSelect+` WHERE i.id=$1`+suffix+` OF i`, id))
			return v, v.Title, "alert", err
		}
		return incidentRecord{ID: c.ID, ApplicationID: c.ApplicationID, EnvironmentID: c.EnvironmentID, EnvironmentKey: c.EnvironmentKey, Title: c.Title}, c.Title, "incident", err
	}
	var v incidentRecord
	var content []byte
	err := tx.QueryRow(r.Context(), `SELECT p.id,p.application_id,ae.id,p.environment_id,p.content FROM v3_proposals p JOIN application_environments ae ON ae.application_id=p.application_id AND ae.key=p.environment_id AND NOT ae.archived WHERE p.id=$1`+suffix+` OF p`, id).Scan(&v.ID, &v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey, &content)
	var fields struct {
		Title string `json:"title"`
	}
	if err == nil {
		err = json.Unmarshal(content, &fields)
	}
	v.Title = fields.Title
	if v.Title == "" {
		v.Title = id
	}
	return v, v.Title, "proposal", err
}

// assignmentScope reads only the target identity before the caller may lock it.
func assignmentScope(r *http.Request, tx pgx.Tx, kind, id string) (incidentRecord, string, error) {
	var v incidentRecord
	query := `SELECT i.application_id,i.environment_id,e.key FROM ops_incidents i
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id
 WHERE i.id=$1`
	if kind == "incident" {
		err := tx.QueryRow(r.Context(), `SELECT application_id,environment_id,environment_key
 FROM v3_incident_cases WHERE id=$1`, id).Scan(&v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey)
		if !errors.Is(err, pgx.ErrNoRows) {
			return v, kind, err
		}
		kind = "alert"
	}
	if kind == "proposal" {
		query = `SELECT p.application_id,ae.id,p.environment_id FROM v3_proposals p
 JOIN application_environments ae ON ae.application_id=p.application_id AND ae.key=p.environment_id AND NOT ae.archived
 WHERE p.id=$1`
	}
	err := tx.QueryRow(r.Context(), query, id).Scan(&v.ApplicationID, &v.EnvironmentID, &v.EnvironmentKey)
	return v, kind, err
}

// Lock only the admitted identity; neither title nor proposal contents are
// needed for assignment. The final policy checks run after this lock.
func lockAssignmentTarget(r *http.Request, tx pgx.Tx, kind, id string, v incidentRecord) error {
	query := `SELECT i.id FROM ops_incidents i
 WHERE i.id=$1 AND i.application_id=$2 AND i.environment_id=$3 FOR UPDATE OF i`
	scope := v.EnvironmentID
	switch kind {
	case "incident":
		query = `SELECT c.id FROM v3_incident_cases c
 WHERE c.id=$1 AND c.application_id=$2 AND c.environment_id=$3 FOR UPDATE OF c`
	case "proposal":
		query = `SELECT p.id FROM v3_proposals p
 WHERE p.id=$1 AND p.application_id=$2 AND p.environment_id=$3 FOR UPDATE OF p`
		scope = v.EnvironmentKey
	}
	var lockedID string
	return tx.QueryRow(r.Context(), query, id, v.ApplicationID, scope).Scan(&lockedID)
}

func (s *Service) pageDecision(r *http.Request, tx pgx.Tx, user core.User, v incidentRecord, module, action string) error {
	return s.pageDecisionWithLocks(r, tx, user, v, module, action, true)
}

func (s *Service) pageDecisionWithLocks(r *http.Request, tx pgx.Tx, user core.User, v incidentRecord, module, action string, lock bool) error {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok {
		return store.ErrForbidden
	}
	selected := false
	for _, env := range scope.EnvironmentIDs {
		selected = selected || env == v.EnvironmentKey
	}
	if !selected {
		return store.ErrNotFound
	}
	if action != "view" && (scope.All || len(scope.EnvironmentIDs) != 1) {
		return store.ErrForbidden
	}
	var team string
	query := `SELECT COALESCE(a.owner_team_id,'') FROM applications a JOIN application_environments ae ON ae.application_id=a.id AND ae.id=$2 AND ae.key=$3 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=ae.key WHERE a.id=$1 AND NOT a.archived AND NOT ae.archived`
	if lock {
		query += " FOR SHARE OF a,ae,ve"
	}
	err := tx.QueryRow(r.Context(), query, v.ApplicationID, v.EnvironmentID, v.EnvironmentKey).Scan(&team)
	if err != nil {
		return err
	}
	if lock {
		if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
			return err
		}
	}
	policy := &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
	d := rbac.V3Decision{Module: module, Action: action, ApplicationID: v.ApplicationID, EnvironmentID: v.EnvironmentKey}
	err = policy.AuthorizeWithTx(r.Context(), tx, user, d)
	if errors.Is(err, store.ErrForbidden) && team != "" {
		d.ApplicationID = ""
		d.TeamID = team
		err = policy.AuthorizeWithTx(r.Context(), tx, user, d)
	}
	return err
}

func (s *Service) assignItem(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind     string `json:"item_kind"`
		ID       string `json:"item_id"`
		Assignee string `json:"assignee"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if in.Kind != "alert" && in.Kind != "incident" && in.Kind != "proposal" || !pageText(in.ID, 128) || !pageText(in.Assignee, 128) {
		httpapi.Error(w, 422, "invalid_assignment", "Supply item kind, stable item ID and assignee user ID")
		return
	}
	tx, err := s.pageTx(r)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	user := httpapi.CurrentUser(r)
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
		respondErr(w, err)
		return
	}
	if !human {
		httpapi.Error(w, 403, "forbidden", "Human assignment required")
		return
	}
	identity, canonicalKind, err := assignmentScope(r, tx, in.Kind, in.ID)
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	module := "events"
	if canonicalKind == "proposal" {
		module = "proposals"
	}
	if err = s.pageDecisionWithLocks(r, tx, user, identity, module, "view", false); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err = s.pageDecisionWithLocks(r, tx, user, identity, module, "change", false); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = lockAssignmentTarget(r, tx, canonicalKind, in.ID, identity); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	v := identity
	// Legacy incident URLs can refer to an alert. Persist the actual object kind
	// so the assignment ledger and the operational owner stay one identity.
	in.Kind = canonicalKind
	if err = s.pageDecision(r, tx, user, v, module, "view"); err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err = s.pageDecision(r, tx, user, v, module, "change"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	var recipient core.User
	err = tx.QueryRow(r.Context(), `SELECT id,name,email,role FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND v3_identity_kind='human' FOR SHARE`, in.Assignee).Scan(&recipient.ID, &recipient.Name, &recipient.Email, &recipient.Role)
	if err == nil {
		err = s.pageDecision(r, tx, recipient, v, "environment", "view")
	}
	if err == nil {
		err = s.pageDecision(r, tx, recipient, v, module, "view")
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 422, "assignee_unavailable", "Assignee must be able to view the environment and item")
		} else {
			respondErr(w, err)
		}
		return
	}
	var previous string
	var epoch time.Time
	err = tx.QueryRow(r.Context(), `SELECT assignee_id,updated_at FROM v3_item_assignments WHERE item_kind=$1 AND item_id=$2 FOR UPDATE`, in.Kind, in.ID).Scan(&previous, &epoch)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		respondErr(w, err)
		return
	}
	// Use a durable monotonic epoch, shared with its notification. Transaction
	// start now() can precede a writer this transaction waited behind; an
	// explicit clock value also distinguishes A -> B -> A without deleting history.
	if previous != recipient.ID {
		if err = tx.QueryRow(r.Context(), `SELECT GREATEST(clock_timestamp(),$1::timestamptz+interval '1 microsecond')`, epoch).Scan(&epoch); err != nil {
			respondErr(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO v3_item_assignments(item_kind,item_id,application_id,environment_id,assignee_id,actor_id,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(item_kind,item_id) DO UPDATE SET assignee_id=EXCLUDED.assignee_id,actor_id=EXCLUDED.actor_id,updated_at=EXCLUDED.updated_at`, in.Kind, in.ID, v.ApplicationID, v.EnvironmentKey, recipient.ID, user.ID, epoch); err != nil {
		respondErr(w, err)
		return
	}
	if in.Kind == "alert" {
		if _, err = tx.Exec(r.Context(), `UPDATE ops_incidents SET assignee_id=$2,updated_at=now() WHERE id=$1`, in.ID, recipient.ID); err != nil {
			respondErr(w, err)
			return
		}
	}
	if previous != recipient.ID {
		if _, err = tx.Exec(r.Context(), `INSERT INTO v3_assignment_notifications(id,item_kind,item_id,recipient_id,actor_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, store.ID(), in.Kind, in.ID, recipient.ID, user.ID, epoch); err != nil {
			respondErr(w, err)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',$2,'item.assign',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'item_kind',$5::text,'assignee_id',$6::text,'notification','inbox'))`, user.ID, v.ApplicationID, in.ID, v.EnvironmentKey, in.Kind, recipient.ID); err != nil {
		respondErr(w, err)
		return
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"ok": true, "notification": "inbox"})
}

// assignedHref opens the assigned object (PA-84): an alert or an incident on
// the incident page (GET /incidents/{id} serves both), a proposal on its
// review page.
func assignedHref(kind, id string) string {
	if kind == "proposal" {
		return "/review/" + url.PathEscape(id)
	}
	return "/incidents/" + url.PathEscape(id)
}

func (s *Service) readAssignmentNotifications(r *http.Request, tx pgx.Tx, since time.Time) ([]persistedInboxItem, error) {
	t := textFor(r.Header.Get("Accept-Language"))
	user := httpapi.CurrentUser(r)
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	type record struct {
		id, kind, item, actor, app, env string
		at                              time.Time
	}
	query := "WITH " + rbac.V3ListGrantsCTE("$1") + `
 SELECT n.id,n.item_kind,n.item_id,n.created_at,n.actor_id,a.application_id,a.environment_id
 FROM v3_assignment_notifications n
 JOIN v3_item_assignments a ON a.item_kind=n.item_kind AND a.item_id=n.item_id
 JOIN applications app ON app.id=a.application_id AND NOT app.archived
 JOIN application_environments ae ON ae.application_id=app.id AND ae.key=a.environment_id AND NOT ae.archived
 JOIN v3_application_environments ve ON ve.application_id=app.id AND ve.environment_id=ae.key
 WHERE n.recipient_id=$1 AND a.assignee_id=$1 AND n.created_at=a.updated_at AND n.created_at>=$2
 AND ae.key=ANY($3::text[]) AND (
  (n.item_kind='proposal' AND ` + rbac.V3ListScopePredicate("ae.key", "app.id", "app.owner_team_id", "proposals", "view") + `)
  OR (n.item_kind<>'proposal' AND ` + rbac.V3ListScopePredicate("ae.key", "app.id", "app.owner_team_id", "events", "view") + `)
 ) ORDER BY n.created_at DESC,n.id`
	type candidate struct {
		record
		title string
	}
	scan := func(row pgx.Row) (*candidate, error) {
		n := new(candidate)
		err := row.Scan(&n.id, &n.kind, &n.item, &n.at, &n.actor, &n.app, &n.env)
		return n, err
	}
	visible := func(ctx context.Context, n *candidate) (bool, error) {
		request := r.WithContext(ctx)
		v, title, canonicalKind, err := s.pageObject(request, tx, n.kind, n.item, false)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// Discovery was an unlocked snapshot. A writer can commit while this
		// request waits for the object lock, so re-read the current epoch under
		// assignment/notification SHARE locks after the object lock.
		if canonicalKind != n.kind {
			return false, nil
		}
		var app, env, recipient string
		var epoch time.Time
		err = tx.QueryRow(ctx, `SELECT a.application_id,a.environment_id,a.assignee_id,a.updated_at
            FROM v3_item_assignments a JOIN v3_assignment_notifications current_notification
              ON current_notification.item_kind=a.item_kind AND current_notification.item_id=a.item_id
            WHERE a.item_kind=$1 AND a.item_id=$2 AND current_notification.id=$3
              AND current_notification.recipient_id=$4 AND current_notification.created_at=a.updated_at
            FOR SHARE OF a,current_notification`, n.kind, n.item, n.id, user.ID).Scan(&app, &env, &recipient, &epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if recipient != user.ID {
			return false, nil
		}
		if v.ApplicationID != app || v.EnvironmentKey != env {
			return false, store.ErrInvalidReference
		}
		module := "events"
		if n.kind == "proposal" {
			module = "proposals"
		}
		if err = s.pageDecision(request, tx, user, v, module, "view"); errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		n.title = title
		return true, nil
	}
	all, err := scanVisibleList(r.Context(), tx, pageListLimit, scan, visible, query, user.ID, since, scope.EnvironmentIDs)
	if err != nil {
		return nil, err
	}
	items := make([]persistedInboxItem, 0, len(all))
	for _, n := range all {
		items = append(items, persistedInboxItem{ID: "assignment:" + n.id, Kind: "assignment", Tone: "neutral", Title: n.title,
			Sub: t.assignedSub(n.kind), At: n.at.UTC().Format(time.RFC3339),
			Href: assignedHref(n.kind, n.item), AssignmentKind: n.kind})
	}
	return items, nil
}

type maintenancePlan struct {
	ID            string   `json:"id"`
	Schedule      string   `json:"schedule"`
	Matchers      []string `json:"matchers"`
	Reason        string   `json:"reason"`
	Owner         string   `json:"owner"`
	ApplicationID string   `json:"application_id"`
	EnvironmentID string   `json:"environment_id"`
	Status        string   `json:"status"`
}

func maintenanceMatcherScope(matchers []string, env string) (string, bool) {
	if len(matchers) < 2 || len(matchers) > 16 {
		return "", false
	}
	seen := map[string]bool{}
	app := ""
	hasEnv := false
	for _, m := range matchers {
		parts := strings.SplitN(m, "=", 2)
		if len(parts) != 2 || !pageText(parts[0], 100) || !pageText(parts[1], 200) || seen[parts[0]] {
			return "", false
		}
		seen[parts[0]] = true
		if parts[0] == "application_id" {
			app = parts[1]
		}
		if parts[0] == "environment" {
			hasEnv = parts[1] == env
		}
	}
	return app, app != "" && hasEnv
}

func (s *Service) createMaintenancePlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Schedule string   `json:"schedule"`
		Matchers []string `json:"matchers"`
		Reason   string   `json:"reason"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, 409, "env_view_only", "Select one environment")
		return
	}
	app, valid := maintenanceMatcherScope(in.Matchers, scope.EnvironmentIDs[0])
	if !valid || !pageText(in.Schedule, 500) || !pageText(in.Reason, 2000) {
		httpapi.Error(w, 422, "invalid_maintenance_plan", "A local schedule description and exact application_id/environment matchers are required")
		return
	}
	tx, err := s.pageTx(r)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var row string
	err = tx.QueryRow(r.Context(), `SELECT id FROM application_environments WHERE application_id=$1 AND key=$2 AND NOT archived`, app, scope.EnvironmentIDs[0]).Scan(&row)
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, httpapi.CurrentUser(r).ID).Scan(&human); err != nil {
		respondErr(w, err)
		return
	}
	if !human {
		httpapi.Error(w, 403, "forbidden", "Human plan author required")
		return
	}
	v := incidentRecord{ApplicationID: app, EnvironmentID: row, EnvironmentKey: scope.EnvironmentIDs[0]}
	if err = s.authorizeAlertTx(r, tx, v, "change"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = s.authorizeAlertTx(r, tx, v, "view"); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	id := store.ID()
	data, _ := json.Marshal(in.Matchers)
	user := httpapi.CurrentUser(r)
	if _, err = tx.Exec(r.Context(), `INSERT INTO v3_maintenance_plans(id,application_id,environment_id,schedule,matchers,reason,owner_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, app, scope.EnvironmentIDs[0], in.Schedule, data, in.Reason, user.ID); err != nil {
		respondErr(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes) VALUES($1,'user',$2,'maintenance_plan.create',$3,'succeeded',jsonb_build_object('environment_id',$4::text,'execution_enabled',false))`, user.ID, app, id, scope.EnvironmentIDs[0]); err != nil {
		respondErr(w, err)
		return
	}
	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"id": id, "status": "planned", "execution_enabled": false})
}

func (s *Service) maintenancePlans(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pageTx(r)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	cte, predicate, args := s.eventListGrants(r, "ae.key", "a.id", "a.owner_team_id", scope.EnvironmentIDs)
	if cte != "" {
		cte = "WITH " + cte + " "
	}
	query := cte + `SELECT p.id,p.schedule,p.matchers,p.reason,u.name,p.application_id,p.environment_id,ae.id
 FROM v3_maintenance_plans p JOIN users u ON u.id=p.owner_id
 JOIN application_environments ae ON ae.application_id=p.application_id AND ae.key=p.environment_id AND NOT ae.archived
 JOIN applications a ON a.id=p.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=ae.key
 WHERE p.environment_id=ANY($1::text[]) AND ` + predicate + ` ORDER BY p.created_at DESC,p.id`
	type candidate struct {
		plan maintenancePlan
		row  string
		raw  []byte
	}
	scan := func(row pgx.Row) (*candidate, error) {
		v := new(candidate)
		err := row.Scan(&v.plan.ID, &v.plan.Schedule, &v.raw, &v.plan.Reason, &v.plan.Owner, &v.plan.ApplicationID, &v.plan.EnvironmentID, &v.row)
		return v, err
	}
	scopeVisible := s.visibleEventScope(r, tx, nil)
	visible := func(ctx context.Context, v *candidate) (bool, error) {
		return scopeVisible(ctx, incidentRecord{ApplicationID: v.plan.ApplicationID, EnvironmentID: v.row, EnvironmentKey: v.plan.EnvironmentID})
	}
	candidates, err := scanVisibleList(r.Context(), tx, pageListLimit, scan, visible, query, args...)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	items := make([]maintenancePlan, 0, len(candidates))
	for _, v := range candidates {
		if err = json.Unmarshal(v.raw, &v.plan.Matchers); err != nil {
			respondErr(w, err)
			return
		}
		v.plan.Status = "planned"
		items = append(items, v.plan)
	}

	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "execution_enabled": false})
}

func (s *Service) notificationDeliveries(w http.ResponseWriter, r *http.Request) {
	duration, err := v3pagequery.ParseRange(r.URL.Query(), 24*time.Hour)
	q := r.URL.Query().Get("q")
	if err != nil || len(r.URL.Query()["q"]) > 1 || len(q) > 256 {
		httpapi.Error(w, 422, "invalid_query", "Invalid time range or search")
		return
	}
	tx, err := s.pageTx(r)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	cte, predicate, args := s.eventListGrants(r, "ae.key", "a.id", "a.owner_team_id", scope.EnvironmentIDs, time.Now().Add(-duration), q)
	if cte != "" {
		cte = "WITH " + cte + " "
	}
	query := cte + `SELECT n.created_at,n.incident_id,n.channel,n.status,n.application_id,n.environment_id,ae.key
 FROM ops_notifications n
 JOIN application_environments ae ON ae.id=n.environment_id AND ae.application_id=n.application_id AND NOT ae.archived
 JOIN applications a ON a.id=n.application_id AND NOT a.archived
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=ae.key
 WHERE ae.key=ANY($1::text[]) AND ` + predicate + `
 AND n.created_at>=$2 AND ($3='' OR strpos(lower(n.incident_id||' '||n.channel||' '||n.status),lower($3))>0)
 ORDER BY n.created_at DESC,n.id DESC`
	type item struct {
		Time     string `json:"time"`
		Alert    string `json:"alert"`
		Receiver string `json:"receiver"`
		Result   string `json:"result"`
	}
	type candidate struct {
		item item
		v    incidentRecord
	}
	scan := func(row pgx.Row) (candidate, error) {
		var c candidate
		var at time.Time
		err := row.Scan(&at, &c.item.Alert, &c.item.Receiver, &c.item.Result, &c.v.ApplicationID, &c.v.EnvironmentID, &c.v.EnvironmentKey)
		c.item.Time = at.UTC().Format(time.RFC3339Nano)
		return c, err
	}
	scopeVisible := s.visibleEventScope(r, tx, nil)
	visible := func(ctx context.Context, c candidate) (bool, error) {
		return scopeVisible(ctx, c.v)
	}
	candidates, err := scanVisibleList(r.Context(), tx, pageListLimit, scan, visible, query, args...)
	if err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	items := make([]item, 0, len(candidates))
	for _, c := range candidates {
		items = append(items, c.item)
	}

	if err = activeAlertReadSession(r.Context(), tx, r); err != nil {
		respondAlertAuthorityError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		respondErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "source": "platform_delivery_ledger", "receiver_kind": "channel_id"})
}
