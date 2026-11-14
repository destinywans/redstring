package v3serviceviews

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/v3integrations"
	"github.com/destinywans/redstring/internal/v3metricpanels"
)

func TestServiceDetailGoldenSignalReasons(t *testing.T) {
	st, _ := serviceTestDB(t)
	ctx := t.Context()
	now := time.Now().UTC()
	seedServiceViews(t, st, now)
	var queryCalls atomic.Int32
	var response atomic.Value
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryCalls.Add(1)
		if r.URL.Path != "/api/v1/query_range" || r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("query") != "up" {
			t.Errorf("unexpected upstream query: %s %s", r.Method, r.URL.Path)
			http.Error(w, "fixture request mismatch", 400)
			return
		}
		if body := response.Load().(string); body != "" {
			fmt.Fprint(w, body)
		} else {
			http.Error(w, "fixture upstream failure", 503)
		}
	}))
	defer source.Close()
	if _, err := st.DB.Exec(ctx, "UPDATE v3_connectors SET endpoint=$1 WHERE id='prom'", source.URL); err != nil {
		t.Fatal(err)
	}

	newReader := func(registered bool) *v3integrations.PrometheusReader {
		t.Helper()
		queries := map[v3integrations.PrometheusQueryKey]string{}
		if registered {
			queries[v3integrations.PrometheusQueryKey{ConnectorID: "prom", ApplicationID: "checkout-app", EnvironmentID: "test", ResourceID: "checkout-metrics", QueryID: "dashboard"}] = "up"
		}
		reader, err := v3integrations.NewPrometheusReader(st.DB, "nonprod", nil, queries)
		if err != nil {
			t.Fatal(err)
		}
		return reader
	}
	policy := &rbac.V3Policy{Store: st}
	svc := New(st, policy, newReader(false), nil)
	svc.Now = func() time.Time { return now }
	api := httpapi.New(st, nil, true)
	svc.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}

	for _, withSLO := range []bool{true, false} {
		if !withSLO {
			if _, err := st.DB.Exec(ctx, "UPDATE v3_service_environment_profiles SET slo_target_percent=NULL WHERE application_id='checkout-app' AND environment_id='test'"); err != nil {
				t.Fatal(err)
			}
		}
		d := getDetail(t, get, "checkout-app")
		if d.Signals != nil || d.Unavailable["signals"] != "query_not_configured" || d.Unavailable["slo.availability"] != "not_configured" || queryCalls.Load() != 0 {
			t.Fatalf("unregistered dashboard (target configured %v): detail=%+v upstream calls=%d", withSLO, d, queryCalls.Load())
		}
	}
	svc.AlertCovered = func(ctx context.Context, app, _ string) string {
		if app == "checkout-app" {
			if _, err := st.DB.Exec(ctx, "UPDATE v3_connector_targets SET external_id='different-metrics' WHERE id='metric-checkout'"); err != nil {
				t.Error(err)
			}
		}
		return ""
	}
	w := get("/services/" + encodeID("checkout-app", "test"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "service_source_changed") {
		t.Fatalf("unregistered dashboard survived a binding change: %d %s", w.Code, w.Body)
	}
	svc.AlertCovered = nil
	if _, err := st.DB.Exec(ctx, "UPDATE v3_connector_targets SET external_id='checkout-metrics' WHERE id='metric-checkout'"); err != nil {
		t.Fatal(err)
	}

	w = get("/services?q=Checkout")
	var listing catalog
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listing) != nil || len(listing.Items) != 1 ||
		listing.Items[0].Unavailable["connectors"] != "not_configured" || queryCalls.Load() != 0 {
		t.Fatalf("catalog should retain previous behavior without SLO: %d %s, calls=%d", w.Code, w.Body, queryCalls.Load())
	}

	svc.MetricReader = newReader(true)
	current := now.Truncate(metricStep * time.Second).Unix()
	matrix := func(series string) string {
		return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":%s}}`, series)
	}
	tests := []struct {
		name           string
		body           string
		signalsReason  string
		latencyReason  string
		observedSignal string
	}{
		{name: "source failure", signalsReason: "unavailable"},
		{name: "empty successful query", body: matrix("[]"), signalsReason: "not_generated"},
		{name: "only SLO panels", body: matrix(fmt.Sprintf(`[{"metric":{"panel":"%s"},"values":[[%d,"1"]]}]`,
			v3metricpanels.SLOBurn1h, current)), signalsReason: "query_not_configured", latencyReason: "query_not_configured"},
		{name: "configured signal with no current sample", body: matrix(fmt.Sprintf(`[{"metric":{"panel":"%s"},"values":[[%d,"NaN"]]}]`,
			v3metricpanels.LatencyP99, current)), signalsReason: "not_generated", latencyReason: "not_generated"},
		{name: "partial signal without an SLO target", body: matrix(fmt.Sprintf(`[{"metric":{"panel":"%s"},"values":[[%d,"42.5"]]}]`,
			v3metricpanels.LatencyP99, current)), observedSignal: "p99"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response.Store(tt.body)
			start := queryCalls.Load()
			d := getDetail(t, get, "checkout-app")
			if queryCalls.Load() != start+1 || d.Unavailable["signals"] != tt.signalsReason ||
				d.Unavailable["signals.p99"] != tt.latencyReason {
				t.Fatalf("signal result=%+v upstream calls=%d", d, queryCalls.Load()-start)
			}
			if d.SLO.Target != nil || d.SLO.Availability != nil || d.SLO.BudgetLeft != nil || d.SLO.Burn != nil || d.Status != nil ||
				d.Unavailable["slo.availability"] != "not_configured" || d.Unavailable["status"] != "not_configured" {
				t.Fatalf("no SLO target should not produce SLO or health status: %+v", d)
			}
			if tt.observedSignal != "" {
				if len(d.Signals) != 1 || d.Signals[0].ID != tt.observedSignal || d.Signals[0].Value != "42.5" ||
					d.Unavailable["signals.cpu"] != "query_not_configured" || d.Unavailable["signals.rps"] != "query_not_configured" {
					t.Fatalf("partial signals=%+v, unavailable=%v", d.Signals, d.Unavailable)
				}
			} else if d.Signals != nil {
				t.Fatalf("expected null signals, got %+v", d.Signals)
			}
		})
	}

	before := queryCalls.Load()
	if _, err := st.DB.Exec(ctx, "DELETE FROM v3_connector_targets WHERE id='metric-checkout'"); err != nil {
		t.Fatal(err)
	}
	d := getDetail(t, get, "checkout-app")
	if d.Signals != nil || d.Unavailable["signals"] != "not_connected" || queryCalls.Load() != before {
		t.Fatalf("unbound service called upstream or had incorrect reason: %+v", d)
	}
}
