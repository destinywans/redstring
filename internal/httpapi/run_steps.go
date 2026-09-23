package httpapi

import (
	"net/http"
	"strings"

	"github.com/wans/aiops-canvas/internal/store"
)

// ConfigureRunSteps exposes the visible agent steps of a run. traceURL is an
// optional external trace UI template (Phoenix, Langfuse…) containing
// {trace_id}; the product links out instead of re-rendering full traces.
func (s *Server) ConfigureRunSteps(db *store.Store, traceURL string) {
	if !strings.Contains(traceURL, "{trace_id}") {
		traceURL = ""
	}
	s.Register("GET /api/v1/runs/{id}/steps", "viewer", func(w http.ResponseWriter, r *http.Request) {
		// Register's resource guard applies the run's investigation application ACL.
		items, e := db.RunSteps(r.Context(), r.PathValue("id"))
		if e != nil {
			fail(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"items": items, "trace_url_template": traceURL})
	})
}
