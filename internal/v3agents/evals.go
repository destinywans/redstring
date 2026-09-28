package v3agents

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type evalRecord struct {
	ID, AgentID, Title, Baseline, Candidate, Status, ResultRef string
	Metrics                                                    []byte
	Created                                                    time.Time
	Finished                                                   *time.Time
}

func (s *Service) loadEval(ctx context.Context, id string) (evalRecord, error) {
	var v evalRecord
	err := s.DB.QueryRow(ctx, `SELECT id,agent_id,title,baseline_version,candidate_version,status,
 COALESCE(metrics,'null'::jsonb),COALESCE(result_ref,''),created_at,finished_at
 FROM v3_agent_eval_experiments WHERE id=$1`, id).Scan(&v.ID, &v.AgentID, &v.Title,
		&v.Baseline, &v.Candidate, &v.Status, &v.Metrics, &v.ResultRef, &v.Created, &v.Finished)
	return v, err
}

func (s *Service) EvalDetail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	v, err := s.loadEval(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	_, ok := s.objectAgent(w, r, v.AgentID, "view")
	if !ok {
		return
	}
	metrics := any(nil)
	if v.Status == "passed" || v.Status == "failed" {
		if err = json.Unmarshal(v.Metrics, &metrics); err != nil {
			unavailable(w)
			return
		}
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "title": v.Title, "dataset": unknown,
		"meta": v.Status, "status": v.Status, "baseline_version": v.Baseline, "candidate_version": v.Candidate,
		"metrics": metrics, "radar": nil, "cases": []any{}, "datasets": []any{}, "evaluators": []any{},
		"result_ref": v.ResultRef, "created_at": timeString(v.Created),
		"missing": []string{"dataset_content", "per_case_output", "radar_without_metric_producer"}, "partial": true})
}

type createEvalBody struct {
	AgentID          string `json:"agent_id"`
	Title            string `json:"title"`
	BaselineVersion  string `json:"baseline_version"`
	CandidateVersion string `json:"candidate_version"`
}

func (s *Service) CreateEval(w http.ResponseWriter, r *http.Request) {
	var body createEvalBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if !bounded(body.AgentID, 128) || !bounded(body.Title, 240) || !bounded(body.BaselineVersion, 80) ||
		!bounded(body.CandidateVersion, 80) || body.BaselineVersion == body.CandidateVersion {
		invalid(w, "Choose an Agent, title, and two distinct known versions")
		return
	}
	a, ok := s.objectAgent(w, r, body.AgentID, "change")
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.authorizedTx(w, r, tx, a, "change") {
		return
	}
	var count int
	err = tx.QueryRow(r.Context(), `SELECT count(*) FROM v3_agent_versions
 WHERE agent_id=$1 AND version=ANY($2)`, a.ID, []string{body.BaselineVersion, body.CandidateVersion}).Scan(&count)
	if err != nil {
		unavailable(w)
		return
	}
	if count != 2 {
		notFound(w)
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_eval_experiments
 (id,agent_id,title,baseline_version,candidate_version,created_by)
 VALUES($1,$2,$3,$4,$5,$6)`, id, a.ID, body.Title, body.BaselineVersion,
		body.CandidateVersion, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.created", id,
		map[string]any{"environment_id": a.Env, "baseline_version": body.BaselineVersion,
			"candidate_version": body.CandidateVersion, "status": "pending"})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 201, map[string]any{"id": id, "status": "pending", "execution_available": false})
}

type caseRecord struct{ ID, AgentID, Title, ExpectedRef string }

func (s *Service) loadCase(ctx context.Context, id string) (caseRecord, error) {
	var c caseRecord
	err := s.DB.QueryRow(ctx, `SELECT id,agent_id,title,expected_ref FROM v3_agent_eval_cases WHERE id=$1`, id).
		Scan(&c.ID, &c.AgentID, &c.Title, &c.ExpectedRef)
	return c, err
}

func (s *Service) EvalCase(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	c, err := s.loadCase(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	_, ok := s.objectAgent(w, r, c.AgentID, "view")
	if !ok {
		return
	}
	rows, err := s.DB.Query(r.Context(), `SELECT label,note,actor_id,created_at FROM v3_agent_eval_labels
 WHERE case_id=$1 ORDER BY created_at DESC,id DESC LIMIT 50`, c.ID)
	if err != nil {
		unavailable(w)
		return
	}
	defer rows.Close()
	labels := make([]any, 0)
	for rows.Next() {
		var label, note, actor string
		var at time.Time
		if err = rows.Scan(&label, &note, &actor, &at); err != nil {
			unavailable(w)
			return
		}
		labels = append(labels, map[string]any{"label": label, "note": note, "actor": actor, "time": timeString(at)})
	}
	if rows.Err() != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": c.ID, "title": c.Title, "queue": unknown,
		"expected_ref": c.ExpectedRef, "labels": labels, "alert": nil, "evidence": nil, "expected": nil,
		"output": nil, "baseline": nil, "auto": nil,
		"missing": []string{"case_input", "model_output", "automated_score"}, "partial": true})
}

type labelBody struct {
	Next string `json:"next"`
	Note string `json:"note"`
}

func (s *Service) LabelCase(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	c, err := s.loadCase(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a, ok := s.objectAgent(w, r, c.AgentID, "change")
	if !ok {
		return
	}
	var body labelBody
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if body.Next != "correct" && body.Next != "incorrect" && body.Next != "partial" && body.Next != "unsure" {
		invalid(w, "Unknown human label")
		return
	}
	if len([]rune(body.Note)) > 2000 {
		invalid(w, "Note too long")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	if !s.authorizedTx(w, r, tx, a, "change") {
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_eval_labels(id,case_id,label,note,actor_id)
 VALUES($1,$2,$3,$4,$5)`, id, c.ID, body.Next, body.Note, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.label", c.ID,
		map[string]any{"environment_id": a.Env, "label": body.Next, "label_id": id})
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"next": body.Next, "id": id})
}

