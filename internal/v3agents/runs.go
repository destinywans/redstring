package v3agents

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
)

type run struct {
	ID, AgentID, Version, App, Env, Trigger, Status, ErrorCode string
	Started                                                    time.Time
	Finished                                                   *time.Time
	Input, Output                                              *int
}

const runColumns = `v.id,v.agent_id,v.version,v.application_id,v.environment_id,v.trigger_kind,v.status,
 COALESCE(v.error_code,''),v.started_at,v.finished_at,v.token_input,v.token_output`

func scanRun(row pgx.Row) (run, error) {
	var v run
	err := row.Scan(&v.ID, &v.AgentID, &v.Version, &v.App, &v.Env, &v.Trigger, &v.Status, &v.ErrorCode,
		&v.Started, &v.Finished, &v.Input, &v.Output)
	return v, err
}

func (s *Service) loadRun(ctx context.Context, id string) (run, error) {
	return scanRun(s.DB.QueryRow(ctx, `SELECT `+runColumns+` FROM v3_agent_runs v WHERE v.id=$1`, id))
}

func (s *Service) runAgent(w http.ResponseWriter, r *http.Request, id, action string) (run, agent, bool) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return run{}, agent{}, false
	}
	v, err := s.loadRun(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return run{}, agent{}, false
	}
	if err != nil {
		unavailable(w)
		return run{}, agent{}, false
	}
	a, ok := s.objectAgent(w, r, v.AgentID, action)
	if !ok {
		return run{}, agent{}, false
	}
	if a.App != v.App || a.Env != v.Env {
		unavailable(w)
		return run{}, agent{}, false
	}
	return v, a, true
}

func runDuration(v run) any {
	if v.Finished == nil {
		return nil
	}
	return v.Finished.Sub(v.Started).Milliseconds()
}
func runTokens(v run) any {
	if v.Input == nil || v.Output == nil {
		return unknown
	}
	return *v.Input + *v.Output
}

func (s *Service) Runs(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "failed" && filter != "blocked" && filter != "negative" {
		invalid(w, "Unknown run filter")
		return
	}
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	rows, err := s.DB.Query(r.Context(), `SELECT `+runColumns+` FROM v3_agent_runs v
 JOIN v3_agents a ON a.id=v.agent_id AND a.application_id=v.application_id
 AND a.environment_id=v.environment_id
 WHERE v.environment_id=ANY($1) AND ($3='all' OR v.status=$3 OR
 ($3='negative' AND EXISTS (SELECT 1 FROM v3_agent_run_feedback f WHERE f.run_id=v.id AND f.vote='down')))
	AND `+visibleAgentGrantSQL+`
 ORDER BY v.started_at DESC,v.id DESC LIMIT 201`, envs, httpapi.CurrentUser(r).ID, filter)
	if err != nil {
		unavailable(w)
		return
	}
	runs := make([]run, 0)
	for rows.Next() {
		v, err := scanRun(rows)
		if err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		runs = append(runs, v)
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	items := make([]any, 0)
	for i, v := range runs {
		if i == 200 {
			break
		}
		a, err := s.loadAgent(r.Context(), v.AgentID)
		if err != nil {
			unavailable(w)
			return
		}
		if !contains(envs, a.Env) || a.App != v.App || a.Env != v.Env {
			continue
		}
		items = append(items, map[string]any{"id": v.ID, "agent": a.Name, "version": v.Version,
			"trigger": v.Trigger, "scope": v.Env + " / " + a.Name, "status": v.Status,
			"steps": unknown, "tokens": runTokens(v), "duration_ms": runDuration(v),
			"started_at": timeString(v.Started), "feedback": nil})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"kpis": []any{}, "trend": nil, "durations": nil,
		"items": items, "counts": nil, "partial": true, "truncated": len(runs) > 200,
		"missing": []string{"full_run_aggregation", "token_breakdown", "step_count_without_span_producer"}})
}

type span struct {
	ID, Parent, Kind, Name, Status, ModelID, ToolID string
	Start                                           time.Time
	Duration                                        *int
	Input, Output                                   *int
}

