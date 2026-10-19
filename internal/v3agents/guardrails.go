package v3agents

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

type guardPolicy struct {
	ID, Env, Name, Description string
	Forced, Enabled            bool
	Revision                   int64
}

func (s *Service) Guardrails(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if filter != "all" && filter != "blocked" && filter != "masked" {
		invalid(w, "Unknown guardrail filter")
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
	ctx := r.Context()
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(ctx)
	var dbNow time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		unavailable(w)
		return
	}
	utc := dbNow.UTC()
	today := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	start, end := today.AddDate(0, 0, -6), today.AddDate(0, 0, 1)
	// The buckets are UTC calendar days; trend_starts_at are the instants
	// each bucket starts, in RFC 3339 (PA-83; the "2006-01-02" trend_days
	// were removed).
	trendStarts := make([]string, 7)
	trend := make([][]int64, 7)
	dayIndex := make(map[string]int, 7)
	for i := range trend {
		day := start.AddDate(0, 0, i)
		trendStarts[i] = day.Format(time.RFC3339)
		dayIndex[day.Format("2006-01-02")] = i
		trend[i] = []int64{0, 0}
	}
	actorID := httpapi.CurrentUser(r).ID
	rows, err := tx.Query(ctx, `SELECT p.id,p.environment_id,p.name,p.description,p.forced,p.enabled,p.revision
 FROM v3_agent_guard_policies p WHERE p.environment_id=ANY($1) AND `+visiblePolicyGrantSQL+`
 ORDER BY p.name,p.id LIMIT 201`, envs, actorID)
	if err != nil {
		unavailable(w)
		return
	}
	allPolicies := make([]guardPolicy, 0)
	for rows.Next() {
		var p guardPolicy
		if err = rows.Scan(&p.ID, &p.Env, &p.Name, &p.Description, &p.Forced, &p.Enabled, &p.Revision); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		allPolicies = append(allPolicies, p)
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	// Aggregate the entire visible, revision-backed reporting set before the
	// 200-row display limit. Legacy reports lack a trusted decision time and do
	// not establish a day bucket or a policy's enabled state at that time.
	counts := map[string]int64{"all": 0, "blocked": 0, "masked": 0}
	policyReports := make(map[string]int64)
	rows, err = tx.Query(ctx, `SELECT ge.policy_id,
 to_char(ge.observed_at AT TIME ZONE 'UTC','YYYY-MM-DD'),ge.action,count(*)::bigint
 FROM v3_agent_guard_events ge
 JOIN v3_agent_runs gr ON gr.id=ge.run_id
 JOIN v3_agents a ON a.id=gr.agent_id AND a.application_id=gr.application_id
 AND a.environment_id=gr.environment_id
 JOIN v3_agent_guard_policies p ON p.id=ge.policy_id AND p.environment_id=gr.environment_id
 JOIN v3_agent_guard_policy_revisions pr ON pr.policy_id=ge.policy_id
 AND pr.revision=ge.policy_revision
 WHERE gr.environment_id=ANY($1) AND ge.observed_at >= $3 AND ge.observed_at < $4
 AND ge.policy_revision IS NOT NULL
 AND pr.enabled AND ge.observed_at >= pr.valid_from
 AND (pr.valid_to IS NULL OR ge.observed_at < pr.valid_to)
 AND `+visibleAgentGrantSQL+` AND `+visiblePolicyGrantSQL+`
 GROUP BY ge.policy_id,2,ge.action`, envs, actorID, start, end)
	if err != nil {
		unavailable(w)
		return
	}
	for rows.Next() {
		var policyID, day, action string
		var n int64
		if err = rows.Scan(&policyID, &day, &action, &n); err != nil {
			rows.Close()
			unavailable(w)
			return
		}
		i, found := dayIndex[day]
		if !found || (action != "blocked" && action != "masked") {
			rows.Close()
			unavailable(w)
			return
		}
		counts["all"] += n
		counts[action] += n
		policyReports[policyID] += n
		if action == "blocked" {
			trend[i][0] += n
		} else {
			trend[i][1] += n
		}
	}
	if rows.Err() != nil {
		rows.Close()
		unavailable(w)
		return
	}
	rows.Close()
	policies := make([]any, 0)
	for i, p := range allPolicies {
		if i == 200 {
			break
		}
		policies = append(policies, map[string]any{"id": p.ID, "icon": "shield", "name": p.Name,
			"desc": guardDescriptionFor(r, p.Name, p.Description), "stat": unknown, "enabled": p.Enabled, "forced": p.Forced,
			"revision": p.Revision, "reported_events_7d": policyReports[p.ID]})
	}
	events := make([]any, 0)
	rows2, err := tx.Query(ctx, `SELECT ge.id,ge.created_at,
 CASE WHEN ge.observed_at IS NOT NULL AND pr.enabled
 AND ge.observed_at >= pr.valid_from AND (pr.valid_to IS NULL OR ge.observed_at < pr.valid_to)
 THEN ge.observed_at ELSE NULL END,
 CASE WHEN ge.observed_at IS NOT NULL AND pr.enabled
 AND ge.observed_at >= pr.valid_from AND (pr.valid_to IS NULL OR ge.observed_at < pr.valid_to)
 THEN ge.policy_revision ELSE NULL END,
 ge.run_id,ge.policy_id,ge.action,
 ge.target_kind,ge.reason_code,gr.agent_id,gr.application_id,gr.environment_id,a.name
 FROM v3_agent_guard_events ge JOIN v3_agent_runs gr ON gr.id=ge.run_id
 JOIN v3_agents a ON a.id=gr.agent_id AND a.application_id=gr.application_id
 AND a.environment_id=gr.environment_id
 JOIN v3_agent_guard_policies p ON p.id=ge.policy_id AND p.environment_id=gr.environment_id
 LEFT JOIN v3_agent_guard_policy_revisions pr ON pr.policy_id=ge.policy_id
 AND pr.revision=ge.policy_revision
 WHERE gr.environment_id=ANY($1) AND ($3='all' OR ge.action=$3)
 AND `+visibleAgentGrantSQL+` AND `+visiblePolicyGrantSQL+`
 ORDER BY ge.created_at DESC,ge.id DESC LIMIT 201`, envs, actorID, filter)
	if err != nil {
		unavailable(w)
		return
	}
	type eventRow struct {
		id, runID, policyID, action, target, reason, agentID, app, env, name string
		at                                                                   time.Time
		observed                                                             *time.Time
		revision                                                             *int64
	}
	allEvents := make([]eventRow, 0)
	for rows2.Next() {
		var v eventRow
		if err = rows2.Scan(&v.id, &v.at, &v.observed, &v.revision, &v.runID, &v.policyID, &v.action, &v.target, &v.reason, &v.agentID, &v.app, &v.env, &v.name); err != nil {
			rows2.Close()
			unavailable(w)
			return
		}
		allEvents = append(allEvents, v)
	}
	if rows2.Err() != nil {
		rows2.Close()
		unavailable(w)
		return
	}
	rows2.Close()
	var detail any
	for i, v := range allEvents {
		if i == 200 {
			break
		}
		eventTime := v.at
		timeSource := "recorded_only"
		if v.observed != nil {
			eventTime = *v.observed
			timeSource = "policy_decision"
		}
		item := map[string]any{"id": v.id, "time": timeString(eventTime), "agent": v.name,
			"recorded_at": timeString(v.at), "time_source": timeSource, "policy_revision": v.revision,
			"run": v.runID, "rule": v.policyID, "action": v.action, "detail": v.reason, "target_kind": v.target}
		events = append(events, item)
		if detail == nil {
			detail = map[string]any{"id": v.id, "title": v.action + " / " + v.target,
				"meta": v.name + " / " + v.runID, "time": timeString(eventTime),
				"recorded_at": timeString(v.at), "time_source": timeSource,
				"policy_revision": v.revision, "agent": v.name, "run": v.runID,
				"rule": v.policyID, "action": v.action, "target_kind": v.target,
				"reason_code": v.reason, "source": nil, "snippet": nil,
				"masked": nil, "verdict": nil}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"policies": policies, "trend": trend,
		"trend_starts_at": trendStarts, "events": events, "counts": counts, "detail": detail,
		"aggregation_basis": "reported_events", "coverage": "unknown", "partial": true,
		"manual_execution_connected": s.ExecutionRuntime() != nil, "execution_scope": "governed_manual_runs",
		"truncated": len(allEvents) > 200 || len(allPolicies) > 200,
		"missing": []string{"runtime_producer_coverage", "legacy_decision_time",
			"full_source_content", "masked_payload", "verdict"}})
}

func (s *Service) Guardrail(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil || s.Access == nil {
		unavailable(w)
		return
	}
	envs, ok := requestScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !httpapi.Decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		invalid(w, "enabled is required")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		unavailable(w)
		return
	}
	defer tx.Rollback(r.Context())
	var p guardPolicy
	err = tx.QueryRow(r.Context(), `SELECT id,environment_id,name,description,forced,enabled,revision
 FROM v3_agent_guard_policies WHERE id=$1 FOR UPDATE`, r.PathValue("id")).
		Scan(&p.ID, &p.Env, &p.Name, &p.Description, &p.Forced, &p.Enabled, &p.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		notFound(w)
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if !contains(envs, p.Env) {
		notFound(w)
		return
	}
	err = s.Access.AuthorizeWithTx(r.Context(), tx, httpapi.CurrentUser(r), rbac.V3Decision{
		Module: "agents", Action: "manage", EnvironmentID: p.Env})
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Guardrail permission required")
		return
	}
	if err != nil {
		unavailable(w)
		return
	}
	if p.Forced && !*body.Enabled {
		httpapi.Error(w, 409, "forced_guardrail", "Forced guardrail cannot be disabled")
		return
	}
	nextRevision := p.Revision
	if p.Enabled != *body.Enabled {
		var validFrom, changedAt time.Time
		err = tx.QueryRow(r.Context(), `SELECT valid_from FROM v3_agent_guard_policy_revisions
 WHERE policy_id=$1 AND revision=$2 AND valid_to IS NULL FOR UPDATE`, p.ID, p.Revision).Scan(&validFrom)
		if err != nil {
			unavailable(w)
			return
		}
		err = tx.QueryRow(r.Context(), `SELECT GREATEST(clock_timestamp(),$1::timestamptz + interval '1 microsecond')`, validFrom).Scan(&changedAt)
		if err != nil {
			unavailable(w)
			return
		}
		tag, closeErr := tx.Exec(r.Context(), `UPDATE v3_agent_guard_policy_revisions SET valid_to=$3
 WHERE policy_id=$1 AND revision=$2 AND valid_to IS NULL`, p.ID, p.Revision, changedAt)
		if closeErr != nil || tag.RowsAffected() != 1 {
			unavailable(w)
			return
		}
		nextRevision++
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_agent_guard_policy_revisions
 (policy_id,revision,enabled,valid_from) VALUES($1,$2,$3,$4)`, p.ID, nextRevision, *body.Enabled, changedAt)
		if err != nil {
			unavailable(w)
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE v3_agent_guard_policies SET enabled=$2,revision=$3,
 updated_by=$4,updated_at=$5 WHERE id=$1`, p.ID, *body.Enabled, nextRevision, httpapi.CurrentUser(r).ID, changedAt)
		if err != nil {
			unavailable(w)
			return
		}
	}
	auditAction := "v3.agent.guardrail.changed"
	if p.Enabled == *body.Enabled {
		auditAction = "v3.agent.guardrail.unchanged"
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
	 VALUES($1,'user','',$8::text,$2,'recorded',jsonb_build_object(
 'environment_id',$3::text,'from',$4::boolean,'to',$5::boolean,
 'revision_from',$6::bigint,'revision_to',$7::bigint))`,
		httpapi.CurrentUser(r).ID, p.ID, p.Env, p.Enabled, *body.Enabled, p.Revision,
		nextRevision, auditAction)
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": p.ID, "enabled": *body.Enabled,
		"revision": nextRevision, "runtime_status": s.executionGuardRuntimeStatus(),
		"manual_execution_connected": s.ExecutionRuntime() != nil, "execution_scope": "governed_manual_runs"})
}

// RegisterGuardPolicy imports reviewed policy metadata. Forced policies are
// always enabled; application writes never gain execution authority here.
func (s *Service) RegisterGuardPolicy(ctx context.Context, id, env, name, description string, forced bool) error {
	if s == nil || s.DB == nil || !bounded(id, 128) || !bounded(env, 128) || !bounded(name, 160) || len([]rune(description)) > 1000 {
		return errors.New("invalid guard policy")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var known bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_application_environments WHERE environment_id=$1)`, env).Scan(&known)
	if err != nil {
		return err
	}
	if !known {
		return errors.New("unknown guard policy environment")
	}
	tag, err := tx.Exec(ctx, `INSERT INTO v3_agent_guard_policies
 (id,environment_id,name,description,forced,enabled) VALUES($1,$2,$3,$4,$5,true)
 ON CONFLICT (id) DO NOTHING`, id, env, name, description, forced)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		_, err = tx.Exec(ctx, `INSERT INTO v3_agent_guard_policy_revisions(policy_id,revision,enabled,valid_from)
 SELECT id,revision,enabled,updated_at FROM v3_agent_guard_policies WHERE id=$1`, id)
		if err != nil {
			return err
		}
	}
	var existingEnv, existingName, existingDescription string
	var existingForced bool
	err = tx.QueryRow(ctx, `SELECT environment_id,name,description,forced FROM v3_agent_guard_policies WHERE id=$1`, id).
		Scan(&existingEnv, &existingName, &existingDescription, &existingForced)
	if err != nil {
		return err
	}
	if existingEnv != env || existingName != name || existingDescription != description || existingForced != forced {
		return errors.New("guard policy scope conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,actor_kind,application_id,action,target,outcome,changes)
 VALUES('system','system','','v3.agent.guardrail.registered',$1,'recorded',
 jsonb_build_object('environment_id',$2::text,'forced',$3::boolean))`, id, env, forced)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GuardPolicySnapshot is DB-backed policy state. ObservedAt is the snapshot
// handoff time, not proof of when a later guard action completed. A synchronous
// runtime reports the completed decision through ReportGuardDecision instead.
type GuardPolicySnapshot struct {
	PolicyID, EnvironmentID string
	Revision                int64
	Enabled                 bool
	ObservedAt              time.Time
}

func (s *Service) PolicySnapshot(ctx context.Context, policyID, environmentID string) (GuardPolicySnapshot, error) {
	var out GuardPolicySnapshot
	if s == nil || s.DB == nil || !bounded(policyID, 128) || !bounded(environmentID, 128) {
		return out, errors.New("invalid guard policy scope")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT environment_id,revision,enabled FROM v3_agent_guard_policies
 WHERE id=$1 AND environment_id=$2 FOR SHARE`, policyID, environmentID).
		Scan(&out.EnvironmentID, &out.Revision, &out.Enabled)
	if err != nil {
		return GuardPolicySnapshot{}, err
	}
	out.PolicyID = policyID
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt); err != nil {
		return GuardPolicySnapshot{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return GuardPolicySnapshot{}, err
	}
	return out, nil
}

