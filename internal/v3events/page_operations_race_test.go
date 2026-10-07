package v3events

import (
	"context"
	"encoding/json"
	"fmt"
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

type pageRaceFixture struct {
	t              *testing.T
	db             *pgxpool.Pool
	api            *httpapi.Server
	actor, other   core.User
	token, appName string
}

func newPageRaceFixture(t *testing.T) *pageRaceFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random disposable schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_page_race_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	cfg.MaxConns = 8
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "race-admin@example.invalid", "isolated-race-password"); err != nil {
		t.Fatal(err)
	}
	actor, token, err := st.Login(ctx, "race-admin@example.invalid", "isolated-race-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other Human", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	f := &pageRaceFixture{t: t, db: db, actor: actor, other: other, token: token, appName: schema}
	f.exec(`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'admin')`, other.ID)
	f.exec(`INSERT INTO applications(id,key,name,environment) VALUES('app','app','App','dev'); INSERT INTO application_environments(id,application_id,key,name) VALUES('app-dev','app','dev','Dev');INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app','dev');INSERT INTO ops_incidents(id,source_id,application_id,environment_id,fingerprint,title,severity,status,alert_status,starts_at,labels) VALUES('alert','fixture','app','app-dev','f','Assignment alert','warning','open','firing',now(),'{}')`)
	policy := &rbac.V3Policy{Store: st}
	svc := New(db, func(ctx context.Context, user core.User, app, env, action string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	}, func(ctx context.Context, tx pgx.Tx, user core.User, app, env, action string) error {
		return policy.AuthorizeWithTx(ctx, tx, user, rbac.V3Decision{Module: "events", Action: action, ApplicationID: app, EnvironmentID: env})
	})
	api := httpapi.New(st, nil, true)
	svc.RegisterPageOperations(api)
	svc.RegisterV3Incidents(api)
	api.Register("GET /api/v1/test-assignment-notifications", "viewer", func(w http.ResponseWriter, r *http.Request) {
		tx, err := svc.pageTx(r)
		if err != nil {
			respondErr(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		items, err := svc.readAssignmentNotifications(r, tx, time.Now().Add(-24*time.Hour))
		if err != nil {
			respondErr(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			respondErr(w, err)
			return
		}
		httpapi.WriteJSON(w, 200, map[string]any{"items": items})
	})
	api.MarkV3ObjectRoute("GET /api/v1/test-assignment-notifications", "events", "view")
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}}, policy, st.Session, nil); err != nil {
		t.Fatal(err)
	}
	f.api = api
	return f
}
func (f *pageRaceFixture) exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(f.t.Context(), sql, args...); err != nil {
		f.t.Fatal(err)
	}
}
func (f *pageRaceFixture) call(method, path string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(raw)))
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.token})
	r.Header.Set("X-AIOps-Environment", "dev")
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}
func (f *pageRaceFixture) want(w *httptest.ResponseRecorder, status int) {
	f.t.Helper()
	if w.Code != status {
		f.t.Fatalf("status=%d expected=%d body=%s", w.Code, status, w.Body.String())
	}
}
func (f *pageRaceFixture) assign(kind, recipient string) {
	f.t.Helper()
	f.want(f.call("POST", "/assignments", map[string]any{"item_kind": kind, "item_id": "alert", "assignee": recipient}), 200)
}
func (f *pageRaceFixture) notificationIDs(w *httptest.ResponseRecorder) []string {
	f.t.Helper()
	f.want(w, 200)
	var body struct {
		Items []persistedInboxItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		f.t.Fatal(err)
	}
	ids := []string{}
	for _, v := range body.Items {
		ids = append(ids, v.ID)
	}
	return ids
}

