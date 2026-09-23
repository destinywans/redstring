package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
	"net/http"
	"sync"
	"time"
)

type TelemetryReader interface {
	QueryMetrics(context.Context, string, string, connectors.TimeWindow) (connectors.MetricEvidence, error)
	QueryLogs(context.Context, string, connectors.TimeWindow, int) (connectors.LogEvidence, error)
}
type ObservationTarget struct {
	ApplicationID string `json:"application_id"`
	EnvironmentID string `json:"environment_id"`
	QueryID       string `json:"query_id,omitempty"`
}
type ObservationBatchRequest struct {
	Level       string              `json:"level,omitempty"`
	Summary     bool                `json:"summary,omitempty"`
	TraceID     string              `json:"trace_id,omitempty"`
	Targets     []ObservationTarget `json:"targets"`
	Start       time.Time           `json:"start,omitempty"`
	End         time.Time           `json:"end,omitempty"`
	StepSeconds int                 `json:"step_seconds,omitempty"`
	Limit       int                 `json:"limit,omitempty"`
	Provider    string              `json:"provider,omitempty"`
	Search      string              `json:"search,omitempty"`
}
type ObservationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type ObservationBatchItem struct {
	ObservationTarget
	BindingRevision int64             `json:"binding_revision"`
	Result          json.RawMessage   `json:"result,omitempty"`
	Error           *ObservationError `json:"error,omitempty"`
}

