// Package v3executions projects persisted recovery executions into the V3
// workspace and records local report publication. It never runs an action.
package v3executions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type Access interface {
	AuthorizeWithTx(context.Context, pgx.Tx, core.User, rbac.V3Decision) error
}

type Service struct {
	DB     *pgxpool.Pool
	Access Access
	// Rollout answers a V3 rollback execution ID (PA-08) and reports whether
	// it did; other IDs fall through to the legacy projection.
	Rollout func(http.ResponseWriter, *http.Request, string) bool
}

func New(db *pgxpool.Pool, access Access) *Service { return &Service{DB: db, Access: access} }

// Register owns GET /executions/{id} on a V3 instance, which registers none
// of the legacy action routes (PA-19 P4).
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/executions/{id}", "viewer", s.Detail)
	api.MarkV3ObjectRoute("GET /api/v1/executions/{id}", "proposals", "view")
	api.Register("POST /api/v1/executions/{id}/report/publish", "viewer", s.Publish)
	api.MarkV3ObjectRoute("POST /api/v1/executions/{id}/report/publish", "knowledge", "manage")
}

func (s *Service) ready(w http.ResponseWriter, r *http.Request, write bool) (httpapi.V3EnvironmentScope, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return httpapi.V3EnvironmentScope{}, false
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return httpapi.V3EnvironmentScope{}, false
	}
	if write && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, 409, "env_view_only", "Select one environment before publishing")
		return httpapi.V3EnvironmentScope{}, false
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

type executionRecord struct {
	ID, PlanID, InvestigationID, ApplicationID, OwnerTeam, EnvironmentID, EnvironmentKey string
	Status, Error, InvestigationStatus, InvestigationTitle, PlanDigest, ProposalDigest   string
	ObservationSeconds, CreatedBy                                                        string
	Attempt                                                                              int
	Result                                                                               []byte
	CreatedAt, UpdatedAt                                                                 time.Time
}

