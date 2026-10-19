package v3alertrules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const trialDuration = 24 * time.Hour

var trialFingerprintRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TrialRuntime is a deployment-provided, durable, non-notifying evaluator.
// IsolationFingerprint must attest to a hard application AND environment data
// boundary, not just an output-label filter or a PromQL string check. Enqueue
// must acknowledge a durable, idempotent job consumed by a running worker; a
// best-effort goroutine or a disabled worker must return an error. The worker
// owns bounded PromQL evaluation, per-tick authorization/source rechecks,
// gaps, leases, and terminal result writes. No current PrometheusSource
// implements this interface: its environment endpoint is shared.
type TrialRuntime interface {
	IsolationFingerprint(context.Context, string, string) (string, error)
	Enqueue(context.Context, TrialJob) error
}

type TrialJob struct {
	ID                 string
	ActorID            string
	EnvironmentID      string
	ApplicationID      string
	SourceFingerprint  string
	ProviderFileDigest string
	Duration           time.Duration
	Spec               DraftRuleSpec
}

type trialRecord struct {
	ID, ActorID, EnvironmentID, ApplicationID, SourceFingerprint, ProviderFileDigest, Status string
	RequestDigest                                                                            string
	Spec                                                                                     DraftRuleSpec
	StartedAt, ExpiresAt                                                                     *time.Time
	UpdatedAt                                                                                time.Time
	EvaluatedPoints, MissingPoints, WouldFire                                                int
	Windows                                                                                  []string
}

// handleTrial is the mode=dry_run branch of POST /alert-rules. It never creates
// a provider rule, GitLab MR, or notification. The accepted response is queued
// only after a trusted isolated worker durably acknowledges the job.
func (s *WriteService) handleTrial(w http.ResponseWriter, r *http.Request, env string, actor core.User, key string, spec DraftRuleSpec) {
	if s == nil || s.DB == nil || s.Access == nil || s.Reader == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_isolation_unavailable", "An application-isolated trial worker is unavailable")
		return
	}
	if spec.Mode != "dry_run" {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_mode", "Choose dry_run mode")
		return
	}
	validated := spec
	validated.Mode = "draft" // Reuse the existing shape validator without allowing draft export.
	if err := validateDraftSpec(validated, env, spec.ApplicationID, s.Reader.ApplicationLabel(env, spec.ApplicationID)); err != nil {
		writeError(w, validationProblem(err))
		return
	}
	if err := s.authorize(r.Context(), actor, env, spec.ApplicationID); err != nil {
		writeError(w, err)
		return
	}
	// Which sources this environment has is said only to an authorized actor (PA-108).
	if err := s.sourceReady(); err != nil {
		writeError(w, err)
		return
	}
	if s.TrialRuntime == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_isolation_unavailable", "An application-isolated trial worker is unavailable")
		return
	}
	// A trial for an existing provider identity could be mistaken for the live
	// rule. Require a genuinely new name in the one bound group.
	groups, observed, err := s.inspectCandidates(r.Context(), env, spec.ApplicationID, []DraftRuleSpec{spec})
	if err != nil {
		writeError(w, err)
		return
	}
	if observed[0].exists || spec.ObservedRuleID != "" {
		httpapi.Error(w, http.StatusConflict, "rule_identity_conflict", "Trial rule identity already exists")
		return
	}
	providerFileDigest := fileIdentityDigest(groups[0].file)
	if err := s.validatePromQL(r.Context(), env, spec.Expr); err != nil {
		writeError(w, err)
		return
	}
	fingerprint, err := s.TrialRuntime.IsolationFingerprint(r.Context(), env, spec.ApplicationID)
	if err != nil || !trialFingerprintRE.MatchString(fingerprint) {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_isolation_unavailable", "An application-isolated trial source is unavailable")
		return
	}
	digest, err := requestHash("dry_run", env, "", spec)
	if err != nil {
		writeError(w, err)
		return
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		writeError(w, err)
		return
	}
	if len(encoded) > 32<<10 {
		httpapi.Error(w, http.StatusRequestEntityTooLarge, "trial_too_large", "Alert rule trial exceeds the size limit")
		return
	}
	record, err := s.reserveTrial(r.Context(), actor, env, key, digest, fingerprint, providerFileDigest, spec, encoded)
	if err != nil {
		writeError(w, err)
		return
	}
	if record.Status == "dispatch_pending" {
		job := TrialJob{ID: record.ID, ActorID: actor.ID, EnvironmentID: env,
			ApplicationID: spec.ApplicationID, SourceFingerprint: fingerprint,
			ProviderFileDigest: providerFileDigest,
			Duration:           trialDuration, Spec: spec}
		if err := s.TrialRuntime.Enqueue(r.Context(), job); err != nil {
			_ = s.stopPendingTrial(context.WithoutCancel(r.Context()), record.ID, actor.ID)
			httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_dispatch_unavailable", "Trial worker did not accept the job")
			return
		}
		if err := s.confirmTrialDispatch(r.Context(), actor, record.ID, fingerprint); err != nil {
			_ = s.stopPendingTrial(context.WithoutCancel(r.Context()), record.ID, actor.ID)
			writeError(w, err)
			return
		}
		record.Status = "queued"
	}
	if record.Status == "stopped" {
		httpapi.Error(w, http.StatusServiceUnavailable, "dry_run_stopped", "Trial was stopped; submit a new request key after restoring its source")
		return
	}
	// An idempotent replay can encounter a worker that already advanced the
	// status. Read the persisted state rather than inventing "running" here.
	current, err := s.loadTrial(r.Context(), record.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusAccepted, trialResponse(current))
}

