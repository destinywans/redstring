package main

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/v3integrations"
	"github.com/wans/aiops-canvas/internal/v3rollouts"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// v3Registrations projects the connector registry for the capability
// readers of the legacy frontend and the operations dashboard (PA-19 P3
// stage 2 step 7, design §3.5): GET /integrations and GET
// /connector-registrations. A capability needs the connector ready (enabled,
// read tool on, valid options, approved credential) and a target with a
// catalog scope; Kubernetes rollback capabilities also need a reviewed
// rollback policy, a write approval and a ready executor.
type v3Registrations struct {
	DB           *pgxpool.Pool
	Registry     *v3telemetry.Resolver
	Workloads    *v3rollouts.Service
	Environments []string
}

// legacyCapabilities keeps the vocabulary the legacy settings page reads.
var legacyCapabilities = map[string]string{"prometheus": "metrics.read", "loki": "logs.read",
	"elasticsearch": "logs.read", "tempo": "traces.read"}

// workloads returns a Kubernetes connector's workload view (nil without one).
func (p v3Registrations) workloads(ctx context.Context, c v3telemetry.RegisteredConnector) (*v3integrations.ConnectorWorkloads, error) {
	if c.Provider != v3telemetry.WorkloadProvider || p.Workloads == nil {
		return nil, nil
	}
	view, err := p.Workloads.ConnectorWorkloads(ctx, c.ID, c.EnvironmentID)
	return &view, err
}

func (p v3Registrations) Integrations(ctx context.Context) ([]httpapi.Integration, error) {
	connectors, err := p.Registry.Registered(ctx, p.DB, p.Environments)
	if err != nil {
		return nil, err
	}
	out := make([]httpapi.Integration, 0, len(connectors))
	for _, c := range connectors {
		ready := len(c.NotReady) == 0
		capabilities := []string{}
		if ready && len(c.Targets) > 0 {
			if capability, ok := legacyCapabilities[c.Provider]; ok {
				capabilities = append(capabilities, capability)
			}
			view, err := p.workloads(ctx, c)
			if err != nil {
				return nil, err
			}
			if view != nil && slices.Contains(view.Capabilities, v3rollouts.CapabilityWorkloadRead) {
				capabilities = append(capabilities, "deployment.read")
			}
			if view != nil && slices.Contains(view.Capabilities, v3rollouts.CapabilityRollbackPropose) {
				capabilities = append(capabilities, "rollback.propose", "rollback.execute_after_approval")
			}
		}
		item := httpapi.NewIntegration(c.ID, c.Name, "connector", c.Provider, ready, nil, capabilities)
		item.TargetApplicationIDs = []string{}
		for _, target := range c.Targets {
			if !slices.Contains(item.TargetApplicationIDs, target.ApplicationID) {
				item.TargetApplicationIDs = append(item.TargetApplicationIDs, target.ApplicationID)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (p v3Registrations) Registrations(ctx context.Context) ([]map[string]any, error) {
	connectors, err := p.Registry.Registered(ctx, p.DB, p.Environments)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for _, c := range connectors {
		view, err := p.workloads(ctx, c)
		if err != nil {
			return nil, err
		}
		for _, target := range c.Targets {
			item := map[string]any{"id": c.ID, "name": c.Name, "kind": c.Provider, "resource": target.ResourceID,
				"environment_id": c.EnvironmentID, "rollback_available": false}
			if c.Provider == v3telemetry.WorkloadProvider {
				item["namespace"], item["deployment"] = target.Namespace, target.Deployment
				if view != nil && slices.Contains(view.Capabilities, v3rollouts.CapabilityRollbackPropose) {
					for _, w := range view.Workloads {
						if w.Key == target.ResourceID && w.Rollback != nil && w.Rollback.Allowed && w.WriteApproved != nil && *w.WriteApproved {
							item["rollback_available"] = true
						}
					}
				}
			}
			items = append(items, item)
		}
	}
	return items, nil
}