func (s *Service) spans(ctx context.Context, runID string) ([]span, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,COALESCE(parent_id,''),kind,name,status,
 COALESCE(model_id,''),COALESCE(tool_id,''),started_at,duration_ms,token_input,token_output
 FROM v3_agent_run_spans WHERE run_id=$1 ORDER BY started_at,id LIMIT 500`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]span, 0)
	for rows.Next() {
		var v span
		if err = rows.Scan(&v.ID, &v.Parent, &v.Kind, &v.Name, &v.Status, &v.ModelID, &v.ToolID,
			&v.Start, &v.Duration, &v.Input, &v.Output); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Service) RunDetail(w http.ResponseWriter, r *http.Request) {
	v, a, ok := s.runAgent(w, r, r.PathValue("id"), "view")
	if !ok {
		return
	}
	spans, err := s.spans(r.Context(), v.ID)
	if err != nil {
		unavailable(w)
		return
	}
	steps := make([]any, 0, len(spans))
	for _, sp := range spans {
		if sp.Kind != "llm" && sp.Kind != "tool" {
			continue
		}
		kind := "tool"
		if sp.Kind == "llm" {
			kind = "model"
		}
		steps = append(steps, map[string]any{"id": sp.ID, "icon": sp.Kind, "name": sp.Name,
			"start": sp.Start.Sub(v.Started).Milliseconds(), "dur": sp.Duration, "meta": sp.Status,
			"kind": kind, "connector": sp.ToolID})
	}
	stats := []any{map[string]string{"k": "status", "v": v.Status}}
	if v.Input != nil {
		stats = append(stats, map[string]string{"k": "input_tokens", "v": strconv.Itoa(*v.Input)})
	}
	if v.Output != nil {
		stats = append(stats, map[string]string{"k": "output_tokens", "v": strconv.Itoa(*v.Output)})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "chain": []any{
		map[string]string{"label": "Agent", "value": a.Name, "icon": "bot"},
		map[string]string{"label": "Version", "value": v.Version, "icon": "version"}},
		"stats": stats, "steps": steps, "total": runDuration(v), "context": nil, "budget": unknown,
		"isolated": []any{}, "memory": nil, "partial": true,
		"missing": []string{"context_composition", "memory_hits", "tool_payloads"}})
}

func (s *Service) RunTrace(w http.ResponseWriter, r *http.Request) {
	v, a, ok := s.runAgent(w, r, r.PathValue("id"), "view")
	if !ok {
		return
	}
	spans, err := s.spans(r.Context(), v.ID)
	if err != nil {
		unavailable(w)
		return
	}
	spanByID := make(map[string]span, len(spans))
	for _, sp := range spans {
		spanByID[sp.ID] = sp
	}
	depth := map[string]int{}
	var spanDepth func(string, int) int
	spanDepth = func(id string, limit int) int {
		if d, ok := depth[id]; ok {
			return d
		}
		sp, ok := spanByID[id]
		if !ok || sp.Parent == "" || limit > len(spans) {
			return 0
		}
		d := spanDepth(sp.Parent, limit+1) + 1
		depth[id] = d
		return d
	}
	items := make([]any, 0, len(spans))
	for _, sp := range spans {
		d := spanDepth(sp.ID, 0)
		items = append(items, map[string]any{"id": sp.ID, "depth": d, "kind": sp.Kind, "name": sp.Name,
			"meta": sp.Status, "start": sp.Start.Sub(v.Started).Milliseconds(), "dur": sp.Duration,
			"parent_id": sp.Parent, "model_id": sp.ModelID, "tool_id": sp.ToolID,
			"token_input": sp.Input, "token_output": sp.Output})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "agent": a.Name, "status": v.Status,
		"stats": []any{}, "scores": []any{}, "session": "", "turns": []any{},
		"total": runDuration(v), "spans": items, "partial": true,
		"missing": []string{"session_turns", "llm_messages", "tool_arguments", "tool_results", "scoring"}})
}

func (s *Service) RunFeedback(w http.ResponseWriter, r *http.Request) {
	v, a, ok := s.runAgent(w, r, r.PathValue("id"), "change")
	if !ok {
		return
	}
	var body struct {
		Vote string `json:"vote"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if body.Vote != "up" && body.Vote != "down" {
		invalid(w, "Vote must be up or down")
		return
	}
	if v.Status == "running" {
		httpapi.Error(w, 409, "run_active", "Feedback requires a completed run")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM v3_agent_runs WHERE id=$1 FOR UPDATE`, v.ID).Scan(&status)
	if err != nil {
		unavailable(w)
		return
	}
	if status == "running" {
		httpapi.Error(w, 409, "run_active", "Feedback requires a completed run")
		return
	}
	if !s.authorizedTx(w, r, tx, a, "change") {
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_run_feedback(run_id,actor_id,vote)
 VALUES($1,$2,$3) ON CONFLICT (run_id,actor_id) DO UPDATE SET vote=EXCLUDED.vote,created_at=now()`,
		v.ID, httpapi.CurrentUser(r).ID, body.Vote)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.run.feedback", v.ID,
		map[string]any{"environment_id": a.Env, "vote": body.Vote})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"vote": body.Vote})
}

