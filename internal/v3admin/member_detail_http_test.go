package v3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMemberDetailContactHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; test creates and drops an isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_member_detail_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	reader, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES
 ('member-reader','Member Reader'),('dev-member','Dev Member'),('test-member','Test Member')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('member-reader','environment','view','environment','dev'),
 ('member-reader','members','view','environment','dev'),
 ('dev-member','environment','view','environment','dev'),
 ('dev-member','services','view','environment','dev'),
 ('test-member','environment','view','environment','test'),
 ('test-member','services','view','environment','test')`,
		`INSERT INTO teams(id,name) VALUES('ops','Operations')`,
		`INSERT INTO users(id,email,name,role,password_hash,department_id,phone,disabled) VALUES
 ('dev-user','dev@example.invalid','Dev User','viewer','','ops','old-phone',false),
 ('disabled-user','disabled@example.invalid','Disabled User','viewer','',NULL,NULL,true),
 ('test-user','test@example.invalid','Test User','viewer','',NULL,NULL,false),
 ('unassigned-user','unassigned@example.invalid','Unassigned User','viewer','',NULL,NULL,false)`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES
 ('dev-user','dev-member'),('disabled-user','dev-member'),('test-user','test-member')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'member-reader')`, reader.ID); err != nil {
		t.Fatal(err)
	}
	_, readerToken, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, id, environment, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/members/"+id, bytes.NewBufferString(body))
		r.Header.Set("X-AIOps-Environment", environment)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body.String())
		}
	}
	read := func(id, token string) memberDetail {
		t.Helper()
		w := request(http.MethodGet, id, "dev", token, "")
		check(w, 200)
		var v memberDetail
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	check(request(http.MethodGet, "dev-user", "dev", "", ""), 401)
	check(request(http.MethodGet, "dev-user", "test", readerToken, ""), 403)
	check(request(http.MethodGet, "test-user", "dev", readerToken, ""), 404)
	check(request(http.MethodGet, "test-user", "dev", adminToken, ""), 404)
	check(request(http.MethodGet, "unassigned-user", "dev", readerToken, ""), 404)
	check(request(http.MethodGet, "unassigned-user", "dev", adminToken, ""), 200)
	if disabled := read("disabled-user", readerToken); disabled.Status != "disabled" {
		t.Fatalf("disabled target missing from detail: %+v", disabled)
	}
	initial := read("dev-user", readerToken)
	if initial.Revision != "1" || initial.Department == nil || initial.Department.ID != "ops" ||
		len(initial.Roles) != 1 || initial.Roles[0].ID != "dev-member" || initial.Phone == nil || *initial.Phone != "old-phone" ||
		initial.MFA != "unknown" || initial.DirectorySyncState != "unknown" || initial.LastLogin != nil || initial.RecordedSource != "local" {
		t.Fatalf("detail mismatched stored/scoped evidence: %+v", initial)
	}
	if _, err := db.Exec(ctx, `UPDATE users SET source='feishu',external_id='feishu-dev' WHERE id='dev-user'`); err != nil {
		t.Fatal(err)
	}
	if managed := read("dev-user", readerToken); managed.RecordedSource != "feishu" || managed.Revision != "1" {
		t.Fatalf("externally recorded member detail changed unexpectedly: %+v", managed)
	}
	check(request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"1","name":"No"}`), 409)
	if _, err := db.Exec(ctx, `UPDATE users SET source='local',external_id='' WHERE id='dev-user'`); err != nil {
		t.Fatal(err)
	}
	check(request(http.MethodPatch, "dev-user", "dev", readerToken, `{"expected_revision":"1","name":"No"}`), 403)
	for _, body := range []string{
		`{}`, `[]`, `{"name":"No"}`, `{"expected_revision":"01","name":"No"}`,
		`{"expected_revision":1,"name":"No"}`, `{"expected_revision":"1","name":" "}`,
		`{"expected_revision":"1","phone":" "}`, `{"expected_revision":"1","email":"new@example.invalid"}`,
		`{"expected_revision":"1","name":"No","name":"Again"}`,
		`{"expected_revision":"1","phone":false}`, `{"expected_revision":"1","phone":null} true`,
	} {
		check(request(http.MethodPatch, "dev-user", "dev", adminToken, body), 422)
	}
	w := request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"1","name":" New Name ","phone":null}`)
	check(w, 200)
	var updated memberDetail
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "New Name" || updated.Phone != nil || updated.Revision != "2" || read("dev-user", readerToken).Revision != "2" {
		t.Fatalf("PATCH did not round-trip: %+v", updated)
	}
	check(request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"1","name":"Stale"}`), 409)
	w = request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"2","name":"New Name"}`)
	check(w, 200)
	if got := read("dev-user", readerToken).Revision; got != "2" {
		t.Fatalf("no-op bumped revision: %s", got)
	}
	var auditCount int
	var changes string
	if err := db.QueryRow(ctx, `SELECT count(*),COALESCE(max(changes::text),'') FROM management_audit WHERE action='v3.member.contact.updated' AND target='dev-user'`).Scan(&auditCount, &changes); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || strings.Contains(changes, "New Name") || strings.Contains(changes, "old-phone") {
		t.Fatalf("audit count or PII wrong: %d %s", auditCount, changes)
	}
	// Legacy and personal-profile writers update the same users columns. The
	// trigger must invalidate a V3 edit based on the old revision.
	if _, err := db.Exec(ctx, `UPDATE users SET phone='legacy-phone' WHERE id='dev-user'`); err != nil {
		t.Fatal(err)
	}
	if got := read("dev-user", readerToken).Revision; got != "3" {
		t.Fatalf("legacy writer did not advance revision: %s", got)
	}
	check(request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"2","phone":null}`), 409)
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_member_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.member.contact.updated' THEN RAISE EXCEPTION 'audit rejected'; END IF; RETURN NEW; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE TRIGGER reject_member_audit BEFORE INSERT ON management_audit FOR EACH ROW EXECUTE FUNCTION reject_member_audit()`); err != nil {
		t.Fatal(err)
	}
	check(request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"3","name":"Rolled Back"}`), 503)
	if after := read("dev-user", readerToken); after.Name != "New Name" || after.Revision != "3" {
		t.Fatalf("audit failure committed contact change: %+v", after)
	}
	if _, err := db.Exec(ctx, `DROP TRIGGER reject_member_audit ON management_audit`); err != nil {
		t.Fatal(err)
	}
	check(request(http.MethodPatch, "dev-user", "all", adminToken, `{"expected_revision":"3","name":"No"}`), 409)
	check(request(http.MethodPatch, "test-user", "dev", adminToken, `{"expected_revision":"1","name":"No"}`), 404)
	check(request(http.MethodPatch, "dev-user", "dev", adminToken, `{"expected_revision":"3","phone":" 555 "}`), 200)
	if after := read("dev-user", readerToken); after.Phone == nil || *after.Phone != "555" || after.Revision != "4" {
		t.Fatalf("final contact did not round-trip: %+v", after)
	}
	var pending sync.WaitGroup
	results := make(chan int, 2)
	for _, body := range []string{
		`{"expected_revision":"4","name":"Concurrent A"}`,
		`{"expected_revision":"4","name":"Concurrent B"}`,
	} {
		pending.Add(1)
		go func(body string) {
			defer pending.Done()
			results <- request(http.MethodPatch, "dev-user", "dev", adminToken, body).Code
		}(body)
	}
	pending.Wait()
	close(results)
	statusCounts := map[int]int{}
	for status := range results {
		statusCounts[status]++
	}
	if statusCounts[200] != 1 || statusCounts[409] != 1 || read("dev-user", readerToken).Revision != "5" {
		t.Fatalf("same revision writers both committed or failed: %v", statusCounts)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='member-reader' AND module='members'`); err != nil {
		t.Fatal(err)
	}
	check(request(http.MethodGet, "dev-user", "dev", readerToken, ""), 403)
	check(request(http.MethodPatch, "dev-user", "dev", readerToken, `{"expected_revision":"5","name":"No"}`), 403)
}
