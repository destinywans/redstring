package rbac

import (
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s *Service) departments(w http.ResponseWriter, r *http.Request) {
	page, size, e := paging(r)
	if e != nil {
		respond(w, nil, e)
		return
	}
	q := r.URL.Query().Get("q")
	var total int
	e = s.Store.DB.QueryRow(r.Context(), "SELECT count(*) FROM teams WHERE aiops_search_match(name,$1)", q).Scan(&total)
	if e != nil {
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
	rows, e := s.Store.DB.Query(r.Context(), `SELECT t.id,t.name,t.revision,(SELECT count(*) FROM team_members m WHERE m.team_id=t.id) FROM teams t WHERE aiops_search_match(t.name,$1) ORDER BY t.name,t.id LIMIT $2 OFFSET $3`, q, size, (page-1)*size)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name string
		var revision int64
		var count int
		if e = rows.Scan(&id, &name, &revision, &count); e != nil {
			respond(w, nil, e)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "revision": revision, "member_count": count})
	}
	respond(w, map[string]any{"items": items, "total": total, "page": page, "page_size": size}, rows.Err())
}
func (s *Service) saveDepartment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Revision int64  `json:"revision"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !validName(in.Name, "") {
		respond(w, nil, store.ErrInvalidReference)
		return
	}
	id := r.PathValue("id")
	tx, e := s.Store.DB.Begin(r.Context())
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer tx.Rollback(r.Context())
	if id == "" {
		id = store.ID()
		e = tx.QueryRow(r.Context(), "INSERT INTO teams(id,name) VALUES($1,$2) RETURNING revision", id, in.Name).Scan(&in.Revision)
	} else {
		e = tx.QueryRow(r.Context(), "UPDATE teams SET name=$2,revision=revision+1 WHERE id=$1 AND revision=$3 RETURNING revision", id, in.Name, in.Revision).Scan(&in.Revision)
		if e == pgx.ErrNoRows {
			e = store.ErrConflict
		}
	}
	if e == nil {
		e = audit(r.Context(), tx, "rbac.department.saved", id, in)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	respond(w, map[string]any{"id": id, "name": in.Name, "revision": in.Revision}, store.ClassifyError(e))
}
func (s *Service) userDepartment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DepartmentID *string `json:"department_id"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	tx, e := s.Store.DB.Begin(ctx)
	if e != nil {
		respond(w, nil, e)
		return
	}
	defer tx.Rollback(ctx)
	if e = lockAuthority(ctx, tx); e != nil {
		respond(w, nil, e)
		return
	}
	var found string
	if e = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL AND source<>'settings_demo' FOR UPDATE", r.PathValue("id")).Scan(&found); e != nil {
		respond(w, nil, store.ClassifyError(e))
		return
	}
	if in.DepartmentID != nil && *in.DepartmentID != "" {
		var team string
		if e = tx.QueryRow(ctx, "SELECT id FROM teams WHERE id=$1", *in.DepartmentID).Scan(&team); e != nil {
			respond(w, nil, store.ErrInvalidReference)
			return
		}
	}
	if _, e = tx.Exec(ctx, "DELETE FROM team_members WHERE user_id=$1", found); e == nil && in.DepartmentID != nil && *in.DepartmentID != "" {
		_, e = tx.Exec(ctx, "INSERT INTO team_members(user_id,team_id) VALUES($1,$2)", found, *in.DepartmentID)
	}
	if e == nil {
		e = store.ProtectLastRBACManager(ctx, tx)
	}
	if e == nil {
		e = audit(ctx, tx, "rbac.user.department", found, in)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	respond(w, map[string]any{"department_id": in.DepartmentID}, store.ClassifyError(e))
}
