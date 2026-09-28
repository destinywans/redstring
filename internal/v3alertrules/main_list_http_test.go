package v3alertrules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestMainAlertRuleListRegisteredHTTPAndProviderScope(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_rules_main_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-rule-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),
 ('viewer','environment','view','environment','staging'),
 ('viewer','alert_rules','view','application','allowed'),
 ('viewer','alert_rules','view','application','stage-app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('allowed','allowed','Allowed','test',false),
 ('hidden','hidden','Hidden','test',false),
 ('stage-app','stage-app','Stage App','staging',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('allowed-test','allowed','test','Test',false),
 ('hidden-test','hidden','test','Test',false),
 ('stage-staging','stage-app','staging','Staging',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('allowed','test'),('hidden','test'),('stage-app','staging')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := st.Login(ctx, "viewer@example.invalid", "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	var stagingFails, tooMany, duplicateVisible atomic.Bool
	rule := func(name, env, app string) map[string]any {
		labels := map[string]string{}
		if env != "" {
			labels["environment"] = env
		}
		if app != "" {
			labels["application"] = app
		}
		return map[string]any{"type": "alerting", "name": name, "query": "up == 0", "state": "inactive", "labels": labels}
	}
	group := func(name string, rules ...map[string]any) map[string]any {
		return map[string]any{"file": "/etc/prometheus/rules.yml", "name": name, "rules": rules}
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("type") != "alert" || !strings.HasSuffix(r.URL.Path, "/api/v1/rules") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/staging/") && stagingFails.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		groups := []map[string]any{}
		if strings.HasPrefix(r.URL.Path, "/test/") {
			groups = []map[string]any{
				group("allowed-group", rule("AllowedRule", "test", "allowed")),
				group("hidden-group", rule("HiddenRule", "test", "hidden")),
				group("unlabeled-group", rule("NoOwner", "", "")),
				group("unbound-group", rule("NoBinding", "test", "allowed")),
			}
			if duplicateVisible.Load() {
				second := rule("AllowedRule", "test", "allowed")
				second["query"] = "up == 1"
				second["state"] = "firing"
				groups[0] = group("allowed-group", rule("AllowedRule", "test", "allowed"), second)
			}
			if tooMany.Load() {
				many := make([]map[string]any, 501)
				for i := range many {
					many[i] = rule("Rule-"+store.ID(), "test", "allowed")
				}
				groups = []map[string]any{group("allowed-group", many...)}
			}
		} else if strings.HasPrefix(r.URL.Path, "/staging/") {
			firing := rule("StageRule", "staging", "stage-app")
			firing["state"] = "firing"
			groups = []map[string]any{group("stage-group", firing)}
		} else {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"groups": groups}})
	}))
	defer provider.Close()
	source, err := NewPrometheusSource(map[string]PrometheusEndpoint{
		"test":    {URL: provider.URL + "/test", AllowHTTP: true},
		"staging": {URL: provider.URL + "/staging", AllowHTTP: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service, err := New(DatabaseCatalog{DB: db}, policy, source, []RuleGroupBinding{
		{EnvironmentID: "test", ApplicationID: "allowed", File: "/etc/prometheus/rules.yml", Group: "allowed-group"},
		{EnvironmentID: "test", ApplicationID: "hidden", File: "/etc/prometheus/rules.yml", Group: "hidden-group"},
		{EnvironmentID: "test", ApplicationID: "allowed", File: "/etc/prometheus/rules.yml", Group: "unlabeled-group"},
		{EnvironmentID: "staging", ApplicationID: "stage-app", File: "/etc/prometheus/rules.yml", Group: "stage-group"},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, true)
	service.Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(path, environment string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	checkList := func(w *httptest.ResponseRecorder, wantAll, wantFiring int) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
		}
		var response struct {
			Items  []struct{ ID, Folder, Name, State string } `json:"items"`
			Counts map[string]int                             `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Items) != wantAll || response.Counts["all"] != wantAll || response.Counts["firing"] != wantFiring || response.Counts["scheduled"] != 0 {
			t.Fatalf("incomplete or invented counts: %s", w.Body.String())
		}
		for _, item := range response.Items {
			if !strings.HasPrefix(item.ID, "prom-") || item.Name == "HiddenRule" || item.Name == "NoOwner" || item.Name == "NoBinding" {
				t.Fatalf("unbound or unauthorized rule escaped: %+v", item)
			}
		}
	}
	checkList(request("/api/v1/alert-rules", "test"), 1, 0)
	checkList(request("/api/v1/alert-rules", "all"), 2, 1)
	visibleID := toDetail("test", "allowed", ObservedRule{GroupIdentity: "/etc/prometheus/rules.yml\x00allowed-group", Group: "allowed-group", Name: "AllowedRule"}).ID
	hiddenID := toDetail("test", "hidden", ObservedRule{GroupIdentity: "/etc/prometheus/rules.yml\x00hidden-group", Group: "hidden-group", Name: "HiddenRule"}).ID
	detailPath := "/api/v1/alert-rules/observed/" + visibleID
	w := request(detailPath, "test")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"expr":"up == 0"`) || !strings.Contains(w.Body.String(), `"partial":true`) {
		t.Fatalf("unique observed detail status=%d body=%s", w.Code, w.Body.String())
	}
	w = request("/api/v1/alert-rules/observed/"+hiddenID, "test")
	if w.Code != 404 || strings.Contains(w.Body.String(), "HiddenRule") {
		t.Fatalf("hidden observed detail leaked: %d %s", w.Code, w.Body.String())
	}
	duplicateVisible.Store(true)
	w = request(detailPath, "test")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "alert_rule_identity_conflict") || strings.Contains(w.Body.String(), "up ==") {
		t.Fatalf("ambiguous observed detail returned a rule: %d %s", w.Code, w.Body.String())
	}
	duplicateVisible.Store(false)
	stagingFails.Store(true)
	w = request("/api/v1/alert-rules/observed/nonexistent", "all")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "rule_source_partial") {
		t.Fatalf("unknown detail with unavailable provider was treated as absent: %d %s", w.Code, w.Body.String())
	}
	for _, env := range []string{"all", "staging"} {
		w := request("/api/v1/alert-rules", env)
		if w.Code != 503 || strings.Contains(w.Body.String(), `"counts"`) {
			t.Fatalf("partial main-list counts leaked for %s: %d %s", env, w.Code, w.Body.String())
		}
	}
	w = request("/api/v1/alert-rules/observed", "all")
	if w.Code != 206 || !strings.Contains(w.Body.String(), `"unavailable_environments":["staging"]`) {
		t.Fatalf("auxiliary partial diagnostic changed: %d %s", w.Code, w.Body.String())
	}
	stagingFails.Store(false)
	tooMany.Store(true)
	w = request("/api/v1/alert-rules", "test")
	if w.Code != 503 || strings.Contains(w.Body.String(), `"counts"`) {
		t.Fatalf("oversized provider was reported as complete: %d %s", w.Code, w.Body.String())
	}
	tooMany.Store(false)
	w = request("/api/v1/alert-rules/observed/nonexistent", "all")
	if w.Code != 404 || strings.Contains(w.Body.String(), "AllowedRule") {
		t.Fatalf("unknown observed detail leaked a rule: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='alert_rules' AND scope_id='allowed'`); err != nil {
		t.Fatal(err)
	}
	w = request("/api/v1/alert-rules", "test")
	if w.Code != 403 {
		t.Fatalf("revoked application grant still listed rules: %d %s", w.Code, w.Body.String())
	}
	w = request(detailPath, "test")
	if w.Code != 403 || strings.Contains(w.Body.String(), "AllowedRule") || strings.Contains(w.Body.String(), "up ==") {
		t.Fatalf("revoked application grant still disclosed detail: %d %s", w.Code, w.Body.String())
	}
}
