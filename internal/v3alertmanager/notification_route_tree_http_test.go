package v3alertmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type fixedRouteTree struct{ Source }

func (fixedRouteTree) ObserveRouteTree(context.Context, string, string) (RouteTreeObservation, error) {
	return RouteTreeObservation{Root: RouteTreeNode{ID: "root", Title: "root"}, ObservedAt: time.Now(),
		ConfigSHA256: strings.Repeat("a", 64)}, nil
}

// PA-56: the sanitized tree is readable with alert_rules.view, as the route
// is marked; testing a route still needs manage.
func TestNotificationRouteTreeNeedsOnlyView(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_route_tree_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.MaxConns = 2
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-route-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer';
 INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),('viewer','alert_rules','view','environment','test')`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, env string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: env})
		}); err != nil {
		t.Fatal(err)
	}
	(&Service{Source: fixedRouteTree{}, Catalog: DatabaseCatalog{DB: db}, Access: policy,
		EnvironmentLabels: map[string]string{"test": "environment"}}).Register(api)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		r.Header.Set("X-AIOps-Environment", "test")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if got := call(http.MethodGet, "/api/v1/notification-routes", ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"id":"root"`) {
		t.Fatalf("view-only tree read: %d %s", got.Code, got.Body.String())
	}
	if got := call(http.MethodPost, "/api/v1/notification-routes/test", `{"labels":{"alertname":"x"}}`); got.Code != http.StatusForbidden {
		t.Fatalf("view-only route test: %d %s", got.Code, got.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='alert_rules'`); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodGet, "/api/v1/notification-routes", ""); got.Code != http.StatusForbidden {
		t.Fatalf("tree read without view: %d %s", got.Code, got.Body.String())
	}
}
