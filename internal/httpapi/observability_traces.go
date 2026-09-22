package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
)

type sharedTraceReader interface {
	QueryTracesBatch(context.Context, []string, connectors.TimeWindow, int) (map[string]connectors.TraceEvidence, error)
}

// All targets have passed the normal batch authorization and environment checks.
// Revalidate before dispatch, and independently before returning each application.
func (s *Server) writeSharedTraceBatch(w http.ResponseWriter, r *http.Request, ctx context.Context, in ObservationBatchRequest, envs []core.Environment, reader sharedTraceReader) {
	items := make([]ObservationBatchItem, len(in.Targets))
	resources := make([]string, len(in.Targets))
	selected := []string{}
	for i, target := range in.Targets {
		items[i] = ObservationBatchItem{ObservationTarget: target, BindingRevision: envs[i].BindingRevision}
		if err := s.AuthorizeApplication(ctx, CurrentUser(r), target.ApplicationID, "view"); err != nil {
			items[i].Error = &ObservationError{Code: "forbidden", Message: "Application access changed"}
			continue
		}
		for _, binding := range envs[i].Bindings {
			if binding.Kind == "tempo" {
				resources[i] = binding.Resource
				break
			}
		}
		if resources[i] == "" {
			items[i].Error = &ObservationError{Code: "not_configured", Message: "No Tempo connector is registered for this target"}
			continue
		}
		selected = append(selected, resources[i])
	}
	results := map[string]connectors.TraceEvidence{}
	var queryErr error
	if len(selected) > 0 {
		results, queryErr = reader.QueryTracesBatch(ctx, selected, connectors.TimeWindow{Start: in.Start, End: in.End}, in.Limit)
	}
	partial := false
	for i, target := range in.Targets {
		item := &items[i]
		if item.Error != nil {
			partial = true
			continue
		}
		value, ok := results[resources[i]]
		switch {
		case ctx.Err() != nil:
			item.Error = &ObservationError{Code: "query_cancelled", Message: "Batch deadline reached or request cancelled"}
		case queryErr != nil || !ok || value.Source.Failure != nil:
			item.Error = &ObservationError{Code: "telemetry_unavailable", Message: "Registered trace provider unavailable; narrow the window or retry"}
		default:
			raw, err := json.Marshal(value)
			if err != nil || len(raw) > 256*1024 {
				item.Error = &ObservationError{Code: "result_too_large", Message: "Trace sample exceeds the response budget; narrow the query"}
			} else {
				item.Result = raw
			}
		}
		if err := s.AuthorizeApplication(ctx, CurrentUser(r), target.ApplicationID, "view"); err != nil {
			item.Result = nil
			item.Error = &ObservationError{Code: "forbidden", Message: "Application access changed"}
		}
		partial = partial || item.Error != nil
	}
	WriteJSON(w, 200, map[string]any{"items": items, "partial": partial})
}
