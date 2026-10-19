// Package v3postmortems stores human-reviewed incident retrospectives. It
// never writes to an external knowledge or collaboration system.
package v3postmortems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3events/alertseverity"
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
	api.Register("GET /api/v1/postmortems", "viewer", s.Summary)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems", "knowledge", "view")
	api.Register("GET /api/v1/postmortems/candidates", "viewer", s.Candidates)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/candidates", "knowledge", "view")
	api.Register("GET /api/v1/postmortems/draft", "viewer", s.Draft)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/draft", "knowledge", "view")
	api.Register("GET /api/v1/postmortems/records", "viewer", s.Records)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/records", "knowledge", "view")
	api.Register("POST /api/v1/postmortems", "viewer", s.Create)
	api.MarkV3ObjectRoute("POST /api/v1/postmortems", "knowledge", "manage")
	api.Register("POST /api/v1/postmortems/{id}/publish", "viewer", s.Publish)
	api.MarkV3ObjectRoute("POST /api/v1/postmortems/{id}/publish", "knowledge", "manage")
	api.Register("GET /api/v1/postmortems/{id}", "viewer", s.Get)
	api.MarkV3ObjectRoute("GET /api/v1/postmortems/{id}", "knowledge", "view")
	api.Register("PATCH /api/v1/postmortems/{id}", "viewer", s.Update)
	api.MarkV3ObjectRoute("PATCH /api/v1/postmortems/{id}", "knowledge", "manage")
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
	IncidentID          string    `json:"incident_id"`
	SourceCloseEventSeq int64     `json:"source_close_event_seq"`
	SourceRevision      string    `json:"source_revision"`
	Sections            []Section `json:"sections"`
	Actions             []Action  `json:"actions"`
	Status              string    `json:"status"`
}

func bounded(v string, max int) bool {
	return v != "" && strings.TrimSpace(v) == v && len([]rune(v)) <= max
}

var sourceRevisionPattern = regexp.MustCompile(`^(ops|case):[1-9][0-9]*$`)

func validate(in CreateInput) bool {
	if !bounded(in.IncidentID, 128) || in.SourceCloseEventSeq < 0 ||
		len(in.SourceRevision) > 80 ||
		(in.SourceRevision != "" && !sourceRevisionPattern.MatchString(in.SourceRevision)) ||
		(in.Status != "draft" && in.Status != "review") ||
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
	ID    string `json:"id"`
	Title string `json:"title"`
	// Severity is "" when unknown; SeverityReason then says why (PA-247).
	Severity       string   `json:"severity"`
	SeverityReason string   `json:"severity_reason,omitempty"`
	Resolved       string   `json:"resolved"`
	Duration       string   `json:"duration"`
	Evidence       int      `json:"evidence"`
	SourceRevision string   `json:"source_revision"`
	UnknownFields  []string `json:"unknown_fields,omitempty"`
}

