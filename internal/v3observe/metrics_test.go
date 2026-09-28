package v3observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type metricTelemetryStub struct {
	calls []string
	empty bool
}

func (m *metricTelemetryStub) QueryMetrics(_ context.Context, app, query string, _ connectors.TimeWindow) (connectors.MetricEvidence, error) {
	m.calls = append(m.calls, app+"/"+query)
	result := connectors.MetricEvidence{Source: connectors.TelemetrySource{HTTPStatus: 200, QueryID: query}, Empty: m.empty}
	if !m.empty {
		result.Series = []connectors.MetricSeries{{Samples: []connectors.MetricSample{{Timestamp: 1, Value: "1"}}}}
	}
	return result, nil
}

func (*metricTelemetryStub) QueryLogsOptions(context.Context, string, string, string, string, bool, connectors.TimeWindow, int) (connectors.LogEvidence, error) {
	return connectors.LogEvidence{}, nil
}

func (*metricTelemetryStub) QueryTracesBatchID(context.Context, []string, connectors.TimeWindow, int, string) (map[string]connectors.TraceEvidence, error) {
	return nil, nil
}

func TestMetricDashboardDoesNotInventMissingPanels(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_metrics_test_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-metrics-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-metrics-password")
	if err != nil {
		t.Fatal(err)
	}
	telemetry := &metricTelemetryStub{}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, telemetry).Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/metrics/dashboard", "services", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/dashboard"+query, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	checkCode := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != status || body.Error.Code != code {
			t.Fatalf("status=%d body=%s, want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	checkCode(request("?service=app&range=6h"), 422, "invalid_range")
	checkCode(request("?service=app"), 503, "not_configured")
	if len(telemetry.calls) != 0 {
		t.Fatalf("queried provider without persisted binding: %v", telemetry.calls)
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"prometheus","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	w := request("?service=app")
	checkCode(w, 503, "dashboard_unavailable")
	var body struct {
		Available map[string]metricProbe `json:"available"`
		Missing   []string               `json:"missing"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.calls) != 2 || telemetry.calls[0] != "registered-app/request_rate" || telemetry.calls[1] != "registered-app/http_p95_seconds" {
		t.Fatalf("unexpected provider calls: %v", telemetry.calls)
	}
	if body.Available["request_rate"].Status != "observed" || body.Available["http_p95_seconds"].Status != "observed" || !slices.Contains(body.Missing, "slo_budget") || !slices.Contains(body.Missing, "requests_by_status") {
		t.Fatalf("dashboard evidence/missing sections: %+v", body)
	}
	telemetry.empty = true
	w = request("?service=app&align=true")
	checkCode(w, 503, "dashboard_unavailable")
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !slices.Contains(body.Missing, "request_rate") || !slices.Contains(body.Missing, "incident_alignment") {
		t.Fatalf("empty metric response: %s error=%v", w.Body.String(), err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='services' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	callsBefore := len(telemetry.calls)
	checkCode(request("?service=app"), 403, "forbidden")
	if len(telemetry.calls) != callsBefore {
		t.Fatalf("provider queried after service grant was revoked: %v", telemetry.calls[callsBefore:])
	}
}
