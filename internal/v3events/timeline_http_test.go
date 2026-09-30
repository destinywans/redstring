package v3events

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
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/operations"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestTimelineAuthenticatedHTTPPersistsAndHonorsCurrentGrant(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_timeline_test_" + store.ID()
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
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-timeline-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev');
		INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');
		INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');
		INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
		VALUES('incident','test','app','app-dev','incident','A real event','warning','firing',now())`); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "admin@example.invalid", "isolated-timeline-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	decision := func(app, env, action string) rbac.V3Decision {
		return rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env}
	}
	service := New(db,
		func(ctx context.Context, u core.User, app, env, action string) error {
			return policy.Authorize(ctx, u, decision(app, env, action))
		},
		func(ctx context.Context, tx pgx.Tx, u core.User, app, env, action string) error {
			return policy.AuthorizeWithTx(ctx, tx, u, decision(app, env, action))
		},
	)
	api := httpapi.New(st, nil, true)
	service.RegisterNew(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"},
	}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	request := func(environment, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/incident/timeline", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	read := func(environment, id string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/"+id+"/timeline/manual", nil)
		r.Header.Set("X-AIOps-Environment", environment)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
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
	assertError(request("dev", `{"text":"note"}`, false), 401, "unauthorized")
	assertError(request("all", `{"text":"note"}`, true), 409, "env_view_only")
	assertError(request("test", `{"text":"note"}`, true), 404, "not_found")
	assertError(request("dev", `{"text":" "}`, true), 422, "invalid_text")
	empty := read("dev", "incident", true)
	if empty.Code != 200 || empty.Body.String() != "{\"items\":[],\"scope\":\"manual_only\"}\n" {
		t.Fatalf("empty manual timeline status=%d body=%s", empty.Code, empty.Body.String())
	}
	response := request("dev", `{"text":"Operator note"}`, true)
	if response.Code != 201 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var entry timelineEntry
	if err := json.Unmarshal(response.Body.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Text != "Operator note" || entry.Actor.Kind != "person" || entry.Actor.Name != "Administrator" {
		t.Fatalf("response=%+v", entry)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.Time); err != nil {
		t.Fatalf("non-ISO timestamp %q: %v", entry.Time, err)
	}
	var text, actorID, auditAction, auditedEntryID, auditedEnv string
	if err := db.QueryRow(ctx, `SELECT t.text,t.actor_id,a.action,a.changes->>'entry_id',a.changes->>'environment_id'
		FROM v3_incident_timeline t JOIN management_audit a ON a.target=t.incident_id
		WHERE t.incident_id='incident' AND a.action='incident.timeline.append'`).Scan(&text, &actorID, &auditAction, &auditedEntryID, &auditedEnv); err != nil {
		t.Fatal(err)
	}
	if text != entry.Text || actorID == "" || auditAction != "incident.timeline.append" || auditedEntryID == "" || auditedEnv != "dev" {
		t.Fatalf("durable timeline/audit mismatch: %q %q %q %q %q", text, actorID, auditAction, auditedEntryID, auditedEnv)
	}
	assertError(read("dev", "incident", false), 401, "unauthorized")
	assertError(read("test", "incident", true), 404, "not_found")
	assertError(read("dev", "missing", true), 404, "not_found")
	if second := request("dev", `{"text":"Second note"}`, true); second.Code != 201 {
		t.Fatalf("second append status=%d body=%s", second.Code, second.Body.String())
	}
	got := read("dev", "incident", true)
	var manual struct {
		Items []timelineEntry `json:"items"`
		Scope string          `json:"scope"`
	}
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &manual) != nil {
		t.Fatalf("manual read status=%d body=%s", got.Code, got.Body.String())
	}
	if manual.Scope != "manual_only" || len(manual.Items) != 2 || manual.Items[0].Text != "Operator note" || manual.Items[1].Text != "Second note" {
		t.Fatalf("manual timeline=%+v", manual)
	}
	for _, item := range manual.Items {
		if item.Actor.Kind != "person" || item.Actor.Name != "Administrator" {
			t.Fatalf("manual actor=%+v", item.Actor)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Time); err != nil {
			t.Fatalf("manual time=%q error=%v", item.Time, err)
		}
	}
	if manual.Items[0].Time > manual.Items[1].Time {
		t.Fatalf("manual timeline out of chronological order: %+v", manual.Items)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	assertError(request("dev", `{"text":"denied after revocation"}`, true), 403, "forbidden")
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_incident_timeline WHERE incident_id='incident'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revocation changed timeline count=%d error=%v", count, err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_incident_timeline(id,incident_id,actor_id,text)
		SELECT 'bulk-'||g,'incident',$1,'bulk note' FROM generate_series(1,499) g`, actorID); err != nil {
		t.Fatal(err)
	}
	assertError(read("dev", "incident", true), 503, "pagination_required")
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='events' AND action='view'`); err != nil {
		t.Fatal(err)
	}
	assertError(read("dev", "incident", true), 404, "not_found")
}
