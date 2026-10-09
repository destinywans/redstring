package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
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
	Telemetry     interface {
		QueryMetrics(context.Context, string, string, connectors.TimeWindow) (connectors.MetricEvidence, error)
	}
	// SourceCompare, when set, is told which file-configured binding was
	// read (PA-19 compare mode). It never changes the result.
	SourceCompare v3telemetry.Observer
}

var metricServiceKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type metricToolInput struct {
	Service string `json:"service"`
	QueryID string `json:"query_id"`
}

type metricToolSeries struct {
	Index   int                       `json:"index"`
	Samples []connectors.MetricSample `json:"samples"`
}

type metricToolEvidence struct {
	Service       string                `json:"service"`
	EnvironmentID string                `json:"environment_id"`
	QueryID       string                `json:"query_id"`
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
	if opts.DB == nil || opts.Telemetry == nil || opts.CurrentUser == nil || opts.LookupService == nil ||
		opts.OwnerID == "" || opts.EnvironmentID == "" || opts.EnvironmentID == "all" {
		return inference.EvidenceTool{}, false
	}
	if telemetry, ok := opts.Telemetry.(*connectors.Telemetry); ok && telemetry == nil {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name:        "read_service_metrics",
		Description: "Read actual Prometheus samples for one registered service in this conversation's environment. Set the query string to JSON such as {\"service\":\"exact-service-key\",\"query_id\":\"error_rate\"}. Allowed query IDs: request_rate, error_rate (5xx share of requests), http_p95_seconds, instances_up. Each is an operator-reviewed expression; a missing one is reported unavailable. No PromQL, URL or time range. Empty results are not health evidence.",
		Read: func(ctx context.Context, input string) (inference.Evidence, error) {
			return readServiceMetrics(ctx, opts, input)
		},
	}, true
}

// serviceMetricQueries are the standard RED/availability query IDs an operator may define
// per service in the telemetry configuration; the expressions themselves stay server-side.
var serviceMetricQueries = map[string]bool{"request_rate": true, "error_rate": true, "http_p95_seconds": true, "instances_up": true}

func readServiceMetrics(ctx context.Context, opts ServiceMetricsToolOptions, input string) (inference.Evidence, error) {
	const unavailable = "service metrics unavailable"
	if len(input) == 0 || len(input) > 512 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	var request metricToolInput
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		!metricServiceKey.MatchString(request.Service) ||
		!serviceMetricQueries[request.QueryID] {
		return inference.Evidence{}, errors.New(unavailable)
	}
	user, err := opts.CurrentUser(ctx, opts.OwnerID)
	if err != nil || user.ID != opts.OwnerID || user.Disabled {
		return inference.Evidence{}, errors.New(unavailable)
	}
	profile, err := opts.LookupService(ctx, user, opts.EnvironmentID, request.Service)
	if err != nil || profile.Key != request.Service || profile.Environment != opts.EnvironmentID || !auditTargetID.MatchString(profile.ID) {
		return inference.Evidence{}, errors.New(unavailable)
	}
	// The authorized service key must have exactly one persisted Prometheus
	// binding in this environment. Ambiguity is an error, never a broader query.
	rows, err := opts.DB.Query(ctx, `SELECT a.id,b.resource
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind='prometheus'
 WHERE a.key=$1 AND NOT a.archived AND b.resource<>'' LIMIT 2`, request.Service, opts.EnvironmentID)
	if err != nil {
		return inference.Evidence{}, errors.New(unavailable)
	}
	var bindings []string
	var appID string
	for rows.Next() {
		var resource string
		if err = rows.Scan(&appID, &resource); err != nil {
			break
		}
		bindings = append(bindings, resource)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(bindings) != 1 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	if opts.SourceCompare != nil {
		opts.SourceCompare.ObserveLegacy(v3telemetry.Use{ApplicationID: appID, EnvironmentID: opts.EnvironmentID, Provider: "prometheus", Resource: bindings[0]})
	}
	window := connectors.TimeWindow{Start: time.Now().UTC().Truncate(time.Second).Add(-15 * time.Minute), StepSeconds: 30}
	window.End = window.Start.Add(15 * time.Minute)
	result, err := opts.Telemetry.QueryMetrics(ctx, bindings[0], request.QueryID, window)
	if err != nil || result.Source.Failure != nil {
		return inference.Evidence{}, errors.New(unavailable)
	}
	if (result.Source.ApplicationID != "" && result.Source.ApplicationID != bindings[0]) ||
		(result.Source.QueryID != "" && result.Source.QueryID != request.QueryID) {
		return inference.Evidence{}, errors.New(unavailable)
	}
	// The connector may return up to 100 series. Bound model context and avoid
	// exposing configured PromQL, endpoint URLs, credential metadata or labels
	// that could include arbitrary provider content.
	evidence := metricToolEvidence{
		Service: request.Service, EnvironmentID: opts.EnvironmentID,
		QueryID: request.QueryID, Window: window, CapturedAt: result.Source.CapturedAt,
		Status: "no_samples", Series: []metricToolSeries{},
		Truncated:  result.LimitReached || len(result.Series) > 8,
		Incomplete: len(result.Source.Warnings) > 0,
	}
	for index, series := range result.Series[:min(len(result.Series), 8)] {
		line := metricToolSeries{Index: index + 1, Samples: series.Samples[:min(len(series.Samples), 32)]}
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
		return inference.Evidence{}, errors.New(unavailable)
	}
	return inference.Evidence{
		Kind: "metrics", Title: "Prometheus samples: " + request.Service + " / " + request.QueryID,
		Content: string(content), Source: "prometheus", TargetID: profile.ID,
	}, nil
}
