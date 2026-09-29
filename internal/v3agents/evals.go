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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type evalRecord struct {
	ID, AgentID, Title, Baseline, Candidate, Status, ResultRef, CreatedBy string
	Metrics                                                               []byte
	Created                                                               time.Time
	Finished                                                              *time.Time
}

func loadEval(ctx context.Context, tx pgx.Tx, id string) (evalRecord, error) {
	var v evalRecord
	err := tx.QueryRow(ctx, `SELECT id,agent_id,title,baseline_version,candidate_version,status,
 COALESCE(metrics,'null'::jsonb),COALESCE(result_ref,''),created_by,created_at,finished_at
 FROM v3_agent_eval_experiments WHERE id=$1 FOR SHARE`, id).Scan(&v.ID, &v.AgentID, &v.Title,
		&v.Baseline, &v.Candidate, &v.Status, &v.Metrics, &v.ResultRef, &v.CreatedBy, &v.Created, &v.Finished)
	return v, err
}

func lockEvalAgent(ctx context.Context, tx pgx.Tx, id string) (agent, error) {
	return scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, id))
}

func lockActiveEvalScope(ctx context.Context, tx pgx.Tx, a agent) error {
	var appID string
	return tx.QueryRow(ctx, `SELECT app.id FROM applications app
 JOIN v3_application_environments mapped
   ON mapped.application_id=app.id AND mapped.environment_id=$2
 JOIN application_environments env
   ON env.application_id=app.id AND env.key=$2
 WHERE app.id=$1 AND NOT app.archived AND NOT env.archived
 FOR SHARE OF app,mapped,env`, a.App, a.Env).Scan(&appID)
}