// EvaluationOutcome is accepted only from a trusted evaluator integration.
// Metric names and values are aggregate scores, never prompts or case inputs.
type EvaluationOutcome struct {
	ID        string
	Status    string // passed, failed, or invalid
	ResultRef string
	Metrics   map[string]float64
}

func (s *Service) RecordEvaluationOutcome(ctx context.Context, out EvaluationOutcome) error {
	if s == nil || s.DB == nil || !bounded(out.ID, 128) || !safeCode(out.ResultRef, 500) ||
		strings.HasPrefix(strings.ToLower(out.ResultRef), "vault:") ||
		(out.Status != "passed" && out.Status != "failed" && out.Status != "invalid") || len(out.Metrics) > 32 ||
		(out.Status == "passed" && len(out.Metrics) == 0) {
		return errors.New("invalid evaluation outcome")
	}
	for k, v := range out.Metrics {
		if !safeCode(k, 80) || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			return errors.New("invalid evaluation metric")
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var agentID string
	err = tx.QueryRow(ctx, `SELECT agent_id FROM v3_agent_eval_experiments WHERE id=$1 AND status='pending' FOR UPDATE`, out.ID).Scan(&agentID)
	if err != nil {
		return err
	}
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, agentID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE v3_agent_eval_experiments SET status=$2,metrics=$3,result_ref=$4,finished_at=now()
 WHERE id=$1`, out.ID, out.Status, out.Metrics, out.ResultRef)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.eval.outcome", out.ID,
		map[string]any{"environment_id": a.Env, "status": out.Status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RegisterEvalCase adds a case reference without copying private source data.
func (s *Service) RegisterEvalCase(ctx context.Context, id, agentID, title, expectedRef string) error {
	if s == nil || s.DB == nil || !bounded(id, 128) || !bounded(agentID, 128) || !bounded(title, 240) ||
		(expectedRef != "" && !safeCode(expectedRef, 500)) {
		return errors.New("invalid evaluation case")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, agentID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_cases(id,agent_id,title,expected_ref)
 VALUES($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING`, id, agentID, title, expectedRef)
	if err != nil {
		return err
	}
	var existingAgent, existingTitle, existingRef string
	err = tx.QueryRow(ctx, `SELECT agent_id,title,expected_ref FROM v3_agent_eval_cases WHERE id=$1`, id).
		Scan(&existingAgent, &existingTitle, &existingRef)
	if err != nil {
		return err
	}
	if existingAgent != agentID || existingTitle != title || existingRef != expectedRef {
		return errors.New("evaluation case conflict")
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.eval.case.registered", id,
		map[string]any{"environment_id": a.Env}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
