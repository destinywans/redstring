package v3agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

var errEvalConflict = errors.New("evaluation execution conflict")
var errEvalConfiguration = errors.New("evaluation execution configuration unavailable")
var errEvalNotConnected = errors.New("evaluation runtime not connected")

type evalVersionMaterial struct {
	Version     string          `json:"version"`
	Config      ExecutionConfig `json:"config"`
	Prompt      string          `json:"prompt"`
	ArtifactRef string          `json:"artifact_ref"`
}
type evalExecutionMaterial struct {
	AgentID       string                `json:"agent_id"`
	ApplicationID string                `json:"application_id"`
	EnvironmentID string                `json:"environment_id"`
	CaseID        string                `json:"case_id"`
	InputID       string                `json:"input_id"`
	Input         EvalInput             `json:"case"`
	Versions      []evalVersionMaterial `json:"versions"`
}
type evalSideResult struct {
	Version      string  `json:"version"`
	Model        string  `json:"model"`
	Text         string  `json:"text"`
	DurationMS   int64   `json:"duration_ms"`
	CheckResults []bool  `json:"check_results"`
	Score        float64 `json:"score"`
	UsageSource  string  `json:"usage_source"`
}

func evalCheckResults(text string, checks []string) ([]bool, float64) {
	values := make([]bool, len(checks))
	passed := 0
	for i, c := range checks {
		values[i] = strings.Contains(text, c)
		if values[i] {
			passed++
		}
	}
	if len(checks) == 0 {
		return values, 0
	}
	return values, float64(passed) / float64(len(checks))
}
func evalCompatible(c ExecutionConfig, input string, runner inference.Runner) bool {
	if runner == nil || !c.valid() || len(c.ToolNames) != 0 || len(c.Prompt)+len(input) > c.MaxInputBytes {
		return false
	}
	for _, m := range runner.Models() {
		if m.ID == c.ModelID {
			return true
		}
	}
	return false
}

