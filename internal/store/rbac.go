package store

import (
	"context"
	"github.com/wans/aiops-canvas/internal/core"
)

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
 (p.scope_type='legacy_application' AND p.action=$3 AND (u.role='admin' OR EXISTS(SELECT 1 FROM application_grants g WHERE g.application_id=$2 AND ($3='view' OR (u.role<>'viewer' AND g.permission='operate')) AND ((g.subject_type='user' AND g.subject_id=u.id) OR (g.subject_type='team' AND g.subject_id IN (SELECT team_id FROM team_members WHERE user_id=u.id))))))))`, u.ID, app, action).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
