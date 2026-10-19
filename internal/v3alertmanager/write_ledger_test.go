package v3alertmanager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testSilenceLedger(t *testing.T) (*pgxpool.Pool, *DatabaseSilenceLedger, core.User) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; random isolated schema only")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_silence_ledger_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.MaxConns = 3
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-silence-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.CreateUser(ctx, core.User{Email: "writer@example.invalid", Name: "Writer", Role: "viewer"}, "isolated-writer-password"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO teams(id,name) VALUES('ops','Ops')`,
		`INSERT INTO applications(id,key,name,environment,owner_team_id,archived) VALUES('app-id','checkout','Checkout','test','ops',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES('app-test','app-id','test','Test',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('app-id','test')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','test'),
 ('viewer','silences','manage','team','ops')`,
	} {
		if _, err = db.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	actor, _, err := st.Login(ctx, "writer@example.invalid", "isolated-writer-password")
	if err != nil {
		t.Fatal(err)
	}
	ledger := NewDatabaseSilenceLedger(db, &rbac.V3Policy{Store: st},
		map[string]string{"test": "environment"}, map[string]map[string]string{"test": {"checkout": "app-id"}})
	ledger.Now = func() time.Time { return time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC) }
	return db, ledger, actor
}

func ledgerAppInput(key string) SilenceCreateInput {
	return SilenceCreateInput{EnvironmentID: "test", IdempotencyKey: key, Comment: "Investigate downstream errors",
		Duration: time.Hour, Matchers: []Matcher{{Name: "application", Value: "checkout", IsEqual: true},
			{Name: "alertname", Value: "DownstreamTimeout", IsEqual: true}}}
}

func observedSilenceForCreate(op SilenceCreateOperation, id string) Silence {
	value := Silence{ID: id, Matchers: append([]Matcher(nil), op.Request.Matchers...),
		StartsAt: op.Request.StartsAt.Add(169 * time.Millisecond), EndsAt: op.Request.EndsAt,
		UpdatedAt: op.Request.StartsAt.Add(time.Second), CreatedBy: op.Request.CreatedBy,
		Comment: op.Request.Comment, Annotations: map[string]string{silenceOperationAnnotation: op.ID}}
	value.Status.State = "active"
	return value
}

func testReconcileClient(t *testing.T, status int, body any) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/silences" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := New(map[string]Endpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestSilenceCreateLedgerIdempotencyAuditAndUnknownReconciliation(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	in := ledgerAppInput("retry-key-001")
	op, err := ledger.ReserveCreate(ctx, actor, in)
	if err != nil || op.State != SilenceCreateReserved || op.ApplicationID != "app-id" ||
		op.Request.Annotations[silenceOperationAnnotation] != op.ID || len(op.Request.Matchers) != 3 ||
		!op.Request.EndsAt.Equal(op.Request.StartsAt.Add(time.Hour)) {
		t.Fatalf("reserve op=%+v err=%v", op, err)
	}
	ledger.Now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }
	replay, err := ledger.ReserveCreate(ctx, actor, in)
	if err != nil || replay.ID != op.ID || !replay.Request.StartsAt.Equal(op.Request.StartsAt) {
		t.Fatalf("retry op=%+v err=%v", replay, err)
	}
	changed := in
	changed.Comment = "different reason"
	if _, err = ledger.ReserveCreate(ctx, actor, changed); !errors.Is(err, ErrSilenceCreateConflict) {
		t.Fatalf("same key changed request: %v", err)
	}
	ledger.Now = func() time.Time { return op.Request.StartsAt.Add(10 * time.Second) }
	dispatch, err := ledger.beginCreate(ctx, actor, op.ID)
	if err != nil || dispatch.State != SilenceCreateDispatching {
		t.Fatalf("dispatch op=%+v err=%v", dispatch, err)
	}
	if _, err = ledger.beginCreate(ctx, actor, op.ID); !errors.Is(err, ErrSilenceCreateBusy) {
		t.Fatalf("second dispatch: %v", err)
	}
	if err = ledger.markCreateUnknown(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if err = ledger.markCreateUnknown(ctx, op.ID); err != nil {
		t.Fatalf("unknown replay: %v", err)
	}
	const providerID = "11111111-2222-3333-4444-555555555555"
	observed := observedSilenceForCreate(op, providerID)
	unrelated := observed
	unrelated.Annotations = map[string]string{silenceOperationAnnotation: "other-operation"}
	client := testReconcileClient(t, http.StatusOK, []Silence{unrelated, observed})
	confirmed, err := ledger.reconcileCreate(ctx, op.ID, client)
	if err != nil || confirmed.State != SilenceCreateConfirmed || confirmed.UpstreamID != providerID {
		t.Fatalf("reconcile op=%+v err=%v", confirmed, err)
	}
	if _, err = ledger.reconcileCreate(ctx, op.ID, client); err != nil {
		t.Fatal(err)
	}
	replay, err = ledger.FindCreateByKey(ctx, actor, in.IdempotencyKey)
	if err != nil || replay.State != SilenceCreateConfirmed || replay.UpstreamID != providerID {
		t.Fatalf("find op=%+v err=%v", replay, err)
	}
	var operations, audits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_silence_create_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action LIKE 'v3.silence_create.%'`, op.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || audits != 4 {
		t.Fatalf("operations=%d audits=%d", operations, audits)
	}
}

func TestSilenceCreateLedgerExpiresDelayedDispatch(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	op, err := ledger.ReserveCreate(ctx, actor, ledgerAppInput("expired-key-001"))
	if err != nil {
		t.Fatal(err)
	}
	ledger.Now = func() time.Time { return op.Request.StartsAt.Add(maxAlertmanagerStartShift + time.Nanosecond) }
	if _, err = ledger.beginCreate(ctx, actor, op.ID); !errors.Is(err, ErrSilenceCreateExpired) {
		t.Fatalf("late dispatch: %v", err)
	}
	var state string
	if err = db.QueryRow(ctx, `SELECT state FROM v3_silence_create_operations WHERE id=$1`, op.ID).Scan(&state); err != nil || state != SilenceCreateExpired {
		t.Fatalf("state=%s err=%v", state, err)
	}
	var audits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='v3.silence_create.expired'`, op.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("expired audits=%d err=%v", audits, err)
	}
	if _, err = ledger.beginCreate(ctx, actor, op.ID); !errors.Is(err, ErrSilenceCreateBusy) {
		t.Fatalf("expired intent redispatched: %v", err)
	}
}

func TestSilenceCreateLedgerRecordsPreNetworkExpiry(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	// Hold the reservation clock one hour in the past to simulate an executor
	// pausing after beginCreate, while the HTTP client uses the actual clock.
	frozen := time.Now().UTC().Add(-time.Hour)
	ledger.Now = func() time.Time { return frozen }
	op, err := ledger.ReserveCreate(ctx, actor, ledgerAppInput("presend-expired-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(ctx, actor, op.ID); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client, err := New(map[string]Endpoint{"test": {URL: server.URL, AllowHTTP: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateSilence(ctx, "test", op.Request); !errors.Is(err, ErrSilenceCreateExpired) || requests != 0 {
		t.Fatalf("pre-network window check err=%v requests=%d", err, requests)
	}
	if err = ledger.markCreateNotSentExpired(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if err = ledger.markCreateNotSentExpired(ctx, op.ID); err != nil {
		t.Fatalf("expired replay: %v", err)
	}
	var state string
	var audits int
	if err = db.QueryRow(ctx, `SELECT state FROM v3_silence_create_operations WHERE id=$1`, op.ID).Scan(&state); err != nil || state != SilenceCreateExpired {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND action='v3.silence_create.expired'`, op.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("expired audits=%d err=%v", audits, err)
	}
}

func TestSilenceCreateLedgerRejectsRevocationAndBroadScope(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	app := ledgerAppInput("scope-key-001")
	op, err := ledger.ReserveCreate(ctx, actor, app)
	if err != nil {
		t.Fatal(err)
	}
	broad := app
	broad.IdempotencyKey = "scope-key-002"
	broad.Matchers = []Matcher{{Name: "alertname", Value: "DownstreamTimeout", IsEqual: true}}
	if _, err = ledger.ReserveCreate(ctx, actor, broad); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("broad without environment grant: %v", err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='silences' AND action='manage' AND scope_kind='team'`); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(ctx, actor, op.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked dispatch: %v", err)
	}
	if _, err = ledger.ReserveCreate(ctx, actor, app); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked replay: %v", err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','silences','manage','environment','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.ReserveCreate(ctx, actor, broad); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("broad without view: %v", err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('viewer','silences','view','environment','test')`); err != nil {
		t.Fatal(err)
	}
	wide, err := ledger.ReserveCreate(ctx, actor, broad)
	if err != nil || wide.ApplicationID != "" || wide.State != SilenceCreateReserved {
		t.Fatalf("broad op=%+v err=%v", wide, err)
	}
	if _, err = db.Exec(ctx, `UPDATE users SET v3_identity_kind='machine' WHERE id=$1`, actor.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = ledger.beginCreate(ctx, actor, wide.ID); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("machine dispatch: %v", err)
	}
	ledger.environmentLabels["staging"] = "cluster"
	if _, err = ledger.ReserveCreate(ctx, actor, ledgerAppInput("mixed-env-label-key")); !errors.Is(err, ErrSilenceCreateInvalid) {
		t.Fatalf("mixed environment label names accepted: %v", err)
	}
}

func TestSilenceCreateLedgerAuditFailureRollsBackIntent(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_silence_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.silence_create.reserved' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_silence_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_silence_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReserveCreate(ctx, actor, ledgerAppInput("audit-key-001")); err == nil {
		t.Fatal("audit failure accepted")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_silence_create_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan intent count=%d err=%v", count, err)
	}
}

