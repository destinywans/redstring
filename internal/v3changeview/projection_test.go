package v3changeview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/delivery"
)

func nativeRun() delivery.Run {
	definition := delivery.Definition{Provider: "native", Nodes: []delivery.Node{
		{ID: "build", Name: "Build", Kind: "container", Image: "alpine:3.23", Command: []string{"true"}},
		{ID: "test", Name: "Test", Kind: "container", Image: "alpine:3.23", Command: []string{"true"}, DependsOn: []delivery.Dependency{{NodeID: "build"}}},
	}}
	b, _ := json.Marshal(definition)
	digest := sha256.Sum256(b)
	created := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	return delivery.Run{
		ID: "run-1", PipelineID: "pipeline-1", Version: 2, ApplicationID: "app-1", EnvironmentID: "prod",
		Provider: "native", Status: "succeeded", ExternalID: "aiops-run-1", CreatedBy: "user-1",
		Snapshot: delivery.Pipeline{ID: "pipeline-1", Version: 2, ApplicationID: "app-1", EnvironmentID: "prod", Name: "Production release", Digest: hex.EncodeToString(digest[:]), Definition: definition},
		Projection: delivery.Projection{Status: "succeeded", ExternalID: "aiops-run-1", ObservedAt: created.Add(8 * time.Minute), Nodes: []delivery.NodeResult{
			{ID: "pod-a", Name: "aiops-run-1.build", Status: "succeeded"},
			{ID: "pod-b", Name: "aiops-run-1.test", Status: "succeeded"},
		}},
		CreatedAt: created, UpdatedAt: created.Add(8 * time.Minute),
	}
}

func TestProjectNativeRunAndSuccessfulStages(t *testing.T) {
	run := nativeRun()
	p, err := projectRun(run, "Checkout API", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != run.ID || p.Brand != "argo" || p.Ref != "aiops-run-1" || p.Title != "Production release" || p.Service != "Checkout API" || p.Trigger != "Recorded by Alice" || p.Status != "success" || p.Duration != "recorded 8m" || p.Time != "2026-09-29T08:00:00Z" {
		t.Fatalf("incorrect source mapping: %+v", p)
	}
	if len(p.Stages) != 2 || p.Stages[0] != "ok" || p.Stages[1] != "ok" {
		t.Fatalf("incorrect stages: %+v", p.Stages)
	}
	stages, err := projectStages(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 || stages[0].Name != "Build" || len(stages[0].Jobs) != 1 || stages[0].Jobs[0].Name != "Build" || stages[0].Jobs[0].State != "ok" || stages[1].Jobs[0].Name != "Test" {
		t.Fatalf("incorrect detail stages: %+v", stages)
	}
}

func TestProjectRunStatesAndIncompleteEvidence(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*delivery.Run)
		want   string
		ok     bool
	}{
		{"failed", func(r *delivery.Run) {
			r.Status = "failed"
			r.Projection.Status = "failed"
			r.Projection.Nodes[1].Status = "failed"
		}, "danger", true},
		{"running", func(r *delivery.Run) {
			r.Status = "running"
			r.Projection.Status = "running"
			r.Projection.Nodes[1].Status = "running"
		}, "accent", true},
		{"canceled", func(r *delivery.Run) {
			r.Status = "cancelled"
			r.Projection.Status = "cancelled"
			r.Projection.Nodes = nil
		}, "neutral", true},
		{"status mismatch", func(r *delivery.Run) { r.Projection.Status = "failed" }, "", false},
		{"missing success node", func(r *delivery.Run) { r.Projection.Nodes = r.Projection.Nodes[:1] }, "", false},
		{"unknown node", func(r *delivery.Run) { r.Projection.Nodes[0].Status = "mystery" }, "", false},
		{"duplicate node", func(r *delivery.Run) { r.Projection.Nodes = append(r.Projection.Nodes, r.Projection.Nodes[0]) }, "", false},
		{"digest changed", func(r *delivery.Run) { r.Snapshot.Digest = "old" }, "", false},
		{"gitlab source", func(r *delivery.Run) { r.Provider = "gitlab" }, "", false},
		{"missing observation", func(r *delivery.Run) { r.Projection.ObservedAt = time.Time{} }, "", false},
		{"future observation", func(r *delivery.Run) { r.Projection.ObservedAt = r.UpdatedAt.Add(16 * time.Second) }, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := nativeRun()
			tc.modify(&run)
			p, err := projectRun(run, "Checkout API", "Alice")
			if (err == nil) != tc.ok {
				t.Fatalf("projectRun ok=%t err=%v", tc.ok, err)
			}
			if tc.ok && p.Stages[len(p.Stages)-1] != tc.want {
				t.Fatalf("got %+v", p.Stages)
			}
			if tc.name != "failed" && tc.name != "running" && tc.name != "canceled" {
				return
			}
			if _, err := projectStages(run); err == nil {
				t.Fatal("detail must reject non-success run")
			}
		})
	}
}

func TestProjectStagesRejectsSkippedSuccessNode(t *testing.T) {
	run := nativeRun()
	run.Projection.Nodes[1].Status = "skipped"
	p, err := projectRun(run, "Checkout API", "Alice")
	if err != nil || p.Stages[1] != "neutral" {
		t.Fatalf("list should show actual skipped stage: %+v, %v", p, err)
	}
	if _, err := projectStages(run); err == nil {
		t.Fatal("detail cannot represent skipped job")
	}
}
