package v3observe

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3conversation"
)

var metricAskRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type metricAskRequest struct {
	Service   string `json:"service"`
	View      string `json:"view"`
	Range     string `json:"range"`
	Question  string `json:"question"`
	RequestID string `json:"request_id"`
}

type metricAskSnapshot struct {
	ApplicationID string                    `json:"application_id"`
	EnvironmentID string                    `json:"environment_id"`
	View          string                    `json:"view"`
	Source        string                    `json:"source"`
	Window        connectors.TimeWindow     `json:"window"`
	CapturedAt    time.Time                 `json:"captured_at"`
	Status        string                    `json:"status"`
	Samples       []connectors.MetricSample `json:"samples"`
}

func (in metricAskRequest) valid() bool {
	return in.Service != "" && len(in.Service) <= 200 && strings.TrimSpace(in.Service) == in.Service &&
		(in.View == "request_rate" || in.View == "http_p95_seconds") &&
		(in.Range == "15m" || in.Range == "1h") &&
		in.Question != "" && strings.TrimSpace(in.Question) == in.Question && len(in.Question) <= 4000 &&
		metricAskRequestID.MatchString(in.RequestID)
}

func (s *Service) metricsAsk(w http.ResponseWriter, r *http.Request) {
	var in metricAskRequest
	if !httpapi.Decode(w, r, &in) {
		return
	}
	if !in.valid() {
		httpapi.Error(w, 422, "invalid_metric_request", "Choose one service, metric view and time range, then enter a question and request ID")
		return
	}
	environments, ok := s.allowedEnvironments(w, r, "")
	if !ok {
		return
	}
	scope, hasScope := httpapi.V3ScopeFromContext(r.Context())
	if !hasScope || scope.All || len(environments) != 1 {
		httpapi.Error(w, 422, "invalid_environment", "Choose one environment")
		return
	}
	if s.MetricReader == nil || s.MetricsConversation == nil {
		httpapi.Error(w, 503, "not_configured", "Metric questions are not configured")
		return
	}
	cookie, err := r.Cookie("aiops_session")
	if err != nil || cookie.Value == "" {
		httpapi.Error(w, 401, "unauthorized", "Login required")
		return
	}
	user, env := httpapi.CurrentUser(r), environments[0]
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	lookup := v3conversation.MetricsQuestionLookup{
		Owner: user, SessionToken: cookie.Value, EnvironmentID: env,
		ApplicationID: in.Service, View: in.View, Range: in.Range,
		Question: in.Question, RequestID: in.RequestID,
	}
	if existing, found, findErr := s.MetricsConversation.FindMetricsQuestion(ctx, lookup); findErr != nil {
		metricAskConversationError(w, findErr)
		return
	} else if found {
		w.Header().Set("Cache-Control", "no-store")
		httpapi.WriteJSON(w, 200, map[string]string{"conversation_id": existing.ConversationID})
		return
	}
	end := time.Now().UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-15 * time.Minute), End: end, StepSeconds: 30}
	if in.Range == "1h" {
		window.Start = end.Add(-time.Hour)
		window.StepSeconds = 120
	}
	result, readErr := s.MetricReader.QueryService(ctx, scope, user, in.Service, env, in.View, window)
	if readErr != nil {
		metricSamplesReadError(w, readErr)
		return
	}
	// Require the canonical persisted application ID. A display name or key is
	// insufficient for a retry that must recover without querying the provider.
	if result.ApplicationID != in.Service || result.EnvironmentID != env || result.QueryID != in.View {
		httpapi.Error(w, 422, "invalid_metric_request", "Choose the service's stable application ID")
		return
	}
	samples, status, complete := metricResultSamples(result, window)
	if !complete || status != "observed" || ctx.Err() != nil {
		httpapi.Error(w, 503, "metric_result_incomplete", "No complete observed metric snapshot is available")
		return
	}
	snapshot, marshalErr := json.Marshal(metricAskSnapshot{
		ApplicationID: result.ApplicationID, EnvironmentID: env, View: in.View,
		Source: "prometheus", Window: window, CapturedAt: result.CapturedAt,
		Status: status, Samples: samples,
	})
	if marshalErr != nil {
		httpapi.Error(w, 503, "metric_result_incomplete", "Metric snapshot is unavailable")
		return
	}
	// The snapshot is observed data, never instructions. The conversation
	// service places it in private model context and persists its exact source.
	started, startErr := s.MetricsConversation.StartMetricsQuestion(ctx, v3conversation.MetricsQuestionInput{
		Owner: user, SessionToken: cookie.Value, EnvironmentID: env,
		English: strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Accept-Language"))), "en"),
		Source: v3conversation.MetricsSource{
			ApplicationID: result.ApplicationID, BindingID: result.BindingID,
			ConnectorID: result.ConnectorID, ResourceID: result.ResourceID,
			QueryID: result.QueryID, Fingerprint: result.SourceFingerprint,
		},
		View: in.View, Range: in.Range, Question: in.Question,
		RequestID: in.RequestID, EvidenceSnapshot: string(snapshot),
		WindowStart: window.Start, WindowEnd: window.End,
	})
	if startErr != nil {
		metricAskConversationError(w, startErr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	statusCode := 201
	if started.Replayed {
		statusCode = 200
	}
	httpapi.WriteJSON(w, statusCode, map[string]string{"conversation_id": started.ConversationID})
}

func metricAskConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, v3conversation.ErrMetricsRequestConflict):
		httpapi.Error(w, 409, "request_conflict", "Request ID was already used for a different metric question")
	case errors.Is(err, v3conversation.ErrModelInputTooLarge):
		httpapi.Error(w, 422, "metric_context_too_large", "Metric question exceeds the model input budget")
	case errors.Is(err, v3conversation.ErrModel):
		httpapi.Error(w, 503, "model_unavailable", "No configured model is available")
	case errors.Is(err, v3conversation.ErrSessionRevoked):
		httpapi.Error(w, 401, "unauthorized", "Login required")
	case errors.Is(err, v3conversation.ErrForbidden):
		httpapi.Error(w, 403, "forbidden", "Metric conversation is outside your authorized scope")
	default:
		httpapi.Error(w, 503, "conversation_unavailable", "Metric conversation is unavailable")
	}
}
