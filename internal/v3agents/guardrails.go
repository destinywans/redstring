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
	rows, err := s.DB.Query(r.Context(), `SELECT p.id,p.environment_id,p.name,p.description,p.forced,p.enabled,p.revision
 FROM v3_agent_guard_policies p WHERE p.environment_id=ANY($1) AND `+visiblePolicyGrantSQL+`
 ORDER BY p.name,p.id LIMIT 201`, envs, httpapi.CurrentUser(r).ID)
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
	policies := make([]any, 0)
	for i, p := range allPolicies {
		if i == 200 {
			break
		}
		policies = append(policies, map[string]any{"id": p.ID, "icon": "shield", "name": p.Name,
			"desc": p.Description, "stat": unknown, "enabled": p.Enabled, "forced": p.Forced,
			"revision": p.Revision})
	}
	events := make([]any, 0)
	rows2, err := s.DB.Query(r.Context(), `SELECT ge.id,ge.created_at,ge.observed_at,ge.policy_revision,
 ge.run_id,ge.policy_id,ge.action,
 ge.target_kind,ge.reason_code,gr.agent_id,gr.application_id,gr.environment_id,a.name
 FROM v3_agent_guard_events ge
 JOIN v3_agent_runs gr ON gr.id=ge.run_id JOIN v3_agents a ON a.id=gr.agent_id
 WHERE gr.environment_id=ANY($1) AND ($3='all' OR ge.action=$3) AND `+visibleAgentGrantSQL+`
 ORDER BY ge.created_at DESC,ge.id DESC LIMIT 201`, envs, httpapi.CurrentUser(r).ID, filter)
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
		events = append(events, map[string]any{"id": v.id, "time": timeString(eventTime), "agent": v.name,
			"recorded_at": timeString(v.at), "time_source": timeSource, "policy_revision": v.revision,
			"run": v.runID, "rule": v.policyID, "action": v.action, "detail": v.reason, "target_kind": v.target})
	}
	httpapi.WriteJSON(w, 200, map[string]any{"policies": policies, "trend": nil, "events": events,
		"counts": nil, "detail": nil, "partial": true, "truncated": len(allEvents) > 200 || len(allPolicies) > 200,
		"missing": []string{"guard_event_aggregate", "masked_payload", "full_source_content"}})
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
	_, err = tx.Exec(r.Context(), `INSERT INTO management_audit
 (actor_id,actor_kind,application_id,action,target,outcome,changes)
	 VALUES($1,'user','','v3.agent.guardrail.changed',$2,'recorded',jsonb_build_object(
 'environment_id',$3::text,'from',$4::boolean,'to',$5::boolean,
 'revision_from',$6::bigint,'revision_to',$7::bigint))`,
		httpapi.CurrentUser(r).ID, p.ID, p.Env, p.Enabled, *body.Enabled, p.Revision, nextRevision)
	if err != nil {
		unavailable(w)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		unavailable(w)
		return
	}
	httpapi.WriteJSON(w, 200, map[string]any{"id": p.ID, "enabled": *body.Enabled, "revision": nextRevision})
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

// GuardPolicySnapshot is the DB-backed state captured when a trusted guard
// producer makes a decision. ObservedAt is the database decision time.
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

// RecordGuardEvent accepts a bounded code and the policy snapshot captured
// at decision time. Reporting may be late, but unproven historical decisions
// are rejected rather than inferred from the current enabled flag.
func (s *Service) RecordGuardEvent(ctx context.Context, in GuardEvent) error {
	if s == nil || s.DB == nil || !bounded(in.ID, 128) || !bounded(in.RunID, 128) || !bounded(in.PolicyID, 128) ||
		!safeAuditCode(in.ReasonCode, 80) || (in.Action != "blocked" && in.Action != "masked") ||
		(in.TargetKind != "tool" && in.TargetKind != "output" && in.TargetKind != "input" && in.TargetKind != "retrieval") ||
		in.PolicyRevision < 1 || in.ObservedAt.IsZero() {
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
	var current time.Time
	err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&current)
	if err != nil {
		return err
	}
	if in.ObservedAt.After(current.Add(time.Second)) ||
		in.ObservedAt.Before(runStarted) || (runFinished != nil && in.ObservedAt.After(*runFinished)) {
		return errors.New("guard event decision time outside run or reporting window")
	}
	var enabled bool
	var validFrom time.Time
	var validTo *time.Time
	err = tx.QueryRow(ctx, `SELECT p.environment_id,pr.enabled,pr.valid_from,pr.valid_to
 FROM v3_agent_guard_policies p JOIN v3_agent_guard_policy_revisions pr
 ON pr.policy_id=p.id WHERE p.id=$1 AND pr.revision=$2 FOR SHARE OF p,pr`,
		in.PolicyID, in.PolicyRevision).Scan(&policyEnv, &enabled, &validFrom, &validTo)
	if err != nil {
		return err
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
