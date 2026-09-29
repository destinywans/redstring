// Package v3admin serves the V3 member directory and editable role matrix.
// Legacy RBAC records remain as historical inputs, but only v3_role_grants
// represent grants displayed and changed by these handlers.
package v3admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct{ Store *store.Store }

func New(s *store.Store) *Service { return &Service{Store: s} }

var errInvalidMatrix = errors.New("invalid role matrix")
var errScopedCell = errors.New("scoped grant cannot be edited in summary matrix")
var errPartialMatrix = errors.New("role matrix cannot represent a partial resource group")
var errUnrepresentableMatrix = errors.New("role grants cannot be represented by the summary matrix")
var errMachineApproval = errors.New("machine identity cannot approve")
var errInvalidStoredRoleGrant = errors.New("stored role grant conflicts with role kind")
var errLastRoleManager = errors.New("cannot remove last role manager")
var errUnsupportedMemberSource = errors.New("member source is not represented in V3 contract")
var errMemberMFAUnavailable = errors.New("member MFA evidence is unavailable")
var errRoleChanged = errors.New("role matrix revision changed")

func respond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Role not found")
	case errors.Is(err, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Insufficient permission")
	case errors.Is(err, store.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, errInvalidMatrix):
		httpapi.Error(w, 422, "invalid_matrix", "Matrix must contain each known resource once with five valid operation cells")
	case errors.Is(err, errMachineApproval):
		httpapi.Error(w, 422, "machine_approval_forbidden", "Machine identities cannot receive approval grants")
	case errors.Is(err, errInvalidStoredRoleGrant):
		httpapi.Error(w, 503, "role_grants_invalid", "Stored role grants conflict with the role kind")
	case errors.Is(err, errScopedCell):
		httpapi.Error(w, 409, "matrix_editor_required", "Scoped grants cannot be changed by the summary matrix")
	case errors.Is(err, errPartialMatrix):
		httpapi.Error(w, 409, "matrix_editor_required", "Grouped resource grants differ and require a dedicated editor")
	case errors.Is(err, errUnrepresentableMatrix):
		httpapi.Error(w, 409, "matrix_editor_required", "Role grants cannot be represented by the summary matrix")
	case errors.Is(err, errLastRoleManager):
		httpapi.Error(w, 409, "last_role_manager", "At least one active human role manager is required")
	case errors.Is(err, errRoleChanged):
		httpapi.Error(w, 409, "role_changed", "Role matrix changed; refresh and try again")
	case errors.Is(err, errUnsupportedMemberSource):
		httpapi.Error(w, 503, "member_source_unmapped", "Member directory contains an unmapped identity source")
	case errors.Is(err, errMemberMFAUnavailable):
		httpapi.Error(w, 503, "member_mfa_unavailable", "Member MFA enrollment and grace policy are unavailable")
	default:
		httpapi.Error(w, 503, "admin_unavailable", "Administration storage unavailable")
	}
}

// Register is called before serving requests. The path-specific V3 marks
// bypass legacy role-rank checks, but require configured V3 environment and
// policy middleware; otherwise the server fails closed.
func (s *Service) Register(api *httpapi.Server) {
	for _, route := range []struct{ pattern, module, action string }{
		{"GET /api/v1/roles", "roles", "view"},
		{"GET /api/v1/roles/{id}", "roles", "view"},
		{"PUT /api/v1/roles/{id}", "roles", "manage"},
		{"GET /api/v1/members", "members", "view"},
	} {
		api.MarkV3Route(route.pattern, route.module, route.action)
	}
	api.Register("GET /api/v1/roles", "viewer", s.roles)
	api.Register("GET /api/v1/roles/{id}", "viewer", s.role)
	api.Register("PUT /api/v1/roles/{id}", "viewer", s.putRole)
	api.Register("GET /api/v1/members", "viewer", s.members)
}

func scoped(r *http.Request) ([]string, bool) {
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	return scope.EnvironmentIDs, ok && len(scope.EnvironmentIDs) > 0
}

