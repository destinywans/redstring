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
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true,"steps":true}}`), 422, "unsafe_template_content")
	assert(request(http.MethodPost, "dev", ownerToken, `{"from_canvas":"CV-source","name":"Unsafe","keep":{"nodes":true,"runbooks":true}}`), 422, "unsafe_template_content")
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
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'canvas-editor')`, owner.ID); err != nil {
		t.Fatal(err)
	}
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
	// Do not silently truncate a template list beyond its documented bound.
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvas_templates(id,owner_id,environment_id,kind,name)
 SELECT 'bulk-' || gs::text,$1,'dev','plan','Bulk' FROM generate_series(1,$2) AS gs`, owner.ID, maxTemplateItems); err != nil {
		t.Fatal(err)
	}
	assert(request(http.MethodGet, "dev", ownerToken, ""), 422, "template_scope_too_broad")
}
