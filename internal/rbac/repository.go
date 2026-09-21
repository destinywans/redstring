package rbac

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/store"
	"strings"
	"unicode/utf8"
)

var ErrProtected = errors.New("protected built-in or dependent record")
var ErrRoleNameConflict = errors.New("role name already exists")
var ErrLegacyMembership = errors.New("base role membership is maintained by member management")
var ErrLastManager = store.ErrLastRBACManager

const roleSelect = `SELECT r.id,r.name,r.description,r.builtin,r.revision,(SELECT count(*) FROM rbac_role_permissions WHERE role_id=r.id),(SELECT count(*) FROM rbac_user_roles WHERE role_id=r.id),(SELECT count(*) FROM rbac_department_roles WHERE role_id=r.id) FROM rbac_roles r`
const permissionSelect = `SELECT p.id,p.name,p.description,p.action,p.scope_type,p.builtin,p.revision,(SELECT count(*) FROM rbac_permission_resources WHERE permission_id=p.id) FROM rbac_permissions p`

func scanRole(row pgx.Row) (Role, error) {
	var v Role
	e := row.Scan(&v.ID, &v.Name, &v.Description, &v.Builtin, &v.Revision, &v.PermissionCount, &v.UserCount, &v.DepartmentCount)
	return v, store.ClassifyError(e)
}
func scanPermission(row pgx.Row) (Permission, error) {
	var v Permission
	e := row.Scan(&v.ID, &v.Name, &v.Description, &v.Action, &v.ScopeType, &v.Builtin, &v.Revision, &v.ResourceCount)
	return v, store.ClassifyError(e)
}
func (s *Service) Role(ctx context.Context, id string) (Role, error) {
	return scanRole(s.Store.DB.QueryRow(ctx, roleSelect+" WHERE r.id=$1", id))
}
func (s *Service) Permission(ctx context.Context, id string) (Permission, error) {
	return scanPermission(s.Store.DB.QueryRow(ctx, permissionSelect+" WHERE p.id=$1", id))
}
func validName(n, d string) bool {
	return strings.TrimSpace(n) != "" && utf8.RuneCountInString(n) <= 200 && utf8.RuneCountInString(d) <= 2000
}
func validPermission(p Permission) bool {
	for _, a := range Actions {
		if a["id"] == p.Action && a["scope_type"] == p.ScopeType {
			return validName(p.Name, p.Description)
		}
	}
	return false
}
func audit(ctx context.Context, tx pgx.Tx, action, id string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, store.Principal(ctx).ID, action, id, b)
	return e
}
func lock(ctx context.Context, tx pgx.Tx, table, id string, revision int64) error {
	return lockRecord(ctx, tx, table, id, revision, false)
}
func lockRecord(ctx context.Context, tx pgx.Tx, table, id string, revision int64, allowBuiltin bool) error {
	var rev int64
	var protected bool
	e := tx.QueryRow(ctx, "SELECT revision,builtin FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&rev, &protected)
	if e != nil {
		return store.ClassifyError(e)
	}
	if protected && !allowBuiltin {
		return ErrProtected
	}
	if rev != revision {
		return store.ErrConflict
	}
	return nil
}

// Account mutations and department membership writes use these same locks.
// Recheck the caller after acquiring them, so queued writes cannot use a grant
// revoked while the HTTP request was waiting.
func lockAuthority(ctx context.Context, tx pgx.Tx) error {
	for _, key := range []int64{752018992, 752018995} {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", key); err != nil {
			return err
		}
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_effective_permissions WHERE user_id=$1 AND action='manage' AND scope_type='management' AND resource_id='rbac')`, store.Principal(ctx).ID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return store.ErrForbidden
	}
	return nil
}

func (s *Service) SaveRole(ctx context.Context, v Role) (Role, error) {
	if !validName(v.Name, v.Description) {
		return v, store.ErrInvalidReference
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if v.ID == "" {
		v.ID = store.ID()
		_, e = tx.Exec(ctx, "INSERT INTO rbac_roles(id,name,description) VALUES($1,$2,$3)", v.ID, strings.TrimSpace(v.Name), v.Description)
	} else {
		if e = lock(ctx, tx, "rbac_roles", v.ID, v.Revision); e != nil {
			return v, e
		}
		_, e = tx.Exec(ctx, "UPDATE rbac_roles SET name=$2,description=$3,revision=revision+1 WHERE id=$1", v.ID, strings.TrimSpace(v.Name), v.Description)
	}
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" && pe.ConstraintName == "rbac_roles_name_key" {
			return v, ErrRoleNameConflict
		}
		return v, store.ClassifyError(e)
	}
	if e = audit(ctx, tx, "rbac.role.saved", v.ID, v); e != nil {
		return v, e
	}
	if e = tx.Commit(ctx); e != nil {
		return v, e
	}
	return s.Role(ctx, v.ID)
}
func (s *Service) SavePermission(ctx context.Context, v Permission) (Permission, error) {
	if !validPermission(v) {
		return v, store.ErrInvalidReference
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if v.ID == "" {
		v.ID = store.ID()
		_, e = tx.Exec(ctx, "INSERT INTO rbac_permissions(id,name,description,action,scope_type) VALUES($1,$2,$3,$4,$5)", v.ID, strings.TrimSpace(v.Name), v.Description, v.Action, v.ScopeType)
	} else {
		if e = lock(ctx, tx, "rbac_permissions", v.ID, v.Revision); e != nil {
			return v, e
		}
		var oldScope, oldAction string
		if e = tx.QueryRow(ctx, "SELECT scope_type,action FROM rbac_permissions WHERE id=$1", v.ID).Scan(&oldScope, &oldAction); e != nil {
			return v, e
		}
		// Never reinterpret previously granted targets as another capability.
		if oldScope != v.ScopeType || oldAction != v.Action {
			return v, store.ErrInvalidReference
		}
		_, e = tx.Exec(ctx, "UPDATE rbac_permissions SET name=$2,description=$3,revision=revision+1 WHERE id=$1", v.ID, strings.TrimSpace(v.Name), v.Description)
	}
	if e != nil {
		return v, store.ClassifyError(e)
	}
	if e = audit(ctx, tx, "rbac.permission.saved", v.ID, v); e != nil {
		return v, e
	}
	if e = tx.Commit(ctx); e != nil {
		return v, e
	}
	return s.Permission(ctx, v.ID)
}
func (s *Service) Delete(ctx context.Context, kind, id string, rev int64) error {
	table := "rbac_roles"
	dependency := `SELECT EXISTS(SELECT 1 FROM rbac_role_permissions WHERE role_id=$1 UNION ALL SELECT 1 FROM rbac_user_roles WHERE role_id=$1 UNION ALL SELECT 1 FROM rbac_department_roles WHERE role_id=$1)`
	if kind == "permissions" {
		table = "rbac_permissions"
		dependency = `SELECT EXISTS(SELECT 1 FROM rbac_role_permissions WHERE permission_id=$1)`
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lock(ctx, tx, table, id, rev); e != nil {
		return e
	}
	var dependent bool
	if e = tx.QueryRow(ctx, dependency, id).Scan(&dependent); e != nil {
		return e
	}
	if dependent {
		return ErrProtected
	}
	if _, e = tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=$1", id); e != nil {
		return store.ClassifyError(e)
	}
	if e = audit(ctx, tx, "rbac."+kind+".deleted", id, nil); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func validateDelta(d Delta) error {
	seen := map[string]bool{}
	if len(d.Add)+len(d.Remove) > 2000 {
		return store.ErrInvalidReference
	}
	for _, list := range [][]string{d.Add, d.Remove} {
		for _, id := range list {
			if id == "" || seen[id] {
				return store.ErrInvalidReference
			}
			seen[id] = true
		}
	}
	return nil
}
func (s *Service) SaveAssociations(ctx context.Context, id string, in Associations) (Role, error) {
	for _, d := range []Delta{in.Permissions, in.Users, in.Departments} {
		if e := validateDelta(d); e != nil {
			return Role{}, e
		}
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return Role{}, e
	}
	defer tx.Rollback(ctx)
	if e = lockAuthority(ctx, tx); e != nil {
		return Role{}, e
	}
	if e = lockRecord(ctx, tx, "rbac_roles", id, in.Revision, true); e != nil {
		return Role{}, e
	}
	var lockedMembership bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_user_roles WHERE role_id=$1 AND user_id=ANY($2::text[]) AND origin='legacy')`, id, in.Users.Remove).Scan(&lockedMembership); e != nil {
		return Role{}, e
	}
	if lockedMembership {
		return Role{}, ErrLegacyMembership
	}
	var builtin bool
	if e = tx.QueryRow(ctx, "SELECT builtin FROM rbac_roles WHERE id=$1", id).Scan(&builtin); e != nil {
		return Role{}, e
	}
	permissionCondition := "scope_type<>'legacy_application'"
	if builtin {
		permissionCondition = "true"
	}
	for _, item := range []struct {
		delta                            Delta
		table, column, source, condition string
	}{{in.Permissions, "rbac_role_permissions", "permission_id", "rbac_permissions", permissionCondition}, {in.Users, "rbac_user_roles", "user_id", "users", "deleted_at IS NULL AND source<>'settings_demo'"}, {in.Departments, "rbac_department_roles", "department_id", "teams", "true"}} {

		for _, target := range item.delta.Add {
			var exists bool
			e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+item.source+" WHERE id=$1 AND "+item.condition+")", target).Scan(&exists)
			if e != nil {
				return Role{}, e
			}
			if !exists {
				return Role{}, store.ErrInvalidReference
			}
		}
		for _, target := range item.delta.Remove {
			var exists bool
			e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+item.table+" WHERE role_id=$1 AND "+item.column+"=$2)", id, target).Scan(&exists)
			if e != nil {
				return Role{}, e
			}
			if !exists {
				return Role{}, store.ErrInvalidReference
			}
		}

		for _, target := range item.delta.Remove {
			if _, e = tx.Exec(ctx, "DELETE FROM "+item.table+" WHERE role_id=$1 AND "+item.column+"=$2", id, target); e != nil {
				return Role{}, e
			}
		}
		for _, target := range item.delta.Add {
			if _, e = tx.Exec(ctx, "INSERT INTO "+item.table+"(role_id,"+item.column+") VALUES($1,$2) ON CONFLICT DO NOTHING", id, target); e != nil {
				return Role{}, store.ClassifyError(e)
			}
		}
	}
	if e = store.ProtectLastRBACManager(ctx, tx); e != nil {
		return Role{}, e
	}
	if _, e = tx.Exec(ctx, "UPDATE rbac_roles SET revision=revision+1 WHERE id=$1", id); e != nil {
		return Role{}, e
	}
	if e = audit(ctx, tx, "rbac.role.associations", id, in); e != nil {
		return Role{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Role{}, e
	}
	return s.Role(ctx, id)
}
func (s *Service) SaveResources(ctx context.Context, id string, in ResourceDelta) (Permission, error) {
	if e := validateDelta(in.Delta); e != nil {
		return Permission{}, e
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return Permission{}, e
	}
	defer tx.Rollback(ctx)
	if e = lockAuthority(ctx, tx); e != nil {
		return Permission{}, e
	}
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018994)"); e != nil {
		return Permission{}, e
	}
	if e = lock(ctx, tx, "rbac_permissions", id, in.Revision); e != nil {
		return Permission{}, e
	}
	var scope string
	if e = tx.QueryRow(ctx, "SELECT scope_type FROM rbac_permissions WHERE id=$1", id).Scan(&scope); e != nil {
		return Permission{}, e
	}
	for _, list := range [][]string{in.Add} {
		for _, target := range list {
			valid := false
			if scope == "management" {
				for _, o := range ManagementObjects {
					if o["id"] == target {
						valid = true
					}
				}
			} else {
				table := "applications"
				extra := ""
				if scope == "resource" {
					table = "cmdb_resources"
					extra = ""
				}
				if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1"+extra+")", target).Scan(&valid); e != nil {
					return Permission{}, e
				}
			}
			if !valid {
				return Permission{}, store.ErrInvalidReference
			}
		}
	}
	for _, target := range in.Remove {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rbac_permission_resources WHERE permission_id=$1 AND resource_id=$2)", id, target).Scan(&exists); e != nil {
			return Permission{}, e
		}
		if !exists {
			return Permission{}, store.ErrInvalidReference
		}
	}
	for _, target := range in.Remove {
		if _, e = tx.Exec(ctx, "DELETE FROM rbac_permission_resources WHERE permission_id=$1 AND resource_id=$2", id, target); e != nil {
			return Permission{}, e
		}
	}
	for _, target := range in.Add {
		if _, e = tx.Exec(ctx, "INSERT INTO rbac_permission_resources VALUES($1,$2) ON CONFLICT DO NOTHING", id, target); e != nil {
			return Permission{}, e
		}
	}
	if e = store.ProtectLastRBACManager(ctx, tx); e != nil {
		return Permission{}, e
	}
	if _, e = tx.Exec(ctx, "UPDATE rbac_permissions SET revision=revision+1 WHERE id=$1", id); e != nil {
		return Permission{}, e
	}
	if e = audit(ctx, tx, "rbac.permission.resources", id, in); e != nil {
		return Permission{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Permission{}, e
	}
	return s.Permission(ctx, id)
}
