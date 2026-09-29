package v3agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var trialVersionPattern = regexp.MustCompile(`^v([1-9][0-9]{0,8})$`)

type trialRequest struct {
	Case string `json:"case"`
	A    string `json:"a"`
	B    string `json:"b"`
}

type trialMaterial struct {
	trialID, input, aPrompt, bPrompt string
	aVersion, bVersion               int
	model                            PromptTrialModelBinding
	adapter                          PromptTrialModelAdapter
	agent                            agent
}

func parseTrialVersion(v string) (int, bool) {
	m := trialVersionPattern.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil && n > 0
}

func trialDigest(parts ...string) string {
	b, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func trialBindingValid(b PromptTrialModelBinding) bool {
	return bounded(b.Provider, 100) && bounded(b.Model, 160) &&
		b.MaxInputBytes > 0 && b.MaxInputBytes <= 32768 &&
		b.MaxOutputTokens > 0 && b.MaxOutputTokens <= 256 &&
		b.MaxCostMicroUSD > 0 && b.MaxCostMicroUSD <= 100000 &&
		b.InputPriceMicroUSDPerMillion > 0 && b.InputPriceMicroUSDPerMillion <= 100000000 &&
		b.OutputPriceMicroUSDPerMillion > 0 && b.OutputPriceMicroUSDPerMillion <= 100000000
}

func trialAdmitted(m trialMaterial) bool {
	if len(m.input) == 0 || len(m.aPrompt) == 0 || len(m.bPrompt) == 0 {
		return false
	}
	inputA, inputB := len(m.input)+len(m.aPrompt), len(m.input)+len(m.bPrompt)
	if inputA > m.model.MaxInputBytes || inputB > m.model.MaxInputBytes {
		return false
	}
	// One token per input byte is a conservative upper bound for admission.
	// Provider billing still depends on its reported token count.
	price := m.model.InputPriceMicroUSDPerMillion
	outPrice := m.model.OutputPriceMicroUSDPerMillion
	worst := (int64(inputA+inputB+2048)*price+999999)/1000000 +
		(2*int64(m.model.MaxOutputTokens)*outPrice+999999)/1000000
	return worst <= m.model.MaxCostMicroUSD
}

// trialScope rechecks the persisted Prompt/case ownership, live scope and
// current session/grants in the same transaction. Caller always closes tx.
func (s *Service) trialScope(w http.ResponseWriter, r *http.Request, tx pgx.Tx, envs []string,
	caseID string) (prompt, agent, caseRecord, bool) {
	if !recheckPromptSession(w, r, tx) {
		return prompt{}, agent{}, caseRecord{}, false
	}
	var p prompt
	err := tx.QueryRow(r.Context(), `SELECT id,agent_id,name,current_version FROM v3_agent_prompts
 WHERE id=$1 FOR SHARE`, r.PathValue("id")).Scan(&p.ID, &p.AgentID, &p.Name, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	a, err := lockEvalAgent(r.Context(), tx, p.AgentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	c, err := loadCaseTx(r.Context(), tx, caseID, "FOR SHARE")
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (c.AgentID != a.ID || c.OriginApp != a.App || c.OriginEnv != a.Env)) {
		notFound(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	if err != nil {
		unavailable(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return prompt{}, agent{}, caseRecord{}, false
	} else if err != nil {
		unavailable(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	if !lockPromptGrantSources(w, r, tx) {
		return prompt{}, agent{}, caseRecord{}, false
	}
	var team string
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(owner_team_id,'') FROM applications WHERE id=$1 FOR SHARE`, a.App).Scan(&team); err != nil {
		unavailable(w)
		return prompt{}, agent{}, caseRecord{}, false
	}
	for _, action := range []string{"view", "change"} {
		decision := rbac.V3Decision{Module: "agents", Action: action, EnvironmentID: a.Env, ApplicationID: a.App}
		err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), decision)
		if errors.Is(err, store.ErrForbidden) && team != "" {
			decision.ApplicationID, decision.TeamID = "", team
			err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), decision)
		}
		if errors.Is(err, store.ErrForbidden) {
			if action == "view" {
				notFound(w)
			} else {
				httpapi.Error(w, 403, "forbidden", "Agent permission required")
			}
			return prompt{}, agent{}, caseRecord{}, false
		}
		if err != nil {
			unavailable(w)
			return prompt{}, agent{}, caseRecord{}, false
		}
	}
	if !recheckPromptSession(w, r, tx) {
		return prompt{}, agent{}, caseRecord{}, false
	}
	return p, a, c, true
}

func trialNotConnected(w http.ResponseWriter) {
	httpapi.Error(w, 503, "prompt_trial_not_connected", "A bounded Prompt trial model is not configured")
}

func trialConflict(w http.ResponseWriter) {
	httpapi.Error(w, 409, "prompt_trial_conflict", "Trial key is in use or belongs to another request")
}

func trialFailed(w http.ResponseWriter) {
	httpapi.Error(w, 502, "prompt_trial_upstream_failed", "Prompt trial did not complete; inspect X-Prompt-Trial-ID")
}

func trialAmbiguous(w http.ResponseWriter) {
	httpapi.Error(w, 502, "prompt_trial_ambiguous", "Provider outcome is ambiguous; inspect X-Prompt-Trial-ID before trying a new key")
}

// PromptTrial makes exactly two sequential no-tools calls through an explicit
// narrow adapter. It never holds a DB transaction open across a provider call.
func (s *Service) PromptTrial(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var in trialRequest
	if !decodePromptVersion(w, r, &in) {
		return
	}
	av, aok := parseTrialVersion(in.A)
	bv, bok := parseTrialVersion(in.B)
	if !bounded(in.Case, 128) || !aok || !bok || av == bv {
		promptRequestError(w, r, "invalid_agent_request", "请选择登记案例和两个不同版本", "Choose a registered case and two distinct versions")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !bounded(key, 128) || len(key) < 8 || strings.ContainsAny(key, "\r\n\x00") {
		promptRequestError(w, r, "idempotency_key_required", "需要 8 到 128 字符的幂等键", "Idempotency-Key must contain 8 to 128 characters")
		return
	}
	requestHash := trialDigest(r.PathValue("id"), in.Case, in.A, in.B)
	material, replay, ok := s.reserveTrial(w, r, envs, in, av, bv, key, requestHash)
	if !ok {
		return
	}
	w.Header().Set("X-Prompt-Trial-ID", material.trialID)
	if replay {
		s.writeTrialResult(w, r, material.trialID)
		return
	}
	adapter := material.adapter
	for _, side := range []struct{ name, body string }{{"a", material.aPrompt}, {"b", material.bPrompt}} {
		if !s.startTrialRun(w, r, envs, in.Case, material, side.name) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		started := time.Now()
		result, runErr := adapter.Run(ctx, PromptTrialModelRequest{
			EnvironmentID: material.agent.Env, Prompt: side.body, Input: material.input,
		})
		cancel()
		duration := time.Since(started).Milliseconds()
		if runErr == nil && (strings.TrimSpace(result.Text) == "" || len(result.Text) > 16384 || len(result.ProviderRequestID) > 256 ||
			result.OutputTokens != nil && (*result.OutputTokens < 0 || *result.OutputTokens > int64(material.model.MaxOutputTokens)) ||
			result.InputTokens != nil && *result.InputTokens < 0) {
			runErr = errors.Join(errors.New("provider returned an out-of-bounds result"), ErrPromptTrialOutcomeAmbiguous)
		}
		if err := s.finishTrialRun(material, side.name, result, duration, runErr); err != nil {
			// A provider call may have completed even when PG cannot confirm its
			// result. A second bounded write records usage without another call.
			_ = s.recordTrialAmbiguous(material, side.name, result, duration)
			trialAmbiguous(w)
			return
		}
		if runErr != nil {
			if errors.Is(runErr, ErrPromptTrialOutcomeAmbiguous) {
				trialAmbiguous(w)
			} else {
				trialFailed(w)
			}
			return
		}
	}
	if !s.trialFinalAuthority(w, r, envs, in.Case) {
		return
	}
	s.writeTrialResult(w, r, material.trialID)
}

func (s *Service) reserveTrial(w http.ResponseWriter, r *http.Request, envs []string, in trialRequest,
	av, bv int, key, requestHash string) (trialMaterial, bool, bool) {
	var m trialMaterial
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	defer tx.Rollback(r.Context())
	p, a, c, ok := s.trialScope(w, r, tx, envs, in.Case)
	if !ok {
		return m, false, false
	}
	m.agent, m.aVersion, m.bVersion = a, av, bv
	var priorID, priorHash, priorPrompt, priorCase, priorApp, priorEnv, priorStatus, priorError string
	var priorCreated time.Time
	err = tx.QueryRow(r.Context(), `SELECT id,request_sha256,prompt_id,case_id,application_id,environment_id,
 status,error_code,created_at FROM v3_agent_prompt_trials WHERE actor_id=$1 AND request_key=$2 FOR UPDATE`,
		httpapi.CurrentUser(r).ID, key).
		Scan(&priorID, &priorHash, &priorPrompt, &priorCase, &priorApp, &priorEnv, &priorStatus, &priorError, &priorCreated)
	if err == nil {
		if priorHash != requestHash || priorPrompt != p.ID || priorCase != c.ID || priorApp != a.App || priorEnv != a.Env {
			trialConflict(w)
			return m, false, false
		}
		if priorStatus == "running" && time.Since(priorCreated) > 2*time.Minute {
			_, err = tx.Exec(r.Context(), `UPDATE v3_agent_prompt_trials
 SET status='failed',error_code='stale_ambiguous',finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, priorID)
			if err == nil {
				err = audit(r.Context(), tx, a, "system", "v3.agent.prompt_trial.failed", priorID,
					map[string]any{"error_code": "stale_ambiguous"})
			}
			if err != nil {
				unavailable(w)
				return m, false, false
			}
			priorStatus, priorError = "failed", "stale_ambiguous"
		}
		if err = tx.Commit(r.Context()); err != nil {
			unavailable(w)
			return m, false, false
		}
		m.trialID = priorID
		w.Header().Set("X-Prompt-Trial-ID", priorID)
		if priorStatus == "succeeded" {
			return m, true, true
		}
		if priorStatus == "failed" {
			if strings.Contains(priorError, "ambiguous") {
				trialAmbiguous(w)
			} else {
				trialFailed(w)
			}
		} else {
			trialConflict(w)
		}
		return m, false, false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		unavailable(w)
		return m, false, false
	}
	var inputID, inputHash string
	var inputRevision int64
	inputID, m.input, inputHash, inputRevision, err = loadPromptTrialCaseInput(r.Context(), tx, c, a)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "prompt_trial_case_input_unavailable", "Registered case has no trusted trial input")
		return m, false, false
	}
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	rows, err := tx.Query(r.Context(), `SELECT version,body FROM v3_agent_prompt_versions
 WHERE prompt_id=$1 AND version=ANY($2) FOR SHARE`, p.ID, []int{av, bv})
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	count := 0
	for rows.Next() {
		var n int
		var body string
		if err = rows.Scan(&n, &body); err != nil {
			break
		}
		if n == av {
			m.aPrompt = body
		} else if n == bv {
			m.bPrompt = body
		}
		count++
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	if count != 2 {
		notFound(w)
		return m, false, false
	}
	m.adapter = s.promptTrialAdapter()
	if m.adapter == nil {
		trialNotConnected(w)
		return m, false, false
	}
	m.model, ok = m.adapter.Binding(a.Env)
	if !ok || !trialBindingValid(m.model) {
		trialNotConnected(w)
		return m, false, false
	}
	if !trialAdmitted(m) {
		httpapi.Error(w, 422, "prompt_trial_budget_exceeded", "Trial input or worst-case cost exceeds its configured limit")
		return m, false, false
	}
	m.trialID = store.ID()
	cmd, err := tx.Exec(r.Context(), `INSERT INTO v3_agent_prompt_trials
 (id,prompt_id,agent_id,application_id,environment_id,case_id,case_input_id,actor_id,request_key,request_sha256,
 a_version,b_version,a_prompt_sha256,b_prompt_sha256,input_sha256,model_provider,model_name,max_output_tokens,max_cost_micro_usd)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
 ON CONFLICT (actor_id,request_key) DO NOTHING`, m.trialID, p.ID, a.ID, a.App, a.Env, c.ID, inputID,
		httpapi.CurrentUser(r).ID, key, requestHash, av, bv, trialDigest(m.aPrompt), trialDigest(m.bPrompt), inputHash,
		m.model.Provider, m.model.Model, m.model.MaxOutputTokens, m.model.MaxCostMicroUSD)
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	if cmd.RowsAffected() != 1 {
		trialConflict(w)
		return m, false, false
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_prompt_trial_runs(trial_id,side,status)
 VALUES($1,'a','pending'),($1,'b','pending')`, m.trialID)
	if err == nil {
		err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.prompt_trial.reserved", m.trialID,
			map[string]any{"prompt_id": p.ID, "case_id": c.ID, "input_revision": inputRevision, "input_sha256": inputHash,
				"a_version": in.A, "b_version": in.B, "model_provider": m.model.Provider, "model_name": m.model.Model,
				"max_cost_micro_usd": m.model.MaxCostMicroUSD})
	}
	if err != nil {
		unavailable(w)
		return m, false, false
	}
	if !recheckPromptSession(w, r, tx) {
		return m, false, false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return m, false, false
	}
	return m, false, true
}

func (s *Service) startTrialRun(w http.ResponseWriter, r *http.Request, envs []string, caseID string,
	m trialMaterial, side string) bool {
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return false
	}
	defer tx.Rollback(r.Context())
	_, a, _, ok := s.trialScope(w, r, tx, envs, caseID)
	if !ok {
		_ = tx.Rollback(r.Context())
		_ = s.failTrial(m, "authority_changed")
		return false
	}
	if a.ID != m.agent.ID || a.App != m.agent.App || a.Env != m.agent.Env {
		notFound(w)
		_ = tx.Rollback(r.Context())
		_ = s.failTrial(m, "scope_changed")
		return false
	}
	cmd, err := tx.Exec(r.Context(), `UPDATE v3_agent_prompt_trial_runs SET status='started',started_at=clock_timestamp()
 WHERE trial_id=$1 AND side=$2 AND status='pending'`, m.trialID, side)
	if err != nil || cmd.RowsAffected() != 1 {
		unavailable(w)
		return false
	}
	if !recheckPromptSession(w, r, tx) {
		return false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return false
	}
	return true
}

func (s *Service) failTrial(m trialMaterial, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	cmd, err := tx.Exec(ctx, `UPDATE v3_agent_prompt_trials SET status='failed',error_code=$2,finished_at=clock_timestamp()
 WHERE id=$1 AND status='running'`, m.trialID, code)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 1 {
		err = audit(ctx, tx, m.agent, "system", "v3.agent.prompt_trial.failed", m.trialID,
			map[string]any{"error_code": code})
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// recordTrialAmbiguous is a best-effort recovery after an outbound call whose
// normal result write did not commit. It saves only factual provider metadata
// and never retries the provider. A later same-key request reconciles a stale
// running row when PG was also unavailable to this recovery write.
func (s *Service) recordTrialAmbiguous(m trialMaterial, side string,
	result PromptTrialModelResult, duration int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM v3_agent_prompt_trials WHERE id=$1 FOR UPDATE`, m.trialID).Scan(&status)
	if err != nil {
		return err
	}
	if status != "running" {
		return tx.Commit(ctx)
	}
	requestID := result.ProviderRequestID
	if len(requestID) > 256 {
		requestID = requestID[:256]
	}
	inputTokens, outputTokens := result.InputTokens, result.OutputTokens
	if inputTokens != nil && *inputTokens < 0 {
		inputTokens = nil
	}
	if outputTokens != nil && (*outputTokens < 0 || *outputTokens > int64(m.model.MaxOutputTokens)) {
		outputTokens = nil
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_prompt_trial_runs
 SET status='failed',provider_request_id=$3,input_tokens=$4,output_tokens=$5,duration_ms=$6,
 error_code='ambiguous_after_provider',finished_at=clock_timestamp()
 WHERE trial_id=$1 AND side=$2 AND status='started'`, m.trialID, side, requestID, inputTokens, outputTokens, duration)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_prompt_trials SET status='failed',
 error_code='ambiguous_after_provider',finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, m.trialID)
	if err == nil {
		err = audit(ctx, tx, m.agent, "system", "v3.agent.prompt_trial.failed", m.trialID,
			map[string]any{"error_code": "ambiguous_after_provider", "side": side,
				"provider_request_id": requestID, "usage_reported": inputTokens != nil && outputTokens != nil})
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) finishTrialRun(m trialMaterial, side string, result PromptTrialModelResult,
	duration int64, runErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status, code := "succeeded", ""
	if runErr != nil {
		status, code = "failed", "provider_error"
		if errors.Is(runErr, ErrPromptTrialOutcomeAmbiguous) {
			code = "ambiguous_after_provider"
		}
	}
	var text string
	requestID := result.ProviderRequestID
	if len(requestID) > 256 {
		requestID = ""
	}
	inputTokens, outputTokens := result.InputTokens, result.OutputTokens
	if inputTokens != nil && (*inputTokens < 0 || *inputTokens > int64(m.model.MaxInputBytes+1024)) {
		inputTokens = nil
	}
	if outputTokens != nil && (*outputTokens < 0 || *outputTokens > int64(m.model.MaxOutputTokens)) {
		outputTokens = nil
	}
	if runErr == nil {
		text = result.Text
	}
	cmd, err := tx.Exec(ctx, `UPDATE v3_agent_prompt_trial_runs
 SET status=$3,output_text=$4,provider_request_id=$5,input_tokens=$6,output_tokens=$7,duration_ms=$8,
 error_code=$9,finished_at=clock_timestamp() WHERE trial_id=$1 AND side=$2 AND status='started'`,
		m.trialID, side, status, text, requestID, inputTokens, outputTokens, duration, code)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() != 1 {
		return errors.New("Prompt trial run state conflict")
	}
	if runErr != nil {
		_, err = tx.Exec(ctx, `UPDATE v3_agent_prompt_trials SET status='failed',error_code=$2,finished_at=clock_timestamp()
 WHERE id=$1 AND status='running'`, m.trialID, code)
	} else if side == "b" {
		_, err = tx.Exec(ctx, `UPDATE v3_agent_prompt_trials SET status='succeeded',finished_at=clock_timestamp()
 WHERE id=$1 AND status='running'`, m.trialID)
	}
	if err == nil {
		err = audit(ctx, tx, m.agent, "system", "v3.agent.prompt_trial.run", m.trialID,
			map[string]any{"side": side, "status": status, "duration_ms": duration, "usage_reported": inputTokens != nil && outputTokens != nil,
				"provider_request_id": requestID, "error_code": code})
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) trialFinalAuthority(w http.ResponseWriter, r *http.Request, envs []string, caseID string) bool {
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return false
	}
	defer tx.Rollback(r.Context())
	_, _, _, ok := s.trialScope(w, r, tx, envs, caseID)
	if !ok {
		return false
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return false
	}
	return true
}

func (s *Service) writeTrialResult(w http.ResponseWriter, r *http.Request, id string) {
	rows, err := s.DB.Query(r.Context(), `SELECT run.side,run.output_text,run.input_tokens,run.output_tokens,
 run.duration_ms,trial.a_version,trial.b_version,trial.model_provider,trial.model_name
 FROM v3_agent_prompt_trial_runs run JOIN v3_agent_prompt_trials trial ON trial.id=run.trial_id
 WHERE trial.id=$1 AND trial.status='succeeded' AND run.status='succeeded' ORDER BY run.side`, id)
	if err != nil {
		unavailable(w)
		return
	}
	items := make([]any, 0, 2)
	for rows.Next() {
		var side, text string
		var inputTokens, outputTokens, duration *int64
		var aVersion, bVersion, version int
		var provider, model string
		if err = rows.Scan(&side, &text, &inputTokens, &outputTokens, &duration,
			&aVersion, &bVersion, &provider, &model); err != nil {
			break
		}
		if side == "a" {
			version = aVersion
		} else {
			version = bVersion
		}
		var tokens, durationValue any
		usageSource := "unknown"
		if inputTokens != nil && outputTokens != nil {
			tokens = strconv.FormatInt(*inputTokens+*outputTokens, 10)
			usageSource = "provider_reported"
		}
		if duration != nil {
			durationValue = strconv.FormatInt(*duration, 10) + " ms"
		}
		items = append(items, map[string]any{"version": promptDisplayVersion(version), "text": text,
			"score": nil, "good": nil, "score_status": "unknown", "tokens": tokens, "duration": durationValue,
			"usage_source": usageSource, "trial_id": id, "model": map[string]string{"provider": provider, "model": model}})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(items) != 2 {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, items)
}
