package rbac

import (
	"context"
	"errors"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

// V3Policy evaluates only the V3 matrix. Legacy role rank and old permission
// bindings are deliberately absent from this decision path.
type V3Policy struct{ Store *store.Store }

type V3Decision struct {
	Module        string
	Action        string
	EnvironmentID string
	ApplicationID string
	TeamID        string
}

var v3ModuleCode = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var v3Actions = map[string]bool{
	"view": true, "change": true, "delete": true,
	"approve": true, "execute": true, "manage": true,
}

var ErrInvalidV3Decision = errors.New("invalid V3 authorization decision")

type v3QueryRow interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func validV3Decision(d V3Decision) bool {
	return v3ModuleCode.MatchString(d.Module) && v3Actions[d.Action] &&
		d.EnvironmentID != "" && d.EnvironmentID != "all" &&
		(d.ApplicationID == "" || d.ApplicationID != "*") &&
		(d.TeamID == "" || d.TeamID != "*") &&
		!(d.ApplicationID != "" && d.TeamID != "")
}

func (p *V3Policy) AuthorizeEnvironment(ctx context.Context, user core.User, environmentID string) error {
	if environmentID == "" || environmentID == "all" || p == nil || p.Store == nil || user.ID == "" || user.Disabled {
		return store.ErrForbidden
	}
	return authorizeV3Environment(ctx, p.Store.DB, user, environmentID)
}

func authorizeV3Environment(ctx context.Context, q v3QueryRow, user core.User, environmentID string) error {
	var permitted bool
	err := q.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
 AND g.module='environment' AND g.action='view'
 AND ((g.scope_kind='global' AND g.scope_id='*') OR (g.scope_kind='environment' AND g.scope_id=$2))
 )`, user.ID, environmentID).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return store.ErrForbidden
	}
	return nil
}

// Authorize checks environment access first, then the exact module operation.
// Application and team scope IDs must be resolved from the persisted target by
// the caller. An application must also be explicitly mapped to this environment.
func (p *V3Policy) Authorize(ctx context.Context, user core.User, decision V3Decision) error {
	if p == nil || p.Store == nil || user.ID == "" || user.Disabled || !validV3Decision(decision) {
		return ErrInvalidV3Decision
	}
	return authorizeV3(ctx, p.Store.DB, user, decision)
}

// AuthorizeWithTx performs every authorization read on the caller's
// transaction connection. Callers must not borrow the pool while holding a
// reply row lock, particularly when the pool has a single connection.
func (p *V3Policy) AuthorizeWithTx(ctx context.Context, tx pgx.Tx, user core.User, decision V3Decision) error {
	if p == nil || p.Store == nil || tx == nil || user.ID == "" || user.Disabled || !validV3Decision(decision) {
		return ErrInvalidV3Decision
	}
	return authorizeV3(ctx, tx, user, decision)
}

func authorizeV3(ctx context.Context, q v3QueryRow, user core.User, decision V3Decision) error {
	// A matrix cell marked conditional is not approval evidence. Until the
	// server can verify a fresh passkey challenge and bind it to the exact
	// proposal, V3 approval remains unavailable even to administrators.
	if decision.Action == "approve" {
		return store.ErrForbidden
	}
	if err := authorizeV3Environment(ctx, q, user, decision.EnvironmentID); err != nil {
		return err
	}
	var permitted bool
	err := q.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=$1
 AND g.module=$2 AND g.action=$3
 AND (g.action<>'approve' OR (g.v3_identity_kind='human' AND g.role_kind<>'machine'))
 AND (
      (g.scope_kind='global' AND g.scope_id='*')
   OR (g.scope_kind='environment' AND g.scope_id=$4)
   OR ($5<>'' AND g.scope_kind='application' AND g.scope_id=$5)
   OR ($6<>'' AND g.scope_kind='team' AND g.scope_id=$6)
 )
 ) AND ($5='' OR EXISTS (
 SELECT 1 FROM v3_application_environments ae
 WHERE ae.application_id=$5 AND ae.environment_id=$4
 ))`, user.ID, decision.Module, decision.Action, decision.EnvironmentID, decision.ApplicationID, decision.TeamID).Scan(&permitted)
	if err != nil {
		return err
	}
	if !permitted {
		return store.ErrForbidden
	}
	return nil
}
