package operations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) Policy(ctx context.Context, env string) (Policy, error) {
	p := DefaultPolicy(env)
	var raw []byte
	var version int64
	e := s.DB.QueryRow(ctx, "SELECT policy,version FROM ops_policies WHERE environment_id=$1", env).Scan(&raw, &version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return p, e
	}
	exists := e == nil
	if exists {
		if e = json.Unmarshal(raw, &p); e != nil {
			return p, e
		}
		p.Version = version
	}
	if env != "workspace" && (!exists || p.Inherit) {
		inherited, e := s.Policy(ctx, "workspace")
		if e != nil {
			return p, e
		}
		inherited.EnvironmentID = env
		inherited.Inherit = true
		inherited.Version = version
		return inherited, nil
	}
	return p, nil
}
func (s *Service) SavePolicy(ctx context.Context, u core.User, p Policy, expected int64) (Policy, error) {
	if u.Role != "admin" {
		return p, store.ErrInvalidReference
	}
	if p.EnvironmentID != "workspace" {
		if _, e := s.environment(ctx, p.EnvironmentID); e != nil {
			return p, e
		}
	}
	if p.IntervalMinutes < 5 || p.IntervalMinutes > 1440 || p.MaxRounds < 1 || p.MaxRounds > 8 || p.MaxToolCalls < 1 || p.MaxToolCalls > 16 || p.TimeoutSeconds < 15 || p.TimeoutSeconds > 300 || p.DailyBudget < 1 || p.DailyBudget > 288 {
		return p, store.ErrInvalidReference
	}
	if p.EnvironmentID == "workspace" {
		p.Inherit = false
	}
	if !p.Inherit && (p.AutoInitial || p.Patrol || p.GenerateExperienceDraft) {
		if !s.modelExists(p.ModelID) || p.ActorID == "" {
			return p, store.ErrInvalidReference
		}
		var active bool
		if e := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled AND role IN('admin','operator'))", p.ActorID).Scan(&active); e != nil {
			return p, e
		}
		if !active {
			return p, store.ErrInvalidReference
		}
		if p.EnvironmentID != "workspace" {
			app, e := s.environment(ctx, p.EnvironmentID)
			if e != nil {
				return p, e
			}
			if _, e = s.actor(ctx, p.ActorID, app); e != nil {
				return p, e
			}
		}
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018997)"); e != nil {
		return p, e
	}
	var current int64
	e = tx.QueryRow(ctx, "SELECT version FROM ops_policies WHERE environment_id=$1 FOR UPDATE", p.EnvironmentID).Scan(&current)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return p, e
	}
	if current != expected {
		return p, store.ErrConflict
	}
	p.Version = current + 1
	b, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO ops_policies(environment_id,policy,version,next_due) VALUES($1,$2,$3,now()+($4*interval '1 minute')) ON CONFLICT(environment_id) DO UPDATE SET policy=excluded.policy,version=excluded.version,next_due=excluded.next_due", p.EnvironmentID, b, p.Version, p.IntervalMinutes); e != nil {
		return p, e
	}
	// Inherited environments use their own due row; reset next due when workspace
	// policy changes so disabled periods are not replayed as a catch-up storm.
	if p.EnvironmentID == "workspace" {
		if _, e = tx.Exec(ctx, "UPDATE ops_policies SET next_due=now()+($1*interval '1 minute') WHERE environment_id<>'workspace' AND COALESCE((policy->>'inherit')::boolean,false)", p.IntervalMinutes); e != nil {
			return p, e
		}
	}
	if _, e = tx.Exec(ctx, "INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'automation.policy.updated',$2,'success',$3)", u.ID, p.EnvironmentID, b); e != nil {
		return p, e
	}
	if e = tx.Commit(ctx); e != nil {
		return p, e
	}
	return s.Policy(ctx, p.EnvironmentID)
}
func (s *Service) AutomationRuns(ctx context.Context, u core.User, env string) ([]AutomationRun, error) {
	ids, e := s.API.VisibleApplicationIDs(ctx, u)
	if e != nil {
		return nil, e
	}
	rows, e := s.DB.Query(ctx, "SELECT r.id,r.environment_id,r.kind,r.status,r.incident_id,r.investigation_id,r.run_id,r.error,r.created_at,r.finished_at,r.result FROM ops_automation_runs r JOIN application_environments e ON e.id=r.environment_id WHERE e.application_id=ANY($1::text[]) AND ($2='' OR r.environment_id=$2) AND (cardinality($3::text[])=0 OR e.application_id=ANY($3::text[])) AND (cardinality($4::text[])=0 OR r.environment_id=ANY($4::text[])) ORDER BY r.created_at DESC LIMIT 200", ids, env, readScopeArrays(ctx).ApplicationIDs, readScopeArrays(ctx).EnvironmentIDs)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AutomationRun{}
	for rows.Next() {
		var v AutomationRun
		if e = rows.Scan(&v.ID, &v.EnvironmentID, &v.Kind, &v.Status, &v.IncidentID, &v.InvestigationID, &v.RunID, &v.Error, &v.CreatedAt, &v.FinishedAt, &v.Result); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) CancelAutomation(ctx context.Context, u core.User, id string) error {
	var app, run string
	e := s.DB.QueryRow(ctx, "SELECT e.application_id,r.run_id FROM ops_automation_runs r JOIN application_environments e ON e.id=r.environment_id WHERE r.id=$1", id).Scan(&app, &run)
	if e != nil {
		return store.ClassifyError(e)
	}
	if e = s.API.AuthorizeApplication(ctx, u, app, "operate"); e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, "UPDATE ops_automation_runs SET cancel_requested=true,status='cancelled',finished_at=now() WHERE id=$1 AND status IN('queued','running','waiting')", id)
	if e != nil {
		return e
	}
	if run != "" {
		_, e = s.Store.Cancel(store.WithPrincipal(ctx, u), run)
	}
	return e
}
