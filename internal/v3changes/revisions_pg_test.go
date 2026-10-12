package v3changes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/core"
)

// PA-94 through the real reads: a reused ReplicaSet's time comes from the
// GitLab deployment its cause names (one bounded deployments read) or from
// the rollback ledger's applied step, and is bounded when neither says.
func TestReadRevisionsResolvesReusedReplicaSets(t *testing.T) {
	f := newChangesFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	const execution = "cd40b79d4b31b1c296f785379772602f"
	applied := now.Add(-10 * time.Minute).Truncate(time.Microsecond)
	// The ledger row's proposal is not part of this isolated schema.
	for _, statement := range []string{
		`ALTER TABLE v3_rollout_executions DROP CONSTRAINT v3_rollout_executions_proposal_id_fkey`,
	} {
		if _, err := f.db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	timeline, _ := json.Marshal([]map[string]any{{"state": "reserved", "at": applied.Add(-time.Second)}, {"state": "applied", "at": applied}})
	if _, err := f.db.Exec(ctx, `INSERT INTO v3_rollout_executions(proposal_id,execution_id,environment_id,application_id,
 action_digest,approval_digest,status,timeline,rollout)
 VALUES('P-1',$1,'dev','app',$2,$2,'succeeded',$3::jsonb,'{"current_revision":"24"}'::jsonb)`,
		execution, strings.Repeat("a", 64), timeline); err != nil {
		t.Fatal(err)
	}
	gitlabCause, rollbackCause, staleCause := "gitlab pipeline 11 1111111",
		"aiops rollback to revision 21 (execution "+execution+")", "gitlab pipeline 10 2222222"
	dayAgo := now.Add(-24 * time.Hour)
	f.service.Revisions = func(context.Context, core.User, string, string) ([]KubernetesRevision, error) {
		return []KubernetesRevision{
			{Revision: "21", CreatedAt: now.Add(-90 * time.Minute)},
			{Revision: "22", CreatedAt: dayAgo, ReplicaSetReused: true, ChangeCause: &gitlabCause},
			{Revision: "23", CreatedAt: now.Add(-20 * time.Minute)},
			{Revision: "24", CreatedAt: dayAgo, ReplicaSetReused: true, ChangeCause: &rollbackCause},
			// Pipeline 10's deployment ended 58 minutes ago, before
			// revisions 23 and 24 took effect: a stale cause, out of order.
			{Revision: "25", CreatedAt: dayAgo, ReplicaSetReused: true, ChangeCause: &staleCause},
		}, nil
	}
	revisions, err := f.service.ReadRevisions(ctx, f.viewer, "app", "dev")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]KubernetesRevision{}
	for _, r := range revisions {
		got[r.Revision] = r
	}
	if r := got["21"]; r.TimeBasis != TimeBasisReplicaSetCreated || r.At == nil || !r.At.Equal(r.CreatedAt) {
		t.Fatalf("new ReplicaSet: %+v", r)
	}
	// Deployment 503 of pipeline 11 started 30 minutes ago; its job finished
	// 29 minutes ago.
	if r := got["22"]; r.TimeBasis != TimeBasisGitLabDeployment || r.At == nil || r.At.Sub(now.Add(-29*time.Minute)).Abs() > time.Minute ||
		r.GitLabPipelineID == nil || *r.GitLabPipelineID != 11 {
		t.Fatalf("GitLab redeploy: %+v at=%v", r, r.At)
	}
	if r := got["24"]; r.TimeBasis != TimeBasisRollbackExecution || r.At == nil || !r.At.Equal(applied) {
		t.Fatalf("product rollback: %+v at=%v", r, r.At)
	}
	if r := got["25"]; r.TimeBasis != TimeBasisUnknown || r.At != nil || r.TimeUnknownReason == nil ||
		*r.TimeUnknownReason != "out_of_order" || r.NotBefore == nil || !r.NotBefore.Equal(applied) || r.NotAfter == nil {
		t.Fatalf("stale cause: %+v", r)
	}
	if q := f.gitlab.query("/api/v4/projects/42/deployments"); !strings.Contains(q, "environment=aiops-lab") ||
		!strings.Contains(q, "updated_after=") || !strings.Contains(q, "sort=asc") || !strings.Contains(q, "per_page=100") {
		t.Fatalf("deployments read: %q", q)
	}
}
