package v3rollouts

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3telemetry"
)

const maxConnectorWorkloads = 200

// Capability names on a Kubernetes connector's detail (design §3.5).
const (
	CapabilityWorkloadRead    = "workload.read"
	CapabilityRevisionsRead   = "revisions.read"
	CapabilityRollbackPropose = "rollback.propose"
)

var errTooManyWorkloads = errors.New("too many workload targets on one connector")

// ConnectorWorkloads is v3integrations' Workloads hook (PA-72): each workload
// target of the connector with its reviewed rollback setting and write
// approval, and the connector's capabilities. Read-only; no credential
// reference is returned. Capabilities need the connector enabled, its
// read_workloads tool on and its read credential approved; rollback.propose
// also needs a workload that may be rolled back with a write approval and a
// ready executor.
func (s *Service) ConnectorWorkloads(ctx context.Context, connectorID, env string) (v3integrations.ConnectorWorkloads, error) {
	out := v3integrations.ConnectorWorkloads{Workloads: []v3integrations.ConnectorWorkload{}, Capabilities: []string{}}
	var enabled bool
	var tools []byte
	var endpoint, credentialRef string
	err := s.DB.QueryRow(ctx, `SELECT enabled,tools,endpoint,credential_ref FROM v3_connectors
 WHERE id=$1 AND environment_id=$2 AND provider=$3`, connectorID, env, v3telemetry.WorkloadProvider).
		Scan(&enabled, &tools, &endpoint, &credentialRef)
	if err != nil {
		return out, err
	}
	rows, err := s.DB.Query(ctx, `SELECT t.external_id,t.application_id,
 CASE WHEN NOT a.archived AND EXISTS(SELECT 1 FROM v3_application_environments ve WHERE ve.application_id=a.id AND ve.environment_id=$2)
  THEN a.key END
 FROM v3_connector_targets t JOIN applications a ON a.id=t.application_id
 WHERE t.connector_id=$1 AND t.environment_id=$2 AND t.target_kind=$3
 ORDER BY t.external_id,t.id LIMIT $4`, connectorID, env, v3telemetry.WorkloadTargetKind, maxConnectorWorkloads+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	proposable := false
	for rows.Next() {
		var key, app string
		var service *string
		if err = rows.Scan(&key, &app, &service); err != nil {
			return out, err
		}
		if len(out.Workloads) == maxConnectorWorkloads {
			return out, errTooManyWorkloads
		}
		w := v3integrations.ConnectorWorkload{Key: key, Service: service}
		if s.Registry != nil {
			scope, ok := s.Registry.Catalog.Workloads[v3telemetry.ScopeKey{ConnectorID: connectorID, ApplicationID: app,
				EnvironmentID: env, ResourceID: key}]
			if ok {
				w.Namespace, w.Deployment = scope.Namespace, scope.Deployment
				policy := &v3integrations.ConnectorWorkloadPolicy{}
				if p := scope.Rollback; p != nil && p.Allowed {
					seconds := s.minimumObservation(p.ObservationSeconds)
					policy.Allowed, policy.ObservationSeconds = true, &seconds
				}
				w.Rollback = policy
				_, approved := findWriteBinding(s.WriteBindings, s.Tier, env, connectorID, scope.Namespace, scope.Deployment)
				w.WriteApproved = &approved
				proposable = proposable || (policy.Allowed && approved)
			}
		}
		out.Workloads = append(out.Workloads, w)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	readable := enabled && toolOn(tools, v3telemetry.WorkloadTool) && credentialRef != "" && s.Registry != nil &&
		s.Registry.Approvals.Allowed(s.Tier, env, connectorID, v3telemetry.WorkloadProvider, endpoint, credentialRef)
	if readable {
		out.Capabilities = append(out.Capabilities, CapabilityWorkloadRead, CapabilityRevisionsRead)
		if proposable && s.Ready(ctx, env) == nil {
			out.Capabilities = append(out.Capabilities, CapabilityRollbackPropose)
		}
	}
	return out, nil
}

func toolOn(raw []byte, name string) bool {
	var tools []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, t := range tools {
		if t.Name == name && t.Enabled {
			return true
		}
	}
	return false
}
