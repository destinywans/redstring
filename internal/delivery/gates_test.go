package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRelease struct {
	planStatus, executionStatus string
	prepares, executes, cancels int
}

func (f *fakeRelease) Prepare(context.Context, string, string, string, string, string) (ReleasePlan, error) {
	f.prepares++
	return ReleasePlan{ID: "plan", Digest: "frozen", Status: f.planStatus}, nil
}
func (f *fakeRelease) Plan(context.Context, string, string) (ReleasePlan, error) {
	return ReleasePlan{ID: "plan", Digest: "frozen", Status: f.planStatus}, nil
}
func (f *fakeRelease) Execute(context.Context, string, string) (ReleaseExecution, error) {
	f.executes++
	return ReleaseExecution{ID: "execution", Status: f.executionStatus}, nil
}
func (f *fakeRelease) Execution(context.Context, string, string) (ReleaseExecution, error) {
	return ReleaseExecution{ID: "execution", Status: f.executionStatus}, nil
}
func (f *fakeRelease) Cancel(context.Context, string, string) error { f.cancels++; return nil }

func TestPostgresDeliveryGatesApprovalVerificationAndCancel(t *testing.T) {
	p, _ := deliveryDatabase(t)
	ctx := context.Background()
	definition := Templates()[0].Definition
	pipe, err := p.SavePipeline(ctx, Pipeline{ApplicationID: "app", EnvironmentID: "env", CreatedBy: "operator", Name: "Release", Definition: definition}, 0)
	if err != nil {
		t.Fatal(err)
	}
	run, err := p.CreateRun(ctx, pipe, "operator", "gated")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.DB.Exec(ctx, "UPDATE delivery_runs SET status='running' WHERE id=$1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	phases := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			var in struct {
				Node  string `json:"nodeFieldSelector"`
				Phase string `json:"phase"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			phases[strings.TrimPrefix(in.Node, "id=")] = in.Phase
		}
		nodes := map[string]any{}
		for _, id := range []string{"approval", "deploy", "health"} {
			phase := phases[id]
			if phase == "" {
				phase = "Running"
			}
			nodes[id] = map[string]any{"id": id, "displayName": id, "type": "Suspend", "phase": phase}
		}
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "aiops-" + run.ID, "annotations": map[string]string{"aiops.operation-id": run.ID, "aiops.pipeline-digest": run.Snapshot.Digest}}, "status": map[string]any{"phase": "Running", "nodes": nodes}})
	}))
	defer server.Close()
	argo, err := NewArgoClient(argoConfig(server.URL), "test")
	if err != nil {
		t.Fatal(err)
	}
	release := &fakeRelease{planStatus: "proposed", executionStatus: "verifying"}
	service := &Service{Repository: p, Argo: argo, Release: release, Authorize: func(context.Context, string, string, string) error { return nil }, ValidateEnvironment: func(context.Context, string, string) error { return nil }}
	projection := func(id string) Projection {
		return Projection{Status: "running", Nodes: []NodeResult{{ID: "build", Name: "build", Status: "succeeded", Outputs: map[string]string{"artifact-digest": "sha256:" + strings.Repeat("f", 64)}}, {ID: id, Name: id, Status: "running"}}}
	}
	for i := 0; i < 2; i++ {
		if err = service.advanceGates(ctx, run, projection("approval")); err != nil {
			t.Fatal(err)
		}
	}
	if release.prepares != 1 || release.executes != 0 || phases["approval"] != "" {
		t.Fatal("approval gate bypass or duplicate plan")
	}
	release.planStatus = "approved"
	if err = service.advanceGates(ctx, run, projection("approval")); err != nil {
		t.Fatal(err)
	}
	if phases["approval"] != "Succeeded" {
		t.Fatal("approved gate not resumed")
	}
	if err = service.advanceGates(ctx, run, projection("deploy")); err != nil {
		t.Fatal(err)
	}
	if release.executes != 1 || phases["deploy"] != "Succeeded" {
		t.Fatal("deployment did not enter separate health verification")
	}
	if err = service.advanceGates(ctx, run, projection("health")); err != nil {
		t.Fatal(err)
	}
	if phases["health"] != "" {
		t.Fatal("health completed before provider verification")
	}
	release.executionStatus = "succeeded"
	if err = service.advanceGates(ctx, run, projection("health")); err != nil {
		t.Fatal(err)
	}
	if phases["health"] != "Succeeded" || release.executes != 1 {
		t.Fatal("health evidence failed or execution repeated")
	}
	_, err = p.DB.Exec(ctx, "UPDATE delivery_runs SET status='cancel_requested' WHERE id=$1", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := release.executes
	if err = service.advanceGates(ctx, run, projection("deploy")); err != nil {
		t.Fatal(err)
	}
	if release.executes != before {
		t.Fatal("stale observed run bypassed committed cancellation")
	}
}
