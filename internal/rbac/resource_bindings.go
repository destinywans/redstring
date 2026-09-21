package rbac

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/store"
)

func availableActions(scope string) []string {
	switch scope {
	case "resource":
		return []string{"view", "update", "delete"}
	case "application":
		return []string{"view", "approve"}
	default:
		return []string{}
	}
}

func targetKey(target ResourceTarget) string { return target.ScopeType + ":" + target.ResourceID }

func validTarget(target ResourceTarget) bool {
	if strings.TrimSpace(target.ResourceID) == "" {
		return false
	}
	switch target.ScopeType {
	case "resource", "application", "legacy_application", "management":
		return true
	default:
		return false
	}
}

func validateBindings(in ResourceDelta) error {
	if len(in.Upsert)+len(in.Remove) > 2000 {
		return store.ErrInvalidReference
	}
	seen := map[string]bool{}
	for _, binding := range in.Upsert {
		key := targetKey(binding.ResourceTarget)
		if !validTarget(binding.ResourceTarget) || seen[key] || len(binding.Actions) == 0 || len(binding.Actions) > 7 {
			return store.ErrInvalidReference
		}
		seen[key] = true
		actions := map[string]bool{}
		for _, action := range binding.Actions {
			if action == "" || actions[action] {
				return store.ErrInvalidReference
			}
			actions[action] = true
		}
	}
	for _, target := range in.Remove {
		key := targetKey(target)
		if !validTarget(target) || seen[key] {
			return store.ErrInvalidReference
		}
		seen[key] = true
	}
	return nil
}

func targetExists(ctx context.Context, tx pgx.Tx, target ResourceTarget) (bool, error) {
	if target.ScopeType == "management" {
		for _, item := range ManagementObjects {
			if item["id"] == target.ResourceID {
				return true, nil
			}
		}
		return false, nil
	}
	table := "applications"
	if target.ScopeType == "resource" {
		table = "cmdb_resources"
	}
	var exists bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE id=$1)", target.ResourceID).Scan(&exists)
	return exists, err
}

func (s *Service) SaveResources(ctx context.Context, id string, in ResourceDelta) (Permission, error) {
	if err := validateBindings(in); err != nil {
		return Permission{}, err
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return Permission{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockAuthority(ctx, tx); err != nil {
		return Permission{}, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018994)"); err != nil {
		return Permission{}, err
	}
	if err = lock(ctx, tx, "rbac_permissions", id, in.Revision); err != nil {
		return Permission{}, err
	}
	for _, binding := range in.Upsert {
		exists, e := targetExists(ctx, tx, binding.ResourceTarget)
		if e != nil {
			return Permission{}, e
		}
		if !exists {
			return Permission{}, store.ErrInvalidReference
		}
		rows, e := tx.Query(ctx, "SELECT action FROM rbac_permission_resources WHERE permission_id=$1 AND scope_type=$2 AND resource_id=$3", id, binding.ScopeType, binding.ResourceID)
		if e != nil {
			return Permission{}, e
		}
		allowed := map[string]bool{}
		for _, action := range availableActions(binding.ScopeType) {
			allowed[action] = true
		}
		for rows.Next() {
			var action string
			if e = rows.Scan(&action); e != nil {
				rows.Close()
				return Permission{}, e
			}
			allowed[action] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return Permission{}, e
		}
		for _, action := range binding.Actions {
			if !allowed[action] {
				return Permission{}, store.ErrInvalidReference
			}
		}
		if _, err = tx.Exec(ctx, "DELETE FROM rbac_permission_resources WHERE permission_id=$1 AND scope_type=$2 AND resource_id=$3", id, binding.ScopeType, binding.ResourceID); err != nil {
			return Permission{}, err
		}
		for _, action := range binding.Actions {
			if _, err = tx.Exec(ctx, "INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,$2,$3,$4)", id, binding.ScopeType, binding.ResourceID, action); err != nil {
				return Permission{}, store.ClassifyError(err)
			}
		}
	}
	for _, target := range in.Remove {
		result, e := tx.Exec(ctx, "DELETE FROM rbac_permission_resources WHERE permission_id=$1 AND scope_type=$2 AND resource_id=$3", id, target.ScopeType, target.ResourceID)
		if e != nil {
			return Permission{}, e
		}
		if result.RowsAffected() == 0 {
			return Permission{}, store.ErrInvalidReference
		}
	}
	if err = store.ProtectLastRBACManager(ctx, tx); err != nil {
		return Permission{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE rbac_permissions SET revision=revision+1 WHERE id=$1", id); err != nil {
		return Permission{}, err
	}
	if err = audit(ctx, tx, "rbac.permission.resources", id, in); err != nil {
		return Permission{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Permission{}, err
	}
	return s.Permission(ctx, id)
}
