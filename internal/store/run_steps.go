package store

import (
	"context"
	"encoding/json"

	"github.com/wans/aiops-canvas/internal/core"
)

// StepRecorder is optional: workers persist visible agent steps when the
// repository supports it, without changing the core RunRepository contract.
type StepRecorder interface {
	SaveEvidenceID(context.Context, Claim, core.Evidence) (string, error)
	RecordStep(context.Context, Claim, core.RunStep) error
}

var _ StepRecorder = (*Store)(nil)

const stepTextLimit = 4096

func clip(s string) string {
	if len(s) <= stepTextLimit {
		return s
	}
	cut := stepTextLimit
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// RecordStep upserts one step under the Attempt lease and publishes it as an
// attempt.step event so open canvases see tool calls as they happen.
func (s *Store) RecordStep(ctx context.Context, c Claim, v core.RunStep) error {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = fence(ctx, tx, c); e != nil {
		return e
	}
	v.RunID, v.AttemptID = c.Run.ID, c.Token
	v.Input, v.Output, v.Error = clip(v.Input), clip(v.Output), clip(v.Error)
	if v.ToolCalls == nil {
		v.ToolCalls = []string{}
	}
	calls, e := json.Marshal(v.ToolCalls)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO run_steps(id,run_id,attempt_id,seq,kind,name,status,input,output,error,evidence_id,input_tokens,output_tokens,tool_calls,trace_id,started_at,finished_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,''),$12,$13,$14,$15,$16,$17)
ON CONFLICT(id) DO UPDATE SET status=EXCLUDED.status,output=EXCLUDED.output,error=EXCLUDED.error,evidence_id=EXCLUDED.evidence_id,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,tool_calls=EXCLUDED.tool_calls,finished_at=EXCLUDED.finished_at
WHERE run_steps.attempt_id=EXCLUDED.attempt_id`,
		v.ID, v.RunID, v.AttemptID, v.Seq, v.Kind, v.Name, v.Status, v.Input, v.Output, v.Error, v.EvidenceID, v.InputTokens, v.OutputTokens, calls, v.TraceID, v.StartedAt, v.FinishedAt)
	if e != nil {
		return e
	}
	if e = event(ctx, tx, c.InvestigationID, c.Run.BranchID, "attempt.step", v); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// RunSteps lists every Attempt's steps for a run, oldest first.
func (s *Store) RunSteps(ctx context.Context, runID string) ([]core.RunStep, error) {
	rows, e := s.DB.Query(ctx, `SELECT id,run_id,attempt_id,seq,kind,name,status,input,output,error,COALESCE(evidence_id,''),input_tokens,output_tokens,tool_calls,trace_id,started_at,finished_at
FROM run_steps WHERE run_id=$1 ORDER BY started_at,seq`, runID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []core.RunStep{}
	for rows.Next() {
		var v core.RunStep
		var calls []byte
		if e = rows.Scan(&v.ID, &v.RunID, &v.AttemptID, &v.Seq, &v.Kind, &v.Name, &v.Status, &v.Input, &v.Output, &v.Error, &v.EvidenceID, &v.InputTokens, &v.OutputTokens, &calls, &v.TraceID, &v.StartedAt, &v.FinishedAt); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(calls, &v.ToolCalls); e != nil {
			return nil, e
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
