package v3models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func isolatedModelStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_model_observation_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-model-admin-password"); err != nil {
		t.Fatal(err)
	}
	return st
}

func modelAPI(t *testing.T, st *store.Store, runtime inference.Runner) (*httpapi.Server, *Service) {
	t.Helper()
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	service := New(st.DB, runtime, policy)
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	return api, service
}

func modelRequest(api *httpapi.Server, method, path, token, env, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-AIOps-Environment", env)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	}
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func requireModelResponse(t *testing.T, w *httptest.ResponseRecorder, status int, modelStatus string) Observation {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status=%d body=%s; want %d", w.Code, w.Body.String(), status)
	}
	var view Observation
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.Status != modelStatus {
		t.Fatalf("view=%+v err=%v body=%s; want %s", view, err, w.Body.String(), modelStatus)
	}
	return view
}

func TestModelObservationHTTPPersistenceAuditAndProcessStaleness(t *testing.T) {
	st := isolatedModelStore(t)
	var upstreamFailure atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models/local" {
			t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
		}
		if upstreamFailure.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"local"}`))
	}))
	defer upstream.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "openai:local", Kind: "openai", Model: "local", BaseURL: upstream.URL + "/v1"}}, HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	api, service := modelAPI(t, st, runtime)
	if service.Source == nil {
		t.Fatal("runtime probe source was not connected")
	}
	_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-model-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	get := func(api *httpapi.Server) *httptest.ResponseRecorder {
		return modelRequest(api, http.MethodGet, "/api/v1/models/observations?model=openai%3Alocal", token, "dev", "")
	}
	unchecked := requireModelResponse(t, get(api), 200, "configured_unverified")
	if unchecked.Model != "openai:local" || unchecked.CheckedAt != nil || unchecked.LatencyMS != nil || unchecked.ErrorCode != "" {
		t.Fatalf("unverified observation invented evidence: %+v", unchecked)
	}
	if w := modelRequest(api, http.MethodPost, "/api/v1/models/check", "", "dev", `{"model":"openai:local"}`); w.Code != 401 {
		t.Fatalf("anonymous check=%d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{}`, `{"model":"absent"}`, `{"model":"openai:local","extra":true}`} {
		w := modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", body)
		if w.Code != 404 && w.Code != 422 {
			t.Fatalf("invalid check %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	checked := requireModelResponse(t, modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`), 200, "verified")
	if checked.CheckedAt == nil || checked.LatencyMS == nil || checked.ErrorCode != "" {
		t.Fatalf("missing observed evidence: %+v", checked)
	}
	replay := requireModelResponse(t, get(api), 200, "verified")
	if replay.CheckedAt == nil || !replay.CheckedAt.Equal(*checked.CheckedAt) {
		t.Fatalf("GET did not replay saved observation: %+v versus %+v", replay, checked)
	}
	var observations, audits int
	if err := st.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_model_observations WHERE model_id='openai:local' AND environment_id='dev'`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='v3.model.checked' AND target='openai:local'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if observations != 1 || audits != 1 {
		t.Fatalf("observation=%d audit=%d", observations, audits)
	}
	if _, err := st.DB.Exec(t.Context(), `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','models','view','environment','dev'),
 ('viewer','models','manage','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), core.User{Email: "model-operator@example.invalid", Name: "Model operator", Role: "viewer"}, "isolated-model-operator-password"); err != nil {
		t.Fatal(err)
	}
	_, operatorToken, err := st.Login(t.Context(), "model-operator@example.invalid", "isolated-model-operator-password")
	if err != nil {
		t.Fatal(err)
	}
	requireModelResponse(t, modelRequest(api, http.MethodPost, "/api/v1/models/check", operatorToken, "dev", `{"model":"openai:local"}`), 200, "verified")
	upstreamFailure.Store(true)
	failed := requireModelResponse(t, modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`), 200, "error")
	if failed.ErrorCode != "authentication_required" || failed.CheckedAt == nil {
		t.Fatalf("failed check invented success or lost observation: %+v", failed)
	}
	if got := requireModelResponse(t, get(api), 200, "error"); got.ErrorCode != "authentication_required" {
		t.Fatalf("failed check did not replace old verified status: %+v", got)
	}
	restartedAPI, restarted := modelAPI(t, st, runtime)
	if restarted.instanceID == service.instanceID {
		t.Fatal("new service reused process observation identity")
	}
	stale := requireModelResponse(t, get(restartedAPI), 200, "configured_unverified")
	if stale.CheckedAt != nil || stale.LatencyMS != nil {
		t.Fatalf("stale observation leaked as current: %+v", stale)
	}
	if _, err := st.DB.Exec(t.Context(), `CREATE FUNCTION reject_model_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.model.checked' THEN RAISE EXCEPTION 'isolated audit sink failure'; END IF;
 RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(t.Context(), `CREATE TRIGGER reject_model_audit_insert BEFORE INSERT ON management_audit
 FOR EACH ROW EXECUTE FUNCTION reject_model_audit()`); err != nil {
		t.Fatal(err)
	}
	upstreamFailure.Store(false)
	rejected := modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`)
	if rejected.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure check=%d %s", rejected.Code, rejected.Body.String())
	}
	unchanged := requireModelResponse(t, get(api), 200, "error")
	if unchanged.CheckedAt == nil || !unchanged.CheckedAt.Equal(*failed.CheckedAt) || unchanged.ErrorCode != failed.ErrorCode {
		t.Fatalf("audit failure committed an observation: before=%+v after=%+v", failed, unchanged)
	}
}

type blockingModelSource struct {
	next    ModelSource
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingModelSource) Models() []inference.Model {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.next.Models()
}

func (s *blockingModelSource) ProbeModel(ctx context.Context, model string) (inference.ModelProbeResult, error) {
	return s.next.ProbeModel(ctx, model)
}

func TestModelObservationReadRejectsGrantRevokedAfterGateway(t *testing.T) {
	st := isolatedModelStore(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"local"}`))
	}))
	defer upstream.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "openai:local", Kind: "openai", Model: "local", BaseURL: upstream.URL + "/v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	api, service := modelAPI(t, st, runtime)
	_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-model-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	requireModelResponse(t, modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`), 200, "verified")
	barrier := &blockingModelSource{next: service.Source, entered: make(chan struct{}), release: make(chan struct{})}
	service.Source = barrier
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- modelRequest(api, http.MethodGet, "/api/v1/models/observations?model=openai%3Alocal", token, "dev", "")
	}()
	select {
	case <-barrier.entered:
	case <-t.Context().Done():
		t.Fatal("observation read did not reach the handler")
	}
	if _, err := st.DB.Exec(t.Context(), `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='models' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	w := <-result
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("revoked observation read=%d %s", w.Code, w.Body.String())
	}
}