// RegisterTelemetryBatch retains the same registered query boundary as the
// single-application API. Authorization completes before any provider is called.
func (s *Server) RegisterTelemetryBatch(reader TelemetryReader) {
	handler := func(kind string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in ObservationBatchRequest
			if !Decode(w, r, &in) {
				return
			}
			if !connectors.ValidLogLevel(in.Level) || (kind != "logs" && (in.Level != "" || in.Summary)) || (in.TraceID != "" && (kind != "traces" || !connectors.ValidTraceID(in.TraceID))) || len(in.Targets) < 1 || len(in.Targets) > 20 || in.Limit < 0 || in.Limit > 1000 || in.StepSeconds < 0 || len(in.Search) > 256 || (in.Provider != "" && in.Provider != "loki" && in.Provider != "elasticsearch") || (kind != "logs" && (in.Provider != "" || in.Search != "")) || (kind == "traces" && (len(in.Targets) > 6 || in.Limit > 20)) {
				Error(w, 422, "invalid_batch", "Select 1 to 20 distinct targets (at most 6 for traces); trace limit is 20 and log search is at most 256 bytes")
				return
			}
			repository, ok := s.Store.(store.PlatformRepository)
			if !ok {
				fail(w, store.ErrForbidden)
				return
			}
			envs := make([]core.Environment, len(in.Targets))
			seen := map[ObservationTarget]bool{}
			for i, t := range in.Targets {
				if t.ApplicationID == "" || t.EnvironmentID == "" || (kind == "metrics" && t.QueryID == "") || (kind != "metrics" && t.QueryID != "") || seen[t] {
					Error(w, 422, "invalid_batch", "Targets must be distinct and include an application, environment and registered metric query where applicable")
					return
				}
				seen[t] = true
				if err := s.AuthorizeApplication(r.Context(), CurrentUser(r), t.ApplicationID, "view"); err != nil {
					fail(w, store.ErrForbidden)
					return
				}
				all, err := repository.Environments(r.Context(), t.ApplicationID)
				if err != nil {
					fail(w, err)
					return
				}
				found := false
				for _, env := range all {
					if env.ID == t.EnvironmentID && !env.Archived {
						envs[i] = env
						found = true
						break
					}
				}
				if !found {
					Error(w, 422, "invalid_scope", "An environment does not belong to the selected application or is archived")
					return
				}
			}
			// Resolve one common window so provider defaults cannot skew comparisons.
			if in.End.IsZero() {
				in.End = time.Now().UTC()
			}
			if in.Start.IsZero() {
				in.Start = in.End.Add(-15 * time.Minute)
			}
			if !in.Start.Before(in.End) {
				Error(w, 422, "invalid_window", "Start must precede end")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
			defer cancel()
			if kind == "traces" {
				if shared, ok := reader.(sharedTraceReader); ok {
					s.writeSharedTraceBatch(w, r, ctx, in, envs, shared)
					return
				}
			}
			if kind == "traces" && in.TraceID != "" {
				Error(w, 422, "trace_lookup_unavailable", "Exact trace lookup is unavailable")
				return
			}
			items := make([]ObservationBatchItem, len(in.Targets))
			jobs := make(chan int)
			var wg sync.WaitGroup
			for n := 0; n < min(4, len(items)); n++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range jobs {
						t := in.Targets[i]
						item := ObservationBatchItem{ObservationTarget: t, BindingRevision: envs[i].BindingRevision}
						item.Error = &ObservationError{Code: "telemetry_unavailable", Message: "Registered provider unavailable; narrow the window or retry"}
						bindingKind := "prometheus"
						if kind == "logs" {
							bindingKind = in.Provider
							if bindingKind == "" {
								bindingKind = "loki"
								for _, b := range envs[i].Bindings {
									if b.Kind == "elasticsearch" {
										bindingKind = "elasticsearch"
										break
									}
								}
							}
						}
						if kind == "traces" {
							bindingKind = "tempo"
						}
						resource := ""
						for _, b := range envs[i].Bindings {
							if b.Kind == bindingKind {
								resource = b.Resource
								break
							}
						}
						if resource == "" {
							item.Error = &ObservationError{Code: "not_configured", Message: "No connector is registered for this target"}
							items[i] = item
							continue
						}
						if reader == nil {
							items[i] = item
							continue
						}
						if ctx.Err() != nil {
							item.Error = &ObservationError{Code: "query_cancelled", Message: "Batch deadline reached or request cancelled"}
							items[i] = item
							continue
						}
						// Recheck before dispatch; a revoked grant must not start a queued query.
						if err := s.AuthorizeApplication(ctx, CurrentUser(r), t.ApplicationID, "view"); err != nil {
							item.Error = &ObservationError{Code: "forbidden", Message: "Application access changed"}
							items[i] = item
							continue
						}
						var value any
						var err error
						window := connectors.TimeWindow{Start: in.Start, End: in.End, StepSeconds: in.StepSeconds}
						if kind == "metrics" {
							value, err = reader.QueryMetrics(ctx, resource, t.QueryID, window)
						} else if kind == "traces" {
							if extended, ok := reader.(interface {
								QueryTraces(context.Context, string, connectors.TimeWindow, int) (connectors.TraceEvidence, error)
							}); ok {
								value, err = extended.QueryTraces(ctx, resource, window, in.Limit)
							} else {
								err = errors.New("trace reader unavailable")
							}
						} else if extended, ok := reader.(interface {
							QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error)
						}); ok {
							value, err = extended.QueryLogsOptions(ctx, resource, bindingKind, in.Search, in.Level, in.Summary, window, in.Limit)
						} else if in.Level != "" || in.Summary {
							err = errors.New("extended log queries unavailable")
						} else if extended, ok := reader.(interface {
							QueryLogsSearch(context.Context, string, string, string, connectors.TimeWindow, int) (connectors.LogEvidence, error)
						}); ok {
							value, err = extended.QueryLogsSearch(ctx, resource, bindingKind, in.Search, window, in.Limit)
						} else if bindingKind == "loki" && in.Search == "" {
							value, err = reader.QueryLogs(ctx, resource, window, in.Limit)
						} else {
							err = errors.New("log provider unavailable")
						}
						if err == nil {
							raw, marshalErr := json.Marshal(value)
							if marshalErr == nil && len(raw) <= 256*1024 {
								item.Result = raw
								item.Error = nil
							} else {
								item.Error = &ObservationError{Code: "result_too_large", Message: "Result exceeds the batch item budget; narrow the query or use the single-target view"}
							}
						} else {
							var qe *connectors.TelemetryError
							if errors.As(err, &qe) && qe.Kind == "scope" && qe.HTTPStatus == 0 {
								item.Error = &ObservationError{Code: "invalid_telemetry_query", Message: qe.Message}
							}
						}
						if ctx.Err() != nil {
							item.Result = nil
							item.Error = &ObservationError{Code: "query_cancelled", Message: "Batch deadline reached or request cancelled"}
							items[i] = item
							continue
						}
						// Do not return data if application access was revoked while the provider ran.
						if err := s.AuthorizeApplication(ctx, CurrentUser(r), t.ApplicationID, "view"); err != nil {
							item.Result = nil
							item.Error = &ObservationError{Code: "forbidden", Message: "Application access changed"}
						}
						items[i] = item
					}
				}()
			}
			for i := range items {
				jobs <- i
			}
			close(jobs)
			wg.Wait()
			partial := false
			for _, item := range items {
				partial = partial || item.Error != nil
			}
			WriteJSON(w, 200, map[string]any{"items": items, "partial": partial})
		}
	}
	s.Register("POST /api/v1/observability/metrics/batch", "viewer", handler("metrics"))
	s.Register("POST /api/v1/observability/logs/batch", "viewer", handler("logs"))
	s.Register("POST /api/v1/observability/traces/batch", "viewer", handler("traces"))
}
