package v3traces

import (
	"context"
	"errors"
	"strings"

	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// PA-19 P3 stage 2 step 4: with the traces reader switched, the trace list
// and detail read registered trace_service targets instead of the legacy
// tempo bindings. The targets of one connector are read together with that
// connector's client, so a trace is still checked against every authorized
// registration of its Tempo at once; several connectors are read one after
// another under one search budget, so grouping never raises a limit.

var (
	errScopeChanged = errors.New("registered trace source changed since the targets were listed")
	errClient       = errors.New("registered trace source cannot be read")
)

// notReadyError lists why a registered trace source cannot be read yet.
type notReadyError struct{ reasons []string }

func (e notReadyError) Error() string {
	return "trace source not ready: " + strings.Join(e.reasons, ", ")
}

// registryBatch lists the trace targets of the given environments (and
// service), keyed like the legacy batch.
func (s *Service) registryBatch(ctx context.Context, environments []string, service string, cursor target, hasCursor bool) ([]target, error) {
	rows, err := s.Store.DB.Query(ctx, `SELECT a.id,ve.environment_id,COALESCE(a.owner_team_id,''),c.provider,t.external_id,t.connector_id,t.id
 FROM applications a JOIN v3_application_environments ve ON ve.application_id=a.id
 JOIN application_environments e ON e.application_id=a.id AND e.key=ve.environment_id AND NOT e.archived
 JOIN v3_connector_targets t ON t.application_id=a.id AND t.environment_id=ve.environment_id
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id
 WHERE NOT a.archived AND ve.environment_id=ANY($1::text[]) AND ($2='' OR a.id=$2 OR a.key=$2)
 AND t.target_kind||':'||c.provider=ANY($3::text[])
 AND (NOT $4::boolean OR (a.id,ve.environment_id,t.id)>($5,$6,$7))
 ORDER BY a.id,ve.environment_id,t.id LIMIT 200`, environments, service, v3telemetry.SurfacePairs(v3telemetry.Traces),
		hasCursor, cursor.ApplicationID, cursor.EnvironmentID, cursor.TargetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	batch := make([]target, 0, 200)
	for rows.Next() {
		var item target
		if err := rows.Scan(&item.ApplicationID, &item.EnvironmentID, &item.TeamID, &item.Provider, &item.Resource,
			&item.ConnectorID, &item.TargetID); err != nil {
			return nil, err
		}
		batch = append(batch, item)
	}
	return batch, rows.Err()
}

// traceGroup is the targets of one connector and that connector's client.
type traceGroup struct {
	client   *connectors.Telemetry
	all      []string
	selected []string
}

// registryGroups resolves every authorized target and builds one client per
// connector. Selected targets are those the query searches or must match.
func (s *Service) registryGroups(ctx context.Context, all, selected []target) ([]traceGroup, error) {
	if s.Registry == nil || s.Clients == nil {
		return nil, errClient
	}
	isSelected := map[string]bool{}
	for _, item := range selected {
		isSelected[item.TargetID] = true
	}
	type pending struct {
		env      string
		sources  []v3telemetry.Source
		all      []string
		selected []string
	}
	byConnector := map[string]*pending{}
	order := []string{}
	for _, item := range all {
		source, err := s.Registry.Resolve(ctx, s.Store.DB, item.ApplicationID, item.EnvironmentID, v3telemetry.Traces)
		switch {
		case errors.Is(err, v3telemetry.ErrAmbiguous):
			return nil, errAmbiguous
		case errors.Is(err, v3telemetry.ErrNotConfigured):
			return nil, errScopeChanged
		case err != nil:
			return nil, err
		case source.TargetID != item.TargetID || source.ConnectorID != item.ConnectorID || source.ResourceID != item.Resource:
			return nil, errScopeChanged
		case len(source.NotReady) > 0:
			return nil, notReadyError{reasons: source.NotReady}
		}
		key := v3telemetry.ScopeKey{ConnectorID: source.ConnectorID, ApplicationID: item.ApplicationID,
			EnvironmentID: item.EnvironmentID, ResourceID: source.ResourceID}
		if s.Registry.Catalog.TempoScopeOverlaps(key) {
			return nil, &connectors.TelemetryError{Kind: "configuration", Message: "Registered Tempo selectors overlap across resources"}
		}
		group := byConnector[item.ConnectorID]
		if group == nil {
			group = &pending{env: item.EnvironmentID}
			byConnector[item.ConnectorID] = group
			order = append(order, item.ConnectorID)
		}
		group.sources = append(group.sources, source)
		group.all = append(group.all, source.ResourceID)
		if isSelected[item.TargetID] {
			group.selected = append(group.selected, source.ResourceID)
		}
	}
	groups := make([]traceGroup, 0, len(order))
	for _, connector := range order {
		group := byConnector[connector]
		client, err := s.Clients.ForGroup(group.env, group.sources)
		if err != nil {
			return nil, errClient
		}
		groups = append(groups, traceGroup{client: client, all: group.all, selected: group.selected})
	}
	return groups, nil
}

// registryObserved is the list search over every group that has a selected
// target. The population is complete only when every group's is.
func registryObserved(ctx context.Context, groups []traceGroup, q connectors.CompleteTraceQuery) (connectors.TraceObservation, error) {
	q.Budget = &connectors.TraceSearchBudget{}
	out := connectors.TraceObservation{Traces: []connectors.CompleteTrace{}, PopulationComplete: true}
	seen := map[string]bool{}
	for _, group := range groups {
		if len(group.selected) == 0 {
			continue
		}
		q.Resources, q.SearchResources = group.all, group.selected
		observed, err := group.client.QueryObservedTraces(ctx, q)
		if err != nil {
			return connectors.TraceObservation{}, err
		}
		out.PopulationComplete = out.PopulationComplete && observed.PopulationComplete
		for _, trace := range observed.Traces {
			if seen[trace.ID] {
				// One trace stored by two registered Tempos cannot be shown
				// as either one's complete trace.
				return connectors.TraceObservation{}, &connectors.TelemetryError{Kind: "partial", Message: "Trace is stored by more than one registered Tempo"}
			}
			seen[trace.ID] = true
			out.Traces = append(out.Traces, trace)
		}
	}
	return out, nil
}

// registryTrace reads one trace by ID from every group that has a selected
// target. A group that cannot show it in its scope does not have it; the
// trace must be found in exactly one group.
func registryTrace(ctx context.Context, groups []traceGroup, q connectors.CompleteTraceQuery) ([]connectors.CompleteTrace, error) {
	q.Budget = &connectors.TraceSearchBudget{}
	var found []connectors.CompleteTrace
	var outOfScope error
	for _, group := range groups {
		if len(group.selected) == 0 {
			continue
		}
		q.Resources, q.SearchResources = group.all, group.selected
		traces, err := group.client.QueryCompleteTraces(ctx, q)
		var telemetry *connectors.TelemetryError
		if errors.As(err, &telemetry) && telemetry.Kind == "scope" {
			outOfScope = err
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(traces) > 0 && len(found) > 0 {
			return nil, &connectors.TelemetryError{Kind: "partial", Message: "Trace is stored by more than one registered Tempo"}
		}
		found = append(found, traces...)
	}
	if len(found) == 0 && outOfScope != nil {
		return nil, outOfScope
	}
	if found == nil {
		found = []connectors.CompleteTrace{}
	}
	return found, nil
}
