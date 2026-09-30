package v3canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/aiextensions"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestInvestigationTemplateStartPersistsAndRechecksDependencies(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_inv_template_start_" + store.ID()
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
		if _, cleanupErr := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); cleanupErr != nil {
			t.Error(cleanupErr)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 12
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = aiextensions.New(db, "").Migrate(ctx); err != nil {
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
		`INSERT INTO rbac_roles(id,name) VALUES ('template-starter','Template Starter')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('template-starter','environment','view','environment','dev'),
 ('template-starter','canvas','view','application','template-app'),
 ('template-starter','canvas','change','application','template-app'),
 ('template-starter','connectors','view','application','template-app'),
 ('template-starter','knowledge','view','application','template-app'),
 ('template-starter','knowledge','view','team','template-team')`,
		`INSERT INTO teams(id,name) VALUES ('template-team','Template Team')`,
		`INSERT INTO applications(id,name,key,owner_team_id,archived) VALUES
 ('template-app','Template App','template-app','template-team',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('template-ae','template-app','dev','Development',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('template-app','dev')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatalf("fixture statement failed: %v\n%s", err, statement)
		}
	}
	user, err := st.CreateUser(ctx, core.User{Email: "starter@example.invalid", Name: "Starter", Role: "viewer"}, "isolated-starter-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES($1,'template-starter')`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO team_members(team_id,user_id) VALUES('template-team',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "starter@example.invalid", "isolated-starter-password")
	if err != nil {
		t.Fatal(err)
	}

	registration := aiextensions.Record{ID: "template-mcp", ApplicationID: "template-app", Kind: "mcp",
		Title: "Read-only diagnostics", Maintainer: "Platform", Enabled: true, Version: 1,
		MCP: &aiextensions.MCPConfig{Endpoint: "https://private.example.invalid/mcp",
			Bindings: []aiextensions.Binding{{Name: "read_metrics", Tool: "read_metrics", ReadOnlyConfirmed: true,
				Arguments: map[string]any{}}}},
	}
	recordJSON, err := json.Marshal(registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO ai_extensions(id,application_id,kind,version,record)
 VALUES('template-mcp','template-app','mcp',1,$1)`, recordJSON); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_environment_bindings
 (extension_id,application_id,environment_id,bound_by)
 VALUES('template-mcp','template-app','dev',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	var binding string
	if err = db.QueryRow(ctx, `SELECT binding_instance_id FROM v3_mcp_environment_bindings
 WHERE extension_id='template-mcp' AND environment_id='dev'`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	var persistedRecord []byte
	if err = db.QueryRow(ctx, `SELECT record FROM ai_extensions WHERE id='template-mcp'`).Scan(&persistedRecord); err != nil {
		t.Fatal(err)
	}
	digest := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	observedTool := aiextensions.ToolInfo{Name: "read_metrics", Description: "Read metrics",
		ReadOnlyHint: true, InputSchema: map[string]any{"type": "object"}}
	toolDeclaration, err := json.Marshal([]aiextensions.ToolInfo{observedTool})
	if err != nil {
		t.Fatal(err)
	}
	toolRaw, err := json.Marshal(observedTool)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := "template-snapshot"
	toolsHash, registrationHash := digest(toolDeclaration), digest(persistedRecord)
	toolHash := digest(toolRaw)
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_discovery_snapshots
 (id,extension_id,binding_instance_id,application_id,environment_id,registration_version,
  registration_hash,tools_hash,tools,tool_count,captured_by)
 VALUES($1,'template-mcp',$2,'template-app','dev',1,$3,$4,$5,1,$6)`,
		snapshotID, binding, registrationHash, toolsHash, toolDeclaration, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_tool_reviews
 (snapshot_id,extension_id,binding_instance_id,application_id,environment_id,
  registration_version,registration_hash,tools_hash,reviewed_by)
 VALUES($1,'template-mcp',$2,'template-app','dev',1,$3,$4,$5)`,
		snapshotID, binding, registrationHash, toolsHash, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_tool_review_decisions
 (snapshot_id,tool_name,tool_hash,decision,enabled,updated_by)
 VALUES($1,'read_metrics',$2,'allow_read_only',true,$3)`, snapshotID, toolHash, user.ID); err != nil {
		t.Fatal(err)
	}
	metadata := `{"visibility":"application","environment_ids":["dev"],"team_id":"template-team"}`
	if _, err = db.Exec(ctx, `INSERT INTO knowledge_documents
 (id,application_id,title,body,source,version,content_hash,created_by,lifecycle,metadata)
 VALUES('template-runbook','template-app','Read-only triage','Inspect metrics','runbook://v3/manual',1,'fixture-hash',$1,'published',$2::jsonb)`,
		user.ID, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_runbooks(document_id,source_kind,steps)
 VALUES('template-runbook','blank','[{"title":"Inspect metrics"}]'::jsonb)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_investigation_templates
 (id,owner,environment_id,application_id,name,description,version,maintainer,
  branches,tools,runbooks,triggers,budget)
 VALUES('template-triage','official','dev','template-app','Latency triage','Review latency safely',1,'Platform',
 '[{"name":"Inspect metrics","step":"Read p95 latency before proposing a recovery.","tools":["template-mcp:read_metrics"]}]',
 '["template-mcp:read_metrics"]','["template-runbook"]',
 '{"manual":true,"auto_triage":false,"matchers":[]}',
 '{"turns":2,"tools":3,"minutes":2}')`); err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{
		Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(method, env, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/investigation-templates", nil)
		if method == http.MethodPost {
			r = httptest.NewRequest(method, "/api/v1/investigation-templates/template-triage/start", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	startBody := func(requestID, version string) string {
		return fmt.Sprintf(`{"inputs":{"application_id":"template-app","template_version":%q,"request_id":%q}}`, version, requestID)
	}
	assertStatus := func(w *httptest.ResponseRecorder, status int, code string) {
		t.Helper()
		if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`)) {
			t.Fatalf("status=%d body=%s; want status=%d code=%q", w.Code, w.Body.String(), status, code)
		}
	}
	startID := func(w *httptest.ResponseRecorder, status int) string {
		t.Helper()
		assertStatus(w, status, "")
		var response struct {
			InvestigationID string `json:"investigation_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(response.InvestigationID, "INV-") {
			t.Fatalf("missing investigation ID: %s", w.Body.String())
		}
		return response.InvestigationID
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	listed := request(http.MethodGet, "dev", "")
	assertStatus(listed, 200, "")
	var catalog struct {
		Items []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &catalog); err != nil || len(catalog.Items) != 1 ||
		catalog.Items[0].ID != "template-triage" || catalog.Items[0].Version != "v1" {
		t.Fatalf("GET must expose the persisted fixture: body=%s err=%v", listed.Body.String(), err)
	}
	id := startID(request(http.MethodPost, "dev", startBody("request-0001", "v1")), 201)
	if got := startID(request(http.MethodPost, "dev", startBody("request-0001", "v1")), 200); got != id {
		t.Fatalf("replay created another investigation: first=%s replay=%s", id, got)
	}
	if n := count(`SELECT count(*) FROM investigations WHERE id=$1 AND application_id='template-app' AND environment_id='template-ae'`, id); n != 1 {
		t.Fatalf("investigation not durably scoped: %d", n)
	}
	if n := count(`SELECT count(*) FROM branches WHERE investigation_id=$1`, id); n != 2 {
		t.Fatalf("expected root and one template child, got %d", n)
	}
	var snapshot, provenance []byte
	if err := db.QueryRow(ctx, `SELECT snapshot FROM v3_investigation_template_starts
 WHERE investigation_id=$1 AND actor_id=$2 AND request_id='request-0001'`, id, user.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT cr.provenance FROM branches b
 JOIN context_revisions cr ON cr.branch_id=b.id AND cr.revision=0
 WHERE b.investigation_id=$1 AND b.parent_id IS NOT NULL`, id).Scan(&provenance); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot), `"template_version": "v1"`) ||
		!strings.Contains(string(snapshot), `"execution": "not_started"`) ||
		!strings.Contains(string(snapshot), `"turns": 2`) ||
		!strings.Contains(string(provenance), `"step": "Read p95 latency before proposing a recovery."`) ||
		!strings.Contains(string(provenance), `"runbooks": ["template-runbook"]`) {
		t.Fatalf("template provenance or budget missing: snapshot=%s provenance=%s", snapshot, provenance)
	}
	if n := count(`SELECT count(*) FROM management_audit WHERE actor_id=$1 AND target=$2
 AND action='v3.investigation_template.started' AND outcome='success'`, user.ID, id); n != 1 {
		t.Fatalf("start audit mismatch: %d", n)
	}

	// Two overlapping retries must resolve to one durable start and one audit.
	before := count(`SELECT count(*) FROM investigations`)
	concurrent := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range concurrent {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			concurrent[index] = request(http.MethodPost, "dev", startBody("request-0002", "v1"))
		}(i)
	}
	wg.Wait()
	ids := map[string]bool{}
	status := map[int]int{}
	for _, w := range concurrent {
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("concurrent start: status=%d body=%s", w.Code, w.Body.String())
		}
		status[w.Code]++
		ids[startID(w, w.Code)] = true
	}
	if len(ids) != 1 || status[200] != 1 || status[201] != 1 ||
		count(`SELECT count(*) FROM investigations`) != before+1 ||
		count(`SELECT count(*) FROM v3_investigation_template_starts WHERE request_id='request-0002'`) != 1 ||
		count(`SELECT count(*) FROM management_audit WHERE action='v3.investigation_template.started'`) != 2 {
		t.Fatalf("concurrent idempotency failed: ids=%v status=%v", ids, status)
	}

	currentVersion := "v1"
	assertRejected := func(name, requestID string, status int, code string) {
		t.Helper()
		baseline := count(`SELECT count(*) FROM investigations`)
		w := request(http.MethodPost, "dev", startBody(requestID, currentVersion))
		assertStatus(w, status, code)
		if got := count(`SELECT count(*) FROM investigations`); got != baseline {
			t.Fatalf("%s created an investigation: before=%d after=%d", name, baseline, got)
		}
	}
	before = count(`SELECT count(*) FROM investigations`)
	assertStatus(request(http.MethodPost, "dev", startBody("request-stale", "v2")), 409, "template_version_changed")
	if count(`SELECT count(*) FROM investigations`) != before {
		t.Fatal("stale template version created an investigation")
	}
	if _, err = db.Exec(ctx, `UPDATE v3_investigation_templates SET active=false,version=version+1 WHERE id='template-triage'`); err != nil {
		t.Fatal(err)
	}
	currentVersion = "v2"
	assertRejected("disabled template", "request-disabled", 404, "not_found")
	if got := startID(request(http.MethodPost, "dev", startBody("request-0001", "v1")), 200); got != id {
		t.Fatalf("retry after template disable lost its committed investigation: got=%s want=%s", got, id)
	}
	if _, err = db.Exec(ctx, `UPDATE v3_investigation_templates SET active=true,version=version+1 WHERE id='template-triage'`); err != nil {
		t.Fatal(err)
	}
	currentVersion = "v3"
	for _, revoked := range []struct{ module, requestID string }{
		{"connectors", "request-no-connectors"},
		{"knowledge", "request-no-knowledge"},
		{"canvas", "request-no-canvas"},
	} {
		action := "view"
		if revoked.module == "canvas" {
			action = "change"
		}
		if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='template-starter'
 AND module=$1 AND action=$2`, revoked.module, action); err != nil {
			t.Fatal(err)
		}
		if revoked.module == "knowledge" {
			if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='template-starter'
 AND module='knowledge' AND action='view' AND scope_kind='team'`); err != nil {
				t.Fatal(err)
			}
		}
		if revoked.module != "canvas" {
			assertRejected("revoked "+revoked.module, revoked.requestID, 403, "template_reference_forbidden")
			if got := startID(request(http.MethodPost, "dev", startBody("request-0001", "v1")), 200); got != id {
				t.Fatalf("retry after %s reference revoke lost original investigation: %s", revoked.module, got)
			}
			listed := request(http.MethodGet, "dev", "")
			if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"items":[]`) {
				t.Fatalf("revoked %s leaked template references: %d %s", revoked.module, listed.Code, listed.Body.String())
			}
		} else {
			assertRejected("revoked canvas", revoked.requestID, 403, "forbidden")
		}
		if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('template-starter',$1,$2,'application','template-app')`, revoked.module, action); err != nil {
			t.Fatal(err)
		}
		if revoked.module == "knowledge" {
			if _, err = db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id)
 VALUES('template-starter','knowledge','view','team','template-team')`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = db.Exec(ctx, `UPDATE v3_mcp_tool_review_decisions SET enabled=false,revision=revision+1,updated_by=$1
 WHERE snapshot_id=$2 AND tool_name='read_metrics'`, user.ID, snapshotID); err != nil {
		t.Fatal(err)
	}
	assertRejected("disabled reviewed tool", "request-tool-off", 409, "template_tool_unavailable")
	if _, err = db.Exec(ctx, `UPDATE v3_mcp_tool_review_decisions SET enabled=true,revision=revision+1,updated_by=$1
 WHERE snapshot_id=$2 AND tool_name='read_metrics'`, user.ID, snapshotID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='draft' WHERE id='template-runbook'`); err != nil {
		t.Fatal(err)
	}
	assertRejected("draft Runbook", "request-draft-runbook", 409, "template_runbook_unavailable")
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET lifecycle='published' WHERE id='template-runbook'`); err != nil {
		t.Fatal(err)
	}
	for _, malformed := range []struct {
		name, metadata, requestID string
	}{
		{"empty environment", `{"visibility":"application","environment_ids":["dev",""],"team_id":"template-team"}`, "request-bad-env"},
		{"null team", `{"visibility":"application","environment_ids":["dev"],"team_id":null}`, "request-bad-team"},
	} {
		if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET metadata=$2::jsonb WHERE id=$1`, "template-runbook", malformed.metadata); err != nil {
			t.Fatal(err)
		}
		assertRejected(malformed.name, malformed.requestID, 409, "template_runbook_unavailable")
	}
	if _, err = db.Exec(ctx, `UPDATE knowledge_documents SET metadata=$2::jsonb WHERE id=$1`, "template-runbook", metadata); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_mcp_discovery_snapshots
 (id,extension_id,binding_instance_id,application_id,environment_id,registration_version,
  registration_hash,tools_hash,tools,tool_count,captured_by)
 VALUES('template-new-snapshot','template-mcp',$1,'template-app','dev',1,$2,$3,$4,1,$5)`,
		binding, registrationHash, toolsHash, toolDeclaration, user.ID); err != nil {
		t.Fatal(err)
	}
	assertRejected("new unreviewed discovery", "request-new-discovery", 409, "template_tool_unavailable")
	if listed := request(http.MethodGet, "dev", ""); listed.Code != 200 || !strings.Contains(listed.Body.String(), `"items":[]`) {
		t.Fatalf("unreviewed discovery remained visible: %d %s", listed.Code, listed.Body.String())
	}
	baseline := count(`SELECT count(*) FROM investigations`)
	assertStatus(request(http.MethodPost, "all", startBody("request-all", currentVersion)), 409, "env_view_only")
	if count(`SELECT count(*) FROM investigations`) != baseline {
		t.Fatal("all-environment write created an investigation")
	}
	var uses int
	if err = db.QueryRow(ctx, `SELECT uses FROM v3_investigation_templates WHERE id='template-triage'`).Scan(&uses); err != nil || uses != 2 {
		t.Fatalf("template use count must equal committed starts: uses=%d err=%v", uses, err)
	}
}
