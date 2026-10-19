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

// PA-88: an incident opens its investigation once; later requests reuse it.
func TestOpenInvestigationFromIncident(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test uses a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_incident_investigation_" + store.ID()
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
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-investigation-password"); err != nil {
		t.Fatal(err)
	}
	login := func(email string) (core.User, string) {
		t.Helper()
		user, err := st.CreateUser(ctx, core.User{Email: email, Name: email, Role: "viewer"}, "isolated-user-password")
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := st.Login(ctx, email, "isolated-user-password")
		if err != nil {
			t.Fatal(err)
		}
		return user, token
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DELETE FROM v3_role_grants WHERE role_id='viewer'`)
	exec(`INSERT INTO rbac_roles(id,name) VALUES('event-viewer','Event Viewer'),('event-operator','Event Operator')`)
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),('viewer','environment','view','environment','test'),
 ('event-viewer','events','view','application','app'),
 ('event-operator','events','view','application','app'),('event-operator','events','change','application','app')`)
	operator, token := login("operator@example.invalid")
	reader, readOnly := login("reader@example.invalid")
	_, outsider := login("outsider@example.invalid")
	exec(`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'event-operator'),($2,'event-viewer')`, operator.ID, reader.ID)
	exec(`INSERT INTO applications(id,name,key,environment) VALUES('app','App','app','dev')`)
	exec(`INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev')`)
	exec(`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev')`)
	start := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Second)
	exec(`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,summary,starts_at)
 VALUES('incident','lab','app','app-dev','incident','LabHigh5xxRate','critical','firing','checkout-api 5xx above 5%',$1)`, start)
	exec(`INSERT INTO v3_incident_cases(id,application_id,environment_id,environment_key,title,severity,impact,actor_id,request_key,request_hash,created_at,updated_at)
 VALUES('INC-1','app','app-dev','dev','Manual checkout incident','P2','Checkout fails for some users',$1,'request-key-1',$2,$3,$3)`,
		operator.ID, strings.Repeat("a", 64), start)
	// A manual incident links the alerts it was confirmed from.
	exec(`INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,alert_status,starts_at)
 VALUES('case-alert','lab','app','app-dev','case-alert','CheckoutErrors','warning','firing',$1)`, start)
	exec(`INSERT INTO v3_incident_case_alerts(case_id,alert_id) VALUES('INC-1','case-alert')`)
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
		})
	api := httpapi.New(st, nil, true)
	service.RegisterV3Incidents(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	open := func(id, env, session, body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/incidents/"+id+"/investigation", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// PA-252: opening one writes (an investigation, the incident's status), so
	// events.view alone is refused with 403 and nothing is created; the V3
	// route skips the legacy "operator" minimum role, so the handler checks.
	for _, id := range []string{"incident", "INC-1"} {
		if status, body := open(id, "dev", readOnly, `{}`); status != 403 {
			t.Fatalf("read-only %s: %d %v", id, status, body)
		}
	}
	if count(`SELECT count(*) FROM investigations`) != 0 ||
		count(`SELECT count(*) FROM ops_incidents WHERE id='incident' AND status='open' AND investigation_id=''`) != 1 ||
		count(`SELECT count(*) FROM v3_incident_cases WHERE id='INC-1' AND investigation_id IS NULL`) != 1 {
		t.Fatal("a read-only request opened an investigation")
	}

	// An alert incident: a new investigation for its service and environment.
	status, body := open("incident", "dev", token, `{}`)
	investigation, _ := body["investigation_id"].(string)
	if status != 200 || !strings.HasPrefix(investigation, "INV-") || body["id"] != "incident" || body["alert_started_at"] == nil {
		t.Fatalf("open: %d %v", status, body)
	}
	var app, env, title, goal, creator, incidentStatus string
	if err := db.QueryRow(ctx, `SELECT i.application_id,i.environment_id,i.title,i.goal,i.created_by,o.status
 FROM investigations i JOIN ops_incidents o ON o.investigation_id=i.id WHERE o.id='incident'`).
		Scan(&app, &env, &title, &goal, &creator, &incidentStatus); err != nil {
		t.Fatal(err)
	}
	if app != "app" || env != "app-dev" || title != "LabHigh5xxRate" || goal != "checkout-api 5xx above 5%" || creator != operator.ID ||
		incidentStatus != "investigating" {
		t.Fatalf("investigation: %s %s %s %s %s %s", app, env, title, goal, creator, incidentStatus)
	}
	if count(`SELECT count(*) FROM branches WHERE investigation_id=$1 AND parent_id IS NULL`, investigation) != 1 ||
		count(`SELECT count(*) FROM context_revisions c JOIN branches b ON b.id=c.branch_id
 WHERE b.investigation_id=$1 AND c.provenance->>'kind'='incident' AND c.provenance->>'incident_id'='incident'`, investigation) != 1 ||
		count(`SELECT count(*) FROM events WHERE investigation_id=$1 AND type='investigation.created'`, investigation) != 1 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.incident.investigation_opened' AND target='incident'`) != 1 {
		t.Fatal("investigation structure or audit missing")
	}
	// PA-189: the detail links the validated investigation; an alert has no
	// case alerts of its own, and the investigation is no longer unavailable.
	links := func(body map[string]any) string {
		raw, _ := json.Marshal(body["links"])
		return string(raw)
	}
	// body is decoded into a map, so the encoder sorts the keys: compare in that order.
	if got, want := links(body), `[{"href":"/canvas/`+investigation+`","kind":"investigation","label":"LabHigh5xxRate"}]`; got != want {
		t.Fatalf("alert links: %s, want %s", got, want)
	}
	if raw, _ := json.Marshal(body["unavailable"]); strings.Contains(string(raw), "links.investigation") {
		t.Fatalf("alert unavailable: %s", raw)
	}
	// Again: the same investigation, nothing new.
	if status, again := open("incident", "dev", token, `{}`); status != 200 || again["investigation_id"] != investigation ||
		count(`SELECT count(*) FROM investigations`) != 1 {
		t.Fatalf("reuse: %d %v", status, again)
	}
	// A linked investigation is opened through its link; the POST still needs events.change.
	if status, again := open("incident", "dev", readOnly, `{}`); status != 403 {
		t.Fatalf("read-only reuse: %d %v", status, again)
	}
	// A manual incident: titled after it, with its impact as the goal.
	status, body = open("INC-1", "dev", token, `{}`)
	caseInvestigation, _ := body["investigation_id"].(string)
	if status != 200 || !strings.HasPrefix(caseInvestigation, "INV-") || caseInvestigation == investigation {
		t.Fatalf("case: %d %v", status, body)
	}
	if count(`SELECT count(*) FROM investigations WHERE id=$1 AND title='Manual checkout incident' AND goal='Checkout fails for some users'`, caseInvestigation) != 1 {
		t.Fatal("case investigation")
	}
	// A case links its investigation and the alerts it was confirmed from.
	if got, want := links(body), `[{"href":"/canvas/`+caseInvestigation+`","kind":"investigation","label":"Manual checkout incident"},`+
		`{"href":"/incidents/case-alert","kind":"alert","label":"CheckoutErrors"}]`; got != want {
		t.Fatalf("case links: %s, want %s", got, want)
	}
	if status, again := open("INC-1", "dev", token, `{}`); status != 200 || again["investigation_id"] != caseInvestigation {
		t.Fatalf("case reuse: %d %v", status, again)
	}
	// No model is called: a model_id is refused.
	if status, body := open("incident", "dev", token, `{"model_id":"deepseek"}`); status != 422 ||
		!strings.Contains(func() string { raw, _ := json.Marshal(body); return string(raw) }(), `"code":"initial_analysis_unavailable"`) {
		t.Fatalf("model: %d %v", status, body)
	}
	// Unknown, another environment, no events.view, the read-only aggregate.
	for name, tc := range map[string]struct {
		id, env, session string
		status           int
	}{
		"unknown": {"missing", "dev", token, 404}, "other environment": {"incident", "test", token, 404},
		"no grant": {"incident", "dev", outsider, 404}, "all": {"incident", "all", token, 409},
	} {
		if status, body := open(tc.id, tc.env, tc.session, `{}`); status != tc.status {
			t.Errorf("%s: %d %v", name, status, body)
		}
	}
	if count(`SELECT count(*) FROM investigations`) != 2 {
		t.Fatal("a refused request created an investigation")
	}
}
