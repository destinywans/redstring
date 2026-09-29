package v3agents

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

var errRunScopeLimit = errors.New("too many authorized persisted runs")
var errRunSpanLimit = errors.New("too many reported spans")

type runRecord struct {
	Value run
	Agent agent
	Team  string
}

type runFeedback struct {
	Up     int64   `json:"up"`
	Down   int64   `json:"down"`
	MyVote *string `json:"my_vote"`
}

type runSpan struct {
	ID, Parent, Kind, Name, Status, ModelID, ToolID string
	Started                                         time.Time
	Duration                                        *int
	Input, Output                                   *int
}

type runSnapshot struct {
	Record   runRecord
	Spans    []runSpan
	Feedback *runFeedback
}

func runReadFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Session expired")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, pgx.ErrNoRows), errors.Is(err, store.ErrForbidden):
		notFound(w)
	case errors.Is(err, errRunScopeLimit), errors.Is(err, errRunSpanLimit):
		httpapi.Error(w, 413, "scope_too_large", "Persisted run view exceeds its bounded scope")
	default:
		unavailable(w)
	}
}

// The caller first pins the run, Agent, and active application relationship.
// The exact account/session and physical grant sources are then held until
// commit, so a revoke that commits first is seen by the final policy check.
func lockRunAuthority(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	userID := httpapi.CurrentUser(r).ID
	if err = store.LockActiveSession(ctx, tx, cookie.Value, userID); err != nil {
		return err
	}
	return lockEvalGrantSources(ctx, tx, userID)
}

func currentRunSession(ctx context.Context, tx pgx.Tx, r *http.Request) error {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return store.ErrSessionRevoked
	}
	return store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
}

func (s *Service) authorizeRunTx(ctx context.Context, tx pgx.Tx, r *http.Request, v runRecord, action string) error {
	user := httpapi.CurrentUser(r)
	err := s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
		Module: "agents", Action: action, EnvironmentID: v.Value.Env, ApplicationID: v.Value.App,
	})
	if err != nil {
		return err
	}
	if v.Team != "" {
		return s.Access.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{
			Module: "agents", Action: action, EnvironmentID: v.Value.Env, TeamID: v.Team,
		})
	}
	return nil
}

func lockRunActiveScope(ctx context.Context, tx pgx.Tx, app, env string) (string, error) {
	var team string
	err := tx.QueryRow(ctx, `SELECT COALESCE(app.owner_team_id,'')
FROM applications app
JOIN v3_application_environments mapped ON mapped.application_id=app.id AND mapped.environment_id=$2
JOIN application_environments e ON e.application_id=app.id AND e.key=$2
WHERE app.id=$1 AND NOT app.archived AND NOT e.archived
FOR SHARE OF app,mapped,e`, app, env).Scan(&team)
	return team, err
}

func lockRunRecord(ctx context.Context, tx pgx.Tx, id string, write bool) (run, error) {
	lock := "FOR SHARE OF v"
	if write {
		lock = "FOR UPDATE OF v"
	}
	return scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM v3_agent_runs v WHERE v.id=$1 `+lock, id))
}

func lockRunAgent(ctx context.Context, tx pgx.Tx, v run) (agent, error) {
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1 FOR SHARE`, v.AgentID))
	if err == nil && (a.App != v.App || a.Env != v.Env) {
		return agent{}, store.ErrNotFound
	}
	return a, err
}

func readRunSpans(ctx context.Context, tx pgx.Tx, id string) ([]runSpan, error) {
	rows, err := tx.Query(ctx, `SELECT id,COALESCE(parent_id,''),kind,name,status,
 COALESCE(model_id,''),COALESCE(tool_id,''),started_at,duration_ms,token_input,token_output
FROM v3_agent_run_spans WHERE run_id=$1
ORDER BY started_at,id LIMIT 501 FOR SHARE`, id)
	if err != nil {
		return nil, err
	}
	spans := make([]runSpan, 0)
	for rows.Next() {
		var sp runSpan
		if err = rows.Scan(&sp.ID, &sp.Parent, &sp.Kind, &sp.Name, &sp.Status,
			&sp.ModelID, &sp.ToolID, &sp.Started, &sp.Duration, &sp.Input, &sp.Output); err != nil {
			break
		}
		spans = append(spans, sp)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(spans) > 500 {
		return nil, errRunSpanLimit
	}
	return spans, nil
}

func readRunFeedback(ctx context.Context, tx pgx.Tx, runID, userID string) (*runFeedback, error) {
	var total int64
	var result runFeedback
	err := tx.QueryRow(ctx, `SELECT count(*),
count(*) FILTER (WHERE vote='up'),count(*) FILTER (WHERE vote='down'),
max(vote) FILTER (WHERE actor_id=$2)
FROM v3_agent_run_feedback WHERE run_id=$1`, runID, userID).
		Scan(&total, &result.Up, &result.Down, &result.MyVote)
	if err != nil || total == 0 {
		return nil, err
	}
	return &result, nil
}

// Every detail and trace field comes from one authorized transaction. Empty
// spans mean the producer did not report span coverage, not zero execution.
func (s *Service) readRunSnapshot(r *http.Request) (runSnapshot, error) {
	var result runSnapshot
	if s == nil || s.DB == nil || s.Access == nil {
		return result, errors.New("run storage unavailable")
	}
	envs, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || len(envs.EnvironmentIDs) == 0 || httpapi.CurrentUser(r).ID == "" {
		return result, store.ErrForbidden
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		return result, store.ErrNotFound
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		return result, err
	}
	defer tx.Rollback(r.Context())
	v, err := lockRunRecord(r.Context(), tx, id, false)
	if err != nil {
		return result, err
	}
	if !contains(envs.EnvironmentIDs, v.Env) {
		return result, store.ErrNotFound
	}
	a, err := lockRunAgent(r.Context(), tx, v)
	if err != nil {
		return result, err
	}
	team, err := lockRunActiveScope(r.Context(), tx, v.App, v.Env)
	if err != nil {
		return result, err
	}
	result.Record = runRecord{Value: v, Agent: a, Team: team}
	if err = lockRunAuthority(r.Context(), tx, r); err != nil {
		return result, err
	}
	if err = s.authorizeRunTx(r.Context(), tx, r, result.Record, "view"); err != nil {
		return result, err
	}
	if result.Spans, err = readRunSpans(r.Context(), tx, v.ID); err != nil {
		return result, err
	}
	if result.Feedback, err = readRunFeedback(r.Context(), tx, v.ID, httpapi.CurrentUser(r).ID); err != nil {
		return result, err
	}
	if err = currentRunSession(r.Context(), tx, r); err != nil {
		return result, err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return result, err
	}
	return result, nil
}
