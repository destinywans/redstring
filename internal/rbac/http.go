package rbac

import (
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"strconv"
	"strings"
)

func respond(w http.ResponseWriter, v any, e error) {
	if e == nil {
		httpapi.WriteJSON(w, 200, v)
		return
	}
	switch {
	case errors.Is(e, ErrRoleNameConflict):
		httpapi.Error(w, 409, "role_name_conflict", "角色名称已存在，请使用其他名称。")
	case errors.Is(e, ErrPermissionNameConflict):
		httpapi.Error(w, 409, "permission_name_conflict", "权限名称已存在，请使用其他名称。")
	case errors.Is(e, ErrLegacyMembership):
		httpapi.Error(w, 409, "legacy_role_membership", "此用户的基础角色由成员管理维护，请在成员管理中调整。")
	case errors.Is(e, ErrLastManager):
		httpapi.Error(w, 409, "last_rbac_manager", "请至少保留一位启用且具有角色与权限管理权限的成员。")
	case errors.Is(e, store.ErrConflict):
		httpapi.Error(w, 409, "revision_conflict", "Changed by another editor; reload before saving")
	case errors.Is(e, store.ErrInvalidReference):
		httpapi.Error(w, 422, "invalid_rbac", "Invalid action, scope, target or association delta")
	case errors.Is(e, store.ErrNotFound):
		httpapi.Error(w, 404, "not_found", "RBAC record not found")
	case errors.Is(e, store.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Explicit management permission required")
	default:
		httpapi.Error(w, 503, "rbac_unavailable", "RBAC storage unavailable")
	}
}
func paging(r *http.Request) (int, int, error) {
	page, size := 1, 10
	var e error
	if s := r.URL.Query().Get("page"); s != "" {
		page, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, store.ErrInvalidReference
		}
	}
	if s := r.URL.Query().Get("page_size"); s != "" {
		size, e = strconv.Atoi(s)
		if e != nil {
			return 0, 0, store.ErrInvalidReference
		}
	}
	if page < 1 || page > 1000000 || size < 1 || size > 100 {
		return 0, 0, store.ErrInvalidReference
	}
	return page, size, nil
}
func (s *Service) Register(api *httpapi.Server) {
	api.Register("GET /api/v1/rbac/me", "viewer", func(w http.ResponseWriter, r *http.Request) {
		objects := []string{}
		if s.Store.AuthorizeCapability(r.Context(), httpapi.CurrentUser(r), "manage", "management", "rbac") == nil {
			for _, object := range ManagementObjects {
				objects = append(objects, object["id"])
			}
		}
		respond(w, map[string]any{"management_objects": objects}, nil)
	})

	register := func(pattern string, h http.HandlerFunc) {
		api.Register(pattern, "viewer", func(w http.ResponseWriter, r *http.Request) {
			if e := s.Store.AuthorizeCapability(r.Context(), httpapi.CurrentUser(r), "manage", "management", "rbac"); e != nil {
				respond(w, nil, e)
				return
			}
			h(w, r)
		})
	}
	register("GET /api/v1/rbac/catalog", func(w http.ResponseWriter, r *http.Request) {
		options := map[string]any{"actions": Actions}
		for _, kind := range []string{"teams"} {
			rows, e := s.Store.DB.Query(r.Context(), "SELECT id,name FROM "+kind+" ORDER BY name,id")
			if e != nil {
				respond(w, nil, e)
				return
			}
			items := []map[string]string{}
			for rows.Next() {
				var id, name string
				if e = rows.Scan(&id, &name); e != nil {
					rows.Close()
					respond(w, nil, e)
					return
				}
				items = append(items, map[string]string{"id": id, "name": name})
			}
			rows.Close()
			if e = rows.Err(); e != nil {
				respond(w, nil, e)
				return
			}
			key := kind
			if kind == "teams" {
				key = "departments"
			}
			options[key] = items
		}
		respond(w, options, nil)
	})
	routes := map[string][]string{
		"roles":       {"GET /api/v1/rbac/roles", "GET /api/v1/rbac/roles/{id}", "POST /api/v1/rbac/roles", "PATCH /api/v1/rbac/roles/{id}", "DELETE /api/v1/rbac/roles/{id}"},
		"permissions": {"GET /api/v1/rbac/permissions", "GET /api/v1/rbac/permissions/{id}", "POST /api/v1/rbac/permissions", "PATCH /api/v1/rbac/permissions/{id}", "DELETE /api/v1/rbac/permissions/{id}"},
	}

	for _, kind := range []string{"roles", "permissions"} {
		kind := kind
		register(routes[kind][0], func(w http.ResponseWriter, r *http.Request) { s.list(w, r, kind) })
		register(routes[kind][1], func(w http.ResponseWriter, r *http.Request) {
			if kind == "roles" {
				v, e := s.Role(r.Context(), r.PathValue("id"))
				respond(w, v, e)
			} else {
				v, e := s.Permission(r.Context(), r.PathValue("id"))
				respond(w, v, e)
			}
		})
		save := func(w http.ResponseWriter, r *http.Request) {
			if kind == "roles" {
				var v Role
				if !httpapi.Decode(w, r, &v) {
					return
				}
				v.ID = r.PathValue("id")
				v, e := s.SaveRole(r.Context(), v)
				respond(w, v, e)
			} else {
				var v Permission
				if !httpapi.Decode(w, r, &v) {
					return
				}
				v.ID = r.PathValue("id")
				v, e := s.SavePermission(r.Context(), v)
				respond(w, v, e)
			}
		}
		register(routes[kind][2], save)
		register(routes[kind][3], save)
		register(routes[kind][4], func(w http.ResponseWriter, r *http.Request) {
			rev, e := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
			if e != nil {
				respond(w, nil, store.ErrInvalidReference)
				return
			}
			e = s.Delete(r.Context(), kind, r.PathValue("id"), rev)
			respond(w, map[string]bool{"deleted": e == nil}, e)
		})
	}
	register("GET /api/v1/rbac/roles/{id}/associations/{kind}", s.candidates)
	register("PUT /api/v1/rbac/roles/{id}/associations", func(w http.ResponseWriter, r *http.Request) {
		var v Associations
		if !httpapi.Decode(w, r, &v) {
			return
		}
		out, e := s.SaveAssociations(r.Context(), r.PathValue("id"), v)
		respond(w, out, e)
	})
	register("GET /api/v1/rbac/permissions/{id}/resources", s.resourceTree)
	register("GET /api/v1/rbac/permissions/{id}/resource-tree", s.resourceTree)
	register("PUT /api/v1/rbac/permissions/{id}/resources", func(w http.ResponseWriter, r *http.Request) {
		var v ResourceDelta
		if !httpapi.Decode(w, r, &v) {
			return
		}
		out, e := s.SaveResources(r.Context(), r.PathValue("id"), v)
		respond(w, out, e)
	})
	register("GET /api/v1/rbac/departments", s.departments)
	register("POST /api/v1/rbac/departments", s.saveDepartment)
	register("PATCH /api/v1/rbac/departments/{id}", s.saveDepartment)
	register("PATCH /api/v1/rbac/users/{id}/department", s.userDepartment)
}

