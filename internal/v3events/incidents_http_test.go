package v3events

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestIncidentsRegisteredHTTPUsesScopedDurableCloseTime(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_incidents_test_" + store.ID()
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
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-incidents-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev'),('other','Other','other','test');
		INSERT INTO application_environments(id,application_id,key,name)
		 VALUES('app-dev','app','dev','Dev'),('other-test','other','test','Test');
		INSERT INTO v3_application_environments(application_id,environment_id)
		 VALUES('app','dev'),('other','test');
		INSERT INTO investigations(id,application_id,title) VALUES('investigation','app','Real investigation');
		INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,ends_at,updated_at,investigation_id)
		 VALUES
		 ('open','test','app','app-dev','open','Open incident','critical','open','firing',now()-interval '90 minutes 10 seconds',NULL,now(),''),
		 ('mitigated','test','app','app-dev','mitigated','Mitigated alert','warning','investigating','resolved',now()-interval '2 hours',now()-interval '1 hour',now(),''),
		 ('closed','test','app','app-dev','closed','Closed investigation','info','resolved','resolved',now()-interval '3 hours',now()-interval '2 hours',now(),'investigation'),
		 ('unknown','test','app','app-dev','unknown','Legacy closed','info','resolved','resolved',now()-interval '4 hours',now()-interval '3 hours',now(),''),
		 ('hidden','test','other','other-test','hidden','Other environment','critical','open','firing',now()-interval '1 hour',NULL,now(),'');
		INSERT INTO events(investigation_id,type,payload,created_at)
		 VALUES('investigation','investigation.closed','{}',now()-interval '1 hour');`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-incidents-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	service := New(db, func(ctx context.Context, user core.User, app, env, action string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, nil)
	api := httpapi.New(st, nil, true)
	api.Register("GET /api/v1/incidents", "viewer", service.Incidents)
	api.MarkV3ObjectRoute("GET /api/v1/incidents", "events", "view")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(env, status string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/incidents"+status, nil)
		r.Header.Set("X-AIOps-Environment", env)
		if auth {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assertCode := func(w *httptest.ResponseRecorder, status int, code string) {
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
	assertCode(request("dev", "", false), 401, "unauthorized")
	assertCode(request("dev", "?status=unknown", true), 422, "invalid_status")
	assertCode(request("prod", "", true), 422, "invalid_environment")

	read := func(env, filter string) ([]incidentSummary, map[string]int) {
		t.Helper()
		w := request(env, filter, true)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Items  []incidentSummary `json:"items"`
			Counts map[string]int    `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Items, body.Counts
	}
	items, counts := read("dev", "")
	if len(items) != 2 || counts["all"] != 4 || counts["active"] != 2 || counts["resolved"] != 2 {
		t.Fatalf("default active items=%+v counts=%+v", items, counts)
	}
	if items[0].ID != "open" || items[0].Severity != "P1" || items[0].Duration != "1h 30m" || items[1].ID != "mitigated" || items[1].Status != "mitigated" || items[1].Duration != "2h 0m" || items[1].Commander != "" {
		t.Fatalf("active DTO/order=%+v", items)
	}
	resolved, _ := read("dev", "?status=resolved")
	if len(resolved) != 2 || resolved[0].ID != "unknown" || resolved[0].Duration != "未记录" || resolved[1].ID != "closed" || resolved[1].Duration != "2h 0m" {
		t.Fatalf("resolved duration must come from close event, not alert end or mutable update: %+v", resolved)
	}
	all, total := read("all", "?status=all")
	if len(all) != 5 || total["all"] != 5 || total["active"] != 3 {
		t.Fatalf("all nonprod items=%+v counts=%+v", all, total)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('admin','events','view','application','app')`); err != nil {
		t.Fatal(err)
	}
	scoped, scopedCounts := read("all", "?status=all")
	if len(scoped) != 4 || scopedCounts["all"] != 4 || scopedCounts["active"] != 2 || scopedCounts["resolved"] != 2 {
		t.Fatalf("application grant must scope list and counts: items=%+v counts=%+v", scoped, scopedCounts)
	}
	// Closing time is immutable even if a later unrelated update changes the row.
	if _, err := db.Exec(ctx, `UPDATE ops_incidents SET updated_at=now()+interval '1 day' WHERE id='closed'`); err != nil {
		t.Fatal(err)
	}
	stable, _ := read("dev", "?status=resolved")
	if stable[0].ID != "closed" || stable[0].Duration != "2h 0m" {
		t.Fatalf("close duration changed after unrelated update: %+v", stable)
	}
}