// Only an instance-wide reader may inspect unassigned directory records.
// A global module grant alone does not expand environment admission.
func (s *Service) hasGlobalCatalogRead(ctx context.Context, actor, module string) (bool, error) {
	var ok bool
	err := s.Store.DB.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants
 WHERE user_id=$1 AND module=$2 AND action='view'
 AND scope_kind='global' AND scope_id='*') AND EXISTS (
 SELECT 1 FROM v3_effective_role_grants
 WHERE user_id=$1 AND module='environment' AND action='view'
 AND scope_kind='global' AND scope_id='*')`, actor, module).Scan(&ok)
	return ok, err
}

func (s *Service) roles(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	global, err := s.hasGlobalCatalogRead(r.Context(), httpapi.CurrentUser(r).ID, "roles")
	if err != nil {
		respond(w, nil, err)
		return
	}
	rows, err := s.Store.DB.Query(r.Context(), `SELECT id,name,v3_kind FROM rbac_roles r
 WHERE ($1 AND NOT EXISTS (SELECT 1 FROM v3_role_grants unassigned WHERE unassigned.role_id=r.id))
 OR EXISTS (SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($2::text[]))
 OR (g.scope_kind='team' AND EXISTS (
  SELECT 1 FROM applications a
  JOIN v3_application_environments ae ON ae.application_id=a.id
  JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
  WHERE a.owner_team_id=g.scope_id AND NOT a.archived AND ae.environment_id=ANY($2::text[])))
 OR (g.scope_kind='application' AND EXISTS (
  SELECT 1 FROM v3_application_environments ae
  JOIN applications a ON a.id=ae.application_id AND NOT a.archived
  JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
  WHERE ae.application_id=g.scope_id AND ae.environment_id=ANY($2::text[])))))
 ORDER BY name,id`, global, envs)
	if err != nil {
		respond(w, nil, err)
		return
	}
	items := []role{}
	for rows.Next() {
		var v role
		if err = rows.Scan(&v.ID, &v.Name, &v.Kind); err != nil {
			break
		}
		items = append(items, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		respond(w, nil, err)
		return
	}
	// App/team grants are usable only after environment.view admits the user
	// to this instance environment. Do not count subjects whose only access is
	// in another environment or whose unrelated grant happens to be global.
	for i := range items {
		err = s.Store.DB.QueryRow(r.Context(), `SELECT count(DISTINCT u.id) FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
 SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id) a
 ON a.user_id=u.id WHERE a.role_id=$1 AND NOT u.disabled AND u.deleted_at IS NULL
 AND u.source<>'settings_demo' AND EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
 AND g.module='environment' AND g.action='view'
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($2::text[]))))`, items[i].ID, envs).Scan(&items[i].Members)
		if err != nil {
			respond(w, nil, err)
			return
		}
	}
	respond(w, map[string]any{"items": items}, nil)
}

func (s *Service) role(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	v, revision, err := s.getRole(r.Context(), r.PathValue("id"), httpapi.CurrentUser(r), roleSessionToken(r), envs, englishLanguage(r.Header.Get("Accept-Language")))
	if err == nil {
		w.Header().Set("X-Role-Matrix-Revision", strconv.FormatInt(revision, 10))
		w.Header().Set("Cache-Control", "private, no-store")
	}
	respond(w, v, err)
}

type grant struct {
	Module    string `json:"module"`
	Action    string `json:"action"`
	ScopeKind string `json:"scope_kind"`
	ScopeID   string `json:"scope_id"`
	Assurance string `json:"assurance"`
}

func roleSessionToken(r *http.Request) string {
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (s *Service) getRole(ctx context.Context, id string, actor core.User, sessionToken string, envs []string, english bool) (roleDetail, int64, error) {
	v := roleDetail{MemberNames: []string{}, Scope: []string{}, Matrix: []matrixRow{}}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return v, 0, err
	}
	defer tx.Rollback(ctx)
	// Writers of accounts, legacy associations and V3 grants acquire these
	// locks exclusively in this order. Recheck authority after queued writers.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return v, 0, err
		}
	}
	if err = store.LockActiveSession(ctx, tx, sessionToken, actor.ID); err != nil {
		return v, 0, err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT source<>'settings_demo' FROM users WHERE id=$1`, actor.ID).Scan(&eligible); err != nil {
		return v, 0, err
	}
	if !eligible {
		return v, 0, store.ErrForbidden
	}
	policy := &rbac.V3Policy{Store: s.Store}
	for _, env := range envs {
		if err = policy.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: "roles", Action: "view", EnvironmentID: env}); err != nil {
			return v, 0, err
		}
	}
	var global bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='roles' AND action='view'
 AND scope_kind='global' AND scope_id='*') AND EXISTS (
 SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1 AND module='environment' AND action='view'
 AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&global); err != nil {
		return v, 0, err
	}
	// The role, visible members and visible grants come from one statement's
	// MVCC snapshot. This also prevents mixed app/team mapping states.
	var membersJSON, grantsJSON []byte
	var revision int64
	err = tx.QueryRow(ctx, `SELECT id,name,description,v3_kind,v3_matrix_revision,
 COALESCE((SELECT jsonb_agg(member.name ORDER BY member.name,member.id) FROM (
  SELECT DISTINCT u.id,u.name FROM users u
  JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
   SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id) a
   ON a.user_id=u.id WHERE a.role_id=r.id AND NOT u.disabled AND u.deleted_at IS NULL
  AND u.source<>'settings_demo' AND EXISTS (
   SELECT 1 FROM v3_effective_role_grants admission WHERE admission.user_id=u.id
   AND admission.module='environment' AND admission.action='view'
   AND (admission.scope_kind='global' OR
    (admission.scope_kind='environment' AND admission.scope_id=ANY($3::text[]))))
 ) member), '[]'::jsonb),
 COALESCE((SELECT jsonb_agg(jsonb_build_object('module',g.module,'action',g.action,
  'scope_kind',g.scope_kind,'scope_id',g.scope_id,'assurance',g.assurance)
  ORDER BY g.module,g.action,g.scope_kind,g.scope_id)
  FROM v3_role_grants g WHERE g.role_id=r.id AND (g.scope_kind='global'
  OR (g.scope_kind='environment' AND g.scope_id=ANY($3::text[]))
  OR (g.scope_kind='team' AND EXISTS (
   SELECT 1 FROM applications a JOIN v3_application_environments ae ON ae.application_id=a.id
   JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
   WHERE a.owner_team_id=g.scope_id AND NOT a.archived AND ae.environment_id=ANY($3::text[])))
  OR (g.scope_kind='application' AND EXISTS (
   SELECT 1 FROM v3_application_environments ae
   JOIN applications a ON a.id=ae.application_id AND NOT a.archived
   JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
   WHERE ae.application_id=g.scope_id AND ae.environment_id=ANY($3::text[]))))), '[]'::jsonb)
 FROM rbac_roles r
 WHERE r.id=$1 AND (($2 AND NOT EXISTS (SELECT 1 FROM v3_role_grants unassigned WHERE unassigned.role_id=r.id))
 OR EXISTS (SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($3::text[]))
 OR (g.scope_kind='team' AND EXISTS (
  SELECT 1 FROM applications a
  JOIN v3_application_environments ae ON ae.application_id=a.id
  JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
  WHERE a.owner_team_id=g.scope_id AND NOT a.archived AND ae.environment_id=ANY($3::text[])))
 OR (g.scope_kind='application' AND EXISTS (
  SELECT 1 FROM v3_application_environments ae
  JOIN applications a ON a.id=ae.application_id AND NOT a.archived
  JOIN application_environments e ON e.application_id=a.id AND e.key=ae.environment_id AND NOT e.archived
  WHERE ae.application_id=g.scope_id AND ae.environment_id=ANY($3::text[]))))))`, id, global, envs).
		Scan(&v.ID, &v.Name, &v.Desc, &v.Kind, &revision, &membersJSON, &grantsJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, 0, store.ErrNotFound
	}
	if err != nil {
		return v, 0, err
	}
	if err = json.Unmarshal(membersJSON, &v.MemberNames); err != nil {
		return v, 0, err
	}
	v.Members = len(v.MemberNames)
	var grants []grant
	if err = json.Unmarshal(grantsJSON, &grants); err != nil {
		return v, 0, err
	}
	if v.Kind == "machine" {
		for _, g := range grants {
			if g.Action == "approve" {
				return v, 0, errInvalidStoredRoleGrant
			}
		}
	}
	if err := validateMatrixProjection(grants); err != nil {
		return v, 0, err
	}
	scopes := map[string]bool{}
	for _, g := range grants {
		if g.ScopeKind == "global" {
			scopes["global"] = true
		} else {
			scopes[g.ScopeKind+":"+g.ScopeID] = true
		}
	}
	for scope := range scopes {
		v.Scope = append(v.Scope, scope)
	}
	sort.Strings(v.Scope)
	v.Actions = []string{"查看", "创建 / 编辑", "删除", "批准", "管理"}
	if english {
		v.Actions = []string{"View", "Create / edit", "Delete", "Approve", "Manage"}
	}
	v.Matrix = displayMatrix(grants, english)
	v.Notes = "机器身份不能审批；审批在通行密钥二次验证接入前不可执行，发起人与批准人不能相同。"
	if english {
		v.Notes = "Machine identities cannot approve. Approval remains unavailable until passkey step-up verification is implemented, and the author cannot approve their own proposal."
	}
	for _, g := range grants {
		if !matrixModule(g.Module) {
			if english {
				v.Notes += " Other platform grants are managed outside this matrix."
			} else {
				v.Notes += " 其他平台授权不在此矩阵中管理。"
			}
			break
		}
	}
	if english {
		v.Notes += " Only grants in the selected authorized environments are shown."
	} else {
		v.Notes += " 此处仅显示所选且已获授权环境内的授权。"
	}
	// A later role or grant read may outlive the Cookie's expiry instant.
	if err := store.LockActiveSession(ctx, tx, sessionToken, actor.ID); err != nil {
		return v, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return v, 0, err
	}
	return v, revision, nil
}

