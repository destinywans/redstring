package v3identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func (s *Service) registerBindings(api *httpapi.Server) {
	for _, pattern := range []string{"GET /api/v1/auth/oidc/bindings/{user}", "POST /api/v1/auth/oidc/bindings", "DELETE /api/v1/auth/oidc/bindings/{user}"} {
		api.MarkV3Route(pattern, "members", "manage")
	}
	api.Register("GET /api/v1/auth/oidc/bindings/{user}", "viewer", s.getBinding)
	api.Register("POST /api/v1/auth/oidc/bindings", "viewer", s.bind)
	api.Register("DELETE /api/v1/auth/oidc/bindings/{user}", "viewer", s.unbind)
}

// Identity binding can cross every environment in an instance. A scoped
// members.manage grant is insufficient even if one selected environment is
// currently visible to the actor.
func (s *Service) globalManager(w http.ResponseWriter, r *http.Request) bool {
	var allowed bool
	err := s.store.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
		JOIN rbac_platform_administrators a ON a.user_id=g.user_id
		WHERE g.user_id=$1 AND g.module='members' AND g.action='manage' AND g.scope_kind='global' AND g.scope_id='*')`,
		httpapi.CurrentUser(r).ID).Scan(&allowed)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return false
	}
	if !allowed {
		httpapi.Error(w, 403, "forbidden", "Platform administrator and global member management required")
		return false
	}
	return true
}

func (s *Service) getBinding(w http.ResponseWriter, r *http.Request) {
	if !s.globalManager(w, r) {
		return
	}
	var subject string
	err := s.store.DB.QueryRow(r.Context(), `SELECT subject FROM v3_oidc_identities WHERE issuer_hash=$1 AND user_id=$2`, s.issuerHash, r.PathValue("user")).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "OIDC binding not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	httpapi.WriteJSON(w, 200, map[string]string{"user_id": r.PathValue("user"), "subject": subject})
}

func (s *Service) bind(w http.ResponseWriter, r *http.Request) {
	if !s.globalManager(w, r) {
		return
	}
	var input struct {
		UserID  string `json:"user_id"`
		Subject string `json:"subject"`
	}
	if !httpapi.Decode(w, r, &input) {
		return
	}
	if input.UserID == "" || len(input.UserID) > 200 || input.Subject == "" || len(input.Subject) > 200 || strings.TrimSpace(input.Subject) != input.Subject {
		httpapi.Error(w, 422, "invalid_identity", "Select an existing user and exact external subject")
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if !lockBindingAuthority(ctx, tx, w) {
		return
	}
	if !managerInTx(ctx, tx, w, httpapi.CurrentUser(r).ID) {
		return
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT NOT disabled AND deleted_at IS NULL AND v3_identity_kind='human' AND source<>'settings_demo'
		FROM users WHERE id=$1 FOR UPDATE`, input.UserID).Scan(&eligible); err != nil || !eligible {
		httpapi.Error(w, 422, "invalid_identity", "Existing enabled human user required")
		return
	}
	if !bindingSessionInTx(ctx, tx, w, r) {
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO v3_oidc_identities(issuer_hash,subject,user_id,created_by) VALUES($1,$2,$3,$4)`,
		s.issuerHash, input.Subject, input.UserID, httpapi.CurrentUser(r).ID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			httpapi.Error(w, 409, "identity_conflict", "Subject or user is already bound")
		} else {
			httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		}
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, input.UserID); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if err = auditBinding(ctx, tx, httpapi.CurrentUser(r).ID, input.UserID, "account.oidc_bound", s.issuerHash, input.Subject); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	httpapi.WriteJSON(w, 201, map[string]string{"user_id": input.UserID, "subject": input.Subject})
}

func (s *Service) unbind(w http.ResponseWriter, r *http.Request) {
	if !s.globalManager(w, r) {
		return
	}
	userID := r.PathValue("user")
	ctx := r.Context()
	tx, err := s.store.DB.Begin(ctx)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	defer tx.Rollback(ctx)
	if !lockBindingAuthority(ctx, tx, w) {
		return
	}
	if !managerInTx(ctx, tx, w, httpapi.CurrentUser(r).ID) {
		return
	}
	var subject string
	err = tx.QueryRow(ctx, `SELECT subject FROM v3_oidc_identities WHERE issuer_hash=$1 AND user_id=$2 FOR UPDATE`, s.issuerHash, userID).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "OIDC binding not found")
		return
	}
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&locked); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if !bindingSessionInTx(ctx, tx, w, r) {
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM v3_oidc_identities WHERE issuer_hash=$1 AND user_id=$2`, s.issuerHash, userID); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if err = auditBinding(ctx, tx, httpapi.CurrentUser(r).ID, userID, "account.oidc_unbound", s.issuerHash, subject); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func lockIdentity(ctx context.Context, tx pgx.Tx, w http.ResponseWriter) bool {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(752018996)`); err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return false
	}
	return true
}

// Account and role writers acquire these locks before user rows. Keep that
// order, then serialize identity changes with OIDC callbacks. Holding the
// governance locks makes the final permission check valid through commit.
func lockBindingAuthority(ctx context.Context, tx pgx.Tx, w http.ResponseWriter) bool {
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
			return false
		}
	}
	return lockIdentity(ctx, tx, w)
}

// Recheck after all identity/user lock waits, before any mutation. In a
// self-bind/unbind the next step deliberately deletes this session; its locked
// authorization must therefore be established before that deletion.
func bindingSessionInTx(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, r *http.Request) bool {
	cookie, err := r.Cookie("aiops_session")
	if err == nil {
		err = store.LockActiveSession(ctx, tx, cookie.Value, httpapi.CurrentUser(r).ID)
		if err == nil {
			// A session-row lock can itself wait past expiry. Its predicate may
			// have been evaluated before that wait, so use the current clock now
			// that the session is locked and cannot be revoked concurrently.
			var active bool
			err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM sessions
				WHERE token_hash=$1 AND user_id=$2`, store.Hash(cookie.Value), httpapi.CurrentUser(r).ID).Scan(&active)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !active {
				err = store.ErrSessionRevoked
			}
		}
	} else {
		err = store.ErrSessionRevoked
	}
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "session_revoked", "Session is no longer active")
		return false
	}
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return false
	}
	return true
}

func managerInTx(ctx context.Context, tx pgx.Tx, w http.ResponseWriter, actor string) bool {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled AND deleted_at IS NULL FOR UPDATE`, actor).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpapi.Error(w, 403, "forbidden", "Platform administrator and global member management required")
		} else {
			httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		}
		return false
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM v3_effective_role_grants g
		JOIN rbac_platform_administrators a ON a.user_id=g.user_id
		WHERE g.user_id=$1 AND g.module='members' AND g.action='manage' AND g.scope_kind='global' AND g.scope_id='*')`, actor).Scan(&allowed)
	if err != nil {
		httpapi.Error(w, 503, "identity_unavailable", "Identity management unavailable")
		return false
	}
	if !allowed {
		httpapi.Error(w, 403, "forbidden", "Platform administrator and global member management required")
		return false
	}
	return true
}

func auditBinding(ctx context.Context, tx pgx.Tx, actor, userID, action, issuerHash, subject string) error {
	changes, err := json.Marshal(map[string]string{"issuer_hash": issuerHash, "subject_hash": store.Hash(subject)})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,$2,$3,'success',$4)`, actor, action, userID, changes)
	return err
}
