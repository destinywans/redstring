// Package v3admin serves the V3 member directory and editable role matrix.
// Legacy RBAC records remain as historical inputs, but only v3_role_grants
// represent grants displayed and changed by these handlers.
package v3admin

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/searchtext"
	"github.com/wans/aiops-canvas/internal/store"
)

type Service struct{ Store *store.Store }

func New(s *store.Store) *Service { return &Service{Store: s} }

var errInvalidMatrix = errors.New("invalid role matrix")
var errScopedCell = errors.New("scoped grant cannot be edited in summary matrix")
var errPartialMatrix = errors.New("role matrix cannot represent a partial resource group")
var errMachineApproval = errors.New("machine identity cannot approve")
var errLastRoleManager = errors.New("cannot remove last role manager")
var errUnsupportedMemberSource = errors.New("member source is not represented in V3 contract")

func respond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "Role not found")
	case errors.Is(err, errInvalidMatrix):
		httpapi.Error(w, 422, "invalid_matrix", "Matrix must contain each known resource once with five valid operation cells")
	case errors.Is(err, errMachineApproval):
		httpapi.Error(w, 422, "machine_approval_forbidden", "Machine identities cannot receive approval grants")
	case errors.Is(err, errScopedCell):
		httpapi.Error(w, 409, "scope_editor_required", "Scoped grants cannot be changed by the summary matrix")
	case errors.Is(err, errPartialMatrix):
		httpapi.Error(w, 409, "matrix_editor_required", "Grouped resource grants differ and require a dedicated editor")
	case errors.Is(err, errLastRoleManager):
		httpapi.Error(w, 409, "last_role_manager", "At least one active human role manager is required")
	case errors.Is(err, errUnsupportedMemberSource):
		httpapi.Error(w, 503, "member_source_unmapped", "Member directory contains an unmapped identity source")
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

// A global reader may inspect unassigned accounts/roles. A reader with
// environment-limited rights sees only subjects whose effective role grants
// overlap the authorized environment set.
func (s *Service) hasGlobalGrant(ctx context.Context, actor, module, action string) (bool, error) {
	var ok bool
	err := s.Store.DB.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM v3_effective_role_grants
 WHERE user_id=$1 AND module=$2 AND action=$3
 AND scope_kind='global' AND scope_id='*')`, actor, module, action).Scan(&ok)
	return ok, err
}

func (s *Service) roles(w http.ResponseWriter, r *http.Request) {
	envs, ok := scoped(r)
	if !ok {
		httpapi.Error(w, 422, "environment_required", "Select an environment")
		return
	}
	global, err := s.hasGlobalGrant(r.Context(), httpapi.CurrentUser(r).ID, "roles", "view")
	if err != nil {
		respond(w, nil, err)
		return
	}
	rows, err := s.Store.DB.Query(r.Context(), `SELECT id,name,v3_kind FROM rbac_roles r
 WHERE $1 OR EXISTS (SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($2::text[]))))
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
	for i := range items {
		err = s.Store.DB.QueryRow(r.Context(), `SELECT count(DISTINCT u.id) FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
 SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id) a
 ON a.user_id=u.id WHERE a.role_id=$1 AND NOT u.disabled AND u.deleted_at IS NULL
 AND u.source<>'settings_demo' AND ($2 OR EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($3::text[])))))`, items[i].ID, global, envs).Scan(&items[i].Members)
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
	global, err := s.hasGlobalGrant(r.Context(), httpapi.CurrentUser(r).ID, "roles", "view")
	if err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.getRole(r.Context(), r.PathValue("id"), envs, global, englishLanguage(r.Header.Get("Accept-Language")))
	respond(w, v, err)
}

type grant struct {
	Module    string
	Action    string
	ScopeKind string
	ScopeID   string
	Assurance string
}

