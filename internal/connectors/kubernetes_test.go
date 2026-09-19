package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, handler http.HandlerFunc) (*Kubernetes, RollbackPlan) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("test"), 0600)
	k := &Kubernetes{config: Config{APIURL: server.URL, Namespace: "test", Deployment: "app", Service: "app", Port: 8080, TokenFile: token}, client: server.Client()}
	raw := json.RawMessage(`{"replicas":1,"template":{"metadata":{"labels":{"app":"app"}},"spec":{"containers":[{"name":"app","image":"bad"}]}}}`)
	var d Deployment
	json.Unmarshal([]byte(`{"metadata":{"uid":"uid","resourceVersion":"8"},"spec":`+string(raw)+`}`), &d)
	target := json.RawMessage(`{"metadata":{"labels":{"app":"app"}},"spec":{"containers":[{"name":"app","image":"good"}]}}`)
	p := RollbackPlan{Namespace: "test", Deployment: "app", UID: "uid", ExpectedTemplateDigest: templateDigest(d.Spec.Template), ExpectedSpecDigest: templateDigest(raw), TargetTemplate: target, TargetTemplateDigest: templateDigest(target), TargetRevision: "1", ObservationSeconds: 180}
	p.Digest = planDigest(p)
	return k, p
}

const badDeployment = `{"metadata":{"name":"app","uid":"uid","resourceVersion":"8"},"spec":{"replicas":1,"template":{"metadata":{"labels":{"app":"app"}},"spec":{"containers":[{"name":"app","image":"bad"}]}}}}`

func TestRejectDriftBeforeWrite(t *testing.T) {
	patches := 0
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			patches++
		}
		var d map[string]any
		json.Unmarshal([]byte(badDeployment), &d)
		d["spec"].(map[string]any)["replicas"] = 2
		json.NewEncoder(w).Encode(d)
	})
	_, e := k.Execute(context.Background(), p, "op")
	if !errors.Is(e, ErrDrift) || patches != 0 {
		t.Fatalf("err=%v writes=%d", e, patches)
	}
}
func TestTamperedPlanRejectedWithoutRequest(t *testing.T) {
	requests := 0
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) { requests++ })
	p.TargetRevision = "9"
	_, e := k.Execute(context.Background(), p, "op")
	if e == nil || requests != 0 {
		t.Fatalf("err=%v requests=%d", e, requests)
	}
}
func TestAmbiguousWriteNotRetriedAndReconcileReadOnly(t *testing.T) {
	writes := 0
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			writes++
			w.WriteHeader(504)
			return
		}
		w.Write([]byte(badDeployment))
	})
	_, e := k.Execute(context.Background(), p, "op")
	if !errors.Is(e, ErrAmbiguous) || writes != 1 {
		t.Fatalf("err=%v writes=%d", e, writes)
	}
	r, e := k.Reconcile(context.Background(), p, "op")
	if e != nil || r.State != "needs_review" || writes != 1 {
		t.Fatalf("reconcile=%+v err=%v writes=%d", r, e, writes)
	}
}
func TestCASBindsResourceVersionAndTarget(t *testing.T) {
	var patches []map[string]json.RawMessage
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PATCH" {
			json.NewDecoder(r.Body).Decode(&patches)
			w.WriteHeader(409)
			return
		}
		w.Write([]byte(badDeployment))
	})
	_, e := k.Execute(context.Background(), p, "op")
	if !errors.Is(e, ErrDrift) || len(patches) != 5 {
		t.Fatalf("err=%v patch=%v", e, patches)
	}
	if string(patches[1]["path"]) != `"/metadata/resourceVersion"` || string(patches[1]["value"]) != `"8"` {
		t.Fatalf("missing CAS: %v", patches)
	}
}
func TestReconcileAppliedRequiresBothMarkerAndTemplate(t *testing.T) {
	var current Deployment
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(current) })
	current.Metadata.UID = p.UID
	current.Metadata.Annotations = map[string]string{operationAnnotation: "op"}
	current.Spec.Template = p.TargetTemplate
	r, e := k.Reconcile(context.Background(), p, "op")
	if e != nil || r.State != "applied" {
		t.Fatalf("%+v %v", r, e)
	}
	current.Spec.Template = json.RawMessage(`{}`)
	r, e = k.Reconcile(context.Background(), p, "op")
	if e != nil || r.State != "needs_review" {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestRecoveryRequiresTrafficAndStableCounterIdentity(t *testing.T) {
	base := Metrics{Requests: 10, Errors: 1, Instance: "pod-a"}
	for _, bad := range []Metrics{{10, 1, "pod-a"}, {11, 2, "pod-a"}, {11, 1, "pod-b"}, {1, 0, "pod-a"}} {
		if validateWindow(base, bad) == nil {
			t.Fatalf("accepted invalid window %+v", bad)
		}
	}
	if e := validateWindow(base, Metrics{15, 1, "pod-a"}); e != nil {
		t.Fatal(e)
	}
}
func TestCleanReplicaSetTemplate(t *testing.T) {
	out, e := cleanTemplate(json.RawMessage(`{"metadata":{"labels":{"app":"app","pod-template-hash":"hash"}},"spec":{}}`))
	if e != nil {
		t.Fatal(e)
	}
	if templateDigest(out) != templateDigest(json.RawMessage(`{"spec":{},"metadata":{"labels":{"app":"app"}}}`)) {
		t.Fatalf("unexpected %s", out)
	}
}

func TestPlanSurvivesJSONBKeyReordering(t *testing.T) {
	k, p := fixture(t, func(w http.ResponseWriter, r *http.Request) {})
	b, _ := json.Marshal(p)
	var v any
	json.Unmarshal(b, &v)
	b, _ = json.Marshal(v)
	var reordered RollbackPlan
	json.Unmarshal(b, &reordered)
	if e := k.validate(reordered); e != nil {
		t.Fatalf("canonical plan rejected: %v", e)
	}
}
