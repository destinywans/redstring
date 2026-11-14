package v3admin

import (
	"context"
	"errors"
	"slices"

	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
)

var errPlatformAdminRequired = errors.New("platform administrator required")
var errGrantExceedsActor = errors.New("grant exceeds actor authority")
var errSelfAccessChange = errors.New("delegated administrator cannot modify own access")
var errAdminApproval = errors.New("admin role cannot receive new approval grants")

// These reads run in the write transaction after memberIdentityLocks, which
// serializes V3 matrix changes with account and association changes.
func isPlatformAdmin(ctx context.Context, tx pgx.Tx, actorID string) (bool, error) {
	var platformAdmin bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM rbac_platform_administrators p JOIN users u ON u.id=p.user_id
 WHERE p.user_id=$1 AND u.v3_identity_kind='human'
 )`, actorID).Scan(&platformAdmin)
	return platformAdmin, err
}

func loadGrantCeiling(ctx context.Context, tx pgx.Tx, actorID string) ([]grant, error) {
	rows, err := tx.Query(ctx, `SELECT module,action,scope_kind,scope_id,assurance
 FROM v3_effective_role_grants WHERE user_id=$1`, actorID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[grant])
}

func privilegedGrant(g grant) bool {
	return g.Action == "approve" || g.Action == "execute" ||
		g.Action == "manage" && (g.Module == "roles" || g.Module == "members" || g.Module == "workspace")
}

func checkGrantCeiling(ceiling, changed []grant) error {
	for _, g := range changed {
		if privilegedGrant(g) {
			return errPlatformAdminRequired
		}
	}
	for _, g := range changed {
		if !slices.ContainsFunc(ceiling, func(held grant) bool {
			return held.Module == g.Module && held.Action == g.Action && held.Assurance == g.Assurance &&
				(held.ScopeKind == "global" || (held.ScopeKind == g.ScopeKind && held.ScopeID == g.ScopeID))
		}) {
			return errGrantExceedsActor
		}
	}
	return nil
}

// Use raw associations rather than the effective view. Disabled members may
// still hold protected roles and could otherwise be reactivated by a delegate.
const privilegedMemberSQL = `EXISTS (
 SELECT 1 FROM (
  SELECT a.role_id FROM rbac_user_roles a WHERE a.user_id=u.id
  UNION ALL
  SELECT d.role_id FROM rbac_department_roles d WHERE d.department_id=u.department_id
 ) assigned
 WHERE assigned.role_id='admin'
 OR EXISTS (
  SELECT 1 FROM rbac_role_permissions rp
  JOIN rbac_permission_resources pr ON pr.permission_id=rp.permission_id
  WHERE rp.role_id=assigned.role_id AND pr.action<>'view'
 )
 OR EXISTS (
  SELECT 1 FROM v3_role_grants g WHERE g.role_id=assigned.role_id
  AND (g.action IN ('approve','execute') OR (g.action='manage' AND g.module IN ('roles','members','workspace')))
 )
)`

const memberHoldsRoleSQL = `(EXISTS (
 SELECT 1 FROM rbac_user_roles a WHERE a.user_id=u.id AND a.role_id=$1
 ) OR EXISTS (
 SELECT 1 FROM rbac_department_roles d WHERE d.department_id=u.department_id AND d.role_id=$1
 ))`

func protectPrivilegedMember(ctx context.Context, tx pgx.Tx, id string) error {
	var privileged bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM users u WHERE u.id=$1 AND `+privilegedMemberSQL+`
 )`, id).Scan(&privileged); err != nil {
		return err
	}
	if privileged {
		return errPlatformAdminRequired
	}
	return nil
}

func protectDelegatedRoleEdit(ctx context.Context, tx pgx.Tx, actorID, roleID, kind string, builtin bool, grants []grant) error {
	if builtin || kind == "system" {
		return errPlatformAdminRequired
	}
	var heldByActor, heldByPrivilegedMember bool
	if err := tx.QueryRow(ctx, `SELECT
 EXISTS (SELECT 1 FROM users u WHERE u.id=$2 AND `+memberHoldsRoleSQL+`),
 EXISTS (SELECT 1 FROM users u WHERE u.deleted_at IS NULL AND u.source<>'settings_demo'
  AND `+memberHoldsRoleSQL+` AND `+privilegedMemberSQL+`)
 `, roleID, actorID).Scan(&heldByActor, &heldByPrivilegedMember); err != nil {
		return err
	}
	if heldByActor {
		return errSelfAccessChange
	}
	if heldByPrivilegedMember || slices.ContainsFunc(grants, privilegedGrant) {
		return errPlatformAdminRequired
	}
	return nil
}

func protectPlatformRoleManager(ctx context.Context, tx pgx.Tx) error {
	var usable bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM rbac_platform_administrators p JOIN users u ON u.id=p.user_id
 WHERE u.v3_identity_kind='human' AND u.password_hash<>''
 AND 5=(SELECT count(DISTINCT (g.module,g.action)) FROM v3_effective_role_grants g
  WHERE g.user_id=p.user_id AND g.scope_kind='global' AND g.scope_id='*'
  AND ((g.module IN ('roles','members') AND g.action IN ('view','manage'))
   OR (g.module='environment' AND g.action='view')))
 )`).Scan(&usable); err != nil {
		return err
	}
	if !usable {
		return store.ErrLastRBACManager
	}
	return nil
}
