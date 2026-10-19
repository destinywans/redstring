package v3integrations

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
)

func TestConnectorTypeDescribesRegisteredProbeWithoutInventedCapabilities(t *testing.T) {
	service := New(nil, "nonprod", nil)
	for provider, descriptor := range descriptors {
		for _, lang := range []string{"zh-CN", "en-US"} {
			t.Run(provider+"/"+lang, func(t *testing.T) {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/connector-types/"+provider, nil)
				request.SetPathValue("type", provider)
				request.Header.Set("Accept-Language", lang)
				response := httptest.NewRecorder()
				service.connectorType(response, request)
				if response.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				var got struct {
					Brand       string `json:"brand"`
					Kind        string `json:"kind"`
					Version     string `json:"version"`
					AuthMethods []struct {
						ID    string `json:"id"`
						Label string `json:"label"`
						Desc  string `json:"desc"`
					} `json:"auth_methods"`
					Scopes []any `json:"scopes"`
					Tools  []struct {
						Name string `json:"name"`
						Desc string `json:"desc"`
					} `json:"tools"`
					WebhookURL string `json:"webhook_url"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Brand != provider || got.Kind == "" || got.Version != "" || len(got.Scopes) != 0 || got.WebhookURL != "" {
					t.Fatalf("invented or missing metadata: %+v", got)
				}
				if len(got.AuthMethods) != len(descriptor.Methods) || got.Scopes == nil || got.Tools == nil || len(got.Tools) != 0 {
					t.Fatalf("descriptor mismatch: %+v", got)
				}
				for i, method := range descriptor.Methods {
					if got.AuthMethods[i].ID != method || got.AuthMethods[i].Label == "" || got.AuthMethods[i].Desc == "" {
						t.Fatalf("auth method %d: %+v", i, got.AuthMethods[i])
					}
				}
			})
		}
	}
}

type environmentAuthorizer struct{}

func (environmentAuthorizer) AuthorizeEnvironment(_ context.Context, _ core.User, id string) error {
	if id == "dev" {
		return nil
	}
	return store.ErrForbidden
}

func TestConnectorScopeUsesAuthorizedEnvironmentSet(t *testing.T) {
	service := New(nil, "nonprod", nil)
	var allowed []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, ok := service.scope(w, r, r.Method == http.MethodPost)
		if ok {
			allowed = ids
			w.WriteHeader(http.StatusNoContent)
		}
	})
	handler, err := httpapi.NewV3EnvironmentHandler(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}}, environmentAuthorizer{}, func(context.Context, string) (core.User, error) {
		return core.User{ID: "admin", Role: "admin"}, nil
	}, next)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/connectors", nil)
	request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
	request.Header.Set("X-AIOps-Environment", "all")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(allowed) != 1 || allowed[0] != "dev" {
		t.Fatalf("allowed=%v status=%d", allowed, response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/connectors", nil)
	request.AddCookie(&http.Cookie{Name: "aiops_session", Value: "test-session"})
	request.Header.Set("X-AIOps-Environment", "all")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("write status=%d body=%s", response.Code, response.Body.String())
	}
}
