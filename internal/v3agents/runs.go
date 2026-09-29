package v3agents

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// run is the persisted producer report. It contains no prompt, model message,
// tool arguments, or retrieved content.
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
	// Completion follows the run row lock; transaction-start now() could precede
	// a guardrail or span recorded while this transaction waited for that lock.
	_, err = tx.Exec(ctx, `UPDATE v3_agent_runs SET status=$2,error_code=NULLIF($3,''),
 token_input=$4,token_output=$5,finished_at=clock_timestamp() WHERE id=$1`, in.ID, in.Status, in.ErrorCode, in.InputTokens, in.OutputTokens)
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
