package httpapi

import (
	"context"
	"github.com/destinywans/redstring/internal/store"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) auditEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{Query: q.Get("q"), ActorID: q.Get("actor_id"), ApplicationID: q.Get("application_id"), Action: q.Get("action"), Outcome: q.Get("outcome"), Cursor: q.Get("cursor")}
	if raw, ok := q["limit"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 250 {
			Error(w, 422, "invalid_filter", "Limit must be between 1 and 250")
			return
		}
		f.Limit = n
	}
	for key, destination := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(key); raw != "" {
			v, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				Error(w, 422, "invalid_filter", "Use RFC3339 timestamps")
				return
			}
			*destination = &v
		}
	}
	if repository, ok := s.Store.(interface {
		SearchAudit(context.Context, store.AuditFilter) (store.AuditPage, error)
	}); ok {
		result, err := repository.SearchAudit(r.Context(), f)
		if err != nil {
			fail(w, err)
			return
		}
		WriteJSON(w, 200, result)
		return
	}
	// Legacy repository adapters remain usable for the original unfiltered call.
	if len(q) > 0 {
		Error(w, 503, "audit_search_unavailable", "Audit search is unavailable for this repository")
		return
	}
	repository := s.Store.(store.PlatformRepository)
	items, err := repository.AuditEntries(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	WriteJSON(w, 200, map[string]any{"items": items})
}
