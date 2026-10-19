package v3agents

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type ExecutionToolsFactory func(context.Context, string, string, string, string) ([]inference.EvidenceTool, error)
type executionDependencies struct {
	runner inference.Runner
	tools  ExecutionToolsFactory
}

var executionAdapters sync.Map

func (s *Service) SetExecutionRuntime(runner inference.Runner, factory ExecutionToolsFactory) {
	if runner == nil {
		executionAdapters.Delete(s)
		return
	}
	executionAdapters.Store(s, executionDependencies{runner, factory})
}
func (s *Service) ExecutionRuntime() inference.Runner {
	if d, ok := executionAdapters.Load(s); ok {
		return d.(executionDependencies).runner
	}
	return nil
}
func (s *Service) RegisterExecution(api *httpapi.Server) {
	api.Register("POST /api/v1/guardrails", "viewer", s.RegisterExecutionGuardrail)
	api.MarkV3ObjectRoute("POST /api/v1/guardrails", "agents", "manage")
	api.Register("GET /api/v1/agent-runs/{id}/result", "viewer", s.ExecutionResult)
	api.MarkV3ObjectRoute("GET /api/v1/agent-runs/{id}/result", "agents", "view")
	api.Register("GET /api/v1/agents/environment-limit", "viewer", s.EnvironmentExecutionLimit)
	api.MarkV3ObjectRoute("GET /api/v1/agents/environment-limit", "agents", "manage")
	api.Register("PUT /api/v1/agents/environment-limit", "viewer", s.EnvironmentExecutionLimit)
	api.MarkV3ObjectRoute("PUT /api/v1/agents/environment-limit", "agents", "manage")
	api.Register("PUT /api/v1/agents/{id}/versions/{version}/execution", "viewer", s.ConfigureExecution)
	api.MarkV3ObjectRoute("PUT /api/v1/agents/{id}/versions/{version}/execution", "agents", "manage")
	api.Register("POST /api/v1/agents/{id}/runs", "viewer", s.ExecuteAgent)
	api.MarkV3ObjectRoute("POST /api/v1/agents/{id}/runs", "agents", "change")
}
func executionToolAllowed(name string) bool {
	switch name {
	case "read_service_profile", "read_service_metrics", "read_service_logs", "search_knowledge":
		return true
	}
	return false
}
func executionVersion(a agent, runID string) string {
	if a.Canary != "" && a.Percent > 0 {
		h := sha256.Sum256([]byte(a.ID + "\x00" + runID))
		if binary.BigEndian.Uint64(h[:8])%100 < uint64(a.Percent) {
			return a.Canary
		}
	}
	return a.Stable
}

type executionPolicy struct {
	ID, Name string
	Revision int64
	Enabled  bool
}
type executionGovernance struct {
	Level, Cap string
	Policies   []executionPolicy
	Tools      []string
}

