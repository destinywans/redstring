package resources

import (
	"encoding/json"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"strings"
)

// Resource update/delete only modify the catalog. They do not operate clusters.
func (s *Service) registerMutations(api *httpapi.Server) {
	for _, method := range []string{"PATCH", "DELETE"} {
		method := method
		pattern := "PATCH /api/v1/resources/{id}"
		if method == "DELETE" {
			pattern = "DELETE /api/v1/resources/{id}"
		}
		api.Register(pattern, "viewer", func(w http.ResponseWriter, r *http.Request) {
			action := "update"
			if method == "DELETE" {
				action = "delete"
			}
			st := &store.Store{DB: s.DB}
			if e := st.AuthorizeCapability(r.Context(), httpapi.CurrentUser(r), action, "resource", r.PathValue("id")); e != nil {
				httpapi.AuthorizationError(w, e)
				return
			}
			var in struct {
				Name        string   `json:"name"`
				Description string   `json:"description"`
				Tags        []string `json:"tags"`
			}
			if method == "PATCH" {
				if !httpapi.Decode(w, r, &in) {
					return
				}
				if strings.TrimSpace(in.Name) == "" || len(in.Name) > 240 || len(in.Description) > 4000 || len(in.Tags) > 40 {
					httpapi.AuthorizationError(w, store.ErrInvalidReference)
					return
				}
			}
			ctx := r.Context()
			tx, e := s.DB.Begin(ctx)
			if e != nil {
				httpapi.AuthorizationError(w, e)
				return
			}
			defer tx.Rollback(ctx)
			if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(752018994)"); e != nil {
				httpapi.AuthorizationError(w, e)
				return
			}
			var source string
			if e = tx.QueryRow(ctx, "SELECT source_kind FROM cmdb_resources WHERE id=$1 FOR UPDATE", r.PathValue("id")).Scan(&source); e != nil {
				httpapi.AuthorizationError(w, store.ClassifyError(e))
				return
			}
			if method == "DELETE" {
				if source != "manual" {
					httpapi.Error(w, 409, "resource_managed", "Only manually registered resources can be deleted")
					return
				}
				_, e = tx.Exec(ctx, `UPDATE rbac_permissions SET revision=revision+1 WHERE scope_type='resource' AND id IN (SELECT permission_id FROM rbac_permission_resources WHERE resource_id=$1)`, r.PathValue("id"))
				if e == nil {
					_, e = tx.Exec(ctx, `DELETE FROM rbac_permission_resources x USING rbac_permissions p WHERE p.id=x.permission_id AND p.scope_type='resource' AND x.resource_id=$1`, r.PathValue("id"))
				}
				if e == nil {
					_, e = tx.Exec(ctx, "DELETE FROM cmdb_resources WHERE id=$1", r.PathValue("id"))
				}
			} else {
				if in.Tags == nil {
					in.Tags = []string{}
				}
				raw, _ := json.Marshal(in.Tags)
				_, e = tx.Exec(ctx, "UPDATE cmdb_resources SET name=$2,description=$3,tags=$4,updated_at=now() WHERE id=$1", r.PathValue("id"), strings.TrimSpace(in.Name), in.Description, raw)
			}
			if e == nil {
				raw, _ := json.Marshal(in)
				_, e = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, httpapi.CurrentUser(r).ID, "resource."+action, r.PathValue("id"), raw)
			}
			if e == nil {
				e = tx.Commit(ctx)
			}
			if e != nil {
				httpapi.AuthorizationError(w, e)
				return
			}
			httpapi.WriteJSON(w, 200, map[string]any{"id": r.PathValue("id"), "action": action})
		})
	}
}
