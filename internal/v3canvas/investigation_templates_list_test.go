package v3canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wans/aiops-canvas/internal/core"
	"github.com/wans/aiops-canvas/internal/httpapi"
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestInvestigationTemplateListUsesPersistedVisibleCatalog(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_inv_templates_list_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var applied int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
 WHERE name IN ('134_v3_investigation_templates.sql','134_v3_pull_attempts.sql')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("expected both distinct 134 migrations, got %d", applied)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('template-reader','Template Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('template-reader','environment','view','environment','test'),
 ('template-reader','canvas','view','application','template-app')`,
		`INSERT INTO teams(id,name) VALUES ('template-team','Template Team')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('template-app','Template App','template-app','template-team')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('template-ae','template-app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('template-app','test')`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	user, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'template-reader')`, user.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	api.Register("GET /api/v1/investigation-templates", "viewer", New(st).listInvestigationTemplates)
	api.MarkV3ObjectRoute("GET /api/v1/investigation-templates", "canvas", "view")
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "test", EnvironmentIDs: []string{"test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "test")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	decode := func(path string) struct {
		Items  []investigationTemplateItem `json:"items"`
		Counts map[string]int              `json:"counts"`
	} {
		t.Helper()
		w := get(path)
		if w.Code != 200 {
			t.Fatalf("GET %s: status=%d body=%s", path, w.Code, w.Body.String())
		}
		var out struct {
			Items  []investigationTemplateItem `json:"items"`
			Counts map[string]int              `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := decode("/api/v1/investigation-templates"); len(got.Items) != 0 || got.Counts["all"] != 0 {
		t.Fatalf("empty catalog must remain empty: %+v", got)
	}
	if _, err = db.Exec(ctx, `INSERT INTO v3_investigation_templates
 (id,owner,environment_id,application_id,team_id,name,description,version,maintainer,branches,tools,runbooks,triggers,budget)
 VALUES
 ('official-check','official','test','template-app',NULL,'Latency check','Read-only checks',1,'SRE',
  '[{"name":"Inspect","step":"Check recent latency","tools":[]}]','[]','[]',
  '{"manual":true,"auto_triage":false,"matchers":[]}','{"turns":2,"tools":3,"minutes":2}'),
 ('team-check','team','test','template-app','template-team','Team latency','Private checks',2,'Team',
  '[{"name":"Inspect","step":"Check team dashboard","tools":[]}]','[]','[]',
  '{"manual":true,"auto_triage":false,"matchers":[]}','{"turns":1,"tools":0,"minutes":1}')`); err != nil {
		t.Fatal(err)
	}
	first := decode("/api/v1/investigation-templates")
	if len(first.Items) != 1 || first.Items[0].ID != "official-check" || first.Items[0].Version != "v1" ||
		first.Counts["all"] != 1 || first.Counts["team"] != 0 {
		t.Fatalf("team template leaked before membership: %+v", first)
	}
	if _, err = db.Exec(ctx, `INSERT INTO team_members(team_id,user_id) VALUES ('template-team',$1)`, user.ID); err != nil {
		t.Fatal(err)
	}
	filtered := decode("/api/v1/investigation-templates?owner=team&q=Team")
	if len(filtered.Items) != 1 || filtered.Items[0].ID != "team-check" ||
		filtered.Counts["all"] != 2 || filtered.Counts["official"] != 1 || filtered.Counts["team"] != 1 {
		t.Fatalf("visible counts or filters wrong: %+v", filtered)
	}
	if _, err = db.Exec(ctx, `DELETE FROM team_members WHERE team_id='template-team' AND user_id=$1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if got := decode("/api/v1/investigation-templates?owner=team"); len(got.Items) != 0 || got.Counts["team"] != 0 {
		t.Fatalf("revoked membership still sees team template: %+v", got)
	}
	for _, statement := range []string{
		`INSERT INTO applications(id,name,key) VALUES ('hidden-app','Hidden App','hidden-app')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES ('hidden-ae','hidden-app','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('hidden-app','test')`,
		`INSERT INTO v3_investigation_templates
 (id,owner,environment_id,application_id,name,version,branches,tools,runbooks,triggers,budget)
 SELECT 'hidden-'||n,'official','test','hidden-app','Hidden '||n,1,
 '[{"name":"Inspect","step":"Read safely","tools":[]}]'::jsonb,'[]'::jsonb,'[]'::jsonb,
 '{"manual":true,"auto_triage":false,"matchers":[]}'::jsonb,
 '{"turns":1,"tools":0,"minutes":1}'::jsonb FROM generate_series(1,501) n`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if got := decode("/api/v1/investigation-templates"); len(got.Items) != 1 || got.Items[0].ID != "official-check" {
		t.Fatalf("unrelated templates exhausted scoped catalog: %+v", got)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_investigation_templates WHERE id='official-check'`); err == nil {
		t.Fatal("configured template ID was deleted and could be reused at the same version")
	}
	if _, err = db.Exec(ctx, `UPDATE v3_investigation_templates SET name='Changed silently' WHERE id='official-check'`); err == nil {
		t.Fatal("template definition changed without version increment")
	}
	if _, err = db.Exec(ctx, `UPDATE v3_investigation_templates SET version=version+1,
 triggers='{"manual":true,"auto_triage":true,"matchers":[]}' WHERE id='official-check'`); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/v1/investigation-templates"); w.Code != 503 {
		t.Fatalf("unsupported auto-triage template must fail closed: status=%d body=%s", w.Code, w.Body.String())
	}
}
