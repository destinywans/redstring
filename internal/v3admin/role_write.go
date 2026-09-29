package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type matrixUpdate struct {
	Matrix []matrixRow `json:"matrix"`
}

func (s *Service) putRole(w http.ResponseWriter, r *http.Request) {
	values := r.Header.Values("If-Role-Matrix-Revision")
	if len(values) == 0 || strings.TrimSpace(r.Header.Get("If-Role-Matrix-Revision")) == "" {
		httpapi.Error(w, http.StatusPreconditionRequired, "role_revision_required", "Read the role matrix revision before saving")
		return
	}
	if len(values) != 1 {
		httpapi.Error(w, http.StatusBadRequest, "invalid_precondition", "Exactly one role matrix revision is required")
		return
	}
	expected, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || expected < 1 || strconv.FormatInt(expected, 10) != values[0] {
		httpapi.Error(w, http.StatusBadRequest, "invalid_precondition", "Role matrix revision must be a positive decimal integer")
		return
	}
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
	revision, err := s.saveMatrix(r.Context(), r.PathValue("id"), envs[0], expected, wanted)
	if err != nil {
		respond(w, nil, err)
		return
	}
	// A manager is not automatically a reader. Returning a fresh detail after
	// commit could disclose member names or describe somebody else's later edit.
	w.Header().Set("X-Role-Matrix-Revision", strconv.FormatInt(revision, 10))
	w.Header().Set("Cache-Control", "private, no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

func (s *Service) saveMatrix(ctx context.Context, roleID, environment string, expected int64, wanted map[string][]int) (int64, error) {
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Account and legacy role-association writers take 992 then 995. Hold
	// those locks before the matrix lock and authority reads, so a queued
	// matrix write cannot use a role assignment revoked by either writer.
	// The final lock serializes matrix writes for the last-manager invariant.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
			return 0, err
		}
	}
	// Middleware authorizes before entering this transaction. Recheck on the
	// same connection after the write lock so a removed grant cannot authorize
	// a later matrix change. The flat editor can only create global grants.
	actor := store.Principal(ctx)
	var activeActor string
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR SHARE`, actor.ID).Scan(&activeActor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, store.ErrForbidden
	}
	if err != nil {
		return 0, err
	}
	if err = (&rbac.V3Policy{Store: s.Store}).AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{
		Module: "roles", Action: "manage", EnvironmentID: environment,
	}); err != nil {
		return 0, err
	}
	var globalManage, globalEnvironmentView bool
	if err = tx.QueryRow(ctx, `SELECT
 EXISTS (SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='roles' AND action='manage' AND scope_kind='global' AND scope_id='*'),
 EXISTS (SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&globalManage, &globalEnvironmentView); err != nil {
		return 0, err
	}
	// The flat editor changes global grants, including environments outside
	// the selected request scope. Require instance-wide environment admission.
	if !globalManage || !globalEnvironmentView {
		return 0, store.ErrForbidden
	}
	var kind string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT v3_kind,v3_matrix_revision FROM rbac_roles WHERE id=$1 FOR UPDATE`, roleID).Scan(&kind, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if revision != expected {
		return 0, errRoleChanged
	}
	if kind == "machine" {
		for _, cells := range wanted {
			if cells[3] > 0 {
				return 0, errMachineApproval
			}
		}
	}
	beforeGrants, err := loadGrants(ctx, tx, roleID)
	if err != nil {
		return 0, err
	}
	if err := validateMatrixEditable(beforeGrants); err != nil {
		return 0, err
	}
	before := matrixForRole(beforeGrants)
	changed := false
	for index, definition := range matrixCatalog {
		cells := wanted[definition.Code]
		if cells == nil {
			return 0, errInvalidMatrix
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
						return 0, err
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
						return 0, err
					}
				}
				changed = true
			}
		}
	}
	if !changed {
		return revision, tx.Commit(ctx)
	}
	var managers int
	err = tx.QueryRow(ctx, `SELECT count(DISTINCT manager.user_id) FROM v3_effective_role_grants manager
 WHERE manager.module='roles' AND manager.action='manage' AND manager.v3_identity_kind='human'
 AND manager.scope_kind='global' AND manager.scope_id='*'
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants admission
  WHERE admission.user_id=manager.user_id AND admission.module='environment'
   AND admission.action='view' AND admission.scope_kind='global' AND admission.scope_id='*')`).Scan(&managers)
	if err != nil {
		return 0, err
	}
	if managers == 0 {
		return 0, errLastRoleManager
	}
	if err = tx.QueryRow(ctx, `UPDATE rbac_roles SET v3_matrix_revision=v3_matrix_revision+1 WHERE id=$1 RETURNING v3_matrix_revision`, roleID).Scan(&revision); err != nil {
		return 0, err
	}
	afterGrants, err := loadGrants(ctx, tx, roleID)
	if err != nil {
		return 0, err
	}
	// Use stable operation and resource codes in audit records, so language
	// changes never rewrite the meaning of an earlier decision.
	if err = auditMatrixCodes(ctx, tx, roleID, before, matrixForRole(afterGrants)); err != nil {
		return 0, err
	}
	return revision, tx.Commit(ctx)
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
	return validateMatrixProjection(grants)
}

// The role detail has one scope list for the whole matrix and only five
// operation columns. It cannot truthfully display an execute grant, a direct
// approval without step-up, or cells with different scope sets. Reject these
// roles instead of turning a partially visible matrix into an editor input.
func validateMatrixProjection(grants []grant) error {
	if err := validateMatrixGroups(grants); err != nil {
		return err
	}
	var common map[string]bool
	cellScopes := map[string]map[string]bool{}
	for _, g := range grants {
		if g.Action == "execute" || g.Action == "approve" && g.Assurance != "passkey" ||
			g.Action != "approve" && g.Assurance != "standard" {
			return errUnrepresentableMatrix
		}
		scope := g.ScopeKind + "\x00" + g.ScopeID
		cell := g.Module + "\x00" + g.Action
		if cellScopes[cell] == nil {
			cellScopes[cell] = map[string]bool{}
		}
		cellScopes[cell][scope] = true
	}
	for _, scopes := range cellScopes {
		if common == nil {
			common = scopes
			continue
		}
		if len(common) != len(scopes) {
			return errUnrepresentableMatrix
		}
		for scope := range common {
			if !scopes[scope] {
				return errUnrepresentableMatrix
			}
		}
	}
	return nil
}

// A grouped cell is truthful only when every underlying module has the same
// operation and assurance. For example, roles.view alone cannot be rendered
// as a granted "Members & roles" view cell.
func validateMatrixGroups(grants []grant) error {
	for _, resource := range matrixCatalog {
		if len(resource.Modules) < 2 {
			continue
		}
		for _, action := range matrixActions {
			var expected map[string]bool
			for index, module := range resource.Modules {
				actual := map[string]bool{}
				for _, g := range grants {
					if g.Module == module && g.Action == action {
						actual[g.ScopeKind+"\x00"+g.ScopeID+"\x00"+g.Assurance] = true
					}
				}
				if index == 0 {
					expected = actual
					continue
				}
				if len(actual) != len(expected) {
					return errPartialMatrix
				}
				for key := range expected {
					if !actual[key] {
						return errPartialMatrix
					}
				}
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