func (s *WriteService) stopPendingTrial(ctx context.Context, id, actorID string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := s.DB.Exec(requestCtx, `UPDATE v3_alert_rule_trials SET status='stopped',updated_at=clock_timestamp()
 WHERE id=$1 AND actor_id=$2 AND status='dispatch_pending'`, id, actorID)
	return err
}

func (s *WriteService) reserveTrial(ctx context.Context, actor core.User, env, key, digest, fingerprint, providerFileDigest string,
	spec DraftRuleSpec, encoded []byte) (trialRecord, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return trialRecord{}, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorizeTx(ctx, tx, actor, env, spec.ApplicationID); err != nil {
		return trialRecord{}, err
	}
	id := store.ID()
	tag, err := tx.Exec(ctx, `INSERT INTO v3_alert_rule_trials
 (id,actor_id,idempotency_key,request_digest,environment_id,application_id,source_fingerprint,provider_file_digest,spec)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)
 ON CONFLICT(actor_id,idempotency_key) DO NOTHING`, id, actor.ID, key, digest, env, spec.ApplicationID, fingerprint, providerFileDigest, encoded)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return trialRecord{}, problem(http.StatusTooManyRequests, "trial_capacity_full", "The isolated trial worker is at capacity")
		}
		return trialRecord{}, err
	}
	if tag.RowsAffected() == 1 {
		_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,application_id,action,target,outcome,changes)
 VALUES($1,$2,'v3.alert_rule_trial.requested',$3,'pending',jsonb_build_object(
 'environment_id',$4::text,'request_digest',$5::text,'source_fingerprint',$6::text))`,
			actor.ID, spec.ApplicationID, id, env, digest, fingerprint)
		if err != nil {
			return trialRecord{}, err
		}
	}
	var record trialRecord
	var rawSpec []byte
	err = tx.QueryRow(ctx, `SELECT id,actor_id,request_digest,environment_id,application_id,source_fingerprint,
 provider_file_digest,spec,status FROM v3_alert_rule_trials WHERE actor_id=$1 AND idempotency_key=$2 FOR UPDATE`, actor.ID, key).
		Scan(&record.ID, &record.ActorID, &record.RequestDigest, &record.EnvironmentID,
			&record.ApplicationID, &record.SourceFingerprint, &record.ProviderFileDigest, &rawSpec, &record.Status)
	if err != nil {
		return trialRecord{}, err
	}
	if record.RequestDigest != digest || record.EnvironmentID != env || record.ApplicationID != spec.ApplicationID {
		return trialRecord{}, problem(http.StatusConflict, "idempotency_conflict", "Idempotency key was used for another request")
	}
	if record.SourceFingerprint != fingerprint {
		return trialRecord{}, problem(http.StatusConflict, "trial_source_changed", "Trial isolation source changed")
	}
	if record.ProviderFileDigest != providerFileDigest {
		return trialRecord{}, problem(http.StatusConflict, "trial_source_changed", "Trial rule group source changed")
	}
	if err := json.Unmarshal(rawSpec, &record.Spec); err != nil {
		return trialRecord{}, problem(http.StatusServiceUnavailable, "trial_invalid", "Trial record is unavailable")
	}
	if err := tx.Commit(ctx); err != nil {
		return trialRecord{}, err
	}
	return record, nil
}

func (s *WriteService) confirmTrialDispatch(ctx context.Context, actor core.User, id, fingerprint string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var env, app, source, state string
	err = tx.QueryRow(ctx, `SELECT environment_id,application_id,source_fingerprint,status
 FROM v3_alert_rule_trials WHERE id=$1 AND actor_id=$2 FOR UPDATE`, id, actor.ID).
		Scan(&env, &app, &source, &state)
	if err != nil {
		return err
	}
	if source != fingerprint {
		return problem(http.StatusConflict, "trial_source_changed", "Trial isolation source changed")
	}
	if state != "dispatch_pending" && state != "queued" && state != "running" &&
		state != "completed" && state != "inconclusive" {
		return problem(http.StatusServiceUnavailable, "dry_run_stopped", "Trial was not accepted by the worker")
	}
	if err := s.authorizeTx(ctx, tx, actor, env, app); err != nil {
		return err
	}
	if state == "dispatch_pending" {
		if _, err = tx.Exec(ctx, `UPDATE v3_alert_rule_trials SET status='queued',updated_at=clock_timestamp()
 WHERE id=$1 AND status='dispatch_pending'`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// RegisterTrials adds a status endpoint without changing the provider-rule
// detail route. The caller must register it alongside WriteService.Register.
func (s *WriteService) RegisterTrials(api *httpapi.Server) {
	api.Register("GET /api/v1/alert-rules/dry-runs/{id}", "viewer", s.TrialStatus)
	api.MarkV3ObjectRoute("GET /api/v1/alert-rules/dry-runs/{id}", "alert_rules", "change")
}

func (s *WriteService) TrialStatus(w http.ResponseWriter, r *http.Request) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusConflict, "single_environment_required", "Select one environment")
		return
	}
	actor := httpapi.CurrentUser(r)
	if s == nil || s.DB == nil || s.Access == nil || actor.ID == "" || actor.Disabled {
		httpapi.Error(w, http.StatusServiceUnavailable, "trial_unavailable", "Trial status is unavailable")
		return
	}
	id := r.PathValue("id")
	if !hexID(id) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Trial not found")
		return
	}
	record, err := s.loadTrial(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (record.ActorID != actor.ID || record.EnvironmentID != scope.EnvironmentIDs[0])) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Trial not found")
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.authorize(r.Context(), actor, record.EnvironmentID, record.ApplicationID); err != nil {
		writeError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, trialResponse(record))
}

func (s *WriteService) loadTrial(ctx context.Context, id string) (trialRecord, error) {
	var record trialRecord
	var rawSpec, rawWindows []byte
	err := s.DB.QueryRow(ctx, `SELECT id,actor_id,request_digest,environment_id,application_id,source_fingerprint,
 provider_file_digest,spec,status,started_at,expires_at,evaluated_points,missing_points,would_fire,windows,updated_at
 FROM v3_alert_rule_trials WHERE id=$1`, id).
		Scan(&record.ID, &record.ActorID, &record.RequestDigest, &record.EnvironmentID,
			&record.ApplicationID, &record.SourceFingerprint, &record.ProviderFileDigest, &rawSpec, &record.Status,
			&record.StartedAt, &record.ExpiresAt, &record.EvaluatedPoints, &record.MissingPoints,
			&record.WouldFire, &rawWindows, &record.UpdatedAt)
	if err != nil {
		return record, err
	}
	if json.Unmarshal(rawSpec, &record.Spec) != nil || json.Unmarshal(rawWindows, &record.Windows) != nil {
		return record, problem(http.StatusServiceUnavailable, "trial_invalid", "Trial record is unavailable")
	}
	return record, nil
}

func trialResponse(record trialRecord) map[string]any {
	status := record.Status
	if (status == "queued" || status == "running") && !record.UpdatedAt.IsZero() &&
		time.Since(record.UpdatedAt) > 2*trialStep {
		status = "stalled"
	}
	return map[string]any{
		"trial_id": record.ID, "status": status,
		"started_at": record.StartedAt, "expires_at": record.ExpiresAt,
		"last_update":      record.UpdatedAt,
		"evaluated_points": record.EvaluatedPoints, "missing_points": record.MissingPoints,
		"would_fire": record.WouldFire, "windows": record.Windows,
	}
}