// StartRun is a trusted producer hook. It derives scope from persisted Agent
// catalog state and stores no model input, output, or tool arguments.
func (s *Service) StartRun(ctx context.Context, id, agentID, version, trigger string) error {
	if s == nil || s.DB == nil || !bounded(id, 128) || !bounded(agentID, 128) || !bounded(version, 80) ||
		(trigger != "manual" && trigger != "alert" && trigger != "scheduled" && trigger != "api") {
		return errors.New("invalid run metadata")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, agentID))
	if err != nil {
		return err
	}
	if a.Paused || (version != a.Stable && version != a.Canary) {
		return errors.New("agent version is not active")
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_runs
 (id,agent_id,version,application_id,environment_id,trigger_kind,status)
 VALUES($1,$2,$3,$4,$5,$6,'running')`, id, a.ID, version, a.App, a.Env, trigger)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.run.started", id,
		map[string]any{"environment_id": a.Env, "version": version, "trigger": trigger}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type RunCompletion struct {
	ID, Status, ErrorCode     string
	InputTokens, OutputTokens *int
}

func (s *Service) CompleteRun(ctx context.Context, in RunCompletion) error {
	if s == nil || s.DB == nil || !bounded(in.ID, 128) ||
		(in.Status != "success" && in.Status != "failed" && in.Status != "blocked") ||
		(in.ErrorCode != "" && !safeAuditCode(in.ErrorCode, 80)) ||
		(in.InputTokens != nil && *in.InputTokens < 0) || (in.OutputTokens != nil && *in.OutputTokens < 0) {
		return errors.New("invalid run completion")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	v, err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM v3_agent_runs v WHERE v.id=$1 FOR UPDATE`, in.ID))
	if err != nil {
		return err
	}
	if v.Status != "running" {
		return errors.New("run already completed")
	}
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, v.AgentID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_runs SET status=$2,error_code=NULLIF($3,''),
 token_input=$4,token_output=$5,finished_at=now() WHERE id=$1`, in.ID, in.Status, in.ErrorCode, in.InputTokens, in.OutputTokens)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.run.completed", in.ID,
		map[string]any{"environment_id": a.Env, "status": in.Status, "error_code": in.ErrorCode}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type SpanEvent struct {
	ID, RunID, ParentID, Kind, Name, Status, ModelID, ToolID string
	Started                                                  time.Time
	DurationMS                                               *int
	InputTokens, OutputTokens                                *int
}

func (s *Service) RecordSpan(ctx context.Context, in SpanEvent) error {
	if s == nil || s.DB == nil || !bounded(in.ID, 128) || !bounded(in.RunID, 128) || !safeCode(in.Name, 160) ||
		(in.ParentID != "" && !safeCode(in.ParentID, 128)) || (in.ModelID != "" && !safeCode(in.ModelID, 160)) ||
		(in.ToolID != "" && !safeAuditCode(in.ToolID, 160)) || in.Started.IsZero() ||
		(in.DurationMS != nil && *in.DurationMS < 0) || (in.InputTokens != nil && *in.InputTokens < 0) ||
		(in.OutputTokens != nil && *in.OutputTokens < 0) {
		return errors.New("invalid span metadata")
	}
	switch in.Kind {
	case "agent", "chain", "llm", "tool", "retriever", "guardrail", "output":
	default:
		return errors.New("invalid span kind")
	}
	switch in.Status {
	case "ok", "failed", "blocked", "unknown":
	default:
		return errors.New("invalid span status")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var agentID string
	err = tx.QueryRow(ctx, `SELECT agent_id FROM v3_agent_runs WHERE id=$1 AND status='running' FOR UPDATE`, in.RunID).Scan(&agentID)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO v3_agent_run_spans
 (id,run_id,parent_id,kind,name,started_at,duration_ms,status,model_id,tool_id,token_input,token_output)
 SELECT $1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),$11,$12
 FROM v3_agent_runs WHERE id=$2 AND status='running'`, in.ID, in.RunID, in.ParentID, in.Kind, in.Name,
		in.Started, in.DurationMS, in.Status, in.ModelID, in.ToolID, in.InputTokens, in.OutputTokens)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("run no longer active")
	}
	if in.Kind == "tool" {
		a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, agentID))
		if err != nil {
			return err
		}
		if err = audit(ctx, tx, a, "system", "v3.agent.tool.called", in.ID,
			map[string]any{"environment_id": a.Env, "run_id": in.RunID, "tool_id": in.ToolID, "status": in.Status}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
