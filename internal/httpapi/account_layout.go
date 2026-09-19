package httpapi

import (
	"encoding/json"
	"errors"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"net"
	"net/http"
	"strings"
	"time"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	now := time.Now()
	s.loginMu.Lock()
	attempts := s.loginAttempts[host]
	recent := attempts[:0]
	for _, t := range attempts {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 10 {
		s.loginMu.Unlock()
		Error(w, 429, "rate_limited", "Retry login in one minute")
		return
	}
	if len(s.loginAttempts) > 10000 {
		for k, v := range s.loginAttempts {
			if len(v) == 0 || now.Sub(v[len(v)-1]) > time.Minute {
				delete(s.loginAttempts, k)
			}
		}
	}
	s.loginAttempts[host] = append(recent, now)
	s.loginMu.Unlock()
	var v struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !Decode(w, r, &v) {
		return
	}
	if len(v.Password) > 1024 || len(v.Email) > 320 {
		Error(w, 422, "invalid_credentials", "Invalid credentials length")
		return
	}
	u, token, e := s.Store.Login(r.Context(), v.Email, v.Password)
	if e != nil {
		Error(w, 401, "invalid_credentials", "Invalid email or password")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "aiops_session", Value: token, Path: "/", HttpOnly: true, Secure: s.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	WriteJSON(w, 200, map[string]any{"user": u})
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	out, e := s.Store.Users(r.Context())
	if e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 200, map[string]any{"items": out})
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
		Role     string `json:"role"`
	}
	if !Decode(w, r, &v) {
		return
	}
	if !strings.Contains(v.Email, "@") || len(v.Email) > 320 || len(v.Password) < 12 || len(v.Password) > 1024 || rank(v.Role) == 0 || strings.TrimSpace(v.Name) == "" {
		Error(w, 422, "invalid_user", "Email, name, role and password of 12–1024 bytes required")
		return
	}
	u, e := s.Store.CreateUser(r.Context(), core.User{Email: v.Email, Name: v.Name, Role: v.Role}, v.Password)
	if e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 201, map[string]any{"user": u})
}
func (s *Server) getLayout(w http.ResponseWriter, r *http.Request) {
	b, e := s.Store.Layout(r.Context(), CurrentUser(r).ID, r.PathValue("id"))
	if e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 200, map[string]any{"layout": b})
}
func (s *Server) putLayout(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Layout map[string]any `json:"layout"`
	}
	if !Decode(w, r, &v) {
		return
	}
	if v.Layout == nil {
		Error(w, 422, "invalid_layout", "Layout must be an object")
		return
	}
	b, _ := json.Marshal(v.Layout)
	if e := s.Store.SaveLayout(r.Context(), CurrentUser(r).ID, r.PathValue("id"), b); e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 200, v)
}
func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	var v store.AccountUpdate
	if !Decode(w, r, &v) {
		return
	}
	if (v.Name != nil && strings.TrimSpace(*v.Name) == "") || (v.Role != nil && rank(*v.Role) == 0) || (v.Password != nil && (len(*v.Password) < 12 || len(*v.Password) > 1024)) {
		Error(w, 422, "invalid_user", "Invalid account update")
		return
	}
	u, e := s.Store.UpdateAccount(r.Context(), r.PathValue("id"), v)
	if errors.Is(e, store.ErrLastAdmin) {
		Error(w, 409, "last_admin", "At least one enabled administrator is required")
		return
	}
	if e != nil {
		fail(w, e)
		return
	}
	WriteJSON(w, 200, map[string]any{"user": u})
}
