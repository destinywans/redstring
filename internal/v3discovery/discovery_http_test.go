package v3discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/knowledge"
	"github.com/destinywans/redstring/internal/operations"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDiscoveryReferencesRegisteredHTTP(t *testing.T) {
	const visibleID = "11111111111111111111111111111111"
	const hiddenID = "22222222222222222222222222222222"
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops a random isolated schema")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_discovery_" + store.ID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := operations.New(db, st, nil, nil, operations.Config{}).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Mixed-kind discovery now reads persisted Runbook references as well as
	// legacy investigations; establish the same knowledge schema as startup.
	if err := knowledge.New(db, nil).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('discovery-app','Discovery App'),('discovery-team','Discovery Team'),('discovery-test','Discovery Test')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('discovery-app','environment','view','environment','dev'),
 ('discovery-app','canvas','view','application','app-visible'),
 ('discovery-app','services','view','environment','dev'),
 ('discovery-app','members','view','environment','dev'),
 ('discovery-team','environment','view','environment','dev'),
	('discovery-team','canvas','view','team','team-visible'),
 ('discovery-test','environment','view','environment','test')`,
		`INSERT INTO teams(id,name) VALUES ('team-visible','Visible Team'),('team-hidden','Hidden Team')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-visible','Visible','app-visible','team-visible'),
 ('app-hidden','Hidden','app-hidden','team-hidden'),
 ('app-unmapped','Unmapped','app-unmapped','team-visible')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-visible','app-visible','dev','Development'),
 ('ae-hidden','app-hidden','dev','Development'),
 ('ae-unmapped','app-unmapped','dev','Development'),
 ('ae-other-env','app-visible','test','Test')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-visible','dev'),('app-hidden','dev')`,
		`INSERT INTO applications(id,name,key) SELECT 'catalog-'||n,'Catalog '||n,'catalog-'||n FROM generate_series(1,35) n`,
		`INSERT INTO application_environments(id,application_id,key,name)
 SELECT 'catalog-'||n||'-dev','catalog-'||n,'dev','Development' FROM generate_series(1,35) n`,
		`INSERT INTO v3_application_environments(application_id,environment_id)
 SELECT 'catalog-'||n,'dev' FROM generate_series(1,35) n`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
 ('11111111111111111111111111111111','app-visible','发布后故障调查','open','ae-visible'),
 ('22222222222222222222222222222222','app-hidden','发布后隐藏调查','open','ae-hidden'),
 ('33333333333333333333333333333333','app-unmapped','发布后未映射调查','open','ae-unmapped'),
 ('44444444444444444444444444444444','app-visible','发布后其他环境调查','open','ae-other-env')`,
		`INSERT INTO users(id,email,name,role,password_hash,disabled,v3_identity_kind) VALUES
 ('person-visible','visible@example.invalid','张明','viewer','',false,'human'),
 ('person-hidden','hidden@example.invalid','张明隐藏','viewer','',false,'human'),
 ('person-disabled','disabled@example.invalid','张明停用','viewer','',true,'human'),
 ('person-machine','machine@example.invalid','张明机器','viewer','',false,'machine'),
 ('person-unassigned','unassigned@example.invalid','未分配成员','viewer','',false,'human')`,
		`INSERT INTO rbac_user_roles(user_id,role_id) VALUES
 ('person-visible','discovery-team'),('person-hidden','discovery-test'),
 ('person-disabled','discovery-team'),('person-machine','discovery-team')`,
	} {
		if _, err := db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	appUser, err := st.CreateUser(ctx, core.User{Email: "app@example.invalid", Name: "App Reader", Role: "viewer"}, "isolated-app-password")
	if err != nil {
		t.Fatal(err)
	}
	teamUser, err := st.CreateUser(ctx, core.User{Email: "team@example.invalid", Name: "Team Reader", Role: "viewer"}, "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'discovery-app'),($2,'discovery-team')`, appUser.ID, teamUser.ID); err != nil {
		t.Fatal(err)
	}
	_, appToken, err := st.Login(ctx, "app@example.invalid", "isolated-app-password")
	if err != nil {
		t.Fatal(err)
	}
	_, teamToken, err := st.Login(ctx, "team@example.invalid", "isolated-team-password")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := st.Login(ctx, "admin@example.invalid", "isolated-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	policy := &rbac.V3Policy{Store: st}
	api := httpapi.New(st, nil, true)
	New(db, policy).Register(api)
	if err := api.ConfigureV3Environment(httpapi.V3InstanceConfig{Tier: "nonprod", DefaultEnv: "dev", EnvironmentIDs: []string{"dev", "test"}},
		policy, st.Session, func(ctx context.Context, u core.User, module, action, environment string) error {
			return policy.Authorize(ctx, u, rbac.V3Decision{Module: module, Action: action, EnvironmentID: environment})
		}); err != nil {
		t.Fatal(err)
	}
	request := func(token, path, env string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", env)
		r.Header.Set("Accept-Language", "zh-CN")
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	checkItems := func(w *httptest.ResponseRecorder, key string) []record {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		var items []record
		if err := json.Unmarshal(payload[key], &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	mentions := request(appToken, "/api/v1/mentions?kind=investigation&q=fabu", "dev")
	if mentions.Code != 200 || !strings.Contains(mentions.Body.String(), `"id":"`+visibleID+`"`) ||
		strings.Contains(mentions.Body.String(), hiddenID) || strings.Contains(mentions.Body.String(), "33333333") ||
		strings.Contains(mentions.Body.String(), "44444444") ||
		!strings.Contains(mentions.Body.String(), `"meta":"调查 · dev"`) {
		t.Fatalf("pinyin scoped mentions: status=%d body=%s", mentions.Code, mentions.Body.String())
	}
	search := request(appToken, "/api/v1/search?q=发布", "dev")
	if search.Code != 200 || !strings.Contains(search.Body.String(), `"href":"/canvases"`) ||
		strings.Contains(search.Body.String(), hiddenID) || strings.Contains(search.Body.String(), "33333333") ||
		strings.Contains(search.Body.String(), "44444444") || !strings.Contains(search.Body.String(), `"recent":[]`) {
		t.Fatalf("scoped search with truthful recent: status=%d body=%s", search.Code, search.Body.String())
	}
	people := request(appToken, "/api/v1/mentions?kind=person&q=zhangming", "dev")
	if people.Code != 200 || !strings.Contains(people.Body.String(), `"id":"person-visible"`) ||
		strings.Contains(people.Body.String(), "person-hidden") || strings.Contains(people.Body.String(), "person-disabled") ||
		strings.Contains(people.Body.String(), "person-machine") {
		t.Fatalf("pinyin scoped people: status=%d body=%s", people.Code, people.Body.String())
	}
	allMentions := request(appToken, "/api/v1/mentions", "dev")
	if allMentions.Code != 200 || !strings.Contains(allMentions.Body.String(), `"kind":"investigation"`) ||
		!strings.Contains(allMentions.Body.String(), `"kind":"person"`) ||
		strings.Contains(allMentions.Body.String(), hiddenID) || strings.Contains(allMentions.Body.String(), "person-hidden") {
		t.Fatalf("bounded all-kind mentions lost less common authorized kinds: status=%d body=%s", allMentions.Code, allMentions.Body.String())
	}
	allItems := checkItems(allMentions, "items")
	if len(allItems) != 30 {
		t.Fatalf("unbounded mention candidates: %d", len(allItems))
	}
	seenKinds := map[string]bool{}
	lastKind := ""
	for _, item := range allItems {
		if item.Kind != lastKind && seenKinds[item.Kind] {
			t.Fatalf("mention kind %s is not grouped: %+v", item.Kind, allItems)
		}
		seenKinds[item.Kind] = true
		lastKind = item.Kind
	}
	if got := checkItems(request(appToken, "/api/v1/mentions?kind=person&q=未分配", "dev"), "items"); len(got) != 0 {
		t.Fatalf("scoped reader saw unassigned person: %+v", got)
	}
	globalPeople := request(adminToken, "/api/v1/mentions?kind=person&q=未分配", "dev")
	if globalPeople.Code != 200 || !strings.Contains(globalPeople.Body.String(), `"id":"person-unassigned"`) ||
		!strings.Contains(globalPeople.Body.String(), `"meta":"人员"`) ||
		strings.Contains(globalPeople.Body.String(), `"meta":"人员 · dev"`) {
		t.Fatalf("global reader unassigned person label: status=%d body=%s", globalPeople.Code, globalPeople.Body.String())
	}
	personSearch := request(appToken, "/api/v1/search?q=zhangming", "dev")
	if personSearch.Code != 200 || !strings.Contains(personSearch.Body.String(), `"kind":"person"`) ||
		!strings.Contains(personSearch.Body.String(), `"href":"/settings/members"`) ||
		strings.Contains(personSearch.Body.String(), "person-hidden") {
		t.Fatalf("scoped person search: status=%d body=%s", personSearch.Code, personSearch.Body.String())
	}
	personPreview := request(appToken, "/api/v1/mentions/person/person-visible/preview", "dev")
	if personPreview.Code != 200 || !strings.Contains(personPreview.Body.String(), `"title":"张明"`) ||
		!strings.Contains(personPreview.Body.String(), `"subtitle":"人员"`) ||
		strings.Contains(personPreview.Body.String(), `"status"`) {
		t.Fatalf("persisted person preview: status=%d body=%s", personPreview.Code, personPreview.Body.String())
	}
	globalPreview := request(adminToken, "/api/v1/mentions/person/person-unassigned/preview", "dev")
	if globalPreview.Code != 200 || !strings.Contains(globalPreview.Body.String(), `"subtitle":"人员"`) {
		t.Fatalf("global reader unassigned person preview: status=%d body=%s", globalPreview.Code, globalPreview.Body.String())
	}
	if w := request(appToken, "/api/v1/mentions/person/person-hidden/preview", "dev"); w.Code != 404 {
		t.Fatalf("cross-environment person preview: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := checkItems(request(teamToken, "/api/v1/mentions?kind=investigation&q=fabu", "dev"), "items"); len(got) != 1 {
		t.Fatalf("team grant mentions: %+v", got)
	}
	preview := request(teamToken, "/api/v1/mentions/investigation/"+visibleID+"/preview", "dev")
	if preview.Code != 200 {
		t.Fatalf("authorized preview: status=%d body=%s", preview.Code, preview.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(preview.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["id"] != visibleID || detail["kind"] != "investigation" || detail["title"] != "发布后故障调查" ||
		detail["subtitle"] != "调查 · dev" || detail["href"] != "/canvases" || detail["status"] != nil ||
		len(detail["metrics"].([]any)) != 0 || len(detail["related"].([]any)) != 0 {
		t.Fatalf("preview must use verified fields only: %s", preview.Body.String())
	}
	for _, id := range []string{hiddenID, "33333333333333333333333333333333", "44444444444444444444444444444444", "absent"} {
		w := request(teamToken, "/api/v1/mentions/investigation/"+id+"/preview", "dev")
		if w.Code != 404 {
			t.Fatalf("inaccessible preview %s: status=%d body=%s", id, w.Code, w.Body.String())
		}
	}
	if w := request("", "/api/v1/mentions?kind=investigation", "dev"); w.Code != 401 {
		t.Fatalf("anonymous mentions: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='discovery-team' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	if got := checkItems(request(teamToken, "/api/v1/mentions?kind=investigation&q=fabu", "dev"), "items"); len(got) != 0 {
		t.Fatalf("revoked team grant still visible: %+v", got)
	}
	if w := request(teamToken, "/api/v1/mentions/investigation/"+visibleID+"/preview", "dev"); w.Code == 200 {
		t.Fatalf("revoked preview still visible: %s", w.Body.String())
	}
	if _, err := db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='discovery-app' AND module='members'`); err != nil {
		t.Fatal(err)
	}
	if got := checkItems(request(appToken, "/api/v1/mentions?kind=person&q=zhangming", "dev"), "items"); len(got) != 0 {
		t.Fatalf("revoked member reader still sees people: %+v", got)
	}
	if w := request(appToken, "/api/v1/mentions/person/person-visible/preview", "dev"); w.Code != 404 {
		t.Fatalf("revoked person preview: status=%d body=%s", w.Code, w.Body.String())
	}
}
