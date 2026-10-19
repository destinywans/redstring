package delivery

import (
	"context"
	"github.com/destinywans/redstring/internal/store"
	"testing"
	"time"
)

func TestLiveConditionalBranchAndSelectedJoin(t *testing.T) {
	conn, _ := liveConfig(t)
	token, err := readCredential(conn["workflows_token_file"])
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewArgoClient(argoConfig(conn["workflows_url"]), token)
	if err != nil {
		t.Fatal(err)
	}
	source, left, right, join := buildNode("source"), buildNode("left"), buildNode("right"), buildNode("join")
	source.Args = []string{"printf left > /workspace/route"}
	source.Outputs = []Output{{Name: "route", Path: "/workspace/route"}}
	left.DependsOn = []Dependency{{NodeID: "source"}}
	right.DependsOn = left.DependsOn
	left.When = &Condition{FromNode: "source", Output: "route", Operator: "eq", Value: "left"}
	right.When = &Condition{FromNode: "source", Output: "route", Operator: "eq", Value: "right"}
	join.Join = "selected"
	join.DependsOn = []Dependency{{NodeID: "left"}, {NodeID: "right"}}
	for _, bad := range []bool{false, true} {
		if bad {
			left.Args = []string{"exit 1"}
		}
		run := testRun(source, left, right, join)
		run.ID = "live-" + store.ID()
		for i := range run.Snapshot.Definition.Nodes {
			run.Snapshot.Definition.Nodes[i].Image = "alpine:3.23.6"
		}
		run.Snapshot.Digest = hash(run.Snapshot.Definition)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		p, err := client.Submit(ctx, run)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		run.Projection = p
		for !terminal(p.Status) {
			select {
			case <-ctx.Done():
				cancel()
				t.Fatal("branch workflow deadline")
			case <-time.After(3 * time.Second):
			}
			next, e := client.Observe(ctx, run)
			if e != nil {
				continue
			}
			p = next
		}
		status := map[string]string{}
		for _, n := range p.Nodes {
			status[n.Name] = n.Status
		}
		if !bad && (p.Status != "succeeded" || status["left"] != "succeeded" || status["right"] != "skipped" || status["join"] != "succeeded") {
			cancel()
			t.Fatalf("wrong positive branch projection: %+v", status)
		}
		if bad && (p.Status != "failed" || status["join"] == "succeeded" || status["join"] == "running") {
			cancel()
			t.Fatalf("failed selected branch released join: %+v", status)
		}
		t.Logf("conditional_branch workflow=%s failure_case=%t statuses=%v", p.ExternalID, bad, status)
		cancel()
	}
}
