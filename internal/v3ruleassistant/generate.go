package v3ruleassistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type generateInput struct {
	Text          string `json:"text"`
	ApplicationID string `json:"application_id,omitempty"`
}

type proposedRule struct {
	QueryID    string  `json:"query_id"`
	Query      string  `json:"query"`
	Op         string  `json:"op"`
	Threshold  float64 `json:"threshold"`
	ForMinutes int     `json:"for_minutes"`
	Severity   string  `json:"severity"`
	Summary    string  `json:"summary"`
}

type generatedDraft struct {
	Query         string  `json:"query"`
	Op            string  `json:"op"`
	Threshold     float64 `json:"threshold"`
	Unit          string  `json:"unit"`
	ForMinutes    int     `json:"for_minutes"`
	Severity      string  `json:"severity"`
	Service       string  `json:"service"`
	Summary       string  `json:"summary"`
	ApplicationID string  `json:"application_id"`
	EnvironmentID string  `json:"environment_id"`
	QueryID       string  `json:"query_id"`
	DraftID       string  `json:"draft_id"`
	Status        string  `json:"status"`
	Model         struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
	} `json:"model"`
}

func parseGeneratedRule(raw string, choices []PreviewBinding, env, app, id string, model GenerateModelBinding) (generatedDraft, error) {
	var proposed proposedRule
	if raw == "" || len(raw) > 16<<10 {
		return generatedDraft{}, errors.New("empty or oversized model draft")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || len(fields) != 7 {
		return generatedDraft{}, errors.New("incomplete model draft")
	}
	for _, name := range []string{"query_id", "query", "op", "threshold", "for_minutes", "severity", "summary"} {
		if _, ok := fields[name]; !ok {
			return generatedDraft{}, errors.New("incomplete model draft")
		}
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&proposed) != nil || dec.Decode(new(any)) != io.EOF {
		return generatedDraft{}, errors.New("invalid model draft JSON")
	}
	if proposed.Op != ">" && proposed.Op != "<" ||
		math.IsNaN(proposed.Threshold) || math.IsInf(proposed.Threshold, 0) || math.Abs(proposed.Threshold) > 1e9 ||
		proposed.ForMinutes < 0 || proposed.ForMinutes > 60 ||
		proposed.Severity != "P1" && proposed.Severity != "P2" && proposed.Severity != "P3" && proposed.Severity != "P4" ||
		!boundedText(proposed.Summary, 240) || strings.ContainsAny(proposed.Summary, "\r\n") {
		return generatedDraft{}, errors.New("out-of-bounds model draft")
	}
	var selected *PreviewBinding
	for i := range choices {
		if choices[i].QueryID == proposed.QueryID {
			selected = &choices[i]
			break
		}
	}
	if selected == nil || validateDraftPromQL(*selected, proposed.Query) != nil {
		return generatedDraft{}, errors.New("unreviewed model PromQL")
	}
	unit := selected.Unit
	if proposed.Query != selected.Query {
		// A generated expression can change rate, increase, or arithmetic.
		// Only the reviewed expression carries a reviewed unit.
		unit = "unknown"
	}
	return generatedDraft{Query: proposed.Query, Op: proposed.Op, Threshold: proposed.Threshold,
		Unit: unit, ForMinutes: proposed.ForMinutes, Severity: proposed.Severity,
		Service: selected.Service, Summary: proposed.Summary, ApplicationID: app,
		EnvironmentID: env, QueryID: selected.QueryID, DraftID: id, Status: "review_required",
		Model: struct {
			Provider string `json:"provider"`
			Name     string `json:"name"`
		}{Provider: model.Provider, Name: model.Model}}, nil
}

func shaJSON(value any) string {
	b, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func validGenerateBinding(b GenerateModelBinding) bool {
	if !boundedText(b.Provider, 100) || !boundedText(b.Model, 160) ||
		b.MaxInputBytes < 1 || b.MaxInputBytes > 16<<10 || b.MaxOutputTokens < 1 || b.MaxOutputTokens > 256 ||
		b.MaxCostMicroUSD < 1 || b.MaxCostMicroUSD > 100000 ||
		b.InputPriceMicroUSDPerMillion < 1 || b.InputPriceMicroUSDPerMillion > 100000000 ||
		b.OutputPriceMicroUSDPerMillion < 1 || b.OutputPriceMicroUSDPerMillion > 100000000 {
		return false
	}
	estimate := (int64(b.MaxInputBytes+1024)*b.InputPriceMicroUSDPerMillion +
		int64(b.MaxOutputTokens)*b.OutputPriceMicroUSDPerMillion + 999999) / 1000000
	return estimate <= b.MaxCostMicroUSD
}

func generateAmbiguous(w http.ResponseWriter) {
	httpapi.Error(w, http.StatusBadGateway, "rule_generate_ambiguous", "Model outcome is uncertain; inspect X-Rule-Draft-Attempt-ID before a new key")
}

func generateFailed(w http.ResponseWriter) {
	httpapi.Error(w, http.StatusBadGateway, "rule_generate_failed", "Model did not produce a valid, reviewable rule draft")
}

// Generate returns a review-only draft. No alert rule, route, deployment or
// approval record is created by this handler.
func (s *Service) Generate(w http.ResponseWriter, r *http.Request) {
	env, ok := singleEnvironment(w, r)
	if !ok {
		return
	}
	var input generateInput
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Text) == "" || len(input.Text) > 2048 ||
		!boundedText(input.ApplicationID, 128) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "invalid_rule_description", "Description or application is invalid")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !boundedText(key, 128) || len(key) < 8 || strings.ContainsAny(key, "\r\n") {
		httpapi.Error(w, http.StatusUnprocessableEntity, "idempotency_key_required", "Provide an 8 to 128 character Idempotency-Key")
		return
	}
	preflight, ok := s.beginAuthorized(w, r, env, input.ApplicationID, "change")
	if !ok {
		return
	}
	if err := preflight.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Initial authorization could not be confirmed")
		return
	}
	selected, ok := s.selectBinding(w, env, input.ApplicationID, "")
	if !ok {
		return
	}
	app := selected.ApplicationID
	choices := s.choices(env, app)
	if len(choices) == 0 || len(choices) > 16 {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_catalog_unavailable", "Reviewed query catalog is unavailable or exceeds its limit")
		return
	}
	modelChoices := make([]GenerateChoice, 0, len(choices))
	seenIDs := make(map[string]bool, len(choices))
	for _, choice := range choices {
		if seenIDs[choice.QueryID] || validateDraftPromQL(choice, choice.Query) != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "rule_catalog_unavailable", "Reviewed query catalog contains unsupported PromQL")
			return
		}
		seenIDs[choice.QueryID] = true
		modelChoices = append(modelChoices, GenerateChoice{ID: choice.QueryID, Query: choice.Query,
			Service: choice.Service, Unit: choice.Unit, Description: choice.Source})
	}
	catalogHash := shaJSON(choices)
	requestHash := shaJSON([]string{env, app, input.Text, catalogHash})
	meta, replay, ok := s.reserveGenerate(w, r, env, app, key, requestHash, catalogHash)
	if !ok {
		return
	}
	w.Header().Set("X-Rule-Draft-Attempt-ID", meta.id)
	if replay != nil {
		httpapi.WriteJSON(w, http.StatusOK, replay)
		return
	}
	started := time.Now()
	result, runErr := meta.model.Run(r.Context(), GenerateModelRequest{EnvironmentID: env, Description: input.Text, Choices: modelChoices})
	duration := time.Since(started).Milliseconds()
	var draft *generatedDraft
	if runErr == nil {
		parsed, err := parseGeneratedRule(result.RawJSON, choices, env, app, meta.id, meta.binding)
		if err != nil {
			runErr = errors.Join(err, ErrGenerateOutcomeAmbiguous)
		} else {
			draft = &parsed
		}
	}
	status, code := "succeeded", ""
	if runErr != nil {
		status, code = "failed", "provider_failed"
		if errors.Is(runErr, ErrGenerateOutcomeAmbiguous) {
			status, code = "ambiguous", "uncertain_after_provider"
		}
		if errors.Is(runErr, ErrGenerateBudgetExceeded) {
			status, code = "ambiguous", "provider_budget_exceeded"
		}
	}
	if err := s.finishGenerate(w, r, meta, status, code, draft, result, duration); err != nil {
		if errors.Is(err, errGenerateAuthorization) {
			_ = s.markGenerateAmbiguous(meta, result, duration, "authorization_changed")
			return // authorizeTx already wrote the single HTTP response.
		}
		_ = s.markGenerateAmbiguous(meta, result, duration, "result_write_uncertain")
		generateAmbiguous(w)
		return
	}
	if runErr != nil {
		if status == "ambiguous" {
			generateAmbiguous(w)
		} else {
			generateFailed(w)
		}
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, draft)
}

