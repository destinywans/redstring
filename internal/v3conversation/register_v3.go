package v3conversation

import "github.com/destinywans/redstring/internal/httpapi"

// RegisterV3 keeps route registration and its V3 permissions together. SSE
// authorizes against the persisted conversation because EventSource cannot
// send the selected-environment header.
func (s *Service) RegisterV3(api *httpapi.Server) {
	s.Register(api.Register)
	// The list contains both chats and canvases, each with its own persisted
	// view grant. A conversation-only route gate would hide canvas-only owners.
	// readOwnedList checks both grants against current roles in one snapshot.
	api.MarkV3ObjectRoute("GET /api/v1/conversations", "conversation", "view")
	api.MarkV3ObjectRoute("GET /api/v1/conversations/{id}", "conversation", "view")
	for _, pattern := range []string{"POST /api/v1/conversations", "POST /api/v1/conversations/{id}/messages"} {
		api.MarkV3Route(pattern, "conversation", "change")
	}
	api.MarkV3Route("GET /api/v1/conversations/{id}/events", "", "")
}
