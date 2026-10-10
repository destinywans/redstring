package v3events

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
	"github.com/wans/aiops-canvas/internal/v3pagequery"
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

func (s *Service) pageDecision(r *http.Request, tx pgx.Tx, user core.User, v incidentRecord, module, action string) error {
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
	err := tx.QueryRow(r.Context(), `SELECT COALESCE(a.owner_team_id,'') FROM applications a JOIN application_environments ae ON ae.application_id=a.id AND ae.id=$2 AND ae.key=$3 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=ae.key WHERE a.id=$1 AND NOT a.archived AND NOT ae.archived FOR SHARE OF a,ae,ve`, v.ApplicationID, v.EnvironmentID, v.EnvironmentKey).Scan(&team)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return err
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
	v, _, canonicalKind, err := s.pageObject(r, tx, in.Kind, in.ID, true)
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	// Legacy incident URLs can refer to an alert. Persist the actual object kind
	// so the assignment ledger and the operational owner stay one identity.
	in.Kind = canonicalKind
	module := "events"
	if in.Kind == "proposal" {
		module = "proposals"
	}
	for _, action := range []string{"view", "change"} {
		if err = s.pageDecision(r, tx, user, v, module, action); err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
	}
	var recipient core.User
	err = tx.QueryRow(r.Context(), `SELECT id,name,email,role FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND v3_identity_kind='human' FOR SHARE`, in.Assignee).Scan(&recipient.ID, &recipient.Name, &recipient.Email, &recipient.Role)
	if err != nil {
		respondIncidentCreateError(w, err)
		return
	}
	if err = s.pageDecision(r, tx, recipient, v, "environment", "view"); err != nil {
		httpapi.Error(w, 422, "assignee_unavailable", "Assignee must be able to view the environment and item")
		return
	}
	if err = s.pageDecision(r, tx, recipient, v, module, "view"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 422, "assignee_unavailable", "Assignee must be able to view the item")
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
	rows, err := tx.Query(r.Context(), `SELECT n.id,n.item_kind,n.item_id,n.created_at,n.actor_id,a.application_id,a.environment_id FROM v3_assignment_notifications n JOIN v3_item_assignments a ON a.item_kind=n.item_kind AND a.item_id=n.item_id WHERE n.recipient_id=$1 AND a.assignee_id=$1 AND n.created_at=a.updated_at AND n.created_at>=$2 ORDER BY n.created_at DESC,n.id LIMIT 501`, httpapi.CurrentUser(r).ID, since)
	if err != nil {
		return nil, err
	}
	type record struct {
		id, kind, item, actor, app, env string
		at                              time.Time
	}
	all := []record{}
	for rows.Next() {
		var v record
		if err = rows.Scan(&v.id, &v.kind, &v.item, &v.at, &v.actor, &v.app, &v.env); err != nil {
			break
		}
		all = append(all, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(all) > 500 {
		return nil, errTooManyIncidents
	}
	items := []persistedInboxItem{}
	for _, n := range all {
		v, title, canonicalKind, err := s.pageObject(r, tx, n.kind, n.item, false)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// Discovery was an unlocked snapshot. A writer can commit while this
		// request waits for the object lock, so re-read the current epoch under
		// assignment/notification SHARE locks after the object lock.
		if canonicalKind != n.kind {
			continue
		}
		var app, env, recipient string
		var epoch time.Time
		err = tx.QueryRow(r.Context(), `SELECT a.application_id,a.environment_id,a.assignee_id,a.updated_at
            FROM v3_item_assignments a JOIN v3_assignment_notifications current_notification
              ON current_notification.item_kind=a.item_kind AND current_notification.item_id=a.item_id
            WHERE a.item_kind=$1 AND a.item_id=$2 AND current_notification.id=$3
              AND current_notification.recipient_id=$4 AND current_notification.created_at=a.updated_at
            FOR SHARE OF a,current_notification`, n.kind, n.item, n.id, httpapi.CurrentUser(r).ID).Scan(&app, &env, &recipient, &epoch)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if recipient != httpapi.CurrentUser(r).ID {
			continue
		}
		if v.ApplicationID != app || v.EnvironmentKey != env {
			return nil, store.ErrInvalidReference
		}
		module := "events"
		if n.kind == "proposal" {
			module = "proposals"
		}
		if err = s.pageDecision(r, tx, httpapi.CurrentUser(r), v, module, "view"); errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		loc := s.Location
		if loc == nil {
			loc = time.UTC
		}
		at := n.at.In(loc)
		now := time.Now().In(loc)
		day := "today"
		if at.Year() != now.Year() || at.YearDay() != now.YearDay() {
			day = "yesterday"
		}
		items = append(items, persistedInboxItem{ID: "assignment:" + n.id, Kind: "assignment", Tone: "neutral", Title: title,
			Sub: t.assignedSub(n.kind), Time: at.Format("15:04"), At: at.UTC().Format(time.RFC3339), Day: day,
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
	rows, err := tx.Query(r.Context(), `SELECT p.id,p.schedule,p.matchers,p.reason,u.name,p.application_id,p.environment_id,ae.id FROM v3_maintenance_plans p JOIN users u ON u.id=p.owner_id JOIN application_environments ae ON ae.application_id=p.application_id AND ae.key=p.environment_id AND NOT ae.archived WHERE p.environment_id=ANY($1::text[]) ORDER BY p.created_at DESC,p.id LIMIT 501`, scope.EnvironmentIDs)
	if err != nil {
		respondErr(w, err)
		return
	}
	type candidate struct {
		plan maintenancePlan
		row  string
	}
	candidates := []candidate{}
	for rows.Next() {
		var v candidate
		var raw []byte
		if err = rows.Scan(&v.plan.ID, &v.plan.Schedule, &raw, &v.plan.Reason, &v.plan.Owner, &v.plan.ApplicationID, &v.plan.EnvironmentID, &v.row); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &v.plan.Matchers); err != nil {
			break
		}
		v.plan.Status = "planned"
		candidates = append(candidates, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(candidates) > 500 {
		respondErr(w, errTooManyIncidents)
		return
	}
	items := []maintenancePlan{}
	for _, v := range candidates {
		err = s.authorizeAlertTx(r, tx, incidentRecord{ApplicationID: v.plan.ApplicationID, EnvironmentID: v.row, EnvironmentKey: v.plan.EnvironmentID}, "view")
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
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
	rows, err := tx.Query(r.Context(), `SELECT n.created_at,n.incident_id,n.channel,n.status,n.application_id,n.environment_id,ae.key FROM ops_notifications n JOIN application_environments ae ON ae.id=n.environment_id AND ae.application_id=n.application_id WHERE ae.key=ANY($1::text[]) AND n.created_at>=$2 AND ($3='' OR strpos(lower(n.incident_id||' '||n.channel||' '||n.status),lower($3))>0) ORDER BY n.created_at DESC,n.id DESC LIMIT 501`, scope.EnvironmentIDs, time.Now().Add(-duration), q)
	if err != nil {
		respondErr(w, err)
		return
	}
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
	all := []candidate{}
	for rows.Next() {
		var c candidate
		var at time.Time
		if err = rows.Scan(&at, &c.item.Alert, &c.item.Receiver, &c.item.Result, &c.v.ApplicationID, &c.v.EnvironmentID, &c.v.EnvironmentKey); err != nil {
			break
		}
		c.item.Time = at.UTC().Format(time.RFC3339Nano)
		all = append(all, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(all) > 500 {
		respondErr(w, errTooManyIncidents)
		return
	}
	items := []item{}
	for _, c := range all {
		err = s.authorizeAlertTx(r, tx, c.v, "view")
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			respondAlertAuthorityError(w, err)
			return
		}
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
