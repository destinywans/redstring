package v3uploads

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// RegisterRevocation installs the explicit owner-only file revocation action.
// The V3 middleware checks the current session and conversation:change grant.
func (s *Service) RegisterRevocation(api *httpapi.Server) {
	const route = "POST /api/v1/uploads/{id}/revoke"
	api.MarkV3Route(route, "conversation", "change")
	api.Register(route, "viewer", s.revoke)
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	user := httpapi.CurrentUser(r)
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	values := r.Header.Values("X-AIOps-Environment")
	if user.ID == "" || user.Disabled || !ok || scope.All || len(scope.EnvironmentIDs) != 1 ||
		len(values) != 1 || values[0] != scope.EnvironmentIDs[0] {
		httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment")
		return
	}
	if s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	id := r.PathValue("id")
	if !sourceUploadID.MatchString(id) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	env := scope.EnvironmentIDs[0]
	if len(s.Environments) != 0 && !s.Environments[env] {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	cookie, cookieErr := r.Cookie("aiops_session")
	if cookieErr != nil {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		return
	}
	if err := store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		if errors.Is(err, store.ErrSessionRevoked) {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization unavailable")
		}
		return
	}
	if _, err := tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization unavailable")
		return
	}
	policy := &rbac.V3Policy{Store: &store.Store{DB: s.DB}}
	if err := policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{
		Module: "conversation", Action: "change", EnvironmentID: env,
	}); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Upload access denied")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization unavailable")
		}
		return
	}
	var hash string
	err = tx.QueryRow(r.Context(), `DELETE FROM v3_uploads
WHERE id=$1 AND owner_id=$2 AND environment_id=$3 AND kind='file'
RETURNING encode(sha256(content),'hex')`, id, user.ID, env).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO v3_upload_revocations(id,owner_id,environment_id,content_hash)
VALUES($1,$2,$3,$4)`, id, user.ID, env, hash)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes)
VALUES($1,'v3.upload.revoked',$2,'success',jsonb_build_object('environment_id',$3::text,'kind','file','content_hash',$4::text))`,
			user.ID, id, env, hash)
	}
	if err == nil {
		var active bool
		err = tx.QueryRow(r.Context(), `SELECT expires_at>clock_timestamp() FROM sessions WHERE token_hash=$1 AND user_id=$2`,
			store.Hash(cookie.Value), user.ID).Scan(&active)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
			httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
			return
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
}
