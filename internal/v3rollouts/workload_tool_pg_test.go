package v3rollouts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/destinywans/redstring/internal/v3catalog"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3kube/kubetest"
)

func (f *rolloutFixture) workloadTool() inference.EvidenceTool {
	f.t.Helper()
	var requester core.User
	if err := f.db.QueryRow(f.t.Context(), `SELECT id FROM users WHERE email='requester@example.invalid'`).Scan(&requester.ID); err != nil {
		f.t.Fatal(err)
	}
	tool, ok := f.service.EvidenceTool(WorkloadToolOptions{OwnerID: requester.ID, EnvironmentID: "dev",
		CurrentUser: func(_ context.Context, id string) (core.User, error) { return core.User{ID: id}, nil },
		Lookup: func(_ context.Context, _ core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if key != "checkout" {
				return v3catalog.ServiceEvidence{}, store.ErrNotFound
			}
			return v3catalog.ServiceEvidence{ID: ServiceID("app", env), Key: key, Environment: env}, nil
		}})
	if !ok || tool.Name != WorkloadToolName {
		f.t.Fatal("workload tool unavailable")
	}
	return tool
}

// PA-72: the model sees revisions and what changes, never an environment
// variable's value; an empty Vault and a missing target are tool errors.
func TestWorkloadToolForTheModel(t *testing.T) {
	f := newRolloutFixture(t)
	tool := f.workloadTool()
	evidence, err := tool.Read(t.Context(), `{"service":"checkout"}`)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != "workload" || evidence.Source != "kubernetes" || evidence.TargetID != ServiceID("app", "dev") {
		t.Fatalf("evidence: %+v", evidence)
	}
	// The current revision carries LAB_FAULTS={"http_error_ratio":0.5}.
	if strings.Contains(evidence.Content, "http_error_ratio") || strings.Contains(evidence.Content, "0.5") ||
		!strings.Contains(evidence.Content, `"name":"LAB_FAULTS"`) {
		t.Fatalf("env values must not reach the model: %s", evidence.Content)
	}
	var w WorkloadEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &w); err != nil {
		t.Fatal(err)
	}
	if w.CurrentRevision != "2" || len(w.Revisions) != 2 || !w.Revisions[0].Current || w.Revisions[1].Revision != "1" ||
		len(w.Revisions[1].Changes) != 1 || w.Revisions[1].Changes[0].From != nil || w.Rollback.BlockedReason != nil ||
		w.Rollback.ObservationSeconds == nil || *w.Rollback.ObservationSeconds != 180 ||
		!strings.Contains(w.ChangesBasis, "from is the current revision's value") {
		t.Fatalf("workload evidence: %+v", w)
	}
	if w.Revisions[0].ReplicaSetReused || w.Revisions[1].ReplicaSetReused || !strings.Contains(tool.Description, "replicaset_reused") {
		t.Fatalf("new ReplicaSets flagged as reused: %+v", w.Revisions)
	}
	// PA-94: returning to the good template reuses revision 1's ReplicaSet
	// as revision 3; its created_at is still that ReplicaSet's creation.
	f.cluster.Touch("checkout-api", kubetest.Template("checkout-api", "api:1.4.2", nil))
	if evidence, err = tool.Read(t.Context(), "checkout"); err != nil || json.Unmarshal([]byte(evidence.Content), &w) != nil {
		t.Fatalf("after reuse: %v", err)
	}
	if w.CurrentRevision != "3" || w.Revisions[0].Revision != "3" || !w.Revisions[0].ReplicaSetReused || w.Revisions[1].ReplicaSetReused {
		t.Fatalf("reused ReplicaSet: %+v", w.Revisions)
	}
	if _, err := tool.Read(t.Context(), "other-service"); inference.ToolErrorCode(err) != inference.ToolUnknownService {
		t.Fatalf("other service: %v", err)
	}

	// A restarted dev-mode Vault: the read token is gone.
	f.secrets.set("vault://kv/lab/k8s-reader#token", "")
	if _, err := tool.Read(t.Context(), "checkout"); inference.ToolErrorCode(err) != inference.ToolSourceUnavailable {
		t.Fatalf("empty Vault: %v", err)
	}
	f.secrets.set("vault://kv/lab/k8s-reader#token", "token")
	if _, err := f.db.Exec(t.Context(), `DELETE FROM v3_connector_targets WHERE id='t-workload'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Read(t.Context(), "checkout"); inference.ToolErrorCode(err) != inference.ToolNotConfigured {
		t.Fatalf("no workload target: %v", err)
	}
}

// PA-72: the Kubernetes connector detail lists its workloads with the
// reviewed rollback setting and write approval, and its capabilities.
func TestConnectorDetailWorkloads(t *testing.T) {
	f := newRolloutFixture(t)
	connectors := v3integrations.New(f.db, "nonprod", f.service.Credentials)
	connectors.Workloads = f.service.ConnectorWorkloads
	connectors.Register(f.api)
	var detail struct {
		Workloads    []v3integrations.ConnectorWorkload `json:"workloads"`
		Capabilities []string                           `json:"capabilities"`
	}
	f.decode(f.call("GET", "/api/v1/connectors/k8s", "requester", ""), 200, &detail)
	if len(detail.Workloads) != 1 || detail.Workloads[0].Key != "checkout-api" || detail.Workloads[0].Namespace != "aiops-lab" ||
		detail.Workloads[0].Service == nil || *detail.Workloads[0].Service != "checkout" || detail.Workloads[0].Rollback == nil ||
		!detail.Workloads[0].Rollback.Allowed || *detail.Workloads[0].Rollback.ObservationSeconds != 180 ||
		detail.Workloads[0].WriteApproved == nil || !*detail.Workloads[0].WriteApproved ||
		strings.Join(detail.Capabilities, ",") != "workload.read,revisions.read,rollback.propose" {
		t.Fatalf("kubernetes connector detail: %+v", detail)
	}
	var other map[string]any
	f.decode(f.call("GET", "/api/v1/connectors/prom", "requester", ""), 200, &other)
	if _, present := other["workloads"]; present {
		t.Fatalf("a Prometheus connector has no workloads: %v", other)
	}

	// Without write approvals (and so without a ready executor) the
	// workload is read-only and rollback cannot be proposed.
	readOnly := &Service{DB: f.db, Tier: "nonprod", Registry: f.service.Registry}
	view, err := readOnly.ConnectorWorkloads(t.Context(), "k8s", "dev")
	if err != nil || len(view.Workloads) != 1 || *view.Workloads[0].WriteApproved ||
		strings.Join(view.Capabilities, ",") != "workload.read,revisions.read" {
		t.Fatalf("read-only view: %+v %v", view, err)
	}
}
