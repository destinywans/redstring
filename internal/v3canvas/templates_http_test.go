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
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
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
	canvasGet := func(path, environment, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	genericList := canvasGet("/api/v1/canvases?kind=plan", "dev", ownerToken)
	assert(genericList, 200, "")
	if !strings.Contains(genericList.Body.String(), `"id":"CV-source"`) || strings.Contains(genericList.Body.String(), `"id":"CV-other"`) || strings.Contains(genericList.Body.String(), `"id":"CV-stage"`) {
		t.Fatalf("generic canvas list must be owner and environment scoped: %s", genericList.Body.String())
	}
	genericDetail := canvasGet("/api/v1/canvases/CV-source", "dev", ownerToken)
	assert(genericDetail, 200, "")
	var genericDoc struct {
		ID        string         `json:"id"`
		Kind      string         `json:"kind"`
		Nodes     []Node         `json:"nodes"`
		Edges     []Edge         `json:"edges"`
		Artifacts []any          `json:"artifacts"`
		Threads   map[string]any `json:"threads"`
	}
	if err = json.Unmarshal(genericDetail.Body.Bytes(), &genericDoc); err != nil || genericDoc.ID != "CV-source" || genericDoc.Kind != "plan" || len(genericDoc.Nodes) != 1 || len(genericDoc.Edges) != 0 || len(genericDoc.Artifacts) != 0 || genericDoc.Threads == nil {
		t.Fatalf("generic canvas detail mismatch: %s (%v)", genericDetail.Body.String(), err)
	}
	assert(canvasGet("/api/v1/canvases/CV-other", "dev", ownerToken), 404, "not_found")
	assert(canvasGet("/api/v1/canvases/CV-source", "staging", ownerToken), 404, "not_found")
	allCanvases := canvasGet("/api/v1/canvases", "all", ownerToken)
	assert(allCanvases, 200, "")
	if !strings.Contains(allCanvases.Body.String(), `"id":"CV-source"`) || !strings.Contains(allCanvases.Body.String(), `"id":"CV-stage"`) || strings.Contains(allCanvases.Body.String(), `"id":"CV-other"`) {
		t.Fatalf("all scope must contain only authorized owner canvases: %s", allCanvases.Body.String())
	}
	assert(canvasGet("/api/v1/canvases/CV-stage", "all", ownerToken), 200, "")
	assert(canvasGet("/api/v1/canvases/CV-source", "all", otherToken), 404, "not_found")
	// The persisted investigation list is now available; this fixture has no
	// investigation in the owner's authorized environment.
	investigationList := canvasGet("/api/v1/canvases?kind=investigation", "dev", ownerToken)
	assert(investigationList, 200, "")
	if !strings.Contains(investigationList.Body.String(), `"items":[]`) || !strings.Contains(investigationList.Body.String(), `"total":0`) {
		t.Fatalf("unexpected investigation rows: %s", investigationList.Body.String())
	}
	assert(request(http.MethodGet, "dev", "", ""), 401, "")
	assert(request(http.MethodPost, "dev", "", `{}`), 401, "")
	assert(request(http.MethodPost, "dev", otherToken, `{}`), 403, "forbidden")
	assert(request(http.MethodPost, "all", ownerToken, `{}`), 409, "env_view_only")
	// The registered detail route must return typed nested blocks, edges,
	// artifacts and empty per-node threads, not opaque database JSON.
	const shapeNodes = `[
 {"id":"root","x":10,"y":20,"w":240,"icon":"layers","tone":"info","title":"Root","sub":"Scope","badge":{"text":"ready","tone":"ok"},"blocks":[{"type":"kv","items":[["target","36k"]],"unknown":"private-extra"}]},
 {"id":"child","x":290,"y":20,"w":240,"icon":"activity","tone":"warn","title":"Child","sub":"Branch","blocks":[{"type":"steps","items":[{"text":"approve","sub":"human","state":"gate"}]}]}
]`
	const shapeEdges = `[{"from":"root","to":"child","tone":"accent","live":true}]`
	const shapeArtifacts = `[{"id":"artifact-1","x":290,"y":320,"icon":"file","tone":"info","title":"Plan","sub":"plan.md","href":"/artifacts/one","unknown":"private-extra"}]`
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,nodes,edges,artifacts)
 VALUES('CV-shapes',$1,'dev','Typed plan','plan',$2::jsonb,$3::jsonb,$4::jsonb)`, owner.ID, shapeNodes, shapeEdges, shapeArtifacts); err != nil {
		t.Fatal(err)
	}
	shaped := canvasGet("/api/v1/canvases/CV-shapes", "dev", ownerToken)
	assert(shaped, 200, "")
	var shapedDoc struct {
		Nodes     []Node                `json:"nodes"`
		Edges     []Edge                `json:"edges"`
		Artifacts []CanvasArtifact      `json:"artifacts"`
		Threads   map[string]nodeThread `json:"threads"`
	}
	if err = json.Unmarshal(shaped.Body.Bytes(), &shapedDoc); err != nil ||
		len(shapedDoc.Nodes) != 2 || shapedDoc.Nodes[0].Badge == nil || len(shapedDoc.Nodes[0].Blocks) != 1 ||
		len(shapedDoc.Edges) != 1 || len(shapedDoc.Artifacts) != 1 ||
		len(shapedDoc.Threads) != 2 || shapedDoc.Threads["child"].Turns == nil ||
		strings.Contains(shaped.Body.String(), "private-extra") {
		t.Fatalf("invalid typed canvas detail: %s (%v)", shaped.Body.String(), err)
	}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$1::jsonb WHERE id='CV-shapes'`, `[{"id":"root","x":10,"y":20,"w":240,"icon":"layers","tone":"info","title":"Root","sub":"Scope","blocks":[{"type":"kv"}]}]`); err != nil {
		t.Fatal(err)
	}
	assert(canvasGet("/api/v1/canvases/CV-shapes", "dev", ownerToken), 503, "canvas_unavailable")
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$1::jsonb WHERE id='CV-shapes'`, shapeNodes); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_node_exchanges
	 (id,canvas_id,node_id,user_id,author,user_text,model_id,model_name,model_provider,state,lease_expires_at)
	 SELECT 'CNT-bulk-' || gs::text,'CV-shapes','root',$1,'Owner','prompt','fake','Fake','Test','failed',clock_timestamp()
	 FROM generate_series(1,$2) gs`, owner.ID, maxCanvasExchanges+1); err != nil {
		t.Fatal(err)
	}
	assert(canvasGet("/api/v1/canvases/CV-shapes", "dev", ownerToken), 413, "canvas_history_too_large")
	// A detail read holds the grant source until its consistent document
	// snapshot has been read. Revocation either precedes the read or waits.
	historyLock, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer historyLock.Rollback(context.Background())
	if _, err = historyLock.Exec(ctx, `LOCK TABLE v3_canvas_node_exchanges IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	reading := make(chan *httptest.ResponseRecorder, 1)
	go func() { reading <- canvasGet("/api/v1/canvases/CV-source", "dev", ownerToken) }()
	waiting := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%SELECT node_id,id,author,user_text,left(assistant_text,65537)%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("detail did not reach locked history read")
	}
	revoked := make(chan error, 1)
	go func() {
		_, revokeErr := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='canvas-editor'`, owner.ID)
		revoked <- revokeErr
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%DELETE FROM rbac_user_roles WHERE user_id=%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("canvas grant revocation did not wait for read")
	}
	if err = historyLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assert(<-reading, 200, "")
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	assert(canvasGet("/api/v1/canvases/CV-source", "dev", ownerToken), 403, "forbidden")
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'canvas-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "staging", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true}}`), 404, "not_found")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-other","name":"Unsafe","keep":{"nodes":true}}`), 404, "not_found")
	// Explicit retention is supported; its content, state and privacy contract
	// is exercised with populated sources in TestCanvasTemplateRetentionHTTPPrivateSnapshots.
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true,"evidence":true}}`), 422, "invalid_body")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":null}`), 422, "invalid_template")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{}}`), 422, "invalid_template")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"steps":false}}`), 422, "invalid_template")
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET artifacts=$2::jsonb WHERE id=$1`, "CV-source",
		mustJSON([]CanvasArtifact{{ID: "secret-artifact", Title: secret, Sub: secret}})); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_node_exchanges
	 (id,canvas_id,node_id,user_id,author,user_text,assistant_text,model_id,model_name,model_provider,state,lease_expires_at,completed_at)
	 VALUES('CNT-source-private','CV-source','source',$1,'Owner',$2,$2,'fake','Fake','Test','completed',clock_timestamp(),clock_timestamp())`, owner.ID, secret); err != nil {
		t.Fatal(err)
	}
	sourceDetail := canvasGet("/api/v1/canvases/CV-source", "dev", ownerToken)
	assert(sourceDetail, 200, "")
	if !strings.Contains(sourceDetail.Body.String(), "secret-artifact") || !strings.Contains(sourceDetail.Body.String(), "CNT-source-private") {
		t.Fatalf("source must contain an artifact and conversation before saving: %s", sourceDetail.Body.String())
	}
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
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":" safe LAYOUT ","keep":{"nodes":true}}`), 409, "duplicate_template")
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates WHERE owner_id=$1`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("duplicate name wrote a second template, count=%d", auditCount)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.canvas_template.saved'`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("duplicate name wrote a second audit record, count=%d", auditCount)
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
	// A saved structural template must create a readable generic canvas. The
	// source's evidence and conversation content must not reappear there.
	canvasBody := fmt.Sprintf(`{"kind":"plan","title":"From safe layout","template_id":%q,"services":[]}`, result.ID)
	createdCanvas := httptest.NewRequest(http.MethodPost, "/api/v1/canvases", strings.NewReader(canvasBody))
	createdCanvas.Header.Set("X-AIOps-Environment", "dev")
	createdCanvas.Header.Set("Content-Type", "application/json")
	createdCanvas.AddCookie(&http.Cookie{Name: "aiops_session", Value: ownerToken})
	createdCanvasResponse := httptest.NewRecorder()
	api.ServeHTTP(createdCanvasResponse, createdCanvas)
	assert(createdCanvasResponse, 201, "")
	var canvasResult struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(createdCanvasResponse.Body.Bytes(), &canvasResult); err != nil || canvasResult.ID == "" {
		t.Fatalf("created canvas ID missing: %s (%v)", createdCanvasResponse.Body.String(), err)
	}
	createdDetail := canvasGet("/api/v1/canvases/"+canvasResult.ID, "dev", ownerToken)
	assert(createdDetail, 200, "")
	if strings.Contains(createdDetail.Body.String(), secret) || !strings.Contains(createdDetail.Body.String(), `"title":"From safe layout"`) || !strings.Contains(createdDetail.Body.String(), `"id":"node-1"`) {
		t.Fatalf("created canvas lost structure or exposed source content: %s", createdDetail.Body.String())
	}
	var freshDoc struct {
		Artifacts []CanvasArtifact      `json:"artifacts"`
		Threads   map[string]nodeThread `json:"threads"`
	}
	if err = json.Unmarshal(createdDetail.Body.Bytes(), &freshDoc); err != nil || len(freshDoc.Artifacts) != 0 || len(freshDoc.Threads) != 1 || len(freshDoc.Threads["node-1"].Turns) != 0 {
		t.Fatalf("source artifact or conversation copied into created canvas: %s (%v)", createdDetail.Body.String(), err)
	}
	assert(canvasGet("/api/v1/canvases/"+canvasResult.ID, "dev", otherToken), 404, "not_found")
	assert(canvasGet("/api/v1/canvases/"+canvasResult.ID, "staging", ownerToken), 404, "not_found")
	english := httptest.NewRequest(http.MethodGet, "/api/v1/canvas-templates", nil)
	english.Header.Set("X-AIOps-Environment", "dev")
	english.Header.Set("Accept-Language", "en-US")
	english.AddCookie(&http.Cookie{Name: "aiops_session", Value: ownerToken})
	englishResponse := httptest.NewRecorder()
	api.ServeHTTP(englishResponse, english)
	assert(englishResponse, 200, "")
	if !strings.Contains(englishResponse.Body.String(), `"nodes":"1 node"`) {
		t.Fatalf("English node count missing: %s", englishResponse.Body.String())
	}
	// An existing source may contain a cycle from legacy data. Saving it as a
	// new template must fail before either the template or audit is written.
	cycleNodes := []Node{{ID: "a", W: 240}, {ID: "b", W: 240}}
	cycleEdges := []Edge{{From: "a", To: "b"}, {From: "b", To: "a"}}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$2::jsonb,edges=$3::jsonb WHERE id=$1`, "CV-source", mustJSON(cycleNodes), mustJSON(cycleEdges)); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Cyclic","keep":{"nodes":true}}`), 422, "unsafe_template_structure")
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates WHERE owner_id=$1`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("cyclic graph wrote a template, count=%d", auditCount)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.canvas_template.saved'`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("cyclic graph wrote an audit record, count=%d", auditCount)
	}
	// A role revocation cannot commit between the save's current grant check
	// and its template/audit commit. The next request sees the revoked grant.
	auditLock, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer auditLock.Rollback(context.Background())
	if _, err = auditLock.Exec(ctx, `LOCK TABLE management_audit IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	saving := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		saving <- request(http.MethodPost, "dev", ownerToken,
			fmt.Sprintf(`{"from_canvas":%q,"name":"Concurrent save","keep":{"nodes":true}}`, canvasResult.ID))
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%INSERT INTO management_audit(actor_id,action,target,outcome,changes)%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("template save did not reach the blocked audit write")
	}
	revoked = make(chan error, 1)
	go func() {
		_, revokeErr := db.Exec(ctx, `DELETE FROM rbac_user_roles WHERE user_id=$1 AND role_id='canvas-editor'`, owner.ID)
		revoked <- revokeErr
	}()
	waiting = false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if err = adminDB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND wait_event_type='Lock'
 AND query LIKE '%DELETE FROM rbac_user_roles WHERE user_id=%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if !waiting {
		t.Fatal("role revocation did not wait for template save")
	}
	if err = auditLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	assert(<-saving, 201, "")
	if err = <-revoked; err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "dev", ownerToken,
		fmt.Sprintf(`{"from_canvas":%q,"name":"After revoke","keep":{"nodes":true}}`, canvasResult.ID)), 403, "forbidden")
	assert(request(http.MethodGet, "dev", ownerToken, ""), 403, "forbidden")
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'canvas-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", ownerToken, ""), 200, "")
	// A large but valid graph is rejected before any template or audit write.
	large := make([]Node, 101)
	for i := range large {
		large[i] = Node{ID: fmt.Sprintf("n-%d", i), W: 260}
	}
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$2::jsonb,edges='[]'::jsonb WHERE id=$1`, "CV-source", mustJSON(large)); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Too large","keep":{"nodes":true}}`), 422, "unsafe_template_structure")
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates WHERE owner_id=$1`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("oversize graph wrote a template, count=%d", auditCount)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.canvas_template.saved'`, owner.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("oversize graph wrote an audit record, count=%d", auditCount)
	}
	// Explicitly keeping no nodes produces a private blank structural template,
	// even when the source itself exceeds the node-copy limit.
	blank := request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Blank structure","keep":{"nodes":false}}`)
	assert(blank, 201, "")
	var blankTemplate template
	if err = json.Unmarshal(blank.Body.Bytes(), &blankTemplate); err != nil || blankTemplate.ID == "" || blankTemplate.Nodes != "0 个节点" {
		t.Fatalf("blank template response mismatch: %s (%v)", blank.Body.String(), err)
	}
	var rawEdges string
	if err = db.QueryRow(ctx, `SELECT nodes::text,edges::text FROM v3_canvas_templates WHERE id=$1 AND owner_id=$2 AND environment_id='dev'`, blankTemplate.ID, owner.ID).Scan(&rawNodes, &rawEdges); err != nil {
		t.Fatal(err)
	}
	if rawNodes != "[]" || rawEdges != "[]" {
		t.Fatalf("blank keep copied source structure: nodes=%s edges=%s", rawNodes, rawEdges)
	}
	// Names are private to one owner, environment, and canvas kind.
	assert(request(http.MethodPost, "staging", ownerToken, `{"from_canvas":"CV-stage","name":"Safe layout","keep":{"nodes":true}}`), 201, "")
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind)
 VALUES('CV-review-template-source',$1,'dev','Review source','review')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-review-template-source","name":"Safe layout","keep":{"nodes":true}}`), 201, "")
	// Concurrent duplicate saves must have one winner and one conflict, with
	// exactly one persisted template and one success audit.
	const raceName = "Race layout"
	parallel := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func() {
			parallel <- request(http.MethodPost, "staging", ownerToken,
				`{"from_canvas":"CV-stage","name":"`+raceName+`","keep":{"nodes":true}}`)
		}()
	}
	first, second := <-parallel, <-parallel
	if first.Code == second.Code || (first.Code != 201 && second.Code != 201) || (first.Code != 409 && second.Code != 409) {
		t.Fatalf("duplicate race must produce one save and one conflict: first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		if response.Code == 409 {
			assert(response, 409, "duplicate_template")
		}
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM v3_canvas_templates WHERE owner_id=$1 AND environment_id='staging' AND kind='plan' AND name=$2`, owner.ID, raceName).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("concurrent duplicate save wrote %d templates", auditCount)
	}
	winner := first
	if winner.Code != 201 {
		winner = second
	}
	var winnerTemplate template
	if err = json.Unmarshal(winner.Body.Bytes(), &winnerTemplate); err != nil || winnerTemplate.ID == "" {
		t.Fatalf("concurrent winner omitted template ID: %s (%v)", winner.Body.String(), err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1
 AND action='v3.canvas_template.saved' AND target=$2`, owner.ID, winnerTemplate.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("concurrent duplicate save wrote %d winner audits", auditCount)
	}
	// Read authorization must reflect an expired session, even when the role
	// grants and template rows still exist.
	if _, err = db.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1`, store.Hash(otherToken)); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", otherToken, ""), 401, "")
	// Do not silently truncate a template list beyond its documented bound,
	// or allow another save whose result would immediately be unreadable.
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name)
 SELECT 'bulk-' || gs::text,$1,'dev','plan','Bulk' FROM generate_series(1,$2) AS gs`, owner.ID, maxTemplateItems); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", ownerToken, ""), 422, "template_scope_too_broad")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-review-template-source","name":"After capacity","keep":{"nodes":true}}`), 422, "template_scope_too_broad")
}

// Retention deliberately stores private snapshots. It must never carry execution
// state, live metric samples, arbitrary artifacts or conversations into a new canvas.
func TestCanvasTemplateRetentionHTTPPrivateSnapshots(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_canvas_retention_" + store.ID()
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
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
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
 ('canvas-reader','canvas','view','environment','dev'),
 ('canvas-reader','canvas','change','environment','dev'),
 ('canvas-editor','knowledge','view','application','retention-app')`,
		`INSERT INTO applications(id,name,key,archived) VALUES('retention-app','Retention App','retention-app',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES('retention-ae','retention-app','dev','Development',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES('retention-app','dev')`,
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
	request := func(method, path, environment, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
	const sourceID = "CV-retention-source"
	const snapshotText = "private-snapshot-text"
	const threadText = "private-conversation-never-retained"
	const artifactText = "unstructured-artifact-never-retained"
	original := []Node{{ID: "source", X: 20, Y: 30, W: 240, Icon: "alert", Tone: "warn", Title: snapshotText,
		Blocks: []json.RawMessage{
			json.RawMessage(`{"type":"steps","items":[{"text":"Check service","sub":"source completed","state":"done"},{"text":"Approve","sub":"human required","state":"gate"}]}`),
			json.RawMessage(`{"type":"text","text":"` + snapshotText + `"}`),
			json.RawMessage(`{"type":"rows","items":[{"text":"Latency","value":"stale-live-value","state":"stale-live-state"}]}`),
			json.RawMessage(`{"type":"bars","values":[123456789]}`),
		}}}
	artifacts := []CanvasArtifact{
		{ID: "published-runbook", X: 30, Y: 40, Title: "stale artifact title", Href: "/runbooks/retained-runbook"},
		{ID: "unstructured-artifact", X: 30, Y: 50, Title: artifactText},
	}
	if _, err = db.Exec(ctx, `INSERT INTO knowledge_documents
 (id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES('retained-runbook','retention-app','Published Runbook','Never execute automatically','runbook://v3/manual',1,'fixture-hash',$1,'published',
 '{"visibility":"application","environment_ids":["dev"]}'::jsonb)`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind,steps)
 VALUES('retained-runbook','blank','[{"title":"Inspect metrics"}]'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,services,nodes,edges,artifacts)
 VALUES($1,$2,'dev','Retention source','plan',ARRAY['retention-app'],$3::jsonb,'[]'::jsonb,$4::jsonb)`, sourceID, owner.ID, mustJSON(original), mustJSON(artifacts)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_node_exchanges
 (id,canvas_id,node_id,user_id,author,user_text,assistant_text,model_id,model_name,model_provider,state,lease_expires_at,completed_at)
 VALUES('CNT-retention-private',$1,'source',$2,'Owner',$3,$3,'fake','Fake','Test','completed',clock_timestamp(),clock_timestamp())`, sourceID, owner.ID, threadText); err != nil {
		t.Fatal(err)
	}
	saved := map[string]string{}
	for _, scenario := range []struct {
		option                   string
		steps, runbooks, content int
	}{{"steps", 2, 0, 0}, {"runbooks", 0, 1, 0}, {"content", 0, 0, 2}} {
		t.Run(scenario.option, func(t *testing.T) {
			body := fmt.Sprintf(`{"from_canvas":%q,"name":%q,"keep":{"nodes":true,%q:true}}`, sourceID, scenario.option, scenario.option)
			created := request(http.MethodPost, "/api/v1/canvas-templates", "dev", ownerToken, body)
			assert(created, 201, "")
			var result template
			if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.ID == "" || result.Retention == nil {
				t.Fatalf("retention receipt missing: %s (%v)", created.Body.String(), err)
			}
			if result.Retention.Steps != scenario.steps || result.Retention.Runbooks != scenario.runbooks || result.Retention.ContentBlocks != scenario.content {
				t.Fatalf("incorrect retained counts: %+v", result.Retention)
			}
			if scenario.option == "runbooks" && result.Retention.RunbookSource != "linked_artifacts" {
				t.Fatalf("Runbook provenance missing: %+v", result.Retention)
			}
			saved[scenario.option] = result.ID
			var ownerID, env string
			var raw []byte
			if err := db.QueryRow(ctx, `SELECT owner_id,environment_id,nodes FROM v3_canvas_templates WHERE id=$1`, result.ID).Scan(&ownerID, &env, &raw); err != nil {
				t.Fatal(err)
			}
			if ownerID != owner.ID || env != "dev" || strings.Contains(string(raw), threadText) || strings.Contains(string(raw), artifactText) {
				t.Fatal("retention persisted wrong privacy scope or unselected source data")
			}
			if strings.Contains(string(raw), snapshotText) != (scenario.option == "content") {
				t.Fatal("only explicit content retention may persist private node text")
			}
			metadata, err := decodePrivateTemplateMetadata(raw, true)
			if err != nil || len(metadata.SourceServices) != 1 || metadata.SourceServices[0] != "retention-app" {
				t.Fatalf("source scope not retained: %+v (%v)", metadata, err)
			}
			var audit struct {
				Steps    int    `json:"retained_steps"`
				Runbooks int    `json:"retained_runbooks"`
				Content  int    `json:"retained_content_blocks"`
				Source   string `json:"source_canvas_id"`
			}
			var auditRaw []byte
			if err := db.QueryRow(ctx, `SELECT changes FROM management_audit WHERE actor_id=$1 AND target=$2 AND action='v3.canvas_template.saved'`, owner.ID, result.ID).Scan(&auditRaw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(auditRaw, &audit); err != nil || audit.Steps != scenario.steps || audit.Runbooks != scenario.runbooks || audit.Content != scenario.content || audit.Source != sourceID {
				t.Fatalf("retention receipt/audit mismatch: %s (%v)", auditRaw, err)
			}
			createBody := fmt.Sprintf(`{"kind":"plan","title":"Fresh canvas","template_id":%q,"services":["retention-app"]}`, result.ID)
			fresh := request(http.MethodPost, "/api/v1/canvases", "dev", ownerToken, createBody)
			assert(fresh, 201, "")
			var createdID struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(fresh.Body.Bytes(), &createdID); err != nil || createdID.ID == "" {
				t.Fatal("created canvas ID missing")
			}
			detail := request(http.MethodGet, "/api/v1/canvases/"+createdID.ID, "dev", ownerToken, "")
			assert(detail, 200, "")
			var doc struct {
				Nodes     []Node                `json:"nodes"`
				Artifacts []CanvasArtifact      `json:"artifacts"`
				Threads   map[string]nodeThread `json:"threads"`
			}
			if err := json.Unmarshal(detail.Body.Bytes(), &doc); err != nil || len(doc.Nodes) != 1 {
				t.Fatalf("invalid fresh canvas: %s (%v)", detail.Body.String(), err)
			}
			for _, thread := range doc.Threads {
				if len(thread.Turns) != 0 {
					t.Fatal("source conversation copied")
				}
			}
			if strings.Contains(detail.Body.String(), threadText) || strings.Contains(detail.Body.String(), artifactText) || strings.Contains(detail.Body.String(), "private_template_v1") {
				t.Fatal("private source data or internal metadata leaked into canvas")
			}
			switch scenario.option {
			case "steps":
				var block struct {
					Type  string       `json:"type"`
					Items []canvasStep `json:"items"`
				}
				if len(doc.Nodes[0].Blocks) != 1 || json.Unmarshal(doc.Nodes[0].Blocks[0], &block) != nil || block.Type != "steps" || len(block.Items) != 2 {
					t.Fatal("retained steps missing")
				}
				if block.Items[0].State != "todo" || block.Items[1].State != "gate" {
					t.Fatalf("execution/approval states changed unsafely: %+v", block.Items)
				}
				for _, step := range block.Items {
					if !strings.HasPrefix(step.Sub, pendingTemplateStepPrefix) {
						t.Fatal("retained step lacks pending review notice")
					}
				}
			case "content":
				if doc.Nodes[0].Title != snapshotText || len(doc.Nodes[0].Blocks) != 2 || !strings.Contains(detail.Body.String(), snapshotText) {
					t.Fatal("explicit private snapshot not retained")
				}
				if strings.Contains(detail.Body.String(), "stale-live-") || strings.Contains(detail.Body.String(), "123456789") {
					t.Fatal("live observation copied as current content")
				}
			case "runbooks":
				if len(doc.Artifacts) != 1 || doc.Artifacts[0].Href != "/runbooks/retained-runbook" || doc.Artifacts[0].Title != "Published Runbook" || !strings.Contains(doc.Artifacts[0].Sub, "Runbook v1") || !strings.Contains(doc.Artifacts[0].Sub, "未执行，待复核") || len(doc.Nodes[0].Blocks) != 0 {
					t.Fatalf("Runbook reference not resolved safely: %+v", doc.Artifacts)
				}
			}
			assert(request(http.MethodGet, "/api/v1/canvases/"+createdID.ID, "dev", otherToken, ""), 404, "not_found")
			assert(request(http.MethodPost, "/api/v1/canvases", "dev", otherToken, createBody), 404, "template_not_found")
			assert(request(http.MethodPost, "/api/v1/canvases", "staging", ownerToken, fmt.Sprintf(`{"kind":"plan","title":"Wrong environment","template_id":%q,"services":[]}`, result.ID)), 404, "template_not_found")
		})
	}
	otherList := request(http.MethodGet, "/api/v1/canvas-templates", "dev", otherToken, "")
	assert(otherList, 200, "")
	for _, id := range saved {
		if strings.Contains(otherList.Body.String(), id) {
			t.Fatal("another owner can list private retained template")
		}
	}
	if strings.Contains(otherList.Body.String(), snapshotText) {
		t.Fatal("private content leaked through template list")
	}
	// Source changes do not rewrite the explicitly selected private content snapshot.
	if _, err = db.Exec(ctx, `UPDATE v3_canvases SET nodes=$2::jsonb WHERE id=$1`, sourceID, mustJSON([]Node{{ID: "source", X: 20, Y: 30, W: 240, Icon: "alert", Tone: "warn", Title: "source changed after save", Blocks: []json.RawMessage{}}})); err != nil {
		t.Fatal(err)
	}
	contentCopy := request(http.MethodPost, "/api/v1/canvases", "dev", ownerToken, fmt.Sprintf(`{"kind":"plan","title":"Snapshot copy","template_id":%q,"services":["retention-app"]}`, saved["content"]))
	assert(contentCopy, 201, "")
	var copied struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(contentCopy.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	copyDetail := request(http.MethodGet, "/api/v1/canvases/"+copied.ID, "dev", ownerToken, "")
	assert(copyDetail, 200, "")
	if !strings.Contains(copyDetail.Body.String(), snapshotText) || strings.Contains(copyDetail.Body.String(), "source changed after save") {
		t.Fatal("template content was reread from changed source")
	}
	createRunbook := fmt.Sprintf(`{"kind":"plan","title":"Runbook copy","template_id":%q,"services":["retention-app"]}`, saved["runbooks"])
	var beforeCanvases, beforeTemplates, beforeAudit int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_canvases),(SELECT count(*) FROM v3_canvas_templates),(SELECT count(*) FROM management_audit)`).Scan(&beforeCanvases, &beforeTemplates, &beforeAudit); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='canvas-editor' AND module='knowledge'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "/api/v1/canvases", "dev", ownerToken, createRunbook), 403, "template_reference_forbidden")
	assert(request(http.MethodPost, "/api/v1/canvas-templates", "dev", ownerToken, fmt.Sprintf(`{"from_canvas":%q,"name":"Revoked Runbook","keep":{"nodes":true,"runbooks":true}}`, sourceID)), 403, "template_reference_forbidden")
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES('canvas-editor','knowledge','view','application','retention-app')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET version=2 WHERE id='retained-runbook'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "/api/v1/canvases", "dev", ownerToken, createRunbook), 409, "template_runbook_unavailable")
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET version=1,lifecycle='draft' WHERE id='retained-runbook'`); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodPost, "/api/v1/canvases", "dev", ownerToken, createRunbook), 409, "template_runbook_unavailable")
	var afterCanvases, afterTemplates, afterAudit int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM v3_canvases),(SELECT count(*) FROM v3_canvas_templates),(SELECT count(*) FROM management_audit)`).Scan(&afterCanvases, &afterTemplates, &afterAudit); err != nil {
		t.Fatal(err)
	}
	if afterCanvases != beforeCanvases || afterTemplates != beforeTemplates || afterAudit != beforeAudit {
		t.Fatal("rejected references must roll back canvas/template and audit together")
	}
}
