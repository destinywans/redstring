package v3telemetry

import (
	"context"
	"sort"
)

// Summary is what the registry holds for an instance's environments, for the
// startup log (PA-19 P3 stage 2 step 8): targets by kind, connectors by
// provider, catalog entries, and the telemetry and Kubernetes connectors
// that are not ready with their reasons.
type Summary struct {
	Targets          map[string]int
	Connectors       map[string]int
	CatalogQueries   int
	CatalogScopes    int
	CatalogWorkloads int
	NotReady         []NotReadyConnector
}

// NotReadyConnector is a connector the registry readers cannot read yet.
type NotReadyConnector struct {
	ID, EnvironmentID, Provider string
	Reasons                     []string
}

// Summarize counts the registry of environments.
func (r *Resolver) Summarize(ctx context.Context, q Querier, environments []string) (Summary, error) {
	out := Summary{Targets: map[string]int{}, Connectors: map[string]int{}}
	rows, err := q.Query(ctx, `SELECT target_kind,count(*) FROM v3_connector_targets
 WHERE environment_id=ANY($1::text[]) GROUP BY target_kind`, environments)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var kind string
		var count int
		if err = rows.Scan(&kind, &count); err != nil {
			break
		}
		out.Targets[kind] = count
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.Query(ctx, `SELECT id,environment_id,provider,endpoint,auth_method,credential_ref,enabled,tools,options
 FROM v3_connectors WHERE environment_id=ANY($1::text[]) ORDER BY environment_id,id LIMIT 10001`, environments)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var state ConnectorState
		var env string
		if err := rows.Scan(&state.ID, &env, &state.Provider, &state.Endpoint, &state.AuthMethod, &state.CredentialRef,
			&state.Enabled, &state.Tools, &state.Options); err != nil {
			return out, err
		}
		out.Connectors[state.Provider]++
		if _, telemetry := telemetryProviders[state.Provider]; !telemetry && state.Provider != WorkloadProvider {
			continue
		}
		if reasons := r.Readiness(env, state); len(reasons) > 0 {
			out.NotReady = append(out.NotReady, NotReadyConnector{ID: state.ID, EnvironmentID: env, Provider: state.Provider, Reasons: reasons})
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	envs := map[string]bool{}
	for _, env := range environments {
		envs[env] = true
	}
	for key := range r.Catalog.Queries {
		if envs[key.EnvironmentID] {
			out.CatalogQueries++
		}
	}
	for key := range r.Catalog.Scopes {
		if envs[key.EnvironmentID] {
			out.CatalogScopes++
		}
	}
	for key := range r.Catalog.Workloads {
		if envs[key.EnvironmentID] {
			out.CatalogWorkloads++
		}
	}
	return out, nil
}

// readerTargetKinds are the target kinds a reader needs at least one of.
var readerTargetKinds = map[string][]string{
	ReaderCoverage:  {"metric_resource", "log_stream", "log_index", "trace_service"},
	ReaderLogs:      {"log_stream", "log_index"},
	ReaderTraces:    {"trace_service"},
	ReaderAIMetrics: {"metric_resource"},
}

// EmptyReaders lists the switched readers that have no target of the kinds
// they read: every service would be not_configured for them.
func (s Summary) EmptyReaders(readers Readers) []string {
	var out []string
	for reader, kinds := range readerTargetKinds {
		if !readers.Unified(reader) {
			continue
		}
		total := 0
		for _, kind := range kinds {
			total += s.Targets[kind]
		}
		if total == 0 {
			out = append(out, reader)
		}
	}
	sort.Strings(out)
	return out
}
