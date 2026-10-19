package v3changeview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/destinywans/redstring/internal/delivery"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type acceptReviewRequest struct {
	ServiceID                 string    `json:"service_id"`
	Decision                  string    `json:"decision"`
	ExpectedRunDigest         string    `json:"expected_run_digest"`
	ExpectedRunUpdatedAt      time.Time `json:"expected_run_updated_at"`
	ExecutionID               string    `json:"execution_id"`
	ExpectedPlanDigest        string    `json:"expected_plan_digest"`
	ExpectedArtifactDigest    string    `json:"expected_artifact_digest"`
	ExpectedCommitSHA         string    `json:"expected_commit_sha"`
	IncidentID                string    `json:"incident_id"`
	ExpectedIncidentUpdatedAt time.Time `json:"expected_incident_updated_at"`
	ExpectedDiffDigest        string    `json:"expected_diff_digest"`
	ExpectedPreviousReviewID  string    `json:"expected_previous_review_id"`
	Diff                      struct {
		Path  string `json:"path"`
		Minus string `json:"minus"`
		Plus  string `json:"plus"`
	} `json:"diff"`
	Risk struct {
		Title string `json:"title"`
		Meta  string `json:"meta"`
		Text  string `json:"text"`
	} `json:"risk"`
	AlertText string `json:"alert_text"`
}

var errReviewStale = errors.New("change review source changed or expired")
var errReviewConflict = errors.New("change run already has a different review")
var errReviewHidden = errors.New("change review service is not visible")

func (in acceptReviewRequest) valid() bool {
	return in.Decision == "accept_for_display" && in.ServiceID != "" &&
		len(in.ServiceID) <= 1024 && validSHA256Hex(in.ExpectedRunDigest) &&
		!in.ExpectedRunUpdatedAt.IsZero() && reviewText(in.ExecutionID, 128) &&
		reviewText(in.ExpectedPlanDigest, 128) && len(in.ExpectedArtifactDigest) == 71 &&
		in.ExpectedArtifactDigest[:7] == "sha256:" && validSHA256Hex(in.ExpectedArtifactDigest[7:]) &&
		validCommitSHA(in.ExpectedCommitSHA) && reviewText(in.IncidentID, 128) &&
		!in.ExpectedIncidentUpdatedAt.IsZero() &&
		in.ExpectedDiffDigest == reviewedDiffDigest(in.Diff.Path, in.Diff.Minus, in.Diff.Plus) &&
		validPreviousReviewID(in.ExpectedPreviousReviewID) &&
		reviewText(in.Diff.Path, 300) && reviewText(in.Diff.Minus, 1000) &&
		reviewText(in.Diff.Plus, 1000) && reviewText(in.Risk.Title, 200) &&
		reviewText(in.Risk.Meta, 300) && reviewText(in.Risk.Text, 2000) &&
		reviewText(in.AlertText, 500)
}

func validPreviousReviewID(value string) bool {
	if value == "" {
		return true
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

func writeReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 403, "forbidden", "A current human change reviewer is required")
	case errors.Is(err, pgx.ErrNoRows), errors.Is(err, errReviewHidden):
		httpapi.Error(w, 404, "not_found", "Change run not found")
	case errors.Is(err, errReviewStale):
		httpapi.Error(w, 409, "change_review_stale", "Run, deployment or incident changed; review current evidence")
	case errors.Is(err, errReviewConflict):
		httpapi.Error(w, 409, "change_review_conflict", "This run already has a different accepted review")
	default:
		httpapi.Error(w, 503, "change_review_unavailable", "Change review source or authorization unavailable")
	}
}

