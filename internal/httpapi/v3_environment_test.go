package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/store"
)

type testV3EnvironmentAuthorizer map[string]bool

func (a testV3EnvironmentAuthorizer) AuthorizeEnvironment(_ context.Context, _ core.User, id string) error {
	if a[id] {
		return nil
	}
	return store.ErrForbidden
}

type testV3EnvironmentAuthorizerFunc func(context.Context, core.User, string) error

func (f testV3EnvironmentAuthorizerFunc) AuthorizeEnvironment(ctx context.Context, user core.User, id string) error {
	return f(ctx, user, id)
}

func testV3Session(_ context.Context, token string) (core.User, error) {
	if token != "good" {
		return core.User{}, errors.New("expired")
	}
	return core.User{ID: "u1"}, nil
}

func TestV3EnvironmentAllIsReadOnlyAndFiltered(t *testing.T) {
	called := 0
	var got V3EnvironmentScope
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		var ok bool
		got, ok = V3ScopeFromContext(r.Context())
		if !ok {
			t.Fatal("missing scope")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test", "staging"}},
		testV3EnvironmentAuthorizer{"dev": true, "test": true}, testV3Session, next)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/resources", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
		r.Header.Set("X-AIOps-Environment", "all")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet); w.Code != http.StatusNoContent {
		t.Fatalf("read status=%d, body=%s", w.Code, w.Body.String())
	}
	if !got.All || !reflect.DeepEqual(got.EnvironmentIDs, []string{"dev", "test"}) {
		t.Fatalf("scope=%+v", got)
	}
	w := request(http.MethodPost)
	if w.Code != http.StatusConflict || called != 1 {
		t.Fatalf("write status=%d, handler calls=%d", w.Code, called)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code != "env_view_only" {
		t.Fatalf("error=%+v, decode=%v", body, err)
	}
}

func TestV3EnvironmentAllAllowsOnlyStructuredReadPost(t *testing.T) {
	called := 0
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		testV3EnvironmentAuthorizer{"dev": true, "test": true}, testV3Session,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called++
			scope, ok := V3ScopeFromContext(r.Context())
			if !ok || !scope.All || !reflect.DeepEqual(scope.EnvironmentIDs, []string{"dev", "test"}) {
				t.Fatalf("unexpected query scope: %+v", scope)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/api/v1/knowledge/search", http.StatusNoContent}, {"/api/v1/knowledge/import", http.StatusConflict}} {
		r := httptest.NewRequest(http.MethodPost, tc.path, nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
		r.Header.Set("X-AIOps-Environment", "all")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s status=%d, want %d", tc.path, w.Code, tc.want)
		}
	}
	if called != 1 {
		t.Fatalf("read-only handler calls=%d", called)
	}
}

func TestV3EnvironmentRejectsMissingAndUnauthorizedScope(t *testing.T) {
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		testV3EnvironmentAuthorizer{"dev": true}, testV3Session, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, path, env, token string
		status                         int
	}{
		{"missing", "GET", "/api/v1/resources", "", "good", 422},
		{"unauthorized", "GET", "/api/v1/resources", "test", "good", 403},
		{"unknown", "GET", "/api/v1/resources", "prod", "good", 422},
		{"anonymous", "GET", "/api/v1/resources", "dev", "", 401},
		{"login exception", "POST", "/api/v1/auth/login", "", "", 204},
		{"logout exception", "POST", "/api/v1/auth/logout", "", "good", 204},
		{"identity exception", "GET", "/api/v1/auth/me", "", "good", 204},
		{"sse persisted scope", "GET", "/api/v1/conversations/123/events", "", "good", 204},
		{"audit export persisted scope", "GET", "/api/v1/audit/exports/export-1", "", "good", 204},
		{"audit export needs session", "GET", "/api/v1/audit/exports/export-1", "", "", 401},
		{"other audit read needs environment", "GET", "/api/v1/audit/other/export-1", "", "good", 422},
		{"audit export extra path needs environment", "GET", "/api/v1/audit/exports/export-1/other", "", "good", 422},
		{"audit export explicit environment", "GET", "/api/v1/audit/exports/export-1", "dev", "good", 204},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, nil)
			if test.env != "" {
				r.Header.Set("X-AIOps-Environment", test.env)
			}
			if test.token != "" {
				r.AddCookie(&http.Cookie{Name: "aiops_session", Value: test.token})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.status {
				t.Fatalf("status=%d, want=%d", w.Code, test.status)
			}
		})
	}
}

func TestV3HeaderlessAuditExportDoesNotAllowMalformedEnvironment(t *testing.T) {
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		testV3EnvironmentAuthorizer{"dev": true}, testV3Session, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/audit/exports/export-1", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
	r.Header.Set("X-AIOps-Environment", "dev,prod")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestV3EnvironmentAllFailsClosedWhenPolicyUnavailable(t *testing.T) {
	called := false
	authorizer := testV3EnvironmentAuthorizerFunc(func(_ context.Context, _ core.User, id string) error {
		if id == "dev" {
			return nil
		}
		return errors.New("database unavailable")
	})
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		authorizer, testV3Session, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/resources", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
	r.Header.Set("X-AIOps-Environment", "all")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status=%d, called=%v", w.Code, called)
	}
}

func TestV3ProductionCannotSelectNonProduction(t *testing.T) {
	h, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "production", DefaultEnv: "prod", EnvironmentIDs: []string{"prod"}},
		testV3EnvironmentAuthorizer{"prod": true}, testV3Session, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope, ok := V3ScopeFromContext(r.Context())
			if !ok || !reflect.DeepEqual(scope.EnvironmentIDs, []string{"prod"}) {
				t.Fatalf("scope=%+v", scope)
			}
			w.WriteHeader(204)
		}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		env    string
		status int
	}{{"", 204}, {"prod", 204}, {"all", 422}, {"dev", 422}} {
		r := httptest.NewRequest("GET", "/api/v1/resources", nil)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
		if test.env != "" {
			r.Header.Set("X-AIOps-Environment", test.env)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("env=%q status=%d, want=%d", test.env, w.Code, test.status)
		}
	}
	if _, err := NewV3EnvironmentHandler(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "prod"}}, testV3EnvironmentAuthorizer{}, testV3Session, http.NotFoundHandler()); !errors.Is(err, ErrInvalidV3Instance) {
		t.Fatalf("invalid configuration accepted: %v", err)
	}
}
