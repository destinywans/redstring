package v3agents

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// EvalInput is explicitly human-supplied test material, not automatically
// observed infrastructure evidence or a correctness judgment.
type EvalInput struct {
	AgentID  string             `json:"agent_id"`
	Title    string             `json:"title"`
	Input    string             `json:"input"`
	Queue    string             `json:"queue"`
	Alert    EvalCaseAlert      `json:"alert"`
	Evidence []EvalCaseEvidence `json:"evidence"`
	Expected EvalCaseExpected   `json:"expected"`
	Checks   []string           `json:"checks"`
}

func evalJSONHash(v any) ([]byte, string, error) {
	raw, err := json.Marshal(v)
	return raw, fmt.Sprintf("%x", sha256.Sum256(raw)), err
}
func validEvalInput(v EvalInput) bool {
	if !bounded(v.AgentID, 128) || !bounded(v.Title, 240) || strings.TrimSpace(v.Input) == "" || len(v.Input) > 8192 || len(v.Checks) < 1 || len(v.Checks) > 16 {
		return false
	}
	view := EvalCaseView{Queue: v.Queue, Alert: v.Alert, Evidence: v.Evidence, Expected: v.Expected,
		Output:   EvalCaseOutput{Version: "v1", Meta: "placeholder", Tools: []string{}, Markdown: "placeholder"},
		Baseline: EvalCaseBaseline{Version: "v2", Score: "0", Text: "placeholder"}, Auto: EvalCaseAuto{Score: "0", Note: "placeholder", Rules: []string{}}}
	if !validEvalCaseView(view) {
		return false
	}
	seen := map[string]bool{}
	for _, check := range v.Checks {
		if !bounded(check, 500) || seen[check] {
			return false
		}
		seen[check] = true
	}
	raw, _, err := evalJSONHash(v)
	// Check the decoded values as well as the marshaled representation: JSON
	// escaping must not hide NUL, and Go must not silently repair invalid UTF-8.
	if err != nil || len(raw) > 48<<10 || !utf8.ValidString(v.Input) {
		return false
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return false
	}
	var textOK func(any) bool
	textOK = func(value any) bool {
		switch x := value.(type) {
		case string:
			return !strings.ContainsRune(x, 0)
		case []any:
			for _, v := range x {
				if !textOK(v) {
					return false
				}
			}
		case map[string]any:
			for _, v := range x {
				if !textOK(v) {
					return false
				}
			}
		}
		return true
	}
	return textOK(decoded)
}

// evalAuthority acquires the same current-session and physical-grant locks
// for every create, read, dispatch and result commit. No lock spans inference.
func (s *Service) evalAuthority(ctx context.Context, tx pgx.Tx, r *http.Request, agentID, action string) (agent, error) {
	if err := lockEvalHumanSession(ctx, tx, r); err != nil {
		return agent{}, err
	}
	if err := lockEvalGrantSources(ctx, tx, httpapi.CurrentUser(r).ID); err != nil {
		return agent{}, err
	}
	a, err := lockEvalAgent(ctx, tx, agentID)
	if err != nil {
		return a, err
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || !contains(scope.EnvironmentIDs, a.Env) {
		return a, store.ErrNotFound
	}
	if err = lockActiveEvalScope(ctx, tx, a); err != nil {
		return a, err
	}
	team, err := evalOwnerTeam(ctx, tx, a)
	if err != nil {
		return a, err
	}
	if err = s.authorizeEvalTx(ctx, tx, r, a, team, "view"); err != nil {
		return a, err
	}
	if action != "view" {
		if err = s.authorizeEvalTx(ctx, tx, r, a, team, action); err != nil {
			return a, err
		}
	}
	return a, evalSessionStillActive(ctx, tx, r)
}
func evalHTTPError(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		err = errEvalConflict
	}
	switch {
	case errors.Is(err, errEvalNotConnected):
		httpapi.Error(w, 503, "eval_not_connected", "Evaluation model runtime is not configured")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrForbidden), errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows):
		notFound(w)
	case errors.Is(err, errEvalConflict):
		httpapi.Error(w, 409, "eval_execution_conflict", "Evaluation execution is already reserved or no longer runnable")
	case errors.Is(err, errEvalConfiguration):
		httpapi.Error(w, 422, "eval_configuration_incompatible", "Two configured tool-free versions and registered model bindings are required")
	default:
		unavailable(w)
	}
}

func (s *Service) CreateEvalCase(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	if _, ok := requestScope(w, r, true); !ok {
		return
	}
	var in EvalInput
	if !decodePromptVersion(w, r, &in) {
		return
	}
	if !validEvalInput(in) {
		invalid(w, "Provide bounded human case input, alert/evidence context, expected answer and 1–16 distinct literal checks")
		return
	}
	ctx := r.Context()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	a, err := s.evalAuthority(ctx, tx, r, in.AgentID, "change")
	if err != nil {
		evalHTTPError(w, err)
		return
	}
	id, inputID := store.ID(), store.ID()
	expectedRef := "eval-input:" + id
	raw, hash, err := evalJSONHash(in)
	if err != nil {
		unavailable(w)
		return
	}
	inputHash := fmt.Sprintf("%x", sha256.Sum256([]byte(in.Input)))
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_cases(id,agent_id,title,expected_ref,origin_application_id,origin_environment_id) VALUES($1,$2,$3,$4,$5,$6)`, id, a.ID, in.Title, expectedRef, a.App, a.Env)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO v3_agent_prompt_trial_case_inputs(id,case_id,agent_id,origin_application_id,origin_environment_id,revision,producer_ref,input_text,input_sha256) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8)`, inputID, id, a.ID, a.App, a.Env, "human:"+httpapi.CurrentUser(r).ID, in.Input, inputHash)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO v3_agent_eval_inputs(case_id,input_id,material,material_sha256,created_by) VALUES($1,$2,$3,$4,$5)`, id, inputID, raw, hash, httpapi.CurrentUser(r).ID)
	}
	if err == nil {
		err = audit(ctx, tx, a, httpapi.CurrentUser(r).ID, "v3.agent.eval.case.input", id, map[string]any{"environment_id": a.Env, "input_id": inputID, "input_sha256": inputHash, "material_sha256": hash, "source": "human_supplied"})
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
	httpapi.WriteJSON(w, 201, map[string]any{"id": id, "input_id": inputID, "input_sha256": inputHash, "material_sha256": hash, "source": "human_supplied", "execution_state": "not_run"})
}

func loadEvalInput(ctx context.Context, tx pgx.Tx, id string) (EvalInput, string, error) {
	var in EvalInput
	var raw []byte
	var hash, inputID string
	err := tx.QueryRow(ctx, `SELECT material,material_sha256,input_id FROM v3_agent_eval_inputs WHERE case_id=$1 FOR SHARE`, id).Scan(&raw, &hash, &inputID)
	if err != nil {
		return in, "", err
	}
	if json.Unmarshal(raw, &in) != nil || !validEvalInput(in) {
		return in, "", errEvalConfiguration
	}
	_, actual, err := evalJSONHash(in)
	if err != nil || hash != actual {
		return in, "", errEvalConfiguration
	}
	return in, inputID, nil
}
