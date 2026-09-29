package v3integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestPrometheusRegistrationBindingVaultAndScopedQuery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_prom_query_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, err := adminDB.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment,archived) VALUES ('checkout','Checkout','checkout','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES
 ('checkout-dev','checkout','dev','Dev','[]'),('checkout-test','checkout','test','Test','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('checkout','dev'),('checkout','test')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','services','view','application','checkout')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	admin, adminSession, err := st.Login(ctx, "admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	readerUser, _, err := st.Login(ctx, "reader@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	var vaultCalls atomic.Int32
	var vaultUnavailable atomic.Bool
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vaultCalls.Add(1)
		if r.URL.Path != "/v1/isolated/data/prom" || r.Header.Get("X-Vault-Token") != "test-vault-access" {
			t.Errorf("unexpected Vault request path=%s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if vaultUnavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"test-prom-bearer"}}}`))
	}))
	defer vault.Close()
	resolver := NewVaultResolver(vault.URL, "test-vault-access")
	var queryCalls atomic.Int32
	var queryUnavailable atomic.Bool
	var queryEntered chan struct{}
	var releaseQuery chan struct{}
	const expression = `sum(rate(checkout_requests_total{application="checkout",environment="dev"}[5m]))`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-prom-bearer" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/-/ready":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/query_range":
			queryCalls.Add(1)
			if r.URL.Query().Get("query") != expression || r.URL.Query().Get("limit") != "100" {
				t.Errorf("unexpected registered query request")
			}
			if queryEntered != nil {
				close(queryEntered)
				<-releaseQuery
			}
			if queryUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			start, parseErr := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
			if parseErr != nil {
				t.Errorf("query start: %v", parseErr)
				return
			}
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"checkout"},"values":[[%d,"2.5"]]}]}}`, start.Unix())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, "nonprod", resolver).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: adminSession})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertHTTP := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d body=%s; want %d", w.Code, w.Body.String(), status)
		}
	}
	created := request(http.MethodPost, "/api/v1/connectors", fmt.Sprintf(`{"type":"prometheus","url":%q,"auth":"bearer","credential_ref":"vault://isolated/prom#token","tools":["query_metrics"]}`, upstream.URL))
	assertHTTP(created, http.StatusCreated)
	var registration struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &registration); err != nil || registration.ID == "" {
		t.Fatalf("connector registration: %s %v", created.Body.String(), err)
	}
	serviceID := base64.RawURLEncoding.EncodeToString([]byte("checkout\x00dev"))
	targetPath := "/api/v1/services/" + serviceID + "/external-targets"
	bad := request(http.MethodPost, targetPath, fmt.Sprintf(`{"connector_id":%q,"target_kind":"metric_resource","external_id":"https://unexpected.example"}`, registration.ID))
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("URL-shaped resource was accepted: %s", bad.Body.String())
	}
	bound := request(http.MethodPost, targetPath, fmt.Sprintf(`{"connector_id":%q,"target_kind":"metric_resource","external_id":"checkout-web"}`, registration.ID))
	assertHTTP(bound, http.StatusCreated)
	var target externalTarget
	if err := json.Unmarshal(bound.Body.Bytes(), &target); err != nil || target.ID == "" || target.Provider != "prometheus" {
		t.Fatalf("metric target: %s %v", bound.Body.String(), err)
	}
	reader, err := NewPrometheusReader(db, "nonprod", resolver, map[PrometheusQueryKey]string{
		{ConnectorID: registration.ID, ApplicationID: "checkout", EnvironmentID: "dev", ResourceID: "checkout-web", QueryID: "request_rate"}: expression,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := httpapi.V3EnvironmentScope{Tier: "nonprod", EnvironmentIDs: []string{"dev"}}
	window := prometheusTestWindow()
	code := func(err error) string {
		t.Helper()
		if item, ok := err.(*PrometheusReadError); ok {
			return item.Code
		}
		if item, ok := err.(*PrometheusQueryError); ok {
			return item.Code
		}
		return ""
	}
	var registrationStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM v3_connectors WHERE id=$1`, registration.ID).Scan(&registrationStatus); err != nil || registrationStatus != "unverified" {
		t.Fatalf("registration incorrectly marked healthy: status=%q err=%v", registrationStatus, err)
	}
	queryUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, admin, "checkout", "dev", target.ID, "request_rate", window); code(err) != "unavailable" || queryCalls.Load() != 1 {
		t.Fatalf("new registration masked failed live query: code=%q calls=%d", code(err), queryCalls.Load())
	}
	queryUnavailable.Store(false)
	assertHTTP(request(http.MethodPost, "/api/v1/connectors/"+registration.ID+"/test", ""), http.StatusOK)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "forbidden" || queryCalls.Load() != 1 {
		t.Fatalf("missing connector grant: code=%q calls=%d", code(err), queryCalls.Load())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','connectors','view','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "not_reviewed", window); code(err) != "query_unregistered" || queryCalls.Load() != 1 {
		t.Fatalf("unregistered query: code=%q calls=%d", code(err), queryCalls.Load())
	}
	if _, err := reader.Query(ctx, httpapi.V3EnvironmentScope{Tier: "nonprod", EnvironmentIDs: []string{"test"}}, readerUser,
		"checkout", "dev", target.ID, "request_rate", window); code(err) != "source_not_found" || queryCalls.Load() != 1 {
		t.Fatalf("cross-environment read: code=%q calls=%d", code(err), queryCalls.Load())
	}
	if _, err := db.Exec(ctx, `UPDATE v3_connectors SET last_checked_at=now()-interval '16 minutes' WHERE id=$1`, registration.ID); err != nil {
		t.Fatal(err)
	}
	evidence, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window)
	if err != nil || evidence.Empty || len(evidence.Series) != 1 || evidence.Series[0].Samples[0].Value != "2.5" || queryCalls.Load() != 2 || vaultCalls.Load() < 3 {
		t.Fatalf("real query evidence=%+v err=%v calls=%d vault=%d", evidence, err, queryCalls.Load(), vaultCalls.Load())
	}
	raw, err := json.Marshal(evidence)
	if err != nil || strings.Contains(string(raw), "test-prom-bearer") || strings.Contains(string(raw), "vault://") || strings.Contains(string(raw), expression) || strings.Contains(string(raw), upstream.URL) {
		t.Fatalf("query evidence leaked private source data: %v", err)
	}
	queryUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "unavailable" {
		t.Fatalf("ready probe masked failed query: code=%q", code(err))
	}
	queryUnavailable.Store(false)
	vaultUnavailable.Store(true)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "credential_unavailable" {
		t.Fatalf("unavailable Vault: code=%q", code(err))
	}
	vaultUnavailable.Store(false)
	assertHTTP(request(http.MethodPatch, "/api/v1/connectors/"+registration.ID,
		`{"tools":[{"name":"query_metrics","enabled":false}]}`), http.StatusOK)
	if _, err := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window); code(err) != "source_unhealthy" {
		t.Fatalf("disabled tool: code=%q", code(err))
	}
	assertHTTP(request(http.MethodPatch, "/api/v1/connectors/"+registration.ID,
		`{"tools":[{"name":"query_metrics","enabled":true}]}`), http.StatusOK)
	queryEntered = make(chan struct{})
	releaseQuery = make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, queryErr := reader.Query(ctx, scope, readerUser, "checkout", "dev", target.ID, "request_rate", window)
		result <- queryErr
	}()
	select {
	case <-queryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("live query did not start")
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='connectors' AND action='view' AND scope_kind='environment' AND scope_id='dev'`); err != nil {
		t.Fatal(err)
	}
	close(releaseQuery)
	if err := <-result; code(err) != "forbidden" {
		t.Fatalf("grant revoked during query: code=%q", code(err))
	}
}
