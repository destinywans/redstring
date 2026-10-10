package v3telemetry

import (
	"context"
	"errors"
)

// RegisteredConnector is a telemetry or Kubernetes connector as the legacy
// capability readers project it (PA-19 P3 stage 2 step 7): its readiness and
// the targets the catalog has a scope for.
type RegisteredConnector struct {
	ID, EnvironmentID, Provider, Name string
	NotReady                          []string
	Targets                           []RegisteredTarget
}

// RegisteredTarget is one target with a catalog scope; Namespace and
// Deployment are set for workloads.
type RegisteredTarget struct {
	ID, ApplicationID, ResourceID string
	Namespace, Deployment         string
}

const maxRegisteredRows = 10000

var errRegisteredLimit = errors.New("too many registered connectors or targets")

// Registered lists the telemetry and Kubernetes connectors of environments,
// each with its readiness (Readiness) and the targets of its kind whose
// service is active and whose catalog scope exists, ordered by environment,
// name and ID.
func (r *Resolver) Registered(ctx context.Context, q Querier, environments []string) ([]RegisteredConnector, error) {
	providers := []string{WorkloadProvider}
	for provider := range telemetryProviders {
		providers = append(providers, provider)
	}
	rows, err := q.Query(ctx, `SELECT id,environment_id,provider,name,endpoint,auth_method,credential_ref,enabled,tools,options
 FROM v3_connectors WHERE environment_id=ANY($1::text[]) AND provider=ANY($2::text[])
 ORDER BY environment_id,name,id LIMIT $3`, environments, providers, maxRegisteredRows+1)
	if err != nil {
		return nil, err
	}
	var out []RegisteredConnector
	index := map[string]int{}
	for rows.Next() {
		var c RegisteredConnector
		var state ConnectorState
		if err = rows.Scan(&state.ID, &c.EnvironmentID, &state.Provider, &c.Name, &state.Endpoint, &state.AuthMethod,
			&state.CredentialRef, &state.Enabled, &state.Tools, &state.Options); err != nil {
			break
		}
		c.ID, c.Provider = state.ID, state.Provider
		c.NotReady = r.Readiness(c.EnvironmentID, state)
		c.Targets = []RegisteredTarget{}
		index[c.EnvironmentID+"\x00"+c.ID] = len(out)
		out = append(out, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(out) > maxRegisteredRows {
		return nil, errRegisteredLimit
	}
	rows, err = q.Query(ctx, `SELECT t.id,t.environment_id,t.connector_id,t.application_id,t.target_kind,t.external_id
 FROM v3_connector_targets t
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN application_environments e ON e.application_id=t.application_id AND e.key=t.environment_id AND NOT e.archived
 WHERE t.environment_id=ANY($1::text[]) ORDER BY t.environment_id,t.connector_id,t.external_id,t.id LIMIT $2`,
		environments, maxRegisteredRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var target RegisteredTarget
		var env, connector, kind string
		if err := rows.Scan(&target.ID, &env, &connector, &target.ApplicationID, &kind, &target.ResourceID); err != nil {
			return nil, err
		}
		if count++; count > maxRegisteredRows {
			return nil, errRegisteredLimit
		}
		i, ok := index[env+"\x00"+connector]
		if !ok {
			continue
		}
		c := &out[i]
		key := ScopeKey{ConnectorID: c.ID, ApplicationID: target.ApplicationID, EnvironmentID: env, ResourceID: target.ResourceID}
		switch {
		case c.Provider == WorkloadProvider && kind == WorkloadTargetKind:
			scope, ok := r.Catalog.Workloads[key]
			if !ok {
				continue
			}
			target.Namespace, target.Deployment = scope.Namespace, scope.Deployment
		case c.Provider == "prometheus" && kind == TargetKind(c.Provider):
			if len(r.Catalog.QueriesFor(key)) == 0 {
				continue
			}
		case kind == TargetKind(c.Provider) && c.Provider != WorkloadProvider:
			if scope, ok := r.Catalog.Scopes[key]; !ok || scope.Provider != c.Provider {
				continue
			}
		default:
			continue
		}
		c.Targets = append(c.Targets, target)
	}
	return out, rows.Err()
}
