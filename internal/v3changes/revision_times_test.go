package v3changes

import (
	"testing"
	"time"
)

// PA-94: when a revision took effect. The prod case: revision 70 reused the
// ReplicaSet created a day earlier, made fresh by revision 69's release.
func TestAssignRevisionTimes(t *testing.T) {
	now := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 12, h, m, 0, 0, time.UTC) }
	dayBefore := time.Date(2026, 10, 11, 12, 37, 30, 0, time.UTC)
	cause := func(text string) *string { return &text }
	pipeline := func(id int64) *int64 { return &id }
	const sha = "17017e16aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const execution = "cd40b79d4b31b1c296f785379772602f"
	deployment := func(pipelineID int64, created, finished time.Time) Deployment {
		return Deployment{ID: pipelineID * 10, PipelineID: &pipelineID, SHA: sha, Status: "success", CreatedAt: created, FinishedAt: &finished}
	}
	type want struct {
		basis, reason string
		at            *time.Time
		notBefore     *time.Time
		notAfter      *time.Time
	}
	ptr := func(t time.Time) *time.Time { return &t }
	for _, c := range []struct {
		name        string
		revisions   []KubernetesRevision
		applied     map[string]time.Time
		ledgerRead  bool
		deployments []Deployment
		gitlab      string
		want        map[string]want
	}{
		{name: "new ReplicaSets", revisions: []KubernetesRevision{
			{Revision: "68", CreatedAt: at(1, 0)},
			{Revision: "69", CreatedAt: at(2, 30)},
		}, ledgerRead: true, want: map[string]want{
			"68": {basis: TimeBasisReplicaSetCreated, at: ptr(at(1, 0))},
			"69": {basis: TimeBasisReplicaSetCreated, at: ptr(at(2, 30))},
		}},
		{name: "product rollback reused a ReplicaSet", revisions: []KubernetesRevision{
			{Revision: "65", CreatedAt: at(2, 0)},
			{Revision: "66", CreatedAt: dayBefore, ReplicaSetReused: true,
				ChangeCause: cause("aiops rollback to revision 64 (execution " + execution + ")")},
		}, ledgerRead: true, applied: map[string]time.Time{execution + "\x0066": at(2, 20)}, want: map[string]want{
			"66": {basis: TimeBasisRollbackExecution, at: ptr(at(2, 20))},
		}},
		{name: "rollback the ledger does not show", revisions: []KubernetesRevision{
			{Revision: "65", CreatedAt: at(2, 0)},
			{Revision: "66", CreatedAt: dayBefore, ReplicaSetReused: true,
				ChangeCause: cause("aiops rollback to revision 64 (execution " + execution + ")")},
		}, ledgerRead: true, applied: map[string]time.Time{execution + "\x0064": at(2, 20)}, want: map[string]want{
			"66": {basis: TimeBasisUnknown, reason: "rollback_execution_not_found", notBefore: ptr(at(2, 0)), notAfter: ptr(now)},
		}},
		{name: "ledger unreadable", revisions: []KubernetesRevision{
			{Revision: "66", CreatedAt: dayBefore, ReplicaSetReused: true,
				ChangeCause: cause("aiops rollback to revision 64 (execution " + execution + ")")},
		}, want: map[string]want{
			"66": {basis: TimeBasisUnknown, reason: "rollback_ledger_unavailable", notBefore: ptr(dayBefore), notAfter: ptr(now)},
		}},
		{name: "GitLab redeploy reused a ReplicaSet", revisions: []KubernetesRevision{
			{Revision: "69", CreatedAt: at(1, 35)},
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 59 17017e16"), GitLabPipelineID: pipeline(59)},
		}, ledgerRead: true, deployments: []Deployment{deployment(59, at(2, 15), at(2, 18))}, want: map[string]want{
			"70": {basis: TimeBasisGitLabDeployment, at: ptr(at(2, 18))},
		}},
		{name: "cause from the deployment that created the ReplicaSet", revisions: []KubernetesRevision{
			{Revision: "69", CreatedAt: at(1, 35)},
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 40 17017e16"), GitLabPipelineID: pipeline(40)},
		}, ledgerRead: true, deployments: []Deployment{deployment(40, dayBefore.Add(-time.Minute), dayBefore.Add(2*time.Minute))}, want: map[string]want{
			"70": {basis: TimeBasisUnknown, reason: "change_cause_stale", notBefore: ptr(at(1, 35)), notAfter: ptr(now)},
		}},
		{name: "cause copied from the bad release", revisions: []KubernetesRevision{
			{Revision: "69", CreatedAt: at(1, 35), ChangeCause: cause("gitlab pipeline 58 17017e16"), GitLabPipelineID: pipeline(58)},
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 58 17017e16"), GitLabPipelineID: pipeline(58)},
		}, ledgerRead: true, deployments: []Deployment{deployment(58, at(1, 34), at(1, 37))}, want: map[string]want{
			"69": {basis: TimeBasisReplicaSetCreated, at: ptr(at(1, 35))},
			"70": {basis: TimeBasisUnknown, reason: "change_cause_stale", notBefore: ptr(at(1, 35)), notAfter: ptr(now)},
		}},
		{name: "no cause: bounded by its neighbours", revisions: []KubernetesRevision{
			{Revision: "69", CreatedAt: at(1, 35)},
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true},
			{Revision: "71", CreatedAt: at(2, 50)},
		}, ledgerRead: true, want: map[string]want{
			"70": {basis: TimeBasisUnknown, reason: "replicaset_reused", notBefore: ptr(at(1, 35)), notAfter: ptr(at(2, 50))},
		}},
		{name: "GitLab unreadable", revisions: []KubernetesRevision{
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 59 17017e16"), GitLabPipelineID: pipeline(59)},
		}, ledgerRead: true, gitlab: "gitlab_unavailable", want: map[string]want{
			"70": {basis: TimeBasisUnknown, reason: "gitlab_unavailable", notBefore: ptr(dayBefore), notAfter: ptr(now)},
		}},
		{name: "deployment not found or still running", revisions: []KubernetesRevision{
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 59 17017e16"), GitLabPipelineID: pipeline(59)},
			{Revision: "71", CreatedAt: dayBefore.Add(time.Hour), ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 60 17017e16"), GitLabPipelineID: pipeline(60)},
		}, ledgerRead: true, deployments: []Deployment{{ID: 600, PipelineID: pipeline(60), SHA: sha, Status: "running", CreatedAt: at(2, 40)}},
			want: map[string]want{
				"70": {basis: TimeBasisUnknown, reason: "gitlab_deployment_not_found", notBefore: ptr(dayBefore), notAfter: ptr(now)},
				"71": {basis: TimeBasisUnknown, reason: "gitlab_deployment_unfinished", notBefore: ptr(dayBefore.Add(time.Hour)), notAfter: ptr(now)},
			}},
		{name: "a time out of revision order", revisions: []KubernetesRevision{
			{Revision: "70", CreatedAt: dayBefore, ReplicaSetReused: true, ChangeCause: cause("gitlab pipeline 59 17017e16"), GitLabPipelineID: pipeline(59)},
			{Revision: "71", CreatedAt: at(1, 0)},
		}, ledgerRead: true, deployments: []Deployment{deployment(59, at(2, 0), at(2, 5))}, want: map[string]want{
			"70": {basis: TimeBasisUnknown, reason: "out_of_order", notBefore: ptr(dayBefore), notAfter: ptr(at(1, 0))},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			assignRevisionTimes(c.revisions, c.applied, c.ledgerRead, c.deployments, c.gitlab, now)
			for _, r := range c.revisions {
				w, ok := c.want[r.Revision]
				if !ok {
					continue
				}
				reason := ""
				if r.TimeUnknownReason != nil {
					reason = *r.TimeUnknownReason
				}
				if r.TimeBasis != w.basis || reason != w.reason || !sameTime(r.At, w.at) || !sameTime(r.NotBefore, w.notBefore) ||
					!sameTime(r.NotAfter, w.notAfter) {
					t.Fatalf("revision %s: basis=%s reason=%q at=%v bounds=%v..%v; want %+v", r.Revision, r.TimeBasis, reason,
						r.At, r.NotBefore, r.NotAfter, w)
				}
			}
		})
	}
}

func sameTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
}

func TestRevisionInWindow(t *testing.T) {
	start, end := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 2, 0, 0, 0, time.UTC)
	ptr := func(t time.Time) *time.Time { return &t }
	for name, c := range map[string]struct {
		r    KubernetesRevision
		want bool
	}{
		"known inside":           {KubernetesRevision{At: ptr(start.Add(time.Minute))}, true},
		"known before":           {KubernetesRevision{At: ptr(start.Add(-time.Minute))}, false},
		"unknown overlapping":    {KubernetesRevision{NotBefore: ptr(start.Add(-time.Hour)), NotAfter: ptr(start.Add(time.Minute))}, true},
		"unknown after":          {KubernetesRevision{NotBefore: ptr(end.Add(time.Minute)), NotAfter: ptr(end.Add(time.Hour))}, false},
		"unknown before":         {KubernetesRevision{NotBefore: ptr(start.Add(-2 * time.Hour)), NotAfter: ptr(start.Add(-time.Hour))}, false},
		"unknown without bounds": {KubernetesRevision{}, false},
	} {
		if got := c.r.InWindow(start, end); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
