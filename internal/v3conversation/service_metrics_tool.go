package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/destinywans/redstring/internal/connectors"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3catalog"
	"github.com/destinywans/redstring/internal/v3telemetry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceMetricsToolOptions separates the live identity/catalog checks from the
// conversation's persisted owner and environment. A tool read must resolve the
// current account, service grant and database binding again; it may never use
// the scope from the model's input as authority.
type ServiceMetricsToolOptions struct {
	DB            *pgxpool.Pool
	OwnerID       string
	EnvironmentID string
	CurrentUser   func(context.Context, string) (core.User, error)
	LookupService func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error)
	// Registry resolves the service's one metric_resource target and the
	// catalog's query IDs, Clients builds that Prometheus connector's client
	// (PA-19 P3 stage 2 step 5; the only path since P4).
	Registry *v3telemetry.Resolver
	Clients  *v3telemetry.Clients
	// clientFor replaces Clients.For in tests.
	clientFor func(env string, source v3telemetry.Source) (metricsReader, string, error)
}

// metricsReader is the Prometheus read the tool makes.
type metricsReader interface {
	QueryMetrics(context.Context, string, string, connectors.TimeWindow) (connectors.MetricEvidence, error)
}

var metricServiceKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type metricToolInput struct {
	Service string `json:"service"`
	QueryID string `json:"query_id"`
}

type metricToolSeries struct {
	Index   int                `json:"index"`
	Labels  map[string]string  `json:"labels,omitempty"`
	Samples []metricToolSample `json:"samples"`
}

// metricToolSample carries an RFC 3339 UTC time like the log samples. The
// model misread Unix seconds by minutes (PA-53).
type metricToolSample struct {
	Time  string `json:"time"`
	Value string `json:"value"`
}

// metricSampleTime formats a Prometheus sample time (Unix seconds, possibly
// fractional) as RFC 3339 UTC, with sub-seconds only when present.
func metricSampleTime(timestamp float64) string {
	seconds := math.Floor(timestamp)
	at := time.Unix(int64(seconds), int64(math.Round((timestamp-seconds)*1e9))).UTC()
	if at.Nanosecond() == 0 {
		return at.Format(time.RFC3339)
	}
	return at.Format(time.RFC3339Nano)
}

type metricToolEvidence struct {
	Service       string                `json:"service"`
	EnvironmentID string                `json:"environment_id"`
	QueryID       string                `json:"query_id"`
	Meaning       string                `json:"meaning,omitempty"`
	Window        connectors.TimeWindow `json:"window"`
	CapturedAt    time.Time             `json:"captured_at"`
	Status        string                `json:"status"`
	Incomplete    bool                  `json:"incomplete"`
	Series        []metricToolSeries    `json:"series"`
	Truncated     bool                  `json:"truncated"`
}

// NewServiceMetricsTool offers a short, server-registered Prometheus read.
// It intentionally has no URL, PromQL, time-range or credential parameters.
// The existing V3 conversation runner persists started/terminal tool audit and
// the returned evidence; this constructor only supplies the scoped read.
func NewServiceMetricsTool(opts ServiceMetricsToolOptions) (inference.EvidenceTool, bool) {
	if opts.DB == nil || opts.CurrentUser == nil || opts.LookupService == nil ||
		opts.OwnerID == "" || opts.EnvironmentID == "" || opts.EnvironmentID == "all" {
		return inference.EvidenceTool{}, false
	}
	if opts.Registry == nil || opts.Clients == nil {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name: "read_service_metrics",
		Description: "Read actual Prometheus samples (last 15 minutes) for one registered service in this conversation's environment. " +
			"Set the query string to JSON such as {\"service\":\"exact-service-key\",\"query_id\":\"error_rate\"}. " +
			"Each service has its own operator-reviewed query IDs; list_services shows them. Common IDs: " + knownMetricQueryHelp() + ". " +
			"No PromQL, URL or time range. Errors start with a code: unknown_service (wrong or unreadable key), not_configured " +
			"(no metrics source or that query_id for this service), source_unavailable (Prometheus failed; retry later). Empty results are not health evidence.",
		Read: func(ctx context.Context, input string) (inference.Evidence, error) {
			return readServiceMetrics(ctx, opts, input)
		},
	}, true
}

// metricQuery documents a well-known query ID (PA-51): what it measures and
// which series labels the model may see. The expression itself is only in the
// reviewed source catalog (AIOPS_V3_SOURCE_CATALOG).
type metricQuery struct {
	meaning string
	labels  []string
}

