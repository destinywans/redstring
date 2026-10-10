package v3telemetry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/wans/aiops-canvas/internal/v3integrations"
)

// Kubernetes workloads (PA-08, PA-19 P3). A workload target binds a service to
// one Deployment through a reviewed catalog entry; whether it may be rolled
// back and how recovery is verified are part of that review, never of a
// request. Workloads are kept apart from the telemetry surfaces so the legacy
// importer and compare code do not treat Kubernetes bindings as telemetry.
const (
	WorkloadProvider   = "kubernetes"
	WorkloadTargetKind = "workload"
	WorkloadTool       = "read_workloads"
)

// WorkloadEntry is the file shape of one reviewed workload scope.
type WorkloadEntry struct {
	ConnectorID   string          `json:"connector_id"`
	ApplicationID string          `json:"application_id"`
	EnvironmentID string          `json:"environment_id"`
	ResourceID    string          `json:"resource_id"`
	Namespace     string          `json:"namespace"`
	Deployment    string          `json:"deployment"`
	Rollback      *RollbackPolicy `json:"rollback,omitempty"`
}

// RollbackPolicy says whether the workload may be rolled back and how the
// executor verifies recovery afterwards.
type RollbackPolicy struct {
	Allowed            bool                 `json:"allowed"`
	ObservationSeconds int                  `json:"observation_seconds"`
	Verification       RollbackVerification `json:"verification"`
}

// RollbackVerification names reviewed Prometheus queries of the same
// application and environment. Each must return one series: the service's
// request rate (per second), its error ratio (0..1) and the age in seconds of
// the newest source sample.
type RollbackVerification struct {
	ConnectorID        string  `json:"connector_id"`
	ResourceID         string  `json:"resource_id"`
	RequestRateQueryID string  `json:"request_rate_query_id"`
	ErrorRatioQueryID  string  `json:"error_ratio_query_id"`
	SampleAgeQueryID   string  `json:"sample_age_query_id"`
	MaxErrorRatio      float64 `json:"max_error_ratio"`
	MinRequestRate     float64 `json:"min_request_rate"`
}

// WorkloadScope is the loaded scope of one workload target.
type WorkloadScope struct {
	Namespace  string
	Deployment string
	Rollback   *RollbackPolicy
}

const maxWorkloads = 200

// kubernetesName is a DNS-1123 label, the form of namespace and Deployment
// names.
var kubernetesName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

func parseWorkloads(entries []WorkloadEntry, queries map[v3integrations.PrometheusQueryKey]string) (map[ScopeKey]WorkloadScope, error) {
	if len(entries) > maxWorkloads {
		return nil, fmt.Errorf("source catalog has too many workloads")
	}
	out := make(map[ScopeKey]WorkloadScope, len(entries))
	for i, entry := range entries {
		key := ScopeKey{ConnectorID: entry.ConnectorID, ApplicationID: entry.ApplicationID,
			EnvironmentID: entry.EnvironmentID, ResourceID: entry.ResourceID}
		if key.ConnectorID == "" || key.ApplicationID == "" || key.EnvironmentID == "" ||
			!v3integrations.ResourceIDValid(key.ResourceID) || !kubernetesName.MatchString(entry.Namespace) ||
			!kubernetesName.MatchString(entry.Deployment) || !validRollbackPolicy(key, entry.Rollback, queries) {
			return nil, fmt.Errorf("invalid workload at index %d", i)
		}
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("duplicate workload at index %d", i)
		}
		out[key] = WorkloadScope{Namespace: entry.Namespace, Deployment: entry.Deployment, Rollback: entry.Rollback}
	}
	return out, nil
}

