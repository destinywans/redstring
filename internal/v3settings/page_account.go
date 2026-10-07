package v3settings

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/mail"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3interactionaudit"
)

func (s *Service) registerPageAccount(api *httpapi.Server) {
	for _, route := range []struct {
		pattern, action string
		handler         http.HandlerFunc
	}{
		{"PUT /api/v1/me/avatar", "profile.avatar.put", s.putPageAvatar},
		{"DELETE /api/v1/me/avatar", "profile.avatar.delete", s.deletePageAvatar},
		{"POST /api/v1/me/contacts/{kind}/verification", "profile.contact.verification", s.verifyPageContact},
		{"DELETE /api/v1/me/contacts/feishu", "profile.contact.feishu.unlink", s.unlinkPageFeishu},
	} {
		v3interactionaudit.Mark(api, s.Store.DB, route.pattern, route.action)
		api.Register(route.pattern, "viewer", route.handler)
		api.MarkV3Route(route.pattern, "", "")
	}
	api.Register("GET /api/v1/me/avatar/content/{revision}", "viewer", s.getPageAvatar)
	api.MarkV3Route("GET /api/v1/me/avatar/content/{revision}", "", "")
}

func (s *Service) pageAccountTx(r *http.Request, write bool) (pgx.Tx, error) {
	if s.Store == nil || s.Store.DB == nil {
		return nil, errIdentityUnavailable
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if write && (!ok || scope.All || len(scope.EnvironmentIDs) != 1) || ok && len(scope.EnvironmentIDs) == 0 {
		return nil, store.ErrForbidden
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil {
		return nil, store.ErrSessionRevoked
	}
	tx, err := s.Store.DB.Begin(r.Context())
	if err != nil {
		return nil, err
	}
	fail := func(err error) (pgx.Tx, error) { _ = tx.Rollback(r.Context()); return nil, err }
	for _, key := range []int64{752018992, 752018995, 752018998} {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock_shared($1)`, key); err != nil {
			return fail(err)
		}
	}
	user := httpapi.CurrentUser(r)
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		return fail(err)
	}
	var human bool
	if err = tx.QueryRow(r.Context(), `SELECT v3_identity_kind='human' FROM users WHERE id=$1`, user.ID).Scan(&human); err != nil {
		return fail(err)
	}
	if !human {
		return fail(store.ErrForbidden)
	}
	if _, err = tx.Exec(r.Context(), `LOCK TABLE rbac_user_roles,rbac_department_roles,v3_role_grants IN SHARE MODE`); err != nil {
		return fail(err)
	}
	policy := &rbac.V3Policy{Store: s.Store}
	for _, env := range scope.EnvironmentIDs {
		if err = policy.AuthorizeWithTx(r.Context(), tx, user, rbac.V3Decision{Module: "environment", Action: "view", EnvironmentID: env}); err != nil {
			return fail(err)
		}
	}
	return tx, nil
}

func pageAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	if errors.Is(err, store.ErrForbidden) {
		httpapi.Error(w, 403, "forbidden", "Account access denied")
		return
	}
	httpapi.Error(w, 503, "profile_unavailable", "Account storage is unavailable")
}

func (s *Service) finishPageAccount(r *http.Request, tx pgx.Tx, action string) error {
	user := httpapi.CurrentUser(r)
	scope, _ := httpapi.V3ScopeFromContext(r.Context())
	if _, err := tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,actor_kind,action,target,outcome,changes) VALUES($1,'user',$2,$1,'succeeded',jsonb_build_object('environment_id',$3::text))`, user.ID, action, scope.EnvironmentIDs[0]); err != nil {
		return err
	}
	cookie, _ := r.Cookie("aiops_session")
	if err := store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}

func validatePageAvatar(data []byte) (string, error) {
	if len(data) == 0 || len(data) > 2<<20 {
		return "", errInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" && format != "jpeg" || cfg.Width < 256 || cfg.Height < 256 || cfg.Width > 4096 || cfg.Height > 4096 {
		return "", errInvalid
	}
	if _, decodedFormat, err := image.Decode(bytes.NewReader(data)); err != nil || decodedFormat != format {
		return "", errInvalid
	}
	if format == "jpeg" {
		return "image/jpeg", nil
	}
	return "image/png", nil
}

func (s *Service) putPageAvatar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, (2<<20)+(64<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		httpapi.Error(w, 422, "invalid_avatar", "Use multipart file PNG or JPEG")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		httpapi.Error(w, 422, "invalid_avatar", "Exactly one file is required")
		return
	}
	data, err := io.ReadAll(io.LimitReader(part, (2<<20)+1))
	_ = part.Close()
	if err != nil {
		httpapi.Error(w, 422, "invalid_avatar", "Avatar exceeds the upload limit")
		return
	}
	if extra, err := reader.NextPart(); err != io.EOF {
		if extra != nil {
			_ = extra.Close()
		}
		httpapi.Error(w, 422, "invalid_avatar", "Exactly one file is required")
		return
	}
	contentType, err := validatePageAvatar(data)
	if err != nil {
		httpapi.Error(w, 422, "invalid_avatar", "PNG/JPEG must be at most 2 MB and 256–4096 pixels per side")
		return
	}
	tx, err := s.pageAccountTx(r, true)
	if err != nil {
		pageAccountError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	user := httpapi.CurrentUser(r)
	revision := store.ID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO v3_user_avatars(user_id,revision,content_type,content) VALUES($1,$2,$3,$4) ON CONFLICT(user_id) DO UPDATE SET revision=EXCLUDED.revision,content_type=EXCLUDED.content_type,content=EXCLUDED.content,updated_at=now()`, user.ID, revision, contentType, data); err != nil {
		pageAccountError(w, err)
		return
	}
	if err = s.finishPageAccount(r, tx, "profile.avatar.put"); err != nil {
		pageAccountError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 200, map[string]string{"url": "/api/v1/me/avatar/content/" + revision})
}

func (s *Service) deletePageAvatar(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pageAccountTx(r, true)
	if err != nil {
		pageAccountError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `DELETE FROM v3_user_avatars WHERE user_id=$1`, httpapi.CurrentUser(r).ID); err != nil {
		pageAccountError(w, err)
		return
	}
	if err = s.finishPageAccount(r, tx, "profile.avatar.delete"); err != nil {
		pageAccountError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Service) getPageAvatar(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pageAccountTx(r, false)
	if err != nil {
		pageAccountError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var contentType string
	var data []byte
	err = tx.QueryRow(r.Context(), `SELECT content_type,content FROM v3_user_avatars WHERE user_id=$1 AND revision=$2`, httpapi.CurrentUser(r).ID, r.PathValue("revision")).Scan(&contentType, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, 404, "not_found", "Avatar not found")
		return
	}
	if err != nil {
		pageAccountError(w, err)
		return
	}
	cookie, _ := r.Cookie("aiops_session")
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, httpapi.CurrentUser(r).ID); err != nil {
		pageAccountError(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		pageAccountError(w, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}

var pagePhone = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func (s *Service) verifyPageContact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Value string `json:"value"`
	}
	if !httpapi.Decode(w, r, &in) {
		return
	}
	valid := false
	switch r.PathValue("kind") {
	case "email":
		addr, err := mail.ParseAddress(in.Value)
		valid = err == nil && addr.Address == in.Value && len(in.Value) <= 254
	case "phone":
		valid = pagePhone.MatchString(in.Value)
	}
	if !valid {
		httpapi.Error(w, 422, "invalid_contact", "Supply a plain email address or international phone number")
		return
	}
	tx, err := s.pageAccountTx(r, true)
	if err != nil {
		pageAccountError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	httpapi.Error(w, 503, "contact_verification_unavailable", "A verified send-and-confirm authority is not configured")
}

func (s *Service) unlinkPageFeishu(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pageAccountTx(r, true)
	if err != nil {
		pageAccountError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	httpapi.Error(w, 503, "contact_unlink_unavailable", "Verified identity unlink and notification fallback are not configured")
}
