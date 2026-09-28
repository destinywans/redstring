// Package v3uploads stores bounded, inert V3 attachment data and validates
// references before a conversation persists them. It does not parse uploaded
// documents, fetch links, or execute their contents.
package v3uploads

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

const MaxUpload = 20 << 20

var ErrInvalidReference = errors.New("invalid attachment reference")

type Upload struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	MIME string `json:"mime"`
	URL  string `json:"url,omitempty"`
}

// ReadAuthorizer rechecks the current V3 conversation:view grant against the
// upload's persisted environment, including on headerless browser downloads.
type ReadAuthorizer func(context.Context, core.User, string) error

type Service struct {
	DB             *pgxpool.Pool
	ReadAuthorizer ReadAuthorizer
	Environments   map[string]bool
}

func New(db *pgxpool.Pool) *Service { return &Service{DB: db} }

// Register marks this route for the V3 environment and role gate, rather than
// silently falling through to the legacy role rank check.
func (s *Service) Register(api *httpapi.Server) {
	const route = "POST /api/v1/uploads"
	api.MarkV3Route(route, "conversation", "change")
	api.Register(route, "viewer", s.upload)
	const contentRoute = "GET /api/v1/uploads/{id}/content"
	// A browser may open this URL without an environment header. Resolve the
	// persisted environment and check its current grant in content instead.
	api.MarkV3ObjectRoute(contentRoute, "conversation", "view")
	api.Register(contentRoute, "viewer", s.content)
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	user := httpapi.CurrentUser(r)
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	// An explicit header is required even on a production instance. The current
	// browser's XHR file path omits it and must fail instead of guessing a scope.
	values := r.Header.Values("X-AIOps-Environment")
	if user.ID == "" || !ok || scope.All || len(scope.EnvironmentIDs) != 1 || len(values) != 1 || values[0] != scope.EnvironmentIDs[0] {
		httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment for the upload")
		return
	}
	if s.DB == nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload+(64<<10))
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		httpapi.Error(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Use multipart or JSON")
		return
	}
	var v Upload
	var content []byte
	switch mediaType {
	case "multipart/form-data":
		v, content, err = parseMultipart(r)
	case "application/json":
		v, content, err = parseJSON(r.Body)
	default:
		httpapi.Error(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Use multipart or JSON")
		return
	}
	if err != nil {
		status, code := http.StatusUnprocessableEntity, "invalid_upload"
		if errors.Is(err, errTooLarge) || isBodyTooLarge(err) {
			status, code = http.StatusRequestEntityTooLarge, "too_large"
		} else if errors.Is(err, errUnsupported) {
			status, code = http.StatusUnsupportedMediaType, "unsupported_media_type"
		}
		httpapi.Error(w, status, code, "Upload is too large or has an unsupported format")
		return
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Could not allocate upload")
		return
	}
	v.ID = hex.EncodeToString(id[:])
	if v.Kind == "file" || v.Kind == "image" {
		v.URL = "/api/v1/uploads/" + v.ID + "/content"
	}
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	storedURL := v.URL
	if v.Kind == "file" || v.Kind == "image" {
		// The database URL column is reserved for external link attachments.
		storedURL = ""
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO v3_uploads(id,owner_id,environment_id,kind,name,size_bytes,mime,url,content) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		v.ID, user.ID, scope.EnvironmentIDs[0], v.Kind, v.Name, v.Size, v.MIME, storedURL, content)
	if err == nil {
		changes, _ := json.Marshal(map[string]any{"kind": v.Kind, "size": v.Size, "mime": v.MIME, "environment_id": scope.EnvironmentIDs[0]})
		_, err = tx.Exec(r.Context(), `INSERT INTO management_audit(actor_id,action,target,outcome,changes) VALUES($1,'v3.upload.created',$2,'success',$3)`, user.ID, v.ID, changes)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, v)
}

// content serves uploaded bytes as a download or validated inline image. The
// middleware checks the session; this handler binds a headerless request to
// the stored scope.
func (s *Service) content(w http.ResponseWriter, r *http.Request) {
	user := httpapi.CurrentUser(r)
	if user.ID == "" || user.Disabled {
		httpapi.Error(w, http.StatusUnauthorized, "unauthorized", "Login required")
		return
	}
	scope, scoped := httpapi.V3ScopeFromContext(r.Context())
	s.serveContent(w, r, user, scope, scoped)
}

