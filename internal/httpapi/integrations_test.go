package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/store"
)

// Give both visible and hidden applications a concrete infrastructure target,
// then authorize only the selected viewer/resource pair. Legacy app grants
// alone intentionally do not provide the current resource-based authority.
func applicationACLFixtureResource(t *testing.T, repository *store.Store, app, environment, viewer string, grantView bool) {
	t.Helper()
	ctx := t.Context()
	resource := "fixture-host-" + environment
	if _, err := repository.DB.Exec(ctx, `INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES($1,'host','Application ACL fixture','manual')`, resource); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DB.Exec(ctx, `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES($1,$2,$3)`, resource, app, environment); err != nil {
		t.Fatal(err)
	}
	if !grantView {
		return
	}
	permission := "fixture-view-" + environment
	role := "fixture-role-" + environment
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO rbac_roles(id,name) VALUES($1,$1)`, []any{role}},
		{`INSERT INTO rbac_permissions(id,name) VALUES($1,$1)`, []any{permission}},
		{`INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,'resource',$2,'view')`, []any{permission, resource}},
		{`INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES($1,$2)`, []any{role, permission}},
		{`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, []any{viewer, role}},
	} {
		if _, err := repository.DB.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationBindingsRespectApplicationACL(t *testing.T) {
	api, server := testServer(t)
	ctx := context.Background()
	repository := api.Store.(*store.Store)
	viewer, err := repository.CreateMember(ctx, store.MemberCreate{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer", Password: "isolated-test-password"})
	if err != nil {
		t.Fatal(err)
	}
	metricBinding := core.Binding{Kind: "prometheus", ConnectorID: "prometheus", Resource: "lab-order-api"}
	kubeBinding := core.Binding{Kind: "kubernetes", ConnectorID: "lab-order-kubernetes", Resource: "deployment"}
	var visibleID string
	for _, key := range []string{"visible", "hidden"} {
		app, err := repository.RegisterApplication(ctx, core.Application{Name: key, Key: key, Environment: "lab"})
		if err != nil {
			t.Fatal(err)
		}
		environments, err := repository.Environments(ctx, app.ID)
		if err != nil || len(environments) != 1 {
			t.Fatalf("environments: %v %v", environments, err)
		}
		environment := environments[0]
		environment.Bindings = []core.Binding{metricBinding, kubeBinding}
		if _, err = repository.SaveEnvironment(ctx, environment); err != nil {
			t.Fatal(err)
		}
		applicationACLFixtureResource(t, repository, app.ID, environment.ID, viewer.ID, key == "visible")
		if key == "visible" {
			visibleID = app.ID
			if err = repository.SetGrants(ctx, app.ID, []core.Grant{{SubjectType: "user", SubjectID: viewer.ID, Permission: "view"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	metric := NewIntegration("prometheus", "Prometheus", "connector", "prometheus", true, []string{"lab-order-api"}, []string{"metrics.read"})
	metric.RegisteredBindings = []core.Binding{metricBinding}
	kube := NewIntegration(kubeBinding.ConnectorID, "Kubernetes", "connector", "kubernetes", true, nil, []string{"deployment.read"})
	kube.RegisteredBindings = []core.Binding{kubeBinding}
	api.Integrations = []Integration{metric, kube}
	_, token, err := repository.Login(ctx, viewer.Email, "isolated-test-password")
	if err != nil {
		t.Fatal(err)
	}
	response := call(t, server, http.MethodGet, "/api/v1/integrations", "", &http.Cookie{Name: "aiops_session", Value: token})
	defer response.Body.Close()
	var result struct {
		Items []Integration `json:"items"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(result.Items) != 2 {
		t.Fatalf("inventory response: %d %+v", response.StatusCode, result)
	}
	for _, item := range result.Items {
		if len(item.ApplicationIDs) != 1 || item.ApplicationIDs[0] != visibleID {
			t.Fatalf("bindings not projected through ACL: %+v", item)
		}
		if item.Status != "configured_unverified" {
			t.Fatalf("configuration presented as health: %+v", item)
		}
	}
	if api.Integrations[0].ApplicationIDs[0] != "lab-order-api" {
		t.Fatal("request mutated shared inventory")
	}
	if _, err = repository.DB.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id LIKE 'fixture-role-%'`, viewer.ID); err != nil {
		t.Fatal(err)
	}
	revoked := call(t, server, http.MethodGet, "/api/v1/integrations", "", &http.Cookie{Name: "aiops_session", Value: token})
	defer revoked.Body.Close()
	result.Items = nil
	if err := json.NewDecoder(revoked.Body).Decode(&result); err != nil || revoked.StatusCode != http.StatusOK || len(result.Items) != 2 {
		t.Fatalf("revoked inventory response: status=%d err=%v", revoked.StatusCode, err)
	}
	for _, item := range result.Items {
		if len(item.ApplicationIDs) != 0 {
			t.Fatalf("revoked resource bindings remain visible: %+v", item)
		}
	}
}
