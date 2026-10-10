package v3conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/v3catalog"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
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
	// Registry answers the metrics and logs columns (PA-19 P3 stage 2 step
	// 5, with read_service_metrics): each service's one target per surface,
	// its readiness and the catalog's query IDs. Expressions never leave it.
	// Without it both columns are not_configured.
	Registry *v3telemetry.Resolver
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
	description := "List the services you may read in this conversation's environment: exact service keys for the other tools, " +
		"whether each has a metrics or log source, and the metric query IDs configured for it. Pass an empty string or {}. " +
		"Call this first when the question does not name a service key. Only services you are allowed to view are listed. " +
		"not_ready means a source is registered but cannot be read yet; the read tools explain why."
	return inference.EvidenceTool{
		Name:        "list_services",
		Description: description,
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
	registered, err := registeredListSources(ctx, opts, keys)
	if err != nil {
		return inference.Evidence{}, inference.NewToolError(inference.ToolSourceUnavailable, "source registrations are temporarily unavailable; retry later")
	}
	out := serviceListEvidence{EnvironmentID: opts.EnvironmentID, Services: []serviceListItem{}, Truncated: truncated,
		Boundary: "Service keys and configured sources only, not health. A listed source can still fail or return no data; " +
			"not_configured means the platform has no source of that kind for the service."}
	for _, service := range services {
		item := serviceListItem{Key: service.Key, Name: service.Name, Tier: service.Tier, Team: service.Team,
			Metrics: "not_configured", MetricQueries: []string{}, Logs: "not_configured"}
		if found, ok := registered[service.Key]; ok {
			item.Metrics, item.MetricQueries, item.Logs = found.Metrics, found.MetricQueries, found.Logs
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

// registeredListSources answers the metrics and logs columns from the
// registry, by service key. A key without an application in the environment
// is left out (both columns stay not_configured).
func registeredListSources(ctx context.Context, opts ServiceListToolOptions, keys []string) (map[string]serviceListItem, error) {
	out := map[string]serviceListItem{}
	if len(keys) == 0 || opts.Registry == nil {
		return out, nil
	}
	rows, err := opts.DB.Query(ctx, `SELECT a.key,a.id
 FROM applications a
 JOIN v3_application_environments ve ON ve.application_id=a.id AND ve.environment_id=$2
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 WHERE a.key=ANY($1::text[]) AND NOT a.archived`, keys, opts.EnvironmentID)
	if err != nil {
		return nil, err
	}
	apps := map[string]string{}
	for rows.Next() {
		var key, app string
		if err = rows.Scan(&key, &app); err != nil {
			break
		}
		apps[key] = app
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	state := func(app string, surface v3telemetry.Surface) (string, v3telemetry.Source, error) {
		source, err := opts.Registry.Resolve(ctx, opts.DB, app, opts.EnvironmentID, surface)
		switch {
		case errors.Is(err, v3telemetry.ErrNotConfigured):
			return "not_configured", source, nil
		case errors.Is(err, v3telemetry.ErrAmbiguous):
			return "ambiguous", source, nil
		case err != nil:
			return "", source, err
		case len(source.NotReady) > 0:
			return "not_ready", source, nil
		}
		return "configured", source, nil
	}
	for _, key := range keys {
		app, ok := apps[key]
		if !ok {
			continue
		}
		item := serviceListItem{MetricQueries: []string{}}
		metrics, source, err := state(app, v3telemetry.Metrics)
		if err != nil {
			return nil, err
		}
		item.Metrics = metrics
		if metrics == "configured" {
			item.MetricQueries = sortedQueryIDs(source.Queries)
		}
		if item.Logs, _, err = state(app, v3telemetry.Logs); err != nil {
			return nil, err
		}
		out[key] = item
	}
	return out, nil
}

// logsRegistered asks the telemetry source whether a log binding can be read;
// without that ability every persisted binding counts as configured.
