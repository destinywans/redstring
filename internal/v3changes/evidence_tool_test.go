package v3changes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3catalog"
)

func (f *changesFixture) tool(t *testing.T) inference.EvidenceTool {
	t.Helper()
	tool, ok := f.service.EvidenceTool(ChangesToolOptions{OwnerID: f.viewer.ID, EnvironmentID: "dev",
		CurrentUser: func(context.Context, string) (core.User, error) { return f.viewer, nil },
		Lookup: func(_ context.Context, user core.User, env, key string) (v3catalog.ServiceEvidence, error) {
			if key != "checkout" || env != "dev" || user.ID != f.viewer.ID {
				return v3catalog.ServiceEvidence{}, store.ErrNotFound
			}
			return v3catalog.ServiceEvidence{ID: f.serviceID, Key: "checkout", Environment: "dev"}, nil
		}})
	if !ok || tool.Name != ChangesToolName {
		t.Fatal("read_service_changes not offered")
	}
	return tool
}

func TestReadServiceChangesTool(t *testing.T) {
	f := newChangesFixture(t)
	ctx := t.Context()
	cause := "gitlab pipeline 11 1111111"
	f.service.Revisions = func(context.Context, core.User, string, string) ([]KubernetesRevision, error) {
		return []KubernetesRevision{
			{Revision: "22", CreatedAt: time.Now().Add(-29 * time.Minute), ChangeCause: &cause},
			{Revision: "23", CreatedAt: time.Now().Add(-5 * time.Minute)},
			// PA-94: 24 reused a ReplicaSet created long ago, without a
			// cause: after 23, so listed by its bounds, with no time.
			{Revision: "24", CreatedAt: time.Now().Add(-40 * time.Hour), ReplicaSetReused: true},
			{Revision: "9", CreatedAt: time.Now().Add(-30 * time.Hour)}}, nil
	}
	tool := f.tool(t)
	evidence, err := tool.Read(ctx, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	var got changesEvidence
	if err := json.Unmarshal([]byte(evidence.Content), &got); err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != "changes" || evidence.Source != "gitlab" || evidence.TargetID != f.serviceID ||
		got.Coverage["gitlab"] != "complete" || got.Coverage["kubernetes"] != "history_limited" || got.Project != "aiops-lab/checkout-api" ||
		len(got.Unavailable) != 0 || got.Truncated {
		t.Fatalf("evidence: %+v %s", evidence, evidence.Content)
	}
	// The deployed commit came from MR !4 and made revision 22.
	if len(got.Deployments) != 2 || got.Deployments[0].IID != 3 || got.Deployments[0].MergeRequest == nil ||
		got.Deployments[0].MergeRequest.IID != 4 || got.Deployments[0].KubernetesRevision == nil ||
		*got.Deployments[0].KubernetesRevision != "22" || got.Deployments[1].KubernetesRevision != nil {
		t.Fatalf("deployments: %+v", got.Deployments)
	}
	// Newest first by number. Revision 24 has no known time; 23 was made by
	// something else; revision 9 is outside the default two hours.
	if len(got.KubernetesRevisions) != 3 || got.KubernetesRevisions[1].Revision != "23" ||
		got.KubernetesRevisions[1].GitLabPipelineID != nil || got.KubernetesRevisions[2].GitLabPipelineID == nil ||
		got.KubernetesRevisions[1].At == nil || got.KubernetesRevisions[1].TimeBasis != TimeBasisReplicaSetCreated {
		t.Fatalf("revisions: %+v", got.KubernetesRevisions)
	}
	if r := got.KubernetesRevisions[0]; r.Revision != "24" || r.At != nil || r.TimeBasis != TimeBasisUnknown || !r.ReplicaSetReused ||
		r.TimeUnknownReason == nil || *r.TimeUnknownReason != "replicaset_reused" || r.NotBefore == nil ||
		*r.NotBefore != got.KubernetesRevisions[1].CreatedAt || r.NotAfter == nil {
		t.Fatalf("untimed revision: %+v", r)
	}
	if !strings.Contains(tool.Description, "created_at is not when the revision took effect") || !strings.Contains(tool.Description, "not_before") {
		t.Fatalf("tool description: %s", tool.Description)
	}
	if len(got.MergedMergeRequests) != 1 || got.MergedMergeRequests[0].IID != 4 || len(got.FailedPipelines) != 2 {
		t.Fatalf("merge requests and pipelines: %+v %+v", got.MergedMergeRequests, got.FailedPipelines)
	}
	// PA-95: a failed pipeline names its merge request as the change list
	// does: !5 from its ref, and from the branch "broken" it was pushed to.
	for _, p := range got.FailedPipelines {
		if p.MergeRequestIID == nil || *p.MergeRequestIID != 5 {
			t.Fatalf("failed pipeline %d (%s) merge request: %v", p.PipelineID, p.Ref, p.MergeRequestIID)
		}
	}
	if strings.Contains(evidence.Content, "hidden@example.invalid") || strings.Contains(evidence.Content, "secret") ||
		strings.Contains(evidence.Content, "Maintainer A") {
		t.Fatalf("evidence carries user details or commit text: %s", evidence.Content)
	}
	// A window in JSON; at most 24 hours; unknown keys and fields refused.
	now := time.Now().UTC()
	window := `{"service":"checkout","since":"` + now.Add(-26*time.Hour).Format(time.RFC3339) + `","until":"` + now.Add(-25*time.Hour).Format(time.RFC3339) + `"}`
	if evidence, err = tool.Read(ctx, window); err != nil || !strings.Contains(evidence.Content, `"deployments":[]`) {
		t.Fatalf("earlier window: %v %s", err, evidence.Content)
	}
	for _, input := range []string{
		`{"service":"checkout","since":"` + now.Add(-25*time.Hour).Format(time.RFC3339) + `"}`,
		`{"service":"checkout","project":"43"}`, `{"service":"checkout","since":"yesterday"}`, ``,
	} {
		if _, err := tool.Read(ctx, input); inference.ToolErrorCode(err) != inference.ToolInvalidInput {
			t.Fatalf("%q: %v", input, err)
		}
	}
	if _, err := tool.Read(ctx, "orders"); inference.ToolErrorCode(err) != inference.ToolUnknownService {
		t.Fatalf("unknown service: %v", err)
	}
	// GitLab down: the revisions still answer and the coverage says so.
	f.gitlab.set("/api/v4/projects/42/pipelines", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if evidence, err = tool.Read(ctx, "checkout"); err != nil || !strings.Contains(evidence.Content, `"gitlab":"unavailable"`) ||
		!strings.Contains(evidence.Content, `"revision":"23"`) {
		t.Fatalf("GitLab unavailable: %v %s", err, evidence.Content)
	}
	f.gitlab.set("/api/v4/projects/42/pipelines", nil)
	// list_services: configured, not_ready without read tools, then gone.
	if state, err := f.service.SourceState(ctx, "app", "dev"); err != nil || state != "configured" {
		t.Fatalf("source state: %s %v", state, err)
	}
	if _, err := f.db.Exec(ctx, `UPDATE v3_connectors SET tools='[]' WHERE id='gitlab-dev'`); err != nil {
		t.Fatal(err)
	}
	if state, err := f.service.SourceState(ctx, "app", "dev"); err != nil || state != "not_ready" {
		t.Fatalf("source state without tools: %s %v", state, err)
	}
	// No GitLab source and no workload: not_configured.
	if _, err := f.db.Exec(ctx, `DELETE FROM v3_service_code_projects`); err != nil {
		t.Fatal(err)
	}
	f.service.Revisions = nil
	if _, err := tool.Read(ctx, "checkout"); inference.ToolErrorCode(err) != inference.ToolNotConfigured {
		t.Fatalf("nothing configured: %v", err)
	}
	if state, err := f.service.SourceState(ctx, "app", "dev"); err != nil || state != "not_configured" {
		t.Fatalf("source state without project: %s %v", state, err)
	}
}
