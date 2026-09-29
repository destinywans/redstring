// Package v3models exposes bounded observations of configured inference models.
// It does not register models, disclose credentials, or certify generation.
package v3models

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// ModelSource is deliberately narrow: configuration remains owned by the
// process inference runtime, and the probe never receives a caller URL/key.
type ModelSource interface {
	Models() []inference.Model
	ProbeModel(context.Context, string) (inference.ModelProbeResult, error)
}

type Service struct {
	DB         *pgxpool.Pool
	Source     ModelSource
	Access     *rbac.V3Policy
	instanceID string
}

type Observation struct {
	Model     string     `json:"model"`
	Status    string     `json:"status"` // configured_unverified, verified, error, unsupported
	CheckedAt *time.Time `json:"checked_at"`
	LatencyMS *int64     `json:"latency_ms"`
	ErrorCode string     `json:"error_code,omitempty"`
}

func New(db *pgxpool.Pool, runtime inference.Runner, access *rbac.V3Policy) *Service {
	var source ModelSource
	if candidate, ok := runtime.(ModelSource); ok {
		source = candidate
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return &Service{DB: db, Source: source, Access: access, instanceID: hex.EncodeToString(id[:])}
}

func (s *Service) Register(api *httpapi.Server) {
	api.Register("POST /api/v1/models/check", "viewer", s.check)
	api.Register("GET /api/v1/models/observations", "viewer", s.observation)
	api.MarkV3Route("POST /api/v1/models/check", "models", "manage")
	api.MarkV3Route("GET /api/v1/models/observations", "models", "view")
}

func (s *Service) selectedEnvironment(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(scope.EnvironmentIDs) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "environment_scope_unavailable", "Environment scope is unavailable")
		return "", false
	}
	if scope.All && write {
		httpapi.Error(w, http.StatusConflict, "env_view_only", "All non-production environments are read-only")
		return "", false
	}
	if scope.All || len(scope.EnvironmentIDs) != 1 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_environment", "Choose one environment")
		return "", false
	}
	return scope.EnvironmentIDs[0], true
}

func (s *Service) configured(id string) bool {
	if s.Source == nil || id == "" || len(id) > 320 {
		return false
	}
	for _, model := range s.Source.Models() {
		if model.ID == id {
			return true
		}
	}
	return false
}

func (s *Service) ready(w http.ResponseWriter) bool {
	if s.DB == nil || s.Source == nil || s.Access == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation source is not connected")
		return false
	}
	return true
}

func (s *Service) observation(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	env, ok := s.selectedEnvironment(w, r, false)
	if !ok {
		return
	}
	query := r.URL.Query()
	models, present := query["model"]
	if len(query) != 1 || !present || len(models) != 1 || models[0] == "" || len(models[0]) > 320 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_model", "Choose one configured model")
		return
	}
	modelID := models[0]
	if !s.configured(modelID) {
		httpapi.Error(w, http.StatusNotFound, "model_not_configured", "Model is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	defer rollback(tx)
	if err := lockModelAuthority(ctx, tx, r, env, "view"); err != nil {
		writeAuthorityError(w, err)
		return
	}
	if err := s.Access.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "models", Action: "view", EnvironmentID: env}); err != nil {
		writeAuthorityError(w, err)
		return
	}
	view := Observation{Model: modelID, Status: "configured_unverified"}
	var instanceID, status, errorCode string
	var checkedAt time.Time
	var latencyMS int64
	err = tx.QueryRow(ctx, `SELECT process_instance_id,status,checked_at,latency_ms,error_code
 FROM v3_model_observations WHERE environment_id=$1 AND model_id=$2`, env, modelID).
		Scan(&instanceID, &status, &checkedAt, &latencyMS, &errorCode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	if err == nil && instanceID == s.instanceID {
		view.Status, view.CheckedAt, view.LatencyMS, view.ErrorCode = status, &checkedAt, &latencyMS, errorCode
	}
	if err := tx.Commit(ctx); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, view)
}

func (s *Service) check(w http.ResponseWriter, r *http.Request) {
	if !s.ready(w) {
		return
	}
	env, ok := s.selectedEnvironment(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Model string `json:"model"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if !s.configured(input.Model) {
		httpapi.Error(w, http.StatusNotFound, "model_not_configured", "Model is not configured")
		return
	}
	result, err := s.Source.ProbeModel(r.Context(), input.Model)
	if errors.Is(err, inference.ErrModelNotConfigured) {
		httpapi.Error(w, http.StatusConflict, "model_configuration_changed", "Model configuration changed during the check")
		return
	}
	if err != nil || result.Model != input.Model || !validProbeResult(result) {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_probe_unavailable", "Model metadata check is unavailable")
		return
	}
	if !s.configured(input.Model) {
		httpapi.Error(w, http.StatusConflict, "model_configuration_changed", "Model configuration changed during the check")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	defer rollback(tx)
	if err := lockModelAuthority(ctx, tx, r, env, "manage"); err != nil {
		writeAuthorityError(w, err)
		return
	}
	if err := s.Access.AuthorizeWithTx(ctx, tx, httpapi.CurrentUser(r), rbac.V3Decision{Module: "models", Action: "manage", EnvironmentID: env}); err != nil {
		writeAuthorityError(w, err)
		return
	}
	view := Observation{Model: input.Model, Status: result.Status, LatencyMS: &result.LatencyMS, ErrorCode: result.ErrorCode}
	var checkedAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO v3_model_observations
 (environment_id,model_id,process_instance_id,status,checked_at,latency_ms,error_code,checked_by)
 VALUES($1,$2,$3,$4,clock_timestamp(),$5,$6,$7)
 ON CONFLICT(environment_id,model_id) DO UPDATE SET
 process_instance_id=EXCLUDED.process_instance_id,status=EXCLUDED.status,
 checked_at=clock_timestamp(),latency_ms=EXCLUDED.latency_ms,error_code=EXCLUDED.error_code,
 checked_by=EXCLUDED.checked_by RETURNING checked_at`,
		env, input.Model, s.instanceID, result.Status, result.LatencyMS, result.ErrorCode, httpapi.CurrentUser(r).ID).
		Scan(&checkedAt)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	view.CheckedAt = &checkedAt
	changes, _ := json.Marshal(map[string]any{"environment": env, "status": result.Status, "error_code": result.ErrorCode, "latency_ms": result.LatencyMS})
	if _, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.model.checked',$2,$3,$4)`, httpapi.CurrentUser(r).ID, input.Model, result.Status, changes); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "model_observation_unavailable", "Model observation storage is unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, view)
}

func validProbeResult(result inference.ModelProbeResult) bool {
	if result.LatencyMS < 0 || len(result.ErrorCode) > 80 {
		return false
	}
	switch result.Status {
	case "verified":
		return result.ErrorCode == ""
	case "error", "unsupported":
		return result.ErrorCode != ""
	default:
		return false
	}
}

func writeAuthorityError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, http.StatusForbidden, "forbidden", "Model access was revoked during the request")
		return
	}
	httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization storage is unavailable")
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
