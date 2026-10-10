package v3traces

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

type sourceStub struct {
	calls      int
	window     connectors.TimeWindow
	trace      connectors.CompleteTrace
	after      func(context.Context) error
	unattested bool
	// coveredFrom, when set, is where a search stopped at its budget.
	coveredFrom time.Time
	viewFilter  string
}

func (s *sourceStub) QueryObservedTraces(ctx context.Context, q connectors.CompleteTraceQuery) (connectors.TraceObservation, error) {
	s.viewFilter = q.ViewFilter
	if q.Window.End.Sub(q.Window.Start) > 7*24*time.Hour {
		return connectors.TraceObservation{}, &connectors.TelemetryError{Kind: "lookback", LimitSeconds: 7 * 86400}
	}
	traces, err := s.QueryCompleteTraces(ctx, q)
	observed := connectors.TraceObservation{Traces: traces, PopulationComplete: !s.unattested,
		WindowStart: q.Window.Start, WindowEnd: q.Window.End}
	if !s.coveredFrom.IsZero() {
		observed.WindowStart, observed.Truncated = s.coveredFrom, true
	}
	return observed, err
}

func (s *sourceStub) QueryCompleteTraces(ctx context.Context, q connectors.CompleteTraceQuery) ([]connectors.CompleteTrace, error) {
	s.calls++
	s.window = q.Window
	if len(q.Resources) != 1 || q.Resources[0] != "registered-app" {
		return nil, &connectors.TelemetryError{Kind: "scope", Message: "unexpected source"}
	}
	if s.after != nil {
		if err := s.after(ctx); err != nil {
			return nil, err
		}
	}
	if q.TraceID != "" && q.TraceID != s.trace.ID {
		return []connectors.CompleteTrace{}, nil
	}
	return []connectors.CompleteTrace{s.trace}, nil
}

