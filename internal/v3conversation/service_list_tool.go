package v3conversation

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/v3catalog"
)

// ServiceListToolOptions lists the services the conversation owner may read
// (PA-50). Without it a model that is not told a service key can only guess,
// and every guess fails.
type ServiceListToolOptions struct {
	DB            *pgxpool.Pool
	OwnerID       string
	EnvironmentID string
	CurrentUser   func(context.Context, string) (core.User, error)
	ListServices  func(context.Context, core.User, string, int) ([]v3catalog.ServiceEvidence, bool, error)
	// Telemetry, when it can tell, supplies each service's metric query IDs
	// and whether its log source is registered. Expressions never leave it.
	Telemetry any
}

const maxListedServices = 100

type serviceListItem struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Tier          string   `json:"tier,omitempty"`
	Team          string   `json:"team,omitempty"`
	Metrics       string   `json:"metrics"`
	MetricQueries []string `json:"metric_queries"`
	Logs          string   `json:"logs"`
}

type serviceListEvidence struct {
	EnvironmentID string            `json:"environment_id"`
	Services      []serviceListItem `json:"services"`
	Truncated     bool              `json:"truncated"`
	Boundary      string            `json:"boundary"`
}

// NewServiceListTool returns list_services. It reads only the catalog and
// the server's source registrations, rechecks the owner and service grants on
// every call, and lists nothing the owner cannot view.
func NewServiceListTool(opts ServiceListToolOptions) (inference.EvidenceTool, bool) {
	if opts.DB == nil || opts.CurrentUser == nil || opts.ListServices == nil || opts.OwnerID == "" ||
		opts.EnvironmentID == "" || opts.EnvironmentID == "all" {
		return inference.EvidenceTool{}, false
	}
	return inference.EvidenceTool{
		Name: "list_services",
		Description: "List the services you may read in this conversation's environment: exact service keys for the other tools, " +
			"whether each has a metrics or log source, and the metric query IDs configured for it. Pass an empty string or {}. " +
			"Call this first when the question does not name a service key. Only services you are allowed to view are listed.",
		Read: func(ctx context.Context, input string) (inference.Evidence, error) {
			return readServiceList(ctx, opts, input)
		},
	}, true
}

func readServiceList(ctx context.Context, opts ServiceListToolOptions, input string) (inference.Evidence, error) {
	if trimmed := strings.TrimSpace(input); trimmed != "" && trimmed != "{}" && trimmed != `""` {
		return inference.Evidence{}, inference.NewToolError(inference.ToolInvalidInput, "list_services takes no input; pass an empty string or {}")
	}
	user, err := opts.CurrentUser(ctx, opts.OwnerID)
	if err != nil || user.ID != opts.OwnerID || user.Disabled {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "the conversation owner could not be verified; retry later")
	}
	services, truncated, err := opts.ListServices(ctx, user, opts.EnvironmentID, maxListedServices)
	if err != nil {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "the service catalog is temporarily unavailable; retry later")
	}
	keys := make([]string, 0, len(services))
	for _, service := range services {
		keys = append(keys, service.Key)
	}
	type sources struct{ metrics, logs []string }
	bound := map[string]*sources{}
	if len(keys) > 0 {
		rows, err := opts.DB.Query(ctx, `SELECT a.key,b.kind,b.resource
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN LATERAL jsonb_to_recordset(e.bindings) AS b(kind text, resource text) ON b.kind IN ('prometheus','loki','elasticsearch')
 WHERE a.key=ANY($1::text[]) AND NOT a.archived AND b.resource<>''`, keys, opts.EnvironmentID)
		if err != nil {
			return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "source registrations are temporarily unavailable; retry later")
		}
		for rows.Next() {
			var key, kind, resource string
			if err = rows.Scan(&key, &kind, &resource); err != nil {
				break
			}
			if bound[key] == nil {
				bound[key] = &sources{}
			}
			if kind == "prometheus" {
				bound[key].metrics = append(bound[key].metrics, resource)
			} else if logsRegistered(opts.Telemetry, kind, resource) {
				bound[key].logs = append(bound[key].logs, kind)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "source registrations are temporarily unavailable; retry later")
		}
	}
	out := serviceListEvidence{EnvironmentID: opts.EnvironmentID, Services: []serviceListItem{}, Truncated: truncated,
		Boundary: "Service keys and configured sources only, not health. A listed source can still fail or return no data; " +
			"not_configured means the platform has no source of that kind for the service."}
	for _, service := range services {
		item := serviceListItem{Key: service.Key, Name: service.Name, Tier: service.Tier, Team: service.Team,
			Metrics: "not_configured", MetricQueries: []string{}, Logs: "not_configured"}
		if found := bound[service.Key]; found != nil {
			if len(found.metrics) == 1 {
				item.Metrics = "configured"
				if ids, known := metricQueryIDs(opts.Telemetry, found.metrics[0]); known {
					if len(ids) == 0 {
						item.Metrics = "not_configured"
					} else {
						item.MetricQueries = ids
					}
				}
			} else if len(found.metrics) > 1 {
				item.Metrics = "ambiguous"
			}
			if len(found.logs) == 1 {
				item.Logs = "configured"
			} else if len(found.logs) > 1 {
				item.Logs = "ambiguous"
			}
		}
		out.Services = append(out.Services, item)
	}
	content, err := json.Marshal(out)
	if err != nil {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "the service list could not be encoded")
	}
	return inference.Evidence{Kind: "service_list", Title: inference.Localized(ctx, "可读取的服务：", "Readable services in ") + opts.EnvironmentID,
		Content: string(content), Source: "service_catalog", TargetID: opts.EnvironmentID}, nil
}

// logsRegistered asks the telemetry source whether a log binding can be read;
// without that ability every persisted binding counts as configured.
func logsRegistered(telemetry any, provider, resource string) bool {
	if registry, ok := telemetry.(interface{ LogsRegistered(string, string) bool }); ok {
		return registry.LogsRegistered(provider, resource)
	}
	return true
}
