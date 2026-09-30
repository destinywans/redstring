package v3canvas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/inference"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
	"github.com/wans/aiops-canvas/internal/v3models"
)

type canvasDraftFakeRunner struct {
	run func(inference.Request) (string, error)
}

func (*canvasDraftFakeRunner) Models() []inference.Model {
	return []inference.Model{{ID: "draft-test", Name: "Draft Test", Provider: "Test"}}
}

func (f *canvasDraftFakeRunner) Run(_ context.Context, req inference.Request, _ func(inference.Event) error) (string, error) {
	return f.run(req)
}

func TestBoundCanvasDraftUsesSelectedEnvironment(t *testing.T) {
	called := 0
	fake := &canvasDraftFakeRunner{run: func(req inference.Request) (string, error) {
		called++
		if req.EnvironmentID != "dev" || req.ModelID != "draft-test" {
			t.Fatalf("wrong draft binding: %+v", req)
		}
		return `{"nodes":[{"title":"Check","summary":"Unverified outline"}]}`, nil
	}}
	path := filepath.Join(t.TempDir(), "bindings.json")
	if err := os.WriteFile(path, []byte(`{"tier":"nonprod","environments":[{"id":"dev","models":["draft-test"],"default_model":"draft-test"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	bindings, err := v3models.LoadBindings(path, "nonprod", []string{"dev"}, fake)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewWithRuntime(nil, v3models.NewBoundRunner(fake, bindings))
	if _, _, failure := svc.generateDraft(t.Context(), "test", createRequest{Title: "Wrong environment"}); failure == nil || called != 0 {
		t.Fatalf("unbound environment used provider: failure=%v calls=%d", failure, called)
	}
	if nodes, model, failure := svc.generateDraft(t.Context(), "dev", createRequest{Title: "Right environment"}); failure != nil || model != "draft-test" || len(nodes) != 1 || called != 1 {
		t.Fatalf("bound draft: nodes=%+v model=%q failure=%v calls=%d", nodes, model, failure, called)
	}
}

func TestCanvasCreateRegisteredRoutePersistsAndAuthorizes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_canvas_create_" + store.ID()
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
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
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
 ('canvas-create-env','Environment Editor'),('canvas-create-app','Application Editor'),
 ('canvas-create-team','Team Editor'),('canvas-create-reader','Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('canvas-create-env','environment','view','environment','dev'),
 ('canvas-create-env','environment','view','environment','staging'),
 ('canvas-create-env','canvas','view','environment','dev'),
 ('canvas-create-env','canvas','change','environment','dev'),
 ('canvas-create-app','environment','view','environment','dev'),
 ('canvas-create-app','canvas','view','application','app-one'),
 ('canvas-create-app','canvas','change','application','app-one'),
 ('canvas-create-team','environment','view','environment','dev'),
 ('canvas-create-team','canvas','view','team','team-one'),
 ('canvas-create-team','canvas','change','team','team-one'),
 ('canvas-create-reader','environment','view','environment','dev'),
 ('canvas-create-reader','canvas','view','environment','dev')`,
		`INSERT INTO teams(id,name) VALUES ('team-one','One'),('team-two','Two')`,
		`INSERT INTO applications(id,name,key,owner_team_id,archived) VALUES
 ('app-one','One','app-one','team-one',false),
 ('app-two','Two','app-two','team-two',false),
 ('app-archived','Archived','app-archived','team-one',true),
 ('app-stage','Staging','app-stage','team-one',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('ae-one','app-one','dev','Development',false),
 ('ae-two','app-two','dev','Development',false),
 ('ae-archived','app-archived','dev','Development',false),
 ('ae-stage','app-stage','staging','Staging',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-one','dev'),('app-two','dev'),('app-archived','dev'),('app-stage','staging')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	createUser := func(email, role string) (core.User, string) {
		t.Helper()
		u, err := st.CreateUser(ctx, core.User{Email: email, Name: email, Role: "viewer"}, "isolated-user-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,$2)`, u.ID, role); err != nil {
			t.Fatal(err)
		}
		_, token, err := st.Login(ctx, email, "isolated-user-password")
		if err != nil {
			t.Fatal(err)
		}
		return u, token
	}
	envUser, envToken := createUser("env@example.invalid", "canvas-create-env")
	appUser, appToken := createUser("app@example.invalid", "canvas-create-app")
	teamUser, teamToken := createUser("team@example.invalid", "canvas-create-team")
	_, readerToken := createUser("reader@example.invalid", "canvas-create-reader")
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	requestOn := func(target http.Handler, token, environment, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/canvases", strings.NewReader(body))
		r.Header.Set("X-AIOps-Environment", environment)
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		target.ServeHTTP(w, r)
		return w
	}
	request := func(token, environment, body string) *httptest.ResponseRecorder {
		return requestOn(api, token, environment, body)
	}
	assert := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("status=%d body=%s; want %d %q", w.Code, w.Body.String(), status, code)
		}
	}
	created := func(w *httptest.ResponseRecorder, prefix string) string {
		t.Helper()
		assert(w, 201, "")
		var result struct {
			ID   string `json:"id"`
			Href string `json:"href"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(result.ID, prefix) || result.Href != "/canvas/"+result.ID {
			t.Fatalf("unexpected persisted canvas identity: %+v", result)
		}
		return result.ID
	}
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A generic canvas is a durable private document, with its audit in the
	// same successful request. The default template supplies safe structure.
	genericID := created(request(envToken, "dev", `{"title":"Capacity plan","kind":"plan","goal":"Prepare a scale review","template_id":"v3-blank-plan","services":["app-one"]}`), "CV-")
	var owner, environment, title, kind, goal, status string
	var services []string
	var nodes, edges, artifacts []byte
	if err := db.QueryRow(ctx, `SELECT owner_id,environment_id,title,kind,goal,status,services,nodes,edges,artifacts
 FROM v3_canvases WHERE id=$1`, genericID).Scan(&owner, &environment, &title, &kind, &goal, &status, &services, &nodes, &edges, &artifacts); err != nil {
		t.Fatal(err)
	}
	if owner != envUser.ID || environment != "dev" || title != "Capacity plan" || kind != "plan" || goal != "Prepare a scale review" || status != "draft" ||
		len(services) != 1 || services[0] != "app-one" {
		t.Fatalf("generic persisted fields mismatch: owner=%q env=%q title=%q kind=%q goal=%q status=%q services=%v", owner, environment, title, kind, goal, status, services)
	}
	decodedNodes, decodedEdges, decodedArtifacts, err := decodeCanvasDocument(nodes, edges, artifacts)
	if err != nil || len(decodedNodes) != 1 || decodedNodes[0].Title != title || len(decodedEdges) != 0 || len(decodedArtifacts) != 0 {
		t.Fatalf("generic structure mismatch: nodes=%s edges=%s artifacts=%s err=%v", nodes, edges, artifacts, err)
	}
	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.canvas.created'
 AND target=$2 AND outcome='success' AND changes->>'environment_id'='dev'`, envUser.ID, genericID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("generic audit mismatch: count=%d err=%v", audits, err)
	}

	// An audit storage error must prevent a canvas without a matching receipt.
	if _, err = db.Exec(ctx, `CREATE FUNCTION fail_canvas_create_audit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN IF NEW.action='v3.canvas.created' THEN RAISE EXCEPTION 'forced canvas audit failure'; END IF;
 RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CREATE TRIGGER fail_canvas_create_audit BEFORE INSERT ON management_audit
 FOR EACH ROW EXECUTE FUNCTION fail_canvas_create_audit()`); err != nil {
		t.Fatal(err)
	}
	beforeCanvases, beforeAudits := count("v3_canvases"), count("management_audit")
	failedAudit := request(envToken, "dev", `{"title":"No unaudited canvas","kind":"plan","services":[]}`)
	if failedAudit.Code < 500 || count("v3_canvases") != beforeCanvases || count("management_audit") != beforeAudits {
		t.Fatalf("audit failure left a canvas or receipt: status=%d body=%s", failedAudit.Code, failedAudit.Body.String())
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER fail_canvas_create_audit ON management_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DROP FUNCTION fail_canvas_create_audit()`); err != nil {
		t.Fatal(err)
	}

	// Authorization must use the live session and role rows, not the login-time
	// identity alone. A read-only user cannot create a canvas.
	assert(request(readerToken, "dev", `{"title":"Reader attempt","kind":"plan"}`), 403, "forbidden")
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='canvas-create-env' AND module='canvas' AND action='change'`); err != nil {
		t.Fatal(err)
	}
	assert(request(envToken, "dev", `{"title":"Revoked attempt","kind":"plan"}`), 403, "forbidden")
	if count("v3_canvases") != beforeCanvases {
		t.Fatal("revoked canvas writer persisted a document")
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, store.Hash(envToken)); err != nil {
		t.Fatal(err)
	}
	assert(request(envToken, "dev", `{"title":"Expired session","kind":"plan"}`), 401, "unauthorized")

	// Application and owner-team scoped change grants are enough for the
	// application-bound investigation. The result is a real legacy investigation.
	appID := created(request(appToken, "dev", `{"title":"Investigate checkout","kind":"investigation","services":["app-one"]}`), "INV-")
	teamID := created(request(teamToken, "dev", `{"title":"Investigate team","kind":"investigation","services":["app-one"]}`), "INV-")
	checkInvestigation := func(id string, user core.User) {
		t.Helper()
		var app, envID, persistedTitle, persistedStatus, creator, branchID string
		if err := db.QueryRow(ctx, `SELECT application_id,environment_id,title,status,created_by
 FROM investigations WHERE id=$1`, id).Scan(&app, &envID, &persistedTitle, &persistedStatus, &creator); err != nil {
			t.Fatal(err)
		}
		if app != "app-one" || envID != "ae-one" || persistedStatus != "open" || creator != user.ID || persistedTitle == "" {
			t.Fatalf("investigation persisted scope mismatch: app=%q env=%q title=%q status=%q creator=%q", app, envID, persistedTitle, persistedStatus, creator)
		}
		if err := db.QueryRow(ctx, `SELECT id FROM branches WHERE investigation_id=$1 AND parent_id IS NULL AND revision=0`, id).Scan(&branchID); err != nil {
			t.Fatalf("root branch missing: %v", err)
		}
		var messages, provenance []byte
		if err := db.QueryRow(ctx, `SELECT messages,provenance FROM context_revisions WHERE branch_id=$1 AND revision=0`, branchID).Scan(&messages, &provenance); err != nil {
			t.Fatalf("initial branch context missing: %v", err)
		}
		if !json.Valid(messages) || !json.Valid(provenance) {
			t.Fatalf("initial context invalid: %s %s", messages, provenance)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM events WHERE investigation_id=$1 AND branch_id=$2 AND type='investigation.created'`, id, branchID).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("created event mismatch: count=%d err=%v", audits, err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE actor_id=$1 AND action='v3.investigation_canvas.created'
 AND target=$2 AND outcome='success' AND application_id='app-one' AND changes->>'environment_id'='dev'`, user.ID, id).Scan(&audits); err != nil || audits != 1 {
			t.Fatalf("investigation audit mismatch: count=%d err=%v", audits, err)
		}
	}
	checkInvestigation(appID, appUser)
	checkInvestigation(teamID, teamUser)

	// Unrelated apps, archived catalog entries, wrong environment and multiple
	// services are rejected before any legacy investigation rows are inserted.
	beforeInvestigations, beforeBranches, beforeEvents := count("investigations"), count("branches"), count("events")
	assert(request(appToken, "dev", `{"title":"Other app","kind":"investigation","services":["app-two"]}`), 403, "forbidden")
	assert(request(appToken, "dev", `{"title":"Unknown app","kind":"investigation","services":["app-unknown"]}`), 403, "forbidden")
	assert(request(appToken, "dev", `{"title":"Archived app","kind":"investigation","services":["app-archived"]}`), 403, "forbidden")
	assert(request(appToken, "dev", `{"title":"Wrong environment","kind":"investigation","services":["app-stage"]}`), 403, "forbidden")
	assert(request(appToken, "dev", `{"title":"Ambiguous app","kind":"investigation","services":["app-one","app-two"]}`), 422, "invalid_service")
	assert(request(appToken, "all", `{"title":"All environments","kind":"investigation","services":["app-one"]}`), 409, "env_view_only")
	if count("investigations") != beforeInvestigations || count("branches") != beforeBranches || count("events") != beforeEvents {
		t.Fatal("rejected investigation create persisted legacy rows")
	}
	// The request contract exposes these controls. They must fail explicitly
	// while no generation runner or investigation template adapter is installed.
	assert(request(appToken, "dev", `{"title":"Draft","kind":"investigation","services":["app-one"],"ai_draft":true}`), 503, "canvas_draft_unavailable")
	assert(request(appToken, "dev", `{"title":"Template","kind":"investigation","services":["app-one"],"template_id":"v3-blank-plan"}`), 422, "invalid_template")
	if count("investigations") != beforeInvestigations {
		t.Fatal("unsupported investigation option persisted a row")
	}

	// The optional AI draft uses a controlled model output, with no tools or
	// purported evidence. Only validated, bounded structure may be persisted.
	_, freshEnvToken, err := st.Login(ctx, "env@example.invalid", "isolated-user-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('canvas-create-env','canvas','change','environment','dev')`); err != nil {
		t.Fatal(err)
	}
	called := 0
	fake := &canvasDraftFakeRunner{run: func(req inference.Request) (string, error) {
		called++
		if len(req.Tools) != 0 || len(req.Messages) == 0 {
			t.Fatalf("draft must be bounded and tool-free: %+v", req)
		}
		prompt := ""
		for _, m := range req.Messages {
			prompt += m.Content
		}
		if !strings.Contains(prompt, "app-one") {
			t.Fatalf("draft omitted verified service identity: %s", prompt)
		}
		return `{"nodes":[{"title":"Check health","summary":"Read current health"},{"title":"Review capacity","summary":"Assess limits"}]}`, nil
	}}
	draftAPI := httpapi.New(st, nil, true)
	NewWithRuntime(st, fake).Register(draftAPI)
	if err = draftAPI.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	assert(requestOn(draftAPI, freshEnvToken, "dev", `{"title":"Missing template","kind":"plan","template_id":"does-not-exist","services":["app-one"],"ai_draft":true}`), 404, "template_not_found")
	if called != 0 {
		t.Fatalf("invalid template incurred %d model calls", called)
	}
	genericDraftID := created(requestOn(draftAPI, freshEnvToken, "dev", `{"title":"Capacity draft","kind":"plan","goal":"Prepare capacity review","services":["app-one"],"ai_draft":true}`), "CV-")
	if err := db.QueryRow(ctx, `SELECT nodes,edges FROM v3_canvases WHERE id=$1`, genericDraftID).Scan(&nodes, &edges); err != nil {
		t.Fatal(err)
	}
	draftNodes, draftEdges, _, err := decodeCanvasDocument(nodes, edges, []byte(`[]`))
	if err != nil || len(draftNodes) != 3 || len(draftEdges) != 2 || draftNodes[1].Title != "Check health" || draftNodes[2].Title != "Review capacity" {
		t.Fatalf("generic draft structure mismatch: nodes=%s edges=%s err=%v", nodes, edges, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM management_audit WHERE action='v3.canvas.created' AND target=$1`, genericDraftID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("generic draft audit mismatch: count=%d err=%v", audits, err)
	}
	investigationDraftID := created(requestOn(draftAPI, appToken, "dev", `{"title":"Checkout draft","kind":"investigation","goal":"Find likely causes","services":["app-one"],"ai_draft":true}`), "INV-")
	var branchTitles []string
	rows, err := db.Query(ctx, `SELECT title FROM branches WHERE investigation_id=$1 ORDER BY parent_id NULLS FIRST,title`, investigationDraftID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var title string
		if err = rows.Scan(&title); err != nil {
			break
		}
		branchTitles = append(branchTitles, title)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(branchTitles) != 3 || branchTitles[1] != "Check health" || branchTitles[2] != "Review capacity" {
		t.Fatalf("investigation draft branches mismatch: titles=%v err=%v", branchTitles, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM context_revisions r JOIN branches b ON b.id=r.branch_id
 WHERE b.investigation_id=$1 AND r.revision=0`, investigationDraftID).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("investigation draft initial contexts mismatch: count=%d err=%v", audits, err)
	}
	if called != 2 {
		t.Fatalf("expected one model call per successful draft, got %d", called)
	}
	beforeCanvases, beforeInvestigations, beforeAudits = count("v3_canvases"), count("investigations"), count("management_audit")
	fake.run = func(inference.Request) (string, error) { return `{"nodes":[{"title":"Incomplete"}]}`, nil }
	malformed := requestOn(draftAPI, freshEnvToken, "dev", `{"title":"Bad draft","kind":"plan","services":["app-one"],"ai_draft":true}`)
	if malformed.Code < 400 || count("v3_canvases") != beforeCanvases || count("management_audit") != beforeAudits {
		t.Fatalf("malformed model draft persisted canvas/audit: %d %s", malformed.Code, malformed.Body.String())
	}
	fake.run = func(inference.Request) (string, error) { return "", errors.New("model unavailable") }
	failedModel := requestOn(draftAPI, appToken, "dev", `{"title":"Failed draft","kind":"investigation","services":["app-one"],"ai_draft":true}`)
	if failedModel.Code < 500 || count("investigations") != beforeInvestigations || count("management_audit") != beforeAudits {
		t.Fatalf("failed model draft persisted investigation/audit: %d %s", failedModel.Code, failedModel.Body.String())
	}
	// A grant revoked while the model is running must fail the final write
	// decision even though preflight authorized the expensive model call.
	fake.run = func(inference.Request) (string, error) {
		_, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='canvas-create-env' AND module='canvas' AND action='change'`)
		return `{"nodes":[{"title":"Stale permission","summary":"Must not be saved"}]}`, err
	}
	revokedDuringDraft := requestOn(draftAPI, freshEnvToken, "dev", `{"title":"Revoked during draft","kind":"plan","services":["app-one"],"ai_draft":true}`)
	assert(revokedDuringDraft, 403, "forbidden")
	if count("v3_canvases") != beforeCanvases || count("management_audit") != beforeAudits {
		t.Fatalf("revoked draft persisted canvas/audit: %d %s", revokedDuringDraft.Code, revokedDuringDraft.Body.String())
	}
}