func (s *Service) getRole(ctx context.Context, id string, envs []string, global, english bool) (roleDetail, error) {
	v := roleDetail{MemberNames: []string{}, Scope: []string{}, Matrix: []matrixRow{}}
	err := s.Store.DB.QueryRow(ctx, `SELECT id,name,description,v3_kind FROM rbac_roles r
 WHERE r.id=$1 AND ($2 OR EXISTS (SELECT 1 FROM v3_role_grants g WHERE g.role_id=r.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($3::text[])))))`, id, global, envs).
		Scan(&v.ID, &v.Name, &v.Desc, &v.Kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, store.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	rows, err := s.Store.DB.Query(ctx, `SELECT DISTINCT u.id,u.name FROM users u
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
 SELECT x.id,dr.role_id FROM users x JOIN rbac_department_roles dr ON dr.department_id=x.department_id) a
 ON a.user_id=u.id WHERE a.role_id=$1 AND NOT u.disabled AND u.deleted_at IS NULL
 AND u.source<>'settings_demo' AND ($2 OR EXISTS (
 SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($3::text[])))))
 ORDER BY u.name,u.id`, id, global, envs)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var memberID, name string
		if err = rows.Scan(&memberID, &name); err != nil {
			break
		}
		v.MemberNames = append(v.MemberNames, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return v, err
	}
	v.Members = len(v.MemberNames)
	grants, err := s.roleGrants(ctx, id)
	if err != nil {
		return v, err
	}
	visible := map[string]bool{}
	for _, e := range envs {
		visible[e] = true
	}
	if !global {
		filtered := grants[:0]
		for _, g := range grants {
			if g.ScopeKind == "global" || g.ScopeKind == "environment" && visible[g.ScopeID] {
				filtered = append(filtered, g)
			}
		}
		grants = filtered
	}
	scopes := map[string]bool{}
	for _, g := range grants {
		if g.ScopeKind == "global" {
			scopes["global"] = true
		} else if g.ScopeKind != "environment" || visible[g.ScopeID] {
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
	return v, nil
}

func (s *Service) roleGrants(ctx context.Context, roleID string) ([]grant, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT module,action,scope_kind,scope_id,assurance
 FROM v3_role_grants WHERE role_id=$1 ORDER BY module,action,scope_kind,scope_id`, roleID)
	if err != nil {
		return nil, err
	}
	grants := []grant{}
	for rows.Next() {
		var g grant
		if err = rows.Scan(&g.Module, &g.Action, &g.ScopeKind, &g.ScopeID, &g.Assurance); err != nil {
			break
		}
		grants = append(grants, g)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	return grants, err
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
	global, err := s.hasGlobalGrant(r.Context(), httpapi.CurrentUser(r).ID, "members", "view")
	if err != nil {
		respond(w, nil, err)
		return
	}
	v, err := s.getMembers(r.Context(), envs, global, filter, r.URL.Query().Get("q"), englishLanguage(r.Header.Get("Accept-Language")))
	respond(w, v, err)
}

func (s *Service) getMembers(ctx context.Context, envs []string, global bool, filter, q string, english bool) (membersView, error) {
	v := membersView{Org: []orgNode{}, Groups: []orgNode{}, Items: []member{}, Counts: map[string]int{"all": 0, "admin": 0, "no_mfa": 0, "disabled": 0}}
	rows, err := s.Store.DB.Query(ctx, `SELECT u.id,u.name,u.email,COALESCE(u.department_id,''),
 COALESCE(t.name,u.department,''),COALESCE(u.phone,''),u.source,u.disabled,u.v3_identity_kind
 FROM users u LEFT JOIN teams t ON t.id=u.department_id
 WHERE u.deleted_at IS NULL AND u.source<>'settings_demo'
 AND ($1 OR EXISTS (SELECT 1 FROM v3_effective_role_grants g WHERE g.user_id=u.id
 AND (g.scope_kind='global' OR (g.scope_kind='environment' AND g.scope_id=ANY($2::text[])))))
 ORDER BY u.name,u.id`, global, envs)
	if err != nil {
		return v, err
	}
	all := []member{}
	deptCounts := map[string]int{}
	for rows.Next() {
		var m member
		var deptID, source, kind string
		var disabled bool
		if err = rows.Scan(&m.ID, &m.Name, &m.Email, &deptID, &m.Department, &m.Phone, &source, &disabled, &kind); err != nil {
			break
		}
		m.Roles = []string{}
		mappedSource, validSource := sourceLabel(source)
		if !validSource {
			rows.Close()
			return v, errUnsupportedMemberSource
		}
		m.Source = mappedSource
		// No verified factor registry exists yet. "na" means unknown here;
		// reporting "none" would falsely claim the user has no second factor.
		m.MFA = "na"
		m.Agent = kind == "machine"
		m.Status = "active"
		if disabled {
			m.Status = "disabled"
		}
		// Session expiry is not a login timestamp. An empty value is truthful.
		m.LastLogin = ""
		all = append(all, m)
		if deptID != "" {
			deptCounts[deptID]++
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return v, err
	}
	for i := range all {
		roleRows, e := s.Store.DB.Query(ctx, `SELECT DISTINCT r.id,r.name FROM rbac_roles r
 JOIN (SELECT user_id,role_id FROM rbac_user_roles UNION
 SELECT u.id,dr.role_id FROM users u JOIN rbac_department_roles dr ON dr.department_id=u.department_id) a
 ON a.role_id=r.id WHERE a.user_id=$1 ORDER BY r.name,r.id`, all[i].ID)
		if e != nil {
			return v, e
		}
		admin := false
		for roleRows.Next() {
			var id, name string
			if e = roleRows.Scan(&id, &name); e != nil {
				break
			}
			all[i].Roles = append(all[i].Roles, name)
			if id == "admin" {
				admin = true
			}
		}
		if e == nil {
			e = roleRows.Err()
		}
		roleRows.Close()
		if e != nil {
			return v, e
		}
		v.Counts["all"]++
		if admin {
			v.Counts["admin"]++
		}
		if all[i].MFA == "none" && all[i].Status == "active" {
			v.Counts["no_mfa"]++
		}
		if all[i].Status == "disabled" {
			v.Counts["disabled"]++
		}
		if (filter == "all" || filter == "admin" && admin || filter == "no_mfa" && all[i].MFA == "none" && all[i].Status == "active" || filter == "disabled" && all[i].Status == "disabled") &&
			(q == "" || searchtext.Match(all[i].Name+" "+all[i].Email+" "+all[i].Phone+" "+all[i].Department, q)) {
			v.Items = append(v.Items, all[i])
		}
	}
	rootName := "全公司"
	if english {
		rootName = "Company"
	}
	v.Org = append(v.Org, orgNode{ID: "all", Name: rootName, Count: len(all), Kind: "org"})
	teamRows, err := s.Store.DB.Query(ctx, `SELECT id,name FROM teams ORDER BY name,id`)
	if err != nil {
		return v, err
	}
	for teamRows.Next() {
		var id, name string
		if err = teamRows.Scan(&id, &name); err != nil {
			break
		}
		if count := deptCounts[id]; count > 0 {
			v.Org = append(v.Org, orgNode{ID: id, Name: name, Count: count, Depth: 1, Kind: "team"})
		}
	}
	if err == nil {
		err = teamRows.Err()
	}
	teamRows.Close()
	return v, err
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