func matrixModule(module string) bool {
	for _, resource := range matrixCatalog {
		if contains(resource.Modules, module) {
			return true
		}
	}
	return false
}

func displayMatrix(grants []grant, english bool) []matrixRow {
	rows := make([]matrixRow, 0, len(matrixCatalog))
	for _, resource := range matrixCatalog {
		row := matrixRow{Resource: label(resource, english), Grants: make([]int, len(matrixActions))}
		for j, action := range matrixActions {
			for _, g := range grants {
				if g.Action != action || !contains(resource.Modules, g.Module) {
					continue
				}
				if g.Assurance == "passkey" {
					row.Grants[j] = 2
				} else if row.Grants[j] == 0 {
					row.Grants[j] = 1
				}
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func contains(xs []string, value string) bool {
	for _, x := range xs {
		if x == value {
			return true
		}
	}
	return false
}

func (s *Service) members(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	if !contains([]string{"all", "admin", "no_mfa", "disabled"}, filter) || len(r.URL.Query().Get("q")) > 256 {
		httpapi.Error(w, 422, "invalid_filter", "Invalid member filter")
		return
	}
	v, err := s.getMembers(r.Context(), httpapi.CurrentUser(r), envs, filter, r.URL.Query().Get("q"), englishLanguage(r.Header.Get("Accept-Language")))
	respond(w, v, err)
}

// The V3 page needs factor enrollment, a grace policy, group membership and
// successful-login history that the current account store cannot prove. Keep
// the useful directory projection explicitly partial instead of inventing a
// clean MFA count, zero-day grace period or most-recent login.
func (s *Service) getMembers(ctx context.Context, actor core.User, envs []string, filter, q string, english bool) (membersView, error) {
	v := membersView{
		Org: []orgNode{}, Groups: []orgNode{}, Items: []member{},
		UnavailableFields: []string{"groups", "org_hierarchy", "legacy_role_mapping", "items[].mfa", "items[].last_login", "counts.no_mfa", "mfa_grace_days", "directory_sync_source"},
	}
	tx, err := s.Store.DB.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	// Account, legacy role-association and V3 matrix writers that change these
	// grants take these locks in this order. Waiting before the authorization
	// recheck closes their middleware-to-handler revocation window.
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return v, err
		}
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL)`, actor.ID).Scan(&active); err != nil {
		return v, err
	}
	if !active {
		return v, store.ErrForbidden
	}
	policy := &rbac.V3Policy{Store: s.Store}
	for _, env := range envs {
		if err = policy.AuthorizeWithTx(ctx, tx, actor, rbac.V3Decision{Module: "members", Action: "view", EnvironmentID: env}); err != nil {
			return v, err
		}
	}
	var global bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='members' AND action='view' AND scope_kind='global' AND scope_id='*')
 AND EXISTS (SELECT 1 FROM v3_effective_role_grants WHERE user_id=$1
  AND module='environment' AND action='view' AND scope_kind='global' AND scope_id='*')`, actor.ID).Scan(&global); err != nil {
		return v, err
	}
	if filter == "no_mfa" {
		return v, errMemberMFAUnavailable
	}
	// A single statement supplies both visible accounts and their projected
	// roles. Counts and the flat organization list derive from those same rows,
	// so an app/team update cannot mix two different list snapshots.
	rows, err := tx.Query(ctx, `SELECT u.id,u.name,u.email,COALESCE(t.id,''),
 COALESCE(t.name,u.department,''),COALESCE(u.phone,''),u.source,u.disabled,u.v3_identity_kind,
 COALESCE(visible_role.id,''),COALESCE(visible_role.name,'')
 FROM users u LEFT JOIN teams t ON t.id=u.department_id
 LEFT JOIN LATERAL (
  SELECT DISTINCT r.id,r.name FROM rbac_roles r
  JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
   SELECT member.id,dr.role_id FROM users member JOIN rbac_department_roles dr ON dr.department_id=member.department_id) assigned
   ON assigned.role_id=r.id
  WHERE assigned.user_id=u.id AND EXISTS (
   SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id AND (
    g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($2::text[]))
    OR (g.scope_kind='application' AND EXISTS (
     SELECT 1 FROM v3_application_environments ae
     JOIN applications app ON app.id=ae.application_id AND NOT app.archived
     JOIN application_environments e ON e.application_id=app.id AND e.key=ae.environment_id AND NOT e.archived
     WHERE ae.application_id=g.scope_id AND ae.environment_id=ANY($2::text[])))
    OR (g.scope_kind='team' AND EXISTS (
     SELECT 1 FROM applications app
     JOIN v3_application_environments ae ON ae.application_id=app.id
     JOIN application_environments e ON e.application_id=app.id AND e.key=ae.environment_id AND NOT e.archived
     WHERE app.owner_team_id=g.scope_id AND NOT app.archived AND ae.environment_id=ANY($2::text[])))
   ))
 ) visible_role ON true
 WHERE u.deleted_at IS NULL AND u.source<>'settings_demo'
 AND (($1 AND NOT EXISTS (
  SELECT 1 FROM (
   SELECT ur.user_id,ur.role_id FROM rbac_user_roles ur
   UNION SELECT member.id,dr.role_id FROM users member JOIN rbac_department_roles dr ON dr.department_id=member.department_id
  ) assigned JOIN v3_role_grants admission ON admission.role_id=assigned.role_id
  WHERE assigned.user_id=u.id AND admission.module='environment' AND admission.action='view'
   AND admission.scope_kind IN ('global','environment')
 )) OR EXISTS (
  SELECT 1 FROM (
   SELECT ur.user_id,ur.role_id FROM rbac_user_roles ur
   UNION SELECT member.id,dr.role_id FROM users member JOIN rbac_department_roles dr ON dr.department_id=member.department_id
  ) assigned JOIN v3_role_grants admission ON admission.role_id=assigned.role_id
  WHERE assigned.user_id=u.id AND admission.module='environment' AND admission.action='view'
   AND (admission.scope_kind='global' OR (admission.scope_kind='environment' AND admission.scope_id=ANY($2::text[])))
 ))
 ORDER BY u.name,u.id,visible_role.name,visible_role.id`, global, envs)
	if err != nil {
		return v, err
	}
	all := []member{}
	indices := map[string]int{}
	adminIDs := map[string]bool{}
	deptCounts := map[string]int{}
	deptNames := map[string]string{}
	for rows.Next() {
		var m member
		var deptID, source, kind, roleID, roleName string
		var disabled bool
		if err = rows.Scan(&m.ID, &m.Name, &m.Email, &deptID, &m.Department, &m.Phone, &source, &disabled, &kind, &roleID, &roleName); err != nil {
			break
		}
		index, known := indices[m.ID]
		if !known {
			mappedSource, validSource := sourceLabel(source)
			if !validSource {
				rows.Close()
				return v, errUnsupportedMemberSource
			}
			m.Source = mappedSource
			m.Roles = []string{}
			m.MFA = "na"
			m.Agent = kind == "machine"
			m.Status = "active"
			if disabled {
				m.Status = "disabled"
			}
			index = len(all)
			indices[m.ID] = index
			all = append(all, m)
			if deptID != "" {
				deptCounts[deptID]++
				deptNames[deptID] = m.Department
			}
		}
		if roleID != "" {
			all[index].Roles = append(all[index].Roles, roleName)
			if roleID == "admin" {
				adminIDs[m.ID] = true
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return v, err
	}
	if err = tx.Commit(ctx); err != nil {
		return v, err
	}
	for _, m := range all {
		v.Counts.All++
		if adminIDs[m.ID] {
			v.Counts.Admin++
		}
		if m.Status == "disabled" {
			v.Counts.Disabled++
		}
		if (filter == "all" || filter == "admin" && adminIDs[m.ID] || filter == "disabled" && m.Status == "disabled") &&
			(q == "" || searchtext.Match(m.Name+" "+m.Email+" "+m.Phone+" "+m.Department, q)) {
			v.Items = append(v.Items, m)
		}
	}
	rootName := "可见成员"
	if english {
		rootName = "Visible members"
	}
	v.Org = append(v.Org, orgNode{ID: "all", Name: rootName, Count: len(all), Kind: "org"})
	for id, count := range deptCounts {
		v.Org = append(v.Org, orgNode{ID: id, Name: deptNames[id], Count: count, Depth: 1, Kind: "team"})
	}
	sort.Slice(v.Org[1:], func(i, j int) bool {
		a, b := v.Org[i+1], v.Org[j+1]
		return a.Name < b.Name || a.Name == b.Name && a.ID < b.ID
	})
	return v, nil
}

func sourceLabel(v string) (string, bool) {
	if contains([]string{"feishu", "ldap", "invite"}, v) {
		return v, true
	}
	if v == "local" || v == "system" {
		return "system", true
	}
	return "", false
}

func matrixForRole(grants []grant) []matrixRow { return displayMatrix(grants, false) }

func canonicalMatrix(rows []matrixRow) (map[string][]int, error) {
	if len(rows) != len(matrixCatalog) {
		return nil, errInvalidMatrix
	}
	result := map[string][]int{}
	for _, row := range rows {
		def, ok := catalogResource(strings.TrimSpace(row.Resource))
		if !ok || len(row.Grants) != len(matrixActions) || result[def.Code] != nil {
			return nil, errInvalidMatrix
		}
		for i, cell := range row.Grants {
			if cell < 0 || cell > 2 || cell == 2 && matrixActions[i] != "approve" {
				return nil, errInvalidMatrix
			}
		}
		result[def.Code] = row.Grants
	}
	return result, nil
}