var knownMetricQueries = map[string]metricQuery{
	"request_rate":                {"requests per second", nil},
	"error_rate":                  {"5xx share of requests", nil},
	"http_p95_seconds":            {"p95 request latency in seconds", nil},
	"instances_up":                {"instances reporting up", nil},
	"node_cpu_busy_ratio":         {"CPU busy ratio (0-1) of each node running this service", []string{"node"}},
	"node_memory_available_ratio": {"available memory ratio (0-1) of each node running this service", []string{"node"}},
	"pod_restarts":                {"container restarts in the window per pod", []string{"pod", "container"}},
	"pod_terminated_reason":       {"1 for the last termination reason of each pod container, such as OOMKilled", []string{"pod", "container", "reason"}},
	"consumer_lag":                {"consumer group lag in messages per topic", []string{"consumergroup", "topic"}},
	"messages_consumed_rate":      {"messages consumed per second per topic", []string{"consumergroup", "topic"}},
}

var knownMetricQueryOrder = []string{"request_rate", "error_rate", "http_p95_seconds", "instances_up", "node_cpu_busy_ratio",
	"node_memory_available_ratio", "pod_restarts", "pod_terminated_reason", "consumer_lag", "messages_consumed_rate"}

func knownMetricQueryHelp() string {
	parts := make([]string, 0, len(knownMetricQueryOrder))
	for _, id := range knownMetricQueryOrder {
		help := id + " (" + knownMetricQueries[id].meaning
		if labels := knownMetricQueries[id].labels; len(labels) > 0 {
			help += "; labels " + strings.Join(labels, ", ")
		}
		parts = append(parts, help+")")
	}
	return strings.Join(parts, ", ")
}

// metricQueryID is the shape of any catalog query ID; whether it exists for a
// service is decided by the catalog, not by this list.
var metricQueryID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// metricLabelValue bounds a label value shown to the model.
var metricLabelValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// metricQueryIDs lists the query IDs a service resource has, when the
// telemetry source can tell; ok is false when it cannot.
func metricsInputError() error {
	return inference.NewToolError(inference.ToolInvalidInput,
		`expected JSON {"service":"exact-service-key","query_id":"error_rate"}; list_services shows the keys and query IDs you can read`)
}

func metricsUnavailable(detail string) error {
	return inference.NewToolError(inference.ToolSourceUnavailable, "service metrics are temporarily unavailable ("+detail+"); retry later")
}

func readServiceMetrics(ctx context.Context, opts ServiceMetricsToolOptions, input string) (inference.Evidence, error) {
	if len(input) == 0 || len(input) > 512 {
		return inference.Evidence{}, metricsInputError()
	}
	var request metricToolInput
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		!metricServiceKey.MatchString(request.Service) || !metricQueryID.MatchString(request.QueryID) {
		return inference.Evidence{}, metricsInputError()
	}
	user, err := opts.CurrentUser(ctx, opts.OwnerID)
	if err != nil || user.ID != opts.OwnerID || user.Disabled {
		return inference.Evidence{}, metricsUnavailable("the conversation owner could not be verified")
	}
	profile, err := opts.LookupService(ctx, user, opts.EnvironmentID, request.Service)
	if errors.Is(err, store.ErrNotFound) {
		return inference.Evidence{}, inference.ErrUnknownService
	}
	if err != nil {
		return inference.Evidence{}, metricsUnavailable("service catalog")
	}
	if profile.Key != request.Service || profile.Environment != opts.EnvironmentID || !auditTargetID.MatchString(profile.ID) {
		return inference.Evidence{}, inference.ErrUnknownService
	}
	reader, resource, err := serviceMetricsSource(ctx, opts, request)
	if err != nil {
		return inference.Evidence{}, err
	}
	window := connectors.TimeWindow{Start: time.Now().UTC().Truncate(time.Second).Add(-15 * time.Minute), StepSeconds: 30}
	window.End = window.Start.Add(15 * time.Minute)
	result, err := reader.QueryMetrics(ctx, resource, request.QueryID, window)
	if err == nil && result.Source.Failure != nil {
		err = result.Source.Failure
	}
	if err != nil {
		var telemetryErr *connectors.TelemetryError
		if errors.As(err, &telemetryErr) && (telemetryErr.Kind == "configuration" || telemetryErr.Kind == "scope") {
			return inference.Evidence{}, inference.NewToolError(inference.ToolNotConfigured,
				"query_id "+request.QueryID+" is not configured for service "+request.Service)
		}
		return inference.Evidence{}, metricsUnavailable("Prometheus query failed")
	}
	if (result.Source.ApplicationID != "" && result.Source.ApplicationID != resource) ||
		(result.Source.QueryID != "" && result.Source.QueryID != request.QueryID) {
		return inference.Evidence{}, metricsUnavailable("unexpected source response")
	}
	// The connector may return up to 100 series. Bound model context and avoid
	// exposing configured PromQL, endpoint URLs, credential metadata or labels
	// that could include arbitrary provider content. Only the reviewed labels
	// of a well-known query (such as node) are kept, with bounded values.
	evidence := metricToolEvidence{
		Service: request.Service, EnvironmentID: opts.EnvironmentID,
		QueryID: request.QueryID, Window: window, CapturedAt: result.Source.CapturedAt,
		Status: "no_samples", Series: []metricToolSeries{},
		Truncated:  result.LimitReached || len(result.Series) > 8,
		Incomplete: len(result.Source.Warnings) > 0,
	}
	if known, ok := knownMetricQueries[request.QueryID]; ok {
		evidence.Meaning = known.meaning
	}
	for index, series := range result.Series[:min(len(result.Series), 8)] {
		line := metricToolSeries{Index: index + 1, Samples: []metricToolSample{},
			Labels: reviewedMetricLabels(request.QueryID, series.Labels)}
		for _, sample := range series.Samples[:min(len(series.Samples), 32)] {
			line.Samples = append(line.Samples, metricToolSample{Time: metricSampleTime(sample.Timestamp), Value: sample.Value})
		}
		if len(series.Samples) > 32 {
			evidence.Truncated = true
		}
		if len(line.Samples) > 0 {
			evidence.Status = "observed"
		}
		evidence.Series = append(evidence.Series, line)
	}
	if evidence.Incomplete || evidence.Truncated {
		evidence.Status = "partial"
	}
	content, err := json.Marshal(evidence)
	if err != nil || len(content) > 32<<10 {
		return inference.Evidence{}, metricsUnavailable("result too large")
	}
	return inference.Evidence{
		Kind: "metrics", Title: inference.Localized(ctx, "Prometheus 样本：", "Prometheus samples: ") + request.Service + " / " + request.QueryID,
		Content: string(content), Source: "prometheus", TargetID: profile.ID,
	}, nil
}

