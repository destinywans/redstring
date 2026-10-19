// Package actions separates human authority and external writes from inference.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
	"log/slog"
	"time"
)

type Connector interface {
	ProposeRollback(context.Context, string) (connectors.RollbackPlan, error)
	Execute(context.Context, connectors.RollbackPlan, string) (connectors.ExecutionResult, error)
	Reconcile(context.Context, connectors.RollbackPlan, string) (connectors.ExecutionResult, error)
	ObservePlan(context.Context, connectors.RollbackPlan, time.Duration) (connectors.RecoveryResult, error)
	Snapshot(context.Context) (connectors.Snapshot, error)
}
type Plan struct {
	Decision        string                  `json:"decision"`
	DecisionBy      string                  `json:"decision_by,omitempty"`
	DecisionReason  string                  `json:"decision_reason,omitempty"`
	DecidedAt       *time.Time              `json:"decided_at,omitempty"`
	ID              string                  `json:"id"`
	InvestigationID string                  `json:"investigation_id"`
	CreatedBy       string                  `json:"created_by"`
	CreatedAt       time.Time               `json:"created_at"`
	ApprovedBy      string                  `json:"approved_by,omitempty"`
	ApprovedAt      *time.Time              `json:"approved_at,omitempty"`
	Proposal        connectors.RollbackPlan `json:"proposal"`
}
type Execution struct {
	ID        string          `json:"id"`
	PlanID    string          `json:"plan_id"`
	Status    string          `json:"status"`
	Attempt   int             `json:"attempt"`
	Error     string          `json:"error,omitempty"`
	Result    json.RawMessage `json:"result"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}
type Lease struct {
	Execution
	Previous string
	Token    string
}

// Repository captures transactional business operations, not SQL or generic rows.
// Implementations must commit each transition and its audit event atomically,
// serialize digest approval/unique execution, and fence every leased mutation.
type Repository interface {
	Migrate(context.Context) error
	Application(context.Context, string) (string, error)
	CreatePlan(context.Context, string, connectors.RollbackPlan, string) (Plan, error)
	Plan(context.Context, string) (Plan, error)
	Plans(context.Context, string) ([]Plan, error)
	Approve(context.Context, string, string, string) (Plan, error)
	Decide(context.Context, string, string, string, string, string) (Plan, error)
	CloseInvestigation(context.Context, string, string, string, string) (Report, error)
	Report(context.Context, string) (Report, error)
	Enqueue(context.Context, string, string) (Execution, error)
	Execution(context.Context, string) (Execution, error)
	ExecutionForPlan(context.Context, string) (Execution, error)
	Claim(context.Context) (Lease, error)
	Transition(context.Context, Lease, string, string, any) error
	Renew(context.Context, Lease) error
}

var ErrNoWork = errors.New("no eligible execution")

type claim = Lease
type Service struct {
	Authorize     func(context.Context, string, string, string) error
	AuthorizePlan func(context.Context, string, Plan, string) error
	Repository    Repository
	Connector     Connector
	ApplicationID string
	Resolve       func(context.Context, string, *connectors.RollbackPlan) (Connector, error)
}

func (s *Service) authorizePlan(ctx context.Context, user string, p Plan, action string) error {
	if s.AuthorizePlan != nil {
		return s.AuthorizePlan(ctx, user, p, action)
	}
	if s.Authorize != nil {
		return s.Authorize(ctx, user, p.InvestigationID, action)
	}
	return nil
}
func NewWithRepository(r Repository, c Connector, app string) *Service {
	return &Service{Repository: r, Connector: c, ApplicationID: app}
}
func (s *Service) Migrate(ctx context.Context) error { return s.Repository.Migrate(ctx) }
func (s *Service) Create(ctx context.Context, inv, revision, user string) (Plan, error) {
	if s.Authorize != nil {
		if e := s.Authorize(ctx, user, inv, "investigate"); e != nil {
			return Plan{}, e
		}
	}
	app, e := s.Repository.Application(ctx, inv)
	if e != nil {
		return Plan{}, e
	}
	connector := s.Connector
	stamp := connectors.RollbackPlan{}
	if s.Resolve != nil {
		connector, e = s.Resolve(ctx, inv, &stamp)
		if e != nil {
			return Plan{}, e
		}
	}
	if connector == nil || (s.Resolve == nil && app != s.ApplicationID) {
		return Plan{}, errors.New("no action connector registered for this application")
	}
	p, e := connector.ProposeRollback(ctx, revision)
	if e != nil {
		return Plan{}, e
	}
	p.EnvironmentID = stamp.EnvironmentID
	p.ConnectorID = stamp.ConnectorID
	p.BindingRevision = stamp.BindingRevision
	p.ConfigurationDigest = stamp.ConfigurationDigest
	connectors.SealPlan(&p)
	return s.Repository.CreatePlan(ctx, inv, p, user)
}
func (s *Service) Plan(ctx context.Context, id string) (Plan, error) {
	return s.Repository.Plan(ctx, id)
}
func (s *Service) Plans(ctx context.Context, inv string) ([]Plan, error) {
	return s.Repository.Plans(ctx, inv)
}
func (s *Service) Approve(ctx context.Context, id, digest, user string) (Plan, error) {
	if s.Authorize != nil || s.AuthorizePlan != nil {
		p, e := s.Plan(ctx, id)
		if e != nil {
			return Plan{}, e
		}
		if e = s.authorizePlan(ctx, user, p, "approve"); e != nil {
			return Plan{}, e
		}
	}
	if s.Resolve != nil {
		p, e := s.Plan(ctx, id)
		if e != nil {
			return Plan{}, e
		}
		if _, e = s.Resolve(ctx, p.InvestigationID, &p.Proposal); e != nil {
			return Plan{}, e
		}
	}
	return s.Repository.Approve(ctx, id, digest, user)
}
func (s *Service) Enqueue(ctx context.Context, id, user string) (Execution, error) {
	if s.Authorize != nil || s.AuthorizePlan != nil {
		p, e := s.Plan(ctx, id)
		if e != nil {
			return Execution{}, e
		}
		if e = s.authorizePlan(ctx, user, p, "execute"); e != nil {
			return Execution{}, e
		}
	}
	if s.Resolve != nil {
		p, e := s.Plan(ctx, id)
		if e != nil {
			return Execution{}, e
		}
		if p.Proposal.EnvironmentID == "" {
			return Execution{}, store.ErrConflict
		}
		if _, e = s.Resolve(ctx, p.InvestigationID, &p.Proposal); e != nil {
			return Execution{}, e
		}
	}
	return s.Repository.Enqueue(ctx, id, user)
}
func (s *Service) Execution(ctx context.Context, id string) (Execution, error) {
	return s.Repository.Execution(ctx, id)
}
func (s *Service) claim(ctx context.Context) (Lease, error) { return s.Repository.Claim(ctx) }
func (s *Service) transition(ctx context.Context, c Lease, state, msg string, result any) error {
	return s.Repository.Transition(ctx, c, state, msg, result)
}
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if s.Connector != nil || s.Resolve != nil {
			c, e := s.claim(ctx)
			if e == nil {
				s.perform(ctx, c)
				continue
			}
			if !errors.Is(e, ErrNoWork) && ctx.Err() == nil {
				slog.Error("action worker claim failed", "error", e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Service) perform(parent context.Context, c claim) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Minute)
	defer cancel()
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		tick := time.NewTicker(8 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if e := s.Repository.Renew(ctx, c); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-hbDone }()
	p, e := s.Plan(ctx, c.PlanID)
	if e != nil {
		return
	}
	finish := func(state, msg string, result any) {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if e := s.transition(cleanup, c, state, msg, result); e != nil && !errors.Is(e, store.ErrLease) {
			slog.Error("action transition failed", "error", e)
		}
	}
	if s.Authorize != nil || s.AuthorizePlan != nil {
		if e = s.authorizePlan(ctx, p.ApprovedBy, p, "approve"); e != nil {
			finish("needs_attention", "Approval principal no longer has approve permission.", nil)
			return
		}
		actors, ok := s.Repository.(interface {
			ExecutionActor(context.Context, string) (string, error)
		})
		if !ok {
			finish("needs_attention", "Execution principal could not be verified.", nil)
			return
		}
		actor, err := actors.ExecutionActor(ctx, c.ID)
		if err != nil || s.authorizePlan(ctx, actor, p, "execute") != nil {
			finish("needs_attention", "Execution principal no longer has execute permission.", nil)
			return
		}
	}
	if s.Resolve != nil {
		local := *s
		resolveCtx := store.WithPrincipal(ctx, core.User{ID: p.ApprovedBy})
		if p.Proposal.EnvironmentID == "" {
			finish("needs_attention", "Legacy plan has no immutable environment binding; create a new proposal.", nil)
			return
		}
		if actors, ok := s.Repository.(interface {
			ExecutionActor(context.Context, string) (string, error)
		}); ok {
			actor, e := actors.ExecutionActor(ctx, c.ID)
			if e != nil {
				finish("needs_attention", "Execution principal could not be verified.", nil)
				return
			}
			if _, e = s.Resolve(store.WithPrincipal(ctx, core.User{ID: actor}), p.InvestigationID, &p.Proposal); e != nil {
				finish("needs_attention", "Execution requester permission or target binding changed.", nil)
				return
			}
		}
		local.Connector, e = s.Resolve(resolveCtx, p.InvestigationID, &p.Proposal)
		if e != nil {
			finish("needs_attention", "Application permission or connector binding changed; review before retry.", nil)
			return
		}
		s = &local
	}
	if c.Attempt > 3 {
		finish("needs_attention", "Recovery attempts exhausted; inspect actual target before a new plan.", nil)
		return
	}
	var applied connectors.ExecutionResult
	if c.Previous == "queued" {
		applied, e = s.Connector.Execute(ctx, p.Proposal, c.ID)
	} else {
		applied, e = s.Connector.Reconcile(ctx, p.Proposal, c.ID)
	}
	if errors.Is(e, connectors.ErrAmbiguous) {
		applied, e = s.Connector.Reconcile(ctx, p.Proposal, c.ID)
	}
	// Cancellation leaves a leased stage recoverable; no new external write is
	// attempted by recovery. The next claim reconciles the same operation marker.
	if ctx.Err() != nil {
		return
	}
	if e != nil || applied.State != "applied" {
		message := "External outcome needs review; no automatic write retry."
		if errors.Is(e, connectors.ErrDrift) {
			message = "Deployment changed; create and approve a new plan."
		}
		finish("needs_attention", message, applied)
		return
	}
	if e = s.transition(ctx, c, "verifying", "", applied); e != nil {
		return
	}
	if e = s.waitReady(ctx, p.Proposal); e != nil {
		if ctx.Err() == nil {
			finish("failed", "Deployment did not converge to the approved state.", applied)
		}
		return
	}
	recovery, e := s.Connector.ObservePlan(ctx, p.Proposal, time.Duration(p.Proposal.ObservationSeconds)*time.Second)
	if ctx.Err() != nil {
		return
	}
	if e != nil {
		finish("needs_attention", "Recovery observation failed; inspect target and telemetry.", recovery)
		return
	}
	if !recovery.Recovered {
		finish("failed", recovery.Reason, recovery)
		return
	}
	finish("succeeded", "", recovery)
}
func (s *Service) waitReady(ctx context.Context, p connectors.RollbackPlan) error {
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		snap, e := s.Connector.Snapshot(ctx)
		if e != nil {
			return e
		}
		d := snap.Deployment
		if d.Metadata.UID != p.UID || snap.TemplateDigest != p.TargetTemplateDigest {
			return connectors.ErrDrift
		}
		if d.Status.ObservedGeneration >= d.Metadata.Generation && d.Status.UpdatedReplicas == d.Spec.Replicas && d.Status.AvailableReplicas == d.Spec.Replicas && d.Status.Replicas == d.Spec.Replicas && d.Spec.Replicas > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("rollout timeout")
		case <-ticker.C:
		}
	}
}