func (v Candidate) MarshalJSON() ([]byte, error) {
	type candidateJSON struct {
		ID             string   `json:"id"`
		Title          string   `json:"title"`
		Severity       any      `json:"severity"`
		SeverityReason string   `json:"severity_reason,omitempty"`
		Resolved       string   `json:"resolved"`
		Duration       any      `json:"duration"`
		Evidence       any      `json:"evidence"`
		SourceRevision string   `json:"source_revision"`
		UnknownFields  []string `json:"unknown_fields,omitempty"`
	}
	out := candidateJSON{ID: v.ID, Title: v.Title, Severity: nullable(v.Severity), SeverityReason: v.SeverityReason, Resolved: v.Resolved,
		Duration: v.Duration, Evidence: v.Evidence, SourceRevision: v.SourceRevision, UnknownFields: v.UnknownFields}
	if slices.Contains(v.UnknownFields, "duration") {
		out.Duration = nil
	}
	if slices.Contains(v.UnknownFields, "evidence") {
		out.Evidence = nil
	}
	return json.Marshal(out)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// severity reads an incident's recorded severity through the shared alert
// severity table, like every events view (PA-247): the level, or "" and the
// reason it is unknown. labels is the source's severity_labels, nil when the
// row recorded no labels; a manual case's P1–P4 maps onto itself.
func severity(raw string, labels map[string]string) (level, reason string) {
	return alertseverity.Stored(raw, labels)
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

const candidatesSQL = postmortemSources + ` SELECT s.incident_id,s.source_kind,s.application_id,s.team_id,s.environment_id,
 s.title,s.severity,s.severity_labels,s.started_at,s.resolved_at,s.close_event_seq,s.status_event_id,COALESCE(s.evidence_count,0)
 FROM source s
 WHERE s.status='resolved' AND s.eligible AND s.resolved_at IS NOT NULL
   AND s.environment_id=ANY($1::text[])
   AND NOT EXISTS(SELECT 1 FROM v3_postmortems pm WHERE pm.incident_id=s.incident_id)
   AND ($2::timestamptz IS NULL OR (s.resolved_at,s.incident_id)<($2::timestamptz,$3::text))
 ORDER BY s.resolved_at DESC,s.incident_id DESC LIMIT 256`

func (s *Service) Candidates(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r)
	if !ok {
		return
	}
	type row struct {
		candidate               Candidate
		labels                  map[string]string
		app, team, env          string
		start                   *time.Time
		closed                  time.Time
		closeSeq, statusEventID *int64
		kind                    string
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
			if err = rows.Scan(&v.candidate.ID, &v.kind, &v.app, &v.team, &v.env, &v.candidate.Title, &v.candidate.Severity, &v.labels, &v.start, &v.closed, &v.closeSeq, &v.statusEventID, &v.candidate.Evidence); err != nil {
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
			// An unknown severity is listed as unknown; the page stays readable (PA-247).
			v.candidate.Severity, v.candidate.SeverityReason = severity(v.candidate.Severity, v.labels)
			v.candidate.Resolved = v.closed.UTC().Format(time.RFC3339)
			v.candidate.SourceRevision = sourceRevision(v.kind, v.closeSeq, v.statusEventID)
			if v.candidate.Severity == "" {
				v.candidate.UnknownFields = append(v.candidate.UnknownFields, "severity")
			}
			if v.start != nil {
				v.candidate.Duration = duration(*v.start, v.closed)
			} else {
				v.candidate.UnknownFields = append(v.candidate.UnknownFields, "duration", "evidence")
			}
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
	// Hold the current session and effective role sources through the write.
	if err = postmortemReadSession(r.Context(), tx, r); err == nil {
		err = lockPostmortemReadGrants(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err != nil {
		postmortemReadError(w, err)
		return
	}
	source, err := lockSource(r.Context(), tx, in.IncidentID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, source.env) {
		httpapi.Error(w, 404, "not_found", "Incident not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Incident unavailable")
		return
	}
	user := httpapi.CurrentUser(r)
	if err = postmortemReadSession(r.Context(), tx, r); err != nil {
		postmortemReadError(w, err)
		return
	}
	if err = s.authorizeTx(r.Context(), tx, user, source.app, source.team, source.env, "manage"); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, 403, "forbidden", "Postmortem permission required")
		} else {
			httpapi.Error(w, 503, "authorization_unavailable", "Postmortem access could not be checked")
		}
		return
	}
	if source.kind == "ops" && in.SourceCloseEventSeq <= 0 && in.SourceRevision == "" {
		httpapi.Error(w, 422, "source_close_event_seq_required", "Re-preview the incident and submit its source_close_event_seq")
		return
	}
	if source.kind == "case" && in.SourceRevision == "" {
		httpapi.Error(w, 422, "source_revision_required", "Re-preview the incident and submit its source_revision")
		return
	}
	if source.kind == "case" && in.SourceCloseEventSeq != 0 {
		httpapi.Error(w, 422, "invalid_postmortem", "A manual incident uses source_revision, not source_close_event_seq")
		return
	}
	if !source.eligible || source.kind == "case" && in.SourceRevision != source.revision() ||
		source.kind == "ops" && (in.SourceCloseEventSeq > 0 && (source.closeSeq == nil || in.SourceCloseEventSeq != *source.closeSeq) ||
			in.SourceRevision != "" && in.SourceRevision != source.revision()) {
		postmortemSourceChanged(w)
		return
	}
	sections, _ := json.Marshal(in.Sections)
	actions, _ := json.Marshal(in.Actions)
	id := store.ID()
	investigationID := ""
	if source.kind == "ops" {
		investigationID = source.investigation
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_postmortems
 (id,incident_id,investigation_id,application_id,environment_id,close_event_seq,
  source_kind,source_case_status_event_id,sections,actions,status,created_by)
 VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb,$11,$12)`,
		id, in.IncidentID, investigationID, source.app, source.env, source.closeSeq,
		source.kind, source.statusEventID, sections, actions, in.Status, user.ID)
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
 VALUES($1,'user',$2,'postmortem.created',$3,'succeeded',jsonb_build_object(
 'incident_id',$4::text,'environment_id',$5::text,'status',$6::text,
 'source_kind',$7::text,'source_revision',$8::text))`,
		user.ID, source.app, id, in.IncidentID, source.env, in.Status, source.kind, source.revision())
	if err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not audit postmortem")
		return
	}
	if err = postmortemReadSession(r.Context(), tx, r); err != nil {
		postmortemReadError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, 503, "postmortems_unavailable", "Could not commit postmortem")
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"id": id})
}

func postmortemSourceChanged(w http.ResponseWriter) {
	httpapi.Error(w, 409, "postmortem_source_changed", "Incident lifecycle source changed; re-preview before saving")
}
