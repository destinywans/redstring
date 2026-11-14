package v3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	exec(`INSERT INTO teams(id,name) VALUES('owner','Owner'),('other','Other')`)
	exec(`INSERT INTO applications(id,name,key,environment,owner_team_id) VALUES('app','App','app','dev','owner')`)
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
	openWithContext := func(ctx context.Context, id, env, session, body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/incidents/"+id+"/investigation", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: session})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	open := func(id, env, session, body string) (int, map[string]any) {
		return openWithContext(ctx, id, env, session, body)
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

	// An event writer without canvas.change cannot open either kind of incident.
	for _, id := range []string{"incident", "INC-1"} {
		if status, body := open(id, "dev", token, `{}`); status != 403 {
			t.Fatalf("event writer without canvas.change for %s: %d %v", id, status, body)
		}
	}
	// canvas.view and another team's canvas.change do not grant creation.
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('event-operator','canvas','view','application','app'),('event-operator','canvas','change','team','other')`)
	for _, id := range []string{"incident", "INC-1"} {
		if status, body := open(id, "dev", token, `{}`); status != 403 {
			t.Fatalf("event writer with canvas.view and unrelated team canvas.change for %s: %d %v", id, status, body)
		}
	}
	// A denied writer must not wait for a target row locked by another request.
	t.Run("canvas denial precedes the incident lock", func(t *testing.T) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, `SELECT id FROM ops_incidents WHERE id='incident' FOR UPDATE;
 SELECT id FROM v3_incident_cases WHERE id='INC-1' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"incident", "INC-1"} {
			callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			status, body := openWithContext(callCtx, id, "dev", token, `{}`)
			cancel()
			if status != 403 {
				t.Fatalf("unauthorized request waited for the row %s: %d %v", id, status, body)
			}
		}
	})
	if count(`SELECT count(*) FROM investigations`) != 0 ||
		count(`SELECT count(*) FROM ops_incidents WHERE id='incident' AND status='open' AND investigation_id=''`) != 1 ||
		count(`SELECT count(*) FROM v3_incident_cases WHERE id='INC-1' AND investigation_id IS NULL`) != 1 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.incident.investigation_opened'`) != 0 {
		t.Fatal("a canvas-unauthorized request changed investigation, incident or audit state")
	}
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('event-operator','canvas','change','application','app')`)

	t.Run("rechecks canvas grant after waiting for the incident lock", func(t *testing.T) {
		tx, err := db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SELECT id FROM ops_incidents WHERE id='incident' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		type result struct {
			status int
			body   map[string]any
		}
		done := make(chan result, 1)
		go func() {
			status, body := openWithContext(callCtx, "incident", "dev", token, `{}`)
			done <- result{status, body}
		}()
		// The request must have passed admission and be waiting for the
		// incident row before the other transaction revokes its grant.
		waitCtx, stopWaiting := context.WithTimeout(ctx, 4*time.Second)
		defer stopWaiting()
		for {
			var pid uint32
			err := db.QueryRow(waitCtx, `SELECT pid FROM pg_stat_activity
 WHERE $1=ANY(pg_blocking_pids(pid)) AND position('FOR UPDATE OF i' in query)>0 LIMIT 1`,
				tx.Conn().PgConn().PID()).Scan(&pid)
			if err == nil {
				break
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			select {
			case <-waitCtx.Done():
				t.Fatal("investigation did not wait for the target row")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if _, err = tx.Exec(ctx, `DELETE FROM v3_role_grants
 WHERE role_id='event-operator' AND module='canvas' AND action='change' AND scope_kind='application'`); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-done:
			if got.status != 403 {
				t.Fatalf("opened an investigation with a revoked grant: %d %v", got.status, got.body)
			}
		case <-callCtx.Done():
			t.Fatal("investigation request did not finish after the row was released")
		}
		if count(`SELECT count(*) FROM investigations`) != 0 ||
			count(`SELECT count(*) FROM management_audit WHERE action='v3.incident.investigation_opened'`) != 0 {
			t.Fatal("the revoked request created an investigation or success audit")
		}
	})
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('event-operator','canvas','change','application','app')`)

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
	// A linked investigation is opened through its link; POST replay requires the same grants as creation.
	if status, again := open("incident", "dev", readOnly, `{}`); status != 403 {
		t.Fatalf("read-only reuse: %d %v", status, again)
	}
	exec(`DELETE FROM v3_role_grants WHERE role_id='event-operator' AND module='canvas' AND action='change' AND scope_kind='application'`)
	if status, again := open("incident", "dev", token, `{}`); status != 403 {
		t.Fatalf("reuse after canvas.change revocation: %d %v", status, again)
	}
	// The existing event link is available through the unchanged read endpoint.
	r := httptest.NewRequest(http.MethodGet, "/api/v1/incidents/incident", nil)
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	var detail map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || w.Code != 200 || detail["investigation_id"] != investigation {
		t.Fatalf("linked incident navigation after write grant revocation: %d %s, %v", w.Code, w.Body.String(), err)
	}
	exec(`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('event-operator','canvas','change','team','owner')`)

	// A manual incident using its current owner-team grant.
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
	// Once the app moves teams, the former owner's grant no longer allows replay.
	exec(`UPDATE applications SET owner_team_id=NULL WHERE id='app'`)
	if status, again := open("INC-1", "dev", token, `{}`); status != 403 {
		t.Fatalf("former owner team replay: %d %v", status, again)
	}
	exec(`UPDATE applications SET owner_team_id='owner' WHERE id='app'`)

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
