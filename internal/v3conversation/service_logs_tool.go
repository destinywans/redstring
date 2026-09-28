package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
)

// ServiceLogsToolOptions must be assembled only for an operator-enabled tool
// model. The conversation runner binds OwnerID and EnvironmentID to the saved
// turn; neither value may come from the model's input.
type ServiceLogsToolOptions struct {
	Enabled       bool
	DB            *pgxpool.Pool
	OwnerID       string
	EnvironmentID string
	CurrentUser   func(context.Context, string) (core.User, error)
	LookupService func(context.Context, core.User, string, string) (v3catalog.ServiceEvidence, error)
	Access        v3catalog.Access
	Telemetry     interface {
		QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error)
	}
}

type logsToolInput struct {
	Service string `json:"service"`
	QueryID string `json:"query_id"`
}

type logsToolSample struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
}

type logsToolEvidence struct {
	Service       string                `json:"service"`
	EnvironmentID string                `json:"environment_id"`
	QueryID       string                `json:"query_id"`
	Window        connectors.TimeWindow `json:"window"`
	CapturedAt    time.Time             `json:"captured_at"`
	Status        string                `json:"status"`
	SampledCount  int                   `json:"sampled_count"`
	Samples       []logsToolSample      `json:"samples"`
	Truncated     bool                  `json:"truncated"`
	Incomplete    bool                  `json:"incomplete"`
	Partial       bool                  `json:"partial"`
	Boundary      string                `json:"boundary"`
}

// NewServiceLogsTool returns only a bounded metadata sample. Log lines,
// labels, trace IDs, configured LogQL, source URL and credentials are omitted:
// arbitrary log text cannot safely be passed to a model as unsanitized evidence.
func NewServiceLogsTool(opts ServiceLogsToolOptions) (inference.EvidenceTool, bool) {
	if !opts.Enabled || opts.DB == nil || opts.Telemetry == nil || opts.CurrentUser == nil || opts.LookupService == nil || opts.Access == nil ||
		opts.OwnerID == "" || opts.EnvironmentID == "" || opts.EnvironmentID == "all" {
		return inference.EvidenceTool{}, false
	}
	if telemetry, ok := opts.Telemetry.(*connectors.Telemetry); ok && telemetry == nil {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name:        "read_service_logs",
		Description: "Read a limited Loki log metadata sample for one registered service in this conversation's environment. Pass JSON with exact service key and query_id=recent or recent_errors. The recent_errors query only matches supported JSON level fields; an empty result does not mean no errors. No arbitrary LogQL, labels, URL, search text or time range. This sample excludes raw log lines and does not establish total volume or service health.",
		Read: func(ctx context.Context, input string) (inference.Evidence, error) {
			return readServiceLogs(ctx, opts, input)
		},
	}, true
}

func readServiceLogs(ctx context.Context, opts ServiceLogsToolOptions, input string) (inference.Evidence, error) {
	const unavailable = "service logs unavailable"
	if len(input) == 0 || len(input) > 512 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	var request logsToolInput
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF ||
		!metricServiceKey.MatchString(request.Service) ||
		(request.QueryID != "recent" && request.QueryID != "recent_errors") {
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
	// Require one persisted Loki association for the authorized service in this
	// exact environment. Duplicate or missing bindings fail closed.
	rows, err := opts.DB.Query(ctx, `SELECT a.id,COALESCE(a.owner_team_id,''),b.resource
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind='loki'
 WHERE a.key=$1 AND NOT a.archived AND b.resource<>'' LIMIT 2`, request.Service, opts.EnvironmentID)
	if err != nil {
		return inference.Evidence{}, errors.New(unavailable)
	}
	type logBinding struct{ appID, teamID, resource string }
	var bindings []logBinding
	for rows.Next() {
		var binding logBinding
		if err = rows.Scan(&binding.appID, &binding.teamID, &binding.resource); err != nil {
			break
		}
		bindings = append(bindings, binding)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(bindings) != 1 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	binding := bindings[0]
	if binding.appID == "" || !auditTargetID.MatchString(binding.appID) {
		return inference.Evidence{}, errors.New(unavailable)
	}
	// Service visibility only authorizes locating the target. A separate logs.view
	// grant on the persisted application (or its persisted owner team) is needed
	// immediately before any provider call. Other policy failures cannot fall back.
	decision := rbac.V3Decision{Module: "logs", Action: "view", EnvironmentID: opts.EnvironmentID, ApplicationID: binding.appID}
	if authErr := opts.Access.Authorize(ctx, user, decision); authErr != nil {
		if !errors.Is(authErr, store.ErrForbidden) || binding.teamID == "" || !auditTargetID.MatchString(binding.teamID) {
			return inference.Evidence{}, errors.New(unavailable)
		}
		decision.ApplicationID = ""
		decision.TeamID = binding.teamID
		if opts.Access.Authorize(ctx, user, decision) != nil {
			return inference.Evidence{}, errors.New(unavailable)
		}
	}
	end := time.Now().UTC().Truncate(time.Second)
	window := connectors.TimeWindow{Start: end.Add(-15 * time.Minute), End: end}
	level := ""
	if request.QueryID == "recent_errors" {
		level = "error"
	}
	result, err := opts.Telemetry.QueryLogsOptions(ctx, binding.resource, "loki", "", level, false, window, 10)
	if err != nil {
		var telemetryErr *connectors.TelemetryError
		if errors.As(err, &telemetryErr) && (telemetryErr.Kind == "configuration" || telemetryErr.Kind == "scope") {
			return inference.Evidence{}, errors.New("service log source unsupported or unconfigured")
		}
		return inference.Evidence{}, errors.New(unavailable)
	}
	if result.Source.Failure != nil || result.Source.Provider != "loki" ||
		result.Source.ApplicationID != binding.resource || len(result.Entries) > 10 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	evidence := logsToolEvidence{
		Service: request.Service, EnvironmentID: opts.EnvironmentID, QueryID: request.QueryID,
		Window: window, CapturedAt: result.Source.CapturedAt,
		Status: "metadata_sample", Samples: []logsToolSample{},
		SampledCount: len(result.Entries), Truncated: result.LimitReached,
		Incomplete: len(result.Source.Warnings) > 0, Partial: true,
		Boundary: "仅返回最近日志的时间和级别；不包含正文、标签或可验证的总条数，不能据此判断服务健康。",
	}
	if result.Empty && len(result.Entries) != 0 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	if len(result.Entries) == 0 {
		evidence.Status = "no_entries_in_sample_window"
	}
	if request.QueryID == "recent_errors" {
		evidence.Boundary += " 错误筛选仅匹配当前查询支持的 JSON level 字段；空结果不表示没有错误。"
	}
	for _, entry := range result.Entries {
		if entry.Timestamp.Before(window.Start) || !entry.Timestamp.Before(window.End) {
			return inference.Evidence{}, errors.New(unavailable)
		}
		evidence.Samples = append(evidence.Samples, logsToolSample{Timestamp: entry.Timestamp, Level: safeLogLevel(entry.Level)})
	}
	content, err := json.Marshal(evidence)
	if err != nil || len(content) > 8<<10 {
		return inference.Evidence{}, errors.New(unavailable)
	}
	return inference.Evidence{
		Kind: "logs", Title: "受限日志摘要: " + request.Service,
		Content: string(content), Source: "loki", TargetID: profile.ID,
	}, nil
}

func safeLogLevel(level string) string {
	switch strings.ToLower(level) {
	case "trace", "debug", "info", "warn", "error", "fatal":
		return strings.ToLower(level)
	default:
		return "unknown"
	}
}