// validRollbackPolicy requires an enabled policy to name existing reviewed
// queries of the same application and environment and sane thresholds. A
// disabled policy may be absent.
func validRollbackPolicy(key ScopeKey, p *RollbackPolicy, queries map[v3integrations.PrometheusQueryKey]string) bool {
	if p == nil {
		return true
	}
	if !p.Allowed {
		return true
	}
	v := p.Verification
	if p.ObservationSeconds < 60 || p.ObservationSeconds > 3600 || v.ConnectorID == "" ||
		!v3integrations.ResourceIDValid(v.ResourceID) || math.IsNaN(v.MaxErrorRatio) || v.MaxErrorRatio <= 0 ||
		v.MaxErrorRatio > 1 || math.IsNaN(v.MinRequestRate) || v.MinRequestRate < 0 || math.IsInf(v.MinRequestRate, 0) {
		return false
	}
	for _, id := range []string{v.RequestRateQueryID, v.ErrorRatioQueryID, v.SampleAgeQueryID} {
		query := v3integrations.PrometheusQueryKey{ConnectorID: v.ConnectorID, ApplicationID: key.ApplicationID,
			EnvironmentID: key.EnvironmentID, ResourceID: v.ResourceID, QueryID: id}
		if id == "" || queries[query] == "" {
			return false
		}
	}
	return true
}

// Workload is what the registry says serves the workload of a service.
type Workload struct {
	TargetID      string
	ConnectorID   string
	Endpoint      string
	AuthMethod    string
	CredentialRef string
	ResourceID    string
	Scope         WorkloadScope
	// NotReady lists why the workload cannot be read yet: connector
	// disabled, tool off, credential missing or not approved, scope missing.
	NotReady []string
}

// ErrWorkloadScopeMissing means the target has no reviewed catalog scope.
var ErrWorkloadScopeMissing = errors.New("workload scope missing")

// ResolveWorkload returns the one workload target of a service, like Resolve
// for telemetry surfaces: zero targets is ErrNotConfigured, two or more is
// ErrAmbiguous, and authorization stays with the caller.
func (r *Resolver) ResolveWorkload(ctx context.Context, q Querier, applicationID, environmentID string) (Workload, error) {
	rows, err := q.Query(ctx, `SELECT t.id,t.connector_id,c.endpoint,c.auth_method,c.credential_ref,
 c.enabled,c.tools,t.external_id
 FROM v3_connector_targets t
 JOIN v3_connectors c ON c.id=t.connector_id AND c.environment_id=t.environment_id AND c.provider=$3
 JOIN applications a ON a.id=t.application_id AND NOT a.archived
 JOIN application_environments e ON e.application_id=t.application_id AND e.key=t.environment_id AND NOT e.archived
 WHERE t.application_id=$1 AND t.environment_id=$2 AND t.target_kind=$4
 ORDER BY t.id LIMIT 2`, applicationID, environmentID, WorkloadProvider, WorkloadTargetKind)
	if err != nil {
		return Workload{}, err
	}
	var found []Workload
	for rows.Next() {
		var w Workload
		var enabled bool
		var tools []byte
		if err = rows.Scan(&w.TargetID, &w.ConnectorID, &w.Endpoint, &w.AuthMethod, &w.CredentialRef,
			&enabled, &tools, &w.ResourceID); err != nil {
			break
		}
		if !enabled {
			w.NotReady = append(w.NotReady, "connector_disabled")
		}
		if !toolEnabled(tools, WorkloadTool) {
			w.NotReady = append(w.NotReady, "tool_disabled")
		}
		if w.AuthMethod != "none" {
			if w.CredentialRef == "" {
				w.NotReady = append(w.NotReady, "credential_missing")
			} else if !r.Approvals.Allowed(r.Tier, environmentID, w.ConnectorID, WorkloadProvider, w.Endpoint, w.CredentialRef) {
				w.NotReady = append(w.NotReady, "credential_not_approved")
			}
		}
		found = append(found, w)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return Workload{}, err
	}
	switch len(found) {
	case 0:
		return Workload{}, ErrNotConfigured
	case 1:
	default:
		return Workload{}, ErrAmbiguous
	}
	w := found[0]
	key := ScopeKey{ConnectorID: w.ConnectorID, ApplicationID: applicationID, EnvironmentID: environmentID, ResourceID: w.ResourceID}
	scope, ok := r.Catalog.Workloads[key]
	if !ok {
		w.NotReady = append(w.NotReady, "scope_missing")
		return w, nil
	}
	w.Scope = scope
	return w, nil
}
