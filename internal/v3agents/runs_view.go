package v3agents

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type runSummary struct {
	PersistedRuns int      `json:"persisted_runs"`
	Success       int      `json:"success"`
	Failed        int      `json:"failed"`
	Blocked       int      `json:"blocked"`
	Running       int      `json:"running"`
	Negative      int      `json:"negative"`
	AvgDurationMS *float64 `json:"avg_duration_ms"`
	FeedbackVotes int64    `json:"feedback_votes"`
}

type runCounts struct {
	All      int `json:"all"`
	Failed   int `json:"failed"`
	Blocked  int `json:"blocked"`
	Negative int `json:"negative"`
}

type runTrend struct {
	OK              []int  `json:"ok"`
	Failed          []int  `json:"failed"`
	Blocked         []int  `json:"blocked"`
	WindowStartAt   string `json:"window_start_at"`
	IntervalMinutes int    `json:"interval_minutes"`
}

type runListItem struct {
	ID            string       `json:"id"`
	Agent         string       `json:"agent"`
	AgentID       string       `json:"agent_id"`
	ApplicationID string       `json:"application_id"`
	EnvironmentID string       `json:"environment_id"`
	Version       string       `json:"version"`
	Trigger       string       `json:"trigger"`
	Scope         string       `json:"scope"`
	Status        string       `json:"status"`
	Steps         *int64       `json:"steps"`
	Tokens        *int64       `json:"tokens"`
	DurationMS    *int64       `json:"duration_ms"`
	StartedAt     string       `json:"started_at"`
	Feedback      *runFeedback `json:"feedback"`
}

func runTimeString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func runOffsetMS(t, start time.Time) int64 { return t.UnixMilli() - start.UnixMilli() }

func runDuration(v run) *int64 {
	if v.Finished == nil || v.Finished.Before(v.Started) {
		return nil
	}
	ms := runOffsetMS(*v.Finished, v.Started)
	return &ms
}

func runTokens(v run) *int64 {
	if v.Input == nil || v.Output == nil {
		return nil
	}
	total := int64(*v.Input) + int64(*v.Output)
	return &total
}

