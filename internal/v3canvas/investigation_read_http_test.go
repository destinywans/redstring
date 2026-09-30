package v3canvas

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
	"github.com/wans/aiops-canvas/internal/rbac"
	"github.com/wans/aiops-canvas/internal/store"
)

func TestInvestigationNodeReadUsesPersistedScopeAndRevision(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops an isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_inv_read_" + store.ID()
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
		`INSERT INTO rbac_roles(id,name) VALUES ('inv-app-reader','App Reader'),('inv-team-reader','Team Reader'),('inv-no-grant','No Grant')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('inv-app-reader','environment','view','environment','dev'),('inv-app-reader','canvas','view','application','app-one'),
 ('inv-team-reader','environment','view','environment','dev'),('inv-team-reader','canvas','view','team','team-one'),
 ('inv-no-grant','environment','view','environment','dev')`,
		`INSERT INTO teams(id,name) VALUES ('team-one','One'),('team-two','Two')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-one','One','app-one','team-one'),('app-two','Two','app-two','team-two')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-one','app-one','dev','Development'),('ae-two','app-two','dev','Development')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES ('app-one','dev'),('app-two','dev')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
 ('11111111111111111111111111111111','app-one','One','open','ae-one'),
 ('22222222222222222222222222222222','app-two','Two','open','ae-two')`,
		`INSERT INTO branches(id,investigation_id,title) VALUES
 ('root-one','11111111111111111111111111111111','Main'),
 ('root-two','22222222222222222222222222222222','Main')`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('root-one',0,'[{"id":"m0","role":"user","content":"old branch state"}]','{"kind":"initial"}'),
 ('root-two',0,'[{"id":"other","role":"user","content":"other application secret"}]','{"kind":"initial"}')`,
		`UPDATE branches SET revision=1 WHERE id='root-one'`,
		`INSERT INTO context_revisions(branch_id,revision,messages,provenance) VALUES
 ('root-one',1,'[{"id":"m1","role":"assistant","content":"persisted current context","evidence_ids":["e1"],"model_id":"legacy"}]','{"kind":"legacy_run"}')`,
	} {
		if _, err = db.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	createUser := func(email, role string) string {
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
		return token
	}
	appToken := createUser("app-reader@example.invalid", "inv-app-reader")
	teamToken := createUser("team-reader@example.invalid", "inv-team-reader")
	deniedToken := createUser("denied@example.invalid", "inv-no-grant")
	api := httpapi.New(st, nil, true)
	New(st).Register(api)
	policy := &rbac.V3Policy{Store: st}
	if err = api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "staging"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(token, env, inv, node string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/investigations/"+inv+"/nodes/"+node+"/messages", nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	const one = "11111111111111111111111111111111"
	const two = "22222222222222222222222222222222"
	for _, token := range []string{appToken, teamToken} {
		w := request(token, "dev", one, "root-one")
		if w.Code != 200 {
			t.Fatalf("authorized read=%d %s", w.Code, w.Body.String())
		}
		var result struct {
			BranchID string         `json:"branch_id"`
			Revision int64          `json:"revision"`
			Messages []core.Message `json:"messages"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.BranchID != "root-one" || result.Revision != 1 || len(result.Messages) != 1 ||
			result.Messages[0].Content != "persisted current context" || result.Messages[0].ModelID != "legacy" ||
			len(result.Messages[0].EvidenceIDs) != 1 || result.Messages[0].EvidenceIDs[0] != "e1" ||
			strings.Contains(w.Body.String(), "old branch state") || strings.Contains(w.Body.String(), "created_at") {
			t.Fatalf("read diverged from current revision: %+v", result)
		}
	}
	for _, tc := range []struct {
		name, token, env, inv, node string
		want                        int
	}{
		{"no grant", deniedToken, "dev", one, "root-one", 404},
		{"other application", appToken, "dev", two, "root-two", 404},
		{"team cannot cross owner", teamToken, "dev", two, "root-two", 404},
		{"wrong investigation", appToken, "dev", two, "root-one", 404},
		{"unknown branch", appToken, "dev", one, "unknown-branch", 404},
		{"wrong environment", appToken, "staging", one, "root-one", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := request(tc.token, tc.env, tc.inv, tc.node)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-two' WHERE id='app-one'`); err != nil {
		t.Fatal(err)
	}
	if w := request(teamToken, "dev", one, "root-one"); w.Code != 404 {
		t.Fatalf("stale team access: %d %s", w.Code, w.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE context_revisions SET messages=jsonb_build_array(jsonb_build_object('id','huge','role','user','content',repeat('x',30000))) WHERE branch_id='root-one' AND revision=1`); err != nil {
		t.Fatal(err)
	}
	if w := request(appToken, "dev", one, "root-one"); w.Code != 503 {
		t.Fatalf("oversize context leaked: %d %s", w.Code, w.Body.String())
	}
}