func loadExecution(ctx context.Context, tx pgx.Tx, id string) (executionRecord, error) {
	var x executionRecord
	query := `SELECT x.id,x.plan_id,p.investigation_id,i.application_id,
 COALESCE(app.owner_team_id,''),env.id,env.key,x.status,x.error,i.status,i.title,
 p.digest,COALESCE(p.proposal->>'digest',''),
 COALESCE(p.proposal->>'observation_seconds',''),x.created_by,x.attempt,x.result,
 x.created_at,x.updated_at
 FROM executions x
 JOIN action_plans p ON p.id=x.plan_id
 JOIN investigations i ON i.id=p.investigation_id
 JOIN applications app ON app.id=i.application_id AND NOT app.archived
 JOIN application_environments env ON env.id=i.environment_id
  AND env.application_id=app.id AND NOT env.archived
 JOIN v3_application_environments mapped ON mapped.application_id=app.id
  AND mapped.environment_id=env.key
 WHERE x.id=$1 AND p.proposal->>'environment_id'=env.id
 FOR SHARE OF x,p,i,app,env,mapped`
	err := tx.QueryRow(ctx, query, id).Scan(&x.ID, &x.PlanID, &x.InvestigationID,
		&x.ApplicationID, &x.OwnerTeam, &x.EnvironmentID, &x.EnvironmentKey,
		&x.Status, &x.Error, &x.InvestigationStatus, &x.InvestigationTitle,
		&x.PlanDigest, &x.ProposalDigest, &x.ObservationSeconds, &x.CreatedBy,
		&x.Attempt, &x.Result, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

type approvalRecord struct {
	Decision, Digest, ApprovedBy string
}

func loadApproval(ctx context.Context, tx pgx.Tx, planID string) (approvalRecord, error) {
	a := approvalRecord{Decision: "pending"}
	err := tx.QueryRow(ctx, `SELECT decision,digest,approved_by FROM approvals
 WHERE plan_id=$1 FOR SHARE`, planID).Scan(&a.Decision, &a.Digest, &a.ApprovedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	return a, err
}

type reportRecord struct {
	Raw            []byte
	RecoveryEquals bool
	CreatedAt      time.Time
	Investigation  string    `json:"investigation_id"`
	Application    string    `json:"application_id"`
	Execution      string    `json:"execution_id"`
	Plan           string    `json:"plan_id"`
	Summary        string    `json:"summary"`
	ClosedBy       string    `json:"closed_by"`
	ClosedAt       time.Time `json:"closed_at"`
	ApprovedBy     string    `json:"approved_by"`
	Proposal       struct {
		Digest string `json:"digest"`
	} `json:"proposal"`
	Recovery json.RawMessage `json:"recovery"`
}

func loadReport(ctx context.Context, tx pgx.Tx, x executionRecord) (*reportRecord, error) {
	var r reportRecord
	err := tx.QueryRow(ctx, `SELECT ir.report,ir.created_at,
 COALESCE(ir.report->'recovery'=x.result,false)
 FROM investigation_reports ir JOIN executions x ON x.id=$2
 WHERE ir.investigation_id=$1 AND ir.execution_id=$2 FOR SHARE OF ir`,
		x.InvestigationID, x.ID).Scan(&r.Raw, &r.CreatedAt, &r.RecoveryEquals)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(r.Raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func validReport(x executionRecord, a approvalRecord, r *reportRecord) bool {
	return r != nil && x.InvestigationStatus == "closed" && r.RecoveryEquals &&
		a.Decision == "approved" && a.Digest == x.PlanDigest &&
		x.ProposalDigest == x.PlanDigest &&
		r.Investigation == x.InvestigationID && r.Application == x.ApplicationID &&
		r.Execution == x.ID && r.Plan == x.PlanID && r.Proposal.Digest == x.PlanDigest &&
		r.ApprovedBy != "" && r.ApprovedBy == a.ApprovedBy &&
		r.ClosedBy != "" && !r.ClosedAt.IsZero()
}

type publicationRecord struct {
	Digest, PublishedBy string
	PublishedAt         time.Time
}

func loadPublication(ctx context.Context, tx pgx.Tx, executionID string) (*publicationRecord, error) {
	var p publicationRecord
	err := tx.QueryRow(ctx, `SELECT report_digest,published_by,published_at
 FROM v3_execution_report_publications WHERE execution_id=$1 FOR SHARE`, executionID).
		Scan(&p.Digest, &p.PublishedBy, &p.PublishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Service) authorizeTx(ctx context.Context, tx pgx.Tx, user core.User,
	app, team, env, module, action string) error {
	d := rbac.V3Decision{Module: module, Action: action, EnvironmentID: env, ApplicationID: app}
	err := s.Access.AuthorizeWithTx(ctx, tx, user, d)
	if !errors.Is(err, store.ErrForbidden) || team == "" {
		return err
	}
	d.ApplicationID = ""
	d.TeamID = team
	return s.Access.AuthorizeWithTx(ctx, tx, user, d)
}

func lockGrantSources(ctx context.Context, tx pgx.Tx, userID string) error {
	var activeID string
	err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled
 AND deleted_at IS NULL AND source<>'settings_demo' FOR SHARE`, userID).Scan(&activeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func lockSession(ctx context.Context, tx pgx.Tx, r *http.Request, human bool) (string, error) {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return "", store.ErrSessionRevoked
	}
	userID := httpapi.CurrentUser(r).ID
	if err = store.LockActiveSession(ctx, tx, cookie.Value, userID); err != nil {
		return "", err
	}
	if human {
		var activeID string
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1
 AND v3_identity_kind='human' AND source<>'settings_demo' FOR SHARE`, userID).Scan(&activeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", store.ErrForbidden
		}
		if err != nil {
			return "", err
		}
	}
	return cookie.Value, nil
}

func sessionStillActive(ctx context.Context, tx pgx.Tx, token, userID string) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
 WHERE token_hash=$1 AND user_id=$2`, store.Hash(token), userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
		return store.ErrSessionRevoked
	}
	return err
}

type observationRecord struct {
	Recovered  *bool      `json:"recovered"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Samples    *int       `json:"samples"`
	Requests   *uint64    `json:"requests"`
	Reason     *string    `json:"reason"`
}

func decodeObservation(raw []byte) (*observationRecord, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if _, ok := fields["recovered"]; !ok {
		return nil, nil
	}
	var o observationRecord
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func verifiedRecovery(x executionRecord, o *observationRecord) bool {
	seconds, err := strconv.Atoi(x.ObservationSeconds)
	if err != nil || seconds < 180 || o == nil || o.Recovered == nil || !*o.Recovered ||
		o.StartedAt == nil || o.FinishedAt == nil || o.Samples == nil || *o.Samples < 1 ||
		o.Requests == nil || *o.Requests < 1 {
		return false
	}
	return !o.StartedAt.IsZero() && !o.FinishedAt.IsZero() &&
		o.FinishedAt.Sub(*o.StartedAt) >= time.Duration(seconds)*time.Second
}

type step struct {
	Seq  int64  `json:"seq"`
	Type string `json:"type"`
	Time string `json:"time"`
}

func loadSteps(ctx context.Context, tx pgx.Tx, x executionRecord) ([]step, bool, error) {
	rows, err := tx.Query(ctx, `SELECT seq,type,created_at FROM events
 WHERE investigation_id=$1 AND payload->>'execution_id'=$2 AND type LIKE 'execution.%'
 ORDER BY seq DESC LIMIT 101`, x.InvestigationID, x.ID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	reverse := make([]step, 0, 101)
	for rows.Next() {
		var v step
		var at time.Time
		if err = rows.Scan(&v.Seq, &v.Type, &at); err != nil {
			return nil, false, err
		}
		v.Time = at.UTC().Format(time.RFC3339)
		reverse = append(reverse, v)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(reverse) > 100
	if truncated {
		reverse = reverse[:100]
	}
	out := make([]step, len(reverse))
	for i := range reverse {
		out[len(reverse)-1-i] = reverse[i]
	}
	return out, truncated, nil
}

func loadEventProof(ctx context.Context, tx pgx.Tx, x executionRecord) (bool, bool, error) {
	var succeeded, closed bool
	err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM events e WHERE e.investigation_id=$1
  AND e.type='execution.succeeded' AND e.payload->>'execution_id'=$2
  AND e.payload->'result'=(SELECT result FROM executions WHERE id=$2)),
 EXISTS(SELECT 1 FROM events e WHERE e.investigation_id=$1
  AND e.type='investigation.closed' AND e.payload->>'execution_id'=$2)`,
		x.InvestigationID, x.ID).Scan(&succeeded, &closed)
	return succeeded, closed, err
}

func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	scope, ok := s.ready(w, r, false)
	if !ok {
		return
	}
	if s.Rollout != nil && s.Rollout(w, r, r.PathValue("id")) {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	x, err := loadExecution(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !inScope(scope, x.EnvironmentKey) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	token, err := lockSession(r.Context(), tx, r, false)
	if !handleSessionError(w, err) {
		return
	}
	if err = lockGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); !handleAccessError(w, err) {
		return
	}
	err = s.authorizeTx(r.Context(), tx, httpapi.CurrentUser(r), x.ApplicationID,
		x.OwnerTeam, x.EnvironmentKey, "proposals", "view")
	if !handleAccessError(w, err) {
		return
	}
	a, err := loadApproval(r.Context(), tx, x.PlanID)
	if err != nil {
		unavailable(w)
		return
	}
	report, err := loadReport(r.Context(), tx, x)
	if err != nil {
		unavailable(w)
		return
	}
	publication, err := loadPublication(r.Context(), tx, x.ID)
	if err != nil {
		unavailable(w)
		return
	}
	steps, truncated, err := loadSteps(r.Context(), tx, x)
	if err != nil {
		unavailable(w)
		return
	}
	observation, err := decodeObservation(x.Result)
	if err != nil {
		unavailable(w)
		return
	}
	var observationView any
	if observation != nil {
		observationView = map[string]any{
			"recovered": observation.Recovered, "started_at": observation.StartedAt,
			"finished_at": observation.FinishedAt, "samples": observation.Samples,
			"requests": observation.Requests, "reason": observation.Reason,
			"values": nil, "threshold": nil, "labels": nil, "band": nil, "marker": nil, "checks": nil,
		}
	}
	var reportView any
	if validReport(x, a, report) {
		var publishedBy, publishedAt any
		if publication != nil {
			publishedBy = publication.PublishedBy
			publishedAt = publication.PublishedAt.UTC().Format(time.RFC3339)
		}
		reportView = map[string]any{
			"title": x.InvestigationTitle, "summary": report.Summary,
			"closed_by": report.ClosedBy, "closed_at": report.ClosedAt.UTC().Format(time.RFC3339),
			"approved_by": report.ApprovedBy, "published": publication != nil,
			"published_by": publishedBy, "published_at": publishedAt,
			"severity": nil, "impact": nil, "timeline": nil, "actions": nil, "refs": nil,
		}
	}
	if publication != nil && (report == nil || reportDigest(report.Raw) != publication.Digest || reportView == nil) {
		unavailable(w)
		return
	}
	missing := []string{"v3_proposal_link", "observation_series", "observation_threshold", "observation_checks", "report_severity", "report_impact", "report_actions", "report_refs"}
	if observation == nil {
		missing = append(missing, "recovery_observation")
	}
	if !validReport(x, a, report) {
		missing = append(missing, "closed_report")
	}
	if truncated {
		missing = append(missing, "complete_execution_events")
	}
	if err = sessionStillActive(r.Context(), tx, token, httpapi.CurrentUser(r).ID); !handleSessionError(w, err) {
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	var approvedBy any
	if a.Decision == "approved" && a.ApprovedBy != "" {
		approvedBy = a.ApprovedBy
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"id": x.ID, "plan_id": x.PlanID, "proposal_id": nil,
		"investigation_id": x.InvestigationID, "application_id": x.ApplicationID,
		"environment_id": x.EnvironmentKey, "status": x.Status,
		"status_source": "executions.status", "attempt": x.Attempt,
		"error": x.Error, "created_at": x.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": x.UpdatedAt.UTC().Format(time.RFC3339),
		"approval": map[string]any{"decision": a.Decision, "approved_by": approvedBy,
			"digest_match": a.Decision == "approved" && a.Digest == x.PlanDigest && x.ProposalDigest == x.PlanDigest},
		"steps": steps, "steps_truncated": truncated,
		"observation": observationView, "report": reportView,
		"partial": true, "missing": missing,
	})
}

func unavailable(w http.ResponseWriter) {
	httpapi.Error(w, 503, "execution_unavailable", "Execution storage or policy is unavailable")
}

func notFound(w http.ResponseWriter) {
	httpapi.Error(w, 404, "not_found", "Execution not found")
}

func handleSessionError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "session_revoked", "Sign in again")
	} else if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "human_required", "A human session is required")
	} else {
		unavailable(w)
	}
	return false
}

func handleAccessError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, store.ErrForbidden) {
		notFound(w)
	} else {
		unavailable(w)
	}
	return false
}
