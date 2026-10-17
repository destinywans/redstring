package v3rollouts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wans/aiops-canvas/internal/v3kube"
)

// PA-172: a candidate says when its revision took effect. A reused
// ReplicaSet's creation is not that time: the change service's time wins,
// and without it the time is unknown rather than the old creation.
func TestRollbackCandidatesSayWhenRevisionsTookEffect(t *testing.T) {
	created := time.Date(2026, 10, 11, 12, 37, 30, 0, time.UTC)
	newer := time.Date(2026, 10, 16, 7, 57, 0, 0, time.UTC)
	d := v3kube.Deployment{}
	d.Spec.Template = json.RawMessage(`{"spec":{"containers":[{"name":"api","image":"api:87"}]}}`)
	revisions := []v3kube.Revision{
		{Revision: "87", CreatedAt: &created, History: []string{"70", "75"}, Template: d.Spec.Template, Digest: v3kube.Digest(d.Spec.Template)},
		{Revision: "86", CreatedAt: &newer, Template: json.RawMessage(`{"spec":{"containers":[{"name":"api","image":"api:86"}]}}`), Digest: "d86"},
	}
	byNumber := func(list []candidateJSON) map[string]candidateJSON {
		out := map[string]candidateJSON{}
		for _, c := range list {
			out[c.Revision] = c
		}
		return out
	}

	plain := byNumber(candidateList(d, revisions, nil))
	if c := plain["87"]; c.At != nil || c.TimeBasis != "unknown" || c.TimeUnknownReason == nil || *c.TimeUnknownReason != "replicaset_reused" || c.CreatedAt == nil {
		t.Fatalf("reused without times: %+v", c)
	}
	if c := plain["86"]; c.At == nil || *c.At != "2026-10-16T07:57:00Z" || c.TimeBasis != "replicaset_created" {
		t.Fatalf("new ReplicaSet: %+v", c)
	}

	applied := time.Date(2026, 10, 17, 3, 12, 0, 0, time.UTC)
	timed := byNumber(candidateList(d, revisions, map[string]RevisionTime{"87": {At: &applied, TimeBasis: "rollback_execution"}}))
	if c := timed["87"]; c.At == nil || *c.At != "2026-10-17T03:12:00Z" || c.TimeBasis != "rollback_execution" || c.TimeUnknownReason != nil {
		t.Fatalf("reused with the change service's time: %+v", c)
	}
}
