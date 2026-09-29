package v3canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

// These requests go through the registered V3 router. Each run owns a fresh
// PostgreSQL schema, so legacy investigation IDs and new INV-* IDs can be
// checked against the same persisted application and branch records.
func TestInvestigationCanvasRegisteredRoutes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_inv_canvas_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *pgxpool.Pool
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
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
	for _, sql := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES
 ('inv-owner','Investigation Owner'),('inv-app-view','App View'),('inv-team-view','Team View'),
 ('inv-app-change','App Change'),('inv-team-change','Team Change'),('inv-env-only','Environment Only')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('inv-owner','environment','view','environment','test'),
 ('inv-owner','canvas','view','environment','test'),('inv-owner','canvas','change','environment','test'),
 ('inv-app-view','environment','view','environment','test'),('inv-app-view','canvas','view','application','app-one'),
 ('inv-team-view','environment','view','environment','test'),('inv-team-view','canvas','view','team','team-one'),
 ('inv-app-change','environment','view','environment','test'),('inv-app-change','canvas','view','application','app-one'),('inv-app-change','canvas','change','application','app-one'),
 ('inv-team-change','environment','view','environment','test'),('inv-team-change','canvas','view','team','team-one'),('inv-team-change','canvas','change','team','team-one'),
 ('inv-env-only','environment','view','environment','test')`,
		`INSERT INTO teams(id,name) VALUES ('team-one','One'),('team-two','Two')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-one','One','app-one','team-one'),('app-two','Two','app-two','team-two')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-one','app-one','test','Development'),('ae-two','app-two','test','Development')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-one','test'),('app-two','test')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
 ('INV-persisted','app-one','Persisted investigation','open','ae-one'),
 ('11111111111111111111111111111111','app-one','Historical investigation','open','ae-one'),
 ('INV-hidden','app-two','Other application secret','open','ae-two')`,
		`INSERT INTO branches(id,investigation_id,title,model_id,revision) VALUES
 ('root-main','INV-persisted','Main','',1),
 ('root-history','11111111111111111111111111111111','Historical root','',0),
 ('root-hidden','INV-hidden','Hidden root','',0)`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('root-main',0,'[{"id":"m0","role":"user","content":"old context"}]','{"kind":"initial"}'),
 ('root-main',1,'[{"id":"m1","role":"user","content":"current parent context"}]','{"kind":"legacy_run"}'),
 ('root-history',0,'[]','{"kind":"initial"}'),
 ('root-hidden',0,'[{"id":"secret","role":"user","content":"secret"}]','{"kind":"initial"}')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('inv-owner','environment','view','environment','dev'),('inv-owner','canvas','view','application','app-dev')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES ('app-dev','Legacy Dev','app-dev','team-one')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES ('ae-dev','app-dev','dev','Development')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-dev','dev')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES ('INV-dev','app-dev','Unsupported environment','open','ae-dev')`,
		`INSERT INTO branches(id,investigation_id,title) VALUES ('root-dev','INV-dev','Main')`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES ('root-dev',0,'[]','{"kind":"initial"}')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	createUser := func(email, role string) (core.User, string) {
		t.Helper()
		u, e := st.CreateUser(ctx, core.User{Email: email, Name: email, Role: "viewer"}, "isolated-user-password")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, u.ID, role); e != nil {
			t.Fatal(e)
		}
		_, token, e := st.Login(ctx, email, "isolated-user-password")
		if e != nil {
			t.Fatal(e)
		}
		return u, token
	}
	_, ownerToken := createUser("owner@example.invalid", "inv-owner")
	_, appViewToken := createUser("app-view@example.invalid", "inv-app-view")
	_, teamViewToken := createUser("team-view@example.invalid", "inv-team-view")
	_, appChangeToken := createUser("app-change@example.invalid", "inv-app-change")
	_, teamChangeToken := createUser("team-change@example.invalid", "inv-team-change")
	_, envOnlyToken := createUser("env-only@example.invalid", "inv-env-only")
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test", "staging", "dev"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, token, environment, path, body string) *httptest.ResponseRecorder {
		t.Helper()
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
	get := func(token, env, id string) *httptest.ResponseRecorder {
		return request(http.MethodGet, token, env, "/api/v1/investigations/"+id+"/canvas", "")
	}
	post := func(token, env, id, body string) *httptest.ResponseRecorder {
		return request(http.MethodPost, token, env, "/api/v1/investigations/"+id+"/branches", body)
	}
	patchNode := func(token, env, id, node, body string) *httptest.ResponseRecorder {
		return request(http.MethodPatch, token, env, "/api/v1/investigations/"+id+"/nodes/"+node, body)
	}
	assertStatus := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), want)
		}
	}
	var graph struct {
		Investigation struct {
			ID           string   `json:"id"`
			Title        string   `json:"title"`
			Status       string   `json:"status"`
			Env          string   `json:"env"`
			Participants []string `json:"participants"`
		} `json:"investigation"`
		Loop  []json.RawMessage `json:"loop"`
		Nodes []struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			Parent        string `json:"parent"`
			Title         string `json:"title"`
			Status        string `json:"status"`
			X             int    `json:"x"`
			Y             int    `json:"y"`
			W             int    `json:"w"`
			Collapsed     *bool  `json:"collapsed"`
			LayoutVersion *int64 `json:"layout_version"`
			Context       struct {
				Version  string          `json:"version"`
				Messages int             `json:"messages"`
				Tokens   json.RawMessage `json:"tokens"`
				Excludes []string        `json:"excludes"`
			} `json:"context"`
			Versions []json.RawMessage `json:"versions"`
		} `json:"nodes"`
		Tray []json.RawMessage `json:"tray"`
	}
	for _, token := range []string{ownerToken, appViewToken, teamViewToken} {
		w := get(token, "test", "INV-persisted")
		assertStatus(w, 200)
		if err = json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
			t.Fatal(err)
		}
		if graph.Investigation.ID != "INV-persisted" || graph.Investigation.Title != "Persisted investigation" || graph.Investigation.Env != "test" ||
			graph.Investigation.Participants == nil || graph.Loop == nil || graph.Tray == nil || len(graph.Nodes) != 1 ||
			graph.Nodes[0].ID != "root-main" || graph.Nodes[0].Kind != "root" || graph.Nodes[0].Context.Messages != 1 ||
			graph.Nodes[0].Context.Version == "" || string(graph.Nodes[0].Context.Tokens) != "null" ||
			graph.Nodes[0].W <= 0 || graph.Nodes[0].LayoutVersion == nil ||
			graph.Nodes[0].Collapsed == nil || strings.Contains(w.Body.String(), "secret") ||
			strings.Contains(w.Body.String(), "old context") {
			t.Fatalf("graph not sourced from current persisted records: %s", w.Body.String())
		}
	}
	assertStatus(get(appViewToken, "test", "11111111111111111111111111111111"), 200)
	assertStatus(get(ownerToken, "dev", "INV-dev"), 422)
	for _, tc := range []struct {
		name, token, env, id string
		want                 int
	}{
		{"no object grant", envOnlyToken, "test", "INV-persisted", 404},
		{"other application", appViewToken, "test", "INV-hidden", 404},
		{"other owner team", teamViewToken, "test", "INV-hidden", 404},
		{"unknown investigation", appViewToken, "test", "INV-unknown", 404},
		{"wrong environment", appViewToken, "staging", "INV-persisted", 403},
	} {
		t.Run("GET "+tc.name, func(t *testing.T) {
			assertStatus(get(tc.token, tc.env, tc.id), tc.want)
		})
	}
	if w := post(appViewToken, "test", "INV-persisted", `{"parent_id":"root-main","title":"Read only"}`); w.Code != 403 && w.Code != 404 {
		t.Fatalf("read-only principal could fork: %d %s", w.Code, w.Body.String())
	}
	if w := post(appChangeToken, "test", "INV-hidden", `{"parent_id":"root-hidden","title":"Cross app"}`); w.Code != 404 {
		t.Fatalf("cross-app branch write: %d %s", w.Code, w.Body.String())
	}
	if w := post(appChangeToken, "test", "INV-persisted", `{"parent_id":"root-hidden","title":"Cross investigation"}`); w.Code != 404 {
		t.Fatalf("cross-investigation parent accepted: %d %s", w.Code, w.Body.String())
	}
	for _, token := range []string{appChangeToken, teamChangeToken} {
		w := post(token, "test", "INV-persisted", `{"parent_id":"root-main","title":"Manual child","x":360,"y":80}`)
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("authorized fork=%d %s", w.Code, w.Body.String())
		}
		var child struct {
			ID            string `json:"id"`
			Kind          string `json:"kind"`
			Parent        string `json:"parent"`
			Title         string `json:"title"`
			LayoutVersion *int64 `json:"layout_version"`
			Context       struct {
				Version  string `json:"version"`
				Inherits string `json:"inherits"`
				Messages int    `json:"messages"`
			} `json:"context"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &child); err != nil {
			t.Fatal(err)
		}
		if child.ID == "" || child.Kind != "branch" || child.Parent != "root-main" || child.Title != "Manual child" ||
			child.Context.Version == "" || child.Context.Messages != 1 || child.LayoutVersion == nil {
			t.Fatalf("fork response is not a persisted CanvasNode: %s", w.Body.String())
		}
		var parent, title string
		var inherited, revision int64
		if err = db.QueryRow(ctx, `SELECT parent_id,title,inherited_parent_revision,revision FROM branches WHERE id=$1 AND investigation_id='INV-persisted'`, child.ID).
			Scan(&parent, &title, &inherited, &revision); err != nil {
			t.Fatal(err)
		}
		if parent != "root-main" || title != "Manual child" || inherited != 1 || revision != 0 {
			t.Fatalf("fork did not pin parent revision: parent=%q title=%q inherited=%d revision=%d", parent, title, inherited, revision)
		}
		var raw []byte
		if err = db.QueryRow(ctx, `SELECT messages FROM context_revisions WHERE branch_id=$1 AND revision=0`, child.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "current parent context") || strings.Contains(string(raw), "old context") {
			t.Fatalf("fork inherited wrong revision: %s", raw)
		}
		var audits int
		if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE target=$1 AND outcome='success' AND action LIKE 'v3.investigation%'`, child.ID).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("fork audit count=%d err=%v", audits, err)
		}
	}
	var branchCount, auditCount int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM branches WHERE investigation_id='INV-persisted'`).Scan(&branchCount); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE context_revisions SET messages=jsonb_build_array(jsonb_build_object('id','huge','role','user','content',repeat('x',30000)))
 WHERE branch_id='root-main' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	if w := post(teamChangeToken, "test", "INV-persisted", `{"parent_id":"root-main","title":"Oversized fork"}`); w.Code != 413 {
		t.Fatalf("oversized parent context fork=%d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE context_revisions SET messages='[{"id":"m1","role":"user","content":"current parent context"}]'::jsonb
 WHERE branch_id='root-main' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	if w := post(teamChangeToken, "test", "INV-persisted", `{"parent_id":"root-main","title":"Stale fork","expected_parent_revision":0}`); w.Code != 409 {
		t.Fatalf("stale parent revision=%d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='closed' WHERE id='11111111111111111111111111111111'`); err != nil {
		t.Fatal(err)
	}
	if w := post(teamChangeToken, "test", "11111111111111111111111111111111", `{"parent_id":"root-history","title":"Closed fork"}`); w.Code != 409 {
		t.Fatalf("closed investigation fork=%d %s", w.Code, w.Body.String())
	}
	var afterBranches, afterRejectedAudits int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM branches WHERE investigation_id='INV-persisted'`).Scan(&afterBranches); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit`).Scan(&afterRejectedAudits); err != nil {
		t.Fatal(err)
	}
	if afterBranches != branchCount || afterRejectedAudits != auditCount {
		t.Fatalf("rejected fork mutated branch/audit: branches=%d/%d audits=%d/%d", branchCount, afterBranches, auditCount, afterRejectedAudits)
	}
	// Layout edits have their own version. A stale drag cannot overwrite a
	// newer title or collapsed state, and context revision remains untouched.
	oldLayoutVersion := *graph.Nodes[0].LayoutVersion
	var beforeAudit int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.investigation%'`).Scan(&beforeAudit); err != nil {
		t.Fatal(err)
	}
	if w := patchNode(ownerToken, "test", "INV-persisted", "root-main", `{"x":450,"y":90}`); w.Code != 428 {
		t.Fatalf("unversioned PATCH must fail: %d %s", w.Code, w.Body.String())
	}
	if w := patchNode(appViewToken, "test", "INV-persisted", "root-main", `{"x":450,"expected_layout_version":0}`); w.Code != 403 && w.Code != 404 {
		t.Fatalf("read-only PATCH accepted: %d %s", w.Code, w.Body.String())
	}
	if w := patchNode(appChangeToken, "test", "INV-hidden", "root-hidden", `{"x":450,"expected_layout_version":0}`); w.Code != 404 {
		t.Fatalf("cross-app PATCH accepted: %d %s", w.Code, w.Body.String())
	}
	if w := patchNode(appChangeToken, "test", "INV-persisted", "root-hidden", `{"x":450,"expected_layout_version":0}`); w.Code != 404 {
		t.Fatalf("cross-investigation PATCH accepted: %d %s", w.Code, w.Body.String())
	}
	if w := patchNode(ownerToken, "test", "INV-persisted", "root-main", `{"title":"Unconditional edit","expected_layout_version":0}`); w.Code != 428 {
		t.Fatalf("title PATCH without expected_title=%d %s", w.Code, w.Body.String())
	}
	versionedPatch := `{"x":450,"y":90,"title":"Renamed root","expected_title":"Main","collapsed":true,"expected_layout_version":` + strconv.FormatInt(oldLayoutVersion, 10) + `}`
	w := patchNode(ownerToken, "test", "INV-persisted", "root-main", versionedPatch)
	if w.Code != 200 {
		t.Fatalf("versioned PATCH=%d %s", w.Code, w.Body.String())
	}
	var changed struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		X             int    `json:"x"`
		Y             int    `json:"y"`
		Collapsed     bool   `json:"collapsed"`
		LayoutVersion int64  `json:"layout_version"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &changed); err != nil {
		t.Fatal(err)
	}
	if changed.ID != "root-main" || changed.Title != "Renamed root" || changed.X != 450 || changed.Y != 90 ||
		!changed.Collapsed || changed.LayoutVersion != oldLayoutVersion+1 {
		t.Fatalf("PATCH response diverged from persisted node: %s", w.Body.String())
	}
	if w = patchNode(ownerToken, "test", "INV-persisted", "root-main", versionedPatch); w.Code != 409 {
		t.Fatalf("stale layout version=%d %s", w.Code, w.Body.String())
	}
	w = get(appViewToken, "test", "INV-persisted")
	assertStatus(w, 200)
	if err = json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.Nodes[0].Title != changed.Title || graph.Nodes[0].X != changed.X || graph.Nodes[0].Y != changed.Y ||
		graph.Nodes[0].Collapsed == nil || !*graph.Nodes[0].Collapsed || graph.Nodes[0].LayoutVersion == nil ||
		*graph.Nodes[0].LayoutVersion != changed.LayoutVersion || graph.Nodes[0].Context.Messages != 1 {
		t.Fatalf("GET did not return current persisted layout/context: %s", w.Body.String())
	}
	var persistedTitle string
	var persistedContextRevision int64
	if err = db.QueryRow(ctx, `SELECT title,revision FROM branches WHERE id='root-main'`).Scan(&persistedTitle, &persistedContextRevision); err != nil {
		t.Fatal(err)
	}
	if persistedTitle != "Renamed root" || persistedContextRevision != 1 {
		t.Fatalf("layout PATCH changed branch context: title=%q revision=%d", persistedTitle, persistedContextRevision)
	}
	var afterAudit int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.investigation%'`).Scan(&afterAudit); err != nil || afterAudit != beforeAudit+1 {
		t.Fatalf("PATCH audit not atomic: before=%d after=%d err=%v", beforeAudit, afterAudit, err)
	}
	// Two writers using the same observed layout version race at the actual
	// registered route. Exactly one update and one audit may commit.
	type patchRaceResult struct {
		x      int
		status int
		body   string
	}
	ready := make(chan struct{})
	results := make(chan patchRaceResult, 2)
	for _, x := range []int{501, 602} {
		go func(x int) {
			<-ready
			body := `{"x":` + strconv.Itoa(x) + `,"expected_layout_version":` + strconv.FormatInt(changed.LayoutVersion, 10) + `}`
			response := patchNode(ownerToken, "test", "INV-persisted", "root-main", body)
			results <- patchRaceResult{x: x, status: response.Code, body: response.Body.String()}
		}(x)
	}
	close(ready)
	first, second := <-results, <-results
	if !(first.status == 200 && second.status == 409 || first.status == 409 && second.status == 200) {
		t.Fatalf("concurrent same-version PATCH results: %+v, %+v", first, second)
	}
	winner := first.x
	if second.status == 200 {
		winner = second.x
	}
	w = get(appViewToken, "test", "INV-persisted")
	assertStatus(w, 200)
	if err = json.Unmarshal(w.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.Nodes[0].X != winner || graph.Nodes[0].LayoutVersion == nil || *graph.Nodes[0].LayoutVersion != changed.LayoutVersion+1 {
		t.Fatalf("concurrent PATCH lost CAS winner: %s", w.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.investigation%'`).Scan(&afterAudit); err != nil || afterAudit != beforeAudit+2 {
		t.Fatalf("concurrent PATCH audit count=%d err=%v", afterAudit, err)
	}
	// The legacy branch editor changes title without touching V3 layout
	// metadata. The V3 title compare protects against this cross-path race.
	legacyTitle := "Legacy edit"
	if _, err = st.PatchBranch(ctx, "root-main", &legacyTitle, nil); err != nil {
		t.Fatal(err)
	}
	currentLayoutVersion := *graph.Nodes[0].LayoutVersion
	staleTitlePatch := `{"title":"Incorrect overwrite","expected_title":"Renamed root","expected_layout_version":` + strconv.FormatInt(currentLayoutVersion, 10) + `}`
	if w = patchNode(ownerToken, "test", "INV-persisted", "root-main", staleTitlePatch); w.Code != 409 {
		t.Fatalf("legacy title change escaped V3 compare: %d %s", w.Code, w.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT title FROM branches WHERE id='root-main'`).Scan(&persistedTitle); err != nil || persistedTitle != legacyTitle {
		t.Fatalf("stale V3 PATCH replaced legacy title: %q err=%v", persistedTitle, err)
	}
	if err = db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action LIKE 'v3.investigation%'`).Scan(&afterAudit); err != nil || afterAudit != beforeAudit+2 {
		t.Fatalf("stale title PATCH wrote audit: count=%d err=%v", afterAudit, err)
	}
	currentTitlePatch := `{"title":"V3 after legacy","expected_title":"Legacy edit","expected_layout_version":` + strconv.FormatInt(currentLayoutVersion, 10) + `}`
	if w = patchNode(ownerToken, "test", "INV-persisted", "root-main", currentTitlePatch); w.Code != 200 {
		t.Fatalf("current title PATCH=%d %s", w.Code, w.Body.String())
	}
	if err = db.QueryRow(ctx, `SELECT title FROM branches WHERE id='root-main'`).Scan(&persistedTitle); err != nil || persistedTitle != "V3 after legacy" {
		t.Fatalf("current title PATCH not persisted: %q err=%v", persistedTitle, err)
	}
	// Team access follows the application's current owner, rather than a
	// snapshot captured when the user or investigation was created.
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-two' WHERE id='app-one'`); err != nil {
		t.Fatal(err)
	}
	assertStatus(get(teamViewToken, "test", "INV-persisted"), 404)
	if w = post(teamChangeToken, "test", "INV-persisted", `{"parent_id":"root-main","title":"Old team"}`); w.Code != 404 {
		t.Fatalf("stale owner-team grant created branch: %d %s", w.Code, w.Body.String())
	}
	assertStatus(get(appViewToken, "test", "INV-persisted"), 200)
	// A deleted role assignment and a deleted session must immediately stop
	// subsequent object reads and writes through the registered routes.
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='inv-app-change' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	if w := post(appChangeToken, "test", "INV-persisted", `{"parent_id":"root-main","title":"Revoked child"}`); w.Code != 403 && w.Code != 404 {
		t.Fatalf("revoked writer persisted branch: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(teamViewToken)); err != nil {
		t.Fatal(err)
	}
	assertStatus(get(teamViewToken, "test", "INV-persisted"), 401)
}
