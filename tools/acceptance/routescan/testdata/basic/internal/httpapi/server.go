package httpapi

import "net/http"

type Server struct{ mux *http.ServeMux }

func (s *Server) Register(pattern, minRole string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h) // pattern is a parameter: not a route
}

func (s *Server) RegisterPublic(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

func (s *Server) MarkV3Route(pattern, module, action string)       {}
func (s *Server) MarkV3ObjectRoute(pattern, module, action string) {}

func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/v1/auth/login", nil)
	s.Register("GET /api/v1/auth/me", "viewer", nil)
	s.Register("GET /api/v1/legacy/thing", "viewer", nil)
	s.mux.HandleFunc("GET /healthz", nil) // outside /api/v1: ignored
}

func v3AuthOrInstance(pattern string) bool {
	switch pattern {
	case "POST /api/v1/auth/login", "GET /api/v1/auth/me":
		return true
	}
	return false
}

func v3SignedPushRoute(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/api/v1/events/alert":
		return true
	}
	return false
}
