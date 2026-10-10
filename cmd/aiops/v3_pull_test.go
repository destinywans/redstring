package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3ingestpull"
	"github.com/wans/aiops-canvas/internal/v3integrations"
)

func TestV3PullCatalogFailsBeforeRouteConfiguration(t *testing.T) {
	db, err := pgxpool.New(context.Background(), "postgres://localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const validSource = `{"sources":[{"id":"checkout-rate","name":"Checkout rate",` +
		`"application_id":"checkout","environment_id":"dev","connector_id":"prometheus",` +
		`"target_id":"checkout-metrics","resource_id":"checkout-web","query_id":"request_rate",` +
		`"expression":"sum(rate(checkout_requests_total[5m]))",` +
		`"name_labels":["__name__"],"label_keys":["pod"]}]}`
	path := filepath.Join(t.TempDir(), "pull.json")
	for _, tc := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"public catalog", validSource, 0644},
		{"caller controlled URL", strings.Replace(validSource, `"query_id":"request_rate"`, `"query_id":"request_rate","url":"http://127.0.0.1:9090"`, 1), 0600},
		{"credential label", strings.Replace(validSource, `"label_keys":["pod"]`, `"label_keys":["api_token"]`, 1), 0600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AIOPS_V3_PULL_SOURCES_CONFIG", path)
			// A malformed catalog must fail before ConfigureV3Environment or
			// route registration touches the deliberately absent API server.
			err := configureV3(context.Background(), &store.Store{DB: db}, nil, nil, nil, nil,
				&v3Instance{}, httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, operations.Config{})
			if err == nil || !strings.Contains(err.Error(), "Pull") {
				t.Fatalf("unsafe Pull catalog passed the startup gate: %v", err)
			}
		})
	}
}

func TestV3PullCatalogDisabledWithoutExplicitFile(t *testing.T) {
	config, err := v3ingestpull.LoadConfig("")
	if err != nil || len(config.Sources) != 0 {
		t.Fatalf("unset Pull catalog should install no sources: %+v, %v", config, err)
	}
}

func TestRegisteredV3PullUsesBoundPrometheusAndVault(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_pull_entry_" + store.ID()
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
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "pull-admin@example.invalid", "isolated-password"); err != nil {
		t.Fatal(err)
	}
	admin, session, err := st.Login(ctx, "pull-admin@example.invalid", "isolated-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key,environment,archived) VALUES('checkout','Checkout','checkout','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,bindings) VALUES('checkout-dev','checkout','dev','Dev','[]')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('checkout','dev')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var queries, secrets atomic.Int32
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secrets.Add(1)
		if r.URL.Path != "/v1/isolated/data/pull" || r.Header.Get("X-Vault-Token") != "vault-test-access" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"data":{"token":"test-bearer-only"}}}`))
	}))
	defer vault.Close()
	const expression = `up{application="checkout",environment="dev"}`
	prometheus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		if r.URL.Path != "/api/v1/query_range" || r.Header.Get("Authorization") != "Bearer test-bearer-only" ||
			r.FormValue("query") != expression {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		start, err := time.Parse(time.RFC3339Nano, r.FormValue("start"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{"__name__":"up","pod":"pod-1","api_token":"never-expose"},"values":[[%d,"1"]]},`+
			`{"metric":{"pod":"pod-2"},"values":[[%d,"1"]]}`+
			`]}}`, start.Unix(), start.Unix())
	}))
	defer prometheus.Close()
	if _, err := db.Exec(ctx, `INSERT INTO v3_connectors(id,environment_id,provider,name,category,endpoint,auth_method,
 credential_ref,enabled,tools) VALUES('pull-prometheus','dev','prometheus','Prometheus','observe',$1,
 'bearer','vault://isolated/pull#token',true,'[{"name":"query_metrics","enabled":true}]')`, prometheus.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_connector_targets(id,application_id,environment_id,connector_id,target_kind,external_id,bound_by)
 VALUES('pull-target','checkout','dev','pull-prometheus','metric_resource','checkout-web',$1)`, admin.ID); err != nil {
		t.Fatal(err)
	}
	source := v3ingestpull.SourceConfig{ID: "checkout-rate", Name: "Checkout rate", ApplicationID: "checkout",
		EnvironmentID: "dev", ConnectorID: "pull-prometheus", TargetID: "pull-target", ResourceID: "checkout-web",
		QueryID: "request_rate", Expression: expression, NameLabels: []string{"__name__", "pod"}, LabelKeys: []string{"pod"}}
	resolver := v3integrations.NewBoundResolver(v3integrations.NewVaultResolver(vault.URL, "vault-test-access"),
		[]v3integrations.CredentialBinding{{Tier: "nonprod", EnvironmentID: "dev", ConnectorID: "pull-prometheus",
			Provider: "prometheus", Endpoint: prometheus.URL, CredentialRef: "vault://isolated/pull#token"}})
	api := httpapi.New(st, nil, true)
	v3cfg := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}}
	if err := registerV3Pull(api, st, v3cfg, resolver, v3ingestpull.Config{Sources: []v3ingestpull.SourceConfig{source}}); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	if err := api.ConfigureV3Environment(v3cfg, policy, st.Session,
		func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, env string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/ingest/pull/" + source.ID
	get := call(http.MethodGet, path, "", "dev")
	if get.Code != 200 || !strings.Contains(get.Body.String(), `"preview"`) ||
		strings.Contains(get.Body.String(), "never-expose") || strings.Contains(get.Body.String(), expression) {
		t.Fatalf("formal Pull GET = %d %s", get.Code, get.Body.String())
	}
	test := call(http.MethodPost, path+"/test", `{}`, "dev")
	if test.Code != 200 || !strings.Contains(test.Body.String(), `"rows":1`) || !strings.Contains(test.Body.String(), `"dropped":1`) {
		t.Fatalf("formal Pull test = %d %s", test.Code, test.Body.String())
	}
	if queries.Load() != 2 || secrets.Load() != 2 {
		t.Fatalf("formal routes did not use bounded readers: queries=%d vault=%d", queries.Load(), secrets.Load())
	}
	other := call(http.MethodGet, path, "", "staging")
	if other.Code != 404 || queries.Load() != 2 || secrets.Load() != 2 {
		t.Fatalf("cross-environment Pull reached source: %d %s", other.Code, other.Body.String())
	}
}
