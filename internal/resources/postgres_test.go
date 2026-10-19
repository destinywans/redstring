package resources

import (
	"context"
	"encoding/json"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestPostgresResourceScopeAndACL(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "resources_test_" + store.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
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
	defer db.Close()
	if e = db.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	_, e = db.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,password_hash) VALUES ('ua','a@example.invalid','Viewer A','viewer','unused'),('ub','b@example.invalid','Viewer B','viewer','unused'),('admin','admin@example.invalid','Admin','admin','unused');
 INSERT INTO applications(id,key,name) VALUES('a','a','Application A'),('b','b','Application B');
 INSERT INTO application_environments(id,application_id,key,name) VALUES('ea','a','production','Production'),('eb','b','production','Production');
 INSERT INTO application_grants(application_id,subject_type,subject_id,permission) VALUES('a','user','ua','view'),('b','user','ub','view');
 INSERT INTO cmdb_resources(id,kind,name,source_kind,is_mock) VALUES('ra','deployment','A workload','manual',false),('rb','deployment','B workload','manual',false),('shared','redis','Shared cache','manual',false),('unlinked','host','Unlinked','manual',false);
 UPDATE cmdb_resources SET attributes='{"application":"Application A","application_id":"a","ip":"10.0.0.7"}' WHERE id='shared';
 INSERT INTO cmdb_resource_applications VALUES('ra','a','ea'),('rb','b','eb'),('shared','a','ea'),('shared','b','eb');
 INSERT INTO cmdb_resource_relations(id,source_resource_id,target_resource_id,kind,provenance,source_kind,is_mock) VALUES('as','ra','shared','depends_on','declared','manual',false),('bs','rb','shared','depends_on','declared','manual',false);`)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"ua", "ub", "admin"} {
		if id != "admin" {
			app := strings.TrimPrefix(id, "u")
			// Each viewer receives only its two infrastructure objects. A
			// shared object's resource grant does not grant the other app.
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO rbac_roles(id,name) VALUES($1,$1)`, []any{"resource-fixture-" + app}},
				{`INSERT INTO rbac_permissions(id,name) VALUES($1,$1)`, []any{"resource-fixture-view-" + app}},
				{`INSERT INTO rbac_role_permissions(role_id,permission_id) VALUES($1,$2)`, []any{"resource-fixture-" + app, "resource-fixture-view-" + app}},
				{`INSERT INTO rbac_permission_resources(permission_id,scope_type,resource_id,action) VALUES($1,'resource',$2,'view'),($1,'resource','shared','view')`, []any{"resource-fixture-view-" + app, "r" + app}},
				{`INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, []any{id, "resource-fixture-" + app}},
			} {
				if _, e = db.DB.Exec(ctx, statement.sql, statement.args...); e != nil {
					t.Fatal(e)
				}
			}
		}
		if _, e = db.DB.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, store.Hash("resource-test-"+id), id); e != nil {
			t.Fatal(e)
		}
	}
	api := httpapi.New(db, nil)
	New(db.DB).Register(api)
	request := func(user, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: "resource-test-" + user})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, uid := range []string{"ua", "ub"} {
		w := request(uid, "GET", "/api/v1/resources?environment_scope=production", "")
		if w.Code != 200 {
			t.Fatalf("list %d %s", w.Code, w.Body.String())
		}
		var p Page
		if e = json.Unmarshal(w.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
		if p.Total != 2 {
			t.Fatalf("visible resource count=%d", p.Total)
		}
		expected := strings.TrimPrefix(uid, "u")
		for _, resource := range p.Items {
			for _, forbidden := range []string{"application", "application_id"} {
				if _, exists := resource.Attributes[forbidden]; exists {
					t.Fatalf("legacy application attribute leaked: %#v", resource)
				}
			}
			if resource.ID == "shared" && resource.Attributes["ip"] != "10.0.0.7" {
				t.Fatal("normal resource attributes were removed")
			}
			for _, link := range resource.Applications {
				if link.ID != expected {
					t.Fatalf("leaked application link %#v", resource)
				}
			}
		}
		w = request(uid, "GET", "/api/v1/resource-relations?root_id=shared&depth=3&environment_scope=production", "")
		if w.Code != 200 {
			t.Fatalf("graph %d %s", w.Code, w.Body.String())
		}
		var g Graph
		_ = json.Unmarshal(w.Body.Bytes(), &g)
		for _, node := range g.Nodes {
			if _, exists := node.Attributes["application_id"]; exists {
				t.Fatal("graph leaked application attribute")
			}
		}
		if len(g.Nodes) != 2 || len(g.Edges) != 1 {
			t.Fatalf("graph leaked endpoint %#v", g)
		}
	}
	for _, path := range []string{"/api/v1/resources/rb", "/api/v1/resources/shared?environment_scope=missing", "/api/v1/resource-relations?root_id=rb"} {
		w := request("ua", "GET", path, "")
		if w.Code != 404 {
			t.Fatalf("expected scoped 404 %s got %d", path, w.Code)
		}
	}
	w := request("ua", "GET", "/api/v1/resources?environment_scope=missing", "")
	var empty Page
	_ = json.Unmarshal(w.Body.Bytes(), &empty)
	if w.Code != 200 || empty.Total != 0 || len(empty.Facets.Kinds) != 0 {
		t.Fatalf("unknown scope broadened: %s", w.Body.String())
	}
	if w = request("ua", "POST", "/api/v1/resources", `{"name":"unauthorized","kind":"redis"}`); w.Code != 403 {
		t.Fatal("viewer created resource")
	}
	if w = request("admin", "POST", "/api/v1/resources", `{"name":"bad env","kind":"redis","applications":[{"id":"a","environment_id":"eb"}]}`); w.Code != 422 {
		t.Fatalf("mismatched env %d %s", w.Code, w.Body.String())
	}
	w = request("admin", "POST", "/api/v1/resources", `{"name":"Registered only","kind":"redis","applications":[{"id":"a","environment_id":"ea"}]}`)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var created Resource
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.IsMock || created.SourceKind != "manual" || created.ObservedAt != nil {
		t.Fatalf("manual registration fabricated observations %#v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("creation receipt lost persisted timestamps")
	}
	var count int
	// Creation acknowledges the committed registration but grants no read
	// authority. Retrying a false 404 previously created another resource.
	if got := request("admin", "GET", "/api/v1/resources/"+created.ID, ""); got.Code != 404 {
		t.Fatalf("registration unexpectedly granted view: %d %s", got.Code, got.Body.String())
	}
	if e = db.DB.QueryRow(ctx, `SELECT count(*) FROM cmdb_resources WHERE name='Registered only'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("registration count=%d err=%v", count, e)
	}
	if e = db.DB.QueryRow(ctx, "SELECT count(*) FROM management_audit WHERE action='resource.created'").Scan(&count); e != nil || count != 1 {
		t.Fatalf("audit missing: %d %v", count, e)
	}
}
