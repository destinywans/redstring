package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestV3RejectsUnmarkedLegacyRoute(t *testing.T) {
	called := false
	s := &Server{mux: http.NewServeMux(), v3Routes: map[string]V3RoutePermission{}}
	s.mux.HandleFunc("GET /api/v1/legacy", func(http.ResponseWriter, *http.Request) { called = true })
	if err := s.ConfigureV3Environment(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, testV3EnvironmentAuthorizer{"dev": true}, testV3Session, nil); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/legacy", nil)
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
	r.Header.Set("X-AIOps-Environment", "dev")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusNotImplemented || called {
		t.Fatalf("status=%d handler_called=%v", w.Code, called)
	}
	s.MarkV3Route("GET /api/v1/legacy", "legacy", "view")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !called {
		t.Fatalf("marked route status=%d handler_called=%v", w.Code, called)
	}
	called = false
	write := httptest.NewRequest(http.MethodPost, "/api/v1/legacy", nil)
	write.AddCookie(&http.Cookie{Name: "aiops_session", Value: "good"})
	write.Header.Set("X-AIOps-Environment", "all")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, write)
	if w.Code != http.StatusConflict || called {
		t.Fatalf("all write status=%d handler_called=%v", w.Code, called)
	}
}

func TestV3OnlyAlertmanagerCallbackBypassesBrowserScope(t *testing.T) {
	called := false
	s := &Server{mux: http.NewServeMux(), v3Routes: map[string]V3RoutePermission{}}
	s.mux.HandleFunc("POST /api/v1/webhooks/alertmanager/{source}", func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.PathValue("source") != "prom" {
			t.Fatal("wrong source path")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	s.mux.HandleFunc("POST /api/v1/webhooks/other/{source}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	if err := s.ConfigureV3Environment(V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, testV3EnvironmentAuthorizer{"dev": true}, testV3Session, nil); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager/prom", nil))
	if w.Code != http.StatusAccepted || !called {
		t.Fatalf("callback status=%d called=%v", w.Code, called)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/other/prom", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("other callback status=%d", w.Code)
	}
}