func TestSilenceCreateLedgerConcurrentReservationAndRetiredApp(t *testing.T) {
	db, ledger, actor := testSilenceLedger(t)
	ctx := t.Context()
	const workers = 4
	var wg sync.WaitGroup
	results := make([]SilenceCreateOperation, workers)
	errorsFound := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errorsFound[index] = ledger.ReserveCreate(ctx, actor, ledgerAppInput("parallel-key-001"))
		}(i)
	}
	wg.Wait()
	for i := range results {
		if errorsFound[i] != nil || results[i].ID != results[0].ID {
			t.Fatalf("parallel[%d]=%+v err=%v", i, results[i], errorsFound[i])
		}
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM v3_silence_create_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("parallel intents=%d err=%v", count, err)
	}
	if _, err := db.Exec(ctx, `UPDATE applications SET archived=true WHERE id='app-id'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.beginCreate(ctx, actor, results[0].ID); !errors.Is(err, ErrSilenceCreateNotFound) {
		t.Fatalf("retired app dispatched: %v", err)
	}
	if err := db.QueryRow(ctx, `SELECT state FROM v3_silence_create_operations WHERE id=$1`, results[0].ID).Scan(&results[0].State); err != nil || results[0].State != SilenceCreateReserved {
		t.Fatalf("retired state=%q err=%v", results[0].State, err)
	}
}
