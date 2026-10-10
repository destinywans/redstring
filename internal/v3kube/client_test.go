package v3kube

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/wans/aiops-canvas/internal/v3kube/kubetest"
)

func fakeCluster(t *testing.T) (*kubetest.Server, *Client) {
	t.Helper()
	server := kubetest.New("aiops-lab", "token")
	t.Cleanup(server.Close)
	good := kubetest.Template("checkout-api", "api:1.4.2", nil)
	bad := kubetest.Template("checkout-api", "api:1.4.2", map[string]string{"LAB_FAULTS": `{"http_error_ratio":0.5}`})
	server.AddDeployment("checkout-api", 2, []string{"release 1.4.2", "lab drill bad-release"}, good, bad)
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
	client, err := New(server.URL, "token", caPEM)
	if err != nil {
		t.Fatal(err)
	}
	return server, client
}

// PA-08: list owned revisions with their differences, then make one
// conditional write that is idempotent, refuses drift and can be reconciled
// after an ambiguous answer.
func TestRollbackAgainstFakeCluster(t *testing.T) {
	ctx := context.Background()
	server, client := fakeCluster(t)
	d, err := client.GetDeployment(ctx, "aiops-lab", "checkout-api")
	if err != nil || d.Revision() != "2" || d.Desired() != 2 || !d.RolledOut() {
		t.Fatalf("deployment: %+v %v", d.Metadata, err)
	}
	revisions, err := client.Revisions(ctx, d)
	if err != nil || len(revisions) != 2 || revisions[0].Revision != "2" || revisions[1].Revision != "1" ||
		revisions[0].Digest != Digest(d.Spec.Template) || revisions[1].ChangeCause != "release 1.4.2" {
		t.Fatalf("revisions: %+v %v", revisions, err)
	}
	changes := Changes(d.Spec.Template, revisions[1].Template)
	if len(changes) != 1 || changes[0].Field != "env" || *changes[0].Name != "LAB_FAULTS" || changes[0].From == nil || changes[0].To != nil {
		raw, _ := json.Marshal(changes)
		t.Fatalf("changes: %s", raw)
	}
	r := Rollback{Namespace: "aiops-lab", Deployment: "checkout-api", UID: d.Metadata.UID,
		ExpectedTemplateDigest: Digest(d.Spec.Template), ExpectedSpecDigest: SpecDigest(d.RawSpec),
		TargetTemplate: revisions[1].Template, TargetTemplateDigest: revisions[1].Digest, OperationID: "exec-1"}
	o, err := client.Apply(ctx, r)
	if err != nil || !o.Applied || server.Patches != 1 {
		t.Fatalf("apply: %+v %v", o, err)
	}
	if revision, _ := server.Revision("checkout-api"); revision != "3" || server.Annotation("checkout-api", OperationAnnotation) != "exec-1" {
		t.Fatalf("rollback did not reuse the old template as revision 3: %s", revision)
	}
	if o, err = client.Apply(ctx, r); err != nil || !o.Applied || server.Patches != 1 {
		t.Fatalf("replayed apply wrote again: %+v %v patches=%d", o, err, server.Patches)
	}
	// Someone else changes the Deployment after the rollback.
	server.Touch("checkout-api", kubetest.Template("checkout-api", "api:1.5.0", nil))
	if o, err = client.Reconcile(ctx, r); err != nil || o.Applied || !o.Drift {
		t.Fatalf("change after the write is not drift: %+v %v", o, err)
	}

	// A stale plan never writes.
	_, fresh := fakeCluster(t)
	d, _ = fresh.GetDeployment(ctx, "aiops-lab", "checkout-api")
	stale := r
	stale.UID = d.Metadata.UID
	stale.ExpectedTemplateDigest = "0000"
	if o, err = fresh.Apply(ctx, stale); err != nil || !o.Drift {
		t.Fatalf("stale plan: %+v %v", o, err)
	}

	// An applied write answered with 500 is ambiguous and reconciled by reading.
	ambiguousServer, ambiguous := fakeCluster(t)
	d, _ = ambiguous.GetDeployment(ctx, "aiops-lab", "checkout-api")
	revisions, _ = ambiguous.Revisions(ctx, d)
	r.UID, r.ExpectedTemplateDigest, r.ExpectedSpecDigest = d.Metadata.UID, Digest(d.Spec.Template), SpecDigest(d.RawSpec)
	ambiguousServer.PatchAppliesThenFails = true
	if _, err = ambiguous.Apply(ctx, r); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ambiguous write: %v", err)
	}
	if o, err = ambiguous.Reconcile(ctx, r); err != nil || !o.Applied {
		t.Fatalf("reconcile after ambiguous write: %+v %v", o, err)
	}

	forbiddenServer, forbidden := fakeCluster(t)
	forbiddenServer.PatchStatus = 403
	d, _ = forbidden.GetDeployment(ctx, "aiops-lab", "checkout-api")
	r.UID, r.ExpectedTemplateDigest, r.ExpectedSpecDigest = d.Metadata.UID, Digest(d.Spec.Template), SpecDigest(d.RawSpec)
	if _, err = forbidden.Apply(ctx, r); !errors.Is(err, ErrForbidden) {
		t.Fatalf("forbidden write: %v", err)
	}
	if _, err = New("http://cluster.example:6443", "token", ""); err == nil {
		t.Fatal("plain HTTP to a remote API accepted")
	}
}
