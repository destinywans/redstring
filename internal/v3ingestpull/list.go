package v3ingestpull

import (
	"errors"
	"net/http"
	"slices"
	"sort"

	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

// pullListItem names a configured Pull source without its private expression,
// connector endpoint or credential reference.
type pullListItem struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	ApplicationID string  `json:"application_id"`
	EnvironmentID string  `json:"environment_id"`
	ConnectorID   string  `json:"connector_id"`
	ConnectorName *string `json:"connector_name"`
	QueryID       string  `json:"query_id"`
	// State is ready when the connector is enabled and the metric target is
	// bound, source_unavailable when they are disabled or unbound. The list
	// never reads the upstream, so ready says the source is configured, not
	// that the upstream answers (PA-248); POST /ingest/pull/{id}/test does.
	State string `json:"state"`
	Href  string `json:"href"`
}

// list returns the server-configured Pull sources the caller may open with
// GET /ingest/pull/{id}: same environment, services.view on the application
// or owner team and environment connectors.view, checked in one transaction.
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.DB == nil {
		writeError(w, failure(503, "storage_unavailable"))
		return
	}
	scope, ok := httpapi.V3ScopeFromContext(r.Context())
	if !ok || scope.Tier != s.Tier || len(scope.EnvironmentIDs) == 0 {
		writeError(w, failure(503, "environment_scope_unavailable"))
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if len(s.sources) == 0 {
		// No private Pull configuration: the list is unknown, not empty.
		httpapi.WriteJSON(w, 200, map[string]any{"items": nil, "empty_reason": nil,
			"unavailable": map[string]string{"items": "not_configured"}})
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	}
	ids := make([]string, 0, len(s.sources))
	for id, source := range s.sources {
		if slices.Contains(scope.EnvironmentIDs, source.EnvironmentID) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	tx, err := s.DB.Begin(r.Context())
	if err != nil {
		writeError(w, failure(503, "storage_unavailable"))
		return
	}
	defer rollback(tx)
	user := httpapi.CurrentUser(r)
	items := make([]pullListItem, 0, len(ids))
	for _, id := range ids {
		source := s.sources[id]
		item := pullListItem{ID: source.ID, Name: source.Name, ApplicationID: source.ApplicationID,
			EnvironmentID: source.EnvironmentID, ConnectorID: source.ConnectorID, QueryID: source.QueryID,
			State: "ready", Href: "/ingest/pull/" + source.ID}
		snapshot, sourceErr := s.sourceTx(r.Context(), tx, user, source, false)
		var p *pullError
		switch {
		case sourceErr == nil:
			name := snapshot.Name
			item.ConnectorName = &name
		case errors.As(sourceErr, &p) && (p.status == 404 || p.status == 403):
			// Hidden application, missing grant or removed application:
			// the source is not listed, as the detail route would not open it.
			continue
		case errors.As(sourceErr, &p) && p.code == "source_unavailable":
			item.State = "source_unavailable"
		default:
			writeError(w, sourceErr)
			return
		}
		items = append(items, item)
	}
	if err = store.LockActiveSession(r.Context(), tx, cookie.Value, user.ID); errors.Is(err, store.ErrSessionRevoked) {
		httpapi.Error(w, 401, "unauthorized", "Session expired")
		return
	} else if err != nil {
		writeError(w, failure(503, "storage_unavailable"))
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, failure(503, "storage_unavailable"))
		return
	}
	emptyReason := any(nil)
	if len(items) == 0 {
		emptyReason = "no_authorized_target"
	}
	httpapi.WriteJSON(w, 200, map[string]any{"items": items, "empty_reason": emptyReason, "unavailable": map[string]string{}})
}
