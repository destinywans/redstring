package httpapi

import (
	"encoding/json"

	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"time"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	host := s.loginClientIP(r)
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
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	items, err := repo.Members(r.Context(), r.URL.Query().Get("include_deleted") == "true")
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	var v store.MemberCreate
	if !Decode(w, r, &v) {
		return
	}
	member, err := repo.CreateMember(r.Context(), v)
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 201, map[string]any{"user": member})
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
	repo, ok := s.memberRepository(w)
	if !ok {
		return
	}
	var v store.MemberUpdate
	if !Decode(w, r, &v) {
		return
	}
	member, err := repo.UpdateMember(r.Context(), r.PathValue("id"), v)
	if err != nil {
		memberError(w, err)
		return
	}
	WriteJSON(w, 200, map[string]any{"user": member})
}
