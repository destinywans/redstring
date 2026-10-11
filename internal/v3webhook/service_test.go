package v3webhook

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
)

func testWebhook(t *testing.T) (*Service, string) {
	t.Helper()
	token := strings.Repeat("t", 32)
	path := filepath.Join(t.TempDir(), "alert-source-token")
	if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(&pgxpool.Pool{}, httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"staging"}}, operations.Config{Sources: []operations.Source{{ID: "prom", TokenFile: path, Mappings: []operations.Mapping{{Labels: map[string]string{"service": "checkout"}, ApplicationID: "checkout", EnvironmentID: "checkout-staging"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, token
}

func alertJSON(labels string) string {
	return fmt.Sprintf(`{"alerts":[{"status":"firing","labels":%s,"startsAt":%q,"fingerprint":"fingerprint-1"}]}`, labels, time.Now().UTC().Format(time.RFC3339Nano))
}

func request(s *Service, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/alertmanager/prom", strings.NewReader(body))
	r.SetPathValue("source", "prom")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.Handler(w, r)
	return w
}

func TestSourceAuthenticationAndMappingFailClosed(t *testing.T) {
	s, token := testWebhook(t)
	calls := 0
	s.persist = func(context.Context, operations.Source, []mappedAlert) (int, error) { calls++; return 0, nil }
	valid := alertJSON(`{"service":"checkout"}`)
	for _, tc := range []struct {
		name, bearer, body string
		status             int
	}{
		{"missing token", "", valid, http.StatusUnauthorized},
		{"wrong token", strings.Repeat("x", 32), valid, http.StatusUnauthorized},
		{"unknown app", token, alertJSON(`{"service":"unknown"}`), http.StatusUnprocessableEntity},
		{"trailing payload", token, valid + `{}`, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := request(s, tc.bearer, tc.body); got.Code != tc.status {
				t.Fatalf("status %d, body %s", got.Code, got.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Fatalf("persist was called %d times before authentication and mapping", calls)
	}
	if got := request(s, token, valid); got.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", got.Code, got.Body.String())
	}
	if calls != 1 {
		t.Fatalf("persist calls = %d", calls)
	}
}

func TestAmbiguousMappingsAndCrossInstanceConfig(t *testing.T) {
	s, token := testWebhook(t)
	source := s.sources["prom"]
	source.Mappings = append(source.Mappings, operations.Mapping{Labels: map[string]string{"service": "checkout", "severity": "critical"}, ApplicationID: "other", EnvironmentID: "other-staging"})
	s.sources["prom"] = source
	s.persist = func(context.Context, operations.Source, []mappedAlert) (int, error) {
		t.Fatal("ambiguous mapping persisted")
		return 0, nil
	}
	if got := request(s, token, alertJSON(`{"service":"checkout","severity":"critical"}`)); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, body %s", got.Code, got.Body.String())
	}
	if _, err := New(&pgxpool.Pool{}, httpapi.V3InstanceConfig{Tier: "nonprod", EnvironmentIDs: []string{"prod"}}, operations.Config{}); err == nil {
		t.Fatal("non-production instance accepted prod environment")
	}
}

func TestMissingLabelDoesNotMatchConfiguredEmptyValue(t *testing.T) {
	source := operations.Source{Mappings: []operations.Mapping{{Labels: map[string]string{"team": ""}, ApplicationID: "app", EnvironmentID: "env"}}}
	if _, ok := selectMapping(source, map[string]string{}); ok {
		t.Fatal("missing label matched configured empty label value")
	}
	if _, ok := selectMapping(source, map[string]string{"team": ""}); !ok {
		t.Fatal("present empty label should match configured empty label value")
	}
}

func TestNoBrowserSessionOrEnvironmentHeaderRequired(t *testing.T) {
	s, token := testWebhook(t)
	s.persist = func(_ context.Context, source operations.Source, alerts []mappedAlert) (int, error) {
		if source.ID != "prom" || len(alerts) != 1 || alerts[0].mapping.EnvironmentID != "checkout-staging" {
			t.Fatalf("unexpected source mapping: %+v %+v", source, alerts)
		}
		return 0, nil
	}
	// request() deliberately supplies neither browser cookie nor environment
	// header. The fixed source mapping is the only accepted scope.
	if got := request(s, token, alertJSON(`{"service":"checkout"}`)); got.Code != http.StatusAccepted {
		t.Fatalf("status %d, body %s", got.Code, got.Body.String())
	}
}
