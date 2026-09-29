package v3conversation

import "github.com/wans/aiops-canvas/internal/httpapi"

// RegisterV3 keeps route registration and its V3 permissions together. SSE
// authorizes against the persisted conversation because EventSource cannot
// send the selected-environment header.
func (s *Service) RegisterV3(api *httpapi.Server) {
	s.Register(api.Register)
	for _, pattern := range []string{"GET /api/v1/conversations", "GET /api/v1/conversations/{id}"} {
		api.MarkV3Route(pattern, "conversation", "view")
	}
	for _, pattern := range []string{"POST /api/v1/conversations", "POST /api/v1/conversations/{id}/messages"} {
		api.MarkV3Route(pattern, "conversation", "change")
	}
	api.MarkV3Route("GET /api/v1/conversations/{id}/events", "", "")
}