func TestModelCheckRejectsRevokedGrantAfterProbe(t *testing.T) {
	st := isolatedModelStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		_, _ = w.Write([]byte(`{"id":"local"}`))
	}))
	defer upstream.Close()
	runtime, err := inference.New(inference.Config{Providers: []inference.Provider{{ID: "openai:local", Kind: "openai", Model: "local", BaseURL: upstream.URL + "/v1"}}, HTTPClient: upstream.Client()})
	if err != nil {
		t.Fatal(err)
	}
	api, _ := modelAPI(t, st, runtime)
	_, token, err := st.Login(t.Context(), "admin@example.invalid", "isolated-model-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- modelRequest(api, http.MethodPost, "/api/v1/models/check", token, "dev", `{"model":"openai:local"}`)
	}()
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("probe did not start")
	}
	if _, err := st.DB.Exec(t.Context(), `DELETE FROM v3_role_grants
 WHERE role_id='admin' AND module='models' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	close(release)
	w := <-result
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("revoked check=%d %s", w.Code, w.Body.String())
	}
	var observations, audits int
	if err := st.DB.QueryRow(t.Context(), `SELECT count(*) FROM v3_model_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action='v3.model.checked'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if observations != 0 || audits != 0 {
		t.Fatalf("revoked probe persisted observation=%d audit=%d", observations, audits)
	}
}
