package v3integrations

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

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
