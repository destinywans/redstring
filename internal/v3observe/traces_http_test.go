package v3observe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type traceTelemetryStub struct {
	*metricTelemetryStub
	calls int
	spans []connectors.TraceSpan
}

func (m *traceTelemetryStub) QueryTracesBatchID(_ context.Context, resources []string, _ connectors.TimeWindow, limit int, id string) (map[string]connectors.TraceEvidence, error) {
	m.calls++
	if len(resources) != 1 || resources[0] != "registered-app" || limit != 20 || id != "" {
		return nil, &connectors.TelemetryError{Kind: "scope", Message: "unexpected trace query"}
	}
	return map[string]connectors.TraceEvidence{"registered-app": {Spans: m.spans}}, nil
}

func TestTraceListDoesNotReportSampleAsPopulation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_traces_test_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-traces-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-traces-password")
	if err != nil {
		t.Fatal(err)
	}
	telemetry := &traceTelemetryStub{metricTelemetryStub: &metricTelemetryStub{}, spans: []connectors.TraceSpan{{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SpanID: "bbbbbbbbbbbbbbbb", Service: "app", Name: "GET /ready",
		Start: time.Now().UTC().Add(-time.Minute), DurationMS: 12,
	}}}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st, policy, telemetry).Register(api)
	api.MarkV3ObjectRoute("GET /api/v1/traces", "traces", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/traces"+query, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertError := func(w *httptest.ResponseRecorder, status int, code string) {
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
	assertError(request("?service=app&view=unknown"), 422, "invalid_view")
	assertError(request("?service=app"), 503, "not_configured")
	if telemetry.calls != 0 {
		t.Fatal("queried Tempo without a persisted binding")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"tempo","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	for _, sample := range [][]connectors.TraceSpan{telemetry.spans, {}} {
		telemetry.spans = sample
		w := request("?service=app")
		assertError(w, 503, "trace_aggregation_unavailable")
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["total"]; exists {
			t.Fatal("sample size was reported as complete trace total")
		}
		if _, exists := body["histogram"]; exists {
			t.Fatal("sample was reported as a complete trace histogram")
		}
		var observed []traceRow
		if err := json.Unmarshal(body["observed_sample"], &observed); err != nil || len(observed) != len(sample) {
			t.Fatalf("observed sample lost: %s, error=%v", w.Body.String(), err)
		}
		if len(observed) != 0 && observed[0].Href != "/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?service=app" {
			t.Fatalf("trace detail link lost service scope: %+v", observed[0])
		}
	}
	if href := traceHref("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "app/team & test"); href != "/traces/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?service=app%2Fteam+%26+test" {
		t.Fatalf("trace service link is not URL encoded: %q", href)
	}
	beforeLengthCheck := telemetry.calls
	w := request("?service=" + strings.Repeat("x", 201))
	assertError(w, 422, "invalid_service")
	if telemetry.calls != beforeLengthCheck {
		t.Fatal("queried Tempo for an invalid service filter")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='traces' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	before := telemetry.calls
	assertError(request("?service=app"), 403, "forbidden")
	if telemetry.calls != before {
		t.Fatal("queried Tempo after trace grant was revoked")
	}
}
