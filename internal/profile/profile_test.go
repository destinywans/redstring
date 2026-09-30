package profile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/store"
)

func avatarFixture(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestAvatarNormalizationBounds(t *testing.T) {
	good := avatarFixture(t)
	if b, e := normalizeAvatar(good); e != nil || len(b) == 0 {
		t.Fatalf("valid avatar: %v", e)
	}
	bad := []string{"data:image/svg+xml;base64,PHN2Zz4=", "data:image/png;base64,invalid", strings.Repeat("x", 2<<20), strings.Replace(good, "image/png", "image/jpeg", 1)}
	for _, value := range bad {
		if _, e := normalizeAvatar(value); e == nil {
			t.Fatal("invalid avatar accepted")
		}
	}
	img := image.NewGray(image.Rect(0, 0, 1025, 1))
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	if _, e := normalizeAvatar("data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())); e == nil {
		t.Fatal("oversized dimensions accepted")
	}
	if b, e := normalizeAvatar(""); e != nil || b != nil {
		t.Fatal("remove avatar")
	}
}
func TestPostgresPersonalProfileIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; all writes use one random schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "profile_test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, e := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); e != nil {
			t.Error(e)
		}
	}()
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	db := &store.Store{DB: pool}
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	password := "original-password-1"
	hash := store.PasswordHash(password)
	for _, role := range []string{"viewer", "operator", "admin"} {
		if _, e = pool.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$1||'@example.invalid',$1,$1,$2)`, role, hash); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = pool.Exec(ctx, `INSERT INTO teams(id,name) VALUES('department','Engineering');INSERT INTO team_members VALUES('department','viewer'),('department','operator');
 INSERT INTO applications(id,key,name,archived) VALUES('visible','visible','Visible',false),('archived','archived','Archived',true),('hidden','hidden','Hidden',false);
 INSERT INTO application_environments(id,application_id,key,name) SELECT id||'-env',id,'test','Test' FROM applications;
 INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('visible-host','host','Visible host','manual'),('archived-host','host','Archived host','manual'),('hidden-host','host','Hidden host','manual');
 INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id) VALUES('visible-host','visible','visible-env'),('archived-host','archived','archived-env'),('hidden-host','hidden','hidden-env');
 INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
 SELECT 'resource-'||action,'resource','visible-host',action FROM unnest(ARRAY['view','update','approve','delete']) action;
 INSERT INTO rbac_permissions(id,name) VALUES('department-view','Department resource view'),('admin-extra-view','Admin additional resource view'),('empty-permission','Empty permission');
 INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES('department-view','resource','archived-host','view'),('admin-extra-view','resource','archived-host','view'),('admin-extra-view','resource','hidden-host','view');
 INSERT INTO rbac_roles(id,name) VALUES('department-reader','Department reader');
 INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES('department-reader','department-view'),('admin','admin-extra-view'),('viewer','empty-permission');
 INSERT INTO rbac_department_roles(department_id,role_id) VALUES('department','department-reader');`); e != nil {
		t.Fatal(e)
	}
	// Login must wait on the same user-row lock as a password change. An
	// unlocked password read could otherwise recreate a session after revocation.
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT id FROM users WHERE id='viewer' FOR UPDATE`); e != nil {
		t.Fatal(e)
	}
	loginDone := make(chan error, 1)
	go func() { _, _, err := db.Login(ctx, "viewer@example.invalid", password); loginDone <- err }()
	deadline := time.Now().Add(3 * time.Second)
	waiting := false
	for time.Now().Before(deadline) {
		if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'SELECT%FOR UPDATE%')`, schema).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("login did not acquire the password-change user lock")
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-loginDone; e != nil {
		t.Fatal(e)
	}
	api := httpapi.New(db, nil)
	New(db).Register(api)
	tokens := map[string]string{}
	for _, role := range []string{"viewer", "operator", "admin"} {
		_, tok, e := db.Login(ctx, role+"@example.invalid", password)
		if e != nil {
			t.Fatal(e)
		}
		tokens[role] = tok
	}
	call := func(role, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: tokens[role]})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	if w := call("", "GET", "/api/v1/profile", ""); w.Code != 401 {
		t.Fatal("anonymous profile")
	}
	for _, role := range []string{"viewer", "operator", "admin"} {
		w := call(role, "GET", "/api/v1/profile", "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", role, w.Code, w.Body.String())
		}
		var p Profile
		if e = json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		expected := 2
		if role == "admin" {
			expected = 3
		}
		if p.User.ID != role || p.User.Phone != nil || len(p.Applications) != expected || !p.CanChangePassword {
			t.Fatalf("wrong projection %+v", p)
		}
		if len(p.Permissions) != 4 || !p.Permissions["view"] || p.Permissions["update"] != (role != "viewer") || p.Permissions["approve"] != (role == "admin") || p.Permissions["delete"] != (role == "admin") {
			t.Fatalf("wrong effective resource capabilities: %+v", p.Permissions)
		}
		if p.IsPlatformAdmin != (role == "admin") {
			t.Fatal("platform administration must be separate from resource actions")
		}
		if role != "admin" {
			if len(p.Departments) != 1 || p.Departments[0].ID != "department" {
				t.Fatal("wrong single department")
			}
			inherited := false
			for _, binding := range p.Roles {
				inherited = inherited || binding.ID == "department-reader" && binding.Source == "department" && binding.DepartmentID == "department"
			}
			if !inherited {
				t.Fatal("department resource role was not shown")
			}
			for _, app := range p.Applications {
				if app.ID == "hidden" {
					t.Fatal("ungranted resource leaked its related application")
				}
			}
		}
		var payload struct {
			Applications []map[string]any `json:"applications"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &payload); e != nil {
			t.Fatal(e)
		}
		for _, app := range payload.Applications {
			if _, exists := app["permission"]; exists {
				t.Fatal("related application must not imply an independent grant")
			}
		}
	}
	for _, body := range []string{`{"user_id":"admin","avatar_data_url":""}`, `{"role":"admin"}`, `{}`, `{"avatar_data_url":null}`} {
		if w := call("viewer", "PATCH", "/api/v1/profile", body); w.Code != 422 {
			t.Fatal("readonly identity changed")
		}
	}
	raw, _ := json.Marshal(map[string]string{"avatar_data_url": avatarFixture(t)})
	if w := call("viewer", "PATCH", "/api/v1/profile", string(raw)); w.Code != 200 {
		t.Fatalf("avatar %d %s", w.Code, w.Body.String())
	}
	var avatar []byte
	if e = pool.QueryRow(ctx, `SELECT avatar_png FROM users WHERE id='admin'`).Scan(&avatar); e != nil || len(avatar) != 0 {
		t.Fatal("avatar changed another identity")
	}
	if w := call("viewer", "PATCH", "/api/v1/profile", `{"avatar_data_url":""}`); w.Code != 200 {
		t.Fatal("remove avatar failed")
	}
	if w := call("viewer", "POST", "/api/v1/profile/password", `{"current_password":"incorrect","new_password":"Replacement-password1!"}`); w.Code != 422 {
		t.Fatal("wrong password must not expire client session")
	}
	if w := call("viewer", "GET", "/api/v1/profile", ""); w.Code != 200 {
		t.Fatal("wrong password revoked session")
	}
	if _, _, e = db.Login(ctx, "viewer@example.invalid", password); e != nil {
		t.Fatal(e)
	}
	w := call("viewer", "POST", "/api/v1/profile/password", `{"current_password":"original-password-1","new_password":"Replacement-password1!"}`)
	if w.Code != 204 {
		t.Fatalf("change password: %d %s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("session cookie not cleared")
	}
	var count int
	if e = pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id='viewer'`).Scan(&count); e != nil || count != 0 {
		t.Fatal("not all own sessions revoked")
	}
	if w = call("viewer", "GET", "/api/v1/profile", ""); w.Code != 401 {
		t.Fatal("old session survived")
	}
	if w = call("admin", "GET", "/api/v1/profile", ""); w.Code != 200 {
		t.Fatal("other identity revoked")
	}
	if _, _, e = db.Login(ctx, "viewer@example.invalid", password); e == nil {
		t.Fatal("old password valid")
	}
	if _, _, e = db.Login(ctx, "viewer@example.invalid", "Replacement-password1!"); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `UPDATE users SET source='enterprise',external_id='directory-operator' WHERE id='operator'`); e != nil {
		t.Fatal(e)
	}
	w = call("operator", "GET", "/api/v1/profile", "")
	var external Profile
	if e = json.Unmarshal(w.Body.Bytes(), &external); e != nil || w.Code != 200 || !external.CanChangePassword {
		t.Fatal("directory identity with local credentials cannot change password")
	}
	if w = call("operator", "POST", "/api/v1/profile/password", `{"current_password":"original-password-1","new_password":"Another-password1!"}`); w.Code != 204 {
		t.Fatal("directory identity could not change its local password")
	}
	// Exercise the public HTTP login validator, not just Store.Login, so a
	// new character-based policy produces passwords the existing login can accept.
	httpLogin := func(secret string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"email": "operator@example.invalid", "password": secret})
		return call("", "POST", "/api/v1/auth/login", string(body))
	}
	w = httpLogin("Another-password1!")
	if w.Code != 200 {
		t.Fatalf("directory account HTTP login: %d", w.Code)
	}
	tokens["operator"] = w.Result().Cookies()[0].Value
	for _, next := range []string{strings.Repeat("x", 1025), strings.Repeat("界", 342)} {
		body, _ := json.Marshal(map[string]string{"current_password": "Another-password1!", "new_password": next})
		if w = call("operator", "POST", "/api/v1/profile/password", string(body)); w.Code != 422 {
			t.Fatal("accepted password beyond HTTP login byte limit")
		}
	}
	maxPassword := "Aa1!" + strings.Repeat("界", 124)
	body, _ := json.Marshal(map[string]string{"current_password": "Another-password1!", "new_password": maxPassword})
	if w = call("operator", "POST", "/api/v1/profile/password", string(body)); w.Code != 204 {
		t.Fatal("128-character password rejected")
	}
	if w = httpLogin(maxPassword); w.Code != 200 {
		t.Fatalf("128-character new password cannot log in: %d", w.Code)
	}
	tokens["operator"] = w.Result().Cookies()[0].Value
	if w = httpLogin(strings.Repeat("x", 1025)); w.Code != 422 {
		t.Fatal("HTTP login limit changed unexpectedly")
	}
	if w = httpLogin("Another-password1!"); w.Code != 401 {
		t.Fatal("previous directory local password still valid")
	}
	if _, e = pool.Exec(ctx, `UPDATE users SET password_hash='' WHERE id='operator'`); e != nil {
		t.Fatal(e)
	}
	w = call("operator", "GET", "/api/v1/profile", "")
	if e = json.Unmarshal(w.Body.Bytes(), &external); e != nil || w.Code != 200 || external.CanChangePassword {
		t.Fatal("identity with no local hash advertised password changes")
	}
	body, _ = json.Marshal(map[string]string{"current_password": maxPassword, "new_password": "Replacement-without-hash1!"})
	if w = call("operator", "POST", "/api/v1/profile/password", string(body)); w.Code != 422 || !strings.Contains(w.Body.String(), "no_local_password") {
		t.Fatal("no-hash account changed password")
	}
	var audit string
	if e = pool.QueryRow(ctx, `SELECT string_agg(changes::text,'') FROM management_audit WHERE action LIKE 'profile.%'`).Scan(&audit); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(audit, "original-password") || strings.Contains(audit, "Replacement-password1!") || strings.Contains(audit, "data:image") {
		t.Fatal("audit leaked profile secrets")
	}
}
