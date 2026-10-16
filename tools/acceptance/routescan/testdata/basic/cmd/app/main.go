package main

import "example.test/internal/httpapi"

func wire(api *httpapi.Server) {
	api.Register("GET /api/v1/instance-less", "viewer", nil)
	api.MarkV3Route("GET /api/v1/instance-less", "eta", "view")
	api.MarkV3Route("GET /api/v1/never-registered", "eta", "view")
}
