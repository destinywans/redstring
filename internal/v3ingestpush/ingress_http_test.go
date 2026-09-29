package v3ingestpush

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

type pushFixture struct {
	db     *pgxpool.Pool
	st     *store.Store
	api    *httpapi.Server
	s      *Service
	token  string
	secret string
	cookie string
}

func setupPush(t *testing.T, rate int) pushFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_push_" + store.ID()
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
	cfg.MaxConns = 8
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-push-password"); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('admin','connectors','view','global','*'),('admin','connectors','manage','global','*'),
 ('admin','services','view','global','*'),('admin','services','change','global','*') ON CONFLICT DO NOTHING`,
		`INSERT INTO applications(id,name,key,environment) VALUES
 ('app-a','Checkout','checkout','dev'),('app-b','Payments','payments','stage')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('app-a-dev','app-a','dev','Dev'),('app-a-stage','app-a','stage','Stage'),
 ('app-b-dev','app-b','dev','Dev'),('app-b-stage','app-b','stage','Stage')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-a','dev'),('app-a','stage'),('app-b','dev'),('app-b','stage')`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	_, cookie, err := st.Login(ctx, "admin@example.invalid", "isolated-push-password")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	secretPath := filepath.Join(dir, "hmac")
	tokenPathB := filepath.Join(dir, "token-b")
	secretPathB := filepath.Join(dir, "hmac-b")
	token, secret := strings.Repeat("t", 32), strings.Repeat("h", 40)
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPathB, []byte(strings.Repeat("u", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPathB, []byte(strings.Repeat("j", 40)), 0600); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "stage"}}
	s, err := New(db, instance, Config{PublicHost: "push.example.invalid", Sources: []Source{
		{ID: "source-a", Provider: "generic", ApplicationID: "app-a", EnvironmentID: "dev", TokenFile: tokenPath, HMACSecretFile: secretPath, Types: []string{"change", "alert"}, RatePerMinute: rate},
		{ID: "source-b", Provider: "generic", ApplicationID: "app-b", EnvironmentID: "stage", TokenFile: tokenPathB, HMACSecretFile: secretPathB, Types: []string{"change"}},
	}}, policy)
	if err != nil {
		t.Fatal(err)
	}
	api := httpapi.New(st, nil, true)
	s.Register(api)
	if err := api.ConfigureV3Environment(instance, policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
		return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
	}); err != nil {
		t.Fatal(err)
	}
	return pushFixture{db: db, st: st, api: api, s: s, token: token, secret: secret, cookie: cookie}
}

func eventBody(typ, id string) []byte {
	state := map[string]string{"change": "succeeded", "alert": "firing", "incident": "open", "resource": "updated", "oncall": "started"}[typ]
	value := map[string]any{"schema": typ + ".v1", "event_id": id, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "subject": "checkout", "title": "Bounded event", "state": state, "attributes": map[string]string{"version": "v1"}}
	if typ == "alert" {
		value["severity"] = "warning"
	}
	body, _ := json.Marshal(value)
	return body
}

func signedPush(f pushFixture, typ, key string, body []byte) *http.Request {
	return signedPushSource("source-a", f.token, f.secret, typ, key, body)
}