type GuardEvent struct {
	ID, RunID, PolicyID, Action, TargetKind, ReasonCode string
	PolicyRevision                                      int64
	ObservedAt                                          time.Time
}

// RecordGuardEvent accepts a trusted producer's actual decision timestamp for
// delayed reporting. Unproven historical decisions are rejected rather than
// inferred from the current enabled flag. A synchronous runtime should call
// ReportGuardDecision so the database captures its final decision time.
func (s *Service) RecordGuardEvent(ctx context.Context, in GuardEvent) error {
	return s.recordGuardEvent(ctx, in, false)
}

// ReportGuardDecision is the synchronous guard-report hook. The caller invokes
// it at the final decision point before exposing a blocked/masked outcome. It
// captures DB time after any guard evaluation, then checks that the same
// policy revision was enabled at that time. A stale revision fails closed and
// must be reevaluated by the executor.
func (s *Service) ReportGuardDecision(ctx context.Context, in GuardEvent) error {
	if !in.ObservedAt.IsZero() {
		return errors.New("guard decision time must be captured by the database")
	}
	return s.recordGuardEvent(ctx, in, true)
}

func (s *Service) recordGuardEvent(ctx context.Context, in GuardEvent, captureNow bool) error {
	if s == nil || s.DB == nil || !bounded(in.ID, 128) || !bounded(in.RunID, 128) || !bounded(in.PolicyID, 128) ||
		!safeAuditCode(in.ReasonCode, 80) || (in.Action != "blocked" && in.Action != "masked") ||
		(in.TargetKind != "tool" && in.TargetKind != "output" && in.TargetKind != "input" && in.TargetKind != "retrieval") ||
		in.PolicyRevision < 1 || (!captureNow && in.ObservedAt.IsZero()) {
		return errors.New("invalid guard event")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var agentID, env, policyEnv string
	var runStarted time.Time
	var runFinished *time.Time
	err = tx.QueryRow(ctx, `SELECT agent_id,environment_id,started_at,finished_at
 FROM v3_agent_runs WHERE id=$1 FOR SHARE`, in.RunID).
		Scan(&agentID, &env, &runStarted, &runFinished)
	if err != nil {
		return err
	}
	var enabled, currentEnabled bool
	var currentRevision int64
	var validFrom time.Time
	var validTo *time.Time
	err = tx.QueryRow(ctx, `SELECT p.environment_id,p.revision,p.enabled,
 pr.enabled,pr.valid_from,pr.valid_to
 FROM v3_agent_guard_policies p JOIN v3_agent_guard_policy_revisions pr
 ON pr.policy_id=p.id WHERE p.id=$1 AND pr.revision=$2 FOR SHARE OF p,pr`,
		in.PolicyID, in.PolicyRevision).Scan(&policyEnv, &currentRevision, &currentEnabled,
		&enabled, &validFrom, &validTo)
	if err != nil {
		return err
	}
	// Capture time only after the policy lock. A concurrent PATCH either waits
	// for this decision to commit or is observed as the new revision here.
	var current time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&current); err != nil {
		return err
	}
	if captureNow {
		in.ObservedAt = current
		if currentRevision != in.PolicyRevision || !currentEnabled {
			return errors.New("guard decision used a stale policy revision")
		}
	}
	if in.ObservedAt.After(current.Add(time.Second)) ||
		in.ObservedAt.Before(runStarted) || (runFinished != nil && in.ObservedAt.After(*runFinished)) {
		return errors.New("guard event decision time outside run or reporting window")
	}
	if policyEnv != env || !enabled || in.ObservedAt.Before(validFrom) ||
		(validTo != nil && !in.ObservedAt.Before(*validTo)) {
		return errors.New("guard policy was not enabled at decision time")
	}
	a, err := scanAgent(tx.QueryRow(ctx, `SELECT `+agentColumns+` FROM v3_agents WHERE id=$1`, agentID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_agent_guard_events
 (id,run_id,policy_id,action,target_kind,reason_code,observed_at,policy_revision)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.ID, in.RunID, in.PolicyID, in.Action,
		in.TargetKind, in.ReasonCode, in.ObservedAt, in.PolicyRevision)
	if err != nil {
		return err
	}
	if err = audit(ctx, tx, a, "system", "v3.agent.guard.event", in.ID,
		map[string]any{"environment_id": env, "policy_id": in.PolicyID, "action": in.Action,
			"target_kind": in.TargetKind, "reason_code": in.ReasonCode,
			"policy_revision": in.PolicyRevision, "observed_at": timeString(in.ObservedAt)}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
