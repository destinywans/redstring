package rbac

import "testing"

func TestPermissionCreationCatalog(t *testing.T) {
	want := []string{"view", "update", "approve", "delete"}
	if len(Actions) != len(want) {
		t.Fatal(Actions)
	}
	for i, action := range Actions {
		if action["id"] != want[i] || action["scope_type"] != "" {
			t.Fatal(action)
		}
	}
}

func binding(scope, id string, actions ...string) ResourceBinding {
	return ResourceBinding{ResourceTarget: ResourceTarget{ScopeType: scope, ResourceID: id}, Actions: actions}
}
