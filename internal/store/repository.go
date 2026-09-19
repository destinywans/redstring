package store

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/core"
)

var ErrNoWork = errors.New("no queued work")
var ErrLastAdmin = errors.New("at least one active administrator required")
var ErrInvalidReference = errors.New("invalid or duplicate resource")

type AccountUpdate struct {
	Name     *string `json:"name"`
	Role     *string `json:"role"`
	Password *string `json:"password"`
	Disabled *bool   `json:"disabled"`
}

// IdentityRepository owns account invariants and credential/session transactions.
// Adapters must revoke sessions atomically when credentials or permissions change.
type IdentityRepository interface {
	Login(context.Context, string, string) (core.User, string, error)
	Session(context.Context, string) (core.User, error)
	Logout(context.Context, string) error
	Users(context.Context) ([]core.User, error)
	CreateUser(context.Context, core.User, string) (core.User, error)
	UpdateAccount(context.Context, string, AccountUpdate) (core.User, error)
}

// InvestigationRepository commits context changes and their durable events as one
// transaction. Per-investigation event sequence order must match commit order.
type InvestigationRepository interface {
	Applications(context.Context) ([]core.Application, error)
	RegisterApplication(context.Context, core.Application) (core.Application, error)
	Investigations(context.Context) ([]core.Investigation, error)
	CreateInvestigation(context.Context, string, string) (core.Investigation, error)
	Investigation(context.Context, string) (core.Investigation, error)
	InvestigationExists(context.Context, string) (bool, error)
	Branch(context.Context, string) (core.Branch, error)
	Fork(context.Context, string, string, *int64) (core.Branch, error)
	PatchBranch(context.Context, string, *string, *string) (core.Branch, error)
	Preview(context.Context, string, string) (core.Preview, error)
	Merge(context.Context, string, string) (core.Branch, error)
	MergeSelected(context.Context, string, string, core.MergeOptions) (core.Branch, error)
	Revisions(context.Context, string) ([]core.ContextRevision, error)
	Revision(context.Context, string, int64) (core.ContextRevision, error)
	Events(context.Context, string, int64) ([]core.Event, error)
	Layout(context.Context, string, string) (json.RawMessage, error)
	SaveLayout(context.Context, string, string, json.RawMessage) error
}

// RunRepository encapsulates single-active-run admission, immutable input,
// idempotency, leases and fencing. All Attempt writes must verify the current lease.
// Claim returns ErrNoWork when no eligible job exists.
type RunRepository interface {
	Enqueue(context.Context, string, string, string, string) (core.Run, error)
	Run(context.Context, string) (core.Run, error)
	Cancel(context.Context, string) (core.Run, error)
	Claim(context.Context) (Claim, error)
	Renew(context.Context, Claim) error
	RetryEvidence(context.Context, Claim) ([]core.Evidence, error)
	SaveEvidence(context.Context, Claim, core.Evidence) error
	AppendDelta(context.Context, Claim, string) error
	Finish(context.Context, Claim, string, error) error
}

type Repository interface {
	IdentityRepository
	InvestigationRepository
	RunRepository
	Health(context.Context) error
}

var _ Repository = (*Store)(nil)
