package v3agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) completeEvalExecution(r *http.Request, m evalExecutionMaterial, results []evalSideResult) error {
	if len(results) != 2 {
		return errEvalConflict
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, v, err := s.evalExecutionScope(ctx, tx, r.WithContext(ctx), "change")
	if err != nil {
		return err
	}
	if a.ID != m.AgentID || a.App != m.ApplicationID || a.Env != m.EnvironmentID || v.Status != "running" {
		return errEvalConflict
	}
	var valid bool
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT status='running' AND deadline>clock_timestamp() AND started_sides=2,material_sha256 FROM v3_agent_eval_executions WHERE experiment_id=$1 FOR UPDATE`, v.ID).Scan(&valid, &storedHash)
	if err != nil {
		return err
	}
	_, actualHash, _ := evalJSONHash(m)
	if !valid || storedHash != actualHash {
		return errEvalConflict
	}
	for _, side := range m.Versions {
		current, e := LoadExecutionConfigTx(ctx, tx, a.ID, side.Version)
		if e != nil {
			return e
		}
		if current.SHA256 != side.Config.SHA256 {
			return errEvalConfiguration
		}
	}
	c, err := loadCaseTx(ctx, tx, m.CaseID, "FOR UPDATE")
	if err != nil {
		return err
	}
	if c.AgentID != a.ID || c.OriginApp != a.App || c.OriginEnv != a.Env || c.ExpectedRef != "eval-input:"+c.ID {
		return errEvalConflict
	}
	view := EvalCaseView{Queue: m.Input.Queue, Alert: m.Input.Alert, Evidence: m.Input.Evidence, Expected: m.Input.Expected,
		Output:   EvalCaseOutput{Version: results[1].Version, Meta: "Single human-supplied case; tool-free model output; usage unknown", Tools: []string{}, Markdown: results[1].Text},
		Baseline: EvalCaseBaseline{Version: results[0].Version, Score: fmt.Sprintf("%.4f", results[0].Score), Text: results[0].Text},
		Auto:     EvalCaseAuto{Score: fmt.Sprintf("%.4f", results[1].Score), Note: "Fraction of explicit case-sensitive literal checks satisfied. Not a human correctness label or general Agent quality score.", Rules: m.Input.Checks}}
	if !validEvalCaseView(view) {
		return errEvalConfiguration
	}
	payload, hash, err := evalJSONHash(view)
	if err != nil {
		return err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM v3_agent_eval_case_snapshots WHERE case_id=$1`, c.ID).Scan(&revision); err != nil {
		return err
	}
	snapshotID := store.ID()
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_case_snapshots(id,case_id,agent_id,revision,producer_ref,input_ref,expected_ref,output_ref,baseline_ref,evaluator_ref,payload,payload_sha256)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, snapshotID, c.ID, a.ID, revision, "eval-execution:"+v.ID, "eval-case-input:"+m.InputID, c.ExpectedRef, "eval-execution:"+v.ID+":candidate", "eval-execution:"+v.ID+":baseline", "literal-contains:v1", payload, hash)
	if err != nil {
		return err
	}
	raw, _, err := evalJSONHash(results)
	if err != nil {
		return err
	}
	status := "failed"
	if results[1].Score == 1 {
		status = "passed"
	}
	metrics := map[string]float64{"literal_checks_baseline": results[0].Score, "literal_checks_candidate": results[1].Score}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status=$2,metrics=$3,result_ref=$4,finished_at=clock_timestamp() WHERE id=$1`, v.ID, status, metrics, "eval-execution:"+v.ID)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.case.snapshot", c.ID, map[string]any{"environment_id": a.Env, "snapshot_id": snapshotID, "snapshot_revision": revision, "producer_ref": "eval-execution:" + v.ID, "payload_sha256": hash}); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.execution.completed", v.ID, map[string]any{"environment_id": a.Env, "status": status, "snapshot_id": snapshotID, "evaluation_kind": "single_case_literal_checks", "case_id": c.ID}); err != nil {
		return err
	}
	if err = evalSessionStillActive(ctx, tx, r); err != nil {
		return err
	}
	// Config/case locks and audit writes above can wait beyond the deadline.
	// Make this the final storage statement: the already locked execution and
	// original session must still be current at publication time. A zero-row
	// update rolls back the report, snapshot, experiment outcome and audits.
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	tag, err := tx.Exec(ctx, `UPDATE v3_agent_eval_executions
 SET status='succeeded',results=$2,snapshot_id=$3,finished_at=clock_timestamp()
 WHERE experiment_id=$1 AND status='running' AND started_sides=2
 AND deadline>clock_timestamp() AND material_sha256=$4 AND actor_id=$5
 AND EXISTS(SELECT 1 FROM sessions WHERE token_hash=$6 AND user_id=$5 AND expires_at>clock_timestamp())`,
		v.ID, raw, snapshotID, actualHash, httpapi.CurrentUser(r).ID, store.Hash(cookie.Value))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errEvalConflict
	}
	return tx.Commit(ctx)
}

// Terminal failure writes retain dispatch count, status and hashes only. They
// cannot fabricate outputs or a passing release gate, even after session loss.
func (s *Service) terminateEvalExecution(id, status, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var agentID string
	if err = tx.QueryRow(ctx, `SELECT agent_id FROM v3_agent_eval_experiments WHERE id=$1 FOR UPDATE`, id).Scan(&agentID); err != nil {
		return err
	}
	var a agent
	a.ID = agentID
	err = tx.QueryRow(ctx, `UPDATE v3_agent_eval_executions SET status=$2,error_code=$3,finished_at=clock_timestamp() WHERE experiment_id=$1 AND status='running' RETURNING application_id,environment_id`, id, status, code).Scan(&a.App, &a.Env)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status='invalid',metrics=NULL,finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, id); err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.eval.execution.terminated", id, map[string]any{"environment_id": a.Env, "status": status, "error_code": code}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func expireEvalExecution(ctx context.Context, tx pgx.Tx, a agent, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE v3_agent_eval_executions SET status='ambiguous',error_code='execution_deadline_expired',finished_at=clock_timestamp() WHERE experiment_id=$1 AND status='running' AND deadline<=clock_timestamp()`, id)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status='invalid',metrics=NULL,finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, id); err != nil {
		return err
	}
	return audit(ctx, tx, a, "system", "v3.agent.eval.execution.terminated", id, map[string]any{"environment_id": a.Env, "status": "ambiguous", "error_code": "execution_deadline_expired"})
}

