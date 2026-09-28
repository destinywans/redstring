// Package v3postmortems stores human-reviewed incident retrospectives. It
// never writes to an external knowledge or collaboration system.
package v3postmortems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	Authorize(context.Context, core.User, rbac.V3Decision) error
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/postmortems/candidates", "viewer", s.Candidates)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/candidates", "knowledge", "view")
	api.Register("GET /api/v1/postmortems/records", "viewer", s.Records)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/records", "knowledge", "view")
	api.Register("POST /api/v1/postmortems", "viewer", s.Create)
	api.MarkV3ObjectRoute("POST /api/v1/postmortems", "knowledge", "manage")
}

type Section struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type Action struct {
	Text  string `json:"text"`
	Owner string `json:"owner"`
	Due   string `json:"due"`
}

type CreateInput struct {
	IncidentID string    `json:"incident_id"`
	Sections   []Section `json:"sections"`
	Actions    []Action  `json:"actions"`
	Status     string    `json:"status"`
}

func bounded(v string, max int) bool {
	return v != "" && strings.TrimSpace(v) == v && len([]rune(v)) <= max
}

func validate(in CreateInput) bool {
	if !bounded(in.IncidentID, 128) || (in.Status != "draft" && in.Status != "review") ||
		len(in.Sections) == 0 || len(in.Sections) > 40 || len(in.Actions) > 100 {
		return false
	}
	keys := map[string]bool{}
	for _, section := range in.Sections {
		if !bounded(section.Key, 80) || !bounded(section.Title, 200) ||
			len([]rune(section.Text)) > 16000 || keys[section.Key] ||
			in.Status == "review" && strings.TrimSpace(section.Text) == "" {
			return false
		}
		keys[section.Key] = true
	}
	for _, action := range in.Actions {
		if !bounded(action.Text, 2000) || !bounded(action.Owner, 200) || !bounded(action.Due, 100) {
			return false
		}
	}
	return true
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request) (httpapi.V3EnvironmentScope, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || s == nil || s.DB == nil || s.Access == nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Postmortem storage or authorization is unavailable")
		return scope, false
	}
	return scope, true
}

func inScope(scope httpapi.V3EnvironmentScope, environment string) bool {
	for _, id := range scope.EnvironmentIDs {
		if id == environment {
			return true
		}
	}
	return false
}

func (s *Service) authorize(ctx context.Context, user core.User, app, team, env, action string) error {
	if err := s.authorizeModule(ctx, user, app, team, env, "events", "view"); err != nil {
		return err
	}
	return s.authorizeModule(ctx, user, app, team, env, "knowledge", action)
}

func (s *Service) authorizeModule(ctx context.Context, user core.User, app, team, env, module, action string) error {
	decision := rbac.V3Decision{Module: module, Action: action, ApplicationID: app, EnvironmentID: env}
	err := s.Access.Authorize(ctx, user, decision)
	if !errors.Is(err, store.ErrForbidden) || team == "" {
		return err
	}
	decision.ApplicationID = ""
	decision.TeamID = team
	return s.Access.Authorize(ctx, user, decision)
}

func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, user core.User, app, team, env, action string) error {
	if err := s.authorizeModuleTx(ctx, tx, user, app, team, env, "events", "view"); err != nil {
		return err
	}
	return s.authorizeModuleTx(ctx, tx, user, app, team, env, "knowledge", action)
}

func (s *Service) authorizeModuleTx(ctx context.Context, tx pgx.Tx, user core.User, app, team, env, module, action string) error {
	decision := rbac.V3Decision{Module: module, Action: action, ApplicationID: app, EnvironmentID: env}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, decision)
	if !errors.Is(err, store.ErrForbidden) || team == "" {
		return err
	}
	decision.ApplicationID = ""
	decision.TeamID = team
	return s.Access.AuthorizeWithTx(ctx, tx, user, decision)
}

type Candidate struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
	Resolved string `json:"resolved"`
	Duration string `json:"duration"`
	Evidence int    `json:"evidence"`
}

func severity(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "p1", "critical", "fatal":
		return "P1", true
	case "p2", "warning", "warn", "high":
		return "P2", true
	case "p3", "info", "medium":
		return "P3", true
	case "p4", "low":
		return "P4", true
	}
	return "", false
}