func (s *Service) acceptReview(w http.ResponseWriter, r *http.Request) {
	var in acceptReviewRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	runID := r.PathValue("run_id")
	app, env, ok := decodeServiceID(in.ServiceID)
	if !ok || !in.valid() || !reviewText(runID, 128) {
		httpapi.Error(w, 422, "invalid_change_review", "Select exact reviewed source versions and complete review text")
		return
	}
	if s == nil || s.Store == nil || s.Store.DB == nil || s.Access == nil {
		writeReviewError(w, errIncomplete)
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 || scope.EnvironmentIDs[0] != env {
		writeReviewError(w, pgx.ErrNoRows)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	actor := httpapi.CurrentUser(r)
	cookie, err := r.Cookie("aiops_session")
	if err != nil || actor.ID == "" {
		writeReviewError(w, store.ErrSessionRevoked)
		return
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = lockChangeReviewAuthoritySources(ctx, tx); err == nil {
		err = store.LockActiveSession(ctx, tx, cookie.Value, actor.ID)
	}
	if err != nil {
		writeReviewError(w, err)
		return
	}
	row, err := loadReviewService(ctx, tx, app, env)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if err = s.authorizeChangeReview(ctx, tx, r, row); err != nil {
		writeReviewError(w, err)
		return
	}
	// Serialize all reviewers for this persisted run, including
	// retries, before checking the single immutable accepted audit record.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734915,hashtext($1))`, app+"|"+runID); err != nil {
		writeReviewError(w, err)
		return
	}
	now := nowOf(s)
	run, err := loadReviewRun(ctx, tx, row, runID, now)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if run.Snapshot.Digest != in.ExpectedRunDigest || !run.UpdatedAt.Equal(in.ExpectedRunUpdatedAt) {
		writeReviewError(w, errReviewStale)
		return
	}
	deploy, err := loadReviewDeployment(ctx, tx, row, run, in.ExecutionID, now)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if deploy.PlanDigest != in.ExpectedPlanDigest || deploy.ArtifactDigest != in.ExpectedArtifactDigest ||
		deploy.CommitSHA != in.ExpectedCommitSHA {
		writeReviewError(w, errReviewStale)
		return
	}
	incident, err := loadReviewIncident(ctx, tx, row, in.IncidentID, deploy.At, now)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if !incident.UpdatedAt.Equal(in.ExpectedIncidentUpdatedAt) {
		writeReviewError(w, errReviewStale)
		return
	}
	payload := auditReviewPayload{
		SchemaVersion: 1, DisplayApproved: true, EnvironmentID: row.EnvID,
		SupersedesReviewID: in.ExpectedPreviousReviewID,
		RunDigest:          run.Snapshot.Digest, RunUpdatedAt: run.UpdatedAt,
		ArtifactDigest: deploy.ArtifactDigest, ExecutionID: deploy.ExecutionID,
		PlanDigest: deploy.PlanDigest, CommitSHA: deploy.CommitSHA,
		IncidentID: incident.ID, IncidentUpdatedAt: incident.UpdatedAt,
		DiffDigest: in.ExpectedDiffDigest, AlertText: in.AlertText,
	}
	payload.Diff = in.Diff
	payload.Risk = in.Risk
	raw, err := json.Marshal(payload)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	rows, err := tx.Query(ctx, `SELECT id,actor_id,created_at,changes=$3::jsonb
 FROM management_audit WHERE application_id=$1 AND target=$2
 AND action='change.review.accepted' AND outcome='success'
 ORDER BY chain_seq DESC LIMIT 1`, row.AppID, run.ID, raw)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	var reviewID int64
	var reviewer string
	var reviewedAt time.Time
	var samePayload bool
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&reviewID, &reviewer, &reviewedAt, &samePayload); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if count != 0 && count == 1 && reviewer == actor.ID && samePayload {
		if err = store.LockActiveSession(ctx, tx, cookie.Value, actor.ID); err == nil {
			err = s.authorizeChangeReview(ctx, tx, r, row)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			writeReviewError(w, err)
			return
		}
		writeReviewAccepted(w, 200, reviewID, run.ID, reviewedAt)
		return
	}
	if (count == 0 && in.ExpectedPreviousReviewID != "") ||
		(count != 0 && in.ExpectedPreviousReviewID != strconv.FormatInt(reviewID, 10)) {
		writeReviewError(w, errReviewConflict)
		return
	}
	if err = store.LockActiveSession(ctx, tx, cookie.Value, actor.ID); err == nil {
		err = s.authorizeChangeReview(ctx, tx, r, row)
	}
	if err != nil {
		writeReviewError(w, err)
		return
	}
	err = tx.QueryRow(ctx, `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES($1,'user',$2,'change.review.accepted',$3,'success',$4::jsonb)
 RETURNING id,created_at`, actor.ID, row.AppID, run.ID, raw).Scan(&reviewID, &reviewedAt)
	if err == nil {
		// The audit chain trigger may wait on its global append lock. A session
		// can expire during that wait even though its row remains locked.
		err = store.LockActiveSession(ctx, tx, cookie.Value, actor.ID)
	}
	if err == nil {
		err = s.authorizeChangeReview(ctx, tx, r, row)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeReviewError(w, err)
		return
	}
	writeReviewAccepted(w, 201, reviewID, run.ID, reviewedAt)
}

func writeReviewAccepted(w http.ResponseWriter, status int, id int64, run string, at time.Time) {
	httpapi.WriteJSON(w, status, map[string]any{
		"review_id": strconv.FormatInt(id, 10), "run_id": run,
		"reviewed_at": at.UTC().Format(time.RFC3339Nano), "status": "accepted",
	})
}

func loadReviewService(ctx context.Context, tx pgx.Tx, app, env string) (serviceRow, error) {
	var row serviceRow
	err := tx.QueryRow(ctx, `SELECT a.id,e.id,e.key,a.key,a.name,COALESCE(a.owner_team_id,'')
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.id=$1 AND NOT a.archived FOR SHARE OF a,ve,e`, app, env).
		Scan(&row.AppID, &row.EnvID, &row.EnvKey, &row.Key, &row.Name, &row.TeamID)
	return row, err
}

func loadReviewRun(ctx context.Context, tx pgx.Tx, row serviceRow, id string, now time.Time) (delivery.Run, error) {
	var run delivery.Run
	var snapshot, projection []byte
	err := tx.QueryRow(ctx, `SELECT id,pipeline_id,version,application_id,environment_id,provider,
 status,external_id,snapshot,projection,created_by,created_at,updated_at
 FROM delivery_runs WHERE id=$1 AND application_id=$2 AND environment_id=$3 FOR SHARE`,
		id, row.AppID, row.EnvID).Scan(&run.ID, &run.PipelineID, &run.Version,
		&run.ApplicationID, &run.EnvironmentID, &run.Provider, &run.Status,
		&run.ExternalID, &snapshot, &projection, &run.CreatedBy, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		return run, err
	}
	if json.Unmarshal(snapshot, &run.Snapshot) != nil || json.Unmarshal(projection, &run.Projection) != nil ||
		validateRun(run) != nil || run.Status != "succeeded" {
		return run, errIncomplete
	}
	if _, err = projectStages(run); err != nil {
		return run, errIncomplete
	}
	if run.CreatedAt.Before(now.Add(-7*24*time.Hour)) || !run.CreatedAt.Before(now) || run.UpdatedAt.After(now) {
		return run, errReviewStale
	}
	return run, nil
}

func loadReviewDeployment(ctx context.Context, tx pgx.Tx, row serviceRow, run delivery.Run, executionID string, now time.Time) (deploymentRecord, error) {
	var out deploymentRecord
	rows, err := tx.Query(ctx, `SELECT x.id,x.status,p.status,p.digest,p.snapshot,de.created_at,g.node_id
 FROM deployment_executions x
 JOIN deployment_plans p ON p.id=x.plan_id
 JOIN delivery_gates g ON g.plan_id=p.id AND g.execution_id=x.id
 JOIN deployment_events de ON de.execution_id=x.id AND de.type='succeeded'
 WHERE x.id=$1 AND g.run_id=$2 AND p.application_id=$3 AND p.environment_id=$4
 ORDER BY de.sequence LIMIT 2 FOR SHARE OF x,p,g,de`, executionID, run.ID, row.AppID, row.EnvID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var raw []byte
	var status, planStatus, nodeID string
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&out.ExecutionID, &status, &planStatus, &out.PlanDigest, &raw, &out.At, &nodeID); err != nil {
			return out, err
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if count != 1 {
		return out, errReviewStale
	}
	var plan struct {
		TargetKind string                  `json:"target_kind"`
		Source     delivery.VerifiedSource `json:"source"`
	}
	if json.Unmarshal(raw, &plan) != nil || plan.TargetKind != "gitops" || status != "succeeded" || planStatus != "approved" ||
		plan.Source.RunID != run.ID || !verifiedReviewSourceNode(run, nodeID, plan.Source.NodeID, plan.Source.ArtifactDigest) ||
		plan.Source.ApplicationID != row.AppID || plan.Source.EnvironmentID != row.EnvID ||
		plan.Source.PipelineDigest != run.Snapshot.Digest || !validCommitSHA(plan.Source.CommitSHA) ||
		!reviewText(plan.Source.ArtifactDigest, 128) || !reviewText(out.PlanDigest, 128) ||
		out.At.Before(run.CreatedAt) || out.At.Before(now.Add(-7*24*time.Hour)) || out.At.After(now) {
		return out, errReviewStale
	}
	out.RunID, out.PipelineDigest = run.ID, run.Snapshot.Digest
	out.ArtifactDigest, out.CommitSHA = plan.Source.ArtifactDigest, plan.Source.CommitSHA
	out.Parent = run
	return out, nil
}

func verifiedReviewSourceNode(run delivery.Run, gateNodeID, sourceNodeID, artifactDigest string) bool {
	deploy := false
	for _, node := range run.Snapshot.Definition.Nodes {
		if node.ID == gateNodeID && node.Kind == "deploy" {
			deploy = true
			break
		}
	}
	if !deploy || sourceNodeID == "" {
		return false
	}
	for _, node := range run.Projection.Nodes {
		if (node.Name == sourceNodeID || node.ID == sourceNodeID) &&
			node.Status == "succeeded" && node.Outputs["artifact-digest"] == artifactDigest {
			return true
		}
	}
	return false
}

func loadReviewIncident(ctx context.Context, tx pgx.Tx, row serviceRow, id string, deployedAt, now time.Time) (alertRecord, error) {
	var out alertRecord
	err := tx.QueryRow(ctx, `SELECT id,title,alert_status,starts_at,ends_at,updated_at
 FROM ops_incidents WHERE id=$1 AND application_id=$2 AND environment_id=$3
 AND NOT suspected FOR SHARE`, id, row.AppID, row.EnvID).
		Scan(&out.ID, &out.Title, &out.Status, &out.StartsAt, &out.EndsAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errReviewStale
	}
	if err != nil {
		return out, err
	}
	if (out.Status != "firing" && out.Status != "resolved") ||
		(out.Status == "firing" && out.EndsAt != nil) ||
		(out.Status == "resolved" && out.EndsAt == nil) ||
		out.StartsAt.Before(deployedAt) || out.StartsAt.After(deployedAt.Add(2*time.Hour)) || out.StartsAt.After(now) ||
		(out.EndsAt != nil && (out.EndsAt.Before(out.StartsAt) || out.EndsAt.After(now))) {
		return out, errReviewStale
	}
	return out, nil
}
