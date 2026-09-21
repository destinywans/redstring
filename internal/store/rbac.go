package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
)

var ErrLastRBACManager = errors.New("at least one active RBAC manager required")
var ErrBaseRoleUnavailable = errors.New("base role was deleted")

func requireBaseRole(ctx context.Context, tx pgx.Tx, role string) error {
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rbac_roles WHERE id=$1)", role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrBaseRoleUnavailable
	}
	return nil
}

// Callers serialize account and department changes with the same advisory locks
// as RBAC writes. Legacy account roles alone do not imply effective management.
func ProtectLastRBACManager(ctx context.Context, tx pgx.Tx) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions p JOIN users u ON u.id=p.user_id WHERE p.action='manage' AND p.scope_type='management' AND p.resource_id='rbac' AND u.password_hash<>'')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrLastRBACManager
	}
	return nil
}

// AuthorizeCapability evaluates persisted action/scope grants. Each action is
// independent: manage, investigate, approve and execute never imply each other.
func (s *Store) AuthorizeCapability(ctx context.Context, u core.User, action, scope, id string) error {
	var allowed bool
	e := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$1 AND p.action=$2 AND p.scope_type=$3 AND p.resource_id=$4)`, u.ID, action, scope, id).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func (s *Store) authorizeApplicationRBAC(ctx context.Context, u core.User, app, action string) error {
	if action == "operate" {
		action = "investigate"
	}
	var exists, archived bool
	e := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1),COALESCE((SELECT archived FROM applications WHERE id=$1),false)`, app).Scan(&exists, &archived)
	if e != nil {
		return e
	}
	if !exists {
		return ErrNotFound
	}
	if archived && action != "view" {
		return ErrForbidden
	}
	var allowed bool
	e = s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1 AND
 ((p.scope_type='application' AND p.resource_id=$2 AND (p.action=$3 OR ($3='view' AND p.action IN ('investigate','approve','execute')))) OR
 (p.scope_type='legacy_application' AND p.resource_id=$2 AND p.action=$3 AND ((`+sessionLegacyRole+`)='admin' OR EXISTS(SELECT 1 FROM application_grants g WHERE g.application_id=$2 AND ($3='view' OR ((`+sessionLegacyRole+`)<>'viewer' AND g.permission='operate')) AND ((g.subject_type='user' AND g.subject_id=u.id) OR (g.subject_type='team' AND g.subject_id IN (SELECT team_id FROM team_members WHERE user_id=u.id))))))))`, u.ID, app, action).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
