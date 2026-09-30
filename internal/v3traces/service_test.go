package v3traces

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/connectors"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type sourceStub struct {
	calls int
	trace connectors.CompleteTrace
	after func(context.Context) error
}

func (s *sourceStub) QueryCompleteTraces(ctx context.Context, q connectors.CompleteTraceQuery) ([]connectors.CompleteTrace, error) {
	s.calls++
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
	New(st, policy, source).Register(api)
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
	if source.calls != 0 {
		t.Fatal("queried Tempo without a persisted binding")
	}
	if _, err := db.Exec(ctx, `UPDATE application_environments SET bindings='[{"kind":"tempo","resource":"registered-app"}]'::jsonb WHERE id='app-dev'`); err != nil {
		t.Fatal(err)
	}
	w := request("/api/v1/traces?service=app")
	if w.Code != 200 {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	var list TraceList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Items) != 1 || len(list.Histogram.Counts) != 12 {
		t.Fatalf("incomplete list contract: %s error=%v", w.Body.String(), err)
	}
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
}