func (s *Service) serveContent(w http.ResponseWriter, r *http.Request, user core.User, scope httpapi.V3EnvironmentScope, scoped bool) {
	if s.DB == nil || s.ReadAuthorizer == nil || len(s.Environments) == 0 {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload retrieval is not configured")
		return
	}
	if !scoped && len(r.Header.Values("X-AIOps-Environment")) != 0 {
		httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment")
		return
	}
	if scoped && (scope.All || len(scope.EnvironmentIDs) != 1) {
		httpapi.Error(w, http.StatusUnprocessableEntity, "environment_required", "Select one environment")
		return
	}
	var environment, kind, name, savedMIME string
	err := s.DB.QueryRow(r.Context(), `SELECT environment_id,kind,name,mime FROM v3_uploads WHERE id=$1 AND owner_id=$2 AND kind IN ('file','image')`, r.PathValue("id"), user.ID).
		Scan(&environment, &kind, &name, &savedMIME)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	if !s.Environments[environment] || (scoped && scope.EnvironmentIDs[0] != environment) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err := s.ReadAuthorizer(r.Context(), user, environment); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			httpapi.Error(w, http.StatusForbidden, "forbidden", "Upload access denied")
		} else {
			httpapi.Error(w, http.StatusServiceUnavailable, "authorization_unavailable", "Authorization unavailable")
		}
		return
	}
	cleaned, err := cleanName(name)
	if err != nil || cleaned != name {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Stored upload metadata is invalid")
		return
	}
	var data []byte
	// Rebind the byte read to every authorization-relevant metadata field. A
	// changed owner or environment between the two queries cannot expose data.
	err = s.DB.QueryRow(r.Context(), `SELECT content FROM v3_uploads WHERE id=$1 AND owner_id=$2 AND environment_id=$3 AND kind=$4 AND name=$5 AND mime=$6`,
		r.PathValue("id"), user.ID, environment, kind, name, savedMIME).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.Error(w, http.StatusNotFound, "not_found", "Upload not found")
		return
	}
	if err != nil {
		httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Upload storage unavailable")
		return
	}
	if kind == "image" {
		if !safeInlineImage(name, savedMIME, data) {
			httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Stored image is invalid")
			return
		}
		w.Header().Set("Content-Type", savedMIME)
		w.Header().Set("Content-Disposition", "inline")
	} else {
		disposition := safeDownloadDisposition(cleaned)
		if disposition == "" {
			httpapi.Error(w, http.StatusServiceUnavailable, "uploads_unavailable", "Stored upload metadata is invalid")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", disposition)
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func safeDownloadDisposition(name string) string {
	cleaned, err := cleanName(name)
	if err != nil || cleaned != name {
		return ""
	}
	return mime.FormatMediaType("attachment", map[string]string{"filename": cleaned})
}

func safeInlineImage(name, savedMIME string, data []byte) bool {
	switch savedMIME {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return false
	}
	detected, err := safeFileType(name, data)
	return err == nil && detected == savedMIME
}

// ValidateAttachments is directly assignable to v3conversation.Options.
// Compare the complete client-visible metadata as well as the ID; a forged
// object must not enter a durable message under another name or URL.
func (s *Service) ValidateAttachments(ctx context.Context, user core.User, environment string, raw []json.RawMessage) error {
	if s.DB == nil || user.ID == "" || environment == "" || environment == "all" || len(raw) > 20 {
		return ErrInvalidReference
	}
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		var submitted Upload
		if err := decodeStrict(bytes.NewReader(item), &submitted); err != nil || submitted.ID == "" {
			return ErrInvalidReference
		}
		if _, exists := seen[submitted.ID]; exists {
			return ErrInvalidReference
		}
		seen[submitted.ID] = struct{}{}
		var saved Upload
		err := s.DB.QueryRow(ctx, `SELECT id,kind,name,size_bytes,mime,url FROM v3_uploads WHERE id=$1 AND owner_id=$2 AND environment_id=$3`, submitted.ID, user.ID, environment).
			Scan(&saved.ID, &saved.Kind, &saved.Name, &saved.Size, &saved.MIME, &saved.URL)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidReference
			}
			return err
		}
		if saved.Kind == "file" || saved.Kind == "image" {
			saved.URL = "/api/v1/uploads/" + saved.ID + "/content"
		}
		if submitted != saved {
			return ErrInvalidReference
		}
	}
	return nil
}

var errTooLarge = errors.New("upload too large")
var errUnsupported = errors.New("unsupported upload type")

func isBodyTooLarge(err error) bool {
	var max *http.MaxBytesError
	return errors.As(err, &max)
}

func decodeStrict(r io.Reader, dst any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return ErrInvalidReference
	}
	return nil
}

func cleanName(value string) (string, error) {
	value = path.Base(strings.ReplaceAll(value, "\\", "/"))
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || utf8.RuneCountInString(value) > 200 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", ErrInvalidReference
	}
	return value, nil
}

func validLink(value string) bool {
	if len(value) > 4096 || strings.TrimSpace(value) != value {
		return false
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
