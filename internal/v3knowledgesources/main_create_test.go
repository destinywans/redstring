package v3knowledgesources

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/knowledge"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestValidateMainCreateLimitsSupportedFamily(t *testing.T) {
	valid := mainCreateRequest{Name: "Guide", Type: "web", URL: "https://example.com/guide.md",
		Schedule: "manual", Chunk: "heading", InheritACL: ptrTrue(), ApplicationID: "app"}
	if status, _, message := validateMainCreate(valid); status != 0 {
		t.Fatalf("valid public Web source rejected: %d %s", status, message)
	}
	for _, kind := range []string{"git"} {
		v := valid
		v.Type = kind
		if status, code, _ := validateMainCreate(v); status != http.StatusNotImplemented || code != "source_adapter_unavailable" {
			t.Errorf("%s returned %d %s", kind, status, code)
		}
	}
	for _, kind := range []string{"feishu", "upload"} {
		v := valid
		v.Type = kind
		if status, _, _ := validateMainCreate(v); status != http.StatusUnprocessableEntity {
			t.Errorf("%s accepted a public Web URL: %d", kind, status)
		}
	}
	private := valid
	private.SecretRef = "vault:kv/secret"
	if status, code, _ := validateMainCreate(private); status != http.StatusNotImplemented || code != "source_adapter_unavailable" {
		t.Errorf("private Web returned %d %s", status, code)
	}
	for _, mutate := range []func(*mainCreateRequest){
		func(v *mainCreateRequest) { v.ApplicationID = "" },
		func(v *mainCreateRequest) { v.URL = "http://example.com/guide.md" },
		func(v *mainCreateRequest) { v.Spaces = []string{"SRE"} },
		func(v *mainCreateRequest) { v.Schedule = "6h" },
		func(v *mainCreateRequest) { v.InheritACL = nil },
	} {
		v := valid
		mutate(&v)
		if status, _, _ := validateMainCreate(v); status != http.StatusUnprocessableEntity {
			t.Errorf("invalid source returned %d: %+v", status, v)
		}
	}
}

func TestValidateGitMarkdownMainCreateRequiresResolvableCredentialAndManualScope(t *testing.T) {
	valid := mainCreateRequest{Name: "Runbooks", Type: "git_markdown",
		URL:       "https://gitlab.example.invalid/platform/runbooks",
		SecretRef: "vault://ops/gitlab#token", ApplicationID: "app",
		Schedule: "manual", Chunk: "heading", InheritACL: ptrTrue()}
	if status, _, detail := validateMainCreate(valid); status != 0 {
		t.Fatalf("valid Git Markdown source rejected: %d %s", status, detail)
	}
	for _, mutate := range []func(*mainCreateRequest){
		func(v *mainCreateRequest) { v.SecretRef = "vault:ops/gitlab" },
		func(v *mainCreateRequest) { v.SecretRef = "" },
		func(v *mainCreateRequest) { v.Schedule = "6h" },
		func(v *mainCreateRequest) { v.Spaces = []string{"docs"} },
		func(v *mainCreateRequest) { v.ApplicationID = "" },
	} {
		v := valid
		mutate(&v)
		if status, _, _ := validateMainCreate(v); status != http.StatusUnprocessableEntity {
			t.Errorf("invalid Git Markdown source returned %d: %+v", status, v)
		}
	}
}

func ptrTrue() *bool { yes := true; return &yes }

func TestMainCreatePublicWebTransactionHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminDB.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public"); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	schema := "v3_main_source_" + store.ID()
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
	cfg.MaxConns = 1
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&knowledge.PGRepository{DB: db}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	viewer, err := st.CreateUser(ctx, core.User{Email: "source-viewer@example.invalid", Name: "Viewer", Role: "viewer"}, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, viewer.Email, "isolated-viewer-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('viewer','environment','view','environment','dev'),
 ('viewer','knowledge','manage','application','app')`,
		`INSERT INTO applications(id,key,name,environment,archived) VALUES
 ('app','app','App','dev',false),('hidden','hidden','Hidden','dev',false)`,
		`INSERT INTO application_environments(id,application_id,key,name,archived) VALUES
 ('app-dev','app','dev','Dev',false),('hidden-dev','hidden','dev','Dev',false)`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app','dev'),('hidden','dev')`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	s := New(db, policy)
	api.Register("POST /api/v1/knowledge-sources", "viewer", s.createMain)
	api.MarkV3ObjectRoute("POST /api/v1/knowledge-sources", "knowledge", "manage")
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, user core.User, module, action, environment string) error {
			return policy.Authorize(ctx, user, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	post := func(body, environment string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge-sources", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	valid := `{"name":"Guide","type":"web","url":"https://example.com/guide.md","secret_ref":"","spaces":[],"schedule":"manual","chunk":"heading","inherit_acl":true,"application_id":"app"}`
	unconfigured := `{"name":"Notes","type":"notion","url":"https://www.notion.so/2499c512bbf14e4691756ae89f64ec7c","secret_ref":"vault://ops/notion#token","spaces":[],"schedule":"manual","chunk":"heading","inherit_acl":true,"application_id":"app"}`
	if w := post(unconfigured, "dev"); w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "source_adapter_unavailable") {
		t.Fatalf("unconfigured adapter create: %d %s", w.Code, w.Body.String())
	}
	if w := post(valid, "all"); w.Code != http.StatusConflict {
		t.Fatalf("all-environment create: %d %s", w.Code, w.Body.String())
	}
	unknownApp := post(strings.Replace(valid, `"application_id":"app"`, `"application_id":"missing"`, 1), "dev")
	ungrantedApp := post(strings.Replace(valid, `"application_id":"app"`, `"application_id":"hidden"`, 1), "dev")
	if unknownApp.Code != http.StatusForbidden || ungrantedApp.Code != http.StatusForbidden || unknownApp.Body.String() != ungrantedApp.Body.String() {
		t.Fatalf("app enumeration: unknown=%d %s, ungranted=%d %s",
			unknownApp.Code, unknownApp.Body.String(), ungrantedApp.Code, ungrantedApp.Body.String())
	}
	if w := post(strings.Replace(valid, `"application_id":"app"`, `"application_id":""`, 1), "dev"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing app: %d %s", w.Code, w.Body.String())
	}
	w := post(valid, "dev")
	if w.Code != http.StatusCreated {
		t.Fatalf("public Web create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID                 string `json:"id"`
		RegistrationStatus string `json:"registration_status"`
		SyncStarted        bool   `json:"sync_started"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" || created.RegistrationStatus != "unverified" || created.SyncStarted {
		t.Fatalf("create response: %+v, %v", created, err)
	}
	var registrations, audits int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM v3_knowledge_source_registrations WHERE id=$1 AND application_id='app' AND environment_id='dev' AND secret_ref=''`, created.ID).Scan(&registrations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM management_audit WHERE target=$1 AND action='v3.knowledge_source.registered'`, created.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if registrations != 1 || audits != 1 {
		t.Fatalf("registration/audit counts: %d/%d", registrations, audits)
	}
	for _, query := range []string{
		`INSERT INTO teams(id,name) VALUES ('t1','Owner team'),('t2','Other team')`,
		`UPDATE applications SET owner_team_id='t1' WHERE id='app'`,
	} {
		if _, err := db.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	withTeam := func(team string) string {
		return strings.Replace(valid, `"application_id":"app"`, `"application_id":"app","team_id":"`+team+`"`, 1)
	}
	wrongTeam := post(withTeam("t2"), "dev")
	ungrantedTeam := post(withTeam("t1"), "dev")
	if wrongTeam.Code != http.StatusForbidden || ungrantedTeam.Code != http.StatusForbidden || wrongTeam.Body.String() != ungrantedTeam.Body.String() {
		t.Fatalf("team enumeration: mismatch=%d %s, ungranted=%d %s",
			wrongTeam.Code, wrongTeam.Body.String(), ungrantedTeam.Code, ungrantedTeam.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','knowledge','manage','team','t2')`); err != nil {
		t.Fatal(err)
	}
	if w := post(withTeam("t2"), "dev"); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("authorized mismatched owner team: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','knowledge','manage','team','t1')`); err != nil {
		t.Fatal(err)
	}
	w = post(withTeam("t1"), "dev")
	if w.Code != http.StatusCreated {
		t.Fatalf("team-authorized create: %d %s", w.Code, w.Body.String())
	}
	var teamCreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &teamCreated); err != nil || teamCreated.ID == "" {
		t.Fatalf("team create response: %+v, %v", teamCreated, err)
	}
	var storedTeam string
	if err := db.QueryRow(ctx, `SELECT COALESCE(team_id,'') FROM v3_knowledge_source_registrations WHERE id=$1`, teamCreated.ID).Scan(&storedTeam); err != nil || storedTeam != "t1" {
		t.Fatalf("stored team: %q, %v", storedTeam, err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='viewer' AND module='knowledge' AND action='manage'`); err != nil {
		t.Fatal(err)
	}
	if w := post(valid, "dev"); w.Code != http.StatusForbidden {
		t.Fatalf("revoked grant create: %d %s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES ('viewer','knowledge','manage','application','app')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE management_audit ADD CONSTRAINT deny_new_source_audit CHECK (false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if w := post(valid, "dev"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit failure create: %d %s", w.Code, w.Body.String())
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM v3_knowledge_source_registrations`).Scan(&registrations); err != nil {
		t.Fatal(err)
	}
	if registrations != 2 {
		t.Fatalf("audit failure left %d registrations", registrations)
	}
}