// serviceMetricsSource finds the registered Prometheus source of the service
// and checks that it serves the requested query ID. It returns the
// connector's client and the resource key to query.
func serviceMetricsSource(ctx context.Context, opts ServiceMetricsToolOptions, request metricToolInput) (metricsReader, string, error) {
	_, _, source, err := registeredSource(ctx, opts.DB, opts.Registry, request.Service, opts.EnvironmentID, v3telemetry.Metrics)
	switch {
	case errors.Is(err, v3telemetry.ErrNotConfigured):
		return nil, "", inference.NewToolError(inference.ToolNotConfigured, "no metrics source is registered for service "+request.Service)
	case errors.Is(err, v3telemetry.ErrAmbiguous):
		return nil, "", inference.NewToolError(inference.ToolNotConfigured,
			"service "+request.Service+" has more than one registered metrics source; an administrator must keep one")
	case err != nil:
		return nil, "", metricsUnavailable("binding lookup")
	case len(source.NotReady) > 0:
		return nil, "", inference.NewToolError(inference.ToolNotConfigured,
			"the metrics source of service "+request.Service+" is registered but not ready ("+strings.Join(source.NotReady, ", ")+")")
	}
	ids := sortedQueryIDs(source.Queries)
	if !slices.Contains(ids, request.QueryID) {
		configured := "none"
		if len(ids) > 0 {
			configured = strings.Join(ids, ", ")
		}
		return nil, "", inference.NewToolError(inference.ToolNotConfigured,
			"query_id "+request.QueryID+" is not configured for service "+request.Service+"; configured: "+configured)
	}
	var client metricsReader
	var key string
	if opts.clientFor != nil {
		client, key, err = opts.clientFor(opts.EnvironmentID, source)
	} else {
		client, key, err = opts.Clients.For(opts.EnvironmentID, source)
	}
	if err != nil {
		return nil, "", inference.NewToolError(inference.ToolNotConfigured,
			"the metrics source of service "+request.Service+" is unsupported or unconfigured")
	}
	return client, key, nil
}

// pageOnlyQueryIDs are catalog queries other readers own. "dashboard" packs
// every metrics-page panel into one result through panel labels the model
// never sees, so its series cannot be read as evidence.
var pageOnlyQueryIDs = map[string]bool{"dashboard": true}

// sortedQueryIDs lists the catalog query IDs of a source the model may use;
// expressions stay here.
func sortedQueryIDs(queries map[string]string) []string {
	ids := make([]string, 0, len(queries))
	for id := range queries {
		if !pageOnlyQueryIDs[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// reviewedMetricLabels keeps only the documented labels of a well-known query;
// a value outside the bounded identifier shape is shown as "unrecognized".
func reviewedMetricLabels(queryID string, labels map[string]string) map[string]string {
	names := knownMetricQueries[queryID].labels
	if len(names) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, name := range names {
		value, ok := labels[name]
		if !ok {
			continue
		}
		if !metricLabelValue.MatchString(value) {
			value = "unrecognized"
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
