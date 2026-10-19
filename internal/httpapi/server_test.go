package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/inference"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type testRuntime struct{}

func (testRuntime) Models() []inference.Model {
	return []inference.Model{{ID: "test", Name: "test", Provider: "test"}}
}
func (testRuntime) Run(context.Context, inference.Request, func(inference.Event) error) (string, error) {
	return "test", nil
}
func testServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; isolated schema only")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, e := store.New(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = db.Bootstrap(ctx, "admin@test.local", "test-secure-password"); e != nil {
		t.Fatal(e)
	}
	api := New(db, testRuntime{})
	server := httptest.NewServer(api)
	t.Cleanup(func() {
		server.Close()
		db.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return api, server
}
func call(t *testing.T, server *httptest.Server, method, path, body string, cookie *http.Cookie) *http.Response {
	t.Helper()
	r, e := http.NewRequest(method, server.URL+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, e := server.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	return resp
}
func TestHTTPRolesCSRFAndDurableReplay(t *testing.T) {
	api, server := testServer(t)
	resp := call(t, server, "GET", "/api/v1/investigations/x/events", "", nil)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("anonymous SSE allowed")
	}
	resp = call(t, server, "POST", "/api/v1/auth/login", `{"email":"admin@test.local","password":"test-secure-password"}`, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatal("login failed")
	}
	cookie := resp.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/applications", strings.NewReader(`{"name":"bad"}`))
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://attacker.invalid")
	resp, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross origin write accepted")
	}
	resp = call(t, server, "POST", "/api/v1/users", `{"email":"viewer@test.local","name":"Viewer","password":"test-secure-password","role":"viewer"}`, cookie)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create user %d", resp.StatusCode)
	}
	resp = call(t, server, "POST", "/api/v1/auth/login", `{"email":"viewer@test.local","password":"test-secure-password"}`, nil)
	resp.Body.Close()
	viewer := resp.Cookies()[0]
	resp = call(t, server, "POST", "/api/v1/applications", `{"name":"forbidden"}`, viewer)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("viewer can write")
	}
	resp = call(t, server, "POST", "/api/v1/applications", `{"name":"demo"}`, cookie)
	var app struct {
		ID string `json:"id"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&app); e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || app.ID == "" {
		t.Fatalf("create application returned %d", resp.StatusCode)
	}
	viewerUser, e := api.Store.Session(context.Background(), viewer.Value)
	if e != nil {
		t.Fatal(e)
	}
	if e = api.Store.(*store.Store).SetGrants(context.Background(), app.ID, []core.Grant{{SubjectType: "user", SubjectID: viewerUser.ID, Permission: "view"}}); e != nil {
		t.Fatal(e)
	}
	adminUser, e := api.Store.Session(context.Background(), cookie.Value)
	if e != nil {
		t.Fatal(e)
	}
	// Legacy application routes now require explicit grants on each linked
	// infrastructure resource. Give the writer update and the viewer view only.
	if _, e = api.Store.(*store.Store).DB.Exec(context.Background(), `
INSERT INTO cmdb_resources(id,kind,name,source_kind) VALUES('layout-host','host','Layout test host','manual');
INSERT INTO rbac_roles(id,name) VALUES('layout-reader','Layout reader'),('layout-writer','Layout writer');
INSERT INTO rbac_permissions(id,name) VALUES('layout-read','Layout read'),('layout-write','Layout write');
INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action)
 VALUES('layout-read','resource','layout-host','view'),('layout-write','resource','layout-host','update');
INSERT INTO rbac_role_permissions(role_id,permission_id)
 VALUES('layout-reader','layout-read'),('layout-writer','layout-read'),('layout-writer','layout-write');`); e != nil {
		t.Fatal(e)
	}
	if _, e = api.Store.(*store.Store).DB.Exec(context.Background(), `INSERT INTO cmdb_resource_applications(resource_id,application_id,environment_id)
SELECT 'layout-host',$1,id FROM application_environments WHERE application_id=$1 ORDER BY id LIMIT 1`, app.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = api.Store.(*store.Store).DB.Exec(context.Background(), `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'layout-reader'),($2,'layout-writer')`, viewerUser.ID, adminUser.ID); e != nil {
		t.Fatal(e)
	}
	inv, e := api.Store.CreateInvestigation(context.Background(), app.ID, "test")
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/investigations/" + inv.ID + "/layout"
	resp = call(t, server, "PUT", path, `{"layout":{"draft":"private"}}`, cookie)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("save layout")
	}
	resp = call(t, server, "GET", path, "", viewer)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized viewer layout returned %d", resp.StatusCode)
	}
	var layout map[string]map[string]any
	if e = json.NewDecoder(resp.Body).Decode(&layout); e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if len(layout["layout"]) != 0 {
		t.Fatal("layout leaked between users")
	}
	events, e := api.Store.Events(context.Background(), inv.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/investigations/"+inv.ID+"/events?after=0", nil)
	req.AddCookie(viewer)
	resp, e = server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized viewer event stream returned %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var event struct {
				Seq int64 `json:"seq"`
			}
			if e = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); e != nil {
				t.Fatal(e)
			}
			if event.Seq != events[0].Seq {
				t.Fatal("wrong replay sequence")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no replayed event")
	}
}
func TestAccountDisableAndLastAdmin(t *testing.T) {
	api, server := testServer(t)
	admin, token, e := api.Store.Login(context.Background(), "admin@test.local", "test-secure-password")
	if e != nil {
		t.Fatal(e)
	}
	cookie := &http.Cookie{Name: "aiops_session", Value: token}
	resp := call(t, server, "PATCH", "/api/v1/users/"+admin.ID, `{"disabled":true}`, cookie)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("last admin disable returned %d", resp.StatusCode)
	}
	resp = call(t, server, "POST", "/api/v1/users", `{"email":"operator@test.local","name":"Operator","password":"test-secure-password","role":"operator"}`, cookie)
	resp.Body.Close()
	user, utoken, e := api.Store.Login(context.Background(), "operator@test.local", "test-secure-password")
	if e != nil {
		t.Fatal(e)
	}
	resp = call(t, server, "PATCH", "/api/v1/users/"+user.ID, `{"disabled":true}`, cookie)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("disable operator")
	}
	resp = call(t, server, "GET", "/api/v1/auth/me", "", &http.Cookie{Name: "aiops_session", Value: utoken})
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("disabled user session remains active")
	}
	if _, _, e = api.Store.Login(context.Background(), "operator@test.local", "test-secure-password"); e == nil {
		t.Fatal("disabled user can log in")
	}
}
func TestSSEStopsAfterSessionRevocation(t *testing.T) {
	api, server := testServer(t)
	ctx := context.Background()
	_, token, e := api.Store.Login(ctx, "admin@test.local", "test-secure-password")
	if e != nil {
		t.Fatal(e)
	}
	app, e := api.Store.RegisterApplication(ctx, core.Application{Name: "Test", Key: "test", Environment: "lab"})
	if e != nil {
		t.Fatal(e)
	}
	inv, e := api.Store.CreateInvestigation(ctx, app.ID, "revoke")
	if e != nil {
		t.Fatal(e)
	}
	streamCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", server.URL+"/api/v1/investigations/"+inv.ID+"/events", nil)
	req.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
	resp, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			break
		}
	}
	if _, e = api.Store.(*store.Store).DB.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", store.Hash(token)); e != nil {
		t.Fatal(e)
	}
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			t.Fatal("received event after revoke")
		}
	}
	if streamCtx.Err() != nil {
		t.Fatal("SSE remained open after session revoked")
	}
}