func loadExecutionGovernance(ctx context.Context, tx pgx.Tx, a lifecycleAgent, c ExecutionConfig) (executionGovernance, string, error) {
	g := executionGovernance{Level: a.Level, Policies: []executionPolicy{}, Tools: []string{}}
	// Table locks cover absent limits, newly enabled policies and newly inserted policies.
	_, err := tx.Exec(ctx, `LOCK TABLE v3_agent_environment_limits,v3_agent_guard_policies IN SHARE MODE`)
	if err != nil {
		return g, "", err
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT max_level FROM v3_agent_environment_limits WHERE environment_id=$1),'L0')`, a.Env).Scan(&g.Cap)
	if err != nil {
		return g, "", err
	}
	rows, err := tx.Query(ctx, `SELECT id,name,revision,enabled FROM v3_agent_guard_policies WHERE environment_id=$1 ORDER BY id LIMIT 65 FOR SHARE`, a.Env)
	if err != nil {
		return g, "", err
	}
	for rows.Next() {
		var p executionPolicy
		if err = rows.Scan(&p.ID, &p.Name, &p.Revision, &p.Enabled); err != nil {
			break
		}
		g.Policies = append(g.Policies, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return g, "", err
	}
	if len(g.Policies) > 64 {
		return g, "", errors.New("policy scope too large")
	}
	g.Tools = append(g.Tools, c.ToolNames...)
	b, _ := json.Marshal(g)
	return g, fmt.Sprintf("%x", sha256.Sum256(b)), err
}
func (g executionGovernance) blocked(c ExecutionConfig) (code string, policy *executionPolicy) {
	if levelNumber(g.Level) < 0 || levelNumber(g.Level) > levelNumber(g.Cap) {
		return "agent_level_cap", nil
	}
	if len(c.ToolNames) > 0 && g.Level == "L0" {
		return "agent_level_tools", nil
	}
	have := map[string]bool{}
	for _, n := range g.Tools {
		have[n] = true
	}
	for _, n := range c.ToolNames {
		if !have[n] {
			return "tool_not_enabled", nil
		}
	}
	for _, p := range g.Policies {
		if !p.Enabled {
			continue
		}
		switch p.Name {
		case "read_only", "bounded_execution", "deny_sensitive_content":
		default:
			return "unsupported_guard_policy", &p
		}
	}
	return "", nil
}
func (g executionGovernance) sensitivePolicy(text string) *executionPolicy {
	lower := strings.ToLower(text)
	// Deterministic, intentionally narrow credential patterns; this is not a
	// claim of comprehensive semantic content safety or secret detection.
	sensitive := strings.Contains(lower, "-----begin private key-----") || strings.Contains(lower, "authorization: bearer ") || strings.Contains(lower, "password=")
	if sensitive {
		for _, p := range g.Policies {
			if p.Enabled && p.Name == "deny_sensitive_content" {
				return &p
			}
		}
	}
	return nil
}

var errExecutionDrift = errors.New("execution authority or configuration changed")

func (s *Service) executionCheck(ctx context.Context, r *http.Request, a lifecycleAgent, c ExecutionConfig, hash string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = s.executionCheckTx(ctx, tx, r, a, c, hash)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Service) executionCheckTx(ctx context.Context, tx pgx.Tx, r *http.Request, a lifecycleAgent, c ExecutionConfig, hash string) error {
	current, err := loadLifecycleAgent(ctx, tx, a.ID, false)
	if err != nil {
		return err
	}
	if current.Revision != a.Revision || current.Paused || current.App != a.App || current.Env != a.Env {
		return errExecutionDrift
	}
	if err = lifecycleSession(ctx, tx, r); err != nil {
		return err
	}
	if err = lockEvalGrantSources(ctx, tx, httpapi.CurrentUser(r).ID); err != nil {
		return err
	}
	if err = s.authorizeLifecycle(ctx, tx, r, current, "view"); err != nil {
		return err
	}
	if err = s.authorizeLifecycle(ctx, tx, r, current, "change"); err != nil {
		return err
	}
	g, h, err := loadExecutionGovernance(ctx, tx, current, c)
	if err != nil {
		return err
	}
	if h != hash {
		return errExecutionDrift
	}
	if code, _ := g.blocked(c); code != "" {
		return errExecutionDrift
	}
	return lifecycleSession(ctx, tx, r)
}

// RecoverExpiredExecutions marks only governed requests whose hard deadline has
// elapsed. It is safe at startup and before new requests; it never retries an
// external model/tool call or invents a successful outcome after a crash.
func (s *Service) RecoverExpiredExecutions(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT r.id,r.agent_id FROM v3_agent_runs r JOIN v3_agent_execution_snapshots x ON x.run_id=r.id
 WHERE r.status='running' AND x.deadline<clock_timestamp() ORDER BY r.id LIMIT 100 FOR UPDATE OF r SKIP LOCKED`)
	if err != nil {
		return err
	}
	type item struct{ id, agent string }
	items := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.agent); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		a, e := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, v.agent))
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, `UPDATE v3_agent_runs SET status='failed',error_code='execution_expired',finished_at=clock_timestamp() WHERE id=$1`, v.id); err != nil {
			return err
		}
		if err = audit(ctx, tx, a, "system", "v3.agent.run.completed", v.id, map[string]any{"environment_id": a.Env, "status": "failed", "error_code": "execution_expired"}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type executionBody struct {
	Input string `json:"input"`
}

func (s *Service) ExecuteAgent(w http.ResponseWriter, r *http.Request) {
	var body executionBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Input) == "" || len(body.Input) > 8192 || !utf8.ValidString(body.Input) || strings.ContainsRune(body.Input, '\x00') {
		invalid(w, "Bounded UTF-8 input required")
		return
	}
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	dep, connected := executionAdapters.Load(s)
	if !connected {
		httpapi.Error(w, 409, "not_connected", "Agent execution runtime is not connected")
		return
	}
	deps := dep.(executionDependencies)
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	if err := s.RecoverExpiredExecutions(r.Context()); err != nil {
		unavailable(w)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	a, err := loadLifecycleAgent(r.Context(), tx, r.PathValue("id"), true)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && a.Env != envs[0]) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = lifecycleSession(r.Context(), tx, r); err == nil {
		err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID)
	}
	if err == nil {
		err = s.authorizeLifecycle(r.Context(), tx, r, a, "view")
	}
	if errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	}
	if err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if err = s.authorizeLifecycle(r.Context(), tx, r, a, "change"); err != nil {
		lifecycleAuthorityError(w, err)
		return
	}
	if a.Paused || a.Stable == "" {
		httpapi.Error(w, 409, "agent_paused", "Agent has no active stable release")
		return
	}
	id := store.ID()
	version := executionVersion(a.agent, id)
	c, err := LoadExecutionConfigTx(r.Context(), tx, a.ID, version)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 409, "not_connected", "Selected Agent version has no executable configuration")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if len(c.Prompt)+len(body.Input) > c.MaxInputBytes {
		invalid(w, "Prompt and input exceed version budget")
		return
	}
	found := false
	for _, m := range deps.runner.Models() {
		if m.ID == c.ModelID && (len(c.ToolNames) == 0 || m.Tools) {
			found = true
		}
	}
	if !found {
		httpapi.Error(w, 409, "not_connected", "Configured model is not available for this Agent")
		return
	}
	g, hash, err := loadExecutionGovernance(r.Context(), tx, a, c)
	if err != nil {
		unavailable(w)
		return
	}
	var busy bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_agent_runs r JOIN v3_agent_execution_snapshots x ON x.run_id=r.id WHERE r.agent_id=$1 AND x.actor_id=$2 AND r.status='running')`, a.ID, httpapi.CurrentUser(r).ID).Scan(&busy)
	if err != nil {
		unavailable(w)
		return
	}
	if busy {
		httpapi.Error(w, 409, "execution_in_progress", "A governed run is already active for this actor and Agent")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_runs(id,agent_id,version,application_id,environment_id,trigger_kind,status) VALUES($1,$2,$3,$4,$5,'manual','running')`, id, a.ID, version, a.App, a.Env)
	deadline := time.Now().Add(time.Duration(c.TimeoutSeconds) * time.Second)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_execution_snapshots(run_id,actor_id,lifecycle_revision,config_sha256,model_id,prompt_id,prompt_version,governance_sha256,deadline) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, httpapi.CurrentUser(r).ID, a.Revision, c.SHA256, c.ModelID, c.PromptID, c.PromptVersion, hash, deadline)
	}
	if err == nil {
		err = audit(r.Context(), tx, a.agent, httpapi.CurrentUser(r).ID, "v3.agent.run.started", id, map[string]any{"environment_id": a.Env, "version": version, "config_sha256": c.SHA256, "lifecycle_revision": a.Revision, "model_id": c.ModelID, "prompt_id": c.PromptID, "prompt_version": c.PromptVersion})
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !s.finishLifecycleWrite(w, r, tx) {
		return
	}
	state := &governedExecution{s: s, r: r, a: a, c: c, g: g, hash: hash, id: id, version: version, steps: map[string]bool{}}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	state.r = r.WithContext(ctx)
	status, code := "success", ""
	answer := ""
	if blocked, p := g.blocked(c); blocked != "" {
		status, code = "blocked", blocked
		err = state.guard(ctx, p, "input", code)
	} else if p := g.sensitivePolicy(c.Prompt + "\n" + body.Input); p != nil {
		status, code = "blocked", "sensitive_input"
		err = state.guard(ctx, p, "input", code)
	} else {
		var tools []inference.EvidenceTool
		tools, err = state.tools(ctx, deps.tools)
		if err == nil {
			answer, err = deps.runner.Run(ctx, inference.Request{RunID: id, AttemptID: id, BranchID: id, EnvironmentID: a.Env, ModelID: c.ModelID,
				Messages: []inference.Message{{Role: "system", Content: c.Prompt}, {Role: "user", Content: body.Input}}, Tools: tools, MaxCalls: c.MaxModelCalls, MaxToolCalls: c.MaxToolCalls, MaxOutputTokens: c.MaxOutputTokens, TimeoutSeconds: c.TimeoutSeconds, Deadline: deadline}, state.emit)
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil && state.failure != nil {
			err = state.failure
		}
		if err == nil && (len(answer) > c.MaxOutputBytes || !utf8.ValidString(answer) || strings.ContainsRune(answer, '\x00')) {
			status, code = "blocked", "output_budget"
		}
		if err == nil && code == "" {
			if p := g.sensitivePolicy(answer); p != nil {
				status, code = "blocked", "sensitive_output"
				err = state.guard(ctx, p, "output", code)
			}
		}
	}
	if err != nil {
		status, code = "failed", "execution_failed"
		if errors.Is(err, errExecutionDrift) || errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrSessionRevoked) {
			status, code = "blocked", "execution_authority_changed"
		}
		if ctx.Err() != nil {
			code = "execution_expired"
		}
	}
	if status != "success" {
		answer = ""
	}
	// Client disconnect and timeout must not prevent terminal metadata persistence.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if err = state.finish(finishCtx, status, code, answer); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			answer = ""
			status, code = "failed", "execution_expired"
			err = state.finish(finishCtx, status, code, answer)
		}
		if errors.Is(err, errExecutionDrift) || errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrSessionRevoked) {
			answer = ""
			status, code = "blocked", "execution_authority_changed"
			err = state.finish(finishCtx, status, code, answer)
		}
		if err != nil {
			unavailable(w)
			return
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": id, "agent_id": a.ID, "version": version, "model_id": c.ModelID, "prompt_id": c.PromptID, "prompt_version": c.PromptVersion, "config_sha256": c.SHA256, "lifecycle_revision": a.Revision, "status": status, "error_code": code, "output": answer, "read_only": true})
}