type generateMeta struct {
	id      string
	app     string
	env     string
	actor   string
	binding GenerateModelBinding
	model   GenerateModel
}

func (s *Service) reserveGenerate(w http.ResponseWriter, r *http.Request, env, app, key, requestHash, catalogHash string) (generateMeta, *generatedDraft, bool) {
	meta := generateMeta{app: app, env: env, actor: httpapi.CurrentUser(r).ID}
	tx, ok := s.beginAuthorized(w, r, env, app, "change")
	if !ok {
		return meta, nil, false
	}
	defer tx.Rollback(r.Context())
	var oldID, oldApp, oldEnv, oldHash, oldCatalog, oldStatus, oldError string
	var oldCreated time.Time
	var oldDraft []byte
	err := tx.QueryRow(r.Context(), `SELECT id,application_id,environment_id,request_sha256,catalog_sha256,
 status,error_code,created_at,draft FROM v3_rule_generate_attempts
 WHERE actor_id=$1 AND request_key=$2 FOR UPDATE`, meta.actor, key).
		Scan(&oldID, &oldApp, &oldEnv, &oldHash, &oldCatalog, &oldStatus, &oldError, &oldCreated, &oldDraft)
	if err == nil {
		if oldApp != app || oldEnv != env || oldHash != requestHash || oldCatalog != catalogHash {
			httpapi.Error(w, http.StatusConflict, "idempotency_conflict", "Idempotency key belongs to another draft request")
			return meta, nil, false
		}
		if oldStatus == "running" && s.now().Sub(oldCreated) > 2*time.Minute {
			_, err = tx.Exec(r.Context(), `UPDATE v3_rule_generate_attempts SET status='ambiguous',
 error_code='stale_ambiguous',finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, oldID)
			if err == nil {
				err = audit(r.Context(), tx, app, meta.actor, "v3.alert_rule.generate.ambiguous", oldID,
					map[string]any{"error_code": "stale_ambiguous"})
			}
			if err != nil {
				httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft state is unavailable")
				return meta, nil, false
			}
			oldStatus = "ambiguous"
		}
		if err = tx.Commit(r.Context()); err != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft state could not be confirmed")
			return meta, nil, false
		}
		meta.id = oldID
		w.Header().Set("X-Rule-Draft-Attempt-ID", oldID)
		if oldStatus == "succeeded" {
			var draft generatedDraft
			if json.Unmarshal(oldDraft, &draft) != nil || draft.DraftID != oldID {
				httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Saved draft is unavailable")
				return meta, nil, false
			}
			return meta, &draft, true
		}
		if oldStatus == "running" {
			httpapi.Error(w, http.StatusConflict, "draft_in_progress", "This draft request is still running")
		} else if oldStatus == "ambiguous" || strings.Contains(oldError, "ambiguous") {
			generateAmbiguous(w)
		} else {
			generateFailed(w)
		}
		return meta, nil, false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft state is unavailable")
		return meta, nil, false
	}
	if s.Model == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_generate_not_connected", "A bounded draft model is not configured")
		return meta, nil, false
	}
	meta.model = s.Model
	var bound bool
	meta.binding, bound = meta.model.Binding(env)
	if !bound || !validGenerateBinding(meta.binding) {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_generate_not_connected", "A bounded draft model is not configured")
		return meta, nil, false
	}
	meta.id = store.ID()
	command, err := tx.Exec(r.Context(), `INSERT INTO v3_rule_generate_attempts
 (id,actor_id,application_id,environment_id,request_key,request_sha256,catalog_sha256,
 model_provider,model_name,max_cost_micro_usd)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
 ON CONFLICT (actor_id,request_key) DO NOTHING`, meta.id, meta.actor, app, env, key, requestHash, catalogHash,
		meta.binding.Provider, meta.binding.Model, meta.binding.MaxCostMicroUSD)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft reservation failed")
		return meta, nil, false
	}
	if command.RowsAffected() != 1 {
		httpapi.Error(w, http.StatusConflict, "draft_in_progress", "This key was reserved concurrently; retry it")
		return meta, nil, false
	}
	budget, err := tx.Exec(r.Context(), `INSERT INTO v3_rule_generate_budget
 (actor_id,application_id,environment_id,next_allowed_at)
 VALUES($1,$2,$3,clock_timestamp()+interval '10 seconds')
 ON CONFLICT (actor_id,application_id,environment_id) DO UPDATE
 SET next_allowed_at=clock_timestamp()+interval '10 seconds'
 WHERE v3_rule_generate_budget.next_allowed_at<=clock_timestamp()`, meta.actor, app, env)
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft budget is unavailable")
		return meta, nil, false
	}
	if budget.RowsAffected() != 1 {
		httpapi.Error(w, http.StatusTooManyRequests, "rule_generate_rate_limited", "Wait before another model draft")
		return meta, nil, false
	}
	if err = audit(r.Context(), tx, app, meta.actor, "v3.alert_rule.generate.reserved", meta.id,
		map[string]any{"environment_id": env, "catalog_sha256": catalogHash,
			"request_sha256": requestHash, "model_provider": meta.binding.Provider,
			"model_name": meta.binding.Model, "max_cost_micro_usd": meta.binding.MaxCostMicroUSD}); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft audit failed")
		return meta, nil, false
	}
	if err = tx.Commit(r.Context()); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "rule_assistant_unavailable", "Draft reservation could not be confirmed")
		return meta, nil, false
	}
	return meta, nil, true
}

func cleanGenerateEvidence(m generateMeta, result GenerateModelResult) (string, *int64, *int64) {
	id := result.ProviderRequestID
	if len(id) > 256 || strings.ContainsAny(id, "\r\n\x00") {
		id = ""
	}
	in, out := result.InputTokens, result.OutputTokens
	if in != nil && *in < 0 {
		in = nil
	}
	if out != nil && *out < 0 {
		out = nil
	}
	return id, in, out
}

var errGenerateAuthorization = errors.New("final draft authorization failed")

func (s *Service) finishGenerate(w http.ResponseWriter, r *http.Request, m generateMeta, status, code string, draft *generatedDraft,
	result GenerateModelResult, duration int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if draft != nil && r.Context().Err() != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Draft request was canceled")
		return errGenerateAuthorization
	}
	if draft != nil && !s.authorizeTx(w, r.WithContext(ctx), tx, m.env, m.app, "change") {
		return errGenerateAuthorization
	}
	var payload []byte
	if draft != nil {
		payload, err = json.Marshal(draft)
		if err != nil {
			return err
		}
	}
	id, in, out := cleanGenerateEvidence(m, result)
	cmd, err := tx.Exec(ctx, `UPDATE v3_rule_generate_attempts SET status=$2,error_code=$3,
 draft=$4,provider_request_id=$5,input_tokens=$6,output_tokens=$7,duration_ms=$8,finished_at=clock_timestamp()
 WHERE id=$1 AND status='running'`, m.id, status, code, payload, id, in, out, duration)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return errors.New("draft state changed")
	}
	if err = audit(ctx, tx, m.app, m.actor, "v3.alert_rule.generate.finished", m.id,
		map[string]any{"status": status, "error_code": code, "provider_request_id": id,
			"usage_reported": in != nil && out != nil, "duration_ms": duration}); err != nil {
		return err
	}
	if draft != nil {
		if r.Context().Err() != nil {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Draft request was canceled")
			return errGenerateAuthorization
		}
		cookie, err := r.Cookie("aiops_session")
		if err != nil {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
			return errGenerateAuthorization
		}
		if err = store.LockActiveSession(ctx, tx, cookie.Value, m.actor); err != nil {
			if errors.Is(err, store.ErrSessionRevoked) {
				httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Session expired")
			} else {
				httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Session verification is unavailable")
			}
			return errGenerateAuthorization
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) markGenerateAmbiguous(m generateMeta, result GenerateModelResult, duration int64, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	id, in, out := cleanGenerateEvidence(m, result)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `UPDATE v3_rule_generate_attempts SET status='ambiguous',
 error_code=$2,provider_request_id=$3,input_tokens=$4,output_tokens=$5,
 duration_ms=$6,finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, m.id, code, id, in, out, duration)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 1 {
		if err = audit(ctx, tx, m.app, m.actor, "v3.alert_rule.generate.ambiguous", m.id,
			map[string]any{"error_code": code, "provider_request_id": id,
				"usage_reported": in != nil && out != nil}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
