package v3agents

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
)

type governedExecution struct {
	mu                         sync.Mutex
	s                          *Service
	r                          *http.Request
	a                          lifecycleAgent
	c                          ExecutionConfig
	g                          executionGovernance
	hash, id, version          string
	calls, models, outputBytes int
	steps                      map[string]bool
	failure                    error
}

func (x *governedExecution) guard(ctx context.Context, p *executionPolicy, kind, code string) error {
	if p == nil {
		return nil
	}
	return x.s.ReportGuardDecision(ctx, GuardEvent{ID: store.ID(), RunID: x.id, PolicyID: p.ID, PolicyRevision: p.Revision, Action: "blocked", TargetKind: kind, ReasonCode: code})
}
func (x *governedExecution) fail(err error) error {
	if x.failure == nil {
		x.failure = err
	}
	return err
}
func (x *governedExecution) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := x.s.executionCheck(ctx, x.r, x.a, x.c, x.hash); err != nil {
		return err
	}
	// Verify the immutable version source on each boundary too. Direct database
	// corruption is not interpreted as a new executable version.
	tx, err := x.s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := LoadExecutionConfigTx(ctx, tx, x.a.ID, x.version)
	if err != nil {
		return err
	}
	if c.SHA256 != x.c.SHA256 {
		return errExecutionDrift
	}
	return tx.Commit(ctx)
}
func (x *governedExecution) tools(ctx context.Context, f ExecutionToolsFactory) ([]inference.EvidenceTool, error) {
	if len(x.c.ToolNames) == 0 {
		return nil, nil
	}
	if f == nil {
		return nil, errors.New("read tool adapter unavailable")
	}
	candidates, err := f(ctx, httpapi.CurrentUser(x.r).ID, x.a.App, x.a.Env, x.c.ModelID)
	if err != nil {
		return nil, err
	}
	byName := map[string]inference.EvidenceTool{}
	for _, t := range candidates {
		if executionToolAllowed(t.Name) && t.Read != nil {
			if _, ok := byName[t.Name]; ok {
				return nil, errors.New("duplicate read tool adapter")
			}
			byName[t.Name] = t
		}
	}
	out := make([]inference.EvidenceTool, 0, len(x.c.ToolNames))
	for _, name := range x.c.ToolNames {
		t, ok := byName[name]
		if !ok {
			return nil, errors.New("configured read tool unavailable")
		}
		read := t.Read
		t.Read = func(ctx context.Context, query string) (inference.Evidence, error) {
			x.mu.Lock()
			defer x.mu.Unlock()
			if x.failure != nil {
				return inference.Evidence{}, x.failure
			}
			if x.calls >= x.c.MaxToolCalls {
				return inference.Evidence{}, x.fail(errors.New("tool budget exhausted"))
			}
			x.calls++
			if err := x.check(ctx); err != nil {
				return inference.Evidence{}, x.fail(err)
			}
			spanID := store.ID()
			started := time.Now()
			if err := x.toolAudit(ctx, spanID, name, "started"); err != nil {
				return inference.Evidence{}, x.fail(err)
			}
			ev, readErr := read(ctx, query)
			if readErr == nil {
				readErr = x.check(ctx)
			}
			if readErr == nil && !validExecutionEvidence(name, x.a.App, x.a.Env, ev) {
				readErr = errors.New("invalid scoped evidence")
			}
			if readErr == nil {
				if p := x.g.sensitivePolicy(ev.Content); p != nil {
					readErr = x.guard(ctx, p, "retrieval", "sensitive_evidence")
					if readErr == nil {
						readErr = errors.New("sensitive evidence blocked")
					}
				}
			}
			status := "ok"
			if readErr != nil {
				status = "failed"
			}
			duration := int(time.Since(started).Milliseconds())
			auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			auditErr := x.s.RecordSpan(auditCtx, SpanEvent{ID: spanID, RunID: x.id, Kind: "tool", Name: name, ToolID: name, Status: status, Started: started, DurationMS: &duration})
			if auditErr != nil {
				return inference.Evidence{}, x.fail(auditErr)
			}
			if readErr != nil {
				return inference.Evidence{}, x.fail(readErr)
			}
			return ev, nil
		}
		out = append(out, t)
	}
	return out, nil
}
func validExecutionEvidence(name, app, env string, e inference.Evidence) bool {
	if !utf8.ValidString(e.Content) || strings.ContainsRune(e.Content, '\x00') || len(e.Content) > 16384 || strings.TrimSpace(e.Content) == "" {
		return false
	}
	target := base64.RawURLEncoding.EncodeToString([]byte(app + "\x00" + env))
	kind, source := "", ""
	switch name {
	case "read_service_profile":
		kind, source = "service_profile", "service_catalog"
	case "read_service_metrics":
		kind, source = "metrics", "prometheus"
	case "read_service_logs":
		kind, source = "logs", "loki"
	case "search_knowledge":
		kind, source, target = "knowledge", "knowledge://published-documents", app
	}
	return kind != "" && e.Kind == kind && e.Source == source && e.TargetID == target
}
func (x *governedExecution) toolAudit(ctx context.Context, id, name, status string) error {
	tx, err := x.s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = x.s.executionCheckTx(ctx, tx, x.r, x.a, x.c, x.hash); err != nil {
		return err
	}
	if err = audit(ctx, tx, x.a.agent, httpapi.CurrentUser(x.r).ID, "v3.agent.tool.started", id, map[string]any{"environment_id": x.a.Env, "run_id": x.id, "tool_id": name, "status": status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (x *governedExecution) emit(e inference.Event) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.failure != nil {
		return x.failure
	}
	// Content is withheld until completion and never copied to execution spans.
	if e.Delta != "" {
		x.outputBytes += len(e.Delta)
		if x.outputBytes > x.c.MaxOutputBytes {
			return x.fail(errors.New("output budget exceeded"))
		}
	}
	if e.Step == nil || e.Step.Kind != "model" {
		return nil
	}
	st := e.Step
	if st.Status == "running" {
		if x.steps[st.ID] || x.models >= x.c.MaxModelCalls {
			return x.fail(errors.New("model call budget exceeded"))
		}
		if err := x.check(x.r.Context()); err != nil {
			return x.fail(err)
		}
		x.steps[st.ID] = true
		x.models++
		return nil
	}
	if !x.steps[st.ID] {
		return x.fail(errors.New("unstarted model step"))
	}
	delete(x.steps, st.ID)
	status := "ok"
	if st.Status != "completed" && st.Status != "succeeded" {
		status = "failed"
	}
	var input, output *int
	if st.Usage != nil {
		input, output = &st.Usage.InputTokens, &st.Usage.OutputTokens
	}
	duration := int(time.Since(st.StartedAt).Milliseconds())
	if st.FinishedAt != nil {
		duration = int(st.FinishedAt.Sub(st.StartedAt).Milliseconds())
	}
	if err := x.s.RecordSpan(x.r.Context(), SpanEvent{ID: store.ID(), RunID: x.id, Kind: "llm", Name: "model", Status: status, ModelID: x.c.ModelID, Started: st.StartedAt, DurationMS: &duration, InputTokens: input, OutputTokens: output}); err != nil {
		return x.fail(err)
	}
	return nil
}
func (x *governedExecution) finish(ctx context.Context, status, code, output string) error {
	tx, err := x.s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Global order: Agent/config authority before run row, matching start and
	// lifecycle management. No network activity while these locks are held.
	if status == "success" {
		if err = x.s.executionCheckTx(ctx, tx, x.r, x.a, x.c, x.hash); err != nil {
			return err
		}
		c, e := LoadExecutionConfigTx(ctx, tx, x.a.ID, x.version)
		if e != nil {
			return e
		}
		if c.SHA256 != x.c.SHA256 {
			return errExecutionDrift
		}
		if len(x.steps) > 0 {
			return errors.New("incomplete model steps")
		}
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT status FROM v3_agent_runs WHERE id=$1 FOR UPDATE`, x.id).Scan(&current)
	if err != nil {
		return err
	}
	if current != "running" {
		return fmt.Errorf("run already terminal: %s", current)
	}
	if status == "success" {
		if err = x.finishBoundary(ctx, tx); err != nil {
			return err
		}
	}
	if status == "success" {
		if len(output) > x.c.MaxOutputBytes {
			return errors.New("result budget exceeded")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO v3_agent_execution_results(run_id,output_text) VALUES($1,$2)`, x.id, output); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_runs SET status=$2,error_code=NULLIF($3,''),finished_at=clock_timestamp() WHERE id=$1`, x.id, status, code)
	if err == nil {
		err = audit(ctx, tx, x.a.agent, httpapi.CurrentUser(x.r).ID, "v3.agent.run.completed", x.id, map[string]any{"environment_id": x.a.Env, "status": status, "error_code": code, "config_sha256": x.c.SHA256})
	}
	if err != nil {
		return err
	}
	if status == "success" {
		if err = x.finishBoundary(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (x *governedExecution) finishBoundary(ctx context.Context, tx pgx.Tx) error {
	if err := lifecycleSession(ctx, tx, x.r); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT deadline>clock_timestamp() FROM v3_agent_execution_snapshots WHERE run_id=$1`, x.id).Scan(&active); err != nil {
		return err
	}
	if !active {
		return context.DeadlineExceeded
	}
	return nil
}
