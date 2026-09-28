package v3canvas

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestCanvasTemplatesHTTPV3ScopeSanitizationAndBoundedList(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_canvas_tpl_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('canvas-editor','Canvas Editor'),('canvas-reader','Canvas Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('canvas-editor','environment','view','environment','dev'),
 ('canvas-editor','environment','view','environment','staging'),
 ('canvas-editor','canvas','view','environment','dev'),
 ('canvas-editor','canvas','view','environment','staging'),
 ('canvas-editor','canvas','change','environment','dev'),
 ('canvas-editor','canvas','change','environment','staging'),
 ('canvas-reader','environment','view','environment','dev'),
 ('canvas-reader','canvas','view','environment','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "owner@example.invalid", Name: "Owner", Role: "viewer"}, "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, core.User{Email: "other@example.invalid", Name: "Other", Role: "viewer"}, "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ user, role string }{{owner.ID, "canvas-editor"}, {other.ID, "canvas-reader"}} {
		if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, row.user, row.role); err != nil {
			t.Fatal(err)
		}
	}
	_, ownerToken, err := st.Login(ctx, "owner@example.invalid", "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, otherToken, err := st.Login(ctx, "other@example.invalid", "isolated-other-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, environment, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/canvas-templates", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s; want status=%d code=%q", w.Code, w.Body.String(), status, code)
		}
	}
	const secret = "private-evidence-do-not-copy"
	original := []Node{{ID: "source", X: 20, Y: 30, W: 240, Icon: "alert", Tone: "warn", Title: secret,
		Blocks: []json.RawMessage{json.RawMessage(`{"type":"text","text":"` + secret + `"}`)}}}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,nodes,edges) VALUES
 ($1,$2,'dev','Source','plan',$3::jsonb,'[]'::jsonb),
 ('CV-other',$4,'dev','Other','plan',$3::jsonb,'[]'::jsonb),
 ('CV-stage',$2,'staging','Stage','plan',$3::jsonb,'[]'::jsonb)`, "CV-source", owner.ID, mustJSON(original), other.ID); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", "", ""), 401, "")
	assert(request(http.MethodPost, "dev", "", `{}`), 401, "")
	assert(request(http.MethodPost, "dev", otherToken, `{}`), 403, "forbidden")
	assert(request(http.MethodPost, "all", ownerToken, `{}`), 409, "env_view_only")
	assert(request(http.MethodPost, "staging", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true}}`), 404, "not_found")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-other","name":"Unsafe","keep":{"nodes":true}}`), 404, "not_found")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true,"steps":true}}`), 422, "unsafe_template_content")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true,"runbooks":true}}`), 422, "unsafe_template_content")
	created := request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Safe layout","keep":{"nodes":true}}`)
	assert(created, 201, "")
	var result template
	if err = json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" || result.Name != "Safe layout" || result.Kind != "plan" || result.Nodes != "1 个节点" || result.IsDefault {
		t.Fatalf("incorrect create DTO: %s (%v)", created.Body.String(), err)
	}
	var persistedOwner, persistedEnv, rawNodes string
	if err = db.QueryRow(ctx, `SELECT owner_id,environment_id,nodes::text FROM v3_canvas_templates WHERE id=$1`, result.ID).Scan(&persistedOwner, &persistedEnv, &rawNodes); err != nil {
		t.Fatal(err)
	}
	if persistedOwner != owner.ID || persistedEnv != "dev" || strings.Contains(rawNodes, secret) || !strings.Contains(rawNodes, "node-1") {
		t.Fatalf("private template or sanitization mismatch: owner=%q env=%q nodes=%s", persistedOwner, persistedEnv, rawNodes)
	}
	var auditCount int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.canvas_template.saved' AND target=$2 AND changes->>'environment_id'='dev' AND changes->>'source_canvas_id'='CV-source'`, owner.ID, result.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("save must have one audit record, got %d", auditCount)
	}
	listed := request(http.MethodGet, "dev", ownerToken, "")
	assert(listed, 200, "")
	var list struct {
		Items []template `json:"items"`
	}
	if err = json.Unmarshal(listed.Body.Bytes(), &list); err != nil || len(list.Items) != 5 {
		t.Fatalf("owner list should include four defaults and private template: %s (%v)", listed.Body.String(), err)
	}
	listed = request(http.MethodGet, "dev", otherToken, "")
	assert(listed, 200, "")
	if strings.Contains(listed.Body.String(), result.ID) {
		t.Fatalf("private template leaked to other user: %s", listed.Body.String())
	}
	listed = request(http.MethodGet, "staging", ownerToken, "")
	assert(listed, 200, "")
	if strings.Contains(listed.Body.String(), result.ID) {
		t.Fatalf("dev template leaked into staging: %s", listed.Body.String())
	}
	listed = request(http.MethodGet, "all", ownerToken, "")
	assert(listed, 200, "")
	if !strings.Contains(listed.Body.String(), result.ID) {
		t.Fatalf("owner all scope omitted own dev template: %s", listed.Body.String())
	}
	// A large but valid graph is rejected before any template or audit write.
	large := make([]Node, 101)
	for i := range large {
		large[i] = Node{ID: fmt.Sprintf("n-%d", i), W: 260}
	}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$2::jsonb WHERE id=$1`, "CV-source", mustJSON(large)); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Too large","keep":{"nodes":true}}`), 422, "unsafe_template_structure")
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates WHERE owner_id=$1`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("oversize graph wrote a template, count=%d", auditCount)
	}
	// Do not silently truncate a template list beyond its documented bound.
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name)
 SELECT 'bulk-' || gs::text,$1,'dev','plan','Bulk' FROM generate_series(1,$2) AS gs`, owner.ID, maxTemplateItems); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", ownerToken, ""), 422, "template_scope_too_broad")
}
