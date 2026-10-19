// Package v3interactionaudit records rejected page writes without retaining
// request bodies, contact details or credentials. Successful domain writes
// keep their audit in the same transaction as the mutation.
package v3interactionaudit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Mark covers authenticated failures from all gates as well as the handler.
// The stable route pattern is the target when a gate has not resolved an ID.
func Mark(api *httpapi.Server, db *pgxpool.Pool, pattern, action string) {
	api.MarkV3FailureAudit(pattern, func(handler http.HandlerFunc) http.HandlerFunc {
		return withFailureAudit(db, action, pattern, handler)
	})
}

func withFailureAudit(db *pgxpool.Pool, action, pattern string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result := httptest.NewRecorder()
		handler(result, r)
		if result.Code >= 400 && db != nil {
			cookie, err := r.Cookie("aiops_session")
			user := httpapi.CurrentUser(r)
			if err == nil && cookie.Value != "" && user.ID != "" {
				tx, err := db.Begin(r.Context())
				if err == nil {
					defer tx.Rollback(r.Context())
					err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID)
					if err == nil {
						var human bool
						err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human)
						if err == nil && human {
							var body struct {
								Error struct {
									Code string `json:"code"`
								} `json:"error"`
							}
							_ = json.Unmarshal(result.Body.Bytes(), &body)
							code := body.Error.Code
							if len(code) > 100 {
								code = "request_failed"
							}
							scope, _ := httpapi.V3VerifiedAuditScope(r)
							env := ""
							if len(scope.EnvironmentIDs) == 1 {
								env = scope.EnvironmentIDs[0]
							}
							target := r.PathValue("id")
							if target == "" {
								target = pattern
							}
							_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes) VALUES($1,'user',$2,$3,'failed',jsonb_build_object('environment_id',$4::text,'http_status',$5::int,'code',$6::text))`, user.ID, action, target, env, result.Code, code)
							if err == nil {
								err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID)
							}
							if err == nil {
								err = tx.Commit(r.Context())
							}
						}
					}
				}
				if err != nil && err != store.ErrSessionRevoked {
					httpapi.Error(w, 503, "audit_unavailable", "Rejected write could not be audited")
					return
				}
			}
		}
		for key, values := range result.Header() {
			w.Header()[key] = append([]string(nil), values...)
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(result.Body.Bytes())
	}
}