func TestTraceRoutesUsePersistedBindingAndRecheckAuthorization(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_traces_complete_test_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	id := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	source := &sourceStub{trace: connectors.CompleteTrace{ID: id, Spans: []connectors.TraceSpan{{
		TraceID: id, SpanID: "bbbbbbbbbbbbbbbb", Service: "app", Version: "v1", Name: "GET /ready",
		Start: time.Now().UTC().Add(-time.Minute), DurationMS: 12,
	}}}}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	compared := &recordedUses{}
	traces := New(st, policy, source)
	traces.SourceCompare = compared
	traces.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
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
			t.Fatalf("status=%d body=%s want %d %s", w.Code, w.Body.String(), status, code)
		}
	}
	assertError(request("/api/v1/traces?service=app"), 404, "not_found")
	assertError(request("/api/v1/traces?service=missing"), 404, "not_found")
	// Without any Tempo binding the unfiltered list states "not configured";
	// it never returns an empty list that reads as "no traces".
	w := request("/api/v1/traces?scope=app")
	var unconfigured map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &unconfigured); err != nil || w.Code != 200 ||
		string(unconfigured["total"]) != "null" || string(unconfigured["items"]) != "null" ||
		string(unconfigured["histogram"]) != "null" || string(unconfigured["facets"]) != "null" ||
		!json.Valid(unconfigured["unavailable"]) || !containsAll(string(unconfigured["unavailable"]), `"items":"not_configured"`, `"total":"not_configured"`) ||
		string(unconfigured["partial_reason"]) != `"not_configured"` {
		t.Fatalf("unconfigured trace list: status=%d body=%s", w.Code, w.Body.String())
	}
	assertError(request("/api/v1/traces/"+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 503, "not_configured")
	if source.calls != 0 {
		t.Fatal("queried Tempo without a persisted binding")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"tempo","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	w = request("/api/v1/traces?service=app")
	if w.Code != 200 {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var list TraceList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Items) != 1 || len(list.Histogram.Counts) != 12 ||
		list.Unavailable == nil || len(list.Unavailable) != 0 {
		t.Fatalf("incomplete list contract: %s error=%v", w.Body.String(), err)
	}
	// An unattested search keeps the closed traces it found but refuses every
	// figure derived from that possibly incomplete population.
	source.unattested = true
	w = request("/api/v1/traces?service=app&view=all")
	var observed struct {
		Total       *int              `json:"total"`
		Facets      []TraceFacet      `json:"facets"`
		Histogram   *TraceHistogram   `json:"histogram"`
		Scatter     []TraceScatter    `json:"scatter"`
		Items       []TraceRow        `json:"items"`
		Partial     bool              `json:"partial"`
		Views       []map[string]any  `json:"views"`
		Unavailable map[string]string `json:"unavailable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &observed); err != nil || w.Code != 200 || observed.Total != nil ||
		observed.Facets != nil || observed.Histogram != nil || observed.Scatter != nil || !observed.Partial ||
		len(observed.Items) != 1 || observed.Items[0].ID != id || len(observed.Views) != 3 || observed.Views[0]["count"] != nil ||
		observed.Unavailable["total"] != "not_configured" || observed.Unavailable["histogram"] != "not_configured" ||
		observed.Unavailable["items"] != "" {
		t.Fatalf("unattested list must keep traces and drop population figures: status=%d body=%s", w.Code, w.Body.String())
	}
	source.unattested = false
	w = request("/api/v1/traces?service=app&source=tempo&range=6h")
	if w.Code != 200 || source.window.End.Sub(source.window.Start) != 6*time.Hour {
		t.Fatalf("requested window not passed upstream: %d %s %+v", w.Code, w.Body.String(), source.window)
	}
	// PA-78: why a list is partial and which window it covers.
	type budgetList struct {
		Total         *int   `json:"total"`
		Partial       bool   `json:"partial"`
		PartialReason string `json:"partial_reason"`
		WindowStartAt string `json:"window_start_at"`
		WindowEndAt   string `json:"window_end_at"`
	}
	readBudget := func() budgetList {
		t.Helper()
		w := request("/api/v1/traces?service=app")
		var body budgetList
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	if full := readBudget(); full.Partial || full.PartialReason != "" || full.WindowStartAt != source.window.Start.UTC().Format(time.RFC3339) ||
		full.WindowEndAt != source.window.End.UTC().Format(time.RFC3339) || full.Total == nil {
		t.Fatalf("complete list window: %+v", full)
	}
	source.coveredFrom = time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	if stopped := readBudget(); !stopped.Partial || stopped.PartialReason != "search_budget" || stopped.Total == nil ||
		stopped.WindowStartAt != source.coveredFrom.Format(time.RFC3339) {
		t.Fatalf("attested list stopped at the budget: %+v", stopped)
	}
	source.unattested = true
	if stopped := readBudget(); !stopped.Partial || stopped.PartialReason != "search_budget" || stopped.Total != nil ||
		stopped.WindowStartAt != source.coveredFrom.Format(time.RFC3339) {
		t.Fatalf("unattested list stopped at the budget: %+v", stopped)
	}
	source.coveredFrom = time.Time{}
	if unattested := readBudget(); unattested.PartialReason != "search_unattested" || unattested.WindowStartAt == "" {
		t.Fatalf("unattested list: %+v", unattested)
	}
	source.unattested = false
	// PA-79: a range beyond the lookback is refused with the limit.
	w = request("/api/v1/traces?service=app&range=30d")
	var rangeError struct {
		Error struct {
			Code       string `json:"code"`
			MaxSeconds int    `json:"max_lookback_seconds"`
			MaxRange   string `json:"max_range"`
		} `json:"error"`
	}
	if json.Unmarshal(w.Body.Bytes(), &rangeError) != nil || w.Code != 422 || rangeError.Error.Code != "range_exceeds_lookback" ||
		rangeError.Error.MaxSeconds != 7*86400 || rangeError.Error.MaxRange != "7d" {
		t.Fatalf("range beyond the lookback: %d %s", w.Code, w.Body.String())
	}
	// PA-80: a view's selector goes to the search.
	for view, filter := range map[string]string{"errors": "{ status = error }", "slow": "{ trace:duration >= 1s }", "all": ""} {
		if w := request("/api/v1/traces?service=app&view=" + view); w.Code != 200 || source.viewFilter != filter {
			t.Fatalf("view %s searched with %q: %d %s", view, source.viewFilter, w.Code, w.Body.String())
		}
	}
	assertError(request("/api/v1/traces?service=app&source=jaeger"), 503, "trace_source_not_connected")
	assertError(request("/api/v1/traces?service=app&range=1h&range=7d"), 422, "invalid_range")
	w = request("/api/v1/traces/" + id + "?service=app")
	if w.Code != 200 {
		t.Fatalf("detail status=%d body=%s", w.Code, w.Body.String())
	}
	var detail TraceView
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.Version != "v1" || len(detail.Spans) != 1 || !detail.Spans[0].Critical {
		t.Fatalf("incomplete detail contract: %s error=%v", w.Body.String(), err)
	}
	for _, path := range []string{"/api/v1/traces?service=app", "/api/v1/traces/" + id + "?service=app"} {
		entered := make(chan struct{})
		release := make(chan struct{})
		source.after = func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() { response <- request(path) }()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			close(release)
			t.Fatal("Tempo query did not enter the blocking source")
		}
		if err := st.Logout(ctx, session); err != nil {
			close(release)
			t.Fatal(err)
		}
		close(release)
		select {
		case w := <-response:
			assertError(w, 401, "unauthorized")
		case <-time.After(10 * time.Second):
			t.Fatal("trace query did not finish after logout")
		}
		_, session, err = st.Login(ctx, "admin@example.invalid", "isolated-traces-password")
		if err != nil {
			t.Fatal(err)
		}
	}
	source.after = func(ctx context.Context) error {
		_, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='traces' AND action='view'`)
		return err
	}
	assertError(request("/api/v1/traces?service=app"), 403, "forbidden")
	assertError(request("/api/v1/traces?service=app"), 404, "not_found")
	// PA-19 compare mode heard each authorized legacy read.
	if !compared.saw(v3telemetry.Use{ApplicationID: "app", EnvironmentID: "dev", Provider: "tempo", Resource: "registered-app"}) {
		t.Fatalf("compare mode uses: %+v", compared.uses)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

// recordedUses collects PA-19 compare-mode notifications.
type recordedUses struct {
	mu   sync.Mutex
	uses []v3telemetry.Use
}

func (r *recordedUses) ObserveLegacy(u v3telemetry.Use) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uses = append(r.uses, u)
}

func (r *recordedUses) saw(u v3telemetry.Use) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.uses {
		if seen == u {
			return true
		}
	}
	return false
}