func signedPushSource(sourceID, token, secret, typ, key string, body []byte) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/events/"+typ, bytes.NewReader(body))
	timestamp := fmt.Sprint(time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + sourceID + "\n" + typ + "\n" + key + "\n"))
	mac.Write(body)
	r.Header.Set("X-AIOps-Source", sourceID)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Timestamp", timestamp)
	r.Header.Set("X-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestPushAllScopeFiltersUnpermittedApplication(t *testing.T) {
	f := setupPush(t, 0)
	if got := callPush(f, signedPush(f, "change", "visible-a", eventBody("change", "visible-a"))); got.Code != 202 {
		t.Fatalf("source-a status=%d body=%s", got.Code, got.Body.String())
	}
	b := f.s.sources["source-b"]
	bToken, err := readSecret(b.TokenFile, 24)
	if err != nil {
		t.Fatal(err)
	}
	bSecret, err := readSecret(b.HMACSecretFile, 32)
	if err != nil {
		t.Fatal(err)
	}
	if got := callPush(f, signedPushSource("source-b", string(bToken), string(bSecret), "change", "hidden-b", eventBody("change", "hidden-b"))); got.Code != 202 {
		t.Fatalf("source-b status=%d body=%s", got.Code, got.Body.String())
	}
	user, err := f.st.CreateUser(t.Context(), core.User{Email: "push-reader@example.invalid", Name: "Push Reader", Role: "viewer"}, "isolated-push-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES('push-scoped','Push scoped')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('push-scoped','environment','view','environment','dev'),
 ('push-scoped','environment','view','environment','stage'),
 ('push-scoped','connectors','view','global','*'),
 ('push-scoped','services','view','application','app-a')`,
	} {
		if _, err := f.db.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(t.Context(), `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'push-scoped')`, user.ID); err != nil {
		t.Fatal(err)
	}
	_, cookie, err := f.st.Login(t.Context(), user.Email, "isolated-push-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/ingest/push", nil)
	r.Header.Set("X-AIOps-Environment", "all")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: cookie})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("scoped view status=%d body=%s", w.Code, w.Body.String())
	}
	var view pushView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Types[1].Today != "1" || len(view.Deliveries) != 1 || view.Deliveries[0].Source != "source-a" || strings.Contains(w.Body.String(), "source-b") {
		t.Fatalf("scoped view leaked source-b: %+v", view)
	}
}

func callPush(f pushFixture, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.s.ingest(w, r)
	return w
}

func TestPushHTTPAuthenticationIdempotencyScopeAndAudit(t *testing.T) {
	f := setupPush(t, 0)
	body := eventBody("change", "deploy-1")
	request := func(typ, key string, body []byte) *httptest.ResponseRecorder {
		return callPush(f, signedPush(f, typ, key, body))
	}
	if got := request("change", "key-1", body); got.Code != 202 {
		t.Fatalf("accepted status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request("change", "key-1", body); got.Code != 409 {
		t.Fatalf("duplicate status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request("alert", "key-1", eventBody("alert", "alert-1")); got.Code != 202 {
		t.Fatalf("same key across type status=%d body=%s", got.Code, got.Body.String())
	}
	if got := request("change", "key-2", body); got.Code != 409 {
		t.Fatalf("same event identity status=%d body=%s", got.Code, got.Body.String())
	}
	invalid := signedPush(f, "change", "invalid-1", []byte(`{"schema":"change.v1"}`))
	if got := callPush(f, invalid); got.Code != 422 {
		t.Fatalf("invalid status=%d body=%s", got.Code, got.Body.String())
	}
	badToken := signedPush(f, "change", "key-3", eventBody("change", "deploy-3"))
	badToken.Header.Set("Authorization", "Bearer wrong")
	if got := callPush(f, badToken); got.Code != 401 {
		t.Fatalf("bad token status=%d", got.Code)
	}
	badSignature := signedPush(f, "change", "key-4", eventBody("change", "deploy-4"))
	badSignature.Header.Set("X-Signature", "sha256="+strings.Repeat("0", 64))
	if got := callPush(f, badSignature); got.Code != 401 {
		t.Fatalf("bad signature status=%d", got.Code)
	}
	spoofed := signedPush(f, "change", "key-spoof", eventBody("change", "deploy-spoof"))
	spoofed.Header.Set("X-AIOps-Source", "source-b")
	if got := callPush(f, spoofed); got.Code != 401 {
		t.Fatalf("cross-source swap status=%d", got.Code)
	}
	stale := signedPush(f, "change", "key-5", eventBody("change", "deploy-5"))
	stale.Header.Set("X-Timestamp", fmt.Sprint(time.Now().Add(-6*time.Minute).Unix()))
	if got := callPush(f, stale); got.Code != 401 {
		t.Fatalf("stale timestamp status=%d", got.Code)
	}
	var events, receipts, audits int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.push.%'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 2 || receipts != 5 || audits != 5 {
		t.Fatalf("events=%d receipts=%d audits=%d", events, receipts, audits)
	}
	// Historical records with the same app or environment, but not the exact
	// currently authorized source/environment/application tuple, stay hidden.
	if _, err := f.db.Exec(t.Context(), `INSERT INTO v3_push_receipts(source_id,provider,application_id,environment_id,event_type,code,result)
 VALUES('source-a','generic','app-b','dev','change',202,'accepted'),('source-a','generic','app-a','stage','change',202,'accepted')`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/ingest/push", nil)
	r.Header.Set("X-AIOps-Environment", "all")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("view status=%d body=%s", w.Code, w.Body.String())
	}
	var view pushView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Availability != "configured_unverified" || len(view.Deliveries) != 5 || view.Types[1].Today != "1" || view.Types[0].Today != "1" {
		t.Fatalf("view leaked cross tuple or miscounted: %+v", view)
	}
	if strings.Contains(w.Body.String(), f.token) || strings.Contains(w.Body.String(), f.secret) {
		t.Fatal("view echoed secret")
	}
}

func TestPushRejectsSharedSecretsAtStartupAndRotation(t *testing.T) {
	f := setupPush(t, 0)
	instance := httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "stage"}}
	policy := &rbac.V3Policy{Store: f.st}
	a, b := f.s.sources["source-a"], f.s.sources["source-b"]
	if _, err := New(f.db, instance, Config{Sources: []Source{a, b}}, policy); err != nil {
		t.Fatalf("distinct credentials refused: %v", err)
	}
	sharedPath := b
	sharedPath.TokenFile = a.TokenFile
	if _, err := New(f.db, instance, Config{Sources: []Source{a, sharedPath}}, policy); err == nil {
		t.Fatal("shared token path accepted")
	}
	sharedContent := b
	if err := os.WriteFile(sharedContent.TokenFile, []byte(f.token), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f.db, instance, Config{Sources: []Source{a, sharedContent}}, policy); err == nil {
		t.Fatal("different files with shared token content accepted")
	}
	if got := callPush(f, signedPush(f, "change", "rotated-convergence", eventBody("change", "rotated-convergence"))); got.Code != 503 {
		t.Fatalf("rotation convergence status=%d", got.Code)
	}
	if err := os.WriteFile(sharedContent.TokenFile, []byte(strings.Repeat("u", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "linked-token")
	if err := os.Symlink(a.TokenFile, symlink); err != nil {
		t.Fatal(err)
	}
	sharedContent.TokenFile = symlink
	if _, err := New(f.db, instance, Config{Sources: []Source{a, sharedContent}}, policy); err == nil {
		t.Fatal("symlink to an existing source credential accepted")
	}
}

func TestPushConcurrentDuplicateAndRateLimit(t *testing.T) {
	f := setupPush(t, 2)
	body := eventBody("change", "deploy-race")
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- callPush(f, signedPush(f, "change", "race-key", body)).Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[202] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent status counts=%v", counts)
	}
	third := callPush(f, signedPush(f, "change", "another-key", eventBody("change", "deploy-next")))
	if third.Code != 429 || third.Header().Get("Retry-After") != "60" {
		t.Fatalf("rate status=%d retry=%q body=%s", third.Code, third.Header().Get("Retry-After"), third.Body.String())
	}
	var receipts int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_receipts`).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("rate-limit wrote receipt: count=%d err=%v", receipts, err)
	}
}