// evalExecutionScope locks the experiment only after the current actor's
// session and grant sources. Its original application/environment audit must
// still match; an Agent move never transfers private historical results.
func (s *Service) evalExecutionScope(ctx context.Context, tx pgx.Tx, r *http.Request, action string) (agent, evalRecord, error) {
	var agentID string
	if err := tx.QueryRow(ctx, `SELECT agent_id FROM v3_agent_eval_experiments WHERE id=$1`, r.PathValue("id")).Scan(&agentID); err != nil {
		return agent{}, evalRecord{}, err
	}
	a, err := s.evalAuthority(ctx, tx, r, agentID, action)
	if err != nil {
		return a, evalRecord{}, err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM v3_agent_eval_experiments WHERE id=$1 FOR UPDATE`, r.PathValue("id")).Scan(&id); err != nil {
		return a, evalRecord{}, err
	}
	v, err := loadEval(ctx, tx, id)
	if err != nil {
		return a, v, err
	}
	var origin string
	err = tx.QueryRow(ctx, `SELECT changes->>'environment_id' FROM management_audit WHERE application_id=$1 AND action='v3.agent.eval.created' AND target=$2 AND actor_id=$3 AND outcome='recorded' ORDER BY id LIMIT 1`, a.App, id, v.CreatedBy).Scan(&origin)
	if err == nil && origin != a.Env {
		err = store.ErrNotFound
	}
	return a, v, err
}

func (s *Service) ExecuteEval(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	if _, ok := requestScope(w, r, true); !ok {
		return
	}
	var in struct {
		CaseID     string `json:"case_id"`
		RequestKey string `json:"request_key"`
	}
	if !decodePromptVersion(w, r, &in) {
		return
	}
	if !safeCode(in.CaseID, 128) || !safeCode(in.RequestKey, 128) || len(in.RequestKey) < 8 {
		invalid(w, "case_id and a unique 8–128 character request_key are required")
		return
	}
	runner := s.ExecutionRuntime()
	material, replay, err := s.reserveEvalExecution(r, in.CaseID, in.RequestKey, runner)
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	if replay {
		s.EvalExecution(w, r)
		return
	}
	id := r.PathValue("id")
	// All outputs remain in memory until both sides and final authorization
	// succeed. A cancellation, failure or ambiguous completion has no report.
	results := make([]evalSideResult, 0, 2)
	runCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := s.watchEvalExecution(runCtx, cancel, r, material)
	terminalCode := ""
	terminalState := "ambiguous"
	for side, v := range material.Versions {
		if err = s.startEvalSide(r, material, side); err != nil {
			terminalCode = "authority_or_state_changed"
			break
		}
		callCtx, callCancel := context.WithTimeout(runCtx, time.Duration(min(v.Config.TimeoutSeconds, 30))*time.Second)
		var rejected atomic.Bool
		started := time.Now()
		text, callErr := runner.Run(callCtx, inference.Request{RunID: "eval-" + id, AttemptID: fmt.Sprintf("%s-%d", id, side), BranchID: "eval-" + material.CaseID, EnvironmentID: material.EnvironmentID, ModelID: v.Config.ModelID,
			Messages: []inference.Message{{Role: "system", Content: v.Prompt}, {Role: "user", Content: material.Input.Input}}, MaxCalls: 1, MaxToolCalls: 1, MaxOutputTokens: v.Config.MaxOutputTokens, TimeoutSeconds: min(v.Config.TimeoutSeconds, 30)}, func(e inference.Event) error {
			if e.Invocation != nil || e.Evidence != nil || e.Type == "tool_invocation" || e.Type == "evidence" {
				rejected.Store(true)
				return errEvalConfiguration
			}
			return callCtx.Err()
		})
		contextErr := callCtx.Err()
		callCancel()
		if callErr != nil || contextErr != nil || rejected.Load() || strings.TrimSpace(text) == "" || len(text) > min(v.Config.MaxOutputBytes, 16384) || !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			terminalCode = "provider_outcome_unconfirmed"
			break
		}
		checks, score := evalCheckResults(text, material.Input.Checks)
		results = append(results, evalSideResult{Version: v.Version, Model: v.Config.ModelID, Text: text, DurationMS: time.Since(started).Milliseconds(), CheckResults: checks, Score: score, UsageSource: "unknown"})
	}
	stop()
	if terminalCode == "" && runCtx.Err() != nil {
		terminalCode = "cancelled_or_authority_changed"
	}
	if terminalCode != "" {
		_ = s.terminateEvalExecution(id, terminalState, terminalCode)
		httpapi.Error(w, 502, "eval_execution_incomplete", "Model execution did not produce a confirmed authorized two-version result; inspect the saved execution")
		return
	}
	if err = s.completeEvalExecution(r, material, results); err != nil {
		_ = s.terminateEvalExecution(id, "ambiguous", "result_commit_unconfirmed")
		evalHTTPError(w, err)
		return
	}
	s.EvalExecution(w, r)
}

func (s *Service) reserveEvalExecution(r *http.Request, caseID, key string, runner inference.Runner) (evalExecutionMaterial, bool, error) {
	var m evalExecutionMaterial
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return m, false, err
	}
	defer tx.Rollback(ctx)
	a, v, err := s.evalExecutionScope(ctx, tx, r, "change")
	if err != nil {
		return m, false, err
	}
	var oldCase, oldKey, oldActor string
	err = tx.QueryRow(ctx, `SELECT case_id,request_key,actor_id FROM v3_agent_eval_executions WHERE experiment_id=$1`, v.ID).Scan(&oldCase, &oldKey, &oldActor)
	if err == nil {
		if oldCase != caseID || oldKey != key || oldActor != httpapi.CurrentUser(r).ID {
			return m, false, errEvalConflict
		}
		return m, true, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, false, err
	}
	if v.Status != "pending" {
		return m, false, errEvalConflict
	}
	if runner == nil {
		return m, false, errEvalNotConnected
	}
	c, err := loadCaseTx(ctx, tx, caseID, "FOR SHARE")
	if err != nil {
		return m, false, err
	}
	if c.AgentID != a.ID || c.OriginApp != a.App || c.OriginEnv != a.Env {
		return m, false, store.ErrNotFound
	}
	in, inputID, err := loadEvalInput(ctx, tx, caseID)
	if err != nil {
		return m, false, err
	}
	m = evalExecutionMaterial{AgentID: a.ID, ApplicationID: a.App, EnvironmentID: a.Env, CaseID: caseID, InputID: inputID, Input: in, Versions: []evalVersionMaterial{}}
	for _, version := range []string{v.Baseline, v.Candidate} {
		config, e := LoadExecutionConfigTx(ctx, tx, a.ID, version)
		if e != nil {
			return m, false, errEvalConfiguration
		}
		if !evalCompatible(config, in.Input, runner) {
			return m, false, errEvalConfiguration
		}
		m.Versions = append(m.Versions, evalVersionMaterial{Version: version, Config: config, Prompt: config.Prompt, ArtifactRef: config.ArtifactRef})
	}
	raw, hash, err := evalJSONHash(m)
	if err != nil {
		return m, false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_executions(experiment_id,case_id,actor_id,application_id,environment_id,request_key,material,material_sha256,status,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'running',clock_timestamp()+interval '70 seconds')`, v.ID, caseID, httpapi.CurrentUser(r).ID, a.App, a.Env, key, raw, hash)
	if err != nil {
		return m, false, err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status='running' WHERE id=$1`, v.ID)
	if err == nil {
		err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.execution.started", v.ID, map[string]any{"environment_id": a.Env, "case_id": caseID, "material_sha256": hash, "evaluation_kind": "single_case_literal_checks", "baseline_config_sha256": m.Versions[0].Config.SHA256, "candidate_config_sha256": m.Versions[1].Config.SHA256})
	}
	if err == nil {
		err = evalSessionStillActive(ctx, tx, r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	return m, false, err
}

func (s *Service) startEvalSide(r *http.Request, m evalExecutionMaterial, side int) error {
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, v, err := s.evalExecutionScope(ctx, tx, r, "change")
	if err != nil {
		return err
	}
	if a.ID != m.AgentID || a.App != m.ApplicationID || a.Env != m.EnvironmentID {
		return store.ErrNotFound
	}
	tag, err := tx.Exec(ctx, `UPDATE v3_agent_eval_executions SET started_sides=started_sides+1 WHERE experiment_id=$1 AND status='running' AND deadline>clock_timestamp() AND started_sides=$2`, v.ID, side)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errEvalConflict
	}
	if err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.call.started", v.ID, map[string]any{"side": side, "version": m.Versions[side].Version, "model_id": m.Versions[side].Config.ModelID}); err != nil {
		return err
	}
	if err = evalSessionStillActive(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) watchEvalExecution(ctx context.Context, cancel context.CancelFunc, r *http.Request, m evalExecutionMaterial) func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-tick.C:
				checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				tx, err := s.DB.Begin(checkCtx)
				if err == nil {
					a, _, e := s.evalExecutionScope(checkCtx, tx, r.WithContext(checkCtx), "change")
					err = e
					if err == nil && (a.App != m.ApplicationID || a.Env != m.EnvironmentID) {
						err = store.ErrNotFound
					}
					var active bool
					if err == nil {
						err = tx.QueryRow(checkCtx, `SELECT status='running' AND deadline>clock_timestamp() FROM v3_agent_eval_executions WHERE experiment_id=$1`, r.PathValue("id")).Scan(&active)
						if !active && err == nil {
							err = errEvalConflict
						}
					}
					_ = tx.Rollback(checkCtx)
				}
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done); <-stopped }
}