// Hold the physical V3 grant sources through the final policy check and
// transaction commit. A revoke commits before that check or waits for commit.
func lockEvalGrantSources(ctx context.Context, tx pgx.Tx, userID string) error {
	var activeUserID string
	err := tx.QueryRow(ctx, `SELECT id FROM users
 WHERE id=$1 AND NOT disabled AND deleted_at IS NULL AND source<>'settings_demo'
 FOR SHARE`, userID).Scan(&activeUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrForbidden
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`)
	return err
}

func (s *Service) EvalDetail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	v, err := loadEval(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a, err := lockEvalAgent(r.Context(), tx, v.AgentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	// The eval row stores only agent_id. Its immutable creation audit is the
	// persisted origin scope; hide records with missing provenance or an Agent
	// moved to another app/environment rather than transferring old results.
	// Restrict by application_id so the existing audit application index bounds
	// the scan; a target index is still needed for large per-app audit histories.
	var originEnv string
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(changes->>'environment_id','')
 FROM management_audit WHERE application_id=$1 AND action='v3.agent.eval.created'
 AND target=$2 AND actor_id=$3 AND outcome='recorded' ORDER BY id LIMIT 1`,
		a.App, v.ID, v.CreatedBy).Scan(&originEnv)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && originEnv != a.Env) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: "view", EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	// A stored experiment is only a partial projection of the frontend's
	// EvalExperiment; no dataset, per-case, or radar producer is connected.
	metrics := any(nil)
	if v.Status == "passed" || v.Status == "failed" {
		if err = json.Unmarshal(v.Metrics, &metrics); err != nil {
			unavailable(w)
			return
		}
	}
	missing := []string{"dataset_content", "per_case_output", "radar_without_metric_producer", "evaluator_catalog"}
	if v.Status == "pending" || v.Status == "running" {
		missing = append(missing, "evaluation_result")
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": v.ID, "title": v.Title, "dataset": unknown,
		"meta": v.Status, "status": v.Status, "baseline_version": v.Baseline, "candidate_version": v.Candidate,
		"metrics": metrics, "radar": nil, "cases": []any{}, "datasets": []any{}, "evaluators": []any{},
		"result_ref": v.ResultRef, "created_at": timeString(v.Created),
		"missing": missing, "partial": true})
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
	// The first authorization only gates discovery. Hold the persisted target
	// and its active scope through the write so a reassignment or archive
	// cannot turn that earlier decision into a write for another application.
	lockedAgent, err := lockEvalAgent(r.Context(), tx, a.ID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (lockedAgent.App != a.App || lockedAgent.Env != a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a = lockedAgent
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
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

type caseRecord struct{ ID, AgentID, Title, ExpectedRef, OriginApp, OriginEnv string }

func (s *Service) EvalCase(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, false)
	if !ok {
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	c, err := loadCaseTx(r.Context(), tx, r.PathValue("id"), "FOR SHARE")
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a, err := lockEvalAgent(r.Context(), tx, c.AgentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if c.OriginApp != a.App || c.OriginEnv != a.Env {
		notFound(w)
		return
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	view, snapshotID, snapshotHash, err := loadEvalCaseSnapshot(r.Context(), tx, c)
	if err != nil {
		unavailable(w)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: "view", EnvironmentID: a.Env, ApplicationID: a.App})
	if errors.Is(err, store.ErrForbidden) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT label,note,actor_id,created_at,COALESCE(snapshot_id,'') FROM v3_agent_eval_labels
 WHERE case_id=$1 ORDER BY created_at DESC,id DESC LIMIT 50`, c.ID)
	if err != nil {
		unavailable(w)
		return
	}
	labels := make([]any, 0)
	for rows.Next() {
		var label, note, actor, labelSnapshotID string
		var at time.Time
		if err = rows.Scan(&label, &note, &actor, &at, &labelSnapshotID); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		labels = append(labels, map[string]any{"label": label, "note": note, "actor": actor,
			"time": timeString(at), "snapshot_id": labelSnapshotID})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	response := map[string]any{"id": c.ID, "title": c.Title,
		"expected_ref": c.ExpectedRef, "labels": labels,
		"snapshot_id": snapshotID, "snapshot_sha256": snapshotHash,
		"complete_v3_dto": view != nil, "partial": view == nil}
	if view == nil {
		response["queue"] = unknown
		response["alert"] = nil
		response["evidence"] = nil
		response["expected"] = nil
		response["output"] = nil
		response["baseline"] = nil
		response["auto"] = nil
		if c.ExpectedRef == "" {
			response["source_state"] = "unconfigured"
		} else {
			response["source_state"] = "awaiting_trusted_snapshot"
		}
		response["missing"] = []string{"case_input", "expected_content", "candidate_output", "baseline_output", "automated_score"}
	} else {
		response["queue"] = view.Queue
		response["alert"] = view.Alert
		response["evidence"] = view.Evidence
		response["expected"] = view.Expected
		response["output"] = view.Output
		response["baseline"] = view.Baseline
		response["auto"] = view.Auto
		response["source_state"] = "trusted_snapshot"
		response["missing"] = []string{}
	}
	httpapi.WriteJSON(w, 200, response)
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
	envs, ok := requestScope(w, r, true)
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
	c, err := loadCaseTx(r.Context(), tx, r.PathValue("id"), "FOR UPDATE")
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	a, err := lockEvalAgent(r.Context(), tx, c.AgentID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !contains(envs, a.Env)) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if c.OriginApp != a.App || c.OriginEnv != a.Env {
		notFound(w)
		return
	}
	if err = lockActiveEvalScope(r.Context(), tx, a); errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if err = lockEvalGrantSources(r.Context(), tx, httpapi.CurrentUser(r).ID); errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Agent permission required")
		return
	} else if err != nil {
		unavailable(w)
		return
	}
	if !s.authorizedTx(w, r, tx, a, "change") {
		return
	}
	_, snapshotID, snapshotHash, err := loadEvalCaseSnapshot(r.Context(), tx, c)
	if err != nil {
		unavailable(w)
		return
	}
	if snapshotID == "" {
		httpapi.Error(w, 409, "evaluation_case_not_ready", "Trusted evaluation snapshot required before labeling")
		return
	}
	id := store.ID()
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_eval_labels(id,case_id,label,note,actor_id,snapshot_id)
 VALUES($1,$2,$3,$4,$5,$6)`, id, c.ID, body.Next, body.Note, httpapi.CurrentUser(r).ID, snapshotID)
	if err != nil {
		unavailable(w)
		return
	}
	err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.label", c.ID,
		map[string]any{"environment_id": a.Env, "label": body.Next, "label_id": id,
			"snapshot_id": snapshotID, "snapshot_sha256": snapshotHash})
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
	a, err := lockEvalAgent(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if err = lockActiveEvalScope(ctx, tx, a); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_cases
 (id,agent_id,title,expected_ref,origin_application_id,origin_environment_id)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO NOTHING`, id, agentID, title, expectedRef, a.App, a.Env)
	if err != nil {
		return err
	}
	var existingAgent, existingTitle, existingRef, originApp, originEnv string
	err = tx.QueryRow(ctx, `SELECT agent_id,title,expected_ref,COALESCE(origin_application_id,''),
 COALESCE(origin_environment_id,'') FROM v3_agent_eval_cases WHERE id=$1`, id).
		Scan(&existingAgent, &existingTitle, &existingRef, &originApp, &originEnv)
	if err != nil {
		return err
	}
	if existingAgent != agentID || existingTitle != title || existingRef != expectedRef || originApp != a.App || originEnv != a.Env {
		return errors.New("evaluation case conflict")
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.eval.case.registered", id,
		map[string]any{"environment_id": a.Env}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
