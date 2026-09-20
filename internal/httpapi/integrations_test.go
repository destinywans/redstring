package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

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
}
