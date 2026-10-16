package forms

import (
	"net/http"

	"example.test/internal/httpapi"
)

type Service struct{}

const packageRoute = "GET /api/v1/const"

// route is a named table row type; fields are positional in the table below.
type route struct {
	action  string
	pattern string
	module  string
}

var packageTable = []route{
	{"view", "GET /api/v1/named", "alpha"},
	{action: "manage", module: "beta", pattern: "POST /api/v1/named"},
}

func (s *Service) Literal(api *httpapi.Server) {
	api.Register("GET /api/v1/literal", "viewer", nil)
	api.MarkV3Route("GET /api/v1/literal", "alpha", "view")
	api.Register("POST /api/v1/literal/{id}", "viewer", nil)
	api.MarkV3ObjectRoute("POST /api/v1/literal/{id}", "alpha", "change")
	api.Register("GET /api/v1/me/self", "viewer", nil)
	api.MarkV3Route("GET /api/v1/me/self", "", "")
}

func (s *Service) Variables(api *httpapi.Server) {
	const local = "PUT /api/v1/local"
	api.Register(local, "viewer", nil)
	api.MarkV3Route(local, "alpha", "manage")
	api.Register(packageRoute, "viewer", nil)
	api.MarkV3Route(packageRoute, "alpha", "view")
	prefix := "/api/v1/joined"
	joined := "DELETE " + prefix + "/{id}"
	api.Register(joined, "viewer", nil)
	api.MarkV3ObjectRoute(joined, "alpha", "manage")
}

// Table: handler sits between pattern and action; module is a call argument.
func (s *Service) Table(api *httpapi.Server) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
		action  string
	}{
		{"GET /api/v1/table", s.handler, "view"},
		{"POST /api/v1/table", s.handler, "change"},
	} {
		handler := route.handler
		api.Register(route.pattern, "viewer", handler)
		api.MarkV3ObjectRoute(route.pattern, "gamma", route.action)
	}
}

// Routes held in a variable, module from the row, branch on a row field.
func (s *Service) VariableTable(api *httpapi.Server) {
	routes := []struct{ pattern, module, action string }{
		{"GET /api/v1/rows", "delta", "view"},
		{"POST /api/v1/rows", "epsilon", "change"},
		{"GET /api/v1/rows/{id}", "delta", "view"},
	}
	for _, r := range routes {
		api.Register(r.pattern, "viewer", nil)
		if r.module == "delta" || r.pattern == "never" {
			api.MarkV3Route(r.pattern, r.module, r.action)
		} else {
			api.MarkV3ObjectRoute(r.pattern, r.module, r.action)
		}
	}
	for _, r := range packageTable {
		api.Register(r.pattern, "viewer", nil)
		api.MarkV3Route(r.pattern, r.module, r.action)
	}
}

// Plain string slices and an unknown condition that does not touch marks.
func (s *Service) Strings(api *httpapi.Server, flag bool) {
	for _, pattern := range []string{"GET /api/v1/strings", "GET /api/v1/strings/{id}"} {
		if flag {
			api.Register(pattern, "viewer", nil)
		}
		api.MarkV3Route(pattern, "zeta", "view")
	}
}

// Legacy-only shapes: a map of slices indexed in a loop, a reassigned
// variable, and a function-typed parameter.
func (s *Service) Legacy(api *httpapi.Server, register func(string, string, http.HandlerFunc)) {
	routes := map[string][]string{
		"one": {"GET /api/v1/old/one", "POST /api/v1/old/one"},
		"two": {"GET /api/v1/old/two", "POST /api/v1/old/two"},
	}
	for _, kind := range []string{"one", "two"} {
		kind := kind
		api.Register(routes[kind][0], "viewer", nil)
		api.Register(routes[kind][1], "viewer", nil)
	}
	for _, method := range []string{"PATCH", "DELETE"} {
		pattern := "PATCH /api/v1/old/resource"
		if method == "DELETE" {
			pattern = "DELETE /api/v1/old/resource"
		}
		api.Register(pattern, "viewer", nil)
	}
	register("GET /api/v1/old/param", "viewer", nil)
	api.RegisterPublic("POST /api/v1/events/alert", nil)
}

// An outer variable assigned under an unknown condition becomes unknown.
func (s *Service) Uncertain(api *httpapi.Server, flag bool) {
	pattern := "GET /api/v1/uncertain"
	if flag {
		pattern = "GET /api/v1/uncertain/other"
	}
	api.Register(pattern, "viewer", nil)
}

func (s *Service) handler(w http.ResponseWriter, r *http.Request) {}
