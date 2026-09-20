package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type userKey struct{}

func CurrentUser(r *http.Request) core.User {
	v, _ := r.Context().Value(userKey{}).(core.User)
	return v
}

type Server struct {
	Store                   store.Repository
	Runtime                 inference.Runner
	mux                     *http.ServeMux
	SecureCookies           bool
	AllowedOrigin           string
	Integrations            []Integration
	ValidateBindings        func([]core.Binding) error
	EnvironmentCapabilities func(core.Environment) []string
	loginMu                 sync.Mutex
	loginAttempts           map[string][]time.Time
}

func New(s store.Repository, r inference.Runner) *Server {
	x := &Server{Store: s, Runtime: r, mux: http.NewServeMux(), loginAttempts: map[string][]time.Time{}}
	x.routes()
	x.platformRoutes()
	return x
}
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
func Error(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		Error(w, 422, "invalid_body", "Invalid JSON body")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		Error(w, 422, "invalid_body", "Expected one JSON object")
		return false
	}
	return true
}
func fail(w http.ResponseWriter, e error) {
	e = store.ClassifyError(e)
	switch {
	case errors.Is(e, store.ErrForbidden):
		Error(w, 403, "forbidden", "Application permission required")
	case errors.Is(e, store.ErrNotFound):
		Error(w, 404, "not_found", "Not found")
	case errors.Is(e, store.ErrConflict):
		Error(w, 409, "conflict", "State changed, a run is active, or request conflicts; refresh before retrying")
	case errors.Is(e, store.ErrInvalidSelection):
		Error(w, 422, "invalid_selection", "Select known, distinct preview message IDs and a supported merge mode; evidence-only requires referenced evidence")
	case errors.Is(e, store.ErrInvalidReference):
		Error(w, 422, "invalid_reference", "Invalid or duplicate resource")
	default:
		slog.Error("API operation failed", "error", e)
		Error(w, 503, "unavailable", "Service operation failed")
	}
}
func rank(role string) int {
	switch role {
	case "admin":
		return 3
	case "operator":
		return 2
	case "viewer":
		return 1
	}
	return 0
}
func (s *Server) Register(pattern, minRole string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("aiops_session")
		if e != nil {
			Error(w, 401, "unauthorized", "Login required")
			return
		}
		u, e := s.Store.Session(r.Context(), cookie.Value)
		if e != nil {
			Error(w, 401, "unauthorized", "Session expired")
			return
		}
		if rank(u.Role) < rank(minRole) {
			Error(w, 403, "forbidden", "Insufficient permission")
			return
		}
		ctx := store.WithPrincipal(context.WithValue(r.Context(), userKey{}, u), u)
		r = r.WithContext(ctx)
		if e := s.authorizeRoute(r); e != nil {
			AuthorizationError(w, e)
			return
		}
		h(w, r)
	})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		origin := r.Header.Get("Origin")
		if origin != "" {
			u, e := url.Parse(origin)
			expected := s.AllowedOrigin
			if expected == "" {
				scheme := "http"
				if r.TLS != nil {
					scheme = "https"
				}
				expected = scheme + "://" + r.Host
			}
			if e != nil || u.Scheme == "" || origin != expected {
				Error(w, 403, "origin_rejected", "Cross-origin write rejected")
				return
			}
		} else if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			Error(w, 403, "origin_rejected", "Cross-site write rejected")
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}
func (s *Server) modelExists(id string) bool {
	for _, m := range s.Runtime.Models() {
		if m.ID == id {
			return true
		}
	}
	return false
}
func (s *Server) routes() {
	s.Register("GET /api/v1/integrations", "viewer", func(w http.ResponseWriter, r *http.Request) {
		items := s.Integrations
		if items == nil {
			items = []Integration{}
		}
		{
			ids, e := s.VisibleApplicationIDs(r.Context(), CurrentUser(r))
			if e != nil {
				fail(w, e)
				return
			}
			allowed := map[string]bool{}
			for _, id := range ids {
				allowed[id] = true
			}
			filtered := make([]Integration, 0, len(items))
			for _, item := range items {
				item.ApplicationIDs = append([]string{}, item.ApplicationIDs...)
				visible := item.ApplicationIDs[:0]
				for _, id := range item.ApplicationIDs {
					if allowed[id] {
						visible = append(visible, id)
					}
				}
				item.ApplicationIDs = visible
				filtered = append(filtered, item)
			}
			items = filtered
		}
		WriteJSON(w, 200, map[string]any{"items": items, "configuration_mode": "server_managed"})
	})
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if e := s.Store.Health(ctx); e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("POST /api/v1/auth/login", s.login)
	s.Register("GET /api/v1/auth/me", "viewer", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, 200, map[string]any{"user": CurrentUser(r)})
	})
	s.Register("POST /api/v1/auth/logout", "viewer", func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("aiops_session")
		if e := s.Store.Logout(r.Context(), c.Value); e != nil {
			fail(w, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "aiops_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
	})
	s.Register("GET /api/v1/models", "viewer", func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{}
		for _, m := range s.Runtime.Models() {
			items = append(items, map[string]any{"id": m.ID, "name": m.Name, "provider": m.Provider, "available": true, "availability": "configured_unverified", "tools": m.Tools, "streaming": m.Streaming, "context_window": m.ContextWindow, "input_budget": m.InputBudget})
		}
		WriteJSON(w, 200, map[string]any{"items": items})
	})
	s.Register("GET /api/v1/applications", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Applications(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		{
			ids, err := s.VisibleApplicationIDs(r.Context(), CurrentUser(r))
			if err != nil {
				fail(w, err)
				return
			}
			allowed := map[string]bool{}
			for _, id := range ids {
				allowed[id] = true
			}
			filtered := v[:0]
			for _, a := range v {
				if allowed[a.ID] {
					filtered = append(filtered, a)
				}
			}
			v = filtered
		}
		for i := range v {
			s.enrichApplication(&v[i])
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	s.Register("POST /api/v1/applications", "admin", func(w http.ResponseWriter, r *http.Request) {
		var a core.Application
		if !Decode(w, r, &a) {
			return
		}
		if strings.TrimSpace(a.Name) == "" {
			Error(w, 422, "name_required", "Application name required")
			return
		}
		a, e := s.Store.RegisterApplication(r.Context(), a)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 201, a)
	})
	s.Register("GET /api/v1/investigations", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Investigations(r.Context())
		if e != nil {
			fail(w, e)
			return
		}
		{
			ids, err := s.VisibleApplicationIDs(r.Context(), CurrentUser(r))
			if err != nil {
				fail(w, err)
				return
			}
			allowed := map[string]bool{}
			for _, id := range ids {
				allowed[id] = true
			}
			filtered := v[:0]
			for _, a := range v {
				if allowed[a.ApplicationID] {
					filtered = append(filtered, a)
				}
			}
			v = filtered
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	s.Register("POST /api/v1/investigations", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			ApplicationID string `json:"application_id"`
			EnvironmentID string `json:"environment_id"`
			Title         string `json:"title"`
		}
		if !Decode(w, r, &v) {
			return
		}
		if strings.TrimSpace(v.Title) == "" || len(v.Title) > 300 {
			Error(w, 422, "invalid_title", "Title must be 1–300 bytes")
			return
		}
		if e := s.AuthorizeApplication(r.Context(), CurrentUser(r), v.ApplicationID, "operate"); e != nil {
			AuthorizationError(w, e)
			return
		}
		x, e := s.Store.CreateInvestigation(store.WithEnvironment(r.Context(), v.EnvironmentID), v.ApplicationID, v.Title)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 201, x)
	})
	s.Register("GET /api/v1/investigations/{id}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Investigation(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, v)
	})
	s.Register("POST /api/v1/branches/{id}/messages", "operator", s.message)
	s.Register("POST /api/v1/branches/{id}/fork", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Title    string `json:"title"`
			Revision *int64 `json:"revision"`
		}
		if !Decode(w, r, &v) {
			return
		}
		if strings.TrimSpace(v.Title) == "" || len(v.Title) > 300 {
			Error(w, 422, "invalid_title", "Title required")
			return
		}
		b, e := s.Store.Fork(r.Context(), r.PathValue("id"), v.Title, v.Revision)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 201, b)
	})
	s.Register("PATCH /api/v1/branches/{id}", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Title   *string `json:"title"`
			ModelID *string `json:"model_id"`
		}
		if !Decode(w, r, &v) {
			return
		}
		if v.ModelID != nil && !s.modelExists(*v.ModelID) {
			Error(w, 422, "invalid_model", "Model not configured")
			return
		}
		if v.Title != nil && (strings.TrimSpace(*v.Title) == "" || len(*v.Title) > 300) {
			Error(w, 422, "invalid_title", "Title required")
			return
		}
		b, e := s.Store.PatchBranch(r.Context(), r.PathValue("id"), v.Title, v.ModelID)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, b)
	})
	s.Register("POST /api/v1/runs/{id}/cancel", "operator", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Cancel(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"run": v})
	})
	s.Register("POST /api/v1/branches/{id}/merge-preview", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Source string `json:"source_branch_id"`
		}
		if !Decode(w, r, &v) {
			return
		}
		p, e := s.Store.Preview(r.Context(), r.PathValue("id"), v.Source)
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, p)
	})
	s.Register("POST /api/v1/branches/{id}/merge", "operator", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Preview    string   `json:"preview_id"`
			MessageIDs []string `json:"message_ids"`
			Mode       string   `json:"mode"`
		}
		if !Decode(w, r, &v) {
			return
		}
		b, e := s.Store.MergeSelected(r.Context(), r.PathValue("id"), v.Preview, core.MergeOptions{MessageIDs: v.MessageIDs, Mode: v.Mode})
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, b)
	})
	s.Register("GET /api/v1/branches/{id}/revisions", "viewer", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Store.Revisions(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": v})
	})
	s.Register("GET /api/v1/branches/{id}/revisions/{revision}", "viewer", func(w http.ResponseWriter, r *http.Request) {
		rev, e := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if e != nil || rev < 0 {
			Error(w, 422, "invalid_revision", "Revision must be a nonnegative integer")
			return
		}
		v, e := s.Store.Revision(r.Context(), r.PathValue("id"), rev)
		if e != nil {
			fail(w, e)
			return
		}
		// Detail always exposes empty arrays for an initial snapshot; summary
		// responses omit the potentially large messages/evidence fields.
		WriteJSON(w, 200, map[string]any{"branch_id": v.BranchID, "revision": v.Revision, "created_at": v.CreatedAt, "message_count": v.MessageCount, "provenance": v.Provenance, "messages": v.Messages, "evidence": v.Evidence})
	})
	s.Register("GET /api/v1/investigations/{id}/events", "viewer", s.events)
	s.Register("GET /api/v1/investigations/{id}/layout", "viewer", s.getLayout)
	s.Register("PUT /api/v1/investigations/{id}/layout", "viewer", s.putLayout)
	s.Register("GET /api/v1/users", "admin", s.users)
	s.Register("POST /api/v1/users", "admin", s.createUser)
	s.Register("PATCH /api/v1/users/{id}", "admin", s.patchUser)
}
func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Content   string `json:"content"`
		ModelID   string `json:"model_id"`
		RequestID string `json:"client_request_id"`
	}
	if !Decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Content) == "" || len(v.Content) > 65536 || len(v.RequestID) < 8 || len(v.RequestID) > 128 {
		Error(w, 422, "invalid_message", "Content and client_request_id (8–128 bytes) required")
		return
	}
	if !s.modelExists(v.ModelID) {
		Error(w, 422, "invalid_model", "Model not configured")
		return
	}
	run, e := s.Store.Enqueue(r.Context(), r.PathValue("id"), v.Content, v.ModelID, v.RequestID)
	if e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 202, map[string]any{"run": run})
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	exists, e := s.Store.InvestigationExists(r.Context(), id)
	if e != nil {
		fail(w, e)
		return
	}
	if !exists {
		fail(w, store.ErrNotFound)
		return
	}
	after := int64(0)
	for _, x := range []string{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")} {
		if x != "" {
			n, e := strconv.ParseInt(x, 10, 64)
			if e != nil || n < 0 {
				Error(w, 422, "invalid_cursor", "Invalid event cursor")
				return
			}
			if n > after {
				after = n
			}
		}
	}
	f, ok := w.(http.Flusher)
	if !ok {
		Error(w, 503, "stream_unavailable", "Streaming unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f.Flush()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		cookie, _ := r.Cookie("aiops_session")
		if cookie == nil {
			return
		}
		if _, err := s.Store.Session(r.Context(), cookie.Value); err != nil {
			return
		}
		if e := s.authorizeRoute(r); e != nil {
			return
		}
		items, e := s.Store.Events(r.Context(), id, after)
		if e != nil {
			return
		}
		for _, v := range items {
			data, _ := json.Marshal(v)
			if _, e = io.WriteString(w, "id: "+strconv.FormatInt(v.Seq, 10)+"\nevent: update\ndata: "+string(data)+"\n\n"); e != nil {
				return
			}
			after = v.Seq
		}
		f.Flush()
		if len(items) == 250 {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		case <-heartbeat.C:
			cookie, _ := r.Cookie("aiops_session")
			if cookie == nil {
				return
			}
			if _, e = s.Store.Session(r.Context(), cookie.Value); e != nil {
				return
			}
			if _, e = io.WriteString(w, ": heartbeat\n\n"); e != nil {
				return
			}
			f.Flush()
		}
	}
}