func TestParticipantsAppendDeduplicatesBeforeTotalLimit(t *testing.T) {
	f := newPageRaceFixture(t)
	names := []string{}
	for i := 0; i < 50; i++ {
		names = append(names, fmt.Sprintf("Person %02d", i))
	}
	f.want(f.call("POST", "/incidents/alert/participants", map[string]any{"names": names}), 200)
	w := f.call("POST", "/incidents/alert/participants", map[string]any{"names": []string{"Person 00", "Person 00"}})
	f.want(w, 200)
	var out struct {
		Participants []string `json:"participants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Participants) != 50 {
		t.Fatal("idempotent append lost full list", err, len(out.Participants))
	}
	f.want(f.call("POST", "/incidents/alert/participants", map[string]any{"names": []string{"Person 51"}}), 409)
	f.want(f.call("POST", "/incidents/alert/participants", map[string]any{"names": append(names, "Person 00")}), 422)
	var total int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_incident_participants WHERE incident_id='alert'`).Scan(&total); err != nil || total != 50 {
		t.Fatal("rejected append persisted", err, total)
	}
}

func TestIncidentAliasAssignmentUsesAlertIdentityAndCurrentEpoch(t *testing.T) {
	f := newPageRaceFixture(t)
	f.assign("incident", f.actor.ID)
	var owner, kind string
	if err := f.db.QueryRow(t.Context(), `SELECT assignee_id FROM ops_incidents WHERE id='alert'`).Scan(&owner); err != nil || owner != f.actor.ID {
		t.Fatal("legacy alias did not update real alert owner", err, owner)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT item_kind FROM v3_item_assignments WHERE item_id='alert'`).Scan(&kind); err != nil || kind != "alert" {
		t.Fatal("alias created duplicate assignment identity", err, kind)
	}
	first := f.notificationIDs(f.call("GET", "/test-assignment-notifications", nil))
	if len(first) != 1 {
		t.Fatal(first)
	}
	f.assign("alert", f.actor.ID)
	same := f.notificationIDs(f.call("GET", "/test-assignment-notifications", nil))
	if len(same) != 1 || same[0] != first[0] {
		t.Fatal("same recipient retry reset epoch", first, same)
	}
	f.assign("incident", f.other.ID)
	if items := f.notificationIDs(f.call("GET", "/test-assignment-notifications", nil)); len(items) != 0 {
		t.Fatal("former recipient still notified", items)
	}
	f.assign("incident", f.actor.ID)
	again := f.notificationIDs(f.call("GET", "/test-assignment-notifications", nil))
	if len(again) != 1 || again[0] == first[0] {
		t.Fatal("A to B to A restored historical notification", first, again)
	}
	var history int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_assignment_notifications WHERE item_kind='alert' AND item_id='alert'`).Scan(&history); err != nil || history != 3 {
		t.Fatal("history or idempotency corrupted", err, history)
	}
}

func TestAssignmentNotificationRechecksAfterObjectLockWait(t *testing.T) {
	f := newPageRaceFixture(t)
	f.assign("alert", f.actor.ID)
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM ops_incidents WHERE id='alert' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- f.call("GET", "/test-assignment-notifications", nil) }()
	// Observe the exact blocked object read, proving discovery saw the old
	// assignment before the writer committed. Do not rely on a scheduling sleep.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if err = f.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE '%FOR SHARE OF i%')`, f.appName).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		tx.Rollback(t.Context())
		t.Fatal("reader did not reach the blocked object-lock read")
	}
	var epoch time.Time
	if err = tx.QueryRow(t.Context(), `SELECT GREATEST(clock_timestamp(),updated_at+interval '1 microsecond') FROM v3_item_assignments WHERE item_kind='alert' AND item_id='alert' FOR UPDATE`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE v3_item_assignments SET assignee_id=$1,updated_at=$2 WHERE item_kind='alert' AND item_id='alert';`, f.other.ID, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `UPDATE ops_incidents SET assignee_id=$1 WHERE id='alert'`, f.other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO v3_assignment_notifications(id,item_kind,item_id,recipient_id,actor_id,created_at) VALUES($1,'alert','alert',$2,$3,$4)`, store.ID(), f.other.ID, f.actor.ID, epoch); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		if items := f.notificationIDs(w); len(items) != 0 {
			t.Fatal("stale assignment notification escaped locked recheck", items)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not finish after writer commit")
	}
}
