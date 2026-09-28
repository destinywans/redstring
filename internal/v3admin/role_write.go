package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

type matrixUpdate struct {
	Matrix []matrixRow `json:"matrix"`
}

func (s *Service) putRole(w http.ResponseWriter, r *http.Request) {
	var req matrixUpdate
	if !httpapi.Decode(w, r, &req) {
		return
	}
	wanted, err := canonicalMatrix(req.Matrix)
	if err != nil {
		respond(w, nil, err)
		return
	}
	envs, ok := scoped(r)
	if !ok || len(envs) != 1 {
		httpapi.Error(w, 422, "environment_required", "Select one environment to change a role")
		return
	}
	// The management grant may be scoped to a chosen environment, but a
	// summary matrix has no scope editor. Such a caller cannot create global
	// grants through this route.
	global, err := s.hasGlobalGrant(r.Context(), httpapi.CurrentUser(r).ID, "roles", "manage")
	if err != nil {
		respond(w, nil, err)
		return
	}
	if !global {
		httpapi.Error(w, 403, "global_role_manager_required", "Global role management is required for the summary matrix")
		return
	}
	err = s.saveMatrix(r.Context(), r.PathValue("id"), wanted)
	if err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.getRole(r.Context(), r.PathValue("id"), envs, true, englishLanguage(r.Header.Get("Accept-Language")))
	respond(w, v, err)
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func loadGrants(ctx context.Context, db queryer, roleID string) ([]grant, error) {
	rows, err := db.Query(ctx, `SELECT module,action,scope_kind,scope_id,assurance
 FROM v3_role_grants WHERE role_id=$1 ORDER BY module,action,scope_kind,scope_id`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []grant{}
	for rows.Next() {
		var g grant
		if err = rows.Scan(&g.Module, &g.Action, &g.ScopeKind, &g.ScopeID, &g.Assurance); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

func (s *Service) saveMatrix(ctx context.Context, roleID string, wanted map[string][]int) error {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Serializing all role matrix changes also protects the last-manager
	// invariant when two different roles are edited concurrently.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018998)`); err != nil {
		return err
	}
	var kind string
	err = tx.QueryRow(ctx, `SELECT v3_kind FROM rbac_roles WHERE id=$1 FOR UPDATE`, roleID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if kind == "machine" {
		for _, cells := range wanted {
			if cells[3] > 0 {
				return errMachineApproval
			}
		}
	}
	beforeGrants, err := loadGrants(ctx, tx, roleID)
	if err != nil {
		return err
	}
	if err := validateMatrixEditable(beforeGrants); err != nil {
		return err
	}
	before := matrixForRole(beforeGrants)
	changed := false
	for index, definition := range matrixCatalog {
		cells := wanted[definition.Code]
		if cells == nil {
			return errInvalidMatrix
		}
		for col, action := range matrixActions {
			oldValue := before[index].Grants[col]
			newValue := cells[col]
			// A selected approval always becomes a conditional passkey
			// grant, whether the UI sent 1 or 2. The authorization layer
			// denies it until it receives verified step-up evidence.
			if action == "approve" && newValue > 0 {
				newValue = 2
			}
			if oldValue == newValue {
				continue
			}
			for _, module := range definition.Modules {
				if oldValue > 0 {
					if _, err = tx.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id=$1 AND module=$2 AND action=$3 AND scope_kind='global' AND scope_id='*'`, roleID, module, action); err != nil {
						return err
					}
				}
				if newValue > 0 {
					assurance := "standard"
					if action == "approve" {
						assurance = "passkey"
					}
					if _, err = tx.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id,assurance)
 VALUES($1,$2,$3,'global','*',$4) ON CONFLICT (role_id,module,action,scope_kind,scope_id)
 DO UPDATE SET assurance=excluded.assurance`, roleID, module, action, assurance); err != nil {
						return err
					}
				}
				changed = true
			}
		}
	}
	if !changed {
		return tx.Commit(ctx)
	}
	var managers int
	err = tx.QueryRow(ctx, `SELECT count(DISTINCT user_id) FROM v3_effective_role_grants
 WHERE module='roles' AND action='manage' AND v3_identity_kind='human'
 AND scope_kind='global' AND scope_id='*'`).Scan(&managers)
	if err != nil {
		return err
	}
	if managers == 0 {
		return errLastRoleManager
	}
	if _, err = tx.Exec(ctx, `UPDATE rbac_roles SET v3_matrix_revision=v3_matrix_revision+1 WHERE id=$1`, roleID); err != nil {
		return err
	}
	afterGrants, err := loadGrants(ctx, tx, roleID)
	if err != nil {
		return err
	}
	// Use stable operation and resource codes in audit records, so language
	// changes never rewrite the meaning of an earlier decision.
	if err = auditMatrixCodes(ctx, tx, roleID, before, matrixForRole(afterGrants)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// validateMatrixEditable keeps the flat V3 editor from silently widening a
// scoped grant or treating one half of a grouped resource as both modules.
// The latter can happen when a role was granted members.view but not roles.view.
func validateMatrixEditable(grants []grant) error {
	for _, g := range grants {
		if g.ScopeKind != "global" || g.ScopeID != "*" {
			return errScopedCell
		}
	}
	for _, resource := range matrixCatalog {
		if len(resource.Modules) < 2 {
			continue
		}
		for _, action := range matrixActions {
			found := 0
			assurance := ""
			for _, module := range resource.Modules {
				for _, g := range grants {
					if g.Module == module && g.Action == action {
						if assurance != "" && assurance != g.Assurance {
							return errPartialMatrix
						}
						assurance = g.Assurance
						found++
						break
					}
				}
			}
			if found > 0 && found != len(resource.Modules) {
				return errPartialMatrix
			}
		}
	}
	return nil
}

func auditMatrixCodes(ctx context.Context, tx pgx.Tx, roleID string, before, after []matrixRow) error {
	toCodes := func(rows []matrixRow) map[string][]int {
		out := map[string][]int{}
		for i, row := range rows {
			out[matrixCatalog[i].Code] = row.Grants
		}
		return out
	}
	changes, err := json.Marshal(map[string]any{"before": toCodes(before), "after": toCodes(after)})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
 VALUES($1,'v3.role.matrix.updated',$2,'success',$3)`, store.Principal(ctx).ID, roleID, changes)
	return err
}
