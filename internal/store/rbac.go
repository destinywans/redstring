package store

import (
	"context"
	"errors"

	"github.com/destinywans/redstring/internal/core"
	"github.com/jackc/pgx/v5"
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators p JOIN users u ON u.id=p.user_id WHERE u.password_hash<>'')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrLastRBACManager
	}
	return nil
}

// Resource operations are independent persisted bindings. The management call
// shape remains internal compatibility for the actual platform-admin identity.
func (s *Store) AuthorizeCapability(ctx context.Context, u core.User, action, scope, id string) error {
	var allowed bool
	if action == "manage" && scope == "management" {
		err := s.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators WHERE user_id=$1)", u.ID).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return nil
	}
	e := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$1 AND p.action=$2 AND p.scope_type=$3 AND p.resource_id=$4)`, u.ID, action, scope, id).Scan(&allowed)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

// Legacy application-wide operations require the corresponding resource action
// on every linked infrastructure resource. A view grant must not authorize an
// investigation or other operator route. Writes and approvals still use the
// exact target, never application authority.
func (s *Store) authorizeApplicationRBAC(ctx context.Context, u core.User, app, action string) error {
	if action != "view" && action != "operate" && action != "investigate" {
		return ErrForbidden
	}
	required := "view"
	if action != "view" {
		required = "update"
	}
	var exists, archived, allowed bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE id=$2),
 COALESCE((SELECT archived FROM applications WHERE id=$2),false),
 EXISTS(SELECT 1 FROM cmdb_resource_applications l JOIN cmdb_resources r ON r.id=l.resource_id WHERE l.application_id=$2 AND rbac_is_infrastructure_kind(r.kind))
 AND NOT EXISTS(SELECT 1 FROM cmdb_resource_applications l JOIN cmdb_resources r ON r.id=l.resource_id
 WHERE l.application_id=$2 AND rbac_is_infrastructure_kind(r.kind)
 AND (NOT EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$1 AND p.action='view' AND p.scope_type='resource' AND p.resource_id=r.id)
	 OR NOT EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$1 AND p.action=$3 AND p.scope_type='resource' AND p.resource_id=r.id)))`, u.ID, app, required).Scan(&exists, &archived, &allowed)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if !allowed || (archived && action != "view") {
		return ErrForbidden
	}
	return nil
}

// AuthorizeRecoveryTarget authorizes the immutable connector target, not another
// resource belonging to the same application. Ambiguous, mock or unregistered
// targets cannot be approved or executed.
func (s *Store) AuthorizeRecoveryTarget(ctx context.Context, user, app, environment, connector, namespace, name, uid, action string) error {
	if (action != "approve" && action != "update") || environment == "" || connector == "" || namespace == "" || name == "" || uid == "" {
		return ErrForbidden
	}
	var count, granted int
	err := s.DB.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE EXISTS(SELECT 1 FROM rbac_effective_permissions p WHERE p.user_id=$1 AND p.action=$8 AND p.scope_type='resource' AND p.resource_id=r.id))
 FROM cmdb_resources r WHERE r.kind='deployment' AND NOT r.is_mock AND r.source_id=$4
 AND r.attributes->>'namespace'=$5 AND r.name=$6 AND (r.external_id=$7 OR r.attributes->>'uid'=$7)
 AND EXISTS(SELECT 1 FROM cmdb_resource_applications l WHERE l.resource_id=r.id AND l.application_id=$2 AND l.environment_id=$3)`, user, app, environment, connector, namespace, name, uid, action).Scan(&count, &granted)
	if err != nil {
		return err
	}
	if count != 1 || granted != 1 {
		return ErrForbidden
	}
	return nil
}
