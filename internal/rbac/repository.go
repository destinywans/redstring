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

var ErrRoleNameConflict = errors.New("role name already exists")
var ErrPermissionNameConflict = errors.New("permission name already exists")
var ErrLegacyMembership = errors.New("base role membership is maintained by member management")
var ErrLastManager = store.ErrLastRBACManager

const roleSelect = `SELECT r.id,r.name,r.description,r.builtin,r.revision,(SELECT count(*) FROM rbac_role_permissions WHERE role_id=r.id),(SELECT count(*) FROM rbac_user_roles WHERE role_id=r.id),(SELECT count(*) FROM rbac_department_roles WHERE role_id=r.id) FROM rbac_roles r`
const permissionResourceCount = `(SELECT count(DISTINCT (scope_type,resource_id)) FROM rbac_permission_resources WHERE permission_id=p.id)`
const permissionSelect = `SELECT p.id,p.name,p.description,p.builtin,p.revision,` + permissionResourceCount + ` FROM rbac_permissions p`

func scanRole(row pgx.Row) (Role, error) {
	var v Role
	e := row.Scan(&v.ID, &v.Name, &v.Description, &v.Builtin, &v.Revision, &v.PermissionCount, &v.UserCount, &v.DepartmentCount)
	return v, store.ClassifyError(e)
}
func scanPermission(row pgx.Row) (Permission, error) {
	var v Permission
	e := row.Scan(&v.ID, &v.Name, &v.Description, &v.Builtin, &v.Revision, &v.ResourceCount)
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
func audit(ctx context.Context, tx pgx.Tx, action, id string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, store.Principal(ctx).ID, action, id, b)
	return e
}
func lock(ctx context.Context, tx pgx.Tx, table, id string, revision int64) error {
	var rev int64
	if err := tx.QueryRow(ctx, "SELECT revision FROM "+table+" WHERE id=$1 FOR UPDATE", id).Scan(&rev); err != nil {
		return store.ClassifyError(err)
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
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_platform_administrators WHERE user_id=$1)`, store.Principal(ctx).ID).Scan(&allowed); err != nil {
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
	if e = lockAuthority(ctx, tx); e != nil {
		return v, e
	}
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
	if !validName(v.Name, v.Description) {
		return v, store.ErrInvalidReference
	}
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		return v, e
	}
	defer tx.Rollback(ctx)
	if e = lockAuthority(ctx, tx); e != nil {
		return v, e
	}
	if v.ID == "" {
		v.ID = store.ID()
		_, e = tx.Exec(ctx, "INSERT INTO rbac_permissions(id,name,description) VALUES($1,$2,$3)", v.ID, strings.TrimSpace(v.Name), v.Description)
	} else {
		if e = lock(ctx, tx, "rbac_permissions", v.ID, v.Revision); e != nil {
			return v, e
		}
		_, e = tx.Exec(ctx, "UPDATE rbac_permissions SET name=$2,description=$3,revision=revision+1 WHERE id=$1", v.ID, strings.TrimSpace(v.Name), v.Description)
	}
	if e != nil {
		var pe *pgconn.PgError
		if errors.As(e, &pe) && pe.Code == "23505" && pe.ConstraintName == "rbac_permissions_name_key" {
			return v, ErrPermissionNameConflict
		}
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
	if kind == "permissions" {
		table = "rbac_permissions"
	} else if kind != "roles" {
		return store.ErrInvalidReference
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAuthority(ctx, tx); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018994)"); err != nil {
		return err
	}
	if err = lock(ctx, tx, table, id, rev); err != nil {
		return err
	}
	if kind == "roles" {
		for _, association := range []string{"rbac_role_permissions", "rbac_user_roles", "rbac_department_roles"} {
			if _, err = tx.Exec(ctx, "DELETE FROM "+association+" WHERE role_id=$1", id); err != nil {
				return err
			}
		}
	} else {
		// Invalidate role association drafts before removing a referenced permission.
		if _, err = tx.Exec(ctx, "UPDATE rbac_roles SET revision=revision+1 WHERE id IN (SELECT role_id FROM rbac_role_permissions WHERE permission_id=$1)", id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "DELETE FROM rbac_role_permissions WHERE permission_id=$1", id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=$1", id); err != nil {
		return store.ClassifyError(err)
	}
	if err = store.ProtectLastRBACManager(ctx, tx); err != nil {
		return err
	}
	if err = audit(ctx, tx, "rbac."+kind+".deleted", id, nil); err != nil {
		return err
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
	if e = lock(ctx, tx, "rbac_roles", id, in.Revision); e != nil {
		return Role{}, e
	}
	var lockedMembership bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rbac_user_roles WHERE role_id=$1 AND user_id=ANY($2::text[]) AND origin='legacy')`, id, in.Users.Remove).Scan(&lockedMembership); e != nil {
		return Role{}, e
	}
	if lockedMembership {
		return Role{}, ErrLegacyMembership
	}
	for _, item := range []struct {
		delta                            Delta
		table, column, source, condition string
	}{{in.Permissions, "rbac_role_permissions", "permission_id", "rbac_permissions", "true"}, {in.Users, "rbac_user_roles", "user_id", "users", "deleted_at IS NULL AND source<>'settings_demo'"}, {in.Departments, "rbac_department_roles", "department_id", "teams", "true"}} {

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