func duration(start, end time.Time) string {
	d := end.Sub(start)
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

const candidatesSQL = `SELECT i.id,i.application_id,COALESCE(a.owner_team_id,''),e.key,i.title,i.severity,i.starts_at,closed.created_at,
 (SELECT count(*) FROM evidence ev JOIN branches b ON b.id=ev.branch_id WHERE b.investigation_id=i.investigation_id)::int
 FROM ops_incidents i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 JOIN investigations inv ON inv.id=i.investigation_id AND inv.application_id=i.application_id AND inv.status='closed'
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN LATERAL (SELECT MIN(ev.created_at) AS created_at FROM events ev
   WHERE ev.investigation_id=inv.id AND ev.type='investigation.closed' AND ev.created_at>=i.starts_at) closed
   ON closed.created_at IS NOT NULL
 WHERE i.status='resolved' AND e.key=ANY($1::text[])
   AND NOT EXISTS(SELECT 1 FROM v3_postmortems pm WHERE pm.incident_id=i.id)
   AND ($2::timestamptz IS NULL OR (closed.created_at,i.id)<($2::timestamptz,$3::text))
 ORDER BY closed.created_at DESC,i.id DESC LIMIT 256`

func (s *Service) Candidates(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	type row struct {
		candidate      Candidate
		app, team, env string
		start, closed  time.Time
	}
	items := make([]Candidate, 0)
	user := httpapi.CurrentUser(r)
	var cursor *time.Time
	cursorID := ""
	for {
		rows, err := s.DB.Query(r.Context(), candidatesSQL, scope.EnvironmentIDs, cursor, cursorID)
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Incident candidates unavailable")
			return
		}
		// Scan and close before authorization: V3Policy may use the same pool.
		batch := make([]row, 0, 256)
		for rows.Next() {
			var v row
			if err = rows.Scan(&v.candidate.ID, &v.app, &v.team, &v.env, &v.candidate.Title, &v.candidate.Severity, &v.start, &v.closed, &v.candidate.Evidence); err != nil {
				break
			}
			batch = append(batch, v)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			httpapi.Error(w, 503, "postmortems_unavailable", "Incident candidates unavailable")
			return
		}
		for _, v := range batch {
			if err = s.authorize(r.Context(), user, v.app, v.team, v.env, "view"); err != nil {
				if errors.Is(err, store.ErrForbidden) {
					continue
				}
				httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
				return
			}
			parsed, valid := severity(v.candidate.Severity)
			if !valid {
				httpapi.Error(w, 503, "source_unavailable", "Incident severity cannot be represented by the V3 contract")
				return
			}
			v.candidate.Severity = parsed
			v.candidate.Resolved = v.closed.UTC().Format(time.RFC3339)
			v.candidate.Duration = duration(v.start, v.closed)
			items = append(items, v.candidate)
			if len(items) > 1000 {
				httpapi.Error(w, 413, "scope_too_large", "Candidate collection exceeds the unpaged V3 response limit")
				return
			}
		}
		if len(batch) < 256 {
			break
		}
		last := batch[len(batch)-1]
		cursor = &last.closed
		cursorID = last.candidate.ID
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items})
}

func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	var in CreateInput
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !validate(in) {
		httpapi.Error(w, 422, "invalid_postmortem", "Incident, sections, actions or status is invalid")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not start postmortem write")
		return
	}
	defer tx.Rollback(r.Context())
	var app, team, env, investigation, incidentStatus string
	var started time.Time
	err = tx.QueryRow(r.Context(), `SELECT i.application_id,COALESCE(a.owner_team_id,''),e.key,i.investigation_id,i.status,i.starts_at
 FROM ops_incidents i
 JOIN applications a ON a.id=i.application_id AND NOT a.archived
 JOIN application_environments e ON e.id=i.environment_id AND e.application_id=i.application_id AND NOT e.archived
 JOIN v3_application_environments ve ON ve.application_id=i.application_id AND ve.environment_id=e.key
 WHERE i.id=$1 FOR UPDATE OF i`, in.IncidentID).Scan(&app, &team, &env, &investigation, &incidentStatus, &started)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, env) {
		httpapi.Error(w, 404, "not_found", "Incident not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Incident unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = s.authorizeTx(r.Context(), tx, user, app, team, env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Postmortem permission required")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	if incidentStatus != "resolved" || investigation == "" {
		httpapi.Error(w, 409, "incident_not_resolved", "Incident has no verified resolution")
		return
	}
	var closedSeq int64
	err = tx.QueryRow(r.Context(), `SELECT ev.seq FROM investigations inv
 JOIN investigation_reports report ON report.investigation_id=inv.id
 JOIN events ev ON ev.investigation_id=inv.id AND ev.type='investigation.closed' AND ev.created_at>=$3
 WHERE inv.id=$1 AND inv.application_id=$2 AND inv.status='closed'
 ORDER BY ev.created_at,ev.seq LIMIT 1`, investigation, app, started).Scan(&closedSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "incident_not_resolved", "Incident has no verified closing event")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Closing evidence unavailable")
		return
	}
	sections, _ := json.Marshal(in.Sections)
	actions, _ := json.Marshal(in.Actions)
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_postmortems
 (id,incident_id,investigation_id,application_id,environment_id,close_event_seq,sections,actions,status,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,$10)`,
		id, in.IncidentID, investigation, app, env, closedSeq, sections, actions, in.Status, user.ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httpapi.Error(w, 409, "postmortem_exists", "Incident already has a postmortem")
		} else {
			httpapi.Error(w, 503, "postmortems_unavailable", "Could not save postmortem")
		}
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'postmortem.created',$3,'succeeded',jsonb_build_object('incident_id',$4::text,'environment_id',$5::text,'status',$6::text,'close_event_seq',$7::bigint))`,
		user.ID, app, id, in.IncidentID, env, in.Status, closedSeq)
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not audit postmortem")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not commit postmortem")
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id})
}
