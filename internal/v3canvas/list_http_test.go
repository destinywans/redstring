package v3canvas

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/destinywans/redstring/internal/core"
	"github.com/destinywans/redstring/internal/httpapi"
	"github.com/destinywans/redstring/internal/rbac"
	"github.com/destinywans/redstring/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCanvasListIncludesOnlyVisiblePersistedInvestigations(t *testing.T) {
	// The legacy store generates 32-hex investigation IDs, not INV-* aliases.
	const visibleID = "11111111111111111111111111111111"
	const createdID = "33333333333333333333333333333333"
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required; creates and drops one isolated schema")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "v3_canvas_list_" + store.ID()
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
	cfg.ConnConfig.RuntimeParams["application_name"] = schema
	db, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := &store.Store{DB: db}
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var indexed bool
	if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema()
 AND tablename='branches' AND indexname='branches_investigation_id_id')`).Scan(&indexed); err != nil || !indexed {
		t.Fatalf("investigation branch list index unavailable: %v", err)
	}
	if err = st.Bootstrap(ctx, "admin@example.invalid", "isolated-admin-password"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM v3_role_grants WHERE role_id='viewer'`,
		`INSERT INTO rbac_roles(id,name) VALUES ('canvas-owner','Canvas Owner'),('canvas-app-reader','Canvas App Reader'),('canvas-team-reader','Canvas Team Reader')`,
		`INSERT INTO v3_role_grants(role_id,module,action,scope_kind,scope_id) VALUES
 ('canvas-owner','environment','view','environment','dev'),
 ('canvas-owner','canvas','view','environment','dev'),
 ('canvas-owner','environment','view','environment','staging'),
 ('canvas-owner','canvas','view','environment','staging'),
 ('canvas-app-reader','environment','view','environment','dev'),
	('canvas-app-reader','canvas','view','application','app-visible'),
	('canvas-team-reader','environment','view','environment','dev'),
	('canvas-team-reader','canvas','view','team','team-visible')`,
		`INSERT INTO teams(id,name) VALUES ('team-visible','Visible Team'),('team-hidden','Hidden Team')`,
		`INSERT INTO applications(id,name,key,owner_team_id) VALUES
 ('app-visible','Visible','app-visible','team-visible'),
 ('app-hidden','Hidden','app-hidden','team-hidden')`,
		`INSERT INTO application_environments(id,application_id,key,name) VALUES
 ('ae-visible','app-visible','dev','Development'),('ae-hidden','app-hidden','dev','Development')`,
		`INSERT INTO v3_application_environments(application_id,environment_id) VALUES
 ('app-visible','dev'),('app-hidden','dev')`,
		`INSERT INTO investigations(id,application_id,title,status,environment_id) VALUES
	('11111111111111111111111111111111','app-visible','Visible investigation','open','ae-visible'),
	('22222222222222222222222222222222','app-hidden','Hidden investigation','closed','ae-hidden')`,
		`INSERT INTO branches(id,investigation_id,title) VALUES
	('root-visible','11111111111111111111111111111111','Main'),
	('child-visible','11111111111111111111111111111111','Branch'),
	('root-hidden','22222222222222222222222222222222','Main')`,
		`UPDATE branches SET parent_id='root-visible' WHERE id='child-visible'`,
	} {
		if _, err = db.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := st.CreateUser(ctx, core.User{Email: "owner@example.invalid", Name: "Owner", Role: "viewer"}, "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := st.CreateUser(ctx, core.User{Email: "reader@example.invalid", Name: "Reader", Role: "viewer"}, "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	teamReader, err := st.CreateUser(ctx, core.User{Email: "team-reader@example.invalid", Name: "Team Reader", Role: "viewer"}, "isolated-team-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO rbac_user_roles(user_id,role_id) VALUES ($1,'canvas-owner'),($2,'canvas-app-reader'),($3,'canvas-team-reader')`, owner.ID, reader.ID, teamReader.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO investigations(id,application_id,title,status,environment_id,created_by)
 VALUES ($1,'app-visible','Created investigation','open','ae-visible',$2)`, createdID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `INSERT INTO branches(id,investigation_id,title) VALUES ('root-created',$1,'Main')`, createdID); err != nil {
		t.Fatal(err)
	}
	planNodes := []Node{{ID: "plan-root", X: 8, Y: 12, W: 120, Tone: "info"}, {ID: "plan-next", X: 150, Y: 12, W: 120, Tone: "ok"}}
	planEdges := []Edge{{From: "plan-root", To: "plan-next"}}
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind,nodes,edges) VALUES
 ('CV-owner',$1,'dev','Private plan','plan',$2::jsonb,$3::jsonb),
 ('CV-stage',$1,'staging','Staging plan','plan','[]'::jsonb,'[]'::jsonb)`, owner.ID, mustJSON(planNodes), mustJSON(planEdges)); err != nil {
		t.Fatal(err)
	}
	_, readerToken, err := st.Login(ctx, "reader@example.invalid", "isolated-reader-password")
	if err != nil {
		t.Fatal(err)
	}
	_, ownerToken, err := st.Login(ctx, "owner@example.invalid", "isolated-owner-password")
	if err != nil {
		t.Fatal(err)
	}
	_, teamToken, err := st.Login(ctx, "team-reader@example.invalid", "isolated-team-reader-password")
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
	requestInEnv := func(token, environment, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", environment)
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	request := func(token, path string) *httptest.ResponseRecorder {
		return requestInEnv(token, "dev", path)
	}
	decode := func(w *httptest.ResponseRecorder) struct {
		Items  []summary      `json:"items"`
		Counts map[string]int `json:"counts"`
		Total  int            `json:"total"`
	} {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var result struct {
			Items  []summary      `json:"items"`
			Counts map[string]int `json:"counts"`
			Total  int            `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	appResponse := request(readerToken, "/api/v1/canvases?kind=investigation")
	visible := decode(appResponse)
	if visible.Total != 2 || visible.Counts["all"] != 2 || visible.Counts["investigation"] != 2 || len(visible.Items) != 2 {
		t.Fatalf("incorrect investigation projection: %+v", visible)
	}
	byID := map[string]summary{}
	for _, item := range visible.Items {
		byID[item.ID] = item
	}
	legacy := byID[visibleID]
	if legacy.ID != visibleID || legacy.Status != "active" || legacy.Href != "/canvas/"+visibleID ||
		legacy.InvestigationID != visibleID || legacy.Meta != "2 个节点" ||
		len(legacy.Thumb.Nodes) != 2 || len(legacy.Thumb.Edges) != 1 ||
		legacy.Participants == nil || len(*legacy.Participants) != 0 ||
		strings.Contains(appResponse.Body.String(), `"participants":null`) {
		t.Fatalf("legacy investigation must have an empty participants array: %s", appResponse.Body.String())
	}
	created := byID[createdID]
	if created.ID != createdID || created.Participants == nil || len(*created.Participants) != 1 ||
		(*created.Participants)[0] != "Owner" || created.Meta != "1 个节点" {
		t.Fatalf("persisted creator was not projected from current user: %+v", created)
	}
	filtered := decode(request(readerToken, "/api/v1/canvases?participant=Owner&participant=Nobody&status=active&page_size=1"))
	if filtered.Total != 1 || filtered.Counts["all"] != 1 || len(filtered.Items) != 1 || filtered.Items[0].ID != createdID {
		t.Fatalf("facets must precede count/page: %+v", filtered)
	}
	pageOne := decode(request(readerToken, "/api/v1/canvases?kind=investigation&page_size=1&page=1"))
	pageTwo := decode(request(readerToken, "/api/v1/canvases?kind=investigation&page_size=1&page=2"))
	if pageOne.Total != 2 || pageTwo.Total != 2 || pageOne.Counts["all"] != 2 || pageTwo.Counts["all"] != 2 ||
		len(pageOne.Items) != 1 || len(pageTwo.Items) != 1 || pageOne.Items[0].ID == pageTwo.Items[0].ID {
		t.Fatalf("investigation paging/counts do not reflect persisted rows: page1=%+v page2=%+v", pageOne, pageTwo)
	}
	if _, err = db.Exec(ctx, `UPDATE users SET name='Owner Renamed' WHERE id=$1`, owner.ID); err != nil {
		t.Fatal(err)
	}
	renamed := decode(request(readerToken, "/api/v1/canvases?kind=investigation&q=Created"))
	if renamed.Total != 1 || len(renamed.Items) != 1 || renamed.Items[0].ID != createdID ||
		renamed.Items[0].Participants == nil || len(*renamed.Items[0].Participants) != 1 ||
		(*renamed.Items[0].Participants)[0] != "Owner Renamed" {
		t.Fatalf("list did not resolve the creator's current name: %+v", renamed)
	}
	teamVisible := decode(request(teamToken, "/api/v1/canvases?kind=investigation"))
	if teamVisible.Total != 2 || len(teamVisible.Items) != 2 ||
		teamVisible.Items[0].ID == "22222222222222222222222222222222" || teamVisible.Items[1].ID == "22222222222222222222222222222222" {
		t.Fatalf("team grant exposed an unrelated application or hid its own: %+v", teamVisible)
	}
	// Hold the page's branch read after the authorization sources have been
	// locked. A concurrent team reassignment must wait until this response has
	// finished; the following request must then observe the revoked team.
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(ctx, `LOCK TABLE branches IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	reading := make(chan *httptest.ResponseRecorder, 1)
	go func() { reading <- request(teamToken, "/api/v1/canvases?kind=investigation") }()
	waitForLock := func(pattern string) {
		t.Helper()
		for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			var waiting bool
			if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
				WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE $2)`, schema, pattern).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				return
			}
		}
		t.Fatalf("canvas request did not wait for lock: %s", pattern)
	}
	waitForLock("WITH visible AS (%")
	reassigned := make(chan error, 1)
	go func() {
		_, updateErr := db.Exec(ctx, `UPDATE applications SET owner_team_id='team-hidden' WHERE id='app-visible'`)
		reassigned <- updateErr
	}()
	waitForLock("UPDATE applications SET owner_team_id=%")
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	concurrentRead := decode(<-reading)
	if concurrentRead.Total != 2 || len(concurrentRead.Items) != 2 {
		t.Fatalf("authorized snapshot was cut short during reassignment: %+v", concurrentRead)
	}
	if err = <-reassigned; err != nil {
		t.Fatal(err)
	}
	teamRevoked := decode(request(teamToken, "/api/v1/canvases?kind=investigation"))
	if teamRevoked.Total != 0 || len(teamRevoked.Items) != 0 {
		t.Fatalf("team grant ignored the persisted owner team: %+v", teamRevoked)
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-visible' WHERE id='app-visible'`); err != nil {
		t.Fatal(err)
	}
	ownerList := decode(request(ownerToken, "/api/v1/canvases?page_size=1"))
	if ownerList.Total != 4 || ownerList.Counts["investigation"] != 3 || ownerList.Counts["plan"] != 1 || len(ownerList.Items) != 1 {
		t.Fatalf("incorrect combined pagination: %+v", ownerList)
	}
	plans := decode(request(ownerToken, "/api/v1/canvases?kind=plan"))
	if plans.Total != 1 || len(plans.Items) != 1 || plans.Items[0].ID != "CV-owner" ||
		len(plans.Items[0].Thumb.Nodes) != 2 || len(plans.Items[0].Thumb.Edges) != 1 || plans.Items[0].Thumb.Edges[0] == "" {
		t.Fatalf("generic thumbnail omitted persisted edge: %+v", plans)
	}
	staging := decode(requestInEnv(ownerToken, "staging", "/api/v1/canvases"))
	if staging.Total != 1 || staging.Counts["plan"] != 1 || len(staging.Items) != 1 || staging.Items[0].ID != "CV-stage" {
		t.Fatalf("selected environment leaked dev canvases: %+v", staging)
	}
	all := decode(requestInEnv(ownerToken, "all", "/api/v1/canvases"))
	if all.Total != 5 || all.Counts["investigation"] != 3 || all.Counts["plan"] != 2 || len(all.Items) != 5 {
		t.Fatalf("all scope did not return exactly the authorized environments: %+v", all)
	}
	closed := decode(request(ownerToken, "/api/v1/canvases?kind=investigation&q=Hidden"))
	if closed.Total != 1 || len(closed.Items) != 1 || closed.Items[0].Status != "resolved" {
		t.Fatalf("closed investigation mapping is incorrect: %+v", closed)
	}
	// The preview is bounded, but root and its connected children must survive
	// even when random child IDs sort before the root ID.
	if _, err = db.Exec(ctx, `INSERT INTO branches(id,investigation_id,parent_id,title)
 SELECT 'a-child-'||n,$1,'root-visible','Branch' FROM generate_series(1,9) n`, visibleID); err != nil {
		t.Fatal(err)
	}
	branched := decode(request(readerToken, "/api/v1/canvases?kind=investigation&q=Visible"))
	if branched.Total != 1 || len(branched.Items) != 1 ||
		len(branched.Items[0].Thumb.Nodes) != 8 || len(branched.Items[0].Thumb.Edges) != 7 {
		t.Fatalf("large investigation preview lost root or connected children: %+v", branched)
	}
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='unknown' WHERE id=$1`, visibleID); err != nil {
		t.Fatal(err)
	}
	failed := request(readerToken, "/api/v1/canvases")
	if failed.Code != 503 || !strings.Contains(failed.Body.String(), `"code":"canvas_unavailable"`) {
		t.Fatalf("unknown status was projected as known: %d %s", failed.Code, failed.Body.String())
	}
	if _, err = db.Exec(ctx, `UPDATE investigations SET status='open' WHERE id=$1`, visibleID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM v3_role_grants WHERE role_id='canvas-app-reader' AND module='canvas'`); err != nil {
		t.Fatal(err)
	}
	revoked := decode(request(readerToken, "/api/v1/canvases"))
	if revoked.Total != 0 || revoked.Counts["investigation"] != 0 || len(revoked.Items) != 0 {
		t.Fatalf("revoked investigation remained visible: %+v", revoked)
	}
}