func TestPushTestIsSyntheticAndSecretRotation(t *testing.T) {
	f := setupPush(t, 0)
	r := httptest.NewRequest("POST", "/api/v1/ingest/push/test", strings.NewReader(`{"type":"change"}`))
	r.Header.Set("X-AIOps-Environment", "dev")
	r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("test status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_events WHERE synthetic`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("test event count=%d err=%v", count, err)
	}
	get := httptest.NewRequest("GET", "/api/v1/ingest/push", nil)
	get.Header.Set("X-AIOps-Environment", "dev")
	get.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
	viewResponse := httptest.NewRecorder()
	f.api.ServeHTTP(viewResponse, get)
	var view pushView
	if err := json.Unmarshal(viewResponse.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Types[1].Today != "0" || len(view.Deliveries) != 1 || view.Deliveries[0].Result != "test_accepted" {
		t.Fatalf("test event masquerades as real event: %+v", view)
	}
	tokenPath := f.s.sources["source-a"].TokenFile
	if err := os.Chmod(tokenPath, 0644); err != nil {
		t.Fatal(err)
	}
	if got := callPush(f, signedPush(f, "change", "rotate-1", eventBody("change", "rotate-1"))); got.Code != 503 {
		t.Fatalf("public token file status=%d", got.Code)
	}
	if err := os.Chmod(tokenPath, 0600); err != nil {
		t.Fatal(err)
	}
	newToken := strings.Repeat("n", 32)
	if err := os.WriteFile(tokenPath, []byte(newToken), 0600); err != nil {
		t.Fatal(err)
	}
	if got := callPush(f, signedPush(f, "change", "rotate-2", eventBody("change", "rotate-2"))); got.Code != 401 {
		t.Fatalf("old token after rotation status=%d", got.Code)
	}
	f.token = newToken
	if got := callPush(f, signedPush(f, "change", "rotate-3", eventBody("change", "rotate-3"))); got.Code != 202 {
		t.Fatalf("new token after rotation status=%d body=%s", got.Code, got.Body.String())
	}
}

func TestPushAllFiveTypesAndExpiredKeyReuse(t *testing.T) {
	f := setupPush(t, 0)
	source := f.s.sources["source-a"]
	source.Types = []string{"alert", "change", "incident", "resource", "oncall"}
	f.s.sources["source-a"] = source
	for _, typ := range source.Types {
		w := callPush(f, signedPush(f, typ, "shared-key", eventBody(typ, typ+"-1")))
		if w.Code != 202 {
			t.Fatalf("%s status=%d body=%s", typ, w.Code, w.Body.String())
		}
	}
	if _, err := f.db.Exec(t.Context(), `UPDATE v3_push_idempotency SET expires_at=now()-interval '1 second'
 WHERE source_id='source-a' AND event_type='change' AND key_hash=$1`, hashKey("shared-key")); err != nil {
		t.Fatal(err)
	}
	w := callPush(f, signedPush(f, "change", "shared-key", eventBody("change", "change-2")))
	if w.Code != 202 {
		t.Fatalf("expired 24h key could not be reused: %d %s", w.Code, w.Body.String())
	}
	var events int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_events`).Scan(&events); err != nil || events != 6 {
		t.Fatalf("all types events=%d err=%v", events, err)
	}
}

func TestPushAuditFailureRollsBackEventReceiptAndFence(t *testing.T) {
	f := setupPush(t, 0)
	if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION push_fail_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.push.accepted' THEN RAISE EXCEPTION 'test audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER push_fail_audit AFTER INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION push_fail_audit()`); err != nil {
		t.Fatal(err)
	}
	w := callPush(f, signedPush(f, "change", "audit-fail", eventBody("change", "audit-fail")))
	if w.Code != 503 {
		t.Fatalf("audit failure status=%d body=%s", w.Code, w.Body.String())
	}
	for _, table := range []string{"v3_push_source_bindings", "v3_push_events", "v3_push_receipts", "v3_push_idempotency", "v3_push_rate_windows"} {
		var count int
		if err := f.db.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func TestPushCurrentMappingRequired(t *testing.T) {
	f := setupPush(t, 0)
	if _, err := f.db.Exec(t.Context(), `UPDATE application_environments SET archived=true WHERE id='app-a-dev'`); err != nil {
		t.Fatal(err)
	}
	w := callPush(f, signedPush(f, "change", "stale-mapping", eventBody("change", "stale-mapping")))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "mapping_unavailable") {
		t.Fatalf("stale mapping status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale mapping wrote event count=%d err=%v", count, err)
	}
}

func TestPushSourceIdentityCannotBeRemapped(t *testing.T) {
	f := setupPush(t, 0)
	if got := callPush(f, signedPush(f, "change", "original", eventBody("change", "original"))); got.Code != 202 {
		t.Fatalf("first accept status=%d body=%s", got.Code, got.Body.String())
	}
	changed := f.s.sources["source-a"]
	changed.ApplicationID = "app-b" // app-b is also mapped to dev.
	f.s.sources["source-a"] = changed
	if got := callPush(f, signedPush(f, "change", "remapped", eventBody("change", "remapped"))); got.Code != 503 || !strings.Contains(got.Body.String(), "mapping_unavailable") {
		t.Fatalf("source reassignment status=%d body=%s", got.Code, got.Body.String())
	}
	var count int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM v3_push_events`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source remap wrote event: count=%d err=%v", count, err)
	}
}

func TestPushTestReauthorizesAfterQueuedGrantRevocation(t *testing.T) {
	f := setupPush(t, 0)
	ctx := t.Context()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(752018992)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int32
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/api/v1/ingest/push/test", strings.NewReader(`{"type":"change"}`))
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: f.cookie})
		w := httptest.NewRecorder()
		f.api.ServeHTTP(w, r)
		result <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE state='active' AND wait_event_type='Lock' AND $1=ANY(pg_blocking_pids(pid))
 AND query LIKE '%pg_advisory_xact_lock_shared%')`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("test request did not reach authority lock")
	}
	if _, err := blocker.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='admin' AND module='connectors' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-result
	if w.Code != 403 {
		t.Fatalf("revoked test still wrote: status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM v3_push_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked test event count=%d err=%v", count, err)
	}
}