// The list is bounded over all currently visible persisted rows before the
// filter and display page are applied. Aggregates never use a truncated page.
func (s *Service) Runs(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()["filter"]
	if len(values) > 1 {
		invalid(w, "Select one run filter")
		return
	}
	filter := "all"
	if len(values) == 1 {
		filter = values[0]
	}
	switch filter {
	case "all", "failed", "blocked", "negative":
	default:
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
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	// Pin run rows first, then catalog rows below. Feedback writes take the
	// same run-before-catalog order; locking joined catalog rows while still
	// scanning multiple runs can deadlock with a feedback writer.
	// The owner team is a current ACL dimension, not historical run origin.
	rows, err := tx.Query(r.Context(), `SELECT `+runColumns+`,a.name,COALESCE(app.owner_team_id,'')
FROM v3_agent_runs v
JOIN v3_agents a ON a.id=v.agent_id AND a.application_id=v.application_id AND a.environment_id=v.environment_id
JOIN applications app ON app.id=v.application_id AND NOT app.archived
JOIN v3_application_environments mapped ON mapped.application_id=app.id AND mapped.environment_id=v.environment_id
JOIN application_environments e ON e.application_id=app.id AND e.key=v.environment_id AND NOT e.archived
WHERE v.environment_id=ANY($1::text[])
AND `+visibleAgentGrantSQL+`
AND (app.owner_team_id IS NULL OR EXISTS (
 SELECT 1 FROM v3_effective_role_grants t WHERE t.user_id=$2 AND t.module='agents' AND t.action='view'
 AND ((t.scope_kind='global' AND t.scope_id='*')
   OR (t.scope_kind='environment' AND t.scope_id=v.environment_id)
   OR (t.scope_kind='team' AND t.scope_id=app.owner_team_id))
))
ORDER BY v.started_at DESC,v.id DESC LIMIT 2001 FOR SHARE OF v`,
		envs, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	candidates := make([]runRecord, 0)
	for rows.Next() {
		var rec runRecord
		err = rows.Scan(&rec.Value.ID, &rec.Value.AgentID, &rec.Value.Version, &rec.Value.App,
			&rec.Value.Env, &rec.Value.Trigger, &rec.Value.Status, &rec.Value.ErrorCode,
			&rec.Value.Started, &rec.Value.Finished, &rec.Value.Input, &rec.Value.Output,
			&rec.Agent.Name, &rec.Team)
		if err != nil {
			break
		}
		rec.Agent.ID, rec.Agent.App, rec.Agent.Env = rec.Value.AgentID, rec.Value.App, rec.Value.Env
		candidates = append(candidates, rec)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		unavailable(w)
		return
	}
	agentRuns := map[string]run{}
	type bindingKey struct{ app, env string }
	bindings := map[bindingKey]bool{}
	for _, rec := range candidates {
		agentRuns[rec.Value.AgentID] = rec.Value
		bindings[bindingKey{rec.Value.App, rec.Value.Env}] = true
	}
	agentIDs := make([]string, 0, len(agentRuns))
	for id := range agentRuns {
		agentIDs = append(agentIDs, id)
	}
	sort.Strings(agentIDs)
	agents := map[string]agent{}
	for _, id := range agentIDs {
		a, lockErr := lockRunAgent(r.Context(), tx, agentRuns[id])
		if errors.Is(lockErr, pgx.ErrNoRows) || errors.Is(lockErr, store.ErrNotFound) {
			continue
		}
		if lockErr != nil {
			unavailable(w)
			return
		}
		agents[id] = a
	}
	bindingKeys := make([]bindingKey, 0, len(bindings))
	for key := range bindings {
		bindingKeys = append(bindingKeys, key)
	}
	sort.Slice(bindingKeys, func(i, j int) bool {
		if bindingKeys[i].app != bindingKeys[j].app {
			return bindingKeys[i].app < bindingKeys[j].app
		}
		return bindingKeys[i].env < bindingKeys[j].env
	})
	currentTeams := map[bindingKey]string{}
	for _, key := range bindingKeys {
		team, lockErr := lockRunActiveScope(r.Context(), tx, key.app, key.env)
		if errors.Is(lockErr, pgx.ErrNoRows) {
			continue
		}
		if lockErr != nil {
			unavailable(w)
			return
		}
		currentTeams[key] = team
	}
	if err = lockRunAuthority(r.Context(), tx, r); err != nil {
		runReadFailure(w, err)
		return
	}
	type scopeKey struct{ app, team, env string }
	authorized := map[scopeKey]bool{}
	visible := make([]runRecord, 0, len(candidates))
	for _, rec := range candidates {
		a, found := agents[rec.Value.AgentID]
		if !found {
			continue
		}
		team, found := currentTeams[bindingKey{rec.Value.App, rec.Value.Env}]
		if !found {
			continue
		}
		rec.Agent, rec.Team = a, team
		key := scopeKey{rec.Value.App, rec.Team, rec.Value.Env}
		allowed, seen := authorized[key]
		if !seen {
			err = s.authorizeRunTx(r.Context(), tx, r, rec, "view")
			if err != nil && !errors.Is(err, store.ErrForbidden) {
				runReadFailure(w, err)
				return
			}
			allowed = err == nil
			authorized[key] = allowed
		}
		if allowed {
			visible = append(visible, rec)
		}
	}
	// More than 2000 initial qualifying rows cannot prove complete scope
	// aggregates, even if a concurrent revoke hid some during rechecking.
	if len(candidates) > 2000 {
		runReadFailure(w, errRunScopeLimit)
		return
	}
	ids := make([]string, 0, len(visible))
	for _, rec := range visible {
		ids = append(ids, rec.Value.ID)
	}
	feedback, err := listRunFeedback(r.Context(), tx, ids, httpapi.CurrentUser(r).ID)
	if err != nil {
		unavailable(w)
		return
	}
	steps, err := listRunSpanCounts(r.Context(), tx, ids)
	if err != nil {
		unavailable(w)
		return
	}
	var now time.Time
	if err = tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		unavailable(w)
		return
	}
	if err = currentRunSession(r.Context(), tx, r); err != nil {
		runReadFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, buildRunsView(visible, feedback, steps, filter, now))
}

func listRunFeedback(ctx context.Context, tx pgx.Tx, ids []string, userID string) (map[string]*runFeedback, error) {
	result := map[string]*runFeedback{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT run_id,count(*) FILTER (WHERE vote='up'),
count(*) FILTER (WHERE vote='down'),max(vote) FILTER (WHERE actor_id=$2)
FROM v3_agent_run_feedback WHERE run_id=ANY($1::text[]) GROUP BY run_id`, ids, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var vote runFeedback
		if err = rows.Scan(&id, &vote.Up, &vote.Down, &vote.MyVote); err != nil {
			break
		}
		result[id] = &vote
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return result, err
}

func listRunSpanCounts(ctx context.Context, tx pgx.Tx, ids []string) (map[string]int64, error) {
	result := map[string]int64{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `SELECT run_id,count(*) FROM v3_agent_run_spans
WHERE run_id=ANY($1::text[]) GROUP BY run_id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var count int64
		if err = rows.Scan(&id, &count); err != nil {
			break
		}
		result[id] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return result, err
}

func buildRunsView(records []runRecord, feedback map[string]*runFeedback, steps map[string]int64, filter string, now time.Time) map[string]any {
	response := map[string]any{
		"kpis": []any{}, "summary": nil, "trend": nil, "durations": nil,
		"items": []runListItem{}, "counts": nil, "partial": true,
		"truncated": false, "aggregation_scope": "authorized_persisted_runs",
		"missing": []string{"live_run_producer", "span_coverage", "llm_messages", "tool_payloads", "scoring"},
	}
	if len(records) == 0 {
		return response
	}
	summary := runSummary{PersistedRuns: len(records)}
	counts := runCounts{All: len(records)}
	start := now.UTC().Truncate(time.Hour).Add(-23 * time.Hour)
	trend := runTrend{OK: make([]int, 24), Failed: make([]int, 24), Blocked: make([]int, 24),
		WindowStartAt: runTimeString(start), IntervalMinutes: 60}
	durations := make([]int64, 0)
	items := make([]runListItem, 0, 200)
	matching := 0
	for _, rec := range records {
		v := rec.Value
		vote := feedback[v.ID]
		negative := vote != nil && vote.Down > 0
		switch v.Status {
		case "success":
			summary.Success++
		case "failed":
			summary.Failed++
			counts.Failed++
		case "blocked":
			summary.Blocked++
			counts.Blocked++
		case "running":
			summary.Running++
		}
		if negative {
			summary.Negative++
			counts.Negative++
		}
		if vote != nil {
			summary.FeedbackVotes += vote.Up + vote.Down
		}
		if v.Status != "running" {
			if duration := runDuration(v); duration != nil {
				durations = append(durations, *duration)
			}
			if !v.Started.Before(start) && !v.Started.After(now) {
				bucket := int(v.Started.UTC().Sub(start) / time.Hour)
				switch v.Status {
				case "success":
					trend.OK[bucket]++
				case "failed":
					trend.Failed[bucket]++
				case "blocked":
					trend.Blocked[bucket]++
				}
			}
		}
		if filter != "all" && filter != v.Status && !(filter == "negative" && negative) {
			continue
		}
		matching++
		if len(items) >= 200 {
			continue
		}
		var stepCount *int64
		if count, reported := steps[v.ID]; reported {
			stepCount = &count
		}
		items = append(items, runListItem{
			ID: v.ID, Agent: rec.Agent.Name, AgentID: v.AgentID,
			ApplicationID: v.App, EnvironmentID: v.Env, Version: v.Version,
			Trigger: v.Trigger, Scope: v.Env + " / " + v.App, Status: v.Status,
			Steps: stepCount, Tokens: runTokens(v), DurationMS: runDuration(v),
			StartedAt: runTimeString(v.Started), Feedback: vote,
		})
	}
	if len(durations) > 0 {
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		avg := float64(0)
		for _, duration := range durations {
			avg += float64(duration)
		}
		avg /= float64(len(durations))
		summary.AvgDurationMS = &avg
		response["durations"] = durations
	}
	response["summary"] = summary
	response["counts"] = counts
	response["trend"] = trend
	response["items"] = items
	response["truncated"] = matching > 200
	response["kpis"] = []any{
		map[string]any{"id": "persisted_runs", "label": "Persisted runs", "value": strconv.Itoa(summary.PersistedRuns), "sub": "Authorized records", "series": []int{}},
		map[string]any{"id": "failed", "label": "Failed records", "value": strconv.Itoa(summary.Failed), "sub": "Persisted reports", "series": []int{}},
		map[string]any{"id": "blocked", "label": "Blocked records", "value": strconv.Itoa(summary.Blocked), "sub": "Persisted reports", "series": []int{}},
	}
	return response
}

func runStats(v run) []map[string]string {
	stats := []map[string]string{{"k": "status", "v": v.Status}}
	if v.Input != nil {
		stats = append(stats, map[string]string{"k": "input_tokens", "v": strconv.Itoa(*v.Input)})
	}
	if v.Output != nil {
		stats = append(stats, map[string]string{"k": "output_tokens", "v": strconv.Itoa(*v.Output)})
	}
	if v.ErrorCode != "" {
		stats = append(stats, map[string]string{"k": "error_code", "v": v.ErrorCode})
	}
	return stats
}

func (s *Service) RunDetail(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.readRunSnapshot(r)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	v, a := snapshot.Record.Value, snapshot.Record.Agent
	var steps any
	if len(snapshot.Spans) > 0 {
		reported := make([]map[string]any, 0)
		for _, sp := range snapshot.Spans {
			if sp.Kind != "llm" && sp.Kind != "tool" {
				continue
			}
			kind := "tool"
			if sp.Kind == "llm" {
				kind = "model"
			}
			item := map[string]any{"id": sp.ID, "icon": sp.Kind, "name": sp.Name,
				"start": runOffsetMS(sp.Started, v.Started), "dur": sp.Duration,
				"meta": sp.Status, "kind": kind, "connector": sp.ToolID}
			reported = append(reported, item)
		}
		if len(reported) > 0 {
			steps = reported
		}
	}
	var errorCode any
	if v.ErrorCode != "" {
		errorCode = v.ErrorCode
	}
	var finished any
	if v.Finished != nil {
		finished = runTimeString(*v.Finished)
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"id": v.ID, "agent": a.Name, "agent_id": a.ID,
		"application_id": v.App, "environment_id": v.Env,
		"version": v.Version, "trigger": v.Trigger, "status": v.Status,
		"error_code": errorCode, "started_at": runTimeString(v.Started), "finished_at": finished,
		"chain": []map[string]string{
			{"label": "Agent", "value": a.Name, "icon": "bot"},
			{"label": "Version", "value": v.Version, "icon": "version"}},
		"stats": runStats(v), "steps": steps, "total": runDuration(v),
		"context": nil, "budget": nil, "isolated": nil, "memory": nil,
		"feedback": snapshot.Feedback, "partial": true,
		"missing": []string{"live_run_producer", "span_coverage", "context_composition", "memory_hits", "tool_payloads"},
	})
}

func traceSpanDepth(spans []runSpan) (map[string]int, error) {
	byID := make(map[string]runSpan, len(spans))
	for _, sp := range spans {
		byID[sp.ID] = sp
	}
	depth, visiting := map[string]int{}, map[string]bool{}
	var resolve func(string) (int, error)
	resolve = func(id string) (int, error) {
		if d, seen := depth[id]; seen {
			return d, nil
		}
		if visiting[id] {
			return 0, errors.New("cyclic run span ancestry")
		}
		sp, exists := byID[id]
		if !exists {
			return 0, errors.New("missing run span parent")
		}
		visiting[id] = true
		d := 0
		if sp.Parent != "" {
			parentDepth, err := resolve(sp.Parent)
			if err != nil {
				return 0, err
			}
			d = parentDepth + 1
		}
		delete(visiting, id)
		depth[id] = d
		return d, nil
	}
	for _, sp := range spans {
		if _, err := resolve(sp.ID); err != nil {
			return nil, err
		}
	}
	return depth, nil
}

func (s *Service) RunTrace(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.readRunSnapshot(r)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	v := snapshot.Record.Value
	var reported any
	if len(snapshot.Spans) > 0 {
		depth, err := traceSpanDepth(snapshot.Spans)
		if err != nil {
			unavailable(w)
			return
		}
		spans := make([]map[string]any, 0, len(snapshot.Spans))
		for _, sp := range snapshot.Spans {
			spans = append(spans, map[string]any{
				"id": sp.ID, "parent_id": sp.Parent, "depth": depth[sp.ID],
				"kind": sp.Kind, "name": sp.Name, "meta": sp.Status,
				"start": runOffsetMS(sp.Started, v.Started), "dur": sp.Duration,
				"model_id": sp.ModelID, "tool_id": sp.ToolID,
				"token_input": sp.Input, "token_output": sp.Output,
			})
		}
		reported = spans
	}
	httpapi.WriteJSON(w, 200, map[string]any{
		"id": v.ID, "agent": snapshot.Record.Agent.Name, "status": v.Status,
		"stats": runStats(v), "scores": nil, "session": nil, "turns": nil,
		"total": runDuration(v), "spans": reported, "partial": true,
		"missing": []string{"live_run_producer", "span_coverage", "session_turns", "llm_messages", "tool_arguments", "tool_results", "scoring"},
	})
}

func (s *Service) RunFeedback(w http.ResponseWriter, r *http.Request) {
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
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !bounded(id, 128) {
		notFound(w)
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	v, err := lockRunRecord(r.Context(), tx, id, true)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if !contains(envs, v.Env) {
		notFound(w)
		return
	}
	a, err := lockRunAgent(r.Context(), tx, v)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	team, err := lockRunActiveScope(r.Context(), tx, v.App, v.Env)
	if err != nil {
		runReadFailure(w, err)
		return
	}
	if err = lockRunAuthority(r.Context(), tx, r); err != nil {
		runReadFailure(w, err)
		return
	}
	if err = s.authorizeRunTx(r.Context(), tx, r, runRecord{Value: v, Agent: a, Team: team}, "change"); err != nil {
		runReadFailure(w, err)
		return
	}
	if v.Status == "running" {
		httpapi.Error(w, 409, "run_active", "Feedback requires a completed run")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_run_feedback(run_id,actor_id,vote)
VALUES($1,$2,$3) ON CONFLICT (run_id,actor_id)
DO UPDATE SET vote=EXCLUDED.vote,created_at=clock_timestamp()`,
		v.ID, httpapi.CurrentUser(r).ID, body.Vote)
	if err != nil {
		unavailable(w)
		return
	}
	if err = audit(r.Context(), tx, a, httpapi.CurrentUser(r).ID, "v3.agent.run.feedback", v.ID,
		map[string]any{"environment_id": v.Env, "vote": body.Vote}); err != nil {
		unavailable(w)
		return
	}
	feedback, err := readRunFeedback(r.Context(), tx, v.ID, httpapi.CurrentUser(r).ID)
	if err != nil || feedback == nil {
		unavailable(w)
		return
	}
	if err = currentRunSession(r.Context(), tx, r); err != nil {
		runReadFailure(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"vote": body.Vote, "feedback": feedback})
}
