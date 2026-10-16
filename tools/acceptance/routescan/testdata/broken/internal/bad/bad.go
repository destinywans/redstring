package bad

import "example.test/internal/httpapi"

func pattern() string { return "GET /api/v1/computed" }

func Register(api *httpapi.Server, module string) {
	api.Register("GET /api/v1/computed", "viewer", nil)
	api.MarkV3Route(pattern(), "alpha", "view")
	api.Register("GET /api/v1/param-module", "viewer", nil)
	api.MarkV3Route("GET /api/v1/param-module", module, "view")
	api.Register("GET /api/v1/twice", "viewer", nil)
	api.MarkV3Route("GET /api/v1/twice", "alpha", "view")
	api.MarkV3ObjectRoute("GET /api/v1/twice", "alpha", "view")
}
