package inventory

import (
	"testing"
	"time"
)

func fixture(kind, name, ns, id, parent string) object {
	metadata := map[string]any{"name": name, "namespace": ns, "uid": id}
	if parent != "" {
		metadata["ownerReferences"] = []any{map[string]any{"uid": parent, "controller": true}}
	}
	return object{"kind": kind, "metadata": metadata, "spec": map[string]any{}, "status": map[string]any{}}
}
func TestStableSourceIdentityAndRuntimeOwnership(t *testing.T) {
	source := Source{ID: "lab", Name: "真实实验集群", EnvironmentKey: "lab"}
	objects := []object{fixture("Namespace", "kube-system", "", "system", ""), fixture("Namespace", "app", "", "ns", ""), fixture("Node", "worker", "", "node", ""), fixture("Deployment", "api", "app", "deployment", ""), fixture("ReplicaSet", "api-abc", "app", "rs", "deployment"), fixture("Pod", "api-abc-1", "app", "pod", "rs"), fixture("Pod", "static", "kube-system", "mirror", "node"), fixture("Pod", "standalone", "app", "bare", "")}
	snapshot, err := convert(source, objects, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"pod": "deployment", "mirror": "node", "bare": "ns"}
	got := map[string]string{}
	for _, edge := range snapshot.Relations {
		if _, ok := want[edge.SourceExternalID]; ok && (edge.Kind == "runtime_owner" || (edge.Kind == "belongs_to" && edge.TargetExternalID == "deployment")) {
			if _, exists := got[edge.SourceExternalID]; exists {
				t.Fatal("multiple runtime authorization parents")
			}
			got[edge.SourceExternalID] = edge.TargetExternalID
		}
	}
	for child, parent := range want {
		if got[child] != parent {
			t.Fatalf("owner %s=%s, want %s", child, got[child], parent)
		}
	}
	if ResourceID("kubernetes", "lab", "pod") == ResourceID("kubernetes", "other", "pod") {
		t.Fatal("source identity collision")
	}
	if ResourceID("kubernetes", "lab", "pod") != ResourceID("kubernetes", "lab", "pod") {
		t.Fatal("unstable identity")
	}
}
func TestUnknownControllerDoesNotInheritNamespace(t *testing.T) {
	source := Source{ID: "lab", Name: "lab"}
	snapshot, err := convert(source, []object{fixture("Namespace", "kube-system", "", "system", ""), fixture("Namespace", "app", "", "ns", ""), fixture("Pod", "api", "app", "pod", "missing-controller")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range snapshot.Relations {
		if edge.SourceExternalID == "pod" && edge.Kind == "runtime_owner" {
			t.Fatal("unresolved controller must fail closed")
		}
	}
}
