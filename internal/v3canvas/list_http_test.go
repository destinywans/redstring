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

func TestCanvasListIncludesOnlyVisiblePersistedInvestigations(t *testing.T) {
	// The legacy store generates 32-hex investigation IDs, not INV-* aliases.
	const visibleID = "11111111111111111111111111111111"
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
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
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
	if _, err = db.Exec(ctx, `INSERT INTO v3_canvases(id,owner_id,environment_id,title,kind) VALUES ('CV-owner',$1,'dev','Private plan','plan')`, owner.ID); err != nil {
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
	request := func(token, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("X-AIOps-Environment", "dev")
		r.AddCookie(&http.Cookie{Name: "aiops_session", Value: token})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
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
	if !strings.Contains(appResponse.Body.String(), `"participants":null`) {
		t.Fatalf("unknown investigation participants were projected as known: %s", appResponse.Body.String())
	}
	visible := decode(appResponse)
	if visible.Total != 1 || visible.Counts["all"] != 1 || visible.Counts["investigation"] != 1 ||
		len(visible.Items) != 1 || visible.Items[0].ID != visibleID || visible.Items[0].Status != "active" ||
		visible.Items[0].Href != "/canvas/"+visibleID || visible.Items[0].InvestigationID != visibleID ||
		visible.Items[0].Meta != "2 个节点" || len(visible.Items[0].Thumb.Nodes) != 2 || len(visible.Items[0].Thumb.Edges) != 1 {
		t.Fatalf("incorrect investigation projection: %+v", visible)
	}
	teamVisible := decode(request(teamToken, "/api/v1/canvases?kind=investigation"))
	if teamVisible.Total != 1 || len(teamVisible.Items) != 1 || teamVisible.Items[0].ID != visibleID {
		t.Fatalf("team grant exposed an unrelated application or hid its own: %+v", teamVisible)
	}
	if _, err = db.Exec(ctx, `UPDATE applications SET owner_team_id='team-hidden' WHERE id='app-visible'`); err != nil {
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
	if ownerList.Total != 3 || ownerList.Counts["investigation"] != 2 || ownerList.Counts["plan"] != 1 || len(ownerList.Items) != 1 {
		t.Fatalf("incorrect combined pagination: %+v", ownerList)
	}
	closed := decode(request(ownerToken, "/api/v1/canvases?kind=investigation&q=Hidden"))
	if closed.Total != 1 || len(closed.Items) != 1 || closed.Items[0].Status != "resolved" {
		t.Fatalf("closed investigation mapping is incorrect: %+v", closed)
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