func (s *Service) EvalExecution(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	if _, ok := requestScope(w, r, false); !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	a, v, err := s.evalExecutionScope(ctx, tx, r, "view")
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	if err = expireEvalExecution(ctx, tx, a, v.ID); err != nil {
		evalHTTPError(w, err)
		return
	}
	var caseID, status, code, hash, snapshot string
	var raw, results []byte
	var sides int
	var created, deadline time.Time
	var finished *time.Time
	err = tx.QueryRow(ctx, `SELECT case_id,status,error_code,material,material_sha256,results,COALESCE(snapshot_id,''),started_sides,created_at,deadline,finished_at FROM v3_agent_eval_executions WHERE experiment_id=$1 FOR SHARE`, v.ID).Scan(&caseID, &status, &code, &raw, &hash, &results, &snapshot, &sides, &created, &deadline, &finished)
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	var material evalExecutionMaterial
	if json.Unmarshal(raw, &material) != nil {
		unavailable(w)
		return
	}
	_, digest, _ := evalJSONHash(material)
	if digest != hash || material.ApplicationID != a.App || material.EnvironmentID != a.Env {
		unavailable(w)
		return
	}
	var output any
	if results != nil && json.Unmarshal(results, &output) != nil {
		unavailable(w)
		return
	}
	if err = evalSessionStillActive(ctx, tx, r); err != nil {
		evalHTTPError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		unavailable(w)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "case_id": caseID, "status": status, "error_code": code, "material": material, "material_sha256": hash, "results": output, "snapshot_id": snapshot, "started_sides": sides, "created_at": created, "deadline": deadline, "finished_at": finished, "evaluation_kind": "single_case_literal_checks", "human_labels_modified": false, "usage_source": "unknown"})
}

func (s *Service) CancelEval(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	if _, ok := requestScope(w, r, true); !ok {
		return
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	a, v, err := s.evalExecutionScope(ctx, tx, r, "change")
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	tag, err := tx.Exec(ctx, `UPDATE v3_agent_eval_executions SET status='cancelled',error_code='cancelled_by_user',finished_at=clock_timestamp() WHERE experiment_id=$1 AND status='running'`, v.ID)
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		evalHTTPError(w, errEvalConflict)
		return
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status='invalid',metrics=NULL,finished_at=clock_timestamp() WHERE id=$1 AND status='running'`, v.ID)
	if err == nil {
		err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.execution.cancelled", v.ID, map[string]any{"environment_id": a.Env})
	}
	if err == nil {
		err = evalSessionStillActive(ctx, tx, r)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "status": "cancelled", "provider_outcome": "may_have_been_billed"})
}