// Accept both form arrays (a,b) and repeated parameters, keeping a bounded
// vocabulary and an explicit empty slice so SQL cardinality treats it as no filter.
func permissionFilterValues(r *http.Request, key string, allowed ...string) ([]string, error) {
	valid := make(map[string]bool, len(allowed))
	for _, value := range allowed {
		valid[value] = true
	}
	values := []string{}
	seen := map[string]bool{}
	for _, raw := range r.URL.Query()[key] {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			if !valid[value] {
				return nil, store.ErrInvalidReference
			}
			if !seen[value] {
				values = append(values, value)
				seen[value] = true
			}
		}
	}
	return values, nil
}

func (s *Service) list(w http.ResponseWriter, r *http.Request, kind string) {
	page, size, e := paging(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query()
	args := []any{strings.TrimSpace(q.Get("q")), q.Get("builtin")}
	from := "rbac_roles r"
	filter := ` WHERE (aiops_search_match(r.id,$1) OR aiops_search_match(r.name,$1) OR aiops_search_match(r.description,$1)) AND ($2='' OR r.builtin::text=$2)`
	selectSQL := roleSelect
	if kind == "permissions" {
		actions, err := permissionFilterValues(r, "action", "view", "update", "approve", "delete")
		if err != nil {
			respond(w, nil, err)
			return
		}
		resourceKinds, err := permissionFilterValues(r, "resource_kind", "cluster", "node", "deployment", "namespace", "host", "network", "storage", "redis", "mysql", "kafka")
		if err != nil {
			respond(w, nil, err)
			return
		}
		scope := strings.TrimSpace(q.Get("scope_type"))
		if scope != "" && scope != "resource" {
			respond(w, nil, store.ErrInvalidReference)
			return
		}
		args = append(args, actions, resourceKinds, scope)
		from = "rbac_permissions p"
		filter = ` WHERE (aiops_search_match(p.id,$1) OR aiops_search_match(p.name,$1) OR aiops_search_match(p.description,$1)) AND ($2='' OR p.builtin::text=$2)
 AND ((cardinality($3::text[])=0 AND cardinality($4::text[])=0 AND $5='') OR EXISTS(
 SELECT 1 FROM rbac_permission_resources x JOIN cmdb_resources resource ON resource.id=x.resource_id
 WHERE x.permission_id=p.id AND x.scope_type='resource' AND rbac_is_infrastructure_kind(resource.kind)
 AND x.action IN ('view','update','approve','delete') AND (cardinality($3::text[])=0 OR x.action=ANY($3::text[])) AND (cardinality($4::text[])=0 OR resource.kind=ANY($4::text[])) AND ($5='' OR x.scope_type=$5)))`
		selectSQL = permissionSelect
	}
	var total int
	if e = s.Store.DB.QueryRow(r.Context(), "SELECT count(*) FROM "+from+filter, args...).Scan(&total); e != nil {
		respond(w, nil, e)
		return
	}
	last := (total + size - 1) / size
	if last < 1 {
		last = 1
	}
	if page > last {
		page = last
	}
	limitArg, offsetArg := len(args)+1, len(args)+2
	args = append(args, size, (page-1)*size)
	rows, e := s.Store.DB.Query(r.Context(), selectSQL+filter+" ORDER BY name,id LIMIT $"+strconv.Itoa(limitArg)+" OFFSET $"+strconv.Itoa(offsetArg), args...)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	items := []any{}
	for rows.Next() {
		if kind == "roles" {
			v, err := scanRole(rows)
			if err != nil {
				respond(w, nil, err)
				return
			}
			items = append(items, v)
		} else {
			v, err := scanPermission(rows)
			if err != nil {
				respond(w, nil, err)
				return
			}
			items = append(items, v)
		}
	}
	respond(w, map[string]any{"items": items, "page": page, "page_size": size, "total": total}, rows.Err())
}
func splitIDs(r *http.Request, key string) []string {
	out := []string{}
	for _, v := range r.URL.Query()[key] {
		for _, id := range strings.Split(v, ",") {
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}
func (s *Service) candidates(w http.ResponseWriter, r *http.Request) {
	page, size, e := paging(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	id, kind := r.PathValue("id"), r.PathValue("kind")
	rev := int64(0)
	var source string
	if kind != "" {
		v, e := s.Role(r.Context(), id)
		if e != nil {
			respond(w, nil, e)
			return
		}
		rev = v.Revision
		switch kind {
		case "permissions":
			source = `SELECT p.id,p.name,p.description,EXISTS(SELECT 1 FROM rbac_role_permissions x WHERE x.role_id=$1 AND x.permission_id=p.id) selected,false inherited,'[]'::jsonb source_departments,'' email,'' action,'' scope_type,'' kind,'' application_id,'' application_name,'' environment,'' department_id,'' department_name,` + permissionResourceCount + ` resource_count FROM rbac_permissions p`
		case "users":
			source = `SELECT u.id,u.name,CASE WHEN u.deleted_at IS NULL THEN '' ELSE '已删除账户，仅可移除已有授权' END description,EXISTS(SELECT 1 FROM rbac_user_roles x WHERE x.role_id=$1 AND x.user_id=u.id) selected,EXISTS(SELECT 1 FROM rbac_department_roles x WHERE x.role_id=$1 AND x.department_id=u.department_id) inherited,COALESCE((SELECT jsonb_agg(t.name) FROM teams t JOIN rbac_department_roles d ON d.department_id=t.id WHERE d.role_id=$1 AND t.id=u.department_id),'[]') source_departments,u.email,'' action,'' scope_type,'' kind,'' application_id,'' application_name,'' environment,COALESCE(u.department_id,'') department_id,COALESCE(t.name,'') department_name,0 resource_count FROM users u LEFT JOIN teams t ON t.id=u.department_id WHERE (u.deleted_at IS NULL OR EXISTS(SELECT 1 FROM rbac_user_roles x WHERE x.role_id=$1 AND x.user_id=u.id)) AND u.source<>'settings_demo'`
		case "departments":
			source = `SELECT t.id,t.name,t.description,EXISTS(SELECT 1 FROM rbac_department_roles x WHERE x.role_id=$1 AND x.department_id=t.id) selected,false inherited,'[]'::jsonb source_departments,'' email,'' action,'' scope_type,'' kind,'' application_id,'' application_name,'' environment,t.id department_id,t.name department_name,0 resource_count FROM teams t`
		default:
			respond(w, nil, store.ErrInvalidReference)
			return
		}
	} else {
		s.resourceTree(w, r)
		return
	}
	q := r.URL.Query()
	selected := q.Get("selected")
	if selected != "" && selected != "all" && selected != "yes" && selected != "no" {
		respond(w, nil, store.ErrInvalidReference)
		return
	}
	args := []any{id, strings.TrimSpace(q.Get("q")), selected, q.Get("action"), q.Get("scope_type"), q.Get("kind"), q.Get("application_id"), q.Get("department_id"), splitIDs(r, "add_ids"), splitIDs(r, "remove_ids")}
	if args[5] == "" {
		args[5] = q.Get("type")
	}
	// Draft selection is evaluated before count and LIMIT; hidden pages are never replaced.
	cte := `WITH raw AS (` + source + `), candidates AS (SELECT *,((selected OR id=ANY($9::text[])) AND NOT id=ANY($10::text[])) draft_selected FROM raw)`
	filter := ` WHERE (aiops_search_match(id,$2) OR aiops_search_match(name,$2) OR aiops_search_match(description,$2) OR aiops_search_match(email,$2)) AND ($3 IN ('','all') OR ($3='yes' AND draft_selected) OR ($3='no' AND NOT draft_selected)) AND ($4='' OR action=$4) AND ($5='' OR scope_type=$5) AND ($6='' OR kind=$6) AND ($7='' OR application_id=$7) AND ($8='' OR department_id=$8)`
	var total int
	if e = s.Store.DB.QueryRow(r.Context(), cte+" SELECT count(*) FROM candidates"+filter, args...).Scan(&total); e != nil {
		respond(w, nil, e)
		return
	}
	last := (total + size - 1) / size
	if last < 1 {
		last = 1
	}
	if page > last {
		page = last
	}
	args = append(args, size, (page-1)*size)
	locked := "false"
	if kind == "users" {
		locked = "EXISTS(SELECT 1 FROM rbac_user_roles x WHERE x.role_id=$1 AND x.user_id=candidates.id AND x.origin='legacy')"
	}
	rows, e := s.Store.DB.Query(r.Context(), cte+` SELECT id,name,description,selected,inherited,source_departments,email,action,scope_type,kind,application_id,application_name,environment,department_id,department_name,resource_count,COALESCE((SELECT is_mock FROM cmdb_resources WHERE id=candidates.id),false),`+locked+` FROM candidates`+filter+` ORDER BY name,id LIMIT $11 OFFSET $12`, args...)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	items := []Candidate{}
	for rows.Next() {
		var v Candidate
		var raw []byte
		if e = rows.Scan(&v.ID, &v.Name, &v.Description, &v.Selected, &v.Inherited, &raw, &v.Email, &v.Action, &v.ScopeType, &v.Kind, &v.ApplicationID, &v.ApplicationName, &v.Environment, &v.DepartmentID, &v.DepartmentName, &v.ResourceCount, &v.IsMock, &v.Locked); e != nil {
			respond(w, nil, e)
			return
		}
		if v.Locked {
			v.LockReason = "此用户的基础角色由成员管理维护，请在成员管理中调整。"
		}
		if e = json.Unmarshal(raw, &v.SourceDepartments); e != nil {
			respond(w, nil, e)
			return
		}
		items = append(items, v)
	}
	respond(w, map[string]any{"items": items, "total": total, "page": page, "page_size": size, "revision": rev}, rows.Err())
}
