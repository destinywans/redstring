package v3traces

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3telemetry"
)

// labTempo is a Tempo that honours the search window and limit like the real
// one: steady lab traffic, one two-span trace every few seconds. Some traces
// lost a span in transit, as the lab produced on 2026-10-10 (a Kafka consumer
// span whose producer span never reached Tempo).
type labTempo struct {
	mu         sync.Mutex
	starts     map[string]time.Time
	incomplete map[string]bool
	server     *httptest.Server
}

func newLabTempo(t *testing.T, now time.Time, every time.Duration, span time.Duration, incomplete ...time.Duration) *labTempo {
	l := &labTempo{}
	l.traffic(now, every, span, incomplete...)
	l.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		defer l.mu.Unlock()
		switch {
		case r.URL.Path == "/api/search":
			start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if !strings.Contains(r.URL.Query().Get("q"), `resource.service.name = "app"`) || limit < 1 {
				t.Errorf("unexpected search %s", r.URL)
			}
			ids := []string{}
			for id, at := range l.starts {
				if at.Unix() >= start && at.Unix() < end {
					ids = append(ids, id)
				}
			}
			sort.Slice(ids, func(i, j int) bool { return l.starts[ids[i]].After(l.starts[ids[j]]) })
			items := []map[string]string{}
			for _, id := range ids {
				if len(items) == limit {
					break
				}
				items = append(items, map[string]string{"traceID": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"traces": items})
		case strings.HasPrefix(r.URL.Path, "/api/traces/"):
			id := strings.TrimPrefix(r.URL.Path, "/api/traces/")
			at, ok := l.starts[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			span := func(spanID, parent, name string, offset time.Duration) map[string]any {
				item := map[string]any{"traceId": id, "spanId": spanID, "name": name,
					"startTimeUnixNano": strconv.FormatInt(at.Add(offset).UnixNano(), 10),
					"endTimeUnixNano":   strconv.FormatInt(at.Add(offset+5*time.Millisecond).UnixNano(), 10)}
				if parent != "" {
					item["parentSpanId"] = parent
				}
				return item
			}
			spans := []any{span("00000000000000a1", "", "POST /orders", 0), span("00000000000000a2", "00000000000000a1", "mysql INSERT", time.Millisecond)}
			if l.incomplete[id] {
				// The consumer span's parent (the producer) is missing.
				spans = []any{span("00000000000000b2", "00000000000000b1", "orders.completed process", 0)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"resourceSpans": []any{map[string]any{
				"resource": map[string]any{"attributes": []any{
					map[string]any{"key": "service.name", "value": map[string]any{"stringValue": "app"}},
					map[string]any{"key": "deployment.environment", "value": map[string]any{"stringValue": "dev"}},
				}},
				"scopeSpans": []any{map[string]any{"spans": spans}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(l.server.Close)
	return l
}

// traffic replaces the stored traces: one complete trace every period over
// span before now, and one incomplete trace at each given age.
func (l *labTempo) traffic(now time.Time, every time.Duration, span time.Duration, incomplete ...time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts, l.incomplete = map[string]time.Time{}, map[string]bool{}
	n := 0
	for at := now.Add(-time.Second); at.After(now.Add(-span)); at = at.Add(-every) {
		n++
		l.starts[fmt.Sprintf("%032x", n)] = at
	}
	for _, ago := range incomplete {
		n++
		id := fmt.Sprintf("%032x", n)
		l.starts[id], l.incomplete[id] = now.Add(-ago), true
	}
}

// PA-87: the default list (All view, one hour) at lab traffic without a
// search completeness attestation is 200 search_budget, also when a trace in
// the searched window lost a span: that trace is left out and counted, it no
// longer fails the whole list with 503. Attested, a left-out trace withdraws
// the population figures: a search that stopped at its budget still says
// search_budget with its window (D30), one that covered the range says
// incomplete_traces. The trace's own detail stays strict.
func TestDefaultTraceListAtLabTrafficSurvivesAnIncompleteTrace(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_traces_lab_list_" + store.ID()
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
	if db, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-traces-password"); err != nil {
		t.Fatal(err)
	}
	_, session, err := st.Login(ctx, "admin@example.invalid", "isolated-traces-password")
	if err != nil {
		t.Fatal(err)
	}
	// One trace every 6 s for two hours; one incomplete trace 30 s ago.
	tempo := newLabTempo(t, time.Now(), 6*time.Second, 2*time.Hour, 30*time.Second)
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,credential_ref,enabled,tools)
 VALUES('tempo-1','dev','tempo','Tempo','observe',$1,'none','',true,'[{"name":"query_traces","desc":"","enabled":true}]')`, tempo.server.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('t-tempo','app','dev','tempo-1','trace_service','registered-app',(SELECT id FROM users WHERE email='admin@example.invalid'))`); err != nil {
		t.Fatal(err)
	}
	catalog, err := v3telemetry.ParseCatalog([]byte(`{"scopes":[{"connector_id":"tempo-1","application_id":"app","environment_id":"dev",
 "resource_id":"registered-app","provider":"tempo","tempo_service":"app","tempo_attributes":{"deployment.environment":"dev"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	traces := New(st, policy)
	traces.Registry = &v3telemetry.Resolver{Tier: "nonprod", Catalog: catalog, Approvals: v3telemetry.NewApprovals(nil)}
	traces.Clients = &v3telemetry.Clients{Tier: "nonprod"}
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
	type listed struct {
		Total              *int       `json:"total"`
		Items              []TraceRow `json:"items"`
		Partial            bool       `json:"partial"`
		PartialReason      string     `json:"partial_reason"`
		ExcludedIncomplete int        `json:"excluded_incomplete"`
		WindowStartAt      string     `json:"window_start_at"`
		WindowEndAt        string     `json:"window_end_at"`
	}
	read := func(path string) listed {
		t.Helper()
		w := request(path)
		var body listed
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return body
	}

	// The default list: All view, one hour, unattested.
	for _, path := range []string{"/api/v1/traces?scope=app", "/api/v1/traces?scope=app&view=all", "/api/v1/traces?scope=app&service=app"} {
		list := read(path)
		start, _ := time.Parse(time.RFC3339, list.WindowStartAt)
		end, _ := time.Parse(time.RFC3339, list.WindowEndAt)
		if !list.Partial || list.PartialReason != "search_budget" || list.Total != nil || list.ExcludedIncomplete != 1 ||
			len(list.Items) == 0 || len(list.Items) > 40 || end.Sub(start) >= time.Hour {
			t.Fatalf("%s: %+v (%d items)", path, list, len(list.Items))
		}
	}
	// Attested, at lab traffic: the search stops at its budget, so the
	// reason stays search_budget with the covered window; the population
	// figures are withdrawn and the left-out trace is still counted (D30).
	traces.Clients.TempoSearchFailClosed = map[string]bool{"tempo-1": true}
	attested := read("/api/v1/traces?scope=app")
	attestedStart, _ := time.Parse(time.RFC3339, attested.WindowStartAt)
	attestedEnd, _ := time.Parse(time.RFC3339, attested.WindowEndAt)
	if attested.Total != nil || !attested.Partial || attested.PartialReason != "search_budget" || attested.ExcludedIncomplete != 1 ||
		attestedEnd.Sub(attestedStart) >= time.Hour {
		t.Fatalf("attested list at the budget with an incomplete trace: %+v", attested)
	}
	// Attested, sparse traffic: the search covers the hour, so the left-out
	// trace is the reason the figures are withdrawn.
	tempo.traffic(time.Now(), 5*time.Minute, 2*time.Hour, 30*time.Second)
	covered := read("/api/v1/traces?scope=app")
	coveredStart, _ := time.Parse(time.RFC3339, covered.WindowStartAt)
	coveredEnd, _ := time.Parse(time.RFC3339, covered.WindowEndAt)
	if covered.Total != nil || covered.PartialReason != "incomplete_traces" || covered.ExcludedIncomplete != 1 ||
		len(covered.Items) != 12 || coveredEnd.Sub(coveredStart) != time.Hour {
		t.Fatalf("attested list covering the range with an incomplete trace: %+v (%d items)", covered, len(covered.Items))
	}
	// Without a left-out trace the attested list is complete again.
	tempo.traffic(time.Now(), 5*time.Minute, 2*time.Hour)
	if complete := read("/api/v1/traces?scope=app"); complete.Total == nil || *complete.Total != 12 || complete.Partial || complete.ExcludedIncomplete != 0 {
		t.Fatalf("attested complete list: %+v", complete)
	}
	traces.Clients.TempoSearchFailClosed = nil
	tempo.traffic(time.Now(), 6*time.Second, 2*time.Hour, 30*time.Second)

	// The incomplete trace's own detail is still refused, not shown in part.
	var incompleteID string
	for id := range tempo.incomplete {
		incompleteID = id
	}
	if w := request("/api/v1/traces/" + incompleteID); w.Code != 503 || !strings.Contains(w.Body.String(), "telemetry_unavailable") {
		t.Fatalf("incomplete trace detail: %d %s", w.Code, w.Body.String())
	}
}
